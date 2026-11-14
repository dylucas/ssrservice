package ssserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/model"
)

// buildClientMethod constructs the client side method for a client.
func buildClientMethod(method, serverPSK, clientPSK string) (shadowsocks.Method, error) {
	if model.IsSS2022(method) {
		if model.IsMultiUser(method) {
			serverKey, err := base64.StdEncoding.DecodeString(serverPSK)
			if err != nil {
				return nil, err
			}
			userKey, err := base64.StdEncoding.DecodeString(clientPSK)
			if err != nil {
				return nil, err
			}
			return shadowaead_2022.New(method, [][]byte{serverKey, userKey}, time.Now)
		}
		return shadowaead_2022.NewWithPassword(method, clientPSK, time.Now)
	}
	return shadowaead.New(method, nil, clientPSK)
}

// dialSSWithPSK opens a Shadowsocks TCP session and returns the decrypted
// stream connected to destination.
func dialSSWithPSK(serverAddr, method, serverPSK, clientPSK, destination string) (net.Conn, error) {
	m, err := buildClientMethod(method, serverPSK, clientPSK)
	if err != nil {
		return nil, err
	}
	raw, err := net.DialTimeout("tcp", serverAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	target := M.ParseSocksaddr(destination)
	if !target.IsValid() {
		raw.Close()
		return nil, fmt.Errorf("invalid destination %q", destination)
	}
	conn, err := m.DialConn(raw, target)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

func newTestKey(t *testing.T, size int) string {
	t.Helper()
	key, err := auth.RandomBase64Key(size)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return key
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	var total int
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// echoRoundTrip writes payload and verifies the echo response.
func echoRoundTrip(t *testing.T, conn net.Conn, payload string) {
	t.Helper()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := readFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("unexpected echo %q, want %q", buf, payload)
	}
}

func TestSS2022MultiUserTCP(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	env.createInbound(model.Method2022AES128GCM, serverPSK,
		model.Client{Name: "alice", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok-a"},
		model.Client{Name: "bob", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok-b"},
	)
	clients, err := env.store.ListClients(0)
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	for _, c := range clients {
		conn, err := dialSSWithPSK(env.addr(), model.Method2022AES128GCM, serverPSK, c.PSK, env.echoV4.addr)
		if err != nil {
			t.Fatalf("dial %s: %v", c.Name, err)
		}
		echoRoundTrip(t, conn, "hello "+c.Name)
		conn.Close()
	}

	waitFor(t, 5*time.Second, "traffic counted", func() bool {
		a, _ := env.mgr.ClientTraffic(clients[0].ID)
		b, _ := env.mgr.ClientTraffic(clients[1].ID)
		return a > 0 && b > 0
	})
	if err := env.mgr.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitFor(t, 5*time.Second, "traffic persisted", func() bool {
		a, err := env.store.ClientByID(clients[0].ID)
		if err != nil {
			return false
		}
		b, err := env.store.ClientByID(clients[1].ID)
		if err != nil {
			return false
		}
		return a.UpBytes > 0 && b.UpBytes > 0
	})
	// Daily buckets must exist as well.
	since := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	usage, err := env.store.DailyUsage(clients[0].ID, since)
	if err != nil {
		t.Fatalf("daily usage: %v", err)
	}
	if len(usage) == 0 || usage[len(usage)-1].UpBytes == 0 {
		t.Fatalf("expected daily usage to be recorded, got %+v", usage)
	}
}

func TestWrongPSKRejected(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "alice", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	storedClients, _ := env.store.ListClients(in.ID)
	stored := storedClients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, newTestKey(t, 16), env.echoV4.addr)
	if err == nil {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Read(make([]byte, 1))
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected wrong PSK to be rejected")
	}
	up, _ := env.mgr.ClientTraffic(stored.ID)
	if up != 0 {
		t.Fatalf("expected no traffic for the real client, got %d", up)
	}
}

func TestLegacyAEADMultiUserTCP(t *testing.T) {
	env := newTestEnv(t)
	in := env.createInbound(model.MethodAES256GCM, "",
		model.Client{Name: "legacy-a", PSK: "password-a", Enabled: true, SubToken: "tok-a"},
		model.Client{Name: "legacy-b", PSK: "password-b", Enabled: true, SubToken: "tok-b"},
	)
	clients, _ := env.store.ListClients(in.ID)
	for _, c := range clients {
		conn, err := dialSSWithPSK(env.addr(), in.Method, "", c.PSK, env.echoV4.addr)
		if err != nil {
			t.Fatalf("dial %s: %v", c.Name, err)
		}
		echoRoundTrip(t, conn, "ping-"+c.Name)
		conn.Close()
	}
}

func TestSingleUser2022(t *testing.T) {
	env := newTestEnv(t)
	client := model.Client{Name: "solo", PSK: newTestKey(t, 32), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022ChaCha20, "", client)
	conn, err := dialSSWithPSK(env.addr(), in.Method, "", client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	echoRoundTrip(t, conn, "xyz")
}

func TestQuotaEnforced(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "limited", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok", QuotaBytes: 8}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write(make([]byte, 64))
	waitFor(t, 5*time.Second, "quota exceeded", func() bool {
		up, down := env.mgr.ClientTraffic(client.ID)
		return up+down >= 8
	})

	conn2, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err == nil {
		conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, rerr := conn2.Read(make([]byte, 1)); rerr == nil {
			t.Fatal("expected quota exceeded to reject new connection")
		}
		conn2.Close()
	}
	conn.Close()
}

func TestExpiredAndDisabledRejected(t *testing.T) {
	cases := []struct {
		name   string
		client model.Client
	}{
		{"expired", model.Client{Name: "old", Enabled: true, ExpiresAt: time.Now().Add(-time.Hour).Unix()}},
		{"disabled", model.Client{Name: "off", Enabled: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			serverPSK := newTestKey(t, 16)
			tc.client.PSK = newTestKey(t, 16)
			tc.client.SubToken = "tok"
			in := env.createInbound(model.Method2022AES128GCM, serverPSK, tc.client)
			clients, _ := env.store.ListClients(in.ID)
			client := clients[0]
			conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
			if err == nil {
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, rerr := conn.Read(make([]byte, 1)); rerr == nil {
					t.Fatalf("expected %s client to be rejected", tc.name)
				}
				conn.Close()
			}
		})
	}
}

func TestIPLimit(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "single-ip", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok", IPLimit: 1}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	first, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	defer first.Close()
	echoRoundTrip(t, first, "a")

	// The same source IP is allowed to open more sessions.
	second, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("second dial same ip: %v", err)
	}
	echoRoundTrip(t, second, "b")
	second.Close()

	// The registry enforces the limit for a different IP.
	if _, err := env.mgr.registry.acquire(client.ID, in.ID, "10.0.0.9", 1, 10); err == nil {
		t.Fatal("expected a second distinct IP to be rejected")
	}
}

func TestPrivateTargetBlocked(t *testing.T) {
	env := newTestEnv(t)
	env.mgr.SetPolicy(PolicyConfig{AllowPrivateTargets: false, BlockMetadata: true})
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "guarded", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, _ := env.store.ListClients(in.ID)
	client = clients[0]

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err == nil {
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, werr := conn.Write([]byte("blocked")); werr == nil {
			if _, rerr := conn.Read(make([]byte, 1)); rerr == nil {
				t.Fatal("expected loopback destination to be blocked")
			}
		}
	}
}

func TestHotReloadAddClient(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	in := env.createInbound(model.Method2022AES128GCM, serverPSK,
		model.Client{Name: "a", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok-a"})
	clients, _ := env.store.ListClients(in.ID)
	clientA := clients[0]

	// Hold an existing connection open across the reload.
	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, clientA.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer conn.Close()
	echoRoundTrip(t, conn, "1")

	clientB := model.Client{InboundID: in.ID, Name: "b", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok-b"}
	if err := env.store.CreateClient(&clientB); err != nil {
		t.Fatalf("create B: %v", err)
	}
	all, _ := env.store.ListClients(in.ID)
	if err := env.mgr.ApplyInbound(in, all); err != nil {
		t.Fatalf("reload: %v", err)
	}
	connB, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, clientB.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer connB.Close()
	echoRoundTrip(t, connB, "2")

	// The pre-existing connection still works.
	echoRoundTrip(t, conn, "3")
}

func TestPortConflictRollback(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	in := env.createInbound(model.Method2022AES128GCM, serverPSK,
		model.Client{Name: "a", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"})

	other := InboundSnapshot{
		ID:        in.ID + 100,
		Remark:    "conflict",
		Listen:    "127.0.0.1",
		Port:      in.Port,
		Method:    model.Method2022AES256GCM,
		ServerPSK: newTestKey(t, 32),
		Enabled:   true,
	}
	if err := env.mgr.apply(other); err == nil {
		t.Fatal("expected port conflict error")
	}
	clients, _ := env.store.ListClients(in.ID)
	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, clients[0].PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("original inbound stopped serving: %v", err)
	}
	echoRoundTrip(t, conn, "still-up")
	conn.Close()
}

func TestUDPSession(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "udp", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
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

	m, err := buildClientMethod(in.Method, serverPSK, client.PSK)
	if err != nil {
		t.Fatalf("client method: %v", err)
	}
	raw, err := net.Dial("udp", env.addr())
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer raw.Close()
	packetConn := m.DialPacketConn(raw)
	defer packetConn.Close()

	type response struct {
		data []byte
		err  error
	}
	ch := make(chan response, 1)
	go func() {
		buf := make([]byte, 2048)
		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			ch <- response{err: err}
			return
		}
		ch <- response{data: append([]byte(nil), buf[:n]...)}
	}()

	if _, err := packetConn.WriteTo([]byte("udp!"), echoConn.LocalAddr()); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	select {
	case resp := <-ch:
		if resp.err != nil {
			t.Fatalf("read packet: %v", resp.err)
		}
		if string(resp.data) != "udp!" {
			t.Fatalf("unexpected response %q", resp.data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for UDP response")
	}
	waitFor(t, 5*time.Second, "udp traffic counted", func() bool {
		up, _ := env.mgr.ClientTraffic(client.ID)
		return up >= 4
	})
}

func TestBlockedMetadataAddress(t *testing.T) {
	p := newPolicy(PolicyConfig{AllowPrivateTargets: true, BlockMetadata: true})
	if err := p.checkAddrPort(mustAddrPort("169.254.169.254:80")); err == nil {
		t.Fatal("metadata address must be blocked even with private targets allowed")
	}
	if err := p.checkAddrPort(mustAddrPort("100.100.100.200:80")); err == nil {
		t.Fatal("alibaba metadata address must be blocked")
	}
}

func mustAddrPort(s string) netip.AddrPort {
	parsed, err := parseAddrPort(s)
	if err != nil {
		panic(err)
	}
	return parsed
}

// ipv6LoopbackPort returns a port free for TCP and UDP on ::1.
func ipv6LoopbackPort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		ln, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		udp, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: port})
		if err != nil {
			continue
		}
		udp.Close()
		return port
	}
	t.Skip("no free IPv6 TCP/UDP port")
	return 0
}

func TestIPv6LoopbackInbound(t *testing.T) {
	port := ipv6LoopbackPort(t)
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)

	in := model.Inbound{
		Remark:     "ipv6",
		Listen:     "::1",
		Port:       port,
		Method:     model.Method2022AES128GCM,
		ServerPSK:  serverPSK,
		UDPEnabled: true,
		Enabled:    true,
	}
	if err := env.store.CreateInbound(&in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	client := model.Client{InboundID: in.ID, Name: "v6", PSK: newTestKey(t, 16),
		Enabled: true, SubToken: "tok-v6", ResetPolicy: model.ResetNone}
	if err := env.store.CreateClient(&client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	clients, _ := env.store.ListClients(in.ID)
	if err := env.mgr.ApplyInbound(in, clients); err != nil {
		t.Fatalf("apply inbound: %v", err)
	}

	if got := in.Addr(); got != fmt.Sprintf("[::1]:%d", port) {
		t.Fatalf("unexpected addrinfo %q", got)
	}
	conn, err := dialSSWithPSK(in.Addr(), in.Method, serverPSK, client.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial over IPv6: %v", err)
	}
	defer conn.Close()
	echoRoundTrip(t, conn, "hello over ipv6")
}

// TestUDPKickClosesSessionImmediately verifies that disconnecting a client
// tears down its UDP NAT session right away instead of leaving it parked until
// the protocol library's UDP timeout expires.
func TestUDPKickClosesSessionImmediately(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "udp-kick", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
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

	m, err := buildClientMethod(in.Method, serverPSK, client.PSK)
	if err != nil {
		t.Fatalf("client method: %v", err)
	}
	raw, err := net.Dial("udp", env.addr())
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}
	defer raw.Close()
	packetConn := m.DialPacketConn(raw)
	defer packetConn.Close()

	if _, err := packetConn.WriteTo([]byte("kick"), echoConn.LocalAddr()); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	waitFor(t, 5*time.Second, "udp session registered", func() bool {
		return env.mgr.SessionCount() > 0
	})

	env.mgr.CloseClient(client.ID)

	// The test environment uses a 2s UDP timeout, so a session that survives
	// for under a second can only have been closed explicitly.
	waitFor(t, 900*time.Millisecond, "udp session closed after kick", func() bool {
		return env.mgr.SessionCount() == 0
	})
}

// TestConcurrentReloadWhileServing hammers the hot reload path while a TCP
// session keeps relaying traffic. Run under -race it proves the inbound
// snapshot swap and the protocol service replacement are concurrency safe.
func TestConcurrentReloadWhileServing(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "live", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, err := env.store.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}

	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, clients[0].PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	echoRoundTrip(t, conn, "before-reload")

	stop := make(chan struct{})
	var (
		wg       sync.WaitGroup
		reloards sync.WaitGroup
	)

	// Keep the existing session busy while the configuration is swapped.
	wg.Add(1)
	go func() {
		defer wg.Done()
		payload := []byte("during-reload")
		buf := make([]byte, len(payload))
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(payload); err != nil {
				return
			}
			if _, err := readFull(conn, buf); err != nil {
				return
			}
		}
	}()

	// Several goroutines reload the same inbound concurrently.
	const reloaders = 4
	const reloads = 20
	for i := 0; i < reloaders; i++ {
		reloards.Add(1)
		go func() {
			defer reloards.Done()
			for j := 0; j < reloads; j++ {
				if err := env.mgr.ApplyInbound(in, clients); err != nil {
					t.Errorf("concurrent reload: %v", err)
					return
				}
			}
		}()
	}
	reloards.Wait()
	close(stop)
	wg.Wait()

	// The original connection must have survived every reload.
	echoRoundTrip(t, conn, "after-reload")
}

// inboundRuntimeOf returns the running runtime of an inbound.
func inboundRuntimeOf(t *testing.T, env *testEnv, id int64) *inboundRuntime {
	t.Helper()
	env.mgr.mu.Lock()
	defer env.mgr.mu.Unlock()
	rt := env.mgr.inbounds[id]
	if rt == nil {
		t.Fatalf("inbound %d is not running", id)
	}
	return rt
}

// TestHotReloadSkipsUnchangedProtocol verifies that a reload which only changes
// non-protocol fields (quota, name, note) keeps the running protocol service,
// while a PSK or enable change rebuilds it.
func TestHotReloadSkipsUnchangedProtocol(t *testing.T) {
	env := newTestEnv(t)
	serverPSK := newTestKey(t, 16)
	client := model.Client{Name: "live", PSK: newTestKey(t, 16), Enabled: true, SubToken: "tok"}
	in := env.createInbound(model.Method2022AES128GCM, serverPSK, client)
	clients, err := env.store.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}
	stored := clients[0]

	rt := inboundRuntimeOf(t, env, in.ID)
	before := rt.services.Load()
	if before == nil {
		t.Fatal("protocol service not published")
	}

	// Keep a session open across the reload: it must survive and keep serving.
	conn, err := dialSSWithPSK(env.addr(), in.Method, serverPSK, stored.PSK, env.echoV4.addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	echoRoundTrip(t, conn, "before")

	// Only non-protocol fields change.
	updated := stored
	updated.QuotaBytes = 1 << 30
	updated.IPLimit = 5
	updated.Name = "renamed"
	updated.Note = "note"
	if err := env.store.UpdateClient(&updated); err != nil {
		t.Fatalf("update client: %v", err)
	}
	reloaded, err := env.store.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}
	if err := env.mgr.ApplyInbound(in, reloaded); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after := rt.services.Load(); after != before {
		t.Fatal("non-protocol change rebuilt the protocol service")
	}
	echoRoundTrip(t, conn, "after-quota-change")

	// A PSK change must rebuild the service.
	rotated := updated
	rotated.PSK = newTestKey(t, 16)
	if err := env.store.UpdateClient(&rotated); err != nil {
		t.Fatalf("rotate psk: %v", err)
	}
	reloaded, _ = env.store.ListClients(in.ID)
	if err := env.mgr.ApplyInbound(in, reloaded); err != nil {
		t.Fatalf("reload after psk change: %v", err)
	}
	after := rt.services.Load()
	if after == before {
		t.Fatal("psk change did not rebuild the protocol service")
	}
	echoRoundTrip(t, conn, "after-psk-change")

	// Disabling the client also changes the protocol user table.
	disabled := rotated
	disabled.Enabled = false
	if err := env.store.UpdateClient(&disabled); err != nil {
		t.Fatalf("disable client: %v", err)
	}
	reloaded, _ = env.store.ListClients(in.ID)
	if err := env.mgr.ApplyInbound(in, reloaded); err != nil {
		t.Fatalf("reload after disable: %v", err)
	}
	if third := rt.services.Load(); third == after {
		t.Fatal("disabling the client did not rebuild the protocol service")
	}
}

// TestUDPWritePacketAddrPort exercises the AddressPort write fast path of the
// server packet writer against a real UDP socket.
func TestUDPWritePacketAddrPort(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen server: %v", err)
	}
	defer server.Close()
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()

	writer := &udpServerConn{conn: server}
	payload := []byte("addrport-fast-path")
	destination := M.SocksaddrFromNetIP(peer.LocalAddr().(*net.UDPAddr).AddrPort())
	if err := writer.WritePacket(buf.As(payload), destination); err != nil {
		t.Fatalf("write packet: %v", err)
	}

	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, 64)
	n, _, err := peer.ReadFromUDPAddrPort(buffer)
	if err != nil {
		t.Fatalf("read packet: %v", err)
	}
	if got := string(buffer[:n]); got != string(payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	// The buffer must have been released by WritePacket.
	if cap(payload) != len(payload) {
		// buf.As wraps the caller slice without pooling; this is just a guard
		// against the helper accidentally switching to a pooled buffer.
		t.Fatal("unexpected buffer ownership")
	}
}

