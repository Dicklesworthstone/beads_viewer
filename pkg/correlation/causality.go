// Package correlation provides temporal causality analysis for beads.
package correlation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// CausalHistory stores only the target's constraints at each committed change.
// Each observation is derived from the complete file, including unchanged and
// out-of-scope blockers. Order is Git first-parent order, never author-date sort.
type CausalHistory struct {
	BeadID               string              `json:"bead_id"`
	Observations         []CausalObservation `json:"observations"`
	Until                *time.Time          `json:"until,omitempty"`
	Revision             string              `json:"revision,omitempty"`
	ReferenceTime        *time.Time          `json:"reference_time,omitempty"`
	ReferenceCommittedAt *time.Time          `json:"reference_committed_at,omitempty"`
}

type CausalObservation struct {
	CommitSHA   string      `json:"commit_sha"`
	Timestamp   time.Time   `json:"timestamp"`
	CommittedAt time.Time   `json:"committed_at"`
	Before      CausalState `json:"before"`
	After       CausalState `json:"after"`
	Changes     []BeadEvent `json:"changes"` // Target and relevant blocking/parent records only
}

type CausalState struct {
	Issue           *HistoricalIssueState `json:"issue"`
	Known           bool                  `json:"known"`
	DependencyState model.DependencyState `json:"dependency_state"`
	Blockers        []string              `json:"blockers"`
	Reason          string                `json:"reason,omitempty"`
}

// CausalEventType categorizes events in the causal chain
type CausalEventType string

const (
	// CausalCreated indicates the bead was created
	CausalCreated CausalEventType = "created"
	// CausalClaimed indicates the bead was claimed (status -> in_progress)
	CausalClaimed CausalEventType = "claimed"
	// CausalCommit indicates a code commit related to the bead
	CausalCommit CausalEventType = "commit"
	// CausalBlocked indicates the bead became blocked
	CausalBlocked CausalEventType = "blocked"
	// CausalUnblocked indicates the bead was unblocked
	CausalUnblocked CausalEventType = "unblocked"
	// CausalClosed indicates the bead was closed
	CausalClosed CausalEventType = "closed"
	// CausalReopened indicates the bead was reopened
	CausalReopened   CausalEventType = "reopened"
	CausalChanged    CausalEventType = "changed"
	CausalDeleted    CausalEventType = "deleted"
	CausalConstraint CausalEventType = "constraint_change"
)

// CausalEvent represents a single event in the causal chain
type CausalEvent struct {
	ID                 int                   `json:"id"`                      // Unique within chain
	Type               CausalEventType       `json:"type"`                    // Event type
	Timestamp          time.Time             `json:"timestamp"`               // When it happened
	Description        string                `json:"description"`             // Human-readable description
	CommitSHA          string                `json:"commit_sha,omitempty"`    // For commit events
	BlockerID          string                `json:"blocker_id,omitempty"`    // For blocked/unblocked events
	CausedByID         *int                  `json:"caused_by_id,omitempty"`  // ID of event that caused this
	EnablesIDs         []int                 `json:"enables_ids,omitempty"`   // IDs of events this enables
	DurationNext       *time.Duration        `json:"duration_next,omitempty"` // Time until next event
	SourceBeadID       string                `json:"source_bead_id,omitempty"`
	CommittedAt        *time.Time            `json:"committed_at,omitempty"`
	Before             *HistoricalIssueState `json:"before,omitempty"`
	After              *HistoricalIssueState `json:"after,omitempty"`
	TransitionObserved bool                  `json:"transition_observed"`
}

// CausalLink relates observed constraints. Chronological proximity and commit
// correlation alone never create a link. Duration is an observed wait, not an
// estimate of task execution time or a counterfactual schedule.
type CausalLink struct {
	From     int           `json:"from"`
	To       int           `json:"to"`
	Kind     string        `json:"kind"`
	Evidence string        `json:"evidence"`
	Duration time.Duration `json:"duration"`
}

// CausalChain represents the full causal flow for a bead
type CausalChain struct {
	BeadID         string             `json:"bead_id"`
	Title          string             `json:"title"`
	Status         string             `json:"status"`
	Events         []CausalEvent      `json:"events"`      // Recorded transitions in Git order; correlations are separate
	EdgeCount      int                `json:"edge_count"`  // Number of causal links
	StartTime      time.Time          `json:"start_time"`  // First event time
	EndTime        time.Time          `json:"end_time"`    // Last event time (or now if open)
	TotalTime      time.Duration      `json:"total_time"`  // Total elapsed time
	IsComplete     bool               `json:"is_complete"` // True if bead is closed
	Links          []CausalLink       `json:"links"`
	RelatedCommits []CorrelatedCommit `json:"related_commits,omitempty"` // Correlation is not causal evidence
	DurationKnown  bool               `json:"duration_known"`
	TimeBasis      string             `json:"time_basis"`
}

