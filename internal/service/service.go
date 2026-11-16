// Package service orchestrates persistence and the Shadowsocks runtime.
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// ValidationError marks user input errors.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// IsValidation reports whether err is a validation error.
func IsValidation(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

// Monitor is the subset of system information the dashboard needs.
type Monitor interface {
	Snapshot() (cpuPercent float64, memUsed, memTotal uint64)
}

// Service is the application layer used by the panel.
type Service struct {
	store  *store.Store
	mgr    *ssserver.Manager
	cfg    *config.Config
	logger *slog.Logger
	loc    *time.Location

	mu           sync.RWMutex
	publicHost   string
	publicPort   int
	subEnabled   bool
	allowPrivate bool
	resetDay     int
	accessLog    bool
	sessionTTL   time.Duration

	resetStop chan struct{}
	resetWG   sync.WaitGroup
	stopOnce  sync.Once

	// reloads counts inbound reloads. It is observability for tests and
	// diagnostics: a bulk operation must rebuild the listener once, not once per
	// user, and that property is otherwise invisible from the outside.
	reloads atomic.Int64
}

// New creates the application service.
func New(st *store.Store, mgr *ssserver.Manager, cfg *config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:      st,
		mgr:        mgr,
		cfg:        cfg,
		logger:     logger,
		loc:        cfg.Location(),
		resetStop:  make(chan struct{}),
		sessionTTL: cfg.Panel.SessionTTL.Duration(),
	}
}

// Bootstrap seeds defaults, creates the first administrator when needed,
// loads listeners and starts the background loops. It returns the generated
// admin password on first run (empty otherwise).
func (s *Service) Bootstrap() (string, error) {
	if err := s.seedSettings(); err != nil {
		return "", err
	}
	if err := s.loadSettings(); err != nil {
		return "", err
	}
	password, err := s.ensureAdmin()
	if err != nil {
		return "", err
	}
	if err := s.LoadRuntime(); err != nil {
		// Not fatal: the panel must still start so the operator can fix the
		// conflicting port. It is logged at error level and repeated per inbound
		// in the panel (InboundView.LastError), because "the process is healthy
		// but a listener is not" is invisible in a warn line nobody reads.
		s.logger.Error("some inbounds failed to start; see the inbound list for the reason", "error", err)
	}
	s.mgr.Start()
	s.startResetLoop()
	return password, nil
}

// SessionSecret returns the persisted panel session secret.
func (s *Service) SessionSecret() ([]byte, error) {
	value, err := s.store.GetSetting(model.SettingSessionSecret)
	if err != nil {
		return nil, err
	}
	return []byte(value), nil
}

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration {
	if s.sessionTTL <= 0 {
		return 24 * time.Hour
	}
	return s.sessionTTL
}

func (s *Service) seedSettings() error {
	defaults := map[string]string{}
	if _, err := s.store.GetSetting(model.SettingSessionSecret); errors.Is(err, store.ErrNotFound) {
		secret, err := auth.RandomToken(32)
		if err != nil {
			return err
		}
		defaults[model.SettingSessionSecret] = secret
	}
	if _, err := s.store.GetSetting(model.SettingPublicHost); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingPublicHost] = ""
	}
	if _, err := s.store.GetSetting(model.SettingPublicPort); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingPublicPort] = "0"
	}
	if _, err := s.store.GetSetting(model.SettingSubscriptionEnabled); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingSubscriptionEnabled] = "true"
	}
	if _, err := s.store.GetSetting(model.SettingAllowPrivate); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingAllowPrivate] = strconv.FormatBool(s.cfg.Security.AllowPrivateTargets)
	}
	if _, err := s.store.GetSetting(model.SettingResetDay); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingResetDay] = strconv.Itoa(s.cfg.Traffic.ResetDay)
	}
	if _, err := s.store.GetSetting(model.SettingTimezone); errors.Is(err, store.ErrNotFound) {
		defaults[model.SettingTimezone] = s.cfg.Traffic.Timezone
	}
	if _, err := s.store.GetSetting(model.SettingAccessLog); errors.Is(err, store.ErrNotFound) {
		// Access history is browsing history; it stays off until the operator
		// asks for it in the settings page.
		defaults[model.SettingAccessLog] = "false"
	}
	if len(defaults) == 0 {
		return nil
	}
	return s.store.SetSettings(defaults)
}

