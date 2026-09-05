package ui

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

func TestBackgroundWorker_NewWithoutPath(t *testing.T) {
	cfg := WorkerConfig{
		BeadsPath: "",
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if worker.State() != WorkerIdle {
		t.Errorf("Expected idle state, got %v", worker.State())
	}

	if worker.GetSnapshot() != nil {
		t.Error("Expected nil snapshot initially")
	}
}

func TestBackgroundWorker_NewWithoutPath_EnvDefaults(t *testing.T) {
	t.Setenv("BV_DEBOUNCE_MS", "123")
	t.Setenv("BV_HEARTBEAT_INTERVAL_S", "9")
	t.Setenv("BV_WATCHDOG_INTERVAL_S", "11")

	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: ""})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if worker.debounceDelay != 123*time.Millisecond {
		t.Errorf("debounceDelay=%v, want %v", worker.debounceDelay, 123*time.Millisecond)
	}
	if cap(worker.msgCh) != backgroundWorkerMessageBuffer {
		t.Errorf("cap(msgCh)=%d, want authoritative mailbox size %d", cap(worker.msgCh), backgroundWorkerMessageBuffer)
	}
	if worker.heartbeatInterval != 9*time.Second {
		t.Errorf("heartbeatInterval=%v, want %v", worker.heartbeatInterval, 9*time.Second)
	}
	if worker.watchdogInterval != 11*time.Second {
		t.Errorf("watchdogInterval=%v, want %v", worker.watchdogInterval, 11*time.Second)
	}
}

func TestEnvMaxLineSizeBytes(t *testing.T) {
	t.Setenv("BV_MAX_LINE_SIZE_MB", "12")
	if got := envMaxLineSizeBytes(); got != 12*1024*1024 {
		t.Errorf("envMaxLineSizeBytes()=%d, want %d", got, 12*1024*1024)
	}

	t.Setenv("BV_MAX_LINE_SIZE_MB", "-1")
	if got := envMaxLineSizeBytes(); got != 0 {
		t.Errorf("envMaxLineSizeBytes() with invalid env=%d, want %d", got, 0)
	}
}

func TestBackgroundWorker_NewWithPath(t *testing.T) {
	// Create a temporary beads file
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	// Write a valid beads file
	content := `{"id":"test-1","title":"Test Issue","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if worker.State() != WorkerIdle {
		t.Errorf("Expected idle state, got %v", worker.State())
	}
}

func TestBackgroundWorker_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should be idempotent
	worker.Stop()
	worker.Stop() // Should not panic

	if worker.State() != WorkerStopped {
		t.Errorf("Expected stopped state, got %v", worker.State())
	}
}

func TestBackgroundWorker_StopCompletesWithinOneSecondWhenLoopStuck(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	// Simulate an unresponsive processing loop. Stop must still honor its public
	// sub-second shutdown contract rather than waiting indefinitely.
	worker.mu.Lock()
	worker.started = true
	worker.done = make(chan struct{})
	worker.mu.Unlock()

	start := time.Now()
	worker.Stop()
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Stop took %v; want less than 1s", elapsed)
	}
}

func TestBackgroundWorker_StopReturnsSnapshotPooledIssues(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")
	if err := os.WriteFile(beadsPath, []byte(`{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: beadsPath})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	pooled := loader.GetIssue()
	pooled.ID = "pooled-1"
	pooled.Labels = append(pooled.Labels, "backend")
	worker.snapshot = &DataSnapshot{
		Issues:           []model.Issue{{ID: "test-1", Title: "Test", Status: model.StatusOpen}},
		pooledIssues:     newPooledIssueLease([]*model.Issue{pooled}),
		CreatedAt:        time.Now(),
		phase2Ready:      true,
		LoadWarningCount: 0,
	}

	worker.Stop()

	if pooled.ID != "" {
		t.Fatalf("expected pooled issue to be reset on Stop, got ID %q", pooled.ID)
	}
	if len(pooled.Labels) != 0 {
		t.Fatalf("expected pooled issue labels to be cleared on Stop, got %v", pooled.Labels)
	}
	if worker.snapshot != nil {
		t.Fatal("expected worker snapshot to be cleared on Stop")
	}
}

func TestModelStopReturnsSnapshotPooledIssuesWithoutWorker(t *testing.T) {
	pooled := loader.GetIssue()
	pooled.ID = "pooled-model"
	pooled.Comments = append(pooled.Comments, &model.Comment{ID: "1", Text: "hello"})

	m := Model{
		snapshot: &DataSnapshot{
			Issues:       []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen}},
			pooledIssues: newPooledIssueLease([]*model.Issue{pooled}),
		},
	}

	m.Stop()

	if pooled.ID != "" {
		t.Fatalf("expected pooled issue to be reset on Model.Stop, got ID %q", pooled.ID)
	}
	if len(pooled.Comments) != 0 {
		t.Fatalf("expected pooled issue comments to be cleared on Model.Stop, got %d", len(pooled.Comments))
	}
	if m.snapshot == nil || m.snapshot.hasPooledIssues() {
		t.Fatal("expected snapshot pooled refs to be cleared on Model.Stop")
	}
}

