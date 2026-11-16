package panel

import (
	"net/http"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// TestSettingsRequireCompleteObject guards the replace semantics of PUT
// /settings: decoding a partial body used to zero every field it did not carry
// and treat the empty timezone as UTC, which silently moved accounting to UTC and
// switched subscriptions off.
func TestSettingsRequireCompleteObject(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	before := decode[map[string]any](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty timezone", map[string]any{"access_log_enabled": true}},
		{"unknown timezone", map[string]any{"timezone": "Mars/Olympus", "reset_day": 1}},
	}
	for _, tc := range cases {
		if rec := tp.do(http.MethodPut, "/api/v1/settings", tc.body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	after := decode[map[string]any](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))
	for key, want := range before {
		if after[key] != want {
			t.Fatalf("rejected update changed %q: %v -> %v", key, want, after[key])
		}
	}
}

// TestSettingsNormalizePublicHost checks that the host is stored the way it is
// validated: share links are built from the stored value, so an uppercase or
// trailing-dot form has to be normalized before it lands there.
func TestSettingsNormalizePublicHost(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	settings := decode[map[string]any](t, tp.do(http.MethodGet, "/api/v1/settings", nil, nil))
	settings["public_host"] = " Example.COM. "
	settings["public_port"] = 8388
	rec := tp.do(http.MethodPut, "/api/v1/settings", settings, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	updated := decode[map[string]any](t, rec)
	if updated["public_host"] != "example.com" {
		t.Fatalf("public_host = %v, want the normalized form", updated["public_host"])
	}
}

// TestToggleRequiresExplicitEnabled covers a request that forgets the field: it
// must be rejected instead of silently disabling the inbound or client.
func TestToggleRequiresExplicitEnabled(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "toggle", "listen": "127.0.0.1", "port": freeTestPort(t),
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
	client := decode[model.Client](t, rec)

	for _, path := range []string{
		"/api/v1/inbounds/" + itoa(in.ID) + "/toggle",
		"/api/v1/clients/" + itoa(client.ID) + "/toggle",
	} {
		if rec := tp.do(http.MethodPost, path, map[string]any{}, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s with no enabled: expected 400, got %d %s", path, rec.Code, rec.Body.String())
		}
		// An explicit value still works, in both directions.
		if rec := tp.do(http.MethodPost, path, map[string]any{"enabled": false}, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s disable: %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := tp.do(http.MethodPost, path, map[string]any{"enabled": true}, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s enable: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// TestExportRejectsMalformedInboundID covers a typo turning into "every client":
// the parameter used to be parsed with the error discarded, and 0 means export
// the whole database, PSKs included.
func TestExportRejectsMalformedInboundID(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	for _, value := range []string{"abc", "-1", "1.5"} {
		rec := tp.do(http.MethodGet, "/api/v1/export/clients?inbound_id="+value, nil, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("inbound_id=%q accepted with %d %s", value, rec.Code, rec.Body.String())
		}
	}
	// The valid form still downloads.
	if rec := tp.do(http.MethodGet, "/api/v1/export/clients", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("plain export: %d %s", rec.Code, rec.Body.String())
	}
}

// TestAuditRejectsMalformedParameters keeps the audit list consistent with the
// client and access lists: a bad parameter is an error, not a silent fallback to
// the default page.
func TestAuditRejectsMalformedParameters(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	for _, query := range []string{
		"limit=abc", "limit=0", "limit=501", "offset=-1", "offset=abc",
		"since=abc", "until=abc", "since=200&until=100",
	} {
		rec := tp.do(http.MethodGet, "/api/v1/audit?"+query, nil, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("audit?%s: expected 400, got %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := tp.do(http.MethodGet, "/api/v1/audit?limit=10&offset=0&since=1&until=9999999999", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("valid audit query: %d %s", rec.Code, rec.Body.String())
	}
}

// TestClientUsageRejectsMalformedDays mirrors the access endpoints: the day count
// is validated rather than quietly replaced with the default window.
func TestClientUsageRejectsMalformedDays(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "usage", "listen": "127.0.0.1", "port": freeTestPort(t),
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
	client := decode[model.Client](t, rec)

	path := "/api/v1/clients/" + itoa(client.ID) + "/usage"
	for _, query := range []string{"?days=abc", "?days=0", "?days=-5", "?days=366"} {
		if rec := tp.do(http.MethodGet, path+query, nil, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := tp.do(http.MethodGet, path+"?days=7", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("valid usage query: %d %s", rec.Code, rec.Body.String())
	}
}

// TestSubscriptionIsNotCached guards the credential response: the subscription
// body embeds the client PSK, so no cache may keep it.
func TestSubscriptionIsNotCached(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "sub", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": "sub-user", "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)

	// The subscription route is outside the API group, so it is requested
	// without the session cookie.
	tp.cookies = nil
	rec = tp.do(http.MethodGet, "/sub/"+client.SubToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	// The 404 path carries it too: a cache must not keep the answer either.
	rec = tp.do(http.MethodGet, "/sub/unknown-token", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("404 Cache-Control = %q, want no-store", got)
	}
}
