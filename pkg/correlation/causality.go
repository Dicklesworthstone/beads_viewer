// Package correlation provides temporal causality analysis for beads.
package correlation

import (
	"sort"
	"strconv"
	"time"
)

// CausalEventType categorizes events in the causal chain
type CausalEventType string

const (
	// CausalCreated indicates the bead was created
	CausalCreated CausalEventType = "created"
	// CausalClaimed indicates the bead was claimed (status -> in_progress)
	CausalClaimed CausalEventType = "claimed"
	// CausalCommit indicates a code commit related to the bead
	CausalCommit CausalEventType = "commit"
	// CausalBlocked is reserved for an explicit block transition from a future
	// history source. The current Git-history builder does not emit this event type.
	CausalBlocked CausalEventType = "blocked"
	// CausalUnblocked is reserved for an explicit unblock transition from a
	// future history source. The current Git-history builder does not emit it.
	CausalUnblocked CausalEventType = "unblocked"
	// CausalClosed indicates the bead was closed
	CausalClosed CausalEventType = "closed"
	// CausalReopened indicates the bead was reopened
	CausalReopened CausalEventType = "reopened"
)

// CausalEvent represents a single event in the causal chain
type CausalEvent struct {
	ID           int             `json:"id"`                      // Unique within chain
	Type         CausalEventType `json:"type"`                    // Event type
	Timestamp    time.Time       `json:"timestamp"`               // When it happened
	Description  string          `json:"description"`             // Human-readable description
	CommitSHA    string          `json:"commit_sha,omitempty"`    // For commit events
	BlockerID    string          `json:"blocker_id,omitempty"`    // For blocked/unblocked events
	CausedByID   *int            `json:"caused_by_id,omitempty"`  // ID of event that caused this
	EnablesIDs   []int           `json:"enables_ids,omitempty"`   // IDs of events this enables
	DurationNext *time.Duration  `json:"duration_next,omitempty"` // Time until next event
}

// CausalChain represents the full causal flow for a bead
type CausalChain struct {
	BeadID     string        `json:"bead_id"`
	Title      string        `json:"title"`
	Status     string        `json:"status"`
	Events     []CausalEvent `json:"events"`      // All events in chronological order
	EdgeCount  int           `json:"edge_count"`  // Number of causal links
	StartTime  time.Time     `json:"start_time"`  // First event time
	EndTime    time.Time     `json:"end_time"`    // Terminal close, or reference time if open; zero when completion timing is unknown
	TotalTime  time.Duration `json:"total_time"`  // Total elapsed time; zero when no defensible end instant exists
	IsComplete bool          `json:"is_complete"` // True if bead is closed
}

// BlockedPeriod represents a contiguous period when the bead was blocked
type BlockedPeriod struct {
	StartTime time.Time     `json:"start_time"`
	EndTime   time.Time     `json:"end_time"`
	Duration  time.Duration `json:"duration"`
	BlockerID string        `json:"blocker_id,omitempty"` // What blocked it
}

