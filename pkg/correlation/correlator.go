// Package correlation provides the Correlator for building complete bead history reports.
package correlation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	var explicitPath string
	if len(beadsFilePath) > 0 {
		explicitPath = beadsFilePath[0]
	}
	gitRoot := repoPath
	if found, err := FindGitRoot(repoPath); err == nil {
		gitRoot = found
	}
	absRepo := repoPath
	if abs, err := filepath.Abs(repoPath); err == nil {
		absRepo = filepath.Clean(abs)
	}
	if gitRoot != repoPath && filepath.Clean(gitRoot) != absRepo {
		if explicitPath != "" && !filepath.IsAbs(explicitPath) {
			subCandidate := filepath.Join(repoPath, explicitPath)
			rootCandidate := filepath.Join(gitRoot, explicitPath)
			if pathExists(subCandidate) || !pathExists(rootCandidate) {
				explicitPath = subCandidate
			}
		} else if explicitPath == "" {
			if info, err := os.Stat(filepath.Join(repoPath, ".beads")); err == nil && info.IsDir() {
				picked := pickBeadsFiles(repoPath, defaultBeadsFiles)
				if len(picked) > 0 {
					explicitPath = filepath.Join(repoPath, filepath.FromSlash(picked[0]))
				}
			}
		}
	}
	var extractor *Extractor
	if explicitPath != "" {
		extractor = NewExtractor(gitRoot, explicitPath)
	} else {
		extractor = NewExtractor(gitRoot)
	}
	storeDirRel := extractor.effectiveStoreDirRel()
	return &Correlator{
		repoPath:    gitRoot,
		extractor:   extractor,
		coCommitter: NewCoCommitExtractor(gitRoot).WithStoreDir(storeDirRel),
		explicit:    NewExplicitMatcher(gitRoot),
		scorer:      NewScorer(),
	}
}

func (c *Correlator) storeDirRel() string {
	if c == nil || c.extractor == nil {
		return ""
	}
	return c.extractor.effectiveStoreDirRel()
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
	Revision               string            // Resolved commit to walk from (empty = HEAD)
	BeadID                 string            // Filter to single bead ID (empty = all)
	Since                  *time.Time        // Only events after this time
	Until                  *time.Time        // Only events before this time
	Limit                  int               // Max commits to process (0 = no limit)
	TargetCommitSHA        string            // Optional single commit SHA to correlate against full .beads event history
	CausalityBeadID        string            // Capture full committed dependency authority for this target only
	TargetBeadAliases      []string          // Optional alternate local IDs for BeadID/CausalityBeadID (e.g. both "99" and "api-99" for deleted workspace targets)
	ExplicitIDAliases      map[string]string // Optional commit-message token -> canonical local BeadID (e.g. "api-1" -> "1", "api-bd-1" -> "bd-1")
	AmbiguousUnprefixedIDs map[string]bool   // Optional unprefixed local IDs that collide across sibling stores sharing .git
	StoreSubtreePrefix     string            // Optional slash-normalized subtree prefix for this store (e.g. "services/api/")
	SiblingSubtreePrefixes []string          // Optional slash-normalized subtree prefixes of sibling stores when StoreSubtreePrefix == ""
}

// historyArtifactFormatVersion is the schema version of historyArtifact. The
// persistent HEAD-artifact cache stores it with every entry and treats a
// mismatch as a miss (see getHeadArtifactCached), so a binary with a newer
// artifact shape lazily rebuilds instead of reading fields that were never
// written. History: v1 = events + co-commit commits only; v2 = adds
// per-method explicit-ID commits, temporal candidates, the walked window and
// per-strategy timings; v3 = owned before/after transitions and optional full
// historical constraint evidence for one causal-analysis target; v4 = Aho-Corasick
// known-ID matching, synthesized creation milestones, mixed .beads/+code
// temporal candidates, and expanded code/explicit file retention.
const historyArtifactFormatVersion = 4

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
// (git history walk + the three correlation strategies).
func (c *Correlator) extractHistoryArtifact(opts CorrelatorOptions) (*historyArtifact, error) {
	return c.extractHistoryArtifactForBeads(nil, opts)
}

func (c *Correlator) extractHistoryArtifactForBeads(beads []BeadInfo, opts CorrelatorOptions) (*historyArtifact, error) {
	art, _, err := c.extractHistoryArtifactWithWalkForBeads(beads, opts)
	return art, err
}