func TestModelStopReleasesSharedPhase2PoolLeaseOnce(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")
	if err := os.WriteFile(beadsPath, []byte(`{"id":"A","title":"Issue A","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write test issues: %v", err)
	}
	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: beadsPath})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	pooled := loader.GetIssue()
	pooled.ID = "shared-phase2"
	var releases atomic.Int32
	phase1 := NewSnapshotBuilder([]model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, IssueType: model.TypeTask}}).Build()
	phase1.pooledIssues = &pooledIssueLease{
		refs: []*model.Issue{pooled},
		release: func(refs []*model.Issue) {
			releases.Add(1)
			loader.ReturnIssuePtrsToPool(refs)
		},
	}
	phase2 := phase1.WithPhase2(phase1.Analysis, phase1.GetInsights(), phase1.Issues, phase1.Analyzer)
	if phase2.pooledIssues != phase1.pooledIssues {
		t.Fatal("expected Phase 2 snapshot to share the Phase 1 pool lease")
	}

	worker.snapshot = phase1
	m := Model{backgroundWorker: worker, snapshot: phase2}
	m.Stop()

	if got := releases.Load(); got != 1 {
		t.Fatalf("pool release count=%d, want exactly 1", got)
	}
	if phase1.hasPooledIssues() || phase2.hasPooledIssues() {
		t.Fatal("expected shared pool lease to be inactive after Stop")
	}
}

func TestBackgroundWorkerSendReleasesDroppedSupersededSnapshotLease(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	var staleReleases atomic.Int32
	stale := &DataSnapshot{
		pooledIssues: &pooledIssueLease{
			refs: []*model.Issue{{ID: "pooled-stale"}},
			release: func([]*model.Issue) {
				staleReleases.Add(1)
			},
		},
	}
	var currentReleases atomic.Int32
	current := &DataSnapshot{
		pooledIssues: &pooledIssueLease{
			refs: []*model.Issue{{ID: "pooled-current"}},
			release: func([]*model.Issue) {
				currentReleases.Add(1)
			},
		},
	}

	worker.mu.Lock()
	worker.snapshot = current
	worker.mu.Unlock()
	worker.msgCh <- SnapshotReadyMsg{Snapshot: stale, SnapshotVer: 1}
	worker.send(SnapshotReadyMsg{Snapshot: current, SnapshotVer: 2})

	if got := staleReleases.Load(); got != 1 {
		t.Fatalf("dropped stale snapshot release count=%d, want 1", got)
	}
	if stale.hasPooledIssues() {
		t.Fatal("dropped stale snapshot retained an active pooled lease")
	}
	if got := currentReleases.Load(); got != 0 {
		t.Fatalf("current snapshot was released while still worker-owned: count=%d", got)
	}
	if !current.hasPooledIssues() {
		t.Fatal("current snapshot lease became inactive while still worker-owned")
	}

	queued := <-worker.msgCh
	ready, ok := queued.(SnapshotReadyMsg)
	if !ok || ready.Snapshot != current || ready.SnapshotVer != 2 {
		t.Fatalf("queued message=%#v, want current SnapshotReadyMsg version 2", queued)
	}
}

func TestBackgroundWorkerSendDoesNotLetPhase2EvictSnapshotReady(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	snapshot := &DataSnapshot{DataHash: "current"}
	ready := SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 7}
	worker.msgCh <- ready
	worker.send(Phase2UpdateMsg{
		DataHash:    snapshot.DataHash,
		Snapshot:    snapshot,
		SnapshotVer: ready.SnapshotVer,
	})

	queued := <-worker.msgCh
	got, ok := queued.(SnapshotReadyMsg)
	if !ok || got.Snapshot != snapshot || got.SnapshotVer != ready.SnapshotVer {
		t.Fatalf("queued message=%#v, want authoritative SnapshotReadyMsg", queued)
	}
	select {
	case unexpected := <-worker.msgCh:
		t.Fatalf("full channel unexpectedly retained optional Phase2 message: %#v", unexpected)
	default:
	}
}

func TestBackgroundWorkerSendDoesNotLetErrorEvictSnapshotReady(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	generation := worker.Generation()
	snapshot := &DataSnapshot{DataHash: "current"}
	ready := SnapshotReadyMsg{
		Snapshot:         snapshot,
		SnapshotVer:      7,
		WorkerGeneration: generation,
	}
	worker.msgCh <- ready
	worker.send(SnapshotErrorMsg{
		Err:              errors.New("recoverable reload error"),
		Recoverable:      true,
		WorkerGeneration: generation,
	})

	queued := <-worker.msgCh
	got, ok := queued.(SnapshotReadyMsg)
	if !ok || got.Snapshot != snapshot || got.SnapshotVer != ready.SnapshotVer {
		t.Fatalf("queued message=%#v, want authoritative SnapshotReadyMsg", queued)
	}
	select {
	case unexpected := <-worker.msgCh:
		t.Fatalf("full channel unexpectedly retained lower-priority error: %#v", unexpected)
	default:
	}
}

func TestBackgroundWorkerSendLetsTerminalErrorEvictSnapshotReady(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	generation := worker.Generation()
	worker.msgCh <- SnapshotReadyMsg{
		Snapshot:         &DataSnapshot{DataHash: "last-usable"},
		SnapshotVer:      7,
		WorkerGeneration: generation,
	}
	terminal := SnapshotErrorMsg{
		Err:              errors.New("worker stopped permanently"),
		Recoverable:      false,
		WorkerGeneration: generation,
	}
	worker.send(terminal)

	queued := <-worker.msgCh
	got, ok := queued.(SnapshotErrorMsg)
	if !ok || got.Recoverable || got.Err == nil || got.Err.Error() != terminal.Err.Error() {
		t.Fatalf("queued message=%#v, want terminal SnapshotErrorMsg", queued)
	}
}

func TestBackgroundWorkerSendLetsSnapshotReadyReplaceError(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	generation := worker.Generation()
	worker.msgCh <- SnapshotErrorMsg{
		Err:              errors.New("older reload error"),
		Recoverable:      true,
		WorkerGeneration: generation,
	}
	snapshot := &DataSnapshot{DataHash: "recovered"}
	worker.send(SnapshotReadyMsg{
		Snapshot:         snapshot,
		SnapshotVer:      8,
		WorkerGeneration: generation,
	})

	queued := <-worker.msgCh
	got, ok := queued.(SnapshotReadyMsg)
	if !ok || got.Snapshot != snapshot || got.SnapshotVer != 8 {
		t.Fatalf("queued message=%#v, want replacement SnapshotReadyMsg", queued)
	}
}

func TestBackgroundWorkerSendDropsStaleGenerationError(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	staleGeneration := worker.Generation()
	mutateWorkerForTest(worker, func() {
		worker.generation++
	})
	worker.send(SnapshotErrorMsg{
		Err:              errors.New("stale reload error"),
		Recoverable:      true,
		WorkerGeneration: staleGeneration,
	})

	select {
	case unexpected := <-worker.msgCh:
		t.Fatalf("stale worker message was queued: %#v", unexpected)
	default:
	}
}

func TestBackgroundWorkerProcessDiscardsInvalidatedBuildError(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	previous := &WorkerError{Phase: "previous", Cause: errors.New("previous error"), Time: time.Now()}
	worker.recordError(previous)

	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	processDone := make(chan struct{})
	staleErr := &WorkerError{Phase: "load", Cause: errors.New("stale load error"), Time: time.Now()}
	go func() {
		worker.processWithSnapshotBuilder(func(bool) snapshotBuildResult {
			close(buildStarted)
			<-releaseBuild
			return snapshotBuildResult{err: staleErr}
		})
		close(processDone)
	}()

	<-buildStarted
	mutateWorkerForTest(worker, func() {
		if worker.state != WorkerProcessing {
			t.Fatalf("worker state=%v, want processing before invalidation", worker.state)
		}
		worker.generation++
		worker.state = WorkerIdle
		worker.processingStart = time.Time{}
	})
	close(releaseBuild)
	<-processDone

	if got := worker.LastError(); got != previous {
		t.Fatalf("stale build changed LastError: got %v, want previous error", got)
	}
	if staleErr.Retries != 0 {
		t.Fatalf("stale build error retry count=%d, want untouched", staleErr.Retries)
	}
	select {
	case unexpected := <-worker.msgCh:
		t.Fatalf("stale build published a worker message: %#v", unexpected)
	default:
	}
}

func TestBackgroundWorkerProcessPublishesAcceptedErrorGeneration(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()
	worker.beadsPath = filepath.Join(t.TempDir(), "missing.jsonl")

	worker.process()

	if worker.LastError() == nil {
		t.Fatal("accepted build error did not update LastError")
	}
	select {
	case queued := <-worker.msgCh:
		msg, ok := queued.(SnapshotErrorMsg)
		if !ok {
			t.Fatalf("queued message=%#v, want SnapshotErrorMsg", queued)
		}
		if msg.WorkerGeneration != worker.Generation() {
			t.Fatalf("error generation=%d, want current generation %d", msg.WorkerGeneration, worker.Generation())
		}
		if msg.Err == nil || !msg.Recoverable {
			t.Fatalf("error message=%#v, want recoverable load error", msg)
		}
	default:
		t.Fatal("accepted build error did not publish SnapshotErrorMsg")
	}
}

func TestBackgroundWorker_TriggerRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// Trigger refresh and wait for processing
	worker.TriggerRefresh()

	// Wait for processing to complete
	time.Sleep(200 * time.Millisecond)

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot after refresh")
	}

	if len(snapshot.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(snapshot.Issues))
	}
}

func TestBackgroundWorker_RefreshRequestMsg(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")
	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: beadsPath})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.HandleRefreshRequest(RefreshRequestMsg{Force: true})
	waitForSnapshotVersion(t, worker, 1)
	first := worker.GetSnapshot()
	firstMsg := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(msg tea.Msg) bool {
		_, ok := msg.(SnapshotReadyMsg)
		return ok
	}).(SnapshotReadyMsg)
	if firstMsg.Snapshot != first {
		t.Fatal("RefreshRequestMsg snapshot was not delivered through worker message channel")
	}
	if firstMsg.WorkerGeneration != worker.Generation() {
		t.Fatalf("SnapshotReadyMsg generation=%d, want %d", firstMsg.WorkerGeneration, worker.Generation())
	}

	worker.HandleRefreshRequest(RefreshRequestMsg{Force: true})
	waitForSnapshotVersion(t, worker, 2)
	if second := worker.GetSnapshot(); second == first {
		t.Fatal("forced RefreshRequestMsg was deduplicated")
	}

	r := &recipe.Recipe{Name: "message-flow"}
	worker.HandleRefreshRequest(RefreshRequestMsg{Recipe: r})
	waitForSnapshotVersion(t, worker, 3)

	worker.mu.RLock()
	gotRecipe := worker.currentRecipe
	worker.mu.RUnlock()
	if gotRecipe != r {
		t.Fatal("RefreshRequestMsg recipe was not applied by worker")
	}
}

func TestBackgroundWorker_WatcherChanged(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	ch := worker.WatcherChanged()
	if ch == nil {
		t.Error("WatcherChanged should return non-nil channel")
	}
}

func TestBackgroundWorker_WatcherChangedNil(t *testing.T) {
	// Worker without path should have nil watcher
	cfg := WorkerConfig{
		BeadsPath: "",
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if worker.WatcherChanged() != nil {
		t.Error("WatcherChanged should return nil when no watcher")
	}
}

func TestWorkerState_String(t *testing.T) {
	tests := []struct {
		state    WorkerState
		expected string
	}{
		{WorkerIdle, "0"},
		{WorkerProcessing, "1"},
		{WorkerStopped, "2"},
	}

	for _, tt := range tests {
		// Just verify the states have distinct values
		if int(tt.state) < 0 || int(tt.state) > 2 {
			t.Errorf("Unexpected state value: %v", tt.state)
		}
	}
}

func TestBackgroundWorker_ContentHashDedup(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test Issue","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// First refresh should build snapshot and set hash
	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 1)
	waitForWorkerIdle(t, worker, 1)

	snapshot1 := worker.GetSnapshot()
	if snapshot1 == nil {
		t.Fatal("Expected snapshot after first refresh")
	}

	hash1 := worker.LastHash()
	if hash1 == "" {
		t.Error("Expected non-empty hash after first refresh")
	}

	// Second refresh with same content should be deduped (snapshot unchanged)
	worker.TriggerRefresh()
	waitForWorkerIdle(t, worker, 2)

	snapshot2 := worker.GetSnapshot()
	hash2 := worker.LastHash()

	// Hash should be the same
	if hash1 != hash2 {
		t.Errorf("Hash changed unexpectedly: %s -> %s", hash1, hash2)
	}

	// Snapshot pointer should be unchanged (deduped)
	if snapshot1 != snapshot2 {
		t.Error("Snapshot pointer changed when content was unchanged - dedup failed")
	}
}

func TestBackgroundWorker_ContentHashChanges(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content1 := `{"id":"test-1","title":"Test Issue","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content1), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// First refresh
	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 1)
	waitForWorkerIdle(t, worker, 1)

	snapshot1 := worker.GetSnapshot()
	if snapshot1 == nil {
		t.Fatal("Expected snapshot after first refresh")
	}
	hash1 := worker.LastHash()

	// Modify the file content
	content2 := `{"id":"test-1","title":"Updated Title","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write modified file: %v", err)
	}

	// Second refresh with different content should rebuild
	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 2)
	waitForWorkerIdle(t, worker, 2)

	snapshot2 := worker.GetSnapshot()
	if snapshot2 == nil {
		t.Fatal("Expected snapshot after second refresh")
	}
	hash2 := worker.LastHash()

	// Hash should be different
	if hash1 == hash2 {
		t.Error("Hash should have changed when content changed")
	}

	// Snapshot should be different
	if snapshot1 == snapshot2 {
		t.Error("Snapshot pointer should have changed when content changed")
	}

	// New snapshot should have updated title
	if snapshot2.Issues[0].Title != "Updated Title" {
		t.Errorf("Expected updated title, got %q", snapshot2.Issues[0].Title)
	}
}

func TestBackgroundWorker_MetricsSnapshot(t *testing.T) {
	t.Setenv("BV_WORKER_METRICS", "1")

	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")
	content := strings.Join([]string{
		`{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}`,
		`{"id":"test-2","title":"Test 2","status":"open","priority":2,"issue_type":"feature"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if worker.GetSnapshot() != nil && worker.Metrics().SnapshotVersion > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if worker.GetSnapshot() == nil {
		t.Fatal("Expected snapshot after refresh")
	}

	metrics := worker.Metrics()
	if metrics.ProcessingCount == 0 {
		t.Fatalf("expected ProcessingCount > 0, got %d", metrics.ProcessingCount)
	}
	if metrics.SnapshotVersion == 0 {
		t.Fatalf("expected SnapshotVersion > 0")
	}
	if metrics.LastSnapshotReadyAt.IsZero() {
		t.Fatal("expected LastSnapshotReadyAt to be set")
	}
	if metrics.SnapshotSizeBytes <= 0 {
		t.Fatalf("expected SnapshotSizeBytes > 0, got %d", metrics.SnapshotSizeBytes)
	}
}

func TestBackgroundWorker_IncrementalListMetrics(t *testing.T) {
	t.Setenv("BV_WORKER_METRICS", "1")

	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	var builder strings.Builder
	for i := 0; i < 10; i++ {
		builder.WriteString(fmt.Sprintf(
			`{"id":"issue-%d","title":"Issue %d","status":"open","priority":%d,"issue_type":"task"}`+"\n",
			i, i, i,
		))
	}
	if err := os.WriteFile(beadsPath, []byte(builder.String()), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 1)

	if snap := worker.GetSnapshot(); snap == nil {
		t.Fatal("Expected snapshot after first refresh")
	} else if snap.IncrementalListUsed {
		t.Fatalf("expected first snapshot to be full rebuild")
	}

	updated := builder.String()
	updated = strings.Replace(updated, `"title":"Issue 0"`, `"title":"Issue 0 updated"`, 1)
	if err := os.WriteFile(beadsPath, []byte(updated), 0644); err != nil {
		t.Fatalf("Failed to write modified file: %v", err)
	}

	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 2)

	snap2 := worker.GetSnapshot()
	if snap2 == nil {
		t.Fatal("Expected snapshot after second refresh")
	}
	if !snap2.IncrementalListUsed {
		t.Fatalf("expected incremental list path on second snapshot")
	}

	metrics := worker.Metrics()
	if metrics.IncrementalListCount != 1 {
		t.Fatalf("IncrementalListCount=%d, want 1", metrics.IncrementalListCount)
	}
	if metrics.FullListCount != 1 {
		t.Fatalf("FullListCount=%d, want 1", metrics.FullListCount)
	}
	if metrics.IncrementalListRatio != 0.5 {
		t.Fatalf("IncrementalListRatio=%f, want 0.5", metrics.IncrementalListRatio)
	}
}

func TestBackgroundWorker_LargeDatasetWarning(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	const issueCount = 5000
	f, err := os.Create(beadsPath)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	writer := bufio.NewWriter(f)
	for i := 0; i < issueCount; i++ {
		line := fmt.Sprintf(`{"id":"issue-%d","title":"Issue %d","status":"open","priority":1,"issue_type":"task"}`+"\n", i, i)
		if _, err := writer.WriteString(line); err != nil {
			_ = f.Close()
			t.Fatalf("Failed to write test file: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = f.Close()
		t.Fatalf("Failed to flush test file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Failed to close test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if worker.GetSnapshot() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot after refresh")
	}
	if snapshot.DatasetTier != datasetTierLarge {
		t.Fatalf("expected datasetTierLarge, got %v", snapshot.DatasetTier)
	}
	if snapshot.SourceIssueCountHint != issueCount {
		t.Fatalf("expected SourceIssueCountHint=%d, got %d", issueCount, snapshot.SourceIssueCountHint)
	}
	if snapshot.LoadedOpenOnly {
		t.Fatalf("expected LoadedOpenOnly=false for large tier")
	}
	if snapshot.TruncatedCount != 0 {
		t.Fatalf("expected TruncatedCount=0, got %d", snapshot.TruncatedCount)
	}
	if snapshot.LargeDatasetWarning == "" {
		t.Fatal("expected LargeDatasetWarning to be populated")
	}
}

func TestBackgroundWorker_HugeDatasetOpenOnly(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	const issueCount = 20000
	f, err := os.Create(beadsPath)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	writer := bufio.NewWriter(f)
	openCount := 0
	for i := 0; i < issueCount; i++ {
		status := "open"
		if i%2 == 0 {
			status = "closed"
		} else {
			openCount++
		}
		if i == 2 {
			status = "tombstone"
		}
		dependencies := ""
		if i == 1 {
			dependencies = `,"dependencies":[{"depends_on_id":"issue-0","type":"blocks"},{"depends_on_id":"issue-2","type":"blocks"}]`
		} else if i == 3 {
			dependencies = `,"dependencies":[{"depends_on_id":"absent","type":"blocks"}]`
		}
		line := fmt.Sprintf(`{"id":"issue-%d","title":"Issue %d","status":"%s","priority":1,"issue_type":"task"%s}`+"\n", i, i, status, dependencies)
		if _, err := writer.WriteString(line); err != nil {
			_ = f.Close()
			t.Fatalf("Failed to write test file: %v", err)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = f.Close()
		t.Fatalf("Failed to flush test file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Failed to close test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if worker.GetSnapshot() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot after refresh")
	}
	if snapshot.DatasetTier != datasetTierHuge {
		t.Fatalf("expected datasetTierHuge, got %v", snapshot.DatasetTier)
	}
	if snapshot.SourceIssueCountHint != issueCount {
		t.Fatalf("expected SourceIssueCountHint=%d, got %d", issueCount, snapshot.SourceIssueCountHint)
	}
	if !snapshot.LoadedOpenOnly {
		t.Fatalf("expected LoadedOpenOnly=true for huge tier")
	}
	if len(snapshot.Issues) != openCount {
		t.Fatalf("expected %d open issues, got %d", openCount, len(snapshot.Issues))
	}
	if !snapshot.Analyzer.Readiness().Ready("issue-1", time.Now()) || snapshot.Analyzer.Readiness().Ready("issue-3", time.Now()) {
		t.Fatal("huge-tier filtering lost closed/tombstone authority or permitted an absent predecessor")
	}
	expectedTruncated := issueCount - openCount
	if snapshot.TruncatedCount != expectedTruncated {
		t.Fatalf("expected TruncatedCount=%d, got %d", expectedTruncated, snapshot.TruncatedCount)
	}
	if !strings.Contains(snapshot.LargeDatasetWarning, "open-only") {
		t.Fatalf("expected LargeDatasetWarning to mention open-only, got %q", snapshot.LargeDatasetWarning)
	}

	// Only the hidden tombstone's ID changes. Visible rows, source count,
	// warnings and tier stay identical, but issue-1's predecessor is now absent.
	content, err := os.ReadFile(beadsPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(content), `"id":"issue-2"`, `"id":"retired-2"`, 1)
	if changed == string(content) {
		t.Fatal("fixture did not contain the expected hidden tombstone")
	}
	if err := os.WriteFile(beadsPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	hiddenResult := worker.buildSnapshotResult(false)
	if hiddenResult.err != nil || hiddenResult.snapshot == nil {
		t.Fatalf("same-count authority change was lost to visible-row dedup: %+v", hiddenResult)
	}
	hiddenSnapshot := hiddenResult.snapshot
	defer hiddenSnapshot.releasePooledIssues()
	if hiddenSnapshot.DataHash != snapshot.DataHash || hiddenSnapshot.AuthorityHash == snapshot.AuthorityHash {
		t.Fatal("visible identity and full authority identity were not kept separate")
	}
	if hiddenSnapshot.SourceIssueCountHint != snapshot.SourceIssueCountHint || hiddenSnapshot.LoadWarningCount != snapshot.LoadWarningCount {
		t.Fatal("fixture changed count/warnings; it must isolate hidden authority dedup")
	}
	if hiddenSnapshot.Analyzer.Readiness().Ready("issue-1", time.Now()) {
		t.Fatal("hidden predecessor removal left stale ready work")
	}

	// Changes that affect only filtered-out rows and load diagnostics keep the
	// open-issue graph hash stable, but still require a new snapshot so the
	// source/truncation/warning metadata does not remain stale.
	appendFile, err := os.OpenFile(beadsPath, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("Failed to open huge fixture for append: %v", err)
	}
	appendWriter := bufio.NewWriter(appendFile)
	const addedClosed = 1000
	for i := 0; i < addedClosed; i++ {
		line := fmt.Sprintf(`{"id":"closed-extra-%d","title":"Closed extra %d","status":"closed","priority":1,"issue_type":"task"}`+"\n", i, i)
		if _, err := appendWriter.WriteString(line); err != nil {
			_ = appendFile.Close()
			t.Fatalf("Failed to append closed issue: %v", err)
		}
	}
	if _, err := appendWriter.WriteString("not-json\n"); err != nil {
		_ = appendFile.Close()
		t.Fatalf("Failed to append malformed line: %v", err)
	}
	if err := appendWriter.Flush(); err != nil {
		_ = appendFile.Close()
		t.Fatalf("Failed to flush appended metadata-only changes: %v", err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatalf("Failed to close appended huge fixture: %v", err)
	}

	result := worker.buildSnapshotResult(false)
	if result.err != nil {
		t.Fatalf("metadata-only rebuild failed: %v", result.err)
	}
	refreshed := result.snapshot
	if refreshed == nil {
		t.Fatal("metadata-only source changes were incorrectly deduplicated")
	}
	defer refreshed.releasePooledIssues()
	if refreshed.DataHash != snapshot.DataHash {
		t.Fatalf("filtered issue hash changed for closed/malformed-only append: %q -> %q", snapshot.DataHash, refreshed.DataHash)
	}
	if refreshed.SourceIssueCountHint != issueCount+addedClosed+1 {
		t.Fatalf("refreshed SourceIssueCountHint=%d, want %d", refreshed.SourceIssueCountHint, issueCount+addedClosed+1)
	}
	if refreshed.TruncatedCount != expectedTruncated+addedClosed+1 {
		t.Fatalf("refreshed TruncatedCount=%d, want %d", refreshed.TruncatedCount, expectedTruncated+addedClosed+1)
	}
	if refreshed.LoadWarningCount != 1 {
		t.Fatalf("refreshed LoadWarningCount=%d, want 1", refreshed.LoadWarningCount)
	}
	if refreshed.LargeDatasetWarning == snapshot.LargeDatasetWarning {
		t.Fatalf("large-dataset warning stayed stale at %q", refreshed.LargeDatasetWarning)
	}
}

func TestBackgroundWorker_ResetHash(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// First refresh
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot1 := worker.GetSnapshot()
	hash1 := worker.LastHash()
	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Reset hash
	worker.ResetHash()
	if worker.LastHash() != "" {
		t.Error("Expected empty hash after reset")
	}

	// Refresh should rebuild even though content unchanged
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot2 := worker.GetSnapshot()
	hash2 := worker.LastHash()

	// Hash should be repopulated
	if hash2 == "" {
		t.Error("Expected hash to be set after refresh")
	}

	// Should have rebuilt (new snapshot pointer)
	if snapshot1 == snapshot2 {
		t.Error("Expected new snapshot after hash reset")
	}
}

func TestBackgroundWorker_ForceRefreshBypassesDedup(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// Build initial snapshot and set hash.
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot1 := worker.GetSnapshot()
	if snapshot1 == nil {
		t.Fatal("Expected snapshot after initial refresh")
	}

	// Second refresh with same content should be deduped.
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)
	if worker.GetSnapshot() != snapshot1 {
		t.Fatal("Expected snapshot pointer to be unchanged after dedup")
	}

	// Force refresh should rebuild even though content is unchanged.
	worker.ForceRefresh()
	time.Sleep(200 * time.Millisecond)
	if worker.GetSnapshot() == snapshot1 {
		t.Fatal("Expected new snapshot after ForceRefresh")
	}
}

func TestBackgroundWorker_SetRecipe_RebuildsOnRecipeChangeWithSameName(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	waitForSnapshot := func(prev *DataSnapshot) *DataSnapshot {
		deadline := time.Now().Add(750 * time.Millisecond)
		for time.Now().Before(deadline) {
			snap := worker.GetSnapshot()
			if snap != nil && snap != prev {
				return snap
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for snapshot change (prev=%p)", prev)
		return nil
	}

	worker.TriggerRefresh()
	snap1 := waitForSnapshot(nil)

	r1 := &recipe.Recipe{
		Name: "demo",
		Filters: recipe.FilterConfig{
			Status: []string{"open"},
		},
	}
	worker.SetRecipe(r1)
	snap2 := waitForSnapshot(snap1)
	if snap2.RecipeName != "demo" {
		t.Fatalf("expected RecipeName demo, got %q", snap2.RecipeName)
	}
	if snap2.RecipeHash != recipeFingerprint(r1) {
		t.Fatalf("expected RecipeHash %q, got %q", recipeFingerprint(r1), snap2.RecipeHash)
	}

	// Same name, different filter: should still trigger a rebuild (bv-4ilb).
	r2 := &recipe.Recipe{
		Name: "demo",
		Filters: recipe.FilterConfig{
			Status: []string{"closed"},
		},
	}
	worker.SetRecipe(r2)
	snap3 := waitForSnapshot(snap2)
	if snap3.RecipeHash != recipeFingerprint(r2) {
		t.Fatalf("expected RecipeHash %q, got %q", recipeFingerprint(r2), snap3.RecipeHash)
	}
}

func TestBackgroundWorker_SnapshotHasDataHash(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot")
	}

	// Snapshot should have DataHash populated
	if snapshot.DataHash == "" {
		t.Error("Expected DataHash to be set in snapshot")
	}

	// DataHash should match LastHash
	if snapshot.DataHash != worker.LastHash() {
		t.Errorf("DataHash mismatch: snapshot=%s, worker=%s", snapshot.DataHash, worker.LastHash())
	}
}

func TestBackgroundWorker_BuildSnapshotDoesNotPublishHashBeforeSwap(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content1 := `{"id":"test-1","title":"Initial","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content1), 0644); err != nil {
		t.Fatalf("Failed to write initial test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()
	waitForSnapshotVersion(t, worker, 1)

	accepted := worker.GetSnapshot()
	if accepted == nil {
		t.Fatal("expected accepted initial snapshot")
	}
	acceptedHash := worker.LastHash()
	if acceptedHash == "" {
		t.Fatal("expected accepted initial hash")
	}

	content2 := `{"id":"test-1","title":"Changed","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write changed test file: %v", err)
	}

	mutateWorkerForTest(worker, func() {
		worker.forceNext = true
	})

	unaccepted := worker.buildSnapshot(false)
	if unaccepted == nil {
		t.Fatal("expected unaccepted changed snapshot")
	}
	defer unaccepted.releasePooledIssues()

	if unaccepted.DataHash == "" {
		t.Fatal("expected unaccepted snapshot DataHash")
	}
	if unaccepted.DataHash == acceptedHash {
		t.Fatal("expected changed snapshot hash to differ from accepted hash")
	}
	if worker.GetSnapshot() != accepted {
		t.Fatal("buildSnapshot should not swap the active snapshot")
	}
	if got := worker.LastHash(); got != acceptedHash {
		t.Fatalf("LastHash changed before snapshot swap: got %q, want %q", got, acceptedHash)
	}
	worker.mu.RLock()
	forceNext := worker.forceNext
	worker.mu.RUnlock()
	if !forceNext {
		t.Fatal("buildSnapshot consumed a force-refresh flag that belongs to the next process run")
	}
}

func TestWorkerError_String(t *testing.T) {
	err := WorkerError{
		Phase:   "load",
		Cause:   os.ErrNotExist,
		Time:    time.Now(),
		Retries: 3,
	}

	s := err.Error()
	if s == "" {
		t.Error("Error() should return non-empty string")
	}

	if !strings.Contains(s, "load") {
		t.Errorf("Error() should contain phase 'load': %s", s)
	}

	if !strings.Contains(s, "3") {
		t.Errorf("Error() should contain retry count: %s", s)
	}

	// Test Unwrap
	if err.Unwrap() != os.ErrNotExist {
		t.Error("Unwrap() should return underlying error")
	}
}

func TestBackgroundWorker_LoadError(t *testing.T) {
	// Create a worker pointing to non-existent file
	cfg := WorkerConfig{
		BeadsPath:     "/nonexistent/path/beads.jsonl",
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		// Watcher creation might fail for non-existent path, which is fine
		t.Skipf("Skipping test - watcher creation failed: %v", err)
	}
	defer worker.Stop()

	// Trigger refresh
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	// Should have no snapshot (load failed)
	if worker.GetSnapshot() != nil {
		t.Error("Expected nil snapshot when file doesn't exist")
	}

	// Should have recorded error
	lastErr := worker.LastError()
	if lastErr == nil {
		t.Error("Expected error to be recorded")
	} else {
		if lastErr.Phase != "load" {
			t.Errorf("Expected phase 'load', got %q", lastErr.Phase)
		}
	}
}

func TestBackgroundWorker_ErrorRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	// Start with no file
	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// First refresh should fail (no file)
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	if worker.GetSnapshot() != nil {
		t.Error("Expected nil snapshot when file doesn't exist")
	}

	// Now create the file
	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Reset hash to force reload
	worker.ResetHash()

	// Second refresh should succeed
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot after file created")
	}

	// Error should be cleared
	if worker.LastError() != nil {
		t.Error("Expected error to be cleared on success")
	}
}

func TestBackgroundWorker_SafeCompute(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// Test that safeCompute catches panics
	err2 := worker.safeCompute("test", func() error {
		panic("intentional panic for testing")
	})

	if err2 == nil {
		t.Error("safeCompute should catch panics")
	}

	if err2.Phase != "test" {
		t.Errorf("Expected phase 'test', got %q", err2.Phase)
	}

	// Verify worker still functional after panic
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	if worker.GetSnapshot() == nil {
		t.Error("Worker should still be functional after panic recovery")
	}
}

func TestHashPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "short string (empty hash)",
			input:    "empty",
			expected: "empty",
		},
		{
			name:     "exactly 16 chars",
			input:    "1234567890123456",
			expected: "1234567890123456",
		},
		{
			name:     "longer than 16 chars",
			input:    "8b423072ec4730921a2b3c4d5e6f7890",
			expected: "8b423072ec473092",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hashPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("hashPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBackgroundWorker_StartAfterStop(t *testing.T) {
	// Test that Start() returns error after Stop() has been called
	cfg := WorkerConfig{
		BeadsPath: "", // No watcher needed for this test
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	// Start and stop the worker
	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	worker.Stop()

	// Attempting to start again should fail
	err = worker.Start()
	if err == nil {
		t.Error("Start() after Stop() should return an error")
	}

	// Verify the worker is stopped
	if worker.State() != WorkerStopped {
		t.Errorf("Expected WorkerStopped state, got %v", worker.State())
	}
}

func TestStartBackgroundWorkerCmdFailureEndsWaiterChain(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	worker.Stop()

	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Status: model.StatusOpen}}, nil, "")
	m.backgroundWorker = worker
	m.snapshotInitPending = true

	msg := StartBackgroundWorkerCmd(worker)()
	startErr, ok := msg.(backgroundWorkerStartErrorMsg)
	if !ok || startErr.err == nil || startErr.worker != worker {
		t.Fatalf("start command result=%#v, want worker-scoped start error", msg)
	}
	updated, cmd := m.Update(startErr)
	m = updated.(*Model)
	if cmd != nil {
		t.Fatal("start failure re-armed the stopped worker waiter")
	}
	if m.backgroundWorker != nil || m.snapshotInitPending {
		t.Fatalf("failed worker remained installed: worker=%p pending=%v", m.backgroundWorker, m.snapshotInitPending)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "starting background worker") {
		t.Fatalf("start failure status=%q error=%v", m.statusMsg, m.statusIsError)
	}
}

func TestWaitForBackgroundWorkerMsgDrainsTerminalErrorAfterStop(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	terminal := SnapshotErrorMsg{
		Err:              errors.New("terminal failure"),
		Recoverable:      false,
		WorkerGeneration: worker.Generation(),
	}
	worker.send(terminal)
	worker.Stop()

	msg := WaitForBackgroundWorkerMsgCmd(worker)()
	envelope, ok := msg.(backgroundWorkerMsg)
	if !ok || envelope.worker != worker {
		t.Fatalf("wait result=%#v, want message scoped to stopped worker", msg)
	}
	got, ok := envelope.msg.(SnapshotErrorMsg)
	if !ok || got.Recoverable || got.Err == nil || got.Err.Error() != terminal.Err.Error() {
		t.Fatalf("wait payload=%#v, want terminal error %#v", envelope.msg, terminal)
	}

	m := NewModel(nil, nil, "")
	m.backgroundWorker = worker
	updated, cmd := m.Update(envelope)
	m = updated.(*Model)
	if cmd != nil || m.backgroundWorker != nil {
		t.Fatalf("terminal error left worker/waiter active: worker=%p cmd=%v", m.backgroundWorker, cmd != nil)
	}
	if !m.statusIsError || !strings.Contains(m.statusMsg, "terminal failure") {
		t.Fatalf("terminal error status=%q error=%v", m.statusMsg, m.statusIsError)
	}
}

func TestStaleBackgroundWorkerStartFailureDoesNotMutateReplacement(t *testing.T) {
	failedWorker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	failedWorker.Stop()

	replacement, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker replacement failed: %v", err)
	}
	defer replacement.Stop()

	m := NewModel([]model.Issue{{ID: "issue-1", Title: "Issue", Status: model.StatusOpen}}, nil, "")
	m.backgroundWorker = replacement
	m.snapshotInitPending = true
	m.statusMsg = "replacement starting"

	msg := StartBackgroundWorkerCmd(failedWorker)()
	startErr, ok := msg.(backgroundWorkerStartErrorMsg)
	if !ok || startErr.worker != failedWorker {
		t.Fatalf("start command result=%#v, want failed-worker-scoped error", msg)
	}
	updated, cmd := m.Update(startErr)
	m = updated.(*Model)
	if cmd != nil {
		t.Fatal("stale start failure unexpectedly scheduled work")
	}
	if m.backgroundWorker != replacement || !m.snapshotInitPending {
		t.Fatalf("stale failure mutated replacement state: worker=%p pending=%v", m.backgroundWorker, m.snapshotInitPending)
	}
	if m.statusMsg != "replacement starting" || m.statusIsError {
		t.Fatalf("stale failure mutated replacement status=%q error=%v", m.statusMsg, m.statusIsError)
	}
}

func TestStaleBackgroundWorkerMessageDoesNotMutateReplacement(t *testing.T) {
	oldWorker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker old failed: %v", err)
	}
	defer oldWorker.Stop()
	replacement, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker replacement failed: %v", err)
	}
	defer replacement.Stop()

	m := NewModel(nil, nil, "")
	m.backgroundWorker = replacement
	m.snapshotInitPending = true
	m.statusMsg = "replacement active"
	stale := backgroundWorkerMsg{
		worker: oldWorker,
		msg: SnapshotErrorMsg{
			Err:              errors.New("old worker failed"),
			Recoverable:      false,
			WorkerGeneration: oldWorker.Generation(),
		},
	}
	updated, cmd := m.Update(stale)
	m = updated.(*Model)
	if cmd != nil || m.backgroundWorker != replacement || !m.snapshotInitPending {
		t.Fatalf("stale message mutated replacement: worker=%p pending=%v cmd=%v", m.backgroundWorker, m.snapshotInitPending, cmd != nil)
	}
	if m.statusMsg != "replacement active" || m.statusIsError {
		t.Fatalf("stale message mutated status=%q error=%v", m.statusMsg, m.statusIsError)
	}
}

