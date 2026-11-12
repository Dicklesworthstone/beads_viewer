// Package correlation provides extraction of co-committed files for bead correlation.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// renamePattern matches git's brace notation for renames: {old => new}
var renamePattern = regexp.MustCompile(`\{[^}]* => ([^}]*)\}`)

// coCommitFetchedSHAsCounter counts the number of commit SHAs that primeBatch
// actually fetched from git (via the batched `git log` passes) across the
// process — i.e. SHAs that missed BOTH the in-memory and the persistent
// per-commit co-commit cache. It exists solely so tests can prove the incremental
// cache fetches only the NEW commits' co-commit data (and 0 when nothing is new).
// It is never read on any production path.
var coCommitFetchedSHAsCounter int64

// CoCommitExtractor extracts files that were changed in the same commit as bead changes
type CoCommitExtractor struct {
	repoPath string

	// ctx, when set (via Correlator.WithContext or directly), bounds the git
	// subprocesses spawned during co-commit extraction (issue #166). nil means
	// context.Background().
	ctx context.Context

	// Memoized per-commit diff data, populated lazily by primeBatch. Once a SHA
	// is present in batchedSHAs, getFilesChanged/getLineStats serve it from these
	// maps instead of forking a per-commit `git show`. See #161 batch fan-out fix.
	fileCache   map[string][]FileChange
	statCache   map[string]map[string]lineStats
	batchedSHAs map[string]struct{}
}

// NewCoCommitExtractor creates a new co-commit extractor
func NewCoCommitExtractor(repoPath string) *CoCommitExtractor {
	return &CoCommitExtractor{repoPath: repoPath}
}

// codeFileExtensions lists file extensions considered "code files"
var codeFileExtensions = map[string]bool{
	".go":      true,
	".py":      true,
	".js":      true,
	".ts":      true,
	".jsx":     true,
	".tsx":     true,
	".rs":      true,
	".java":    true,
	".kt":      true,
	".swift":   true,
	".c":       true,
	".cpp":     true,
	".h":       true,
	".hpp":     true,
	".rb":      true,
	".php":     true,
	".cs":      true,
	".scala":   true,
	".yaml":    true,
	".yml":     true,
	".json":    true,
	".toml":    true,
	".md":      true,
	".sql":     true,
	".sh":      true,
	".bash":    true,
	".zsh":     true,
	".html":    true,
	".htm":     true,
	".css":     true,
	".scss":    true,
	".sass":    true,
	".less":    true,
	".svelte":  true,
	".vue":     true,
	".astro":   true,
	".proto":   true,
	".graphql": true,
	".gql":     true,
	".tf":      true,
	".hcl":     true,
	".zig":     true,
	".wat":     true,
	".wit":     true,
	".nix":     true,
	".ps1":     true,
	".psm1":    true,
	".bat":     true,
	".cmd":     true,
	".cmake":   true,
}

// codeFileBasenames lists exact basenames considered code/build/manifest files
// even when they lack a recognized extension.
var codeFileBasenames = map[string]bool{
	"Makefile":       true,
	"GNUmakefile":    true,
	"Dockerfile":     true,
	"Containerfile":  true,
	"Justfile":       true,
	"Vagrantfile":    true,
	"Tiltfile":       true,
	"BUILD":          true,
	"BUILD.bazel":    true,
	"WORKSPACE":      true,
	"flake.nix":      true,
	"go.mod":         true,
	"Cargo.toml":     true,
	"package.json":   true,
	"pyproject.toml": true,
}

// excludedPaths lists path prefixes that should be excluded
var excludedPaths = []string{
	".beads/",
	".bv/",
	".git/",
	"node_modules/",
	"vendor/",
	"__pycache__/",
	".venv/",
	"venv/",
	"dist/",
	"build/",
	".next/",
}

