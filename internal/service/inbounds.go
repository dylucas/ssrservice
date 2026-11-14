package service

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// InboundInput is the editable part of an inbound.
type InboundInput struct {
	Remark    string `json:"remark"`
	Listen    string `json:"listen"`
	Port      int    `json:"port"`
	Method    string `json:"method"`
	ServerPSK string `json:"server_psk"`
	// Domain overrides the global public host for this inbound's share links.
	Domain     string `json:"domain"`
	DomainPort int    `json:"domain_port"`
	UDPEnabled bool   `json:"udp_enabled"`
	Enabled    bool   `json:"enabled"`
}

// InboundView is an inbound enriched with aggregate counters.
type InboundView struct {
	model.Inbound
	ClientCount        int   `json:"client_count"`
	EnabledClientCount int   `json:"enabled_client_count"`
	UpBytes            int64 `json:"up_bytes"`
	DownBytes          int64 `json:"down_bytes"`
	OnlineIPs          int   `json:"online_ips"`
	// LastError explains why this inbound is enabled but not listening (a taken
	// port, a missing permission, an unusable key). It is empty when the runtime
	// matches the row, which is the normal case.
	LastError string `json:"last_error,omitempty"`
}

// inboundViewOf fills an inbound view from the store aggregate (nil when the
// inbound has no clients) and its online IP count.
func inboundViewOf(in model.Inbound, agg *store.InboundAggregate, onlineIPs int) InboundView {
	view := InboundView{Inbound: in, OnlineIPs: onlineIPs}
	if agg != nil {
		view.ClientCount = agg.ClientCount
		view.EnabledClientCount = agg.EnabledClientCount
		view.UpBytes = agg.UpBytes
		view.DownBytes = agg.DownBytes
	}
	return view
}

// ListInbounds returns all inbounds with aggregate counters.
//
// The counters come from one grouped SQL query and the online IP counts from the
// live sessions: listing a handful of inbounds used to read every client row
// into Go just to sum it.
func (s *Service) ListInbounds() ([]InboundView, error) {
	inbounds, err := s.store.ListInbounds()
	if err != nil {
		return nil, err
	}
	aggregates, err := s.store.InboundAggregates(0)
	if err != nil {
		return nil, err
	}
	online := s.mgr.OnlineIPsByInbound()
	applyErrors := s.mgr.InboundErrors()
	views := make([]InboundView, 0, len(inbounds))
	for _, in := range inbounds {
		view := inboundViewOf(in, nil, online[in.ID])
		if agg, ok := aggregates[in.ID]; ok {
			view = inboundViewOf(in, &agg, online[in.ID])
		}
		if in.Enabled {
			// Only an enabled inbound can be "not listening"; for a disabled one
			// the runtime state is the intended state and a stale message would
			// be noise.
			view.LastError = applyErrors[in.ID]
		}
		views = append(views, view)
	}
	return views, nil
}

// GetInbound returns one inbound with counters.
func (s *Service) GetInbound(id int64) (*InboundView, error) {
	in, err := s.store.InboundByID(id)
	if err != nil {
		return nil, err
	}
	aggregates, err := s.store.InboundAggregates(id)
	if err != nil {
		return nil, err
	}
	onlineIPs := s.mgr.OnlineIPsByInbound()[id]
	view := inboundViewOf(*in, nil, onlineIPs)
	if agg, ok := aggregates[id]; ok {
		view = inboundViewOf(*in, &agg, onlineIPs)
	}
	if in.Enabled {
		view.LastError = s.mgr.InboundErrors()[id]
	}
	return &view, nil
}

// CreateInbound validates and persists a new inbound, then starts it.
func (s *Service) CreateInbound(input InboundInput) (*model.Inbound, error) {
	in := model.Inbound{
		Remark:     strings.TrimSpace(input.Remark),
		Listen:     strings.TrimSpace(input.Listen),
		Port:       input.Port,
		Method:     input.Method,
		ServerPSK:  strings.TrimSpace(input.ServerPSK),
		Domain:     input.Domain,
		DomainPort: input.DomainPort,
		UDPEnabled: input.UDPEnabled,
		Enabled:    input.Enabled,
	}
	if err := s.prepareInbound(&in); err != nil {
		return nil, err
	}
	if in.ServerPSK == "" && model.RequiresServerPSK(in.Method) {
		psk, err := ssserver.GenerateServerPSK(in.Method)
		if err != nil {
			return nil, err
		}
		in.ServerPSK = psk
	}
	if in.Remark == "" {
		in.Remark = "SS " + strconv.Itoa(in.Port)
	}
	if err := s.store.CreateInbound(&in); err != nil {
		return nil, err
	}
	if err := s.reloadInbound(in.ID); err != nil {
		_ = s.store.DeleteInbound(in.ID)
		return nil, err
	}
	return &in, nil
}

