package evaluator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher monitors a single file for changes using fsnotify with
// polling fallback. When the file changes, it calls the onChange callback.
// If onChange returns an error the watcher retains the previous mtime so
// the next poll cycle retries.
type FileWatcher struct {
	path         string
	logger       *slog.Logger
	pollInterval time.Duration
	sighupCh     <-chan os.Signal
	onChange     func(ctx context.Context, trigger string) error

	mu      sync.Mutex
	lastMod time.Time
}

// NewFileWatcher creates a generic file watcher. onChange is called whenever
// the watched file is modified (via fsnotify, mtime poll, or SIGHUP).
func NewFileWatcher(
	path string,
	pollInterval time.Duration,
	sighupCh <-chan os.Signal,
	logger *slog.Logger,
	onChange func(ctx context.Context, trigger string) error,
) *FileWatcher {
	return &FileWatcher{
		path:         path,
		pollInterval: pollInterval,
		sighupCh:     sighupCh,
		logger:       logger,
		onChange:     onChange,
	}
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (w *FileWatcher) Run(ctx context.Context) {
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

	w.logger.Info("watching file", "path", w.path)

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
			w.logger.Info("SIGHUP received, reloading", "path", w.path)
			w.tryReload("SIGHUP")
		}
	}
}

func (w *FileWatcher) runPollOnly(ctx context.Context) {
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
			w.logger.Info("SIGHUP received, reloading", "path", w.path)
			w.tryReload("SIGHUP")
		}
	}
}

func (w *FileWatcher) tryReloadIfChanged() {
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

func (w *FileWatcher) tryReload(trigger string) {
	if err := w.onChange(context.Background(), trigger); err != nil {
		w.logger.Error("reload failed", "trigger", trigger, "path", w.path, "error", err)
		return
	}

	if info, err := os.Stat(w.path); err == nil {
		w.mu.Lock()
		w.lastMod = info.ModTime()
		w.mu.Unlock()
	}
}

// ── DescriptorWatcher ──────────────────────────────────────────────────

// SyncAndReloadFunc is called by the DescriptorWatcher in monolithic mode.
// It receives the parsed definitions and syncs them to the DB. If it returns
// an error, the sync is considered failed.
type SyncAndReloadFunc func(ctx context.Context, defs []FlagDef) error

// DescriptorWatcher monitors the descriptors file for changes and syncs
// definitions to the DB in standalone mode.
type DescriptorWatcher struct {
	fw              *FileWatcher
	path            string
	logger          *slog.Logger
	syncAndReloadFn SyncAndReloadFunc // If set, sync to DB on file change.
}

// NewDescriptorWatcher creates a descriptor file watcher.
func NewDescriptorWatcher(
	path string,
	pollInterval time.Duration,
	sighupCh <-chan os.Signal,
	logger *slog.Logger,
) *DescriptorWatcher {
	dw := &DescriptorWatcher{
		path:   path,
		logger: logger,
	}
	dw.fw = NewFileWatcher(path, pollInterval, sighupCh, logger, dw.handleChange)
	return dw
}

// SetSyncAndReload sets the sync callback for monolithic mode. When set,
// descriptor file changes trigger: parse → sync to DB.
func (dw *DescriptorWatcher) SetSyncAndReload(fn SyncAndReloadFunc) {
	dw.syncAndReloadFn = fn
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (dw *DescriptorWatcher) Run(ctx context.Context) {
	dw.fw.Run(ctx)
}

// tryReload parses the descriptor file and calls the sync callback.
// Exported for testing via the unexported name in the same package.
func (dw *DescriptorWatcher) tryReload(trigger string) {
	if err := dw.handleChange(context.Background(), trigger); err != nil {
		dw.logger.Error("descriptor reload failed, continuing with current state",
			"trigger", trigger, "error", err)
	}
}

func (dw *DescriptorWatcher) handleChange(_ context.Context, trigger string) error {
	defs, err := ParseDescriptorFile(dw.path)
	if err != nil {
		return fmt.Errorf("parse descriptors: %w", err)
	}

	if dw.syncAndReloadFn != nil {
		if err := dw.syncAndReloadFn(context.Background(), defs); err != nil {
			return fmt.Errorf("sync: %w", err)
		}
	}

	dw.logger.Info("descriptors reloaded",
		"trigger", trigger,
		"total_flags", len(defs))
	return nil
}