func (s *Service) loadSettings() error {
	values, err := s.store.AllSettings()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.publicHost = values[model.SettingPublicHost]
	s.publicPort = parseInt(values[model.SettingPublicPort])
	s.subEnabled = values[model.SettingSubscriptionEnabled] != "false"
	s.allowPrivate = values[model.SettingAllowPrivate] == "true"
	s.resetDay = parseInt(values[model.SettingResetDay])
	s.accessLog = values[model.SettingAccessLog] == "true"
	if tz := values[model.SettingTimezone]; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			s.loc = loc
		}
	}
	loc := s.loc
	accessLog := s.accessLog
	s.mu.Unlock()
	// Keep the traffic accounting time zone in sync with the panel setting so
	// daily buckets and monthly resets do not need a restart to follow it.
	s.mgr.SetLocation(loc)
	s.mgr.SetAccessEnabled(accessLog)
	s.applyPolicy()
	return nil
}

func (s *Service) applyPolicy() {
	s.mu.RLock()
	allow := s.allowPrivate
	s.mu.RUnlock()
	s.mgr.SetPolicy(ssserver.PolicyConfig{
		AllowPrivateTargets: allow,
		BlockMetadata:       s.cfg.Security.BlockMetadata,
		PanelListen:         s.cfg.Panel.Listen,
		PanelPort:           s.cfg.Panel.Port,
	})
}

func (s *Service) ensureAdmin() (string, error) {
	count, err := s.store.AdminCount()
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}
	username := "admin"
	password := ""
	fromEnv := false
	if env := os.Getenv("SSPANEL_ADMIN_PASSWORD"); env != "" {
		password = env
		fromEnv = true
	} else {
		generated, err := auth.RandomPassword(16)
		if err != nil {
			return "", err
		}
		password = generated
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := s.store.CreateAdmin(username, hash, !fromEnv); err != nil {
		return "", err
	}
	s.logger.Info("created administrator", "username", username, "must_change_password", !fromEnv)
	return password, nil
}

// LoadRuntime starts the listeners for all persisted inbounds.
func (s *Service) LoadRuntime() error {
	inbounds, err := s.store.ListInbounds()
	if err != nil {
		return err
	}
	clients, err := s.store.ListClients(0)
	if err != nil {
		return err
	}
	return s.mgr.Load(inbounds, clients)
}

// Stop flushes counters and stops the background loops.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.resetStop)
		s.resetWG.Wait()
		s.mgr.Stop()
	})
}

// ---- settings -------------------------------------------------------------

// SettingsView is the runtime settings exposed to the panel.
type SettingsView struct {
	PublicHost     string `json:"public_host"`
	PublicPort     int    `json:"public_port"`
	SubscriptionOn bool   `json:"subscription_enabled"`
	AllowPrivate   bool   `json:"allow_private_targets"`
	ResetDay       int    `json:"reset_day"`
	Timezone       string `json:"timezone"`
	AccessLog      bool   `json:"access_log_enabled"`
}

// Settings returns the current runtime settings.
func (s *Service) Settings() SettingsView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SettingsView{
		PublicHost:     s.publicHost,
		PublicPort:     s.publicPort,
		SubscriptionOn: s.subEnabled,
		AllowPrivate:   s.allowPrivate,
		ResetDay:       s.resetDay,
		Timezone:       s.loc.String(),
		AccessLog:      s.accessLog,
	}
}

// UpdateSettings validates and persists runtime settings.
func (s *Service) UpdateSettings(in SettingsView) error {
	if in.PublicPort < 0 || in.PublicPort > 65535 {
		return invalid("public_port must be between 0 and 65535")
	}
	if in.ResetDay < 0 || in.ResetDay > 28 {
		return invalid("reset_day must be between 0 and 28")
	}
	// PUT /settings replaces the whole object, so an empty timezone is a client
	// bug, not a request to use UTC: time.LoadLocation("") succeeds and would
	// silently move every accounting day to UTC.
	if strings.TrimSpace(in.Timezone) == "" {
		return invalid("timezone is required")
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return invalid("invalid timezone: %v", err)
	}
	// Store the normalized form, not the raw field: " Example.COM. " passes
	// validation but would end up inside every share link and Clash config.
	host := normalizeHost(in.PublicHost)
	if host != "" && !validHost(host) {
		return invalid("public_host must be a hostname or IP address")
	}
	values := map[string]string{
		model.SettingPublicHost:          host,
		model.SettingPublicPort:          strconv.Itoa(in.PublicPort),
		model.SettingSubscriptionEnabled: strconv.FormatBool(in.SubscriptionOn),
		model.SettingAllowPrivate:        strconv.FormatBool(in.AllowPrivate),
		model.SettingResetDay:            strconv.Itoa(in.ResetDay),
		model.SettingTimezone:            loc.String(),
		model.SettingAccessLog:           strconv.FormatBool(in.AccessLog),
	}
	if err := s.store.SetSettings(values); err != nil {
		return err
	}
	s.mu.Lock()
	s.publicHost = host
	s.publicPort = in.PublicPort
	s.subEnabled = in.SubscriptionOn
	s.allowPrivate = in.AllowPrivate
	s.resetDay = in.ResetDay
	s.accessLog = in.AccessLog
	s.loc = loc
	s.mu.Unlock()
	s.mgr.SetLocation(loc)
	s.mgr.SetAccessEnabled(in.AccessLog)
	s.applyPolicy()
	return nil
}

