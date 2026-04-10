-- +goose Up
CREATE TABLE IF NOT EXISTS feature_flags.standalone_lease (
    id          VARCHAR(64) PRIMARY KEY DEFAULT 'singleton',
    instance_id VARCHAR(255) NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS feature_flags.standalone_lease;
