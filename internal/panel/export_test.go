package panel

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// seedAuditEntries writes n audit rows directly through the store so the export
// has something to render without going through every audited endpoint.
func seedAuditEntries(t *testing.T, tp *testPanel, n int) {
	t.Helper()
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		if err := tp.store.InsertAudit(model.AuditLog{
			CreatedAt:  now - int64(i),
			Username:   "admin",
			Action:     model.AuditClientCreate,
			TargetType: "client",
			TargetID:   int64(i + 1),
			Detail:     "name=user-" + itoa(int64(i)),
			IP:         "203.0.113.5",
		}); err != nil {
			t.Fatalf("insert audit: %v", err)
		}
	}
}

// TestExportAuditCSV covers the shape of the file: the documented columns, the
// rows from the filter, and the attachment headers.
func TestExportAuditCSV(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	seedAuditEntries(t, tp, 5)

	rec := tp.do(http.MethodGet, "/api/v1/export/audit", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="sspanel-audit-`) {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Export-Truncated"); got != "" {
		t.Fatalf("a small export claimed truncation: %q", got)
	}

	rows := readCSV(t, rec.Body.Bytes())
	want := []string{"created_at", "created_at_local", "username", "action",
		"target_type", "target_id", "detail", "ip"}
	if len(rows) < 2 || strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v, want %v", rows[0], want)
	}
	// Five seeded rows plus the login that the test performed.
	if len(rows)-1 < 5 {
		t.Fatalf("exported %d rows, want at least the 5 seeded ones", len(rows)-1)
	}
	// The detail column is exported verbatim: it never contains a credential,
	// so it needs no redaction.
	if !strings.Contains(rec.Body.String(), "name=user-0") {
		t.Fatalf("detail was not exported:\n%s", rec.Body.String())
	}
	// The local stamp is rendered in the effective zone, and has the shape an
	// operator can read without a converter.
	if rows[1][1] == "" || len(rows[1][1]) != 19 {
		t.Fatalf("created_at_local = %q", rows[1][1])
	}
}

// TestExportAuditFilters checks that the export honours the same filters as the
// list, so a file never contains rows the page would not show.
func TestExportAuditFilters(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	seedAuditEntries(t, tp, 3)
	if err := tp.store.InsertAudit(model.AuditLog{
		CreatedAt: time.Now().Unix(), Username: "admin",
		Action: model.AuditSettingsUpdate, TargetType: "settings", Detail: "timezone=UTC",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := tp.do(http.MethodGet, "/api/v1/export/audit?action="+model.AuditSettingsUpdate, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	rows := readCSV(t, rec.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("filtered export has %d data rows, want 1:\n%s", len(rows)-1, rec.Body.String())
	}
	if rows[1][3] != model.AuditSettingsUpdate {
		t.Fatalf("row = %v, want the settings update", rows[1])
	}
}

// TestExportRejectsPaging keeps an export from silently exporting "page 3".
func TestExportRejectsPaging(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	for _, path := range []string{
		"/api/v1/export/audit?limit=10",
		"/api/v1/export/audit?offset=5",
		"/api/v1/export/audit?since=200&until=100",
		"/api/v1/export/audit?since=abc",
		"/api/v1/export/access?limit=10",
		"/api/v1/export/access?offset=5",
		"/api/v1/export/access?outcome=weird",
		"/api/v1/export/access?network=icmp",
		"/api/v1/export/access?since_day=yesterday",
		"/api/v1/export/access?since_day=2026-11-15&until_day=2026-11-14",
	} {
		if rec := tp.do(http.MethodGet, path, nil, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// TestExportAccessCSV covers the access history export: columns, filters, and
// the day window.
func TestExportAccessCSV(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedAccessClient(t, tp)
	loc := tp.svc.Location()
	today := time.Now().In(loc)
	yesterday := today.AddDate(0, 0, -1)
	if err := tp.store.SaveAccess([]model.AccessRecord{
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "today.example", Port: 443,
			IP: "93.184.216.34", Outcome: model.AccessAllowed, UpBytes: 10, DownBytes: 20,
			StartedAt: today.Unix(), DurationMS: 40, Day: today.Format("2006-01-02")},
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "old.example", Port: 80,
			Outcome: model.AccessBlocked, Reason: model.AccessReasonPolicy,
			StartedAt: yesterday.Unix(), Day: yesterday.Format("2006-01-02")},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rec := tp.do(http.MethodGet, "/api/v1/export/access", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	rows := readCSV(t, rec.Body.Bytes())
	want := []string{"started_at", "started_at_local", "client_id", "client_name",
		"inbound_id", "inbound_remark", "network", "host", "port", "ip", "outcome",
		"reason", "up_bytes", "down_bytes", "duration_ms"}
	if strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v, want %v", rows[0], want)
	}
	if len(rows) != 3 {
		t.Fatalf("exported %d data rows, want 2:\n%s", len(rows)-1, rec.Body.String())
	}
	// The joined names are part of the file, like the list endpoint.
	if rows[1][3] != "alice" || rows[1][5] != "main" {
		t.Fatalf("names missing from %v", rows[1])
	}

	// The day form narrows to one day, the same way the list does.
	onlyToday := tp.do(http.MethodGet,
		"/api/v1/export/access?since_day="+today.Format("2006-01-02"), nil, nil)
	if onlyToday.Code != http.StatusOK {
		t.Fatalf("day export: %d %s", onlyToday.Code, onlyToday.Body.String())
	}
	dayRows := readCSV(t, onlyToday.Body.Bytes())
	if len(dayRows) != 2 || dayRows[1][7] != "today.example" {
		t.Fatalf("day window exported %v", dayRows)
	}

	// The outcome filter applies too.
	blocked := tp.do(http.MethodGet, "/api/v1/export/access?outcome=blocked", nil, nil)
	blockedRows := readCSV(t, blocked.Body.Bytes())
	if len(blockedRows) != 2 || blockedRows[1][7] != "old.example" {
		t.Fatalf("outcome filter exported %v", blockedRows)
	}
}

// TestExportAuditIsAudited covers the trail entry: an export of the audit log
// shows up in the audit log.
func TestExportAuditIsAudited(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	if rec := tp.do(http.MethodGet, "/api/v1/export/audit", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	audit := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditExportAudit, nil, nil))
	if len(audit.Items) != 1 {
		t.Fatalf("export was audited %d times, want once", len(audit.Items))
	}
}

// readCSV parses a CSV response body, failing the test on malformed input.
func readCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\n%s", err, body)
	}
	if len(rows) == 0 {
		t.Fatal("export produced no rows at all")
	}
	return rows
}
