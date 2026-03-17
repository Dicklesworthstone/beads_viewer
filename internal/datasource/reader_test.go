package datasource

import (
	"database/sql"
	"os"
	"path/filepath"
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
// When DoltReader is added, append it here and all contract tests run automatically.
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

// --- Test fixtures ---

// createContractTestSQLiteDB creates a SQLite DB with 3 issues (2 open, 1 closed).
func createContractTestSQLiteDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
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
	_, err = db.Exec(`
		INSERT INTO issues (id, title, status, updated_at) VALUES
		('CTR-1', 'First issue',  'open',   ?),
		('CTR-2', 'Second issue', 'open',   ?),
		('CTR-3', 'Third issue',  'closed', ?)
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
