package datasource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

type closeErrorIssueReader struct {
	IssueReader
	closeErr   error
	closeCalls int
}

func (r *closeErrorIssueReader) Close() error {
	r.closeCalls++
	return r.closeErr
}

// #190: an issue failing model validation (updated_at < created_at) must not be
// silently dropped — the load still succeeds, but LastLoadReport records the
// drop with a count and a per-line reason.
func TestLoadReport_TimestampInvertedIssueIsCounted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")

	// 10 valid issues keep the error rate (1/11 ≈ 9.1%) under the smart
	// loader's 10% gate, so this exercises the fused load path.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, `{"id":"bd-ok%d","title":"valid issue %d","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T10:00:00Z","updated_at":"2026-07-06T11:00:00Z"}`+"\n", i, i)
	}
	b.WriteString(`{"id":"bd-test1","title":"timestamp-inverted issue","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T17:42:31Z","updated_at":"2026-07-06T10:43:40Z"}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	issues, err := loadAndValidateJSONL(DataSource{
		Type:     SourceTypeJSONLLocal,
		Path:     path,
		Priority: PriorityJSONLLocal,
	})
	if err != nil {
		t.Fatalf("loadAndValidateJSONL: %v", err)
	}
	if len(issues) != 10 {
		t.Fatalf("expected the 10 valid issues to load, got %d", len(issues))
	}

	rep := LastLoadReport()
	if rep == nil {
		t.Fatalf("expected a load report after a successful JSONL load, got nil")
	}
	if rep.Path != path {
		t.Errorf("report path = %q, want %q", rep.Path, path)
	}
	if rep.Valid != 10 {
		t.Errorf("report valid = %d, want 10", rep.Valid)
	}
	if rep.Errors != 1 {
		t.Errorf("report errors = %d, want 1 (the timestamp-inverted issue must be counted, not silently dropped)", rep.Errors)
	}
	if len(rep.Warnings) != 1 {
		t.Fatalf("report warnings = %v, want exactly one skip reason", rep.Warnings)
	}
	warning := rep.Warnings[0]
	if !strings.Contains(warning, "line 11") {
		t.Errorf("warning %q should name the file line (line 11)", warning)
	}
	if !strings.Contains(warning, "created_at") {
		t.Errorf("warning %q should carry the validation reason (updated_at before created_at)", warning)
	}
}

func TestLoadIssuesFromDirWithOptionsHonorsScopedParserControls(t *testing.T) {
	t.Setenv("BV_ROBOT", "")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	path := filepath.Join(beadsDir, "issues.jsonl")
	var content strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&content, `{"id":"KEEP-%d","title":"valid","status":"open","issue_type":"task"}`+"\n", i)
	}
	content.WriteString(`{"id":"BROKEN",not-json}` + "\n")
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stats loader.ParseStats
	var warnings []string
	warningCount := 0
	issues, err := LoadIssuesFromDirWithOptions(beadsDir, loader.ParseOptions{
		Stats:        &stats,
		WarningCount: &warningCount,
		BufferSize:   loader.DefaultMaxBufferSize,
		IssueFilter:  func(issue *model.Issue) bool { return issue.ID == "KEEP-3" },
		WarningHandler: func(message string) {
			warnings = append(warnings, message)
		},
	})
	if err != nil {
		t.Fatalf("LoadIssuesFromDirWithOptions: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "KEEP-3" {
		t.Fatalf("filtered issues = %+v, want KEEP-3", issues)
	}
	if stats.Valid != 10 || stats.Errors != 1 || stats.Skipped != 0 {
		t.Fatalf("parse stats = %+v, want Valid=10 Errors=1 Skipped=0", stats)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "line 11") {
		t.Fatalf("scoped warnings = %v, want exactly malformed line 11", warnings)
	}
	if warningCount != 1 {
		t.Fatalf("scoped warning count = %d, want 1", warningCount)
	}
}

func TestLoadIssuesFromDirWithOptionsReportsExactWarningCountPastMessageCap(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	var content strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&content, `{"id":"VALID-%d","title":"valid","status":"open","issue_type":"task"}`+"\n", i)
	}
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&content, `{"id":"BROKEN-%d",not-json}`+"\n", i)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(content.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	warningCount := 0
	var warningMessages []string
	issues, err := LoadIssuesFromDirWithOptions(beadsDir, loader.ParseOptions{
		WarningCount: &warningCount,
		WarningHandler: func(message string) {
			warningMessages = append(warningMessages, message)
		},
	})
	if err != nil {
		t.Fatalf("LoadIssuesFromDirWithOptions: %v", err)
	}
	if len(issues) != 200 {
		t.Fatalf("issues=%d, want 200", len(issues))
	}
	if warningCount != 20 {
		t.Fatalf("warning count=%d, want exact count 20", warningCount)
	}
	if len(warningMessages) != maxLoadReportWarnings+1 || !strings.Contains(warningMessages[len(warningMessages)-1], "additional parse warnings omitted") {
		t.Fatalf("bounded warning messages=%q, want %d details plus summary", warningMessages, maxLoadReportWarnings)
	}
}

func TestEmitAuthorityWarningsUsesInteractiveStderrButKeepsRobotModeSilent(t *testing.T) {
	const warning = "higher-ranked tracker source was rejected; using fallback"

	t.Run("interactive", func(t *testing.T) {
		t.Setenv("BV_ROBOT", "")
		stderr := captureDatasourceStderr(t, func() {
			emitAuthorityWarnings(loader.ParseOptions{}, []string{warning})
		})
		if !strings.Contains(stderr, "Warning: "+warning) {
			t.Fatalf("interactive authority warning was silent: %q", stderr)
		}
	})

	t.Run("robot", func(t *testing.T) {
		t.Setenv("BV_ROBOT", "1")
		stderr := captureDatasourceStderr(t, func() {
			emitAuthorityWarnings(loader.ParseOptions{}, []string{warning})
		})
		if stderr != "" {
			t.Fatalf("robot authority warning leaked to stderr: %q", stderr)
		}
	})
}

func TestFusedJSONLValidationIgnoresWhitespaceOnlyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	content := strings.Repeat("   \n\t\n", 20) +
		`{"id":"LIVE-1","title":"valid","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	issues, err := loadAndValidateJSONL(DataSource{Type: SourceTypeJSONLLocal, Path: path})
	if err != nil {
		t.Fatalf("whitespace formatting must not trip the malformed-record gate: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "LIVE-1" {
		t.Fatalf("issues = %+v, want LIVE-1", issues)
	}
	report := LastLoadReport()
	if report == nil || report.Valid != 1 || report.Errors != 0 || len(report.Warnings) != 0 {
		t.Fatalf("load report = %+v, want one valid issue and no errors", report)
	}
}

// #190: when the error rate trips the smart loader's gate (e.g. a tiny JSONL
// whose only record fails validation — the original repro), the legacy
// fallback load must still publish the drop accounting.
func TestLoadReport_LegacyFallbackRecordsDrops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := `{"id":"bd-test1","title":"timestamp-inverted issue","status":"open","issue_type":"task","priority":2,"created_at":"2026-07-06T17:42:31Z","updated_at":"2026-07-06T10:43:40Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	issues, err := loadLegacyJSONL(dir)
	if err != nil {
		t.Fatalf("loadLegacyJSONL: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected the invalid issue to be skipped by the tolerant parse, got %d issues", len(issues))
	}

	rep := LastLoadReport()
	if rep == nil {
		t.Fatalf("expected a load report after legacy fallback load, got nil")
	}
	if rep.Valid != 0 || rep.Errors != 1 {
		t.Errorf("report valid/errors = %d/%d, want 0/1", rep.Valid, rep.Errors)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "created_at") {
		t.Errorf("report warnings = %v, want one reason mentioning created_at", rep.Warnings)
	}
}

func TestLoadReport_LegacyFallbackFiltersTombstonesWithMalformedInput(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "issues.jsonl")
	content := `{"id":"LIVE-1","title":"Live issue","status":"open","issue_type":"task"}` + "\n" +
		`{"id":"DELETED-1","title":"Deleted issue","status":"tombstone","issue_type":"task"}` + "\n" +
		`{"id":"BROKEN",not-json}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fallback fixture: %v", err)
	}

	issues, err := loadLegacyJSONL(dir)
	if err != nil {
		t.Fatalf("loadLegacyJSONL: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "LIVE-1" {
		t.Fatalf("legacy fallback leaked tombstone or lost live issue: %+v", issues)
	}
	report := LastLoadReport()
	if report == nil {
		t.Fatal("legacy fallback did not publish parse accounting")
	}
	if report.Path != path || report.Valid != 2 || report.Errors != 1 || report.Skipped != 0 {
		t.Fatalf("legacy fallback report = %+v, want path=%q Valid=2 Errors=1 Skipped=0", report, path)
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "line 3") {
		t.Fatalf("legacy fallback warnings = %v, want malformed line 3 evidence", report.Warnings)
	}
}

// A clean load must overwrite the previous report so zero-error loads never
// resurface stale problems.
func TestLoadReport_CleanLoadOverwritesPreviousErrors(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "issues.jsonl")
	bad := `{"id":"bd-bad","title":"inverted","status":"open","issue_type":"task","created_at":"2026-07-06T17:00:00Z","updated_at":"2026-07-06T10:00:00Z"}` + "\n"
	if err := os.WriteFile(badPath, []byte(bad), 0o644); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	if _, err := loadLegacyJSONL(dir); err != nil {
		t.Fatalf("load bad fixture: %v", err)
	}
	if rep := LastLoadReport(); rep == nil || rep.Errors != 1 {
		t.Fatalf("expected errors=1 after bad load, got %+v", rep)
	}

	goodDir := t.TempDir()
	goodPath := filepath.Join(goodDir, "issues.jsonl")
	good := `{"id":"bd-ok","title":"valid","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(goodPath, []byte(good), 0o644); err != nil {
		t.Fatalf("write good fixture: %v", err)
	}
	if _, err := loadAndValidateJSONL(DataSource{Type: SourceTypeJSONLLocal, Path: goodPath, Priority: PriorityJSONLLocal}); err != nil {
		t.Fatalf("load good fixture: %v", err)
	}
	rep := LastLoadReport()
	if rep == nil {
		t.Fatalf("expected a load report after clean load")
	}
	if rep.Errors != 0 || rep.Valid != 1 || len(rep.Warnings) != 0 {
		t.Errorf("clean load should overwrite the previous report, got %+v", rep)
	}
	if rep.Path != goodPath {
		t.Errorf("report path = %q, want %q", rep.Path, goodPath)
	}
}

func TestLoadReportSuccessfulSQLiteLoadClearsPriorJSONLEvidence(t *testing.T) {
	recordLoadReport(LoadReport{
		Path:     "stale/issues.jsonl",
		Valid:    1,
		Errors:   1,
		Warnings: []string{"stale warning"},
	})
	dbPath := filepath.Join(t.TempDir(), "beads.db")
	createContractTestSQLiteDB(t, dbPath)
	issues, err := loadAndValidate(DataSource{Type: SourceTypeSQLite, Path: dbPath})
	if err != nil {
		t.Fatalf("load SQLite authority: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("SQLite fixture returned no issues")
	}
	if report := LastLoadReport(); report != nil {
		t.Fatalf("SQLite authority inherited stale JSONL report: %+v", report)
	}
}

func TestLastSelectedSourceRecordsSuccessfulFallbackCandidate(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")

	for _, test := range []struct {
		name           string
		setupFallback  func(t *testing.T, beadsDir string) DataSource
		wantLoadReport bool
		wantIssueID    string
	}{
		{
			name: "invalid fresher JSONL falls back to SQLite",
			setupFallback: func(t *testing.T, beadsDir string) DataSource {
				t.Helper()
				dbPath := filepath.Join(beadsDir, "beads.db")
				createContractTestSQLiteDB(t, dbPath)
				return DataSource{Type: SourceTypeSQLite, Path: dbPath}
			},
			wantIssueID: "CTR-1",
		},
		{
			name: "invalid fresher JSONL falls back to valid JSONL",
			setupFallback: func(t *testing.T, beadsDir string) DataSource {
				t.Helper()
				issuesPath := filepath.Join(beadsDir, "issues.jsonl")
				content := `{"id":"JSONL-1","title":"Valid fallback","status":"open","issue_type":"task"}` + "\n"
				if err := os.WriteFile(issuesPath, []byte(content), 0o644); err != nil {
					t.Fatalf("write valid JSONL fallback: %v", err)
				}
				return DataSource{Type: SourceTypeJSONLLocal, Path: issuesPath}
			},
			wantLoadReport: true,
			wantIssueID:    "JSONL-1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDir := t.TempDir()
			beadsDir := filepath.Join(repoDir, ".beads")
			if err := os.Mkdir(beadsDir, 0o755); err != nil {
				t.Fatalf("mkdir beads: %v", err)
			}
			fallback := test.setupFallback(t, beadsDir)
			invalidPath := filepath.Join(beadsDir, "fresher-invalid.jsonl")
			if err := os.WriteFile(invalidPath, []byte("{not-json}\n"), 0o644); err != nil {
				t.Fatalf("write invalid JSONL candidate: %v", err)
			}
			older := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
			newer := older.Add(time.Hour)
			if err := os.Chtimes(fallback.Path, older, older); err != nil {
				t.Fatalf("age fallback source: %v", err)
			}
			if err := os.Chtimes(invalidPath, newer, newer); err != nil {
				t.Fatalf("freshen invalid source: %v", err)
			}

			sources, err := DiscoverSources(DiscoveryOptions{
				BeadsDir:               beadsDir,
				RepoPath:               repoDir,
				ValidateAfterDiscovery: false,
			})
			if err != nil {
				t.Fatalf("discover fallback fixture: %v", err)
			}
			if len(sources) < 2 || sources[0].Path != invalidPath {
				t.Fatalf("fixture did not put invalid source first by freshness: %+v", sources)
			}

			issues, err := LoadIssues(repoDir)
			if err != nil {
				t.Fatalf("load with invalid fresher candidate: %v", err)
			}
			foundIssue := false
			for _, issue := range issues {
				if issue.ID == test.wantIssueID {
					foundIssue = true
					break
				}
			}
			if !foundIssue {
				t.Fatalf("loaded issues = %+v, want issue %s", issues, test.wantIssueID)
			}
			selected, ok := LastSelectedSource()
			if !ok {
				t.Fatal("LastSelectedSource did not record the successful fallback")
			}
			if selected.Type != fallback.Type || selected.Path != fallback.Path {
				t.Fatalf("LastSelectedSource = %+v, want successful fallback %+v", selected, fallback)
			}
			if selected.Path == invalidPath {
				t.Fatalf("LastSelectedSource recorded rejected fresher candidate %q", invalidPath)
			}
			if warnings := LastAuthorityWarnings(); len(warnings) != 0 {
				t.Fatalf("arbitrary rejected JSONL candidate created tracker-authority warnings: %v", warnings)
			}
			report := LastLoadReport()
			if test.wantLoadReport {
				if report == nil || report.Path != fallback.Path {
					t.Fatalf("LastLoadReport = %+v, want successful JSONL fallback %q", report, fallback.Path)
				}
			} else if report != nil {
				t.Fatalf("SQLite fallback inherited rejected JSONL evidence: %+v", report)
			}
		})
	}
}

func TestLoadSmartRecordsRejectedCanonicalSourceFallback(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")

	for _, test := range []struct {
		name           string
		setupRejected  func(t *testing.T, beadsDir string) string
		setupFallback  func(t *testing.T, beadsDir string) string
		wantLoadReport bool
	}{
		{
			name: "corrupt SQLite falls back to JSONL",
			setupRejected: func(t *testing.T, beadsDir string) string {
				t.Helper()
				path := filepath.Join(beadsDir, "beads.db")
				if err := os.WriteFile(path, []byte("not a SQLite database"), 0o644); err != nil {
					t.Fatalf("write corrupt SQLite: %v", err)
				}
				return path
			},
			setupFallback: func(t *testing.T, beadsDir string) string {
				t.Helper()
				path := filepath.Join(beadsDir, "issues.jsonl")
				if err := os.WriteFile(path, []byte(`{"id":"JSONL-1","title":"Fallback","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
					t.Fatalf("write JSONL fallback: %v", err)
				}
				return path
			},
			wantLoadReport: true,
		},
		{
			name: "corrupt canonical JSONL falls back to SQLite",
			setupRejected: func(t *testing.T, beadsDir string) string {
				t.Helper()
				path := filepath.Join(beadsDir, "issues.jsonl")
				if err := os.WriteFile(path, []byte("{not-json}\n"), 0o644); err != nil {
					t.Fatalf("write corrupt JSONL: %v", err)
				}
				return path
			},
			setupFallback: func(t *testing.T, beadsDir string) string {
				t.Helper()
				path := filepath.Join(beadsDir, "beads.db")
				createContractTestSQLiteDB(t, path)
				return path
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDir := t.TempDir()
			beadsDir := filepath.Join(repoDir, ".beads")
			if err := os.Mkdir(beadsDir, 0o755); err != nil {
				t.Fatalf("mkdir beads: %v", err)
			}
			rejectedPath := test.setupRejected(t, beadsDir)
			fallbackPath := test.setupFallback(t, beadsDir)
			older := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
			newer := older.Add(time.Hour)
			if err := os.Chtimes(fallbackPath, older, older); err != nil {
				t.Fatalf("age fallback source: %v", err)
			}
			if err := os.Chtimes(rejectedPath, newer, newer); err != nil {
				t.Fatalf("freshen rejected source: %v", err)
			}

			issues, err := LoadIssues(repoDir)
			if err != nil {
				t.Fatalf("load fallback: %v", err)
			}
			if len(issues) == 0 {
				t.Fatal("fallback returned no issues")
			}
			warnings := LastAuthorityWarnings()
			joined := strings.Join(warnings, "\n")
			for _, want := range []string{rejectedPath, fallbackPath, "higher-ranked tracker source", "using fallback"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("authority warnings %q do not contain %q", joined, want)
				}
			}
			report := LastLoadReport()
			if test.wantLoadReport {
				if report == nil || !strings.Contains(strings.Join(report.AuthorityWarnings, "\n"), rejectedPath) {
					t.Fatalf("JSONL fallback report omitted authority warning: %+v", report)
				}
			} else if report != nil {
				t.Fatalf("SQLite fallback unexpectedly retained JSONL report: %+v", report)
			}
		})
	}
}

