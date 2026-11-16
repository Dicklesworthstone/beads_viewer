package export

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestParseGHUsername(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "standard output",
			output:   "  Logged in to github.com account testuser (oauth_token)",
			expected: "testuser",
		},
		{
			name:     "with parenthetical",
			output:   "github.com\n  Logged in to github.com account myuser (keyring)",
			expected: "myuser",
		},
		{
			name:     "no account info",
			output:   "Not logged in to github.com",
			expected: "",
		},
		{
			name:     "empty output",
			output:   "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseGHUsername(tc.output)
			if result != tc.expected {
				t.Errorf("parseGHUsername(%q) = %q, want %q", tc.output, result, tc.expected)
			}
		})
	}
}

func TestSuggestRepoName(t *testing.T) {
	tests := []struct {
		bundlePath string
		expected   string
	}{
		{"/home/user/myproject/bv-pages", "myproject-pages"},
		{"/home/user/awesome-app/docs", "awesome-app-pages"},
		{"/home/user/My Project", "my-project"},
		{"./output", "output"},
	}

	for _, tc := range tests {
		t.Run(tc.bundlePath, func(t *testing.T) {
			result := SuggestRepoName(tc.bundlePath)
			if result != tc.expected {
				t.Errorf("SuggestRepoName(%q) = %q, want %q", tc.bundlePath, result, tc.expected)
			}
		})
	}

	t.Run("cwd dot and beads paths", func(t *testing.T) {
		tmpParent := t.TempDir()
		repoDir := filepath.Join(tmpParent, "acme_repo")
		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		t.Chdir(repoDir)

		if got := SuggestRepoName(""); got != "acme_repo-pages" {
			t.Fatalf("SuggestRepoName(\"\") = %q, want %q", got, "acme_repo-pages")
		}
		if got := SuggestRepoName("."); got != "acme_repo" {
			t.Fatalf("SuggestRepoName(\".\") = %q, want %q", got, "acme_repo")
		}
		if got := SuggestRepoName(beadsDir); got != "acme_repo-pages" {
			t.Fatalf("SuggestRepoName(.beads) = %q, want %q", got, "acme_repo-pages")
		}
		if got := SuggestRepoName(filepath.Join(beadsDir, "issues.jsonl")); got != "acme_repo-pages" {
			t.Fatalf("SuggestRepoName(.beads/issues.jsonl) = %q, want %q", got, "acme_repo-pages")
		}
	})
}

func TestGitHubDeployConfig(t *testing.T) {
	config := GitHubDeployConfig{
		RepoName:         "test-repo",
		Private:          true,
		Description:      "Test description",
		BundlePath:       "/tmp/test",
		SkipConfirmation: true,
		ForceOverwrite:   false,
	}

	if config.RepoName != "test-repo" {
		t.Errorf("Expected RepoName 'test-repo', got %s", config.RepoName)
	}

	if !config.Private {
		t.Error("Expected Private to be true")
	}

	if config.ForceOverwrite {
		t.Error("Expected ForceOverwrite to be false")
	}
}

func TestGitHubDeployResult(t *testing.T) {
	result := GitHubDeployResult{
		RepoFullName: "user/repo",
		PagesURL:     "https://user.github.io/repo/",
		GitRemote:    "https://github.com/user/repo.git",
	}

	if result.RepoFullName != "user/repo" {
		t.Errorf("Expected RepoFullName 'user/repo', got %s", result.RepoFullName)
	}

	if result.PagesURL != "https://user.github.io/repo/" {
		t.Errorf("Expected PagesURL 'https://user.github.io/repo/', got %s", result.PagesURL)
	}
}

func TestGitHubStatus(t *testing.T) {
	status := GitHubStatus{
		Installed:     true,
		Authenticated: true,
		Username:      "testuser",
		GitConfigured: true,
		GitName:       "Test User",
		GitEmail:      "test@example.com",
	}

	if !status.Installed {
		t.Error("Expected Installed to be true")
	}

	if !status.Authenticated {
		t.Error("Expected Authenticated to be true")
	}

	if status.Username != "testuser" {
		t.Errorf("Expected Username 'testuser', got %s", status.Username)
	}

	if !status.GitConfigured {
		t.Error("Expected GitConfigured to be true")
	}
}

