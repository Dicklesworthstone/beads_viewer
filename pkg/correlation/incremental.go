// Package correlation provides incremental history updates to avoid full repo scans.
package correlation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
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
	if ic == nil {
		return nil
	}
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

// GenerateReport generates a history report, using incremental updates when
// possible. The returned report may be cache-backed and must be treated as
// immutable.
func (ic *IncrementalCorrelator) GenerateReport(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	result, err := ic.GenerateReportWithDetails(beads, opts)
	if err != nil {
		return nil, err
	}
	return result.Report, nil
}

// GenerateReportWithDetails generates a report and returns detailed update
// information. Result.Report may be cache-backed and must be treated as
// immutable.
func (ic *IncrementalCorrelator) GenerateReportWithDetails(beads []BeadInfo, opts CorrelatorOptions) (*IncrementalUpdateResult, error) {
	// Build cache key
	key, err := buildCacheKeyContext(ic.ctx, ic.cache.repoPath, beads, opts)
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
	if !key.historyCacheSafe() {
		// Incremental ancestry and exact cache hits are both unsound across a
		// shallow-boundary move: fetch --deepen can reveal parents while HEAD is
		// unchanged. Compute from the currently visible history without retaining
		// it, and retry cache eligibility on the next invocation.
		ic.recordFullRefresh()
		report, err := ic.correlator.GenerateReport(beads, opts)
		if err != nil {
			return nil, err
		}
		return &IncrementalUpdateResult{
			Report:         report,
			WasIncremental: false,
			RefreshReason:  "repository history is shallow or unavailable",
		}, nil
	}

	// Check cache
	if cached, ok := ic.cache.Get(key); ok {
		ic.recordCacheHit()
		return &IncrementalUpdateResult{
			Report:         cached,
			WasIncremental: true,
			NewCommitCount: 0,
		}, nil
	}

	// Cache miss - try an incremental update only for the unbounded all-history
	// shape. Since/Until/Limit windows can evict previously included commits as
	// HEAD advances, so append-only merging cannot preserve their semantics.
	if incrementalOptionsSupported(opts) {
		base := ic.findExistingReport(beads, opts, key.HeadSHA)
		if base != nil {
			result, err := ic.tryIncrementalUpdate(base.report, base.headSHA, key.HeadSHA, beads, opts)
			if err == nil && result != nil {
				// Do not return a report for a cursor that stopped being current
				// while the incremental extraction was running. Falling through to
				// a full refresh gives the caller the same drift boundary as every
				// other cache-miss path.
				if ic.cacheReportIfCurrent(key, beads, opts, result.Report) {
					ic.recordIncrementalUpdate()
					return result, nil
				}
			}
			// If incremental failed, fall through to full refresh.
		}
	}

	// Full refresh
	ic.recordFullRefresh()

	report, err := ic.correlator.GenerateReport(beads, opts)
	if err != nil {
		return nil, err
	}

	refreshReason := "no suitable cached report for incremental update"
	// A stable cache key proves this full report was generated while the
	// repository stayed at key.HeadSHA. The cache helper records that proven
	// cursor instead of the newest event timestamp, which misses code-only commits.
	if !ic.cacheReportIfCurrent(key, beads, opts, report) {
		refreshReason = "repository changed during full refresh"
	}

	return &IncrementalUpdateResult{
		Report:         report,
		WasIncremental: false,
		RefreshReason:  refreshReason,
	}, nil
}

func incrementalOptionsSupported(opts CorrelatorOptions) bool {
	return opts.BeadID == "" && opts.Since == nil && opts.Until == nil && opts.Limit == 0
}

