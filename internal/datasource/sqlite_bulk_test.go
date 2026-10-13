package datasource

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// Count actual prepared statements, not elapsed time. The underlying driver
// still executes every statement against SQLite, including BEGIN/COMMIT and
// the WAL reader/writer schedule. No query-count hint reaches production code.
func TestSQLiteBulkGraphQueryBudgetAndRefresh(t *testing.T) {
	for _, size := range []int{1, 64, 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path, writer := snapshotFixture(t, false)
			tx, err := writer.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for i := 0; i < size; i++ {
				id, target := fmt.Sprintf("bulk-%04d", i), fmt.Sprintf("bulk-%04d", i+1)
				for _, query := range []struct {
					sql  string
					args []any
				}{
					{"INSERT INTO issues VALUES (?, 'bulk issue', 'open', 2)", []any{id}},
					{"INSERT INTO dependencies VALUES (?, ?, 'blocks')", []any{id, target}},
					{"INSERT INTO labels VALUES (?, 'first'), (?, 'second')", []any{id, id}},
					{"INSERT INTO comments VALUES (2, ?, 'fixture', 'later', '2026-01-02T00:00:00Z'), (1, ?, 'fixture', 'earlier', '2026-01-01T00:00:00Z')", []any{id, id}},
				} {
					if _, err := tx.Exec(query.sql, query.args...); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			queries := make(map[string]int)
			reader := snapshotReaderUsingDriver(t, path, &snapshotInterleaveDriver{
				inner: &sqlite.Driver{}, onRow: func() error { return nil },
				prepareErr: func(query string) error { queries[query]++; return nil },
			})
			reader.db.SetMaxOpenConns(1)
			for _, phase := range []string{"first", "refreshed"} {
				clear(queries)
				if phase == "refreshed" {
					if _, err := writer.Exec("UPDATE labels SET label='refreshed' WHERE label='first'"); err != nil {
						t.Fatal(err)
					}
				}
				issues, err := reader.LoadIssueAuthority()
				if err != nil || len(issues) != size+3 {
					t.Fatalf("load %s: %d issues, %v", phase, len(issues), err)
				}
				if report := reader.LoadReport(); report.WarningCount != 0 || len(report.TombstoneIDs) != 1 || report.TombstoneIDs[0] != "deleted" {
					t.Fatalf("lost authority: %+v", report)
				}
				for _, issue := range issues {
					if !strings.HasPrefix(issue.ID, "bulk-") {
						continue
					}
					if len(issue.Labels) != 2 || !((issue.Labels[0] == phase && issue.Labels[1] == "second") || (issue.Labels[1] == phase && issue.Labels[0] == "second")) {
						t.Fatalf("labels lost during %s refresh: %+v", phase, issue)
					}
					if len(issue.Comments) != 2 || issue.Comments[0].Text != "earlier" || issue.Comments[1].Text != "later" || issue.Comments[0].IssueID != issue.ID {
						t.Fatalf("comments lost or reordered: %+v", issue)
					}
					var ordinal int
					if _, err := fmt.Sscanf(issue.ID, "bulk-%d", &ordinal); err != nil {
						t.Fatal(err)
					}
					if len(issue.Dependencies) != 1 || issue.Dependencies[0].IssueID != issue.ID || issue.Dependencies[0].DependsOnID != fmt.Sprintf("bulk-%04d", ordinal+1) {
						t.Fatalf("edge lost (including missing final target): %+v", issue)
					}
				}
				// One table read each, independent of issue/edge count. Schema
				// inspection is bounded too; no hidden per-issue metadata query.
				total := 0
				for _, n := range queries {
					total += n
				}
				if total > 9 {
					t.Fatalf("%d issues issued %d statements: %v", size+3, total, queries)
				}
				for _, table := range []string{"issues", "dependencies", "labels", "comments"} {
					count := 0
					for query, n := range queries {
						if strings.Contains(query, "FROM "+table) {
							count += n
						}
					}
					if count != 1 {
						t.Fatalf("%s read %d times for %d issues: %v", table, count, size+3, queries)
					}
				}
				assertSnapshotReaderReleased(t, reader, writer)
			}
		})
	}
}

func TestSQLiteBulkGraphPreservesDuplicateAndMissingAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beads.db")
	writer, err := sql.Open("sqlite", sqliteFileDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL;
CREATE TABLE issues (id TEXT, title TEXT, status TEXT, labels TEXT);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);
CREATE TABLE labels (issue_id TEXT, label TEXT);
CREATE TABLE comments (id INTEGER, issue_id TEXT, author TEXT, text TEXT, created_at TEXT);
INSERT INTO issues VALUES ('duplicate', 'invalid first', '', NULL),
 ('duplicate', 'valid second', 'open', NULL), ('inline', 'inline labels', 'open', '["kept"]'),
 ('deleted', 'tombstone', 'tombstone', NULL);