func TestGitHubPagesStatus(t *testing.T) {
	status := GitHubPagesStatus{
		Enabled:   true,
		URL:       "https://user.github.io/repo/",
		Branch:    "main",
		Path:      "/",
		BuildType: "legacy",
	}

	if !status.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if status.URL != "https://user.github.io/repo/" {
		t.Errorf("Expected URL 'https://user.github.io/repo/', got %s", status.URL)
	}

	if status.Branch != "main" {
		t.Errorf("Expected Branch 'main', got %s", status.Branch)
	}
}

func TestCheckGHStatus_Integration(t *testing.T) {
	// This test checks if the function runs without panic
	// Actual values depend on the environment
	status, err := CheckGHStatus()
	if err != nil {
		t.Errorf("CheckGHStatus() returned error: %v", err)
	}

	if status == nil {
		t.Fatal("CheckGHStatus() returned nil status")
	}

	// Log the status for debugging (won't fail test)
	t.Logf("gh CLI installed: %v", status.Installed)
	t.Logf("gh authenticated: %v", status.Authenticated)
	t.Logf("git configured: %v", status.GitConfigured)
}

func TestInitAndPush_MissingBundlePath(t *testing.T) {
	// Test with non-existent path should fail gracefully
	err := InitAndPush("/nonexistent/path/12345", "user/repo", false)
	if err == nil {
		t.Error("Expected error for non-existent bundle path")
	}
}

func TestDeployToGitHubPages_MissingBundlePath(t *testing.T) {
	config := GitHubDeployConfig{
		RepoName:         "test-repo",
		BundlePath:       "/nonexistent/path/12345",
		SkipConfirmation: true,
	}

	_, err := DeployToGitHubPages(config)
	if err == nil {
		t.Error("Expected error for non-existent bundle path")
	}
}

func TestRepoExists_InvalidRepo(t *testing.T) {
	// This will return false for a non-existent repo
	exists := RepoExists("definitely-not-a-real-repo-12345-xyzzy")
	if exists {
		t.Error("Expected RepoExists to return false for non-existent repo")
	}
}

func TestGetRepoFullName_WithOwner(t *testing.T) {
	// If name already contains owner, should return as-is
	name, err := getRepoFullName("owner/repo")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if name != "owner/repo" {
		t.Errorf("Expected 'owner/repo', got %s", name)
	}
}

func TestShowInstallInstructions(t *testing.T) {
	// Just verify it doesn't panic
	// Capture stdout is complex, so we just ensure no crash
	ShowInstallInstructions()
}

func TestSuggestRepoName_CurrentDir(t *testing.T) {
	// Create a temp directory to test with
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "test-project", "bv-pages")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	result := SuggestRepoName(subDir)
	// Should use parent project name + -pages
	if result != "test-project-pages" {
		t.Errorf("Expected 'test-project-pages', got %s", result)
	}
}

func TestSuggestRepoName_RegularDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "my-static-site")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	result := SuggestRepoName(subDir)
	if result != "my-static-site" {
		t.Errorf("Expected 'my-static-site', got %s", result)
	}
}

func TestWriteGitHubActionsWorkflow(t *testing.T) {
	tmpDir := t.TempDir()

	// Write the workflow
	if err := WriteGitHubActionsWorkflow(tmpDir); err != nil {
		t.Fatalf("WriteGitHubActionsWorkflow failed: %v", err)
	}

	// Check that the workflow file exists
	workflowPath := filepath.Join(tmpDir, ".github", "workflows", "static.yml")
	if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
		t.Errorf("Workflow file was not created at %s", workflowPath)
	}

	// Read and verify content
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("Failed to read workflow file: %v", err)
	}

	// Verify key workflow elements
	contentStr := string(content)
	checks := []string{
		"name: Deploy static content to Pages",
		"push:",
		"branches: [\"main\"]",
		"workflow_dispatch:",
		// Actions must be pinned to full commit SHAs (supply-chain hardening);
		// a mutable tag could be moved to steal the Pages OIDC token.
		"actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4",
		"actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b # v5",
		"actions/upload-pages-artifact@56afc609e74202658d3ffba0e8f6dda462b719fa # v3",
		"actions/deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e # v4",
	}

	for _, check := range checks {
		if !strings.Contains(contentStr, check) {
			t.Errorf("Workflow missing expected content: %s", check)
		}
	}
}

