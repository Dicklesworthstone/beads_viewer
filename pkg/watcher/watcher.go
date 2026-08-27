package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultPollInterval is the default polling interval for fallback mode.
const DefaultPollInterval = 2 * time.Second

// Common errors.
var (
	ErrFileRemoved    = errors.New("watched file was removed")
	ErrPermission     = errors.New("permission denied")
	ErrAlreadyStarted = errors.New("watcher already started")
)

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

// WithSiblingFiles adds file names in the watched file's directory to the
// same change stream. Names containing a directory component are ignored: a
// Watcher deliberately owns one directory watch, and callers must opt in to
// every sibling that can affect their source selection.
func WithSiblingFiles(names ...string) WatcherOption {
	return func(w *Watcher) {
		w.siblingFiles = append(w.siblingFiles, names...)
	}
}

// WithAdditionalFiles adds exact paths, including files in other directories,
// to the same change stream. This is used when one logical data source can move
// between a repository's local tracker and Git worktree tracker exports.
func WithAdditionalFiles(paths ...string) WatcherOption {
	return func(w *Watcher) {
		w.additionalFiles = append(w.additionalFiles, paths...)
	}
}

// WithDirectoryChanges adds non-recursive directory watches. Any immediate
// child creation, removal, rename, or write joins the same debounced change
// stream. It is intended for source sets whose admissible filenames are not
// known until discovery runs again.
func WithDirectoryChanges(paths ...string) WatcherOption {
	return func(w *Watcher) {
		w.directoryPaths = append(w.directoryPaths, paths...)
	}
}

// WithRecursiveDirectoryChanges is the recursive counterpart to
// WithDirectoryChanges. Existing subdirectories are armed with fsnotify, new
// subdirectories are added as they appear, and polling fallback snapshots the
// complete tree.
func WithRecursiveDirectoryChanges(paths ...string) WatcherOption {
	return func(w *Watcher) {
		w.recursiveDirectoryPaths = append(w.recursiveDirectoryPaths, paths...)
	}
}

// WithDirectoryPatterns watches matching immediate children of a directory.
// Patterns use slash-separated path.Match syntax (for example, "*.jsonl").
func WithDirectoryPatterns(directory string, patterns ...string) WatcherOption {
	return func(w *Watcher) {
		w.patternDirectoryWatches = append(w.patternDirectoryWatches, directoryWatch{
			path:     directory,
			patterns: append([]string(nil), patterns...),
		})
	}
}

// WithRecursiveDirectoryPatterns watches matching descendants of a directory.
// Patterns are matched against slash-separated paths relative to the root (for
// example, "*/issues.jsonl").
func WithRecursiveDirectoryPatterns(directory string, patterns ...string) WatcherOption {
	return func(w *Watcher) {
		w.patternDirectoryWatches = append(w.patternDirectoryWatches, directoryWatch{
			path:      directory,
			recursive: true,
			patterns:  append([]string(nil), patterns...),
		})
	}
}

type fileState struct {
	exists      bool
	mtime       time.Time
	size        int64
	info        os.FileInfo
	changeSec   int64
	changeNsec  int64
	hasChangeAt bool
}

type directoryWatch struct {
	path      string
	recursive bool
	patterns  []string
}

