package datasource

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// readerFactory creates an IssueReader from a temp directory with test fixtures.
type readerFactory struct {
	name  string
	setup func(t *testing.T) IssueReader
}

// readerFactories returns factories for every IssueReader backend under test.
func readerFactories() []readerFactory {
	return []readerFactory{
		{
			name: "SQLite",
			setup: func(t *testing.T) IssueReader {
				t.Helper()
				dir := t.TempDir()
				dbPath := filepath.Join(dir, "beads.db")
				createContractTestSQLiteDB(t, dbPath)
				src := DataSource{Type: SourceTypeSQLite, Path: dbPath}
				r, err := NewReader(src)
				if err != nil {
					t.Fatalf("NewReader(SQLite): %v", err)
				}
				t.Cleanup(func() { r.Close() })
				return r
			},
		},
		{
			name: "JSONL",
			setup: func(t *testing.T) IssueReader {
				t.Helper()
				dir := t.TempDir()
				jsonlPath := filepath.Join(dir, "issues.jsonl")
				createContractTestJSONL(t, jsonlPath)
				src := DataSource{Type: SourceTypeJSONLLocal, Path: jsonlPath}
				r, err := NewReader(src)
				if err != nil {
					t.Fatalf("NewReader(JSONL): %v", err)
				}
				t.Cleanup(func() { r.Close() })
				return r
			},
		},
	}
}

// --- Contract tests: every backend must pass all of these ---

func TestReaderContract_LoadIssues(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)
			issues, err := r.LoadIssues()
			if err != nil {
				t.Fatalf("LoadIssues: %v", err)
			}
			if len(issues) != 3 {
				t.Errorf("want 3 issues, got %d", len(issues))
			}
			ids := map[string]bool{}
			for _, iss := range issues {
				ids[iss.ID] = true
			}
			for _, want := range []string{"CTR-1", "CTR-2", "CTR-3"} {
				if !ids[want] {
					t.Errorf("missing issue %s", want)
				}
			}
		})
	}
}

func TestReaderContract_LoadIssuesFiltered(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)
			issues, err := r.LoadIssuesFiltered(func(iss *model.Issue) bool {
				return iss.Status == "open"
			})
			if err != nil {
				t.Fatalf("LoadIssuesFiltered: %v", err)
			}
			if len(issues) != 2 {
				t.Errorf("want 2 open issues, got %d", len(issues))
			}
		})
	}
}

func TestReaderContract_LoadIssuesFiltered_Nil(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)
			issues, err := r.LoadIssuesFiltered(nil)
			if err != nil {
				t.Fatalf("LoadIssuesFiltered(nil): %v", err)
			}
			if len(issues) != 3 {
				t.Errorf("nil filter should return all: want 3, got %d", len(issues))
			}
		})
	}
}

func TestReaderContract_CountIssues(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)
			count, err := r.CountIssues()
			if err != nil {
				t.Fatalf("CountIssues: %v", err)
			}
			if count != 3 {
				t.Errorf("want 3, got %d", count)
			}
		})
	}
}

func TestReaderContract_GetIssueByID(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)

			tests := []struct {
				id      string
				wantErr bool
				title   string
			}{
				{"CTR-1", false, "First issue"},
				{"CTR-2", false, "Second issue"},
				{"CTR-3", false, "Third issue"},
				{"NOPE-999", true, ""},
			}

			for _, tt := range tests {
				t.Run(tt.id, func(t *testing.T) {
					iss, err := r.GetIssueByID(tt.id)
					if tt.wantErr {
						if err == nil {
							t.Errorf("GetIssueByID(%q) = nil error; want error", tt.id)
						}
						return
					}
					if err != nil {
						t.Fatalf("GetIssueByID(%q): %v", tt.id, err)
					}
					if iss.Title != tt.title {
						t.Errorf("title = %q; want %q", iss.Title, tt.title)
					}
				})
			}
		})
	}
}

func TestReaderContract_GetLastModified(t *testing.T) {
	for _, f := range readerFactories() {
		t.Run(f.name, func(t *testing.T) {
			r := f.setup(t)
			mod, err := r.GetLastModified()
			if err != nil {
				t.Fatalf("GetLastModified: %v", err)
			}
			if mod.IsZero() {
				t.Error("GetLastModified returned zero time")
			}
		})
	}
}

func TestNewReader_UnknownType(t *testing.T) {
	_, err := NewReader(DataSource{Type: "bogus"})
	if err == nil {
		t.Error("NewReader with unknown type should fail")
	}
}

