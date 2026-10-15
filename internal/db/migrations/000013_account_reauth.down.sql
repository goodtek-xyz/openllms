-- +migrate Down
-- Drops captured account emails and pending re-link flags. Both are recoverable:
-- email by re-link (or id_token decode), the flag on the next upstream 401.
ALTER TABLE accounts
    DROP COLUMN IF EXISTS reauth_required_at,
    DROP COLUMN IF EXISTS email;
