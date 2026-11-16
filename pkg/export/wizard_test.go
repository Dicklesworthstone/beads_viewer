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
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")

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
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")

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
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")

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
	if wizardDefault.config.RepoName != "acme-tracker-pages" {
		t.Fatalf("RepoName with empty bundlePath (.beads) = %q, want %q", wizardDefault.config.RepoName, "acme-tracker-pages")
	}

	wizardJSONL := NewWizard(filepath.Join(tmpDir, "acme-tracker", ".beads", "issues.jsonl"))
	wizardJSONL.input = &wizardInputReader{data: []byte("\nn\n\n")}
	if err := wizardJSONL.collectGitHubConfig(); err != nil {
		t.Fatalf("collectGitHubConfig (issues.jsonl): %v", err)
	}
	if wizardJSONL.config.RepoName != "acme-tracker-pages" {
		t.Fatalf("RepoName with empty bundlePath (issues.jsonl) = %q, want %q", wizardJSONL.config.RepoName, "acme-tracker-pages")
	}

	cfWizard := NewWizard(filepath.Join(tmpDir, "acme-tracker", ".beads", "issues.jsonl"))
	cfWizard.input = &wizardInputReader{data: []byte("\n\n")}
	if err := cfWizard.collectCloudflareConfig(); err != nil {
		t.Fatalf("collectCloudflareConfig (issues.jsonl): %v", err)
	}
	if cfWizard.config.CloudflareProject != "acme-tracker-pages" {
		t.Fatalf("CloudflareProject with empty bundlePath (issues.jsonl) = %q, want %q", cfWizard.config.CloudflareProject, "acme-tracker-pages")
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

func TestWizard_CollectCloudflareConfig_ValidatesProjectName(t *testing.T) {
	t.Setenv("TERM", "dumb")
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, "acme-repo.git", ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Default suggestion strips .git and validates cleanly
	validWizard := NewWizard(beadsDir)
	validWizard.input = &wizardInputReader{data: []byte("\n\n")}
	if err := validWizard.collectCloudflareConfig(); err != nil {
		t.Fatalf("collectCloudflareConfig valid default failed: %v", err)
	}
	if validWizard.config.CloudflareProject != "acme-repo-pages" {
		t.Fatalf("CloudflareProject = %q, want %q", validWizard.config.CloudflareProject, "acme-repo-pages")
	}

	// Invalid project name in accessible/dumb mode fails form validation instead of silently accepting invalid name
	invalidWizard := NewWizard(beadsDir)
	invalidWizard.input = &wizardInputReader{data: []byte("INVALID_PROJECT_NAME!\nmain\n")}
	err := invalidWizard.collectCloudflareConfig()
	if err == nil && invalidWizard.config.CloudflareProject == "INVALID_PROJECT_NAME!" {
		t.Fatal("expected collectCloudflareConfig to reject invalid Cloudflare project name 'INVALID_PROJECT_NAME!'")
	}
}

func TestWizard_IsolatesSavedConfigByRepo(t *testing.T) {
	t.Setenv("TERM", "dumb")
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")

	parentDir := t.TempDir()
	repoABeads := filepath.Join(parentDir, "repo-a", ".beads")
	repoBBeads := filepath.Join(parentDir, "repo-b", ".beads")
	for _, dir := range []string{repoABeads, repoBBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte{}, 0o644); err != nil {
			t.Fatalf("WriteFile issues.jsonl: %v", err)
		}
	}

	wizA := NewWizard(filepath.Join(repoABeads, "issues.jsonl"))
	wizA.config.Title = "Repo A Dashboard"
	wizA.config.DeployTarget = "github"
	wizA.config.RepoName = "repo-a-pages"
	wizA.config.RepoPrivate = true
	if err := SaveWizardConfig(wizA.config); err != nil {
		t.Fatalf("SaveWizardConfig(repo-a): %v", err)
	}

	saved, err := LoadWizardConfig()
	if err != nil || saved == nil {
		t.Fatalf("LoadWizardConfig: saved=%+v err=%v", saved, err)
	}

	// Same repo (including .beads directory path) matches saved config.
	if !NewWizard(repoABeads).matchesSavedConfigRepo(saved) {
		t.Fatal("expected repo-a wizard to match repo-a saved config")
	}

	// Symlink to repo-a also resolves and matches.
	symlinkRepoA := filepath.Join(parentDir, "repo-a-link")
	if err := os.Symlink(filepath.Join(parentDir, "repo-a"), symlinkRepoA); err == nil {
		if !NewWizard(filepath.Join(symlinkRepoA, ".beads")).matchesSavedConfigRepo(saved) {
			t.Fatal("expected symlinked repo-a wizard to match repo-a saved config")
		}
	}

	// Different repo-b MUST NOT match or reuse repo-a's saved deployment config!
	wizB := NewWizard(repoBBeads)
	if wizB.matchesSavedConfigRepo(saved) {
		t.Fatalf("repo-b wizard falsely matched repo-a saved config: saved=%+v", saved)
	}

	// Running wizB.Run() must go straight to Step 1 (Export Configuration) rather
	// than prompting "Use previous configuration?" or inheriting repo-a-pages.
	// Step 1: IncludeClosed (y), IncludeHistory (y), Title ("Repo B Dashboard"), Subtitle ("")
	// Step 2: Select option 3 ("local")
	// Step 3: Output directory ("./bv-pages-b")
	wizB.input = &wizardInputReader{data: []byte("y\ny\nRepo B Dashboard\n\n3\n./bv-pages-b\n")}
	res, err := wizB.Run()
	if err != nil {
		t.Fatalf("wizB.Run() failed: %v", err)
	}
	if res.DeployTarget != "local" {
		t.Fatalf("wizB DeployTarget = %q, want %q", res.DeployTarget, "local")
	}
	if wizB.isUpdate {
		t.Fatal("wizB.isUpdate = true, expected false when saved config belongs to repo-a")
	}
	if wizB.config.RepoName == "repo-a-pages" || wizB.config.Title == "Repo A Dashboard" {
		t.Fatalf("wizB leaked repo-a config: %+v", wizB.config)
	}
}

