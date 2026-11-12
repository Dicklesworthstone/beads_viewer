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

	rawMu     sync.RWMutex
	rawStates map[string]*incrementalRawState

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// incremental updates (issue #166). nil means context.Background().
	ctx context.Context
}

type incrementalRawState struct {
	headSHA   string
	events    []BeadEvent
	coCommits []CorrelatedCommit
	walk      []walkedCommit
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

// WithFeedbackStore attaches the confirm/reject feedback store to the
// underlying correlator so both full refreshes and incremental updates preserve
// and re-apply user feedback overrides.
func (ic *IncrementalCorrelator) WithFeedbackStore(store *FeedbackStore) *IncrementalCorrelator {
	if ic.correlator != nil {
		ic.correlator.WithFeedbackStore(store)
	}
	return ic
}

// NewIncrementalCorrelator creates a correlator with incremental update support.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewIncrementalCorrelator(repoPath string, beadsFilePath ...string) *IncrementalCorrelator {
	return &IncrementalCorrelator{
		correlator: NewCorrelator(repoPath, beadsFilePath...),
		cache:      NewHistoryCache(repoPath),
		rawStates:  make(map[string]*incrementalRawState),
	}
}

// NewIncrementalCorrelatorWithOptions creates a correlator with custom cache settings.
// beadsFilePath is optional and forwarded to the underlying correlator.
func NewIncrementalCorrelatorWithOptions(repoPath string, maxAge time.Duration, maxSize int, beadsFilePath ...string) *IncrementalCorrelator {
	return &IncrementalCorrelator{
		correlator: NewCorrelator(repoPath, beadsFilePath...),
		cache:      NewHistoryCacheWithOptions(repoPath, maxAge, maxSize),
		rawStates:  make(map[string]*incrementalRawState),
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

func canUseIncrementalMode(opts CorrelatorOptions) bool {
	return opts.Revision == "" && opts.CausalityBeadID == "" && opts.Since == nil && opts.Until == nil
}

// GenerateReportWithDetails generates a report and returns detailed update information
func (ic *IncrementalCorrelator) GenerateReportWithDetails(beads []BeadInfo, opts CorrelatorOptions) (*IncrementalUpdateResult, error) {
	if ic.correlator != nil && ic.correlator.explicit != nil {
		ic.correlator.explicit.SetKnownBeadIDs(beadIDsFromInfos(beads))
	}

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
	key.Feedback = ic.correlator.feedbackFingerprint()

	// Check cache
	if cached, ok := ic.cache.Get(key); ok {
		ic.recordCacheHit()
		return &IncrementalUpdateResult{
			Report:         cached,
			WasIncremental: true,
			NewCommitCount: 0,
		}, nil
	}

	// Cache miss - try incremental update through live HEAD when options allow.
	// Revision-bound, date-bounded, or full causal evidence queries require
	// complete extraction below; same-key cache hits above remain usable.
	if canUseIncrementalMode(opts) {
		if state := ic.getRawState(opts); state != nil && state.headSHA != "" {
			result, nextState, incErr := ic.tryIncrementalUpdateFromRaw(state, key.HeadSHA, beads, opts)
			if incErr == nil && result != nil {
				ic.putRawState(opts, nextState)
				ic.recordIncrementalUpdate()
				ic.cache.Put(key, result.Report)
				return result, nil
			}
		} else if existingReport := ic.findExistingReport(beads, opts); existingReport != nil && existingReport.LatestCommitSHA != "" {
			result, incErr := ic.tryIncrementalUpdate(existingReport, beads, opts)
			if incErr == nil && result != nil {
				ic.recordIncrementalUpdate()
				ic.cache.Put(key, result.Report)
				return result, nil
			}
		}
	}

	// Full refresh
	ic.recordFullRefresh()

	if canUseIncrementalMode(opts) {
		art, walk, err := ic.correlator.extractHistoryArtifactWithWalk(opts)
		if err != nil {
			return nil, err
		}
		report := ic.correlator.assembleReport(beads, opts, art)
		if key.HeadSHA != "" {
			report.LatestCommitSHA = key.HeadSHA
		}
		ic.putRawState(opts, &incrementalRawState{
			headSHA:   key.HeadSHA,
			events:    append([]BeadEvent(nil), art.Events...),
			coCommits: cloneCorrelatedCommits(art.Commits),
			walk:      cloneWalkedCommits(walk),
		})
		ic.cache.Put(key, report)
		return &IncrementalUpdateResult{
			Report:         report,
			WasIncremental: false,
			RefreshReason:  "no suitable cached report for incremental update",
		}, nil
	}

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

func (ic *IncrementalCorrelator) getRawState(opts CorrelatorOptions) *incrementalRawState {
	rawKey := hashOptions(opts)
	ic.rawMu.RLock()
	defer ic.rawMu.RUnlock()
	if ic.rawStates == nil {
		return nil
	}
	return ic.rawStates[rawKey]
}

func (ic *IncrementalCorrelator) putRawState(opts CorrelatorOptions, state *incrementalRawState) {
	if state == nil {
		return
	}
	rawKey := hashOptions(opts)
	ic.rawMu.Lock()
	defer ic.rawMu.Unlock()
	if ic.rawStates == nil {
		ic.rawStates = make(map[string]*incrementalRawState)
	}
	ic.rawStates[rawKey] = state
}

func cloneWalkedCommits(walk []walkedCommit) []walkedCommit {
	cloned := make([]walkedCommit, len(walk))
	for i := range walk {
		cloned[i] = walk[i]
		if walk[i].Files != nil {
			cloned[i].Files = append([]string(nil), walk[i].Files...)
		}
	}
	return cloned
}

// findExistingReport looks for a cached report that can be incrementally updated
func (ic *IncrementalCorrelator) findExistingReport(beads []BeadInfo, opts CorrelatorOptions) *HistoryReport {
	optsPrefix := hashOptions(opts) + ":matcher:"
	optsHash := ic.correlator.cacheOptionsHash(opts)
	beadsHash := hashBeads(beads)

	ic.cache.mu.RLock()
	defer ic.cache.mu.RUnlock()

	var fallback *HistoryReport
	for _, entry := range ic.cache.entries {
		if entry.Key.BeadsHash == beadsHash && entry.Key.Options == optsHash {
			return entry.Report
		}
		if strings.HasPrefix(entry.Key.Options, optsPrefix) {
			fallback = entry.Report
		}
	}
	return fallback
}

func (ic *IncrementalCorrelator) tryIncrementalUpdateFromRaw(
	state *incrementalRawState,
	currentHeadSHA string,
	beads []BeadInfo,
	opts CorrelatorOptions,
) (*IncrementalUpdateResult, *incrementalRawState, error) {
	var newCommitSHAs []string
	if currentHeadSHA != state.headSHA {
		if !isCommitAncestor(ic.ctx, ic.cache.repoPath, state.headSHA, currentHeadSHA) {
			return nil, nil, fmt.Errorf("cached HEAD %s is not an ancestor of %s", state.headSHA, currentHeadSHA)
		}
		var err error
		newCommitSHAs, err = getCommitsSince(ic.ctx, ic.cache.repoPath, state.headSHA)
		if err != nil {
			return nil, nil, fmt.Errorf("finding new commits: %w", err)
		}
		if len(newCommitSHAs) > IncrementalThreshold {
			return nil, nil, fmt.Errorf("too many new commits (%d > %d)", len(newCommitSHAs), IncrementalThreshold)
		}
	}

	var newEvents []BeadEvent
	var newCoCommits []CorrelatedCommit
	var newWalk []walkedCommit
	if len(newCommitSHAs) > 0 {
		extractor := ic.incrementalExtractor()
		var err error
		newEvents, err = extractEventsFromCommits(extractor, newCommitSHAs, opts.BeadID)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting new events: %w", err)
		}
		newCoCommits, err = ic.correlator.coCommitter.ExtractAllCoCommits(newEvents)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting co-commits: %w", err)
		}
		newWalk, err = walkCommitsBySHAs(ic.ctx, ic.cache.repoPath, newCommitSHAs)
		if err != nil {
			return nil, nil, fmt.Errorf("walking incremental commits: %w", err)
		}
	}

	allEvents := make([]BeadEvent, 0, len(state.events)+len(newEvents))
	allEvents = append(allEvents, state.events...)
	allEvents = append(allEvents, newEvents...)

	allCoCommits := make([]CorrelatedCommit, 0, len(state.coCommits)+len(newCoCommits))
	allCoCommits = append(allCoCommits, cloneCorrelatedCommits(state.coCommits)...)
	allCoCommits = append(allCoCommits, newCoCommits...)

	allWalk := make([]walkedCommit, 0, len(state.walk)+len(newWalk))
	allWalk = append(allWalk, cloneWalkedCommits(state.walk)...)
	allWalk = append(allWalk, newWalk...)

	if opts.Limit > 0 {
		if len(allWalk) > opts.Limit {
			allWalk = allWalk[len(allWalk)-opts.Limit:]
		}
		allEvents, allCoCommits = trimEventsAndCoCommitsToLimit(allEvents, allCoCommits, opts.Limit)
	}

	startCoCommit := time.Now()
	coCommitCandidates := len(allCoCommits)
	coCommitDur := durationMS(time.Since(startCoCommit))

	startExplicit := time.Now()
	matcher := ic.correlator.explicit.withMergedKnownIDs(beadIDsFromEvents(allEvents))
	explicitCommits, err := ic.correlator.extractExplicitCommitsWithMatcher(matcher, allWalk, opts.BeadID)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting explicit-id commits: %w", err)
	}
	explicitDur := durationMS(time.Since(startExplicit))

	startTemporal := time.Now()
	temporalCandidates, err := ic.correlator.extractTemporalCandidates(allEvents, allWalk, opts.BeadID)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting temporal candidates: %w", err)
	}
	temporalDur := durationMS(time.Since(startTemporal))

	art := &historyArtifact{
		FormatVersion: historyArtifactFormatVersion,
		Events:        allEvents,
		Commits:       allCoCommits,
		Explicit:      explicitCommits,
		Temporal:      temporalCandidates,
		WalkedCommits: len(allWalk),
		Strategies: []StrategyRun{
			{Name: MethodCoCommitted.String(), Ran: true, DurationMS: coCommitDur, Candidates: coCommitCandidates},
			{Name: MethodExplicitID.String(), Ran: true, DurationMS: explicitDur, Candidates: len(explicitCommits)},
			{Name: MethodTemporalAuthor.String(), Ran: true, DurationMS: temporalDur, Candidates: len(temporalCandidates)},
		},
	}

	merged := ic.correlator.assembleReport(beads, opts, art)
	nextHead := currentHeadSHA
	if nextHead == "" && len(newCommitSHAs) > 0 {
		nextHead = newCommitSHAs[len(newCommitSHAs)-1]
	}
	if nextHead == "" {
		nextHead = state.headSHA
	}
	merged.LatestCommitSHA = nextHead

	mergedNewCommits := 0
	if len(newCommitSHAs) > 0 {
		newSet := make(map[string]struct{}, len(newCommitSHAs))
		for _, sha := range newCommitSHAs {
			newSet[sha] = struct{}{}
		}
		seenSHAs := make(map[string]struct{})
		for _, h := range merged.Histories {
			for _, cc := range h.Commits {
				if _, isNew := newSet[cc.SHA]; isNew {
					seenSHAs[cc.SHA] = struct{}{}
				}
			}
		}
		mergedNewCommits = len(seenSHAs)
	}

	nextState := &incrementalRawState{
		headSHA:   nextHead,
		events:    allEvents,
		coCommits: cloneCorrelatedCommits(allCoCommits),
		walk:      cloneWalkedCommits(allWalk),
	}

	return &IncrementalUpdateResult{
		Report:            merged,
		WasIncremental:    true,
		NewCommitCount:    len(newCommitSHAs),
		MergedEventCount:  len(newEvents),
		MergedCommitCount: mergedNewCommits,
	}, nextState, nil
}

