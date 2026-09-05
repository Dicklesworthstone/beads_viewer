package main_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBoardTUIWorkflow launches the TUI in board view mode to verify it initializes cleanly.
// Uses BV_TUI_AUTOCLOSE_MS to avoid hanging.
func TestBoardTUIWorkflow(t *testing.T) {
	skipIfNoScript(t)
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create a realistic board scenario with multiple statuses
	beads := `{"id":"open-1","title":"Open task","status":"open","priority":1,"issue_type":"task"}
{"id":"open-2","title":"Second open task","status":"open","priority":2,"issue_type":"task"}
{"id":"prog-1","title":"In progress task","status":"in_progress","priority":1,"issue_type":"feature"}
{"id":"blocked-1","title":"Blocked task","status":"blocked","priority":1,"issue_type":"bug","dependencies":[{"issue_id":"blocked-1","depends_on_id":"prog-1","type":"blocks"}]}
{"id":"closed-1","title":"Closed task","status":"closed","priority":2,"issue_type":"task"}
{"id":"closed-2","title":"Another closed","status":"closed","priority":3,"issue_type":"chore"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := scriptTUICommand(ctx, bv)
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"BV_TUI_AUTOCLOSE_MS=1500", // Auto-close after 1.5s
	)

	out, err := runCmdToFile(t, cmd)
	if ctx.Err() == context.DeadlineExceeded {
		t.Skipf("skipping Board TUI test: timed out; output:\n%s", out)
	}
	if err != nil {
		t.Fatalf("Board TUI run failed: %v\n%s", err, out)
	}
}

// TestBoardRobotTriageIncludesStatusCounts verifies robot-triage returns counts by status
// which can be used to populate a board view.
func TestBoardRobotTriageIncludesStatusCounts(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with various statuses
	beads := `{"id":"open-1","title":"Open 1","status":"open","priority":1,"issue_type":"task"}
{"id":"open-2","title":"Open 2","status":"open","priority":2,"issue_type":"task"}
{"id":"prog-1","title":"Progress 1","status":"in_progress","priority":1,"issue_type":"task"}
{"id":"closed-1","title":"Closed 1","status":"closed","priority":2,"issue_type":"task"}`

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
		t.Fatalf("Failed to parse triage output: %v\nOutput: %s", err, out)
	}

	byStatus := result.Triage.ProjectHealth.Counts.ByStatus

	// Verify status counts
	if byStatus["open"] != 2 {
		t.Errorf("Expected 2 open issues, got %d", byStatus["open"])
	}
	if byStatus["in_progress"] != 1 {
		t.Errorf("Expected 1 in_progress issue, got %d", byStatus["in_progress"])
	}
	if byStatus["closed"] != 1 {
		t.Errorf("Expected 1 closed issue, got %d", byStatus["closed"])
	}
}

// TestBoardRobotPlanReturnsGroupedTracks verifies robot-plan can provide
// data suitable for board swimlanes.
func TestBoardRobotPlanReturnsGroupedTracks(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues that form parallel tracks
	beads := `{"id":"root","title":"Root task","status":"open","priority":1,"issue_type":"epic"}
{"id":"track-a","title":"Track A task","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"track-a","depends_on_id":"root","type":"blocks"}]}
{"id":"track-b","title":"Track B task","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"track-b","depends_on_id":"root","type":"blocks"}]}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-plan")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-plan failed: %v\n%s", err, out)
	}

	// Verify it returns valid JSON with plan structure
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse plan output: %v", err)
	}

	if _, ok := result["plan"]; !ok {
		t.Error("Expected 'plan' key in robot-plan output")
	}
}

