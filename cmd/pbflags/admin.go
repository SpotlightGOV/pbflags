package main

import (
	"context"
	"encoding/json"
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
	"github.com/SpotlightGOV/pbflags/internal/credentials"
)

// --- flag ---

func runFlag(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: pb flag <subcommand>

Subcommands:
  list           List features and flags
  get <id>       Show flag detail
  kill <id>      Kill a flag (emergency disable)
  unkill <id>    Restore a killed flag`)
		os.Exit(1)
	}

	switch args[0] {
	case "list":
		runFlagList(args[1:])
	case "get":
		runFlagGet(args[1:])
	case "kill":
		runFlagKill(args[1:])
	case "unkill":
		runFlagUnkill(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pb flag: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

// --- flag list ---

func runFlagList(args []string) {
	fs := flag.NewFlagSet("pb flag list", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL, default http://localhost:9200)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.ListFeatures(context.Background(), connect.NewRequest(&pbflagsv1.ListFeaturesRequest{}))
	if err != nil {
		fatal(fmt.Errorf("list features: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	features := resp.Msg.GetFeatures()
	if len(features) == 0 {
		fmt.Println("No features found.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FLAG\tTYPE\tSTATE\tVALUE")
	for _, feat := range features {
		for _, f := range feat.GetFlags() {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				f.GetFlagId(),
				shortType(f.GetFlagType()),
				shortState(f.GetState()),
				shortValue(f.GetCurrentValue()),
			)
		}
	}
	tw.Flush()
}

// --- get ---

func runFlagGet(args []string) {
	fs := flag.NewFlagSet("pb flag get", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pb flag get <flag-id>")
		os.Exit(1)
	}
	flagID := fs.Args()[0]

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.GetFlag(context.Background(), connect.NewRequest(&pbflagsv1.GetFlagRequest{FlagId: flagID}))
	if err != nil {
		fatal(fmt.Errorf("get flag: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	f := resp.Msg.GetFlag()
	if f == nil {
		fmt.Fprintf(os.Stderr, "flag %q not found\n", flagID)
		os.Exit(1)
	}

	fmt.Printf("Flag:     %s\n", f.GetFlagId())
	if f.GetDisplayName() != "" {
		fmt.Printf("Name:     %s\n", f.GetDisplayName())
	}
	if f.GetDescription() != "" {
		fmt.Printf("Desc:     %s\n", f.GetDescription())
	}
	fmt.Printf("Type:     %s\n", shortType(f.GetFlagType()))
	fmt.Printf("State:    %s\n", shortState(f.GetState()))
	fmt.Printf("Default:  %s\n", shortValue(f.GetDefaultValue()))
	fmt.Printf("Current:  %s\n", shortValue(f.GetCurrentValue()))
	if f.GetLayer() != "" {
		fmt.Printf("Layer:    %s\n", f.GetLayer())
	}
	if f.GetArchived() {
		fmt.Printf("Archived: yes\n")
	}
}

// --- kill / unkill ---

func runFlagKill(args []string) {
	doStateChange(args, pbflagsv1.State_STATE_KILLED, "killed")
}

func runFlagUnkill(args []string) {
	doStateChange(args, pbflagsv1.State_STATE_DEFAULT, "unkilled")
}

func doStateChange(args []string, state pbflagsv1.State, verb string) {
	cmdName := "kill"
	if state == pbflagsv1.State_STATE_DEFAULT {
		cmdName = "unkill"
	}
	fs := flag.NewFlagSet("pb flag "+cmdName, flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if len(fs.Args()) == 0 {
		fmt.Fprintf(os.Stderr, "usage: pb flag %s <flag-id>\n", cmdName)
		os.Exit(1)
	}
	flagID := fs.Args()[0]

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.UpdateFlagState(context.Background(), connect.NewRequest(&pbflagsv1.UpdateFlagStateRequest{
		FlagId: flagID,
		State:  state,
	}))
	if err != nil {
		fatal(fmt.Errorf("%s flag: %w", cmdName, err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Printf("%s %s\n", flagID, verb)
}

// --- audit ---

func runAudit(args []string) {
	fs := flag.NewFlagSet("pb audit", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL")
	flagFilter := fs.String("flag", "", "Filter by flag ID")
	limit := fs.Int("limit", 20, "Max entries to return")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}

	resp, err := client.GetAuditLog(context.Background(), connect.NewRequest(&pbflagsv1.GetAuditLogRequest{
		FlagId: *flagFilter,
		Limit:  int32(*limit),
	}))
	if err != nil {
		fatal(fmt.Errorf("audit log: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	entries := resp.Msg.GetEntries()
	if len(entries) == 0 {
		fmt.Println("No audit entries.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tFLAG\tACTION\tACTOR")
	for _, e := range entries {
		ts := ""
		if e.GetCreatedAt() != nil {
			ts = e.GetCreatedAt().AsTime().Local().Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			ts,
			e.GetFlagId(),
			e.GetAction(),
			e.GetActor(),
		)
	}
	tw.Flush()
}

// --- auth ---

func runAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: pb auth <subcommand>

Subcommands:
  login    Save API credentials
  status   Show current identity
  logout   Remove stored credentials`)
		os.Exit(1)
	}

	switch args[0] {
	case "login":
		runAuthLogin(args[1:])
	case "status":
		runAuthStatus(args[1:])
	case "logout":
		runAuthLogout(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pb auth: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

func runAuthLogin(args []string) {
	fs := flag.NewFlagSet("pb auth login", flag.ExitOnError)
	token := fs.String("token", "", "API token")
	actor := fs.String("actor", "", "Actor identity for audit logging (e.g. alice@example.com)")
	fs.Parse(args)

	if *token == "" {
		fmt.Fprintln(os.Stderr, "error: --token is required")
		os.Exit(1)
	}

	if err := credentials.Save(credentials.Credentials{
		Token: *token,
		Actor: *actor,
	}); err != nil {
		fatal(err)
	}

	path, _ := credentials.Path()
	fmt.Printf("Credentials saved to %s\n", path)
}

func runAuthStatus(_ []string) {
	creds, err := credentials.Load()
	if err != nil {
		fatal(err)
	}

	if creds.Token == "" {
		fmt.Println("Not authenticated.")
		fmt.Println("Run: pb auth login --token=<token>")
		return
	}

	// Show source.
	if os.Getenv("PBFLAGS_TOKEN") != "" {
		fmt.Println("Source:  PBFLAGS_TOKEN environment variable")
	} else {
		path, _ := credentials.Path()
		fmt.Printf("Source:  %s\n", path)
	}

	if len(creds.Token) > 8 {
		fmt.Printf("Token:   %s...%s\n", creds.Token[:4], creds.Token[len(creds.Token)-4:])
	} else {
		fmt.Printf("Token:   ***\n")
	}
	if creds.Actor != "" {
		fmt.Printf("Actor:   %s\n", creds.Actor)
	}
}

func runAuthLogout(_ []string) {
	if err := credentials.Remove(); err != nil {
		fatal(err)
	}
	fmt.Println("Credentials removed.")
}

// --- helpers ---

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fatal(err)
	}
}

