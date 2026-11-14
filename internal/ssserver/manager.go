package ssserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/ydong/ssrservice/internal/model"
)

// Listener error kinds exposed to the panel.
var (
	ErrPortInUse   = errors.New("port already in use")
	ErrPermission  = errors.New("permission denied binding port")
	ErrInvalidAddr = errors.New("invalid listen address")
)

// inboundRuntime is one running inbound: its listeners, protocol service and
// UDP socket.
type inboundRuntime struct {
	snapshot InboundSnapshot

	tcpListener net.Listener
	udpConn     net.PacketConn
	udpServer   *udpServerConn

	services atomic.Pointer[ssService]

	serveCtx    context.Context
	serveCancel context.CancelFunc
	udpMu       sync.Mutex
	udpDone     chan struct{}

	mu     sync.Mutex
	closed bool
}

type ssService struct {
	service         protocolService
	defaultClientID int64
}

func (rt *inboundRuntime) close() {
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return
	}
	rt.closed = true
	rt.mu.Unlock()
	if rt.serveCancel != nil {
		rt.serveCancel()
	}
	if rt.tcpListener != nil {
		rt.tcpListener.Close()
	}
	rt.closeUDP()
}

func (rt *inboundRuntime) isClosed() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.closed
}

func (rt *inboundRuntime) closeUDP() {
	rt.udpMu.Lock()
	conn := rt.udpConn
	done := rt.udpDone
	rt.udpConn = nil
	rt.udpServer = nil
	rt.udpDone = nil
	rt.udpMu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// Manager owns all Shadowsocks listeners and the shared accounting state.
type Manager struct {
	logger *slog.Logger
	opts   Options

	usage    *usageManager
	registry *registry

	policy atomic.Pointer[policy]
	dialer atomic.Pointer[net.Dialer]

	mu       sync.Mutex
	inbounds map[int64]*inboundRuntime

	stop     chan struct{}
	stopOnce sync.Once
	stopWG   sync.WaitGroup

	startedAt time.Time
}

// New creates a Manager. Call Load and Start afterwards.
func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = 10 * time.Second
	}
	if opts.EnforceInterval <= 0 {
		opts.EnforceInterval = 30 * time.Second
	}
	if opts.UDPTimeout <= 0 {
		opts.UDPTimeout = 60 * time.Second
	}
	if opts.Location == nil {
		opts.Location = time.Local
	}
	m := &Manager{
		logger:    opts.Logger,
		opts:      opts,
		registry:  newRegistry(),
		inbounds:  make(map[int64]*inboundRuntime),
		stop:      make(chan struct{}),
		startedAt: time.Now(),
	}
	m.usage = newUsageManager(opts.Sink, opts.Location, m.closeClient)
	m.SetPolicy(opts.Policy)
	return m
}

// StartedAt returns when the manager was created.
func (m *Manager) StartedAt() time.Time { return m.startedAt }

// SetPolicy replaces the destination policy for new connections.
func (m *Manager) SetPolicy(cfg PolicyConfig) {
	p := newPolicy(cfg)
	m.policy.Store(p)
	m.dialer.Store(&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   p.control,
	})
}

func (m *Manager) currentDialer() *net.Dialer {
	if d := m.dialer.Load(); d != nil {
		return d
	}
	return &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
}

func (m *Manager) currentPolicy() *policy { return m.policy.Load() }

// Load seeds the runtime from persisted rows and starts the enabled listeners.
func (m *Manager) Load(inbounds []model.Inbound, clients []model.Client) error {
	byInbound := make(map[int64][]model.Client, len(inbounds))
	for _, c := range clients {
		byInbound[c.InboundID] = append(byInbound[c.InboundID], c)
	}
	m.SeedUsage(clients)
	var errs []error
	for _, in := range inbounds {
		if err := m.apply(snapshotOf(in, byInbound[in.ID])); err != nil {
			errs = append(errs, fmt.Errorf("inbound %d (%s): %w", in.ID, in.Remark, err))
		}
	}
	return errors.Join(errs...)
}

