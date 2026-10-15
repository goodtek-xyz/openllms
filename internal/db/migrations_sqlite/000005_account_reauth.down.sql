-- SQLite >= 3.35 supports DROP COLUMN (modernc.org/sqlite bundles a newer release).
-- Drops captured account emails and pending re-link flags (recoverable, see Postgres down).
ALTER TABLE accounts DROP COLUMN reauth_required_at;
ALTER TABLE accounts DROP COLUMN email;
