package service

import (
	"errors"
	"strings"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
	"github.com/ydong/ssrservice/internal/subscription"
)

// ClientInput is the editable part of a client.
type ClientInput struct {
	Name        string `json:"name"`
	PSK         string `json:"psk"`
	Enabled     bool   `json:"enabled"`
	QuotaBytes  int64  `json:"quota_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	IPLimit     int    `json:"ip_limit"`
	ResetPolicy string `json:"reset_policy"`
	ResetDay    int    `json:"reset_day"`
	Note        string `json:"note"`
}

// ClientView is a client enriched with computed status and share links.
type ClientView struct {
	model.Client
	Status          string   `json:"status"`
	UsedBytes       int64    `json:"used_bytes"`
	RemainingBytes  int64    `json:"remaining_bytes"`
	OnlineIPs       []string `json:"online_ips"`
	InboundRemark   string   `json:"inbound_remark"`
	Method          string   `json:"method"`
	Link            string   `json:"link"`
	SubscriptionURL string   `json:"subscription_url"`
}

// ListClients returns all clients, optionally filtered by inbound.
func (s *Service) ListClients(inboundID int64) ([]ClientView, error) {
	clients, err := s.store.ListClients(inboundID)
	if err != nil {
		return nil, err
	}
	// The endpoint and subscription state only depend on the inbound and on the
	// settings, so compute them once per inbound instead of once per client.
	contexts := make(map[int64]clientViewContext)
	online := s.mgr.OnlineIPs()
	subEnabled := s.SubscriptionEnabled()
	now := time.Now().Unix()
	views := make([]ClientView, 0, len(clients))
	for _, c := range clients {
		ctx, ok := contexts[c.InboundID]
		if !ok {
			in, err := s.store.InboundByID(c.InboundID)
			if err != nil {
				contexts[c.InboundID] = clientViewContext{}
				continue
			}
			ctx = s.clientViewContextFor(in, subEnabled, online, now)
			contexts[c.InboundID] = ctx
		}
		if ctx.inbound == nil {
			continue
		}
		views = append(views, s.clientView(c, ctx))
	}
	return views, nil
}

// GetClient returns one client view.
func (s *Service) GetClient(id int64) (*ClientView, error) {
	c, err := s.store.ClientByID(id)
	if err != nil {
		return nil, err
	}
	in, err := s.store.InboundByID(c.InboundID)
	if err != nil {
		return nil, err
	}
	ctx := s.clientViewContextFor(in, s.SubscriptionEnabled(), s.mgr.OnlineIPs(), time.Now().Unix())
	view := s.clientView(*c, ctx)
	return &view, nil
}

// clientViewContext is the state shared by every client view of one inbound in
// a single listing.
type clientViewContext struct {
	inbound    *model.Inbound
	host       string
	port       int
	subEnabled bool
	online     map[int64][]string
	now        int64
}

// clientViewContextFor resolves the share-link endpoint of an inbound once.
func (s *Service) clientViewContextFor(in *model.Inbound, subEnabled bool, online map[int64][]string, now int64) clientViewContext {
	host, port := s.resolveHostPort(in, "")
	return clientViewContext{
		inbound:    in,
		host:       host,
		port:       port,
		subEnabled: subEnabled,
		online:     online,
		now:        now,
	}
}

func (s *Service) clientView(c model.Client, ctx clientViewContext) ClientView {
	status := c.StatusAt(ctx.now)
	if status != model.StatusDisabled {
		// Live counters may be ahead of the persisted ones.
		up, down := s.mgr.ClientTraffic(c.ID)
		if up > c.UpBytes {
			c.UpBytes = up
		}
		if down > c.DownBytes {
			c.DownBytes = down
		}
		status = c.StatusAt(ctx.now)
	}
	view := ClientView{
		Client:         c,
		Status:         status,
		UsedBytes:      c.UsedBytes(),
		RemainingBytes: c.RemainingBytes(),
		OnlineIPs:      ctx.online[c.ID],
	}
	if view.OnlineIPs == nil {
		view.OnlineIPs = []string{}
	}
	if in := ctx.inbound; in != nil {
		view.InboundRemark = in.Remark
		view.Method = in.Method
		proxy := subscription.Proxy{
			Name:      c.Name,
			Method:    in.Method,
			ServerPSK: in.ServerPSK,
			ClientPSK: c.PSK,
			Host:      ctx.host,
			Port:      ctx.port,
			UDP:       in.UDPEnabled,
		}
		view.Link = subscription.Link(proxy)
		view.SubscriptionURL = subscriptionPath(c.SubToken, ctx.subEnabled)
	}
	return view
}

// subscriptionPath renders the public subscription path, or "" when
// subscriptions are disabled or the client has no token.
func subscriptionPath(token string, enabled bool) string {
	if token == "" || !enabled {
		return ""
	}
	return "/sub/" + token
}

func (s *Service) subscriptionURL(token string) string {
	return subscriptionPath(token, s.withinSubscription())
}

// CreateClient adds a user to an inbound.
func (s *Service) CreateClient(inboundID int64, input ClientInput) (*model.Client, error) {
	in, err := s.store.InboundByID(inboundID)
	if err != nil {
		return nil, err
	}
	c := model.Client{
		InboundID:   inboundID,
		Name:        strings.TrimSpace(input.Name),
		PSK:         strings.TrimSpace(input.PSK),
		Enabled:     input.Enabled,
		QuotaBytes:  input.QuotaBytes,
		ExpiresAt:   input.ExpiresAt,
		IPLimit:     input.IPLimit,
		ResetPolicy: input.ResetPolicy,
		ResetDay:    input.ResetDay,
		Note:        strings.TrimSpace(input.Note),
	}
	if err := s.prepareClient(in, &c, 0); err != nil {
		return nil, err
	}
	if c.PSK == "" {
		psk, err := ssserver.GenerateClientPSK(in.Method)
		if err != nil {
			return nil, err
		}
		c.PSK = psk
	}
	token, err := s.uniqueToken()
	if err != nil {
		return nil, err
	}
	c.SubToken = token
	// Start the monthly reset clock at creation so a new client is not reset
	// immediately on the next scheduler tick.
	c.LastResetAt = model.NowUnix()
	if err := s.store.CreateClient(&c); err != nil {
		return nil, err
	}
	if err := s.reloadInbound(inboundID); err != nil {
		_ = s.store.DeleteClient(c.ID)
		_ = s.reloadInbound(inboundID)
		return nil, err
	}
	return &c, nil
}

// UpdateClient updates a user and applies the change to the runtime.
func (s *Service) UpdateClient(id int64, input ClientInput) (*model.Client, error) {
	old, err := s.store.ClientByID(id)
	if err != nil {
		return nil, err
	}
	in, err := s.store.InboundByID(old.InboundID)
	if err != nil {
		return nil, err
	}
	updated := *old
	updated.Name = strings.TrimSpace(input.Name)
	// An empty PSK means "keep the current one"; only create flows generate.
	if psk := strings.TrimSpace(input.PSK); psk != "" {
		updated.PSK = psk
	}
	updated.Enabled = input.Enabled
	updated.QuotaBytes = input.QuotaBytes
	updated.ExpiresAt = input.ExpiresAt
	updated.IPLimit = input.IPLimit
	updated.ResetPolicy = input.ResetPolicy
	updated.ResetDay = input.ResetDay
	updated.Note = strings.TrimSpace(input.Note)
	if err := s.prepareClient(in, &updated, id); err != nil {
		return nil, err
	}
	if err := s.store.UpdateClient(&updated); err != nil {
		return nil, err
	}
	if err := s.reloadInbound(in.ID); err != nil {
		_ = s.store.UpdateClient(old)
		_ = s.reloadInbound(in.ID)
		return nil, err
	}
	if !updated.Enabled {
		s.mgr.CloseClient(updated.ID)
	}
	return &updated, nil
}

// DeleteClient removes a user.
func (s *Service) DeleteClient(id int64) error {
	c, err := s.store.ClientByID(id)
	if err != nil {
		return err
	}
	if err := s.store.DeleteClient(id); err != nil {
		return err
	}
	s.mgr.RemoveClient(id)
	return s.reloadInbound(c.InboundID)
}

// ToggleClient enables or disables a user.
func (s *Service) ToggleClient(id int64, enabled bool) error {
	c, err := s.store.ClientByID(id)
	if err != nil {
		return err
	}
	if err := s.store.SetClientEnabled(id, enabled); err != nil {
		return err
	}
	if !enabled {
		s.mgr.CloseClient(id)
	}
	return s.reloadInbound(c.InboundID)
}

// KickClient disconnects all sessions of a user.
func (s *Service) KickClient(id int64) error {
	s.mgr.CloseClient(id)
	return nil
}

// ResetClientToken rotates the subscription token of a user.
func (s *Service) ResetClientToken(id int64) (string, error) {
	if _, err := s.store.ClientByID(id); err != nil {
		return "", err
	}
	token, err := s.uniqueToken()
	if err != nil {
		return "", err
	}
	if err := s.store.UpdateClientToken(id, token); err != nil {
		return "", err
	}
	return token, nil
}

// ClientLink returns the ss:// link and subscription URL for a user.
func (s *Service) ClientLink(id int64, fallbackHost string) (string, string, error) {
	c, err := s.store.ClientByID(id)
	if err != nil {
		return "", "", err
	}
	in, err := s.store.InboundByID(c.InboundID)
	if err != nil {
		return "", "", err
	}
	host, port := s.resolveHostPort(in, fallbackHost)
	link := subscription.Link(subscription.Proxy{
		Name:      c.Name,
		Method:    in.Method,
		ServerPSK: in.ServerPSK,
		ClientPSK: c.PSK,
		Host:      host,
		Port:      port,
		UDP:       in.UDPEnabled,
	})
	return link, s.subscriptionURL(c.SubToken), nil
}

// Usage returns daily traffic for a client.
func (s *Service) Usage(clientID int64, days int) ([]model.DailyUsage, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).In(s.location()).Format("2006-01-02")
	return s.store.DailyUsage(clientID, since)
}

func (s *Service) prepareClient(in *model.Inbound, c *model.Client, selfID int64) error {
	if c.Name == "" {
		return invalid("name must not be empty")
	}
	if len(c.Name) > 64 {
		return invalid("name must be at most 64 characters")
	}
	if len(c.Note) > 255 {
		return invalid("note must be at most 255 characters")
	}
	if c.QuotaBytes < 0 || c.ExpiresAt < 0 || c.IPLimit < 0 {
		return invalid("quota, expiry and ip limit must not be negative")
	}
	if c.IPLimit > 10000 {
		return invalid("ip_limit must be at most 10000")
	}
	switch c.ResetPolicy {
	case "":
		c.ResetPolicy = model.ResetNone
	case model.ResetNone, model.ResetMonthly:
	default:
		return invalid("reset_policy must be none or monthly")
	}
	if c.ResetDay < 0 || c.ResetDay > 28 {
		return invalid("reset_day must be between 0 and 28")
	}
	if c.ResetPolicy == model.ResetMonthly && c.ResetDay == 0 {
		// 0 means "use the global reset day"; leave it.
	}
	if c.PSK != "" {
		if err := ssserver.ValidateClientPSK(in.Method, c.PSK); err != nil {
			return invalid("%v", err)
		}
	}
	if !model.IsMultiUser(in.Method) && c.Enabled {
		clients, err := s.store.ListClients(in.ID)
		if err != nil {
			return err
		}
		for _, other := range clients {
			if other.ID == selfID || !other.Enabled {
				continue
			}
			return invalid("method %s only supports a single enabled client per inbound; disable the other client first", in.Method)
		}
	}
	return nil
}

func (s *Service) uniqueToken() (string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		token, err := auth.RandomToken(18)
		if err != nil {
			return "", err
		}
		if _, err := s.store.ClientBySubToken(token); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		return token, nil
	}
	return "", errors.New("could not generate a unique subscription token")
}
