package ssserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// captureSink records the batches the access recorder hands over.
type captureSink struct {
	mu      sync.Mutex
	batches [][]model.AccessRecord
	err     error
}

func (c *captureSink) SaveAccess(records []model.AccessRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	batch := make([]model.AccessRecord, len(records))
	copy(batch, records)
	c.batches = append(c.batches, batch)
	return nil
}

func (c *captureSink) all() []model.AccessRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.AccessRecord, 0)
	for _, batch := range c.batches {
		out = append(out, batch...)
	}
	return out
}

// storedAccess polls the store until at least n access records are present.
func storedAccess(t *testing.T, env *testEnv, n int) []model.AccessEvent {
	t.Helper()
	var events []model.AccessEvent
	waitFor(t, 8*time.Second, "access records", func() bool {
		env.mgr.FlushAccess()
		got, _, err := env.store.ListAccessEvents(store.AccessEventFilter{})
		if err != nil {
			return false
		}
		if len(got) >= n {
			events = got
			return true
		}
		return false
	})
	return events
}

// splitHostPort returns the host and numeric port of an address string.
func splitHostPort(t *testing.T, address string) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	return host, value
}

// udpTarget builds the destination a client would request: a domain name with
// a port.
func udpTarget(host string, port int) M.Socksaddr {
	return M.Socksaddr{Fqdn: host, Port: uint16(port)}
}

func netipAddrPort(t *testing.T, address string) netip.AddrPort {
	t.Helper()
	parsed, err := netip.ParseAddrPort(address)
	if err != nil {
		t.Fatalf("addr port %q: %v", address, err)
	}
	return parsed
}

func TestAccessRecordingDisabledByDefault(t *testing.T) {
	env := newTestEnv(t)
	if env.mgr.AccessEnabled() {
		t.Fatal("access recording must start disabled")
	}
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "idle", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	echoRoundTrip(t, conn, "quiet")
	conn.Close()

	if count, err := env.store.CountAccessEvents(); err != nil || count != 0 {
		t.Fatalf("disabled recording wrote %d rows (%v)", count, err)
	}
	if dropped := env.mgr.AccessDropped(); dropped != 0 {
		t.Fatalf("disabled recording dropped %d records", dropped)
	}
}

func TestAccessRecordsAllowedTCP(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.Start()
	env.mgr.SetAccessEnabled(true)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "alice", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	echoRoundTrip(t, conn, "hello access")
	conn.Close()

	events := storedAccess(t, env, 1)
	event := events[0]
	if event.Outcome != model.AccessAllowed {
		t.Fatalf("outcome = %q, want allowed", event.Outcome)
	}
	if event.Network != "tcp" || event.Host != "127.0.0.1" {
		t.Fatalf("unexpected target %+v", event)
	}
	echoHost, echoPort := splitHostPort(t, env.echoV4.addr)
	if event.Port != echoPort || event.IP != echoHost {
		t.Fatalf("port/ip = %d/%q, want %d/%q", event.Port, event.IP, echoPort, echoHost)
	}
	if event.UpBytes < 11 || event.DownBytes < 11 {
		t.Fatalf("bytes not counted: up=%d down=%d", event.UpBytes, event.DownBytes)
	}
	if event.ClientName != "alice" || event.InboundRemark != "test" {
		t.Fatalf("names not joined: %+v", event)
	}
	if event.DurationMS < 0 {
		t.Fatalf("negative duration: %d", event.DurationMS)
	}
}

func TestAccessRecordsPolicyBlockedTarget(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.Start()
	env.mgr.SetAccessEnabled(true)
	env.mgr.SetPolicy(PolicyConfig{AllowPrivateTargets: false, BlockMetadata: true})
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "guarded", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err == nil {
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("blocked"))
		_, _ = conn.Read(make([]byte, 1))
		conn.Close()
	}

	events := storedAccess(t, env, 1)
	if events[0].Outcome != model.AccessBlocked || events[0].Reason != model.AccessReasonPolicy {
		t.Fatalf("unexpected record %+v", events[0])
	}
	// A blocked target never dialed, so it has no resolved address and no
	// payload behind it.
	if events[0].IP != "" {
		t.Fatalf("blocked record carries an IP: %+v", events[0])
	}
}

