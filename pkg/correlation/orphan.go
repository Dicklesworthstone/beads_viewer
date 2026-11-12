// Package correlation provides orphan commit detection with smart heuristics.
package correlation

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// OrphanSignal represents a reason why a commit might be orphaned.
type OrphanSignal string

const (
	// SignalOrphanTiming: Commit during active bead window without linkage
	SignalOrphanTiming OrphanSignal = "timing"
	// SignalOrphanFiles: Commit touches files associated with a bead
	SignalOrphanFiles OrphanSignal = "files"
	// SignalOrphanMessage: Commit message contains bead-like patterns
	SignalOrphanMessage OrphanSignal = "message"
	// SignalOrphanAuthor: Author has linked commits nearby
	SignalOrphanAuthor OrphanSignal = "author"
)

// Pre-compiled regex patterns for generic action-verb / issue-number signals
// on unlinked commits. Explicit bead ID matching delegates to ExplicitMatcher
// so Correlator and OrphanDetector share a single canonical matcher.
var orphanMessagePatterns = []struct {
	re     *regexp.Regexp
	weight int
}{
	{regexp.MustCompile(`\b(fix|fixes|fixed)\b`), 10},
	{regexp.MustCompile(`\b(close|closes|closed)\b`), 10},
	{regexp.MustCompile(`\b(resolve|resolves|resolved)\b`), 10},
	{regexp.MustCompile(`\b(implement|implements|implemented)\b`), 8},
	{regexp.MustCompile(`\b(add|adds|added)\b`), 5},
	{regexp.MustCompile(`#\d+`), 15}, // Issue number reference
}

// OrphanCandidate represents a commit that might be missing a bead linkage.
type OrphanCandidate struct {
	// Commit information
	SHA         string    `json:"sha"`
	ShortSHA    string    `json:"short_sha"`
	Message     string    `json:"message"`
	Author      string    `json:"author"`
	AuthorEmail string    `json:"author_email"`
	Timestamp   time.Time `json:"timestamp"`
	Files       []string  `json:"files,omitempty"` // Files changed in this commit

	// Detection results
	SuspicionScore int               `json:"suspicion_score"` // 0-100
	ProbableBeads  []ProbableBead    `json:"probable_beads"`  // Beads this might belong to
	Signals        []OrphanSignalHit `json:"signals"`         // Why we think it's orphaned
}

// ProbableBead is a bead that an orphan commit might belong to.
type ProbableBead struct {
	BeadID     string   `json:"bead_id"`
	BeadTitle  string   `json:"bead_title"`
	BeadStatus string   `json:"bead_status"`
	Confidence int      `json:"confidence"` // 0-100
	Reasons    []string `json:"reasons"`    // Why we think it matches
}

// OrphanSignalHit records a detected signal with details.
type OrphanSignalHit struct {
	Signal  OrphanSignal `json:"signal"`
	Details string       `json:"details"`
	Weight  int          `json:"weight"` // Contribution to score
}

// OrphanReport is the JSON output for --robot-orphans.
type OrphanReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	GitRange    string              `json:"git_range"` // e.g., "last 30 days"
	DataHash    string              `json:"data_hash"` // Beads content hash
	Window      OrphanWindow        `json:"window"`    // Commit window scanned (aligned with the correlation index)
	Stats       OrphanReportStats   `json:"stats"`
	Candidates  []OrphanCandidate   `json:"candidates"`
	ByBead      map[string][]string `json:"by_bead,omitempty"` // BeadID -> []commit SHAs
	UsageHints  []string            `json:"usage_hints"`
}