func (c *Correlator) extractHistoryArtifactWithWalk(opts CorrelatorOptions) (*historyArtifact, []walkedCommit, error) {
	return c.extractHistoryArtifactWithWalkForBeads(nil, opts)
}

func (c *Correlator) extractHistoryArtifactWithWalkForBeads(beads []BeadInfo, opts CorrelatorOptions) (*historyArtifact, []walkedCommit, error) {
	if opts.Revision != "" {
		if err := validateGitRefArg(opts.Revision); err != nil {
			return nil, nil, err
		}
	}
	targetSHA := strings.ToLower(strings.TrimSpace(opts.TargetCommitSHA))
	if targetSHA != "" {
		if err := validateGitRefArg(targetSHA); err != nil {
			return nil, nil, err
		}
	}
	extractOpts := ExtractOptions{
		Revision:      opts.Revision,
		Since:         opts.Since,
		Until:         opts.Until,
		Limit:         opts.Limit,
		BeadID:        opts.BeadID,
		BeadIDAliases: opts.TargetBeadAliases,
	}
	if targetSHA != "" {
		// Keep full surrounding .beads lifecycle transitions (EventClaimed..EventClosed)
		// intact while gating the target commit itself against Since/Until below.
		extractOpts.Since = nil
	}
	art := &historyArtifact{FormatVersion: historyArtifactFormatVersion}
	if opts.CausalityBeadID != "" {
		var err error
		art.CausalHistory, err = c.extractor.extractCausalHistory(opts.CausalityBeadID, extractOpts)
		if err != nil {
			return nil, nil, fmt.Errorf("extracting causal history: %w", err)
		}
	}

	// Strategy 1: co_committed — lifecycle events from the beads file history
	// plus the code files changed in the same commits.
	start := time.Now()
	events, err := c.extractor.Extract(extractOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting events: %w", err)
	}
	coCommitEvents := events
	if targetSHA != "" {
		coCommitEvents = make([]BeadEvent, 0, 4)
		for _, ev := range events {
			if !strings.EqualFold(ev.CommitSHA, targetSHA) {
				continue
			}
			if opts.Since != nil && ev.Timestamp.Before(*opts.Since) {
				continue
			}
			if opts.Until != nil && ev.Timestamp.After(*opts.Until) {
				continue
			}
			coCommitEvents = append(coCommitEvents, ev)
		}
	}
	commits, err := c.coCommitter.ExtractAllCoCommits(coCommitEvents)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting co-commits: %w", err)
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
		return nil, nil, err
	}
	var walk []walkedCommit
	if targetSHA != "" {
		walk, err = walkCommitsBySHAs(c.ctx, c.repoPath, []string{targetSHA})
		if err == nil && (opts.Since != nil || opts.Until != nil) {
			filteredWalk := walk[:0]
			for _, wc := range walk {
				if opts.Since != nil && wc.Timestamp.Before(*opts.Since) {
					continue
				}
				if opts.Until != nil && wc.Timestamp.After(*opts.Until) {
					continue
				}
				filteredWalk = append(filteredWalk, wc)
			}
			walk = filteredWalk
		}
	} else {
		walk, err = walkCommits(c.ctx, c.repoPath, extractOpts)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("walking commits: %w", err)
	}
	art.WalkedCommits = len(walk)

	// Strategy 2: explicit_id — bead ids referenced in commit messages.
	// Combine caller-configured IDs on c.explicit, report beads/targets,
	// ExplicitIDAliases, and historical JSONL event IDs without mutating c.explicit.
	start = time.Now()
	matcher := c.explicit.withReportOptions(opts, knownBeadIDsForReport(beads, opts), beadIDsFromEvents(events))
	explicit, err := c.extractExplicitCommitsWithOptions(matcher, walk, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting explicit-id commits: %w", err)
	}
	art.Explicit = explicit
	art.Strategies = append(art.Strategies, StrategyRun{
		Name: MethodExplicitID.String(), Ran: true,
		DurationMS: durationMS(time.Since(start)), Candidates: len(explicit),
	})

	// Strategy 3: temporal_author — the claimant's code commits inside the
	// bead's active window. Precedence against explicit-ID links is resolved
	// at assembly (mergeStrategyCommits), once it is known which referenced
	// beads actually exist; pure tracker-only commits are excluded here while
	// mixed code + tracker commits are admitted.
	start = time.Now()
	temporal, err := c.extractTemporalCandidatesWithOptions(events, walk, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("extracting temporal candidates: %w", err)
	}
	art.Temporal = temporal
	art.Strategies = append(art.Strategies, StrategyRun{
		Name: MethodTemporalAuthor.String(), Ran: true,
		DurationMS: durationMS(time.Since(start)), Candidates: len(temporal),
	})

	return art, walk, nil
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
// commit's repository files (served from the co-commit extractor's batched cache).
// Pure .beads/-only bookkeeping commits and empty commits are skipped, while any
// explicit-ID commit touching at least one non-.beads/ repository file (including
// docs, config, or build files) is retained.
func (c *Correlator) extractExplicitCommits(walk []walkedCommit, beadFilter string) ([]CorrelatedCommit, error) {
	return c.extractExplicitCommitsWithMatcher(c.explicit, walk, beadFilter)
}

