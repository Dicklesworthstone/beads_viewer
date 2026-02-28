package datasource

import (
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
	"database/sql"
)

// TestSQLiteReaderLabelsFromSeparateTable verifies that labels stored in a
// separate `labels` table (br/beads-rs format) are correctly loaded and
// exported — i.e. watch-export does not silently drop labels on re-export.
func TestSQLiteReaderLabelsFromSeparateTable(t *testing.T) {
	// Create a temporary br-format SQLite database (no labels column on issues).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2,
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			due_date DATETIME,
			closed_at DATETIME,
			external_ref TEXT,
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER,
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			source_repo TEXT,
			tombstone INTEGER DEFAULT 0
		);
		CREATE TABLE labels (
			issue_id TEXT NOT NULL,
			label TEXT NOT NULL,
			PRIMARY KEY (issue_id, label)
		);
		CREATE TABLE dependencies (
			issue_id TEXT NOT NULL,
			depends_on_id TEXT NOT NULL,
			dependency_type TEXT NOT NULL DEFAULT 'blocks',
			PRIMARY KEY (issue_id, depends_on_id)
		);
		CREATE TABLE comments (
			id TEXT PRIMARY KEY,
			issue_id TEXT NOT NULL,
			author TEXT,
			text TEXT,
			created_at DATETIME
		);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	_, err = db.Exec(`INSERT INTO issues (id, title) VALUES ('test-1', 'Test Issue')`)
	if err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	_, err = db.Exec(`INSERT INTO labels (issue_id, label) VALUES ('test-1', 'approved'), ('test-1', 'prue')`)
	if err != nil {
		t.Fatalf("insert labels: %v", err)
	}
	db.Close()

	// Now read via SQLiteReader (same path as watch-export uses).
	source := DataSource{
		Type:  SourceTypeSQLite,
		Path:  dbPath,
		Valid: true,
	}
	reader, err := NewSQLiteReader(source)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer reader.Close()

	issues, err := reader.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	issue := issues[0]
	labelSet := make(map[string]bool)
	for _, l := range issue.Labels {
		labelSet[l] = true
	}

	if !labelSet["approved"] {
		t.Errorf("expected label 'approved' in %v", issue.Labels)
	}
	if !labelSet["prue"] {
		t.Errorf("expected label 'prue' in %v", issue.Labels)
	}
}

// TestSQLiteReaderLabelsColumnPreserved verifies that native bv-format
// databases (labels as JSON column on issues) still work correctly.
func TestSQLiteReaderLabelsColumnPreserved(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "beads.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			priority INTEGER NOT NULL DEFAULT 2,
			issue_type TEXT NOT NULL DEFAULT 'task',
			assignee TEXT,
			estimated_minutes INTEGER,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			due_date DATETIME,
			closed_at DATETIME,
			external_ref TEXT,
			compaction_level INTEGER DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit TEXT,
			original_size INTEGER,
			labels TEXT,
			design TEXT NOT NULL DEFAULT '',
			acceptance_criteria TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			source_repo TEXT,
			tombstone INTEGER DEFAULT 0
		);
		CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);
		CREATE TABLE comments (id TEXT PRIMARY KEY, issue_id TEXT, author TEXT, text TEXT, created_at DATETIME);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	_, err = db.Exec(`INSERT INTO issues (id, title, labels) VALUES ('test-2', 'Native Issue', '["approved","prue"]')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	source := DataSource{Type: SourceTypeSQLite, Path: dbPath, Valid: true}
	reader, err := NewSQLiteReader(source)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer reader.Close()

	issues, err := reader.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	labelSet := make(map[string]bool)
	for _, l := range issues[0].Labels {
		labelSet[l] = true
	}
	if !labelSet["approved"] || !labelSet["prue"] {
		t.Errorf("expected approved+prue labels, got %v", issues[0].Labels)
	}
	_ = os.Remove(dbPath)
}
