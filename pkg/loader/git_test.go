package loader

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// parseJSONL is a small test helper to parse issues from JSONL data.
func parseJSONL(data []byte) ([]model.Issue, error) {
	return ParseIssues(bytes.NewReader(data))
}

// setupTestGitRepo creates a temporary git repo with beads files
func setupTestGitRepo(t *testing.T) (string, func()) {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "git-loader-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create .beads directory and initial file
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		cleanup()
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Write initial beads file
	initialContent := `{"id":"ISSUE-1","title":"First issue","status":"open","priority":1,"issue_type":"task"}
{"id":"ISSUE-2","title":"Second issue","status":"open","priority":2,"issue_type":"task"}
`
	beadsFile := filepath.Join(beadsDir, "beads.base.jsonl")
	if err := os.WriteFile(beadsFile, []byte(initialContent), 0644); err != nil {
		cleanup()
		t.Fatalf("failed to write beads file: %v", err)
	}

	// Commit initial state
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")
	// Ensure subsequent commits have a distinct timestamp for deterministic date-based resolution
	time.Sleep(1500 * time.Millisecond)

	// Add a third issue in second commit
	updatedContent := `{"id":"ISSUE-1","title":"First issue","status":"open","priority":1,"issue_type":"task"}
{"id":"ISSUE-2","title":"Second issue","status":"open","priority":2,"issue_type":"task"}
{"id":"ISSUE-3","title":"Third issue","status":"open","priority":3,"issue_type":"task"}
`
	if err := os.WriteFile(beadsFile, []byte(updatedContent), 0644); err != nil {
		cleanup()
		t.Fatalf("failed to update beads file: %v", err)
	}

	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Add third issue")

	return tmpDir, cleanup
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, out)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, out)
	}
	return string(out)
}

func initGitLoaderTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	return repoDir
}

func TestWithoutBeadsAuthorityEnvIsCaseInsensitive(t *testing.T) {
	got := withoutBeadsAuthorityEnv([]string{
		"PATH=/bin",
		"beads_dir=/wrong",
		"BeAdS_Db=/wrong.db",
		"KEEP=value",
	})
	want := []string{"PATH=/bin", "KEEP=value"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("filtered environment = %v, want %v", got, want)
	}
}