INSERT INTO dependencies VALUES ('duplicate', 'absent-blocker', 'blocks'), ('inline', 'deleted', 'blocks'), ('orphan', 'duplicate', 'blocks');
INSERT INTO labels VALUES ('duplicate', 'one'), ('duplicate', 'two'), ('inline', 'not-inline'), ('orphan', 'not-an-issue');
INSERT INTO comments VALUES (1, 'duplicate', 'fixture', 'kept comment', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	issues, err := reader.LoadIssueAuthority()
	if err != nil || len(issues) != 3 {
		t.Fatalf("load: %+v, %v", issues, err)
	}
	for _, issue := range issues {
		switch issue.ID {
		case "duplicate":
			if issue.Title != "valid second" || len(issue.Labels) != 2 || len(issue.Comments) != 1 || len(issue.Dependencies) != 1 || issue.Dependencies[0].DependsOnID != "absent-blocker" {
				t.Fatalf("first invalid row stole the valid row's authority: %+v", issue)
			}
		case "inline":
			if len(issue.Labels) != 1 || issue.Labels[0] != "kept" || len(issue.Dependencies) != 1 || issue.Dependencies[0].DependsOnID != "deleted" {
				t.Fatalf("inline/tombstone authority changed: %+v", issue)
			}
		case "deleted":
			if !issue.Status.IsTombstone() {
				t.Fatal("tombstone resurrected")
			}
		default:
			t.Fatalf("orphan relation invented an issue: %+v", issue)
		}
	}
	if report := reader.LoadReport(); report.Errors != 1 || report.ReadErrors != 0 || report.WarningCount != 1 {
		t.Fatalf("duplicate/error accounting changed: %+v", report)
	}
	assertSnapshotReaderReleased(t, reader, writer)
}

func TestSQLiteBulkGraphReportsRelatedReadFailures(t *testing.T) {
	for _, table := range []string{"dependencies", "labels", "comments"} {
		t.Run(table, func(t *testing.T) {
			path, writer := snapshotFixture(t, false)
			reader := snapshotReaderUsingDriver(t, path, &snapshotInterleaveDriver{
				inner: &sqlite.Driver{}, onRow: func() error { return nil },
				prepareErr: func(query string) error {
					if strings.Contains(query, "FROM "+table) {
						return errors.New("injected unreadable related table")
					}
					return nil
				},
			})
			issues, err := reader.LoadIssueAuthority()
			if err != nil || len(issues) != 3 {
				t.Fatalf("readable issues must remain inspectable: %d, %v", len(issues), err)
			}
			if report := reader.LoadReport(); report.ReadErrors != 1 || report.WarningCount != 1 || !strings.Contains(report.Warnings[0], "injected unreadable") {
				t.Fatalf("failed table read was hidden or repeated per issue: %+v", report)
			}
			assertSnapshotReaderReleased(t, reader, writer)
		})
	}
}

func TestSQLiteBulkGraphReportsMalformedRelatedRows(t *testing.T) {
	path, writer := snapshotFixture(t, false)
	if _, err := writer.Exec(`INSERT INTO labels VALUES ('a-job', NULL);
INSERT INTO dependencies VALUES ('a-job', NULL, 'blocks'), ('a-job', 'blocker', '   ');
INSERT INTO comments VALUES (2, 'a-job', NULL, 'bad author', NULL);`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	issues, err := reader.LoadIssueAuthority()
	if err != nil || len(issues) != 3 {
		t.Fatalf("readable issues lost: %d, %v", len(issues), err)
	}
	if report := reader.LoadReport(); report.ReadErrors != 4 || report.WarningCount != 4 {
		t.Fatalf("malformed related rows lost their authority warnings: %+v", report)
	}
	for _, issue := range issues {
		if issue.ID == "a-job" && (len(issue.Dependencies) != 1 || len(issue.Labels) != 1 || len(issue.Comments) != 1) {
			t.Fatalf("valid related rows were lost with invalid ones: %+v", issue)
		}
	}
	assertSnapshotReaderReleased(t, reader, writer)
}

// The issue cursor is closed before bulk hydration. That must not end the
// transaction: a commit between table reads cannot tear one returned graph.
func TestSQLiteBulkGraphKeepsSnapshotBetweenTables(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("full_%t", full), func(t *testing.T) {
			path, writer := snapshotFixture(t, full)
			committed := false
			reader := snapshotReaderUsingDriver(t, path, &snapshotInterleaveDriver{
				inner: &sqlite.Driver{}, onRow: func() error { return nil },
				prepareErr: func(query string) error {
					if committed || !strings.Contains(query, "FROM dependencies") {
						return nil
					}
					_, err := writer.Exec(`BEGIN;
UPDATE issues SET title='after', status='closed' WHERE id='a-job';
UPDATE labels SET label='after' WHERE issue_id='a-job';
DELETE FROM dependencies WHERE issue_id='a-job';
UPDATE comments SET text='after' WHERE issue_id='a-job';
COMMIT;`)
					committed = err == nil
					return err
				},
			})
			for _, phase := range []string{"before", "after"} {
				issues, err := reader.LoadIssueAuthority()
				if err != nil || !committed || len(issues) != 3 || reader.LoadReport().WarningCount != 0 {
					t.Fatalf("load %s: committed=%t count=%d error=%v report=%+v", phase, committed, len(issues), err, reader.LoadReport())
				}
				for _, issue := range issues {
					if issue.ID != "a-job" {
						continue
					}
					if issue.Title != phase || len(issue.Labels) != 1 || issue.Labels[0] != phase || len(issue.Comments) != 1 || issue.Comments[0].Text != phase {
						t.Fatalf("mixed snapshot at %s: %+v", phase, issue)
					}
					if phase == "before" {
						if issue.Status != "open" || len(issue.Dependencies) != 1 || issue.Dependencies[0].DependsOnID != "blocker" {
							t.Fatalf("snapshot lost its blocker: %+v", issue)
						}
					} else if issue.Status != "closed" || len(issue.Dependencies) != 0 {
						t.Fatalf("refresh missed concurrent commit: %+v", issue)
					}
				}
				assertSnapshotReaderReleased(t, reader, writer)
			}
		})
	}
}
