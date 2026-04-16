-- +goose Up

-- Allow 'automation' as a third source for overrides (alongside cli/ui).
-- Mirrors pbflagsv1.OverrideSource enum.
ALTER TABLE feature_flags.condition_overrides
  DROP CONSTRAINT IF EXISTS condition_overrides_source_check;
ALTER TABLE feature_flags.condition_overrides
  ADD CONSTRAINT condition_overrides_source_check
  CHECK (source IN ('cli', 'ui', 'automation'));

-- Non-partial (flag_id) index. The two existing partial unique indexes
-- cover their respective shapes for unique enforcement, but neither
-- supports a fast lookup of "all overrides on this flag" because the
-- NULL-condition partial doesn't help when the SELECT is unconstrained
-- on condition_index. The DBFetcher hot path queries by flag_id only.
CREATE INDEX IF NOT EXISTS idx_condition_overrides_flag_id
  ON feature_flags.condition_overrides (flag_id);

-- Drop the unused created_at DESC index. ListAllOverrides now applies a
-- LIMIT so the planner picks a Top-N seq scan; the index never gets used.
DROP INDEX IF EXISTS feature_flags.idx_condition_overrides_created_at;

-- +goose Down
CREATE INDEX IF NOT EXISTS idx_condition_overrides_created_at
  ON feature_flags.condition_overrides (created_at DESC);
DROP INDEX IF EXISTS feature_flags.idx_condition_overrides_flag_id;
ALTER TABLE feature_flags.condition_overrides
  DROP CONSTRAINT IF EXISTS condition_overrides_source_check;
ALTER TABLE feature_flags.condition_overrides
  ADD CONSTRAINT condition_overrides_source_check
  CHECK (source IN ('cli', 'ui'));
