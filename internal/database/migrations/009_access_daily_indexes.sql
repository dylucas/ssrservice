-- The rollup's primary key is (day, client_id, inbound_id, host), which serves
-- the window scan every summary starts with. The two filters the page offers by
-- id are not prefixes of that key, so without these indexes they fall back to
-- scanning the whole window: measured at 26-28 ms for a client filter over a
-- 90 day window, against 6-8 ms when the index is available.
--
-- Keep this file free of semicolons outside statement terminators: the migration
-- runner splits the script on every semicolon.
CREATE INDEX IF NOT EXISTS idx_access_daily_client ON access_daily(client_id, day);
CREATE INDEX IF NOT EXISTS idx_access_daily_inbound ON access_daily(inbound_id, day);