func TestInstallBackgroundWorkerResetsInstanceLocalSequenceFences(t *testing.T) {
	oldWorker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker old failed: %v", err)
	}
	defer oldWorker.Stop()
	replacement, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker replacement failed: %v", err)
	}
	defer replacement.Stop()

	m := NewModel(nil, nil, "")
	m.installBackgroundWorker(oldWorker)
	m.lastWorkerGeneration = 41
	m.lastAppliedSnapshotVer = 73
	m.installBackgroundWorker(replacement)
	if m.lastWorkerGeneration != 0 || m.lastAppliedSnapshotVer != 0 {
		t.Fatalf("replacement inherited old fences generation/version=%d/%d", m.lastWorkerGeneration, m.lastAppliedSnapshotVer)
	}

	cfg := snapshotBuildConfigDefault()
	cfg.SkipPhase2 = true
	snapshot := NewSnapshotBuilder([]model.Issue{{
		ID: "replacement-1", Title: "Replacement", Status: model.StatusOpen, IssueType: model.TypeTask,
	}}).WithBuildConfig(cfg).Build()
	updated, _ := m.Update(backgroundWorkerMsg{
		worker: replacement,
		msg: SnapshotReadyMsg{
			Snapshot:         snapshot,
			SnapshotVer:      1,
			WorkerGeneration: replacement.Generation(),
		},
	})
	m = updated.(*Model)
	if m.snapshot != snapshot || m.lastWorkerGeneration != 1 || m.lastAppliedSnapshotVer != 1 {
		t.Fatalf("fresh worker snapshot was rejected: snapshot=%p want=%p generation/version=%d/%d", m.snapshot, snapshot, m.lastWorkerGeneration, m.lastAppliedSnapshotVer)
	}
}

