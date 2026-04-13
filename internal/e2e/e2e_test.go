//go:build e2e

// Package e2e provides browser-driven end-to-end tests for the pbflags admin
// web UI using Playwright. Tests are gated behind the "e2e" build tag so they
// do not run during normal `go test ./...`.
//
// Prerequisites:
//
//	go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps
//
// Run:
//
//	go test -tags e2e -count=1 -p 1 -v ./internal/e2e/
//
// Headed mode (visible browser with 500ms slowdown):
//
//	HEADED=1 go test -tags e2e -count=1 -p 1 -v ./internal/e2e/
package e2e

import (
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardLoads(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	// The dashboard should show the feature card.
	err = page.Locator(".feature-card").First().WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Check feature name is visible.
	featureName := page.Locator("[data-feature='" + env.tf.FeatureID + "']")
	err = featureName.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateAttached,
	})
	require.NoError(t, err)

	// Verify sidebar brand.
	brand := page.Locator(".sidebar-brand h1")
	text, err := brand.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "pbflags")
}

func TestDashboardExpandCollapseFeature(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Cards start collapsed — flag table body should be hidden.
	collapsed, err := card.Evaluate(`el => el.classList.contains('collapsed')`, nil)
	require.NoError(t, err)
	assert.True(t, collapsed.(bool), "feature card should start collapsed")

	// Click header to expand.
	err = card.Locator(".feature-header").Click()
	require.NoError(t, err)

	collapsed, err = card.Evaluate(`el => el.classList.contains('collapsed')`, nil)
	require.NoError(t, err)
	assert.False(t, collapsed.(bool), "feature card should be expanded after click")

	// Flags should be visible.
	rows := card.Locator("tbody tr")
	count, err := rows.Count()
	require.NoError(t, err)
	assert.Equal(t, 4, count, "should show 4 flags")
}

func TestNavigateToFlagDetail(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	// Expand the feature card first.
	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	err = card.Locator(".feature-header").Click()
	require.NoError(t, err)

	// Click the first flag link.
	flagLink := card.Locator("a.flag-name").First()
	err = flagLink.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// Should navigate to flag detail.
	breadcrumb := page.Locator(".breadcrumb")
	err = breadcrumb.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Flag detail header should be visible.
	flagID := page.Locator(".flag-id-sub")
	text, err := flagID.First().TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, env.tf.FeatureID+"/")
}

func TestKillAndUnkillFlag(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2))
	require.NoError(t, err)

	// Wait for flag detail to load.
	killBtn := page.Locator("button.btn-kill")
	err = killBtn.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Kill the flag.
	err = killBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// State pill should show KILLED.
	statePill := page.Locator(".state-pill")
	err = statePill.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	text, err := statePill.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "KILLED")

	// Unkill button should appear.
	unkillBtn := page.Locator("button:has-text('Unkill')")
	err = unkillBtn.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Unkill the flag.
	err = unkillBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// State should revert to DEFAULT.
	statePill = page.Locator(".state-pill")
	text, err = statePill.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "DEFAULT")
}

func TestFlagDetailReadOnly(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2))
	require.NoError(t, err)

	// Wait for the flag detail page to load.
	breadcrumb := page.Locator(".breadcrumb")
	err = breadcrumb.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Conditions section should be visible (shows static default for non-conditional flags).
	condSection := page.Locator("#conditions-section")
	err = condSection.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// There should be NO input fields or textareas for value editing.
	inputs := page.Locator("#conditions-section input[type='text'], #conditions-section textarea, #conditions-section select")
	count, err := inputs.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "conditions section should be read-only — no input/textarea/select elements")

	// There should be NO override section.
	overrides := page.Locator("#overrides-section")
	count, err = overrides.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "overrides section should not exist")
}

func TestDashboardReadOnly(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	// Expand feature card.
	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	err = card.Locator(".feature-header").Click()
	require.NoError(t, err)

	// Dashboard flag rows should have no input fields or select dropdowns for value editing.
	editControls := card.Locator("tbody input[type='text'], tbody select.flag-value-select")
	count, err := editControls.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "dashboard should have no inline value editing controls")
}

func TestAuditLogPage(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)

	// Navigate to audit log via sidebar.
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	auditLink := page.Locator(".sidebar-nav a[href='/audit']")
	err = auditLink.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// Audit log page should load.
	auditTable := page.Locator(".audit-table")
	err = auditTable.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
}

func TestDashboardSearch(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	// Wait for dashboard.
	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Type in the search box — use DispatchEvent to ensure oninput fires.
	searchInput := page.Locator("#filter-search")
	err = searchInput.Fill(env.tf.FeatureID)
	require.NoError(t, err)
	err = searchInput.DispatchEvent("input", nil)
	require.NoError(t, err)

	// Feature card should still be visible.
	visible, err := card.IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "feature card should be visible after matching search")

	// Search for something that doesn't exist.
	err = searchInput.Fill("nonexistent_feature_xyz")
	require.NoError(t, err)
	err = searchInput.DispatchEvent("input", nil)
	require.NoError(t, err)

	// Feature card should be hidden.
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	})
	require.NoError(t, err, "feature card should be hidden when search doesn't match")
}

func TestDashboardStateFilter(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Filter by ENABLED state — all flags are DEFAULT so the card should hide.
	stateFilter := page.Locator("#filter-state")
	_, err = stateFilter.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice("ENABLED"),
	})
	require.NoError(t, err)

	// Card should become hidden.
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
	})
	require.NoError(t, err, "card should be hidden when no flags match state filter")

	// Filter by DEFAULT — card should reappear.
	_, err = stateFilter.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice("DEFAULT"),
	})
	require.NoError(t, err)

	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	})
	require.NoError(t, err, "card should be visible when flags match state filter")
}

func TestBreadcrumbNavigation(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	// Navigate to a flag detail page.
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1))
	require.NoError(t, err)

	breadcrumb := page.Locator(".breadcrumb")
	err = breadcrumb.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Click the "dashboard" breadcrumb link.
	dashLink := breadcrumb.Locator("a:has-text('dashboard')")
	err = dashLink.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// Should be back on dashboard.
	featureCard := page.Locator(".feature-card")
	err = featureCard.First().WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
}

func TestKillFlagFromDashboard(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	// Expand the feature card.
	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	err = card.Locator(".feature-header").Click()
	require.NoError(t, err)

	// Find a kill button in the first flag row.
	flagRow := card.Locator("tbody tr").First()
	killBtn := flagRow.Locator(".btn-icon-kill")
	err = killBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// The row should now show an "Unkill" button.
	unkillBtn := flagRow.Locator("button:has-text('Unkill')")
	err = unkillBtn.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
}

func TestHTMXPartialSwap(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	_, err := page.Goto(env.baseURL)
	require.NoError(t, err)

	card := page.Locator(".feature-card").First()
	err = card.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Navigate via sidebar to audit log — this should be an htmx partial swap.
	auditLink := page.Locator(".sidebar-nav a[href='/audit']")
	err = auditLink.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// The sidebar should still be present (not a full page reload).
	sidebar := page.Locator(".sidebar")
	visible, err := sidebar.IsVisible()
	require.NoError(t, err)
	assert.True(t, visible, "sidebar should persist across htmx navigation")

	// The audit link should be active.
	activeClass, err := auditLink.GetAttribute("class")
	require.NoError(t, err)
	assert.Contains(t, activeClass, "active")

	// URL should have changed.
	assert.Contains(t, page.URL(), "/audit")
}
