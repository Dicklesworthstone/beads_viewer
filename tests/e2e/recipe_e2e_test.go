package main_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
)

// recipeProject writes a small project whose issues carry a "sprint" label
// on some items, plus a project recipe file under .beads/recipes that keeps
// only open sprint work. Returns the project dir.
func recipeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, strings.Join([]string{
		`{"id":"SP-1","title":"Sprint root","status":"open","priority":1,"issue_type":"task","labels":["sprint"]}`,
		`{"id":"SP-2","title":"Sprint follow-up","status":"open","priority":2,"issue_type":"task","labels":["sprint"],"dependencies":[{"issue_id":"SP-2","depends_on_id":"SP-1","type":"blocks"}]}`,
		`{"id":"BL-1","title":"Backlog item","status":"open","priority":0,"issue_type":"task","labels":["backlog"]}`,
		`{"id":"SP-9","title":"Sprint done","status":"closed","priority":1,"issue_type":"task","labels":["sprint"]}`,
	}, "\n")+"\n")
	writeRecipeFile(t, dir, "sprint.yaml", `description: "Open sprint work"
filters:
  status: [open, in_progress]
  tags: [sprint]
sort:
  field: priority
  secondary:
    field: id
`)
	return dir
}

func writeRecipeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	recipesDir := filepath.Join(dir, ".beads", "recipes")
	if err := os.MkdirAll(recipesDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", recipesDir, err)
	}
	path := filepath.Join(recipesDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// runBVSplit runs bv and returns stdout and stderr separately along with the
// exit error, for tests that must inspect diagnostics on both outcomes.
func runBVSplit(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(buildBvBinary(t), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "TERM=dumb")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type recipePlanPayload struct {
	DataHash string `json:"data_hash"`
	Plan     struct {
		Tracks []struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"tracks"`
		TotalActionable int `json:"total_actionable"`
		TotalBlocked    int `json:"total_blocked"`
	} `json:"plan"`
}

func planIssueIDs(p recipePlanPayload) map[string]bool {
	ids := make(map[string]bool)
	for _, track := range p.Plan.Tracks {
		for _, item := range track.Items {
			ids[item.ID] = true
		}
	}
	return ids
}

// Existing explicit Markdown export must consume recipe membership and graph
// defaults. This control also runs against the pre-wiring CLI unchanged.
func TestRecipeExportSelectedMarkdownControl(t *testing.T) {
	dir := recipeProject(t)
	writeRecipeFile(t, dir, "report.yaml", `filters:
  status: [open]
  tags: [sprint]
sort:
  field: priority
  direction: desc
view:
  max_items: 1
export:
  format: json
  include_graph: false
`)
	path := filepath.Join(dir, "selected.md")
	stdout, stderr, err := runBVSplit(t, dir, "--recipe", "report", "--export-md", path, "--no-hooks")
	if err != nil {
		t.Fatalf("explicit Markdown recipe export failed: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.HasPrefix(content, "# ") || !strings.Contains(content, "Sprint follow-up") {
		t.Errorf("explicit Markdown format or selected positive row missing:\n%s", content)
	}
	for _, unwanted := range []string{"Sprint root", "Backlog item", "Sprint done", "```mermaid"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("recipe selection/graph=false ignored: unwanted %q in report:\n%s", unwanted, content)
		}
	}
}

func TestRecipeReportFormatsAndOverrides(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1788220800")
	for _, format := range []string{"markdown", "json", "csv", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			dir := recipeProject(t)
			writeRecipeFile(t, dir, "report.yaml", "filters:\n  status: [open]\n  tags: [sprint]\nsort:\n  field: priority\n  direction: desc\nview:\n  max_items: 1\nexport:\n  format: "+format+"\n")
			path := filepath.Join(dir, "report.out")
			args := []string{"--recipe", "report", "--export", path, "--no-hooks"}
			stdout, stderr, err := runBVSplit(t, dir, args...)
			if err != nil {
				t.Fatalf("export %s: %v\nstdout=%s\nstderr=%s", format, err, stdout, stderr)
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			repeatedPath := filepath.Join(dir, "report-again.out")
			stdout, stderr, err = runBVSplit(t, dir, "--recipe", "report", "--export", repeatedPath, "--no-hooks")
			if err != nil {
				t.Fatalf("repeat: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			second, err := os.ReadFile(repeatedPath)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("fixed-clock report changed: %v\nfirst=%s\nsecond=%s", err, first, second)
			}
			if strings.Contains(string(first), "Backlog item") || strings.Contains(string(first), "Sprint done") {
				t.Fatalf("excluded bodies/graph leaked: %s", first)
			}
			switch format {
			case "json":
				var result struct {
					Issues []struct {
						ID string `json:"id"`
					} `json:"issues"`
					Graph           struct{ Nodes, Edges int } `json:"graph"`
					SourceAuthority struct {
						State                  string `json:"state"`
						Loaded, Valid, Visible int
					} `json:"source_authority"`
					SourcePath    string `json:"source_path"`
					DataHash      string `json:"data_hash"`
					AuthorityHash string `json:"authority_hash"`
				}
				if err := json.Unmarshal(first, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Issues) != 1 || result.Issues[0].ID != "SP-2" || result.Graph.Nodes != 2 || result.Graph.Edges != 1 {
					t.Fatalf("body selection vs context graph mismatch: %s", first)
				}
				if result.SourceAuthority.State != "complete" || result.SourceAuthority.Loaded != 1 || result.SourceAuthority.Valid != 4 || result.SourceAuthority.Visible != 4 || result.SourcePath == "" || result.DataHash == "" || result.AuthorityHash == "" {
					t.Fatalf("real loader authority/provenance lost by report: %s", first)
				}
			case "csv":
				rows, err := csv.NewReader(bytes.NewReader(first)).ReadAll()
				if err != nil || len(rows) != 2 || rows[1][0] != "SP-2" {
					t.Fatalf("CSV selected rows: %q %v", rows, err)
				}
			case "markdown":
				if !strings.HasPrefix(string(first), "# ") || !bytes.Contains(first, []byte("```mermaid")) || !bytes.Contains(first, []byte("Sprint root")) || bytes.Contains(first, []byte("## 📋 SP-1")) {
					t.Fatalf("Markdown selection/context graph: %s", first)
				}
			case "mermaid":
				if !strings.HasPrefix(string(first), "graph TD") || !bytes.Contains(first, []byte("SP-2 ==> SP-1")) {
					t.Fatalf("Mermaid selected dependency closure: %s", first)
				}
			}
			// Both explicit format and explicit false override recipe defaults.
			overridePath := filepath.Join(dir, "override.json")
			stdout, stderr, err = runBVSplit(t, dir, "--recipe", "report", "--export", overridePath, "--export-format", "json", "--export-include-graph=false", "--no-hooks")
			if err != nil {
				t.Fatalf("override: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			raw, err := os.ReadFile(overridePath)
			if err != nil {
				t.Fatal(err)
			}
			var overridden map[string]json.RawMessage
			if err := json.Unmarshal(raw, &overridden); err != nil {
				t.Fatal(err)
			}
			if _, ok := overridden["graph"]; ok {
				t.Fatalf("explicit graph=false ignored: %s", raw)
			}
			writeRecipeFile(t, dir, "empty.yaml", "filters:\n  tags: [does-not-exist]\nexport:\n  format: "+format+"\n")
			emptyPath := filepath.Join(dir, "empty.out")
			stdout, stderr, err = runBVSplit(t, dir, "--recipe", "empty", "--export", emptyPath, "--no-hooks")
			if err != nil {
				t.Fatalf("empty selection export: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
			}
			empty, err := os.ReadFile(emptyPath)
			if err != nil || bytes.Contains(empty, []byte("Sprint")) || bytes.Contains(empty, []byte("Backlog")) {
				t.Fatalf("empty selection leaked source content: %v\n%s", err, empty)
			}
		})
	}
}

func TestRecipeReportPartialAndHistoricalAuthority(t *testing.T) {
	t.Run("partial source remains inspectable", func(t *testing.T) {
		dir := recipeProject(t)
		writeIssuesJSONL(t, dir, "{\"id\":\"valid\",\"title\":\"Retained issue\",\"status\":\"open\",\"priority\":1,\"issue_type\":\"task\"}\nmalformed row\n")
		path := filepath.Join(dir, "partial.json")
		stdout, stderr, err := runBVSplit(t, dir, "--export", path, "--export-format", "json", "--no-hooks")
		if err != nil {
			t.Fatalf("partial exploratory report: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			SourceAuthority struct {
				State                  string `json:"state"`
				ClaimSafe              bool   `json:"claim_safe"`
				Valid, Errors, Skipped int
			} `json:"source_authority"`
			Issues []struct {
				ID      string `json:"id"`
				Actions struct {
					Claim json.RawMessage `json:"claim"`
				} `json:"actions"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		// The malformed row is an error; Skipped counts recognized non-issue
		// records, of which this two-row fixture contains none.
		if len(report.Issues) != 1 || report.Issues[0].ID != "valid" || report.SourceAuthority.State != "partial" || report.SourceAuthority.ClaimSafe || report.SourceAuthority.Valid != 1 || report.SourceAuthority.Errors != 1 || report.SourceAuthority.Skipped != 0 {
			t.Fatalf("partial report lost useful data or exact source diagnostics: %s", raw)
		}
		if len(report.Issues[0].Actions.Claim) > 0 && string(report.Issues[0].Actions.Claim) != "null" {
			t.Fatalf("partial report emitted claim: %s", raw)
		}
	})
	t.Run("historical explicit report does not start TUI", func(t *testing.T) {
		dir, _ := createHistoryRepo(t)
		path := filepath.Join(dir, "history.json")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, buildBvBinary(t), "--as-of", "HEAD~2", "--export", path, "--export-format", "json", "--no-hooks")
		cmd.Dir = dir
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("historical report failed or started TUI: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			AsOf       string `json:"as_of"`
			AsOfCommit string `json:"as_of_commit"`
			SourceKind string `json:"source_kind"`
			Issues     []struct {
				ID, Status string
				Actions    struct{ Claim, Show json.RawMessage } `json:"actions"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		if report.AsOf != "HEAD~2" || len(report.AsOfCommit) != 40 || report.SourceKind != "git" || len(report.Issues) != 1 || report.Issues[0].ID != "HIST-1" || report.Issues[0].Status != "open" {
			t.Fatalf("historical source selection/provenance missing: %s", raw)
		}
		for _, action := range []json.RawMessage{report.Issues[0].Actions.Claim, report.Issues[0].Actions.Show} {
			if len(action) > 0 && string(action) != "null" {
				t.Fatalf("historical report emitted live route: %s", raw)
			}
		}
	})
}

func TestRecipeReportLabelScopeKeepsContextOutOfBodies(t *testing.T) {
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, strings.Join([]string{
		`{"id":"api-1","title":"Selected API task","status":"open","priority":1,"issue_type":"task","labels":["backend"],"dependencies":[{"depends_on_id":"web-1","type":"blocks"}]}`,
		`{"id":"api-free","title":"Independent API task","status":"open","priority":2,"issue_type":"task","labels":["backend"]}`,
		`{"id":"web-1","title":"Frontend graph context","status":"open","priority":1,"issue_type":"task","labels":["frontend"]}`,
	}, "\n"))
	path := filepath.Join(dir, "scoped.json")
	stdout, stderr, err := runBVSplit(t, dir, "--label", "backend", "--export", path, "--export-format", "json", "--no-hooks")
	if err != nil {
		t.Fatalf("scoped report: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
		Graph struct{ Nodes, Edges int } `json:"graph"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool)
	for _, issue := range report.Issues {
		ids[issue.ID] = true
	}
	if len(ids) != 2 || !ids["api-1"] || !ids["api-free"] || ids["web-1"] || report.Graph.Nodes != 3 || report.Graph.Edges != 1 {
		t.Fatalf("selected report bodies and graph context were conflated: %s", raw)
	}
}

func TestRecipeReportTemplateAndWriteErrors(t *testing.T) {
	dir := recipeProject(t)
	writeRecipeFile(t, dir, "report.yaml", "filters:\n  tags: [sprint]\n  status: [open]\nsort:\n  field: id\nexport:\n  format: markdown\n  include_graph: false\n  template: missing.tmpl\n")
	for name, body := range map[string]string{"malformed.tmpl": "{{range", "unknown-field.tmpl": "{{.Unknown}}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"missing template", nil, "read export template"},
		{"malformed template", []string{"--export-template", "malformed.tmpl"}, "parse export template"},
		{"template render error", []string{"--export-template", "unknown-field.tmpl"}, "render export template"},
		{"unknown format", []string{"--export-format", "shell"}, "export format"},
		{"incompatible template", []string{"--export-format", "json"}, "templates require markdown"},
		{"incompatible CSV graph", []string{"--export-format", "csv", "--export-template=", "--export-include-graph=true"}, "CSV cannot include a graph"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.out")
			args := append([]string{"--recipe", "report", "--export", path, "--no-hooks"}, tc.extra...)
			stdout, stderr, err := runBVSplit(t, dir, args...)
			if err == nil || !strings.Contains(stdout+stderr, tc.want) {
				t.Fatalf("expected %q failure: %v\nstdout=%s\nstderr=%s", tc.want, err, stdout, stderr)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration wrote a report: %v", err)
			}
		})
	}
	goodTemplate := filepath.Join(dir, "literal.tmpl")
	if err := os.WriteFile(goodTemplate, []byte("Selected {{range .Issues}}{{.ID}} {{end}}"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "custom.md")
	stdout, stderr, err := runBVSplit(t, dir, "--recipe", "report", "--export", path, "--export-template", "literal.tmpl", "--no-hooks")
	if err != nil {
		t.Fatalf("template override positive: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := goldmark.Convert(raw, &rendered); err != nil {
		t.Fatal(err)
	}
	// Escaped field punctuation must preserve the exact visible selection and
	// order. The template's authored paragraph remains ordinary Markdown.
	visible := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rendered.String(), "<p>"), "</p>\n"))
	if visible != "Selected SP-1 SP-2" {
		t.Fatalf("template selected order: markdown=%q rendered=%q", raw, rendered.String())
	}
	stdout, stderr, err = runBVSplit(t, dir, "--recipe", "report", "--export", t.TempDir(), "--export-template=", "--no-hooks")
	if err == nil || !strings.Contains(stdout+stderr, "Error exporting") {
		t.Fatalf("write error not surfaced: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
}

func TestRecipeReportHookOrderingAndExitPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, preExit, postExit, postPolicy string
		wantFailure, wantReport             bool
		wantMarker                          string
	}{
		{"success", "0", "0", "fail", false, true, "prepost"},
		{"pre failure", "7", "0", "fail", true, false, "pre"},
		{"post failure", "0", "7", "fail", true, true, "prepost"},
		{"post continue", "0", "7", "continue", false, true, "prepost"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := recipeProject(t)
			writeRecipeFile(t, dir, "report.yaml", "filters:\n  tags: [sprint]\n  status: [open]\nview:\n  max_items: 1\nexport:\n  format: json\n  include_graph: false\n")
			if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
				t.Fatal(err)
			}
			config := fmt.Sprintf(`hooks:
  pre-export:
    - name: before-write
      command: 'test "$BV_EXPORT_FORMAT" = json && test "$BV_ISSUE_COUNT" = 1 && test ! -e "$BV_EXPORT_PATH" && printf pre > hook-order; exit %s'
      on_error: fail
  post-export:
    - name: after-write
      command: 'test -f "$BV_EXPORT_PATH" && printf post >> hook-order; exit %s'
      on_error: %s
`, tc.preExit, tc.postExit, tc.postPolicy)
			if err := os.WriteFile(filepath.Join(dir, ".bv", "hooks.yaml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "report.json")
			stdout, stderr, err := runBVSplit(t, dir, "--recipe", "report", "--export", path)
			if (err != nil) != tc.wantFailure {
				t.Fatalf("hook exit policy: got %v want failure=%v\nstdout=%s\nstderr=%s", err, tc.wantFailure, stdout, stderr)
			}
			_, statErr := os.Stat(path)
			if (statErr == nil) != tc.wantReport {
				t.Fatalf("report write order: stat=%v want report=%v", statErr, tc.wantReport)
			}
			marker, readErr := os.ReadFile(filepath.Join(dir, "hook-order"))
			if readErr != nil || string(marker) != tc.wantMarker {
				t.Fatalf("actual hooks did not observe format/count/write order: got %q error=%v want %q\nstdout=%s\nstderr=%s", marker, readErr, tc.wantMarker, stdout, stderr)
			}
		})
	}
}

// TestRecipePathArgumentRobotPlan: `--recipe .beads/recipes/x.yaml --robot-plan`
// exits 0 and the plan contains only the recipe's issues; the same file is
// also addressable by its stem.
func TestRecipePathArgumentRobotPlan(t *testing.T) {
	dir := recipeProject(t)

	for _, arg := range []string{filepath.Join(".beads", "recipes", "sprint.yaml"), "sprint"} {
		t.Run(arg, func(t *testing.T) {
			var payload recipePlanPayload
			if err := runBVCommandJSON(t, dir, &payload, "--recipe", arg, "--robot-plan"); err != nil {
				t.Fatalf("--recipe %s --robot-plan: %v", arg, err)
			}
			if payload.DataHash == "" {
				t.Fatalf("missing data_hash")
			}
			ids := planIssueIDs(payload)
			if !ids["SP-1"] {
				t.Fatalf("plan is missing the actionable sprint issue SP-1: %v", ids)
			}
			for id := range ids {
				if id != "SP-1" && id != "SP-2" {
					t.Fatalf("plan contains %s, which the recipe filters out (%v)", id, ids)
				}
			}
			// BL-1 is open and actionable but not sprint-labelled; SP-9 is closed.
			// The plan's totals must reflect the two-issue recipe scope, not all four.
			if total := payload.Plan.TotalActionable + payload.Plan.TotalBlocked; total != 2 {
				t.Fatalf("plan scope = %d issues (actionable=%d blocked=%d), want 2", total, payload.Plan.TotalActionable, payload.Plan.TotalBlocked)
			}
		})
	}

	// Without the recipe the backlog item is planned too, proving the filter did the work.
	var unfiltered recipePlanPayload
	if err := runBVCommandJSON(t, dir, &unfiltered, "--robot-plan"); err != nil {
		t.Fatalf("--robot-plan: %v", err)
	}
	if !planIssueIDs(unfiltered)["BL-1"] {
		t.Fatalf("unfiltered plan should include BL-1: %v", planIssueIDs(unfiltered))
	}
}

// TestRecipeHighImpactRobotTriage: the builtin high-impact recipe (a PageRank
// sort) still drives --robot-triage.
func TestRecipeHighImpactRobotTriage(t *testing.T) {
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, strings.Join([]string{
		`{"id":"ROOT","title":"Root blocker","status":"open","priority":3,"issue_type":"task"}`,
		`{"id":"MID","title":"Middle","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"MID","depends_on_id":"ROOT","type":"blocks"}]}`,
		`{"id":"LEAF","title":"Leaf","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"LEAF","depends_on_id":"MID","type":"blocks"}]}`,
		`{"id":"SOLO","title":"Independent","status":"open","priority":0,"issue_type":"task"}`,
		`{"id":"DONE","title":"Finished","status":"closed","priority":0,"issue_type":"task"}`,
	}, "\n")+"\n")

	var payload struct {
		DataHash string `json:"data_hash"`
		Triage   struct {
			Recommendations []struct {
				ID string `json:"id"`
			} `json:"recommendations"`
		} `json:"triage"`
	}
	if err := runBVCommandJSON(t, dir, &payload, "--recipe", "high-impact", "--robot-triage"); err != nil {
		t.Fatalf("--recipe high-impact --robot-triage: %v", err)
	}
	if payload.DataHash == "" {
		t.Fatalf("missing data_hash")
	}
	if len(payload.Triage.Recommendations) == 0 {
		t.Fatalf("expected triage recommendations under the high-impact recipe")
	}
	for _, rec := range payload.Triage.Recommendations {
		if rec.ID == "DONE" {
			t.Fatalf("high-impact keeps only open/in_progress issues; got closed DONE in %+v", payload.Triage.Recommendations)
		}
	}
}

// TestRecipeProjectFileListedInRobotRecipes: --robot-recipes reports the
// source (and defining path) of every recipe, project files included.
func TestRecipeProjectFileListedInRobotRecipes(t *testing.T) {
	dir := recipeProject(t)
	var payload struct {
		Recipes []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"`
			Path        string `json:"path"`
		} `json:"recipes"`
	}
	if err := runBVCommandJSON(t, dir, &payload, "--robot-recipes"); err != nil {
		t.Fatalf("--robot-recipes: %v", err)
	}
	seen := map[string]string{}
	for _, r := range payload.Recipes {
		seen[r.Name] = r.Source
		if r.Name != "sprint" {
			continue
		}
		if r.Source != "project-file" {
			t.Fatalf("sprint source = %q, want project-file", r.Source)
		}
		if r.Description != "Open sprint work" {
			t.Fatalf("sprint description = %q", r.Description)
		}
		if !strings.HasSuffix(r.Path, filepath.Join(".beads", "recipes", "sprint.yaml")) {
			t.Fatalf("sprint path = %q, want the defining file", r.Path)
		}
	}
	if seen["sprint"] == "" {
		t.Fatalf("project-file recipe 'sprint' not listed; got %v", seen)
	}
	if seen["high-impact"] != "builtin" || seen["actionable"] != "builtin" {
		t.Fatalf("builtin recipes should report source builtin: %v", seen)
	}
}

// TestRecipeUnknownFilterKeyRejected: a recipe file with a misspelt filter key
// fails with the key named, whether addressed by path or by name.
func TestRecipeUnknownFilterKeyRejected(t *testing.T) {
	dir := recipeProject(t)
	bad := writeRecipeFile(t, dir, "bad.yaml", `description: "typo in filter key"
filters:
  statuses: [open]
`)

	stdout, stderr, err := runBVSplit(t, dir, "--recipe", bad, "--robot-plan")
	if err == nil {
		t.Fatalf("--recipe %s should fail; stdout=%s", bad, stdout)
	}
	if !strings.Contains(stderr, "statuses") || !strings.Contains(stderr, "bad.yaml") {
		t.Fatalf("stderr should name the unknown key and file:\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("no JSON must be emitted for a rejected recipe; stdout=%s", stdout)
	}

	stdout, stderr, err = runBVSplit(t, dir, "--recipe", "bad", "--robot-plan")
	if err == nil {
		t.Fatalf("--recipe bad should fail; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, `unknown recipe "bad"`) || !strings.Contains(stderr, "statuses") {
		t.Fatalf("stderr should say the name is unknown and why the file was skipped:\n%s", stderr)
	}

	// The good sibling file still loads despite the bad one.
	var payload recipePlanPayload
	if err := runBVCommandJSON(t, dir, &payload, "--recipe", "sprint", "--robot-plan"); err != nil {
		t.Fatalf("--recipe sprint after a bad sibling: %v", err)
	}
}

// TestRecipeMissingPathErrors: a path that does not exist is a clear error,
// not an "unknown recipe" with a list of builtins.
func TestRecipeMissingPathErrors(t *testing.T) {
	dir := recipeProject(t)
	missing := filepath.Join(".beads", "recipes", "nope.yaml")
	stdout, stderr, err := runBVSplit(t, dir, "--recipe", missing, "--robot-plan")
	if err == nil {
		t.Fatalf("--recipe %s should fail; stdout=%s", missing, stdout)
	}
	if !strings.Contains(stderr, "recipe file not found") || !strings.Contains(stderr, "nope.yaml") {
		t.Fatalf("stderr should say the recipe file was not found:\n%s", stderr)
	}
}

func TestRecipeSettingsDoNotExportWithoutRequest(t *testing.T) {
	dir := recipeProject(t)
	writeRecipeFile(t, dir, "cols.yaml", `filters:
  status: [open]
view:
  columns: [id, title]
  max_items: 1
export:
  format: markdown
  include_graph: false
  template: missing-template.tmpl
`)
	files := func() []string {
		t.Helper()
		var paths []string
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return paths
	}
	before := files()
	stdout, stderr, err := runBVSplit(t, dir, "--recipe", "cols", "--robot-plan")
	if err != nil {
		t.Fatalf("--recipe cols --robot-plan: %v\nstderr=%s", err, stderr)
	}
	var payload recipePlanPayload
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("plan JSON: %v\n%s", err, stdout)
	}
	if strings.Contains(stderr, "not applied") || strings.Contains(stderr, "missing-template") {
		t.Fatalf("ordinary analysis consulted export-only settings:\n%s", stderr)
	}
	if after := files(); !slices.Equal(before, after) {
		t.Fatalf("mere recipe selection created files: before=%v after=%v", before, after)
	}
	// max_items: 1 narrows the plan to a single issue.
	if total := payload.Plan.TotalActionable + payload.Plan.TotalBlocked; total != 1 {
		t.Fatalf("plan scope = %d issues, want 1 (view.max_items)", total)
	}
}

func TestRecipeTUIPresentationAndReset(t *testing.T) {
	skipIfNoScript(t)
	dir := recipeProject(t)
	writeIssuesJSONL(t, dir, `{"id":"SP-1","title":"界界界界界 Alpha","status":"open","priority":1,"issue_type":"task"}
{"id":"BL-1","title":"Recovered backlog marker","status":"open","priority":0,"issue_type":"task"}
`)
	writeRecipeFile(t, dir, "presentation.yaml", "filters:\n  id_prefix: SP-\nview:\n  columns: [title]\n  truncate_title: 5\n")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := scriptTUICommand(ctx, buildBvBinary(t), "--recipe", "presentation")
	// A piped test runner supplies a zero-sized terminal to script. Give the
	// actual PTY dimensions, as a terminal emulator would, before bv starts.
	if runtime.GOOS == "linux" {
		for i, arg := range cmd.Args {
			if arg == "-c" && i+1 < len(cmd.Args) {
				cmd.Args[i+1] = "stty columns 150 rows 40 && " + cmd.Args[i+1]
				break
			}
		}
	} else if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(ctx, "script", "-q", "/dev/null", "sh", "-c", "stty columns 150 rows 40 && exec \"$@\"", "sh", buildBvBinary(t), "--recipe", "presentation")
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "BV_TUI_AUTOCLOSE_MS=12000")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	path := filepath.Join(t.TempDir(), "recipe-tui.log")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	defer func() { cancel(); stdin.Close(); <-wait }()
	waitFor := func(marker string) string {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), marker) {
				return string(raw)
			}
			select {
			case <-deadline.C:
				t.Fatalf("TUI never rendered %q; output:\n%s", marker, raw)
			case <-ctx.Done():
				t.Fatalf("TUI context ended: %v; output:\n%s", ctx.Err(), raw)
			case <-tick.C:
			}
		}
	}
	initial := waitFor("界界…")
	if strings.Contains(initial, "Recovered backlog marker") {
		t.Fatal("recipe did not filter initial presentation")
	}
	if _, err := stdin.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	waitFor("Recovered backlog marker")
}