// ExtractCoCommittedFiles extracts code files changed in the same commit as a bead event
func (c *CoCommitExtractor) ExtractCoCommittedFiles(event BeadEvent) ([]FileChange, error) {
	// Get file list with status
	files, err := c.getFilesChanged(event.CommitSHA)
	if err != nil {
		return nil, err
	}

	// Get line stats
	stats, err := c.getLineStats(event.CommitSHA)
	if err != nil {
		// Non-fatal: continue without stats
		stats = make(map[string]lineStats)
	}

	// Filter to code files only
	var codeFiles []FileChange
	for _, f := range files {
		if !isCodeFile(f.Path) {
			continue
		}
		if isExcludedPath(f.Path) {
			continue
		}

		// Add line stats if available
		if s, ok := stats[f.Path]; ok {
			f.Insertions = s.insertions
			f.Deletions = s.deletions
		}

		codeFiles = append(codeFiles, f)
	}

	return codeFiles, nil
}

// ExtractNonExcludedFiles extracts all non-excluded repository files changed in
// commitSHA regardless of file extension (e.g. .gitignore, docs, config files).
// Used by explicit_id correlation so commits explicitly naming a bead ID are
// never dropped solely because they touched non-code repository files.
func (c *CoCommitExtractor) ExtractNonExcludedFiles(commitSHA string) ([]FileChange, error) {
	files, err := c.getFilesChanged(commitSHA)
	if err != nil {
		return nil, err
	}

	stats, err := c.getLineStats(commitSHA)
	if err != nil {
		stats = make(map[string]lineStats)
	}

	var repoFiles []FileChange
	for _, f := range files {
		if isExcludedPath(f.Path) {
			continue
		}
		if s, ok := stats[f.Path]; ok {
			f.Insertions = s.insertions
			f.Deletions = s.deletions
		}
		repoFiles = append(repoFiles, f)
	}

	return repoFiles, nil
}

// CreateCorrelatedCommit creates a CorrelatedCommit with confidence scoring
func (c *CoCommitExtractor) CreateCorrelatedCommit(event BeadEvent, files []FileChange) CorrelatedCommit {
	confidence := c.calculateConfidence(event, files)
	reason := c.generateReason(event, files, confidence)

	return CorrelatedCommit{
		BeadID:      event.BeadID,
		SHA:         event.CommitSHA,
		ShortSHA:    shortSHA(event.CommitSHA),
		Message:     event.CommitMsg,
		Author:      event.Author,
		AuthorEmail: event.AuthorEmail,
		Timestamp:   event.Timestamp,
		Files:       files,
		Method:      MethodCoCommitted,
		Confidence:  confidence,
		Reason:      reason,
	}
}

// lineStats holds insertion/deletion counts for a file
type lineStats struct {
	insertions int
	deletions  int
}

// excludePathspecArgs builds git pathspec arguments that exclude the directories
// in excludedPaths. These are appended after a "--" separator so git skips diffing
// the (often large) excluded blobs (e.g. .beads/issues.jsonl) entirely instead of
// computing line stats for content the caller discards via isExcludedPath. See #160.
func excludePathspecArgs() []string {
	args := make([]string, 0, len(excludedPaths)+2)
	args = append(args, "--", ".")
	for _, prefix := range excludedPaths {
		// Trim trailing slash; ':(exclude,glob)dir/**' matches everything under dir.
		dir := strings.TrimSuffix(prefix, "/")
		args = append(args, fmt.Sprintf(":(exclude,glob)%s/**", dir))
	}
	return args
}

