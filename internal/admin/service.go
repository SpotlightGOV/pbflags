package admin

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
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
	features, err := a.store.ListFeatures(ctx)
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

	a.logger.Info("updating flag state",
		"flag_id", msg.FlagId, "state", msg.State.String(), "actor", msg.Actor)

	if err := a.store.UpdateFlagState(ctx, msg.FlagId, msg.State, msg.Value, msg.Actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.UpdateFlagStateResponse{}), nil
}

func (a *AdminService) SetFlagOverride(ctx context.Context, req *connect.Request[pbflagsv1.SetFlagOverrideRequest]) (*connect.Response[pbflagsv1.SetFlagOverrideResponse], error) {
	msg := req.Msg
	if msg.FlagId == "" || msg.EntityId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	if msg.State == pbflagsv1.State_STATE_UNSPECIFIED {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	a.logger.Info("setting flag override",
		"flag_id", msg.FlagId, "entity_id", msg.EntityId,
		"state", msg.State.String(), "actor", msg.Actor)

	if err := a.store.SetFlagOverride(ctx, msg.FlagId, msg.EntityId, msg.State, msg.Value, msg.Actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.SetFlagOverrideResponse{}), nil
}

func (a *AdminService) RemoveFlagOverride(ctx context.Context, req *connect.Request[pbflagsv1.RemoveFlagOverrideRequest]) (*connect.Response[pbflagsv1.RemoveFlagOverrideResponse], error) {
	msg := req.Msg
	if msg.FlagId == "" || msg.EntityId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	a.logger.Info("removing flag override",
		"flag_id", msg.FlagId, "entity_id", msg.EntityId, "actor", msg.Actor)

	if err := a.store.RemoveFlagOverride(ctx, msg.FlagId, msg.EntityId, msg.Actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.RemoveFlagOverrideResponse{}), nil
}

func (a *AdminService) GetAuditLog(ctx context.Context, req *connect.Request[pbflagsv1.GetAuditLogRequest]) (*connect.Response[pbflagsv1.GetAuditLogResponse], error) {
	entries, err := a.store.GetAuditLog(ctx, AuditLogFilter{FlagID: req.Msg.FlagId, Limit: req.Msg.Limit})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pbflagsv1.GetAuditLogResponse{Entries: entries}), nil
}
