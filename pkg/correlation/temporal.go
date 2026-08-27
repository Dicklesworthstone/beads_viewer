// Package correlation provides temporal correlation of commits to beads based on authorship and time windows.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
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
		fmt.Sprintf("--author=%s", authorRegex),
		fmt.Sprintf("--since=%s", window.Start.Format(time.RFC3339)),
		fmt.Sprintf("--until=%s", window.End.Format(time.RFC3339)),
		"--format=" + gitLogHeaderFormat,
		"--no-merges",
	}

	cmd := lifecycleGitLogCommand(t.ctx, t.repoPath, args...)

	out, err := cmd.Output()
	if err != nil {
		// A successful log with no matching commits exits zero and returns empty
		// output. A non-zero exit with empty stderr is still a failure; in
		// particular, CommandContext commonly reports cancellation that way after
		// killing Git. Preserve cancellation for callers instead of converting it
		// into a successful, cacheable empty result.
		if t.ctx != nil {
			if ctxErr := t.ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("git log canceled: %w", ctxErr)
			}
		}
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	// Extract path hints from bead title for confidence scoring
	pathHints := extractPathHints(window.Title)

	// Parse commits
	var commits []CorrelatedCommit
	objectIDWidth := 0
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
			return nil, fmt.Errorf("parse temporal commit header: %w", err)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(info.SHA)
		} else if len(info.SHA) != objectIDWidth {
			return nil, fmt.Errorf("mixed-width temporal commit object IDs: got %d and %d characters", objectIDWidth, len(info.SHA))
		}

		sha := info.SHA

		// Skip commits already correlated via higher-confidence methods
		if t.seenCommits[sha] {
			continue
		}

		// Skip commits that touch beads files (those are handled by co-commit extractor).
		// An unreadable/malformed diff is not evidence that the commit touches Beads;
		// propagate it so callers cannot mistake an incomplete scan for success.
		touchesBeads, err := t.touchesBeadsFile(sha)
		if err != nil {
			return nil, fmt.Errorf("checking Beads-file changes for temporal commit %s: %w", sha, err)
		}
		if touchesBeads {
			continue
		}

		// Get file changes for this commit
		files, err := t.coCommitter.ExtractCoCommittedFiles(BeadEvent{CommitSHA: sha})
		if err != nil {
			if t.ctx != nil {
				if ctxErr := t.ctx.Err(); ctxErr != nil {
					err = ctxErr
				}
			}
			return nil, fmt.Errorf("extracting code files for temporal commit %s: %w", sha, err)
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

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan temporal commit output: %w", err)
	}
	return commits, nil
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

// touchesBeadsFile checks if a commit modifies any beads file.
func (t *TemporalCorrelator) touchesBeadsFile(sha string) (bool, error) {
	// Force rename detection off so a move from .beads/ to another directory
	// exposes both the deleted pre-image and added post-image. NUL framing keeps
	// control characters in paths opaque, while the shared co-commit policy pins
	// every diff/config input and repository-routing environment variable.
	gitArgs := append([]string{"show"}, coCommitDiffArgs("--name-status")...)
	gitArgs = append(gitArgs, "--no-renames", "--format=", "--end-of-options", sha, "--")
	cmd := coCommitGitCommand(t.ctx, t.repoPath, gitArgs)

	out, err := cmd.Output()
	if err != nil {
		if t.ctx != nil {
			if ctxErr := t.ctx.Err(); ctxErr != nil {
				return false, fmt.Errorf("git show canceled: %w", ctxErr)
			}
		}
		return false, fmt.Errorf("git show --name-status failed: %w", err)
	}
	files, err := parseNameStatus(out)
	if err != nil {
		return false, fmt.Errorf("parsing git show --name-status: %w", err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, ".beads/") {
			return true, nil
		}
	}
	return false, nil
}

// calculateTemporalConfidence computes dynamic confidence for temporal correlation
func (t *TemporalCorrelator) calculateTemporalConfidence(window TemporalWindow, files []FileChange, pathHints []string) float64 {
	base := 0.50

	// Factor 1: How many beads was this author working on?
	activeBeads := t.activeBeadCount(window)
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
	parts := []string{
		fmt.Sprintf("Commit by %s during bead's active window", window.Author),
	}

	windowDuration := window.End.Sub(window.Start)
	if windowDuration < 4*time.Hour {
		parts = append(parts, "short window (<4h)")
	} else if windowDuration > 7*24*time.Hour {
		parts = append(parts, fmt.Sprintf("long window (%dd)", int(windowDuration.Hours()/24)))
	}

	activeBeads := t.activeBeadCount(window)
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
	// Milestones are a lossy legacy summary: Claimed is the first claim while
	// Reopened is the latest reopen, so they cannot represent a later reclaim.
	// Prefer the most recent activation identity that they can express. The
	// production ExtractAllTemporalCorrelations path derives the interval from
	// the complete chronological Events slice instead.
	if milestones.Closed == nil {
		return nil
	}
	startEvent := latestTemporalActivation(milestones)
	if startEvent == nil || startEvent.Timestamp.IsZero() || milestones.Closed.Timestamp.Before(startEvent.Timestamp) {
		return nil
	}

	return &TemporalWindow{
		BeadID:      beadID,
		Title:       title,
		Author:      startEvent.Author,
		AuthorEmail: startEvent.AuthorEmail,
		Start:       startEvent.Timestamp,
		End:         milestones.Closed.Timestamp,
	}
}