func TestAccessRecordsDialFailure(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.Start()
	env.mgr.SetAccessEnabled(true)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "unlucky", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	// A port that is free for TCP and UDP has no listener, so the policy allows
	// the address and the dial is what fails.
	deadPort := freePort(t)
	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK,
		net.JoinHostPort("127.0.0.1", strconv.Itoa(deadPort)))
	if err == nil {
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = conn.Write([]byte("nobody home"))
		_, _ = conn.Read(make([]byte, 1))
		conn.Close()
	}

	events := storedAccess(t, env, 1)
	if events[0].Outcome != model.AccessFailed || events[0].Reason != model.AccessReasonDial {
		t.Fatalf("unexpected record %+v", events[0])
	}
	if events[0].Port != deadPort {
		t.Fatalf("port = %d, want %d", events[0].Port, deadPort)
	}
}

func TestAccessRecordsQuotaBlocked(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.Start()
	env.mgr.SetAccessEnabled(true)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "over", PSK: newTestKey(t, 16), Enabled: true,
		SubToken: "tok", QuotaBytes: 8}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	// Spend the quota through the relay, then connect again. The protocol still
	// accepts the user, and the gate inside the handler is what rejects it, so
	// this is the path that records a blocked/quota event.
	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	_, _ = conn.Write(make([]byte, 64))
	waitFor(t, 5*time.Second, "quota exceeded", func() bool {
		up, down := env.mgr.ClientTraffic(client.ID)
		return up+down >= 8
	})
	conn.Close()

	// Give the first connection's allowed record time to land, then dial again.
	storedAccess(t, env, 1)
	blocked, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err == nil {
		blocked.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = blocked.Write([]byte("nope"))
		_, _ = blocked.Read(make([]byte, 1))
		blocked.Close()
	}

	var events []model.AccessEvent
	waitFor(t, 8*time.Second, "blocked quota record", func() bool {
		env.mgr.FlushAccess()
		got, _, err := env.store.ListAccessEvents(store.AccessEventFilter{Outcome: model.AccessBlocked})
		if err != nil || len(got) == 0 {
			return false
		}
		events = got
		return true
	})
	if events[0].Reason != model.AccessReasonQuota {
		t.Fatalf("unexpected record %+v", events[0])
	}
}

func TestAccessRecordsUDP(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.Start()
	env.mgr.SetAccessEnabled(true)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "udp-user", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	echoConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("udp echo: %v", err)
	}
	defer echoConn.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := echoConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = echoConn.WriteToUDP(buf[:n], addr)
		}
	}()

	method, err := buildClientMethod(in.Method, serverPSK, client.PSK)
	if err != nil {
		t.Fatalf("client method: %v", err)
	}
	raw, err := net.Dial("udp", env.addr())
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	packetConn := method.DialPacketConn(raw)
	if _, err := packetConn.WriteTo([]byte("udp access"), echoConn.LocalAddr()); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	buf := make([]byte, 2048)
	raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := packetConn.ReadFrom(buf); err != nil {
		t.Fatalf("read packet: %v", err)
	}
	packetConn.Close()
	raw.Close()

	// The record is emitted when the NAT session ends, which can be as late as
	// the configured UDP idle timeout.
	var events []model.AccessEvent
	deadline := time.Now().Add(10 * time.Second)
	for {
		env.mgr.FlushAccess()
		got, _, err := env.store.ListAccessEvents(store.AccessEventFilter{})
		if err == nil && len(got) > 0 {
			events = got
			break
		}
		if time.Now().After(deadline) {
			count, countErr := env.store.CountAccessEvents()
			got, total, listErr := env.store.ListAccessEvents(store.AccessEventFilter{})
			t.Fatalf("timed out waiting for udp access record (stored=%d dropped=%d countErr=%v list=%d/%d listErr=%v accessEnabled=%v sessions=%d)",
				count, env.mgr.AccessDropped(), countErr, len(got), total, listErr, env.mgr.AccessEnabled(), env.mgr.SessionCount())
		}
		time.Sleep(20 * time.Millisecond)
	}
	event := events[0]
	if event.Network != "udp" || event.Outcome != model.AccessAllowed {
		t.Fatalf("unexpected record %+v", event)
	}
	echoHost, echoPort := splitHostPort(t, echoConn.LocalAddr().String())
	if event.Host != echoHost || event.Port != echoPort {
		t.Fatalf("target = %s:%d, want %s:%d", event.Host, event.Port, echoHost, echoPort)
	}
	// The UDP record outlives the per-packet accounting, so both directions
	// must have been accumulated on the destination entry.
	if event.UpBytes < int64(len("udp access")) || event.DownBytes < int64(len("udp access")) {
		t.Fatalf("udp bytes not counted: up=%d down=%d", event.UpBytes, event.DownBytes)
	}
}

