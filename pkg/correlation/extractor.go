// Package correlation provides extraction of bead lifecycle events from git history.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	json "github.com/goccy/go-json"
)

// ExtractOptions controls which commits and beads to extract events from
type ExtractOptions struct {
	Revision string     // Resolved commit to walk from (empty = HEAD); excludes descendants by ancestry
	Since    *time.Time // Only commits after this time (nil = no limit)
	Until    *time.Time // Only commits before this time (nil = no limit)
	Limit    int        // Max commits to process (0 = no limit)
	BeadID   string     // Filter to single bead ID (empty = all beads)
}

// Extractor extracts bead lifecycle events from git history
type Extractor struct {
	repoPath   string
	beadsFiles []string // Files to track (e.g., .beads/beads.jsonl, .beads/issues.jsonl)

	// ctx, when set (via Correlator.WithContext or directly), bounds the git
	// subprocesses spawned during extraction (issue #166). nil means
	// context.Background().
	ctx context.Context
}

// NewExtractor creates a new extractor for the given repository.
// beadsFilePath is optional; when empty the extractor will track the standard
// Beads files inside .beads/. A variadic parameter is used to preserve
// backward compatibility with existing call sites that pass only repoPath.
func NewExtractor(repoPath string, beadsFilePath ...string) *Extractor {
	e := &Extractor{
		repoPath:   repoPath,
		beadsFiles: pickBeadsFiles(repoPath, defaultBeadsFiles),
	}

	// If a specific file is provided, prioritize it
	var beadPath string
	if len(beadsFilePath) > 0 {
		beadPath = beadsFilePath[0]
	}
	if beadPath != "" {
		// Ensure relative path if possible, though absolute usually works with git if inside repo
		// For simplicity, we prepend it to the list so it's picked up by buildGitLogArgs as primary
		rel, err := filepath.Rel(repoPath, beadPath)
		if err == nil {
			e.beadsFiles = prependBeadsFile(rel, e.beadsFiles)
		} else {
			e.beadsFiles = prependBeadsFile(beadPath, e.beadsFiles)
		}
	}

	return e
}

func (e *Extractor) primaryBeadsFile() string {
	if len(e.beadsFiles) > 0 && e.beadsFiles[0] != "" {
		return e.beadsFiles[0]
	}
	return defaultBeadsFiles[0]
}

// commitInfo holds parsed commit metadata
type commitInfo struct {
	SHA         string
	Timestamp   time.Time
	Author      string
	AuthorEmail string
	Message     string
}

// beadSnapshot represents a bead's state at a point in time
type beadSnapshot struct {
	ID           string
	Status       string
	Title        string
	Dependencies []HistoricalDependency
}

// snapshotBlobSizeThreshold is the followed-file blob size (in bytes) at or above
// which Extract prefers the snapshot path over the legacy `git log -p` path.
//
// The legacy `-p` path asks git to run its Myers diff over the whole multi-MB
// JSONL blob at every commit *and stream the full patch text back* — its cost is
// dominated by that one subprocess and grows with blob size. The snapshot path
// (pass-3 rewrite) instead runs a metadata-only `git log --raw` (~constant), reads
// each *unique* blob once through a single `git cat-file --batch`, and computes
// the per-commit record diff in Go with a 64-bit-hashed line multiset. It pays
// one extra git fork but no per-commit diff, so it wins as soon as the blob is
// big enough for git's diff to cost more than that fork.
//
// Measured on this machine (200-commit histories, warm cache, real
// extractViaSnapshots vs extractViaGitLogPatch):
//
//	blob       legacy `-p`     snapshot     winner
//	~1 KB      ~9.1 ms         ~12.6 ms     legacy (by ~3 ms, irrelevant)
//	~100 KB    ~150 ms         ~84 ms       snapshot (~1.8x)
//	~1.9 MB    ~853 ms         ~561 ms      snapshot (~1.5x)
//
// The only regime where legacy wins is a sub-KB blob, where the whole extraction
// is already <15 ms and the difference is a few milliseconds. The crossover sits
// well under 100 KB, so we gate at 64 KB: any real beads history takes the fast
// snapshot path (including this repo's 1.9 MB blob, the #161 case), while a
// pathologically tiny repo keeps the marginally-faster native diff where it
// cannot matter. Output is byte-identical on either side of the gate (verified by
// the differential test and the golden artifacts), so the threshold is purely a
// speed/heuristic knob and never changes triage results.
const snapshotBlobSizeThreshold = 64 * 1024 // 64 KB

