package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultPollInterval is the default polling interval for fallback mode.
const DefaultPollInterval = 2 * time.Second

// Common errors.
var (
	ErrFileRemoved      = errors.New("watched file was removed")
	ErrPermission       = errors.New("permission denied")
	ErrAlreadyStarted   = errors.New("watcher already started")
	ErrWatchUnavailable = errors.New("file notification watch lost; using polling")
)

var stoppedWatcherDone = func() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// WatcherOption configures a Watcher.
type WatcherOption func(*Watcher)

// WithDebounceDuration sets the debounce duration.
func WithDebounceDuration(d time.Duration) WatcherOption {
	return func(w *Watcher) {
		w.debounceDuration = d
	}
}

// WithPollInterval sets the polling interval for fallback mode.
func WithPollInterval(d time.Duration) WatcherOption {
	return func(w *Watcher) {
		if d <= 0 {
			d = DefaultPollInterval
		}
		w.pollInterval = d
	}
}

// WithOnChange sets the callback invoked when the file changes.
func WithOnChange(fn func()) WatcherOption {
	return func(w *Watcher) {
		w.onChange = fn
	}
}

// WithOnError sets the callback invoked on errors.
func WithOnError(fn func(error)) WatcherOption {
	return func(w *Watcher) {
		w.onError = fn
	}
}

// WithForcePoll forces polling mode even if fsnotify is available.
func WithForcePoll(force bool) WatcherOption {
	return func(w *Watcher) {
		w.forcePoll = force
	}
}

// Watcher monitors a file for changes using fsnotify with polling fallback.
type Watcher struct {
	path             string
	walPath          string
	debounceDuration time.Duration
	pollInterval     time.Duration
	onChange         func()
	onError          func(error)
	forcePoll        bool
	forcePollEnv     bool
	fsType           FilesystemType

	fsWatcher   *fsnotify.Watcher
	debouncer   *Debouncer
	useFallback bool
	lastExists  bool
	lastMtime   time.Time
	lastSize    int64
	lastWALInfo os.FileInfo

	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	// SAFETY: every producer captures runGeneration at Start. State mutation and
	// change-channel publication must re-check that generation while holding mu,
	// so work from a stopped run cannot cross a later Stop/Start boundary. Each
	// fsnotify loop also receives its run's channels directly; it must never read
	// w.fsWatcher after launch and accidentally attach to a replacement watcher.
	runGeneration uint64
	mu            sync.RWMutex
	changeCh      chan struct{}
}

// NewWatcher creates a new file watcher for the given path.
func NewWatcher(path string, opts ...WatcherOption) (*Watcher, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		path:             absPath,
		debounceDuration: DefaultDebounceDuration,
		pollInterval:     DefaultPollInterval,
		onChange:         func() {},
		onError:          func(error) {},
		changeCh:         make(chan struct{}, 1),
	}
	// Match the SQLite extensions supported by the source loader. Committed
	// changes can live only in this companion until a checkpoint updates path:
	// https://www.sqlite.org/wal.html
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".db", ".sqlite", ".sqlite3":
		w.walPath = absPath + "-wal"
	}

	for _, opt := range opts {
		opt(w)
	}

	w.debouncer = NewDebouncer(w.debounceDuration)

	return w, nil
}

// Start begins watching the file for changes.
func (w *Watcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return ErrAlreadyStarted
	}

	// Reset per-start state.
	w.useFallback = false
	w.forcePollEnv = false
	w.fsType = FSTypeUnknown

	if envBool("BV_FORCE_POLLING") || envBool("BV_FORCE_POLL") {
		w.forcePollEnv = true
	}

	w.fsType = DetectFilesystemType(w.path)
	if isRemoteFilesystem(w.fsType) {
		w.useFallback = true
	}

	forcePoll := w.forcePoll || w.forcePollEnv
	if forcePoll {
		w.useFallback = true
	}

	// Get initial file state
	info, err := os.Stat(w.path)
	if err != nil {
		if os.IsPermission(err) {
			return ErrPermission
		}
		// File might not exist yet, that's okay
		w.lastExists = false
		w.lastMtime = time.Time{}
		w.lastSize = 0
	} else {
		w.lastExists = true
		w.lastMtime = info.ModTime()
		w.lastSize = info.Size()
	}
	w.lastWALInfo = nil
	if w.walPath != "" {
		info, err := os.Stat(w.walPath)
		if err != nil && !os.IsNotExist(err) {
			if os.IsPermission(err) {
				return ErrPermission
			}
			return err
		}
		w.lastWALInfo = info
	}

	w.ctx, w.cancel = newRunContext()
	runCtx := w.ctx
	w.runGeneration++
	runGeneration := w.runGeneration

	// Try to use fsnotify
	if !forcePoll && !w.useFallback {
		fsw, err := fsnotify.NewWatcher()
		if err == nil {
			// Watch the directory containing the file (more reliable for atomic writes)
			dir := filepath.Dir(w.path)
			if err := fsw.Add(dir); err != nil {
				fsw.Close()
				w.useFallback = true
			} else {
				w.fsWatcher = fsw
				w.useFallback = false
				go w.watchFsnotify(runCtx, runGeneration, fsw.Events, fsw.Errors)
			}
		} else {
			w.useFallback = true
		}
	} else {
		w.useFallback = true
	}

	// Start polling as fallback or primary
	if w.useFallback {
		go w.watchPolling(runCtx, runGeneration)
	}

	w.started = true
	return nil
}

