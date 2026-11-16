package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/ydong/ssrservice/internal/model"
)

// ReplaceConfig installs a configuration snapshot in one transaction.
//
// The service validates the snapshot first; this function assumes it is sound
// and only cares about atomicity: an operator must never see a config where the
// new inbounds arrived but the old clients did not, or where settings changed
// but the admin credentials did not.
//
// The session secret is deliberately not in the snapshot's settings, so this
// only writes the seven panel keys. Keeping the existing secret means importing
// invalidates sessions whose password hash or two-step state changed, without
// forcing a restart of the panel's cookie signer.
func (s *Store) ReplaceConfig(snapshot model.ConfigSnapshot, adminID int64) error {
	return s.write(func(tx *sql.Tx) error {
		// Children before parents: clients reference inbounds.
		if _, err := tx.Exec(`DELETE FROM clients`); err != nil {
			return fmt.Errorf("clear clients: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM inbounds`); err != nil {
			return fmt.Errorf("clear inbounds: %w", err)
		}

		now := time.Now().Unix()
		for _, in := range snapshot.Inbounds {
			res, err := tx.Exec(`INSERT INTO inbounds(remark, listen, port, method, server_psk,
				domain, domain_port, udp_enabled, enabled, created_at, updated_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
				in.Remark, in.Listen, in.Port, in.Method, in.ServerPSK,
				in.Domain, in.DomainPort, boolInt(in.UDPEnabled), boolInt(in.Enabled),
				now, now)
			if err != nil {
				return fmt.Errorf("insert inbound %s: %w", in.Remark, err)
			}
			inboundID, err := res.LastInsertId()
			if err != nil {
				return err
			}
			for _, c := range in.Clients {
				policy := c.ResetPolicy
				if policy == "" {
					policy = model.ResetNone
				}
				if _, err := tx.Exec(`INSERT INTO clients(inbound_id, name, psk, enabled, quota_bytes,
					up_bytes, down_bytes, expires_at, ip_limit, reset_policy, reset_day,
					last_reset_at, sub_token, note, created_at, updated_at)
					VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					inboundID, c.Name, c.PSK, boolInt(c.Enabled), c.QuotaBytes,
					c.UpBytes, c.DownBytes, c.ExpiresAt, c.IPLimit, policy, c.ResetDay,
					c.LastResetAt, c.SubToken, c.Note, now, now); err != nil {
					return fmt.Errorf("insert client %s: %w", c.Name, err)
				}
			}
		}

		// Only the seven panel keys: an attacker-crafted snapshot cannot change
		// the session secret, and future settings are not silently reset.
		settings := map[string]string{
			model.SettingPublicHost:          snapshot.Settings.PublicHost,
			model.SettingPublicPort:          fmt.Sprintf("%d", snapshot.Settings.PublicPort),
			model.SettingSubscriptionEnabled: boolString(snapshot.Settings.SubscriptionEnabled),
			model.SettingAllowPrivate:        boolString(snapshot.Settings.AllowPrivateTargets),
			model.SettingResetDay:            fmt.Sprintf("%d", snapshot.Settings.ResetDay),
			model.SettingTimezone:            snapshot.Settings.Timezone,
			model.SettingAccessLog:           boolString(snapshot.Settings.AccessLogEnabled),
		}
		for key, value := range settings {
			if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}

		// Administrator credentials: password hash and two-step state travel
		// together so the migrated instance uses the same login.
		if _, err := tx.Exec(`UPDATE admins SET username = ?, password_hash = ?,
			must_change_password = ?, totp_enabled = ?, totp_secret = ?,
			totp_pending_secret = '', totp_last_step = ? WHERE id = ?`,
			snapshot.Admin.Username, snapshot.Admin.PasswordHash,
			boolInt(snapshot.Admin.MustChangePassword), boolInt(snapshot.Admin.TOTPEnabled),
			snapshot.Admin.TOTPSecret, snapshot.Admin.TOTPLastStep, adminID); err != nil {
			return fmt.Errorf("update admin: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM admin_recovery_codes WHERE admin_id = ?`, adminID); err != nil {
			return fmt.Errorf("clear recovery codes: %w", err)
		}
		for _, hash := range snapshot.Admin.RecoveryCodeHashes {
			if _, err := tx.Exec(`INSERT INTO admin_recovery_codes(admin_id, code_hash, created_at)
				VALUES(?, ?, ?)`, adminID, hash, now); err != nil {
				return fmt.Errorf("insert recovery code: %w", err)
			}
		}
		return nil
	})
}

// boolString formats a boolean as the "true"/"false" strings settings use.
func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
