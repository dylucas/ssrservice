package service

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// InboundInput is the editable part of an inbound.
type InboundInput struct {
	Remark     string `json:"remark"`
	Listen     string `json:"listen"`
	Port       int    `json:"port"`
	Method     string `json:"method"`
	ServerPSK  string `json:"server_psk"`
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
	views := make([]InboundView, 0, len(inbounds))
	for _, in := range inbounds {
		agg, ok := aggregates[in.ID]
		if !ok {
			views = append(views, inboundViewOf(in, nil, online[in.ID]))
			continue
		}
		views = append(views, inboundViewOf(in, &agg, online[in.ID]))
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
	clients, err := s.store.ListClients(id)
	if err != nil {
		return err
	}
	s.mgr.RemoveInbound(id)
	if err := s.store.DeleteInbound(id); err != nil {
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
	return s.reloadInbound(id)
}

// RestartInbound rebuilds the listener of an inbound.
func (s *Service) RestartInbound(id int64) error {
	s.mgr.RemoveInbound(id)
	return s.reloadInbound(id)
}

func (s *Service) reloadInbound(id int64) error {
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
	return nil
}

func (s *Service) validateClientsForMethod(inboundID int64, method string) error {
	clients, err := s.store.ListClients(inboundID)
	if err != nil {
		return err
	}
	for _, c := range clients {
		if err := ssserver.ValidateClientPSK(method, c.PSK); err != nil {
			return invalid("client %q is not compatible with method %s: %v; delete the client or change its password first", c.Name, method, err)
		}
	}
	return nil
}

// ErrInvalidListen is returned for malformed listen addresses.
var ErrInvalidListen = errors.New("invalid listen address")

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
func (s *Service) resolveHostPort(in *model.Inbound, fallbackHost string) (string, int) {
	s.mu.RLock()
	host := s.publicHost
	port := s.publicPort
	s.mu.RUnlock()
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

// IsStoreNotFound reports whether err is a missing row.
func IsStoreNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