// UpdateInbound updates an inbound and applies the change to the runtime.
func (s *Service) UpdateInbound(id int64, input InboundInput) (*model.Inbound, error) {
	old, err := s.store.InboundByID(id)
	if err != nil {
		return nil, err
	}
	updated := *old
	updated.Remark = strings.TrimSpace(input.Remark)
	updated.Listen = strings.TrimSpace(input.Listen)
	updated.Port = input.Port
	updated.Method = input.Method
	updated.Domain = input.Domain
	updated.DomainPort = input.DomainPort
	updated.UDPEnabled = input.UDPEnabled
	updated.Enabled = input.Enabled
	// An empty server PSK means "keep the current one" when the method still
	// requires it; switching methods or creating flows generate a new key.
	if psk := strings.TrimSpace(input.ServerPSK); psk != "" {
		updated.ServerPSK = psk
	} else if updated.Method != old.Method {
		// The old key may not match the new method's length.
		if err := ssserver.ValidateServerPSK(updated.Method, old.ServerPSK); err != nil || old.ServerPSK == "" {
			updated.ServerPSK = ""
		}
	}
	if !model.RequiresServerPSK(updated.Method) {
		updated.ServerPSK = ""
	}
	if err := s.prepareInbound(&updated); err != nil {
		return nil, err
	}
	if updated.ServerPSK == "" && model.RequiresServerPSK(updated.Method) {
		psk, err := ssserver.GenerateServerPSK(updated.Method)
		if err != nil {
			return nil, err
		}
		updated.ServerPSK = psk
	}
	if err := s.validateClientsForMethod(id, updated.Method); err != nil {
		return nil, err
	}
	if err := s.store.UpdateInbound(&updated); err != nil {
		return nil, err
	}
	if err := s.reloadInbound(id); err != nil {
		// Roll back both the database row and the runtime.
		_ = s.store.UpdateInbound(old)
		_ = s.reloadInbound(id)
		return nil, err
	}
	return &updated, nil
}

// DeleteInbound stops and removes an inbound.
//
// The client rows are read before the delete so their runtime accounting
// accounts can be reclaimed afterwards. The order matters: stop serving first,
// then delete the rows (which cascades to clients and traffic), and only drop
// the in-memory counters once the database committed. Otherwise a failed delete
// would leave the rows intact while the counters -- and therefore the quota
// verdict for those users -- were already discarded.
func (s *Service) DeleteInbound(id int64) error {
	in, err := s.store.InboundByID(id)
	if err != nil {
		return err
	}
	clients, err := s.store.ListClients(id)
	if err != nil {
		return err
	}
	s.mgr.RemoveInbound(id)
	if err := s.store.DeleteInbound(id); err != nil {
		// The rows are still there, so the listener has to be too; otherwise a
		// failed delete would silently stop a service that the panel still
		// lists as enabled.
		if restoreErr := s.mgr.ApplyInbound(*in, clients); restoreErr != nil {
			s.logger.Error("restore inbound after a failed delete also failed",
				"inbound_id", id, "error", restoreErr)
		}
		return err
	}
	for _, c := range clients {
		s.mgr.RemoveClient(c.ID)
	}
	return nil
}

// ToggleInbound enables or disables an inbound.
func (s *Service) ToggleInbound(id int64, enabled bool) error {
	in, err := s.store.InboundByID(id)
	if err != nil {
		return err
	}
	in.Enabled = enabled
	if err := s.store.UpdateInbound(in); err != nil {
		return err
	}
	if !enabled {
		s.mgr.RemoveInbound(id)
		return nil
	}
	if err := s.reloadInbound(id); err != nil {
		// The row says enabled but the listener never started; putting the old
		// value back keeps the panel honest and lets the operator retry.
		in.Enabled = false
		_ = s.store.UpdateInbound(in)
		return err
	}
	return nil
}

