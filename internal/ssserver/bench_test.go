package ssserver

import (
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// benchSink counts flushes without touching a database.
type benchSink struct {
	calls int
}

func (s *benchSink) SaveUsage(snaps []model.UsageSnapshot) error {
	s.calls++
	return nil
}

func benchNow() time.Time {
	return time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
}

func newBenchUsageManager(accounts int) (*usageManager, *benchSink) {
	sink := &benchSink{}
	u := newUsageManager(sink, time.UTC, nil)
	now := benchNow()
	u.now = func() time.Time { return now }
	for i := 1; i <= accounts; i++ {
		u.ensure(ClientSnapshot{ID: int64(i), Enabled: true}, 0, 0)
	}
	return u, sink
}

// BenchmarkUsageAdd measures the per-packet accounting path.
func BenchmarkUsageAdd(b *testing.B) {
	u, _ := newBenchUsageManager(1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u.add(1, true, 64)
	}
}

// BenchmarkUsageAddParallel measures the accounting path under contention.
func BenchmarkUsageAddParallel(b *testing.B) {
	u, _ := newBenchUsageManager(1)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			u.add(1, true, 64)
		}
	})
}

// BenchmarkUsageFlushIdle measures a flush tick where no client has new
// traffic. Idle accounts must not be handed to the sink.
func BenchmarkUsageFlushIdle(b *testing.B) {
	u, _ := newBenchUsageManager(1000)
	if err := u.flush(); err != nil {
		b.Fatalf("initial flush: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := u.flush(); err != nil {
			b.Fatalf("flush: %v", err)
		}
	}
}

// BenchmarkPacketWithHeadroom measures the outbound UDP buffer construction.
func BenchmarkPacketWithHeadroom(b *testing.B) {
	payload := make([]byte, 1400)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer := packetWithHeadroom(payload)
		buffer.Release()
	}
}

// BenchmarkUsageAddNonUTC measures accounting in a non-UTC deployment, where
// the day boundary calculation has to convert time zones on every packet.
func BenchmarkUsageAddNonUTC(b *testing.B) {
	sink := &benchSink{}
	u := newUsageManager(sink, time.FixedZone("CST", 8*3600), nil)
	now := benchNow().In(time.FixedZone("CST", 8*3600))
	u.now = func() time.Time { return now }
	u.ensure(ClientSnapshot{ID: 1, Enabled: true}, 0, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u.add(1, true, 64)
	}
}
