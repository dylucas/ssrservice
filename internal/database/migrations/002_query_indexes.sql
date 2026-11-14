-- Indexes for the queries the panel and the background workers run often.
-- The migration splitter is semicolon based, so keep comments free of
-- semicolons.

-- DailyTotals filters traffic_daily by day across every client (dashboard and
-- the 30 day chart). The primary key starts with client_id so it cannot serve
-- that predicate.
CREATE INDEX IF NOT EXISTS idx_traffic_daily_day ON traffic_daily(day);

-- The monthly reset worker scans clients by reset policy and last reset time
-- once per minute.
CREATE INDEX IF NOT EXISTS idx_clients_reset ON clients(reset_policy, last_reset_at);
