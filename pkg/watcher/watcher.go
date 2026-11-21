package watcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/fsnotify/fsnotify"
)

// DefaultPollInterval is the default polling interval for fallback mode.
const DefaultPollInterval = 2 * time.Second

const defaultJSONLSettlePolls = 3

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

func defaultPollIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(env.PollInterval.Get())
	if raw == "" {
		return DefaultPollInterval
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return DefaultPollInterval
}

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
	watchPath        string
	walPath          string
	isSQLite         bool
	isJSONL          bool
	debounceDuration time.Duration
	pollInterval     time.Duration
	onChange         func()
	onError          func(error)
	forcePoll        bool
	forcePollEnv     bool
	fsType           FilesystemType

	fsWatcher           *fsnotify.Watcher
	debouncer           *Debouncer
	useFallback         bool
	lastExists          bool
	lastInfo            os.FileInfo
	lastMtime           time.Time
	lastSize            int64
	lastEndsWithNewline bool
	lastWALInfo         os.FileInfo

	unsettledJSONL    bool
	unsettledSince    time.Time
	unsettledPolls    int
	settleTimer       *time.Timer
	jsonlSettleWindow time.Duration
	jsonlSettlePolls  int

	// Optional unexported deterministic polling seams for zero-sleep tests.
	// Both are nil in production and incur zero overhead when unset.
	pollTicker func(d time.Duration) (<-chan time.Time, func())
	onPollPass func(runGeneration uint64)

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

func resolveWatchedPath(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil && resolved != "" {
		return resolved
	}
	cur := absPath
	for depth := 0; depth < 8; depth++ {
		linkTarget, err := os.Readlink(cur)
		if err != nil {
			break
		}
		if !filepath.IsAbs(linkTarget) {
			linkTarget = filepath.Join(filepath.Dir(cur), linkTarget)
		}
		cur = filepath.Clean(linkTarget)
		if resolved, err := filepath.EvalSymlinks(cur); err == nil && resolved != "" {
			return resolved
		}
	}
	if evalDir, err := filepath.EvalSymlinks(filepath.Dir(cur)); err == nil && evalDir != "" {
		return filepath.Join(evalDir, filepath.Base(cur))
	}
	return cur
}

func classifyWatchedExtensions(absPath, resolvedPath string) (isSQLite, isJSONL bool) {
	for _, candidate := range []string{absPath, resolvedPath} {
		switch strings.ToLower(filepath.Ext(candidate)) {
		case ".db", ".sqlite", ".sqlite3":
			return true, false
		case ".jsonl":
			return false, true
		}
	}
	return false, false
}

// watchedDirsAndLinks returns the directories fsnotify should watch (the resolved
// target's directory plus any distinct parent directories of symlink hops along
// absPath) and the set of cleaned symlink paths whose replacement can retarget
// the watched file.
func watchedDirsAndLinks(absPath, resolvedPath string) (dirs []string, linkEntries map[string]bool) {
	seenDirs := make(map[string]bool, 4)
	addDir := func(d string) {
		d = filepath.Clean(d)
		if d != "" && !seenDirs[d] {
			seenDirs[d] = true
			dirs = append(dirs, d)
		}
	}
	cleanResolved := filepath.Clean(resolvedPath)
	targetDir := filepath.Clean(filepath.Dir(cleanResolved))
	addDir(targetDir)

	cleanOrig := filepath.Clean(absPath)
	origDir := filepath.Clean(filepath.Dir(cleanOrig))
	linkEntries = make(map[string]bool, 4)
	if origDir != targetDir || cleanOrig != cleanResolved {
		addDir(origDir)
		linkEntries[cleanOrig] = true
	}

	cur := cleanOrig
	for depth := 0; depth < 16; depth++ {
		if evalCur, err := filepath.EvalSymlinks(cur); err == nil && filepath.Clean(evalCur) == cur {
			break
		}
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			linkEntries[cur] = true
			addDir(filepath.Dir(cur))
		}
		parent := filepath.Clean(filepath.Dir(cur))
		if parent == cur {
			break
		}
		cur = parent
	}
	return dirs, linkEntries
}

