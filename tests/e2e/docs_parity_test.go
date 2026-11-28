package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/pkg/agents"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
)

func TestDocsParity_CopiedRecipeHasEffectiveBehavior(t *testing.T) {
	readme := repoFile(t, "README.md")
	block := regexp.MustCompile("(?s)```yaml\\n(# \\.bv/recipes\\.yaml\\n.*?)\\n```").FindStringSubmatch(readme)
	if len(block) != 2 {
		t.Fatal("README must contain the copyable project recipe YAML")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bv", "recipes.yaml"), []byte(block[1]), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := recipe.NewLoader(recipe.WithProjectDir(dir), recipe.WithUserPath(filepath.Join(dir, "absent-user.yaml")))
	if err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	if warnings := loader.Warnings(); len(warnings) != 0 {
		t.Fatalf("copied recipe rejected: %v", warnings)
	}
	r := loader.Get("sprint-review")
	if r == nil || !reflect.DeepEqual(r.View.Columns, []string{"id", "title", "status", "priority", "updated"}) ||
		!r.View.ShowMetrics || r.View.MaxItems != 50 || r.Export.Format != "markdown" ||
		r.Export.IncludeGraph == nil || !*r.Export.IncludeGraph {
		t.Fatalf("copied recipe lost presentation/export settings: %+v", r)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{ID: "old", Status: model.StatusOpen, UpdatedAt: now.Add(-15 * 24 * time.Hour)},
		{ID: "backlog", Status: model.StatusOpen, UpdatedAt: now, Labels: []string{"backlog"}},
		{ID: "icebox", Status: model.StatusOpen, UpdatedAt: now, Labels: []string{"icebox"}},
		{ID: "parked", Status: model.StatusBlocked, UpdatedAt: now},
		{ID: "recent", Status: model.StatusInProgress, UpdatedAt: now.Add(-time.Hour)},
		{ID: "tie-low", Status: model.StatusOpen, Priority: 3, UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "tie-high", Status: model.StatusClosed, Priority: 1, UpdatedAt: now.Add(-2 * time.Hour)},
	}
	for i := 0; i < 60; i++ {
		issues = append(issues, model.Issue{ID: fmt.Sprintf("tail-%02d", i), Status: model.StatusOpen, UpdatedAt: now.Add(-time.Duration(i+3) * time.Hour)})
	}
	selected, err := recipe.Apply(issues, recipe.Metrics{}, r, now)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"recent", "tie-high", "tie-low"}
	for i := 0; i <= 46; i++ {
		wantIDs = append(wantIDs, fmt.Sprintf("tail-%02d", i))
	}
	var gotIDs []string
	for _, issue := range selected {
		gotIDs = append(gotIDs, issue.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("copied recipe failed filters, complete secondary ordering, or cap: got %v want %v", gotIDs, wantIDs)
	}
}

func TestDocsParity_CopiedRobotQueriesReturnMeaningfulResults(t *testing.T) {
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
		t.Setenv(name, "")
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("actual copied jq examples require jq on PATH")
	}
	bv := buildBvBinary(t)
	dir := t.TempDir()
	writeBeads(t, dir, `{"id":"ROOT","title":"Unblocker","status":"open","priority":4,"issue_type":"task"}
{"id":"MID","title":"Bridge","status":"open","priority":4,"issue_type":"task","dependencies":[{"issue_id":"MID","depends_on_id":"ROOT","type":"blocks"}]}
{"id":"LEAF","title":"Dependent","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"LEAF","depends_on_id":"MID","type":"blocks"}]}`)
	for _, tc := range []struct {
		document string
		prefix   string
		stale    string
		check    func([]any) bool
	}{
		{"README.md", "bv --robot-plan | jq ", ".tracks[].items[] | {id, unblocks}", func(rows []any) bool {
			row, ok := rows[0].(map[string]any)
			return ok && len(rows) == 1 && row["id"] == "ROOT" && reflect.DeepEqual(row["unblocks"], []any{"MID"})
		}},
		{"README.md", "bv --robot-insights | jq '.full_stats.core_number", ".cores | to_entries | sort_by(-.value)[:5]", func(rows []any) bool {
			entries, ok := rows[0].([]any)
			if !ok || len(rows) != 1 || len(entries) != 3 {
				return false
			}
			keys := make(map[string]bool)
			for _, entry := range entries {
				row, ok := entry.(map[string]any)
				if !ok || row["value"] != float64(1) {
					return false
				}
				key, ok := row["key"].(string)
				if !ok || keys[key] {
					return false
				}
				keys[key] = true
			}
			return reflect.DeepEqual(keys, map[string]bool{"ROOT": true, "MID": true, "LEAF": true})
		}},
		{"README.md", "bv --robot-insights | jq '.Articulation", ".articulation", func(rows []any) bool {
			return reflect.DeepEqual(rows, []any{[]any{"MID"}})
		}},
		{"README.md", "bv --robot-priority | jq ", ".priority.recommendations[] | select(.confidence > 0.6)", func(rows []any) bool {
			// Default --robot-priority aligns with --robot-triage ready-leaf filtering:
			// only actionable ROOT is recommended by default; blocked MID requires --robot-include-inactive.
			if len(rows) != 1 {
				return false
			}
			row, ok := rows[0].(map[string]any)
			if !ok {
				return false
			}
			confidence, ok := row["confidence"].(float64)
			return ok && confidence > 0.6 && row["issue_id"] == "ROOT"
		}},
		{"AGENTS.md", "bv --robot-triage | jq ", ".quick_ref", func(rows []any) bool {
			row, ok := rows[0].(map[string]any)
			if !ok || len(rows) != 1 || row["open_count"] != float64(3) || row["actionable_count"] != float64(1) {
				return false
			}
			picks, ok := row["top_picks"].([]any)
			if !ok || len(picks) != 1 {
				return false
			}
			pick, ok := picks[0].(map[string]any)
			return ok && pick["id"] == "ROOT"
		}},
		{"SKILL.md", "bv --robot-plan | jq ", ".summary.highest_impact", func(rows []any) bool {
			return reflect.DeepEqual(rows, []any{"ROOT"})
		}},
		{"SKILL.md", "bv --robot-triage | jq '.triage.recommendations[0]", ".recommendations[0]", func(rows []any) bool {
			row, ok := rows[0].(map[string]any)
			return ok && len(rows) == 1 && row["id"] == "ROOT" && row["claimable"] == true &&
				reflect.DeepEqual(row["unblocks_ids"], []any{"MID"})
		}},
		{"SKILL.md", "bv --robot-triage --robot-include-inactive | jq '.triage.recommendations[0]", ".recommendations[0]", func(rows []any) bool {
			row, ok := rows[0].(map[string]any)
			// Explicit planning diagnostics retain the original blocked bridge
			// details without claiming it as ready work.
			return ok && len(rows) == 1 && row["id"] == "MID" && row["claimable"] == false &&
				reflect.DeepEqual(row["blocked_by"], []any{"ROOT"}) && reflect.DeepEqual(row["unblocks_ids"], []any{"LEAF"})
		}},
	} {
		t.Run(tc.document+"/"+tc.prefix, func(t *testing.T) {
			var line string
			for _, candidate := range strings.Split(repoFile(t, tc.document), "\n") {
				if strings.HasPrefix(candidate, tc.prefix) {
					line = candidate
					break
				}
			}
			parts := strings.SplitN(line, " | jq ", 2)
			if len(parts) != 2 {
				t.Fatalf("missing copyable quoted jq example for %q", tc.prefix)
			}
			quoted := regexp.MustCompile(`^'([^']*)'(?:\s+#.*)?$`).FindStringSubmatch(parts[1])
			if len(quoted) != 2 {
				t.Fatalf("invalid copied jq expression: %s", parts[1])
			}
			cmd := exec.Command(bv, strings.Fields(strings.TrimPrefix(parts[0], "bv "))...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1788220800")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			payload, err := cmd.Output()
			if err != nil {
				t.Fatalf("copied bv command: %v\n%s", err, stderr.String())
			}
			t.Logf("fixture=ROOT<-MID<-LEAF argv=%q SOURCE_DATE_EPOCH=1788220800 exit=0 stdout=%s stderr=%s", cmd.Args, payload, stderr.String())
			query := exec.Command(jq, "-c", quoted[1])
			query.Stdin = bytes.NewReader(payload)
			query.Stderr = &stderr
			output, err := query.Output()
			if err != nil {
				t.Fatalf("copied jq expression: %v\n%s", err, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Logf("command diagnostics: %s", stderr.String())
			}
			var rows []any
			decoder := json.NewDecoder(bytes.NewReader(output))
			for {
				var row any
				if err := decoder.Decode(&row); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			if len(rows) == 0 || !tc.check(rows) {
				t.Fatalf("documented query returned the wrong fixture result: %s", output)
			}
			// These deliberately stale paths previously bypassed the actual robot
			// envelopes or used the wrong metric casing. The same fixture and
			// consumer must reject them rather than accepting an empty/null result.
			stale := exec.Command(jq, "-c", tc.stale)
			stale.Stdin = bytes.NewReader(payload)
			var staleStderr bytes.Buffer
			stale.Stderr = &staleStderr
			staleOutput, staleErr := stale.Output()
			t.Logf("copied jq=%q expected fixture result observed=%s; stale jq=%q exit=%v stdout=%s stderr=%s", quoted[1], output, tc.stale, staleErr, staleOutput, staleStderr.String())
			if staleErr == nil {
				var staleRows []any
				decoder := json.NewDecoder(bytes.NewReader(staleOutput))
				for {
					var row any
					if err := decoder.Decode(&row); err == io.EOF {
						break
					} else if err != nil {
						t.Fatalf("decode stale-query control: %v", err)
					}
					staleRows = append(staleRows, row)
				}
				if len(staleRows) > 0 && tc.check(staleRows) {
					t.Fatalf("stale query unexpectedly satisfied the documented behavior: %s", staleOutput)
				}
			} else if _, ok := staleErr.(*exec.ExitError); !ok {
				t.Fatalf("stale query did not execute: %v", staleErr)
			}
		})
	}
	t.Run("robot_priority_include_inactive_retains_blocked_bridge", func(t *testing.T) {
		cmd := exec.Command(bv, "--robot-priority", "--robot-include-inactive")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1788220800")
		payload, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--robot-priority --robot-include-inactive failed: %v\n%s", err, payload)
		}
		var parsed struct {
			Recommendations []struct {
				IssueID    string  `json:"issue_id"`
				Confidence float64 `json:"confidence"`
			} `json:"recommendations"`
		}
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("unmarshal priority payload: %v\n%s", err, payload)
		}
		foundBridge := false
		for _, rec := range parsed.Recommendations {
			if rec.IssueID == "MID" && rec.Confidence > 0.6 {
				foundBridge = true
			}
		}
		if !foundBridge {
			t.Fatalf("expected --robot-priority --robot-include-inactive to retain blocked bridge MID, got %+v", parsed.Recommendations)
		}
	})
}

func TestDocsParity_CopiedHistoryQueryPreservesUnits(t *testing.T) {
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
		t.Setenv(name, "")
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("actual copied history query requires jq on PATH")
	}
	bv := buildBvBinary(t)
	dir := t.TempDir()
	git := func(date string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_AUTHOR_NAME=Docs Test", "GIT_AUTHOR_EMAIL=docs@example.invalid",
			"GIT_COMMITTER_NAME=Docs Test", "GIT_COMMITTER_EMAIL=docs@example.invalid",
			"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git argv=%q date=%s exit=%v output=%s", cmd.Args, date, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("2026-06-01T00:00:00Z", "init", "--initial-branch=main")
	steps := []struct {
		status string
		event  string
		date   string
	}{
		{"open", "created", "2026-06-01T00:00:00Z"},
		{"in_progress", "claimed", "2026-06-02T00:00:00Z"},
		{"closed", "closed", "2026-06-04T00:00:00Z"},
	}
	commits := make(map[string]string)
	for _, step := range steps {
		writeBeads(t, dir, fmt.Sprintf(`{"id":"BV-123","title":"Documented lifecycle","status":%q,"priority":1,"issue_type":"task"}`, step.status))
		if err := os.WriteFile(filepath.Join(dir, "work.go"), []byte(fmt.Sprintf("package work\nconst State = %q\n", step.status)), 0o600); err != nil {
			t.Fatal(err)
		}
		git(step.date, "add", ".beads", "work.go")
		git(step.date, "-c", "commit.gpgsign=false", "commit", "-m", "BV-123 "+step.event)
		commits[step.event] = git(step.date, "rev-parse", "HEAD")
	}
	readme := repoFile(t, "README.md")
	line := regexp.MustCompile(`(?m)^bv --robot-history \| jq '([^']+)'$`).FindStringSubmatch(readme)
	if len(line) != 2 {
		t.Fatal("README must contain a copyable robot-history query with explicit duration units")
	}
	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1788220800")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	payload, err := cmd.Output()
	t.Logf("fixture=BV-123 created June 1, claimed June 2, closed June 4 UTC commits=%v argv=%q SOURCE_DATE_EPOCH=1788220800 exit=%v stdout=%s stderr=%s", commits, cmd.Args, err, payload, stderr.String())
	if err != nil {
		t.Fatal("copied robot-history command failed")
	}
	var report struct {
		Histories map[string]struct {
			Events []struct {
				EventType string `json:"event_type"`
				CommitSHA string `json:"commit_sha"`
			} `json:"events"`
			Milestones map[string]struct {
				Timestamp string `json:"timestamp"`
				CommitSHA string `json:"commit_sha"`
			} `json:"milestones"`
		} `json:"histories"`
		CommitIndex map[string][]string `json:"commit_index"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatal(err)
	}
	history := report.Histories["BV-123"]
	if len(report.Histories) != 1 || len(history.Events) != 3 || len(history.Milestones) != 3 {
		t.Fatalf("history must contain one bead with its three actual lifecycle events and named milestones: %+v", report)
	}
	for i, step := range steps {
		event := history.Events[i]
		milestone := history.Milestones[step.event]
		if event.EventType != step.event || event.CommitSHA != commits[step.event] ||
			milestone.Timestamp != step.date || milestone.CommitSHA != commits[step.event] {
			t.Fatalf("event or milestone lost actual %s commit %s: event=%+v milestone=%+v", step.event, commits[step.event], event, milestone)
		}
	}
	// The reverse index maps correlated code commits, not every lifecycle
	// event. Co-commit correlation covers the actual claim/close edits here.
	for _, event := range []string{"claimed", "closed"} {
		if !reflect.DeepEqual(report.CommitIndex[commits[event]], []string{"BV-123"}) {
			t.Fatalf("reverse lookup lost the %s code commit %s: %v", event, commits[event], report.CommitIndex)
		}
	}
	want := map[string]any{
		"avg_cycle_time_days": float64(2),
		"beads":               []any{map[string]any{"id": "BV-123", "claim_to_close_ns": float64(48 * time.Hour)}},
	}
	for _, tc := range []struct {
		name  string
		query string
		valid bool
	}{
		{"copied", line[1], true},
		{"missing histories envelope", `{avg_cycle_time_days: .stats.avg_cycle_time_days, beads: [.history | to_entries[] | {id: .key, claim_to_close_ns: .value.cycle_time.claim_to_close}]}`, false},
		{"duration mistaken for seconds", `{avg_cycle_time_days: .stats.avg_cycle_time_days, beads: [.histories | to_entries[] | {id: .key, claim_to_close_ns: (.value.cycle_time.claim_to_close / 1000000000)}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := exec.Command(jq, "-c", tc.query)
			query.Stdin = bytes.NewReader(payload)
			var diagnostics bytes.Buffer
			query.Stderr = &diagnostics
			output, err := query.Output()
			t.Logf("jq argv=%q exit=%v stdout=%s stderr=%s want=%v valid=%v", query.Args, err, output, diagnostics.String(), want, tc.valid)
			if err != nil {
				if _, exited := err.(*exec.ExitError); !tc.valid && exited {
					return
				}
				t.Fatalf("query failed: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatal(err)
			}
			if matches := reflect.DeepEqual(got, want); matches != tc.valid {
				t.Fatalf("query validity=%v, got %v, want %v", tc.valid, got, want)
			}
		})
	}
}

func TestDocsParity_ForecastWorkedExamples(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	closedAt, explicit, completed := now.Add(-24*time.Hour), 120, 240
	issues := []model.Issue{
		{ID: "explicit", Status: model.StatusOpen, IssueType: model.TypeFeature, Description: strings.Repeat("界", 1000), EstimatedMinutes: &explicit},
		{ID: "median", Status: model.StatusOpen, IssueType: model.TypeFeature, Description: strings.Repeat("界", 1000)},
		{ID: "explicit-child", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "explicit", Type: model.DepBlocks}}},
		{ID: "median-child", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "median", Type: model.DepBlocks}}},
		{ID: "completed", Status: model.StatusClosed, IssueType: model.TypeTask, EstimatedMinutes: &completed, ClosedAt: &closedAt},
	}
	stats := analysis.NewAnalyzer(issues).Analyze()
	readme := repoFile(t, "README.md")
	for _, tc := range []struct {
		id      string
		minutes int
		days    float64
	}{{"explicit", 280, 280.0 / 17.6}, {"median", 421, 421.0 / 17.6}} {
		// Values are independently worked from base 120/180, feature 1.3,
		// depth 2 => 1.2, 1000 runes => 1.5; closed task 240 * 1.1 = 264m =>
		// velocity 264/30 = 8.8 min/day; integer minutes / (8.8 * 2).
		row := regexp.MustCompile(`(?m)^\| ` + tc.id + ` \| ([0-9]+) \| ([0-9.]+) \|$`).FindStringSubmatch(readme)
		if len(row) != 3 {
			t.Errorf("README is missing its executable %s forecast example", tc.id)
			continue
		}
		minutes, _ := strconv.Atoi(row[1])
		days, _ := strconv.ParseFloat(row[2], 64)
		eta, err := analysis.EstimateETAForIssue(issues, &stats, tc.id, 2, now)
		if err != nil {
			t.Fatal(err)
		}
		if stats.GetCriticalPathScore(tc.id) != 2 || minutes != tc.minutes ||
			math.Abs(days-tc.days) >= 1e-4 ||
			eta.EstimatedMinutes != minutes ||
			math.Abs(eta.EstimatedDays-tc.days) >= 1e-9 ||
			math.Abs(eta.VelocityMinutesPerDay-8.8) >= 1e-9 {
			t.Errorf("%s documented=%dm/%gd runtime=%+v, independently expected=%dm/%gd", tc.id, minutes, days, eta, tc.minutes, tc.days)
		}
	}
}

