package main_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRobotSourceAuthority(t *testing.T) {
	bv := buildBvBinary(t)
	for _, tc := range []struct {
		name       string
		apiPresent bool
		webPresent bool
		webEnabled bool
		state      string
		loaded     int
		failed     int
		disabled   int
	}{
		{"two_successful_including_empty", true, true, true, "complete", 2, 0, 0},
		{"one_failed", true, false, true, "partial", 1, 1, 0},
		{"all_failed", false, false, true, "unknown", 0, 2, 0},
		{"disabled_is_not_failed", true, false, false, "complete", 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(rel, contents string) {
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.apiPresent {
				write("api/.beads/issues.jsonl", `{"id":"safe","title":"Useful surviving work","status":"open","issue_type":"task","labels":["backend"]}`+"\n")
			}
			if tc.webPresent {
				write("web/.beads/issues.jsonl", "")
			}
			write(".bv/workspace.yaml", fmt.Sprintf("name: authority-test\nrepos:\n  - name: api\n    path: api\n    prefix: api-\n  - name: web\n    path: web\n    prefix: web-\n    enabled: %t\ndiscovery:\n  enabled: false\n", tc.webEnabled))
			cmd := exec.Command(bv, "--robot-next", "--workspace", filepath.Join(root, ".bv", "workspace.yaml"))
			cmd.Dir = root
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err != nil) != (tc.loaded == 0) {
				t.Fatalf("exit=%v loaded=%d\nstdout=%s\nstderr=%s", err, tc.loaded, stdout.String(), stderr.String())
			}
			var payload struct {
				Actionable   bool   `json:"actionable"`
				ClaimCommand string `json:"claim_command"`
				ID           string `json:"id"`
				Diagnostic   *struct {
					ID string `json:"id"`
				} `json:"diagnostic_top_pick"`
				Actions struct {
					Claim any `json:"claim"`
					Show  any `json:"show"`
				} `json:"actions"`
				AuthorityHash string `json:"authority_hash"`
				Authority     *struct {
					State     string `json:"state"`
					ClaimSafe bool   `json:"claim_safe"`
					Loaded    int    `json:"loaded"`
					Failed    int    `json:"failed"`
					Disabled  int    `json:"disabled"`
					Valid     int    `json:"valid"`
					Sources   []struct {
						Status string `json:"status"`
					} `json:"sources"`
				} `json:"source_authority"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("robot source failures must remain structured JSON: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			if payload.Authority == nil || payload.Authority.State != tc.state || payload.AuthorityHash == "" {
				t.Fatalf("missing or incorrect source authority: %+v\nstdout=%s\nstderr=%s", payload, stdout.String(), stderr.String())
			}
			a := payload.Authority
			if a.Loaded != tc.loaded || a.Failed != tc.failed || a.Disabled != tc.disabled || len(a.Sources) != 2 {
				t.Fatalf("source accounting does not reconcile: %+v", a)
			}
			wantValid := 0
			if tc.apiPresent {
				wantValid = 1
			}
			if a.Valid != wantValid {
				t.Fatalf("valid=%d want=%d; empty healthy source must remain successful", a.Valid, wantValid)
			}
			if tc.state == "complete" {
				if !a.ClaimSafe {
					t.Fatalf("complete sources must preserve graph authority: %+v", payload)
				}
			} else if a.ClaimSafe {
				t.Fatalf("incomplete sources emitted a proven claim: %+v", payload)
			}
			if payload.Actionable || payload.ID != "" || payload.ClaimCommand != "" || payload.Actions.Claim != nil || payload.Actions.Show != nil {
				t.Fatalf("metadata-free workspace invented a live tracker route: %+v", payload)
			}
			if tc.apiPresent && (payload.Diagnostic == nil || payload.Diagnostic.ID != "api-safe") {
				t.Fatalf("surviving source lost its useful diagnostic candidate: %+v", payload)
			}
		})
	}
}

func TestPartialWorkspaceAuthoritySurvivesDerivedOutputs(t *testing.T) {
	bv := buildBvBinary(t)
	root := writeWorkspaceFixture(t)
	config := filepath.Join(root, ".bv", "workspace.yaml")
	contents, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	contents = bytes.Replace(contents, []byte("path: apps/web"), []byte("path: missing-web"), 1)
	if err := os.WriteFile(config, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	var authorityHash string
	for _, args := range [][]string{
		{"--robot-triage"}, {"--robot-triage", "--brief"}, {"--robot-triage-by-track"},
		{"--robot-triage-by-label"}, {"--robot-plan"}, {"--robot-graph"}, {"--robot-insights"},
		{"--search", "API", "--robot-search"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := exec.Command(bv, append(args, "--workspace", config)...)
			cmd.Dir = root
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
				t.Fatalf("%v\nstdout=%s", err, stdout.String())
			}
			authority, ok := payload["source_authority"].(map[string]any)
			if !ok || authority["state"] != "partial" || authority["claim_safe"] != false || authority["readiness"] != "provisional" {
				t.Fatalf("derived output lost provisional source authority: %s", stdout.String())
			}
			if payload["scope_hash"] == "" || payload["scope_hash"] == nil || payload["authority_hash"] == "" || payload["authority_hash"] == nil {
				t.Fatalf("missing distinct source/scope identity: %s", stdout.String())
			}
			if authorityHash == "" {
				authorityHash = payload["authority_hash"].(string)
			}
			if payload["authority_hash"] != authorityHash {
				t.Fatalf("derived view changed source authority identity: %v != %s", payload["authority_hash"], authorityHash)
			}
			var inspect func(any)
			inspect = func(value any) {
				switch value := value.(type) {
				case map[string]any:
					for key, item := range value {
						if (key == "claim_command" || key == "claim_top") && item != "" {
							t.Errorf("partial output emitted %s=%v", key, item)
						}
						if (key == "claimable" || key == "actionable") && item == true {
							t.Errorf("partial output emitted proven %s", key)
						}
						inspect(item)
					}
				case []any:
					for _, item := range value {
						inspect(item)
					}
				}
			}
			inspect(payload)
			// The partial report must still contain useful surviving issue data.
			if !strings.Contains(stdout.String(), "api-AUTH-1") {
				t.Fatalf("partial analysis discarded the surviving repository: %s", stdout.String())
			}
		})
	}
}

func runAuthorityFixture(t *testing.T, bv, dir string, extraEnv []string, args ...string) map[string]any {
	t.Helper()
	cmd := exec.Command(bv, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	return payload
}

// Source fixtures prove graph authority without pretending that arbitrary
// JSONL or SQLite files are mutable trackers. Live command execution has its
// own real-tracker regression in TestRobotActionRoutesLiveTrackers.
func assertUnboundNextCandidate(t *testing.T, payload map[string]any, id string, complete bool) {
	t.Helper()
	authority, ok := payload["source_authority"].(map[string]any)
	if !ok || authority["claim_safe"] != complete {
		t.Fatalf("graph authority claim_safe must be %t: %+v", complete, payload)
	}
	diagnostic, ok := payload["diagnostic_top_pick"].(map[string]any)
	if !ok || diagnostic["id"] != id {
		t.Fatalf("expected diagnostic candidate %q: %+v", id, payload)
	}
	if payload["actionable"] != false || payload["id"] != nil || payload["claim_command"] != nil || payload["show_command"] != nil {
		t.Fatalf("unbound source invented an executable action: %+v", payload)
	}
	if actions, ok := payload["actions"].(map[string]any); ok && (actions["claim"] != nil || actions["show"] != nil) {
		t.Fatalf("unbound source invented a nested tracker route: %+v", actions)
	}
}

func TestRobotSourceAuthorityIdentityTracksSourceFailure(t *testing.T) {
	bv := buildBvBinary(t)
	root := writeWorkspaceFixture(t)
	if err := os.WriteFile(filepath.Join(root, "apps/web/.beads/issues.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".bv/workspace.yaml")
	healthyConfig, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	run := func() map[string]any {
		return runAuthorityFixture(t, bv, root, nil, "--robot-next", "--workspace", config)
	}
	healthy := run()
	failedConfig := bytes.Replace(healthyConfig, []byte("path: apps/web"), []byte("path: missing-web"), 1)
	if err := os.WriteFile(config, failedConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	partial := run()
	if healthy["data_hash"] != partial["data_hash"] || healthy["authority_hash"] == partial["authority_hash"] {
		t.Fatalf("unchanged surviving records must keep data_hash but changed source failure must alter authority_hash: healthy=%+v partial=%+v", healthy, partial)
	}
	assertUnboundNextCandidate(t, healthy, "api-AUTH-1", true)
	assertUnboundNextCandidate(t, partial, "api-AUTH-1", false)
	if err := os.WriteFile(config, healthyConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered := run()
	if recovered["authority_hash"] != healthy["authority_hash"] {
		t.Fatalf("restoring the healthy source must recover the original authority: %+v", recovered)
	}
	assertUnboundNextCandidate(t, recovered, "api-AUTH-1", true)
}

func TestRobotSourceAuthorityJSONLAccountingAndWarningBound(t *testing.T) {
	bv := buildBvBinary(t)
	root := t.TempDir()
	beads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beads, "issues.jsonl")
	safe := `{"id":"safe","title":"Safe issue","status":"open","issue_type":"task","dependencies":[{"depends_on_id":"gone","type":"blocks"}]}` + "\n"
	valid := safe + `{"id":"gone","title":"Deleted predecessor","status":"tombstone","issue_type":"task"}` + "\n" + `{"_type":"memory","id":"memory"}` + "\n"
	if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	healthy := runAuthorityFixture(t, bv, root, nil, "--robot-next")
	assertUnboundNextCandidate(t, healthy, "safe", true)
	oversized := `{"id":"huge","title":"Huge","status":"open","issue_type":"task","description":"` + strings.Repeat("x", 2*1024*1024) + `"}` + "\n"
	corrupt := valid + safe + strings.Repeat("{invalid\n", 25) + oversized
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := runAuthorityFixture(t, bv, root, []string{"BV_MAX_LINE_SIZE_MB=1"}, "--robot-next")
	authority, ok := partial["source_authority"].(map[string]any)
	if !ok {
		t.Fatalf("missing authority: %+v", partial)
	}
	for key, want := range map[string]any{"state": "partial", "claim_safe": false, "valid": float64(2), "errors": float64(27), "skipped": float64(1), "visible": float64(1), "tombstones": float64(1), "warning_count": float64(27)} {
		if authority[key] != want {
			t.Errorf("%s=%v want=%v; authority=%+v", key, authority[key], want, authority)
		}
	}
	sources, ok := authority["sources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("source count does not reconcile: %+v", authority)
	}
	source := sources[0].(map[string]any)
	warnings, ok := source["warnings"].([]any)
	if !ok || len(warnings) != 10 || source["warning_count"] != float64(27) {
		t.Fatalf("warning cap lost original count: %+v", source)
	}
	if partial["actionable"] != false || partial["claim_command"] != nil {
		t.Fatalf("record loss emitted a claim: %+v", partial)
	}
	if healthy["data_hash"] != partial["data_hash"] || healthy["authority_hash"] == partial["authority_hash"] {
		t.Fatalf("parse loss must change authority even when visible records are identical")
	}
}

func TestRobotSourceAuthorityStaleBDExport(t *testing.T) {
	bv := buildBvBinary(t)
	root := t.TempDir()
	beads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beads, "issues.jsonl"), []byte(`{"id":"safe","title":"Existing export","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	healthy := runAuthorityFixture(t, bv, root, nil, "--robot-next")
	assertUnboundNextCandidate(t, healthy, "safe", true)
	if err := os.WriteFile(filepath.Join(beads, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(beads, "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := runAuthorityFixture(t, bv, root, []string{"PATH=" + t.TempDir()}, "--robot-next")
	authority, ok := stale["source_authority"].(map[string]any)
	if !ok || authority["state"] != "partial" || authority["claim_safe"] != false || stale["actionable"] != false || stale["claim_command"] != nil {
		t.Fatalf("stale bd export emitted a proven claim or lost diagnostics: %+v", stale)
	}
	source := authority["sources"].([]any)[0].(map[string]any)
	if source["stale"] != true || source["warning_count"].(float64) == 0 {
		t.Fatalf("stale fallback reason missing: %+v", source)
	}
	if healthy["data_hash"] != stale["data_hash"] || healthy["authority_hash"] == stale["authority_hash"] {
		t.Fatalf("stale authority must differ despite identical issue content")
	}
}

func TestRobotSourceAuthorityUsesResolvedRedirect(t *testing.T) {
	bv := buildBvBinary(t)
	root := t.TempDir()
	local := filepath.Join(root, ".beads")
	resolved := filepath.Join(root, "actual", ".beads")
	for _, dir := range []string{local, resolved} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "issues.jsonl"), []byte(`{"id":"trap","title":"Wrong local source","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "redirect"), []byte(resolved+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(resolved, "issues.jsonl")
	if err := os.WriteFile(path, []byte(`{"id":"safe","title":"Redirected authority","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := runAuthorityFixture(t, bv, root, nil, "--robot-next")
	assertUnboundNextCandidate(t, payload, "safe", true)
	if payload["source_path"] != path {
		t.Fatalf("redirected source did not retain the resolved source identity: %+v", payload)
	}
	authority, ok := payload["source_authority"].(map[string]any)
	if !ok || authority["state"] != "complete" {
		t.Fatalf("resolved source authority missing: %+v", payload)
	}
	source := authority["sources"].([]any)[0].(map[string]any)
	if source["source_path"] != path || source["valid"] != float64(1) {
		t.Fatalf("report names a source that did not back the snapshot: %+v", source)
	}
}

func TestRobotSourceAuthoritySQLiteReadLoss(t *testing.T) {
	bv := buildBvBinary(t)
	root := t.TempDir()
	path := filepath.Join(root, "beads.db")
	writeDB := func(statements string) {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(statements); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	writeDB(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER);
INSERT INTO issues VALUES ('safe', 'Safe candidate', 'open', 1);`)
	healthy := runAuthorityFixture(t, bv, root, nil, "--db", path, "--robot-next")
	assertUnboundNextCandidate(t, healthy, "safe", true)
	writeDB(`INSERT INTO issues VALUES ('bad', 'Unreadable priority', 'open', 'invalid');
CREATE TABLE dependencies (issue_id TEXT, depends_on_id TEXT, dependency_type TEXT);
INSERT INTO dependencies VALUES ('safe', NULL, 'blocks');`)
	partial := runAuthorityFixture(t, bv, root, nil, "--db", path, "--robot-next")
	authority, ok := partial["source_authority"].(map[string]any)
	if !ok || authority["state"] != "partial" || authority["errors"] != float64(1) || authority["read_errors"] != float64(1) || authority["valid"] != float64(1) || partial["actionable"] != false || partial["claim_command"] != nil {
		t.Fatalf("SQLite row/edge loss was erased or emitted a claim: %+v", partial)
	}
	writeDB(`UPDATE issues SET priority = 2, status = 'closed' WHERE id = 'bad'; UPDATE dependencies SET depends_on_id = 'bad' WHERE issue_id = 'safe';`)
	repaired := runAuthorityFixture(t, bv, root, nil, "--db", path, "--robot-next")
	assertUnboundNextCandidate(t, repaired, "safe", true)
}

func TestWorkspaceRobotTriageCleanOutput(t *testing.T) {
	bv := buildBvBinary(t)

	workspaceRoot := t.TempDir()
	configPath := filepath.Join(workspaceRoot, ".bv", "workspace.yaml")

	// Create two repos with issues.
	apiBeadsDir := filepath.Join(workspaceRoot, "services", "api", ".beads")
	webBeadsDir := filepath.Join(workspaceRoot, "apps", "web", ".beads")
	if err := os.MkdirAll(apiBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir api beads: %v", err)
	}
	if err := os.MkdirAll(webBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir web beads: %v", err)
	}

	apiIssues := `{"id":"AUTH-1","title":"API auth","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(apiBeadsDir, "issues.jsonl"), []byte(apiIssues+"\n"), 0o644); err != nil {
		t.Fatalf("write api issues.jsonl: %v", err)
	}

	// Cross-repo dependency references must already be namespaced.
	webIssues := `{"id":"UI-1","title":"Web UI","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"UI-1","depends_on_id":"api-AUTH-1","type":"blocks"}]}`
	if err := os.WriteFile(filepath.Join(webBeadsDir, "issues.jsonl"), []byte(webIssues+"\n"), 0o644); err != nil {
		t.Fatalf("write web issues.jsonl: %v", err)
	}

	config := `
name: test-workspace
repos:
  - name: api
    path: services/api
    prefix: api-
  - name: web
    path: apps/web
    prefix: web-
discovery:
  enabled: false
`
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir .bv: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write workspace.yaml: %v", err)
	}

	cmd := exec.Command(bv, "--robot-triage", "--workspace", configPath)
	cmd.Dir = workspaceRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-triage --workspace failed: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("expected empty stderr for robot JSON, got: %s", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON on stdout: %v\nstdout=%s", err, stdout.String())
	}
	if _, ok := payload["generated_at"]; !ok {
		t.Fatalf("missing generated_at")
	}
	if _, ok := payload["triage"]; !ok {
		t.Fatalf("missing triage")
	}
}

// writeWorkspaceFixture builds a two-repo workspace: api (AUTH-1, AUTH-2)
// and web (UI-1 blocked by api-AUTH-1), with discovery disabled so only the
// listed repos load. It returns the workspace root.
func writeWorkspaceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("services/api/.beads/issues.jsonl",
		`{"id":"AUTH-1","title":"API auth","status":"open","priority":1,"issue_type":"task"}
{"id":"AUTH-2","title":"API tokens","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"AUTH-2","depends_on_id":"AUTH-1","type":"blocks"}]}
`)
	write("apps/web/.beads/issues.jsonl",
		`{"id":"UI-1","title":"Web UI","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"UI-1","depends_on_id":"api-AUTH-1","type":"blocks"}]}
`)
	write(".bv/workspace.yaml", `
name: test-workspace
repos:
  - name: api
    path: services/api
    prefix: api-
  - name: web
    path: apps/web
    prefix: web-
discovery:
  enabled: false
`)
	// A directory with no .beads of its own, nested under the workspace root.
	if err := os.MkdirAll(filepath.Join(root, "docs", "notes"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	return root
}

type workspaceGraphPayload struct {
	SourceKind string `json:"source_kind"`
	SourcePath string `json:"source_path"`
	Scope      struct {
		Repo string `json:"repo"`
	} `json:"scope"`
	Adjacency struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	} `json:"adjacency"`
}

func runWorkspaceGraph(t *testing.T, bv, dir string, extra ...string) workspaceGraphPayload {
	t.Helper()
	cmd := exec.Command(bv, append([]string{"--robot-graph"}, extra...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-graph %v in %s failed: %v\nstderr=%s\nstdout=%s", extra, dir, err, stderr.String(), stdout.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("robot JSON must keep stderr empty, got: %s", got)
	}
	var p workspaceGraphPayload
	if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
		t.Fatalf("invalid JSON: %v\nstdout=%s", err, stdout.String())
	}
	return p
}

// TestWorkspaceAutoDiscoveryFromNestedDir (I2): with no .beads reachable, bv
// finds .bv/workspace.yaml in a parent directory by itself, namespaces every
// issue with its repo prefix, keeps the cross-repo dependency, and --repo
// narrows the graph to one repo.
func TestWorkspaceAutoDiscoveryFromNestedDir(t *testing.T) {
	bv := buildBvBinary(t)
	root := writeWorkspaceFixture(t)
	// Allow real parent workspace discovery within this fixture while keeping
	// Git from routing its nested directory into the worker's source checkout.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DIR", "")
	nested := filepath.Join(root, "docs", "notes")

	p := runWorkspaceGraph(t, bv, nested)
	if p.SourceKind != "workspace" || p.SourcePath != filepath.Join(root, ".bv", "workspace.yaml") {
		t.Fatalf("envelope source=%s/%s; want workspace/%s", p.SourceKind, p.SourcePath, filepath.Join(root, ".bv", "workspace.yaml"))
	}
	ids := map[string]bool{}
	for _, n := range p.Adjacency.Nodes {
		ids[n.ID] = true
	}
	for _, want := range []string{"api-AUTH-1", "api-AUTH-2", "web-UI-1"} {
		if !ids[want] {
			t.Fatalf("namespaced id %s missing from nodes %v", want, ids)
		}
	}
	if ids["AUTH-1"] || ids["UI-1"] {
		t.Fatalf("unprefixed ids leaked into the workspace graph: %v", ids)
	}
	var crossRepo, intraRepo bool
	for _, e := range p.Adjacency.Edges {
		if (e.From == "web-UI-1" && e.To == "api-AUTH-1") || (e.From == "api-AUTH-1" && e.To == "web-UI-1") {
			crossRepo = true
		}
		if (e.From == "api-AUTH-2" && e.To == "api-AUTH-1") || (e.From == "api-AUTH-1" && e.To == "api-AUTH-2") {
			intraRepo = true
		}
	}
	if !crossRepo || !intraRepo {
		t.Fatalf("expected the cross-repo (web→api) and intra-repo edges, got %+v", p.Adjacency.Edges)
	}

	// --repo api: only the api repo's two issues and their edge remain.
	api := runWorkspaceGraph(t, bv, nested, "--repo", "api")
	if len(api.Adjacency.Nodes) != 2 || api.Scope.Repo != "api" {
		t.Fatalf("--repo api: nodes=%d scope.repo=%q; want 2 nodes scoped to api: %+v", len(api.Adjacency.Nodes), api.Scope.Repo, api.Adjacency.Nodes)
	}
	for _, n := range api.Adjacency.Nodes {
		if !strings.HasPrefix(n.ID, "api-") {
			t.Fatalf("--repo api leaked %s", n.ID)
		}
	}

	// From the workspace root itself (no .beads there either) discovery also applies.
	if rootView := runWorkspaceGraph(t, bv, root); len(rootView.Adjacency.Nodes) != 3 {
		t.Fatalf("from the workspace root: nodes=%d; want 3", len(rootView.Adjacency.Nodes))
	}

	// Inside a member repo the repo's own .beads wins unless --workspace is passed.
	single := runWorkspaceGraph(t, bv, filepath.Join(root, "services", "api"))
	if single.SourceKind == "workspace" || len(single.Adjacency.Nodes) != 2 {
		t.Fatalf("inside services/api expected the single-repo view (2 nodes), got source=%s nodes=%d", single.SourceKind, len(single.Adjacency.Nodes))
	}
	forced := runWorkspaceGraph(t, bv, filepath.Join(root, "services", "api"), "--workspace", filepath.Join(root, ".bv", "workspace.yaml"))
	if forced.SourceKind != "workspace" || len(forced.Adjacency.Nodes) != 3 {
		t.Fatalf("--workspace override inside a member repo: source=%s nodes=%d; want workspace/3", forced.SourceKind, len(forced.Adjacency.Nodes))
	}
}
