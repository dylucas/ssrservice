package service

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// newImportEnv builds a Service over a temp store with one inbound.
func newImportEnv(t *testing.T, method string) (*Service, *store.Store, model.Inbound) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "import.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	mgr := ssserver.New(ssserver.Options{
		Sink: st, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: 10 * time.Second,
		Policy: ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	svc := New(st, mgr, cfg, nil)
	t.Cleanup(func() { svc.Stop() })

	serverPSK := ""
	if model.RequiresServerPSK(method) {
		// Exactly 16 bytes, base64: the method requires a 16 byte key.
		serverPSK = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	}
	in := model.Inbound{Remark: "import", Listen: "127.0.0.1", Port: freePortForTest(t),
		Method: method, ServerPSK: serverPSK, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if err := svc.reloadInbound(in.ID); err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	return svc, st, in
}

// freePortForTest returns a loopback port that is free for TCP and UDP.
func freePortForTest(t *testing.T) int {
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
	t.Fatal("could not find a free port")
	return 0
}

// TestImportClientsCSV covers header detection, auto PSK generation and a
// duplicate-name failure without aborting the batch.
func TestImportClientsCSV(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	// Seed one user so a duplicate can be attempted.
	if _, err := svc.CreateClient(in.ID, ClientInput{Name: "existing", Enabled: true}); err != nil {
		t.Fatalf("seed client: %v", err)
	}
	before := svc.ReloadCount()

	content := "name,psk,enabled,quota_bytes,expires_at,ip_limit,reset_policy,reset_day,note\n" +
		"alice,,true,1024,0,2,none,0,laptop\n" +
		"bob,,false,0,0,0,monthly,1,\n" +
		"existing,,true,0,0,0,none,0,\n"
	result, err := svc.ImportClients(in.ID, ImportRequest{Format: ImportFormatAuto, Content: content})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 2 {
		t.Fatalf("created = %d, want 2 (result %+v)", result.Created, result)
	}
	if len(result.Failed) != 1 || !strings.Contains(result.Failed[0].Error, "already exists") {
		t.Fatalf("unexpected failures: %+v", result.Failed)
	}
	if result.Failed[0].Name != "existing" {
		t.Fatalf("failure names the wrong row: %+v", result.Failed[0])
	}

	clients, err := st.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(clients) != 3 {
		t.Fatalf("stored %d clients, want 3", len(clients))
	}
	byName := make(map[string]model.Client, len(clients))
	for _, c := range clients {
		byName[c.Name] = c
	}
	if byName["alice"].PSK == "" || byName["alice"].QuotaBytes != 1024 || byName["alice"].IPLimit != 2 {
		t.Fatalf("alice was imported incorrectly: %+v", byName["alice"])
	}
	if byName["alice"].Note != "laptop" {
		t.Fatalf("note not imported: %+v", byName["alice"])
	}
	if byName["bob"].Enabled {
		t.Fatalf("bob should be disabled: %+v", byName["bob"])
	}
	if byName["bob"].ResetPolicy != model.ResetMonthly || byName["bob"].ResetDay != 1 {
		t.Fatalf("reset policy not imported: %+v", byName["bob"])
	}
	// Every imported user gets a subscription token.
	for _, c := range clients {
		if c.SubToken == "" {
			t.Fatalf("client %q has no subscription token", c.Name)
		}
	}
	// The batch reloads the inbound exactly once, not once per created user.
	if got := svc.ReloadCount() - before; got != 1 {
		t.Fatalf("import reloaded the inbound %d times, want 1", got)
	}
}

// TestImportClientsFromSSLinks imports ss:// links and rejects a method mismatch.
func TestImportClientsFromSSLinks(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	serverPSK := in.ServerPSK
	clientPSK := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210"))
	matching := "ss://" + b64url("2022-blake3-aes-128-gcm:"+serverPSK+":"+clientPSK) +
		"@203.0.113.5:8388#alice"
	mismatched := "ss://" + b64url("aes-256-gcm:secret") + "@203.0.113.5:8388#bob"

	result, err := svc.ImportClients(in.ID, ImportRequest{Content: matching + "\n" + mismatched})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 {
		t.Fatalf("created = %d, want 1 (%+v)", result.Created, result)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected one failure: %+v", result.Failed)
	}
	if result.Failed[0].Name != "bob" {
		t.Fatalf("failure names the wrong row: %+v", result.Failed[0])
	}

	clients, _ := st.ListClients(in.ID)
	if len(clients) != 1 || clients[0].Name != "alice" || clients[0].PSK != clientPSK {
		t.Fatalf("ss import stored %+v", clients)
	}
}

