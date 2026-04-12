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
	"strings"
	"testing"
	"time"

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

func TestUpdateStringFlagValue(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	// Navigate to the string flag detail page.
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2))
	require.NoError(t, err)

	// Wait for the value input form.
	valueInput := page.Locator(".detail-value-input")
	err = valueInput.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Type a new value and submit.
	err = valueInput.Fill("world")
	require.NoError(t, err)

	setBtn := page.Locator(".detail-value-form button.btn-primary")
	err = setBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// The state pill should now show ENABLED.
	statePill := page.Locator(".state-pill")
	text, err := statePill.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "ENABLED")

	// The input should reflect the new value.
	valueInput = page.Locator(".detail-value-input")
	val, err := valueInput.InputValue()
	require.NoError(t, err)
	assert.Equal(t, "world", val)

	// Audit log should show the state change.
	auditRow := page.Locator(".audit-table tbody tr").First()
	err = auditRow.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	actionText, err := auditRow.Locator(".audit-action").TextContent()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE_STATE", actionText)
}

func TestAddAndRemoveOverride(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	// Navigate to the bool flag (user layer — supports overrides).
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1))
	require.NoError(t, err)

	// Wait for overrides section.
	overridesSection := page.Locator("#overrides-section")
	err = overridesSection.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Fill in the override form.
	entityInput := overridesSection.Locator("input[name='entity_id']")
	err = entityInput.Fill("test-user-42")
	require.NoError(t, err)

	valueSelect := overridesSection.Locator("select[name='value']")
	_, err = valueSelect.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice("false"),
	})
	require.NoError(t, err)

	addBtn := overridesSection.Locator("button:has-text('Add Override')")
	err = addBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// The override should appear in the table.
	overridesSection = page.Locator("#overrides-section")
	entityCell := overridesSection.Locator(".entity-id:has-text('test-user-42')")
	err = entityCell.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Remove the override — need to handle the confirmation dialog.
	page.OnDialog(func(dialog playwright.Dialog) {
		dialog.Accept()
	})

	removeBtn := overridesSection.Locator("tr:has-text('test-user-42') button:has-text('Remove')")
	err = removeBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// The override row should be gone.
	overridesSection = page.Locator("#overrides-section")
	entityCell = overridesSection.Locator(".entity-id:has-text('test-user-42')")
	count, err := entityCell.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "override should be removed")
}

func TestAuditLogPage(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	// First make a state change so there's an audit entry.
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(2))
	require.NoError(t, err)

	valueInput := page.Locator(".detail-value-input")
	err = valueInput.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
	err = valueInput.Fill("audit-test-val")
	require.NoError(t, err)

	setBtn := page.Locator(".detail-value-form button.btn-primary")
	err = setBtn.Click()
	require.NoError(t, err)
	waitForHTMX(page)

	// Navigate to audit log via sidebar.
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

	// There should be at least one entry.
	rows := auditTable.Locator("tbody tr")
	count, err := rows.Count()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 1)
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

	// Feature card should be hidden — use WaitFor with Hidden state since
	// Locator.Evaluate waits for element to be visible (which won't happen).
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

	// The row should now show a "Unkill" button.
	unkillBtn := flagRow.Locator("button:has-text('Unkill')")
	err = unkillBtn.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)
}

func TestBulkImportOverrides(t *testing.T) {
	env := setupEnv(t)

	page := env.newPage(t)
	// Navigate to the bool flag (user layer — supports overrides).
	_, err := page.Goto(env.baseURL + "/flags/" + env.tf.FlagID(1))
	require.NoError(t, err)

	// Wait for overrides section.
	overridesSection := page.Locator("#overrides-section")
	err = overridesSection.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Open bulk import panel.
	bulkToggle := overridesSection.Locator("button:has-text('Bulk Import')")
	err = bulkToggle.Click()
	require.NoError(t, err)

	// The bulk import panel should be visible.
	bulkPanel := overridesSection.Locator(".bulk-import-panel")
	err = bulkPanel.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateVisible,
	})
	require.NoError(t, err)

	// Type some overrides in the paste textarea and trigger the oninput handler.
	textarea := bulkPanel.Locator(".bulk-textarea")
	err = textarea.Fill("bulk-user-1:true\nbulk-user-2:false\nbulk-user-3:true")
	require.NoError(t, err)
	err = textarea.DispatchEvent("input", nil)
	require.NoError(t, err)

	// Wait for preview count to update.
	previewCount := bulkPanel.Locator(".bulk-preview-count")
	_, err = page.WaitForFunction(`() => {
		const el = document.querySelector('.bulk-preview-count');
		return el && el.textContent.includes('3 overrides');
	}`, playwright.PageWaitForFunctionOptions{Timeout: playwright.Float(5000)})
	require.NoError(t, err)
	text, err := previewCount.TextContent()
	require.NoError(t, err)
	assert.Contains(t, text, "3 overrides")

	// The import button should now be enabled.
	importBtn := bulkPanel.Locator(".bulk-import-btn")
	disabled, err := importBtn.IsDisabled()
	require.NoError(t, err)
	require.False(t, disabled, "import button should be enabled after parsing overrides")

	// Intercept the bulk import API response since the JS immediately refreshes
	// the page via htmx.ajax after showing results, destroying the results DOM.
	responseCh := make(chan playwright.Response, 1)
	page.OnResponse(func(resp playwright.Response) {
		if strings.Contains(resp.URL(), "/api/flags/overrides/bulk/") {
			responseCh <- resp
		}
	})

	err = importBtn.Click()
	require.NoError(t, err)

	// Wait for the bulk import API response.
	select {
	case resp := <-responseCh:
		assert.Equal(t, 200, resp.Status(), "bulk import should return 200")
		body, err := resp.Body()
		require.NoError(t, err)
		bodyStr := string(body)
		assert.Contains(t, bodyStr, `"created":3`)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for bulk import API response")
	}
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
