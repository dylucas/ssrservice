// Package service orchestrates persistence and the Shadowsocks runtime.
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
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
	sessionTTL   time.Duration

	resetStop chan struct{}
	resetWG   sync.WaitGroup
	stopOnce  sync.Once
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
		s.logger.Warn("some inbounds failed to start", "error", err)
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
	if tz := values[model.SettingTimezone]; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			s.loc = loc
		}
	}
	loc := s.loc
	s.mu.Unlock()
	// Keep the traffic accounting time zone in sync with the panel setting so
	// daily buckets and monthly resets do not need a restart to follow it.
	s.mgr.SetLocation(loc)
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
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return invalid("invalid timezone: %v", err)
	}
	host := in.PublicHost
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
	s.loc = loc
	s.mu.Unlock()
	s.mgr.SetLocation(loc)
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

func validHost(host string) bool {
	if host == "" {
		return false
	}
	for _, r := range host {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == ':' || r == '[' || r == ']' {
			continue
		}
		return false
	}
	return true
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
