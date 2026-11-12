package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestEmitScript_BashAndFish(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// JSONL alone supplies recommendations, but cannot prove a live tracker route.
	writeBeads(t, env, `{"id":"A","title":"Unblocker","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Blocked","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"B","depends_on_id":"A","type":"blocks"}]}`)

	tests := []struct {
		name        string
		formatFlag  string
		wantShebang string
		wantExtra   string
	}{
		{name: "bash", formatFlag: "bash", wantShebang: "#!/usr/bin/env bash", wantExtra: "set -euo pipefail"},
		{name: "zsh", formatFlag: "zsh", wantShebang: "#!/usr/bin/env zsh", wantExtra: ""},
		{name: "fish", formatFlag: "fish", wantShebang: "#!/usr/bin/env fish", wantExtra: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(bv, "--emit-script", "--script-limit=1", "--script-format="+tt.formatFlag)
			cmd.Dir = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run failed: %v\n%s", err, out)
			}
			s := string(out)
			if !strings.Contains(s, tt.wantShebang) {
				t.Fatalf("missing shebang %q:\n%s", tt.wantShebang, s)
			}
			if tt.wantExtra != "" && !strings.Contains(s, tt.wantExtra) {
				t.Fatalf("missing %q:\n%s", tt.wantExtra, s)
			}
			if !strings.Contains(s, "# 1. A: Unblocker (score:") || !strings.Contains(s, "# No verified live tracker route") {
				t.Fatalf("unbound script lost the useful issue identity or route diagnostic:\n%s", s)
			}
			for _, line := range strings.Split(s, "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") && line != tt.wantExtra {
					t.Fatalf("unbound script contains an executable action %q:\n%s", line, s)
				}
			}
			if !strings.Contains(s, "# Data hash:") {
				t.Fatalf("missing data hash header:\n%s", s)
			}
		})
	}
}

func TestEmitScript_WithRobotTriageAndContainerEpicFiltering(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	writeBeads(t, env, `{"id":"EPIC","title":"Container Epic","status":"open","priority":0,"issue_type":"epic"}
{"id":"LEAF","title":"Actionable Leaf","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"LEAF","depends_on_id":"EPIC","type":"parent-child"}]}
{"id":"BLK","title":"Blocked Leaf","status":"open","priority":1,"issue_type":"task","dependencies":[{"issue_id":"BLK","depends_on_id":"LEAF","type":"blocks"}]}`)

	// Combined --robot-triage --emit-script must emit shell script (not JSON) and exclude EPIC + BLK by default.
	cmd := exec.Command(bv, "--robot-triage", "--emit-script", "--script-limit=5")
	cmd.Dir = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-triage --emit-script failed: %v\n%s", err, out)
	}
	script := string(out)
	if !strings.HasPrefix(script, "#!/usr/bin/env bash\n") {
		t.Fatalf("expected bash shebang instead of JSON output, got:\n%s", script)
	}
	if !strings.Contains(script, "# 1. LEAF: Actionable Leaf") {
		t.Fatalf("expected actionable leaf in script, got:\n%s", script)
	}
	if strings.Contains(script, "EPIC: Container Epic") || strings.Contains(script, "BLK: Blocked Leaf") {
		t.Fatalf("default --emit-script should exclude container epic and blocked leaf, got:\n%s", script)
	}

	// With --robot-include-inactive, container epic and blocked leaf appear with non-claimable diagnostics.
	inactiveCmd := exec.Command(bv, "--robot-triage", "--emit-script", "--robot-include-inactive", "--script-limit=5")
	inactiveCmd.Dir = env
	inactiveOut, err := inactiveCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-triage --emit-script --robot-include-inactive failed: %v\n%s", err, inactiveOut)
	}
	inactiveScript := string(inactiveOut)
	if !strings.Contains(inactiveScript, "EPIC: Container Epic") || !strings.Contains(inactiveScript, "Coordinate open child tasks") {
		t.Fatalf("expected container epic diagnostic in --robot-include-inactive script, got:\n%s", inactiveScript)
	}
	if !strings.Contains(inactiveScript, "BLK: Blocked Leaf") {
		t.Fatalf("expected blocked leaf in --robot-include-inactive script, got:\n%s", inactiveScript)
	}

	// Incompatible primary robot flag with --emit-script must fail cleanly.
	badCmd := exec.Command(bv, "--robot-insights", "--emit-script")
	badCmd.Dir = env
	if badOut, err := badCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected --robot-insights --emit-script to fail, got:\n%s", badOut)
	}
}

