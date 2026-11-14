package service

import (
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

func TestLastResetBoundary(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, loc)
	got := time.Unix(LastResetBoundary(now, loc, 1), 0).In(loc)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}

	// Before the reset day: use the previous month, including February.
	now = time.Date(2026, 3, 2, 0, 30, 0, 0, loc)
	got = time.Unix(LastResetBoundary(now, loc, 5), 0).In(loc)
	want = time.Date(2026, 2, 5, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("got %s want %s", got, want)
	}

	// Exactly at the boundary counts as reached.
	now = time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	got = time.Unix(LastResetBoundary(now, loc, 1), 0).In(loc)
	if !got.Equal(now) {
		t.Fatalf("got %s want %s", got, now)
	}
}

func TestValidHost(t *testing.T) {
	valid := []string{"example.com", "1.2.3.4", "sub.domain", "2001:db8::1"}
	for _, host := range valid {
		if !validHost(host) {
			t.Fatalf("%q should be valid", host)
		}
	}
	invalid := []string{"", "bad host", "http://x", "a/b"}
	for _, host := range invalid {
		if validHost(host) {
			t.Fatalf("%q should be invalid", host)
		}
	}
}

func TestValidateListenAddress(t *testing.T) {
	if err := validateListenAddress("127.0.0.1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateListenAddress("0.0.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bad := range []string{"", "127.0.0.1:8388", "host name"} {
		if err := validateListenAddress(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

// TestValidateHostValue covers the strict host validation used for the inbound
// domain and the global public_host setting.
func TestValidateHostValue(t *testing.T) {
	valid := []string{
		"example.com",
		"sub.domain.example.com",
		"hk-node-1.example.com",
		"1.2.3.4",
		"2001:db8::1",
		"a.b",
		strings.Repeat("a", 63) + ".com",
	}
	for _, host := range valid {
		if err := validateHostValue(host); err != nil {
			t.Fatalf("%q should be valid: %v", host, err)
		}
	}

	// Each entry documents a value that would break an ss:// link if accepted.
	// validateHostValue expects an already normalized value: callers run
	// normalizeHost first, so a value of spaces arrives as "" there.
	invalid := map[string]string{
		"":                                "empty",
		" ":                               "invalid character",
		"bad host":                        "invalid character",
		"http://example.com":              "invalid character",
		"example.com/x":                   "invalid character",
		"example.com:8388":                "invalid character",
		"[::1]":                           "invalid character",
		"a..b":                            "empty labels",
		"example..com":                    "empty labels",
		"-bad.example.com":                "hyphen",
		"bad-.example.com":                "hyphen",
		"bad_host.example.com":            "invalid character",
		"fe80::1%en0":                     "zone",
		strings.Repeat("a", 64):           "at most 63",
		strings.Repeat("a", 250) + ".com": "at most 253",
	}
	for host, hint := range invalid {
		err := validateHostValue(host)
		if err == nil {
			t.Fatalf("%q should be rejected", host)
		}
		if !strings.Contains(err.Error(), hint) {
			t.Fatalf("error for %q = %v, want it to mention %q", host, err, hint)
		}
	}
}

// TestNormalizeHost documents the canonical form stored in the database.
func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"  Example.COM.  ": "example.com",
		"example.com":      "example.com",
		".":                "",
		"":                 "",
		"1.2.3.4":          "1.2.3.4",
	}
	for input, want := range cases {
		if got := normalizeHost(input); got != want {
			t.Fatalf("normalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestRunScheduledResets covers the three branches of the monthly reset worker,
// which had no test at all.
//
// The dangerous ones are the first tick after an upgrade (which must establish a
// baseline instead of wiping someone's traffic) and the boundary comparison
// (which must not re-reset a client that was already reset this period).
func TestRunScheduledResets(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	// 15 March 2026, 12:00 CST: the most recent day-1 boundary is 1 March.
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, loc)
	boundary := time.Date(2026, 3, 1, 0, 0, 0, 0, loc).Unix()

	newClient := func(name string, policy string, lastReset int64, day int) model.Client {
		return model.Client{
			Name: name, Enabled: true, UpBytes: 1000, DownBytes: 500,
			ResetPolicy: policy, ResetDay: day, LastResetAt: lastReset,
			PSK: "unused-for-reset", SubToken: "tok-" + name,
		}
	}

	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	svc.mu.Lock()
	svc.loc = loc
	svc.resetDay = 1
	svc.mu.Unlock()

	cases := []struct {
		name      string
		client    model.Client
		wantReset bool
		wantLast  int64
	}{
		{
			name:      "first tick establishes a baseline",
			client:    newClient("fresh", model.ResetMonthly, 0, 0),
			wantReset: false,
			wantLast:  now.Unix(),
		},
		{
			name:      "boundary already applied is skipped",
			client:    newClient("applied", model.ResetMonthly, boundary, 0),
			wantReset: false,
			wantLast:  boundary,
		},
		{
			name:      "boundary reached resets the counters",
			client:    newClient("due", model.ResetMonthly, boundary-86400*20, 0),
			wantReset: true,
			wantLast:  boundary,
		},
		{
			name:      "per-client reset day overrides the global one",
			client:    newClient("perday", model.ResetMonthly, 0, 10),
			wantReset: false,
			wantLast:  now.Unix(),
		},
		{
			name:      "policy none is never a candidate",
			client:    newClient("none", model.ResetNone, boundary-86400*20, 0),
			wantReset: false,
			wantLast:  boundary - 86400*20,
		},
	}

	created := make([]model.Client, 0, len(cases))
	for _, tc := range cases {
		c := tc.client
		c.InboundID = in.ID
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("create %s: %v", tc.client.Name, err)
		}
		created = append(created, c)
	}

	svc.runScheduledResets(now)

	for i, tc := range cases {
		stored, err := st.ClientByID(created[i].ID)
		if err != nil {
			t.Fatalf("%s: reload: %v", tc.name, err)
		}
		if tc.wantReset {
			if stored.UpBytes != 0 || stored.DownBytes != 0 {
				t.Fatalf("%s: counters = %d/%d, want 0/0", tc.name, stored.UpBytes, stored.DownBytes)
			}
		} else if stored.UpBytes != 1000 || stored.DownBytes != 500 {
			t.Fatalf("%s: counters = %d/%d, want them untouched", tc.name, stored.UpBytes, stored.DownBytes)
		}
		if stored.LastResetAt != tc.wantLast {
			t.Fatalf("%s: last_reset_at = %d, want %d", tc.name, stored.LastResetAt, tc.wantLast)
		}
	}
}

// TestRunScheduledResetsIsIdempotent makes sure two ticks in the same period do
// not reset twice (which would wipe traffic that arrived after the first tick).
func TestRunScheduledResetsIsIdempotent(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, loc)
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	svc.mu.Lock()
	svc.loc = loc
	svc.resetDay = 1
	svc.mu.Unlock()

	c := model.Client{
		InboundID: in.ID, Name: "monthly", Enabled: true,
		UpBytes: 10, ResetPolicy: model.ResetMonthly,
		LastResetAt: time.Date(2026, 2, 1, 0, 0, 0, 0, loc).Unix(),
		PSK:         "unused-for-reset", SubToken: "tok-monthly",
	}
	if err := st.CreateClient(&c); err != nil {
		t.Fatalf("create: %v", err)
	}
	svc.runScheduledResets(now)
	// Traffic arrives after the reset.
	if err := st.SaveUsage([]model.UsageSnapshot{{ClientID: c.ID, UpBytes: 7, DownBytes: 3, Day: "2026-03-15"}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	svc.runScheduledResets(now)

	stored, err := st.ClientByID(c.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.UpBytes != 7 || stored.DownBytes != 3 {
		t.Fatalf("second tick wiped new traffic: %d/%d", stored.UpBytes, stored.DownBytes)
	}
}

// TestDailyTotalsSinceFollowsTheRuntimeTimeZone pins the dashboard window to the
// effective time zone rather than the configuration file.
//
// UpdateSettings changes the runtime location and the accounting manager but not
// cfg, so the dashboard used to build its 30 day window from a value that could
// be a whole day stale, while the per-client usage endpoint already used the new
// one. The test inserts one day inside the window and one outside and checks
// that the change of zone moves the cut-off.
func TestDailyTotalsSinceFollowsTheRuntimeTimeZone(t *testing.T) {
	loc, err := time.LoadLocation("Pacific/Kiritimati") // UTC+14
	if err != nil {
		t.Skipf("zone unavailable: %v", err)
	}
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)

	view := svc.Settings()
	view.Timezone = loc.String()
	if err := svc.UpdateSettings(view); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if got := svc.Location().String(); got != loc.String() {
		t.Fatalf("Location() = %q, want %q", got, loc.String())
	}

	// A client is required for the rollup rows to be valid.
	c := model.Client{
		InboundID: in.ID, Name: "daily", Enabled: true, ResetPolicy: model.ResetNone,
		PSK: "unused-for-rollup", SubToken: "tok-daily",
	}
	if err := st.CreateClient(&c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	today := time.Now().In(loc)
	inside := today.AddDate(0, 0, -5).Format("2006-01-02")
	outside := today.AddDate(0, 0, -40).Format("2006-01-02")
	for _, day := range []string{inside, outside} {
		if err := st.SaveUsage([]model.UsageSnapshot{{
			ClientID: c.ID, UpBytes: 10, DownBytes: 5, Day: day, DayUpBytes: 10, DayDownBytes: 5,
		}}); err != nil {
			t.Fatalf("save usage %s: %v", day, err)
		}
	}

	days, err := svc.DailyTotalsSince(30)
	if err != nil {
		t.Fatalf("daily totals: %v", err)
	}
	var sawInside, sawOutside bool
	for _, d := range days {
		if d.Day == inside {
			sawInside = true
		}
		if d.Day == outside {
			sawOutside = true
		}
	}
	if !sawInside {
		t.Fatalf("day %s inside the window is missing: %+v", inside, days)
	}
	if sawOutside {
		t.Fatalf("day %s outside the window leaked in: %+v", outside, days)
	}
}
