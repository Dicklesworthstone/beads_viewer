package datasource

import (
	"database/sql"
	"os"
	"path/filepath"
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