// Watcher monitors a primary file and optional siblings using fsnotify with
// polling fallback.
type Watcher struct {
	path                    string
	debounceDuration        time.Duration
	pollInterval            time.Duration
	onChange                func()
	onError                 func(error)
	forcePoll               bool
	forcePollEnv            bool
	fsType                  FilesystemType
	siblingFiles            []string
	additionalFiles         []string
	directoryPaths          []string
	recursiveDirectoryPaths []string
	patternDirectoryWatches []directoryWatch
	watchedPaths            []string
	watchedPathSet          map[string]struct{}
	directoryWatches        []directoryWatch

	fsWatcher       *fsnotify.Watcher
	debouncer       *Debouncer
	useFallback     bool
	fileStates      map[string]fileState
	directoryStates map[string]fileState

	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	mu       sync.RWMutex
	changeCh chan struct{}
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

	for _, opt := range opts {
		opt(w)
	}
	w.configureWatchedPaths()

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
	w.debouncer = NewDebouncer(w.debounceDuration)
	w.useFallback = false
	w.forcePollEnv = false
	w.fsType = FSTypeUnknown

	if envBool("BV_FORCE_POLLING") || envBool("BV_FORCE_POLL") {
		w.forcePollEnv = true
	}

	w.fsType = DetectFilesystemType(w.path)
	filesystemPaths := append([]string(nil), w.watchedPaths...)
	for _, watch := range w.directoryWatches {
		filesystemPaths = append(filesystemPaths, watch.path)
	}
	for _, path := range filesystemPaths {
		pathType := DetectFilesystemType(path)
		if isRemoteFilesystem(pathType) {
			w.fsType = pathType
			w.useFallback = true
			break
		}
	}

	forcePoll := w.forcePoll || w.forcePollEnv
	if forcePoll {
		w.useFallback = true
	}

	// Get initial state for every opted-in source candidate. Missing candidates
	// are expected: watching the directory lets us observe their later creation.
	w.fileStates = make(map[string]fileState, len(w.watchedPaths))
	for _, path := range w.watchedPaths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsPermission(err) {
				return ErrPermission
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat watched file %s: %w", path, err)
			}
			w.fileStates[path] = fileState{}
			continue
		}
		w.fileStates[path] = fileStateFromInfo(info)
	}
	directoryStates, err := snapshotDirectoryStatesFor(w.directoryWatches)
	if err != nil {
		if os.IsPermission(err) {
			return ErrPermission
		}
		return fmt.Errorf("snapshot watched directories: %w", err)
	}
	w.directoryStates = directoryStates

	w.ctx, w.cancel = newRunContext()
	runCtx := w.ctx
	changedDuringStart := false
	var startFsnotify *fsnotify.Watcher

	// Try to use fsnotify
	if !forcePoll && !w.useFallback {
		fsw, err := fsnotify.NewWatcher()
		if err == nil {
			// Watch every directory containing an opted-in file. Directory watches
			// remain reliable across atomic replacement of the individual files.
			watchDirs := make(map[string]struct{})
			for _, path := range w.watchedPaths {
				watchDirs[filepath.Dir(path)] = struct{}{}
			}
			for _, watch := range w.directoryWatches {
				dirs, collectErr := collectWatchDirectories(watch)
				if collectErr != nil {
					if os.IsNotExist(collectErr) {
						w.useFallback = true
						break
					}
					fsw.Close()
					return fmt.Errorf("prepare directory watch %s: %w", watch.path, collectErr)
				}
				for _, dir := range dirs {
					watchDirs[dir] = struct{}{}
				}
			}
			var addErr error
			if !w.useFallback {
				for dir := range watchDirs {
					if err := fsw.Add(dir); err != nil {
						addErr = err
						break
					}
				}
			}
			if addErr != nil || w.useFallback {
				fsw.Close()
				w.useFallback = true
			} else {
				w.fsWatcher = fsw
				w.useFallback = false
				// Close the stat-before-watch race: once the directory watch is armed,
				// rescan every candidate. Later changes are queued by fsnotify; earlier
				// ones are reflected here and produce one debounced refresh below.
				var refreshErr error
				changedDuringStart, refreshErr = w.refreshWatchedStatesLocked()
				if refreshErr != nil {
					fsw.Close()
					w.fsWatcher = nil
					return fmt.Errorf("refresh watched sources: %w", refreshErr)
				}
				startFsnotify = fsw
			}
		} else {
			w.useFallback = true
		}
	} else {
		w.useFallback = true
	}

	w.started = true
	runDebouncer := w.debouncer
	// Start polling as fallback or primary only after publishing the run as
	// started. Otherwise a very short poll interval or an immediately queued
	// fsnotify event can observe started=false and terminate the new run.
	if w.useFallback {
		go w.watchPolling(runCtx, runDebouncer)
	} else if startFsnotify != nil {
		go w.watchFsnotify(runCtx, startFsnotify, runDebouncer)
	}

	if changedDuringStart {
		w.scheduleChange(runCtx, runDebouncer)
	}
	return nil
}

// Stop stops watching the file.
// Note: The changeCh channel is intentionally NOT closed here. Closing it would
// cause race conditions with notifyChange() and break WatchFileCmd (which would
// receive immediately and potentially loop). Callers that wait on Changed()
// should also have their own shutdown signal.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}

	cancel := w.cancel
	fsw := w.fsWatcher
	debouncer := w.debouncer
	w.ctx = nil
	w.cancel = nil
	w.fsWatcher = nil
	w.useFallback = false
	w.started = false
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if fsw != nil {
		_ = fsw.Close()
	}
	debouncer.Cancel()
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

