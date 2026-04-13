package evaluator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dgraph-io/ristretto/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

// condCacheEntry wraps a condition evaluation result. A nil Value with
// noMatch=true is a cached "no condition matched" sentinel — avoids
// re-evaluating the full CEL chain on every no-match context.
type condCacheEntry struct {
	Value   *pbflagsv1.FlagValue
	NoMatch bool
}

// ConditionCache caches condition evaluation results keyed by
// dimension-classified cache keys with per-flag version stamps
// for invalidation on config changes.
type ConditionCache struct {
	cache    *ristretto.Cache[string, *condCacheEntry]
	mu       sync.RWMutex
	versions map[string]uint64 // flagID → version counter
}

// NewConditionCache creates a condition result cache with the given max entries.
func NewConditionCache(maxEntries int64) (*ConditionCache, error) {
	if maxEntries <= 0 {
		maxEntries = 10_000
	}
	cache, err := ristretto.NewCache(&ristretto.Config[string, *condCacheEntry]{
		NumCounters: maxEntries * 10,
		MaxCost:     maxEntries,
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}
	return &ConditionCache{
		cache:    cache,
		versions: make(map[string]uint64),
	}, nil
}

// Get looks up a cached condition result. Returns (value, noMatch, found).
// When noMatch is true, the CEL chain was evaluated and no condition matched.
func (c *ConditionCache) Get(key string) (val *pbflagsv1.FlagValue, noMatch bool, found bool) {
	entry, ok := c.cache.Get(key)
	if !ok {
		return nil, false, false
	}
	return entry.Value, entry.NoMatch, true
}

// Set stores a condition match result.
func (c *ConditionCache) Set(key string, val *pbflagsv1.FlagValue) {
	c.cache.Set(key, &condCacheEntry{Value: val}, 1)
}

// SetNoMatch stores a "no condition matched" sentinel.
func (c *ConditionCache) SetNoMatch(key string) {
	c.cache.Set(key, &condCacheEntry{NoMatch: true}, 1)
}

// InvalidateFlag bumps the version for a flag, making all existing
// cache entries for that flag unreachable (they have the old version
// in their key). Old entries are evicted naturally by LRU.
func (c *ConditionCache) InvalidateFlag(flagID string) {
	c.mu.Lock()
	c.versions[flagID]++
	c.mu.Unlock()
}

// FlagVersion returns the current cache version for a flag.
func (c *ConditionCache) FlagVersion(flagID string) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.versions[flagID]
}

// Close releases cache resources.
func (c *ConditionCache) Close() {
	c.cache.Close()
}

// Clear evicts all entries.
func (c *ConditionCache) Clear() {
	c.cache.Clear()
}

// Wait blocks until all pending writes are processed.
func (c *ConditionCache) Wait() {
	c.cache.Wait()
}

// CachedDimMeta holds the deserialized dimension metadata for a flag,
// loaded from the dimension_metadata JSONB column.
type CachedDimMeta map[string]*celenv.DimensionMeta

// BuildCacheKey constructs a dimension-classified cache key from the
// flag ID, version stamp, dimension metadata, and evaluation context.
// Values are length-prefixed to prevent delimiter collision.
func BuildCacheKey(flagID string, version uint64, meta CachedDimMeta, evalCtx proto.Message) string {
	if len(meta) == 0 || evalCtx == nil {
		return fmt.Sprintf("%s@%d", flagID, version)
	}

	rm := evalCtx.ProtoReflect()
	fields := rm.Descriptor().Fields()

	names := make([]string, 0, len(meta))
	for name := range meta {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "%s@%d", flagID, version)

	for _, name := range names {
		dm := meta[name]
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil {
			continue
		}
		val := rm.Get(fd)

		b.WriteByte('\x00') // NUL separator — cannot appear in UTF-8 proto strings
		b.WriteString(name)
		b.WriteByte('\x00')

		switch dm.Classification {
		case celenv.Bounded, celenv.Unbounded:
			v := formatFieldValue(fd, val)
			fmt.Fprintf(&b, "%d:%s", len(v), v)

		case celenv.FiniteFilterUniform:
			if matchesLiteral(fd, val, dm.LiteralSet) {
				b.WriteByte('1')
			} else {
				b.WriteByte('0')
			}

		case celenv.FiniteFilterDistinct:
			if lit := findMatchingLiteral(fd, val, dm.LiteralSet); lit != "" {
				fmt.Fprintf(&b, "%d:%s", len(lit), lit)
			} else {
				b.WriteByte('-')
			}
		}
	}

	return b.String()
}

// ParseDimMeta deserializes the dimension_metadata JSONB.
func ParseDimMeta(data []byte) CachedDimMeta {
	if len(data) == 0 {
		return nil
	}
	var meta CachedDimMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return meta
}

func formatFieldValue(fd protoreflect.FieldDescriptor, val protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return val.String()
	case protoreflect.BoolKind:
		if val.Bool() {
			return "true"
		}
		return "false"
	case protoreflect.EnumKind:
		return fmt.Sprintf("%d", val.Enum())
	case protoreflect.Int64Kind:
		return fmt.Sprintf("%d", val.Int())
	default:
		return fmt.Sprintf("%v", val.Interface())
	}
}

func matchesLiteral(fd protoreflect.FieldDescriptor, val protoreflect.Value, literals []string) bool {
	v := formatFieldValue(fd, val)
	for _, lit := range literals {
		if v == lit {
			return true
		}
	}
	return false
}

func findMatchingLiteral(fd protoreflect.FieldDescriptor, val protoreflect.Value, literals []string) string {
	v := formatFieldValue(fd, val)
	for _, lit := range literals {
		if v == lit {
			return lit
		}
	}
	return ""
}