// NewWatcher creates a new file watcher for the given path.
func NewWatcher(path string, opts ...WatcherOption) (*Watcher, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	resolvedPath := resolveWatchedPath(absPath)
	isSQLite, isJSONL := classifyWatchedExtensions(absPath, resolvedPath)

	w := &Watcher{
		path:             absPath,
		watchPath:        resolvedPath,
		isSQLite:         isSQLite,
		isJSONL:          isJSONL,
		debounceDuration: DefaultDebounceDuration,
		pollInterval:     defaultPollIntervalFromEnv(),
		onChange:         func() {},
		onError:          func(error) {},
		changeCh:         make(chan struct{}, 1),
	}
	// Match the SQLite extensions supported by the source loader. Committed
	// changes can live only in this companion until a checkpoint updates path:
	// https://www.sqlite.org/wal.html
	if isSQLite {
		w.walPath = resolvedPath + "-wal"
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
	w.clearSettleLocked()

	if envBool("BV_FORCE_POLLING") || envBool("BV_FORCE_POLL") {
		w.forcePollEnv = true
	}

	// Re-resolve symlinks at Start in case the link or parent directory was
	// created after NewWatcher.
	w.watchPath = resolveWatchedPath(w.path)
	isSQLite, isJSONL := classifyWatchedExtensions(w.path, w.watchPath)
	w.isSQLite = isSQLite
	w.isJSONL = isJSONL
	if w.isSQLite {
		w.walPath = w.watchPath + "-wal"
	} else {
		w.walPath = ""
	}
	watchPath := w.watchPath
	walPath := w.walPath

	w.fsType = DetectFilesystemType(watchPath)
	if isRemoteFilesystem(w.fsType) {
		w.useFallback = true
	}

	forcePoll := w.forcePoll || w.forcePollEnv
	if forcePoll {
		w.useFallback = true
	}

	// Get initial file state
	info, endsWithNewline, err := statWatchedFile(watchPath, w.isJSONL)
	if err != nil {
		if os.IsPermission(err) {
			return ErrPermission
		}
		// File might not exist yet, that's okay
		w.lastExists = false
		w.lastInfo = nil
		w.lastMtime = time.Time{}
		w.lastSize = 0
		w.lastEndsWithNewline = false
	} else {
		w.lastExists = true
		w.lastInfo = info
		w.lastMtime = info.ModTime()
		w.lastSize = info.Size()
		w.lastEndsWithNewline = endsWithNewline
	}
	w.lastWALInfo = nil
	if walPath != "" {
		info, err := os.Stat(walPath)
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
			// Watch the directory containing the resolved target file (more reliable
			// for atomic writes and symlinked SQLite -wal sidecars), plus any symlink
			// parent directory so atomic symlink replacement is observed immediately.
			dirs, _ := watchedDirsAndLinks(w.path, watchPath)
			targetDir := filepath.Clean(filepath.Dir(watchPath))
			if err := fsw.Add(targetDir); err != nil {
				fsw.Close()
				w.useFallback = true
			} else {
				registered := map[string]bool{targetDir: true}
				for _, extraDir := range dirs {
					if registered[extraDir] {
						continue
					}
					if addErr := fsw.Add(extraDir); addErr == nil {
						registered[extraDir] = true
					}
				}
				w.fsWatcher = fsw
				w.useFallback = false
				go w.watchFsnotifyWithBackend(runCtx, runGeneration, fsw, registered, fsw.Events, fsw.Errors)
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

	w.clearSettleLocked()
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

// ResolvedPath returns the symlink-resolved target file path being monitored.
func (w *Watcher) ResolvedPath() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.watchPath != "" {
		return w.watchPath
	}
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
	v := strings.TrimSpace(env.Get(name))
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

func (w *Watcher) watchedPaths() (watchPath, walPath string) {
	w.mu.RLock()
	watchPath = w.watchPath
	walPath = w.walPath
	w.mu.RUnlock()
	if watchPath == "" {
		watchPath = w.path
	}
	return watchPath, walPath
}

func (w *Watcher) refreshResolvedTarget(runGeneration uint64) (retargeted, active bool) {
	newResolved := resolveWatchedPath(w.path)
	isSQLite, isJSONL := classifyWatchedExtensions(w.path, newResolved)
	newWALPath := ""
	var newWALInfo os.FileInfo
	if isSQLite {
		newWALPath = newResolved + "-wal"
		if info, err := os.Stat(newWALPath); err == nil {
			newWALInfo = info
		}
	}
	newFSType := DetectFilesystemType(newResolved)

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false, false
	}
	if w.watchPath == newResolved && w.walPath == newWALPath && w.isSQLite == isSQLite && w.isJSONL == isJSONL {
		return false, true
	}
	retargeted = w.watchPath != newResolved
	w.watchPath = newResolved
	w.walPath = newWALPath
	w.isSQLite = isSQLite
	w.isJSONL = isJSONL
	w.fsType = newFSType
	if retargeted {
		w.lastWALInfo = newWALInfo
	}
	return retargeted, true
}

func (w *Watcher) inspectTargetAfterSymlinkEvent(runGeneration uint64, forceChange bool) {
	watchPath, _ := w.watchedPaths()
	w.mu.RLock()
	isJSONL := w.isJSONL
	w.mu.RUnlock()

	info, endsWithNewline, err := statWatchedFile(watchPath, isJSONL)
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
		return
	}

	changed, withheld, active := w.observePathSample(runGeneration, info, endsWithNewline, false)
	if !active || withheld {
		return
	}
	if changed || forceChange {
		w.scheduleChange(runGeneration)
	}
}

// watchFsnotify monitors using fsnotify events.
func (w *Watcher) watchFsnotify(
	ctx context.Context,
	runGeneration uint64,
	events <-chan fsnotify.Event,
	errors <-chan error,
) {
	w.watchFsnotifyWithBackend(ctx, runGeneration, nil, nil, events, errors)
}

func (w *Watcher) watchFsnotifyWithBackend(
	ctx context.Context,
	runGeneration uint64,
	fsw *fsnotify.Watcher,
	registeredDirs map[string]bool,
	events <-chan fsnotify.Event,
	errors <-chan error,
) {
	watchPath, walPath := w.watchedPaths()
	cleanOrigPath := filepath.Clean(w.path)
	targetDir := filepath.Clean(filepath.Dir(watchPath))
	_, linkEntries := watchedDirsAndLinks(w.path, watchPath)
	if registeredDirs == nil {
		registeredDirs = map[string]bool{targetDir: true}
	}

	syncBackendDirs := func() {
		watchPath, walPath = w.watchedPaths()
		targetDir = filepath.Clean(filepath.Dir(watchPath))
		desiredDirs, nextLinks := watchedDirsAndLinks(w.path, watchPath)
		linkEntries = nextLinks
		if fsw == nil {
			return
		}
		desiredSet := make(map[string]bool, len(desiredDirs))
		for _, d := range desiredDirs {
			desiredSet[d] = true
			if !registeredDirs[d] {
				if err := fsw.Add(d); err == nil {
					registeredDirs[d] = true
				}
			}
		}
		for d := range registeredDirs {
			if !desiredSet[d] {
				_ = fsw.Remove(d)
				delete(registeredDirs, d)
			}
		}
	}

	// On macOS/BSD kqueue, atomically replacing a symlink via symlink + rename
	// can complete before fsnotify scans the directory, emitting zero events.
	// When watching a symlinked path against a real fsnotify backend, poll the
	// symlink target lightly so atomic retargets are never missed.
	var symlinkTicker *time.Ticker
	var symlinkTickC <-chan time.Time
	if fsw != nil && len(linkEntries) > 0 {
		symlinkTicker = time.NewTicker(100 * time.Millisecond)
		symlinkTickC = symlinkTicker.C
		defer symlinkTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-symlinkTickC:
			retargeted, active := w.refreshResolvedTarget(runGeneration)
			if !active {
				return
			}
			if retargeted {
				syncBackendDirs()
				w.inspectTargetAfterSymlinkEvent(runGeneration, true)
			}

		case event, ok := <-events:
			if !ok {
				w.fallbackToPolling(ctx, runGeneration, ErrWatchUnavailable)
				return
			}
			cleanEvent := filepath.Clean(event.Name)

			if linkEntries[cleanEvent] || (cleanEvent == cleanOrigPath && cleanOrigPath != filepath.Clean(watchPath)) {
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					retargeted, active := w.refreshResolvedTarget(runGeneration)
					if !active {
						return
					}
					syncBackendDirs()
					w.inspectTargetAfterSymlinkEvent(runGeneration, retargeted)
				}
				continue
			}

			// On macOS/BSD kqueue, atomically replacing an existing symlink via
			// rename(tmpLink, link) emits an event for tmpLink rather than link.
			// Re-check symlink resolution when an event arrives in a link parent dir.
			if len(linkEntries) > 0 && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				eventDir := filepath.Clean(filepath.Dir(cleanEvent))
				inLinkDir := false
				for linkPath := range linkEntries {
					linkDir := filepath.Clean(filepath.Dir(linkPath))
					if eventDir == linkDir {
						inLinkDir = true
						break
					}
					if resolvedDir, err := filepath.EvalSymlinks(linkDir); err == nil && filepath.Clean(resolvedDir) == eventDir {
						inLinkDir = true
						break
					}
				}
				if inLinkDir {
					retargeted, active := w.refreshResolvedTarget(runGeneration)
					if !active {
						return
					}
					if retargeted {
						syncBackendDirs()
						w.inspectTargetAfterSymlinkEvent(runGeneration, true)
						continue
					}
				}
			}

			// fsnotify removes a directory watch when that directory moves or
			// disappears, without necessarily closing the notification stream.
			// Poll the selected path so recreating the directory can recover.
			origDir := filepath.Clean(filepath.Dir(cleanOrigPath))
			if (cleanEvent == targetDir || cleanEvent == origDir) &&
				event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				w.fallbackToPolling(ctx, runGeneration, ErrWatchUnavailable)
				return
			}

			if walPath != "" && (cleanEvent == filepath.Clean(walPath) ||
				(filepath.Clean(filepath.Dir(cleanEvent)) == targetDir && filepath.Base(cleanEvent) == filepath.Base(walPath))) {
				// WAL removal/rename is a normal checkpoint lifecycle event, not
				// removal of the selected database. Ignore -shm: readers update it.
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					w.scheduleChange(runGeneration)
				}
				continue
			}
			// Only care about this source and its exact WAL companion.
			if cleanEvent != filepath.Clean(watchPath) && cleanEvent != cleanOrigPath {
				continue
			}
			if cleanEvent == cleanOrigPath {
				if retargeted, active := w.refreshResolvedTarget(runGeneration); !active {
					return
				} else if retargeted {
					syncBackendDirs()
					w.inspectTargetAfterSymlinkEvent(runGeneration, true)
					continue
				}
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
	// Include Chmod because macOS/BSD kqueue reports os.Truncate as NOTE_ATTRIB
	// (fsnotify.Chmod); observePathSample filters pure metadata-only chmods.
	if op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Chmod) == 0 {
		return
	}

	watchPath, _ := w.watchedPaths()
	w.mu.RLock()
	isJSONL := w.isJSONL
	w.mu.RUnlock()

	info, endsWithNewline, err := statWatchedFile(watchPath, isJSONL)
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

	_, withheld, active := w.observePathSample(runGeneration, info, endsWithNewline, false)
	if !active || withheld {
		return
	}
	w.scheduleChange(runGeneration)
}