// orphanUsageHints documents how the payload is computed so an agent can read
// suspicion_score and orphan_ratio without consulting the source.
var orphanUsageHints = []string{
	"window: the non-merge commits scanned; source=history_index means the same bounded walk the correlation index (co_committed, explicit_id, temporal_author) covered, so an orphan is a code commit in that window no strategy linked",
	"stats.total_commits counts window commits that changed something outside .beads/; stats.beads_only_commits are the bookkeeping commits (e.g. `br sync`) skipped as neither orphans nor correlated, so window.commits = total_commits + beads_only_commits",
	"a high orphan_ratio usually means code and tracker updates land in separate commits; only explicit bead ids in messages, temporal author overlap, or confirm feedback can link those code commits",
	"suspicion_score (0-100, capped) sums signal weights: timing=30 per bead whose claimed→closed window contains the commit, files=25 per (file, bead) pair where a linked bead touched the same path, message<=35 for bead-like patterns in the subject, author=15 per bead the same author worked on within +/-7 days",
	"probable_beads.confidence adds 35 when the message names the bead id; the top 3 beads are kept; candidates below --orphans-min-score are dropped",
	"link a candidate with --robot-confirm-correlation SHA:BEAD (or exclude a false link with --robot-reject-correlation); both shape later history reports",
}

// OrphanReportStats provides aggregate statistics.
type OrphanReportStats struct {
	TotalCommits     int     `json:"total_commits"`
	CorrelatedCount  int     `json:"correlated_count"`
	OrphanCount      int     `json:"orphan_count"`
	BeadsOnlyCommits int     `json:"beads_only_commits"` // Window commits skipped because they changed nothing outside .beads/
	CandidateCount   int     `json:"candidate_count"`    // Orphans with probable beads
	OrphanRatio      float64 `json:"orphan_ratio"`
	AvgSuspicion     float64 `json:"avg_suspicion_score"`
}

// OrphanDetector finds commits that probably should be linked to beads.
type OrphanDetector struct {
	repoPath    string
	dataHash    string
	lookup      *ReverseLookup
	fileLookup  *FileLookup
	explicit    *ExplicitMatcher
	beadWindows map[string]TemporalWindow // BeadID -> active time window
	authorBeads map[string][]string       // Author email -> BeadIDs they worked on
	now         time.Time
	window      *HistoryWindow // The correlation index's commit window, when the report carries one
}

// WithExplicitMatcher configures OrphanDetector and its underlying ReverseLookup
// to share an ExplicitMatcher (augmented with all known Bead IDs in the report).
func (od *OrphanDetector) WithExplicitMatcher(m *ExplicitMatcher) *OrphanDetector {
	if m == nil {
		return od
	}
	var known []string
	if od.lookup != nil {
		for id, history := range od.lookup.beads {
			known = append(known, id)
			if history.BeadID != "" && history.BeadID != id {
				known = append(known, history.BeadID)
			}
			for _, ev := range history.Events {
				if ev.BeadID != "" {
					known = append(known, ev.BeadID)
				}
			}
		}
	}
	shared := m.withMergedKnownIDs(known)
	od.explicit = shared
	if od.lookup != nil {
		od.lookup.explicit = shared
	}
	return od
}

// NewOrphanDetector creates a detector from a history report.
func NewOrphanDetector(report *HistoryReport, repoPath string) *OrphanDetector {
	return NewOrphanDetectorAt(report, repoPath, time.Now())
}

// NewOrphanDetectorAt creates a detector with a caller-owned reference instant
// for open activity windows and serialized report metadata. The zero instant is
// valid and is preserved (SOURCE_DATE_EPOCH can legitimately map to it).
func NewOrphanDetectorAt(report *HistoryReport, repoPath string, now time.Time) *OrphanDetector {
	return newOrphanDetector(report, repoPath, now)
}

// NewSmartOrphanDetector is an alias for NewOrphanDetector for compatibility.
func NewSmartOrphanDetector(report *HistoryReport, repoPath string) *OrphanDetector {
	return NewOrphanDetector(report, repoPath)
}