func TestDocsParity_RecipeKeyDispatchesPicker(t *testing.T) {
	readme := repoFile(t, "README.md")
	row := regexp.MustCompile("(?m)^\\| .*\\| `([^`]+)` \\| Recipe picker \\|$").FindStringSubmatch(readme)
	if len(row) != 2 || len([]rune(row[1])) != 1 {
		t.Fatal("README must document a single recipe-picker key")
	}
	m := ui.NewModel([]model.Issue{{ID: "doc-key", Title: "Visible issue", Status: model.StatusOpen}}, nil, "")
	t.Cleanup(m.Stop)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	if strings.Contains(m.View(), "Select Recipe") {
		t.Fatal("picker was already open before the documented key")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(row[1])})
	if !strings.Contains(m.View(), "Select Recipe") {
		t.Fatalf("documented key %q did not open the actual picker", row[1])
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.View(), "Select Recipe") {
		t.Fatal("Escape did not return from the picker")
	}
}

func TestDocsParity_InsightsMapDefaultMatchesRuntime(t *testing.T) {
	readme := repoFile(t, "README.md")
	row := regexp.MustCompile("(?m)^\\| `BV_INSIGHTS_MAP_LIMIT` \\|.*\\| `([0-9]+)` \\|$").FindStringSubmatch(readme)
	if len(row) != 2 {
		t.Fatal("README must give the actual numeric insights-map default")
	}
	defaultLimit, err := strconv.Atoi(row[1])
	if err != nil || defaultLimit != 200 {
		t.Fatalf("documented default=%q, want 200", row[1])
	}
	dir := t.TempDir()
	var fixture strings.Builder
	for i := 0; i < 230; i++ {
		fmt.Fprintf(&fixture, "{\"id\":\"map-%03d\",\"title\":\"Issue %d\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":2}\n", i, i)
	}
	writeIssuesJSONL(t, dir, fixture.String())
	bv := buildBvBinary(t)
	var environment []string
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		if key != "BEADS_DIR" && key != "BEADS_DB" && key != "BD_DB" && key != "BV_INSIGHTS_MAP_LIMIT" {
			environment = append(environment, item)
		}
	}
	for _, tc := range []struct {
		value string
		want  int
	}{{"", defaultLimit}, {"17", 17}, {"0", defaultLimit}, {"-1", defaultLimit}, {"invalid", defaultLimit}} {
		t.Run("value="+tc.value, func(t *testing.T) {
			cmd := exec.Command(bv, "--robot-insights")
			cmd.Dir = dir
			cmd.Env = environment
			if tc.value != "" {
				cmd.Env = append(cmd.Env, "BV_INSIGHTS_MAP_LIMIT="+tc.value)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("argv=%q env limit=%q exit=%v stderr=%s stdout=%s", cmd.Args, tc.value, err, stderr.String(), out)
			}
			var result struct {
				FullStats struct {
					PageRank map[string]float64 `json:"pagerank"`
				} `json:"full_stats"`
			}
			if err := json.Unmarshal(out, &result); err != nil || len(result.FullStats.PageRank) != tc.want {
				t.Fatalf("limit=%q expected=%d observed=%d decode=%v stderr=%s stdout=%s", tc.value, tc.want, len(result.FullStats.PageRank), err, stderr.String(), out)
			}
			t.Logf("argv=%q fixture=230 independent issues limit=%q expected=%d observed=%d stderr=%q", cmd.Args, tc.value, tc.want, len(result.FullStats.PageRank), stderr.String())
		})
	}
}

func TestDocsParity_ConfiguredCoverageThresholds(t *testing.T) {
	workflow := repoFile(t, ".github/workflows/ci.yml")
	document := repoFile(t, "docs/testing.md")
	configured := make(map[string]string)
	for _, row := range regexp.MustCompile(`github\.com/Dicklesworthstone/beads_viewer/(pkg/[a-z_]+)\) req=([0-9]+)`).FindAllStringSubmatch(workflow, -1) {
		configured[row[1]] = row[2]
	}
	documented := make(map[string]string)
	for _, row := range regexp.MustCompile("(?m)^\\| `(pkg/[a-z_]+)` \\| ([0-9]+)% \\|$").FindAllStringSubmatch(document, -1) {
		documented[row[1]] = row[2]
	}
	if len(configured) < 8 || !reflect.DeepEqual(documented, configured) {
		t.Fatalf("documented coverage thresholds do not match configured workflow: documented=%v configured=%v", documented, configured)
	}
}

// workflowUsesRef matches the action reference of a workflow `uses:` line.
var workflowUsesRef = regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*["']?([^\s"'#]+)`)

// pinnedActionRef is an action pinned to a full commit SHA. A tag or branch
// can be moved upstream to run different code with the job's secrets.
var pinnedActionRef = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

// TestRepoPolicy_WorkflowActionsArePinned keeps every GitHub Action in this
// repository's workflows pinned to a full commit SHA (#197). Local actions
// (./path) and digest-pinned Docker images are allowed.
func TestRepoPolicy_WorkflowActionsArePinned(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	checked := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range workflowUsesRef.FindAllStringSubmatch(string(data), -1) {
			ref := match[1]
			checked++
			switch {
			case strings.HasPrefix(ref, "./"):
			case strings.HasPrefix(ref, "docker://") && strings.Contains(ref, "@sha256:"):
			case pinnedActionRef.MatchString(ref):
			default:
				t.Errorf("%s: action %q is not pinned to a full commit SHA", filepath.Base(path), ref)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no `uses:` lines; the pattern no longer matches the workflows")
	}
}

// repoFile reads a file relative to the repository root (tests/e2e/..).
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// TestDocsParity_NoPendingMarkers: the 2026-09-01 reality check tagged every
// README sentence that described unshipped behaviour with a bv:pending
// marker. All of them were resolved; a new marker means a doc claim landed
// ahead of its code and must not ship.
func TestDocsParity_NoPendingMarkers(t *testing.T) {
	for _, rel := range []string{"README.md", "AGENTS.md", "docs/performance.md"} {
		if n := strings.Count(repoFile(t, rel), "bv:pending"); n != 0 {
			t.Errorf("%s still carries %d bv:pending marker(s); ship the code or remove the claim", rel, n)
		}
	}
}

// TestDocsParity_AlertTableMatchesCode: the README alert tables must name
// every alert type the drift package can emit, and every configuration key
// the tables mention must exist in the drift config.
func TestDocsParity_AlertTableMatchesCode(t *testing.T) {
	readme := repoFile(t, "README.md")
	start := strings.Index(readme, "## 🚨 Alerts System")
	if start < 0 {
		t.Fatalf("README has no Alerts System section")
	}
	section := readme[start:]
	if end := strings.Index(section, "### TUI Integration"); end > 0 {
		section = section[:end]
	}
	for _, typ := range drift.AllAlertTypes() {
		if !strings.Contains(section, "`"+string(typ)+"`") {
			t.Errorf("README alert tables do not document %q", typ)
		}
	}

	config := repoFile(t, "pkg/drift/config.go")
	keyRe := regexp.MustCompile("`([a-z_]+)` \\(")
	seen := map[string]bool{}
	for _, m := range keyRe.FindAllStringSubmatch(section, -1) {
		key := m[1]
		if seen[key] {
			continue
		}
		seen[key] = true
		if !strings.Contains(config, "yaml:\""+key+"\"") {
			t.Errorf("README documents drift key %q that pkg/drift/config.go does not define", key)
		}
	}
	if len(seen) < 10 {
		t.Errorf("expected the alert tables to document the .bv/drift.yaml keys, found %d", len(seen))
	}
}

// TestAgentsMD_HasRCHTrustBoundary: the RCH section must say what leaves the
// machine and what never may.
func TestAgentsMD_HasRCHTrustBoundary(t *testing.T) {
	agents := repoFile(t, "AGENTS.md")
	idx := strings.Index(agents, "### Trust boundary")
	if idx < 0 {
		t.Fatalf("AGENTS.md RCH section has no 'Trust boundary' subsection")
	}
	sub := agents[idx:]
	for _, want := range []string{"never be shipped", "fails open", "approval"} {
		if !strings.Contains(sub, want) {
			t.Errorf("RCH trust boundary subsection should mention %q", want)
		}
	}
}

// TestDocsParity_ReadmeBlurbMatchesGenerated: the "Ready-made Blurb" section
// of the README must be the same text bv installs into AGENTS.md
// (agents.AgentBlurb). Every non-blank line of the generated blurb (minus its
// HTML marker lines) has to appear verbatim in the README, so a change to one
// without the other fails here.
func TestDocsParity_ReadmeBlurbMatchesGenerated(t *testing.T) {
	readme := repoFile(t, "README.md")
	start := strings.Index(readme, agents.BlurbStartMarker)
	if start < 0 {
		t.Fatalf("README is missing current blurb marker %s", agents.BlurbStartMarker)
	}
	end := strings.Index(readme[start:], agents.BlurbEndMarker)
	if end < 0 {
		t.Fatal("README blurb is missing its end marker")
	}
	copied := readme[start : start+end+len(agents.BlurbEndMarker)]
	if copied != strings.TrimSpace(agents.AgentBlurb) {
		t.Fatal("README copied blurb differs from AgentBlurb; additions and omissions must be synchronized in both directions")
	}
	var missing []string
	for _, line := range strings.Split(agents.AgentBlurb, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "<!--") {
			continue
		}
		if !strings.Contains(readme, line) {
			missing = append(missing, line)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d generated blurb line(s) are not in README.md (regenerate the Ready-made Blurb section from pkg/agents/blurb.go):\n%s", len(missing), strings.Join(missing, "\n"))
	}
	if !strings.Contains(readme, agents.BlurbStartMarker) {
		t.Fatalf("README blurb section should carry the %s marker so agents can find the installed version", agents.BlurbStartMarker)
	}
}

// TestReadme_NoUnpinnedPipedInstallers (G1 / bv-apal.14): the README must never
// tell users to pipe a script from the moving main branch into a shell; every
// raw.githubusercontent.com installer URL has to name a commit SHA whose
// install.sh / install.ps1 blob matches HEAD.
func TestReadme_NoUnpinnedPipedInstallers(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	checkReadme := func(readme string) error {
		if strings.Contains(readme, "beads_viewer/main/install") {
			return fmt.Errorf("README pipes an installer from the moving main branch; pin it to a commit SHA")
		}
		pinned := regexp.MustCompile(`(?:github\.com/Dicklesworthstone/beads_viewer/blob|raw\.githubusercontent\.com/Dicklesworthstone/beads_viewer)/([0-9a-f]{40})/(install\.(?:sh|ps1))`)
		matches := pinned.FindAllStringSubmatch(readme, -1)
		if len(matches) < 4 {
			return fmt.Errorf("expected the install.sh and install.ps1 examples to be pinned to a 40-hex commit, found %d", len(matches))
		}
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			return nil
		}
		for _, m := range matches {
			sha, script := m[1], m[2]
			catCmd := exec.Command("git", "cat-file", "-e", sha+"^{commit}")
			catCmd.Dir = repo
			if err := catCmd.Run(); err != nil {
				t.Logf("skipping blob check for %s:%s (commit not in local history): %v", sha, script, err)
				continue
			}
			pinnedCmd := exec.Command("git", "rev-parse", fmt.Sprintf("%s:%s", sha, script))
			pinnedCmd.Dir = repo
			pinnedBlob, err := pinnedCmd.Output()
			if err != nil {
				return fmt.Errorf("git rev-parse %s:%s failed: %w", sha, script, err)
			}
			headCmd := exec.Command("git", "rev-parse", fmt.Sprintf("HEAD:%s", script))
			headCmd.Dir = repo
			headBlob, err := headCmd.Output()
			if err != nil {
				return fmt.Errorf("git rev-parse HEAD:%s failed: %w", script, err)
			}
			if strings.TrimSpace(string(pinnedBlob)) != strings.TrimSpace(string(headBlob)) {
				return fmt.Errorf("README.md pins %s to commit %s (blob %s), which does not match HEAD:%s (blob %s)",
					script, sha, strings.TrimSpace(string(pinnedBlob)), script, strings.TrimSpace(string(headBlob)))
			}
		}
		return nil
	}

	if err := checkReadme(repoFile(t, "README.md")); err != nil {
		t.Fatal(err)
	}

	t.Run("negative_control_ee81c286", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			t.Skip("git repository metadata not present")
		}
		cmd := exec.Command("git", "show", "ee81c286e89ed6f9d5d10c62da307cafe95c710b:README.md")
		cmd.Dir = repo
		oldReadme, err := cmd.Output()
		if err != nil {
			t.Skipf("pre-fix commit ee81c286 not available in clone: %v", err)
		}
		err = checkReadme(string(oldReadme))
		if err == nil {
			t.Fatal("expected pre-fix ee81c286 README.md (pinned to a43b8e85) to fail blob parity check, but it passed")
		}
		t.Logf("negative control rejected stale ee81c286 README.md as expected: %v", err)
	})
}

