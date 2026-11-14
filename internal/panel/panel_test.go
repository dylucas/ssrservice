package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
	"github.com/ydong/ssrservice/internal/webui"
)

type testPanel struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	store   *store.Store
	svc     *service.Service
	cookies []*http.Cookie
	client  *http.Client
}

func init() {
	// Keep bcrypt fast in tests; production uses the default cost.
	auth.BcryptCost = 4
}

func newTestPanel(t *testing.T) *testPanel {
	t.Helper()
	return newTestPanelWith(t, nil)
}

// newTestPanelWith builds a panel after applying mutate to the configuration.
func newTestPanelWith(t *testing.T, mutate func(*config.Config)) *testPanel {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "panel.db")
	cfg.Panel.Port = 2096
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	mgr := ssserver.New(ssserver.Options{
		Sink:            st,
		Location:        time.UTC,
		FlushInterval:   time.Hour,
		EnforceInterval: time.Hour,
		UDPTimeout:      10 * time.Second,
		Policy:          ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	svc := service.New(st, mgr, cfg, nil)
	if _, err := svc.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	secret, err := svc.SessionSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	sessions, err := auth.NewSessions(secret, time.Hour)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	srv := New(cfg, svc, nil, sessions)
	tp := &testPanel{t: t, server: srv, handler: srv.Handler(), store: st, svc: svc,
		client: &http.Client{Timeout: 5 * time.Second}}
	t.Cleanup(func() {
		svc.Stop()
		st.Close()
	})
	return tp
}

func (tp *testPanel) do(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	tp.t.Helper()
	return tp.doFrom(method, path, body, headers, "")
}

// doFrom is do with an explicit client remote address.
func (tp *testPanel) doFrom(method, path string, body any, headers map[string]string, remoteAddr string) *httptest.ResponseRecorder {
	tp.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			tp.t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, cookie := range tp.cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	tp.handler.ServeHTTP(rec, req)
	// Remember session cookies.
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			tp.cookies = []*http.Cookie{cookie}
		}
	}
	return rec
}

func (tp *testPanel) login() {
	tp.t.Helper()
	// Reset the generated password to a known value.
	admin, err := tp.store.AdminByUsername("admin")
	if err != nil {
		tp.t.Fatalf("admin: %v", err)
	}
	hash, err := auth.HashPassword("test-password-123")
	if err != nil {
		tp.t.Fatalf("hash: %v", err)
	}
	if err := tp.store.UpdateAdminPassword(admin.ID, hash, false); err != nil {
		tp.t.Fatalf("update: %v", err)
	}
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, nil)
	if rec.Code != http.StatusOK {
		tp.t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestLoginAndAuthFlow(t *testing.T) {
	tp := newTestPanel(t)
	if rec := tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	admin, _ := tp.store.AdminByUsername("admin")
	hash, _ := auth.HashPassword("test-password-123")
	_ = tp.store.UpdateAdminPassword(admin.ID, hash, false)

	if rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong"}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "admin", "password": "test-password-123"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	me := tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil)
	if me.Code != http.StatusOK {
		t.Fatalf("me: %d %s", me.Code, me.Body.String())
	}
	// Cross-origin writes are rejected.
	rec = tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "x", "listen": "127.0.0.1", "port": 25000,
		"method": model.Method2022AES128GCM, "enabled": true,
	}, map[string]string{"Origin": "https://evil.example"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected origin rejection, got %d %s", rec.Code, rec.Body.String())
	}
	// Logout invalidates the session.
	if rec := tp.do(http.MethodPost, "/api/v1/auth/logout", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 after logout, got %d", rec.Code)
	}
}

func TestChangePasswordInvalidatesOldSessions(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "test-password-123",
		"new_password":     "another-password-456",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tp.do(http.MethodGet, "/api/v1/auth/me", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("session should survive password change, got %d", rec.Code)
	}
	// Old password no longer works.
	tp.cookies = nil
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password still accepted: %d", rec.Code)
	}
	if rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "another-password-456",
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("new password rejected: %d", rec.Code)
	}
}

