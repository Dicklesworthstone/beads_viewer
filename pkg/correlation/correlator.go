// Package correlation provides the Correlator for building complete bead history reports.
package correlation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	json "github.com/goccy/go-json"
)

// Correlator orchestrates the extraction and correlation of bead history data.
// It runs three strategies over the same bounded commit window and merges
// their results per (commit, bead):
//
//   - co_committed: commits that changed a bead record alongside code
//     (extractor + coCommitter);
//   - explicit_id: commits whose message references a bead id (explicit);
//   - temporal_author: commits by a bead's claimant inside its claimed→closed
//     window that no higher-confidence strategy already linked.
//
// An optional FeedbackStore (WithFeedbackStore) is consulted during report
// assembly: rejected pairs are dropped, confirmed pairs are pinned to 1.0.
type Correlator struct {
	repoPath    string
	extractor   *Extractor
	coCommitter *CoCommitExtractor
	explicit    *ExplicitMatcher
	scorer      *Scorer
	feedback    *FeedbackStore

	// ctx, when set via WithContext, bounds every git subprocess spawned
	// during report generation (issue #166). nil means context.Background().
	ctx context.Context
}

// NewCorrelator creates a new correlator for the given repository.
// beadsFilePath is optional and forwarded to the extractor so history follows
// the correct beads file; variadic form preserves compatibility with older
// single-argument callers.
func NewCorrelator(repoPath string, beadsFilePath ...string) *Correlator {
	return &Correlator{
		repoPath:    repoPath,
		extractor:   NewExtractor(repoPath, beadsFilePath...),
		coCommitter: NewCoCommitExtractor(repoPath),
		explicit:    NewExplicitMatcher(repoPath),
		scorer:      NewScorer(),
	}
}

// WithContext binds ctx to the correlator and its underlying extractors so
// every git subprocess spawned while generating a report is killed as soon as
// ctx is cancelled (issue #166: bounded robot liveness). It mutates and
// returns the receiver for chaining:
//
//	report, err := NewCorrelator(dir).WithContext(ctx).GenerateReportCached(beads, opts)
//
// A nil ctx (or never calling WithContext) preserves the legacy
// run-to-completion behavior.
func (c *Correlator) WithContext(ctx context.Context) *Correlator {
	c.ctx = ctx
	c.extractor.ctx = ctx
	c.coCommitter.ctx = ctx
	c.explicit.ctx = ctx
	return c
}

// WithFeedbackStore attaches the confirm/reject store so every report this
// correlator assembles honors stored feedback (see applyFeedback). It mutates
// and returns the receiver for chaining:
//
//	report, err := NewCorrelator(dir, beadsPath).WithFeedbackStore(store).GenerateReportCached(beads, opts)
//
// A nil store leaves reports unfiltered and stats.feedback_applied absent.
func (c *Correlator) WithFeedbackStore(store *FeedbackStore) *Correlator {
	c.feedback = store
	return c
}

// feedbackFingerprint returns the attached store's Fingerprint, or "" when no
// store is attached. It keys the assembled-report caches (never the artifact).
func (c *Correlator) feedbackFingerprint() string {
	if c.feedback == nil {
		return ""
	}
	return c.feedback.Fingerprint()
}

// CorrelatorOptions controls how the history report is generated
type CorrelatorOptions struct {
	Revision        string     // Resolved commit to walk from (empty = HEAD)
	BeadID          string     // Filter to single bead ID (empty = all)
	Since           *time.Time // Only events after this time
	Until           *time.Time // Only events before this time
	Limit           int        // Max commits to process (0 = no limit)
	CausalityBeadID string     // Capture full committed dependency authority for this target only
}

