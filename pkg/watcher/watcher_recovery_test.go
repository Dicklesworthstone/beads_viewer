package watcher

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite"
)

// Inject at the existing notification-channel boundary. The event loop,
// debouncer, polling loop and filesystem observations are real. This avoids
// overflowing the machine-wide kernel queue just to reproduce lost events.
func notificationWatch(t *testing.T, path string, onError func(error)) (*Watcher, chan fsnotify.Event, chan error, <-chan struct{}) {
	t.Helper()
	w, err := NewWatcher(path, WithForcePoll(true), WithPollInterval(10*time.Millisecond),
		WithDebounceDuration(time.Millisecond), WithOnError(onError))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	w.lastExists, w.lastMtime, w.lastSize = true, info.ModTime(), info.Size()
	if w.walPath != "" {
		w.lastWALInfo, err = os.Stat(w.walPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	w.ctx, w.cancel = newRunContext()
	w.started, w.runGeneration = true, 1
	runCtx := w.ctx
	events, failures := make(chan fsnotify.Event, 8), make(chan error, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.watchFsnotify(runCtx, 1, events, failures)
	}()
	t.Cleanup(func() {
		w.Stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("notification loop did not stop")
		}
	})
	return w, events, failures, done
}

func requireWatchChange(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher failed to deliver a refresh")
	}
}

func requireWatchQuiet(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Changed():
		t.Fatal("unchanged selected family produced another refresh")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatcherNotificationFailuresResumePolling(t *testing.T) {
	for _, failure := range []string{"overflow", "io_error", "events_closed", "errors_closed", "directory_renamed", "directory_removed"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "selected.db")
			if err := os.WriteFile(path, []byte("unchanged main"), 0o600); err != nil {
				t.Fatal(err)
			}
			reported := make(chan error, 8)
			w, events, failures, _ := notificationWatch(t, path, func(err error) { reported <- err })
			changed, runDone := w.Changed(), w.Done()
			var want error
			switch failure {
			case "overflow":
				want = fsnotify.ErrEventOverflow
				failures <- want
			case "io_error":
				want = io.ErrUnexpectedEOF
				failures <- want
			case "events_closed":
				close(events)
			case "errors_closed":
				close(failures)
			case "directory_renamed":
				events <- fsnotify.Event{Name: filepath.Dir(path), Op: fsnotify.Rename}
			case "directory_removed":
				events <- fsnotify.Event{Name: filepath.Dir(path), Op: fsnotify.Remove}
			}
			select {
			case got := <-reported:
				if got == nil || (want != nil && !errors.Is(got, want)) {
					t.Fatalf("failure cause changed: %v, want %v", got, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("notification loss was silently ignored")
			}
			if !w.IsPolling() || !w.IsStarted() || w.Changed() != changed || w.Done() != runDone {
				t.Fatal("handover must preserve the running watch and existing consumers")
			}
			// No disk metadata changed: uncertainty alone must request a rescan.
			requireWatchChange(t, w)
			requireWatchQuiet(t, w)
			for i := 0; i < 3; i++ {
				if err := os.WriteFile(path+"-wal", bytes.Repeat([]byte{byte(i)}, i+1), 0o600); err != nil {
					t.Fatal(err)
				}
				requireWatchChange(t, w)
				requireWatchQuiet(t, w)
			}
			// Reader coordination and unrelated siblings must not create loops.
			for _, suffix := range []string{"-shm", "-wal.other", ".other-wal"} {
				if err := os.WriteFile(path+suffix, []byte("not selected data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			requireWatchQuiet(t, w)
		})
	}
}

func TestWatcherFallbackObservesRealWALCommits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.sqlite3")
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
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	reader, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reported := make(chan error, 8)
	w, _, failures, _ := notificationWatch(t, path, func(err error) { reported <- err })
	failures <- fsnotify.ErrEventOverflow
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow not reported")
	}
	if !w.IsPolling() {
		t.Fatal("lost WAL notifications did not activate polling")
	}
	requireWatchChange(t, w)
	requireWatchQuiet(t, w)
	for i := 0; i < 12; i++ {
		title := fmt.Sprintf("committed WAL version %d", i)
		if _, err := db.Exec("UPDATE issues SET title=?", title); err != nil {
			t.Fatal(err)
		}
		requireWatchChange(t, w)
		var got string
		if err := reader.QueryRow("SELECT title FROM issues").Scan(&got); err != nil || got != title {
			t.Fatalf("fresh snapshot after fallback: %q, %v; want %q", got, err, title)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("fixture must exercise WAL-only commits, not main-file writes")
		}
		requireWatchQuiet(t, w)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	requireWatchChange(t, w) // last close checkpoints and removes the WAL
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("fixture did not checkpoint/remove WAL on last close: %v", err)
	}
}

func TestWatcherFallbackDoesNotWaitForErrorCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	w, _, failures, _ := notificationWatch(t, path, func(error) { close(entered); <-release })
	failures <- fsnotify.ErrEventOverflow
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("error callback did not run")
	}
	if !w.IsPolling() {
		t.Fatal("blocked callback prevented polling handover")
	}
	requireWatchChange(t, w)
	requireWatchQuiet(t, w)
	if err := os.WriteFile(path+"-wal", []byte("later change"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireWatchChange(t, w)
}

func TestWatcherFallbackCanStopFromErrorCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	var w *Watcher
	stopped := make(chan struct{})
	var failures chan error
	w, _, failures, _ = notificationWatch(t, path, func(error) {
		w.Stop()
		close(stopped)
	})
	failures <- fsnotify.ErrEventOverflow
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("OnError deadlocked calling Stop")
	}
	if w.IsStarted() {
		t.Fatal("fallback revived a stopped watcher")
	}
	select {
	case <-w.Done():
	default:
		t.Fatal("Stop did not cancel fallback's context")
	}
	requireWatchQuiet(t, w)
}

