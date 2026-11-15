package datasource

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestValidationCacheDoesNotStoreVerdictForChangedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"VALID-1","title":"valid","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := DataSource{Type: SourceTypeJSONLLocal, Path: path}
	opts := DefaultValidationOptions()
	opts.Verbose = true
	var rewriteErr error
	opts.Logger = func(string) {
		if rewriteErr == nil {
			rewriteErr = os.WriteFile(path, []byte("invalid JSONL now\n"), 0o644)
		}
	}
	if err := ValidateSourceWithOptions(&source, opts); err != nil {
		t.Fatalf("validation before rewrite: %v", err)
	}
	if rewriteErr != nil {
		t.Fatal(rewriteErr)
	}
	changed := DataSource{Type: SourceTypeJSONLLocal, Path: path}
	if hit, err := lookupValidationCache(&changed, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("cached verdict for changed input: hit=%t err=%v", hit, err)
	}
}

// TestDiscoverSources_OnlySQLite tests discovery with only a SQLite source
func TestDiscoverSources_OnlySQLite(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create SQLite database
	dbPath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, dbPath)

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("DiscoverSources failed: %v", err)
	}

	if len(sources) == 0 {
		t.Fatal("Expected at least one source")
	}

	found := false
	for _, s := range sources {
		if s.Type == SourceTypeSQLite {
			found = true
			if s.Path != dbPath {
				t.Errorf("Expected path %s, got %s", dbPath, s.Path)
			}
			if s.Priority != PrioritySQLite {
				t.Errorf("Expected priority %d, got %d", PrioritySQLite, s.Priority)
			}
		}
	}
	if !found {
		t.Error("SQLite source not found")
	}
}

// TestDiscoverSources_OnlyJSONL tests discovery with only a JSONL source
func TestDiscoverSources_OnlyJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create JSONL file
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"TEST-1","title":"Test","status":"open"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("DiscoverSources failed: %v", err)
	}

	if len(sources) == 0 {
		t.Fatal("Expected at least one source")
	}

	found := false
	for _, s := range sources {
		if s.Type == SourceTypeJSONLLocal {
			found = true
			if s.Path != jsonlPath {
				t.Errorf("Expected path %s, got %s", jsonlPath, s.Path)
			}
		}
	}
	if !found {
		t.Error("JSONL source not found")
	}
}

// TestDiscoverSources_Multiple tests discovery with multiple sources
func TestDiscoverSources_Multiple(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create SQLite database
	dbPath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, dbPath)

	// Create JSONL file
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"TEST-1","title":"Test","status":"open"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("DiscoverSources failed: %v", err)
	}

	if len(sources) < 2 {
		t.Fatalf("Expected at least 2 sources, got %d", len(sources))
	}

	foundSQLite := false
	foundJSONL := false
	for _, s := range sources {
		if s.Type == SourceTypeSQLite {
			foundSQLite = true
		}
		if s.Type == SourceTypeJSONLLocal {
			foundJSONL = true
		}
	}

	if !foundSQLite {
		t.Error("SQLite source not found")
	}
	if !foundJSONL {
		t.Error("JSONL source not found")
	}
}

// TestDiscoverSources_Empty tests discovery with no sources
func TestDiscoverSources_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("DiscoverSources failed: %v", err)
	}

	if len(sources) != 0 {
		t.Errorf("Expected 0 sources, got %d", len(sources))
	}
}

func TestLoadIssues_ExplicitDirectoryIgnoresCallerWorktree(t *testing.T) {
	for _, envName := range []string{loader.BeadsDBEnvVar, loader.BeadsDirEnvVar} {
		t.Run(envName, func(t *testing.T) {
			t.Setenv(loader.BeadsDBEnvVar, "")
			t.Setenv(loader.BeadsDirEnvVar, "")

			repoDir := t.TempDir()
			git := exec.Command("git", "init", "-b", "main")
			git.Dir = repoDir
			if output, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, output)
			}
			foreignDir := filepath.Join(repoDir, ".git", "beads-worktrees", "foreign")
			if err := os.MkdirAll(foreignDir, 0o755); err != nil {
				t.Fatalf("mkdir foreign worktree export: %v", err)
			}
			foreignPath := filepath.Join(foreignDir, "issues.jsonl")
			if err := os.WriteFile(foreignPath, []byte(`{"id":"FOREIGN-1","title":"Wrong repository","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
				t.Fatalf("write foreign worktree export: %v", err)
			}

			selectedDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(selectedDir, 0o755); err != nil {
				t.Fatalf("mkdir selected tracker: %v", err)
			}
			selectedPath := filepath.Join(selectedDir, "issues.jsonl")
			if err := os.WriteFile(selectedPath, []byte(`{"id":"SELECTED-1","title":"Selected repository","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
				t.Fatalf("write selected tracker export: %v", err)
			}
			older := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
			newer := older.Add(time.Hour)
			if err := os.Chtimes(selectedPath, older, older); err != nil {
				t.Fatalf("age selected tracker export: %v", err)
			}
			if err := os.Chtimes(foreignPath, newer, newer); err != nil {
				t.Fatalf("freshen foreign worktree export: %v", err)
			}

			t.Setenv(envName, selectedDir)
			loaded, err := LoadIssues(repoDir)
			if err != nil {
				t.Fatalf("LoadIssues with explicit directory: %v", err)
			}
			if len(loaded.Issues) != 1 || loaded.Issues[0].ID != "SELECTED-1" {
				t.Fatalf("explicit directory loaded issues = %#v, want only SELECTED-1", loaded.Issues)
			}
			if loaded.Source.Path != selectedPath {
				t.Fatalf("selected source = %s, want %s", loaded.Source.Path, selectedPath)
			}

			fromDir, err := LoadIssuesFromDir(selectedDir)
			if err != nil {
				t.Fatalf("LoadIssuesFromDir: %v", err)
			}
			if len(fromDir.Issues) != 1 || fromDir.Issues[0].ID != "SELECTED-1" {
				t.Fatalf("explicit LoadIssuesFromDir issues = %#v, want only SELECTED-1", fromDir.Issues)
			}

			t.Setenv(envName, "")
			inferred, err := LoadIssues(repoDir)
			if err != nil {
				t.Fatalf("LoadIssues with inferred tracker: %v", err)
			}
			if len(inferred.Issues) != 1 || inferred.Issues[0].ID != "FOREIGN-1" {
				t.Fatalf("inferred tracker loaded issues = %#v, want worktree source", inferred.Issues)
			}
		})
	}
}