// Stop stops watching the file.
// Note: The changeCh channel is intentionally NOT closed here. Closing it would
// cause race conditions with notifyChange() and break WatchFileCmd (which would
// receive immediately and potentially loop). Callers that wait on Changed()
// should also have their own shutdown signal.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return
	}
	w.started = false

	if w.cancel != nil {
		w.cancel()
	}

	if w.fsWatcher != nil {
		w.fsWatcher.Close()
		w.fsWatcher = nil
	}

	w.debouncer.Cancel()

	// An event published before Stop acquired mu may still be buffered even
	// though the old consumer has already exited. Drain it before a later Start
	// installs another run that consumes the same coalescing channel.
	for {
		select {
		case <-w.changeCh:
		default:
			return
		}
	}
}

// IsPolling returns true if the watcher is using polling mode.
func (w *Watcher) IsPolling() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.useFallback
}

// IsStarted returns true if the watcher is running.
func (w *Watcher) IsStarted() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.started
}

// Changed returns a channel that receives when the file changes.
// This is an alternative to using the OnChange callback.
func (w *Watcher) Changed() <-chan struct{} {
	return w.changeCh
}

// Done is closed when the current watcher run stops. A watcher that has not
// started (or has already stopped) returns an already-closed channel. Callers
// waiting on Changed should also select on Done so Stop cannot strand them.
func (w *Watcher) Done() <-chan struct{} {
	w.mu.RLock()
	ctx := w.ctx
	started := w.started
	w.mu.RUnlock()

	if !started || ctx == nil {
		return stoppedWatcherDone
	}
	return ctx.Done()
}

// Path returns the watched file path.
func (w *Watcher) Path() string {
	return w.path
}

// FilesystemType returns the best-effort filesystem classification for the watched path.
func (w *Watcher) FilesystemType() FilesystemType {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.fsType
}

// PollInterval returns the polling interval used when polling mode is active.
func (w *Watcher) PollInterval() time.Duration {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.pollInterval
}

func envBool(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// newRunContext returns a context whose cancel function is owned by Watcher.Stop.
func newRunContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// watchFsnotify monitors using fsnotify events.
func (w *Watcher) watchFsnotify(
	ctx context.Context,
	runGeneration uint64,
	events <-chan fsnotify.Event,
	errors <-chan error,
) {
	targetFile := filepath.Base(w.path)

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-events:
			if !ok {
				w.fallbackToPolling(ctx, runGeneration, ErrWatchUnavailable)
				return
			}
			// fsnotify removes a directory watch when that directory moves or
			// disappears, without necessarily closing the notification stream.
			// Poll the selected path so recreating the directory can recover.
			if filepath.Clean(event.Name) == filepath.Dir(w.path) &&
				event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.fallbackToPolling(ctx, runGeneration, ErrWatchUnavailable)
				return
			}

			eventFile := filepath.Base(event.Name)
			if w.walPath != "" && eventFile == targetFile+"-wal" {
				// WAL removal/rename is a normal checkpoint lifecycle event, not
				// removal of the selected database. Ignore -shm: readers update it.
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					w.scheduleChange(runGeneration)
				}
				continue
			}
			// Only care about this source and its exact WAL companion.
			if eventFile != targetFile {
				continue
			}

			w.handleFsnotifyFileEvent(runGeneration, event.Op)

		case err, ok := <-errors:
			if !ok || err == nil {
				err = ErrWatchUnavailable
			}
			// An overflow means events have already been lost. Other backend
			// errors likewise cannot promise continued delivery. Reporting an
			// error alone leaves a live TUI stale until another event arrives.
			w.fallbackToPolling(ctx, runGeneration, err)
			return
		}
	}
}

// fallbackToPolling hands one active run from a failed notification backend to
// polling. It neither stops the logical watcher nor replaces its Changed/Done
// channels, so the TUI's existing consumer keeps receiving future updates.
func (w *Watcher) fallbackToPolling(ctx context.Context, runGeneration uint64, cause error) {
	w.mu.Lock()
	if !w.runIsActiveLocked(runGeneration) || w.useFallback {
		w.mu.Unlock()
		return
	}
	w.useFallback = true
	failed := w.fsWatcher
	w.fsWatcher = nil
	// Keep the existing fingerprints: re-baselining here could swallow a
	// commit made between the lost event and the first poll. Only one caller
	// can start this poller, and Stop cancels this same run's context.
	go w.watchPolling(ctx, runGeneration)
	w.mu.Unlock()

	// Lost notifications demand a rescan even if mtime/size did not advance.
	// Queue it and start polling BEFORE caller-controlled error reporting.
	w.scheduleChange(runGeneration)
	if failed != nil {
		failed.Close()
	}
	// Close only the detached backend, never one installed by a later Start.
	// No external callback runs under mu; OnError may call Stop or Start.
	w.reportError(runGeneration, cause)
}