// CausalInsights contains derived analysis from the causal chain
type CausalInsights struct {
	TotalDuration     time.Duration   `json:"total_duration"`     // Total time from create to close/now
	BlockedDuration   time.Duration   `json:"blocked_duration"`   // Total time between explicit block/unblock events; currently zero for Git-built chains
	ActiveDuration    time.Duration   `json:"active_duration"`    // Time not blocked
	BlockedPercentage float64         `json:"blocked_percentage"` // % of time blocked
	BlockedPeriods    []BlockedPeriod `json:"blocked_periods"`    // Each blocked period
	CriticalPath      []int           `json:"critical_path"`      // Event IDs on critical path
	CriticalPathDesc  string          `json:"critical_path_desc"` // Human-readable critical path
	CommitCount       int             `json:"commit_count"`       // Number of commits
	AvgTimeBetween    *time.Duration  `json:"avg_time_between"`   // Avg time between events
	LongestGap        *time.Duration  `json:"longest_gap"`        // Longest gap between events
	LongestGapDesc    string          `json:"longest_gap_desc"`   // Description of longest gap
	EstimatedWithout  *time.Duration  `json:"estimated_without"`  // Est. time without blocks
	Summary           string          `json:"summary"`            // One-line summary
	Recommendations   []string        `json:"recommendations"`    // Actionable insights
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
	IncludeCommits bool // Include commit events in chain (default true)
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
// wall clock when the reference instant predates the latest observed event.
func (hr *HistoryReport) BuildCausalityChainAt(beadID string, opts CausalityOptions, now time.Time) *CausalityResult {
	if hr == nil {
		return nil
	}

	history, exists := hr.Histories[beadID]
	if !exists {
		return nil
	}

	status := normalizeStatus(history.Status)
	chain := &CausalChain{
		BeadID:     beadID,
		Title:      history.Title,
		Status:     status,
		Events:     []CausalEvent{},
		IsComplete: status == "closed" || status == "tombstone",
	}

	// Collect all events with their timestamps
	type rawEvent struct {
		timestamp       time.Time
		eventType       CausalEventType
		description     string
		commitSHA       string
		sourceCommitSHA string
		blockerID       string
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
		default:
			continue // Skip modified events for now
		}

		rawEvents = append(rawEvents, rawEvent{
			timestamp:       event.Timestamp,
			eventType:       causalType,
			description:     desc,
			sourceCommitSHA: event.CommitSHA,
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
			// Robot output must retain the collision-resistant commit identity.
			// ShortSHA remains a legacy fallback for hand-built histories that do
			// not carry the full object ID.
			commitSHA := commit.SHA
			if commitSHA == "" {
				commitSHA = commit.ShortSHA
			}
			rawEvents = append(rawEvents, rawEvent{
				timestamp:       commit.Timestamp,
				eventType:       CausalCommit,
				description:     "Commit: " + desc,
				commitSHA:       commitSHA,
				sourceCommitSHA: commit.SHA,
			})
		}
	}

	// Git timestamps have only second-level ordering fidelity. Preserve source
	// order when instants tie: history.Events is already in causal/topological
	// order, so ranking tied lifecycle events by type can invert reopen -> close
	// into close -> reopen and fabricate an incomplete terminal state. Commits are
	// initially appended after lifecycle events; the equal-time reconciliation
	// below only moves one when a shared source commit establishes causal order.
	sort.SliceStable(rawEvents, func(i, j int) bool {
		return rawEvents[i].timestamp.Before(rawEvents[j].timestamp)
	})
	// Within a tied timestamp, place a correlated code commit immediately before
	// the lifecycle transition from the same Git commit. This preserves the
	// lifecycle slice's known causal order while ensuring a close co-committed
	// with code counts as part of the completed interval. Unmatched equal-time
	// commits remain after lifecycle events: timestamp equality alone is not
	// evidence that they caused the transition.
	for groupStart := 0; groupStart < len(rawEvents); {
		groupEnd := groupStart + 1
		for groupEnd < len(rawEvents) && rawEvents[groupEnd].timestamp.Equal(rawEvents[groupStart].timestamp) {
			groupEnd++
		}
		group := rawEvents[groupStart:groupEnd]
		ordered := make([]rawEvent, 0, len(group))
		used := make([]bool, len(group))
		for i, event := range group {
			if event.eventType == CausalCommit {
				continue
			}
			if event.sourceCommitSHA != "" {
				for j, candidate := range group {
					if used[j] || candidate.eventType != CausalCommit || candidate.sourceCommitSHA != event.sourceCommitSHA {
						continue
					}
					ordered = append(ordered, candidate)
					used[j] = true
				}
			}
			ordered = append(ordered, event)
			used[i] = true
		}
		for i, event := range group {
			if !used[i] {
				ordered = append(ordered, event)
			}
		}
		copy(group, ordered)
		groupStart = groupEnd
	}

	// Convert to CausalEvents with IDs and link causality
	var prevEventID *int
	for i, raw := range rawEvents {
		event := CausalEvent{
			ID:          i,
			Type:        raw.eventType,
			Timestamp:   raw.timestamp,
			Description: raw.description,
			CommitSHA:   raw.commitSHA,
			BlockerID:   raw.blockerID,
		}

		// Link to previous event (simple linear causality for now)
		if prevEventID != nil {
			event.CausedByID = prevEventID
			// Update previous event's enables
			if len(chain.Events) > 0 {
				chain.Events[*prevEventID].EnablesIDs = append(
					chain.Events[*prevEventID].EnablesIDs, i)
			}
		}

		// Calculate duration to next event
		if i > 0 && len(chain.Events) > 0 {
			dur := raw.timestamp.Sub(chain.Events[i-1].Timestamp)
			chain.Events[i-1].DurationNext = &dur
		}

		chain.Events = append(chain.Events, event)
		id := i
		prevEventID = &id
	}

	// Set chain metadata
	if len(chain.Events) > 0 {
		chain.StartTime = chain.Events[0].Timestamp
		lastEventTime := chain.Events[len(chain.Events)-1].Timestamp
		if chain.IsComplete {
			// A current closed-like status establishes state, not timing. Only a
			// terminal close transition establishes the completion instant; an
			// arbitrary last commit must never be presented as that instant.
			if completedAt, ok := chainCompletionTime(chain.Events); ok {
				chain.EndTime = completedAt
				chain.TotalTime = chain.EndTime.Sub(chain.StartTime)
			}
		} else {
			chain.EndTime = now
			if chain.EndTime.Before(lastEventTime) {
				chain.EndTime = lastEventTime
			}
			chain.TotalTime = chain.EndTime.Sub(chain.StartTime)
		}
	}

	// Count edges
	for _, event := range chain.Events {
		chain.EdgeCount += len(event.EnablesIDs)
	}

	// Build insights
	insights := buildInsights(chain)

	return &CausalityResult{
		GeneratedAt: now,
		DataHash:    hr.DataHash,
		Chain:       chain,
		Insights:    insights,
	}
}

// buildInsights derives analytical insights from a causal chain. Its transition
// handling is exercised with synthetic chains and reserved for a future history
// source; BuildCausalityChainAt cannot currently derive blocked/unblocked
// transitions from BeadEvent history and therefore never fabricates them.
func buildInsights(chain *CausalChain) *CausalInsights {
	insights := &CausalInsights{
		TotalDuration:   chain.TotalTime,
		BlockedPeriods:  []BlockedPeriod{},
		CriticalPath:    []int{},
		Recommendations: []string{},
	}
	insightEvents := causalInsightEvents(chain)

	// Count commits that occurred within the causal interval. Correlated commits
	// after the terminal close remain visible in chain.Events but cannot explain
	// how long completion took.
	for _, event := range insightEvents {
		if event.Type == CausalCommit {
			insights.CommitCount++
		}
	}

	// Find blocked periods from explicit blocked/unblocked causal events.
	var inBlockedState bool
	var blockedStart time.Time
	var currentBlocker string

	appendBlockedPeriod := func(end time.Time) {
		if end.Before(blockedStart) {
			end = blockedStart
		}
		period := BlockedPeriod{
			StartTime: blockedStart,
			EndTime:   end,
			Duration:  end.Sub(blockedStart),
			BlockerID: currentBlocker,
		}
		insights.BlockedPeriods = append(insights.BlockedPeriods, period)
		insights.BlockedDuration += period.Duration
	}

	for _, event := range insightEvents {
		switch event.Type {
		case CausalBlocked:
			if inBlockedState {
				// Repeated blocked observations do not restart the contiguous
				// interval. Fill a previously unknown blocker when possible.
				if currentBlocker == "" {
					currentBlocker = event.BlockerID
				}
				continue
			}
			inBlockedState = true
			blockedStart = event.Timestamp
			currentBlocker = event.BlockerID
		case CausalUnblocked, CausalClosed:
			if inBlockedState {
				appendBlockedPeriod(event.Timestamp)
				inBlockedState = false
			}
		}
	}

	// A bead can still be blocked at the end of the observed chain. Account
	// for that open interval through the caller-pinned end time rather than
	// silently reporting zero blocked time.
	if inBlockedState {
		appendBlockedPeriod(chain.EndTime)
	}

	// Calculate duration-derived fields only when the chain has a defensible end
	// instant. Closed-like status without a terminal close transition has known
	// state but unknown completion duration.
	durationKnown := chainDurationKnown(chain)
	if durationKnown {
		insights.ActiveDuration = insights.TotalDuration - insights.BlockedDuration
		if insights.ActiveDuration < 0 {
			insights.ActiveDuration = 0
		}
		if insights.TotalDuration > 0 {
			insights.BlockedPercentage = float64(insights.BlockedDuration) / float64(insights.TotalDuration) * 100
			if insights.BlockedPercentage > 100 {
				insights.BlockedPercentage = 100
			}
		}
	}

	// Build critical path (for now, it's the full linear path)
	for _, event := range insightEvents {
		insights.CriticalPath = append(insights.CriticalPath, event.ID)
	}

	// Build critical path description
	if len(insightEvents) > 0 {
		var pathParts []string
		for _, event := range insightEvents {
			pathParts = append(pathParts, string(event.Type))
		}
		if len(pathParts) > 5 {
			insights.CriticalPathDesc = pathParts[0] + " → ... → " + pathParts[len(pathParts)-1]
		} else {
			desc := ""
			for i, p := range pathParts {
				if i > 0 {
					desc += " → "
				}
				desc += p
			}
			insights.CriticalPathDesc = desc
		}
	}

	// Calculate average time between events and find longest gap
	if len(insightEvents) > 1 {
		var totalGap time.Duration
		var longestGap time.Duration
		longestGapIdx := 1 // Initialize to 1 (first valid gap index), not 0

		for i := 1; i < len(insightEvents); i++ {
			gap := insightEvents[i].Timestamp.Sub(insightEvents[i-1].Timestamp)
			totalGap += gap
			if gap > longestGap {
				longestGap = gap
				longestGapIdx = i
			}
		}

		avgGap := totalGap / time.Duration(len(insightEvents)-1)
		insights.AvgTimeBetween = &avgGap
		insights.LongestGap = &longestGap
		// longestGapIdx is always >= 1 since we initialize to 1 and only update with i >= 1
		insights.LongestGapDesc = formatGapDescription(insightEvents[longestGapIdx-1], insightEvents[longestGapIdx], longestGap)
	}

	// Estimate time without blocks
	if durationKnown && insights.BlockedDuration > 0 {
		estimated := insights.ActiveDuration
		insights.EstimatedWithout = &estimated
	}

	// Build summary
	insights.Summary = buildSummary(chain, insights)

	// Generate recommendations
	insights.Recommendations = generateRecommendations(chain, insights)

	return insights
}

func causalInsightEvents(chain *CausalChain) []CausalEvent {
	if chain == nil || len(chain.Events) == 0 || !chain.IsComplete {
		if chain == nil {
			return nil
		}
		return chain.Events
	}
	completedAt, complete := chainCompletionTime(chain.Events)
	if !complete {
		return chain.Events
	}
	// Equal-timestamp ordering places commits before close. Stop at the terminal
	// close event itself so later post-completion observations cannot leak into
	// duration-derived insights or the reported causal path.
	for i := len(chain.Events) - 1; i >= 0; i-- {
		if chain.Events[i].Type == CausalClosed && chain.Events[i].Timestamp.Equal(completedAt) {
			return chain.Events[:i+1]
		}
	}
	return chain.Events
}

// formatGapDescription creates a human-readable description of a gap
func formatGapDescription(from, to CausalEvent, gap time.Duration) string {
	return formatDurationShort(gap) + " between " + string(from.Type) + " and " + string(to.Type)
}

// buildSummary creates a one-line summary of the bead's causal history
func buildSummary(chain *CausalChain, insights *CausalInsights) string {
	if !chain.IsComplete {
		if chain.Status == "blocked" {
			if !chainEndsBlocked(chain.Events) {
				return "Blocked; transition timing unavailable from history"
			}
			return "Blocked now (" + formatDurationShort(insights.TotalDuration) + " total, " +
				formatDurationShort(insights.BlockedDuration) + " blocked)"
		}
		if insights.BlockedPercentage > 50 {
			return "In progress, mostly blocked (" + formatDurationShort(insights.TotalDuration) + " total, " +
				formatPercent(insights.BlockedPercentage) + " blocked)"
		}
		return "In progress for " + formatDurationShort(insights.TotalDuration) +
			" with " + formatCommitCount(insights.CommitCount)
	}
	if !chainEndsComplete(chain.Events) {
		return "Completed; transition timing unavailable from history"
	}

	if insights.BlockedPercentage > 30 {
		return "Completed in " + formatDurationShort(insights.TotalDuration) +
			" (" + formatPercent(insights.BlockedPercentage) + " blocked)"
	}

	return "Completed in " + formatDurationShort(insights.TotalDuration) +
		" with " + formatCommitCount(insights.CommitCount)
}

func formatCommitCount(count int) string {
	noun := "commits"
	if count == 1 {
		noun = "commit"
	}
	return formatInt(count) + " " + noun
}

// generateRecommendations creates actionable insights
func generateRecommendations(chain *CausalChain, insights *CausalInsights) []string {
	var recs []string

	if chain.Status == "blocked" && !chainEndsBlocked(chain.Events) {
		recs = append(recs, "Blocked-duration metrics are unavailable because history has no explicit block transition")
	}
	if chain.IsComplete && !chainEndsComplete(chain.Events) {
		recs = append(recs, "Completion-duration metrics are unavailable because history has no terminal close transition")
	}

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

func chainEndsBlocked(events []CausalEvent) bool {
	blocked := false
	for _, event := range events {
		switch event.Type {
		case CausalBlocked:
			blocked = true
		case CausalUnblocked, CausalClosed:
			blocked = false
		}
	}
	return blocked
}

func chainEndsComplete(events []CausalEvent) bool {
	_, complete := chainCompletionTime(events)
	return complete
}

func chainCompletionTime(events []CausalEvent) (time.Time, bool) {
	complete := false
	var completedAt time.Time
	for _, event := range events {
		switch event.Type {
		case CausalClosed:
			complete = true
			completedAt = event.Timestamp
		case CausalReopened:
			complete = false
			completedAt = time.Time{}
		}
	}
	return completedAt, complete
}

func chainDurationKnown(chain *CausalChain) bool {
	if chain == nil || len(chain.Events) == 0 {
		return false
	}
	if !chain.IsComplete {
		return true
	}
	return chainEndsComplete(chain.Events)
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
	if days < 30 {
		return formatInt(days/7) + "w"
	}
	months := days / 30
	return formatInt(months) + "mo"
}

func formatPercent(p float64) string {
	return formatInt(int(p)) + "%"
}

func formatInt(n int) string {
	return strconv.Itoa(n)
}

// Note: appendUnique and normalizePath are defined in other files in this package