func latestTemporalActivation(milestones BeadMilestones) *BeadEvent {
	activation := milestones.Claimed
	if milestones.Reopened != nil && (activation == nil || milestones.Reopened.Timestamp.After(activation.Timestamp)) {
		activation = milestones.Reopened
	}
	return activation
}

// latestTemporalActivityStart returns the beginning of the most recent
// continuous active interval. A bead is inactive between a close and a later
// reopen, so using its first claim would manufacture temporal evidence across
// that gap.
func latestTemporalActivityStart(milestones BeadMilestones) time.Time {
	if activation := latestTemporalActivation(milestones); activation != nil {
		return activation.Timestamp
	}
	return time.Time{}
}

func extractTemporalWindowFromHistory(beadID string, history BeadHistory) *TemporalWindow {
	if len(history.Events) == 0 {
		return ExtractWindowFromMilestones(beadID, history.Title, history.Milestones)
	}

	events := append([]BeadEvent(nil), history.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	var activation *BeadEvent
	var completed *TemporalWindow
	for i := range events {
		event := &events[i]
		switch event.EventType {
		case EventClaimed, EventReopened:
			activation = event
			// A later activation means the previous completed interval is no
			// longer the bead's terminal state until another close arrives.
			completed = nil
		case EventClosed:
			if activation != nil && !event.Timestamp.Before(activation.Timestamp) {
				completed = &TemporalWindow{
					BeadID:      beadID,
					Title:       history.Title,
					Author:      activation.Author,
					AuthorEmail: activation.AuthorEmail,
					Start:       activation.Timestamp,
					End:         event.Timestamp,
				}
			}
			activation = nil
		}
	}
	return completed
}

// temporalActivityIntervalsAt derives author-owned activity intervals that
// can overlap a target window ending at through. Open intervals are bounded by
// through solely for overlap counting; they are never returned as completed
// target windows.
func temporalActivityIntervalsAt(beadID string, history BeadHistory, through time.Time) []TemporalWindow {
	if len(history.Events) == 0 {
		activation := latestTemporalActivation(history.Milestones)
		if activation == nil || activation.Timestamp.IsZero() || activation.Timestamp.After(through) {
			return nil
		}
		end := through
		if closed := history.Milestones.Closed; closed != nil && !closed.Timestamp.Before(activation.Timestamp) && closed.Timestamp.Before(end) {
			end = closed.Timestamp
		}
		return []TemporalWindow{{
			BeadID:      beadID,
			Title:       history.Title,
			Author:      activation.Author,
			AuthorEmail: activation.AuthorEmail,
			Start:       activation.Timestamp,
			End:         end,
		}}
	}

	events := append([]BeadEvent(nil), history.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	intervals := make([]TemporalWindow, 0)
	var activation *BeadEvent
	for i := range events {
		event := &events[i]
		if event.Timestamp.After(through) {
			break
		}
		switch event.EventType {
		case EventClaimed, EventReopened:
			activation = event
		case EventClosed:
			if activation != nil && !event.Timestamp.Before(activation.Timestamp) {
				intervals = append(intervals, TemporalWindow{
					BeadID:      beadID,
					Title:       history.Title,
					Author:      activation.Author,
					AuthorEmail: activation.AuthorEmail,
					Start:       activation.Timestamp,
					End:         event.Timestamp,
				})
			}
			activation = nil
		}
	}
	if activation != nil && !through.Before(activation.Timestamp) {
		intervals = append(intervals, TemporalWindow{
			BeadID:      beadID,
			Title:       history.Title,
			Author:      activation.Author,
			AuthorEmail: activation.AuthorEmail,
			Start:       activation.Timestamp,
			End:         through,
		})
	}
	return intervals
}

func sameTemporalAuthor(first, second TemporalWindow) bool {
	firstEmail := strings.TrimSpace(first.AuthorEmail)
	secondEmail := strings.TrimSpace(second.AuthorEmail)
	if firstEmail != "" || secondEmail != "" {
		return firstEmail != "" && secondEmail != "" && strings.EqualFold(firstEmail, secondEmail)
	}
	firstName := strings.TrimSpace(first.Author)
	secondName := strings.TrimSpace(second.Author)
	return firstName != "" && secondName != "" && strings.EqualFold(firstName, secondName)
}

func countConcurrentTemporalBeads(histories map[string]BeadHistory, target TemporalWindow) int {
	concurrent := 0
	for beadID, history := range histories {
		for _, interval := range temporalActivityIntervalsAt(beadID, history, target.End) {
			if sameTemporalAuthor(target, interval) && interval.Start.Before(target.End) && interval.End.After(target.Start) {
				concurrent++
				break
			}
		}
	}
	return concurrent
}

// ExtractAllTemporalCorrelations finds temporal correlations for all beads with completed windows
func (t *TemporalCorrelator) ExtractAllTemporalCorrelations(histories map[string]BeadHistory) ([]CorrelatedCommit, error) {
	var allCommits []CorrelatedCommit
	beadIDs := make([]string, 0, len(histories))
	for beadID := range histories {
		beadIDs = append(beadIDs, beadID)
	}
	sort.Strings(beadIDs)

	for _, beadID := range beadIDs {
		history := histories[beadID]
		window := extractTemporalWindowFromHistory(beadID, history)
		if window == nil {
			continue
		}

		// Calculate concurrent beads for this author during this window
		window.ActiveBeads = countConcurrentTemporalBeads(histories, *window)

		commits, err := t.FindCommitsInWindow(*window)
		if err != nil {
			return nil, fmt.Errorf("finding temporal commits for bead %q: %w", beadID, err)
		}

		allCommits = append(allCommits, commits...)
	}

	return allCommits, nil
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