// primeBatch fetches name-status and numstat for every requested SHA in two
// batched `git log` invocations and memoizes the result, so subsequent
// getFilesChanged/getLineStats calls for those SHAs are served from memory
// instead of forking one `git show` per commit. SHAs already batched are
// skipped, keeping the call idempotent.
//
// We use `git log --no-walk=unsorted <SHAs>` rather than N×`git show`: a single
// process streams each commit's first-parent diff (exactly what `git show`
// computes) for the whole set. Two passes are required because git's
// --name-status and --numstat are mutually exclusive in one invocation (the last
// flag wins); the status letters live in one pass and the +/- line counts in the
// other, matching the two existing parsers byte-for-byte. The same exclude
// pathspecs are applied, so a commit whose diff is empty under the pathspec is
// omitted from the stream — identical to `git show` printing nothing (verified)
// and yielding an empty file list for that SHA. Either git-pass failure is
// returned before any missing SHA is marked complete or cached.
func (c *CoCommitExtractor) primeBatch(shas []string) error {
	if c.fileCache == nil {
		c.fileCache = make(map[string][]FileChange)
		c.statCache = make(map[string]map[string]lineStats)
		c.batchedSHAs = make(map[string]struct{})
	}

	want := make([]string, 0, len(shas))
	wantSet := make(map[string]struct{}, len(shas))
	for _, sha := range shas {
		if sha == "" {
			continue
		}
		if _, done := c.batchedSHAs[sha]; done {
			continue
		}
		if _, queued := wantSet[sha]; queued {
			continue
		}
		wantSet[sha] = struct{}{}
		want = append(want, sha)
	}
	if len(want) == 0 {
		return nil
	}

	// Persistent layer: a commit's (files, lineStats) is a pure function of
	// (SHA, exclude-pathspec set) — see per_commit_cocommit_cache.go. Serve
	// SHAs already on disk straight into the in-memory maps and run the batched
	// `git log` ONLY for SHAs missing from both memory and disk. A disk hit
	// populates fileCache/statCache identically to the git path: Files in git
	// (name-status) order, LineStats reconstructed exactly via fromLineStatsMap,
	// and an empty diff memoized as nil files + empty stat map. So the in-memory
	// maps are byte-identical regardless of how many SHAs came from disk.
	namespace := perCommitCoCommitCacheNamespace()
	disk := loadPerCommitCoCommit(namespace)

	missing := want
	if disk != nil {
		missing = make([]string, 0, len(want))
		for _, sha := range want {
			if e, ok := disk[sha]; ok {
				c.fileCache[sha] = e.Files
				c.statCache[sha] = fromLineStatsMap(e.LineStats)
				c.batchedSHAs[sha] = struct{}{}
				continue
			}
			missing = append(missing, sha)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	atomic.AddInt64(&coCommitFetchedSHAsCounter, int64(len(missing)))

	files, err := c.batchFilesChanged(missing)
	if err != nil {
		return fmt.Errorf("batching co-commit name-status: %w", err)
	}
	stats, err := c.batchLineStats(missing)
	if err != nil {
		return fmt.Errorf("batching co-commit numstat: %w", err)
	}

	fresh := make(map[string]perCommitCoCommitEntry, len(missing))
	now := time.Now().UTC()
	for _, sha := range missing {
		// files/stats maps only carry SHAs that appeared in the stream (i.e.
		// produced a non-empty diff under the exclude pathspecs). Absent SHAs
		// memoize as empty so future lookups hit the cache rather than re-forking
		// git — matching the legacy per-commit path, where an empty diff yields an
		// empty file list and the SHA contributes no correlated commit.
		c.fileCache[sha] = files[sha]
		s, ok := stats[sha]
		if !ok {
			s = map[string]lineStats{}
		}
		c.statCache[sha] = s
		c.batchedSHAs[sha] = struct{}{}
		fresh[sha] = perCommitCoCommitEntry{
			CreatedAt: now,
			Files:     files[sha],
			LineStats: toLineStatsMap(s),
		}
	}
	// Persist only the freshly fetched SHAs (no-rewrite-on-pure-hit: when every
	// requested SHA was already on disk, missing is empty and we returned above).
	// Both git passes succeeded before any missing SHA was memoized, so a transient
	// failure can neither masquerade as an empty diff nor poison a higher-level
	// history artifact/report cache.
	storePerCommitCoCommit(namespace, fresh)
	return nil
}

// batchLogArgs builds `git log --no-walk=unsorted <diffFlag> --format=<header>
// <SHAs> -- <exclude pathspecs>`, reusing the streaming-log header and exclude
// helpers shared with the snapshot extractor.
func batchLogArgs(diffFlag string, shas []string) []string {
	args := make([]string, 0, len(shas)+8)
	args = append(args, "log", "--no-walk=unsorted", diffFlag, "--format="+gitLogHeaderFormat)
	args = append(args, shas...)
	args = append(args, excludePathspecArgs()...)
	return args
}

// batchFilesChanged runs one `git log --name-status` over all SHAs and returns
// per-SHA FileChange lists using the same parsing as getFilesChanged. It
// returns an error (not just an empty map) on git failure so
// the caller can avoid persisting a poisoned "empty diff" entry for SHAs that
// were never actually inspected. A successful run with a genuinely-empty diff for
// some SHA still returns no error (the empty result is correct and cacheable).
func (c *CoCommitExtractor) batchFilesChanged(shas []string) (map[string][]FileChange, error) {
	files := make(map[string][]FileChange, len(shas))

	cmd := gitCommand(c.ctx, withNoColorGit(batchLogArgs("--name-status", shas))...)
	cmd.Dir = c.repoPath
	out, err := cmd.Output()
	if err != nil {
		return files, err
	}

	c.forEachCommitChunk(out, func(sha string, payload []byte) {
		files[sha] = parseNameStatus(payload)
	})
	return files, nil
}

// batchLineStats runs one `git log --numstat` over all SHAs and returns per-SHA
// line-stat maps using the same parsing as getLineStats. Like batchFilesChanged,
// a git failure is surfaced as an error so the caller skips persisting empties.
func (c *CoCommitExtractor) batchLineStats(shas []string) (map[string]map[string]lineStats, error) {
	stats := make(map[string]map[string]lineStats, len(shas))

	cmd := gitCommand(c.ctx, withNoColorGit(batchLogArgs("--numstat", shas))...)
	cmd.Dir = c.repoPath
	out, err := cmd.Output()
	if err != nil {
		return stats, err
	}

	c.forEachCommitChunk(out, func(sha string, payload []byte) {
		stats[sha] = parseNumstat(payload)
	})
	return stats, nil
}

// forEachCommitChunk splits a `git log --format=<header>` stream into per-commit
// chunks (the same boundary detection parseSnapshotLog uses) and invokes fn with
// the commit SHA and the diff payload that follows its header line.
func (c *CoCommitExtractor) forEachCommitChunk(out []byte, fn func(sha string, payload []byte)) {
	locs := commitPattern.FindAllIndex(out, -1)
	for i, loc := range locs {
		start := loc[0]
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := out[start:end]

		nl := bytes.IndexByte(chunk, '\n')
		if nl < 0 {
			continue
		}
		// The header is %H<NUL>... ; the SHA is the leading 40 hex chars.
		header := chunk[:nl]
		z := bytes.IndexByte(header, 0)
		if z < 0 {
			continue
		}
		sha := string(header[:z])
		fn(sha, chunk[nl+1:])
	}
}

// parseNameStatus parses git name-status payload lines into FileChange entries.
// Shared by getFilesChanged (per-commit `git show`) and the batched path.
func parseNameStatus(payload []byte) []FileChange {
	var files []FileChange
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64*1024), gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Format: "M\tpath/to/file" or "R100\told\tnew"
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		action := parts[0]
		path := parts[1]

		// Handle renames: R100\told\tnew
		if len(parts) == 3 && strings.HasPrefix(action, "R") {
			path = parts[2] // Use new name
			action = "R"
		}

		// Normalize action to single char
		if len(action) > 1 {
			action = string(action[0])
		}

		files = append(files, FileChange{
			Path:   path,
			Action: action,
		})
	}
	return files
}