func (c CausalChain) MarshalJSON() ([]byte, error) {
	type fields CausalChain
	var duration *time.Duration
	if c.DurationKnown {
		duration = &c.TotalTime
	}
	return json.Marshal(struct {
		fields
		Duration *time.Duration `json:"total_time"`
	}{fields(c), duration})
}

// BlockedPeriod represents a contiguous period when the bead was blocked
type BlockedPeriod struct {
	StartTime     time.Time     `json:"start_time"`
	EndTime       time.Time     `json:"end_time"`
	Duration      time.Duration `json:"duration"`
	BlockerID     string        `json:"blocker_id,omitempty"` // What blocked it
	BlockerIDs    []string      `json:"blocker_ids,omitempty"`
	Kind          string        `json:"kind"` // explicit_status, dependency, or union
	Ongoing       bool          `json:"ongoing"`
	StartObserved bool          `json:"start_observed"`
}

// CausalInsights contains derived analysis from the causal chain
type CausalInsights struct {
	TotalDuration             time.Duration   `json:"total_duration"`     // Total time from create to close/now
	BlockedDuration           time.Duration   `json:"blocked_duration"`   // Total time spent blocked
	ActiveDuration            time.Duration   `json:"active_duration"`    // Nonblocked elapsed, not execution effort
	BlockedPercentage         float64         `json:"blocked_percentage"` // % of time blocked
	BlockedPeriods            []BlockedPeriod `json:"blocked_periods"`    // Each blocked period
	CriticalPath              []int           `json:"critical_path"`      // Event IDs on critical path
	CriticalPathDesc          string          `json:"critical_path_desc"` // Human-readable critical path
	CommitCount               int             `json:"commit_count"`       // Number of commits
	AvgTimeBetween            *time.Duration  `json:"avg_time_between"`   // Avg time between events
	LongestGap                *time.Duration  `json:"longest_gap"`        // Longest gap between events
	LongestGapDesc            string          `json:"longest_gap_desc"`   // Description of longest gap
	EstimatedWithout          *time.Duration  `json:"estimated_without"`  // Est. time without blocks
	Summary                   string          `json:"summary"`            // One-line summary
	Recommendations           []string        `json:"recommendations"`    // Actionable insights
	Coverage                  string          `json:"coverage"`           // complete, partial, inconsistent, unavailable
	Limitations               []string        `json:"limitations"`
	DurationKnown             bool            `json:"duration_known"`
	BlockedDurationKnown      bool            `json:"blocked_duration_known"`
	ExplicitBlockedDuration   time.Duration   `json:"explicit_blocked_duration"`
	DependencyWaitDuration    time.Duration   `json:"dependency_wait_duration"`
	CriticalPathDuration      time.Duration   `json:"critical_path_duration"`
	CriticalPathDurationKnown bool            `json:"critical_path_duration_known"`
	ExplicitBlockedPeriods    []BlockedPeriod `json:"explicit_blocked_periods"`
	DependencyWaitPeriods     []BlockedPeriod `json:"dependency_wait_periods"`
	ExplicitDurationKnown     bool            `json:"explicit_duration_known"`
	DependencyDurationKnown   bool            `json:"dependency_duration_known"`
}

// Unknown measurements are null, not a fabricated zero. The Go fields retain
// the observed-window sums so internal callers can inspect partial evidence.
func (i CausalInsights) MarshalJSON() ([]byte, error) {
	type fields CausalInsights
	var total, blocked, active, explicit, dependency, critical *time.Duration
	var percentage *float64
	if i.DurationKnown {
		total = &i.TotalDuration
	}
	if i.BlockedDurationKnown {
		blocked = &i.BlockedDuration
	}
	if i.ExplicitDurationKnown {
		explicit = &i.ExplicitBlockedDuration
	}
	if i.DependencyDurationKnown {
		dependency = &i.DependencyWaitDuration
	}
	if i.CriticalPathDurationKnown {
		critical = &i.CriticalPathDuration
	}
	if i.DurationKnown && i.BlockedDurationKnown {
		active, percentage = &i.ActiveDuration, &i.BlockedPercentage
	}
	return json.Marshal(struct {
		fields
		Total      *time.Duration `json:"total_duration"`
		Blocked    *time.Duration `json:"blocked_duration"`
		Active     *time.Duration `json:"active_duration"`
		Explicit   *time.Duration `json:"explicit_blocked_duration"`
		Dependency *time.Duration `json:"dependency_wait_duration"`
		Percentage *float64       `json:"blocked_percentage"`
		Critical   *time.Duration `json:"critical_path_duration"`
	}{fields(i), total, blocked, active, explicit, dependency, percentage, critical})
}