func (c *Correlator) extractExplicitCommitsWithMatcher(matcher *ExplicitMatcher, walk []walkedCommit, beadFilter string) ([]CorrelatedCommit, error) {
	return c.extractExplicitCommitsWithOptions(matcher, walk, CorrelatorOptions{BeadID: beadFilter})
}

func (c *Correlator) extractExplicitCommitsWithOptions(matcher *ExplicitMatcher, walk []walkedCommit, opts CorrelatorOptions) ([]CorrelatedCommit, error) {
	if matcher == nil {
		matcher = c.explicit
	}
	matches := matcher.MatchWalkedCommitsFiltered(walk, opts.BeadID, opts.TargetBeadAliases)
	if len(matches) == 0 {
		return nil, nil
	}
	storeDirRel := c.storeDirRel()
	normStorePrefix := normalizeSubtreePrefix(opts.StoreSubtreePrefix)
	normSiblingPrefixes := normalizeSubtreePrefixes(opts.SiblingSubtreePrefixes)

	walkBySHA := make(map[string]walkedCommit, len(walk))
	for _, wc := range walk {
		walkBySHA[wc.SHA] = wc
	}
	filteredMatches := make([]ExplicitMatch, 0, len(matches))
	shas := make([]string, 0, len(matches))
	queued := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		wc, ok := walkBySHA[m.CommitSHA]
		if ok && (len(wc.Files) == 0 || wc.trackerOnly(storeDirRel)) {
			continue
		}
		if ok && !m.qualifiedAlias && isAmbiguousUnprefixedID(opts.AmbiguousUnprefixedIDs, m.BeadID) {
			if !commitMatchesStoreSubtree(wc.Files, storeDirRel, normStorePrefix, normSiblingPrefixes) {
				continue
			}
		}
		filteredMatches = append(filteredMatches, m)
		if _, dup := queued[m.CommitSHA]; !dup {
			queued[m.CommitSHA] = struct{}{}
			shas = append(shas, m.CommitSHA)
		}
	}
	if len(filteredMatches) == 0 {
		return nil, nil
	}
	if err := c.coCommitter.primeBatch(shas); err != nil {
		return nil, err
	}
	commits := make([]CorrelatedCommit, 0, len(filteredMatches))
	for _, m := range filteredMatches {
		cc := matcher.CreateCorrelatedCommit(m, c.coCommitter)
		if len(cc.Files) == 0 {
			if wc, ok := walkBySHA[m.CommitSHA]; ok {
				for _, f := range wc.Files {
					if isTrackerPath(f, storeDirRel) || isExcludedPath(f) {
						continue
					}
					cc.Files = append(cc.Files, FileChange{Path: f, Action: "M"})
				}
			}
		}
		if len(cc.Files) == 0 {
			continue
		}
		commits = append(commits, cc)
	}
	return commits, nil
}

func isAmbiguousUnprefixedID(ambiguous map[string]bool, id string) bool {
	if len(ambiguous) == 0 || id == "" {
		return false
	}
	if ambiguous[id] {
		return true
	}
	return ambiguous[strings.ToLower(id)]
}

func normalizeSubtreePrefix(prefix string) string {
	clean := normalizeRepoSlashPath(prefix)
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return ""
	}
	return clean + "/"
}

