-- +goose Up
ALTER TABLE feature_flags.features ADD COLUMN IF NOT EXISTS sync_sha VARCHAR(40);

-- +goose Down
ALTER TABLE feature_flags.features DROP COLUMN IF EXISTS sync_sha;
