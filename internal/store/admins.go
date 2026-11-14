package store

import (
	"database/sql"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

const adminColumns = `id, username, password_hash, must_change_password, created_at, last_login_at`

func scanAdmin(row interface{ Scan(...any) error }) (*model.Admin, error) {
	var (
		admin      model.Admin
		mustChange int64
	)
	if err := row.Scan(&admin.ID, &admin.Username, &admin.PasswordHash, &mustChange, &admin.CreatedAt, &admin.LastLoginAt); err != nil {
		return nil, scanErr(err)
	}
	admin.MustChangePassword = mustChange != 0
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
