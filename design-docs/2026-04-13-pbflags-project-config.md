# .pbflags.yaml project configuration

**Date:** 2026-04-13
**Status:** Proposed

## Problem

Consumer repos need to pass the same flags (`--descriptors`, `--config`, `--database`)
to every `pbflags-sync` invocation. There's no standard for where proto definitions and
YAML configs live, so every repo invents its own layout. The CLI can't provide good
defaults or validation without knowing the project structure.

## Proposal

### Project config file: `.pbflags.yaml`

A YAML file at the repo root that the CLI discovers by walking up from cwd (like
`buf.yaml`, `.goreleaser.yaml`). All fields are optional — the CLI works without it
but provides better defaults when it exists.

```yaml
# .pbflags.yaml
features_path: features    # directory containing .proto and .yaml files
```

### Features directory convention

Proto definitions and YAML condition configs live together under `features_path`.
Either layout works:

**Flat:**
```
features/
  notifications.proto
  notifications.yaml
  billing.proto
  billing.yaml
```

**Subdirectories:**
```
features/
  notifications/
    notifications.proto
    notifications.yaml
  billing/
    billing.proto
    billing.yaml
```

The tooling scans `features_path` recursively for `*.proto` and `*.yaml` files.
It does not impose a naming convention — the proto `feature` ID and the YAML
`feature:` key are the linkage, not the filename.

### CLI behavior with `.pbflags.yaml`

When `.pbflags.yaml` is present and `features_path` is set:

- `pbflags-sync` infers `--descriptors` by running `buf build <features_path>` if
  no explicit `--descriptors` is given
- `pbflags-sync` infers `--config` as `<features_path>` if no explicit `--config`
  is given
- `pbflags-sync validate` works with zero flags — just run it from the repo root
- `pbflags-sync show <flag>` likewise

### Future fields

Fields to add as needs arise (not in initial implementation):

```yaml
# .pbflags.yaml — future
features_path: features
descriptors_path: build/descriptors.pb   # pre-built descriptor set (skip buf build)
database: ${PBFLAGS_DATABASE}            # default connection string (env var expansion)
sync:
  sha_command: git rev-parse HEAD        # how to derive the sync SHA
```

## Implementation

1. Add `.pbflags.yaml` parsing to a new `internal/projectconfig` package
2. Update `pbflags-sync` to load project config and use as defaults
3. Update `pbflags-admin --standalone` similarly
4. Update `docs/agent-setup.md` and `docs/contributing.md` with the recommended layout
