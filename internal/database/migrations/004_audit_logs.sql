-- Write-operation audit trail.
--
-- Only panel mutations and login attempts are recorded. The detail column holds
-- an explicitly assembled key=value summary: credentials (password, client PSK,
-- server PSK, subscription token) are never written, so an audit export cannot
-- leak secrets. The migration splitter is semicolon based, so keep comments free
-- of semicolons.
CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    admin_id INTEGER NOT NULL DEFAULT 0,
    username TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id INTEGER NOT NULL DEFAULT 0,
    detail TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action, created_at);
