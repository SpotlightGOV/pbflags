package configcli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"gopkg.in/yaml.v3"
)

// Show renders the effective condition chain for a flag as a human-readable table.
func Show(descriptorData []byte, configDir, flagQuery string, w io.Writer) error {
	defs, err := evaluator.ParseDescriptors(descriptorData)
	if err != nil {
		return fmt.Errorf("parse descriptors: %w", err)
	}

	// Parse flagQuery as "feature/flagname" or just "flagname".
	featureID, flagName := parseFlagQuery(flagQuery)

	// Find matching flag definition.
	var matchedDef *evaluator.FlagDef
	for i := range defs {
		if defs[i].Name == flagName && (featureID == "" || defs[i].FeatureID == featureID) {
			matchedDef = &defs[i]
			featureID = defs[i].FeatureID
			break
		}
	}
	if matchedDef == nil {
		return fmt.Errorf("flag %q not found in proto descriptors", flagQuery)
	}

	// Build flag type map for the feature.
	flagTypes := map[string]pbflagsv1.FlagType{}
	for _, d := range defs {
		if d.FeatureID == featureID {
			flagTypes[d.Name] = d.FlagType
		}
	}

	// Find and parse the config file for this feature.
	cfg, err := findFeatureConfig(configDir, featureID, flagTypes)
	if err != nil {
		return err
	}

	entry, ok := cfg.Flags[flagName]
	if !ok {
		return fmt.Errorf("flag %q not found in config for feature %q", flagName, featureID)
	}

	// Render.
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Flag:\t%s/%s\n", featureID, flagName)
	fmt.Fprintf(tw, "Type:\t%s\n", matchedDef.FlagType.String())
	fmt.Fprintln(tw)

	if entry.Value != nil {
		fmt.Fprintf(tw, "Mode:\tStatic\n")
		fmt.Fprintf(tw, "Value:\t%v\n", formatFlagValue(entry.Value))
	} else if len(entry.Conditions) > 0 {
		fmt.Fprintf(tw, "Mode:\tConditions (%d rules)\n", len(entry.Conditions))
		fmt.Fprintln(tw)
		fmt.Fprintf(tw, "#\tCondition\tValue\n")
		fmt.Fprintf(tw, "-\t---------\t-----\n")
		for i, cond := range entry.Conditions {
			when := cond.When
			if when == "" {
				when = "(otherwise)"
			}
			fmt.Fprintf(tw, "%d\t%s\t%v\n", i+1, when, formatFlagValue(cond.Value))
		}
	}

	return tw.Flush()
}

func parseFlagQuery(q string) (featureID, flagName string) {
	if idx := strings.LastIndex(q, "/"); idx >= 0 {
		return q[:idx], q[idx+1:]
	}
	return "", q
}

func findFeatureConfig(configDir, featureID string, flagTypes map[string]pbflagsv1.FlagType) (*configfile.Config, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, fmt.Errorf("read config directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}
		path := filepath.Join(configDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		var peek struct {
			Feature string `yaml:"feature"`
		}
		if err := yaml.Unmarshal(data, &peek); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		if peek.Feature != featureID {
			continue
		}
		cfg, _, err := configfile.Parse(data, flagTypes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no config file found for feature %q in %s", featureID, configDir)
}

func formatFlagValue(fv *pbflagsv1.FlagValue) string {
	if fv == nil {
		return "(nil)"
	}
	switch v := fv.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return fmt.Sprintf("%v", v.BoolValue)
	case *pbflagsv1.FlagValue_StringValue:
		return fmt.Sprintf("%q", v.StringValue)
	case *pbflagsv1.FlagValue_Int64Value:
		return fmt.Sprintf("%d", v.Int64Value)
	case *pbflagsv1.FlagValue_DoubleValue:
		return fmt.Sprintf("%g", v.DoubleValue)
	case *pbflagsv1.FlagValue_BoolListValue:
		return fmt.Sprintf("%v", v.BoolListValue.GetValues())
	case *pbflagsv1.FlagValue_StringListValue:
		return fmt.Sprintf("%v", v.StringListValue.GetValues())
	case *pbflagsv1.FlagValue_Int64ListValue:
		return fmt.Sprintf("%v", v.Int64ListValue.GetValues())
	case *pbflagsv1.FlagValue_DoubleListValue:
		return fmt.Sprintf("%v", v.DoubleListValue.GetValues())
	default:
		return fmt.Sprintf("%v", fv)
	}
}
