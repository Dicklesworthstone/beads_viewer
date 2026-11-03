// Package correlation provides incremental history updates to avoid full repo scans.
package correlation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IncrementalThreshold defines the maximum number of new commits before falling back to full refresh.
// When there are more than this many new commits, it's more efficient to do a full rebuild.
const IncrementalThreshold = 100

// IncrementalUpdateResult contains the outcome of an incremental update attempt
type IncrementalUpdateResult struct {
	Report            *HistoryReport // The updated report
	WasIncremental    bool           // True if update was incremental, false if full refresh
	NewCommitCount    int            // Number of new commits processed
	MergedEventCount  int            // Number of events merged
	MergedCommitCount int            // Number of commits merged
	RefreshReason     string         // Why full refresh was used (if applicable)
}

// IncrementalCorrelator extends CachedCorrelator with incremental update support
type IncrementalCorrelator struct {
	correlator *Correlator
	cache      *HistoryCache
	hits       int64
	misses     int64
	increments int64 // Count of successful incremental updates
	refreshes  int64 // Count of full refreshes
	mu         sync.Mutex

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// incremental updates (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the correlator (and its underlying full
// correlator) so their git subprocesses are cancelled when ctx is done
// (issue #166). Returns the receiver for chaining.
func (ic *IncrementalCorrelator) WithContext(ctx context.Context) *IncrementalCorrelator {
	ic.ctx = ctx
	if ic.correlator != nil {
		ic.correlator.WithContext(ctx)
	}
	return ic
}

// NewIncrementalCorrelator creates a correlator with incremental update support.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewIncrementalCorrelator(repoPath string, beadsFilePath ...string) *IncrementalCorrelator {
	return &IncrementalCorrelator{
		correlator: NewCorrelator(repoPath, beadsFilePath...),
		cache:      NewHistoryCache(repoPath),
	}
}

// NewIncrementalCorrelatorWithOptions creates a correlator with custom cache settings.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewIncrementalCorrelatorWithOptions(repoPath string, maxAge time.Duration, maxSize int, beadsFilePath ...string) *IncrementalCorrelator {
	return &IncrementalCorrelator{
		correlator: NewCorrelator(repoPath, beadsFilePath...),
		cache:      NewHistoryCacheWithOptions(repoPath, maxAge, maxSize),
	}
}

// GenerateReport generates a history report, using incremental updates when possible
func (ic *IncrementalCorrelator) GenerateReport(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	result, err := ic.GenerateReportWithDetails(beads, opts)
	if err != nil {
		return nil, err
	}
	return result.Report, nil
}

// GenerateReportWithDetails generates a report and returns detailed update information
func (ic *IncrementalCorrelator) GenerateReportWithDetails(beads []BeadInfo, opts CorrelatorOptions) (*IncrementalUpdateResult, error) {
	// Build cache key
	key, err := BuildCacheKey(ic.cache.repoPath, beads, opts)
	if err != nil {
		// If we can't build a cache key, do a full refresh
		report, err := ic.correlator.GenerateReport(beads, opts)
		if err != nil {
			return nil, err
		}
		return &IncrementalUpdateResult{
			Report:         report,
			WasIncremental: false,
			RefreshReason:  "failed to build cache key",
		}, nil
	}
	key.Options = ic.correlator.cacheOptionsHash(opts)

	// Check cache
	if cached, ok := ic.cache.Get(key); ok {
		ic.recordCacheHit()
		return &IncrementalUpdateResult{
			Report:         cached,
			WasIncremental: true,
			NewCommitCount: 0,
		}, nil
	}

	// Cache miss - try incremental update if we have a cached report with same beads
	existingReport := ic.findExistingReport(beads, opts)
	// The incremental merger appends through live HEAD and carries lifecycle
	// events only. Revision-bound or full causal evidence needs the ordinary
	// complete extraction below; same-key cache hits above remain usable.
	if existingReport != nil && existingReport.LatestCommitSHA != "" && opts.Revision == "" && opts.CausalityBeadID == "" {
		result, err := ic.tryIncrementalUpdate(existingReport, beads, opts)
		if err == nil && result != nil {
			ic.recordIncrementalUpdate()
			ic.cache.Put(key, result.Report)
			return result, nil
		}
		// If incremental failed, fall through to full refresh
	}

	// Full refresh
	ic.recordFullRefresh()

	report, err := ic.correlator.GenerateReport(beads, opts)
	if err != nil {
		return nil, err
	}

	ic.cache.Put(key, report)

	return &IncrementalUpdateResult{
		Report:         report,
		WasIncremental: false,
		RefreshReason:  "no suitable cached report for incremental update",
	}, nil
}

