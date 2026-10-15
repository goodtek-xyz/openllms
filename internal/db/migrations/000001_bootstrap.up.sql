-- Bootstrap schema for llms. Domain tables arrive in later migrations.

CREATE TABLE IF NOT EXISTS schema_meta (
    key   text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_meta (key, value)
VALUES ('app', 'llms'), ('bootstrap_task', 'TASK-003')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