// newOrphanDetector is the internal constructor.
func newOrphanDetector(report *HistoryReport, repoPath string, now time.Time) *OrphanDetector {
	lookup := NewReverseLookupWithRepo(report, repoPath)
	od := &OrphanDetector{
		repoPath:    repoPath,
		dataHash:    report.DataHash,
		lookup:      lookup,
		fileLookup:  NewFileLookup(report),
		explicit:    lookup.explicit,
		beadWindows: make(map[string]TemporalWindow),
		authorBeads: make(map[string][]string),
		now:         now,
		window:      report.Window,
	}

	// Build temporal windows for each bead
	for beadID, history := range report.Histories {
		if history.Milestones.Claimed != nil {
			start := history.Milestones.Claimed.Timestamp
			end := orphanActivityWindowEnd(history, now)
			if !isClosedHistoryStatus(history.Status) && history.Milestones.Reopened != nil && history.Milestones.Reopened.Timestamp.After(start) {
				start = history.Milestones.Reopened.Timestamp
			}
			od.beadWindows[beadID] = TemporalWindow{
				BeadID: beadID,
				Title:  history.Title,
				Start:  start,
				End:    end,
			}
		}

		// Build author -> beads mapping
		if history.LastAuthor != "" {
			for _, commit := range history.Commits {
				if commit.AuthorEmail != "" {
					od.authorBeads[commit.AuthorEmail] = appendUnique(
						od.authorBeads[commit.AuthorEmail], beadID)
				}
			}
		}
	}
	for author := range od.authorBeads {
		sort.Strings(od.authorBeads[author])
	}

	return od
}

func isClosedHistoryStatus(status string) bool {
	normalized := normalizeStatus(status)
	return normalized == "closed" || normalized == "tombstone"
}

func orphanActivityWindowEnd(history BeadHistory, now time.Time) time.Time {
	if isClosedHistoryStatus(history.Status) && history.Milestones.Closed != nil {
		return history.Milestones.Closed.Timestamp
	}
	return now
}

// DetectOrphans finds orphan commits with smart detection. When the history
// report the detector was built from records the window its correlation index
// covers, that window is scanned (and reported with source "history_index")
// regardless of opts: an orphan is meaningful only relative to the commits the
// index actually had a chance to correlate. opts is used as-is only for
// reports assembled without a walk.
func (od *OrphanDetector) DetectOrphans(opts ExtractOptions) (*OrphanReport, error) {
	source := "options"
	if od.window != nil {
		opts = ExtractOptions{Revision: od.window.Revision, Limit: od.window.Limit, Since: od.window.Since, Until: od.window.Until}
		source = "history_index"
	}

	// Get basic orphans first
	orphans, stats, err := od.lookup.FindOrphanCommits(opts)
	if err != nil {
		return nil, fmt.Errorf("finding orphan commits: %w", err)
	}
	stats.Window.Source = source

	report := &OrphanReport{
		GeneratedAt: od.now,
		GitRange:    formatGitRange(opts),
		DataHash:    od.dataHash,
		Window:      stats.Window,
		Candidates:  make([]OrphanCandidate, 0, len(orphans)),
		ByBead:      make(map[string][]string),
		UsageHints:  append([]string(nil), orphanUsageHints...),
	}

	// Analyze each orphan
	var totalSuspicion int
	candidateCount := 0

	for _, orphan := range orphans {
		candidate, err := od.analyzeOrphan(orphan)
		if err != nil {
			return nil, fmt.Errorf("analyzing orphan commit %s: %w", orphan.SHA, err)
		}

		if candidate.SuspicionScore > 0 {
			report.Candidates = append(report.Candidates, candidate)
			totalSuspicion += candidate.SuspicionScore
			if len(candidate.ProbableBeads) > 0 {
				candidateCount++
				// Index by probable bead
				for _, pb := range candidate.ProbableBeads {
					report.ByBead[pb.BeadID] = append(report.ByBead[pb.BeadID], candidate.SHA)
				}
			}
		}
	}

	// Sort by suspicion score (highest first)
	sort.Slice(report.Candidates, func(i, j int) bool {
		if report.Candidates[i].SuspicionScore != report.Candidates[j].SuspicionScore {
			return report.Candidates[i].SuspicionScore > report.Candidates[j].SuspicionScore
		}
		return report.Candidates[i].SHA < report.Candidates[j].SHA
	})

	// Calculate stats
	report.Stats = OrphanReportStats{
		TotalCommits:     stats.TotalCommits,
		CorrelatedCount:  stats.CorrelatedCmts,
		OrphanCount:      stats.OrphanCommits,
		BeadsOnlyCommits: stats.BeadsOnlyCommits,
		CandidateCount:   candidateCount,
		OrphanRatio:      stats.OrphanRatio,
	}
	if len(report.Candidates) > 0 {
		report.Stats.AvgSuspicion = float64(totalSuspicion) / float64(len(report.Candidates))
	}

	return report, nil
}

