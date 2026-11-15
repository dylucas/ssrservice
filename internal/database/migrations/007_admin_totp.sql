-- Per-administrator TOTP two-step verification.
--
-- The secret lives next to the password hash because it is a credential of the
-- same rank: whoever can read this database can already forge sessions and
-- impersonate the administrator. totp_pending_secret holds a secret whose
-- enrollment a client started but never confirmed, and login keeps using
-- totp_secret until the pending one is proven with a live code.
--
-- Keep this file free of semicolons outside statement terminators: the migration
-- runner splits the script on every semicolon.
ALTER TABLE admins ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE admins ADD COLUMN totp_pending_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE admins ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE admins ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- Recovery codes are single use, so only the hash is stored. The primary key
-- makes consumption an atomic DELETE whose RowsAffected says who won.
CREATE TABLE IF NOT EXISTS admin_recovery_codes (
    admin_id INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (admin_id, code_hash)
);