func TestUDPAccessFlushesActiveDestinationBeforeSessionEnds(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: time.Hour,
		Policy: PolicyConfig{AllowPrivateTargets: true},
	})
	mgr.Start()
	t.Cleanup(mgr.Stop)
	mgr.SetAccessEnabled(true)
	tracker := mgr.newUDPAccess(1, 1)
	target := netipAddrPort(t, "127.0.0.1:53")
	base := time.Now()

	// A live destination with traffic is not reported on the first sweep: the
	// quiet window gives a short burst time to accumulate into one record.
	tracker.addUp(target, udpTarget("example.com", 53), 10)
	tracker.sweep(base.Add(time.Second))
	mgr.FlushAccess()
	if got := len(sink.all()); got != 0 {
		t.Fatalf("a one second old burst was reported early: %d records", got)
	}

	// Once quiet, it is reported without waiting for the NAT session to end.
	tracker.sweep(base.Add(3 * time.Second))
	mgr.FlushAccess()
	if got := len(sink.all()); got != 1 {
		t.Fatalf("quiet destination not flushed: %d records", got)
	}
	// The synthetic clock also moved the idle cutoff, so the entry survived: the
	// quiet window flushes bytes, it does not end the session.
	if _, ok := tracker.targets[target]; !ok {
		t.Fatal("the quiet flush removed a destination that was still live")
	}

	// A continuously active destination is flushed after the max window even
	// though it never goes quiet. lastUsed is moved forward explicitly to model
	// a destination that keeps receiving packets until the sweep.
	tracker.addUp(target, udpTarget("example.com", 53), 5)
	at := base.Add(3*time.Second + accessUDPMaxWindow + time.Second)
	tracker.targets[target].lastUsed.Store(at.Add(-time.Second).UnixNano())
	tracker.sweep(at)
	mgr.FlushAccess()
	if got := len(sink.all()); got != 2 {
		t.Fatalf("active destination was not flushed at the max window: %d records", got)
	}
	tracker.finishAll()
	tracker.finishAll()
	mgr.FlushAccess()

	records := sink.all()
	totalUp := int64(0)
	for _, record := range records {
		totalUp += record.UpBytes
	}
	if totalUp != 15 {
		t.Fatalf("flushed %d bytes, want every byte once (15): %+v", totalUp, records)
	}
	// The session end must not repeat bytes that were already reported.
	if len(records) != 2 {
		t.Fatalf("expected one record per active window, got %d", len(records))
	}
}

