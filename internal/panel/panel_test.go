package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