// SeedUsage initializes live counters from persisted rows.
func (m *Manager) SeedUsage(clients []model.Client) {
	for _, c := range clients {
		m.usage.ensure(clientSnapshotOf(c), c.UpBytes, c.DownBytes)
	}
}

// ApplyInbound creates, updates or removes an inbound. Existing sessions are
// kept alive; new connections use the new configuration.
func (m *Manager) ApplyInbound(in model.Inbound, clients []model.Client) error {
	err := m.apply(snapshotOf(in, clients))
	if err == nil && !in.Enabled {
		m.stopInbound(in.ID)
	}
	return err
}

func (m *Manager) apply(snap InboundSnapshot) error {
	m.mu.Lock()
	old := m.inbounds[snap.ID]
	if old != nil && old.isClosed() {
		old = nil
		delete(m.inbounds, snap.ID)
	}
	m.mu.Unlock()

	if old != nil && old.snapshot.Listen == snap.Listen && old.snapshot.Port == snap.Port && old.tcpListener != nil {
		if !snap.Enabled {
			return nil
		}
		return m.reconfigure(old, snap)
	}

	if !snap.Enabled {
		m.mu.Lock()
		m.inbounds[snap.ID] = &inboundRuntime{snapshot: snap}
		m.mu.Unlock()
		if old != nil {
			old.close()
		}
		return nil
	}

	newRT, err := m.buildRuntime(snap)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.inbounds[snap.ID] = newRT
	m.mu.Unlock()
	if old != nil {
		old.close()
	}
	return nil
}

// reconfigure keeps the listeners and swaps the protocol service. It also
// adds or removes the UDP socket when that flag changed.
func (m *Manager) reconfigure(rt *inboundRuntime, snap InboundSnapshot) error {
	rt.mu.Lock()
	closed := rt.closed
	rt.mu.Unlock()
	if closed {
		return m.apply(snap)
	}

	handler := &handler{manager: m, inboundID: snap.ID, method: snap.Method}
	service, defaultID, err := buildProtocol(snap, handler, m.opts.UDPTimeout)
	if err != nil {
		return err
	}
	handler.defaultClientID = defaultID

	if rt.serveCtx == nil {
		rt.serveCtx, rt.serveCancel = context.WithCancel(context.Background())
	}
	// Bring the UDP socket in line with the new snapshot before swapping the
	// service so a bind failure aborts the update.
	if snap.UDPEnabled && rt.currentUDP() == nil {
		if err := m.bindUDP(rt); err != nil {
			return err
		}
	} else if !snap.UDPEnabled && rt.currentUDP() != nil {
		rt.closeUDP()
	}

	rt.services.Store(&ssService{service: service, defaultClientID: defaultID})

	rt.mu.Lock()
	rt.snapshot = snap
	rt.mu.Unlock()
	m.usage.ensureClients(snap.Clients)
	m.logger.Info("inbound reloaded", "id", snap.ID, "method", snap.Method,
		"udp", snap.UDPEnabled, "users", len(snap.Clients))
	return nil
}

func (rt *inboundRuntime) currentUDP() net.PacketConn {
	rt.udpMu.Lock()
	defer rt.udpMu.Unlock()
	return rt.udpConn
}

func (m *Manager) bindUDP(rt *inboundRuntime) error {
	udpAddr, err := net.ResolveUDPAddr("udp", rt.snapshot.Addr())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAddr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return classifyListenError(err)
	}
	rt.udpMu.Lock()
	rt.udpConn = conn
	rt.udpServer = &udpServerConn{conn: conn}
	done := make(chan struct{})
	rt.udpDone = done
	ctx := rt.serveCtx
	rt.udpMu.Unlock()
	m.startUDPLoop(ctx, rt, conn, done)
	return nil
}

