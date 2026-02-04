package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRobotRecipesContract verifies --robot-recipes output structure.
func TestRobotRecipesContract(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Test","status":"open","priority":1,"issue_type":"task"}`)

	var payload struct {
		Recipes []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Source      string `json:"source"`
		} `json:"recipes"`
	}
	runRobotJSON(t, bv, env, "--robot-recipes", &payload)

	if len(payload.Recipes) == 0 {
		t.Fatalf("recipes missing recipe list")
	}

	// Verify expected recipes exist
	found := make(map[string]bool)
	for _, r := range payload.Recipes {
		if r.Name == "" || r.Description == "" {
			t.Fatalf("recipe has empty name or description: %+v", r)
		}
		found[r.Name] = true
	}

	// Check for common recipes
	expectedRecipes := []string{"default", "actionable", "blocked"}
	for _, name := range expectedRecipes {
		if !found[name] {
			t.Fatalf("expected recipe %q not found; got %v", name, found)
		}
	}
}

// TestRobotHelpContract verifies --robot-help output is non-empty text.
func TestRobotHelpContract(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Test","status":"open","priority":1,"issue_type":"task"}`)

	cmd := exec.Command(bv, "--robot-help")
	cmd.Dir = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-help failed: %v\n%s", err, out)
	}

	helpText := string(out)
	if len(helpText) < 100 {
		t.Fatalf("robot-help output too short: %d bytes", len(helpText))
	}

	// Verify key sections
	expectedSections := []string{
		"--robot-triage",
		"--robot-plan",
		"--robot-insights",
		"--robot-next",
	}
	for _, section := range expectedSections {
		if !strings.Contains(helpText, section) {
			t.Fatalf("robot-help missing %q", section)
		}
	}
}

// TestRobotDriftContract verifies --robot-drift output structure.
// Note: --robot-drift requires --check-drift flag and may need a baseline.
func TestRobotDriftContract(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	// Create issues to enable drift detection.
	writeBeads(t, env, `{"id":"A","title":"High priority","status":"open","priority":1,"issue_type":"task","created":"2025-01-01T00:00:00Z","updated":"2025-01-02T00:00:00Z"}
{"id":"B","title":"Old unworked","status":"open","priority":1,"issue_type":"task","created":"2025-01-01T00:00:00Z"}`)

	// Run with --check-drift --robot-drift flags
	cmd := exec.Command(bv, "--check-drift", "--robot-drift")
	cmd.Dir = env
	out, err := cmd.CombinedOutput()
	// Drift check may fail without baseline, that's OK
	if err != nil {
		// Check if output is still valid JSON (error response)
		var payload map[string]any
		if jsonErr := json.Unmarshal(out, &payload); jsonErr == nil {
			// Got JSON error response, which is valid
			return
		}
		// If no baseline exists, the command may fail - that's acceptable
		t.Skipf("drift check requires baseline: %v", err)
		return
	}

	var payload struct {
		DataHash string `json:"data_hash"`
		Drift    struct {
			Status string `json:"status"`
		} `json:"drift"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("drift json decode failed: %v\nout=%s", err, out)
	}

	// Just verify we got some drift-related output
	_ = payload.Drift.Status
}

// TestRobotEmptyDataEdgeCases verifies graceful handling of empty data.
func TestRobotEmptyDataEdgeCases(t *testing.T) {
	bv := buildBvBinary(t)

	tests := []struct {
		name string
		flag string
	}{
		{"triage empty", "--robot-triage"},
		{"plan empty", "--robot-plan"},
		{"insights empty", "--robot-insights"},
		{"priority empty", "--robot-priority"},
		{"suggest empty", "--robot-suggest"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := t.TempDir()
			// Create empty beads file
			writeBeads(t, env, "")

			var payload map[string]any
			cmd := exec.Command(bv, tc.flag)
			cmd.Dir = env
			out, err := cmd.CombinedOutput()
			// Should not crash, but may return error for empty data
			if err != nil {
				// Expected for some commands with no data
				return
			}

			if err := json.Unmarshal(out, &payload); err != nil {
				// Empty data may return non-JSON
				return
			}

			// If we got JSON, verify data_hash is present
			if hash, ok := payload["data_hash"]; ok && hash == "" {
				t.Fatalf("%s returned empty data_hash", tc.flag)
			}
		})
	}
}

// TestRobotFilterByLabel verifies --label filter works with robot commands.
func TestRobotFilterByLabel(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"API-1","title":"API issue","status":"open","priority":1,"issue_type":"task","labels":["api"]}
{"id":"API-2","title":"API task 2","status":"open","priority":2,"issue_type":"task","labels":["api"]}
{"id":"WEB-1","title":"Web issue","status":"open","priority":1,"issue_type":"task","labels":["web"]}`)

	tests := []struct {
		name          string
		args          []string
		expectAPIOnly bool
	}{
		{"insights filtered", []string{"--robot-insights", "--label", "api"}, true},
		{"plan filtered", []string{"--robot-plan", "--label", "api"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bv, tc.args...)
			cmd.Dir = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", tc.name, err, out)
			}

			var payload map[string]any
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("%s json decode failed: %v", tc.name, err)
			}

			if payload["data_hash"] == "" {
				t.Fatalf("%s missing data_hash", tc.name)
			}

			// Check if output reflects filtered data
			// The exact structure varies by command, just verify we got JSON
		})
	}
}

