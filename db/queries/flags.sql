-- Flag sync queries. Used by the migrate/sync binary to sync proto flag
-- definitions to the database.

-- name: UpsertFeature :exec
INSERT INTO feature_flags.features (feature_id, display_name, description, owner)
VALUES ($1, $2, $3, $4)
ON CONFLICT (feature_id) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  description = EXCLUDED.description,
  owner = EXCLUDED.owner,
  updated_at = now();

-- name: GetFlagTypeAndLayer :one
SELECT flag_type, layer
FROM feature_flags.flags
WHERE flag_id = $1;

-- name: UpsertFlag :exec
INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, display_name, flag_type, layer, description, default_value)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (flag_id) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  description = EXCLUDED.description,
  default_value = EXCLUDED.default_value,
  archived_at = NULL,
  updated_at = now();

-- name: GetActiveFlagIDsForFeatures :many
SELECT flag_id
FROM feature_flags.flags
WHERE feature_id = ANY($1::varchar[])
  AND archived_at IS NULL;

-- name: ArchiveFlag :exec
UPDATE feature_flags.flags
SET archived_at = now(), updated_at = now()
WHERE flag_id = $1;
