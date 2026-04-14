-- +goose Up
-- Replace conditions and dimension_metadata JSONB columns with bytea for proto encoding.
-- Greenfield: no production data in these columns yet, so drop-and-add is safe.
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS conditions;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS dimension_metadata;
ALTER TABLE feature_flags.flags ADD COLUMN conditions BYTEA;
ALTER TABLE feature_flags.flags ADD COLUMN dimension_metadata BYTEA;
ALTER TABLE feature_flags.flags ADD COLUMN condition_count INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS condition_count;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS dimension_metadata;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS conditions;
ALTER TABLE feature_flags.flags ADD COLUMN conditions JSONB;
ALTER TABLE feature_flags.flags ADD COLUMN dimension_metadata JSONB;
