package datasource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverBeadsWorkspaces(t *testing.T) {
	// Create temporary directory structure
	tmpDir := t.TempDir()

	// Create test .beads directories
	testDirs := []string{
		filepath.Join(tmpDir, ".beads"),                    // Root level
		filepath.Join(tmpDir, "repo1", ".beads"),           // First child
		filepath.Join(tmpDir, "repo2", ".beads"),           // Second child
		filepath.Join(tmpDir, "nested", "repo3", ".beads"), // Nested child
	}

	for _, dir := range testDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create test directory %s: %v", dir, err)
		}
	}

	// Test discovery with depth 3
	workspaces := DiscoverBeadsWorkspaces(tmpDir, 3)

	// Should find all 4 .beads directories
	if len(workspaces) != 4 {
		t.Errorf("Expected 4 workspaces, got %d", len(workspaces))
	}

	// Verify workspace information
	repoNames := make(map[string]bool)
	for _, ws := range workspaces {
		repoNames[ws.RepoName] = true

		// Verify BeadsDir ends with .beads
		if filepath.Base(ws.BeadsDir) != ".beads" {
			t.Errorf("Expected BeadsDir to end with .beads, got %s", ws.BeadsDir)
		}

		// Verify RepoPath is parent of BeadsDir
		expectedBeadsDir := filepath.Join(ws.RepoPath, ".beads")
		if ws.BeadsDir != expectedBeadsDir {
			t.Errorf("BeadsDir mismatch: expected %s, got %s", expectedBeadsDir, ws.BeadsDir)
		}
	}

	// Check that we found the expected repositories
	expectedRepos := []string{filepath.Base(tmpDir), "repo1", "repo2", "repo3"}
	for _, repo := range expectedRepos {
		if !repoNames[repo] {
			t.Errorf("Expected to find repository %s", repo)
		}
	}
}

func TestDiscoverBeadsWorkspaces_MaxDepth(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested structure deeper than maxDepth
	testDirs := []string{
		filepath.Join(tmpDir, "level1", ".beads"),
		filepath.Join(tmpDir, "level1", "level2", ".beads"),
		filepath.Join(tmpDir, "level1", "level2", "level3", ".beads"),
		filepath.Join(tmpDir, "level1", "level2", "level3", "level4", ".beads"),
	}

	for _, dir := range testDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create test directory %s: %v", dir, err)
		}
	}

	// Test with maxDepth = 2
	workspaces := DiscoverBeadsWorkspaces(tmpDir, 2)

	// Should only find level1 and level2 (depth 1 and 2)
	if len(workspaces) != 2 {
		t.Errorf("Expected 2 workspaces with maxDepth=2, got %d", len(workspaces))
	}
}

func TestDiscoverBeadsWorkspaces_IgnorePatterns(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .beads in directories that should be ignored
	testDirs := []string{
		filepath.Join(tmpDir, "valid-repo", ".beads"),
		filepath.Join(tmpDir, ".git", ".beads"),               // Should be ignored
		filepath.Join(tmpDir, "node_modules", "pkg", ".beads"), // Should be ignored
		filepath.Join(tmpDir, "target", "debug", ".beads"),     // Should be ignored
	}

	for _, dir := range testDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create test directory %s: %v", dir, err)
		}
	}

	workspaces := DiscoverBeadsWorkspaces(tmpDir, 3)

	// Should only find valid-repo
	if len(workspaces) != 1 {
		t.Errorf("Expected 1 workspace, got %d", len(workspaces))
	}

	if len(workspaces) > 0 && workspaces[0].RepoName != "valid-repo" {
		t.Errorf("Expected to find valid-repo, got %s", workspaces[0].RepoName)
	}
}

func TestDiscoverBeadsWorkspaces_NoBeadsDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some directories without .beads
	os.MkdirAll(filepath.Join(tmpDir, "repo1"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "repo2"), 0755)

	workspaces := DiscoverBeadsWorkspaces(tmpDir, 3)

	// Should find no workspaces
	if len(workspaces) != 0 {
		t.Errorf("Expected 0 workspaces, got %d", len(workspaces))
	}
}
