// Package correlation provides types for correlating beads with git history,
// enabling lifecycle tracking and code commit attribution.
package correlation

import (
	"time"
)

// EventType categorizes lifecycle events for a bead
type EventType string

const (
	// EventCreated indicates the bead first appeared in the beads file
	EventCreated EventType = "created"
	// EventClaimed indicates status changed to in_progress
	EventClaimed EventType = "claimed"
	// EventClosed indicates status changed to a closed-like state.
	EventClosed EventType = "closed"
	// EventReopened indicates status changed FROM a closed-like state to open/in_progress
	EventReopened EventType = "reopened"
	// EventModified indicates other significant changes (title, priority, deps)
	EventModified EventType = "modified"
	// EventDeleted records removal from the history source, not completion.
	EventDeleted EventType = "deleted"
)

// String returns the string representation of EventType
func (e EventType) String() string {
	return string(e)
}

// IsValid returns true if the event type is a recognized value
func (e EventType) IsValid() bool {
	switch e {
	case EventCreated, EventClaimed, EventClosed, EventReopened, EventModified, EventDeleted:
		return true
	}
	return false
}

// BeadEvent represents a single lifecycle event for a bead, extracted from git history
type BeadEvent struct {
	BeadID             string                `json:"bead_id"`
	EventType          EventType             `json:"event_type"`
	Timestamp          time.Time             `json:"timestamp"`
	CommitSHA          string                `json:"commit_sha"`
	CommitMsg          string                `json:"commit_message"`
	Author             string                `json:"author"`
	AuthorEmail        string                `json:"author_email"`
	Before             *HistoricalIssueState `json:"before,omitempty"`
	After              *HistoricalIssueState `json:"after,omitempty"`
	TransitionObserved bool                  `json:"transition_observed"` // Retained record transitions parsed successfully; full-source authority is separate
}

// HistoricalIssueState retains the decision evidence in a committed record.
// Dependencies are values owned by the extraction, independent of live issues.
type HistoricalIssueState struct {
	ID           string                 `json:"id"`
	Status       string                 `json:"status"`
	Title        string                 `json:"title"`
	Dependencies []HistoricalDependency `json:"dependencies,omitempty"`
}