// TestUDPAccessSweepRacesWithClose drives the two paths that can reach the same
// destination entry (the idle sweep and the session close) at once. Run under
// -race this guards the promise that every byte is reported exactly once even
// when they interleave.
func TestUDPAccessSweepRacesWithClose(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: time.Second,
		Policy: PolicyConfig{AllowPrivateTargets: true},
	})
	mgr.Start()
	t.Cleanup(mgr.Stop)
	mgr.SetAccessEnabled(true)
	tracker := mgr.newUDPAccess(1, 1)
	target := netipAddrPort(t, "127.0.0.1:53")

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tracker.addUp(target, udpTarget("example.com", 53), 1)
			tracker.addDown(target, 1)
		}
		close(done)
	}()
	go func() {
		defer wg.Done()
		for {
			tracker.sweep(time.Now())
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	wg.Wait()
	tracker.finishAll()
	mgr.FlushAccess()

	totalUp, totalDown := int64(0), int64(0)
	for _, record := range sink.all() {
		totalUp += record.UpBytes
		totalDown += record.DownBytes
	}
	// Every byte was sent exactly once; the records may be split across
	// windows and sessions, but they must add up to what was carried.
	// The loop sends one byte in each direction 200 times.
	if totalUp != 200 || totalDown != 200 {
		t.Fatalf("reported %d up / %d down for 200/200 sent: %+v", totalUp, totalDown, sink.all())
	}
}

func TestDisablingRecordingFinishesLiveUDPSessions(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: time.Hour,
		Policy: PolicyConfig{AllowPrivateTargets: true},
	})
	mgr.Start()
	t.Cleanup(mgr.Stop)
	mgr.SetAccessEnabled(true)
	tracker := mgr.newUDPAccess(1, 1)
	target := netipAddrPort(t, "127.0.0.1:53")
	tracker.addUp(target, udpTarget("example.com", 53), 42)

	mgr.SetAccessEnabled(false)
	mgr.FlushAccess()

	records := sink.all()
	if len(records) != 1 || records[0].UpBytes != 42 {
		t.Fatalf("disabling did not flush the live session: %+v", records)
	}
	// No bytes may keep accumulating once recording is off: the tracker was
	// finished, so the next packet finds no entry and a disabled beginAccess
	// returns nil.
	tracker.addUp(target, udpTarget("example.com", 53), 7)
	tracker.finishAll()
	mgr.FlushAccess()
	if got := len(sink.all()); got != 1 {
		t.Fatalf("a disabled recorder reported new traffic: %d records", got)
	}
}

func TestAccessRecorderDropsWhenQueueFull(t *testing.T) {
	sink := &captureSink{}
	recorder := newAccessRecorder(nil, sink, time.UTC)
	recorder.setEnabled(true)
	// No start(): nothing drains the queue, so every record past its capacity
	// is dropped and counted instead of blocking.
	for i := 0; i < accessQueueSize+10; i++ {
		recorder.submit(model.AccessRecord{ClientID: int64(i + 1), Host: "example.com"})
	}
	if got := recorder.droppedCount(); got != 10 {
		t.Fatalf("dropped = %d, want 10", got)
	}
}

// TestAccessRecorderCountsSinkFailures guards the promise the panel makes: when
// a write fails, those records show up in dropped_records instead of vanishing.
func TestAccessRecorderCountsSinkFailures(t *testing.T) {
	sink := &captureSink{err: errors.New("disk full")}
	recorder := newAccessRecorder(nil, sink, time.UTC)
	recorder.setEnabled(true)
	recorder.start()
	t.Cleanup(recorder.shutdown)

	recorder.submit(model.AccessRecord{ClientID: 1, Host: "example.com", StartedAt: time.Now().Unix()})
	recorder.submit(model.AccessRecord{ClientID: 2, Host: "example.org", StartedAt: time.Now().Unix()})
	recorder.flushNow()

	if got := recorder.droppedCount(); got == 0 {
		t.Fatal("a failing sink did not count the lost records")
	}
}

// TestUDPRecordBlockedRetriesAfterReenable covers a subtle case: the block key is
// what stops one rejected destination from being reported per packet, but it
// must not be kept when the record could not be created. Otherwise a
// destination blocked while recording was off would never be reported for the
// rest of the NAT session.
func TestUDPRecordBlockedRetriesAfterReenable(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink:    sink,
		Location:      time.UTC,
		UDPTimeout:    time.Second,
		FlushInterval: time.Hour,
	})
	mgr.SetAccessEnabled(true)
	// Start only the recorder loop: the record has to be drained to reach the
	// sink, and the traffic loops are irrelevant to this case.
	mgr.access.start()
	t.Cleanup(mgr.access.shutdown)
	tracker := mgr.newUDPAccess(1, 1)
	if tracker == nil {
		t.Fatal("expected a tracker while recording is on")
	}
	t.Cleanup(tracker.finishAll)

	blocked := udpTarget("blocked.example", 80)
	mgr.SetAccessEnabled(false)
	tracker.recordBlocked(blocked, model.AccessReasonPolicy)
	mgr.SetAccessEnabled(true)
	tracker.recordBlocked(blocked, model.AccessReasonPolicy)
	tracker.recordBlocked(blocked, model.AccessReasonPolicy)
	mgr.FlushAccess()

	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly one after re-enabling: %+v", len(records), records)
	}
	if records[0].Outcome != model.AccessBlocked || records[0].Reason != model.AccessReasonPolicy {
		t.Fatalf("unexpected record %+v", records[0])
	}
}

