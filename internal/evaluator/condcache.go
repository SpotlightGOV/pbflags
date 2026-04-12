package evaluator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dgraph-io/ristretto/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

// ConditionCache caches condition evaluation results keyed by
// dimension-classified cache keys. Flags without conditions use
// the flag ID alone as the cache key (one entry).
type ConditionCache struct {
	cache *ristretto.Cache[string, *pbflagsv1.FlagValue]
}

// NewConditionCache creates a condition result cache with the given max entries.
func NewConditionCache(maxEntries int64) (*ConditionCache, error) {
	if maxEntries <= 0 {
		maxEntries = 10_000
	}
	cache, err := ristretto.NewCache(&ristretto.Config[string, *pbflagsv1.FlagValue]{
		NumCounters: maxEntries * 10,
		MaxCost:     maxEntries,
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}
	return &ConditionCache{cache: cache}, nil
}

// Get looks up a cached condition result.
func (c *ConditionCache) Get(key string) (*pbflagsv1.FlagValue, bool) {
	return c.cache.Get(key)
}

// Set stores a condition result. cost=1 per entry for LRU counting.
func (c *ConditionCache) Set(key string, val *pbflagsv1.FlagValue) {
	c.cache.Set(key, val, 1)
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
// flag ID, dimension metadata, and evaluation context.
// For flags with no conditions/metadata, returns just the flag ID.
func BuildCacheKey(flagID string, meta CachedDimMeta, evalCtx proto.Message) string {
	if len(meta) == 0 || evalCtx == nil {
		return flagID
	}

	rm := evalCtx.ProtoReflect()
	fields := rm.Descriptor().Fields()

	// Sort dimension names for deterministic keys.
	names := make([]string, 0, len(meta))
	for name := range meta {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(flagID)

	for _, name := range names {
		dm := meta[name]
		fd := fields.ByName(protoreflect.Name(name))
		if fd == nil {
			continue
		}
		val := rm.Get(fd)

		b.WriteByte('|')
		b.WriteString(name)

		switch dm.Classification {
		case celenv.Bounded:
			// Include full dimension value.
			b.WriteByte('=')
			b.WriteString(formatFieldValue(fd, val))

		case celenv.FiniteFilterUniform:
			// Check if context value matches any literal in the set.
			b.WriteString(":match=")
			if matchesLiteral(fd, val, dm.LiteralSet) {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}

		case celenv.FiniteFilterDistinct:
			// Check which specific literal matches (or "none").
			b.WriteString(":match=")
			if lit := findMatchingLiteral(fd, val, dm.LiteralSet); lit != "" {
				b.WriteString(lit)
			} else {
				b.WriteString("none")
			}

		case celenv.Unbounded:
			// Include raw dimension value (LRU-capped by the cache).
			b.WriteByte('=')
			b.WriteString(formatFieldValue(fd, val))
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
