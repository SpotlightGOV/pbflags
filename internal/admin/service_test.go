package admin

import (
	"context"
	"errors"
	"fmt"
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

// newTestAdminServiceWithOverrides is the variant with the gating flag on.
// Use when the test exercises a code path that's gated by
// --allow-condition-overrides but that returns from validation before
// touching the (zero-value) Store.
func newTestAdminServiceWithOverrides() *AdminService {
	return NewAdminService(&Store{}, slog.Default(), WithAllowConditionOverrides())
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

// ── Sync lock + condition-override RPC validation ───────────────────
//
// These tests cover handler-level validation (gating + required-field
// checks). The happy-path / store-touching cases live in store_test.go
// against a real database.

func TestAdminService_AcquireSyncLock_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.AcquireSyncLock(context.Background(),
		connect.NewRequest(&pbflagsv1.AcquireSyncLockRequest{Reason: "incident-1"}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_AcquireSyncLock_EmptyReason(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.AcquireSyncLock(context.Background(),
		connect.NewRequest(&pbflagsv1.AcquireSyncLockRequest{Reason: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ReleaseSyncLock_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.ReleaseSyncLock(context.Background(),
		connect.NewRequest(&pbflagsv1.ReleaseSyncLockRequest{Reason: "done"}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_ReleaseSyncLock_EmptyReason(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.ReleaseSyncLock(context.Background(),
		connect.NewRequest(&pbflagsv1.ReleaseSyncLockRequest{Reason: ""}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_GetSyncLock_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.GetSyncLock(context.Background(),
		connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_SetConditionOverride_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.SetConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.SetConditionOverrideRequest{
			FlagId: "feature/1", Reason: "r",
			Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_SetConditionOverride_EmptyFlagID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.SetConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.SetConditionOverrideRequest{
			FlagId: "", Reason: "r",
			Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_SetConditionOverride_NilValue(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.SetConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.SetConditionOverrideRequest{
			FlagId: "feature/1", Reason: "r",
			Value: nil,
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_SetConditionOverride_EmptyReason(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.SetConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.SetConditionOverrideRequest{
			FlagId: "feature/1", Reason: "",
			Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ClearConditionOverride_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.ClearConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearConditionOverrideRequest{
			FlagId: "feature/1", Reason: "done",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_ClearConditionOverride_EmptyFlagID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.ClearConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearConditionOverrideRequest{
			FlagId: "", Reason: "done",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ClearConditionOverride_EmptyReason(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.ClearConditionOverride(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearConditionOverrideRequest{
			FlagId: "feature/1", Reason: "",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ClearAllConditionOverrides_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.ClearAllConditionOverrides(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearAllConditionOverridesRequest{
			FlagId: "feature/1", Reason: "done",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAdminService_ClearAllConditionOverrides_EmptyFlagID(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.ClearAllConditionOverrides(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearAllConditionOverridesRequest{
			FlagId: "", Reason: "done",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ClearAllConditionOverrides_EmptyReason(t *testing.T) {
	t.Parallel()
	svc := newTestAdminServiceWithOverrides()
	_, err := svc.ClearAllConditionOverrides(context.Background(),
		connect.NewRequest(&pbflagsv1.ClearAllConditionOverridesRequest{
			FlagId: "feature/1", Reason: "",
		}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAdminService_ListConditionOverrides_GatedOff(t *testing.T) {
	t.Parallel()
	svc := newTestAdminService()
	_, err := svc.ListConditionOverrides(context.Background(),
		connect.NewRequest(&pbflagsv1.ListConditionOverridesRequest{}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

// ── validateConditionIndex (pb-wff.20) ──────────────────────────────

func TestValidateConditionIndex_NilAlwaysOK(t *testing.T) {
	t.Parallel()
	// Empty chain.
	require.NoError(t, validateConditionIndex(&pbflagsv1.FlagDetail{}, nil))
	// With chain.
	chain := []*pbflagsv1.ConditionDetail{
		{Index: 0, Cel: "x == 1"},
		{Index: 1, Cel: ""}, // otherwise
	}
	require.NoError(t, validateConditionIndex(&pbflagsv1.FlagDetail{Conditions: chain}, nil))
}

func TestValidateConditionIndex_OutOfRange(t *testing.T) {
	t.Parallel()
	chain := []*pbflagsv1.ConditionDetail{{Index: 0, Cel: "x == 1"}}
	flag := &pbflagsv1.FlagDetail{Conditions: chain}
	for _, badIdx := range []int32{-1, 1, 99} {
		i := badIdx
		err := validateConditionIndex(flag, &i)
		require.Error(t, err, "idx=%d", badIdx)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		require.Contains(t, err.Error(), "out of range")
	}
}

func TestValidateConditionIndex_RejectsOtherwiseRow(t *testing.T) {
	t.Parallel()
	chain := []*pbflagsv1.ConditionDetail{
		{Index: 0, Cel: "x == 1"},
		{Index: 1, Cel: ""}, // otherwise — addressing this is forbidden
	}
	flag := &pbflagsv1.FlagDetail{Conditions: chain}
	i := int32(1)
	err := validateConditionIndex(flag, &i)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "otherwise")
}

func TestValidateConditionIndex_AcceptsRealCondition(t *testing.T) {
	t.Parallel()
	chain := []*pbflagsv1.ConditionDetail{
		{Index: 0, Cel: "x == 1"},
		{Index: 1, Cel: "y == 2"},
		{Index: 2, Cel: ""}, // otherwise
	}
	flag := &pbflagsv1.FlagDetail{Conditions: chain}
	for _, goodIdx := range []int32{0, 1} {
		i := goodIdx
		require.NoError(t, validateConditionIndex(flag, &i), "idx=%d", goodIdx)
	}
}

func TestValidateConditionIndex_NoOtherwiseChainAcceptsLast(t *testing.T) {
	t.Parallel()
	// A chain whose last entry is a real CEL (no otherwise fallback).
	// The compiled default IS reachable here, so the last index is a
	// normal addressable condition — accept it.
	chain := []*pbflagsv1.ConditionDetail{
		{Index: 0, Cel: "x == 1"},
		{Index: 1, Cel: "y == 2"},
	}
	flag := &pbflagsv1.FlagDetail{Conditions: chain}
	i := int32(1)
	require.NoError(t, validateConditionIndex(flag, &i))
}

// ── mapStoreErr classification ──────────────────────────────────────
// Callers always check err != nil before invoking mapStoreErr, so nil
// passthrough is intentionally not part of the contract.

func TestMapStoreErr_FlagNotFound(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("xyz: %w", ErrFlagNotFound)
	err := mapStoreErr(wrapped)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestMapStoreErr_OverrideNotFound(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("missing override: %w", ErrOverrideNotFound)
	err := mapStoreErr(wrapped)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestMapStoreErr_InvalidArgument(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("flag_id: %w", ErrInvalidArgument)
	err := mapStoreErr(wrapped)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestMapStoreErr_DefaultsToInternal(t *testing.T) {
	t.Parallel()
	err := mapStoreErr(errors.New("boom"))
	require.Error(t, err)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}