func TestGetGitDirIgnoresAmbientRepositoryRouting(t *testing.T) {
	targetRepo := initGitLoaderTestRepo(t)
	foreignRepo := initGitLoaderTestRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(foreignRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", foreignRepo)

	got, err := GetGitDir(targetRepo)
	if err != nil {
		t.Fatalf("GetGitDir: %v", err)
	}
	want := filepath.Join(targetRepo, ".git")
	if got != want {
		t.Fatalf("GetGitDir used ambient repository: got %s, want %s", got, want)
	}
}

func commitGitLoaderFixture(t *testing.T, repoDir, relativePath, content, message string) {
	t.Helper()
	path := filepath.Join(repoDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", relativePath, err)
	}
	runGit(t, repoDir, "add", "--", relativePath)
	runGit(t, repoDir, "commit", "-m", message)
}

func TestNewGitLoader(t *testing.T) {
	loader := NewGitLoader("/some/path")
	if loader.repoPath != "/some/path" {
		t.Errorf("expected repoPath /some/path, got %s", loader.repoPath)
	}
	if loader.cache == nil {
		t.Error("cache should not be nil")
	}
}

func TestNewGitLoaderWithCacheTTL(t *testing.T) {
	ttl := 10 * time.Minute
	loader := NewGitLoaderWithCacheTTL("/some/path", ttl)
	if loader.cache.maxAge != ttl {
		t.Errorf("expected cache TTL %v, got %v", ttl, loader.cache.maxAge)
	}
}

func TestGitLoaderCommandsIgnoreAmbientCrossRepositoryRouting(t *testing.T) {
	targetRepo := initGitLoaderTestRepo(t)
	firstContent := "{\"id\":\"TARGET-1\",\"title\":\"First\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, targetRepo, ".beads/issues.jsonl", firstContent, "target first")
	firstSHA := strings.TrimSpace(runGitOutput(t, targetRepo, "rev-parse", "HEAD"))
	secondContent := firstContent + "{\"id\":\"TARGET-2\",\"title\":\"Second\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, targetRepo, ".beads/issues.jsonl", secondContent, "target second")
	headSHA := strings.TrimSpace(runGitOutput(t, targetRepo, "rev-parse", "HEAD"))
	headDateText := strings.TrimSpace(runGitOutput(t, targetRepo, "log", "--format=%cI", "-n1", "HEAD"))
	headDate, err := time.Parse(time.RFC3339, headDateText)
	if err != nil {
		t.Fatalf("parse target HEAD date %q: %v", headDateText, err)
	}

	foreignRepo := initGitLoaderTestRepo(t)
	commitGitLoaderFixture(t, foreignRepo, "README.md", "foreign repository\n", "foreign only")
	t.Setenv("GIT_DIR", filepath.Join(foreignRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", foreignRepo)

	gitLoader := NewGitLoader(targetRepo)
	resolved, err := gitLoader.ResolveRevision("HEAD")
	if err != nil {
		t.Fatalf("ResolveRevision under ambient routing: %v", err)
	}
	if resolved != headSHA {
		t.Fatalf("ResolveRevision used foreign repository: got %s want %s", resolved, headSHA)
	}
	report, err := gitLoader.LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("LoadAtWithReport under ambient routing: %v", err)
	}
	if report.CommitSHA != headSHA || len(report.Issues) != 2 || report.Issues[0].ID != "TARGET-1" || report.Issues[1].ID != "TARGET-2" {
		t.Fatalf("historical report came from foreign repository: %+v", report)
	}
	if !report.CommitTime.Equal(headDate) {
		t.Fatalf("historical report commit time = %v, want %v", report.CommitTime, headDate)
	}
	revisions, err := gitLoader.ListRevisions(10)
	if err != nil {
		t.Fatalf("ListRevisions under ambient routing: %v", err)
	}
	if len(revisions) != 2 || revisions[0].Message != "target second" || revisions[1].Message != "target first" {
		t.Fatalf("ListRevisions came from foreign repository: %+v", revisions)
	}
	between, err := gitLoader.GetCommitsBetween(firstSHA, headSHA)
	if err != nil {
		t.Fatalf("GetCommitsBetween under ambient routing: %v", err)
	}
	if len(between) != 1 || between[0].SHA != headSHA || between[0].Message != "target second" {
		t.Fatalf("GetCommitsBetween came from foreign repository: %+v", between)
	}
	hasBeads, err := gitLoader.HasBeadsAtRevision("HEAD")
	if err != nil || !hasBeads {
		t.Fatalf("HasBeadsAtRevision under ambient routing = %v, %v", hasBeads, err)
	}
	issuesAtDate, err := gitLoader.LoadAtDate(headDate.Add(time.Hour))
	if err != nil {
		t.Fatalf("LoadAtDate under ambient routing: %v", err)
	}
	if len(issuesAtDate) != 2 || issuesAtDate[1].ID != "TARGET-2" {
		t.Fatalf("LoadAtDate came from foreign repository: %+v", issuesAtDate)
	}
}

func TestGitLoaderIgnoresReplacementRefs(t *testing.T) {
	repoDir := initGitLoaderTestRepo(t)
	oldContent := "{\"id\":\"OLD-1\",\"title\":\"Old\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", oldContent, "old authority")
	currentContent := "{\"id\":\"CURRENT-1\",\"title\":\"Current\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", currentContent, "current authority")
	runGit(t, repoDir, "replace", "HEAD", "HEAD~1")
	if raw := runGitOutput(t, repoDir, "show", "HEAD:.beads/issues.jsonl"); !strings.Contains(raw, "OLD-1") || strings.Contains(raw, "CURRENT-1") {
		t.Fatalf("replacement fixture did not substitute the historical tree: %q", raw)
	}

	report, err := NewGitLoader(repoDir).LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("LoadAtWithReport with replacement ref: %v", err)
	}
	if len(report.Issues) != 1 || report.Issues[0].ID != "CURRENT-1" {
		t.Fatalf("replacement ref changed historical authority: %+v", report)
	}
}