func TestLoadSmartPreservesRejectedAuthoritiesThroughTolerantLegacyFallback(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv(loader.BeadsDBEnvVar, "")
	t.Setenv(loader.BeadsDirEnvVar, "")

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	content := `{"id":"JSONL-1","title":"Tolerant fallback","status":"open","issue_type":"task"}` + "\n" +
		`{"id":"BROKEN",not-json}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write partly malformed JSONL: %v", err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(dbPath, []byte("not a SQLite database"), 0o644); err != nil {
		t.Fatalf("write corrupt SQLite: %v", err)
	}
	older := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if err := os.Chtimes(issuesPath, older, older); err != nil {
		t.Fatalf("age JSONL fallback: %v", err)
	}
	if err := os.Chtimes(dbPath, newer, newer); err != nil {
		t.Fatalf("freshen corrupt SQLite: %v", err)
	}

	issues, err := LoadIssues(repoDir)
	if err != nil {
		t.Fatalf("load tolerant fallback: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "JSONL-1" {
		t.Fatalf("loaded issues = %+v, want sole valid JSONL issue", issues)
	}
	report := LastLoadReport()
	if report == nil || report.Valid != 1 || report.Errors != 1 {
		t.Fatalf("legacy fallback report = %+v, want Valid=1 Errors=1", report)
	}
	warnings := strings.Join(LastAuthorityWarnings(), "\n")
	for _, want := range []string{dbPath, issuesPath, "higher-ranked tracker source", "using fallback"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("authority warnings %q do not contain %q", warnings, want)
		}
	}
	if got := strings.Join(report.AuthorityWarnings, "\n"); got != warnings {
		t.Fatalf("load report authority warnings = %q, want LastAuthorityWarnings %q", got, warnings)
	}
}

func TestLoadFromSourceSQLitePropagatesCloseFailure(t *testing.T) {
	jsonlPath := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"bv-close","title":"close proof","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	baseReader, err := NewJSONLReader(DataSource{Type: SourceTypeJSONLLocal, Path: jsonlPath})
	if err != nil {
		t.Fatalf("create injected reader: %v", err)
	}
	closeErr := errors.New("injected close failure")
	reader := &closeErrorIssueReader{IssueReader: baseReader, closeErr: closeErr}
	source := DataSource{Type: SourceTypeSQLite, Path: "injected.db"}

	issues, err := loadFromSourceWithReaderFactory(source, func(got DataSource) (IssueReader, error) {
		if got != source {
			t.Fatalf("reader factory source = %+v, want %+v", got, source)
		}
		return reader, nil
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("LoadFromSource error = %v, want injected close error", err)
	}
	if issues != nil {
		t.Fatalf("LoadFromSource returned %d issues despite close failure", len(issues))
	}
	if reader.closeCalls != 1 {
		t.Fatalf("reader Close calls = %d, want exactly 1", reader.closeCalls)
	}
	if report := LastLoadReport(); report == nil || report.Path != jsonlPath {
		t.Fatalf("failed SQLite close cleared load evidence: %+v", report)
	}
}

func TestLoadFromSourceJSONLReplacesStaleLoadReport(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.jsonl")
	if err := os.WriteFile(firstPath, []byte(
		`{"id":"FIRST","title":"valid","status":"open","issue_type":"task"}`+"\n"+
			`{"id":"BROKEN",not-json}`+"\n",
	), 0o644); err != nil {
		t.Fatalf("write first source: %v", err)
	}
	firstRecorder := newLoadRecorder(firstPath)
	if _, err := loader.LoadIssuesFromFileWithOptions(firstPath, firstRecorder.options()); err != nil {
		t.Fatalf("seed stale report: %v", err)
	}
	firstRecorder.commit()

	secondPath := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(secondPath, []byte(
		`{"id":"SECOND","title":"valid","status":"open","issue_type":"task"}`+"\n",
	), 0o644); err != nil {
		t.Fatalf("write second source: %v", err)
	}
	issues, err := LoadFromSource(DataSource{Type: SourceTypeJSONLLocal, Path: secondPath})
	if err != nil {
		t.Fatalf("LoadFromSource: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "SECOND" {
		t.Fatalf("issues = %+v, want SECOND", issues)
	}
	report := LastLoadReport()
	if report == nil || report.Path != secondPath || report.Valid != 1 || report.Errors != 0 {
		t.Fatalf("load report = %+v, want clean second source", report)
	}
}