// CausalityResult is the top-level output for --robot-causality
type CausalityResult struct {
	GeneratedAt time.Time       `json:"generated_at"`
	DataHash    string          `json:"data_hash"`
	Chain       *CausalChain    `json:"chain"`
	Insights    *CausalInsights `json:"insights"`
}

// CausalityOptions configures causality analysis
type CausalityOptions struct {
	IncludeCommits bool              // Include commit events in chain (default true)
	BlockerTitles  map[string]string // BeadID -> Title for blocker descriptions
}

// DefaultCausalityOptions returns sensible defaults
func DefaultCausalityOptions() CausalityOptions {
	return CausalityOptions{
		IncludeCommits: true,
	}
}

// BuildCausalityChain constructs the causal chain for a bead
func (hr *HistoryReport) BuildCausalityChain(beadID string, opts CausalityOptions) *CausalityResult {
	return hr.BuildCausalityChainAt(beadID, opts, time.Now())
}

// BuildCausalityChainAt constructs the causal chain using a caller-owned
// reference instant for open chains and serialized result metadata. The zero
// instant is valid; open-chain duration is clamped rather than consulting the
// wall clock when the reference instant predates the first event.
func (hr *HistoryReport) BuildCausalityChainAt(beadID string, opts CausalityOptions, now time.Time) *CausalityResult {
	history, exists := hr.Histories[beadID]
	if !exists {
		return nil
	}
	if hr.CausalHistory != nil && hr.CausalHistory.BeadID == beadID {
		return hr.buildRecordedCausality(history, opts, now)
	}

	chain := &CausalChain{
		BeadID:     beadID,
		Title:      history.Title,
		Status:     history.Status,
		Events:     []CausalEvent{},
		IsComplete: isCompleteCausalStatus(history.Status),
		TimeBasis:  "author timestamps; chronology only",
	}

	// Collect all events with their timestamps
	type rawEvent struct {
		timestamp   time.Time
		eventType   CausalEventType
		description string
		commitSHA   string
		blockerID   string
	}
	var rawEvents []rawEvent

	// Add lifecycle events
	for _, event := range history.Events {
		var causalType CausalEventType
		var desc string

		switch event.EventType {
		case EventCreated:
			causalType = CausalCreated
			desc = "Bead created"
		case EventClaimed:
			causalType = CausalClaimed
			desc = "Work started (claimed)"
		case EventClosed:
			causalType = CausalClosed
			desc = "Work completed (closed)"
		case EventReopened:
			causalType = CausalReopened
			desc = "Bead reopened"
		case EventDeleted:
			causalType = CausalDeleted
			desc = "Bead removed from the historical source"
		default:
			continue // Skip modified events for now
		}

		rawEvents = append(rawEvents, rawEvent{
			timestamp:   event.Timestamp,
			eventType:   causalType,
			description: desc,
		})
	}

	// Add commit events if requested
	if opts.IncludeCommits {
		for _, commit := range history.Commits {
			desc := commit.Message
			// Truncate by runes (not bytes) to avoid splitting multi-byte characters
			runes := []rune(desc)
			if len(runes) > 50 {
				desc = string(runes[:47]) + "..."
			}
			rawEvents = append(rawEvents, rawEvent{
				timestamp:   commit.Timestamp,
				eventType:   CausalCommit,
				description: "Commit: " + desc,
				commitSHA:   commit.ShortSHA,
			})
		}
	}

	// Sort by timestamp
	sort.Slice(rawEvents, func(i, j int) bool {
		if !rawEvents[i].timestamp.Equal(rawEvents[j].timestamp) {
			return rawEvents[i].timestamp.Before(rawEvents[j].timestamp)
		}
		leftOrder := causalEventOrder(rawEvents[i].eventType)
		rightOrder := causalEventOrder(rawEvents[j].eventType)
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if rawEvents[i].commitSHA != rawEvents[j].commitSHA {
			return rawEvents[i].commitSHA < rawEvents[j].commitSHA
		}
		return rawEvents[i].description < rawEvents[j].description
	})

	// Without committed dependency observations these are a chronology only.
	for i, raw := range rawEvents {
		event := CausalEvent{
			ID:          i,
			Type:        raw.eventType,
			Timestamp:   raw.timestamp,
			Description: raw.description,
			CommitSHA:   raw.commitSHA,
			BlockerID:   raw.blockerID,
		}

		chain.Events = append(chain.Events, event)
	}

	// Set chain metadata
	if len(chain.Events) > 0 {
		chain.StartTime = chain.Events[0].Timestamp
		chain.EndTime = chain.Events[len(chain.Events)-1].Timestamp
		if !chain.IsComplete {
			chain.EndTime = now
			if chain.EndTime.Before(chain.StartTime) {
				chain.EndTime = chain.StartTime
			}
		}
		chain.TotalTime = chain.EndTime.Sub(chain.StartTime)
		chain.DurationKnown = chain.Events[0].Type == CausalCreated
	}

	// Count edges
	for _, event := range chain.Events {
		chain.EdgeCount += len(event.EnablesIDs)
	}

	// Build insights
	insights := buildInsights(chain, history)
	insights.DurationKnown = chain.DurationKnown
	insights.Coverage = "unavailable"
	insights.Limitations = []string{"Committed dependency snapshots were not retained; chronology does not establish blocking or causation."}
	insights.Summary = "Lifecycle chronology only; blocking duration is unavailable"
	insights.Recommendations = append([]string(nil), insights.Limitations...)

	return &CausalityResult{
		GeneratedAt: now,
		DataHash:    hr.DataHash,
		Chain:       chain,
		Insights:    insights,
	}
}

