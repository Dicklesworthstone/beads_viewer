// Package loader provides issue loading and file discovery utilities.
// This file handles automatic ignore-file management for the .bv directory.
package loader

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// NoGitignoreEnvVar disables all automatic ignore-file management when set to
// any non-empty value (same convention as BV_NO_BROWSER / BV_NO_CACHE).
const NoGitignoreEnvVar = "BV_NO_GITIGNORE"

// EnsureBVIgnored makes sure the .bv/ directory (semantic search index,
// baselines, drift config, etc.) is ignored by git without littering the
// project's committed files.
//
// Behavior matrix (issue #179; supersedes the old unconditional .gitignore
// append):
//
//   - BV_NO_GITIGNORE set (non-empty)  → do nothing.
//   - No .git in projectDir            → do nothing (nothing to ignore for).
//   - .bv already covered by the repo .gitignore, by .git/info/exclude, or by
//     the user's global gitignore (core.excludesFile / XDG default) → do
//     nothing.
//   - Otherwise → append ".bv/" to .git/info/exclude (pure file I/O, no
//     subprocess, invisible to collaborators, shared across linked worktrees,
//     never needs a commit). Linked-worktree and submodule ".git" pointer
//     files are resolved; if the git dir cannot be resolved, fall back to
//     appending to the repo .gitignore so the artifacts still get ignored.
//
// The function is idempotent and safe to call multiple times. It never
// deletes or rewrites existing user content — it only ever appends.
//
// Returns nil on success, or an error if a file cannot be read/written.
func EnsureBVIgnored(projectDir string) error {
	if env.NoGitignore.Get() != "" {
		return nil
	}

	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return err
		}
	}

	// Without a git repository there is nothing to ignore for; never create
	// ignore files in non-git directories.
	gitPath := filepath.Join(projectDir, ".git")
	gitInfo, err := os.Stat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// Already covered by the committed .gitignore? Leave everything alone.
	gitignorePath := filepath.Join(projectDir, ".gitignore")
	present, err := ignoreFileCoversBV(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if present {
		return nil
	}

	// Resolve the git dir (plain directory, or a linked-worktree/submodule
	// "gitdir:" pointer file) and check .git/info/exclude.
	excludePath := ""
	if gitDir, ok := resolveGitCommonDir(projectDir, gitPath, gitInfo.IsDir()); ok {
		excludePath = filepath.Join(gitDir, "info", "exclude")
		present, err = ignoreFileCoversBV(excludePath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if present {
			return nil
		}
	}

	// Already covered by the user's global gitignore? (Best-effort, pure file
	// I/O — no git subprocess.)
	if globalIgnoreCoversBV() {
		return nil
	}

	// Preferred destination: the per-repo exclude file.
	if excludePath != "" {
		if err := os.MkdirAll(filepath.Dir(excludePath), 0755); err == nil {
			return appendToIgnoreFile(excludePath, ".bv/")
		}
	}

	// Graceful fallback: .git exists but the exclude file is unusable —
	// append to the repo .gitignore so .bv/ still gets ignored.
	return appendToIgnoreFile(gitignorePath, ".bv/")
}

// resolveGitCommonDir resolves the directory that holds the repository's
// shared state (where info/exclude lives). For a normal checkout that is the
// .git directory itself. For a linked worktree or a submodule, .git is a file
// containing "gitdir: <path>"; linked worktrees additionally point at their
// shared common dir via a "commondir" pointer file, which git uses to locate
// the single info/exclude shared by all worktrees.
func resolveGitCommonDir(projectDir, gitPath string, isDir bool) (string, bool) {
	gitDir := gitPath
	if !isDir {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return "", false
		}
		firstLine, _, _ := strings.Cut(string(data), "\n")
		target, ok := strings.CutPrefix(strings.TrimSpace(firstLine), "gitdir:")
		if !ok {
			return "", false
		}
		gitDir = strings.TrimSpace(target)
		if gitDir == "" {
			return "", false
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(projectDir, gitDir)
		}
	}

	// Linked worktrees keep per-worktree state in .git/worktrees/<name> but
	// share info/exclude via the common dir recorded in "commondir".
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			gitDir = common
		}
	}

	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return filepath.Clean(gitDir), true
}

// globalIgnoreCoversBV reports whether the user's global gitignore already
// covers the .bv directory. Best-effort: any unreadable/absent file simply
// does not match.
func globalIgnoreCoversBV() bool {
	for _, path := range globalIgnorePaths() {
		if present, err := ignoreFileCoversBV(path); err == nil && present {
			return true
		}
	}
	return false
}

// globalIgnorePaths returns candidate paths for the user's global gitignore:
// core.excludesFile if configured in the global git config, otherwise git's
// default location ($XDG_CONFIG_HOME/git/ignore, with ~/.config as the
// XDG_CONFIG_HOME fallback).
func globalIgnorePaths() []string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}

	// Global config files in ascending precedence (later wins), mirroring
	// git: $XDG_CONFIG_HOME/git/config, then ~/.gitconfig. GIT_CONFIG_GLOBAL
	// overrides both.
	var configs []string
	if override := os.Getenv("GIT_CONFIG_GLOBAL"); override != "" {
		configs = []string{override}
	} else {
		if xdg != "" {
			configs = append(configs, filepath.Join(xdg, "git", "config"))
		}
		if home != "" {
			configs = append(configs, filepath.Join(home, ".gitconfig"))
		}
	}

	excludesFile := ""
	for _, cfg := range configs {
		if v, ok := parseExcludesFile(cfg); ok {
			excludesFile = v // later config wins, like git
		}
	}
	if excludesFile != "" {
		return []string{expandUserPath(excludesFile, home)}
	}

	if xdg == "" {
		return nil
	}
	return []string{filepath.Join(xdg, "git", "ignore")}
}

// parseExcludesFile scans a git config file for core.excludesFile. It is a
// deliberately minimal INI reader (sections + key = value); it does not
// implement include directives or conditional sections. Best-effort by
// design — a miss just means the default global ignore path is checked.
func parseExcludesFile(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	value := ""
	found := false
	inCore := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inCore = strings.EqualFold(line, "[core]")
			continue
		}
		if !inCore {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "excludesfile") {
			continue
		}
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"`)
		if val != "" {
			value = val
			found = true // last occurrence wins, like git
		}
	}
	return value, found
}

// expandUserPath expands a leading "~/" to the user's home directory, the way
// git treats core.excludesFile values.
func expandUserPath(path, home string) string {
	if strings.HasPrefix(path, "~/") && home != "" {
		return filepath.Join(home, path[2:])
	}
	return path
}

// ignoreFileCoversBV checks if .bv is already covered by the given
// gitignore-syntax file. It returns true if any of these patterns are found:
//   - .bv
//   - .bv/
//   - .bv/*
//   - .bv/**
func ignoreFileCoversBV(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Check for patterns that would cover .bv/
		if matchesBVPattern(line) {
			return true, nil
		}
	}

	return false, scanner.Err()
}

// matchesBVPattern checks if a gitignore line covers the .bv directory.
func matchesBVPattern(line string) bool {
	// Normalize: remove leading/trailing slashes for comparison
	normalized := strings.TrimPrefix(line, "/")

	// Exact matches for .bv directory
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

// appendToIgnoreFile appends a pattern to a gitignore-syntax file.
// It creates the file if it doesn't exist.
// It ensures there's a newline before the pattern if the file doesn't end with one.
func appendToIgnoreFile(path string, pattern string) error {
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

	_, err = file.WriteString(toWrite)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
