package drift

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func driftReuseFixture(now time.Time) []model.Issue {
	issues := []model.Issue{{ID: "gate", Title: "Release integration", Status: model.StatusOpen, Priority: 1,
		UpdatedAt: now.Add(-40 * 24 * time.Hour), Labels: []string{"z", "a"}}}
	for _, id := range []string{"third", "first", "second"} {
		issues = append(issues, model.Issue{ID: id, Title: id, Status: model.StatusOpen, Priority: 0,
			UpdatedAt: now.Add(-40 * 24 * time.Hour), Dependencies: []*model.Dependency{{DependsOnID: "gate", Type: model.DepBlocks}}})
	}
	return issues
}

func newDriftReuseCalculator(issues []model.Issue, now time.Time) *Calculator {
	bl := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: len(issues), OpenCount: len(issues)}}
	c := NewCalculator(bl, bl, nil)
	c.SetNow(now)
	c.SetIssues(issues)
	return c
}

func TestCalculatorReuseAnalyzerPreservesExactAlertsAndOrder(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	issues := driftReuseFixture(now)
	// The graph snapshot and current presentation have different outer orders.
	source := []model.Issue{issues[2], issues[1], issues[0], issues[3]}
	a := analysis.NewAnalyzer(source)
	a.SetNow(now)
	c := newDriftReuseCalculator(issues, now)
	if !c.ReuseAnalyzer(a) || c.analyzerCache != a {
		t.Fatal("matching graph was not reused")
	}
	want := newDriftReuseCalculator(driftReuseFixture(now), now).Calculate()
	got := c.Calculate()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reused alerts differ from ordinary full rebuild:\ngot %#v\nwant %#v", got, want)
	}
	var staleIDs []string
	gate := false
	for _, alert := range got.Alerts {
		if alert.Type == AlertStaleIssue {
			staleIDs = append(staleIDs, alert.IssueID)
		}
		if alert.Type == AlertBlockingCascade && alert.IssueID == "gate" && alert.UnblocksCount == 3 {
			gate = true
		}
	}
	if !gate || !reflect.DeepEqual(staleIDs, []string{"gate", "third", "first", "second"}) {
		t.Fatalf("missing real cascade or presentation order: gate=%v stale=%v", gate, staleIDs)
	}
	issues[0].Labels[0] = "changed"
	issues[1].Dependencies[0].DependsOnID = "missing"
	issues[2].Title = "mutated caller row"
	if got := c.Calculate(); !reflect.DeepEqual(got, want) {
		t.Fatal("accepted source still aliases caller-owned issue rows")
	}
}

func TestCalculatorLookupAlertsUseLatestDuplicateMetadata(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []model.Issue{{ID: "gate", Status: model.StatusOpen, Priority: 4, UpdatedAt: now, Labels: []string{"日本語", "a"}}}
	for i := 0; i < 8; i++ {
		rows = append(rows, model.Issue{ID: fmt.Sprintf("leaf-%d", i), Status: model.StatusOpen, Priority: 0, UpdatedAt: now,
			Dependencies: []*model.Dependency{{DependsOnID: "gate", Type: model.DepBlocks}}})
	}
	latest := rows[1].Clone()
	rows[1].Priority = 4
	rows = append(rows, latest)
	result := newDriftReuseCalculator(rows, now).Calculate()
	found := map[AlertType]bool{}
	for _, alert := range result.Alerts {
		if alert.IssueID != "gate" {
			continue
		}
		switch alert.Type {
		case AlertBlockingCascade:
			if alert.UnblocksCount != 8 || alert.DownstreamPrioritySum != 0 {
				t.Fatalf("duplicate metadata changed unblock priorities: %+v", alert)
			}
		case AlertHighImpactUnblock:
			if alert.UnblocksCount != 8 || len(alert.Details) != 8 {
				t.Fatalf("duplicate metadata lost urgent successors: %+v", alert)
			}
		case AlertPriorityMismatch:
			if !reflect.DeepEqual(alert.Labels, rows[0].Labels) || alert.BaselineVal != 4 || alert.CurrentVal >= 4 {
				t.Fatalf("priority alert lost source metadata: %+v", alert)
			}
		default:
			continue
		}
		found[alert.Type] = true
	}
	if len(found) != 3 {
		t.Fatalf("expected all three lookup consumers to emit, got %v", found)
	}
}

func TestCalculatorReuseAnalyzerRejectsDifferentSource(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func([]model.Issue) []model.Issue
	}{
		{"same-id-title", func(rows []model.Issue) []model.Issue { rows[0].Title += " changed"; return rows }},
		{"nested-dependency", func(rows []model.Issue) []model.Issue { rows[1].Dependencies[0].DependsOnID = "missing"; return rows }},
		{"ordered-labels", func(rows []model.Issue) []model.Issue {
			rows[0].Labels[0], rows[0].Labels[1] = rows[0].Labels[1], rows[0].Labels[0]
			return rows
		}},
		{"missing-row", func(rows []model.Issue) []model.Issue { return rows[:len(rows)-1] }},
		{"new-id", func(rows []model.Issue) []model.Issue { rows[1].ID = "replacement"; return rows }},
		{"duplicate", func(rows []model.Issue) []model.Issue { rows[1] = rows[0]; return rows }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := driftReuseFixture(now)
			a := analysis.NewAnalyzer(rows)
			a.SetNow(now)
			rows = tc.change(rows)
			c := newDriftReuseCalculator(rows, now)
			if c.ReuseAnalyzer(a) {
				t.Fatal("different source accepted")
			}
			if got, want := c.Calculate(), newDriftReuseCalculator(rows, now).Calculate(); !reflect.DeepEqual(got, want) {
				t.Fatal("rejected source did not retain ordinary calculation")
			}
		})
	}
	rows := driftReuseFixture(now)
	rows[1] = rows[0]
	a := analysis.NewAnalyzer(rows)
	a.SetNow(now)
	if newDriftReuseCalculator(rows, now).ReuseAnalyzer(a) {
		t.Fatal("matching duplicate IDs must still reject reuse")
	}
	if newDriftReuseCalculator(nil, now).ReuseAnalyzer(nil) {
		t.Fatal("nil analyzer accepted")
	}
	a = analysis.NewAnalyzer(nil)
	a.SetNow(now)
	if !newDriftReuseCalculator(nil, now).ReuseAnalyzer(a) {
		t.Fatal("empty matching source rejected")
	}
	if newDriftReuseCalculator(nil, now.Add(time.Hour)).ReuseAnalyzer(a) {
		t.Fatal("mismatched reference clock accepted")
	}
}

func TestCalculatorReuseAnalyzerDetachesChangedBorrower(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, mutation := range []string{"clock", "weights", "candidates"} {
		t.Run(mutation, func(t *testing.T) {
			rows := driftReuseFixture(now)
			rows[0].Dependencies = []*model.Dependency{{DependsOnID: "hidden", Type: model.DepBlocks}}
			full := append(append([]model.Issue(nil), rows...), model.Issue{ID: "hidden", Status: model.StatusTombstone})
			a := analysis.NewAnalyzer(rows)
			a.SetNow(now)
			a.SetReadinessScope(model.NewReadinessIndex(full), map[string]bool{"gate": true})
			weights := analysis.DefaultWeights()
			weights.PageRank += .05
			weights.PriorityBoost -= .05
			a.SetWeights(weights)
			c := newDriftReuseCalculator(rows, now)
			if !c.ReuseAnalyzer(a) {
				t.Fatal("matching scoped analyzer rejected")
			}
			captured := a.CaptureScoring()
			want := c.Calculate()
			switch mutation {
			case "clock":
				a.SetNow(now.Add(365 * 24 * time.Hour))
			case "weights":
				a.SetWeights(analysis.DefaultWeights())
			case "candidates":
				a.SetReadinessScope(a.Readiness(), map[string]bool{})
			}
			changed := a.CaptureScoring()
			selected := a.IsCandidate("gate")
			got := c.Calculate()
			if c.analyzerCache == a || c.analyzerSource != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("changed borrowed analyzer not detached with exact captured results: got %#v want %#v", got, want)
			}
			if c.analyzerCache.CaptureScoring() != captured || a.CaptureScoring() != changed || a.IsCandidate("gate") != selected {
				t.Fatal("detaching changed exact scoring or mutated borrowed analyzer")
			}
			if c.analyzerCache.CountActionableIssues() != 1 || c.analyzerCache.IsCandidate("third") {
				t.Fatal("detached analyzer lost hidden authority or candidate scope")
			}
		})
	}
}

func TestCalculatorSetNowInvalidatesReadinessAndSetIssuesDropsScope(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	boundary := now.Add(time.Hour)
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			rows := driftReuseFixture(now)
			rows[0].DeferUntil = &boundary
			if reuse {
				rows[0].Dependencies = []*model.Dependency{{DependsOnID: "hidden", Type: model.DepBlocks}}
			}
			c := newDriftReuseCalculator(rows, now)
			a := analysis.NewAnalyzer(rows)
			a.SetNow(now)
			if reuse {
				full := append(append([]model.Issue(nil), rows...), model.Issue{ID: "hidden", Status: model.StatusTombstone})
				a.SetReadinessScope(model.NewReadinessIndex(full), map[string]bool{"gate": true, "third": true, "first": true, "second": true})
				if !c.ReuseAnalyzer(a) {
					t.Fatal("matching analyzer rejected")
				}
			}
			before := c.Calculate()
			for _, alert := range before.Alerts {
				if alert.Type == AlertBlockingCascade && alert.IssueID == "gate" {
					t.Fatal("future-deferred gate advertised")
				}
			}
			c.SetNow(boundary)
			found := false
			for _, alert := range c.Calculate().Alerts {
				if alert.Type == AlertBlockingCascade && alert.IssueID == "gate" && alert.UnblocksCount == 3 {
					found = true
				}
				if !alert.DetectedAt.Equal(boundary) {
					t.Fatal("alert timestamp did not advance")
				}
			}
			if !found || !a.Now().Equal(now) {
				t.Fatal("boundary lost ready gate or mutated borrowed clock")
			}
			c.SetIssues([]model.Issue{{ID: "replacement", Status: model.StatusOpen}})
			c.Calculate()
			if c.analyzer().CountActionableIssues() != 1 || c.analyzerReadiness != nil || c.analyzerSource != nil {
				t.Fatal("SetIssues retained stale source scope")
			}
		})
	}
}

func BenchmarkCalculatorReuseAnalyzer(b *testing.B) {
	rows, err := testutil.PerformanceIssues("unicode", 10000, 20260904)
	if err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	a := analysis.NewAnalyzer(rows)
	a.SetNow(now)
	a.Readiness()
	want := newDriftReuseCalculator(rows, now).Calculate()
	c := newDriftReuseCalculator(rows, now)
	if !c.ReuseAnalyzer(a) || !reflect.DeepEqual(c.Calculate(), want) {
		b.Fatal("full result parity failed before timing")
	}
	for _, name := range []string{"source-validation", "fresh", "reuse"} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if name == "source-validation" {
					if !a.MatchesIssues(rows) {
						b.Fatal("source mismatch")
					}
					continue
				}
				c := newDriftReuseCalculator(rows, now)
				if name == "reuse" && !c.ReuseAnalyzer(a) {
					b.Fatal("source mismatch")
				}
				c.Calculate()
			}
		})
	}
}

func TestCalculatorNoDrift(t *testing.T) {
	bl := &baseline.Baseline{
		Version:   1,
		CreatedAt: time.Now(),
		Stats: baseline.GraphStats{
			NodeCount:       100,
			EdgeCount:       200,
			Density:         0.02,
			OpenCount:       50,
			ClosedCount:     40,
			BlockedCount:    10,
			CycleCount:      0,
			ActionableCount: 40,
		},
	}

	// Current matches baseline
	current := &baseline.Baseline{
		Version:   1,
		CreatedAt: time.Now(),
		Stats:     bl.Stats,
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	if result.HasDrift {
		t.Errorf("expected no drift, got %d alerts", len(result.Alerts))
	}
}

func TestCalculatorNewCycle(t *testing.T) {
	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10, EdgeCount: 15},
		Cycles: [][]string{},
	}

	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: [][]string{{"A", "B", "C", "A"}},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	if !result.HasDrift {
		t.Error("expected drift to be detected")
	}

	if result.CriticalCount != 1 {
		t.Errorf("expected 1 critical alert, got %d", result.CriticalCount)
	}

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertNewCycle {
			found = true
			if alert.Severity != SeverityCritical {
				t.Errorf("new cycle should be critical, got %s", alert.Severity)
			}
		}
	}
	if !found {
		t.Error("expected new_cycle alert")
	}
}

func TestCalculatorSetNowPinsAllAlertTimesAndStaleness(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10, EdgeCount: 10},
		Cycles: [][]string{},
	}
	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: [][]string{{"A", "B", "A"}},
	}
	calc := NewCalculator(bl, current, nil)
	calc.SetNow(pinned)
	calc.SetIssues([]model.Issue{{
		ID:        "STALE",
		Status:    model.StatusOpen,
		UpdatedAt: pinned.Add(-40 * 24 * time.Hour),
	}})

	result := calc.Calculate()
	if len(result.Alerts) < 2 {
		t.Fatalf("expected cycle and staleness alerts, got %#v", result.Alerts)
	}
	for _, alert := range result.Alerts {
		if !alert.DetectedAt.Equal(pinned) {
			t.Fatalf("alert %s detected_at = %v, want %v", alert.Type, alert.DetectedAt, pinned)
		}
	}
}

func TestCalculatorSetNowAcceptsZeroEpoch(t *testing.T) {
	bl := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 1}}
	current := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 2}}
	calc := NewCalculator(bl, current, nil)
	calc.SetNow(time.Time{})
	result := calc.Calculate()
	if len(result.Alerts) == 0 {
		t.Fatal("expected node-count drift alert")
	}
	for _, alert := range result.Alerts {
		if !alert.DetectedAt.IsZero() {
			t.Fatalf("alert detected_at=%v, want zero epoch", alert.DetectedAt)
		}
	}
}

func TestCalculatorDensityGrowth(t *testing.T) {
	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			EdgeCount: 200,
			Density:   0.02,
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			EdgeCount: 400,
			Density:   0.04, // 100% increase - definitely above 50% warning threshold
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			found = true
			if alert.Severity != SeverityWarning {
				t.Errorf("100%% density increase should be warning, got %s", alert.Severity)
			}
		}
	}
	if !found {
		t.Error("expected density_growth alert")
	}
}

func TestCalculatorBlockedIncrease(t *testing.T) {
	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:    100,
			BlockedCount: 5,
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:    100,
			BlockedCount: 15, // +10
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertBlockedIncrease {
			found = true
			if alert.Severity != SeverityWarning {
				t.Errorf("blocked increase should be warning, got %s", alert.Severity)
			}
		}
	}
	if !found {
		t.Error("expected blocked_increase alert")
	}
}

func TestCalculatorPageRankChange(t *testing.T) {
	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 100},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{
				{ID: "TASK-1", Value: 0.2},
				{ID: "TASK-2", Value: 0.15},
			},
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 100},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{
				{ID: "TASK-1", Value: 0.35}, // 75% increase
				{ID: "TASK-3", Value: 0.18}, // New entry
			},
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertPageRankChange {
			found = true
		}
	}
	if !found {
		t.Error("expected pagerank_change alert")
	}
}

func TestCalculatorStalenessWarningAndCritical(t *testing.T) {
	now := time.Now().UTC()
	issues := []model.Issue{
		{ID: "OLD-WARN", Status: model.StatusOpen, UpdatedAt: now.Add(-16 * 24 * time.Hour)},
		{ID: "OLD-CRIT", Status: model.StatusOpen, UpdatedAt: now.Add(-35 * 24 * time.Hour)},
		{ID: "INPROG", Status: model.StatusInProgress, UpdatedAt: now.Add(-8 * 24 * time.Hour)},
	}

	bl := &baseline.Baseline{Stats: baseline.GraphStats{}}
	current := &baseline.Baseline{Stats: baseline.GraphStats{}}
	calc := NewCalculator(bl, current, nil)
	calc.SetIssues(issues)

	result := calc.Calculate()

	var warnCount, critCount int
	for _, a := range result.Alerts {
		if a.Type != AlertStaleIssue {
			continue
		}
		if a.Severity == SeverityWarning {
			warnCount++
		}
		if a.Severity == SeverityCritical {
			critCount++
		}
	}

	if warnCount != 2 { // OLD-WARN + INPROG (in_progress threshold tightened)
		t.Fatalf("expected 2 warning staleness alerts, got %d", warnCount)
	}
	if critCount != 1 {
		t.Fatalf("expected 1 critical staleness alert, got %d", critCount)
	}
}