// AddAdditionalFiles extends a live watcher with exact paths. Existing paths
// remain armed; adding a duplicate is a no-op.
func (w *Watcher) AddAdditionalFiles(paths ...string) error {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve additional watch path %s: %w", path, err)
		}
		absolutePath = filepath.Clean(absolutePath)
		w.mu.RLock()
		_, exists := w.watchedPathSet[absolutePath]
		w.mu.RUnlock()
		if exists {
			continue
		}
		pathType := DetectFilesystemType(absolutePath)
		remotePath := isRemoteFilesystem(pathType)
		state := fileState{}
		if info, statErr := os.Stat(absolutePath); statErr == nil {
			state = fileStateFromInfo(info)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat additional watch path %s: %w", absolutePath, statErr)
		}

		w.mu.Lock()
		if _, exists := w.watchedPathSet[absolutePath]; exists {
			w.mu.Unlock()
			continue
		}
		w.watchedPathSet[absolutePath] = struct{}{}
		w.watchedPaths = append(w.watchedPaths, absolutePath)
		w.fileStates[absolutePath] = state
		fsw := w.fsWatcher
		ctx := w.ctx
		debouncer := w.debouncer
		started := w.started
		if remotePath {
			w.fsType = pathType
		}
		w.mu.Unlock()

		if started && remotePath && ctx != nil {
			w.startPollingFallback(ctx)
			continue
		}
		if started && fsw != nil {
			if addErr := fsw.Add(filepath.Dir(absolutePath)); addErr != nil {
				w.onError(fmt.Errorf("add file parent watch %s: %w", filepath.Dir(absolutePath), addErr))
				if ctx != nil {
					w.startPollingFallback(ctx)
				}
				continue
			}
			if !w.isCurrentFsnotifyRun(ctx, fsw) {
				continue
			}
			// Close the stat-before-arm race just as Start does. Changes after Add
			// are queued by fsnotify; changes before it are reflected by this rescan.
			if info, statErr := os.Stat(absolutePath); statErr == nil {
				if changed, current := w.recordFileInfoPathForRun(ctx, debouncer, absolutePath, info); !current {
					continue
				} else if changed {
					w.scheduleChange(ctx, debouncer)
				}
			} else if os.IsNotExist(statErr) {
				if changed, current := w.recordMissingPathForRun(ctx, debouncer, absolutePath); !current {
					continue
				} else if changed {
					w.scheduleChange(ctx, debouncer)
				}
			} else {
				w.onError(fmt.Errorf("rescan additional watch path %s: %w", absolutePath, statErr))
			}
		}
	}
	return nil
}

// AddDirectoryPatterns extends a live watcher with a filtered non-recursive
// directory. It is safe to call after Start.
func (w *Watcher) AddDirectoryPatterns(directory string, patterns ...string) error {
	return w.addDirectoryPatterns(directory, false, patterns)
}

// AddRecursiveDirectoryPatterns extends a live watcher with a filtered
// recursive directory. It is safe to call after Start.
func (w *Watcher) AddRecursiveDirectoryPatterns(directory string, patterns ...string) error {
	return w.addDirectoryPatterns(directory, true, patterns)
}

