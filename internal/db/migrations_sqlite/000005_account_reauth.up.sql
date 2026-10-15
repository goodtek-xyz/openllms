-- SQLite: account email + re-link flag (mirrors Postgres 000013_account_reauth).
-- ADD COLUMN with NULL default only rewrites the schema entry, not the rows.
ALTER TABLE accounts ADD COLUMN email TEXT NULL;
ALTER TABLE accounts ADD COLUMN reauth_required_at TEXT NULL;
