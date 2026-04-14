-- +goose Up
ALTER TABLE feature_flags.launches
  ADD COLUMN ramp_source VARCHAR(20) NOT NULL DEFAULT 'unspecified'
  CHECK (ramp_source IN ('unspecified', 'config', 'cli', 'ui'));

-- +goose Down
ALTER TABLE feature_flags.launches DROP COLUMN IF EXISTS ramp_source;