func TestDiscoverSources_RespectsBeadsDBSpecificFile(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "selected.jsonl")
	content := `{"id":"JSONL-1","title":"Selected JSONL","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	createTestSQLiteDB(t, filepath.Join(beadsDir, "beads.db"))
	t.Setenv(loader.BeadsDBEnvVar, jsonlPath)

	sources, err := DiscoverSources(DiscoveryOptions{ValidateAfterDiscovery: true})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected exactly the explicit source, got %#v", sources)
	}
	if sources[0].Path != jsonlPath || sources[0].Type != SourceTypeJSONLLocal {
		t.Fatalf("expected explicit JSONL source, got %#v", sources[0])
	}
}

func TestResolveBeadsDBPath_MissingSQLiteFileUsesParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, ".beads", "selected.sqlite3")

	got := resolveBeadsDBPath(dbPath)
	if got != filepath.Dir(dbPath) {
		t.Fatalf("missing sqlite file should resolve to parent dir: got %s, want %s", got, filepath.Dir(dbPath))
	}
}

func TestLoadIssues_RespectsBeadsDBSpecificJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "selected.jsonl")
	content := `{"id":"JSONL-1","title":"Selected JSONL","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	createTestSQLiteDB(t, filepath.Join(beadsDir, "beads.db"))
	t.Setenv(loader.BeadsDBEnvVar, jsonlPath)

	loaded, err := LoadIssues(tmpDir)
	issues := loaded.Issues
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "JSONL-1" {
		t.Fatalf("expected explicit JSONL source, got %#v", issues)
	}
}

func TestLoadIssues_RespectsBeadsDBSpecificSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"JSONL-1","title":"Default JSONL","status":"open","issue_type":"task"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(beadsDir, "selected.db")
	createSingleIssueSQLiteDB(t, dbPath, "SQLITE-1")
	t.Setenv(loader.BeadsDBEnvVar, dbPath)

	loaded, err := LoadIssues(tmpDir)
	issues := loaded.Issues
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "SQLITE-1" {
		t.Fatalf("expected explicit SQLite source, got %#v", issues)
	}
}

// TestValidateSQLite_Valid tests validation of a valid SQLite database
func TestValidateSQLite_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	createTestSQLiteDB(t, dbPath)

	source := DataSource{
		Type: SourceTypeSQLite,
		Path: dbPath,
	}

	err := ValidateSource(&source)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	if !source.Valid {
		t.Error("Expected source to be valid")
	}
	if source.IssueCount != 2 {
		t.Errorf("Expected 2 issues, got %d", source.IssueCount)
	}
}

// TestValidateSQLite_Empty tests validation of an empty but valid SQLite database
func TestValidateSQLite_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")
	createEmptySQLiteDB(t, dbPath)

	source := DataSource{
		Type: SourceTypeSQLite,
		Path: dbPath,
	}

	err := ValidateSource(&source)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	if !source.Valid {
		t.Error("Expected source to be valid")
	}
	if source.IssueCount != 0 {
		t.Errorf("Expected 0 issues, got %d", source.IssueCount)
	}
}

// TestValidateSQLite_Corrupted tests validation of a corrupted SQLite database
func TestValidateSQLite_Corrupted(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	// Write garbage data
	if err := os.WriteFile(dbPath, []byte("THIS IS NOT A VALID SQLITE DATABASE"), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeSQLite,
		Path: dbPath,
	}

	err := ValidateSource(&source)
	if err == nil {
		t.Fatal("Expected validation to fail for corrupted database")
	}

	if source.Valid {
		t.Error("Expected source to be invalid")
	}
}

// TestValidateSQLite_WrongSchema tests validation of SQLite with missing columns
func TestValidateSQLite_WrongSchema(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "beads.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Create table with wrong schema (missing required columns)
	_, err = db.Exec("CREATE TABLE issues (foo TEXT)")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	source := DataSource{
		Type: SourceTypeSQLite,
		Path: dbPath,
	}

	err = ValidateSource(&source)
	if err == nil {
		t.Fatal("Expected validation to fail for wrong schema")
	}

	if source.Valid {
		t.Error("Expected source to be invalid")
	}
}

// TestValidateJSONL_Valid tests validation of a valid JSONL file
func TestValidateJSONL_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	content := `{"id":"TEST-1","title":"Test Issue 1","status":"open"}
{"id":"TEST-2","title":"Test Issue 2","status":"closed"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	}

	err := ValidateSource(&source)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	if !source.Valid {
		t.Error("Expected source to be valid")
	}
	if source.IssueCount != 2 {
		t.Errorf("Expected 2 issues, got %d", source.IssueCount)
	}
}

// TestValidateJSONL_Empty tests validation of an empty JSONL file
func TestValidateJSONL_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	if err := os.WriteFile(jsonlPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	}

	err := ValidateSource(&source)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	if !source.Valid {
		t.Error("Expected empty file to be valid")
	}
	if source.IssueCount != 0 {
		t.Errorf("Expected 0 issues, got %d", source.IssueCount)
	}
}

// TestValidateJSONL_PartialCorrupt tests validation with <10% bad lines
func TestValidateJSONL_PartialCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// 9 valid, 1 invalid = 10% error rate (at threshold)
	content := `{"id":"TEST-1","title":"Test 1","status":"open"}
{"id":"TEST-2","title":"Test 2","status":"open"}
{"id":"TEST-3","title":"Test 3","status":"open"}
{"id":"TEST-4","title":"Test 4","status":"open"}
{"id":"TEST-5","title":"Test 5","status":"open"}
{"id":"TEST-6","title":"Test 6","status":"open"}
{"id":"TEST-7","title":"Test 7","status":"open"}
{"id":"TEST-8","title":"Test 8","status":"open"}
{"id":"TEST-9","title":"Test 9","status":"open"}
not valid json
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	}

	err := ValidateSource(&source)
	if err != nil {
		t.Fatalf("Validation failed: %v", err)
	}

	if !source.Valid {
		t.Error("Expected source with 10% errors to be valid")
	}
}

// TestValidateJSONL_HeavyCorrupt tests validation with >10% bad lines
func TestValidateJSONL_HeavyCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// 8 valid, 3 invalid = ~27% error rate
	content := `{"id":"TEST-1","title":"Test 1","status":"open"}
{"id":"TEST-2","title":"Test 2","status":"open"}
{"id":"TEST-3","title":"Test 3","status":"open"}
{"id":"TEST-4","title":"Test 4","status":"open"}
{"id":"TEST-5","title":"Test 5","status":"open"}
{"id":"TEST-6","title":"Test 6","status":"open"}
{"id":"TEST-7","title":"Test 7","status":"open"}
{"id":"TEST-8","title":"Test 8","status":"open"}
not valid json 1
not valid json 2
not valid json 3
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	}

	err := ValidateSource(&source)
	if err == nil {
		t.Fatal("Expected validation to fail for heavily corrupted file")
	}

	if source.Valid {
		t.Error("Expected source to be invalid")
	}
}

// TestValidateJSONL_MissingFields tests validation with missing required fields
func TestValidateJSONL_MissingFields(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	// Missing "title" field in all entries
	content := `{"id":"TEST-1","status":"open"}
{"id":"TEST-2","status":"open"}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	}

	err := ValidateSource(&source)
	if err == nil {
		t.Fatal("Expected validation to fail for missing required fields")
	}
}

// TestSelectBestSource_SingleValid tests selection with one valid source
func TestSelectBestSource_SingleValid(t *testing.T) {
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  time.Now(),
			Valid:    true,
		},
	}

	selected, err := SelectBestSource(sources)
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}

	if selected.Path != "/test/beads.db" {
		t.Errorf("Expected /test/beads.db, got %s", selected.Path)
	}
}