func TestCalculatorStalenessPreservesBoundariesOrderAndPrefix(t *testing.T) {
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)
	cfg := DefaultConfig()
	cfg.LabelOverrides = map[string]*LabelConfig{
		"slow":  {StaleWarningDays: 20, StaleCriticalDays: 60},
		"tight": {StaleWarningDays: 4, StaleCriticalDays: 8, InProgressStaleMultiplier: .25},
	}
	var issues []model.Issue
	prefix := Alert{Type: AlertNewCycle, Severity: SeverityCritical, IssueID: "existing", Details: []string{"keep", "order"}}
	want := []Alert{prefix}
	for _, tc := range []struct {
		id       string
		status   model.Status
		updated  time.Time
		created  time.Time
		labels   []string
		severity Severity
		days     int
	}{
		{id: "under-warning", updated: now.Add(-14*24*time.Hour + time.Second)},
		{id: "at-warning", updated: now.Add(-14 * 24 * time.Hour), severity: SeverityWarning, days: 14},
		{id: "at-critical", updated: now.Add(-30 * 24 * time.Hour), severity: SeverityCritical, days: 30},
		{id: "progress-warning", status: model.StatusInProgress, updated: now.Add(-7 * 24 * time.Hour), severity: SeverityWarning, days: 7},
		{id: "progress-critical", status: model.StatusInProgress, updated: now.Add(-15 * 24 * time.Hour), severity: SeverityCritical, days: 15},
		{id: "created-fallback", created: old, severity: SeverityCritical, days: 40},
		{id: "looser-label", updated: old, labels: []string{"slow"}, severity: SeverityWarning, days: 40},
		{id: "tightest-label", status: model.StatusInProgress, updated: now.Add(-24 * time.Hour), labels: []string{"slow", "tight"}, severity: SeverityWarning, days: 1},
		{id: "unknown-label", updated: old, labels: []string{"日本語"}, severity: SeverityCritical, days: 40},
		{id: "empty-labels", updated: old, labels: []string{}, severity: SeverityCritical, days: 40},
		{id: "future", updated: now.Add(time.Hour)},
		{id: "closed", status: model.StatusClosed, updated: old},
		{id: "tombstone", status: model.StatusTombstone, updated: old},
		{id: "unknown-time"},
	} {
		status := tc.status
		if status == "" {
			status = model.StatusOpen
		}
		issues = append(issues, model.Issue{ID: tc.id, Status: status, UpdatedAt: tc.updated, CreatedAt: tc.created, Labels: tc.labels})
		if tc.severity == "" {
			continue
		}
		lastActive := tc.updated
		if lastActive.IsZero() {
			lastActive = tc.created
		}
		want = append(want, Alert{
			Type: AlertStaleIssue, Severity: tc.severity, Labels: tc.labels,
			SuggestedAction: "Update, close, or re-triage the issue; stale work hides real priorities",
			Message:         fmt.Sprintf("Issue %s inactive for %d days", tc.id, tc.days),
			IssueID:         tc.id, DetectedAt: now,
			Details: []string{fmt.Sprintf("status=%s", status), "last_update=" + lastActive.Format(time.RFC3339)},
		})
	}
	calc := newDriftReuseCalculator(issues, now)
	calc.config = cfg
	result := Result{Alerts: []Alert{prefix}}
	calc.checkStaleness(&result)
	if !reflect.DeepEqual(result.Alerts, want) {
		t.Fatalf("staleness boundaries, metadata or prefix changed:\ngot %#v\nwant %#v", result.Alerts, want)
	}
	for _, tc := range []struct {
		name     string
		rows     []model.Issue
		disabled bool
	}{
		{name: "nil-issues"},
		{name: "empty-issues", rows: []model.Issue{}},
		{name: "no-matches", rows: []model.Issue{{ID: "fresh", Status: model.StatusOpen, UpdatedAt: now}}},
		{name: "disabled", rows: issues, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newDriftReuseCalculator(tc.rows, now)
			if tc.disabled {
				c.config.DisabledAlerts = []string{string(AlertStaleIssue)}
			}
			for _, initial := range [][]Alert{nil, {}, {prefix}} {
				result := Result{Alerts: initial}
				c.checkStaleness(&result)
				if !reflect.DeepEqual(result.Alerts, initial) {
					t.Fatalf("no-op changed existing/nil/empty alerts: got %#v want %#v", result.Alerts, initial)
				}
			}
		})
	}
}

func BenchmarkCalculatorStaleness(b *testing.B) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		staleStep int
		overrides bool
	}{
		{name: "all-stale", staleStep: 1},
		{name: "sparse-stale", staleStep: 100},
		{name: "all-fresh"},
		{name: "label-overrides", staleStep: 1, overrides: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			rows := make([]model.Issue, 10000)
			wantCount := 0
			for i := range rows {
				rows[i] = model.Issue{ID: fmt.Sprintf("stale-%05d", i), Status: model.StatusOpen, UpdatedAt: now, Labels: []string{"日本語"}}
				if tc.staleStep != 0 && i%tc.staleStep == 0 {
					rows[i].UpdatedAt = now.Add(-40 * 24 * time.Hour)
					wantCount++
				}
			}
			c := newDriftReuseCalculator(rows, now)
			if tc.overrides {
				c.config.LabelOverrides = map[string]*LabelConfig{"日本語": {StaleWarningDays: 20, StaleCriticalDays: 60}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := Result{}
				c.checkStaleness(&result)
				if len(result.Alerts) != wantCount {
					b.Fatalf("stale alert count=%d, want %d", len(result.Alerts), wantCount)
				}
			}
		})
	}
}

func TestCalculatorBlockingCascade(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Blocker A", Status: model.StatusOpen},
		{ID: "B", Title: "Blocked by A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Title: "Also blocked by A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Title: "Independent", Status: model.StatusOpen},
	}
	bl := &baseline.Baseline{Stats: baseline.GraphStats{}}
	current := &baseline.Baseline{Stats: baseline.GraphStats{}}
	cfg := DefaultConfig()
	cfg.BlockingCascadeInfo = 2
	cfg.BlockingCascadeWarning = 3

	calc := NewCalculator(bl, current, cfg)
	calc.SetIssues(issues)

	result := calc.Calculate()

	var cascade Alert
	found := false
	for _, a := range result.Alerts {
		if a.Type == AlertBlockingCascade && a.IssueID == "A" {
			found = true
			cascade = a
		}
	}
	if !found {
		t.Fatalf("expected blocking cascade alert for A")
	}
	if cascade.Severity != SeverityInfo {
		t.Fatalf("expected info severity, got %s", cascade.Severity)
	}
	if len(cascade.Details) != 2 {
		t.Fatalf("expected 2 downstream ids, got %d", len(cascade.Details))
	}
	// Verify new fields (bv-165)
	if cascade.UnblocksCount != 2 {
		t.Fatalf("expected UnblocksCount=2, got %d", cascade.UnblocksCount)
	}
}

