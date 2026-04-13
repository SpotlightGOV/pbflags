// Package configexport generates YAML flag configuration files from existing
// database state. This is the migration bridge from DB-driven to config-driven
// flags: global values become static entries, per-entity overrides become
// ctx.entity_id conditions.
package configexport

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// ExportedConfig is a single feature's generated YAML config.
type ExportedConfig struct {
	FeatureID string
	YAML      []byte
}

// Export reads all non-archived features and their flags from the database
// and generates one YAML config per feature.
func Export(ctx context.Context, pool *pgxpool.Pool) ([]ExportedConfig, error) {
	features, err := loadFeatures(ctx, pool)
	if err != nil {
		return nil, err
	}

	var configs []ExportedConfig
	for _, f := range features {
		data, err := generateYAML(f)
		if err != nil {
			return nil, fmt.Errorf("feature %q: %w", f.id, err)
		}
		configs = append(configs, ExportedConfig{FeatureID: f.id, YAML: data})
	}
	return configs, nil
}

type feature struct {
	id    string
	flags []flag
}

type flag struct {
	name      string
	flagType  string
	state     string
	value     *pbflagsv1.FlagValue
	overrides []override
}

type override struct {
	entityID string
	state    string
	value    *pbflagsv1.FlagValue
}

func loadFeatures(ctx context.Context, pool *pgxpool.Pool) ([]feature, error) {
	rows, err := pool.Query(ctx, `
		SELECT fl.feature_id, fl.display_name, fl.flag_type, fl.state, fl.value
		FROM feature_flags.flags fl
		WHERE fl.archived_at IS NULL
		ORDER BY fl.feature_id, fl.field_number`)
	if err != nil {
		return nil, fmt.Errorf("query flags: %w", err)
	}
	defer rows.Close()

	featureMap := map[string]*feature{}
	var featureOrder []string

	for rows.Next() {
		var featureID, flagName, flagType, state string
		var valueBytes []byte
		if err := rows.Scan(&featureID, &flagName, &flagType, &state, &valueBytes); err != nil {
			return nil, fmt.Errorf("scan flag: %w", err)
		}

		f, ok := featureMap[featureID]
		if !ok {
			f = &feature{id: featureID}
			featureMap[featureID] = f
			featureOrder = append(featureOrder, featureID)
		}

		var val *pbflagsv1.FlagValue
		if len(valueBytes) > 0 {
			val = &pbflagsv1.FlagValue{}
			if err := proto.Unmarshal(valueBytes, val); err != nil {
				val = nil
			}
		}

		f.flags = append(f.flags, flag{
			name:     flagName,
			flagType: flagType,
			state:    state,
			value:    val,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load overrides for all flags.
	for _, f := range featureMap {
		for i := range f.flags {
			overrides, err := loadOverrides(ctx, pool, f.id, f.flags[i].name)
			if err != nil {
				return nil, err
			}
			f.flags[i].overrides = overrides
		}
	}

	result := make([]feature, 0, len(featureOrder))
	for _, id := range featureOrder {
		result = append(result, *featureMap[id])
	}
	return result, nil
}

func loadOverrides(ctx context.Context, pool *pgxpool.Pool, featureID, flagName string) ([]override, error) {
	// Flag IDs in the overrides table use the feature_id/field_number format,
	// but we queried by display_name. We need the flag_id.
	var flagID string
	err := pool.QueryRow(ctx, `
		SELECT flag_id FROM feature_flags.flags
		WHERE feature_id = $1 AND display_name = $2`,
		featureID, flagName).Scan(&flagID)
	if err != nil {
		return nil, nil // no flag found — skip
	}

	rows, err := pool.Query(ctx, `
		SELECT entity_id, state, value
		FROM feature_flags.flag_overrides
		WHERE flag_id = $1
		ORDER BY entity_id`, flagID)
	if err != nil {
		return nil, fmt.Errorf("query overrides for %s: %w", flagID, err)
	}
	defer rows.Close()

	var overrides []override
	for rows.Next() {
		var entityID, state string
		var valueBytes []byte
		if err := rows.Scan(&entityID, &state, &valueBytes); err != nil {
			return nil, err
		}
		var val *pbflagsv1.FlagValue
		if len(valueBytes) > 0 {
			val = &pbflagsv1.FlagValue{}
			if err := proto.Unmarshal(valueBytes, val); err != nil {
				val = nil
			}
		}
		overrides = append(overrides, override{entityID: entityID, state: state, value: val})
	}
	return overrides, rows.Err()
}

// yamlConfig matches the YAML structure expected by configfile.Parse.
type yamlConfig struct {
	Feature string               `yaml:"feature"`
	Flags   map[string]yamlEntry `yaml:"flags"`
}

type yamlEntry struct {
	Value      any             `yaml:"value,omitempty"`
	Conditions []yamlCondition `yaml:"conditions,omitempty"`
}

type yamlCondition struct {
	When      string `yaml:"when,omitempty"`
	Otherwise any    `yaml:"otherwise,omitempty"`
	Value     any    `yaml:"value,omitempty"`
}

func generateYAML(f feature) ([]byte, error) {
	cfg := yamlConfig{
		Feature: f.id,
		Flags:   make(map[string]yamlEntry, len(f.flags)),
	}

	for _, fl := range f.flags {
		entry, err := buildFlagEntry(fl)
		if err != nil {
			return nil, fmt.Errorf("flag %q: %w", fl.name, err)
		}
		cfg.Flags[fl.name] = entry
	}

	// Generate with a comment header.
	var b strings.Builder
	b.WriteString("# Generated by: pbflags config export\n")
	b.WriteString(fmt.Sprintf("# Feature: %s\n", f.id))
	b.WriteString("# Review and commit to your config directory.\n\n")

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	b.Write(data)
	return []byte(b.String()), nil
}

func buildFlagEntry(fl flag) (yamlEntry, error) {
	// If the flag has overrides, generate a condition chain.
	enabledOverrides := filterEnabledOverrides(fl.overrides)
	if len(enabledOverrides) > 0 {
		return buildConditionEntry(fl, enabledOverrides)
	}

	// Static value.
	val, err := flagValueToYAML(fl.value)
	if err != nil {
		return yamlEntry{}, err
	}
	if val == nil {
		// Flag is DEFAULT or has no server-side value — use a typed zero.
		val = typedZero(fl.flagType)
	}
	return yamlEntry{Value: val}, nil
}

func buildConditionEntry(fl flag, overrides []override) (yamlEntry, error) {
	var conditions []yamlCondition

	// Group overrides by value to produce cleaner conditions.
	type group struct {
		entityIDs []string
		value     any
	}
	groups := map[string]*group{} // key: serialized value
	var groupOrder []string

	for _, o := range overrides {
		val, err := flagValueToYAML(o.value)
		if err != nil {
			return yamlEntry{}, err
		}
		key := fmt.Sprintf("%v", val)
		g, ok := groups[key]
		if !ok {
			g = &group{value: val}
			groups[key] = g
			groupOrder = append(groupOrder, key)
		}
		g.entityIDs = append(g.entityIDs, o.entityID)
	}

	for _, key := range groupOrder {
		g := groups[key]
		var when string
		if len(g.entityIDs) == 1 {
			when = fmt.Sprintf(`ctx.user_id == %q`, g.entityIDs[0])
		} else {
			sort.Strings(g.entityIDs)
			quoted := make([]string, len(g.entityIDs))
			for i, id := range g.entityIDs {
				quoted[i] = fmt.Sprintf("%q", id)
			}
			when = fmt.Sprintf(`ctx.user_id in [%s]`, strings.Join(quoted, ", "))
		}
		conditions = append(conditions, yamlCondition{When: when, Value: g.value})
	}

	// Otherwise clause: use the global value or typed zero.
	otherwiseVal, err := flagValueToYAML(fl.value)
	if err != nil {
		return yamlEntry{}, err
	}
	if otherwiseVal == nil {
		otherwiseVal = typedZero(fl.flagType)
	}
	conditions = append(conditions, yamlCondition{Otherwise: otherwiseVal})

	return yamlEntry{Conditions: conditions}, nil
}

func filterEnabledOverrides(overrides []override) []override {
	var result []override
	for _, o := range overrides {
		if o.state == "ENABLED" && o.value != nil {
			result = append(result, o)
		}
	}
	return result
}

func flagValueToYAML(fv *pbflagsv1.FlagValue) (any, error) {
	if fv == nil {
		return nil, nil
	}
	switch v := fv.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return v.BoolValue, nil
	case *pbflagsv1.FlagValue_StringValue:
		return v.StringValue, nil
	case *pbflagsv1.FlagValue_Int64Value:
		return v.Int64Value, nil
	case *pbflagsv1.FlagValue_DoubleValue:
		return v.DoubleValue, nil
	case *pbflagsv1.FlagValue_BoolListValue:
		if v.BoolListValue == nil {
			return []bool{}, nil
		}
		return v.BoolListValue.Values, nil
	case *pbflagsv1.FlagValue_StringListValue:
		if v.StringListValue == nil {
			return []string{}, nil
		}
		return v.StringListValue.Values, nil
	case *pbflagsv1.FlagValue_Int64ListValue:
		if v.Int64ListValue == nil {
			return []int64{}, nil
		}
		return v.Int64ListValue.Values, nil
	case *pbflagsv1.FlagValue_DoubleListValue:
		if v.DoubleListValue == nil {
			return []float64{}, nil
		}
		return v.DoubleListValue.Values, nil
	default:
		return nil, nil
	}
}

func typedZero(flagType string) any {
	switch flagType {
	case "BOOL":
		return false
	case "STRING":
		return ""
	case "INT64":
		return int64(0)
	case "DOUBLE":
		return float64(0)
	case "BOOL_LIST":
		return []bool{}
	case "STRING_LIST":
		return []string{}
	case "INT64_LIST":
		return []int64{}
	case "DOUBLE_LIST":
		return []float64{}
	default:
		return nil
	}
}
