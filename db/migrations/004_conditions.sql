-- +goose Up
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS conditions JSONB;
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS dimension_metadata JSONB;
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS cel_version VARCHAR(50);

-- +goose Down
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS cel_version;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS dimension_metadata;
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS conditions;
