package store

import (
	"testing"

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
