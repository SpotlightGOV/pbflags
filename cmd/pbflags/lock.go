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
//
// Exit codes (consistent across all error paths):
//   - 0 success
//   - 1 any failure (network, server error, already-locked, missing
//     --reason, etc.)
//
// In --json mode every code path emits a JSON document on stdout:
//   - success: the proto response message (AcquireSyncLockResponse or
//     GetSyncLockResponse).
//   - already-locked: the current GetSyncLockResponse, plus a
//     human-readable note on stderr (suppressed in --json).
//   - other errors: a {"error": "..."} object.
func runLock(args []string) {
	fs := flag.NewFlagSet("pb lock", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	reason := fs.String("reason", "", "Reason for the lock (required for acquire)")
	status := fs.Bool("status", false, "Show current lock state without changing it")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	client, err := adminclient.New(*admin)
	if err != nil {
		emitError(*jsonOut, err)
		os.Exit(1)
	}
	ctx := context.Background()

	if *status {
		resp, err := client.GetSyncLock(ctx, connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
		if err != nil {
			emitError(*jsonOut, fmt.Errorf("get sync lock: %w", err))
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(resp.Msg)
			return
		}
		printLockStatus(resp.Msg)
		return
	}

	if *reason == "" {
		emitError(*jsonOut, fmt.Errorf("--reason is required to lock (use --status to view current state)"))
		os.Exit(1)
	}

	resp, err := client.AcquireSyncLock(ctx, connect.NewRequest(&pbflagsv1.AcquireSyncLockRequest{
		Reason: *reason,
	}))
	if err != nil {
		// On FailedPrecondition (already locked), prefer to surface the
		// current holder so the operator can see who/why. In --json mode
		// emit the GetSyncLockResponse on stdout (machine-parseable);
		// otherwise print a human-readable note. Either way exit 1 to
		// match all other failure paths — the caller can disambiguate
		// "already locked" from the response shape (held=true).
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			cur, getErr := client.GetSyncLock(ctx, connect.NewRequest(&pbflagsv1.GetSyncLockRequest{}))
			if getErr == nil && cur.Msg.GetHeld() {
				if *jsonOut {
					printJSON(cur.Msg)
				} else {
					fmt.Fprintln(os.Stderr, "Sync is already locked.")
					printLockStatus(cur.Msg)
				}
				os.Exit(1)
			}
		}
		emitError(*jsonOut, fmt.Errorf("lock: %w", err))
		os.Exit(1)
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
	fmt.Println(`Unlock with: pb unlock --reason="..."`)
}

// runUnlock implements `pb unlock --reason="..."` to release the global
// sync lock. The reason is required and captured in the audit row.
//
// Exit codes / --json behaviour mirror runLock: all failures exit 1
// and emit JSON-on-stdout when --json is set.
func runUnlock(args []string) {
	fs := flag.NewFlagSet("pb unlock", flag.ExitOnError)
	admin := fs.String("admin", "", "Admin API URL (or PBFLAGS_ADMIN_URL)")
	reason := fs.String("reason", "", "Reason for releasing the lock (required, captured in audit log)")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if *reason == "" {
		emitError(*jsonOut, fmt.Errorf("--reason is required to unlock"))
		os.Exit(1)
	}

	client, err := adminclient.New(*admin)
	if err != nil {
		emitError(*jsonOut, err)
		os.Exit(1)
	}
	ctx := context.Background()

	resp, err := client.ReleaseSyncLock(ctx, connect.NewRequest(&pbflagsv1.ReleaseSyncLockRequest{
		Reason: *reason,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			emitError(*jsonOut, fmt.Errorf("sync is not locked"))
			os.Exit(1)
		}
		emitError(*jsonOut, fmt.Errorf("unlock: %w", err))
		os.Exit(1)
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

// emitError writes an error in --json or human form. In --json mode
// the document goes to stdout (so machine consumers can pipe it like a
// normal response); in human mode it goes to stderr with the
// conventional "error: " prefix.
func emitError(jsonOut bool, err error) {
	if jsonOut {
		printJSON(map[string]string{"error": err.Error()})
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
}
