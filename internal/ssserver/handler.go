package ssserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	sgauth "github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/ydong/ssrservice/internal/model"
)

const (
	maxUDPPacketSize     = 65535
	maxDestinations      = 256
	maxSessionsPerClient = 4096
)

var relayBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}

// rawConnKey carries the underlying TCP connection so sessions can be
// interrupted without calling Close on the protocol wrapper (which is not safe
// concurrently with writes in the protocol library).
type rawConnKey struct{}

// handler implements the Shadowsocks data-plane for one inbound.
type handler struct {
	manager         *Manager
	inboundID       int64
	method          string
	defaultClientID int64
}

var _ shadowsocks.Handler = (*handler)(nil)

func rawConnFromContext(ctx context.Context) net.Conn {
	raw, _ := ctx.Value(rawConnKey{}).(net.Conn)
	return raw
}

func (h *handler) clientID(ctx context.Context) int64 {
	if id, ok := sgauth.UserFromContext[uint64](ctx); ok && id != 0 {
		return int64(id)
	}
	return h.defaultClientID
}

// NewConnection handles one decrypted TCP stream.
func (h *handler) NewConnection(ctx context.Context, conn net.Conn, metadata M.Metadata) error {
	// The handshake is done at this point: lift the handshake deadline.
	_ = conn.SetDeadline(time.Time{})

	clientID := h.clientID(ctx)
	if clientID == 0 {
		return ErrNoUser
	}
	m := h.manager
	// The destination is normalized inside beginAccess, not here: when
	// recording is off it returns nil before touching the address, so the
	// disabled data path allocates nothing for analytics.
	access := m.beginAccess(clientID, h.inboundID, "tcp", metadata.Destination)
	defer access.finish()
	ipLimit, err := m.usage.authorize(clientID, time.Now())
	if err != nil {
		access.block(accessReasonFor(err))
		return err
	}
	sess, err := m.registry.acquire(clientID, h.inboundID, sourceIP(metadata.Source), ipLimit, maxSessionsPerClient)
	if err != nil {
		access.block(accessReasonFor(err))
		return err
	}
	defer m.registry.release(sess)
	raw := rawConnFromContext(ctx)
	if raw == nil {
		raw = conn
	}
	m.registry.onClose(sess, func() { raw.Close() })

	target, err := m.currentPolicy().resolve(ctx, metadata.Destination)
	if err != nil {
		access.block(policyReasonFor(err))
		return err
	}
	remote, err := m.currentDialer().DialContext(ctx, "tcp", target.String())
	if err != nil {
		access.fail(model.AccessReasonDial)
		return fmt.Errorf("dial %s: %w", target, err)
	}
	access.allowAddr(target.Addr())
	defer remote.Close()
	m.registry.onClose(sess, func() { remote.Close() })

	h.relay(conn, remote, clientID, access)
	return nil
}

func (h *handler) relay(client, remote net.Conn, clientID int64, access *accessEntry) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.copyAndCount(remote, client, clientID, true, access)
	}()
	go func() {
		defer wg.Done()
		h.copyAndCount(client, remote, clientID, false, access)
	}()
	wg.Wait()
}

func (h *handler) copyAndCount(dst, src net.Conn, clientID int64, up bool, access *accessEntry) {
	bufp := relayBufferPool.Get().(*[]byte)
	defer relayBufferPool.Put(bufp)
	writer := &countingWriter{
		w: dst,
		add: func(n int64) {
			h.manager.usage.add(clientID, up, n)
			if up {
				access.addUp(n)
			} else {
				access.addDown(n)
			}
		},
	}
	_, _ = io.CopyBuffer(writer, src, *bufp)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

type countingWriter struct {
	w   io.Writer
	add func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.add(int64(n))
	}
	return n, err
}

