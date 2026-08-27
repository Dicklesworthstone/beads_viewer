package workspace_test

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"
)

func requireWorkspaceLoaderString(t *testing.T, name, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("expected %s %q, got %q", name, want, got)
	}
}

func captureWorkspaceStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() {
		os.Stderr = original
		_ = writer.Close()
		_ = reader.Close()
	}()

	fn()
	os.Stderr = original
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		_ = reader.Close()
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stderr reader: %v", err)
	}
	return string(output)
}

// createTestBeadsFile creates a .beads/beads.jsonl file with test issues
func createTestBeadsFile(t *testing.T, repoPath string, issues []model.Issue) {
	t.Helper()

	createTestBeadsFileAt(t, filepath.Join(repoPath, ".beads"), issues)
}

func createTestBeadsFileAt(t *testing.T, beadsDir string, issues []model.Issue) {
	t.Helper()

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	beadsFile := filepath.Join(beadsDir, "beads.jsonl")
	file, err := os.Create(beadsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	encoder := json.NewEncoder(file)
	for _, issue := range issues {
		// Provide sensible defaults so validation in loader passes
		if issue.IssueType == "" {
			issue.IssueType = model.TypeTask
		}
		if issue.Status == "" {
			issue.Status = model.StatusOpen
		}
		if err := encoder.Encode(issue); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAggregateLoaderLoadAll(t *testing.T) {
	tmpDir := t.TempDir()

	// Create api repo with issues
	apiRepo := filepath.Join(tmpDir, "services", "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "AUTH-1", Title: "Auth feature", Status: model.StatusOpen, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "AUTH-2", Title: "Auth bug", Status: model.StatusClosed, Priority: 2, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	// Create web repo with issues
	webRepo := filepath.Join(tmpDir, "apps", "web")
	if err := os.MkdirAll(webRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, webRepo, []model.Issue{
		{ID: "UI-1", Title: "UI feature", Status: model.StatusOpen, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	// Create workspace config
	config := &workspace.Config{
		Name: "test-workspace",
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "services/api", Prefix: "api-"},
			{Name: "web", Path: "apps/web", Prefix: "web-"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())

	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	// Should have 3 total issues
	if len(issues) != 3 {
		t.Errorf("len(issues) = %d, want 3", len(issues))
	}

	// Should have 2 results (one per repo)
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2", len(results))
	}

	// Check namespacing
	issueIDs := make(map[string]bool, len(issues))
	for _, issue := range issues {
		issueIDs[issue.ID] = true
	}
	if !issueIDs["api-AUTH-1"] {
		t.Error("Expected to find api-AUTH-1 (namespaced)")
	}
	if !issueIDs["web-UI-1"] {
		t.Error("Expected to find web-UI-1 (namespaced)")
	}

	for _, issue := range issues {
		switch issue.ID {
		case "api-AUTH-1", "api-AUTH-2":
			requireWorkspaceLoaderString(t, issue.ID+" SourceRepo", issue.SourceRepo, "api")
		case "web-UI-1":
			requireWorkspaceLoaderString(t, issue.ID+" SourceRepo", issue.SourceRepo, "web")
		}
	}
}

func TestAggregateLoaderReportsDroppedRecordsFromSuccessfulRepository(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "api")
	beadsDir := filepath.Join(repoPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"id":"GOOD-1","title":"Good","status":"open","issue_type":"task"}` + "\n" +
		`{"id":"BROKEN",not-json}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	config := &workspace.Config{Repos: []workspace.RepoConfig{{Name: "api", Path: "api", Prefix: "api-"}}}
	aggregate := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := aggregate.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "api-GOOD-1" {
		t.Fatalf("loaded issues = %+v, want only namespaced valid record", issues)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("load results = %+v, want one tolerant successful repository", results)
	}
	if got := results[0].ParseStats; got.Valid != 1 || got.Errors != 1 || got.Skipped != 0 {
		t.Fatalf("parse stats = %+v, want Valid=1 Errors=1 Skipped=0", got)
	}
}

func TestAggregateLoaderExcludesTombstonesButKeepsSourceAccounting(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "api")
	createTestBeadsFile(t, repoPath, []model.Issue{
		{ID: "LIVE-1", Title: "Live", Status: model.StatusOpen},
		{ID: "OLD-1", Title: "Deleted", Status: model.StatusTombstone},
	})

	config := &workspace.Config{Repos: []workspace.RepoConfig{{Name: "api", Path: "api", Prefix: "api-"}}}
	issues, results, err := workspace.NewAggregateLoader(config, tmpDir).LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "api-LIVE-1" {
		t.Fatalf("workspace issues = %+v, want only api-LIVE-1", issues)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("load results = %+v, want one successful repository", results)
	}
	if got := results[0].ParseStats; got.Valid != 2 || got.Errors != 0 || got.Skipped != 0 {
		t.Fatalf("parse stats = %+v, want source Valid=2 Errors=0 Skipped=0", got)
	}
}

func TestAggregateLoaderDefaultParseWarningsRemainInteractiveAndRobotSafe(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, "api", ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := "{\"id\":\"GOOD-1\",\"title\":\"Good\",\"status\":\"open\",\"issue_type\":\"task\"}\n" +
		"{\"id\":\"BROKEN\",not-json}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tolerant workspace source: %v", err)
	}
	config := &workspace.Config{Repos: []workspace.RepoConfig{{Name: "api", Path: "api", Prefix: "api-"}}}

	for _, tc := range []struct {
		name         string
		robot        string
		wantWarnings int
	}{
		{name: "interactive", robot: "", wantWarnings: 1},
		{name: "robot", robot: "1", wantWarnings: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BV_ROBOT", tc.robot)
			aggregate := workspace.NewAggregateLoader(config, tmpDir)
			var issues []model.Issue
			var results []workspace.LoadResult
			var loadErr error
			stderr := captureWorkspaceStderr(t, func() {
				issues, results, loadErr = aggregate.LoadAll(context.Background())
			})
			if loadErr != nil {
				t.Fatalf("LoadAll() error = %v", loadErr)
			}
			if len(issues) != 1 || len(results) != 1 || results[0].ParseStats.Valid != 1 || results[0].ParseStats.Errors != 1 {
				t.Fatalf("tolerant load lost issue/evidence: issues=%+v results=%+v", issues, results)
			}
			if got := strings.Count(stderr, "Warning: skipping malformed JSON on line 2:"); got != tc.wantWarnings {
				t.Fatalf("stderr warning count=%d, want %d; stderr=%q", got, tc.wantWarnings, stderr)
			}
		})
	}
}

func TestAggregateLoaderCustomLoggerReceivesParseWarningOnce(t *testing.T) {
	t.Setenv("BV_ROBOT", "")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, "api", ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := "{\"id\":\"GOOD-1\",\"title\":\"Good\",\"status\":\"open\",\"issue_type\":\"task\"}\n" +
		"{\"id\":\"BROKEN\",not-json}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatalf("write tolerant workspace source: %v", err)
	}

	config := &workspace.Config{Repos: []workspace.RepoConfig{{Name: "api", Path: "api", Prefix: "api-"}}}
	aggregate := workspace.NewAggregateLoader(config, tmpDir)
	var logged strings.Builder
	aggregate.SetLogger(log.New(&logged, "", 0))
	stderr := captureWorkspaceStderr(t, func() {
		if _, _, err := aggregate.LoadAll(context.Background()); err != nil {
			t.Errorf("LoadAll() error = %v", err)
		}
	})
	if stderr != "" {
		t.Fatalf("custom logger duplicated parse warning to stderr: %q", stderr)
	}
	if got := strings.Count(logged.String(), "repository \"api\": skipping malformed JSON on line 2:"); got != 1 {
		t.Fatalf("custom logger warning count=%d, want 1; log=%q", got, logged.String())
	}
}