func TestInboundAndClientAPI(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	port := freeTestPort(t)

	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "api-test", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "udp_enabled": true, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	if in.ID == 0 || in.ServerPSK == "" {
		t.Fatalf("unexpected inbound %+v", in)
	}
	// Duplicate port is rejected.
	rec = tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "dup", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
		t.Fatalf("expected conflict, got %d %s", rec.Code, rec.Body.String())
	}

	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "alice", "enabled": true, "quota_bytes": 1024,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)
	if client.PSK == "" || client.SubToken == "" {
		t.Fatalf("client secrets missing: %+v", client)
	}

	rec = tp.do(http.MethodGet, "/api/v1/clients/"+itoa(client.ID)+"/link", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: %d %s", rec.Code, rec.Body.String())
	}
	link := decode[struct {
		Link string `json:"link"`
		Sub  string `json:"subscription_url"`
	}](t, rec)
	if link.Link == "" || link.Sub == "" {
		t.Fatalf("unexpected link payload %+v", link)
	}

	// Subscription endpoint returns the base64 payload.
	sub := tp.do(http.MethodGet, "/sub/"+client.SubToken, nil, nil)
	if sub.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", sub.Code, sub.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(sub.Body.String())
	if err != nil || len(decoded) == 0 {
		t.Fatalf("invalid subscription payload %q", sub.Body.String())
	}

	// Clash user agent gets YAML.
	sub = tp.do(http.MethodGet, "/sub/"+client.SubToken, nil, map[string]string{"User-Agent": "ClashForWindows/0.20"})
	if sub.Code != http.StatusOK || !bytes.Contains(sub.Body.Bytes(), []byte("proxies:")) {
		t.Fatalf("clash subscription failed: %d %s", sub.Code, sub.Body.String())
	}

	// Toggle the client; the subscription becomes unavailable.
	rec = tp.do(http.MethodPost, "/api/v1/clients/"+itoa(client.ID)+"/toggle", map[string]bool{"enabled": false}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
	}
	if sub := tp.do(http.MethodGet, "/sub/"+client.SubToken, nil, nil); sub.Code != http.StatusNotFound {
		t.Fatalf("disabled client subscription should be 404, got %d", sub.Code)
	}

	// Reset traffic and token.
	for _, action := range []string{"reset", "kick", "token"} {
		rec = tp.do(http.MethodPost, "/api/v1/clients/"+itoa(client.ID)+"/"+action, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, rec.Code, rec.Body.String())
		}
	}

	// Invalid method is rejected.
	rec = tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "bad", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": "rc4-md5", "enabled": true,
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad method, got %d %s", rec.Code, rec.Body.String())
	}

	// Delete.
	if rec := tp.do(http.MethodDelete, "/api/v1/inbounds/"+itoa(in.ID), nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tp.do(http.MethodGet, "/api/v1/inbounds/"+itoa(in.ID), nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUpdateWithoutPSKKeepsSecrets(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "keep", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "udp_enabled": true, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	serverPSK := in.ServerPSK

	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{"name": "alice", "enabled": true}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)
	clientPSK := client.PSK

	// Update both without sending the secrets.
	if rec := tp.do(http.MethodPut, "/api/v1/inbounds/"+itoa(in.ID), map[string]any{
		"remark": "keep-updated", "listen": "127.0.0.1", "port": in.Port,
		"method": model.Method2022AES128GCM, "server_psk": "", "udp_enabled": true, "enabled": true,
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("update inbound: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tp.do(http.MethodPut, "/api/v1/clients/"+itoa(client.ID), map[string]any{
		"name": "alice", "psk": "", "enabled": true, "quota_bytes": 2048,
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("update client: %d %s", rec.Code, rec.Body.String())
	}

	storedIn, err := tp.store.InboundByID(in.ID)
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if storedIn.ServerPSK != serverPSK {
		t.Fatalf("server PSK changed unexpectedly: %q != %q", storedIn.ServerPSK, serverPSK)
	}
	storedClient, err := tp.store.ClientByID(client.ID)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if storedClient.PSK != clientPSK {
		t.Fatalf("client PSK changed unexpectedly")
	}
	if storedClient.QuotaBytes != 2048 {
		t.Fatalf("quota not updated: %d", storedClient.QuotaBytes)
	}
}

func TestSingleUserInboundRejectsSecondClient(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "solo", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022ChaCha20, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	first := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{"name": "a", "enabled": true}, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first client: %d %s", first.Code, first.Body.String())
	}
	second := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{"name": "b", "enabled": true}, nil)
	if second.Code != http.StatusBadRequest {
		t.Fatalf("expected rejection of second client, got %d %s", second.Code, second.Body.String())
	}
}

func TestDashboardSettingsBackup(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodGet, "/api/v1/dashboard", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Body.String())
	}
	rec = tp.do(http.MethodGet, "/api/v1/system", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("system: %d %s", rec.Code, rec.Body.String())
	}

	rec = tp.do(http.MethodGet, "/api/v1/settings", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	settings := decode[service.SettingsView](t, rec)
	settings.PublicHost = "example.com"
	settings.PublicPort = 8443
	settings.ResetDay = 5
	settings.Timezone = "Asia/Shanghai"
	rec = tp.do(http.MethodPut, "/api/v1/settings", settings, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update settings: %d %s", rec.Code, rec.Body.String())
	}
	got := decode[service.SettingsView](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))
	if got.PublicHost != "example.com" || got.PublicPort != 8443 || got.ResetDay != 5 {
		t.Fatalf("settings not persisted: %+v", got)
	}
	// Invalid timezone is rejected.
	bad := got
	bad.Timezone = "Bad/Zone"
	if rec := tp.do(http.MethodPut, "/api/v1/settings", bad, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	rec = tp.do(http.MethodGet, "/api/v1/backup", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("backup: %d len=%d", rec.Code, rec.Body.Len())
	}
}

func TestHealthzAndFrontendFallback(t *testing.T) {
	tp := newTestPanel(t)
	rec := tp.do(http.MethodGet, "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
	rec = tp.do(http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("index: %d", rec.Code)
	}
}

func freeTestPort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			continue
		}
		udp.Close()
		return port
	}
	t.Fatal("no free port")
	return 0
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

var _ = gin.ReleaseMode

func TestForcedPasswordChangeFlow(t *testing.T) {
	tp := newTestPanel(t)
	// Simulate the first-run state: known password, must change.
	admin, _ := tp.store.AdminByUsername("admin")
	hash, _ := auth.HashPassword("initial-password-1")
	if err := tp.store.UpdateAdminPassword(admin.ID, hash, true); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "initial-password-1",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	// Business endpoints are blocked.
	if rec := tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d %s", rec.Code, rec.Body.String())
	}
	// The password change endpoint must remain reachable.
	rec = tp.do(http.MethodPut, "/api/v1/auth/password", map[string]string{
		"current_password": "initial-password-1",
		"new_password":     "changed-password-2",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", rec.Code, rec.Body.String())
	}
	if rec := tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected access after change, got %d %s", rec.Code, rec.Body.String())
	}
}

// indexRuntimeConfig extracts and decodes the JSON configuration block served
// with index.html.
func indexRuntimeConfig(t *testing.T, body string) runtimeConfig {
	t.Helper()
	marker := `<script id="sspanel-config" type="application/json">`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("runtime config block missing in %q", body)
	}
	start += len(marker)
	end := strings.Index(body[start:], "</script>")
	if end < 0 {
		t.Fatalf("unterminated runtime config block")
	}
	var cfg runtimeConfig
	if err := json.Unmarshal([]byte(body[start:start+end]), &cfg); err != nil {
		t.Fatalf("decode runtime config: %v", err)
	}
	return cfg
}

// requireBuiltFrontend skips tests that assert on the embedded frontend
// assets. The assets are a build artifact (make web), so a checkout without a
// frontend build can only exercise the "not built" placeholder.
func requireBuiltFrontend(t *testing.T) {
	t.Helper()
	if !webui.Built() {
		t.Skip("frontend not built; run make web first")
	}
}

func TestIndexServesJSONRuntimeConfig(t *testing.T) {
	requireBuiltFrontend(t)
	tp := newTestPanel(t)
	rec := tp.do(http.MethodGet, "/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("index: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "window.__SSPANEL__") {
		t.Fatalf("executable inline config should be gone")
	}
	cfg := indexRuntimeConfig(t, body)
	if cfg.BasePath != "/" {
		t.Fatalf("unexpected base path %q", cfg.BasePath)
	}
	if cfg.Version == "" {
		t.Fatalf("version must be injected")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("unexpected cache-control %q", got)
	}
}

