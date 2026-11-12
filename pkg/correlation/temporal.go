// Package correlation provides temporal correlation of commits to beads based on authorship and time windows.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TemporalCorrelator finds commits by the same author within a bead's active time window
type TemporalCorrelator struct {
	repoPath     string
	coCommitter  *CoCommitExtractor // For getting file changes
	seenCommits  map[string]bool    // Track commits already correlated by higher-confidence methods
	activeByAuth map[string]int     // Count of active beads per author (for confidence scoring)

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// the correlator (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the correlator (and its co-commit extractor) so
// their git subprocesses are cancelled when ctx is done (issue #166). Returns
// the receiver for chaining.
func (t *TemporalCorrelator) WithContext(ctx context.Context) *TemporalCorrelator {
	t.ctx = ctx
	if t.coCommitter != nil {
		t.coCommitter.ctx = ctx
	}
	return t
}

// NewTemporalCorrelator creates a new temporal correlator
func NewTemporalCorrelator(repoPath string) *TemporalCorrelator {
	return &TemporalCorrelator{
		repoPath:     repoPath,
		coCommitter:  NewCoCommitExtractor(repoPath),
		seenCommits:  make(map[string]bool),
		activeByAuth: make(map[string]int),
	}
}

// SetSeenCommits marks commits that were already correlated via higher-confidence methods
func (t *TemporalCorrelator) SetSeenCommits(commits []CorrelatedCommit) {
	for _, c := range commits {
		t.seenCommits[c.SHA] = true
	}
}

// SetActiveBeadsPerAuthor sets the count of active beads per author for confidence calculation
func (t *TemporalCorrelator) SetActiveBeadsPerAuthor(counts map[string]int) {
	t.activeByAuth = counts
}

// TemporalWindow represents the time window when a bead was actively being worked on
type TemporalWindow struct {
	BeadID      string
	Title       string
	Author      string
	AuthorEmail string
	Start       time.Time // When bead was claimed
	End         time.Time // When bead was closed
	ActiveBeads int       // How many beads the author had active during this window
}

// FindCommitsInWindow finds commits by the specified author within the given time window
func (t *TemporalCorrelator) FindCommitsInWindow(window TemporalWindow) ([]CorrelatedCommit, error) {
	authorRegex := gitAuthorRegex(window.Author, window.AuthorEmail)
	if authorRegex == "" {
		return nil, nil
	}

	// Build git log command with author and date filters
	args := []string{
		"log",
		fmt.Sprintf("--author=%s", authorRegex),
		fmt.Sprintf("--since=%s", window.Start.Format(time.RFC3339)),
		fmt.Sprintf("--until=%s", window.End.Format(time.RFC3339)),
		"--format=" + gitLogHeaderFormat,
		"--no-merges",
	}

	cmd := gitCommand(t.ctx, args...)
	cmd.Dir = t.repoPath

	out, err := cmd.Output()
	if err != nil {
		// No commits found is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	// Extract path hints from bead title for confidence scoring
	pathHints := extractPathHints(window.Title)

	// Parse commits
	var commits []CorrelatedCommit
	scanner := bufio.NewScanner(bytes.NewReader(out))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		info, err := parseCommitInfo(line)
		if err != nil {
			continue
		}

		sha := info.SHA

		// Skip commits already correlated via higher-confidence methods
		if t.seenCommits[sha] {
			continue
		}

		// Skip pure tracker-bookkeeping commits (100% .beads/ paths);
		// mixed code + .beads/ commits are admitted.
		if t.isBeadsOnlyCommit(sha) {
			continue
		}

		// Get file changes for this commit
		files, err := t.coCommitter.ExtractCoCommittedFiles(BeadEvent{CommitSHA: sha})
		if err != nil {
			continue
		}

		// Skip if no code files
		if len(files) == 0 {
			continue
		}

		// Calculate dynamic confidence
		confidence := t.calculateTemporalConfidence(window, files, pathHints)
		reason := t.generateTemporalReason(window, files, pathHints)

		commits = append(commits, CorrelatedCommit{
			BeadID:      window.BeadID,
			SHA:         sha,
			ShortSHA:    shortSHA(sha),
			Message:     info.Message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
			Timestamp:   info.Timestamp,
			Files:       files,
			Method:      MethodTemporalAuthor,
			Confidence:  confidence,
			Reason:      reason,
		})
	}

	return commits, scanner.Err()
}

func gitAuthorRegex(author, authorEmail string) string {
	identity := strings.TrimSpace(authorEmail)
	if identity == "" {
		identity = strings.TrimSpace(author)
	}
	if identity == "" {
		return ""
	}
	return regexp.QuoteMeta(identity)
}

// isBeadsOnlyCommit reports whether every modified path in the commit is
// tracker metadata under .beads/. Mixed commits touching both .beads/ and
// repository code files return false so temporal correlation evaluates them.
func (t *TemporalCorrelator) isBeadsOnlyCommit(sha string) bool {
	cmd := gitCommand(t.ctx, "show", "--name-only", "--format=", sha)
	cmd.Dir = t.repoPath

	out, err := cmd.Output()
	if err != nil {
		return false
	}

	total := 0
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		path := strings.TrimSpace(scanner.Text())
		if path == "" {
			continue
		}
		total++
		if !strings.HasPrefix(path, ".beads/") {
			return false
		}
	}
	return total > 0
}