func TestGitLoaderIgnoresAmbientConfigRouting(t *testing.T) {
	repoDir := initGitLoaderTestRepo(t)
	content := "{\"id\":\"CONFIG-SAFE\",\"title\":\"Config safe\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", content, "config safe")
	t.Setenv("GIT_CONFIG_GLOBAL", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "not-an-integer")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")

	report, err := NewGitLoader(repoDir).LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("LoadAtWithReport under ambient config routing: %v", err)
	}
	if len(report.Issues) != 1 || report.Issues[0].ID != "CONFIG-SAFE" {
		t.Fatalf("ambient config changed historical authority: %+v", report)
	}
}

func TestGitLoaderCommandSanitizesAuthorityEnvironmentAndReplacementPolicy(t *testing.T) {
	for _, name := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH",
		"GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_NAMESPACE",
		"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
	} {
		t.Setenv(name, "hostile")
	}
	t.Setenv("LANG", "hostile")
	t.Setenv("LC_ALL", "hostile")

	cmd := NewGitLoader("/target/repository").gitCommand("rev-parse", "HEAD")
	if cmd.Dir != "/target/repository" {
		t.Fatalf("git command directory = %q", cmd.Dir)
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "--no-replace-objects" || cmd.Args[2] != "rev-parse" {
		t.Fatalf("git command policy args = %q", cmd.Args)
	}
	seenLocale := false
	for _, entry := range cmd.Env {
		name, value, _ := strings.Cut(entry, "=")
		if gitLoaderEnvironmentOverridesAuthority(name) {
			t.Fatalf("git command retained authority override %q", entry)
		}
		if strings.EqualFold(name, "LANG") {
			t.Fatalf("git command retained ambient language %q", entry)
		}
		if strings.EqualFold(name, "LC_ALL") {
			if value != "C" {
				t.Fatalf("git command locale = %q", entry)
			}
			seenLocale = true
		}
	}
	if !seenLocale {
		t.Fatal("git command environment missing LC_ALL=C")
	}
}

func TestGitLoader_LoadAt_HEAD(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)
	issues, err := loader.LoadAt("HEAD")
	if err != nil {
		t.Fatalf("LoadAt(HEAD) failed: %v", err)
	}

	if len(issues) != 3 {
		t.Errorf("expected 3 issues at HEAD, got %d", len(issues))
	}
}

func TestGitLoader_LoadAt_OlderCommit(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)
	issues, err := loader.LoadAt("HEAD~1")
	if err != nil {
		t.Fatalf("LoadAt(HEAD~1) failed: %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("expected 2 issues at HEAD~1, got %d", len(issues))
	}
}

func TestGitLoader_ResolveRevision(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	// HEAD should resolve to a SHA
	sha, err := loader.ResolveRevision("HEAD")
	if err != nil {
		t.Fatalf("ResolveRevision(HEAD) failed: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("expected 40-char SHA, got %d chars: %s", len(sha), sha)
	}
}

func TestGitLoaderResolveRevisionPeelsAnnotatedTagAndRejectsTree(t *testing.T) {
	repoDir := initGitLoaderTestRepo(t)
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", "{\"id\":\"TAGGED-1\",\"title\":\"Tagged\",\"status\":\"open\",\"issue_type\":\"task\"}\n", "tagged authority")
	runGit(t, repoDir, "tag", "-a", "historical-tag", "-m", "annotated historical tag")

	gitLoader := NewGitLoader(repoDir)
	resolved, err := gitLoader.ResolveRevision("historical-tag")
	if err != nil {
		t.Fatalf("resolve annotated tag: %v", err)
	}
	wantCommit := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "historical-tag^{commit}"))
	tagObject := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "historical-tag"))
	if resolved != wantCommit || resolved == tagObject {
		t.Fatalf("annotated tag resolved to %q, want peeled commit %q (tag object %q)", resolved, wantCommit, tagObject)
	}

	if _, err := gitLoader.ResolveRevision("HEAD^{tree}"); err == nil {
		t.Fatal("tree object was accepted as a historical commit revision")
	}
}

