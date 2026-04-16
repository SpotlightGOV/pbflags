package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/adminclient"
)

// runOverrides implements `pb overrides` — a cross-flag listing of every
// active condition override. Designed for spotting forgotten overrides
// (--min-age) and auditing per-actor activity (--actor).
//
// Overrides are persistent until a human clears them or sync clears
// them on success — there is no TTL, so this listing is the only way
// to find drift across the fleet.
func runOverrides(args []string) {
	fs := flag.NewFlagSet("pb overrides", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	flagFilter := fs.String("flag", "", "Filter to a single flag_id")
	minAge := fs.String("min-age", "", `Show overrides older than this (e.g. "24h", "7d", "30m")`)
	actor := fs.String("actor", "", "Filter by actor (case-insensitive substring)")
	jsonOut := fs.Bool("json", false, "Output the raw proto response as JSON")
	fs.Parse(args)

	var minAgeSec int64
	if *minAge != "" {
		d, err := parseExtendedDuration(*minAge)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --min-age %q: %v\n", *minAge, err)
			os.Exit(1)
		}
		if d < 0 {
			fmt.Fprintln(os.Stderr, "error: --min-age must be non-negative")
			os.Exit(1)
		}
		minAgeSec = int64(d.Seconds())
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	resp, err := client.ListConditionOverrides(ctx, connect.NewRequest(&pbflagsv1.ListConditionOverridesRequest{
		FlagId:        *flagFilter,
		MinAgeSeconds: minAgeSec,
		Actor:         *actor,
	}))
	if err != nil {
		fatal(fmt.Errorf("list overrides: %w", err))
	}

	if *jsonOut {
		// Emit the raw proto so the JSON shape is stable / scriptable;
		// the WAS column is a CLI render concern computed from per-flag
		// GetFlag, not part of the wire response.
		printJSON(resp.Msg)
		return
	}

	entries := resp.Msg.GetEntries()
	if len(entries) == 0 {
		fmt.Println("No active overrides.")
		return
	}

	// Pre-fetch each unique flag's chain so we can render the WAS
	// column (the value being shadowed at the override's position).
	// Bounded by the number of distinct flag_ids in the result set,
	// which is at most defaultOverrideListLimit (100).
	chains := make(map[string]*pbflagsv1.FlagDetail)
	for _, e := range entries {
		fid := e.GetFlagId()
		if _, ok := chains[fid]; ok {
			continue
		}
		fr, fErr := client.GetFlag(ctx, connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: fid}))
		if fErr != nil {
			// A missing or errored flag isn't fatal — render "?" for
			// its WAS cells so the rest of the listing still appears.
			chains[fid] = nil
			continue
		}
		chains[fid] = fr.Msg.GetFlag()
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FLAG\tCOND\tVALUE\tWAS\tAGE\tACTOR\tREASON")
	for _, e := range entries {
		cond := "*"
		if e.ConditionIndex != nil {
			cond = strconv.Itoa(int(*e.ConditionIndex))
		}
		was := "?"
		if fd := chains[e.GetFlagId()]; fd != nil {
			was = shortValue(originalForOverride(fd, e.ConditionIndex))
		}
		age := "—"
		if e.GetCreatedAt() != nil {
			age = humanAge(time.Since(e.GetCreatedAt().AsTime()))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.GetFlagId(),
			cond,
			shortValue(e.GetOverrideValue()),
			was,
			age,
			e.GetActor(),
			truncate(e.GetReason(), 60),
		)
	}
	tw.Flush()
}

// originalForOverride returns the value being shadowed by an override
// at (flag, condIdx). nil condIdx → static / compiled default. Returns
// nil when the index is out of range (chain shape changed since the
// override was created); the caller renders "—" / "?" in that case.
func originalForOverride(fd *pbflagsv1.FlagDetail, condIdx *int32) *pbflagsv1.FlagValue {
	if condIdx == nil {
		return fd.GetDefaultValue()
	}
	idx := int(*condIdx)
	chain := fd.GetConditions()
	if idx < 0 || idx >= len(chain) {
		return nil
	}
	return chain[idx].GetValue()
}

// parseExtendedDuration extends time.ParseDuration with a 'd' suffix
// (days), since "7d" reads more naturally than "168h" for the
// forgotten-override use case.
func parseExtendedDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Accept simple "<N>d" form. Mixed forms (e.g. "1d6h") are not
	// supported — keep the parser narrow so it stays predictable.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("parse days: %w", err)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(s)
}

// humanAge renders a duration in the most useful single unit for an
// audit listing. Whole-day overrides matter most; sub-second precision
// would just be noise.
func humanAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// truncate caps long reason strings so they don't blow out the table
// width. Keeps the first n runes and appends an ellipsis.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
