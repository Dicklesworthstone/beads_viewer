package loader

import (
	"bufio"
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	json "github.com/goccy/go-json"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// BeadsDirEnvVar is the name of the environment variable for custom beads directory
const BeadsDirEnvVar = "BEADS_DIR"

// BeadsDBEnvVar is the name of the environment variable for a specific database file
// or .beads directory path. Takes priority over BEADS_DIR.
// Can point to a specific file (e.g., /path/to/.beads/beads.jsonl) or a .beads directory.
const BeadsDBEnvVar = "BEADS_DB"

// WorkspaceBackend identifies the command/data backend for the current workspace.
type WorkspaceBackend string

const (
	// BackendBR is the legacy/file-first stack used by beads_rust and older repos.
	BackendBR WorkspaceBackend = "br"
	// BackendBD is the Dolt-native steveyegge/beads stack.
	BackendBD WorkspaceBackend = "bd"
)

// WorkspaceResolution captures the resolved .beads directory and active JSONL path.
type WorkspaceResolution struct {
	Backend  WorkspaceBackend
	BeadsDir string
	JSONL    string
	Warning  string
}

// PreferredJSONLNames defines the default priority order for legacy/file-first
// stacks. Modern bd workspaces bypass this via PrepareBeadsDirForRead and use
// .beads/issues.jsonl explicitly.
var PreferredJSONLNames = []string{"beads.jsonl", "issues.jsonl", "beads.base.jsonl"}

type beadsMetadata struct {
	Backend string `json:"backend"`
}

// GetBeadsDir returns the beads directory path, with the following priority:
//  1. BEADS_DB env var (can point to a file or directory; if file, returns parent dir)
//  2. BEADS_DIR env var (used directly as the .beads directory)
//  3. .beads in the given repoPath (or cwd if empty)
//  4. .beads in the main git repository root (for worktrees)
func GetBeadsDir(repoPath string) (string, error) {
	// Check BEADS_DB environment variable first (highest priority after --db flag)
	if envDB := os.Getenv(BeadsDBEnvVar); envDB != "" {
		return resolveBeadsDB(envDB)
	}

	// Check BEADS_DIR environment variable
	if envDir := os.Getenv(BeadsDirEnvVar); envDir != "" {
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

	// Check for .beads in the given path first
	beadsDir := filepath.Join(repoPath, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		return beadsDir, nil
	}

	// If not found, check if we're in a git worktree and look in the main repo
	mainRepoRoot, err := getMainRepoRoot(repoPath)
	if err == nil && mainRepoRoot != "" && mainRepoRoot != repoPath {
		mainBeadsDir := filepath.Join(mainRepoRoot, ".beads")
		if _, err := os.Stat(mainBeadsDir); err == nil {
			return mainBeadsDir, nil
		}
	}

	// Return the original path even if .beads doesn't exist
	// (caller will handle the error)
	return beadsDir, nil
}

// resolveBeadsDB interprets a BEADS_DB value which can be either:
//   - An absolute path to a specific file (e.g., /path/to/.beads/beads.jsonl or /path/to/.beads/beads.db)
//   - An absolute path to a .beads directory
//
// If it points to a file, returns the parent directory.
// If it points to a directory, returns the directory itself.
func resolveBeadsDB(dbPath string) (string, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		// Path doesn't exist yet -- guess based on whether it looks like a file path
		if strings.HasSuffix(dbPath, ".jsonl") || strings.HasSuffix(dbPath, ".db") {
			return filepath.Dir(dbPath), nil
		}
		// Assume it's a directory
		return dbPath, nil
	}

	if info.IsDir() {
		return dbPath, nil
	}

	// It's a file -- return the parent directory
	return filepath.Dir(dbPath), nil
}

// DetectWorkspaceBackend determines whether the given .beads directory belongs
// to a modern bd (Dolt-native) workspace or a legacy/file-first stack.
func DetectWorkspaceBackend(beadsDir string) WorkspaceBackend {
	if beadsDir == "" {
		return BackendBR
	}

	// Fast path: modern beads stores Dolt data under .beads/dolt/.
	if info, err := os.Stat(filepath.Join(beadsDir, "dolt")); err == nil && info.IsDir() {
		return BackendBD
	}

	// Fallback: metadata.json may explicitly record the backend.
	metaPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return BackendBR
	}

	var meta beadsMetadata
	if err := stdjson.Unmarshal(data, &meta); err != nil {
		return BackendBR
	}
	if strings.EqualFold(strings.TrimSpace(meta.Backend), "dolt") {
		return BackendBD
	}

	return BackendBR
}