// watchPolling monitors using periodic stat checks.
func (w *Watcher) watchPolling(ctx context.Context, runGeneration uint64) {
	w.mu.RLock()
	pollInterval := w.pollInterval
	tickerFactory := w.pollTicker
	onPollPass := w.onPollPass
	w.mu.RUnlock()

	var tickCh <-chan time.Time
	if tickerFactory != nil {
		var stopFn func()
		tickCh, stopFn = tickerFactory(pollInterval)
		if stopFn != nil {
			defer stopFn()
		}
	} else {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		tickCh = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-tickCh:
			w.pollOnce(runGeneration)
			if onPollPass != nil {
				onPollPass(runGeneration)
			}
		}
	}
}

func (w *Watcher) pollOnce(runGeneration uint64) {
	retargeted, active := w.refreshResolvedTarget(runGeneration)
	if !active {
		return
	}
	watchPath, _ := w.watchedPaths()
	w.mu.RLock()
	isJSONL := w.isJSONL
	w.mu.RUnlock()

	info, endsWithNewline, err := statWatchedFile(watchPath, isJSONL)
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
		return
	}

	changed, withheld, active := w.observePathSample(runGeneration, info, endsWithNewline, true)
	walChanged := w.pollWAL(runGeneration)

	if active && !withheld && (changed || retargeted || walChanged) {
		w.scheduleChange(runGeneration)
	} else if active && withheld && (retargeted || walChanged) {
		w.scheduleChange(runGeneration)
	}
}

