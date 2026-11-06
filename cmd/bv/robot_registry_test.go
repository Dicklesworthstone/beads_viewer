package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestRobotCapacity_FullSourceReadiness(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1788912000")
	now := time.Unix(1788912000, 0).UTC()
	future := now.Add(time.Second)
	issues := []model.Issue{
		{ID: "outer", Status: model.StatusOpen, Labels: []string{"other"}},
		{ID: "ready", Status: model.StatusInProgress},
		{ID: "related", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "outer", Type: model.DepRelated}}},
		{ID: "missing", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "absent", Type: model.DepBlocks}}},
		{ID: "future", Status: model.StatusOpen, DeferUntil: &future},
		{ID: "due", Status: model.StatusOpen, DeferUntil: &now},
		{ID: "parked", Status: model.StatusBlocked},
		{ID: "review", Status: model.Status("qa-review")},
		{ID: "parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "outer", Type: model.DepBlocks}}},
		{ID: "child", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "parent", Type: model.DepParentChild}}},
		{ID: "resolved", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "done", Type: model.DepBlocks}, {DependsOnID: "gone", Type: model.DepConditionalBlocks}}},
		{ID: "done", Status: model.StatusClosed, Labels: []string{"other"}},
		{ID: "gone", Status: model.StatusTombstone, Labels: []string{"other"}},
	}
	selected := make(map[string]bool)
	var visible []model.Issue
	for i := range issues {
		if len(issues[i].Labels) == 0 {
			issues[i].Labels = []string{"focus"}
			selected[issues[i].ID] = true
			visible = append(visible, issues[i])
		}
	}
	for _, tc := range []struct {
		name       string
		issues     []model.Issue
		candidates map[string]bool
		label      string
		want       []string
		open       int
	}{
		{"unscoped", issues, nil, "", []string{"due", "outer", "ready", "related", "resolved"}, 11},
		{"global_candidates", issues, selected, "", []string{"due", "ready", "related", "resolved"}, 10},
		{"capacity_label", issues, nil, "focus", []string{"due", "ready", "related", "resolved"}, 10},
		{"authority_outside_graph", visible, nil, "", []string{"due", "ready", "related", "resolved"}, 10},
		{"empty_candidates", issues, map[string]bool{}, "", nil, 0},
		{"explicit_false", issues, map[string]bool{"related": true, "outer": false}, "focus", []string{"related"}, 1},
		{"disjoint_intersection", issues, map[string]bool{"outer": true}, "focus", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			ctx := RobotContext{Issues: tc.issues, CandidateIDs: tc.candidates, Readiness: model.NewReadinessIndex(issues), Encoder: json.NewEncoder(&out)}
			if err := handleRobotCapacity(ctx, phaseThreeRobotHandlerConfig{CapacityLabel: &tc.label}); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Actionable      []string `json:"actionable"`
				ActionableCount int      `json:"actionable_count"`
				Open            int      `json:"open_issue_count"`
				Total           int      `json:"total_minutes"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.Actionable, tc.want) || got.ActionableCount != len(tc.want) || got.Open != tc.open {
				t.Errorf("capacity=%+v, want ready=%v open=%d; output=%s", got, tc.want, tc.open, out.String())
			}
			if tc.open == 0 && got.Total != 0 || tc.open > 0 && got.Total <= 0 {
				t.Errorf("backlog duration does not match selected work: %+v", got)
			}
		})
	}
	writeErr := errors.New("capacity output unavailable")
	err := handleRobotCapacity(RobotContext{Encoder: json.NewEncoder(failingWriter{err: writeErr})}, phaseThreeRobotHandlerConfig{})
	if !errors.Is(err, writeErr) {
		t.Fatalf("output failure lost: %v", err)
	}
}

func TestRobotTriageRecommendationsUseAuthoritativeActionability(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1788912000")
	now := time.Unix(1788912000, 0).UTC()
	future := now.Add(time.Hour)
	issues := []model.Issue{
		{ID: "live", Title: "Ready leaf", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, Labels: []string{"lane"}},
		{ID: "ongoing", Status: model.StatusInProgress, Priority: 3, IssueType: model.TypeTask, Labels: []string{"lane"}},
		{ID: "future", Status: model.StatusOpen, DeferUntil: &future, Priority: 0, IssueType: model.TypeTask},
		{ID: "missing-blocker", Status: model.StatusOpen, Priority: 0, IssueType: model.TypeTask,
			Dependencies: []*model.Dependency{{DependsOnID: "absent", Type: model.DepBlocks}}},
		{ID: "closed", Status: model.StatusClosed, Priority: 0, IssueType: model.TypeTask},
		{ID: "tombstone", Status: model.StatusTombstone, Priority: 0, IssueType: model.TypeTask},
	}
	// Enough high-priority parked rows to catch filtering after the TopN cap.
	for i := 0; i < 16; i++ {
		issues = append(issues, model.Issue{ID: "deferred-" + strconv.Itoa(i), Status: model.StatusDeferred, Priority: 0, IssueType: model.TypeTask, Labels: []string{"lane"}})
	}
	for _, inactiveOnly := range []bool{false, true} {
		for _, mode := range []string{"full", "brief", "track", "label"} {
			t.Run(strconv.FormatBool(inactiveOnly)+"/"+mode, func(t *testing.T) {
				rows := append([]model.Issue(nil), issues...)
				want := []string{"live", "ongoing"}
				if inactiveOnly {
					rows[0].Status = model.StatusClosed
					rows[1].Status = model.StatusClosed
					want = nil
				}
				var out bytes.Buffer
				brief, track, label := mode == "brief", mode == "track", mode == "label"
				if err := handleRobotTriage(RobotContext{Issues: rows, Readiness: model.NewReadinessIndex(rows), Encoder: json.NewEncoder(&out)}, phaseThreeRobotHandlerConfig{
					RobotTriageBriefFlag: &brief, RobotTriageByTrackFlag: &track, RobotTriageByLabelFlag: &label,
				}); err != nil {
					t.Fatal(err)
				}
				var payload struct {
					Triage          analysis.TriageResult     `json:"triage"`
					Recommendations []analysis.Recommendation `json:"recommendations"`
				}
				if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if brief {
					payload.Triage.Recommendations = payload.Recommendations
				}
				var ids []string
				for _, rec := range payload.Triage.Recommendations {
					ids = append(ids, rec.ID)
					if !model.NewReadinessIndex(rows).Ready(rec.ID, now) {
						t.Errorf("nonactionable recommendation %s: %s", rec.ID, out.String())
					}
				}
				slices.Sort(ids)
				if !slices.Equal(ids, want) {
					t.Fatalf("recommendations=%v, want %v; output=%s", ids, want, out.String())
				}
				if mode != "brief" && payload.Triage.ProjectHealth.Counts.Total != len(rows) {
					t.Fatalf("filter removed health inventory: %+v", payload.Triage.ProjectHealth.Counts)
				}
				for _, group := range payload.Triage.RecommendationsByTrack {
					for _, rec := range group.Recommendations {
						if !slices.Contains(want, rec.ID) {
							t.Errorf("inactive track recommendation %s", rec.ID)
						}
					}
				}
				for _, group := range payload.Triage.RecommendationsByLabel {
					for _, rec := range group.Recommendations {
						if !slices.Contains(want, rec.ID) {
							t.Errorf("inactive label recommendation %s", rec.ID)
						}
					}
				}
			})
		}
	}
	t.Run("explicit_inactive_diagnostics", func(t *testing.T) {
		var out bytes.Buffer
		include := true
		if err := handleRobotTriage(RobotContext{Issues: issues[2:], Encoder: json.NewEncoder(&out)}, phaseThreeRobotHandlerConfig{IncludeInactive: &include}); err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Triage analysis.TriageResult `json:"triage"`
		}
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Triage.Recommendations) == 0 {
			t.Fatal("explicit diagnostics lost inactive planning guidance")
		}
		if payload.Triage.QuickRef.ActionableCount != 0 || len(payload.Triage.QuickRef.TopPicks) != 0 {
			t.Fatalf("explicit diagnostics made inactive work actionable: %s", out.String())
		}
		for _, rec := range payload.Triage.Recommendations {
			if rec.Claimable || rec.Actions.Claim != nil {
				t.Fatalf("explicit diagnostics enabled a claim for %s", rec.ID)
			}
		}
	})
}

func TestRobotCapacity_BlockingEdgesAndOrder(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1788912000")
	issues := []model.Issue{
		{ID: "z", Status: model.StatusOpen},
		{ID: "a", Status: model.StatusOpen},
		{ID: "b", Status: model.StatusOpen, Dependencies: []*model.Dependency{nil, {DependsOnID: "a", Type: model.DepBlocks}, {DependsOnID: "a", Type: model.DepBlocks}, {DependsOnID: "a", Type: model.DepConditionalBlocks}}},
		{ID: "c", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DependencyType("")}}},
		{ID: "d", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "z", Type: model.DepConditionalBlocks}}},
		{ID: "e", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "z", Type: model.DepWaitsFor}}},
		{ID: "related", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepRelated}}},
		{ID: "child", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepParentChild}}},
		{ID: "closed", Status: model.StatusClosed, Dependencies: []*model.Dependency{{DependsOnID: "a", Type: model.DepBlocks}}},
		{ID: "gone", Status: model.StatusTombstone, Dependencies: []*model.Dependency{{DependsOnID: "z", Type: model.DepBlocks}}},
	}
	var previous string
	for _, reverse := range []bool{false, true} {
		if reverse {
			slices.Reverse(issues)
		}
		var out bytes.Buffer
		if err := handleRobotCapacity(RobotContext{Issues: issues, Encoder: json.NewEncoder(&out)}, phaseThreeRobotHandlerConfig{}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Actionable   []string `json:"actionable"`
			CriticalPath []string `json:"critical_path"`
			Bottlenecks  []struct {
				ID     string   `json:"id"`
				Count  int      `json:"blocks_count"`
				Blocks []string `json:"blocks"`
			} `json:"bottlenecks"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Actionable, []string{"a", "child", "related", "z"}) || !slices.Equal(got.CriticalPath, []string{"a", "b"}) {
			t.Errorf("reverse=%v: wrong readiness or deterministic blocking path: %+v", reverse, got)
		}
		if len(got.Bottlenecks) != 2 {
			t.Errorf("reverse=%v: wrong bottleneck count: %+v", reverse, got.Bottlenecks)
		} else {
			for i, want := range [][]string{{"a", "b", "c"}, {"z", "d", "e"}} {
				b := got.Bottlenecks[i]
				if b.ID != want[0] || b.Count != 2 || !slices.Equal(b.Blocks, want[1:]) {
					t.Errorf("reverse=%v: bottleneck=%+v, want %v", reverse, b, want)
				}
			}
		}
		if reverse && out.String() != previous {
			t.Error("capacity JSON changed with input ordering")
		}
		previous = out.String()
	}
}

