package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
			foundBridge := false
			for _, entry := range rows {
				row, ok := entry.(map[string]any)
				if !ok {
					return false
				}
				confidence, ok := row["confidence"].(float64)
				if !ok || confidence <= 0.6 {
					return false
				}
				if row["issue_id"] == "MID" {
					foundBridge = true
				}
			}
			return foundBridge
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
			// Recommendations rank impact, while quick_ref above selects ready
			// work. This is the documented "not necessarily claimable" case.
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
	}{{"explicit", 280, 17.5}, {"median", 421, 26.3125}} {
		// Values are independently worked from base 120/180, feature 1.3,
		// depth 2 => 1.2, 1000 runes => 1.5; integer minutes / (8 * 2).
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
		if stats.GetCriticalPathScore(tc.id) != 2 || minutes != tc.minutes || days != tc.days ||
			eta.EstimatedMinutes != minutes || eta.EstimatedDays != days || eta.VelocityMinutesPerDay != 8 {
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

// TestReadme_NoUnpinnedPipedInstallers (G1): the README must never tell
// users to pipe a script from the moving main branch into a shell; every
// raw.githubusercontent.com installer URL has to name a commit SHA.
func TestReadme_NoUnpinnedPipedInstallers(t *testing.T) {
	readme := repoFile(t, "README.md")
	if strings.Contains(readme, "beads_viewer/main/install") {
		t.Fatalf("README pipes an installer from the moving main branch; pin it to a commit SHA")
	}
	pinned := regexp.MustCompile(`raw\.githubusercontent\.com/Dicklesworthstone/beads_viewer/([0-9a-f]{40})/install\.(sh|ps1)`)
	if len(pinned.FindAllString(readme, -1)) < 2 {
		t.Fatalf("expected the install.sh and install.ps1 examples to be pinned to a 40-hex commit")
	}
}

// TestDocsParity_NoStaleBehaviourPhrases (F4): wording that once described
// behaviour the code does not have must not come back.
func TestDocsParity_NoStaleBehaviourPhrases(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, stale := range []string{
		"hooks are opt-in",    // hooks run whenever .bv/hooks.yaml exists; --no-hooks is the opt-out
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