func (w *Watcher) addDirectoryPatterns(directory string, recursive bool, patterns []string) error {
	watch, err := normalizeDirectoryWatch(directory, recursive, patterns)
	if err != nil {
		return err
	}
	w.mu.RLock()
	for _, existing := range w.directoryWatches {
		if sameDirectoryWatch(existing, watch) {
			w.mu.RUnlock()
			return nil
		}
	}
	w.mu.RUnlock()
	states, err := snapshotDirectoryStatesFor([]directoryWatch{watch})
	if err != nil {
		return err
	}
	dirs, collectErr := collectWatchDirectories(watch)
	if collectErr != nil && !os.IsNotExist(collectErr) {
		return fmt.Errorf("collect directory watch %s: %w", watch.path, collectErr)
	}
	missingRoot := os.IsNotExist(collectErr)

	pathType := DetectFilesystemType(watch.path)
	remotePath := isRemoteFilesystem(pathType)
	w.mu.Lock()
	for _, existing := range w.directoryWatches {
		if sameDirectoryWatch(existing, watch) {
			w.mu.Unlock()
			return nil
		}
	}
	w.directoryWatches = append(w.directoryWatches, watch)
	for path, state := range states {
		w.directoryStates[path] = state
	}
	fsw := w.fsWatcher
	ctx := w.ctx
	debouncer := w.debouncer
	started := w.started
	if remotePath {
		w.fsType = pathType
	}
	w.mu.Unlock()

	if started && remotePath && ctx != nil {
		w.startPollingFallback(ctx)
		return nil
	}
	if started && missingRoot && ctx != nil {
		// No existing ancestor was available to arm reliably. Polling is the
		// recovery path that observes the routed directory when it is created.
		w.startPollingFallback(ctx)
		return nil
	}
	if started && fsw != nil {
		armed := true
		for _, dir := range dirs {
			if addErr := fsw.Add(dir); addErr != nil {
				w.onError(fmt.Errorf("add directory watch %s: %w", dir, addErr))
				if ctx != nil {
					w.startPollingFallback(ctx)
				}
				armed = false
				break
			}
		}
		if armed {
			if !w.isCurrentFsnotifyRun(ctx, fsw) {
				return nil
			}
			// Close the snapshot-before-arm race for dynamic directory roots.
			if refreshed, refreshErr := w.snapshotDirectoryStates(); refreshErr != nil {
				w.onError(fmt.Errorf("rescan directory watch %s: %w", watch.path, refreshErr))
			} else {
				if changed, current := w.replaceDirectoryStatesForRun(ctx, debouncer, refreshed); !current {
					return nil
				} else if changed {
					w.scheduleChange(ctx, debouncer)
				}
			}
		}
	}
	return nil
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

func (w *Watcher) configureWatchedPaths() {
	w.watchedPaths = []string{w.path}
	w.watchedPathSet = map[string]struct{}{filepath.Clean(w.path): {}}
	w.fileStates = map[string]fileState{w.path: {}}
	w.directoryStates = make(map[string]fileState)
	dir := filepath.Dir(w.path)
	for _, name := range w.siblingFiles {
		name = strings.TrimSpace(name)
		if name == "" || name == "." || filepath.Base(name) != name {
			continue
		}
		path := filepath.Join(dir, name)
		if _, exists := w.watchedPathSet[filepath.Clean(path)]; exists {
			continue
		}
		w.watchedPathSet[filepath.Clean(path)] = struct{}{}
		w.watchedPaths = append(w.watchedPaths, path)
		w.fileStates[path] = fileState{}
	}
	for _, path := range w.additionalFiles {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		absolutePath = filepath.Clean(absolutePath)
		if _, exists := w.watchedPathSet[absolutePath]; exists {
			continue
		}
		w.watchedPathSet[absolutePath] = struct{}{}
		w.watchedPaths = append(w.watchedPaths, absolutePath)
		w.fileStates[absolutePath] = fileState{}
	}
	seenDirectories := make(map[string]bool, len(w.directoryPaths)+len(w.recursiveDirectoryPaths)+len(w.patternDirectoryWatches))
	addDirectoryWatch := func(path string, recursive bool, patterns []string) {
		watch, err := normalizeDirectoryWatch(path, recursive, patterns)
		if err != nil {
			return
		}
		key := directoryWatchKey(watch)
		if seenDirectories[key] {
			return
		}
		seenDirectories[key] = true
		w.directoryWatches = append(w.directoryWatches, watch)
	}
	for _, path := range w.directoryPaths {
		addDirectoryWatch(path, false, nil)
	}
	for _, path := range w.recursiveDirectoryPaths {
		addDirectoryWatch(path, true, nil)
	}
	for _, watch := range w.patternDirectoryWatches {
		addDirectoryWatch(watch.path, watch.recursive, watch.patterns)
	}
}

func normalizeDirectoryWatch(directory string, recursive bool, patterns []string) (directoryWatch, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return directoryWatch{}, fmt.Errorf("directory watch path is empty")
	}
	absolutePath, err := filepath.Abs(directory)
	if err != nil {
		return directoryWatch{}, fmt.Errorf("resolve directory watch path %s: %w", directory, err)
	}
	normalizedPatterns := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
		if pattern != "" {
			normalizedPatterns = append(normalizedPatterns, pattern)
		}
	}
	return directoryWatch{path: filepath.Clean(absolutePath), recursive: recursive, patterns: normalizedPatterns}, nil
}