// historyArtifactFormatVersion is the schema version of historyArtifact. The
// persistent HEAD-artifact cache stores it with every entry and treats a
// mismatch as a miss (see getHeadArtifactCached), so a binary with a newer
// artifact shape lazily rebuilds instead of reading fields that were never
// written. History: v1 = events + co-commit commits only; v2 = adds
// per-method explicit-ID commits, temporal candidates, the walked window and
// per-strategy timings; v3 = owned before/after transitions and optional full
// historical constraint evidence for one causal-analysis target.
const historyArtifactFormatVersion = 3

// historyArtifact holds the purely history-derived (HEAD + options only)
// intermediate products of report generation: the extracted lifecycle events
// and the per-strategy correlations derived from committed history. NOTHING
// here depends on the passed-in beads slice (bead ID/Title/Status) — Extract
// reads only committed git history, ExtractAllCoCommits is a pure function of
// events, the explicit-ID and temporal strategies read the committed walk, and
// the title-dependent part of temporal scoring is deferred to assembly (see
// temporalCandidate). This is the expensive part of GenerateReport (the git-blob
// extraction plus the batched git logs), so it is the unit cached by the
// HEAD-keyed disk cache and reused unchanged across working-tree bead edits.
type historyArtifact struct {
	FormatVersion int                 `json:"format_version"`
	Events        []BeadEvent         `json:"events"`
	Commits       []CorrelatedCommit  `json:"commits"`  // co_committed
	Explicit      []CorrelatedCommit  `json:"explicit"` // explicit_id (final confidence)
	Temporal      []temporalCandidate `json:"temporal"` // temporal_author (scored at assembly)
	WalkedCommits int                 `json:"walked_commits"`
	Strategies    []StrategyRun       `json:"strategies"`
	CausalHistory *CausalHistory      `json:"causal_history,omitempty"`
}

// CorrelatedCommit.BeadID carries the json:"-" tag (it is internal linking state,
// intentionally hidden from the public report JSON). The HEAD-artifact disk cache,
// however, serializes the PRE-assembly commit slices and MUST round-trip BeadID —
// assembleReport groups commits onto beads by exactly that field. Without this the
// cache would return commit-less reports on the middle-tier (bead-edit) path.
// Custom (Un)MarshalJSON preserves BeadID via parallel *_bead_ids arrays
// without disturbing the public CorrelatedCommit tag.
type historyArtifactWire struct {
	FormatVersion   int                 `json:"format_version"`
	Events          []BeadEvent         `json:"events"`
	Commits         []CorrelatedCommit  `json:"commits"`
	CommitBeadIDs   []string            `json:"commit_bead_ids,omitempty"`
	Explicit        []CorrelatedCommit  `json:"explicit,omitempty"`
	ExplicitBeadIDs []string            `json:"explicit_bead_ids,omitempty"`
	Temporal        []temporalCandidate `json:"temporal,omitempty"`
	WalkedCommits   int                 `json:"walked_commits"`
	Strategies      []StrategyRun       `json:"strategies,omitempty"`
	CausalHistory   *CausalHistory      `json:"causal_history,omitempty"`
}

func commitBeadIDs(commits []CorrelatedCommit) []string {
	ids := make([]string, len(commits))
	for i := range commits {
		ids[i] = commits[i].BeadID
	}
	return ids
}

func restoreCommitBeadIDs(field string, commits []CorrelatedCommit, ids []string) error {
	if len(ids) != len(commits) {
		return fmt.Errorf("decoding history artifact: %s_bead_ids length %d does not match %s length %d", field, len(ids), field, len(commits))
	}
	for i := range commits {
		commits[i].BeadID = ids[i]
	}
	return nil
}

func (a historyArtifact) MarshalJSON() ([]byte, error) {
	return json.Marshal(historyArtifactWire{
		FormatVersion:   a.FormatVersion,
		Events:          a.Events,
		Commits:         a.Commits,
		CommitBeadIDs:   commitBeadIDs(a.Commits),
		Explicit:        a.Explicit,
		ExplicitBeadIDs: commitBeadIDs(a.Explicit),
		Temporal:        a.Temporal,
		WalkedCommits:   a.WalkedCommits,
		Strategies:      a.Strategies,
		CausalHistory:   a.CausalHistory,
	})
}

