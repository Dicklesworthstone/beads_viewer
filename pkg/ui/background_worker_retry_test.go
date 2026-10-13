package ui

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"
)

func loadRetryWorker(t *testing.T) *BackgroundWorker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	loopCtx, loopCancel := context.WithCancel(ctx)
	w := &BackgroundWorker{
		ctx: ctx, cancel: cancel, loopCtx: loopCtx, loopCancel: loopCancel,
		started: true, generation: 1, state: WorkerIdle,
		heartbeatInterval: 10 * time.Millisecond,
		msgCh:             make(chan tea.Msg, 32), logLevel: LogLevelNone,
	}
	t.Cleanup(func() {
		w.mu.Lock()
		w.state = WorkerStopped
		w.mu.Unlock()
		loopCancel()
		cancel()
	})
	return w
}

func loadRetryFailure(w *BackgroundWorker, at time.Time, failures int) {
	w.mu.Lock()
	w.lastError = &WorkerError{Phase: "load", Cause: errors.New("database is locked"), Time: at, Retries: failures}
	w.errorCount = failures
	w.mu.Unlock()
}

func TestBackgroundWorkerLoadRetryBackoff(t *testing.T) {
	for _, tc := range []struct {
		failures int
		delay    time.Duration
	}{{0, 5 * time.Second}, {1, 5 * time.Second}, {2, 10 * time.Second},
		{3, 20 * time.Second}, {4, 30 * time.Second}, {100, 30 * time.Second},
		{int(^uint(0) >> 1), 30 * time.Second}} {
		if got := loadRetryDelay(tc.failures); got != tc.delay {
			t.Fatalf("failures=%d: delay=%v, want %v", tc.failures, got, tc.delay)
		}
		w := loadRetryWorker(t)
		at := time.Now()
		loadRetryFailure(w, at, tc.failures)
		if got := w.reserveLoadRetry(w.loopCtx, at.Add(tc.delay-time.Nanosecond)); got != nil {
			t.Fatal("retry admitted before its backoff elapsed")
		}
		if got := w.reserveLoadRetry(w.loopCtx, at.Add(tc.delay)); got == nil {
			t.Fatal("due load retry was not admitted")
		}
	}
}

func TestBackgroundWorkerLoadRetryAdmissionConditions(t *testing.T) {
	for _, state := range []string{"not_started", "stopped", "processing", "scheduled", "recovering",
		"cancelled_loop", "replaced_loop", "analysis_error", "cleared_error", "unknown_error_time", "future_error"} {
		t.Run(state, func(t *testing.T) {
			w := loadRetryWorker(t)
			now := time.Now()
			loadRetryFailure(w, now.Add(-time.Minute), 1)
			loop := w.loopCtx
			switch state {
			case "not_started":
				w.started = false
			case "stopped":
				w.state = WorkerStopped
			case "processing":
				w.state = WorkerProcessing
			case "scheduled":
				w.processScheduled = true
			case "recovering":
				w.recovering = true
			case "cancelled_loop":
				w.loopCancel()
			case "replaced_loop":
				w.loopCtx = context.Background()
			case "analysis_error":
				w.lastError.Phase = "analyze_phase1"
			case "cleared_error":
				w.lastError = nil
			case "unknown_error_time":
				w.lastError.Time = time.Time{}
			case "future_error":
				w.lastError.Time = now.Add(time.Minute)
			}
			wasScheduled, wasForced := w.processScheduled, w.forceNext
			if got := w.reserveLoadRetry(loop, now); got != nil {
				t.Fatalf("retry admitted in %s", state)
			}
			if w.processScheduled != wasScheduled || w.forceNext != wasForced {
				t.Fatal("rejected retry changed another request's scheduling state")
			}
		})
	}
}

func TestBackgroundWorkerLoadRetryReservesOneBuild(t *testing.T) {
	w := loadRetryWorker(t)
	now := time.Now()
	loadRetryFailure(w, now.Add(-time.Minute), 1)
	var callers sync.WaitGroup
	var reserved atomic.Int32
	for i := 0; i < 64; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if w.reserveLoadRetry(w.loopCtx, now) != nil {
				reserved.Add(1)
			}
		}()
	}
	callers.Wait()
	if reserved.Load() != 1 {
		t.Fatalf("concurrent heartbeat checks reserved %d builds", reserved.Load())
	}
	if !w.processScheduled || w.forceNext {
		t.Fatal("reservation must queue one build without prematurely consuming a force request")
	}
}