func TestBasePathRouting(t *testing.T) {
	tp := newTestPanelWith(t, func(cfg *config.Config) {
		cfg.Panel.BasePath = "/panel"
	})

	rec := tp.do(http.MethodGet, "/panel", nil, nil)
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected 308, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/panel/" {
		t.Fatalf("unexpected redirect target %q", loc)
	}

	// The index under the base path is served from the embedded build, which a
	// checkout without a frontend build does not have.
	if webui.Built() {
		rec = tp.do(http.MethodGet, "/panel/", nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("index under base path: %d", rec.Code)
		}
		if cfg := indexRuntimeConfig(t, rec.Body.String()); cfg.BasePath != "/panel/" {
			t.Fatalf("unexpected base path %q", cfg.BasePath)
		}
	}

	rec = tp.do(http.MethodGet, "/panel/api/v1/auth/me", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected JSON error, got %q", ct)
	}
}

func TestHashedAssetsAreImmutable(t *testing.T) {
	requireBuiltFrontend(t)
	entries, err := fs.ReadDir(webui.FS(), "assets")
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	name := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			name = entry.Name()
			break
		}
	}
	if name == "" {
		t.Skip("no built assets embedded")
	}
	tp := newTestPanel(t)
	rec := tp.do(http.MethodGet, "/assets/"+name, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("expected immutable cache header, got %q", got)
	}
}

func TestForwardedForIgnoredByDefault(t *testing.T) {
	tp := newTestPanel(t)
	const remote = "203.0.113.7:4000"
	for i := 0; i < 5; i++ {
		rec := tp.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "wrong",
		}, map[string]string{"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i+1)}, remote)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rec.Code)
		}
	}
	// A different spoofed address must not grant a fresh bucket.
	rec := tp.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "wrong",
	}, map[string]string{"X-Forwarded-For": "198.51.100.200"}, remote)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected spoofed X-Forwarded-For to be ignored, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestForwardedForHonoredByTrustedProxy(t *testing.T) {
	tp := newTestPanelWith(t, func(cfg *config.Config) {
		cfg.Panel.TrustedProxies = []string{"127.0.0.1"}
	})
	const remote = "127.0.0.1:5555"
	for i := 0; i < 5; i++ {
		rec := tp.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
			"username": "admin", "password": "wrong",
		}, map[string]string{"X-Forwarded-For": "198.51.100.10"}, remote)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i+1, rec.Code)
		}
	}
	rec := tp.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "wrong",
	}, map[string]string{"X-Forwarded-For": "198.51.100.10"}, remote)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for the repeated client, got %d", rec.Code)
	}
	// Another client behind the same proxy has its own bucket.
	rec = tp.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "wrong",
	}, map[string]string{"X-Forwarded-For": "198.51.100.11"}, remote)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a new client, got %d", rec.Code)
	}
}

func TestSecureCookieRequiresTrustedProxy(t *testing.T) {
	newPanel := func(trusted bool) *testPanel {
		tp := newTestPanelWith(t, func(cfg *config.Config) {
			if trusted {
				cfg.Panel.TrustedProxies = []string{"127.0.0.1"}
			}
		})
		tp.login()
		return tp
	}

	trusted := newPanel(true)
	rec := trusted.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, map[string]string{"X-Forwarded-Proto": "https"}, "127.0.0.1:4444")
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted login: %d %s", rec.Code, rec.Body.String())
	}
	if cookie := sessionCookie(t, rec); !cookie.Secure {
		t.Fatalf("expected a Secure cookie behind a trusted TLS proxy")
	}
	if hsts := rec.Header().Get("Strict-Transport-Security"); hsts == "" {
		t.Fatalf("expected HSTS behind a trusted TLS proxy")
	}

	plain := newPanel(false)
	rec = plain.doFrom(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "test-password-123",
	}, map[string]string{"X-Forwarded-Proto": "https"}, "192.0.2.1:4321")
	if rec.Code != http.StatusOK {
		t.Fatalf("plain login: %d %s", rec.Code, rec.Body.String())
	}
	if cookie := sessionCookie(t, rec); cookie.Secure {
		t.Fatalf("spoofed X-Forwarded-Proto must not set Secure")
	}
	if hsts := rec.Header().Get("Strict-Transport-Security"); hsts != "" {
		t.Fatalf("spoofed X-Forwarded-Proto must not set HSTS")
	}
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.SessionCookieName {
			return cookie
		}
	}
	t.Fatalf("session cookie missing")
	return nil
}

func TestAPIBodyLimit(t *testing.T) {
	tp := newTestPanel(t)
	rec := tp.do(http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": strings.Repeat("a", maxAPIRequestBody+1024),
	}, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d %s", rec.Code, rec.Body.String())
	}
	var payload errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Error.Code != "payload_too_large" {
		t.Fatalf("unexpected error code %q", payload.Error.Code)
	}
}

// TestDeleteInboundClearsAccounting verifies that deleting an inbound also
// reclaims the in-memory accounts of its clients, so the dashboard totals stop
// counting removed users.
func TestDeleteInboundClearsAccounting(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	port := freeTestPort(t)

	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "delete-accounting", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "udp_enabled": true, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)

	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "accounted", "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)

	// Give the client persisted counters, then reload them into the runtime so
	// the live totals are non-zero before the delete.
	if err := tp.store.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 1234, DownBytes: 5678,
		Day: "2026-06-01", DayUpBytes: 1234, DayDownBytes: 5678,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	persisted, err := tp.store.ClientByID(client.ID)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	// Drop the account created when the client was added, then seed it with the
	// persisted counters.
	tp.svc.Manager().RemoveClient(client.ID)
	tp.svc.Manager().SeedUsage([]model.Client{*persisted})
	if up, down := tp.svc.Manager().TotalTraffic(); up != 1234 || down != 5678 {
		t.Fatalf("unexpected seeded totals: up=%d down=%d", up, down)
	}

	if resp := tp.do(http.MethodDelete, "/api/v1/inbounds/"+itoa(in.ID), nil, nil); resp.Code != http.StatusOK {
		t.Fatalf("delete inbound: %d %s", resp.Code, resp.Body.String())
	}
	if up, down := tp.svc.Manager().TotalTraffic(); up != 0 || down != 0 {
		t.Fatalf("deleted client still counted: up=%d down=%d", up, down)
	}
	if _, err := tp.store.ClientByID(client.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("client row should be gone, got %v", err)
	}
}

