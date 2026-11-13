package export

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
)

func TestReadWizardInputTimeoutDoesNotKeepConsumingPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	_, err = readWizardInput(reader, 20*time.Millisecond)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("readWizardInput error = %v, want deadline exceeded", err)
	}

	// SAFETY: after the timeout there must be no abandoned reader goroutine.
	// Data written now must remain available to this caller, and clearing the
	// helper's deadline must make the pipe reusable.
	want := "input after timeout\n"
	if _, err := io.WriteString(writer, want); err != nil {
		t.Fatalf("write after timeout: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read after timeout: %v", err)
	}
	if string(got) != want {
		t.Fatalf("read after timeout = %q, want %q", got, want)
	}
}

func TestReadWizardInputReadsClosedPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })

	want := "local\nProject Issues\n"
	if _, err := io.WriteString(writer, want); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	got, err := readWizardInput(reader, time.Second)
	if err != nil {
		t.Fatalf("readWizardInput: %v", err)
	}
	if string(got) != want {
		t.Fatalf("readWizardInput = %q, want %q", got, want)
	}
}

func TestWizardNewFormUsesBufferedInput(t *testing.T) {
	t.Setenv("TERM", "dumb")
	wizard := NewWizard("/tmp/test")
	wizard.input = &wizardInputReader{data: []byte("y\nn\n")}

	first := false
	second := true
	form := wizard.newForm(huh.NewGroup(
		huh.NewConfirm().
			Title("First?").
			Value(&first),
		huh.NewConfirm().
			Title("Second?").
			Value(&second),
	))
	if err := form.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !first || second {
		t.Fatalf("buffered answers = (%t, %t), want (true, false)", first, second)
	}
}

func TestNewWizard(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	if wizard == nil {
		t.Fatal("NewWizard returned nil")
	}

	if wizard.beadsPath != "/tmp/test" {
		t.Errorf("Expected beadsPath '/tmp/test', got %s", wizard.beadsPath)
	}

	if wizard.config == nil {
		t.Error("Expected config to be initialized")
	}

	// Check new defaults
	if !wizard.config.IncludeClosed {
		t.Error("Expected IncludeClosed to be true by default")
	}
	if !wizard.config.IncludeHistory {
		t.Error("Expected IncludeHistory to be true by default")
	}
}

func TestWizardConfig(t *testing.T) {
	config := WizardConfig{
		IncludeClosed:   true,
		IncludeHistory:  true,
		Title:           "Test Title",
		Subtitle:        "Test Subtitle",
		DeployTarget:    "github",
		RepoName:        "test-repo",
		RepoPrivate:     true,
		RepoDescription: "Test description",
		OutputPath:      "/tmp/output",
	}

	if !config.IncludeClosed {
		t.Error("Expected IncludeClosed to be true")
	}

	if !config.IncludeHistory {
		t.Error("Expected IncludeHistory to be true")
	}

	if config.Title != "Test Title" {
		t.Errorf("Expected Title 'Test Title', got %s", config.Title)
	}

	if config.DeployTarget != "github" {
		t.Errorf("Expected DeployTarget 'github', got %s", config.DeployTarget)
	}

	if !config.RepoPrivate {
		t.Error("Expected RepoPrivate to be true")
	}
}

func TestWizardResult(t *testing.T) {
	result := WizardResult{
		BundlePath:   "/tmp/bundle",
		RepoFullName: "user/repo",
		PagesURL:     "https://user.github.io/repo/",
		DeployTarget: "github",
	}

	if result.BundlePath != "/tmp/bundle" {
		t.Errorf("Expected BundlePath '/tmp/bundle', got %s", result.BundlePath)
	}

	if result.RepoFullName != "user/repo" {
		t.Errorf("Expected RepoFullName 'user/repo', got %s", result.RepoFullName)
	}

	if result.PagesURL != "https://user.github.io/repo/" {
		t.Errorf("Expected PagesURL 'https://user.github.io/repo/', got %s", result.PagesURL)
	}
}

