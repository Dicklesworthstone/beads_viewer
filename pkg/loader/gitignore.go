// Package loader provides issue loading and file discovery utilities.
// This file handles automatic .gitignore management for the .bv directory.
package loader

import (
	"os"
	"os/exec"
	"path/filepath"
)

// EnsureBVInGitignore ensures that .bv/ is ignored by git.
// This prevents bv-specific files (semantic search index, baselines, drift config, etc.)
// from polluting the git repository.
//
// The function is idempotent and safe to call multiple times.
// It uses git check-ignore to detect if .bv is already ignored (respecting all
// gitignore sources: .gitignore, .git/info/exclude, global gitignore, etc.).
// If not ignored, it appends ".bv/" to the project's .gitignore file.
//
// Returns nil on success, or an error if the file cannot be written.
func EnsureBVInGitignore(projectDir string) error {
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	// Check if .bv is already ignored by git
	if isBVIgnoredByGit(projectDir) {
		return nil
	}

	// Append .bv/ to .gitignore
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	return appendToGitignore(gitignorePath, ".bv/")
}

// isBVIgnoredByGit uses git check-ignore to determine if .bv is already ignored.
// This is more robust than manual parsing since it respects all gitignore sources:
// .gitignore, .git/info/exclude, global gitignore, and nested .gitignore files.
func isBVIgnoredByGit(projectDir string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", ".bv/")
	cmd.Dir = projectDir
	err := cmd.Run()
	// Exit code 0 means the path is ignored, exit code 1 means it's not ignored
	return err == nil
}

// appendToGitignore appends a pattern to the .gitignore file.
// It creates the file if it doesn't exist.
// It ensures there's a newline before the pattern if the file doesn't end with one.
func appendToGitignore(path string, pattern string) error {
	// Check if file exists and its current content
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Open file for appending (creates if not exists)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Build the content to append based on whether file has existing content
	var toWrite string
	if len(content) == 0 {
		// New file: just add comment and pattern (no leading blank line)
		toWrite = "# bv (beads viewer) local config and caches\n" + pattern + "\n"
	} else {
		// Existing file: ensure proper separation
		if content[len(content)-1] != '\n' {
			// File doesn't end with newline, add one first
			toWrite = "\n"
		}
		// Add blank line separator, comment, and pattern
		toWrite += "\n# bv (beads viewer) local config and caches\n" + pattern + "\n"
	}

	if _, err := file.WriteString(toWrite); err != nil {
		return err
	}

	return nil
}
