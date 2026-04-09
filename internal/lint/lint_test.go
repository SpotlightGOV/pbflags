package lint

import (
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolFlag(id, name, layer string) evaluator.FlagDef {
	return evaluator.FlagDef{
		FlagID:   id,
		Name:     name,
		FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL,
		Layer:    layer,
	}
}

func stringFlag(id, name, layer string) evaluator.FlagDef {
	return evaluator.FlagDef{
		FlagID:   id,
		Name:     name,
		FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING,
		Layer:    layer,
	}
}

func TestCheck_NoChanges(t *testing.T) {
	defs := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
		stringFlag("f/2", "name", ""),
	}
	violations := Check(defs, defs)
	assert.Empty(t, violations)
}

func TestCheck_FlagRemoved(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
		stringFlag("f/2", "name", ""),
	}
	current := []evaluator.FlagDef{
		stringFlag("f/2", "name", ""),
	}
	violations := Check(base, current)
	require.Len(t, violations, 1)
	assert.Equal(t, "f/1", violations[0].FlagID)
	assert.Equal(t, RuleFlagRemoved, violations[0].Rule)
}

func TestCheck_FlagAdded(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
	}
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
		stringFlag("f/2", "name", ""),
	}
	violations := Check(base, current)
	assert.Empty(t, violations, "adding a flag is not a breaking change")
}

func TestCheck_TypeChanged(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", ""),
	}
	current := []evaluator.FlagDef{
		stringFlag("f/1", "enabled", ""),
	}
	violations := Check(base, current)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleTypeChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "BOOL")
	assert.Contains(t, violations[0].Message, "STRING")
}

func TestCheck_LayerGlobalToLayer(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", ""),
	}
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
	}
	violations := Check(base, current)
	assert.Empty(t, violations, "global → layer is allowed")
}

func TestCheck_LayerToGlobal(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
	}
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", ""),
	}
	violations := Check(base, current)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleLayerChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "global")
	assert.Contains(t, violations[0].Guidance, "Migrating")
}

func TestCheck_LayerAToLayerB(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
	}
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "entity"),
	}
	violations := Check(base, current)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleLayerChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "user")
	assert.Contains(t, violations[0].Message, "entity")
}

func TestCheck_LayerGlobalExplicitToImplicit(t *testing.T) {
	// "global" (explicit) → "" (implicit) — both are global, no change.
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "global"),
	}
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", ""),
	}
	violations := Check(base, current)
	assert.Empty(t, violations, "global explicit ↔ implicit is not a change")
}

func TestCheck_MultipleViolations(t *testing.T) {
	base := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
		stringFlag("f/2", "name", ""),
		boolFlag("f/3", "active", "entity"),
	}
	current := []evaluator.FlagDef{
		// f/1: layer user → global (violation)
		boolFlag("f/1", "enabled", ""),
		// f/2: removed (violation)
		// f/3: layer entity → tenant (violation)
		boolFlag("f/3", "active", "tenant"),
	}
	violations := Check(base, current)
	assert.Len(t, violations, 3)

	rules := make(map[string]int)
	for _, v := range violations {
		rules[v.Rule]++
	}
	assert.Equal(t, 1, rules[RuleFlagRemoved])
	assert.Equal(t, 2, rules[RuleLayerChanged])
}

func TestCheck_EmptyBase(t *testing.T) {
	current := []evaluator.FlagDef{
		boolFlag("f/1", "enabled", "user"),
	}
	violations := Check(nil, current)
	assert.Empty(t, violations, "all-new flags is not a breaking change")
}

func TestViolation_String(t *testing.T) {
	v := Violation{
		FlagID:   "notifications/1",
		Rule:     RuleFlagRemoved,
		Message:  "flag \"email_enabled\" was removed",
		Guidance: "Archive the flag instead.",
	}
	s := v.String()
	assert.Contains(t, s, "notifications/1")
	assert.Contains(t, s, RuleFlagRemoved)
	assert.Contains(t, s, "Archive")
}
