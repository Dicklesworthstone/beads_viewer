package main_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	_ "modernc.org/sqlite"
)

func TestRobotCycleSuggestionRepairsImportedTracker(t *testing.T) {
	br, err := exec.LookPath("br")
	if err != nil {
		t.Skip("cycle repair proof requires installed br; synthetic route tests are not a substitute")
	}
	dir := filepath.Join(t.TempDir(), "legacy cycle ' tracker")
	var environment []string
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		if key != "BEADS_DIR" && key != "BEADS_DB" && key != "BD_DB" && key != "BEADS_JSONL" {
			environment = append(environment, item)
		}
	}
	environment = append(environment, "BV_NO_GITIGNORE=1", "SOURCE_DATE_EPOCH=1788472800")
	run := func(executable string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(executable, args...)
		cmd.Dir, cmd.Env = dir, environment
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		t.Logf("cwd=%q argv=%q exit=%v stderr=%s stdout=%s", dir, append([]string{executable}, args...), err, stderr.String(), out)
		if err != nil {
			t.Fatalf("real imported-tracker repair: %v", err)
		}
		return out
	}
	// This is a legacy cyclic import/repair fixture, not a claim that the
	// healthy tracker permits creating a cycle through br dep add. The real
	// import path establishes the database; no storage corruption is planted.
	writeBeads(t, dir, `{"id":"cycle-1","title":"Legacy first","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","dependencies":[{"issue_id":"cycle-1","depends_on_id":"cycle-2","type":"blocks","created_at":"2026-01-01T00:00:00Z"}]}
{"id":"cycle-2","title":"Legacy second","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","dependencies":[{"issue_id":"cycle-2","depends_on_id":"cycle-3","type":"blocks","created_at":"2026-01-01T00:00:00Z"}]}
{"id":"cycle-3","title":"Legacy third","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","dependencies":[{"issue_id":"cycle-3","depends_on_id":"cycle-1","type":"blocks","created_at":"2026-01-01T00:00:00Z"},{"issue_id":"cycle-3","depends_on_id":"cycle-side","type":"blocks","created_at":"2026-01-01T00:00:00Z"}]}
{"id":"cycle-side","title":"Unrelated predecessor","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`)
	run(br, "init", "--prefix", "cycle", "--json")
	run(br, "sync", "--import-only", "--json")
	readDatabaseEdges := func() []string {
		t.Helper()
		db, err := sql.Open("sqlite", filepath.Join(dir, ".beads", "beads.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query("SELECT issue_id, depends_on_id, type FROM dependencies ORDER BY issue_id, depends_on_id, type")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var edges []string
		for rows.Next() {
			var from, to, kind string
			if err := rows.Scan(&from, &to, &kind); err != nil {
				t.Fatal(err)
			}
			edges = append(edges, from+">"+to+":"+kind)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return edges
	}
	before := []string{"cycle-1>cycle-2:blocks", "cycle-2>cycle-3:blocks", "cycle-3>cycle-1:blocks", "cycle-3>cycle-side:blocks"}
	if got := readDatabaseEdges(); !reflect.DeepEqual(got, before) {
		t.Fatalf("real import did not establish the legacy cycle and side edge: got%q want%q", got, before)
	}
	bv := buildBvBinary(t)
	if control := os.Getenv("BV_SUGGEST_TEST_BINARY"); control != "" {
		bv = control
	}
	readSuggestions := func() analysis.SuggestionSet {
		t.Helper()
		raw := run(bv, "--robot-suggest", "--suggest-type", "cycle")
		var report struct {
			Set analysis.SuggestionSet `json:"suggestions"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		return report.Set
	}
	suggestions := readSuggestions()
	if len(suggestions.Suggestions) != 1 || suggestions.Stats.ActionableCount != 1 {
		t.Fatalf("expected one actionable cycle warning: %+v", suggestions)
	}
	action := suggestions.Suggestions[0].Action
	if action == nil || action.WorkingDirectory != dir || len(action.Argv) < 3 || !reflect.DeepEqual(action.Argv[len(action.Argv)-3:], []string{"--", "cycle-3", "cycle-1"}) {
		t.Fatalf("cycle warning must remove the actual last edge from cycle-3 to cycle-1: %+v", action)
	}
	run(action.Argv[0], action.Argv[1:]...)
	want := []string{"cycle-1>cycle-2:blocks", "cycle-2>cycle-3:blocks", "cycle-3>cycle-side:blocks"}
	if got := readDatabaseEdges(); !reflect.DeepEqual(got, want) {
		t.Fatalf("repair removed the wrong tracker edges: got%q want%q", got, want)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var exportedEdges []string
	issueCount := 0
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var issue model.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			t.Fatal(err)
		}
		issueCount++
		for _, dep := range issue.Dependencies {
			if dep != nil {
				exportedEdges = append(exportedEdges, issue.ID+">"+dep.DependsOnID+":"+string(dep.Type))
			}
		}
	}
	sort.Strings(exportedEdges)
	if issueCount != 4 || !reflect.DeepEqual(exportedEdges, want) {
		t.Fatalf("tracker-written export lost issues or other edges: count%d edges%q want%q; JSONL=%s", issueCount, exportedEdges, want, raw)
	}
	if remaining := readSuggestions(); len(remaining.Suggestions) != 0 || remaining.Stats.ActionableCount != 0 {
		t.Fatalf("cycle still reported after real tracker repair: %+v", remaining)
	}
	// The normal tracker mutation path still rejects reintroducing the cycle.
	// A repair fixture accepted through import is not a bypass of that guard.
	cmd := exec.Command(br, "dep", "add", "cycle-3", "cycle-1", "--json")
	cmd.Dir, cmd.Env = dir, environment
	rejected, err := cmd.CombinedOutput()
	t.Logf("healthy cycle guard cwd=%q argv=%q exit=%v output=%s", dir, cmd.Args, err, rejected)
	if err == nil || !strings.Contains(strings.ToLower(string(rejected)), "cycle") {
		t.Fatalf("tracker must reject cycle recreation: error=%v output=%s", err, rejected)
	}
	if got := readDatabaseEdges(); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected cycle recreation changed tracker edges: %q", got)
	}
	afterRejection, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
	if err != nil || !bytes.Equal(afterRejection, raw) {
		t.Fatalf("rejected cycle recreation changed exported state: error=%v before=%s after=%s", err, raw, afterRejection)
	}
}

func TestRobotSuggestionsExecuteOnlyVerifiedTracker(t *testing.T) {
	br, err := exec.LookPath("br")
	if err != nil {
		t.Skip("real suggestion mutation proof requires installed br; unit routes are not a substitute")
	}
	bv := buildBvBinary(t)
	if control := os.Getenv("BV_SUGGEST_TEST_BINARY"); control != "" {
		bv = control
	}
	root := t.TempDir()
	var environment []string
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		if key != "BEADS_DIR" && key != "BEADS_DB" && key != "BD_DB" {
			environment = append(environment, item)
		}
	}
	environment = append(environment, "BV_NO_GITIGNORE=1", "SOURCE_DATE_EPOCH=1788472800")
	run := func(dir, executable string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(executable, args...)
		cmd.Dir, cmd.Env = dir, environment
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		t.Logf("cwd=%q argv=%q exit=%v stderr=%s stdout=%s", dir, append([]string{executable}, args...), err, stderr.String(), out)
		if err != nil {
			t.Fatalf("real tracker/CLI execution: %v", err)
		}
		return out
	}
	repos := []string{filepath.Join(root, "owner's api $ repo"), filepath.Join(root, "web repo")}
	for _, dir := range repos {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeBeads(t, dir, `{"id":"shared-1","title":"Fix authentication login bug","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
{"id":"shared-2","title":"Fix authentication login bug","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","labels":["auth","bug","--needs-review"]}`)
		run(dir, br, "init", "--prefix", "shared", "--json")
		run(dir, br, "show", "shared-1", "--json")
	}
	workspacePath := filepath.Join(root, "workspace.yaml")
	workspaceData := fmt.Sprintf("repos:\n  - name: api\n    path: %q\n    prefix: api-\n  - name: web\n    path: %q\n    prefix: web-\n", repos[0], repos[1])
	if err := os.WriteFile(workspacePath, []byte(workspaceData), 0o600); err != nil {
		t.Fatal(err)
	}
	suggest := func(kind string, extra ...string) analysis.SuggestionSet {
		t.Helper()
		args := append([]string{"--workspace", workspacePath, "--robot-suggest", "--suggest-type", kind, "--suggest-confidence", "0.1"}, extra...)
		raw := run(root, bv, args...)
		var report struct {
			Set analysis.SuggestionSet `json:"suggestions"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Set.Suggestions) == 0 {
			t.Fatalf("lost positive suggestion analysis: %s", raw)
		}
		return report.Set
	}
	duplicates := suggest("duplicate")
	var related *model.IssueCommand
	allowed, withheld := 0, 0
	for _, suggestion := range duplicates.Suggestions {
		sameTracker := strings.HasPrefix(suggestion.TargetBead, "api-") == strings.HasPrefix(suggestion.RelatedBead, "api-")
		if sameTracker {
			allowed++
			if suggestion.Action == nil || suggestion.ActionCommand != suggestion.Action.Shell {
				t.Errorf("same tracker pair lacks executable action: %+v", suggestion)
			}
			if strings.HasPrefix(suggestion.TargetBead, "api-") {
				related = suggestion.Action
			}
		} else {
			withheld++
			if suggestion.Action != nil || suggestion.ActionCommand != "" || suggestion.Metadata["action_unavailable_reason"] == nil {
				t.Errorf("cross-tracker pair borrowed local-ID route: %+v", suggestion)
			}
		}
	}
	if allowed != 2 || withheld != 4 || duplicates.Stats.ActionableCount != 2 || related == nil {
		t.Fatalf("want 2 local positives/4 cross-tracker refusals, got allowed=%d withheld=%d set=%+v", allowed, withheld, duplicates)
	}
	var label, literalLabel, blocking *model.IssueCommand
	var addedLabel string
	for _, suggestion := range suggest("label").Suggestions {
		if suggestion.TargetBead == "api-shared-1" && suggestion.Action != nil {
			value, _ := suggestion.Metadata["suggested_label"].(string)
			if value == "--needs-review" {
				literalLabel = suggestion.Action
			} else if label == nil {
				label, addedLabel = suggestion.Action, value
			}
		}
	}
	for _, suggestion := range suggest("dependency").Suggestions {
		if suggestion.TargetBead == "web-shared-2" && suggestion.RelatedBead == "web-shared-1" {
			blocking = suggestion.Action
			break
		}
	}
	if label == nil || literalLabel == nil || addedLabel == "" || blocking == nil {
		t.Fatalf("missing same-tracker label/dependency positive: label=%+v literalLabel=%+v blocking=%+v", label, literalLabel, blocking)
	}
	for _, action := range []*model.IssueCommand{related, label, literalLabel, blocking} {
		if action.WorkingDirectory != repos[0] && action.WorkingDirectory != repos[1] {
			t.Fatalf("action escaped isolated fixtures: %+v", action)
		}
		if len(action.Argv) == 0 || strings.Contains(action.Shell, "api-shared-") || strings.Contains(action.Shell, "web-shared-") {
			t.Fatalf("display ID leaked into mutation route: %+v", action)
		}
		run(action.WorkingDirectory, action.Argv[0], action.Argv[1:]...)
	}
	// Read the tracker-written exports directly: successful mutations must be
	// visible to the next viewer, without an extra sync or an in-memory substitute.
	for i, dir := range repos {
		raw, err := os.ReadFile(filepath.Join(dir, ".beads", "issues.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[string]model.Issue)
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var issue model.Issue
			if err := json.Unmarshal(line, &issue); err != nil {
				t.Fatal(err)
			}
			seen[issue.ID] = issue
		}
		from, to, kind := "shared-1", "shared-2", model.DepRelated
		if i == 1 {
			from, to, kind = "shared-2", "shared-1", model.DepBlocks
		}
		found := false
		for _, dep := range seen[from].Dependencies {
			if dep != nil && dep.DependsOnID == to && dep.Type == kind {
				found = true
			}
		}
		if !found {
			t.Fatalf("successful mutation absent from tracker export: cwd=%q data=%s", dir, raw)
		}
		if i == 0 {
			for _, want := range []string{addedLabel, "--needs-review"} {
				found := false
				for _, label := range seen["shared-1"].Labels {
					found = found || label == want
				}
				if !found {
					t.Fatalf("successful literal label mutation %q absent from tracker export: %s", want, raw)
				}
			}
		}
	}
	assertNoActions := func(raw []byte) {
		t.Helper()
		var report struct {
			Set analysis.SuggestionSet `json:"suggestions"`
		}
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Set.Suggestions) == 0 || report.Set.Stats.ActionableCount != 0 {
			t.Fatalf("read-only source lost analyses or retained actionable count: %s", raw)
		}
		for _, suggestion := range report.Set.Suggestions {
			if suggestion.Action != nil || suggestion.ActionCommand != "" || suggestion.Metadata["action_unavailable_reason"] == nil {
				t.Fatalf("read-only source emitted nested mutation: %+v", suggestion)
			}
		}
	}
	// A real Git snapshot of a live tracker remains historical even if the
	// current directory also contains the writable tracker database.
	run(repos[0], "git", "init", "-b", "main")
	run(repos[0], "git", "config", "user.name", "Suggestion Fixture")
	run(repos[0], "git", "config", "user.email", "suggestion@example.invalid")
	run(repos[0], "git", "add", ".beads/issues.jsonl")
	run(repos[0], "git", "commit", "-m", "Fixture snapshot")
	assertNoActions(run(repos[0], bv, "--as-of", "HEAD", "--robot-suggest", "--suggest-type", "duplicate"))
	unknown := filepath.Join(root, "JSONL without live tracker")
	raw, err := os.ReadFile(filepath.Join(repos[0], ".beads", "issues.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeBeads(t, unknown, string(raw))
	assertNoActions(run(unknown, bv, "--robot-suggest", "--suggest-type", "duplicate"))
	// Same surviving real sources, one failed enabled source: analyses remain,
	// but no nested typed or shell mutation can be authorized.
	workspaceData += fmt.Sprintf("  - name: missing\n    path: %q\n    prefix: missing-\n", filepath.Join(root, "missing"))
	if err := os.WriteFile(workspacePath, []byte(workspaceData), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := suggest("duplicate")
	for _, suggestion := range partial.Suggestions {
		if suggestion.Action != nil || suggestion.ActionCommand != "" {
			t.Fatalf("partial workspace emitted nested mutation: %+v", suggestion)
		}
	}
	if partial.Stats.ActionableCount != 0 {
		t.Fatalf("partial workspace falsely counts actionable suggestions: %+v", partial.Stats)
	}
}

func TestRobotSuggestContract(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	// Two similar issues to exercise suggestion pipeline (duplicates/labels may or may not trigger).
	writeBeads(t, env, `{"id":"A","title":"Login OAuth bug","status":"open","priority":1,"issue_type":"task","description":"OAuth login fails with 500 in auth handler"}
{"id":"B","title":"OAuth login failure","status":"open","priority":2,"issue_type":"task","description":"Login via OAuth returns error; auth flow seems broken"}`)

	var first struct {
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		Suggestions struct {
			Suggestions []struct {
				Type       string  `json:"type"`
				TargetBead string  `json:"target_bead"`
				Confidence float64 `json:"confidence"`
			} `json:"suggestions"`
			Stats struct {
				Total int `json:"total"`
			} `json:"stats"`
		} `json:"suggestions"`
		UsageHints []string `json:"usage_hints"`
	}
	runRobotJSON(t, bv, env, "--robot-suggest", &first)

	if first.GeneratedAt == "" {
		t.Fatalf("suggest missing generated_at")
	}
	if first.DataHash == "" {
		t.Fatalf("suggest missing data_hash")
	}
	if len(first.UsageHints) == 0 {
		t.Fatalf("suggest missing usage_hints")
	}
	if first.Suggestions.Stats.Total != len(first.Suggestions.Suggestions) {
		t.Fatalf("suggest stats.total mismatch: %d vs %d", first.Suggestions.Stats.Total, len(first.Suggestions.Suggestions))
	}

	// Determinism: second call should share the same data_hash
	var second struct {
		DataHash string `json:"data_hash"`
	}
	runRobotJSON(t, bv, env, "--robot-suggest", &second)
	if first.DataHash != second.DataHash {
		t.Fatalf("suggest data_hash changed between calls: %v vs %v", first.DataHash, second.DataHash)
	}
}
