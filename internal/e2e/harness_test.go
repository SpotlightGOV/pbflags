//go:build e2e

package e2e

import (
	"context"
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

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/admin"
	"github.com/SpotlightGOV/pbflags/internal/admin/web"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// testEnv holds all resources for an E2E test session.
type testEnv struct {
	pool    *pgxpool.Pool
	baseURL string
	pw      *playwright.Playwright
	browser playwright.Browser
}

// testDefs returns a set of flag definitions for E2E tests.
// Uses a unique feature name to avoid collisions with other tests.
func testDefs(prefix string) []evaluator.FlagDef {
	return []evaluator.FlagDef{
		{FlagID: prefix + "/1", FeatureID: prefix, FieldNum: 1, Name: "enabled", FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL, Layer: "user", Default: boolVal(true)},
		{FlagID: prefix + "/2", FeatureID: prefix, FieldNum: 2, Name: "greeting", FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING, Layer: "", Default: stringVal("hello")},
		{FlagID: prefix + "/3", FeatureID: prefix, FieldNum: 3, Name: "max_items", FlagType: pbflagsv1.FlagType_FLAG_TYPE_INT64, Layer: "", Default: int64Val(10)},
		{FlagID: prefix + "/4", FeatureID: prefix, FieldNum: 4, Name: "threshold", FlagType: pbflagsv1.FlagType_FLAG_TYPE_DOUBLE, Layer: "", Default: doubleVal(0.5)},
	}
}

func boolVal(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}
func stringVal(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}
func int64Val(v int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}
}
func doubleVal(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}

// seedFlags inserts a feature and its flags into the database.
func seedFlags(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `INSERT INTO feature_flags.features (feature_id) VALUES ($1) ON CONFLICT DO NOTHING`, prefix)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, flag_type, layer, state) VALUES
			($1, $5, 1, 'BOOL',   'USER',   'DEFAULT'),
			($2, $5, 2, 'STRING', 'GLOBAL', 'DEFAULT'),
			($3, $5, 3, 'INT64',  'GLOBAL', 'DEFAULT'),
			($4, $5, 4, 'DOUBLE', 'GLOBAL', 'DEFAULT')
		ON CONFLICT DO NOTHING`,
		prefix+"/1", prefix+"/2", prefix+"/3", prefix+"/4", prefix)
	require.NoError(t, err)
}

// setupEnv starts a PostgreSQL test container, a web admin server, and
// a Playwright browser. The returned testEnv is cleaned up automatically.
func setupEnv(t *testing.T) *testEnv {
	t.Helper()

	_, pool := testdb.Require(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Build a minimal flag set and store so the web handler can serve pages.
	prefix := "e2e_svc"
	defs := testDefs(prefix)
	defaults := evaluator.NewDefaults(defs)
	reg := evaluator.NewRegistry(defaults)

	store := admin.NewStore(pool, logger)
	store.SetRegistry(reg)

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