// TestGitHubActionsWorkflowPinsEveryAction: every action in the generated
// Pages workflow, not just the four listed above, is pinned to a full commit
// SHA, so a newly added step cannot reintroduce a mutable tag (#197).
func TestGitHubActionsWorkflowPinsEveryAction(t *testing.T) {
	uses := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*["']?([^\s"'#]+)`).FindAllStringSubmatch(GitHubActionsWorkflowContent, -1)
	if len(uses) == 0 {
		t.Fatal("generated workflow has no `uses:` lines")
	}
	pinned := regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	for _, match := range uses {
		if !pinned.MatchString(match[1]) {
			t.Errorf("generated workflow action %q is not pinned to a full commit SHA", match[1])
		}
	}
}

func TestGitHubActionsWorkflowContent(t *testing.T) {
	// Verify the workflow content is valid YAML-like structure
	content := GitHubActionsWorkflowContent

	// Check required fields
	requiredFields := []string{
		"name:",
		"on:",
		"permissions:",
		"jobs:",
		"deploy:",
		"steps:",
	}

	for _, field := range requiredFields {
		if !strings.Contains(content, field) {
			t.Errorf("GitHubActionsWorkflowContent missing required field: %s", field)
		}
	}
}

func TestDeleteRepository_NoConfirm(t *testing.T) {
	err := DeleteRepository("user/repo", false)
	if err == nil {
		t.Error("Expected error when confirm is false")
	}
	if !strings.Contains(err.Error(), "requires confirmation") {
		t.Errorf("Expected confirmation error, got: %v", err)
	}
}

func TestOpenInBrowser_TestMode(t *testing.T) {
	// BV_TEST_MODE is set in main_test.go TestMain, so this should be a no-op
	t.Setenv("BV_NO_BROWSER", "1")
	err := OpenInBrowser("https://example.com")
	if err != nil {
		t.Errorf("Expected no error in test mode, got: %v", err)
	}
}

func TestParseGHUsername_MultipleLines(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "gh status full output",
			output:   "github.com\n  ✓ Logged in to github.com account jdoe (GITHUB_TOKEN)\n  ✓ Git operations for github.com configured to use https\n  ✓ Token: gho_xxxx\n  ✓ Token scopes: admin:org, codespace",
			expected: "jdoe",
		},
		{
			name:     "account with no space after name",
			output:   "  Logged in to github.com account alice(keyring)",
			expected: "alice",
		},
		{
			name:     "no logged-in line at all",
			output:   "github.com\n  Token: none\n  Git: configured",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseGHUsername(tc.output)
			if result != tc.expected {
				t.Errorf("parseGHUsername(%q) = %q, want %q", tc.output, result, tc.expected)
			}
		})
	}
}

func TestGitHubActionsStatus_Struct(t *testing.T) {
	// Test the struct can be used correctly
	status := &GitHubActionsStatus{
		WorkflowRunning:     true,
		WorkflowQueued:      false,
		LastRunStatus:       "in_progress",
		LastRunCreatedAt:    "2026-02-09T18:00:00Z",
		PossiblyRateLimited: false,
	}

	if !status.WorkflowRunning {
		t.Error("Expected WorkflowRunning to be true")
	}
	if status.WorkflowQueued {
		t.Error("Expected WorkflowQueued to be false")
	}
	if status.PossiblyRateLimited {
		t.Error("Expected PossiblyRateLimited to be false")
	}
	if status.LastRunStatus != "in_progress" {
		t.Errorf("Expected LastRunStatus 'in_progress', got %s", status.LastRunStatus)
	}

	// Test rate-limited scenario
	rateLimited := &GitHubActionsStatus{
		WorkflowQueued:      true,
		PossiblyRateLimited: true,
		LastRunStatus:       "queued",
	}

	if !rateLimited.PossiblyRateLimited {
		t.Error("Expected PossiblyRateLimited to be true")
	}
}

func TestListUserRepos_DefaultLimit(t *testing.T) {
	// ListUserRepos normalizes limit <= 0 to 30
	// We can't test the actual API call, but we can verify the
	// function handles empty output from gh CLI gracefully
	// by checking the error type rather than the result.
	_, err := ListUserRepos(0)
	// The error should be about gh execution, not about limit handling
	if err != nil && strings.Contains(err.Error(), "limit") {
		t.Errorf("Limit normalization failed: %v", err)
	}
}

func TestSuggestRepoName_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "spaces in name",
			path:     "/home/user/My Cool Project",
			expected: "my-cool-project",
		},
		{
			name:     "mixed case",
			path:     "/home/user/CamelCaseProject",
			expected: "camelcaseproject",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := SuggestRepoName(tc.path)
			if result != tc.expected {
				t.Errorf("SuggestRepoName(%q) = %q, want %q", tc.path, result, tc.expected)
			}
		})
	}
}