func TestWizardConfig_XDGAndNoSavedConfig(t *testing.T) {
	homeDir := t.TempDir()
	xdgDir := filepath.Join(t.TempDir(), "custom-xdg")
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("BV_NO_SAVED_CONFIG", "")

	wantPath := filepath.Join(xdgDir, "bv", "pages-wizard.json")
	if got := WizardConfigPath(); got != wantPath {
		t.Fatalf("WizardConfigPath() with XDG_CONFIG_HOME = %q, want %q", got, wantPath)
	}

	// When BV_NO_SAVED_CONFIG is set, SaveWizardConfig must not write to disk.
	t.Setenv("BV_NO_SAVED_CONFIG", "1")
	if err := SaveWizardConfig(&WizardConfig{
		Title:        "Should Not Persist",
		DeployTarget: "local",
	}); err != nil {
		t.Fatalf("SaveWizardConfig under BV_NO_SAVED_CONFIG=1 failed: %v", err)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("expected no config file written when BV_NO_SAVED_CONFIG=1, stat err=%v", err)
	}

	// Write a real config when BV_NO_SAVED_CONFIG is cleared, then verify LoadWizardConfig
	// and LoadWizardConfigForRepo both return nil without reading when BV_NO_SAVED_CONFIG=1.
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	repoBeads := filepath.Join(t.TempDir(), "xdg-repo", ".beads")
	if err := os.MkdirAll(repoBeads, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wiz := NewWizard(repoBeads)
	wiz.config.Title = "XDG Persisted"
	wiz.config.DeployTarget = "local"
	wiz.config.OutputPath = "./xdg-out"
	if err := SaveWizardConfig(wiz.config); err != nil {
		t.Fatalf("SaveWizardConfig(xdg): %v", err)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected XDG config file at %q: %v", wantPath, err)
	}

	t.Setenv("BV_NO_SAVED_CONFIG", "1")
	if cfg, err := LoadWizardConfig(); err != nil || cfg != nil {
		t.Fatalf("LoadWizardConfig under BV_NO_SAVED_CONFIG=1 = (%+v, %v), want (nil, nil)", cfg, err)
	}
	if cfg, err := LoadWizardConfigForRepo(repoBeads); err != nil || cfg != nil {
		t.Fatalf("LoadWizardConfigForRepo under BV_NO_SAVED_CONFIG=1 = (%+v, %v), want (nil, nil)", cfg, err)
	}
}

func TestWizard_MultiRepoSavedConfigPreservation(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("BV_NO_SAVED_CONFIG", "")

	parentDir := t.TempDir()
	repoABeads := filepath.Join(parentDir, "repo-alpha", ".beads")
	repoBBeads := filepath.Join(parentDir, "repo-beta", ".beads")
	for _, dir := range []string{repoABeads, repoBBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte{}, 0o644); err != nil {
			t.Fatalf("WriteFile issues.jsonl: %v", err)
		}
	}

	wizA := NewWizard(repoABeads)
	wizA.config.Title = "Alpha Pages"
	wizA.config.DeployTarget = "local"
	wizA.config.OutputPath = filepath.Join(parentDir, "alpha-out")
	if err := SaveWizardConfig(wizA.config); err != nil {
		t.Fatalf("SaveWizardConfig(alpha): %v", err)
	}

	wizB := NewWizard(repoBBeads)
	wizB.config.Title = "Beta Pages"
	wizB.config.DeployTarget = "local"
	wizB.config.OutputPath = filepath.Join(parentDir, "beta-out")
	if err := SaveWizardConfig(wizB.config); err != nil {
		t.Fatalf("SaveWizardConfig(beta): %v", err)
	}

	// Saving Repo B must NOT clobber Repo A's saved config!
	loadedA, err := LoadWizardConfigForRepo(repoABeads)
	if err != nil || loadedA == nil {
		t.Fatalf("LoadWizardConfigForRepo(alpha): got (%+v, %v)", loadedA, err)
	}
	if loadedA.Title != "Alpha Pages" || loadedA.OutputPath != wizA.config.OutputPath {
		t.Fatalf("Repo A config clobbered after saving Repo B: %+v", loadedA)
	}

	loadedB, err := LoadWizardConfigForRepo(repoBBeads)
	if err != nil || loadedB == nil {
		t.Fatalf("LoadWizardConfigForRepo(beta): got (%+v, %v)", loadedB, err)
	}
	if loadedB.Title != "Beta Pages" || loadedB.OutputPath != wizB.config.OutputPath {
		t.Fatalf("Repo B config mismatch: %+v", loadedB)
	}

	// Re-running Wizard in Repo A must offer Repo A's saved config and accept "y\n" update!
	wizAResume := NewWizard(repoABeads)
	wizAResume.input = &wizardInputReader{data: []byte("y\n")}
	res, err := wizAResume.Run()
	if err != nil {
		t.Fatalf("wizAResume.Run() failed: %v", err)
	}
	if !wizAResume.isUpdate || res.DeployTarget != "local" || wizAResume.config.Title != "Alpha Pages" {
		t.Fatalf("wizAResume failed to restore Repo A config: isUpdate=%v res=%+v cfg=%+v",
			wizAResume.isUpdate, res, wizAResume.config)
	}
}
