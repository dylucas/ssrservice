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
	// snapshot is replaced on every configuration change and read by the data
	// plane (accept loop, connection handlers, UDP loop). It is atomically
	// swapped so hot reloads never race with in-flight traffic.
	snapshot atomic.Pointer[InboundSnapshot]

	tcpListener net.Listener
	// udpConn is always a *net.UDPConn so the data plane can use the
	// netip.AddrPort read/write methods and avoid a net.Addr allocation per
	// packet.
	udpConn *net.UDPConn

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
	// sig fingerprints everything buildProtocol consumes (method, server PSK
	// and the enabled users and their PSKs). A reload that only changes
	// non-protocol fields (quota, expiry, note, IP limit) keeps the existing
	// service instead of rebuilding the user table and ciphers.
	sig [32]byte
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

// view returns a copy of the current runtime configuration.
func (rt *inboundRuntime) view() InboundSnapshot {
	if snap := rt.snapshot.Load(); snap != nil {
		return *snap
	}
	return InboundSnapshot{}
}

// setSnapshot atomically publishes a new runtime configuration.
func (rt *inboundRuntime) setSnapshot(snap InboundSnapshot) {
	rt.snapshot.Store(&snap)
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

	// applyMu serializes inbound configuration changes so two concurrent
	// panel requests cannot bind the same port or interleave listener swaps.
	applyMu sync.Mutex

	// flushReq asks the flush loop to persist the counters now instead of on the
	// next tick. It is buffered with capacity one: a burst of quota violations
	// coalesces into a single immediate flush.
	flushReq chan struct{}

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
		flushReq:  make(chan struct{}, 1),
		stop:      make(chan struct{}),
		startedAt: time.Now(),
	}
	m.usage = newUsageManager(opts.Sink, opts.Location, m.onClientExceeded)
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
// kept alive; new connections use the new configuration. A disabled inbound is
// stopped by applyLocked itself, including its listener and live sessions.
func (m *Manager) ApplyInbound(in model.Inbound, clients []model.Client) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.applyLocked(snapshotOf(in, clients))
}

// apply applies a new inbound configuration. It serializes with every other
// configuration change.
func (m *Manager) apply(snap InboundSnapshot) error {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	return m.applyLocked(snap)
}

// applyLocked is apply; the caller must hold applyMu.
func (m *Manager) applyLocked(snap InboundSnapshot) error {
	m.mu.Lock()
	old := m.inbounds[snap.ID]
	if old != nil && old.isClosed() {
		old = nil
		delete(m.inbounds, snap.ID)
	}
	m.mu.Unlock()

	// A disabled inbound is handled first: it must stop listening and drop its
	// sessions even when the listen address did not change, so no caller can
	// leave a disabled inbound serving traffic.
	if !snap.Enabled {
		idle := &inboundRuntime{}
		idle.setSnapshot(snap)
		m.mu.Lock()
		m.inbounds[snap.ID] = idle
		m.mu.Unlock()
		if old != nil {
			old.close()
		}
		m.registry.closeInbound(snap.ID)
		return nil
	}

	if old != nil {
		current := old.view()
		if current.Listen == snap.Listen && current.Port == snap.Port && old.tcpListener != nil {
			return m.reconfigure(old, snap)
		}
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
// adds or removes the UDP socket when that flag changed. A reload that does not
// touch anything buildProtocol consumes keeps the current service.
func (m *Manager) reconfigure(rt *inboundRuntime, snap InboundSnapshot) error {
	rt.mu.Lock()
	closed := rt.closed
	rt.mu.Unlock()
	if closed {
		return m.applyLocked(snap)
	}

	sig := protocolSignature(snap)

	var (
		service   protocolService
		defaultID int64
	)
	// The existing service is reused when the protocol inputs did not change.
	// The snapshot is still published below, so quota, expiry and enabled
	// flags reach the accounting gates immediately.
	rebuild := true
	if current := rt.services.Load(); current != nil && current.sig == sig {
		rebuild = false
	}

	if rebuild {
		handler := &handler{manager: m, inboundID: snap.ID, method: snap.Method}
		var err error
		service, defaultID, err = buildProtocol(snap, handler, m.opts.UDPTimeout)
		if err != nil {
			return err
		}
		handler.defaultClientID = defaultID
	}

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

	// Seed the accounting state before publishing the new service so a client
	// added by this reload cannot be served before it has an account.
	m.usage.ensureClients(snap.Clients)
	if rebuild {
		rt.services.Store(&ssService{service: service, defaultClientID: defaultID, sig: sig})
	}
	rt.setSnapshot(snap)
	m.logger.Info("inbound reloaded", "id", snap.ID, "method", snap.Method,
		"udp", snap.UDPEnabled, "users", enabledClientCount(snap.Clients),
		"protocol_rebuilt", rebuild)
	return nil
}

func (rt *inboundRuntime) currentUDP() *net.UDPConn {
	rt.udpMu.Lock()
	defer rt.udpMu.Unlock()
	return rt.udpConn
}

func (m *Manager) bindUDP(rt *inboundRuntime) error {
	udpAddr, err := net.ResolveUDPAddr("udp", rt.view().Addr())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAddr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return classifyListenError(err)
	}
	server := &udpServerConn{conn: conn}
	rt.udpMu.Lock()
	rt.udpConn = conn
	done := make(chan struct{})
	rt.udpDone = done
	ctx := rt.serveCtx
	rt.udpMu.Unlock()
	m.startUDPLoop(ctx, rt, conn, server, done)
	return nil
}

func (m *Manager) buildRuntime(snap InboundSnapshot) (*inboundRuntime, error) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &inboundRuntime{serveCtx: ctx, serveCancel: cancel}
	rt.setSnapshot(snap)

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
	rt.services.Store(&ssService{service: service, defaultClientID: defaultID, sig: protocolSignature(snap)})

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
		m.startUDPLoop(ctx, rt, rt.udpConn, &udpServerConn{conn: rt.udpConn}, done)
	}

	m.logger.Info("inbound listening", "id", snap.ID, "remark", snap.Remark,
		"address", snap.Addr(), "method", snap.Method, "udp", snap.UDPEnabled,
		"users", enabledClientCount(snap.Clients))
	return rt, nil
}

