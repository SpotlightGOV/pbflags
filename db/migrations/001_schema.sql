-- +goose Up
-- pbflags schema. Apply to PostgreSQL before starting pbflags-server in root mode.

CREATE SCHEMA IF NOT EXISTS feature_flags;

CREATE TABLE feature_flags.features (
    feature_id   VARCHAR(255) PRIMARY KEY NOT NULL,
    display_name VARCHAR(255) NOT NULL DEFAULT '',
    description  VARCHAR(1024) NOT NULL DEFAULT '',
    owner        VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE feature_flags.flags (
    flag_id              VARCHAR(512) PRIMARY KEY NOT NULL,
    feature_id           VARCHAR(255) NOT NULL REFERENCES feature_flags.features(feature_id),
    field_number         INT NOT NULL,
    display_name         VARCHAR(255) NOT NULL DEFAULT '',
    flag_type            VARCHAR(20) NOT NULL,
    layer                VARCHAR(50) NOT NULL DEFAULT 'GLOBAL',
    description          VARCHAR(1024) NOT NULL DEFAULT '',
    default_value        BYTEA,
    state                VARCHAR(20) NOT NULL DEFAULT 'DEFAULT',
    value                BYTEA,
    archived_at          TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT valid_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED')),
    CONSTRAINT uq_flags_feature_field UNIQUE (feature_id, field_number)
);

CREATE INDEX idx_flags_killed ON feature_flags.flags(state) WHERE state = 'KILLED';

CREATE TABLE feature_flags.flag_overrides (
    flag_id    VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id) ON DELETE CASCADE,
    entity_id  VARCHAR(255) NOT NULL,
    state      VARCHAR(20) NOT NULL DEFAULT 'ENABLED',
    value      BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_id, entity_id),
    CONSTRAINT valid_override_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED'))
);

CREATE INDEX idx_overrides_entity ON feature_flags.flag_overrides(entity_id);
CREATE INDEX idx_overrides_killed ON feature_flags.flag_overrides(state) WHERE state = 'KILLED';

CREATE TABLE feature_flags.flag_audit_log (
    id         BIGSERIAL PRIMARY KEY,
    flag_id    VARCHAR(512) NOT NULL,
    action     VARCHAR(50) NOT NULL,
    old_value  BYTEA,
    new_value  BYTEA,
    actor      VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_flag ON feature_flags.flag_audit_log(flag_id, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS feature_flags.idx_audit_flag;
DROP TABLE IF EXISTS feature_flags.flag_audit_log;
DROP INDEX IF EXISTS feature_flags.idx_overrides_killed;
DROP INDEX IF EXISTS feature_flags.idx_overrides_entity;
DROP TABLE IF EXISTS feature_flags.flag_overrides;
DROP INDEX IF EXISTS feature_flags.idx_flags_killed;
DROP TABLE IF EXISTS feature_flags.flags;
DROP TABLE IF EXISTS feature_flags.features;
DROP SCHEMA IF EXISTS feature_flags;
