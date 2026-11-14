package ssserver

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
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