// TestDocsParity_NoStaleBehaviourPhrases (F4): wording that once described
// behaviour the code does not have must not come back.
func TestDocsParity_NoStaleBehaviourPhrases(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, stale := range []string{
		"relative timestamps", // markdown export writes absolute dates for comments
		"Windows requires Go 1.21",
		"dependency-aware scheduling", // forecast/capacity are heuristics, not a scheduler
	} {
		if strings.Contains(readme, stale) {
			t.Errorf("README still says %q", stale)
		}
	}
	for _, must := range []string{
		"BV_BACKGROUND_MODE", // startup default plus runtime promotion documented in the env table
		"### Cache",          // disk cache location, TTL, invalidation, opt-out
		"--no-hooks",         // the hooks opt-out
	} {
		if !strings.Contains(readme, must) {
			t.Errorf("README lost the %q documentation", must)
		}
	}
}

// bvEnvVarsInCode returns every BV_* environment variable name that appears
// as a string literal in non-test Go code, keyed by the first file naming it.
func bvEnvVarsInCode(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	// Any BV_* string literal counts: most variables are read through named
	// constants (os.Getenv(EnvSemanticEmbedder)), so matching Getenv alone misses them.
	re := regexp.MustCompile(`"(BV_[A-Z0-9_]+)"`)
	found := map[string]string{}
	for _, dir := range []string{"cmd", "pkg", "internal"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range re.FindAllStringSubmatch(string(data), -1) {
				if _, seen := found[m[1]]; !seen {
					found[m[1]] = strings.TrimPrefix(path, root+string(filepath.Separator))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return found
}

// TestDocsParity_EnvVarsDocumented (F2): every BV_* variable the code reads
// has a row in the README environment table, and every documented row is
// read by the code. Variables that are internal wiring between bv and its
// own subprocesses are listed as exemptions with the reason.
func TestDocsParity_EnvVarsDocumented(t *testing.T) {
	readme := repoFile(t, "README.md")
	rowRe := regexp.MustCompile("(?m)^\\| `(BV_[A-Z0-9_]+)`")
	documented := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(readme, -1) {
		documented[m[1]] = true
	}
	exempt := map[string]string{
		"BV_TEST_MODE":      "test harness switch, not a user setting",
		"BV_BROWSER_LOG":    "test harness capture of browser opens",
		"BV_SKIP_ENV_TESTS": "test harness switch",
	}
	inCode := bvEnvVarsInCode(t)
	if len(inCode) < 10 {
		t.Fatalf("scanner found only %d BV_* variables; the walk is broken", len(inCode))
	}
	for name, file := range inCode {
		if _, ok := exempt[name]; ok {
			continue
		}
		if !documented[name] {
			t.Errorf("%s is read in %s but has no row in the README environment table", name, file)
		}
	}
	for name := range documented {
		if _, ok := inCode[name]; !ok {
			t.Errorf("README documents %s but no non-test Go code reads it", name)
		}
	}
}

// TestDocsParity_RobotCommandsDocumented (F2): every robot command the
// binary advertises in --robot-capabilities must be mentioned in README.md
// and in the AGENTS.md blurb source, and every environment variable the
// capabilities payload lists must have a README row.
func TestDocsParity_RobotCommandsDocumented(t *testing.T) {
	bv := buildBvBinary(t)
	cmd := exec.Command(bv, "--robot-capabilities")
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("--robot-capabilities: %v", err)
	}
	var caps struct {
		Commands []struct {
			Name string `json:"name"`
			Flag string `json:"flag"`
		} `json:"commands"`
		EnvironmentVariables map[string]json.RawMessage `json:"environment_variables"`
	}
	if err := json.Unmarshal(out, &caps); err != nil {
		t.Fatalf("capabilities decode: %v", err)
	}
	if len(caps.Commands) < 20 {
		t.Fatalf("capabilities lists only %d commands; payload shape changed?", len(caps.Commands))
	}
	readme := repoFile(t, "README.md")
	for _, c := range caps.Commands {
		fields := strings.Fields(c.Flag) // "--robot-related ISSUE_ID": only the flag token must appear
		if len(fields) == 0 || strings.Contains(readme, fields[0]) {
			continue
		}
		t.Errorf("robot command %s is advertised by --robot-capabilities but README.md never mentions %s", c.Name, fields[0])
	}
	for name := range caps.EnvironmentVariables {
		if !strings.HasPrefix(name, "BV_") {
			continue
		}
		if !strings.Contains(readme, "| `"+name+"`") {
			t.Errorf("environment variable %s is advertised by --robot-capabilities but has no README env table row", name)
		}
	}
}

// TestDocsParity_KeyBindingsDocumented (F2): every key the TUI registers in
// its binding registry (the source of the shortcuts sidebar and help) must
// appear somewhere in the README's key tables, so a new binding cannot ship
// undocumented and a removed one cannot linger in the docs unnoticed.
func TestDocsParity_KeyBindingsDocumented(t *testing.T) {
	readme := repoFile(t, "README.md")
	// Keys are written in the README inside backticks, e.g. `Shift+Tab`,
	// `n` / `N`, `ctrl+d`; compare case-insensitively on the backticked form.
	lower := strings.ToLower(readme)
	var missing []string
	seen := map[string]bool{}
	for _, doc := range ui.GetKeyBindingDocs() {
		key := strings.TrimSpace(doc.Key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if !strings.Contains(lower, "`"+strings.ToLower(key)+"`") {
			missing = append(missing, key+" ("+doc.Desc+", "+doc.Category+")")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d registered key binding(s) are not documented in README.md:\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

// TestDocsParity_ToonAdviceMatchesMeasuredSizes (bv-apal.3): the binary's own
// agent-facing docs must not sell TOON as a uniform token saving. TOON is
// measured smaller than JSON only for the wide --robot-graph payload and
// 9-15% larger for the nested ones (tests/artifacts/perf/toon_vs_json.md), so
// any --robot-docs example that advertises a saving has to name a payload the
// artifact records as a win. The old text ("saves ~30-50% tokens", with
// `bv robot-triage --toon` as the saving example) fails this test.
func TestDocsParity_ToonAdviceMatchesMeasuredSizes(t *testing.T) {
	artifact := repoFile(t, filepath.Join("tests", "artifacts", "perf", "toon_vs_json.md"))
	// The artifact table records TOON/JSON ratios per command in the
	// "TOON / JSON" column. A command is a documented win only when its
	// recorded ratio is below 1.
	wins := map[string]bool{}
	for _, line := range strings.Split(artifact, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `--robot-") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 4 {
			t.Fatalf("unexpected artifact row shape: %q", line)
		}
		command := strings.Trim(strings.TrimSpace(cells[0]), "`")
		ratio, err := strconv.ParseFloat(strings.TrimSpace(cells[3]), 64)
		if err != nil {
			t.Fatalf("unparsable TOON/JSON ratio %q for %s in row %q", cells[3], command, line)
		}
		wins[command] = ratio < 1.0
		t.Logf("artifact ratio %-24s %.2f win=%v", command, ratio, ratio < 1.0)
	}
	if len(wins) == 0 {
		t.Fatalf("no TOON/JSON ratio rows parsed from tests/artifacts/perf/toon_vs_json.md")
	}
	if !wins["--robot-graph"] {
		t.Fatalf("artifact no longer records --robot-graph as the TOON win; refresh README and this test together")
	}

	bv := buildBvBinary(t)
	out, err := exec.Command(bv, "--robot-docs", "all").Output()
	if err != nil {
		t.Fatalf("argv=%q exit=%v stdout=%s", []string{bv, "--robot-docs", "all"}, err, out)
	}
	var docs struct {
		Guide struct {
			OutputModes map[string]string `json:"output_modes"`
		} `json:"guide"`
		Examples []struct {
			Description string `json:"description"`
			Command     string `json:"command"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(out, &docs); err != nil {
		t.Fatalf("decode --robot-docs all: %v\nstdout=%s", err, out)
	}
	modes := docs.Guide.OutputModes
	toonBlurb, ok := modes["toon"]
	if !ok {
		t.Fatalf("--robot-docs no longer describes the toon output mode; modes=%v", modes)
	}
	t.Logf("output_modes.toon = %q", toonBlurb)
	// An unqualified savings claim is the defect: the blurb has to name the
	// payload shape that actually wins, not a blanket percentage.
	if !strings.Contains(toonBlurb, "--robot-graph") {
		t.Errorf("output_modes.toon must name the payload TOON actually shrinks (--robot-graph), got %q", toonBlurb)
	}
	if !strings.Contains(strings.ToLower(toonBlurb), "larger") {
		t.Errorf("output_modes.toon must state that nested payloads are larger, got %q", toonBlurb)
	}

	// Any example whose description promises a saving must use a measured win.
	savingWords := []string{"saves", "saving", "smaller"}
	commandRe := regexp.MustCompile(`\b(robot-[a-z-]+)\b`)
	for _, example := range docs.Examples {
		if !strings.Contains(example.Command, "toon") && !strings.Contains(example.Command, "TOON") {
			continue
		}
		lowerDesc := strings.ToLower(example.Description)
		promises := false
		for _, word := range savingWords {
			if strings.Contains(lowerDesc, word) {
				promises = true
			}
		}
		if !promises {
			continue
		}
		named := commandRe.FindStringSubmatch(example.Command)
		if named == nil {
			t.Errorf("TOON saving example names no robot command: %+v", example)
			continue
		}
		flag := "--" + named[1]
		if !wins[flag] {
			t.Errorf("example %q promises a TOON saving but %s is measured at ratio >= 1 in toon_vs_json.md", example.Command, flag)
			continue
		}
		t.Logf("saving example %q -> %s (measured win)", example.Command, flag)
	}
}

// TestDocsParity_ToonFallbackDeclaresJSONOutputFormat (bv-apal.3): TOON
// encoding shells out to the `tru` binary. When no encoder is discoverable the
// payload falls back to JSON, and the envelope's own output_format field must
// say so rather than claiming "toon" over JSON bytes.
func TestDocsParity_ToonFallbackDeclaresJSONOutputFormat(t *testing.T) {
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, "{\"id\":\"toon-1\",\"title\":\"Fallback fixture\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":2}\n")
	bv := buildBvBinary(t)

	cmd := exec.Command(bv, "--robot-next", "--format", "toon")
	cmd.Dir = dir
	// Force TOON discovery to fail deterministically on any host. Pointing
	// TOON_TRU_BIN at a nonexistent path short-circuits findTruBinary (it
	// returns immediately rather than falling through to PATH or the
	// well-known /usr/bin/tru style locations), so a host that happens to have
	// tru installed cannot make this assert a false premise. PATH is also
	// stripped as a second guard.
	cmd.Env = []string{
		"HOME=" + filepath.Join(dir, "nonexistent"),
		"PATH=",
		"TOON_TRU_BIN=" + filepath.Join(dir, "no-such-tru"),
		"BV_NO_BROWSER=1", "BV_TEST_MODE=1", "TOON_STATS=1",
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("argv=%q exit=%v stderr=%s stdout=%s", cmd.Args, err, stderr.String(), out)
	}
	if !json.Valid(out) {
		t.Fatalf("expected the JSON fallback with no encoder present, got:\n%s\nstderr=%s", out, stderr.String())
	}
	if !strings.Contains(stderr.String(), "falling back to JSON") {
		t.Errorf("expected the fallback warning on stderr, got %q", stderr.String())
	}
	var envelope struct {
		OutputFormat string `json:"output_format"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("decode fallback payload: %v\nstdout=%s", err, out)
	}
	if envelope.OutputFormat != "json" {
		t.Fatalf("fallback payload declares output_format=%q but the bytes are JSON; agents keying on the envelope are misled", envelope.OutputFormat)
	}
	t.Logf("argv=%q output_format=%q stderr=%q", cmd.Args, envelope.OutputFormat, strings.TrimSpace(stderr.String()))
}

// TestDocsParity_RobotSchemaTypesMatchRuntime (bv-apal.3): README points agents
// at `bv --robot-schema` as "the complete contract", so a declared JSON type
// that disagrees with what the command actually emits is a lie in the contract
// an agent is told to trust. For each command exercised here, every schema
// property that the real payload also carries must agree on array vs object vs
// string vs number. The insights Cores/Slack properties were declared "object"
// while the command emits arrays; that state fails this test.
func TestDocsParity_RobotSchemaTypesMatchRuntime(t *testing.T) {
	// Load only the temp fixture, not whatever tracker the host env points at.
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
		t.Setenv(name, "")
	}
	dir := t.TempDir()
	var fixture strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&fixture, "{\"id\":\"sch-%03d\",\"title\":\"Issue %d\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":2}\n", i, i)
	}
	// A couple of dependencies so cycle/critical-path style fields are populated.
	fmt.Fprintf(&fixture, "{\"id\":\"sch-dep\",\"title\":\"Dependent\",\"status\":\"open\",\"issue_type\":\"task\",\"priority\":1,\"dependencies\":[{\"id\":\"sch-000\",\"type\":\"blocks\"}]}\n")
	writeIssuesJSONL(t, dir, fixture.String())
	bv := buildBvBinary(t)

	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("argv=%q exit=%v stderr=%s", cmd.Args, err, stderr.String())
		}
		return out
	}

	// A property's "type" is either a string or, for nullable fields, a list
	// such as ["string","null"]. Accept both and treat a list as a set of
	// permitted kinds.
	var schema struct {
		Commands map[string]struct {
			Properties map[string]struct {
				Type json.RawMessage `json:"type"`
			} `json:"properties"`
		} `json:"commands"`
	}
	permittedKinds := func(raw json.RawMessage) []string {
		if len(raw) == 0 {
			return nil
		}
		var single string
		if err := json.Unmarshal(raw, &single); err == nil {
			return []string{single}
		}
		var many []string
		if err := json.Unmarshal(raw, &many); err == nil {
			return many
		}
		return nil
	}
	if err := json.Unmarshal(run("--robot-schema"), &schema); err != nil {
		t.Fatalf("decode --robot-schema: %v", err)
	}
	if len(schema.Commands) == 0 {
		t.Fatal("--robot-schema declared no commands")
	}

	// jsonKind reports the JSON Schema type name for a decoded value.
	jsonKind := func(v any) string {
		switch v.(type) {
		case []any:
			return "array"
		case map[string]any:
			return "object"
		case string:
			return "string"
		case float64, json.Number:
			return "number"
		case bool:
			return "boolean"
		case nil:
			return "null"
		}
		return "unknown"
	}

	// Commands whose payload this fixture reliably populates.
	for _, command := range []string{"robot-insights", "robot-plan", "robot-triage", "robot-alerts", "robot-graph"} {
		declared, ok := schema.Commands[command]
		if !ok {
			t.Errorf("--robot-schema declares no entry for %s", command)
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(run("--"+command), &payload); err != nil {
			t.Fatalf("decode --%s: %v", command, err)
		}
		checked := 0
		for name, spec := range declared.Properties {
			kinds := permittedKinds(spec.Type)
			if len(kinds) == 0 {
				continue
			}
			value, present := payload[name]
			if !present {
				// Optional/omitempty fields are not evidence of drift.
				continue
			}
			actual := jsonKind(value)
			// null means the runtime omitted a value rather than
			// contradicting the declared type.
			if actual == "null" {
				continue
			}
			checked++
			allowed := false
			for _, kind := range kinds {
				if kind == actual || (kind == "integer" && actual == "number") {
					allowed = true
				}
			}
			if !allowed {
				t.Errorf("--%s: schema declares %q as %v but the command emits %q", command, name, kinds, actual)
			}
		}
		if checked == 0 {
			t.Errorf("--%s: no declared property was present in the real payload; the fixture or the schema entry is wrong", command)
		}
		t.Logf("--%s: %d declared properties checked against real output", command, checked)
	}
}

// TestDocsParity_RobotWallProseMatchesArtifact (bv-apal.3 / bv-q0po): the
// README's startup paragraph cites tests/artifacts/perf/robot_wall.json for
// per-command wall times. That prose previously claimed a warm/cold split and
// a `bv --version` figure the artifact does not contain. Rather than police
// wording, this pins the numbers: every fact the paragraph states about the
// artifact must be derivable from the artifact itself.
func TestDocsParity_RobotWallProseMatchesArtifact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "artifacts", "perf", "robot_wall.json"))
	if err != nil {
		t.Fatalf("read robot_wall.json: %v", err)
	}
	var artifact struct {
		Go       string `json:"go"`
		Commands []struct {
			Command string `json:"command"`
			MS      int    `json:"ms"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("decode robot_wall.json: %v", err)
	}
	if len(artifact.Commands) == 0 {
		t.Fatal("robot_wall.json records no commands")
	}

	byCommand := map[string]int{}
	minMS, maxMS := artifact.Commands[0].MS, artifact.Commands[0].MS
	for _, entry := range artifact.Commands {
		byCommand[entry.Command] = entry.MS
		if entry.MS < minMS {
			minMS = entry.MS
		}
		if entry.MS > maxMS {
			maxMS = entry.MS
		}
	}

	readme := repoFile(t, "README.md")
	var paragraph string
	for _, line := range strings.Split(readme, "\n") {
		if strings.Contains(line, "robot_wall.json") {
			paragraph = line
			break
		}
	}
	if paragraph == "" {
		t.Fatal("README no longer cites tests/artifacts/perf/robot_wall.json")
	}

	// The artifact is a single pass per command. Prose must not claim a
	// warm/cold characterisation the artifact cannot support.
	for _, forbidden := range []string{"with warm caches", "first cold run"} {
		if strings.Contains(paragraph, forbidden) {
			t.Errorf("README claims %q but robot_wall.json records one run per command with no warm/cold split", forbidden)
		}
	}
	// The artifact does not time `bv --version`.
	if _, timed := byCommand["--version"]; !timed && regexp.MustCompile(`ms for \x60bv --version\x60`).MatchString(paragraph) {
		t.Error("README gives a bv --version wall time, but robot_wall.json does not time it")
	}

	// The artifact writes "go1.25.5"; prose reads better as "Go 1.25.5", so
	// compare on the version number rather than the exact token.
	mustContain := map[string]string{
		"the artifact's Go version": strings.TrimPrefix(artifact.Go, "go"),
		"the command count":         fmt.Sprintf("%d commands", len(artifact.Commands)),
		"the observed span":         fmt.Sprintf("%d-%d ms", minMS, maxMS),
	}
	for _, command := range []string{"--robot-next", "--robot-insights", "--robot-triage"} {
		ms, ok := byCommand[command]
		if !ok {
			continue
		}
		mustContain[command+" timing"] = fmt.Sprintf("`%s` at %d ms", command, ms)
	}
	for what, want := range mustContain {
		if !strings.Contains(paragraph, want) {
			t.Errorf("README startup paragraph does not state %s (%q) as recorded in robot_wall.json", what, want)
		}
	}
	t.Logf("artifact: go=%s commands=%d span=%d-%d ms", artifact.Go, len(artifact.Commands), minMS, maxMS)
}

// TestDocsParity_InstallersReportBuiltToolchain (bv-y3m9): both installers built
// the source with the module's `toolchain` directive (go1.26.8 for v0.25.0)
// while printing the launcher's Go version, so a user on go1.26.5 was told the
// build used 1.26.5. The source-build path cannot run inside `go test` (it needs
// a full network build and, on Windows, a live console), so this is a structural
// guard on the behaviour-defining lines: each installer must derive a reported
// build toolchain from `go version -m` on the built binary, and must not present
// the launcher version as the build compiler. Real-host verification is recorded
// on bv-y3m9 (install.sh via RCH; install.ps1 on Windows against a 1.26.5-launched
// build of go1.26.8 source).
func TestDocsParity_InstallersReportBuiltToolchain(t *testing.T) {
	for _, tc := range []struct {
		file      string
		derives   string // the command that reads the real toolchain from the artifact
		builtLine string // the report of the toolchain actually used
		stale     string // the old wording that presented the launcher as the build Go
	}{
		{"install.sh", "go version -m", "Built with $built_go", "Using Go $go_version\""},
		{"install.ps1", "go version -m $binary", "Built with $builtGo", "vendored source with Go $goVersion"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			body := repoFile(t, tc.file)
			if !strings.Contains(body, tc.derives) {
				t.Errorf("%s must read the real toolchain from the built binary via %q", tc.file, tc.derives)
			}
			if !strings.Contains(body, tc.builtLine) {
				t.Errorf("%s must report the toolchain actually used (%q)", tc.file, tc.builtLine)
			}
			if strings.Contains(body, tc.stale) {
				t.Errorf("%s still presents the launcher Go as the build compiler (%q); it must be qualified", tc.file, tc.stale)
			}
		})
	}
}

// TestDocsParity_RepoAgentsMDHasCurrentBlurb (bv-apal.14): guards against
// bumping pkg/agents.BlurbVersion without running --agents-update on the
// repository's own AGENTS.md.
func TestDocsParity_RepoAgentsMDHasCurrentBlurb(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	checkAgentsDir := func(dir string) error {
		detection := agents.DetectAgentFile(dir)
		if !detection.Found() {
			return fmt.Errorf("AGENTS.md not found in %s", dir)
		}
		if !detection.HasBlurb || detection.NeedsBlurb() || detection.NeedsUpgrade() || detection.HasMalformedBlurb() {
			return fmt.Errorf("AGENTS.md blurb is out of date or malformed: version=%d want=%d needsUpgrade=%v err=%q",
				detection.BlurbVersion, agents.BlurbVersion, detection.NeedsUpgrade(), detection.BlurbStructureError)
		}
		if detection.BlurbVersion != agents.BlurbVersion || detection.BlurbCount != 1 || detection.BlurbStructureError != "" {
			return fmt.Errorf("AGENTS.md BlurbVersion=%d (want %d), BlurbCount=%d (want 1), err=%q",
				detection.BlurbVersion, agents.BlurbVersion, detection.BlurbCount, detection.BlurbStructureError)
		}
		if !strings.Contains(detection.Content, agents.AgentBlurb) {
			return fmt.Errorf("AGENTS.md does not contain the exact current agents.AgentBlurb text")
		}
		if strings.Contains(detection.Content, "br update <id> --status=in_progress") {
			return fmt.Errorf("AGENTS.md still contains stale '--status=in_progress' claim guidance instead of '--claim'")
		}
		return nil
	}

	if err := checkAgentsDir(repo); err != nil {
		t.Fatal(err)
	}

	t.Run("negative_control_ee81c286", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			t.Skip("git repository metadata not present")
		}
		cmd := exec.Command("git", "show", "ee81c286e89ed6f9d5d10c62da307cafe95c710b:AGENTS.md")
		cmd.Dir = repo
		oldAgents, err := cmd.Output()
		if err != nil {
			t.Skipf("pre-fix commit ee81c286 not available in clone: %v", err)
		}
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), oldAgents, 0o600); err != nil {
			t.Fatal(err)
		}
		err = checkAgentsDir(tmp)
		if err == nil {
			t.Fatal("expected pre-fix ee81c286 AGENTS.md (v5 blurb) to fail currency check, but it passed")
		}
		t.Logf("negative control rejected stale ee81c286 AGENTS.md as expected: %v", err)
	})

	t.Run("negative_control_516e96aa_v7", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			t.Skip("git repository metadata not present")
		}
		cmd := exec.Command("git", "show", "516e96aa:AGENTS.md")
		cmd.Dir = repo
		oldAgents, err := cmd.Output()
		if err != nil {
			t.Skipf("pre-v8 commit 516e96aa not available in clone: %v", err)
		}
		tmp := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmp, "AGENTS.md"), oldAgents, 0o600); err != nil {
			t.Fatal(err)
		}
		err = checkAgentsDir(tmp)
		if err == nil {
			t.Fatal("expected pre-v8 516e96aa AGENTS.md (v7 blurb) to fail v8 currency check, but it passed")
		}
		t.Logf("negative control rejected stale 516e96aa AGENTS.md (v7) as expected: %v", err)
	})
}

// TestDocsParity_ReleaseAndProvenanceContracts (bv-oonu.24): guards release,
// provenance, testing, README, flake, and changelog documentation against
// policy/version/User-Agent/CI drift.
func TestDocsParity_ReleaseAndProvenanceContracts(t *testing.T) {
	releasing := repoFile(t, "docs/RELEASING.md")
	for _, must := range []string{
		"v0.26.0 verification and receipt qualification",
		"scripts/release_gate.sh preflight",
		"govulncheck",
	} {
		if !strings.Contains(releasing, must) {
			t.Errorf("docs/RELEASING.md missing required release contract clause %q", must)
		}
	}

	provenance := repoFile(t, "docs/PROVENANCE.md")
	if strings.Contains(provenance, "OpenAI File Downloader") || strings.Contains(provenance, "XaiImageApiFetch") {
		t.Error("docs/PROVENANCE.md must never instruct borrowing a third-party User-Agent")
	}
	if !strings.Contains(provenance, "beads-viewer-updater") {
		t.Error("docs/PROVENANCE.md must document honest project User-Agent identifiers")
	}

	testingDoc := repoFile(t, "docs/testing.md")
	if strings.Contains(testingDoc, "before\nthe workflow is re-enabled") || strings.Contains(testingDoc, "before the workflow is re-enabled") {
		t.Error("docs/testing.md must not suggest re-enabling GitHub Actions workflows")
	}
	if !strings.Contains(testingDoc, "DSR") {
		t.Error("docs/testing.md CI Integration section must document permanent DSR-only policy")
	}

	readme := repoFile(t, "README.md")
	if strings.Contains(readme, "codecov.io/gh/Dicklesworthstone/beads_viewer") {
		t.Error("README.md must not display stale Codecov badge")
	}

	flake := repoFile(t, "flake.nix")
	if strings.Contains(flake, "blob/v${bvVersion}/LICENSE") || strings.Contains(flake, "blob/v0.26.1/LICENSE") {
		t.Error("flake.nix must not contain untagged GitHub blob/v<version>/LICENSE URLs")
	}

	changelog := repoFile(t, "CHANGELOG.md")
	if !strings.Contains(changelog, "### Post-516e96aa reality-check gap closure") {
		t.Error("CHANGELOG.md Unreleased section must distinguish Post-516e96aa gap-closure work from v0.26.0..516e96aa history")
	}

	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		logCmd := exec.Command("git", "log", "v0.26.0..516e96aa", "--no-merges", "--format=%h")
		logCmd.Dir = repo
		if out, err := logCmd.Output(); err == nil {
			shas := strings.Fields(string(out))
			if len(shas) == 0 {
				t.Fatal("expected non-empty git log v0.26.0..516e96aa --no-merges")
			}
			for _, sha := range shas {
				if !strings.Contains(changelog, sha) {
					t.Errorf("CHANGELOG.md Unreleased section missing post-v0.26.0 commit SHA %s", sha)
				}
			}
		}
	}
}

// TestDocsParity_PagesLoadProseMatchesArtifact (bv-apal.14): guards README.md's
// static-export size and date citations against drift from
// tests/artifacts/perf/pages_load.json (mirroring TestDocsParity_RobotWallProseMatchesArtifact).
func TestDocsParity_PagesLoadProseMatchesArtifact(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(repo, "tests", "artifacts", "perf", "pages_load.json"))
	if err != nil {
		t.Fatalf("read pages_load.json: %v", err)
	}
	var artifact struct {
		GeneratedAt string `json:"generated_at"`
		Issues      int    `json:"issues"`
		Edges       int    `json:"edges"`
		Bytes       struct {
			IndexHTML       int64 `json:"index_html"`
			BeadsSQLite3    int64 `json:"beads_sqlite3"`
			GraphLayoutJSON int64 `json:"graph_layout_json"`
			TriageJSON      int64 `json:"triage_json"`
			HistoryJSON     int64 `json:"history_json"`
			VendorTotal     int64 `json:"vendor_total"`
			BundleTotal     int64 `json:"bundle_total"`
		} `json:"bytes"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("decode pages_load.json: %v", err)
	}
	genTime, err := time.Parse(time.RFC3339, artifact.GeneratedAt)
	if err != nil {
		t.Fatalf("parse pages_load.json generated_at %q: %v", artifact.GeneratedAt, err)
	}

	indexKB := fmt.Sprintf("%d KB", (artifact.Bytes.IndexHTML+500)/1000)
	layoutKB := fmt.Sprintf("%d KB", (artifact.Bytes.GraphLayoutJSON+500)/1000)
	triageKB := fmt.Sprintf("%d KB", (artifact.Bytes.TriageJSON+500)/1000)
	historyKB := fmt.Sprintf("%d KB", (artifact.Bytes.HistoryJSON+500)/1000)
	sqliteMB := fmt.Sprintf("%.1f MB", float64(artifact.Bytes.BeadsSQLite3)/1e6)
	bundleMB := fmt.Sprintf("%.1f MB", float64(artifact.Bytes.BundleTotal)/1e6)
	vendorMB := fmt.Sprintf("%.1f MB", float64(artifact.Bytes.VendorTotal)/1e6)
	dateStr := genTime.UTC().Format("January 2, 2006")

	mustContain := map[string]string{
		"tree index.html size":                    fmt.Sprintf("(%s)", indexKB),
		"tree beads.sqlite3 size and issue count": fmt.Sprintf("%s for this repository's %d issues", sqliteMB, artifact.Issues),
		"tree graph_layout.json size and counts":  fmt.Sprintf("(%s for %d issues / %d edges)", layoutKB, artifact.Issues, artifact.Edges),
		"tree triage.json size":                   fmt.Sprintf("(%s)", triageKB),
		"tree history.json size":                  fmt.Sprintf("(%s)", historyKB),
		"table graph_layout.json row":             fmt.Sprintf("| `graph_layout.json` | %s for %d issues / %d edges |", layoutKB, artifact.Issues, artifact.Edges),
		"table beads.sqlite3 row":                 fmt.Sprintf("| `beads.sqlite3` | %s for %d issues |", sqliteMB, artifact.Issues),
		"bundle_total and vendor_total prose":     fmt.Sprintf("whole bundle %s, of which %s is the vendored viewer libraries", bundleMB, vendorMB),
		"5 MiB SQLite chunking explanation":       "5 MiB",
		"retained export date":                    fmt.Sprintf("retained %s export", dateStr),
	}
	checkReadme := func(readme string) error {
		for what, want := range mustContain {
			if !strings.Contains(readme, want) {
				return fmt.Errorf("README static-export section does not match pages_load.json for %s: missing %q", what, want)
			}
		}
		return nil
	}

	if err := checkReadme(repoFile(t, "README.md")); err != nil {
		t.Fatal(err)
	}

	t.Run("negative_control_ee81c286", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			t.Skip("git repository metadata not present")
		}
		cmd := exec.Command("git", "show", "ee81c286e89ed6f9d5d10c62da307cafe95c710b:README.md")
		cmd.Dir = repo
		oldReadme, err := cmd.Output()
		if err != nil {
			t.Skipf("pre-fix commit ee81c286 not available in clone: %v", err)
		}
		err = checkReadme(string(oldReadme))
		if err == nil {
			t.Fatal("expected pre-fix ee81c286 README.md (9.7 MB / 611 issues) to fail pages_load.json parity check, but it passed")
		}
		t.Logf("negative control rejected stale ee81c286 README.md as expected: %v", err)
	})
}