func (ic *IncrementalCorrelator) recordCacheHit() {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	ic.hits++
}

func (ic *IncrementalCorrelator) recordIncrementalUpdate() {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	ic.increments++
}

func (ic *IncrementalCorrelator) recordFullRefresh() {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	ic.misses++
	ic.refreshes++
}

func (ic *IncrementalCorrelator) statsSnapshot() (hits, misses, increments, refreshes int64) {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	return ic.hits, ic.misses, ic.increments, ic.refreshes
}

// findExistingReport looks for a cached report that can be incrementally updated
func (ic *IncrementalCorrelator) findExistingReport(beads []BeadInfo, opts CorrelatorOptions) *HistoryReport {
	// Look for any cached report with the same beads hash (different HEAD is OK)
	beadsHash := hashBeads(beads)
	optsHash := ic.correlator.cacheOptionsHash(opts)

	ic.cache.mu.RLock()
	defer ic.cache.mu.RUnlock()

	for _, entry := range ic.cache.entries {
		// Match on beads and options, but allow different HEAD
		if entry.Key.BeadsHash == beadsHash && entry.Key.Options == optsHash {
			return entry.Report
		}
	}
	return nil
}

// tryIncrementalUpdate attempts to update an existing report incrementally
func (ic *IncrementalCorrelator) tryIncrementalUpdate(existing *HistoryReport, beads []BeadInfo, opts CorrelatorOptions) (*IncrementalUpdateResult, error) {
	// Find new commits since the existing report
	newCommitSHAs, err := getCommitsSince(ic.ctx, ic.cache.repoPath, existing.LatestCommitSHA)
	if err != nil {
		return nil, fmt.Errorf("finding new commits: %w", err)
	}

	// If too many new commits, fall back to full refresh
	if len(newCommitSHAs) > IncrementalThreshold {
		return nil, fmt.Errorf("too many new commits (%d > %d)", len(newCommitSHAs), IncrementalThreshold)
	}

	// If no new commits, the existing report is still valid
	if len(newCommitSHAs) == 0 {
		return &IncrementalUpdateResult{
			Report:         existing,
			WasIncremental: true,
			NewCommitCount: 0,
		}, nil
	}

	extractor := ic.incrementalExtractor()
	newEvents, err := extractEventsFromCommits(extractor, newCommitSHAs, opts.BeadID)
	if err != nil {
		return nil, fmt.Errorf("extracting new events: %w", err)
	}

	// Extract co-commits from new events
	coCommitter := NewCoCommitExtractor(ic.cache.repoPath)
	coCommitter.ctx = ic.ctx
	newCorrelatedCommits, err := coCommitter.ExtractAllCoCommits(newEvents)
	if err != nil {
		return nil, fmt.Errorf("extracting co-commits: %w", err)
	}

	// Merge new data with existing report
	merged := mergeReportsThrough(existing, beads, newEvents, newCorrelatedCommits, newCommitSHAs[len(newCommitSHAs)-1])

	return &IncrementalUpdateResult{
		Report:            merged,
		WasIncremental:    true,
		NewCommitCount:    len(newCommitSHAs),
		MergedEventCount:  len(newEvents),
		MergedCommitCount: len(newCorrelatedCommits),
	}, nil
}

