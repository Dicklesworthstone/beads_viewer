package recipe

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// GraphMetrics supplies per-issue graph scores for the metric sort fields.
// *analysis.GraphStats satisfies it; pkg/recipe deliberately does not import
// pkg/analysis so the TUI and the robot path can both hand in whatever stats
// they already computed.
type GraphMetrics interface {
	GetPageRankScore(id string) float64
	GetBetweennessScore(id string) float64
	GetCriticalPathScore(id string) float64
}

// Metrics carries the lookups a recipe sort may need. A nil Graph makes
// pagerank/betweenness/impact read as 0 and a nil Triage makes triage read as
// 0, so ties fall through to the secondary sort and the ID tie-break. Callers
// that want a meaningful metric order must supply the corresponding source;
// Recipe.NeedsGraphMetrics and NeedsTriageScores say which are required.
type Metrics struct {
	Graph     GraphMetrics
	Triage    map[string]float64    // issue ID -> triage score
	Readiness *model.ReadinessIndex // Full dependency authority before display scoping
}

func (m Metrics) score(field, id string) float64 {
	switch field {
	case SortFieldPageRank:
		if m.Graph != nil {
			return m.Graph.GetPageRankScore(id)
		}
	case SortFieldBetweenness:
		if m.Graph != nil {
			return m.Graph.GetBetweennessScore(id)
		}
	case SortFieldImpact:
		if m.Graph != nil {
			return m.Graph.GetCriticalPathScore(id)
		}
	case SortFieldTriage:
		if m.Triage != nil {
			return m.Triage[id]
		}
	}
	return 0
}

// Apply is the one recipe engine shared by the TUI and the robot path. It
// filters issues with r.Filters as of now, orders them by the sort chain (see
// SortConfig) using metrics for the graph fields, and keeps at most
// r.View.MaxItems when that is positive. The input slice is never mutated; a
// nil recipe returns issues unchanged. A zero now means time.Now().
func Apply(issues []model.Issue, metrics Metrics, r *Recipe, now time.Time) ([]model.Issue, error) {
	if r == nil {
		return issues, nil
	}
	filtered, err := filter(issues, r, now, metrics.Readiness)
	if err != nil {
		return nil, err
	}
	SortIssues(filtered, metrics, r)
	if r.View.MaxItems > 0 && len(filtered) > r.View.MaxItems {
		filtered = filtered[:r.View.MaxItems]
	}
	return filtered, nil
}

// Filter returns the issues matching every filter in r.Filters, in input
// order, as a new slice. Blocker checks look at the full input set, so a
// blocker excluded by the status filter still counts as blocking. Issues
// without a created/updated timestamp are not excluded by the date filters.
// Malformed time filters are reported rather than skipped.
func Filter(issues []model.Issue, r *Recipe, now time.Time) ([]model.Issue, error) {
	return filter(issues, r, now, nil)
}

func filter(issues []model.Issue, r *Recipe, now time.Time, readiness *model.ReadinessIndex) ([]model.Issue, error) {
	if r == nil {
		return append([]model.Issue(nil), issues...), nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	f := r.Filters

	thresholds, err := parseTimeFilters(f, now)
	if err != nil {
		return nil, err
	}

	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}

	result := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if !matchesStatus(issue, f.Status) ||
			!matchesPriority(issue, f.Priority) ||
			!hasAllTags(issue, f.Tags) ||
			hasAnyTag(issue, f.ExcludeTags) ||
			!thresholds.matches(issue) {
			continue
		}
		if f.HasBlockers != nil && *f.HasBlockers != (readiness.DependencyState(issue.ID) != model.DependenciesSatisfied) {
			continue
		}
		if f.Actionable != nil {
			actionable := readiness.Ready(issue.ID, now)
			if *f.Actionable != actionable {
				continue
			}
		}
		if f.TitleContains != "" && !strings.Contains(strings.ToLower(issue.Title), strings.ToLower(f.TitleContains)) {
			continue
		}
		if f.IDPrefix != "" && !strings.HasPrefix(issue.ID, f.IDPrefix) {
			continue
		}
		result = append(result, issue)
	}
	return result, nil
}

type timeThresholds struct {
	createdAfter, createdBefore, updatedAfter, updatedBefore time.Time
}

func parseTimeFilters(f FilterConfig, now time.Time) (timeThresholds, error) {
	var t timeThresholds
	var err error
	if t.createdAfter, err = parseTimeFilter("created_after", f.CreatedAfter, now); err != nil {
		return t, err
	}
	if t.createdBefore, err = parseTimeFilter("created_before", f.CreatedBefore, now); err != nil {
		return t, err
	}
	if t.updatedAfter, err = parseTimeFilter("updated_after", f.UpdatedAfter, now); err != nil {
		return t, err
	}
	if t.updatedBefore, err = parseTimeFilter("updated_before", f.UpdatedBefore, now); err != nil {
		return t, err
	}
	return t, nil
}

func parseTimeFilter(key, value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	threshold, err := ParseRelativeTime(value, now)
	if err != nil {
		return time.Time{}, fmt.Errorf("filters.%s: %w", key, err)
	}
	return threshold, nil
}

