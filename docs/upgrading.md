# Upgrading

## Standalone

If your proto definitions changed, rebuild the descriptor set first:

```bash
buf build proto -o descriptors.pb
```

Then replace the binary (or image) and restart. Standalone mode runs migrations and syncs definitions on startup, so no separate `pbflags-sync` step is needed.

## Production (multi-instance)

Upgrade `pbflags-sync` first. It runs migrations before syncing, so the database schema is updated before any other component sees it:

1. **Regenerate code and rebuild `descriptors.pb`** from the updated proto definitions.
2. **Deploy the new `pbflags-sync`** in your CI/CD pipeline. This applies any pending migrations and syncs definitions.
3. **Roll out `pbflags-admin`** instances. They check the schema version on startup and will work with the updated schema.
4. **Roll out `pbflags-evaluator`** instances. They only read from the database, so they are safe to update last.

This order matters because `pbflags-admin` and `pbflags-evaluator` do not run migrations — they verify the schema is at the expected version and fail fast if it is not. Always let `pbflags-sync` go first.

## Version-specific upgrade guides

- [User-defined layers](upgrade-guide-user-defined-layers.md) — migrating from hardcoded to user-defined layer enums (v0.6.0)