func directoryWatchKey(watch directoryWatch) string {
	return fmt.Sprintf("%t:%s:%s", watch.recursive, watch.path, strings.Join(watch.patterns, "\x00"))
}

func sameDirectoryWatch(first, second directoryWatch) bool {
	return directoryWatchKey(first) == directoryWatchKey(second)
}

func (w *Watcher) refreshWatchedStatesLocked() (bool, error) {
	changed := false
	for _, path := range w.watchedPaths {
		state := w.fileStates[path]
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) && state.exists {
				w.fileStates[path] = fileState{}
				changed = true
				continue
			}
			if os.IsNotExist(err) {
				continue
			}
			return changed, fmt.Errorf("stat watched file %s: %w", path, err)
		}
		next := fileStateFromInfo(info)
		if !sameFileState(state, next) {
			changed = true
		}
		w.fileStates[path] = next
	}
	directoryStates, err := snapshotDirectoryStatesFor(w.directoryWatches)
	if err != nil {
		return changed, err
	}
	if !sameFileStates(w.directoryStates, directoryStates) {
		changed = true
	}
	w.directoryStates = directoryStates
	return changed, nil
}

func (w *Watcher) snapshotDirectoryStates() (map[string]fileState, error) {
	w.mu.RLock()
	watches := append([]directoryWatch(nil), w.directoryWatches...)
	w.mu.RUnlock()
	return snapshotDirectoryStatesFor(watches)
}

func snapshotDirectoryStatesFor(watches []directoryWatch) (map[string]fileState, error) {
	states := make(map[string]fileState)
	for _, watch := range watches {
		info, err := os.Stat(watch.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", watch.path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("watched directory path is not a directory: %s", watch.path)
		}
		if len(watch.patterns) == 0 {
			states[watch.path] = fileStateFromInfo(info)
		}
		if watch.recursive {
			err = filepath.WalkDir(watch.path, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if path == watch.path {
					return nil
				}
				if !watch.matches(path) {
					return nil
				}
				entryInfo, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				states[filepath.Clean(path)] = fileStateFromInfo(entryInfo)
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", watch.path, err)
			}
			continue
		}
		entries, readErr := os.ReadDir(watch.path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", watch.path, readErr)
		}
		for _, entry := range entries {
			path := filepath.Join(watch.path, entry.Name())
			if !watch.matches(path) {
				continue
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				return nil, fmt.Errorf("stat %s: %w", filepath.Join(watch.path, entry.Name()), infoErr)
			}
			states[path] = fileStateFromInfo(entryInfo)
		}
	}
	return states, nil
}

func sameFileStates(first, second map[string]fileState) bool {
	if len(first) != len(second) {
		return false
	}
	for path, state := range first {
		other, ok := second[path]
		if !ok || !sameFileState(state, other) {
			return false
		}
	}
	return true
}

func fileStateFromInfo(info os.FileInfo) fileState {
	if info == nil {
		return fileState{}
	}
	changeSec, changeNsec, hasChangeAt := fileInfoChangeTime(info)
	return fileState{
		exists:      true,
		mtime:       info.ModTime(),
		size:        info.Size(),
		info:        info,
		changeSec:   changeSec,
		changeNsec:  changeNsec,
		hasChangeAt: hasChangeAt,
	}
}

// fileInfoChangeTime extracts the inode metadata-change timestamp exposed by
// Unix Stat_t implementations. Reflection keeps the watcher portable without
// proliferating nearly identical build-tag files for Ctim versus Ctimespec.
// Platforms that do not expose either shape fall back to mtime/size/identity.
func fileInfoChangeTime(info os.FileInfo) (seconds, nanoseconds int64, ok bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	for _, fieldName := range []string{"Ctim", "Ctimespec"} {
		stamp := value.FieldByName(fieldName)
		if !stamp.IsValid() || stamp.Kind() != reflect.Struct {
			continue
		}
		seconds, secondsOK := signedIntegerField(stamp, "Sec")
		nanoseconds, nanosOK := signedIntegerField(stamp, "Nsec")
		if secondsOK && nanosOK {
			return seconds, nanoseconds, true
		}
	}
	seconds, secondsOK := signedIntegerField(value, "Ctime")
	nanoseconds, nanosOK := signedIntegerField(value, "Ctimensec")
	if secondsOK {
		return seconds, nanoseconds, !nanosOK || nanoseconds >= 0
	}
	return 0, 0, false
}

