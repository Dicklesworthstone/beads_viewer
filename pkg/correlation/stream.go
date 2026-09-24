// Package correlation provides streaming git history parsing for memory efficiency.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultHistoryLimit is the default maximum number of commits to process
const DefaultHistoryLimit = 500

// ProgressCallback is called during streaming operations to report progress
type ProgressCallback func(processed, total int)

// StreamExtractor provides memory-efficient streaming extraction of bead events
type StreamExtractor struct {
	repoPath   string
	beadsFiles []string
	progressCB ProgressCallback

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// the extractor (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the extractor so its git subprocesses are
// cancelled when ctx is done (issue #166). Returns the receiver for chaining.
func (s *StreamExtractor) WithContext(ctx context.Context) *StreamExtractor {
	s.ctx = ctx
	return s
}

// NewStreamExtractor creates a new streaming extractor
func NewStreamExtractor(repoPath string) *StreamExtractor {
	return &StreamExtractor{
		repoPath:   repoPath,
		beadsFiles: pickBeadsFiles(repoPath, defaultBeadsFiles),
	}
}

func (s *StreamExtractor) primaryBeadsFile() string {
	if len(s.beadsFiles) > 0 && s.beadsFiles[0] != "" {
		return s.beadsFiles[0]
	}
	return defaultBeadsFiles[0]
}

// SetProgressCallback sets the progress callback for streaming operations
func (s *StreamExtractor) SetProgressCallback(cb ProgressCallback) {
	s.progressCB = cb
}

func (s *StreamExtractor) progressCallback(opts StreamOptions) ProgressCallback {
	if opts.OnProgress != nil {
		return opts.OnProgress
	}
	return s.progressCB
}

// StreamOptions controls streaming extraction behavior
type StreamOptions struct {
	Since       *time.Time // Only commits after this time
	Until       *time.Time // Only commits before this time
	ClosedSince *time.Time // Only beads closed since this time (for skipping old closed beads)
	Limit       int        // Max commits to process (0 = DefaultHistoryLimit)
	BeadID      string     // Filter to single bead ID
	OnProgress  ProgressCallback
}

// StreamEvents extracts bead events using streaming parser (memory efficient)
func (s *StreamExtractor) StreamEvents(opts StreamOptions) ([]BeadEvent, error) {
	// Apply default limit
	limit := opts.Limit
	if limit == 0 {
		limit = DefaultHistoryLimit
	}

	onProgress := s.progressCallback(opts)

	// First, count commits for progress reporting (fast)
	totalCommits := 0
	if onProgress != nil {
		var err error
		totalCommits, err = s.countCommits(opts)
		if err != nil {
			// Non-fatal, just won't show accurate progress
			totalCommits = 0
		}
	}

	// Build git log command for streaming
	cmd := s.buildStreamCommand(opts, limit)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting git log: %w", err)
	}

	// Parse events as they stream in
	events, parseErr := s.parseStream(stdout, opts.BeadID, opts.ClosedSince, totalCommits, onProgress)
	if parseErr != nil {
		// Stop git before waiting. If parsing stopped early, git may still be
		// blocked writing to stdout; waiting first can deadlock.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("parsing git log stream: %w", parseErr)
	}

	// Wait for command to finish
	cmdErr := cmd.Wait()
	if cmdErr != nil {
		// Check if it's just because we reached the limit
		if exitErr, ok := cmdErr.(*exec.ExitError); ok {
			if exitErr.ExitCode() != 0 && exitErr.ExitCode() != 141 {
				return nil, fmt.Errorf("git log failed: %w", cmdErr)
			}
		}
	}

	// Reverse to chronological order
	reverseEvents(events)

	return events, nil
}

// countCommits quickly counts commits matching the criteria
func (s *StreamExtractor) countCommits(opts StreamOptions) (int, error) {
	args := []string{"rev-list", "--count", "HEAD", "--"}
	args = append(args, s.primaryBeadsFile())

	if opts.Since != nil {
		args = insertBefore(args, "--", fmt.Sprintf("--since=%s", opts.Since.Format(time.RFC3339)))
	}
	if opts.Until != nil {
		args = insertBefore(args, "--", fmt.Sprintf("--until=%s", opts.Until.Format(time.RFC3339)))
	}

	cmd := gitCommand(s.ctx, args...)
	cmd.Dir = s.repoPath

	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	var count int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
	return count, nil
}