// Extract extracts bead lifecycle events from git history.
//
// It reconstructs lifecycle events from per-commit JSONL snapshot differences
// (extractViaSnapshots) instead of asking git to produce a full textual patch
// (`git log -p`) of the followed beads blob. The `-p` path runs git's diff over
// the whole multi-MB JSONL at every commit (O(blob x commits)) and dominated
// `--robot-triage` on large repos (#161). The snapshot path reads each blob and
// computes the changed record lines in Go, feeding the *unchanged* parseDiff so
// event semantics are identical (proven by the differential test).
//
// The two paths produce byte-identical events; they differ only in cost profile,
// so Extract dispatches on the followed file's current blob size: large blobs
// (where `-p` blows up to minutes) take the snapshot path; small blobs take the
// faster native path. See snapshotBlobSizeThreshold.
func (e *Extractor) Extract(opts ExtractOptions) ([]BeadEvent, error) {
	if e.preferSnapshotPath() {
		return e.extractViaSnapshots(opts)
	}
	return e.extractViaGitLogPatch(opts)
}

// preferSnapshotPath reports whether the followed beads file's current (HEAD)
// blob is large enough that the snapshot path should be used. It runs a single
// cheap `git cat-file -s HEAD:<file>`. If the size cannot be determined (no
// history yet, untracked file, detached/empty repo), it returns true so the
// snapshot path — which already degrades gracefully to "no commits" — handles the
// edge case, never falling back to a slower-or-equal native diff in that case.
func (e *Extractor) preferSnapshotPath() bool {
	primary := e.primaryBeadsFile()
	cmd := gitCommand(e.ctx, "cat-file", "-s", "HEAD:"+primary)
	cmd.Dir = e.repoPath
	out, err := cmd.Output()
	if err != nil {
		return true
	}
	var size int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &size); err != nil {
		return true
	}
	return size >= snapshotBlobSizeThreshold
}

// extractViaGitLogPatch is the legacy `git log -p` extraction path, retained for
// reference and differential testing against extractViaSnapshots.
func (e *Extractor) extractViaGitLogPatch(opts ExtractOptions) ([]BeadEvent, error) {
	// Build git log command
	logArgs := e.buildGitLogArgs(opts)

	// Disable colors so patch lines still start with raw '+' and '-'.
	cmd := gitCommand(e.ctx, withNoColorGit(logArgs)...)
	cmd.Dir = e.repoPath

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting git log: %w", err)
	}

	// Parse output stream
	events, parseErr := e.parseGitLogOutput(stdout, opts.BeadID)

	// If parsing failed, ensure we drain the pipe or kill the process to avoid deadlock
	// where git log is blocked writing to full pipe while we wait for it to exit.
	if parseErr != nil {
		// Try to kill the process to unblock the write
		_ = cmd.Process.Kill()
		// We still need to wait to clean up zombies, but now it should exit quickly
		_ = cmd.Wait()
		return nil, fmt.Errorf("parsing git log output: %w", parseErr)
	}

	if err := cmd.Wait(); err != nil {
		// If git log failed (non-zero exit), prefer that error
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	// Sort commits chronologically (git log returns newest commit first) while
	// preserving deterministic within-commit event ordering (e.g. EventCreated
	// before synthesized EventClaimed / EventClosed).
	reverseCommitEventGroups(events)

	return events, nil
}

// buildGitLogArgs constructs the git log command arguments
func (e *Extractor) buildGitLogArgs(opts ExtractOptions) []string {
	args := []string{
		"log",
		"-p",                             // Include patch/diff
		"--unified=0",                    // Zero context: parser only needs +/- JSONL lines, not unchanged records (#160)
		"--follow",                       // Track renames; requires a single pathspec (handled below)
		"--format=" + gitLogHeaderFormat, // Custom format for commit info
		"--",
	}

	// Add time filters before "--"
	if opts.Since != nil {
		args = insertBefore(args, "--", fmt.Sprintf("--since=%s", opts.Since.Format(time.RFC3339)))
	}
	if opts.Until != nil {
		args = insertBefore(args, "--", fmt.Sprintf("--until=%s", opts.Until.Format(time.RFC3339)))
	}
	if opts.Limit > 0 {
		args = insertBefore(args, "--", fmt.Sprintf("-n%d", opts.Limit))
	}
	if opts.Revision != "" {
		args = insertBefore(args, "--", opts.Revision)
	}

	// Optimization: If filtering by BeadID, tell git to only show commits
	// where this ID appears in the diff (added or removed).
	// We use -G with a regex to match the JSON field to avoid false positives on common IDs like "1".
	// Regex matches: "id": [whitespace] "BEAD_ID"
	if opts.BeadID != "" {
		// Escape regex meta-characters in BeadID just in case
		safeID := regexp.QuoteMeta(opts.BeadID)
		regex := fmt.Sprintf(`"id":\s*"%s"`, safeID)
		args = insertBefore(args, "--", fmt.Sprintf("-G%s", regex))
	}

	// Use primary beads file for follow support (git requires single pathspec with --follow)
	primary := ".beads/beads.jsonl"
	if len(e.beadsFiles) > 0 {
		primary = e.beadsFiles[0]
	}
	args = append(args, primary)

	return args
}

