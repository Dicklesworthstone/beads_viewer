package datasource

import (
	"context"
	"database/sql"
	"net/url"
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

func TestSQLiteLoadReportAccountsForReadLoss(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extra      string
		errors     int
		readErrors int
	}{
		{"minimal_valid_without_optional_tables", "", 0, 0},
		{"malformed_issue_row", `INSERT INTO issues VALUES ('bad', 'Unreadable priority', 'open', 'bad', NULL);`, 1, 0},
		{"invalid_issue_row", `INSERT INTO issues VALUES ('bad', 'Invalid status', 'unsupported', 2, NULL);`, 1, 0},
		{"malformed_dependency_row", `CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT); INSERT INTO dependencies VALUES ('safe', NULL, 'blocks');`, 0, 1},
		{"malformed_dependency_table", `CREATE TABLE dependencies (issue_id TEXT, type TEXT);`, 0, 1},
		{"legacy_dependency_without_type", `CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT); INSERT INTO dependencies VALUES ('safe', 'missing');`, 0, 0},
		{"legacy_empty_dependency_type", `CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT); INSERT INTO dependencies VALUES ('safe', 'missing', '');`, 0, 0},
		{"unsupported_dependency_type", `CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, type TEXT); INSERT INTO dependencies VALUES ('safe', 'missing', 'unsupported');`, 0, 1},
		{"invalid_deferral", `UPDATE issues SET defer_until = 'not-a-time';`, 0, 1},
		{"invalid_creation_time", `ALTER TABLE issues ADD COLUMN created_at TEXT; UPDATE issues SET created_at = 'not-a-time';`, 0, 1},
		{"invalid_update_time", `ALTER TABLE issues ADD COLUMN updated_at TEXT; UPDATE issues SET updated_at = 'not-a-time';`, 0, 1},
		{"reversed_issue_times", `ALTER TABLE issues ADD COLUMN created_at TEXT; ALTER TABLE issues ADD COLUMN updated_at TEXT; INSERT INTO issues VALUES ('bad', 'Reversed times', 'open', 2, NULL, '2026-01-02T00:00:00Z', '2026-01-01T00:00:00Z');`, 1, 0},
		{"duplicate_issue_id", `INSERT INTO issues VALUES ('safe', 'Duplicate', 'open', 2, NULL);`, 1, 0},
		{"malformed_label_row", `CREATE TABLE labels (issue_id TEXT, label TEXT); INSERT INTO labels VALUES ('safe', NULL);`, 0, 1},
		{"malformed_label_json", `ALTER TABLE issues ADD COLUMN labels TEXT; UPDATE issues SET labels = '[invalid';`, 0, 1},
		{"malformed_comments_table", `CREATE TABLE comments (issue_id TEXT, body TEXT);`, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beads.db")
			db, err := sql.Open("sqlite", sqliteFileDSN(path, ""))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE issues (id TEXT, title TEXT, status TEXT, priority INTEGER, defer_until TEXT);
INSERT INTO issues VALUES ('safe', 'Readable issue', 'open', 2, NULL);` + tc.extra); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadFromSource(DataSource{Type: SourceTypeSQLite, Path: path})
			if err != nil {
				t.Fatalf("partial source must remain inspectable: %v", err)
			}
			if len(loaded.Issues) != 1 || loaded.Issues[0].ID != "safe" {
				t.Fatalf("readable unique issue lost: %+v", loaded.Issues)
			}
			report := loaded.Report
			if report.Valid != 1 || report.Errors != tc.errors || report.ReadErrors != tc.readErrors {
				t.Fatalf("read-loss accounting = %+v; want valid=1 errors=%d read_errors=%d", report, tc.errors, tc.readErrors)
			}
			if report.WarningCount != tc.errors+tc.readErrors || len(report.Warnings) != report.WarningCount {
				t.Fatalf("warning totals do not reconcile: %+v", report)
			}
			if strings.HasPrefix(tc.name, "legacy_") {
				deps := loaded.Issues[0].Dependencies
				if len(deps) != 1 || deps[0].DependsOnID != "missing" || !deps[0].Type.IsBlocking() {
					t.Fatalf("legacy blocking edge was lost: %+v", deps)
				}
				if model.NewReadinessIndex(loaded.Issues).Ready("safe", time.Now()) {
					t.Fatal("missing legacy blocker must withhold readiness")
				}
			}
		})
	}
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

func TestReaderContract_TombstonesRetainDependencyAuthority(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite-full", "sqlite-minimal"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			source := DataSource{Type: SourceTypeJSONLLocal, Path: filepath.Join(dir, "issues.jsonl")}
			if backend == "jsonl" {
				contents := `{"id":"safe","title":"Safe","status":"open","issue_type":"task","dependencies":[{"depends_on_id":"gone","type":"blocks"}]}
{"id":"unknown","title":"Unknown","status":"open","issue_type":"task","dependencies":[{"depends_on_id":"absent","type":"blocks"}]}
{"id":"gone","title":"Deleted","status":"tombstone","issue_type":"task"}
`
				if err := os.WriteFile(source.Path, []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				source = DataSource{Type: SourceTypeSQLite, Path: filepath.Join(dir, "beads.db")}
				if backend == "sqlite-full" {
					createContractTestSQLiteDB(t, source.Path)
				}
				db, err := sql.Open("sqlite", sqliteFileDSN(source.Path, ""))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				if backend == "sqlite-minimal" {
					_, err = db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, tombstone INTEGER);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);`)
					if err != nil {
						t.Fatal(err)
					}
				}
				_, err = db.Exec(`INSERT INTO issues (id, title, status, tombstone) VALUES
('safe', 'Safe', 'open', 0), ('unknown', 'Unknown', 'open', 0), ('gone', 'Deleted', 'closed', 1);
INSERT INTO dependencies VALUES ('safe', 'gone', 'blocks'), ('unknown', 'absent', 'blocks');`)
				if err != nil {
					t.Fatal(err)
				}
			}
			reader, err := NewReader(source)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			visible, err := reader.LoadIssues()
			if err != nil {
				t.Fatal(err)
			}
			for _, issue := range visible {
				if issue.ID == "gone" {
					t.Fatal("tombstone leaked into display rows")
				}
			}
			report := reader.LoadReport()
			if len(report.TombstoneIDs) != 1 || report.TombstoneIDs[0] != "gone" || report.Path != source.Path {
				t.Fatalf("missing tombstone authority: %+v", report)
			}
			authority := append([]model.Issue(nil), visible...)
			for _, id := range report.TombstoneIDs {
				authority = append(authority, model.Issue{ID: id, Status: model.StatusTombstone})
			}
			index := model.NewReadinessIndex(authority)
			if !index.Ready("safe", time.Now()) || index.Ready("unknown", time.Now()) {
				t.Fatalf("tombstoned predecessor must be satisfied; missing predecessor must be unknown: safe=%s unknown=%s", index.DependencyState("safe"), index.DependencyState("unknown"))
			}
			report.TombstoneIDs[0] = "absent"
			if reader.LoadReport().TombstoneIDs[0] != "gone" {
				t.Fatal("caller mutated retained authority")
			}
			if backend != "jsonl" {
				full, err := reader.(*SQLiteReader).LoadIssueAuthority()
				if err != nil {
					t.Fatal(err)
				}
				index = model.NewReadinessIndex(full)
				if !index.Ready("safe", time.Now()) || index.Ready("unknown", time.Now()) {
					t.Fatal("SQLite reload authority lost tombstone or fabricated missing record")
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

func TestSQLiteReader_ConfiguresEveryPooledConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pool.db")
	createContractTestSQLiteDB(t, dbPath)

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	r.db.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := r.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire first pooled connection: %v", err)
	}
	defer first.Close()
	second, err := r.db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire second pooled connection: %v", err)
	}
	defer second.Close()

	for i, conn := range []*sql.Conn{first, second} {
		var busyTimeout int
		if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", i+1, err)
		}
		if busyTimeout != 5000 {
			t.Errorf("connection %d busy_timeout = %d, want 5000", i+1, busyTimeout)
		}

		var queryOnly int
		if err := conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
			t.Fatalf("connection %d query_only: %v", i+1, err)
		}
		if queryOnly != 1 {
			t.Errorf("connection %d query_only = %d, want 1", i+1, queryOnly)
		}
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

