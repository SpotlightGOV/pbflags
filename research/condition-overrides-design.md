# Condition Value Overrides — Design Document

**Issue:** pb-wff
**Status:** Draft design for review

## Problem

When an incident requires changing flag behavior immediately, operators must today:
1. Edit YAML config, open a PR, get it reviewed, merge, and wait for sync
2. Or kill the entire flag (nuclear option)
3. Or kill/pause a launch (only useful if the issue is launch-driven)

There's no middle ground. Operators need to be able to change the *value* on a
specific condition (or the static default) in real time, then follow up with a
config-as-code PR at their own pace.

Additionally, during incident response the config-as-code pipeline itself can
be a hazard: partial application of a pending commit, or a scheduled sync
landing mid-incident, can push the running system into an untested
configuration. We need a way to temporarily freeze config pushes without
killing individual flags.

## Design Principles

1. **Config-as-code stays source of truth** when in use — live overrides are
   *temporary divergences*, not replacements.
2. **Primitives stay orthogonal.** Launches are for gradual rollouts. Overrides
   are for surgical value changes. A freeze is for pausing config pushes.
   Operators compose them; the system doesn't conflate them.
3. **Follow the launch ramp precedent** — `ramp_source` already tracks
   config vs cli vs ui, with warnings on override. Condition overrides should
   work the same way.
4. **Audit everything** — every override (and freeze) is logged with actor,
   old value, new value, and source.
5. **Gated by server config** — a `--allow-condition-overrides` flag (default
   false) must be set to enable the capability. Future RBAC narrows it further.
6. **V1 = edit values only** — changing CEL expressions is V2.

## Precedent: Launch Ramp Source Tracking

The existing launch ramp system already solves the "config vs runtime" tension:

- `ramp_source` column: `config | cli | ui | unspecified`
- Sync sets `ramp_source = 'config'` and overwrites the value
- CLI/UI can change ramp, which sets `ramp_source = 'cli'` or `'ui'`
- When `ramp_source` was `'config'`, the API returns a warning: "next sync will
  overwrite"
- Sync preserves runtime ramp when config has no explicit ramp value

Condition overrides follow this exact pattern.

---

## Data Model

### New migration: `0xx_condition_overrides.sql`

```sql
-- Per-condition value overrides. One row per overridden condition on a flag,
-- plus optionally one row per flag with condition_index IS NULL, which
-- overrides the static/compiled default (flags with no conditions, or the
-- terminal `otherwise` fall-through).
CREATE TABLE feature_flags.condition_overrides (
  flag_id          VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id),
  condition_index  INT          NULL,     -- 0-based index; NULL = static default
  override_value   BYTEA        NOT NULL, -- proto-encoded FlagValue
  source           VARCHAR(20)  NOT NULL CHECK (source IN ('cli', 'ui')),
  actor            VARCHAR(255) NOT NULL,
  reason           TEXT         NOT NULL,
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- One override per (flag, condition).
CREATE UNIQUE INDEX condition_overrides_flag_cond
  ON feature_flags.condition_overrides (flag_id, condition_index)
  WHERE condition_index IS NOT NULL;

-- One static-default override per flag.
CREATE UNIQUE INDEX condition_overrides_flag_default
  ON feature_flags.condition_overrides (flag_id)
  WHERE condition_index IS NULL;
```

**Why a separate table (not an extra column on `flags`)?**
- `flags.conditions` is a proto blob written by sync — we don't want to modify
  it and create merge conflicts with the next sync.
- Separate table makes it trivial to "clear all overrides" after a config PR
  lands.
- Evaluator can load overrides independently and layer them on top.

### Alternative considered: inline in StoredConditions

Adding an `override_value` field to `CompiledCondition` would be simpler for the
evaluator but creates a conflict: sync would need to either preserve or clobber
the override, complicating the sync path. Keeping overrides out-of-band is
cleaner.

---

## Evaluation Changes

### Precedence (updated)

