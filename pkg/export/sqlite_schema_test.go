package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

// containsString is a helper for checking error messages (case-insensitive)
func containsString(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func TestCreateSchema(t *testing.T) {
	// Create temp file for test database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA synchronous=FULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=DELETE`); err != nil {
		t.Fatal(err)
	}
	// Observe actual SQLite commits on this database's sole connection.
	var commits atomic.Int64
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		hooks, ok := raw.(interface{ RegisterCommitHook(sqlite.CommitHookFn) })
		if !ok {
			return fmt.Errorf("SQLite connection does not expose commit hooks: %T", raw)
		}
		hooks.RegisterCommitHook(func() int32 {
			commits.Add(1)
			return 0
		})
		return nil
	})
	closeErr := conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	// Create schema
	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}
	if got := commits.Load(); got != 1 {
		t.Errorf("schema creation committed %d times; want one durable transaction", got)
	}
	if err := CreateSchema(db); err != nil {
		t.Fatalf("repeated CreateSchema failed: %v", err)
	}
	var synchronous int
	var journalMode string
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 || journalMode != "delete" {
		t.Fatalf("schema creation changed durability: synchronous=%d journal_mode=%s", synchronous, journalMode)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Verify the complete schema survives closing and reopening the database.
	tables := []string{"issues", "dependencies", "comments", "issue_metrics", "triage_recommendations", "export_meta"}
	for _, table := range tables {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("Table %s not found: %v", table, err)
		}
	}

	// Verify indexes exist (comments indexes added in bv-52)
	indexes := []string{
		"idx_issues_status", "idx_issues_priority", "idx_issues_updated", "idx_issues_type_status",
		"idx_deps_issue", "idx_deps_depends", "idx_deps_type",
		"idx_comments_issue", "idx_comments_created", "idx_metrics_score", "idx_metrics_pagerank",
	}
	for _, idx := range indexes {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name)
		if err != nil {
			t.Errorf("Index %s not found: %v", idx, err)
		}
	}
}

func TestCreateSchemaRollsBackFailedDDL(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "rollback.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// This existing table permits the earlier DDL but rejects a late index
	// because triage_score is absent. Existing schema and user rows must survive.
	if _, err := db.Exec(`CREATE TABLE issue_metrics (issue_id TEXT PRIMARY KEY, sentinel TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO issue_metrics VALUES ('existing', 'preserve me')`); err != nil {
		t.Fatal(err)
	}
	readSchema := func() [][4]string {
		t.Helper()
		rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_schema ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var schema [][4]string
		for rows.Next() {
			var entry [4]string
			if err := rows.Scan(&entry[0], &entry[1], &entry[2], &entry[3]); err != nil {
				t.Fatal(err)
			}
			schema = append(schema, entry)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return schema
	}
	before := readSchema()
	err = CreateSchema(db)
	var sqliteErr *sqlite.Error
	if err == nil || !strings.Contains(err.Error(), "create indexes: create index:") || !errors.As(err, &sqliteErr) {
		t.Fatalf("expected wrapped SQLite index error, got %v", err)
	}
	if after := readSchema(); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed schema creation left partial DDL: before=%v after=%v", before, after)
	}
	var sentinel string
	if err := db.QueryRow(`SELECT sentinel FROM issue_metrics WHERE issue_id='existing'`).Scan(&sentinel); err != nil {
		t.Fatal(err)
	}
	if sentinel != "preserve me" {
		t.Fatalf("existing row changed: %q", sentinel)
	}
	if db.Stats().InUse != 0 {
		t.Fatal("failed schema creation retained its connection")
	}
}

func TestCreateSchemaCommitFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE existing (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO existing VALUES ('preserve me')`); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var value string
	if err := readTx.QueryRow(`SELECT value FROM existing`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	// The real reader's shared lock permits schema writes in a transaction,
	// but prevents their commit until the reader releases that lock.
	err = CreateSchema(db)
	var sqliteErr *sqlite.Error
	if err == nil || !strings.Contains(err.Error(), "commit schema:") || !errors.As(err, &sqliteErr) {
		t.Fatalf("expected wrapped SQLite commit failure, got %v", err)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var added int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name <> 'existing'`).Scan(&added); err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("failed commit left %d schema objects", added)
	}
	if err := db.QueryRow(`SELECT value FROM existing`).Scan(&value); err != nil || value != "preserve me" {
		t.Fatalf("existing data lost: %q, %v", value, err)
	}
	if err := CreateSchema(db); err != nil {
		t.Fatalf("schema retry after releasing reader failed: %v", err)
	}
}

