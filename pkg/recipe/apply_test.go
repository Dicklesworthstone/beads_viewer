package recipe_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// stubGraph is a GraphMetrics stand-in keyed by issue ID.
type stubGraph struct {
	pagerank, betweenness, impact map[string]float64
}

func (s stubGraph) GetPageRankScore(id string) float64     { return s.pagerank[id] }
func (s stubGraph) GetBetweennessScore(id string) float64  { return s.betweenness[id] }
func (s stubGraph) GetCriticalPathScore(id string) float64 { return s.impact[id] }

func ptrBool(b bool) *bool { return &b }

func ids(issues []model.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.ID)
	}
	return out
}

func requireIDs(t *testing.T, got []model.Issue, want ...string) {
	t.Helper()
	gotIDs := ids(got)
	if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("issue order mismatch\n got: %v\nwant: %v", gotIDs, want)
	}
}

func mustApply(t *testing.T, issues []model.Issue, metrics recipe.Metrics, r *recipe.Recipe, now time.Time) []model.Issue {
	t.Helper()
	got, err := recipe.Apply(issues, metrics, r, now)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return got
}

func mustFilter(t *testing.T, issues []model.Issue, r *recipe.Recipe, now time.Time) []model.Issue {
	t.Helper()
	got, err := recipe.Filter(issues, r, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	return got
}

func builtin(t *testing.T, name string) *recipe.Recipe {
	t.Helper()
	loader := recipe.NewLoader(recipe.WithUserPath("/nonexistent/recipes.yaml"), recipe.WithProjectDir("/nonexistent/project"))
	if err := loader.Load(); err != nil {
		t.Fatalf("load builtins: %v", err)
	}
	r := loader.Get(name)
	if r == nil {
		t.Fatalf("builtin recipe %q missing", name)
	}
	return r
}

func TestApply_MetricSortsAndSecondary(t *testing.T) {
	// Deliberately shuffled input; C and D tie on pagerank so the secondary
	// (priority asc) must decide, and E/F tie on both so natural ID order must.
	issues := []model.Issue{
		{ID: "bv-3", Title: "C", Status: model.StatusOpen, Priority: 3},
		{ID: "bv-1", Title: "A", Status: model.StatusOpen, Priority: 2},
		{ID: "bv-10", Title: "F", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-4", Title: "D", Status: model.StatusOpen, Priority: 0},
		{ID: "bv-2", Title: "B", Status: model.StatusInProgress, Priority: 1},
		{ID: "bv-9", Title: "E", Status: model.StatusOpen, Priority: 1},
		{ID: "bv-5", Title: "closed", Status: model.StatusClosed, Priority: 0},
	}
	graph := stubGraph{
		pagerank:    map[string]float64{"bv-1": 0.9, "bv-2": 0.7, "bv-3": 0.5, "bv-4": 0.5, "bv-9": 0.1, "bv-10": 0.1, "bv-5": 1.0},
		betweenness: map[string]float64{"bv-1": 1, "bv-2": 5, "bv-3": 3},
		impact:      map[string]float64{"bv-1": 0.2, "bv-2": 0.8},
	}
	metrics := recipe.Metrics{Graph: graph}

	highImpact := builtin(t, "high-impact")
	if !highImpact.NeedsGraphMetrics() || highImpact.NeedsTriageScores() {
		t.Fatalf("high-impact should need graph metrics only")
	}
	got := mustApply(t, issues, metrics, highImpact, time.Now())
	// bv-5 is closed and filtered out despite the top pagerank; bv-3/bv-4 tie
	// on pagerank and fall through to priority asc; bv-9/bv-10 tie on both and
	// fall through to natural ID order (bv-9 before bv-10).
	requireIDs(t, got, "bv-1", "bv-2", "bv-4", "bv-3", "bv-9", "bv-10")

	// Without a graph source every score reads as 0 and the secondary sort
	// carries the order: priority asc, then natural ID.
	got = mustApply(t, issues, recipe.Metrics{}, highImpact, time.Now())
	requireIDs(t, got, "bv-4", "bv-2", "bv-9", "bv-10", "bv-1", "bv-3")

	// Betweenness sort, explicit ascending; missing scores read as 0 (bv-10).
	r := &recipe.Recipe{Sort: recipe.SortConfig{Field: recipe.SortFieldBetweenness, Direction: "asc"}}
	got = mustApply(t, issues[:3], metrics, r, time.Now())
	requireIDs(t, got, "bv-10", "bv-1", "bv-3")

	// Impact (critical path) defaults to descending.
	r = &recipe.Recipe{Sort: recipe.SortConfig{Field: recipe.SortFieldImpact}}
	got = mustApply(t, issues[:5], metrics, r, time.Now())
	requireIDs(t, got, "bv-2", "bv-1", "bv-3", "bv-4", "bv-10")

	// Triage scores come from Metrics.Triage, not the graph.
	triage := builtin(t, "triage")
	if !triage.NeedsTriageScores() || triage.NeedsGraphMetrics() {
		t.Fatalf("triage should need triage scores only")
	}
	got = mustApply(t, issues, recipe.Metrics{Triage: map[string]float64{"bv-3": 0.9, "bv-1": 0.4, "bv-2": 0.4}}, triage, time.Now())
	requireIDs(t, got, "bv-3", "bv-2", "bv-1", "bv-4", "bv-9", "bv-10")

	// A tertiary level in the chain is honoured too.
	r = &recipe.Recipe{Sort: recipe.SortConfig{
		Field: recipe.SortFieldPageRank,
		Secondary: &recipe.SortConfig{
			Field:     recipe.SortFieldStatus,
			Secondary: &recipe.SortConfig{Field: recipe.SortFieldTitle, Direction: "desc"},
		},
	}}
	tied := []model.Issue{
		{ID: "t-1", Title: "alpha", Status: model.StatusOpen},
		{ID: "t-2", Title: "beta", Status: model.StatusOpen},
		{ID: "t-3", Title: "gamma", Status: model.StatusBlocked},
	}
	got = mustApply(t, tied, recipe.Metrics{}, r, time.Now())
	requireIDs(t, got, "t-3", "t-2", "t-1")
}

func TestApply_MaxItems(t *testing.T) {
	issues := make([]model.Issue, 0, 30)
	for i := 1; i <= 30; i++ {
		issues = append(issues, model.Issue{ID: "q-" + strings.Repeat("0", 2-len(itoa(i))) + itoa(i), Title: "t", Status: model.StatusOpen, Priority: i % 4})
	}
	quickWins := builtin(t, "quick-wins")
	if quickWins.View.MaxItems != 15 {
		t.Fatalf("quick-wins max_items = %d, want 15", quickWins.View.MaxItems)
	}
	got := mustApply(t, issues, recipe.Metrics{}, quickWins, time.Now())
	if len(got) != 15 {
		t.Fatalf("quick-wins returned %d issues, want 15", len(got))
	}
	for _, issue := range got {
		if issue.Priority != 2 && issue.Priority != 3 {
			t.Fatalf("quick-wins kept priority %d issue %s", issue.Priority, issue.ID)
		}
	}
	// Truncation happens after sorting: the kept items are the best-ranked ones.
	for i := 1; i < len(got); i++ {
		if got[i-1].Priority > got[i].Priority {
			t.Fatalf("quick-wins not sorted by priority: %v", ids(got))
		}
	}

	// max_items larger than the result set is a no-op; zero means unlimited.
	r := &recipe.Recipe{View: recipe.ViewConfig{MaxItems: 100}}
	if got := mustApply(t, issues, recipe.Metrics{}, r, time.Now()); len(got) != 30 {
		t.Fatalf("max_items 100 kept %d of 30", len(got))
	}
	r.View.MaxItems = 0
	if got := mustApply(t, issues, recipe.Metrics{}, r, time.Now()); len(got) != 30 {
		t.Fatalf("max_items 0 kept %d of 30", len(got))
	}

	// Input is never mutated by Apply.
	before := strings.Join(ids(issues), ",")
	mustApply(t, issues, recipe.Metrics{}, &recipe.Recipe{Sort: recipe.SortConfig{Field: "priority"}, View: recipe.ViewConfig{MaxItems: 3}}, time.Now())
	if after := strings.Join(ids(issues), ","); after != before {
		t.Fatalf("Apply mutated its input:\n before %s\n after  %s", before, after)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestFilter_StatusPriorityTagsTitlePrefix(t *testing.T) {
	issues := []model.Issue{
		{ID: "UI-1", Title: "Add login button", Status: model.StatusOpen, Priority: 1, Labels: []string{"Frontend", "p0"}},
		{ID: "API-2", Title: "Login endpoint", Status: model.StatusInProgress, Priority: 2, Labels: []string{"backend"}},
		{ID: "API-3", Title: "Health check", Status: model.StatusClosed, Priority: 2, Labels: []string{"backend", "ops"}},
		{ID: "API-4", Title: "Deleted", Status: model.StatusTombstone, Priority: 0},
	}
	now := time.Now()

	r := &recipe.Recipe{Filters: recipe.FilterConfig{Status: []string{"OPEN", "in_progress"}}}
	requireIDs(t, mustFilter(t, issues, r, now), "UI-1", "API-2")

	// "closed" matches exactly: tombstones are not closed issues.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Status: []string{"closed"}}}
	requireIDs(t, mustFilter(t, issues, r, now), "API-3")

	r = &recipe.Recipe{Filters: recipe.FilterConfig{Priority: []int{2}}}
	requireIDs(t, mustFilter(t, issues, r, now), "API-2", "API-3")

	// tags: ALL required, case-insensitive; exclude_tags: ANY drops.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Tags: []string{"backend"}}}
	requireIDs(t, mustFilter(t, issues, r, now), "API-2", "API-3")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Tags: []string{"backend", "OPS"}}}
	requireIDs(t, mustFilter(t, issues, r, now), "API-3")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Tags: []string{"frontend"}, ExcludeTags: []string{"P0"}}}
	requireIDs(t, mustFilter(t, issues, r, now))

	r = &recipe.Recipe{Filters: recipe.FilterConfig{TitleContains: "LOGIN", IDPrefix: "API"}}
	requireIDs(t, mustFilter(t, issues, r, now), "API-2")

	// A nil recipe keeps everything (as a copy).
	requireIDs(t, mustFilter(t, issues, nil, now), "UI-1", "API-2", "API-3", "API-4")
}