```
1. Killed?           → killed_at IS NOT NULL → return compiled default
2. Conditions?       → walk chain top-to-bottom, first match wins
   ├─ Condition matches?
   │  ├─ Has launch override? → entity in ramp? → return launch.value
   │  ├─ Has OVERRIDE for this condition index?   ← NEW
   │  │  └─ return override_value
   │  └─ return condition.value
   └─ No match → fall through
3. Static override?  → condition_index IS NULL override exists → return it  ← NEW
4. Default           → return compiled default from proto
```

**Key decision (revised):** Overrides and launches are orthogonal primitives.
Launch precedence is unchanged — if a launch is ramping an entity into a new
value, that still wins. Overrides change the *underlying* value for a condition.
If the launch itself is the problem, kill or pause the launch; if the base
value is wrong, override it. Composing the two falls out naturally and keeps
each primitive's mental model simple.

### Implementation

The evaluator already loads conditions from a `StoredConditions` proto. The
change is:

```go
// In the evaluator's condition-walk loop:
if override, ok := overrides[conditionIndex]; ok {
    return override.Value, EvaluationSourceOverride
}
// ... existing launch/value logic
```

Overrides are loaded alongside conditions during cache refresh. They're a small
map keyed by `(flag_id, condition_index)`.

New `EvaluationSource` value: `EVALUATION_SOURCE_CONDITION_OVERRIDE`

---

## Sync Behavior

When `pbflags sync` (or standalone file-watch sync) runs:

1. **Freeze check first.** If the global freeze is held (see below), sync
   fails loudly with a non-zero exit and a message pointing at the lock
   holder, reason, and `pbflags unlock` command. No writes occur.
2. **Conditions are overwritten as today** — `flags.conditions` is replaced
   with the compiled config-as-code chain.
3. **Overrides are auto-cleared by sync.** Once sync runs successfully, all
   overrides for the synced flags are deleted. The intended workflow is:
   take the freeze → apply overrides → merge follow-up config PR → release
   the freeze → next sync picks up the config change and clears the now-redundant
   overrides in the same operation. Each cleared override is audit-logged.

**Why auto-clear is safe now.** Without the freeze, auto-clear races with
operators: a mid-incident override could be wiped by a scheduled sync. With the
freeze, the window where overrides matter is also the window where sync is
blocked, so by the time sync runs again the config is expected to be the
source of truth again and the overrides are, by definition, stale.

**No stale detection / CEL hashing.** The earlier design tried to detect
when a condition chain changed shape beneath an override. With auto-clear on
sync, this concern evaporates: overrides are short-lived by construction, and
the next sync resets the world.

---

## Global Freeze

The global freeze is a first-class primitive: a single boolean (with metadata)
that, when held, causes all config-as-code sync operations to fail loudly.
It's the "big red button" for the config pipeline during incident response.

### Scope: global, not per-feature or per-flag

Launches and multi-flag config changes routinely span features. A per-feature
or per-flag freeze could allow half of a commit to land while the other half
is blocked — pushing the running system into an untested combination. A global
freeze prevents that entirely.

### No TTL, no auto-release

The freeze is released only by an explicit `pbflags unlock` (or equivalent UI
action). There is no expiration. The reasoning:

- While the freeze is held, every sync attempt fails loudly — there's a
  continuous, visible signal that something is off. Forgotten freezes surface
  quickly through blocked deploys.
- A TTL that silently expires mid-incident is strictly worse: configuration
  can start flowing again while no one is paying attention, defeating the
  purpose of the freeze.

### Data model

```sql
-- Singleton row. Absence = unlocked.
CREATE TABLE feature_flags.sync_freeze (
  id          INT          PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  actor       VARCHAR(255) NOT NULL,
  reason      TEXT         NOT NULL,
  created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
```

### API

