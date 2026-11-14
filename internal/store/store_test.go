package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ydong/ssrservice/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	// Reopening must not fail.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
}

func TestAdminCRUD(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !admin.MustChangePassword || admin.ID == 0 {
		t.Fatalf("unexpected admin %+v", admin)
	}
	got, err := st.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.PasswordHash != "hash" {
		t.Fatalf("unexpected hash %q", got.PasswordHash)
	}
	if _, err := st.CreateAdmin("admin", "other", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if err := st.UpdateAdminPassword(admin.ID, "new", false); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = st.AdminByID(admin.ID)
	if got.PasswordHash != "new" || got.MustChangePassword {
		t.Fatalf("update did not apply: %+v", got)
	}
	if _, err := st.AdminByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestInboundAndClientLifecycle(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Remark: "ss", Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, ServerPSK: "abc", UDPEnabled: true, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("id not assigned")
	}
	dup := model.Inbound{Remark: "dup", Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	used, err := st.PortInUse("0.0.0.0", 8388, 0)
	if err != nil || !used {
		t.Fatalf("expected port in use, got %v %v", used, err)
	}

	client := model.Client{InboundID: in.ID, Name: "alice", PSK: "psk", Enabled: true, SubToken: "tok-1", QuotaBytes: 100}
	if err := st.CreateClient(&client); err != nil {
		t.Fatalf("create client: %v", err)
	}
	dupClient := model.Client{InboundID: in.ID, Name: "alice", PSK: "x", Enabled: true, SubToken: "tok-2"}
	if err := st.CreateClient(&dupClient); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected name conflict, got %v", err)
	}

	if err := st.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 30, DownBytes: 40,
		Day: "2026-01-02", DayUpBytes: 30, DayDownBytes: 40,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	got, _ := st.ClientByID(client.ID)
	if got.UpBytes != 30 || got.DownBytes != 40 {
		t.Fatalf("usage not persisted: %+v", got)
	}
	if got.UsedBytes() != 70 || got.RemainingBytes() != 30 {
		t.Fatalf("unexpected derived values: used=%d remaining=%d", got.UsedBytes(), got.RemainingBytes())
	}
	usage, err := st.DailyUsage(client.ID, "2026-01-01")
	if err != nil || len(usage) != 1 {
		t.Fatalf("daily usage: %v %v", usage, err)
	}
	// Upsert accumulates.
	if err := st.SaveUsage([]model.UsageSnapshot{{
		ClientID: client.ID, UpBytes: 35, DownBytes: 40,
		Day: "2026-01-02", DayUpBytes: 5,
	}}); err != nil {
		t.Fatalf("save usage: %v", err)
	}
	usage, _ = st.DailyUsage(client.ID, "2026-01-01")
	if usage[0].UpBytes != 35 {
		t.Fatalf("daily upsert failed: %+v", usage[0])
	}

	if err := st.DeleteInbound(in.ID); err != nil {
		t.Fatalf("delete inbound: %v", err)
	}
	if _, err := st.ClientByID(client.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clients should cascade, got %v", err)
	}
}

func TestClientHelpers(t *testing.T) {
	now := int64(1_000_000)
	active := model.Client{Enabled: true, QuotaBytes: 100, DownBytes: 10}
	if active.StatusAt(now) != model.StatusActive {
		t.Fatalf("unexpected status %q", active.StatusAt(now))
	}
	disabled := model.Client{Enabled: false}
	if disabled.StatusAt(now) != model.StatusDisabled {
		t.Fatalf("unexpected status %q", disabled.StatusAt(now))
	}
	expired := model.Client{Enabled: true, ExpiresAt: now - 1}
	if expired.StatusAt(now) != model.StatusExpired {
		t.Fatalf("unexpected status %q", expired.StatusAt(now))
	}
	over := model.Client{Enabled: true, QuotaBytes: 10, UpBytes: 10}
	if over.StatusAt(now) != model.StatusQuotaExceeded {
		t.Fatalf("unexpected status %q", over.StatusAt(now))
	}
	unlimited := &model.Client{}
	if unlimited.RemainingBytes() != -1 {
		t.Fatal("unlimited quota must return -1")
	}
}

func TestSettings(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.GetSetting("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := st.SetSettings(map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := st.SetSetting("a", "3"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, err := st.AllSettings()
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if all["a"] != "3" || all["b"] != "2" {
		t.Fatalf("unexpected settings %+v", all)
	}
}

func TestBackup(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateAdmin("admin", "hash", false); err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(path); err != nil {
		t.Fatalf("backup: %v", err)
	}
	copyStore, err := Open(path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copyStore.Close()
	count, err := copyStore.AdminCount()
	if err != nil || count != 1 {
		t.Fatalf("backup content missing: count=%d err=%v", count, err)
	}
}

func TestClientsForReset(t *testing.T) {
	st := newTestStore(t)
	in := model.Inbound{Listen: "0.0.0.0", Port: 8388, Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	monthly := model.Client{InboundID: in.ID, Name: "m", PSK: "x", Enabled: true, SubToken: "t1",
		ResetPolicy: model.ResetMonthly, ResetDay: 1, LastResetAt: 100}
	if err := st.CreateClient(&monthly); err != nil {
		t.Fatalf("client: %v", err)
	}
	none := model.Client{InboundID: in.ID, Name: "n", PSK: "y", Enabled: true, SubToken: "t2"}
	if err := st.CreateClient(&none); err != nil {
		t.Fatalf("client: %v", err)
	}
	candidates, err := st.ClientsForReset(200)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != monthly.ID {
		t.Fatalf("unexpected candidates %+v", candidates)
	}
}
