package datasource

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// sourceDebounceTimer tracks a trailing-edge debounce timer for a logical source.
type sourceDebounceTimer struct {
	seq   uint64
	timer *time.Timer
}

// SourceWatcher monitors data sources for changes and triggers callbacks
type SourceWatcher struct {
	watcher       *fsnotify.Watcher
	sources       []DataSource
	watchRefs     map[string]int
	sourceWatches map[string][]string
	callback      func(changed DataSource)
	debounce      time.Duration
	timers        map[string]*sourceDebounceTimer
	nextSeq       uint64
	stopped       bool
	mu            sync.Mutex
	done          chan struct{}
	stopOnce      sync.Once
	verbose       bool
	logger        func(msg string)
}

// WatcherOptions configures the source watcher
type WatcherOptions struct {
	// Debounce is the minimum time between callbacks for the same file
	// Default: 100ms
	Debounce time.Duration
	// Verbose enables detailed logging
	Verbose bool
	// Logger receives log messages when Verbose is true
	Logger func(msg string)
}

// DefaultWatcherOptions returns sensible default watcher options
func DefaultWatcherOptions() WatcherOptions {
	return WatcherOptions{
		Debounce: 100 * time.Millisecond,
		Verbose:  false,
		Logger:   func(string) {},
	}
}

func sourceWatchTargets(source DataSource) (primary string, extras []string) {
	resolvedMain := resolveSourceTargetPath(source.Path)
	primary = resolvedMain
	resolvedDir := filepath.Dir(resolvedMain)
	if resolvedDir != "" && resolvedDir != resolvedMain {
		extras = append(extras, resolvedDir)
	}
	if source.Type == SourceTypeSQLite {
		walPath := resolvedMain + "-wal"
		if _, err := os.Stat(walPath); err == nil {
			extras = append(extras, walPath)
		}
	}
	if absOrig, err := filepath.Abs(source.Path); err == nil {
		origDir := filepath.Dir(absOrig)
		if origDir != "" && origDir != resolvedDir {
			extras = append(extras, origDir)
		}
	}
	return primary, extras
}

func (sw *SourceWatcher) addWatchRefLocked(target string) error {
	target = filepath.Clean(target)
	if sw.watchRefs[target] == 0 {
		if err := sw.watcher.Add(target); err != nil {
			return err
		}
	}
	sw.watchRefs[target]++
	return nil
}

func (sw *SourceWatcher) releaseWatchRefLocked(target string) error {
	target = filepath.Clean(target)
	if sw.watchRefs[target] <= 1 {
		delete(sw.watchRefs, target)
		return sw.watcher.Remove(target)
	}
	sw.watchRefs[target]--
	return nil
}

func (sw *SourceWatcher) registerSourceLocked(source DataSource, strict bool) error {
	primary, extras := sourceWatchTargets(source)
	var registered []string
	if err := sw.addWatchRefLocked(primary); err != nil {
		if strict {
			return err
		}
	} else {
		registered = append(registered, filepath.Clean(primary))
	}
	seen := make(map[string]bool, len(registered)+len(extras))
	for _, r := range registered {
		seen[r] = true
	}
	for _, extra := range extras {
		cleanExtra := filepath.Clean(extra)
		if seen[cleanExtra] {
			continue
		}
		if err := sw.addWatchRefLocked(cleanExtra); err == nil {
			seen[cleanExtra] = true
			registered = append(registered, cleanExtra)
		}
	}
	sw.sourceWatches[source.Path] = registered
	return nil
}

// syncSourceWatchesLocked reconciles fsnotify watch targets for source when its
// symlink target retargets to a different directory or its SQLite -wal sibling
// is created or checkpointed away.
func (sw *SourceWatcher) syncSourceWatchesLocked(source DataSource) {
	if sw.stopped {
		return
	}
	primary, extras := sourceWatchTargets(source)
	desiredList := make([]string, 0, 1+len(extras))
	desiredSet := make(map[string]bool, 1+len(extras))
	addDesired := func(p string) {
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		if !desiredSet[clean] {
			desiredSet[clean] = true
			desiredList = append(desiredList, clean)
		}
	}
	addDesired(primary)
	for _, extra := range extras {
		addDesired(extra)
	}

	oldTargets := sw.sourceWatches[source.Path]
	oldSet := make(map[string]bool, len(oldTargets))
	for _, t := range oldTargets {
		oldSet[t] = true
	}

	var nextRegistered []string
	for _, target := range desiredList {
		if oldSet[target] {
			nextRegistered = append(nextRegistered, target)
			continue
		}
		if err := sw.addWatchRefLocked(target); err == nil {
			nextRegistered = append(nextRegistered, target)
		}
	}
	for _, oldTarget := range oldTargets {
		if !desiredSet[oldTarget] {
			_ = sw.releaseWatchRefLocked(oldTarget)
		}
	}
	sw.sourceWatches[source.Path] = nextRegistered
}

