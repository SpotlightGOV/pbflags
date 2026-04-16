-- +goose Up

-- Singleton row representing the global config-sync freeze. When present,
-- pbflags-sync (and standalone admin file-watch sync) refuse to write. The
-- freeze is the "big red button" for the config-as-code pipeline during
-- incident response. Absence of the row = unlocked.
CREATE TABLE IF NOT EXISTS feature_flags.sync_freeze (
    id          INT          PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    actor       VARCHAR(255) NOT NULL,
    reason      TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Per-(flag, condition) value overrides. One row per overridden condition
-- on a flag, plus optionally one row per flag with condition_index IS NULL
-- which overrides the static/compiled default (flags with no conditions, or
-- the terminal "otherwise" fall-through).
CREATE TABLE IF NOT EXISTS feature_flags.condition_overrides (
    flag_id          VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id) ON DELETE CASCADE,
    condition_index  INT          NULL,
    override_value   BYTEA        NOT NULL,
    source           VARCHAR(20)  NOT NULL CHECK (source IN ('cli', 'ui')),
    actor            VARCHAR(255) NOT NULL,
    reason           TEXT         NOT NULL,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Two partial unique indexes: one row per (flag, condition) when the index is
-- set, and one static-default row per flag when the index is NULL.
CREATE UNIQUE INDEX IF NOT EXISTS condition_overrides_flag_cond
  ON feature_flags.condition_overrides (flag_id, condition_index)
  WHERE condition_index IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS condition_overrides_flag_default
  ON feature_flags.condition_overrides (flag_id)
  WHERE condition_index IS NULL;

CREATE INDEX IF NOT EXISTS idx_condition_overrides_created_at
  ON feature_flags.condition_overrides (created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS feature_flags.idx_condition_overrides_created_at;
DROP INDEX IF EXISTS feature_flags.condition_overrides_flag_default;
DROP INDEX IF EXISTS feature_flags.condition_overrides_flag_cond;
DROP TABLE IF EXISTS feature_flags.condition_overrides;
DROP TABLE IF EXISTS feature_flags.sync_freeze;
