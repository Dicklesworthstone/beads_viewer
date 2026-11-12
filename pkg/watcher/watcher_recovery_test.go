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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite"
)

type manualPollClock struct {
	tickCh    chan time.Time
	passDone  chan uint64
	extraHook func(runGeneration uint64)
}

func newManualPollClock() *manualPollClock {
	return &manualPollClock{
		tickCh:   make(chan time.Time),
		passDone: make(chan uint64, 16),
	}
}

func (c *manualPollClock) attach(w *Watcher) {
	w.pollTicker = func(time.Duration) (<-chan time.Time, func()) {
		return c.tickCh, func() {}
	}
	w.onPollPass = func(runGeneration uint64) {
		if c.extraHook != nil {
			c.extraHook(runGeneration)
		}
		c.passDone <- runGeneration
	}
}

func (c *manualPollClock) step(t *testing.T, w *Watcher) {
	t.Helper()
	if err := c.stepErr(w); err != nil {
		t.Fatal(err)
	}
}

func (c *manualPollClock) stepErr(w *Watcher) error {
	w.mu.RLock()
	started := w.started
	polling := w.useFallback
	targetGen := w.runGeneration
	var done <-chan struct{}
	if w.ctx != nil {
		done = w.ctx.Done()
	}
	w.mu.RUnlock()
	if !started || !polling {
		return fmt.Errorf("cannot step poll pass when watcher started=%v polling=%v", started, polling)
	}

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	for {
		select {
		case c.tickCh <- time.Now():
		case <-done:
			return errors.New("watcher stopped while sending deterministic poll tick")
		case <-deadline.C:
			return errors.New("timed out sending deterministic poll tick")
		}

		for {
			select {
			case gen := <-c.passDone:
				if gen == targetGen {
					return nil
				}
				if gen < targetGen {
					goto nextTick
				}
			case <-done:
				return errors.New("watcher stopped while awaiting deterministic poll pass")
			case <-deadline.C:
				return errors.New("timed out awaiting deterministic poll pass")
			}
		}
	nextTick:
	}
}

// Inject at the existing notification-channel boundary. The event loop,
// debouncer, polling loop and filesystem observations are real. This avoids
// overflowing the machine-wide kernel queue just to reproduce lost events.
func notificationWatch(t *testing.T, path string, onError func(error)) (*Watcher, *manualPollClock, chan fsnotify.Event, chan error, <-chan struct{}) {
	t.Helper()
	w, err := NewWatcher(path, WithForcePoll(true), WithPollInterval(10*time.Millisecond),
		WithDebounceDuration(time.Millisecond), WithOnError(onError))
	if err != nil {
		t.Fatal(err)
	}
	clock := newManualPollClock()
	clock.attach(w)
	info, endsWithNewline, err := statWatchedFile(path, w.isJSONL)
	if err != nil {
		t.Fatal(err)
	}
	w.lastExists = true
	w.lastInfo = info
	w.lastMtime = info.ModTime()
	w.lastSize = info.Size()
	w.lastEndsWithNewline = endsWithNewline
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
	return w, clock, events, failures, done
}

func waitWatchChangeErr(w *Watcher) error {
	select {
	case <-w.Changed():
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("watcher failed to deliver a refresh")
	}
}

func requireWatchChange(t *testing.T, w *Watcher) {
	t.Helper()
	if err := waitWatchChangeErr(w); err != nil {
		t.Fatal(err)
	}
}

func flushPendingDebounce(w *Watcher) error {
	if !w.debouncer.hasPending() {
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for w.debouncer.hasPending() {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for pending debouncer callback")
		}
		time.Sleep(200 * time.Microsecond)
	}
	return nil
}

func checkWatchQuiet(w *Watcher, clock *manualPollClock) error {
	if clock != nil && w.IsStarted() && w.IsPolling() {
		for i := 0; i < 2; i++ {
			if err := clock.stepErr(w); err != nil {
				return err
			}
		}
	}
	if err := flushPendingDebounce(w); err != nil {
		return err
	}
	select {
	case <-w.Changed():
		return errors.New("unchanged selected family produced another refresh")
	default:
		return nil
	}
}

