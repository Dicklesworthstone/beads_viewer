package datasource

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Interpose only after SQLite has actually stepped the first issue row. This
// is a real WAL reader/writer interleaving, not a sleep or a synthetic snapshot:
// the independent writer commits before the loader queries labels/edges/comments.
// The wrapper deliberately uses database/sql's standard prepared-statement
// fallback, so every statement and transaction still executes in modernc.
type snapshotInterleaveDriver struct {
	inner driver.Driver
	onRow func() error
	once  sync.Once
	err   error
}

func (d *snapshotInterleaveDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &snapshotInterleaveConn{Conn: conn, owner: d}, nil
}

type snapshotInterleaveConn struct {
	driver.Conn
	owner *snapshotInterleaveDriver
}

func (c *snapshotInterleaveConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	if strings.Contains(query, "FROM issues") && !strings.Contains(query, "COUNT(*)") {
		return &snapshotInterleaveStmt{Stmt: stmt, owner: c.owner}, nil
	}
	return stmt, nil
}

type snapshotInterleaveStmt struct {
	driver.Stmt
	owner *snapshotInterleaveDriver
}

func (s *snapshotInterleaveStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.Stmt.Query(args)
	if err != nil {
		return nil, err
	}
	return &snapshotInterleaveRows{Rows: rows, owner: s.owner}, nil
}

type snapshotInterleaveRows struct {
	driver.Rows
	owner *snapshotInterleaveDriver
}

func (r *snapshotInterleaveRows) Next(values []driver.Value) error {
	if err := r.Rows.Next(values); err != nil {
		return err
	}
	r.owner.once.Do(func() { r.owner.err = r.owner.onRow() })
	return r.owner.err
}

var snapshotDriverSequence atomic.Uint64