func signedIntegerField(value reflect.Value, name string) (int64, bool) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := field.Uint()
		if unsigned > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(unsigned), true
	default:
		return 0, false
	}
}

func sameFileState(first, second fileState) bool {
	if first.exists != second.exists || !first.mtime.Equal(second.mtime) || first.size != second.size {
		return false
	}
	if !first.exists {
		return true
	}
	// FileInfo snapshots let polling distinguish an atomic replacement even when
	// the writer deliberately preserves both size and modification time. Legacy
	// test helpers can still record timestamp-only state, so absence of either
	// identity leaves the traditional comparison intact.
	if first.info == nil || second.info == nil {
		return true
	}
	if !os.SameFile(first.info, second.info) {
		return false
	}
	if first.hasChangeAt && second.hasChangeAt &&
		(first.changeSec != second.changeSec || first.changeNsec != second.changeNsec) {
		return false
	}
	return true
}

func collectWatchDirectories(watch directoryWatch) ([]string, error) {
	info, err := os.Stat(watch.path)
	if err != nil {
		if os.IsNotExist(err) {
			parent := filepath.Dir(watch.path)
			if parentInfo, parentErr := os.Stat(parent); parentErr == nil && parentInfo.IsDir() {
				return []string{parent}, nil
			}
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory")
	}
	dirs := []string{filepath.Dir(watch.path), watch.path}
	if !watch.recursive {
		return dirs, nil
	}
	err = filepath.WalkDir(watch.path, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && path != watch.path {
			dirs = append(dirs, filepath.Clean(path))
		}
		return nil
	})
	return dirs, err
}

func (watch directoryWatch) contains(path string) bool {
	rel, err := filepath.Rel(watch.path, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return rel == "." || watch.recursive || filepath.Dir(rel) == "."
}

func (watch directoryWatch) matches(path string) bool {
	if !watch.contains(path) {
		return false
	}
	rel, err := filepath.Rel(watch.path, path)
	if err != nil {
		return false
	}
	if len(watch.patterns) == 0 {
		return true
	}
	rel = filepath.ToSlash(rel)
	for _, pattern := range watch.patterns {
		if matched, matchErr := pathpkg.Match(pattern, rel); matchErr == nil && matched {
			return true
		}
	}
	return false
}

// watchFsnotify monitors using fsnotify events.
func (w *Watcher) watchFsnotify(ctx context.Context, fsw *fsnotify.Watcher, debouncer *Debouncer) {
	// Capture channel references to avoid race with Stop() setting fsWatcher to nil
	w.mu.RLock()
	if w.fsWatcher != fsw {
		w.mu.RUnlock()
		return
	}
	events := fsw.Events
	errors := fsw.Errors
	w.mu.RUnlock()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-events:
			if !ok {
				return
			}
			if !w.isCurrentFsnotifyRun(ctx, fsw) {
				return
			}

			// Only care about the primary file and explicitly configured candidates.
			targetPath, err := filepath.Abs(event.Name)
			if err != nil {
				continue
			}
			targetPath = filepath.Clean(targetPath)
			w.mu.RLock()
			_, watched := w.watchedPathSet[targetPath]
			w.mu.RUnlock()
			directoryMatched, recursiveContained, directoryContained, directoryRoot := w.matchesDirectoryWatch(targetPath)
			topologyChange := (recursiveContained || directoryRoot) && event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0
			if !watched && !directoryMatched && !topologyChange {
				continue
			}
			if watched {
				w.handleFsnotifyPathEvent(ctx, debouncer, targetPath, event.Op)
			}
			if directoryContained {
				if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
					if info, statErr := os.Stat(targetPath); statErr == nil && info.IsDir() {
						if dirs, collectErr := collectWatchDirectories(directoryWatch{path: targetPath, recursive: recursiveContained}); collectErr == nil {
							for _, dir := range dirs {
								if addErr := fsw.Add(dir); addErr != nil {
									w.onError(fmt.Errorf("add directory watch %s: %w", dir, addErr))
									w.startPollingFallback(ctx)
									break
								}
							}
						} else if collectErr != nil {
							w.onError(fmt.Errorf("collect new directory watch %s: %w", targetPath, collectErr))
							w.startPollingFallback(ctx)
						}
					}
				}
				if (directoryMatched || topologyChange) && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					w.scheduleChange(ctx, debouncer)
				}
			}

		case err, ok := <-errors:
			if !ok {
				return
			}
			if !w.isCurrentFsnotifyRun(ctx, fsw) {
				return
			}
			w.onError(err)
			// An fsnotify error (notably an event-queue overflow) means change
			// delivery is no longer complete. Keep the directory watcher alive for
			// any later events, but add polling for this run so missed writes are
			// eventually observed instead of silently freezing the view.
			w.startPollingFallback(ctx)
		}
	}
}