// TestBoardFiltersByType verifies issues can be filtered by type for type-based swimlanes.
func TestBoardFiltersByType(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with various types
	beads := `{"id":"bug-1","title":"Bug 1","status":"open","priority":1,"issue_type":"bug"}
{"id":"feat-1","title":"Feature 1","status":"open","priority":2,"issue_type":"feature"}
{"id":"task-1","title":"Task 1","status":"open","priority":2,"issue_type":"task"}
{"id":"epic-1","title":"Epic 1","status":"open","priority":1,"issue_type":"epic"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use robot-triage to verify types are tracked
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

	// Verify type counts for swimlane grouping
	if byType["bug"] != 1 {
		t.Errorf("Expected 1 bug, got %d", byType["bug"])
	}
	if byType["feature"] != 1 {
		t.Errorf("Expected 1 feature, got %d", byType["feature"])
	}
	if byType["task"] != 1 {
		t.Errorf("Expected 1 task, got %d", byType["task"])
	}
	if byType["epic"] != 1 {
		t.Errorf("Expected 1 epic, got %d", byType["epic"])
	}
}

// TestBoardFiltersByPriority verifies issues can be filtered by priority for priority-based swimlanes.
func TestBoardFiltersByPriority(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with various priorities
	beads := `{"id":"p0-1","title":"Critical","status":"open","priority":0,"issue_type":"task"}
{"id":"p1-1","title":"High 1","status":"open","priority":1,"issue_type":"task"}
{"id":"p1-2","title":"High 2","status":"open","priority":1,"issue_type":"task"}
{"id":"p2-1","title":"Medium","status":"open","priority":2,"issue_type":"task"}
{"id":"p3-1","title":"Low","status":"open","priority":3,"issue_type":"task"}`

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

	// Verify priority counts for swimlane grouping
	if byPriority["0"] != 1 {
		t.Errorf("Expected 1 P0 issue, got %d", byPriority["0"])
	}
	if byPriority["1"] != 2 {
		t.Errorf("Expected 2 P1 issues, got %d", byPriority["1"])
	}
	if byPriority["2"] != 1 {
		t.Errorf("Expected 1 P2 issue, got %d", byPriority["2"])
	}
	if byPriority["3"] != 1 {
		t.Errorf("Expected 1 P3 issue, got %d", byPriority["3"])
	}
}

// TestBoardWithDependencies verifies the board correctly handles blocked issues.
func TestBoardWithDependencies(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with blocking dependencies
	beads := `{"id":"blocker","title":"Blocker task","status":"in_progress","priority":1,"issue_type":"task"}
{"id":"blocked-1","title":"Blocked 1","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"issue_id":"blocked-1","depends_on_id":"blocker","type":"blocks"}]}
{"id":"blocked-2","title":"Blocked 2","status":"blocked","priority":2,"issue_type":"task","dependencies":[{"issue_id":"blocked-2","depends_on_id":"blocker","type":"blocks"}]}
{"id":"open-1","title":"Open dependency-gated work 1","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"open-1","depends_on_id":"blocker","type":"blocks"}]}
{"id":"open-2","title":"Open dependency-gated work 2","status":"open","priority":2,"issue_type":"task","dependencies":[{"issue_id":"open-2","depends_on_id":"blocker","type":"blocks"}]}`

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
				UnblocksIDs   []string `json:"unblocks_ids"`
				UnblocksCount int      `json:"unblocks_count"`
			} `json:"blockers_to_clear"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	// Verify blocked count
	if result.Triage.ProjectHealth.Counts.Blocked != 2 {
		t.Errorf("Expected 2 blocked issues, got %d", result.Triage.ProjectHealth.Counts.Blocked)
	}

	// Verify blockers_to_clear identifies the blocker
	found := false
	for _, blocker := range result.Triage.BlockersToClear {
		if blocker.ID == "blocker" {
			found = true
			if blocker.UnblocksCount != 2 {
				t.Errorf("Expected blocker to unblock 2 issues, got %d", blocker.UnblocksCount)
			}
			// Completion releases dependency-gated open work but cannot change
			// either explicitly parked issue's status.
			if len(blocker.UnblocksIDs) != 2 || blocker.UnblocksIDs[0] != "open-1" || blocker.UnblocksIDs[1] != "open-2" {
				t.Errorf("Expected only open work to unblock, got %v", blocker.UnblocksIDs)
			}
		}
	}
	if !found {
		t.Error("Expected 'blocker' in blockers_to_clear")
	}
}

// TestBoardLargeDataset verifies board handles 100+ issues without errors.
func TestBoardLargeDataset(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Generate 100 issues
	var lines []string
	statuses := []string{"open", "in_progress", "blocked", "closed"}
	types := []string{"task", "bug", "feature", "epic"}

	for i := 0; i < 100; i++ {
		line := `{"id":"issue-` + string(rune('A'+i/26)) + string(rune('A'+i%26)) + `","title":"Issue ` + string(rune('0'+i/10)) + string(rune('0'+i%10)) + `","status":"` + statuses[i%4] + `","priority":` + string(rune('0'+i%5)) + `,"issue_type":"` + types[i%4] + `"}`
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
		t.Fatalf("robot-triage with 100 issues failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					Total int `json:"total"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	if result.Triage.ProjectHealth.Counts.Total != 100 {
		t.Errorf("Expected 100 total issues, got %d", result.Triage.ProjectHealth.Counts.Total)
	}
}

// TestBoardEmptyState verifies board handles empty dataset gracefully.
func TestBoardEmptyState(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Empty beads file
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bv, "--robot-triage")
	cmd.Dir = tempDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage with empty data failed: %v\n%s", err, out)
	}

	var result struct {
		Triage struct {
			ProjectHealth struct {
				Counts struct {
					Total int `json:"total"`
				} `json:"counts"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse triage output: %v", err)
	}

	if result.Triage.ProjectHealth.Counts.Total != 0 {
		t.Errorf("Expected 0 total issues for empty data, got %d", result.Triage.ProjectHealth.Counts.Total)
	}
}

// TestBoardSearchIntegration verifies robot-search works for board filtering.
func TestBoardSearchIntegration(t *testing.T) {
	bv := buildBvBinary(t)

	tempDir := t.TempDir()
	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Use distinctive tokens that will rank well with semantic search
	beads := `{"id":"auth-1","title":"Fix authentication bug","description":"authentication authentication authentication login security","status":"open","priority":1,"issue_type":"bug"}
{"id":"auth-2","title":"Add OAuth support","description":"oauth oauth oauth authentication flow token","status":"open","priority":2,"issue_type":"feature"}
{"id":"db-1","title":"Database migration","description":"database schema migration postgres","status":"open","priority":1,"issue_type":"task"}`

	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use --search with --robot-search (semantic search via hash embedder)
	cmd := exec.CommandContext(ctx, bv, "--search", "authentication", "--robot-search")
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(),
		"BV_SEMANTIC_EMBEDDER=hash",
		"BV_SEMANTIC_DIM=2048",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-search failed: %v\n%s", err, out)
	}

	var result struct {
		Results []struct {
			IssueID string `json:"issue_id"`
		} `json:"results"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("Failed to parse search output: %v", err)
	}

	// Should find at least the auth-related issues
	if len(result.Results) == 0 {
		t.Error("Expected at least 1 search result")
	}
}
