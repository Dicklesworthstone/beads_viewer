package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// TestRobotPlanAndPriorityIncludeMetadata runs the built binary against a tiny fixture project
// to assert that robot-plan and robot-priority include data_hash, analysis_config, and status.
func TestRobotPlanAndPriorityIncludeMetadata(t *testing.T) {
	dir := t.TempDir()
	// create minimal .beads directory with beads.jsonl
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	beads := `{"id":"TEST-1","title":"A","status":"open","priority":1,"issue_type":"task"}
{"id":"TEST-2","title":"B","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"TEST-2","depends_on_id":"TEST-1","type":"blocks"}]}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	runAndCheck := func(flag string) {
		cmd := exec.Command(exe, flag)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s failed: %v, out=%s", flag, err, string(out))
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("%s json: %v", flag, err)
		}
		if _, ok := payload["data_hash"]; !ok {
			t.Fatalf("%s missing data_hash", flag)
		}
		if _, ok := payload["analysis_config"]; !ok {
			t.Fatalf("%s missing analysis_config", flag)
		}
		statusAny, ok := payload["status"]
		if !ok {
			t.Fatalf("%s missing status", flag)
		}

		status, ok := statusAny.(map[string]any)
		if !ok {
			t.Fatalf("%s status not an object", flag)
		}

		// Ensure the status contract is usable at process exit (no pending/empty states).
		expected := []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"}
		for _, metric := range expected {
			entryAny, ok := status[metric]
			if !ok {
				t.Fatalf("%s status missing %s", flag, metric)
			}
			entry, ok := entryAny.(map[string]any)
			if !ok {
				t.Fatalf("%s status.%s not an object", flag, metric)
			}
			stateAny, ok := entry["state"]
			if !ok {
				t.Fatalf("%s status.%s missing state", flag, metric)
			}
			state, _ := stateAny.(string)
			if state == "" {
				t.Fatalf("%s status.%s state empty", flag, metric)
			}
			if state == "pending" {
				t.Fatalf("%s status.%s still pending at exit", flag, metric)
			}
		}
	}

	runAndCheck("--robot-plan")
	runAndCheck("--robot-priority")
}

// buildTestBinary builds the current module's bv binary for testing.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "bv-testbin")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = "." // build current package
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bv: %v, out=%s", err, string(out))
	}
	return exe
}

// TestTOONOutputFormat verifies that --format=toon produces valid TOON output (bd-2lmf)
func TestTOONOutputFormat(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON tests")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Test Issue","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	// Test TOON output for robot-next
	cmd := exec.Command(exe, "--robot-next", "--format=toon")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with toon failed: %v", err)
	}

	// TOON output should not start with { (that's JSON)
	toonOut := string(out)
	if len(toonOut) > 0 && toonOut[0] == '{' {
		t.Fatalf("TOON output looks like JSON, expected TOON format: %s", toonOut[:min(100, len(toonOut))])
	}

	// Should contain key: value pattern typical of TOON
	if !containsKeyValuePattern(toonOut) {
		t.Fatalf("TOON output doesn't look like TOON: %s", toonOut[:min(200, len(toonOut))])
	}
}

func TestRobotNextFailClosedWhenNoClaimableItem(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"BLOCKED-1","title":"Blocked high impact","status":"blocked","priority":0,"issue_type":"bug"}
{"id":"OWNED-1","title":"Already owned","status":"open","assignee":"OtherAgent","priority":1,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)
	cmd := exec.Command(exe, "--robot-next", "--format=json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, out)
	}
	if got := payload["actionable"]; got != false {
		t.Fatalf("actionable = %v, want false; payload=%v", got, payload)
	}
	if _, ok := payload["claim_command"]; ok {
		t.Fatalf("fail-closed robot-next must not emit claim_command: %s", out)
	}
	if _, ok := payload["status"].(map[string]any); !ok {
		t.Fatalf("robot-next missing metric status: %s", out)
	}
	degraded, ok := payload["degraded"].([]any)
	if !ok || len(degraded) == 0 {
		t.Fatalf("robot-next fail-closed response missing degraded[]: %s", out)
	}
	first, ok := degraded[0].(map[string]any)
	if !ok || first["code"] != "no_actionable_recommendation" {
		t.Fatalf("unexpected degraded payload: %v", degraded)
	}
}

func TestRobotNextEmitsClaimOnlyForSafeTopPick(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"READY-1","title":"Ready work","status":"open","priority":1,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)
	cmd := exec.Command(exe, "--robot-next", "--format=json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, out)
	}
	if got := payload["actionable"]; got != true {
		t.Fatalf("actionable = %v, want true; payload=%v", got, payload)
	}
	if got := payload["id"]; got != "READY-1" {
		t.Fatalf("id = %v, want READY-1; payload=%v", got, payload)
	}
	claim, ok := payload["claim_command"].(string)
	if !ok || !strings.Contains(claim, "READY-1") {
		t.Fatalf("safe robot-next missing claim command for READY-1: %s", out)
	}
	if _, ok := payload["status"].(map[string]any); !ok {
		t.Fatalf("robot-next missing metric status: %s", out)
	}
}

func TestRobotCommandsExcludeFutureDeferredClosedAndTombstoneIssues(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"FUTURE-1","title":"Deferred by scheduler","status":"open","priority":0,"issue_type":"task","defer_until":"2026-09-20T07:00:00Z"}
{"id":"READY-1","title":"Ready work","status":"open","priority":4,"issue_type":"task"}
{"id":"CLOSED-1","title":"Closed work","status":"closed","priority":0,"issue_type":"task"}
{"id":"TOMBSTONE-1","title":"Deleted work","status":"tombstone","priority":0,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)
	run := func(flag string, dst any) []byte {
		t.Helper()
		cmd := exec.Command(exe, flag, "--format=json")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1787320800")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s failed: %v", flag, err)
		}
		if err := json.Unmarshal(out, dst); err != nil {
			t.Fatalf("%s json: %v\n%s", flag, err, out)
		}
		return out
	}

	var next struct {
		Actionable bool   `json:"actionable"`
		ID         string `json:"id"`
		ClaimCmd   string `json:"claim_command"`
	}
	nextOut := run("--robot-next", &next)
	if !next.Actionable || next.ID != "READY-1" || !strings.Contains(next.ClaimCmd, "READY-1") {
		t.Fatalf("robot-next selected non-ready work: %s", nextOut)
	}

	var triage struct {
		Triage struct {
			QuickRef struct {
				TopPicks []analysis.TopPick `json:"top_picks"`
			} `json:"quick_ref"`
			Recommendations []analysis.Recommendation `json:"recommendations"`
		} `json:"triage"`
	}
	triageOut := run("--robot-triage", &triage)
	if len(triage.Triage.QuickRef.TopPicks) == 0 || triage.Triage.QuickRef.TopPicks[0].ID != "READY-1" {
		t.Fatalf("robot-triage did not lead with READY-1: %s", triageOut)
	}
	for _, pick := range triage.Triage.QuickRef.TopPicks {
		switch pick.ID {
		case "FUTURE-1", "CLOSED-1", "TOMBSTONE-1":
			t.Fatalf("robot-triage leaked non-ready pick %s: %s", pick.ID, triageOut)
		}
	}
	for _, rec := range triage.Triage.Recommendations {
		if rec.ID == "FUTURE-1" && (!strings.Contains(rec.Action, "Wait until") || strings.Contains(strings.Join(rec.Reasons, " "), "available for work")) {
			t.Fatalf("robot-triage described future-deferred work as ready: %#v", rec)
		}
	}
}

func TestRobotNextClaimablePickRejectsAssignedTopPick(t *testing.T) {
	picks := []analysis.TopPick{{
		ID:    "ASSIGNED-1",
		Title: "Already owned",
		Score: 100,
	}}
	issues := []model.Issue{{
		ID:        "ASSIGNED-1",
		Title:     "Already owned",
		Status:    model.StatusOpen,
		IssueType: model.TypeTask,
		Assignee:  " cc11 ",
	}}

	_, diagnostic, reasons, ok := robotNextClaimablePick(picks, issues, time.Time{})
	if ok {
		t.Fatalf("assigned top pick must not be claimable")
	}
	if diagnostic == nil || diagnostic.ID != "ASSIGNED-1" {
		t.Fatalf("diagnostic = %+v, want ASSIGNED-1", diagnostic)
	}
	if len(reasons) == 0 || !strings.Contains(strings.Join(reasons, "; "), "assigned") {
		t.Fatalf("reasons = %v, want assigned reason", reasons)
	}
}

// TestTOONRoundTrip verifies that TOON output can be decoded back to JSON (bd-2lmf)
func TestTOONRoundTrip(t *testing.T) {
	// Check if tru binary is available
	truPath, err := exec.LookPath("tru")
	if err != nil {
		t.Skip("tru binary not available, skipping TOON round-trip test")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Round Trip Test","status":"open","priority":2,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	// Get TOON output
	cmd := exec.Command(exe, "--robot-next", "--format=toon")
	cmd.Dir = dir
	toonOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with toon failed: %v", err)
	}

	// Decode TOON back to JSON using tru --decode
	decodeCmd := exec.Command(truPath, "--decode")
	decodeCmd.Stdin = strings.NewReader(string(toonOut))
	jsonOut, err := decodeCmd.Output()
	if err != nil {
		t.Fatalf("tru --decode failed: %v", err)
	}

	// Verify the decoded JSON is valid and contains expected fields
	var payload map[string]interface{}
	if err := json.Unmarshal(jsonOut, &payload); err != nil {
		t.Fatalf("decoded JSON is invalid: %v, content: %s", err, string(jsonOut))
	}

	// Check required fields are present
	if _, ok := payload["id"]; !ok {
		t.Error("decoded payload missing 'id' field")
	}
	if _, ok := payload["title"]; !ok {
		t.Error("decoded payload missing 'title' field")
	}
	if _, ok := payload["generated_at"]; !ok {
		t.Error("decoded payload missing 'generated_at' field")
	}
}

// TestTOONTokenStats verifies that --stats produces token statistics on stderr (bd-2lmf)
func TestTOONTokenStats(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON stats test")
	}

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	beads := `{"id":"TEST-1","title":"Stats Test Issue","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)

	// Test --stats flag with TOON output
	cmd := exec.Command(exe, "--robot-next", "--format=toon", "--stats")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	_, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-next with stats failed: %v", err)
	}

	stderrStr := stderr.String()
	// Should contain token statistics
	if !strings.Contains(stderrStr, "tok") || !strings.Contains(stderrStr, "savings") {
		t.Errorf("--stats should produce token statistics on stderr, got: %s", stderrStr)
	}
}

// TestTOONSchemaOutput verifies that --robot-schema works with TOON format (bd-2lmf)
func TestTOONSchemaOutput(t *testing.T) {
	// Check if tru binary is available
	if _, err := exec.LookPath("tru"); err != nil {
		t.Skip("tru binary not available, skipping TOON schema test")
	}

	exe := buildTestBinary(t)

	// Test --robot-schema with TOON format
	cmd := exec.Command(exe, "--robot-schema", "--format=toon")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("robot-schema with toon failed: %v", err)
	}

	toonOut := string(out)
	// Should produce valid TOON output
	if len(toonOut) > 0 && toonOut[0] == '{' {
		t.Fatalf("TOON output looks like JSON, expected TOON format")
	}

	// Should contain schema_version key
	if !strings.Contains(toonOut, "schema_version") {
		t.Error("TOON schema output missing schema_version")
	}
}

// containsKeyValuePattern checks if the string looks like TOON format
func containsKeyValuePattern(s string) bool {
	// TOON format typically has lines like "key: value" without the JSON braces/quotes
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Look for key: value pattern (not JSON's "key": value)
		if strings.Contains(trimmed, ": ") && !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "\"") {
			return true
		}
	}
	return false
}
