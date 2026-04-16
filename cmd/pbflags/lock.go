package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/adminclient"
)

// runLock implements `pb lock --reason="..."` to acquire the global sync
// lock, and `pb lock --status` to display the current lock state.
func runLock(args []string) {
	fs := flag.NewFlagSet("pb lock", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	reason := fs.String("reason", "", "Reason for the lock (required for acquire)")
	status := fs.Bool("status", false, "Show current lock state without changing it")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	if *status {
		resp, err := client.GetSyncLock(ctx, connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
		if err != nil {
			fatal(fmt.Errorf("get sync lock: %w", err))
		}
		if *jsonOut {
			printJSON(resp.Msg)
			return
		}
		printLockStatus(resp.Msg)
		return
	}

	if *reason == "" {
		fmt.Fprintln(os.Stderr, "error: --reason is required to lock (use --status to view current state)")
		os.Exit(1)
	}

	resp, err := client.AcquireSyncLock(ctx, connect.NewRequest(&pbflagsv1.AcquireSyncLockRequest{
		Reason: *reason,
	}))
	if err != nil {
		// On FailedPrecondition (already locked), fetch and pretty-print the
		// current holder so the user sees structured info, not a raw message.
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			cur, getErr := client.GetSyncLock(ctx, connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
			if getErr == nil && cur.Msg.GetHeld() {
				fmt.Fprintln(os.Stderr, "Sync is already locked.")
				printLockStatus(cur.Msg)
				os.Exit(2)
			}
		}
		fatal(fmt.Errorf("lock: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}

	since := ""
	if resp.Msg.GetHeldSince() != nil {
		since = resp.Msg.GetHeldSince().AsTime().Local().Format(time.DateTime)
	}
	fmt.Println("Sync locked.")
	if since != "" {
		fmt.Printf("  since:  %s\n", since)
	}
	fmt.Printf("  reason: %s\n", *reason)
	fmt.Println()
	fmt.Println("All config syncs will fail until unlocked.")
	fmt.Println("Unlock with: pb unlock")
}

// runUnlock implements `pb unlock` to release the global sync lock.
func runUnlock(args []string) {
	fs := flag.NewFlagSet("pb unlock", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		fatal(err)
	}
	ctx := context.Background()

	resp, err := client.ReleaseSyncLock(ctx, connect.NewRequest(&pbflagsv1.ReleaseSyncLockRequest{}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			fmt.Fprintln(os.Stderr, "Sync is not locked.")
			os.Exit(2)
		}
		fatal(fmt.Errorf("unlock: %w", err))
	}

	if *jsonOut {
		printJSON(resp.Msg)
		return
	}
	fmt.Println("Sync unlocked.")
}

// printLockStatus prints a GetSyncLockResponse in human-readable form.
func printLockStatus(m *pbflagsv1.GetSyncLockResponse) {
	if !m.GetHeld() {
		fmt.Println("Sync: UNLOCKED")
		return
	}
	fmt.Println("Sync: LOCKED")
	fmt.Printf("  holder: %s\n", m.GetActor())
	fmt.Printf("  reason: %s\n", m.GetReason())
	if m.GetHeldSince() != nil {
		t := m.GetHeldSince().AsTime()
		age := time.Since(t).Truncate(time.Second)
		fmt.Printf("  since:  %s (%s ago)\n", t.Local().Format(time.DateTime), age)
	}
}
