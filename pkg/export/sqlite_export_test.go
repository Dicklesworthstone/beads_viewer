package export

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"modernc.org/sqlite"
)

// makeTestIssue creates a test issue with given parameters.
func makeTestIssue(id, title string, status model.Status, priority int, issueType model.IssueType) *model.Issue {
	now := time.Now()
	return &model.Issue{
		ID:          id,
		Title:       title,
		Description: "Test description for " + id,
		Status:      status,
		Priority:    priority,
		IssueType:   issueType,
		Labels:      []string{"test"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestNewSQLiteExporter(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("test-1", "Test Issue", model.StatusOpen, 2, model.TypeTask),
	}
	deps := []*model.Dependency{}

	exp := NewSQLiteExporter(issues, deps, nil, nil)

	if exp == nil {
		t.Fatal("NewSQLiteExporter returned nil")
	}

	if len(exp.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(exp.Issues))
	}

	if exp.Config.ChunkThreshold != 5*1024*1024 {
		t.Errorf("Expected default chunk threshold 5MB, got %d", exp.Config.ChunkThreshold)
	}
}

func TestSetGitHash(t *testing.T) {
	exp := NewSQLiteExporter(nil, nil, nil, nil)
	exp.SetGitHash("abc123")

	if exp.gitHash != "abc123" {
		t.Errorf("Expected git hash abc123, got %s", exp.gitHash)
	}
}

func TestSQLiteExportFullSourceReadiness(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	dep := func(id string, kind model.DependencyType) *model.Dependency {
		return &model.Dependency{DependsOnID: id, Type: kind}
	}
	cases := []struct {
		issue model.Issue
		state model.DependencyState
		ready bool
	}{
		{model.Issue{ID: "open", Status: model.StatusOpen}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "progress", Status: model.StatusInProgress, Assignee: "agent"}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "deferred", Status: model.StatusOpen, DeferUntil: &later}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "due-now", Status: model.StatusOpen, DeferUntil: &now}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "blocked-status", Status: model.StatusBlocked}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "custom-status", Status: "qa-review"}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "missing", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("absent", model.DepBlocks)}}, model.DependenciesUnknown, false},
		{model.Issue{ID: "missing-parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("absent", model.DepParentChild)}}, model.DependenciesUnknown, false},
		{model.Issue{ID: "filtered-blocker", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-open", model.DepWaitsFor)}}, model.DependenciesUnsatisfied, false},
		{model.Issue{ID: "inherited", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-parent", model.DepParentChild)}}, model.DependenciesUnsatisfied, false},
		{model.Issue{ID: "resolved", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-closed", model.DepBlocks), dep("hidden-deleted", model.DepWaitsFor)}}, model.DependenciesSatisfied, true},
	}
	source := []model.Issue{
		{ID: "hidden-open", Status: model.StatusOpen},
		{ID: "hidden-parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-open", model.DepConditionalBlocks)}},
		{ID: "hidden-closed", Status: model.StatusClosed},
		{ID: "hidden-deleted", Status: model.StatusTombstone},
	}
	var visible []*model.Issue
	for i := range cases {
		cases[i].issue.Title = cases[i].issue.ID
		cases[i].issue.IssueType = model.TypeTask
		source = append(source, cases[i].issue)
		visible = append(visible, &cases[i].issue)
	}
	exporter := NewSQLiteExporter(visible, nil, nil, nil)
	exporter.Config.Readiness = model.NewReadinessIndex(source)
	exporter.Config.ReadinessAt = now
	output := t.TempDir()
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(output, "beads.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range cases {
		var state string
		var ready bool
		if err := db.QueryRow(`SELECT dependency_state, is_actionable FROM issue_overview_mv WHERE id = ?`, tc.issue.ID).Scan(&state, &ready); err != nil {
			t.Fatal(err)
		}
		if state != string(tc.state) || ready != tc.ready {
			t.Errorf("%s: state=%s ready=%v, want %s/%v", tc.issue.ID, state, ready, tc.state, tc.ready)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE id LIKE 'hidden-%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("context rows leaked into display: count=%d err=%v", count, err)
	}
	var clock string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key='readiness_at'`).Scan(&clock); err != nil || clock != now.Format(time.RFC3339Nano) {
		t.Fatalf("readiness clock=%q err=%v", clock, err)
	}
}

func TestSQLiteExportStandaloneReadinessRefresh(t *testing.T) {
	prerequisite := makeTestIssue("root", "Root", model.StatusOpen, 1, model.TypeTask)
	child := makeTestIssue("child", "Child", model.StatusOpen, 1, model.TypeTask)
	child.Dependencies = []*model.Dependency{{IssueID: "child", DependsOnID: "root", Type: model.DepBlocks}}
	// Supply the same edge both ways, as real callers may do, plus a separate
	// missing prerequisite. Neither merging nor a later export may mutate input.
	missing := makeTestIssue("missing-child", "Missing", model.StatusOpen, 1, model.TypeTask)
	exporter := NewSQLiteExporter([]*model.Issue{prerequisite, child, missing}, []*model.Dependency{
		child.Dependencies[0], {IssueID: "missing-child", DependsOnID: "absent", Type: model.DepBlocks},
	}, nil, nil)
	for _, closed := range []bool{false, true} {
		if closed {
			prerequisite.Status = model.StatusClosed
		}
		output := t.TempDir()
		if err := exporter.Export(output); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(output, "beads.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		var ready bool
		if err := db.QueryRow(`SELECT is_actionable FROM issue_overview_mv WHERE id='child'`).Scan(&ready); err != nil || ready != closed {
			t.Errorf("after root closed=%v child ready=%v err=%v", closed, ready, err)
		}
		var state string
		if err := db.QueryRow(`SELECT dependency_state, is_actionable FROM issue_overview_mv WHERE id='missing-child'`).Scan(&state, &ready); err != nil || state != "unknown" || ready {
			t.Errorf("missing prerequisite: state=%q ready=%v err=%v", state, ready, err)
		}
		db.Close()
	}
	if len(child.Dependencies) != 1 || len(missing.Dependencies) != 0 {
		t.Fatal("export mutated caller dependency slices")
	}
}

func TestSQLiteExportMetadataRollsBackFailedInsert(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata-rollback.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := CreateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := InsertMetaValue(db, "sentinel", "preserve me"); err != nil {
		t.Fatal(err)
	}
	// Reject the second new metadata row regardless of Go map iteration
	// order. ABORT rolls back only that statement, so the caller must roll
	// back the earlier successful write as part of its own transaction.
	if _, err := db.Exec(`CREATE TRIGGER reject_second_metadata
		BEFORE INSERT ON export_meta
		WHEN (SELECT COUNT(*) FROM export_meta WHERE key <> 'sentinel') >= 1
		BEGIN
			SELECT RAISE(ABORT, 'reject second metadata row');
		END`); err != nil {
		t.Fatal(err)
	}
	exporter := NewSQLiteExporter(nil, nil, nil, nil)
	exporter.readiness = model.NewReadinessIndex(nil)
	exporter.readinessAt = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	err = exporter.insertMeta(db)
	var sqliteErr *sqlite.Error
	if err == nil || !strings.Contains(err.Error(), "reject second metadata row") || !errors.As(err, &sqliteErr) {
		t.Fatalf("expected SQLite trigger refusal after the first metadata write, got %v", err)
	}
	if db.Stats().InUse != 0 {
		t.Fatal("failed metadata insertion retained its connection")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM export_meta`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed metadata insertion left %d rows; want only the original sentinel", count)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'sentinel'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "preserve me" {
		t.Fatalf("existing metadata changed: %q", value)
	}
}

func TestSQLiteExportFailedRebuildPreservesPublishedDatabase(t *testing.T) {
	output := t.TempDir()
	issue := makeTestIssue("published", "Original", model.StatusOpen, 1, model.TypeTask)
	exporter := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(output, "beads.sqlite3")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A real primary-key violation fails after schema creation and partial
	// inserts. The previously published snapshot must remain byte-for-byte intact.
	broken := NewSQLiteExporter([]*model.Issue{issue, issue}, nil, nil, nil)
	if err := broken.Export(output); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: issues.id") {
		t.Fatalf("expected duplicate issue primary-key failure, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed rebuild changed published database: %v", err)
	}
	issue.Title = "Rebuilt"
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow(`SELECT title FROM issues WHERE id='published'`).Scan(&title); err != nil || title != "Rebuilt" {
		t.Fatalf("successful rebuild not published: title=%q err=%v", title, err)
	}
	remaining, err := filepath.Glob(filepath.Join(output, ".beads-*"))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("temporary databases remain: %v err=%v", remaining, err)
	}
}

func TestSQLiteExportRespectsUmask(t *testing.T) {
	const directoryEnv = "BV_TEST_EXPORT_UMASK_DIR"
	const maskEnv = "BV_TEST_EXPORT_UMASK"
	if directory := os.Getenv(directoryEnv); directory != "" {
		mask := os.Getenv(maskEnv)
		want := os.FileMode(0644)
		if mask == "077" {
			want = 0600
		} else if mask != "022" {
			t.Fatalf("unsupported test umask %q", mask)
		}
		issue := makeTestIssue("private", "Private issue", model.StatusOpen, 1, model.TypeTask)
		exporter := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)
		for range 2 {
			if err := exporter.Export(directory); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(directory, "beads.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("database mode under umask %s = %04o, want %04o", mask, got, want)
			}
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix umask semantics")
	}
	for _, mask := range []string{"077", "022"} {
		t.Run(mask, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0755); err != nil {
				t.Fatal(err)
			}
			// Change the mask only in a subprocess, never in the concurrent test runner.
			command := exec.Command("sh", "-c", `umask "$1"; shift; exec "$@"`, "sh", mask, os.Args[0], "-test.run=^TestSQLiteExportRespectsUmask$") // ubs:ignore — fixed shell program; literal test masks and executable are passed as quoted positional arguments.
			command.Env = append(os.Environ(), directoryEnv+"="+directory, maskEnv+"="+mask)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("umask %s export failed: %v\n%s", mask, err, output)
			}
		})
	}
}

