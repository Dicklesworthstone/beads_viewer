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
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// ExtractOptions controls which commits and beads to extract events from
type ExtractOptions struct {
	Since  *time.Time // Only commits after this time (nil = no limit)
	Until  *time.Time // Only commits before this time (nil = no limit)
	Limit  int        // Max commits to process (0 = no limit)
	BeadID string     // Filter to single bead ID (empty = all beads)
}

// Extractor extracts bead lifecycle events from git history
type Extractor struct {
	repoPath   string
	beadsFiles []string // Files to track (e.g., .beads/beads.jsonl, .beads/issues.jsonl)

	// blobReaderFactory is an internal test seam for verifying that snapshot
	// extraction does not publish results until the long-lived cat-file process
	// has exited successfully. nil uses the real Git-backed reader.
	blobReaderFactory func() (snapshotBlobReadCloser, error)

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
	ID     string
	Status string
	Title  string
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
	cmd := repoGitCommand(e.ctx, e.repoPath, "cat-file", "-s", "HEAD:"+primary)
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

	cmd := lifecycleGitLogCommand(e.ctx, e.repoPath, logArgs...)

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

	// Sort chronologically (git log returns newest first)
	reverseEvents(events)

	return events, nil
}

// buildGitLogArgs constructs the git log command arguments
func (e *Extractor) buildGitLogArgs(opts ExtractOptions) []string {
	args := []string{
		"-p",                             // Include patch/diff
		"--unified=1",                    // One context line exposes line-1 BOM eligibility shifts
		"--follow",                       // Track renames; requires a single pathspec (handled below)
		"--format=" + gitLogHeaderFormat, // Custom format for commit info
	}
	args = append(args, lifecycleHistoryOrderArgs()...)
	args = append(args, "--")

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

	// Do not use Git's -G pickaxe for BeadID filtering. A record can become
	// loader-eligible solely because an unchanged first-line BOM record moves
	// from a later physical line to line one; that commit contains no matching
	// +/- record for the bead. parseDiff applies the exact ID filter after the
	// complete patch has reconstructed that eligibility transition.

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
	objectIDWidth := 0

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
			// Silently skipping an oversized JSONL record would omit a lifecycle
			// event and could then persist that partial history. Fail closed; the
			// subprocess caller will kill/drain Git as appropriate.
			return nil, fmt.Errorf("git log line exceeds %d-byte parser limit", maxScanTokenSize)
		}

		line := string(lineBytes)

		// Check for commit header. A NUL-bearing line that does not match a
		// canonical SHA-1/SHA-256 header is malformed Git output, not diff text.
		if commitPattern.MatchString(line) {
			// Finish previous commit
			processCommit()

			// Parse new header
			info, err := parseCommitInfo(line)
			if err != nil {
				return nil, fmt.Errorf("parsing commit header: %w", err)
			}
			if objectIDWidth == 0 {
				objectIDWidth = len(info.SHA)
			} else if len(info.SHA) != objectIDWidth {
				return nil, fmt.Errorf("mixed-width commit object IDs: got %d and %d characters", objectIDWidth, len(info.SHA))
			}

			currentCommit = &info
		} else if strings.ContainsRune(line, '\x00') {
			return nil, fmt.Errorf("malformed commit header with noncanonical object ID")
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

// commitPattern matches a complete lowercase SHA-1 or SHA-256 object ID at the
// start of a commit in our custom log format. The terminating NUL prevents a
// 64-character ID from being mistaken for a 40-character prefix.
var commitPattern = regexp.MustCompile(`(?m)^(?:[0-9a-f]{40}|[0-9a-f]{64})\x00`)

// parseCommitInfo extracts commit metadata from the header line
func parseCommitInfo(line string) (commitInfo, error) {
	parts := strings.SplitN(line, "\x00", 5)
	if len(parts) != 5 {
		return commitInfo{}, fmt.Errorf("invalid commit format: %s", line)
	}
	if !isCanonicalCommitSHA(parts[0]) {
		return commitInfo{}, fmt.Errorf("invalid commit object ID %q", parts[0])
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
	var classifier unifiedDiffRecordClassifier

	// Track old and new bead states for status change detection
	oldBeads := make(map[string]beadSnapshot)
	newBeads := make(map[string]beadSnapshot)
	seenBeads := make(map[string]bool)

	// diffData is already resident in memory, so walk its line boundaries
	// directly. A Scanner limit here used to silently truncate a synthesized
	// snapshot diff when one valid JSONL record exceeded 10 MiB, omitting the
	// event and allowing the partial result into the per-commit cache.
	for start := 0; start < len(diffData); {
		relativeEnd := bytes.IndexByte(diffData[start:], '\n')
		end := len(diffData)
		next := len(diffData)
		if relativeEnd >= 0 {
			end = start + relativeEnd
			next = end + 1
		}
		lineBytes := diffData[start:end]
		if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
			lineBytes = lineBytes[:len(lineBytes)-1]
		}
		line := string(lineBytes)
		start = next

		jsonStr, added, ok := classifier.classify(line)
		if !ok {
			continue
		}
		if snap, parsed := parseBeadJSON(jsonStr); parsed && (filterBeadID == "" || snap.ID == filterBeadID) {
			if added {
				newBeads[snap.ID] = snap
			} else {
				oldBeads[snap.ID] = snap
			}
			seenBeads[snap.ID] = true
		}
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
			BeadID:      beadID,
			Timestamp:   info.Timestamp,
			CommitSHA:   info.SHA,
			CommitMsg:   info.Message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
		}

		if !hadOld && hasNew {
			// New bead created
			event.EventType = EventCreated
			events = append(events, event)
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
		// Note: We don't track deletions (hadOld && !hasNew) as they're not in our EventType
	}

	return events
}

// beadJSONFromDiffLine classifies one unified-diff record and returns its JSON
// payload. JSON permits the four ASCII whitespace bytes, so both the
// resident/snapshot and streaming history paths trim exactly those bytes after
// the +/- marker before requiring an object. A BOM is accepted only immediately
// after the marker, corresponding to the loader's physical first-line BOM.
// Diff metadata such as +++/--- is rejected because it does not begin with an
// object after normalization.
func beadJSONFromDiffLine(line string) (jsonStr string, added bool, ok bool) {
	return beadJSONFromDiffLineAt(line, true)
}

func beadJSONFromDiffLineAt(line string, allowBOM bool) (jsonStr string, added bool, ok bool) {
	if len(line) < 2 || (line[0] != '+' && line[0] != '-') {
		return "", false, false
	}
	jsonStr, ok = beadJSONFromPhysicalLine(line[1:], allowBOM)
	return jsonStr, line[0] == '+', ok
}

func beadJSONFromPhysicalLine(line string, allowBOM bool) (jsonStr string, ok bool) {
	jsonStr = line
	if allowBOM {
		jsonStr = strings.TrimPrefix(jsonStr, "\uFEFF")
	}
	jsonStr = strings.Trim(jsonStr, " \t\r\n")
	if !strings.HasPrefix(jsonStr, "{") {
		return "", false
	}
	return jsonStr, true
}

var unifiedDiffHunkPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,[0-9]+)? \+([0-9]+)(?:,[0-9]+)? @@`)

// unifiedDiffRecordClassifier tracks the old/new physical line numbers supplied
// by unified-diff hunk headers. The authoritative loader strips a BOM only from
// physical line one, so accepting an immediate BOM on every +/- line would
// create lifecycle events for records the loader rejects.
type unifiedDiffRecordClassifier struct {
	oldLine int
	newLine int
	inHunk  bool
	sawHunk bool
}

func (c *unifiedDiffRecordClassifier) classify(line string) (jsonStr string, added bool, ok bool) {
	if matches := unifiedDiffHunkPattern.FindStringSubmatch(line); matches != nil {
		oldLine, oldErr := strconv.Atoi(matches[1])
		newLine, newErr := strconv.Atoi(matches[2])
		if oldErr == nil && newErr == nil {
			c.oldLine = oldLine
			c.newLine = newLine
			c.inHunk = true
			c.sawHunk = true
		}
		return "", false, false
	}
	if strings.HasPrefix(line, "diff --git ") {
		c.oldLine = 0
		c.newLine = 0
		c.inHunk = false
		c.sawHunk = false
		return "", false, false
	}
	if len(line) == 0 {
		return "", false, false
	}

	switch line[0] {
	case '+':
		allowBOM := !c.sawHunk || (c.inHunk && c.newLine == 1)
		jsonStr, added, ok = beadJSONFromDiffLineAt(line, allowBOM)
		if c.inHunk && !strings.HasPrefix(line, "+++") {
			c.newLine++
		}
		return jsonStr, added, ok
	case '-':
		allowBOM := !c.sawHunk || (c.inHunk && c.oldLine == 1)
		jsonStr, added, ok = beadJSONFromDiffLineAt(line, allowBOM)
		if c.inHunk && !strings.HasPrefix(line, "---") {
			c.oldLine++
		}
		return jsonStr, added, ok
	case ' ':
		oldJSON, oldEligible := beadJSONFromPhysicalLine(line[1:], c.inHunk && c.oldLine == 1)
		newJSON, newEligible := beadJSONFromPhysicalLine(line[1:], c.inHunk && c.newLine == 1)
		if c.inHunk {
			c.oldLine++
			c.newLine++
		}
		switch {
		case oldEligible && !newEligible:
			return oldJSON, false, true
		case !oldEligible && newEligible:
			return newJSON, true, true
		}
	}
	return "", false, false
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
		ID     string `json:"id"`
		Status string `json:"status"`
		Title  string `json:"title"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &partial); err != nil {
		return beadSnapshot{}, false
	}

	if partial.ID == "" {
		return beadSnapshot{}, false
	}

	return beadSnapshot{
		ID:     partial.ID,
		Status: partial.Status,
		Title:  partial.Title,
	}, true
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