// TestImportRejectsOversizedAndEmpty documents the input bounds.
func TestImportRejectsOversizedAndEmpty(t *testing.T) {
	svc, _, in := newImportEnv(t, model.Method2022AES128GCM)

	if _, err := svc.ImportClients(in.ID, ImportRequest{Content: "   \n\n"}); err == nil {
		t.Fatal("empty content must be rejected")
	}
	var big strings.Builder
	big.WriteString("name\n")
	for i := 0; i < maxImportLines+1; i++ {
		big.WriteString("user" + itoaService(i) + "\n")
	}
	if _, err := svc.ImportClients(in.ID, ImportRequest{Content: big.String()}); err == nil {
		t.Fatal("oversized batch must be rejected")
	} else if !IsValidation(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	if _, err := svc.ImportClients(9999, ImportRequest{Content: "name\nalice\n"}); err == nil {
		t.Fatal("missing inbound must be rejected")
	}
}

// TestImportSingleUserMethodGuard verifies the per-inbound single-user rule also
// applies to imports.
func TestImportSingleUserMethodGuard(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022ChaCha20)
	result, err := svc.ImportClients(in.ID, ImportRequest{
		Content: "name,psk\nalice,\nbob,\n",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 || len(result.Failed) != 1 {
		t.Fatalf("single user method imported %d (failures %+v)", result.Created, result.Failed)
	}
	clients, _ := st.ListClients(in.ID)
	if len(clients) != 1 {
		t.Fatalf("stored %d clients, want 1", len(clients))
	}
}

// TestExportImportRoundTrip exports the users of one inbound and imports them
// into a second inbound with the same method.
func TestExportImportRoundTrip(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	created := []string{"alice", "bob", "carol"}
	for _, name := range created {
		if _, err := svc.CreateClient(in.ID, ClientInput{Name: name, Enabled: true}); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	content, err := svc.ExportClientsCSV(in.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(content)).ReadAll()
	if err != nil {
		t.Fatalf("parse export: %v", err)
	}
	if len(records) != len(created)+1 {
		t.Fatalf("export has %d rows, want %d (+header)", len(records), len(created)+1)
	}
	header := strings.Join(records[0], ",")
	for _, column := range []string{"name", "psk", "quota_bytes", "expires_at", "reset_policy"} {
		if !strings.Contains(header, column) {
			t.Fatalf("export header is missing %q: %s", column, header)
		}
	}

	// Import into a second inbound.
	second := model.Inbound{Remark: "second", Listen: "127.0.0.1", Port: freePortForTest(t),
		Method: in.Method, ServerPSK: in.ServerPSK, Enabled: true}
	if err := st.CreateInbound(&second); err != nil {
		t.Fatalf("create second inbound: %v", err)
	}
	if err := svc.reloadInbound(second.ID); err != nil {
		t.Fatalf("reload second: %v", err)
	}

	result, err := svc.ImportClients(second.ID, ImportRequest{Format: ImportFormatCSV, Content: string(content)})
	if err != nil {
		t.Fatalf("import round trip: %v", err)
	}
	if result.Created != len(created) || len(result.Failed) != 0 {
		t.Fatalf("round trip created %d (failures %+v)", result.Created, result.Failed)
	}

	original, _ := st.ListClients(in.ID)
	imported, _ := st.ListClients(second.ID)
	if len(original) != len(imported) {
		t.Fatalf("client count mismatch: %d vs %d", len(original), len(imported))
	}
	psk := make(map[string]string, len(original))
	for _, c := range original {
		psk[c.Name] = c.PSK
	}
	for _, c := range imported {
		if psk[c.Name] != c.PSK {
			t.Fatalf("PSK for %q changed across export/import", c.Name)
		}
	}
}

func b64url(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func itoaService(n int) string {
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