func TestWriteRobotJSONPreservesPayloadTimestamp(t *testing.T) {
	const precise = "2026-09-05T02:00:00.123456789Z"
	const envelopeTime = "2026-09-05T02:00:00Z"
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"precise payload", map[string]any{"generated_at": precise, "issue_count": 2}, precise},
		{"envelope fallback", map[string]any{"issue_count": 2}, envelopeTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := NewSQLiteExporter(nil, nil, nil, nil)
			exporter.Config.RobotEnvelope = map[string]json.RawMessage{
				"generated_at":     json.RawMessage(`"` + envelopeTime + `"`),
				"source_authority": json.RawMessage(`{"state":"partial","claim_safe":false}`),
				"authority_hash":   json.RawMessage(`"source-fingerprint"`),
			}
			path := filepath.Join(t.TempDir(), "meta.json")
			if err := exporter.writeRobotJSON(path, tc.payload); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				GeneratedAt   string `json:"generated_at"`
				IssueCount    int    `json:"issue_count"`
				AuthorityHash string `json:"authority_hash"`
				Authority     struct {
					State     string `json:"state"`
					ClaimSafe bool   `json:"claim_safe"`
				} `json:"source_authority"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.GeneratedAt != tc.want || got.IssueCount != 2 || got.AuthorityHash != "source-fingerprint" || got.Authority.State != "partial" || got.Authority.ClaimSafe {
				t.Fatalf("payload timestamp or shared authority changed: got %s, want timestamp %q, count2 and partial authority", data, tc.want)
			}
		})
	}
}

func TestExport_CreatesDatabase(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("exp-1", "Export Test 1", model.StatusOpen, 1, model.TypeBug),
		makeTestIssue("exp-2", "Export Test 2", model.StatusInProgress, 2, model.TypeFeature),
	}

	deps := []*model.Dependency{
		{IssueID: "exp-2", DependsOnID: "exp-1", Type: model.DepBlocks},
	}

	exp := NewSQLiteExporter(issues, deps, nil, nil)
	exp.SetGitHash("test123")

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify database file exists
	dbPath := filepath.Join(tmpDir, "beads.sqlite3")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}

	// Verify we can query the database
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open exported database: %v", err)
	}
	defer db.Close()

	// Check issues
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count); err != nil {
		t.Fatalf("Query issues count failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 issues, got %d", count)
	}

	// Check dependencies
	if err := db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&count); err != nil {
		t.Fatalf("Query dependencies count failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 dependency, got %d", count)
	}

	// Check metadata
	var version string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatalf("Query version failed: %v", err)
	}
	if version != "1.0.0" {
		t.Errorf("Expected version 1.0.0, got %s", version)
	}

	// Check git hash in metadata
	var gitCommit string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'git_commit'`).Scan(&gitCommit); err != nil {
		t.Fatalf("Query git_commit failed: %v", err)
	}
	if gitCommit != "test123" {
		t.Errorf("Expected git_commit test123, got %s", gitCommit)
	}
}

