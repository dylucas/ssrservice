package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ydong/ssrservice/internal/auth"
)

// recoveryCodeCount reads the unused code count straight from the table. The
// store has no count method: nothing in the product needs the number, so the
// tests assert the rows themselves.
func recoveryCodeCount(t *testing.T, st *Store, adminID int64) int {
	t.Helper()
	var count int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM admin_recovery_codes WHERE admin_id = ?`, adminID).Scan(&count); err != nil {
		t.Fatalf("count recovery codes: %v", err)
	}
	return count
}

// TestAdminTOTPColumnsDefaultOff pins the upgrade path: an administrator created
// by an older build must read back with two-step verification off, not "on with
// an empty secret".
func TestAdminTOTPColumnsDefaultOff(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.AdminByID(admin.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.TOTPEnabled || got.TOTPSecret != "" || got.TOTPPendingSecret != "" || got.TOTPLastStep != 0 {
		t.Fatalf("unexpected new admin totp state: %+v", got)
	}
}

func TestAdminTOTPPendingAndEnable(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.SetAdminTOTPPending(admin.ID, "PENDINGSECRET"); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	got, _ := st.AdminByID(admin.ID)
	if got.TOTPPendingSecret != "PENDINGSECRET" {
		t.Fatalf("pending secret = %q", got.TOTPPendingSecret)
	}
	// Enrollment is not active yet: login must still be single factor.
	if got.TOTPEnabled || got.TOTPSecret != "" {
		t.Fatalf("pending enrollment leaked into the active state: %+v", got)
	}

	codes, err := auth.GenerateRecoveryCodes(3)
	if err != nil {
		t.Fatalf("codes: %v", err)
	}
	hashes := make([]string, 0, len(codes))
	for _, code := range codes {
		hashes = append(hashes, auth.HashRecoveryCode(code))
	}
	if err := st.EnableAdminTOTP(admin.ID, "PENDINGSECRET", 4242, hashes); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, _ = st.AdminByID(admin.ID)
	if !got.TOTPEnabled || got.TOTPSecret != "PENDINGSECRET" || got.TOTPPendingSecret != "" || got.TOTPLastStep != 4242 {
		t.Fatalf("unexpected enabled state: %+v", got)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 3 {
		t.Fatalf("recovery codes left = %d, want 3", left)
	}

	if err := st.DisableAdminTOTP(admin.ID); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ = st.AdminByID(admin.ID)
	if got.TOTPEnabled || got.TOTPSecret != "" || got.TOTPLastStep != 0 {
		t.Fatalf("disable left state behind: %+v", got)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 0 {
		t.Fatalf("disable left %d recovery codes", left)
	}
}

// TestConsumeAdminTOTPStepIsSingleUse checks both the sequential and the
// concurrent case: the conditional UPDATE is what makes a code single use, and a
// race between two submissions must still admit exactly one.
func TestConsumeAdminTOTPStepIsSingleUse(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.EnableAdminTOTP(admin.ID, "SECRET", 100, nil); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if ok, err := st.ConsumeAdminTOTPStep(admin.ID, 100); err != nil || ok {
		t.Fatalf("step at the recorded mark accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ConsumeAdminTOTPStep(admin.ID, 99); err != nil || ok {
		t.Fatalf("older step accepted: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ConsumeAdminTOTPStep(admin.ID, 101); err != nil || !ok {
		t.Fatalf("fresh step rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ConsumeAdminTOTPStep(admin.ID, 101); err != nil || ok {
		t.Fatalf("replayed step accepted: ok=%v err=%v", ok, err)
	}

	// Concurrent submissions of the same fresh step: exactly one wins.
	const racers = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
		errCh = make(chan error, racers)
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := st.ConsumeAdminTOTPStep(admin.ID, 500)
			if err != nil {
				errCh <- err
				return
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent consume: %v", err)
	}
	if wins != 1 {
		t.Fatalf("%d concurrent consumers won, want exactly 1", wins)
	}
}

func TestConsumeAdminTOTPRecoveryCode(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	codes, err := auth.GenerateRecoveryCodes(2)
	if err != nil {
		t.Fatalf("codes: %v", err)
	}
	hashes := []string{auth.HashRecoveryCode(codes[0]), auth.HashRecoveryCode(codes[1])}
	if err := st.EnableAdminTOTP(admin.ID, "SECRET", 1, hashes); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// A normalized variant of the same code is the same code.
	hash := auth.HashRecoveryCode(" " + codes[0] + " ")
	if ok, err := st.ConsumeAdminTOTPRecoveryCode(admin.ID, hash); err != nil || !ok {
		t.Fatalf("first use rejected: ok=%v err=%v", ok, err)
	}
	if ok, err := st.ConsumeAdminTOTPRecoveryCode(admin.ID, hash); err != nil || ok {
		t.Fatalf("replay accepted: ok=%v err=%v", ok, err)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 1 {
		t.Fatalf("recovery codes left = %d, want 1", left)
	}
	// An unknown code consumes nothing.
	if ok, err := st.ConsumeAdminTOTPRecoveryCode(admin.ID, auth.HashRecoveryCode("ZZZZZZZZZZZZ")); err != nil || ok {
		t.Fatalf("unknown code accepted: ok=%v err=%v", ok, err)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 1 {
		t.Fatalf("failed consume changed the count to %d", left)
	}
}

// TestReplaceAdminTOTPRecoveryCodesInvalidatesOldSet covers rotation: the old
// codes must stop working the moment the new set is installed.
func TestReplaceAdminTOTPRecoveryCodesInvalidatesOldSet(t *testing.T) {
	st := newTestStore(t)
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	oldCodes, _ := auth.GenerateRecoveryCodes(2)
	oldHashes := []string{auth.HashRecoveryCode(oldCodes[0]), auth.HashRecoveryCode(oldCodes[1])}
	if err := st.EnableAdminTOTP(admin.ID, "SECRET", 1, oldHashes); err != nil {
		t.Fatalf("enable: %v", err)
	}
	newCodes, _ := auth.GenerateRecoveryCodes(2)
	newHashes := []string{auth.HashRecoveryCode(newCodes[0]), auth.HashRecoveryCode(newCodes[1])}
	if err := st.ReplaceAdminTOTPRecoveryCodes(admin.ID, newHashes); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if ok, _ := st.ConsumeAdminTOTPRecoveryCode(admin.ID, oldHashes[0]); ok {
		t.Fatal("an old recovery code still works after rotation")
	}
	if ok, _ := st.ConsumeAdminTOTPRecoveryCode(admin.ID, newHashes[0]); !ok {
		t.Fatal("a new recovery code does not work")
	}
	if err := st.ReplaceAdminTOTPRecoveryCodes(admin.ID+999, newHashes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replace for an unknown admin = %v, want ErrNotFound", err)
	}
}

// TestAdminTOTPUnknownAdmin checks that the mutators report ErrNotFound instead
// of silently succeeding on a missing row.
func TestAdminTOTPUnknownAdmin(t *testing.T) {
	st := newTestStore(t)
	const missing = 4242
	if err := st.SetAdminTOTPPending(missing, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAdminTOTPPending = %v", err)
	}
	if err := st.EnableAdminTOTP(missing, "x", 1, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EnableAdminTOTP = %v", err)
	}
	if err := st.DisableAdminTOTP(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DisableAdminTOTP = %v", err)
	}
}

// TestRecoveryCodesCascadeWithAdmin keeps the foreign key honest: deleting the
// administrator removes the codes rather than leaving orphans that a later id
// reuse could inherit.
func TestRecoveryCodesCascadeWithAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	admin, err := st.CreateAdmin("admin", "hash", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	codes, _ := auth.GenerateRecoveryCodes(2)
	if err := st.EnableAdminTOTP(admin.ID, "SECRET", 1, []string{
		auth.HashRecoveryCode(codes[0]), auth.HashRecoveryCode(codes[1]),
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM admins WHERE id = ?`, admin.ID); err != nil {
		t.Fatalf("delete admin: %v", err)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 0 {
		t.Fatalf("orphan recovery codes left = %d", left)
	}
}

