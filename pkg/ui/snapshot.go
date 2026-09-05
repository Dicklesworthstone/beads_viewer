// Package ui provides the terminal user interface for beads_viewer.
// This file implements the DataSnapshot type for thread-safe UI rendering.
package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
	"github.com/charmbracelet/bubbles/list"
)

type datasetTier int

const (
	datasetTierUnknown datasetTier = iota
	datasetTierSmall
	datasetTierMedium
	datasetTierLarge
	datasetTierHuge
)

func datasetTierForIssueCount(total int) datasetTier {
	switch {
	case total <= 0:
		return datasetTierUnknown
	case total < 1000:
		return datasetTierSmall
	case total < 5000:
		return datasetTierMedium
	case total < 20000:
		return datasetTierLarge
	default:
		return datasetTierHuge
	}
}

func (t datasetTier) String() string {
	switch t {
	case datasetTierSmall:
		return "small"
	case datasetTierMedium:
		return "medium"
	case datasetTierLarge:
		return "large"
	case datasetTierHuge:
		return "huge"
	default:
		return "unknown"
	}
}

func isClosedLikeStatus(status model.Status) bool {
	return status == model.StatusClosed || status == model.StatusTombstone
}

type snapshotBuildConfig struct {
	PrecomputeTriage      bool
	PrecomputeTree        bool
	PrecomputeBoard       bool
	PrecomputeGraphLayout bool
	PrecomputeInsights    bool
	SkipPhase2            bool
}

func snapshotBuildConfigDefault() snapshotBuildConfig {
	return snapshotBuildConfig{
		PrecomputeTriage:      true,
		PrecomputeTree:        true,
		PrecomputeBoard:       true,
		PrecomputeGraphLayout: true,
		PrecomputeInsights:    true,
		SkipPhase2:            false,
	}
}

func snapshotBuildConfigForTier(tier datasetTier) snapshotBuildConfig {
	cfg := snapshotBuildConfigDefault()
	switch tier {
	case datasetTierLarge:
		cfg.PrecomputeTriage = false
		cfg.PrecomputeTree = false
		// The UI needs these structures to install a snapshot. Preparing them
		// here keeps their O(N) construction off the event loop at 5k–20k rows.
		cfg.PrecomputeInsights = false
	case datasetTierHuge:
		cfg.PrecomputeTriage = false
		cfg.PrecomputeTree = false
		cfg.PrecomputeBoard = false
		cfg.PrecomputeGraphLayout = false
		cfg.PrecomputeInsights = false
		cfg.SkipPhase2 = true
	}
	return cfg
}

func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%dm", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

const incrementalListMaxChangeRatio = 0.2

// IssueDiffStats summarizes the change volume between snapshots.
type IssueDiffStats struct {
	Changed int
	Total   int
	Ratio   float64
}

// pooledIssueLease gives Phase 1 and Phase 2 snapshots shared, one-shot
// ownership of pooled parser structs. A Phase 2 snapshot is a distinct object,
// so copying the raw pointer slice would otherwise let shutdown return the same
// objects to sync.Pool twice.
type pooledIssueLease struct {
	once     sync.Once
	released atomic.Bool
	refs     []*model.Issue
	release  func([]*model.Issue)
}

func newPooledIssueLease(refs []*model.Issue) *pooledIssueLease {
	if len(refs) == 0 {
		return nil
	}
	return &pooledIssueLease{
		refs:    refs,
		release: loader.ReturnIssuePtrsToPool,
	}
}

func (l *pooledIssueLease) releaseOnce() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		refs := l.refs
		l.refs = nil
		if len(refs) > 0 && l.release != nil {
			l.release(refs)
		}
		l.released.Store(true)
	})
}

func (l *pooledIssueLease) active() bool {
	return l != nil && !l.released.Load()
}

// DataSnapshot is an immutable, self-contained representation of all data
// the UI needs to render. Once created, it never changes - this is critical
// for thread safety when the background worker is building the next snapshot.
//
// The UI thread reads exclusively from its current snapshot pointer.
// When a new snapshot is ready, the UI swaps the pointer atomically.
type DataSnapshot struct {
	// Core data
	Issues   []model.Issue           // All issues (sorted)
	IssueMap map[string]*model.Issue // Lookup by ID
	// pooledIssues is shared by Phase 1/Phase 2 snapshots so parser refs are
	// returned exactly once even though both snapshot objects can outlive a swap.
	pooledIssues *pooledIssueLease
	// ViewIssues are the issues included in the current view context (e.g. recipe).
	// When empty, callers should fall back to Issues.
	ViewIssues []model.Issue

	// Graph analysis
	Analyzer *analysis.Analyzer
	Analysis *analysis.GraphStats
	insights analysis.Insights // unexported for immutability; use GetInsights()

	// Computed statistics
	CountOpen    int
	CountReady   int
	CountBlocked int
	CountClosed  int

	// Pre-computed UI data. The unexported adapter/cache fields let the UI install
	// the common unfiltered view without rebuilding interface slices, search
	// documents, alert state, or selection indexes on the event loop.
	ListItems      []IssueItem // Pre-built list items with scores
	listModelItems []list.Item
	listIndexByID  map[string]int
	listOrderHash  uint64
	semanticIDs    []string
	semanticDocs   map[string]string
	alerts         []drift.Alert
	alertsCritical int
	alertsWarning  int
	alertsInfo     int
	phase2Triage   *analysis.TriageResult
	// Detached mutable inputs are prepared before publication, so starting a
	// Phase2 command does not deep-copy every view on the event loop.
	phase2Input   *DataSnapshot
	TriageScores  map[string]float64
	TriageReasons map[string]analysis.TriageReasons
	QuickWinSet   map[string]bool
	BlockerSet    map[string]bool
	UnblocksMap   map[string][]string
	// TreeRoots and TreeNodeMap contain a pre-built parent/child tree for the Tree view.
	// These are computed off-thread by SnapshotBuilder to avoid UI-thread work when
	// entering the tree view for large datasets.
	TreeRoots   []*IssueTreeNode
	TreeNodeMap map[string]*IssueTreeNode
	// BoardState contains pre-built Kanban board columns for each swimlane mode (bv-guxz).
	BoardState *BoardState
	// graphLayout contains pre-built graph view data (blockers/dependents, sorted IDs, ranks)
	// to avoid rebuilding graph structures on the UI thread (bv-za8z).
	// Unexported for immutability; use GetGraphLayout().
	graphLayout *GraphLayout

	// Metadata
	CreatedAt     time.Time // When this snapshot was built
	DataHash      string    // Hash of source data for cache validation
	AuthorityHash string    // Full source identity, including hidden dependency records.
	RecipeName    string    // Active recipe name for this snapshot (bv-2h40)
	RecipeHash    string    // Fingerprint of active recipe for this snapshot (bv-4ilb)
	// DatasetTier is a tiered performance mode for large datasets (bv-9thm).
	// When unknown, normal behavior applies.
	DatasetTier datasetTier
	// SourceIssueCountHint is an approximate total issue count from the source file
	// (e.g., JSONL line count). This may be 0 if unavailable.
	SourceIssueCountHint int
	// LoadedOpenOnly indicates the snapshot intentionally excluded closed/tombstone
	// issues for performance (huge tier).
	LoadedOpenOnly bool
	// TruncatedCount is an approximate count of issues excluded by load policy.
	// This may include invalid/empty lines when computed from a line count hint.
	TruncatedCount int
	// LargeDatasetWarning is a short, user-facing warning to show in the footer.
	LargeDatasetWarning string
	// LoadWarningCount is the number of non-fatal parse warnings encountered while loading.
	// In TUI mode, warnings must not be printed to stderr during render.
	LoadWarningCount int

	// Phase 2 analysis status
	// phase2Ready is true when expensive metrics (PageRank, Betweenness, etc.) are computed.
	// UI can render immediately with Phase 1 data, then refresh when Phase 2 completes.
	// Unexported for immutability; use IsPhase2Ready().
	phase2Ready bool

	// Incremental update metadata (bv-5mzz).
	IssueDiff      *analysis.IssueDiff
	IssueDiffStats IssueDiffStats
	// IncrementalListUsed reports whether list items were rebuilt incrementally.
	IncrementalListUsed bool

	// Error state (for graceful degradation)
	LoadError    error     // Non-nil if last load had recoverable errors
	ErrorTime    time.Time // When error occurred
	StaleWarning bool      // True if data is from previous successful load
}

