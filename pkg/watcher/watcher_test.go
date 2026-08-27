package watcher

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatcherFsnotifyErrorEnablesPollingFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(path, WithPollInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := newRunContext()
	defer cancel()
	fsw := &fsnotify.Watcher{
		Events: make(chan fsnotify.Event),
		Errors: make(chan error, 1),
	}
	w.mu.Lock()
	w.ctx = ctx
	w.cancel = cancel
	w.fsWatcher = fsw
	w.started = true
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.watchFsnotify(ctx, fsw, w.debouncer)
		close(done)
	}()
	fsw.Errors <- errors.New("event queue overflow")

	deadline := time.After(500 * time.Millisecond)
	for !w.IsPolling() {
		select {
		case <-deadline:
			t.Fatal("fsnotify error did not enable polling fallback")
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fsnotify watcher did not stop after cancellation")
	}
	w.debouncer.Cancel()
}

func TestDebouncer_CoalescesRapidTriggers(t *testing.T) {
	d := NewDebouncer(50 * time.Millisecond)

	var callCount atomic.Int32

	// Trigger rapidly 10 times
	for i := 0; i < 10; i++ {
		d.Trigger(func() {
			callCount.Add(1)
		})
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce to complete
	time.Sleep(100 * time.Millisecond)

	if count := callCount.Load(); count != 1 {
		t.Errorf("expected 1 callback invocation, got %d", count)
	}
}

func TestDebouncer_Cancel(t *testing.T) {
	d := NewDebouncer(50 * time.Millisecond)

	var called atomic.Bool

	d.Trigger(func() {
		called.Store(true)
	})

	// Cancel before debounce completes
	d.Cancel()

	time.Sleep(100 * time.Millisecond)

	if called.Load() {
		t.Error("callback should not have been invoked after cancel")
	}
}

func TestDebouncer_DefaultDuration(t *testing.T) {
	d := NewDebouncer(0)
	if d.Duration() != DefaultDebounceDuration {
		t.Errorf("expected default duration %v, got %v", DefaultDebounceDuration, d.Duration())
	}
}

func TestWatcher_DetectsFileChange(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	var (
		changeMu sync.Mutex
		changed  bool
	)

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(50*time.Millisecond),
		WithOnChange(func() {
			changeMu.Lock()
			changed = true
			changeMu.Unlock()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Give watcher time to initialize
	time.Sleep(100 * time.Millisecond)

	// Modify file
	if err := os.WriteFile(tmpFile, []byte("modified content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for change detection
	time.Sleep(300 * time.Millisecond)

	changeMu.Lock()
	wasChanged := changed
	changeMu.Unlock()

	if !wasChanged {
		t.Error("expected change to be detected")
	}
}

func TestWatcher_PollingFallback(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	var (
		changeMu sync.Mutex
		changed  bool
	)

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(50*time.Millisecond),
		WithPollInterval(100*time.Millisecond),
		WithForcePoll(true),
		WithOnChange(func() {
			changeMu.Lock()
			changed = true
			changeMu.Unlock()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if !w.IsPolling() {
		t.Error("expected watcher to be in polling mode")
	}

	// Give polling time to start
	time.Sleep(50 * time.Millisecond)

	// Modify file
	if err := os.WriteFile(tmpFile, []byte("modified via polling"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for polling to detect change
	time.Sleep(300 * time.Millisecond)

	changeMu.Lock()
	wasChanged := changed
	changeMu.Unlock()

	if !wasChanged {
		t.Error("expected change to be detected via polling")
	}
}

func TestWatcher_ChangedChannel(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(50*time.Millisecond),
		WithPollInterval(100*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Modify file
	go func() {
		time.Sleep(50 * time.Millisecond)
		os.WriteFile(tmpFile, []byte("new content"), 0644)
	}()

	// Wait for change via channel
	select {
	case <-w.Changed():
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout waiting for change notification")
	}
}

func TestWatcher_EnvForcePolling(t *testing.T) {
	t.Setenv("BV_FORCE_POLLING", "1")

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(10*time.Millisecond),
		WithPollInterval(25*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if !w.IsPolling() {
		t.Fatal("expected watcher to be in polling mode when BV_FORCE_POLLING is set")
	}
}

func TestWatcher_RemoteFilesystem_UsesPolling(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := detectFilesystemTypeFunc
	detectFilesystemTypeFunc = func(string) FilesystemType { return FSTypeNFS }
	t.Cleanup(func() { detectFilesystemTypeFunc = orig })

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(10*time.Millisecond),
		WithPollInterval(25*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if !w.IsPolling() {
		t.Fatal("expected watcher to use polling on remote filesystem")
	}
	if got := w.FilesystemType(); got != FSTypeNFS {
		t.Fatalf("expected filesystem type %v, got %v", FSTypeNFS, got)
	}
}

func TestWatcher_FileRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	var (
		errMu    sync.Mutex
		gotError error
	)

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(50*time.Millisecond),
		WithPollInterval(100*time.Millisecond),
		WithForcePoll(true),
		WithOnError(func(err error) {
			errMu.Lock()
			gotError = err
			errMu.Unlock()
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Give watcher time to start
	time.Sleep(50 * time.Millisecond)

	// Remove file
	if err := os.Remove(tmpFile); err != nil {
		t.Fatal(err)
	}

	// Wait for error detection
	time.Sleep(300 * time.Millisecond)

	errMu.Lock()
	receivedError := gotError
	errMu.Unlock()

	if receivedError != ErrFileRemoved {
		t.Errorf("expected ErrFileRemoved, got %v", receivedError)
	}

	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("removed primary file did not trigger source reselection")
	}
}

func TestWatcher_PollingDetectsNewSiblingFile(t *testing.T) {
	tmpDir := t.TempDir()
	primary := filepath.Join(tmpDir, "issues.jsonl")
	if err := os.WriteFile(primary, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(primary,
		WithSiblingFiles("beads.db"),
		WithDebounceDuration(5*time.Millisecond),
		WithPollInterval(10*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if err := os.WriteFile(filepath.Join(tmpDir, "beads.db"), []byte("new candidate"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("polling watcher did not detect newly created sibling candidate")
	}
}

func TestWatcherPollingDetectsAdditionalFileInAnotherDirectory(t *testing.T) {
	primaryDir := t.TempDir()
	secondaryDir := t.TempDir()
	primary := filepath.Join(primaryDir, "issues.jsonl")
	secondary := filepath.Join(secondaryDir, "issues.jsonl")
	if err := os.WriteFile(primary, []byte("primary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondary, []byte("secondary"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(primary,
		WithAdditionalFiles(secondary),
		WithDebounceDuration(5*time.Millisecond),
		WithPollInterval(10*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if err := os.WriteFile(secondary, []byte("secondary changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("polling watcher did not detect additional-file change")
	}
}

func TestWatcherDirectoryPatternsIgnoreUnmatchedFiles(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "issues.jsonl")
	if err := os.WriteFile(primary, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(primary,
		WithDirectoryPatterns(dir, "*.jsonl", "beads.db", "beads.db-wal"),
		WithDebounceDuration(time.Millisecond),
		WithPollInterval(5*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if err := os.WriteFile(filepath.Join(dir, "beads.db-shm"), []byte("noise"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
		t.Fatal("unmatched SQLite shared-memory file triggered a change")
	case <-time.After(40 * time.Millisecond):
	}

	if err := os.WriteFile(filepath.Join(dir, "new.jsonl"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("matching JSONL creation was not detected")
	}
}

func TestWatcherRecursiveDirectoryPatternsDetectNewWorktree(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(primary, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(primary,
		WithRecursiveDirectoryPatterns(root, "*/issues.jsonl"),
		WithDebounceDuration(time.Millisecond),
		WithPollInterval(5*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	worktree := filepath.Join(root, "feature")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "issues.jsonl"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("recursive watcher did not detect a new worktree issue file")
	}
}

func TestWatcherDynamicAddsBeforeAndAfterStart(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(primary, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	existing := filepath.Join(directory, "existing.jsonl")
	if err := os.WriteFile(existing, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(primary,
		WithDebounceDuration(time.Millisecond),
		WithPollInterval(5*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	// This used to panic because the pre-Start directory state map was nil.
	if err := w.AddDirectoryPatterns(directory, "*.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	additional := filepath.Join(t.TempDir(), "later.jsonl")
	if err := w.AddAdditionalFiles(additional); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(additional, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("dynamically added exact file was not detected")
	}

	newDirectory := t.TempDir()
	if err := w.AddDirectoryPatterns(newDirectory, "*.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDirectory, "new.jsonl"), []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("dynamically added directory pattern was not detected")
	}
}

func TestWatcherDynamicRemotePathEnablesPolling(t *testing.T) {
	primary := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(primary, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteDir := t.TempDir()
	remote := filepath.Join(remoteDir, "remote.jsonl")

	original := detectFilesystemTypeFunc
	detectFilesystemTypeFunc = func(path string) FilesystemType {
		if filepath.Clean(path) == filepath.Clean(remoteDir) {
			return FSTypeNFS
		}
		return FSTypeLocal
	}
	t.Cleanup(func() { detectFilesystemTypeFunc = original })

	w, err := NewWatcher(primary, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if w.IsPolling() {
		t.Fatal("local primary unexpectedly started in polling mode")
	}
	if err := w.AddAdditionalFiles(remote); err != nil {
		t.Fatal(err)
	}
	if !w.IsPolling() {
		t.Fatal("adding a remote source did not enable polling fallback")
	}
	if got := w.FilesystemType(); got != FSTypeNFS {
		t.Fatalf("filesystem type = %v, want nfs", got)
	}
}

func TestWatcher_PollingFileRemovedReportsOnce(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	var removedErrors atomic.Int32
	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(5*time.Millisecond),
		WithPollInterval(10*time.Millisecond),
		WithForcePoll(true),
		WithOnError(func(err error) {
			if err == ErrFileRemoved {
				removedErrors.Add(1)
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if err := os.Rename(tmpFile, tmpFile+".moved"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(500 * time.Millisecond)
	for removedErrors.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for removal error")
		case <-time.After(10 * time.Millisecond):
		}
	}

	time.Sleep(75 * time.Millisecond)
	if count := removedErrors.Load(); count != 1 {
		t.Fatalf("expected one removal error, got %d", count)
	}
}

func TestWatcher_PollingDetectsBackwardMtimeChangeSameSize(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("aaaa"), 0644); err != nil {
		t.Fatal(err)
	}
	initialMtime := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(tmpFile, initialMtime, initialMtime); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(5*time.Millisecond),
		WithPollInterval(10*time.Millisecond),
		WithForcePoll(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if err := os.WriteFile(tmpFile, []byte("bbbb"), 0644); err != nil {
		t.Fatal(err)
	}
	olderMtime := initialMtime.Add(-time.Hour)
	if err := os.Chtimes(tmpFile, olderMtime, olderMtime); err != nil {
		t.Fatal(err)
	}

	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for same-size backward-mtime change")
	}
}

func TestWatcher_FsnotifyCreateRefreshesRemovedState(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile, WithDebounceDuration(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.debouncer.Cancel()

	info, err := os.Stat(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	w.recordStat(info.ModTime(), info.Size())
	if !w.recordMissing() {
		t.Fatal("initial removal should report that the file existed")
	}

	if err := os.WriteFile(tmpFile, []byte("recreated"), 0644); err != nil {
		t.Fatal(err)
	}
	w.handleFsnotifyFileEvent(fsnotify.Create)

	if !w.recordMissing() {
		t.Fatal("recreated file should be tracked as present before the next removal")
	}
}

func TestWatcher_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	if w.IsStarted() {
		t.Error("watcher should not be started initially")
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	if !w.IsStarted() {
		t.Error("watcher should be started after Start()")
	}

	// Double start should error
	if err := w.Start(); err != ErrAlreadyStarted {
		t.Errorf("expected ErrAlreadyStarted, got %v", err)
	}

	w.Stop()

	if w.IsStarted() {
		t.Error("watcher should not be started after Stop()")
	}

	// Double stop should be safe
	w.Stop()
}

func TestWatcher_RestartRejectsOldRunDebounce(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher(tmpFile,
		WithForcePoll(true),
		WithPollInterval(time.Hour),
		WithDebounceDuration(5*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	w.mu.RLock()
	oldCtx := w.ctx
	oldDebouncer := w.debouncer
	w.mu.RUnlock()
	w.Stop()
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	w.mu.RLock()
	currentState := w.fileStates[tmpFile]
	w.mu.RUnlock()
	if changed, current := w.recordStatPathForRun(
		oldCtx,
		oldDebouncer,
		tmpFile,
		currentState.mtime.Add(time.Hour),
		currentState.size+1,
	); changed || current {
		t.Fatalf("old run state write = changed %v, current %v; want both false", changed, current)
	}
	w.mu.RLock()
	afterStaleWrite := w.fileStates[tmpFile]
	w.mu.RUnlock()
	if !sameFileState(afterStaleWrite, currentState) {
		t.Fatalf("old run mutated current file state: before %+v, after %+v", currentState, afterStaleWrite)
	}

	w.scheduleChange(oldCtx, oldDebouncer)
	select {
	case <-w.Changed():
		t.Fatal("old run delivered a change after restart")
	case <-time.After(30 * time.Millisecond):
	}

	w.mu.RLock()
	currentCtx := w.ctx
	currentDebouncer := w.debouncer
	w.mu.RUnlock()
	w.scheduleChange(currentCtx, currentDebouncer)
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("current run debounce did not deliver a change")
	}
}

func TestWatcher_FileStateDetectsIdentityChangeWithSameMetadata(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.jsonl")
	secondPath := filepath.Join(dir, "second.jsonl")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte("same"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, path := range []string{firstPath, secondPath} {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstInfo.Size() != secondInfo.Size() || !firstInfo.ModTime().Equal(secondInfo.ModTime()) {
		t.Fatal("test setup did not produce matching size and modification time")
	}
	if sameFileState(fileStateFromInfo(firstInfo), fileStateFromInfo(secondInfo)) {
		t.Fatal("different file identities with matching size and modification time were treated as unchanged")
	}
}

func TestWatcher_FileStateDetectsSameIdentityContentChangeWithRestoredMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same-file.jsonl")
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := fileStateFromInfo(beforeInfo)
	if !before.hasChangeAt {
		t.Skip("platform does not expose a change-time token")
	}

	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	after := fileStateFromInfo(afterInfo)
	if !os.SameFile(beforeInfo, afterInfo) || before.size != after.size || !before.mtime.Equal(after.mtime) {
		t.Fatal("test setup did not preserve identity, size, and modification time")
	}
	if before.changeSec == after.changeSec && before.changeNsec == after.changeNsec {
		t.Skip("filesystem did not advance change time at test resolution")
	}
	if sameFileState(before, after) {
		t.Fatal("same-inode content replacement with restored metadata was treated as unchanged")
	}
}

func TestWatcher_RestartPollingUsesPerRunContext(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	var changes atomic.Int32
	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(time.Millisecond),
		WithPollInterval(time.Millisecond),
		WithForcePoll(true),
		WithOnChange(func() {
			changes.Add(1)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		if err := w.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
		w.Stop()
	}

	if err := w.Start(); err != nil {
		t.Fatalf("final start: %v", err)
	}
	defer w.Stop()

	if err := os.WriteFile(tmpFile, []byte("after restart"), 0644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(500 * time.Millisecond)
	for changes.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for change after watcher restart")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestWatcher_Path(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	absPath, _ := filepath.Abs(tmpFile)
	if w.Path() != absPath {
		t.Errorf("expected path %s, got %s", absPath, w.Path())
	}
}

func TestWatcher_PollInterval(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	customInterval := 500 * time.Millisecond
	w, err := NewWatcher(tmpFile, WithPollInterval(customInterval))
	if err != nil {
		t.Fatal(err)
	}

	if got := w.PollInterval(); got != customInterval {
		t.Errorf("expected poll interval %v, got %v", customInterval, got)
	}
}

func TestWatcher_InvalidPollIntervalUsesDefault(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")

	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, interval := range []time.Duration{0, -time.Millisecond} {
		w, err := NewWatcher(tmpFile, WithPollInterval(interval))
		if err != nil {
			t.Fatal(err)
		}
		if got := w.PollInterval(); got != DefaultPollInterval {
			t.Fatalf("PollInterval(%v) = %v, want %v", interval, got, DefaultPollInterval)
		}
	}
}

func TestFilesystemType_String(t *testing.T) {
	tests := []struct {
		fsType   FilesystemType
		expected string
	}{
		{FSTypeUnknown, "unknown"},
		{FSTypeLocal, "local"},
		{FSTypeNFS, "nfs"},
		{FSTypeSMB, "smb"},
		{FSTypeSSHFS, "sshfs"},
		{FSTypeFUSE, "fuse"},
		{FilesystemType(99), "unknown"}, // invalid type
	}

	for _, tc := range tests {
		if got := tc.fsType.String(); got != tc.expected {
			t.Errorf("FilesystemType(%d).String() = %q, expected %q", tc.fsType, got, tc.expected)
		}
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		value    string
		expected bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"YES", true},
		{"y", true},
		{"Y", true},
		{"on", true},
		{"ON", true},
		{"0", false},
		{"false", false},
		{"no", false},
		{"", false},
		{"invalid", false},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("TEST_ENV_BOOL", tc.value)
			if got := envBool("TEST_ENV_BOOL"); got != tc.expected {
				t.Errorf("envBool(%q) = %v, expected %v", tc.value, got, tc.expected)
			}
		})
	}
}

func TestEnvBool_Unset(t *testing.T) {
	// Ensure the variable is not set
	os.Unsetenv("TEST_UNSET_VAR")
	if got := envBool("TEST_UNSET_VAR"); got != false {
		t.Errorf("envBool for unset var = %v, expected false", got)
	}
}

func TestDetectFilesystemType_EmptyPath(t *testing.T) {
	if got := DetectFilesystemType(""); got != FSTypeUnknown {
		t.Errorf("DetectFilesystemType(\"\") = %v, expected FSTypeUnknown", got)
	}
}

func TestDetectFilesystemType_NonExistentPath(t *testing.T) {
	// Should fall back to parent directory detection
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "does_not_exist.txt")
	// Should not panic, should return some valid type
	_ = DetectFilesystemType(nonExistent)
}

func TestWatcher_EnvForcePoll(t *testing.T) {
	// Test BV_FORCE_POLL (alternative env var)
	t.Setenv("BV_FORCE_POLL", "true")

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile,
		WithDebounceDuration(10*time.Millisecond),
		WithPollInterval(25*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	if !w.IsPolling() {
		t.Fatal("expected watcher to be in polling mode when BV_FORCE_POLL is set")
	}
}
