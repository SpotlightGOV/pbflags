//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Tests for the pb-wff condition-override + sync-lock admin UI.
//
// All write paths are gated by --allow-condition-overrides so each test
// opts into the gated env via withOverrides(). Tests that acquire the
// global sync lock must clean it up explicitly because the lock is a
// singleton row, not scoped to the per-test feature.

// TestOverrideStaticDefault sets an override on the always-rendered
// static-default row of a no-conditions flag, then clears it.
func TestOverrideStaticDefault(t *testing.T) {
	env := setupEnv(t, withOverrides())

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2)) // STRING flag
	require.NoError(t, err)

	require.NoError(t, page.Locator(".cond-action-cell").First().WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))

	// Open the static-default row's Override popover.
	openOverrideForm(t, page, ".condition-row-default")

	require.NoError(t, page.Locator(".override-popover input[name='value']").Fill("hotfix-value"))
	require.NoError(t, page.Locator(".override-popover input[name='reason']").Fill("e2e: static default override"))
	require.NoError(t, page.Locator(".override-popover button[type='submit']").Click())
	waitForHTMX(page)

	// Row should now be amber + show overridden value.
	overriddenRow := page.Locator(".condition-row-default.condition-row-overridden")
	require.NoError(t, overriddenRow.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	val, err := overriddenRow.Locator(".cond-value-overridden").TextContent()
	require.NoError(t, err)
	assert.Contains(t, val, "hotfix-value")

	// Clear override via the × button.
	require.NoError(t, overriddenRow.Locator(".btn-icon-clear").Click())
	waitForHTMX(page)

	// After clear, the default row should still exist but no longer be amber.
	count, err := page.Locator(".condition-row-default.condition-row-overridden").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "row should no longer be marked overridden after clear")
}

// TestOverrideBoolFlag exercises the BOOL-typed value-select widget on
// the override form (vs the free-text input the STRING test exercised).
func TestOverrideBoolFlag(t *testing.T) {
	env := setupEnv(t, withOverrides())

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1)) // BOOL flag
	require.NoError(t, err)

	require.NoError(t, page.Locator(".condition-row-default").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))

	openOverrideForm(t, page, ".condition-row-default")

	// BOOL flags get a select, not a text input.
	sel := page.Locator(".override-popover select[name='value']")
	require.NoError(t, sel.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	_, err = sel.SelectOption(playwright.SelectOptionValues{Values: playwright.StringSlice("true")})
	require.NoError(t, err)
	require.NoError(t, page.Locator(".override-popover input[name='reason']").Fill("e2e: bool override"))
	require.NoError(t, page.Locator(".override-popover button[type='submit']").Click())
	waitForHTMX(page)

	val, err := page.Locator(".condition-row-default .cond-value-overridden").TextContent()
	require.NoError(t, err)
	assert.Contains(t, val, "true")
}

// TestDashboardOverrideBadge confirms that after setting an override on
// a flag, the dashboard shows the amber OVR badge for that row.
func TestDashboardOverrideBadge(t *testing.T) {
	env := setupEnv(t, withOverrides())

	// Set the override directly via the store (faster than driving the UI
	// for a side-effect test).
	_, err := env.store.SetConditionOverride(
		context.Background(),
		env.tf.FlagID(2), nil,
		&pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "badge-test"}},
		"ui", "e2e@local", "e2e: dashboard badge",
	)
	require.NoError(t, err)

	page := env.newPage(t)
	_, err = page.Goto(env.baseURL + "/")
	require.NoError(t, err)

	// Expand the feature card.
	card := page.Locator(".feature-card").First()
	require.NoError(t, card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	require.NoError(t, card.Locator(".feature-header").Click())

	// The overridden row should carry data-overridden + an OVR badge.
	row := page.Locator("tr[data-flag='" + env.tf.FlagID(2) + "']")
	require.NoError(t, row.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateAttached,
	}))
	overridden, err := row.GetAttribute("data-overridden")
	require.NoError(t, err)
	assert.Equal(t, "true", overridden)

	badge := row.Locator(".override-badge")
	require.NoError(t, badge.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	text, err := badge.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "OVR")
}

