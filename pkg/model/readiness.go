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
	issues   map[string]readinessIssue
	states   map[string]DependencyState
	children map[string][]string
}

// Keep only decision inputs. Copy dependency values into one slice per issue
// instead of cloning display text, comments and separately allocated edges.
type readinessIssue struct {
	Status       Status
	IssueType    IssueType
	Assignee     string
	DeferUntil   *time.Time
	Dependencies []readinessDependency
}

type readinessDependency struct {
	DependsOnID string
	Type        DependencyType
}

func (issue readinessIssue) isDeferredAt(now time.Time) bool {
	return issue.DeferUntil != nil && issue.DeferUntil.After(now)
}

func NewReadinessIndex(issues []Issue) *ReadinessIndex {
	r := &ReadinessIndex{
		issues:   make(map[string]readinessIssue, len(issues)),
		states:   make(map[string]DependencyState, len(issues)),
		children: make(map[string][]string),
	}
	for _, issue := range issues {
		owned := readinessIssue{
			Status:    issue.Status,
			IssueType: issue.IssueType,
			Assignee:  issue.Assignee,
		}
		if issue.DeferUntil != nil {
			deferUntil := *issue.DeferUntil
			owned.DeferUntil = &deferUntil
		}
		if len(issue.Dependencies) > 0 {
			owned.Dependencies = make([]readinessDependency, 0, len(issue.Dependencies))
			for _, dep := range issue.Dependencies {
				if dep != nil {
					owned.Dependencies = append(owned.Dependencies, readinessDependency{
						DependsOnID: dep.DependsOnID,
						Type:        dep.Type,
					})
				}
			}
		}
		r.issues[issue.ID] = owned
	}
	r.compute()
	return r
}

func closedForReadiness(status Status) bool {
	return status == StatusClosed || status == StatusTombstone
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
	pending := make(map[string]int, len(r.issues))
	for id, issue := range r.issues {
		r.states[id] = DependenciesSatisfied
		for _, dep := range issue.Dependencies {
			if dep.Type == DepParentChild {
				r.children[dep.DependsOnID] = append(r.children[dep.DependsOnID], id)
			}
			if closedForReadiness(issue.Status) {
				continue
			}
			other, exists := r.issues[dep.DependsOnID]
			if dep.Type.IsBlocking() {
				switch {
				case !exists:
					r.states[id] = DependenciesUnknown
				case !closedForReadiness(other.Status):
					r.states[id] = combineDependencyState(r.states[id], DependenciesUnsatisfied)
				}
			} else if dep.Type == DepParentChild {
				if !exists {
					r.states[id] = DependenciesUnknown
				} else if !closedForReadiness(other.Status) {
					pending[id]++
				}
			}
		}
	}
	// Process parents before children. There is no arbitrary depth cutoff:
	// a blocked ancestor still gates a descendant fifty or ten thousand hops
	// away. A parent cycle (and its descendants) stays unresolved/unknown.
	queue := make([]string, 0, len(r.issues))
	for id, issue := range r.issues {
		if !closedForReadiness(issue.Status) && pending[id] == 0 {
			queue = append(queue, id)
		}
	}
	for head := 0; head < len(queue); head++ {
		id := queue[head]
		for _, child := range r.children[id] {
			if closedForReadiness(r.issues[child].Status) {
				continue
			}
			r.states[child] = combineDependencyState(r.states[child], r.states[id])
			pending[child]--
			if pending[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	for id, remaining := range pending {
		if remaining > 0 {
			r.states[id] = DependenciesUnknown
		}
	}
}

func (r *ReadinessIndex) DependencyState(id string) DependencyState {
	if state, ok := r.states[id]; ok {
		return state
	}
	return DependenciesUnknown
}

// Ready includes ongoing work for planning. Claimable adds the stricter
// open/unassigned/non-epic/leaf policy used when suggesting a new claim.
func (r *ReadinessIndex) Ready(id string, now time.Time) bool {
	issue, exists := r.issues[id]
	return exists && (issue.Status == StatusOpen || issue.Status == StatusInProgress) &&
		!issue.isDeferredAt(now) && r.DependencyState(id) == DependenciesSatisfied
}

func (r *ReadinessIndex) Claimable(id string, now time.Time) bool {
	issue, exists := r.issues[id]
	return exists && r.Ready(id, now) && issue.Status == StatusOpen &&
		issue.Assignee == "" && issue.IssueType != TypeEpic && !r.HasOpenChildren(id)
}

func (r *ReadinessIndex) HasOpenChildren(id string) bool {
	for _, child := range r.children[id] {
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
	issue, exists := r.issues[id]
	if !exists || closedForReadiness(issue.Status) {
		return nil
	}
	set := make(map[string]bool)
	for _, dep := range issue.Dependencies {
		other, exists := r.issues[dep.DependsOnID]
		if dep.Type.IsBlocking() && (!exists || !closedForReadiness(other.Status)) ||
			dep.Type == DepParentChild && r.DependencyState(dep.DependsOnID) != DependenciesSatisfied {
			set[dep.DependsOnID] = true
		}
	}
	var ids []string
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ReadyAfter evaluates a bounded what-if frontier against the same authority.
// Closed/completed parents stop inheritance; cycles and missing parents remain
// unknown. Memoization shares parent walks within this query.
func (r *ReadinessIndex) ReadyAfter(id string, now time.Time, completed map[string]bool) bool {
	if len(completed) == 0 {
		return r.Ready(id, now)
	}
	issue, exists := r.issues[id]
	if !exists || completed[id] || (issue.Status != StatusOpen && issue.Status != StatusInProgress) || issue.isDeferredAt(now) {
		return false
	}
	states := make(map[string]DependencyState)
	visiting := make(map[string]bool)
	var visit func(string) DependencyState
	visit = func(id string) DependencyState {
		issue, exists := r.issues[id]
		if !exists || visiting[id] {
			return DependenciesUnknown
		}
		if completed[id] || closedForReadiness(issue.Status) {
			return DependenciesSatisfied
		}
		if state, ok := states[id]; ok {
			return state
		}
		visiting[id] = true
		state := DependenciesSatisfied
		for _, dep := range issue.Dependencies {
			if dep.Type.IsBlocking() {
				other, exists := r.issues[dep.DependsOnID]
				if !exists {
					state = DependenciesUnknown
				} else if !completed[dep.DependsOnID] && !closedForReadiness(other.Status) {
					state = combineDependencyState(state, DependenciesUnsatisfied)
				}
			} else if dep.Type == DepParentChild {
				state = combineDependencyState(state, visit(dep.DependsOnID))
			}
		}
		delete(visiting, id)
		states[id] = state
		return state
	}
	return visit(id) == DependenciesSatisfied
}