func TestGitLoader_ResolveRevision_DateString(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	// Get commit date of the first commit (HEAD~1) and ensure date resolution returns that SHA.
	dateStr := runGitOutput(t, repoDir, "log", "--format=%cI", "-n1", "HEAD~1")
	dateStr = strings.TrimSpace(dateStr)
	if dateStr == "" {
		t.Fatalf("expected non-empty commit date")
	}

	expectedSHA := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD~1"))

	sha, err := loader.ResolveRevision(dateStr)
	if err != nil {
		t.Fatalf("ResolveRevision(date) failed: %v", err)
	}

	if sha != expectedSHA {
		t.Fatalf("expected SHA %s for date %s, got %s", expectedSHA, dateStr, sha)
	}
}

func TestGitLoader_LoadAtDateUsesCommitHistory(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	headDate := strings.TrimSpace(runGitOutput(t, repoDir, "log", "--format=%cI", "-n1", "HEAD"))
	if headDate == "" {
		t.Fatalf("expected non-empty HEAD commit date")
	}
	headTime, err := time.Parse(time.RFC3339, headDate)
	if err != nil {
		t.Fatalf("parse HEAD commit date: %v", err)
	}

	issues, err := loader.LoadAtDate(headTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("LoadAtDate after HEAD commit failed: %v", err)
	}

	if len(issues) != 3 {
		t.Fatalf("expected 3 issues after HEAD commit date, got %d", len(issues))
	}
}

func TestGitLoader_ResolveRevision_DateBeforeHistory(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)
	firstDate := strings.TrimSpace(runGitOutput(t, repoDir, "log", "--format=%cI", "-n1", "HEAD~1"))
	if firstDate == "" {
		t.Fatalf("expected non-empty first commit date")
	}
	firstTime, err := time.Parse(time.RFC3339, firstDate)
	if err != nil {
		t.Fatalf("parse first commit date: %v", err)
	}

	_, err = loader.ResolveRevision(firstTime.Add(-time.Hour).Format(time.RFC3339))
	if err == nil {
		t.Fatalf("expected error resolving date before repository history")
	}
}

func TestParseDateStringUsesLocalForDateOnly(t *testing.T) {
	dateStr := "2025-01-02"
	tm, ok := parseDateString(dateStr)
	if !ok {
		t.Fatalf("expected parseDateString to parse date-only string")
	}
	if tm.Location() != time.Local {
		t.Fatalf("expected location to be time.Local, got %v", tm.Location())
	}
}

func TestRevisionCacheExpires(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Use a generous TTL/sleep gap to avoid timing flake on slow clocks.
	loader := NewGitLoaderWithCacheTTL(repoDir, 50*time.Millisecond)

	// First load populates cache
	if _, err := loader.LoadAt("HEAD"); err != nil {
		t.Fatalf("LoadAt failed: %v", err)
	}
	if stats := loader.CacheStats(); stats.ValidEntries != 1 {
		t.Fatalf("expected 1 valid cache entry, got %d", stats.ValidEntries)
	}

	// Wait long enough for entry to expire
	time.Sleep(120 * time.Millisecond)

	// Cache should report zero valid entries, and LoadAt should still succeed (re-fetch)
	if stats := loader.CacheStats(); stats.ValidEntries != 0 {
		t.Fatalf("expected cache entry to expire, got %d valid", stats.ValidEntries)
	}
	if _, err := loader.LoadAt("HEAD"); err != nil {
		t.Fatalf("LoadAt after expiry failed: %v", err)
	}
}