func (ic *IncrementalCorrelator) cacheReportIfCurrent(expected CacheKey, beads []BeadInfo, opts CorrelatorOptions, report *HistoryReport) bool {
	if report == nil {
		return false
	}
	current, err := buildCacheKeyContext(ic.ctx, ic.cache.repoPath, beads, opts)
	if err != nil || current != expected {
		return false
	}
	report.LatestCommitSHA = expected.HeadSHA
	ic.cache.Put(expected, report)
	return true
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

type incrementalBase struct {
	report      *HistoryReport
	headSHA     string
	commitCount int
}

// findExistingReport finds the nearest fresh cached ancestor of currentHead.
// A map iteration winner is not safe: it can select an arbitrary stale or
// diverged branch and merge unrelated commits into the report.
func (ic *IncrementalCorrelator) findExistingReport(beads []BeadInfo, opts CorrelatorOptions, currentHead string) *incrementalBase {
	optsHash := hashOptions(opts)
	now := time.Now()

	type candidate struct {
		report  *HistoryReport
		headSHA string
	}
	var candidates []candidate

	ic.cache.mu.RLock()
	for _, entry := range ic.cache.entries {
		if entry == nil || entry.Report == nil || entry.Key.HeadSHA == "" || entry.Key.HeadSHA == currentHead {
			continue
		}
		// Title/status edits can be applied during merge, but adding an ID whose
		// lifecycle predates the cached report would require older events that the
		// report never retained. Require the same ID set while allowing metadata
		// (and therefore BeadsHash) to change.
		if entry.Key.HistoryState == coCommitHistoryStateFull && entry.Key.Options == optsHash && reportHasBeadIDs(entry.Report, beads) && cacheCreatedAtIsFresh(entry.CreatedAt, now, ic.cache.maxAge) {
			candidates = append(candidates, candidate{report: entry.Report, headSHA: entry.Key.HeadSHA})
		}
	}
	ic.cache.mu.RUnlock()

	// Make ties deterministic even though HistoryCache stores entries in a map.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].headSHA < candidates[j].headSHA })

	var best *incrementalBase
	for _, candidate := range candidates {
		count, err := countCommitsBetween(ic.ctx, ic.cache.repoPath, candidate.headSHA, currentHead)
		if err != nil || count > IncrementalThreshold {
			continue
		}
		if best == nil || count < best.commitCount {
			best = &incrementalBase{report: candidate.report, headSHA: candidate.headSHA, commitCount: count}
		}
	}
	return best
}

func reportHasBeadIDs(report *HistoryReport, beads []BeadInfo) bool {
	if report == nil {
		return false
	}
	ids := make(map[string]struct{}, len(beads))
	for _, bead := range beads {
		ids[bead.ID] = struct{}{}
	}
	if len(report.Histories) != len(ids) {
		return false
	}
	for id := range ids {
		if _, ok := report.Histories[id]; !ok {
			return false
		}
	}
	return true
}

// tryIncrementalUpdate attempts to update an existing report incrementally
func (ic *IncrementalCorrelator) tryIncrementalUpdate(existing *HistoryReport, baseSHA, throughSHA string, beads []BeadInfo, opts CorrelatorOptions) (*IncrementalUpdateResult, error) {
	// Find new commits since the existing report
	newCommitSHAs, err := getCommitsBetween(ic.ctx, ic.cache.repoPath, baseSHA, throughSHA)
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
	merged := mergeReportsThrough(existing, beads, newEvents, newCorrelatedCommits, throughSHA)

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
	throughSHA, err := getGitHeadContext(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("resolving current HEAD: %w", err)
	}
	return getCommitsBetween(ctx, repoPath, sinceSHA, throughSHA)
}

func getCommitsBetween(ctx context.Context, repoPath, sinceSHA, throughSHA string) ([]string, error) {
	if sinceSHA == "" || throughSHA == "" {
		return nil, fmt.Errorf("both since and through SHAs are required")
	}
	if !isCanonicalCommitSHA(sinceSHA) || !isCanonicalCommitSHA(throughSHA) {
		return nil, fmt.Errorf("since and through must be canonical commit object IDs")
	}
	if len(sinceSHA) != len(throughSHA) {
		return nil, fmt.Errorf("mixed-width incremental endpoints: got %d and %d characters", len(sinceSHA), len(throughSHA))
	}
	ancestor, err := isGitAncestor(ctx, repoPath, sinceSHA, throughSHA)
	if err != nil {
		return nil, err
	}
	if !ancestor {
		return nil, fmt.Errorf("cached commit %s is not an ancestor of %s", sinceSHA, throughSHA)
	}

	// Pin both endpoints. Reading a moving HEAD here could discover one range and
	// then extract another after a concurrent commit.
	args := []string{"rev-list", "--reverse"}
	args = append(args, lifecycleHistoryOrderArgs()...)
	args = append(args, fmt.Sprintf("%s..%s", sinceSHA, throughSHA))
	cmd := repoGitCommand(ctx, repoPath, args...)

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
	for _, sha := range lines {
		if !isCanonicalCommitSHA(sha) || len(sha) != len(throughSHA) {
			return nil, fmt.Errorf("git rev-list returned invalid or mixed-width commit object ID %q", sha)
		}
	}

	return lines, nil
}

func isGitAncestor(ctx context.Context, repoPath, ancestorSHA, descendantSHA string) (bool, error) {
	cmd := repoGitCommand(ctx, repoPath, "merge-base", "--is-ancestor", ancestorSHA, descendantSHA)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("checking git ancestry: %w: %s", err, strings.TrimSpace(string(out)))
}

// countCommitsSince returns the number of commits since the given SHA.
// ctx bounds the git subprocess (#166); nil means context.Background().
func countCommitsSince(ctx context.Context, repoPath, sinceSHA string) (int, error) {
	if sinceSHA == "" {
		return 0, fmt.Errorf("no since SHA provided")
	}
	throughSHA, err := getGitHeadContext(ctx, repoPath)
	if err != nil {
		return 0, fmt.Errorf("resolving current HEAD: %w", err)
	}
	return countCommitsBetween(ctx, repoPath, sinceSHA, throughSHA)
}

func countCommitsBetween(ctx context.Context, repoPath, sinceSHA, throughSHA string) (int, error) {
	if !isCanonicalCommitSHA(sinceSHA) || !isCanonicalCommitSHA(throughSHA) {
		return 0, fmt.Errorf("since and through must be canonical commit object IDs")
	}
	if len(sinceSHA) != len(throughSHA) {
		return 0, fmt.Errorf("mixed-width incremental endpoints: got %d and %d characters", len(sinceSHA), len(throughSHA))
	}
	ancestor, err := isGitAncestor(ctx, repoPath, sinceSHA, throughSHA)
	if err != nil {
		return 0, err
	}
	if !ancestor {
		return 0, fmt.Errorf("cached commit %s is not an ancestor of %s", sinceSHA, throughSHA)
	}

	cmd := repoGitCommand(ctx, repoPath, "rev-list", "--count", fmt.Sprintf("%s..%s", sinceSHA, throughSHA))

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
	objectIDWidth := 0
	for _, sha := range commitSHAs {
		if !isCanonicalCommitSHA(sha) {
			return nil, fmt.Errorf("invalid incremental commit object ID %q", sha)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(sha)
		} else if len(sha) != objectIDWidth {
			return nil, fmt.Errorf("mixed-width incremental commit object IDs: got %d and %d characters", objectIDWidth, len(sha))
		}
	}

	// --no-walk cannot combine with --follow to recover the historical side of
	// a rename. If this exact incremental range contains any rename, even an
	// unrelated one, conservatively make the caller rebuild through the normal
	// single-path --follow extraction. This prevents a status change to an old
	// Beads path from disappearing when the extractor's current primary path is
	// the rename's post-image. Unrelated renames can cost one full refresh, but
	// they cannot corrupt the cached lifecycle history.
	containsRename, err := incrementalRangeContainsRename(extractor, commitSHAs)
	if err != nil {
		return nil, fmt.Errorf("checking incremental range for renames: %w", err)
	}
	if containsRename {
		return nil, fmt.Errorf("incremental range contains a rename; full history refresh required")
	}

	// Use git log with --no-walk to process specific commits exactly as listed.
	// This avoids range semantics (A..B) which can be tricky with root commits
	// or non-linear history segments.
	args := []string{
		"-p",
		"--unified=1",
		"--format=" + gitLogHeaderFormat,
		"--no-walk=unsorted",
	}
	args = append(args, commitSHAs...)
	args = append(args, "--", extractor.primaryBeadsFile())

	cmd := lifecycleGitLogCommand(extractor.ctx, extractor.repoPath, args...)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log for commits failed: %w", err)
	}

	events, err := extractor.parseGitLogOutput(bytes.NewReader(out), filterBeadID)
	if err != nil {
		return nil, err
	}

	// --no-walk=unsorted preserves the oldest-to-newest rev-list order supplied
	// above, including when commit timestamps are backdated or equal.
	return events, nil
}