func statWatchedFile(path string, checkTrailingNewline bool) (os.FileInfo, bool, error) {
	if !checkTrailingNewline {
		info, err := os.Stat(path)
		return info, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil, false, err
		}
		info, statErr := os.Stat(path)
		if statErr == nil {
			return info, false, nil
		}
		return nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	endsWithNewline := false
	if info.Size() > 0 && info.Mode().IsRegular() {
		var b [1]byte
		if n, rerr := f.ReadAt(b[:], info.Size()-1); rerr == nil && n == 1 && b[0] == '\n' {
			endsWithNewline = true
		}
	}
	return info, endsWithNewline, nil
}

// pollWAL includes creation, replacement and checkpoint removal in the change
// fingerprint while preserving the selected database path and run generation.
func (w *Watcher) pollWAL(runGeneration uint64) bool {
	_, walPath := w.watchedPaths()
	if walPath == "" {
		return false
	}
	info, err := os.Stat(walPath)
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
	w.clearSettleLocked()
	w.lastExists = false
	w.lastInfo = nil
	w.lastMtime = time.Time{}
	w.lastSize = 0
	w.lastEndsWithNewline = false
	return hadFile, true
}

func (w *Watcher) recordStat(runGeneration uint64, info os.FileInfo) (changed, active bool) {
	if info == nil {
		return false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false, false
	}

	mtime := info.ModTime()
	size := info.Size()
	changed = !w.lastExists ||
		!mtime.Equal(w.lastMtime) ||
		size != w.lastSize ||
		(w.lastInfo != nil && !os.SameFile(w.lastInfo, info))
	w.lastExists = true
	w.lastInfo = info
	w.lastMtime = mtime
	w.lastSize = size
	return changed, true
}

