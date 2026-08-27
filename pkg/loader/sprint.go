package loader

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// SprintsFileName is the canonical filename for sprint storage.
const SprintsFileName = "sprints.jsonl"

// LoadSprints reads sprints from the beads directory's sprints.jsonl.
// Respects BEADS_DB, BEADS_DIR env vars and git worktree detection via GetBeadsDir.
// Missing file is treated as "no sprints" (empty slice, nil error).
func LoadSprints(repoPath string) ([]model.Sprint, error) {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}

	sprintsPath := filepath.Join(beadsDir, SprintsFileName)
	return LoadSprintsFromFile(sprintsPath)
}

// LoadSprintsFromFile reads sprints directly from a specific JSONL file path.
// Missing file is treated as "no sprints" (empty slice, nil error).
func LoadSprintsFromFile(path string) ([]model.Sprint, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []model.Sprint{}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open sprints file: %w", err)
	}
	defer file.Close()

	return ParseSprints(file)
}

// ParseSprints parses JSONL content from a reader into sprints.
// Malformed or invalid sprints are skipped with warnings written to stderr,
// consistent with ParseIssues behavior (suppressed in robot mode).
func ParseSprints(r io.Reader) ([]model.Sprint, error) {
	sprints := make([]model.Sprint, 0)

	warn := func(msg string) {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
	}
	if os.Getenv("BV_ROBOT") == "1" {
		warn = func(string) {}
	}

	scanner := bufio.NewScanner(r)
	// Allow reasonably sized sprint entries (keep smaller than issues).
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Strip UTF-8 BOM if present on the first line.
		if lineNum == 1 {
			line = stripBOM(line)
		}

		var sprint model.Sprint
		if err := json.Unmarshal(line, &sprint); err != nil {
			warn(fmt.Sprintf("skipping malformed sprint JSON on line %d: %v", lineNum, err))
			continue
		}
		if err := sprint.Validate(); err != nil {
			warn(fmt.Sprintf("skipping invalid sprint on line %d: %v", lineNum, err))
			continue
		}

		sprints = append(sprints, sprint)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading sprints stream: %w", err)
	}

	return sprints, nil
}

// SaveSprints writes sprints to the beads directory's sprints.jsonl.
// Respects BEADS_DB, BEADS_DIR env vars and git worktree detection via GetBeadsDir.
func SaveSprints(repoPath string, sprints []model.Sprint) error {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return err
	}

	sprintsPath := filepath.Join(beadsDir, SprintsFileName)
	return SaveSprintsToFile(sprintsPath, sprints)
}

// SaveSprintsToFile writes sprints to a specific file path.
// The write is atomic (temp file + rename) to be safe with editors and watchers.
func SaveSprintsToFile(path string, sprints []model.Sprint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tmpName := tmp.Name()
	// Track whether we've closed the file to avoid double-close
	closed := false
	cleanup := func() {
		if !closed {
			_ = tmp.Close()
			closed = true
		}
		_ = os.Remove(tmpName)
	}

	enc := json.NewEncoder(tmp)
	for _, sprint := range sprints {
		if err := enc.Encode(sprint); err != nil {
			cleanup()
			return fmt.Errorf("failed to encode sprint %s: %w", sprint.ID, err)
		}
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	closed = true

	if err := os.Rename(tmpName, path); err != nil {
		// File is already closed, just remove it
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
