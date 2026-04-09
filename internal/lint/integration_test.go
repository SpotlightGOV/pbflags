package lint_test

import (
	"testing"

	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/lint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheck_RealDescriptors uses the real descriptors.pb to verify that
// Check produces no violations when comparing a descriptor set against itself.
func TestCheck_RealDescriptors(t *testing.T) {
	defs, err := evaluator.ParseDescriptorFile("../evaluator/testdata/descriptors.pb")
	require.NoError(t, err)
	require.NotEmpty(t, defs)

	violations := lint.Check(defs, defs)
	assert.Empty(t, violations, "same descriptors should produce no violations")
}

// TestCheck_RealDescriptors_SimulatedBreaking uses real descriptors as a base
// and simulates breaking changes by modifying the parsed FlagDefs.
func TestCheck_RealDescriptors_SimulatedBreaking(t *testing.T) {
	defs, err := evaluator.ParseDescriptorFile("../evaluator/testdata/descriptors.pb")
	require.NoError(t, err)
	require.NotEmpty(t, defs)

	// Simulate removing the last flag.
	current := make([]evaluator.FlagDef, len(defs)-1)
	copy(current, defs[:len(defs)-1])

	violations := lint.Check(defs, current)
	require.Len(t, violations, 1)
	assert.Equal(t, lint.RuleFlagRemoved, violations[0].Rule)
}

// TestCheck_RealDescriptors_LayerChange uses real descriptors and simulates
// changing a flag's layer from "user" to "entity".
func TestCheck_RealDescriptors_LayerChange(t *testing.T) {
	defs, err := evaluator.ParseDescriptorFile("../evaluator/testdata/descriptors.pb")
	require.NoError(t, err)

	// Find the user-layer flag and change it.
	current := make([]evaluator.FlagDef, len(defs))
	copy(current, defs)
	for i := range current {
		if current[i].Layer == "user" {
			current[i].Layer = "entity"
			break
		}
	}

	violations := lint.Check(defs, current)
	require.Len(t, violations, 1)
	assert.Equal(t, lint.RuleLayerChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "user")
	assert.Contains(t, violations[0].Message, "entity")
}