func matchEventToSource(source DataSource, watchedTargets []string, cleanEvent string, op fsnotify.Op) bool {
	resolvedMain := filepath.Clean(resolveSourceTargetPath(source.Path))
	cleanOrig := filepath.Clean(source.Path)
	absOrig := cleanOrig
	if abs, err := filepath.Abs(source.Path); err == nil {
		absOrig = filepath.Clean(abs)
	}
	if source.Type == SourceTypeSQLite {
		walTarget := resolvedMain + "-wal"
		walOrig := cleanOrig + "-wal"
		walAbs := absOrig + "-wal"
		if cleanEvent == walTarget || cleanEvent == walOrig || cleanEvent == walAbs {
			return op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0
		}
	}
	if cleanEvent == resolvedMain || cleanEvent == cleanOrig || cleanEvent == absOrig {
		return op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0
	}
	// Match events against the previously registered primary target (or its -wal)
	// in case a symlink retargeted right as an event from the previous target arrived.
	if len(watchedTargets) > 0 {
		prevPrimary := watchedTargets[0]
		if cleanEvent == prevPrimary || (source.Type == SourceTypeSQLite && cleanEvent == prevPrimary+"-wal") {
			return op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0
		}
		// On macOS/BSD kqueue, atomically replacing a symlink via symlink(target, tmp)
		// + rename(tmp, link) may emit CREATE for tmp before rename completes and no
		// event for link itself. When source.Path is a symlink and an entry is
		// created/renamed/removed in its directory, let the trailing-edge debouncer
		// re-resolve the symlink target after the rename settles.
		isSymlink := resolvedMain != cleanOrig && resolvedMain != absOrig
		if (resolvedMain != prevPrimary || isSymlink) && op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
			eventDir := filepath.Clean(filepath.Dir(cleanEvent))
			origDir := filepath.Clean(filepath.Dir(cleanOrig))
			absDir := filepath.Clean(filepath.Dir(absOrig))
			if eventDir == origDir || eventDir == absDir {
				return true
			}
			if evalDir, err := filepath.EvalSymlinks(origDir); err == nil && filepath.Clean(evalDir) == eventDir {
				return true
			}
		}
	}
	return false
}

// NewSourceWatcher creates a watcher for the given sources
func NewSourceWatcher(sources []DataSource, callback func(DataSource), opts WatcherOptions) (*SourceWatcher, error) {
	if opts.Logger == nil {
		opts.Logger = func(string) {}
	}
	if opts.Debounce == 0 {
		opts.Debounce = 100 * time.Millisecond
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create file watcher: %w", err)
	}

	copiedSources := append([]DataSource(nil), sources...)
	sw := &SourceWatcher{
		watcher:       watcher,
		sources:       copiedSources,
		watchRefs:     make(map[string]int),
		sourceWatches: make(map[string][]string),
		callback:      callback,
		debounce:      opts.Debounce,
		timers:        make(map[string]*sourceDebounceTimer),
		done:          make(chan struct{}),
		verbose:       opts.Verbose,
		logger:        opts.Logger,
	}

	// Add all source paths (and SQLite WAL / resolved parent directories) to the watcher
	for _, source := range copiedSources {
		if err := sw.registerSourceLocked(source, false); err != nil {
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Cannot watch %s: %v", source.Path, err))
			}
		} else if opts.Verbose {
			opts.Logger(fmt.Sprintf("Watching: %s", source.Path))
		}
	}

	return sw, nil
}

// Start begins watching for file changes
func (sw *SourceWatcher) Start() {
	go sw.run()
}