func (w *Watcher) handleFsnotifyFileEvent(runGeneration uint64, op fsnotify.Op) {
	if op&fsnotify.Remove != 0 {
		hadFile, active := w.recordMissing(runGeneration)
		if active && hadFile {
			w.reportError(runGeneration, ErrFileRemoved)
		}
		return
	}
	if op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}

	info, err := os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			if op&fsnotify.Rename != 0 {
				hadFile, active := w.recordMissing(runGeneration)
				if active && hadFile {
					w.reportError(runGeneration, ErrFileRemoved)
				}
			}
		} else if os.IsPermission(err) {
			w.reportError(runGeneration, ErrPermission)
		} else {
			w.reportError(runGeneration, err)
		}
		return
	}

	if _, active := w.recordStat(runGeneration, info.ModTime(), info.Size()); !active {
		return
	}
	w.scheduleChange(runGeneration)
}

// watchPolling monitors using periodic stat checks.
func (w *Watcher) watchPolling(ctx context.Context, runGeneration uint64) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			info, err := os.Stat(w.path)
			if err != nil {
				if os.IsNotExist(err) {
					hadFile, active := w.recordMissing(runGeneration)
					if active && hadFile {
						w.reportError(runGeneration, ErrFileRemoved)
					}
				} else if os.IsPermission(err) {
					w.reportError(runGeneration, ErrPermission)
				} else {
					w.reportError(runGeneration, err)
				}
				continue
			}

			changed, active := w.recordStat(runGeneration, info.ModTime(), info.Size())
			walChanged := w.pollWAL(runGeneration)

			if active && (changed || walChanged) {
				w.scheduleChange(runGeneration)
			}
		}
	}
}

// pollWAL includes creation, replacement and checkpoint removal in the change
// fingerprint while preserving the selected database path and run generation.
func (w *Watcher) pollWAL(runGeneration uint64) bool {
	if w.walPath == "" {
		return false
	}
	info, err := os.Stat(w.walPath)
	if err != nil && !os.IsNotExist(err) {
		if os.IsPermission(err) {
			w.reportError(runGeneration, ErrPermission)
		} else {
			w.reportError(runGeneration, err)
		}
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false
	}
	previous := w.lastWALInfo
	changed := (previous == nil) != (info == nil)
	if previous != nil && info != nil {
		changed = !previous.ModTime().Equal(info.ModTime()) || previous.Size() != info.Size() || !os.SameFile(previous, info)
	}
	w.lastWALInfo = info
	return changed
}

// scheduleChange serializes access to the shared debouncer with Stop and
// Start. Without this fence, a producer delayed after recordStat could resume
// in a later run and cancel that run's legitimate debounce timer.
func (w *Watcher) scheduleChange(runGeneration uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return
	}
	w.debouncer.Trigger(func() {
		w.notifyChange(runGeneration)
	})
}

func (w *Watcher) recordMissing(runGeneration uint64) (hadFile, active bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false, false
	}

	hadFile = w.lastExists
	w.lastExists = false
	w.lastMtime = time.Time{}
	w.lastSize = 0
	return hadFile, true
}

func (w *Watcher) recordStat(runGeneration uint64, mtime time.Time, size int64) (changed, active bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false, false
	}

	changed = !w.lastExists || !mtime.Equal(w.lastMtime) || size != w.lastSize
	w.lastExists = true
	w.lastMtime = mtime
	w.lastSize = size
	return changed, true
}

// notifyChange invokes the onChange callback and signals the change channel.
func (w *Watcher) notifyChange(runGeneration uint64) {
	w.mu.RLock()
	active := w.runIsActiveLocked(runGeneration)
	onChange := w.onChange
	w.mu.RUnlock()

	if !active {
		return
	}

	// User callbacks must run without mu: they may call Stop or other Watcher
	// methods. Such a callback may overlap a concurrent Stop once admitted, but
	// its generation is checked again before any internal publication.
	onChange()

	// Check and publish atomically with respect to Stop/Start. Stop holds the
	// same mutex while invalidating the run and draining any queued old event.
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return
	}
	select {
	case w.changeCh <- struct{}{}:
	default:
	}
}

func (w *Watcher) reportError(runGeneration uint64, err error) {
	w.mu.RLock()
	active := w.runIsActiveLocked(runGeneration)
	onError := w.onError
	w.mu.RUnlock()
	if active {
		onError(err)
	}
}

func (w *Watcher) runIsActiveLocked(runGeneration uint64) bool {
	return w.started && w.runGeneration == runGeneration
}
