package admin

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// newTestAdminService returns an AdminService backed by a zero-value Store.
// All validation tests return before any store method is called, so a nil
// pool is safe.
func newTestAdminService() *AdminService {
	return NewAdminService(&Store{}, slog.Default())
}

func TestAdminService_GetFlag_EmptyFlagID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.GetFlag(context.Background(), connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UpdateFlagState_EmptyFlagID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "",
		State:  pbflagsv1.State_STATE_KILLED,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UpdateFlagState_Unspecified(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: "feature/1",
		State:  pbflagsv1.State_STATE_UNSPECIFIED,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_SetFlagOverride_Unimplemented(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.SetFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.SetFlagOverrideRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestAdminService_RemoveFlagOverride_Unimplemented(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.RemoveFlagOverride(context.Background(), connect.NewRequest(&pbflagsv1.RemoveFlagOverrideRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestAdminService_GetLaunch_EmptyLaunchID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.GetLaunch(context.Background(), connect.NewRequest(&pbflagsv1.GetLaunchRequest{LaunchId: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UpdateLaunchRamp_EmptyLaunchID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UpdateLaunchRamp(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchRampRequest{
		LaunchId: "",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UpdateLaunchStatus_EmptyLaunchID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: "",
		Status:   "active",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UpdateLaunchStatus_EmptyStatus(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: "launch-1",
		Status:   "",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_KillLaunch_EmptyLaunchID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.KillLaunch(context.Background(), connect.NewRequest(&pbflagsv1.KillLaunchRequest{LaunchId: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_UnkillLaunch_EmptyLaunchID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.UnkillLaunch(context.Background(), connect.NewRequest(&pbflagsv1.UnkillLaunchRequest{LaunchId: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