func TestFilter_DateWindows(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	issues := []model.Issue{
		{ID: "fresh", CreatedAt: now.Add(-1 * day), UpdatedAt: now.Add(-1 * day)},
		{ID: "week", CreatedAt: now.Add(-8 * day), UpdatedAt: now.Add(-8 * day)},
		{ID: "old", CreatedAt: now.Add(-40 * day), UpdatedAt: now.Add(-40 * day)},
		{ID: "undated"},
	}

	// created_after / updated_after keep recent issues; undated ones are not excluded.
	r := &recipe.Recipe{Filters: recipe.FilterConfig{CreatedAfter: "7d"}}
	requireIDs(t, mustFilter(t, issues, r, now), "fresh", "undated")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{UpdatedAfter: "2w"}}
	requireIDs(t, mustFilter(t, issues, r, now), "fresh", "week", "undated")

	// created_before / updated_before keep older issues.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{CreatedBefore: "7d"}}
	requireIDs(t, mustFilter(t, issues, r, now), "week", "old", "undated")
	r = &recipe.Recipe{Filters: recipe.FilterConfig{UpdatedBefore: "30d"}}
	requireIDs(t, mustFilter(t, issues, r, now), "old", "undated")

	// A window combines both bounds; ISO dates work too.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{UpdatedAfter: "2026-07-01", UpdatedBefore: "3d"}}
	requireIDs(t, mustFilter(t, issues, r, now), "week", "old", "undated")

	// Malformed time strings are an error naming the field, never a silent skip.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{UpdatedAfter: "fortnight"}}
	if _, err := recipe.Filter(issues, r, now); err == nil || !strings.Contains(err.Error(), "filters.updated_after") {
		t.Fatalf("expected updated_after error, got %v", err)
	}
	r = &recipe.Recipe{Filters: recipe.FilterConfig{CreatedBefore: "soon"}}
	if _, err := recipe.Apply(issues, recipe.Metrics{}, r, now); err == nil || !strings.Contains(err.Error(), "filters.created_before") {
		t.Fatalf("expected created_before error, got %v", err)
	}
}