func TestSQLiteReader_MinimalValidatedSchemaLoads(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL
		);
		INSERT INTO issues (id, title, status) VALUES
		('MIN-1', 'Minimal one', 'open'),
		('MIN-2', 'Minimal two', 'closed');
	`)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	source := DataSource{Type: SourceTypeSQLite, Path: dbPath}
	if err := ValidateSource(&source); err != nil {
		t.Fatalf("minimal schema should validate: %v", err)
	}

	r, err := NewReader(source)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	issues, err := r.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].IssueType != model.TypeTask || issues[1].IssueType != model.TypeTask {
		t.Fatalf("minimal schema should default issue type to task: %#v", issues)
	}

	count, err := r.CountIssues()
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}

	modified, err := r.GetLastModified()
	if err != nil {
		t.Fatalf("GetLastModified without updated_at should not fail: %v", err)
	}
	if !modified.IsZero() {
		t.Fatalf("expected zero last-modified for schema without updated_at, got %v", modified)
	}
}

func TestSQLiteReaderExcludesStatusTombstonesWithoutMarkerColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "status-tombstone.db")
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			updated_at TEXT
		);
		INSERT INTO issues (id, title, status, updated_at) VALUES
		('LIVE-1', 'Live issue', ' OPEN ', '2026-01-01T00:00:00Z'),
		('CLOSED-1', 'Closed issue', ' CLOSED ', '2025-01-01T00:00:00Z'),
		('DELETED-1', 'Deleted issue', ' TOMBSTONE ', '2030-01-01T00:00:00Z');
	`)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	source := DataSource{Type: SourceTypeSQLite, Path: dbPath}
	if err := ValidateSource(&source); err != nil {
		t.Fatalf("ValidateSource: %v", err)
	}
	if source.IssueCount != 2 {
		t.Fatalf("validated IssueCount=%d, want two non-tombstone issues", source.IssueCount)
	}

	reader, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer reader.Close()

	issues, err := reader.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 2 || issues[0].ID != "LIVE-1" || issues[1].ID != "CLOSED-1" {
		t.Fatalf("status tombstone leaked from LoadIssues: %#v", issues)
	}
	if issues[0].Status != model.StatusOpen || issues[1].Status != model.StatusClosed {
		t.Fatalf("SQLite statuses were not normalized: %#v", issues)
	}
	count, err := reader.CountIssues()
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountIssues=%d, want two non-tombstone issues", count)
	}
	if deleted, err := reader.GetIssueByID("DELETED-1"); err == nil || deleted != nil {
		t.Fatalf("GetIssueByID returned status tombstone: issue=%#v err=%v", deleted, err)
	}
	modified, err := reader.GetLastModified()
	if err != nil {
		t.Fatalf("GetLastModified: %v", err)
	}
	wantModified := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !modified.Equal(wantModified) {
		t.Fatalf("GetLastModified=%v, want live issue time %v", modified, wantModified)
	}
}

func TestSQLiteReader_MissingReadOnlyDatabaseFailsAtOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err == nil {
		_ = r.Close()
		t.Fatal("expected missing read-only database to fail during NewSQLiteReader")
	}
}

func TestSQLiteReader_EscapesURIControlCharsInPath(t *testing.T) {
	for _, name := range []string{"odd?name.db", "odd#name.db"} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), name)
			createContractTestSQLiteDB(t, dbPath)

			r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
			if err != nil {
				t.Fatalf("NewSQLiteReader: %v", err)
			}
			defer r.Close()

			issue, err := r.GetIssueByID("CTR-1")
			if err != nil {
				t.Fatalf("GetIssueByID: %v", err)
			}
			if issue.ID != "CTR-1" {
				t.Fatalf("opened wrong SQLite database: got issue %q", issue.ID)
			}
		})
	}
}