func TestVerifyGitHubPagesDeployment_IssueCountMismatchReturnsError(t *testing.T) {
	t.Setenv("PATH", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 1}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	err := VerifyGitHubPagesDeployment(server.URL, 2, time.Second)
	if err == nil {
		t.Fatal("Expected VerifyGitHubPagesDeployment to fail on stale issue count")
	}
	if !strings.Contains(err.Error(), "issue count mismatch") {
		t.Fatalf("Unexpected verification error: %v", err)
	}
}

func TestVerifyGitHubPagesDeployment_ZeroIssueCountMismatchReturnsError(t *testing.T) {
	t.Setenv("PATH", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 1}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	err := VerifyGitHubPagesDeployment(server.URL, 0, time.Second)
	if err == nil {
		t.Fatal("Expected VerifyGitHubPagesDeployment to fail when empty export verifies against stale live data")
	}
	if !strings.Contains(err.Error(), "issue count mismatch") {
		t.Fatalf("Unexpected verification error: %v", err)
	}
}

func TestVerifyGitHubPagesDeployment_Success(t *testing.T) {
	t.Setenv("PATH", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 2}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := VerifyGitHubPagesDeployment(server.URL, 2, time.Second); err != nil {
		t.Fatalf("VerifyGitHubPagesDeployment returned error: %v", err)
	}
}

func TestEnableGitHubPagesArgsAndPreviewServerWithConfig(t *testing.T) {
	args := enableGitHubPagesArgs("octo/demo-pages")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"api repos/octo/demo-pages/pages",
		"-X POST",
		"-f build_type=workflow",
		"-f source[branch]=main",
		"-f source[path]=/",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("enableGitHubPagesArgs missing %q in %v", want, args)
		}
	}

	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html><html><body>ok</body></html>"), 0o644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}
	cfg := DefaultPreviewConfig()
	cfg.BundlePath = bundleDir
	cfg.Port = 0
	cfg.OpenBrowser = false
	cfg.Quiet = true

	srv := NewPreviewServerWithConfig(cfg)
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()
	t.Cleanup(func() {
		_ = srv.Stop()
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
		}
	})

	var statusOK bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Port() > 0 {
			resp, err := http.Get(srv.URL() + LiveReloadScriptPath)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					statusOK = true
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !statusOK {
		t.Fatalf("NewPreviewServerWithConfig(DefaultPreviewConfig()) did not expose live-reload script endpoint")
	}
}

func TestWriteGitHubActionsWorkflow_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()

	// Write twice - should not error
	if err := WriteGitHubActionsWorkflow(tmpDir); err != nil {
		t.Fatalf("First write failed: %v", err)
	}
	if err := WriteGitHubActionsWorkflow(tmpDir); err != nil {
		t.Fatalf("Second write (idempotent) failed: %v", err)
	}

	// Content should be valid
	content, err := os.ReadFile(filepath.Join(tmpDir, ".github", "workflows", "static.yml"))
	if err != nil {
		t.Fatalf("Failed to read workflow: %v", err)
	}
	if !strings.Contains(string(content), "deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e") {
		t.Error("Workflow content missing expected action")
	}
}

func TestSuggestRepoName_DotGitAndSanitization(t *testing.T) {
	if got := SuggestRepoName("/tmp/acme-service.git/.beads"); got != "acme-service-pages" {
		t.Fatalf("SuggestRepoName(.git/.beads) = %q, want %q", got, "acme-service-pages")
	}
	if got := SuggestRepoName("/tmp/My Repo!@#.GIT/bv-pages"); got != "my-repo-pages" {
		t.Fatalf("SuggestRepoName(My Repo!@#.GIT/bv-pages) = %q, want %q", got, "my-repo-pages")
	}
	if got := SuggestRepoName("/tmp/custom-bundle.git"); got != "custom-bundle" {
		t.Fatalf("SuggestRepoName(custom-bundle.git) = %q, want %q", got, "custom-bundle")
	}
	if got := SuggestRepoName("!!!.git"); got != "beads-viewer-pages" {
		t.Fatalf("SuggestRepoName(!!!.git) = %q, want %q", got, "beads-viewer-pages")
	}
}