func normalizeSubtreePrefixes(prefixes []string) []string {
	if len(prefixes) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(prefixes))
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		norm := normalizeSubtreePrefix(p)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func commitMatchesStoreSubtree(files []string, storeDirRel, normStorePrefix string, normSiblingPrefixes []string) bool {
	if normStorePrefix == "" && len(normSiblingPrefixes) == 0 {
		return true
	}
	var nonTracker []string
	for _, f := range files {
		if isTrackerPath(f, storeDirRel) {
			continue
		}
		clean := normalizeRepoSlashPath(f)
		if clean == "" || clean == "." {
			continue
		}
		nonTracker = append(nonTracker, clean)
	}
	if len(nonTracker) == 0 {
		return false
	}
	if normStorePrefix != "" {
		for _, nf := range nonTracker {
			if strings.HasPrefix(nf, normStorePrefix) {
				return true
			}
		}
		return false
	}
	for _, nf := range nonTracker {
		inSibling := false
		for _, sib := range normSiblingPrefixes {
			if strings.HasPrefix(nf, sib) {
				inSibling = true
				break
			}
		}
		if !inSibling {
			return true
		}
	}
	return false
}

// extractTemporalCandidates finds, for every bead with a claimed→closed
// window, the walked commits by the claimant inside that window that (a) are
// not pure tracker-only bookkeeping commits and (b) changed at least one code file.
func (c *Correlator) extractTemporalCandidates(events []BeadEvent, walk []walkedCommit, beadFilter string) ([]temporalCandidate, error) {
	return c.extractTemporalCandidatesWithOptions(events, walk, CorrelatorOptions{BeadID: beadFilter})
}