func TestLongestCapacityChain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		starts []string
		blocks map[string][]string
		want   []string
	}{
		{"empty", nil, nil, nil},
		{"isolated", []string{"b", "a"}, nil, []string{"b"}},
		{"neighbor_tie", []string{"a"}, map[string][]string{"a": {"c", "b"}}, []string{"a", "c"}},
		{"root_tie", []string{"c", "a"}, map[string][]string{"a": {"b"}, "c": {"d"}}, []string{"c", "d"}},
		{"shared_suffix", []string{"a"}, map[string][]string{"a": {"c", "b"}, "b": {"d"}, "c": {"d"}, "d": {"e"}}, []string{"a", "c", "d", "e"}},
		{"longer_later", []string{"a"}, map[string][]string{"a": {"b", "c"}, "c": {"d"}}, []string{"a", "c", "d"}},
		{"reachable_cycle", []string{"a"}, map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"b", "d"}}, []string{"a", "b", "c", "d"}},
		{"self_cycle", []string{"a"}, map[string][]string{"a": {"a", "b"}}, []string{"a", "b"}},
		{"unreachable_cycle", []string{"a"}, map[string][]string{"a": {"b"}, "c": {"d"}, "d": {"c"}}, []string{"a", "b"}},
		{"empty_id", []string{"a"}, map[string][]string{"a": {""}, "": {"b"}}, []string{"a", "", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := longestCapacityChain(tc.starts, tc.blocks); !slices.Equal(got, tc.want) {
				t.Fatalf("path=%v, want %v", got, tc.want)
			}
		})
	}

	// Keep the original exhaustive traversal as a small-graph oracle. The new
	// recurrence must preserve its exact choice, including input-order ties.
	rng := rand.New(rand.NewSource(20260909))
	for trial := 0; trial < 200; trial++ {
		blocks := make(map[string][]string)
		for i := 0; i < 7; i++ {
			for _, j := range rng.Perm(7) {
				if (trial%2 == 0 && j <= i) || rng.Intn(4) != 0 {
					continue
				}
				blocks[strconv.Itoa(i)] = append(blocks[strconv.Itoa(i)], strconv.Itoa(j))
			}
		}
		var starts []string
		for _, i := range rng.Perm(7)[:3] {
			starts = append(starts, strconv.Itoa(i))
		}
		var want []string
		visited := make(map[string]bool)
		var walk func(string, []string)
		walk = func(id string, path []string) {
			if visited[id] {
				return
			}
			visited[id] = true
			path = append(path, id)
			if len(path) > len(want) {
				want = append([]string(nil), path...)
			}
			for _, next := range blocks[id] {
				walk(next, path)
			}
			visited[id] = false
		}
		for _, start := range starts {
			walk(start, nil)
		}
		if got := longestCapacityChain(starts, blocks); !slices.Equal(got, want) {
			t.Fatalf("trial=%d starts=%v blocks=%v path=%v, want %v", trial, starts, blocks, got, want)
		}
	}
}

