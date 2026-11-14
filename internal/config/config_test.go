package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if cfg.Panel.Port != 2096 || cfg.Panel.Listen != "127.0.0.1" {
		t.Fatalf("unexpected panel defaults: %+v", cfg.Panel)
	}
	if cfg.Traffic.FlushInterval.Duration() != 10*time.Second {
		t.Fatalf("unexpected flush interval: %v", cfg.Traffic.FlushInterval)
	}
}

func TestLoadFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
panel:
  listen: 0.0.0.0
  port: 8080
database:
  path: ` + dir + `/data.db
traffic:
  flush_interval: 3s
  reset_day: 9
  timezone: Asia/Shanghai
log:
  level: debug
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSPANEL_PANEL_PORT", "9090")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Panel.Listen != "0.0.0.0" || cfg.Panel.Port != 9090 {
		t.Fatalf("env override failed: %+v", cfg.Panel)
	}
	if cfg.Traffic.FlushInterval.Duration() != 3*time.Second {
		t.Fatalf("duration parse failed: %v", cfg.Traffic.FlushInterval)
	}
	if cfg.Traffic.ResetDay != 9 || cfg.Log.Level != "debug" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Location().String() != "Asia/Shanghai" {
		t.Fatalf("unexpected location %q", cfg.Location())
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad port", func(c *Config) { c.Panel.Port = 70000 }},
		{"bad listen", func(c *Config) { c.Panel.Listen = "not-an-ip" }},
		{"tls cert only", func(c *Config) { c.Panel.TLSCert = "/tmp/cert" }},
		{"bad reset day", func(c *Config) { c.Traffic.ResetDay = 31 }},
		{"bad timezone", func(c *Config) { c.Traffic.Timezone = "Nowhere/Zone" }},
		{"bad log level", func(c *Config) { c.Log.Level = "verbose" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			cfg.normalize()
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestBasePathNormalize(t *testing.T) {
	cfg := Default()
	cfg.Panel.BasePath = "panel/"
	cfg.normalize()
	if cfg.Panel.BasePath != "/panel" {
		t.Fatalf("unexpected base path %q", cfg.Panel.BasePath)
	}
}

// TestLocalhostListenIsNormalized pins down that the only hostname the
// validator accepts becomes a concrete address.
//
// config.Validate allows "localhost" but the proxy policy compares the panel
// listen address against resolved destination IPs, where a hostname can never
// match. Leaving it unnormalized silently disabled the "the panel port is not
// reachable through the proxy" block, which matters as soon as an operator
// enables allow_private_targets.
func TestLocalhostListenIsNormalized(t *testing.T) {
	for _, listen := range []string{"localhost", "LocalHost", " localhost "} {
		t.Run(listen, func(t *testing.T) {
			cfg := Default()
			cfg.Panel.Listen = listen
			cfg.normalize()
			if cfg.Panel.Listen != "127.0.0.1" {
				t.Fatalf("panel.listen %q normalized to %q, want 127.0.0.1", listen, cfg.Panel.Listen)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("normalized config must validate: %v", err)
			}
		})
	}
}