func TestExport_CreatesDataDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("data-1", "Data Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify data directory exists
	dataDir := filepath.Join(tmpDir, "data")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		t.Error("Data directory was not created")
	}

	// Verify meta.json exists
	metaPath := filepath.Join(dataDir, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Error("meta.json was not created")
	}

	// Read and verify meta.json
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("Failed to read meta.json: %v", err)
	}

	var meta ExportMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Failed to parse meta.json: %v", err)
	}

	if meta.IssueCount != 1 {
		t.Errorf("Expected issue_count 1, got %d", meta.IssueCount)
	}
}

func TestExport_MaterializedView(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("mv-1", "MV Test", model.StatusOpen, 1, model.TypeBug),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Verify materialized view exists and has data
	var title string
	err = db.QueryRow(`SELECT title FROM issue_overview_mv WHERE id = ?`, "mv-1").Scan(&title)
	if err != nil {
		t.Fatalf("Query materialized view failed: %v", err)
	}
	if title != "MV Test" {
		t.Errorf("Expected title 'MV Test', got '%s'", title)
	}
}

// TestExport_RichTextFieldsRoundTrip guards against the #170 regression where
// notes (and the sibling design / acceptance_criteria fields) were silently
// dropped from the static-site export — they were never written to the issues
// table nor surfaced in issue_overview_mv, so the web UI could never display
// them. The web viewer reads a single issue via `SELECT * FROM issue_overview_mv`,
// so we assert these fields round-trip through the materialized view (not just
// the base table).
func TestExport_RichTextFieldsRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	now := time.Now()
	issues := []*model.Issue{
		{
			ID:                 "rich-1",
			Title:              "Issue with rich text",
			Description:        "The description.",
			Design:             "The design notes.",
			AcceptanceCriteria: "The acceptance criteria.",
			Notes:              "The notes that must be visible in the web UI.",
			Status:             model.StatusOpen,
			Priority:           1,
			IssueType:          model.TypeTask,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)
	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// The web UI's getIssue() runs `SELECT * FROM issue_overview_mv`, so the
	// rich-text fields must be present in the materialized view to be displayed.
	var design, acceptance, notes string
	err = db.QueryRow(
		`SELECT design, acceptance_criteria, notes FROM issue_overview_mv WHERE id = ?`,
		"rich-1",
	).Scan(&design, &acceptance, &notes)
	if err != nil {
		t.Fatalf("Query rich-text fields from materialized view failed: %v", err)
	}

	if design != "The design notes." {
		t.Errorf("design not exported: got %q", design)
	}
	if acceptance != "The acceptance criteria." {
		t.Errorf("acceptance_criteria not exported: got %q", acceptance)
	}
	if notes != "The notes that must be visible in the web UI." {
		t.Errorf("notes not exported (regression of #170): got %q", notes)
	}
}