```protobuf
rpc AcquireSyncFreeze(AcquireSyncFreezeRequest) returns (AcquireSyncFreezeResponse);
rpc ReleaseSyncFreeze(ReleaseSyncFreezeRequest) returns (ReleaseSyncFreezeResponse);
rpc GetSyncFreeze(GetSyncFreezeRequest) returns (GetSyncFreezeResponse);

message AcquireSyncFreezeRequest {
  string actor = 1;
  string reason = 2;  // required
}
```

### CLI

```
pbflags lock --reason="INC-1234: digest storm, holding config while we investigate"
pbflags unlock
pbflags lock --status
```

### Sync interaction

```
$ pbflags sync
ERROR: sync freeze is active
  Held by: alice@example.com
  Reason:  INC-1234: digest storm, holding config while we investigate
  Since:   2026-04-16 14:03:11Z (17m ago)
  Release: pbflags unlock
```

---

## API Extensions

### Proto (admin.proto)

```protobuf
// Set a value override on a specific condition (or static default).
rpc SetConditionOverride(SetConditionOverrideRequest)
    returns (SetConditionOverrideResponse);

// Clear a condition override.
rpc ClearConditionOverride(ClearConditionOverrideRequest)
    returns (ClearConditionOverrideResponse);

// Clear ALL overrides on a flag.
rpc ClearAllConditionOverrides(ClearAllConditionOverridesRequest)
    returns (ClearAllConditionOverridesResponse);

message SetConditionOverrideRequest {
  string flag_id = 1;
  // 0-based index into the condition chain. Omit (or set via oneof absence)
  // to override the static/compiled default.
  optional int32 condition_index = 2;
  FlagValue value = 3;
  string actor = 4;
  string reason = 5;           // required — incident ticket, explanation
}

message SetConditionOverrideResponse {
  // Warning when config-as-code is in use for this flag.
  string warning = 1;
  // The previous value at this condition (for confirmation UX).
  FlagValue previous_value = 2;
}

message ClearConditionOverrideRequest {
  string flag_id = 1;
  // Omit to clear the static-default override.
  optional int32 condition_index = 2;
  string actor = 3;
}

message ClearConditionOverrideResponse {}

message ClearAllConditionOverridesRequest {
  string flag_id = 1;
  string actor = 2;
}

message ClearAllConditionOverridesResponse {
  int32 cleared_count = 1;
}
```

### Extend FlagDetail (for UI display)

```protobuf
message FlagDetail {
  // ... existing fields ...

  // Active condition overrides on this flag.
  repeated ConditionOverrideDetail condition_overrides = 12;
  // True when config-as-code manages this flag's conditions.
  bool config_managed = 13;
}

message ConditionOverrideDetail {
  // Absent = override of the static/compiled default.
  optional int32 condition_index = 1;
  FlagValue override_value = 2;
  FlagValue original_value = 3;
  string actor = 4;
  string reason = 5;
  google.protobuf.Timestamp created_at = 6;
}
```

---

## Web UI Endpoints

### New HTMX endpoints

```
POST /api/conditions/override/{flagID...}
  Form: condition_index, value, reason
  → Sets override, returns updated #conditions-section fragment

POST /api/conditions/clear-override/{flagID...}
  Form: condition_index
  → Clears one override, returns updated #conditions-section

POST /api/conditions/clear-all-overrides/{flagID...}
  → Clears all overrides, returns updated #conditions-section
```

### Flag Detail Page Changes

The conditions table gains an **Actions** column (V1):

```
# | Condition                          | Value    | Launch Override | Actions
1 | ctx.plan == PlanLevel.ENTERPRISE   | "daily"  | —              | [Override Value]
2 | ctx.plan == PlanLevel.PRO          | "weekly" | digest-rollout | [Override Value]
3 | otherwise                          | "weekly" | —              | [Override Value]
```

When a condition has an active override:

