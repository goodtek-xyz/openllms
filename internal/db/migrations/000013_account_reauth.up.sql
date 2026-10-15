-- +migrate Up
-- Expand step (expand/contract): additive, nullable, no default → metadata-only in
-- Postgres 11+ (no table rewrite; brief ACCESS EXCLUSIVE on accounts only).
--   email               vendor account email captured at link time (NULL = unknown).
--   reauth_required_at  set once when an OAuth account needs a human re-link; the
--                       account leaves routing while it is non-NULL. The NULL→now()
--                       transition (UPDATE … WHERE reauth_required_at IS NULL) is the
--                       single winner that sends notifications; re-link clears it.
-- Old gateway versions never select or write these columns.
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS email text NULL,
    ADD COLUMN IF NOT EXISTS reauth_required_at timestamptz NULL;