// TestSelectBestSource_FresherWins tests that newer timestamp wins
func TestSelectBestSource_FresherWins(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/old.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now.Add(-1 * time.Hour),
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/new.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now,
			Valid:    true,
		},
	}

	selected, err := SelectBestSource(sources)
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}

	if selected.Path != "/test/new.jsonl" {
		t.Errorf("Expected newer source, got %s", selected.Path)
	}
}

// TestSelectBestSource_PriorityTiebreaker tests that priority breaks ties
func TestSelectBestSource_PriorityTiebreaker(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/local.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now,
			Valid:    true,
		},
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  now, // Same time
			Valid:    true,
		},
	}

	selected, err := SelectBestSource(sources)
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}

	if selected.Type != SourceTypeSQLite {
		t.Errorf("Expected SQLite (higher priority), got %s", selected.Type)
	}
}

func TestSelectionCanonicalJSONLNameBreaksEqualTies(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{Type: SourceTypeJSONLLocal, Path: "/test/beads.base.jsonl", Priority: PriorityJSONLLocal, ModTime: now, Valid: true},
		{Type: SourceTypeJSONLLocal, Path: "/test/beads.jsonl", Priority: PriorityJSONLLocal, ModTime: now, Valid: true},
		{Type: SourceTypeJSONLLocal, Path: "/test/issues.jsonl", Priority: PriorityJSONLLocal, ModTime: now, Valid: true},
	}
	want := "/test/issues.jsonl"

	for _, preferFreshest := range []bool{true, false} {
		opts := DefaultSelectionOptions()
		opts.PreferFreshest = preferFreshest

		selected, err := SelectBestSourceWithOptions(sources, opts)
		if err != nil {
			t.Fatalf("SelectBestSourceWithOptions(preferFreshest=%t): %v", preferFreshest, err)
		}
		if selected.Path != want {
			t.Errorf("SelectBestSourceWithOptions(preferFreshest=%t) = %q, want %q", preferFreshest, selected.Path, want)
		}

		loaded, err := SelectWithFallback(sources, func(DataSource) error { return nil }, opts)
		if err != nil {
			t.Fatalf("SelectWithFallback(preferFreshest=%t): %v", preferFreshest, err)
		}
		if loaded.Path != want {
			t.Errorf("SelectWithFallback(preferFreshest=%t) = %q, want %q", preferFreshest, loaded.Path, want)
		}
	}

	fused := append([]DataSource(nil), sources...)
	sortByFreshnessThenPriority(fused)
	if fused[0].Path != want {
		t.Errorf("sortByFreshnessThenPriority first = %q, want %q", fused[0].Path, want)
	}
}

func TestSelectBestSource_MaxAgeDeltaUsesNewestWhenPriorityPreferred(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/stale.db",
			Priority: PrioritySQLite,
			ModTime:  now.Add(-48 * time.Hour),
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/fresh.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now,
			Valid:    true,
		},
	}

	selected, err := SelectBestSourceWithOptions(sources, SelectionOptions{
		PreferFreshest:      false,
		MinimumValidSources: 1,
		MaxAgeDelta:         time.Hour,
	})
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}

	if selected.Path != "/test/fresh.jsonl" {
		t.Fatalf("expected stale high-priority source to be filtered, got %s", selected.Path)
	}
}

// TestSelectBestSource_AllInvalid tests that error is returned when all invalid
func TestSelectBestSource_AllInvalid(t *testing.T) {
	sources := []DataSource{
		{
			Type:  SourceTypeSQLite,
			Path:  "/test/beads.db",
			Valid: false,
		},
		{
			Type:  SourceTypeJSONLLocal,
			Path:  "/test/issues.jsonl",
			Valid: false,
		},
	}

	_, err := SelectBestSource(sources)
	if err != ErrNoValidSources {
		t.Errorf("Expected ErrNoValidSources, got %v", err)
	}
}

// TestSelectBestSource_SkipsInvalid tests that invalid sources are skipped
func TestSelectBestSource_SkipsInvalid(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  now, // Newest, but invalid
			Valid:    false,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/issues.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now.Add(-1 * time.Hour), // Older, but valid
			Valid:    true,
		},
	}

	selected, err := SelectBestSource(sources)
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}

	if selected.Path != "/test/issues.jsonl" {
		t.Errorf("Expected valid JSONL source, got %s", selected.Path)
	}
}

// TestFallbackChain_FirstValid tests fallback when first source works
func TestFallbackChain_FirstValid(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  now,
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/issues.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now.Add(-1 * time.Hour),
			Valid:    true,
		},
	}

	loadCalls := 0
	selected, err := SelectWithFallback(sources, func(s DataSource) error {
		loadCalls++
		return nil // Success
	}, DefaultSelectionOptions())

	if err != nil {
		t.Fatalf("Fallback failed: %v", err)
	}

	if loadCalls != 1 {
		t.Errorf("Expected 1 load call, got %d", loadCalls)
	}
	if selected.Type != SourceTypeSQLite {
		t.Errorf("Expected first source, got %s", selected.Type)
	}
}

// TestFallbackChain_SecondValid tests fallback when first fails
func TestFallbackChain_SecondValid(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  now,
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/issues.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now.Add(-1 * time.Hour),
			Valid:    true,
		},
	}

	loadCalls := 0
	selected, err := SelectWithFallback(sources, func(s DataSource) error {
		loadCalls++
		if s.Type == SourceTypeSQLite {
			return os.ErrNotExist // First source fails
		}
		return nil // Second source works
	}, DefaultSelectionOptions())

	if err != nil {
		t.Fatalf("Fallback failed: %v", err)
	}

	if loadCalls != 2 {
		t.Errorf("Expected 2 load calls, got %d", loadCalls)
	}
	if selected.Type != SourceTypeJSONLLocal {
		t.Errorf("Expected fallback to JSONL, got %s", selected.Type)
	}
}

// TestFallbackChain_AllFail tests fallback when all sources fail
func TestFallbackChain_AllFail(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeSQLite,
			Path:     "/test/beads.db",
			Priority: PrioritySQLite,
			ModTime:  now,
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/issues.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now.Add(-1 * time.Hour),
			Valid:    true,
		},
	}

	_, err := SelectWithFallback(sources, func(s DataSource) error {
		return os.ErrNotExist // All fail
	}, DefaultSelectionOptions())

	if err == nil {
		t.Fatal("Expected error when all sources fail")
	}
}

func TestAutoRefreshManager_HandleChangeCallbackCanReadCurrentSource(t *testing.T) {
	source := createValidJSONLSource(t)
	manager := &AutoRefreshManager{
		currentSource: &DataSource{
			Type:    source.Type,
			Path:    source.Path,
			ModTime: source.ModTime.Add(-time.Minute),
			Valid:   true,
		},
		sources: []DataSource{source},
		opts:    DefaultSelectionOptions(),
	}

	done := make(chan struct{})
	manager.onSourceChange = func(newSource DataSource, reason string) {
		_ = manager.CurrentSource()
		close(done)
	}

	go manager.handleChange(source)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleChange callback deadlocked while reading CurrentSource")
	}
}

