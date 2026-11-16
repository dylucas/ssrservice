package panel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// seedBatchClients creates one multi-user inbound with n clients.
func seedBatchClients(t *testing.T, tp *testPanel, n int) (model.Inbound, []model.Client) {
	t.Helper()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "batch", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	clients := make([]model.Client, 0, n)
	for i := 0; i < n; i++ {
		rec := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
			"name": "user-" + itoa(int64(i)), "enabled": true,
			"quota_bytes": 1024, "expires_at": 1900000000,
		}, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create client %d: %d %s", i, rec.Code, rec.Body.String())
		}
		clients = append(clients, decode[model.Client](t, rec))
	}
	return in, clients
}

// TestBatchEndpointDisablesAndEnables covers the happy path through HTTP.
func TestBatchEndpointDisablesAndEnables(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	_, clients := seedBatchClients(t, tp, 3)
	ids := []int64{clients[0].ID, clients[1].ID, clients[2].ID}

	rec := tp.do(http.MethodPost, "/api/v1/batch/clients", map[string]any{
		"action": "disable", "ids": ids,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch disable: %d %s", rec.Code, rec.Body.String())
	}
	result := decode[struct {
		Action  string `json:"action"`
		Applied int    `json:"applied"`
		Failed  []struct {
			ID    int64  `json:"id"`
			Error string `json:"error"`
		} `json:"failed"`
	}](t, rec)
	if result.Action != "disable" || result.Applied != 3 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result %+v", result)
	}
	for _, c := range clients {
		got := decode[model.Client](t, tp.do(http.MethodGet, "/api/v1/clients/"+itoa(c.ID), nil, nil))
		if got.Enabled {
			t.Fatalf("%s is still enabled", c.Name)
		}
	}

	// The action is audited once, summarised rather than listing every id.
	audit := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action="+model.AuditClientBatch, nil, nil))
	if len(audit.Items) != 1 {
		t.Fatalf("batch was audited %d times", len(audit.Items))
	}
	detail := audit.Items[0].Detail
	for _, want := range []string{`action="disable"`, "selected=3", "applied=3"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("audit detail %q is missing %q", detail, want)
		}
	}
}

// TestBatchEndpointValidation covers the rejections: a bad request must not
// half-apply.
func TestBatchEndpointValidation(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	_, clients := seedBatchClients(t, tp, 1)
	id := clients[0].ID

	bad := []map[string]any{
		{"action": "explode", "ids": []int64{id}},
		{"action": "enable"},
		{"action": "enable", "ids": []int64{}},
		{"action": "enable", "ids": []int64{0}},
		{"action": "extend", "ids": []int64{id}, "days": 0},
		{"action": "extend", "ids": []int64{id}, "days": 99999},
		{"action": "add_quota", "ids": []int64{id}, "bytes": 0},
	}
	for _, body := range bad {
		if rec := tp.do(http.MethodPost, "/api/v1/batch/clients", body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: expected 400, got %d %s", body, rec.Code, rec.Body.String())
		}
	}
	// Nothing was changed by the rejected requests.
	got := decode[model.Client](t, tp.do(http.MethodGet, "/api/v1/clients/"+itoa(id), nil, nil))
	if !got.Enabled || got.QuotaBytes != 1024 || got.ExpiresAt != 1900000000 {
		t.Fatalf("a rejected batch still changed the client: %+v", got)
	}
}

// TestBatchEndpointReportsUnknownIDs covers partial success through HTTP: the
// reply names the id that does not exist instead of failing the request.
func TestBatchEndpointReportsUnknownIDs(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	_, clients := seedBatchClients(t, tp, 1)

	rec := tp.do(http.MethodPost, "/api/v1/batch/clients", map[string]any{
		"action": "disable", "ids": []int64{clients[0].ID, 987654},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	result := decode[struct {
		Applied int `json:"applied"`
		Failed  []struct {
			ID    int64  `json:"id"`
			Error string `json:"error"`
		} `json:"failed"`
	}](t, rec)
	if result.Applied != 1 || len(result.Failed) != 1 || result.Failed[0].ID != 987654 {
		t.Fatalf("unexpected result %+v", result)
	}
}

// TestDashboardAttentionMatchesFilteredLists covers the deep link the dashboard
// cards rely on: each number equals the total of the list it points at.
func TestDashboardAttentionMatchesFilteredLists(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, clients := seedBatchClients(t, tp, 2)

	// Expire one of them and exceed the quota of the other, then clear one's
	// traffic so it stays idle.
	expiring := tp.do(http.MethodPut, "/api/v1/clients/"+itoa(clients[0].ID), map[string]any{
		"name": clients[0].Name, "enabled": true, "quota_bytes": 1024,
		"expires_at": nowUnixPlus(3 * 24 * 60 * 60),
	}, nil)
	if expiring.Code != http.StatusOK {
		t.Fatalf("expire: %d %s", expiring.Code, expiring.Body.String())
	}
	overQuota := tp.do(http.MethodPut, "/api/v1/clients/"+itoa(clients[1].ID), map[string]any{
		"name": clients[1].Name, "enabled": true, "quota_bytes": 1,
		"expires_at": 1900000000,
	}, nil)
	if overQuota.Code != http.StatusOK {
		t.Fatalf("quota: %d %s", overQuota.Code, overQuota.Body.String())
	}
	// Move the second client's counters over its quota the way the runtime does:
	// the usage columns are accounting state, not form fields, so UpdateClient
	// never writes them.
	if err := tp.store.SaveUsage([]model.UsageSnapshot{{
		ClientID: clients[1].ID, UpBytes: 10, DownBytes: 0,
		Day: nowFunc().In(tp.svc.Location()).Format("2006-01-02"), DayUpBytes: 10,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	_ = in

	dashboard := decode[struct {
		Attention struct {
			ExpiringSoon  int64 `json:"expiring_soon"`
			Expired       int64 `json:"expired"`
			QuotaExceeded int64 `json:"quota_exceeded"`
			Idle          int64 `json:"idle"`
			WindowDays    int   `json:"expiring_window_days"`
		} `json:"attention"`
	}](t, tp.do(http.MethodGet, "/api/v1/dashboard", nil, nil))
	if dashboard.Attention.WindowDays != 7 {
		t.Fatalf("window = %d, want 7", dashboard.Attention.WindowDays)
	}
	if dashboard.Attention.ExpiringSoon != 1 {
		t.Fatalf("expiring_soon = %d, want 1", dashboard.Attention.ExpiringSoon)
	}
	if dashboard.Attention.QuotaExceeded != 1 {
		t.Fatalf("quota_exceeded = %d, want 1", dashboard.Attention.QuotaExceeded)
	}

	// Each card links to /clients?status=..., and the list total has to agree.
	for _, status := range []string{"expiring", "quota_exceeded", "idle", "expired"} {
		list := decode[struct {
			Total int64 `json:"total"`
		}](t, tp.do(http.MethodGet, "/api/v1/clients?status="+status, nil, nil))
		var want int64
		switch status {
		case "expiring":
			want = dashboard.Attention.ExpiringSoon
		case "quota_exceeded":
			want = dashboard.Attention.QuotaExceeded
		case "idle":
			want = dashboard.Attention.Idle
		case "expired":
			want = dashboard.Attention.Expired
		}
		if list.Total != want {
			t.Fatalf("status=%s: list total %d, dashboard card %d", status, list.Total, want)
		}
	}
}

// nowUnixPlus returns the current unix time shifted by delta seconds.
func nowUnixPlus(delta int64) int64 {
	return nowFunc().Unix() + delta
}