func TestAggregateLoaderRejectsAllSkippedRepositoryAsWrongSource(t *testing.T) {
	tmpDir := t.TempDir()
	goodRepo := filepath.Join(tmpDir, "good")
	createTestBeadsFile(t, goodRepo, []model.Issue{{ID: "READY-1", Title: "Ready"}})
	skippedBeadsDir := filepath.Join(tmpDir, "skipped", ".beads")
	if err := os.MkdirAll(skippedBeadsDir, 0o755); err != nil {
		t.Fatalf("create skipped repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skippedBeadsDir, "issues.jsonl"), []byte("{\"_type\":\"sprint\",\"id\":\"sprint-1\"}\n"), 0o644); err != nil {
		t.Fatalf("write all-skipped source: %v", err)
	}
	config := &workspace.Config{Repos: []workspace.RepoConfig{
		{Name: "good", Path: "good", Prefix: "good-"},
		{Name: "skipped", Path: "skipped", Prefix: "skipped-"},
	}}
	issues, results, err := workspace.NewAggregateLoader(config, tmpDir).LoadAll(context.Background())
	if err != nil {
		t.Fatalf("one valid repository should keep exploratory workspace load available: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "good-READY-1" {
		t.Fatalf("loaded issues = %+v, want only the valid repository", issues)
	}
	if len(results) != 2 || results[1].Error == nil || results[1].ParseStats.Skipped != 1 || results[1].ParseStats.Valid != 0 {
		t.Fatalf("all-skipped repository was not marked failed/incomplete: %+v", results)
	}
}

func TestAggregateLoaderRejectsCrossRepositoryNamespacedIDCollision(t *testing.T) {
	tmpDir := t.TempDir()
	createTestBeadsFile(t, filepath.Join(tmpDir, "one"), []model.Issue{{ID: "b-1", Title: "First"}})
	createTestBeadsFile(t, filepath.Join(tmpDir, "two"), []model.Issue{{ID: "1", Title: "Second"}})
	config := &workspace.Config{Repos: []workspace.RepoConfig{
		{Name: "one", Path: "one", Prefix: "a-"},
		{Name: "two", Path: "two", Prefix: "a-b-"},
	}}
	issues, results, err := workspace.NewAggregateLoader(config, tmpDir).LoadAll(context.Background())
	if err == nil {
		t.Fatalf("namespaced ID collision returned issues %+v and results %+v", issues, results)
	}
	for _, want := range []string{"duplicate issue IDs", "a-b-1", `path "one"`, `path "two"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("collision error %q missing %q", err, want)
		}
	}
}

func TestAggregateLoaderRejectsNormalizedSourceRepoCollision(t *testing.T) {
	tmpDir := t.TempDir()
	createTestBeadsFile(t, filepath.Join(tmpDir, "one"), []model.Issue{{ID: "1", Title: "First"}})
	createTestBeadsFile(t, filepath.Join(tmpDir, "two"), []model.Issue{{ID: "2", Title: "Second"}})
	config := &workspace.Config{Repos: []workspace.RepoConfig{
		{Name: "one", Path: "one", Prefix: "api-"},
		{Name: "two", Path: "two", Prefix: "api:"},
	}}
	issues, results, err := workspace.NewAggregateLoader(config, tmpDir).LoadAll(context.Background())
	if err == nil {
		t.Fatalf("normalized source repository collision returned issues %+v and results %+v", issues, results)
	}
	for _, want := range []string{"normalized source repository key", `"api"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("source repository collision error %q missing %q", err, want)
		}
	}
}

func TestAggregateLoaderSourceRepoKeyForHyphenatedDefaultPrefix(t *testing.T) {
	tmpDir := t.TempDir()

	repoPath := filepath.Join(tmpDir, "backend-service")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, repoPath, []model.Issue{
		{ID: "AUTH-1", Title: "Auth feature", Status: model.StatusOpen, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	config := &workspace.Config{
		Name: "test-workspace",
		Repos: []workspace.RepoConfig{
			{Path: "backend-service"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, _, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1", len(issues))
	}
	if issues[0].ID != "backend-service-AUTH-1" {
		t.Fatalf("issue ID = %q, want %q", issues[0].ID, "backend-service-AUTH-1")
	}
	requireWorkspaceLoaderString(t, "SourceRepo", issues[0].SourceRepo, "backend-service")
}

func TestAggregateLoaderPartialFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Create only api repo (web repo missing)
	apiRepo := filepath.Join(tmpDir, "services", "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "AUTH-1", Title: "Auth feature", Status: model.StatusOpen, Priority: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "services/api", Prefix: "api-"},
			{Name: "web", Path: "apps/web", Prefix: "web-"}, // This repo doesn't exist
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())

	// Should not return error for partial failures
	if err != nil {
		t.Fatalf("LoadAll() should not error on partial failure: %v", err)
	}

	// Should still have issues from api repo
	if len(issues) != 1 {
		t.Errorf("len(issues) = %d, want 1", len(issues))
	}

	// Check results
	var apiResult, webResult *workspace.LoadResult
	for i := range results {
		if results[i].RepoName == "api" {
			apiResult = &results[i]
		}
		if results[i].RepoName == "web" {
			webResult = &results[i]
		}
	}

	if apiResult == nil || apiResult.Error != nil {
		t.Error("api repo should load successfully")
	}
	if webResult == nil || webResult.Error == nil {
		t.Error("web repo should have error (missing)")
	}
}

func TestAggregateLoaderAllReposFailed(t *testing.T) {
	tmpDir := t.TempDir()

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "missing-api", Prefix: "api-"},
			{Name: "web", Path: "missing-web", Prefix: "web-"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())

	if err == nil {
		t.Fatal("LoadAll() should error when every enabled repo fails")
	}
	if issues != nil {
		t.Errorf("issues = %v, want nil on all-failed workspace", issues)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	for _, result := range results {
		if result.Error == nil {
			t.Errorf("repo %q should have an error", result.RepoName)
		}
	}
}

func TestAggregateLoaderEmptySuccessfulRepo(t *testing.T) {
	tmpDir := t.TempDir()

	emptyRepo := filepath.Join(tmpDir, "empty")
	if err := os.MkdirAll(emptyRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, emptyRepo, nil)

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "empty", Path: "empty", Prefix: "empty-"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())

	if err != nil {
		t.Fatalf("LoadAll() should not error for an empty successful repo: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("len(issues) = %d, want 0", len(issues))
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Error != nil {
		t.Errorf("empty repo should load successfully, got %v", results[0].Error)
	}
}

func TestAggregateLoaderNamespacesDependencies(t *testing.T) {
	tmpDir := t.TempDir()

	apiRepo := filepath.Join(tmpDir, "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}

	// Create issue with dependencies
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{
			ID:       "AUTH-1",
			Title:    "Auth feature",
			Status:   model.StatusOpen,
			Priority: 1,
			Dependencies: []*model.Dependency{
				{
					IssueID:     "AUTH-1",
					DependsOnID: "AUTH-2", // Local dependency
					Type:        model.DepBlocks,
				},
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "AUTH-2",
			Title:     "Prerequisite",
			Status:    model.StatusOpen,
			Priority:  0,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	})

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: "api", Prefix: "be-"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, _, err := loader.LoadAll(context.Background())

	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	// Find AUTH-1 and check its dependencies are namespaced
	var auth1 *model.Issue
	for i := range issues {
		if issues[i].ID == "be-AUTH-1" {
			auth1 = &issues[i]
			break
		}
	}

	if auth1 == nil {
		t.Fatal("Could not find be-AUTH-1")
	}

	if len(auth1.Dependencies) == 0 {
		t.Fatal("Expected dependencies to be preserved")
	}

	dep := auth1.Dependencies[0]
	if dep.IssueID != "be-AUTH-1" {
		t.Errorf("Dependency IssueID = %q, want %q", dep.IssueID, "be-AUTH-1")
	}
	if dep.DependsOnID != "be-AUTH-2" {
		t.Errorf("Dependency DependsOnID = %q, want %q", dep.DependsOnID, "be-AUTH-2")
	}
}

func TestAggregateLoaderDisabledRepos(t *testing.T) {
	tmpDir := t.TempDir()

	// Create both repos
	apiRepo := filepath.Join(tmpDir, "api")
	webRepo := filepath.Join(tmpDir, "web")
	for _, repo := range []string{apiRepo, webRepo} {
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
	}

	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "API-1", Title: "API", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})
	createTestBeadsFile(t, webRepo, []model.Issue{
		{ID: "WEB-1", Title: "Web", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	disabled := false
	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: "api", Prefix: "api-"},
			{Path: "web", Prefix: "web-", Enabled: &disabled},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())

	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	// Should only have 1 result (disabled repo excluded)
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}

	// Should only have 1 issue
	if len(issues) != 1 {
		t.Errorf("len(issues) = %d, want 1", len(issues))
	}

	if issues[0].ID != "api-API-1" {
		t.Errorf("issues[0].ID = %q, want %q", issues[0].ID, "api-API-1")
	}
}

func TestAggregateLoaderEmptyConfig(t *testing.T) {
	config := &workspace.Config{
		Repos: []workspace.RepoConfig{},
	}

	loader := workspace.NewAggregateLoader(config, "/tmp")
	_, _, err := loader.LoadAll(context.Background())

	if err == nil {
		t.Error("LoadAll() should error on empty config")
	}
}

func TestAggregateLoaderNilConfig(t *testing.T) {
	loader := workspace.NewAggregateLoader(nil, "/tmp")
	_, _, err := loader.LoadAll(context.Background())

	if err == nil {
		t.Error("LoadAll() should error on nil config")
	}
}

func TestAggregateLoaderContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a repo
	apiRepo := filepath.Join(tmpDir, "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "API-1", Title: "API", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: "api", Prefix: "api-"},
		},
	}

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loader := workspace.NewAggregateLoader(config, tmpDir)
	_, results, _ := loader.LoadAll(ctx)

	// Results should have context error
	if len(results) > 0 && results[0].Error == nil {
		// Note: Due to the race between cancellation and loading,
		// the result may or may not have an error. This test just
		// ensures we don't panic on cancellation.
	}
}

func TestSummarize(t *testing.T) {
	results := []workspace.LoadResult{
		{RepoName: "api", Issues: make([]model.Issue, 5)},
		{RepoName: "web", Issues: make([]model.Issue, 3)},
		{RepoName: "broken", Error: os.ErrNotExist},
	}

	summary := workspace.Summarize(results)

	if summary.TotalRepos != 3 {
		t.Errorf("TotalRepos = %d, want 3", summary.TotalRepos)
	}
	if summary.SuccessfulRepos != 2 {
		t.Errorf("SuccessfulRepos = %d, want 2", summary.SuccessfulRepos)
	}
	if summary.FailedRepos != 1 {
		t.Errorf("FailedRepos = %d, want 1", summary.FailedRepos)
	}
	if summary.TotalIssues != 8 {
		t.Errorf("TotalIssues = %d, want 8", summary.TotalIssues)
	}
	if len(summary.FailedRepoNames) != 1 || summary.FailedRepoNames[0] != "broken" {
		t.Errorf("FailedRepoNames = %v, want [broken]", summary.FailedRepoNames)
	}
}

func TestLoadAllFromConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .bv directory and config
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create api repo
	apiRepo := filepath.Join(tmpDir, "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "API-1", Title: "API", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	// Write workspace config
	configPath := filepath.Join(bvDir, "workspace.yaml")
	configContent := `
repos:
  - path: api
    prefix: api-
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	issues, results, err := workspace.LoadAllFromConfig(context.Background(), configPath)
	if err != nil {
		t.Fatalf("LoadAllFromConfig() error = %v", err)
	}

	if len(issues) != 1 {
		t.Errorf("len(issues) = %d, want 1", len(issues))
	}

	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}

	if issues[0].ID != "api-API-1" {
		t.Errorf("issues[0].ID = %q, want %q", issues[0].ID, "api-API-1")
	}
}

func TestLoadAllFromConfigFollowsRepositoryTrackerRedirect(t *testing.T) {
	workspaceRoot := t.TempDir()
	configDir := filepath.Join(workspaceRoot, ".bv")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	repoDir := filepath.Join(workspaceRoot, "api")
	localBeads := filepath.Join(repoDir, ".beads")
	terminalBeads := filepath.Join(workspaceRoot, "tracker", ".beads")
	if err := os.MkdirAll(localBeads, 0o755); err != nil {
		t.Fatalf("mkdir local beads: %v", err)
	}
	if err := os.MkdirAll(terminalBeads, 0o755); err != nil {
		t.Fatalf("mkdir terminal beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localBeads, "redirect"), []byte("../../tracker/.beads\n"), 0o644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(localBeads, "issues.jsonl"),
		[]byte(`{"id":"STALE-LOCAL","title":"Stale local copy","status":"open","issue_type":"task"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write stale local issues: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(terminalBeads, "issues.jsonl"),
		[]byte(`{"id":"TERMINAL-1","title":"Terminal tracker","status":"open","issue_type":"task"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write terminal issues: %v", err)
	}
	configPath := filepath.Join(configDir, "workspace.yaml")
	if err := os.WriteFile(configPath, []byte("repos:\n  - path: api\n    prefix: api-\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	issues, results, err := workspace.LoadAllFromConfig(context.Background(), configPath)
	if err != nil {
		t.Fatalf("LoadAllFromConfig: %v", err)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("workspace results = %+v, want one successful redirected repo", results)
	}
	if len(issues) != 1 || issues[0].ID != "api-TERMINAL-1" {
		t.Fatalf("workspace issues = %+v, want terminal redirect authority only", issues)
	}
}

func TestLoadAllFromRelativeConfigRefreshesCorrectBDRepoWithSanitizedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake bd fixture uses POSIX sh")
	}
	t.Setenv("BV_ROBOT", "1")
	workspaceRoot := t.TempDir()
	configDir := filepath.Join(workspaceRoot, ".bv")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "workspace.yaml")
	if err := os.WriteFile(configPath, []byte("repos:\n  - name: api\n    path: api\n    prefix: api-\n"), 0o644); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	targetRepo := filepath.Join(workspaceRoot, "api")
	targetBeads := filepath.Join(targetRepo, ".beads")
	if err := os.MkdirAll(filepath.Join(targetBeads, "embeddeddolt"), 0o755); err != nil {
		t.Fatalf("create target bd workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetBeads, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("write target metadata: %v", err)
	}
	targetIssues := filepath.Join(targetBeads, "issues.jsonl")
	targetFixture := `{"id":"TARGET-STALE","title":"Target stale export","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(targetIssues, []byte(targetFixture), 0o644); err != nil {
		t.Fatalf("write target compatibility export: %v", err)
	}

	hostileBeads := filepath.Join(workspaceRoot, "hostile", ".beads")
	if err := os.MkdirAll(hostileBeads, 0o755); err != nil {
		t.Fatalf("create hostile beads directory: %v", err)
	}
	hostileDB := filepath.Join(hostileBeads, "issues.jsonl")
	hostileFixture := `{"id":"HOSTILE-1","title":"Wrong ambient source","status":"open","issue_type":"task"}` + "\n"
	if err := os.WriteFile(hostileDB, []byte(hostileFixture), 0o644); err != nil {
		t.Fatalf("write hostile source: %v", err)
	}
	t.Setenv("BEADS_DIR", hostileBeads)
	t.Setenv("BEADS_DB", hostileDB)

	binDir := filepath.Join(workspaceRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create fake bd directory: %v", err)
	}
	observationPath := filepath.Join(workspaceRoot, "bd-observation.txt")
	script := "#!/bin/sh\n" +
		"{\n" +
		"  printf 'command=%s %s\\n' \"$1\" \"$2\"\n" +
		"  printf 'pwd=%s\\n' \"$(pwd -P)\"\n" +
		"  printf 'beads_dir=%s\\n' \"${BEADS_DIR-unset}\"\n" +
		"  printf 'beads_db=%s\\n' \"${BEADS_DB-unset}\"\n" +
		"  printf 'output=%s\\n' \"$3\"\n" +
		"} > '" + observationPath + "'\n" +
		"printf 'workspace-intentional-refresh-failure\\n' >&2\n" +
		"exit 23\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workspaceRoot); err != nil {
		t.Fatalf("change to workspace root: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	var issues []model.Issue
	var results []workspace.LoadResult
	var loadErr error
	stderr := captureWorkspaceStderr(t, func() {
		issues, results, loadErr = workspace.LoadAllFromConfig(context.Background(), filepath.Join(".bv", "workspace.yaml"))
	})
	if loadErr != nil {
		t.Fatalf("LoadAllFromConfig() error = %v", loadErr)
	}
	if stderr != "" {
		t.Fatalf("robot workspace load wrote stderr: %q", stderr)
	}
	if len(issues) != 1 || issues[0].ID != "api-TARGET-STALE" {
		t.Fatalf("workspace loaded wrong authority: %+v", issues)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("workspace results = %+v, want one usable stale-export result", results)
	}
	if len(results[0].AuthorityWarnings) != 1 || !strings.Contains(results[0].AuthorityWarnings[0], "workspace-intentional-refresh-failure") {
		t.Fatalf("workspace authority warnings = %v, want surfaced bd fallback", results[0].AuthorityWarnings)
	}

	absTargetBeads, err := filepath.Abs(filepath.Join("api", ".beads"))
	if err != nil {
		t.Fatalf("resolve target beads path: %v", err)
	}
	absTargetIssues, err := filepath.Abs(filepath.Join("api", ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatalf("resolve target issues path: %v", err)
	}
	observation, err := os.ReadFile(observationPath)
	if err != nil {
		t.Fatalf("read fake bd observation: %v", err)
	}
	for _, want := range []string{
		"command=export -o",
		"pwd=" + filepath.Dir(absTargetBeads),
		"beads_dir=" + absTargetBeads,
		"beads_db=unset",
		"output=" + absTargetIssues,
	} {
		if !strings.Contains(string(observation), want+"\n") {
			t.Fatalf("bd observation %q missing exact line %q", observation, want)
		}
	}
}

func TestLoadAllFromConfigWithDiscovery(t *testing.T) {
	tmpDir := t.TempDir()

	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	apiRepo := filepath.Join(tmpDir, "services", "api")
	sharedRepo := filepath.Join(tmpDir, "packages", "shared")
	ignoredRepo := filepath.Join(tmpDir, "node_modules", "ignored")
	createTestBeadsFileAt(t, filepath.Join(apiRepo, "tracker"), []model.Issue{
		{
			ID:       "API-1",
			Title:    "API needs shared utility",
			Status:   model.StatusOpen,
			Priority: 1,
			Dependencies: []*model.Dependency{
				{
					IssueID:     "API-1",
					DependsOnID: "shared-UTIL-1",
					Type:        model.DepBlocks,
				},
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	})
	createTestBeadsFileAt(t, filepath.Join(sharedRepo, "tracker"), []model.Issue{
		{ID: "UTIL-1", Title: "Shared utility", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})
	createTestBeadsFileAt(t, filepath.Join(ignoredRepo, "tracker"), []model.Issue{
		{ID: "IGN-1", Title: "Ignored dependency", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	configPath := filepath.Join(bvDir, "workspace.yaml")
	configContent := `
discovery:
  enabled: true
  patterns:
    - "services/*"
    - "packages/*"
    - "node_modules/*"
  exclude:
    - node_modules
  max_depth: 2
defaults:
  beads_path: tracker
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	issues, results, err := workspace.LoadAllFromConfig(context.Background(), configPath)
	if err != nil {
		t.Fatalf("LoadAllFromConfig() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}

	byID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	if _, ok := byID["api-API-1"]; !ok {
		t.Fatal("expected discovered issue api-API-1")
	}
	if _, ok := byID["shared-UTIL-1"]; !ok {
		t.Fatal("expected discovered issue shared-UTIL-1")
	}
	if _, ok := byID["ignored-IGN-1"]; ok {
		t.Fatal("excluded node_modules repo should not be loaded")
	}

	apiIssue := byID["api-API-1"]
	if len(apiIssue.Dependencies) != 1 {
		t.Fatalf("len(apiIssue.Dependencies) = %d, want 1", len(apiIssue.Dependencies))
	}
	requireWorkspaceLoaderString(t, "cross-repo dependency", apiIssue.Dependencies[0].DependsOnID, "shared-UTIL-1")
	requireWorkspaceLoaderString(t, "api SourceRepo", apiIssue.SourceRepo, "api")
	requireWorkspaceLoaderString(t, "shared SourceRepo", byID["shared-UTIL-1"].SourceRepo, "shared")
}

func TestAggregateLoaderDiscoveryFollowsTrackerRedirect(t *testing.T) {
	workspaceRoot := t.TempDir()
	repoBeads := filepath.Join(workspaceRoot, "services", "api", ".beads")
	terminalBeads := filepath.Join(workspaceRoot, "tracker", ".beads")
	if err := os.MkdirAll(repoBeads, 0o755); err != nil {
		t.Fatalf("create discovered redirect stub: %v", err)
	}
	if err := os.MkdirAll(terminalBeads, 0o755); err != nil {
		t.Fatalf("create terminal tracker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoBeads, "redirect"), []byte("../../../tracker/.beads\n"), 0o644); err != nil {
		t.Fatalf("write discovered tracker redirect: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(terminalBeads, "issues.jsonl"),
		[]byte(`{"id":"REDIRECTED-1","title":"Redirected","status":"open","issue_type":"task"}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write terminal issue source: %v", err)
	}

	config := &workspace.Config{Discovery: workspace.DiscoveryConfig{
		Enabled:  true,
		Patterns: []string{"services/*"},
		MaxDepth: 2,
	}}
	issues, results, err := workspace.NewAggregateLoader(config, workspaceRoot).LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() through discovered redirect: %v", err)
	}
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("discovered redirect results = %+v, want one successful repository", results)
	}
	if len(issues) != 1 || issues[0].ID != "api-REDIRECTED-1" {
		t.Fatalf("discovered redirect issues = %+v, want terminal tracker issue", issues)
	}
}

func TestAggregateLoaderDiscoveryRespectsDisabledExplicitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	apiRepo := filepath.Join(tmpDir, "services", "api")
	webRepo := filepath.Join(tmpDir, "services", "web")
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "API-1", Title: "Disabled API", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})
	createTestBeadsFile(t, webRepo, []model.Issue{
		{ID: "WEB-1", Title: "Discovered web", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	disabled := false
	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: "services/api", Prefix: "api-", Enabled: &disabled},
		},
		Discovery: workspace.DiscoveryConfig{
			Enabled:  true,
			Patterns: []string{"services/*"},
			MaxDepth: 2,
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, results, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if len(issues) != 1 {
		t.Fatalf("len(issues) = %d, want 1", len(issues))
	}
	requireWorkspaceLoaderString(t, "loaded issue ID", issues[0].ID, "web-WEB-1")
}

func TestLoadAllFromConfigMissing(t *testing.T) {
	_, _, err := workspace.LoadAllFromConfig(context.Background(), "/nonexistent/workspace.yaml")
	if err == nil {
		t.Error("LoadAllFromConfig() should error on missing config")
	}
}

func TestAggregateLoaderAbsolutePaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Create repo with absolute path
	apiRepo := filepath.Join(tmpDir, "api")
	if err := os.MkdirAll(apiRepo, 0755); err != nil {
		t.Fatal(err)
	}
	createTestBeadsFile(t, apiRepo, []model.Issue{
		{ID: "API-1", Title: "API", Status: model.StatusOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	})

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Path: apiRepo, Prefix: "api-"}, // Absolute path
		},
	}

	loader := workspace.NewAggregateLoader(config, "/different/root")
	issues, _, err := loader.LoadAll(context.Background())

	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(issues) != 1 {
		t.Errorf("len(issues) = %d, want 1", len(issues))
	}
}

func TestAggregateLoaderCustomBeadsPath(t *testing.T) {
	tmpDir := t.TempDir()

	// Repo with custom beads path
	repoDir := filepath.Join(tmpDir, "svc")
	customBeads := filepath.Join(repoDir, "custom_beads")
	createTestBeadsFileAt(t, customBeads, []model.Issue{
		{ID: "CUST-1", Title: "Custom beads", Status: model.StatusOpen, IssueType: model.TypeTask},
	})

	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "svc", Path: "svc", Prefix: "svc-", BeadsPath: "custom_beads"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, _, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "svc-CUST-1" {
		t.Errorf("expected namespaced ID svc-CUST-1, got %s", issues[0].ID)
	}
}

func TestAggregateLoaderDefaultsBeadsPath(t *testing.T) {
	tmpDir := t.TempDir()

	repoDir := filepath.Join(tmpDir, "svc")
	createTestBeadsFileAt(t, filepath.Join(repoDir, "tracker"), []model.Issue{
		{ID: "CUST-1", Title: "Default beads path", Status: model.StatusOpen, IssueType: model.TypeTask},
	})

	config := &workspace.Config{
		Defaults: workspace.RepoDefaults{BeadsPath: "tracker"},
		Repos: []workspace.RepoConfig{
			{Name: "svc", Path: "svc", Prefix: "svc-"},
		},
	}

	loader := workspace.NewAggregateLoader(config, tmpDir)
	issues, _, err := loader.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll() error = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	requireWorkspaceLoaderString(t, "namespaced ID", issues[0].ID, "svc-CUST-1")
}