func TestSQLiteReader_FallbackSchemaLoadsGraphMetadata(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "export.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			issue_type TEXT NOT NULL,
			assignee TEXT,
			labels TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			defer_until TEXT,
			closed_at TEXT
		);
		CREATE TABLE dependencies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'blocks'
		);
		CREATE TABLE comments (
			id TEXT PRIMARY KEY,
			issue_id TEXT NOT NULL,
			author TEXT,
			text TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		INSERT INTO issues (id, title, description, status, priority, issue_type, assignee, labels, created_at, updated_at, defer_until)
		VALUES ('EXP-1', 'Export issue', 'from export schema', 'open', 1, 'task', 'cc11', '["graph","sqlite"]', ?, ?, '2031-03-04T05:06:07Z');
		INSERT INTO issues (id, title, description, status, priority, issue_type, assignee, labels, created_at, updated_at)
		VALUES ('ROOT-1', 'Root issue', '', 'open', 2, 'task', '', '[]', ?, ?);
		INSERT INTO dependencies (issue_id, depends_on_id, type)
		VALUES ('EXP-1', 'ROOT-1', 'blocks');
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES ('comment-1', 'EXP-1', 'agent', 'keeps metadata', ?);
	`, now, now, now, now, now)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	issue, err := r.GetIssueByID("EXP-1")
	if err != nil {
		t.Fatalf("GetIssueByID: %v", err)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "graph" || issue.Labels[1] != "sqlite" {
		t.Fatalf("expected labels from fallback schema, got %#v", issue.Labels)
	}
	if issue.Assignee != "cc11" {
		t.Fatalf("expected assignee from fallback schema, got %q", issue.Assignee)
	}
	wantDefer := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	if issue.DeferUntil == nil || !issue.DeferUntil.Equal(wantDefer) {
		t.Fatalf("expected defer_until %v from fallback schema, got %v", wantDefer, issue.DeferUntil)
	}
	root, err := r.GetIssueByID("ROOT-1")
	if err != nil {
		t.Fatalf("GetIssueByID(ROOT-1): %v", err)
	}
	if root.DeferUntil != nil {
		t.Fatalf("expected NULL defer_until to stay nil, got %v", root.DeferUntil)
	}
	if len(issue.Dependencies) != 1 {
		t.Fatalf("expected one dependency from fallback schema, got %#v", issue.Dependencies)
	}
	if issue.Dependencies[0].IssueID != "EXP-1" ||
		issue.Dependencies[0].DependsOnID != "ROOT-1" ||
		issue.Dependencies[0].Type != model.DepBlocks {
		t.Fatalf("unexpected dependency: %#v", issue.Dependencies[0])
	}
	if len(issue.Comments) != 1 || issue.Comments[0].Text != "keeps metadata" {
		t.Fatalf("expected comments from fallback schema, got %#v", issue.Comments)
	}
}

func TestSQLiteReaderRejectsIssueScanFailureInsteadOfReturningPartialAuthority(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad-issue.db")
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER
		);
		INSERT INTO issues (id, title, status, priority) VALUES
		('GOOD-1', 'Good row', 'open', 1),
		('BAD-1', 'Bad row', 'open', 'not-an-integer');
	`)
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	issues, err := r.LoadIssues()
	if err == nil {
		t.Fatalf("LoadIssues returned partial authority: %#v", issues)
	}
	if issues != nil {
		t.Fatalf("LoadIssues returned %d issues with scan error", len(issues))
	}
	if !strings.Contains(err.Error(), "scanning simple SQLite issue row") {
		t.Fatalf("LoadIssues error lost scan context: %v", err)
	}
}

func TestSQLiteReaderRejectsFullIssueScanFailureInsteadOfDowngrading(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad-full-issue.db")
	createContractTestSQLiteDB(t, dbPath)
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE issues SET priority = 'not-an-integer' WHERE id = 'CTR-2'")
	if closeErr := db.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	issues, err := r.LoadIssues()
	if err == nil {
		t.Fatalf("LoadIssues silently skipped a corrupt full-schema row: %#v", issues)
	}
	if issues != nil {
		t.Fatalf("LoadIssues returned %d issues with full-schema scan error", len(issues))
	}
	if !strings.Contains(err.Error(), "scanning full SQLite issue row") {
		t.Fatalf("LoadIssues error lost full-schema scan context: %v", err)
	}
}