// bv #198: url.URL{Scheme:"file", Path:p}.String() emits "file://" + p, so a
// Windows drive path or a relative path put its first segment in the URI
// authority slot and SQLite refused it with "invalid uri authority".
func TestSQLiteURIPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	cases := []struct {
		name    string
		path    string
		windows bool
		want    string
	}{
		{
			name:    "windows drive path",
			path:    `E:\Shared\Workspaces\personal\wp-block-x\.beads\beads.db`,
			windows: true,
			want:    "/E:/Shared/Workspaces/personal/wp-block-x/.beads/beads.db",
		},
		{
			name:    "windows drive path with forward slashes",
			path:    "c:/repo/.beads/beads.db",
			windows: true,
			want:    "/c:/repo/.beads/beads.db",
		},
		{
			name:    "windows UNC path",
			path:    `\\server\share\repo\.beads\beads.db`,
			windows: true,
			want:    "//server/share/repo/.beads/beads.db",
		},
		{
			name:    "posix absolute path unchanged",
			path:    "/home/u/repo/.beads/beads.db",
			windows: false,
			want:    "/home/u/repo/.beads/beads.db",
		},
		{
			name:    "posix path with URI control characters unchanged (escaped later by net/url)",
			path:    "/home/u/odd?dir#x/beads.db",
			windows: false,
			want:    "/home/u/odd?dir#x/beads.db",
		},
		{
			name:    "relative path is made absolute",
			path:    filepath.Join(".beads", "beads.db"),
			windows: false,
			want:    filepath.Join(cwd, ".beads", "beads.db"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqliteURIPath(tc.path, tc.windows); got != tc.want {
				t.Fatalf("sqliteURIPath(%q, windows=%v) = %q, want %q", tc.path, tc.windows, got, tc.want)
			}
		})
	}
}

