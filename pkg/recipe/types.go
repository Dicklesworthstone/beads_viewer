package recipe

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Recipe defines a reusable view configuration for beads.
//
// Apply resolves membership, ordering and limits. The TUI consumes View and
// Metrics as presentation defaults when selecting a recipe.
type Recipe struct {
	Name        string       `yaml:"name" json:"name"`
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Filters     FilterConfig `yaml:"filters,omitempty" json:"filters,omitempty"`
	Sort        SortConfig   `yaml:"sort,omitempty" json:"sort,omitempty"`
	View        ViewConfig   `yaml:"view,omitempty" json:"view,omitempty"`
	Export      ExportConfig `yaml:"export,omitempty" json:"export,omitempty"`
	// Metrics lists analysis metrics a view should surface.
	// Setting Metrics also enables metric display in the TUI.
	Metrics []string `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

// FilterConfig defines which issues to include. Every field is honoured by
// Filter; the semantics are documented per field.
type FilterConfig struct {
	Status        []string `yaml:"status,omitempty" json:"status,omitempty"`                 // Keep issues whose status equals one of these (case-insensitive)
	Priority      []int    `yaml:"priority,omitempty" json:"priority,omitempty"`             // Keep issues whose priority is one of these (0 = highest)
	Type          []string `yaml:"type,omitempty" json:"type,omitempty"`                     // Keep issues whose issue_type equals one of these (case-insensitive)
	Assignee      []string `yaml:"assignee,omitempty" json:"assignee,omitempty"`             // Keep issues whose assignee matches one of these (case-insensitive; "unassigned" or "" matches blank)
	Repo          []string `yaml:"repo,omitempty" json:"repo,omitempty"`                     // Keep issues whose source_repo (or ID prefix) matches one of these (case-insensitive)
	Tags          []string `yaml:"tags,omitempty" json:"tags,omitempty"`                     // Keep issues carrying ALL of these labels (case-insensitive)
	ExcludeTags   []string `yaml:"exclude_tags,omitempty" json:"exclude_tags,omitempty"`     // Drop issues carrying ANY of these labels (case-insensitive)
	CreatedAfter  string   `yaml:"created_after,omitempty" json:"created_after,omitempty"`   // Keep issues created at or after this; relative ("14d", "2w", "1m", "1y") or ISO date
	CreatedBefore string   `yaml:"created_before,omitempty" json:"created_before,omitempty"` // Keep issues created at or before this; relative or ISO date
	UpdatedAfter  string   `yaml:"updated_after,omitempty" json:"updated_after,omitempty"`   // Keep issues updated at or after this; relative or ISO date
	UpdatedBefore string   `yaml:"updated_before,omitempty" json:"updated_before,omitempty"` // Keep issues updated at or before this; relative or ISO date
	HasBlockers   *bool    `yaml:"has_blockers,omitempty" json:"has_blockers,omitempty"`     // true = has an open blocking dependency, false = has none
	Actionable    *bool    `yaml:"actionable,omitempty" json:"actionable,omitempty"`         // true = no open blockers and not deferred, false = the complement
	TitleContains string   `yaml:"title_contains,omitempty" json:"title_contains,omitempty"` // Case-insensitive substring match on the title
	IDPrefix      string   `yaml:"id_prefix,omitempty" json:"id_prefix,omitempty"`           // e.g., "bv-" for project filtering
}

// SortConfig defines how to order issues.
//
// Field is one of the SortField* constants (created_at/updated_at are accepted
// as aliases of created/updated). Direction is "asc" or "desc"; when empty the
// field's natural direction applies: priority, title, id and status ascend,
// created, updated and the graph metrics (pagerank, betweenness, impact,
// triage) descend. Secondary breaks ties; issue ID (natural order) breaks
// any remaining tie so output never depends on input order.
type SortConfig struct {
	Field     string      `yaml:"field" json:"field"`
	Direction string      `yaml:"direction,omitempty" json:"direction,omitempty"`
	Secondary *SortConfig `yaml:"secondary,omitempty" json:"secondary,omitempty"`
}

// ViewConfig controls display options.
//
// MaxItems is applied by Apply. Other fields configure the existing TUI list,
// detail and graph views. Later navigation wins until another recipe is chosen.
type ViewConfig struct {
	Columns       []string `yaml:"columns,omitempty" json:"columns,omitempty"`               // Ordered list columns; empty keeps the normal adaptive row
	ShowGraph     bool     `yaml:"show_graph,omitempty" json:"show_graph,omitempty"`         // Start in the dependency graph
	ShowMetrics   bool     `yaml:"show_metrics,omitempty" json:"show_metrics,omitempty"`     // Show selected metric values on list rows and in issue details
	GroupBy       string   `yaml:"group_by,omitempty" json:"group_by,omitempty"`             // List groups: status, priority, assignee, tag/label (first sorted label), none
	Collapsed     bool     `yaml:"collapsed,omitempty" json:"collapsed,omitempty"`           // Start list groups collapsed; Enter toggles a group
	MaxItems      int      `yaml:"max_items,omitempty" json:"max_items,omitempty"`           // Keep only the first N issues after sorting (0 = unlimited)
	TruncateTitle int      `yaml:"truncate_title,omitempty" json:"truncate_title,omitempty"` // Maximum title display cells; 0 uses available width
}

// Presentation field names are shared by validation and the TUI renderer.
var ViewColumns = []string{"id", "title", "status", "priority", "assignee", "created", "updated", "tags", "blockers", "repo"}
var ViewMetrics = []string{"pagerank", "betweenness", "impact", "triage", "hub", "authority", "eigenvector", "kcore", "slack", "risk", "eta"}

var viewColumnAliases = map[string]string{
	"label":       "tags",
	"labels":      "tags",
	"source_repo": "repo",
}

// CanonicalViewColumn normalizes accepted column aliases (e.g. label/labels -> tags).
func CanonicalViewColumn(col string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(col))
	if alias, ok := viewColumnAliases[c]; ok {
		c = alias
	}
	for _, known := range ViewColumns {
		if c == known {
			return c, true
		}
	}
	return c, false
}

// ExportConfig supplies defaults only when an export is explicitly requested.
type ExportConfig struct {
	Format         string `yaml:"format,omitempty" json:"format,omitempty"`                   // markdown, json, csv, mermaid
	IncludeGraph   *bool  `yaml:"include_graph,omitempty" json:"include_graph,omitempty"`     // nil uses the format default
	IncludeActions *bool  `yaml:"include_actions,omitempty" json:"include_actions,omitempty"` // nil uses the format default (true)
	Template       string `yaml:"template,omitempty" json:"template,omitempty"`               // Markdown template path; user defaults resolve relative to their config file

	// Only the loader can authorize a default from the invoking user's config.
	// Bind that authority to the exact resolved path, so copying this value and
	// replacing Template does not authorize a different file. YAML/JSON cannot
	// supply this field, and project overrides never inherit it.
	authorizedTemplate string
}

// TemplateAuthorizedByUser reports whether Template is the unchanged path
// loaded from the invoking user's recipe configuration. Project recipes and
// directly loaded recipe files need an explicit export-template override.
func (e ExportConfig) TemplateAuthorizedByUser() bool {
	return e.Template != "" && e.Template == e.authorizedTemplate
}

// Sort fields accepted by SortConfig.Field.
const (
	SortFieldPriority    = "priority"
	SortFieldCreated     = "created"
	SortFieldUpdated     = "updated"
	SortFieldTitle       = "title"
	SortFieldID          = "id"
	SortFieldStatus      = "status"
	SortFieldAssignee    = "assignee"
	SortFieldPageRank    = "pagerank"    // Metrics.Graph
	SortFieldBetweenness = "betweenness" // Metrics.Graph
	SortFieldImpact      = "impact"      // Metrics.Graph (critical-path score)
	SortFieldTriage      = "triage"      // Metrics.Triage
	SortFieldRisk        = "risk"        // Metrics.Risk (composite operational risk)
	SortFieldETA         = "eta"         // Metrics.ETA (estimated days to completion)
	SortFieldSlack       = "slack"       // Metrics.Slack (schedule slack; 0 = critical path)
	SortFieldKCore       = "kcore"       // Metrics.KCore (k-core membership number)
)

// sortFieldAliases maps accepted spellings to their canonical field.
var sortFieldAliases = map[string]string{
	"created_at": SortFieldCreated,
	"updated_at": SortFieldUpdated,
	"risk_score": SortFieldRisk,
	"k_core":     SortFieldKCore,
	"core":       SortFieldKCore,
}

// canonicalSortField normalises case and aliases; ok is false for unknown fields.
func canonicalSortField(field string) (string, bool) {
	f := strings.ToLower(strings.TrimSpace(field))
	if alias, ok := sortFieldAliases[f]; ok {
		f = alias
	}
	switch f {
	case SortFieldPriority, SortFieldCreated, SortFieldUpdated, SortFieldTitle, SortFieldID, SortFieldStatus, SortFieldAssignee,
		SortFieldPageRank, SortFieldBetweenness, SortFieldImpact, SortFieldTriage,
		SortFieldRisk, SortFieldETA, SortFieldSlack, SortFieldKCore:
		return f, true
	}
	return f, false
}

// isGraphMetricField reports whether field reads Metrics.Graph (or GraphStats-derived Slack/KCore).
func isGraphMetricField(field string) bool {
	return field == SortFieldPageRank || field == SortFieldBetweenness || field == SortFieldImpact ||
		field == SortFieldSlack || field == SortFieldKCore
}

// SortChain returns the sort configs in tie-break order: primary, secondary,
// secondary's secondary, and so on. Entries with an empty field are skipped.
func (r *Recipe) SortChain() []SortConfig {
	if r == nil {
		return nil
	}
	var chain []SortConfig
	for s := &r.Sort; s != nil; s = s.Secondary {
		if strings.TrimSpace(s.Field) != "" {
			chain = append(chain, *s)
		}
	}
	return chain
}

// NeedsGraphMetrics reports whether the recipe sorts by pagerank, betweenness
// or impact, so callers can decide whether to run graph analysis first.
func (r *Recipe) NeedsGraphMetrics() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && isGraphMetricField(f) {
			return true
		}
	}
	return false
}

// NeedsTriageScores reports whether the recipe sorts by triage score.
func (r *Recipe) NeedsTriageScores() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && f == SortFieldTriage {
			return true
		}
	}
	return false
}

// NeedsRiskScores reports whether the recipe sorts by composite operational risk.
func (r *Recipe) NeedsRiskScores() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && f == SortFieldRisk {
			return true
		}
	}
	return false
}

// NeedsETAScores reports whether the recipe sorts by ETA days.
func (r *Recipe) NeedsETAScores() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && f == SortFieldETA {
			return true
		}
	}
	return false
}

// NeedsSlackScores reports whether the recipe sorts by schedule slack.
func (r *Recipe) NeedsSlackScores() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && f == SortFieldSlack {
			return true
		}
	}
	return false
}

// NeedsKCoreScores reports whether the recipe sorts by k-core number.
func (r *Recipe) NeedsKCoreScores() bool {
	for _, s := range r.SortChain() {
		if f, ok := canonicalSortField(s.Field); ok && f == SortFieldKCore {
			return true
		}
	}
	return false
}

// Validate rejects recipes Apply cannot honour: unknown sort fields or
// directions, malformed time filters, blank status values and a negative
// max_items, plus unsupported presentation fields. Status is deliberately an
// open vocabulary: br projects define custom workflow states such as "done" or
// "qa-review", so any nonblank value is accepted and matched case-insensitively
// against the loaded issues.
func (r *Recipe) Validate() error {
	if r == nil {
		return errors.New("recipe is nil")
	}
	var problems []string
	for s := &r.Sort; s != nil; s = s.Secondary {
		field := strings.TrimSpace(s.Field)
		dir := strings.ToLower(strings.TrimSpace(s.Direction))
		if field != "" {
			if _, ok := canonicalSortField(field); !ok {
				problems = append(problems, fmt.Sprintf("sort.field %q is not one of %s", s.Field, strings.Join(knownSortFields(), ", ")))
			}
		}
		switch dir {
		case "", "asc", "desc":
		default:
			problems = append(problems, fmt.Sprintf("sort.direction %q must be asc or desc", s.Direction))
		}
	}
	now := time.Now()
	for _, tf := range []struct{ key, value string }{
		{"filters.created_after", r.Filters.CreatedAfter},
		{"filters.created_before", r.Filters.CreatedBefore},
		{"filters.updated_after", r.Filters.UpdatedAfter},
		{"filters.updated_before", r.Filters.UpdatedBefore},
	} {
		if tf.value == "" {
			continue
		}
		if _, err := ParseRelativeTime(tf.value, now); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", tf.key, err))
		}
	}
	for _, s := range r.Filters.Status {
		if !model.Status(strings.ToLower(strings.TrimSpace(s))).IsValid() {
			problems = append(problems, fmt.Sprintf("filters.status %q must not be blank", s))
		}
	}
	for _, t := range r.Filters.Type {
		if strings.TrimSpace(t) == "" {
			problems = append(problems, fmt.Sprintf("filters.type %q must not be blank", t))
		}
	}
	for _, rp := range r.Filters.Repo {
		if strings.TrimSpace(rp) == "" {
			problems = append(problems, fmt.Sprintf("filters.repo %q must not be blank", rp))
		}
	}
	if r.View.MaxItems < 0 {
		problems = append(problems, fmt.Sprintf("view.max_items %d must not be negative", r.View.MaxItems))
	}
	if r.View.TruncateTitle < 0 {
		problems = append(problems, "view.truncate_title must not be negative")
	}
	seenCols := make(map[string]bool)
	for _, value := range r.View.Columns {
		canon, valid := CanonicalViewColumn(value)
		if !valid {
			problems = append(problems, fmt.Sprintf("view.columns %q must be one of %s (or label/labels/source_repo)", value, strings.Join(ViewColumns, ", ")))
		}
		if seenCols[canon] {
			problems = append(problems, fmt.Sprintf("view.columns repeats %q", value))
		}
		seenCols[canon] = true
	}
	seenMetrics := make(map[string]bool)
	for _, value := range r.Metrics {
		canon := strings.ToLower(strings.TrimSpace(value))
		valid := false
		for _, known := range ViewMetrics {
			if canon == known {
				valid = true
				break
			}
		}
		if !valid {
			problems = append(problems, fmt.Sprintf("metrics %q must be one of %s", value, strings.Join(ViewMetrics, ", ")))
		}
		if seenMetrics[canon] {
			problems = append(problems, fmt.Sprintf("metrics repeats %q", value))
		}
		seenMetrics[canon] = true
	}
	groupBy := strings.ToLower(strings.TrimSpace(r.View.GroupBy))
	switch groupBy {
	case "", "none", "status", "priority", "assignee", "tag", "tags", "label", "labels", "repo", "source_repo":
	default:
		problems = append(problems, fmt.Sprintf("view.group_by %q must be status, priority, assignee, tag, label, repo or none", r.View.GroupBy))
	}
	if r.View.Collapsed && (groupBy == "" || groupBy == "none") {
		problems = append(problems, "view.collapsed requires view.group_by")
	}
	exportFormat := strings.ToLower(strings.TrimSpace(r.Export.Format))
	switch exportFormat {
	case "", "markdown", "html", "json", "csv", "mermaid":
	default:
		problems = append(problems, fmt.Sprintf("export.format %q must be markdown, html, json, csv or mermaid", r.Export.Format))
	}
	if len(problems) == 0 {
		for i, value := range r.Metrics {
			r.Metrics[i] = strings.ToLower(strings.TrimSpace(value))
		}
		r.Export.Format = exportFormat
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func knownSortFields() []string {
	return []string{SortFieldPriority, SortFieldCreated, SortFieldUpdated, SortFieldTitle, SortFieldID, SortFieldStatus, SortFieldAssignee,
		SortFieldPageRank, SortFieldBetweenness, SortFieldImpact, SortFieldTriage,
		SortFieldRisk, SortFieldETA, SortFieldSlack, SortFieldKCore}
}

// relativeTimePattern matches relative time expressions like "14d", "2w", "1m", "1y"
var relativeTimePattern = regexp.MustCompile(`^(\d+)([dwmy])$`)

// ParseRelativeTime converts a relative time string to an absolute time.
// Supports: Nd (days), Nw (weeks), Nm (months), Ny (years)
// If the string is not a relative time, it tries to parse as ISO 8601.
// Returns zero time if parsing fails.
func ParseRelativeTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}

	s = strings.TrimSpace(s)

	// Try relative time first (case-insensitive)
	if matches := relativeTimePattern.FindStringSubmatch(strings.ToLower(s)); matches != nil {
		n, err := strconv.Atoi(matches[1])
		if err != nil || n < 0 || n > 365000 {
			return time.Time{}, &TimeParseError{Input: s}
		}
		unit := matches[2]

		switch unit {
		case "d":
			return now.AddDate(0, 0, -n), nil
		case "w":
			return now.AddDate(0, 0, -n*7), nil
		case "m":
			return now.AddDate(0, -n, 0), nil
		case "y":
			return now.AddDate(-n, 0, 0), nil
		}
	}

	// Try ISO 8601 formats (preserve case for parsing)
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	for _, format := range formats {
		// Use ParseInLocation to respect the reference time's location (e.g., Local)
		// This ensures date-only strings ("2024-01-01") are interpreted as midnight
		// in the user's timezone, not UTC.
		if t, err := time.ParseInLocation(format, s, now.Location()); err == nil {
			return t, nil
		}
	}

	return time.Time{}, &TimeParseError{Input: s}
}

// TimeParseError indicates a time parsing failure
type TimeParseError struct {
	Input string
}

func (e *TimeParseError) Error() string {
	return "invalid time format: " + e.Input + " (expected relative like '14d', '2w', '1m' or ISO date)"
}