func TestInsertAndQueryIssues(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert test issue
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "test-1", "Test Issue", "Test description", "open", 2, "task", `["test"]`, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("Insert issue failed: %v", err)
	}

	// Insert test metrics
	_, err = db.Exec(`
		INSERT INTO issue_metrics (issue_id, pagerank, betweenness, critical_path_depth, triage_score, blocks_count, blocked_by_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "test-1", 0.15, 0.05, 3, 0.75, 2, 1)
	if err != nil {
		t.Fatalf("Insert metrics failed: %v", err)
	}

	// Query back
	var id, title, status string
	var priority int
	err = db.QueryRow(`SELECT id, title, status, priority FROM issues WHERE id = ?`, "test-1").Scan(&id, &title, &status, &priority)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if id != "test-1" || title != "Test Issue" || status != "open" || priority != 2 {
		t.Errorf("Unexpected values: id=%s title=%s status=%s priority=%d", id, title, status, priority)
	}

	// Query metrics
	var pagerank, triageScore float64
	err = db.QueryRow(`SELECT pagerank, triage_score FROM issue_metrics WHERE issue_id = ?`, "test-1").Scan(&pagerank, &triageScore)
	if err != nil {
		t.Fatalf("Query metrics failed: %v", err)
	}

	if pagerank != 0.15 || triageScore != 0.75 {
		t.Errorf("Unexpected metrics: pagerank=%f triageScore=%f", pagerank, triageScore)
	}
}

// TestInsertAndQueryComments tests the comments table functionality (bv-52)
func TestInsertAndQueryComments(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert test issue first (foreign key reference)
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "comment-test-1", "Issue with comments", "Test description", "open", 2, "task", `["test"]`, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("Insert issue failed: %v", err)
	}

	// Insert test comments
	_, err = db.Exec(`
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES
			(?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?)
	`,
		1, "comment-test-1", "alice", "First comment from Alice", "2025-01-01T12:00:00Z",
		2, "comment-test-1", "bob", "Reply from Bob", "2025-01-01T13:00:00Z",
	)
	if err != nil {
		t.Fatalf("Insert comments failed: %v", err)
	}

	// Query comments back
	rows, err := db.Query(`SELECT id, issue_id, author, text FROM comments WHERE issue_id = ? ORDER BY created_at`, "comment-test-1")
	if err != nil {
		t.Fatalf("Query comments failed: %v", err)
	}
	defer rows.Close()

	var comments []struct {
		ID      int64
		IssueID string
		Author  string
		Text    string
	}
	for rows.Next() {
		var c struct {
			ID      int64
			IssueID string
			Author  string
			Text    string
		}
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text); err != nil {
			t.Fatalf("Scan comment failed: %v", err)
		}
		comments = append(comments, c)
	}

	if len(comments) != 2 {
		t.Fatalf("Expected 2 comments, got %d", len(comments))
	}
	if comments[0].Author != "alice" || comments[1].Author != "bob" {
		t.Errorf("Unexpected comment authors: %s, %s", comments[0].Author, comments[1].Author)
	}

	// Verify comment count via materialized view
	if err := CreateMaterializedViews(db); err != nil {
		t.Fatalf("CreateMaterializedViews failed: %v", err)
	}

	var commentCount int
	err = db.QueryRow(`SELECT comment_count FROM issue_overview_mv WHERE id = ?`, "comment-test-1").Scan(&commentCount)
	if err != nil {
		t.Fatalf("Query comment_count from MV failed: %v", err)
	}

	if commentCount != 2 {
		t.Errorf("Expected comment_count=2 in MV, got %d", commentCount)
	}
}

func TestCreateFTSIndex(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert test data
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, labels, created_at, updated_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"fts-1", "Authentication Bug", "Login fails on mobile", "open", 1, "bug", `["auth", "mobile"]`, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z",
		"fts-2", "Add Dark Mode", "Implement dark theme for UI", "open", 3, "feature", `["ui", "theme"]`, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("Insert test data failed: %v", err)
	}

	// Create FTS index (modernc.org/sqlite has FTS5 built-in)
	if err := CreateFTSIndex(db); err != nil {
		if containsString(err.Error(), "no such module: fts5") {
			t.Skip("FTS5 unexpectedly not available - skipping")
		}
		t.Fatalf("CreateFTSIndex failed: %v", err)
	}

	// Test FTS search
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM issues_fts WHERE issues_fts MATCH 'authentication'`).Scan(&count)
	if err != nil {
		t.Fatalf("FTS query failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 FTS match, got %d", count)
	}

	// Test prefix search
	err = db.QueryRow(`SELECT COUNT(*) FROM issues_fts WHERE issues_fts MATCH 'dark*'`).Scan(&count)
	if err != nil {
		t.Fatalf("FTS prefix query failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 FTS prefix match, got %d", count)
	}
}