// buildStreamCommand creates the git log command for streaming
func (s *StreamExtractor) buildStreamCommand(opts StreamOptions, limit int) *exec.Cmd {
	args := []string{
		"log",
		"-p",
		"--follow",
		"--format=" + gitLogHeaderFormat,
	}

	if opts.Since != nil {
		args = append(args, fmt.Sprintf("--since=%s", opts.Since.Format(time.RFC3339)))
	}
	if opts.Until != nil {
		args = append(args, fmt.Sprintf("--until=%s", opts.Until.Format(time.RFC3339)))
	}
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}

	args = append(args, "--")

	// Use primary beads file
	args = append(args, s.primaryBeadsFile())

	cmd := gitCommand(s.ctx, withNoColorGit(args)...)
	cmd.Dir = s.repoPath
	return cmd
}

// parseStream parses git log output as a stream
func (s *StreamExtractor) parseStream(r io.Reader, filterBeadID string, closedSince *time.Time, total int, onProgress ProgressCallback) ([]BeadEvent, error) {
	var events []BeadEvent
	var currentCommit *commitBuffer
	processed := 0

	scanner := bufio.NewScanner(r)
	// Use 64KB initial buffer, grow up to 10MB (matching extractor.go)
	buf := make([]byte, 64*1024)
	const maxCapacity = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()

		// Check for new commit header (uses package-level compiled regex)
		if commitPattern.MatchString(line) {
			// Process previous commit if exists
			if currentCommit != nil {
				commitEvents, err := s.processCommitBuffer(currentCommit, filterBeadID, closedSince)
				if err != nil {
					return events, err
				}
				events = append(events, commitEvents...)
			}

			// Start new commit
			currentCommit = &commitBuffer{
				headerLine: line,
				diffLines:  make([]string, 0, 100),
			}

			processed++
			if onProgress != nil && processed%10 == 0 {
				onProgress(processed, total)
			}
		} else if currentCommit != nil {
			// Only collect lines that might contain bead JSON (lines starting with + or -)
			if len(line) > 0 && (line[0] == '+' || line[0] == '-') && strings.Contains(line, "{") {
				currentCommit.diffLines = append(currentCommit.diffLines, line)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("scanning stream: %w", err)
	}

	// Process final commit
	if currentCommit != nil {
		commitEvents, err := s.processCommitBuffer(currentCommit, filterBeadID, closedSince)
		if err != nil {
			return events, err
		}
		events = append(events, commitEvents...)
	}

	if onProgress != nil {
		onProgress(processed, total)
	}

	return events, nil
}

// commitBuffer holds buffered data for a single commit
type commitBuffer struct {
	headerLine string
	diffLines  []string
}

// processCommitBuffer processes a buffered commit and extracts events
func (s *StreamExtractor) processCommitBuffer(buf *commitBuffer, filterBeadID string, closedSince *time.Time) ([]BeadEvent, error) {
	// Parse commit info
	info, err := parseCommitHeader(buf.headerLine)
	if err != nil {
		return nil, fmt.Errorf("parsing commit header: %w", err)
	}

	// Parse diff
	events := s.parseBufferedDiff(buf.diffLines, info, filterBeadID, closedSince)
	return events, nil
}

// parseCommitHeader extracts commit metadata from the header line
func parseCommitHeader(line string) (commitInfo, error) {
	parts := strings.SplitN(line, "\x00", 5)
	if len(parts) != 5 {
		return commitInfo{}, fmt.Errorf("invalid commit format: %s", line)
	}

	timestamp, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return commitInfo{}, fmt.Errorf("invalid timestamp: %w", err)
	}

	return commitInfo{
		SHA:         parts[0],
		Timestamp:   timestamp,
		Author:      parts[2],
		AuthorEmail: parts[3],
		Message:     parts[4],
	}, nil
}