// calculateTemporalConfidence computes dynamic confidence for temporal correlation
func (t *TemporalCorrelator) calculateTemporalConfidence(window TemporalWindow, files []FileChange, pathHints []string) float64 {
	return temporalConfidence(window, t.activeBeadCount(window), files, pathHints)
}

// temporalConfidence is the receiver-free confidence model shared by the
// per-window git path (TemporalCorrelator) and the walk-based strategy the
// Correlator runs; activeBeads is the number of beads the author had active
// during the window (already resolved by the caller).
func temporalConfidence(window TemporalWindow, activeBeads int, files []FileChange, pathHints []string) float64 {
	base := 0.50

	// Factor 1: How many beads was this author working on?
	if activeBeads <= 1 {
		base += 0.20 // Only one bead = higher confidence
	} else if activeBeads == 2 {
		base += 0.10
	} else if activeBeads > 3 {
		base -= 0.10 // Many beads = lower confidence
	}

	// Factor 2: How long is the time window?
	windowDuration := window.End.Sub(window.Start)
	if windowDuration < 4*time.Hour {
		base += 0.10 // Short window = more focused
	} else if windowDuration < 24*time.Hour {
		base += 0.05
	} else if windowDuration > 7*24*time.Hour {
		base -= 0.15 // Week+ window = lots of potential commits
	} else if windowDuration > 3*24*time.Hour {
		base -= 0.05
	}

	// Factor 3: Do commit files match path hints from bead title?
	if len(pathHints) > 0 && pathsMatchHints(files, pathHints) {
		base += 0.15 // File paths match keywords in title
	}

	// Clamp to [0.20, 0.85] - temporal correlation should never be too confident
	return clamp(base, 0.20, 0.85)
}

// generateTemporalReason creates a human-readable explanation for the correlation
func (t *TemporalCorrelator) generateTemporalReason(window TemporalWindow, files []FileChange, pathHints []string) string {
	return temporalReason(window, t.activeBeadCount(window), files, pathHints)
}

// temporalReason is the receiver-free reason builder paired with
// temporalConfidence.
func temporalReason(window TemporalWindow, activeBeads int, files []FileChange, pathHints []string) string {
	parts := []string{
		fmt.Sprintf("Commit by %s during bead's active window", window.Author),
	}

	windowDuration := window.End.Sub(window.Start)
	if windowDuration < 4*time.Hour {
		parts = append(parts, "short window (<4h)")
	} else if windowDuration > 7*24*time.Hour {
		parts = append(parts, fmt.Sprintf("long window (%dd)", int(windowDuration.Hours()/24)))
	}

	if activeBeads <= 1 {
		parts = append(parts, "author had only this bead active")
	} else if activeBeads > 3 {
		parts = append(parts, fmt.Sprintf("author had %d beads active", activeBeads))
	}

	if len(pathHints) > 0 && pathsMatchHints(files, pathHints) {
		parts = append(parts, "file paths match bead title keywords")
	}

	return strings.Join(parts, "; ")
}

func (t *TemporalCorrelator) activeBeadCount(window TemporalWindow) int {
	if window.ActiveBeads > 0 {
		return window.ActiveBeads
	}
	if t.activeByAuth != nil {
		if count, ok := t.activeByAuth[window.AuthorEmail]; ok {
			return count
		}
	}
	return 1
}

