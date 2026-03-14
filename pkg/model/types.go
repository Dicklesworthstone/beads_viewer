package model

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"
)

// Issue represents a trackable work item
type Issue struct {
	ID                 string        `json:"id"`
	ContentHash        string        `json:"-"`
	Title              string        `json:"title"`
	Description        string        `json:"description"`
	Design             string        `json:"design,omitempty"`
	AcceptanceCriteria string        `json:"acceptance_criteria,omitempty"`
	Notes              string        `json:"notes,omitempty"`
	Status             Status        `json:"status"`
	Priority           int           `json:"priority"`
	IssueType          IssueType     `json:"issue_type"`
	Assignee           string        `json:"assignee,omitempty"`
	EstimatedMinutes   *int          `json:"estimated_minutes,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	DueDate            *time.Time    `json:"due_date,omitempty"`
	ClosedAt           *time.Time    `json:"closed_at,omitempty"`
	ExternalRef        *string       `json:"external_ref,omitempty"`
	CompactionLevel    int           `json:"compaction_level,omitempty"`
	CompactedAt        *time.Time    `json:"compacted_at,omitempty"`
	CompactedAtCommit  *string       `json:"compacted_at_commit,omitempty"`
	OriginalSize       int           `json:"original_size,omitempty"`
	Labels             []string      `json:"labels,omitempty"`
	Dependencies       []*Dependency `json:"dependencies,omitempty"`
	Comments           []*Comment    `json:"comments,omitempty"`
	SourceRepo         string        `json:"source_repo,omitempty"`
}

// Clone creates a deep copy of the issue
func (i Issue) Clone() Issue {
	clone := i

	if i.EstimatedMinutes != nil {
		v := *i.EstimatedMinutes
		clone.EstimatedMinutes = &v
	}
	if i.ClosedAt != nil {
		v := *i.ClosedAt
		clone.ClosedAt = &v
	}
	if i.DueDate != nil {
		v := *i.DueDate
		clone.DueDate = &v
	}
	if i.ExternalRef != nil {
		v := *i.ExternalRef
		clone.ExternalRef = &v
	}
	if i.CompactedAt != nil {
		v := *i.CompactedAt
		clone.CompactedAt = &v
	}
	if i.CompactedAtCommit != nil {
		v := *i.CompactedAtCommit
		clone.CompactedAtCommit = &v
	}

	if i.Labels != nil {
		clone.Labels = make([]string, len(i.Labels))
		copy(clone.Labels, i.Labels)
	}

	if i.Dependencies != nil {
		clone.Dependencies = make([]*Dependency, len(i.Dependencies))
		for idx, dep := range i.Dependencies {
			if dep != nil {
				v := *dep
				clone.Dependencies[idx] = &v
			}
		}
	}

	if i.Comments != nil {
		clone.Comments = make([]*Comment, len(i.Comments))
		for idx, comment := range i.Comments {
			if comment != nil {
				v := *comment
				clone.Comments[idx] = &v
			}
		}
	}

	return clone
}

// Validate checks if the issue data is logically valid
func (i *Issue) Validate() error {
	if i.ID == "" {
		return fmt.Errorf("issue ID cannot be empty")
	}
	if i.Title == "" {
		return fmt.Errorf("issue title cannot be empty")
	}
	if i.Status == "" {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	if !i.IssueType.IsValid() {
		return fmt.Errorf("invalid issue type: %s", i.IssueType)
	}
	if !i.UpdatedAt.IsZero() && !i.CreatedAt.IsZero() && i.UpdatedAt.Before(i.CreatedAt) {
		return fmt.Errorf("updated_at (%v) cannot be before created_at (%v)", i.UpdatedAt, i.CreatedAt)
	}
	return nil
}

// Status represents the current state of an issue
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusDeferred   Status = "deferred" // Deliberately put on ice for later
	StatusDraft      Status = "draft"    // Being authored, not yet ready for execution
	StatusPinned     Status = "pinned"   // Persistent bead that stays open indefinitely
	StatusHooked     Status = "hooked"   // Work attached to an agent's hook (GUPP)
	StatusReview     Status = "review"   // Awaiting review before completion
	StatusClosed     Status = "closed"
	StatusTombstone  Status = "tombstone" // Soft-deleted issue
)

// IsValid returns true if the status is a recognized value
func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusBlocked, StatusDeferred, StatusDraft,
		StatusPinned, StatusHooked, StatusReview, StatusClosed, StatusTombstone:
		return true
	}
	return false
}

// IsClosed returns true if the status represents a closed state
func (s Status) IsClosed() bool {
	return s == StatusClosed
}

// IsOpen returns true if the status represents an active (open or in_progress) state
func (s Status) IsOpen() bool {
	return s == StatusOpen || s == StatusInProgress
}

// IsTombstone returns true if the status represents a permanently deleted/archived state
func (s Status) IsTombstone() bool {
	return s == StatusTombstone
}

// IssueType categorizes the kind of work
type IssueType string

const (
	TypeBug     IssueType = "bug"
	TypeFeature IssueType = "feature"
	TypeTask    IssueType = "task"
	TypeEpic    IssueType = "epic"
	TypeChore   IssueType = "chore"
)

// IsValid returns true if the issue type is non-empty.
// Any non-empty type is considered valid to support extensibility in the Beads ecosystem
// (e.g., Gastown orchestration types like "role", "agent", "molecule").
// The UI will display a default icon for unrecognized types.
func (t IssueType) IsValid() bool {
	return t != ""
}

// IsKnownType returns true if the issue type is one of the standard bv types.
// This is used for sorting and icon selection, not validation.
func (t IssueType) IsKnownType() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore:
		return true
	}
	return false
}

// Dependency represents a relationship between issues
type Dependency struct {
	IssueID     string         `json:"issue_id"`
	DependsOnID string         `json:"depends_on_id"`
	Type        DependencyType `json:"type"`
	CreatedAt   time.Time      `json:"created_at"`
	CreatedBy   string         `json:"created_by"`
}

// IssueMetrics holds computed metrics for export/robot consumers.
type IssueMetrics struct {
	PageRank          float64 `json:"pagerank,omitempty"`
	Betweenness       float64 `json:"betweenness,omitempty"`
	CriticalPathDepth int     `json:"critical_path_depth,omitempty"`
	TriageScore       float64 `json:"triage_score,omitempty"`
	BlocksCount       int     `json:"blocks_count,omitempty"`
	BlockedByCount    int     `json:"blocked_by_count,omitempty"`
}

// DependencyType categorizes the relationship
type DependencyType string

const (
	DepBlocks         DependencyType = "blocks"
	DepRelated        DependencyType = "related"
	DepParentChild    DependencyType = "parent-child"
	DepDiscoveredFrom DependencyType = "discovered-from"
)

// IsValid returns true if the dependency type is a recognized value
func (d DependencyType) IsValid() bool {
	switch d {
	case DepBlocks, DepRelated, DepParentChild, DepDiscoveredFrom:
		return true
	}
	return false
}

// IsBlocking returns true if this dependency type represents a blocking relationship.
// Note: An empty string ("") is treated as blocking for backward compatibility with
// legacy beads data that predates the typed dependency system. This means dependencies
// created without an explicit type will block by default.
func (d DependencyType) IsBlocking() bool {
	return d == "" || d == DepBlocks
}

// Comment represents a comment on an issue
type Comment struct {
	ID        int64     `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// UnmarshalJSON accepts both the legacy numeric comment IDs and the UUID-style
// string IDs emitted by current bd exports. String IDs are mapped to a stable
// synthetic int64 so the rest of the viewer can remain unchanged.
func (c *Comment) UnmarshalJSON(data []byte) error {
	type rawComment struct {
		ID        json.RawMessage `json:"id"`
		IssueID   string          `json:"issue_id"`
		Author    string          `json:"author"`
		Text      string          `json:"text"`
		CreatedAt time.Time       `json:"created_at"`
	}

	var raw rawComment
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	commentID, err := parseCommentID(raw.ID)
	if err != nil {
		return err
	}

	c.ID = commentID
	c.IssueID = raw.IssueID
	c.Author = raw.Author
	c.Text = raw.Text
	c.CreatedAt = raw.CreatedAt
	return nil
}

func parseCommentID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	var numericID int64
	if err := json.Unmarshal(raw, &numericID); err == nil {
		return numericID, nil
	}

	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		if stringID == "" {
			return 0, nil
		}
		if parsed, err := strconv.ParseInt(stringID, 10, 64); err == nil {
			return parsed, nil
		}
		hasher := fnv.New64a()
		_, _ = hasher.Write([]byte(stringID))
		return int64(hasher.Sum64() & 0x7fffffffffffffff), nil
	}

	return 0, fmt.Errorf("invalid comment id: %s", string(raw))
}

