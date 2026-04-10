package evaluator

import (
	"sync"
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry_LoadAndSwap(t *testing.T) {
	initial := &Defaults{flags: map[string]FlagDef{
		"f/1": {FlagID: "f/1", Default: boolVal(true)},
	}}
	reg := NewRegistry(initial)

	d := reg.Load()
	require.Equal(t, 1, d.Len(), "initial len")

	next := &Defaults{flags: map[string]FlagDef{
		"f/1": {FlagID: "f/1"},
		"f/2": {FlagID: "f/2"},
	}}
	reg.Swap(next)

	d = reg.Load()
	require.Equal(t, 2, d.Len(), "after swap len")
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := NewRegistry(&Defaults{flags: make(map[string]FlagDef)})
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := reg.Load()
			_ = d.FlagIDs()
		}()
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Swap(&Defaults{flags: map[string]FlagDef{
				"f/1": {FlagID: "f/1"},
			}})
		}()
	}

	wg.Wait()
}

func TestDefaults_Get(t *testing.T) {
	d := NewDefaults([]FlagDef{
		{FlagID: "f/1", FeatureID: "f", FieldNum: 1, FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL, Default: boolVal(true)},
		{FlagID: "f/2", FeatureID: "f", FieldNum: 2, FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING},
	})

	def, ok := d.Get("f/1")
	require.True(t, ok, "expected f/1 found")
	require.Equal(t, pbflagsv1.FlagType_FLAG_TYPE_BOOL, def.FlagType, "type")

	_, ok = d.Get("nonexistent")
	require.False(t, ok, "expected nonexistent not found")
}

func TestDefaults_DefaultValue(t *testing.T) {
	d := NewDefaults([]FlagDef{
		{FlagID: "f/1", Default: boolVal(true)},
		{FlagID: "f/2"},
	})

	v := d.DefaultValue("f/1")
	require.NotNil(t, v, "DefaultValue(f/1)")
	require.Equal(t, true, v.GetBoolValue(), "DefaultValue(f/1)")

	require.Nil(t, d.DefaultValue("f/2"), "DefaultValue(f/2)")
	require.Nil(t, d.DefaultValue("nonexistent"), "DefaultValue(nonexistent)")
}

func TestDefaults_FlagIDs(t *testing.T) {
	d := NewDefaults([]FlagDef{
		{FlagID: "a/1"},
		{FlagID: "b/2"},
		{FlagID: "c/3"},
	})

	ids := d.FlagIDs()
	require.Len(t, ids, 3, "FlagIDs len")

	idSet := make(map[string]struct{})
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	for _, want := range []string{"a/1", "b/2", "c/3"} {
		assert.Contains(t, idSet, want, "FlagIDs missing %q", want)
	}
}

func TestFlagDef_IsGlobalLayer(t *testing.T) {
	tests := []struct {
		layer string
		want  bool
	}{
		{"", true},
		{"global", true},
		{"GLOBAL", true},
		{"user", false},
		{"entity", false},
	}

	for _, tt := range tests {
		def := FlagDef{Layer: tt.layer}
		assert.Equal(t, tt.want, def.IsGlobalLayer(), "IsGlobalLayer(layer=%q)", tt.layer)
	}
}