func (ic *IncrementalCorrelator) incrementalExtractor() *Extractor {
	if ic.correlator != nil && ic.correlator.extractor != nil {
		return ic.correlator.extractor
	}

	repoPath := ""
	if ic.cache != nil {
		repoPath = ic.cache.repoPath
	}
	return NewExtractor(repoPath)
}

// getCommitsSince returns commit SHAs since the given commit (exclusive).
// ctx bounds the git subprocess (#166); nil means context.Background().
func getCommitsSince(ctx context.Context, repoPath, sinceSHA string) ([]string, error) {
	if sinceSHA == "" {
		return nil, fmt.Errorf("no since SHA provided")
	}

	// Use git rev-list to get commits since the given SHA
	cmd := gitCommand(ctx, "rev-list", "--reverse", fmt.Sprintf("%s..HEAD", sinceSHA))
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		// Check if the SHA exists
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git rev-list failed: %s", string(exitErr.Stderr))
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil // No new commits
	}

	return lines, nil
}

// countCommitsSince returns the number of commits since the given SHA.
// ctx bounds the git subprocess (#166); nil means context.Background().
func countCommitsSince(ctx context.Context, repoPath, sinceSHA string) (int, error) {
	if sinceSHA == "" {
		return 0, fmt.Errorf("no since SHA provided")
	}

	cmd := gitCommand(ctx, "rev-list", "--count", fmt.Sprintf("%s..HEAD", sinceSHA))
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	count, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parsing commit count: %w", err)
	}

	return count, nil
}

// extractEventsFromCommits extracts bead events from specific commits
func extractEventsFromCommits(extractor *Extractor, commitSHAs []string, filterBeadID string) ([]BeadEvent, error) {
	if len(commitSHAs) == 0 {
		return nil, nil
	}

	// Use git log with --no-walk to process specific commits exactly as listed.
	// This avoids range semantics (A..B) which can be tricky with root commits
	// or non-linear history segments.
	args := []string{
		"log",
		"-p",
		"--format=" + gitLogHeaderFormat,
		"--no-walk",
	}
	args = append(args, commitSHAs...)
	args = append(args, "--", extractor.primaryBeadsFile())

	cmd := gitCommand(extractor.ctx, withNoColorGit(args)...)
	cmd.Dir = extractor.repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log for commits failed: %w", err)
	}

	events, err := extractor.parseGitLogOutput(bytes.NewReader(out), filterBeadID)
	if err != nil {
		return nil, err
	}

	// Reverse to chronological order (git log output depends on input order but usually LIFO)
	// We want chronological for the history report
	reverseEvents(events)
	return events, nil
}

// mergeReports creates a new report by merging existing data with new events/commits
func mergeReports(existing *HistoryReport, beads []BeadInfo, newEvents []BeadEvent, newCommits []CorrelatedCommit) *HistoryReport {
	return mergeReportsThrough(existing, beads, newEvents, newCommits, "")
}

func cloneBeadEvent(event *BeadEvent) *BeadEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	return &cloned
}

func cloneBeadMilestones(milestones BeadMilestones) BeadMilestones {
	return BeadMilestones{
		Created:  cloneBeadEvent(milestones.Created),
		Claimed:  cloneBeadEvent(milestones.Claimed),
		Closed:   cloneBeadEvent(milestones.Closed),
		Reopened: cloneBeadEvent(milestones.Reopened),
	}
}

func cloneDuration(duration *time.Duration) *time.Duration {
	if duration == nil {
		return nil
	}
	cloned := *duration
	return &cloned
}

func cloneCycleTime(cycleTime *CycleTime) *CycleTime {
	if cycleTime == nil {
		return nil
	}
	return &CycleTime{
		ClaimToClose:  cloneDuration(cycleTime.ClaimToClose),
		CreateToClose: cloneDuration(cycleTime.CreateToClose),
		CreateToClaim: cloneDuration(cycleTime.CreateToClaim),
	}
}

