package lint

import (
	"fmt"
	"sort"
	"strings"

	"unicode"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/SpotlightGOV/pbflags/internal/evaluator"
)

// Rule names for scope-related violations.
const (
	RuleScopeDimChanged     = "scope_dimension_changed"
	RuleScopeRemoved        = "scope_removed"
	RuleFeatureScopeRemoved = "feature_scope_removed"
	RuleCondScopeCompat     = "condition_scope_compat"
)

// ScopeInfo holds a scope's name and dimensions for comparison.
type ScopeInfo struct {
	Name       string
	Dimensions []string // additional dimensions beyond globally required
}

// FeatureScopeInfo holds a feature's scope declarations for comparison.
type FeatureScopeInfo struct {
	FeatureID string
	Scopes    []string
}

// CheckScopes compares scope definitions between base and current descriptors.
// Returns breaking-change violations for:
//   - Scope dimension set changes
//   - Scope removals (removes generated *Features type)
//   - Feature scope removals (removes generated accessor method)
func CheckScopes(baseScopes, currentScopes []ScopeInfo, baseFeatures, currentFeatures []FeatureScopeInfo) []Violation {
	var violations []Violation

	baseMap := indexScopes(baseScopes)
	currentMap := indexScopes(currentScopes)

	// Check for removed scopes and dimension changes.
	for name, baseSI := range baseMap {
		curSI, exists := currentMap[name]
		if !exists {
			violations = append(violations, Violation{
				FlagID:   name,
				Rule:     RuleScopeRemoved,
				Message:  fmt.Sprintf("scope %q removed", name),
				Guidance: fmt.Sprintf("Removing scope %q deletes the generated %sFeatures type. Consumers using it will fail to compile.", name, toPascalCase(name)),
			})
			continue
		}

		// Check dimension changes.
		added, removed := diffStrings(baseSI.Dimensions, curSI.Dimensions)
		if len(added) > 0 || len(removed) > 0 {
			var parts []string
			if len(added) > 0 {
				parts = append(parts, "added: "+strings.Join(added, ", "))
			}
			if len(removed) > 0 {
				parts = append(parts, "removed: "+strings.Join(removed, ", "))
			}
			violations = append(violations, Violation{
				FlagID:  name,
				Rule:    RuleScopeDimChanged,
				Message: fmt.Sprintf("scope %q dimension set changed (%s)", name, strings.Join(parts, "; ")),
				Guidance: fmt.Sprintf("This changes the %sFeatures constructor signature. "+
					"Either add a new scope (e.g., %q) or coordinate the change with consumers.",
					toPascalCase(name), name+"_v2"),
			})
		}
	}

	// Check for removed feature-scope bindings.
	baseFS := indexFeatureScopes(baseFeatures)
	for _, cur := range currentFeatures {
		baseScopes, ok := baseFS[cur.FeatureID]
		if !ok {
			continue // new feature, not a breaking change
		}
		for _, s := range baseScopes {
			if !containsStr(cur.Scopes, s) {
				violations = append(violations, Violation{
					FlagID:  cur.FeatureID,
					Rule:    RuleFeatureScopeRemoved,
					Message: fmt.Sprintf("feature %q removed from scope %q", cur.FeatureID, s),
					Guidance: fmt.Sprintf("Removing feature %q from scope %q deletes the %s() accessor on %sFeatures. Consumers calling it will fail to compile.",
						cur.FeatureID, s, toPascalCase(cur.FeatureID), toPascalCase(s)),
				})
			}
		}
	}

	return violations
}

// CheckConditionScopeCompat checks that every CEL dimension referenced by a
// feature's conditions is available in every scope the feature declares.
// This uses the condition ASTs and scope definitions from the current descriptors.
func CheckConditionScopeCompat(
	contextMsg protoreflect.MessageDescriptor,
	scopes []ScopeInfo,
	features []FeatureScopeInfo,
	flagDefs []evaluator.FlagDef,
	conditionDims map[string][]string, // flagID → dimension names used in conditions
) []Violation {
	if contextMsg == nil || len(scopes) == 0 {
		return nil
	}

	// Build globally required dimension set.
	globalDims := map[string]bool{}
	fields := contextMsg.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		// A dimension is globally required if presence == REQUIRED (enum value 1).
		opts := f.Options()
		if opts == nil {
			continue
		}
		rm := opts.(interface{ ProtoReflect() protoreflect.Message }).ProtoReflect()
		rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			if fd.Number() == 51004 && fd.IsExtension() {
				dimMsg := v.Message()
				dimMsg.Range(func(dfd protoreflect.FieldDescriptor, dv protoreflect.Value) bool {
					if dfd.Name() == "presence" && dv.Enum() == 1 {
						globalDims[string(f.Name())] = true
					}
					return true
				})
			}
			return true
		})
	}

	// Build scope → available dimensions (global + scope-specific).
	scopeDims := map[string]map[string]bool{}
	for _, s := range scopes {
		avail := map[string]bool{}
		for g := range globalDims {
			avail[g] = true
		}
		for _, d := range s.Dimensions {
			avail[d] = true
		}
		scopeDims[s.Name] = avail
	}

	// Build featureID → scopes lookup.
	featureScopeMap := map[string][]string{}
	for _, f := range features {
		featureScopeMap[f.FeatureID] = f.Scopes
	}

	var violations []Violation
	for flagID, dims := range conditionDims {
		featureID := flagFeatureID(flagID)
		fScopes := featureScopeMap[featureID]
		for _, dim := range dims {
			for _, scopeName := range fScopes {
				avail := scopeDims[scopeName]
				if avail == nil || !avail[dim] {
					violations = append(violations, Violation{
						FlagID: flagID,
						Rule:   RuleCondScopeCompat,
						Message: fmt.Sprintf("condition references dimension %q which is not in scope %q",
							dim, scopeName),
						Guidance: fmt.Sprintf("Feature %q is available in scope %q, but condition on flag %q uses dimension %q which is not provided by that scope. Either remove %q from the feature's scopes or rewrite the condition.",
							featureID, scopeName, flagID, dim, scopeName),
					})
				}
			}
		}
	}
	return violations
}