func (w *Watcher) matchesDirectoryWatch(path string) (matched, recursiveContained, contained, root bool) {
	w.mu.RLock()
	watches := append([]directoryWatch(nil), w.directoryWatches...)
	w.mu.RUnlock()
	for _, watch := range watches {
		if !watch.contains(path) {
			continue
		}
		contained = true
		if same, err := filepath.Rel(watch.path, path); err == nil && same == "." {
			root = true
		}
		if watch.recursive {
			recursiveContained = true
		}
		if watch.matches(path) {
			matched = true
		}
	}
	return matched, recursiveContained, contained, root
}

func (w *Watcher) startPollingFallback(ctx context.Context) {
	w.mu.Lock()
	if w.ctx != ctx || !w.started || w.useFallback {
		w.mu.Unlock()
		return
	}
	w.useFallback = true
	debouncer := w.debouncer
	w.mu.Unlock()
	go w.watchPolling(ctx, debouncer)
}

func (w *Watcher) isCurrentFsnotifyRun(ctx context.Context, fsw *fsnotify.Watcher) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.started && w.ctx == ctx && w.fsWatcher == fsw
}

func (w *Watcher) isCurrentRun(ctx context.Context) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.started && w.ctx == ctx
}

func (w *Watcher) isCurrentRunDebouncer(ctx context.Context, debouncer *Debouncer) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.started && w.ctx == ctx && w.debouncer == debouncer
}

func (w *Watcher) scheduleChange(ctx context.Context, debouncer *Debouncer) {
	if debouncer == nil {
		return
	}
	debouncer.Trigger(func() {
		if w.isCurrentRunDebouncer(ctx, debouncer) {
			w.notifyChange()
		}
	})
}

func (w *Watcher) handleFsnotifyFileEvent(op fsnotify.Op) {
	w.mu.RLock()
	ctx := w.ctx
	debouncer := w.debouncer
	w.mu.RUnlock()
	w.handleFsnotifyPathEvent(ctx, debouncer, w.path, op)
}

func (w *Watcher) handleFsnotifyPathEvent(ctx context.Context, debouncer *Debouncer, path string, op fsnotify.Op) {
	if op&fsnotify.Remove != 0 {
		if changed, current := w.recordMissingPathForRun(ctx, debouncer, path); !current {
			return
		} else if changed {
			if path == w.path {
				w.onError(ErrFileRemoved)
			}
			w.scheduleChange(ctx, debouncer)
		}
		return
	}
	if op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if changed, current := w.recordMissingPathForRun(ctx, debouncer, path); !current {
				return
			} else if changed {
				if path == w.path {
					w.onError(ErrFileRemoved)
				}
				w.scheduleChange(ctx, debouncer)
			}
		} else if os.IsPermission(err) {
			w.onError(ErrPermission)
		} else {
			w.onError(err)
		}
		return
	}

	if _, current := w.recordFileInfoPathForRun(ctx, debouncer, path, info); !current {
		return
	}
	w.scheduleChange(ctx, debouncer)
}

