# Troubleshooting

## Schema version errors

```
database schema version 0 < required 1
  run "pbflags-sync --database=..." to apply migrations, or
  start with "pbflags-admin --standalone" to auto-migrate
```

`pbflags-admin` and `pbflags-evaluator` do not run migrations — they verify the schema version on startup and fail fast if it is behind. Run `pbflags-sync` first, or use `pbflags-admin --standalone` which migrates automatically.

## Standalone lease warnings

```
STANDALONE CONFLICT: another standalone instance is active
```

Another `pbflags-admin --standalone` instance recently wrote a heartbeat to the `feature_flags.standalone_lease` table. Running multiple standalone instances risks split-brain definition conflicts.

If you are certain the other instance is gone, the warning clears automatically within 2 minutes (the heartbeat expiry window). The current instance continues running — this is a warning, not a fatal error.

## Definition changes not propagating

Admin and evaluator instances poll the database for definition changes every 60 seconds by default. After running `pbflags-sync`, changes may take up to one poll cycle to appear.

To reduce the interval, use `--definition-poll-interval`:

```bash
pbflags-evaluator --database=postgres://... --definition-poll-interval=10s
```

## Evaluator returns defaults for all flags

If the evaluator returns compiled defaults for every flag, check:

1. **Database connectivity**: the evaluator may have fallen back to stale/default mode. Check the `/healthz` endpoint — a `DEGRADED` status indicates fetch failures.
2. **Definitions not synced**: run `pbflags-sync` to ensure flag definitions are in the database.
3. **Schema not migrated**: the evaluator checks the schema version on startup. If it failed, it logs an error and exits.

## Kill switch not taking effect

Kill/unkill changes made through the admin UI are written to the database immediately. Evaluator instances pick them up through their cache TTLs:

- **Kill set**: ~30 second polling interval
- **Conditions/values**: 10 minute cache TTL (on-demand fetch on miss)

The kill switch has the shortest propagation delay, making it the appropriate tool for emergency shutoffs.

## `--database` vs `--upstream` on evaluator

`pbflags-evaluator` requires exactly one of these flags:

- `--database`: connects directly to PostgreSQL (readonly access). Use this for evaluators that can reach the database.
- `--upstream`: proxies to another evaluator over HTTP. Use this for fan-out reduction when you don't want every evaluator to hold a database connection.

Providing both or neither is an error.

## Condition evaluation issues

### Condition chain not taking effect

- Verify the config was synced: check the admin UI for the sync SHA badge on the flag detail page.
- Run `pbflags-sync validate --descriptors=descriptors.pb --features=./features` to check for compilation errors.
- Run `pbflags-sync show <flag>` to see the compiled condition chain.
- Check that `conditions` JSONB is populated: `SELECT conditions FROM feature_flags.flags WHERE flag_id = '<id>'`

### CEL expression errors in logs

The evaluator logs `CEL evaluation error` at WARN level with `flag_id`, `cond_index`, and `cel` fields.

Common causes:
- Referencing a context field that doesn't exist (e.g., `ctx.foo` when `foo` is not a dimension)
- Type mismatches (comparing string to int)

Use `pbflags-sync validate` to catch compilation errors before deploy.

### Flag returns default when conditions should match

- Check that the evaluation context is being passed correctly in the request.
- Verify the condition chain has an `otherwise` clause — without one, the flag falls through to the compiled default.
- Check if the flag is killed (`killed_at IS NOT NULL`) — killed flags always return the compiled default regardless of conditions.

### Admin UI shows "Conditions error" banner

This means the conditions JSONB in the database is malformed.

- Check the raw JSON: `SELECT conditions FROM feature_flags.flags WHERE flag_id = '<id>'`
- Re-run `pbflags-sync --features=...` to rewrite the conditions from the YAML source.