func TestSwitchToLegacyDeploymentArgsAndGitFallbackAuthor(t *testing.T) {
	args := switchToLegacyDeploymentArgs("octo/demo-pages")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"api -X PUT repos/octo/demo-pages/pages",
		"-f build_type=legacy",
		"-f source[branch]=gh-pages",
		"-f source[path]=/",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("switchToLegacyDeploymentArgs missing %q in %v", want, args)
		}
	}

	// Isolate git config so neither system nor user identity exists.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_CONFIG_HOME", emptyHome)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(emptyHome, "nonexistent-gitconfig"))

	repoDir := t.TempDir()
	initCmd := exec.Command("git", "init")
	initCmd.Dir = repoDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, string(out))
	}
	if err := os.WriteFile(filepath.Join(repoDir, "index.html"), []byte("<h1>Pages</h1>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repoDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, string(out))
	}

	commitArgs := gitCommitArgsForDir(repoDir, []string{"commit", "-m", "Update bv pages"})
	if gotFirst := firstArg(commitArgs); gotFirst != "commit" {
		t.Fatalf("firstArg(%v) = %q, want %q", commitArgs, gotFirst, "commit")
	}
	commitJoined := strings.Join(commitArgs, " ")
	for _, want := range []string{"-c user.name=bv-pages", "-c user.email=bv-pages@users.noreply.github.com"} {
		if !strings.Contains(commitJoined, want) {
			t.Fatalf("gitCommitArgsForDir missing fallback %q in %v", want, commitArgs)
		}
	}

	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Dir = repoDir
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit with fallback author failed: %v (%s)", err, string(out))
	}

	logCmd := exec.Command("git", "log", "-1", "--format=%an <%ae>")
	logCmd.Dir = repoDir
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v (%s)", err, string(logOut))
	}
	if got := strings.TrimSpace(string(logOut)); got != "bv-pages <bv-pages@users.noreply.github.com>" {
		t.Fatalf("commit author = %q, want %q", got, "bv-pages <bv-pages@users.noreply.github.com>")
	}
}