func TestExport_ChunkConfigCreated(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("chunk-1", "Chunk Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify chunk config exists
	configPath := filepath.Join(tmpDir, "beads.sqlite3.config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read chunk config: %v", err)
	}

	var config ChunkConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to parse chunk config: %v", err)
	}

	// Small database should not be chunked
	if config.Chunked {
		t.Error("Small database should not be chunked")
	}

	if config.TotalSize <= 0 {
		t.Error("Total size should be positive")
	}

	// Hash must be present for OPFS cache invalidation (bv-pages-cache-fix)
	// Without this, all deployments use cache key "default" and old data persists
	if config.Hash == "" {
		t.Error("Config hash is required for OPFS cache invalidation but was empty")
	}

	// Hash should be 64 hex chars (SHA-256)
	if len(config.Hash) != 64 {
		t.Errorf("Expected SHA-256 hash (64 chars), got %d chars: %s", len(config.Hash), config.Hash)
	}
}

func TestGetExportedIssues(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("get-1", "Get Test 1", model.StatusOpen, 1, model.TypeBug),
		makeTestIssue("get-2", "Get Test 2", model.StatusInProgress, 2, model.TypeFeature),
	}

	deps := []*model.Dependency{
		{IssueID: "get-2", DependsOnID: "get-1", Type: model.DepBlocks},
	}

	exp := NewSQLiteExporter(issues, deps, nil, nil)
	exported := exp.GetExportedIssues()

	if len(exported) != 2 {
		t.Fatalf("Expected 2 exported issues, got %d", len(exported))
	}

	// Verify blocking relationships:
	// get-2 depends on get-1, so get-1 blocks get-2
	var foundGet1, foundGet2 bool
	for _, e := range exported {
		switch e.ID {
		case "get-1":
			foundGet1 = true
			// get-1 blocks get-2
			if e.BlocksCount != 1 {
				t.Errorf("get-1: expected blocks_count 1, got %d", e.BlocksCount)
			}
			if len(e.BlocksIDs) != 1 || e.BlocksIDs[0] != "get-2" {
				t.Errorf("get-1: expected blocks_ids ['get-2'], got %v", e.BlocksIDs)
			}
			if e.BlockedByCount != 0 {
				t.Errorf("get-1: expected blocked_by_count 0, got %d", e.BlockedByCount)
			}
		case "get-2":
			foundGet2 = true
			// get-2 is blocked by get-1
			if e.BlockedByCount != 1 {
				t.Errorf("get-2: expected blocked_by_count 1, got %d", e.BlockedByCount)
			}
			if len(e.BlockedByIDs) != 1 || e.BlockedByIDs[0] != "get-1" {
				t.Errorf("get-2: expected blocked_by_ids ['get-1'], got %v", e.BlockedByIDs)
			}
			if e.BlocksCount != 0 {
				t.Errorf("get-2: expected blocks_count 0, got %d", e.BlocksCount)
			}
		}
	}

	if !foundGet1 {
		t.Error("Issue get-1 not found in exported issues")
	}
	if !foundGet2 {
		t.Error("Issue get-2 not found in exported issues")
	}
}