// RestartInbound rebuilds the listener of an inbound.
//
// A restart has to stop listening before it can bind the same address again, so
// it cannot be atomic. What it can do is not lose the working listener: if the
// rebuild fails (the port was taken by another process in between, the UDP bind
// failed, a client's PSK no longer parses), the previous configuration is
// applied again instead of leaving the inbound dead until someone notices.
func (s *Service) RestartInbound(id int64) error {
	previous, err := s.store.InboundByID(id)
	if err != nil {
		return err
	}
	clients, err := s.store.ListClients(id)
	if err != nil {
		return err
	}
	s.mgr.RemoveInbound(id)
	if err := s.reloadInbound(id); err != nil {
		if restoreErr := s.mgr.ApplyInbound(*previous, clients); restoreErr != nil {
			return fmt.Errorf("%w (restoring the previous listener also failed: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

// ReloadCount reports how many times an inbound has been reloaded since start.
// Bulk paths assert that they reload once rather than once per user.
func (s *Service) ReloadCount() int64 { return s.reloads.Load() }

func (s *Service) reloadInbound(id int64) error {
	s.reloads.Add(1)
	in, err := s.store.InboundByID(id)
	if err != nil {
		return err
	}
	clients, err := s.store.ListClients(id)
	if err != nil {
		return err
	}
	return s.mgr.ApplyInbound(*in, clients)
}

func (s *Service) prepareInbound(in *model.Inbound) error {
	in.Method = strings.TrimSpace(in.Method)
	if !model.IsMethod(in.Method) {
		return invalid("unsupported method %q", in.Method)
	}
	if in.Listen == "" {
		in.Listen = "0.0.0.0"
	}
	if err := validateListenAddress(in.Listen); err != nil {
		return err
	}
	if in.Port < 1 || in.Port > 65535 {
		return invalid("port must be between 1 and 65535")
	}
	if err := ssserver.ValidateServerPSK(in.Method, in.ServerPSK); err != nil {
		return invalid("%v", err)
	}
	in.Remark = strings.TrimSpace(in.Remark)
	if len(in.Remark) > 64 {
		return invalid("remark must be at most 64 characters")
	}
	in.Domain = normalizeHost(in.Domain)
	if in.Domain != "" {
		if err := validateHostValue(in.Domain); err != nil {
			return invalid("invalid domain: %v", err)
		}
	}
	if in.DomainPort < 0 || in.DomainPort > 65535 {
		return invalid("domain_port must be between 0 and 65535")
	}
	// A port without a host would be silently ignored when links are built, which
	// is the easiest mistake to make in the form.
	if in.DomainPort > 0 && in.Domain == "" {
		return invalid("domain_port requires a domain")
	}
	return nil
}

func (s *Service) validateClientsForMethod(inboundID int64, method string) error {
	clients, err := s.store.ListClients(inboundID)
	if err != nil {
		return err
	}
	enabled := 0
	for _, c := range clients {
		if err := ssserver.ValidateClientPSK(method, c.PSK); err != nil {
			return invalid("client %q is not compatible with method %s: %v; delete the client or change its password first", c.Name, method, err)
		}
		if c.Enabled {
			enabled++
		}
	}
	// A method switch can introduce the single-user restriction by itself: two
	// enabled users were fine under an SS2022 multi-user method and become
	// ambiguous under chacha20-poly1305, which has no identity header. Without
	// this check the runtime would silently serve only one of them and attribute
	// both users' traffic to that one.
	if !model.IsMultiUser(method) && enabled > 1 {
		return invalid("method %s only supports a single enabled client per inbound; disable all but one client first", method)
	}
	return nil
}

func validateListenAddress(listen string) error {
	if listen == "" {
		return invalid("listen address must not be empty")
	}
	if listen == "localhost" {
		return nil
	}
	if strings.HasPrefix(listen, "[") || strings.HasSuffix(listen, "]") {
		return invalid("listen must be an IP address without brackets, for example :: or 2001:db8::1")
	}
	addr, err := netip.ParseAddr(listen)
	if err != nil {
		return invalid("listen must be an IP address, for example 0.0.0.0, :: or 127.0.0.1")
	}
	if addr.Zone() != "" {
		return invalid("listen must not contain an IPv6 zone suffix")
	}
	return nil
}

// resolveHostPort decides which host and port to embed in share links.
//
// Precedence, most specific first:
//
//	host: inbound domain -> global public_host -> request hint -> listen address
//	port: inbound domain_port -> global public_port -> inbound port
//
// The inbound domain wins because it is the only value that describes one inbound
// while the global setting is shared by all of them. The port chain keeps a
// domain-only configuration from silently changing the port: falling back to the
// global port (and then the inbound port) matches what the link showed before.
func (s *Service) resolveHostPort(in *model.Inbound, fallbackHost string) (string, int) {
	s.mu.RLock()
	globalHost := s.publicHost
	globalPort := s.publicPort
	s.mu.RUnlock()

	if in != nil && in.Domain != "" {
		port := in.DomainPort
		if port <= 0 {
			port = globalPort
		}
		if port <= 0 {
			port = in.Port
		}
		return in.Domain, port
	}

	host := globalHost
	port := globalPort
	if host == "" {
		host = strings.TrimSpace(fallbackHost)
	}
	if host == "" {
		host = in.Listen
		switch host {
		case "0.0.0.0":
			host = "127.0.0.1"
		case "::", "[::]":
			// An unspecified IPv6 address is not dialable; share links are
			// far more useful pointing at the loopback interface.
			host = "::1"
		}
	}
	if port <= 0 {
		port = in.Port
	}
	return host, port
}
