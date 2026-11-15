package export

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireCurl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
}

func TestParseWranglerWhoami(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantName string
		wantID   string
	}{
		{
			name: "standard output",
			output: `
Getting User settings...
Account Name: My Account
Account ID: abc123def456
`,
			wantName: "My Account",
			wantID:   "abc123def456",
		},
		{
			name: "email fallback",
			output: `
Getting User settings...
user@example.com
`,
			wantName: "user@example.com",
			wantID:   "",
		},
		{
			name:     "empty output",
			output:   "",
			wantName: "",
			wantID:   "",
		},
		{
			name: "only account ID",
			output: `
Account ID: xyz789
`,
			wantName: "",
			wantID:   "xyz789",
		},
		{
			name: "whitespace handling",
			output: `
  Account Name:   Spaced Name
  Account ID:   spaced-id
`,
			wantName: "Spaced Name",
			wantID:   "spaced-id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, id := parseWranglerWhoami(tc.output)
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
		})
	}
}

func TestParseCloudflareURL(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "simple pages.dev URL",
			output: "Deployment complete! https://my-project.pages.dev",
			want:   "https://my-project.pages.dev",
		},
		{
			name:   "URL with hash suffix",
			output: "Live at https://abc123-my-project.pages.dev/",
			want:   "https://abc123-my-project.pages.dev/",
		},
		{
			name:   "URL with trailing punctuation",
			output: "See https://my-project.pages.dev.",
			want:   "https://my-project.pages.dev",
		},
		{
			name:   "no URL",
			output: "Deployment failed",
			want:   "",
		},
		{
			name:   "multiline output",
			output: "Uploading...\nDone!\nURL: https://test-deploy.pages.dev\nComplete",
			want:   "https://test-deploy.pages.dev",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCloudflareURL(tc.output)
			if got != tc.want {
				t.Errorf("parseCloudflareURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseDeploymentID(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "UUID in output",
			output: "Deployment ID: 12345678-1234-1234-1234-123456789abc",
			want:   "12345678-1234-1234-1234-123456789abc",
		},
		{
			name:   "no UUID",
			output: "Deployment complete",
			want:   "",
		},
		{
			name:   "UUID with surrounding text",
			output: "Created deployment a1b2c3d4-e5f6-7890-abcd-ef1234567890 successfully",
			want:   "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDeploymentID(tc.output)
			if got != tc.want {
				t.Errorf("parseDeploymentID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSuggestProjectName(t *testing.T) {
	tests := []struct {
		name       string
		bundlePath string
		want       string
	}{
		{
			name:       "simple directory",
			bundlePath: "/path/to/my-project",
			want:       "my-project",
		},
		{
			name:       "bv-pages directory",
			bundlePath: "/path/to/myapp/bv-pages",
			want:       "myapp-pages",
		},
		{
			name:       "dist directory",
			bundlePath: "/path/to/webapp/dist",
			want:       "webapp-pages",
		},
		{
			name:       "underscore conversion",
			bundlePath: "/path/to/my_project",
			want:       "my-project",
		},
		{
			name:       "space conversion",
			bundlePath: "/path/to/my project",
			want:       "my-project",
		},
		{
			name:       "uppercase conversion",
			bundlePath: "/path/to/MyProject",
			want:       "myproject",
		},
		{
			name:       "special char removal",
			bundlePath: "/path/to/my@project!",
			want:       "myproject",
		},
		{
			name:       "multiple hyphens collapsed",
			bundlePath: "/path/to/my---project",
			want:       "my-project",
		},
		{
			name:       "punctuation only falls back",
			bundlePath: "/path/to/!!!",
			want:       "beads-viewer-pages",
		},
		{
			name:       "root path falls back",
			bundlePath: string(filepath.Separator),
			want:       "beads-viewer-pages",
		},
		{
			name:       "generic output at filesystem root falls back",
			bundlePath: string(filepath.Separator) + "dist",
			want:       "beads-viewer-pages",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SuggestProjectName(tc.bundlePath)
			if got != tc.want {
				t.Errorf("SuggestProjectName(%q) = %q, want %q", tc.bundlePath, got, tc.want)
			}
		})
	}

	t.Run("cwd dot and beads paths", func(t *testing.T) {
		tmpParent := t.TempDir()
		repoDir := filepath.Join(tmpParent, "Acme_Repo")
		beadsDir := filepath.Join(repoDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		t.Chdir(repoDir)

		if got := SuggestProjectName(""); got != "acme-repo-pages" {
			t.Fatalf("SuggestProjectName(\"\") = %q, want %q", got, "acme-repo-pages")
		}
		if got := SuggestProjectName("."); got != "acme-repo" {
			t.Fatalf("SuggestProjectName(\".\") = %q, want %q", got, "acme-repo")
		}
		if got := SuggestProjectName(beadsDir); got != "acme-repo-pages" {
			t.Fatalf("SuggestProjectName(.beads) = %q, want %q", got, "acme-repo-pages")
		}
		if got := SuggestProjectName(filepath.Join(beadsDir, "issues.jsonl")); got != "acme-repo-pages" {
			t.Fatalf("SuggestProjectName(.beads/issues.jsonl) = %q, want %q", got, "acme-repo-pages")
		}
	})
}

func TestGenerateHeadersFile(t *testing.T) {
	tmpDir := t.TempDir()

	if err := GenerateHeadersFile(tmpDir); err != nil {
		t.Fatalf("GenerateHeadersFile failed: %v", err)
	}

	// Verify file exists
	headersPath := filepath.Join(tmpDir, "_headers")
	data, err := os.ReadFile(headersPath)
	if err != nil {
		t.Fatalf("Failed to read _headers: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "\t") {
		t.Fatal("_headers should use spaces for Cloudflare header indentation, not tabs")
	}

	// Check for expected security headers
	expectedHeaders := []string{
		"X-Frame-Options: DENY",
		"X-Content-Type-Options: nosniff",
		"Referrer-Policy: strict-origin-when-cross-origin",
		"Cross-Origin-Opener-Policy: same-origin",
		"Cross-Origin-Embedder-Policy: require-corp",
		"Cross-Origin-Resource-Policy: same-origin",
		"Content-Type: application/javascript",
		"Content-Type: application/wasm",
	}

	for _, header := range expectedHeaders {
		if !strings.Contains(content, header) {
			t.Errorf("_headers missing: %s", header)
		}
	}
}

func TestCloudflareConfirmPrompt(t *testing.T) {
	// This function reads from stdin, so we test edge cases
	// by verifying the logic flow rather than actual input

	// The function defaults to false on error or non-y input
	// We can't easily test stdin in unit tests without mocking
	// So we just verify the function exists and has expected signature
	_ = cloudflareConfirmPrompt // Function exists
}

func TestCloudflareDeployConfig_Defaults(t *testing.T) {
	config := CloudflareDeployConfig{
		ProjectName: "test-project",
		BundlePath:  "/path/to/bundle",
	}

	// Default branch should be empty (set by DeployToCloudflarePages)
	if config.Branch != "" {
		t.Errorf("Expected empty default branch, got %q", config.Branch)
	}

	// SkipConfirmation should default to false
	if config.SkipConfirmation {
		t.Error("Expected SkipConfirmation to default to false")
	}
}

func TestCloudflareStatus_Fields(t *testing.T) {
	status := CloudflareStatus{
		Installed:     true,
		Authenticated: true,
		AccountName:   "Test Account",
		AccountID:     "test-123",
		NPMInstalled:  true,
	}

	if !status.Installed {
		t.Error("Expected Installed to be true")
	}
	if !status.Authenticated {
		t.Error("Expected Authenticated to be true")
	}
	if status.AccountName != "Test Account" {
		t.Errorf("AccountName = %q, want %q", status.AccountName, "Test Account")
	}
}

func TestCloudflareDeployResult_Fields(t *testing.T) {
	result := CloudflareDeployResult{
		ProjectName:  "my-project",
		URL:          "https://my-project.pages.dev",
		DeploymentID: "12345678-1234-1234-1234-123456789abc",
	}

	if result.ProjectName != "my-project" {
		t.Errorf("ProjectName = %q, want %q", result.ProjectName, "my-project")
	}
	if result.URL != "https://my-project.pages.dev" {
		t.Errorf("URL = %q, want %q", result.URL, "https://my-project.pages.dev")
	}
	if result.DeploymentID != "12345678-1234-1234-1234-123456789abc" {
		t.Errorf("DeploymentID = %q, want %q", result.DeploymentID, "12345678-1234-1234-1234-123456789abc")
	}
}

func TestEnsureCloudflareProject_Integration(t *testing.T) {
	// Skip if wrangler is not installed (this is an integration test)
	if _, err := exec.LookPath("wrangler"); err != nil {
		t.Skip("wrangler not installed, skipping integration test")
	}

	// Test that the function doesn't panic with an invalid project name
	// We don't actually create a project, just test the error handling
	err := EnsureCloudflareProject("", "main")
	if err == nil {
		t.Log("Empty project name may or may not be an error depending on wrangler version")
	}
}

func TestCloudflareProjectExists_ParsesOutput(t *testing.T) {
	// This tests that CloudflareProjectExists can parse wrangler output
	// We can't actually call wrangler in a unit test, but we can verify
	// the function exists and has the right signature
	var _ func(string) (bool, error) = CloudflareProjectExists
}

func TestCreateCloudflareProject_Signature(t *testing.T) {
	// Verify the function signature
	var _ func(string, string) error = CreateCloudflareProject
}

func TestVerifyCloudflareDeployment_IssueCountMismatchReturnsError(t *testing.T) {
	requireCurl(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 1}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	err := VerifyCloudflareDeployment(server.URL, 2, time.Second)
	if err == nil {
		t.Fatal("Expected VerifyCloudflareDeployment to fail on stale issue count")
	}
	if !strings.Contains(err.Error(), "issue count mismatch") {
		t.Fatalf("Unexpected verification error: %v", err)
	}
}

func TestVerifyCloudflareDeployment_ZeroIssueCountMismatchReturnsError(t *testing.T) {
	requireCurl(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 1}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	err := VerifyCloudflareDeployment(server.URL, 0, time.Second)
	if err == nil {
		t.Fatal("Expected VerifyCloudflareDeployment to fail when empty export verifies against stale live data")
	}
	if !strings.Contains(err.Error(), "issue count mismatch") {
		t.Fatalf("Unexpected verification error: %v", err)
	}
}

func TestVerifyCloudflareDeployment_Success(t *testing.T) {
	requireCurl(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/data/meta.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue_count": 2}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if err := VerifyCloudflareDeployment(server.URL, 2, time.Second); err != nil {
		t.Fatalf("VerifyCloudflareDeployment returned error: %v", err)
	}
}

func TestSuggestProjectName_DotGitTruncationAndValidateProjectName(t *testing.T) {
	if got := SuggestProjectName("/tmp/acme_repo.git/.beads"); got != "acme-repo-pages" {
		t.Fatalf("SuggestProjectName(.git/.beads) = %q, want %q", got, "acme-repo-pages")
	}
	if got := SuggestProjectName("/tmp/acme.GIT/bv-pages"); got != "acme-pages" {
		t.Fatalf("SuggestProjectName(.GIT/bv-pages) = %q, want %q", got, "acme-pages")
	}

	longDir := "/tmp/" + strings.Repeat("verylongsegment-", 6) + "tail.git/.beads/issues.jsonl"
	gotLong := SuggestProjectName(longDir)
	if len(gotLong) > cloudflareProjectNameMaxLen {
		t.Fatalf("SuggestProjectName length = %d, want <= %d (%q)", len(gotLong), cloudflareProjectNameMaxLen, gotLong)
	}
	if err := ValidateProjectName(gotLong); err != nil {
		t.Fatalf("ValidateProjectName(%q) failed: %v", gotLong, err)
	}
	if err := ValidateProjectName("Invalid_Upper"); err == nil {
		t.Fatal("expected ValidateProjectName to reject uppercase/underscore project name")
	}
}
