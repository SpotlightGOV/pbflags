//go:build ignore

// Seeds a couple of demo condition overrides on the dev database so the
// admin UI shows the override + sync-lock surfaces without an operator
// having to click through the popovers first. Idempotent: re-running
// upserts the same overrides.
//
// Usage (from the repo root):
//
//	go run dev/seed-overrides.go
//
// Database URL defaults to the same Postgres the rest of the dev tooling
// targets; override with PBFLAGS_DATABASE.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/admin"
)

const defaultDB = "postgres://admin:admin@localhost:5433/pbflags?sslmode=disable"

func main() {
	dbURL := os.Getenv("PBFLAGS_DATABASE")
	if dbURL == "" {
		dbURL = defaultDB
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	store := admin.NewStore(pool, logger)

	// Override #1: notifications/email_enabled cond[0] (is_internal=true)
	// flipped to false. Mimics an incident where internal email is muted.
	condIdx0 := int32(0)
	if _, err := store.SetConditionOverride(ctx,
		"notifications/email_enabled",
		&condIdx0,
		boolValue(false),
		"ui",
		"demo@local",
		"demo: muting internal email for incident response",
	); err != nil {
		fatal("set notifications/email_enabled cond[0]", err)
	}
	fmt.Println("✓ override: notifications/email_enabled cond[0] = false")

	// Override #2: notifications/score_threshold static-default flipped
	// to 0.9 (cranked up to limit notification volume).
	if _, err := store.SetConditionOverride(ctx,
		"notifications/score_threshold",
		nil,
		doubleValue(0.9),
		"ui",
		"demo@local",
		"demo: bumping score threshold to reduce alert volume",
	); err != nil {
		fatal("set notifications/score_threshold default", err)
	}
	fmt.Println("✓ override: notifications/score_threshold default = 0.9")

	fmt.Println()
	fmt.Println("Demo overrides seeded. Refresh the admin UI to see:")
	fmt.Println("  - amber 'OVR' badges on the dashboard rows")
	fmt.Println("  - amber-tinted condition rows on the flag detail pages")
	fmt.Println("  - the 'Clear all overrides' header button on each affected flag")
	fmt.Println()
	fmt.Println("To exercise the sync-lock banner, click 'Acquire' in the sidebar lock card.")
}

func fatal(what string, err error) {
	// Tolerate ErrFlagNotFound — the demo features may not be synced yet.
	if errors.Is(err, admin.ErrFlagNotFound) {
		fmt.Fprintf(os.Stderr, "skip: %s — flag not synced yet (run `make dev-seed` first)\n", what)
		return
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}

func boolValue(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}

func doubleValue(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}