func TestRobotForecast_CandidateScope(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1788912000")
	issues := []model.Issue{
		{ID: "focus", Status: model.StatusOpen, Labels: []string{"focus"}, Dependencies: []*model.Dependency{{DependsOnID: "outer", Type: model.DepBlocks}}},
		{ID: "outer", Status: model.StatusOpen, Labels: []string{"other"}},
		{ID: "done", Status: model.StatusClosed, Labels: []string{"focus"}},
	}
	for _, tc := range []struct {
		name       string
		candidates map[string]bool
		label      string
		target     string
		want       []string
		wantError  bool
	}{
		{"unscoped", nil, "", "all", []string{"focus", "outer"}, false},
		{"selected", map[string]bool{"focus": true}, "", "all", []string{"focus"}, false},
		{"empty", map[string]bool{}, "", "all", nil, false},
		{"false_entry", map[string]bool{"focus": true, "outer": false}, "", "all", []string{"focus"}, false},
		{"intersection", map[string]bool{"focus": true}, "other", "all", nil, false},
		{"selected_single", map[string]bool{"focus": true}, "focus", "focus", []string{"focus"}, false},
		{"outside_single", map[string]bool{"focus": true}, "", "outer", nil, true},
		{"label_excluded_single", nil, "focus", "outer", nil, true},
		{"empty_single", map[string]bool{}, "", "focus", nil, true},
		{"missing_single", nil, "", "missing", nil, true},
		{"selected_closed_single", map[string]bool{"done": true}, "focus", "done", []string{"done"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := newRobotRegistry()
			registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotForecastFlag: &tc.target, ForecastLabel: &tc.label})
			var out, stderr bytes.Buffer
			ctx := RobotContext{Issues: issues, CandidateIDs: tc.candidates, Encoder: json.NewEncoder(&out), Stderr: &stderr}
			result := dispatchRobotFlagResult(&registry, "robot-forecast", ctx)
			if !result.Handled {
				t.Fatal("forecast handler not dispatched")
			}
			if tc.wantError {
				if result.ExitCode == 0 || out.Len() != 0 || !strings.Contains(stderr.String(), tc.target) {
					t.Fatalf("excluded target should fail without forecast JSON: result=%+v stdout=%s stderr=%s", result, out.String(), stderr.String())
				}
				return
			}
			if result.ExitCode != 0 || result.Err != nil {
				t.Fatalf("forecast failed: %+v stderr=%s", result, stderr.String())
			}
			var got struct {
				Count     int                    `json:"forecast_count"`
				Forecasts []analysis.ETAEstimate `json:"forecasts"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, forecast := range got.Forecasts {
				ids = append(ids, forecast.IssueID)
			}
			if !slices.Equal(ids, tc.want) || got.Count != len(tc.want) {
				t.Fatalf("forecast IDs=%v count=%d, want %v; output=%s", ids, got.Count, tc.want, out.String())
			}
		})
	}
}

func TestRobotHistoryTimeoutFromMillisecondsChecked(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want time.Duration
		ok   bool
	}{
		{name: "negative is unset", ms: -1, want: 0, ok: false},
		{name: "zero remains unbounded sentinel", ms: 0, want: 0, ok: true},
		{name: "ordinary duration", ms: 1250, want: 1250 * time.Millisecond, ok: true},
		{
			name: "largest exact millisecond duration",
			ms:   maxRobotHistoryTimeoutMillis,
			want: time.Duration(maxRobotHistoryTimeoutMillis) * time.Millisecond,
			ok:   true,
		},
		{
			name: "one millisecond beyond duration range saturates",
			ms:   maxRobotHistoryTimeoutMillis + 1,
			want: time.Duration(math.MaxInt64),
			ok:   true,
		},
		{
			name: "largest parsed integer saturates",
			ms:   math.MaxInt64,
			want: time.Duration(math.MaxInt64),
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := robotHistoryTimeoutFromMilliseconds(tt.ms)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("robotHistoryTimeoutFromMilliseconds(%d) = (%s, %v), want (%s, %v)", tt.ms, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveRobotHistoryTimeoutSaturatesOverflow(t *testing.T) {
	t.Setenv("BV_ROBOT_HISTORY_TIMEOUT_MS", strconv.FormatInt(maxRobotHistoryTimeoutMillis+1, 10))
	unset := -1
	if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &unset}); got != time.Duration(math.MaxInt64) {
		t.Fatalf("overflowing environment timeout = %s, want saturation at %s", got, time.Duration(math.MaxInt64))
	}
	if strconv.IntSize == 64 {
		overflowingMillis := int64(maxRobotHistoryTimeoutMillis + 1)
		overflowingFlag := int(overflowingMillis)
		if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &overflowingFlag}); got != time.Duration(math.MaxInt64) {
			t.Fatalf("overflowing flag timeout = %s, want saturation at %s", got, time.Duration(math.MaxInt64))
		}
	}

	flagValue := 25
	if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &flagValue}); got != 25*time.Millisecond {
		t.Fatalf("explicit flag timeout = %s, want 25ms and precedence over environment", got)
	}
}

func TestRobotRegistryValidate_RejectsModifierAlone(t *testing.T) {
	var robotTriage bool
	robotByLabel := "backend"

	registry := newRobotRegistry()
	registry.Register(RobotCommand{
		Name:        "robot-triage",
		FlagName:    "robot-triage",
		FlagPtr:     &robotTriage,
		Description: "Unified triage output",
	})
	registry.Register(RobotCommand{
		Name:            "robot-by-label",
		FlagName:        "robot-by-label",
		FlagPtr:         &robotByLabel,
		RequiredCoFlags: []string{"robot-triage", "robot-insights", "robot-plan", "robot-priority"},
		IsModifier:      true,
		Description:     "Filter robot output by label",
	})
	registry.Register(RobotCommand{
		Name:        "robot-insights",
		FlagName:    "robot-insights",
		FlagPtr:     ptrTo(false),
		Description: "Insights output",
	})
	registry.Register(RobotCommand{
		Name:        "robot-plan",
		FlagName:    "robot-plan",
		FlagPtr:     ptrTo(false),
		Description: "Plan output",
	})
	registry.Register(RobotCommand{
		Name:        "robot-priority",
		FlagName:    "robot-priority",
		FlagPtr:     ptrTo(false),
		Description: "Priority output",
	})

	err := registry.Validate()
	if err == nil {
		t.Fatal("expected modifier-alone validation error")
	}
	if !strings.Contains(err.Error(), "--robot-by-label") {
		t.Fatalf("expected error to mention modifier flag, got %q", err)
	}
	if !strings.Contains(err.Error(), "--robot-triage") {
		t.Fatalf("expected error to mention required co-flag, got %q", err)
	}

	robotTriage = true
	if err := registry.Validate(); err != nil {
		t.Fatalf("expected modifier to validate once paired with primary flag: %v", err)
	}
}

func TestRobotRegistryAnyActive_MatchesOldLogic(t *testing.T) {
	var (
		robotHelp       bool
		robotInsights   bool
		robotTriage     bool
		robotSearch     bool
		robotFileBeads  string
		robotByLabel    string
		robotByAssignee string
		robotDocs       string
	)

	registry := newRobotRegistry()
	registry.Register(RobotCommand{Name: "robot-help", FlagName: "robot-help", FlagPtr: &robotHelp, Description: "Help"})
	registry.Register(RobotCommand{Name: "robot-insights", FlagName: "robot-insights", FlagPtr: &robotInsights, Description: "Insights"})
	registry.Register(RobotCommand{Name: "robot-triage", FlagName: "robot-triage", FlagPtr: &robotTriage, Description: "Triage"})
	registry.Register(RobotCommand{Name: "robot-search", FlagName: "robot-search", FlagPtr: &robotSearch, Description: "Search"})
	registry.Register(RobotCommand{Name: "robot-file-beads", FlagName: "robot-file-beads", FlagPtr: &robotFileBeads, Description: "File beads"})
	registry.Register(RobotCommand{
		Name:            "robot-by-label",
		FlagName:        "robot-by-label",
		FlagPtr:         &robotByLabel,
		RequiredCoFlags: []string{"robot-insights", "robot-triage"},
		IsModifier:      true,
		Description:     "Label filter",
	})
	registry.Register(RobotCommand{
		Name:            "robot-by-assignee",
		FlagName:        "robot-by-assignee",
		FlagPtr:         &robotByAssignee,
		RequiredCoFlags: []string{"robot-insights", "robot-triage"},
		IsModifier:      true,
		Description:     "Assignee filter",
	})
	registry.Register(RobotCommand{Name: "robot-docs", FlagName: "robot-docs", FlagPtr: &robotDocs, Description: "Docs"})

	oldLogic := func() bool {
		return robotHelp ||
			robotInsights ||
			robotTriage ||
			robotSearch ||
			robotFileBeads != "" ||
			robotByLabel != "" ||
			robotByAssignee != "" ||
			robotDocs != ""
	}

	tests := []struct {
		name  string
		setup func()
	}{
		{name: "none active", setup: func() {}},
		{name: "help active", setup: func() { robotHelp = true }},
		{name: "primary robot command active", setup: func() { robotTriage = true }},
		{name: "string command active", setup: func() { robotFileBeads = "pkg/ui/model.go" }},
		{name: "modifier alone still enables robot mode", setup: func() { robotByLabel = "backend" }},
		{name: "docs topic active", setup: func() { robotDocs = "commands" }},
		{name: "multiple mixed flags", setup: func() {
			robotSearch = true
			robotByAssignee = "alice"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			robotHelp = false
			robotInsights = false
			robotTriage = false
			robotSearch = false
			robotFileBeads = ""
			robotByLabel = ""
			robotByAssignee = ""
			robotDocs = ""

			tt.setup()

			if got, want := registry.AnyActive(), oldLogic(); got != want {
				t.Fatalf("AnyActive()=%v, want %v", got, want)
			}
		})
	}
}

func TestRobotRegistryDispatchFlag_RunsActiveHandler(t *testing.T) {
	var robotHelp bool
	var called int

	registry := newRobotRegistry()
	registry.Register(RobotCommand{
		Name:     "robot-help",
		FlagName: "robot-help",
		FlagPtr:  &robotHelp,
		Handler: func(ctx RobotContext) error {
			called++
			if got := ctx.StdoutOrDefault(); got != ctx.Stdout {
				t.Fatalf("expected dispatch to preserve stdout writer")
			}
			return nil
		},
	})

	stdout := &bytes.Buffer{}
	ctx := RobotContext{Stdout: stdout}

	handled, err := registry.DispatchFlag("robot-help", ctx)
	if err != nil {
		t.Fatalf("inactive flag should not error: %v", err)
	}
	if handled {
		t.Fatal("inactive flag should not dispatch")
	}

	robotHelp = true
	handled, err = registry.DispatchFlag("robot-help", ctx)
	if err != nil {
		t.Fatalf("dispatch returned error: %v", err)
	}
	if !handled {
		t.Fatal("active flag should dispatch")
	}
	if called != 1 {
		t.Fatalf("handler call count = %d, want 1", called)
	}
}

func TestRobotDiffHandlerPinsNestedTimestampWithoutMutatingInput(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(pinned.Unix(), 10))

	active := true
	registry := newRobotRegistry()
	registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotDiffFlag: &active})

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	originalTo := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	diff := &analysis.SnapshotDiff{FromTimestamp: from, ToTimestamp: originalTo}
	var output bytes.Buffer
	handled, err := registry.DispatchFlag("robot-diff", RobotContext{
		DataHash:             "current-hash",
		Diff:                 diff,
		DiffResolvedRevision: "abc123",
		DiffHistoricalIssues: nil,
		Encoder:              json.NewEncoder(&output),
	})
	if err != nil {
		t.Fatalf("dispatch robot-diff: %v", err)
	}
	if !handled {
		t.Fatal("robot-diff handler was not dispatched")
	}
	var decoded struct {
		GeneratedAt string `json:"generated_at"`
		Diff        struct {
			FromTimestamp time.Time `json:"from_timestamp"`
			ToTimestamp   time.Time `json:"to_timestamp"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode robot-diff output: %v\n%s", err, output.String())
	}
	if decoded.GeneratedAt != pinned.Format(time.RFC3339) || !decoded.Diff.ToTimestamp.Equal(pinned) {
		t.Fatalf("pinned output times = generated %q, nested %v; want %v", decoded.GeneratedAt, decoded.Diff.ToTimestamp, pinned)
	}
	if !decoded.Diff.FromTimestamp.Equal(from) {
		t.Fatalf("from timestamp = %v, want %v", decoded.Diff.FromTimestamp, from)
	}
	if !diff.ToTimestamp.Equal(originalTo) {
		t.Fatalf("handler mutated input diff timestamp to %v", diff.ToTimestamp)
	}
}

func TestDispatchRobotFlagResult_ReturnsComposableOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var robotHelp bool

		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return nil
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if result.Handled {
			t.Fatal("inactive flag should not dispatch")
		}
		if result.ExitCode != 0 {
			t.Fatalf("inactive flag exit code = %d, want 0", result.ExitCode)
		}

		robotHelp = true
		result = dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 0 {
			t.Fatalf("successful dispatch exit code = %d, want 0", result.ExitCode)
		}
		if result.Err != nil {
			t.Fatalf("successful dispatch should not return error: %v", result.Err)
		}
		if result.AlreadyReported {
			t.Fatal("successful dispatch should not be marked reported")
		}
	})

	t.Run("handler error", func(t *testing.T) {
		var robotHelp = true
		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return errors.New("boom")
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 1 {
			t.Fatalf("error dispatch exit code = %d, want 1", result.ExitCode)
		}
		if result.Err == nil || !strings.Contains(result.Err.Error(), "boom") {
			t.Fatalf("error dispatch returned err = %v, want boom", result.Err)
		}
		if result.AlreadyReported {
			t.Fatal("plain handler errors should not be marked reported")
		}
	})

	t.Run("reported exit", func(t *testing.T) {
		var robotHelp = true
		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return newReportedRobotHandlerExit(2)
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 2 {
			t.Fatalf("reported dispatch exit code = %d, want 2", result.ExitCode)
		}
		if result.Err != nil {
			t.Fatalf("reported exit should not retain wrapped error: %v", result.Err)
		}
		if !result.AlreadyReported {
			t.Fatal("reported exit should preserve AlreadyReported")
		}
	})
}