func TestRevisionCacheRejectsFutureTimestamp(t *testing.T) {
	cache := &revisionCache{
		entries: map[string]cacheEntry{
			"future": {
				issues:    []model.Issue{{ID: "stale"}},
				loadedAt:  time.Now().Add(time.Hour),
				commitSHA: "future",
			},
		},
		maxAge: 5 * time.Minute,
	}
	gitLoader := &GitLoader{cache: cache}
	if stats := gitLoader.CacheStats(); stats.ValidEntries != 0 {
		t.Fatalf("future-dated cache entry counted as valid: %+v", stats)
	}
	if report, ok := cache.getReport("future"); ok {
		t.Fatalf("future-dated cache entry was returned: %+v", report)
	}
	if stats := gitLoader.CacheStats(); stats.TotalEntries != 0 {
		t.Fatalf("future-dated cache entry was not evicted: %+v", stats)
	}
}

func TestGetCommitsBetween(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Add a third commit touching beads file
	beadsFile := filepath.Join(repoDir, ".beads", "beads.base.jsonl")
	updated := `{"id":"ISSUE-1","title":"First issue","status":"open","priority":1,"issue_type":"task"}
{"id":"ISSUE-2","title":"Second issue","status":"open","priority":2,"issue_type":"task"}
{"id":"ISSUE-3","title":"Third issue","status":"open","priority":3,"issue_type":"task","assignee":"bob"}
`
	if err := os.WriteFile(beadsFile, []byte(updated), 0644); err != nil {
		t.Fatalf("update beads file: %v", err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "Assign third issue")

	loader := NewGitLoader(repoDir)

	// from first commit to HEAD should include the two newer commits
	fromSHA := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD~2"))
	revs, err := loader.GetCommitsBetween(fromSHA, "HEAD")
	if err != nil {
		t.Fatalf("GetCommitsBetween failed: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 commits between first and HEAD, got %d", len(revs))
	}
	if revs[0].Message == "" || revs[1].Message == "" {
		t.Fatalf("expected commit messages to be populated: %+v", revs)
	}
}

func TestGitLoader_Cache(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	// First load - should populate cache
	issues1, err := loader.LoadAt("HEAD")
	if err != nil {
		t.Fatalf("first LoadAt failed: %v", err)
	}

	// Check cache stats
	stats := loader.CacheStats()
	if stats.ValidEntries != 1 {
		t.Errorf("expected 1 valid cache entry, got %d", stats.ValidEntries)
	}

	// Second load - should hit cache
	issues2, err := loader.LoadAt("HEAD")
	if err != nil {
		t.Fatalf("second LoadAt failed: %v", err)
	}

	if len(issues1) != len(issues2) {
		t.Error("cached and non-cached results differ")
	}
}

func TestGitLoader_ConcurrentMissesReturnCallerOwnedReports(t *testing.T) {
	repoDir := initGitLoaderTestRepo(t)
	content := "{\"id\":\"CONCURRENT-1\",\"title\":\"Original\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", content, "concurrent historical load")

	gitLoader := NewGitLoader(repoDir)
	const callers = 12
	start := make(chan struct{})
	reports := make(chan GitLoadReport, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			report, err := gitLoader.LoadAtWithReport("HEAD")
			if err != nil {
				errs <- err
				return
			}
			reports <- report
		}()
	}
	close(start)
	wg.Wait()
	close(reports)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent historical load: %v", err)
	}
	loaded := make([]GitLoadReport, 0, callers)
	for report := range reports {
		if len(report.Issues) != 1 || report.Issues[0].Title != "Original" || report.CommitSHA == "" {
			t.Fatalf("concurrent historical report = %+v", report)
		}
		loaded = append(loaded, report)
	}
	if len(loaded) != callers {
		t.Fatalf("received %d reports, want %d", len(loaded), callers)
	}
	for i := range loaded {
		loaded[i].Issues[0].Title = "mutated"
	}

	fresh, err := gitLoader.LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("cached historical load after caller mutations: %v", err)
	}
	if len(fresh.Issues) != 1 || fresh.Issues[0].Title != "Original" {
		t.Fatalf("concurrent callers aliased cached report: %+v", fresh.Issues)
	}
	if stats := gitLoader.CacheStats(); stats.TotalEntries != 1 || stats.ValidEntries != 1 {
		t.Fatalf("concurrent misses produced unexpected cache stats: %+v", stats)
	}
}