// TestPolicyReasonFor separates a refused destination from one that could not be
// resolved: the panel shows them as different reasons and the API documents
// both.
func TestPolicyReasonFor(t *testing.T) {
	blocked := fmt.Errorf("%w: loopback address", ErrDestinationBlocked)
	if got := policyReasonFor(blocked); got != model.AccessReasonPolicy {
		t.Fatalf("blocked destination = %q, want %q", got, model.AccessReasonPolicy)
	}
	resolveErr := fmt.Errorf("resolve example.invalid: %w", errors.New("no such host"))
	if got := policyReasonFor(resolveErr); got != model.AccessReasonResolve {
		t.Fatalf("resolve failure = %q, want %q", got, model.AccessReasonResolve)
	}
}

func TestAccessRecorderNormalizesAndBuckets(t *testing.T) {
	sink := &captureSink{}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	recorder := newAccessRecorder(nil, sink, loc)
	recorder.setEnabled(true)
	recorder.start()
	t.Cleanup(recorder.shutdown)

	// 2026-11-14T16:30:00Z is 2026-11-15 00:30 in Shanghai: the accounting day
	// comes from the effective zone, not from UTC.
	started := time.Date(2026, 11, 14, 16, 30, 0, 0, time.UTC).Unix()
	recorder.submit(model.AccessRecord{
		ClientID: 1, InboundID: 2, Network: "tcp",
		Host: "  Example.COM.  ", Port: 443, Outcome: model.AccessAllowed,
		StartedAt: started,
	})
	recorder.flushNow()

	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("sink received %d records", len(records))
	}
	if records[0].Host != "example.com" {
		t.Fatalf("host = %q, want example.com", records[0].Host)
	}
	if records[0].Day != "2026-11-15" {
		t.Fatalf("day = %q, want 2026-11-15", records[0].Day)
	}
}

func TestAccessRecorderSinkFailureIsLogged(t *testing.T) {
	sink := &captureSink{err: errors.New("disk full")}
	recorder := newAccessRecorder(nil, sink, time.UTC)
	recorder.setEnabled(true)
	recorder.start()
	t.Cleanup(recorder.shutdown)

	recorder.submit(model.AccessRecord{ClientID: 1, Host: "example.com", StartedAt: time.Now().Unix()})
	// flushNow must return even though the sink fails; a failing analytics
	// write never blocks the data plane.
	recorder.flushNow()
}

// TestDisabledRecordingAllocatesNothing guards the promise that analytics costs
// the data plane nothing while it is off: every per-connection and per-packet
// call must return before formatting an address or touching a map.
func TestDisabledRecordingAllocatesNothing(t *testing.T) {
	env := newTestEnv(t)
	if env.mgr.AccessEnabled() {
		t.Fatal("this test needs recording to start disabled")
	}
	destination := M.Socksaddr{Fqdn: "example.com", Port: 443}
	allocs := testing.AllocsPerRun(200, func() {
		entry := env.mgr.beginAccess(1, 1, "tcp", destination)
		entry.addUp(1)
		entry.finish()
	})
	if allocs != 0 {
		t.Fatalf("disabled TCP path allocates %v times per call", allocs)
	}

	var entry *accessEntry
	allocs = testing.AllocsPerRun(200, func() {
		entry.allowAddr(netipAddrPort(t, "127.0.0.1:53").Addr())
	})
	if allocs != 0 {
		t.Fatalf("disabled TCP allow path allocates %v times per call", allocs)
	}

	var udp *udpAccess
	allocs = testing.AllocsPerRun(200, func() {
		udp.addUp(netipAddrPort(t, "127.0.0.1:53"), destination, 1)
		udp.recordBlocked(destination, model.AccessReasonPolicy)
	})
	if allocs != 0 {
		t.Fatalf("disabled UDP path allocates %v times per call", allocs)
	}
}