func TestCreateMaterializedViews(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert test data
	_, err = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at)
		VALUES
			(?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"mv-1", "Test MV 1", "Test materialized view 1", "open", 2, "task", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z",
		"mv-2", "Test MV 2", "Test materialized view 2", "open", 2, "task", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z",
		"mv-3", "Test MV 3", "Test materialized view 3", "open", 2, "task", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// mv-1 depends on mv-2 via a blocking relationship. mv-3 depends on mv-2 via legacy empty type.
	_, err = db.Exec(`
		INSERT INTO dependencies (issue_id, depends_on_id, type)
		VALUES
			(?, ?, ?),
			(?, ?, ?)
	`,
		"mv-1", "mv-2", "blocks",
		"mv-3", "mv-2", "",
	)
	if err != nil {
		t.Fatalf("Insert dependencies failed: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO issue_metrics (issue_id, pagerank, triage_score, blocks_count, blocked_by_count)
		VALUES
			(?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?),
			(?, ?, ?, ?, ?)
	`,
		"mv-1", 0.25, 0.80, 0, 1,
		"mv-2", 0.10, 0.50, 2, 0,
		"mv-3", 0.05, 0.20, 0, 1,
	)
	if err != nil {
		t.Fatalf("Insert metrics failed: %v", err)
	}

	// Create materialized views
	if err := CreateMaterializedViews(db); err != nil {
		t.Fatalf("CreateMaterializedViews failed: %v", err)
	}

	// Verify view exists and has data
	var id string
	var pagerank, triageScore float64
	var blocksCount, blockedByCount int
	var blocksIDs, blockedByIDs sql.NullString
	err = db.QueryRow(`
		SELECT id, pagerank, triage_score, blocks_count, blocked_by_count, blocks_ids, blocked_by_ids
		FROM issue_overview_mv
		WHERE id = ?
	`, "mv-1").Scan(&id, &pagerank, &triageScore, &blocksCount, &blockedByCount, &blocksIDs, &blockedByIDs)
	if err != nil {
		t.Fatalf("Query MV failed: %v", err)
	}

	if pagerank != 0.25 || triageScore != 0.80 || blocksCount != 0 || blockedByCount != 1 {
		t.Errorf("Unexpected MV values: pagerank=%f triageScore=%f blocksCount=%d blockedByCount=%d", pagerank, triageScore, blocksCount, blockedByCount)
	}
	if blocksIDs.Valid && blocksIDs.String != "" {
		t.Errorf("Unexpected mv-1 blocks_ids: %q", blocksIDs.String)
	}
	if !blockedByIDs.Valid || blockedByIDs.String != "mv-2" {
		t.Errorf("Unexpected mv-1 blocked_by_ids: valid=%v value=%q", blockedByIDs.Valid, blockedByIDs.String)
	}

	blocksIDs = sql.NullString{}
	blockedByIDs = sql.NullString{}
	err = db.QueryRow(`
		SELECT blocks_ids, blocked_by_ids
		FROM issue_overview_mv
		WHERE id = ?
	`, "mv-2").Scan(&blocksIDs, &blockedByIDs)
	if err != nil {
		t.Fatalf("Query MV for mv-2 failed: %v", err)
	}
	if !blocksIDs.Valid || blocksIDs.String != "mv-1,mv-3" {
		t.Errorf("Unexpected mv-2 blocks_ids: valid=%v value=%q", blocksIDs.Valid, blocksIDs.String)
	}
	if blockedByIDs.Valid && blockedByIDs.String != "" {
		t.Errorf("Unexpected mv-2 blocked_by_ids: %q", blockedByIDs.String)
	}

	blocksIDs = sql.NullString{}
	blockedByIDs = sql.NullString{}
	err = db.QueryRow(`
		SELECT blocks_ids, blocked_by_ids
		FROM issue_overview_mv
		WHERE id = ?
	`, "mv-3").Scan(&blocksIDs, &blockedByIDs)
	if err != nil {
		t.Fatalf("Query MV for mv-3 failed: %v", err)
	}
	if blocksIDs.Valid && blocksIDs.String != "" {
		t.Errorf("Unexpected mv-3 blocks_ids: %q", blocksIDs.String)
	}
	if !blockedByIDs.Valid || blockedByIDs.String != "mv-2" {
		t.Errorf("Unexpected mv-3 blocked_by_ids: valid=%v value=%q", blockedByIDs.Valid, blockedByIDs.String)
	}
}

func TestInsertMetaValue(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert meta value
	if err := InsertMetaValue(db, "version", "1.0.0"); err != nil {
		t.Fatalf("InsertMetaValue failed: %v", err)
	}

	// Query it back
	var value string
	err = db.QueryRow(`SELECT value FROM export_meta WHERE key = ?`, "version").Scan(&value)
	if err != nil {
		t.Fatalf("Query meta failed: %v", err)
	}

	if value != "1.0.0" {
		t.Errorf("Expected version 1.0.0, got %s", value)
	}

	// Test update (INSERT OR REPLACE)
	if err := InsertMetaValue(db, "version", "1.0.1"); err != nil {
		t.Fatalf("InsertMetaValue update failed: %v", err)
	}

	err = db.QueryRow(`SELECT value FROM export_meta WHERE key = ?`, "version").Scan(&value)
	if err != nil {
		t.Fatalf("Query meta after update failed: %v", err)
	}

	if value != "1.0.1" {
		t.Errorf("Expected version 1.0.1, got %s", value)
	}
}

func TestDefaultSQLiteExportConfig(t *testing.T) {
	config := DefaultSQLiteExportConfig()

	if config.ChunkThreshold != 5*1024*1024 {
		t.Errorf("Expected ChunkThreshold 5MB, got %d", config.ChunkThreshold)
	}

	if config.ChunkSize != 1*1024*1024 {
		t.Errorf("Expected ChunkSize 1MB, got %d", config.ChunkSize)
	}

	if config.PageSize != 1024 {
		t.Errorf("Expected PageSize 1024, got %d", config.PageSize)
	}

	if !config.IncludeRobotOutputs {
		t.Error("Expected IncludeRobotOutputs true")
	}
}

// TestOptimizeDatabase verifies optimization runs without error
func TestOptimizeDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.sqlite3")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := CreateSchema(db); err != nil {
		t.Fatalf("CreateSchema failed: %v", err)
	}

	// Insert some data
	_, _ = db.Exec(`
		INSERT INTO issues (id, title, description, status, priority, issue_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, "opt-1", "Optimize Test", "Testing optimization", "open", 2, "task", "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z")

	// Create FTS before optimize - skip if not available
	if err := CreateFTSIndex(db); err != nil {
		if containsString(err.Error(), "no such module: fts5") {
			t.Log("FTS5 not available - testing optimize without FTS")
		} else {
			t.Fatalf("CreateFTSIndex failed: %v", err)
		}
	}

	// Optimize should not error
	if err := OptimizeDatabase(db, 1024); err != nil {
		t.Fatalf("OptimizeDatabase failed: %v", err)
	}

	// Verify database is still queryable
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count)
	if err != nil {
		t.Fatalf("Query after optimize failed: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 issue after optimize, got %d", count)
	}

	// Verify file exists and is non-empty
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db file failed: %v", err)
	}

	if info.Size() == 0 {
		t.Error("Database file is empty after optimize")
	}
}