func requireWatchQuiet(t *testing.T, w *Watcher, clock *manualPollClock) {
	t.Helper()
	if err := checkWatchQuiet(w, clock); err != nil {
		t.Fatal(err)
	}
}

// settleAfterWrite drives deterministic poll ticks after a completed write.
// It requires at least one refresh, then verifies the poller's recorded
// fingerprint matches the family on disk with no debounce pending and no
// refresh queued across three consecutive poll passes. More than
// maxRefreshesPerWrite refreshes fails the test (preserving the churn guard).
func settleAfterWrite(t *testing.T, w *Watcher, clock *manualPollClock) {
	t.Helper()
	refreshes, err := checkSettleAfterWrite(w, clock)
	if err != nil {
		t.Fatal(err)
	}
	walSize := int64(-1)
	if w.walPath != "" {
		if info, err := os.Stat(w.walPath); err == nil {
			walSize = info.Size()
		}
	}
	t.Logf("settled after %d refresh(es); selected WAL now %d bytes", refreshes, walSize)
}

func checkSettleAfterWrite(w *Watcher, clock *manualPollClock) (int, error) {
	const maxRefreshesPerWrite = 4
	if !w.debouncer.hasPending() && len(w.Changed()) == 0 {
		if err := clock.stepErr(w); err != nil {
			return 0, err
		}
	}
	if err := waitWatchChangeErr(w); err != nil {
		return 0, err
	}
	refreshes, clean := 1, 0
	for clean < 3 {
		if err := clock.stepErr(w); err != nil {
			return refreshes, err
		}
		if err := flushPendingDebounce(w); err != nil {
			return refreshes, err
		}
		select {
		case <-w.Changed():
			refreshes++
			clean = 0
			if refreshes > maxRefreshesPerWrite {
				return refreshes, fmt.Errorf("one write produced %d refreshes, more than the %d states it can show", refreshes, maxRefreshesPerWrite)
			}
			continue
		default:
		}
		ok, err := checkWatchRecordedFamily(w)
		if err != nil {
			return refreshes, err
		}
		if ok {
			clean++
		} else {
			clean = 0
		}
	}
	return refreshes, nil
}