// TestCalculatorBlockingCascadeWithPriorities verifies the downstream priority sum calculation (bv-165)
func TestCalculatorBlockingCascadeWithPriorities(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Blocker A", Status: model.StatusOpen, Priority: 2},
		{ID: "B", Title: "Blocked by A (P1)", Status: model.StatusOpen, Priority: 1, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Title: "Blocked by A (P3)", Status: model.StatusOpen, Priority: 3, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D", Title: "Blocked by A (P0 critical)", Status: model.StatusOpen, Priority: 0, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	bl := &baseline.Baseline{Stats: baseline.GraphStats{}}
	current := &baseline.Baseline{Stats: baseline.GraphStats{}}
	cfg := DefaultConfig()
	cfg.BlockingCascadeInfo = 2
	cfg.BlockingCascadeWarning = 5

	calc := NewCalculator(bl, current, cfg)
	calc.SetIssues(issues)

	result := calc.Calculate()

	var cascade Alert
	found := false
	for _, a := range result.Alerts {
		if a.Type == AlertBlockingCascade && a.IssueID == "A" {
			found = true
			cascade = a
		}
	}
	if !found {
		t.Fatalf("expected blocking cascade alert for A")
	}

	// Verify UnblocksCount
	if cascade.UnblocksCount != 3 {
		t.Fatalf("expected UnblocksCount=3, got %d", cascade.UnblocksCount)
	}

	// Verify DownstreamPrioritySum: P1 + P3 + P0 = 1 + 3 + 0 = 4
	expectedPrioritySum := 4
	if cascade.DownstreamPrioritySum != expectedPrioritySum {
		t.Fatalf("expected DownstreamPrioritySum=%d, got %d", expectedPrioritySum, cascade.DownstreamPrioritySum)
	}
}

func TestResultSummary(t *testing.T) {
	result := &Result{
		HasDrift: true,
		Alerts: []Alert{
			{Type: AlertNewCycle, Severity: SeverityCritical, Message: "New cycle"},
			{Type: AlertDensityGrowth, Severity: SeverityWarning, Message: "Density up"},
		},
		CriticalCount: 1,
		WarningCount:  1,
	}

	summary := result.Summary()

	if !strings.Contains(summary, "CRITICAL") {
		t.Error("summary should mention critical")
	}
	if !strings.Contains(summary, "WARNING") {
		t.Error("summary should mention warning")
	}
}

func TestResultExitCode(t *testing.T) {
	tests := []struct {
		name     string
		result   *Result
		expected int
	}{
		{"no drift", &Result{}, 0},
		{"info only", &Result{HasDrift: true, InfoCount: 1}, 0},
		{"warning", &Result{HasDrift: true, WarningCount: 1}, 2},
		{"critical", &Result{HasDrift: true, CriticalCount: 1}, 1},
		{"critical and warning", &Result{HasDrift: true, CriticalCount: 1, WarningCount: 1}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.ExitCode(); got != tt.expected {
				t.Errorf("ExitCode() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestResultHasCritical(t *testing.T) {
	tests := []struct {
		name     string
		result   *Result
		expected bool
	}{
		{"no alerts", &Result{}, false},
		{"info only", &Result{InfoCount: 5}, false},
		{"warning only", &Result{WarningCount: 3}, false},
		{"critical", &Result{CriticalCount: 1}, true},
		{"critical with others", &Result{CriticalCount: 2, WarningCount: 1, InfoCount: 3}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.HasCritical(); got != tt.expected {
				t.Errorf("HasCritical() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestResultHasWarnings(t *testing.T) {
	tests := []struct {
		name     string
		result   *Result
		expected bool
	}{
		{"no alerts", &Result{}, false},
		{"info only", &Result{InfoCount: 5}, false},
		{"warning only", &Result{WarningCount: 1}, true},
		{"critical only", &Result{CriticalCount: 1}, true},
		{"warning and info", &Result{WarningCount: 2, InfoCount: 3}, true},
		{"critical and warning", &Result{CriticalCount: 1, WarningCount: 2}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.HasWarnings(); got != tt.expected {
				t.Errorf("HasWarnings() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExampleConfig(t *testing.T) {
	example := ExampleConfig()

	// Should not be empty
	if example == "" {
		t.Error("ExampleConfig() returned empty string")
	}

	// Should be valid YAML that can be parsed
	var config Config
	if err := yaml.Unmarshal([]byte(example), &config); err != nil {
		t.Errorf("ExampleConfig() returned invalid YAML: %v", err)
	}

	// Should contain expected keys
	expectedKeys := []string{
		"density_warning_pct",
		"density_info_pct",
		"blocked_increase_threshold",
		"pagerank_change_warning_pct",
	}
	for _, key := range expectedKeys {
		if !strings.Contains(example, key) {
			t.Errorf("ExampleConfig() should contain %q", key)
		}
	}

	// Parsed config should have reasonable values
	if config.DensityWarningPct <= 0 {
		t.Error("ExampleConfig() density_warning_pct should be positive")
	}
	if config.BlockedIncreaseThreshold < 0 {
		t.Error("ExampleConfig() blocked_increase_threshold should be non-negative")
	}
}

func TestConfigLoadDefault(t *testing.T) {
	tmpDir := t.TempDir()

	config, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Should return defaults
	if config.DensityWarningPct != 50 {
		t.Errorf("expected default density_warning_pct=50, got %f", config.DensityWarningPct)
	}
}

func TestConfigLoadCustom(t *testing.T) {
	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	configContent := `
density_warning_pct: 75
blocked_increase_threshold: 10
`
	if err := os.WriteFile(filepath.Join(bvDir, "drift.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if config.DensityWarningPct != 75 {
		t.Errorf("expected density_warning_pct=75, got %f", config.DensityWarningPct)
	}
	if config.BlockedIncreaseThreshold != 10 {
		t.Errorf("expected blocked_increase_threshold=10, got %d", config.BlockedIncreaseThreshold)
	}
}

func TestConfigLoadInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Invalid config: negative density warning
	configContent := `density_warning_pct: -50`
	if err := os.WriteFile(filepath.Join(bvDir, "drift.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Error("expected error for invalid config, got nil")
	}
}

// TestConfigLoadInvalidYAML tests loading a file with invalid YAML syntax
func TestConfigLoadInvalidYAML(t *testing.T) {
	t.Log("Testing LoadConfig with invalid YAML syntax")

	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write invalid YAML (bad indentation/syntax)
	invalidYAML := `density_warning_pct: 50
  bad_indentation: true
    this_is_invalid`
	configPath := filepath.Join(bvDir, "drift.yaml")
	if err := os.WriteFile(configPath, []byte(invalidYAML), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("Created invalid YAML file at: %s", configPath)

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
		if !strings.Contains(err.Error(), "parsing") {
			t.Errorf("error should mention parsing, got: %v", err)
		}
	}
}

// TestConfigLoadPermissionError tests LoadConfig with an unreadable file
func TestConfigLoadPermissionError(t *testing.T) {
	// Skip on systems where we can't reliably test permissions
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	t.Log("Testing LoadConfig with permission denied")

	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create file with no read permissions
	configPath := filepath.Join(bvDir, "drift.yaml")
	if err := os.WriteFile(configPath, []byte("density_warning_pct: 50"), 0000); err != nil {
		t.Fatal(err)
	}
	t.Logf("Created unreadable file at: %s", configPath)

	_, err := LoadConfig(tmpDir)
	if err == nil {
		t.Error("expected error for unreadable file, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
		if !strings.Contains(err.Error(), "reading") {
			t.Errorf("error should mention reading, got: %v", err)
		}
	}

	// Cleanup: restore permissions so temp dir can be removed
	os.Chmod(configPath, 0644)
}

// TestConfigSaveInvalidConfig tests SaveConfig with a config that fails validation
func TestConfigSaveInvalidConfig(t *testing.T) {
	t.Log("Testing SaveConfig with invalid config")

	tmpDir := t.TempDir()

	// Create invalid config
	invalidConfig := &Config{
		DensityWarningPct: -100, // Invalid: negative
	}

	err := SaveConfig(tmpDir, invalidConfig)
	if err == nil {
		t.Error("expected error for invalid config, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("error should mention invalid, got: %v", err)
		}
	}

	// Verify no file was created
	configPath := filepath.Join(tmpDir, ".bv", "drift.yaml")
	if _, err := os.Stat(configPath); err == nil {
		t.Error("config file should not have been created for invalid config")
	}
}

// TestConfigSaveMkdirError tests SaveConfig when .bv cannot be created (file exists)
func TestConfigSaveMkdirError(t *testing.T) {
	t.Log("Testing SaveConfig when .bv is a file instead of directory")

	tmpDir := t.TempDir()
	bvPath := filepath.Join(tmpDir, ".bv")

	// Create a FILE named .bv where a directory is expected
	if err := os.WriteFile(bvPath, []byte("blocking file"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("Created blocking file at: %s", bvPath)

	config := DefaultConfig()
	err := SaveConfig(tmpDir, config)
	if err == nil {
		t.Error("expected error when .bv is a file, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
		if !strings.Contains(err.Error(), "creating config directory") {
			t.Errorf("error should mention creating config directory, got: %v", err)
		}
	}
}

// TestConfigSavePermissionError tests SaveConfig when directory is not writable
func TestConfigSavePermissionError(t *testing.T) {
	// Skip on systems where we can't reliably test permissions
	if os.Getuid() == 0 {
		t.Skip("Skipping permission test when running as root")
	}

	t.Log("Testing SaveConfig with permission denied")

	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0555); err != nil { // Read-only directory
		t.Fatal(err)
	}
	t.Logf("Created read-only directory at: %s", bvDir)

	config := DefaultConfig()
	err := SaveConfig(tmpDir, config)
	if err == nil {
		t.Error("expected error for read-only directory, got nil")
	} else {
		t.Logf("Got expected error: %v", err)
	}

	// Cleanup: restore permissions
	os.Chmod(bvDir, 0755)
}

func TestConfigSave(t *testing.T) {
	tmpDir := t.TempDir()

	config := &Config{
		DensityWarningPct:        80,
		BlockedIncreaseThreshold: 3,
	}

	if err := SaveConfig(tmpDir, config); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".bv", "drift.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file should exist: %v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{"valid default", DefaultConfig(), false},
		{"negative density warning", &Config{DensityWarningPct: -10}, true},
		{"info > warning", &Config{DensityWarningPct: 10, DensityInfoPct: 20}, true},
		{"negative blocked", &Config{DensityWarningPct: 50, BlockedIncreaseThreshold: -1}, true},
		{"negative node growth", &Config{DensityWarningPct: 50, NodeGrowthInfoPct: -5}, true},
		{"negative edge growth", &Config{DensityWarningPct: 50, EdgeGrowthInfoPct: -5}, true},
		{"actionable decrease > 100", &Config{DensityWarningPct: 50, ActionableDecreaseWarningPct: 150}, true},
		{"negative actionable increase", &Config{DensityWarningPct: 50, ActionableIncreaseInfoPct: -10}, true},
		{"negative pagerank change", &Config{DensityWarningPct: 50, PageRankChangeWarningPct: -20}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCycleKey(t *testing.T) {
	// Same cycle represented identically should match
	key1 := cycleKey([]string{"A", "B", "C", "A"})
	key2 := cycleKey([]string{"A", "B", "C", "A"})

	if key1 != key2 {
		t.Errorf("identical cycles should match: %s vs %s", key1, key2)
	}

	// Different cycle should not match
	key3 := cycleKey([]string{"X", "Y", "Z", "X"})
	if key1 == key3 {
		t.Error("different cycles should have different keys")
	}

	// Empty cycle
	key4 := cycleKey([]string{})
	if key4 != "" {
		t.Errorf("empty cycle should have empty key, got %s", key4)
	}
}

// =============================================================================
// Calculator Method Branch Coverage Tests (bv-cam.3)
// =============================================================================

// TestCheckActionable_BaselineZero tests that actionable=0 baseline skips calculation
func TestCheckActionable_BaselineZero(t *testing.T) {
	t.Log("Testing checkActionable when baseline actionable=0 (skip case)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 0, // Zero baseline should skip calculation
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 50, // Huge increase but should be ignored
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	// Should not have actionable alert when baseline is 0
	for _, alert := range result.Alerts {
		if alert.Type == AlertActionableChange {
			t.Errorf("should not generate actionable alert when baseline is 0, got: %s", alert.Message)
		}
	}
	t.Log("Correctly skipped actionable check with zero baseline")
}

// TestCheckActionable_InfoIncrease tests actionable increase triggers info alert
func TestCheckActionable_InfoIncrease(t *testing.T) {
	t.Log("Testing checkActionable with 25% increase (info alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 100,
		},
	}

	// 25% increase should trigger info (default threshold is 20%)
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 125,
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertActionableChange {
			found = true
			if alert.Severity != SeverityInfo {
				t.Errorf("25%% increase should be info, got %s", alert.Severity)
			}
			t.Logf("Got expected alert: %s", alert.Message)
		}
	}
	if !found {
		t.Error("expected actionable_change info alert for 25% increase")
	}
}

// TestCheckActionable_DecreaseWarning tests large decrease triggers warning
func TestCheckActionable_DecreaseWarning(t *testing.T) {
	t.Log("Testing checkActionable with 35% decrease (warning alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 100,
		},
	}

	// 35% decrease should trigger warning (default threshold is 30%)
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 65,
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertActionableChange {
			found = true
			if alert.Severity != SeverityWarning {
				t.Errorf("35%% decrease should be warning, got %s", alert.Severity)
			}
			t.Logf("Got expected alert: %s", alert.Message)
		}
	}
	if !found {
		t.Error("expected actionable_change warning alert for 35% decrease")
	}
}

// TestCheckActionable_InfoDecrease tests moderate decrease yields info alert (not warning)
func TestCheckActionable_InfoDecrease(t *testing.T) {
	t.Log("Testing checkActionable with 25% decrease (info alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 80,
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 60, // 25% decrease (warning threshold is 30%)
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertActionableChange {
			found = true
			if alert.Severity != SeverityInfo {
				t.Errorf("25%% decrease should be info, got %s", alert.Severity)
			}
		}
	}
	if !found {
		t.Fatal("expected actionable_change info alert for 25% decrease")
	}
}

// TestCheckCycles_BaselineHasCycles tests that removing cycles doesn't alert
func TestCheckCycles_BaselineHasCycles(t *testing.T) {
	t.Log("Testing checkCycles when baseline has cycles but current removes them")

	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10},
		Cycles: [][]string{{"A", "B", "C", "A"}, {"X", "Y", "X"}},
	}

	// Current has fewer cycles - should NOT alert (only new cycles alert)
	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: [][]string{{"A", "B", "C", "A"}},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertNewCycle {
			t.Errorf("should not alert when cycles are removed, got: %s", alert.Message)
		}
	}
	t.Log("Correctly did not alert when cycles were removed")
}

// TestCheckCycles_NewCycles tests that new cycles trigger critical alerts
func TestCheckCycles_NewCycles(t *testing.T) {
	t.Log("Testing checkCycles when current snapshot adds new cycles")

	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10},
		Cycles: [][]string{}, // No cycles in baseline
	}

	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: [][]string{{"A", "B", "A"}},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertNewCycle {
			found = true
			if alert.Severity != SeverityCritical {
				t.Errorf("new cycles should be critical, got %s", alert.Severity)
			}
			if alert.Delta != 1 {
				t.Errorf("expected delta 1 new cycle, got %.0f", alert.Delta)
			}
		}
	}
	if !found {
		t.Fatal("expected critical new_cycle alert when cycles are added")
	}
}

// TestCheckCycles_SameCycles tests that identical cycles don't alert
func TestCheckCycles_SameCycles(t *testing.T) {
	t.Log("Testing checkCycles when baseline and current have same cycles")

	cycles := [][]string{{"A", "B", "C", "A"}, {"X", "Y", "X"}}

	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10},
		Cycles: cycles,
	}

	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: cycles,
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertNewCycle {
			t.Errorf("should not alert when cycles are identical, got: %s", alert.Message)
		}
	}
	t.Log("Correctly did not alert when cycles are identical")
}

// TestCheckCycles_BothEmpty tests that empty cycles in both don't alert
func TestCheckCycles_BothEmpty(t *testing.T) {
	t.Log("Testing checkCycles when both baseline and current have no cycles")

	bl := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10},
		Cycles: [][]string{},
	}

	current := &baseline.Baseline{
		Stats:  bl.Stats,
		Cycles: [][]string{},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertNewCycle {
			t.Errorf("should not alert when both have empty cycles, got: %s", alert.Message)
		}
	}
	t.Log("Correctly did not alert when both have empty cycles")
}

// TestCheckDensity_InfoLevel tests density increase at info level (not warning)
func TestCheckDensity_InfoLevel(t *testing.T) {
	t.Log("Testing checkDensity with 30% increase (info level, not warning)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.02,
		},
	}

	// 30% increase: above 20% info threshold, below 50% warning threshold
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.026, // 30% increase
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			found = true
			if alert.Severity != SeverityInfo {
				t.Errorf("30%% density increase should be info, got %s", alert.Severity)
			}
			t.Logf("Got expected alert: %s", alert.Message)
		}
	}
	if !found {
		t.Error("expected density_growth info alert for 30% increase")
	}
}

// TestCheckDensity_Decrease tests that density decrease doesn't alert
func TestCheckDensity_Decrease(t *testing.T) {
	t.Log("Testing checkDensity when density decreases (no alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.05,
		},
	}

	// Density decreased - should NOT alert
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.02, // 60% decrease
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			t.Errorf("should not alert when density decreases, got: %s", alert.Message)
		}
	}
	t.Log("Correctly did not alert when density decreased")
}

// TestCheckDensity_WarningLevel tests density increase crossing warning threshold
func TestCheckDensity_WarningLevel(t *testing.T) {
	t.Log("Testing checkDensity with 75% increase (warning level)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.02,
		},
	}

	// 75% increase: above 50% warning threshold
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.035, // (0.035-0.02)/0.02 = 75%
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	found := false
	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			found = true
			if alert.Severity != SeverityWarning {
				t.Errorf("75%% density increase should be warning, got %s", alert.Severity)
			}
			t.Logf("Got expected warning: %s", alert.Message)
		}
	}
	if !found {
		t.Fatal("expected density_growth warning alert for 75% increase")
	}
}

// TestCheckDensity_BaselineZero tests that zero baseline density skips check
func TestCheckDensity_BaselineZero(t *testing.T) {
	t.Log("Testing checkDensity when baseline density=0 (skip case)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0, // Zero baseline should skip
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.05, // Would be infinite increase but should be skipped
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			t.Errorf("should not alert when baseline density is 0, got: %s", alert.Message)
		}
	}
	t.Log("Correctly skipped density check with zero baseline")
}

// TestCheckActionable_SmallChanges tests that small changes don't trigger alerts
func TestCheckActionable_SmallChanges(t *testing.T) {
	t.Log("Testing checkActionable with small changes (no alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 100,
		},
	}

	// 10% increase (threshold 20%) -> No Alert
	currentInc := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 110,
		},
	}

	calcInc := NewCalculator(bl, currentInc, nil)
	resultInc := calcInc.Calculate()
	for _, alert := range resultInc.Alerts {
		if alert.Type == AlertActionableChange {
			t.Errorf("10%% increase should not alert, got: %s", alert.Message)
		}
	}

	// 10% decrease (threshold 20% for info) -> No Alert
	currentDec := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       100,
			ActionableCount: 90,
		},
	}

	calcDec := NewCalculator(bl, currentDec, nil)
	resultDec := calcDec.Calculate()
	for _, alert := range resultDec.Alerts {
		if alert.Type == AlertActionableChange {
			t.Errorf("10%% decrease should not alert, got: %s", alert.Message)
		}
	}
}

// TestCheckDensity_SmallIncrease tests that small density increase doesn't alert
func TestCheckDensity_SmallIncrease(t *testing.T) {
	t.Log("Testing checkDensity with 10% increase (no alert)")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.02,
		},
	}

	// 10% increase (threshold 20%)
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.022,
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	for _, alert := range result.Alerts {
		if alert.Type == AlertDensityGrowth {
			t.Errorf("10%% density increase should not alert, got: %s", alert.Message)
		}
	}
}

func TestCalculatorZeroBaseline(t *testing.T) {
	t.Log("Testing calculator with zero-value baseline")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{}, // All zero
	}

	// Current has some values
	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount:       10,
			EdgeCount:       20,
			Density:         0.1,
			BlockedCount:    2,
			ActionableCount: 8,
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	// Should not crash.
	// With zero baseline, most percentage calc checks should be skipped or handle div-by-zero safely.
	// Node/Edge growth checks check "if blNodes > 0".
	// Density check checks "if blDensity == 0".
	// Actionable check checks "if blAction > 0".
	// Blocked check is absolute difference, so 2 - 0 = 2. Threshold is 5. So no alert.

	if result.HasDrift {
		t.Errorf("expected no drift with zero baseline, got %d alerts", len(result.Alerts))
		for _, a := range result.Alerts {
			t.Logf("Alert: %s", a.Message)
		}
	}
}

func TestCalculatorBoundaryThresholds(t *testing.T) {
	t.Log("Testing exact boundary conditions")

	cfg := &Config{
		DensityWarningPct:        50.0,
		DensityInfoPct:           0.0,  // keep default info enabled for below test
		NodeGrowthInfoPct:        1000, // silence unrelated alerts
		EdgeGrowthInfoPct:        1000, // silence unrelated alerts
		BlockedIncreaseThreshold: 999,  // silence blocked delta alerts
	}

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.50,
		},
	}

	// Case 1: Slightly above 50% increase (~50.2%) to avoid float rounding ambiguity
	currentExact := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.751,
		},
	}

	calcExact := NewCalculator(bl, currentExact, cfg)
	resExact := calcExact.Calculate()

	found := false
	for _, a := range resExact.Alerts {
		if a.Type == AlertDensityGrowth && a.Severity == SeverityWarning {
			found = true
		}
	}
	if !found {
		t.Error("Exact 50% density increase should trigger warning")
		for _, a := range resExact.Alerts {
			t.Logf("Found alert: [%s] %s (Delta: %f)", a.Severity, a.Message, a.Delta)
		}
	}

	// Case 2: Just below 50% increase (0.10 -> 0.1499)
	// Should NOT trigger warning (assuming no info threshold or low info threshold)
	// Default info is 20%, so it might trigger Info. Let's set Info to 49.9 to be safe or ignore info alerts.
	// Let's explicitly check it does NOT trigger Warning.
	currentBelow := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 100,
			Density:   0.1499,
		},
	}

	calcBelow := NewCalculator(bl, currentBelow, cfg)
	resBelow := calcBelow.Calculate()

	for _, a := range resBelow.Alerts {
		if a.Type == AlertDensityGrowth && a.Severity == SeverityWarning {
			t.Errorf("49.9%% density increase should NOT trigger warning, got: %s", a.Message)
		}
	}
}

func TestCalculatorEmptyMetrics(t *testing.T) {
	t.Log("Testing with empty metric slices")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{}, // Empty
		},
		Cycles: [][]string{},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{}, // Empty
		},
		Cycles: [][]string{},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	if result.HasDrift {
		t.Error("Empty metrics should not trigger drift")
	}
}

func TestCalculatorLargeValues(t *testing.T) {
	t.Log("Testing with large values to ensure no overflow/panic")

	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 1000000,
			EdgeCount: 5000000,
			Density:   0.5,
		},
	}

	current := &baseline.Baseline{
		Stats: baseline.GraphStats{
			NodeCount: 1000000, // No change
			EdgeCount: 5000000,
			Density:   0.5,
		},
	}

	calc := NewCalculator(bl, current, nil)
	result := calc.Calculate()

	if result.HasDrift {
		t.Error("Large stable values should not trigger drift")
	}
}

func TestCheckBlocked_StrictThresholds(t *testing.T) {
	bl := &baseline.Baseline{Stats: baseline.GraphStats{BlockedCount: 5}}

	tests := []struct {
		name      string
		threshold int
		curCount  int
		wantAlert bool
	}{
		{"threshold 0, delta 0", 0, 5, false}, // Should NOT alert (fixed bug)
		{"threshold 0, delta 1", 0, 6, true},  // Should alert (any increase)
		{"threshold 5, delta 4", 5, 9, false}, // Should not alert (below threshold)
		{"threshold 5, delta 5", 5, 10, true}, // Should alert (at threshold)
		{"threshold 5, delta 0", 5, 5, false}, // Should not alert
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := &baseline.Baseline{Stats: baseline.GraphStats{BlockedCount: tt.curCount}}
			cfg := &Config{BlockedIncreaseThreshold: tt.threshold}
			calc := NewCalculator(bl, cur, cfg)
			res := calc.Calculate()

			gotAlert := false
			for _, alert := range res.Alerts {
				if alert.Type == AlertBlockedIncrease {
					gotAlert = true
					break
				}
			}

			if gotAlert != tt.wantAlert {
				t.Errorf("wantAlert %v, got %v", tt.wantAlert, gotAlert)
			}
		})
	}
}