// pathHintPatterns extracts potential file-related keywords from text
var pathHintPattern = regexp.MustCompile(`(?i)\b(?:` +
	// File paths
	`(?:[a-z_][a-z0-9_]*(?:/[a-z_][a-z0-9_]*)+(?:\.[a-z]+)?)|` +
	// Package/module names
	`(?:pkg|src|lib|internal|cmd|app)/[a-z_][a-z0-9_]*|` +
	// Component/feature keywords
	`(?:auth|login|user|api|db|database|config|service|handler|controller|model|view|component|util|helper|test|tests)` +
	`)\b`)

// extractPathHints extracts potential file paths and keywords from bead title
func extractPathHints(title string) []string {
	matches := pathHintPattern.FindAllString(strings.ToLower(title), -1)
	if len(matches) == 0 {
		return nil
	}

	// Deduplicate
	seen := make(map[string]bool)
	var hints []string
	for _, m := range matches {
		m = strings.ToLower(m)
		if !seen[m] {
			seen[m] = true
			hints = append(hints, m)
		}
	}
	return hints
}

// pathsMatchHints checks if any file path contains any of the hints
func pathsMatchHints(files []FileChange, hints []string) bool {
	for _, f := range files {
		lowerPath := strings.ToLower(f.Path)
		for _, hint := range hints {
			if strings.Contains(lowerPath, hint) {
				return true
			}
		}
	}
	return false
}

// clamp restricts a value to the given range
func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// ExtractWindowFromMilestones creates a TemporalWindow from bead milestones
func ExtractWindowFromMilestones(beadID, title string, milestones BeadMilestones) *TemporalWindow {
	// Need both claimed and closed events to define a window
	if milestones.Claimed == nil || milestones.Closed == nil {
		return nil
	}

	return &TemporalWindow{
		BeadID:      beadID,
		Title:       title,
		Author:      milestones.Claimed.Author,
		AuthorEmail: milestones.Claimed.AuthorEmail,
		Start:       milestones.Claimed.Timestamp,
		End:         milestones.Closed.Timestamp,
	}
}

// ExtractAllTemporalCorrelations finds temporal correlations for all beads with completed windows
func (t *TemporalCorrelator) ExtractAllTemporalCorrelations(histories map[string]BeadHistory) ([]CorrelatedCommit, error) {
	var allCommits []CorrelatedCommit

	for beadID, history := range histories {
		window := ExtractWindowFromMilestones(beadID, history.Title, history.Milestones)
		if window == nil {
			continue
		}

		// Calculate concurrent beads for this author during this window
		concurrent := 0
		for _, otherHistory := range histories {
			if otherHistory.Milestones.Claimed != nil && otherHistory.Milestones.Claimed.AuthorEmail == window.AuthorEmail {
				start := otherHistory.Milestones.Claimed.Timestamp
				var end time.Time
				if otherHistory.Milestones.Closed != nil {
					end = otherHistory.Milestones.Closed.Timestamp
				} else {
					end = time.Now()
				}

				// Check for overlap: start1 < end2 && end1 > start2
				if start.Before(window.End) && end.After(window.Start) {
					concurrent++
				}
			}
		}
		window.ActiveBeads = concurrent

		commits, err := t.FindCommitsInWindow(*window)
		if err != nil {
			// Non-fatal: skip this bead
			continue
		}

		allCommits = append(allCommits, commits...)
	}

	return allCommits, nil
}

// temporalCandidate is a walk-based temporal match stored in the history
// artifact BEFORE confidence is assigned. Confidence depends on the bead's
// title (path hints, +0.15), and titles come from the working-tree bead set,
// which the HEAD-keyed artifact cache deliberately excludes; so the artifact
// keeps only HEAD-derived facts and assembleReport finalizes the confidence
// against the current title via finalizeTemporalCandidate.
type temporalCandidate struct {
	BeadID       string       `json:"bead_id"`
	SHA          string       `json:"sha"`
	Message      string       `json:"message"`
	Author       string       `json:"author"`
	AuthorEmail  string       `json:"author_email"`
	Timestamp    time.Time    `json:"timestamp"`
	Files        []FileChange `json:"files"`
	WindowAuthor string       `json:"window_author"` // claimant display name (for the reason text)
	WindowStart  time.Time    `json:"window_start"`
	WindowEnd    time.Time    `json:"window_end"`
	ActiveBeads  int          `json:"active_beads"`
}