func shortState(s pbflagsv1.State) string {
	switch s {
	case pbflagsv1.State_STATE_ENABLED:
		return "enabled"
	case pbflagsv1.State_STATE_DEFAULT:
		return "default"
	case pbflagsv1.State_STATE_KILLED:
		return "KILLED"
	default:
		return s.String()
	}
}

func shortType(t pbflagsv1.FlagType) string {
	s := t.String()
	s = strings.TrimPrefix(s, "FLAG_TYPE_")
	return strings.ToLower(s)
}

// parseFlagValue parses a CLI string into a FlagValue typed according to
// the flag's declared type. List types are comma-separated. Empty strings
// are accepted for STRING and *_LIST types (yielding empty values / lists).
func parseFlagValue(t pbflagsv1.FlagType, raw string) (*pbflagsv1.FlagValue, error) {
	switch t {
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid bool %q: %w", raw, err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: b}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_STRING:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: raw}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_INT64:
		i, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 %q: %w", raw, err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: i}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid double %q: %w", raw, err)
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: f}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST:
		parts := splitListValue(raw)
		out := make([]bool, len(parts))
		for i, p := range parts {
			b, err := strconv.ParseBool(p)
			if err != nil {
				return nil, fmt.Errorf("invalid bool %q at index %d: %w", p, i, err)
			}
			out[i] = b
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{
			BoolListValue: &pbflagsv1.BoolList{Values: out},
		}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST:
		parts := splitListValue(raw)
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{
			StringListValue: &pbflagsv1.StringList{Values: parts},
		}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST:
		parts := splitListValue(raw)
		out := make([]int64, len(parts))
		for i, p := range parts {
			v, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid int64 %q at index %d: %w", p, i, err)
			}
			out[i] = v
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{
			Int64ListValue: &pbflagsv1.Int64List{Values: out},
		}}, nil
	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST:
		parts := splitListValue(raw)
		out := make([]float64, len(parts))
		for i, p := range parts {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid double %q at index %d: %w", p, i, err)
			}
			out[i] = v
		}
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{
			DoubleListValue: &pbflagsv1.DoubleList{Values: out},
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported flag type %s", t)
	}
}

// splitListValue splits a comma-separated CLI list value, trimming
// whitespace around each element. An empty input yields an empty slice.
func splitListValue(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func shortValue(v *pbflagsv1.FlagValue) string {
	if v == nil {
		return "—"
	}
	switch val := v.Value.(type) {
	case *pbflagsv1.FlagValue_BoolValue:
		return strconv.FormatBool(val.BoolValue)
	case *pbflagsv1.FlagValue_StringValue:
		return val.StringValue
	case *pbflagsv1.FlagValue_Int64Value:
		return strconv.FormatInt(val.Int64Value, 10)
	case *pbflagsv1.FlagValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'f', -1, 64)
	case *pbflagsv1.FlagValue_StringListValue:
		if val.StringListValue == nil || len(val.StringListValue.Values) == 0 {
			return "[]"
		}
		return "[" + strings.Join(val.StringListValue.Values, ", ") + "]"
	case *pbflagsv1.FlagValue_Int64ListValue:
		if val.Int64ListValue == nil || len(val.Int64ListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.Int64ListValue.Values))
		for i, v := range val.Int64ListValue.Values {
			parts[i] = strconv.FormatInt(v, 10)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pbflagsv1.FlagValue_DoubleListValue:
		if val.DoubleListValue == nil || len(val.DoubleListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.DoubleListValue.Values))
		for i, v := range val.DoubleListValue.Values {
			parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *pbflagsv1.FlagValue_BoolListValue:
		if val.BoolListValue == nil || len(val.BoolListValue.Values) == 0 {
			return "[]"
		}
		parts := make([]string, len(val.BoolListValue.Values))
		for i, v := range val.BoolListValue.Values {
			parts[i] = strconv.FormatBool(v)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return "—"
	}
}