// TestDisabledAlerts verifies that disabled alert types are not emitted (bv-167)
func TestDisabledAlerts(t *testing.T) {
	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 15},
	}
	current := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 10, EdgeCount: 15},
		Cycles: [][]string{{"A", "B", "C", "A"}},
	}

	// Without disabling, we should get a cycle alert
	cfg := DefaultConfig()
	calc := NewCalculator(bl, current, cfg)
	result := calc.Calculate()

	hasCycleAlert := false
	for _, a := range result.Alerts {
		if a.Type == AlertNewCycle {
			hasCycleAlert = true
			break
		}
	}
	if !hasCycleAlert {
		t.Fatal("expected cycle alert without disabling")
	}

	// With disabled_alerts containing new_cycle, no cycle alert
	cfg.DisabledAlerts = []string{"new_cycle"}
	calc = NewCalculator(bl, current, cfg)
	result = calc.Calculate()

	for _, a := range result.Alerts {
		if a.Type == AlertNewCycle {
			t.Error("cycle alert should be disabled")
		}
	}
}

// TestIsAlertDisabled verifies the IsAlertDisabled helper (bv-167)
func TestIsAlertDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.IsAlertDisabled("stale_issue") {
		t.Error("stale_issue should not be disabled by default")
	}

	cfg.DisabledAlerts = []string{"stale_issue", "new_cycle"}
	if !cfg.IsAlertDisabled("stale_issue") {
		t.Error("stale_issue should be disabled")
	}
	if !cfg.IsAlertDisabled("new_cycle") {
		t.Error("new_cycle should be disabled")
	}
	if cfg.IsAlertDisabled("density_growth") {
		t.Error("density_growth should not be disabled")
	}
}

// TestLabelOverrides verifies per-label threshold customization (bv-167)
func TestLabelOverrides(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StaleWarningDays = 14
	cfg.StaleCriticalDays = 30
	cfg.InProgressStaleMultiplier = 0.5

	// Without overrides, defaults are returned
	warn, crit, mult := cfg.GetStalenessThresholds([]string{"some-label"})
	if warn != 14 || crit != 30 || mult != 0.5 {
		t.Errorf("expected defaults without overrides, got warn=%d crit=%d mult=%f", warn, crit, mult)
	}

	// Add label overrides
	cfg.LabelOverrides = map[string]*LabelConfig{
		"urgent": {
			StaleWarningDays:  3,
			StaleCriticalDays: 7,
		},
		"low-priority": {
			StaleWarningDays:  30,
			StaleCriticalDays: 60,
		},
	}

	// Issue with "urgent" label should get tighter thresholds
	warn, crit, _ = cfg.GetStalenessThresholds([]string{"urgent"})
	if warn != 3 || crit != 7 {
		t.Errorf("urgent label should have tighter thresholds, got warn=%d crit=%d", warn, crit)
	}

	// Issue with "low-priority" label gets looser thresholds if explicitly configured
	// We want to allow relaxing thresholds for specific labels (e.g. icebox, backlog)
	warn, crit, _ = cfg.GetStalenessThresholds([]string{"low-priority"})
	if warn != 30 || crit != 60 {
		t.Errorf("low-priority should override with looser thresholds, got warn=%d crit=%d", warn, crit)
	}

	// Issue with multiple labels uses tightest (smallest) values
	warn, crit, _ = cfg.GetStalenessThresholds([]string{"low-priority", "urgent"})
	if warn != 3 || crit != 7 {
		t.Errorf("multiple labels should use tightest, got warn=%d crit=%d", warn, crit)
	}
}

// TestLabelOverridesValidation verifies validation of label overrides (bv-167)
func TestLabelOverridesValidation(t *testing.T) {
	cfg := DefaultConfig()

	// Valid config
	cfg.LabelOverrides = map[string]*LabelConfig{
		"urgent": {StaleWarningDays: 3, StaleCriticalDays: 7},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config should pass validation: %v", err)
	}

	// Invalid: critical < warning
	cfg.LabelOverrides = map[string]*LabelConfig{
		"broken": {StaleWarningDays: 10, StaleCriticalDays: 5},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("critical < warning should fail validation")
	}

	// Invalid: negative days
	cfg.LabelOverrides = map[string]*LabelConfig{
		"broken": {StaleWarningDays: -1},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("negative days should fail validation")
	}
}

// alertFixture builds a calculator whose Calculate output contains at least
// one alert of the named type. Each fixture is minimal and isolated so a
// failing type points at exactly one emitter.
type alertFixture struct {
	baseline    *baseline.Baseline
	current     *baseline.Baseline
	issues      []model.Issue
	config      *Config
	sourceDiffs []datasource.SourceDiff
}

func alertFixtures(now time.Time) map[AlertType]alertFixture {
	quietStats := baseline.GraphStats{NodeCount: 10, EdgeCount: 10, OpenCount: 10, ActionableCount: 5}
	quiet := func() (*baseline.Baseline, *baseline.Baseline) {
		return &baseline.Baseline{Stats: quietStats}, &baseline.Baseline{Stats: quietStats}
	}
	fresh := now.Add(-time.Hour)
	blocksOn := func(id, blocker string) *model.Dependency {
		return &model.Dependency{IssueID: id, DependsOnID: blocker, Type: model.DepBlocks}
	}
	closedAt := func(t time.Time) *time.Time { return &t }

	fixtures := map[AlertType]alertFixture{}

	bl, cur := quiet()
	fixtures[AlertStaleIssue] = alertFixture{baseline: bl, current: cur, issues: []model.Issue{
		{ID: "STALE", Status: model.StatusOpen, Labels: []string{"backend"}, UpdatedAt: now.AddDate(0, 0, -20)},
	}}

	bl, cur = quiet()
	fixtures[AlertBlockingCascade] = alertFixture{baseline: bl, current: cur, issues: []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen, Priority: 2, UpdatedAt: fresh},
		{ID: "D1", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("D1", "ROOT")}},
		{ID: "D2", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("D2", "ROOT")}},
		{ID: "D3", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("D3", "ROOT")}},
	}}

	bl, cur = quiet()
	fixtures[AlertHighImpactUnblock] = alertFixture{baseline: bl, current: cur, issues: []model.Issue{
		{ID: "ROOT", Status: model.StatusOpen, Priority: 2, UpdatedAt: fresh},
		{ID: "P0A", Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P0A", "ROOT")}},
		{ID: "P1B", Status: model.StatusOpen, Priority: 1, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P1B", "ROOT")}},
		{ID: "P3C", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P3C", "ROOT")}},
	}}

	bl, cur = quiet()
	fixtures[AlertAbandonedClaim] = alertFixture{baseline: bl, current: cur, issues: []model.Issue{
		{ID: "CLAIMED", Status: model.StatusInProgress, Assignee: "agent-7", UpdatedAt: now.AddDate(0, 0, -20)},
	}}

	bl, cur = quiet()
	fixtures[AlertPotentialDuplicate] = alertFixture{baseline: bl, current: cur, issues: []model.Issue{
		{ID: "DUP-1", Title: "Fix login timeout on slow networks", Status: model.StatusOpen, UpdatedAt: fresh},
		{ID: "DUP-2", Title: "Fix login timeout on slow networks", Status: model.StatusOpen, UpdatedAt: fresh},
	}}

	// A P4 hub that everything depends on: the graph says it deserves a
	// far higher priority than it carries.
	bl, cur = quiet()
	mismatch := []model.Issue{{ID: "HUB", Status: model.StatusOpen, Priority: 4, UpdatedAt: fresh}}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("LEAF-%d", i)
		mismatch = append(mismatch, model.Issue{ID: id, Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn(id, "HUB")}})
	}
	fixtures[AlertPriorityMismatch] = alertFixture{baseline: bl, current: cur, issues: mismatch}

	bl, cur = quiet()
	velocity := []model.Issue{}
	for i := 0; i < 6; i++ {
		velocity = append(velocity, model.Issue{ID: fmt.Sprintf("OLD-%d", i), Status: model.StatusClosed, UpdatedAt: fresh, ClosedAt: closedAt(now.AddDate(0, 0, -10))})
	}
	velocity = append(velocity, model.Issue{ID: "NEW-0", Status: model.StatusClosed, UpdatedAt: fresh, ClosedAt: closedAt(now.AddDate(0, 0, -2))})
	fixtures[AlertVelocityDrop] = alertFixture{baseline: bl, current: cur, issues: velocity}

	fixtures[AlertNewCycle] = alertFixture{
		baseline: &baseline.Baseline{Stats: quietStats},
		current:  &baseline.Baseline{Stats: quietStats, Cycles: [][]string{{"A", "B", "A"}}},
	}
	fixtures[AlertDensityGrowth] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, Density: 0.10}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 16, Density: 0.16}},
	}
	fixtures[AlertNodeCountChange] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 14, EdgeCount: 10}},
	}
	fixtures[AlertEdgeCountChange] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 14}},
	}
	fixtures[AlertScopeCreep] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, OpenCount: 10}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, OpenCount: 13}},
	}
	fixtures[AlertBlockedIncrease] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, BlockedCount: 1}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, BlockedCount: 7}},
	}
	fixtures[AlertActionableChange] = alertFixture{
		baseline: &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, ActionableCount: 10}},
		current:  &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 10, EdgeCount: 10, ActionableCount: 5}},
	}
	fixtures[AlertPageRankChange] = alertFixture{
		baseline: &baseline.Baseline{Stats: quietStats, TopMetrics: baseline.TopMetrics{PageRank: []baseline.MetricItem{{ID: "X", Value: 0.10}}}},
		current:  &baseline.Baseline{Stats: quietStats, TopMetrics: baseline.TopMetrics{PageRank: []baseline.MetricItem{{ID: "X", Value: 0.20}}}},
	}
	bl, cur = quiet()
	fixtures[AlertSourceInconsistency] = alertFixture{
		baseline: bl,
		current:  cur,
		issues: []model.Issue{
			{ID: "SRC-1", Title: "SQLite title", Status: model.StatusOpen, Labels: []string{"sync"}, UpdatedAt: fresh},
		},
		sourceDiffs: []datasource.SourceDiff{
			datasource.DetectInconsistencies(
				[]model.Issue{{ID: "SRC-1", Title: "SQLite title", Status: model.StatusOpen, Priority: 1}},
				[]model.Issue{{ID: "SRC-1", Title: "SQLite title", Status: model.StatusClosed, Priority: 1}},
				".beads/beads.db",
				".beads/issues.jsonl",
				datasource.DiffOptions{CompareFields: []string{"status", "priority", "title"}},
			),
		},
	}
	return fixtures
}

func alertsOfType(result *Result, typ AlertType) []Alert {
	var out []Alert
	for _, a := range result.Alerts {
		if a.Type == typ {
			out = append(out, a)
		}
	}
	return out
}

// TestDrift_EveryAlertTypeHasEmitter is the D7 gate: every declared alert
// type fires on its fixture, is silenced by disabled_alerts, carries a
// suggested action, and a quiet fixture yields no alerts at all.
func TestDrift_EveryAlertTypeHasEmitter(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fixtures := alertFixtures(now)

	declared := AllAlertTypes()
	if len(declared) != len(fixtures) {
		t.Fatalf("AllAlertTypes has %d entries but %d fixtures exist; add a fixture for every type", len(declared), len(fixtures))
	}
	for _, typ := range declared {
		fx, ok := fixtures[typ]
		if !ok {
			t.Fatalf("no fixture for alert type %q", typ)
		}
		t.Run(string(typ), func(t *testing.T) {
			calc := NewCalculator(fx.baseline, fx.current, fx.config)
			calc.SetNow(now)
			calc.SetIssues(fx.issues)
			calc.SetSourceDiffs(fx.sourceDiffs)
			result := calc.Calculate()
			got := alertsOfType(result, typ)
			t.Logf("%s: %d alert(s): %+v", typ, len(got), got)
			if len(got) == 0 {
				t.Fatalf("fixture for %s produced no %s alert; all alerts: %+v", typ, typ, result.Alerts)
			}
			for _, a := range got {
				if a.SuggestedAction == "" {
					t.Fatalf("%s alert has no suggested_action: %+v", typ, a)
				}
				if a.Message == "" || a.Severity == "" {
					t.Fatalf("%s alert missing message/severity: %+v", typ, a)
				}
				if !a.DetectedAt.Equal(now) {
					t.Fatalf("%s alert detected_at=%s; want the pinned instant %s", typ, a.DetectedAt, now)
				}
			}

			cfg := fx.config
			if cfg == nil {
				cfg = DefaultConfig()
			}
			disabled := *cfg
			disabled.DisabledAlerts = append(append([]string(nil), cfg.DisabledAlerts...), string(typ))
			calc = NewCalculator(fx.baseline, fx.current, &disabled)
			calc.SetNow(now)
			calc.SetIssues(fx.issues)
			calc.SetSourceDiffs(fx.sourceDiffs)
			if left := alertsOfType(calc.Calculate(), typ); len(left) != 0 {
				t.Fatalf("disabled_alerts=[%s] still produced %d alert(s): %+v", typ, len(left), left)
			}
		})
	}

	t.Run("quiet fixture", func(t *testing.T) {
		stats := baseline.GraphStats{NodeCount: 3, EdgeCount: 2, OpenCount: 3, ActionableCount: 2}
		fresh := now.Add(-time.Hour)
		issues := []model.Issue{
			{ID: "A", Title: "Write the parser", Status: model.StatusOpen, Priority: 2, UpdatedAt: fresh},
			{ID: "B", Title: "Ship the release notes", Status: model.StatusInProgress, Priority: 2, Assignee: "me", UpdatedAt: fresh},
			{ID: "C", Title: "Rotate the signing key", Status: model.StatusOpen, Priority: 2, UpdatedAt: fresh, Dependencies: []*model.Dependency{{IssueID: "C", DependsOnID: "A", Type: model.DepBlocks}}},
		}
		calc := NewCalculator(&baseline.Baseline{Stats: stats}, &baseline.Baseline{Stats: stats}, nil)
		calc.SetNow(now)
		calc.SetIssues(issues)
		result := calc.Calculate()
		if len(result.Alerts) != 0 || result.HasDrift {
			t.Fatalf("healthy project produced alerts: %+v", result.Alerts)
		}
		if result.ExitCode() != 0 {
			t.Fatalf("quiet exit code=%d; want 0", result.ExitCode())
		}
	})
}