// stubPacketConn is an inert N.PacketConn for session lifecycle tests.
type stubPacketConn struct{ closed int32 }

func (c *stubPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (c *stubPacketConn) WritePacket(*buf.Buffer, M.Socksaddr) error { return nil }
func (c *stubPacketConn) Close() error                               { atomic.StoreInt32(&c.closed, 1); return nil }
func (c *stubPacketConn) LocalAddr() net.Addr                        { return nil }
func (c *stubPacketConn) SetDeadline(time.Time) error                { return nil }
func (c *stubPacketConn) SetReadDeadline(time.Time) error            { return nil }
func (c *stubPacketConn) SetWriteDeadline(time.Time) error           { return nil }

// TestUDPSessionSocketReuse verifies that repeated sends to the same address
// family use one socket and that closing the session releases it.
func TestUDPSessionSocketReuse(t *testing.T) {
	env := newTestEnv(t)
	client := &stubPacketConn{}
	s := newUDPClientSession(context.Background(), env.mgr, client, 1)
	defer s.close()

	first, err := s.socket(true)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	second, err := s.socket(true)
	if err != nil {
		t.Fatalf("socket again: %v", err)
	}
	if first != second {
		t.Fatal("expected the IPv4 socket to be reused")
	}
	if s.sockets[socketIndex(true)].Load() != first {
		t.Fatal("socket not stored in the atomic slot")
	}
	s.close()
	if s.sockets[socketIndex(true)].Load() != nil {
		t.Fatal("close did not clear the socket slot")
	}
	if _, err := s.socket(true); err == nil {
		t.Fatal("expected socket creation to fail after close")
	}
	if atomic.LoadInt32(&client.closed) != 1 {
		t.Fatal("close did not close the protocol side of the NAT session")
	}
}