func TestDocsParity_RobotCapacityFormulaAndLabelScope(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, want := range []string{
		"graham_min_days",
		"graham_max_days",
		"velocity_minutes_per_day",
		"clamped to `[graham_min_days, graham_max_days]`",
		"`--robot-capacity`'s `total_days` and `--robot-forecast`'s `summary.total_days` convert minutes at a fixed eight-hour workday (`minutes / 480`)",
	} {
		if !strings.Contains(readme, want) {
			t.Fatalf("README.md capacity/forecast formula prose missing %q", want)
		}
	}
	if strings.Contains(readme, "legacy `estimated_days`/`total_days`") {
		t.Fatal("README.md must not claim --robot-capacity estimated_days uses fixed 480 min/day")
	}

	bv := buildBvBinary(t)
	ws := t.TempDir()
	beadsDir := filepath.Join(ws, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	createdAt := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	// Global workspace has 3 explicit 240-minute closed tasks in other labels (so global median = 240),
	// while the "backend" label has two open tasks with NO explicit estimated_minutes.
	// Before the fix, --robot-capacity --capacity-label backend passed targetIssues instead of ctx.Issues
	// to EstimateETAForIssue, collapsing base minutes from 240 -> 60 and disagreeing with ComputeGrahamBounds
	// and --robot-forecast!
	lines := []string{
		fmt.Sprintf(`{"id":"CL-1","title":"Closed 1","status":"closed","priority":1,"issue_type":"task","estimated_minutes":240,"labels":["ops"],"created_at":%q,"updated_at":%q,"closed_at":%q}`, createdAt, closedAt, closedAt),
		fmt.Sprintf(`{"id":"CL-2","title":"Closed 2","status":"closed","priority":1,"issue_type":"task","estimated_minutes":240,"labels":["ops"],"created_at":%q,"updated_at":%q,"closed_at":%q}`, createdAt, closedAt, closedAt),
		fmt.Sprintf(`{"id":"CL-3","title":"Closed 3","status":"closed","priority":1,"issue_type":"task","estimated_minutes":240,"labels":["ops"],"created_at":%q,"updated_at":%q,"closed_at":%q}`, createdAt, closedAt, closedAt),
		fmt.Sprintf(`{"id":"BE-1","title":"Backend 1","status":"open","priority":1,"issue_type":"task","labels":["backend"],"created_at":%q,"updated_at":%q}`, createdAt, createdAt),
		fmt.Sprintf(`{"id":"BE-2","title":"Backend 2","status":"open","priority":1,"issue_type":"task","labels":["backend"],"created_at":%q,"updated_at":%q}`, createdAt, createdAt),
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capCmd := exec.Command(bv, "--robot-capacity", "--capacity-label", "backend", "--agents", "2")
	capCmd.Dir = ws
	capCmd.Env = append(os.Environ(), "BV_ROBOT_NOW="+now.Format(time.RFC3339))
	capOut, err := capCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-capacity failed: %v\n%s", err, capOut)
	}
	var capPayload struct {
		TotalMinutes          int     `json:"total_minutes"`
		TotalDays             float64 `json:"total_days"`
		EstimatedDays         float64 `json:"estimated_days"`
		GrahamMinDays         float64 `json:"graham_min_days"`
		GrahamMaxDays         float64 `json:"graham_max_days"`
		VelocityMinutesPerDay float64 `json:"velocity_minutes_per_day"`
	}
	if err := json.Unmarshal(capOut, &capPayload); err != nil {
		t.Fatalf("unmarshal --robot-capacity: %v\n%s", err, capOut)
	}

	fcCmd := exec.Command(bv, "--robot-forecast", "all", "--forecast-label", "backend")
	fcCmd.Dir = ws
	fcCmd.Env = append(os.Environ(), "BV_ROBOT_NOW="+now.Format(time.RFC3339))
	fcOut, err := fcCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-forecast failed: %v\n%s", err, fcOut)
	}
	var fcPayload struct {
		Summary struct {
			TotalMinutes int     `json:"total_minutes"`
			TotalDays    float64 `json:"total_days"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(fcOut, &fcPayload); err != nil {
		t.Fatalf("unmarshal --robot-forecast: %v\n%s", err, fcOut)
	}

	if capPayload.TotalMinutes != fcPayload.Summary.TotalMinutes || capPayload.TotalMinutes < 480 {
		t.Fatalf("expected --robot-capacity total_minutes (%d) to match --robot-forecast summary.total_minutes (%d) using global 240m median",
			capPayload.TotalMinutes, fcPayload.Summary.TotalMinutes)
	}
	wantTotalDays := math.Round(float64(capPayload.TotalMinutes)/480.0*100) / 100
	if math.Abs(capPayload.TotalDays-wantTotalDays) > 0.02 {
		t.Fatalf("total_days = %v, want minutes/480 = %v", capPayload.TotalDays, wantTotalDays)
	}
	if capPayload.EstimatedDays < capPayload.GrahamMinDays-0.02 || capPayload.EstimatedDays > capPayload.GrahamMaxDays+0.02 {
		t.Fatalf("estimated_days = %v outside Graham bounds [%v, %v]", capPayload.EstimatedDays, capPayload.GrahamMinDays, capPayload.GrahamMaxDays)
	}
	// Verify historical velocity (720m / 30d = 24 min/day, far from 480 min/day) drove estimated_days > total_days.
	if capPayload.EstimatedDays <= capPayload.TotalDays {
		t.Fatalf("expected velocity-calibrated estimated_days (%v at vel=%v/d) > fixed-480m total_days (%v)",
			capPayload.EstimatedDays, capPayload.VelocityMinutesPerDay, capPayload.TotalDays)
	}
}

func TestDocsParity_TutorialKeysFileBeadsGlobAndLabelFlowCascade(t *testing.T) {
	readme := repoFile(t, "README.md")
	if !strings.Contains(readme, "| **Context Tutorial** | `~` |") {
		t.Fatal("README.md must document `~` as Context Tutorial in the help surfaces table")
	}
	if strings.Contains(readme, "press `` ` `` while in Board view") {
		t.Fatal("README.md must not claim backtick opens the context-filtered tutorial")
	}

	// Verify live TUI behavior matches README: in Board view, `~` sets contextMode=true, while `` ` `` sets contextMode=false.
	m := ui.NewModel([]model.Issue{
		{ID: "T-1", Title: "Board item", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
	}, nil, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(*ui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = updated.(*ui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'~'}})
	m = updated.(*ui.Model)
	if !m.IsTutorialContextMode() {
		t.Fatal("expected pressing '~' in Board view to open context-filtered tutorial")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(*ui.Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'`'}})
	m = updated.(*ui.Model)
	if m.IsTutorialContextMode() {
		t.Fatal("expected pressing '`' in Board view to open unfiltered full tutorial")
	}

	// Verify --robot-file-beads glob support + bad-glob rejection + --robot-label-flow blockage_cascade.
	bv := buildBvBinary(t)
	ws := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(ws, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	issuesJSONL := strings.Join([]string{
		`{"id":"bv-10","title":"Root schema","status":"open","priority":1,"issue_type":"task","labels":["schema"]}`,
		`{"id":"bv-11","title":"DB blocked","status":"open","priority":1,"issue_type":"task","labels":["db"],"dependencies":[{"issue_id":"bv-11","depends_on_id":"bv-10","type":"blocks"}]}`,
		`{"id":"bv-12","title":"UI waiting","status":"open","priority":2,"issue_type":"task","labels":["ui"],"dependencies":[{"issue_id":"bv-12","depends_on_id":"bv-11","type":"blocks"}]}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(ws, ".beads", "issues.jsonl"), []byte(issuesJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "pkg", "sub", "alpha.go"), []byte("package sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "feat: implement bv-10 alpha")

	globCmd := exec.Command(bv, "--robot-file-beads", "pkg/sub/*.go")
	globCmd.Dir = ws
	globOut, err := globCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-file-beads glob failed: %v\n%s", err, globOut)
	}
	var fileBeads struct {
		TotalBeads int `json:"total_beads"`
		OpenBeads  []struct {
			BeadID string `json:"bead_id"`
		} `json:"open_beads"`
	}
	if err := json.Unmarshal(globOut, &fileBeads); err != nil {
		t.Fatalf("unmarshal --robot-file-beads: %v\n%s", err, globOut)
	}
	if fileBeads.TotalBeads != 1 || len(fileBeads.OpenBeads) != 1 || fileBeads.OpenBeads[0].BeadID != "bv-10" {
		t.Fatalf("expected --robot-file-beads glob to match bv-10, got %+v", fileBeads)
	}

	badGlobCmd := exec.Command(bv, "--robot-file-beads", "[")
	badGlobCmd.Dir = ws
	if out, err := badGlobCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected invalid glob '[' to fail, got exit 0:\n%s", out)
	}

	flowCmd := exec.Command(bv, "--robot-label-flow")
	flowCmd.Dir = ws
	flowOut, err := flowCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-label-flow failed: %v\n%s", err, flowOut)
	}
	var flowPayload struct {
		BlockageCascade struct {
			TotalBlocked int `json:"total_blocked"`
			TopUnblocks  []struct {
				IssueID string `json:"issue_id"`
			} `json:"top_unblocks"`
		} `json:"blockage_cascade"`
	}
	if err := json.Unmarshal(flowOut, &flowPayload); err != nil {
		t.Fatalf("unmarshal --robot-label-flow: %v\n%s", err, flowOut)
	}
	if flowPayload.BlockageCascade.TotalBlocked != 2 || len(flowPayload.BlockageCascade.TopUnblocks) == 0 {
		t.Fatalf("expected --robot-label-flow blockage_cascade with 2 blocked issues and top_unblocks, got %+v", flowPayload.BlockageCascade)
	}
}

// TestDocsParity_RC40CrossDocumentTruthAndNegativeControls (bv-vjsx / bv-j9i7):
// locks every cross-document reconciliation across README.md, AGENTS.md,
// SKILL.md, docs/{performance,PROVENANCE,RELEASING,testing}.md,
// bv-graph-wasm/README.md, CHANGELOG.md, and MANIFEST.json against live
// runtime behavior and planted stale-doc negative controls, emitting structured
// JSON diagnostics on every check.
func TestDocsParity_RC40CrossDocumentTruthAndNegativeControls(t *testing.T) {
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
		t.Setenv(name, "")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	sourceRev := "unknown"
	if out, err := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output(); err == nil {
		sourceRev = strings.TrimSpace(string(out))
	}

	type rc40AuditLog struct {
		SourceRev string   `json:"source_rev"`
		FixtureID string   `json:"fixture_id"`
		DocPath   string   `json:"doc_path"`
		Argv      []string `json:"argv,omitempty"`
		Expected  string   `json:"expected"`
		Observed  string   `json:"observed"`
		Stdout    string   `json:"stdout,omitempty"`
		Stderr    string   `json:"stderr,omitempty"`
		ExitCode  int      `json:"exit_code"`
	}
	formatLog := func(entry rc40AuditLog) string {
		entry.SourceRev = sourceRev
		b, err := json.Marshal(entry)
		if err != nil {
			return fmt.Sprintf("marshal audit log: %v", err)
		}
		return string(b)
	}

	bv := buildBvBinary(t)

	// 1. SKILL.md covers 47/47 primary commands from live --robot-capabilities.
	t.Run("skill_md_47_command_parity", func(t *testing.T) {
		cmd := exec.Command(bv, "--robot-capabilities")
		cmd.Dir = t.TempDir()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "empty_temp_dir",
				DocPath:   "SKILL.md",
				Argv:      cmd.Args,
				Expected:  "exit 0 with 47 registered commands",
				Observed:  err.Error(),
				Stdout:    string(out),
				Stderr:    stderr.String(),
				ExitCode:  1,
			}))
		}
		var caps struct {
			Commands []struct {
				Name string `json:"name"`
				Flag string `json:"flag"`
			} `json:"commands"`
		}
		if err := json.Unmarshal(out, &caps); err != nil {
			t.Fatalf("decode --robot-capabilities: %v", err)
		}
		if len(caps.Commands) != 47 {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "robot_capabilities_count",
				DocPath:   "SKILL.md",
				Argv:      cmd.Args,
				Expected:  "47 commands",
				Observed:  fmt.Sprintf("%d commands", len(caps.Commands)),
				Stderr:    stderr.String(),
				ExitCode:  0,
			}))
		}
		checkSkillDoc := func(skillText string) error {
			var missing []string
			for _, c := range caps.Commands {
				fields := strings.Fields(c.Flag)
				if len(fields) == 0 {
					continue
				}
				exactToken := regexp.MustCompile(`(?m)(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(fields[0]) + `(?:[^A-Za-z0-9_-]|$)`)
				if !exactToken.MatchString(skillText) {
					missing = append(missing, fields[0])
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("SKILL.md missing %d registered command(s): %s", len(missing), strings.Join(missing, ", "))
			}
			return nil
		}
		skillDoc := repoFile(t, "SKILL.md")
		if err := checkSkillDoc(skillDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "live_skill_md",
				DocPath:   "SKILL.md",
				Argv:      cmd.Args,
				Expected:  "all 47 registered commands present",
				Observed:  err.Error(),
				Stderr:    stderr.String(),
				ExitCode:  0,
			}))
		}
		// Planted negative control: remove --robot-causality from synthetic SKILL.md.
		staleSkill := strings.ReplaceAll(skillDoc, "--robot-causality", "--robot-removed-control")
		if err := checkSkillDoc(staleSkill); err == nil {
			t.Fatalf("negative control failed: SKILL.md missing --robot-causality unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_missing_robot_causality",
				DocPath:   "SKILL.md",
				Argv:      cmd.Args,
				Expected:  "missing --robot-causality error",
				Observed:  err.Error(),
				ExitCode:  0,
			}))
		}
	})

	// 2. docs/PROVENANCE.md & bv-graph-wasm/README.md match MANIFEST.json and live vendor files byte-for-byte.
	t.Run("manifest_provenance_and_wasm_readme_parity", func(t *testing.T) {
		rawManifest := repoFile(t, filepath.Join("pkg", "export", "viewer_assets", "vendor", "MANIFEST.json"))
		var manifest struct {
			GraphWasm struct {
				SourceSHA256 string `json:"source_sha256"`
			} `json:"graph_wasm"`
			Files []struct {
				Name   string `json:"name"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		}
		if err := json.Unmarshal([]byte(rawManifest), &manifest); err != nil {
			t.Fatalf("decode MANIFEST.json: %v", err)
		}
		byName := make(map[string]string, len(manifest.Files))
		for _, f := range manifest.Files {
			byName[f.Name] = f.SHA256
		}
		sourceSHA := manifest.GraphWasm.SourceSHA256
		if sourceSHA == "" || byName["bv_graph_bg.wasm"] == "" || byName["bv_graph.js"] == "" {
			t.Fatalf("unexpected MANIFEST.json entries: source_sha256=%q wasm=%q js=%q", sourceSHA, byName["bv_graph_bg.wasm"], byName["bv_graph.js"])
		}
		wasmStat, err := os.Stat(filepath.Join(repo, "pkg", "export", "viewer_assets", "vendor", "bv_graph_bg.wasm"))
		if err != nil {
			t.Fatalf("stat bv_graph_bg.wasm: %v", err)
		}
		jsStat, err := os.Stat(filepath.Join(repo, "pkg", "export", "viewer_assets", "vendor", "bv_graph.js"))
		if err != nil {
			t.Fatalf("stat bv_graph.js: %v", err)
		}
		formatBytes := func(n int64) string {
			s := strconv.FormatInt(n, 10)
			if len(s) <= 3 {
				return s
			}
			var parts []string
			for len(s) > 3 {
				parts = append([]string{s[len(s)-3:]}, parts...)
				s = s[:len(s)-3]
			}
			parts = append([]string{s}, parts...)
			return strings.Join(parts, ",")
		}
		wasmBytesStr := formatBytes(wasmStat.Size())
		jsBytesStr := formatBytes(jsStat.Size())

		checkProvenance := func(provText string) error {
			for _, want := range []string{
				sourceSHA,
				"`" + wasmBytesStr + "` bytes",
				"`" + jsBytesStr + "` bytes",
				"explicit tracked-only `//go:embed` globs",
				"`218` `bv-graph-wasm`",
			} {
				if !strings.Contains(provText, want) {
					return fmt.Errorf("docs/PROVENANCE.md missing %q", want)
				}
			}
			if strings.Contains(provText, "15175bfceaf") {
				return fmt.Errorf("docs/PROVENANCE.md still contains stale 2026-09-09 source_sha256")
			}
			return nil
		}
		provDoc := repoFile(t, "docs/PROVENANCE.md")
		if err := checkProvenance(provDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "manifest_vs_provenance",
				DocPath:   "docs/PROVENANCE.md",
				Expected:  fmt.Sprintf("source_sha256=%s wasm=%s js=%s", sourceSHA, wasmBytesStr, jsBytesStr),
				Observed:  err.Error(),
			}))
		}
		staleProv := strings.ReplaceAll(provDoc, sourceSHA, "15175bfceaf00000000000000000000000000000000000000000000000000000")
		if err := checkProvenance(staleProv); err == nil {
			t.Fatal("negative control failed: stale source_sha256 in docs/PROVENANCE.md unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_stale_provenance_sha",
				DocPath:   "docs/PROVENANCE.md",
				Expected:  "mismatch error on stale source_sha256",
				Observed:  err.Error(),
			}))
		}

		wasmReadme := repoFile(t, "bv-graph-wasm/README.md")
		for _, want := range []string{
			wasmBytesStr + " B",
			jsBytesStr + " B",
			`opt-level = "z"`,
			"`full`",
			"`core`",
			"gzip -n -9",
		} {
			if !strings.Contains(wasmReadme, want) {
				t.Fatalf("%s", formatLog(rc40AuditLog{
					FixtureID: "manifest_vs_wasm_readme",
					DocPath:   "bv-graph-wasm/README.md",
					Expected:  want,
					Observed:  "missing from bv-graph-wasm/README.md",
				}))
			}
		}
	})

	// 3. README.md ETA fallback (median/7, 60/7 ~= 8.57 min/day) matches live pkg/analysis/eta.go.
	t.Run("readme_eta_fallback_matches_live_eta_go", func(t *testing.T) {
		now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
		issues := []model.Issue{
			{ID: "ETA-1", Title: "No-history fallback task", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2},
		}
		stats := analysis.NewAnalyzer(issues).Analyze()
		eta, err := analysis.EstimateETAForIssue(issues, &stats, "ETA-1", 1, now)
		if err != nil {
			t.Fatalf("EstimateETAForIssue failed: %v", err)
		}
		wantVel := 60.0 / 7.0
		if math.Abs(eta.VelocityMinutesPerDay-wantVel) > 1e-6 {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "eta_no_history_single_task",
				DocPath:   "README.md",
				Expected:  fmt.Sprintf("velocity=%.6f (60/7)", wantVel),
				Observed:  fmt.Sprintf("velocity=%.6f", eta.VelocityMinutesPerDay),
			}))
		}
		checkETADoc := func(readmeText string) error {
			if strings.Contains(readmeText, "median/5") {
				return fmt.Errorf("README.md contains stale median/5 velocity fallback")
			}
			for _, want := range []string{"median/7", "60/7", "8.57 min/day"} {
				if !strings.Contains(readmeText, want) {
					return fmt.Errorf("README.md missing live ETA fallback clause %q", want)
				}
			}
			return nil
		}
		readme := repoFile(t, "README.md")
		if err := checkETADoc(readme); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "eta_no_history_single_task",
				DocPath:   "README.md",
				Expected:  "median/7 and 60/7 (~8.57 min/day)",
				Observed:  err.Error(),
			}))
		}
		staleReadme := strings.ReplaceAll(readme, "median/7", "median/5")
		if err := checkETADoc(staleReadme); err == nil {
			t.Fatal("negative control failed: stale median/5 README snippet unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_stale_eta_median_div_5",
				DocPath:   "README.md",
				Expected:  "stale median/5 rejected",
				Observed:  err.Error(),
			}))
		}
	})

	// 4. README.md --robot-label-flow critical_paths, --pages hooks, temporal hints, Freshness, BV_SEMANTIC_MODEL, BV_WORKER_LOG_LEVEL, Sprint & Time-Travel.
	t.Run("readme_runtime_prose_reconciliations", func(t *testing.T) {
		ws := t.TempDir()
		writeIssuesJSONL(t, ws, strings.Join([]string{
			`{"id":"LF-1","title":"Schema","status":"open","priority":1,"issue_type":"task","labels":["schema"]}`,
			`{"id":"LF-2","title":"API","status":"open","priority":1,"issue_type":"task","labels":["api"],"dependencies":[{"issue_id":"LF-2","depends_on_id":"LF-1","type":"blocks"}]}`,
			`{"id":"LF-3","title":"Web","status":"open","priority":2,"issue_type":"task","labels":["web"],"dependencies":[{"issue_id":"LF-3","depends_on_id":"LF-2","type":"blocks"}]}`,
		}, "\n")+"\n")
		cmd := exec.Command(bv, "--robot-label-flow")
		cmd.Dir = ws
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "label_flow_3_labels",
				DocPath:   "README.md",
				Argv:      cmd.Args,
				Expected:  "exit 0 with populated flow.critical_paths",
				Observed:  err.Error(),
				Stdout:    string(out),
				Stderr:    stderr.String(),
				ExitCode:  1,
			}))
		}
		var flowResp struct {
			Flow struct {
				CriticalPaths []any `json:"critical_paths"`
			} `json:"flow"`
		}
		if err := json.Unmarshal(out, &flowResp); err != nil || len(flowResp.Flow.CriticalPaths) == 0 {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "label_flow_3_labels",
				DocPath:   "README.md",
				Argv:      cmd.Args,
				Expected:  "len(flow.critical_paths) > 0",
				Observed:  fmt.Sprintf("len=%d err=%v", len(flowResp.Flow.CriticalPaths), err),
				Stdout:    string(out),
				Stderr:    stderr.String(),
			}))
		}

		checkReadmeProse := func(readmeText string) error {
			for _, stale := range []string{
				"critical_paths array is empty",
				"`critical_paths` array is empty",
				"label-to-path",
				"across all statuses",
				"ignored by hash",
				"ignored by `hash`",
				"wizard does not run hooks",
				"`--pages` wizard does not run hooks",
				"`Enter` jumps to the selected sprint issue",
				"| `0` | First column |",
				"| `$` | Last column |",
				"| `g` / `G` | Jump to first / last node |",
			} {
				if strings.Contains(strings.ToLower(readmeText), strings.ToLower(stale)) {
					return fmt.Errorf("README.md still contains stale claim %q", stale)
				}
			}
			for _, must := range []string{
				"`flow.critical_paths` (up to 10 cross-label paths)",
				"`extractPathHints`",
				"active non-closed, non-parked issues",
				"FNV-1a token seed and search index cache filename",
				"the interactive `--pages` wizard does run `.bv/hooks.yaml` unless `--no-hooks` is passed",
				"| `BV_WORKER_LOG_LEVEL` | Log level for the background snapshot worker (`none`, `error`, `warn`, `info`, `debug`, `trace`). | `none` |",
				"unified help overlay",
				"`[REOPENED]`",
				"`Enter` jumps to the first sprint bead",
				"| `0` | First item in column |",
				"| `$` | Last item in column |",
				"| `gg` / `G` | Jump to first / last node |",
			} {
				if !strings.Contains(readmeText, must) {
					return fmt.Errorf("README.md missing reconciled truth %q", must)
				}
			}
			return nil
		}
		readme := repoFile(t, "README.md")
		if err := checkReadmeProse(readme); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "readme_reconciled_prose",
				DocPath:   "README.md",
				Expected:  "all RC-C40 README reconciliations present and stale phrases absent",
				Observed:  err.Error(),
			}))
		}
		staleReadme := readme + "\nThe current critical_paths array is empty.\n"
		if err := checkReadmeProse(staleReadme); err == nil {
			t.Fatal("negative control failed: stale critical_paths prose unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_stale_critical_paths",
				DocPath:   "README.md",
				Expected:  "stale critical_paths claim rejected",
				Observed:  err.Error(),
			}))
		}
	})

	// 5. AGENTS.md zero raw os.Getenv("BV_*"/"BEADS_*") code examples + accurate stores/packages/jq.
	t.Run("agents_md_truth_and_no_raw_getenv_examples", func(t *testing.T) {
		rawGetenvRe := regexp.MustCompile(`os\.Getenv\("(?:BV_|BEADS_)`)
		checkAgentsDoc := func(agentsText string) error {
			for i, line := range strings.Split(agentsText, "\n") {
				if !rawGetenvRe.MatchString(line) {
					continue
				}
				// The rule sentence forbidding raw os.Getenv("BV_*") is allowed; code snippets calling it are forbidden.
				if strings.Contains(line, "never call raw") {
					continue
				}
				return fmt.Errorf("AGENTS.md:%d contains raw env example: %s", i+1, strings.TrimSpace(line))
			}
			for _, stale := range []string{
				"Git hooks integration",
				"Agent detection and session tracking",
			} {
				if strings.Contains(agentsText, stale) {
					return fmt.Errorf("AGENTS.md still contains stale package description %q", stale)
				}
			}
			for _, must := range []string{
				"env.NoBrowser.IsSet() || env.TestMode.IsSet()",
				"Export lifecycle hooks",
				"AGENTS.md blurb detection, verification, and injection",
				"`.beads/beads.db` SQLite and `.beads/issues.jsonl`",
				"{status: .status.Cycles, cycles: .Cycles}",
			} {
				if !strings.Contains(agentsText, must) {
					return fmt.Errorf("AGENTS.md missing reconciled truth %q", must)
				}
			}
			return nil
		}
		agentsDoc := repoFile(t, "AGENTS.md")
		if err := checkAgentsDoc(agentsDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "live_agents_md",
				DocPath:   "AGENTS.md",
				Expected:  "zero raw os.Getenv code examples and accurate descriptions",
				Observed:  err.Error(),
			}))
		}
		staleAgents := strings.ReplaceAll(agentsDoc, "env.NoBrowser.IsSet() || env.TestMode.IsSet()", `os.Getenv("BV_NO_BROWSER") != "" || os.Getenv("BV_TEST_MODE") != ""`)
		if err := checkAgentsDoc(staleAgents); err == nil {
			t.Fatal("negative control failed: raw os.Getenv(\"BV_NO_BROWSER\") example in AGENTS.md unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_raw_getenv_in_agents_md",
				DocPath:   "AGENTS.md",
				Expected:  "raw os.Getenv(\"BV_NO_BROWSER\") example rejected",
				Observed:  err.Error(),
			}))
		}
	})

	// 6. docs/{performance,RELEASING,testing}.md and CHANGELOG.md parity with live scripts.
	t.Run("docs_perf_releasing_testing_and_changelog_parity", func(t *testing.T) {
		checkPerfDoc := func(perfText string) error {
			if strings.Contains(perfText, "and the P1 implementation and proof beads remain open.") {
				return fmt.Errorf("docs/performance.md still claims closed beads bv-apal.1/.2 remain open")
			}
			if !strings.Contains(perfText, "`bv-apal.1` and `bv-apal.2` subsequently completed") || !strings.Contains(perfText, "2026-09-19") {
				return fmt.Errorf("docs/performance.md missing truthful 2026-09-19 closure update for bv-apal.1/.2")
			}
			return nil
		}
		perfDoc := repoFile(t, "docs/performance.md")
		if err := checkPerfDoc(perfDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "docs_performance_closed_beads",
				DocPath:   "docs/performance.md",
				Expected:  "truthful historical + 2026-09-19 closure status for bv-apal.1/.2",
				Observed:  err.Error(),
			}))
		}
		if err := checkPerfDoc("The remaining CLI evidence cannot be replaced by the UI pass, and the P1 implementation and proof beads remain open."); err == nil {
			t.Fatal("negative control failed: stale docs/performance.md open-bead claim unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_stale_perf_open_beads",
				DocPath:   "docs/performance.md",
				Expected:  "stale open-bead claim rejected",
				Observed:  err.Error(),
			}))
		}

		checkReleasingDoc := func(relText string) error {
			for _, must := range []string{
				"scripts/verify_vendor.sh --source",
				"all 47 registered robot commands",
				"tests/scripts/graph_wasm_test.sh",
				"Go 1.26.11",
			} {
				if !strings.Contains(relText, must) {
					return fmt.Errorf("docs/RELEASING.md missing %q", must)
				}
			}
			return nil
		}
		releasingDoc := repoFile(t, "docs/RELEASING.md")
		if err := checkReleasingDoc(releasingDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "docs_releasing_stage7_and_stage10",
				DocPath:   "docs/RELEASING.md",
				Expected:  "verify_vendor.sh --source, 47 robot commands, graph_wasm_test.sh, Go 1.26.11",
				Observed:  err.Error(),
			}))
		}
		if err := checkReleasingDoc(strings.ReplaceAll(releasingDoc, "scripts/verify_vendor.sh --source", "scripts/verify_vendor.sh")); err == nil {
			t.Fatal("negative control failed: docs/RELEASING.md omitting --source unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_missing_verify_vendor_source_flag",
				DocPath:   "docs/RELEASING.md",
				Expected:  "missing --source flag rejected",
				Observed:  err.Error(),
			}))
		}

		// Verify docs/testing.md timeout matches scripts/release_gate.sh and thresholds match scripts/coverage.sh.
		gateScript := repoFile(t, "scripts/release_gate.sh")
		if !strings.Contains(gateScript, "-timeout=30m") {
			t.Fatalf("scripts/release_gate.sh no longer uses -timeout=30m; update docs/testing.md and this parity check together")
		}
		covScript := repoFile(t, "scripts/coverage.sh")
		covThresholds := make(map[string]string)
		for _, row := range regexp.MustCompile(`github\.com/Dicklesworthstone/beads_viewer/(pkg/[a-z_]+)\) echo ([0-9]+) ;;`).FindAllStringSubmatch(covScript, -1) {
			covThresholds[row[1]] = row[2]
		}
		checkTestingDoc := func(testDocText string) error {
			if strings.Contains(testDocText, "-timeout=45m") {
				return fmt.Errorf("docs/testing.md still contains stale -timeout=45m")
			}
			if !strings.Contains(testDocText, "go test ./tests/e2e -timeout=30m") {
				return fmt.Errorf("docs/testing.md missing -timeout=30m matching scripts/release_gate.sh")
			}
			docThresholds := make(map[string]string)
			for _, row := range regexp.MustCompile("(?m)^\\| `(pkg/[a-z_]+)` \\| ([0-9]+)% \\|$").FindAllStringSubmatch(testDocText, -1) {
				docThresholds[row[1]] = row[2]
			}
			if len(covThresholds) < 8 || !reflect.DeepEqual(docThresholds, covThresholds) {
				return fmt.Errorf("docs/testing.md coverage thresholds %v != scripts/coverage.sh %v", docThresholds, covThresholds)
			}
			return nil
		}
		testingDoc := repoFile(t, "docs/testing.md")
		if err := checkTestingDoc(testingDoc); err != nil {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "docs_testing_timeout_and_coverage",
				DocPath:   "docs/testing.md",
				Expected:  "-timeout=30m and exact match with scripts/coverage.sh thresholds",
				Observed:  err.Error(),
			}))
		}
		if err := checkTestingDoc(strings.ReplaceAll(testingDoc, "-timeout=30m", "-timeout=45m")); err == nil {
			t.Fatal("negative control failed: stale -timeout=45m in docs/testing.md unexpectedly passed")
		} else {
			t.Logf("negative_control_pass %s", formatLog(rc40AuditLog{
				FixtureID: "planted_stale_testing_timeout_45m",
				DocPath:   "docs/testing.md",
				Expected:  "stale -timeout=45m rejected",
				Observed:  err.Error(),
			}))
		}

		changelog := repoFile(t, "CHANGELOG.md")
		if !strings.Contains(changelog, "### Wave 18–21 reality-check closure") {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "changelog_wave18_21",
				DocPath:   "CHANGELOG.md",
				Expected:  "Wave 18–21 reality-check closure section under Unreleased",
				Observed:  "missing",
			}))
		}
	})

	// 7. Live TUI runtime verification of documented Sprint Ctrl+d/Ctrl+u/Enter, Board 0/$, and Tree gg keys.
	t.Run("sprint_board_tree_documented_keys_match_runtime", func(t *testing.T) {
		now := time.Now().UTC()
		issues := make([]model.Issue, 0, 25)
		beadIDs := make([]string, 0, 25)
		for i := 0; i < 25; i++ {
			id := fmt.Sprintf("SP-%02d", i+1)
			beadIDs = append(beadIDs, id)
			issues = append(issues, model.Issue{
				ID:        id,
				Title:     fmt.Sprintf("Sprint issue %02d for scroll verification", i+1),
				Status:    model.StatusOpen,
				Priority:  2,
				IssueType: model.TypeTask,
			})
		}
		sprints := []model.Sprint{{
			ID:        "sprint-active",
			Name:      "Active Sprint",
			StartDate: now.Add(-24 * time.Hour),
			EndDate:   now.Add(6 * 24 * time.Hour),
			BeadIDs:   beadIDs,
		}}
		m := ui.NewModel(issues, nil, "")
		t.Cleanup(m.Stop)
		m.SetSprints(sprints)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
		m = updated.(*ui.Model)

		// Open Sprint Dashboard with P, scroll down with ctrl+d, scroll back up with ctrl+u.
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
		m = updated.(*ui.Model)
		viewTop := m.View()
		if !strings.Contains(viewTop, "Active Sprint") {
			t.Fatalf("pressing P did not open Sprint Dashboard; view=%q", viewTop)
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		m = updated.(*ui.Model)
		viewScrolled := m.View()
		if viewScrolled == viewTop {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "sprint_dashboard_ctrl_d_scroll",
				DocPath:   "README.md",
				Expected:  "ctrl+d advances Sprint Dashboard viewport",
				Observed:  "Sprint Dashboard view unchanged after ctrl+d",
			}))
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		m = updated.(*ui.Model)
		if m.View() != viewTop {
			t.Fatalf("ctrl+u did not restore Sprint Dashboard top view")
		}

		// Press Enter in Sprint Dashboard: must exit Sprint view and jump to first sprint bead SP-01.
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(*ui.Model)
		afterSprintEnter := m.View()
		if !strings.Contains(afterSprintEnter, "SP-01") || strings.Contains(afterSprintEnter, "Beads in Sprint:") {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "sprint_dashboard_enter_jump",
				DocPath:   "README.md",
				Expected:  "Enter exits Sprint Dashboard and jumps to first sprint bead SP-01",
				Observed:  "Sprint Enter did not jump to SP-01 detail view",
			}))
		}
		// Return to list before Board and Tree checks.
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(*ui.Model)

		// Verify Board 0/$ move within column (not switching columns).
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
		m = updated.(*ui.Model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'$'}})
		m = updated.(*ui.Model)
		boardBottom := m.View()
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
		m = updated.(*ui.Model)
		boardTop := m.View()
		if boardBottom == boardTop {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "board_0_dollar_column_jump",
				DocPath:   "pkg/ui/keybindings.go",
				Expected:  "$ and 0 jump to last/first item in Open column",
				Observed:  "board view identical after $ and 0",
			}))
		}

		// Return to list, open Tree view with E, jump to bottom with G, then back to top with gg.
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = updated.(*ui.Model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
		m = updated.(*ui.Model)
		treeTopInitial := m.View()
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
		m = updated.(*ui.Model)
		treeBottom := m.View()
		if treeBottom == treeTopInitial {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "tree_G_bottom_jump",
				DocPath:   "README.md",
				Expected:  "G jumps to last node in Tree view",
				Observed:  "tree view identical after G",
			}))
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		m = updated.(*ui.Model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
		m = updated.(*ui.Model)
		treeTopAfterGG := m.View()
		if treeTopAfterGG != treeTopInitial {
			t.Fatalf("%s", formatLog(rc40AuditLog{
				FixtureID: "tree_gg_top_jump",
				DocPath:   "README.md",
				Expected:  "gg jumps back to first node in Tree view",
				Observed:  "tree view after gg did not match initial top view",
			}))
		}
	})
}