func (m *Manager) buildRuntime(snap InboundSnapshot) (*inboundRuntime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &inboundRuntime{snapshot: snap, serveCtx: ctx, serveCancel: cancel}

	handler := &handler{manager: m, inboundID: snap.ID, method: snap.Method}
	service, defaultID, err := buildProtocol(snap, handler, m.opts.UDPTimeout)
	if err != nil {
		cancel()
		return nil, err
	}
	handler.defaultClientID = defaultID

	tcpAddr, err := net.ResolveTCPAddr("tcp", snap.Addr())
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrInvalidAddr, err)
	}
	ln, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		cancel()
		return nil, classifyListenError(err)
	}
	rt.tcpListener = ln
	rt.services.Store(&ssService{service: service, defaultClientID: defaultID})

	if snap.UDPEnabled {
		udpAddr, err := net.ResolveUDPAddr("udp", snap.Addr())
		if err != nil {
			ln.Close()
			cancel()
			return nil, fmt.Errorf("%w: %v", ErrInvalidAddr, err)
		}
		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			ln.Close()
			cancel()
			return nil, classifyListenError(err)
		}
		rt.udpConn = udpConn
		rt.udpServer = &udpServerConn{conn: udpConn}
	}

	m.usage.ensureClients(snap.Clients)

	m.stopWG.Add(1)
	go func() {
		defer m.stopWG.Done()
		m.serveTCP(ctx, rt)
	}()
	if rt.udpConn != nil {
		done := make(chan struct{})
		rt.udpDone = done
		m.startUDPLoop(ctx, rt, rt.udpConn, done)
	}

	m.logger.Info("inbound listening", "id", snap.ID, "remark", snap.Remark,
		"address", snap.Addr(), "method", snap.Method, "udp", snap.UDPEnabled,
		"users", len(snap.Clients))
	return rt, nil
}

func (m *Manager) startUDPLoop(ctx context.Context, rt *inboundRuntime, conn net.PacketConn, done chan struct{}) {
	m.stopWG.Add(1)
	go func() {
		defer m.stopWG.Done()
		defer close(done)
		m.serveUDP(ctx, rt, conn)
	}()
}

func (m *Manager) serveTCP(ctx context.Context, rt *inboundRuntime) {
	for {
		conn, err := rt.tcpListener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			m.logger.Warn("accept failed", "inbound_id", rt.snapshot.ID, "error", err)
			continue
		}
		go m.handleTCP(ctx, rt, conn)
	}
}

func (m *Manager) handleTCP(ctx context.Context, rt *inboundRuntime, conn net.Conn) {
	svc := rt.services.Load()
	if svc == nil {
		conn.Close()
		return
	}
	// Bound the handshake so idle connections cannot pile up.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	metadata := M.Metadata{Source: M.SocksaddrFromNet(conn.RemoteAddr())}
	sessionCtx := context.WithValue(ctx, rawConnKey{}, conn)
	err := svc.service.NewConnection(sessionCtx, conn, metadata)
	if err != nil {
		m.logger.Debug("connection failed", "inbound_id", rt.snapshot.ID,
			"source", metadata.Source.String(), "error", err)
	}
	conn.Close()
}

func (m *Manager) serveUDP(ctx context.Context, rt *inboundRuntime, conn net.PacketConn) {
	readBuf := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := conn.ReadFrom(readBuf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			m.logger.Warn("udp read failed", "inbound_id", rt.snapshot.ID, "error", err)
			continue
		}
		if n == 0 {
			continue
		}
		svc := rt.services.Load()
		if svc == nil {
			continue
		}
		packet := packetWithHeadroom(readBuf[:n])
		metadata := M.Metadata{Source: M.SocksaddrFromNet(addr)}
		if err := svc.service.NewPacket(ctx, rt.udpServer, packet, metadata); err != nil {
			packet.Release()
			m.logger.Debug("udp packet rejected", "inbound_id", rt.snapshot.ID,
				"source", metadata.Source.String(), "error", err)
		}
	}
}

func (m *Manager) stopInbound(id int64) {
	m.mu.Lock()
	rt := m.inbounds[id]
	m.mu.Unlock()
	if rt == nil {
		return
	}
	rt.close()
	m.registry.closeInbound(id)
}

