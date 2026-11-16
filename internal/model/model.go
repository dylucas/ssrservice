// Package model defines the domain types shared by the panel, the store and
// the Shadowsocks runtime.
package model

import (
	"net"
	"strconv"
	"strings"
	"time"
)

// Shadowsocks method identifiers supported by this server.
const (
	Method2022AES128GCM = "2022-blake3-aes-128-gcm"
	Method2022AES256GCM = "2022-blake3-aes-256-gcm"
	Method2022ChaCha20  = "2022-blake3-chacha20-poly1305"
	MethodAES128GCM     = "aes-128-gcm"
	MethodAES256GCM     = "aes-256-gcm"
	MethodChaCha20      = "chacha20-ietf-poly1305"
)

// MethodKeySize maps a method to its PSK length in bytes.
var MethodKeySize = map[string]int{
	Method2022AES128GCM: 16,
	Method2022AES256GCM: 32,
	Method2022ChaCha20:  32,
	MethodAES128GCM:     16,
	MethodAES256GCM:     32,
	MethodChaCha20:      32,
}

// Methods lists the supported methods in display order.
var Methods = []string{
	Method2022AES128GCM,
	Method2022AES256GCM,
	Method2022ChaCha20,
	MethodAES128GCM,
	MethodAES256GCM,
	MethodChaCha20,
}

// IsMethod reports whether name is a supported Shadowsocks method.
func IsMethod(name string) bool {
	_, ok := MethodKeySize[name]
	return ok
}

// IsSS2022 reports whether the method belongs to the Shadowsocks 2022 family.
func IsSS2022(method string) bool {
	return strings.HasPrefix(method, "2022-")
}

// IsMultiUser reports whether the method isolates multiple users on one port.
// Shadowsocks 2022 chacha20-poly1305 has no identity header, so it can only
// serve a single user.
func IsMultiUser(method string) bool {
	if method == Method2022ChaCha20 {
		return false
	}
	return IsMethod(method)
}

// RequiresServerPSK reports whether an inbound needs a server PSK.
func RequiresServerPSK(method string) bool {
	return IsSS2022(method) && IsMultiUser(method)
}

// KeySize returns the PSK length of the method, or 0 for unknown methods.
func KeySize(method string) int {
	return MethodKeySize[method]
}

// Client status values are computed from the client's configuration and
// counters; they are never stored in the database.
const (
	StatusActive        = "active"
	StatusDisabled      = "disabled"
	StatusExpired       = "expired"
	StatusQuotaExceeded = "quota_exceeded"
)

