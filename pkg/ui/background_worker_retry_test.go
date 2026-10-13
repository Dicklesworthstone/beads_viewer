package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