// checkWatchRecordedFamily reports whether the poller has recorded the selected
// family as it is on disk now, with no debounce timer pending and no refresh
// queued. It reads the debouncer while holding w.mu, the same lock order as
// scheduleChange, so it never sees a Trigger half-done.
func checkWatchRecordedFamily(w *Watcher) (bool, error) {
	mainInfo, err := os.Stat(w.path)
	if err != nil {
		return false, err
	}
	var walInfo os.FileInfo
	if w.walPath != "" {
		if walInfo, err = os.Stat(w.walPath); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.lastExists || w.lastInfo == nil || !os.SameFile(w.lastInfo, mainInfo) ||
		!w.lastMtime.Equal(mainInfo.ModTime()) || w.lastSize != mainInfo.Size() || w.unsettledJSONL {
		return false, nil
	}
	if (w.lastWALInfo == nil) != (walInfo == nil) {
		return false, nil
	}
	if walInfo != nil && (!os.SameFile(w.lastWALInfo, walInfo) ||
		!w.lastWALInfo.ModTime().Equal(walInfo.ModTime()) || w.lastWALInfo.Size() != walInfo.Size()) {
		return false, nil
	}
	return !w.debouncer.hasPending() && len(w.changeCh) == 0, nil
}

func TestWatcherNotificationFailuresResumePolling(t *testing.T) {
	for _, failure := range []string{"overflow", "io_error", "events_closed", "errors_closed", "directory_renamed", "directory_removed"} {
		t.Run(failure, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "selected.db")
			if err := os.WriteFile(path, []byte("unchanged main"), 0o600); err != nil {
				t.Fatal(err)
			}
			reported := make(chan error, 8)
			w, clock, events, failures, _ := notificationWatch(t, path, func(err error) { reported <- err })
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
			requireWatchQuiet(t, w, clock)
			for i := 0; i < 3; i++ {
				if err := os.WriteFile(path+"-wal", bytes.Repeat([]byte{byte(i)}, i+1), 0o600); err != nil {
					t.Fatal(err)
				}
				settleAfterWrite(t, w, clock)
				requireWatchQuiet(t, w, clock)
			}
			// Reader coordination and unrelated siblings must not create loops.
			for _, suffix := range []string{"-shm", "-wal.other", ".other-wal"} {
				if err := os.WriteFile(path+suffix, []byte("not selected data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			requireWatchQuiet(t, w, clock)
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
	w, clock, _, failures, _ := notificationWatch(t, path, func(err error) { reported <- err })
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
	requireWatchQuiet(t, w, clock)
	for i := 0; i < 12; i++ {
		title := fmt.Sprintf("committed WAL version %d", i)
		if _, err := db.Exec("UPDATE issues SET title=?", title); err != nil {
			t.Fatal(err)
		}
		settleAfterWrite(t, w, clock)
		var got string
		if err := reader.QueryRow("SELECT title FROM issues").Scan(&got); err != nil || got != title {
			t.Fatalf("fresh snapshot after fallback: %q, %v; want %q", got, err, title)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("fixture must exercise WAL-only commits, not main-file writes")
		}
		requireWatchQuiet(t, w, clock)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	clock.step(t, w)
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
	w, clock, _, failures, _ := notificationWatch(t, path, func(error) { close(entered); <-release })
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
	requireWatchQuiet(t, w, clock)
	if err := os.WriteFile(path+"-wal", []byte("later change"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.step(t, w)
	requireWatchChange(t, w)
}

func TestWatcherFallbackCanStopFromErrorCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	var w *Watcher
	stopped := make(chan struct{})
	var (
		clock    *manualPollClock
		failures chan error
	)
	w, clock, _, failures, _ = notificationWatch(t, path, func(error) {
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
	requireWatchQuiet(t, w, clock)
}

func TestWatcherFallbackIsSingleAndOldRunsCannotRevive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selected.db")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	var reports atomic.Int32
	w, clock, _, failures, done := notificationWatch(t, path, func(error) { reports.Add(1) })
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
	requireWatchQuiet(t, w, clock)
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
	requireWatchQuiet(t, w, clock)
	if err := os.WriteFile(path+"-wal", []byte("new run"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.step(t, w)
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
	clock := newManualPollClock()
	clock.attach(w)
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
	requireWatchQuiet(t, w, clock)
	if err := os.WriteFile(path+"-wal", []byte("after closure"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.step(t, w)
	requireWatchChange(t, w)
}

func TestWatcherRecoveryNegativeControls(t *testing.T) {
	t.Run("churn_plant_exceeds_refresh_cap", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "selected.db")
		if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
			t.Fatal(err)
		}
		w, err := NewWatcher(path, WithForcePoll(true), WithDebounceDuration(time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		clock := newManualPollClock()
		clock.extraHook = func(runGeneration uint64) {
			// Planted bug: every poll schedules a refresh even when unchanged.
			w.scheduleChange(runGeneration)
		}
		clock.attach(w)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()

		if err := os.WriteFile(path+"-wal", []byte("commit"), 0o600); err != nil {
			t.Fatal(err)
		}
		refreshes, settleErr := checkSettleAfterWrite(w, clock)
		if settleErr == nil || !strings.Contains(settleErr.Error(), "more than the 4 states it can show") {
			t.Fatalf("expected churn negative control failure (>4 refreshes), got refreshes=%d err=%v", refreshes, settleErr)
		}
	})

	t.Run("shm_sibling_plant_fails_quiet_check", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "selected.db")
		if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
			t.Fatal(err)
		}
		w, err := NewWatcher(path, WithForcePoll(true), WithDebounceDuration(time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		var lastSHM os.FileInfo
		clock := newManualPollClock()
		clock.extraHook = func(runGeneration uint64) {
			// Planted bug: poller inspects the -shm reader-coordination sibling.
			info, statErr := os.Stat(path + "-shm")
			if statErr == nil && (lastSHM == nil || !lastSHM.ModTime().Equal(info.ModTime()) || lastSHM.Size() != info.Size()) {
				lastSHM = info
				w.scheduleChange(runGeneration)
			}
		}
		clock.attach(w)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()

		if err := os.WriteFile(path+"-shm", []byte("reader index"), 0o600); err != nil {
			t.Fatal(err)
		}
		quietErr := checkWatchQuiet(w, clock)
		if quietErr == nil || !strings.Contains(quietErr.Error(), "unchanged selected family produced another refresh") {
			t.Fatalf("expected -shm negative control failure, got err=%v", quietErr)
		}
	})
}

func TestWatcherSameSizeAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	fixedTime := time.Now().Truncate(time.Second)

	if err := os.WriteFile(path, []byte("{\"id\":\"1\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(path, WithForcePoll(true), WithDebounceDuration(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	clock := newManualPollClock()
	clock.attach(w)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	requireWatchQuiet(t, w, clock)

	staged := filepath.Join(dir, "issues.jsonl.tmp")
	if err := os.WriteFile(staged, []byte("{\"id\":\"2\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staged, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}

	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) || afterInfo.Size() != beforeInfo.Size() || os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("fixture must preserve mtime (%v vs %v) and size (%d vs %d) while changing inode",
			beforeInfo.ModTime(), afterInfo.ModTime(), beforeInfo.Size(), afterInfo.Size())
	}

	settleAfterWrite(t, w, clock)
	requireWatchQuiet(t, w, clock)
}

func TestWatcherTransientTruncate(t *testing.T) {
	t.Run("coalesces_transient_zero_byte_and_torn_jsonl", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "issues.jsonl")
		if err := os.WriteFile(path, []byte("{\"id\":\"bv-1\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		var (
			mu        sync.Mutex
			snapshots []string
		)
		w, err := NewWatcher(path,
			WithForcePoll(true),
			WithDebounceDuration(time.Millisecond),
			WithOnChange(func() {
				data, rerr := os.ReadFile(path)
				if rerr == nil {
					mu.Lock()
					snapshots = append(snapshots, string(data))
					mu.Unlock()
				}
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		clock := newManualPollClock()
		clock.attach(w)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()

		// Non-atomic writer step 1: O_TRUNC empties the previously valid JSONL file.
		if err := os.Truncate(path, 0); err != nil {
			t.Fatal(err)
		}
		clock.step(t, w)
		if err := flushPendingDebounce(w); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.Changed():
			t.Fatal("watcher fired prematurely on transient 0-byte JSONL")
		default:
		}

		// Non-atomic writer step 2: partial trailing line without newline.
		if err := os.WriteFile(path, []byte("{\"id\":\"bv-2\""), 0o600); err != nil {
			t.Fatal(err)
		}
		clock.step(t, w)
		if err := flushPendingDebounce(w); err != nil {
			t.Fatal(err)
		}
		select {
		case <-w.Changed():
			t.Fatal("watcher fired prematurely on torn non-newline-terminated JSONL")
		default:
		}

		// Non-atomic writer step 3: completed newline-terminated payload.
		if err := os.WriteFile(path, []byte("{\"id\":\"bv-2\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		settleAfterWrite(t, w, clock)
		requireWatchQuiet(t, w, clock)

		mu.Lock()
		got := append([]string(nil), snapshots...)
		mu.Unlock()
		if len(got) != 1 || got[0] != "{\"id\":\"bv-2\"}\n" {
			t.Fatalf("expected single clean snapshot for completed write, got %q", got)
		}
	})

	t.Run("emits_when_zero_byte_clear_persists", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "issues.jsonl")
		if err := os.WriteFile(path, []byte("{\"id\":\"bv-1\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		w, err := NewWatcher(path, WithForcePoll(true), WithDebounceDuration(time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		clock := newManualPollClock()
		clock.attach(w)
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()

		if err := os.Truncate(path, 0); err != nil {
			t.Fatal(err)
		}

		// First defaultJSONLSettlePolls-1 observations withhold to guard against transient O_TRUNC.
		for i := 1; i < defaultJSONLSettlePolls; i++ {
			clock.step(t, w)
			if err := flushPendingDebounce(w); err != nil {
				t.Fatal(err)
			}
			select {
			case <-w.Changed():
				t.Fatalf("emitted before settle window completed on poll %d", i)
			default:
			}
		}

		// Once 0-byte state persists across the settle window, watcher must emit
		// so intentional clears are never lost, then remain quiet.
		clock.step(t, w)
		requireWatchChange(t, w)
		requireWatchQuiet(t, w, clock)
	})
}
