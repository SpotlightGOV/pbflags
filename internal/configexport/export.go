// Package configexport generates YAML flag configuration files from existing
// database state. This is the migration bridge from DB-driven to config-driven
// flags: global values become static entries, per-entity overrides become
// ctx.<entityDim> conditions.
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
	"github.com/SpotlightGOV/pbflags/internal/flagfmt"
)

// ExportedConfig is a single feature's generated YAML config.
type ExportedConfig struct {
	FeatureID string
	YAML      []byte
}

// Options configures the export.
type Options struct {
	// EntityDimension is the context dimension name used for per-entity
	// override conditions (e.g., "user_id", "account_id"). Required when
	// the database has overrides. The exporter does not assume a default.
	EntityDimension string
}

// Export reads all non-archived features and their flags from the database
// and generates one YAML config per feature.
func Export(ctx context.Context, pool *pgxpool.Pool, opts Options) ([]ExportedConfig, error) {
	features, err := loadFeatures(ctx, pool)
	if err != nil {
		return nil, err
	}

	var configs []ExportedConfig
	for _, f := range features {
		data, err := generateYAML(f, opts)
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
	flagID    string
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
	// Single query for all flags (includes flag_id to avoid N+1 on overrides).
	rows, err := pool.Query(ctx, `
		SELECT fl.flag_id, fl.feature_id, fl.display_name, fl.flag_type, fl.state, fl.value
		FROM feature_flags.flags fl
		WHERE fl.archived_at IS NULL
		ORDER BY fl.feature_id, fl.field_number`)
	if err != nil {
		return nil, fmt.Errorf("query flags: %w", err)
	}
	defer rows.Close()

	featureMap := map[string]*feature{}
	var featureOrder []string
	var allFlagIDs []string
	flagIndex := map[string]*flag{} // flag_id → *flag

	for rows.Next() {
		var flagID, featureID, flagName, flagType, state string
		var valueBytes []byte
		if err := rows.Scan(&flagID, &featureID, &flagName, &flagType, &state, &valueBytes); err != nil {
			return nil, fmt.Errorf("scan flag: %w", err)
		}

		f, ok := featureMap[featureID]
		if !ok {
			f = &feature{id: featureID}
			featureMap[featureID] = f
			featureOrder = append(featureOrder, featureID)
		}

		val, err := unmarshalValue(valueBytes)
		if err != nil {
			return nil, fmt.Errorf("unmarshal value for %s: %w", flagID, err)
		}

		fl := flag{
			flagID:   flagID,
			name:     flagName,
			flagType: flagType,
			state:    state,
			value:    val,
		}
		f.flags = append(f.flags, fl)
		allFlagIDs = append(allFlagIDs, flagID)
		flagIndex[flagID] = &f.flags[len(f.flags)-1]
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Bulk load all overrides in one query.
	if len(allFlagIDs) > 0 {
		if err := loadAllOverrides(ctx, pool, allFlagIDs, flagIndex); err != nil {
			return nil, err
		}
	}

	result := make([]feature, 0, len(featureOrder))
	for _, id := range featureOrder {
		result = append(result, *featureMap[id])
	}
	return result, nil
}

func loadAllOverrides(ctx context.Context, pool *pgxpool.Pool, flagIDs []string, index map[string]*flag) error {
	rows, err := pool.Query(ctx, `
		SELECT flag_id, entity_id, state, value
		FROM feature_flags.flag_overrides
		WHERE flag_id = ANY($1)
		ORDER BY flag_id, entity_id`, flagIDs)
	if err != nil {
		return fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var flagID, entityID, state string
		var valueBytes []byte
		if err := rows.Scan(&flagID, &entityID, &state, &valueBytes); err != nil {
			return fmt.Errorf("scan override: %w", err)
		}
		val, err := unmarshalValue(valueBytes)
		if err != nil {
			return fmt.Errorf("unmarshal override value for %s/%s: %w", flagID, entityID, err)
		}
		if fl, ok := index[flagID]; ok {
			fl.overrides = append(fl.overrides, override{entityID: entityID, state: state, value: val})
		}
	}
	return rows.Err()
}

func unmarshalValue(b []byte) (*pbflagsv1.FlagValue, error) {
	if len(b) == 0 {
		return nil, nil
	}
	v := &pbflagsv1.FlagValue{}
	if err := proto.Unmarshal(b, v); err != nil {
		return nil, err
	}
	return v, nil
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

func generateYAML(f feature, opts Options) ([]byte, error) {
	cfg := yamlConfig{
		Feature: f.id,
		Flags:   make(map[string]yamlEntry, len(f.flags)),
	}

	for _, fl := range f.flags {
		entry, err := buildFlagEntry(fl, opts)
		if err != nil {
			return nil, fmt.Errorf("flag %q: %w", fl.name, err)
		}
		cfg.Flags[fl.name] = entry
	}

	var b strings.Builder
	b.WriteString("# Generated by: pbflags config export\n")
	fmt.Fprintf(&b, "# Feature: %s\n", f.id)
	b.WriteString("# Review and commit to your config directory.\n\n")

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	b.Write(data)
	return []byte(b.String()), nil
}

func buildFlagEntry(fl flag, opts Options) (yamlEntry, error) {
	// Collect overrides that produce active behavior changes.
	activeOverrides := filterActiveOverrides(fl.overrides)
	if len(activeOverrides) > 0 {
		if opts.EntityDimension == "" {
			return yamlEntry{}, fmt.Errorf("flag %q has overrides but no --entity-dimension specified", fl.name)
		}
		return buildConditionEntry(fl, activeOverrides, opts.EntityDimension)
	}

	val, err := flagfmt.AsAny(fl.value)
	if err != nil {
		return yamlEntry{}, err
	}
	if val == nil {
		val = typedZero(fl.flagType)
	}
	return yamlEntry{Value: val}, nil
}

func buildConditionEntry(fl flag, overrides []override, entityDim string) (yamlEntry, error) {
	var conditions []yamlCondition

	// Group ENABLED overrides by value for cleaner conditions.
	type group struct {
		entityIDs []string
		value     any
	}
	groups := map[string]*group{}
	var groupOrder []string

	for _, o := range overrides {
		if o.state != "ENABLED" {
			continue
		}
		val, err := flagfmt.AsAny(o.value)
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
		when := buildWhen(entityDim, g.entityIDs)
		conditions = append(conditions, yamlCondition{When: when, Value: g.value})
	}

	// KILLED overrides: emit a condition returning the typed zero (compiled
	// default) for those entities, preserving the "this entity is suppressed"
	// semantics from the DB.
	var killedIDs []string
	for _, o := range overrides {
		if o.state == "KILLED" {
			killedIDs = append(killedIDs, o.entityID)
		}
	}
	if len(killedIDs) > 0 {
		sort.Strings(killedIDs)
		when := buildWhen(entityDim, killedIDs)
		conditions = append(conditions, yamlCondition{When: when, Value: typedZero(fl.flagType)})
	}

	// Otherwise: use the global value or typed zero.
	otherwiseVal, err := flagfmt.AsAny(fl.value)
	if err != nil {
		return yamlEntry{}, err
	}
	if otherwiseVal == nil {
		otherwiseVal = typedZero(fl.flagType)
	}
	conditions = append(conditions, yamlCondition{Otherwise: otherwiseVal})

	return yamlEntry{Conditions: conditions}, nil
}

func buildWhen(entityDim string, entityIDs []string) string {
	if len(entityIDs) == 1 {
		return fmt.Sprintf("ctx.%s == %s", entityDim, celStringLiteral(entityIDs[0]))
	}
	sort.Strings(entityIDs)
	quoted := make([]string, len(entityIDs))
	for i, id := range entityIDs {
		quoted[i] = celStringLiteral(id)
	}
	return fmt.Sprintf("ctx.%s in [%s]", entityDim, strings.Join(quoted, ", "))
}

// celStringLiteral produces a CEL-compatible double-quoted string literal.
// CEL string grammar supports \" and \\ escapes; we escape only those plus
// control characters, avoiding Go-specific escape sequences like \a, \v.
func celStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20: // other control chars
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// filterActiveOverrides returns overrides that have behavioral impact:
// ENABLED with a value, or KILLED (which suppresses the flag).
func filterActiveOverrides(overrides []override) []override {
	var result []override
	for _, o := range overrides {
		switch {
		case o.state == "ENABLED" && o.value != nil:
			result = append(result, o)
		case o.state == "KILLED":
			result = append(result, o)
		}
	}
	return result
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