func trimEventsAndCoCommitsToLimit(events []BeadEvent, coCommits []CorrelatedCommit, limit int) ([]BeadEvent, []CorrelatedCommit) {
	if limit <= 0 || len(events) == 0 {
		return events, coCommits
	}
	var distinctSHAs []string
	seen := make(map[string]struct{})
	for _, ev := range events {
		if _, ok := seen[ev.CommitSHA]; !ok {
			seen[ev.CommitSHA] = struct{}{}
			distinctSHAs = append(distinctSHAs, ev.CommitSHA)
		}
	}
	if len(distinctSHAs) <= limit {
		return events, coCommits
	}
	keep := make(map[string]struct{}, limit)
	for _, sha := range distinctSHAs[len(distinctSHAs)-limit:] {
		keep[sha] = struct{}{}
	}
	trimmedEvents := make([]BeadEvent, 0, len(events))
	for _, ev := range events {
		if _, ok := keep[ev.CommitSHA]; ok {
			trimmedEvents = append(trimmedEvents, ev)
		}
	}
	trimmedCommits := make([]CorrelatedCommit, 0, len(coCommits))
	for _, cc := range coCommits {
		if _, ok := keep[cc.SHA]; ok {
			trimmedCommits = append(trimmedCommits, cc)
		}
	}
	return trimmedEvents, trimmedCommits
}