func TestFilter_BlockersActionableAndDeferral(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(90 * 24 * time.Hour)
	elapsed := now.Add(-time.Minute)
	blocks := func(on string) []*model.Dependency {
		return []*model.Dependency{{DependsOnID: on, Type: model.DepBlocks}}
	}
	issues := []model.Issue{
		{ID: "root", Status: model.StatusOpen},
		{ID: "closed-root", Status: model.StatusClosed},
		{ID: "gone-root", Status: model.StatusTombstone},
		{ID: "blocked", Status: model.StatusOpen, Dependencies: blocks("root")},
		{ID: "unblocked", Status: model.StatusOpen, Dependencies: blocks("closed-root")},
		{ID: "unblocked-tombstone", Status: model.StatusOpen, Dependencies: blocks("gone-root")},
		{ID: "dangling", Status: model.StatusOpen, Dependencies: blocks("missing")},
		{ID: "related", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "root", Type: model.DepRelated}, nil}},
		{ID: "deferred", Status: model.StatusOpen, DeferUntil: &future},
		{ID: "undeferred", Status: model.StatusOpen, DeferUntil: &elapsed},
	}

	r := &recipe.Recipe{Filters: recipe.FilterConfig{HasBlockers: ptrBool(true)}}
	requireIDs(t, mustFilter(t, issues, r, now), "blocked", "dangling")

	r = &recipe.Recipe{Filters: recipe.FilterConfig{HasBlockers: ptrBool(false)}}
	requireIDs(t, mustFilter(t, issues, r, now), "root", "closed-root", "gone-root", "unblocked", "unblocked-tombstone", "related", "deferred", "undeferred")

	// actionable: open/ongoing, proven dependency-ready, and not deferred.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}}
	requireIDs(t, mustFilter(t, issues, r, now), "root", "unblocked", "unblocked-tombstone", "related", "undeferred")

	// actionable: false is the complement, not a no-op.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(false)}}
	requireIDs(t, mustFilter(t, issues, r, now), "closed-root", "gone-root", "blocked", "dangling", "deferred")

	// A blocker hidden by the status filter still blocks.
	r = &recipe.Recipe{Filters: recipe.FilterConfig{Status: []string{"open"}, Actionable: ptrBool(true), IDPrefix: "block"}}
	requireIDs(t, mustFilter(t, issues, r, now))

	// The builtin actionable recipe combines status + actionable.
	got := mustApply(t, issues, recipe.Metrics{}, builtin(t, "actionable"), now)
	requireIDs(t, got, "related", "root", "unblocked", "unblocked-tombstone", "undeferred")
}

