-- +goose Up
CREATE TABLE IF NOT EXISTS feature_flags.launches (
    launch_id        VARCHAR(255) PRIMARY KEY,
    feature_id       VARCHAR(255) NOT NULL REFERENCES feature_flags.features(feature_id),
    flag_id          VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id),
    dimension        VARCHAR(255) NOT NULL,
    population_cel   TEXT,
    value            JSONB NOT NULL,
    ramp_percentage  INT NOT NULL DEFAULT 0 CHECK (ramp_percentage BETWEEN 0 AND 100),
    status           VARCHAR(20) NOT NULL DEFAULT 'CREATED'
                     CHECK (status IN ('CREATED', 'ACTIVE', 'BAKED', 'COMPLETED', 'ABANDONED')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_launches_flag ON feature_flags.launches(flag_id);

-- +goose Down
DROP TABLE IF EXISTS feature_flags.launches;
