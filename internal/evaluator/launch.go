package evaluator

import (
	"fmt"
	"hash/fnv"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// CachedLaunch holds a compiled launch ready for evaluation.
type CachedLaunch struct {
	LaunchID   string
	Dimension  string               // hashable dimension name (e.g. "user_id")
	Population cel.Program          // compiled population predicate; nil = all entities
	Value      *pbflagsv1.FlagValue // value for entities in the ramp
	RampPct    int                  // 0-100
}

// EvaluateLaunches determines if the entity falls within any active launch's ramp.
// Returns the launch value and launch ID if matched, or (nil, "") otherwise.
// Launches are evaluated in order; first match wins.
func EvaluateLaunches(launches []CachedLaunch, evalCtx proto.Message) (*pbflagsv1.FlagValue, string) {
	if evalCtx == nil {
		return nil, ""
	}
	for i := range launches {
		launch := &launches[i]

		// 1. Check population restriction.
		if launch.Population != nil {
			out, _, err := launch.Population.Eval(map[string]any{"ctx": evalCtx})
			if err != nil || !out.Value().(bool) {
				continue // not in population
			}
		}

		// 2. Extract dimension value from context.
		dimValue := extractDimensionValue(evalCtx, launch.Dimension)
		if dimValue == "" {
			continue // dimension not set in context
		}

		// 3. Hash check: hash(launch_id + dimension_value) % 100 < ramp_pct
		bucket := HashBucket(launch.LaunchID, dimValue)
		if bucket < launch.RampPct {
			return launch.Value, launch.LaunchID
		}
	}
	return nil, ""
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
func extractDimensionValue(msg proto.Message, dimName string) string {
	rm := msg.ProtoReflect()
	fd := rm.Descriptor().Fields().ByName(protoreflect.Name(dimName))
	if fd == nil {
		return ""
	}
	v := rm.Get(fd)
	switch fd.Kind() {
	case protoreflect.StringKind:
		s := v.String()
		if s == "" {
			return ""
		}
		return s
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