// Inbound is a Shadowsocks listener together with its user list.
type Inbound struct {
	ID        int64  `json:"id"`
	Remark    string `json:"remark"`
	Listen    string `json:"listen"`
	Port      int    `json:"port"`
	Method    string `json:"method"`
	ServerPSK string `json:"server_psk,omitempty"`
	// Domain, when set, is the hostname used in share links and subscriptions for
	// this inbound instead of the global public_host. DomainPort overrides the
	// port, with 0 meaning "fall back to public_port, then to the inbound port".
	Domain     string `json:"domain"`
	DomainPort int    `json:"domain_port"`
	UDPEnabled bool   `json:"udp_enabled"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// Addr returns the host:port the inbound listens on. IPv6 literals are
// bracketed ("[::1]:8388").
func (i *Inbound) Addr() string {
	return net.JoinHostPort(i.Listen, strconv.Itoa(i.Port))
}

// Client is a Shadowsocks user.
type Client struct {
	ID          int64  `json:"id"`
	InboundID   int64  `json:"inbound_id"`
	Name        string `json:"name"`
	PSK         string `json:"psk,omitempty"`
	Enabled     bool   `json:"enabled"`
	QuotaBytes  int64  `json:"quota_bytes"`
	UpBytes     int64  `json:"up_bytes"`
	DownBytes   int64  `json:"down_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	IPLimit     int    `json:"ip_limit"`
	ResetPolicy string `json:"reset_policy"`
	ResetDay    int    `json:"reset_day"`
	LastResetAt int64  `json:"last_reset_at"`
	SubToken    string `json:"sub_token"`
	Note        string `json:"note"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// UsedBytes returns the total traffic counted for the client.
func (c *Client) UsedBytes() int64 {
	if c == nil {
		return 0
	}
	return c.UpBytes + c.DownBytes
}

// RemainingBytes returns the quota left; a negative value means unlimited.
func (c *Client) RemainingBytes() int64 {
	if c == nil || c.QuotaBytes <= 0 {
		return -1
	}
	remaining := c.QuotaBytes - c.UsedBytes()
	if remaining < 0 {
		return 0
	}
	return remaining
}

// StatusAt computes the effective status at the given unix timestamp.
func (c *Client) StatusAt(now int64) string {
	switch {
	case !c.Enabled:
		return StatusDisabled
	case c.ExpiresAt > 0 && now >= c.ExpiresAt:
		return StatusExpired
	case c.QuotaBytes > 0 && c.UsedBytes() >= c.QuotaBytes:
		return StatusQuotaExceeded
	default:
		return StatusActive
	}
}

// Admin is a panel administrator.
type Admin struct {
	ID                 int64  `json:"id"`
	Username           string `json:"username"`
	PasswordHash       string `json:"-"`
	MustChangePassword bool   `json:"must_change_password"`
	// TOTPEnabled reports whether two-step verification is active for this
	// administrator. The secrets themselves never reach the API.
	TOTPEnabled bool `json:"totp_enabled"`
	// TOTPSecret is the active secret; empty means no verified enrollment.
	// TOTPPendingSecret holds a proposed secret whose enrollment a client has
	// started but not confirmed with a live code.
	TOTPSecret        string `json:"-"`
	TOTPPendingSecret string `json:"-"`
	// TOTPLastStep is the newest time step already accepted. It makes a code
	// single use even when it is submitted twice in the same window.
	TOTPLastStep int64 `json:"-"`
	CreatedAt    int64 `json:"created_at"`
	LastLoginAt  int64 `json:"last_login_at"`
}

// DailyUsage is one day of traffic for a client.
type DailyUsage struct {
	Day       string `json:"day"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

// UsageSnapshot is one client's flushed counters, sent from the runtime to the
// store. UpBytes/DownBytes are absolute totals; DayUpBytes/DayDownBytes are
// increments for the given day.
type UsageSnapshot struct {
	ClientID     int64  `json:"client_id"`
	UpBytes      int64  `json:"up_bytes"`
	DownBytes    int64  `json:"down_bytes"`
	Day          string `json:"day"`
	DayUpBytes   int64  `json:"day_up_bytes"`
	DayDownBytes int64  `json:"day_down_bytes"`
}

// Settings keys stored in the settings table.
const (
	SettingPublicHost          = "public_host"
	SettingPublicPort          = "public_port"
	SettingSubscriptionEnabled = "subscription_enabled"
	SettingSessionSecret       = "session_secret"
	SettingAllowPrivate        = "allow_private_targets"
	SettingResetDay            = "reset_day"
	SettingTimezone            = "timezone"
	SettingAccessLog           = "access_log_enabled"
)

// Access record outcomes. They are stable identifiers the panel filters and
// labels, so treat them as part of the API surface.
const (
	AccessAllowed = "allowed"
	AccessBlocked = "blocked"
	AccessFailed  = "failed"
)

// Access record reasons explain a blocked or failed connection. See
// docs/api.md for the meaning of each value.
const (
	AccessReasonDisabled = "disabled"
	AccessReasonExpired  = "expired"
	AccessReasonQuota    = "quota"
	AccessReasonIPLimit  = "ip_limit"
	AccessReasonSessions = "sessions"
	AccessReasonPolicy   = "policy"
	AccessReasonResolve  = "resolve"
	AccessReasonDial     = "dial"
)

// AccessMaxHostsPerKey bounds how many distinct destinations one
// (day, client, inbound) key keeps in the daily rollup. The host is whatever the
// client asked for, so without a bound a client rotating subdomains could grow
// the table for the whole retention window. Overflow is folded into
// AccessOtherHost, which keeps the totals complete while bounding the key.
const AccessMaxHostsPerKey = 256

// AccessOtherHost is the rollup row that carries every destination folded away
// by the cap above. It never appears in the raw event list, where the real
// destination is still known.
const AccessOtherHost = "<other>"

// AccessRecord is one proxy access as recorded by the data plane: where a
// client asked to connect, whether it was allowed, and how much it moved.
//
// It deliberately carries no payload, URL, DNS query or client source address.
// Host is the destination the client requested (a domain name or an IP
// literal), already normalized by the runtime; IP is the address actually
// dialed and stays empty when the connection never got that far.
type AccessRecord struct {
	ClientID   int64  `json:"client_id"`
	InboundID  int64  `json:"inbound_id"`
	Network    string `json:"network"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	IP         string `json:"ip"`
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason"`
	UpBytes    int64  `json:"up_bytes"`
	DownBytes  int64  `json:"down_bytes"`
	StartedAt  int64  `json:"started_at"`
	DurationMS int64  `json:"duration_ms"`
	// Day is the accounting day (YYYY-MM-DD) in the effective time zone. The
	// store buckets the daily aggregate with it; an empty value stores the raw
	// event without updating the aggregate.
	Day string `json:"day"`
}

// AccessEvent is one stored access record with the names the panel displays.
type AccessEvent struct {
	ID            int64  `json:"id"`
	StartedAt     int64  `json:"started_at"`
	DurationMS    int64  `json:"duration_ms"`
	ClientID      int64  `json:"client_id"`
	ClientName    string `json:"client_name"`
	InboundID     int64  `json:"inbound_id"`
	InboundRemark string `json:"inbound_remark"`
	Network       string `json:"network"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	IP            string `json:"ip"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason"`
	UpBytes       int64  `json:"up_bytes"`
	DownBytes     int64  `json:"down_bytes"`
}

// AccessHostTotal is one row of the most requested destinations.
type AccessHostTotal struct {
	Host        string `json:"host"`
	Hits        int64  `json:"hits"`
	BlockedHits int64  `json:"blocked_hits"`
	UpBytes     int64  `json:"up_bytes"`
	DownBytes   int64  `json:"down_bytes"`
}

// AccessClientTotal is one row of the most active clients.
type AccessClientTotal struct {
	ClientID      int64  `json:"client_id"`
	Name          string `json:"name"`
	InboundID     int64  `json:"inbound_id"`
	InboundRemark string `json:"inbound_remark"`
	Hits          int64  `json:"hits"`
	UpBytes       int64  `json:"up_bytes"`
	DownBytes     int64  `json:"down_bytes"`
}

// AccessDayTotal is one day of the access trend.
type AccessDayTotal struct {
	Day         string `json:"day"`
	Hits        int64  `json:"hits"`
	AllowedHits int64  `json:"allowed_hits"`
	BlockedHits int64  `json:"blocked_hits"`
	FailedHits  int64  `json:"failed_hits"`
	UpBytes     int64  `json:"up_bytes"`
	DownBytes   int64  `json:"down_bytes"`
}

// AccessSummary aggregates the access records of a window. It is read from the
// daily rollup, never from the raw events, so the panel can show 90 days
// without scanning millions of rows.
type AccessSummary struct {
	TotalHits   int64               `json:"total_hits"`
	AllowedHits int64               `json:"allowed_hits"`
	BlockedHits int64               `json:"blocked_hits"`
	FailedHits  int64               `json:"failed_hits"`
	UniqueHosts int64               `json:"unique_hosts"`
	UpBytes     int64               `json:"up_bytes"`
	DownBytes   int64               `json:"down_bytes"`
	Daily       []AccessDayTotal    `json:"daily"`
	TopHosts    []AccessHostTotal   `json:"top_hosts"`
	TopClients  []AccessClientTotal `json:"top_clients"`
}

// Reset policy values.
const (
	ResetNone    = "none"
	ResetMonthly = "monthly"
)

// NowUnix returns the current unix time in seconds.
func NowUnix() int64 { return time.Now().Unix() }

// Audit actions recorded in the audit trail. They are stable identifiers the
// panel filters on, so treat them as part of the API surface.
const (
	AuditAuthLogin         = "auth.login"
	AuditAuthLoginFailed   = "auth.login.failed"
	AuditAuthLogout        = "auth.logout"
	AuditAdminPassword     = "admin.password"
	AuditAdminReauthFailed = "admin.reauth.failed"
	AuditAdminTOTPEnable   = "admin.totp.enable"
	AuditAdminTOTPDisable  = "admin.totp.disable"
	AuditAdminTOTPRecovery = "admin.totp.recovery"
	AuditInboundCreate     = "inbound.create"
	AuditInboundUpdate     = "inbound.update"
	AuditInboundDelete     = "inbound.delete"
	AuditInboundToggle     = "inbound.toggle"
	AuditInboundRestart    = "inbound.restart"
	AuditSettingsUpdate    = "settings.update"
	AuditClientCreate      = "client.create"
	AuditClientUpdate      = "client.update"
	AuditClientDelete      = "client.delete"
	AuditClientToggle      = "client.toggle"
	AuditClientReset       = "client.reset"
	AuditClientToken       = "client.token"
	AuditClientKick        = "client.kick"
	AuditClientImport      = "client.import"
	AuditClientExport      = "client.export"
)

// AuditActions lists every recorded action in display order.
var AuditActions = []string{
	AuditAuthLogin,
	AuditAuthLoginFailed,
	AuditAuthLogout,
	AuditAdminPassword,
	AuditAdminReauthFailed,
	AuditAdminTOTPEnable,
	AuditAdminTOTPDisable,
	AuditAdminTOTPRecovery,
	AuditInboundCreate,
	AuditInboundUpdate,
	AuditInboundDelete,
	AuditInboundToggle,
	AuditInboundRestart,
	AuditClientCreate,
	AuditClientUpdate,
	AuditClientDelete,
	AuditClientToggle,
	AuditClientReset,
	AuditClientToken,
	AuditClientKick,
	AuditClientImport,
	AuditClientExport,
	AuditSettingsUpdate,
}

// AuditLog is one recorded panel mutation or login attempt.
type AuditLog struct {
	ID         int64  `json:"id"`
	CreatedAt  int64  `json:"created_at"`
	AdminID    int64  `json:"admin_id"`
	Username   string `json:"username"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Detail     string `json:"detail"`
	IP         string `json:"ip"`
}