// analyzeOrphan applies heuristics to an orphan commit.
func (od *OrphanDetector) analyzeOrphan(orphan OrphanCommit) (OrphanCandidate, error) {
	candidate := OrphanCandidate{
		SHA:           orphan.SHA,
		ShortSHA:      orphan.ShortSHA,
		Message:       orphan.Message,
		Author:        orphan.Author,
		AuthorEmail:   orphan.AuthorEmail,
		Timestamp:     orphan.Timestamp,
		Signals:       make([]OrphanSignalHit, 0),
		ProbableBeads: make([]ProbableBead, 0),
	}

	// Files changed in this commit: FindOrphanCommits already carries them from
	// the walk (no per-commit git show); an orphan built without them falls
	// back to asking git.
	candidate.Files = orphan.Files
	if candidate.Files == nil && od.repoPath != "" {
		files, err := od.getCommitFiles(orphan.SHA)
		if err != nil {
			return OrphanCandidate{}, err
		}
		candidate.Files = files
	}

	// Track probable beads with scores
	beadScores := make(map[string]*probableBeadBuilder)

	// Heuristic 1: Timing - commit during active bead window
	od.checkTiming(&candidate, beadScores)

	// Heuristic 2: Files - commit touches files associated with beads
	od.checkFiles(&candidate, beadScores)

	// Heuristic 3: Message - contains bead-like patterns
	od.checkMessage(&candidate, beadScores)

	// Heuristic 4: Author - has linked commits nearby
	od.checkAuthor(&candidate, beadScores)

	// Build probable beads list
	for beadID, builder := range beadScores {
		if builder.score > 0 {
			candidate.ProbableBeads = append(candidate.ProbableBeads, ProbableBead{
				BeadID:     beadID,
				BeadTitle:  builder.title,
				BeadStatus: builder.status,
				Confidence: minInt(builder.score, 100),
				Reasons:    builder.reasons,
			})
		}
	}

	// Sort probable beads by confidence
	sort.Slice(candidate.ProbableBeads, func(i, j int) bool {
		if candidate.ProbableBeads[i].Confidence != candidate.ProbableBeads[j].Confidence {
			return candidate.ProbableBeads[i].Confidence > candidate.ProbableBeads[j].Confidence
		}
		return candidate.ProbableBeads[i].BeadID < candidate.ProbableBeads[j].BeadID
	})
	for i := range candidate.ProbableBeads {
		sort.Strings(candidate.ProbableBeads[i].Reasons)
	}
	sort.Slice(candidate.Signals, func(i, j int) bool {
		if candidate.Signals[i].Signal != candidate.Signals[j].Signal {
			return candidate.Signals[i].Signal < candidate.Signals[j].Signal
		}
		if candidate.Signals[i].Details != candidate.Signals[j].Details {
			return candidate.Signals[i].Details < candidate.Signals[j].Details
		}
		return candidate.Signals[i].Weight < candidate.Signals[j].Weight
	})

	// Limit to top 3 probable beads
	if len(candidate.ProbableBeads) > 3 {
		candidate.ProbableBeads = candidate.ProbableBeads[:3]
	}

	// Calculate total suspicion score
	for _, signal := range candidate.Signals {
		candidate.SuspicionScore += signal.Weight
	}
	candidate.SuspicionScore = minInt(candidate.SuspicionScore, 100)

	return candidate, nil
}

// probableBeadBuilder accumulates evidence for a probable bead match.
type probableBeadBuilder struct {
	title   string
	status  string
	score   int
	reasons []string
}