// PrepareWorkspaceForRead resolves the active JSONL file for the workspace.
// For bd workspaces, it can refresh .beads/issues.jsonl by running
// `bd export -o .beads/issues.jsonl` before reading.
func PrepareWorkspaceForRead(repoPath string, refreshBDExport bool, warnFunc func(string)) (*WorkspaceResolution, error) {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}
	return PrepareBeadsDirForRead(beadsDir, refreshBDExport, warnFunc)
}

// PrepareBeadsDirForRead resolves the active JSONL file for an explicit .beads
// directory. This is used by workspace loading where the beads path is already
// known and may not be discoverable from cwd/BEADS_DIR alone.
func PrepareBeadsDirForRead(beadsDir string, refreshBDExport bool, warnFunc func(string)) (*WorkspaceResolution, error) {
	resolution := &WorkspaceResolution{
		Backend:  DetectWorkspaceBackend(beadsDir),
		BeadsDir: beadsDir,
	}

	if resolution.Backend == BackendBD {
		issuesPath := filepath.Join(beadsDir, "issues.jsonl")
		if refreshBDExport {
			if err := exportBDIssuesJSONL(beadsDir, issuesPath); err != nil {
				if _, statErr := os.Stat(issuesPath); statErr == nil {
					resolution.Warning = fmt.Sprintf("bd export failed, using existing issues.jsonl: %v", err)
					if warnFunc != nil {
						warnFunc(resolution.Warning)
					}
				} else {
					return nil, fmt.Errorf("failed to refresh bd compatibility JSONL: %w", err)
				}
			}
		}

		if _, err := os.Stat(issuesPath); err != nil {
			return nil, fmt.Errorf("no compatibility JSONL found at %s; run 'bd export -o .beads/issues.jsonl'", issuesPath)
		}

		resolution.JSONL = issuesPath
		return resolution, nil
	}

	jsonlPath, err := FindJSONLPath(beadsDir)
	if err != nil {
		return nil, err
	}
	resolution.JSONL = jsonlPath
	return resolution, nil
}

func exportBDIssuesJSONL(beadsDir, issuesPath string) error {
	if _, err := exec.LookPath("bd"); err != nil {
		return fmt.Errorf("bd binary not found in PATH")
	}

	repoRoot := filepath.Dir(beadsDir)
	cmd := exec.Command("bd", "export", "-o", issuesPath)
	cmd.Dir = repoRoot
	// Force the command to operate against the resolved .beads directory.
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s", BeadsDirEnvVar, beadsDir))
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// getMainRepoRoot returns the root directory of the main git repository.
// For regular repos, this returns the repo root.
// For worktrees, this returns the main repository root (not the worktree root).
func getMainRepoRoot(repoPath string) (string, error) {
	// First, check if we're in a git repository at all
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = repoPath
	topLevelOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	worktreeRoot := strings.TrimSpace(string(topLevelOut))

	// Check if this is a worktree by looking at the git-common-dir
	// For regular repos: git-common-dir == git-dir
	// For worktrees: git-common-dir points to main repo's .git
	cmd = exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = repoPath
	commonDirOut, err := cmd.Output()
	if err != nil {
		// Fallback: not a worktree or old git version
		return worktreeRoot, nil
	}
	commonDir := strings.TrimSpace(string(commonDirOut))

	cmd = exec.Command("git", "rev-parse", "--path-format=absolute", "--git-dir")
	cmd.Dir = repoPath
	gitDirOut, err := cmd.Output()
	if err != nil {
		return worktreeRoot, nil
	}
	gitDir := strings.TrimSpace(string(gitDirOut))

	// If git-common-dir == git-dir, we're in a regular repo
	if commonDir == gitDir {
		return worktreeRoot, nil
	}

	// We're in a worktree. The main repo root is the parent of git-common-dir.
	// git-common-dir typically points to /path/to/main-repo/.git
	mainRepoRoot := filepath.Dir(commonDir)

	return mainRepoRoot, nil
}

// FindJSONLPath locates the active beads JSONL file in the given directory.
// Legacy/file-first stacks prefer beads.jsonl, then issues.jsonl, then
// beads.base.jsonl. Modern bd interop bypasses this via PrepareBeadsDirForRead.
// Skips backup files and merge artifacts.
func FindJSONLPath(beadsDir string) (string, error) {
	return FindJSONLPathWithWarnings(beadsDir, nil)
}

// FindJSONLPathWithWarnings is like FindJSONLPath but optionally reports warnings
// about detected merge artifacts via the provided callback.
func FindJSONLPathWithWarnings(beadsDir string, warnFunc func(msg string)) (string, error) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read beads directory: %w", err)
	}

	var candidates []string
	var mergeArtifacts []string
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

		// Skip git merge conflict artifacts (beads.left.jsonl, beads.right.jsonl)
		// These are OURS/THEIRS sides during a merge conflict
		if strings.HasPrefix(name, "beads.left") || strings.HasPrefix(name, "beads.right") {
			mergeArtifacts = append(mergeArtifacts, name)
			continue
		}

		candidates = append(candidates, name)
	}

	// Warn about detected merge artifacts
	if len(mergeArtifacts) > 0 && warnFunc != nil {
		warnFunc(fmt.Sprintf("Merge artifact files detected: %s. Clean them up before relying on the JSONL view.",
			strings.Join(mergeArtifacts, ", ")))
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no beads JSONL file found in %s", beadsDir)
	}

	// Priority order for beads files:
	// 1. issues.jsonl (canonical for current bd export interop)
	// 2. beads.jsonl (legacy fallback)
	// 3. beads.base.jsonl (fallback, may be present during merge resolution)
	// 4. First candidate
	preferredNames := PreferredJSONLNames

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

