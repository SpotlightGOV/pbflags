package evaluator

import (
	"sync/atomic"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Defaults is an immutable map of flag_id → FlagDef, built from descriptors.pb.
type Defaults struct {
	flags map[string]FlagDef
}

// NewDefaults creates a Defaults from parsed flag definitions.
func NewDefaults(defs []FlagDef) *Defaults {
	m := make(map[string]FlagDef, len(defs))
	for _, d := range defs {
		m[d.FlagID] = d
	}
	return &Defaults{flags: m}
}

// Get returns the flag definition and its default value, or false if unknown.
func (d *Defaults) Get(flagID string) (FlagDef, bool) {
	def, ok := d.flags[flagID]
	return def, ok
}

// DefaultValue returns the compiled default FlagValue for a flag, or nil.
func (d *Defaults) DefaultValue(flagID string) *pbflagsv1.FlagValue {
	def, ok := d.flags[flagID]
	if !ok || def.Default == nil {
		return nil
	}
	return def.Default
}

// FlagIDs returns all known flag IDs.
func (d *Defaults) FlagIDs() []string {
	ids := make([]string, 0, len(d.flags))
	for id := range d.flags {
		ids = append(ids, id)
	}
	return ids
}

// Len returns the number of flags in the registry.
func (d *Defaults) Len() int {
	return len(d.flags)
}

// Registry provides atomic access to the current Defaults snapshot.
type Registry struct {
	current atomic.Pointer[Defaults]
}

// NewRegistry creates a Registry with the given initial defaults.
func NewRegistry(initial *Defaults) *Registry {
	r := &Registry{}
	r.current.Store(initial)
	return r
}

// Load returns the current Defaults snapshot.
func (r *Registry) Load() *Defaults {
	return r.current.Load()
}

// Swap atomically replaces the defaults with a new snapshot.
func (r *Registry) Swap(next *Defaults) {
	r.current.Store(next)
}