// Sprint represents a time-boxed period of work
type Sprint struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	StartDate      time.Time `json:"start_date,omitzero"`
	EndDate        time.Time `json:"end_date,omitzero"`
	BeadIDs        []string  `json:"bead_ids,omitempty"`
	VelocityTarget float64   `json:"velocity_target,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
}

// Validate checks if the sprint data is logically valid
func (s *Sprint) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("sprint ID cannot be empty")
	}
	if s.Name == "" {
		return fmt.Errorf("sprint name cannot be empty")
	}
	if !s.EndDate.IsZero() && !s.StartDate.IsZero() && s.EndDate.Before(s.StartDate) {
		return fmt.Errorf("end_date (%v) cannot be before start_date (%v)", s.EndDate, s.StartDate)
	}
	return nil
}

// IsActive returns true if the sprint is currently active (today is within the sprint dates)
func (s *Sprint) IsActive() bool {
	now := time.Now()
	return !s.StartDate.IsZero() && !s.EndDate.IsZero() &&
		(now.Equal(s.StartDate) || now.After(s.StartDate)) &&
		(now.Equal(s.EndDate) || now.Before(s.EndDate))
}

// Forecast represents an ETA prediction for a specific bead
type Forecast struct {
	BeadID     string    `json:"bead_id"`
	ETADate    time.Time `json:"eta_date"`
	Confidence float64   `json:"confidence"`
	Factors    []string  `json:"factors,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
}

// Validate checks if the forecast data is logically valid.
func (f *Forecast) Validate() error {
	if f.BeadID == "" {
		return fmt.Errorf("bead_id cannot be empty")
	}
	if f.ETADate.IsZero() {
		return fmt.Errorf("eta_date cannot be empty")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return fmt.Errorf("confidence (%v) must be between 0 and 1", f.Confidence)
	}
	return nil
}

// BurndownPoint represents a single point in a burndown chart
type BurndownPoint struct {
	Date      time.Time `json:"date"`
	Remaining int       `json:"remaining"`
	Completed int       `json:"completed"`
}

// Validate checks if the burndown point data is logically valid.
func (b *BurndownPoint) Validate() error {
	if b.Date.IsZero() {
		return fmt.Errorf("date cannot be empty")
	}
	if b.Remaining < 0 {
		return fmt.Errorf("remaining (%d) cannot be negative", b.Remaining)
	}
	if b.Completed < 0 {
		return fmt.Errorf("completed (%d) cannot be negative", b.Completed)
	}
	return nil
}