// parseNumstat parses git numstat payload lines into a per-path lineStats map.
// Shared by getLineStats (per-commit `git show`) and the batched path.
func parseNumstat(payload []byte) map[string]lineStats {
	stats := make(map[string]lineStats)
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64*1024), gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Format: "42\t10\tpath/to/file" or "-\t-\tbinary/file"
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}

		insertions := 0
		deletions := 0

		// Binary files show "-" instead of numbers
		if parts[0] != "-" {
			insertions, _ = strconv.Atoi(parts[0])
		}
		if parts[1] != "-" {
			deletions, _ = strconv.Atoi(parts[1])
		}

		// Handle renames: path might be "old => new" format
		path := parts[2]
		if strings.Contains(path, " => ") {
			// Extract new path from "old => new" or "{old => new}" format
			path = extractNewPath(path)
		}

		stats[path] = lineStats{
			insertions: insertions,
			deletions:  deletions,
		}
	}
	return stats
}

// getFilesChanged returns the name-status file list for a commit. When the SHA
// was primed via primeBatch it is served from the in-memory cache; otherwise it
// falls back to a per-commit `git show --name-status`.
func (c *CoCommitExtractor) getFilesChanged(sha string) ([]FileChange, error) {
	if c.fileCache != nil {
		if _, ok := c.batchedSHAs[sha]; ok {
			return c.fileCache[sha], nil
		}
	}

	gitArgs := append([]string{"show", "--name-status", "--format=", sha}, excludePathspecArgs()...)
	cmd := gitCommand(c.ctx, gitArgs...)
	cmd.Dir = c.repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show --name-status failed: %w", err)
	}

	return parseNameStatus(out), nil
}

