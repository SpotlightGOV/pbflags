# Condition Value Overrides — Design Document

**Issue:** pb-wff
**Status:** Draft design for review

## Problem

When an incident requires changing flag behavior immediately, operators must today:
1. Edit YAML config, open a PR, get it reviewed, merge, and wait for sync
2. Or kill the entire flag (nuclear option)

There's no middle ground. Operators need to be able to change the *value* on a
specific condition (or the static default) in real time, then follow up with a
config-as-code PR at their own pace.

## Design Principles

1. **Config-as-code stays source of truth** when in use — live overrides are
   *temporary divergences*, not replacements.
2. **Follow the launch ramp precedent** — `ramp_source` already tracks
   config vs cli vs ui, with warnings on override. Condition overrides should
   work the same way.
3. **Audit everything** — every override is logged with actor, old value, new
   value, and source.
4. **Gated by server config** — a `--allow-condition-overrides` flag (default
   false) must be set to enable the capability. Future RBAC narrows it further.
5. **V1 = edit values only** — changing CEL expressions is V2.

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
-- Per-condition value overrides. One row per overridden condition on a flag.
-- The condition is identified by its 0-based index in the stored condition chain.
CREATE TABLE feature_flags.condition_overrides (
  flag_id          VARCHAR(512) NOT NULL REFERENCES feature_flags.flags(flag_id),
  condition_index  INT          NOT NULL,  -- 0-based index into StoredConditions
  override_value   BYTEA        NOT NULL,  -- proto-encoded FlagValue
  source           VARCHAR(20)  NOT NULL CHECK (source IN ('cli', 'ui')),
  actor            VARCHAR(255) NOT NULL,
  created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
  PRIMARY KEY (flag_id, condition_index)
);

-- Static-value override for flags with no conditions (or for the compiled default).
-- Only one row per flag. condition_index = -1 signals "override the static default."
-- (Alternatively, we use the same table with condition_index = -1.)
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
   │  ├─ Has OVERRIDE for this condition index?   ← NEW
   │  │  └─ return override_value (skip launch logic for this condition)
   │  ├─ Has launch override?
   │  │  └─ Entity in ramp? → return launch.value
   │  └─ return condition.value
   └─ No match → fall through
3. Static override?  → condition_index = -1 override exists → return it  ← NEW
4. Default           → return compiled default from proto
```

**Key decision:** An override on a condition *replaces the entire value path*
for that condition, including any launch override. This is the correct incident
response behavior — if you're overriding a condition's value during an incident,
you want deterministic output, not "some users get the override and launch-ramp
users get something else."

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

1. **Conditions are overwritten as today** — `flags.conditions` is replaced with
   the compiled config-as-code chain.
2. **Overrides are NOT automatically cleared.** The sync logs a warning:

   ```
   WARN flag notifications/digest_frequency has 1 active condition override(s) — review and clear via UI or CLI
   ```

3. **Index stability:** If the condition chain changes shape (conditions added,
   removed, reordered), existing overrides may point to the wrong condition.
   Sync detects this by comparing the old and new condition count and CEL
   expressions at each index. If an override's target condition changed:

   ```
   WARN override on notifications/digest_frequency[1] is stale — condition at index 1 changed from
        'ctx.plan == PlanLevel.ENTERPRISE' to 'ctx.plan == PlanLevel.PRO'; override will be IGNORED
   ```

   The override row gets a `stale` flag set. Stale overrides are excluded from
   evaluation but kept for audit trail until explicitly cleared.

### Stale detection

Add `stale BOOLEAN NOT NULL DEFAULT false` and `target_cel_hash VARCHAR(64)` to
`condition_overrides`. When the override is created, store a hash of the CEL
expression at that index. On sync, compare hashes; mark stale if they diverge.

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
  int32 condition_index = 2;   // 0-based; -1 = static default
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
  int32 condition_index = 2;   // -1 = static default
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
  int32 condition_index = 1;
  FlagValue override_value = 2;
  FlagValue original_value = 3;
  string actor = 4;
  string reason = 5;
  google.protobuf.Timestamp created_at = 6;
  bool stale = 7;
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
# | Condition                          | Value              | Launch Override | Actions
1 | ctx.plan == PlanLevel.ENTERPRISE   | "daily"            | —              | [Override Value]
2 | ctx.plan == PlanLevel.PRO          | "hourly" ⚠ OVERRIDE | (bypassed)    | [Clear Override]
  |                                    | was: "weekly"      |                |
3 | otherwise                          | "weekly"           | —              | [Override Value]
```

