package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/adminclient"
)

// runCondition dispatches `pb condition <subcommand>`.
func runCondition(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: pb condition <subcommand>

Subcommands:
  override <flag_id> [condition_index] <value> --reason="..."
                                Set a value override on a specific condition
                                (omit condition_index to override the static
                                / compiled default).
  clear <flag_id> [condition_index]
                                Clear an override (omit condition_index to
                                clear ALL overrides on the flag).
  list <flag_id>                Show the condition chain + active overrides.`)
		os.Exit(1)
	}

	switch args[0] {
	case "override":
		runConditionOverride(args[1:])
	case "clear":
		runConditionClear(args[1:])
	case "list":
		runConditionList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pb condition: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

// --- pb condition override ---

func runConditionOverride(args []string) {
	fs := flag.NewFlagSet("pb condition override", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	reason := fs.String("reason", "", "Reason for the override (required)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 2 || len(pos) > 3 {
		fmt.Fprintln(os.Stderr, `usage: pb condition override <flag_id> [condition_index] <value> --reason="..."`)
		os.Exit(1)
	}
	if *reason == "" {
		fmt.Fprintln(os.Stderr, "error: --reason is required")
		os.Exit(1)
	}

	flagID := pos[0]
	var condIdx *int32
	var rawValue string
	if len(pos) == 3 {
		idx, err := strconv.Atoi(pos[1])
		if err != nil || idx < 0 {
			fmt.Fprintf(os.Stderr, "error: invalid condition_index %q (must be a non-negative integer)\n", pos[1])
			os.Exit(1)
		}
		i32 := int32(idx)
		condIdx = &i32
		rawValue = pos[2]
	} else {
		rawValue = pos[1]
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	// Fetch the flag to learn its type so we can parse the input value
	// correctly, and to check config-managed / lock-status nudges.
	flagResp, err := client.GetFlag(ctx, connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: flagID}))
	if err != nil {
		fatal(fmt.Errorf("get flag: %w", err))
	}
	fd := flagResp.Msg.GetFlag()
	if fd == nil {
		fmt.Fprintf(os.Stderr, "flag %q not found\n", flagID)
		os.Exit(1)
	}

	value, err := parseFlagValue(fd.GetFlagType(), rawValue)
	if err != nil {
		fatal(fmt.Errorf("parse value: %w", err))
	}

	req := &pbflagsv1.SetConditionOverrideRequest{
		FlagId:         flagID,
		ConditionIndex: condIdx,
		Value:          value,
		Reason:         *reason,
		Source:         "cli",
	}
	resp, err := client.SetConditionOverride(ctx, connect.NewRequest(req))
	if err != nil {
		fatal(fmt.Errorf("set condition override: %w", err))
	}

	// Nudge: if config-managed and the sync lock isn't held, mention the
	// safer workflow. Don't block — operators may legitimately want a
	// short-lived override without locking the pipeline.
	if fd.GetConfigManaged() {
		lockResp, lockErr := client.GetSyncLock(ctx, connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
		held := lockErr == nil && lockResp.Msg.GetHeld()
		if !held {
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "⚠ This flag is managed by config-as-code. The next sync will clear this override.")
			fmt.Fprintln(os.Stderr, "  For incident response, the safer workflow is:")
			fmt.Fprintln(os.Stderr, "    pb lock --reason=\"...\"   →  set overrides  →  follow-up config PR  →  pb unlock")
			fmt.Fprintln(os.Stderr, "")
		}
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	target := "default"
	if condIdx != nil {
		target = fmt.Sprintf("condition[%d]", *condIdx)
	}
	fmt.Printf("Override set on %s %s: %s\n", flagID, target, shortValue(value))
	if prev := resp.Msg.GetPreviousValue(); prev != nil {
		fmt.Printf("  was: %s\n", shortValue(prev))
	}
	if w := resp.Msg.GetWarning(); w != "" {
		fmt.Printf("  warning: %s\n", w)
	}
}

// --- pb condition clear ---

func runConditionClear(args []string) {
	fs := flag.NewFlagSet("pb condition clear", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 1 || len(pos) > 2 {
		fmt.Fprintln(os.Stderr, "usage: pb condition clear <flag_id> [condition_index]")
		os.Exit(1)
	}
	flagID := pos[0]

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	if len(pos) == 1 {
		resp, err := client.ClearAllConditionOverrides(ctx, connect.NewRequest(&pbflagsv1.ClearAllConditionOverridesRequest{
			FlagId: flagID,
		}))
		if err != nil {
			fatal(fmt.Errorf("clear all: %w", err))
		}
		if *jsonOut {
			printJSON(resp.Msg)
			return
		}
		fmt.Printf("Cleared %d override(s) on %s\n", resp.Msg.GetClearedCount(), flagID)
		return
	}

	idx, err := strconv.Atoi(pos[1])
	if err != nil || idx < 0 {
		fmt.Fprintf(os.Stderr, "error: invalid condition_index %q (must be a non-negative integer; omit to clear all)\n", pos[1])
		os.Exit(1)
	}
	i32 := int32(idx)

	resp, err := client.ClearConditionOverride(ctx, connect.NewRequest(&pbflagsv1.ClearConditionOverrideRequest{
		FlagId:         flagID,
		ConditionIndex: &i32,
	}))
	if err != nil {
		fatal(fmt.Errorf("clear override: %w", err))
	}
	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("Cleared override on %s condition[%d]\n", flagID, idx)
}

// --- pb condition list ---

func runConditionList(args []string) {
	fs := flag.NewFlagSet("pb condition list", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) != 1 {
		fmt.Fprintln(os.Stderr, "usage: pb condition list <flag_id>")
		os.Exit(1)
	}
	flagID := fs.Args()[0]

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	resp, err := client.GetFlag(ctx, connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: flagID}))
	if err != nil {
		fatal(fmt.Errorf("get flag: %w", err))
	}
	fd := resp.Msg.GetFlag()
	if fd == nil {
		fmt.Fprintf(os.Stderr, "flag %q not found\n", flagID)
		os.Exit(1)
	}

	if *jsonOut {
		printJSON(fd)
		return
	}

	// Build override map keyed by condition index. Static-default override
	// (nil index) is shown separately.
	condOverrides := map[int32]*pbflagsv1.ConditionOverrideDetail{}
	var defaultOverride *pbflagsv1.ConditionOverrideDetail
	for _, o := range fd.GetConditionOverrides() {
		if o.ConditionIndex == nil {
			defaultOverride = o
		} else {
			condOverrides[*o.ConditionIndex] = o
		}
	}

	fmt.Printf("Flag:   %s\n", fd.GetFlagId())
	fmt.Printf("Type:   %s\n", shortType(fd.GetFlagType()))
	if fd.GetConfigManaged() {
		fmt.Println("Source: config-as-code")
	}
	fmt.Println()

	chain := fd.GetConditions()
	if len(chain) == 0 {
		fmt.Println("Conditions: (none — static flag)")
		fmt.Printf("  default: %s\n", shortValue(fd.GetDefaultValue()))
		if defaultOverride != nil {
			fmt.Printf("           ⚠ OVERRIDE: %s  (%s, %s ago) — %s\n",
				shortValue(defaultOverride.GetOverrideValue()),
				defaultOverride.GetActor(),
				time.Since(defaultOverride.GetCreatedAt().AsTime()).Truncate(time.Second),
				defaultOverride.GetReason())
		}
		return
	}

	fmt.Println("Conditions:")
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tCEL\tVALUE\tLAUNCH\tOVERRIDE")
	for _, c := range chain {
		cel := c.GetCel()
		if cel == "" {
			cel = "<otherwise>"
		}
		launch := "—"
		if c.GetLaunchId() != "" {
			launch = c.GetLaunchId()
		}
		ov := "—"
		if o, ok := condOverrides[c.GetIndex()]; ok {
			ov = fmt.Sprintf("%s (was: %s)", shortValue(o.GetOverrideValue()), shortValue(c.GetValue()))
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n",
			c.GetIndex(),
			cel,
			shortValue(c.GetValue()),
			launch,
			ov,
		)
	}
	tw.Flush()

	if defaultOverride != nil {
		fmt.Println()
		fmt.Printf("Static-default override: %s  (%s, %s ago) — %s\n",
			shortValue(defaultOverride.GetOverrideValue()),
			defaultOverride.GetActor(),
			time.Since(defaultOverride.GetCreatedAt().AsTime()).Truncate(time.Second),
			defaultOverride.GetReason())
	}

	// Print details for each active override.
	if len(condOverrides) > 0 {
		fmt.Println()
		fmt.Println("Active overrides:")
		for _, c := range chain {
			o, ok := condOverrides[c.GetIndex()]
			if !ok {
				continue
			}
			fmt.Printf("  [%d] %s by %s (%s ago) — %s\n",
				c.GetIndex(),
				shortValue(o.GetOverrideValue()),
				o.GetActor(),
				time.Since(o.GetCreatedAt().AsTime()).Truncate(time.Second),
				o.GetReason())
		}
	}
}
