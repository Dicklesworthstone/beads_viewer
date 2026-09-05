package main_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// These are local JSONL CLI fixtures, not live tracker or mutation proofs.
// SAFE always supplies a selected, ready three-successor cascade. GATE differs
// only in source authority, candidate eligibility, or its deferral boundary.
func TestRobotAlerts_PreservesReadinessAuthorityScopeAndClock(t *testing.T) {
	bv := buildBvBinary(t)
	binary, err := os.ReadFile(bv)
	if err != nil {
		t.Fatalf("read test binary identity: %v", err)
	}
	t.Logf("actual CLI binary=%s sha256=%x", bv, sha256.Sum256(binary))

	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	deferUntil := now.Add(time.Hour)
	for _, tc := range []struct {
		name          string
		predecessor   model.Status
		missing       bool
		contextOnly   bool
		deferred      bool
		atBoundary    bool
		repoScope     bool
		wantGate      bool
		wantTombstone int
	}{
		{name: "hidden-tombstone", predecessor: model.StatusTombstone, wantGate: true, wantTombstone: 1},
		{name: "closed-external-repo", predecessor: model.StatusClosed, repoScope: true, wantGate: true},
		{name: "known-closed-label-context", predecessor: model.StatusClosed, wantGate: true},
		{name: "missing-predecessor", missing: true},
		{name: "excluded-candidate-context", contextOnly: true},
		{name: "future-deferral", deferred: true},
		{name: "deferral-boundary", deferred: true, atBoundary: true, wantGate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixtureDir := t.TempDir()
			gate := model.Issue{
				ID: "api-gate", Title: "Gate for dependent integration", Status: model.StatusOpen,
				Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"},
				CreatedAt: now, UpdatedAt: now,
			}
			if tc.contextOnly {
				// Its backend successors retain GATE as graph context. GATE itself
				// is not selected by --label backend and cannot be recommended.
				gate.Labels = []string{"frontend"}
			}
			if tc.predecessor != "" || tc.missing {
				gate.Dependencies = []*model.Dependency{{
					IssueID: gate.ID, DependsOnID: "web-done", Type: model.DepBlocks,
				}}
			}
			if tc.deferred {
				gate.DeferUntil = &deferUntil
			}
			issues := []model.Issue{gate, {
				ID: "api-safe", Title: "Independent selected work", Status: model.StatusOpen,
				Priority: 1, IssueType: model.TypeTask, Labels: []string{"backend"},
				CreatedAt: now, UpdatedAt: now,
			}}
			for _, parent := range []string{"gate", "safe"} {
				for i, name := range []string{"third", "first", "second"} {
					id := "api-" + parent + "-" + name
					issues = append(issues, model.Issue{
						ID: id, Title: fmt.Sprintf("%s downstream %s", parent, name), Status: model.StatusOpen,
						Priority: i, IssueType: model.TypeTask, Labels: []string{"backend"},
						CreatedAt: now, UpdatedAt: now,
						Dependencies: []*model.Dependency{{IssueID: id, DependsOnID: "api-" + parent, Type: model.DepBlocks}},
					})
				}
			}
			if tc.predecessor != "" {
				issues = append(issues, model.Issue{
					ID: "web-done", Title: "Completed external predecessor", Status: tc.predecessor,
					Priority: 2, IssueType: model.TypeTask, Labels: []string{"frontend"},
					CreatedAt: now, UpdatedAt: now, ClosedAt: &now,
				})
			}
			var fixture bytes.Buffer
			for _, issue := range issues {
				if err := json.NewEncoder(&fixture).Encode(issue); err != nil {
					t.Fatalf("encode local JSONL fixture: %v", err)
				}
			}
			writeBeads(t, fixtureDir, fixture.String())

			clock := now
			if tc.atBoundary {
				clock = deferUntil
			}
			args := []string{"--robot-alerts", "--label", "backend"}
			if tc.repoScope {
				args = []string{"--robot-alerts", "--repo", "api"}
			}
			cmd := exec.Command(bv, args...)
			cmd.Dir = fixtureDir
			cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH="+strconv.FormatInt(clock.Unix(), 10), "BV_NO_CACHE=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			t.Logf("local fixture sha256=%x\n%s\nargv=%q SOURCE_DATE_EPOCH=%d exit=%v\nstdout=%s\nstderr=%s",
				sha256.Sum256(fixture.Bytes()), fixture.String(), cmd.Args, clock.Unix(), err, stdout.String(), stderr.String())
			if err != nil {
				t.Fatalf("robot-alerts failed: %v", err)
			}
			var output struct {
				GeneratedAt   time.Time     `json:"generated_at"`
				Alerts        []drift.Alert `json:"alerts"`
				SkippedChecks []any         `json:"skipped_checks"`
				Scope         struct {
					Label string `json:"label"`
					Repo  string `json:"repo"`
				} `json:"scope"`
				SourceAuthority struct {
					State      string `json:"state"`
					Valid      int    `json:"valid"`
					Tombstones int    `json:"tombstones"`
				} `json:"source_authority"`
				Summary struct {
					Total int `json:"total"`
				} `json:"summary"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode robot-alerts: %v", err)
			}
			if !output.GeneratedAt.Equal(clock) {
				t.Errorf("envelope clock=%s, want %s", output.GeneratedAt, clock)
			}
			if tc.repoScope {
				if output.Scope.Repo != "api" || output.Scope.Label != "" {
					t.Errorf("scope=%+v, want only repo api", output.Scope)
				}
			} else if output.Scope.Label != "backend" || output.Scope.Repo != "" {
				t.Errorf("scope=%+v, want only label backend", output.Scope)
			}
			if output.SourceAuthority.State != "complete" || output.SourceAuthority.Valid != len(issues) || output.SourceAuthority.Tombstones != tc.wantTombstone {
				t.Errorf("source authority=%+v, want complete valid=%d tombstones=%d", output.SourceAuthority, len(issues), tc.wantTombstone)
			}
			if len(output.SkippedChecks) != 0 {
				t.Errorf("tiny fixture skipped alert checks: %+v", output.SkippedChecks)
			}
			if output.Summary.Total != len(output.Alerts) {
				t.Errorf("summary total=%d, want %d", output.Summary.Total, len(output.Alerts))
			}
			var cascadeIDs, highImpactIDs []string
			for _, alert := range output.Alerts {
				if !alert.DetectedAt.Equal(clock) {
					t.Errorf("alert uses different clock: %+v; want %s", alert, clock)
				}
				if alert.Type == drift.AlertStaleIssue {
					t.Errorf("fresh fixture produced stale issue at captured clock: %+v", alert)
				}
				if alert.Type == drift.AlertHighImpactUnblock {
					highImpactIDs = append(highImpactIDs, alert.IssueID)
					wantUrgent := []string{alert.IssueID + "-first", alert.IssueID + "-third"}
					if alert.UnblocksCount != 3 || !slices.Equal(alert.Details, wantUrgent) || alert.SuggestedAction == "" {
						t.Errorf("high-impact alert=%+v, want three unblocks, urgent details%v and suggested action", alert, wantUrgent)
					}
				}
				if alert.Type != drift.AlertBlockingCascade {
					continue
				}
				cascadeIDs = append(cascadeIDs, alert.IssueID)
				wantDetails := []string{alert.IssueID + "-first", alert.IssueID + "-second", alert.IssueID + "-third"}
				if alert.UnblocksCount != 3 || alert.DownstreamPrioritySum != 3 || !slices.Equal(alert.Details, wantDetails) {
					t.Errorf("cascade=%+v, want count3/prioritysum3 and details%v", alert, wantDetails)
				}
				if alert.SuggestedAction == "" {
					t.Errorf("ready cascade has no suggested action: %+v", alert)
				}
			}
			slices.Sort(cascadeIDs)
			wantIDs := []string{"api-safe"}
			if tc.wantGate {
				wantIDs = []string{"api-gate", "api-safe"}
			}
			if !slices.Equal(cascadeIDs, wantIDs) {
				t.Errorf("blocking cascade IDs=%v, want %v", cascadeIDs, wantIDs)
			}
			slices.Sort(highImpactIDs)
			if !slices.Equal(highImpactIDs, wantIDs) {
				t.Errorf("high-impact unblock IDs=%v, want %v", highImpactIDs, wantIDs)
			}
		})
	}
}

func TestRobotAlerts_BasicAndFilters(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	now := time.Now().UTC()
	staleUpdated := now.AddDate(0, 0, -20).Format(time.RFC3339) // warning (default 14d)
	staleCreated := now.AddDate(0, 0, -25).Format(time.RFC3339) // keep valid ordering
	tombstoneUpdated := now.AddDate(0, 0, -20).Format(time.RFC3339)
	tombstoneCreated := now.AddDate(0, 0, -25).Format(time.RFC3339)
	freshTime := now.AddDate(0, 0, -1).Format(time.RFC3339)

	// ROOT unblocks 3 issues => blocking_cascade (info); STALE triggers stale_issue (warning).
	writeBeads(t, env, fmt.Sprintf(
		`{"id":"ROOT","title":"Root","status":"open","priority":1,"issue_type":"task","created_at":"%s","updated_at":"%s"}
{"id":"D1","title":"Dep1","status":"open","priority":2,"issue_type":"task","created_at":"%s","updated_at":"%s","dependencies":[{"issue_id":"D1","depends_on_id":"ROOT","type":"blocks"}]}
{"id":"D2","title":"Dep2","status":"open","priority":2,"issue_type":"task","created_at":"%s","updated_at":"%s","dependencies":[{"issue_id":"D2","depends_on_id":"ROOT","type":"blocks"}]}
{"id":"D3","title":"Dep3","status":"open","priority":2,"issue_type":"task","created_at":"%s","updated_at":"%s","dependencies":[{"issue_id":"D3","depends_on_id":"ROOT","type":"blocks"}]}
{"id":"STALE","title":"Stale issue","status":"open","priority":3,"issue_type":"task","created_at":"%s","updated_at":"%s"}
{"id":"TOMBSTONE","title":"Removed","status":"tombstone","priority":3,"issue_type":"task","created_at":"%s","updated_at":"%s"}`,
		freshTime, freshTime,
		freshTime, freshTime,
		freshTime, freshTime,
		freshTime, freshTime,
		staleCreated, staleUpdated,
		tombstoneCreated, tombstoneUpdated,
	))

	type alert struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		IssueID  string `json:"issue_id"`
	}
	type payload struct {
		DataHash string  `json:"data_hash"`
		Alerts   []alert `json:"alerts"`
		Summary  struct {
			Total    int `json:"total"`
			Critical int `json:"critical"`
			Warning  int `json:"warning"`
			Info     int `json:"info"`
		} `json:"summary"`
	}

	run := func(args ...string) payload {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
		var p payload
		if err := json.Unmarshal(out, &p); err != nil {
			t.Fatalf("json decode: %v\nout=%s", err, out)
		}
		return p
	}

	// Unfiltered output should include at least one stale and one cascade alert.
	base := run("--robot-alerts")
	if base.DataHash == "" {
		t.Fatalf("missing data_hash")
	}
	if base.Summary.Total != len(base.Alerts) {
		t.Fatalf("summary.total=%d; want %d", base.Summary.Total, len(base.Alerts))
	}
	foundStale := false
	foundCascade := false
	foundTombstone := false
	for _, a := range base.Alerts {
		if a.Type == "stale_issue" && a.Severity == "warning" && a.IssueID == "STALE" {
			foundStale = true
		}
		if a.Type == "stale_issue" && a.IssueID == "TOMBSTONE" {
			foundTombstone = true
		}
		if a.Type == "blocking_cascade" && a.IssueID == "ROOT" {
			foundCascade = true
		}
	}
	if !foundStale {
		t.Fatalf("expected stale_issue warning for STALE, got %+v", base.Alerts)
	}
	if foundTombstone {
		t.Fatalf("did not expect stale_issue for TOMBSTONE, got %+v", base.Alerts)
	}
	if !foundCascade {
		t.Fatalf("expected blocking_cascade for ROOT, got %+v", base.Alerts)
	}

	// Type filter.
	onlyStale := run("--robot-alerts", "--alert-type=stale_issue")
	if len(onlyStale.Alerts) == 0 {
		t.Fatalf("expected stale_issue alerts, got 0")
	}
	for _, a := range onlyStale.Alerts {
		if a.Type != "stale_issue" {
			t.Fatalf("unexpected alert type %q in filtered output: %+v", a.Type, a)
		}
	}

	// Severity filter.
	onlyWarning := run("--robot-alerts", "--severity=warning")
	if len(onlyWarning.Alerts) == 0 {
		t.Fatalf("expected warning alerts, got 0")
	}
	for _, a := range onlyWarning.Alerts {
		if a.Severity != "warning" {
			t.Fatalf("unexpected severity %q in filtered output: %+v", a.Severity, a)
		}
	}
}

func TestRobotAlerts_UsesBaselineWhenPresent(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	now := time.Now().UTC()
	ts := now.Add(-1 * time.Hour).Format(time.RFC3339) // stable, non-stale timestamp

	// Start with a single issue and save a baseline.
	writeBeads(t, env, fmt.Sprintf(
		`{"id":"A","title":"A","status":"open","priority":1,"issue_type":"task","created_at":"%s","updated_at":"%s"}`,
		ts, ts,
	))
	save := exec.Command(bv, "--save-baseline", "test baseline")
	save.Dir = env
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("save baseline failed: %v\n%s", err, out)
	}

	// Change the graph: add a second issue (node_count_change drift).
	writeBeads(t, env, fmt.Sprintf(
		`{"id":"A","title":"A","status":"open","priority":1,"issue_type":"task","created_at":"%s","updated_at":"%s"}
{"id":"B","title":"B","status":"open","priority":1,"issue_type":"task","created_at":"%s","updated_at":"%s"}`,
		ts, ts, ts, ts,
	))

	type alert struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
	}
	type payload struct {
		Alerts []alert `json:"alerts"`
	}

	cmd := exec.Command(bv, "--robot-alerts")
	cmd.Dir = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-alerts failed: %v\n%s", err, out)
	}
	var p payload
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	found := false
	for _, a := range p.Alerts {
		if a.Type == "node_count_change" {
			found = true
			if a.Severity != "info" {
				t.Fatalf("expected node_count_change severity=info, got %q", a.Severity)
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected node_count_change in alerts, got %+v", p.Alerts)
	}
}

// TestRobotAlerts_ProactiveTypesAndLabelFilter covers the emitters added for
// the README alert table (D7): every proactive type fires on one fixture,
// --alert-type isolates each, --alert-label keeps only alerts on issues that
// carry the label, and every alert carries a suggested_action.
func TestRobotAlerts_ProactiveTypesAndLabelFilter(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	now := time.Now().UTC()
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	idle := now.AddDate(0, 0, -20).Format(time.RFC3339)
	created := now.AddDate(0, 0, -40).Format(time.RFC3339)
	priorWindow := now.AddDate(0, 0, -10).Format(time.RFC3339)
	recentWindow := now.AddDate(0, 0, -2).Format(time.RFC3339)

	var lines []string
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	// high_impact_unblock + blocking_cascade: HUB (P4, label backend) unblocks two P0 items and one P3.
	add(`{"id":"HUB","title":"Hub","status":"open","priority":4,"issue_type":"task","labels":["backend"],"created_at":"%s","updated_at":"%s"}`, created, fresh)
	for i, p := range []int{0, 0, 3} {
		add(`{"id":"LEAF-%d","title":"Leaf %d","status":"open","priority":%d,"issue_type":"task","created_at":"%s","updated_at":"%s","dependencies":[{"issue_id":"LEAF-%d","depends_on_id":"HUB","type":"blocks"}]}`, i, i, p, created, fresh, i)
	}
	// abandoned_claim: claimed and idle for 20 days (label ops).
	add(`{"id":"CLAIMED","title":"Claimed and forgotten","status":"in_progress","priority":2,"issue_type":"task","assignee":"agent-7","labels":["ops"],"created_at":"%s","updated_at":"%s"}`, created, idle)
	// potential_duplicate: two near-identical titles.
	add(`{"id":"DUP-A","title":"Fix login timeout on slow networks","status":"open","priority":2,"issue_type":"bug","created_at":"%s","updated_at":"%s"}`, created, fresh)
	add(`{"id":"DUP-B","title":"Fix login timeout on slow networks","status":"open","priority":2,"issue_type":"bug","created_at":"%s","updated_at":"%s"}`, created, fresh)
	// velocity_drop: six closes 10 days ago, one 2 days ago.
	for i := 0; i < 6; i++ {
		add(`{"id":"OLD-%d","title":"Old close %d","status":"closed","priority":2,"issue_type":"task","created_at":"%s","updated_at":"%s","closed_at":"%s"}`, i, i, created, priorWindow, priorWindow)
	}
	add(`{"id":"NEW-0","title":"Recent close","status":"closed","priority":2,"issue_type":"task","created_at":"%s","updated_at":"%s","closed_at":"%s"}`, created, recentWindow, recentWindow)
	writeBeads(t, env, strings.Join(lines, "\n"))

	type alert struct {
		Type            string   `json:"type"`
		Severity        string   `json:"severity"`
		IssueID         string   `json:"issue_id"`
		RelatedIssueID  string   `json:"related_issue_id"`
		Labels          []string `json:"labels"`
		SuggestedAction string   `json:"suggested_action"`
	}
	type payload struct {
		Alerts     []alert  `json:"alerts"`
		UsageHints []string `json:"usage_hints"`
	}
	run := func(args ...string) payload {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
		var p payload
		if err := json.Unmarshal(out, &p); err != nil {
			t.Fatalf("json decode: %v\nout=%s", err, out)
		}
		return p
	}

	all := run("--robot-alerts")
	byType := map[string][]alert{}
	for _, a := range all.Alerts {
		byType[a.Type] = append(byType[a.Type], a)
		if a.SuggestedAction == "" {
			t.Errorf("alert without suggested_action: %+v", a)
		}
	}
	t.Logf("alert types: %v", func() []string {
		var ts []string
		for k, v := range byType {
			ts = append(ts, fmt.Sprintf("%s=%d", k, len(v)))
		}
		return ts
	}())

	want := map[string]func(a alert) bool{
		"high_impact_unblock": func(a alert) bool { return a.IssueID == "HUB" && a.Severity == "warning" },
		"blocking_cascade":    func(a alert) bool { return a.IssueID == "HUB" },
		"priority_mismatch":   func(a alert) bool { return a.IssueID == "HUB" && a.Severity == "warning" },
		"abandoned_claim":     func(a alert) bool { return a.IssueID == "CLAIMED" && a.Severity == "warning" },
		"potential_duplicate": func(a alert) bool { return a.IssueID != "" && a.RelatedIssueID != "" && a.Severity == "info" },
		"velocity_drop":       func(a alert) bool { return a.Severity == "warning" },
	}
	for typ, ok := range want {
		matched := false
		for _, a := range byType[typ] {
			if ok(a) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("expected a %s alert matching the fixture, got %+v", typ, byType[typ])
		}
		only := run("--robot-alerts", "--alert-type="+typ)
		if len(only.Alerts) == 0 {
			t.Errorf("--alert-type=%s returned nothing", typ)
		}
		for _, a := range only.Alerts {
			if a.Type != typ {
				t.Errorf("--alert-type=%s leaked %s", typ, a.Type)
			}
		}
	}

	backend := run("--robot-alerts", "--alert-label=backend")
	if len(backend.Alerts) == 0 {
		t.Fatalf("--alert-label=backend should keep HUB's alerts")
	}
	for _, a := range backend.Alerts {
		if a.IssueID != "HUB" {
			t.Errorf("--alert-label=backend leaked an alert on %s (%s): labels=%v", a.IssueID, a.Type, a.Labels)
		}
	}
	ops := run("--robot-alerts", "--alert-label=OPS")
	if len(ops.Alerts) == 0 {
		t.Fatalf("--alert-label match should be case-insensitive")
	}
	for _, a := range ops.Alerts {
		if a.IssueID != "CLAIMED" {
			t.Errorf("--alert-label=ops leaked an alert on %s (%s)", a.IssueID, a.Type)
		}
	}
	if none := run("--robot-alerts", "--alert-label=no-such-label"); len(none.Alerts) != 0 {
		t.Errorf("unknown label should filter everything out, got %+v", none.Alerts)
	}
	hinted := false
	for _, h := range all.UsageHints {
		if strings.Contains(h, "suggested_action") {
			hinted = true
		}
	}
	if !hinted {
		t.Errorf("usage_hints should mention suggested_action: %v", all.UsageHints)
	}
}