// insertBefore inserts a value before a marker in a slice
func insertBefore(slice []string, marker, value string) []string {
	for i, v := range slice {
		if v == marker {
			result := make([]string, 0, len(slice)+1)
			result = append(result, slice[:i]...)
			result = append(result, value)
			result = append(result, slice[i:]...)
			return result
		}
	}
	return slice
}

// parseGitLogOutput parses the combined commit info and diff output from a stream
func (e *Extractor) parseGitLogOutput(r io.Reader, filterBeadID string) ([]BeadEvent, error) {
	var events []BeadEvent

	// Use bufio.Reader instead of Scanner to handle long lines
	const maxScanTokenSize = 10 * 1024 * 1024 // 10MB
	reader := bufio.NewReaderSize(r, maxScanTokenSize)

	var currentCommit *commitInfo
	var diffBuffer bytes.Buffer

	// Helper to process the accumulated commit
	processCommit := func() {
		if currentCommit == nil {
			return
		}
		diffBytes := diffBuffer.Bytes()
		if len(diffBytes) > 0 {
			diffEvents := e.parseDiff(diffBytes, *currentCommit, filterBeadID)
			events = append(events, diffEvents...)
		}
		diffBuffer.Reset()
	}

	for {
		// ReadLine returns a single line, not including the end-of-line bytes.
		lineBytes, isPrefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		if isPrefix {
			// Line too long. Skip it.
			// Consume until newline or EOF
			for isPrefix {
				_, isPrefix, err = reader.ReadLine()
				if err != nil && !errors.Is(err, io.EOF) {
					return nil, err
				}
				if errors.Is(err, io.EOF) {
					break
				}
			}
			// Skip processing this line as it's incomplete/truncated
			continue
		}

		line := string(lineBytes)

		// Check for commit header
		if commitPattern.MatchString(line) {
			// Finish previous commit
			processCommit()

			// Parse new header
			info, err := parseCommitInfo(line)
			if err != nil {
				// Treat as diff content if parsing fails but regex matched
				// (Shouldn't happen with our regex, but safe fallback)
				diffBuffer.WriteString(line)
				diffBuffer.WriteByte('\n')
				continue
			}

			currentCommit = &info
		} else {
			// Diff content
			if currentCommit != nil {
				diffBuffer.WriteString(line)
				diffBuffer.WriteByte('\n')
			}
		}
	}

	// Process final commit
	processCommit()

	return events, nil
}

// commitPattern matches the start of a commit in our custom log format
var commitPattern = regexp.MustCompile(`(?m)^[0-9a-f]{40}\x00`)

// parseCommitInfo extracts commit metadata from the header line
func parseCommitInfo(line string) (commitInfo, error) {
	parts := strings.SplitN(line, "\x00", 5)
	if len(parts) != 5 {
		return commitInfo{}, fmt.Errorf("invalid commit format: %s", line)
	}

	timestamp, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return commitInfo{}, fmt.Errorf("invalid timestamp: %w", err)
	}

	info := commitInfo{
		SHA:         parts[0],
		Timestamp:   timestamp,
		Author:      parts[2],
		AuthorEmail: parts[3],
		Message:     parts[4],
	}

	return info, nil
}

