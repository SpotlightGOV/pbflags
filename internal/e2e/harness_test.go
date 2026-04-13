//go:build e2e

package e2e

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"

	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/admin/web"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// testEnv holds all resources for an E2E test session.
type testEnv struct {
	pool    *pgxpool.Pool
	baseURL string
	pw      *playwright.Playwright
	browser playwright.Browser
	tf      *testdb.TestFeature // per-test isolated feature
}

// setupEnv starts a PostgreSQL test container, a web admin server, and
// a Playwright browser. The returned testEnv is cleaned up automatically.
// e2eSpecs returns the standard set of flag specs for E2E tests.
func e2eSpecs() []testdb.FlagSpec {
	return []testdb.FlagSpec{
		{FlagType: "BOOL"},   // flag 1: supports per-entity overrides
		{FlagType: "STRING"}, // flag 2
		{FlagType: "INT64"},  // flag 3
		{FlagType: "DOUBLE"}, // flag 4
	}
}

func setupEnv(t *testing.T) *testEnv {
	t.Helper()

	_, pool := testdb.Require(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Create an isolated feature per test via CreateTestFeature.
	tf := testdb.CreateTestFeature(t, pool, e2eSpecs())

	store := admin.NewStore(pool, logger)

	handler, err := web.NewHandler(store, logger, web.EnvConfig{Name: "e2e-test"})
	require.NoError(t, err)

	mux := http.NewServeMux()
	handler.Register(mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	baseURL := fmt.Sprintf("http://%s", ln.Addr())

	// Launch Playwright.
	pw, err := playwright.Run()
	require.NoError(t, err, "failed to start playwright — have you run `go run github.com/playwright-community/playwright-go/cmd/playwright install --with-deps`?")
	t.Cleanup(func() { pw.Stop() })

	headed := os.Getenv("HEADED") == "1"
	var slowMo float64
	if headed {
		slowMo = 500
	}

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(!headed),
		SlowMo:   playwright.Float(slowMo),
	})
	require.NoError(t, err)
	t.Cleanup(func() { browser.Close() })

	return &testEnv{
		pool:    pool,
		baseURL: baseURL,
		pw:      pw,
		browser: browser,
		tf:      tf,
	}
}

// newPage creates a new browser context and page with optional tracing.
// On test failure, the trace is saved to testdata/traces/<testname>.zip.
func (e *testEnv) newPage(t *testing.T) playwright.Page {
	t.Helper()

	ctx, err := e.browser.NewContext()
	require.NoError(t, err)

	// Start tracing — on failure we save a trace zip for debugging.
	err = ctx.Tracing().Start(playwright.TracingStartOptions{
		Screenshots: playwright.Bool(true),
		Snapshots:   playwright.Bool(true),
		Sources:     playwright.Bool(true),
	})
	require.NoError(t, err)

	page, err := ctx.NewPage()
	require.NoError(t, err)

	t.Cleanup(func() {
		if t.Failed() {
			traceDir := filepath.Join("testdata", "traces")
			os.MkdirAll(traceDir, 0o755)
			tracePath := filepath.Join(traceDir, t.Name()+".zip")
			ctx.Tracing().Stop(tracePath)
			t.Logf("Trace saved to %s — view with: npx playwright show-trace %s", tracePath, tracePath)
		} else {
			ctx.Tracing().Stop()
		}
		ctx.Close()
	})

	return page
}

// waitForHTMX waits for any in-flight htmx requests to complete.
func waitForHTMX(page playwright.Page) {
	// Wait for htmx to settle (no active requests).
	page.WaitForFunction(`() => {
		if (typeof htmx === 'undefined') return true;
		return document.querySelectorAll('.htmx-request').length === 0;
	}`, playwright.PageWaitForFunctionOptions{
		Timeout: playwright.Float(float64(5 * time.Second / time.Millisecond)),
	})
}
