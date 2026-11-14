package store

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// seedBenchClients inserts n clients spread over a few inbounds.
func seedBenchClients(b *testing.B, st *Store, n int) {
	b.Helper()
	inbounds := make([]model.Inbound, 0, 5)
	for i := 0; i < 5; i++ {
		in := model.Inbound{Listen: "127.0.0.1", Port: 20000 + i,
			Method: model.Method2022AES128GCM, Enabled: true}
		if err := st.CreateInbound(&in); err != nil {
			b.Fatalf("inbound: %v", err)
		}
		inbounds = append(inbounds, in)
	}
	clients := make([]model.Client, 0, n)
	for i := 0; i < n; i++ {
		clients = append(clients, model.Client{
			InboundID: inbounds[i%len(inbounds)].ID,
			Name:      "bench-" + itoaBench(i),
			PSK:       "psk", Enabled: i%3 != 0, SubToken: "tok-" + itoaBench(i),
			UpBytes: int64(i), DownBytes: int64(i * 2),
		})
	}
	for i := range clients {
		if err := st.CreateClient(&clients[i]); err != nil {
			b.Fatalf("client: %v", err)
		}
	}
}

func itoaBench(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [12]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

func benchStore(b *testing.B, n int) *Store {
	b.Helper()
	b.StopTimer()
	st, err := Open(b.TempDir() + "/bench.db")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { st.Close() })
	seedBenchClients(b, st, n)
	b.StartTimer()
	return st
}

// benchEmptyStore opens a store with one inbound and no clients, for the insert
// benchmarks that seed their own rows.
func benchEmptyStore(b *testing.B, port int) *Store {
	b.Helper()
	b.StopTimer()
	st, err := Open(b.TempDir() + "/insert-bench.db")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { st.Close() })
	in := model.Inbound{Listen: "127.0.0.1", Port: port, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		b.Fatalf("inbound: %v", err)
	}
	b.StartTimer()
	return st
}

// BenchmarkInboundAggregates measures the grouped query the inbound list uses.
func BenchmarkInboundAggregates(b *testing.B) {
	st := benchStore(b, 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.InboundAggregates(0); err != nil {
			b.Fatalf("aggregates: %v", err)
		}
	}
}

// BenchmarkListClientPage measures the paged list projection the panel renders.
// The first page is the case the panel hits on every visit; the unfiltered
// count dominates it because SQLite has to visit every row to produce a total.
func BenchmarkListClientPage(b *testing.B) {
	st := benchStore(b, 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.ListClientPage(ClientPageFilter{
			Limit: ClientPageDefaultLimit, Now: time.Now().Unix(),
		}); err != nil {
			b.Fatalf("page: %v", err)
		}
	}
}

// BenchmarkListClientPageFiltered measures the same page with a status filter,
// which is the slowest documented query because the status has no index and the
// count must evaluate the predicate on every row.
func BenchmarkListClientPageFiltered(b *testing.B) {
	st := benchStore(b, 5000)
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.ListClientPage(ClientPageFilter{
			Status: model.StatusActive, Limit: ClientPageDefaultLimit, Now: now,
		}); err != nil {
			b.Fatalf("page: %v", err)
		}
	}
}

// BenchmarkListClientsFull measures the full-row scan, kept for comparison.
func BenchmarkListClientsFull(b *testing.B) {
	st := benchStore(b, 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.ListClients(0); err != nil {
			b.Fatalf("clients: %v", err)
		}
	}
}

// seedBenchUsage creates n clients each with 30 days of daily traffic, which is
// the shape the dashboard aggregates over.
func seedBenchUsage(b *testing.B, n int) *Store {
	b.Helper()
	b.StopTimer()
	st, err := Open(b.TempDir() + "/usage-bench.db")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { st.Close() })
	in := model.Inbound{Listen: "127.0.0.1", Port: 31000, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		b.Fatalf("inbound: %v", err)
	}
	for i := 0; i < n; i++ {
		c := model.Client{InboundID: in.ID, Name: "bench-" + itoaBench(i), PSK: "psk",
			Enabled: true, SubToken: "tok-" + itoaBench(i)}
		if err := st.CreateClient(&c); err != nil {
			b.Fatalf("client: %v", err)
		}
	}
	// One batch carrying the whole month for every client.
	snaps := make([]model.UsageSnapshot, 0, n*30)
	for i := 0; i < n; i++ {
		c, err := st.ClientByID(int64(i + 1))
		if err != nil {
			b.Fatalf("client: %v", err)
		}
		for d := 0; d < 30; d++ {
			day := time.Now().AddDate(0, 0, -d).Format("2006-01-02")
			snaps = append(snaps, model.UsageSnapshot{
				ClientID: c.ID, UpBytes: int64(i*d + 1), DownBytes: int64(i * d * 2),
				Day: day, DayUpBytes: int64(i + 1), DayDownBytes: int64(i * 2),
			})
		}
	}
	if err := st.SaveUsage(snaps); err != nil {
		b.Fatalf("seed usage: %v", err)
	}
	b.StartTimer()
	return st
}