func TestWizard_GetConfig(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	config := wizard.GetConfig()
	if config == nil {
		t.Fatal("GetConfig returned nil")
	}

	// Default values - both IncludeClosed and IncludeHistory default to true
	if !config.IncludeClosed {
		t.Error("Expected IncludeClosed to be true by default")
	}

	if !config.IncludeHistory {
		t.Error("Expected IncludeHistory to be true by default")
	}

	if config.DeployTarget != "" {
		t.Errorf("Expected empty DeployTarget by default, got %s", config.DeployTarget)
	}
}

func TestWizardConfigPath(t *testing.T) {
	path := WizardConfigPath()

	// Should return a valid path (or empty if no home dir)
	if path != "" {
		if !filepath.IsAbs(path) {
			t.Errorf("Expected absolute path, got %s", path)
		}

		// Should end with pages-wizard.json
		if filepath.Base(path) != "pages-wizard.json" {
			t.Errorf("Expected path to end with pages-wizard.json, got %s", path)
		}
	}
}

func TestSaveAndLoadWizardConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	loaded, err := LoadWizardConfig()
	if err != nil {
		t.Fatalf("LoadWizardConfig returned error: %v", err)
	}
	if loaded != nil {
		t.Fatalf("Expected nil config when file doesn't exist, got %+v", loaded)
	}

	config := &WizardConfig{
		IncludeClosed:  true,
		IncludeHistory: true,
		Title:          "Saved Title",
		SourcePath:     filepath.Join(tmpDir, ".beads", "selected.jsonl"),
		DeployTarget:   "github",
		RepoName:       "saved-repo",
		RepoPrivate:    true,
	}

	if err := SaveWizardConfig(config); err != nil {
		t.Fatalf("SaveWizardConfig returned error: %v", err)
	}

	loaded, err = LoadWizardConfig()
	if err != nil {
		t.Fatalf("LoadWizardConfig after save returned error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Expected loaded config, got nil")
	}
	if loaded.Title != "Saved Title" {
		t.Fatalf("Expected loaded Title %q, got %q", "Saved Title", loaded.Title)
	}
	if loaded.RepoName != "saved-repo" {
		t.Fatalf("Expected loaded RepoName %q, got %q", "saved-repo", loaded.RepoName)
	}
	if loaded.DeployTarget != "github" {
		t.Fatalf("Expected loaded DeployTarget %q, got %q", "github", loaded.DeployTarget)
	}
	if loaded.SourcePath != config.SourcePath {
		t.Fatalf("Expected loaded SourcePath %q, got %q", config.SourcePath, loaded.SourcePath)
	}
}

func TestSaveWizardConfigRejectsNil(t *testing.T) {
	if err := SaveWizardConfig(nil); err == nil {
		t.Fatal("Expected SaveWizardConfig to reject nil config")
	}
}