func (s *DataSnapshot) attachPooledIssues(refs []*model.Issue) {
	if s == nil {
		return
	}
	s.pooledIssues = newPooledIssueLease(refs)
}

func (s *DataSnapshot) releasePooledIssues() {
	if s == nil {
		return
	}
	s.pooledIssues.releaseOnce()
}

func (s *DataSnapshot) hasPooledIssues() bool {
	return s != nil && s.pooledIssues.active()
}

// IsPhase2Ready returns whether expensive Phase 2 metrics are computed.
func (s *DataSnapshot) IsPhase2Ready() bool {
	if s == nil {
		return false
	}
	return s.phase2Ready
}

// GetInsights returns the precomputed insights for this snapshot.
func (s *DataSnapshot) GetInsights() analysis.Insights {
	if s == nil {
		return analysis.Insights{}
	}
	return s.insights
}

// GetGraphLayout returns the precomputed graph layout for this snapshot.
func (s *DataSnapshot) GetGraphLayout() *GraphLayout {
	if s == nil {
		return nil
	}
	return s.graphLayout
}

// GraphLayout contains precomputed data used by the graph view.
// This intentionally focuses on the current ASCII graph view needs (relationships + ranks),
// not geometric coordinates.
type GraphLayout struct {
	// Relationships (blocks/dependents)
	Blockers   map[string][]string // What each issue depends on (blocks this issue)
	Dependents map[string][]string // What depends on each issue (this issue blocks)

	// Navigation order (all IDs in the snapshot)
	SortedIDs []string

	// Metric ranks (1 = best, higher = worse). Missing ranks imply "unknown".
	RankPageRank     map[string]int
	RankBetweenness  map[string]int
	RankEigenvector  map[string]int
	RankHubs         map[string]int
	RankAuthorities  map[string]int
	RankCriticalPath map[string]int
	RankInDegree     map[string]int
	RankOutDegree    map[string]int
}

// BoardState contains precomputed Kanban columns for each swimlane mode.
// This lets the UI swap board data in O(1) when the full dataset is shown.
type BoardState struct {
	ByStatus   [4][]model.Issue
	ByPriority [4][]model.Issue
	ByType     [4][]model.Issue
}

func (s *BoardState) ColumnsForMode(mode SwimLaneMode) [4][]model.Issue {
	if s == nil {
		return [4][]model.Issue{}
	}
	switch mode {
	case SwimByPriority:
		return s.ByPriority
	case SwimByType:
		return s.ByType
	default:
		return s.ByStatus
	}
}

func (l *GraphLayout) UpdatePhase2Ranks(stats *analysis.GraphStats) {
	if l == nil || stats == nil {
		return
	}

	// Phase 2 ranks may become available later (AnalyzeAsync).
	l.RankPageRank = stats.PageRankRank()
	l.RankBetweenness = stats.BetweennessRank()
	l.RankEigenvector = stats.EigenvectorRank()
	l.RankHubs = stats.HubsRank()
	l.RankAuthorities = stats.AuthoritiesRank()
	l.RankCriticalPath = stats.CriticalPathRank()

	// Rebuild SortedIDs using the new critical-path ranking, preserving determinism.
	l.SortedIDs = orderIssueIDsByRank(l.SortedIDs, l.RankCriticalPath)
}

// SnapshotBuilder constructs DataSnapshots from raw data.
// This is used by the BackgroundWorker to build new snapshots.
type SnapshotBuilder struct {
	issues   []model.Issue
	analyzer *analysis.Analyzer
	analysis *analysis.GraphStats
	recipe   *recipe.Recipe
	cfg      snapshotBuildConfig

	prevSnapshot *DataSnapshot
	diff         *analysis.IssueDiff
	diffStats    IssueDiffStats
}