func TestAccessEntryIsNilSafe(t *testing.T) {
	var entry *accessEntry
	// The disabled path calls every method through a nil entry; none may panic
	// and none may allocate a record.
	entry.allow("127.0.0.1")
	entry.block(model.AccessReasonPolicy)
	entry.fail(model.AccessReasonDial)
	entry.addUp(10)
	entry.addDown(20)
	entry.finish()

	var udp *udpAccess
	udp.addUp(netipAddrPort(t, "127.0.0.1:53"), udpTarget("example.com", 53), 10)
	udp.addDown(netipAddrPort(t, "127.0.0.1:53"), 10)
	udp.recordBlocked(udpTarget("blocked.example", 80), model.AccessReasonPolicy)
	udp.finishAll()
}

func TestBeginAccessReturnsNilWhenDisabled(t *testing.T) {
	env := newTestEnv(t)
	if entry := env.mgr.beginAccess(1, 1, "tcp", M.Socksaddr{Fqdn: "example.com", Port: 443}); entry != nil {
		t.Fatalf("expected no entry while recording is off, got %+v", entry)
	}
	if access := env.mgr.newUDPAccess(1, 1); access != nil {
		t.Fatalf("expected no UDP tracker while recording is off, got %+v", access)
	}
}

func TestAccessReasonFor(t *testing.T) {
	cases := map[error]string{
		ErrClientDisabled:  model.AccessReasonDisabled,
		ErrClientExpired:   model.AccessReasonExpired,
		ErrQuotaExceeded:   model.AccessReasonQuota,
		ErrIPLimit:         model.AccessReasonIPLimit,
		ErrTooManySessions: model.AccessReasonSessions,
		ErrUnknownClient:   model.AccessReasonPolicy,
	}
	for err, want := range cases {
		if got := accessReasonFor(err); got != want {
			t.Fatalf("accessReasonFor(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestUDPAccessDestinationCap(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.SetAccessEnabled(true)
	recorder := env.mgr.access
	tracker := env.mgr.newUDPAccess(1, 1)
	for i := 0; i < maxDestinations+5; i++ {
		addr := netipAddrPort(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(1000+i)))
		tracker.observe(addr, M.Socksaddr{})
	}
	if got := len(tracker.targets); got != maxDestinations {
		t.Fatalf("tracked %d destinations, want the cap %d", got, maxDestinations)
	}
	if recorder.droppedCount() < 5 {
		t.Fatalf("expected the overflow to be counted, dropped = %d", recorder.droppedCount())
	}
	tracker.finishAll()
}

// TestAccessRecorderCountsRecordsLostAtShutdown guards the promise behind
// dropped_records: a connection that finishes after the loop has left used to
// submit into a channel nobody reads, so the record vanished without being
// counted.
func TestAccessRecorderCountsRecordsLostAtShutdown(t *testing.T) {
	sink := &captureSink{}
	recorder := newAccessRecorder(nil, sink, time.UTC)
	recorder.setEnabled(true)
	recorder.start()
	recorder.shutdown()

	recorder.submit(model.AccessRecord{ClientID: 1, Host: "example.com", StartedAt: time.Now().Unix()})
	if got := recorder.droppedCount(); got != 1 {
		t.Fatalf("dropped = %d, want the post-shutdown record counted", got)
	}
}

// TestAccessRecorderDrainsOnShutdown checks the other half: what was queued
// before the stop is persisted rather than being counted as lost.
func TestAccessRecorderDrainsOnShutdown(t *testing.T) {
	sink := &captureSink{}
	recorder := newAccessRecorder(nil, sink, time.UTC)
	recorder.setEnabled(true)
	recorder.start()
	for i := 0; i < 10; i++ {
		recorder.submit(model.AccessRecord{ClientID: int64(i + 1), Host: "example.com", StartedAt: time.Now().Unix()})
	}
	recorder.shutdown()

	if got := len(sink.all()); got != 10 {
		t.Fatalf("persisted %d records, want 10", got)
	}
	if got := recorder.droppedCount(); got != 0 {
		t.Fatalf("dropped = %d, want 0", got)
	}
}

// TestDisablingRecordingFlushesLiveTCPEntry covers the switch-off path for a
// connection that is still running: its bytes are reported once, and traffic
// that moves after the switch is not attributed to the access page.
func TestDisablingRecordingFlushesLiveTCPEntry(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour,
	})
	mgr.access.start()
	t.Cleanup(mgr.access.shutdown)
	mgr.SetAccessEnabled(true)

	entry := mgr.beginAccess(7, 3, "tcp", M.Socksaddr{Fqdn: "example.com", Port: 443})
	if entry == nil {
		t.Fatal("expected an entry while recording is on")
	}
	entry.allowAddr(netip.MustParseAddr("93.184.216.34"))
	entry.addUp(100)
	entry.addDown(200)

	mgr.SetAccessEnabled(false)
	mgr.FlushAccess()

	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("switch-off emitted %d records, want 1: %+v", len(records), records)
	}
	if records[0].UpBytes != 100 || records[0].DownBytes != 200 {
		t.Fatalf("switch-off record = %+v", records[0])
	}
	if records[0].Host != "example.com" || records[0].Outcome != model.AccessAllowed {
		t.Fatalf("switch-off record lost its destination or outcome: %+v", records[0])
	}

	// Bytes moved after the switch belong to nobody, and the deferred finish
	// must not emit a second row.
	entry.addUp(5)
	entry.finish()
	mgr.FlushAccess()
	if got := len(sink.all()); got != 1 {
		t.Fatalf("a muted entry reported again: %d records", got)
	}
	mgr.access.liveMu.Lock()
	live := len(mgr.access.live)
	mgr.access.liveMu.Unlock()
	if live != 0 {
		t.Fatalf("%d entries still tracked after the switch", live)
	}
}