// TestEmitScript_FiveSurfaceReadinessEquivalenceAndReadmeExamples (bv-apal.24)
// proves that default invocations of all 5 recommendation surfaces:
//   1. --robot-triage
//   2. --emit-script
//   3. --agent-brief (<dir>/triage.json + <dir>/brief.md)
//   4. --priority-brief (<file>)
//   5. --export-pages (<dir>/data/triage.json)
// emit the exact same actionable leaf recommendation IDs on a mixed fixture
// (ready leaves, container epic + blocked child, childless open epic, blocked
// task, future-deferred task), that --robot-include-inactive surfaces the
// excluded non-claimable diagnostics, and that the bash --emit-script examples
// in README.md execute cleanly and pass `bash -n` syntax validation.
func TestEmitScript_FiveSurfaceReadinessEquivalenceAndReadmeExamples(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	fixture := strings.Join([]string{
		`{"id":"L1","title":"Ready Leaf Unblocker","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		`{"id":"L2","title":"Ready Leaf Standalone","status":"open","priority":2,"issue_type":"task","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		`{"id":"EPIC-CONT","title":"Umbrella Container Epic","status":"open","priority":0,"issue_type":"epic","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		`{"id":"CHILD-BLK","title":"Blocked Open Epic Child","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-03T00:00:00Z","updated_at":"2026-08-25T00:00:00Z","dependencies":[{"issue_id":"CHILD-BLK","depends_on_id":"EPIC-CONT","type":"parent-child"},{"issue_id":"CHILD-BLK","depends_on_id":"L1","type":"blocks"}]}`,
		`{"id":"EPIC-SOLO","title":"Childless Actionable Epic","status":"open","priority":2,"issue_type":"epic","created_at":"2026-08-04T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
		`{"id":"TASK-BLK","title":"Dependency Blocked Task","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-05T00:00:00Z","updated_at":"2026-08-25T00:00:00Z","dependencies":[{"issue_id":"TASK-BLK","depends_on_id":"L1","type":"blocks"}]}`,
		`{"id":"TASK-DEF","title":"Future Deferred Task","status":"open","priority":1,"issue_type":"task","defer_until":"2099-01-01T00:00:00Z","created_at":"2026-08-06T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`,
	}, "\n") + "\n"
	writeBeads(t, env, fixture)

	runBV := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir = env
		cmd.Env = append(os.Environ(),
			"BV_NO_BROWSER=1",
			"BV_TEST_MODE=1",
			"BV_NO_SAVED_CONFIG=1",
			"SOURCE_DATE_EPOCH=1788264000",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bv %v failed: %v\n%s", args, err, out)
		}
		return out
	}

	type recEntry struct {
		ID        string `json:"id"`
		Claimable bool   `json:"claimable"`
	}

	// 1. --robot-triage
	var robotTriage struct {
		Triage struct {
			Recommendations []recEntry `json:"recommendations"`
		} `json:"triage"`
	}
	if err := json.Unmarshal(runBV("--robot-triage"), &robotTriage); err != nil {
		t.Fatalf("unmarshal --robot-triage: %v", err)
	}
	var triageIDs []string
	gotSet := map[string]bool{}
	for _, r := range robotTriage.Triage.Recommendations {
		triageIDs = append(triageIDs, r.ID)
		gotSet[r.ID] = true
	}
	wantSet := map[string]bool{"L1": true, "L2": true, "EPIC-SOLO": true}
	if !reflect.DeepEqual(gotSet, wantSet) || len(triageIDs) != 3 {
		t.Fatalf("--robot-triage actionable IDs = %v, want permutation of {L1, L2, EPIC-SOLO}", triageIDs)
	}

	// 2. --emit-script
	scriptOut := string(runBV("--emit-script", "--script-limit=10"))
	reScriptRec := regexp.MustCompile(`(?m)^# \d+\. ([A-Z0-9-]+): `)
	var scriptIDs []string
	for _, m := range reScriptRec.FindAllStringSubmatch(scriptOut, -1) {
		scriptIDs = append(scriptIDs, m[1])
	}
	if !reflect.DeepEqual(scriptIDs, triageIDs) {
		t.Fatalf("--emit-script IDs = %v, want exact match with --robot-triage %v\nscript:\n%s", scriptIDs, triageIDs, scriptOut)
	}

	// 3. --agent-brief
	agentBriefDir := filepath.Join(env, "agent-brief")
	runBV("--agent-brief", agentBriefDir)
	agentTriageRaw, err := os.ReadFile(filepath.Join(agentBriefDir, "triage.json"))
	if err != nil {
		t.Fatalf("read agent-brief/triage.json: %v", err)
	}
	var agentTriage struct {
		Recommendations []recEntry `json:"recommendations"`
	}
	if err := json.Unmarshal(agentTriageRaw, &agentTriage); err != nil {
		t.Fatalf("unmarshal agent-brief/triage.json: %v", err)
	}
	var agentBriefIDs []string
	for _, r := range agentTriage.Recommendations {
		agentBriefIDs = append(agentBriefIDs, r.ID)
	}
	if !reflect.DeepEqual(agentBriefIDs, triageIDs) {
		t.Fatalf("--agent-brief triage.json IDs = %v, want %v", agentBriefIDs, triageIDs)
	}
	agentBriefMDBytes, err := os.ReadFile(filepath.Join(agentBriefDir, "brief.md"))
	if err != nil {
		t.Fatalf("read agent-brief/brief.md: %v", err)
	}
	agentBriefMD := string(agentBriefMDBytes)

	// 4. --priority-brief
	prioBriefPath := filepath.Join(env, "priority-brief.md")
	runBV("--priority-brief", prioBriefPath)
	prioBriefBytes, err := os.ReadFile(prioBriefPath)
	if err != nil {
		t.Fatalf("read priority-brief.md: %v", err)
	}
	prioBriefMD := string(prioBriefBytes)

	for surfaceName, md := range map[string]string{
		"--agent-brief brief.md": agentBriefMD,
		"--priority-brief":       prioBriefMD,
	} {
		for _, wantID := range triageIDs {
			if !strings.Contains(md, wantID) {
				t.Fatalf("%s missing actionable item %s:\n%s", surfaceName, wantID, md)
			}
		}
		for _, excludedID := range []string{"EPIC-CONT", "CHILD-BLK", "TASK-BLK", "TASK-DEF"} {
			if strings.Contains(md, excludedID) {
				t.Fatalf("%s leaked inactive item %s:\n%s", surfaceName, excludedID, md)
			}
		}
	}

	// 5. --export-pages (<dir>/data/triage.json)
	pagesDir := filepath.Join(env, "pages-bundle")
	runBV("--export-pages", pagesDir, "--pages-include-history=false", "--no-hooks")
	pagesTriageRaw, err := os.ReadFile(filepath.Join(pagesDir, "data", "triage.json"))
	if err != nil {
		t.Fatalf("read pages data/triage.json: %v", err)
	}
	var pagesTriage struct {
		Recommendations []recEntry `json:"recommendations"`
	}
	if err := json.Unmarshal(pagesTriageRaw, &pagesTriage); err != nil {
		t.Fatalf("unmarshal pages data/triage.json: %v", err)
	}
	var pagesIDs []string
	for _, r := range pagesTriage.Recommendations {
		pagesIDs = append(pagesIDs, r.ID)
	}
	if !reflect.DeepEqual(pagesIDs, triageIDs) {
		t.Fatalf("--export-pages data/triage.json IDs = %v, want %v", pagesIDs, triageIDs)
	}

	// 6. --robot-include-inactive opt-in surfaces all 4 excluded items as non-claimable diagnostics.
	var inactiveTriage struct {
		Triage struct {
			Recommendations []recEntry `json:"recommendations"`
		} `json:"triage"`
	}
	if err := json.Unmarshal(runBV("--robot-triage", "--robot-include-inactive"), &inactiveTriage); err != nil {
		t.Fatalf("unmarshal inactive --robot-triage: %v", err)
	}
	inactiveClaimable := map[string]bool{}
	for _, r := range inactiveTriage.Triage.Recommendations {
		inactiveClaimable[r.ID] = r.Claimable
	}
	for _, excludedID := range []string{"EPIC-CONT", "CHILD-BLK", "TASK-BLK", "TASK-DEF"} {
		claimable, present := inactiveClaimable[excludedID]
		if !present || claimable {
			t.Fatalf("--robot-include-inactive expected %s present with claimable=false, got present=%v claimable=%v", excludedID, present, claimable)
		}
	}

	// 7. Extract and execute the bash --emit-script examples directly from README.md and validate via `bash -n`.
	bashBin, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found on PATH")
	}
	readme := repoFile(t, "README.md")
	var readmeBashExamples []string
	for _, line := range strings.Split(readme, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "bv ") && strings.Contains(trimmed, "--emit-script") && !strings.Contains(trimmed, "--script-format=") {
			readmeBashExamples = append(readmeBashExamples, trimmed)
		}
	}
	if len(readmeBashExamples) < 2 {
		t.Fatalf("expected at least 2 bash --emit-script examples in README.md, found %v", readmeBashExamples)
	}
	for _, example := range readmeBashExamples {
		argPart := strings.TrimSpace(strings.Split(example, ">")[0])
		fields := strings.Fields(strings.TrimPrefix(argPart, "bv "))
		emitted := runBV(fields...)
		if !bytes.HasPrefix(emitted, []byte("#!/usr/bin/env bash\n")) {
			t.Fatalf("README example %q did not emit bash shebang:\n%s", example, emitted)
		}
		syntaxCheck := exec.Command(bashBin, "-n")
		syntaxCheck.Stdin = bytes.NewReader(emitted)
		if out, err := syntaxCheck.CombinedOutput(); err != nil {
			t.Fatalf("README example %q emitted invalid bash syntax: %v\n%s\nscript:\n%s", example, err, out, emitted)
		}
	}
}