func snapshotReaderWithInterleave(t *testing.T, path string, onRow func() error) *SQLiteReader {
	t.Helper()
	name := fmt.Sprintf("bv-snapshot-interleave-%d", snapshotDriverSequence.Add(1))
	sql.Register(name, &snapshotInterleaveDriver{inner: &sqlite.Driver{}, onRow: onRow})
	db, err := sql.Open(name, sqliteReadOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &SQLiteReader{db: db, path: path}
}

func snapshotFixture(t *testing.T, full bool) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "beads.db")
	writer, err := sql.Open("sqlite", sqliteFileDSN(path, "_pragma=busy_timeout(2000)&_pragma=wal_autocheckpoint(0)"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	writer.SetMaxOpenConns(1)
	columns := "id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER"
	if full {
		columns += `, description TEXT, issue_type TEXT DEFAULT 'task', assignee TEXT,
 estimated_minutes INTEGER, created_at DATETIME, updated_at DATETIME, due_date DATETIME,
 defer_until DATETIME, closed_at DATETIME, external_ref TEXT, compaction_level INTEGER,
 compacted_at DATETIME, compacted_at_commit TEXT, original_size INTEGER, design TEXT,
 acceptance_criteria TEXT, notes TEXT, source_repo TEXT, tombstone INTEGER`
	}
	_, err = writer.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE issues (` + columns + `);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
CREATE TABLE labels (issue_id TEXT, label TEXT);
CREATE TABLE comments (id INTEGER, issue_id TEXT, author TEXT, text TEXT, created_at TEXT);
INSERT INTO issues (id, title, status, priority) VALUES
 ('a-job', 'before', 'open', 2), ('blocker', 'Blocking work', 'open', 2),
 ('deleted', 'Retained dependency authority', 'tombstone', 2);
INSERT INTO dependencies VALUES ('a-job', 'blocker', 'blocks');
INSERT INTO labels VALUES ('a-job', 'before');
INSERT INTO comments VALUES (1, 'a-job', 'fixture', 'before', '2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	return path, writer
}

func assertSnapshotReaderReleased(t *testing.T, reader *SQLiteReader, writer *sql.DB) {
	t.Helper()
	if stats := reader.db.Stats(); stats.InUse != 0 {
		t.Fatalf("load retained a connection/transaction: %+v", stats)
	}
	// A retained reader mark prevents TRUNCATE. Checking only pool statistics
	// would miss a leaked SQLite statement or a transaction left on an idle conn.
	var busy, frames, checkpointed int
	if err := writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("read snapshot blocked checkpoint: busy=%d frames=%d backfill=%d err=%v", busy, frames, checkpointed, err)
	}
}

func TestSQLiteIssueSnapshotAcrossConcurrentCommit(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("full_schema_%t", full), func(t *testing.T) {
			path, writer := snapshotFixture(t, full)
			committed := false
			reader := snapshotReaderWithInterleave(t, path, func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := writer.ExecContext(ctx, `BEGIN;
UPDATE issues SET title='after', status='closed' WHERE id='a-job';
DELETE FROM dependencies WHERE issue_id='a-job';
UPDATE labels SET label='after' WHERE issue_id='a-job';
UPDATE comments SET text='after' WHERE issue_id='a-job';
COMMIT;`)
				committed = err == nil
				return err
			})
			for _, phase := range []string{"before", "after"} {
				issues, err := reader.LoadIssueAuthority()
				if err != nil {
					t.Fatal(err)
				}
				if !committed {
					t.Fatal("writer did not commit inside the live issue query")
				}
				if len(issues) != 3 || reader.LoadReport().WarningCount != 0 {
					t.Fatalf("snapshot lost source authority: %+v; report=%+v", issues, reader.LoadReport())
				}
				var job *model.Issue
				for i := range issues {
					if issues[i].ID == "a-job" {
						job = &issues[i]
					}
				}
				if job == nil || job.Title != phase || len(job.Labels) != 1 || job.Labels[0] != phase || len(job.Comments) != 1 || job.Comments[0].Text != phase {
					t.Fatalf("mixed WAL generations in %s snapshot: %+v", phase, job)
				}
				if phase == "before" {
					if job.Status != "open" || len(job.Dependencies) != 1 || job.Dependencies[0].DependsOnID != "blocker" {
						t.Fatalf("concurrent commit removed a live snapshot's blocker: %+v", job)
					}
				} else if job.Status != "closed" || len(job.Dependencies) != 0 {
					t.Fatalf("next refresh did not see the committed state: %+v", job)
				}
				// Both committed states are non-actionable. Mixing the old open
				// issue with its newly deleted edge would invent ready work.
				if model.NewReadinessIndex(issues).Ready("a-job", time.Now()) {
					t.Fatal("a torn snapshot invented ready work that never existed")
				}
				assertSnapshotReaderReleased(t, reader, writer)
			}
		})
	}
}

func TestSQLiteIssueSnapshotUsesOnePooledConnection(t *testing.T) {
	path, writer := snapshotFixture(t, false)
	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.db.SetMaxOpenConns(1)
	type outcome struct {
		issues []model.Issue
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		issues, err := reader.LoadIssues()
		done <- outcome{issues: issues, err: err}
	}()
	select {
	case got := <-done:
		if got.err != nil || len(got.issues) != 2 || reader.LoadReport().WarningCount != 0 {
			t.Fatalf("single-connection graph load: %+v; report=%+v", got, reader.LoadReport())
		}
	case <-time.After(5 * time.Second):
		// Closing the pool also wakes any accidental nested db.Query waiter.
		reader.Close()
		t.Fatal("graph load borrowed a second pooled connection while its issue cursor was open")
	}
	assertSnapshotReaderReleased(t, reader, writer)
}

func TestSQLiteIssueSnapshotReleasesFailedRead(t *testing.T) {
	path, writer := snapshotFixture(t, false)
	injected := errors.New("injected issue iteration failure")
	reader := snapshotReaderWithInterleave(t, path, func() error { return injected })
	issues, err := reader.LoadIssueAuthority()
	if !errors.Is(err, injected) || len(issues) != 0 {
		t.Fatalf("failed iteration published a partial graph: %+v, %v", issues, err)
	}
	assertSnapshotReaderReleased(t, reader, writer)
	if _, err := writer.Exec("DROP TABLE issues"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadIssueAuthority(); err == nil {
		t.Fatal("unreadable source was accepted")
	}
	assertSnapshotReaderReleased(t, reader, writer)
}
