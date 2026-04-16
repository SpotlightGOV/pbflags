package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/authn"
)

// AdminService implements the FlagAdminService Connect handler.
type AdminService struct {
	store                 *Store
	logger                *slog.Logger
	allowRuntimeOverrides bool
}

// AdminServiceOption configures optional AdminService behavior.
type AdminServiceOption func(*AdminService)

// WithAllowRuntimeOverrides enables every state-changing RPC: condition
// overrides, sync lock acquire/release, flag state updates (kill/unkill),
// and launch ramp/status/kill/unkill. Default is disabled — operators
// opt in via the --allow-runtime-overrides server flag (default true at
// the binary level; off only on locked-down read-only deployments).
func WithAllowRuntimeOverrides() AdminServiceOption {
	return func(a *AdminService) { a.allowRuntimeOverrides = true }
}

// NewAdminService creates a FlagAdminService handler.
func NewAdminService(store *Store, logger *slog.Logger, opts ...AdminServiceOption) *AdminService {
	a := &AdminService{store: store, logger: logger}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *AdminService) ListFeatures(ctx context.Context, _ *connect.Request[pbflagsv1.ListFeaturesRequest]) (*connect.Response[pbflagsv1.ListFeaturesResponse], error) {
	features, _, err := a.store.ListFeatures(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.ListFeaturesResponse{Features: features}), nil
}

func (a *AdminService) GetFlag(ctx context.Context, req *connect.Request[pbflagsv1.GetFlagRequest]) (*connect.Response[pbflagsv1.GetFlagResponse], error) {
	flagID := req.Msg.FlagId
	if flagID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	flag, _, err := a.store.GetFlag(ctx, flagID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if flag == nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&pbflagsv1.GetFlagResponse{Flag: flag}), nil
}

func (a *AdminService) UpdateFlagState(ctx context.Context, req *connect.Request[pbflagsv1.UpdateFlagStateRequest]) (*connect.Response[pbflagsv1.UpdateFlagStateResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	msg := req.Msg
	if msg.FlagId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if msg.State == pbflagsv1.State_STATE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Prefer authenticated identity; fall back to request-level actor field.
	actor := authn.SubjectFromContext(ctx, msg.Actor)

	a.logger.Info("updating flag state",
		"flag_id", msg.FlagId, "state", msg.State.String(), "actor", actor)

	if err := a.store.UpdateFlagState(ctx, msg.FlagId, msg.State, actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.UpdateFlagStateResponse{}), nil
}

func (a *AdminService) SetFlagOverride(_ context.Context, _ *connect.Request[pbflagsv1.SetFlagOverrideRequest]) (*connect.Response[pbflagsv1.SetFlagOverrideResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("per-entity overrides have been removed; use conditions instead"))
}

func (a *AdminService) RemoveFlagOverride(_ context.Context, _ *connect.Request[pbflagsv1.RemoveFlagOverrideRequest]) (*connect.Response[pbflagsv1.RemoveFlagOverrideResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("per-entity overrides have been removed; use conditions instead"))
}

func (a *AdminService) GetAuditLog(ctx context.Context, req *connect.Request[pbflagsv1.GetAuditLogRequest]) (*connect.Response[pbflagsv1.GetAuditLogResponse], error) {
	entries, err := a.store.GetAuditLog(ctx, AuditLogFilter{FlagID: req.Msg.FlagId, Limit: req.Msg.Limit})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.GetAuditLogResponse{Entries: entries}), nil
}

// ── Launch RPCs ─────────────────────────────────────────────────────

func (a *AdminService) ListLaunches(ctx context.Context, req *connect.Request[pbflagsv1.ListLaunchesRequest]) (*connect.Response[pbflagsv1.ListLaunchesResponse], error) {
	var launches []Launch
	var err error
	if fid := req.Msg.GetFeatureId(); fid != "" {
		launches, err = a.store.ListLaunchesAffecting(ctx, fid)
	} else {
		launches, err = a.store.ListAllLaunches(ctx)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.ListLaunchesResponse{
		Launches: launchesToProto(launches),
	}), nil
}

func (a *AdminService) GetLaunch(ctx context.Context, req *connect.Request[pbflagsv1.GetLaunchRequest]) (*connect.Response[pbflagsv1.GetLaunchResponse], error) {
	if req.Msg.GetLaunchId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	l, err := a.store.GetLaunch(ctx, req.Msg.GetLaunchId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if l == nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&pbflagsv1.GetLaunchResponse{
		Launch: launchToProto(l),
	}), nil
}

