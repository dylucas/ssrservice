package service

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// newAccessEnv builds a Service over a temp store, without starting the
// background loops unless a test asks for them.
func newAccessEnv(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "access.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	mgr := ssserver.New(ssserver.Options{
		Sink: st, AccessSink: st, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: 10 * time.Second,
		Policy: ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	svc := New(st, mgr, cfg, nil)
	t.Cleanup(func() { svc.Stop() })
	if err := svc.seedSettings(); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := svc.loadSettings(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	return svc, st
}

func TestAccessLogDefaultsOffAndPersists(t *testing.T) {
	svc, st := newAccessEnv(t)

	if svc.Settings().AccessLog {
		t.Fatal("access recording must default to off")
	}
	if svc.mgr.AccessEnabled() {
		t.Fatal("runtime must start with access recording off")
	}
	if raw, err := st.GetSetting(model.SettingAccessLog); err != nil || raw != "false" {
		t.Fatalf("stored setting = %q (%v), want false", raw, err)
	}

	settings := svc.Settings()
	settings.AccessLog = true
	if err := svc.UpdateSettings(settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if !svc.Settings().AccessLog || !svc.mgr.AccessEnabled() {
		t.Fatal("enabling the setting must reach the runtime")
	}
	if raw, _ := st.GetSetting(model.SettingAccessLog); raw != "true" {
		t.Fatalf("stored setting = %q, want true", raw)
	}

	// A restart reads the value back and applies it to the new runtime.
	mgr := ssserver.New(ssserver.Options{
		Sink: st, AccessSink: st, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: time.Second,
		Policy: ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	t.Cleanup(func() { mgr.Stop() })
	restarted := New(st, mgr, svc.cfg, nil)
	if err := restarted.loadSettings(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !restarted.mgr.AccessEnabled() {
		t.Fatal("the persisted setting was not applied on reload")
	}

	// Turning it off again takes effect without a restart.
	settings = restarted.Settings()
	settings.AccessLog = false
	if err := restarted.UpdateSettings(settings); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if restarted.mgr.AccessEnabled() {
		t.Fatal("disabling the setting must stop recording")
	}
}

func TestAccessSummaryWindowUsesEffectiveTimezone(t *testing.T) {
	svc, st := newAccessEnv(t)
	in := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28101,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk", Enabled: true, SubToken: "tok"}
	if err := st.CreateClient(&client); err != nil {
		t.Fatalf("client: %v", err)
	}

	// The accounting zone is set to UTC+14, so "today" there is tomorrow's UTC
	// date late in the UTC day. A record bucketed on the zone's today must fall
	// inside a one day window.
	settings := svc.Settings()
	settings.Timezone = "Pacific/Kiritimati"
	if err := svc.UpdateSettings(settings); err != nil {
		t.Fatalf("timezone: %v", err)
	}
	day := time.Now().In(svc.Location()).Format("2006-01-02")
	record := model.AccessRecord{ClientID: client.ID, InboundID: in.ID, Network: "tcp",
		Host: "example.com", Port: 443, Outcome: model.AccessAllowed,
		UpBytes: 10, DownBytes: 20, StartedAt: time.Now().Unix(), Day: day}
	if err := st.SaveAccess([]model.AccessRecord{record}); err != nil {
		t.Fatalf("save: %v", err)
	}

	summary, err := svc.AccessSummary(1, 0, 0)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalHits != 1 || summary.UpBytes != 10 {
		t.Fatalf("the record fell outside its own day window: %+v", summary)
	}

	// A 7 day window is exactly 7 points even though only one day has a record:
	// the chart is drawn by index, so a missing day would compress the axis.
	week, err := svc.AccessSummary(7, 0, 0)
	if err != nil {
		t.Fatalf("week summary: %v", err)
	}
	if len(week.Daily) != 7 {
		t.Fatalf("7 day window has %d daily rows, want one per day", len(week.Daily))
	}
	last := week.Daily[len(week.Daily)-1]
	if last.Day != day || last.Hits != 1 {
		t.Fatalf("last day = %+v, want today %s with the record", last, day)
	}
	for _, row := range week.Daily[:len(week.Daily)-1] {
		if row.Day == day {
			continue
		}
		if row.Hits != 0 {
			t.Fatalf("day without a record = %+v, want zeroes", row)
		}
	}

	// An invalid day count falls back to the raw retention window instead of
	// producing an empty "0 day" query.
	fallback, err := svc.AccessSummary(0, 0, 0)
	if err != nil {
		t.Fatalf("fallback summary: %v", err)
	}
	if fallback.TotalHits != 1 {
		t.Fatalf("fallback window = %+v", fallback)
	}
	// A window wider than the rollup is clamped, not rejected.
	clamped, err := svc.AccessSummary(AccessMaxWindowDays+30, 0, 0)
	if err != nil {
		t.Fatalf("clamped summary: %v", err)
	}
	if clamped.TotalHits != 1 {
		t.Fatalf("clamped window = %+v", clamped)
	}
}

func TestPruneAccessUsesBothWindows(t *testing.T) {
	svc, st := newAccessEnv(t)
	in := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28102,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk", Enabled: true, SubToken: "tok"}
	if err := st.CreateClient(&client); err != nil {
		t.Fatalf("client: %v", err)
	}

	now := time.Now()
	rawDays, aggregateDays := AccessRetention()
	records := []model.AccessRecord{
		// Raw beyond one week, rollup still inside the quarter.
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "a.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now.AddDate(0, 0, -(rawDays + 2)).Unix(),
			Day: now.AddDate(0, 0, -(rawDays + 2)).Format("2006-01-02")},
		// Beyond both windows.
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "b.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now.AddDate(0, 0, -(aggregateDays + 5)).Unix(),
			Day: now.AddDate(0, 0, -(aggregateDays + 5)).Format("2006-01-02")},
		// Fresh: kept in both.
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "c.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now.Unix(),
			Day: now.Format("2006-01-02")},
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	svc.pruneAccess(false)

	events, err := st.CountAccessEvents()
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("raw events left = %d, want 1", events)
	}
	// The rollup keeps the week-old day (inside the quarter) and the fresh day.
	daily, err := st.CountAccessDaily()
	if err != nil {
		t.Fatalf("count daily: %v", err)
	}
	if daily != 2 {
		t.Fatalf("rollup rows left = %d, want 2", daily)
	}
}

func TestAccessRetentionValues(t *testing.T) {
	raw, aggregate := AccessRetention()
	if raw != 7 {
		t.Fatalf("raw retention = %d, want 7", raw)
	}
	if aggregate != 90 {
		t.Fatalf("aggregate retention = %d, want 90", aggregate)
	}
	if AccessMaxWindowDays != aggregate {
		t.Fatalf("max window = %d, want the aggregate retention %d", AccessMaxWindowDays, aggregate)
	}
}

// TestPruneAccessFoldsRecentOverflow covers the cardinality cap being part of the
// retention chore: a day that collected more destinations than the cap keeps the
// most visited ones and folds the rest, so the rollup stays bounded.
func TestPruneAccessFoldsRecentOverflow(t *testing.T) {
	svc, st := newAccessEnv(t)
	in := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28103,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk", Enabled: true, SubToken: "tok"}
	if err := st.CreateClient(&client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	day := time.Now().In(svc.Location()).Format("2006-01-02")
	records := make([]model.AccessRecord, 0, model.AccessMaxHostsPerKey+20)
	for i := 0; i < model.AccessMaxHostsPerKey+20; i++ {
		records = append(records, model.AccessRecord{
			ClientID: client.ID, InboundID: in.ID, Network: "tcp",
			Host: "overflow-" + strconv.Itoa(i) + ".example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: time.Now().Unix(), Day: day,
		})
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	svc.pruneAccess(false)

	rows, err := st.CountAccessDaily()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != model.AccessMaxHostsPerKey+1 {
		t.Fatalf("%d rollup rows after the retention pass, want the cap plus the overflow row", rows)
	}
	summary, err := svc.AccessSummary(7, 0, 0)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalHits != int64(len(records)) {
		t.Fatalf("hits after folding = %d, want %d", summary.TotalHits, len(records))
	}
}