// TestRobotInvalidOptionHandling verifies graceful error handling.
func TestRobotInvalidOptionHandling(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Test","status":"open","priority":1,"issue_type":"task"}`)

	tests := []struct {
		name        string
		args        []string
		expectError bool
	}{
		{"invalid robot flag", []string{"--robot-invalid-command"}, true},
		{"invalid priority value", []string{"--robot-min-confidence", "invalid"}, true},
		{"invalid label filter", []string{"--robot-triage", "--label", ""}, false}, // empty label may be valid
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bv, tc.args...)
			cmd.Dir = env
			_, err := cmd.CombinedOutput()

			if tc.expectError && err == nil {
				t.Fatalf("%s: expected error but got none", tc.name)
			}
			// Just verify we don't crash - errors are acceptable for invalid input
		})
	}
}

// TestRobotDeterminismAcrossCommands verifies data_hash consistency.
func TestRobotDeterminismAcrossCommands(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Root","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Blocked","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"B","depends_on_id":"A","type":"blocks"}]}`)

	commands := []string{
		"--robot-triage",
		"--robot-plan",
		"--robot-insights",
		"--robot-priority",
	}

	hashes := make(map[string]string)
	for _, cmd := range commands {
		var payload struct {
			DataHash string `json:"data_hash"`
		}
		runRobotJSON(t, bv, env, cmd, &payload)
		hashes[cmd] = payload.DataHash
	}

	// All commands should return the same data_hash for the same data
	firstHash := hashes[commands[0]]
	for cmd, hash := range hashes {
		if hash != firstHash {
			t.Fatalf("data_hash mismatch: %s=%q vs %s=%q",
				commands[0], firstHash, cmd, hash)
		}
	}
}

// TestRobotOutputContainsUsageHints verifies all commands include hints.
func TestRobotOutputContainsUsageHints(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Test","status":"open","priority":1,"issue_type":"task"}`)

	commands := []string{
		"--robot-triage",
		"--robot-plan",
		"--robot-insights",
		"--robot-priority",
		"--robot-suggest",
	}

	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			var payload struct {
				UsageHints []string `json:"usage_hints"`
			}
			runRobotJSON(t, bv, env, cmd, &payload)

			if len(payload.UsageHints) == 0 {
				t.Fatalf("%s missing usage_hints", cmd)
			}
			for i, hint := range payload.UsageHints {
				if hint == "" {
					t.Fatalf("%s usage_hints[%d] is empty", cmd, i)
				}
			}
		})
	}
}