func TestSyncReloadAfterTerminalWorkerErrorDetachesOldSnapshot(t *testing.T) {
	t.Setenv("BV_BACKGROUND_MODE", "0")
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "issues.jsonl")
	if err := os.WriteFile(beadsPath, []byte(`{"id":"new-1","title":"New issue","status":"open","issue_type":"task"}`), 0o644); err != nil {
		t.Fatalf("write replacement issues: %v", err)
	}

	oldIssues := []model.Issue{{ID: "old-1", Title: "Old issue", Status: model.StatusOpen, IssueType: model.TypeTask}}
	m := NewModel(oldIssues, nil, beadsPath)
	if m.watcher != nil {
		defer m.watcher.Stop()
	}
	cfg := snapshotBuildConfigDefault()
	cfg.SkipPhase2 = true
	m.snapshot = NewSnapshotBuilder(oldIssues).WithBuildConfig(cfg).Build()

	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	m.installBackgroundWorker(worker)
	updated, _ := m.Update(backgroundWorkerMsg{
		worker: worker,
		msg: SnapshotErrorMsg{
			Err:              errors.New("terminal worker failure"),
			Recoverable:      false,
			WorkerGeneration: worker.Generation(),
		},
	})
	m = updated.(*Model)
	if m.backgroundWorker != nil {
		t.Fatal("terminal error left the worker installed")
	}

	updated, _ = m.Update(FileChangedMsg{})
	m = updated.(*Model)
	if m.statusIsError {
		t.Fatalf("synchronous fallback reload failed: %s", m.statusMsg)
	}
	if m.snapshot != nil {
		t.Fatal("successful synchronous reload retained the old worker snapshot")
	}
	if len(m.issues) != 1 || m.issues[0].ID != "new-1" {
		t.Fatalf("synchronous fallback issues=%#v, want only new-1", m.issues)
	}

	m.analysis.WaitForPhase2()
	updated, _ = m.Update(Phase2ReadyMsg{
		Stats:    m.analysis,
		Insights: m.analysis.GenerateInsights(len(m.issues)),
	})
	m = updated.(*Model)
	if m.snapshot != nil {
		t.Fatal("legacy Phase 2 completion rebuilt a snapshot from stale worker surfaces")
	}
	if len(m.list.Items()) != 1 || m.list.Items()[0].(IssueItem).Issue.ID != "new-1" {
		t.Fatalf("post-Phase-2 list retained stale items: %#v", m.list.Items())
	}
	if len(m.graphView.sortedIDs) != 1 || m.graphView.sortedIDs[0] != "new-1" {
		t.Fatalf("post-Phase-2 graph IDs=%v, want [new-1]", m.graphView.sortedIDs)
	}
	if _, ok := m.issueMap["old-1"]; ok {
		t.Fatal("post-Phase-2 issue map retained old-1")
	}
}