// NewSnapshotBuilder creates a builder for constructing a DataSnapshot.
func NewSnapshotBuilder(issues []model.Issue, authority ...*model.ReadinessIndex) *SnapshotBuilder {
	var readiness *model.ReadinessIndex
	if len(authority) > 0 {
		readiness = authority[0]
	}
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}
	issues = issuesWithoutTombstones(issues)
	analyzer := analysis.NewAnalyzer(issues)
	analyzer.SetReadinessScope(readiness, nil)
	return &SnapshotBuilder{
		issues:   issues,
		analyzer: analyzer,
		cfg:      snapshotBuildConfigDefault(),
	}
}

func issuesWithoutTombstones(issues []model.Issue) []model.Issue {
	for i, issue := range issues {
		if !issue.Status.IsTombstone() {
			continue
		}
		visible := make([]model.Issue, 0, len(issues)-1)
		visible = append(visible, issues[:i]...)
		for _, remaining := range issues[i+1:] {
			if !remaining.Status.IsTombstone() {
				visible = append(visible, remaining)
			}
		}
		return visible
	}
	return issues
}

// WithWeights installs feedback-adjusted factor weights on the builder's
// analyzer so triage and priority hints computed from this snapshot use them.
// nil leaves the defaults in place.
func (b *SnapshotBuilder) WithWeights(w *analysis.Weights) *SnapshotBuilder {
	if w != nil {
		b.analyzer.SetWeights(*w)
	}
	return b
}

// feedbackWeightsForBeadsPath loads .beads/feedback.json next to the issue
// file and returns the adjusted factor weights when enough accept/ignore
// samples exist to apply them (analysis.MinFeedbackSamples); nil otherwise.
// This is the TUI counterpart of the robot path's loadRobotFeedback, so the
// priority hints (p) and the actionable view rank issues the same way
// --robot-triage does.
func feedbackWeightsForBeadsPath(beadsPath string) *analysis.Weights {
	if beadsPath == "" {
		return nil
	}
	fb, err := analysis.LoadFeedback(filepath.Dir(beadsPath))
	if err != nil || fb == nil || !fb.Applies() {
		return nil
	}
	w := fb.Weights()
	return &w
}

// WithAnalysis sets the pre-computed analysis (for when we have cached results).
func (b *SnapshotBuilder) WithAnalysis(a *analysis.GraphStats) *SnapshotBuilder {
	b.analysis = a
	return b
}

func (b *SnapshotBuilder) WithRecipe(r *recipe.Recipe) *SnapshotBuilder {
	b.recipe = r
	return b
}

func (b *SnapshotBuilder) WithBuildConfig(cfg snapshotBuildConfig) *SnapshotBuilder {
	b.cfg = cfg
	return b
}

// WithPreviousSnapshot enables incremental list-item rebuilds when possible.
func (b *SnapshotBuilder) WithPreviousSnapshot(prev *DataSnapshot, diff *analysis.IssueDiff) *SnapshotBuilder {
	b.prevSnapshot = prev
	b.diff = diff
	b.diffStats = issueDiffStats(diff)
	return b
}