func cloneCorrelatedCommits(commits []CorrelatedCommit) []CorrelatedCommit {
	cloned := make([]CorrelatedCommit, len(commits))
	for i := range commits {
		cloned[i] = commits[i]
		if commits[i].Files != nil {
			cloned[i].Files = append([]FileChange(nil), commits[i].Files...)
		}
	}
	return cloned
}

func mergeReportsThrough(existing *HistoryReport, beads []BeadInfo, newEvents []BeadEvent, newCommits []CorrelatedCommit, latestProcessedSHA string) *HistoryReport {
	// Create a deep copy of existing histories
	histories := make(map[string]BeadHistory, len(existing.Histories))
	for id, h := range existing.Histories {
		// Deep copy the history
		eventsCopy := make([]BeadEvent, len(h.Events))
		copy(eventsCopy, h.Events)

		histories[id] = BeadHistory{
			BeadID:     h.BeadID,
			Title:      h.Title,
			Status:     h.Status,
			Events:     eventsCopy,
			Milestones: cloneBeadMilestones(h.Milestones),
			Commits:    cloneCorrelatedCommits(h.Commits),
			CycleTime:  cloneCycleTime(h.CycleTime),
			LastAuthor: h.LastAuthor,
		}
	}

	// Add any new beads that weren't in the existing report
	for _, bead := range beads {
		if _, exists := histories[bead.ID]; !exists {
			histories[bead.ID] = BeadHistory{
				BeadID:  bead.ID,
				Title:   bead.Title,
				Status:  bead.Status,
				Events:  []BeadEvent{},
				Commits: []CorrelatedCommit{},
			}
		}
	}

	// Update bead statuses from current beads list
	for _, bead := range beads {
		if h, exists := histories[bead.ID]; exists {
			h.Title = bead.Title
			h.Status = bead.Status
			histories[bead.ID] = h
		}
	}

	// Merge new events
	eventsByBead := make(map[string][]BeadEvent)
	for _, event := range newEvents {
		eventsByBead[event.BeadID] = append(eventsByBead[event.BeadID], event)
	}

	for beadID, events := range eventsByBead {
		if h, exists := histories[beadID]; exists {
			h.Events = append(h.Events, events...)
			// Recalculate milestones
			h.Milestones = GetBeadMilestones(h.Events)
			h.CycleTime = CalculateCycleTime(h.Milestones)
			histories[beadID] = h
		}
	}

	// Merge new commits
	commitsByBead := make(map[string][]CorrelatedCommit)
	for _, commit := range newCommits {
		// Match full report assembly: the correlation itself owns bead linkage.
		// Same-SHA lifecycle events can belong to multiple beads and must not
		// cross-associate their distinct correlation records.
		if commit.BeadID != "" {
			commitsByBead[commit.BeadID] = append(commitsByBead[commit.BeadID], commit)
		}
	}

	for beadID, commits := range commitsByBead {
		if h, exists := histories[beadID]; exists {
			h.Commits = dedupCommits(append(h.Commits, commits...))
			// Update last author
			if len(h.Commits) > 0 {
				h.LastAuthor = h.Commits[len(h.Commits)-1].Author
			}
			histories[beadID] = h
		}
	}

	// Build a stable reverse index; HistoryReport exposes these map-derived
	// values as arrays in robot JSON.
	commitIndex := BuildCommitIndex(histories)

	// Calculate new stats
	stats := calculateMergedStats(histories, newCommits)

	// Find latest commit SHA when a direct caller did not supply the rev-list
	// cursor. Incremental callers always use the newest processed SHA so code-only
	// commits and non-monotonic author timestamps still advance the cache cursor.
	var latestTime time.Time
	latestSHA := latestProcessedSHA
	for _, event := range newEvents {
		if latestProcessedSHA == "" && event.Timestamp.After(latestTime) {
			latestTime = event.Timestamp
			latestSHA = event.CommitSHA
		}
	}
	for _, commit := range newCommits {
		if latestProcessedSHA == "" && commit.Timestamp.After(latestTime) {
			latestTime = commit.Timestamp
			latestSHA = commit.SHA
		}
	}
	// Fall back to existing if no new commits
	if latestSHA == "" {
		latestSHA = existing.LatestCommitSHA
	}

	return &HistoryReport{
		GeneratedAt:     time.Now().UTC(),
		DataHash:        existing.DataHash,
		GitRange:        existing.GitRange + " (incremental)",
		LatestCommitSHA: latestSHA,
		Stats:           stats,
		Histories:       histories,
		CommitIndex:     commitIndex,
	}
}