// TestAPIResponsesAreNotCacheable verifies sensitive API payloads carry
// no-store, while the immutable assets and the SPA shell keep their policies.
func TestAPIResponsesAreNotCacheable(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	rec := tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbounds: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("api cache-control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("api pragma = %q, want no-cache", got)
	}

	// Unauthenticated API responses are uncacheable too.
	anon := newTestPanel(t)
	rec = anon.do(http.MethodGet, "/api/v1/inbounds", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous inbounds: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("anonymous api cache-control = %q, want no-store", got)
	}
}

// listClientsBody returns the decoded items of GET /api/v1/clients.
func (tp *testPanel) listClientsBody(header http.Header) []map[string]any {
	tp.t.Helper()
	rec := tp.doRaw(http.MethodGet, "/api/v1/clients", header)
	if rec.Code != http.StatusOK {
		tp.t.Fatalf("list clients: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	body := rec.Body.Bytes()
	if rec.Header().Get("Content-Encoding") == "gzip" {
		body = gunzip(tp.t, body)
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		tp.t.Fatalf("decode clients: %v", err)
	}
	if payload.Total != len(payload.Items) {
		tp.t.Fatalf("total %d != items %d", payload.Total, len(payload.Items))
	}
	return payload.Items
}

// TestClientListOmitsSecrets verifies the list representation carries no
// credentials, while the single client endpoints still expose them.
func TestClientListOmitsSecrets(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	port := freeTestPort(t)

	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "list-secrets", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "udp_enabled": true, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "no-secrets", "enabled": true, "quota_bytes": 1024,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)

	items := tp.listClientsBody(http.Header{})
	if len(items) != 1 {
		t.Fatalf("expected 1 client, got %d", len(items))
	}
	item := items[0]
	for _, forbidden := range []string{
		"psk", "sub_token", "link", "subscription_url",
		// Timestamps the list view never renders.
		"created_at", "updated_at", "last_reset_at",
	} {
		if _, present := item[forbidden]; present {
			t.Fatalf("list item must not contain %q: %v", forbidden, item)
		}
	}
	for _, required := range []string{
		"id", "inbound_id", "name", "enabled", "quota_bytes", "up_bytes", "down_bytes",
		"expires_at", "ip_limit", "reset_policy", "reset_day", "note",
		"status", "used_bytes", "remaining_bytes", "online_ips", "inbound_remark", "method",
	} {
		if _, present := item[required]; !present {
			t.Fatalf("list item is missing %q: %v", required, item)
		}
	}
	if item["status"] != model.StatusActive {
		t.Fatalf("unexpected status %v", item["status"])
	}

	// The detail endpoint still carries the credentials and the share link.
	rec = tp.do(http.MethodGet, "/api/v1/clients/"+itoa(client.ID), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get client: %d", rec.Code)
	}
	detail := decode[model.Client](t, rec)
	if detail.PSK == "" {
		t.Fatal("detail must include the client PSK")
	}
	var full map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if full["link"] == "" || full["link"] == nil {
		t.Fatal("detail must include the share link")
	}
	if full["subscription_url"] == "" || full["subscription_url"] == nil {
		t.Fatal("detail must include the subscription URL")
	}
}

// seedClients creates n clients in one inbound and returns the inbound id.
func (tp *testPanel) seedClients(n int) int64 {
	tp.t.Helper()
	port := freeTestPort(tp.t)
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "seeded", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "udp_enabled": false, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		tp.t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](tp.t, rec)
	for i := 0; i < n; i++ {
		rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
			"name": "seeded-" + itoa(int64(i)), "enabled": true,
		}, nil)
		if rec.Code != http.StatusCreated {
			tp.t.Fatalf("create client %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	return in.ID
}

// TestClientListGzip verifies the credential-free list is compressed when the
// client accepts it, and that both forms decode to the same JSON.
func TestClientListGzip(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	tp.seedClients(40)

	plain := tp.doRaw(http.MethodGet, "/api/v1/clients", http.Header{})
	if plain.Code != http.StatusOK {
		t.Fatalf("plain list: %d", plain.Code)
	}
	if got := plain.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("plain list was compressed")
	}
	if got := plain.Header().Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("plain list vary = %q", got)
	}

	compressed := tp.doRaw(http.MethodGet, "/api/v1/clients",
		http.Header{"Accept-Encoding": []string{"gzip, deflate"}})
	if compressed.Code != http.StatusOK {
		t.Fatalf("compressed list: %d", compressed.Code)
	}
	if got := compressed.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("list content-encoding = %q, want gzip", got)
	}
	if compressed.Body.Len() >= plain.Body.Len() {
		t.Fatalf("compressed body %d is not smaller than %d", compressed.Body.Len(), plain.Body.Len())
	}
	if got := compressed.Header().Get("Content-Length"); got != strconv.Itoa(compressed.Body.Len()) {
		t.Fatalf("content-length = %q, body = %d", got, compressed.Body.Len())
	}
	var plainPayload, gzPayload any
	if err := json.Unmarshal(plain.Body.Bytes(), &plainPayload); err != nil {
		t.Fatalf("decode plain: %v", err)
	}
	if err := json.Unmarshal(gunzip(t, compressed.Body.Bytes()), &gzPayload); err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	if !reflect.DeepEqual(plainPayload, gzPayload) {
		t.Fatal("compressed and plain payloads differ")
	}
}

