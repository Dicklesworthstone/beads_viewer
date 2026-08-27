package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// E2E: Multi-Step Workflow Tests (bv-qfr5)
// Tests complete user workflows spanning multiple commands with state verification
// ============================================================================

// TestWorkflow_NewProjectSetup tests the new project initialization workflow
func TestWorkflow_NewProjectSetup(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()

	// Step 1: Verify bv handles missing .beads directory gracefully
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Log("Note: bv succeeded without .beads (may auto-create or warn)")
	}
	// Should either fail gracefully or return empty triage
	if !strings.Contains(string(out), "no issues") && !strings.Contains(string(out), "beads") && err == nil {
		// If it succeeded, should be valid JSON with 0 issues
		var result map[string]interface{}
		if jsonErr := json.Unmarshal(out, &result); jsonErr == nil {
			if triage, ok := result["triage"].(map[string]interface{}); ok {
				if quickRef, ok := triage["quick_ref"].(map[string]interface{}); ok {
					if count, ok := quickRef["open_count"].(float64); ok && count != 0 {
						t.Errorf("expected 0 open issues for empty project, got %v", count)
					}
				}
			}
		}
	}

	// Step 2: Create .beads directory and first issue
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	firstIssue := `{"id": "PROJ-1", "title": "Initial Setup Task", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(firstIssue), 0644); err != nil {
		t.Fatalf("failed to write beads.jsonl: %v", err)
	}

	// Step 3: Run analysis on new project
	cmd = exec.Command(bv, "--robot-insights")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-insights failed: %v\n%s", err, out)
	}

	var insights map[string]interface{}
	if err := json.Unmarshal(out, &insights); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify basic stats
	if stats, ok := insights["Stats"].(map[string]interface{}); ok {
		if nodeCount, ok := stats["NodeCount"].(float64); !ok || nodeCount != 1 {
			t.Errorf("expected 1 node, got %v", stats["NodeCount"])
		}
	}

	// Step 4: Add more issues and verify graph grows
	moreIssues := `{"id": "PROJ-1", "title": "Initial Setup Task", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "PROJ-2", "title": "Second Task", "status": "open", "priority": 2, "issue_type": "task", "dependencies": [{"depends_on_id": "PROJ-1", "type": "blocks"}]}
{"id": "PROJ-3", "title": "Third Task", "status": "open", "priority": 2, "issue_type": "task", "dependencies": [{"depends_on_id": "PROJ-2", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(moreIssues), 0644); err != nil {
		t.Fatalf("failed to update beads.jsonl: %v", err)
	}

	cmd = exec.Command(bv, "--robot-plan")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-plan failed: %v\n%s", err, out)
	}

	var plan map[string]interface{}
	if err := json.Unmarshal(out, &plan); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify plan has tracks
	if planData, ok := plan["plan"].(map[string]interface{}); ok {
		if tracks, ok := planData["tracks"].([]interface{}); !ok || len(tracks) == 0 {
			t.Error("expected execution plan tracks")
		}
	}
}

// TestWorkflow_TriageAndRecommendations tests the triage workflow
func TestWorkflow_TriageAndRecommendations(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()

	// Create a project with mixed priorities and dependencies
	issues := `{"id": "EPIC-1", "title": "Epic: Feature X", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "TASK-1", "title": "High Priority Blocker", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "TASK-2", "title": "Medium Task", "status": "open", "priority": 2, "issue_type": "task", "dependencies": [{"depends_on_id": "TASK-1", "type": "blocks"}]}
{"id": "TASK-3", "title": "Low Priority Task", "status": "open", "priority": 3, "issue_type": "task", "dependencies": [{"depends_on_id": "TASK-2", "type": "blocks"}]}
{"id": "BUG-1", "title": "Critical Bug", "status": "open", "priority": 0, "issue_type": "bug"}`
	writeBeads(t, projectDir, issues)

	// Step 1: Get triage recommendations
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var triage map[string]interface{}
	if err := json.Unmarshal(out, &triage); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify triage structure
	triageData, ok := triage["triage"].(map[string]interface{})
	if !ok {
		t.Fatal("missing triage field")
	}

	// Check quick_ref
	quickRef, ok := triageData["quick_ref"].(map[string]interface{})
	if !ok {
		t.Fatal("missing quick_ref")
	}
	if openCount, ok := quickRef["open_count"].(float64); !ok || openCount != 5 {
		t.Errorf("expected 5 open issues, got %v", quickRef["open_count"])
	}

	// Check recommendations exist
	recommendations, ok := triageData["recommendations"].([]interface{})
	if !ok || len(recommendations) == 0 {
		t.Error("expected recommendations")
	}

	// Step 2: Get the top recommendation via --robot-next
	cmd = exec.Command(bv, "--robot-next")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-next failed: %v\n%s", err, out)
	}

	var next map[string]interface{}
	if err := json.Unmarshal(out, &next); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify next recommendation has claim command
	if _, ok := next["claim_command"]; !ok {
		t.Error("missing claim_command in robot-next output")
	}
	if _, ok := next["show_command"]; !ok {
		t.Error("missing show_command in robot-next output")
	}

	// Step 3: Verify blockers_to_clear in triage
	blockers, ok := triageData["blockers_to_clear"].([]interface{})
	if !ok {
		t.Log("Note: no blockers_to_clear in triage (may be expected)")
	} else if len(blockers) > 0 {
		// Verify blocker structure
		firstBlocker := blockers[0].(map[string]interface{})
		if _, ok := firstBlocker["id"]; !ok {
			t.Error("blocker missing id field")
		}
	}
}