// watchPolling monitors using periodic stat checks.
func (w *Watcher) watchPolling(ctx context.Context, debouncer *Debouncer) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if !w.isCurrentRun(ctx) {
				return
			}
			w.mu.RLock()
			watchedPaths := append([]string(nil), w.watchedPaths...)
			w.mu.RUnlock()
			for _, path := range watchedPaths {
				info, err := os.Stat(path)
				if err != nil {
					if os.IsNotExist(err) {
						hadFile, current := w.recordMissingPathForRun(ctx, debouncer, path)
						if !current {
							return
						}
						if hadFile {
							if path == w.path {
								w.onError(ErrFileRemoved)
							}
							w.scheduleChange(ctx, debouncer)
						}
					} else if os.IsPermission(err) {
						w.onError(ErrPermission)
					} else {
						w.onError(err)
					}
					continue
				}

				if changed, current := w.recordFileInfoPathForRun(ctx, debouncer, path, info); !current {
					return
				} else if changed {
					w.scheduleChange(ctx, debouncer)
				}
			}
			directoryStates, err := w.snapshotDirectoryStates()
			if err != nil {
				if os.IsPermission(err) {
					w.onError(ErrPermission)
				} else {
					w.onError(err)
				}
				continue
			}
			if directoryChanged, current := w.replaceDirectoryStatesForRun(ctx, debouncer, directoryStates); !current {
				return
			} else if directoryChanged {
				w.scheduleChange(ctx, debouncer)
			}
		}
	}
}

func (w *Watcher) recordMissing() bool {
	return w.recordMissingPath(w.path)
}

func (w *Watcher) recordMissingPath(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	state := w.fileStates[path]
	w.fileStates[path] = fileState{}
	return state.exists
}

func (w *Watcher) recordMissingPathForRun(ctx context.Context, debouncer *Debouncer, path string) (changed, current bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx != nil && (!w.started || w.ctx != ctx || w.debouncer != debouncer) {
		return false, false
	}
	state := w.fileStates[path]
	w.fileStates[path] = fileState{}
	return state.exists, true
}

func (w *Watcher) recordStat(mtime time.Time, size int64) bool {
	return w.recordStatPath(w.path, mtime, size)
}

func (w *Watcher) recordStatPath(path string, mtime time.Time, size int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	state := w.fileStates[path]
	changed := !state.exists || !mtime.Equal(state.mtime) || size != state.size
	w.fileStates[path] = fileState{
		exists:      true,
		mtime:       mtime,
		size:        size,
		info:        state.info,
		changeSec:   state.changeSec,
		changeNsec:  state.changeNsec,
		hasChangeAt: state.hasChangeAt,
	}
	return changed
}

func (w *Watcher) recordStatPathForRun(ctx context.Context, debouncer *Debouncer, path string, mtime time.Time, size int64) (changed, current bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx != nil && (!w.started || w.ctx != ctx || w.debouncer != debouncer) {
		return false, false
	}
	state := w.fileStates[path]
	changed = !state.exists || !mtime.Equal(state.mtime) || size != state.size
	w.fileStates[path] = fileState{
		exists:      true,
		mtime:       mtime,
		size:        size,
		info:        state.info,
		changeSec:   state.changeSec,
		changeNsec:  state.changeNsec,
		hasChangeAt: state.hasChangeAt,
	}
	return changed, true
}

func (w *Watcher) recordFileInfoPathForRun(ctx context.Context, debouncer *Debouncer, path string, info os.FileInfo) (changed, current bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx != nil && (!w.started || w.ctx != ctx || w.debouncer != debouncer) {
		return false, false
	}
	next := fileStateFromInfo(info)
	changed = !sameFileState(w.fileStates[path], next)
	w.fileStates[path] = next
	return changed, true
}

func (w *Watcher) replaceDirectoryStatesForRun(ctx context.Context, debouncer *Debouncer, states map[string]fileState) (changed, current bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx != nil && (!w.started || w.ctx != ctx || w.debouncer != debouncer) {
		return false, false
	}
	changed = !sameFileStates(w.directoryStates, states)
	w.directoryStates = states
	return changed, true
}

// notifyChange invokes the onChange callback and signals the change channel.
func (w *Watcher) notifyChange() {
	w.mu.RLock()
	started := w.started
	w.mu.RUnlock()

	// Don't notify if watcher has been stopped - avoid calling callbacks
	// after Stop() has been called. This is best-effort; there's a small
	// race window, but callbacks are idempotent so it's harmless.
	if !started {
		return
	}

	w.onChange()

	// Non-blocking send to change channel
	select {
	case w.changeCh <- struct{}{}:
	default:
	}
}