func TestBackgroundWorker_ConcurrentTrigger(t *testing.T) {
	// Test that concurrent TriggerRefresh calls don't cause duplicate processing
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Fire multiple TriggerRefresh calls concurrently
	// The fix ensures only one process() runs at a time, others mark dirty
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			worker.TriggerRefresh()
		}(i)
	}
	wg.Wait()

	waitForSnapshotVersion(t, worker, 1)
	waitForWorkerIdle(t, worker, 1)

	// Worker should still be in idle state (not stuck in processing)
	if worker.State() != WorkerIdle {
		t.Errorf("Expected idle state after concurrent triggers, got %v", worker.State())
	}

	// Should have a valid snapshot
	if worker.GetSnapshot() == nil {
		t.Error("Expected snapshot after concurrent triggers")
	}
}

func TestBackgroundWorker_RapidWritesKeepUIResponsive(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")
	const (
		initialIssues = 100
		rapidWrites   = 50
	)
	if err := writeStressIssuesFile(beadsPath, initialIssues, 0, "initial"); err != nil {
		t.Fatalf("write initial issues: %v", err)
	}

	issues, err := loader.LoadIssuesFromFile(beadsPath)
	if err != nil {
		t.Fatalf("load initial issues: %v", err)
	}
	m := NewModel(issues, nil, "")

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 25 * time.Millisecond,
		IdleGC:        &IdleGCConfig{Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()
	m.backgroundWorker = worker
	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	worker.TriggerRefresh()

	writerDone := make(chan error, 1)
	go func() {
		for i := 0; i < rapidWrites; i++ {
			f, openErr := os.OpenFile(beadsPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if openErr != nil {
				writerDone <- openErr
				return
			}
			_, writeErr := fmt.Fprintf(f,
				`{"id":"rapid-%d","title":"Rapid %d","status":"open","priority":2,"issue_type":"task"}`+"\n",
				i, i,
			)
			closeErr := f.Close()
			if writeErr != nil {
				writerDone <- writeErr
				return
			}
			if closeErr != nil {
				writerDone <- closeErr
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		writerDone <- nil
	}()

	var (
		updateTotal    time.Duration
		maxUpdate      time.Duration
		renderTotal    time.Duration
		maxRender      time.Duration
		sampleCount    int
		updatesOver50  int
		snapshotCount  int
		errorCount     int
		latestCount    int
		writerErr      error
		writerFinished bool
	)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()

	for !writerFinished || latestCount != initialIssues+rapidWrites {
		select {
		case writerErr = <-writerDone:
			writerFinished = true
		case msg := <-worker.Messages():
			switch typed := msg.(type) {
			case SnapshotReadyMsg:
				snapshotCount++
				latestCount = len(typed.Snapshot.Issues)
			case SnapshotErrorMsg:
				errorCount++
			}
			updated, _ := m.Update(msg)
			m = updated.(*Model)
		case <-tick.C:
			start := time.Now()
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = updated.(*Model)
			updateLatency := time.Since(start)
			renderStart := time.Now()
			if view := m.View(); view == "" {
				t.Fatal("View returned empty output during rapid writes")
			}
			renderLatency := time.Since(renderStart)
			updateTotal += updateLatency
			renderTotal += renderLatency
			sampleCount++
			if updateLatency > maxUpdate {
				maxUpdate = updateLatency
			}
			if renderLatency > maxRender {
				maxRender = renderLatency
			}
			if updateLatency > 50*time.Millisecond {
				updatesOver50++
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for final snapshot: writer_finished=%v latest_count=%d snapshots=%d errors=%d",
				writerFinished, latestCount, snapshotCount, errorCount)
		}
	}

	if writerErr != nil {
		t.Fatalf("rapid writer failed: %v", writerErr)
	}
	if sampleCount == 0 {
		t.Fatal("expected UI latency samples")
	}
	if snapshotCount >= rapidWrites {
		t.Fatalf("expected rapid writes to coalesce: snapshots=%d writes=%d", snapshotCount, rapidWrites)
	}
	if errorCount != 0 {
		t.Fatalf("unexpected background worker errors: %d", errorCount)
	}

	averageUpdate := updateTotal / time.Duration(sampleCount)
	averageRender := renderTotal / time.Duration(sampleCount)
	over50Ratio := float64(updatesOver50) / float64(sampleCount)
	t.Logf("rapid-write UI latency: update_avg=%v update_max=%v update_over50ms=%d/%d (%.2f%%), render_avg=%v render_max=%v, snapshots=%d writes=%d",
		averageUpdate, maxUpdate, updatesOver50, sampleCount, over50Ratio*100,
		averageRender, maxRender, snapshotCount, rapidWrites)
	if averageUpdate >= 50*time.Millisecond {
		t.Fatalf("average UI update latency=%v, want <50ms", averageUpdate)
	}
	if over50Ratio >= 0.05 {
		t.Fatalf("UI update samples over 50ms=%.2f%%, want <5%%", over50Ratio*100)
	}
}

func TestBackgroundWorker_TriggerRefreshCoalescesWhileProcessScheduled(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: ""})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	mutateWorkerForTest(worker, func() {
		worker.processScheduled = true
	})

	worker.TriggerRefresh()

	state, dirty, scheduled := workerStateFlags(worker)
	if state != WorkerIdle {
		t.Fatalf("state=%v, want %v", state, WorkerIdle)
	}
	if !scheduled {
		t.Fatal("expected existing scheduled process to remain scheduled")
	}
	if !dirty {
		t.Fatal("expected refresh to mark worker dirty while process is scheduled")
	}
	if got := worker.coalesceCount.Load(); got != 1 {
		t.Fatalf("coalesceCount=%d, want 1", got)
	}
	if got := worker.Metrics().ProcessingCount; got != 0 {
		t.Fatalf("ProcessingCount=%d, want 0", got)
	}
}

type workerLockProbeWriter struct {
	worker     *BackgroundWorker
	observed   atomic.Int64
	violations atomic.Int64
}

func (w *workerLockProbeWriter) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), `"component":"background_worker"`) {
		return len(p), nil
	}
	w.observed.Add(1)
	if !w.worker.mu.TryRLock() {
		w.violations.Add(1)
		return len(p), nil
	}
	w.worker.mu.RUnlock()
	return len(p), nil
}

