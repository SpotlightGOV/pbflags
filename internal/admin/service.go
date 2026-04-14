package admin

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/authn"
)

// AdminService implements the FlagAdminService Connect handler.
type AdminService struct {
	store  *Store
	logger *slog.Logger
}

// NewAdminService creates a FlagAdminService handler.
func NewAdminService(store *Store, logger *slog.Logger) *AdminService {
	return &AdminService{store: store, logger: logger}
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
	if req.Msg.GetLaunchId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	actor := authn.SubjectFromContext(ctx, "api")
	a.logger.Info("updating launch ramp",
		"launch_id", req.Msg.GetLaunchId(), "ramp_pct", req.Msg.GetRampPercentage(), "actor", actor)
	if err := a.store.UpdateLaunchRamp(ctx, req.Msg.GetLaunchId(), int(req.Msg.GetRampPercentage()), actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.UpdateLaunchRampResponse{}), nil
}

func (a *AdminService) UpdateLaunchStatus(ctx context.Context, req *connect.Request[pbflagsv1.UpdateLaunchStatusRequest]) (*connect.Response[pbflagsv1.UpdateLaunchStatusResponse], error) {
	if req.Msg.GetLaunchId() == "" || req.Msg.GetStatus() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
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
