package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ydong/ssrservice/internal/auth"
	"github.com/ydong/ssrservice/internal/config"
	"github.com/ydong/ssrservice/internal/model"
	"github.com/ydong/ssrservice/internal/ssserver"
	"github.com/ydong/ssrservice/internal/store"
)

// newConfigEnv builds a service with a seeded administrator and a known
// session secret, so a round trip can check what did and did not move.
func newConfigEnv(t *testing.T) (*Service, *store.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/config.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	mgr := ssserver.New(ssserver.Options{
		Sink: st, AccessSink: st, Location: time.UTC,
		FlushInterval: time.Hour, EnforceInterval: time.Hour, UDPTimeout: 10 * time.Second,
		Policy: ssserver.PolicyConfig{AllowPrivateTargets: true},
	})
	svc := New(st, mgr, cfg, nil)
	t.Cleanup(func() { svc.Stop() })

	// Seed the settings the way New()+seedSettings would, including a session
	// secret, so the round trip can check it survived untouched.
	if err := st.SetSettings(map[string]string{
		model.SettingSessionSecret:       "target-session-secret-0123456789abcdef",
		model.SettingPublicHost:          "",
		model.SettingPublicPort:          "0",
		model.SettingSubscriptionEnabled: "true",
		model.SettingAllowPrivate:        "false",
		model.SettingResetDay:            "1",
		model.SettingTimezone:            "UTC",
		model.SettingAccessLog:           "false",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := svc.loadSettings(); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	hash, err := auth.HashPassword("migration-password-123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	admin, err := st.CreateAdmin("admin", hash, false)
	if err != nil {
		t.Fatalf("admin: %v", err)
	}
	return svc, st, admin.ID
}

// configPSK returns a base64 key of the required size, for fixtures.
func configPSK(size int) string {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// TestConfigRoundTrip exports a configured instance, imports it into a fresh
// one and checks that every credential and setting survived.
func TestConfigRoundTrip(t *testing.T) {
	source, sourceStore, adminID := newConfigEnv(t)

	// A couple of settings different from the defaults.
	settings := source.Settings()
	settings.PublicHost = "node.example.com"
	settings.Timezone = "Asia/Shanghai"
	settings.ResetDay = 5
	if err := source.UpdateSettings(settings); err != nil {
		t.Fatalf("settings: %v", err)
	}

	// Two inbounds: a multi-user one with two users, a single-user one.
	multi := model.Inbound{Remark: "main", Listen: "127.0.0.1", Port: 28311,
		Method: model.Method2022AES128GCM, ServerPSK: configPSK(16), Enabled: true, UDPEnabled: true}
	if err := sourceStore.CreateInbound(&multi); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	solo := model.Inbound{Remark: "solo", Listen: "127.0.0.1", Port: 28312,
		Method: model.Method2022ChaCha20, Enabled: true}
	if err := sourceStore.CreateInbound(&solo); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	alice := model.Client{InboundID: multi.ID, Name: "alice", PSK: configPSK(16),
		Enabled: true, SubToken: "tok-alice", QuotaBytes: 1024, UpBytes: 100, DownBytes: 200}
	bob := model.Client{InboundID: multi.ID, Name: "bob", PSK: configPSK(16),
		Enabled: true, SubToken: "tok-bob"}
	carol := model.Client{InboundID: solo.ID, Name: "carol", PSK: configPSK(32),
		Enabled: true, SubToken: "tok-carol"}
	for i := range []*model.Client{&alice, &bob, &carol} {
		c := []*model.Client{&alice, &bob, &carol}[i]
		if err := sourceStore.CreateClient(c); err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
	}

	// Enable TOTP so the migration has to carry its secret and backup codes.
	if err := sourceStore.EnableAdminTOTP(adminID, "JBSWY3DPEHPK3PXP", 59812345,
		[]string{"hash1", "hash2"}); err != nil {
		t.Fatalf("totp: %v", err)
	}

	snapshot, err := source.ExportConfig()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if snapshot.Version != model.ConfigSnapshotVersion {
		t.Fatalf("version = %d", snapshot.Version)
	}
	if len(snapshot.Inbounds) != 2 {
		t.Fatalf("exported %d inbounds, want 2", len(snapshot.Inbounds))
	}
	// The session secret must not travel.
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "session_secret") {
		t.Fatal("export leaked the session secret")
	}

	// Import into a fresh instance.
	target, targetStore, targetAdmin := newConfigEnv(t)
	result, err := target.ImportConfig(snapshot, true)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Inbounds != 2 || result.Clients != 3 || result.Reloads != 2 {
		t.Fatalf("unexpected import result %+v", result)
	}

	after, err := target.ExportConfig()
	if err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if after.Settings.PublicHost != "node.example.com" || after.Settings.Timezone != "Asia/Shanghai" ||
		after.Settings.ResetDay != 5 {
		t.Fatalf("settings did not survive: %+v", after.Settings)
	}
	if after.Admin.PasswordHash != snapshot.Admin.PasswordHash ||
		after.Admin.TOTPSecret != snapshot.Admin.TOTPSecret ||
		after.Admin.TOTPLastStep != snapshot.Admin.TOTPLastStep {
		t.Fatalf("admin credentials did not survive: %+v", after.Admin)
	}
	if len(after.Admin.RecoveryCodeHashes) != 2 {
		t.Fatalf("recovery codes did not survive: %+v", after.Admin.RecoveryCodeHashes)
	}
	if len(after.Inbounds) != 2 || len(after.Inbounds[0].Clients) != 2 || len(after.Inbounds[1].Clients) != 1 {
		t.Fatalf("structure did not survive: %+v", after.Inbounds)
	}
	// PSKs and subscription tokens are the whole point of a config migration.
	if after.Inbounds[0].Clients[0].PSK != alice.PSK ||
		after.Inbounds[0].Clients[1].SubToken != bob.SubToken ||
		after.Inbounds[1].ServerPSK != solo.ServerPSK {
		t.Fatalf("credentials did not survive: %+v", after.Inbounds)
	}
	if after.Inbounds[0].Clients[0].UpBytes != 100 {
		t.Fatalf("usage counters did not survive: %+v", after.Inbounds[0].Clients[0])
	}

	// The session secret stays with the target, and the target session still
	// works: only the password hash changed, which the session version covers.
	targetSecret, err := targetStore.GetSetting(model.SettingSessionSecret)
	if err != nil || targetSecret == "" {
		t.Fatalf("target lost its session secret: %q %v", targetSecret, err)
	}
	admin, err := targetStore.AdminByID(targetAdmin)
	if err != nil || admin.PasswordHash != snapshot.Admin.PasswordHash {
		t.Fatalf("target admin not replaced: %v %+v", err, admin)
	}
}

// TestImportRequiresConfirm covers the guard that prevents an accidental wipe.
func TestImportRequiresConfirm(t *testing.T) {
	svc, st, _ := newConfigEnv(t)
	in := model.Inbound{Remark: "keep", Listen: "127.0.0.1", Port: 28313,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	snapshot, err := svc.ExportConfig()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := svc.ImportConfig(snapshot, false); err == nil {
		t.Fatal("import without confirm succeeded")
	}
	// The existing inbound is untouched.
	if _, err := st.InboundByID(in.ID); err != nil {
		t.Fatalf("existing inbound was removed: %v", err)
	}
}

// TestImportValidationRefusesEverything covers the "all or nothing" rule: each
// bad snapshot is refused, and the current configuration stays intact.
func TestImportValidationRefusesEverything(t *testing.T) {
	svc, st, _ := newConfigEnv(t)
	in := model.Inbound{Remark: "keep", Listen: "127.0.0.1", Port: 28314,
		Method: model.Method2022AES128GCM, Enabled: true}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	good, err := svc.ExportConfig()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	// Give the snapshot two clients with distinct tokens so the duplicate-token
	// case has something to mutate; the other cases do not care.
	good.Inbounds = append(good.Inbounds, model.ConfigInbound{
		Remark: "with-users", Listen: "127.0.0.1", Port: 28320,
		Method: model.Method2022AES128GCM, ServerPSK: configPSK(16), Enabled: true,
		Clients: []model.ConfigClient{
			{Name: "u1", PSK: configPSK(16), Enabled: true, SubToken: "tok-u1"},
			{Name: "u2", PSK: configPSK(16), Enabled: true, SubToken: "tok-u2"},
		},
	})

	cases := map[string]func(*model.ConfigSnapshot){
		"unknown version": func(s *model.ConfigSnapshot) { s.Version = 999 },
		"empty timezone":  func(s *model.ConfigSnapshot) { s.Settings.Timezone = "" },
		"bad timezone": func(s *model.ConfigSnapshot) {
			s.Settings.Timezone = "Mars/Olympus"
		},
		"bad public port": func(s *model.ConfigSnapshot) { s.Settings.PublicPort = 99999 },
		"no password":     func(s *model.ConfigSnapshot) { s.Admin.PasswordHash = "" },
		"totp no secret": func(s *model.ConfigSnapshot) {
			s.Admin.TOTPEnabled = true
			s.Admin.TOTPSecret = ""
		},
		"duplicate port": func(s *model.ConfigSnapshot) {
			s.Inbounds = append(s.Inbounds, s.Inbounds[0])
		},
		"duplicate token": func(s *model.ConfigSnapshot) {
			for i := range s.Inbounds {
				if len(s.Inbounds[i].Clients) > 1 {
					s.Inbounds[i].Clients[1].SubToken = s.Inbounds[i].Clients[0].SubToken
					return
				}
			}
			t.Fatal("fixture has no inbound with two clients")
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bad := good
			bad.Inbounds = append([]model.ConfigInbound{}, good.Inbounds...)
			for i := range bad.Inbounds {
				bad.Inbounds[i].Clients = append([]model.ConfigClient{}, good.Inbounds[i].Clients...)
			}
			mutate(&bad)
			_, err := svc.ImportConfig(bad, true)
			if err == nil {
				t.Fatalf("%s: import succeeded", name)
			}
			if !IsValidation(err) {
				t.Fatalf("%s: expected a validation error, got %v", name, err)
			}
			// Nothing was written: the original inbound and settings survive.
			stored, err := st.InboundByID(in.ID)
			if err != nil || stored.Remark != "keep" {
				t.Fatalf("%s: existing config changed: %+v %v", name, stored, err)
			}
		})
	}
}

// TestImportValidationFields pins that invalid fields are reported by name, so
// the panel can point at the exact entry that needs fixing.
func TestImportValidationFields(t *testing.T) {
	svc, _, _ := newConfigEnv(t)
	snapshot, err := svc.ExportConfig()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	snapshot.Settings.Timezone = "Not/AZone"
	_, err = svc.ImportConfig(snapshot, true)
	if err == nil {
		t.Fatal("invalid timezone accepted")
	}
	var validation *ValidationError
	if !errorsAs(err, &validation) || validation.Fields["settings.timezone"] == "" {
		t.Fatalf("fields not reported: %v", err)
	}
}

// errorsAs is a tiny helper so tests do not have to import errors in both forms.
func errorsAs(err error, target **ValidationError) bool {
	for err != nil {
		if v, ok := err.(*ValidationError); ok {
			*target = v
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestImportReplacesRuntimeListeners is the regression for a bug found by the
// smoke test: applying the imported inbounds without first removing the old
// listeners left the old ones holding the ports, so every import failed with
// port_in_use (409). The import has to restore a working listener, not just rows.
func TestImportReplacesRuntimeListeners(t *testing.T) {
	svc, st, _ := newConfigEnv(t)
	in := model.Inbound{Remark: "listener", Listen: "127.0.0.1", Port: 28330,
		Method: model.Method2022AES128GCM, ServerPSK: configPSK(16), Enabled: true}
	if _, err := svc.CreateInbound(InboundInput{
		Remark: in.Remark, Listen: in.Listen, Port: in.Port, Method: in.Method,
		ServerPSK: in.ServerPSK, Enabled: true,
	}); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	// Change a field so the imported snapshot differs from the running set.
	snapshot, err := svc.ExportConfig()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	snapshot.Inbounds[0].Remark = "renamed after export"
	snapshot.Inbounds[0].Port = 28331

	result, err := svc.ImportConfig(snapshot, true)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Reloads != 1 || len(result.Warnings) != 0 {
		t.Fatalf("listener did not restart cleanly: %+v", result)
	}
	// The import recreates rows, so the id changes; find the listener by its
	// new port, which is what the runtime actually bound.
	all, err := st.ListInbounds()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || all[0].Port != 28331 || all[0].Remark != "renamed after export" {
		t.Fatalf("inbound not replaced: %+v", all)
	}
}