// Stop stops watching file changes. Safe to call multiple times.
func (sw *SourceWatcher) Stop() {
	sw.stopOnce.Do(func() {
		sw.mu.Lock()
		sw.stopped = true
		for path, entry := range sw.timers {
			if entry != nil && entry.timer != nil {
				entry.timer.Stop()
			}
			delete(sw.timers, path)
		}
		sw.mu.Unlock()

		close(sw.done)
		sw.watcher.Close()
	})
}

func (sw *SourceWatcher) scheduleDebouncedLocked(source DataSource, eventName string) {
	if sw.stopped {
		return
	}
	sourcePath := source.Path
	if prev, ok := sw.timers[sourcePath]; ok && prev != nil && prev.timer != nil {
		prev.timer.Stop()
	}
	sw.nextSeq++
	seq := sw.nextSeq
	sw.timers[sourcePath] = &sourceDebounceTimer{
		seq: seq,
		timer: time.AfterFunc(sw.debounce, func() {
			sw.fireDebouncedSource(sourcePath, eventName, seq)
		}),
	}
}

func (sw *SourceWatcher) fireDebouncedSource(sourcePath, eventName string, seq uint64) {
	sw.mu.Lock()
	if sw.stopped {
		sw.mu.Unlock()
		return
	}
	entry, ok := sw.timers[sourcePath]
	if !ok || entry == nil || entry.seq != seq {
		sw.mu.Unlock()
		return
	}
	delete(sw.timers, sourcePath)

	var changedSource DataSource
	found := false
	for i := range sw.sources {
		if sw.sources[i].Path == sourcePath {
			changedSource = sw.sources[i]
			found = true
			break
		}
	}
	verbose := sw.verbose
	logger := sw.logger
	callback := sw.callback
	sw.mu.Unlock()

	if !found {
		return
	}
	select {
	case <-sw.done:
		return
	default:
	}

	if verbose {
		logger(fmt.Sprintf("Source changed: %s", eventName))
	}

	if err := RefreshSourceInfo(&changedSource); err != nil {
		if verbose {
			logger(fmt.Sprintf("Failed to refresh source info: %v", err))
		}
	}

	sw.mu.Lock()
	if sw.stopped {
		sw.mu.Unlock()
		return
	}
	sourceStillWatched := false
	for i := range sw.sources {
		if sw.sources[i].Path == sourcePath {
			sw.sources[i] = changedSource
			sourceStillWatched = true
			break
		}
	}
	if sourceStillWatched {
		sw.syncSourceWatchesLocked(changedSource)
	}
	sw.mu.Unlock()
	if !sourceStillWatched {
		return
	}

	select {
	case <-sw.done:
		return
	default:
	}

	if callback != nil {
		callback(changedSource)
	}
}

// run is the main event loop for the watcher
func (sw *SourceWatcher) run() {
	// On macOS/BSD kqueue, atomically replacing an existing symlink via
	// symlink(target, tmp) + rename(tmp, link) can complete before fsnotify scans
	// the directory, emitting zero events because the entry name is unchanged.
	// Periodically re-check symlink targets (no-op when no sources are symlinks).
	symlinkCheckTicker := time.NewTicker(100 * time.Millisecond)
	defer symlinkCheckTicker.Stop()

	for {
		select {
		case <-sw.done:
			return

		case <-symlinkCheckTicker.C:
			sw.mu.Lock()
			if sw.stopped {
				sw.mu.Unlock()
				return
			}
			for i := range sw.sources {
				src := sw.sources[i]
				watched := sw.sourceWatches[src.Path]
				if len(watched) == 0 {
					continue
				}
				resolvedNow := filepath.Clean(resolveSourceTargetPath(src.Path))
				if resolvedNow != "" && resolvedNow != watched[0] {
					sw.syncSourceWatchesLocked(src)
					sw.scheduleDebouncedLocked(src, src.Path)
				}
			}
			sw.mu.Unlock()

		case event, ok := <-sw.watcher.Events:
			if !ok {
				return
			}

			// Ignore pure chmod/unrelated events; WAL checkpoints may rename/remove -wal
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
				continue
			}

			cleanEvent := filepath.Clean(event.Name)

			sw.mu.Lock()
			if sw.stopped {
				sw.mu.Unlock()
				return
			}
			var changedSource DataSource
			sourceFound := false
			for i := range sw.sources {
				if matchEventToSource(sw.sources[i], sw.sourceWatches[sw.sources[i].Path], cleanEvent, event.Op) {
					changedSource = sw.sources[i]
					sourceFound = true
					break
				}
			}
			if !sourceFound {
				sw.mu.Unlock()
				continue
			}

			// Re-resolve symlink/WAL watch targets immediately so follow-up events
			// in a newly retargeted directory are captured during the quiet window.
			sw.syncSourceWatchesLocked(changedSource)

			// Trailing-edge debounce keyed by logical source path so rapid WAL page
			// writes followed by a SQLite commit coalesce into a post-commit refresh.
			sw.scheduleDebouncedLocked(changedSource, event.Name)
			sw.mu.Unlock()

		case err, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
			if sw.verbose {
				sw.logger(fmt.Sprintf("Watcher error: %v", err))
			}
		}
	}
}