func (a *historyArtifact) UnmarshalJSON(b []byte) error {
	var w historyArtifactWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	if err := restoreCommitBeadIDs("commits", w.Commits, w.CommitBeadIDs); err != nil {
		return err
	}
	if len(w.Explicit) > 0 || len(w.ExplicitBeadIDs) > 0 {
		if err := restoreCommitBeadIDs("explicit", w.Explicit, w.ExplicitBeadIDs); err != nil {
			return err
		}
	}
	a.FormatVersion = w.FormatVersion
	a.Events = w.Events
	a.Commits = w.Commits
	a.Explicit = w.Explicit
	a.Temporal = w.Temporal
	a.WalkedCommits = w.WalkedCommits
	a.Strategies = w.Strategies
	a.CausalHistory = w.CausalHistory
	return nil
}

// extractHistoryArtifact runs ONLY the HEAD/options-dependent extraction steps
// (git history walk + the three correlation strategies). It is deterministic
// given the repository HEAD and the extract options; it never reads the
// working-tree bead slice. Split out so the result can be memoized
// independently of bead edits. Every git subprocess is bound to c.ctx, so a
// cancelled context aborts whichever strategy is running and returns its
// error (issue #166).
func (c *Correlator) extractHistoryArtifact(opts CorrelatorOptions) (*historyArtifact, error) {
	extractOpts := ExtractOptions{
		Revision: opts.Revision,
		Since:    opts.Since,
		Until:    opts.Until,
		Limit:    opts.Limit,
		BeadID:   opts.BeadID,
	}
	art := &historyArtifact{FormatVersion: historyArtifactFormatVersion}
	if opts.CausalityBeadID != "" {
		var err error
		art.CausalHistory, err = c.extractor.extractCausalHistory(opts.CausalityBeadID, extractOpts)
		if err != nil {
			return nil, fmt.Errorf("extracting causal history: %w", err)
		}
	}

	// Strategy 1: co_committed — lifecycle events from the beads file history
	// plus the code files changed in the same commits.
	start := time.Now()
	events, err := c.extractor.Extract(extractOpts)
	if err != nil {
		return nil, fmt.Errorf("extracting events: %w", err)
	}
	commits, err := c.coCommitter.ExtractAllCoCommits(events)
	if err != nil {
		return nil, fmt.Errorf("extracting co-commits: %w", err)
	}
	art.Events = events
	art.Commits = commits
	art.Strategies = append(art.Strategies, StrategyRun{
		Name: MethodCoCommitted.String(), Ran: true,
		DurationMS: durationMS(time.Since(start)), Candidates: len(commits),
	})

	// One bounded metadata walk feeds both remaining strategies (and defines the
	// window the orphan detector reports).
	if err := c.checkCtx(); err != nil {
		return nil, err
	}
	walk, err := walkCommits(c.ctx, c.repoPath, extractOpts)
	if err != nil {
		return nil, fmt.Errorf("walking commits: %w", err)
	}
	art.WalkedCommits = len(walk)

	// Strategy 2: explicit_id — bead ids referenced in commit messages.
	start = time.Now()
	explicit, err := c.extractExplicitCommits(walk, opts.BeadID)
	if err != nil {
		return nil, fmt.Errorf("extracting explicit-id commits: %w", err)
	}
	art.Explicit = explicit
	art.Strategies = append(art.Strategies, StrategyRun{
		Name: MethodExplicitID.String(), Ran: true,
		DurationMS: durationMS(time.Since(start)), Candidates: len(explicit),
	})

	// Strategy 3: temporal_author — the claimant's code commits inside the
	// bead's active window. Precedence against explicit-ID links is resolved
	// at assembly (mergeStrategyCommits), once it is known which referenced
	// beads actually exist; co-commit links never overlap because those
	// commits touch .beads/ and are excluded here.
	start = time.Now()
	temporal, err := c.extractTemporalCandidates(events, walk, opts.BeadID)
	if err != nil {
		return nil, fmt.Errorf("extracting temporal candidates: %w", err)
	}
	art.Temporal = temporal
	art.Strategies = append(art.Strategies, StrategyRun{
		Name: MethodTemporalAuthor.String(), Ran: true,
		DurationMS: durationMS(time.Since(start)), Candidates: len(temporal),
	})

	return art, nil
}

