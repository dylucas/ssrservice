package store

import (
	"database/sql"
)

// GetSetting returns one setting value.
func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		return "", scanErr(err)
	}
	return value, nil
}

// SetSettings upserts several settings in one transaction.
func (s *Store) SetSettings(values map[string]string) error {
	return s.write(func(tx *sql.Tx) error {
		for key, value := range values {
			if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// AllSettings returns every setting.
func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}