func (t timeThresholds) matches(issue model.Issue) bool {
	if !t.createdAfter.IsZero() && !issue.CreatedAt.IsZero() && issue.CreatedAt.Before(t.createdAfter) {
		return false
	}
	if !t.createdBefore.IsZero() && !issue.CreatedAt.IsZero() && issue.CreatedAt.After(t.createdBefore) {
		return false
	}
	if !t.updatedAfter.IsZero() && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.Before(t.updatedAfter) {
		return false
	}
	if !t.updatedBefore.IsZero() && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.After(t.updatedBefore) {
		return false
	}
	return true
}

func matchesStatus(issue model.Issue, statuses []string) bool {
	if len(statuses) == 0 {
		return true
	}
	for _, s := range statuses {
		if strings.EqualFold(string(issue.Status), strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func matchesPriority(issue model.Issue, priorities []int) bool {
	if len(priorities) == 0 {
		return true
	}
	for _, p := range priorities {
		if issue.Priority == p {
			return true
		}
	}
	return false
}

func hasLabel(issue model.Issue, tag string) bool {
	for _, label := range issue.Labels {
		if strings.EqualFold(label, tag) {
			return true
		}
	}
	return false
}

func hasAllTags(issue model.Issue, tags []string) bool {
	for _, tag := range tags {
		if !hasLabel(issue, tag) {
			return false
		}
	}
	return true
}

func hasAnyTag(issue model.Issue, tags []string) bool {
	for _, tag := range tags {
		if hasLabel(issue, tag) {
			return true
		}
	}
	return false
}

// SortIssues orders issues in place by r's sort chain. Each level compares
// its field in its (possibly defaulted) direction and falls through to the
// next level on a tie; natural issue-ID order breaks any remaining tie, so the
// result is deterministic regardless of input order. Unknown fields compare
// equal (Validate rejects them at load time). A nil recipe or empty chain
// leaves the slice untouched.
func SortIssues(issues []model.Issue, metrics Metrics, r *Recipe) {
	chain := r.SortChain()
	if len(chain) == 0 {
		return
	}
	levels := make([]sortLevel, 0, len(chain))
	for _, s := range chain {
		field, _ := canonicalSortField(s.Field)
		levels = append(levels, sortLevel{field: field, desc: sortDescending(field, s.Direction)})
	}
	sort.SliceStable(issues, func(i, j int) bool {
		for _, lvl := range levels {
			cmp := compareIssues(issues[i], issues[j], lvl.field, metrics)
			if cmp == 0 {
				continue
			}
			if lvl.desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return naturalLess(issues[i].ID, issues[j].ID)
	})
}

type sortLevel struct {
	field string
	desc  bool
}

// sortDescending resolves an explicit direction, or the field's natural one:
// newest-first for dates and highest-first for metrics, ascending otherwise.
func sortDescending(field, direction string) bool {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "desc":
		return true
	case "asc":
		return false
	}
	switch field {
	case SortFieldCreated, SortFieldUpdated, SortFieldPageRank, SortFieldBetweenness, SortFieldImpact, SortFieldTriage:
		return true
	}
	return false
}

// compareIssues returns -1, 0 or 1 ordering a before b in ascending order of field.
func compareIssues(a, b model.Issue, field string, metrics Metrics) int {
	switch field {
	case SortFieldPriority:
		return compareInts(a.Priority, b.Priority)
	case SortFieldCreated:
		return compareTimes(a.CreatedAt, b.CreatedAt)
	case SortFieldUpdated:
		return compareTimes(a.UpdatedAt, b.UpdatedAt)
	case SortFieldTitle:
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case SortFieldID:
		switch {
		case naturalLess(a.ID, b.ID):
			return -1
		case naturalLess(b.ID, a.ID):
			return 1
		}
		return 0
	case SortFieldStatus:
		return strings.Compare(string(a.Status), string(b.Status))
	case SortFieldPageRank, SortFieldBetweenness, SortFieldImpact, SortFieldTriage:
		return compareFloats(metrics.score(field, a.ID), metrics.score(field, b.ID))
	}
	return 0
}

func compareInts(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareFloats(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareTimes(a, b time.Time) int {
	switch {
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

// naturalLess orders IDs sharing a prefix by their trailing number
// ("bv-2" < "bv-10") and everything else lexically.
func naturalLess(s1, s2 string) bool {
	split := func(s string) (string, int, bool) {
		lastDigit := -1
		for i := len(s) - 1; i >= 0; i-- {
			if s[i] < '0' || s[i] > '9' {
				break
			}
			lastDigit = i
		}
		if lastDigit == -1 {
			return s, 0, false
		}
		num, err := strconv.Atoi(s[lastDigit:])
		if err != nil {
			return s, 0, false
		}
		return s[:lastDigit], num, true
	}
	p1, n1, ok1 := split(s1)
	p2, n2, ok2 := split(s2)
	if ok1 && ok2 && p1 == p2 {
		return n1 < n2
	}
	return s1 < s2
}