type HistoricalDependency struct {
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// CorrelationMethod describes how a commit was linked to a bead
type CorrelationMethod string

const (
	// MethodCoCommitted means the commit modified beads.jsonl and this bead simultaneously
	MethodCoCommitted CorrelationMethod = "co_committed"
	// MethodExplicitID means the commit message explicitly references the bead ID
	MethodExplicitID CorrelationMethod = "explicit_id"
	// MethodTemporalAuthor means the commit is temporally close and by the assignee
	MethodTemporalAuthor CorrelationMethod = "temporal_author"
)

// String returns the string representation of CorrelationMethod
func (c CorrelationMethod) String() string {
	return string(c)
}

// IsValid returns true if the correlation method is a recognized value
func (c CorrelationMethod) IsValid() bool {
	switch c {
	case MethodCoCommitted, MethodExplicitID, MethodTemporalAuthor:
		return true
	}
	return false
}

// FileChange represents a single file modification within a commit
type FileChange struct {
	Path       string `json:"path"`
	Action     string `json:"action"` // A=added, M=modified, D=deleted, R=renamed
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// CorrelatedCommit represents a code commit linked to a bead with confidence metadata
type CorrelatedCommit struct {
	BeadID      string            `json:"-"` // Internal use for linking
	SHA         string            `json:"sha"`
	ShortSHA    string            `json:"short_sha"`
	Message     string            `json:"message"`
	Author      string            `json:"author"`
	AuthorEmail string            `json:"author_email"`
	Timestamp   time.Time         `json:"timestamp"`
	Files       []FileChange      `json:"files"`
	Method      CorrelationMethod `json:"method"`     // Primary (highest-confidence) method
	Methods     []string          `json:"methods"`    // Every method that matched this (commit, bead) pair
	Confidence  float64           `json:"confidence"` // 0.0 to 1.0
	Reason      string            `json:"reason"`     // Human-readable explanation
	Confirmed   bool              `json:"confirmed,omitempty"`
}

// AllMethods returns every correlation method that linked this commit to its
// bead. Commits assembled by the Correlator always carry Methods; a commit
// built elsewhere (tests, incremental merges) falls back to its primary Method.
func (c CorrelatedCommit) AllMethods() []string {
	if len(c.Methods) > 0 {
		return c.Methods
	}
	if c.Method == "" {
		return nil
	}
	return []string{c.Method.String()}
}

// MethodDistributionConfirmedByFeedback is the HistoryStats.MethodDistribution
// key counting commits whose confidence was pinned to 1.0 by a stored
// confirmation. It is not a CorrelationMethod: the commit keeps its real
// methods and additionally counts here.
const MethodDistributionConfirmedByFeedback = "confirmed_by_feedback"

// BeadMilestones contains key lifecycle timestamps for quick access
type BeadMilestones struct {
	Created  *BeadEvent `json:"created,omitempty"`
	Claimed  *BeadEvent `json:"claimed,omitempty"`
	Closed   *BeadEvent `json:"closed,omitempty"`
	Reopened *BeadEvent `json:"reopened,omitempty"` // Most recent if multiple
}

// CycleTime represents the duration between lifecycle events
type CycleTime struct {
	ClaimToClose  *time.Duration `json:"claim_to_close,omitempty"`  // Time from claimed to closed
	CreateToClose *time.Duration `json:"create_to_close,omitempty"` // Time from created to closed
	CreateToClaim *time.Duration `json:"create_to_claim,omitempty"` // Time from created to claimed
}

// BeadHistory is the complete correlation record for a single bead
type BeadHistory struct {
	BeadID     string             `json:"bead_id"`
	Title      string             `json:"title"`
	Status     string             `json:"status"`
	Events     []BeadEvent        `json:"events"`      // All lifecycle events, chronological
	Milestones BeadMilestones     `json:"milestones"`  // Key events for quick access
	Commits    []CorrelatedCommit `json:"commits"`     // Related code commits
	CycleTime  *CycleTime         `json:"cycle_time"`  // nil if not yet closed
	LastAuthor string             `json:"last_author"` // Most recent committer
}

// CommitIndex provides O(1) lookup from commit SHA to bead IDs
type CommitIndex map[string][]string

// HistoryStats provides aggregate statistics for the history report
type HistoryStats struct {
	TotalBeads         int              `json:"total_beads"`
	BeadsWithCommits   int              `json:"beads_with_commits"`
	TotalCommits       int              `json:"total_commits"`
	UniqueAuthors      int              `json:"unique_authors"`
	AvgCommitsPerBead  float64          `json:"avg_commits_per_bead"`
	AvgCycleTimeDays   *float64         `json:"avg_cycle_time_days,omitempty"` // nil if no closed beads
	MethodDistribution map[string]int   `json:"method_distribution"`           // Count per correlation method (a multi-method commit counts once per method)
	Strategies         []StrategyRun    `json:"strategies,omitempty"`          // Which correlation strategies ran and what they cost
	FeedbackApplied    *FeedbackApplied `json:"feedback_applied,omitempty"`    // nil when no feedback store was consulted
}

// StrategyRun records one correlation strategy's execution during history
// extraction. DurationMS is wall time measured when the artifact was
// extracted; a report served from the persistent caches carries the timing of
// the extraction that produced the cached artifact, not of the current call.
type StrategyRun struct {
	Name       string  `json:"name"`
	Ran        bool    `json:"ran"`
	DurationMS float64 `json:"duration_ms"`
	Candidates int     `json:"candidates"` // Raw (commit, bead) pairs produced before assembly filtered unknown beads
}

// FeedbackApplied counts how stored correlation feedback shaped the report:
// rejected pairs were removed from histories and the commit index, confirmed
// pairs were pinned to confidence 1.0, ignored pairs were left untouched.
type FeedbackApplied struct {
	Confirmed int `json:"confirmed"`
	Rejected  int `json:"rejected"`
	Ignored   int `json:"ignored"`
}

// HistoryWindow is the commit window the correlation index covers. The
// explicit-ID and temporal strategies walk the last Limit non-merge commits
// (bounded by Since/Until); Commits is how many commits that walk yielded.
// The orphan detector aligns its own window to this one.
type HistoryWindow struct {
	Revision string     `json:"revision,omitempty"`
	Limit    int        `json:"limit"`
	Since    *time.Time `json:"since,omitempty"`
	Until    *time.Time `json:"until,omitempty"`
	Commits  int        `json:"commits"`
}

// HistoryReport is the top-level output structure for --robot-history
type HistoryReport struct {
	GeneratedAt     time.Time              `json:"generated_at"`
	DataHash        string                 `json:"data_hash"`                   // Hash of source beads.jsonl for consistency checks
	GitRange        string                 `json:"git_range"`                   // e.g., "HEAD~100..HEAD" or "2024-01-01..2024-12-15"
	LatestCommitSHA string                 `json:"latest_commit_sha,omitempty"` // Most recent commit SHA for incremental updates
	Window          *HistoryWindow         `json:"window,omitempty"`            // Commit window the index covers (nil for reports assembled without a walk)
	Stats           HistoryStats           `json:"stats"`                       // Aggregate statistics
	Histories       map[string]BeadHistory `json:"histories"`                   // BeadID -> BeadHistory
	CommitIndex     CommitIndex            `json:"commit_index"`                // SHA -> []BeadID for reverse lookup
	CausalHistory   *CausalHistory         `json:"causal_history,omitempty"`    // Full historical authority, only for an explicitly requested target

	// rejectedPairs records explicit (commitSHA -> beadID) feedback rejections
	// from FeedbackStore so ReverseLookup suppresses fallback per-pair instead of
	// disabling explicit matching globally across unrelated commits.
	rejectedPairs map[string]map[string]bool
}

// MergeRejectedPairs copies per-pair feedback rejections from other into r.
// When qualify is non-nil and returns a non-empty namespaced ID, only the
// qualified ID is recorded so a rejection in one workspace repo cannot poison
// an unprefixed sibling store in a shared-git monorepo.
func (r *HistoryReport) MergeRejectedPairs(other *HistoryReport, qualify func(string) string) {
	if r == nil || other == nil || len(other.rejectedPairs) == 0 {
		return
	}
	if r.rejectedPairs == nil {
		r.rejectedPairs = make(map[string]map[string]bool, len(other.rejectedPairs))
	}
	for sha, beads := range other.rejectedPairs {
		if sha == "" || len(beads) == 0 {
			continue
		}
		if r.rejectedPairs[sha] == nil {
			r.rejectedPairs[sha] = make(map[string]bool, len(beads))
		}
		for beadID, rejected := range beads {
			if !rejected || beadID == "" {
				continue
			}
			targetID := beadID
			if qualify != nil {
				if qualID := qualify(beadID); qualID != "" {
					targetID = qualID
				}
			}
			r.rejectedPairs[sha][targetID] = true
		}
	}
}

// FilterOptions controls which beads to include in the history report
type FilterOptions struct {
	BeadIDs       []string   `json:"bead_ids,omitempty"`       // Specific beads to include (nil = all)
	Since         *time.Time `json:"since,omitempty"`          // Only events after this time
	Until         *time.Time `json:"until,omitempty"`          // Only events before this time
	Authors       []string   `json:"authors,omitempty"`        // Filter by author name/email
	MinConfidence float64    `json:"min_confidence,omitempty"` // Minimum confidence for commits (default 0)
	IncludeClosed bool       `json:"include_closed"`           // Include closed beads (Go default: false)
}

// SignalType categorizes the types of signals that contribute to correlation confidence
type SignalType string

const (
	// SignalMessageMatch indicates the commit message explicitly contains the bead ID
	SignalMessageMatch SignalType = "message_match"
	// SignalTiming indicates the commit is temporally close to bead status changes
	SignalTiming SignalType = "timing"
	// SignalFileOverlap indicates shared files between commit and bead scope
	SignalFileOverlap SignalType = "file_overlap"
	// SignalAuthorMatch indicates same author as the bead's assignee
	SignalAuthorMatch SignalType = "author_match"
	// SignalProximity indicates adjacent to other confirmed linked commits
	SignalProximity SignalType = "proximity"
	// SignalCoCommit indicates the commit modified beads file and code together
	SignalCoCommit SignalType = "co_commit"
)

// CorrelationSignal represents a single factor contributing to correlation confidence
type CorrelationSignal struct {
	Type   SignalType `json:"type"`   // Type of signal detected
	Weight int        `json:"weight"` // Contribution to overall confidence (0-100)
	Detail string     `json:"detail"` // Human/agent readable explanation
}

// CorrelationExplanation provides a detailed breakdown of why a commit is linked to a bead
type CorrelationExplanation struct {
	CommitSHA      string              `json:"commit_sha"`
	BeadID         string              `json:"bead_id"`
	Confidence     float64             `json:"confidence"`     // 0.0 to 1.0
	ConfidencePct  int                 `json:"confidence_pct"` // 0 to 100 for display
	Level          string              `json:"level"`          // "very high", "high", "moderate", "low", "very low"
	Method         CorrelationMethod   `json:"method"`         // Primary correlation method
	Signals        []CorrelationSignal `json:"signals"`        // All contributing signals
	TotalWeight    int                 `json:"total_weight"`   // Sum of signal weights
	Summary        string              `json:"summary"`        // One-line summary
	Recommendation string              `json:"recommendation"` // Suggested action
	// Feedback is the stored confirm/reject/ignore decision for this pair, if
	// any. The explanation always describes the raw strategy score so a
	// rejected pair can still be explained (and the rejection reconsidered).
	Feedback *CorrelationFeedback `json:"feedback,omitempty"`
}

// FeedbackType categorizes user/agent feedback on correlations
type FeedbackType string

const (
	// FeedbackConfirm indicates the correlation is correct
	FeedbackConfirm FeedbackType = "confirm"
	// FeedbackReject indicates the correlation is incorrect
	FeedbackReject FeedbackType = "reject"
	// FeedbackIgnore indicates the correlation should be excluded from training
	FeedbackIgnore FeedbackType = "ignore"
)

// CorrelationFeedback represents user/agent feedback on a correlation
type CorrelationFeedback struct {
	CommitSHA    string       `json:"commit_sha"`
	BeadID       string       `json:"bead_id"`
	FeedbackAt   time.Time    `json:"feedback_at"`
	FeedbackBy   string       `json:"feedback_by"`   // Agent or user identifier
	Type         FeedbackType `json:"type"`          // confirm, reject, ignore
	Reason       string       `json:"reason"`        // Optional explanation
	OriginalConf float64      `json:"original_conf"` // Confidence before feedback
}

// FeedbackStats provides aggregate statistics about correlation feedback
type FeedbackStats struct {
	TotalFeedback  int     `json:"total_feedback"`
	Confirmed      int     `json:"confirmed"`
	Rejected       int     `json:"rejected"`
	Ignored        int     `json:"ignored"`
	AccuracyRate   float64 `json:"accuracy_rate"`    // confirmed / (confirmed + rejected)
	AvgConfirmConf float64 `json:"avg_confirm_conf"` // Avg confidence of confirmed
	AvgRejectConf  float64 `json:"avg_reject_conf"`  // Avg confidence of rejected
}
