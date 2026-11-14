package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	// Reopening must not fail.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
}

func TestAdminCRUD(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !admin.MustChangePassword || admin.ID == 0 {
		t.Fatalf("unexpected admin %+v", admin)
	}
	got, err := st.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.PasswordHash != "hash" {
		t.Fatalf("unexpected hash %q", got.PasswordHash)
	}
	if _, err := st.CreateAdmin("admin", "other", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := st.UpdateAdminPassword(admin.ID, "new", false); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = st.AdminByID(admin.ID)
	if got.PasswordHash != "new" || got.MustChangePassword {
		t.Fatalf("update did not apply: %+v", got)
	}
	if _, err := st.AdminByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestInboundAndClientLifecycle(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Remark: "ss", Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, ServerPSK: "abc", UDPEnabled: true, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("id not assigned")
	}
	dup := model.Inbound{Remark: "dup", Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	used, err := st.PortInUse("0.0.0.0", 8388, 0)
	if err != nil || !used {
		t.Fatalf("expected port in use, got %v %v", used, err)
	}

	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk", Enabled: true, SubToken: "tok-1", QuotaBytes: 100}
	if err := st.CreateClient(&client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	dupClient := model.Client{InboundID: in.ID, Name: "alice", PSK: "x", Enabled: true, SubToken: "tok-2"}
	if err := st.CreateClient(&dupClient); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected name conflict, got %v", err)
	}

	if err := st.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 30, DownBytes: 40,
		Day: "2026-01-02", DayUpBytes: 30, DayDownBytes: 40,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	got, _ := st.ClientByID(client.ID)
	if got.UpBytes != 30 || got.DownBytes != 40 {
		t.Fatalf("usage not persisted: %+v", got)
	}
	if got.UsedBytes() != 70 || got.RemainingBytes() != 30 {
		t.Fatalf("unexpected derived values: used=%d remaining=%d", got.UsedBytes(), got.RemainingBytes())
	}
	usage, err := st.DailyUsage(client.ID, "2026-01-01")
	if err != nil || len(usage) != 1 {
		t.Fatalf("daily usage: %v %v", usage, err)
	}
	// Upsert accumulates.
	if err := st.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 35, DownBytes: 40,
		Day: "2026-01-02", DayUpBytes: 5,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	usage, _ = st.DailyUsage(client.ID, "2026-01-01")
	if usage[0].UpBytes != 35 {
		t.Fatalf("daily upsert failed: %+v", usage[0])
	}

	if err := st.DeleteInbound(in.ID); err != nil {
		t.Fatalf("delete inbound: %v", err)
	}
	if _, err := st.ClientByID(client.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clients should cascade, got %v", err)
	}
}

func TestClientHelpers(t *testing.T) {
	now := int64(1_000_000)
	active := model.Client{Enabled: true, QuotaBytes: 100, DownBytes: 10}
	if active.StatusAt(now) != model.StatusActive {
		t.Fatalf("unexpected status %q", active.StatusAt(now))
	}
	disabled := model.Client{Enabled: false}
	if disabled.StatusAt(now) != model.StatusDisabled {
		t.Fatalf("unexpected status %q", disabled.StatusAt(now))
	}
	expired := model.Client{Enabled: true, ExpiresAt: now - 1}
	if expired.StatusAt(now) != model.StatusExpired {
		t.Fatalf("unexpected status %q", expired.StatusAt(now))
	}
	over := model.Client{Enabled: true, QuotaBytes: 10, UpBytes: 10}
	if over.StatusAt(now) != model.StatusQuotaExceeded {
		t.Fatalf("unexpected status %q", over.StatusAt(now))
	}
	unlimited := &model.Client{}
	if unlimited.RemainingBytes() != -1 {
		t.Fatal("unlimited quota must return -1")
	}
}

func TestSettings(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetSetting("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := st.SetSettings(map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.SetSetting("a", "3"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, err := st.AllSettings()
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all["a"] != "3" || all["b"] != "2" {
		t.Fatalf("unexpected settings %+v", all)
	}
}

func TestBackup(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateAdmin("admin", "hash", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(path); err != nil {
		t.Fatalf("backup: %v", err)
	}
	copyStore, err := Open(path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copyStore.Close()
	count, err := copyStore.AdminCount()
	if err != nil || count != 1 {
		t.Fatalf("backup content missing: count=%d err=%v", count, err)
	}
}

func TestClientsForReset(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	monthly := model.Client{InboundID: in.ID, Name: "m", PSK: "x", Enabled: true, SubToken: "t1",
		ResetPolicy: model.ResetMonthly, ResetDay: 1, LastResetAt: 100}
	if err := st.CreateClient(&monthly); err != nil {
		t.Fatalf("client: %v", err)
	}
	none := model.Client{InboundID: in.ID, Name: "n", PSK: "y", Enabled: true, SubToken: "t2"}
	if err := st.CreateClient(&none); err != nil {
		t.Fatalf("client: %v", err)
	}
	candidates, err := st.ClientsForReset(200)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != monthly.ID {
		t.Fatalf("unexpected candidates %+v", candidates)
	}
}

// TestQueryIndexes verifies the 002 migration created the indexes the panel
// and the reset worker rely on.
func TestQueryIndexes(t *testing.T) {
	st := newTestStore(t)
	want := map[string]bool{
		"idx_traffic_daily_day": false,
		"idx_clients_reset":     false,
	}
	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for name, found := range want {
		if !found {
			t.Fatalf("index %s missing", name)
		}
	}
}

// TestSaveUsageBatch covers the prepared-statement batch writer across several
// clients and days.
func TestSaveUsageBatch(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	a := model.Client{InboundID: in.ID, Name: "a", PSK: "x", Enabled: true, SubToken: "t1"}
	b := model.Client{InboundID: in.ID, Name: "b", PSK: "y", Enabled: true, SubToken: "t2"}
	if err := st.CreateClient(&a); err != nil {
		t.Fatalf("client a: %v", err)
	}
	if err := st.CreateClient(&b); err != nil {
		t.Fatalf("client b: %v", err)
	}

	batch := []model.UsageSnapshot{
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-05-01", DayUpBytes: 10, DayDownBytes: 20},
		{ClientID: b.ID, UpBytes: 1, DownBytes: 2, Day: "2026-05-01", DayUpBytes: 1, DayDownBytes: 2},
		{ClientID: a.ID, UpBytes: 15, DownBytes: 30, Day: "2026-05-02", DayUpBytes: 5, DayDownBytes: 10},
	}
	if err := st.SaveUsage(batch); err != nil {
		t.Fatalf("save batch: %v", err)
	}

	gotA, err := st.ClientByID(a.ID)
	if err != nil {
		t.Fatalf("client a: %v", err)
	}
	gotB, err := st.ClientByID(b.ID)
	if err != nil {
		t.Fatalf("client b: %v", err)
	}
	if gotA.UpBytes != 15 || gotA.DownBytes != 30 {
		t.Fatalf("client a totals not applied: %+v", gotA)
	}
	if gotB.UpBytes != 1 || gotB.DownBytes != 2 {
		t.Fatalf("client b totals not applied: %+v", gotB)
	}

	usageA, err := st.DailyUsage(a.ID, "2026-05-01")
	if err != nil {
		t.Fatalf("daily usage: %v", err)
	}
	if len(usageA) != 2 || usageA[0].UpBytes != 10 || usageA[1].UpBytes != 5 {
		t.Fatalf("unexpected daily rows: %+v", usageA)
	}

	totals, err := st.DailyTotals("2026-05-01")
	if err != nil {
		t.Fatalf("daily totals: %v", err)
	}
	if len(totals) != 2 || totals[0].UpBytes != 11 {
		t.Fatalf("unexpected daily totals: %+v", totals)
	}
}

// TestInboundAggregates verifies the grouped SQL aggregation the inbound list
// renders: counts, enabled counts and traffic totals per inbound.
func TestInboundAggregates(t *testing.T) {
	st := newTestStore(t)
	mkInbound := func(port int) model.Inbound {
		in := model.Inbound{Listen: "127.0.0.1", Port: port, Method: model.Method2022AES128GCM, Enabled: true}
		if err := st.CreateInbound(&in); err != nil {
			t.Fatalf("inbound: %v", err)
		}
		return in
	}
	a := mkInbound(9001)
	b := mkInbound(9002)
	empty := mkInbound(9003)

	seed := []struct {
		inbound model.Inbound
		name    string
		enabled bool
		up      int64
		down    int64
	}{
		{a, "a1", true, 100, 200},
		{a, "a2", false, 1, 2},
		{b, "b1", true, 3, 4},
		{b, "b2", true, 5, 6},
	}
	for i, s := range seed {
		c := model.Client{InboundID: s.inbound.ID, Name: s.name, PSK: "x", Enabled: s.enabled,
			SubToken: "tok-" + s.name, UpBytes: s.up, DownBytes: s.down}
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	all, err := st.InboundAggregates(0)
	if err != nil {
		t.Fatalf("aggregates: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected aggregates for 2 inbounds, got %d: %+v", len(all), all)
	}
	if agg := all[a.ID]; agg.ClientCount != 2 || agg.EnabledClientCount != 1 ||
		agg.UpBytes != 101 || agg.DownBytes != 202 {
		t.Fatalf("unexpected aggregate for inbound a: %+v", agg)
	}
	if agg := all[b.ID]; agg.ClientCount != 2 || agg.EnabledClientCount != 2 ||
		agg.UpBytes != 8 || agg.DownBytes != 10 {
		t.Fatalf("unexpected aggregate for inbound b: %+v", agg)
	}
	if _, ok := all[empty.ID]; ok {
		t.Fatalf("inbound without clients must not appear: %+v", all[empty.ID])
	}

	one, err := st.InboundAggregates(b.ID)
	if err != nil {
		t.Fatalf("single aggregate: %v", err)
	}
	if len(one) != 1 || one[b.ID].ClientCount != 2 {
		t.Fatalf("filtered aggregate returned %+v", one)
	}
}

// TestListClientRows verifies the lean list projection matches the full rows
// field by field, for both the filtered and the unfiltered query.
// TestListClientPage covers paging, inbound filtering, the joined inbound
// fields, LIKE escaping and the agreement between `total` and `items`.
func TestListClientPage(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Remark: "香港", Listen: "127.0.0.1", Port: 9100,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	other := model.Inbound{Remark: "東京", Listen: "127.0.0.1", Port: 9101,
		Method: model.MethodAES128GCM, Enabled: true}
	if err := st.CreateInbound(&other); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	full := []model.Client{
		{InboundID: in.ID, Name: "b", PSK: "psk-b", Enabled: false, QuotaBytes: 100, UpBytes: 10,
			DownBytes: 20, ExpiresAt: 1700000000, IPLimit: 3, ResetPolicy: model.ResetMonthly,
			ResetDay: 5, Note: "note-b", SubToken: "t-b"},
		{InboundID: in.ID, Name: "a", PSK: "psk-a", Enabled: true, UpBytes: 1, DownBytes: 2, SubToken: "t-a"},
		{InboundID: other.ID, Name: "c", PSK: "psk-c", Enabled: true, UpBytes: 3, DownBytes: 4, SubToken: "t-c"},
		{InboundID: in.ID, Name: "pct-100%", PSK: "psk-d", Enabled: true, SubToken: "t-d"},
		{InboundID: in.ID, Name: "under_score", PSK: "psk-e", Enabled: true, SubToken: "t-e"},
	}
	for i := range full {
		if err := st.CreateClient(&full[i]); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	// The projection carries every field of the full row it renders.
	page, total, err := st.ListClientPage(ClientPageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != int64(len(full)) || len(page) != len(full) {
		t.Fatalf("total=%d len(page)=%d, want %d", total, len(page), len(full))
	}
	for i := range full {
		w, g := full[i], page[i]
		if w.ID != g.ID || w.InboundID != g.InboundID || w.Name != g.Name || w.Enabled != g.Enabled ||
			w.QuotaBytes != g.QuotaBytes || w.UpBytes != g.UpBytes || w.DownBytes != g.DownBytes ||
			w.ExpiresAt != g.ExpiresAt || w.IPLimit != g.IPLimit || w.ResetPolicy != g.ResetPolicy ||
			w.ResetDay != g.ResetDay || w.Note != g.Note {
			t.Fatalf("row %d mismatch:\nfull %+v\npage %+v", i, w, g)
		}
	}
	// The join supplies the inbound fields, so the list never has to read the
	// inbound rows (which carry the server PSK).
	if page[0].InboundRemark != "香港" || page[0].Method != model.Method2022AES128GCM {
		t.Fatalf("joined inbound fields missing: %+v", page[0])
	}
	if page[2].InboundRemark != "東京" || page[2].Method != model.MethodAES128GCM {
		t.Fatalf("joined inbound fields missing: %+v", page[2])
	}

	// Paging walks the same ordered sequence without skipping or repeating.
	seen := make(map[int64]bool, len(full))
	for offset := 0; offset < len(full); offset += 2 {
		part, partTotal, err := st.ListClientPage(ClientPageFilter{Limit: 2, Offset: offset})
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		if partTotal != int64(len(full)) {
			t.Fatalf("total at offset %d = %d", offset, partTotal)
		}
		for _, row := range part {
			if seen[row.ID] {
				t.Fatalf("row %d appeared twice", row.ID)
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != len(full) {
		t.Fatalf("paging saw %d of %d rows", len(seen), len(full))
	}

	// An offset past the end is an empty page with the real total, not an error.
	past, pastTotal, err := st.ListClientPage(ClientPageFilter{Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatalf("past end: %v", err)
	}
	if len(past) != 0 || pastTotal != int64(len(full)) {
		t.Fatalf("past end: %d rows, total %d", len(past), pastTotal)
	}

	// Inbound filter.
	filtered, filteredTotal, err := st.ListClientPage(ClientPageFilter{InboundID: in.ID, Limit: 10})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if filteredTotal != 4 || len(filtered) != 4 {
		t.Fatalf("expected 4 rows for the inbound, got %d (total %d)", len(filtered), filteredTotal)
	}
	for _, row := range filtered {
		if row.InboundID != in.ID {
			t.Fatalf("filter leaked row from another inbound: %+v", row)
		}
	}

	// The free-text filter matches name, note, inbound remark and method, and
	// treats the LIKE wildcards in the input literally.
	queries := map[string]int{
		"香港":           4, // inbound remark
		"東京":           1, // inbound remark
		"note-b":       1, // client note
		"blake3":       4, // method
		"100%":         1, // % is literal, not "any run of characters"
		"under_score":  1, // _ is literal, not "any single character"
		"underXscore":  0,
		"no-such-user": 0,
	}
	for query, want := range queries {
		rows, gotTotal, err := st.ListClientPage(ClientPageFilter{Query: query, Limit: 10})
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if int(gotTotal) != want || len(rows) != want {
			t.Fatalf("query %q: %d rows (total %d), want %d", query, len(rows), gotTotal, want)
		}
	}

	// An unknown status and an over-large limit are normalised by the store,
	// which is what keeps a non-panel caller from widening the page.
	if _, _, err := st.ListClientPage(ClientPageFilter{Status: "bogus"}); err == nil {
		t.Fatalf("expected an error for an unknown status")
	}
	clamped, _, err := st.ListClientPage(ClientPageFilter{Limit: ClientPageMaxLimit + 1})
	if err != nil {
		t.Fatalf("clamped: %v", err)
	}
	if len(clamped) != len(full) {
		t.Fatalf("clamped page has %d rows", len(clamped))
	}
}

// TestClientStatusClauseMatchesStatusAt pins the SQL status filter to the Go
// rule it mirrors. If the two ever drift, the panel would render a row with one
// status while claiming to filter by another.
func TestClientStatusClauseMatchesStatusAt(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Remark: "status", Listen: "127.0.0.1", Port: 9200,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	now := int64(1_800_000_000)
	// Every branch of model.Client.StatusAt, including the edges: expiry exactly
	// now, quota exactly reached, and the zero values that disable each rule.
	fixtures := []model.Client{
		{Name: "disabled", Enabled: false},
		{Name: "disabled-and-expired-and-over-quota", Enabled: false, ExpiresAt: now - 1, QuotaBytes: 1, UpBytes: 5},
		{Name: "expired", Enabled: true, ExpiresAt: now - 1},
		{Name: "expired-at-now", Enabled: true, ExpiresAt: now},
		{Name: "expires-later", Enabled: true, ExpiresAt: now + 1},
		{Name: "never-expires", Enabled: true},
		{Name: "quota-exactly-reached", Enabled: true, QuotaBytes: 10, UpBytes: 4, DownBytes: 6},
		{Name: "quota-below", Enabled: true, QuotaBytes: 10, UpBytes: 4, DownBytes: 5},
		{Name: "unlimited", Enabled: true, QuotaBytes: 0, UpBytes: 1 << 40},
		{Name: "expired-wins-over-quota", Enabled: true, ExpiresAt: now - 1, QuotaBytes: 1, UpBytes: 9},
		{Name: "quota-wins-over-active", Enabled: true, ExpiresAt: now + 100, QuotaBytes: 1, UpBytes: 1},
	}
	for i := range fixtures {
		fixtures[i].InboundID = in.ID
		fixtures[i].PSK = "psk"
		fixtures[i].SubToken = "tok-" + itoaStore(i)
		if err := st.CreateClient(&fixtures[i]); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	for _, status := range []string{model.StatusActive, model.StatusDisabled,
		model.StatusExpired, model.StatusQuotaExceeded} {
		want := make(map[int64]string, len(fixtures))
		for i := range fixtures {
			if got := fixtures[i].StatusAt(now); got == status {
				want[fixtures[i].ID] = fixtures[i].Name
			}
		}
		if len(want) == 0 {
			t.Fatalf("fixtures do not cover %s", status)
		}
		rows, total, err := st.ListClientPage(ClientPageFilter{Status: status, Now: now,
			Limit: ClientPageMaxLimit})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if int(total) != len(want) {
			t.Fatalf("%s: total %d, want %d (%v)", status, total, len(want), want)
		}
		got := make(map[int64]string, len(rows))
		for _, row := range rows {
			got[row.ID] = row.Name
		}
		if len(got) != len(want) {
			t.Fatalf("%s: SQL returned %d rows, Go projected %d\nSQL %v\nGo  %v",
				status, len(got), len(want), got, want)
		}
		for id, name := range want {
			if got[id] != name {
				t.Fatalf("%s: row %d missing from SQL result\nSQL %v\nGo  %v", status, id, got, want)
			}
		}
	}

	// "all" and "" mean no filter at all.
	for _, status := range []string{"", "all"} {
		_, total, err := st.ListClientPage(ClientPageFilter{Status: status, Now: now,
			Limit: ClientPageMaxLimit})
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		if int(total) != len(fixtures) {
			t.Fatalf("status %q: total %d, want %d", status, total, len(fixtures))
		}
	}
}

// TestClientPageRowModelRoundTrip verifies Model() carries every field of the
// lean projection, so the service can reuse the domain status/quota rules.
func TestClientPageRowModelRoundTrip(t *testing.T) {
	row := ClientPageRow{
		ID: 7, InboundID: 3, Name: "alice", Enabled: true, QuotaBytes: 1024,
		UpBytes: 100, DownBytes: 200, ExpiresAt: 1700000000, IPLimit: 2,
		ResetPolicy: model.ResetMonthly, ResetDay: 9, Note: "laptop",
		InboundRemark: "hk", Method: model.Method2022AES128GCM,
	}
	c := row.Model()
	if c.ID != row.ID || c.InboundID != row.InboundID || c.Name != row.Name ||
		c.Enabled != row.Enabled || c.QuotaBytes != row.QuotaBytes ||
		c.UpBytes != row.UpBytes || c.DownBytes != row.DownBytes ||
		c.ExpiresAt != row.ExpiresAt || c.IPLimit != row.IPLimit ||
		c.ResetPolicy != row.ResetPolicy || c.ResetDay != row.ResetDay || c.Note != row.Note {
		t.Fatalf("round trip lost a field:\nrow %+v\nmodel %+v", row, c)
	}
	// Credentials are intentionally absent from the projection.
	if c.PSK != "" || c.SubToken != "" {
		t.Fatalf("projection must not invent credentials: %+v", c)
	}
	if c.UsedBytes() != 300 || c.RemainingBytes() != 724 {
		t.Fatalf("unexpected derived values: used=%d remaining=%d", c.UsedBytes(), c.RemainingBytes())
	}
}

// TestListClientIdentities checks the narrow projection an import uses to
// reject duplicates and to count enabled users.
func TestListClientIdentities(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Listen: "127.0.0.1", Port: 9300, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	other := model.Inbound{Listen: "127.0.0.1", Port: 9301, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&other); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	for i, c := range []model.Client{
		{InboundID: in.ID, Name: "a", Enabled: true},
		{InboundID: in.ID, Name: "b", Enabled: false},
		{InboundID: other.ID, Name: "c", Enabled: true},
	} {
		c.PSK = "psk"
		c.SubToken = "tok-" + itoaStore(i)
		if err := st.CreateClient(&c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	all, err := st.ListClientIdentities(0)
	if err != nil {
		t.Fatalf("identities: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 identities, got %d", len(all))
	}
	if all[0].Name != "a" || !all[0].Enabled || all[1].Name != "b" || all[1].Enabled {
		t.Fatalf("unexpected identities: %+v", all)
	}
	filtered, err := st.ListClientIdentities(in.ID)
	if err != nil {
		t.Fatalf("filtered identities: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2 identities for the inbound, got %d", len(filtered))
	}
	for _, identity := range filtered {
		if identity.Name == "c" {
			t.Fatalf("filter leaked a client from another inbound")
		}
	}
}

// TestCreateClientsBatch covers the chunked bulk insert: ids come back in input
// order across chunk boundaries, the defaults match CreateClient, and a
// rejected batch is atomic so the caller can retry row by row.
func TestCreateClientsBatch(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Listen: "127.0.0.1", Port: 9400, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}

	// More than two chunks, so the ordering guarantee is exercised across
	// statements rather than inside a single one.
	const rows = createClientsChunk*2 + 7
	batch := make([]*model.Client, 0, rows)
	for i := 0; i < rows; i++ {
		batch = append(batch, &model.Client{
			InboundID: in.ID,
			Name:      "bulk-" + itoaStore(i),
			PSK:       "psk",
			Enabled:   i%2 == 0,
			SubToken:  "bulk-tok-" + itoaStore(i),
		})
	}
	if err := st.CreateClients(batch); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	for i, c := range batch {
		if c.ID == 0 {
			t.Fatalf("row %d has no id after the batch insert", i)
		}
		if i > 0 && batch[i-1].ID >= c.ID {
			t.Fatalf("ids are not in input order: %d then %d", batch[i-1].ID, c.ID)
		}
	}
	// The ids must be the ones actually stored, in the same order.
	stored, err := st.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != rows {
		t.Fatalf("stored %d rows, want %d", len(stored), rows)
	}
	for i := range stored {
		if stored[i].ID != batch[i].ID || stored[i].Name != batch[i].Name ||
			stored[i].Enabled != batch[i].Enabled {
			t.Fatalf("row %d mismatch: stored %+v, batch %+v", i, stored[i], batch[i])
		}
		// Defaults must match the single-row insert.
		if stored[i].ResetPolicy != model.ResetNone {
			t.Fatalf("row %d reset policy %q", i, stored[i].ResetPolicy)
		}
		if stored[i].CreatedAt == 0 || stored[i].UpdatedAt == 0 {
			t.Fatalf("row %d lost its timestamps: %+v", i, stored[i])
		}
	}

	// An empty batch is a no-op, not an error.
	if err := st.CreateClients(nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}

	// A duplicate name is rejected and the whole batch rolls back, which is what
	// the import's per-row fallback relies on.
	duplicate := []*model.Client{
		{InboundID: in.ID, Name: "bulk-0", PSK: "psk", SubToken: "dup-1"},
		{InboundID: in.ID, Name: "bulk-0", PSK: "psk", SubToken: "dup-2"},
	}
	if err := st.CreateClients(duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for a duplicate, got %v", err)
	}
	afterConflict, err := st.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(afterConflict) != rows {
		t.Fatalf("a rejected batch left %d extra rows", len(afterConflict)-rows)
	}
	if duplicate[0].ID != 0 || duplicate[1].ID != 0 {
		t.Fatalf("a rejected batch must not report ids: %+v", duplicate)
	}
}

// TestInboundDomainRoundTrip verifies the domain columns survive create, update
// and clearing.
func TestInboundDomainRoundTrip(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Remark: "hk", Listen: "127.0.0.1", Port: 9400,
		Method: model.Method2022AES128GCM, Enabled: true,
		Domain: "hk.example.com", DomainPort: 8443}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.InboundByID(in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Domain != "hk.example.com" || got.DomainPort != 8443 {
		t.Fatalf("domain not persisted: %+v", got)
	}

	got.Domain = "hk2.example.com"
	got.DomainPort = 0
	if err := st.UpdateInbound(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reloaded, _ := st.InboundByID(in.ID)
	if reloaded.Domain != "hk2.example.com" || reloaded.DomainPort != 0 {
		t.Fatalf("domain not updated: %+v", reloaded)
	}

	// Clearing the domain returns the inbound to the global setting.
	reloaded.Domain = ""
	if err := st.UpdateInbound(reloaded); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared, _ := st.InboundByID(in.ID)
	if cleared.Domain != "" || cleared.DomainPort != 0 {
		t.Fatalf("domain not cleared: %+v", cleared)
	}

	// ListInbounds must carry the columns too.
	list, err := st.ListInbounds()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Domain != "" {
		t.Fatalf("list mismatch: %+v", list)
	}
}
