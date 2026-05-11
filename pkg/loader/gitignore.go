// Package loader provides issue loading and file discovery utilities.
// This file handles automatic .gitignore management for the .bv directory.
package loader

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resolveGitDir returns the absolute path of the repository's git directory
// for projectDir, using `git rev-parse --git-dir`. This correctly handles
// worktrees and submodules where `<projectDir>/.git` is a file pointer.
//
// Exposed as a package variable so tests can stub it.
var resolveGitDir = func(projectDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = projectDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", os.ErrNotExist
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(projectDir, gitDir)
	}
	return gitDir, nil
}

// resolveCoreExcludesFile returns the path configured by
// `git config --get core.excludesFile`, expanded for a leading `~`.
// Returns empty string if unset.
//
// Exposed as a package variable so tests can stub it.
var resolveCoreExcludesFile = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "core.excludesFile")
	out, err := cmd.Output()
	if err != nil {
		// `git config --get` exits 1 when key is unset; treat as empty.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return expandHome(strings.TrimSpace(string(out))), nil
}

// EnsureBVInGitignore ensures that .bv/ is ignored by git for projectDir.
// It inspects, in order:
//   - <projectDir>/.gitignore
//   - <gitDir>/info/exclude       (per-repo, uncommitted)
//   - core.excludesFile           (user-wide, from `git config`)
//   - $XDG_CONFIG_HOME/git/ignore (or ~/.config/git/ignore) and
//     ~/.gitignore_global         (conventional fallbacks)
//
// If any of those already covers .bv/, the local .gitignore is left untouched.
// Otherwise, .bv/ is appended to <projectDir>/.gitignore (creating it if
// needed). The function is idempotent and safe to call repeatedly.
func EnsureBVInGitignore(projectDir string) error {
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	gitignorePath := filepath.Join(projectDir, ".gitignore")

	// 1. Local .gitignore.
	if covered, err := isBVInGitignore(gitignorePath); err == nil && covered {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	// 2. .git/info/exclude.
	if gitDir, err := resolveGitDir(projectDir); err == nil && gitDir != "" {
		excludePath := filepath.Join(gitDir, "info", "exclude")
		if covered, err := isBVInGitignore(excludePath); err == nil && covered {
			return nil
		}
	}

	// 3. core.excludesFile, then conventional fallbacks.
	for _, candidate := range globalGitignoreCandidates() {
		if candidate == "" {
			continue
		}
		if covered, err := isBVInGitignore(candidate); err == nil && covered {
			return nil
		}
	}

	// 4. Append.
	return appendToGitignore(gitignorePath, ".bv/")
}

// globalGitignoreCandidates returns the ordered list of paths to inspect for a
// global ignore of .bv/. First entry is core.excludesFile (if set), followed by
// the conventional fallbacks git itself checks.
func globalGitignoreCandidates() []string {
	var paths []string
	if p, err := resolveCoreExcludesFile(); err == nil && p != "" {
		paths = append(paths, p)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "git", "ignore"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths,
			filepath.Join(home, ".config", "git", "ignore"),
			filepath.Join(home, ".gitignore_global"),
		)
	}
	return paths
}

// expandHome expands a leading `~/` (or bare `~`) using the user's home dir.
// Other paths are returned unchanged.
func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// isBVInGitignore reports whether the file at path contains a pattern that
// covers the .bv directory. Missing files return (false, os.ErrNotExist).
func isBVInGitignore(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if matchesBVPattern(line) {
			return true, nil
		}
	}

	return false, scanner.Err()
}

// matchesBVPattern reports whether a gitignore line covers the .bv directory.
// Recognized forms (with optional leading `/` or `**/`):
//
//	.bv  .bv/  .bv/*  .bv/**  .bv/**/*
func matchesBVPattern(line string) bool {
	normalized := strings.TrimPrefix(line, "/")
	normalized = strings.TrimPrefix(normalized, "**/")

	patterns := []string{
		".bv",
		".bv/",
		".bv/*",
		".bv/**",
		".bv/**/*",
	}

	for _, pattern := range patterns {
		if normalized == pattern {
			return true
		}
	}

	return false
}

// appendToGitignore appends a pattern to the .gitignore file, creating it if
// missing. Ensures a separating blank line when appending to existing content.
func appendToGitignore(path string, pattern string) error {
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	var toWrite string
	if len(content) == 0 {
		toWrite = "# bv (beads viewer) local config and caches\n" + pattern + "\n"
	} else {
		if content[len(content)-1] != '\n' {
			toWrite = "\n"
		}
		toWrite += "\n# bv (beads viewer) local config and caches\n" + pattern + "\n"
	}

	_, err = file.WriteString(toWrite)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
