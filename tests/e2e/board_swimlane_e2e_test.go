package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSwimlaneModeStatusGrouping verifies issues are correctly grouped by status.
// This tests the data foundation for Status swimlane mode.
func TestSwimlaneModeStatusGrouping(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with all status variants
	beads := `{"id":"open-1","title":"Open task 1","status":"open","priority":1,"issue_type":"task"}
{"id":"open-2","title":"Open task 2","status":"open","priority":2,"issue_type":"bug"}
{"id":"open-3","title":"Open task 3","status":"open","priority":1,"issue_type":"feature"}
{"id":"prog-1","title":"In progress 1","status":"in_progress","priority":1,"issue_type":"task"}
{"id":"prog-2","title":"In progress 2","status":"in_progress","priority":2,"issue_type":"bug"}
{"id":"blocked-1","title":"Blocked 1","status":"blocked","priority":1,"issue_type":"task","dependencies":[{"issue_id":"blocked-1","depends_on_id":"prog-1","type":"blocks"}]}
{"id":"blocked-2","title":"Blocked 2","status":"blocked","priority":2,"issue_type":"feature","dependencies":[{"issue_id":"blocked-2","depends_on_id":"prog-2","type":"blocks"}]}
{"id":"closed-1","title":"Closed 1","status":"closed","priority":1,"issue_type":"task"}
{"id":"closed-2","title":"Closed 2","status":"closed","priority":2,"issue_type":"bug"}
{"id":"closed-3","title":"Closed 3","status":"closed","priority":3,"issue_type":"chore"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					ByStatus map[string]int `json:"by_status"`
					Total    int            `json:"total"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	byStatus := result.Triage.ProjectHealth.Counts.ByStatus

	// Verify Status swimlane column counts
	tests := []struct {
		status string
		want   int
	}{
		{"open", 3},
		{"in_progress", 2},
		{"blocked", 2},
		{"closed", 3},
	}

	for _, tc := range tests {
		if got := byStatus[tc.status]; got != tc.want {
			t.Errorf("Status '%s': got %d, want %d", tc.status, got, tc.want)
		}
	}

	// Total should match sum
	if result.Triage.ProjectHealth.Counts.Total != 10 {
		t.Errorf("Total: got %d, want 10", result.Triage.ProjectHealth.Counts.Total)
	}
}

// TestSwimlaneModePriorityGrouping verifies issues are correctly grouped by priority.
// This tests the data foundation for Priority swimlane mode.
func TestSwimlaneModePriorityGrouping(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with priorities P0-P4
	beads := `{"id":"p0-1","title":"Critical 1","status":"open","priority":0,"issue_type":"bug"}
{"id":"p0-2","title":"Critical 2","status":"open","priority":0,"issue_type":"bug"}
{"id":"p1-1","title":"High 1","status":"open","priority":1,"issue_type":"task"}
{"id":"p1-2","title":"High 2","status":"in_progress","priority":1,"issue_type":"feature"}
{"id":"p1-3","title":"High 3","status":"open","priority":1,"issue_type":"task"}
{"id":"p2-1","title":"Medium 1","status":"open","priority":2,"issue_type":"task"}
{"id":"p2-2","title":"Medium 2","status":"open","priority":2,"issue_type":"task"}
{"id":"p3-1","title":"Low 1","status":"open","priority":3,"issue_type":"chore"}
{"id":"p4-1","title":"Backlog 1","status":"open","priority":4,"issue_type":"task"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					ByPriority map[string]int `json:"by_priority"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	byPriority := result.Triage.ProjectHealth.Counts.ByPriority

	// Verify Priority swimlane column counts
	tests := []struct {
		priority string
		want     int
	}{
		{"0", 2}, // P0 Critical
		{"1", 3}, // P1 High
		{"2", 2}, // P2 Medium
		{"3", 1}, // P3 Low
		{"4", 1}, // P4 Backlog
	}

	for _, tc := range tests {
		if got := byPriority[tc.priority]; got != tc.want {
			t.Errorf("Priority P%s: got %d, want %d", tc.priority, got, tc.want)
		}
	}
}

// TestSwimlaneModeTypeGrouping verifies issues are correctly grouped by type.
// This tests the data foundation for Type swimlane mode.
func TestSwimlaneModeTypeGrouping(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with various types
	beads := `{"id":"bug-1","title":"Bug 1","status":"open","priority":1,"issue_type":"bug"}
{"id":"bug-2","title":"Bug 2","status":"open","priority":2,"issue_type":"bug"}
{"id":"bug-3","title":"Bug 3","status":"closed","priority":1,"issue_type":"bug"}
{"id":"feat-1","title":"Feature 1","status":"open","priority":1,"issue_type":"feature"}
{"id":"feat-2","title":"Feature 2","status":"in_progress","priority":2,"issue_type":"feature"}
{"id":"task-1","title":"Task 1","status":"open","priority":2,"issue_type":"task"}
{"id":"task-2","title":"Task 2","status":"open","priority":3,"issue_type":"task"}
{"id":"epic-1","title":"Epic 1","status":"open","priority":1,"issue_type":"epic"}
{"id":"chore-1","title":"Chore 1","status":"open","priority":3,"issue_type":"chore"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					ByType map[string]int `json:"by_type"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	byType := result.Triage.ProjectHealth.Counts.ByType

	// Verify Type swimlane column counts
	tests := []struct {
		issueType string
		want      int
	}{
		{"bug", 3},
		{"feature", 2},
		{"task", 2},
		{"epic", 1},
		{"chore", 1},
	}

	for _, tc := range tests {
		if got := byType[tc.issueType]; got != tc.want {
			t.Errorf("Type '%s': got %d, want %d", tc.issueType, got, tc.want)
		}
	}
}