// TestSyncLockBannerAndRelease covers acquiring the sync lock via the
// sidebar and releasing it via the sticky banner.
func TestSyncLockBannerAndRelease(t *testing.T) {
	env := setupEnv(t, withOverrides())

	// Singleton sync_lock row is global; clean up if any test leaves it set.
	t.Cleanup(func() {
		env.pool.Exec(context.Background(), "DELETE FROM feature_flags.sync_lock WHERE id = 1")
	})

	page := env.newPage(t)
	// The lock acquire control lives on the flag detail header (pb-wff.37),
	// not the sidebar — start there.
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1))
	require.NoError(t, err)

	// No banner before lock is acquired.
	count, err := page.Locator(".lock-banner").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Open the flag-header acquire popover.
	acquire := page.Locator(".lock-form-acquire")
	require.NoError(t, acquire.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	require.NoError(t, acquire.Locator("summary").Click())

	require.NoError(t, page.Locator(".lock-form-acquire input[name='reason']").Fill("e2e: testing the lock"))
	require.NoError(t, page.Locator(".lock-form-acquire button[type='submit']").Click())

	// HX-Refresh causes a full reload.
	require.NoError(t, page.Locator(".lock-banner").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))

	bannerActor, err := page.Locator(".lock-banner-actor").TextContent()
	require.NoError(t, err)
	assert.NotEmpty(t, bannerActor, "banner should show the holder")

	bannerReason, err := page.Locator(".lock-banner-reason").TextContent()
	require.NoError(t, err)
	assert.Contains(t, bannerReason, "e2e: testing the lock")

	// Body should carry sync-locked class while held.
	bodyClass, err := page.Locator("body").GetAttribute("class")
	require.NoError(t, err)
	assert.Contains(t, bodyClass, "sync-locked")

	// Header acquire control should be hidden while locked.
	count, err = page.Locator(".lock-form-acquire").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "acquire control should hide while locked")

	// Banner persists across navigation — visit another flag detail page.
	_, err = page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2))
	require.NoError(t, err)
	require.NoError(t, page.Locator(".lock-banner").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))

	// Release via the banner's Release popover. The form has a confirm()
	// dialog — auto-accept it.
	page.OnDialog(func(d playwright.Dialog) { d.Accept() })

	require.NoError(t, page.Locator(".lock-form-release summary").Click())
	require.NoError(t, page.Locator(".lock-form-release input[name='reason']").Fill("e2e: releasing"))
	require.NoError(t, page.Locator(".lock-form-release button[type='submit']").Click())

	// HX-Refresh; banner should be gone.
	require.NoError(t, page.Locator(".lock-banner").WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	}))
}

// TestOverrideGatingDisabled confirms that when the env does NOT enable
// overrides, the override controls do not render in the UI. The gate's
// 403-on-POST behavior is covered by handler_test.go unit tests.
func TestOverrideGatingDisabled(t *testing.T) {
	env := setupEnv(t) // no withOverrides()

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1))
	require.NoError(t, err)

	require.NoError(t, page.Locator("#conditions-section").WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))

	count, err := page.Locator(".override-action-stack").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no override controls should appear when gating is off")

	count, err = page.Locator(".lock-form-acquire").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no sidebar lock acquire should appear when gating is off")

	count, err = page.Locator(".cond-col-action").Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no override column header should render when gating is off")
}

// openOverrideForm clicks the <summary> on the override popover for the
// given row selector, opening the form for input.
func openOverrideForm(t *testing.T, page playwright.Page, rowSelector string) {
	t.Helper()
	summary := page.Locator(rowSelector + " .override-form-details summary")
	require.NoError(t, summary.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	}))
	require.NoError(t, summary.Click())
}
