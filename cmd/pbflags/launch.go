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

func runLaunch(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: pb launch <subcommand>

Subcommands:
  list            List launches
  get <id>        Show launch detail
  ramp <id> <n>   Set ramp percentage (0-100)
  status <id> <s> Set lifecycle status
  soak <id>       Set ramp to 100% and status to SOAKING
  land <id>       Promote launch values and remove launch from config
  abandon <id>    Set status to ABANDONED
  kill <id>       Kill a launch (emergency disable)
  unkill <id>     Restore a killed launch`)
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		runLaunchList(args[1:])
	case "get":
		runLaunchGet(args[1:])
	case "ramp":
		runLaunchRamp(args[1:])
	case "status":
		runLaunchStatus(args[1:])
	case "soak":
		runLaunchSoak(args[1:])
	case "abandon":
		runLaunchAbandon(args[1:])
	case "land":
		runLaunchLand(args[1:])
	case "kill":
		runLaunchKill(args[1:])
	case "unkill":
		runLaunchUnkill(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pb launch: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

func runLaunchList(args []string) {
	fs := flag.NewFlagSet("pb launch list", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	feature := fs.String("feature", "", "Filter by feature ID")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.ListLaunches(context.Background(), connect.NewRequest(&pbflagsv1.ListLaunchesRequest{
		FeatureId: *feature,
	}))
	if err != nil {
		fatal(fmt.Errorf("list launches: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	launches := resp.Msg.GetLaunches()
	if len(launches) == 0 {
		fmt.Println("No launches found.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "LAUNCH\tDIMENSION\tRAMP\tSTATUS\tKILLED")
	for _, l := range launches {
		killed := ""
		if l.GetKilledAt() != nil {
			killed = "KILLED"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d%%\t%s\t%s\n",
			l.GetLaunchId(),
			l.GetDimension(),
			l.GetRampPercentage(),
			l.GetStatus(),
			killed,
		)
	}
	tw.Flush()
}

func runLaunchGet(args []string) {
	fs := flag.NewFlagSet("pb launch get", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch get <launch-id>")
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.GetLaunch(context.Background(), connect.NewRequest(&pbflagsv1.GetLaunchRequest{
		LaunchId: fs.Args()[0],
	}))
	if err != nil {
		fatal(fmt.Errorf("get launch: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	l := resp.Msg.GetLaunch()
	if l == nil {
		fmt.Fprintf(os.Stderr, "launch %q not found\n", fs.Args()[0])
		os.Exit(1)
	}

	fmt.Printf("Launch:     %s\n", l.GetLaunchId())
	if l.GetScopeFeatureId() != "" {
		fmt.Printf("Feature:    %s\n", l.GetScopeFeatureId())
	}
	fmt.Printf("Dimension:  %s\n", l.GetDimension())
	fmt.Printf("Ramp:       %d%%\n", l.GetRampPercentage())
	if l.GetRampSource() != "" && l.GetRampSource() != "unspecified" {
		fmt.Printf("Ramp src:   %s\n", l.GetRampSource())
	}
	fmt.Printf("Status:     %s\n", l.GetStatus())
	if l.GetKilledAt() != nil {
		fmt.Printf("Killed at:  %s\n", l.GetKilledAt().AsTime().Local().Format(time.DateTime))
	}
	if l.GetDescription() != "" {
		fmt.Printf("Desc:       %s\n", l.GetDescription())
	}
	if len(l.GetAffectedFeatures()) > 0 {
		fmt.Printf("Affects:    %v\n", l.GetAffectedFeatures())
	}
	if l.GetCreatedAt() != nil {
		fmt.Printf("Created:    %s\n", l.GetCreatedAt().AsTime().Local().Format(time.DateTime))
	}
}

func runLaunchRamp(args []string) {
	fs := flag.NewFlagSet("pb launch ramp", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pb launch ramp <launch-id> <percentage>")
		os.Exit(1)
	}

	pct, err := strconv.Atoi(fs.Args()[1])
	if err != nil || pct < 0 || pct > 100 {
		fmt.Fprintln(os.Stderr, "error: percentage must be 0-100")
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.UpdateLaunchRamp(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchRampRequest{
		LaunchId:       fs.Args()[0],
		RampPercentage: int32(pct),
		Source:         "cli",
	}))
	if err != nil {
		fatal(fmt.Errorf("update launch ramp: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s ramped to %d%%\n", fs.Args()[0], pct)
	if resp.Msg.GetWarning() != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", resp.Msg.GetWarning())
	}
}

func runLaunchStatus(args []string) {
	fs := flag.NewFlagSet("pb launch status", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) < 2 {
		fmt.Fprintln(os.Stderr, "usage: pb launch status <launch-id> <status>")
		fmt.Fprintln(os.Stderr, "  valid statuses: CREATED, ACTIVE, SOAKING, COMPLETED, ABANDONED")
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: fs.Args()[0],
		Status:   fs.Args()[1],
	}))
	if err != nil {
		fatal(fmt.Errorf("update launch status: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s status set to %s\n", fs.Args()[0], fs.Args()[1])
}

func runLaunchKill(args []string) {
	fs := flag.NewFlagSet("pb launch kill", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch kill <launch-id>")
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.KillLaunch(context.Background(), connect.NewRequest(&pbflagsv1.KillLaunchRequest{
		LaunchId: fs.Args()[0],
	}))
	if err != nil {
		fatal(fmt.Errorf("kill launch: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s killed\n", fs.Args()[0])
}

func runLaunchSoak(args []string) {
	fs := flag.NewFlagSet("pb launch soak", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch soak <launch-id>")
		os.Exit(1)
	}

	launchID := fs.Args()[0]
	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	// Set ramp to 100%.
	rampResp, err := client.UpdateLaunchRamp(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchRampRequest{
		LaunchId:       launchID,
		RampPercentage: 100,
		Source:         "cli",
	}))
	if err != nil {
		fatal(fmt.Errorf("set ramp to 100%%: %w", err))
	}

	// Set status to SOAKING.
	statusResp, err := client.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: launchID,
		Status:   "SOAKING",
	}))
	if err != nil {
		fatal(fmt.Errorf("set status to SOAKING: %w", err))
	}

	if *jsonOut {
		printJSON(statusResp.Msg)
		return
	}
	fmt.Printf("%s ramped to 100%% and status set to SOAKING\n", launchID)
	if rampResp.Msg.GetWarning() != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", rampResp.Msg.GetWarning())
	}
}

func runLaunchAbandon(args []string) {
	fs := flag.NewFlagSet("pb launch abandon", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch abandon <launch-id>")
		os.Exit(1)
	}

	launchID := fs.Args()[0]
	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.UpdateLaunchStatus(context.Background(), connect.NewRequest(&pbflagsv1.UpdateLaunchStatusRequest{
		LaunchId: launchID,
		Status:   "ABANDONED",
	}))
	if err != nil {
		fatal(fmt.Errorf("abandon launch: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s abandoned\n", launchID)
}

func runLaunchUnkill(args []string) {
	fs := flag.NewFlagSet("pb launch unkill", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb launch unkill <launch-id>")
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.UnkillLaunch(context.Background(), connect.NewRequest(&pbflagsv1.UnkillLaunchRequest{
		LaunchId: fs.Args()[0],
	}))
	if err != nil {
		fatal(fmt.Errorf("unkill launch: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s unkilled\n", fs.Args()[0])
}
