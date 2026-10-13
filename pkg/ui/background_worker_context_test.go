package ui

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	_ "modernc.org/sqlite"
)

func TestWorkerReadLifetimeFollowsAdmittedLoop(t *testing.T) {
	w := loadRetryWorker(t)
	w.processingTimeout = time.Second
	old := &DataSnapshot{DataHash: "last usable", phase2Ready: true}
	w.snapshot = old
	entered := make(chan context.Context, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.processSnapshotContext(func(ctx context.Context, _ bool) snapshotBuildResult {
			entered <- ctx
			<-ctx.Done()
			return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: ctx.Err(), Time: time.Now()}}
		}, nil)
	}()
	var admitted context.Context
	select {
	case admitted = <-entered:
	case <-time.After(time.Second):
		t.Fatal("build did not start")
	}
	oldCancel := w.loopCancel
	// Match the recovery boundary: replace the loop/generation before
	// cancelling the old one. A delayed read must not adopt the new context.
	w.mu.Lock()
	w.loopCtx, w.loopCancel = context.WithCancel(w.ctx)
	w.generation++
	w.state = WorkerIdle
	w.mu.Unlock()
	defer w.loopCancel()
	oldCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old SQLite load did not observe loop cancellation")
	}
	if !errors.Is(admitted.Err(), context.Canceled) || w.LastError() != nil || w.GetSnapshot() != old {
		t.Fatal("invalidated read changed the replacement generation")
	}
	select {
	case msg := <-w.msgCh:
		t.Fatalf("old read published after recovery: %#v", msg)
	default:
	}
	var next context.Context
	fresh := &DataSnapshot{DataHash: "fresh", phase2Ready: true}
	w.processSnapshotContext(func(ctx context.Context, _ bool) snapshotBuildResult {
		next = ctx
		if ctx.Err() != nil {
			return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: ctx.Err()}}
		}
		return snapshotBuildResult{snapshot: fresh, clearError: true}
	}, nil)
	if w.GetSnapshot() != fresh || w.LastError() != nil || next == admitted || next.Err() == nil {
		t.Fatal("replacement did not get a fresh, subsequently released read lifetime")
	}
}

func TestWorkerReadDeadlineRetainsLastSnapshotAndCanRetry(t *testing.T) {
	w := loadRetryWorker(t)
	w.processingTimeout = 20 * time.Millisecond
	old := &DataSnapshot{DataHash: "old", phase2Ready: true}
	w.snapshot, w.lastHash = old, old.DataHash
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.processSnapshotContext(func(ctx context.Context, _ bool) snapshotBuildResult {
			<-ctx.Done()
			return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: ctx.Err(), Time: time.Now()}}
		}, nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read exceeded its worker processing budget")
	}
	failure := w.LastError()
	if failure == nil || failure.Phase != "load" || !errors.Is(failure, context.DeadlineExceeded) || w.GetSnapshot() != old {
		t.Fatalf("deadline did not preserve the last good snapshot: %v", failure)
	}
	attempt := w.reserveLoadRetry(w.loopCtx, failure.Time.Add(loadRetryDelay(1)))
	if attempt == nil {
		t.Fatal("a timed-out load became permanently ineligible for recovery")
	}
	fresh := &DataSnapshot{DataHash: old.DataHash, phase2Ready: true}
	w.processSnapshotContext(func(ctx context.Context, force bool) snapshotBuildResult {
		if ctx.Err() != nil || !force {
			return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: errors.New("retry reused expired context or deduped recovery")}}
		}
		return snapshotBuildResult{snapshot: fresh, clearError: true}
	}, attempt)
	if w.GetSnapshot() != fresh || w.LastError() != nil {
		t.Fatal("fresh retry failed after deadline")
	}
}

func TestWorkerRootCancellationReachesInFlightRead(t *testing.T) {
	w := loadRetryWorker(t)
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.processSnapshotContext(func(ctx context.Context, _ bool) snapshotBuildResult {
			close(entered)
			<-ctx.Done()
			return snapshotBuildResult{err: &WorkerError{Phase: "load", Cause: ctx.Err(), Time: time.Now()}}
		}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("build did not enter read")
	}
	// Stop marks the state before cancelling the root. No separate load
	// goroutine or read-specific global is needed to propagate that signal.
	w.mu.Lock()
	w.state = WorkerStopped
	w.mu.Unlock()
	w.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("root cancellation did not terminate the read")
	}
	if w.LastError() != nil {
		t.Fatal("shutdown published a spurious load failure")
	}
}

func TestWorkerContextSQLiteReloadPreservesAuthority(t *testing.T) {
	for _, extension := range []string{".db", ".SQLITE", ".sqlite3"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beads"+extension)
			writer, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.SetMaxOpenConns(1)
			if _, err := writer.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
INSERT INTO issues VALUES ('job', 'Job', 'open', 2), ('blocker', 'Hidden blocker', 'open', 2), ('deleted', 'Satisfied', 'tombstone', 2);
INSERT INTO dependencies VALUES ('job', 'blocker', 'blocks');`); err != nil {
				t.Fatal(err)
			}
			opts := loader.ParseOptions{IssueFilter: func(issue *model.Issue) bool { return issue.ID == "job" }}
			loaded, err := loadIssuesForReloadContext(context.Background(), path, opts)
			if err != nil || len(loaded.Issues) != 1 || loaded.Authority == nil || loaded.AuthorityHash == "" {
				t.Fatalf("reload lost complete authority or filtering: %+v, %v", loaded, err)
			}
			if loaded.Authority.Ready("job", time.Now()) {
				t.Fatal("filtered-out blocker was discarded from readiness")
			}
			if count, err := countIssuesForReloadContext(context.Background(), path); err != nil || count != 2 {
				t.Fatalf("metadata count lost tombstone semantics: %d, %v", count, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if loaded, err := loadIssuesForReloadContext(ctx, path, opts); !errors.Is(err, context.Canceled) || loaded.Issues != nil || loaded.Authority != nil {
				t.Fatalf("cancelled reload gained authority: %+v, %v", loaded, err)
			}
			if _, err := countIssuesForReloadContext(ctx, path); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled count ignored worker lifetime: %v", err)
			}
			if _, err := writer.Exec("UPDATE dependencies SET depends_on_id=NULL"); err != nil {
				t.Fatal(err)
			}
			if loaded, err := loadIssuesForReloadContext(context.Background(), path, opts); err == nil || loaded.Issues != nil {
				t.Fatal("context path weakened incomplete-authority rejection")
			}
			var busy, frames, checkpointed int
			if err := writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
				t.Fatalf("worker read retained WAL reader mark: busy=%d, %v", busy, err)
			}
		})
	}
}