// NewPacketConnection handles one UDP NAT session.
func (h *handler) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata M.Metadata) error {
	clientID := h.clientID(ctx)
	if clientID == 0 {
		return ErrNoUser
	}
	m := h.manager
	// The destinations are unknown until packets arrive, so the blocked gate
	// reasons (disabled, expired, quota, IP limit) cannot name a record here.
	ipLimit, err := m.usage.authorize(clientID, time.Now())
	if err != nil {
		return err
	}
	sess, err := m.registry.acquire(clientID, h.inboundID, sourceIP(metadata.Source), ipLimit, maxSessionsPerClient)
	if err != nil {
		return err
	}
	defer m.registry.release(sess)

	s := newUDPClientSession(ctx, m, conn, clientID, h.inboundID)
	m.registry.onClose(sess, s.close)
	defer s.close()

	// One buffer serves the whole session: ReadPacket copies the payload into
	// it, the payload is written out before the next iteration, and the
	// buffer is reset in place. Acquiring a 64 KiB pooled buffer per packet
	// cost an allocation and a pool round trip on every datagram.
	buffer := buf.NewSize(maxUDPPacketSize)
	defer buffer.Release()

	for {
		buffer.Reset()
		dest, err := conn.ReadPacket(buffer)
		if err != nil {
			return nil
		}
		if buffer.Len() == 0 {
			continue
		}
		target, err := m.currentPolicy().resolve(ctx, dest)
		if err != nil {
			s.access.recordBlocked(dest, policyReasonFor(err))
			// dest.String() allocates, and a rejected stream can be one packet
			// per microsecond: only format it when the line is actually emitted.
			if m.logger.Enabled(ctx, slog.LevelDebug) {
				m.logger.DebugContext(ctx, "udp destination rejected", "client_id", clientID,
					"destination", dest.String(), "error", err)
			}
			continue
		}
		// WriteToUDPAddrPort copies the payload into the kernel buffer
		// synchronously, so the payload can be handed over without an
		// intermediate copy and the buffer reused afterwards.
		n := buffer.Len()
		err = s.send(target, dest, buffer.Bytes())
		if err != nil {
			m.logger.Debug("udp write failed", "client_id", clientID,
				"destination", target.String(), "error", err)
			continue
		}
		m.usage.add(clientID, true, int64(n))
	}
}

// NewError receives protocol errors from the Shadowsocks library.
func (h *handler) NewError(ctx context.Context, err error) {
	h.manager.logger.Debug("shadowsocks error", "inbound_id", h.inboundID, "error", err)
}

// udpClientSession owns the upstream UDP sockets of one client NAT session.
type udpClientSession struct {
	ctx       context.Context
	m         *Manager
	client    N.PacketConn
	clientID  int64
	inboundID int64
	access    *udpAccess

	// mu serializes socket creation against close. The sockets themselves are
	// kept in an atomic array so the hot send path reads them without taking
	// the mutex (index 0 = IPv4, 1 = IPv6).
	mu      sync.Mutex
	sockets [2]atomic.Pointer[net.UDPConn]
	allowed *destinationSet
	closed  bool
	wg      sync.WaitGroup
}

func newUDPClientSession(ctx context.Context, m *Manager, client N.PacketConn, clientID, inboundID int64) *udpClientSession {
	return &udpClientSession{
		ctx:       ctx,
		m:         m,
		client:    client,
		clientID:  clientID,
		inboundID: inboundID,
		access:    m.newUDPAccess(clientID, inboundID),
		allowed:   newDestinationSet(maxDestinations),
	}
}

func (s *udpClientSession) send(target netip.AddrPort, requested M.Socksaddr, payload []byte) error {
	conn, err := s.socket(target.Addr().Is4())
	if err != nil {
		return err
	}
	s.allowed.add(target)
	_, err = conn.WriteToUDPAddrPort(payload, target)
	if err == nil {
		if n := len(payload); n > 0 {
			// addUp reads the requested host itself, and returns before
			// formatting anything when this session has no tracker (recording
			// off). Only what the kernel accepted is recorded: a rejected
			// datagram never left the host and would otherwise show up as
			// transferred bytes on the access page.
			s.access.addUp(target, requested, int64(n))
		}
	}
	return err
}

// socketIndex maps the address family to the atomic socket slot.
func socketIndex(ipv4 bool) int {
	if ipv4 {
		return 0
	}
	return 1
}