func TestSaveWizardConfigCleansTempOnReplaceFailure(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configPath := WizardConfigPath()
	if configPath == "" {
		t.Skip("WizardConfigPath returned empty path")
	}
	if err := os.MkdirAll(configPath, 0755); err != nil {
		t.Fatalf("MkdirAll destination directory: %v", err)
	}

	err := SaveWizardConfig(&WizardConfig{Title: "Cannot Replace Directory"})
	if err == nil {
		t.Fatal("Expected SaveWizardConfig to fail when destination is a directory")
	}

	matches, err := filepath.Glob(filepath.Join(filepath.Dir(configPath), filepath.Base(configPath)+".tmp-*"))
	if err != nil {
		t.Fatalf("Glob temporary config files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("Expected failed save to clean temporary config files, found %v", matches)
	}
}

func TestLoadWizardConfig_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	configPath := WizardConfigPath()
	if configPath == "" {
		t.Skip("WizardConfigPath returned empty path")
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("{"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadWizardConfig()
	if err == nil {
		t.Fatal("Expected LoadWizardConfig to return error for invalid JSON")
	}
}

func TestWizard_PerformExport(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	tmpDir := t.TempDir()
	err := wizard.PerformExport(tmpDir)
	if err != nil {
		t.Errorf("PerformExport returned unexpected error: %v", err)
	}

	if wizard.bundlePath != tmpDir {
		t.Errorf("Expected bundlePath %s, got %s", tmpDir, wizard.bundlePath)
	}
}

func TestWizard_PrintBanner(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	// Just verify it doesn't panic
	wizard.printBanner()
}

func TestWizard_PrintSuccess_GitHub(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	result := &WizardResult{
		BundlePath:   "/tmp/bundle",
		RepoFullName: "user/repo",
		PagesURL:     "https://user.github.io/repo/",
		DeployTarget: "github",
	}

	// Just verify it doesn't panic
	wizard.PrintSuccess(result)
}

func TestWizard_PrintSuccess_Local(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	result := &WizardResult{
		BundlePath:   "/tmp/bundle",
		DeployTarget: "local",
	}

	// Just verify it doesn't panic
	wizard.PrintSuccess(result)
}

func TestWizard_PrintSuccess_Cloudflare(t *testing.T) {
	wizard := NewWizard("/tmp/test")

	result := &WizardResult{
		BundlePath:        "/tmp/bundle",
		CloudflareProject: "my-project",
		CloudflareURL:     "https://my-project.pages.dev",
		DeployTarget:      "cloudflare",
	}

	// Just verify it doesn't panic
	wizard.PrintSuccess(result)
}

func TestWizard_CollectGitHubConfig_UsesSuggestRepoName(t *testing.T) {
	t.Setenv("TERM", "dumb")
	tmpDir := t.TempDir()
	bundleDir := filepath.Join(tmpDir, "acme-tracker", "bv-pages")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	wizard := NewWizard(filepath.Join(tmpDir, "acme-tracker", ".beads"))
	wizard.bundlePath = bundleDir
	wizard.input = &wizardInputReader{data: []byte("\nn\n\n")}

	if err := wizard.collectGitHubConfig(); err != nil {
		t.Fatalf("collectGitHubConfig: %v", err)
	}
	if wizard.config.RepoName != "acme-tracker-pages" {
		t.Fatalf("RepoName = %q, want %q", wizard.config.RepoName, "acme-tracker-pages")
	}

	wizardDefault := NewWizard(filepath.Join(tmpDir, "acme-tracker", ".beads"))
	wizardDefault.input = &wizardInputReader{data: []byte("\nn\n\n")}
	if err := wizardDefault.collectGitHubConfig(); err != nil {
		t.Fatalf("collectGitHubConfig (empty bundlePath): %v", err)
	}
	if !strings.HasSuffix(wizardDefault.config.RepoName, "-pages") {
		t.Fatalf("RepoName with empty bundlePath = %q, want -pages suffix", wizardDefault.config.RepoName)
	}
}

func TestWizard_CheckPrerequisites_GHMissing(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("PATH", t.TempDir())

	// Non-interactive without input buffer returns clean error immediately
	nonInteractive := NewWizard("/tmp/test")
	nonInteractive.config.DeployTarget = "github"
	err := nonInteractive.checkPrerequisites()
	if err == nil || !strings.Contains(err.Error(), "gh CLI is required") {
		t.Fatalf("non-interactive checkPrerequisites error = %v, want 'gh CLI is required'", err)
	}

	// Interactive prompt declined returns clean error without attempting install
	declined := NewWizard("/tmp/test")
	declined.config.DeployTarget = "github"
	declined.input = &wizardInputReader{data: []byte("n\n")}
	err = declined.checkPrerequisites()
	if err == nil || !strings.Contains(err.Error(), "gh CLI is required") {
		t.Fatalf("declined checkPrerequisites error = %v, want 'gh CLI is required'", err)
	}
}
