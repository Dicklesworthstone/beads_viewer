package datasource

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

// Live reloads construct readiness from LoadIssueAuthority without carrying a
// LoadReport. A partial read must fail there, not certify a missing blocker or
// an invalid deferral as ready work. Diagnostic LoadIssues remains tolerant.
func TestSQLiteAuthorityRejectsIncompleteSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, damage, repair string }{
		{"null_blocker", "UPDATE dependencies SET depends_on_id=NULL", "UPDATE dependencies SET depends_on_id='blocker'"},
		{"invalid_edge_type", "UPDATE dependencies SET type='   '", "UPDATE dependencies SET type='blocks'"},
		{"unreadable_edge_schema", "ALTER TABLE dependencies RENAME COLUMN depends_on_id TO hidden_target", "ALTER TABLE dependencies RENAME COLUMN hidden_target TO depends_on_id"},
		{"invalid_deferral", "ALTER TABLE issues ADD COLUMN defer_until TEXT; UPDATE issues SET defer_until='not-a-date' WHERE id='a-job'", "UPDATE issues SET defer_until=NULL"},
		{"dropped_blocker", "UPDATE issues SET priority='unreadable' WHERE id='blocker'", "UPDATE issues SET priority=2 WHERE id='blocker'"},
		{"invalid_label", "UPDATE labels SET label=NULL", "UPDATE labels SET label='before'"},
		{"invalid_comment", "UPDATE comments SET author=NULL", "UPDATE comments SET author='fixture'"},
		{"all_rows_rejected", "UPDATE issues SET status=' '", "UPDATE issues SET status=CASE WHEN id='deleted' THEN 'tombstone' ELSE 'open' END"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, writer := snapshotFixture(t, false)
			if _, err := writer.Exec(tc.damage); err != nil {
				t.Fatal(err)
			}
			reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			payload := func() [2][]byte {
				t.Helper()
				var value [2][]byte
				for i, suffix := range []string{"", "-wal"} {
					value[i], err = os.ReadFile(path + suffix)
					if err != nil {
						t.Fatal(err)
					}
				}
				return value
			}
			before := payload()
			issues, err := reader.LoadIssueAuthority()
			if err == nil || issues != nil {
				t.Fatalf("incomplete source certified as authority: issues=%+v err=%v report=%+v", issues, err, reader.LoadReport())
			}
			if !strings.Contains(err.Error(), "incomplete SQLite issue snapshot") {
				t.Fatalf("read loss was not diagnosed: %v", err)
			}
			report := reader.LoadReport()
			if report.Errors+report.ReadErrors == 0 || report.WarningCount == 0 {
				t.Fatalf("refusal lost the read-loss evidence: %+v", report)
			}
			// Refusing graph authority must not make a damaged source impossible
			// to inspect or silently erase the existing tolerant read contract.
			visible, err := reader.LoadIssues()
			if err != nil || len(visible) != report.Valid-len(report.TombstoneIDs) {
				t.Fatalf("diagnostic rows lost: count=%d err=%v report=%+v", len(visible), err, report)
			}
			after := payload()
			for i := range before {
				if !bytes.Equal(before[i], after[i]) {
					t.Fatal("read/refusal changed durable database or WAL bytes")
				}
			}
			assertSnapshotReaderReleased(t, reader, writer)
			if _, err := writer.Exec(tc.repair); err != nil {
				t.Fatal(err)
			}
			issues, err = reader.LoadIssueAuthority()
			if err != nil || len(issues) != 3 || reader.LoadReport().WarningCount != 0 {
				t.Fatalf("repaired source could not regain authority: count=%d err=%v report=%+v", len(issues), err, reader.LoadReport())
			}
			for _, issue := range issues {
				if issue.ID == "a-job" && (len(issue.Dependencies) != 1 || issue.Dependencies[0].DependsOnID != "blocker") {
					t.Fatalf("fresh authority lost its blocker: %+v", issue)
				}
			}
			assertSnapshotReaderReleased(t, reader, writer)
		})
	}
}

// Real SQLite executes the unaffected statements. The related-table failure
// occurs through the existing driver boundary, then clears with NO file edit.
// A later read must retry, regain authority, and release its WAL reader mark.
func TestSQLiteAuthorityRetriesRelatedReadAfterTransientFailure(t *testing.T) {
	for _, table := range []string{"dependencies", "labels", "comments"} {
		t.Run(table, func(t *testing.T) {
			path, writer := snapshotFixture(t, false)
			var fail atomic.Bool
			fail.Store(true)
			reader := snapshotReaderUsingDriver(t, path, &snapshotInterleaveDriver{
				inner: &sqlite.Driver{}, onRow: func() error { return nil },
				prepareErr: func(query string) error {
					if fail.Load() && strings.Contains(query, "FROM "+table) {
						return errors.New("transient related read failure")
					}
					return nil
				},
			})
			reader.db.SetMaxOpenConns(1)
			issues, err := reader.LoadIssueAuthority()
			if err == nil || issues != nil || !strings.Contains(err.Error(), "transient related read failure") {
				t.Fatalf("failed %s read became authoritative: %+v, %v", table, issues, err)
			}
			fail.Store(false)
			issues, err = reader.LoadIssueAuthority()
			if err != nil || len(issues) != 3 || reader.LoadReport().ReadErrors != 0 {
				t.Fatalf("retry kept old failure: count=%d err=%v report=%+v", len(issues), err, reader.LoadReport())
			}
			assertSnapshotReaderReleased(t, reader, writer)
		})
	}
}

func TestSQLiteAuthorityAllowsCleanEmptySnapshot(t *testing.T) {
	path, writer := snapshotFixture(t, false)
	if _, err := writer.Exec("DELETE FROM issues; DELETE FROM labels; DELETE FROM dependencies; DELETE FROM comments"); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	issues, err := reader.LoadIssueAuthority()
	if err != nil || len(issues) != 0 || reader.LoadReport().WarningCount != 0 {
		t.Fatalf("clean empty project refused: %+v, %v", issues, err)
	}
	assertSnapshotReaderReleased(t, reader, writer)
}