func (a *AdminService) UpdateLaunchRamp(ctx context.Context, req *connect.Request[pbflagsv1.UpdateLaunchRampRequest]) (*connect.Response[pbflagsv1.UpdateLaunchRampResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetLaunchId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	source := req.Msg.GetSource()
	if source == "" {
		source = "cli" // default for API callers
	}
	actor := authn.SubjectFromContext(ctx, "api")
	a.logger.Info("updating launch ramp",
		"launch_id", req.Msg.GetLaunchId(), "ramp_pct", req.Msg.GetRampPercentage(), "source", source, "actor", actor)
	prevSource, err := a.store.UpdateLaunchRamp(ctx, req.Msg.GetLaunchId(), int(req.Msg.GetRampPercentage()), source, actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &pbflagsv1.UpdateLaunchRampResponse{}
	if prevSource == "config" {
		resp.Warning = "ramp_percentage is defined in config; this change will be overwritten on next sync"
	}
	return connect.NewResponse(resp), nil
}

// validLaunchStatuses is the set of allowed launch lifecycle statuses,
// matching the DB CHECK constraint.
var validLaunchStatuses = map[string]bool{
	"CREATED": true, "ACTIVE": true, "SOAKING": true,
	"COMPLETED": true, "ABANDONED": true,
}

func (a *AdminService) UpdateLaunchStatus(ctx context.Context, req *connect.Request[pbflagsv1.UpdateLaunchStatusRequest]) (*connect.Response[pbflagsv1.UpdateLaunchStatusResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetLaunchId() == "" || req.Msg.GetStatus() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if !validLaunchStatuses[req.Msg.GetStatus()] {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("invalid status %q; must be one of CREATED, ACTIVE, SOAKING, COMPLETED, ABANDONED", req.Msg.GetStatus()))
	}
	actor := authn.SubjectFromContext(ctx, "api")
	a.logger.Info("updating launch status",
		"launch_id", req.Msg.GetLaunchId(), "status", req.Msg.GetStatus(), "actor", actor)
	if err := a.store.UpdateLaunchStatus(ctx, req.Msg.GetLaunchId(), req.Msg.GetStatus(), actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.UpdateLaunchStatusResponse{}), nil
}

func (a *AdminService) KillLaunch(ctx context.Context, req *connect.Request[pbflagsv1.KillLaunchRequest]) (*connect.Response[pbflagsv1.KillLaunchResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetLaunchId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	actor := authn.SubjectFromContext(ctx, "api")
	a.logger.Info("killing launch", "launch_id", req.Msg.GetLaunchId(), "actor", actor)
	if err := a.store.KillLaunch(ctx, req.Msg.GetLaunchId(), actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.KillLaunchResponse{}), nil
}

func (a *AdminService) UnkillLaunch(ctx context.Context, req *connect.Request[pbflagsv1.UnkillLaunchRequest]) (*connect.Response[pbflagsv1.UnkillLaunchResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetLaunchId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	actor := authn.SubjectFromContext(ctx, "api")
	a.logger.Info("unkilling launch", "launch_id", req.Msg.GetLaunchId(), "actor", actor)
	if err := a.store.UnkillLaunch(ctx, req.Msg.GetLaunchId(), actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.UnkillLaunchResponse{}), nil
}

// ── Sync lock RPCs ──────────────────────────────────────────────────

func (a *AdminService) AcquireSyncLock(ctx context.Context, req *connect.Request[pbflagsv1.AcquireSyncLockRequest]) (*connect.Response[pbflagsv1.AcquireSyncLockResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reason is required"))
	}
	actor := authn.SubjectFromContext(ctx, "")
	a.logger.Info("acquiring sync lock", "actor", actor, "reason", req.Msg.GetReason())
	info, err := a.store.AcquireSyncLock(ctx, actor, req.Msg.GetReason())
	if err != nil {
		var held *SyncLockHeldError
		if errors.As(err, &held) {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("sync is already locked by %s since %s: %s",
					held.Info.Actor, held.Info.CreatedAt.Format(time.RFC3339), held.Info.Reason))
		}
		return nil, mapStoreErr(err)
	}
	return connect.NewResponse(&pbflagsv1.AcquireSyncLockResponse{
		HeldSince: timestamppb.New(info.CreatedAt),
	}), nil
}

func (a *AdminService) ReleaseSyncLock(ctx context.Context, req *connect.Request[pbflagsv1.ReleaseSyncLockRequest]) (*connect.Response[pbflagsv1.ReleaseSyncLockResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reason is required"))
	}
	actor := authn.SubjectFromContext(ctx, "")
	a.logger.Info("releasing sync lock", "actor", actor, "reason", req.Msg.GetReason())
	if err := a.store.ReleaseSyncLock(ctx, actor, req.Msg.GetReason()); err != nil {
		if errors.Is(err, ErrSyncNotLocked) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, mapStoreErr(err)
	}
	return connect.NewResponse(&pbflagsv1.ReleaseSyncLockResponse{}), nil
}

// GetSyncLock is gated by --allow-condition-overrides for symmetry with
// Acquire/Release: if the feature isn't on, no part of the lock surface is
// addressable.
func (a *AdminService) GetSyncLock(ctx context.Context, _ *connect.Request[pbflagsv1.GetSyncLockRequest]) (*connect.Response[pbflagsv1.GetSyncLockResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	info, err := a.store.GetSyncLock(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &pbflagsv1.GetSyncLockResponse{}
	if info != nil {
		resp.Held = true
		resp.Actor = info.Actor
		resp.Reason = info.Reason
		resp.HeldSince = timestamppb.New(info.CreatedAt)
	}
	return connect.NewResponse(resp), nil
}

// ── Condition override RPCs ─────────────────────────────────────────

func (a *AdminService) SetConditionOverride(ctx context.Context, req *connect.Request[pbflagsv1.SetConditionOverrideRequest]) (*connect.Response[pbflagsv1.SetConditionOverrideResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetFlagId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("flag_id is required"))
	}
	if req.Msg.GetValue() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("value is required"))
	}
	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reason is required"))
	}
	source := SourceProtoToString(req.Msg.GetSource())
	actor := authn.SubjectFromContext(ctx, "")

	var condIdx *int32
	if req.Msg.ConditionIndex != nil {
		v := req.Msg.GetConditionIndex()
		condIdx = &v
	}

	a.logger.Info("setting condition override",
		"flag_id", req.Msg.GetFlagId(), "cond_index", formatCondIndex(condIdx),
		"actor", actor, "source", source)

	// Look up the original chain value (or static default) for the response.
	// Done before the write so a concurrent sync that clears overrides won't
	// race with this read.
	flag, _, gErr := a.store.GetFlag(ctx, req.Msg.GetFlagId())
	if gErr != nil {
		return nil, connect.NewError(connect.CodeInternal, gErr)
	}
	if flag == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("flag %s not found", req.Msg.GetFlagId()))
	}
	if vErr := validateConditionIndex(flag, condIdx); vErr != nil {
		return nil, vErr
	}
	var originalValue *pbflagsv1.FlagValue
	if condIdx == nil {
		originalValue = flag.GetDefaultValue()
	} else {
		idx := int(*condIdx)
		originalValue = flag.GetConditions()[idx].GetValue()
	}

	prev, err := a.store.SetConditionOverride(ctx, req.Msg.GetFlagId(), condIdx, req.Msg.GetValue(), source, actor, req.Msg.GetReason())
	if err != nil {
		return nil, mapStoreErr(err)
	}

	resp := &pbflagsv1.SetConditionOverrideResponse{
		OriginalValue:         originalValue,
		PreviousOverrideValue: prev,
	}
	managed, mgErr := a.store.IsConfigManaged(ctx, req.Msg.GetFlagId())
	if mgErr == nil && managed {
		resp.Warning = "this flag is managed by config-as-code; the next successful sync will clear this override"
	}
	return connect.NewResponse(resp), nil
}