func TestSQLiteFileDSN_WindowsDrivePathHasEmptyAuthority(t *testing.T) {
	u := url.URL{
		Scheme:   "file",
		Path:     sqliteURIPath(`E:\Shared\Workspaces\personal\wp-block-x\.beads\beads.db`, true),
		RawQuery: "mode=ro",
	}
	got := u.String()
	want := "file:///E:/Shared/Workspaces/personal/wp-block-x/.beads/beads.db?mode=ro"
	if got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
	if strings.Contains(got, "%5C") {
		t.Fatalf("DSN still carries escaped backslashes: %q", got)
	}
}

func TestSQLiteFileDSN_RelativePathIsRooted(t *testing.T) {
	got := sqliteFileDSN(filepath.Join(".beads", "beads.db"), "mode=ro")
	if !strings.HasPrefix(got, "file:///") {
		t.Fatalf("relative path DSN must have an empty authority, got %q", got)
	}
}

func TestSQLiteReader_OpensRelativePath(t *testing.T) {
	dir := t.TempDir()
	createContractTestSQLiteDB(t, filepath.Join(dir, "beads.db"))
	t.Chdir(dir)

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: "beads.db"})
	if err != nil {
		t.Fatalf("NewSQLiteReader(relative path): %v", err)
	}
	defer r.Close()

	issue, err := r.GetIssueByID("CTR-1")
	if err != nil {
		t.Fatalf("GetIssueByID: %v", err)
	}
	if issue.ID != "CTR-1" {
		t.Fatalf("opened wrong SQLite database: got issue %q", issue.ID)
	}
}