func TestDrift_NewEmitterSemantics(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	quiet := baseline.GraphStats{NodeCount: 10, EdgeCount: 10, OpenCount: 10, ActionableCount: 5}
	newCalc := func(cfg *Config, issues []model.Issue) *Result {
		calc := NewCalculator(&baseline.Baseline{Stats: quiet}, &baseline.Baseline{Stats: quiet}, cfg)
		calc.SetNow(now)
		calc.SetIssues(issues)
		return calc.Calculate()
	}
	blocksOn := func(id, blocker string) *model.Dependency {
		return &model.Dependency{IssueID: id, DependsOnID: blocker, Type: model.DepBlocks}
	}
	closedAt := func(t time.Time) *time.Time { return &t }

	t.Run("velocity_drop needs a real baseline window", func(t *testing.T) {
		var issues []model.Issue
		for i := 0; i < 3; i++ { // only 3 closes in the prior window: below VelocityMinBaseline (5)
			issues = append(issues, model.Issue{ID: fmt.Sprintf("OLD-%d", i), Status: model.StatusClosed, UpdatedAt: fresh, ClosedAt: closedAt(now.AddDate(0, 0, -10))})
		}
		if got := alertsOfType(newCalc(nil, issues), AlertVelocityDrop); len(got) != 0 {
			t.Fatalf("3 prior closes must not alarm: %+v", got)
		}
		for i := 3; i < 6; i++ {
			issues = append(issues, model.Issue{ID: fmt.Sprintf("OLD-%d", i), Status: model.StatusClosed, UpdatedAt: fresh, ClosedAt: closedAt(now.AddDate(0, 0, -9))})
		}
		for i := 0; i < 4; i++ { // 4 recent vs 6 prior = -33%, under the 50% default
			issues = append(issues, model.Issue{ID: fmt.Sprintf("NEW-%d", i), Status: model.StatusClosed, UpdatedAt: fresh, ClosedAt: closedAt(now.AddDate(0, 0, -1))})
		}
		if got := alertsOfType(newCalc(nil, issues), AlertVelocityDrop); len(got) != 0 {
			t.Fatalf("a 33%% dip must not alarm at the 50%% default: %+v", got)
		}
		cfg := DefaultConfig()
		cfg.VelocityDropPct = 25
		got := alertsOfType(newCalc(cfg, issues), AlertVelocityDrop)
		if len(got) != 1 || got[0].Severity != SeverityWarning || got[0].BaselineVal != 6 || got[0].CurrentVal != 4 {
			t.Fatalf("with velocity_drop_pct=25 want one warning 6→4, got %+v", got)
		}
	})

	t.Run("high_impact_unblock escalates on two urgent downstream items", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "ROOT", Status: model.StatusOpen, Priority: 2, UpdatedAt: fresh},
			{ID: "P0A", Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P0A", "ROOT")}},
			{ID: "P3B", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P3B", "ROOT")}},
			{ID: "P3C", Status: model.StatusOpen, Priority: 3, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("P3C", "ROOT")}},
		}
		got := alertsOfType(newCalc(nil, issues), AlertHighImpactUnblock)
		if len(got) != 1 || got[0].Severity != SeverityInfo || got[0].IssueID != "ROOT" || got[0].UnblocksCount != 3 {
			t.Fatalf("one P0 downstream: want info for ROOT unblocking 3, got %+v", got)
		}
		if len(got[0].Details) != 1 || got[0].Details[0] != "P0A" {
			t.Fatalf("details should list the urgent items only: %+v", got[0].Details)
		}
		issues[2].Priority = 1
		got = alertsOfType(newCalc(nil, issues), AlertHighImpactUnblock)
		if len(got) != 1 || got[0].Severity != SeverityWarning {
			t.Fatalf("two urgent downstream items: want warning, got %+v", got)
		}
		// Plenty of downstream work but none of it urgent: cascade fires, this does not.
		for i := range issues {
			issues[i].Priority = 3
		}
		result := newCalc(nil, issues)
		if got := alertsOfType(result, AlertHighImpactUnblock); len(got) != 0 {
			t.Fatalf("no urgent downstream: high_impact_unblock must stay silent, got %+v", got)
		}
		if got := alertsOfType(result, AlertBlockingCascade); len(got) != 1 {
			t.Fatalf("blocking_cascade should still fire on 3 downstream items, got %+v", got)
		}
	})

	t.Run("abandoned_claim requires an assignee and honours label overrides", func(t *testing.T) {
		idle := now.AddDate(0, 0, -20) // > 14d default (14 x 0.5 x 2)
		issues := []model.Issue{
			{ID: "NOBODY", Status: model.StatusInProgress, UpdatedAt: idle},
			{ID: "CLAIMED", Status: model.StatusInProgress, Assignee: "agent-7", UpdatedAt: idle},
			{ID: "RECENT", Status: model.StatusInProgress, Assignee: "agent-8", UpdatedAt: now.AddDate(0, 0, -10)},
			{ID: "URGENT", Status: model.StatusInProgress, Assignee: "agent-9", Labels: []string{"urgent"}, UpdatedAt: now.AddDate(0, 0, -5)},
		}
		cfg := DefaultConfig()
		cfg.LabelOverrides = map[string]*LabelConfig{"urgent": {StaleWarningDays: 2, StaleCriticalDays: 4}}
		got := alertsOfType(newCalc(cfg, issues), AlertAbandonedClaim)
		ids := map[string]Alert{}
		for _, a := range got {
			ids[a.IssueID] = a
		}
		if _, ok := ids["NOBODY"]; ok {
			t.Fatalf("in_progress without an assignee is stale, not an abandoned claim: %+v", got)
		}
		if _, ok := ids["RECENT"]; ok {
			t.Fatalf("10 idle days is under the 14-day default: %+v", got)
		}
		claimed, ok := ids["CLAIMED"]
		if !ok || claimed.Severity != SeverityWarning || !strings.Contains(claimed.Message, "agent-7") {
			t.Fatalf("want a warning naming agent-7 for CLAIMED, got %+v", got)
		}
		urgent, ok := ids["URGENT"]
		if !ok {
			t.Fatalf("label override (2d x 0.5 x 2 = 2d) should flag URGENT after 5 idle days: %+v", got)
		}
		if len(urgent.Labels) != 1 || urgent.Labels[0] != "urgent" {
			t.Fatalf("alert should carry the issue labels for --alert-label: %+v", urgent)
		}
	})

	t.Run("potential_duplicate is capped and skips dissimilar titles", func(t *testing.T) {
		var issues []model.Issue
		for i := 0; i < 6; i++ {
			issues = append(issues, model.Issue{ID: fmt.Sprintf("SAME-%d", i), Title: "Migrate billing exports to parquet format", Status: model.StatusOpen, UpdatedAt: fresh})
		}
		issues = append(issues, model.Issue{ID: "OTHER", Title: "Rename the tutorial header", Status: model.StatusOpen, UpdatedAt: fresh})
		// Closed twins are history, not duplicates to consolidate.
		issues = append(issues,
			model.Issue{ID: "DONE-1", Title: "Archive the quarterly invoice batch", Status: model.StatusClosed, UpdatedAt: fresh},
			model.Issue{ID: "DONE-2", Title: "Archive the quarterly invoice batch", Status: model.StatusClosed, UpdatedAt: fresh},
		)
		cfg := DefaultConfig()
		cfg.DuplicateMaxAlerts = 4
		got := alertsOfType(newCalc(cfg, issues), AlertPotentialDuplicate)
		if len(got) != 4 {
			t.Fatalf("duplicate_max_alerts=4 should cap the %d similar pairs to 4, got %d: %+v", 15, len(got), got)
		}
		for _, a := range got {
			if a.IssueID == "OTHER" || a.RelatedIssueID == "OTHER" || a.RelatedIssueID == "" {
				t.Fatalf("dissimilar issue paired, or pair missing related_issue_id: %+v", a)
			}
			if strings.HasPrefix(a.IssueID, "DONE") || strings.HasPrefix(a.RelatedIssueID, "DONE") {
				t.Fatalf("closed issues must not be paired as duplicates: %+v", a)
			}
		}
		cfg.DuplicateMaxAlerts = 100
		for _, a := range alertsOfType(newCalc(cfg, issues), AlertPotentialDuplicate) {
			if strings.HasPrefix(a.IssueID, "DONE") || strings.HasPrefix(a.RelatedIssueID, "DONE") {
				t.Fatalf("closed issues must not be paired as duplicates even uncapped: %+v", a)
			}
		}
	})

	t.Run("priority_mismatch respects the confidence floor", func(t *testing.T) {
		issues := []model.Issue{{ID: "HUB", Status: model.StatusOpen, Priority: 4, UpdatedAt: fresh}}
		for i := 0; i < 8; i++ {
			id := fmt.Sprintf("LEAF-%d", i)
			issues = append(issues, model.Issue{ID: id, Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn(id, "HUB")}})
		}
		got := alertsOfType(newCalc(nil, issues), AlertPriorityMismatch)
		var hub *Alert
		for i := range got {
			if got[i].IssueID == "HUB" {
				hub = &got[i]
			}
		}
		if hub == nil || hub.Severity != SeverityWarning || hub.BaselineVal != 4 || hub.CurrentVal >= 4 {
			t.Fatalf("want a warning that P4 HUB deserves a higher priority, got %+v", got)
		}
		// Find a milder hub whose "increase" recommendation lands between
		// the default floor and certainty, so raising the floor just above
		// it must silence it. Shapes are probed because confidence saturates
		// quickly on tiny graphs.
		var mild []model.Issue
		var conf float64
		var seen []string
	probe:
		for _, hubPriority := range []int{2, 3, 4} {
			for leaves := 1; leaves <= 6 && mild == nil; leaves++ {
				candidate := []model.Issue{{ID: "HUB", Status: model.StatusOpen, Priority: hubPriority, UpdatedAt: fresh}}
				for i := 0; i < leaves; i++ {
					id := fmt.Sprintf("LEAF-%d", i)
					candidate = append(candidate, model.Issue{ID: id, Status: model.StatusOpen, Priority: 1, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn(id, "HUB")}})
				}
				analyzer := analysis.NewAnalyzer(candidate)
				// Probe with the calculator's clock, so these fresh fixtures
				// cannot acquire staleness confidence as the calendar advances.
				analyzer.SetNow(now)
				for _, rec := range analyzer.GenerateRecommendations() {
					if rec.IssueID != "HUB" || rec.Direction != "increase" {
						continue
					}
					seen = append(seen, fmt.Sprintf("P%d/%d leaves=%.2f", hubPriority, leaves, rec.Confidence))
					if rec.Confidence >= 0.6 && rec.Confidence < 1.0 {
						mild, conf = candidate, rec.Confidence
						break probe
					}
				}
			}
		}
		if mild == nil {
			t.Fatalf("no probed fixture produced an increase recommendation in [0.6,1.0): %v", seen)
		}
		got = alertsOfType(newCalc(nil, mild), AlertPriorityMismatch)
		if len(got) != 1 || got[0].IssueID != "HUB" {
			t.Fatalf("mild hub fixture should produce exactly one HUB alert at the default floor, got %+v", got)
		}
		cfg := DefaultConfig()
		cfg.PriorityMismatchMinConfidence = conf + 0.01
		if got := alertsOfType(newCalc(cfg, mild), AlertPriorityMismatch); len(got) != 0 {
			t.Fatalf("confidence floor %.2f should silence a %.2f recommendation, got %+v", cfg.PriorityMismatchMinConfidence, conf, got)
		}
		// Downgrade suggestions are not alerts: a leaf that the graph says
		// could be lower stays silent.
		leafOnly := []model.Issue{
			{ID: "A", Title: "one", Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh},
			{ID: "B", Title: "two", Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{blocksOn("B", "A")}},
		}
		for _, a := range alertsOfType(newCalc(nil, leafOnly), AlertPriorityMismatch) {
			if a.Delta > 0 {
				t.Fatalf("decrease recommendation surfaced as an alert: %+v", a)
			}
		}
	})

	t.Run("scope_creep needs a baseline open count", func(t *testing.T) {
		calc := NewCalculator(&baseline.Baseline{Stats: baseline.GraphStats{OpenCount: 0}}, &baseline.Baseline{Stats: baseline.GraphStats{OpenCount: 30}}, nil)
		calc.SetNow(now)
		if got := alertsOfType(calc.Calculate(), AlertScopeCreep); len(got) != 0 {
			t.Fatalf("no baseline open count: must stay silent, got %+v", got)
		}
		calc = NewCalculator(&baseline.Baseline{Stats: baseline.GraphStats{OpenCount: 10}}, &baseline.Baseline{Stats: baseline.GraphStats{OpenCount: 11}}, nil)
		calc.SetNow(now)
		if got := alertsOfType(calc.Calculate(), AlertScopeCreep); len(got) != 0 {
			t.Fatalf("10%% growth is under the 20%% default, got %+v", got)
		}
	})
}

// TestDrift_ExpensiveChecksSkipAboveCap: the TUI snapshot builder runs
// Calculate on every refresh, so on big graphs the whole-graph checks must
// step aside and say so instead of stalling the snapshot.
func TestDrift_ExpensiveChecksSkipAboveCap(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	issues := []model.Issue{{ID: "HUB", Status: model.StatusOpen, Priority: 4, UpdatedAt: fresh}}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("LEAF-%d", i)
		issues = append(issues, model.Issue{ID: id, Title: "Fix login timeout on slow networks", Status: model.StatusOpen, Priority: 0, UpdatedAt: fresh, Dependencies: []*model.Dependency{{IssueID: id, DependsOnID: "HUB", Type: model.DepBlocks}}})
	}
	stats := baseline.GraphStats{NodeCount: 9, EdgeCount: 8}
	cfg := DefaultConfig()
	cfg.ProactiveMaxIssues = 5
	calc := NewCalculator(&baseline.Baseline{Stats: stats}, &baseline.Baseline{Stats: stats}, cfg)
	calc.SetNow(now)
	calc.SetIssues(issues)
	result := calc.Calculate()

	if got := alertsOfType(result, AlertPriorityMismatch); len(got) != 0 {
		t.Fatalf("priority_mismatch must be skipped above the cap, got %+v", got)
	}
	if got := alertsOfType(result, AlertPotentialDuplicate); len(got) != 0 {
		t.Fatalf("potential_duplicate must be skipped above the cap, got %+v", got)
	}
	if got := alertsOfType(result, AlertHighImpactUnblock); len(got) == 0 {
		t.Fatalf("cheap checks must still run above the cap: %+v", result.Alerts)
	}
	skipped := map[AlertType]string{}
	for _, s := range result.SkippedChecks {
		skipped[s.Type] = s.Reason
	}
	if len(skipped) != 2 || !strings.Contains(skipped[AlertPriorityMismatch], "proactive_max_issues=5") || skipped[AlertPotentialDuplicate] == "" {
		t.Fatalf("skipped_checks should name both expensive checks with the cap: %+v", result.SkippedChecks)
	}

	cfg.ProactiveMaxIssues = 0 // no cap
	calc = NewCalculator(&baseline.Baseline{Stats: stats}, &baseline.Baseline{Stats: stats}, cfg)
	calc.SetNow(now)
	calc.SetIssues(issues)
	result = calc.Calculate()
	if len(result.SkippedChecks) != 0 || len(alertsOfType(result, AlertPriorityMismatch)) == 0 {
		t.Fatalf("cap 0 must run everything: skipped=%+v alerts=%+v", result.SkippedChecks, result.Alerts)
	}
}

func TestConfigValidate_ProactiveKeys(t *testing.T) {
	cfg := &Config{DensityWarningPct: 50}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("omitted proactive keys must backfill, got %v", err)
	}
	def := DefaultConfig()
	if cfg.VelocityDropPct != def.VelocityDropPct || cfg.HighImpactUnblockMin != def.HighImpactUnblockMin || cfg.DuplicateJaccardThreshold != def.DuplicateJaccardThreshold || cfg.SourceDriftMaxAlerts != 0 || cfg.PriorityMismatchMinConfidence != def.PriorityMismatchMinConfidence || cfg.AbandonedClaimMultiplier != def.AbandonedClaimMultiplier || cfg.ScopeCreepPct != def.ScopeCreepPct {
		t.Fatalf("backfilled config differs from defaults: %+v", cfg)
	}
	bad := map[string]*Config{
		"velocity pct > 100":      {DensityWarningPct: 50, VelocityDropPct: 150},
		"negative window":         {DensityWarningPct: 50, VelocityWindowDays: -1},
		"priority max > 4":        {DensityWarningPct: 50, HighImpactPriorityMax: 9},
		"jaccard > 1":             {DensityWarningPct: 50, DuplicateJaccardThreshold: 1.5},
		"confidence > 1":          {DensityWarningPct: 50, PriorityMismatchMinConfidence: 2},
		"negative duplicate cap":  {DensityWarningPct: 50, DuplicateMaxAlerts: -1},
		"abandoned multiplier 11": {DensityWarningPct: 50, AbandonedClaimMultiplier: 11},
		"negative proactive cap":  {DensityWarningPct: 50, ProactiveMaxIssues: -5},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: want validation error", name)
		}
	}
	// The example file must round-trip through the loader with every key present.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(dir), []byte(ExampleConfig()), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("example config failed to load: %v", err)
	}
	if loaded.VelocityWindowDays != 7 || loaded.DuplicateMaxAlerts != 10 || loaded.SourceDriftMaxAlerts != 10 || loaded.ScopeCreepPct != 20 || loaded.HighImpactPriorityMax != 1 {
		t.Fatalf("example config keys not honoured: %+v", loaded)
	}
	for _, key := range []string{"proactive_max_issues", "scope_creep_pct", "velocity_drop_pct", "velocity_window_days", "velocity_min_baseline", "high_impact_unblock_min", "high_impact_priority_max", "abandoned_claim_multiplier", "duplicate_jaccard_threshold", "duplicate_max_alerts", "source_drift_max_alerts", "priority_mismatch_min_confidence", "sprint_blocked_days", "sprint_inactive_days"} {
		if !strings.Contains(ExampleConfig(), key+":") {
			t.Errorf("ExampleConfig missing key %s", key)
		}
	}
}

