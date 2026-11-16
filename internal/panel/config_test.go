package panel

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// configTestPSK returns a valid key for the given size.
func configTestPSK(size int) string {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// TestExportConfigEndpoint covers the attachment headers and what the snapshot
// does and does not contain.
func TestExportConfigEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, _ := seedAccessClient(t, tp)
	_ = in

	rec := tp.do(http.MethodGet, "/api/v1/export/config", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="sspanel-config-`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	var snapshot model.ConfigSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if snapshot.Version != model.ConfigSnapshotVersion {
		t.Fatalf("version = %d", snapshot.Version)
	}
	if len(snapshot.Inbounds) == 0 {
		t.Fatal("export has no inbounds")
	}
	// The session secret and history are not part of a configuration snapshot.
	body := rec.Body.String()
	for _, forbidden := range []string{"session_secret", "access_events", "traffic_daily", "audit_logs"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("snapshot contains %q", forbidden)
		}
	}
	// The administrator's hash is carried by design, so a migrated instance
	// keeps the same login.
	if !strings.HasPrefix(snapshot.Admin.PasswordHash, "$2") {
		t.Fatalf("admin password hash missing: %+v", snapshot.Admin)
	}

	audit := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditExportConfig, nil, nil))
	if len(audit.Items) != 1 {
		t.Fatalf("export was audited %d times", len(audit.Items))
	}
}

// TestImportConfigEndpoint covers a successful round trip through HTTP and the
// two request-level guards (missing confirm, bad version).
func TestImportConfigEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedAccessClient(t, tp)
	_ = in
	_ = client

	exported := tp.do(http.MethodGet, "/api/v1/export/config", nil, nil)
	var snapshot model.ConfigSnapshot
	if err := json.Unmarshal(exported.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode export: %v", err)
	}

	// Without confirm the current configuration stays put.
	rec := tp.do(http.MethodPost, "/api/v1/import/config", map[string]any{"config": snapshot}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-confirm: expected 400, got %d %s", rec.Code, rec.Body.String())
	}

	// A bad version is rejected with a validation field.
	bad := snapshot
	bad.Version = 999
	rec = tp.do(http.MethodPost, "/api/v1/import/config",
		map[string]any{"confirm": true, "config": bad}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad version: expected 400, got %d %s", rec.Code, rec.Body.String())
	}

	// A valid import succeeds and reports what it installed.
	rec = tp.do(http.MethodPost, "/api/v1/import/config",
		map[string]any{"confirm": true, "config": snapshot}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	result := decode[model.ImportConfigResult](t, rec)
	if result.Inbounds < 1 || result.Clients < 1 {
		t.Fatalf("unexpected import result %+v", result)
	}

	audit := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditImportConfig, nil, nil))
	if len(audit.Items) != 1 {
		t.Fatalf("import was audited %d times", len(audit.Items))
	}
}

// TestImportConfigReportsFields pins the field map in the error envelope, so a
// bad upload can point at the exact offending entry.
func TestImportConfigReportsFields(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	_, _ = seedAccessClient(t, tp)
	exported := tp.do(http.MethodGet, "/api/v1/export/config", nil, nil)
	var snapshot model.ConfigSnapshot
	if err := json.Unmarshal(exported.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode: %v", err)
	}
	snapshot.Settings.Timezone = "Mars/Olympus"

	rec := tp.do(http.MethodPost, "/api/v1/import/config",
		map[string]any{"confirm": true, "config": snapshot}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body.String())
	}
	response := decode[struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}](t, rec)
	if response.Error.Fields["settings.timezone"] == "" {
		t.Fatalf("timezone field not reported: %+v", response.Error)
	}
}

// TestExportConfigRequiresAuth covers the auth boundary on both endpoints.
func TestExportConfigRequiresAuth(t *testing.T) {
	tp := newTestPanel(t)
	if rec := tp.do(http.MethodGet, "/api/v1/export/config", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("export without auth: %d", rec.Code)
	}
	if rec := tp.do(http.MethodPost, "/api/v1/import/config", map[string]any{}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("import without auth: %d", rec.Code)
	}
}