func isCompleteCausalStatus(status string) bool {
	normalized := normalizeStatus(status)
	return normalized == "closed" || normalized == "tombstone"
}

func causalStateWait(s CausalState) (explicit, dependency, known bool) {
	if s.Issue == nil || !s.Known {
		return false, false, false
	}
	if isCompleteCausalStatus(s.Issue.Status) {
		return false, false, true
	}
	return normalizeLifecycleStatus(s.Issue.Status) == "blocked", s.DependencyState == model.DependenciesUnsatisfied, s.DependencyState != model.DependenciesUnknown
}

func sameCausalConstraints(a, b CausalState) bool {
	if a.Known != b.Known || a.DependencyState != b.DependencyState || strings.Join(a.Blockers, "\x00") != strings.Join(b.Blockers, "\x00") {
		return false
	}
	if a.Issue == nil || b.Issue == nil {
		return a.Issue == nil && b.Issue == nil
	}
	return a.Issue.Status == b.Issue.Status
}

func appendBlockedPeriod(periods []BlockedPeriod, start, end time.Time, blockers []string, kind string, observed, ongoing bool) []BlockedPeriod {
	if end.Before(start) {
		return periods
	}
	if len(periods) > 0 {
		last := &periods[len(periods)-1]
		if last.EndTime.Equal(start) && strings.Join(last.BlockerIDs, "\x00") == strings.Join(blockers, "\x00") {
			last.EndTime = end
			last.Duration += end.Sub(start)
			last.Ongoing = ongoing
			return periods
		}
	}
	p := BlockedPeriod{StartTime: start, EndTime: end, Duration: end.Sub(start), BlockerIDs: append([]string(nil), blockers...), Kind: kind, StartObserved: observed, Ongoing: ongoing}
	if len(blockers) == 1 {
		p.BlockerID = blockers[0]
	}
	return append(periods, p)
}