func TestApplyActionableRetainsFullDependencyAuthority(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	blocks := func(id string) []*model.Dependency {
		return []*model.Dependency{{DependsOnID: id, Type: model.DepBlocks}}
	}
	visible := []model.Issue{
		{ID: "api-chain", Status: model.StatusOpen, Dependencies: blocks("web")},
		{ID: "api-closed", Status: model.StatusOpen, Dependencies: blocks("done")},
		{ID: "api-tombstone", Status: model.StatusOpen, Dependencies: blocks("gone")},
		{ID: "api-child", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "parent", Type: model.DepParentChild}}},
		{ID: "api-missing", Status: model.StatusOpen, Dependencies: blocks("missing")},
		{ID: "api-ongoing", Status: model.StatusInProgress},
		{ID: "api-parked", Status: model.StatusBlocked},
	}
	full := append(append([]model.Issue{}, visible...), []model.Issue{
		{ID: "web", Status: model.StatusOpen, Dependencies: blocks("ops")},
		{ID: "ops", Status: model.StatusOpen},
		{ID: "done", Status: model.StatusClosed},
		{ID: "gone", Status: model.StatusTombstone},
		{ID: "parent", Status: model.StatusOpen, Dependencies: blocks("ops")},
	}...)
	r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}, Sort: recipe.SortConfig{Field: "id", Direction: "asc"}}
	metrics := recipe.Metrics{Readiness: model.NewReadinessIndex(full)}
	requireIDs(t, mustApply(t, visible, metrics, r, now), "api-closed", "api-ongoing", "api-tombstone")

	// The full-authority filter can prove closed predecessors satisfied even
	// when they are hidden. Without authority they remain unknown, not ready.
	requireIDs(t, mustApply(t, visible, recipe.Metrics{}, r, now), "api-ongoing")

	// Closing the inherited external blocker releases the child. The direct
	// web predecessor remains open, so api-chain is still withheld.
	for i := range full {
		if full[i].ID == "ops" {
			full[i].Status = model.StatusClosed
		}
	}
	metrics.Readiness = model.NewReadinessIndex(full)
	requireIDs(t, mustApply(t, visible, metrics, r, now), "api-child", "api-closed", "api-ongoing", "api-tombstone")

	// No selected candidates means no output, even with useful context.
	requireIDs(t, mustApply(t, nil, metrics, r, now))
}

