package store

import (
	"database/sql"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

const adminColumns = `id, username, password_hash, must_change_password,
	totp_enabled, totp_secret, totp_pending_secret, totp_last_step,
	created_at, last_login_at`

func scanAdmin(row interface{ Scan(...any) error }) (*model.Admin, error) {
	var (
		admin      model.Admin
		mustChange int64
		totpOn     int64
	)
	if err := row.Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &mustChange,
		&totpOn, &admin.TOTPSecret, &admin.TOTPPendingSecret, &admin.TOTPLastStep,
		&admin.CreatedAt, &admin.LastLoginAt); err != nil {
		return nil, scanErr(err)
	}
	admin.MustChangePassword = mustChange != 0
	admin.TOTPEnabled = totpOn != 0
	return &admin, nil
}

// CreateAdmin inserts an administrator.
func (s *Store) CreateAdmin(username, passwordHash string, mustChange bool) (*model.Admin, error) {
	now := time.Now().Unix()
	must := 0
	if mustChange {
		must = 1
	}
	var id int64
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO admins(username, password_hash, must_change_password, created_at)
			VALUES(?, ?, ?, ?)`, username, passwordHash, must, now)
		if err != nil {
			return scanErr(err)
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return &model.Admin{
		ID:                 id,
		Username:           username,
		PasswordHash:       passwordHash,
		MustChangePassword: mustChange,
		CreatedAt:          now,
	}, nil
}

// AdminByUsername looks up an administrator by name.
func (s *Store) AdminByUsername(username string) (*model.Admin, error) {
	row := s.db.QueryRow(`SELECT `+adminColumns+` FROM admins WHERE username = ?`, username)
	return scanAdmin(row)
}

// AdminByID looks up an administrator by id.
func (s *Store) AdminByID(id int64) (*model.Admin, error) {
	row := s.db.QueryRow(`SELECT `+adminColumns+` FROM admins WHERE id = ?`, id)
	return scanAdmin(row)
}

// AdminCount returns the number of administrators.
func (s *Store) AdminCount() (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count)
	return count, err
}

// UpdateAdminPassword updates an administrator's password hash.
func (s *Store) UpdateAdminPassword(id int64, passwordHash string, mustChange bool) error {
	must := 0
	if mustChange {
		must = 1
	}
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE admins SET password_hash = ?, must_change_password = ? WHERE id = ?`,
			passwordHash, must, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TouchAdminLogin records a successful login.
func (s *Store) TouchAdminLogin(id int64) error {
	return s.write(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE admins SET last_login_at = ? WHERE id = ?`, time.Now().Unix(), id)
		return err
	})
}

// SetAdminTOTPPending stores a proposed TOTP secret. The active secret and the
// enabled flag are untouched: enrollment only takes effect once a live code
// proves the operator really added the account to an authenticator.
func (s *Store) SetAdminTOTPPending(id int64, secret string) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE admins SET totp_pending_secret = ? WHERE id = ?`, secret, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// EnableAdminTOTP makes a verified secret active and installs a fresh set of
// recovery codes in the same transaction: a half enrolled administrator (enabled
// without recovery codes, or the other way round) must never be observable.
func (s *Store) EnableAdminTOTP(id int64, secret string, lastStep int64, recoveryHashes []string) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE admins
			SET totp_secret = ?, totp_pending_secret = '', totp_enabled = 1, totp_last_step = ?
			WHERE id = ?`, secret, lastStep, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if err := replaceRecoveryCodes(tx, id, recoveryHashes); err != nil {
			return err
		}
		return nil
	})
}

// DisableAdminTOTP clears every TOTP artifact for the administrator.
func (s *Store) DisableAdminTOTP(id int64) error {
	return s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE admins
			SET totp_secret = '', totp_pending_secret = '', totp_enabled = 0, totp_last_step = 0
			WHERE id = ?`, id)
		if err != nil {
			return scanErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(`DELETE FROM admin_recovery_codes WHERE admin_id = ?`, id)
		return err
	})
}

// ConsumeAdminTOTPStep records that step was accepted and reports whether this
// call was the one that won. The UPDATE is conditional, so two concurrent
// submissions of the same code cannot both succeed: exactly one row changes.
func (s *Store) ConsumeAdminTOTPStep(id int64, step int64) (bool, error) {
	var consumed bool
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE admins SET totp_last_step = ?
			WHERE id = ? AND totp_last_step < ?`, step, id, step)
		if err != nil {
			return scanErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		consumed = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return consumed, nil
}

// ReplaceAdminTOTPRecoveryCodes swaps the recovery code set. It is a separate
// operation from enabling so an operator can rotate codes without re-enrolling.
func (s *Store) ReplaceAdminTOTPRecoveryCodes(id int64, recoveryHashes []string) error {
	return s.write(func(tx *sql.Tx) error {
		var exists int64
		if err := tx.QueryRow(`SELECT COUNT(*) FROM admins WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		return replaceRecoveryCodes(tx, id, recoveryHashes)
	})
}

// ConsumeAdminTOTPRecoveryCode deletes one recovery code and reports whether it
// existed. The DELETE is the consumption: a replay finds no row.
func (s *Store) ConsumeAdminTOTPRecoveryCode(id int64, codeHash string) (bool, error) {
	var consumed bool
	err := s.write(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM admin_recovery_codes WHERE admin_id = ? AND code_hash = ?`, id, codeHash)
		if err != nil {
			return scanErr(err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		consumed = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return consumed, nil
}

// replaceRecoveryCodes rewrites the code set inside an existing transaction.
func replaceRecoveryCodes(tx *sql.Tx, id int64, hashes []string) error {
	if _, err := tx.Exec(`DELETE FROM admin_recovery_codes WHERE admin_id = ?`, id); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, hash := range hashes {
		if _, err := tx.Exec(`INSERT INTO admin_recovery_codes(admin_id, code_hash, created_at) VALUES(?, ?, ?)`,
			id, hash, now); err != nil {
			return scanErr(err)
		}
	}
	return nil
}

// RecoveryCodeHashes returns the stored hashes of an administrator's unused
// recovery codes. They travel in a configuration snapshot so a migrated TOTP
// account keeps its backup codes instead of being locked out.
func (s *Store) RecoveryCodeHashes(adminID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT code_hash FROM admin_recovery_codes WHERE admin_id = ? ORDER BY code_hash`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hashes := make([]string, 0, 10)
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}
	return hashes, rows.Err()
}