func TestAutoRefreshManager_HandleChangeLoggerCanReadCurrentSource(t *testing.T) {
	source := createValidJSONLSource(t)
	manager := &AutoRefreshManager{
		currentSource: &DataSource{
			Type:    source.Type,
			Path:    source.Path,
			ModTime: source.ModTime.Add(-time.Minute),
			Valid:   true,
		},
		sources: []DataSource{source},
	}
	manager.opts = DefaultSelectionOptions()
	manager.opts.Verbose = true
	logged := make(chan struct{}, 1)
	manager.opts.Logger = func(string) {
		_ = manager.CurrentSource()
		select {
		case logged <- struct{}{}:
		default:
		}
	}

	done := make(chan struct{})
	go func() {
		manager.handleChange(source)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleChange deadlocked while its selection logger read CurrentSource")
	}

	select {
	case <-logged:
	default:
		t.Fatal("handleChange returned without invoking its selection logger")
	}
}

func TestSourceWatcher_AddSourceLoggerCanReadSources(t *testing.T) {
	source := createValidJSONLSource(t)
	sw, err := NewSourceWatcher(nil, nil, DefaultWatcherOptions())
	if err != nil {
		t.Fatalf("NewSourceWatcher: %v", err)
	}
	defer sw.Stop()

	sw.verbose = true
	sw.logger = func(string) {
		_ = sw.Sources()
	}

	done := make(chan error, 1)
	go func() {
		done <- sw.AddSource(source)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("AddSource: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AddSource deadlocked while its logger read Sources")
	}
}

func TestSourceWatcher_RemoveSourceLoggerCanReadSources(t *testing.T) {
	source := createValidJSONLSource(t)
	sw, err := NewSourceWatcher([]DataSource{source}, nil, DefaultWatcherOptions())
	if err != nil {
		t.Fatalf("NewSourceWatcher: %v", err)
	}
	defer sw.Stop()

	sw.verbose = true
	sw.logger = func(string) {
		_ = sw.Sources()
	}

	done := make(chan error, 1)
	go func() {
		done <- sw.RemoveSource(source.Path)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RemoveSource: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RemoveSource deadlocked while its logger read Sources")
	}
}

func TestAutoRefreshManager_ForceRefreshCallbackCanReadCurrentSource(t *testing.T) {
	source := createValidJSONLSource(t)
	manager := &AutoRefreshManager{
		sources: []DataSource{source},
		opts:    DefaultSelectionOptions(),
	}

	done := make(chan struct{})
	manager.onSourceChange = func(newSource DataSource, reason string) {
		_ = manager.CurrentSource()
		close(done)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- manager.ForceRefresh()
	}()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ForceRefresh failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ForceRefresh deadlocked while invoking source change callback")
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ForceRefresh returned without invoking source change callback")
	}
}

func TestAutoRefreshManager_ForceRefreshLoggerCanReadCurrentSource(t *testing.T) {
	source := createValidJSONLSource(t)
	manager := &AutoRefreshManager{
		sources: []DataSource{source},
	}
	manager.opts = DefaultSelectionOptions()
	manager.opts.Verbose = true
	logged := make(chan struct{}, 1)
	manager.opts.Logger = func(string) {
		_ = manager.CurrentSource()
		select {
		case logged <- struct{}{}:
		default:
		}
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- manager.ForceRefresh()
	}()

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("ForceRefresh failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ForceRefresh deadlocked while its selection logger read CurrentSource")
	}

	select {
	case <-logged:
	default:
		t.Fatal("ForceRefresh returned without invoking its selection logger")
	}
}

func TestAutoRefreshManager_ForceRefreshPublishesUnchangedSelection(t *testing.T) {
	source := createValidJSONLSource(t)
	manager := &AutoRefreshManager{
		currentSource: &DataSource{
			Type:       source.Type,
			Path:       source.Path,
			Priority:   source.Priority,
			ModTime:    source.ModTime.Add(-time.Hour),
			Valid:      true,
			IssueCount: source.IssueCount,
			Size:       0,
		},
		sources: []DataSource{source},
		opts:    DefaultSelectionOptions(),
	}

	callbackCount := 0
	manager.onSourceChange = func(newSource DataSource, reason string) {
		callbackCount++
		if newSource.Path != source.Path {
			t.Errorf("callback path = %q, want %q", newSource.Path, source.Path)
		}
		if reason != "force refresh" {
			t.Errorf("callback reason = %q, want %q", reason, "force refresh")
		}
	}

	if err := manager.ForceRefresh(); err != nil {
		t.Fatalf("ForceRefresh failed: %v", err)
	}
	if callbackCount != 1 {
		t.Fatalf("callback count = %d, want 1 for an unchanged selected path", callbackCount)
	}
	got := manager.CurrentSource()
	if got.Path != source.Path {
		t.Fatalf("current source path = %q, want %q", got.Path, source.Path)
	}
	if got.Size != source.Size || !got.ModTime.Equal(source.ModTime) {
		t.Fatalf("current source metadata = size %d, modtime %v; want size %d, modtime %v", got.Size, got.ModTime, source.Size, source.ModTime)
	}
}

func createValidJSONLSource(t *testing.T) DataSource {
	t.Helper()

	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	content := `{"id":"TEST-1","title":"Test Issue","status":"open"}` + "\n"
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("write JSONL source: %v", err)
	}
	info, err := os.Stat(jsonlPath)
	if err != nil {
		t.Fatalf("stat JSONL source: %v", err)
	}

	return DataSource{
		Type:     SourceTypeJSONLLocal,
		Path:     jsonlPath,
		Priority: PriorityJSONLLocal,
		ModTime:  info.ModTime(),
		Valid:    true,
		Size:     info.Size(),
	}
}

