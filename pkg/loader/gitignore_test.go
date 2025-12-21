package loader

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo initializes a git repo in the given directory with isolated config
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	// Isolate from global gitconfig to avoid test pollution
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
}

func TestIsBVIgnoredByGit(t *testing.T) {
	// Isolate from user's global gitconfig
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	tests := []struct {
		name           string
		gitignoreContent string
		expected       bool
	}{
		{
			name:           "empty gitignore",
			gitignoreContent: "",
			expected:       false,
		},
		{
			name:           "has .bv",
			gitignoreContent: "node_modules/\n.bv\n*.log\n",
			expected:       true,
		},
		{
			name:           "has .bv/",
			gitignoreContent: "node_modules/\n.bv/\n*.log\n",
			expected:       true,
		},
		{
			name:           "has .bv/*",
			gitignoreContent: ".bv/*\n",
			expected:       true,
		},
		{
			name:           "has /.bv/",
			gitignoreContent: "/.bv/\n",
			expected:       true,
		},
		{
			name:           "commented out",
			gitignoreContent: "# .bv/\n",
			expected:       false,
		},
		{
			name:           "different pattern",
			gitignoreContent: ".beads/\nnode_modules/\n",
			expected:       false,
		},
		{
			name:           "similar but not matching",
			gitignoreContent: ".bv2/\n.bvx\nbv/\n",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			initGitRepo(t, tmpDir)

			if tt.gitignoreContent != "" {
				gitignorePath := filepath.Join(tmpDir, ".gitignore")
				if err := os.WriteFile(gitignorePath, []byte(tt.gitignoreContent), 0644); err != nil {
					t.Fatalf("failed to write .gitignore: %v", err)
				}
			}

			got := isBVIgnoredByGit(tmpDir)
			if got != tt.expected {
				t.Errorf("isBVIgnoredByGit() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsBVIgnoredByGit_NotAGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	// Not a git repo, git check-ignore will fail
	got := isBVIgnoredByGit(tmpDir)
	if got != false {
		t.Errorf("isBVIgnoredByGit() in non-git dir = %v, want false", got)
	}
}

func TestAppendToGitignore(t *testing.T) {
	tests := []struct {
		name            string
		existingContent string
		pattern         string
		wantContains    []string
		wantPrefix      string // expected prefix of the file (for checking no leading blank line)
	}{
		{
			name:            "new file",
			existingContent: "",
			pattern:         ".bv/",
			wantContains:    []string{"# bv (beads viewer)", ".bv/"},
			wantPrefix:      "#", // should start with comment, not blank line
		},
		{
			name:            "existing file with newline",
			existingContent: "node_modules/\n",
			pattern:         ".bv/",
			wantContains:    []string{"node_modules/", "# bv (beads viewer)", ".bv/"},
			wantPrefix:      "node_modules/",
		},
		{
			name:            "existing file without trailing newline",
			existingContent: "node_modules/",
			pattern:         ".bv/",
			wantContains:    []string{"node_modules/", "# bv (beads viewer)", ".bv/"},
			wantPrefix:      "node_modules/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			gitignorePath := filepath.Join(tmpDir, ".gitignore")

			// Create existing file if content is provided
			if tt.existingContent != "" {
				if err := os.WriteFile(gitignorePath, []byte(tt.existingContent), 0644); err != nil {
					t.Fatalf("failed to write existing file: %v", err)
				}
			}

			if err := appendToGitignore(gitignorePath, tt.pattern); err != nil {
				t.Fatalf("appendToGitignore() error = %v", err)
			}

			content, err := os.ReadFile(gitignorePath)
			if err != nil {
				t.Fatalf("failed to read result: %v", err)
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(string(content), want) {
					t.Errorf("result missing %q, got:\n%s", want, content)
				}
			}

			// Check prefix (no unexpected leading blank lines)
			if tt.wantPrefix != "" && !strings.HasPrefix(string(content), tt.wantPrefix) {
				t.Errorf("expected file to start with %q, got:\n%s", tt.wantPrefix, content)
			}
		})
	}
}

func TestEnsureBVInGitignore(t *testing.T) {
	// Isolate from user's global gitconfig
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	t.Run("creates gitignore if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		initGitRepo(t, tmpDir)

		if err := EnsureBVInGitignore(tmpDir); err != nil {
			t.Fatalf("EnsureBVInGitignore() error = %v", err)
		}

		content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		if !strings.Contains(string(content), ".bv/") {
			t.Errorf("expected .bv/ in .gitignore, got:\n%s", content)
		}
	})

	t.Run("adds to existing gitignore", func(t *testing.T) {
		tmpDir := t.TempDir()
		initGitRepo(t, tmpDir)
		gitignorePath := filepath.Join(tmpDir, ".gitignore")

		// Create existing .gitignore
		if err := os.WriteFile(gitignorePath, []byte("node_modules/\n"), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		if err := EnsureBVInGitignore(tmpDir); err != nil {
			t.Fatalf("EnsureBVInGitignore() error = %v", err)
		}

		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		if !strings.Contains(string(content), "node_modules/") {
			t.Error("existing content was lost")
		}
		if !strings.Contains(string(content), ".bv/") {
			t.Errorf("expected .bv/ in .gitignore, got:\n%s", content)
		}
	})

	t.Run("idempotent - doesn't duplicate", func(t *testing.T) {
		tmpDir := t.TempDir()
		initGitRepo(t, tmpDir)
		gitignorePath := filepath.Join(tmpDir, ".gitignore")

		// Create existing .gitignore with .bv/ already present
		if err := os.WriteFile(gitignorePath, []byte(".bv/\n"), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		if err := EnsureBVInGitignore(tmpDir); err != nil {
			t.Fatalf("EnsureBVInGitignore() error = %v", err)
		}

		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		// Count occurrences of .bv/
		count := strings.Count(string(content), ".bv/")
		if count != 1 {
			t.Errorf("expected exactly 1 occurrence of .bv/, got %d:\n%s", count, content)
		}
	})

	t.Run("recognizes existing .bv pattern", func(t *testing.T) {
		tmpDir := t.TempDir()
		initGitRepo(t, tmpDir)
		gitignorePath := filepath.Join(tmpDir, ".gitignore")

		// Create existing .gitignore with .bv (without slash)
		if err := os.WriteFile(gitignorePath, []byte(".bv\n"), 0644); err != nil {
			t.Fatalf("failed to write .gitignore: %v", err)
		}

		if err := EnsureBVInGitignore(tmpDir); err != nil {
			t.Fatalf("EnsureBVInGitignore() error = %v", err)
		}

		content, err := os.ReadFile(gitignorePath)
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		// Should still have just .bv, not add .bv/
		if strings.Contains(string(content), "# bv (beads viewer)") {
			t.Errorf("should not add when .bv already present, got:\n%s", content)
		}
	})

	t.Run("non-git directory still creates gitignore", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Not a git repo - git check-ignore will fail, so we'll add .bv/

		if err := EnsureBVInGitignore(tmpDir); err != nil {
			t.Fatalf("EnsureBVInGitignore() error = %v", err)
		}

		content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
		if err != nil {
			t.Fatalf("failed to read .gitignore: %v", err)
		}

		if !strings.Contains(string(content), ".bv/") {
			t.Errorf("expected .bv/ in .gitignore, got:\n%s", content)
		}
	})
}

func TestEnsureBVInGitignore_UsesCurrentDir(t *testing.T) {
	// Isolate from user's global gitconfig
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// Save current directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	// Call with empty string - should use current directory
	if err := EnsureBVInGitignore(""); err != nil {
		t.Fatalf("EnsureBVInGitignore() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".gitignore"))
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if !strings.Contains(string(content), ".bv/") {
		t.Errorf("expected .bv/ in .gitignore, got:\n%s", content)
	}
}
