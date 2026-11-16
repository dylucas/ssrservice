package store

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// seedAccessFixture creates one inbound with two clients so the joins in the
// listing have names to resolve.
func seedAccessFixture(t *testing.T, st *Store) (model.Inbound, model.Client, model.Client) {
	t.Helper()
	in := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28001,
		Method: model.Method2022AES128GCM, Enabled: true, UDPEnabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	first := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk-a", Enabled: true, SubToken: "tok-a"}
	second := model.Client{InboundID: in.ID, Name: "bob", PSK: "psk-b", Enabled: true, SubToken: "tok-b"}
	if err := st.CreateClient(&first); err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := st.CreateClient(&second); err != nil {
		t.Fatalf("client: %v", err)
	}
	return in, first, second
}

func accessRecord(clientID, inboundID int64, host string, port int, outcome, reason, day string) model.AccessRecord {
	return model.AccessRecord{
		ClientID: clientID, InboundID: inboundID, Network: "tcp",
		Host: host, Port: port, IP: "93.184.216.34", Outcome: outcome, Reason: reason,
		StartedAt: time.Now().Unix(), DurationMS: 12, Day: day,
	}
}

func TestSaveAccessStoresEventsAndRollup(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	day := "2026-11-15"

	records := []model.AccessRecord{
		accessRecord(alice.ID, in.ID, "example.com", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "example.com", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "blocked.example", 80, model.AccessBlocked, model.AccessReasonPolicy, day),
	}
	records[0].UpBytes, records[0].DownBytes = 100, 200
	records[1].UpBytes, records[1].DownBytes = 10, 20
	records[2].Day = day
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	events, total, err := st.ListAccessEvents(AccessEventFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(events) != 3 {
		t.Fatalf("expected 3 events, got %d/%d", len(events), total)
	}
	if events[0].ClientName != "alice" || events[0].InboundRemark != "main" {
		t.Fatalf("names not joined: %+v", events[0])
	}

	summary, err := st.AccessSummary(AccessSummaryFilter{SinceDay: "2026-11-01"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalHits != 3 || summary.AllowedHits != 2 || summary.BlockedHits != 1 {
		t.Fatalf("unexpected summary %+v", summary)
	}
	if summary.UniqueHosts != 2 {
		t.Fatalf("unique hosts = %d, want 2", summary.UniqueHosts)
	}
	if summary.UpBytes != 110 || summary.DownBytes != 220 {
		t.Fatalf("unexpected bytes %d/%d", summary.UpBytes, summary.DownBytes)
	}
	if len(summary.Daily) != 1 || summary.Daily[0].Hits != 3 {
		t.Fatalf("unexpected daily %+v", summary.Daily)
	}
	if len(summary.TopHosts) != 2 || summary.TopHosts[0].Host != "example.com" ||
		summary.TopHosts[0].Hits != 2 {
		t.Fatalf("unexpected top hosts %+v", summary.TopHosts)
	}
	if len(summary.TopClients) != 1 || summary.TopClients[0].ClientID != alice.ID ||
		summary.TopClients[0].Name != "alice" {
		t.Fatalf("unexpected top clients %+v", summary.TopClients)
	}
}

func TestSaveAccessAccumulatesAcrossBatches(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	day := "2026-11-15"

	first := accessRecord(alice.ID, in.ID, "example.com", 443, model.AccessAllowed, "", day)
	first.UpBytes, first.DownBytes = 5, 7
	if err := st.SaveAccess([]model.AccessRecord{first}); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	second := accessRecord(alice.ID, in.ID, "example.com", 443, model.AccessAllowed, "", day)
	second.UpBytes, second.DownBytes = 3, 4
	if err := st.SaveAccess([]model.AccessRecord{second}); err != nil {
		t.Fatalf("second batch: %v", err)
	}

	summary, err := st.AccessSummary(AccessSummaryFilter{SinceDay: day})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalHits != 2 || summary.UpBytes != 8 || summary.DownBytes != 11 {
		t.Fatalf("rollup did not accumulate: %+v", summary)
	}
	// The raw records stay individual rows; only the rollup folds them.
	if count, err := st.CountAccessEvents(); err != nil || count != 2 {
		t.Fatalf("events = %d, %v", count, err)
	}
}

func TestListAccessEventsFilters(t *testing.T) {
	st := newTestStore(t)
	in, alice, bob := seedAccessFixture(t, st)
	now := time.Now().Unix()

	records := []model.AccessRecord{
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "example.com", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now - 100, Day: "2026-11-15"},
		{ClientID: bob.ID, InboundID: in.ID, Network: "udp", Host: "dns.example", Port: 53,
			Outcome: model.AccessBlocked, Reason: model.AccessReasonPolicy, StartedAt: now - 50, Day: "2026-11-15"},
		{ClientID: bob.ID, InboundID: in.ID, Network: "tcp", Host: "example.org", Port: 443,
			Outcome: model.AccessFailed, Reason: model.AccessReasonDial, StartedAt: now, Day: "2026-11-15"},
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	cases := []struct {
		name   string
		filter AccessEventFilter
		want   int
	}{
		{"all", AccessEventFilter{}, 3},
		{"client", AccessEventFilter{ClientID: bob.ID}, 2},
		{"inbound", AccessEventFilter{InboundID: in.ID}, 3},
		{"outcome", AccessEventFilter{Outcome: model.AccessBlocked}, 1},
		{"network", AccessEventFilter{Network: "udp"}, 1},
		{"host substring", AccessEventFilter{Query: "example."}, 2},
		{"time window", AccessEventFilter{Since: now - 60, Until: now + 1}, 2},
		{"no match", AccessEventFilter{Query: "nowhere.invalid"}, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			events, total, err := st.ListAccessEvents(testCase.filter)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(events) != testCase.want || total != int64(testCase.want) {
				t.Fatalf("got %d/%d, want %d", len(events), total, testCase.want)
			}
		})
	}

	// The listing is newest first, so paging is stable across requests.
	page, _, err := st.ListAccessEvents(AccessEventFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page) != 1 || page[0].Host != "dns.example" {
		t.Fatalf("unexpected page %+v", page)
	}
}

func TestListAccessEventsEscapesLikeWildcards(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	records := []model.AccessRecord{
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "100%off.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: time.Now().Unix(), Day: "2026-11-15"},
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "a_b.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: time.Now().Unix(), Day: "2026-11-15"},
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "axb.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: time.Now().Unix(), Day: "2026-11-15"},
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, total, err := st.ListAccessEvents(AccessEventFilter{Query: "100%"}); err != nil || total != 1 {
		t.Fatalf("percent search matched %d rows (err %v)", total, err)
	}
	if _, total, err := st.ListAccessEvents(AccessEventFilter{Query: "a_b"}); err != nil || total != 1 {
		t.Fatalf("underscore search matched %d rows (err %v)", total, err)
	}
}

func TestAccessSummaryWindowAndFilters(t *testing.T) {
	st := newTestStore(t)
	in, alice, bob := seedAccessFixture(t, st)
	records := []model.AccessRecord{
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "a.example", Port: 443,
			Outcome: model.AccessAllowed, UpBytes: 10, Day: "2026-11-10"},
		{ClientID: bob.ID, InboundID: in.ID, Network: "tcp", Host: "b.example", Port: 443,
			Outcome: model.AccessAllowed, UpBytes: 20, Day: "2026-11-14"},
		{ClientID: bob.ID, InboundID: in.ID, Network: "tcp", Host: "c.example", Port: 443,
			Outcome: model.AccessBlocked, Reason: model.AccessReasonPolicy, Day: "2026-11-15"},
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The window excludes the oldest day.
	windowed, err := st.AccessSummary(AccessSummaryFilter{SinceDay: "2026-11-14"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if windowed.TotalHits != 2 || windowed.UpBytes != 20 {
		t.Fatalf("window not applied: %+v", windowed)
	}
	if len(windowed.Daily) != 2 || windowed.Daily[0].Day != "2026-11-14" {
		t.Fatalf("unexpected daily %+v", windowed.Daily)
	}

	// Filters compose with the window.
	filtered, err := st.AccessSummary(AccessSummaryFilter{SinceDay: "2026-11-01", ClientID: alice.ID})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if filtered.TotalHits != 1 || filtered.UniqueHosts != 1 {
		t.Fatalf("client filter not applied: %+v", filtered)
	}

	// An empty window is an empty result, not an error.
	empty, err := st.AccessSummary(AccessSummaryFilter{SinceDay: "2027-01-01"})
	if err != nil {
		t.Fatalf("empty summary: %v", err)
	}
	if empty.TotalHits != 0 || empty.Daily == nil || empty.TopHosts == nil || empty.TopClients == nil {
		t.Fatalf("empty summary should carry empty slices: %+v", empty)
	}
}

func TestAccessSummaryTopLimit(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	for i := 0; i < 20; i++ {
		record := accessRecord(alice.ID, in.ID, hostOfIndex(i), 443, model.AccessAllowed, "", "2026-11-15")
		record.UpBytes = int64(20 - i)
		if err := st.SaveAccess([]model.AccessRecord{record}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	summary, err := st.AccessSummary(AccessSummaryFilter{SinceDay: "2026-11-01", TopLimit: 5})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(summary.TopHosts) != 5 {
		t.Fatalf("top hosts = %d, want 5", len(summary.TopHosts))
	}
	// Default limit is 10, clamped to the maximum of 50.
	summary, err = st.AccessSummary(AccessSummaryFilter{SinceDay: "2026-11-01", TopLimit: 1000})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if len(summary.TopHosts) != 20 {
		t.Fatalf("top hosts = %d, want all 20", len(summary.TopHosts))
	}
}

func hostOfIndex(i int) string {
	return string(rune('a'+i%26)) + ".example"
}

func TestPruneAccess(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	now := time.Now().Unix()

	records := []model.AccessRecord{
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "old.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now - 8*24*3600, Day: "2026-11-01"},
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "fresh.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now - 3600, Day: "2026-11-15"},
		{ClientID: alice.ID, InboundID: in.ID, Network: "tcp", Host: "archived.example", Port: 443,
			Outcome: model.AccessAllowed, StartedAt: now - 100*24*3600, Day: "2026-09-01"},
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	events, daily, err := st.PruneAccess(now-7*24*3600, "2026-09-15")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if events != 2 {
		t.Fatalf("pruned %d events, want 2", events)
	}
	if daily != 1 {
		t.Fatalf("pruned %d rollup rows, want 1", daily)
	}
	if count, err := st.CountAccessEvents(); err != nil || count != 1 {
		t.Fatalf("events left = %d (%v)", count, err)
	}
	if count, err := st.CountAccessDaily(); err != nil || count != 2 {
		t.Fatalf("rollup rows left = %d (%v)", count, err)
	}
}

func TestSaveAccessSkipsEmptyBatch(t *testing.T) {
	st := newTestStore(t)
	if err := st.SaveAccess(nil); err != nil {
		t.Fatalf("save nil: %v", err)
	}
	if count, err := st.CountAccessEvents(); err != nil || count != 0 {
		t.Fatalf("events = %d (%v)", count, err)
	}
}

func TestListAccessEventsWithoutNames(t *testing.T) {
	st := newTestStore(t)
	// A record whose client was deleted keeps its id but joins to no name; the
	// panel renders the fallback instead of dropping the row.
	record := model.AccessRecord{ClientID: 4242, InboundID: 4243, Network: "tcp",
		Host: "gone.example", Port: 443, Outcome: model.AccessAllowed,
		StartedAt: time.Now().Unix(), Day: "2026-11-15"}
	if err := st.SaveAccess([]model.AccessRecord{record}); err != nil {
		t.Fatalf("save: %v", err)
	}
	events, total, err := st.ListAccessEvents(AccessEventFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || events[0].ClientName != "" || events[0].InboundRemark != "" {
		t.Fatalf("unexpected row %+v", events)
	}
}

// TestListAccessEventsTotalCap guards the bounded count: rows past the offset
// cap cannot be paged to, so the count stops there instead of walking the whole
// window.
func TestListAccessEventsTotalCap(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	now := time.Now().Unix()
	batch := make([]model.AccessRecord, 0, 40)
	for i := 0; i < 40; i++ {
		record := accessRecord(alice.ID, in.ID, "example.com", 443, model.AccessAllowed, "", "")
		record.StartedAt = now - int64(i)
		batch = append(batch, record)
	}
	if err := st.SaveAccess(batch); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A limit below the row count reports the exact number.
	if _, total, err := st.ListAccessEvents(AccessEventFilter{Limit: 5}); err != nil || total != 40 {
		t.Fatalf("total = %d (%v), want 40", total, err)
	}
	// The count is capped at the offset cap plus one; the page still works.
	_, total, err := st.ListAccessEvents(AccessEventFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total > accessCountCap {
		t.Fatalf("total = %d, want at most %d", total, accessCountCap)
	}
}

// TestAccessEventFiltersUseInbound makes sure the inbound filter narrows rows
// and not just the count: the listing and the summary must agree on it.
func TestAccessEventFiltersUseInbound(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	day := "2026-11-15"
	records := []model.AccessRecord{
		accessRecord(alice.ID, in.ID, "first.example", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID+1, "second.example", 443, model.AccessAllowed, "", day),
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}
	events, total, err := st.ListAccessEvents(AccessEventFilter{InboundID: in.ID + 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(events) != 1 || events[0].Host != "second.example" {
		t.Fatalf("inbound filter returned %d/%d rows: %+v", total, len(events), events)
	}
}

// TestPruneAccessDrainsLargeBacklog exercises the bounded delete path: one call
// removes at most one batch, so a backlog is drained over several calls. Every
// call reports what it removed, and the loop ends with nothing left.
func TestPruneAccessDrainsLargeBacklog(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	const rows = accessPruneBatch*2 + 37
	records := make([]model.AccessRecord, 0, rows)
	for i := 0; i < rows; i++ {
		// Started two weeks ago, day far behind the aggregate cutoff.
		record := accessRecord(alice.ID, in.ID, "old.example", 443, model.AccessAllowed, "", "2020-01-01")
		record.StartedAt = time.Now().Unix() - 14*24*3600
		records = append(records, record)
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	var events, daily int64
	calls := 0
	for call := 0; call < 200; call++ {
		var e, d int64
		var err error
		e, d, err = st.PruneAccess(cutoff, "2021-01-01")
		if err != nil {
			t.Fatalf("prune call %d: %v", call, err)
		}
		events += e
		daily += d
		calls++
		count, err := st.CountAccessEvents()
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if count == 0 {
			break
		}
	}
	if events != rows {
		t.Fatalf("pruned %d events over the calls, want %d", events, rows)
	}
	// A call may chain batches while it stays inside its time budget, so the
	// number of calls is not the invariant; the per-statement bound is, and
	// TestPruneAccessBatchesAreBoundedByRows covers it directly.
	_ = calls
	if daily != 1 {
		t.Fatalf("pruned %d rollup rows, want 1", daily)
	}
	if count, err := st.CountAccessEvents(); err != nil || count != 0 {
		t.Fatalf("events left = %d (%v)", count, err)
	}
	if count, err := st.CountAccessDaily(); err != nil || count != 0 {
		t.Fatalf("rollup rows left = %d (%v)", count, err)
	}
}

// TestPruneAccessDeletesRollupInBoundedBatches covers the WITHOUT ROWID batching:
// the rollup is pruned by primary-key range so one transaction never carries a
// whole day of rows, and the reported count still covers everything removed.
func TestPruneAccessDeletesRollupInBoundedBatches(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	const perDay = 3
	days := []string{"2026-01-01", "2026-01-02", "2026-01-03"}
	records := make([]model.AccessRecord, 0, len(days)*perDay)
	for _, day := range days {
		for i := 0; i < perDay; i++ {
			record := accessRecord(alice.ID, in.ID, "host-"+day+"-"+string(rune('a'+i))+".example",
				443, model.AccessAllowed, "", day)
			record.StartedAt = time.Now().Add(-120 * 24 * time.Hour).Unix()
			records = append(records, record)
		}
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A fresh day inside both windows stays.
	if err := st.SaveAccess([]model.AccessRecord{
		accessRecord(alice.ID, in.ID, "keep.example", 443, model.AccessAllowed, "", "2026-11-15"),
	}); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	_, daily, err := st.PruneAccess(time.Now().Add(-7*24*time.Hour).Unix(), "2026-06-01")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if daily != int64(len(days)*perDay) {
		t.Fatalf("pruned %d rollup rows, want %d", daily, len(days)*perDay)
	}
	left, err := st.CountAccessDaily()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("%d rollup rows left, want the fresh day only", left)
	}
}

// TestFoldAccessHostsCapsAndConservesTotals checks the cardinality cap: a key
// with more destinations than the cap keeps the most visited ones, folds the
// rest into the shared overflow row, and loses no hits or bytes doing it.
func TestFoldAccessHostsCapsAndConservesTotals(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	day := "2026-11-15"
	const hosts = 40
	const cap = 10
	records := make([]model.AccessRecord, 0, hosts)
	for i := 0; i < hosts; i++ {
		record := accessRecord(alice.ID, in.ID, "h"+strconv.Itoa(i)+".example", 443,
			model.AccessAllowed, "", day)
		record.UpBytes = int64(i + 1)
		record.DownBytes = int64((i + 1) * 2)
		records = append(records, record)
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	before, err := st.AccessSummary(AccessSummaryFilter{SinceDay: day, TopLimit: 50})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	folded, err := st.FoldAccessHosts(day, cap, 0)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if folded != int64(hosts-cap) {
		t.Fatalf("folded %d rows, want %d", folded, hosts-cap)
	}

	rows, err := st.CountAccessDaily()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != cap+1 {
		t.Fatalf("%d rollup rows after folding, want %d kept plus the overflow row", rows, cap)
	}
	after, err := st.AccessSummary(AccessSummaryFilter{SinceDay: day, TopLimit: 50})
	if err != nil {
		t.Fatalf("summary after fold: %v", err)
	}
	if after.TotalHits != before.TotalHits || after.UpBytes != before.UpBytes ||
		after.DownBytes != before.DownBytes {
		t.Fatalf("folding changed the totals: before %+v after %+v", before, after)
	}
	if after.UniqueHosts != cap+1 {
		t.Fatalf("unique hosts = %d, want %d", after.UniqueHosts, cap+1)
	}
	// The overflow row is visible as a normal destination, and a host from the
	// kept set survived. Every host has one hit here, so the tie is broken by
	// name and the kept set starts at h0.example.
	var sawOther, sawTop bool
	for _, host := range after.TopHosts {
		switch host.Host {
		case model.AccessOtherHost:
			sawOther = true
		case "h0.example":
			sawTop = true
		}
	}
	if !sawOther || !sawTop {
		t.Fatalf("folded view = %+v, want the overflow row and the busiest host", after.TopHosts)
	}

	// Folding again is a no-op: the key is already inside the cap.
	again, err := st.FoldAccessHosts(day, cap, 0)
	if err != nil {
		t.Fatalf("second fold: %v", err)
	}
	if again != 0 {
		t.Fatalf("second fold removed %d rows, want 0", again)
	}
}

// TestFoldAccessHostsKeepsExistingOverflowRow covers a key that already has an
// overflow row: the new overflow is added to it instead of creating a second one
// or counting the old row back into the kept set.
func TestFoldAccessHostsKeepsExistingOverflowRow(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	day := "2026-11-15"
	const cap = 2
	records := []model.AccessRecord{
		accessRecord(alice.ID, in.ID, model.AccessOtherHost, 0, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "a.example", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "b.example", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "c.example", 443, model.AccessAllowed, "", day),
		accessRecord(alice.ID, in.ID, "d.example", 443, model.AccessAllowed, "", day),
	}
	records[0].UpBytes = 7
	records[3].UpBytes = 100
	records[4].UpBytes = 200
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := st.FoldAccessHosts(day, cap, 0); err != nil {
		t.Fatalf("fold: %v", err)
	}
	summary, err := st.AccessSummary(AccessSummaryFilter{SinceDay: day, TopLimit: 50})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalHits != 5 || summary.UpBytes != 307 {
		t.Fatalf("totals after folding an existing overflow row: %+v", summary)
	}
	if summary.UniqueHosts != cap+1 {
		t.Fatalf("unique hosts = %d, want %d", summary.UniqueHosts, cap+1)
	}
}

// TestAccessRollupFiltersUseIndexes guards the 009 migration: the two id filters
// have to be able to use an index, because they are not prefixes of the
// (day, client_id, inbound_id, host) primary key.
func TestAccessRollupFiltersUseIndexes(t *testing.T) {
	st := newTestStore(t)
	cases := []struct {
		query string
		args  []any
		want  string
	}{
		{
			query: `SELECT SUM(hits) FROM access_daily WHERE day >= ? AND client_id = ?`,
			args:  []any{"2026-01-01", int64(1)},
			want:  "idx_access_daily_client",
		},
		{
			query: `SELECT SUM(hits) FROM access_daily WHERE day >= ? AND inbound_id = ?`,
			args:  []any{"2026-01-01", int64(1)},
			want:  "idx_access_daily_inbound",
		},
	}
	for _, tc := range cases {
		rows, err := st.db.Query("EXPLAIN QUERY PLAN "+tc.query, tc.args...)
		if err != nil {
			t.Fatalf("explain %q: %v", tc.query, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatalf("scan plan: %v", err)
			}
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if !strings.Contains(plan.String(), tc.want) {
			t.Fatalf("query plan for %q does not use %s:\n%s", tc.query, tc.want, plan.String())
		}
	}
}

// TestPruneAccessBatchesAreBoundedByRows covers the invariant the retention
// design rests on: no single DELETE may carry more than one batch of rows, so a
// big backlog cannot hold the store write lock for a long time.
func TestPruneAccessBatchesAreBoundedByRows(t *testing.T) {
	st := newTestStore(t)
	in, alice, _ := seedAccessFixture(t, st)
	const rows = accessPruneBatch + 250
	records := make([]model.AccessRecord, 0, rows)
	for i := 0; i < rows; i++ {
		record := accessRecord(alice.ID, in.ID, "host-"+strconv.Itoa(i)+".example", 443,
			model.AccessAllowed, "", "2020-01-01")
		record.StartedAt = time.Now().Add(-14 * 24 * time.Hour).Unix()
		records = append(records, record)
	}
	if err := st.SaveAccess(records); err != nil {
		t.Fatalf("save: %v", err)
	}

	deleted, err := st.pruneAccessBatch(time.Now().Add(-7 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if deleted != accessPruneBatch {
		t.Fatalf("one batch deleted %d rows, want the bound %d", deleted, accessPruneBatch)
	}
	deleted, err = st.pruneAccessBatch(time.Now().Add(-7 * 24 * time.Hour).Unix())
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if deleted != 250 {
		t.Fatalf("the tail batch deleted %d rows, want 250", deleted)
	}

	// The rollup follows the same rule: one batch, not one whole day.
	const days = 4
	rollup := make([]model.AccessRecord, 0, days*accessPruneBatch/1000)
	for d := 0; d < days; d++ {
		day := time.Now().AddDate(0, 0, -100-d).Format("2006-01-02")
		for i := 0; i < accessPruneBatch/1000; i++ {
			record := accessRecord(alice.ID, in.ID, "r-"+strconv.Itoa(d)+"-"+strconv.Itoa(i)+".example",
				443, model.AccessAllowed, "", day)
			record.StartedAt = time.Now().AddDate(0, 0, -100-d).Unix()
			rollup = append(rollup, record)
		}
	}
	if err := st.SaveAccess(rollup); err != nil {
		t.Fatalf("save rollup: %v", err)
	}
	got, err := st.pruneAccessDaysBatch(time.Now().Format("2006-01-02"))
	if err != nil {
		t.Fatalf("rollup batch: %v", err)
	}
	if got <= 0 {
		t.Fatalf("the rollup batch removed nothing")
	}
	if got > accessPruneBatch {
		t.Fatalf("the rollup batch removed %d rows, over the bound %d", got, accessPruneBatch)
	}
}
