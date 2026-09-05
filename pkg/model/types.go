package model

import (
	stdjson "encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	json "github.com/goccy/go-json"
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
	DeferUntil         *time.Time    `json:"defer_until,omitempty"` // Scheduler deferral: hidden from ready/actionable until this instant passes
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
	// Origin is established by the live loader before workspace namespacing.
	// Serialized input cannot manufacture authority to emit tracker commands.
	Origin *IssueOrigin `json:"-"`
}

// IssueOrigin identifies the exact tracker that supplied a displayed issue.
// It is transient: exporting and reimporting an issue never preserves a live
// mutation route. Capabilities describe the installed executable at load time.
type IssueOrigin struct {
	LocalID          string
	WorkingDirectory string
	TrackerDirectory string
	Database         string
	Tracker          string
	Executable       string
	SupportsClaim    bool
	ReadOnlyReason   string
}

// IssueCommand carries executable argv as well as its POSIX-shell rendering.
// WorkingDirectory applies when executing Argv directly.
type IssueCommand struct {
	WorkingDirectory string   `json:"working_directory"`
	Argv             []string `json:"argv"`
	Shell            string   `json:"shell"`
}

type IssueActions struct {
	WorkingDirectory  string        `json:"working_directory,omitempty"`
	LocalID           string        `json:"local_id,omitempty"`
	Tracker           string        `json:"tracker,omitempty"`
	Show              *IssueCommand `json:"show,omitempty"`
	Claim             *IssueCommand `json:"claim,omitempty"`
	UnavailableReason string        `json:"unavailable_reason,omitempty"`
}

// ShellQuote preserves one literal argument, including quotes and newlines.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (o *IssueOrigin) routeAvailable() bool {
	return o != nil && o.Executable != "" && o.Database != "" && o.WorkingDirectory != "" &&
		o.TrackerDirectory != "" && o.LocalID != "" && (o.Tracker == "br" || o.Tracker == "bd")
}

// command uses only an established origin and literal argv. Read-only inspection
// must not import or flush stale exports; successful mutations keep normal export
// behavior so subsequent viewers can observe the tracker change.
func (o *IssueOrigin) command(mutating bool, operation ...string) *IssueCommand {
	args := []string{"env", "BEADS_DIR=" + o.TrackerDirectory, "BEADS_DB=" + o.Database, "BD_DB=" + o.Database,
		o.Executable, "--db", o.Database}
	if o.Tracker == "br" {
		args = append(args, "--no-auto-import")
		if !mutating {
			args = append(args, "--no-auto-flush")
		}
	}
	args = append(args, operation...)
	quoted := make([]string, len(args))
	for n, arg := range args {
		quoted[n] = ShellQuote(arg)
	}
	return &IssueCommand{WorkingDirectory: o.WorkingDirectory, Argv: args,
		Shell: "cd -- " + ShellQuote(o.WorkingDirectory) + " && " + strings.Join(quoted, " ")}
}

type MutationKind string

const (
	MutationAddDependency    MutationKind = "add_dependency"
	MutationRelate           MutationKind = "relate"
	MutationRemoveDependency MutationKind = "remove_dependency"
	MutationAddLabel         MutationKind = "add_label"
)

// MutationAction binds a hygiene suggestion to live local IDs. Dependencies
// across trackers remain analytical suggestions because one tracker's CLI cannot
// safely interpret another tracker's ID. This never executes the suggestion.
func (i Issue) MutationAction(kind MutationKind, peer *Issue, value string) (*IssueCommand, string) {
	o := i.Origin
	if !o.routeAvailable() {
		return nil, "source has no verified live tracker route"
	}
	if o.ReadOnlyReason != "" {
		return nil, o.ReadOnlyReason
	}
	if kind == MutationAddLabel {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
			return nil, "suggested label is empty or contains a NUL byte"
		}
		return o.command(true, "update", "--json", "--add-label="+value, "--", o.LocalID), ""
	}
	if kind != MutationAddDependency && kind != MutationRelate && kind != MutationRemoveDependency {
		return nil, "unsupported tracker mutation"
	}
	if peer == nil || !peer.Origin.routeAvailable() {
		return nil, "related issue has no verified live tracker route"
	}
	p := peer.Origin
	if p.ReadOnlyReason != "" {
		return nil, p.ReadOnlyReason
	}
	if o.Tracker != p.Tracker || o.Database != p.Database || o.WorkingDirectory != p.WorkingDirectory ||
		o.TrackerDirectory != p.TrackerDirectory || o.Executable != p.Executable {
		return nil, "related issues belong to different trackers"
	}
	if o.LocalID == p.LocalID {
		return nil, "dependency action refers to the same local issue"
	}
	operation := []string{"dep", "add", "--json"}
	if kind == MutationRemoveDependency {
		operation[1] = "remove"
	} else if kind == MutationRelate {
		operation = append(operation, "--type", "related")
	}
	operation = append(operation, "--", o.LocalID, p.LocalID)
	return o.command(true, operation...), ""
}