func TestExportToJSON(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("json-1", "JSON Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)
	exp.SetGitHash("jsonhash")

	jsonPath := filepath.Join(tmpDir, "export.json")
	if err := exp.ExportToJSON(jsonPath); err != nil {
		t.Fatalf("ExportToJSON failed: %v", err)
	}

	// Read and verify JSON
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("Failed to read export.json: %v", err)
	}

	var output struct {
		Meta   ExportMeta    `json:"meta"`
		Issues []ExportIssue `json:"issues"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("Failed to parse export.json: %v", err)
	}

	if output.Meta.IssueCount != 1 {
		t.Errorf("Expected issue_count 1, got %d", output.Meta.IssueCount)
	}

	if output.Meta.GitCommit != "jsonhash" {
		t.Errorf("Expected git_commit 'jsonhash', got '%s'", output.Meta.GitCommit)
	}

	if len(output.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(output.Issues))
	}
}

func TestExport_WithLabels(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("label-1", "Label Test", model.StatusOpen, 2, model.TypeTask)
	issue.Labels = []string{"bug", "urgent", "backend"}

	exp := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var labels string
	if err := db.QueryRow(`SELECT labels FROM issues WHERE id = ?`, "label-1").Scan(&labels); err != nil {
		t.Fatalf("Query labels failed: %v", err)
	}

	// Labels should be JSON array
	var parsedLabels []string
	if err := json.Unmarshal([]byte(labels), &parsedLabels); err != nil {
		t.Fatalf("Failed to parse labels JSON: %v", err)
	}

	if len(parsedLabels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(parsedLabels))
	}
}

func TestExport_WithClosedAt(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("closed-1", "Closed Test", model.StatusClosed, 2, model.TypeTask)
	closedTime := time.Now().Add(-time.Hour)
	issue.ClosedAt = &closedTime

	exp := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var closedAt sql.NullString
	if err := db.QueryRow(`SELECT closed_at FROM issues WHERE id = ?`, "closed-1").Scan(&closedAt); err != nil {
		t.Fatalf("Query closed_at failed: %v", err)
	}

	if !closedAt.Valid {
		t.Error("closed_at should not be null for closed issue")
	}
}

// TestExport_WithComments tests the comments export functionality (bv-52)
func TestExport_WithComments(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("comments-1", "Issue with comments", model.StatusOpen, 2, model.TypeTask)
	now := time.Now()
	issue.Comments = []*model.Comment{
		{ID: "1", IssueID: "comments-1", Author: "alice", Text: "First comment", CreatedAt: now.Add(-time.Hour)},
		{ID: "2", IssueID: "comments-1", Author: "bob", Text: "Second comment", CreatedAt: now},
	}
	otherIssue := makeTestIssue("comments-2", "Another issue with a reused comment ID", model.StatusOpen, 2, model.TypeTask)
	otherIssue.Comments = []*model.Comment{
		{ID: "1", IssueID: "comments-2", Author: "carol", Text: "Same local ID, different issue", CreatedAt: now},
	}

	exp := NewSQLiteExporter([]*model.Issue{issue, otherIssue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Verify comments were inserted
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments WHERE issue_id = ?`, "comments-1").Scan(&count); err != nil {
		t.Fatalf("Query comments count failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 comments, got %d", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&count); err != nil {
		t.Fatalf("Query total comments count failed: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 total comments with reused per-issue IDs, got %d", count)
	}

	// Verify comment content (id is now composite: issue_id:comment_id)
	var author, text string
	if err := db.QueryRow(`SELECT author, text FROM comments WHERE id = ?`, "comments-1:1").Scan(&author, &text); err != nil {
		t.Fatalf("Query comment 1 failed: %v", err)
	}
	if author != "alice" || text != "First comment" {
		t.Errorf("Comment 1: expected author='alice', text='First comment', got author='%s', text='%s'", author, text)
	}
	if err := db.QueryRow(`SELECT author, text FROM comments WHERE id = ?`, "comments-2:1").Scan(&author, &text); err != nil {
		t.Fatalf("Query reused comment ID failed: %v", err)
	}
	if author != "carol" || text != "Same local ID, different issue" {
		t.Errorf("Reused comment ID: got author=%q, text=%q", author, text)
	}

	// Verify comment_count in materialized view
	var commentCount int
	if err := db.QueryRow(`SELECT comment_count FROM issue_overview_mv WHERE id = ?`, "comments-1").Scan(&commentCount); err != nil {
		t.Fatalf("Query comment_count from MV failed: %v", err)
	}
	if commentCount != 2 {
		t.Errorf("Expected comment_count=2 in MV, got %d", commentCount)
	}
}

func TestExport_EmptyData(t *testing.T) {
	tmpDir := t.TempDir()

	exp := NewSQLiteExporter([]*model.Issue{}, []*model.Dependency{}, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export with empty data failed: %v", err)
	}

	// Should still create a valid database
	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count); err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
}

func TestExport_DisableRobotOutputs(t *testing.T) {
	tmpDir := t.TempDir()

	exp := NewSQLiteExporter([]*model.Issue{
		makeTestIssue("robot-1", "Robot Test", model.StatusOpen, 2, model.TypeTask),
	}, nil, nil, nil)

	exp.Config.IncludeRobotOutputs = false

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// meta.json should not exist when robot outputs are disabled
	metaPath := filepath.Join(tmpDir, "data", "meta.json")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("meta.json should not exist when robot outputs are disabled")
	}
}

func TestStringSliceContains(t *testing.T) {
	tests := []struct {
		slice    []string
		val      string
		expected bool
	}{
		{[]string{"a", "b", "c"}, "b", true},
		{[]string{"a", "b", "c"}, "B", true}, // case-insensitive
		{[]string{"a", "b", "c"}, "d", false},
		{[]string{}, "a", false},
		{nil, "a", false},
	}

	for _, tc := range tests {
		result := stringSliceContains(tc.slice, tc.val)
		if result != tc.expected {
			t.Errorf("stringSliceContains(%v, %s) = %v, expected %v", tc.slice, tc.val, result, tc.expected)
		}
	}
}
