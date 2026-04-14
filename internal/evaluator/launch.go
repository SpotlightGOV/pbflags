package evaluator

import (
	"fmt"
	"hash/fnv"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// CachedLaunch holds a launch's evaluation metadata (dimension and ramp).
// Launch-to-flag binding is expressed inline on individual conditions
// via StoredCondition.LaunchID/LaunchValue — the CachedLaunch only
// carries the information needed for hash evaluation and cache key
// construction.
type CachedLaunch struct {
	LaunchID  string
	Dimension string // hashable dimension name (e.g. "user_id")
	RampPct   int    // 0-100
}

// InRamp checks if the entity is in this launch's ramp bucket.
func (l *CachedLaunch) InRamp(evalCtx proto.Message) bool {
	dimValue := extractDimensionValue(evalCtx, l.Dimension)
	// Scope enforcement guarantees the dimension is present. Hash all values
	// including empty strings — deterministic bucket assignment, no panic.
	return HashBucket(l.LaunchID, dimValue) < l.RampPct
}

// HashBucket returns a deterministic bucket 0-99 for a (launch, dimension) pair.
// Uses FNV-32a for fast, well-distributed hashing.
// WARNING: Changing this function re-buckets all users. Do not modify.
func HashBucket(launchID, dimValue string) int {
	h := fnv.New32a()
	h.Write([]byte(launchID))
	h.Write([]byte{0}) // separator
	h.Write([]byte(dimValue))
	return int(h.Sum32() % 100)
}

// extractDimensionValue reads a named field from a proto message and returns its string representation.
// Returns "" for unset fields — the caller hashes all values including empty strings.
func extractDimensionValue(msg proto.Message, dimName string) string {
	rm := msg.ProtoReflect()
	fd := rm.Descriptor().Fields().ByName(protoreflect.Name(dimName))
	if fd == nil {
		return ""
	}
	v := rm.Get(fd)
	switch fd.Kind() {
	case protoreflect.StringKind:
		return v.String()
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return fmt.Sprintf("%d", v.Int())
	case protoreflect.BoolKind:
		if v.Bool() {
			return "true"
		}
		return "false"
	case protoreflect.EnumKind:
		return fmt.Sprintf("%d", v.Enum())
	default:
		return ""
	}
}