**Visual treatment for overrides:**
- Override value shown in amber/warning color with badge
- Original value shown below in muted text ("was: ...")
- Launch override column shows "(bypassed)" since overrides supersede launches
- If config-managed: a banner at the top of the conditions section:

  > ⚠ This flag's conditions are managed by config-as-code (synced from
  > `abc1234`). Overrides here are temporary — the next sync will NOT clear
  > them, but the underlying conditions may change. Clear overrides after your
  > config PR lands.

**Override modal/inline form:**
- Click "Override Value" → inline form appears in the row
- Shows: current value, input for new value (respects `supported_values` if set),
  required "Reason" text field
- Submit button: "Override — I understand this is a temporary change"
- On submit: HTMX POST, section re-renders with override applied

**Static-value flags (no conditions):**
- Same pattern, but the single "compiled default" row gets the Override button
- condition_index = -1

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
pb condition override <flag_id> <condition_index> <value> --reason="..."

  Override the value for a specific condition on a flag.
  Use condition_index=-1 to override the static default.

  --reason   Required. Why this override is being set (incident ticket, etc.)

  Examples:
    pb condition override notifications/5 2 "daily" --reason="INC-1234: digest storm"
    pb condition override notifications/5 -1 "weekly" --reason="INC-1234: safe default"
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

### Warning on override

When config-as-code is in use, the CLI prints:

```
⚠ Warning: This flag is managed by config-as-code (last sync: abc1234).
  This override is temporary and will NOT be cleared by the next sync.
  Follow up with a config change and then run: pb condition clear notifications/5

Set override? [y/N]
```

(Non-interactive mode with `--yes` skips the prompt, still prints the warning.)

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
| `SET_CONDITION_OVERRIDE` | Override value set on condition N |
| `CLEAR_CONDITION_OVERRIDE` | Override cleared on condition N |
| `CLEAR_ALL_CONDITION_OVERRIDES` | All overrides cleared on flag |
| `CONDITION_OVERRIDE_STALE` | Sync marked override as stale |

Each entry records: flag_id, condition_index, old_value, new_value, actor, reason.

---

## Implementation Phases

### Phase 1: Data model + evaluator (no UI)
- [ ] Migration: `condition_overrides` table
- [ ] Store methods: SetConditionOverride, ClearConditionOverride, etc.
- [ ] Evaluator: load overrides, apply in condition walk
- [ ] New `EvaluationSource` value
- [ ] Sync: warn on active overrides, stale detection

### Phase 2: CLI
- [ ] `pb condition override`, `pb condition clear`, `pb condition list`
- [ ] Warning/confirmation when config-managed
- [ ] `--allow-condition-overrides` server flag

### Phase 3: Admin UI
- [ ] Flag detail: override indicators, inline override form
- [ ] Flag detail: clear override / clear all buttons
- [ ] Config-managed banner with sync SHA
- [ ] Dashboard: override badge on affected flags
- [ ] Audit log: override actions

### Phase 4 (V2): CEL expression editing
- [ ] UI: inline CEL editor with syntax highlighting
- [ ] Validation: CEL compilation check before save
- [ ] Override applies to full condition (CEL + value), not just value

---

## Open Questions

1. **Should sync auto-clear overrides when the config value matches the override
   value?** (i.e., the follow-up PR landed and set the same value — the override
   is now redundant.) Leaning yes for convenience, but it's a surprise
   side-effect. Could be opt-in via `--auto-clear-matching-overrides`.

2. **Should there be a TTL on overrides?** e.g., "override expires after 24h
   unless renewed." Prevents forgotten overrides from drifting forever. Could be
   a V2 addition.

3. **Should the override table store the full condition chain snapshot?** This
   would make stale detection trivial (diff the whole chain) but adds storage
   cost. Current design with CEL hash is lighter.