// calculateMergedStats computes statistics for the merged report
func calculateMergedStats(histories map[string]BeadHistory, newCommits []CorrelatedCommit) HistoryStats {
	stats := HistoryStats{
		TotalBeads:         len(histories),
		MethodDistribution: make(map[string]int),
	}

	authors := make(map[string]bool)
	uniqueCommits := make(map[string]bool)
	var cycleTimes []time.Duration

	for _, h := range histories {
		if len(h.Commits) > 0 {
			stats.BeadsWithCommits++
		}

		for _, commit := range h.Commits {
			uniqueCommits[commit.SHA] = true
			authors[commit.Author] = true
			for _, method := range commit.AllMethods() {
				stats.MethodDistribution[method]++
			}
			if commit.Confirmed {
				stats.MethodDistribution[MethodDistributionConfirmedByFeedback]++
			}
		}

		for _, event := range h.Events {
			authors[event.Author] = true
		}

		if h.CycleTime != nil && h.CycleTime.ClaimToClose != nil {
			cycleTimes = append(cycleTimes, *h.CycleTime.ClaimToClose)
		}
	}

	stats.TotalCommits = len(uniqueCommits)
	stats.UniqueAuthors = len(authors)

	if stats.BeadsWithCommits > 0 {
		stats.AvgCommitsPerBead = float64(stats.TotalCommits) / float64(stats.BeadsWithCommits)
	}

	if len(cycleTimes) > 0 {
		var total time.Duration
		for _, ct := range cycleTimes {
			total += ct
		}
		avgDays := total.Hours() / 24 / float64(len(cycleTimes))
		stats.AvgCycleTimeDays = &avgDays
	}

	return stats
}

// InvalidateCache clears all cached entries
func (ic *IncrementalCorrelator) InvalidateCache() {
	ic.cache.Invalidate()
}

// CacheStats returns cache and incremental update statistics
func (ic *IncrementalCorrelator) CacheStats() IncrementalCorrelatorStats {
	hits, misses, increments, refreshes := ic.statsSnapshot()
	cacheStats := ic.cache.Stats()

	var hitRate float64
	total := hits + misses
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	var incrementRate float64
	updates := increments + refreshes
	if updates > 0 {
		incrementRate = float64(increments) / float64(updates)
	}

	return IncrementalCorrelatorStats{
		Hits:               hits,
		Misses:             misses,
		HitRate:            hitRate,
		IncrementalUpdates: increments,
		FullRefreshes:      refreshes,
		IncrementRate:      incrementRate,
		CacheSize:          cacheStats.Size,
		MaxSize:            cacheStats.MaxSize,
		MaxAge:             cacheStats.MaxAge,
	}
}

// IncrementalCorrelatorStats provides statistics about incremental update performance
type IncrementalCorrelatorStats struct {
	Hits               int64
	Misses             int64
	HitRate            float64
	IncrementalUpdates int64
	FullRefreshes      int64
	IncrementRate      float64 // Ratio of incremental updates to total non-cached updates
	CacheSize          int
	MaxSize            int
	MaxAge             time.Duration
}

// CanUpdateIncrementally checks if incremental update is possible for the given cached report
func CanUpdateIncrementally(repoPath string, cachedReport *HistoryReport) (bool, int, error) {
	if cachedReport == nil || cachedReport.LatestCommitSHA == "" {
		return false, 0, nil
	}

	count, err := countCommitsSince(context.Background(), repoPath, cachedReport.LatestCommitSHA)
	if err != nil {
		return false, 0, err
	}

	return count <= IncrementalThreshold, count, nil
}