func createSingleIssueSQLiteDB(t *testing.T, path, id string) {
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
			status TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO issues (id, title, status) VALUES (?, 'Selected SQLite', 'open')`, id)
	if err != nil {
		t.Fatal(err)
	}
}

// Helper to create a test SQLite database with sample data
func TestDiscoverSourcesUsesSQLiteWALFreshness(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, dbPath)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("{\"id\":\"JSONL-1\",\"title\":\"Export\",\"status\":\"open\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	walPath := dbPath + "-wal"
	if err := os.WriteFile(walPath, []byte("pending WAL bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldest := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	middle := oldest.Add(time.Hour)
	newest := middle.Add(time.Hour)
	for path, timestamp := range map[string]time.Time{
		dbPath: oldest, jsonlPath: middle, walPath: newest,
	} {
		if err := os.Chtimes(path, timestamp, timestamp); err != nil {
			t.Fatalf("set mtime for %s: %v", path, err)
		}
	}
	sources, err := DiscoverSources(DiscoveryOptions{BeadsDir: beadsDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) < 2 || sources[0].Type != SourceTypeSQLite || !sources[0].ModTime.Equal(newest) {
		t.Fatalf("sources = %+v, want WAL-fresh SQLite first", sources)
	}
}

func TestDiscoverSourcesPropagatesSQLiteStatFailure(t *testing.T) {
	beadsDir := t.TempDir()
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.Symlink(filepath.Base(dbPath), dbPath); err != nil {
		t.Skipf("cannot create self-referential symlink: %v", err)
	}
	_, err := DiscoverSources(DiscoveryOptions{BeadsDir: beadsDir})
	if err == nil || !strings.Contains(err.Error(), "stat SQLite source") || !strings.Contains(err.Error(), dbPath) {
		t.Fatalf("source stat error = %v, want explicit canonical SQLite refusal", err)
	}
}

func TestSQLiteValidationCacheIdentityIncludesWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "beads.db")
	if err := os.WriteFile(dbPath, []byte("main database identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := DataSource{Type: SourceTypeSQLite, Path: dbPath, ValidationError: "cached pre-WAL failure"}
	storeValidationCache(&source)
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); !hit || err == nil {
		t.Fatalf("initial cache lookup = hit %v, err %v; want cached failure", hit, err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("new committed WAL state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("post-WAL cache lookup = hit %v, err %v; want miss", hit, err)
	}
}

func TestValidationCacheKeyIncludesSourceType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.data")
	if err := os.WriteFile(path, []byte("same bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonl := DataSource{Type: SourceTypeJSONLLocal, Path: path, Valid: true, IssueCount: 1}
	storeValidationCache(&jsonl)
	sqlite := DataSource{Type: SourceTypeSQLite, Path: path}
	if hit, err := lookupValidationCache(&sqlite, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("SQLite lookup after JSONL cache entry = hit %v, err %v; want miss", hit, err)
	}
}

func TestValidationCacheRejectsSameSizeRewriteWithRestoredMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	source := DataSource{Type: SourceTypeJSONLLocal, Path: path, Valid: true, IssueCount: 1}
	before, err := sourceValidationIdentity(&source)
	if err != nil {
		t.Fatal(err)
	}
	if !before.main.hasChangeAt {
		t.Skip("platform does not expose change time")
	}
	storeValidationCache(&source)
	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	after, err := sourceValidationIdentity(&source)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before.main.info, after.main.info) || before.main.size != after.main.size || !before.main.modTime.Equal(after.main.modTime) {
		t.Fatal("test setup did not preserve file identity, size, and mtime")
	}
	if before.main.changeSec == after.main.changeSec && before.main.changeNsec == after.main.changeNsec {
		t.Skip("filesystem did not advance change time at test resolution")
	}
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("same-metadata rewrite cache lookup = hit %v, err %v; want miss", hit, err)
	}
}

func TestValidationCacheDoesNotStoreChangedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := DataSource{Type: SourceTypeJSONLLocal, Path: path, Valid: true, IssueCount: 1}
	before, err := sourceValidationIdentity(&source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second version"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeValidationCacheIfUnchanged(&source, before, true)
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("changed source cache lookup = hit %v, err %v; want miss", hit, err)
	}
}

func createTestSQLiteDB(t *testing.T, path string) {
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
			tombstone INTEGER DEFAULT 0
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		INSERT INTO issues (id, title, status) VALUES
		('TEST-1', 'Test Issue 1', 'open'),
		('TEST-2', 'Test Issue 2', 'closed')
	`)
	if err != nil {
		t.Fatal(err)
	}
}

