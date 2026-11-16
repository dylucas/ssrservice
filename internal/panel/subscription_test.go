package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// seedSubscriptionClient creates one inbound and one active client and returns
// the client's subscription token and id.
func seedSubscriptionClient(t *testing.T, tp *testPanel, name string) (string, int64, int64) {
	t.Helper()
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "sub-" + name, "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)
	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", map[string]any{
		"name": name, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create client: %d %s", rec.Code, rec.Body.String())
	}
	client := decode[model.Client](t, rec)
	return client.SubToken, client.ID, in.ID
}

// TestSubscriptionUserInfoHeader covers the account figures clients render from
// the response headers: used / total, and the expiry instant.
func TestSubscriptionUserInfoHeader(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	token, clientID, _ := seedSubscriptionClient(t, tp, "alice")

	// Give the account a quota, a usage total and an expiry so the header has
	// values to report.
	rec := tp.do(http.MethodPut, "/api/v1/clients/"+itoa(clientID), map[string]any{
		"name": "alice", "enabled": true,
		"quota_bytes": 10 * 1024 * 1024 * 1024,
		"expires_at":  1900000000,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update client: %d %s", rec.Code, rec.Body.String())
	}
	if err := tp.store.SaveUsage(nil); err != nil {
		t.Fatalf("save usage: %v", err)
	}

	tp.cookies = nil
	rec = tp.do(http.MethodGet, "/sub/"+token, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", rec.Code, rec.Body.String())
	}
	header := rec.Header().Get("Subscription-Userinfo")
	if header == "" {
		t.Fatal("Subscription-Userinfo is missing")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			t.Fatalf("malformed field %q in %q", part, header)
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if got := fields["total"]; got != strconv.Itoa(10*1024*1024*1024) {
		t.Fatalf("total = %q, want the quota: %q", got, header)
	}
	if got := fields["expire"]; got != "1900000000" {
		t.Fatalf("expire = %q, want the expiry: %q", got, header)
	}
	if _, ok := fields["upload"]; !ok {
		t.Fatalf("upload missing: %q", header)
	}
	if _, ok := fields["download"]; !ok {
		t.Fatalf("download missing: %q", header)
	}
	// Unlimited and never have explicit zero values, not missing fields: a
	// client that treats a missing field as unknown would hide the numbers it
	// does have.
	if got := rec.Header().Get("Profile-Update-Interval"); got != "12" {
		t.Fatalf("Profile-Update-Interval = %q", got)
	}
	if got := rec.Header().Get("Profile-Title"); !strings.HasPrefix(got, "base64:") {
		t.Fatalf("Profile-Title = %q, want the base64 form", got)
	}

	// A 404 must not carry the account headers: they would confirm that the
	// token exists.
	missing := tp.do(http.MethodGet, "/sub/unknown-token", nil, nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", missing.Code)
	}
	if got := missing.Header().Get("Subscription-Userinfo"); got != "" {
		t.Fatalf("404 carried Subscription-Userinfo %q", got)
	}
}

// TestSubscriptionUnlimitedAndNever checks the two documented zero values.
func TestSubscriptionUnlimitedAndNever(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	token, _, _ := seedSubscriptionClient(t, tp, "bob")

	tp.cookies = nil
	rec := tp.do(http.MethodGet, "/sub/"+token, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscription: %d %s", rec.Code, rec.Body.String())
	}
	header := rec.Header().Get("Subscription-Userinfo")
	for _, want := range []string{"total=0", "expire=0"} {
		if !strings.Contains(header, want) {
			t.Fatalf("header %q is missing %q", header, want)
		}
	}
}

// TestSubscriptionSingBoxFormat checks format dispatch and that the payload is a
// loadable profile.
func TestSubscriptionSingBoxFormat(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()
	token, _, _ := seedSubscriptionClient(t, tp, "carol")

	tp.cookies = nil
	for _, path := range []string{"/sub/" + token + "?format=singbox", "/sub/" + token + "?format=sing-box"} {
		rec := tp.do(http.MethodGet, path, map[string]any{}, map[string]string{"User-Agent": ""})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("%s: Content-Type = %q", path, got)
		}
		var profile struct {
			Outbounds []struct {
				Type     string `json:"type"`
				Tag      string `json:"tag"`
				Server   string `json:"server"`
				Port     int    `json:"server_port"`
				Method   string `json:"method"`
				Password string `json:"password"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
			t.Fatalf("%s: not valid JSON: %v\n%s", path, err, rec.Body.String())
		}
		if len(profile.Outbounds) != 1 {
			t.Fatalf("%s: %d outbounds", path, len(profile.Outbounds))
		}
		outbound := profile.Outbounds[0]
		if outbound.Type != "shadowsocks" || outbound.Tag != "carol" {
			t.Fatalf("%s: unexpected outbound %+v", path, outbound)
		}
		if outbound.Method != model.Method2022AES128GCM || outbound.Port == 0 {
			t.Fatalf("%s: unexpected endpoint %+v", path, outbound)
		}
		// Shadowsocks 2022 credentials carry the server PSK.
		if !strings.Contains(outbound.Password, ":") {
			t.Fatalf("%s: password %q has no server PSK separator", path, outbound.Password)
		}
	}

	// A sing-box user-agent is detected without the query parameter.
	rec := tp.do(http.MethodGet, "/sub/"+token, map[string]any{},
		map[string]string{"User-Agent": "sing-box 1.10.0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ua negotiation: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("sing-box user agent did not select the JSON profile: %q", got)
	}
}