// checkTiming checks if commit was during an active bead's time window.
func (od *OrphanDetector) checkTiming(candidate *OrphanCandidate, beadScores map[string]*probableBeadBuilder) {
	for beadID, window := range od.beadWindows {
		if candidate.Timestamp.After(window.Start) && candidate.Timestamp.Before(window.End) {
			// Commit during bead's active window
			weight := 30 // Base weight for timing match

			candidate.Signals = append(candidate.Signals, OrphanSignalHit{
				Signal:  SignalOrphanTiming,
				Details: fmt.Sprintf("Commit during %s active period", beadID),
				Weight:  weight,
			})

			if _, ok := beadScores[beadID]; !ok {
				beadScores[beadID] = &probableBeadBuilder{
					title:  window.Title,
					status: "in_progress", // If we're here, it was active
				}
			}
			beadScores[beadID].score += weight
			beadScores[beadID].reasons = append(beadScores[beadID].reasons,
				"commit during active timeframe")
		}
	}
}

// checkFiles checks if commit touches files associated with beads.
func (od *OrphanDetector) checkFiles(candidate *OrphanCandidate, beadScores map[string]*probableBeadBuilder) {
	if od.fileLookup == nil || len(candidate.Files) == 0 {
		return
	}

	for _, file := range candidate.Files {
		result := od.fileLookup.LookupByFile(file)

		// Check both open and closed beads that touched this file.
		// Use three-index slice to prevent modifying result.OpenBeads' underlying array.
		allRefs := append(result.OpenBeads[:len(result.OpenBeads):len(result.OpenBeads)], result.ClosedBeads...)
		for _, ref := range allRefs {
			weight := 25 // Base weight for file overlap

			candidate.Signals = append(candidate.Signals, OrphanSignalHit{
				Signal:  SignalOrphanFiles,
				Details: fmt.Sprintf("Touches %s (linked to %s)", file, ref.BeadID),
				Weight:  weight,
			})

			if _, ok := beadScores[ref.BeadID]; !ok {
				beadScores[ref.BeadID] = &probableBeadBuilder{
					title:  ref.Title,
					status: ref.Status,
				}
			}
			beadScores[ref.BeadID].score += weight
			beadScores[ref.BeadID].reasons = append(beadScores[ref.BeadID].reasons,
				fmt.Sprintf("touches file %s", file))
		}
	}
}

// checkMessage checks if commit message contains bead-like patterns using the
// shared ExplicitMatcher plus generic action-verb heuristics.
func (od *OrphanDetector) checkMessage(candidate *OrphanCandidate, beadScores map[string]*probableBeadBuilder) {
	msg := strings.ToLower(candidate.Message)

	// Look for generic action-verb / issue-number patterns
	totalWeight := 0
	var matchDetails []string

	for _, p := range orphanMessagePatterns {
		if p.re.MatchString(msg) {
			totalWeight += p.weight
			match := p.re.FindString(msg)
			matchDetails = append(matchDetails, match)
		}
	}

	matcher := od.explicit
	if matcher == nil {
		matcher = NewExplicitMatcher(od.repoPath)
	}
	idMatches := matcher.ExtractIDsFromMessage(candidate.Message)
	for _, idMatch := range idMatches {
		totalWeight += 25
		if idMatch.RawMatch != "" {
			matchDetails = append(matchDetails, idMatch.RawMatch)
		} else {
			matchDetails = append(matchDetails, idMatch.ID)
		}
		od.scoreMentionedBead(beadScores, idMatch.ID)
	}

	if totalWeight > 0 {
		candidate.Signals = append(candidate.Signals, OrphanSignalHit{
			Signal:  SignalOrphanMessage,
			Details: fmt.Sprintf("Message patterns: %s", strings.Join(matchDetails, ", ")),
			Weight:  minInt(totalWeight, 35),
		})
	}
}

