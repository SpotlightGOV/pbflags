package sync

import (
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestCollectLaunches_ScopePresenceValidation(t *testing.T) {
	// Setup: a launch on user_id, feature with scopes ["anon", "user"].
	// Scope "anon" only has session_id, scope "user" has session_id + user_id.
	// The launch should be rejected because user_id is not in the anon scope.
	configs := map[string]*configfile.Config{
		"notifications": {
			Feature: "notifications",
			Flags: map[string]configfile.FlagEntry{
				"email_enabled": {
					Conditions: []configfile.Condition{
						{
							When:   `ctx.plan == 1`,
							Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
							Launch: &configfile.LaunchOverride{ID: "gradual-emails"},
						},
					},
				},
			},
			Launches: map[string]configfile.LaunchEntry{
				"gradual-emails": {Dimension: "user_id", RampPercentage: intPtr(50)},
			},
		},
	}

	hashableDims := map[string]bool{"user_id": true, "session_id": true}

	scopeDims := map[string]map[string]bool{
		"anon": {"session_id": true},
		"user": {"session_id": true, "user_id": true},
	}
	featureScopes := map[string][]string{
		"notifications": {"anon", "user"},
	}

	_, err := CollectLaunches(configs, t.TempDir(), hashableDims, scopeDims, featureScopes)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available in scope")
	assert.Contains(t, err.Error(), "user_id")
	assert.Contains(t, err.Error(), "anon")
}

func TestCollectLaunches_ScopePresenceValid(t *testing.T) {
	// Launch on session_id (globally required) — should pass for all scopes.
	configs := map[string]*configfile.Config{
		"notifications": {
			Feature: "notifications",
			Flags: map[string]configfile.FlagEntry{
				"email_enabled": {
					Conditions: []configfile.Condition{
						{
							When:   `ctx.plan == 1`,
							Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
							Launch: &configfile.LaunchOverride{ID: "gradual-emails"},
						},
					},
				},
			},
			Launches: map[string]configfile.LaunchEntry{
				"gradual-emails": {Dimension: "session_id", RampPercentage: intPtr(50)},
			},
		},
	}

	hashableDims := map[string]bool{"user_id": true, "session_id": true}

	scopeDims := map[string]map[string]bool{
		"anon": {"session_id": true},
		"user": {"session_id": true, "user_id": true},
	}
	featureScopes := map[string][]string{
		"notifications": {"anon", "user"},
	}

	lc, err := CollectLaunches(configs, t.TempDir(), hashableDims, scopeDims, featureScopes)
	require.NoError(t, err)
	assert.Contains(t, lc.Defined, "gradual-emails")
}

func TestCollectLaunches_NilScopeInfoSkipsValidation(t *testing.T) {
	// When scope info is nil, validation is skipped (backwards compat).
	configs := map[string]*configfile.Config{
		"notifications": {
			Feature: "notifications",
			Flags: map[string]configfile.FlagEntry{
				"email_enabled": {
					Conditions: []configfile.Condition{
						{
							When:   `ctx.plan == 1`,
							Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
							Launch: &configfile.LaunchOverride{ID: "gradual-emails"},
						},
					},
				},
			},
			Launches: map[string]configfile.LaunchEntry{
				"gradual-emails": {Dimension: "user_id", RampPercentage: intPtr(50)},
			},
		},
	}

	hashableDims := map[string]bool{"user_id": true}

	// nil scope info — should not fail.
	lc, err := CollectLaunches(configs, t.TempDir(), hashableDims, nil, nil)
	require.NoError(t, err)
	assert.Contains(t, lc.Defined, "gradual-emails")
}
