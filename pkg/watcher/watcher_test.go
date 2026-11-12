package watcher

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite"
)

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

func TestWatcher_SQLiteWALCommits(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		poll bool
	}{
		{"fsnotify", "selected.sqlite3", false},
		{"polling", "selected.SQLITE", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BV_FORCE_POLL", "")
			t.Setenv("BV_FORCE_POLLING", "")
			path := filepath.Join(t.TempDir(), tc.file)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if _, err := db.Exec(`CREATE TABLE issues (title TEXT);
				INSERT INTO issues VALUES ('before');
				PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;`); err != nil {
				t.Fatal(err)
			}
			var journalMode string
			if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil || journalMode != "wal" {
				t.Fatalf("SQLite journal mode=%q, want wal: %v", journalMode, err)
			}
			if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
				t.Fatalf("expected WAL to be created by the first watched commit: %v", err)
			}
			errors := make(chan error, 10)
			w, err := NewWatcher(path, WithForcePoll(tc.poll), WithPollInterval(20*time.Millisecond),
				WithDebounceDuration(10*time.Millisecond), WithOnError(func(err error) { errors <- err }))
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Start(); err != nil {
				t.Fatal(err)
			}
			defer w.Stop()
			t.Logf("polling=%v", w.IsPolling())
			if w.IsPolling() != tc.poll {
				t.Fatalf("watcher backend polling=%v, want %v", w.IsPolling(), tc.poll)
			}
			// Similar sibling names must not refresh this selected database.
			for _, sibling := range []string{path + ".other-wal", path + "-wal.other"} {
				if err := os.WriteFile(sibling, []byte("unrelated"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-w.Changed():
				t.Fatal("unrelated sidecar triggered a refresh")
			case <-time.After(100 * time.Millisecond):
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, title := range []string{"after", "again"} {
				if _, err := db.Exec("UPDATE issues SET title=?", title); err != nil {
					t.Fatal(err)
				}
				if wal, err := os.Stat(path + "-wal"); err != nil || wal.Size() == 0 {
					t.Fatalf("committed update %q did not create a nonempty WAL: %v", title, err)
				}
				after, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
					t.Fatal("commit changed main database; test must exercise WAL-only updates")
				}
				select {
				case <-w.Changed():
				case err := <-errors:
					t.Fatalf("watch error: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatalf("missed committed WAL update %q with writer still open", title)
				}
			}
			// Closing the last SQLite connection checkpoints and removes its WAL.
			// That is a content change, not removal of the selected database.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
				t.Fatalf("SQLite did not remove WAL after checkpoint: %v", err)
			}
			select {
			case <-w.Changed():
			case err := <-errors:
				t.Fatalf("checkpoint was reported as a database error: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("checkpoint did not notify")
			}
			select {
			case err := <-errors:
				t.Fatalf("checkpoint reported an error: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestWatcher_JSONLIgnoresWALSidecar(t *testing.T) {
	for _, poll := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "issues.jsonl")
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		w, err := NewWatcher(path, WithForcePoll(poll), WithPollInterval(20*time.Millisecond), WithDebounceDuration(10*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		if w.IsPolling() != poll {
			w.Stop()
			t.Fatalf("JSONL watcher backend polling=%v, want %v", w.IsPolling(), poll)
		}
		if err := os.WriteFile(path+"-wal", []byte("unrelated"), 0o644); err != nil {
			w.Stop()
			t.Fatal(err)
		}
		select {
		case <-w.Changed():
			w.Stop()
			t.Fatalf("JSONL watcher treated a sibling as SQLite WAL (polling=%v)", poll)
		case <-time.After(100 * time.Millisecond):
		}
		w.Stop()
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
	w.mu.Lock()
	w.started = true
	w.runGeneration++
	runGeneration := w.runGeneration
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
	}()

	if _, active := w.recordStat(runGeneration, info); !active {
		t.Fatal("test run should be active")
	}
	if hadFile, active := w.recordMissing(runGeneration); !active || !hadFile {
		t.Fatal("initial removal should report that the file existed")
	}

	if err := os.WriteFile(tmpFile, []byte("recreated"), 0644); err != nil {
		t.Fatal(err)
	}
	w.handleFsnotifyFileEvent(runGeneration, fsnotify.Create)

	if hadFile, active := w.recordMissing(runGeneration); !active || !hadFile {
		t.Fatal("recreated file should be tracked as present before the next removal")
	}
}

func TestWatcher_FsnotifyRunUsesCapturedChannels(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile, WithDebounceDuration(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer w.debouncer.Cancel()

	// Establish an active generation without publishing an fsWatcher through
	// the owner. The loop must use only the channels captured by Start; reading
	// w.fsWatcher after launch would make it return early here.
	w.mu.Lock()
	w.started = true
	w.runGeneration++
	runGeneration := w.runGeneration
	w.fsWatcher = nil
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.started = false
		w.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan fsnotify.Event, 1)
	errors := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.watchFsnotify(ctx, runGeneration, events, errors)
	}()

	events <- fsnotify.Event{Name: tmpFile, Op: fsnotify.Write}
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fsnotify loop ignored the channels captured for its run")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fsnotify loop did not stop after cancellation")
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

func TestWatcher_DoneFollowsRunLifecycle(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(tmpFile, WithForcePoll(true))
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-w.Done():
	default:
		t.Fatal("Done should be closed before the watcher starts")
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	firstRunDone := w.Done()
	select {
	case <-firstRunDone:
		t.Fatal("Done closed while the watcher was running")
	default:
	}

	w.Stop()
	select {
	case <-firstRunDone:
	default:
		t.Fatal("Done remained open after Stop")
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	secondRunDone := w.Done()
	if secondRunDone == firstRunDone {
		t.Fatal("restart reused the previous run's Done channel")
	}
	select {
	case <-secondRunDone:
		t.Fatal("restarted watcher exposed a closed Done channel")
	default:
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

func TestWatcher_RestartRejectsQueuedAndLatePriorRunChanges(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.jsonl")
	if err := os.WriteFile(tmpFile, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	var callbacks atomic.Int32
	w, err := NewWatcher(tmpFile,
		WithForcePoll(true),
		WithPollInterval(time.Hour),
		WithDebounceDuration(5*time.Millisecond),
		WithOnChange(func() { callbacks.Add(1) }),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	w.mu.RLock()
	firstRun := w.runGeneration
	w.mu.RUnlock()

	// Queue an event with no consumer. Stop must remove it before the next run.
	w.notifyChange(firstRun)
	w.Stop()

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	w.mu.RLock()
	secondRun := w.runGeneration
	baselineMtime := w.lastMtime
	baselineSize := w.lastSize
	w.mu.RUnlock()
	if secondRun == firstRun {
		t.Fatal("restart did not advance watcher generation")
	}

	select {
	case <-w.Changed():
		t.Fatal("restarted watcher received an event queued by the prior run")
	default:
	}

	// A stale producer must not touch the shared debouncer after restart. If it
	// did, it would cancel the active generation's pending timer.
	w.scheduleChange(secondRun)
	w.scheduleChange(firstRun)
	select {
	case <-w.Changed():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stale producer canceled the active generation's debounce timer")
	}
	if got := callbacks.Load(); got != 2 {
		t.Fatalf("debounced active producer callback count=%d, want 2", got)
	}

	// A producer that was delayed across Stop/Start cannot mutate the new
	// baseline, invoke callbacks, or publish on the shared change channel.
	otherFile := filepath.Join(filepath.Dir(tmpFile), "other.jsonl")
	if err := os.WriteFile(otherFile, []byte("different size payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	futureTime := baselineMtime.Add(time.Hour)
	if err := os.Chtimes(otherFile, futureTime, futureTime); err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Stat(otherFile)
	if err != nil {
		t.Fatal(err)
	}
	if changed, active := w.recordStat(firstRun, otherInfo); changed || active {
		t.Fatalf("stale recordStat returned changed=%v active=%v", changed, active)
	}
	w.notifyChange(firstRun)
	w.mu.RLock()
	gotMtime := w.lastMtime
	gotSize := w.lastSize
	w.mu.RUnlock()
	if !gotMtime.Equal(baselineMtime) || gotSize != baselineSize {
		t.Fatal("stale producer changed the restarted watcher's file baseline")
	}
	if got := callbacks.Load(); got != 2 {
		t.Fatalf("stale producer invoked callback; callback count=%d", got)
	}
	select {
	case <-w.Changed():
		t.Fatal("stale producer published into the restarted run")
	default:
	}

	// The active generation still publishes normally.
	w.notifyChange(secondRun)
	select {
	case <-w.Changed():
	default:
		t.Fatal("active watcher generation failed to publish")
	}
	if got := callbacks.Load(); got != 3 {
		t.Fatalf("active producer callback count=%d, want 3", got)
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

func TestWatcher_RecordStatDetectsInodeChangeSameSizeMtime(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.jsonl")
	pathB := filepath.Join(dir, "b.jsonl")
	fixedTime := time.Now().Truncate(time.Second)

	if err := os.WriteFile(pathA, []byte("{\"id\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("{\"id\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathA, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathB, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}

	infoA, err := os.Stat(pathA)
	if err != nil {
		t.Fatal(err)
	}
	infoB, err := os.Stat(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if !infoA.ModTime().Equal(infoB.ModTime()) || infoA.Size() != infoB.Size() || os.SameFile(infoA, infoB) {
		t.Fatalf("fixture requires distinct inodes with identical mtime and size")
	}

	w, err := NewWatcher(pathA)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.started = true
	w.runGeneration = 1
	w.mu.Unlock()

	if changed, active := w.recordStat(1, infoA); !active || !changed {
		t.Fatalf("initial recordStat = (%v, %v), want (true, true)", changed, active)
	}
	if changed, active := w.recordStat(1, infoA); !active || changed {
		t.Fatalf("repeat same-file recordStat = (%v, %v), want (false, true)", changed, active)
	}
	if changed, active := w.recordStat(1, infoB); !active || !changed {
		t.Fatalf("inode-changed recordStat = (%v, %v), want (true, true)", changed, active)
	}
}

func TestWatcher_FsnotifyTransientJSONLTruncateSettles(t *testing.T) {
	t.Setenv("BV_FORCE_POLL", "")
	t.Setenv("BV_FORCE_POLLING", "")

	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"bv-1\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		obsMu    sync.Mutex
		observed []string
	)
	w, err := NewWatcher(path,
		WithDebounceDuration(15*time.Millisecond),
		WithOnChange(func() {
			data, rerr := os.ReadFile(path)
			if rerr == nil {
				obsMu.Lock()
				observed = append(observed, string(data))
				obsMu.Unlock()
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	w.jsonlSettleWindow = 80 * time.Millisecond

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	// Step 1: non-atomic writer truncates to 0 bytes, pauses longer than debounce,
	// writes a torn partial line, then completes with newline inside settle window.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := f.WriteString("{\"id\":\"bv-2\""); err != nil {
		f.Close()
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := f.WriteString("}\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-w.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for completed JSONL write notification")
	}

	obsMu.Lock()
	gotSnapshots := append([]string(nil), observed...)
	obsMu.Unlock()
	if len(gotSnapshots) != 1 || gotSnapshots[0] != "{\"id\":\"bv-2\"}\n" {
		t.Fatalf("unexpected snapshots during non-atomic JSONL write: %q", gotSnapshots)
	}

	// Step 2: intentional truncation to 0 bytes that persists across the settle window
	// must still notify so genuine clears are never lost.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for persistent 0-byte JSONL clear notification")
	}
	obsMu.Lock()
	finalSnapshots := append([]string(nil), observed...)
	obsMu.Unlock()
	if len(finalSnapshots) != 2 || finalSnapshots[1] != "" {
		t.Fatalf("expected persistent 0-byte clear snapshot after settle window, got %q", finalSnapshots)
	}
}