func TestBackgroundWorker_LogSinkRunsOutsideStateLock(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{BeadsPath: ""})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()
	worker.logLevel = LogLevelDebug

	probe := &workerLockProbeWriter{worker: worker}
	originalOutput := log.Writer()
	log.SetOutput(probe)
	t.Cleanup(func() { log.SetOutput(originalOutput) })

	mutateWorkerForTest(worker, func() {
		worker.processScheduled = true
	})
	worker.TriggerRefresh()
	worker.ForceRefresh()

	mutateWorkerForTest(worker, func() {
		worker.state = WorkerIdle
		worker.dirty = false
		worker.processScheduled = true
	})
	worker.processWithSnapshotBuilder(func(bool) snapshotBuildResult {
		return snapshotBuildResult{}
	})

	if got := probe.observed.Load(); got < 4 {
		t.Fatalf("observed background-worker log writes = %d, want at least 4", got)
	}
	if got := probe.violations.Load(); got != 0 {
		t.Fatalf("background-worker log sink invoked while state lock held %d times", got)
	}
}

func TestBackgroundWorker_Phase2Async(t *testing.T) {
	// Test that Phase 2 analysis runs asynchronously (bv-e3ub)
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	// Create a file with some dependencies to make Phase 2 analysis non-trivial
	content := `{"id":"test-1","title":"Root","status":"open","priority":1,"issue_type":"task"}
{"id":"test-2","title":"Child","status":"open","priority":2,"issue_type":"task","dependencies":[{"depends_on":"test-1","type":"blocks"}]}
`
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// Trigger refresh and wait for snapshot
	worker.TriggerRefresh()
	time.Sleep(200 * time.Millisecond)

	snapshot := worker.GetSnapshot()
	if snapshot == nil {
		t.Fatal("Expected snapshot after refresh")
	}

	// Snapshot should exist with analysis
	if snapshot.Analysis == nil {
		t.Fatal("Expected Analysis in snapshot")
	}

	// Wait for Phase 2 to complete using the GraphStats API
	snapshot.Analysis.WaitForPhase2()

	// After waiting, Phase 2 should be ready
	if !snapshot.Analysis.IsPhase2Ready() {
		t.Error("Phase 2 should be ready after WaitForPhase2()")
	}
}

func TestBackgroundWorker_Phase2UpdateMsgDelivered(t *testing.T) {
	// Verify that the worker emits Phase2UpdateMsg asynchronously with a matching hash (bv-j97z).
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	// Small dependency graph; Phase 2 should typically run async.
	content := `{"id":"root","title":"Root","status":"open","priority":1,"issue_type":"task"}
{"id":"child","title":"Child","status":"open","priority":2,"issue_type":"task","dependencies":[{"depends_on_id":"root","type":"blocks"}]}
`
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	start := time.Now()
	t.Logf("[%s] EVENT=trigger_refresh", start.UTC().Format(time.RFC3339Nano))
	worker.TriggerRefresh()

	var snapshot *DataSnapshot
	var phase2Hash string
	var snapshotAt time.Time
	var phase2At time.Time

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()

	for snapshot == nil || phase2Hash == "" {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for SnapshotReadyMsg and Phase2UpdateMsg (snapshot=%v, phase2Hash=%q)", snapshot != nil, phase2Hash)
		case msg := <-worker.Messages():
			switch m := msg.(type) {
			case SnapshotReadyMsg:
				if m.Snapshot == nil {
					continue
				}
				snapshot = m.Snapshot
				snapshotAt = time.Now()
				t.Logf("[%s] EVENT=snapshot_ready elapsed_ms=%.3f issues=%d hash=%s",
					snapshotAt.UTC().Format(time.RFC3339Nano),
					float64(snapshotAt.Sub(start).Microseconds())/1000.0,
					len(snapshot.Issues),
					hashPrefix(snapshot.DataHash),
				)

				// If Phase 2 already completed by the time we received the snapshot,
				// the update message may be suppressed (no work to signal).
				if snapshot.Analysis != nil && snapshot.Analysis.IsPhase2Ready() {
					t.Skip("phase 2 completed before snapshot delivery; Phase2UpdateMsg may not be emitted for this dataset")
				}

			case Phase2UpdateMsg:
				phase2Hash = m.DataHash
				phase2At = time.Now()
				t.Logf("[%s] EVENT=phase2_update elapsed_ms=%.3f hash=%s",
					phase2At.UTC().Format(time.RFC3339Nano),
					float64(phase2At.Sub(start).Microseconds())/1000.0,
					hashPrefix(phase2Hash),
				)
			}
		}
	}

	if snapshot.DataHash == "" {
		t.Fatal("expected snapshot DataHash to be set")
	}
	if phase2Hash != snapshot.DataHash {
		t.Fatalf("phase2 hash mismatch: got %s, want %s", phase2Hash, snapshot.DataHash)
	}
}

func TestBackgroundWorker_RunPhase2AnalysisSignalsMatchingSnapshot(t *testing.T) {
	issues := []model.Issue{
		{ID: "root", Title: "Root", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "child", Title: "Child", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "root", Type: model.DepBlocks}}},
	}
	snapshot := NewSnapshotBuilder(issues).Build()
	snapshot.Analysis.WaitForPhase2()
	snapshot.DataHash = "phase2-signal-test"
	snapshot.phase2Ready = false

	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()
	worker.snapshot = snapshot

	const snapshotVersion = 9
	workerGeneration := worker.Generation()
	go worker.runPhase2Analysis(snapshot, snapshotVersion, workerGeneration)
	msg := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(msg tea.Msg) bool {
		_, ok := msg.(Phase2UpdateMsg)
		return ok
	}).(Phase2UpdateMsg)
	if msg.DataHash != snapshot.DataHash {
		t.Fatalf("Phase2UpdateMsg hash=%q, want %q", msg.DataHash, snapshot.DataHash)
	}
	if msg.Stats != snapshot.Analysis {
		t.Fatal("Phase2UpdateMsg did not preserve the active GraphStats identity")
	}
	if msg.Snapshot != snapshot || msg.SnapshotVer != snapshotVersion {
		t.Fatalf("Phase2UpdateMsg identity=(%p,%d), want (%p,%d)", msg.Snapshot, msg.SnapshotVer, snapshot, snapshotVersion)
	}
	if msg.WorkerGeneration != workerGeneration {
		t.Fatalf("Phase2UpdateMsg generation=%d, want %d", msg.WorkerGeneration, workerGeneration)
	}

	m := NewModel(issues, nil, "")
	m.snapshot = snapshot
	m.lastAppliedSnapshotVer = snapshotVersion
	if view := m.View(); view == "" {
		t.Fatal("UI did not render while Phase 2 snapshot was pending")
	}
	newM, _ := m.Update(msg)
	m = newM.(*Model)
	if !m.snapshot.phase2Ready {
		t.Fatal("matching Phase2UpdateMsg did not mark current snapshot ready")
	}
}

func TestBackgroundWorker_RunPhase2AnalysisDropsInvalidatedGeneration(t *testing.T) {
	issues := []model.Issue{{ID: "root", Title: "Root", Status: model.StatusOpen, IssueType: model.TypeTask}}
	snapshot := NewSnapshotBuilder(issues).Build()
	snapshot.Analysis.WaitForPhase2()
	snapshot.DataHash = "stale-phase2-generation"
	snapshot.phase2Ready = false

	worker, err := NewBackgroundWorker(WorkerConfig{})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()
	worker.snapshot = snapshot
	staleGeneration := worker.Generation()
	mutateWorkerForTest(worker, func() {
		worker.generation++
	})

	worker.runPhase2Analysis(snapshot, 9, staleGeneration)

	select {
	case unexpected := <-worker.msgCh:
		t.Fatalf("invalidated Phase 2 generation published a message: %#v", unexpected)
	default:
	}
}

func TestBackgroundWorker_Phase2NoSendAfterStop(t *testing.T) {
	// Test that runPhase2Analysis doesn't send if worker is stopped (bv-e3ub)
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	cfg := WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	}

	worker, err := NewBackgroundWorker(cfg)
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}

	// Trigger refresh
	worker.TriggerRefresh()

	// Stop immediately (before Phase 2 can complete)
	worker.Stop()

	// Worker should be stopped
	if worker.State() != WorkerStopped {
		t.Errorf("Expected stopped state, got %v", worker.State())
	}

	// The test passes if we reach here without panicking
	// (runPhase2Analysis should gracefully handle stopped worker)
}

func TestDataSnapshot_GetGraphStats(t *testing.T) {
	// Test GetGraphStats helper method (bv-e3ub)

	// Test nil snapshot
	var nilSnapshot *DataSnapshot
	if nilSnapshot.GetGraphStats() != nil {
		t.Error("GetGraphStats on nil snapshot should return nil")
	}

	// Test snapshot with nil Analysis
	emptySnapshot := &DataSnapshot{}
	if emptySnapshot.GetGraphStats() != nil {
		t.Error("GetGraphStats with nil Analysis should return nil")
	}
}

func waitForBackgroundWorkerMsg(t *testing.T, worker *BackgroundWorker, timeout time.Duration, predicate func(tea.Msg) bool) tea.Msg {
	t.Helper()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case msg := <-worker.Messages():
			if predicate(msg) {
				return msg
			}
		case <-timer.C:
			t.Fatalf("timeout waiting for BackgroundWorker message (%v)", timeout)
		}
	}
}

