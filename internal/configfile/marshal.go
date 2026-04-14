package configfile

import (
	"fmt"
	"sort"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"gopkg.in/yaml.v3"
)

// Marshal serializes a Config back to canonical YAML. The output is
// deterministic (sorted flag and launch keys) and suitable for use as
// a canonical formatter. Round-tripping through Parse → Marshal produces
// output that, when re-parsed, yields an identical Config — any drift
// indicates lossy parsing.
func Marshal(cfg *Config) ([]byte, error) {
	raw := marshalConfig(cfg)
	return yamlMarshal(raw)
}

// MarshalCrossFeatureLaunch serializes a standalone cross-feature launch
// entry to canonical YAML.
func MarshalCrossFeatureLaunch(entry LaunchEntry) ([]byte, error) {
	raw := marshalLaunchEntry(entry)
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	doc.Content = append(doc.Content, launchEntryNode(raw))
	return encodeNode(doc)
}

func marshalConfig(cfg *Config) *rawConfig {
	raw := &rawConfig{
		Feature: cfg.Feature,
	}

	if len(cfg.Launches) > 0 {
		raw.Launches = make(map[string]rawLaunchEntry, len(cfg.Launches))
		for id, l := range cfg.Launches {
			raw.Launches[id] = marshalLaunchEntry(l)
		}
	}

	if len(cfg.Flags) > 0 {
		raw.Flags = make(map[string]rawFlagEntry, len(cfg.Flags))
		for name, f := range cfg.Flags {
			raw.Flags[name] = marshalFlagEntry(f)
		}
	}

	return raw
}

func marshalLaunchEntry(l LaunchEntry) rawLaunchEntry {
	return rawLaunchEntry{
		Dimension:      l.Dimension,
		RampPercentage: l.RampPercentage,
		Description:    l.Description,
	}
}

func marshalFlagEntry(f FlagEntry) rawFlagEntry {
	entry := rawFlagEntry{}
	if f.Value != nil {
		entry.Value = flagValueToRaw(f.Value)
		entry.hasValue = true
	}
	if f.Launch != nil {
		entry.Launch = &rawLaunchOverride{
			ID:    f.Launch.ID,
			Value: flagValueToRaw(f.Launch.Value),
		}
	}
	if len(f.Conditions) > 0 {
		entry.Conditions = make([]rawCondition, len(f.Conditions))
		for i, c := range f.Conditions {
			entry.Conditions[i] = marshalCondition(c)
		}
	}
	return entry
}

func marshalCondition(c Condition) rawCondition {
	rc := rawCondition{comment: c.Comment}
	if c.When != "" {
		rc.When = c.When
		rc.Value = flagValueToRaw(c.Value)
		rc.hasValue = true
	} else {
		rc.Otherwise = flagValueToRaw(c.Value)
		rc.hasOther = true
	}
	if c.Launch != nil {
		rc.Launch = &rawLaunchOverride{
			ID:    c.Launch.ID,
			Value: flagValueToRaw(c.Launch.Value),
		}
	}
	return rc
}

func flagValueToRaw(fv *pbflagsv1.FlagValue) any {
	if fv == nil {
		return nil
	}
	switch v := fv.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return v.BoolValue
	case *pbflagsv1.FlagValue_StringValue:
		return v.StringValue
	case *pbflagsv1.FlagValue_Int64Value:
		return v.Int64Value
	case *pbflagsv1.FlagValue_DoubleValue:
		return v.DoubleValue
	case *pbflagsv1.FlagValue_BoolListValue:
		vals := v.BoolListValue.GetValues()
		out := make([]any, len(vals))
		for i, b := range vals {
			out[i] = b
		}
		return out
	case *pbflagsv1.FlagValue_StringListValue:
		vals := v.StringListValue.GetValues()
		out := make([]any, len(vals))
		for i, s := range vals {
			out[i] = s
		}
		return out
	case *pbflagsv1.FlagValue_Int64ListValue:
		vals := v.Int64ListValue.GetValues()
		out := make([]any, len(vals))
		for i, n := range vals {
			out[i] = n
		}
		return out
	case *pbflagsv1.FlagValue_DoubleListValue:
		vals := v.DoubleListValue.GetValues()
		out := make([]any, len(vals))
		for i, d := range vals {
			out[i] = d
		}
		return out
	default:
		return nil
	}
}

