package ssserver

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/store"
)

// testEnv is a complete server harness for integration tests.
type testEnv struct {
	t      *testing.T
	mgr    *Manager
	store  *store.Store
	dir    string
	echoV4 *echoServer
	echoV6 *echoServer
	port   int
}

type echoServer struct {
	ln   net.Listener
	addr string
}

func newEchoServer(t *testing.T) *echoServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = copyBuf(c, c)
			}(conn)
		}
	}()
	return &echoServer{ln: ln, addr: ln.Addr().String()}
}

func copyBuf(dst net.Conn, src net.Conn) (int64, error) {
	buf := make([]byte, 4096)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err != nil {
			return total, nil
		}
	}
}

// freePort returns a port that is free for both TCP and UDP on loopback.
func freePort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			continue
		}
		udp.Close()
		return port
	}
	t.Fatal("could not find a free TCP/UDP port")
	return 0
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mgr := New(Options{
		Sink:            st,
		AccessSink:      st,
		Location:        time.UTC,
		FlushInterval:   time.Hour,
		EnforceInterval: time.Hour,
		UDPTimeout:      2 * time.Second,
		Policy:          PolicyConfig{AllowPrivateTargets: true},
	})
	env := &testEnv{
		t:      t,
		mgr:    mgr,
		store:  st,
		dir:    dir,
		echoV4: newEchoServer(t),
		port:   freePort(t),
	}
	t.Cleanup(func() {
		mgr.Stop()
		env.echoV4.ln.Close()
		st.Close()
		os.RemoveAll(dir)
	})
	return env
}

// createInbound inserts and starts an inbound with the given clients.
func (e *testEnv) createInbound(method string, serverPSK string, clients ...model.Client) model.Inbound {
	e.t.Helper()
	in := model.Inbound{
		Remark:     "test",
		Listen:     "127.0.0.1",
		Port:       e.port,
		Method:     method,
		ServerPSK:  serverPSK,
		UDPEnabled: true,
		Enabled:    true,
	}
	if err := e.store.CreateInbound(&in); err != nil {
		e.t.Fatalf("create inbound: %v", err)
	}
	for i := range clients {
		clients[i].InboundID = in.ID
		clients[i].ResetPolicy = model.ResetNone
		if err := e.store.CreateClient(&clients[i]); err != nil {
			e.t.Fatalf("create client: %v", err)
		}
	}
	stored, err := e.store.ListClients(in.ID)
	if err != nil {
		e.t.Fatalf("list clients: %v", err)
	}
	if err := e.mgr.ApplyInbound(in, stored); err != nil {
		e.t.Fatalf("apply inbound: %v", err)
	}
	return in
}

func (e *testEnv) addr() string {
	return fmt.Sprintf("127.0.0.1:%d", e.port)
}

// waitFor waits until fn returns true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