// LoadIssues reads issues from the beads directory.
// Respects BEADS_DIR environment variable, otherwise uses .beads in repoPath.
// Automatically finds the correct JSONL file (issues.jsonl preferred, beads.jsonl fallback).
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

// DefaultMaxBufferSize is the default buffer size for the scanner (10MB).
const DefaultMaxBufferSize = 1024 * 1024 * 10

// ParseOptions configures the behavior of ParseIssues.
type ParseOptions struct {
	// WarningHandler is called with warning messages (e.g., malformed JSON).
	// If nil, warnings are printed to os.Stderr.
	WarningHandler func(string)

	// BufferSize sets the maximum line size (in bytes) to read at once.
	// Lines longer than this are skipped with a warning.
	// If 0, uses DefaultMaxBufferSize (10MB).
	BufferSize int

	// IssueFilter optionally filters parsed issues. Return true to include.
	// When nil, all valid issues are included.
	IssueFilter func(*model.Issue) bool
}

// LoadIssuesFromFileWithOptions reads issues from a file with custom options.
func LoadIssuesFromFileWithOptions(path string, opts ParseOptions) ([]model.Issue, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no beads issues found at %s", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open issues file: %w", err)
	}
	defer file.Close()

	return ParseIssuesWithOptions(file, opts)
}

// LoadIssuesFromFileWithOptionsPooled reads issues from a file with pooling enabled.
// The caller must return pooled issues via ReturnIssuePtrsToPool when no longer needed.
func LoadIssuesFromFileWithOptionsPooled(path string, opts ParseOptions) (PooledIssues, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return PooledIssues{}, fmt.Errorf("no beads issues found at %s", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return PooledIssues{}, fmt.Errorf("failed to open issues file: %w", err)
	}
	defer file.Close()

	return ParseIssuesWithOptionsPooled(file, opts)
}

// LoadIssuesFromFile reads issues directly from a specific JSONL file path.
func LoadIssuesFromFile(path string) ([]model.Issue, error) {
	return LoadIssuesFromFileWithOptions(path, ParseOptions{})
}

// LoadIssuesFromFilePooled reads issues directly from a JSONL file path with pooling enabled.
func LoadIssuesFromFilePooled(path string) (PooledIssues, error) {
	return LoadIssuesFromFileWithOptionsPooled(path, ParseOptions{})
}

// ParseIssues parses JSONL content from a reader into issues.
// Handles UTF-8 BOM stripping, large lines, and validation.
func ParseIssues(r io.Reader) ([]model.Issue, error) {
	return ParseIssuesWithOptions(r, ParseOptions{})
}

// ParseIssuesWithOptions parses JSONL content with custom options.
func ParseIssuesWithOptions(r io.Reader, opts ParseOptions) ([]model.Issue, error) {
	issues, _, err := parseIssuesWithOptions(r, opts, false)
	return issues, err
}

// ParseIssuesWithOptionsPooled parses JSONL content with pooling enabled.
// The caller must return pooled issues via ReturnIssuePtrsToPool when no longer needed.
func ParseIssuesWithOptionsPooled(r io.Reader, opts ParseOptions) (PooledIssues, error) {
	issues, poolRefs, err := parseIssuesWithOptions(r, opts, true)
	if err != nil {
		return PooledIssues{}, err
	}
	return PooledIssues{Issues: issues, PoolRefs: poolRefs}, nil
}

