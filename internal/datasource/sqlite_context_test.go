package datasource

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Interpose at the driver's context boundary, not at a production helper.
// SQLite still executes the transaction and every unblocked query. Blocking
// one statement until its context ends makes missing propagation deterministic.
type cancelProbeDriver struct {
	inner   driver.Driver
	onQuery func(context.Context, string) error
}

func (d *cancelProbeDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &cancelProbeConn{Conn: c, owner: d}, nil
}

type cancelProbeConn struct {
	driver.Conn
	owner *cancelProbeDriver
}

func (c *cancelProbeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.owner.onQuery(ctx, query); err != nil {
		return nil, err
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *cancelProbeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c, ok := c.Conn.(driver.ConnBeginTx); ok {
		return c.BeginTx(ctx, opts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Conn.Begin()
}

func TestSQLiteContextCancellationReleasesSnapshot(t *testing.T) {
	for _, statement := range []string{"PRAGMA table_info(issues)", "FROM issues i", "FROM labels", "FROM dependencies", "FROM comments"} {
		t.Run(statement, func(t *testing.T) {
			path, writer := snapshotFixture(t, false)
			if _, err := writer.Exec("PRAGMA busy_timeout=0"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			escape := make(chan struct{})
			defer close(escape) // also releases a broken, non-context baseline
			var blocked atomic.Bool
			d := &cancelProbeDriver{inner: &sqlite.Driver{}, onQuery: func(got context.Context, query string) error {
				if strings.Contains(query, statement) && blocked.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-got.Done():
						return got.Err()
					case <-escape:
						return errors.New("test released an uncancelled query")
					}
				}
				return nil
			}}
			name := fmt.Sprintf("bv-cancel-%d", snapshotDriverSequence.Add(1))
			sql.Register(name, d)
			db, err := sql.Open(name, sqliteReadOnlyDSN(path))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			db.SetMaxOpenConns(1)
			reader := &SQLiteReader{db: db, path: path}
			done := make(chan error, 1)
			go func() {
				issues, err := reader.LoadIssueAuthorityContext(ctx)
				if issues != nil {
					err = fmt.Errorf("cancelled read published %d issues: %v", len(issues), err)
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("read did not reach the selected SQLite statement")
			}
			if _, err := writer.Exec("INSERT INTO issues VALUES ('fresh', 'Committed during read', 'open', 2)"); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(statement, "FROM") {
				// Earlier schema reads already pinned this transaction. Prove
				// that it really retains a WAL mark before testing its release.
				var busy, frames, checkpointed int
				if err := writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &checkpointed); err != nil || busy != 1 {
					t.Fatalf("fixture did not pin a snapshot: busy=%d, %v", busy, err)
				}
			}
			readBytes := func(suffix string) []byte {
				t.Helper()
				b, err := os.ReadFile(path + suffix)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			main, wal := readBytes(""), readBytes("-wal")
			shmBefore, err := os.Stat(path + "-shm")
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation identity was lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled query retained its read operation")
			}
			if !bytes.Equal(main, readBytes("")) || !bytes.Equal(wal, readBytes("-wal")) {
				t.Fatal("cancelling a reader changed durable database or WAL bytes")
			}
			shmAfter, err := os.Stat(path + "-shm")
			if err != nil || !os.SameFile(shmBefore, shmAfter) {
				t.Fatal("cancellation replaced the shared WAL index")
			}
			// A fresh context on the same reader must work; the caller's
			// cancelled context must not be retained in the pool or reader.
			freshCtx, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			issues, err := reader.LoadIssueAuthorityContext(freshCtx)
			if err != nil || len(issues) != 4 || reader.LoadReport().ReadErrors != 0 {
				t.Fatalf("fresh read after cancellation: count=%d, %v", len(issues), err)
			}
			assertSnapshotReaderReleased(t, reader, writer)
		})
	}
}

func TestSQLiteContextBoundsPoolWaitsAndMetadata(t *testing.T) {
	path, writer := snapshotFixture(t, true)
	reader, err := NewSQLiteReaderContext(context.Background(), DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.db.SetMaxOpenConns(1)
	conn, err := reader.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, tc := range []struct {
		name string
		read func(context.Context) error
	}{
		{"authority", func(ctx context.Context) error { _, err := reader.LoadIssueAuthorityContext(ctx); return err }},
		{"snapshot", func(ctx context.Context) error { _, _, err := reader.LoadIssueSnapshotContext(ctx); return err }},
		{"count", func(ctx context.Context) error { _, err := reader.CountIssuesContext(ctx); return err }},
		{"modified", func(ctx context.Context) error { _, err := reader.GetLastModifiedContext(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tc.read(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("pool wait lost deadline: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pool wait ignored deadline")
			}
		})
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if count, err := reader.CountIssues(); err != nil || count != 2 {
		t.Fatalf("legacy read after cancelled pool waits: %d, %v", count, err)
	}
	assertSnapshotReaderReleased(t, reader, writer)
}

func TestSQLiteCancelledOpenDoesNotTouchSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader, err := NewSQLiteReaderContext(ctx, DataSource{Type: SourceTypeSQLite, Path: path})
	if reader != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open touched discovery or SQLite: reader=%v err=%v", reader, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancelled open created a source: %v", err)
	}
}