func (m *Manager) startUDPLoop(ctx context.Context, rt *inboundRuntime, conn *net.UDPConn, server *udpServerConn, done chan struct{}) {
	m.stopWG.Add(1)
	go func() {
		defer m.stopWG.Done()
		defer close(done)
		m.serveUDP(ctx, rt, conn, server)
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
			m.logger.Warn("accept failed", "inbound_id", rt.view().ID, "error", err)
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
		m.logger.Debug("connection failed", "inbound_id", rt.view().ID,
			"source", metadata.Source.String(), "error", err)
	}
	conn.Close()
}

func (m *Manager) serveUDP(ctx context.Context, rt *inboundRuntime, conn *net.UDPConn, server *udpServerConn) {
	readBuf := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := conn.ReadFromUDPAddrPort(readBuf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			m.logger.Warn("udp read failed", "inbound_id", rt.view().ID, "error", err)
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
		metadata := M.Metadata{Source: M.SocksaddrFromNetIP(addr)}
		if err := svc.service.NewPacket(ctx, server, packet, metadata); err != nil {
			packet.Release()
			m.logger.Debug("udp packet rejected", "inbound_id", rt.view().ID,
				"source", metadata.Source.String(), "error", err)
		}
	}
}

// RemoveInbound stops an inbound and forgets it.
func (m *Manager) RemoveInbound(id int64) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	rt := m.inbounds[id]
	delete(m.inbounds, id)
	m.mu.Unlock()
	if rt != nil {
		rt.close()
	}
	m.registry.closeInbound(id)
}

// flushLoop periodically persists traffic counters and also serves the
// on-demand flush requests queued by onClientExceeded.
func (m *Manager) flushLoop() {
	defer m.stopWG.Done()
	ticker := time.NewTicker(m.opts.FlushInterval)
	defer ticker.Stop()
	flush := func() {
		if err := m.usage.flush(); err != nil {
			m.logger.Warn("flush usage failed", "error", err)
		}
	}
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			flush()
		case <-m.flushReq:
			flush()
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

// onClientExceeded runs when a client crosses its quota: it disconnects every
// session of that client and asks the flush loop to persist the counters now,
// so the panel and the database see the final numbers immediately instead of
// waiting for the next flush tick. The request is dropped while a flush is
// already pending, and it never blocks the data plane.
func (m *Manager) onClientExceeded(clientID int64) {
	m.registry.closeClient(clientID)
	select {
	case m.flushReq <- struct{}{}:
	default:
	}
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
	m.usage.reset(clientID)
}

// ClientTraffic returns the live counters of a client.
func (m *Manager) ClientTraffic(clientID int64) (up, down int64) {
	return m.usage.totals(clientID)
}

// TotalTraffic returns the sum of the live counters of every client. It reads
// memory only, so the dashboard can poll it without scanning the database.
func (m *Manager) TotalTraffic() (up, down int64) {
	return m.usage.totalsAll()
}

// SetLocation changes the accounting time zone used for daily buckets and
// monthly resets. It takes effect on the next accounting call.
func (m *Manager) SetLocation(location *time.Location) {
	m.usage.setLocation(location)
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
	conn *net.UDPConn
}

var _ N.PacketConn = (*udpServerConn)(nil)

func (c *udpServerConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (c *udpServerConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	// The common case is an IP destination: write through netip.AddrPort so no
	// *net.UDPAddr is allocated per packet. Domain names (never produced by the
	// server packet writer, but kept for safety) fall back to WriteTo.
	if ap, ok := udpDestination(destination); ok {
		_, err := c.conn.WriteToUDPAddrPort(buffer.Bytes(), ap)
		return err
	}
	_, err := c.conn.WriteTo(buffer.Bytes(), destination.UDPAddr())
	return err
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