func (a *AdminService) ClearConditionOverride(ctx context.Context, req *connect.Request[pbflagsv1.ClearConditionOverrideRequest]) (*connect.Response[pbflagsv1.ClearConditionOverrideResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetFlagId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("flag_id is required"))
	}
	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reason is required"))
	}
	actor := authn.SubjectFromContext(ctx, "")

	var condIdx *int32
	if req.Msg.ConditionIndex != nil {
		v := req.Msg.GetConditionIndex()
		condIdx = &v
	}

	// Validate condition_index against the chain shape before touching
	// the store. Symmetric with SetConditionOverride: callers should not
	// be able to address an "otherwise" row by index — that override
	// shape can never have been created in the first place.
	if condIdx != nil {
		flag, _, gErr := a.store.GetFlag(ctx, req.Msg.GetFlagId())
		if gErr != nil {
			return nil, connect.NewError(connect.CodeInternal, gErr)
		}
		if flag == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("flag %s not found", req.Msg.GetFlagId()))
		}
		if vErr := validateConditionIndex(flag, condIdx); vErr != nil {
			return nil, vErr
		}
	}

	a.logger.Info("clearing condition override",
		"flag_id", req.Msg.GetFlagId(), "cond_index", formatCondIndex(condIdx), "actor", actor, "reason", req.Msg.GetReason())
	if err := a.store.ClearConditionOverride(ctx, req.Msg.GetFlagId(), condIdx, actor, req.Msg.GetReason()); err != nil {
		return nil, mapStoreErr(err)
	}
	return connect.NewResponse(&pbflagsv1.ClearConditionOverrideResponse{}), nil
}

