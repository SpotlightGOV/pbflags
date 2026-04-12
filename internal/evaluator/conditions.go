package evaluator

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

// CachedCondition is a compiled condition ready for evaluation.
type CachedCondition struct {
	Program cel.Program          // compiled CEL program; nil for "otherwise"
	Value   *pbflagsv1.FlagValue // the value to return when this condition matches
}

// ConditionEvaluator compiles stored condition JSON into CEL programs and
// evaluates them against an EvaluationContext proto message.
type ConditionEvaluator struct {
	compiler    *celenv.Compiler
	contextDesc protoreflect.MessageDescriptor
	logger      *slog.Logger
}

// NewConditionEvaluator creates a ConditionEvaluator from the EvaluationContext
// message descriptor. Returns nil if md is nil (no conditions support).
func NewConditionEvaluator(md protoreflect.MessageDescriptor, logger *slog.Logger) (*ConditionEvaluator, error) {
	if md == nil {
		return nil, nil
	}
	compiler, err := celenv.NewCompiler(md)
	if err != nil {
		return nil, fmt.Errorf("create CEL compiler: %w", err)
	}
	return &ConditionEvaluator{
		compiler:    compiler,
		contextDesc: md,
		logger:      logger,
	}, nil
}

// storedCondition is the JSON format stored in the conditions JSONB column.
type storedCondition struct {
	CEL   *string         `json:"cel"`
	Value json.RawMessage `json:"value"`
}

// CompileConditions parses the conditions JSONB and compiles CEL programs.
// Returns nil if conditionsJSON is nil (no conditions). On compile failure,
// logs the error and returns nil (graceful degradation to default).
func (ce *ConditionEvaluator) CompileConditions(flagID string, conditionsJSON []byte) []CachedCondition {
	if len(conditionsJSON) == 0 {
		return nil
	}

	var stored []storedCondition
	if err := json.Unmarshal(conditionsJSON, &stored); err != nil {
		ce.logger.Error("failed to parse conditions JSON", "flag_id", flagID, "error", err)
		return nil
	}

	conditions := make([]CachedCondition, 0, len(stored))
	for i, sc := range stored {
		fv := &pbflagsv1.FlagValue{}
		if err := protojson.Unmarshal(sc.Value, fv); err != nil {
			ce.logger.Error("failed to unmarshal condition value", "flag_id", flagID, "index", i, "error", err)
			return nil
		}

		if sc.CEL == nil {
			// "otherwise" clause — no program, just the value.
			conditions = append(conditions, CachedCondition{Value: fv})
			continue
		}

		compiled, err := ce.compiler.Compile(*sc.CEL)
		if err != nil {
			ce.logger.Error("failed to compile CEL condition", "flag_id", flagID, "index", i, "cel", *sc.CEL, "error", err)
			return nil // degrade: fall back to compiled default
		}
		conditions = append(conditions, CachedCondition{Program: compiled.Program, Value: fv})
	}

	return conditions
}

// EvaluateConditions iterates the condition chain and returns the value of
// the first matching condition. Returns nil if no condition matches or if
// evalCtx is nil.
func (ce *ConditionEvaluator) EvaluateConditions(conditions []CachedCondition, evalCtx proto.Message) *pbflagsv1.FlagValue {
	if len(conditions) == 0 || evalCtx == nil {
		return nil
	}

	activation := map[string]any{"ctx": evalCtx}
	for _, cond := range conditions {
		if cond.Program == nil {
			// "otherwise" — always matches.
			return cond.Value
		}
		out, _, err := cond.Program.Eval(activation)
		if err != nil {
			ce.logger.Debug("CEL evaluation error", "error", err)
			continue // skip failed condition, try next
		}
		if out.Value() == true {
			return cond.Value
		}
	}
	return nil
}

// UnmarshalContext deserializes an anypb.Any into a dynamic proto message
// matching the EvaluationContext type. Returns nil if the Any is nil.
func (ce *ConditionEvaluator) UnmarshalContext(anyCtx *anypb.Any) (proto.Message, error) {
	if anyCtx == nil || len(anyCtx.Value) == 0 {
		return nil, nil
	}
	msg := dynamicpb.NewMessage(ce.contextDesc)
	if err := proto.Unmarshal(anyCtx.Value, msg); err != nil {
		return nil, fmt.Errorf("unmarshal evaluation context: %w", err)
	}
	return msg, nil
}