func (hr *HistoryReport) buildRecordedCausality(history BeadHistory, opts CausalityOptions, now time.Time) *CausalityResult {
	observations := hr.CausalHistory.Observations
	chain := &CausalChain{BeadID: history.BeadID, Title: history.Title, Status: history.Status, IsComplete: isCompleteCausalStatus(history.Status), Events: []CausalEvent{}, Links: []CausalLink{}}
	chain.TimeBasis = "author timestamps of committed snapshots, in Git first-parent order; intervals are observed history, not execution effort"
	insights := &CausalInsights{Coverage: "complete", DurationKnown: true, BlockedDurationKnown: true, ExplicitDurationKnown: true, DependencyDurationKnown: true, BlockedPeriods: []BlockedPeriod{}, ExplicitBlockedPeriods: []BlockedPeriod{}, DependencyWaitPeriods: []BlockedPeriod{}, CriticalPath: []int{}, Limitations: []string{}}
	result := &CausalityResult{GeneratedAt: now, DataHash: hr.DataHash, Chain: chain, Insights: insights}
	if opts.IncludeCommits {
		chain.RelatedCommits = history.Commits
		insights.CommitCount = len(history.Commits)
	}
	limit := func(reason string) {
		for _, existing := range insights.Limitations {
			if existing == reason {
				return
			}
		}
		insights.Limitations = append(insights.Limitations, reason)
		if insights.Coverage == "complete" {
			insights.Coverage = "partial"
		}
	}
	add := func(event CausalEvent) int {
		event.ID = len(chain.Events)
		chain.Events = append(chain.Events, event)
		return event.ID
	}
	link := func(from, to int, kind, evidence string, duration time.Duration) {
		if from < 0 || to <= from {
			return
		}
		chain.Links = append(chain.Links, CausalLink{From: from, To: to, Kind: kind, Evidence: evidence, Duration: duration})
		chain.Events[from].EnablesIDs = appendUniqueInt(chain.Events[from].EnablesIDs, to)
		if chain.Events[to].CausedByID == nil {
			id := from
			chain.Events[to].CausedByID = &id
		}
	}
	boundaries := make([]int, len(observations))
	first := -1
	clocksValid := true
	for i, obs := range observations {
		boundaries[i] = -1
		if i > 0 && (obs.Timestamp.Before(observations[i-1].Timestamp) || obs.CommittedAt.Before(observations[i-1].CommittedAt)) {
			clocksValid = false
			limit("Git transition order contradicts author or committer clocks; elapsed measurements are unavailable.")
			insights.Coverage = "inconsistent"
		}
		if obs.After.Issue != nil && first < 0 {
			first = i
			chain.StartTime = obs.Timestamp
			if obs.Before.Issue != nil || !obs.Before.Known {
				insights.DurationKnown = false
				insights.BlockedDurationKnown = false
				insights.ExplicitDurationKnown = false
				insights.DependencyDurationKnown = false
				limit("Creation predates the retained window or the initial source is incomplete.")
			}
		}
		var causes []int
		for _, event := range obs.Changes {
			t := CausalConstraint
			desc := "Dependency record changed: " + event.BeadID
			title := opts.BlockerTitles[event.BeadID]
			if event.After != nil && event.After.Title != "" {
				title = event.After.Title
			} else if event.Before != nil && event.Before.Title != "" {
				title = event.Before.Title
			}
			if title != "" {
				desc += " (" + title + ")"
			}
			if event.BeadID == history.BeadID {
				t = CausalChanged
				desc = "Bead record changed"
				switch event.EventType {
				case EventCreated:
					t = CausalCreated
					desc = "Bead first recorded"
				case EventClaimed:
					t = CausalClaimed
					desc = "Work claimed"
				case EventClosed:
					t = CausalClosed
					desc = "Bead closed"
				case EventReopened:
					t = CausalReopened
					desc = "Bead reopened"
				case EventDeleted:
					t = CausalDeleted
					desc = "Bead removed from the historical source"
				}
				if event.After != nil && normalizeLifecycleStatus(event.After.Status) == "blocked" && (event.Before == nil || normalizeLifecycleStatus(event.Before.Status) != "blocked") {
					t = CausalBlocked
					desc = "Explicit blocked status recorded"
				}
				if event.Before != nil && normalizeLifecycleStatus(event.Before.Status) == "blocked" && event.After != nil && !isCompleteCausalStatus(event.After.Status) && normalizeLifecycleStatus(event.After.Status) != "blocked" {
					t = CausalUnblocked
					desc = "Explicit blocked status cleared"
				}
			}
			if !event.TransitionObserved {
				desc = "Incomplete source: record evidence for " + event.BeadID + "; transition is uncertain"
			}
			id := add(CausalEvent{Type: t, Timestamp: obs.Timestamp, CommittedAt: &obs.CommittedAt, Description: desc, CommitSHA: obs.CommitSHA, SourceBeadID: event.BeadID, Before: event.Before, After: event.After, TransitionObserved: event.TransitionObserved})
			if event.BeadID == history.BeadID {
				boundaries[i] = id
			} else {
				causes = append(causes, id)
			}
		}
		gateChanged := !sameCausalConstraints(obs.Before, obs.After)
		// Dependency transitions can occur while the target record is unchanged.
		// Put the target consequence after all source changes in the same commit.
		if gateChanged && (boundaries[i] < 0 || len(causes) > 0) {
			exp, dep, known := causalStateWait(obs.After)
			t := CausalConstraint
			desc := "Dependency state became unknown"
			if known {
				if exp || dep {
					t = CausalBlocked
					desc = "Recorded constraints prevent readiness"
				} else {
					t = CausalUnblocked
					desc = "Recorded constraints no longer prevent readiness"
				}
			}
			boundaries[i] = add(CausalEvent{Type: t, Timestamp: obs.Timestamp, CommittedAt: &obs.CommittedAt, Description: desc, CommitSHA: obs.CommitSHA, SourceBeadID: history.BeadID, TransitionObserved: obs.Before.Known && obs.After.Known})
		}
		// A lifecycle change or creation alone is not evidence that another
		// record caused it. Require an actual, known dependency gate transition
		// while the target remains nonterminal on both sides.
		dependencyChanged := obs.Before.Issue != nil && obs.After.Issue != nil &&
			!isCompleteCausalStatus(obs.Before.Issue.Status) && !isCompleteCausalStatus(obs.After.Issue.Status) &&
			obs.Before.DependencyState != obs.After.DependencyState &&
			obs.Before.DependencyState != model.DependenciesUnknown && obs.After.DependencyState != model.DependenciesUnknown
		if dependencyChanged && obs.Before.Known && obs.After.Known {
			for _, cause := range causes {
				e := chain.Events[cause]
				if e.Before != nil && e.After != nil && isCompleteCausalStatus(e.Before.Status) == isCompleteCausalStatus(e.After.Status) && equalHistoricalDependencies(e.Before.Dependencies, e.After.Dependencies) {
					continue
				}
				link(cause, boundaries[i], "dependency_transition", "This committed dependency-state change accompanies the target gate transition; simultaneous changes are joint evidence, not isolated causal estimates.", 0)
			}
		}
	}
	if first < 0 {
		insights.Coverage = "unavailable"
		insights.DurationKnown = false
		insights.BlockedDurationKnown = false
		insights.ExplicitDurationKnown = false
		insights.DependencyDurationKnown = false
		limit("No retained committed state for this bead.")
	} else {
		last := observations[len(observations)-1]
		chain.EndTime = now
		if hr.CausalHistory.ReferenceTime != nil {
			chain.EndTime = *hr.CausalHistory.ReferenceTime
			if chain.EndTime.Before(last.Timestamp) || hr.CausalHistory.ReferenceCommittedAt == nil || hr.CausalHistory.ReferenceCommittedAt.Before(last.CommittedAt) {
				clocksValid = false
				limit("The requested revision's clocks contradict the retained transition order; elapsed measurements are unavailable.")
				insights.Coverage = "inconsistent"
			}
		}
		if hr.CausalHistory.Until != nil && chain.EndTime.After(*hr.CausalHistory.Until) {
			chain.EndTime = *hr.CausalHistory.Until
			limit("The history ends at the requested cutoff; later state is not observed.")
		}
		if last.After.Issue == nil || normalizeLifecycleStatus(last.After.Issue.Status) != normalizeLifecycleStatus(history.Status) {
			insights.BlockedDurationKnown = false
			insights.DurationKnown = false
			insights.ExplicitDurationKnown = false
			insights.DependencyDurationKnown = false
			limit("Current status differs from the retained committed state.")
		}
		if last.After.Issue != nil && isCompleteCausalStatus(last.After.Issue.Status) {
			for i := len(observations) - 1; i >= first; i-- {
				obs := observations[i]
				if obs.After.Issue != nil && isCompleteCausalStatus(obs.After.Issue.Status) && (obs.Before.Issue == nil || !isCompleteCausalStatus(obs.Before.Issue.Status)) {
					chain.EndTime = obs.Timestamp
					break
				}
			}
		}
		if chain.EndTime.Before(chain.StartTime) {
			clocksValid = false
			limit("The reference clock precedes the observed lifecycle.")
			insights.Coverage = "inconsistent"
		}
		waitStartID := -1
		var waitStart time.Time
		for i := first; i < len(observations); i++ {
			obs := observations[i]
			exp, dep, known := causalStateWait(obs.After)
			if !known {
				insights.DependencyDurationKnown = false
				if !exp {
					insights.BlockedDurationKnown = false
				}
				if !obs.After.Known || obs.After.Issue == nil {
					insights.ExplicitDurationKnown = false
				}
				limit("Some dependency intervals are unknown because records are missing, invalid, deleted, or cyclic through parents.")
			}
			if i > first && !sameCausalConstraints(observations[i-1].After, obs.Before) {
				insights.BlockedDurationKnown = false
				insights.ExplicitDurationKnown = false
				insights.DependencyDurationKnown = false
				limit("The retained snapshots have a discontinuity.")
			}
			blocked := exp || dep
			if blocked && waitStartID < 0 {
				waitStartID = boundaries[i]
				waitStart = obs.Timestamp
			}
			if !blocked && waitStartID >= 0 {
				if known && clocksValid {
					link(waitStartID, boundaries[i], "observed_wait", "Recorded blocked interval ended at this observed state transition.", obs.Timestamp.Sub(waitStart))
				}
				waitStartID = -1
			}
			end := chain.EndTime
			if i+1 < len(observations) && observations[i+1].Timestamp.Before(end) {
				end = observations[i+1].Timestamp
			}
			if !clocksValid || end.Before(obs.Timestamp) || obs.After.Issue == nil {
				continue
			}
			if isCompleteCausalStatus(obs.After.Issue.Status) {
				continue
			}
			ongoing := i == len(observations)-1 && !chain.IsComplete
			startObserved := obs.Before.Issue == nil || func() bool { e, d, k := causalStateWait(obs.Before); return k && !e && !d }()
			if blocked {
				insights.BlockedPeriods = appendBlockedPeriod(insights.BlockedPeriods, obs.Timestamp, end, obs.After.Blockers, "union", startObserved, ongoing)
				insights.BlockedDuration += end.Sub(obs.Timestamp)
			}
			if exp {
				insights.ExplicitBlockedPeriods = appendBlockedPeriod(insights.ExplicitBlockedPeriods, obs.Timestamp, end, nil, "explicit_status", startObserved, ongoing)
				insights.ExplicitBlockedDuration += end.Sub(obs.Timestamp)
			}
			if dep {
				insights.DependencyWaitPeriods = appendBlockedPeriod(insights.DependencyWaitPeriods, obs.Timestamp, end, obs.After.Blockers, "dependency", startObserved, ongoing)
				insights.DependencyWaitDuration += end.Sub(obs.Timestamp)
			}
		}
		if waitStartID >= 0 && clocksValid && !chain.IsComplete && insights.BlockedDurationKnown {
			id := add(CausalEvent{Type: "observation", Timestamp: chain.EndTime, Description: "Open wait measured through the reference clock; no release event is inferred", SourceBeadID: history.BeadID})
			link(waitStartID, id, "ongoing_wait", "The last known blocked state remains observed through the caller's reference instant.", chain.EndTime.Sub(waitStart))
		}
		if clocksValid {
			chain.TotalTime = chain.EndTime.Sub(chain.StartTime)
			insights.TotalDuration = chain.TotalTime
			insights.ActiveDuration = chain.TotalTime - insights.BlockedDuration
			if chain.TotalTime > 0 {
				insights.BlockedPercentage = float64(insights.BlockedDuration) / float64(chain.TotalTime) * 100
			}
		} else {
			insights.DurationKnown = false
			insights.BlockedDurationKnown = false
			insights.ExplicitDurationKnown = false
			insights.DependencyDurationKnown = false
		}
	}
	chain.DurationKnown = insights.DurationKnown
	chain.EdgeCount = len(chain.Links)
	buildConstraintPath(chain, insights)
	insights.CriticalPathDurationKnown = clocksValid && len(insights.CriticalPath) > 0
	if !clocksValid && len(insights.CriticalPath) > 0 {
		insights.CriticalPathDesc = "Dependency transitions are retained, but inconsistent clocks prevent measuring the constraint path."
	}
	if clocksValid {
		populateCausalGaps(chain, insights)
	}
	insights.Summary = buildSummary(chain, insights)
	if !insights.DurationKnown || !insights.BlockedDurationKnown {
		insights.Summary = "Partial historical evidence; total or blocked duration is unavailable"
	}
	insights.Recommendations = generateRecommendations(chain, insights)
	if !insights.DurationKnown || !insights.BlockedDurationKnown {
		insights.Recommendations = append([]string{"Review the incomplete history or inconsistent clocks before drawing duration conclusions."}, insights.Limitations...)
	}
	insights.Recommendations = append(insights.Recommendations, "Nonblocked elapsed time is not execution effort or a minimum completion-time estimate.")
	return result
}

