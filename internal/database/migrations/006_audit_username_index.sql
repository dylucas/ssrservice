-- The audit page filters by operator (username), which had no index.
--
-- Without one, "how many rows did admin do" scanned the whole trail and then
-- counted the matches: 4.4ms at 50k rows and growing linearly with the 90 day
-- retention window. The index is (username, created_at) so the page query can
-- also walk one operator's rows in display order. The migration splitter is
-- semicolon based, so keep comments free of semicolons.
CREATE INDEX IF NOT EXISTS idx_audit_username ON audit_logs(username, created_at);