func TestBackgroundWorkerLoadRetryRejectsStaleAttempt(t *testing.T) {
	for _, change := range []string{"generation", "cancelled_loop", "replaced_loop", "success", "new_failure", "stopped", "processing", "recovering"} {
		t.Run(change, func(t *testing.T) {
			w := loadRetryWorker(t)
			now := time.Now()
			loadRetryFailure(w, now.Add(-time.Minute), 1)
			attempt := w.reserveLoadRetry(w.loopCtx, now)
			if attempt == nil {
				t.Fatal("missing retry reservation")
			}
			switch change {
			case "generation":
				w.generation++
			case "cancelled_loop":
				w.loopCancel()
			case "replaced_loop":
				w.loopCtx = context.Background()
			case "success":
				w.lastError = nil
				w.errorCount = 0
			case "new_failure":
				loadRetryFailure(w, now, 2)
			case "stopped":
				w.state = WorkerStopped
			case "processing":
				w.state = WorkerProcessing
			case "recovering":
				w.recovering = true
			}
			// A newer manual request owns these flags. Old retry entry must not
			// clear processScheduled before discovering its generation is stale.
			w.forceNext = true
			beforeError, beforeState := w.lastError, w.state
			called := false
			w.processSnapshotAttempt(func(bool) snapshotBuildResult {
				called = true
				return snapshotBuildResult{clearError: true}
			}, attempt)
			if called || !w.processScheduled || !w.forceNext || w.lastError != beforeError || w.state != beforeState {
				t.Fatal("stale retry crossed a result/lifecycle boundary")
			}
		})
	}
}

func TestBackgroundWorkerLoadRetryFailureThenSameHashSuccess(t *testing.T) {
	w := loadRetryWorker(t)
	old := &DataSnapshot{DataHash: "same-committed-data"}
	w.snapshot, w.lastHash = old, old.DataHash
	now := time.Now()
	loadRetryFailure(w, now.Add(-time.Minute), 1)
	attempt := w.reserveLoadRetry(w.loopCtx, now)
	if attempt == nil {
		t.Fatal("missing first retry")
	}
	secondFailure := &WorkerError{Phase: "load", Cause: errors.New("still busy"), Time: now}
	w.processSnapshotAttempt(func(force bool) snapshotBuildResult {
		if !force {
			t.Error("recovery must bypass hash dedup")
		}
		return snapshotBuildResult{err: secondFailure}
	}, attempt)
	if w.snapshot != old || w.lastHash != old.DataHash || w.lastError != secondFailure || w.errorCount != 2 {
		t.Fatal("failed retry replaced the last good snapshot or cleared the failure")
	}
	if got := w.reserveLoadRetry(w.loopCtx, now.Add(10*time.Second-time.Nanosecond)); got != nil {
		t.Fatal("repeated failure did not increase backoff")
	}
	select {
	case msg := <-w.msgCh:
		if _, ok := msg.(SnapshotErrorMsg); !ok {
			t.Fatalf("expected visible retry error, got %T", msg)
		}
	default:
		t.Fatal("failed retry was hidden")
	}
	attempt = w.reserveLoadRetry(w.loopCtx, now.Add(10*time.Second))
	if attempt == nil {
		t.Fatal("missing second retry")
	}
	fresh := &DataSnapshot{DataHash: old.DataHash}
	w.processSnapshotAttempt(func(force bool) snapshotBuildResult {
		if !force {
			// Match the real builder's unchanged-content outcome. Returning
			// only clearError would never notify the TUI that it recovered.
			return snapshotBuildResult{clearError: true}
		}
		return snapshotBuildResult{snapshot: fresh, clearError: true}
	}, attempt)
	if w.snapshot != fresh || w.lastError != nil || w.errorCount != 0 || w.processScheduled || w.forceNext {
		t.Fatal("successful retry failed to reset error/backoff/scheduling state")
	}
	select {
	case msg := <-w.msgCh:
		ready, ok := msg.(SnapshotReadyMsg)
		if !ok || ready.Snapshot != fresh || ready.SnapshotVer != 1 {
			t.Fatalf("same-hash recovery must publish a new ready snapshot: %#v", msg)
		}
	default:
		t.Fatal("same-hash recovery was silently deduplicated")
	}
	if w.reserveLoadRetry(w.loopCtx, now.Add(time.Hour)) != nil {
		t.Fatal("success left a retry scheduled")
	}
	loadRetryFailure(w, now, 1)
	if w.reserveLoadRetry(w.loopCtx, now.Add(5*time.Second)) == nil {
		t.Fatal("a later independent failure retained the old backoff")
	}
}

