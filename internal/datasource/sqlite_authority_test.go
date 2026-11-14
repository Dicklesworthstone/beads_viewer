package datasource

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
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

func TestSQLiteAuthority_DependencyMetadataAndSpecialFlagsParityWithJSONL(t *testing.T) {
	path, writer := snapshotFixture(t, true)
	for _, stmt := range []string{
		"DELETE FROM issues; DELETE FROM labels; DELETE FROM dependencies; DELETE FROM comments",
		"ALTER TABLE issues ADD COLUMN created_by TEXT DEFAULT ''",
		"ALTER TABLE issues ADD COLUMN close_reason TEXT DEFAULT ''",
		"ALTER TABLE issues ADD COLUMN ephemeral INTEGER DEFAULT 0",
		"ALTER TABLE issues ADD COLUMN pinned INTEGER DEFAULT 0",
		"ALTER TABLE issues ADD COLUMN is_template INTEGER DEFAULT 0",
		"ALTER TABLE dependencies ADD COLUMN created_at TEXT",
		"ALTER TABLE dependencies ADD COLUMN created_by TEXT",
		`INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at, closed_at, created_by, close_reason, ephemeral, pinned, is_template) VALUES
			('br-1', 'Root issue', 'desc 1', 'closed', 1, 'task', '2026-11-10 12:30:45.123456', '2026-11-11T08:00:00Z', '2026-11-11T08:00:00Z', 'alice', 'shipped cleanly', 0, 0, 0),
			('br-2', 'Pinned child', 'desc 2', 'open', 2, 'feature', '2026-11-10T13:00:00Z', '2026-11-11T09:00:00Z', NULL, 'bob', '', 1, 1, 1)`,
		`INSERT INTO labels (issue_id, label) VALUES ('br-2', 'zeta'), ('br-2', 'alpha')`,
		`INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by) VALUES
			('br-2', 'br-1', 'blocks', '2026-11-10T13:05:00Z', 'bob')`,
	} {
		if _, err := writer.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	sqliteIssues, err := reader.LoadIssueAuthority()
	if err != nil {
		t.Fatalf("LoadIssueAuthority: %v", err)
	}
	if len(sqliteIssues) != 2 {
		t.Fatalf("len(sqliteIssues) = %d, want 2", len(sqliteIssues))
	}

	byID := make(map[string]model.Issue, len(sqliteIssues))
	for _, iss := range sqliteIssues {
		byID[iss.ID] = iss
	}
	br1 := byID["br-1"]
	wantCreated := time.Date(2026, 11, 10, 12, 30, 45, 123456000, time.UTC)
	if !br1.CreatedAt.Equal(wantCreated) {
		t.Fatalf("br-1 CreatedAt = %v, want %v", br1.CreatedAt, wantCreated)
	}
	if br1.CreatedBy != "alice" || br1.CloseReason != "shipped cleanly" {
		t.Fatalf("br-1 metadata = (%q, %q), want (alice, shipped cleanly)", br1.CreatedBy, br1.CloseReason)
	}
	br2 := byID["br-2"]
	if !br2.Ephemeral || !br2.Pinned || !br2.IsTemplate || br2.CreatedBy != "bob" {
		t.Fatalf("br-2 special flags/created_by mismatch: %+v", br2)
	}
	if len(br2.Labels) != 2 || br2.Labels[0] != "alpha" || br2.Labels[1] != "zeta" {
		t.Fatalf("br-2 labels not sorted deterministically: %v", br2.Labels)
	}
	if len(br2.Dependencies) != 1 || br2.Dependencies[0].CreatedBy != "bob" || !br2.Dependencies[0].CreatedAt.Equal(time.Date(2026, 11, 10, 13, 5, 0, 0, time.UTC)) {
		t.Fatalf("br-2 dependency metadata missing: %+v", br2.Dependencies)
	}

	// Round-trip through JSONL and verify identical ComputeDataHash.
	var jsonlIssues []model.Issue
	for _, iss := range sqliteIssues {
		raw, err := json.Marshal(iss)
		if err != nil {
			t.Fatalf("marshal %s: %v", iss.ID, err)
		}
		var parsed model.Issue
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", iss.ID, err)
		}
		jsonlIssues = append(jsonlIssues, parsed)
	}
	sqliteHash := analysis.ComputeDataHash(sqliteIssues)
	jsonlHash := analysis.ComputeDataHash(jsonlIssues)
	if sqliteHash != jsonlHash {
		t.Fatalf("SQLite ComputeDataHash (%s) != JSONL ComputeDataHash (%s)", sqliteHash, jsonlHash)
	}
}

func TestSQLiteAuthority_MinimalCommentsNullDepTypeAndInvalidTimestamps(t *testing.T) {
	path, writer := snapshotFixture(t, false)
	for _, stmt := range []string{
		"DROP TABLE comments",
		"CREATE TABLE comments (issue_id TEXT, text TEXT)",
		"INSERT INTO comments (issue_id, text) VALUES ('a-job', 'minimal comment text')",
		"UPDATE dependencies SET type = NULL WHERE issue_id = 'a-job'",
	} {
		if _, err := writer.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	issues, err := reader.LoadIssueAuthority()
	if err != nil || len(issues) != 3 {
		t.Fatalf("expected minimal comments + NULL dep type to succeed: len=%d err=%v", len(issues), err)
	}
	var gotJob model.Issue
	for _, iss := range issues {
		if iss.ID == "a-job" {
			gotJob = iss
		}
	}
	if len(gotJob.Comments) != 1 || gotJob.Comments[0].Text != "minimal comment text" {
		t.Fatalf("minimal comments mismatch: %+v", gotJob.Comments)
	}
	if len(gotJob.Dependencies) != 1 || !gotJob.Dependencies[0].Type.IsBlocking() {
		t.Fatalf("NULL dep type must retain blocking semantics: %+v", gotJob.Dependencies)
	}

	// Malformed non-empty created_at in dependencies must increment ReadErrors and reject authority.
	for _, stmt := range []string{
		"ALTER TABLE dependencies ADD COLUMN created_at TEXT",
		"UPDATE dependencies SET created_at = 'not-a-timestamp' WHERE issue_id = 'a-job'",
	} {
		if _, err := writer.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	if bad, badErr := reader.LoadIssueAuthority(); badErr == nil || bad != nil {
		t.Fatalf("expected invalid dependency created_at to reject authority, got issues=%+v err=%v", bad, badErr)
	}
}
