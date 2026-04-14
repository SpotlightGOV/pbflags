package lint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckScopes_NoChanges(t *testing.T) {
	scopes := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	features := []FeatureScopeInfo{
		{FeatureID: "notifications", Scopes: []string{"anon", "user"}},
	}
	violations := CheckScopes(scopes, scopes, features, features)
	assert.Empty(t, violations)
}

func TestCheckScopes_ScopeRemoved(t *testing.T) {
	base := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	current := []ScopeInfo{
		{Name: "anon"},
	}
	violations := CheckScopes(base, current, nil, nil)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleScopeRemoved, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "user")
	assert.Contains(t, violations[0].Guidance, "UserFeatures")
}

func TestCheckScopes_ScopeDimensionAdded(t *testing.T) {
	base := []ScopeInfo{
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	current := []ScopeInfo{
		{Name: "user", Dimensions: []string{"user_id", "org_id"}},
	}
	violations := CheckScopes(base, current, nil, nil)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleScopeDimChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "added: org_id")
}

func TestCheckScopes_ScopeDimensionRemoved(t *testing.T) {
	base := []ScopeInfo{
		{Name: "tenant", Dimensions: []string{"user_id", "tenant_id"}},
	}
	current := []ScopeInfo{
		{Name: "tenant", Dimensions: []string{"tenant_id"}},
	}
	violations := CheckScopes(base, current, nil, nil)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleScopeDimChanged, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "removed: user_id")
}

func TestCheckScopes_FeatureScopeRemoved(t *testing.T) {
	scopes := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	baseFeat := []FeatureScopeInfo{
		{FeatureID: "notifications", Scopes: []string{"anon", "user"}},
	}
	curFeat := []FeatureScopeInfo{
		{FeatureID: "notifications", Scopes: []string{"user"}},
	}
	violations := CheckScopes(scopes, scopes, baseFeat, curFeat)
	require.Len(t, violations, 1)
	assert.Equal(t, RuleFeatureScopeRemoved, violations[0].Rule)
	assert.Contains(t, violations[0].Message, "notifications")
	assert.Contains(t, violations[0].Message, "anon")
	assert.Contains(t, violations[0].Guidance, "Notifications()")
}

func TestCheckScopes_NewFeatureNotBreaking(t *testing.T) {
	scopes := []ScopeInfo{{Name: "anon"}}
	baseFeat := []FeatureScopeInfo{}
	curFeat := []FeatureScopeInfo{
		{FeatureID: "billing", Scopes: []string{"anon"}},
	}
	violations := CheckScopes(scopes, scopes, baseFeat, curFeat)
	assert.Empty(t, violations, "adding a new feature is not breaking")
}

func TestCheckScopes_ScopeAddedNotBreaking(t *testing.T) {
	base := []ScopeInfo{{Name: "anon"}}
	current := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	violations := CheckScopes(base, current, nil, nil)
	assert.Empty(t, violations, "adding a new scope is not breaking")
}

func TestCheckScopes_FeatureScopeAddedNotBreaking(t *testing.T) {
	scopes := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	baseFeat := []FeatureScopeInfo{
		{FeatureID: "notifications", Scopes: []string{"anon"}},
	}
	curFeat := []FeatureScopeInfo{
		{FeatureID: "notifications", Scopes: []string{"anon", "user"}},
	}
	violations := CheckScopes(scopes, scopes, baseFeat, curFeat)
	assert.Empty(t, violations, "adding a feature to a new scope is not breaking")
}

func TestCheckConditionScopeCompat(t *testing.T) {
	scopes := []ScopeInfo{
		{Name: "anon"},
		{Name: "user", Dimensions: []string{"user_id"}},
	}
	features := []FeatureScopeInfo{
		{FeatureID: "user_prefs", Scopes: []string{"anon", "user"}},
	}

	// Flag uses user_id in a condition, but feature is in "anon" scope
	// which doesn't have user_id.
	condDims := map[string][]string{
		"user_prefs/1": {"user_id"},
	}

	violations := CheckConditionScopeCompat(nil, scopes, features, nil, condDims)
	// contextMsg is nil, so returns nil.
	assert.Empty(t, violations)
}