func (c *Correlator) extractTemporalCandidatesWithOptions(events []BeadEvent, walk []walkedCommit, opts CorrelatorOptions) ([]temporalCandidate, error) {
	windows := temporalWindowsFromEvents(events, opts.BeadID, opts.TargetBeadAliases...)
	if len(windows) == 0 {
		return nil, nil
	}
	storeDirRel := c.storeDirRel()

	type pair struct {
		window TemporalWindow
		commit walkedCommit
	}
	var pairs []pair
	var shas []string
	queued := make(map[string]struct{})
	for _, w := range windows {
		for _, wc := range walk {
			if len(wc.Files) == 0 || wc.trackerOnly(storeDirRel) || !commitInWindow(w, wc) {
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
	art, err := c.extractHistoryArtifactForBeads(beads, opts)
	if err != nil {
		return nil, err
	}
	return c.assembleReport(beads, opts, art), nil
}

func knownBeadIDsForReport(beads []BeadInfo, opts CorrelatorOptions) []string {
	ids := beadIDsFromInfos(beads)
	if opts.BeadID != "" {
		ids = append(ids, opts.BeadID)
	}
	if opts.CausalityBeadID != "" && opts.CausalityBeadID != opts.BeadID {
		ids = append(ids, opts.CausalityBeadID)
	}
	for _, alias := range opts.TargetBeadAliases {
		if alias != "" {
			ids = append(ids, alias)
		}
	}
	return ids
}

func beadIDsFromInfos(beads []BeadInfo) []string {
	ids := make([]string, 0, len(beads))
	for _, b := range beads {
		if b.ID != "" {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

func beadIDsFromEvents(events []BeadEvent) []string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.BeadID != "" {
			ids = append(ids, ev.BeadID)
		}
	}
	return ids
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

	// Build bead histories from the co-commit strategy, synthesize targeted
	// deleted/tombstoned beads that have real git evidence, then merge the
	// explicit-ID and temporal strategies per (commit, bead).
	histories := c.buildHistories(beads, events, commits)
	c.synthesizeTargetHistories(histories, opts, art)
	c.mergeStrategyCommitsWithOptions(histories, beads, opts, art)

	// Honor stored confirm/reject feedback before anything derives from the
	// commit lists (index, stats).
	feedbackApplied := c.applyFeedback(histories)

	// Apply bead filter if specified (retaining opts.BeadID and any matching TargetBeadAliases)
	if opts.BeadID != "" {
		filtered := make(map[string]BeadHistory)
		for _, id := range reportFilterTargetIDs(opts.BeadID, opts.TargetBeadAliases) {
			if h, ok := histories[id]; ok {
				filtered[id] = h
			}
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
	result := make([]CorrelatedCommit, 0, len(commits))
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
func reportSynthesisTargets(opts CorrelatorOptions) []string {
	if opts.BeadID == "" && opts.CausalityBeadID == "" {
		return nil
	}
	seen := make(map[string]struct{}, 2+len(opts.TargetBeadAliases))
	var targets []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}
	add(opts.BeadID)
	add(opts.CausalityBeadID)
	for _, alias := range opts.TargetBeadAliases {
		add(alias)
	}
	return targets
}

func reportFilterTargetIDs(primary string, aliases []string) []string {
	if primary == "" {
		return nil
	}
	seen := make(map[string]struct{}, 1+len(aliases))
	out := make([]string, 0, 1+len(aliases))
	out = append(out, primary)
	seen[primary] = struct{}{}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func resolveUniqueTargetGroupID(histories map[string]BeadHistory, opts CorrelatorOptions) (map[string]struct{}, string) {
	targets := reportSynthesisTargets(opts)
	if len(targets) <= 1 {
		return nil, ""
	}
	group := make(map[string]struct{}, len(targets))
	existing := ""
	existingCount := 0
	for _, id := range targets {
		group[id] = struct{}{}
		if _, ok := histories[id]; ok {
			existing = id
			existingCount++
		}
	}
	if existingCount != 1 {
		return group, ""
	}
	return group, existing
}

// synthesizeTargetHistories synthesizes BeadHistory entries for explicitly
// requested targets (opts.BeadID, opts.CausalityBeadID, and opts.TargetBeadAliases)
// that are absent from live beads (e.g. tombstoned or deleted in git) when
// tracker-derived git evidence exists. Unfiltered reports and nonexistent target
// IDs with zero tracker evidence are never synthesized.
func (c *Correlator) synthesizeTargetHistories(histories map[string]BeadHistory, opts CorrelatorOptions, art *historyArtifact) {
	if art == nil {
		return
	}
	targets := reportSynthesisTargets(opts)
	if len(targets) == 0 {
		return
	}

	for _, targetID := range targets {
		if _, exists := histories[targetID]; exists {
			continue
		}

		eventsForTarget := []BeadEvent{}
		for _, ev := range art.Events {
			if ev.BeadID == targetID {
				eventsForTarget = append(eventsForTarget, ev)
			}
		}

		coCommitsForTarget := []CorrelatedCommit{}
		for _, cc := range art.Commits {
			if cc.BeadID == targetID {
				coCommitsForTarget = append(coCommitsForTarget, cc)
			}
		}

		hasCausalObs := false
		if art.CausalHistory != nil && art.CausalHistory.BeadID == targetID {
			for _, obs := range art.CausalHistory.Observations {
				if (obs.Before.Known && obs.Before.Issue != nil) || (obs.After.Known && obs.After.Issue != nil) {
					hasCausalObs = true
					break
				}
			}
		}

		// Require tracker-derived git evidence (.beads/ events, co-commits, or causal snapshots)
		// so a nonexistent target ID that merely matches a word in a commit message never
		// fabricates a synthetic BeadHistory; explicit commits still attach in mergeStrategyCommits.
		if len(eventsForTarget) == 0 && len(coCommitsForTarget) == 0 && !hasCausalObs {
			continue
		}

		title := ""
		lastAfterStatus := ""
		for _, ev := range eventsForTarget {
			if ev.Before != nil && ev.Before.Title != "" {
				title = ev.Before.Title
			}
			if ev.After != nil {
				if ev.After.Title != "" {
					title = ev.After.Title
				}
				if ev.After.Status != "" {
					lastAfterStatus = ev.After.Status
				}
			}
		}
		if title == "" && art.CausalHistory != nil && art.CausalHistory.BeadID == targetID {
			for _, obs := range art.CausalHistory.Observations {
				if obs.Before.Issue != nil && obs.Before.Issue.Title != "" {
					title = obs.Before.Issue.Title
				}
				if obs.After.Issue != nil && obs.After.Issue.Title != "" {
					title = obs.After.Issue.Title
				}
			}
		}
		if title == "" {
			title = targetID
		}

		status := "deleted"
		if len(eventsForTarget) > 0 {
			lastEv := eventsForTarget[len(eventsForTarget)-1]
			switch {
			case lastEv.EventType == EventDeleted:
				status = "deleted"
			case lastEv.After != nil && lastEv.After.Status == "tombstone":
				status = "tombstone"
			case lastAfterStatus != "":
				status = lastAfterStatus
			}
		} else if art.CausalHistory != nil && art.CausalHistory.BeadID == targetID && len(art.CausalHistory.Observations) > 0 {
			lastObs := art.CausalHistory.Observations[len(art.CausalHistory.Observations)-1]
			if lastObs.After.Known && lastObs.After.Issue != nil && lastObs.After.Issue.Status != "" {
				status = lastObs.After.Issue.Status
			}
		}

		deduped := dedupCommits(coCommitsForTarget)
		milestones := GetBeadMilestones(eventsForTarget)
		history := BeadHistory{
			BeadID:     targetID,
			Title:      title,
			Status:     status,
			Events:     eventsForTarget,
			Commits:    deduped,
			Milestones: milestones,
			CycleTime:  CalculateCycleTime(milestones),
		}
		if len(history.Commits) > 0 {
			history.LastAuthor = history.Commits[len(history.Commits)-1].Author
		} else if len(history.Events) > 0 {
			history.LastAuthor = history.Events[len(history.Events)-1].Author
		}
		histories[targetID] = history
	}
}

func (c *Correlator) mergeStrategyCommits(histories map[string]BeadHistory, beads []BeadInfo, art *historyArtifact) {
	c.mergeStrategyCommitsWithOptions(histories, beads, CorrelatorOptions{}, art)
}

func (c *Correlator) mergeStrategyCommitsWithOptions(histories map[string]BeadHistory, beads []BeadInfo, opts CorrelatorOptions, art *historyArtifact) {
	titles := make(map[string]string, len(histories)+len(beads))
	for id, h := range histories {
		titles[id] = h.Title
	}
	for _, b := range beads {
		titles[b.ID] = b.Title
	}
	targetGroup, uniqueTarget := resolveUniqueTargetGroupID(histories, opts)
	explicitByBead := make(map[string][]CorrelatedCommit)
	explicitBeadsBySHA := make(map[string]map[string]struct{})
	for _, cc := range art.Explicit {
		beadID := cc.BeadID
		if _, known := histories[beadID]; !known {
			if uniqueTarget != "" {
				if _, inGroup := targetGroup[beadID]; inGroup {
					beadID = uniqueTarget
					cc.BeadID = uniqueTarget
				}
			}
			if _, knownAfter := histories[beadID]; !knownAfter {
				continue
			}
		}
		explicitByBead[beadID] = append(explicitByBead[beadID], cc)
		if explicitBeadsBySHA[cc.SHA] == nil {
			explicitBeadsBySHA[cc.SHA] = make(map[string]struct{})
		}
		explicitBeadsBySHA[cc.SHA][beadID] = struct{}{}
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
		return []CorrelatedCommit{}
	}

	merged := make([]CorrelatedCommit, 0, len(order))
	for _, sha := range order {
		group := bySHA[sha]
		result := group[0]
		if group[0].Method != "" {
			result.Methods = []string{group[0].Method.String()}
		}
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

// FindGitRoot walks upward from startPath (starting at filepath.Dir if startPath
// is an existing regular file) until it finds a directory containing ".git" (a
// normal .git directory or a worktree/submodule .git file).
func FindGitRoot(startPath string) (string, error) {
	if strings.TrimSpace(startPath) == "" {
		return "", fmt.Errorf("not a git repository: empty path")
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", fmt.Errorf("resolving path %s: %w", startPath, err)
	}
	dir := filepath.Clean(abs)
	if info, statErr := os.Stat(dir); statErr == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	} else if statErr != nil && os.IsNotExist(statErr) {
		return "", fmt.Errorf("not a git repository: %s", startPath)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Clean(dir), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a git repository: %s", startPath)
		}
		dir = parent
	}
}

// ValidateRepositoryWithBeadsDir checks that repoPath lies inside a git
// repository (walking upward via FindGitRoot) and that beadsDir (or
// filepath.Join(repoPath, ".beads") when beadsDir == "") contains at least one
// recognized Beads store file.
func ValidateRepositoryWithBeadsDir(repoPath, beadsDir string) error {
	if _, err := FindGitRoot(repoPath); err != nil {
		return fmt.Errorf("not a git repository: %s", repoPath)
	}

	targetBeadsDir := beadsDir
	if targetBeadsDir == "" {
		targetBeadsDir = filepath.Join(repoPath, ".beads")
	}

	beadsFiles := []string{
		filepath.Join(targetBeadsDir, "issues.jsonl"),
		filepath.Join(targetBeadsDir, "beads.jsonl"),
		filepath.Join(targetBeadsDir, "beads.base.jsonl"),
		filepath.Join(targetBeadsDir, "beads.db"),
		filepath.Join(targetBeadsDir, "metadata.json"),
	}

	for _, f := range beadsFiles {
		if _, err := os.Stat(f); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no beads file found in %s", targetBeadsDir)
}

// ValidateRepository checks if the repository is valid for correlation
func ValidateRepository(repoPath string) error {
	return ValidateRepositoryWithBeadsDir(repoPath, "")
}