// parseDiff extracts bead events from a diff section
func (e *Extractor) parseDiff(diffData []byte, info commitInfo, filterBeadID string) []BeadEvent {
	var events []BeadEvent

	// Track old and new bead states for status change detection
	oldBeads := make(map[string]beadSnapshot)
	newBeads := make(map[string]beadSnapshot)
	seenBeads := make(map[string]bool)
	complete := true

	scanner := bufio.NewScanner(bytes.NewReader(diffData))
	// Increase buffer for large diffs
	const maxCapacity = 1024 * 1024 * 10 // 10MB
	// Start with 64KB buffer, grow up to maxCapacity
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines and diff metadata lines that start with:
		// '@' = hunk headers (@@)
		// 'd' = diff --git
		// 'i' = index
		// 'n' = new file mode
		// We only care about lines starting with +/- which are actual changes.
		if isIgnorableDiffMetadataLine(line) {
			continue
		}

		// Check for removed lines (old state) - JSON starts with {
		if strings.HasPrefix(line, "-{") {
			jsonStr := strings.TrimPrefix(line, "-")
			if snap, ok := parseBeadJSON(jsonStr); ok {
				if filterBeadID == "" || snap.ID == filterBeadID {
					oldBeads[snap.ID] = snap
					seenBeads[snap.ID] = true
				}
			} else {
				complete = false
			}
			continue
		}

		// Check for added lines (new state) - JSON starts with {
		if strings.HasPrefix(line, "+{") {
			jsonStr := strings.TrimPrefix(line, "+")
			if snap, ok := parseBeadJSON(jsonStr); ok {
				if filterBeadID == "" || snap.ID == filterBeadID {
					newBeads[snap.ID] = snap
					seenBeads[snap.ID] = true
				}
			} else {
				complete = false
			}
			continue
		}
	}
	if scanner.Err() != nil {
		complete = false
	}

	// Generate events by comparing old and new states. Iterate the affected bead
	// IDs in a deterministic (sorted) order rather than Go's randomized map order:
	// the within-commit event order is then stable across runs, which (a) makes
	// the per-commit event cache's replayed order byte-identical to a fresh full
	// extraction (the incremental path's correctness contract), and (b) removes a
	// latent run-to-run non-determinism in the emitted event sequence. Downstream
	// consumers group events by bead ID and timestamps are equal within a commit,
	// so this does not change any report/golden output — it only fixes the order.
	sortedBeadIDs := make([]string, 0, len(seenBeads))
	for beadID := range seenBeads {
		sortedBeadIDs = append(sortedBeadIDs, beadID)
	}
	sort.Strings(sortedBeadIDs)
	for _, beadID := range sortedBeadIDs {
		oldSnap, hadOld := oldBeads[beadID]
		newSnap, hasNew := newBeads[beadID]

		event := BeadEvent{
			BeadID:             beadID,
			Timestamp:          info.Timestamp,
			CommitSHA:          info.SHA,
			CommitMsg:          info.Message,
			Author:             info.Author,
			AuthorEmail:        info.AuthorEmail,
			TransitionObserved: complete,
		}
		if hadOld {
			event.Before = oldSnap.historicalState()
		}
		if hasNew {
			event.After = newSnap.historicalState()
		}

		if !hadOld && hasNew {
			// New bead created; synthesize initial Claimed / Closed milestone
			// when the bead enters history directly as in_progress or closed.
			event.EventType = EventCreated
			events = append(events, event)
			if synth, ok := synthesizedCreationMilestone(event, newSnap.Status); ok {
				events = append(events, synth)
			}
		} else if hadOld && hasNew {
			// Check for status change
			if oldSnap.Status != newSnap.Status {
				event.EventType = determineStatusEvent(oldSnap.Status, newSnap.Status)
				events = append(events, event)
			} else {
				// Other modification (title, etc.)
				event.EventType = EventModified
				events = append(events, event)
			}
		}
		if hadOld && !hasNew {
			event.EventType = EventDeleted
			events = append(events, event)
		}
	}

	return events
}

func isIgnorableDiffMetadataLine(line string) bool {
	if len(line) == 0 {
		return true
	}
	switch line[0] {
	case '@', 'd', 'i', 'n':
		return true
	default:
		return false
	}
}