// TestReenablingRecordingResumesLiveUDPSession covers a NAT session that outlives
// the switch: turning recording back on has to resume reporting for it instead
// of leaving it silent until the session dies.
func TestReenablingRecordingResumesLiveUDPSession(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC, UDPTimeout: time.Hour,
		FlushInterval: time.Hour, EnforceInterval: time.Hour,
	})
	mgr.access.start()
	t.Cleanup(mgr.access.shutdown)
	mgr.SetAccessEnabled(true)
	tracker := mgr.newUDPAccess(1, 1)
	if tracker == nil {
		t.Fatal("expected a tracker while recording is on")
	}
	target := netipAddrPort(t, "127.0.0.1:53")

	tracker.addUp(target, udpTarget("example.com", 53), 10)
	mgr.SetAccessEnabled(false)
	mgr.FlushAccess()
	tracker.addUp(target, udpTarget("example.com", 53), 100)
	mgr.SetAccessEnabled(true)
	tracker.addUp(target, udpTarget("example.com", 53), 5)
	tracker.finishAll()
	mgr.FlushAccess()

	total := int64(0)
	for _, record := range sink.all() {
		total += record.UpBytes
	}
	if total != 15 {
		t.Fatalf("reported %d bytes, want 15 (10 before the switch, 5 after): %+v", total, sink.all())
	}
}

// TestUDPReplyBeforeSendKeepsRequestedHost covers a reply that arrives before the
// send path reports the request: the entry is created from the resolved address
// first, and the panel still has to show the name the client asked for.
func TestUDPReplyBeforeSendKeepsRequestedHost(t *testing.T) {
	sink := &captureSink{}
	mgr := New(Options{
		AccessSink: sink, Location: time.UTC, UDPTimeout: time.Hour,
		FlushInterval: time.Hour, EnforceInterval: time.Hour,
	})
	mgr.access.start()
	t.Cleanup(mgr.access.shutdown)
	mgr.SetAccessEnabled(true)
	tracker := mgr.newUDPAccess(1, 1)
	target := netipAddrPort(t, "127.0.0.1:53")

	tracker.addDown(target, 10)
	tracker.addUp(target, udpTarget("dns.example.com", 53), 20)
	tracker.finishAll()
	mgr.FlushAccess()

	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("emitted %d records, want 1: %+v", len(records), records)
	}
	if records[0].Host != "dns.example.com" {
		t.Fatalf("host = %q, want the requested name", records[0].Host)
	}
	if records[0].IP != "127.0.0.1" {
		t.Fatalf("ip = %q, want the resolved address", records[0].IP)
	}
}