func TestGitLoaderReportPreservesDroppedRecordEvidenceAcrossCacheHits(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	fixture := "{\"id\":\"READY-1\",\"title\":\"Ready\",\"status\":\"open\",\"issue_type\":\"task\"}\n" +
		"{\"id\":\"BROKEN\"\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write historical fixture: %v", err)
	}
	runGit(t, repoDir, "add", ".beads/issues.jsonl")
	runGit(t, repoDir, "commit", "-m", "add partial historical authority")

	gitLoader := NewGitLoader(repoDir)
	first, err := gitLoader.LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("first historical report: %v", err)
	}
	if first.ParseStats.Valid != 1 || first.ParseStats.Errors != 1 || len(first.Warnings) != 1 {
		t.Fatalf("first historical accounting = %+v warnings=%v", first.ParseStats, first.Warnings)
	}
	if first.SourcePath != ".beads/issues.jsonl" || first.CommitSHA == "" {
		t.Fatalf("first historical authority identity = %q at %q", first.CommitSHA, first.SourcePath)
	}
	first.Issues[0].Title = "caller-mutated"
	first.Warnings[0] = "caller-mutated"

	second, err := gitLoader.LoadAtWithReport("HEAD")
	if err != nil {
		t.Fatalf("cached historical report: %v", err)
	}
	if second.ParseStats != first.ParseStats || len(second.Warnings) != 1 || strings.Contains(second.Warnings[0], "caller-mutated") {
		t.Fatalf("cached accounting was lost or aliased: stats=%+v warnings=%v", second.ParseStats, second.Warnings)
	}
	if len(second.Issues) != 1 || second.Issues[0].Title != "Ready" {
		t.Fatalf("cached historical issues were aliased: %+v", second.Issues)
	}
}

func TestGitLoaderReportExcludesTombstonesWithoutChangingParseAccounting(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	content := strings.Join([]string{
		`{"id":"LIVE-1","title":"Visible","status":"open","issue_type":"task"}`,
		`{"id":"DELETED-1","title":"Deleted","status":"tombstone","issue_type":"task"}`,
	}, "\n") + "\n"
	commitGitLoaderFixture(t, repoDir, ".beads/issues.jsonl", content, "historical tombstone authority")

	gitLoader := NewGitLoader(repoDir)
	for attempt := 1; attempt <= 2; attempt++ {
		report, err := gitLoader.LoadAtWithReport("HEAD")
		if err != nil {
			t.Fatalf("attempt %d historical report: %v", attempt, err)
		}
		if len(report.Issues) != 1 || report.Issues[0].ID != "LIVE-1" {
			t.Fatalf("attempt %d visible historical issues = %+v, want only LIVE-1", attempt, report.Issues)
		}
		if report.ParseStats != (ParseStats{Valid: 2}) {
			t.Fatalf("attempt %d parse stats = %+v, want both valid source records counted", attempt, report.ParseStats)
		}
	}
}

