package analysis

import (
	"sort"
	"sync"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TriageContext provides unified caching for triage-related computations.
//
// It wraps an Analyzer and provides lazy-computed, cached accessors for
// expensive operations that are called multiple times during triage analysis.
//
// Lifecycle:
//  1. Create with NewTriageContext(analyzer)
//  2. Use accessors (ActionableIssues, BlockerDepth, etc.) - all cached
//  3. Discard when triage is complete (caches are not persisted)
//
// Thread Safety:
//   - Single-request use: No locking needed
//   - Concurrent use: Use NewTriageContextThreadSafe() for synchronized access
//
// Performance:
//   - First access to each method computes and caches the result
//   - Subsequent accesses return cached values in O(1)
//   - Memory: O(n) where n is the number of issues
type TriageContext struct {
	// Input
	analyzer *Analyzer

	// Computed caches
	actionable         []model.Issue
	actionableSet      map[string]bool
	actionableComputed bool

	blockerDepth     map[string]int
	openBlockers     map[string][]string
	unblocksMap      map[string][]string
	unblocksComputed bool

	// Thread safety (nil for single-threaded mode)
	mu *sync.Mutex
}

// NewTriageContext creates a new TriageContext for single-threaded use.
//
// The context shares the Analyzer's underlying data structures but maintains
// its own caches for derived computations.
func NewTriageContext(analyzer *Analyzer) *TriageContext {
	return &TriageContext{
		analyzer:     analyzer,
		blockerDepth: make(map[string]int),
		openBlockers: make(map[string][]string),
	}
}

// NewTriageContextThreadSafe creates a thread-safe TriageContext.
//
// Use this when the context may be accessed concurrently from multiple goroutines.
// The thread-safe version uses a simple mutex to avoid deadlocks from nested calls.
func NewTriageContextThreadSafe(analyzer *Analyzer) *TriageContext {
	ctx := NewTriageContext(analyzer)
	ctx.mu = &sync.Mutex{}
	return ctx
}

// lock acquires the mutex if present (for thread-safe mode)
func (ctx *TriageContext) lock() {
	if ctx.mu != nil {
		ctx.mu.Lock()
	}
}

// unlock releases the mutex if present
func (ctx *TriageContext) unlock() {
	if ctx.mu != nil {
		ctx.mu.Unlock()
	}
}

// Analyzer returns the underlying Analyzer.
//
// Use this when you need direct access to Analyzer methods that aren't cached.
func (ctx *TriageContext) Analyzer() *Analyzer {
	return ctx.analyzer
}

// ActionableIssues returns all open issues that are not blocked.
//
// The result is computed once and cached. Subsequent calls return the cached value.
// Time complexity: O(n) on first call, O(1) thereafter.
func (ctx *TriageContext) ActionableIssues() []model.Issue {
	ctx.lock()
	defer ctx.unlock()

	ctx.ensureActionableInternal()
	return ctx.actionable
}

// IsActionable returns true when the issue is non-closed, has no active
// scheduler deferral, and has neither an open blocking dependency nor a blocked
// parent chain.
//
// This ensures ActionableIssues is computed first, then does O(1) lookup.
func (ctx *TriageContext) IsActionable(id string) bool {
	ctx.lock()
	defer ctx.unlock()

	ctx.ensureActionableInternal()
	return ctx.actionableSet[id]
}

// ensureActionableInternal populates actionable caches without locking.
// MUST be called while holding the lock.
func (ctx *TriageContext) ensureActionableInternal() {
	if ctx.actionableComputed {
		return
	}

	ctx.actionable = ctx.analyzer.GetActionableIssues()
	ctx.actionableSet = make(map[string]bool, len(ctx.actionable))
	for _, issue := range ctx.actionable {
		ctx.actionableSet[issue.ID] = true
	}
	ctx.actionableComputed = true
}

// ActionableCount returns the number of actionable issues.
func (ctx *TriageContext) ActionableCount() int {
	return len(ctx.ActionableIssues())
}

// BlockerDepth returns the depth of the blocker chain for an issue.
//
// Returns:
//   - 0 if the issue has no open blockers
//   - 1 if blocked by issues that have no blockers themselves
//   - n if the longest chain of blockers has n levels
//   - -1 if the issue is part of a dependency cycle
//
// Time complexity: O(d) on first call where d is depth, O(1) thereafter.
func (ctx *TriageContext) BlockerDepth(id string) int {
	ctx.lock()
	defer ctx.unlock()

	if depth, ok := ctx.blockerDepth[id]; ok {
		return depth
	}

	// Compute depth with cycle detection (no nested locking)
	depth := ctx.computeBlockerDepthInternal(id, make(map[string]bool))
	ctx.blockerDepth[id] = depth
	return depth
}

// computeBlockerDepthInternal recursively computes the blocker depth.
// MUST be called while holding the lock.
func (ctx *TriageContext) computeBlockerDepthInternal(id string, visiting map[string]bool) int {
	// Check cache first
	if depth, ok := ctx.blockerDepth[id]; ok {
		return depth
	}

	// Cycle detection
	if visiting[id] {
		return -1
	}
	visiting[id] = true
	defer func() { visiting[id] = false }()

	// Get open blockers directly (no locking)
	blockers := ctx.getOpenBlockersInternal(id)
	if len(blockers) == 0 {
		ctx.blockerDepth[id] = 0
		return 0
	}

	maxDepth := 0
	for _, blockerID := range blockers {
		depth := ctx.computeBlockerDepthInternal(blockerID, visiting)
		if depth == -1 {
			ctx.blockerDepth[id] = -1
			return -1
		}
		if depth+1 > maxDepth {
			maxDepth = depth + 1
		}
	}

	ctx.blockerDepth[id] = maxDepth
	return maxDepth
}

// getOpenBlockersInternal returns open blockers without locking.
// MUST be called while holding the lock.
//
// IMPORTANT: only true predecessor edges contribute directly. `parent-child`
// edges in beads' JSONL are *rollup* edges (an epic contains its children);
// they are NOT direct predecessor edges. An open, unblocked parent never
// "blocks" its child, and closing the parent never "unblocks" the child —
// that's precisely backwards: the parent closes when its children do.
//
// However, a parent that is itself blocked DOES propagate downward to its
// descendants: a child cannot start while its parent is gated. This matches
// the canonical beads_rust (`br`) semantics:
//
//	// from beads_rust/src/storage/sqlite.rs (compute_blocked_issues_map_impl):
//	// "Standard blocking edges (blocks, conditional-blocks, waits-for) block
//	//  directly. parent-child does not make an open parent block a child;
//	//  instead it propagates an already-blocked parent down to its descendants."
//
// The previous implementation here added every open parent-child parent to
// the blocker set unconditionally, which produced inverted advice like:
//
//	"Work on <epic> first to unblock <epic>.1"
//
// for child tasks whose only edge was a parent-child rollup, even when the
// parent epic itself was perfectly unblocked. That broke `--robot-triage`,
// `--robot-next`, the TUI "Triage Insights" blocked-by list, the "complete
// X first" action hint in GenerateTriageReasons, the UnblocksMap (which
// inflated parent-epic unblocks_ids), and the blocker-depth scoring that
// consumes it.
//
// The fix below restores the distinction: a parent only enters the child's
// blocked_by set when the parent is itself transitively blocked. See
// beads_viewer#158 for the original report. The propagation uses a small
// internal helper (isTransitivelyBlockedInternal) with cycle detection so a
// pathological parent-child cycle cannot infinitely recurse.
func (ctx *TriageContext) getOpenBlockersInternal(id string) []string {
	if blockers, ok := ctx.openBlockers[id]; ok {
		return blockers
	}

	// 1. Direct predecessor edges. Analyzer.GetOpenBlockers already filters
	//    via DependencyType.IsBlocking(), so parent-child / related /
	//    discovered-from edges are excluded by construction.
	blockerSet := make(map[string]struct{})
	for _, blockerID := range ctx.analyzer.GetOpenBlockers(id) {
		blockerSet[blockerID] = struct{}{}
	}

	// 2. Transitive parent-blocked propagation. Match br's behavior in
	//    propagate_blocked_parents: an open parent is added to the child's
	//    blocked_by only when the parent itself is transitively blocked.
	//    A standalone open parent with no real predecessors yields nothing.
	if issue, ok := ctx.analyzer.issueMap[id]; ok {
		for _, dep := range issue.Dependencies {
			if dep == nil || dep.Type != model.DepParentChild {
				continue
			}
			parent, exists := ctx.analyzer.issueMap[dep.DependsOnID]
			if !exists || isClosedLikeStatus(parent.Status) {
				continue
			}
			if ctx.isTransitivelyBlockedInternal(parent.ID, map[string]bool{id: true}) {
				blockerSet[parent.ID] = struct{}{}
			}
		}
	}

	if len(blockerSet) == 0 {
		ctx.openBlockers[id] = nil
		return nil
	}

	blockers := make([]string, 0, len(blockerSet))
	for blockerID := range blockerSet {
		blockers = append(blockers, blockerID)
	}
	sort.Strings(blockers)
	ctx.openBlockers[id] = blockers
	return blockers
}

// isTransitivelyBlockedInternal returns true if `id` has at least one
// open direct predecessor (a `blocks`-type edge to a non-closed issue) OR
// has an open parent-child parent that is itself transitively blocked.
//
// MUST be called while holding the lock. The `visiting` set carries cycle
// detection across recursive calls. If a cycle is encountered, the cycle
// participants are conservatively treated as NOT blocking (returning false),
// matching the way br's propagate_blocked_parents stops at already-seen
// nodes.
//
// This intentionally does NOT consult `ctx.openBlockers` (the cache built
// by getOpenBlockersInternal), because doing so would re-enter the caller
// for the parent ID and trip cycle detection on the legitimate
// child→parent→grandparent walk. Instead it inspects the analyzer's raw
// edges directly, which is O(d) per node.
func (ctx *TriageContext) isTransitivelyBlockedInternal(id string, visiting map[string]bool) bool {
	if visiting[id] {
		return false
	}

	issue, ok := ctx.analyzer.issueMap[id]
	if !ok || isClosedLikeStatus(issue.Status) {
		return false
	}

	// Direct predecessor check: any open blocking-type edge gates this issue.
	if len(ctx.analyzer.GetOpenBlockers(id)) > 0 {
		return true
	}

	// Parent-blocked propagation: an open parent that is itself blocked
	// propagates blocking down. A standalone open parent does not.
	visiting[id] = true
	defer delete(visiting, id)
	for _, dep := range issue.Dependencies {
		if dep == nil || dep.Type != model.DepParentChild {
			continue
		}
		parent, exists := ctx.analyzer.issueMap[dep.DependsOnID]
		if !exists || isClosedLikeStatus(parent.Status) {
			continue
		}
		if ctx.isTransitivelyBlockedInternal(parent.ID, visiting) {
			return true
		}
	}
	return false
}

// OpenBlockers returns the IDs of open issues that block claiming the given issue.
//
// Two contributors:
//
//  1. Direct predecessor edges — `DependencyType.IsBlocking()` (currently
//     `blocks` and the empty-string legacy default).
//  2. Transitive parent-blocked propagation — an open parent-child parent
//     that is itself transitively blocked.
//
// An unblocked open parent does NOT block its child, matching `br ready`
// semantics. See implementation notes on getOpenBlockersInternal and
// isTransitivelyBlockedInternal (beads_viewer#158).
//
// Returns nil if the issue doesn't exist or has no open blockers.
// Time complexity: O(d) on first call where d is dependency count, O(1) thereafter.
func (ctx *TriageContext) OpenBlockers(id string) []string {
	ctx.lock()
	defer ctx.unlock()

	return ctx.getOpenBlockersInternal(id)
}

// UnblocksMap returns a map of issue ID -> IDs of non-deferred issues that
// would lose their sole current claim blocker if this issue were completed.
//
// An issue A unblocks issue B if:
//  1. A is the ONLY remaining open blocker for B (either a direct predecessor
//     or a parent whose blocked state propagates to B)
//  2. B is not closed-like or scheduler-deferred
//
// Time complexity: O(n*d) on first call, O(1) thereafter.
func (ctx *TriageContext) UnblocksMap() map[string][]string {
	ctx.lock()
	defer ctx.unlock()

	if ctx.unblocksComputed {
		return ctx.unblocksMap
	}

	// Compute unblocks using the same logic as buildUnblocksMap
	ctx.unblocksMap = buildUnblocksMap(ctx)
	ctx.unblocksComputed = true
	return ctx.unblocksMap
}

// Unblocks returns the IDs of issues that would be unblocked if this issue
// were completed.
func (ctx *TriageContext) Unblocks(id string) []string {
	return ctx.UnblocksMap()[id]
}

// UnblocksCount returns how many issues would be unblocked by completing this issue.
func (ctx *TriageContext) UnblocksCount(id string) int {
	return len(ctx.Unblocks(id))
}

// AllBlockerDepths returns all computed blocker depths.
// Forces computation for all issues in the analyzer.
//
// This is useful when you need blocker depths for all issues (e.g., triage scoring).
func (ctx *TriageContext) AllBlockerDepths() map[string]int {
	ctx.lock()
	defer ctx.unlock()

	// Compute for all issues (while holding lock)
	visiting := make(map[string]bool)
	for id := range ctx.analyzer.issueMap {
		if _, ok := ctx.blockerDepth[id]; !ok {
			ctx.computeBlockerDepthInternal(id, visiting)
		}
	}

	// Return a copy to prevent external modification
	result := make(map[string]int, len(ctx.blockerDepth))
	for k, v := range ctx.blockerDepth {
		result[k] = v
	}
	return result
}

// Reset clears all caches for reuse with fresh data.
//
// Call this if the underlying Analyzer data changes and you want to recompute.
func (ctx *TriageContext) Reset() {
	ctx.lock()
	defer ctx.unlock()

	ctx.actionable = nil
	ctx.actionableSet = nil
	ctx.actionableComputed = false
	ctx.blockerDepth = make(map[string]int)
	ctx.openBlockers = make(map[string][]string)
	ctx.unblocksMap = nil
	ctx.unblocksComputed = false
}

// GetIssue returns an issue by ID from the underlying Analyzer.
// This is a convenience passthrough that doesn't require caching.
func (ctx *TriageContext) GetIssue(id string) *model.Issue {
	return ctx.analyzer.GetIssue(id)
}

// getIssue returns the analyzer's read-only internal view for triage hot paths.
func (ctx *TriageContext) getIssue(id string) *model.Issue {
	return ctx.analyzer.getIssue(id)
}

// IssueCount returns the total number of issues.
func (ctx *TriageContext) IssueCount() int {
	return len(ctx.analyzer.issueMap)
}

// Issues returns all issues from the underlying Analyzer.
func (ctx *TriageContext) Issues() []model.Issue {
	issues := make([]model.Issue, 0, len(ctx.analyzer.issueMap))
	for _, issue := range ctx.analyzer.issueMap {
		issues = append(issues, issue.Clone())
	}
	return issues
}