// Helper to create an empty SQLite database
func createEmptySQLiteDB(t *testing.T, path string) {
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
			tombstone INTEGER DEFAULT 0
		)
	`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDetectInconsistencies_DeterministicTruncationAndCompareFields(t *testing.T) {
	var issuesA, issuesB []model.Issue
	for i := 25; i >= 1; i-- {
		id := fmt.Sprintf("bv-%02d", i)
		issuesA = append(issuesA, model.Issue{
			ID:       id,
			Title:    fmt.Sprintf("Title A %02d", i),
			Status:   model.StatusOpen,
			Priority: 1,
		})
		if i%5 == 0 {
			continue // missing in B
		}
		issuesB = append(issuesB, model.Issue{
			ID:       id,
			Title:    fmt.Sprintf("Title B %02d", i),
			Status:   model.StatusClosed,
			Priority: 3,
		})
	}
	issuesB = append(issuesB, model.Issue{
		ID:       "bv-00",
		Title:    "Only in B",
		Status:   model.StatusOpen,
		Priority: 2,
	})

	opts := DiffOptions{
		MaxDifferences: 6,
		CompareFields:  []string{" STATUS ", "Priority", "TITLE", "status"},
	}

	baseline := DetectInconsistencies(issuesA, issuesB, "beads.db", "issues.jsonl", opts)
	if len(baseline.IssueDiffs) != 6 {
		t.Fatalf("len(IssueDiffs) = %d, want 6: %+v", len(baseline.IssueDiffs), baseline.IssueDiffs)
	}
	// Sorted IDs start at bv-00 (missing in beads.db), bv-01 (status, priority, title), bv-02 (status, priority).
	wantOrder := []struct {
		id    string
		field string
	}{
		{"bv-00", "presence"},
		{"bv-01", "status"},
		{"bv-01", "priority"},
		{"bv-01", "title"},
		{"bv-02", "status"},
		{"bv-02", "priority"},
	}
	for idx, want := range wantOrder {
		got := baseline.IssueDiffs[idx]
		if got.IssueID != want.id || got.Field != want.field {
			t.Fatalf("IssueDiffs[%d] = (%s, %s), want (%s, %s)", idx, got.IssueID, got.Field, want.id, want.field)
		}
	}

	for run := 0; run < 25; run++ {
		again := DetectInconsistencies(issuesA, issuesB, "beads.db", "issues.jsonl", opts)
		if !reflect.DeepEqual(again, baseline) {
			t.Fatalf("run %d produced non-deterministic SourceDiff:\ngot:  %+v\nwant: %+v", run, again, baseline)
		}
	}
}

func TestCheckAllSourcesConsistent_DetectsPriorityAndTitleDriftBetweenSQLiteAndJSONL(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER DEFAULT 2,
			issue_type TEXT DEFAULT 'task',
			tombstone INTEGER DEFAULT 0
		);
		INSERT INTO issues (id, title, status, priority, issue_type) VALUES
			('BV-1', 'Updated SQLite Title', 'open', 0, 'task'),
			('BV-2', 'Only in SQLite', 'open', 2, 'task');
	`); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	jsonlContent := strings.Join([]string{
		`{"id":"BV-1","title":"Stale JSONL Title","status":"open","priority":2,"issue_type":"task"}`,
		`{"id":"BV-3","title":"Only in JSONL","status":"closed","priority":1,"issue_type":"task"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: true,
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}

	diffs, err := CheckSourcesConsistent(sources)
	if err != nil {
		t.Fatalf("CheckSourcesConsistent: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 pair SourceDiff, got %d: %+v", len(diffs), diffs)
	}
	got := diffs[0]
	if len(got.IssueDiffs) != 4 {
		t.Fatalf("expected 4 IssueDiffs (BV-1 priority + title, BV-2 missing, BV-3 missing), got %+v", got.IssueDiffs)
	}
	if _, ok := got.MissingIssues["BV-2"]; !ok {
		t.Fatalf("expected BV-2 in MissingIssues, got %+v", got.MissingIssues)
	}
	if _, ok := got.MissingIssues["BV-3"]; !ok {
		t.Fatalf("expected BV-3 in MissingIssues, got %+v", got.MissingIssues)
	}
}

func TestDetectInconsistencies_PopulatesMissingIssuesClonedAndOmitsInJSON(t *testing.T) {
	issuesA := []model.Issue{
		{ID: "ONLY-A", Title: "Only A", Status: model.StatusOpen, Labels: []string{"backend"}},
	}
	issuesB := []model.Issue{
		{ID: "ONLY-B", Title: "Only B", Status: model.StatusInProgress, Labels: []string{"frontend"}},
	}

	diff := DetectInconsistencies(issuesA, issuesB, ".beads/beads.db", ".beads/issues.jsonl", DefaultDiffOptions())
	if len(diff.MissingIssues) != 2 {
		t.Fatalf("expected 2 entries in MissingIssues, got %d: %+v", len(diff.MissingIssues), diff.MissingIssues)
	}

	// Mutating original slices must not mutate cloned MissingIssues.
	issuesA[0].Labels[0] = "mutated-a"
	issuesB[0].Labels[0] = "mutated-b"

	if got := diff.MissingIssues["ONLY-A"].Labels; len(got) != 1 || got[0] != "backend" {
		t.Fatalf("MissingIssues[ONLY-A] was not cloned cleanly: %+v", diff.MissingIssues["ONLY-A"])
	}
	if got := diff.MissingIssues["ONLY-B"].Labels; len(got) != 1 || got[0] != "frontend" {
		t.Fatalf("MissingIssues[ONLY-B] was not cloned cleanly: %+v", diff.MissingIssues["ONLY-B"])
	}

	rawJSON, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("json.Marshal(SourceDiff): %v", err)
	}
	if strings.Contains(string(rawJSON), "MissingIssues") {
		t.Fatalf("MissingIssues must be json:\"-\", got %s", string(rawJSON))
	}
}

func TestDiscoverSources_SkipsLeftoverSQLiteInBDWorkspace(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"dolt","backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !loader.IsBDWorkspace(beadsDir) {
		t.Fatal("expected test fixture to be recognized by loader.IsBDWorkspace")
	}

	// Plant a leftover pre-migration SQLite beads.db with conflicting rows.
	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			status TEXT NOT NULL,
			priority INTEGER DEFAULT 2,
			issue_type TEXT DEFAULT 'task',
			tombstone INTEGER DEFAULT 0
		);
		INSERT INTO issues (id, title, status, priority, issue_type) VALUES
			('BD-OLD', 'Stale pre-Dolt SQLite row', 'open', 1, 'task');
	`); err != nil {
		t.Fatal(err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"BD-LIVE","title":"Live Dolt export","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: true,
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Type != SourceTypeJSONLLocal || sources[0].Path != jsonlPath {
		t.Fatalf("expected only JSONL export source in bd workspace (leftover beads.db skipped), got %+v", sources)
	}

	diffs, err := CheckSourcesConsistent(sources)
	if err != nil {
		t.Fatalf("CheckSourcesConsistent: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("expected 0 false-positive source diffs in bd workspace, got %+v", diffs)
	}
}

func TestSymlinkedSQLiteWALFreshnessValidationAndWatch(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real-sqlite")
	beadsDir := filepath.Join(root, "workspace", ".beads")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	realDB := filepath.Join(realDir, "beads.db")
	createTestSQLiteDB(t, realDB)
	symlinkDB := filepath.Join(beadsDir, "beads.db")
	if err := os.Symlink(realDB, symlinkDB); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	resolvedDB, err := filepath.EvalSymlinks(realDB)
	if err != nil {
		t.Fatal(err)
	}
	realWAL := resolvedDB + "-wal"
	realSHM := resolvedDB + "-shm"

	// 1. Validation cache invalidates when real target WAL changes behind a symlinked beads.db.
	cachedSource := DataSource{Type: SourceTypeSQLite, Path: symlinkDB, Valid: true, IssueCount: 2}
	storeValidationCache(&cachedSource)
	if hit, err := lookupValidationCache(&cachedSource, DefaultValidationOptions()); !hit || err != nil {
		t.Fatalf("expected initial validation cache hit for symlinked SQLite, got hit=%v err=%v", hit, err)
	}

	// Perform a real WAL-mode SQLite write on resolvedDB so both SQLite validation and WAL freshness hold.
	dbConn, err := sql.Open("sqlite", resolvedDB)
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close()
	if _, err := dbConn.Exec(`PRAGMA journal_mode=WAL; INSERT INTO issues (id, title, status) VALUES ('TEST-3', 'WAL Issue', 'open');`); err != nil {
		t.Fatal(err)
	}

	if hit, _ := lookupValidationCache(&cachedSource, DefaultValidationOptions()); hit {
		t.Fatal("expected validation cache miss after WAL commit behind symlinked SQLite")
	}

	// 2. DiscoverSources and SourceFromFile fold real target WAL mtime into symlinked SQLite source ModTime.
	baseMtime := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	walMtime := baseMtime.Add(2 * time.Hour)
	if err := os.Chtimes(resolvedDB, baseMtime, baseMtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(realWAL, walMtime, walMtime); err != nil {
		t.Fatal(err)
	}

	discovered, err := DiscoverSources(DiscoveryOptions{BeadsDir: beadsDir, ValidateAfterDiscovery: true})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Path != symlinkDB || !discovered[0].ModTime.Equal(walMtime) {
		t.Fatalf("DiscoverSources = %+v, want symlinked SQLite with WAL ModTime %v", discovered, walMtime)
	}
	fromFile, ok, err := SourceFromFile(symlinkDB)
	if err != nil || !ok {
		t.Fatalf("SourceFromFile: ok=%v err=%v", ok, err)
	}
	if !fromFile.ModTime.Equal(walMtime) {
		t.Fatalf("SourceFromFile.ModTime = %v, want WAL ModTime %v", fromFile.ModTime, walMtime)
	}

	// 3. SourceWatcher and AutoRefreshManager fire on real target WAL updates (and ignore -shm writes).
	var armCallbacks atomic.Int32
	armCh := make(chan DataSource, 4)
	manager, err := NewAutoRefreshManager(discovered, AutoRefreshOptions{
		WatcherOptions: WatcherOptions{Debounce: 15 * time.Millisecond, Logger: func(string) {}},
		SelectionOptions: SelectionOptions{
			PreferFreshest:      true,
			MinimumValidSources: 1,
		},
		OnSourceChange: func(newSource DataSource, _ string) {
			armCallbacks.Add(1)
			select {
			case armCh <- newSource:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewAutoRefreshManager: %v", err)
	}
	manager.Start()
	defer manager.Stop()

	// Negative control: -shm metadata/unrelated sibling writes must not trigger AutoRefreshManager callback.
	// (Note: never O_TRUNC a live WAL-mode SQLite -shm file while sql.DB holds a 32KB mmap on it, which raises SIGBUS.)
	shmTouch := time.Now().Add(30 * time.Second)
	if err := os.Chtimes(realSHM, shmTouch, shmTouch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "unrelated.tmp"), []byte("ignore-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case unexpected := <-armCh:
		t.Fatalf("unexpected source change callback on -shm/unrelated activity: %+v", unexpected)
	case <-time.After(80 * time.Millisecond):
	}

	// Positive assertion: SQLite WAL commit advances WAL mtime and triggers AutoRefreshManager callback on symlinkDB.
	if _, err := dbConn.Exec(`INSERT INTO issues (id, title, status) VALUES ('TEST-4', 'Second WAL Issue', 'open');`); err != nil {
		t.Fatal(err)
	}
	laterWAL := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	if err := os.Chtimes(realWAL, laterWAL, laterWAL); err != nil {
		t.Fatal(err)
	}

	select {
	case updated := <-armCh:
		if updated.Path != symlinkDB {
			t.Fatalf("callback source path = %q, want %q", updated.Path, symlinkDB)
		}
		if updated.IssueCount != 4 {
			t.Fatalf("callback IssueCount = %d, want 4", updated.IssueCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for AutoRefreshManager callback on symlinked SQLite WAL update (count=%d)", armCallbacks.Load())
	}
}

func TestSQLiteReader_RecordsLoaderParseMetric(t *testing.T) {
	wasEnabled := metrics.Enabled()
	metrics.SetEnabled(true)
	defer metrics.SetEnabled(wasEnabled)

	dbPath := filepath.Join(t.TempDir(), "beads.db")
	createTestSQLiteDB(t, dbPath)
	src, ok, err := SourceFromFile(dbPath)
	if err != nil || !ok {
		t.Fatalf("SourceFromFile: ok=%v err=%v", ok, err)
	}

	reader, err := NewSQLiteReader(src)
	if err != nil {
		t.Fatalf("NewSQLiteReader: %v", err)
	}
	defer reader.Close()

	before := metrics.LoaderParse.Count()
	issues, err := reader.LoadIssues()
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(issues))
	}
	afterLoadIssues := metrics.LoaderParse.Count()
	if afterLoadIssues != before+1 {
		t.Fatalf("LoaderParse.Count() after LoadIssues = %d, want %d (no double-counting or omission)", afterLoadIssues, before+1)
	}

	if _, _, err := reader.LoadIssueSnapshot(); err != nil {
		t.Fatalf("LoadIssueSnapshot: %v", err)
	}
	afterSnapshot := metrics.LoaderParse.Count()
	if afterSnapshot != before+2 {
		t.Fatalf("LoaderParse.Count() after LoadIssueSnapshot = %d, want %d", afterSnapshot, before+2)
	}
}

func TestSourceWatcher_BurstWALWritesAndSymlinkRetarget(t *testing.T) {
	root := t.TempDir()
	realDirA := filepath.Join(root, "real-sqlite-a")
	realDirB := filepath.Join(root, "real-sqlite-b")
	beadsDir := filepath.Join(root, "workspace", ".beads")
	for _, dir := range []string{realDirA, realDirB, beadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	realDBA := filepath.Join(realDirA, "beads.db")
	createTestSQLiteDB(t, realDBA) // starts with 2 issues
	symlinkDB := filepath.Join(beadsDir, "beads.db")
	if err := os.Symlink(realDBA, symlinkDB); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	resolvedDBA, err := filepath.EvalSymlinks(realDBA)
	if err != nil {
		t.Fatal(err)
	}
	dbConnA, err := sql.Open("sqlite", resolvedDBA)
	if err != nil {
		t.Fatal(err)
	}
	defer dbConnA.Close()
	dbConnA.SetMaxOpenConns(1)
	if _, err := dbConnA.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;`); err != nil {
		t.Fatal(err)
	}

	discovered, err := DiscoverSources(DiscoveryOptions{BeadsDir: beadsDir, ValidateAfterDiscovery: true})
	if err != nil || len(discovered) != 1 {
		t.Fatalf("DiscoverSources = %+v, err=%v", discovered, err)
	}

	updatesCh := make(chan DataSource, 32)
	manager, err := NewAutoRefreshManager(discovered, AutoRefreshOptions{
		WatcherOptions: WatcherOptions{Debounce: 30 * time.Millisecond, Logger: func(string) {}},
		SelectionOptions: SelectionOptions{
			PreferFreshest:      true,
			MinimumValidSources: 1,
		},
		OnSourceChange: func(newSource DataSource, _ string) {
			updatesCh <- newSource
		},
	})
	if err != nil {
		t.Fatalf("NewAutoRefreshManager: %v", err)
	}
	manager.Start()
	defer manager.Stop()

	// 1. Burst-write 10 rapid SQLite WAL transactions within <20ms through symlinkDB.
	// Trailing-edge debounce must coalesce intermediate page writes and deliver the
	// committed final count (2 + 10 = 12 issues), never dropping the final commit.
	for i := 1; i <= 10; i++ {
		if _, err := dbConnA.Exec(`INSERT INTO issues (id, title, status) VALUES (?, ?, 'open')`,
			fmt.Sprintf("BURST-%d", i), fmt.Sprintf("Burst Issue %d", i)); err != nil {
			t.Fatalf("burst insert %d: %v", i, err)
		}
		time.Sleep(1 * time.Millisecond)
	}

	deadline := time.After(2 * time.Second)
	gotFinalBurst := false
	for !gotFinalBurst {
		select {
		case updated := <-updatesCh:
			if updated.Path == symlinkDB && updated.IssueCount == 12 {
				gotFinalBurst = true
			}
		case <-deadline:
			cur := manager.CurrentSource()
			t.Fatalf("timed out waiting for IssueCount=12 after 10 rapid WAL transactions; current=%+v", cur)
		}
	}

	// 2. Atomically retarget symlinkDB to realDirB/beads.db, then commit a WAL write in realDirB.
	// SourceWatcher must dynamically add realDirB and realDirB/beads.db-wal to its fsnotify watches.
	realDBB := filepath.Join(realDirB, "beads.db")
	createTestSQLiteDB(t, realDBB) // 2 issues
	resolvedDBB, err := filepath.EvalSymlinks(realDBB)
	if err != nil {
		t.Fatal(err)
	}
	dbConnB, err := sql.Open("sqlite", resolvedDBB)
	if err != nil {
		t.Fatal(err)
	}
	defer dbConnB.Close()
	dbConnB.SetMaxOpenConns(1)
	if _, err := dbConnB.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		INSERT INTO issues (id, title, status) VALUES ('B-3', 'Target B 3', 'open'), ('B-4', 'Target B 4', 'open'), ('B-5', 'Target B 5', 'open');`); err != nil {
		t.Fatal(err)
	}

	tmpLink := filepath.Join(beadsDir, "beads.db.next")
	if err := os.Symlink(realDBB, tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpLink, symlinkDB); err != nil {
		t.Fatal(err)
	}

	retargetDeadline := time.After(2 * time.Second)
	gotRetarget := false
	for !gotRetarget {
		select {
		case updated := <-updatesCh:
			if updated.Path == symlinkDB && updated.IssueCount == 5 {
				gotRetarget = true
			}
		case <-retargetDeadline:
			t.Fatalf("timed out waiting for symlink retarget IssueCount=5; current=%+v", manager.CurrentSource())
		}
	}

	// Subsequent WAL commit inside the newly retargeted directory realDirB must also fire.
	if _, err := dbConnB.Exec(`INSERT INTO issues (id, title, status) VALUES ('B-6', 'Target B Post-Retarget WAL', 'open');`); err != nil {
		t.Fatal(err)
	}
	postRetargetDeadline := time.After(2 * time.Second)
	for {
		select {
		case updated := <-updatesCh:
			if updated.Path == symlinkDB && updated.IssueCount == 6 {
				return
			}
		case <-postRetargetDeadline:
			t.Fatalf("timed out waiting for post-retarget WAL commit IssueCount=6; current=%+v", manager.CurrentSource())
		}
	}
}

func TestLoadIssuesSilent_ZeroStderrAndCorruptSiblingSQLiteGuard(t *testing.T) {
	t.Setenv("BV_ROBOT", "")
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DIR", "")

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Declare beads.db + issues.jsonl in metadata.json so AttachIssueOrigins binds Origin.Database.
	meta := `{"database":"beads.db","jsonl_export":"issues.jsonl","backend":"sqlite"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	// Older corrupt sibling beads.db + merge artifact that would normally emit stderr warnings.
	corruptDBPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(corruptDBPath, []byte("corrupt sqlite database bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.left.jsonl"), []byte(sidecarIssueA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Newer issues.jsonl with 20 valid issues + 1 malformed line (<10% error rate, emits parse warning).
	var sb strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&sb, "{\"id\":\"ISS-%d\",\"title\":\"Issue %d\",\"status\":\"open\",\"issue_type\":\"task\"}\n", i, i)
	}
	sb.WriteString("{malformed json line\n")
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	older := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	newer := older.Add(5 * time.Minute)
	if err := os.Chtimes(corruptDBPath, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(issuesPath, newer, newer); err != nil {
		t.Fatal(err)
	}

	var loaded LoadResult
	var loadErr error
	stderr := captureStderr(t, func() {
		loaded, loadErr = LoadIssuesSilent(repoDir)
	})
	if loadErr != nil {
		t.Fatalf("LoadIssuesSilent error = %v", loadErr)
	}
	if stderr != "" {
		t.Fatalf("LoadIssuesSilent wrote to os.Stderr: %q", stderr)
	}
	if loaded.Source.Path != issuesPath {
		t.Fatalf("selected source = %q, want %q", loaded.Source.Path, issuesPath)
	}
	if len(loaded.Issues) != 20 {
		t.Fatalf("len(Issues) = %d, want 20", len(loaded.Issues))
	}
	if loaded.Report.WarningCount != 1 || len(loaded.Report.Warnings) != 1 {
		t.Fatalf("expected 1 captured parse warning in Report, got %+v", loaded.Report)
	}
	if !loaded.Report.Stale {
		t.Fatalf("expected Report.Stale=true when sibling beads.db is corrupt: %+v", loaded.Report)
	}
	foundSiblingWarn := false
	for _, w := range loaded.Report.AuthorityWarnings {
		if strings.Contains(w, "sibling SQLite source") && strings.Contains(w, "failed validation") {
			foundSiblingWarn = true
			break
		}
	}
	if !foundSiblingWarn {
		t.Fatalf("expected sibling SQLite validation warning in AuthorityWarnings, got %v", loaded.Report.AuthorityWarnings)
	}
	for _, iss := range loaded.Issues {
		if iss.Origin == nil {
			t.Fatalf("issue %s missing Origin", iss.ID)
		}
		if iss.Origin.Database != "" {
			t.Fatalf("issue %s Origin.Database = %q, want empty when sibling beads.db is corrupt", iss.ID, iss.Origin.Database)
		}
		if iss.Origin.ReadOnlyReason != "sibling SQLite database is corrupt" {
			t.Fatalf("issue %s Origin.ReadOnlyReason = %q, want %q", iss.ID, iss.Origin.ReadOnlyReason, "sibling SQLite database is corrupt")
		}
	}

	// [R2-A1] Non-silent LoadIssues and direct LoadFromSource must enforce the exact same corrupt sibling SQLite guard.
	t.Setenv("BV_ROBOT", "1")
	cliLoaded, cliErr := LoadIssues(repoDir)
	if cliErr != nil {
		t.Fatalf("LoadIssues(repoDir) error = %v", cliErr)
	}
	if !cliLoaded.Report.Stale || len(cliLoaded.Report.AuthorityWarnings) == 0 {
		t.Fatalf("LoadIssues(repoDir) expected Stale=true and AuthorityWarnings on corrupt sibling beads.db, got %+v", cliLoaded.Report)
	}
	for _, iss := range cliLoaded.Issues {
		if iss.Origin == nil || iss.Origin.Database != "" || iss.Origin.ReadOnlyReason != "sibling SQLite database is corrupt" {
			t.Fatalf("LoadIssues issue %s Origin = %+v, want empty Database and corrupt ReadOnlyReason", iss.ID, iss.Origin)
		}
	}

	directLoaded, directErr := LoadFromSource(DataSource{Type: SourceTypeJSONLLocal, Path: issuesPath})
	if directErr != nil {
		t.Fatalf("LoadFromSource(issues.jsonl) error = %v", directErr)
	}
	if !directLoaded.Report.Stale || len(directLoaded.Report.AuthorityWarnings) == 0 {
		t.Fatalf("LoadFromSource(issues.jsonl) expected Stale=true and AuthorityWarnings on corrupt sibling beads.db, got %+v", directLoaded.Report)
	}
	for _, iss := range directLoaded.Issues {
		if iss.Origin == nil || iss.Origin.Database != "" || iss.Origin.ReadOnlyReason != "sibling SQLite database is corrupt" {
			t.Fatalf("LoadFromSource issue %s Origin = %+v, want empty Database and corrupt ReadOnlyReason", iss.ID, iss.Origin)
		}
	}
	t.Setenv("BV_ROBOT", "")

	// Also verify bd workspace fallback in LoadIssuesSilent never writes to os.Stderr.
	bdRepo, bdBeads := makeBDWorkspace(t)
	hideBD(t, bdRepo)
	if err := os.WriteFile(filepath.Join(bdBeads, "issues.jsonl"), []byte(sidecarIssueA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var bdLoaded LoadResult
	var bdErr error
	bdStderr := captureStderr(t, func() {
		bdLoaded, bdErr = LoadIssuesSilent(bdRepo)
	})
	if bdErr != nil {
		t.Fatalf("LoadIssuesSilent(bdRepo) error = %v", bdErr)
	}
	if bdStderr != "" {
		t.Fatalf("LoadIssuesSilent(bdRepo) wrote to os.Stderr: %q", bdStderr)
	}
	if !bdLoaded.Report.Stale || len(bdLoaded.Report.AuthorityWarnings) == 0 {
		t.Fatalf("expected AuthorityWarnings captured on bd fallback, got %+v", bdLoaded.Report)
	}
}