func (w *Watcher) observePathSample(runGeneration uint64, info os.FileInfo, endsWithNewline bool, fromPoll bool) (changed, withheld, active bool) {
	if info == nil {
		return false, false, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return false, false, false
	}

	isTransientJSONL := w.isJSONL &&
		w.lastExists &&
		w.lastSize > 0 &&
		(info.Size() == 0 || (w.lastEndsWithNewline && !endsWithNewline))

	if isTransientJSONL {
		if !w.unsettledJSONL {
			w.unsettledJSONL = true
			w.unsettledSince = time.Now()
			w.unsettledPolls = 0
			w.debouncer.Cancel()
		}
		if fromPoll {
			w.unsettledPolls++
		}

		settledFinal := false
		if w.pollTicker != nil {
			if fromPoll && w.unsettledPolls >= w.effectiveSettlePollsLocked() {
				settledFinal = true
			}
			if w.jsonlSettleWindow > 0 && time.Since(w.unsettledSince) >= w.jsonlSettleWindow {
				settledFinal = true
			}
		} else if time.Since(w.unsettledSince) >= w.effectiveSettleWindowLocked() {
			settledFinal = true
		}

		if !settledFinal {
			if w.pollTicker == nil {
				w.armSettleTimerLocked(runGeneration)
			}
			return false, true, true
		}

		w.clearSettleLocked()
		w.lastExists = true
		w.lastInfo = info
		w.lastMtime = info.ModTime()
		w.lastSize = info.Size()
		w.lastEndsWithNewline = endsWithNewline
		return true, false, true
	}

	wasUnsettled := w.unsettledJSONL
	w.clearSettleLocked()
	mtime := info.ModTime()
	size := info.Size()
	changed = wasUnsettled ||
		!w.lastExists ||
		!mtime.Equal(w.lastMtime) ||
		size != w.lastSize ||
		(w.lastInfo != nil && !os.SameFile(w.lastInfo, info)) ||
		(w.isJSONL && endsWithNewline != w.lastEndsWithNewline)
	w.lastExists = true
	w.lastInfo = info
	w.lastMtime = mtime
	w.lastSize = size
	w.lastEndsWithNewline = endsWithNewline
	return changed, false, true
}

