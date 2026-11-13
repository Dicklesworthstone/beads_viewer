package model

import (
	"sort"
	"time"
)

// DependencyState distinguishes proven readiness from incomplete dependency
// data. Both unsatisfied and unknown dependencies withhold ready work.
type DependencyState string

const (
	DependenciesSatisfied   DependencyState = "satisfied"
	DependenciesUnsatisfied DependencyState = "unsatisfied"
	DependenciesUnknown     DependencyState = "unknown"
)

// ReadinessIndex owns readiness data for the full source independently of display and
// ranking scopes. Construction is O(issues + edges); readiness lookups are O(1).
// It does not compute expensive graph metrics or mutate the caller's issues.
type ReadinessIndex struct {
	idToIdx         map[string]int
	ids             []string
	issues          []readinessIssue
	states          []DependencyState
	children        [][]int32
	missingChildren map[string][]int32
}

// Keep only decision inputs. Copy dependency values into a single slab across
// the index instead of cloning display text, comments and separately allocated edges.
type readinessIssue struct {
	Status        Status
	IssueType     IssueType
	Assignee      string
	DeferUntil    *time.Time
	Dependencies  []readinessDependency
	hasParentEdge bool
}

type readinessDependency struct {
	DependsOnID  string
	DependsOnIdx int32
	Type         DependencyType
}

func (issue readinessIssue) isDeferredAt(now time.Time) bool {
	return issue.DeferUntil != nil && issue.DeferUntil.After(now)
}

func NewReadinessIndex(issues []Issue) *ReadinessIndex {
	nInput := len(issues)
	idToIdx := make(map[string]int, nInput)
	ids := make([]string, 0, nInput)
	lastRow := make([]int, 0, nInput)
	for rowIdx := range issues {
		id := issues[rowIdx].ID
		if idx, exists := idToIdx[id]; exists {
			lastRow[idx] = rowIdx
		} else {
			idToIdx[id] = len(ids)
			ids = append(ids, id)
			lastRow = append(lastRow, rowIdx)
		}
	}

	n := len(ids)
	totalDeps := 0
	totalDefers := 0
	totalParentEdges := 0
	for idx := 0; idx < n; idx++ {
		src := &issues[lastRow[idx]]
		if src.DeferUntil != nil {
			totalDefers++
		}
		for _, dep := range src.Dependencies {
			if dep != nil {
				totalDeps++
				if dep.Type == DepParentChild {
					totalParentEdges++
				}
			}
		}
	}

	var depSlab []readinessDependency
	if totalDeps > 0 {
		depSlab = make([]readinessDependency, totalDeps)
	}
	var deferSlab []time.Time
	if totalDefers > 0 {
		deferSlab = make([]time.Time, totalDefers)
	}

	ownedIssues := make([]readinessIssue, n)
	var childDeg []int32
	if totalParentEdges > 0 {
		childDeg = make([]int32, n)
	}
	var missingChildren map[string][]int32

	depPos := 0
	deferPos := 0
	for idx := 0; idx < n; idx++ {
		src := &issues[lastRow[idx]]
		owned := readinessIssue{
			Status:    src.Status,
			IssueType: src.IssueType,
			Assignee:  src.Assignee,
		}
		if src.DeferUntil != nil {
			deferSlab[deferPos] = *src.DeferUntil
			owned.DeferUntil = &deferSlab[deferPos]
			deferPos++
		}
		if len(src.Dependencies) > 0 {
			start := depPos
			for _, dep := range src.Dependencies {
				if dep == nil {
					continue
				}
				targetIdx := int32(-1)
				if resolvedIdx, ok := idToIdx[dep.DependsOnID]; ok {
					targetIdx = int32(resolvedIdx)
				}
				depSlab[depPos] = readinessDependency{
					DependsOnID:  dep.DependsOnID,
					DependsOnIdx: targetIdx,
					Type:         dep.Type,
				}
				depPos++
				if dep.Type == DepParentChild {
					owned.hasParentEdge = true
					if targetIdx >= 0 {
						childDeg[targetIdx]++
					} else {
						if missingChildren == nil {
							missingChildren = make(map[string][]int32)
						}
						missingChildren[dep.DependsOnID] = append(missingChildren[dep.DependsOnID], int32(idx))
					}
				}
			}
			if depPos > start {
				owned.Dependencies = depSlab[start:depPos:depPos]
			}
		}
		ownedIssues[idx] = owned
	}

	children := make([][]int32, n)
	if totalParentEdges > 0 {
		knownParentEdges := 0
		for _, deg := range childDeg {
			knownParentEdges += int(deg)
		}
		if knownParentEdges > 0 {
			childSlab := make([]int32, knownParentEdges)
			offset := 0
			for idx, deg := range childDeg {
				if deg > 0 {
					end := offset + int(deg)
					children[idx] = childSlab[offset:offset:end]
					offset = end
				}
			}
		}
	}

	r := &ReadinessIndex{
		idToIdx:         idToIdx,
		ids:             ids,
		issues:          ownedIssues,
		states:          make([]DependencyState, n),
		children:        children,
		missingChildren: missingChildren,
	}
	r.compute()
	return r
}

