-- Proxy access analytics: which destinations each client asked for, whether
-- the request was allowed, and how much data moved.
--
-- Raw events are short lived (7 days) and carry only the destination host, the
-- port, the resolved address and byte counts -- never payloads, URLs, or the
-- client's source address. The daily rollup keeps 90 days of counts so the
-- panel can describe a long window without retaining individual records.
--
-- The rollup is keyed by (day, client, inbound, host) because those are the only
-- dimensions the overview filters on. The outcome is stored as three counters
-- rather than as a key column: keeping it in the key multiplied the row count by
-- roughly four while every summary query had to fold it back with CASE WHEN.
-- Measured on a 90 day window (200 clients x 20 destinations) the wider key held
-- 1.44M rows and a summary cost 233-437 ms, against 360k rows and 60-110 ms for
-- this shape.
--
-- There is deliberately no foreign key to clients: deleting a user must not
-- cascade thousands of history rows inside one write transaction, and a record
-- whose user is gone is displayed as "deleted user" until retention removes it.
--
-- Keep this file free of semicolons outside statement terminators: the migration
-- runner splits the script on every semicolon.
CREATE TABLE IF NOT EXISTS access_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    client_id INTEGER NOT NULL,
    inbound_id INTEGER NOT NULL,
    network TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0
);

-- The list is newest first and every filter is applied on top of a time range,
-- so the range index carries most queries. Client and inbound each get their own
-- because those are the two filters the page offers by id: without the inbound
-- one, filtering by inbound counted 31.9 ms over 200k events instead of 2.7 ms.
CREATE INDEX IF NOT EXISTS idx_access_events_started ON access_events(started_at);
CREATE INDEX IF NOT EXISTS idx_access_events_client ON access_events(client_id, started_at);
CREATE INDEX IF NOT EXISTS idx_access_events_inbound ON access_events(inbound_id, started_at);

CREATE TABLE IF NOT EXISTS access_daily (
    day TEXT NOT NULL,
    client_id INTEGER NOT NULL,
    inbound_id INTEGER NOT NULL,
    host TEXT NOT NULL,
    hits INTEGER NOT NULL DEFAULT 0,
    allowed INTEGER NOT NULL DEFAULT 0,
    blocked INTEGER NOT NULL DEFAULT 0,
    failed INTEGER NOT NULL DEFAULT 0,
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, client_id, inbound_id, host)
) WITHOUT ROWID;