func TestGitLoaderRejectsAllSkippedPreferredSourceWithoutCachingOrFallback(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"_type\":\"sprint\",\"id\":\"sprint-1\"}\n"), 0o644); err != nil {
		t.Fatalf("write all-skipped preferred source: %v", err)
	}
	validFallback := "{\"id\":\"FALLBACK-1\",\"title\":\"Must not load\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(validFallback), 0o644); err != nil {
		t.Fatalf("write lower-precedence fallback: %v", err)
	}
	runGit(t, repoDir, "add", ".beads/issues.jsonl", ".beads/beads.jsonl")
	runGit(t, repoDir, "commit", "-m", "add non-issue historical authority")

	gitLoader := NewGitLoader(repoDir)
	for attempt := 1; attempt <= 2; attempt++ {
		report, err := gitLoader.LoadAtWithReport("HEAD")
		if err == nil {
			t.Fatalf("attempt %d accepted all-skipped preferred source or fell back: %+v", attempt, report)
		}
		for _, want := range []string{".beads/issues.jsonl", "no issue records", "1 non-issue/error lines"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("attempt %d error %q missing %q", attempt, err, want)
			}
		}
		if stats := gitLoader.CacheStats(); stats.TotalEntries != 0 {
			t.Fatalf("attempt %d cached rejected historical authority: %+v", attempt, stats)
		}
	}
}

func TestGitLoaderRejectsMalformedPreferredSourceWithoutLegacyFallback(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	repoDir := initGitLoaderTestRepo(t)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"BROKEN\"\n"), 0o644); err != nil {
		t.Fatalf("write malformed preferred source: %v", err)
	}
	fallback := "{\"id\":\"FALLBACK-1\",\"title\":\"Must not load\",\"status\":\"open\",\"issue_type\":\"task\"}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(fallback), 0o644); err != nil {
		t.Fatalf("write lower-precedence fallback: %v", err)
	}
	runGit(t, repoDir, "add", ".beads/issues.jsonl", ".beads/beads.jsonl")
	runGit(t, repoDir, "commit", "-m", "add corrupt preferred historical authority")

	gitLoader := NewGitLoader(repoDir)
	report, err := gitLoader.LoadAtWithReport("HEAD")
	if err == nil {
		t.Fatalf("malformed preferred source fell through to legacy authority: %+v", report)
	}
	for _, want := range []string{".beads/issues.jsonl", "no issue records"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
	if stats := gitLoader.CacheStats(); stats.TotalEntries != 0 {
		t.Fatalf("malformed preferred authority was cached: %+v", stats)
	}
}

func TestGitLoaderAcceptsAndCachesGenuinelyEmptyHistoricalSource(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write empty historical source: %v", err)
	}
	runGit(t, repoDir, "add", ".beads/issues.jsonl")
	runGit(t, repoDir, "commit", "-m", "add empty historical authority")

	gitLoader := NewGitLoader(repoDir)
	for attempt := 1; attempt <= 2; attempt++ {
		report, err := gitLoader.LoadAtWithReport("HEAD")
		if err != nil {
			t.Fatalf("attempt %d rejected genuinely empty historical source: %v", attempt, err)
		}
		if len(report.Issues) != 0 || report.ParseStats != (ParseStats{}) {
			t.Fatalf("attempt %d empty report = %+v", attempt, report)
		}
		if stats := gitLoader.CacheStats(); stats.TotalEntries != 1 || stats.ValidEntries != 1 {
			t.Fatalf("attempt %d empty historical cache stats = %+v", attempt, stats)
		}
	}
}

func TestGitLoader_ClearCache(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	// Load to populate cache
	_, err := loader.LoadAt("HEAD")
	if err != nil {
		t.Fatalf("LoadAt failed: %v", err)
	}

	// Clear cache
	loader.ClearCache()

	stats := loader.CacheStats()
	if stats.TotalEntries != 0 {
		t.Errorf("expected 0 entries after clear, got %d", stats.TotalEntries)
	}
}