```
# | Condition                          | Value              | Launch Override      | Actions
1 | ctx.plan == PlanLevel.ENTERPRISE   | "daily"            | —                   | [Override Value]
2 | ctx.plan == PlanLevel.PRO          | "hourly" ⚠ OVERRIDE | digest-rollout     | [Clear Override]
  |                                    | was: "weekly"      | (applied on top)    |
3 | otherwise                          | "weekly"           | —                   | [Override Value]
```

**Visual treatment for overrides:**
- Override value shown in amber/warning color with badge
- Original value shown below in muted text ("was: ...")
- Launch override column unchanged — launches still ramp on top of overrides
- If config-managed: a banner at the top of the conditions section:

  > ⚠ This flag's conditions are managed by config-as-code (synced from
  > `abc1234`). Overrides here are temporary — the next sync will clear them.
  > Hold the global freeze (`pbflags lock`) while you apply overrides and
  > prepare a follow-up config PR.

**Override modal/inline form:**
- Click "Override Value" → inline form appears in the row
- Shows: current value, input for new value (respects `supported_values` if set),
  required "Reason" text field
- Submit button: "Override — I understand this is a temporary change"
- On submit: HTMX POST, section re-renders with override applied

**Static-value flags (no conditions):**
- Same pattern, but the single "compiled default" row gets the Override button
- condition_index is NULL (omitted in the API request)

### Banner when ANY overrides are active (flag detail header)

```html
<div class="alert alert-warning">
  ⚠ 1 active override — this flag's behavior differs from config-as-code.
  <button>Clear All Overrides</button>
</div>
```

### Dashboard indicator

Flags with active overrides show a small badge on the dashboard list so
operators can see at a glance which flags are diverged.

---

## CLI Commands

### `pb condition override`

```
pb condition override <flag_id> [condition_index] <value> --reason="..."

  Override the value for a specific condition on a flag.
  Omit condition_index to override the static/compiled default.

  --reason   Required. Why this override is being set (incident ticket, etc.)

  Examples:
    pb condition override notifications/5 2 "daily" --reason="INC-1234: digest storm"
    pb condition override notifications/5 "weekly" --reason="INC-1234: safe default"
```

### `pb condition clear`

```
pb condition clear <flag_id> [condition_index]

  Clear condition override(s) on a flag.
  If condition_index is omitted, clears ALL overrides on the flag.

  Examples:
    pb condition clear notifications/5 2       # clear one
    pb condition clear notifications/5          # clear all
```

### `pb condition list`

```
pb condition list <flag_id>

  Show the condition chain for a flag, including any active overrides.

  Output:
    #  Condition                          Value     Override  Source
    1  ctx.plan == PlanLevel.ENTERPRISE   "daily"   —         config
    2  ctx.plan == PlanLevel.PRO          "hourly"  ⚠ was: "weekly"  cli (INC-1234)
    3  otherwise                          "weekly"  —         config
```

### `pb overrides` — global listing

```
pb overrides [--min-age=DURATION] [--actor=USER] [--format=table|json]

  List every active condition override across all flags, newest first.
  Intended as the "what's diverged right now?" observability tool: overrides
  don't expire automatically, so operators periodically review this list and
  decide what to clear.

  --min-age   Only show overrides older than this (e.g., 24h, 7d).
              Useful for "forgot to clean up after the incident" sweeps.
  --actor     Filter by actor.
  --format    Default table; json for scripting.

  Output:
    FLAG                        COND  VALUE      WAS        AGE     ACTOR    REASON
    notifications/digest_freq   2     "hourly"   "weekly"   2h14m   alice    INC-1234: digest storm
    billing/trial_length        —     14         30         6d03h   bob      INC-1198: accounting bug
    search/min_score            1     0.5        0.7        14d     carol    experiment, remove by EOW
```

This replaces any notion of a TTL. Overrides are deliberately persistent
until a human clears them (or a sync clears them as part of a successful
config push). `pb overrides --min-age=7d` is the expected tool for spotting
overrides that have outlived their purpose.

### Warning on override

When config-as-code is in use, the CLI prints:

