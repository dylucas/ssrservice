package ssserver

import (
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"

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

// BenchmarkUsageAuthorize measures the per-connection access gate.
func BenchmarkUsageAuthorize(b *testing.B) {
	u, _ := newBenchUsageManager(1)
	now := benchNow()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := u.authorize(1, now); err != nil {
			b.Fatalf("authorize: %v", err)
		}
	}
}

// BenchmarkDestinationSetAdd measures the UDP destination whitelist fast path
// for an address that was already seen.
func BenchmarkDestinationSetAdd(b *testing.B) {
	set := newDestinationSet(256)
	target := netip.MustParseAddrPort("203.0.113.5:443")
	set.add(target)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set.add(target)
	}
}

// BenchmarkUDPDestination measures the Socksaddr to netip.AddrPort fast path
// used when writing a reply packet back to a client.
func BenchmarkUDPDestination(b *testing.B) {
	destination := M.SocksaddrFromNetIP(netip.MustParseAddrPort("[2001:db8::1]:8388"))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := udpDestination(destination); !ok {
			b.Fatal("expected a dialable address")
		}
	}
}

// BenchmarkProtocolSignature measures the reload fingerprint. Comparing two
// signatures is what lets a quota or note change skip the protocol rebuild.
func BenchmarkProtocolSignature(b *testing.B) {
	clients := make([]ClientSnapshot, 0, 100)
	for i := 0; i < 100; i++ {
		clients = append(clients, ClientSnapshot{ID: int64(i + 1), PSK: "psk", Enabled: true})
	}
	snap := testSnapshot(model.Method2022AES128GCM, "server-psk", clients...)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = protocolSignature(snap)
	}
}

// BenchmarkUDPBufferReset measures reusing one NAT session buffer per packet
// instead of acquiring and releasing a pooled 64 KiB buffer for every datagram.
func BenchmarkUDPBufferReset(b *testing.B) {
	buffer := buf.NewSize(maxUDPPacketSize)
	defer buffer.Release()
	payload := make([]byte, 1400)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer.Reset()
		buffer.Write(payload)
		buffer.Reset()
	}
}

// BenchmarkUDPBufferPerPacket measures the old per-datagram allocation pattern
// (acquire a pooled 64 KiB buffer, release it) for comparison with
// BenchmarkUDPBufferReset.
func BenchmarkUDPBufferPerPacket(b *testing.B) {
	payload := make([]byte, 1400)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer := buf.NewSize(maxUDPPacketSize)
		buffer.Write(payload)
		buffer.Release()
	}
}