// Build constructs the final immutable DataSnapshot.
// This performs all necessary computations that should happen in the background.
// Uses AnalyzeAsync() so Phase 2 metrics compute in background - check Phase2Ready
// or call GetGraphStats().WaitForPhase2() if you need Phase 2 data immediately.
func (b *SnapshotBuilder) Build() *DataSnapshot {
	issues := b.issues
	createdAt := time.Now()
	// Readiness, recipes, and scoring share the analyzer's reference instant.
	// Freshness still describes when this snapshot was actually constructed.
	now := b.analyzer.Now()

	// Apply default sorting to match the legacy reload path:
	// Open first, then priority (ascending), then created date (newest first).
	sort.Slice(issues, func(i, j int) bool {
		iClosed := isClosedLikeStatus(issues[i].Status)
		jClosed := isClosedLikeStatus(issues[j].Status)
		if iClosed != jClosed {
			return !iClosed
		}
		if issues[i].Priority != issues[j].Priority {
			return issues[i].Priority < issues[j].Priority
		}
		return issues[i].CreatedAt.After(issues[j].CreatedAt)
	})

	// Compute analysis if not provided
	// Use AnalyzeAsync to allow Phase 2 to run in background
	graphStats := b.analysis
	if graphStats == nil {
		if b.cfg.SkipPhase2 {
			// Still compute Phase 1 metrics, but skip expensive Phase 2 work.
			cfg := analysis.ConfigForSize(len(issues), 0)
			cfg.ComputePageRank = false
			cfg.ComputeBetweenness = false
			cfg.ComputeEigenvector = false
			cfg.ComputeHITS = false
			cfg.ComputeCriticalPath = false
			cfg.ComputeCycles = false
			graphStats = b.analyzer.AnalyzeAsyncWithConfig(context.Background(), cfg)
		} else {
			graphStats = b.analyzer.AnalyzeAsync(context.Background())
		}
	}

	// Build lookup map
	issueMap := make(map[string]*model.Issue, len(issues))
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	// Compute statistics
	cOpen, cReady, cBlocked, cClosed := 0, 0, 0, 0
	readiness := b.analyzer.Readiness()
	for i := range issues {
		issue := &issues[i]
		if isClosedLikeStatus(issue.Status) {
			cClosed++
			continue
		}

		cOpen++
		if issue.Status == model.StatusBlocked {
			cBlocked++
		}
		if b.analyzer.IsCandidate(issue.ID) && readiness.Ready(issue.ID, now) {
			cReady++
		}
	}

	var recipeTriage map[string]float64
	var triageResult analysis.TriageResult
	if b.cfg.PrecomputeTriage || (b.recipe != nil && b.recipe.NeedsTriageScores()) {
		triageResult = analysis.ComputeTriageFromAnalyzer(b.analyzer, graphStats, issues, analysis.TriageOptions{}, now)
		recipeTriage = make(map[string]float64, len(triageResult.Recommendations))
		for _, rec := range triageResult.Recommendations {
			recipeTriage[rec.ID] = rec.Score
		}
	}
	viewIssues := issues
	var recipeErr error
	if b.recipe != nil {
		viewIssues, recipeErr = applyRecipeToIssues(issues, b.analyzer, graphStats, recipeTriage, b.recipe, now)
	}

	// Build list items with graph scores (respecting recipe filtering/sorting when present).
	listItemsIncremental := false
	statsForListItems := graphStats
	// If analysis was computed asynchronously for this snapshot build, treat Phase 2 scores
	// as not-yet-available to keep list items deterministic (they will be refreshed when
	// Phase 2 completes via Phase2ReadyMsg).
	if b.analysis == nil {
		statsForListItems = nil
	}
	var listItems []IssueItem
	if shouldUseIncrementalList(b.prevSnapshot, b.diff, b.recipe, b.diffStats, viewIssues) {
		listItems = buildListItemsIncremental(viewIssues, statsForListItems, b.prevSnapshot, b.diff)
		listItemsIncremental = true
	} else {
		listItems = buildListItems(viewIssues, statsForListItems)
	}

	var (
		triageScores  map[string]float64
		triageReasons map[string]analysis.TriageReasons
		quickWinSet   map[string]bool
		blockerSet    map[string]bool
		unblocksMap   map[string][]string
	)

	// Compute triage insights (may be skipped for large/huge datasets; bv-9thm).
	if b.cfg.PrecomputeTriage || (b.recipe != nil && b.recipe.NeedsTriageScores()) {
		triageScores = make(map[string]float64, len(triageResult.Recommendations))
		triageReasons = make(map[string]analysis.TriageReasons, len(triageResult.Recommendations))
		quickWinSet = make(map[string]bool, len(triageResult.QuickWins))
		blockerSet = make(map[string]bool, len(triageResult.BlockersToClear))
		unblocksMap = make(map[string][]string, len(triageResult.Recommendations))

		for _, rec := range triageResult.Recommendations {
			triageScores[rec.ID] = rec.Score
			if len(rec.Reasons) > 0 {
				triageReasons[rec.ID] = analysis.TriageReasons{
					Primary:    rec.Reasons[0],
					All:        rec.Reasons,
					ActionHint: rec.Action,
				}
			}
			unblocksMap[rec.ID] = rec.UnblocksIDs
		}
		for _, qw := range triageResult.QuickWins {
			quickWinSet[qw.ID] = true
		}
		for _, bl := range triageResult.BlockersToClear {
			blockerSet[bl.ID] = true
		}

		// Update list items with triage data
		for i := range listItems {
			id := listItems[i].Issue.ID
			listItems[i].TriageScore = triageScores[id]
			if reasons, exists := triageReasons[id]; exists {
				listItems[i].TriageReason = reasons.Primary
				listItems[i].TriageReasons = reasons.All
			}
			listItems[i].IsQuickWin = quickWinSet[id]
			listItems[i].IsBlocker = blockerSet[id]
			listItems[i].UnblocksCount = len(unblocksMap[id])
		}
	}

	var (
		treeRoots   []*IssueTreeNode
		treeNodeMap map[string]*IssueTreeNode
	)
	if b.cfg.PrecomputeTree {
		treeRoots, treeNodeMap = buildIssueTreeNodes(issues)
	}

	var boardState *BoardState
	if b.cfg.PrecomputeBoard {
		boardState = buildBoardState(issues)
	}

	insights := analysis.Insights{Stats: graphStats, ClusterDensity: graphStats.Density}
	if b.cfg.PrecomputeInsights {
		insights = graphStats.GenerateInsights(len(issues))
	}

	var graphLayout *GraphLayout
	if b.cfg.PrecomputeGraphLayout {
		graphLayout = buildGraphLayout(issues, graphStats)
	}

	listModelItems := make([]list.Item, len(listItems))
	listIndexByID := make(map[string]int, len(listItems))
	for i := range listItems {
		item := listItems[i]
		listModelItems[i] = item
		id := item.Issue.ID
		listIndexByID[id] = i
	}
	semanticIDs, semanticDocs := buildSnapshotSearchDocuments(listItems, b.prevSnapshot, b.diff)

	alerts, alertsCritical, alertsWarning, alertsInfo := computeAlerts(issues, graphStats, b.analyzer)

	snapshot := &DataSnapshot{
		LoadError:      recipeErr,
		Issues:         issues,
		IssueMap:       issueMap,
		ViewIssues:     viewIssues,
		Analyzer:       b.analyzer,
		Analysis:       graphStats,
		insights:       insights,
		CountOpen:      cOpen,
		CountReady:     cReady,
		CountBlocked:   cBlocked,
		CountClosed:    cClosed,
		ListItems:      listItems,
		listModelItems: listModelItems,
		listIndexByID:  listIndexByID,
		listOrderHash:  listOrderFingerprint(listItems),
		semanticIDs:    semanticIDs,
		semanticDocs:   semanticDocs,
		alerts:         alerts,
		alertsCritical: alertsCritical,
		alertsWarning:  alertsWarning,
		alertsInfo:     alertsInfo,
		TriageScores:   triageScores,
		TriageReasons:  triageReasons,
		QuickWinSet:    quickWinSet,
		BlockerSet:     blockerSet,
		UnblocksMap:    unblocksMap,
		TreeRoots:      treeRoots,
		TreeNodeMap:    treeNodeMap,
		BoardState:     boardState,
		graphLayout:    graphLayout,
		CreatedAt:      createdAt,
		RecipeName:     recipeName(b.recipe),
		RecipeHash:     recipeFingerprint(b.recipe),
		phase2Ready:    graphStats.IsPhase2Ready(),
		IssueDiff:      b.diff,
		IssueDiffStats: IssueDiffStats{
			Changed: b.diffStats.Changed,
			Total:   b.diffStats.Total,
			Ratio:   b.diffStats.Ratio,
		},
		IncrementalListUsed: listItemsIncremental,
	}
	snapshot.phase2Input = snapshot.detachedPhase2Input()
	return snapshot
}