func TestGitLoader_ListRevisions(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	revisions, err := loader.ListRevisions(10)
	if err != nil {
		t.Fatalf("ListRevisions failed: %v", err)
	}

	// We made 2 commits that touched beads files
	if len(revisions) != 2 {
		t.Errorf("expected 2 revisions, got %d", len(revisions))
	}

	// Revisions should be in reverse chronological order
	if len(revisions) >= 2 {
		if revisions[0].Message != "Add third issue" {
			t.Errorf("expected newest commit message 'Add third issue', got %q", revisions[0].Message)
		}
		if revisions[1].Message != "Initial commit" {
			t.Errorf("expected oldest commit message 'Initial commit', got %q", revisions[1].Message)
		}
	}
}

func TestGitLoader_HasBeadsAtRevision(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	// Should have beads at HEAD
	exists, err := loader.HasBeadsAtRevision("HEAD")
	if err != nil {
		t.Fatalf("HasBeadsAtRevision failed: %v", err)
	}
	if !exists {
		t.Error("expected beads to exist at HEAD")
	}
}

func TestGitLoader_HasNoBeadsAtRevision(t *testing.T) {
	repoDir := initGitLoaderTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("no beads yet\n"), 0o644); err != nil {
		t.Fatalf("write README fixture: %v", err)
	}
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "repository without beads")

	exists, err := NewGitLoader(repoDir).HasBeadsAtRevision("HEAD")
	if err != nil {
		t.Fatalf("HasBeadsAtRevision failed: %v", err)
	}
	if exists {
		t.Fatal("reported beads files in a revision that has none")
	}
}

func TestGitLoader_InvalidRevision(t *testing.T) {
	repoDir, cleanup := setupTestGitRepo(t)
	defer cleanup()

	loader := NewGitLoader(repoDir)

	_, err := loader.LoadAt("nonexistent-branch")
	if err == nil {
		t.Error("expected error for invalid revision")
	}
}

func TestParseJSONL(t *testing.T) {
	data := []byte(`{"id":"TEST-1","title":"Test","status":"open","priority":1,"issue_type":"task"}
{"id":"TEST-2","title":"Test 2","status":"closed","priority":2,"issue_type":"task"}
`)
	issues, err := parseJSONL(data)
	if err != nil {
		t.Fatalf("parseJSONL failed: %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(issues))
	}

	if issues[0].ID != "TEST-1" {
		t.Errorf("expected first issue ID TEST-1, got %s", issues[0].ID)
	}
}

func TestParseJSONL_SkipsMalformed(t *testing.T) {
	data := []byte(`{"id":"GOOD-1","title":"Good","status":"open","priority":1,"issue_type":"task"}
{this is not valid json}
{"id":"GOOD-2","title":"Good 2","status":"open","priority":2,"issue_type":"task"}
`)
	issues, err := parseJSONL(data)
	if err != nil {
		t.Fatalf("parseJSONL failed: %v", err)
	}

	// Should skip the malformed line
	if len(issues) != 2 {
		t.Errorf("expected 2 valid issues, got %d", len(issues))
	}
}

func TestParseJSONL_EmptyLines(t *testing.T) {
	data := []byte(`{"id":"TEST-1","title":"Test","status":"open","priority":1,"issue_type":"task"}

{"id":"TEST-2","title":"Test 2","status":"open","priority":2,"issue_type":"task"}

`)
	issues, err := parseJSONL(data)
	if err != nil {
		t.Fatalf("parseJSONL failed: %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("expected 2 issues (empty lines skipped), got %d", len(issues))
	}
}

func TestCacheExpiry(t *testing.T) {
	// Use a very short TTL for testing
	loader := NewGitLoaderWithCacheTTL("/unused", 1*time.Millisecond)

	// Manually add to cache
	loader.cache.set("abc123", nil)

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Should return no valid entries
	stats := loader.CacheStats()
	if stats.ValidEntries != 0 {
		t.Errorf("expected 0 valid entries after expiry, got %d", stats.ValidEntries)
	}
}
