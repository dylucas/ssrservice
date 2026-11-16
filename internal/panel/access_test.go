package panel

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
	"github.com/ydong/ssrservice/internal/store"
)

// seedAccessClient creates one inbound and one client for the access tests.
func seedAccessClient(t *testing.T, tp *testPanel) (model.Inbound, model.Client) {
	t.Helper()
	in := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28301,
		Method: model.Method2022AES128GCM, Enabled: true, UDPEnabled: true}
	if err := tp.store.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk-a", Enabled: true, SubToken: "tok-a"}
	if err := tp.store.CreateClient(&client); err != nil {
		t.Fatalf("client: %v", err)
	}
	return in, client
}

func TestAccessEndpointsRequireAuth(t *testing.T) {
	tp := newTestPanel(t)
	for _, path := range []string{"/api/v1/access/summary", "/api/v1/access/events"} {
		if rec := tp.do(http.MethodGet, path, nil, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d", path, rec.Code)
		}
	}
}

func TestAccessSummaryDisabledByDefault(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodGet, "/api/v1/access/summary", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	summary := decode[map[string]any](t, rec)
	if enabled, _ := summary["enabled"].(bool); enabled {
		t.Fatalf("access recording should be reported as off: %v", summary)
	}
	if days, _ := summary["days"].(float64); int(days) != service.AccessRawRetentionDays {
		t.Fatalf("default window = %v, want %d", summary["days"], service.AccessRawRetentionDays)
	}
	if total, _ := summary["total_hits"].(float64); total != 0 {
		t.Fatalf("unexpected hits on a fresh install: %v", summary)
	}
	if raw, _ := summary["raw_retention_days"].(float64); int(raw) != service.AccessRawRetentionDays {
		t.Fatalf("raw retention = %v", summary["raw_retention_days"])
	}
}

func TestAccessSummaryAndEvents(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedAccessClient(t, tp)

	day := time.Now().In(tp.svc.Location()).Format("2006-01-02")
	records := []model.AccessRecord{
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "example.com", Port: 443,
			IP: "93.184.216.34", Outcome: model.AccessAllowed, UpBytes: 120, DownBytes: 340,
			StartedAt: time.Now().Unix(), DurationMS: 40, Day: day},
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "blocked.example", Port: 80,
			Outcome: model.AccessBlocked, Reason: model.AccessReasonPolicy,
			StartedAt: time.Now().Unix(), Day: day},
	}
	if err := tp.store.SaveAccess(records); err != nil {
		t.Fatalf("save access: %v", err)
	}

	summary := decode[struct {
		Enabled    bool             `json:"enabled"`
		TotalHits  int64            `json:"total_hits"`
		Allowed    int64            `json:"allowed_hits"`
		Blocked    int64            `json:"blocked_hits"`
		Unique     int64            `json:"unique_hosts"`
		UpBytes    int64            `json:"up_bytes"`
		DownBytes  int64            `json:"down_bytes"`
		TopHosts   []map[string]any `json:"top_hosts"`
		TopClients []map[string]any `json:"top_clients"`
		Daily      []map[string]any `json:"daily"`
	}](t, tp.do(http.MethodGet, "/api/v1/access/summary?days=7", nil, nil))
	if summary.TotalHits != 2 || summary.Allowed != 1 || summary.Blocked != 1 {
		t.Fatalf("unexpected summary %+v", summary)
	}
	if summary.Unique != 2 || summary.UpBytes != 120 || summary.DownBytes != 340 {
		t.Fatalf("unexpected summary numbers %+v", summary)
	}
	if len(summary.TopHosts) != 2 || len(summary.TopClients) != 1 {
		t.Fatalf("unexpected top lists %+v", summary)
	}
	// The trend has one row per day of the window, including the days without
	// traffic, so the chart cannot compress a gap.
	if len(summary.Daily) != 7 {
		t.Fatalf("daily has %d rows, want one per window day: %+v", len(summary.Daily), summary.Daily)
	}
	if last := summary.Daily[len(summary.Daily)-1]; last["hits"] != float64(2) {
		t.Fatalf("last day = %+v, want the two records", last)
	}

	events := decode[accessEventsResponse](t, tp.do(http.MethodGet, "/api/v1/access/events", nil, nil))
	if events.Total != 2 || len(events.Items) != 2 {
		t.Fatalf("unexpected events %+v", events)
	}
	if events.Limit != store.AccessEventDefaultLimit || events.Offset != 0 {
		t.Fatalf("paging not echoed: %+v", events)
	}
	// Newest first, and the names come from the join.
	if events.Items[0].ClientName != "alice" || events.Items[0].InboundRemark != "main" {
		t.Fatalf("names not joined: %+v", events.Items[0])
	}

	// Filters narrow both endpoints.
	blocked := decode[accessEventsResponse](t,
		tp.do(http.MethodGet, "/api/v1/access/events?outcome=blocked", nil, nil))
	if blocked.Total != 1 || blocked.Items[0].Outcome != model.AccessBlocked {
		t.Fatalf("outcome filter: %+v", blocked)
	}
	scoped := decode[accessEventsResponse](t,
		tp.do(http.MethodGet, "/api/v1/access/events?q=example.com", nil, nil))
	if scoped.Total != 1 || scoped.Items[0].Host != "example.com" {
		t.Fatalf("host filter: %+v", scoped)
	}
	byClient := decode[accessEventsResponse](t,
		tp.do(http.MethodGet, "/api/v1/access/events?client_id="+strconv.FormatInt(client.ID, 10), nil, nil))
	if byClient.Total != 2 {
		t.Fatalf("client filter: %+v", byClient)
	}
	filtered := decode[struct {
		TotalHits   int64 `json:"total_hits"`
		UniqueHosts int64 `json:"unique_hosts"`
	}](t, tp.do(http.MethodGet,
		"/api/v1/access/summary?client_id="+strconv.FormatInt(client.ID, 10)+"&inbound_id="+
			strconv.FormatInt(in.ID, 10), nil, nil))
	if filtered.TotalHits != 2 {
		t.Fatalf("summary filters: %+v", filtered)
	}
}