func parseIssuesWithOptions(r io.Reader, opts ParseOptions, usePool bool) ([]model.Issue, []*model.Issue, error) {
	var issues []model.Issue
	var poolRefs []*model.Issue
	if f, ok := r.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			// Heuristic: average issue line ~2KB. Prefer conservative underestimation to
			// avoid large over-allocations for big files.
			const avgIssueBytes = 2 * 1024
			const minCap = 64
			const maxCap = 200_000

			est := int(info.Size() / avgIssueBytes)
			if est < minCap && info.Size() > 0 {
				est = minCap
			}
			if est > maxCap {
				est = maxCap
			}
			if est > 0 {
				issues = make([]model.Issue, 0, est)
				if usePool {
					poolRefs = make([]*model.Issue, 0, est)
				}
			}
		}
	}

	// Determine buffer size
	maxCapacity := opts.BufferSize
	if maxCapacity <= 0 {
		maxCapacity = DefaultMaxBufferSize
	}

	reader := bufio.NewReaderSize(r, maxCapacity)

	// Default warning handler prints to stderr (suppressed in robot mode).
	warn := opts.WarningHandler
	if warn == nil {
		if os.Getenv("BV_ROBOT") == "1" {
			warn = func(string) {}
		} else {
			warn = func(msg string) {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
			}
		}
	}

	lineNum := 0
	for {
		lineNum++
		// ReadLine returns a single line, not including the end-of-line bytes.
		// If the line was too long for the buffer then isPrefix is set and the
		// beginning of the line is returned.
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			if usePool {
				ReturnIssuePtrsToPool(poolRefs)
			}
			return nil, nil, fmt.Errorf("error reading issues stream at line %d: %w", lineNum, err)
		}

		if isPrefix {
			// Line too long. Discard the rest of the line.
			warn(fmt.Sprintf("skipping line %d: line too long (exceeds %d bytes)", lineNum, maxCapacity))
			for isPrefix {
				_, isPrefix, err = reader.ReadLine()
				if err != nil && err != io.EOF {
					if usePool {
						ReturnIssuePtrsToPool(poolRefs)
					}
					return nil, nil, fmt.Errorf("error skipping long line at line %d: %w", lineNum, err)
				}
				if err == io.EOF {
					break
				}
			}
			continue
		}

		if len(line) == 0 {
			continue
		}

		// Strip UTF-8 BOM if present on the first line
		if lineNum == 1 {
			line = stripBOM(line)
		}

		if usePool {
			issue := GetIssue()
			if err := json.Unmarshal(line, issue); err != nil {
				PutIssue(issue)
				// Skip malformed lines but warn
				warn(fmt.Sprintf("skipping malformed JSON on line %d: %v", lineNum, err))
				continue
			}

			issue.Status = normalizeIssueStatus(issue.Status)

			// Validate issue
			if err := issue.Validate(); err != nil {
				PutIssue(issue)
				// Skip invalid issues
				warn(fmt.Sprintf("skipping invalid issue on line %d: %v", lineNum, err))
				continue
			}

			if opts.IssueFilter != nil && !opts.IssueFilter(issue) {
				PutIssue(issue)
				continue
			}

			issues = append(issues, *issue)
			poolRefs = append(poolRefs, issue)
		} else {
			var issue model.Issue
			if err := json.Unmarshal(line, &issue); err != nil {
				// Skip malformed lines but warn
				warn(fmt.Sprintf("skipping malformed JSON on line %d: %v", lineNum, err))
				continue
			}

			issue.Status = normalizeIssueStatus(issue.Status)

			// Validate issue
			if err := issue.Validate(); err != nil {
				// Skip invalid issues
				warn(fmt.Sprintf("skipping invalid issue on line %d: %v", lineNum, err))
				continue
			}

			if opts.IssueFilter != nil && !opts.IssueFilter(&issue) {
				continue
			}

			issues = append(issues, issue)
		}
	}

	return issues, poolRefs, nil
}

// stripBOM removes the UTF-8 Byte Order Mark if present
func stripBOM(b []byte) []byte {
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		return b[3:]
	}
	return b
}

func normalizeIssueStatus(status model.Status) model.Status {
	trimmed := strings.TrimSpace(string(status))
	if trimmed == "" {
		return ""
	}
	return model.Status(strings.ToLower(trimmed))
}