func TestSQLiteReaderRejectsGraphMetadataFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		schema     string
		wantErrSub string
	}{
		{
			name: "dependency scan",
			schema: `
				CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
				INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('ISSUE-1', NULL, 'blocks');
			`,
			wantErrSub: "scanning dependencies for SQLite issue",
		},
		{
			name: "comment query",
			schema: `
				CREATE TABLE comments (issue_id TEXT);
				INSERT INTO comments (issue_id) VALUES ('ISSUE-1');
			`,
			wantErrSub: "querying comments for SQLite issue",
		},
		{
			name: "label scan",
			schema: `
				CREATE TABLE labels (issue_id TEXT, label TEXT);
				INSERT INTO labels (issue_id, label) VALUES ('ISSUE-1', NULL);
			`,
			wantErrSub: "scanning labels for SQLite issue",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "bad-graph.db")
			db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`
				CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL);
				INSERT INTO issues (id, title, status) VALUES ('ISSUE-1', 'Issue', 'open');
			` + tc.schema)
			if closeErr := db.Close(); err == nil && closeErr != nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}

			r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
			if err != nil {
				t.Fatalf("NewSQLiteReader: %v", err)
			}
			defer r.Close()

			issues, err := r.LoadIssues()
			if err == nil {
				t.Fatalf("LoadIssues returned partial authority: %#v", issues)
			}
			if issues != nil {
				t.Fatalf("LoadIssues returned %d issues with graph metadata error", len(issues))
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("LoadIssues error %q missing %q", err, tc.wantErrSub)
			}
		})
	}
}

func TestSQLiteReaderUsesOneSnapshotForIssuesAndDependencies(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snapshot.db")
	writer, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var journalMode string
	if err := writer.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal mode = %q, want WAL", journalMode)
	}
	if _, err := writer.Exec(`
		CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL);
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT);
		INSERT INTO issues (id, title, status) VALUES
		('A', 'First', 'open'),
		('B', 'Second', 'open');
		INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('B', 'OLD', 'blocks');
	`); err != nil {
		t.Fatalf("create snapshot fixture: %v", err)
	}

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	var updateErr error
	updated := false
	issues, err := r.LoadIssuesFiltered(func(issue *model.Issue) bool {
		if issue.ID == "A" && !updated {
			updated = true
			_, updateErr = writer.Exec("UPDATE dependencies SET depends_on_id = 'NEW' WHERE issue_id = 'B'")
		}
		return true
	})
	if err != nil {
		t.Fatalf("LoadIssuesFiltered: %v", err)
	}
	if updateErr != nil {
		t.Fatalf("concurrent dependency update: %v", updateErr)
	}
	if !updated {
		t.Fatal("filter hook did not perform the coordinated write")
	}

	byID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	b := byID["B"]
	if len(b.Dependencies) != 1 || b.Dependencies[0].DependsOnID != "OLD" {
		t.Fatalf("mixed-time issue graph observed: B dependencies = %#v, want snapshot value OLD", b.Dependencies)
	}
	var persisted string
	if err := writer.QueryRow("SELECT depends_on_id FROM dependencies WHERE issue_id = 'B'").Scan(&persisted); err != nil {
		t.Fatalf("read persisted dependency: %v", err)
	}
	if persisted != "NEW" {
		t.Fatalf("coordinated write did not persist: got %q", persisted)
	}
}

// --- Test fixtures ---

// createContractTestSQLiteDB creates a SQLite DB with 3 issues (2 open, 1 closed).
func createContractTestSQLiteDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteFileDSN(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER DEFAULT 3,
			issue_type TEXT DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			due_date DATETIME,
			defer_until DATETIME,
			closed_at DATETIME,
			external_ref TEXT,
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER DEFAULT 0,
			labels TEXT,
			design TEXT,
			acceptance_criteria TEXT,
			notes TEXT,
			source_repo TEXT,
			tombstone INTEGER DEFAULT 0
		);
		CREATE TABLE dependencies (
			issue_id TEXT,
			depends_on_id TEXT,
			dependency_type TEXT
		);
		CREATE TABLE comments (
			id TEXT,
			issue_id TEXT,
			author TEXT,
			text TEXT,
			created_at DATETIME
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	// CTR-1 carries a future defer_until (RFC3339 with a non-UTC offset, as
	// br writes via to_rfc3339()); CTR-2/3 leave it NULL.
	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, issue_type, updated_at, defer_until) VALUES
		('CTR-1', 'First issue',  'open',   'task', ?, '2031-03-04T01:06:07-04:00'),
		('CTR-2', 'Second issue', 'open',   'task', ?, NULL),
		('CTR-3', 'Third issue',  'closed', 'task', ?, NULL)
	`, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

// createContractTestJSONL creates a JSONL file with the same 3 issues.
func createContractTestJSONL(t *testing.T, path string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	content := `{"id":"CTR-1","title":"First issue","status":"open","issue_type":"task","updated_at":"` + now + `"}
{"id":"CTR-2","title":"Second issue","status":"open","issue_type":"task","updated_at":"` + now + `"}
{"id":"CTR-3","title":"Third issue","status":"closed","issue_type":"task","updated_at":"` + now + `"}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
