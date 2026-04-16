-- +goose Up
-- Add a launch-defined progression of ramp percentages. When set, the
-- admin UI uses these as the quick-pick chips in the ramp editor instead
-- of the default 0/5/10/25/50/75/100. Empty array = no override (use
-- defaults). Values are validated 0-100 by the YAML loader; the column
-- itself stays permissive so we can backfill or accept future shapes.
ALTER TABLE feature_flags.launches
  ADD COLUMN ramp_steps INTEGER[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE feature_flags.launches DROP COLUMN IF EXISTS ramp_steps;