// TestClientListSkipsGzipForSmallResponses verifies tiny lists are sent plain.
func TestClientListSkipsGzipForSmallResponses(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	tp.seedClients(1)

	rec := tp.doRaw(http.MethodGet, "/api/v1/clients", http.Header{"Accept-Encoding": []string{"gzip"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("small list was compressed")
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(strings.ToLower(got), "accept-encoding") {
		t.Fatalf("vary = %q", got)
	}
}

// TestClientListPaging covers the server-side page: the default page bound, an
// explicit limit/offset, the echoed paging values, and the filters that run in
// SQL instead of the browser.
func TestClientListPaging(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(101)

	first := decode[service.ClientListPage](t, tp.doRaw(http.MethodGet, "/api/v1/clients", http.Header{}))
	if first.Total != 101 || len(first.Items) != store.ClientPageDefaultLimit {
		t.Fatalf("default page: %d items, total %d", len(first.Items), first.Total)
	}
	if first.Limit != store.ClientPageDefaultLimit || first.Offset != 0 {
		t.Fatalf("default page echo: limit=%d offset=%d", first.Limit, first.Offset)
	}
	// The paged projection is still the credential-free summary.
	raw := tp.doRaw(http.MethodGet, "/api/v1/clients?limit=100", http.Header{})
	var rawItems struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw.Body.Bytes(), &rawItems); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, forbidden := range []string{"psk", "sub_token", "link", "subscription_url"} {
		if _, ok := rawItems.Items[0][forbidden]; ok {
			t.Fatalf("paged list leaked %s", forbidden)
		}
	}

	second := decode[service.ClientListPage](t,
		tp.doRaw(http.MethodGet, "/api/v1/clients?limit=2&offset=1", http.Header{}))
	if second.Total != 101 || len(second.Items) != 2 || second.Limit != 2 || second.Offset != 1 {
		t.Fatalf("limited page: %d items, total %d, limit %d, offset %d",
			len(second.Items), second.Total, second.Limit, second.Offset)
	}
	if second.Items[0].Name != "seeded-1" || second.Items[1].Name != "seeded-2" {
		t.Fatalf("unexpected page contents: %q, %q", second.Items[0].Name, second.Items[1].Name)
	}
	if second.Items[0].ID >= second.Items[1].ID {
		t.Fatalf("page is not ordered by id")
	}

	// An offset past the end is an empty page with the real total, not a 404.
	past := decode[service.ClientListPage](t,
		tp.doRaw(http.MethodGet, "/api/v1/clients?limit=10&offset=500", http.Header{}))
	if len(past.Items) != 0 || past.Total != 101 {
		t.Fatalf("past end: %d items, total %d", len(past.Items), past.Total)
	}

	// Paging and filter values are validated rather than clamped: a caller that
	// asked for more than a page must not believe it received everything.
	for _, path := range []string{
		"/api/v1/clients?limit=0",
		"/api/v1/clients?limit=501",
		"/api/v1/clients?limit=abc",
		"/api/v1/clients?offset=-1",
		"/api/v1/clients?offset=abc",
		"/api/v1/clients?inbound_id=abc",
		"/api/v1/clients?status=bogus",
		"/api/v1/clients?q=" + strings.Repeat("x", store.ClientQueryMaxLen+1),
	} {
		if rec := tp.doRaw(http.MethodGet, path, http.Header{}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// Status filtering agrees with the status the same page reports.
	disabledID := second.Items[0].ID
	if rec := tp.do(http.MethodPost, "/api/v1/clients/"+itoa(disabledID)+"/toggle",
		map[string]any{"enabled": false}, nil); rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
	}
	disabled := decode[service.ClientListPage](t,
		tp.doRaw(http.MethodGet, "/api/v1/clients?status=disabled&limit=500", http.Header{}))
	if disabled.Total != 1 || len(disabled.Items) != 1 || disabled.Items[0].ID != disabledID {
		t.Fatalf("status=disabled: %d items, total %d", len(disabled.Items), disabled.Total)
	}
	if disabled.Items[0].Status != model.StatusDisabled {
		t.Fatalf("disabled row reported status %q", disabled.Items[0].Status)
	}
	active := decode[service.ClientListPage](t,
		tp.doRaw(http.MethodGet, "/api/v1/clients?status=active&limit=500", http.Header{}))
	if active.Total != 100 || len(active.Items) != 100 {
		t.Fatalf("status=active: %d items, total %d", len(active.Items), active.Total)
	}
	for _, item := range active.Items {
		if item.Status != model.StatusActive {
			t.Fatalf("status=active returned a %q row", item.Status)
		}
	}

	// The free-text filter matches name, inbound remark and method.
	for path, want := range map[string]int64{
		"/api/v1/clients?q=seeded-100":        1,
		"/api/v1/clients?q=seeded":            101, // inbound remark and every name
		"/api/v1/clients?q=blake3":            101, // method
		"/api/v1/clients?q=no-such-user-here": 0,
	} {
		got := decode[service.ClientListPage](t, tp.doRaw(http.MethodGet, path, http.Header{}))
		if got.Total != want {
			t.Fatalf("%s: total %d, want %d", path, got.Total, want)
		}
	}

	// The per-inbound endpoint pages the same way and ignores a conflicting
	// inbound_id in the query string.
	perInbound := decode[service.ClientListPage](t, tp.doRaw(http.MethodGet,
		"/api/v1/inbounds/"+itoa(inboundID)+"/clients?limit=3&inbound_id=999999", http.Header{}))
	if perInbound.Total != 101 || len(perInbound.Items) != 3 {
		t.Fatalf("per-inbound page: %d items, total %d", len(perInbound.Items), perInbound.Total)
	}
	for _, item := range perInbound.Items {
		if item.InboundID != inboundID {
			t.Fatalf("per-inbound page leaked a row of inbound %d", item.InboundID)
		}
	}
}

// TestInboundClientListUsesSummary verifies the per-inbound list is trimmed too.
func TestInboundClientListUsesSummary(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(1)

	rec := tp.doRaw(http.MethodGet, "/api/v1/inbounds/"+itoa(inboundID)+"/clients", http.Header{})
	if rec.Code != http.StatusOK {
		t.Fatalf("inbound clients: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(payload.Items))
	}
	for _, forbidden := range []string{"psk", "sub_token", "link", "subscription_url"} {
		if _, present := payload.Items[0][forbidden]; present {
			t.Fatalf("inbound client list must not contain %q", forbidden)
		}
	}
}

// BenchmarkClientListEncode measures JSON encoding of a 2000 row summary list.
func BenchmarkClientListEncode(b *testing.B) {
	items := benchmarkClientSummaries(2000)
	payload := gin.H{"items": items, "total": len(items)}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	writer := newJSONWriter()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.Body.Reset()
		writer.writeJSON(c, http.StatusOK, payload)
	}
}

// BenchmarkClientListEncodeGzip measures the compressed variant.
func BenchmarkClientListEncodeGzip(b *testing.B) {
	items := benchmarkClientSummaries(2000)
	payload := gin.H{"items": items, "total": len(items)}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	c.Request.Header.Set("Accept-Encoding", "gzip")
	writer := newJSONWriter()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder.Body.Reset()
		writer.writeJSON(c, http.StatusOK, payload)
	}
}

func benchmarkClientSummaries(n int) []service.ClientSummary {
	items := make([]service.ClientSummary, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, service.ClientSummary{
			ID: int64(i + 1), InboundID: 1, Name: "benchmark-user-" + itoa(int64(i)),
			Enabled: true, QuotaBytes: 100 << 30, UpBytes: int64(i) * 1024,
			DownBytes: int64(i) * 2048, Status: model.StatusActive,
			UsedBytes: int64(i) * 3072, RemainingBytes: 100 << 30,
			OnlineIPs: []string{}, InboundRemark: "benchmark", Method: model.Method2022AES128GCM,
		})
	}
	return items
}

// TestInboundsAggregatesMatchClientList verifies the per-inbound counters the
// panel renders equal the sum over the client list, so the SQL aggregation that
// replaced the in-Go loop cannot drift from the client data.
func TestInboundsAggregatesMatchClientList(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	// Two inbounds with different client mixes (one disabled, one with traffic).
	first := tp.seedClients(3)
	second := tp.seedClients(2)

	reloaded, err := tp.store.ListInbounds()
	if err != nil {
		t.Fatalf("inbounds: %v", err)
	}
	for _, in := range reloaded {
		for _, c := range mustClientsOf(t, tp, in.ID) {
			if c.Name == "seeded-0" && in.ID == second {
				if err := tp.store.SetClientEnabled(c.ID, false); err != nil {
					t.Fatalf("disable: %v", err)
				}
			}
			if err := tp.store.SaveUsage([]model.UsageSnapshot{{
				ClientID: c.ID, UpBytes: 100, DownBytes: 200, Day: "2026-07-01",
				DayUpBytes: 100, DayDownBytes: 200,
			}}); err != nil {
				t.Fatalf("usage: %v", err)
			}
		}
	}

	rec := tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbounds: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID                 int64 `json:"id"`
			ClientCount        int   `json:"client_count"`
			EnabledClientCount int   `json:"enabled_client_count"`
			UpBytes            int64 `json:"up_bytes"`
			DownBytes          int64 `json:"down_bytes"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Derive the expected values from the client list endpoint.
	items := tp.listClientsBody(http.Header{})
	type want struct {
		total, enabled int
		up, down       int64
	}
	expected := make(map[int64]want)
	for _, item := range items {
		id := int64(item["inbound_id"].(float64))
		w := expected[id]
		w.total++
		if item["enabled"].(bool) {
			w.enabled++
		}
		w.up += int64(item["up_bytes"].(float64))
		w.down += int64(item["down_bytes"].(float64))
		expected[id] = w
	}

	seen := 0
	for _, in := range payload.Items {
		w, ok := expected[in.ID]
		if !ok {
			w = want{}
		}
		if in.ClientCount != w.total || in.EnabledClientCount != w.enabled ||
			in.UpBytes != w.up || in.DownBytes != w.down {
			t.Fatalf("inbound %d aggregate mismatch: got {count:%d enabled:%d up:%d down:%d}, want %+v",
				in.ID, in.ClientCount, in.EnabledClientCount, in.UpBytes, in.DownBytes, w)
		}
		seen++
	}
	if seen < 2 || first == 0 || second == 0 {
		t.Fatalf("expected both seeded inbounds in the response, saw %d", seen)
	}
}

// mustClientsOf returns the clients of one inbound.
func mustClientsOf(t *testing.T, tp *testPanel, inboundID int64) []model.Client {
	t.Helper()
	clients, err := tp.store.ListClients(inboundID)
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	return clients
}

// TestDashboardTrafficMatchesDetail verifies the dashboard's 30 day series comes
// from the maintained rollup and still equals a recomputation from the detail
// rows, with days outside the window excluded.
func TestDashboardTrafficMatchesDetail(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(2)
	clients := mustClientsOf(t, tp, inboundID)

	// Two days inside the window and one clearly outside it.
	inside := time.Now().In(tp.svc.Config().Location()).AddDate(0, 0, -1).Format("2006-01-02")
	today := time.Now().In(tp.svc.Config().Location()).Format("2006-01-02")
	outside := time.Now().In(tp.svc.Config().Location()).AddDate(0, 0, -90).Format("2006-01-02")
	snaps := []model.UsageSnapshot{
		{ClientID: clients[0].ID, UpBytes: 100, DownBytes: 200, Day: inside, DayUpBytes: 100, DayDownBytes: 200},
		{ClientID: clients[1].ID, UpBytes: 10, DownBytes: 20, Day: inside, DayUpBytes: 10, DayDownBytes: 20},
		{ClientID: clients[0].ID, UpBytes: 150, DownBytes: 260, Day: today, DayUpBytes: 50, DayDownBytes: 60},
		{ClientID: clients[1].ID, UpBytes: 10, DownBytes: 20, Day: outside, DayUpBytes: 7, DayDownBytes: 7},
	}
	if err := tp.store.SaveUsage(snaps); err != nil {
		t.Fatalf("save usage: %v", err)
	}

	rec := tp.do(http.MethodGet, "/api/v1/dashboard", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Traffic30d []model.DailyUsage `json:"traffic_30d"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Expected values are recomputed from the detail rows through the store, so
	// this fails if the rollup drifts from what the detail table says.
	since := time.Now().AddDate(0, 0, -30).In(tp.svc.Config().Location()).Format("2006-01-02")
	want, err := tp.store.DailyTotals(since)
	if err != nil {
		t.Fatalf("daily totals: %v", err)
	}
	if len(payload.Traffic30d) != len(want) {
		t.Fatalf("dashboard has %d days, want %d: %+v vs %+v",
			len(payload.Traffic30d), len(want), payload.Traffic30d, want)
	}
	for i := range want {
		if payload.Traffic30d[i] != want[i] {
			t.Fatalf("day %d: dashboard=%+v want=%+v", i, payload.Traffic30d[i], want[i])
		}
	}
	// The out-of-window day must not appear.
	for _, day := range payload.Traffic30d {
		if day.Day == outside {
			t.Fatalf("out-of-window day %s leaked into the series", outside)
		}
	}
	// Sanity: both in-window days are present with their expected totals.
	found := map[string]model.DailyUsage{}
	for _, day := range payload.Traffic30d {
		found[day.Day] = day
	}
	if got := found[inside]; got.UpBytes != 110 || got.DownBytes != 220 {
		t.Fatalf("inside day totals = %+v", got)
	}
	if got := found[today]; got.UpBytes != 50 || got.DownBytes != 60 {
		t.Fatalf("today totals = %+v", got)
	}
}

// TestAuditLogsMutations verifies every mutation writes an audit row, and that no
// credential value ever reaches the trail.
func TestAuditLogsMutations(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	port := freeTestPort(t)

	created := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "audited", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", created.Code, created.Body.String())
	}
	in := decode[model.Inbound](t, created)

	clientRec := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "audit-user", "enabled": true, "quota_bytes": 2048,
	}, nil)
	if clientRec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", clientRec.Code, clientRec.Body.String())
	}
	client := decode[model.Client](t, clientRec)

	if rec := tp.do(http.MethodPost, "/api/v1/clients/"+itoa(client.ID)+"/toggle",
		map[string]any{"enabled": false}, nil); rec.Code != http.StatusOK {
		t.Fatalf("toggle client: %d", rec.Code)
	}
	if rec := tp.do(http.MethodPut, "/api/v1/settings", map[string]any{
		"public_host": "", "public_port": 0, "subscription_enabled": true,
		"allow_private_targets": false, "reset_day": 1, "timezone": "UTC",
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("update settings: %d %s", rec.Code, rec.Body.String())
	}

	rec := tp.do(http.MethodGet, "/api/v1/audit", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items   []model.AuditLog `json:"items"`
		Total   int64            `json:"total"`
		Actions []string         `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if payload.Total != int64(len(payload.Items)) || payload.Total == 0 {
		t.Fatalf("unexpected audit totals: total=%d len=%d", payload.Total, len(payload.Items))
	}

	wantActions := map[string]bool{
		model.AuditAuthLogin:      false,
		model.AuditInboundCreate:  false,
		model.AuditClientCreate:   false,
		model.AuditClientToggle:   false,
		model.AuditSettingsUpdate: false,
	}
	for _, entry := range payload.Items {
		if _, ok := wantActions[entry.Action]; ok {
			wantActions[entry.Action] = true
		}
		if entry.Username != "admin" {
			t.Fatalf("audit row has no actor: %+v", entry)
		}
		if entry.IP == "" {
			t.Fatalf("audit row has no IP: %+v", entry)
		}
	}
	for action, seen := range wantActions {
		if !seen {
			t.Fatalf("missing audit action %q (got %d rows)", action, len(payload.Items))
		}
	}

	// The trail must never contain a credential value.
	body := rec.Body.String()
	for _, secret := range []string{client.PSK, client.SubToken, in.ServerPSK, "audit-pass-123"} {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatalf("audit trail leaked a credential")
		}
	}
	if len(payload.Actions) == 0 {
		t.Fatal("audit response must list the known actions")
	}
}

// TestAuditLoginFailure verifies a failed login is recorded without the password.
func TestAuditLoginFailure(t *testing.T) {
	tp := newTestPanel(t)
	// A fresh panel has no session; attempt a wrong password.
	rec := tp.do(http.MethodPost, "/api/v1/auth/login",
		map[string]any{"username": "admin", "password": "wrong-password-value"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login: %d", rec.Code)
	}
	tp.login()

	rec = tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditAuthLoginFailed, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, model.AuditAuthLoginFailed) {
		t.Fatalf("failed login was not audited: %s", body)
	}
	if !strings.Contains(body, "admin") {
		t.Fatalf("failed login should record the attempted username: %s", body)
	}
	if strings.Contains(body, "wrong-password-value") {
		t.Fatal("audit trail leaked the attempted password")
	}
}

// TestAuditFilterByAction exercises the server side filter and paging echo.
func TestAuditFilterByAction(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	for i := 0; i < 3; i++ {
		port := freeTestPort(t)
		if rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
			"remark": "filtered", "listen": "127.0.0.1", "port": port,
			"method": model.Method2022AES128GCM, "enabled": true,
		}, nil); rec.Code != http.StatusCreated {
			t.Fatalf("create inbound: %d", rec.Code)
		}
	}

	rec := tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditInboundCreate+"&limit=2", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items  []model.AuditLog `json:"items"`
		Total  int64            `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Total != 3 {
		t.Fatalf("total = %d, want 3", payload.Total)
	}
	if len(payload.Items) != 2 || payload.Limit != 2 || payload.Offset != 0 {
		t.Fatalf("unexpected page: len=%d limit=%d offset=%d", len(payload.Items), payload.Limit, payload.Offset)
	}
	for _, entry := range payload.Items {
		if entry.Action != model.AuditInboundCreate {
			t.Fatalf("action filter leaked %q", entry.Action)
		}
	}
}

// TestExportClientsCSVEndpoint verifies the CSV download headers and contents.
func TestExportClientsCSVEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(2)

	rec := tp.doRaw(http.MethodGet, "/api/v1/export/clients?inbound_id="+itoa(inboundID), http.Header{})
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") ||
		!strings.Contains(cd, ".csv") {
		t.Fatalf("content-disposition = %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("export has %d lines, want header + 2 users: %q", len(lines), rec.Body.String())
	}
	if !strings.HasPrefix(lines[0], "inbound_id,inbound_remark,name,psk") {
		t.Fatalf("unexpected header: %s", lines[0])
	}

	// The export is audited without recording any PSK.
	auditRec := tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditClientExport, nil, nil)
	if auditRec.Code != http.StatusOK || !strings.Contains(auditRec.Body.String(), model.AuditClientExport) {
		t.Fatalf("export was not audited: %d %s", auditRec.Code, auditRec.Body.String())
	}
}