// buildSnapshotSearchDocuments owns its ID slice and map. Document strings are
// immutable and can be shared only when the complete source diff says the issue
// is unchanged. Iterating the current list keeps removed and filtered rows out.
func buildSnapshotSearchDocuments(items []IssueItem, previous *DataSnapshot, diff *analysis.IssueDiff) ([]string, map[string]string) {
	var unchanged map[string]bool
	if previous != nil && diff != nil && !diff.HasDuplicateIDs {
		unchanged = make(map[string]bool, len(diff.Unchanged))
		for _, id := range diff.Unchanged {
			unchanged[id] = true
		}
	}
	ids := make([]string, len(items))
	docs := make(map[string]string, len(items))
	for i, item := range items {
		id := item.Issue.ID
		ids[i] = id
		// Canonical fingerprints treat labels as a set; search text preserves
		// their order. Require that order too before sharing the old string.
		if unchanged[id] && previous.IssueMap[id] != nil && slices.Equal(previous.IssueMap[id].Labels, item.Issue.Labels) {
			if document, exists := previous.semanticDocs[id]; exists {
				docs[id] = document
				continue
			}
		}
		docs[id] = search.IssueDocument(item.Issue)
	}
	return ids, docs
}

func listOrderFingerprint(items []IssueItem) uint64 {
	const (
		offset64 = uint64(14695981039346656037)
		prime64  = uint64(1099511628211)
	)
	hash := offset64
	for i := range items {
		id := items[i].Issue.ID
		for j := 0; j < len(id); j++ {
			hash ^= uint64(id[j])
			hash *= prime64
		}
		hash ^= 0xff
		hash *= prime64
	}
	return hash
}

func issueDiffStats(diff *analysis.IssueDiff) IssueDiffStats {
	if diff == nil {
		return IssueDiffStats{}
	}
	changed := len(diff.Added) + len(diff.Removed) + len(diff.Modified)
	total := changed + len(diff.Unchanged)
	ratio := 0.0
	if total > 0 {
		ratio = float64(changed) / float64(total)
	}
	return IssueDiffStats{
		Changed: changed,
		Total:   total,
		Ratio:   ratio,
	}
}

func shouldUseIncrementalList(prev *DataSnapshot, diff *analysis.IssueDiff, r *recipe.Recipe, stats IssueDiffStats, currentIssues []model.Issue) bool {
	if prev == nil || diff == nil || len(prev.ListItems) == 0 {
		return false
	}
	if diff.HasDuplicateIDs {
		return false
	}
	// Topology changes can alter graph-derived scores for otherwise unchanged
	// issues, so reusing their old list items would be incorrect. Additions and
	// removals also change pagination and may shift recipe membership.
	if len(diff.Added) > 0 || len(diff.Removed) > 0 || len(diff.DependencyChanged) > 0 {
		return false
	}

	currentRecipeName := ""
	currentRecipeHash := ""
	if r != nil {
		currentRecipeName = r.Name
		currentRecipeHash = recipeFingerprint(r)
	}

	if prev.RecipeName != currentRecipeName || prev.RecipeHash != currentRecipeHash {
		return false
	}
	if len(currentIssues) != len(prev.ListItems) {
		return false
	}
	for i := range currentIssues {
		if currentIssues[i].ID != prev.ListItems[i].Issue.ID {
			return false
		}
	}
	if stats.Total == 0 {
		return false
	}
	return stats.Ratio <= incrementalListMaxChangeRatio
}

func buildListItems(issues []model.Issue, stats *analysis.GraphStats) []IssueItem {
	listItems := make([]IssueItem, len(issues))
	for i := range issues {
		listItems[i] = buildIssueItemForSnapshot(issues[i], stats)
	}
	return listItems
}

func buildListItemsIncremental(issues []model.Issue, stats *analysis.GraphStats, prev *DataSnapshot, diff *analysis.IssueDiff) []IssueItem {
	if prev == nil || len(prev.ListItems) != len(issues) || diff == nil {
		return buildListItems(issues, stats)
	}

	listItems := make([]IssueItem, len(issues))
	copy(listItems, prev.ListItems)
	for i := range listItems {
		// Fingerprints canonicalize collection order. Keep derived metrics, but
		// publish the current source order for labels, comments and dependencies.
		listItems[i].Issue = issues[i]
		clearIssueItemEphemeral(&listItems[i])
	}
	for _, id := range diff.Modified {
		index, ok := prev.listIndexByID[id]
		if !ok || index < 0 || index >= len(issues) || issues[index].ID != id {
			return buildListItems(issues, stats)
		}
		listItems[index] = buildIssueItemForSnapshot(issues[index], stats)
	}
	return listItems
}

func buildIssueItemForSnapshot(issue model.Issue, stats *analysis.GraphStats) IssueItem {
	item := IssueItem{}
	resetIssueItemForSnapshot(&item, issue, stats)
	return item
}

func resetIssueItemForSnapshot(item *IssueItem, issue model.Issue, stats *analysis.GraphStats) {
	item.Issue = issue
	if stats != nil {
		item.GraphScore = stats.GetPageRankScore(issue.ID)
		item.Impact = stats.GetCriticalPathScore(issue.ID)
	} else {
		item.GraphScore = 0
		item.Impact = 0
	}
	item.RepoPrefix = issueRepoKey(issue)
	clearIssueItemEphemeral(item)
}

func clearIssueItemEphemeral(item *IssueItem) {
	item.DiffStatus = DiffStatusNone

	item.SearchScore = 0
	item.SearchTextScore = 0
	item.SearchComponents = nil
	item.SearchScoreSet = false

	item.TriageScore = 0
	item.TriageReason = ""
	item.TriageReasons = nil
	item.IsQuickWin = false
	item.IsBlocker = false
	item.UnblocksCount = 0
}

func recipeName(r *recipe.Recipe) string {
	if r == nil {
		return ""
	}
	return r.Name
}

