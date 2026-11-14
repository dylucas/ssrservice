package panel

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/service"
)

// errorBody decodes the API error envelope.
func errorBody(t *testing.T, payload []byte) apiError {
	t.Helper()
	var envelope struct {
		Error apiError `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, payload)
	}
	return envelope.Error
}

// TestErrorResponsesDoNotEchoInternals pins the API contract: a 500 or 409 must
// not carry SQLite constraint names, table/column identifiers or filesystem
// paths.
//
// The previous default branch serialized err.Error() straight into the
// envelope, so a duplicate client name produced
// "UNIQUE constraint failed: clients.inbound_id, clients.name" and a failed
// backup produced the absolute temp path. Both leak internals to the browser,
// and the second leaks the server's filesystem layout.
func TestErrorResponsesDoNotEchoInternals(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "conflict", "listen": "127.0.0.1", "port": freeTestPort(t),
		"method": model.Method2022AES128GCM, "enabled": true,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)

	body := map[string]any{"name": "dup-user", "enabled": true}
	if rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", body, nil); rec.Code != http.StatusCreated {
		t.Fatalf("first client: %d %s", rec.Code, rec.Body.String())
	}
	rec = tp.do(http.MethodPost, "/api/v1/inbounds/"+itoa(in.ID)+"/clients", body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected conflict, got %d %s", rec.Code, rec.Body.String())
	}
	assertNoInternals(t, rec.Body.Bytes())
	apiErr := errorBody(t, rec.Body.Bytes())
	if !strings.Contains(apiErr.Message, "name") {
		t.Fatalf("conflict message should name the collided field: %q", apiErr.Message)
	}

	// An inbound that is physically present but has no matching row reaches the
	// 500 branch: the service tries to load it after the id passed parseID.
	rec = tp.do(http.MethodGet, "/api/v1/clients/999999", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing client should be 404, got %d", rec.Code)
	}

	// The backup endpoint builds a temporary file; a failure there must not
	// echo its path. TMPDIR pointing at a missing directory is the cheapest way
	// to force one.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	rec = tp.do(http.MethodGet, "/api/v1/backup", nil, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("backup unexpectedly succeeded with an unwritable TMPDIR")
	}
	assertNoInternals(t, rec.Body.Bytes())
}

// assertNoInternals fails when a response body contains schema or filesystem
// details that belong in the log, not in the API.
func assertNoInternals(t *testing.T, payload []byte) {
	t.Helper()
	text := string(payload)
	for _, leak := range []string{"UNIQUE constraint", "constraint failed", "clients.", "inbounds.", "sqlite", "os.", "/tmp/", "sspanel-backup-"} {
		if strings.Contains(text, leak) {
			t.Fatalf("response leaks %q: %s", leak, text)
		}
	}
	if strings.Contains(text, os.TempDir()) {
		t.Fatalf("response leaks the temp directory: %s", text)
	}
}

// TestInboundListSurfacesStartFailure checks that the API explains why an
// enabled inbound is not listening.
//
// Without it, the only trace of a failed bind was one log line at startup and
// the panel showed a healthy inbound that rejected every connection.
func TestInboundListSurfacesStartFailure(t *testing.T) {
	tp := newTestPanel(t)
	tp.login()

	port := freeTestPort(t)
	rec := tp.do(http.MethodPost, "/api/v1/inbounds", map[string]any{
		"remark": "will-fail", "listen": "127.0.0.1", "port": port,
		"method": model.Method2022AES128GCM, "enabled": false,
	}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	in := decode[model.Inbound](t, rec)

	// Simulate the startup path: the row is enabled but the address is gone by
	// the time the runtime tries to bind it.
	in.Enabled = true
	if err := tp.store.UpdateInbound(&in); err != nil {
		t.Fatalf("mark enabled: %v", err)
	}
	blocker, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skipf("could not occupy the port: %v", err)
	}
	defer blocker.Close()
	if err := tp.svc.LoadRuntime(); err == nil {
		t.Fatal("loading a blocked inbound must report the failure")
	}

	rec = tp.do(http.MethodGet, "/api/v1/inbounds", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []service.InboundView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected one inbound, got %d", len(list.Items))
	}
	view := list.Items[0]
	if !view.Enabled {
		t.Fatal("the row stays enabled so the operator can retry or fix the port")
	}
	if view.LastError == "" {
		t.Fatal("an enabled inbound that failed to bind must expose last_error")
	}
	assertNoInternals(t, []byte(view.LastError))

	// Once the port frees up, a successful apply clears the message.
	blocker.Close()
	if err := tp.svc.LoadRuntime(); err != nil {
		t.Fatalf("reload after freeing the port: %v", err)
	}
	rec = tp.do(http.MethodGet, "/api/v1/inbounds/"+itoa(in.ID), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get inbound: %d %s", rec.Code, rec.Body.String())
	}
	got := decode[service.InboundView](t, rec)
	if got.LastError != "" {
		t.Fatalf("last_error not cleared: %q", got.LastError)
	}
}