// TestWorkflow_TimeTravelAnalysis tests baseline/diff workflow
func TestWorkflow_TimeTravelAnalysis(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	beadsPath := filepath.Join(beadsDir, "beads.jsonl")

	// Helper to run git commands
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = projectDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	// Initialize git repo
	git("init")

	// Step 1: Create initial state and commit
	initialState := `{"id": "A", "title": "Task A", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "B", "title": "Task B", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "C", "title": "Task C", "status": "open", "priority": 3, "issue_type": "task"}`
	if err := os.WriteFile(beadsPath, []byte(initialState), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "initial state")

	// Step 2: Save baseline
	cmd := exec.Command(bv, "--save-baseline", "Initial snapshot")
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("save-baseline failed: %v\n%s", err, out)
	}

	// Verify baseline file created
	baselinePath := filepath.Join(projectDir, ".bv", "baseline.json")
	if _, err := os.Stat(baselinePath); os.IsNotExist(err) {
		t.Fatal("baseline file not created")
	}

	// Step 3: Verify baseline info
	cmd = exec.Command(bv, "--baseline-info")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline-info failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Initial snapshot") {
		t.Error("baseline info should contain description")
	}

	// Step 4: Make changes (close one, add one, change priority) and commit
	changedState := `{"id": "A", "title": "Task A", "status": "closed", "priority": 1, "issue_type": "task"}
{"id": "B", "title": "Task B", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "C", "title": "Task C", "status": "open", "priority": 3, "issue_type": "task"}
{"id": "D", "title": "New Task D", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(beadsPath, []byte(changedState), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "changes: close A, add D, reprioritize B")

	// Step 5: Check drift
	cmd = exec.Command(bv, "--check-drift")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		// Drift check may return non-zero depending on severity configuration.
		t.Logf("Drift check returned error (may be expected): %v", err)
	}
	// May or may not fail depending on severity
	t.Logf("Drift check output: %s", string(out))

	// Step 6: Get robot-diff for detailed changes (using HEAD~1 since we have git)
	cmd = exec.Command(bv, "--robot-diff", "--diff-since", "HEAD~1")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		// robot-diff may exit non-zero if there are changes
		t.Logf("robot-diff returned error (expected if changes detected): %v", err)
	}

	var diff map[string]interface{}
	if err := json.Unmarshal(out, &diff); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}

	// Verify diff structure
	if _, ok := diff["generated_at"]; !ok {
		t.Error("missing generated_at")
	}
	if diffData, ok := diff["diff"].(map[string]interface{}); ok {
		// Check for new issues (D was added)
		if newIssues, ok := diffData["new_issues"].([]interface{}); ok {
			foundD := false
			for _, issue := range newIssues {
				if issueMap, ok := issue.(map[string]interface{}); ok {
					if issueMap["id"] == "D" {
						foundD = true
						break
					}
				}
			}
			if !foundD {
				t.Log("Note: issue D not found in new_issues")
			}
		}
		// Check for closed issues (A was closed)
		if closedIssues, ok := diffData["closed_issues"].([]interface{}); ok {
			foundA := false
			for _, issue := range closedIssues {
				if issueMap, ok := issue.(map[string]interface{}); ok {
					if issueMap["id"] == "A" {
						foundA = true
						break
					}
				}
			}
			if !foundA {
				t.Log("Note: issue A not found in closed_issues")
			}
		}
	}
}

// TestWorkflow_LabelScopedAnalysis tests label filtering workflow
func TestWorkflow_LabelScopedAnalysis(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Create multi-label project
	issues := `{"id": "API-1", "title": "API Endpoint", "status": "open", "priority": 1, "issue_type": "task", "labels": ["api", "backend"]}
{"id": "API-2", "title": "API Auth", "status": "open", "priority": 0, "issue_type": "task", "labels": ["api", "security"]}
{"id": "UI-1", "title": "Dashboard", "status": "open", "priority": 2, "issue_type": "task", "labels": ["frontend", "ui"]}
{"id": "UI-2", "title": "Settings Page", "status": "open", "priority": 2, "issue_type": "task", "labels": ["frontend", "ui"], "dependencies": [{"depends_on_id": "API-1", "type": "blocks"}]}
{"id": "DB-1", "title": "Schema Migration", "status": "open", "priority": 1, "issue_type": "task", "labels": ["backend", "database"]}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(issues), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Step 1: Get unfiltered graph stats
	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-graph failed: %v\n%s", err, out)
	}

	var fullGraph map[string]interface{}
	if err := json.Unmarshal(out, &fullGraph); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	fullNodeCount := fullGraph["nodes"].(float64)
	if fullNodeCount != 5 {
		t.Errorf("expected 5 nodes, got %v", fullNodeCount)
	}

	// Step 2: Filter by label 'api'
	cmd = exec.Command(bv, "--robot-graph", "--graph-format=json", "--label=api")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-graph with label failed: %v\n%s", err, out)
	}

	var apiGraph map[string]interface{}
	if err := json.Unmarshal(out, &apiGraph); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	apiNodeCount := apiGraph["nodes"].(float64)
	if apiNodeCount >= fullNodeCount {
		t.Errorf("filtered graph should have fewer nodes: api=%v, full=%v", apiNodeCount, fullNodeCount)
	}

	// Step 3: Verify filters_applied is set
	if filters, ok := apiGraph["filters_applied"].(map[string]interface{}); ok {
		if filters["label"] != "api" {
			t.Errorf("expected label filter 'api', got %v", filters["label"])
		}
	}

	// Step 4: Get label health metrics
	cmd = exec.Command(bv, "--robot-label-health")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-label-health failed: %v\n%s", err, out)
	}

	var labelHealth map[string]interface{}
	if err := json.Unmarshal(out, &labelHealth); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify we have label metrics
	if results, ok := labelHealth["results"].(map[string]interface{}); ok {
		if labels, ok := results["labels"].([]interface{}); !ok || len(labels) == 0 {
			t.Error("expected label health metrics")
		}
	}

	// Step 5: Get label attention ranking
	cmd = exec.Command(bv, "--robot-label-attention", "--attention-limit=3")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-label-attention failed: %v\n%s", err, out)
	}

	var labelAttention map[string]interface{}
	if err := json.Unmarshal(out, &labelAttention); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if results, ok := labelAttention["results"].(map[string]interface{}); ok {
		if rankings, ok := results["rankings"].([]interface{}); !ok {
			t.Error("expected label attention rankings")
		} else if len(rankings) > 3 {
			t.Errorf("expected at most 3 rankings with --attention-limit=3, got %d", len(rankings))
		}
	}
}

