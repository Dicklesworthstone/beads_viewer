package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGlobalGitignore points HOME and XDG_CONFIG_HOME at an empty tmp dir
// and stubs resolveCoreExcludesFile/resolveGitDir to return empty, so tests
// don't read the developer's real ~/.gitignore_global or run real git.
func isolateGlobalGitignore(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	origGitDir := resolveGitDir
	origExcludes := resolveCoreExcludesFile
	resolveGitDir = func(string) (string, error) { return "", os.ErrNotExist }
	resolveCoreExcludesFile = func() (string, error) { return "", nil }
	t.Cleanup(func() {
		resolveGitDir = origGitDir
		resolveCoreExcludesFile = origExcludes
	})
}

func TestMatchesBVPattern(t *testing.T) {
	tests := []struct {
		line    string
		matches bool
	}{
		// Should match
		{".bv", true},
		{".bv/", true},
		{".bv/*", true},
		{".bv/**", true},
		{".bv/**/*", true},
		{"/.bv", true}, // Leading slash should be normalized
		{"/.bv/", true},
		{"**/.bv", true},
		{"**/.bv/", true},
		{"**/.bv/*", true},
		{"**/.bv/**", true},

		// Should not match
		{"", false},
		{"#.bv", false}, // Comment
		{".bv2", false},
		{".bvx", false},
		{"bv/", false},
		{".beads/", false},
		{"node_modules/", false},
		{".bv-backup", false},
		{"*.bv", false},
		{"**/.bv2", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := matchesBVPattern(tt.line)
			if got != tt.matches {
				t.Errorf("matchesBVPattern(%q) = %v, want %v", tt.line, got, tt.matches)
			}
		})
	}
}

func TestIsBVInGitignore(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "empty file",
			content:  "",
			expected: false,
		},
		{
			name:     "has .bv",
			content:  "node_modules/\n.bv\n*.log\n",
			expected: true,
		},
		{
			name:     "has .bv/",
			content:  "node_modules/\n.bv/\n*.log\n",
			expected: true,
		},
		{
			name:     "has .bv/*",
			content:  ".bv/*\n",
			expected: true,
		},
		{
			name:     "has /.bv/",
			content:  "/.bv/\n",
			expected: true,
		},
		{
			name:     "commented out",
			content:  "# .bv/\n",
			expected: false,
		},
		{
			name:     "different pattern",
			content:  ".beads/\nnode_modules/\n",
			expected: false,
		},
		{
			name:     "similar but not matching",
			content:  ".bv2/\n.bvx\nbv/\n",
			expected: false,
		},
		{
			name:     "with whitespace",
			content:  "  .bv/  \n",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			gitignorePath := filepath.Join(tmpDir, ".gitignore")

			if err := os.WriteFile(gitignorePath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			got, err := isBVInGitignore(gitignorePath)
			if err != nil {
				t.Fatalf("isBVInGitignore() error = %v", err)
			}
			if got != tt.expected {
				t.Errorf("isBVInGitignore() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsBVInGitignore_FileNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	gitignorePath := filepath.Join(tmpDir, ".gitignore")

	_, err := isBVInGitignore(gitignorePath)
	if !os.IsNotExist(err) {
		t.Errorf("expected IsNotExist error, got %v", err)
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
	isolateGlobalGitignore(t)
	t.Run("creates gitignore if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()

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
}

func TestEnsureBVInGitignore_UsesCurrentDir(t *testing.T) {
	isolateGlobalGitignore(t)
	// Save current directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	tmpDir := t.TempDir()
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

func TestEnsureBVInGitignore_RespectsGitInfoExclude(t *testing.T) {
	isolateGlobalGitignore(t)

	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0755); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(gitDir, "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("# local excludes\n.bv/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := resolveGitDir
	resolveGitDir = func(dir string) (string, error) { return gitDir, nil }
	t.Cleanup(func() { resolveGitDir = orig })

	if err := EnsureBVInGitignore(tmpDir); err != nil {
		t.Fatalf("EnsureBVInGitignore() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitignore to be created, stat err = %v", err)
	}
}

func TestEnsureBVInGitignore_RespectsCoreExcludesFile(t *testing.T) {
	isolateGlobalGitignore(t)

	tmpDir := t.TempDir()
	globalPath := filepath.Join(tmpDir, "global_ignore")
	if err := os.WriteFile(globalPath, []byte("**/.bv/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	orig := resolveCoreExcludesFile
	resolveCoreExcludesFile = func() (string, error) { return globalPath, nil }
	t.Cleanup(func() { resolveCoreExcludesFile = orig })

	projectDir := t.TempDir()
	if err := EnsureBVInGitignore(projectDir); err != nil {
		t.Fatalf("EnsureBVInGitignore() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitignore to be created, stat err = %v", err)
	}
}

func TestEnsureBVInGitignore_RespectsHomeGitignoreGlobal(t *testing.T) {
	isolateGlobalGitignore(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".gitignore_global"), []byte(".bv\n"), 0644); err != nil {
		t.Fatal(err)
	}

	projectDir := t.TempDir()
	if err := EnsureBVInGitignore(projectDir); err != nil {
		t.Fatalf("EnsureBVInGitignore() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectDir, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("expected no .gitignore to be created, stat err = %v", err)
	}
}

func TestEnsureBVInGitignore_AppendsWhenNoSourceCovers(t *testing.T) {
	isolateGlobalGitignore(t)

	projectDir := t.TempDir()
	if err := EnsureBVInGitignore(projectDir); err != nil {
		t.Fatalf("EnsureBVInGitignore() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore created, err = %v", err)
	}
	if !strings.Contains(string(content), ".bv/") {
		t.Errorf("expected .bv/ in .gitignore, got:\n%s", content)
	}

	// Second call must be idempotent.
	if err := EnsureBVInGitignore(projectDir); err != nil {
		t.Fatalf("second EnsureBVInGitignore() error = %v", err)
	}
	content2, _ := os.ReadFile(filepath.Join(projectDir, ".gitignore"))
	if strings.Count(string(content2), ".bv/") != 1 {
		t.Errorf("expected 1 occurrence of .bv/, got %d:\n%s", strings.Count(string(content2), ".bv/"), content2)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := map[string]string{
		"":              "",
		"~":             home,
		"~/foo":         filepath.Join(home, "foo"),
		"/abs/path":     "/abs/path",
		"relative/path": "relative/path",
	}
	for in, want := range tests {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
