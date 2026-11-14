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
	ID         int64  `json:"id"`
	Remark     string `json:"remark"`
	Listen     string `json:"listen"`
	Port       int    `json:"port"`
	Method     string `json:"method"`
	ServerPSK  string `json:"server_psk,omitempty"`
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
	CreatedAt          int64  `json:"created_at"`
	LastLoginAt        int64  `json:"last_login_at"`
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
)

// Reset policy values.
const (
	ResetNone    = "none"
	ResetMonthly = "monthly"
)

// NowUnix returns the current unix time in seconds.
func NowUnix() int64 { return time.Now().Unix() }