// TestImportClientsEndpoint verifies the import endpoint reports per-line results.
func TestImportClientsEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(1)

	rec := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(inboundID)+"/clients/import", map[string]any{
		"format":  "csv",
		"content": "name,psk\nalice,\nbob,\nseeded-0,\n",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Created int `json:"created"`
		Failed  []struct {
			Line  int    `json:"line"`
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Created != 2 || len(result.Failed) != 1 {
		t.Fatalf("created=%d failed=%d (%+v)", result.Created, len(result.Failed), result.Failed)
	}
	if result.Failed[0].Name != "seeded-0" {
		t.Fatalf("unexpected failure: %+v", result.Failed[0])
	}

	// The imported users are visible in the list.
	items := tp.listClientsBody(http.Header{})
	if len(items) != 3 {
		t.Fatalf("client list has %d items, want 3", len(items))
	}
	names := make(map[string]bool, len(items))
	for _, item := range items {
		names[item["name"].(string)] = true
	}
	for _, want := range []string{"alice", "bob", "seeded-0"} {
		if !names[want] {
			t.Fatalf("imported user %q is missing: %v", want, names)
		}
	}

	// Malformed JSON is a bad request.
	if rec := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(inboundID)+"/clients/import",
		"not-an-object", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d %s", rec.Code, rec.Body.String())
	}
}