func applyRecipeToIssues(issues []model.Issue, analyzer *analysis.Analyzer, stats *analysis.GraphStats, triage map[string]float64, r *recipe.Recipe, now time.Time) ([]model.Issue, error) {
	if r == nil {
		return append([]model.Issue(nil), issues...), nil
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	metrics := recipe.Metrics{Triage: triage}
	if stats != nil {
		metrics.Graph = stats
	}
	if analyzer != nil {
		metrics.Readiness = analyzer.Readiness()
	}
	candidates := issues
	if r.Filters.Actionable != nil && analyzer != nil {
		candidates = make([]model.Issue, 0, len(issues))
		for _, issue := range issues {
			if analyzer.IsCandidate(issue.ID) {
				candidates = append(candidates, issue)
			}
		}
	}
	return recipe.Apply(candidates, metrics, r, now)
}

func buildGraphLayout(issues []model.Issue, stats *analysis.GraphStats) *GraphLayout {
	size := len(issues)
	ids := make([]string, 0, size)
	blockers := make(map[string][]string, size)
	dependents := make(map[string][]string, size)

	for i := range issues {
		issue := &issues[i]
		ids = append(ids, issue.ID)

		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			blockers[issue.ID] = append(blockers[issue.ID], dep.DependsOnID)
			dependents[dep.DependsOnID] = append(dependents[dep.DependsOnID], issue.ID)
		}
	}

	layout := &GraphLayout{
		Blockers:   blockers,
		Dependents: dependents,
	}

	if stats != nil {
		layout.RankInDegree = stats.InDegreeRank()
		layout.RankOutDegree = stats.OutDegreeRank()
		layout.RankPageRank = stats.PageRankRank()
		layout.RankBetweenness = stats.BetweennessRank()
		layout.RankEigenvector = stats.EigenvectorRank()
		layout.RankHubs = stats.HubsRank()
		layout.RankAuthorities = stats.AuthoritiesRank()
		layout.RankCriticalPath = stats.CriticalPathRank()
	}

	layout.SortedIDs = orderIssueIDsByRank(ids, layout.RankCriticalPath)
	return layout
}

func buildBoardState(issues []model.Issue) *BoardState {
	if len(issues) == 0 {
		return nil
	}
	return &BoardState{
		ByStatus:   groupIssuesByMode(issues, SwimByStatus),
		ByPriority: groupIssuesByMode(issues, SwimByPriority),
		ByType:     groupIssuesByMode(issues, SwimByType),
	}
}

func orderIssueIDsByRank(ids []string, ranks map[string]int) []string {
	if len(ids) == 0 {
		return nil
	}

	// If we have a rank map, rebuild in O(n) without sorting (ranks are 1..N).
	if len(ranks) > 0 {
		ordered := make([]string, len(ids))
		var missing []string

		for _, id := range ids {
			rank := ranks[id]
			if rank < 1 || rank > len(ordered) || ordered[rank-1] != "" {
				missing = append(missing, id)
				continue
			}
			ordered[rank-1] = id
		}

		// Compact any gaps then append missing IDs deterministically.
		out := make([]string, 0, len(ids))
		for _, id := range ordered {
			if id != "" {
				out = append(out, id)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out = append(out, missing...)
		}
		if len(out) > 0 {
			return out
		}
	}

	// Fallback: stable alphabetical ordering.
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return sorted
}

// GetGraphStats returns the GraphStats pointer for Phase 2 waiting.
// Callers can use stats.WaitForPhase2() to block until Phase 2 completes.
func (s *DataSnapshot) GetGraphStats() *analysis.GraphStats {
	if s == nil {
		return nil
	}
	return s.Analysis
}

// IsEmpty returns true if the snapshot has no issues.
func (s *DataSnapshot) IsEmpty() bool {
	return s == nil || len(s.Issues) == 0
}

// GetIssue returns an issue by ID, or nil if not found.
func (s *DataSnapshot) GetIssue(id string) *model.Issue {
	if s == nil || s.IssueMap == nil {
		return nil
	}
	return s.IssueMap[id]
}

// Age returns how long ago this snapshot was created.
func (s *DataSnapshot) Age() time.Duration {
	if s == nil {
		return 0
	}
	return time.Since(s.CreatedAt)
}

// deepCopyTree creates a deep copy of TreeRoots and TreeNodeMap.
// This is necessary because the tree view mutates node.Expanded and node.Children,
// which would corrupt shared snapshots if we only did pointer aliasing.
// Issue pointers are rebound to the provided issueMap so the copied tree stays
// detached from any legacy slice aliasing of the original snapshot issues.
// Returns (nil, nil) if input is empty.
func deepCopyTree(roots []*IssueTreeNode, nodeMap map[string]*IssueTreeNode, issueMap map[string]*model.Issue) ([]*IssueTreeNode, map[string]*IssueTreeNode) {
	if len(roots) == 0 && len(nodeMap) == 0 {
		return nil, nil
	}

	// Build a mapping from old node pointers to new node pointers
	oldToNew := make(map[*IssueTreeNode]*IssueTreeNode, len(nodeMap))

	// First pass: create shallow copies of all nodes (without Children/Parent links)
	for _, oldNode := range nodeMap {
		if oldNode == nil {
			continue
		}
		issue := oldNode.Issue
		if oldNode.Issue != nil && issueMap != nil {
			if rebound, ok := issueMap[oldNode.Issue.ID]; ok {
				issue = rebound
			}
		}
		newNode := &IssueTreeNode{
			Issue:    issue,
			Expanded: oldNode.Expanded,
			Depth:    oldNode.Depth,
			// Children and Parent set in second pass
		}
		oldToNew[oldNode] = newNode
	}

	// Second pass: rebuild Children slices and Parent pointers
	for oldNode, newNode := range oldToNew {
		if len(oldNode.Children) > 0 {
			newNode.Children = make([]*IssueTreeNode, 0, len(oldNode.Children))
			for _, oldChild := range oldNode.Children {
				if newChild, ok := oldToNew[oldChild]; ok {
					newNode.Children = append(newNode.Children, newChild)
				}
			}
		}
		if oldNode.Parent != nil {
			if newParent, ok := oldToNew[oldNode.Parent]; ok {
				newNode.Parent = newParent
			}
		}
	}

	// Build new roots slice
	var newRoots []*IssueTreeNode
	if len(roots) > 0 {
		newRoots = make([]*IssueTreeNode, 0, len(roots))
		for _, oldRoot := range roots {
			if newRoot, ok := oldToNew[oldRoot]; ok {
				newRoots = append(newRoots, newRoot)
			}
		}
	}

	// Build new nodeMap
	var newNodeMap map[string]*IssueTreeNode
	if len(nodeMap) > 0 {
		newNodeMap = make(map[string]*IssueTreeNode, len(nodeMap))
		for id, oldNode := range nodeMap {
			if newNode, ok := oldToNew[oldNode]; ok {
				newNodeMap[id] = newNode
			}
		}
	}

	return newRoots, newNodeMap
}

// deepCopyListItems creates a deep copy of a ListItems slice.
// Each IssueItem contains mutable issue backing state plus mutable adapter
// fields (SearchComponents map, TriageReasons slice) that must be copied to
// prevent race conditions between snapshots.
func deepCopyListItems(items []IssueItem) []IssueItem {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]IssueItem, len(items))
	for i := range items {
		cloned[i] = items[i]
		cloned[i].Issue = items[i].Issue.Clone()
		// Deep copy the mutable SearchComponents map
		if len(items[i].SearchComponents) > 0 {
			cloned[i].SearchComponents = make(map[string]float64, len(items[i].SearchComponents))
			for k, v := range items[i].SearchComponents {
				cloned[i].SearchComponents[k] = v
			}
		}
		// Deep copy the mutable TriageReasons slice
		if len(items[i].TriageReasons) > 0 {
			cloned[i].TriageReasons = make([]string, len(items[i].TriageReasons))
			copy(cloned[i].TriageReasons, items[i].TriageReasons)
		}
	}
	return cloned
}