// TestEnabledUDPPacketPathStopsAllocatingAfterFirstPacket covers the hot path
// while recording is ON: a destination is created once, and every later packet
// of the same NAT target must only touch counters. The upgrade path that labels
// a destination with the requested name is the one thing that could allocate per
// packet, so it returns before formatting once the entry carries a name.
func TestEnabledUDPPacketPathStopsAllocatingAfterFirstPacket(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.SetAccessEnabled(true)
	tracker := env.mgr.newUDPAccess(1, 1)
	t.Cleanup(tracker.finishAll)
	target := netipAddrPort(t, "127.0.0.1:53")
	requested := udpTarget("dns.example.com", 53)

	// The first packet creates the entry and normalizes the name.
	tracker.addUp(target, requested, 1)
	if entry := tracker.targets[target]; entry == nil || entry.host != "dns.example.com" {
		t.Fatalf("first packet did not create a named entry: %+v", tracker.targets[target])
	}

	allocs := testing.AllocsPerRun(200, func() {
		tracker.addUp(target, requested, 1)
	})
	if allocs != 0 {
		t.Fatalf("a recorded UDP packet allocates %v times after the first one", allocs)
	}
	allocs = testing.AllocsPerRun(200, func() {
		tracker.addDown(target, 1)
	})
	if allocs != 0 {
		t.Fatalf("a recorded UDP reply allocates %v times", allocs)
	}
}

// TestUDPObserveStillUpgradesAnAddressFirstEntry checks the other side of that
// fast path: an entry created from a resolved address before the send path
// reports the request is upgraded exactly once, and stays named afterwards.
func TestUDPObserveStillUpgradesAnAddressFirstEntry(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.SetAccessEnabled(true)
	tracker := env.mgr.newUDPAccess(1, 1)
	t.Cleanup(tracker.finishAll)
	target := netipAddrPort(t, "127.0.0.1:53")

	tracker.addDown(target, 5)
	entry := tracker.targets[target]
	if entry == nil || entry.host != "127.0.0.1" {
		t.Fatalf("reply-first entry = %+v, want the address", entry)
	}
	if entry.named.Load() {
		t.Fatal("an entry created from an address must not claim a name")
	}
	tracker.addUp(target, udpTarget("dns.example.com", 53), 5)
	if entry.host != "dns.example.com" || !entry.named.Load() {
		t.Fatalf("entry after the request arrived = host %q named %v", entry.host, entry.named.Load())
	}
	// A later packet with a different name does not rewrite it: the first
	// request for a NAT destination is what the client meant.
	tracker.addUp(target, udpTarget("other.example", 53), 5)
	if entry.host != "dns.example.com" {
		t.Fatalf("host was rewritten to %q", entry.host)
	}
}

// TestDropWarningIsRateLimitedOnce checks the shape of the drop warning: a burst
// of drops logs once, and the counter still sees every record. The rate limit is
// what keeps a stalled recorder from turning analytics into a log flood.
func TestDropWarningIsRateLimitedOnce(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	recorder := newAccessRecorder(logger, &captureSink{}, time.UTC)
	recorder.setEnabled(true)
	// No start(): the queue stays undrained, so every record past its capacity
	// is dropped.
	for i := 0; i < accessQueueSize+500; i++ {
		recorder.submit(model.AccessRecord{ClientID: int64(i + 1), Host: "example.com"})
	}
	if got := recorder.droppedCount(); got != 500 {
		t.Fatalf("dropped = %d, want 500", got)
	}
	warnings := strings.Count(buf.String(), "access records dropped")
	if warnings != 1 {
		t.Fatalf("logged %d drop warnings in one burst, want 1", warnings)
	}
}
