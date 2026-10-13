package ui

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"
)

// The datasource deliberately supports partial diagnostic reads. Live worker
// reloads must not mistake those for a complete readiness graph. Exercise the
// real SQLite reader with an unreadable blocker and an invalid deferral, then
// require heartbeat recovery with no file watcher and no manual refresh.
func TestBackgroundWorkerWithholdsPartialSQLiteAuthority(t *testing.T) {
	for _, tc := range []struct{ name, damage, repair string }{
		{"blocker", "UPDATE dependencies SET depends_on_id=NULL", "UPDATE dependencies SET depends_on_id='blocker'"},
		{"deferral", "UPDATE issues SET defer_until='unreadable' WHERE id='job'", "UPDATE issues SET defer_until=NULL WHERE id='job'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err := writer.Exec(`PRAGMA journal_mode=WAL;
PRAGMA wal_autocheckpoint=0;
CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER, issue_type TEXT, defer_until TEXT);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
INSERT INTO issues VALUES ('job', 'Dependent job', 'open', 2, 'task', NULL),
 ('blocker', 'Unfinished blocker', 'open', 2, 'task', NULL);
INSERT INTO dependencies VALUES ('job', 'blocker', 'blocks');`); err != nil {
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
			if old == nil || len(old.Issues) != 2 || w.LastError() != nil {
				t.Fatalf("initial graph failed: %v", w.LastError())
			}
			for len(w.msgCh) != 0 {
				<-w.msgCh
			}
			if _, err := writer.Exec(tc.damage); err != nil {
				t.Fatal(err)
			}
			payload := func() [2][]byte {
				t.Helper()
				var value [2][]byte
				for i, suffix := range []string{"", "-wal"} {
					var err error
					value[i], err = os.ReadFile(path + suffix)
					if err != nil {
						t.Fatal(err)
					}
				}
				return value
			}
			before := payload()
			w.process()
			failure := w.LastError()
			if failure == nil || failure.Phase != "load" || !strings.Contains(failure.Error(), "incomplete SQLite issue snapshot") {
				t.Fatalf("partial SQLite graph did not become a retryable load failure: %v", failure)
			}
			if w.GetSnapshot() != old || w.LastHash() != old.DataHash {
				t.Fatal("partial graph replaced the last complete snapshot")
			}
			after := payload()
			if !bytes.Equal(before[0], after[0]) || !bytes.Equal(before[1], after[1]) {
				t.Fatal("refusing a partial graph changed main/WAL bytes")
			}
			for len(w.msgCh) != 0 {
				msg := <-w.msgCh
				if _, ok := msg.(SnapshotReadyMsg); ok {
					t.Fatal("partial graph was published to the UI")
				}
			}
			if _, err := writer.Exec(tc.repair); err != nil {
				t.Fatal(err)
			}
			before = payload()
			// Age only the accepted error: exact backoff boundaries are tested
			// elsewhere. Neither the watcher nor TriggerRefresh can rescue this.
			w.mu.Lock()
			w.lastError.Time = time.Now().Add(-time.Minute)
			w.mu.Unlock()
			done := make(chan struct{})
			go w.processLoop(w.loopCtx, done)
			defer func() {
				w.loopCancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("heartbeat loop did not stop")
				}
			}()
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			var fresh *DataSnapshot
			for fresh == nil {
				select {
				case msg := <-w.msgCh:
					if ready, ok := msg.(SnapshotReadyMsg); ok {
						fresh = ready.Snapshot
					}
				case <-timer.C:
					t.Fatalf("repaired graph not published: %v", w.LastError())
				}
			}
			if fresh == old || fresh.DataHash != old.DataHash || w.LastError() != nil || len(fresh.Issues) != 2 {
				t.Fatal("repaired graph did not republish the complete same-hash snapshot")
			}
			for _, issue := range fresh.Issues {
				if issue.ID == "job" && (len(issue.Dependencies) != 1 || issue.Dependencies[0].DependsOnID != "blocker") {
					t.Fatalf("recovery lost the unfinished blocker: %+v", issue)
				}
			}
			if w.watcher.IsStarted() {
				t.Fatal("test accidentally enabled file notifications")
			}
			after = payload()
			if !bytes.Equal(before[0], after[0]) || !bytes.Equal(before[1], after[1]) {
				t.Fatal("heartbeat recovery changed main/WAL bytes")
			}
			var integrity string
			if err := writer.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity after recovery: %q, %v", integrity, err)
			}
			var busy, frames, checkpointed int
			if err := writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
				t.Fatalf("recovery retained a reader: busy=%d frames=%d backfill=%d err=%v", busy, frames, checkpointed, err)
			}
		})
	}
}