func TestSprintAtRiskThresholdsConfig(t *testing.T) {
	defaults := DefaultConfig().AtRiskThresholds()
	if defaults.BlockedDays != 2 || defaults.InactiveDays != 4 {
		t.Fatalf("unexpected default AtRiskThresholds: %+v", defaults)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	customYAML := "sprint_blocked_days: 5\nsprint_inactive_days: 9\n"
	if err := os.WriteFile(ConfigPath(dir), []byte(customYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	th := cfg.AtRiskThresholds()
	if th.BlockedDays != 5 || th.InactiveDays != 9 {
		t.Fatalf("custom AtRiskThresholds = %+v, want {BlockedDays:5 InactiveDays:9}", th)
	}

	negBlocked := DefaultConfig()
	negBlocked.SprintBlockedDays = -1
	if err := negBlocked.Validate(); err == nil {
		t.Fatal("expected validation error for negative sprint_blocked_days")
	}
	negInactive := DefaultConfig()
	negInactive.SprintInactiveDays = -2
	if err := negInactive.Validate(); err == nil {
		t.Fatal("expected validation error for negative sprint_inactive_days")
	}
}

func TestCalculatorCandidateScopingExcludesBoundaryBlockers(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)
	closedPrior := now.AddDate(0, 0, -10)
	closedRecent := now.AddDate(0, 0, -2)

	issues := []model.Issue{
		// In-scope candidate: fresh, open, unique title
		{ID: "CAND-1", Title: "Implement vector index compaction", Status: model.StatusOpen, UpdatedAt: now.Add(-time.Hour)},
		// Out-of-scope boundary issues that would otherwise trigger stale_issue, abandoned_claim, potential_duplicate, and velocity_drop
		{ID: "EXT-STALE", Title: "Migrate legacy auth middleware", Status: model.StatusOpen, UpdatedAt: old},
		{ID: "EXT-CLAIMED", Title: "Migrate legacy auth middleware", Status: model.StatusInProgress, Assignee: "agent-ext", UpdatedAt: old},
	}
	for i := 0; i < 6; i++ {
		issues = append(issues, model.Issue{
			ID:        fmt.Sprintf("EXT-CLOSED-%d", i),
			Title:     fmt.Sprintf("External closed %d", i),
			Status:    model.StatusClosed,
			UpdatedAt: now,
			ClosedAt:  &closedPrior,
		})
	}
	issues = append(issues, model.Issue{
		ID:        "EXT-RECENT",
		Title:     "External recent close",
		Status:    model.StatusClosed,
		UpdatedAt: now,
		ClosedAt:  &closedRecent,
	})

	a := analysis.NewAnalyzer(issues)
	a.SetNow(now)
	a.SetReadinessScope(model.NewReadinessIndex(issues), map[string]bool{"CAND-1": true})

	quiet := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: len(issues), OpenCount: 3}}
	calc := NewCalculator(quiet, quiet, nil)
	calc.SetNow(now)
	calc.SetIssues(issues)
	if !calc.ReuseAnalyzer(a) {
		t.Fatal("expected ReuseAnalyzer to accept matching scoped analyzer")
	}

	result := calc.Calculate()
	for _, alert := range result.Alerts {
		switch alert.Type {
		case AlertStaleIssue, AlertAbandonedClaim, AlertPotentialDuplicate, AlertVelocityDrop:
			t.Fatalf("out-of-scope boundary issues leaked into %s alert: %+v", alert.Type, alert)
		}
	}
}

func TestCalculatorTopMetricsCentralityDrift(t *testing.T) {
	bl := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank:     []baseline.MetricItem{{ID: "A", Value: 0.40}},
			Betweenness:  []baseline.MetricItem{{ID: "B", Value: 0.20}},
			CriticalPath: []baseline.MetricItem{{ID: "C", Value: 5.0}},
			Hubs:         []baseline.MetricItem{{ID: "D", Value: 0.50}},
			Authorities:  []baseline.MetricItem{{ID: "E", Value: 0.50}},
		},
	}
	cur := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank:     []baseline.MetricItem{{ID: "A", Value: 0.40}}, // unchanged
			Betweenness:  []baseline.MetricItem{{ID: "B", Value: 0.50}}, // +150%
			CriticalPath: []baseline.MetricItem{{ID: "C2", Value: 8.0}}, // C dropped, C2 entered
			Hubs:         []baseline.MetricItem{{ID: "D", Value: 0.50}},
			Authorities:  []baseline.MetricItem{{ID: "E", Value: 0.10}}, // -80%
		},
	}

	calc := NewCalculator(bl, cur, nil)
	result := calc.Calculate()
	got := alertsOfType(result, AlertPageRankChange)
	if len(got) != 1 {
		t.Fatalf("expected 1 AlertPageRankChange from non-PageRank TopMetrics drift, got %d: %+v", len(got), result.Alerts)
	}
	joined := strings.Join(got[0].Details, " | ")
	for _, want := range []string{"Betweenness: B: 150.0% change", "CriticalPath: C dropped from top", "CriticalPath: C2 entered top", "Authorities: E: -80.0% change"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected detail %q in %q", want, joined)
		}
	}
}

func TestCalculatorLegacyBaselineAndCandidateCycles(t *testing.T) {
	// 1. Pre-existing legacy .bv/baseline.json only populated TopMetrics.PageRank.
	// Comparing against a current snapshot that populates Betweenness/CriticalPath/Hubs/Authorities
	// must NOT emit false "entered top" centrality warnings!
	legacyBL := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{{ID: "A", Value: 0.40}},
		},
	}
	curFull := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{
			PageRank:     []baseline.MetricItem{{ID: "A", Value: 0.40}},
			Betweenness:  []baseline.MetricItem{{ID: "B", Value: 0.50}},
			CriticalPath: []baseline.MetricItem{{ID: "C", Value: 5.0}},
			Hubs:         []baseline.MetricItem{{ID: "D", Value: 0.50}},
			Authorities:  []baseline.MetricItem{{ID: "E", Value: 0.50}},
		},
	}
	resLegacy := NewCalculator(legacyBL, curFull, nil).Calculate()
	if got := alertsOfType(resLegacy, AlertPageRankChange); len(got) != 0 {
		t.Fatalf("legacy baseline triggered false AlertPageRankChange warnings: %+v", got)
	}

	// 2. Phase 2 skipped in current snapshot (empty current TopMetrics on non-empty graph)
	// must NOT emit false "dropped from top" warnings!
	curSkippedPhase2 := &baseline.Baseline{
		Stats:      baseline.GraphStats{NodeCount: 10},
		TopMetrics: baseline.TopMetrics{},
	}
	resSkipped := NewCalculator(curFull, curSkippedPhase2, nil).Calculate()
	if got := alertsOfType(resSkipped, AlertPageRankChange); len(got) != 0 {
		t.Fatalf("Phase-2-skipped current snapshot triggered false AlertPageRankChange warnings: %+v", got)
	}

	// 3. Candidate-scoped checkCycles must ignore purely out-of-scope cycles while catching in-scope cycles.
	blCyc := &baseline.Baseline{
		Stats: baseline.GraphStats{NodeCount: 4},
	}
	curOutOnly := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 4},
		Cycles: [][]string{{"EXT-1", "EXT-2"}},
	}
	calcOut := NewCalculator(blCyc, curOutOnly, nil)
	calcOut.SetCandidates(map[string]bool{"CAND-1": true, "CAND-2": true})
	if got := alertsOfType(calcOut.Calculate(), AlertNewCycle); len(got) != 0 {
		t.Fatalf("out-of-scope cycle leaked into candidate-scoped drift alerts: %+v", got)
	}

	curInAndOut := &baseline.Baseline{
		Stats:  baseline.GraphStats{NodeCount: 4},
		Cycles: [][]string{{"EXT-1", "EXT-2"}, {"CAND-1", "EXT-1"}},
	}
	calcIn := NewCalculator(blCyc, curInAndOut, nil)
	calcIn.SetCandidates(map[string]bool{"CAND-1": true, "CAND-2": true})
	gotCyc := alertsOfType(calcIn.Calculate(), AlertNewCycle)
	if len(gotCyc) != 1 || gotCyc[0].Delta != 1 || gotCyc[0].CurrentVal != 1 {
		t.Fatalf("expected exactly 1 in-scope cycle alert (Delta=1 CurrentVal=1), got %+v", gotCyc)
	}
}

func TestSourceInconsistencyAlerts_CandidateScopingCapAndFallback(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	quiet := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 3, OpenCount: 3}}

	diffs := []datasource.SourceDiff{
		datasource.DetectInconsistencies(
			[]model.Issue{
				{ID: "EXT-1", Title: "Out of scope A", Status: model.StatusOpen, Priority: 1},
				{ID: "CAND-1", Title: "SQLite title", Status: model.StatusOpen, Priority: 1},
				{ID: "CAND-2", Title: "Same", Status: model.StatusOpen, Priority: 0},
			},
			[]model.Issue{
				{ID: "EXT-1", Title: "Out of scope B", Status: model.StatusClosed, Priority: 3},
				{ID: "CAND-1", Title: "JSONL title", Status: model.StatusClosed, Priority: 2},
				{ID: "CAND-2", Title: "Same", Status: model.StatusOpen, Priority: 2},
			},
			".beads/beads.db",
			".beads/issues.jsonl",
			datasource.DiffOptions{CompareFields: []string{"status", "priority", "title"}},
		),
	}

	cfg := DefaultConfig()
	cfg.SourceDriftMaxAlerts = 2
	calc := NewCalculator(quiet, quiet, cfg)
	calc.SetNow(now)
	calc.SetIssues([]model.Issue{
		{ID: "CAND-1", Status: model.StatusOpen, Labels: []string{"backend"}, UpdatedAt: now},
		{ID: "CAND-2", Status: model.StatusOpen, Labels: []string{"frontend"}, UpdatedAt: now},
	})
	calc.SetCandidates(map[string]bool{"CAND-1": true, "CAND-2": true})
	calc.SetSourceDiffs(diffs)

	got := alertsOfType(calc.Calculate(), AlertSourceInconsistency)
	if len(got) != 2 {
		t.Fatalf("expected 2 in-scope capped source_inconsistency alerts, got %d: %+v", len(got), got)
	}
	for _, a := range got {
		if a.IssueID != "CAND-1" {
			t.Fatalf("expected sorted in-scope CAND-1 alerts first (EXT-1 filtered out), got %+v", a)
		}
		if len(a.Labels) != 1 || a.Labels[0] != "backend" {
			t.Fatalf("expected issue labels attached to alert, got %+v", a)
		}
	}

	// Single-issue fallback literal
	fallback := SourceInconsistencyAlerts([]datasource.SourceDiff{{
		IssueID:    "SINGLE-1",
		Difference: "missing in .beads/issues.jsonl",
		SourceA:    ".beads/beads.db",
		SourceB:    ".beads/issues.jsonl",
		ValueA:     "open",
	}}, 10, now)
	if len(fallback) != 1 || fallback[0].IssueID != "SINGLE-1" {
		t.Fatalf("expected single-issue fallback alert, got %+v", fallback)
	}
}

func TestStalenessAndDuplicates_SkipParkedAndTemplateBeads(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	staleTime := now.AddDate(0, 0, -45)
	future := now.Add(48 * time.Hour)
	quiet := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 7, OpenCount: 7}}

	issues := []model.Issue{
		{ID: "PARK-DEF", Title: "Implement distributed cache invalidation", Status: model.StatusDeferred, UpdatedAt: staleTime},
		{ID: "PARK-PIN-STATUS", Title: "Implement distributed cache invalidation", Status: model.StatusPinned, UpdatedAt: staleTime},
		{ID: "PARK-PIN-BOOL", Title: "Implement distributed cache invalidation", Status: model.StatusOpen, Pinned: true, UpdatedAt: staleTime},
		{ID: "PARK-TMPL", Title: "Implement distributed cache invalidation", Status: model.StatusOpen, IsTemplate: true, UpdatedAt: staleTime},
		{ID: "PARK-EPH", Title: "Implement distributed cache invalidation", Status: model.StatusOpen, Ephemeral: true, UpdatedAt: staleTime},
		{ID: "PARK-FUTURE", Title: "Implement distributed cache invalidation", Status: model.StatusOpen, DeferUntil: &future, UpdatedAt: staleTime},
		{ID: "LIVE-OPEN", Title: "Implement distributed cache invalidation", Status: model.StatusOpen, UpdatedAt: now},
	}

	calc := NewCalculator(quiet, quiet, nil)
	calc.SetNow(now)
	calc.SetIssues(issues)
	res := calc.Calculate()

	if stale := alertsOfType(res, AlertStaleIssue); len(stale) != 0 {
		t.Fatalf("parked/template/ephemeral beads triggered stale_issue alerts: %+v", stale)
	}
	// Verify PARK-TMPL is excluded from potential_duplicate alerts.
	for _, a := range alertsOfType(res, AlertPotentialDuplicate) {
		if a.IssueID == "PARK-TMPL" || a.RelatedIssueID == "PARK-TMPL" {
			t.Fatalf("IsTemplate bead leaked into potential_duplicate alert: %+v", a)
		}
	}
}

func TestConfigExists(t *testing.T) {
	dir := t.TempDir()
	if ConfigExists(dir) {
		t.Fatal("expected ConfigExists=false for empty dir")
	}

	custom := DefaultConfig()
	custom.StaleWarningDays = 3
	if err := SaveConfig(dir, custom); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if !ConfigExists(dir) {
		t.Fatal("expected ConfigExists(dir)=true after SaveConfig")
	}
	if !ConfigExists(ConfigPath(dir)) {
		t.Fatal("expected ConfigExists(ConfigPath(dir))=true after SaveConfig")
	}

	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.StaleWarningDays != 3 {
		t.Fatalf("expected saved StaleWarningDays=3, got %d", loaded.StaleWarningDays)
	}
}

func TestReuseAnalyzer_PreservesSecondaryOnlyPresenceAlertsAndAllowCandidateIDs(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	quiet := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 1, OpenCount: 1}}

	primaryIssues := []model.Issue{
		{ID: "PRIM-1", Title: "Primary issue", Status: model.StatusOpen, Labels: []string{"backend"}, UpdatedAt: now},
	}
	secondaryIssues := []model.Issue{
		{ID: "PRIM-1", Title: "Primary issue", Status: model.StatusOpen, Labels: []string{"backend"}, UpdatedAt: now},
		{ID: "SEC-BACKEND", Title: "Only in JSONL backend", Status: model.StatusOpen, Labels: []string{"backend"}, UpdatedAt: now},
		{ID: "SEC-FRONTEND", Title: "Only in JSONL frontend", Status: model.StatusOpen, Labels: []string{"frontend"}, UpdatedAt: now},
	}
	diffs := []datasource.SourceDiff{
		datasource.DetectInconsistencies(
			primaryIssues,
			secondaryIssues,
			".beads/beads.db",
			".beads/issues.jsonl",
			datasource.DiffOptions{CompareFields: []string{"status", "priority", "title"}},
		),
	}

	// 1. Unscoped ReuseAnalyzer (!a.HasCandidateScope()) must emit BOTH secondary-only presence alerts!
	unscopedAn := analysis.NewAnalyzer(primaryIssues)
	unscopedAn.SetNow(now)
	calcUnscoped := NewCalculator(quiet, quiet, nil)
	calcUnscoped.SetNow(now)
	calcUnscoped.SetIssues(primaryIssues)
	if !calcUnscoped.ReuseAnalyzer(unscopedAn) {
		t.Fatal("expected ReuseAnalyzer(unscopedAn) to succeed")
	}
	calcUnscoped.SetSourceDiffs(diffs)
	gotUnscoped := alertsOfType(calcUnscoped.Calculate(), AlertSourceInconsistency)
	if len(gotUnscoped) != 2 {
		t.Fatalf("expected 2 secondary-only source_inconsistency alerts with unscoped ReuseAnalyzer, got %d: %+v", len(gotUnscoped), gotUnscoped)
	}
	if len(gotUnscoped[0].Labels) != 1 || gotUnscoped[0].Labels[0] != "backend" {
		t.Fatalf("expected MissingIssues labels attached to secondary-only alert, got %+v", gotUnscoped[0])
	}

	// 2. Scoped ReuseAnalyzer + AllowCandidateIDs: only SEC-BACKEND (matching --label backend) is emitted,
	// while SEC-FRONTEND stays filtered out and the borrowed analyzer cache remains valid!
	scopedAn := analysis.NewAnalyzer(primaryIssues)
	scopedAn.SetReadinessScope(model.NewReadinessIndex(primaryIssues), map[string]bool{"PRIM-1": true})
	scopedAn.SetNow(now)

	calcScoped := NewCalculator(quiet, quiet, nil)
	calcScoped.SetNow(now)
	calcScoped.SetIssues(primaryIssues)
	if !calcScoped.ReuseAnalyzer(scopedAn) {
		t.Fatal("expected ReuseAnalyzer(scopedAn) to succeed")
	}
	calcScoped.AllowCandidateIDs([]string{"SEC-BACKEND"})
	calcScoped.SetSourceDiffs(diffs)

	gotScoped := alertsOfType(calcScoped.Calculate(), AlertSourceInconsistency)
	if len(gotScoped) != 1 || gotScoped[0].IssueID != "SEC-BACKEND" {
		t.Fatalf("expected only SEC-BACKEND alert after AllowCandidateIDs, got %+v", gotScoped)
	}
	if calcScoped.analyzerCache != scopedAn {
		t.Fatal("AllowCandidateIDs for secondary-only ID must not invalidate borrowed analyzer cache")
	}
}

