-- Post-sync seed: promote demo launches to interesting lifecycle states.
-- Run after `make dev-seed` or `make dev` to make the admin UI more useful.
--
-- Usage: psql $PBFLAGS_DATABASE < dev/seed-launches.sql

-- email_enabled_pro_rollout: ACTIVE at 25% — a launch in progress.
UPDATE feature_flags.launches
SET status = 'ACTIVE', updated_at = now()
WHERE launch_id = 'email_enabled_pro_rollout' AND status = 'CREATED';

-- hourly_digest_rollout: SOAKING at 100% — ready to land.
UPDATE feature_flags.launches
SET status = 'SOAKING', ramp_percentage = 100, updated_at = now()
WHERE launch_id = 'hourly_digest_rollout' AND status = 'CREATED';

-- scoring_experiment: ACTIVE at 50%, killed — emergency disable demo.
UPDATE feature_flags.launches
SET status = 'ACTIVE', killed_at = now(), updated_at = now()
WHERE launch_id = 'scoring_experiment' AND status = 'CREATED';