// No filesystem watcher is started. This isolates the missing-event case:
// after one accepted load error the process loop must recover using its
// heartbeat alone, rereading the identical real JSONL file through the normal
// snapshot builder. No file change, manual TriggerRefresh or ForceRefresh may
// rescue the test. Backoff boundary coverage above uses an explicit clock;
// aging the error here avoids adding a five-second sleep to every suite run.
func TestBackgroundWorkerHeartbeatRetriesWithoutFileEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	data := []byte("{\"id\":\"retry-1\",\"title\":\"Still committed\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":2}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w := loadRetryWorker(t)
	w.beadsPath = path
	var err error
	w.watcher, err = watcher.NewWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	w.process()
	old := w.GetSnapshot()
	if old == nil || len(old.Issues) != 1 {
		t.Fatal("normal snapshot builder did not load the initial fixture")
	}
	w.processWithSnapshotBuilder(func(bool) snapshotBuildResult {
		return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: errors.New("transient read failure"), Time: time.Now().Add(-time.Minute)}}
	})
	for len(w.msgCh) != 0 {
		<-w.msgCh
	}
	done := make(chan struct{})
	go w.processLoop(w.loopCtx, done)
	defer func() {
		w.loopCancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("retry process loop failed to stop")
		}
	}()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-w.msgCh:
			ready, ok := msg.(SnapshotReadyMsg)
			if !ok {
				continue
			}
			if ready.Snapshot == old || ready.Snapshot.DataHash != old.DataHash || len(ready.Snapshot.Issues) != 1 {
				t.Fatal("retry did not publish the unchanged committed data afresh")
			}
			if w.LastError() != nil {
				t.Fatal("successful heartbeat retry retained its error")
			}
			if current, err := os.ReadFile(path); err != nil || string(current) != string(data) {
				t.Fatal("retry modified the selected source")
			}
			if w.watcher.IsStarted() {
				t.Fatal("fixture accidentally permitted filesystem notifications")
			}
			return
		case <-deadline.C:
			t.Fatal("a healthy heartbeat left the failed load stale without a file event")
		}
	}
}

// A file notification can arrive before the retry backoff expires. Its normal
// (non-forced) refresh must still publish recovery for unchanged data, otherwise
// it clears lastError silently and also removes the heartbeat's retry reason.
func TestBackgroundWorkerEventRecoversSameHashBeforeHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	data := []byte("{\"id\":\"retry-1\",\"title\":\"Still committed\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":2}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w := loadRetryWorker(t)
	w.beadsPath = path
	w.process()
	old := w.GetSnapshot()
	if old == nil || len(old.Issues) != 1 {
		t.Fatal("normal snapshot builder did not load the initial fixture")
	}
	now := time.Now()
	w.processWithSnapshotBuilder(func(bool) snapshotBuildResult {
		return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: errors.New("transient read failure"), Time: now}}
	})
	if w.reserveLoadRetry(w.loopCtx, now) != nil {
		t.Fatal("fixture must recover before an automatic retry becomes eligible")
	}
	for len(w.msgCh) != 0 {
		<-w.msgCh
	}
	w.TriggerRefresh() // same entry point as a file event, not ForceRefresh
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-w.msgCh:
			ready, ok := msg.(SnapshotReadyMsg)
			if !ok {
				continue
			}
			if ready.Snapshot == old || ready.Snapshot.DataHash != old.DataHash || len(ready.Snapshot.Issues) != 1 {
				t.Fatal("event recovery did not republish the unchanged committed data")
			}
			if w.LastError() != nil {
				t.Fatal("successful event refresh retained its error")
			}
			if current, err := os.ReadFile(path); err != nil || string(current) != string(data) {
				t.Fatal("recovery modified the selected source")
			}
			return
		case <-deadline.C:
			t.Fatal("event recovery silently cleared the load failure without notifying the UI")
		}
	}
}