// TestClientUsageEndpoint verifies the per-client daily usage series.
func TestClientUsageEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	inboundID := tp.seedClients(1)
	clients := mustClientsOf(t, tp, inboundID)
	day := time.Now().In(tp.svc.Config().Location()).Format("2006-01-02")
	if err := tp.store.SaveUsage([]model.UsageSnapshot{{
		ClientID: clients[0].ID, UpBytes: 11, DownBytes: 22,
		Day: day, DayUpBytes: 11, DayDownBytes: 22,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}

	rec := tp.do(http.MethodGet, "/api/v1/clients/"+itoa(clients[0].ID)+"/usage?days=7", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []model.DailyUsage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].UpBytes != 11 || payload.Items[0].DownBytes != 22 {
		t.Fatalf("unexpected usage payload: %+v", payload.Items)
	}
}

// TestInboundDomainDrivesLinks verifies the per-inbound domain ends up in share
// links and in both subscription formats.
func TestInboundDomainDrivesLinks(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	port := freeTestPort(t)

	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "hk", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "enabled": true,
		"domain": "HK.Example.com.", "domain_port": 8443,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	if in.Domain != "hk.example.com" || in.DomainPort != 8443 {
		t.Fatalf("domain not normalized: %+v", in)
	}

	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients",
		map[string]any{"name": "alice", "enabled": true}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)

	// Detail view and the link endpoint both use the inbound domain.
	rec = tp.do(http.MethodGet, "/api/v1/clients/"+itoa(client.ID)+"/link", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: %d %s", rec.Code, rec.Body.String())
	}
	link := decode[struct {
		Link string `json:"link"`
		Sub  string `json:"subscription_url"`
	}](t, rec)
	if !strings.Contains(link.Link, "hk.example.com:8443") {
		t.Fatalf("share link does not use the inbound domain: %s", link.Link)
	}

	rec = tp.do(http.MethodGet, "/api/v1/clients/"+itoa(client.ID), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d", rec.Code)
	}
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if got, _ := detail["link"].(string); !strings.Contains(got, "hk.example.com:8443") {
		t.Fatalf("detail link does not use the inbound domain: %s", got)
	}

	// The subscription endpoints (base64 and Clash) resolve the same way.
	subRec := tp.doRaw(http.MethodGet, link.Sub, http.Header{})
	if subRec.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", subRec.Code, subRec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(subRec.Body.String()))
	if err != nil {
		t.Fatalf("decode subscription: %v", err)
	}
	if !strings.Contains(string(decoded), "hk.example.com:8443") {
		t.Fatalf("subscription does not use the inbound domain: %s", decoded)
	}

	clashRes := tp.doRaw(http.MethodGet, link.Sub+"?format=clash", http.Header{})
	if clashRes.Code != http.StatusOK {
		t.Fatalf("clash subscription: %d", clashRes.Code)
	}
	if !strings.Contains(clashRes.Body.String(), "hk.example.com") {
		t.Fatalf("clash subscription does not use the inbound domain: %s", clashRes.Body.String())
	}
	if !strings.Contains(clashRes.Body.String(), "8443") {
		t.Fatalf("clash subscription does not use the domain port: %s", clashRes.Body.String())
	}
}