func (s *Service) withinSubscription() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.subEnabled
}

func (s *Service) location() *time.Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loc
}

// Location returns the time zone the daily buckets and monthly resets use.
//
// The panel must build its date windows from this and not from the
// configuration file: the operator can change the time zone in the settings
// page without restarting, and at that point the config value is stale. The
// dashboard read the config while the per-client usage read this, so the same
// page could describe two different 30 day windows.
func (s *Service) Location() *time.Location { return s.location() }

// DailyTotalsSince returns the per-day traffic rollup of the last `days` days,
// evaluated in the effective time zone.
//
// The lower bound is days-1 back because the query keeps every day from it
// onward: counting back `days` would return one bucket more than the caller
// asked for and make "the last 30 days" 31 points on the chart.
func (s *Service) DailyTotalsSince(days int) ([]model.DailyUsage, error) {
	if days <= 0 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -(days - 1)).In(s.location()).Format("2006-01-02")
	return s.store.DailyTotals(since)
}

// Backup writes a database snapshot to path.
func (s *Service) Backup(path string) error {
	return s.store.BackupTo(path)
}

// Store exposes the store for the panel layer.
func (s *Service) Store() *store.Store { return s.store }

// Manager exposes the runtime for the panel layer.
func (s *Service) Manager() *ssserver.Manager { return s.mgr }

// Config exposes the configuration.
func (s *Service) Config() *config.Config { return s.cfg }

// Logger exposes the logger.
func (s *Service) Logger() *slog.Logger { return s.logger }

// Counts returns row counts.
func (s *Service) Counts() (store.Counts, error) { return s.store.Stats() }

func parseInt(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

// maxHostNameLen is the DNS limit for a fully qualified name.
const maxHostNameLen = 253

// maxHostLabelLen is the DNS limit for one label.
const maxHostLabelLen = 63

// normalizeHost lowercases a host value and strips one trailing dot (the absolute
// FQDN form), so "Example.COM." and "example.com" compare and display the same.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	// A value of just "." trims to the empty string, which callers treat as unset.
	return host
}

// validateHostValue checks a hostname or IP literal used in share links.
//
// It is deliberately stricter than a character allowlist: a scheme, a port or
// bracketed IPv6 in this field would end up inside an ss:// URI and produce a
// link no client can parse. Brackets are rejected because the IPv6 form is
// written bare here and bracketed only when the URI is assembled.
func validateHostValue(host string) error {
	if host == "" {
		return errors.New("host must not be empty")
	}
	if len(host) > maxHostNameLen {
		return fmt.Errorf("host must be at most %d characters", maxHostNameLen)
	}
	// An IP literal is valid as-is; a zone suffix is not meaningful in a share link.
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return errors.New("host must not contain an IPv6 zone suffix")
		}
		return nil
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			continue
		default:
			return fmt.Errorf("host contains an invalid character %q", r)
		}
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return errors.New("host must not contain empty labels")
		}
		if len(label) > maxHostLabelLen {
			return fmt.Errorf("each part of host must be at most %d characters", maxHostLabelLen)
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("host parts must not start or end with a hyphen")
		}
	}
	return nil
}

// validHost reports whether host passes validateHostValue.
func validHost(host string) bool {
	return validateHostValue(normalizeHost(host)) == nil
}

// SubscriptionEnabled reports whether public subscriptions are enabled.
func (s *Service) SubscriptionEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.subEnabled
}

// PublicEndpoint returns the host and port used in share links.
func (s *Service) PublicEndpoint(in *model.Inbound) (string, int) {
	return s.resolveHostPort(in, "")
}

// TrafficTotalsLive returns aggregate counters including unflushed traffic.
// It reads the in-memory accounting state instead of scanning the clients
// table, which keeps dashboard polling cheap as the user count grows.
func (s *Service) TrafficTotalsLive() (up, down int64, err error) {
	up, down = s.mgr.TotalTraffic()
	return up, down, nil
}

// OnlineIPCount returns the number of distinct connected source IPs.
func (s *Service) OnlineIPCount() int {
	total := 0
	for _, ips := range s.mgr.OnlineIPs() {
		total += len(ips)
	}
	return total
}

// SessionCount returns the number of live proxy sessions.
func (s *Service) SessionCount() int { return s.mgr.SessionCount() }
