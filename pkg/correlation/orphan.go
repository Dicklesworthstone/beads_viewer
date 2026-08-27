// Package correlation provides orphan commit detection with smart heuristics.
package correlation

import (
	"context"
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

// Pre-compiled regex patterns for message analysis (compiled once at init).
var (
	// Message patterns for detecting bead-related commits
	orphanMessagePatterns = []struct {
		re     *regexp.Regexp
		weight int
	}{
		{regexp.MustCompile(`\b(fix|fixes|fixed)\b`), 10},
		{regexp.MustCompile(`\b(close|closes|closed)\b`), 10},
		{regexp.MustCompile(`\b(resolve|resolves|resolved)\b`), 10},
		{regexp.MustCompile(`\b(implement|implements|implemented)\b`), 8},
		{regexp.MustCompile(`\b(add|adds|added)\b`), 5},
		{regexp.MustCompile(`#\d+`), 15},                               // Issue number reference
		{regexp.MustCompile(`\b[a-z]{2,5}-\d+\b`), 20},                 // JIRA-style ID (lowercase since message is lowercased)
		{regexp.MustCompile(`\bbv-[a-z0-9]+(?:[.-][a-z0-9]+)*\b`), 25}, // bv-xxx pattern
		{regexp.MustCompile(`\bbeads?[-_]?\d+\b`), 25},                 // bead-123 pattern
	}

	// Pattern for extracting specific bead IDs from messages
	orphanBeadIDPattern = regexp.MustCompile(`(?i)\bbv-([a-z0-9]+(?:[.-][a-z0-9]+)*)\b`)
)

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
	Stats       OrphanReportStats   `json:"stats"`
	Candidates  []OrphanCandidate   `json:"candidates"`
	ByBead      map[string][]string `json:"by_bead,omitempty"` // BeadID -> []commit SHAs
}

// OrphanReportStats provides aggregate statistics.
type OrphanReportStats struct {
	TotalCommits    int     `json:"total_commits"`
	CorrelatedCount int     `json:"correlated_count"`
	OrphanCount     int     `json:"orphan_count"`
	CandidateCount  int     `json:"candidate_count"` // Orphans with probable beads
	OrphanRatio     float64 `json:"orphan_ratio"`
	AvgSuspicion    float64 `json:"avg_suspicion_score"`
}