// TestWorkflow_ExportPipeline tests the export workflow
func TestWorkflow_ExportPipeline(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Create project with issues
	issues := `{"id": "PROJ-1", "title": "Feature A", "status": "open", "priority": 1, "issue_type": "feature"}
{"id": "PROJ-2", "title": "Bug Fix", "status": "in_progress", "priority": 0, "issue_type": "bug"}
{"id": "PROJ-3", "title": "Task", "status": "closed", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(issues), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Step 1: Generate insights
	cmd := exec.Command(bv, "--robot-insights")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-insights failed: %v\n%s", err, out)
	}

	var insights map[string]interface{}
	if err := json.Unmarshal(out, &insights); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Step 2: Export to markdown
	mdPath := filepath.Join(projectDir, "report.md")
	cmd = exec.Command(bv, "--export-md", mdPath)
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export-md failed: %v\n%s", err, out)
	}

	// Verify markdown file created
	if _, err := os.Stat(mdPath); os.IsNotExist(err) {
		t.Fatal("markdown file not created")
	}

	mdContent, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("failed to read markdown: %v", err)
	}

	// Verify markdown content
	if !strings.Contains(string(mdContent), "Feature A") {
		t.Error("markdown should contain issue titles")
	}

	// Step 3: Export graph to multiple formats
	formats := []string{"json", "dot", "mermaid"}
	for _, format := range formats {
		cmd = exec.Command(bv, "--robot-graph", "--graph-format="+format)
		cmd.Dir = projectDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("robot-graph --%s failed: %v\n%s", format, err, out)
		}

		var graphOut map[string]interface{}
		if err := json.Unmarshal(out, &graphOut); err != nil {
			t.Fatalf("invalid JSON for %s format: %v", format, err)
		}

		if graphOut["format"] != format {
			t.Errorf("expected format=%s, got %v", format, graphOut["format"])
		}
	}

	// Step 4: Verify all exports are consistent
	cmd = exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-triage failed: %v\n%s", err, out)
	}

	var triage map[string]interface{}
	if err := json.Unmarshal(out, &triage); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Cross-check counts: open_count is the STRICT status count (#165), so
	// only PROJ-1 (open) counts; PROJ-2 (in_progress) and PROJ-3 (closed)
	// must not. quick_ref.open_count must equal by_status.open, and the
	// legacy non-closed aggregate lives on not_closed_count.
	triageData, ok := triage["triage"].(map[string]interface{})
	if !ok {
		t.Fatalf("triage output missing 'triage' object: %v", triage)
	}
	quickRef, ok := triageData["quick_ref"].(map[string]interface{})
	if !ok {
		t.Fatalf("triage output missing 'quick_ref' object: %v", triageData)
	}
	openCount, ok := quickRef["open_count"].(float64)
	if !ok {
		t.Fatalf("quick_ref missing numeric open_count: %v", quickRef)
	}
	if openCount != 1 {
		t.Errorf("open_count: got %v, want 1 (strict status==open; PROJ-1 only)", openCount)
	}
	notClosedCount, ok := quickRef["not_closed_count"].(float64)
	if !ok {
		t.Fatalf("quick_ref missing numeric not_closed_count: %v", quickRef)
	}
	if notClosedCount != 2 { // PROJ-1 open + PROJ-2 in_progress
		t.Errorf("not_closed_count: got %v, want 2 (open + in_progress)", notClosedCount)
	}
	health, ok := triageData["project_health"].(map[string]interface{})
	if !ok {
		t.Fatalf("triage output missing 'project_health' object: %v", triageData)
	}
	counts, ok := health["counts"].(map[string]interface{})
	if !ok {
		t.Fatalf("project_health missing 'counts' object: %v", health)
	}
	byStatus, ok := counts["by_status"].(map[string]interface{})
	if !ok {
		t.Fatalf("counts missing 'by_status' object: %v", counts)
	}
	if byStatusOpen, _ := byStatus["open"].(float64); byStatusOpen != openCount {
		t.Errorf("quick_ref.open_count=%v must equal by_status.open=%v", openCount, byStatusOpen)
	}
	// Partition invariant: not_closed == actionable + not_actionable.
	actionable, _ := quickRef["actionable_count"].(float64)
	notActionable, _ := quickRef["not_actionable_count"].(float64)
	if notClosedCount != actionable+notActionable {
		t.Errorf("partition invariant violated: not_closed_count=%v, actionable_count=%v, not_actionable_count=%v",
			notClosedCount, actionable, notActionable)
	}
}

// TestWorkflow_StateTransitions tests state changes are detected correctly
func TestWorkflow_StateTransitions(t *testing.T) {
	bv := buildBvBinary(t)
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	beadsPath := filepath.Join(beadsDir, "beads.jsonl")

	// Step 1: Create issue in 'open' state
	state1 := `{"id": "TRACK-1", "title": "Tracked Issue", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(beadsPath, []byte(state1), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out1, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("triage failed: %v\n%s", err, out1)
	}

	var triage1 map[string]interface{}
	json.Unmarshal(out1, &triage1)
	triageData1 := triage1["triage"].(map[string]interface{})
	quickRef1 := triageData1["quick_ref"].(map[string]interface{})
	openCount1 := quickRef1["open_count"].(float64)

	// Step 2: Transition to 'in_progress'
	state2 := `{"id": "TRACK-1", "title": "Tracked Issue", "status": "in_progress", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(beadsPath, []byte(state2), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	cmd = exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out2, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("triage failed: %v\n%s", err, out2)
	}

	var triage2 map[string]interface{}
	json.Unmarshal(out2, &triage2)
	triageData2 := triage2["triage"].(map[string]interface{})
	quickRef2 := triageData2["quick_ref"].(map[string]interface{})
	inProgressCount2 := quickRef2["in_progress_count"].(float64)

	if inProgressCount2 != 1 {
		t.Errorf("expected 1 in_progress, got %v", inProgressCount2)
	}

	// Step 3: Transition to 'closed'
	state3 := `{"id": "TRACK-1", "title": "Tracked Issue", "status": "closed", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(beadsPath, []byte(state3), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	cmd = exec.Command(bv, "--robot-triage")
	cmd.Dir = projectDir
	out3, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("triage failed: %v\n%s", err, out3)
	}

	var triage3 map[string]interface{}
	json.Unmarshal(out3, &triage3)
	triageData3 := triage3["triage"].(map[string]interface{})
	quickRef3 := triageData3["quick_ref"].(map[string]interface{})
	openCount3 := quickRef3["open_count"].(float64)

	// Verify state changed
	if openCount3 >= openCount1 {
		t.Errorf("closing issue should decrease open count: was %v, now %v", openCount1, openCount3)
	}
}