func TestAccessEndpointValidation(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	bad := []string{
		"/api/v1/access/summary?days=0",
		"/api/v1/access/summary?days=" + strconv.Itoa(service.AccessMaxWindowDays+1),
		"/api/v1/access/summary?days=abc",
		"/api/v1/access/summary?client_id=-1",
		"/api/v1/access/summary?inbound_id=abc",
		"/api/v1/access/events?limit=0",
		"/api/v1/access/events?limit=" + strconv.Itoa(store.AccessEventMaxLimit+1),
		"/api/v1/access/events?offset=-1",
		"/api/v1/access/events?outcome=weird",
		"/api/v1/access/events?network=icmp",
		"/api/v1/access/events?q=" + url.QueryEscape(repeatRune('a', store.AccessQueryMaxLen+1)),
		"/api/v1/access/events?since=abc",
		"/api/v1/access/events?since=200&until=100",
		"/api/v1/access/events?since_day=2026-13-01",
		"/api/v1/access/events?since_day=yesterday",
		"/api/v1/access/events?since_day=2026-11-15&until_day=2026-11-14",
	}
	for _, path := range bad {
		rec := tp.do(http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", path, rec.Code, rec.Body.String())
		}
	}

	good := []string{
		"/api/v1/access/summary",
		"/api/v1/access/summary?days=1",
		"/api/v1/access/summary?days=7",
		"/api/v1/access/summary?days=" + strconv.Itoa(service.AccessMaxWindowDays),
		"/api/v1/access/events?limit=1&offset=0",
		"/api/v1/access/events?outcome=all&network=all",
		"/api/v1/access/events?since=100&until=200",
		"/api/v1/access/events?since_day=2026-11-10&until_day=2026-11-16",
		"/api/v1/access/events?since_day=2026-11-16",
	}
	for _, path := range good {
		rec := tp.do(http.MethodGet, path, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

// TestAccessToggleAppliesToRuntime walks the setting through the API and checks
// that the runtime follows it without a restart; the audit trail records the
// change but no destination history.
func TestAccessToggleAppliesToRuntime(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	settings := decode[service.SettingsView](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))
	if settings.AccessLog {
		t.Fatal("access recording must default to off")
	}
	settings.AccessLog = true
	if rec := tp.do(http.MethodPut, "/api/v1/settings", settings, nil); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if !tp.svc.AccessEnabled() {
		t.Fatal("the runtime did not pick the setting up")
	}
	got := decode[service.SettingsView](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))
	if !got.AccessLog {
		t.Fatalf("setting not persisted: %+v", got)
	}
	summary := decode[map[string]any](t, tp.do(http.MethodGet, "/api/v1/access/summary", nil, nil))
	if enabled, _ := summary["enabled"].(bool); !enabled {
		t.Fatalf("summary still reports recording as off: %v", summary)
	}

	logs := decode[struct {
		Items []model.AuditLog `json:"items"`
	}](t, tp.do(http.MethodGet, "/api/v1/audit?action=settings.update", nil, nil))
	if len(logs.Items) == 0 {
		t.Fatal("settings change was not audited")
	}
	if detail := logs.Items[0].Detail; !containsAll(detail, "access_log_enabled=true") {
		t.Fatalf("audit detail does not record the switch: %q", detail)
	}

	// The historical records stay readable after the switch is turned off; the
	// page uses `enabled` to say that nothing new is being collected.
	settings.AccessLog = false
	if rec := tp.do(http.MethodPut, "/api/v1/settings", settings, nil); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if tp.svc.AccessEnabled() {
		t.Fatal("the runtime kept recording after it was turned off")
	}
	if rec := tp.do(http.MethodGet, "/api/v1/access/events", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("history should stay readable: %d %s", rec.Code, rec.Body.String())
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		found := false
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestAccessEventsDayWindow covers the day form of the window: the picker picks
// days, and only the server knows the zone those days belong to, so the day
// boundaries are resolved server-side and the end date is inclusive.
func TestAccessEventsDayWindow(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedAccessClient(t, tp)

	loc := tp.svc.Location()
	today := time.Now().In(loc)
	yesterday := today.AddDate(0, 0, -1)
	if err := tp.store.SaveAccess([]model.AccessRecord{
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "today.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: today.Unix(),
			Day: today.Format("2006-01-02")},
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "yesterday.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: yesterday.Unix(),
			Day: yesterday.Format("2006-01-02")},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	todayDay := today.Format("2006-01-02")
	both := decode[accessEventsResponse](t, tp.do(http.MethodGet,
		"/api/v1/access/events?since_day="+yesterday.Format("2006-01-02")+"&until_day="+todayDay, nil, nil))
	if both.Total != 2 {
		t.Fatalf("inclusive day window returned %d rows, want 2: %+v", both.Total, both.Items)
	}

	onlyToday := decode[accessEventsResponse](t, tp.do(http.MethodGet,
		"/api/v1/access/events?since_day="+todayDay, nil, nil))
	if onlyToday.Total != 1 || onlyToday.Items[0].Host != "today.example" {
		t.Fatalf("open-ended day window = %+v", onlyToday)
	}
}
