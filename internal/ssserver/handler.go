package ssserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	sgauth "github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
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
	if err := m.usage.checkActive(clientID, time.Now()); err != nil {
		return err
	}
	sess, err := m.registry.acquire(clientID, h.inboundID, sourceIP(metadata.Source), m.usage.ipLimit(clientID), maxSessionsPerClient)
	if err != nil {
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
		return err
	}
	remote, err := m.currentDialer().DialContext(ctx, "tcp", target.String())
	if err != nil {
		return fmt.Errorf("dial %s: %w", target, err)
	}
	defer remote.Close()
	m.registry.onClose(sess, func() { remote.Close() })

	h.relay(conn, remote, clientID)
	return nil
}

func (h *handler) relay(client, remote net.Conn, clientID int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.copyAndCount(remote, client, clientID, true)
	}()
	go func() {
		defer wg.Done()
		h.copyAndCount(client, remote, clientID, false)
	}()
	wg.Wait()
}

func (h *handler) copyAndCount(dst, src net.Conn, clientID int64, up bool) {
	bufp := relayBufferPool.Get().(*[]byte)
	defer relayBufferPool.Put(bufp)
	writer := &countingWriter{
		w:   dst,
		add: func(n int64) { h.manager.usage.add(clientID, up, n) },
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
	if err := m.usage.checkActive(clientID, time.Now()); err != nil {
		return err
	}
	sess, err := m.registry.acquire(clientID, h.inboundID, sourceIP(metadata.Source), m.usage.ipLimit(clientID), maxSessionsPerClient)
	if err != nil {
		return err
	}
	defer m.registry.release(sess)

	s := newUDPClientSession(ctx, m, conn, clientID)
	m.registry.onClose(sess, s.close)
	defer s.close()

	for {
		buffer := buf.NewSize(maxUDPPacketSize)
		dest, err := conn.ReadPacket(buffer)
		if err != nil {
			buffer.Release()
			return nil
		}
		if buffer.Len() == 0 {
			buffer.Release()
			continue
		}
		target, err := m.currentPolicy().resolve(ctx, dest)
		if err != nil {
			buffer.Release()
			m.logger.Debug("udp destination rejected", "client_id", clientID,
				"destination", dest.String(), "error", err)
			continue
		}
		// WriteToUDPAddrPort copies the payload into the kernel buffer
		// synchronously, so the pooled buffer can be handed over without an
		// intermediate copy. Release happens right after the write.
		n := buffer.Len()
		err = s.send(target, buffer.Bytes())
		buffer.Release()
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
	ctx      context.Context
	m        *Manager
	client   N.PacketConn
	clientID int64

	mu      sync.Mutex
	sockets map[bool]*net.UDPConn // keyed by isIPv4
	allowed *destinationSet
	closed  bool
	wg      sync.WaitGroup
}

func newUDPClientSession(ctx context.Context, m *Manager, client N.PacketConn, clientID int64) *udpClientSession {
	return &udpClientSession{
		ctx:      ctx,
		m:        m,
		client:   client,
		clientID: clientID,
		sockets:  make(map[bool]*net.UDPConn),
		allowed:  newDestinationSet(maxDestinations),
	}
}

func (s *udpClientSession) send(target netip.AddrPort, payload []byte) error {
	conn, err := s.socket(target.Addr().Is4())
	if err != nil {
		return err
	}
	s.allowed.add(target)
	_, err = conn.WriteToUDPAddrPort(payload, target)
	return err
}

func (s *udpClientSession) socket(ipv4 bool) (*net.UDPConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, net.ErrClosed
	}
	if conn := s.sockets[ipv4]; conn != nil {
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
	s.sockets[ipv4] = conn
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
	for _, conn := range s.sockets {
		conns = append(conns, conn)
	}
	s.sockets = make(map[bool]*net.UDPConn)
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
