-- Per-inbound share-link domain.
--
-- A deployment with several inbounds could previously only pick one host for all
-- of them through the global public_host setting. These two columns let an inbound
-- point share links and subscriptions at its own hostname. An empty domain keeps
-- the previous behaviour exactly. The migration splitter is semicolon based, so
-- keep comments free of semicolons.
ALTER TABLE inbounds ADD COLUMN domain TEXT NOT NULL DEFAULT '';

-- Optional port override for the domain. 0 means "fall back to the global
-- public_port, then to the inbound port".
ALTER TABLE inbounds ADD COLUMN domain_port INTEGER NOT NULL DEFAULT 0;
