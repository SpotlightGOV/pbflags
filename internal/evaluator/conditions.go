package evaluator

import (
	"fmt"
	"log/slog"

	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
)

// CachedCondition is a compiled condition ready for evaluation.
type CachedCondition struct {
	Program     cel.Program          // compiled CEL program; nil for "otherwise"
	Value       *pbflagsv1.FlagValue // the value to return when this condition matches
	Source      string               // original CEL expression text (empty for "otherwise")
	LaunchID    string               // launch override ID; empty if no override
	LaunchValue *pbflagsv1.FlagValue // value when entity is in launch ramp; nil if no override
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

// CompileConditions parses the stored conditions proto and compiles CEL programs.
// Returns nil if conditionsData is nil (no conditions). On compile failure,
// logs the error and returns nil (graceful degradation to default).
func (ce *ConditionEvaluator) CompileConditions(flagID string, conditionsData []byte) []CachedCondition {
	if len(conditionsData) == 0 {
		return nil
	}

	var stored pbflagsv1.StoredConditions
	if err := proto.Unmarshal(conditionsData, &stored); err != nil {
		ce.logger.Error("failed to parse stored conditions", "flag_id", flagID, "error", err)
		return nil
	}

	conditions := make([]CachedCondition, 0, len(stored.Conditions))
	for i, sc := range stored.Conditions {
		fv := &pbflagsv1.FlagValue{}
		if err := proto.Unmarshal(sc.Value, fv); err != nil {
			ce.logger.Error("failed to unmarshal condition value", "flag_id", flagID, "index", i, "error", err)
			return nil
		}

		cc := CachedCondition{Value: fv}

		// Parse launch override if present.
		if sc.LaunchId != "" && len(sc.LaunchValue) > 0 {
			lv := &pbflagsv1.FlagValue{}
			if err := proto.Unmarshal(sc.LaunchValue, lv); err != nil {
				ce.logger.Error("failed to unmarshal launch value", "flag_id", flagID, "index", i, "launch_id", sc.LaunchId, "error", err)
				// Degrade: ignore the launch override, use base value.
			} else {
				cc.LaunchID = sc.LaunchId
				cc.LaunchValue = lv
			}
		}

		if sc.Cel == "" {
			// "otherwise" clause — no program, just the value.
			conditions = append(conditions, cc)
			continue
		}

		compiled, err := ce.compiler.Compile(sc.Cel)
		if err != nil {
			ce.logger.Error("failed to compile CEL condition", "flag_id", flagID, "index", i, "cel", sc.Cel, "error", err)
			return nil // degrade: fall back to compiled default
		}
		cc.Program = compiled.Program
		cc.Source = sc.Cel
		conditions = append(conditions, cc)
	}

	return conditions
}

// CompileExpression compiles a single CEL expression to a program.
// Returns nil on compile failure (logs error).
func (ce *ConditionEvaluator) CompileExpression(expr string) cel.Program {
	compiled, err := ce.compiler.Compile(expr)
	if err != nil {
		ce.logger.Error("failed to compile CEL expression", "cel", expr, "error", err)
		return nil
	}
	return compiled.Program
}

// EvalResult holds the result and metadata from condition evaluation.
type EvalResult struct {
	Value             *pbflagsv1.FlagValue
	ConditionsChecked int    // how many CEL programs were evaluated
	LaunchHit         bool   // true if the returned value came from a launch override
	LaunchID          string // non-empty if LaunchHit
	OverrideHit       bool   // true if a live condition-value override was applied
}

// EvaluateConditions iterates the condition chain and returns the value of
// the first matching condition. When a matched condition has a launch
// override and the launch is active, checks if the entity is in the
// launch ramp and returns the launch value if so.
//
// The launches map is keyed by launch ID → CachedLaunch for active launches.
// Nil or empty map means no active launches.
func (ce *ConditionEvaluator) EvaluateConditions(flagID string, conditions []CachedCondition, evalCtx proto.Message, launches ...CachedLaunch) *EvalResult {
	return ce.EvaluateConditionsWithOverrides(flagID, conditions, evalCtx, nil, launches...)
}

// EvaluateConditionsWithOverrides is EvaluateConditions plus a per-condition
// override map. When a condition matches and overrides[i] is set, the
// override value replaces the compiled condition value. Launches still take
// precedence — if a launch is in ramp for the matched condition, the launch
// value wins (overrides and launches are orthogonal per the design doc).
func (ce *ConditionEvaluator) EvaluateConditionsWithOverrides(
	flagID string,
	conditions []CachedCondition,
	evalCtx proto.Message,
	overrides map[int32]*pbflagsv1.FlagValue,
	launches ...CachedLaunch,
) *EvalResult {
	if len(conditions) == 0 || evalCtx == nil {
		return &EvalResult{}
	}

	// Build launch lookup for O(1) access.
	var launchMap map[string]*CachedLaunch
	if len(launches) > 0 {
		launchMap = make(map[string]*CachedLaunch, len(launches))
		for i := range launches {
			launchMap[launches[i].LaunchID] = &launches[i]
		}
	}

	checked := 0
	activation := map[string]any{"ctx": evalCtx}
	for i, cond := range conditions {
		if cond.Program == nil {
			// "otherwise" — always matches.
			return ce.buildResult(cond, int32(i), overrides, launchMap, evalCtx, checked)
		}
		checked++
		out, _, err := cond.Program.Eval(activation)
		if err != nil {
			ce.logger.Warn("CEL evaluation error",
				"flag_id", flagID,
				"cond_index", i,
				"cel", cond.Source,
				"error", err)
			continue // skip failed condition, try next
		}
		if b, ok := out.Value().(bool); ok && b {
			return ce.buildResult(cond, int32(i), overrides, launchMap, evalCtx, checked)
		}
	}
	return &EvalResult{ConditionsChecked: checked}
}

// buildResult layers override and launch decisions on top of a matched
// condition. Precedence: launch (if entity in ramp) > override > compiled
// condition value.
func (ce *ConditionEvaluator) buildResult(
	cond CachedCondition,
	condIdx int32,
	overrides map[int32]*pbflagsv1.FlagValue,
	launchMap map[string]*CachedLaunch,
	evalCtx proto.Message,
	checked int,
) *EvalResult {
	// Resolve the base value: override (if any) wins over compiled value.
	base := cond.Value
	overrideHit := false
	if ov, ok := overrides[condIdx]; ok && ov != nil {
		base = ov
		overrideHit = true
	}

	// Launch override (if entity is in ramp) wins over both.
	val, launchID := applyLaunchOverrideValue(cond, base, launchMap, evalCtx)
	if launchID != "" {
		return &EvalResult{
			Value:             val,
			ConditionsChecked: checked,
			LaunchHit:         true,
			LaunchID:          launchID,
			OverrideHit:       false, // launch took precedence; report launch as the source
		}
	}
	return &EvalResult{
		Value:             base,
		ConditionsChecked: checked,
		OverrideHit:       overrideHit,
	}
}

// applyLaunchOverrideValue checks if a matched condition has a launch
// override and if the entity is in the launch ramp. Returns (value,
// launchID); launchID is empty when no launch override was applied. The
// `base` argument is the value to return when no launch fires (i.e. either
// the compiled value or a live override).
func applyLaunchOverrideValue(cond CachedCondition, base *pbflagsv1.FlagValue, launchMap map[string]*CachedLaunch, evalCtx proto.Message) (*pbflagsv1.FlagValue, string) {
	if cond.LaunchID == "" || cond.LaunchValue == nil {
		return base, ""
	}
	launch, ok := launchMap[cond.LaunchID]
	if !ok {
		return base, ""
	}
	if launch.InRamp(evalCtx) {
		return cond.LaunchValue, cond.LaunchID
	}
	return base, ""
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