// TestSwimlaneMixedDataForAllModes creates data suitable for testing all three swimlane modes.
// Each mode should correctly categorize the same set of issues.
func TestSwimlaneMixedDataForAllModes(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create a diverse dataset that has meaningful distributions across all three modes
	// This is what the TUI swimlane cycling would use
	var lines []string
	statuses := []string{"open", "in_progress", "blocked", "closed"}
	types := []string{"bug", "feature", "task", "epic"}
	priorities := []int{0, 1, 1, 2, 2, 2, 3, 3, 4, 4}

	for i := 0; i < 20; i++ {
		deps := ""
		if statuses[i%4] == "blocked" && i > 0 {
			// Add blocking dependency
			deps = fmt.Sprintf(`,"dependencies":[{"issue_id":"issue-%02d","depends_on_id":"issue-%02d","type":"blocks"}]`, i, i-1)
		}
		line := fmt.Sprintf(`{"id":"issue-%02d","title":"Issue %d","status":"%s","priority":%d,"issue_type":"%s"%s}`,
			i, i, statuses[i%4], priorities[i%10], types[i%4], deps)
		lines = append(lines, line)
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					Total      int            `json:"total"`
					ByStatus   map[string]int `json:"by_status"`
					ByPriority map[string]int `json:"by_priority"`
					ByType     map[string]int `json:"by_type"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	counts := result.Triage.ProjectHealth.Counts

	// Verify total
	if counts.Total != 20 {
		t.Errorf("Total: got %d, want 20", counts.Total)
	}

	// Status mode: 5 each (20 issues, 4 statuses)
	for _, status := range statuses {
		if got := counts.ByStatus[status]; got != 5 {
			t.Errorf("Status '%s': got %d, want 5", status, got)
		}
	}

	// Type mode: 5 each (20 issues, 4 types)
	for _, issueType := range types {
		if got := counts.ByType[issueType]; got != 5 {
			t.Errorf("Type '%s': got %d, want 5", issueType, got)
		}
	}

	// Priority mode: verify all priorities have issues
	for p := 0; p <= 4; p++ {
		key := fmt.Sprintf("%d", p)
		if _, ok := counts.ByPriority[key]; !ok {
			t.Errorf("Priority P%d: no issues found", p)
		}
	}
}

// TestSwimlaneEmptyCategoriesHandling verifies behavior when some categories are empty.
// The board should handle missing categories gracefully.
func TestSwimlaneEmptyCategoriesHandling(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Only open and closed issues - no in_progress or blocked
	beads := `{"id":"open-1","title":"Open task","status":"open","priority":2,"issue_type":"task"}
{"id":"closed-1","title":"Closed task","status":"closed","priority":2,"issue_type":"task"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					ByStatus map[string]int `json:"by_status"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	byStatus := result.Triage.ProjectHealth.Counts.ByStatus

	// Verify present categories
	if byStatus["open"] != 1 {
		t.Errorf("Status 'open': got %d, want 1", byStatus["open"])
	}
	if byStatus["closed"] != 1 {
		t.Errorf("Status 'closed': got %d, want 1", byStatus["closed"])
	}

	// Empty categories should not appear (or be 0)
	if byStatus["in_progress"] != 0 {
		t.Errorf("Status 'in_progress': expected 0, got %d", byStatus["in_progress"])
	}
	if byStatus["blocked"] != 0 {
		t.Errorf("Status 'blocked': expected 0, got %d", byStatus["blocked"])
	}
}

// TestSwimlaneTUIStartsWithMixedData verifies the TUI launches in board mode with mixed data.
// Uses BV_TUI_AUTOCLOSE_MS to prevent hanging.
func TestSwimlaneTUIStartsWithMixedData(t *testing.T) {
	skipIfNoScript(t)
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create diverse test data for all swimlane modes
	beads := `{"id":"p0-bug","title":"Critical bug","status":"open","priority":0,"issue_type":"bug"}
{"id":"p1-feat","title":"High feature","status":"in_progress","priority":1,"issue_type":"feature"}
{"id":"p2-task","title":"Medium task","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"issue_id":"p2-task","depends_on_id":"p1-feat","type":"blocks"}]}
{"id":"p3-chore","title":"Low chore","status":"closed","priority":3,"issue_type":"chore"}
{"id":"p1-epic","title":"High epic","status":"open","priority":1,"issue_type":"epic"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Launch TUI with auto-close
	cmd := scriptTUICommand(ctx, bv)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"BV_TUI_AUTOCLOSE_MS=1500",
	)

	ensureCmdStdinCloses(t, ctx, cmd, 3*time.Second)
	out, err := runCmdToFile(t, cmd)
	if ctx.Err() == context.DeadlineExceeded {
		t.Skipf("TUI test timed out; output:\n%s", out)
	}
	if err != nil {
		t.Fatalf("TUI with swimlane data failed: %v\n%s", err, out)
	}
}

// TestSwimlaneDependencyVisualIndicators verifies blocked/blocking counts are tracked.
// This supports the visual dependency indicators (red/yellow/green borders) in board view.
func TestSwimlaneDependencyVisualIndicators(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create a dependency chain: root blocks two items, one of which blocks another
	// root -> blocked-1, blocked-2
	// blocked-1 -> blocked-3
	beads := `{"id":"root","title":"Root blocker","status":"in_progress","priority":1,"issue_type":"task"}
{"id":"blocked-1","title":"Blocked by root","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"issue_id":"blocked-1","depends_on_id":"root","type":"blocks"}]}
{"id":"blocked-2","title":"Also blocked by root","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"issue_id":"blocked-2","depends_on_id":"root","type":"blocks"}]}
{"id":"blocked-3","title":"Blocked by blocked-1","status":"blocked","priority":3,"issue_type":"task","dependencies":[{"issue_id":"blocked-3","depends_on_id":"blocked-1","type":"blocks"}]}
{"id":"open-1","title":"Open work gated by root","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"open-1","depends_on_id":"root","type":"blocks"}]}
{"id":"open-2","title":"More open work gated by root","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"open-2","depends_on_id":"root","type":"blocks"}]}
{"id":"open-3","title":"Open work gated by open-1","status":"open","priority":3,"issue_type":"task","dependencies":[{"issue_id":"open-3","depends_on_id":"open-1","type":"blocks"}]}
{"id":"ready","title":"Ready to work","status":"open","priority":2,"issue_type":"task"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					Blocked int `json:"blocked"`
				} `json:"counts"`
			} `json:"project_health"`
			BlockersToClear []struct {
				ID            string   `json:"id"`
				UnblocksCount int      `json:"unblocks_count"`
				UnblocksIDs   []string `json:"unblocks_ids"`
			} `json:"blockers_to_clear"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	// Verify blocked count for RED border cards
	if result.Triage.ProjectHealth.Counts.Blocked != 3 {
		t.Errorf("Blocked count: got %d, want 3", result.Triage.ProjectHealth.Counts.Blocked)
	}

	// Verify blockers for YELLOW border cards
	found := false
	for _, blocker := range result.Triage.BlockersToClear {
		if blocker.ID == "root" {
			found = true
			// Only the two open direct dependents resume; parked status and
			// the still-blocked grandchild are not immediate unblocks.
			if blocker.UnblocksCount != 2 {
				t.Errorf("Root unblocks count: got %d, want 2", blocker.UnblocksCount)
			}
			if len(blocker.UnblocksIDs) != 2 || blocker.UnblocksIDs[0] != "open-1" || blocker.UnblocksIDs[1] != "open-2" {
				t.Errorf("Root unblocks IDs: got %v, want [open-1 open-2]", blocker.UnblocksIDs)
			}
		}
	}
	if !found {
		t.Error("Expected 'root' in blockers_to_clear (should have yellow border)")
	}
}

// TestSwimlaneSingleIssuePerCategory verifies board handles minimal data.
func TestSwimlaneSingleIssuePerCategory(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// One issue per status - minimal valid state
	beads := `{"id":"open-1","title":"Open","status":"open","priority":1,"issue_type":"task"}
{"id":"prog-1","title":"In Progress","status":"in_progress","priority":2,"issue_type":"feature"}
{"id":"blocked-1","title":"Blocked","status":"blocked","priority":3,"issue_type":"bug","dependencies":[{"issue_id":"blocked-1","depends_on_id":"prog-1","type":"blocks"}]}
{"id":"closed-1","title":"Closed","status":"closed","priority":4,"issue_type":"epic"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					Total      int            `json:"total"`
					ByStatus   map[string]int `json:"by_status"`
					ByType     map[string]int `json:"by_type"`
					ByPriority map[string]int `json:"by_priority"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	counts := result.Triage.ProjectHealth.Counts

	// All should be 1
	for status, count := range counts.ByStatus {
		if count != 1 {
			t.Errorf("Status '%s': got %d, want 1", status, count)
		}
	}
	for issueType, count := range counts.ByType {
		if count != 1 {
			t.Errorf("Type '%s': got %d, want 1", issueType, count)
		}
	}
}
