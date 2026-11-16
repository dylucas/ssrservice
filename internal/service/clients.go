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

// ClientSummary is the list representation of a client.
//
// It deliberately omits every credential and derived secret: the client PSK,
// the subscription token and the rendered share link are fetched per client
// from /clients/:id or /clients/:id/link. Keeping them out of the list means a
// bulk response carries no secret, which is also why the list can safely be
// gzip-compressed while the rest of the API cannot.
//
// The timestamps a list view never renders (created_at, updated_at,
// last_reset_at) are left out too.
type ClientSummary struct {
	ID             int64    `json:"id"`
	InboundID      int64    `json:"inbound_id"`
	Name           string   `json:"name"`
	Enabled        bool     `json:"enabled"`
	QuotaBytes     int64    `json:"quota_bytes"`
	UpBytes        int64    `json:"up_bytes"`
	DownBytes      int64    `json:"down_bytes"`
	ExpiresAt      int64    `json:"expires_at"`
	IPLimit        int      `json:"ip_limit"`
	ResetPolicy    string   `json:"reset_policy"`
	ResetDay       int      `json:"reset_day"`
	Note           string   `json:"note"`
	Status         string   `json:"status"`
	UsedBytes      int64    `json:"used_bytes"`
	RemainingBytes int64    `json:"remaining_bytes"`
	OnlineIPs      []string `json:"online_ips"`
	InboundRemark  string   `json:"inbound_remark"`
	Method         string   `json:"method"`
}

// ClientListFilter selects one page of the client list.
type ClientListFilter struct {
	// InboundID restricts the page to one inbound; 0 means all of them.
	InboundID int64
	// Status filters by the computed status. "" and "all" mean no filter.
	Status string
	// Query is a case-insensitive substring matched against the client name,
	// the client note, the inbound remark and the method.
	Query string
	// Limit and Offset page the result; zero values mean the documented
	// defaults.
	Limit  int
	Offset int
}