// BenchmarkDailyTotals measures the dashboard 30 day series after moving it onto
// the per-day rollup.
func BenchmarkDailyTotals(b *testing.B) {
	st := seedBenchUsage(b, 2000)
	since := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.DailyTotals(since); err != nil {
			b.Fatalf("daily totals: %v", err)
		}
	}
}

// BenchmarkCreateClientPerRow measures the per-row insert the import path used
// to run, kept so the batched form can be compared against it.
func BenchmarkCreateClientPerRow(b *testing.B) {
	st := benchEmptyStore(b, 9600)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for r := 0; r < 999; r++ {
			c := model.Client{InboundID: 1, Name: "row-" + itoaBench(i) + "-" + itoaBench(r),
				PSK: "psk", Enabled: true, SubToken: "row-tok-" + itoaBench(i) + "-" + itoaBench(r)}
			if err := st.CreateClient(&c); err != nil {
				b.Fatalf("client: %v", err)
			}
		}
	}
}

// BenchmarkCreateClientsBatch measures one bulk import insert.
func BenchmarkCreateClientsBatch(b *testing.B) {
	st := benchEmptyStore(b, 9601)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := make([]*model.Client, 0, 999)
		for r := 0; r < 999; r++ {
			batch = append(batch, &model.Client{InboundID: 1,
				Name: "batch-" + itoaBench(i) + "-" + itoaBench(r), PSK: "psk",
				Enabled: true, SubToken: "batch-tok-" + itoaBench(i) + "-" + itoaBench(r)})
		}
		if err := st.CreateClients(batch); err != nil {
			b.Fatalf("batch: %v", err)
		}
	}
}

// benchAuditStore opens a store with n audit rows spread over the retention
// window and two operators, the shape the audit page filters.
func benchAuditStore(b *testing.B, n int) *Store {
	b.Helper()
	b.StopTimer()
	st, err := Open(b.TempDir() + "/audit-bench.db")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { st.Close() })
	now := time.Now().Unix()
	err = st.write(func(tx *sql.Tx) error {
		const chunk = 200
		for start := 0; start < n; start += chunk {
			end := start + chunk
			if end > n {
				end = n
			}
			var sb strings.Builder
			sb.WriteString(`INSERT INTO audit_logs(created_at, admin_id, username, action,
				target_type, target_id, detail, ip) VALUES `)
			args := make([]any, 0, (end-start)*8)
			for i := start; i < end; i++ {
				if i > start {
					sb.WriteByte(',')
				}
				sb.WriteString(`(?,?,?,?,?,?,?,?)`)
				operator := "admin"
				action := model.AuditClientUpdate
				if i%2 == 1 {
					operator = "ops"
					action = model.AuditAuthLogin
				}
				args = append(args, now-int64(i*60), 1, operator, action,
					"client", i%50, `name="bench" quota_bytes=1`, "192.0.2.10")
			}
			if _, err := tx.Exec(sb.String(), args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("seed audit: %v", err)
	}
	b.StartTimer()
	return st
}

// BenchmarkListAuditByUsername measures the filtered audit page, which is what
// the (username, created_at) index exists for.
func BenchmarkListAuditByUsername(b *testing.B) {
	st := benchAuditStore(b, 50000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.ListAudit(AuditFilter{Username: "admin"}); err != nil {
			b.Fatalf("audit: %v", err)
		}
	}
}

// BenchmarkListAuditPlain measures the unfiltered first page for comparison.
func BenchmarkListAuditPlain(b *testing.B) {
	st := benchAuditStore(b, 50000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.ListAudit(AuditFilter{}); err != nil {
			b.Fatalf("audit: %v", err)
		}
	}
}

// BenchmarkSaveUsage measures a full flush: one snapshot per client.
func BenchmarkSaveUsage(b *testing.B) {
	st, err := Open(b.TempDir() + "/flush-bench.db")
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { st.Close() })
	in := model.Inbound{Listen: "127.0.0.1", Port: 31001, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		b.Fatalf("inbound: %v", err)
	}
	const clients = 2000
	snaps := make([]model.UsageSnapshot, 0, clients)
	for i := 0; i < clients; i++ {
		c := model.Client{InboundID: in.ID, Name: "flush-" + itoaBench(i), PSK: "psk",
			Enabled: true, SubToken: "flush-tok-" + itoaBench(i)}
		if err := st.CreateClient(&c); err != nil {
			b.Fatalf("client: %v", err)
		}
		snaps = append(snaps, model.UsageSnapshot{
			ClientID: c.ID, UpBytes: int64(i + 1), DownBytes: int64(i * 2),
			Day: time.Now().Format("2006-01-02"), DayUpBytes: 1, DayDownBytes: 2,
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.SaveUsage(snaps); err != nil {
			b.Fatalf("save usage: %v", err)
		}
	}
}