func TestSortIssues_DefaultsAndFields(t *testing.T) {
	now := time.Now()
	issues := []model.Issue{
		{ID: "A", Title: "zzz", Priority: 2, Status: model.StatusOpen, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-30 * time.Minute)},
		{ID: "B", Title: "aaa", Priority: 0, Status: model.StatusBlocked, CreatedAt: now, UpdatedAt: now},
	}
	sorted := func(field, direction string) []model.Issue {
		cp := append([]model.Issue(nil), issues...)
		recipe.SortIssues(cp, recipe.Metrics{}, &recipe.Recipe{Sort: recipe.SortConfig{Field: field, Direction: direction}})
		return cp
	}

	requireIDs(t, sorted("priority", ""), "B", "A")     // priority defaults ascending (P0 first)
	requireIDs(t, sorted("priority", "desc"), "A", "B") // explicit desc
	requireIDs(t, sorted("created", ""), "B", "A")      // dates default newest first
	requireIDs(t, sorted("created_at", "asc"), "A", "B")
	requireIDs(t, sorted("updated", ""), "B", "A")
	requireIDs(t, sorted("updated_at", "ASC"), "A", "B")
	requireIDs(t, sorted("title", ""), "B", "A")
	requireIDs(t, sorted("title", "desc"), "A", "B")
	requireIDs(t, sorted("status", ""), "B", "A") // "blocked" < "open"

	// ID sorts naturally: bv-2 before bv-10.
	idIssues := []model.Issue{{ID: "bv-10"}, {ID: "bv-2"}, {ID: "bv-1"}, {ID: "x"}}
	recipe.SortIssues(idIssues, recipe.Metrics{}, &recipe.Recipe{Sort: recipe.SortConfig{Field: "id"}})
	requireIDs(t, idIssues, "bv-1", "bv-2", "bv-10", "x")

	// An unknown field compares equal (Validate rejects it at load time) and
	// the natural-ID tie-break makes the order deterministic.
	requireIDs(t, sorted("unknown", ""), "A", "B")

	// No sort field leaves input order alone.
	cp := []model.Issue{{ID: "z"}, {ID: "a"}}
	recipe.SortIssues(cp, recipe.Metrics{}, &recipe.Recipe{})
	requireIDs(t, cp, "z", "a")
	recipe.SortIssues(cp, recipe.Metrics{}, nil)
	requireIDs(t, cp, "z", "a")
}

func TestValidate_RejectsUnusableRecipes(t *testing.T) {
	cases := []struct {
		name string
		r    recipe.Recipe
		want string
	}{
		{"unknown sort field", recipe.Recipe{Sort: recipe.SortConfig{Field: "karma"}}, `sort.field "karma"`},
		{"unknown secondary field", recipe.Recipe{Sort: recipe.SortConfig{Field: "priority", Secondary: &recipe.SortConfig{Field: "vibes"}}}, `sort.field "vibes"`},
		{"bad direction", recipe.Recipe{Sort: recipe.SortConfig{Field: "priority", Direction: "down"}}, `sort.direction "down"`},
		{"bad created_after", recipe.Recipe{Filters: recipe.FilterConfig{CreatedAfter: "yesterday"}}, "filters.created_after"},
		{"bad updated_before", recipe.Recipe{Filters: recipe.FilterConfig{UpdatedBefore: "1 month"}}, "filters.updated_before"},
		{"unknown status", recipe.Recipe{Filters: recipe.FilterConfig{Status: []string{"open", "done"}}}, `filters.status "done"`},
		{"negative max_items", recipe.Recipe{View: recipe.ViewConfig{MaxItems: -1}}, "view.max_items -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}

	ok := recipe.Recipe{
		Filters: recipe.FilterConfig{Status: []string{"OPEN", "in_progress"}, UpdatedAfter: "14d", CreatedBefore: "2026-01-01"},
		Sort:    recipe.SortConfig{Field: "updated_at", Direction: "DESC", Secondary: &recipe.SortConfig{Field: "pagerank"}},
		View:    recipe.ViewConfig{MaxItems: 5},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid recipe rejected: %v", err)
	}
	var nilRecipe *recipe.Recipe
	if err := nilRecipe.Validate(); err == nil {
		t.Fatalf("nil recipe should not validate")
	}
}

func TestCompleteRecipeFieldsValidation(t *testing.T) {
	includeGraph := true
	full := recipe.Recipe{
		View: recipe.ViewConfig{
			Columns: []string{"id"}, ShowGraph: true, ShowMetrics: true, GroupBy: "status", Collapsed: true, MaxItems: 5, TruncateTitle: 40,
		},
		Export:  recipe.ExportConfig{Format: "markdown", IncludeGraph: &includeGraph, Template: "t.tmpl"},
		Metrics: []string{"pagerank"},
	}
	if err := full.Validate(); err != nil {
		t.Fatalf("valid combined presentation/export fields rejected: %v", err)
	}
	full.Export.Format = "shell"
	if err := full.Validate(); err == nil || !strings.Contains(err.Error(), "export.format") {
		t.Fatalf("invalid export format accepted: %v", err)
	}

	// Builtins continue to validate with the complete field schema.
	loader := recipe.NewLoader(recipe.WithUserPath("/nonexistent/recipes.yaml"), recipe.WithProjectDir("/nonexistent/project"))
	if err := loader.Load(); err != nil {
		t.Fatalf("load builtins: %v", err)
	}
	for _, r := range loader.List() {
		if err := r.Validate(); err != nil {
			t.Fatalf("builtin %s is invalid: %v", r.Name, err)
		}
	}
}