// scoreMentionedBead credits a bead whose ID appears in a commit message.
// Lookup is case-insensitive; unknown IDs and ambiguous case-colliding IDs are
// ignored. Even an exact-case key is rejected when another key is EqualFold:
// callers such as checkMessage intentionally perform case-insensitive matching.
func (od *OrphanDetector) scoreMentionedBead(beadScores map[string]*probableBeadBuilder, beadID string) {
	matches := make([]string, 0, 1)
	for id := range od.lookup.beads {
		if strings.EqualFold(id, beadID) {
			matches = append(matches, id)
		}
	}
	if len(matches) != 1 {
		return
	}
	beadID = matches[0]
	history := od.lookup.beads[beadID]
	if _, exists := beadScores[beadID]; !exists {
		beadScores[beadID] = &probableBeadBuilder{
			title:  history.Title,
			status: history.Status,
		}
	}
	beadScores[beadID].score += 35
	beadScores[beadID].reasons = append(beadScores[beadID].reasons,
		"bead ID mentioned in commit message")
}

// checkAuthor checks if author has linked commits nearby.
func (od *OrphanDetector) checkAuthor(candidate *OrphanCandidate, beadScores map[string]*probableBeadBuilder) {
	if candidate.AuthorEmail == "" {
		return
	}

	beadIDs := od.authorBeads[candidate.AuthorEmail]
	if len(beadIDs) == 0 {
		return
	}

	// Check if any of the author's beads were active around the commit time
	for _, beadID := range beadIDs {
		window, ok := od.beadWindows[beadID]
		if !ok {
			continue
		}

		// Check if commit is within a week of the bead's active window
		windowStart := window.Start.Add(-7 * 24 * time.Hour)
		windowEnd := window.End.Add(7 * 24 * time.Hour)

		if candidate.Timestamp.After(windowStart) && candidate.Timestamp.Before(windowEnd) {
			weight := 15

			candidate.Signals = append(candidate.Signals, OrphanSignalHit{
				Signal:  SignalOrphanAuthor,
				Details: fmt.Sprintf("Author worked on %s around this time", beadID),
				Weight:  weight,
			})

			if _, ok := beadScores[beadID]; !ok {
				history, exists := od.lookup.beads[beadID]
				if !exists {
					// Bead not in lookup - use window info as fallback
					history = BeadHistory{Title: window.Title, Status: "unknown"}
				}
				beadScores[beadID] = &probableBeadBuilder{
					title:  history.Title,
					status: history.Status,
				}
			}
			beadScores[beadID].score += weight
			beadScores[beadID].reasons = append(beadScores[beadID].reasons,
				"same author worked on bead nearby")
		}
	}
}

// getCommitFiles returns files changed in a commit.
func (od *OrphanDetector) getCommitFiles(sha string) ([]string, error) {
	cocommit := &CoCommitExtractor{repoPath: od.repoPath}
	fileChanges, err := cocommit.getFilesChanged(sha)
	if err != nil {
		return nil, fmt.Errorf("get files changed: %w", err)
	}

	var result []string
	for _, fc := range fileChanges {
		result = append(result, fc.Path)
	}
	return result, nil
}

// formatGitRange formats the extraction options as a human-readable string.
func formatGitRange(opts ExtractOptions) string {
	if opts.Revision == "" && opts.Since == nil && opts.Until == nil && opts.Limit == 0 {
		return "all history"
	}

	parts := []string{}
	if opts.Revision != "" {
		parts = append(parts, "at "+opts.Revision)
	}
	if opts.Since != nil {
		parts = append(parts, fmt.Sprintf("since %s", opts.Since.Format("2006-01-02")))
	}
	if opts.Until != nil {
		parts = append(parts, fmt.Sprintf("until %s", opts.Until.Format("2006-01-02")))
	}
	if opts.Limit > 0 {
		parts = append(parts, fmt.Sprintf("limit %d", opts.Limit))
	}

	if len(parts) == 0 {
		return "all history"
	}
	return strings.Join(parts, ", ")
}

// appendUnique appends a string to a slice if not already present.
func appendUnique(slice []string, s string) []string {
	for _, existing := range slice {
		if existing == s {
			return slice
		}
	}
	return append(slice, s)
}