// TestInboundDomainValidation verifies the API rejects bad domain input.
func TestInboundDomainValidation(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"scheme in domain", map[string]any{"domain": "http://example.com"}},
		{"port without domain", map[string]any{"domain_port": 8443}},
		{"bracketed ipv6", map[string]any{"domain": "[::1]"}},
		{"empty label", map[string]any{"domain": "a..b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"remark": "bad", "listen": "127.0.0.1", "port": freeTestPort(t),
				"method": model.Method2022AES128GCM, "enabled": true,
			}
			for key, value := range tc.body {
				body[key] = value
			}
			rec := tp.do(http.MethodPost, "/api/v1/inbounds", body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestInboundWithoutDomainKeepsLegacyLinks guards backward compatibility: an
// inbound with no domain must produce exactly the link the global setting gives.
func TestInboundWithoutDomainKeepsLegacyLinks(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	if rec := tp.do(http.MethodPut, "/api/v1/settings", map[string]any{
		"public_host": "global.example.com", "public_port": 9000,
		"subscription_enabled": true, "allow_private_targets": false,
		"reset_day": 1, "timezone": "UTC",
	}, nil); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}

	inboundID := tp.seedClients(1)
	clients := mustClientsOf(t, tp, inboundID)
	rec := tp.do(http.MethodGet, "/api/v1/clients/"+itoa(clients[0].ID)+"/link", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: %d", rec.Code)
	}
	link := decode[struct {
		Link string `json:"link"`
	}](t, rec)
	if !strings.Contains(link.Link, "global.example.com:9000") {
		t.Fatalf("link should fall back to the global settings: %s", link.Link)
	}
}