func TestWatcherFallbackIsSingleAndOldRunsCannotRevive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reports atomic.Int32
	w, _, failures, done := notificationWatch(t, path, func(error) { reports.Add(1) })
	failures <- fsnotify.ErrEventOverflow
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("failed notification loop did not hand over")
	}
	requireWatchChange(t, w)
	closed := make(chan fsnotify.Event)
	close(closed)
	var peers sync.WaitGroup
	for i := 0; i < 16; i++ {
		peers.Add(1)
		go func() {
			defer peers.Done()
			w.watchFsnotify(w.ctx, 1, closed, nil)
		}()
	}
	peers.Wait()
	if reports.Load() != 1 {
		t.Fatalf("duplicate handovers: %d error callbacks", reports.Load())
	}
	requireWatchQuiet(t, w)
	w.Stop()
	if err := w.Start(); err != nil { // ForcePoll was set by the fixture.
		t.Fatal(err)
	}
	// Deliberately deliver a stopped run's failure AFTER Start. An old
	// producer must not detach the new backend or schedule a spurious refresh.
	w.watchFsnotify(context.Background(), 1, closed, nil)
	if reports.Load() != 1 || !w.IsStarted() || !w.IsPolling() {
		t.Fatal("old notification failure crossed the restart generation")
	}
	requireWatchQuiet(t, w)
	if err := os.WriteFile(path+"-wal", []byte("new run"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireWatchChange(t, w)
}

// Also cover the actual fsnotify backend when the platform supports it. Unlike
// the injected overflow tests, this verifies detachment/Close of a real watcher.
func TestWatcherNativeBackendClosureResumesPolling(t *testing.T) {
	t.Setenv("BV_FORCE_POLL", "")
	t.Setenv("BV_FORCE_POLLING", "")
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	reported := make(chan error, 8)
	w, err := NewWatcher(path, WithPollInterval(10*time.Millisecond),
		WithDebounceDuration(time.Millisecond), WithOnError(func(err error) { reported <- err }))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if w.IsPolling() {
		t.Skip("native notifications unavailable on this filesystem")
	}
	w.mu.RLock()
	backend := w.fsWatcher
	w.mu.RUnlock()
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reported:
	case <-time.After(2 * time.Second):
		t.Fatal("closed native backend left a silently dead watcher")
	}
	if !w.IsPolling() || !w.IsStarted() {
		t.Fatal("native backend closure did not preserve monitoring")
	}
	requireWatchChange(t, w)
	requireWatchQuiet(t, w)
	if err := os.WriteFile(path+"-wal", []byte("after closure"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireWatchChange(t, w)
}