func equalHistoricalDependencies(a, b []HistoricalDependency) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func appendUniqueInt(ids []int, id int) []int {
	for _, v := range ids {
		if v == id {
			return ids
		}
	}
	return append(ids, id)
}

// Longest path in the observed constraint DAG. Events without evidence links
// do not become part of the path merely because they occurred between others.
func buildConstraintPath(chain *CausalChain, insights *CausalInsights) {
	weights := make([]time.Duration, len(chain.Events))
	paths := make([][]int, len(chain.Events))
	for to := range chain.Events {
		for _, edge := range chain.Links {
			if edge.To != to {
				continue
			}
			weight := weights[edge.From] + edge.Duration
			if len(paths[to]) == 0 || weight > weights[to] {
				path := paths[edge.From]
				if len(path) == 0 {
					path = []int{edge.From}
				}
				paths[to] = append(append([]int(nil), path...), to)
				weights[to] = weight
			}
		}
		if len(paths[to]) > 0 && (len(insights.CriticalPath) == 0 || weights[to] > insights.CriticalPathDuration) {
			insights.CriticalPath = paths[to]
			insights.CriticalPathDuration = weights[to]
		}
	}
	if len(insights.CriticalPath) > 0 {
		insights.CriticalPathDesc = fmt.Sprintf("Longest evidenced constraint path: %s observed waiting; not a project schedule", formatDurationShort(insights.CriticalPathDuration))
	} else {
		insights.CriticalPathDesc = "No evidence-supported constraint path in the retained history"
	}
}