// TestDocsParity_SortModesRecipeMetricsAndPostV028Examples verifies that the
// README sort modes table, recipe presentation metrics list, Robot Protocol
// Commands summary table, and post-v0.28.0 CLI examples (--robot-check-dep and
// batch --robot-whatif) match live code with planted negative controls.
func TestDocsParity_SortModesRecipeMetricsAndPostV028Examples(t *testing.T) {
	readme := repoFile(t, "README.md")

	t.Run("sort_modes_table_and_prose_match_ui", func(t *testing.T) {
		checkSortModes := func(doc string) error {
			if strings.Contains(doc, "five distinct sort modes") {
				return fmt.Errorf("README.md still claims five distinct sort modes")
			}
			startMarker := "<!-- bv:generated:sort-modes -->"
			startIdx := strings.Index(doc, startMarker)
			if startIdx < 0 {
				return fmt.Errorf("README.md missing %s marker", startMarker)
			}
			rest := doc[startIdx:]
			endIdx := strings.Index(rest, "<!-- /bv:generated -->")
			if endIdx < 0 {
				return fmt.Errorf("README.md missing closing generated marker for sort-modes")
			}
			section := rest[:endIdx]
			for _, mode := range ui.GetSortModeDocs() {
				if !strings.Contains(section, "`"+mode.KeyDisplay+"`") {
					return fmt.Errorf("README.md sort modes table missing %q", mode.KeyDisplay)
				}
			}
			return nil
		}
		if err := checkSortModes(readme); err != nil {
			t.Fatal(err)
		}
		stale := strings.ReplaceAll(readme, "nine distinct sort modes", "five distinct sort modes")
		if err := checkSortModes(stale); err == nil {
			t.Fatal("negative control failed: stale five-mode prose unexpectedly passed")
		}
		missingRisk := strings.ReplaceAll(readme, "`Risk ↓`", "`Removed`")
		if err := checkSortModes(missingRisk); err == nil {
			t.Fatal("negative control failed: missing Risk sort mode unexpectedly passed")
		}
	})

	t.Run("recipe_view_metrics_documented_in_readme", func(t *testing.T) {
		checkMetrics := func(doc string) error {
			idx := strings.Index(doc, "| `metrics` |")
			if idx < 0 {
				return fmt.Errorf("README.md missing recipe `metrics` documentation row")
			}
			end := strings.Index(doc[idx:], "\n")
			row := doc[idx:]
			if end != -1 {
				row = doc[idx : idx+end]
			}
			for _, metric := range recipe.ViewMetrics {
				if !strings.Contains(row, "`"+metric+"`") {
					return fmt.Errorf("README.md recipe metrics row missing ViewMetrics entry %q: %s", metric, row)
				}
			}
			return nil
		}
		if err := checkMetrics(readme); err != nil {
			t.Fatal(err)
		}
		stale := strings.Replace(readme, ", `risk`, `eta`.", ".", 1)
		if err := checkMetrics(stale); err == nil {
			t.Fatal("negative control failed: README recipe metrics row missing risk/eta unexpectedly passed")
		}
	})

	t.Run("robot_summary_table_and_live_check_dep_and_batch_whatif", func(t *testing.T) {
		start := strings.Index(readme, "### Robot Protocol Commands")
		end := strings.Index(readme, "Issue-backed analysis commands support `--as-of <ref>`")
		if start < 0 || end <= start {
			t.Fatal("could not locate README Robot Protocol Commands summary table")
		}
		summaryTable := readme[start:end]
		if !strings.Contains(summaryTable, "`--robot-check-dep") {
			t.Fatalf("README Robot Protocol Commands summary table missing --robot-check-dep")
		}
		if !strings.Contains(summaryTable, "`--robot-whatif <id>[,id2,...]`") {
			t.Fatalf("README Robot Protocol Commands summary table missing batch --robot-whatif syntax")
		}

		bv := buildBvBinary(t)
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		fixture := strings.Join([]string{
			`{"id":"A","title":"Root A","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
			`{"id":"B","title":"Root B","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
			`{"id":"C","title":"Diamond child","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"C","depends_on_id":"A","type":"blocks"},{"issue_id":"C","depends_on_id":"B","type":"blocks"}],"created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}

		checkCtx, checkCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer checkCancel()
		checkCmd := exec.CommandContext(checkCtx, bv, "--robot-check-dep", "A:C,A:B")
		checkCmd.Dir = dir
		checkCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "SOURCE_DATE_EPOCH=1788220800")
		var checkErr bytes.Buffer
		checkCmd.Stderr = &checkErr
		checkOut, err := checkCmd.Output()
		if err != nil {
			t.Fatalf("--robot-check-dep A:C,A:B failed: %v\nstderr: %s", err, checkErr.String())
		}
		var checkPayload struct {
			AllSafe      bool `json:"all_safe"`
			CheckedCount int  `json:"checked_count"`
			CycleCount   int  `json:"cycle_count"`
			Results      []struct {
				FromID     string `json:"from_id"`
				ToID       string `json:"to_id"`
				CanAdd     bool   `json:"can_add"`
				WouldCycle bool   `json:"would_cycle"`
			} `json:"results"`
		}
		if err := json.Unmarshal(checkOut, &checkPayload); err != nil {
			t.Fatalf("decode --robot-check-dep output: %v\n%s", err, checkOut)
		}
		if checkPayload.CheckedCount != 2 || checkPayload.CycleCount != 1 || checkPayload.AllSafe {
			t.Fatalf("unexpected --robot-check-dep summary: %+v", checkPayload)
		}
		if len(checkPayload.Results) != 2 || !checkPayload.Results[0].WouldCycle || checkPayload.Results[0].CanAdd || checkPayload.Results[1].WouldCycle || !checkPayload.Results[1].CanAdd {
			t.Fatalf("unexpected --robot-check-dep results: %+v", checkPayload.Results)
		}

		whatIfCtx, whatIfCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer whatIfCancel()
		whatIfCmd := exec.CommandContext(whatIfCtx, bv, "--robot-whatif", "A,B")
		whatIfCmd.Dir = dir
		whatIfCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "SOURCE_DATE_EPOCH=1788220800")
		var whatIfErr bytes.Buffer
		whatIfCmd.Stderr = &whatIfErr
		whatIfOut, err := whatIfCmd.Output()
		if err != nil {
			t.Fatalf("--robot-whatif A,B failed: %v\nstderr: %s", err, whatIfErr.String())
		}
		var batchPayload struct {
			BatchResult struct {
				SynergyUnblocks int      `json:"synergy_unblocks"`
				SynergyIssueIDs []string `json:"synergy_issue_ids"`
			} `json:"batch_result"`
		}
		if err := json.Unmarshal(whatIfOut, &batchPayload); err != nil {
			t.Fatalf("decode --robot-whatif A,B output: %v\n%s", err, whatIfOut)
		}
		if batchPayload.BatchResult.SynergyUnblocks != 1 || len(batchPayload.BatchResult.SynergyIssueIDs) != 1 || batchPayload.BatchResult.SynergyIssueIDs[0] != "C" {
			t.Fatalf("unexpected batch what-if synergy: %+v", batchPayload.BatchResult)
		}

		badCtx, badCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer badCancel()
		badCmd := exec.CommandContext(badCtx, bv, "--robot-whatif", "A,MISSING-999")
		badCmd.Dir = dir
		badCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "SOURCE_DATE_EPOCH=1788220800")
		if badOut, err := badCmd.CombinedOutput(); err == nil {
			t.Fatalf("negative control failed: --robot-whatif A,MISSING-999 exited 0:\n%s", badOut)
		}
	})
}

// TestDocsParity_Wave22_RC_C43_ContractAndArtifactParity adversarially proves RC-C43:
//  1. Live `--robot-docs commands` and `--robot-capabilities` advertise `remove:` syntax and
//     all 8 removal `key_fields` on `robot-check-dep`, plus `high_risk` and `cross_repo_flow`
//     on `robot-insights` (with planted negative controls rejecting omission).
//  2. `README.md`, `SKILL.md`, and `docs/generated/flags.md` agree on `--robot-check-dep`
//     add + `remove:` syntax (with planted negative controls rejecting stale add-only text).
//  3. `tests/artifacts/perf/pages_load.json` satisfies WS-25 size caps + positive Chromium
//     `first_render_ms`, and `README.md` rejects stale `367 KB` / `20.6 MB` Nov 21 prose.
//  4. `CHANGELOG.md` `Unreleased` records every post-`09f6df4e` commit plus Wave 22 work
//     (with planted negative controls rejecting omission).
//  5. Live `--robot-check-dep remove:C:A` returns exact removal semantics (`operation`,
//     `can_remove`, `exists`).
func TestDocsParity_Wave22_RC_C43_ContractAndArtifactParity(t *testing.T) {
	bv := buildBvBinary(t)
	readme := repoFile(t, "README.md")
	skill := repoFile(t, "SKILL.md")
	flagsMD := repoFile(t, filepath.Join("docs", "generated", "flags.md"))
	changelog := repoFile(t, "CHANGELOG.md")
	pagesLoadRaw := repoFile(t, filepath.Join("tests", "artifacts", "perf", "pages_load.json"))

	// 1. Live --robot-docs commands & --robot-capabilities 4D contract check + negative controls.
	t.Run("robot_docs_and_capabilities_4d_contract", func(t *testing.T) {
		wantCheckDepFields := []string{
			"results[].operation",
			"results[].can_remove",
			"results[].exists",
			"results[].cycles_before",
			"results[].cycles_after",
			"results[].cycles_broken",
			"results[].would_break_all_cycles",
			"results[].newly_unblocked",
		}
		wantInsightsFields := []string{"high_risk", "cross_repo_flow"}

		validateContract := func(checkDepFlag string, checkDepFields, insightsFields []string) error {
			if !strings.Contains(checkDepFlag, "remove:") {
				return fmt.Errorf("robot-check-dep flag %q missing remove: syntax", checkDepFlag)
			}
			cdSet := make(map[string]bool, len(checkDepFields))
			for _, f := range checkDepFields {
				cdSet[f] = true
			}
			for _, req := range wantCheckDepFields {
				if !cdSet[req] {
					return fmt.Errorf("robot-check-dep key_fields missing %q", req)
				}
			}
			inSet := make(map[string]bool, len(insightsFields))
			for _, f := range insightsFields {
				inSet[f] = true
			}
			for _, req := range wantInsightsFields {
				if !inSet[req] {
					return fmt.Errorf("robot-insights key_fields missing %q", req)
				}
			}
			return nil
		}

		// Live --robot-docs commands
		docsCmd := exec.Command(bv, "--robot-docs", "commands")
		docsCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1")
		docsOut, err := docsCmd.Output()
		if err != nil {
			t.Fatalf("--robot-docs commands failed: %v", err)
		}
		var docsPayload struct {
			Commands map[string]struct {
				Flag      string   `json:"flag"`
				KeyFields []string `json:"key_fields"`
			} `json:"commands"`
		}
		if err := json.Unmarshal(docsOut, &docsPayload); err != nil {
			t.Fatalf("decode --robot-docs commands: %v", err)
		}
		cdDoc := docsPayload.Commands["robot-check-dep"]
		inDoc := docsPayload.Commands["robot-insights"]
		if err := validateContract(cdDoc.Flag, cdDoc.KeyFields, inDoc.KeyFields); err != nil {
			t.Fatalf("--robot-docs commands 4D contract failed: %v", err)
		}

		// Live --robot-capabilities
		capsCmd := exec.Command(bv, "--robot-capabilities")
		capsCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1")
		capsOut, err := capsCmd.Output()
		if err != nil {
			t.Fatalf("--robot-capabilities failed: %v", err)
		}
		var capsPayload struct {
			Commands []struct {
				Name      string   `json:"name"`
				Flag      string   `json:"flag"`
				KeyFields []string `json:"key_fields"`
			} `json:"commands"`
		}
		if err := json.Unmarshal(capsOut, &capsPayload); err != nil {
			t.Fatalf("decode --robot-capabilities: %v", err)
		}
		var capCDFlag string
		var capCDFields, capInFields []string
		for _, c := range capsPayload.Commands {
			switch c.Name {
			case "robot-check-dep":
				capCDFlag, capCDFields = c.Flag, c.KeyFields
			case "robot-insights":
				capInFields = c.KeyFields
			}
		}
		if err := validateContract(capCDFlag, capCDFields, capInFields); err != nil {
			t.Fatalf("--robot-capabilities 4D contract failed: %v", err)
		}

		// Planted negative controls: dropping removal fields or cross_repo_flow MUST fail.
		if err := validateContract("--robot-check-dep FROM_ID:TO_ID[,...]", cdDoc.KeyFields, inDoc.KeyFields); err == nil {
			t.Fatal("negative control failed: stale add-only robot-check-dep flag passed")
		}
		if err := validateContract(cdDoc.Flag, []string{"results[].can_add"}, inDoc.KeyFields); err == nil {
			t.Fatal("negative control failed: robot-check-dep missing removal key_fields passed")
		}
		if err := validateContract(cdDoc.Flag, cdDoc.KeyFields, []string{"full_stats", "Cycles"}); err == nil {
			t.Fatal("negative control failed: robot-insights missing high_risk/cross_repo_flow passed")
		}
	})

	// 2. Cross-doc README/SKILL/flags.md check-dep remove: parity + planted negative controls.
	t.Run("cross_doc_check_dep_remove_syntax", func(t *testing.T) {
		checkDocs := func(r, s, f string) error {
			start := strings.Index(r, "### Robot Protocol Commands")
			end := strings.Index(r, "Issue-backed analysis commands support `--as-of <ref>`")
			if start < 0 || end <= start {
				return fmt.Errorf("missing README Robot Protocol Commands table")
			}
			summary := r[start:end]
			if !strings.Contains(summary, "`--robot-check-dep <[remove:]from:to>[,...]`") {
				return fmt.Errorf("README summary table missing [remove:]from:to syntax")
			}
			if !strings.Contains(s, "bv --robot-check-dep [remove:]<from:to>") {
				return fmt.Errorf("SKILL.md missing [remove:]<from:to> syntax")
			}
			if !strings.Contains(f, "remove:fromID:toID") {
				return fmt.Errorf("docs/generated/flags.md missing remove:fromID:toID")
			}
			return nil
		}
		if err := checkDocs(readme, skill, flagsMD); err != nil {
			t.Fatal(err)
		}
		staleReadme := strings.Replace(readme, "`--robot-check-dep <[remove:]from:to>[,...]`", "`--robot-check-dep <from:to>[,...]`", 1)
		if err := checkDocs(staleReadme, skill, flagsMD); err == nil {
			t.Fatal("negative control failed: stale README check-dep syntax passed")
		}
		staleSkill := strings.Replace(skill, "bv --robot-check-dep [remove:]<from:to>", "bv --robot-check-dep <from:to>", 1)
		if err := checkDocs(readme, staleSkill, flagsMD); err == nil {
			t.Fatal("negative control failed: stale SKILL.md check-dep syntax passed")
		}
	})

	// 3. Refreshed pages_load.json caps + real Chromium first_render_ms + stale Nov 21 negative control.
	t.Run("pages_load_refreshed_and_rejects_stale_nov21", func(t *testing.T) {
		var pl struct {
			FirstRenderMS *float64 `json:"first_render_ms"`
			Bytes         struct {
				BundleTotal int64 `json:"bundle_total"`
				HistoryJSON int64 `json:"history_json"`
				LabelsJSON  int64 `json:"labels_json"`
			} `json:"bytes"`
		}
		if err := json.Unmarshal([]byte(pagesLoadRaw), &pl); err != nil {
			t.Fatalf("decode pages_load.json: %v", err)
		}
		if pl.FirstRenderMS == nil || *pl.FirstRenderMS <= 0 {
			t.Fatalf("pages_load.json must record positive numeric Chromium first_render_ms, got %v", pl.FirstRenderMS)
		}
		if pl.Bytes.BundleTotal > 23_000_000 || pl.Bytes.HistoryJSON >= 1_200_000 || pl.Bytes.LabelsJSON > 650_000 {
			t.Fatalf("pages_load.json violates WS-25 caps: %+v", pl.Bytes)
		}
		checkFreshReadme := func(r string) error {
			for _, staleTok := range []string{"(367 KB)", "whole bundle 20.6 MB", "retained November 21, 2026 export"} {
				if strings.Contains(r, staleTok) {
					return fmt.Errorf("README retains stale Nov 21 pages_load token %q", staleTok)
				}
			}
			if !strings.Contains(r, fmt.Sprintf("%.1f ms", *pl.FirstRenderMS)) {
				return fmt.Errorf("README missing recorded first_render_ms %.1f ms", *pl.FirstRenderMS)
			}
			return nil
		}
		if err := checkFreshReadme(readme); err != nil {
			t.Fatal(err)
		}
		if err := checkFreshReadme("whole bundle 20.6 MB (367 KB) retained November 21, 2026 export"); err == nil {
			t.Fatal("negative control failed: stale Nov 21 README prose passed")
		}
	})

	// 4. CHANGELOG.md Unreleased coverage of all 8 post-09f6df4e commits + Wave 22 + negative control.
	t.Run("changelog_unreleased_post_09f6df4e_coverage", func(t *testing.T) {
		start := strings.Index(changelog, "## Unreleased")
		end := strings.Index(changelog, "## v0.28.0")
		if start < 0 || end <= start {
			t.Fatal("could not locate CHANGELOG.md Unreleased section")
		}
		unreleased := changelog[start:end]
		required := []string{
			"47d98f9a", "bc91ba87", "6eb74b23", "2c44321f",
			"390fb2a1", "6bd8b659", "69c274e1", "938540a8",
			"commit_catalog", "createDefaultFilters()", "bv-apal.92", "bv-oonu.26", "bv-oonu.28",
		}
		checkChangelog := func(sec string) error {
			for _, tok := range required {
				if !strings.Contains(sec, tok) {
					return fmt.Errorf("CHANGELOG.md Unreleased missing %q", tok)
				}
			}
			return nil
		}
		if err := checkChangelog(unreleased); err != nil {
			t.Fatal(err)
		}
		staleCL := strings.ReplaceAll(unreleased, "69c274e1", "00000000")
		if err := checkChangelog(staleCL); err == nil {
			t.Fatal("negative control failed: CHANGELOG.md missing 69c274e1 passed")
		}
	})

	// 5. Live --robot-check-dep remove:C:A end-to-end verification.
	t.Run("live_robot_check_dep_remove", func(t *testing.T) {
		dir := t.TempDir()
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		fixture := strings.Join([]string{
			`{"id":"A","title":"Root A","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
			`{"id":"C","title":"Blocked child","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"C","depends_on_id":"A","type":"blocks"}],"created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bv, "--robot-check-dep", "remove:C:A")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("--robot-check-dep remove:C:A failed: %v", err)
		}
		var payload struct {
			Results []struct {
				Operation      string   `json:"operation"`
				Exists         bool     `json:"exists"`
				CanRemove      bool     `json:"can_remove"`
				NewlyUnblocked []string `json:"newly_unblocked"`
			} `json:"results"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("decode --robot-check-dep remove:C:A: %v\n%s", err, string(out))
		}
		if len(payload.Results) != 1 || payload.Results[0].Operation != "remove" || !payload.Results[0].Exists || !payload.Results[0].CanRemove || len(payload.Results[0].NewlyUnblocked) != 1 || payload.Results[0].NewlyUnblocked[0] != "C" {
			t.Fatalf("unexpected --robot-check-dep remove:C:A payload: %+v", payload)
		}
	})
}