func closedForReadiness(status Status) bool {
	return status == StatusClosed || status == StatusTombstone
}

// ResolvedIDs preserves prerequisite resolution when display/export filters
// omit closed issues or the loader removes tombstone rows.
func (r *ReadinessIndex) ResolvedIDs() []string {
	var ids []string
	if r == nil {
		return ids
	}
	for idx := range r.issues {
		if closedForReadiness(r.issues[idx].Status) {
			ids = append(ids, r.ids[idx])
		}
	}
	sort.Strings(ids)
	return ids
}

func combineDependencyState(a, b DependencyState) DependencyState {
	if a == DependenciesUnknown || b == DependenciesUnknown {
		return DependenciesUnknown
	}
	if a == DependenciesUnsatisfied || b == DependenciesUnsatisfied {
		return DependenciesUnsatisfied
	}
	return DependenciesSatisfied
}

func (r *ReadinessIndex) compute() {
	n := len(r.issues)
	if n == 0 {
		return
	}
	pending := make([]int32, n)
	for idx := 0; idx < n; idx++ {
		issue := &r.issues[idx]
		state := DependenciesSatisfied
		for _, dep := range issue.Dependencies {
			if dep.Type == DepParentChild && dep.DependsOnIdx >= 0 {
				r.children[dep.DependsOnIdx] = append(r.children[dep.DependsOnIdx], int32(idx))
			}
			if closedForReadiness(issue.Status) {
				continue
			}
			if dep.Type.IsBlocking() {
				switch {
				case dep.DependsOnIdx < 0:
					state = DependenciesUnknown
				case !closedForReadiness(r.issues[dep.DependsOnIdx].Status):
					state = combineDependencyState(state, DependenciesUnsatisfied)
				}
			} else if dep.Type == DepParentChild {
				if dep.DependsOnIdx < 0 {
					state = DependenciesUnknown
				} else if !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
					pending[idx]++
				}
			}
		}
		r.states[idx] = state
	}
	// Process parents before children. There is no arbitrary depth cutoff:
	// a blocked ancestor still gates a descendant fifty or ten thousand hops
	// away. A parent cycle (and its descendants) stays unresolved/unknown.
	queue := make([]int32, 0, n)
	for idx := 0; idx < n; idx++ {
		if !closedForReadiness(r.issues[idx].Status) && pending[idx] == 0 {
			queue = append(queue, int32(idx))
		}
	}
	for head := 0; head < len(queue); head++ {
		u := queue[head]
		parentState := r.states[u]
		for _, child := range r.children[u] {
			if closedForReadiness(r.issues[child].Status) {
				continue
			}
			r.states[child] = combineDependencyState(r.states[child], parentState)
			pending[child]--
			if pending[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	for idx, remaining := range pending {
		if remaining > 0 {
			r.states[idx] = DependenciesUnknown
		}
	}
}

func (r *ReadinessIndex) DependencyState(id string) DependencyState {
	if r == nil {
		return DependenciesUnknown
	}
	if idx, ok := r.idToIdx[id]; ok {
		return r.states[idx]
	}
	return DependenciesUnknown
}

// Ready includes ongoing work for planning. Claimable adds the stricter
// open/unassigned/non-epic/leaf policy used when suggesting a new claim.
func (r *ReadinessIndex) Ready(id string, now time.Time) bool {
	if r == nil {
		return false
	}
	idx, exists := r.idToIdx[id]
	if !exists {
		return false
	}
	issue := &r.issues[idx]
	return (issue.Status == StatusOpen || issue.Status == StatusInProgress) &&
		!issue.isDeferredAt(now) && r.states[idx] == DependenciesSatisfied
}

func (r *ReadinessIndex) Claimable(id string, now time.Time) bool {
	if r == nil {
		return false
	}
	idx, exists := r.idToIdx[id]
	if !exists {
		return false
	}
	issue := &r.issues[idx]
	return issue.Status == StatusOpen && !issue.isDeferredAt(now) &&
		r.states[idx] == DependenciesSatisfied &&
		issue.Assignee == "" && issue.IssueType != TypeEpic && !r.hasOpenChildrenAt(idx)
}

func (r *ReadinessIndex) hasOpenChildrenAt(idx int) bool {
	for _, child := range r.children[idx] {
		if !closedForReadiness(r.issues[child].Status) {
			return true
		}
	}
	return false
}

func (r *ReadinessIndex) HasOpenChildren(id string) bool {
	if r == nil {
		return false
	}
	if idx, ok := r.idToIdx[id]; ok {
		return r.hasOpenChildrenAt(idx)
	}
	for _, child := range r.missingChildren[id] {
		if !closedForReadiness(r.issues[child].Status) {
			return true
		}
	}
	return false
}

// Blockers includes unresolved references and parents whose dependencies are
// unsatisfied or unknown. These IDs explain why an item was withheld, even
// when the blocker is outside the visible scope or absent from the source.
func (r *ReadinessIndex) Blockers(id string) []string {
	if r == nil {
		return nil
	}
	idx, exists := r.idToIdx[id]
	if !exists {
		return nil
	}
	issue := &r.issues[idx]
	if closedForReadiness(issue.Status) || r.states[idx] == DependenciesSatisfied || len(issue.Dependencies) == 0 {
		return nil
	}
	ids := make([]string, 0, len(issue.Dependencies))
	for _, dep := range issue.Dependencies {
		if dep.Type.IsBlocking() {
			if dep.DependsOnIdx < 0 || !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
				ids = append(ids, dep.DependsOnID)
			}
		} else if dep.Type == DepParentChild {
			if dep.DependsOnIdx < 0 || r.states[dep.DependsOnIdx] != DependenciesSatisfied {
				ids = append(ids, dep.DependsOnID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 1 {
		sort.Strings(ids)
		write := 1
		for read := 1; read < len(ids); read++ {
			if ids[read] != ids[write-1] {
				ids[write] = ids[read]
				write++
			}
		}
		ids = ids[:write]
	}
	return ids
}

// ReadyAfterSingle evaluates ReadyAfter when simulating completion of a single
// issue without allocating a map per query.
func (r *ReadinessIndex) ReadyAfterSingle(id string, now time.Time, completedID string) bool {
	if completedID == "" {
		return r.Ready(id, now)
	}
	if r == nil || id == completedID {
		return false
	}
	idx, exists := r.idToIdx[id]
	if !exists {
		return false
	}
	issue := &r.issues[idx]
	if (issue.Status != StatusOpen && issue.Status != StatusInProgress) || issue.isDeferredAt(now) {
		return false
	}
	completedIdx := int32(-1)
	if cIdx, ok := r.idToIdx[completedID]; ok {
		completedIdx = int32(cIdx)
	}
	if completedIdx < 0 {
		return r.states[idx] == DependenciesSatisfied
	}
	if r.states[idx] == DependenciesSatisfied {
		return true
	}
	if !issue.hasParentEdge {
		for _, dep := range issue.Dependencies {
			if dep.Type.IsBlocking() {
				if dep.DependsOnIdx < 0 {
					return false
				}
				if dep.DependsOnIdx != completedIdx && !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
					return false
				}
			}
		}
		return true
	}

	var states map[int32]DependencyState
	var visiting map[int32]bool
	var visit func(int32) DependencyState
	visit = func(cur int32) DependencyState {
		if cur < 0 {
			return DependenciesUnknown
		}
		if cur == completedIdx || closedForReadiness(r.issues[cur].Status) || r.states[cur] == DependenciesSatisfied {
			return DependenciesSatisfied
		}
		if visiting[cur] {
			return DependenciesUnknown
		}
		if state, ok := states[cur]; ok {
			return state
		}
		if visiting == nil {
			visiting = make(map[int32]bool)
			states = make(map[int32]DependencyState)
		}
		visiting[cur] = true
		state := DependenciesSatisfied
		for _, dep := range r.issues[cur].Dependencies {
			if dep.Type.IsBlocking() {
				if dep.DependsOnIdx < 0 {
					state = DependenciesUnknown
				} else if dep.DependsOnIdx != completedIdx && !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
					state = combineDependencyState(state, DependenciesUnsatisfied)
				}
			} else if dep.Type == DepParentChild {
				state = combineDependencyState(state, visit(dep.DependsOnIdx))
			}
		}
		delete(visiting, cur)
		states[cur] = state
		return state
	}
	return visit(int32(idx)) == DependenciesSatisfied
}

// ReadyAfter evaluates a bounded what-if frontier against the same authority.
// Closed/completed parents stop inheritance; cycles and missing parents remain
// unknown. Memoization shares parent walks within this query.
func (r *ReadinessIndex) ReadyAfter(id string, now time.Time, completed map[string]bool) bool {
	if len(completed) == 0 {
		return r.Ready(id, now)
	}
	if r == nil || completed[id] {
		return false
	}
	idx, exists := r.idToIdx[id]
	if !exists {
		return false
	}
	issue := &r.issues[idx]
	if (issue.Status != StatusOpen && issue.Status != StatusInProgress) || issue.isDeferredAt(now) {
		return false
	}
	if r.states[idx] == DependenciesSatisfied {
		return true
	}
	if !issue.hasParentEdge {
		for _, dep := range issue.Dependencies {
			if dep.Type.IsBlocking() {
				if dep.DependsOnIdx < 0 {
					return false
				}
				if !completed[dep.DependsOnID] && !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
					return false
				}
			}
		}
		return true
	}

	var states map[int32]DependencyState
	var visiting map[int32]bool
	var visit func(int32) DependencyState
	visit = func(cur int32) DependencyState {
		if cur < 0 {
			return DependenciesUnknown
		}
		if completed[r.ids[cur]] || closedForReadiness(r.issues[cur].Status) || r.states[cur] == DependenciesSatisfied {
			return DependenciesSatisfied
		}
		if visiting[cur] {
			return DependenciesUnknown
		}
		if state, ok := states[cur]; ok {
			return state
		}
		if visiting == nil {
			visiting = make(map[int32]bool)
			states = make(map[int32]DependencyState)
		}
		visiting[cur] = true
		state := DependenciesSatisfied
		for _, dep := range r.issues[cur].Dependencies {
			if dep.Type.IsBlocking() {
				if dep.DependsOnIdx < 0 {
					state = DependenciesUnknown
				} else if !completed[dep.DependsOnID] && !closedForReadiness(r.issues[dep.DependsOnIdx].Status) {
					state = combineDependencyState(state, DependenciesUnsatisfied)
				}
			} else if dep.Type == DepParentChild {
				state = combineDependencyState(state, visit(dep.DependsOnIdx))
			}
		}
		delete(visiting, cur)
		states[cur] = state
		return state
	}
	return visit(int32(idx)) == DependenciesSatisfied
}