func (w *Watcher) effectiveSettleWindowLocked() time.Duration {
	if w.jsonlSettleWindow > 0 {
		return w.jsonlSettleWindow
	}
	return min(max(2*w.debounceDuration, 25*time.Millisecond), 250*time.Millisecond)
}

func (w *Watcher) effectiveSettleRecheckIntervalLocked() time.Duration {
	window := w.effectiveSettleWindowLocked()
	return min(max(window/4, 5*time.Millisecond), 25*time.Millisecond)
}

func (w *Watcher) effectiveSettlePollsLocked() int {
	if w.jsonlSettlePolls > 0 {
		return w.jsonlSettlePolls
	}
	return defaultJSONLSettlePolls
}

func (w *Watcher) clearSettleLocked() {
	if w.settleTimer != nil {
		w.settleTimer.Stop()
		w.settleTimer = nil
	}
	w.unsettledJSONL = false
	w.unsettledSince = time.Time{}
	w.unsettledPolls = 0
}

func (w *Watcher) armSettleTimerLocked(runGeneration uint64) {
	if w.settleTimer != nil {
		w.settleTimer.Stop()
	}
	delay := w.effectiveSettleRecheckIntervalLocked()
	w.settleTimer = time.AfterFunc(delay, func() {
		w.recheckUnsettledJSONL(runGeneration)
	})
}

func (w *Watcher) recheckUnsettledJSONL(runGeneration uint64) {
	w.mu.RLock()
	active := w.runIsActiveLocked(runGeneration)
	isJSONL := w.isJSONL
	watchPath := w.watchPath
	w.mu.RUnlock()
	if !active || !isJSONL {
		return
	}
	if watchPath == "" {
		watchPath = w.path
	}

	info, endsWithNewline, err := statWatchedFile(watchPath, true)
	if err != nil {
		if os.IsNotExist(err) {
			hadFile, stillActive := w.recordMissing(runGeneration)
			if stillActive && hadFile {
				w.reportError(runGeneration, ErrFileRemoved)
			}
		} else if os.IsPermission(err) {
			w.reportError(runGeneration, ErrPermission)
		} else {
			w.reportError(runGeneration, err)
		}
		return
	}

	changed, withheld, stillActive := w.observePathSample(runGeneration, info, endsWithNewline, false)
	if stillActive && !withheld && changed {
		w.scheduleChange(runGeneration)
	}
}

func (w *Watcher) shouldDeferJSONLNotify(runGeneration uint64) bool {
	w.mu.RLock()
	isJSONL := w.isJSONL
	watchPath := w.watchPath
	w.mu.RUnlock()
	if !isJSONL {
		return false
	}
	if watchPath == "" {
		watchPath = w.path
	}
	info, endsWithNewline, err := statWatchedFile(watchPath, true)
	if err != nil || info == nil {
		return false
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.runIsActiveLocked(runGeneration) {
		return true
	}
	if w.lastExists && w.lastSize > 0 && (info.Size() == 0 || (w.lastEndsWithNewline && !endsWithNewline)) {
		if !w.unsettledJSONL {
			w.unsettledJSONL = true
			w.unsettledSince = time.Now()
			w.unsettledPolls = 0
		}
		if w.pollTicker == nil {
			w.armSettleTimerLocked(runGeneration)
		}
		return true
	}
	return false
}

// notifyChange invokes the onChange callback and signals the change channel.
func (w *Watcher) notifyChange(runGeneration uint64) {
	if w.shouldDeferJSONLNotify(runGeneration) {
		return
	}

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