// getLineStats returns insertion/deletion counts per file for a commit. When the
// SHA was primed via primeBatch it is served from the in-memory cache; otherwise
// it falls back to a per-commit `git show --numstat`.
func (c *CoCommitExtractor) getLineStats(sha string) (map[string]lineStats, error) {
	if c.statCache != nil {
		if _, ok := c.batchedSHAs[sha]; ok {
			return c.statCache[sha], nil
		}
	}

	gitArgs := append([]string{"show", "--numstat", "--format=", sha}, excludePathspecArgs()...)
	cmd := gitCommand(c.ctx, gitArgs...)
	cmd.Dir = c.repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show --numstat failed: %w", err)
	}

	return parseNumstat(out), nil
}

// extractNewPath handles git's rename notation in numstat output
func extractNewPath(path string) string {
	// Handle "{prefix/}{old => new}{/suffix}" format
	if strings.Contains(path, "{") {
		// Complex case: "pkg/{old => new}/file.go"
		path = renamePattern.ReplaceAllString(path, "$1")
		// Fix potential double slashes if a segment was removed (e.g. "{old => }")
		return strings.ReplaceAll(path, "//", "/")
	}

	// Simple case: "old => new"
	if idx := strings.Index(path, " => "); idx != -1 {
		return path[idx+4:]
	}

	return path
}

// calculateConfidence computes the confidence score for a co-commit correlation
func (c *CoCommitExtractor) calculateConfidence(event BeadEvent, files []FileChange) float64 {
	// Base confidence for co-committed files
	confidence := 0.95

	// Bonus: commit message mentions bead ID
	if containsBeadID(event.CommitMsg, event.BeadID) {
		confidence += 0.04
	}

	// Penalty: shotgun commit (>20 files)
	if len(files) > 20 {
		confidence -= 0.10
	}

	// Penalty: only test files
	if allTestFiles(files) {
		confidence -= 0.05
	}

	// Clamp to [0, 1]
	if confidence > 1.0 {
		confidence = 1.0
	}
	if confidence < 0.0 {
		confidence = 0.0
	}

	return confidence
}

// generateReason creates a human-readable explanation for the correlation
func (c *CoCommitExtractor) generateReason(event BeadEvent, files []FileChange, confidence float64) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Co-committed with bead status change to %s", event.EventType))

	if containsBeadID(event.CommitMsg, event.BeadID) {
		parts = append(parts, "commit message references bead ID")
	}

	if len(files) > 20 {
		parts = append(parts, fmt.Sprintf("large commit (%d files)", len(files)))
	}

	if allTestFiles(files) {
		parts = append(parts, "contains only test files")
	}

	return strings.Join(parts, "; ")
}

