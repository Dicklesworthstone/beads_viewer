package datasource

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestSQLiteReader_FullSchemaLoadsDeferUntil covers issue #191 on the primary
// (full-column) SQLite load path: defer_until must round-trip as an instant,
// including a non-UTC RFC3339 offset, and NULL must stay nil.
func TestSQLiteReader_FullSchemaLoadsDeferUntil(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "defer-until.db")
	createContractTestSQLiteDB(t, dbPath)

	r, err := NewSQLiteReader(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer r.Close()

	issues, err := r.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	byID := make(map[string]int, len(issues))
	for i := range issues {
		byID[issues[i].ID] = i
	}

	first, ok := byID["CTR-1"]
	if !ok {
		t.Fatalf("CTR-1 missing from %d loaded issues", len(issues))
	}
	// 01:06:07-04:00 is 05:06:07Z — the comparison is instant-based.
	want := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	if issues[first].DeferUntil == nil || !issues[first].DeferUntil.Equal(want) {
		t.Fatalf("CTR-1 defer_until = %v, want %v", issues[first].DeferUntil, want)
	}

	second, ok := byID["CTR-2"]
	if !ok {
		t.Fatalf("CTR-2 missing from %d loaded issues", len(issues))
	}
	if issues[second].DeferUntil != nil {
		t.Fatalf("CTR-2 defer_until = %v, want nil", issues[second].DeferUntil)
	}
}

// TestSQLiteReader_FullSchemaWithoutDeferUntilColumn guards the compatibility
// path: a database predating the defer_until column must still load through
// the full-column query (not silently fall back), with DeferUntil nil.
func TestSQLiteReader_FullSchemaWithoutDeferUntilColumn(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", sqliteFileDSN(dbPath, ""))
	if err != nil {
		t.Fatal(err)
	}
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
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);
		CREATE TABLE comments (id TEXT, issue_id TEXT, author TEXT, text TEXT, created_at DATETIME);
		INSERT INTO issues (id, title, status, issue_type, estimated_minutes, created_at, updated_at)
		VALUES ('OLD-1', 'Legacy issue', 'open', 'task', 30, '2026-01-01T03:04:05Z', '2026-01-02T03:04:05Z');
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

	issue, err := r.GetIssueByID("OLD-1")
	if err != nil {
		t.Fatalf("GetIssueByID: %v", err)
	}
	if issue.DeferUntil != nil {
		t.Fatalf("legacy schema defer_until = %v, want nil", issue.DeferUntil)
	}
	// estimated_minutes is only populated by the full-column query; the simple
	// fallback ignores it. Seeing it proves the full query still succeeds when
	// defer_until is absent.
	if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 30 {
		t.Fatalf("expected full-column load path (estimated_minutes=30), got %v", issue.EstimatedMinutes)
	}
}