func TestWriteRobotHelp_ReturnsWriterError(t *testing.T) {
	err := writeRobotHelp(failingWriter{err: errors.New("write failed")})
	if err == nil {
		t.Fatal("expected writer error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected wrapped writer error, got %v", err)
	}
}

func TestWriteRobotHelp_ReturnsWriterErrorAfterIntro(t *testing.T) {
	writer := &failAfterNWritesWriter{
		failAfter: 1,
		err:       errors.New("write failed after intro"),
	}

	err := writeRobotHelp(writer)
	if err == nil {
		t.Fatal("expected writer error after intro")
	}
	// The first write after the intro is the generated commands heading.
	if !strings.Contains(err.Error(), "commands heading") {
		t.Fatalf("expected contextual error for later write, got %v", err)
	}
	if !strings.Contains(err.Error(), "write failed after intro") {
		t.Fatalf("expected underlying writer error, got %v", err)
	}
}

func TestFilterOrphanReportByMinScoreRebuildsDerivedFields(t *testing.T) {
	report := &correlation.OrphanReport{
		Stats: correlation.OrphanReportStats{
			CandidateCount: 2,
			AvgSuspicion:   70,
		},
		Candidates: []correlation.OrphanCandidate{
			{
				ShortSHA:       "aaaaaaa",
				SuspicionScore: 90,
				ProbableBeads:  []correlation.ProbableBead{{BeadID: "bv-keep"}},
			},
			{
				ShortSHA:       "bbbbbbb",
				SuspicionScore: 20,
				ProbableBeads:  []correlation.ProbableBead{{BeadID: "bv-drop"}},
			},
		},
		ByBead: map[string][]string{
			"bv-keep": []string{"aaaaaaa"},
			"bv-drop": []string{"bbbbbbb"},
		},
	}

	filterOrphanReportByMinScore(report, 50)

	if len(report.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(report.Candidates))
	}
	if strings.Compare(report.Candidates[0].ShortSHA, "aaaaaaa") != 0 {
		t.Fatalf("candidate short SHA = %q, want aaaaaaa", report.Candidates[0].ShortSHA)
	}
	if report.Stats.CandidateCount != 1 {
		t.Fatalf("stats candidate count = %d, want 1", report.Stats.CandidateCount)
	}
	if report.Stats.AvgSuspicion != 90 {
		t.Fatalf("avg suspicion = %v, want 90", report.Stats.AvgSuspicion)
	}
	if got := report.ByBead["bv-keep"]; len(got) != 1 || strings.Compare(got[0], "aaaaaaa") != 0 {
		t.Fatalf("kept by_bead entry = %#v, want aaaaaaa", got)
	}
	if dropped := report.ByBead["bv-drop"]; dropped != nil {
		t.Fatalf("dropped candidate still present in by_bead: %#v", dropped)
	}

	filterOrphanReportByMinScore(report, 101)
	if len(report.Candidates) != 0 {
		t.Fatalf("candidate count after filtering all = %d, want 0", len(report.Candidates))
	}
	if report.Stats.CandidateCount != 0 {
		t.Fatalf("stats candidate count after filtering all = %d, want 0", report.Stats.CandidateCount)
	}
	if report.Stats.AvgSuspicion != 0 {
		t.Fatalf("avg suspicion after filtering all = %v, want 0", report.Stats.AvgSuspicion)
	}
	if len(report.ByBead) != 0 {
		t.Fatalf("by_bead after filtering all = %#v, want empty", report.ByBead)
	}
}

