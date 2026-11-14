package store

import (
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

// BenchmarkListClientRows measures the lean list projection.
func BenchmarkListClientRows(b *testing.B) {
	st := benchStore(b, 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.ListClientRows(0); err != nil {
			b.Fatalf("rows: %v", err)
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
