package ssserver

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

var metadataAddresses = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"), // AWS / GCP / Azure / OpenStack
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("fd00:ec2::254"),   // AWS IPv6
}

type resolveEntry struct {
	addrs   []netip.Addr
	expires time.Time
}

type policy struct {
	allowPrivate  bool
	blockMetadata bool
	panelPort     int

	// panelAny is set when the panel listens on a wildcard address, in which
	// case every address reaches the panel port. panelAddr is the parsed
	// listen address otherwise. Both are computed once instead of on every
	// dial and UDP packet.
	panelAny  bool
	panelAddr netip.Addr

	mu       sync.Mutex
	resolved map[string]resolveEntry
}

func newPolicy(cfg PolicyConfig) *policy {
	p := &policy{
		allowPrivate:  cfg.AllowPrivateTargets,
		blockMetadata: cfg.BlockMetadata,
		panelPort:     cfg.PanelPort,
		resolved:      make(map[string]resolveEntry),
	}
	listen := cfg.PanelListen
	// Anything that is not a concrete address could resolve to more than one
	// local endpoint ("localhost" is both 127.0.0.1 and ::1), so it is treated
	// like a wildcard. Failing closed is the only safe default here: guessing
	// wrong would leave the panel reachable through the proxy for anyone who
	// enabled private targets.
	if listen == "" || listen == "0.0.0.0" || listen == "::" {
		p.panelAny = true
	} else if addr, err := netip.ParseAddr(listen); err == nil {
		p.panelAddr = addr.Unmap()
	} else {
		p.panelAny = true
	}
	return p
}

// checkAddr validates one destination address.
func (p *policy) checkAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrDestinationBlocked)
	}
	addr = addr.Unmap()
	if p.blockMetadata {
		for _, blocked := range metadataAddresses {
			if addr == blocked {
				return fmt.Errorf("%w: cloud metadata address %s", ErrDestinationBlocked, addr)
			}
		}
	}
	if p.allowPrivate {
		return nil
	}
	if addr.IsLoopback() {
		return fmt.Errorf("%w: loopback address %s", ErrDestinationBlocked, addr)
	}
	if addr.IsPrivate() {
		return fmt.Errorf("%w: private address %s", ErrDestinationBlocked, addr)
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: link-local address %s", ErrDestinationBlocked, addr)
	}
	if addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%w: multicast address %s", ErrDestinationBlocked, addr)
	}
	if addr.IsUnspecified() {
		return fmt.Errorf("%w: unspecified address", ErrDestinationBlocked)
	}
	if addr.Is4() {
		if b := addr.As4(); b[0] == 0 {
			return fmt.Errorf("%w: reserved address %s", ErrDestinationBlocked, addr)
		}
	}
	return nil
}

// checkAddrPort validates a destination and blocks the panel's own endpoint.
func (p *policy) checkAddrPort(ap netip.AddrPort) error {
	if err := p.checkAddr(ap.Addr()); err != nil {
		return err
	}
	if p.panelPort > 0 && int(ap.Port()) == p.panelPort && p.isPanelAddress(ap.Addr()) {
		return fmt.Errorf("%w: panel address", ErrDestinationBlocked)
	}
	return nil
}

func (p *policy) isPanelAddress(addr netip.Addr) bool {
	if p.panelAny {
		return true
	}
	if !p.panelAddr.IsValid() {
		return false
	}
	return p.panelAddr == addr.Unmap()
}

// resolve converts a requested destination into a dialable address after
// policy validation.
func (p *policy) resolve(ctx context.Context, dest M.Socksaddr) (netip.AddrPort, error) {
	dest = dest.Unwrap()
	if dest.Addr.IsValid() {
		ap := netip.AddrPortFrom(dest.Addr.Unmap(), dest.Port)
		if err := p.checkAddrPort(ap); err != nil {
			return netip.AddrPort{}, err
		}
		return ap, nil
	}
	if dest.Fqdn == "" || dest.Port == 0 {
		return netip.AddrPort{}, fmt.Errorf("%w: empty destination", ErrDestinationBlocked)
	}
	addrs, err := p.lookup(ctx, dest.Fqdn)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for _, addr := range addrs {
		ap := netip.AddrPortFrom(addr, dest.Port)
		if err := p.checkAddrPort(ap); err == nil {
			return ap, nil
		}
	}
	return netip.AddrPort{}, fmt.Errorf("%w: %s resolves to blocked addresses", ErrDestinationBlocked, dest.Fqdn)
}

func (p *policy) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	now := time.Now()
	p.mu.Lock()
	if entry, ok := p.resolved[host]; ok && now.Before(entry.expires) {
		addrs := entry.addrs
		p.mu.Unlock()
		return addrs, nil
	}
	p.mu.Unlock()

	resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	addrs := make([]netip.Addr, 0, len(resolved))
	for _, addr := range resolved {
		addrs = append(addrs, addr.Unmap())
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	p.mu.Lock()
	if len(p.resolved) > 2048 {
		p.resolved = make(map[string]resolveEntry)
	}
	p.resolved[host] = resolveEntry{addrs: addrs, expires: now.Add(60 * time.Second)}
	p.mu.Unlock()
	return addrs, nil
}

// control is used as net.Dialer.Control to validate the resolved IP right
// before connecting, which also protects against DNS rebinding.
func (p *policy) control(network, address string, _ syscall.RawConn) error {
	ap, err := parseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDestinationBlocked, err)
	}
	return p.checkAddrPort(ap)
}

func parseAddrPort(address string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(address); err == nil {
		return ap, nil
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return netip.AddrPort{}, err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), nil
}