func TestLoadConfig_SourceDriftMaxAlertsZeroUnlimitedOn15DriftedIssues(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	quiet := &baseline.Baseline{Stats: baseline.GraphStats{NodeCount: 15, OpenCount: 15}}

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	yamlBody := "source_drift_alert: true\nsource_drift_max_alerts: 0\n"
	if err := os.WriteFile(ConfigPath(projectDir), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SourceDriftMaxAlerts != 0 {
		t.Fatalf("SourceDriftMaxAlerts = %d, want 0 (unlimited)", cfg.SourceDriftMaxAlerts)
	}

	var issuesA, issuesB []model.Issue
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("DRIFT-%02d", i+1)
		issuesA = append(issuesA, model.Issue{ID: id, Title: "Same", Status: model.StatusOpen, Priority: 1, UpdatedAt: now})
		issuesB = append(issuesB, model.Issue{ID: id, Title: "Same", Status: model.StatusClosed, Priority: 1, UpdatedAt: now})
	}
	diffs := []datasource.SourceDiff{
		datasource.DetectInconsistencies(issuesA, issuesB, ".beads/beads.db", ".beads/issues.jsonl", datasource.DefaultDiffOptions()),
	}

	calc := NewCalculator(quiet, quiet, cfg)
	calc.SetNow(now)
	calc.SetIssues(issuesA)
	calc.SetSourceDiffs(diffs)

	got := alertsOfType(calc.Calculate(), AlertSourceInconsistency)
	if len(got) != 15 {
		t.Fatalf("expected all 15 alerts when source_drift_max_alerts: 0, got %d", len(got))
	}

	// Verify negative SourceDriftMaxAlerts is rejected by Validate().
	negCfg := DefaultConfig()
	negCfg.SourceDriftMaxAlerts = -5
	if err := negCfg.Validate(); err == nil {
		t.Fatalf("expected Validate to reject negative SourceDriftMaxAlerts (-5), got nil")
	}

	// Verify source_drift_alert: false disables AlertSourceInconsistency.
	if err := os.WriteFile(ConfigPath(projectDir), []byte("source_drift_alert: false\nsource_drift_max_alerts: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	disabledCfg, err := LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig disabled: %v", err)
	}
	calcDisabled := NewCalculator(quiet, quiet, disabledCfg)
	calcDisabled.SetNow(now)
	calcDisabled.SetSourceDiffs(diffs)
	if gotDis := alertsOfType(calcDisabled.Calculate(), AlertSourceInconsistency); len(gotDis) != 0 {
		t.Fatalf("expected 0 alerts when source_drift_alert: false, got %+v", gotDis)
	}
}

func TestSourceInconsistencyAlerts_DirectionAwareSuggestedAction(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	sqliteIssues := []model.Issue{
		{ID: "BV-SQLITE-ONLY", Title: "SQLite only", Status: model.StatusOpen, UpdatedAt: t0},
		{ID: "BV-JSONL-NEWER", Title: "Old title", Status: model.StatusOpen, UpdatedAt: t0},
		{ID: "BV-TIE", Title: "Title A", Status: model.StatusOpen, UpdatedAt: t0},
	}
	jsonlIssues := []model.Issue{
		{ID: "BV-JSONL-ONLY", Title: "JSONL only", Status: model.StatusOpen, UpdatedAt: t1},
		{ID: "BV-JSONL-NEWER", Title: "Newer JSONL title", Status: model.StatusClosed, UpdatedAt: t1},
		{ID: "BV-TIE", Title: "Title A", Status: model.StatusClosed, UpdatedAt: t0},
	}

	diff := datasource.DetectInconsistencies(
		sqliteIssues,
		jsonlIssues,
		".beads/beads.db",
		".beads/issues.jsonl",
		datasource.DiffOptions{CompareFields: []string{"status"}},
	)
	alerts := SourceInconsistencyAlerts([]datasource.SourceDiff{diff}, 0, t1)
	byID := make(map[string]Alert, len(alerts))
	for _, a := range alerts {
		byID[a.IssueID] = a
	}

	if got := byID["BV-JSONL-ONLY"].SuggestedAction; !strings.HasPrefix(got, "Run 'br sync --import-only'") {
		t.Fatalf("BV-JSONL-ONLY action should lead with --import-only, got %q", got)
	}
	if got := byID["BV-SQLITE-ONLY"].SuggestedAction; !strings.HasPrefix(got, "Run 'br sync --flush-only'") {
		t.Fatalf("BV-SQLITE-ONLY action should lead with --flush-only, got %q", got)
	}
	if got := byID["BV-JSONL-NEWER"].SuggestedAction; !strings.HasPrefix(got, "Run 'br sync --import-only'") {
		t.Fatalf("BV-JSONL-NEWER action should lead with --import-only when JSONL is newer, got %q", got)
	}
	if got := byID["BV-TIE"].SuggestedAction; !strings.Contains(got, "authoritative") || !strings.Contains(got, "--flush-only") || !strings.Contains(got, "--import-only") {
		t.Fatalf("BV-TIE action should mention checking authoritative source before --flush-only vs --import-only, got %q", got)
	}

	// Presence mismatch where newer JSONL tombstoned/deleted an issue still in stale SQLite
	// must NEVER advise --flush-only (which would resurrect deleted issues over newer JSONL).
	tombDiff := datasource.SourceDiff{
		SourceA: ".beads/beads.db",
		SourceB: ".beads/issues.jsonl",
		IssueDiffs: []datasource.IssueSourceDiff{
			{
				IssueID:    "BV-TOMB-UPSTREAM",
				Field:      "presence",
				SourceA:    ".beads/beads.db",
				SourceB:    ".beads/issues.jsonl",
				ValueA:     "open",
				ValueB:     "",
				Difference: "missing in .beads/issues.jsonl",
				ModTimeA:   t0,
				ModTimeB:   t1, // JSONL is newer than SQLite
			},
			{
				IssueID:    "BV-DEL-SQLITE",
				Field:      "presence",
				SourceA:    ".beads/beads.db",
				SourceB:    ".beads/issues.jsonl",
				ValueA:     "",
				ValueB:     "open",
				Difference: "missing in .beads/beads.db",
				ModTimeA:   t1, // SQLite is newer than JSONL
				ModTimeB:   t0,
			},
		},
	}
	tombAlerts := SourceInconsistencyAlerts([]datasource.SourceDiff{tombDiff}, 0, t1)
	if len(tombAlerts) != 2 {
		t.Fatalf("expected 2 presence alerts, got %d", len(tombAlerts))
	}
	if strings.Contains(tombAlerts[0].SuggestedAction, "--flush-only") || !strings.HasPrefix(tombAlerts[0].SuggestedAction, "Run 'br sync --import-only'") {
		t.Fatalf("newer JSONL deletion must recommend --import-only without --flush-only, got %q", tombAlerts[0].SuggestedAction)
	}
	if !strings.Contains(tombAlerts[1].SuggestedAction, "authoritative") {
		t.Fatalf("newer SQLite deletion vs stale JSONL should advise checking authoritative source, got %q", tombAlerts[1].SuggestedAction)
	}
}

func TestDriftLoadConfigRejectsUnknownYAMLFieldsAndMultiDoc(t *testing.T) {
	writeDriftYAML := func(content string) string {
		t.Helper()
		dir := t.TempDir()
		bvDir := filepath.Join(dir, ".bv")
		if err := os.MkdirAll(bvDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bvDir, "drift.yaml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// Positive observable 1: unknown top-level or nested label_overrides key fails closed.
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown top-level key",
			yaml: "stale_warnig_days: 7\n",
			want: "field stale_warnig_days not found",
		},
		{
			name: "unknown nested label_overrides key",
			yaml: "label_overrides:\n  urgent:\n    stale_warnig_days: 3\n",
			want: "field stale_warnig_days not found",
		},
		{
			name: "multi-document YAML",
			yaml: "stale_warning_days: 10\n---\nstale_warning_days: 5\n",
			want: "multiple YAML documents",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeDriftYAML(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadConfig() error = %v, want %q", err, tc.want)
			}
		})
	}

	// Planted negative controls: comment-only YAML and valid YAML succeed.
	commentDir := writeDriftYAML("# Only comments in .bv/drift.yaml\n\n")
	defCfg, err := LoadConfig(commentDir)
	if err != nil {
		t.Fatalf("comment-only drift.yaml failed: %v", err)
	}
	if defCfg.StaleWarningDays != DefaultConfig().StaleWarningDays {
		t.Fatalf("comment-only StaleWarningDays = %d, want default %d", defCfg.StaleWarningDays, DefaultConfig().StaleWarningDays)
	}

	validDir := writeDriftYAML("# Valid config\nstale_warning_days: 10\nstale_critical_days: 25\n")
	validCfg, err := LoadConfig(validDir)
	if err != nil {
		t.Fatalf("valid drift.yaml failed: %v", err)
	}
	if validCfg.StaleWarningDays != 10 || validCfg.StaleCriticalDays != 25 {
		t.Fatalf("validCfg = (%d, %d), want (10, 25)", validCfg.StaleWarningDays, validCfg.StaleCriticalDays)
	}
}

