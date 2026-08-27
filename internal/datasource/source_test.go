package datasource

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

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
		RepoPath:               filepath.Dir(beadsDir),
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

func TestDiscoverSourcesUsesSQLiteWALFreshness(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	createTestSQLiteDB(t, dbPath)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"JSONL-1","title":"Export","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write JSONL: %v", err)
	}
	walPath := dbPath + "-wal"
	if err := os.WriteFile(walPath, []byte("pending WAL bytes"), 0o644); err != nil {
		t.Fatalf("write WAL marker: %v", err)
	}
	oldest := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	middle := oldest.Add(time.Hour)
	newest := middle.Add(time.Hour)
	for path, timestamp := range map[string]time.Time{
		dbPath:    oldest,
		jsonlPath: middle,
		walPath:   newest,
	} {
		if err := os.Chtimes(path, timestamp, timestamp); err != nil {
			t.Fatalf("set mtime for %s: %v", path, err)
		}
	}

	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(sources) < 2 || sources[0].Type != SourceTypeSQLite {
		t.Fatalf("sources = %+v, want WAL-fresh SQLite ranked before JSONL", sources)
	}
	if !sources[0].ModTime.Equal(newest) {
		t.Fatalf("SQLite effective mtime = %v, want WAL mtime %v", sources[0].ModTime, newest)
	}
}

func TestSQLiteValidationCacheIdentityIncludesWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "beads.db")
	if err := os.WriteFile(dbPath, []byte("main database identity"), 0o644); err != nil {
		t.Fatalf("write database marker: %v", err)
	}
	source := DataSource{
		Type:            SourceTypeSQLite,
		Path:            dbPath,
		Valid:           false,
		ValidationError: "cached pre-WAL failure",
	}
	storeValidationCache(&source)
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); !hit || err == nil {
		t.Fatalf("initial validation cache lookup = hit %v, err %v; want cached failure", hit, err)
	}

	if err := os.WriteFile(dbPath+"-wal", []byte("new committed WAL state"), 0o644); err != nil {
		t.Fatalf("write WAL marker: %v", err)
	}
	if hit, err := lookupValidationCache(&source, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("post-WAL validation cache lookup = hit %v, err %v; want cache miss", hit, err)
	}
}

func TestValidationCacheKeyIncludesSourceType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.data")
	if err := os.WriteFile(path, []byte("same bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	jsonl := DataSource{
		Type:       SourceTypeJSONLLocal,
		Path:       path,
		Valid:      true,
		IssueCount: 1,
	}
	storeValidationCache(&jsonl)

	sqlite := DataSource{Type: SourceTypeSQLite, Path: path}
	if hit, err := lookupValidationCache(&sqlite, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("SQLite lookup after JSONL cache entry = hit %v, err %v; want cache miss", hit, err)
	}
}

func TestValidationCacheDetectsSameIdentityRewriteWithRestoredMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatalf("write initial source: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("pin initial metadata: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat initial source: %v", err)
	}
	before := validationPathIdentityFromInfo(beforeInfo)
	if !before.hasChangeAt {
		t.Skip("platform does not expose a change-time token")
	}

	source := DataSource{Type: SourceTypeJSONLLocal, Path: path, Valid: true, IssueCount: 1}
	storeValidationCache(&source)
	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("restore source metadata: %v", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rewritten source: %v", err)
	}
	after := validationPathIdentityFromInfo(afterInfo)
	if !os.SameFile(beforeInfo, afterInfo) || before.size != after.size || !before.modTime.Equal(after.modTime) {
		t.Fatal("test setup did not preserve identity, size, and modification time")
	}
	if before.changeSec == after.changeSec && before.changeNsec == after.changeNsec {
		t.Skip("filesystem did not advance change time at test resolution")
	}

	changed := DataSource{Type: SourceTypeJSONLLocal, Path: path}
	if hit, err := lookupValidationCache(&changed, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("rewritten source cache lookup = hit %v, err %v; want miss", hit, err)
	}
}

func TestValidationCacheDoesNotStoreVerdictForFileChangedDuringValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"VALID-1","title":"valid","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write initial source: %v", err)
	}
	source := DataSource{Type: SourceTypeJSONLLocal, Path: path}
	opts := DefaultValidationOptions()
	opts.Verbose = true
	var rewriteErr error
	opts.Logger = func(string) {
		if rewriteErr == nil {
			rewriteErr = os.WriteFile(path, []byte("not valid JSONL anymore\n"), 0o644)
		}
	}
	if err := ValidateSourceWithOptions(&source, opts); err != nil {
		t.Fatalf("validation of initial source: %v", err)
	}
	if rewriteErr != nil {
		t.Fatalf("rewrite source during validation: %v", rewriteErr)
	}

	changed := DataSource{Type: SourceTypeJSONLLocal, Path: path}
	if hit, err := lookupValidationCache(&changed, DefaultValidationOptions()); hit || err != nil {
		t.Fatalf("changed source cache lookup = hit %v, err %v; want miss", hit, err)
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

func TestDiscoverSourcesPropagatesSQLiteStatFailure(t *testing.T) {
	beadsDir := t.TempDir()
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.Symlink(filepath.Base(dbPath), dbPath); err != nil {
		t.Skipf("cannot create self-referential symlink fixture: %v", err)
	}

	_, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		ValidateAfterDiscovery: false,
	})
	if err == nil {
		t.Fatal("DiscoverSources silently ignored an unreadable canonical SQLite source")
	}
	for _, want := range []string{"stat SQLite source", dbPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("DiscoverSources error = %q, want %q", err, want)
		}
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

	issues, err := LoadIssues(tmpDir)
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

	issues, err := LoadIssues(tmpDir)
	if err != nil {
		t.Fatalf("LoadIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "SQLITE-1" {
		t.Fatalf("expected explicit SQLite source, got %#v", issues)
	}
}

func TestLoadIssues_ExplicitDirectoryDoesNotMixInCallerWorktreeExports(t *testing.T) {
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
			issues, err := LoadIssues(repoDir)
			if err != nil {
				t.Fatalf("LoadIssues with explicit directory: %v", err)
			}
			if len(issues) != 1 || issues[0].ID != "SELECTED-1" {
				t.Fatalf("explicit directory loaded issues = %#v, want only SELECTED-1", issues)
			}
			selected, ok := LastSelectedSource()
			if !ok || selected.Path != selectedPath {
				t.Fatalf("selected source = %+v (ok=%t), want %s", selected, ok, selectedPath)
			}
		})
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

func TestSelectBestSourceEqualTimePrefersCanonicalJSONLName(t *testing.T) {
	now := time.Now()
	sources := []DataSource{
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/beads.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now,
			Valid:    true,
		},
		{
			Type:     SourceTypeJSONLLocal,
			Path:     "/test/issues.jsonl",
			Priority: PriorityJSONLLocal,
			ModTime:  now,
			Valid:    true,
		},
	}

	selected, err := SelectBestSource(sources)
	if err != nil {
		t.Fatalf("Selection failed: %v", err)
	}
	if selected.Path != "/test/issues.jsonl" {
		t.Fatalf("selected %s, want canonical issues.jsonl", selected.Path)
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