// deepCopyBoardState creates a deep copy of a BoardState.
// BoardState contains [4][]model.Issue arrays which are mutable slices
// that must be copied to prevent race conditions between snapshots.
func deepCopyBoardState(bs *BoardState) *BoardState {
	if bs == nil {
		return nil
	}
	cloned := &BoardState{}
	for i := 0; i < 4; i++ {
		if len(bs.ByStatus[i]) > 0 {
			cloned.ByStatus[i] = make([]model.Issue, len(bs.ByStatus[i]))
			for j := range bs.ByStatus[i] {
				cloned.ByStatus[i][j] = bs.ByStatus[i][j].Clone()
			}
		}
		if len(bs.ByPriority[i]) > 0 {
			cloned.ByPriority[i] = make([]model.Issue, len(bs.ByPriority[i]))
			for j := range bs.ByPriority[i] {
				cloned.ByPriority[i][j] = bs.ByPriority[i][j].Clone()
			}
		}
		if len(bs.ByType[i]) > 0 {
			cloned.ByType[i] = make([]model.Issue, len(bs.ByType[i]))
			for j := range bs.ByType[i] {
				cloned.ByType[i][j] = bs.ByType[i][j].Clone()
			}
		}
	}
	return cloned
}

// graphLayoutWithRanks returns a new GraphLayout with Phase 2 ranks populated from stats.
// This is a pure function - it does not modify the input.
func graphLayoutWithRanks(old *GraphLayout, stats *analysis.GraphStats) *GraphLayout {
	if old == nil {
		return nil
	}
	if stats == nil {
		// Return a shallow copy with no rank updates
		return &GraphLayout{
			Blockers:         old.Blockers,
			Dependents:       old.Dependents,
			SortedIDs:        old.SortedIDs,
			RankPageRank:     old.RankPageRank,
			RankBetweenness:  old.RankBetweenness,
			RankEigenvector:  old.RankEigenvector,
			RankHubs:         old.RankHubs,
			RankAuthorities:  old.RankAuthorities,
			RankCriticalPath: old.RankCriticalPath,
			RankInDegree:     old.RankInDegree,
			RankOutDegree:    old.RankOutDegree,
		}
	}

	criticalPathRank := stats.CriticalPathRank()
	return &GraphLayout{
		Blockers:         old.Blockers,
		Dependents:       old.Dependents,
		SortedIDs:        orderIssueIDsByRank(old.SortedIDs, criticalPathRank),
		RankPageRank:     stats.PageRankRank(),
		RankBetweenness:  stats.BetweennessRank(),
		RankEigenvector:  stats.EigenvectorRank(),
		RankHubs:         stats.HubsRank(),
		RankAuthorities:  stats.AuthoritiesRank(),
		RankCriticalPath: criticalPathRank,
		RankInDegree:     stats.InDegreeRank(),
		RankOutDegree:    stats.OutDegreeRank(),
	}
}

// detachedPhase2Input captures the mutable issue-backed surfaces on the UI
// thread before asynchronous preparation. Layout ranks and semantic documents
// are immutable snapshot data; the preparation only reads them.
func (s *DataSnapshot) detachedPhase2Input() *DataSnapshot {
	if s == nil {
		return nil
	}
	cloned := *s
	cloned.phase2Input = nil
	if input := s.phase2Input; input != nil {
		// Keep current source metadata (the worker fills hashes/load warnings
		// after Build), while reusing private detached issue-backed inputs.
		cloned.Issues, cloned.IssueMap = input.Issues, input.IssueMap
		cloned.ViewIssues, cloned.ListItems = input.ViewIssues, input.ListItems
		cloned.TreeRoots, cloned.TreeNodeMap = input.TreeRoots, input.TreeNodeMap
		cloned.BoardState = input.BoardState
		return &cloned
	}
	cloned.Issues = cloneIssuesForAsync(s.Issues)
	cloned.IssueMap = make(map[string]*model.Issue, len(cloned.Issues))
	for i := range cloned.Issues {
		cloned.IssueMap[cloned.Issues[i].ID] = &cloned.Issues[i]
	}
	cloned.ViewIssues = cloneIssuesForAsync(s.ViewIssues)
	cloned.ListItems = deepCopyListItems(s.ListItems)
	cloned.TreeRoots, cloned.TreeNodeMap = deepCopyTree(s.TreeRoots, s.TreeNodeMap, cloned.IssueMap)
	cloned.BoardState = deepCopyBoardState(s.BoardState)
	return &cloned
}