func TestShouldFallbackToLegacyPages(t *testing.T) {
	cases := []struct {
		name   string
		status *GitHubActionsStatus
		want   bool
	}{
		{"nil status", nil, false},
		{"zero runs and inactive", &GitHubActionsStatus{TotalCount: 0, HasActiveRun: false}, true},
		{"failed run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: false, LastRunStatus: "completed", LastConclusion: "failure"}, true},
		{"cancelled run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: false, LastRunStatus: "completed", LastConclusion: "cancelled"}, true},
		{"startup_failure run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: false, LastRunStatus: "completed", LastConclusion: "startup_failure"}, true},
		{"rate limited queued run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: true, WorkflowQueued: true, PossiblyRateLimited: true}, true},
		{"active in_progress run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: true, WorkflowRunning: true, LastRunStatus: "in_progress"}, false},
		{"fresh queued run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: true, WorkflowQueued: true, LastRunStatus: "queued"}, false},
		{"completed success run", &GitHubActionsStatus{TotalCount: 1, HasActiveRun: false, LastRunStatus: "completed", LastConclusion: "success"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFallbackToLegacyPages(tc.status); got != tc.want {
				t.Fatalf("shouldFallbackToLegacyPages(%+v) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

func TestCheckGitHubActionsStatus_ParsesRunsAndTriggersFallback(t *testing.T) {
	binDir := t.TempDir()
	responseFile := filepath.Join(t.TempDir(), "runs.json")
	ghLog := filepath.Join(t.TempDir(), "gh.log")
	gitLog := filepath.Join(t.TempDir(), "git.log")

	ghScript := filepath.Join(binDir, "gh")
	ghBody := "#!/usr/bin/env bash\n" +
		"echo \"$*\" >> " + fmt.Sprintf("%q", ghLog) + "\n" +
		"if [[ \"$1\" == \"auth\" && \"$2\" == \"status\" ]]; then\n" +
		"  echo \"  Logged in to github.com account octo (oauth_token)\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$1\" == \"repo\" && \"$2\" == \"view\" ]]; then\n" +
		"  echo \"octo/demo-pages\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$1\" == \"api\" && \"$*\" == *\"/contents\"* ]]; then\n" +
		"  echo '[]'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$1\" == \"api\" && \"$*\" == *\"/actions/runs\"* ]]; then\n" +
		"  cat " + fmt.Sprintf("%q", responseFile) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$1\" == \"api\" && \"$*\" == *\"/pages\"* ]]; then\n" +
		"  echo '{\"html_url\":\"https://octo.github.io/demo-pages/\"}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo '{}'\n"
	if err := os.WriteFile(ghScript, []byte(ghBody), 0o755); err != nil {
		t.Fatalf("WriteFile gh stub: %v", err)
	}

	gitScript := filepath.Join(binDir, "git")
	gitBody := "#!/usr/bin/env bash\n" +
		"if [[ -n \"${GIT_DIR:-}\" || -n \"${GIT_WORK_TREE:-}\" ]]; then\n" +
		"  echo \"poisoned git env leaked: GIT_DIR=${GIT_DIR:-} GIT_WORK_TREE=${GIT_WORK_TREE:-}\" >&2\n" +
		"  exit 99\n" +
		"fi\n" +
		"echo \"$*\" >> " + fmt.Sprintf("%q", gitLog) + "\n" +
		"if [[ \"$*\" == *\"config user.name\"* || \"$*\" == *\"config --get user.name\"* ]]; then\n" +
		"  echo \"Octo\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$*\" == *\"config user.email\"* || \"$*\" == *\"config --get user.email\"* ]]; then\n" +
		"  echo \"octo@example.com\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [[ \"$1\" == \"remote\" && \"$2\" == \"get-url\" ]]; then\n" +
		"  exit 1\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(gitScript, []byte(gitBody), 0o755); err != nil {
		t.Fatalf("WriteFile git stub: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// Poison ambient GIT_DIR and GIT_WORK_TREE to prove SanitizedGitEnv strips them!
	t.Setenv("GIT_DIR", "/nonexistent/poison/.git")
	t.Setenv("GIT_WORK_TREE", "/nonexistent/poison")
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/replace/")

	origWait := githubActionsInitialWait
	githubActionsInitialWait = 0
	t.Cleanup(func() { githubActionsInitialWait = origWait })

	// 1. Failed workflow run -> CheckGitHubActionsStatus captures failure & triggers legacy fallback.
	if err := os.WriteFile(responseFile, []byte(`{"total_count":1,"workflow_runs":[{"status":"completed","conclusion":"failure","created_at":"2026-11-16T00:00:00Z"}]}`), 0o644); err != nil {
		t.Fatalf("WriteFile runs.json: %v", err)
	}
	st, err := CheckGitHubActionsStatus("octo/demo-pages")
	if err != nil {
		t.Fatalf("CheckGitHubActionsStatus error: %v", err)
	}
	if st.TotalCount != 1 || st.HasActiveRun || st.LastConclusion != "failure" {
		t.Fatalf("unexpected failed status: %+v", st)
	}
	if !shouldFallbackToLegacyPages(st) {
		t.Fatalf("expected legacy fallback on failed conclusion: %+v", st)
	}

	// 2. DeployToGitHubPagesWithFallback with zero runs triggers SwitchToLegacyDeployment + PushToGHPagesBranch
	// and never leaks poisoned GIT_DIR / GIT_WORK_TREE into git subprocesses.
	if err := os.WriteFile(responseFile, []byte(`{"total_count":0,"workflow_runs":[]}`), 0o644); err != nil {
		t.Fatalf("WriteFile zero runs.json: %v", err)
	}
	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}
	res, err := DeployToGitHubPagesWithFallback(GitHubDeployConfig{
		RepoName:         "demo-pages",
		BundlePath:       bundleDir,
		SkipConfirmation: true,
		ForceOverwrite:   true,
	}, skipDeploymentIssueCountVerification)
	if err != nil {
		t.Fatalf("DeployToGitHubPagesWithFallback failed: %v", err)
	}
	if res.RepoFullName != "octo/demo-pages" {
		t.Fatalf("RepoFullName = %q, want octo/demo-pages", res.RepoFullName)
	}

	ghLogged, _ := os.ReadFile(ghLog)
	if !strings.Contains(string(ghLogged), "build_type=legacy") {
		t.Fatalf("expected SwitchToLegacyDeployment (build_type=legacy) in gh log, got:\n%s", string(ghLogged))
	}
	gitLogged, _ := os.ReadFile(gitLog)
	if !strings.Contains(string(gitLogged), "HEAD:gh-pages") {
		t.Fatalf("expected PushToGHPagesBranch (HEAD:gh-pages) in git log, got:\n%s", string(gitLogged))
	}
}