// parseBufferedDiff extracts events from buffered diff lines
func (s *StreamExtractor) parseBufferedDiff(lines []string, info commitInfo, filterBeadID string, closedSince *time.Time) []BeadEvent {
	var events []BeadEvent

	oldBeads := make(map[string]beadSnapshot)
	newBeads := make(map[string]beadSnapshot)
	seenBeads := make(map[string]bool)

	for _, line := range lines {
		// Robustly handle diff lines:
		// 1. Identify removal (-) or addition (+)
		// 2. Trim spaces to handle indented JSON (e.g. "  {")
		// 3. Verify it starts with "{" to avoid false positives on non-JSON diffs

		if strings.HasPrefix(line, "-") {
			jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			if strings.HasPrefix(jsonStr, "{") {
				if snap, ok := parseBeadJSON(jsonStr); ok {
					if filterBeadID == "" || snap.ID == filterBeadID {
						oldBeads[snap.ID] = snap
						seenBeads[snap.ID] = true
					}
				}
			}
		} else if strings.HasPrefix(line, "+") {
			jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "+"))
			if strings.HasPrefix(jsonStr, "{") {
				if snap, ok := parseBeadJSON(jsonStr); ok {
					if filterBeadID == "" || snap.ID == filterBeadID {
						newBeads[snap.ID] = snap
						seenBeads[snap.ID] = true
					}
				}
			}
		}
	}

	// Generate events in a stable order within each commit.
	beadIDs := make([]string, 0, len(seenBeads))
	for beadID := range seenBeads {
		beadIDs = append(beadIDs, beadID)
	}
	sort.Strings(beadIDs)
	for _, beadID := range beadIDs {
		oldSnap, hadOld := oldBeads[beadID]
		newSnap, hasNew := newBeads[beadID]

		event := BeadEvent{
			BeadID:      beadID,
			Timestamp:   info.Timestamp,
			CommitSHA:   info.SHA,
			CommitMsg:   info.Message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
		}

		if !hadOld && hasNew {
			event.EventType = EventCreated
			events = append(events, event)
		} else if hadOld && hasNew {
			if oldSnap.Status != newSnap.Status {
				event.EventType = determineStatusEvent(oldSnap.Status, newSnap.Status)

				// Skip old closed beads if ClosedSince is set
				if closedSince != nil && event.EventType == EventClosed {
					if info.Timestamp.Before(*closedSince) {
						continue
					}
				}

				events = append(events, event)
			} else {
				event.EventType = EventModified
				events = append(events, event)
			}
		}
	}

	return events
}

// BatchFileStatsExtractor extracts file stats for multiple commits in batches
type BatchFileStatsExtractor struct {
	repoPath  string
	batchSize int
	mu        sync.Mutex
	cache     map[string][]FileChange

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// the extractor (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the extractor so its git subprocesses are
// cancelled when ctx is done (issue #166). Returns the receiver for chaining.
func (b *BatchFileStatsExtractor) WithContext(ctx context.Context) *BatchFileStatsExtractor {
	b.ctx = ctx
	return b
}

// NewBatchFileStatsExtractor creates a new batch extractor
func NewBatchFileStatsExtractor(repoPath string) *BatchFileStatsExtractor {
	return &BatchFileStatsExtractor{
		repoPath:  repoPath,
		batchSize: 50, // Process 50 commits at a time
		cache:     make(map[string][]FileChange),
	}
}

// SetBatchSize sets the batch size for git operations
func (b *BatchFileStatsExtractor) SetBatchSize(size int) {
	if size > 0 {
		b.mu.Lock()
		defer b.mu.Unlock()

		b.batchSize = size
	}
}

// ExtractBatch extracts file changes for multiple commit SHAs in a batch
func (b *BatchFileStatsExtractor) ExtractBatch(shas []string) (map[string][]FileChange, error) {
	result, uncached := b.splitCached(shas)

	if len(uncached) == 0 {
		return result, nil
	}

	batchSize := b.currentBatchSize()

	// Process in batches
	for i := 0; i < len(uncached); i += batchSize {
		end := i + batchSize
		if end > len(uncached) {
			end = len(uncached)
		}
		batch := uncached[i:end]

		batchResult, err := b.extractBatchFiles(batch)
		if err != nil {
			return result, err
		}

		// Merge results and update cache
		b.storeBatchResult(result, batchResult)
	}

	return result, nil
}

func (b *BatchFileStatsExtractor) currentBatchSize() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.batchSize
}

