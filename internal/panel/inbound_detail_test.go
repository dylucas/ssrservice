package panel

import (
	"net/http"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

// TestInboundDetailEndpoint covers the shape of the detail page: the traffic
// series is filled to the requested window, the busiest list is bounded, the
// status distribution matches the inbound, and the failures list is empty (but
// present) while recording is off.
func TestInboundDetailEndpoint(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedDetailInbound(t, tp)

	// Two clients: one with traffic today, one disabled.
	rec := tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "bob", "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	other := decode[model.Client](t, rec)
	if err := tp.store.SetClientEnabled(other.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	day := time.Now().In(tp.svc.Location()).Format("2006-01-02")
	if err := tp.store.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 1024, DownBytes: 2048,
		Day: day, DayUpBytes: 1024, DayDownBytes: 2048,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}

	detail := decode[struct {
		Inbound struct {
			ID   int64 `json:"id"`
			Port int   `json:"port"`
		} `json:"inbound"`
		Days  int `json:"days"`
		Daily []struct {
			Day  string `json:"day"`
			Up   int64  `json:"up_bytes"`
			Down int64  `json:"down_bytes"`
		} `json:"daily"`
		TopClients []struct {
			ID      int64 `json:"id"`
			UpBytes int64 `json:"up_bytes"`
		} `json:"top_clients"`
		StatusCounts struct {
			Active   int64 `json:"active"`
			Disabled int64 `json:"disabled"`
		} `json:"status_counts"`
		RecentFailures  []model.AccessEvent `json:"recent_failures"`
		AccessRecording bool                `json:"access_recording"`
	}](t, tp.do(http.MethodGet, "/api/v1/inbounds/"+itoa(in.ID)+"/detail?days=7", nil, nil))

	if detail.Inbound.ID != in.ID || detail.Days != 7 {
		t.Fatalf("unexpected detail header %+v", detail)
	}
	// One row per day of the window, with today carrying the traffic.
	if len(detail.Daily) != 7 {
		t.Fatalf("daily has %d rows, want 7", len(detail.Daily))
	}
	last := detail.Daily[len(detail.Daily)-1]
	if last.Day != day || last.Up != 1024 || last.Down != 2048 {
		t.Fatalf("last day = %+v, want today's traffic", last)
	}
	// The busiest user first.
	if len(detail.TopClients) == 0 || detail.TopClients[0].ID != client.ID {
		t.Fatalf("top clients = %+v", detail.TopClients)
	}
	if detail.StatusCounts.Active != 1 || detail.StatusCounts.Disabled != 1 {
		t.Fatalf("status counts = %+v, want one active and one disabled",
			detail.StatusCounts)
	}
	// Recording is off on a fresh install: the page gets an empty array plus the
	// flag that explains it, not null.
	if detail.AccessRecording {
		t.Fatal("recording should be off on a fresh install")
	}
	if detail.RecentFailures == nil || len(detail.RecentFailures) != 0 {
		t.Fatalf("recent failures = %v, want an empty array", detail.RecentFailures)
	}
}

// TestInboundDetailRecentFailures covers the failure list when recording is on:
// only blocked and failed accesses, newest first.
func TestInboundDetailRecentFailures(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	in, client := seedDetailInbound(t, tp)
	if err := tp.svc.UpdateSettings(settingsWithAccessLog(tp, true)); err != nil {
		t.Fatalf("enable recording: %v", err)
	}
	day := time.Now().In(tp.svc.Location()).Format("2006-01-02")
	now := time.Now().Unix()
	if err := tp.store.SaveAccess([]model.AccessRecord{
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "ok.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now - 3, Day: day},
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "blocked.example", Port: 80,
			Outcome: model.AccessBlocked, Reason: model.AccessReasonPolicy, StartedAt: now - 2, Day: day},
		{ClientID: client.ID, InboundID: in.ID, Network: "tcp", Host: "failed.example", Port: 443,
			Outcome: model.AccessFailed, Reason: model.AccessReasonResolve, StartedAt: now - 1, Day: day},
	}); err != nil {
		t.Fatalf("save access: %v", err)
	}

	detail := decode[struct {
		RecentFailures []struct {
			Host    string `json:"host"`
			Outcome string `json:"outcome"`
		} `json:"recent_failures"`
		AccessRecording bool `json:"access_recording"`
	}](t, tp.do(http.MethodGet, "/api/v1/inbounds/"+itoa(in.ID)+"/detail?days=7", nil, nil))
	if !detail.AccessRecording {
		t.Fatal("recording should be reported as on")
	}
	if len(detail.RecentFailures) != 2 {
		t.Fatalf("recent failures = %+v, want the two non-allowed rows", detail.RecentFailures)
	}
	if detail.RecentFailures[0].Host != "failed.example" {
		t.Fatalf("failures are not newest first: %+v", detail.RecentFailures)
	}
	for _, failure := range detail.RecentFailures {
		if failure.Outcome == model.AccessAllowed {
			t.Fatalf("an allowed access leaked into the failure list: %+v", failure)
		}
	}
}

// TestInboundDetailValidation covers the rejections and the missing-inbound case.
func TestInboundDetailValidation(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	_, _ = seedDetailInbound(t, tp)
	for _, query := range []string{"?days=0", "?days=91", "?days=abc", "?days=-1"} {
		if rec := tp.do(http.MethodGet, "/api/v1/inbounds/1/detail"+query, nil, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := tp.do(http.MethodGet, "/api/v1/inbounds/999999/detail", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing inbound: expected 404, got %d %s", rec.Code, rec.Body.String())
	}
}

// settingsWithAccessLog returns the current settings with the access log toggled.
// The full object is returned because PUT /settings replaces every field.
func settingsWithAccessLog(tp *testPanel, enabled bool) service.SettingsView {
	out := tp.svc.Settings()
	out.AccessLog = enabled
	return out
}

// seedDetailInbound creates one inbound and one client with keys the runtime can
// build a listener from, so the endpoint's reload succeeds.
func seedDetailInbound(t *testing.T, tp *testPanel) (model.Inbound, model.Client) {
	t.Helper()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "detail", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "alice", "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	return in, decode[model.Client](t, rec)
}
