-- +goose Up

-- Migrate kill state to timestamp column.
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS killed_at TIMESTAMP;
UPDATE feature_flags.flags SET killed_at = updated_at WHERE state = 'KILLED';

-- Drop legacy columns now replaced by conditions JSONB and killed_at.
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS state;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS value;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS layer;

-- Drop override table — overrides are now conditions in YAML config.
DROP TABLE IF EXISTS feature_flags.flag_overrides;

-- Index for kill-set polling query.
CREATE INDEX IF NOT EXISTS idx_flags_killed ON feature_flags.flags (killed_at) WHERE killed_at IS NOT NULL;

-- +goose Down

-- Recreate columns.
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS layer VARCHAR(50) NOT NULL DEFAULT 'GLOBAL';
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS value BYTEA;
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS state VARCHAR(20) NOT NULL DEFAULT 'DEFAULT';

-- Migrate killed_at back to state.
UPDATE feature_flags.flags SET state = 'KILLED' WHERE killed_at IS NOT NULL;

-- Drop killed_at.
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS killed_at;
DROP INDEX IF EXISTS feature_flags.idx_flags_killed;

-- Recreate overrides table.
CREATE TABLE IF NOT EXISTS feature_flags.flag_overrides (
    flag_id TEXT NOT NULL REFERENCES feature_flags.flags(flag_id),
    entity_id TEXT NOT NULL,
    state VARCHAR(20) NOT NULL DEFAULT 'ENABLED' CHECK (state IN ('ENABLED', 'DISABLED')),
    value BYTEA,
    created_at TIMESTAMP NOT NULL DEFAULT now(),
    updated_at TIMESTAMP NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_id, entity_id)
);
