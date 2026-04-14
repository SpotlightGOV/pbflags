package evaluator

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeKillFetcher implements KillFetcher for testing.
type fakeKillFetcher struct {
	ks      *KillSet
	err     error
	calls   int
	callsCh chan struct{} // signaled on each call
}

func (f *fakeKillFetcher) GetKilledFlags(_ context.Context) (*KillSet, error) {
	f.calls++
	if f.callsCh != nil {
		select {
		case f.callsCh <- struct{}{}:
		default:
		}
	}
	return f.ks, f.err
}

func TestKillPoller_InitialPoll(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	tracker := NewHealthTracker(NewNoopMetrics())
	fetcher := &fakeKillFetcher{
		ks: &KillSet{FlagIDs: map[string]struct{}{"flag/1": {}}},
	}

	poller := NewKillPoller(fetcher, cache, tracker, 10*time.Second, 5*time.Second, slog.Default(), NewNoopMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	// Give the initial poll time to execute.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	require.GreaterOrEqual(t, fetcher.calls, 1, "poller should call fetcher at least once on start")
	require.True(t, cache.GetKillSet().IsKilled("flag/1"), "cache should contain killed flag after initial poll")
}

func TestKillPoller_PreservesLastKnownOnFailure(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	tracker := NewHealthTracker(NewNoopMetrics())
	callsCh := make(chan struct{}, 10)

	fetcher := &fakeKillFetcher{
		ks:      &KillSet{FlagIDs: map[string]struct{}{"flag/1": {}}},
		callsCh: callsCh,
	}

	poller := NewKillPoller(fetcher, cache, tracker, 20*time.Millisecond, 5*time.Second, slog.Default(), NewNoopMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	// Wait for initial successful poll.
	<-callsCh
	time.Sleep(10 * time.Millisecond) // let cache update complete

	// Now make fetcher fail.
	fetcher.err = errors.New("connection refused")
	fetcher.ks = nil

	// Wait for at least one failed poll.
	<-callsCh
	time.Sleep(10 * time.Millisecond)

	cancel()
	<-done

	// Kill set should still contain the previously fetched data.
	require.True(t, cache.GetKillSet().IsKilled("flag/1"), "kill set should be preserved after fetch failure")
}

func TestKillPoller_CancelsGracefully(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	tracker := NewHealthTracker(NewNoopMetrics())
	fetcher := &fakeKillFetcher{
		ks: &KillSet{FlagIDs: map[string]struct{}{}},
	}

	poller := NewKillPoller(fetcher, cache, tracker, 1*time.Hour, 5*time.Second, slog.Default(), NewNoopMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	// Cancel immediately after the initial poll completes.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not shut down within 2 seconds after context cancel")
	}
}

func TestKillPoller_UpdatesCacheOnSuccess(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	tracker := NewHealthTracker(NewNoopMetrics())
	callsCh := make(chan struct{}, 10)

	fetcher := &fakeKillFetcher{
		ks:      &KillSet{FlagIDs: map[string]struct{}{"flag/1": {}}},
		callsCh: callsCh,
	}

	poller := NewKillPoller(fetcher, cache, tracker, 20*time.Millisecond, 5*time.Second, slog.Default(), NewNoopMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.Run(ctx)
		close(done)
	}()

	// Wait for first poll.
	<-callsCh
	time.Sleep(10 * time.Millisecond)
	require.True(t, cache.GetKillSet().IsKilled("flag/1"))

	// Update kill set.
	fetcher.ks = &KillSet{FlagIDs: map[string]struct{}{"flag/2": {}}}

	// Wait for next poll.
	<-callsCh
	time.Sleep(10 * time.Millisecond)

	cancel()
	<-done

	require.False(t, cache.GetKillSet().IsKilled("flag/1"), "flag/1 should no longer be killed")
	require.True(t, cache.GetKillSet().IsKilled("flag/2"), "flag/2 should now be killed")
}