// ClientListPage is one page of the client list.
//
// The paging values are echoed so the client can render a pager without
// duplicating the server's defaults and bounds.
type ClientListPage struct {
	Items  []ClientSummary `json:"items"`
	Total  int64           `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// ListClientsPage returns one page of the client list.
//
// The list used to return every client, which on a 5000 user deployment was
// 1.71 MB of JSON and 9.6 MB of allocations per request while the view only
// renders 20 rows. The page is filtered and paged in SQL, and the inbound
// remark and method come back from the same join, so the list no longer reads
// the inbound rows (and therefore never touches their server PSK).
func (s *Service) ListClientsPage(filter ClientListFilter) (*ClientListPage, error) {
	now := time.Now().Unix()
	status := filter.Status
	if status == "all" {
		status = ""
	}
	rows, total, err := s.store.ListClientPage(store.ClientPageFilter{
		InboundID: filter.InboundID,
		Status:    status,
		Query:     strings.TrimSpace(filter.Query),
		Limit:     filter.Limit,
		Offset:    filter.Offset,
		Now:       now,
	})
	if err != nil {
		return nil, err
	}
	// Online IPs are tracked per live session, so the map is as large as the
	// number of connected users, not the number of stored ones.
	online := s.mgr.OnlineIPs()
	items := make([]ClientSummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.clientSummary(row, online, now))
	}
	limit, offset := store.NormalizeClientPage(filter.Limit, filter.Offset)
	return &ClientListPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// clientSummary projects one page row into its list representation. The row is
// projected back onto the domain type first so the status and remaining-quota
// rules stay in a single place instead of being reimplemented for the lean row.
func (s *Service) clientSummary(row store.ClientPageRow, online map[int64][]string, now int64) ClientSummary {
	c, status := s.withLiveStatus(row.Model(), now)
	ips := online[c.ID]
	if ips == nil {
		ips = []string{}
	}
	return ClientSummary{
		ID:             c.ID,
		InboundID:      c.InboundID,
		Name:           c.Name,
		Enabled:        c.Enabled,
		QuotaBytes:     c.QuotaBytes,
		UpBytes:        c.UpBytes,
		DownBytes:      c.DownBytes,
		ExpiresAt:      c.ExpiresAt,
		IPLimit:        c.IPLimit,
		ResetPolicy:    c.ResetPolicy,
		ResetDay:       c.ResetDay,
		Note:           c.Note,
		Status:         status,
		UsedBytes:      c.UsedBytes(),
		RemainingBytes: c.RemainingBytes(),
		OnlineIPs:      ips,
		InboundRemark:  row.InboundRemark,
		Method:         row.Method,
	}
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

// withLiveStatus raises the persisted counters of a client to the live ones and
// recomputes its status. Live counters may be ahead of the database by up to one
// flush interval, and a disabled client is skipped because it cannot have served
// traffic since it was disabled.
func (s *Service) withLiveStatus(c model.Client, now int64) (model.Client, string) {
	status := c.StatusAt(now)
	if status == model.StatusDisabled {
		return c, status
	}
	up, down := s.mgr.ClientTraffic(c.ID)
	if up > c.UpBytes {
		c.UpBytes = up
	}
	if down > c.DownBytes {
		c.DownBytes = down
	}
	return c, c.StatusAt(now)
}

func (s *Service) clientView(c model.Client, ctx clientViewContext) ClientView {
	c, status := s.withLiveStatus(c, ctx.now)
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
//
// Enabling is a state change like any other and must pass the same rules as an
// edit, otherwise a single-user inbound could end up serving two clients with
// the traffic of both booked to one of them. A failed reload rolls the database
// row back, so the panel never disagrees with the runtime.
func (s *Service) ToggleClient(id int64, enabled bool) error {
	c, err := s.store.ClientByID(id)
	if err != nil {
		return err
	}
	if enabled {
		in, err := s.store.InboundByID(c.InboundID)
		if err != nil {
			return err
		}
		if err := s.ensureSingleUserSlot(in, id); err != nil {
			return err
		}
	}
	if err := s.store.SetClientEnabled(id, enabled); err != nil {
		return err
	}
	if !enabled {
		s.mgr.CloseClient(id)
	}
	if err := s.reloadInbound(c.InboundID); err != nil {
		_ = s.store.SetClientEnabled(id, c.Enabled)
		_ = s.reloadInbound(c.InboundID)
		return err
	}
	return nil
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

// UsageMaxDays bounds the per-client traffic window: the panel offers 7, 30 and
// 90 days, and the rollup is not kept long enough to answer a year.
const UsageMaxDays = 365

// Usage returns the daily traffic of the last `days` days for a client.
//
// Like DailyTotalsSince, the lower bound is days-1 back: the query keeps every
// day from it onward, so counting back `days` would answer N+1 buckets.
func (s *Service) Usage(clientID int64, days int) ([]model.DailyUsage, error) {
	if days <= 0 || days > UsageMaxDays {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -(days - 1)).In(s.location()).Format("2006-01-02")
	return s.store.DailyUsage(clientID, since)
}

// validateClientFields checks everything about a client that does not depend on
// the other clients of its inbound.
//
// It is split out of prepareClient (which also enforces the per-inbound
// single-user rule) so a bulk import can validate a whole batch without
// re-reading the inbound's user list for every line.
func validateClientFields(in *model.Inbound, c *model.Client) error {
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
	if c.PSK != "" {
		if err := ssserver.ValidateClientPSK(in.Method, c.PSK); err != nil {
			return invalid("%v", err)
		}
	}
	return nil
}

func (s *Service) prepareClient(in *model.Inbound, c *model.Client, selfID int64) error {
	if err := validateClientFields(in, c); err != nil {
		return err
	}
	if !c.Enabled {
		return nil
	}
	return s.ensureSingleUserSlot(in, selfID)
}

// ensureSingleUserSlot enforces "at most one enabled client" for methods that
// cannot isolate users.
//
// Shadowsocks 2022 chacha20-poly1305 has no identity header, so the runtime
// builds one service from the first enabled user and attributes every connection
// to it. A second enabled user therefore does not just fail to connect: traffic,
// quota and expiry are accounted against somebody else.
//
// The rule used to be enforced only on create and import, which left two ways
// around it: toggling an existing disabled client on, and switching an inbound's
// method to a single-user one. Both now call this helper.
func (s *Service) ensureSingleUserSlot(in *model.Inbound, selfID int64) error {
	if model.IsMultiUser(in.Method) {
		return nil
	}
	count, err := s.store.CountEnabledClients(in.ID, selfID)
	if err != nil {
		return err
	}
	if count > 0 {
		return invalid("method %s only supports a single enabled client per inbound; disable the other client first", in.Method)
	}
	return nil
}

// uniqueToken returns one unused subscription token.
func (s *Service) uniqueToken() (string, error) {
	tokens, err := s.uniqueTokens(1)
	if err != nil {
		return "", err
	}
	return tokens[0], nil
}

// uniqueTokens returns n distinct, unused subscription tokens.
//
// A bulk import needs one token per created user; generating them one at a time
// cost one collision query each. This picks the whole batch up front, checks the
// batch's existence with a few queries and regenerates only the collisions.
func (s *Service) uniqueTokens(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	tokens := make([]string, n)
	// Distinct within the batch first, so the existence check sees true
	// duplicates rather than two copies of the same fresh token.
	seen := make(map[string]struct{}, n)
	for i := range tokens {
		for {
			token, err := auth.RandomToken(18)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[token]; duplicate {
				continue
			}
			seen[token] = struct{}{}
			tokens[i] = token
			break
		}
	}
	for attempt := 0; attempt < 5; attempt++ {
		taken, err := s.store.TokensInUse(tokens)
		if err != nil {
			return nil, err
		}
		if len(taken) == 0 {
			return tokens, nil
		}
		for i, token := range tokens {
			if !taken[token] {
				continue
			}
			replacement, err := auth.RandomToken(18)
			if err != nil {
				return nil, err
			}
			tokens[i] = replacement
		}
	}
	return nil, errors.New("could not generate unique subscription tokens")
}

// SubscriptionFor resolves a subscription token into the proxy record used to
// render the subscription document.
//
// The panel used to load the client and the inbound through Store() and decide
// the enabled/active rule itself, which duplicated a domain rule outside the
// layer that owns it. A token lookup, unlike an API call, must not reveal the
// difference between "no such token", "subscription disabled" and "client not
// active": all three return ErrNotFound and the handler answers a plain 404.
func (s *Service) SubscriptionFor(token string) (subscription.Proxy, error) {
	if token == "" || !s.withinSubscription() {
		return subscription.Proxy{}, store.ErrNotFound
	}
	client, err := s.store.ClientBySubToken(token)
	if err != nil {
		return subscription.Proxy{}, err
	}
	in, err := s.store.InboundByID(client.InboundID)
	if err != nil {
		return subscription.Proxy{}, err
	}
	if !in.Enabled || client.StatusAt(time.Now().Unix()) != model.StatusActive {
		return subscription.Proxy{}, store.ErrNotFound
	}
	host, port := s.resolveHostPort(in, "")
	return subscription.Proxy{
		Name:      client.Name,
		Method:    in.Method,
		ServerPSK: in.ServerPSK,
		ClientPSK: client.PSK,
		Host:      host,
		Port:      port,
		UDP:       in.UDPEnabled,
	}, nil
}
