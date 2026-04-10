// Package lint detects breaking changes between two versions of pbflags
// proto definitions.
package lint

import (
	"fmt"
	"strings"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

// Violation represents a breaking change detected between two versions of
// flag definitions.
type Violation struct {
	FlagID   string // e.g., "notifications/1"
	Rule     string // machine-readable rule name
	Message  string // human-readable description
	Guidance string // suggested fix
}

// String formats the violation for terminal output.
func (v Violation) String() string {
	s := fmt.Sprintf("%s: %s: %s", v.FlagID, v.Rule, v.Message)
	if v.Guidance != "" {
		s += "\n  " + v.Guidance
	}
	return s
}

// Rule names.
const (
	RuleTypeChanged  = "type_changed"
	RuleLayerChanged = "layer_changed"
)

// Check compares base and current flag definitions and returns any
// breaking change violations. This is a pure function with no I/O.
func Check(base, current []evaluator.FlagDef) []Violation {
	baseMap := indexByID(base)
	currentMap := indexByID(current)

	var violations []Violation

	for id, baseDef := range baseMap {
		curDef, exists := currentMap[id]
		if !exists {
			continue // Flag removal is normal lifecycle, not a violation.
		}

		// Check type change.
		if baseDef.FlagType != curDef.FlagType {
			violations = append(violations, Violation{
				FlagID:   id,
				Rule:     RuleTypeChanged,
				Message:  fmt.Sprintf("flag type changed from %s to %s", flagTypeName(baseDef.FlagType), flagTypeName(curDef.FlagType)),
				Guidance: "Changing a flag's type breaks generated client code and may corrupt stored values. Define a new flag with the desired type instead.",
			})
		}

		// Check layer transition.
		if v, ok := checkLayerChange(id, baseDef, curDef); ok {
			violations = append(violations, v)
		}
	}

	return violations
}

// checkLayerChange validates the layer transition for a single flag.
// Returns a violation and true if the transition is forbidden.
func checkLayerChange(flagID string, base, current evaluator.FlagDef) (Violation, bool) {
	baseGlobal := base.IsGlobalLayer()
	curGlobal := current.IsGlobalLayer()
	baseLayer := normalizeLayer(base.Layer)
	curLayer := normalizeLayer(current.Layer)

	// No change.
	if baseLayer == curLayer {
		return Violation{}, false
	}

	// Global → Layer: allowed.
	if baseGlobal && !curGlobal {
		return Violation{}, false
	}

	// Layer → Global: forbidden.
	if !baseGlobal && curGlobal {
		return Violation{
			FlagID:  flagID,
			Rule:    RuleLayerChanged,
			Message: fmt.Sprintf("layer changed from %q to global", baseLayer),
			Guidance: "Changing a layered flag to global orphans existing override data. " +
				"Define a new global flag and migrate your code to use it. " +
				"See the \"Migrating a flag to a different layer\" section in the README.",
		}, true
	}

	// Layer A → Layer B: forbidden.
	return Violation{
		FlagID:  flagID,
		Rule:    RuleLayerChanged,
		Message: fmt.Sprintf("layer changed from %q to %q", baseLayer, curLayer),
		Guidance: "Changing a flag's layer invalidates existing override data (entity IDs from the old layer are misinterpreted as the new layer). " +
			"Define a new flag with the desired layer and migrate overrides. " +
			"See the \"Migrating a flag to a different layer\" section in the README.",
	}, true
}

func normalizeLayer(layer string) string {
	if layer == "" || strings.EqualFold(layer, "global") {
		return "global"
	}
	return strings.ToLower(layer)
}

func flagTypeName(ft pbflagsv1.FlagType) string {
	return strings.TrimPrefix(ft.String(), "FLAG_TYPE_")
}

func indexByID(defs []evaluator.FlagDef) map[string]evaluator.FlagDef {
	m := make(map[string]evaluator.FlagDef, len(defs))
	for _, d := range defs {
		m[d.FlagID] = d
	}
	return m
}
