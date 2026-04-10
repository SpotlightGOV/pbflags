-- +goose Up
ALTER TABLE feature_flags.flags ADD COLUMN IF NOT EXISTS supported_values BYTEA;

-- +goose Down
ALTER TABLE feature_flags.flags DROP COLUMN IF EXISTS supported_values;