// parseBeadJSON extracts minimal bead info from a JSON line
func parseBeadJSON(jsonStr string) (beadSnapshot, bool) {
	var partial struct {
		ID           string                 `json:"id"`
		Status       string                 `json:"status"`
		Title        string                 `json:"title"`
		Dependencies []HistoricalDependency `json:"dependencies"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &partial); err != nil {
		return beadSnapshot{}, false
	}

	if partial.ID == "" {
		return beadSnapshot{}, false
	}
	// Canonical empty values survive the optional JSON field's cache round trip.
	if len(partial.Dependencies) == 0 {
		partial.Dependencies = nil
	}

	return beadSnapshot{
		ID:           partial.ID,
		Status:       partial.Status,
		Title:        partial.Title,
		Dependencies: partial.Dependencies,
	}, true
}

func (s beadSnapshot) historicalState() *HistoricalIssueState {
	return &HistoricalIssueState{ID: s.ID, Status: s.Status, Title: s.Title, Dependencies: s.Dependencies}
}

func (d *HistoricalDependency) UnmarshalJSON(data []byte) error {
	var dependency model.Dependency
	if err := json.Unmarshal(data, &dependency); err != nil {
		return err
	}
	d.DependsOnID = dependency.DependsOnID
	d.Type = string(dependency.Type)
	return nil
}

// determineStatusEvent determines the appropriate event type for a status transition
func determineStatusEvent(oldStatus, newStatus string) EventType {
	oldStatus = normalizeLifecycleStatus(oldStatus)
	newStatus = normalizeLifecycleStatus(newStatus)

	switch newStatus {
	case "in_progress":
		if isClosedLifecycleStatus(oldStatus) {
			return EventReopened
		}
		return EventClaimed
	case "closed", "tombstone":
		return EventClosed
	case "open":
		if isClosedLifecycleStatus(oldStatus) {
			return EventReopened
		}
		return EventModified
	default:
		return EventModified
	}
}

func normalizeLifecycleStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func isClosedLifecycleStatus(status string) bool {
	return status == "closed" || status == "tombstone"
}

// synthesizedCreationMilestone emits a synthetic EventClaimed or EventClosed
// immediately after EventCreated when a bead first appears in JSONL already in
// an active (in_progress) or terminal (closed/tombstone) lifecycle state.
func synthesizedCreationMilestone(created BeadEvent, rawStatus string) (BeadEvent, bool) {
	norm := normalizeLifecycleStatus(rawStatus)
	synth := created
	switch {
	case norm == "in_progress":
		synth.EventType = EventClaimed
		return synth, true
	case isClosedLifecycleStatus(norm):
		synth.EventType = EventClosed
		return synth, true
	default:
		return BeadEvent{}, false
	}
}

// reverseCommitEventGroups reverses commit blocks from newest-first to
// oldest-first while preserving within-commit event ordering.
func reverseCommitEventGroups(events []BeadEvent) {
	reverseEvents(events)
	for start := 0; start < len(events); {
		end := start + 1
		for end < len(events) && events[end].CommitSHA == events[start].CommitSHA {
			end++
		}
		if end-start > 1 {
			reverseEvents(events[start:end])
		}
		start = end
	}
}

// reverseEvents reverses a slice of events in place
func reverseEvents(events []BeadEvent) {
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
}

// ExtractForBead extracts all events for a specific bead
func (e *Extractor) ExtractForBead(beadID string, opts ExtractOptions) ([]BeadEvent, error) {
	opts.BeadID = beadID
	return e.Extract(opts)
}

// GetBeadMilestones returns the key lifecycle milestones for a bead
func GetBeadMilestones(events []BeadEvent) BeadMilestones {
	var milestones BeadMilestones

	for i := range events {
		event := &events[i]
		switch event.EventType {
		case EventCreated:
			if milestones.Created == nil {
				milestones.Created = event
			}
		case EventClaimed:
			if milestones.Claimed == nil {
				milestones.Claimed = event
			}
		case EventClosed:
			milestones.Closed = event // Keep latest
		case EventReopened:
			milestones.Reopened = event // Keep latest
		}
	}

	return milestones
}

// CalculateCycleTime computes cycle time metrics from milestones
func CalculateCycleTime(milestones BeadMilestones) *CycleTime {
	if milestones.Closed == nil {
		return nil
	}

	ct := &CycleTime{}

	if milestones.Claimed != nil {
		d := milestones.Closed.Timestamp.Sub(milestones.Claimed.Timestamp)
		ct.ClaimToClose = &d
	}

	if milestones.Created != nil {
		d := milestones.Closed.Timestamp.Sub(milestones.Created.Timestamp)
		ct.CreateToClose = &d

		if milestones.Claimed != nil {
			d := milestones.Claimed.Timestamp.Sub(milestones.Created.Timestamp)
			ct.CreateToClaim = &d
		}
	}

	return ct
}