// TestAdminTOTPMigrationUpgradesAnExistingDatabase pins the upgrade path for a
// deployment that already has the first six migrations applied: opening it must
// add the TOTP columns, keep the existing administrator readable, and leave
// two-step verification off.
func TestAdminTOTPMigrationUpgradesAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	// The pre-TOTP shape of the tables this test needs, plus the migration ledger
	// saying 1..6 are already applied.
	statements := []string{
		`CREATE TABLE admins (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			must_change_password INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			last_login_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`,
		`INSERT INTO admins(username, password_hash, must_change_password, created_at)
			VALUES('admin', 'hash', 0, 1)`,
	}
	for _, version := range []int{1, 2, 3, 4, 5, 6} {
		statements = append(statements, fmt.Sprintf(
			`INSERT INTO schema_migrations(version, applied_at) VALUES(%d, 1)`, version))
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare old database: %v\nstatement: %s", err, statement)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded store: %v", err)
	}
	defer st.Close()

	admin, err := st.AdminByUsername("admin")
	if err != nil {
		t.Fatalf("load upgraded admin: %v", err)
	}
	if admin.PasswordHash != "hash" || admin.TOTPEnabled || admin.TOTPSecret != "" || admin.TOTPLastStep != 0 {
		t.Fatalf("unexpected upgraded admin: %+v", admin)
	}
	// The new table exists and the new columns accept writes.
	if err := st.SetAdminTOTPPending(admin.ID, "NEWSECRET"); err != nil {
		t.Fatalf("set pending after upgrade: %v", err)
	}
	if left := recoveryCodeCount(t, st, admin.ID); left != 0 {
		t.Fatalf("count after upgrade = %d", left)
	}
	// Reopening must be a no-op: 007 is recorded, not reapplied.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded store: %v", err)
	}
	defer st2.Close()
	if admin2, err := st2.AdminByUsername("admin"); err != nil || admin2.TOTPPendingSecret != "NEWSECRET" {
		t.Fatalf("state did not survive reopening: %+v (%v)", admin2, err)
	}
}
