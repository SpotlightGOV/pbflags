-- +goose Up
-- Store the pruned EvaluationContext FileDescriptorSet so runtime binaries
-- (pbflags-evaluator, pbflags-admin) can reconstruct a ConditionEvaluator
-- without needing the original proto sources.
CREATE TABLE IF NOT EXISTS feature_flags.context_descriptor (
    id             INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    descriptor_set BYTEA NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS feature_flags.context_descriptor;