// RemoveInbound stops an inbound and forgets it.
func (m *Manager) RemoveInbound(id int64) {
	m.mu.Lock()
	rt := m.inbounds[id]
	delete(m.inbounds, id)
	m.mu.Unlock()
	if rt != nil {
		rt.close()
	}
	m.registry.closeInbound(id)
}

// flushLoop periodically persists traffic counters.
func (m *Manager) flushLoop() {
	defer m.stopWG.Done()
	ticker := time.NewTicker(m.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			if err := m.usage.flush(); err != nil {
				m.logger.Warn("flush usage failed", "error", err)
			}
		}
	}
}

// enforceLoop closes sessions for clients that are no longer allowed to serve
// traffic.
func (m *Manager) enforceLoop() {
	defer m.stopWG.Done()
	ticker := time.NewTicker(m.opts.EnforceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			for _, clientID := range m.usage.expiredClients(now) {
				m.registry.closeClient(clientID)
			}
		}
	}
}

func (m *Manager) closeClient(clientID int64) {
	m.registry.closeClient(clientID)
}

// CloseClient disconnects all sessions of a client.
func (m *Manager) CloseClient(clientID int64) {
	m.registry.closeClient(clientID)
}

// Flush persists pending counters.
func (m *Manager) Flush() error {
	return m.usage.flush()
}

// RemoveClient forgets a client and disconnects its sessions.
func (m *Manager) RemoveClient(clientID int64) {
	m.registry.closeClient(clientID)
	m.usage.remove(clientID)
}

// ResetClientTraffic zeroes the live counters of a client.
func (m *Manager) ResetClientTraffic(clientID int64) {
	m.usage.reset(clientID, m.opts.Location)
}

// ClientTraffic returns the live counters of a client.
func (m *Manager) ClientTraffic(clientID int64) (up, down int64) {
	return m.usage.totals(clientID)
}

// OnlineIPs returns the distinct source IPs currently connected per client.
func (m *Manager) OnlineIPs() map[int64][]string {
	return m.registry.onlineIPs()
}

// SessionCount returns the number of live sessions.
func (m *Manager) SessionCount() int {
	return m.registry.sessionCount()
}

// Stop flushes counters, closes all listeners and disconnects all sessions.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stop)
		m.mu.Lock()
		inbounds := m.inbounds
		m.inbounds = make(map[int64]*inboundRuntime)
		m.mu.Unlock()
		for _, rt := range inbounds {
			rt.close()
		}
		m.registry.closeAll()
		if err := m.usage.flush(); err != nil && m.logger != nil {
			m.logger.Warn("final flush failed", "error", err)
		}
		m.stopWG.Wait()
	})
}

// udpServerConn adapts the shared UDP listener socket to the packet writer
// expected by the Shadowsocks library. Writes are directed back to the client
// address supplied by the library.
type udpServerConn struct {
	conn net.PacketConn
}

var _ N.PacketConn = (*udpServerConn)(nil)

func (c *udpServerConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (c *udpServerConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	addr := destination.UDPAddr()
	if _, err := c.conn.WriteTo(buffer.Bytes(), addr); err != nil {
		return err
	}
	return nil
}

func (c *udpServerConn) Close() error                       { return nil }
func (c *udpServerConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *udpServerConn) SetDeadline(t time.Time) error      { return nil }
func (c *udpServerConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *udpServerConn) SetWriteDeadline(t time.Time) error { return nil }

func classifyListenError(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Err != nil {
			msg := opErr.Err.Error()
			if containsAny(msg, "address already in use", "in use") {
				return fmt.Errorf("%w: %v", ErrPortInUse, err)
			}
			if containsAny(msg, "permission denied", "access denied") {
				return fmt.Errorf("%w: %v", ErrPermission, err)
			}
		}
	}
	return err
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Start launches the background flush and enforcement loops.
func (m *Manager) Start() {
	m.stopWG.Add(2)
	go m.flushLoop()
	go m.enforceLoop()
}