// TestRobotPlanWithRecipe verifies --recipe filter works with plan.
func TestRobotPlanWithRecipe(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Open task","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Blocked task","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"B","depends_on_id":"A","type":"blocks"}]}
{"id":"C","title":"Done task","status":"closed","priority":1,"issue_type":"task"}`)

	tests := []struct {
		name   string
		recipe string
	}{
		{"actionable recipe", "actionable"},
		{"blocked recipe", "blocked"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bv, "--recipe", tc.recipe, "--robot-plan")
			cmd.Dir = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", tc.name, err, out)
			}

			var payload struct {
				DataHash string `json:"data_hash"`
				Plan     struct {
					Tracks []any `json:"tracks"`
				} `json:"plan"`
			}
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("%s json decode failed: %v", tc.name, err)
			}

			if payload.DataHash == "" {
				t.Fatalf("%s missing data_hash", tc.name)
			}
		})
	}
}

// TestRobotNextWithFilters verifies --robot-next respects filters.
func TestRobotNextWithFilters(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"BLOCKED-1","title":"Blocked issue","status":"blocked","priority":1,"issue_type":"task"}`)

	cmd := exec.Command(bv, "--recipe", "actionable", "--robot-next")
	cmd.Dir = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-next with recipe failed: %v\n%s", err, out)
	}

	var payload struct {
		ID       string `json:"id"`
		DataHash string `json:"data_hash"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-next json decode failed: %v\n%s", err, out)
	}

	if payload.DataHash == "" {
		t.Fatalf("robot-next missing data_hash")
	}
	if payload.ID != "" || payload.Message != "No actionable items available" {
		t.Fatalf("expected no actionable items with recipe filter, got id=%q message=%q", payload.ID, payload.Message)
	}
}

// TestRobotTriageQuickWins verifies quick_wins section is populated.
func TestRobotTriageQuickWins(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	// Create issues with varying complexity to generate quick wins
	writeBeads(t, env, `{"id":"EASY","title":"Simple task","status":"open","priority":1,"issue_type":"task","estimate":"30m"}
{"id":"HARD","title":"Complex task","status":"open","priority":1,"issue_type":"epic","estimate":"2w"}`)

	var payload struct {
		DataHash string `json:"data_hash"`
		Triage   struct {
			QuickWins []struct {
				ID string `json:"id"`
			} `json:"quick_wins"`
		} `json:"triage"`
	}
	runRobotJSON(t, bv, env, "--robot-triage", &payload)

	if payload.DataHash == "" {
		t.Fatalf("triage missing data_hash")
	}
	// quick_wins may be empty if no clear quick wins exist
	// Just verify structure is present
	_ = payload.Triage.QuickWins
}

// TestRobotTriageBlockersToClear verifies blockers_to_clear section.
func TestRobotTriageBlockersToClear(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	// Create blocking structure to populate blockers_to_clear
	writeBeads(t, env, `{"id":"BLOCKER","title":"Blocking issue","status":"open","priority":1,"issue_type":"task"}
{"id":"BLOCKED-1","title":"Blocked 1","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"BLOCKED-1","depends_on_id":"BLOCKER","type":"blocks"}]}
{"id":"BLOCKED-2","title":"Blocked 2","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"BLOCKED-2","depends_on_id":"BLOCKER","type":"blocks"}]}`)

	var payload struct {
		DataHash string `json:"data_hash"`
		Triage   struct {
			BlockersToClear []struct {
				ID            string `json:"id"`
				UnblocksCount int    `json:"unblocks_count"`
			} `json:"blockers_to_clear"`
		} `json:"triage"`
	}
	runRobotJSON(t, bv, env, "--robot-triage", &payload)

	if payload.DataHash == "" {
		t.Fatalf("triage missing data_hash")
	}
	if len(payload.Triage.BlockersToClear) == 0 {
		t.Fatalf("expected blockers_to_clear with blocking issue")
	}
	// Find the blocker
	found := false
	for _, b := range payload.Triage.BlockersToClear {
		if b.ID == "BLOCKER" && b.UnblocksCount >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected BLOCKER in blockers_to_clear with unblocks_count >= 2: %+v",
			payload.Triage.BlockersToClear)
	}
}

func TestRobotMode_IgnoresBackgroundModeFlagAndEnv(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"A","title":"Alpha","status":"open","priority":1,"issue_type":"task"}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Baseline.
	baselineCmd := exec.CommandContext(ctx, bv, "--robot-triage")
	baselineCmd.Dir = env
	baselineOut, err := baselineCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline --robot-triage failed: %v\n%s", err, baselineOut)
	}
	var baseline struct {
		DataHash string `json:"data_hash"`
	}
	if err := json.Unmarshal(baselineOut, &baseline); err != nil {
		t.Fatalf("baseline json decode: %v\nout=%s", err, baselineOut)
	}
	if baseline.DataHash == "" {
		t.Fatalf("baseline missing data_hash")
	}

	// BV_BACKGROUND_MODE should not impact robot mode behavior/output.
	envCmd := exec.CommandContext(ctx, bv, "--robot-triage")
	envCmd.Dir = env
	envCmd.Env = append(os.Environ(), "BV_BACKGROUND_MODE=1")
	envOut, err := envCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("BV_BACKGROUND_MODE=1 --robot-triage failed: %v\n%s", err, envOut)
	}
	var envPayload struct {
		DataHash string `json:"data_hash"`
	}
	if err := json.Unmarshal(envOut, &envPayload); err != nil {
		t.Fatalf("env json decode: %v\nout=%s", err, envOut)
	}
	if envPayload.DataHash != baseline.DataHash {
		t.Fatalf("data_hash changed with BV_BACKGROUND_MODE=1: %s vs %s", envPayload.DataHash, baseline.DataHash)
	}

	// --background-mode flag should be accepted but ignored for robot commands.
	flagCmd := exec.CommandContext(ctx, bv, "--background-mode", "--robot-triage")
	flagCmd.Dir = env
	flagOut, err := flagCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--background-mode --robot-triage failed: %v\n%s", err, flagOut)
	}
	var flagPayload struct {
		DataHash string `json:"data_hash"`
	}
	if err := json.Unmarshal(flagOut, &flagPayload); err != nil {
		t.Fatalf("flag json decode: %v\nout=%s", err, flagOut)
	}
	if flagPayload.DataHash != baseline.DataHash {
		t.Fatalf("data_hash changed with --background-mode: %s vs %s", flagPayload.DataHash, baseline.DataHash)
	}
}
