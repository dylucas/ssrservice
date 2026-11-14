// Package config loads and validates the process configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings such as "10s".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var seconds int64
	if err := value.Decode(&seconds); err != nil {
		return fmt.Errorf("invalid duration: expected string or integer seconds")
	}
	*d = Duration(time.Duration(seconds) * time.Second)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Duration returns the standard library duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// PanelConfig configures the embedded web panel.
type PanelConfig struct {
	Listen         string   `yaml:"listen"`
	Port           int      `yaml:"port"`
	BasePath       string   `yaml:"base_path"`
	TLSCert        string   `yaml:"tls_cert"`
	TLSKey         string   `yaml:"tls_key"`
	SessionTTL     Duration `yaml:"session_ttl"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// DatabaseConfig configures the embedded SQLite database.
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// SecurityConfig holds defaults for security related runtime settings. The
// panel stores the effective values in the database; these are used to seed
// them on first start.
type SecurityConfig struct {
	AllowPrivateTargets bool `yaml:"allow_private_targets"`
	BlockMetadata       bool `yaml:"block_metadata"`
	LoginRateLimit      int  `yaml:"login_rate_limit"`
}

// TrafficConfig holds defaults for traffic related runtime settings.
type TrafficConfig struct {
	FlushInterval   Duration `yaml:"flush_interval"`
	EnforceInterval Duration `yaml:"enforce_interval"`
	ResetDay        int      `yaml:"reset_day"`
	Timezone        string   `yaml:"timezone"`
	UDPTimeout      Duration `yaml:"udp_timeout"`
}

// LogConfig configures logging.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Config is the root configuration.
type Config struct {
	Panel    PanelConfig    `yaml:"panel"`
	Database DatabaseConfig `yaml:"database"`
	Security SecurityConfig `yaml:"security"`
	Traffic  TrafficConfig  `yaml:"traffic"`
	Log      LogConfig      `yaml:"log"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Panel: PanelConfig{
			Listen:     "127.0.0.1",
			Port:       2096,
			BasePath:   "/",
			SessionTTL: Duration(24 * time.Hour),
		},
		Database: DatabaseConfig{Path: "./data/sspanel.db"},
		Security: SecurityConfig{
			AllowPrivateTargets: false,
			BlockMetadata:       true,
			LoginRateLimit:      5,
		},
		Traffic: TrafficConfig{
			FlushInterval:   Duration(10 * time.Second),
			EnforceInterval: Duration(30 * time.Second),
			ResetDay:        1,
			Timezone:        "Local",
			UDPTimeout:      Duration(60 * time.Second),
		},
		Log: LogConfig{Level: "info", Format: "text"},
	}
}

// Load reads the configuration file at path on top of the defaults. A missing
// file is not an error: defaults and environment variables are used.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// Fall through to defaults; the caller may create the file.
		default:
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}
	applyEnv(cfg)
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v, ok := os.LookupEnv("SSPANEL_PANEL_LISTEN"); ok {
		cfg.Panel.Listen = v
	}
	if v, ok := os.LookupEnv("SSPANEL_PANEL_PORT"); ok {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Panel.Port = port
		}
	}
	if v, ok := os.LookupEnv("SSPANEL_PANEL_BASE_PATH"); ok {
		cfg.Panel.BasePath = v
	}
	if v, ok := os.LookupEnv("SSPANEL_DB_PATH"); ok {
		cfg.Database.Path = v
	}
	if v, ok := os.LookupEnv("SSPANEL_LOG_LEVEL"); ok {
		cfg.Log.Level = v
	}
	if v, ok := os.LookupEnv("SSPANEL_LOG_FORMAT"); ok {
		cfg.Log.Format = v
	}
	if v, ok := os.LookupEnv("SSPANEL_TLS_CERT"); ok {
		cfg.Panel.TLSCert = v
	}
	if v, ok := os.LookupEnv("SSPANEL_TLS_KEY"); ok {
		cfg.Panel.TLSKey = v
	}
}

func (c *Config) normalize() {
	if c.Panel.Listen == "" {
		c.Panel.Listen = "127.0.0.1"
	}
	if c.Panel.BasePath == "" {
		c.Panel.BasePath = "/"
	}
	if !strings.HasPrefix(c.Panel.BasePath, "/") {
		c.Panel.BasePath = "/" + c.Panel.BasePath
	}
	if c.Panel.BasePath != "/" && strings.HasSuffix(c.Panel.BasePath, "/") {
		c.Panel.BasePath = strings.TrimRight(c.Panel.BasePath, "/")
	}
	if c.Panel.SessionTTL <= 0 {
		c.Panel.SessionTTL = Duration(24 * time.Hour)
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/sspanel.db"
	}
	if c.Traffic.FlushInterval < Duration(time.Second) {
		c.Traffic.FlushInterval = Duration(10 * time.Second)
	}
	if c.Traffic.EnforceInterval < Duration(time.Second) {
		c.Traffic.EnforceInterval = Duration(30 * time.Second)
	}
	if c.Traffic.UDPTimeout < Duration(10*time.Second) {
		c.Traffic.UDPTimeout = Duration(60 * time.Second)
	}
	if c.Traffic.Timezone == "" {
		c.Traffic.Timezone = "Local"
	}
	if c.Security.LoginRateLimit <= 0 {
		c.Security.LoginRateLimit = 5
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
}

// Validate checks the configuration for obvious errors.
func (c *Config) Validate() error {
	if c.Panel.Port < 1 || c.Panel.Port > 65535 {
		return fmt.Errorf("panel.port must be between 1 and 65535")
	}
	if ip := net.ParseIP(c.Panel.Listen); ip == nil && c.Panel.Listen != "localhost" {
		return fmt.Errorf("panel.listen %q is not a valid IP address", c.Panel.Listen)
	}
	if (c.Panel.TLSCert == "") != (c.Panel.TLSKey == "") {
		return fmt.Errorf("panel.tls_cert and panel.tls_key must be set together")
	}
	for _, proxy := range c.Panel.TrustedProxies {
		if net.ParseIP(proxy) == nil {
			if _, _, err := net.ParseCIDR(proxy); err != nil {
				return fmt.Errorf("panel.trusted_proxies: %q is not an IP or CIDR", proxy)
			}
		}
	}
	if c.Traffic.ResetDay < 0 || c.Traffic.ResetDay > 28 {
		return fmt.Errorf("traffic.reset_day must be between 0 and 28")
	}
	if _, err := time.LoadLocation(c.Traffic.Timezone); err != nil {
		return fmt.Errorf("traffic.timezone: %w", err)
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("log.level must be one of debug, info, warn, error")
	}
	switch strings.ToLower(c.Log.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("log.format must be text or json")
	}
	return nil
}

// Location returns the configured time zone.
func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Traffic.Timezone)
	if err != nil {
		return time.Local
	}
	return loc
}

// EnsureDataDir creates the parent directory of the database file.
func (c *Config) EnsureDataDir() error {
	dir := filepath.Dir(c.Database.Path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", dir, err)
	}
	return nil
}
