package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/SpotlightGOV/pbflags/internal/configfile"
)

// launchDef holds a parsed launch definition and its source.
type launchDef struct {
	Entry          configfile.LaunchEntry
	ScopeFeatureID string // non-empty for feature-scoped launches
}

// launchCollection holds the result of collecting and validating launches
// across all feature configs and the launches/ subdirectory.
type launchCollection struct {
	Defined map[string]launchDef       // launchID → definition
	Refs    map[string]map[string]bool // launchID → set of referencing featureIDs
}

// collectLaunches gathers launch definitions and references from parsed configs
// and the launches/ subdirectory. Returns an error if validation fails
// (duplicate IDs, missing references, scope violations, non-UNIFORM dimensions).
func CollectLaunches(
	configs map[string]*configfile.Config,
	configDir string,
	hashableDims map[string]bool,
) (*launchCollection, error) {
	lc := &launchCollection{
		Defined: map[string]launchDef{},
		Refs:    map[string]map[string]bool{},
	}

	// Collect feature-scoped launches and all references.
	for featureID, cfg := range configs {
		for launchID, entry := range cfg.Launches {
			if _, dup := lc.Defined[launchID]; dup {
				return nil, fmt.Errorf("launch %q defined in multiple features", launchID)
			}
			lc.Defined[launchID] = launchDef{Entry: entry, ScopeFeatureID: featureID}
		}

		for _, flagEntry := range cfg.Flags {
			if flagEntry.Launch != nil {
				addRef(lc.Refs, flagEntry.Launch.ID, featureID)
			}
			for _, cond := range flagEntry.Conditions {
				if cond.Launch != nil {
					addRef(lc.Refs, cond.Launch.ID, featureID)
				}
			}
		}
	}

	// Collect cross-feature launches from launches/ subdirectory.
	launchesDir := filepath.Join(configDir, "launches")
	if entries, err := os.ReadDir(launchesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !isYAML(entry.Name()) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(launchesDir, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("read launch file %s: %w", entry.Name(), err)
			}
			launchID := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			launch, err := configfile.ParseCrossFeatureLaunch(data)
			if err != nil {
				return nil, fmt.Errorf("launch %s: %w", launchID, err)
			}
			if _, dup := lc.Defined[launchID]; dup {
				return nil, fmt.Errorf("launch %q defined in both a feature config and launches/ directory", launchID)
			}
			lc.Defined[launchID] = launchDef{Entry: launch, ScopeFeatureID: ""}
		}
	}

	// Validate references exist and scope enforcement.
	for launchID, refs := range lc.Refs {
		def, exists := lc.Defined[launchID]
		if !exists {
			var features []string
			for f := range refs {
				features = append(features, f)
			}
			sort.Strings(features)
			return nil, fmt.Errorf("launch %q referenced by features %v but not defined in any config or launches/ directory", launchID, features)
		}
		if def.ScopeFeatureID != "" {
			for refFeature := range refs {
				if refFeature != def.ScopeFeatureID {
					return nil, fmt.Errorf("launch %q is scoped to feature %q but referenced from feature %q; move the launch to launches/ to use it across features", launchID, def.ScopeFeatureID, refFeature)
				}
			}
		}
	}

	// Validate dimensions are UNIFORM.
	for launchID, def := range lc.Defined {
		if !hashableDims[def.Entry.Dimension] {
			return nil, fmt.Errorf("launch %q: dimension %q is not marked UNIFORM in proto", launchID, def.Entry.Dimension)
		}
	}

	return lc, nil
}

// AffectedFeatures returns the sorted list of features that reference a launch.
func (lc *launchCollection) AffectedFeatures(launchID string) []string {
	refs := lc.Refs[launchID]
	if len(refs) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(refs))
	for f := range refs {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// IDs returns all defined launch IDs.
func (lc *launchCollection) IDs() []string {
	out := make([]string, 0, len(lc.Defined))
	for id := range lc.Defined {
		out = append(out, id)
	}
	return out
}

func addRef(refs map[string]map[string]bool, launchID, featureID string) {
	if refs[launchID] == nil {
		refs[launchID] = map[string]bool{}
	}
	refs[launchID][featureID] = true
}