func causalEventOrder(eventType CausalEventType) int {
	switch eventType {
	case CausalCreated:
		return 0
	case CausalClaimed:
		return 1
	case CausalBlocked:
		return 2
	case CausalCommit:
		return 3
	case CausalUnblocked:
		return 4
	case CausalClosed:
		return 5
	case CausalReopened:
		return 6
	default:
		return 7
	}
}

// buildInsights derives analytical insights from the causal chain
func buildInsights(chain *CausalChain, history BeadHistory) *CausalInsights {
	insights := &CausalInsights{
		TotalDuration:   chain.TotalTime,
		BlockedPeriods:  []BlockedPeriod{},
		CriticalPath:    []int{},
		Recommendations: []string{},
	}

	// Count commits
	for _, event := range chain.Events {
		if event.Type == CausalCommit {
			insights.CommitCount++
		}
	}

	populateCausalGaps(chain, insights)
	// Nonblocked elapsed time is not a defensible counterfactual minimum.
	insights.Summary = buildSummary(chain, insights)
	insights.Recommendations = generateRecommendations(chain, insights)
	return insights
}

// Gaps describe observed transition chronology, not causal relationships.
// Contradictory clocks leave all gap fields unavailable, preserving Git order.
func populateCausalGaps(chain *CausalChain, insights *CausalInsights) {
	for i := 1; i < len(chain.Events); i++ {
		if chain.Events[i].Timestamp.Before(chain.Events[i-1].Timestamp) {
			return
		}
	}
	if len(chain.Events) > 1 {
		var totalGap time.Duration
		var longestGap time.Duration
		longestGapIdx := 1 // Initialize to 1 (first valid gap index), not 0

		for i := 1; i < len(chain.Events); i++ {
			gap := chain.Events[i].Timestamp.Sub(chain.Events[i-1].Timestamp)
			chain.Events[i-1].DurationNext = &gap
			totalGap += gap
			if gap > longestGap {
				longestGap = gap
				longestGapIdx = i
			}
		}

		avgGap := totalGap / time.Duration(len(chain.Events)-1)
		insights.AvgTimeBetween = &avgGap
		insights.LongestGap = &longestGap
		// longestGapIdx is always >= 1 since we initialize to 1 and only update with i >= 1
		insights.LongestGapDesc = formatGapDescription(chain.Events[longestGapIdx-1], chain.Events[longestGapIdx], longestGap)
	}

}