func isCommitAncestor(ctx context.Context, repoPath, ancestorSHA, descendantSHA string) bool {
	if ancestorSHA == "" || descendantSHA == "" {
		return false
	}
	if ancestorSHA == descendantSHA {
		return true
	}
	cmd := gitCommand(ctx, "merge-base", "--is-ancestor", ancestorSHA, descendantSHA)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

// tryIncrementalUpdate attempts to update an existing report incrementally when
// only a HistoryReport (without raw walk state) is present in the cache.
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

	// If no new commits, re-merge metadata, feedback, and DataHash against current beads
	if len(newCommitSHAs) == 0 {
		merged := mergeReportsWithCorrelator(ic.correlator, existing, beads, nil, nil, existing.LatestCommitSHA)
		return &IncrementalUpdateResult{
			Report:         merged,
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
	newCoCommits, err := ic.correlator.coCommitter.ExtractAllCoCommits(newEvents)
	if err != nil {
		return nil, fmt.Errorf("extracting co-commits: %w", err)
	}

	newWalk, err := walkCommitsBySHAs(ic.ctx, ic.cache.repoPath, newCommitSHAs)
	if err != nil {
		return nil, fmt.Errorf("walking incremental commits: %w", err)
	}

	// Collect all known IDs and combined events from existing histories + newEvents + beads
	knownIDs := beadIDsFromInfos(beads)
	var allEvents []BeadEvent
	for id, h := range existing.Histories {
		if id != "" {
			knownIDs = append(knownIDs, id)
		}
		allEvents = append(allEvents, h.Events...)
	}
	allEvents = append(allEvents, newEvents...)
	knownIDs = append(knownIDs, beadIDsFromEvents(allEvents)...)

	matcher := ic.correlator.explicit.withMergedKnownIDs(knownIDs)
	newExplicit, err := ic.correlator.extractExplicitCommitsWithMatcher(matcher, newWalk, opts.BeadID)
	if err != nil {
		return nil, fmt.Errorf("extracting explicit-id commits: %w", err)
	}

	newTemporalCands, err := ic.correlator.extractTemporalCandidates(allEvents, newWalk, opts.BeadID)
	if err != nil {
		return nil, fmt.Errorf("extracting temporal candidates: %w", err)
	}

	titles := make(map[string]string, len(beads))
	for _, h := range existing.Histories {
		titles[h.BeadID] = h.Title
	}
	for _, b := range beads {
		titles[b.ID] = b.Title
	}
	explicitBeadsBySHA := make(map[string]map[string]struct{})
	for _, cc := range newExplicit {
		if explicitBeadsBySHA[cc.SHA] == nil {
			explicitBeadsBySHA[cc.SHA] = make(map[string]struct{})
		}
		explicitBeadsBySHA[cc.SHA][cc.BeadID] = struct{}{}
	}
	var newTemporal []CorrelatedCommit
	for _, cand := range newTemporalCands {
		if expSet, ok := explicitBeadsBySHA[cand.SHA]; ok {
			if _, same := expSet[cand.BeadID]; !same {
				continue
			}
		}
		newTemporal = append(newTemporal, finalizeTemporalCandidate(cand, titles[cand.BeadID]))
	}

	allNewCommits := make([]CorrelatedCommit, 0, len(newCoCommits)+len(newExplicit)+len(newTemporal))
	allNewCommits = append(allNewCommits, newCoCommits...)
	allNewCommits = append(allNewCommits, newExplicit...)
	allNewCommits = append(allNewCommits, newTemporal...)

	// Merge new data with existing report
	merged := mergeReportsWithCorrelator(ic.correlator, existing, beads, newEvents, allNewCommits, newCommitSHAs[len(newCommitSHAs)-1])

	return &IncrementalUpdateResult{
		Report:            merged,
		WasIncremental:    true,
		NewCommitCount:    len(newCommitSHAs),
		MergedEventCount:  len(newEvents),
		MergedCommitCount: len(allNewCommits),
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

	// Use git log with --no-walk to process specific commits.
	args := []string{
		"log",
		"-p",
		"--unified=0",
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

	parsed, err := extractor.parseGitLogOutput(bytes.NewReader(out), filterBeadID)
	if err != nil {
		return nil, err
	}

	// Reassemble commit groups in the caller's chronological commitSHAs order
	// while preserving deterministic within-commit event ordering (EventCreated
	// before synthesized EventClaimed / EventClosed).
	bySHA := make(map[string][]BeadEvent, len(commitSHAs))
	for _, ev := range parsed {
		bySHA[ev.CommitSHA] = append(bySHA[ev.CommitSHA], ev)
	}
	events := make([]BeadEvent, 0, len(parsed))
	seen := make(map[string]struct{}, len(commitSHAs))
	for _, sha := range commitSHAs {
		if _, dup := seen[sha]; dup {
			continue
		}
		seen[sha] = struct{}{}
		events = append(events, bySHA[sha]...)
	}
	return events, nil
}

func walkCommitsBySHAs(ctx context.Context, repoPath string, commitSHAs []string) ([]walkedCommit, error) {
	if len(commitSHAs) == 0 {
		return nil, nil
	}
	args := []string{
		"log",
		"--no-merges",
		"--name-only",
		"--no-walk",
		"--format=" + commitWalkFormat,
	}
	args = append(args, commitSHAs...)

	cmd := gitCommand(ctx, withNoColorGit(args)...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("git log incremental walk: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log incremental walk failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git log incremental walk failed: %w", err)
	}
	parsed, err := parseCommitWalk(out)
	if err != nil {
		return nil, err
	}
	bySHA := make(map[string]walkedCommit, len(parsed))
	for _, wc := range parsed {
		bySHA[wc.SHA] = wc
	}
	ordered := make([]walkedCommit, 0, len(parsed))
	seen := make(map[string]struct{}, len(commitSHAs))
	for _, sha := range commitSHAs {
		if _, dup := seen[sha]; dup {
			continue
		}
		seen[sha] = struct{}{}
		if wc, ok := bySHA[sha]; ok {
			ordered = append(ordered, wc)
		}
	}
	return ordered, nil
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
	return mergeReportsWithCorrelator(nil, existing, beads, newEvents, newCommits, latestProcessedSHA)
}

func mergeReportsWithCorrelator(c *Correlator, existing *HistoryReport, beads []BeadInfo, newEvents []BeadEvent, newCommits []CorrelatedCommit, latestProcessedSHA string) *HistoryReport {
	scorer := NewScorer()
	if c != nil && c.scorer != nil {
		scorer = c.scorer
	}

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

	// Merge new commits grouped by bead and correlation method so distinct
	// strategies combine signals while exact duplicates within the same method
	// are deduplicated cleanly.
	commitsByBead := make(map[string][]CorrelatedCommit)
	for _, commit := range newCommits {
		if commit.BeadID != "" {
			commitsByBead[commit.BeadID] = append(commitsByBead[commit.BeadID], commit)
		}
	}

	for beadID, commits := range commitsByBead {
		if h, exists := histories[beadID]; exists {
			h.Commits = mergeIncrementalBeadCommits(scorer, h.Commits, commits)
			// Update last author
			if len(h.Commits) > 0 {
				h.LastAuthor = h.Commits[len(h.Commits)-1].Author
			} else if len(h.Events) > 0 {
				h.LastAuthor = h.Events[len(h.Events)-1].Author
			}
			histories[beadID] = h
		}
	}

	// Ensure every history has a non-nil Commits slice.
	for beadID, h := range histories {
		if h.Commits == nil {
			h.Commits = []CorrelatedCommit{}
			histories[beadID] = h
		}
	}

	var feedbackApplied *FeedbackApplied
	if c != nil && c.feedback != nil {
		feedbackApplied = applyFeedbackIdempotent(c.feedback, histories)
	}

	// Build a stable reverse index; HistoryReport exposes these map-derived
	// values as arrays in robot JSON.
	commitIndex := BuildCommitIndex(histories)

	// Calculate new stats
	stats := calculateMergedStats(histories, newCommits)
	stats.FeedbackApplied = feedbackApplied

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

	dataHash := existing.DataHash
	if beads != nil || dataHash == "" {
		dataHash = hashBeads(beads)
	}

	return &HistoryReport{
		GeneratedAt:     time.Now().UTC(),
		DataHash:        dataHash,
		GitRange:        existing.GitRange + " (incremental)",
		LatestCommitSHA: latestSHA,
		Stats:           stats,
		Histories:       histories,
		CommitIndex:     commitIndex,
	}
}

func mergeIncrementalBeadCommits(scorer *Scorer, existing, incoming []CorrelatedCommit) []CorrelatedCommit {
	byMethod := make(map[CorrelationMethod][]CorrelatedCommit)
	var methodOrder []CorrelationMethod
	add := func(cc CorrelatedCommit) {
		if _, ok := byMethod[cc.Method]; !ok {
			methodOrder = append(methodOrder, cc.Method)
		}
		byMethod[cc.Method] = append(byMethod[cc.Method], cc)
	}
	for _, cc := range existing {
		add(cc)
	}
	for _, cc := range incoming {
		add(cc)
	}
	if len(methodOrder) == 0 {
		return []CorrelatedCommit{}
	}
	if len(methodOrder) == 1 && methodOrder[0] == "" {
		return dedupCommits(byMethod[""])
	}
	lists := make([][]CorrelatedCommit, 0, len(methodOrder))
	for _, m := range methodOrder {
		lists = append(lists, byMethod[m])
	}
	return mergeCorrelatedCommits(scorer, lists...)
}

func applyFeedbackIdempotent(store *FeedbackStore, histories map[string]BeadHistory) *FeedbackApplied {
	if store == nil {
		return nil
	}
	applied := &FeedbackApplied{}
	for beadID, history := range histories {
		if len(history.Commits) == 0 {
			continue
		}
		kept := make([]CorrelatedCommit, 0, len(history.Commits))
		changed := false
		for _, commit := range history.Commits {
			fb, ok := store.Get(commit.SHA, beadID)
			if !ok {
				kept = append(kept, commit)
				continue
			}
			switch fb.Type {
			case FeedbackReject:
				applied.Rejected++
				changed = true
				continue
			case FeedbackConfirm:
				applied.Confirmed++
				changed = true
				commit.Confidence = 1.0
				if !commit.Confirmed {
					commit.Confirmed = true
					commit.Reason = commit.Reason + "; confirmed by feedback (" + fb.FeedbackBy + ")"
				}
			case FeedbackIgnore:
				applied.Ignored++
			}
			kept = append(kept, commit)
		}
		if !changed {
			continue
		}
		history.Commits = kept
		if len(kept) > 0 {
			history.LastAuthor = kept[len(kept)-1].Author
		} else if len(history.Events) > 0 {
			history.LastAuthor = history.Events[len(history.Events)-1].Author
		} else {
			history.LastAuthor = ""
		}
		histories[beadID] = history
	}
	return applied
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
	ic.rawMu.Lock()
	ic.rawStates = make(map[string]*incrementalRawState)
	ic.rawMu.Unlock()
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