// isCodeFile checks if a file path is a code file based on basename or extension
func isCodeFile(path string) bool {
	// Handle git quoting (e.g. "path/with spaces.go")
	if len(path) > 2 && path[0] == '"' && path[len(path)-1] == '"' {
		// Basic unquote: strip quotes.
		// Git might use C-style escapes (e.g. \t, \n, \"), but for extension checking
		// simply stripping the surrounding quotes handles the common case of spaces.
		// For complex escapes, we accept that filepath.Ext might be imperfect,
		// but this covers 99% of "filename with space.go" cases.
		path = path[1 : len(path)-1]
	}

	normalized := strings.ReplaceAll(path, "\\", "/")
	base := filepath.Base(normalized)
	if codeFileBasenames[base] {
		return true
	}

	ext := strings.ToLower(filepath.Ext(base))
	return codeFileExtensions[ext]
}

// isExcludedPath checks if a path should be excluded
func isExcludedPath(path string) bool {
	// Check for direct prefix (fast path for root dirs)
	for _, prefix := range excludedPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	// Check for nested directories (e.g. src/node_modules/...)
	// We look for "/dirname/" in the path
	for _, prefix := range excludedPaths {
		// Only check directory exclusions (ending in /)
		if strings.HasSuffix(prefix, "/") {
			// Check for "/prefix" anywhere in path
			// We prepend / to ensure we match a directory boundary
			if strings.Contains(path, "/"+prefix) {
				return true
			}
		}
	}
	return false
}

// containsBeadID checks if text contains the bead ID
func containsBeadID(text, beadID string) bool {
	if beadID == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(beadID))
}

// allTestFiles returns true if all files are test files
func allTestFiles(files []FileChange) bool {
	if len(files) == 0 {
		return false
	}

	testPatterns := []string{"_test.go", ".test.js", ".test.ts", ".spec.js", ".spec.ts", "_test.py", "test_"}

	for _, f := range files {
		isTest := false
		lowerPath := strings.ToLower(f.Path)
		for _, pattern := range testPatterns {
			if strings.Contains(lowerPath, pattern) {
				isTest = true
				break
			}
		}
		if !isTest {
			return false
		}
	}
	return true
}

// shortSHA returns the first 7 characters of a SHA
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ExtractAllCoCommits extracts co-committed files for all events with status changes
func (c *CoCommitExtractor) ExtractAllCoCommits(events []BeadEvent) ([]CorrelatedCommit, error) {
	var commits []CorrelatedCommit
	fileCache := make(map[string][]FileChange) // Cache file lookups by SHA

	// Batch all relevant commit SHAs through a single pair of `git log` calls so
	// the per-event ExtractCoCommittedFiles below reads from memory instead of
	// forking two `git show` processes per commit (#161). Collect the same SHAs
	// the loop will actually request (status-change events only).
	batchSHAs := make([]string, 0, len(events))
	for _, event := range events {
		if event.EventType != EventClaimed && event.EventType != EventClosed {
			continue
		}
		batchSHAs = append(batchSHAs, event.CommitSHA)
	}
	if err := c.primeBatch(batchSHAs); err != nil {
		return nil, fmt.Errorf("priming co-commit batch: %w", err)
	}

	for _, event := range events {
		// Only process status change events
		if event.EventType != EventClaimed && event.EventType != EventClosed {
			continue
		}

		// Use cached files if available, otherwise fetch from git
		files, cached := fileCache[event.CommitSHA]
		if !cached {
			var err error
			files, err = c.ExtractCoCommittedFiles(event)
			if err != nil {
				return nil, fmt.Errorf("extracting co-committed files for %s: %w", event.CommitSHA, err)
			}
			fileCache[event.CommitSHA] = files
		}

		// Only create correlation if there are code files
		if len(files) == 0 {
			continue
		}

		commit := c.CreateCorrelatedCommit(event, files)
		commits = append(commits, commit)
	}

	return commits, nil
}