// yamlMarshal produces canonical YAML from a rawConfig using yaml.Node
// to control key ordering: feature, launches, flags (sorted).
func yamlMarshal(raw *rawConfig) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	root := &yaml.Node{Kind: yaml.MappingNode}
	doc.Content = append(doc.Content, root)

	// feature:
	root.Content = append(root.Content,
		scalarNode("feature"), scalarNode(raw.Feature))

	// launches: (sorted by ID)
	if len(raw.Launches) > 0 {
		launchesMap := &yaml.Node{Kind: yaml.MappingNode}
		ids := sortedKeys(raw.Launches)
		for _, id := range ids {
			l := raw.Launches[id]
			launchesMap.Content = append(launchesMap.Content,
				scalarNode(id), launchEntryNode(l))
		}
		root.Content = append(root.Content,
			scalarNode("launches"), launchesMap)
	}

	// flags: (sorted by name)
	if len(raw.Flags) > 0 {
		flagsMap := &yaml.Node{Kind: yaml.MappingNode}
		names := sortedKeys(raw.Flags)
		for _, name := range names {
			f := raw.Flags[name]
			flagsMap.Content = append(flagsMap.Content,
				scalarNode(name), flagEntryNode(f))
		}
		root.Content = append(root.Content,
			scalarNode("flags"), flagsMap)
	}

	return encodeNode(doc)
}

func launchEntryNode(l rawLaunchEntry) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	m.Content = append(m.Content,
		scalarNode("dimension"), scalarNode(l.Dimension))
	if l.RampPercentage != nil {
		m.Content = append(m.Content,
			scalarNode("ramp_percentage"), intNode(*l.RampPercentage))
	}
	if l.Description != "" {
		m.Content = append(m.Content,
			scalarNode("description"), scalarNode(l.Description))
	}
	return m
}

func flagEntryNode(f rawFlagEntry) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if f.hasValue {
		m.Content = append(m.Content,
			scalarNode("value"), valueNode(f.Value))
		if f.Launch != nil {
			m.Content = append(m.Content,
				scalarNode("launch"), launchOverrideNode(f.Launch))
		}
	}
	if len(f.Conditions) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, c := range f.Conditions {
			seq.Content = append(seq.Content, conditionNode(c))
		}
		m.Content = append(m.Content,
			scalarNode("conditions"), seq)
	}
	return m
}

func conditionNode(c rawCondition) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if c.comment != "" {
		m.HeadComment = c.comment
	}
	if c.When != "" {
		m.Content = append(m.Content,
			scalarNode("when"), scalarNode(c.When))
		m.Content = append(m.Content,
			scalarNode("value"), valueNode(c.Value))
	} else {
		m.Content = append(m.Content,
			scalarNode("otherwise"), valueNode(c.Otherwise))
	}
	if c.Launch != nil {
		m.Content = append(m.Content,
			scalarNode("launch"), launchOverrideNode(c.Launch))
	}
	return m
}

func launchOverrideNode(lo *rawLaunchOverride) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	m.Content = append(m.Content,
		scalarNode("id"), scalarNode(lo.ID))
	m.Content = append(m.Content,
		scalarNode("value"), valueNode(lo.Value))
	return m
}

func valueNode(v any) *yaml.Node {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		n.Kind = yaml.ScalarNode
		n.Value = fmt.Sprintf("%v", v)
	}
	return n
}

func scalarNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v, Tag: "!!str"}
}

func intNode(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: fmt.Sprintf("%d", v), Tag: "!!int"}
}

func encodeNode(doc *yaml.Node) ([]byte, error) {
	var buf []byte
	enc := yaml.NewEncoder(writerFunc(func(p []byte) (int, error) {
		buf = append(buf, p...)
		return len(p), nil
	}))
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf, nil
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