// temporalWindowsFromEvents derives one claimed→closed window per bead from
// extracted lifecycle events (chronological), with ActiveBeads set to the
// number of beads the same claimant had claimed and still open at any point
// during the window (itself included). Unlike ExtractAllTemporalCorrelations
// it never consults time.Now(): a bead that was never closed is treated as
// open for the rest of history, which is the same answer for every past
// window and keeps the artifact deterministic. Windows are returned in bead
// id order.
func temporalWindowsFromEvents(events []BeadEvent, beadFilter string) []TemporalWindow {
	eventsByBead := make(map[string][]BeadEvent)
	for _, ev := range events {
		eventsByBead[ev.BeadID] = append(eventsByBead[ev.BeadID], ev)
	}
	beadIDs := make([]string, 0, len(eventsByBead))
	for id := range eventsByBead {
		beadIDs = append(beadIDs, id)
	}
	sort.Strings(beadIDs)

	type claimSpan struct {
		email  string
		start  time.Time
		end    time.Time
		closed bool
	}
	spans := make(map[string]claimSpan, len(beadIDs))
	var windows []TemporalWindow
	for _, id := range beadIDs {
		m := GetBeadMilestones(eventsByBead[id])
		if m.Claimed == nil {
			continue
		}
		span := claimSpan{email: m.Claimed.AuthorEmail, start: m.Claimed.Timestamp}
		if m.Closed != nil {
			span.end = m.Closed.Timestamp
			span.closed = true
		}
		spans[id] = span
		if beadFilter != "" && id != beadFilter {
			continue
		}
		if w := ExtractWindowFromMilestones(id, "", m); w != nil {
			windows = append(windows, *w)
		}
	}
	for i := range windows {
		w := &windows[i]
		concurrent := 0
		for _, span := range spans {
			if span.email != w.AuthorEmail {
				continue
			}
			if !span.start.Before(w.End) {
				continue
			}
			if span.closed && !span.end.After(w.Start) {
				continue
			}
			concurrent++
		}
		w.ActiveBeads = concurrent
	}
	return windows
}

// commitInWindow reports whether a walked commit was authored by the window's
// claimant (email match when the claim carries one, else display name) inside
// [Start, End]. Matching by email rather than a regex over "Name <email>"
// avoids the substring false positives of `git log --author`.
func commitInWindow(w TemporalWindow, wc walkedCommit) bool {
	if wc.Timestamp.Before(w.Start) || wc.Timestamp.After(w.End) {
		return false
	}
	if w.AuthorEmail != "" {
		return strings.EqualFold(wc.AuthorEmail, w.AuthorEmail)
	}
	if w.Author != "" {
		return strings.EqualFold(wc.Author, w.Author)
	}
	return false
}

// finalizeTemporalCandidate turns an artifact candidate into a scored
// CorrelatedCommit using the bead's CURRENT title for path hints.
func finalizeTemporalCandidate(cand temporalCandidate, title string) CorrelatedCommit {
	window := TemporalWindow{
		BeadID:      cand.BeadID,
		Title:       title,
		Author:      cand.WindowAuthor,
		Start:       cand.WindowStart,
		End:         cand.WindowEnd,
		ActiveBeads: cand.ActiveBeads,
	}
	active := cand.ActiveBeads
	if active <= 0 {
		active = 1
	}
	hints := extractPathHints(title)
	return CorrelatedCommit{
		BeadID:      cand.BeadID,
		SHA:         cand.SHA,
		ShortSHA:    shortSHA(cand.SHA),
		Message:     cand.Message,
		Author:      cand.Author,
		AuthorEmail: cand.AuthorEmail,
		Timestamp:   cand.Timestamp,
		Files:       cand.Files,
		Method:      MethodTemporalAuthor,
		Confidence:  temporalConfidence(window, active, cand.Files, hints),
		Reason:      temporalReason(window, active, cand.Files, hints),
	}
}

// calculateActiveBeadsPerAuthor computes how many beads each author had in progress
// Deprecated: Concurrent active beads are now calculated per-window in ExtractAllTemporalCorrelations
func (t *TemporalCorrelator) calculateActiveBeadsPerAuthor(histories map[string]BeadHistory) {
	authorCounts := make(map[string]int)

	for _, history := range histories {
		if history.Milestones.Claimed != nil {
			email := history.Milestones.Claimed.AuthorEmail
			authorCounts[email]++
		}
	}

	t.activeByAuth = authorCounts
}