// OrphanDetector finds commits that probably should be linked to beads.
type OrphanDetector struct {
	repoPath    string
	dataHash    string
	lookup      *ReverseLookup
	fileLookup  *FileLookup
	beadWindows map[string][]TemporalWindow // BeadID -> all known active time windows
	authorBeads map[string][]string         // normalized author email -> BeadIDs they worked on
	now         time.Time
	ctx         context.Context
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

// WithContext binds ctx to Git subprocesses used during orphan detection. A
// nil context is accepted and has the same meaning as context.Background().
func (od *OrphanDetector) WithContext(ctx context.Context) *OrphanDetector {
	if od == nil {
		return nil
	}
	od.ctx = ctx
	if od.lookup != nil {
		od.lookup.WithContext(ctx)
	}
	return od
}

// newOrphanDetector is the internal constructor.
func newOrphanDetector(report *HistoryReport, repoPath string, now time.Time) *OrphanDetector {
	od := &OrphanDetector{
		repoPath:    repoPath,
		lookup:      NewReverseLookupWithRepo(report, repoPath),
		fileLookup:  NewFileLookup(report),
		beadWindows: make(map[string][]TemporalWindow),
		authorBeads: make(map[string][]string),
		now:         now,
	}
	if report == nil {
		return od
	}
	od.dataHash = report.DataHash

	// Build temporal windows for each bead
	for beadID, history := range report.Histories {
		// Tombstones remain in the reverse lookup so their already-linked commits
		// are not reclassified as orphans, but deleted work must never become a
		// probable destination through timing or author heuristics.
		if normalizeStatus(history.Status) == "tombstone" {
			continue
		}
		if windows := orphanActivityWindows(beadID, history, now); len(windows) > 0 {
			od.beadWindows[beadID] = windows
		}

		// Build author -> beads mapping
		for _, commit := range history.Commits {
			authorEmail := normalizeAuthorEmail(commit.AuthorEmail)
			if authorEmail != "" {
				od.authorBeads[authorEmail] = appendUnique(
					od.authorBeads[authorEmail], beadID)
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

func orphanActivityWindows(beadID string, history BeadHistory, now time.Time) []TemporalWindow {
	if isClosedHistoryStatus(history.Status) {
		terminalWindow := extractTemporalWindowFromHistory(beadID, history)
		if terminalWindow == nil {
			// A closed status without a terminal close interval has no defensible
			// end time. Treating it as active through now creates false timing and
			// author signals.
			return nil
		}

		// A closed bead may have been claimed, closed, and reopened more than
		// once. Derive every completed interval through the terminal close so an
		// all-history orphan scan does not silently forget earlier work periods.
		return temporalActivityIntervalsAt(beadID, history, terminalWindow.End)
	}

	return temporalActivityIntervalsAt(beadID, history, now)
}

func normalizeAuthorEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// DetectOrphans finds orphan commits with smart detection.
func (od *OrphanDetector) DetectOrphans(opts ExtractOptions) (*OrphanReport, error) {
	if od == nil {
		return nil, fmt.Errorf("orphan detector is nil")
	}
	if od.lookup == nil {
		return nil, fmt.Errorf("orphan detector has no reverse lookup")
	}
	od.lookup.WithContext(od.ctx)

	// Get basic orphans first
	orphans, stats, err := od.lookup.FindOrphanCommits(opts)
	if err != nil {
		return nil, fmt.Errorf("finding orphan commits: %w", err)
	}

	report := &OrphanReport{
		GeneratedAt: od.now,
		GitRange:    formatGitRange(opts),
		DataHash:    od.dataHash,
		Candidates:  make([]OrphanCandidate, 0, len(orphans)),
		ByBead:      make(map[string][]string),
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
		TotalCommits:    stats.TotalCommits,
		CorrelatedCount: stats.CorrelatedCmts,
		OrphanCount:     stats.OrphanCommits,
		CandidateCount:  candidateCount,
		OrphanRatio:     stats.OrphanRatio,
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

	// Get files changed in this commit
	if od.repoPath != "" {
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
	for beadID, windows := range od.beadWindows {
		for _, window := range windows {
			if candidate.Timestamp.Before(window.Start) || candidate.Timestamp.After(window.End) {
				continue
			}

			// Commit during bead's active window
			weight := 30 // Base weight for timing match

			candidate.Signals = append(candidate.Signals, OrphanSignalHit{
				Signal:  SignalOrphanTiming,
				Details: fmt.Sprintf("Commit during %s active period", beadID),
				Weight:  weight,
			})

			if _, ok := beadScores[beadID]; !ok {
				title := window.Title
				status := "unknown"
				if history, exists := od.lookup.beads[beadID]; exists {
					title = history.Title
					status = history.Status
				}
				beadScores[beadID] = &probableBeadBuilder{
					title:  title,
					status: status,
				}
			}
			beadScores[beadID].score += weight
			beadScores[beadID].reasons = append(beadScores[beadID].reasons,
				"commit during active timeframe")
			break
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

// checkMessage checks if commit message contains bead-like patterns.
func (od *OrphanDetector) checkMessage(candidate *OrphanCandidate, beadScores map[string]*probableBeadBuilder) {
	msg := strings.ToLower(candidate.Message)

	// Look for bead-like patterns (using pre-compiled regexes)
	totalWeight := 0
	var matchDetails []string

	for _, p := range orphanMessagePatterns {
		if p.re.MatchString(msg) {
			totalWeight += p.weight
			match := p.re.FindString(msg)
			matchDetails = append(matchDetails, match)
		}
	}

	// Custom ID patterns (--id-pattern, #188) count as strong ID signals,
	// matched against the original-case message since the pattern is
	// user-supplied.
	for _, re := range CustomIDPatterns() {
		if match := re.FindString(candidate.Message); match != "" {
			totalWeight += 25
			matchDetails = append(matchDetails, match)
		}
	}

	if totalWeight > 0 {
		candidate.Signals = append(candidate.Signals, OrphanSignalHit{
			Signal:  SignalOrphanMessage,
			Details: fmt.Sprintf("Message patterns: %s", strings.Join(matchDetails, ", ")),
			Weight:  minInt(totalWeight, 35),
		})
	}

	// Try to match specific bead IDs mentioned in message (case-insensitive)
	matches := orphanBeadIDPattern.FindAllStringSubmatch(msg, -1)
	for _, match := range matches {
		if len(match) >= 2 {
			od.scoreMentionedBead(beadScores, "bv-"+strings.ToLower(match[1]))
		}
	}

	// Custom ID patterns (--id-pattern, #188): extract candidate IDs from the
	// original-case message and credit any that name a known bead. The ID is
	// capture group 1 when the pattern defines one, else the whole match.
	for _, re := range CustomIDPatterns() {
		for _, match := range re.FindAllStringSubmatch(candidate.Message, -1) {
			id := ""
			if len(match) >= 2 && match[1] != "" {
				id = match[1]
			} else if len(match) >= 1 {
				id = match[0]
			}
			if id != "" {
				od.scoreMentionedBead(beadScores, id)
			}
		}
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
	if normalizeStatus(history.Status) == "tombstone" {
		return
	}
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

	beadIDs := od.authorBeads[normalizeAuthorEmail(candidate.AuthorEmail)]
	if len(beadIDs) == 0 {
		return
	}

	// Check if any of the author's beads were active around the commit time
	for _, beadID := range beadIDs {
		windows := od.beadWindows[beadID]
		if len(windows) == 0 {
			continue
		}

		for _, window := range windows {
			// Check if commit is within a week of the bead's active window.
			windowStart := window.Start.Add(-7 * 24 * time.Hour)
			windowEnd := window.End.Add(7 * 24 * time.Hour)

			if candidate.Timestamp.Before(windowStart) || candidate.Timestamp.After(windowEnd) {
				continue
			}

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
			break
		}
	}
}

// getCommitFiles returns files changed in a commit.
func (od *OrphanDetector) getCommitFiles(sha string) ([]string, error) {
	cocommit := &CoCommitExtractor{repoPath: od.repoPath, ctx: od.ctx}
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
	if opts.Since == nil && opts.Until == nil && opts.Limit == 0 {
		return "all history"
	}

	parts := []string{}
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