func TestParseCorrelationArgTrimsAndRejectsEmptyParts(t *testing.T) {
	commitSHA, beadID, err := parseCorrelationArg("  abc123 : bv-1  ")
	if err != nil {
		t.Fatalf("parseCorrelationArg returned error: %v", err)
	}
	if commitSHA != "abc123" {
		t.Fatalf("commit SHA = %q, want abc123", commitSHA)
	}
	if beadID != "bv-1" {
		t.Fatalf("bead ID = %q, want bv-1", beadID)
	}

	tests := []string{
		"",
		"abc123",
		":bv-1",
		"abc123:",
		"   :   ",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, _, err := parseCorrelationArg(input); err == nil {
				t.Fatalf("parseCorrelationArg(%q) succeeded, want error", input)
			}
		})
	}
}

func TestResolveCorrelatedCommitRejectsAmbiguousPrefix(t *testing.T) {
	commits := []correlation.CorrelatedCommit{
		{SHA: "abc123def456", ShortSHA: "abc123d", Confidence: 0.8},
		{SHA: "abc123fff000", ShortSHA: "abc123f", Confidence: 0.7},
	}

	commit, err := resolveCorrelatedCommit(commits, "abc123d")
	if err != nil {
		t.Fatalf("resolveCorrelatedCommit returned error: %v", err)
	}
	if commit == nil || commit.SHA != "abc123def456" {
		t.Fatalf("resolved commit = %#v, want abc123def456", commit)
	}

	commit, err = resolveCorrelatedCommit(commits, "ABC123F")
	if err != nil {
		t.Fatalf("resolveCorrelatedCommit uppercase short SHA returned error: %v", err)
	}
	if commit == nil || commit.SHA != "abc123fff000" {
		t.Fatalf("uppercase resolved commit = %#v, want abc123fff000", commit)
	}

	commit, err = resolveCorrelatedCommit(commits, "abc123")
	if err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if commit != nil {
		t.Fatalf("commit = %#v, want nil on ambiguity", commit)
	}
	if !strings.Contains(err.Error(), "ambiguous commit SHA prefix") {
		t.Fatalf("error = %q, want ambiguity message", err.Error())
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failAfterNWritesWriter struct {
	failAfter int
	writes    int
	err       error
}

func (w *failAfterNWritesWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAfter {
		return 0, w.err
	}
	w.writes++
	return len(p), nil
}