```
⚠ Warning: This flag is managed by config-as-code (last sync: abc1234).
  This override is temporary — the next successful sync will clear it.
  If you're handling an incident, take the freeze first: pbflags lock --reason="..."

Set override? [y/N]
```

(Non-interactive mode with `--yes` skips the prompt, still prints the warning.)

If the global freeze is *not* held, the CLI also emits a hint that the
expected workflow is lock → override → follow-up config PR → unlock. The CLI
does not block unlocked overrides — operators may legitimately want a
short-lived override without freezing the pipeline — but it nudges toward the
safer flow.

---

## Server Configuration

### New admin server flag

```
--allow-condition-overrides   Enable condition value overrides via UI and CLI (default: false)
```

Environment variable: `PBFLAGS_ALLOW_CONDITION_OVERRIDES=true`

When disabled:
- API returns `connect.CodePermissionDenied` with message
  "condition overrides are disabled on this server"
- UI hides the Override/Clear buttons entirely
- CLI prints error and exits

### Future RBAC hook point

The override endpoints check a capability that will map to an RBAC permission:

```go
func (s *Service) SetConditionOverride(ctx context.Context, req *connect.Request[...]) (...) {
    if !s.allowConditionOverrides {
        return nil, connect.NewError(connect.CodePermissionDenied, ...)
    }
    // Future: check req actor has "condition_override" permission
    ...
}
```

---

## Audit Log

New actions:

| Action | Description |
|--------|-------------|
| `SET_CONDITION_OVERRIDE` | Override value set on condition N (or default) |
| `CLEAR_CONDITION_OVERRIDE` | Override cleared on condition N (or default) |
| `CLEAR_ALL_CONDITION_OVERRIDES` | All overrides cleared on flag |
| `CONDITION_OVERRIDE_AUTO_CLEARED` | Sync cleared an override as part of a successful sync |
| `ACQUIRE_SYNC_FREEZE` | Global sync freeze acquired |
| `RELEASE_SYNC_FREEZE` | Global sync freeze released |

Each override entry records: flag_id, condition_index, old_value, new_value,
actor, reason. Freeze entries record: actor, reason, duration-held (on release).

---

## Implementation Phases

### Phase 1: Freeze + data model + evaluator (no UI)
- [ ] Migration: `sync_freeze` table, `condition_overrides` table (NULL index + partial unique indexes)
- [ ] Store methods: AcquireSyncFreeze, ReleaseSyncFreeze, SetConditionOverride, ClearConditionOverride
- [ ] Sync: freeze-check gate; on success, auto-clear overrides for synced flags (audit-logged)
- [ ] Evaluator: load overrides, apply in condition walk
- [ ] New `EvaluationSource` value

### Phase 2: CLI
- [ ] `pbflags lock`, `pbflags unlock`, `pbflags lock --status`
- [ ] `pb condition override`, `pb condition clear`, `pb condition list`
- [ ] Warning/confirmation when config-managed and freeze not held
- [ ] `--allow-condition-overrides` server flag

### Phase 3: Admin UI
- [ ] Flag detail: override indicators, inline override form
- [ ] Flag detail: clear override / clear all buttons
- [ ] Config-managed banner with sync SHA
- [ ] Dashboard: override badge on affected flags; freeze banner when held
- [ ] Freeze acquire/release UI (with reason field)
- [ ] Audit log: override + freeze actions

### Phase 4 (V2): CEL expression editing
- [ ] UI: inline CEL editor with syntax highlighting
- [ ] Validation: CEL compilation check before save
- [ ] Override applies to full condition (CEL + value), not just value

---

## Open Questions

None outstanding. Earlier drafts raised:

- A per-override TTL — rejected. Time-based magical state changes create
  silent divergence; `pb overrides --min-age=...` is the right surface for
  spotting forgotten overrides.
- Freeze visibility in evaluator caches / SDK health — rejected. The freeze
  only gates admin-side config pushes; running services keep serving the
  last synced config and have no need to know about it.
