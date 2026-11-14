package store

import (
	"errors"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

// detailTotals recomputes the per-day totals straight from the detail rows, so
// the tests compare the maintained rollup against the source of truth.
func detailTotals(t *testing.T, st *Store) map[string][2]int64 {
	t.Helper()
	rows, err := st.db.Query(`SELECT day, COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
		FROM traffic_daily GROUP BY day`)
	if err != nil {
		t.Fatalf("detail totals: %v", err)
	}
	defer rows.Close()
	totals := make(map[string][2]int64)
	for rows.Next() {
		var (
			day      string
			up, down int64
		)
		if err := rows.Scan(&day, &up, &down); err != nil {
			t.Fatalf("scan: %v", err)
		}
		totals[day] = [2]int64{up, down}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return totals
}

// rollupTotals reads the rollup rows the dashboard uses.
func rollupTotals(t *testing.T, st *Store) map[string][2]int64 {
	t.Helper()
	rows, err := st.db.Query(`SELECT day, up_bytes, down_bytes FROM traffic_daily_totals`)
	if err != nil {
		t.Fatalf("rollup totals: %v", err)
	}
	defer rows.Close()
	totals := make(map[string][2]int64)
	for rows.Next() {
		var (
			day      string
			up, down int64
		)
		if err := rows.Scan(&day, &up, &down); err != nil {
			t.Fatalf("scan: %v", err)
		}
		totals[day] = [2]int64{up, down}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return totals
}

// assertRollupMatchesDetail is the invariant every mutation must preserve.
func assertRollupMatchesDetail(t *testing.T, st *Store) {
	t.Helper()
	want := detailTotals(t, st)
	got := rollupTotals(t, st)
	if len(want) != len(got) {
		t.Fatalf("rollup has %d days, detail has %d: rollup=%v detail=%v", len(got), len(want), got, want)
	}
	for day, w := range want {
		if g, ok := got[day]; !ok || g != w {
			t.Fatalf("day %s: rollup=%v want=%v", day, got[day], w)
		}
	}
}

func newUsageTestStore(t *testing.T) (*Store, model.Inbound) {
	t.Helper()
	st := newTestStore(t)
	in := model.Inbound{Listen: "127.0.0.1", Port: 9300, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	return st, in
}

func mkClient(t *testing.T, st *Store, inboundID int64, name string) model.Client {
	t.Helper()
	c := model.Client{InboundID: inboundID, Name: name, PSK: "psk", Enabled: true, SubToken: "tok-" + name}
	if err := st.CreateClient(&c); err != nil {
		t.Fatalf("client %s: %v", name, err)
	}
	return c
}

// TestRollupTracksSaveUsage verifies the rollup stays equal to a recomputation
// from the detail rows across several batches, including repeated days.
func TestRollupTracksSaveUsage(t *testing.T) {
	st, in := newUsageTestStore(t)
	a := mkClient(t, st, in.ID, "a")
	b := mkClient(t, st, in.ID, "b")

	batches := [][]model.UsageSnapshot{
		{
			{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-05-01", DayUpBytes: 10, DayDownBytes: 20},
			{ClientID: b.ID, UpBytes: 1, DownBytes: 2, Day: "2026-05-01", DayUpBytes: 1, DayDownBytes: 2},
		},
		{
			// Same day again, plus a new day and a totals-only entry.
			{ClientID: a.ID, UpBytes: 15, DownBytes: 25, Day: "2026-05-01", DayUpBytes: 5, DayDownBytes: 5},
			{ClientID: b.ID, UpBytes: 3, DownBytes: 4, Day: "2026-05-02", DayUpBytes: 2, DayDownBytes: 2},
			{ClientID: b.ID, UpBytes: 3, DownBytes: 4, Day: "2026-05-02"},
		},
	}
	for i, batch := range batches {
		if err := st.SaveUsage(batch); err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		assertRollupMatchesDetail(t, st)
	}

	totals, err := st.DailyTotals("2026-05-01")
	if err != nil {
		t.Fatalf("daily totals: %v", err)
	}
	if len(totals) != 2 {
		t.Fatalf("expected 2 days, got %+v", totals)
	}
	if totals[0].Day != "2026-05-01" || totals[0].UpBytes != 16 || totals[0].DownBytes != 27 {
		t.Fatalf("unexpected first day: %+v", totals[0])
	}
	if totals[1].Day != "2026-05-02" || totals[1].UpBytes != 2 || totals[1].DownBytes != 2 {
		t.Fatalf("unexpected second day: %+v", totals[1])
	}
}

// TestSaveUsageBatchedChunkBoundaries exercises writes that straddle the internal
// chunk size so a chunking bug cannot hide behind small inputs.
func TestSaveUsageBatchedChunkBoundaries(t *testing.T) {
	st, in := newUsageTestStore(t)
	const clients = 3
	ids := make([]int64, 0, clients)
	for i := 0; i < clients; i++ {
		c := mkClient(t, st, in.ID, "chunk-"+itoaStore(i))
		ids = append(ids, c.ID)
	}

	// usageWriteChunk is 200; build a batch of exactly 3*200+1 rows spread over
	// three days so every chunk boundary is crossed.
	const total = usageWriteChunk*3 + 1
	batch := make([]model.UsageSnapshot, 0, total)
	wantUp := make(map[string]int64)
	wantClientUp := make(map[int64]int64)
	wantClientDown := make(map[int64]int64)
	for i := 0; i < total; i++ {
		id := ids[i%len(ids)]
		day := []string{"2026-06-01", "2026-06-02", "2026-06-03"}[i%3]
		up := int64(i + 1)
		down := int64(i * 2)
		batch = append(batch, model.UsageSnapshot{
			ClientID: id, UpBytes: up, DownBytes: down,
			Day: day, DayUpBytes: up, DayDownBytes: down,
		})
		wantUp[day] += up
		wantClientUp[id] += up
		wantClientDown[id] += down
	}
	if err := st.SaveUsage(batch); err != nil {
		t.Fatalf("save: %v", err)
	}
	assertRollupMatchesDetail(t, st)

	rollup := rollupTotals(t, st)
	for day, up := range wantUp {
		if rollup[day][0] != up {
			t.Fatalf("rollup day %s up = %d, want %d", day, rollup[day][0], up)
		}
	}
	// The last snapshot per client wins for the absolute totals (that is the
	// runtime's contract: snapshots carry running totals).
	for _, id := range ids {
		c, err := st.ClientByID(id)
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		var lastUp, lastDown int64
		for _, snap := range batch {
			if snap.ClientID == id {
				lastUp, lastDown = snap.UpBytes, snap.DownBytes
			}
		}
		if c.UpBytes != lastUp || c.DownBytes != lastDown {
			t.Fatalf("client %d totals = %d/%d, want %d/%d", id, c.UpBytes, c.DownBytes, lastUp, lastDown)
		}
	}
}

// TestRollupAfterDeleteClient verifies a client delete drops its contribution,
// including days where it was the only contributor (the rollup row must vanish).
func TestRollupAfterDeleteClient(t *testing.T) {
	st, in := newUsageTestStore(t)
	a := mkClient(t, st, in.ID, "del-a")
	b := mkClient(t, st, in.ID, "del-b")
	if err := st.SaveUsage([]model.UsageSnapshot{
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-07-01", DayUpBytes: 10, DayDownBytes: 20},
		{ClientID: b.ID, UpBytes: 5, DownBytes: 6, Day: "2026-07-01", DayUpBytes: 5, DayDownBytes: 6},
		// Only a contributes to this day.
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-07-02", DayUpBytes: 3, DayDownBytes: 4},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	assertRollupMatchesDetail(t, st)

	if err := st.DeleteClient(a.ID); err != nil {
		t.Fatalf("delete client: %v", err)
	}
	assertRollupMatchesDetail(t, st)

	rollup := rollupTotals(t, st)
	if _, ok := rollup["2026-07-02"]; ok {
		t.Fatalf("day with no remaining detail rows must disappear from the rollup: %v", rollup)
	}
	if rollup["2026-07-01"][0] != 5 {
		t.Fatalf("remaining day up = %d, want 5", rollup["2026-07-01"][0])
	}
	if _, err := st.ClientByID(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client should be gone, got %v", err)
	}
}

// TestRollupAfterDeleteInbound covers the cascade from the inbound side.
func TestRollupAfterDeleteInbound(t *testing.T) {
	st, in := newUsageTestStore(t)
	other := model.Inbound{Listen: "127.0.0.1", Port: 9301, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&other); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	a := mkClient(t, st, in.ID, "cascade-a")
	b := mkClient(t, st, in.ID, "cascade-b")
	keep := mkClient(t, st, other.ID, "keep")
	if err := st.SaveUsage([]model.UsageSnapshot{
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-08-01", DayUpBytes: 10, DayDownBytes: 20},
		{ClientID: b.ID, UpBytes: 1, DownBytes: 2, Day: "2026-08-01", DayUpBytes: 1, DayDownBytes: 2},
		{ClientID: keep.ID, UpBytes: 7, DownBytes: 8, Day: "2026-08-01", DayUpBytes: 7, DayDownBytes: 8},
		{ClientID: b.ID, UpBytes: 1, DownBytes: 2, Day: "2026-08-02", DayUpBytes: 1, DayDownBytes: 2},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.DeleteInbound(in.ID); err != nil {
		t.Fatalf("delete inbound: %v", err)
	}
	assertRollupMatchesDetail(t, st)
	rollup := rollupTotals(t, st)
	if _, ok := rollup["2026-08-02"]; ok {
		t.Fatalf("cascade-only day must vanish: %v", rollup)
	}
	if rollup["2026-08-01"][0] != 7 || rollup["2026-08-01"][1] != 8 {
		t.Fatalf("surviving inbound's traffic lost: %v", rollup["2026-08-01"])
	}
}

// TestRebuildDailyTotals repairs a deliberately corrupted rollup.
func TestRebuildDailyTotals(t *testing.T) {
	st, in := newUsageTestStore(t)
	a := mkClient(t, st, in.ID, "rebuild-a")
	if err := st.SaveUsage([]model.UsageSnapshot{
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-09-01", DayUpBytes: 10, DayDownBytes: 20},
		{ClientID: a.ID, UpBytes: 10, DownBytes: 20, Day: "2026-09-02", DayUpBytes: 4, DayDownBytes: 5},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE traffic_daily_totals SET up_bytes = up_bytes + 999`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if _, err := st.db.Exec(`INSERT INTO traffic_daily_totals(day, up_bytes, down_bytes) VALUES('2026-01-01', 5, 5)`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if err := st.RebuildDailyTotals(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	assertRollupMatchesDetail(t, st)
}

func itoaStore(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [8]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