func TestDriftCandidateScopePriorityMismatchWithheldDuplicatesScopeGateAndVelocityReopen(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	// 1. collectTopMetricChanges candidate filter (positive: in-scope change kept; negative: out-of-scope filtered).
	calcScoped := NewCalculator(&baseline.Baseline{}, &baseline.Baseline{}, DefaultConfig())
	calcScoped.SetCandidates(map[string]bool{"IN-SCOPE": true})
	changes := calcScoped.collectTopMetricChanges(
		[]baseline.MetricItem{{ID: "IN-SCOPE", Value: 0.10}, {ID: "OUT-SCOPE", Value: 0.10}},
		[]baseline.MetricItem{{ID: "IN-SCOPE", Value: 0.40}, {ID: "OUT-SCOPE", Value: 0.90}},
		50,
		"PageRank",
	)
	if len(changes) != 1 || !strings.Contains(changes[0], "IN-SCOPE") || strings.Contains(changes[0], "OUT-SCOPE") {
		t.Fatalf("collectTopMetricChanges candidate filter = %v, want only IN-SCOPE", changes)
	}

	// 2. MatchesScope gating aggregate cross-snapshot deltas:
	// Unscoped baseline vs scoped current must suppress aggregate deltas while Scope==nil vs BaselineScope{} fires them.
	blUnscoped := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Stats: baseline.GraphStats{
			NodeCount:       10,
			EdgeCount:       10,
			Density:         0.10,
			OpenCount:       5,
			BlockedCount:    1,
			ActionableCount: 5,
		},
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{{ID: "A", Value: 0.10}},
		},
	}
	curChangedStats := baseline.GraphStats{
		NodeCount:       30,
		EdgeCount:       90,
		Density:         0.30,
		OpenCount:       20,
		BlockedCount:    12,
		ActionableCount: 1,
	}
	curScoped := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Scope:   &baseline.BaselineScope{Label: "api"},
		Stats:   curChangedStats,
		TopMetrics: baseline.TopMetrics{
			PageRank: []baseline.MetricItem{{ID: "A", Value: 0.80}},
		},
	}
	curUnscoped := &baseline.Baseline{
		Version:    baseline.CurrentVersion,
		Scope:      nil,
		Stats:      curChangedStats,
		TopMetrics: curScoped.TopMetrics,
	}

	aggregateTypes := map[AlertType]bool{
		AlertDensityGrowth:    true,
		AlertNodeCountChange:  true,
		AlertEdgeCountChange:  true,
		AlertBlockedIncrease:  true,
		AlertActionableChange: true,
		AlertScopeCreep:       true,
		AlertPageRankChange:   true,
	}

	mismatchedRes := NewCalculator(blUnscoped, curScoped, DefaultConfig()).Calculate()
	for _, a := range mismatchedRes.Alerts {
		if aggregateTypes[a.Type] {
			t.Fatalf("mismatched scope emitted aggregate drift alert %s: %+v", a.Type, a)
		}
	}

	matchedRes := NewCalculator(blUnscoped, curUnscoped, DefaultConfig()).Calculate()
	seenAgg := make(map[AlertType]bool)
	for _, a := range matchedRes.Alerts {
		if aggregateTypes[a.Type] {
			seenAgg[a.Type] = true
		}
	}
	for wantType := range aggregateTypes {
		if !seenAgg[wantType] {
			t.Fatalf("matching unscoped comparison missed expected aggregate alert %s (got %v)", wantType, matchedRes.Alerts)
		}
	}

	// 3. checkPriorityMismatch SelectionReadyLeaf:
	// READY-LEAF is P4 unblocking 4 urgent P0 tasks (positive observable), whereas blocked, deferred, pinned,
	// ephemeral, template, and epic-with-open-child are all withheld by SelectionReadyLeaf (planted negative controls).
	futureDefer := now.Add(48 * time.Hour)
	mismatchIssues := []model.Issue{
		{ID: "READY-LEAF", Title: "Load-bearing leaf", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{ID: "NEG-BLOCKER", Title: "Upstream blocker", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 0, CreatedAt: now, UpdatedAt: now},
		{
			ID: "NEG-BLOCKED", Title: "Blocked P4", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "NEG-BLOCKED", DependsOnID: "NEG-BLOCKER", Type: model.DepBlocks}},
		},
		{ID: "NEG-DEFERRED", Title: "Deferred P4", Status: model.StatusOpen, DeferUntil: &futureDefer, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{ID: "NEG-PINNED", Title: "Pinned P4", Status: model.StatusPinned, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{ID: "NEG-EPHEMERAL", Title: "Ephemeral P4", Status: model.StatusOpen, Ephemeral: true, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{ID: "NEG-TEMPLATE", Title: "Template P4", Status: model.StatusOpen, IsTemplate: true, IssueType: model.TypeTask, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{ID: "NEG-EPIC", Title: "Epic parent P4", Status: model.StatusOpen, IssueType: model.TypeEpic, Priority: 4, CreatedAt: now, UpdatedAt: now},
		{
			ID: "EPIC-CHILD", Title: "Open child of epic", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 0, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "EPIC-CHILD", DependsOnID: "NEG-EPIC", Type: model.DepParentChild}},
		},
	}
	// Give every P4 candidate (READY-LEAF and all negative controls) identical downstream fan-out so any failure of SelectionReadyLeaf would fire!
	for idx, src := range []string{"READY-LEAF", "NEG-BLOCKED", "NEG-DEFERRED", "NEG-PINNED", "NEG-EPHEMERAL", "NEG-TEMPLATE", "NEG-EPIC"} {
		for k := 0; k < 4; k++ {
			depID := fmt.Sprintf("FAN-%d-%d", idx, k)
			mismatchIssues = append(mismatchIssues, model.Issue{
				ID: depID, Title: depID, Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 0, CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: depID, DependsOnID: src, Type: model.DepBlocks}},
			})
		}
	}
	pmCfg := DefaultConfig()
	pmCfg.PriorityMismatchMinConfidence = 0.30
	eqBl := &baseline.Baseline{Version: baseline.CurrentVersion, Stats: baseline.GraphStats{NodeCount: len(mismatchIssues), OpenCount: len(mismatchIssues)}}
	pmCalc := NewCalculator(eqBl, eqBl, pmCfg)
	pmCalc.SetNow(now)
	pmCalc.SetIssues(mismatchIssues)
	pmRes := pmCalc.Calculate()
	pmIDs := make(map[string]bool)
	for _, a := range pmRes.Alerts {
		if a.Type == AlertPriorityMismatch {
			pmIDs[a.IssueID] = true
		}
	}
	if !pmIDs["READY-LEAF"] {
		t.Fatalf("expected AlertPriorityMismatch for READY-LEAF, got %v", pmIDs)
	}
	for _, negID := range []string{"NEG-BLOCKED", "NEG-DEFERRED", "NEG-PINNED", "NEG-EPHEMERAL", "NEG-TEMPLATE", "NEG-EPIC"} {
		if pmIDs[negID] {
			t.Fatalf("non-ready-leaf %q leaked into AlertPriorityMismatch: %v", negID, pmIDs)
		}
	}

	// 4. checkPotentialDuplicate withheld skip (including case-insensitive Status "PINNED"):
	dupText := "Implement OAuth2 PKCE authentication callback token exchange validation handler"
	dupIssues := []model.Issue{
		{ID: "DUP-LIVE-1", Title: dupText, Description: dupText, Status: model.StatusOpen},
		{ID: "DUP-LIVE-2", Title: dupText, Description: dupText, Status: model.StatusOpen},
		{ID: "DUP-PIN-UPPER", Title: dupText, Description: dupText, Status: " PINNED "},
		{ID: "DUP-PIN-BOOL", Title: dupText, Description: dupText, Status: model.StatusOpen, Pinned: true},
		{ID: "DUP-EPH", Title: dupText, Description: dupText, Status: model.StatusOpen, Ephemeral: true},
		{ID: "DUP-TPL", Title: dupText, Description: dupText, Status: model.StatusOpen, IsTemplate: true},
		{ID: "DUP-CLOSED", Title: dupText, Description: dupText, Status: model.StatusClosed},
	}
	dupCfg := DefaultConfig()
	dupCfg.DuplicateJaccardThreshold = 0.5
	dupCfg.DuplicateMaxAlerts = 20
	dupCalc := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, dupCfg)
	dupCalc.SetNow(now)
	dupCalc.SetIssues(dupIssues)
	dupRes := dupCalc.Calculate()
	dupCount := 0
	for _, a := range dupRes.Alerts {
		if a.Type != AlertPotentialDuplicate {
			continue
		}
		dupCount++
		if (a.IssueID != "DUP-LIVE-1" && a.IssueID != "DUP-LIVE-2") || (a.RelatedIssueID != "DUP-LIVE-1" && a.RelatedIssueID != "DUP-LIVE-2") {
			t.Fatalf("withheld/closed duplicate leaked into AlertPotentialDuplicate: %+v", a)
		}
	}
	if dupCount != 1 {
		t.Fatalf("expected exactly 1 live duplicate alert (DUP-LIVE-1 <-> DUP-LIVE-2), got %d", dupCount)
	}

	// 5. checkVelocityDrop reopened negative control + ClosedAt==nil UpdatedAt fallback:
	priorClose := now.Add(-10 * 24 * time.Hour) // Prior 7d window [-14d, -7d]
	recentClose := now.Add(-2 * 24 * time.Hour) // Recent 7d window (-7d, 0]
	futureClose := now.Add(24 * time.Hour)      // Future (> now), must be ignored
	velIssues := []model.Issue{
		// 5 genuinely closed issues in prior window (>= DefaultConfig().VelocityMinBaseline=5, including ClosedAt==nil fallback to UpdatedAt):
		{ID: "PRIOR-1", Status: model.StatusClosed, ClosedAt: &priorClose, UpdatedAt: priorClose},
		{ID: "PRIOR-2", Status: model.StatusClosed, ClosedAt: &priorClose, UpdatedAt: priorClose},
		{ID: "PRIOR-3", Status: model.StatusClosed, ClosedAt: nil, UpdatedAt: priorClose},
		{ID: "PRIOR-4", Status: model.StatusClosed, ClosedAt: nil, UpdatedAt: priorClose},
		{ID: "PRIOR-5", Status: model.StatusTombstone, ClosedAt: nil, UpdatedAt: priorClose},
		// Planted negative controls in recent window: reopened StatusOpen/StatusInProgress with stale ClosedAt, plus future ClosedAt:
		{ID: "REOPEN-1", Status: model.StatusOpen, ClosedAt: &recentClose, UpdatedAt: recentClose},
		{ID: "REOPEN-2", Status: model.StatusInProgress, ClosedAt: &recentClose, UpdatedAt: recentClose},
		{ID: "REOPEN-3", Status: model.StatusOpen, ClosedAt: &recentClose, UpdatedAt: recentClose},
		{ID: "REOPEN-4", Status: model.StatusOpen, ClosedAt: &recentClose, UpdatedAt: recentClose},
		{ID: "REOPEN-5", Status: model.StatusOpen, ClosedAt: &recentClose, UpdatedAt: recentClose},
		{ID: "FUTURE-CLOSED", Status: model.StatusClosed, ClosedAt: &futureClose, UpdatedAt: futureClose},
	}
	velCalc := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, DefaultConfig())
	velCalc.SetNow(now)
	velCalc.SetIssues(velIssues)
	velRes := velCalc.Calculate()
	foundDrop := false
	for _, a := range velRes.Alerts {
		if a.Type == AlertVelocityDrop {
			foundDrop = true
			if a.BaselineVal != 5 || a.CurrentVal != 0 {
				t.Fatalf("AlertVelocityDrop BaselineVal=%v CurrentVal=%v, want 5 -> 0 (reopened & future excluded)", a.BaselineVal, a.CurrentVal)
			}
		}
	}
	if !foundDrop {
		t.Fatalf("expected AlertVelocityDrop when recent window only has reopened open issues with stale ClosedAt, got %+v", velRes.Alerts)
	}
	// Positive counterpart: when those 5 recent issues remain genuinely closed, no velocity_drop alert fires.
	for i := range velIssues {
		if strings.HasPrefix(velIssues[i].ID, "REOPEN-") {
			velIssues[i].Status = model.StatusClosed
		}
	}
	noDropCalc := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, DefaultConfig())
	noDropCalc.SetNow(now)
	noDropCalc.SetIssues(velIssues)
	for _, a := range noDropCalc.Calculate().Alerts {
		if a.Type == AlertVelocityDrop {
			t.Fatalf("unexpected AlertVelocityDrop when recent window has 5 genuinely closed issues: %+v", a)
		}
	}
}

func TestCheckCycles_GatedOnMatchesScope(t *testing.T) {
	// Planted negative control: baseline captured under --label frontend (Cycles: nil)
	// compared against an unscoped current snapshot containing preexisting backend cycle be-1 <-> be-2
	// must NOT emit a false CRITICAL AlertNewCycle.
	blFrontend := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Scope:   &baseline.BaselineScope{Label: "frontend"},
		Cycles:  nil,
	}
	curUnscopedWithCycle := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Scope:   &baseline.BaselineScope{},
		Stats:   baseline.GraphStats{CycleCount: 1},
		Cycles:  [][]string{{"be-1", "be-2", "be-1"}},
	}
	negRes := NewCalculator(blFrontend, curUnscopedWithCycle, DefaultConfig()).Calculate()
	for _, a := range negRes.Alerts {
		if a.Type == AlertNewCycle {
			t.Fatalf("mismatched scope falsely emitted AlertNewCycle: %+v", a)
		}
	}

	// Positive observable: when scopes match, a newly introduced cycle DOES trigger AlertNewCycle (critical).
	blUnscoped := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Scope:   nil,
		Cycles:  nil,
	}
	posRes := NewCalculator(blUnscoped, curUnscopedWithCycle, DefaultConfig()).Calculate()
	foundCycle := false
	for _, a := range posRes.Alerts {
		if a.Type == AlertNewCycle && a.Severity == SeverityCritical {
			foundCycle = true
		}
	}
	if !foundCycle {
		t.Fatalf("expected critical AlertNewCycle when sameScope == true, got %+v", posRes.Alerts)
	}
}

func TestRC36_DriftP0ConfigDuplicateLabelsAndInfoSuggestedActions(t *testing.T) {
	// 1. Explicit high_impact_priority_max: 0 (P0-only) and duplicate_max_alerts: 0 (unlimited) preserved in LoadConfig.
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	yamlCfg := "high_impact_priority_max: 0\nduplicate_max_alerts: 0\n"
	if err := os.WriteFile(ConfigPath(projectDir), []byte(yamlCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if loaded.HighImpactPriorityMax != 0 {
		t.Fatalf("HighImpactPriorityMax = %d, want 0 (explicit P0-only overwritten)", loaded.HighImpactPriorityMax)
	}
	if loaded.DuplicateMaxAlerts != 0 {
		t.Fatalf("DuplicateMaxAlerts = %d, want 0 (explicit unlimited overwritten)", loaded.DuplicateMaxAlerts)
	}

	// Negative source_drift_max_alerts must return a validation error instead of silently resetting.
	negDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(negDir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(negDir), []byte("source_drift_max_alerts: -1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(negDir); err == nil {
		t.Fatalf("expected LoadConfig to reject negative source_drift_max_alerts: -1")
	}

	// duplicate_max_alerts: 0 (unlimited) must emit all 28 pairs for 8 identical-title open issues (not capped at 20).
	dupNow := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eightDups := make([]model.Issue, 8)
	for i := range eightDups {
		eightDups[i] = model.Issue{
			ID:        fmt.Sprintf("DUP-%02d", i+1),
			Title:     "Identical OAuth callback token validation regression",
			Status:    model.StatusOpen,
			Priority:  2,
			Labels:    []string{"auth"},
			UpdatedAt: dupNow,
		}
	}
	calcUnlim := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, loaded)
	calcUnlim.SetNow(dupNow)
	calcUnlim.SetIssues(eightDups)
	if got := alertsOfType(calcUnlim.Calculate(), AlertPotentialDuplicate); len(got) != 28 {
		t.Fatalf("duplicate_max_alerts: 0 emitted %d AlertPotentialDuplicate alerts for 8 identical issues, want 28 (unlimited)", len(got))
	}

	// Verify P0-only config fires AlertHighImpactUnblock for P0 dependent and ignores P1-only dependents.
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	issuesP1Only := []model.Issue{
		{ID: "ROOT-P1", Title: "Root", Status: model.StatusOpen, Priority: 2, UpdatedAt: now},
		{ID: "D1", Title: "D1", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "D1", DependsOnID: "ROOT-P1", Type: model.DepBlocks}}},
		{ID: "D2", Title: "D2", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "D2", DependsOnID: "ROOT-P1", Type: model.DepBlocks}}},
		{ID: "D3", Title: "D3", Status: model.StatusOpen, Priority: 1, UpdatedAt: now, Dependencies: []*model.Dependency{{IssueID: "D3", DependsOnID: "ROOT-P1", Type: model.DepBlocks}}},
	}
	calcP1 := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, loaded)
	calcP1.SetNow(now)
	calcP1.SetIssues(issuesP1Only)
	if got := alertsOfType(calcP1.Calculate(), AlertHighImpactUnblock); len(got) != 0 {
		t.Fatalf("P0-only config should ignore P1-only unblock cascade, got %+v", got)
	}
	issuesP1Only[1].Priority = 0
	calcP0 := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, loaded)
	calcP0.SetNow(now)
	calcP0.SetIssues(issuesP1Only)
	if got := alertsOfType(calcP0.Calculate(), AlertHighImpactUnblock); len(got) != 1 {
		t.Fatalf("P0-only config should fire 1 AlertHighImpactUnblock when a P0 is unblocked, got %+v", got)
	}

	// 2. AlertPotentialDuplicate populates deduplicated sorted Labels union across both issues (for --alert-label).
	dupIssues := []model.Issue{
		{ID: "DUP-A", Title: "Fix OAuth token refresh race condition in mobile client", Status: model.StatusOpen, Labels: []string{"auth", "mobile"}, UpdatedAt: now},
		{ID: "DUP-B", Title: "Fix OAuth token refresh race condition in mobile client", Status: model.StatusOpen, Labels: []string{"mobile", "security"}, UpdatedAt: now},
	}
	dupCalc := NewCalculator(&baseline.Baseline{Version: 1}, &baseline.Baseline{Version: 1}, loaded)
	dupCalc.SetNow(now)
	dupCalc.SetIssues(dupIssues)
	dupAlerts := alertsOfType(dupCalc.Calculate(), AlertPotentialDuplicate)
	if len(dupAlerts) != 1 {
		t.Fatalf("expected 1 AlertPotentialDuplicate, got %d", len(dupAlerts))
	}
	wantLabels := []string{"auth", "mobile", "security"}
	if strings.Join(dupAlerts[0].Labels, ",") != strings.Join(wantLabels, ",") {
		t.Fatalf("AlertPotentialDuplicate.Labels = %v, want %v", dupAlerts[0].Labels, wantLabels)
	}
	if dupAlerts[0].SuggestedAction == "" {
		t.Fatalf("AlertPotentialDuplicate missing SuggestedAction")
	}

	// 3. Info-level AlertDensityGrowth and AlertActionableChange emit non-empty SuggestedAction.
	bl := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Stats: baseline.GraphStats{
			NodeCount:       10,
			EdgeCount:       10,
			Density:         0.10,
			ActionableCount: 10,
		},
	}
	cur := &baseline.Baseline{
		Version: baseline.CurrentVersion,
		Stats: baseline.GraphStats{
			NodeCount:       10,
			EdgeCount:       13,
			Density:         0.125, // +25% >= DensityInfoPct(20) and < DensityWarningPct(50)
			ActionableCount: 13,    // +30% >= ActionableIncreaseInfoPct(20)
		},
	}
	infoRes := NewCalculator(bl, cur, DefaultConfig()).Calculate()
	seenDensityInfo := false
	seenActionInfo := false
	for _, a := range infoRes.Alerts {
		if a.SuggestedAction == "" {
			t.Fatalf("alert %s (%s) emitted empty SuggestedAction", a.Type, a.Severity)
		}
		if a.Type == AlertDensityGrowth && a.Severity == SeverityInfo {
			seenDensityInfo = true
		}
		if a.Type == AlertActionableChange && a.Severity == SeverityInfo {
			seenActionInfo = true
		}
	}
	if !seenDensityInfo || !seenActionInfo {
		t.Fatalf("expected both Info density_growth (%v) and Info actionable_change (%v), got %+v", seenDensityInfo, seenActionInfo, infoRes.Alerts)
	}
}

func TestWave22_SaveConfigPreservesExplicitZerosFromDefaultConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.HighImpactPriorityMax = 0
	cfg.DuplicateMaxAlerts = 0
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if cfg.HighImpactPriorityMax != 0 || cfg.DuplicateMaxAlerts != 0 {
		t.Fatalf("SaveConfig mutated explicit zeros: high=%d dup=%d", cfg.HighImpactPriorityMax, cfg.DuplicateMaxAlerts)
	}
	reloaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reloaded.HighImpactPriorityMax != 0 || reloaded.DuplicateMaxAlerts != 0 {
		t.Fatalf("reloaded explicit zeros lost: high=%d dup=%d", reloaded.HighImpactPriorityMax, reloaded.DuplicateMaxAlerts)
	}
}