// incrementalRangeContainsRename reports whether any requested commit has a
// first-parent rename. The probe is deliberately unfiltered by path: filtering
// to the current Beads path would recreate the very pre-image visibility gap it
// guards. lifecycleGitLogCommand pins rename detection and ignores ambient Git
// configuration, while the final --diff-merges override makes merge commits
// conservative too. Empty pretty output means the returned bytes consist only
// of rename records plus Git's record separators.
func incrementalRangeContainsRename(extractor *Extractor, commitSHAs []string) (bool, error) {
	args := []string{
		"--no-walk=unsorted",
		"--name-status",
		"-z",
		"--diff-filter=R",
		"--diff-merges=first-parent",
		"--format=",
		"--end-of-options",
	}
	args = append(args, commitSHAs...)
	args = append(args, "--")

	cmd := lifecycleGitLogCommand(extractor.ctx, extractor.repoPath, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return false, fmt.Errorf("git rename probe failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return false, fmt.Errorf("git rename probe failed: %w", err)
	}
	return len(bytes.Trim(out, "\x00\r\n")) != 0, nil
}

// mergeReports creates a new report by merging existing data with new events/commits
func mergeReports(existing *HistoryReport, beads []BeadInfo, newEvents []BeadEvent, newCommits []CorrelatedCommit) *HistoryReport {
	return mergeReportsThrough(existing, beads, newEvents, newCommits, "")
}

func mergeReportsThrough(existing *HistoryReport, beads []BeadInfo, newEvents []BeadEvent, newCommits []CorrelatedCommit, latestProcessedSHA string) *HistoryReport {
	currentBeadIDs := make(map[string]struct{}, len(beads))
	for _, bead := range beads {
		currentBeadIDs[bead.ID] = struct{}{}
	}

	// Create a deep copy of existing histories
	histories := make(map[string]BeadHistory, len(existing.Histories))
	for id, h := range existing.Histories {
		if _, keep := currentBeadIDs[id]; !keep {
			continue
		}
		// Deep copy the history. Milestone and cycle-time pointers must refer to
		// the new report rather than retaining mutable objects owned by the cache
		// entry that served as the incremental base.
		eventsCopy := make([]BeadEvent, len(h.Events))
		copy(eventsCopy, h.Events)
		commitsCopy := make([]CorrelatedCommit, len(h.Commits))
		for i := range h.Commits {
			commitsCopy[i] = h.Commits[i]
			commitsCopy[i].Files = append([]FileChange(nil), h.Commits[i].Files...)
		}
		milestones := GetBeadMilestones(eventsCopy)

		histories[id] = BeadHistory{
			BeadID:     h.BeadID,
			Title:      h.Title,
			Status:     h.Status,
			Events:     eventsCopy,
			Milestones: milestones,
			Commits:    commitsCopy,
			CycleTime:  CalculateCycleTime(milestones),
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
			h.LastAuthor = mostRecentHistoryAuthor(h.Events, h.Commits)
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
			h.LastAuthor = mostRecentHistoryAuthor(h.Events, h.Commits)
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
		DataHash:        hashBeads(beads),
		GitRange:        existing.GitRange,
		LatestCommitSHA: latestSHA,
		Stats:           stats,
		Histories:       histories,
		CommitIndex:     commitIndex,
	}
}

// calculateMergedStats computes statistics for the merged report
func calculateMergedStats(histories map[string]BeadHistory, newCommits []CorrelatedCommit) HistoryStats {
	return calculateHistoryStats(histories)
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
	if !coCommitPersistentCacheSafe(context.Background(), repoPath) {
		return false, 0, nil
	}

	count, err := countCommitsSince(context.Background(), repoPath, cachedReport.LatestCommitSHA)
	if err != nil {
		return false, 0, err
	}

	return count <= IncrementalThreshold, count, nil
}