// Actions constructs suggestions only. The tracker rechecks an atomic claim
// when executed; the snapshot is never a reservation of future work.
func (i Issue) Actions(claimable bool) IssueActions {
	o := i.Origin
	if o == nil {
		return IssueActions{UnavailableReason: "source has no verified live tracker"}
	}
	a := IssueActions{WorkingDirectory: o.WorkingDirectory, LocalID: o.LocalID, Tracker: o.Tracker}
	if !o.routeAvailable() {
		a.UnavailableReason = o.ReadOnlyReason
		if a.UnavailableReason == "" {
			a.UnavailableReason = "live tracker route is incomplete"
		}
		return a
	}
	a.Show = o.command(false, "show", "--json", "--", o.LocalID)
	switch {
	case o.ReadOnlyReason != "":
		a.UnavailableReason = o.ReadOnlyReason
	case !claimable:
		a.UnavailableReason = "snapshot does not establish claim readiness"
	case !o.SupportsClaim:
		a.UnavailableReason = "installed tracker does not advertise atomic --claim"
	default:
		a.Claim = o.command(true, "update", "--json", "--claim", "--", o.LocalID)
	}
	return a
}

// Clone creates a deep copy of the issue
func (i Issue) Clone() Issue {
	clone := i
	if i.Origin != nil {
		origin := *i.Origin
		clone.Origin = &origin
	}

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
	if i.DeferUntil != nil {
		v := *i.DeferUntil
		clone.DeferUntil = &v
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

// IsDeferredAt reports whether the issue's defer_until deferral is still
// active at the given instant. This mirrors `br ready`: a bead whose
// defer_until lies strictly in the future is withheld from ready/actionable
// views; once the instant is reached (or if no deferral is set) it is not
// deferred. The comparison is instant-based, so the source timezone of the
// timestamp is irrelevant.
func (i Issue) IsDeferredAt(now time.Time) bool {
	return i.DeferUntil != nil && i.DeferUntil.After(now)
}

// Validate checks if the issue data is logically valid
func (i *Issue) Validate() error {
	if i.ID == "" {
		return fmt.Errorf("issue ID cannot be empty")
	}
	if i.Title == "" {
		return fmt.Errorf("issue title cannot be empty")
	}
	if !i.Status.IsValid() {
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

// UnmarshalJSON accepts the dependency target field names emitted by the
// Beads ecosystem. `depends_on_id` is the canonical field; `depends_on` and
// `target_id` appear in older JSONL exports and should still produce graph
// edges instead of silently loading as empty dependencies.
func (d *Dependency) UnmarshalJSON(data []byte) error {
	type rawDependency struct {
		IssueID     string         `json:"issue_id"`
		DependsOnID string         `json:"depends_on_id"`
		DependsOn   string         `json:"depends_on"`
		TargetID    string         `json:"target_id"`
		Type        DependencyType `json:"type"`
		CreatedAt   time.Time      `json:"created_at"`
		CreatedBy   string         `json:"created_by"`
	}
	var raw rawDependency
	// goccy deliberately leaves invalid UTF-8 bytes intact, whereas the legacy
	// encoding/json contract replaces them with U+FFFD. Keep that rare input on
	// the standard-library path; valid UTF-8 is the profiled production case.
	if !utf8.Valid(data) {
		if err := stdjson.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if err := json.Unmarshal(data, &raw); err != nil {
		// Keep the successful, production-heavy path on goccy, while replaying
		// failures through the standard library so callers retain the legacy
		// concrete error type and message. Reset raw first because a decoder may
		// populate fields before reporting a later error.
		raw = rawDependency{}
		if err := stdjson.Unmarshal(data, &raw); err != nil {
			return err
		}
	}

	d.IssueID = raw.IssueID
	d.DependsOnID = raw.DependsOnID
	if d.DependsOnID == "" {
		d.DependsOnID = raw.DependsOn
	}
	if d.DependsOnID == "" {
		d.DependsOnID = raw.TargetID
	}
	d.Type = raw.Type
	d.CreatedAt = raw.CreatedAt
	d.CreatedBy = raw.CreatedBy
	return nil
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

// Comment represents a comment on an issue.
//
// ID is intentionally a string: beads v1.0+ writes UUIDv7 string IDs,
// but legacy data and tests may still produce integer IDs. Encoding the
// field as a string with a json.Number-tolerant unmarshaller (see
// UnmarshalJSON below) lets the loader accept either shape without
// dropping the comment from the parsed issue (issue #145).
type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Author    string    `json:"author"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// UnmarshalJSON accepts ID as either a string (UUIDv7, the v1.0+ format)
// or a JSON number (legacy integer-id JSONL written by older beads
// versions and present in some test fixtures). Numbers are stringified
// preserving their original textual form so round-tripping a legacy
// numeric id back through the export does not silently lose precision.
func (c *Comment) UnmarshalJSON(data []byte) error {
	type rawComment struct {
		ID        stdjson.RawMessage `json:"id"`
		IssueID   string             `json:"issue_id"`
		Author    string             `json:"author"`
		Text      string             `json:"text"`
		CreatedAt time.Time          `json:"created_at"`
	}
	var raw rawComment
	if err := stdjson.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.IssueID = raw.IssueID
	c.Author = raw.Author
	c.Text = raw.Text
	c.CreatedAt = raw.CreatedAt
	if len(raw.ID) == 0 || string(raw.ID) == "null" {
		c.ID = ""
		return nil
	}
	// Try as string first (UUIDv7, the v1.0+ format).
	var s string
	if err := stdjson.Unmarshal(raw.ID, &s); err == nil {
		c.ID = s
		return nil
	}
	// Fall back to numeric form (legacy integer ids). Strip leading/
	// trailing whitespace that JSON might surround the bare number with;
	// the rest of the literal goes through verbatim so we keep the
	// caller's exact representation.
	c.ID = strings.TrimSpace(string(raw.ID))
	return nil
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
