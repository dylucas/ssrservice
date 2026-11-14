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
		// The key length follows the method (16 or 32 bytes); hardcoding one
		// silently restricted the helper to the aes-128 method.
		generated, err := ssserver.GenerateServerPSK(method)
		if err != nil {
			t.Fatalf("server psk: %v", err)
		}
		serverPSK = generated
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

// TestSplitImportLinesKeepsOriginalNumbers guards the numbering of pasted
// import content.
//
// splitImportLines drops empty lines and returns the 1-based number of each
// surviving line in the original paste. The parsers used the index of the
// filtered slice instead, so "ss://ok\n\nss://bad" reported line 2 for a line
// that is the third one, while the comment and the API example promised the
// original number.
func TestSplitImportLinesKeepsOriginalNumbers(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []int
	}{
		{"simple", "a\nb\nc", []int{1, 2, 3}},
		{"blank between rows", "a\n\nb", []int{1, 3}},
		{"leading and trailing blanks", "\n\na\n\n", []int{3}},
		{"crlf", "a\r\n\r\nb", []int{1, 3}},
		{"whitespace only lines", "a\n   \n\t\nb", []int{1, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := splitImportLines(tc.content)
			got := make([]int, len(lines))
			for i, line := range lines {
				got[i] = line.Number
			}
			if len(got) != len(tc.want) {
				t.Fatalf("numbers = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("numbers = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestImportReportsOriginalLineNumbers is the end-to-end check: a failed row
// after a blank separator reports the line of the paste, not of the filtered
// slice. ImportClients trims the whole payload, so leading and trailing blank
// lines are removed before numbering; only interior blanks survive.
func TestImportReportsOriginalLineNumbers(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"blank line between rows", "{good}\n\n{bad}", 3},
		{"two blank lines between rows", "{good}\n\n\n{bad}", 4},
		{"crlf with a blank row", "{good}\r\n\r\n{bad}", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh inbound per case: names are unique inside one inbound, and
			// a shared environment would turn the second alice into a
			// duplicate-name failure instead of testing the line number.
			svc, _, in := newImportEnv(t, model.Method2022AES128GCM)
			serverPSK := in.ServerPSK
			clientPSK := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210"))
			good := "ss://" + b64url("2022-blake3-aes-128-gcm:"+serverPSK+":"+clientPSK) +
				"@203.0.113.5:8388#alice"
			bad := "ss://" + b64url("aes-256-gcm:secret") + "@203.0.113.5:8388#bob"
			content := strings.NewReplacer("{good}", good, "{bad}", bad).Replace(tc.content)
			result, err := svc.ImportClients(in.ID, ImportRequest{Format: ImportFormatSS, Content: content})
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if len(result.Failed) != 1 {
				t.Fatalf("expected one failure, got %+v", result.Failed)
			}
			if result.Failed[0].Line != tc.want {
				t.Fatalf("failure line = %d, want %d (%+v)", result.Failed[0].Line, tc.want, result.Failed[0])
			}
		})
	}
}

// TestImportCSVReportsOriginalLineNumbers is the CSV counterpart: the header
// line still counts, and a blank separator does not shift the numbers.
func TestImportCSVReportsOriginalLineNumbers(t *testing.T) {
	svc, _, in := newImportEnv(t, model.Method2022AES128GCM)
	content := "name,psk,enabled\n\nalice,," + "\n\n,\n"
	result, err := svc.ImportClients(in.ID, ImportRequest{Format: ImportFormatCSV, Content: content})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("expected one failure, got %+v", result.Failed)
	}
	if result.Failed[0].Line != 5 {
		t.Fatalf("failure line = %d, want 5 (%+v)", result.Failed[0].Line, result.Failed[0])
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

// TestImportClientsBulk imports a full-size batch and pins the properties the
// batched writer has to preserve: every row lands, every token differs, and the
// inbound is reloaded exactly once.
func TestImportClientsBulk(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	// The CSV header counts towards the line limit.
	const rows = maxImportLines - 1
	var content strings.Builder
	content.WriteString("name,psk\n")
	for i := 0; i < rows; i++ {
		content.WriteString("bulk-" + itoaService(i) + ",\n")
	}

	before := svc.ReloadCount()
	start := time.Now()
	result, err := svc.ImportClients(in.ID, ImportRequest{
		Format: ImportFormatCSV, Content: content.String(),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	t.Logf("imported %d clients in %s", result.Created, elapsed)
	if result.Created != rows || len(result.Failed) != 0 {
		t.Fatalf("created %d (failures %+v), want %d", result.Created, result.Failed, rows)
	}
	// One reload for the whole batch, not one per user.
	if got := svc.ReloadCount() - before; got != 1 {
		t.Fatalf("import reloaded the inbound %d times, want 1", got)
	}

	clients, err := st.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(clients) != rows {
		t.Fatalf("stored %d clients, want %d", len(clients), rows)
	}
	tokens := make(map[string]struct{}, len(clients))
	for _, c := range clients {
		if c.PSK == "" {
			t.Fatalf("client %q has no PSK", c.Name)
		}
		if c.SubToken == "" {
			t.Fatalf("client %q has no subscription token", c.Name)
		}
		if _, duplicate := tokens[c.SubToken]; duplicate {
			t.Fatalf("subscription token of %q was reused", c.Name)
		}
		tokens[c.SubToken] = struct{}{}
	}
}

// TestInsertImportRowsAttributesFailures verifies the bulk insert falls back to
// per-row statements when the database rejects a chunk, so a conflicting line is
// reported with its own line number and the other lines still land.
func TestInsertImportRowsAttributesFailures(t *testing.T) {
	svc, st, in := newImportEnv(t, model.Method2022AES128GCM)
	if _, err := svc.CreateClient(in.ID, ClientInput{Name: "taken", Enabled: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tokens, err := svc.uniqueTokens(2)
	if err != nil {
		t.Fatalf("tokens: %v", err)
	}
	makeRow := func(line int, name, token string) importRow {
		psk, err := ssserver.GenerateClientPSK(in.Method)
		if err != nil {
			t.Fatalf("psk: %v", err)
		}
		return importRow{line: line, client: model.Client{InboundID: in.ID, Name: name,
			PSK: psk, Enabled: true, SubToken: token, LastResetAt: model.NowUnix()}}
	}
	rows := []importRow{
		makeRow(2, "fresh", tokens[0]),
		makeRow(3, "taken", tokens[1]),
	}
	created, failed := svc.insertImportRows(rows)
	if created != 1 {
		t.Fatalf("created %d, want 1", created)
	}
	if len(failed) != 1 {
		t.Fatalf("expected exactly one failure, got %+v", failed)
	}
	if failed[0].Line != 3 || failed[0].Name != "taken" {
		t.Fatalf("failure points at the wrong line: %+v", failed[0])
	}
	stored, err := st.ListClients(in.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make(map[string]bool, len(stored))
	for _, c := range stored {
		names[c.Name] = true
	}
	if !names["fresh"] || !names["taken"] {
		t.Fatalf("unexpected stored clients: %v", names)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d clients, want 2", len(stored))
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