// formatGapDescription creates a human-readable description of a gap
func formatGapDescription(from, to CausalEvent, gap time.Duration) string {
	return formatDurationShort(gap) + " between " + string(from.Type) + " and " + string(to.Type)
}

// buildSummary creates a one-line summary of the bead's causal history
func buildSummary(chain *CausalChain, insights *CausalInsights) string {
	if !chain.IsComplete {
		if insights.BlockedPercentage > 50 {
			return "In progress, mostly blocked (" + formatDurationShort(insights.TotalDuration) + " total, " +
				formatPercent(insights.BlockedPercentage) + " blocked)"
		}
		return "In progress for " + formatDurationShort(insights.TotalDuration) +
			" with " + formatInt(insights.CommitCount) + " commits"
	}

	if insights.BlockedPercentage > 30 {
		return "Completed in " + formatDurationShort(insights.TotalDuration) +
			" (" + formatPercent(insights.BlockedPercentage) + " blocked)"
	}

	return "Completed in " + formatDurationShort(insights.TotalDuration) +
		" with " + formatInt(insights.CommitCount) + " commits"
}

// generateRecommendations creates actionable insights
func generateRecommendations(chain *CausalChain, insights *CausalInsights) []string {
	var recs []string

	// High blocked percentage
	if insights.BlockedPercentage > 50 {
		recs = append(recs, "High blocked percentage ("+formatPercent(insights.BlockedPercentage)+
			") - consider addressing blockers earlier in the process")
	}

	// Long gaps
	if insights.LongestGap != nil && *insights.LongestGap > 7*24*time.Hour {
		recs = append(recs, "Longest gap of "+formatDurationShort(*insights.LongestGap)+
			" - consider breaking work into smaller pieces")
	}

	// Few commits for long duration
	if insights.TotalDuration > 7*24*time.Hour && insights.CommitCount < 3 {
		recs = append(recs, "Few commits over "+formatDurationShort(insights.TotalDuration)+
			" - consider more frequent incremental commits")
	}

	// Still in progress for a long time
	if !chain.IsComplete && insights.TotalDuration > 14*24*time.Hour {
		recs = append(recs, "Open for "+formatDurationShort(insights.TotalDuration)+
			" - consider breaking into subtasks or closing if complete")
	}

	if len(recs) == 0 {
		recs = append(recs, "No significant issues detected in the causal flow")
	}

	return recs
}

// Helper functions

func formatDurationShort(d time.Duration) string {
	if d < time.Hour {
		return formatInt(int(d.Minutes())) + "m"
	}
	if d < 24*time.Hour {
		return formatInt(int(d.Hours())) + "h"
	}
	days := int(d.Hours() / 24)
	if days < 7 {
		return formatInt(days) + "d"
	}
	weeks := days / 7
	if weeks < 4 {
		return formatInt(weeks) + "w"
	}
	months := days / 30
	return formatInt(months) + "mo"
}

func formatPercent(p float64) string {
	return formatInt(int(p)) + "%"
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	if neg {
		result = "-" + result
	}
	return result
}

// Note: appendUnique and normalizePath are defined in other files in this package
