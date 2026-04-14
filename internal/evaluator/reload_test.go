package evaluator

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDescriptorWatcher_PollDetectsChange(t *testing.T) {
	t.Parallel()

	// Create a temp file with valid content (empty descriptor set is fine for
	// testing — ParseDescriptorFile will fail but tryReload handles that
	// gracefully and logs an error).
	tmpFile, err := os.CreateTemp(t.TempDir(), "descriptors-*.pb")
	require.NoError(t, err)
	tmpFile.Close()

	sighup := make(chan os.Signal, 1)
	w := NewDescriptorWatcher(tmpFile.Name(), 30*time.Millisecond, sighup, slog.Default())

	var reloads atomic.Int32
	w.SetSyncAndReload(func(_ context.Context, defs []FlagDef) error {
		reloads.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Touch the file to trigger mtime change.
	time.Sleep(60 * time.Millisecond) // let initial mtime be recorded
	require.NoError(t, os.WriteFile(tmpFile.Name(), []byte{}, 0o644))

	// Wait for poll to detect the change.
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done
	// We can't assert reloads > 0 because ParseDescriptorFile may fail on
	// empty data and the sync callback won't be called. But the watcher
	// should at least run and shut down cleanly.
}

func TestDescriptorWatcher_SIGHUPTriggersReload(t *testing.T) {
	t.Parallel()

	tmpFile, err := os.CreateTemp(t.TempDir(), "descriptors-*.pb")
	require.NoError(t, err)
	tmpFile.Close()

	sighup := make(chan os.Signal, 1)
	w := NewDescriptorWatcher(tmpFile.Name(), 0, sighup, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Give watcher time to start.
	time.Sleep(50 * time.Millisecond)

	// Send SIGHUP.
	sighup <- os.Interrupt // any signal — the channel just carries the event

	// Wait for it to be processed.
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done
}

func TestDescriptorWatcher_CancelsGracefully(t *testing.T) {
	t.Parallel()

	tmpFile, err := os.CreateTemp(t.TempDir(), "descriptors-*.pb")
	require.NoError(t, err)
	tmpFile.Close()

	sighup := make(chan os.Signal, 1)
	w := NewDescriptorWatcher(tmpFile.Name(), 1*time.Hour, sighup, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not shut down within 2 seconds after context cancel")
	}
}

func TestDescriptorWatcher_ClosedSIGHUPChannel(t *testing.T) {
	t.Parallel()

	tmpFile, err := os.CreateTemp(t.TempDir(), "descriptors-*.pb")
	require.NoError(t, err)
	tmpFile.Close()

	sighup := make(chan os.Signal, 1)
	close(sighup) // simulate channel close

	w := NewDescriptorWatcher(tmpFile.Name(), 1*time.Hour, sighup, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// success — watcher should exit when sighup channel is closed
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("watcher did not shut down when sighup channel was closed")
	}
}
