-- Per-day traffic rollup used by the dashboard's 30 day chart.
--
-- The dashboard used to aggregate traffic_daily across every client on each
-- poll, which scanned ~155k rows for a 5000 client deployment to produce 31
-- numbers. This table keeps one row per day, maintained inside the same
-- transaction as the detail rows. The migration splitter is semicolon based, so
-- keep comments free of semicolons.
CREATE TABLE IF NOT EXISTS traffic_daily_totals (
    day TEXT PRIMARY KEY,
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0
);

-- Backfill from the existing detail rows. INSERT OR REPLACE is used instead of
-- INSERT ... ON CONFLICT because an upsert from a SELECT needs a WHERE clause to
-- parse unambiguously.
INSERT OR REPLACE INTO traffic_daily_totals(day, up_bytes, down_bytes)
    SELECT day, COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
    FROM traffic_daily GROUP BY day;
