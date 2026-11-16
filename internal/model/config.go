package model

// This file defines the portable configuration snapshot used to move a panel
// setup between instances.
//
// The snapshot is deliberately a different shape from the domain types: it
// carries credentials (PSKs, password hash, TOTP secrets) that the API never
// returns, and it omits every historical table. Anyone who can read the file
// can impersonate the administrator and every client, so it is as sensitive as
// the database itself.

// ConfigSnapshotVersion is the only format accepted today. A future format
// change bumps this and ImportConfig rejects older/newer files instead of
// half-applying them.
const ConfigSnapshotVersion = 1

// ConfigSnapshot is the root of an exported configuration.
type ConfigSnapshot struct {
	Version    int             `json:"version"`
	ExportedAt int64           `json:"exported_at"`
	Settings   ConfigSettings  `json:"settings"`
	Admin      ConfigAdmin     `json:"admin"`
	Inbounds   []ConfigInbound `json:"inbounds"`
}

// ConfigSettings holds the panel settings that travel with a configuration.
// The session secret is intentionally absent: it is regenerated on a fresh
// install, and importing a snapshot invalidates all existing sessions.
type ConfigSettings struct {
	PublicHost          string `json:"public_host"`
	PublicPort          int    `json:"public_port"`
	SubscriptionEnabled bool   `json:"subscription_enabled"`
	AllowPrivateTargets bool   `json:"allow_private_targets"`
	ResetDay            int    `json:"reset_day"`
	Timezone            string `json:"timezone"`
	AccessLogEnabled    bool   `json:"access_log_enabled"`
}

// ConfigAdmin carries the administrator's credentials so a migrated instance
// keeps the same password and two-step verification state.
type ConfigAdmin struct {
	Username           string   `json:"username"`
	PasswordHash       string   `json:"password_hash"`
	MustChangePassword bool     `json:"must_change_password"`
	TOTPEnabled        bool     `json:"totp_enabled"`
	TOTPSecret         string   `json:"totp_secret,omitempty"`
	TOTPLastStep       int64    `json:"totp_last_step,omitempty"`
	RecoveryCodeHashes []string `json:"recovery_code_hashes,omitempty"`
}

// ConfigInbound is one listener and its users.
type ConfigInbound struct {
	Remark     string         `json:"remark"`
	Listen     string         `json:"listen"`
	Port       int            `json:"port"`
	Method     string         `json:"method"`
	ServerPSK  string         `json:"server_psk,omitempty"`
	Domain     string         `json:"domain"`
	DomainPort int            `json:"domain_port"`
	UDPEnabled bool           `json:"udp_enabled"`
	Enabled    bool           `json:"enabled"`
	Clients    []ConfigClient `json:"clients"`
}

// ConfigClient is one user of an inbound. Usage counters travel with the user
// so quotas and "expires soon" survive the move; the daily history does not.
type ConfigClient struct {
	Name        string `json:"name"`
	PSK         string `json:"psk"`
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
}

// ImportConfigResult reports what a successful import installed.
type ImportConfigResult struct {
	Inbounds int      `json:"inbounds"`
	Clients  int      `json:"clients"`
	Reloads  int      `json:"reloads"`
	Warnings []string `json:"warnings,omitempty"`
}