// ExtractScopesFromDescriptors builds ScopeInfo and FeatureScopeInfo from raw descriptors
// using contextutil's discovery functions and evaluator's FlagDef parsing.
func ExtractScopesFromDescriptors(descriptorData []byte) ([]ScopeInfo, []FeatureScopeInfo, error) {
	files, _, err := evaluator.ParseDescriptorSet(descriptorData)
	if err != nil {
		return nil, nil, fmt.Errorf("parse descriptor set: %w", err)
	}

	// Discover scopes.
	var scopes []ScopeInfo
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		opts := fd.Options()
		if opts == nil {
			return true
		}
		rm := opts.ProtoReflect()
		rm.Range(func(extFd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			if extFd.Number() == 51005 && extFd.IsExtension() {
				list := v.List()
				for i := 0; i < list.Len(); i++ {
					scope := parseScopeFromReflect(list.Get(i).Message())
					scopes = append(scopes, scope)
				}
			}
			return true
		})
		return true
	})

	// Discover feature scopes.
	var features []FeatureScopeInfo
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Messages().Len(); i++ {
			msg := fd.Messages().Get(i)
			if fi := parseFeatureScopeFromReflect(msg); fi != nil {
				features = append(features, *fi)
			}
		}
		return true
	})

	return scopes, features, nil
}

func parseScopeFromReflect(msg protoreflect.Message) ScopeInfo {
	var si ScopeInfo
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch fd.Name() {
		case "name":
			si.Name = v.String()
		case "dimensions":
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				si.Dimensions = append(si.Dimensions, list.Get(i).String())
			}
		}
		return true
	})
	return si
}

func parseFeatureScopeFromReflect(msg protoreflect.MessageDescriptor) *FeatureScopeInfo {
	opts := msg.Options()
	if opts == nil {
		return nil
	}
	rm := opts.ProtoReflect()
	var fi *FeatureScopeInfo
	rm.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Number() == 51000 && fd.IsExtension() {
			m := v.Message()
			fi = &FeatureScopeInfo{}
			m.Range(func(innerFd protoreflect.FieldDescriptor, innerV protoreflect.Value) bool {
				switch innerFd.Name() {
				case "id":
					fi.FeatureID = innerV.String()
				case "scopes":
					list := innerV.List()
					for i := 0; i < list.Len(); i++ {
						fi.Scopes = append(fi.Scopes, list.Get(i).String())
					}
				}
				return true
			})
			return false
		}
		return true
	})
	if fi != nil && fi.FeatureID == "" {
		return nil
	}
	return fi
}

func indexScopes(scopes []ScopeInfo) map[string]ScopeInfo {
	m := make(map[string]ScopeInfo, len(scopes))
	for _, s := range scopes {
		m[s.Name] = s
	}
	return m
}

func indexFeatureScopes(features []FeatureScopeInfo) map[string][]string {
	m := make(map[string][]string, len(features))
	for _, f := range features {
		m[f.FeatureID] = f.Scopes
	}
	return m
}

func diffStrings(base, current []string) (added, removed []string) {
	baseSet := map[string]bool{}
	for _, s := range base {
		baseSet[s] = true
	}
	curSet := map[string]bool{}
	for _, s := range current {
		curSet[s] = true
	}
	for _, s := range current {
		if !baseSet[s] {
			added = append(added, s)
		}
	}
	for _, s := range base {
		if !curSet[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func flagFeatureID(flagID string) string {
	if i := strings.Index(flagID, "/"); i >= 0 {
		return flagID[:i]
	}
	return flagID
}

func toPascalCase(s string) string {
	var b strings.Builder
	upper := true
	for _, r := range s {
		if r == '_' || r == '-' {
			upper = true
			continue
		}
		if upper {
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