// AddSource adds a new source to watch
func (sw *SourceWatcher) AddSource(source DataSource) error {
	sw.mu.Lock()
	if sw.stopped {
		sw.mu.Unlock()
		return fmt.Errorf("watcher is stopped")
	}

	// Check if already watching
	for _, s := range sw.sources {
		if s.Path == source.Path {
			sw.mu.Unlock()
			return nil
		}
	}

	if err := sw.registerSourceLocked(source, true); err != nil {
		sw.mu.Unlock()
		return fmt.Errorf("failed to watch %s: %w", source.Path, err)
	}

	sw.sources = append(sw.sources, source)
	verbose := sw.verbose
	logger := sw.logger
	sw.mu.Unlock()

	// SAFETY: logger is caller-controlled and may re-enter Sources, AddSource,
	// or RemoveSource. Invoke it only after releasing the non-reentrant mutex.
	if verbose {
		logger(fmt.Sprintf("Added watch: %s", source.Path))
	}

	return nil
}

// RemoveSource stops watching a source
func (sw *SourceWatcher) RemoveSource(path string) error {
	sw.mu.Lock()

	if prev, ok := sw.timers[path]; ok {
		if prev != nil && prev.timer != nil {
			prev.timer.Stop()
		}
		delete(sw.timers, path)
	}

	targets, ok := sw.sourceWatches[path]
	if !ok {
		if err := sw.watcher.Remove(path); err != nil {
			sw.mu.Unlock()
			return fmt.Errorf("failed to remove watch %s: %w", path, err)
		}
	} else {
		var firstErr error
		for _, target := range targets {
			if err := sw.releaseWatchRefLocked(target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		delete(sw.sourceWatches, path)
		if firstErr != nil {
			sw.mu.Unlock()
			return fmt.Errorf("failed to remove watch %s: %w", path, firstErr)
		}
	}

	// Remove from sources list
	for i, s := range sw.sources {
		if s.Path == path {
			sw.sources = append(sw.sources[:i], sw.sources[i+1:]...)
			break
		}
	}

	verbose := sw.verbose
	logger := sw.logger
	sw.mu.Unlock()

	// SAFETY: see AddSource. No external callback may run while sw.mu is held.
	if verbose {
		logger(fmt.Sprintf("Removed watch: %s", path))
	}

	return nil
}

// Sources returns the list of watched sources
func (sw *SourceWatcher) Sources() []DataSource {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	result := make([]DataSource, len(sw.sources))
	copy(result, sw.sources)
	return result
}

// AutoRefreshManager manages automatic source revalidation and reselection
type AutoRefreshManager struct {
	watcher        *SourceWatcher
	currentSource  *DataSource
	sources        []DataSource
	onSourceChange func(newSource DataSource, reason string)
	mu             sync.RWMutex
	opts           SelectionOptions
}

// AutoRefreshOptions configures the auto-refresh manager
type AutoRefreshOptions struct {
	// WatcherOptions for the underlying file watcher
	WatcherOptions WatcherOptions
	// SelectionOptions for source reselection
	SelectionOptions SelectionOptions
	// OnSourceChange is called when the active source changes
	OnSourceChange func(newSource DataSource, reason string)
}

// NewAutoRefreshManager creates a manager that automatically re-selects sources on changes
func NewAutoRefreshManager(sources []DataSource, opts AutoRefreshOptions) (*AutoRefreshManager, error) {
	copiedSources := append([]DataSource(nil), sources...)
	manager := &AutoRefreshManager{
		sources:        copiedSources,
		onSourceChange: opts.OnSourceChange,
		opts:           opts.SelectionOptions,
	}

	// Initial source selection
	selected, err := SelectBestSourceWithOptions(copiedSources, opts.SelectionOptions)
	if err != nil {
		return nil, err
	}
	manager.currentSource = &selected

	// Create watcher
	watcher, err := NewSourceWatcher(copiedSources, manager.handleChange, opts.WatcherOptions)
	if err != nil {
		return nil, err
	}
	manager.watcher = watcher

	return manager, nil
}

// Start begins automatic refresh monitoring
func (m *AutoRefreshManager) Start() {
	m.watcher.Start()
}

// Stop stops automatic refresh monitoring
func (m *AutoRefreshManager) Stop() {
	m.watcher.Stop()
}

// CurrentSource returns the currently selected source
func (m *AutoRefreshManager) CurrentSource() DataSource {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.currentSource == nil {
		return DataSource{}
	}
	return *m.currentSource
}

// handleChange is called when any source changes
func (m *AutoRefreshManager) handleChange(changed DataSource) {
	m.mu.Lock()

	// Re-validate the changed source
	for i := range m.sources {
		if m.sources[i].Path == changed.Path {
			m.sources[i].ModTime = changed.ModTime
			m.sources[i].Size = changed.Size
			ValidateSource(&m.sources[i])
			break
		}
	}

	// Re-select best source. SelectionOptions.Logger is caller-controlled and
	// may re-enter CurrentSource, so buffer its messages while m.mu is held and
	// deliver them only after releasing the non-reentrant lock.
	selectionOpts, flushSelectionLogs := bufferSelectionLogging(m.opts)
	newSelected, err := SelectBestSourceWithOptions(m.sources, selectionOpts)
	if err != nil {
		m.mu.Unlock()
		flushSelectionLogs()
		return
	}

	// Check if selection changed
	if m.currentSource != nil && m.currentSource.Path == newSelected.Path &&
		m.currentSource.ModTime.Equal(newSelected.ModTime) &&
		m.currentSource.Size == newSelected.Size &&
		m.currentSource.IssueCount == newSelected.IssueCount &&
		m.currentSource.Valid == newSelected.Valid {
		m.mu.Unlock()
		flushSelectionLogs()
		return
	}

	oldPath := ""
	if m.currentSource != nil {
		oldPath = m.currentSource.Path
	}

	m.currentSource = &newSelected

	callback := m.onSourceChange
	reason := "source updated"
	if oldPath != newSelected.Path {
		reason = fmt.Sprintf("switched from %s", oldPath)
	}
	m.mu.Unlock()

	flushSelectionLogs()
	if callback != nil {
		callback(newSelected, reason)
	}
}

// ForceRefresh triggers a manual refresh of all sources
func (m *AutoRefreshManager) ForceRefresh() error {
	m.mu.Lock()

	// Refresh and revalidate all sources
	for i := range m.sources {
		RefreshSourceInfo(&m.sources[i])
		ValidateSource(&m.sources[i])
	}

	// Re-select without invoking caller-controlled logging under m.mu. See
	// handleChange for the re-entrancy hazard.
	selectionOpts, flushSelectionLogs := bufferSelectionLogging(m.opts)
	newSelected, err := SelectBestSourceWithOptions(m.sources, selectionOpts)
	if err != nil {
		m.mu.Unlock()
		flushSelectionLogs()
		return err
	}

	// A forced refresh is also a forced publication. The selected path may be
	// unchanged while its contents, validation result, modtime, or size changed;
	// suppressing the callback in that case leaves the UI on the old snapshot
	// and also leaves currentSource carrying stale metadata.
	m.currentSource = &newSelected
	callback := m.onSourceChange
	m.mu.Unlock()

	flushSelectionLogs()
	if callback != nil {
		callback(newSelected, "force refresh")
	}

	return nil
}

func bufferSelectionLogging(opts SelectionOptions) (SelectionOptions, func()) {
	logger := opts.Logger
	var logs []string
	if opts.Verbose && logger != nil {
		opts.Logger = func(message string) {
			logs = append(logs, message)
		}
	}
	return opts, func() {
		if logger == nil {
			return
		}
		for _, message := range logs {
			logger(message)
		}
	}
}