func waitForSnapshotVersion(t *testing.T, worker *BackgroundWorker, minVersion uint64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if worker.Metrics().SnapshotVersion >= minVersion {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for snapshot version %d (got %d)", minVersion, worker.Metrics().SnapshotVersion)
}

func waitForWorkerIdle(t *testing.T, worker *BackgroundWorker, minProcessingCount uint64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		metrics := worker.Metrics()
		state, dirty, scheduled := workerStateFlags(worker)
		if state == WorkerIdle && !dirty && !scheduled && metrics.ProcessingCount >= minProcessingCount {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	metrics := worker.Metrics()
	state, dirty, scheduled := workerStateFlags(worker)
	t.Fatalf(
		"timeout waiting for idle worker after %d processes (state=%v dirty=%v scheduled=%v processes=%d snapshots=%d)",
		minProcessingCount,
		state,
		dirty,
		scheduled,
		metrics.ProcessingCount,
		metrics.SnapshotVersion,
	)
}

func workerStateFlags(worker *BackgroundWorker) (WorkerState, bool, bool) {
	worker.mu.RLock()
	defer worker.mu.RUnlock()
	return worker.state, worker.dirty, worker.processScheduled
}

func mutateWorkerForTest(worker *BackgroundWorker, fn func()) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	fn()
}

func TestBackgroundWorker_MalformedJSON_WarnsAndContinues(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"ok-1","title":"Ok 1","status":"open","priority":1,"issue_type":"task"}
not json
{"id":"bad-only-id"}
{"id":"ok-2","title":"Ok 2","status":"open","priority":2,"issue_type":"task"}
`
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	worker.TriggerRefresh()

	msg := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SnapshotReadyMsg)
		return ok
	})

	ready := msg.(SnapshotReadyMsg)
	if ready.Snapshot == nil {
		t.Fatal("Expected non-nil snapshot")
	}
	if got, want := len(ready.Snapshot.Issues), 2; got != want {
		t.Fatalf("Expected %d issues, got %d", want, got)
	}
	if ready.Snapshot.LoadWarningCount == 0 {
		t.Error("Expected LoadWarningCount > 0 for malformed/invalid lines")
	}
	if worker.LastError() != nil {
		t.Errorf("Expected LastError to be nil for parse warnings, got: %v", worker.LastError())
	}
}

func TestBackgroundWorker_PreservesSnapshotOnPermissionErrorAndRecovers(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content1 := `{"id":"test-1","title":"Initial","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content1), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	// Build initial snapshot.
	worker.TriggerRefresh()
	msg1 := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SnapshotReadyMsg)
		return ok
	})
	snapshot1 := msg1.(SnapshotReadyMsg).Snapshot
	if snapshot1 == nil {
		t.Fatal("Expected initial snapshot")
	}

	// Make file unreadable.
	if err := os.Chmod(beadsPath, 0000); err != nil {
		t.Skipf("chmod not supported: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(beadsPath, 0644)
	})
	if readable, openErr := os.Open(beadsPath); openErr == nil {
		_ = readable.Close()
		t.Skip("permission bits do not make the fixture unreadable for this test user")
	}

	worker.TriggerRefresh()
	msgErr := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SnapshotErrorMsg)
		return ok
	})
	errMsg := msgErr.(SnapshotErrorMsg)
	if errMsg.Err == nil {
		t.Fatal("Expected SnapshotErrorMsg to contain error")
	}
	if !errMsg.Recoverable {
		t.Error("Expected Recoverable=true for permission errors")
	}

	// Snapshot must be preserved after an error.
	if worker.GetSnapshot() != snapshot1 {
		t.Fatal("Expected previous snapshot to be preserved on load error")
	}
	if worker.LastError() == nil {
		t.Fatal("Expected LastError to be set after load error")
	}

	// Restore permissions and write new content to force a successful rebuild.
	if err := os.Chmod(beadsPath, 0644); err != nil {
		t.Fatalf("Failed to restore file permissions: %v", err)
	}

	content2 := `{"id":"test-1","title":"Recovered","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content2), 0644); err != nil {
		t.Fatalf("Failed to write recovered file: %v", err)
	}
	worker.ResetHash()

	worker.TriggerRefresh()
	msg2 := waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SnapshotReadyMsg)
		return ok
	})
	snapshot2 := msg2.(SnapshotReadyMsg).Snapshot
	if snapshot2 == nil {
		t.Fatal("Expected snapshot after recovery")
	}
	if snapshot2 == snapshot1 {
		t.Fatal("Expected new snapshot pointer after recovery rebuild")
	}
	if got, want := snapshot2.Issues[0].Title, "Recovered"; got != want {
		t.Fatalf("Expected updated title %q, got %q", want, got)
	}
	if worker.LastError() != nil {
		t.Fatalf("Expected LastError to be cleared after recovery, got: %v", worker.LastError())
	}
}

func TestBackgroundWorker_HeartbeatUpdatesHealth(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:         beadsPath,
		DebounceDelay:     10 * time.Millisecond,
		HeartbeatInterval: 10 * time.Millisecond,
		HeartbeatTimeout:  200 * time.Millisecond,
		WatchdogInterval:  time.Hour, // keep deterministic in tests
		ProcessingTimeout: time.Hour,
		MaxRecoveries:     3,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	h1 := worker.Health()
	if !h1.Started || !h1.Alive || h1.LastHeartbeat.IsZero() {
		t.Fatalf("expected started+alive health, got: %+v", h1)
	}

	time.Sleep(30 * time.Millisecond)
	h2 := worker.Health()
	if !h2.LastHeartbeat.After(h1.LastHeartbeat) {
		t.Fatalf("expected heartbeat to advance: %v -> %v", h1.LastHeartbeat, h2.LastHeartbeat)
	}
}

func TestBackgroundWorker_CheckHealth_TriggersRecoveryOnMissedHeartbeat(t *testing.T) {
	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	content := `{"id":"test-1","title":"Test","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:         beadsPath,
		DebounceDelay:     10 * time.Millisecond,
		HeartbeatInterval: time.Hour, // suppress updates so we can force "missed"
		HeartbeatTimeout:  10 * time.Millisecond,
		WatchdogInterval:  time.Hour, // keep deterministic in tests
		ProcessingTimeout: time.Hour,
		MaxRecoveries:     3,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	worker.mu.RLock()
	loopCancelPublished := worker.loopCancel != nil
	worker.mu.RUnlock()
	if !loopCancelPublished {
		t.Fatal("Start returned before publishing the process-loop cancellation handle")
	}

	mutateWorkerForTest(worker, func() {
		worker.lastHeartbeat = time.Now().Add(-time.Second)
	})

	recoveryStart := time.Now()
	worker.checkHealth(time.Now())
	if elapsed := time.Since(recoveryStart); elapsed >= time.Second {
		t.Fatalf("immediate post-Start recovery took %v; process loop was not cancellable", elapsed)
	}

	if got := worker.Health().RecoveryCount; got < 1 {
		t.Fatalf("expected recoveryCount to increment, got %d", got)
	}
	if worker.State() == WorkerStopped {
		t.Fatal("expected worker to remain running after recovery attempt")
	}
}

func TestBackgroundWorker_MaybeIdleGC_TriggersAfterThreshold(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath: "",
		IdleGC: &IdleGCConfig{
			Enabled:     true,
			Threshold:   5 * time.Second,
			CheckEvery:  time.Hour, // avoid nondeterministic ticker behavior in unit tests
			MinInterval: 30 * time.Second,
			GCPercent:   200,
		},
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	gcCalls := 0
	worker.idleGCFunc = func() { gcCalls++ }

	now := time.Now()
	worker.recordActivityAt(now.Add(-10 * time.Second))

	worker.maybeIdleGC(now)

	if gcCalls != 1 {
		t.Fatalf("expected idle GC to run once, ran %d times", gcCalls)
	}
	if got := worker.Health().IdleGCCount; got != 1 {
		t.Fatalf("expected IdleGCCount=1, got %d", got)
	}

	// Enforce min-interval gating.
	worker.maybeIdleGC(now.Add(1 * time.Second))
	if gcCalls != 1 {
		t.Fatalf("expected idle GC to be gated by MinInterval, ran %d times", gcCalls)
	}
}

func TestModelUpdate_RecordsUserInputForIdleGC(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath: "",
		IdleGC:    &IdleGCConfig{Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "key", msg: tea.KeyMsg{Type: tea.KeyDown}},
		{name: "mouse", msg: tea.MouseMsg{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldActivity := time.Now().Add(-time.Hour)
			worker.recordActivityAt(oldActivity)

			m := NewModel(nil, nil, "")
			m.backgroundWorker = worker
			m.Update(tc.msg)

			got := time.Unix(0, worker.lastActivityUnixNano.Load())
			if !got.After(oldActivity) {
				t.Fatalf("user input did not advance activity time: got=%v old=%v", got, oldActivity)
			}
		})
	}
}