func durationMS(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// checkCtx reports the bound context's error, if any, so a cancellation that
// landed between two strategies is honored before the next git call.
func (c *Correlator) checkCtx() error {
	if c.ctx == nil {
		return nil
	}
	return c.ctx.Err()
}

// extractExplicitCommits converts walk matches into CorrelatedCommits with the
// commit's code files (served from the co-commit extractor's batched cache).
// Like the co-commit strategy it keeps only commits that touched at least one
// code file: a bookkeeping-only commit that names a bead is not a code change.
func (c *Correlator) extractExplicitCommits(walk []walkedCommit, beadFilter string) ([]CorrelatedCommit, error) {
	matches := c.explicit.MatchWalkedCommits(walk, beadFilter)
	if len(matches) == 0 {
		return nil, nil
	}
	shas := make([]string, 0, len(matches))
	for _, m := range matches {
		shas = append(shas, m.CommitSHA)
	}
	if err := c.coCommitter.primeBatch(shas); err != nil {
		return nil, err
	}
	commits := make([]CorrelatedCommit, 0, len(matches))
	for _, m := range matches {
		cc := c.explicit.CreateCorrelatedCommit(m, c.coCommitter)
		if len(cc.Files) == 0 {
			continue
		}
		commits = append(commits, cc)
	}
	return commits, nil
}

// extractTemporalCandidates finds, for every bead with a claimed→closed
// window, the walked commits by the claimant inside that window that (a) did
// not touch .beads/ (those belong to the co-commit strategy) and (b) changed
// at least one code file.
func (c *Correlator) extractTemporalCandidates(events []BeadEvent, walk []walkedCommit, beadFilter string) ([]temporalCandidate, error) {
	windows := temporalWindowsFromEvents(events, beadFilter)
	if len(windows) == 0 {
		return nil, nil
	}

	type pair struct {
		window TemporalWindow
		commit walkedCommit
	}
	var pairs []pair
	var shas []string
	queued := make(map[string]struct{})
	for _, w := range windows {
		for _, wc := range walk {
			if wc.touchesBeadsDir() || !commitInWindow(w, wc) {
				continue
			}
			pairs = append(pairs, pair{window: w, commit: wc})
			if _, ok := queued[wc.SHA]; !ok {
				queued[wc.SHA] = struct{}{}
				shas = append(shas, wc.SHA)
			}
		}
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	if err := c.coCommitter.primeBatch(shas); err != nil {
		return nil, err
	}

	filesBySHA := make(map[string][]FileChange, len(shas))
	candidates := make([]temporalCandidate, 0, len(pairs))
	for _, p := range pairs {
		files, ok := filesBySHA[p.commit.SHA]
		if !ok {
			var err error
			files, err = c.coCommitter.ExtractCoCommittedFiles(BeadEvent{CommitSHA: p.commit.SHA})
			if err != nil {
				return nil, fmt.Errorf("extracting files for %s: %w", p.commit.SHA, err)
			}
			filesBySHA[p.commit.SHA] = files
		}
		if len(files) == 0 {
			continue
		}
		candidates = append(candidates, temporalCandidate{
			BeadID:       p.window.BeadID,
			SHA:          p.commit.SHA,
			Message:      p.commit.Subject,
			Author:       p.commit.Author,
			AuthorEmail:  p.commit.AuthorEmail,
			Timestamp:    p.commit.Timestamp,
			Files:        files,
			WindowAuthor: p.window.Author,
			WindowStart:  p.window.Start,
			WindowEnd:    p.window.End,
			ActiveBeads:  p.window.ActiveBeads,
		})
	}
	return candidates, nil
}

// GenerateReport generates a complete history report
func (c *Correlator) GenerateReport(beads []BeadInfo, opts CorrelatorOptions) (*HistoryReport, error) {
	art, err := c.extractHistoryArtifact(opts)
	if err != nil {
		return nil, err
	}
	return c.assembleReport(beads, opts, art), nil
}

// assembleReport builds the final HistoryReport from the current bead slice and
// a (possibly cached) history artifact. Every step here is cheap and depends on
// the passed-in beads (title/status enrichment, temporal path hints, stored
// feedback, stats, data hash); the expensive history extraction lives in
// extractHistoryArtifact. Splitting the two lets a working-tree bead edit
// (which flips hashBeads but not HEAD) reuse the cached artifact and re-run
// only this assembly. The output is a pure function of (beads, opts, artifact,
// feedback store contents).
func (c *Correlator) assembleReport(beads []BeadInfo, opts CorrelatorOptions, art *historyArtifact) *HistoryReport {
	events := art.Events
	commits := art.Commits

	// Build bead histories from the co-commit strategy, then merge the
	// explicit-ID and temporal strategies per (commit, bead).
	histories := c.buildHistories(beads, events, commits)
	c.mergeStrategyCommits(histories, beads, art)

	// Honor stored confirm/reject feedback before anything derives from the
	// commit lists (index, stats).
	feedbackApplied := c.applyFeedback(histories)

	// Apply bead filter if specified
	if opts.BeadID != "" {
		filtered := make(map[string]BeadHistory)
		if h, ok := histories[opts.BeadID]; ok {
			filtered[opts.BeadID] = h
		}
		histories = filtered
	}

	// Build commit index
	commitIndex := c.buildCommitIndex(histories)

	// Calculate stats
	stats := c.calculateStats(histories, commits)
	stats.Strategies = art.Strategies
	stats.FeedbackApplied = feedbackApplied

	// Build git range description
	gitRange := c.describeGitRange(opts)

	// Calculate data hash
	dataHash := c.calculateDataHash(beads)

	// Get latest commit SHA for incremental updates
	latestCommitSHA := c.findLatestCommitSHA(events, commits)
	if opts.Revision != "" {
		latestCommitSHA = opts.Revision
	}

	return &HistoryReport{
		GeneratedAt:     time.Now().UTC(),
		DataHash:        dataHash,
		GitRange:        gitRange,
		LatestCommitSHA: latestCommitSHA,
		Window: &HistoryWindow{
			Revision: opts.Revision,
			Limit:    opts.Limit,
			Since:    opts.Since,
			Until:    opts.Until,
			Commits:  art.WalkedCommits,
		},
		Stats:         stats,
		Histories:     histories,
		CommitIndex:   commitIndex,
		CausalHistory: art.CausalHistory,
	}
}

// findLatestCommitSHA finds the most recent commit SHA from events and commits
func (c *Correlator) findLatestCommitSHA(events []BeadEvent, commits []CorrelatedCommit) string {
	var latest time.Time
	var latestSHA string

	// Check events
	for _, e := range events {
		if e.Timestamp.After(latest) {
			latest = e.Timestamp
			latestSHA = e.CommitSHA
		}
	}

	// Check commits
	for _, commit := range commits {
		if commit.Timestamp.After(latest) {
			latest = commit.Timestamp
			latestSHA = commit.SHA
		}
	}

	return latestSHA
}

// BeadInfo is minimal bead information needed for correlation
type BeadInfo struct {
	ID     string
	Title  string
	Status string
}

// buildHistories constructs BeadHistory for each bead
func (c *Correlator) buildHistories(beads []BeadInfo, events []BeadEvent, commits []CorrelatedCommit) map[string]BeadHistory {
	histories := make(map[string]BeadHistory)

	// Initialize histories from bead list
	for _, bead := range beads {
		histories[bead.ID] = BeadHistory{
			BeadID:  bead.ID,
			Title:   bead.Title,
			Status:  bead.Status,
			Events:  []BeadEvent{},
			Commits: []CorrelatedCommit{},
		}
	}

	// Group events by bead ID
	eventsByBead := make(map[string][]BeadEvent)
	for _, event := range events {
		eventsByBead[event.BeadID] = append(eventsByBead[event.BeadID], event)
	}

	// Group commits by bead ID
	commitsByBead := make(map[string][]CorrelatedCommit)
	for _, commit := range commits {
		if commit.BeadID != "" {
			commitsByBead[commit.BeadID] = append(commitsByBead[commit.BeadID], commit)
		}
	}

	// Build complete histories
	for beadID, history := range histories {
		if events, ok := eventsByBead[beadID]; ok {
			history.Events = events
		}
		if commits, ok := commitsByBead[beadID]; ok {
			history.Commits = dedupCommits(commits)
		}

		// Calculate milestones
		history.Milestones = GetBeadMilestones(history.Events)

		// Calculate cycle time
		history.CycleTime = CalculateCycleTime(history.Milestones)

		// Set last author
		if len(history.Commits) > 0 {
			history.LastAuthor = history.Commits[len(history.Commits)-1].Author
		} else if len(history.Events) > 0 {
			history.LastAuthor = history.Events[len(history.Events)-1].Author
		}

		histories[beadID] = history
	}

	return histories
}

// dedupCommits removes duplicate commits by SHA
func dedupCommits(commits []CorrelatedCommit) []CorrelatedCommit {
	seen := make(map[string]bool)
	var result []CorrelatedCommit
	for _, c := range commits {
		if !seen[c.SHA] {
			seen[c.SHA] = true
			result = append(result, c)
		}
	}
	return result
}

// mergeStrategyCommits folds the artifact's explicit-ID commits and temporal
// candidates into the co-commit histories, merging per (commit, bead) so each
// SHA appears once per bead with every method that matched it (Methods), a
// combined confidence (Scorer.CombineConfidence) and a combined reason. Pairs
// naming a bead absent from the current bead set are dropped: the artifact is
// HEAD-only and may carry references to ids that never existed (a commit
// subject mentioning bv-9999). Commits end up in chronological order (ties
// keep strategy precedence: co_committed, explicit_id, temporal_author) and
// LastAuthor is recomputed from that order.
func (c *Correlator) mergeStrategyCommits(histories map[string]BeadHistory, beads []BeadInfo, art *historyArtifact) {
	titles := make(map[string]string, len(beads))
	for _, b := range beads {
		titles[b.ID] = b.Title
	}
	explicitByBead := make(map[string][]CorrelatedCommit)
	explicitBeadsBySHA := make(map[string]map[string]struct{})
	for _, cc := range art.Explicit {
		if _, known := histories[cc.BeadID]; !known {
			continue
		}
		explicitByBead[cc.BeadID] = append(explicitByBead[cc.BeadID], cc)
		if explicitBeadsBySHA[cc.SHA] == nil {
			explicitBeadsBySHA[cc.SHA] = make(map[string]struct{})
		}
		explicitBeadsBySHA[cc.SHA][cc.BeadID] = struct{}{}
	}
	temporalByBead := make(map[string][]CorrelatedCommit)
	for _, cand := range art.Temporal {
		if _, known := histories[cand.BeadID]; !known {
			continue
		}
		// A commit whose message names a DIFFERENT existing bead belongs to
		// that bead: the explicit signal outranks "same author, same window".
		// Naming the same bead merges both signals; naming a bead that does
		// not exist carries no information and does not block the temporal link.
		if beads, ok := explicitBeadsBySHA[cand.SHA]; ok {
			if _, same := beads[cand.BeadID]; !same {
				continue
			}
		}
		temporalByBead[cand.BeadID] = append(temporalByBead[cand.BeadID], finalizeTemporalCandidate(cand, titles[cand.BeadID]))
	}

	for beadID, history := range histories {
		history.Commits = mergeCorrelatedCommits(c.scorer, history.Commits, explicitByBead[beadID], temporalByBead[beadID])
		if len(history.Commits) > 0 {
			history.LastAuthor = history.Commits[len(history.Commits)-1].Author
		} else if len(history.Events) > 0 {
			history.LastAuthor = history.Events[len(history.Events)-1].Author
		}
		histories[beadID] = history
	}
}

// mergeCorrelatedCommits merges per-strategy commit lists (given in strategy
// precedence order) for ONE bead into a single chronological list. A SHA found
// by one strategy keeps that strategy's confidence and reason; a SHA found by
// several gets Methods listing all of them, Method = the highest-confidence
// one, Confidence = Scorer.CombineConfidence over the per-strategy scores,
// Reason = Scorer.CombineReasons, and the union of their files. Within a
// strategy a duplicate SHA keeps its first occurrence (dedupCommits).
func mergeCorrelatedCommits(scorer *Scorer, strategies ...[]CorrelatedCommit) []CorrelatedCommit {
	var order []string
	bySHA := make(map[string][]CorrelatedCommit)
	for _, list := range strategies {
		for _, cc := range dedupCommits(list) {
			if _, ok := bySHA[cc.SHA]; !ok {
				order = append(order, cc.SHA)
			}
			bySHA[cc.SHA] = append(bySHA[cc.SHA], cc)
		}
	}
	if len(order) == 0 {
		return nil
	}

	merged := make([]CorrelatedCommit, 0, len(order))
	for _, sha := range order {
		group := bySHA[sha]
		result := group[0]
		result.Methods = []string{group[0].Method.String()}
		if len(group) > 1 {
			signals := make([]ConfidenceSignal, 0, len(group))
			seenFiles := make(map[string]struct{})
			var files []FileChange
			best := 0
			for i, cc := range group {
				if i > 0 {
					result.Methods = append(result.Methods, cc.Method.String())
				}
				signals = append(signals, ConfidenceSignal{Method: cc.Method, Confidence: cc.Confidence, Reason: cc.Reason})
				if cc.Confidence > group[best].Confidence {
					best = i
				}
				for _, f := range cc.Files {
					if _, dup := seenFiles[f.Path]; dup {
						continue
					}
					seenFiles[f.Path] = struct{}{}
					files = append(files, f)
				}
			}
			result.Method = group[best].Method
			result.Confidence = scorer.CombineConfidence(signals)
			result.Reason = scorer.CombineReasons(signals)
			result.Files = files
		}
		merged = append(merged, result)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})
	return merged
}

// applyFeedback consults the attached FeedbackStore for every (commit, bead)
// pair in histories: a rejection removes the commit from that bead (and hence
// from the commit index and stats built afterwards), a confirmation pins its
// confidence to 1.0 and marks it Confirmed, an ignore leaves it untouched but
// is counted. Returns nil when no store is attached.
func (c *Correlator) applyFeedback(histories map[string]BeadHistory) *FeedbackApplied {
	if c.feedback == nil {
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
			fb, ok := c.feedback.Get(commit.SHA, beadID)
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
				commit.Confirmed = true
				commit.Reason = commit.Reason + "; confirmed by feedback (" + fb.FeedbackBy + ")"
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

// buildCommitIndex creates a reverse lookup from commit SHA to bead IDs
func (c *Correlator) buildCommitIndex(histories map[string]BeadHistory) CommitIndex {
	return BuildCommitIndex(histories)
}

// BuildCommitIndex creates a deterministic reverse lookup from commit SHA to
// bead IDs. A malformed/redundant history must not duplicate a bead in one
// commit's list, and map iteration must not leak into robot JSON arrays.
func BuildCommitIndex(histories map[string]BeadHistory) CommitIndex {
	seen := make(map[string]map[string]struct{})
	for beadID, history := range histories {
		for _, commit := range history.Commits {
			if seen[commit.SHA] == nil {
				seen[commit.SHA] = make(map[string]struct{})
			}
			seen[commit.SHA][beadID] = struct{}{}
		}
	}

	index := make(CommitIndex, len(seen))
	for sha, beadSet := range seen {
		beadIDs := make([]string, 0, len(beadSet))
		for beadID := range beadSet {
			beadIDs = append(beadIDs, beadID)
		}
		sort.Strings(beadIDs)
		index[sha] = beadIDs
	}
	return index
}

// calculateStats computes aggregate statistics
func (c *Correlator) calculateStats(histories map[string]BeadHistory, commits []CorrelatedCommit) HistoryStats {
	stats := HistoryStats{
		TotalBeads:         len(histories),
		MethodDistribution: make(map[string]int),
	}

	// Track unique authors and commits
	authors := make(map[string]bool)
	uniqueCommits := make(map[string]bool)

	// Collect cycle times for average
	var cycleTimes []time.Duration

	for _, history := range histories {
		if len(history.Commits) > 0 {
			stats.BeadsWithCommits++
		}

		for _, commit := range history.Commits {
			uniqueCommits[commit.SHA] = true
			authors[commit.Author] = true
			for _, method := range commit.AllMethods() {
				stats.MethodDistribution[method]++
			}
			if commit.Confirmed {
				stats.MethodDistribution[MethodDistributionConfirmedByFeedback]++
			}
		}

		for _, event := range history.Events {
			authors[event.Author] = true
		}

		// Collect cycle time
		if history.CycleTime != nil && history.CycleTime.ClaimToClose != nil {
			cycleTimes = append(cycleTimes, *history.CycleTime.ClaimToClose)
		}
	}

	stats.TotalCommits = len(uniqueCommits)
	stats.UniqueAuthors = len(authors)

	if stats.BeadsWithCommits > 0 {
		stats.AvgCommitsPerBead = float64(stats.TotalCommits) / float64(stats.BeadsWithCommits)
	}

	// Calculate average cycle time
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

// describeGitRange creates a human-readable description of the git range
func (c *Correlator) describeGitRange(opts CorrelatorOptions) string {
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
		parts = append(parts, fmt.Sprintf("limit %d commits", opts.Limit))
	}

	if len(parts) == 0 {
		return "all history"
	}

	result := ""
	for i, part := range parts {
		if i > 0 {
			result += ", "
		}
		result += part
	}
	return result
}

// calculateDataHash creates a hash of the input beads for consistency checking
func (c *Correlator) calculateDataHash(beads []BeadInfo) string {
	return hashBeads(beads)
}

// ValidateRepository checks if the repository is valid for correlation
func ValidateRepository(repoPath string) error {
	// Check if git directory exists
	gitDir := filepath.Join(repoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return fmt.Errorf("not a git repository: %s", repoPath)
	}

	// Check if any beads file exists (multiple possible names, including SQLite-backed workspaces)
	beadsFiles := []string{
		filepath.Join(repoPath, ".beads", "issues.jsonl"),
		filepath.Join(repoPath, ".beads", "beads.jsonl"),
		filepath.Join(repoPath, ".beads", "beads.base.jsonl"),
		filepath.Join(repoPath, ".beads", "beads.db"),
		filepath.Join(repoPath, ".beads", "metadata.json"),
	}

	found := false
	for _, f := range beadsFiles {
		if _, err := os.Stat(f); err == nil {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("no beads file found in %s/.beads/", repoPath)
	}

	return nil
}
