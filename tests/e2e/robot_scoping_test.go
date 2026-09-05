package main_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	_ "modernc.org/sqlite"
)

func TestRobotActionRoutesLiveTrackers(t *testing.T) {
	br, err := exec.LookPath("br")
	if err != nil {
		t.Skip("live tracker proof requires installed br; renderer units are not a substitute")
	}
	bv := buildBvBinary(t)
	if control := os.Getenv("BV_ACTION_TEST_BINARY"); control != "" {
		bv = control // Explicit old-binary negative control against this same fixture.
	}
	root := t.TempDir()
	cleanEnv := func(dir string) []string {
		var environment []string
		for _, item := range os.Environ() {
			key := strings.SplitN(item, "=", 2)[0]
			if key != "BEADS_DIR" && key != "BEADS_DB" && key != "BD_DB" {
				environment = append(environment, item)
			}
		}
		return append(environment, "SOURCE_DATE_EPOCH=1788472800", "BV_NO_GITIGNORE=1")
	}
	tracker := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(br, args...)
		cmd.Dir, cmd.Env = dir, cleanEnv(dir)
		out, err := cmd.CombinedOutput()
		t.Logf("tracker cwd=%q argv=%q exit=%v output=%s", dir, args, err, out)
		if err != nil {
			t.Fatalf("tracker fixture: %v", err)
		}
		return out
	}
	repos := []string{filepath.Join(root, "a repo ' $"), filepath.Join(root, "b repo")}
	for i, dir := range repos {
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		issue := map[string]any{"id": "same-1", "title": fmt.Sprintf("Repository %d", i), "status": "open", "priority": 1,
			"issue_type": "task", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "labels": []string{"work"}}
		data, err := json.Marshal(issue)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		tracker(dir, "init", "--prefix", "same", "--json")
		tracker(dir, "show", "same-1", "--json") // A real br database with the imported ID.
	}
	workspacePath := filepath.Join(root, "workspace.yaml")
	workspaceData := fmt.Sprintf("repos:\n  - name: api\n    path: %q\n    prefix: api-\n  - name: web\n    path: %q\n    prefix: web-\n", repos[0], repos[1])
	if err := os.WriteFile(workspacePath, []byte(workspaceData), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, environment []string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir, cmd.Env = dir, append(cleanEnv(dir), environment...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		t.Logf("bv=%q cwd=%q argv=%q exit=%v stderr=%s stdout=%s", bv, dir, args, err, stderr.String(), out)
		if err != nil {
			t.Fatalf("bv failed: %v", err)
		}
		return out
	}
	verifyShow := func(actions model.IssueActions, dir, title string) {
		t.Helper()
		if actions.LocalID != "same-1" || actions.WorkingDirectory != dir || actions.Show == nil || actions.Claim == nil {
			t.Fatalf("wrong live action route: %+v, want cwd%q localIDsame-1", actions, dir)
		}
		for _, command := range []*model.IssueCommand{actions.Show, actions.Claim} {
			if command.Argv[len(command.Argv)-1] != "same-1" || strings.Contains(command.Shell, "/data/projects/beads_viewer") {
				t.Fatalf("command escaped isolated tracker: %+v", command)
			}
		}
		if !strings.Contains(actions.Claim.Shell, "'--claim'") {
			t.Fatal("non-atomic claim suggestion")
		}
		// Execute generated inspection only, from a directory with no tracker
		// and hostile inherited selector variables pointing at the other repo.
		for _, shell := range []bool{false, true} {
			var cmd *exec.Cmd
			if shell {
				cmd = exec.Command("sh", "-c", actions.Show.Shell)
				cmd.Dir = root
			} else {
				cmd = exec.Command(actions.Show.Argv[0], actions.Show.Argv[1:]...)
				cmd.Dir = actions.WorkingDirectory
			}
			cmd.Env = append(cleanEnv(root), "BEADS_DIR="+filepath.Join(repos[1], ".beads"), "BEADS_DB="+filepath.Join(repos[1], ".beads", "beads.db"))
			out, err := cmd.CombinedOutput()
			var issues []model.Issue
			if err != nil || json.Unmarshal(out, &issues) != nil || len(issues) != 1 || issues[0].ID != "same-1" || issues[0].Title != title {
				t.Fatalf("generated show shell=%v reached wrong issue: err%v out%s", shell, err, out)
			}
		}
	}
	var triage struct {
		Triage struct {
			Recommendations []struct {
				ID      string             `json:"id"`
				Actions model.IssueActions `json:"actions"`
			} `json:"recommendations"`
		} `json:"triage"`
	}
	data := run(root, nil, "--robot-triage", "--workspace", workspacePath, "--robot-triage-by-track", "--robot-triage-by-label")
	if err := json.Unmarshal(data, &triage); err != nil {
		t.Fatal(err)
	}
	if len(triage.Triage.Recommendations) != 2 {
		t.Fatalf("lost colliding local IDs: %s", data)
	}
	for _, rec := range triage.Triage.Recommendations {
		i := 0
		if rec.ID == "web-same-1" {
			i = 1
		} else if rec.ID != "api-same-1" {
			t.Fatalf("wrong namespace: %s", rec.ID)
		}
		verifyShow(rec.Actions, repos[i], fmt.Sprintf("Repository %d", i))
	}
	for _, tc := range []struct {
		name              string
		args, environment []string
	}{
		{"db", []string{"--db", filepath.Join(repos[0], ".beads", "beads.db")}, nil},
		{"export", []string{"--db", filepath.Join(repos[0], ".beads", "issues.jsonl")}, nil},
		{"beads_dir", nil, []string{"BEADS_DIR=" + filepath.Join(repos[0], ".beads")}},
		{"beads_db", nil, []string{"BEADS_DB=" + filepath.Join(repos[0], ".beads", "beads.db")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var next struct {
				Actionable bool               `json:"actionable"`
				Actions    model.IssueActions `json:"actions"`
			}
			out := run(root, tc.environment, append([]string{"--robot-next"}, tc.args...)...)
			if err := json.Unmarshal(out, &next); err != nil || !next.Actionable {
				t.Fatalf("live route missing: %s", out)
			}
			verifyShow(next.Actions, repos[0], "Repository 0")
		})
	}
	t.Run("script_executes_only_bound_inspection", func(t *testing.T) {
		script := run(root, nil, "--emit-script", "--workspace", workspacePath)
		cmd := exec.Command("bash", "-c", string(script))
		cmd.Dir = root
		cmd.Env = append(cleanEnv(root), "BEADS_DIR="+filepath.Join(repos[1], ".beads"))
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Contains(out, []byte("Repository 0")) || !bytes.Contains(out, []byte("Repository 1")) {
			t.Fatalf("generated script lost a bound inspection: err%v out%s script%s", err, out, script)
		}
		for _, dir := range repos {
			var current []model.Issue
			if err := json.Unmarshal(tracker(dir, "show", "same-1", "--json"), &current); err != nil || len(current) != 1 || current[0].Status != model.StatusOpen || current[0].Assignee != "" {
				t.Fatalf("inspection script mutated tracker %q: %+v err%v", dir, current, err)
			}
		}
	})
	t.Run("metadata_declared_custom_database", func(t *testing.T) {
		dir := filepath.Join(root, "custom tracker")
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(repos[0], ".beads", "issues.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		tracker(dir, "init", "--prefix", "same", "--json")
		tracker(dir, "show", "same-1", "--json")
		originalDB := filepath.Join(beadsDir, "beads.db")
		customDB := filepath.Join(beadsDir, "custom ' tracker.sqlite3")
		db, err := sql.Open("sqlite", originalDB)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := db.Exec("VACUUM INTO ?", customDB)
		closeErr := db.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy isolated tracker database: %v %v", copyErr, closeErr)
		}
		metadataPath := filepath.Join(beadsDir, "metadata.json")
		metadata, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(metadata, &fields); err != nil {
			t.Fatal(err)
		}
		fields["database"] = filepath.Base(customDB)
		metadata, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataPath, metadata, 0o644); err != nil {
			t.Fatal(err)
		}
		var next struct {
			Actions model.IssueActions `json:"actions"`
		}
		if err := json.Unmarshal(run(root, nil, "--db", customDB, "--robot-next"), &next); err != nil {
			t.Fatal(err)
		}
		verifyShow(next.Actions, dir, "Repository 0")
		if next.Actions.Show.Argv[6] != customDB || next.Actions.Claim.Argv[6] != customDB {
			t.Fatalf("metadata database replaced by default: %+v", next.Actions)
		}
		// Both actors receive the same snapshot suggestion. The real tracker
		// must accept exactly one atomic claim, not let the last writer win.
		type claimResult struct {
			actor string
			out   []byte
			err   error
		}
		results := make(chan claimResult, 2)
		start := make(chan struct{})
		for _, actor := range []string{"routing-actor-a", "routing-actor-b"} {
			go func(actor string) {
				command := next.Actions.Claim
				argv := append([]string{}, command.Argv[:5]...)
				argv = append(argv, "--actor", actor)
				argv = append(argv, command.Argv[5:]...)
				cmd := exec.Command(argv[0], argv[1:]...)
				cmd.Dir, cmd.Env = command.WorkingDirectory, cleanEnv(root)
				<-start
				out, err := cmd.CombinedOutput()
				results <- claimResult{actor, out, err}
			}(actor)
		}
		close(start)
		winner, successes := "", 0
		for n := 0; n < 2; n++ {
			result := <-results
			t.Logf("concurrent claim actor=%s exit=%v output=%s", result.actor, result.err, result.out)
			if result.err == nil {
				winner, successes = result.actor, successes+1
			}
		}
		if successes != 1 {
			t.Fatalf("tracker accepted %d concurrent claims, want exactly one", successes)
		}
		var claimed []model.Issue
		if err := json.Unmarshal(tracker(dir, "--db", customDB, "show", "same-1", "--json"), &claimed); err != nil || len(claimed) != 1 || claimed[0].Status != model.StatusInProgress || claimed[0].Assignee != winner {
			t.Fatalf("tracker claim winner mismatch: winner%s issue%+v err%v", winner, claimed, err)
		}
	})
	t.Run("exported_live_actions", func(t *testing.T) {
		jsonPath := filepath.Join(root, "live-report.json")
		run(repos[0], nil, "--export", jsonPath, "--export-format", "json", "--no-hooks")
		data, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Issues []struct {
				Actions model.IssueActions `json:"actions"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(data, &report); err != nil || len(report.Issues) != 1 {
			t.Fatalf("live JSON report: err%v out%s", err, data)
		}
		verifyShow(report.Issues[0].Actions, repos[0], "Repository 0")
		bundle := filepath.Join(root, "live-agent-brief")
		run(repos[0], nil, "--agent-brief", bundle)
		data, err = os.ReadFile(filepath.Join(bundle, "triage.json"))
		if err != nil {
			t.Fatal(err)
		}
		var brief struct {
			Recommendations []struct {
				Actions model.IssueActions `json:"actions"`
			} `json:"recommendations"`
		}
		if err := json.Unmarshal(data, &brief); err != nil || len(brief.Recommendations) != 1 {
			t.Fatalf("live agent brief: err%v out%s", err, data)
		}
		verifyShow(brief.Recommendations[0].Actions, repos[0], "Repository 0")
	})
	assertNoMutation := func(t *testing.T, out []byte) {
		t.Helper()
		if bytes.Contains(out, []byte("--claim")) || bytes.Contains(out, []byte("--status")) || bytes.Contains(out, []byte("claim_command\":\"")) {
			t.Fatalf("unverified source emitted mutation: %s", out)
		}
	}
	t.Run("partial_workspace", func(t *testing.T) {
		partialPath := filepath.Join(root, "partial.yaml")
		partial := workspaceData + "  - name: missing\n    path: absent\n    prefix: missing-\n"
		if err := os.WriteFile(partialPath, []byte(partial), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"--robot-next"}, {"--robot-triage", "--robot-triage-by-track", "--robot-triage-by-label"}, {"--robot-triage", "--brief"}, {"--emit-script"}} {
			assertNoMutation(t, run(root, nil, append(args, "--workspace", partialPath)...))
		}
	})
	t.Run("redirect", func(t *testing.T) {
		dir := filepath.Join(root, "redirect repo")
		if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".beads", "redirect"), []byte(filepath.Join(repos[0], ".beads")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var next struct {
			Actions model.IssueActions `json:"actions"`
		}
		if err := json.Unmarshal(run(dir, nil, "--robot-next"), &next); err != nil {
			t.Fatal(err)
		}
		verifyShow(next.Actions, repos[0], "Repository 0")
	})
	t.Run("undeclared_sidecar", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(repos[0], ".beads", "issues.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(repos[0], ".beads", "archive.jsonl")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		assertNoMutation(t, run(root, nil, "--robot-next", "--db", path))
	})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repos[1], cleanEnv(repos[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %q: %v %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Action Routing Test")
	git("config", "user.email", "routing@example.invalid")
	git("add", "-f", ".beads/issues.jsonl")
	git("commit", "-m", "Open historical issue fixture")
	t.Run("worktree", func(t *testing.T) {
		worktree := filepath.Join(root, "linked worktree")
		git("worktree", "add", "--detach", worktree, "HEAD")
		// Explicitly use the tracker on the main worktree, retaining its live
		// metadata identity rather than borrowing the linked export's IDs.
		var next struct {
			Actions model.IssueActions `json:"actions"`
		}
		if err := json.Unmarshal(run(worktree, []string{"BEADS_DIR=" + filepath.Join(repos[1], ".beads")}, "--robot-next"), &next); err != nil {
			t.Fatal(err)
		}
		verifyShow(next.Actions, repos[1], "Repository 1")
		assertNoMutation(t, run(worktree, nil, "--robot-next", "--db", filepath.Join(worktree, ".beads", "issues.jsonl")))
	})
	t.Run("historical_now_closed", func(t *testing.T) {
		tracker(repos[1], "close", "same-1", "--reason", "isolated historical fixture", "--json")
		for _, args := range [][]string{{"--robot-next"}, {"--robot-triage", "--robot-triage-by-track", "--robot-triage-by-label"}, {"--robot-triage", "--brief"}, {"--emit-script"}} {
			out := run(repos[1], nil, append(args, "--as-of", "HEAD")...)
			assertNoMutation(t, out)
			if !bytes.Contains(out, []byte("same-1")) && !bytes.Contains(out, []byte("Repository 1")) {
				t.Fatalf("historical analysis disappeared: %s", out)
			}
		}
		for _, format := range []string{"markdown", "json"} {
			path := filepath.Join(root, "historical-report."+format)
			run(repos[1], nil, "--as-of", "HEAD", "--export", path, "--export-format", format, "--no-hooks")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			assertNoMutation(t, data)
			if !bytes.Contains(data, []byte("same-1")) {
				t.Fatalf("historical issue lost from %s report", format)
			}
		}
	})
	t.Run("arbitrary_file", func(t *testing.T) {
		copyPath := filepath.Join(root, "snapshot.jsonl")
		data, err := os.ReadFile(filepath.Join(repos[0], ".beads", "issues.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copyPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"--robot-next"}, {"--robot-triage", "--robot-triage-by-track", "--robot-triage-by-label"}, {"--robot-triage", "--brief"}, {"--emit-script"}} {
			assertNoMutation(t, run(root, nil, append(args, "--db", copyPath)...))
		}
	})
	// A valid live snapshot does not reserve the issue. Close the isolated
	// fixture after capturing its suggestion and require br to reject it.
	t.Run("tracker_rechecks_stale_claim", func(t *testing.T) {
		var next struct {
			Actions model.IssueActions `json:"actions"`
		}
		if err := json.Unmarshal(run(repos[0], nil, "--robot-next"), &next); err != nil {
			t.Fatal(err)
		}
		if next.Actions.Claim == nil {
			t.Fatal("missing live claim suggestion")
		}
		tracker(repos[0], "close", "same-1", "--reason", "isolated stale recommendation control", "--json")
		command := next.Actions.Claim
		cmd := exec.Command(command.Argv[0], command.Argv[1:]...)
		cmd.Dir = next.Actions.WorkingDirectory
		cmd.Env = cleanEnv(root)
		out, err := cmd.CombinedOutput()
		t.Logf("stale claim argv%q exit%v out%s", command.Argv, err, out)
		if err == nil {
			t.Fatal("installed tracker accepted claim of now-closed issue")
		}
		var issues []model.Issue
		if err := json.Unmarshal(tracker(repos[0], "show", "same-1", "--json"), &issues); err != nil || len(issues) != 1 || issues[0].Status != model.StatusClosed {
			t.Fatal("stale claim changed closed fixture")
		}
	})
}

// Scoping flags (--label, --recipe, --repo, --as-of) must be honoured by every
// issue-backed robot command, and every payload must say which scope applied.
// Reality check 2026-09-01 found five commands reloading the working tree and
// silently ignoring all four; this matrix is the regression gate. Commands are
// enumerated from --robot-capabilities so a new command is covered automatically.

type capabilityCommand struct {
	Name         string `json:"name"`
	Flag         string `json:"flag"`
	NeedsIssues  bool   `json:"needs_issues"`
	NeedsGit     bool   `json:"needs_git"`
	NeedsSprint  bool   `json:"needs_sprint"`
	NeedsBase    bool   `json:"needs_baseline"`
	MutatesState bool   `json:"mutates_state"`
}

func loadCapabilities(t *testing.T, bv string) []capabilityCommand {
	t.Helper()
	cmd := exec.Command(bv, "--robot-capabilities")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-capabilities: %v", err)
	}
	var payload struct {
		Commands []capabilityCommand `json:"commands"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if len(payload.Commands) < 20 {
		t.Fatalf("capabilities lists only %d commands", len(payload.Commands))
	}
	return payload.Commands
}

// scopingSkip lists commands the matrix cannot drive generically: they need
// git history, a sprint file, a baseline, extra arguments with side effects,
// or produce no issue data at all.
var scopingSkip = map[string]string{
	"robot-drift":               "requires --check-drift",
	"robot-diff":                "requires --diff-since (covered by robot_diff_test.go)",
	"robot-search":              "requires --search (covered by robot_search_test.go)",
	"robot-confirm-correlation": "mutates the feedback store",
	"robot-reject-correlation":  "mutates the feedback store",
	"robot-explain-correlation": "needs a real commit sha",
	"robot-sprint-show":         "needs a sprint id (needs_sprint)",
}

// argvFor turns a capabilities flag string such as "--robot-blocker-chain ISSUE_ID"
// into argv with placeholders substituted for the fixture.
func argvFor(flag, inScopeID string) []string {
	parts := strings.Fields(flag)
	for i, p := range parts {
		switch p {
		case "ISSUE_ID":
			parts[i] = inScopeID
		case "README.md":
			parts[i] = "README.md"
		}
	}
	return parts
}

type scopedRun struct {
	exit    error
	stdout  string
	stderr  string
	payload map[string]json.RawMessage
}

func runScoped(t *testing.T, bv, dir string, args ...string) scopedRun {
	t.Helper()
	cmd := exec.Command(bv, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	r := scopedRun{exit: err, stdout: out.String(), stderr: errb.String()}
	if err == nil {
		_ = json.Unmarshal(out.Bytes(), &r.payload)
	}
	return r
}

func scopeOf(t *testing.T, r scopedRun) (map[string]any, bool) {
	t.Helper()
	raw, ok := r.payload["scope"]
	if !ok {
		return nil, false
	}
	var scope map[string]any
	if err := json.Unmarshal(raw, &scope); err != nil {
		t.Fatalf("scope decode: %v", err)
	}
	return scope, true
}

func TestRobotScoping_EmptyLabel(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := t.TempDir()
	writeIssuesJSONL(t, repoDir, strings.Join([]string{
		`{"id":"api-1","title":"Backend","status":"open","issue_type":"task","priority":1,"labels":["backend"]}`,
		`{"id":"web-1","title":"Frontend","status":"open","issue_type":"task","priority":1,"labels":["frontend"]}`,
		`{"id":"api-closed","title":"Done","status":"closed","issue_type":"task","priority":1,"labels":["inactive"]}`,
		`{"id":"api-deferred","title":"Later","status":"deferred","issue_type":"task","priority":1,"labels":["inactive"]}`,
	}, "\n")+"\n")
	emptyDir := t.TempDir()
	writeIssuesJSONL(t, emptyDir, "")
	for _, tc := range []struct {
		name  string
		dir   string
		label string
		extra []string
	}{
		{"absent", repoDir, "absent-label", nil},
		{"case_sensitive", repoDir, "Backend", nil},
		{"repo_intersection", repoDir, "frontend", []string{"--repo", "api"}},
		{"recipe_intersection", repoDir, "inactive", []string{"--recipe", "actionable"}},
		{"empty_project", emptyDir, "backend", nil},
	} {
		for _, command := range []string{"--robot-plan", "--robot-triage", "--robot-next", "--robot-graph", "--robot-priority"} {
			t.Run(tc.name+"/"+command, func(t *testing.T) {
				args := append([]string{command, "--label", tc.label}, tc.extra...)
				r := runScoped(t, bv, tc.dir, args...)
				t.Logf("argv=%q exit=%v stderr=%q stdout=%s", args, r.exit, r.stderr, r.stdout)
				if r.exit != nil || r.payload == nil {
					t.Fatalf("empty scope must return JSON: %v", r.exit)
				}
				scope, ok := scopeOf(t, r)
				if !ok || scope["label"] != tc.label {
					t.Errorf("scope = %v, want label %q", scope, tc.label)
				}
				for _, key := range []string{"source_path", "source_kind", "data_hash"} {
					if len(r.payload[key]) == 0 || string(r.payload[key]) == `""` {
						t.Errorf("missing source metadata %s", key)
					}
				}
				for _, id := range []string{"api-1", "web-1", "api-closed", "api-deferred"} {
					if strings.Contains(r.stdout, `"`+id+`"`) {
						t.Errorf("empty scope leaked issue %s", id)
					}
				}
				var payload struct {
					Plan struct {
						TotalActionable int `json:"total_actionable"`
						TotalBlocked    int `json:"total_blocked"`
					} `json:"plan"`
					Triage struct {
						QuickRef struct {
							Total      int `json:"not_closed_count"`
							Actionable int `json:"actionable_count"`
						} `json:"quick_ref"`
					} `json:"triage"`
					Summary struct {
						TotalIssues int `json:"total_issues"`
					} `json:"summary"`
					Nodes      int  `json:"nodes"`
					Actionable bool `json:"actionable"`
				}
				if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Nodes != 0 || payload.Actionable || payload.Plan.TotalActionable != 0 || payload.Plan.TotalBlocked != 0 || payload.Triage.QuickRef.Total != 0 || payload.Triage.QuickRef.Actionable != 0 || payload.Summary.TotalIssues != 0 {
					t.Errorf("empty scope has nodes or an actionable pick: %+v", payload)
				}
				// Inspect values recursively: usage hints may name a claim field,
				// but no nested result may actually offer a mutation command.
				var checkClaims func(any)
				checkClaims = func(value any) {
					switch v := value.(type) {
					case map[string]any:
						for key, child := range v {
							if (key == "claim_command" || key == "claim_top") && child != nil && child != "" {
								t.Errorf("empty scope emitted %s=%v", key, child)
							}
							checkClaims(child)
						}
					case []any:
						for _, child := range v {
							checkClaims(child)
						}
					}
				}
				var decoded any
				if err := json.Unmarshal([]byte(r.stdout), &decoded); err != nil {
					t.Fatal(err)
				}
				checkClaims(decoded)
			})
		}
	}
	// Positive controls protect the matching policy and distinguish omitted
	// scope from a nonempty label that matches nothing.
	for _, label := range []string{"", "backend"} {
		r := runScoped(t, bv, repoDir, "--robot-plan", "--label", label)
		if r.exit != nil || !strings.Contains(r.stdout, `"api-1"`) {
			t.Fatalf("matching label %q lost ready issue: %v\n%s%s", label, r.exit, r.stdout, r.stderr)
		}
	}
}

func TestRobotScoping_DependencyAuthority(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, strings.Join([]string{
		`{"id":"api-1","title":"Backend blocked through frontend","status":"open","issue_type":"task","priority":0,"labels":["backend"],"dependencies":[{"depends_on_id":"web-1","type":"blocks"}]}`,
		`{"id":"web-1","title":"Frontend blocked through ops","status":"open","issue_type":"task","priority":0,"labels":["frontend"],"dependencies":[{"depends_on_id":"ops-1","type":"blocks"}]}`,
		`{"id":"ops-1","title":"Ops root","status":"open","issue_type":"task","priority":1,"labels":["ops"]}`,
		`{"id":"api-parent","title":"Parent inherits external blocker","status":"open","issue_type":"epic","priority":0,"labels":["backend"],"dependencies":[{"depends_on_id":"web-1","type":"blocks"}]}`,
		`{"id":"api-child","title":"Child inherits parent blockage","status":"open","issue_type":"task","priority":0,"labels":["backend"],"dependencies":[{"depends_on_id":"api-parent","type":"parent-child"}]}`,
		`{"id":"api-missing","title":"Unknown blocker","status":"open","issue_type":"task","priority":0,"labels":["backend"],"dependencies":[{"depends_on_id":"unknown","type":"blocks"}]}`,
		`{"id":"api-safe","title":"Closed external blocker is satisfied","status":"open","issue_type":"task","priority":4,"labels":["backend"],"dependencies":[{"depends_on_id":"web-done","type":"blocks"}]}`,
		`{"id":"web-done","title":"Done frontend","status":"closed","issue_type":"task","priority":1,"labels":["frontend"]}`,
	}, "\n")+"\n")
	for _, flags := range [][]string{
		{"--repo", "api"}, {"--label", "backend"},
		{"--repo", "api", "--recipe", "actionable"},
		{"--label", "backend", "--recipe", "actionable"},
		{"--repo", "api", "--label", "backend", "--recipe", "actionable"},
	} {
		for _, command := range []string{"--robot-plan", "--robot-triage", "--robot-next"} {
			t.Run(command+strings.Join(flags, "_"), func(t *testing.T) {
				args := append([]string{command}, flags...)
				r := runScoped(t, bv, dir, args...)
				t.Logf("argv=%q exit=%v stdout=%s stderr=%s", args, r.exit, r.stdout, r.stderr)
				if r.exit != nil {
					t.Fatalf("run: %v", r.exit)
				}
				var payload struct {
					ID         string `json:"id"`
					Actionable bool   `json:"actionable"`
					Diagnostic struct {
						ID string `json:"id"`
					} `json:"diagnostic_top_pick"`
					Plan struct {
						TotalActionable int `json:"total_actionable"`
						Tracks          []struct {
							Items []struct {
								ID string `json:"id"`
							} `json:"items"`
						} `json:"tracks"`
					} `json:"plan"`
					Triage struct {
						QuickRef struct {
							TopPicks []struct {
								ID string `json:"id"`
							} `json:"top_picks"`
						} `json:"quick_ref"`
						Recommendations []struct {
							ID        string `json:"id"`
							Claimable bool   `json:"claimable"`
						} `json:"recommendations"`
					} `json:"triage"`
				}
				if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
					t.Fatal(err)
				}
				var ready []string
				switch command {
				case "--robot-plan":
					if payload.Plan.TotalActionable != 1 {
						t.Errorf("actionable count=%d want 1", payload.Plan.TotalActionable)
					}
					for _, track := range payload.Plan.Tracks {
						for _, item := range track.Items {
							ready = append(ready, item.ID)
						}
					}
				case "--robot-triage":
					for _, pick := range payload.Triage.QuickRef.TopPicks {
						ready = append(ready, pick.ID)
					}
					for _, rec := range payload.Triage.Recommendations {
						if rec.Claimable && rec.ID != "api-safe" {
							t.Errorf("unsafe claimable recommendation %s", rec.ID)
						}
						if strings.HasPrefix(rec.ID, "web-") || strings.HasPrefix(rec.ID, "ops-") {
							t.Errorf("context-only recommendation %s", rec.ID)
						}
					}
				case "--robot-next":
					if payload.Actionable {
						t.Error("metadata-free fixture unexpectedly acquired a live mutation route")
					}
					ready = append(ready, payload.Diagnostic.ID)
				}
				if len(ready) != 1 || ready[0] != "api-safe" {
					t.Errorf("ready=%v, want [api-safe]", ready)
				}
			})
		}
	}
	r := runScoped(t, bv, dir, "--robot-graph", "--label", "backend")
	if r.exit != nil || !strings.Contains(r.stdout, `"web-1"`) {
		t.Fatalf("exploratory graph lost dependency context: %v\n%s%s", r.exit, r.stdout, r.stderr)
	}
}

func TestRobotScoping_TombstoneAuthorityAcrossSources(t *testing.T) {
	bv := buildBvBinary(t)
	const selected = `{"id":"api-safe","title":"Tombstone releases work","status":"open","issue_type":"task","priority":2,"labels":["backend"],"dependencies":[{"depends_on_id":"web-gone","type":"blocks"}]}
{"id":"api-unknown","title":"Missing dependency withholds work","status":"open","issue_type":"task","priority":0,"labels":["backend"],"dependencies":[{"depends_on_id":"web-absent","type":"blocks"}]}
`
	const deleted = `{"id":"web-gone","title":"Deleted predecessor","status":"tombstone","issue_type":"task","priority":1,"labels":["frontend"]}` + "\n"
	for _, source := range []string{"jsonl", "sqlite", "historical", "workspace"} {
		t.Run(source, func(t *testing.T) {
			dir := t.TempDir()
			var sourceArgs []string
			switch source {
			case "jsonl", "historical":
				writeIssuesJSONL(t, dir, selected+deleted)
				if source == "historical" {
					for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", ".beads/issues.jsonl"}, {"commit", "-q", "-m", "tombstone authority"}} {
						cmd := exec.Command("git", args...)
						cmd.Dir = dir
						cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
						if out, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("git %v: %v\n%s", args, err, out)
						}
					}
					sourceArgs = []string{"--as-of", "HEAD"}
				}
			case "sqlite":
				beadsDir := filepath.Join(dir, ".beads")
				if err := os.MkdirAll(beadsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", filepath.Join(beadsDir, "beads.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				_, err = db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER, labels TEXT, tombstone INTEGER);
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);
INSERT INTO issues VALUES ('api-safe', 'Tombstone releases work', 'open', 2, '["backend"]', 0), ('api-unknown', 'Missing dependency withholds work', 'open', 0, '["backend"]', 0), ('web-gone', 'Deleted predecessor', 'closed', 1, '["frontend"]', 1);
INSERT INTO dependencies VALUES ('api-safe', 'web-gone', 'blocks'), ('api-unknown', 'web-absent', 'blocks');`)
				if err != nil {
					t.Fatal(err)
				}
			case "workspace":
				writeIssuesJSONL(t, filepath.Join(dir, "api"), selected)
				writeIssuesJSONL(t, filepath.Join(dir, "web"), deleted)
				config := filepath.Join(dir, ".bv", "workspace.yaml")
				if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(config, []byte("name: tombstones\nrepos:\n  - name: api\n    path: api\n    prefix: api-\n  - name: web\n    path: web\n    prefix: web-\ndiscovery:\n  enabled: false\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				sourceArgs = []string{"--workspace", config}
			}
			for _, command := range []string{"--robot-plan", "--robot-triage", "--robot-next"} {
				t.Run(command, func(t *testing.T) {
					args := append([]string{command, "--label", "backend", "--recipe", "actionable"}, sourceArgs...)
					r := runScoped(t, bv, dir, args...)
					t.Logf("source=%s argv=%q exit=%v stdout=%s stderr=%s", source, args, r.exit, r.stdout, r.stderr)
					if r.exit != nil {
						t.Fatalf("run: %v", r.exit)
					}
					if !strings.Contains(r.stdout, `"api-safe"`) || strings.Contains(r.stdout, `"api-unknown"`) {
						t.Errorf("known tombstone must release api-safe; missing predecessor must withhold api-unknown: %s", r.stdout)
					}
					var payload struct {
						ID         string `json:"id"`
						Actionable bool   `json:"actionable"`
						Diagnostic struct {
							ID string `json:"id"`
						} `json:"diagnostic_top_pick"`
						Plan struct {
							TotalActionable int `json:"total_actionable"`
						} `json:"plan"`
						Triage struct {
							QuickRef struct {
								ActionableCount int `json:"actionable_count"`
							} `json:"quick_ref"`
						} `json:"triage"`
					}
					if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
						t.Fatal(err)
					}
					switch command {
					case "--robot-plan":
						if payload.Plan.TotalActionable != 1 {
							t.Errorf("actionable=%d want 1", payload.Plan.TotalActionable)
						}
					case "--robot-triage":
						if payload.Triage.QuickRef.ActionableCount != 1 {
							t.Errorf("actionable=%d want 1", payload.Triage.QuickRef.ActionableCount)
						}
					case "--robot-next":
						if payload.Diagnostic.ID != "api-safe" || payload.Actionable {
							t.Errorf("safe next pick missing: %s", r.stdout)
						}
					}
				})
			}
		})
	}
}

func TestRobotScoping_LabelRecipeRepoHonouredByEveryCommand(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := t.TempDir()
	// api-1 (backend, open, unblocked) blocks api-2 (backend); web-9 (frontend, open).
	writeIssuesJSONL(t, repoDir, strings.Join([]string{
		`{"id":"api-1","title":"Backend root","status":"open","issue_type":"task","priority":1,"labels":["backend"],"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-02T00:00:00Z"}`,
		`{"id":"api-2","title":"Backend follow-up","status":"open","issue_type":"task","priority":2,"labels":["backend"],"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-02T00:00:00Z","dependencies":[{"issue_id":"api-2","depends_on_id":"api-1","type":"blocks"}]}`,
		`{"id":"web-9","title":"Frontend only","status":"open","issue_type":"task","priority":2,"labels":["frontend"],"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-02T00:00:00Z"}`,
	}, "\n")+"\n")
	// The repo filter also honours source_repo; ids carry the api-/web- prefixes.

	cases := []struct {
		name       string
		flags      []string
		scopeKey   string
		scopeValue string
		excluded   string // an issue id that must not appear anywhere in the payload
	}{
		{"label", []string{"--label", "backend"}, "label", "backend", "web-9"},
		{"recipe", []string{"--recipe", "actionable"}, "recipe", "actionable", "api-2"},
		{"repo", []string{"--repo", "api"}, "repo", "api", "web-9"},
	}

	commands := loadCapabilities(t, bv)
	covered := 0
	for _, c := range commands {
		if !c.NeedsIssues || c.NeedsGit || c.NeedsSprint || c.NeedsBase || c.MutatesState {
			continue
		}
		if reason, skip := scopingSkip[c.Name]; skip {
			t.Logf("skip %s: %s", c.Name, reason)
			continue
		}
		for _, tc := range cases {
			args := append(argvFor(c.Flag, "api-1"), tc.flags...)
			r := runScoped(t, bv, repoDir, args...)
			t.Logf("%s %s: exit=%v stderr=%q", c.Name, tc.name, r.exit, strings.TrimSpace(r.stderr))
			if r.exit != nil {
				t.Errorf("%s with %v: exit %v\n%s%s", c.Name, tc.flags, r.exit, r.stdout, r.stderr)
				continue
			}
			if r.payload == nil {
				t.Errorf("%s with %v: stdout is not a JSON object:\n%s", c.Name, tc.flags, r.stdout)
				continue
			}
			scope, ok := scopeOf(t, r)
			if !ok || scope[tc.scopeKey] != tc.scopeValue {
				t.Errorf("%s with %v: envelope scope = %v, want %s=%s", c.Name, tc.flags, scope, tc.scopeKey, tc.scopeValue)
			}
			if strings.Contains(r.stdout, `"`+tc.excluded+`"`) {
				t.Errorf("%s with %v: payload mentions out-of-scope issue %s:\n%s", c.Name, tc.flags, tc.excluded, truncate(r.stdout, 800))
			}
			covered++
		}
	}
	if covered < 30 {
		t.Fatalf("matrix covered only %d (command, scope) pairs; expected at least 30", covered)
	}
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestRobotScoping_AsOfHonouredOrDeclaredUnsupported(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	old := `{"id":"hist-old","title":"Old","status":"open","issue_type":"task","priority":1,"created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-02T00:00:00Z"}`
	newer := `{"id":"hist-new","title":"New","status":"open","issue_type":"task","priority":1,"created_at":"2026-08-03T00:00:00Z","updated_at":"2026-08-04T00:00:00Z"}`
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(old+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", ".beads/issues.jsonl")
	git("commit", "-q", "-m", "old")
	oldSHA := git("rev-parse", "HEAD")
	if err := os.WriteFile(issuesPath, []byte(old+"\n"+newer+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".beads/issues.jsonl")
	git("commit", "-q", "-m", "new")

	commands := loadCapabilities(t, bv)
	honoured, declared := 0, 0
	for _, c := range commands {
		if !c.NeedsIssues || c.NeedsBase || c.MutatesState {
			continue
		}
		if reason, skip := scopingSkip[c.Name]; skip {
			t.Logf("skip %s: %s", c.Name, reason)
			continue
		}
		args := append(argvFor(c.Flag, "hist-old"), "--as-of", oldSHA)
		r := runScoped(t, bv, repoDir, args...)
		t.Logf("%s --as-of: exit=%v stderr=%q", c.Name, r.exit, strings.TrimSpace(r.stderr))
		if r.exit != nil {
			// Commands needing sprint files or git correlation data may fail on
			// this bare fixture; that is fine as long as they did not silently
			// answer from HEAD. Nothing more to assert without a payload.
			if !c.NeedsSprint && !c.NeedsGit {
				t.Errorf("%s --as-of: exit %v\n%s%s", c.Name, r.exit, r.stdout, r.stderr)
			}
			continue
		}
		if r.payload == nil {
			t.Errorf("%s --as-of: not a JSON object:\n%s", c.Name, r.stdout)
			continue
		}
		var asOf, asOfCommit, sourceKind string
		_ = json.Unmarshal(r.payload["as_of"], &asOf)
		_ = json.Unmarshal(r.payload["as_of_commit"], &asOfCommit)
		_ = json.Unmarshal(r.payload["source_kind"], &sourceKind)
		if asOf != oldSHA || !shaRE.MatchString(asOfCommit) || sourceKind != "git" {
			t.Errorf("%s --as-of: envelope as_of=%q as_of_commit=%q source_kind=%q", c.Name, asOf, asOfCommit, sourceKind)
		}
		scope, _ := scopeOf(t, r)
		unsupported, _ := scope["unsupported"].([]any)
		declaresAsOf := false
		for _, u := range unsupported {
			if u == "as_of" {
				declaresAsOf = true
			}
		}
		switch {
		case c.NeedsGit || c.NeedsSprint:
			if !declaresAsOf {
				t.Errorf("%s reads live history/sprint files and must declare as_of unsupported, got scope %v", c.Name, scope)
			}
			declared++
		default:
			if declaresAsOf {
				t.Errorf("%s analyses ctx.Issues and must not declare as_of unsupported", c.Name)
			}
			if strings.Contains(r.stdout, `"hist-new"`) {
				t.Errorf("%s --as-of %s answered from HEAD (mentions hist-new):\n%s", c.Name, oldSHA[:7], truncate(r.stdout, 800))
			}
			honoured++
		}
	}
	if honoured < 12 {
		t.Fatalf("only %d commands proved to honour --as-of; expected at least 12", honoured)
	}
	t.Logf("honoured=%d declared-unsupported=%d", honoured, declared)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