// WithPhase2 returns a new DataSnapshot with Phase 2 analysis results populated.
// It keeps read-only Phase 1 structures where safe, but clones issue-backed and
// tree-backed state so the returned snapshot remains detached from any legacy UI
// code that may continue mutating slices, maps, or tree expansion state.
// This is the core method for immutable snapshot updates (bv-f6uz).
func (s *DataSnapshot) WithPhase2(stats *analysis.GraphStats, insights analysis.Insights, issues []model.Issue, analyzer *analysis.Analyzer) *DataSnapshot {
	if s == nil {
		return nil
	}

	issuesClone := cloneIssuesForAsync(s.Issues)
	clonedIssueMap := make(map[string]*model.Issue, len(issuesClone))
	for i := range issuesClone {
		clonedIssueMap[issuesClone[i].ID] = &issuesClone[i]
	}

	// Compute triage data from Phase 2 analysis
	var triageScores map[string]float64
	var triageReasons map[string]analysis.TriageReasons
	var quickWinSet map[string]bool
	var blockerSet map[string]bool
	var unblocksMap map[string][]string
	var completedTriage *analysis.TriageResult

	if stats != nil && analyzer != nil && len(issues) > 0 {
		triageResult := analysis.ComputeTriageFromAnalyzer(analyzer, stats, issues, analysis.TriageOptions{}, analyzer.Now())
		completedTriage = &triageResult
		triageScores = make(map[string]float64, len(triageResult.Recommendations))
		triageReasons = make(map[string]analysis.TriageReasons, len(triageResult.Recommendations))
		quickWinSet = make(map[string]bool, len(triageResult.QuickWins))
		blockerSet = make(map[string]bool, len(triageResult.BlockersToClear))
		unblocksMap = make(map[string][]string, len(triageResult.Recommendations))

		for _, rec := range triageResult.Recommendations {
			triageScores[rec.ID] = rec.Score
			if len(rec.Reasons) > 0 {
				triageReasons[rec.ID] = analysis.TriageReasons{
					Primary:    rec.Reasons[0],
					All:        rec.Reasons,
					ActionHint: rec.Action,
				}
			}
			unblocksMap[rec.ID] = rec.UnblocksIDs
		}
		for _, qw := range triageResult.QuickWins {
			quickWinSet[qw.ID] = true
		}
		for _, bl := range triageResult.BlockersToClear {
			blockerSet[bl.ID] = true
		}
	}

	// Deep copy tree structures since tree view mutates node.Expanded and node.Children.
	// Rebind tree nodes to cloned issues so the new snapshot stays detached from
	// legacy m.issues sorting and pointer churn.
	treeRoots, treeNodeMap := deepCopyTree(s.TreeRoots, s.TreeNodeMap, clonedIssueMap)
	listItems := deepCopyListItems(s.ListItems)
	listModelItems := make([]list.Item, len(listItems))
	listIndexByID := make(map[string]int, len(listItems))
	for i := range listItems {
		item := &listItems[i]
		id := item.Issue.ID
		if stats != nil {
			item.GraphScore = stats.GetPageRankScore(id)
			item.Impact = stats.GetCriticalPathScore(id)
		}
		item.TriageScore = triageScores[id]
		reasons := triageReasons[id]
		item.TriageReason = reasons.Primary
		item.TriageReasons = reasons.All
		item.IsQuickWin = quickWinSet[id]
		item.IsBlocker = blockerSet[id]
		item.UnblocksCount = len(unblocksMap[id])
		listModelItems[i] = listItems[i]
		listIndexByID[listItems[i].Issue.ID] = i
	}
	alerts, alertsCritical, alertsWarning, alertsInfo := computeAlerts(issuesClone, stats, analyzer)

	return &DataSnapshot{
		// Clone mutable Phase 1 data so the new snapshot stays immutable even if
		// legacy UI state continues mutating its own slices or maps.
		Issues:         issuesClone,
		IssueMap:       clonedIssueMap,
		pooledIssues:   s.pooledIssues,
		ViewIssues:     cloneIssuesForAsync(s.ViewIssues),
		ListItems:      listItems, // Deep copy - contains mutable SearchComponents/TriageReasons
		listModelItems: listModelItems,
		listIndexByID:  listIndexByID,
		listOrderHash:  s.listOrderHash,
		semanticIDs:    s.semanticIDs,
		semanticDocs:   s.semanticDocs,
		alerts:         alerts,
		alertsCritical: alertsCritical,
		alertsWarning:  alertsWarning,
		alertsInfo:     alertsInfo,
		phase2Triage:   completedTriage,
		TreeRoots:      treeRoots,                        // Deep copy - tree view mutates these
		TreeNodeMap:    treeNodeMap,                      // Deep copy - tree view mutates these
		BoardState:     deepCopyBoardState(s.BoardState), // Deep copy - contains mutable [4][]model.Issue arrays

		// Updated with Phase 2 data
		Analyzer:      analyzer,
		Analysis:      stats,
		insights:      insights,
		graphLayout:   graphLayoutWithRanks(s.graphLayout, stats),
		phase2Ready:   true,
		TriageScores:  triageScores,
		TriageReasons: triageReasons,
		QuickWinSet:   quickWinSet,
		BlockerSet:    blockerSet,
		UnblocksMap:   unblocksMap,

		// Copied metadata
		CountOpen:            s.CountOpen,
		CountReady:           s.CountReady,
		CountBlocked:         s.CountBlocked,
		CountClosed:          s.CountClosed,
		CreatedAt:            s.CreatedAt,
		DataHash:             s.DataHash,
		AuthorityHash:        s.AuthorityHash,
		RecipeName:           s.RecipeName,
		RecipeHash:           s.RecipeHash,
		DatasetTier:          s.DatasetTier,
		SourceIssueCountHint: s.SourceIssueCountHint,
		LoadedOpenOnly:       s.LoadedOpenOnly,
		TruncatedCount:       s.TruncatedCount,
		LargeDatasetWarning:  s.LargeDatasetWarning,
		LoadWarningCount:     s.LoadWarningCount,
		IssueDiff:            s.IssueDiff,
		IssueDiffStats:       s.IssueDiffStats,
		IncrementalListUsed:  s.IncrementalListUsed,
		LoadError:            s.LoadError,
		ErrorTime:            s.ErrorTime,
		StaleWarning:         s.StaleWarning,
	}
}
