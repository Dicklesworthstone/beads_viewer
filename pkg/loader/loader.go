package loader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// FindJSONLPath locates the beads JSONL file in the given directory.
// Prefers beads.jsonl (canonical) over issues.jsonl (legacy fallback).
// Skips backup files and merge artifacts.
func FindJSONLPath(beadsDir string) (string, error) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read beads directory: %w", err)
	}

	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		// Must be a .jsonl file
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		// Skip backups, merge artifacts, and deletion manifests
		if strings.Contains(name, ".backup") ||
			strings.Contains(name, ".orig") ||
			strings.Contains(name, ".merge") ||
			name == "deletions.jsonl" {
			continue
		}

		candidates = append(candidates, name)
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no beads JSONL file found in %s", beadsDir)
	}

	// Priority order for beads files:
	// 1. beads.jsonl (canonical)
	// 2. beads.base.jsonl (bd's primary storage)
	// 3. issues.jsonl (legacy)
	// 4. First candidate
	preferredNames := []string{"beads.jsonl", "beads.base.jsonl", "issues.jsonl"}

	for _, preferred := range preferredNames {
		for _, name := range candidates {
			if name == preferred {
				path := filepath.Join(beadsDir, name)
				// Check if file has content (skip empty files)
				if info, err := os.Stat(path); err == nil && info.Size() > 0 {
					return path, nil
				}
			}
		}
	}

	// Fall back to first non-empty candidate
	for _, name := range candidates {
		path := filepath.Join(beadsDir, name)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, nil
		}
	}

	// Last resort: return first candidate even if empty
	return filepath.Join(beadsDir, candidates[0]), nil
}

// GetBeadsDir returns the beads directory path, respecting the BEADS_DIR environment variable.
// If BEADS_DIR is set, it is used directly.
// Otherwise, falls back to .beads in the given repoPath (or cwd if repoPath is empty).
func GetBeadsDir(repoPath string) (string, error) {
	// Check BEADS_DIR environment variable first
	if envDir := os.Getenv("BEADS_DIR"); envDir != "" {
		return envDir, nil
	}

	// Fall back to .beads in repo path
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current working directory: %w", err)
		}
	}

	return filepath.Join(repoPath, ".beads"), nil
}

// LoadIssues reads issues from the .beads directory in the given repository path.
// Automatically finds the correct JSONL file (beads.jsonl preferred, issues.jsonl fallback).
// Respects the BEADS_DIR environment variable if set.
func LoadIssues(repoPath string) ([]model.Issue, error) {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}

	jsonlPath, err := FindJSONLPath(beadsDir)
	if err != nil {
		return nil, err
	}

	return LoadIssuesFromFile(jsonlPath)
}

// LoadIssuesFromFile reads issues directly from a specific JSONL file path.
func LoadIssuesFromFile(path string) ([]model.Issue, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no beads issues found at %s", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open issues file: %w", err)
	}
	defer file.Close()

	var issues []model.Issue
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines (issues can be large)
	const maxCapacity = 1024 * 1024 * 10 // 10MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Strip UTF-8 BOM if present on the first line
		if lineNum == 1 {
			line = stripBOM(line)
		}

		var issue model.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			// Skip malformed lines but continue loading the rest
			continue
		}

		// Validate issue
		if err := issue.Validate(); err != nil {
			// Skip invalid issues
			fmt.Fprintf(os.Stderr, "Warning: skipping invalid issue on line %d: %v\n", lineNum, err)
			continue
		}

		issues = append(issues, issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading issues file: %w", err)
	}

	return issues, nil
}

// stripBOM removes the UTF-8 Byte Order Mark if present
func stripBOM(b []byte) []byte {
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		return b[3:]
	}
	return b
}