// Exercise a real SQLite lock, not a synthetic WorkerError. The file watcher
// never starts: releasing contention must restore the live WAL snapshot via
// the heartbeat alone, without another file event or a JSONL fallback.
func TestBackgroundWorkerHeartbeatRecoversSQLiteAfterLockRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "beads.db")
	t.Setenv("BEADS_DIR", dir)
	t.Setenv("BEADS_DB", path)
	t.Setenv("BD_DB", path)
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	const title = "WAL-only load retry sentinel"
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL;
PRAGMA wal_autocheckpoint=0;
CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER, issue_type TEXT);
INSERT INTO issues VALUES ('wal-retry-1', 'WAL-only load retry sentinel', 'open', 2, 'task');`); err != nil {
		t.Fatal(err)
	}
	w := loadRetryWorker(t)
	w.beadsPath = path
	w.watcher, err = watcher.NewWatcher(path)
	if err != nil {
		t.Fatal(err)
	}
	w.process()
	old := w.GetSnapshot()
	if old == nil || len(old.Issues) != 1 || old.Issues[0].Title != title {
		t.Fatal("initial live SQLite snapshot was not loaded")
	}
	protected := func() [2][]byte {
		t.Helper()
		var result [2][]byte
		for i, suffix := range []string{"", "-wal"} {
			var err error
			result[i], err = os.ReadFile(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	before := protected()
	if len(before[1]) <= 32 || bytes.Contains(before[0], []byte(title)) || !bytes.Contains(before[1], []byte(title)) {
		t.Fatal("fixture must retain the committed issue only in WAL, not the main file")
	}
	unchanged := func(stage string) {
		t.Helper()
		after := protected()
		if !bytes.Equal(before[0], after[0]) || !bytes.Equal(before[1], after[1]) {
			t.Fatalf("%s changed protected main/WAL bytes", stage)
		}
	}
	if _, err := writer.Exec("PRAGMA locking_mode=EXCLUSIVE; BEGIN EXCLUSIVE; COMMIT;"); err != nil {
		t.Fatal(err)
	}
	// Use the production reader's normal busy timeout. Both the count probe
	// and the load may time out; the accepted error must be a real load error.
	w.process()
	failure := w.LastError()
	if failure == nil || failure.Phase != "load" ||
		(!strings.Contains(strings.ToLower(failure.Error()), "locked") && !strings.Contains(strings.ToLower(failure.Error()), "busy")) {
		t.Fatalf("SQLite contention did not produce a real load failure: %v", failure)
	}
	if w.GetSnapshot() != old || w.LastHash() != old.DataHash {
		t.Fatal("failed SQLite read replaced the last usable snapshot")
	}
	unchanged("failed load")
	if _, err := writer.Exec("PRAGMA locking_mode=NORMAL"); err != nil {
		t.Fatal(err)
	}
	// The pragma alone does not release a retained EXCLUSIVE lock. A finished
	// read on this same connection releases it without changing main/WAL bytes.
	var count int
	if err := writer.QueryRow("SELECT count(*) FROM issues").Scan(&count); err != nil || count != 1 {
		t.Fatalf("releasing SQLite lock: count=%d, %v", count, err)
	}
	unchanged("lock release")
	// Backoff boundaries have a separate exact-clock test. Age this genuine
	// accepted error to avoid another five-second sleep; do not trigger a read.
	w.mu.Lock()
	w.lastError.Time = time.Now().Add(-time.Minute)
	w.mu.Unlock()
	for len(w.msgCh) != 0 {
		<-w.msgCh
	}
	done := make(chan struct{})
	go w.processLoop(w.loopCtx, done)
	defer func() {
		w.loopCancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SQLite retry loop failed to stop")
		}
	}()
	waitForSnapshot := func() *DataSnapshot {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case msg := <-w.msgCh:
				if ready, ok := msg.(SnapshotReadyMsg); ok {
					return ready.Snapshot
				}
			case <-timer.C:
				t.Fatalf("SQLite retry did not publish a snapshot: %v", w.LastError())
			}
		}
	}
	fresh := waitForSnapshot()
	if fresh == nil || fresh == old || fresh.DataHash != old.DataHash ||
		len(fresh.Issues) != 1 || fresh.Issues[0].Title != title || w.LastError() != nil {
		t.Fatal("heartbeat failed to republish the unchanged committed SQLite issue")
	}
	if w.watcher.IsStarted() {
		t.Fatal("fixture accidentally enabled filesystem notifications")
	}
	unchanged("heartbeat recovery")
	// Later writes must remain visible through the same worker. This explicit
	// refresh happens only AFTER the missing-event recovery has been proved.
	if _, err := writer.Exec("INSERT INTO issues VALUES ('wal-retry-2', 'Later committed issue', 'open', 2, 'task')"); err != nil {
		t.Fatal(err)
	}
	w.TriggerRefresh()
	later := waitForSnapshot()
	if later == nil || len(later.Issues) != 2 || later.DataHash == fresh.DataHash {
		t.Fatal("subsequent SQLite commit was not visible after recovery")
	}
	var integrity string
	if err := writer.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity after recovery: %q, %v", integrity, err)
	}
	var busy, frames, checkpointed int
	if err := writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("recovered worker retained a reader mark: busy=%d frames=%d backfill=%d, %v", busy, frames, checkpointed, err)
	}
}