func (b *BatchFileStatsExtractor) splitCached(shas []string) (map[string][]FileChange, []string) {
	result := make(map[string][]FileChange)
	var uncached []string

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, sha := range shas {
		files, ok := b.cache[sha]
		if !ok {
			uncached = append(uncached, sha)
			continue
		}

		result[sha] = cloneFileChanges(files)
	}

	return result, uncached
}

func (b *BatchFileStatsExtractor) storeBatchResult(result map[string][]FileChange, batchResult map[string][]FileChange) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for sha, files := range batchResult {
		result[sha] = cloneFileChanges(files)
		b.cache[sha] = cloneFileChanges(files)
	}
}

func cloneFileChanges(files []FileChange) []FileChange {
	if len(files) == 0 {
		return nil
	}

	copied := make([]FileChange, len(files))
	copy(copied, files)
	return copied
}

// extractBatchFiles extracts files for a batch of commits using a single git command
func (b *BatchFileStatsExtractor) extractBatchFiles(shas []string) (map[string][]FileChange, error) {
	result := make(map[string][]FileChange)

	// Use git log with specific commits to get all file changes in one call
	// Format: commit SHA, then name-status
	args := []string{"log", "--name-status", "--format=%H", "--no-walk"}
	args = append(args, shas...)

	cmd := gitCommand(b.ctx, args...)
	cmd.Dir = b.repoPath

	out, err := cmd.Output()
	if err != nil {
		// Fall back to individual extraction
		return b.extractIndividually(shas)
	}

	// Parse output: each commit starts with SHA line, followed by empty line, then files
	var currentSHA string
	var currentFiles []FileChange

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()

		// Check if this is a SHA line (40 hex chars)
		if len(line) == 40 && isHexString(line) {
			// Save previous commit's files
			if currentSHA != "" {
				result[currentSHA] = filterCodeFiles(currentFiles)
			}
			currentSHA = line
			currentFiles = nil
			continue
		}

		if line == "" {
			continue
		}

		// Parse file change line
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			action := parts[0]
			path := parts[1]

			if len(parts) == 3 && strings.HasPrefix(action, "R") {
				path = parts[2]
				action = "R"
			}

			if len(action) > 1 {
				action = string(action[0])
			}

			currentFiles = append(currentFiles, FileChange{
				Path:   path,
				Action: action,
			})
		}
	}

	// Save last commit's files
	if currentSHA != "" {
		result[currentSHA] = filterCodeFiles(currentFiles)
	}

	return result, scanner.Err()
}

// extractIndividually falls back to extracting files one commit at a time
func (b *BatchFileStatsExtractor) extractIndividually(shas []string) (map[string][]FileChange, error) {
	result := make(map[string][]FileChange)
	cocommit := NewCoCommitExtractor(b.repoPath)

	for _, sha := range shas {
		files, err := cocommit.getFilesChanged(sha)
		if err != nil {
			continue // Skip failed commits
		}
		result[sha] = filterCodeFiles(files)
	}

	return result, nil
}

// filterCodeFiles filters a list of file changes to only code files
func filterCodeFiles(files []FileChange) []FileChange {
	var result []FileChange
	for _, f := range files {
		if isCodeFile(f.Path) && !isExcludedPath(f.Path) {
			result = append(result, f)
		}
	}
	return result
}

// isHexString checks if a string contains only lowercase hexadecimal characters.
// Git SHAs from `git log --format=%H` are always lowercase.
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ClearCache clears the file stats cache
func (b *BatchFileStatsExtractor) ClearCache() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.cache = make(map[string][]FileChange)
}
