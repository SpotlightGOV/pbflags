-- +goose Up
-- Reshape launches table for the new design: per-condition value overrides
-- with evaluation scopes. Breaking change — pre-release, no production launches.
--
-- Precondition: no ACTIVE or SOAKING launches exist.
-- (BAKED status from old schema maps to SOAKING in new lifecycle.)
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM feature_flags.launches WHERE status IN ('ACTIVE', 'BAKED')) THEN
    RAISE EXCEPTION 'Cannot migrate: ACTIVE or BAKED launches exist. Complete or abandon them first.';
  END IF;
END $$;
-- +goose StatementEnd

-- Drop the index that references the column we're about to drop.
DROP INDEX IF EXISTS feature_flags.idx_launches_flag;

-- Drop columns no longer needed: binding is inline in conditions now.
ALTER TABLE feature_flags.launches DROP COLUMN flag_id;
ALTER TABLE feature_flags.launches DROP COLUMN population_cel;
ALTER TABLE feature_flags.launches DROP COLUMN value;

-- Rename feature_id → scope_feature_id and allow NULL for cross-feature launches.
ALTER TABLE feature_flags.launches RENAME COLUMN feature_id TO scope_feature_id;
ALTER TABLE feature_flags.launches ALTER COLUMN scope_feature_id DROP NOT NULL;

-- Add new columns.
ALTER TABLE feature_flags.launches ADD COLUMN affected_features TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE feature_flags.launches ADD COLUMN description TEXT;
ALTER TABLE feature_flags.launches ADD COLUMN killed_at TIMESTAMPTZ;

-- Update status CHECK constraint: BAKED → SOAKING in new lifecycle.
ALTER TABLE feature_flags.launches DROP CONSTRAINT IF EXISTS launches_status_check;
ALTER TABLE feature_flags.launches ADD CONSTRAINT launches_status_check
  CHECK (status IN ('CREATED', 'ACTIVE', 'SOAKING', 'COMPLETED', 'ABANDONED'));

-- +goose Down
-- Reverse the reshape. This loses data (dropped columns cannot be restored).
ALTER TABLE feature_flags.launches DROP CONSTRAINT IF EXISTS launches_status_check;
ALTER TABLE feature_flags.launches ADD CONSTRAINT launches_status_check
  CHECK (status IN ('CREATED', 'ACTIVE', 'BAKED', 'COMPLETED', 'ABANDONED'));

ALTER TABLE feature_flags.launches DROP COLUMN IF EXISTS killed_at;
ALTER TABLE feature_flags.launches DROP COLUMN IF EXISTS description;
ALTER TABLE feature_flags.launches DROP COLUMN IF EXISTS affected_features;

ALTER TABLE feature_flags.launches ALTER COLUMN scope_feature_id SET NOT NULL;
ALTER TABLE feature_flags.launches RENAME COLUMN scope_feature_id TO feature_id;

ALTER TABLE feature_flags.launches ADD COLUMN flag_id VARCHAR(512) NOT NULL DEFAULT '' REFERENCES feature_flags.flags(flag_id);
ALTER TABLE feature_flags.launches ADD COLUMN population_cel TEXT;
ALTER TABLE feature_flags.launches ADD COLUMN value JSONB NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS idx_launches_flag ON feature_flags.launches(flag_id);