func (a *AdminService) ClearAllConditionOverrides(ctx context.Context, req *connect.Request[pbflagsv1.ClearAllConditionOverridesRequest]) (*connect.Response[pbflagsv1.ClearAllConditionOverridesResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	if req.Msg.GetFlagId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("flag_id is required"))
	}
	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reason is required"))
	}
	actor := authn.SubjectFromContext(ctx, "")
	a.logger.Info("clearing all condition overrides", "flag_id", req.Msg.GetFlagId(), "actor", actor, "reason", req.Msg.GetReason())
	count, err := a.store.ClearAllConditionOverrides(ctx, req.Msg.GetFlagId(), actor, req.Msg.GetReason())
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return connect.NewResponse(&pbflagsv1.ClearAllConditionOverridesResponse{ClearedCount: int32(count)}), nil
}

func (a *AdminService) ListConditionOverrides(ctx context.Context, req *connect.Request[pbflagsv1.ListConditionOverridesRequest]) (*connect.Response[pbflagsv1.ListConditionOverridesResponse], error) {
	if !a.allowRuntimeOverrides {
		return nil, errRuntimeOverridesDisabled()
	}
	filter := OverrideListFilter{
		FlagID: req.Msg.GetFlagId(),
		Actor:  req.Msg.GetActor(),
	}
	if s := req.Msg.GetMinAgeSeconds(); s > 0 {
		filter.MinAge = time.Duration(s) * time.Second
	}
	overrides, err := a.store.ListAllOverrides(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &pbflagsv1.ListConditionOverridesResponse{}
	for _, o := range overrides {
		entry := &pbflagsv1.ConditionOverrideListEntry{
			FlagId:        o.FlagID,
			OverrideValue: o.Value,
			Source:        sourceStringToProto(o.Source),
			Actor:         o.Actor,
			Reason:        o.Reason,
			CreatedAt:     timestamppb.New(o.CreatedAt),
		}
		if o.ConditionIndex != nil {
			entry.ConditionIndex = o.ConditionIndex
		}
		resp.Entries = append(resp.Entries, entry)
	}
	return connect.NewResponse(resp), nil
}

func errRuntimeOverridesDisabled() error {
	return connect.NewError(connect.CodePermissionDenied,
		errors.New("runtime overrides are disabled on this server (start with --allow-runtime-overrides=true to enable state-changing RPCs)"))
}

// validateConditionIndex enforces the chain-shape invariants for
// override addressing (pb-wff.20):
//
//  1. condition_index, if set, must be in [0, len(chain)).
//  2. condition_index must NOT point at the trailing "otherwise" row
//     (a cond entry with empty CEL). The otherwise row IS the
//     fallback at evaluation time, so any override on the compiled
//     default must use the NULL form (omit condition_index). This
//     prevents two distinct rows in condition_overrides from
//     representing the same evaluation effect.
//
// nil condIdx is always valid (overrides the static / compiled default).
func validateConditionIndex(flag *pbflagsv1.FlagDetail, condIdx *int32) error {
	if condIdx == nil {
		return nil
	}
	chain := flag.GetConditions()
	idx := int(*condIdx)
	if idx < 0 || idx >= len(chain) {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("condition_index %d out of range (chain has %d conditions)", idx, len(chain)))
	}
	if chain[idx].GetCel() == "" {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("condition[%d] is the 'otherwise' fallback row; omit condition_index to override the default", idx))
	}
	return nil
}

// mapStoreErr translates store-layer sentinel errors to Connect codes.
// Replaced the old substring matcher; new errors should be added by
// wrapping ErrFlagNotFound / ErrOverrideNotFound / ErrInvalidArgument
// in the store, not by tweaking strings here.
func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, ErrFlagNotFound), errors.Is(err, ErrOverrideNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// ── Launch conversion helpers ───────────────────────────────────────

func launchesToProto(launches []Launch) []*pbflagsv1.LaunchDetail {
	out := make([]*pbflagsv1.LaunchDetail, len(launches))
	for i := range launches {
		out[i] = launchToProto(&launches[i])
	}
	return out
}

func launchToProto(l *Launch) *pbflagsv1.LaunchDetail {
	d := &pbflagsv1.LaunchDetail{
		LaunchId:         l.LaunchID,
		Dimension:        l.Dimension,
		RampPercentage:   int32(l.RampPct),
		RampSource:       l.RampSource,
		Status:           l.Status,
		AffectedFeatures: l.AffectedFeatures,
		CreatedAt:        timestamppb.New(l.CreatedAt),
		UpdatedAt:        timestamppb.New(l.UpdatedAt),
	}
	if l.ScopeFeatureID != nil {
		d.ScopeFeatureId = *l.ScopeFeatureID
	}
	if l.Description != nil {
		d.Description = *l.Description
	}
	if l.KilledAt != nil {
		d.KilledAt = timestamppb.New(*l.KilledAt)
	}
	return d
}