func TestBackgroundWorker_GCPausesUnderRapidSnapshotLoad(t *testing.T) {
	if os.Getenv("PERF_TEST") != "1" {
		t.Skip("set PERF_TEST=1 to run timing-sensitive GC pause test")
	}
	beadsPath := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := writeStressIssuesFile(beadsPath, 1000, 0, "gc-pause"); err != nil {
		t.Fatalf("write stress issues: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath: beadsPath,
		IdleGC:    &IdleGCConfig{Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	for i := 0; i < 10; i++ {
		snapshot := worker.buildSnapshot(true)
		if snapshot == nil {
			t.Fatalf("buildSnapshot returned nil at iteration %d", i)
		}
		if snapshot.Analysis != nil {
			snapshot.Analysis.WaitForPhase2()
		}
		snapshot.releasePooledIssues()
		snapshot = nil
		runtime.GC()
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if after.NumGC <= before.NumGC {
		t.Fatalf("expected GC cycles under rapid snapshot load: before=%d after=%d", before.NumGC, after.NumGC)
	}

	firstCycle := before.NumGC + 1
	if after.NumGC-firstCycle+1 > uint32(len(after.PauseNs)) {
		firstCycle = after.NumGC - uint32(len(after.PauseNs)) + 1
	}
	pauses := make([]time.Duration, 0, after.NumGC-firstCycle+1)
	for cycle := firstCycle; cycle <= after.NumGC; cycle++ {
		pause := time.Duration(after.PauseNs[(cycle-1)%uint32(len(after.PauseNs))])
		pauses = append(pauses, pause)
	}
	sort.Slice(pauses, func(i, j int) bool { return pauses[i] < pauses[j] })
	maxPause := pauses[len(pauses)-1]
	p95Index := (95*len(pauses) + 99) / 100 // nearest-rank percentile
	p95Pause := pauses[p95Index-1]
	t.Logf("rapid snapshot GC cycles=%d p95_pause=%v max_pause=%v", after.NumGC-before.NumGC, p95Pause, maxPause)
	// A single runtime pause includes shared-host scheduler delay and is not a
	// stable unit-test signal. Bound the sustained tail instead; the maximum is
	// retained above so isolated profiling still exposes individual outliers.
	if p95Pause >= 10*time.Millisecond {
		t.Fatalf("p95 GC pause=%v, want <10ms (max=%v)", p95Pause, maxPause)
	}
}

func TestBackgroundWorker_MaybeIdleGC_DoesNotRunWhenProcessing(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath: "",
		IdleGC: &IdleGCConfig{
			Enabled:     true,
			Threshold:   5 * time.Second,
			CheckEvery:  time.Hour,
			MinInterval: 30 * time.Second,
			GCPercent:   200,
		},
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	gcCalls := 0
	worker.idleGCFunc = func() { gcCalls++ }

	now := time.Now()
	worker.recordActivityAt(now.Add(-10 * time.Second))

	mutateWorkerForTest(worker, func() {
		worker.state = WorkerProcessing
	})

	worker.maybeIdleGC(now)
	if gcCalls != 0 {
		t.Fatalf("expected idle GC to not run during processing, ran %d times", gcCalls)
	}
	if got := worker.Health().IdleGCCount; got != 0 {
		t.Fatalf("expected IdleGCCount=0, got %d", got)
	}
}

func TestBackgroundWorker_AttemptRecovery_GivesUpAfterMaxRecoveries(t *testing.T) {
	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:         "",
		MaxRecoveries:     1,
		HeartbeatInterval: time.Hour,
		WatchdogInterval:  time.Hour,
		HeartbeatTimeout:  10 * time.Millisecond,
		ProcessingTimeout: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	worker.attemptRecovery("test-1")

	worker.attemptRecovery("test-2")
	_ = waitForBackgroundWorkerMsg(t, worker, 2*time.Second, func(m tea.Msg) bool {
		msg, ok := m.(SnapshotErrorMsg)
		return ok && !msg.Recoverable
	}).(SnapshotErrorMsg)

	if worker.State() != WorkerStopped {
		t.Fatalf("expected worker to be stopped after giving up, got state=%v", worker.State())
	}
}

func TestStress_SustainedWrites(t *testing.T) {
	if os.Getenv("PERF_TEST") != "1" {
		t.Skip("set PERF_TEST=1 to run 10+ minute stress tests")
	}
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	const issueCount = 200
	if err := writeStressIssuesFile(beadsPath, issueCount, 0, "init"); err != nil {
		t.Fatalf("failed to write initial beads file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var snapshotCount atomic.Int64
	var errorCount atomic.Int64
	go countWorkerMessages(worker, &snapshotCount, &errorCount)

	var initialMem runtime.MemStats
	initialGoros := runtime.NumGoroutine()
	runtime.ReadMemStats(&initialMem)
	initialFDs, fdOK := procFDCount()

	duration := requireTestDurationOrSkip(t, 10*time.Minute, 30*time.Second)
	end := time.Now().Add(duration)
	writeInterval := 100 * time.Millisecond

	// Ensure the worker processes at least one file-change event before we start the long loop.
	if err := writeStressIssuesFile(beadsPath, issueCount, 0, "warmup"); err != nil {
		t.Fatalf("failed to write warmup beads file: %v", err)
	}
	waitForAtomicAtLeast(t, 10*time.Second, &snapshotCount, 1)

	writeCount := 0
	for now := time.Now(); now.Before(end); now = time.Now() {
		// Rewrite with stable issue count (stress file watching + parsing + analysis,
		// without unbounded memory growth from an ever-expanding dataset).
		changeIndex := writeCount % issueCount
		if err := writeStressIssuesFile(beadsPath, issueCount, changeIndex, fmt.Sprintf("tick-%d", writeCount)); err != nil {
			t.Fatalf("failed to write beads file: %v", err)
		}
		writeCount++

		// Sample every minute.
		if writeCount%600 == 0 {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			goros := runtime.NumGoroutine()
			if fdOK {
				fds, _ := procFDCount()
				t.Logf("Minute %d: heap=%dMB goros=%d fds=%d writes=%d", writeCount/600, mem.Alloc/1024/1024, goros, fds, writeCount)
			} else {
				t.Logf("Minute %d: heap=%dMB goros=%d writes=%d", writeCount/600, mem.Alloc/1024/1024, goros, writeCount)
			}
		}

		time.Sleep(writeInterval)
	}

	worker.Stop()

	// Final checks.
	runtime.GC()
	time.Sleep(1 * time.Second)

	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	finalGoros := runtime.NumGoroutine()
	finalFDs := 0
	if fdOK {
		finalFDs, _ = procFDCount()
	}

	memDelta := int64(finalMem.Alloc) - int64(initialMem.Alloc)
	goroDelta := finalGoros - initialGoros
	fdDelta := finalFDs - initialFDs

	t.Logf("Final: heap=%dMB (delta=%dMB) goros=%d (delta=%d) fds=%d (delta=%d) writes=%d",
		finalMem.Alloc/1024/1024, memDelta/1024/1024,
		finalGoros, goroDelta,
		finalFDs, fdDelta,
		writeCount,
	)

	if got := snapshotCount.Load(); got < 1 {
		t.Fatalf("expected at least one SnapshotReadyMsg, got %d", got)
	}
	if got := errorCount.Load(); got != 0 {
		t.Fatalf("expected no SnapshotErrorMsg, got %d", got)
	}
	if goroDelta > 10 {
		t.Fatalf("goroutine leak: delta=%d (want <= 10)", goroDelta)
	}
	if memDelta > 100*1024*1024 {
		t.Fatalf("memory growth too high: delta=%dMB (want <= 100MB)", memDelta/1024/1024)
	}
	if fdOK && fdDelta > 10 {
		t.Fatalf("file descriptor leak: delta=%d (want <= 10)", fdDelta)
	}
}

func TestStress_BurstWrites(t *testing.T) {
	if os.Getenv("PERF_TEST") != "1" {
		t.Skip("set PERF_TEST=1 to run 10+ minute stress tests")
	}
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	const issueCount = 200
	if err := writeStressIssuesFile(beadsPath, issueCount, 0, "init"); err != nil {
		t.Fatalf("failed to write initial beads file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	var snapshotCount atomic.Int64
	var errorCount atomic.Int64
	go countWorkerMessages(worker, &snapshotCount, &errorCount)

	var initialMem runtime.MemStats
	initialGoros := runtime.NumGoroutine()
	runtime.ReadMemStats(&initialMem)
	initialFDs, fdOK := procFDCount()

	duration := requireTestDurationOrSkip(t, 5*time.Minute, 30*time.Second)
	end := time.Now().Add(duration)

	writeCount := 0
	for time.Now().Before(end) {
		// Burst of 10 quick writes (agent completing task).
		for i := 0; i < 10; i++ {
			changeIndex := writeCount % issueCount
			if err := writeStressIssuesFile(beadsPath, issueCount, changeIndex, fmt.Sprintf("burst-%d", writeCount)); err != nil {
				t.Fatalf("failed to write beads file: %v", err)
			}
			writeCount++
			time.Sleep(10 * time.Millisecond)
		}

		// Quiet period (agent thinking).
		time.Sleep(2 * time.Second)
	}

	worker.Stop()
	runtime.GC()
	time.Sleep(1 * time.Second)

	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	finalGoros := runtime.NumGoroutine()
	finalFDs := 0
	if fdOK {
		finalFDs, _ = procFDCount()
	}

	memDelta := int64(finalMem.Alloc) - int64(initialMem.Alloc)
	goroDelta := finalGoros - initialGoros
	fdDelta := finalFDs - initialFDs

	t.Logf("Final: heap=%dMB (delta=%dMB) goros=%d (delta=%d) fds=%d (delta=%d) writes=%d snapshots=%d errors=%d",
		finalMem.Alloc/1024/1024, memDelta/1024/1024,
		finalGoros, goroDelta,
		finalFDs, fdDelta,
		writeCount,
		snapshotCount.Load(),
		errorCount.Load(),
	)

	if got := snapshotCount.Load(); got < 1 {
		t.Fatalf("expected at least one SnapshotReadyMsg, got %d", got)
	}
	if got := errorCount.Load(); got != 0 {
		t.Fatalf("expected no SnapshotErrorMsg, got %d", got)
	}
	if goroDelta > 10 {
		t.Fatalf("goroutine leak: delta=%d (want <= 10)", goroDelta)
	}
	if memDelta > 100*1024*1024 {
		t.Fatalf("memory growth too high: delta=%dMB (want <= 100MB)", memDelta/1024/1024)
	}
	if fdOK && fdDelta > 10 {
		t.Fatalf("file descriptor leak: delta=%d (want <= 10)", fdDelta)
	}
}

func TestStress_MemoryPressure(t *testing.T) {
	if os.Getenv("PERF_TEST") != "1" {
		t.Skip("set PERF_TEST=1 to run 10+ minute stress tests")
	}
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	// Simulate constrained memory environment.
	oldLimit := debug.SetMemoryLimit(256 * 1024 * 1024) // 256MB
	t.Cleanup(func() {
		debug.SetMemoryLimit(oldLimit)
	})

	tmpDir := t.TempDir()
	beadsPath := filepath.Join(tmpDir, "beads.jsonl")

	const issueCount = 2000
	if err := writeStressIssuesFile(beadsPath, issueCount, 0, "init"); err != nil {
		t.Fatalf("failed to write initial beads file: %v", err)
	}

	worker, err := NewBackgroundWorker(WorkerConfig{
		BeadsPath:     beadsPath,
		DebounceDelay: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewBackgroundWorker failed: %v", err)
	}
	defer worker.Stop()

	if err := worker.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	worker.TriggerRefresh()
	timeout := clampToDeadline(t, 60*time.Second, 30*time.Second)
	_ = waitForBackgroundWorkerMsg(t, worker, timeout, func(m tea.Msg) bool {
		msg, ok := m.(SnapshotReadyMsg)
		return ok && msg.Snapshot != nil
	})
}

func countWorkerMessages(worker *BackgroundWorker, snapshotCount, errorCount *atomic.Int64) {
	if worker == nil {
		return
	}
	for {
		select {
		case <-worker.Done():
			return
		case msg := <-worker.Messages():
			switch msg.(type) {
			case SnapshotReadyMsg:
				snapshotCount.Add(1)
			case SnapshotErrorMsg:
				errorCount.Add(1)
			}
		}
	}
}

func waitForAtomicAtLeast(t *testing.T, timeout time.Duration, counter *atomic.Int64, min int64) {
	t.Helper()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()

	for {
		if counter.Load() >= min {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for counter >= %d (got %d)", min, counter.Load())
		case <-tick.C:
		}
	}
}

func requireTestDurationOrSkip(t *testing.T, desired, safetyWindow time.Duration) time.Duration {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < desired+safetyWindow {
			t.Skipf("need >= %s remaining before test deadline (have %s); run with -timeout >= %s", desired+safetyWindow, remaining, desired+safetyWindow)
		}
	}
	return desired
}

func clampToDeadline(t *testing.T, desired, safetyWindow time.Duration) time.Duration {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline) - safetyWindow
		if remaining <= 0 {
			t.Skip("insufficient time before test deadline; increase -timeout")
		}
		if remaining < desired {
			return remaining
		}
	}
	return desired
}

func procFDCount() (int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func writeStressIssuesFile(path string, issueCount int, mutateIndex int, mutateSuffix string) error {
	if issueCount <= 0 {
		return fmt.Errorf("invalid issueCount: %d", issueCount)
	}
	if mutateIndex < 0 || mutateIndex >= issueCount {
		return fmt.Errorf("invalid mutateIndex: %d", mutateIndex)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < issueCount; i++ {
		title := fmt.Sprintf("Stress Issue %d", i)
		if i == mutateIndex {
			title = fmt.Sprintf("%s (%s)", title, mutateSuffix)
		}

		// Keep the payload small and stable (stress parsing/analysis without inflating memory).
		// created_at / updated_at are optional (zero values are accepted), but including updated_at
		// forces content to change while remaining valid JSON.
		line := fmt.Sprintf(
			`{"id":"stress-%d","title":%q,"status":"open","priority":1,"issue_type":"task","updated_at":%q}`+"\n",
			i, title, now,
		)
		if _, err := f.WriteString(line); err != nil {
			return err
		}
	}

	return f.Sync()
}