func (s *udpClientSession) socket(ipv4 bool) (*net.UDPConn, error) {
	index := socketIndex(ipv4)
	if conn := s.sockets[index].Load(); conn != nil {
		return conn, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	if conn := s.sockets[index].Load(); conn != nil {
		return conn, nil
	}
	network := "udp6"
	laddr := &net.UDPAddr{IP: net.IPv6unspecified}
	if ipv4 {
		network = "udp4"
		laddr = &net.UDPAddr{IP: net.IPv4zero}
	}
	conn, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	s.sockets[index].Store(conn)
	s.wg.Add(1)
	go s.readLoop(conn)
	return conn, nil
}

func (s *udpClientSession) readLoop(conn *net.UDPConn) {
	defer s.wg.Done()
	// Reuse one buffer: packetWithHeadroom copies the payload before the
	// protocol library sees it, so subsequent reads cannot clobber in-flight
	// packets.
	readBuf := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := conn.ReadFromUDPAddrPort(readBuf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		if !s.allowed.contains(addr) {
			continue
		}
		destination := M.SocksaddrFromNetIP(addr).Unwrap()
		out := packetWithHeadroom(readBuf[:n])
		s.m.usage.add(s.clientID, false, int64(n))
		s.access.addDown(addr, int64(n))
		if err := s.client.WritePacket(out, destination); err != nil {
			out.Release()
			return
		}
	}
}

func (s *udpClientSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := make([]*net.UDPConn, 0, len(s.sockets))
	for i := range s.sockets {
		if conn := s.sockets[i].Swap(nil); conn != nil {
			conns = append(conns, conn)
		}
	}
	s.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
	// Closing the client side unblocks the pending ReadPacket in
	// NewPacketConnection immediately; otherwise the NAT session (and its
	// accounting slot) would linger until the protocol library's UDP timeout
	// expires even though the client was kicked, disabled or removed.
	_ = s.client.Close()
	s.wg.Wait()
	s.access.finishAll()
}

// accessDestination is the destination as the client asked for it: a domain
// name when one was requested, otherwise the IP literal. It is what the panel
// shows, while the resolved address is recorded separately once dialed.
type accessTarget struct {
	host string
	port int
}

func accessDestination(destination M.Socksaddr) accessTarget {
	destination = destination.Unwrap()
	if destination.Fqdn != "" {
		return accessTarget{host: destination.Fqdn, port: int(destination.Port)}
	}
	if destination.Addr.IsValid() {
		return accessTarget{host: destination.Addr.Unmap().String(), port: int(destination.Port)}
	}
	return accessTarget{}
}

func sourceIP(source M.Socksaddr) string {
	if source.Addr.IsValid() {
		return source.Addr.Unmap().String()
	}
	if source.Fqdn != "" {
		return source.Fqdn
	}
	return "unknown"
}

// udpHeadroom is reserved in front of UDP payloads for the Shadowsocks 2022
// packet header, which is prepended by the protocol library.
const (
	udpHeadroom = 1024
	udpTailroom = 256
)

// packetWithHeadroom copies payload into a buffer whose start leaves room for
// the protocol header and whose end leaves room for the AEAD tag.
func packetWithHeadroom(payload []byte) *buf.Buffer {
	buffer := buf.NewSize(udpHeadroom + len(payload) + udpTailroom)
	buffer.Resize(udpHeadroom, len(payload))
	copy(buffer.Bytes(), payload)
	return buffer
}

// udpDestination converts a Socksaddr into a dialable netip.AddrPort. Domain
// names and invalid addresses report false and take the slow path.
func udpDestination(destination M.Socksaddr) (netip.AddrPort, bool) {
	destination = destination.Unwrap()
	if !destination.Addr.IsValid() || destination.Port == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(destination.Addr, destination.Port), true
}

// destinationSet remembers remote endpoints.
type destinationSet struct {
	mu   sync.RWMutex
	max  int
	seen map[netip.AddrPort]struct{}
}

func newDestinationSet(max int) *destinationSet {
	return &destinationSet{max: max, seen: make(map[netip.AddrPort]struct{})}
}

func (d *destinationSet) add(ap netip.AddrPort) {
	// Already-seen destinations are the common case; answer them with a read
	// lock instead of taking the write lock for every packet.
	d.mu.RLock()
	_, seen := d.seen[ap]
	d.mu.RUnlock()
	if seen {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[ap]; ok {
		return
	}
	if len(d.seen) >= d.max {
		return
	}
	d.seen[ap] = struct{}{}
}

func (d *destinationSet) contains(ap netip.AddrPort) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.seen[ap]
	return ok
}
