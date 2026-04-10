package evaluator

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SyncAndReloadFunc is called by the DescriptorWatcher in monolithic mode.
// It receives the parsed definitions, syncs them to the DB, then loads
// definitions back from DB and returns them. If it returns an error, the
// registry swap is skipped.
type SyncAndReloadFunc func(ctx context.Context, defs []FlagDef) ([]FlagDef, error)

// DescriptorWatcher monitors the descriptors file for changes and atomically
// swaps the defaults registry when a valid new descriptor set is parsed.
type DescriptorWatcher struct {
	path            string
	registry        *Registry
	logger          *slog.Logger
	pollInterval    time.Duration
	sighupCh        <-chan os.Signal
	syncAndReloadFn SyncAndReloadFunc // If set, sync→reload from DB before swap.

	mu      sync.Mutex
	lastMod time.Time
}

// NewDescriptorWatcher creates a descriptor file watcher.
func NewDescriptorWatcher(
	path string,
	reg *Registry,
	pollInterval time.Duration,
	sighupCh <-chan os.Signal,
	logger *slog.Logger,
) *DescriptorWatcher {
	return &DescriptorWatcher{
		path:         path,
		registry:     reg,
		pollInterval: pollInterval,
		sighupCh:     sighupCh,
		logger:       logger,
	}
}

// SetSyncAndReload sets the sync callback for monolithic mode. When set,
// descriptor file changes trigger: parse → sync to DB → reload from DB → swap.
// If sync fails, the current registry is preserved.
func (w *DescriptorWatcher) SetSyncAndReload(fn SyncAndReloadFunc) {
	w.syncAndReloadFn = fn
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (w *DescriptorWatcher) Run(ctx context.Context) {
	if info, err := os.Stat(w.path); err == nil {
		w.mu.Lock()
		w.lastMod = info.ModTime()
		w.mu.Unlock()
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		w.logger.Warn("fsnotify unavailable, using poll-only mode", "error", err)
		w.runPollOnly(ctx)
		return
	}
	defer fsw.Close()

	if err := fsw.Add(w.path); err != nil {
		w.logger.Warn("fsnotify watch failed, using poll-only mode", "path", w.path, "error", err)
		w.runPollOnly(ctx)
		return
	}

	w.logger.Info("watching descriptors file", "path", w.path)

	var pollCh <-chan time.Time
	var pollTicker *time.Ticker
	if w.pollInterval > 0 {
		pollTicker = time.NewTicker(w.pollInterval)
		pollCh = pollTicker.C
		defer pollTicker.Stop()
	}

	const debounce = 500 * time.Millisecond
	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return
		case event, ok := <-fsw.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.NewTimer(debounce)
				debounceCh = debounceTimer.C
			}
		case <-debounceCh:
			debounceCh = nil
			debounceTimer = nil
			w.tryReload("fsnotify")
		case err, ok := <-fsw.Errors:
			if !ok {
				return
			}
			w.logger.Warn("fsnotify error", "error", err)
		case <-pollCh:
			w.tryReloadIfChanged()
		case _, ok := <-w.sighupCh:
			if !ok {
				return
			}
			w.logger.Info("SIGHUP received, reloading descriptors")
			w.tryReload("SIGHUP")
		}
	}
}

func (w *DescriptorWatcher) runPollOnly(ctx context.Context) {
	interval := w.pollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tryReloadIfChanged()
		case _, ok := <-w.sighupCh:
			if !ok {
				return
			}
			w.logger.Info("SIGHUP received, reloading descriptors")
			w.tryReload("SIGHUP")
		}
	}
}

func (w *DescriptorWatcher) tryReloadIfChanged() {
	info, err := os.Stat(w.path)
	if err != nil {
		return
	}

	w.mu.Lock()
	changed := info.ModTime().After(w.lastMod)
	w.mu.Unlock()

	if changed {
		w.tryReload("mtime poll")
	}
}

func (w *DescriptorWatcher) tryReload(trigger string) {
	defs, err := ParseDescriptorFile(w.path)
	if err != nil {
		w.logger.Error("descriptor reload failed, continuing with current state",
			"trigger", trigger, "error", err)
		return
	}

	// In monolithic mode: sync to DB, then reload from DB.
	if w.syncAndReloadFn != nil {
		defs, err = w.syncAndReloadFn(context.Background(), defs)
		if err != nil {
			w.logger.Error("sync failed, keeping current registry",
				"trigger", trigger, "error", err)
			return
		}
	}

	old := w.registry.Load()
	next := NewDefaults(defs)
	w.registry.Swap(next)

	if info, err := os.Stat(w.path); err == nil {
		w.mu.Lock()
		w.lastMod = info.ModTime()
		w.mu.Unlock()
	}

	added, removed := diffFlagIDs(old, next)
	w.logger.Info("descriptors reloaded",
		"trigger", trigger,
		"total_flags", next.Len(),
		"added", len(added),
		"removed", len(removed))
}

func diffFlagIDs(old, next *Defaults) (added, removed []string) {
	oldSet := make(map[string]struct{})
	for _, id := range old.FlagIDs() {
		oldSet[id] = struct{}{}
	}
	newSet := make(map[string]struct{})
	for _, id := range next.FlagIDs() {
		newSet[id] = struct{}{}
	}
	for id := range newSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, id)
		}
	}
	for id := range oldSet {
		if _, ok := newSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	return
}
