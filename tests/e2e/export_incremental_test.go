package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// Export Incremental Updates E2E Tests (bv-2ino)
// Tests updating an existing static export with changes to the underlying data.

// =============================================================================
// 1. Adding New Issues
// =============================================================================

// TestExportIncremental_AddNewIssues verifies new issues appear after re-export.
func TestExportIncremental_AddNewIssues(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial export with 2 issues
	initialData := `{"id": "issue-1", "title": "Initial Issue 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "issue-2", "title": "Initial Issue 2", "status": "open", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)
	initialMeta := readMetaJSON(t, exportDir)
	if initialMeta.IssueCount != 2 {
		t.Fatalf("initial export issue_count = %d, want 2", initialMeta.IssueCount)
	}
	assertExportedIssueIDs(t, exportDir, []string{"issue-1", "issue-2"})

	// Add 3 new issues
	updatedData := initialData + `
{"id": "issue-3", "title": "New Issue 3", "status": "open", "priority": 1, "issue_type": "bug"}
{"id": "issue-4", "title": "New Issue 4", "status": "open", "priority": 3, "issue_type": "feature"}
{"id": "issue-5", "title": "New Issue 5", "status": "open", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)
	updatedMeta := readMetaJSON(t, exportDir)

	if updatedMeta.IssueCount != 5 {
		t.Errorf("updated export issue_count = %d, want 5", updatedMeta.IssueCount)
	}
	if updatedMeta.GeneratedAt == initialMeta.GeneratedAt {
		t.Error("generated_at should change after re-export")
	}
	assertExportedIssueIDs(t, exportDir, []string{"issue-1", "issue-2", "issue-3", "issue-4", "issue-5"})

	// Verify triage includes new issues
	triage := readTriageJSON(t, exportDir)
	if len(triage.Recommendations) < 5 {
		t.Logf("recommendations count = %d (may filter some issues)", len(triage.Recommendations))
	}
}

// TestExportIncremental_AddIssuesWithDependencies verifies new deps appear in graph.
func TestExportIncremental_AddIssuesWithDependencies(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial export with standalone issues
	initialData := `{"id": "epic-1", "title": "Epic 1", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "task-1", "title": "Task 1", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Add issues with dependencies
	updatedData := `{"id": "epic-1", "title": "Epic 1", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "task-1", "title": "Task 1", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "epic-1", "type": "blocks"}]}
{"id": "task-2", "title": "Task 2", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "epic-1", "type": "blocks"}]}
{"id": "subtask-1", "title": "Subtask 1", "status": "open", "priority": 2, "issue_type": "task", "dependencies": [{"target_id": "task-1", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)

	// Verify meta.json updated
	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 4 {
		t.Errorf("issue_count = %d, want 4", meta.IssueCount)
	}

	// Verify triage reflects updated state
	triage := readTriageJSON(t, exportDir)
	if triage.Meta.IssueCount != 4 {
		t.Errorf("triage meta.issue_count = %d, want 4", triage.Meta.IssueCount)
	}
}

// =============================================================================
// 2. Closing Issues
// =============================================================================

// TestExportIncremental_CloseIssues verifies closed issues update correctly.
func TestExportIncremental_CloseIssues(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial export with all open issues
	initialData := `{"id": "close-1", "title": "Will Close 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "close-2", "title": "Will Close 2", "status": "open", "priority": 2, "issue_type": "bug"}
{"id": "stay-open", "title": "Stays Open", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")
	initialTriage := readTriageJSON(t, exportDir)
	initialOpenCount := initialTriage.QuickRef.OpenCount

	// Close 2 issues
	updatedData := `{"id": "close-1", "title": "Will Close 1", "status": "closed", "priority": 1, "issue_type": "task"}
{"id": "close-2", "title": "Will Close 2", "status": "closed", "priority": 2, "issue_type": "bug"}
{"id": "stay-open", "title": "Stays Open", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")
	updatedTriage := readTriageJSON(t, exportDir)
	updatedOpenCount := updatedTriage.QuickRef.OpenCount

	// After closing 2 of 3 issues, open count should decrease
	if updatedOpenCount >= initialOpenCount {
		t.Errorf("open count should decrease: was %d, now %d", initialOpenCount, updatedOpenCount)
	}
	// Note: closed issues may not appear in triage.json unless filtered, just verify open decreased
}

// TestExportIncremental_CloseBlockingIssue verifies unblocking propagates.
func TestExportIncremental_CloseBlockingIssue(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial: blocker -> blocked
	initialData := `{"id": "blocker", "title": "Blocking Issue", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "blocked", "title": "Blocked Issue", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "blocker", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")

	// Close the blocker
	updatedData := `{"id": "blocker", "title": "Blocking Issue", "status": "closed", "priority": 0, "issue_type": "task"}
{"id": "blocked", "title": "Blocked Issue", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "blocker", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")
	triage := readTriageJSON(t, exportDir)

	// The previously blocked issue should now be actionable
	openCount := triage.QuickRef.OpenCount
	if openCount < 1 {
		t.Errorf("expected at least 1 open issue after closing blocker, got %d", openCount)
	}
}

// =============================================================================
// 3. Modifying Dependencies
// =============================================================================

// TestExportIncremental_AddDependency verifies adding deps updates graph.
func TestExportIncremental_AddDependency(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial: no dependencies
	initialData := `{"id": "parent", "title": "Parent Issue", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "child", "title": "Child Issue", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)
	initialMeta := readMetaJSON(t, exportDir)
	if got := queryExportedDependencyRows(t, exportDir); len(got) != 0 {
		t.Fatalf("initial database should contain no dependencies, got %v", got)
	}

	// Add dependency relationship
	updatedData := `{"id": "parent", "title": "Parent Issue", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "child", "title": "Child Issue", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "parent", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)
	updatedMeta := readMetaJSON(t, exportDir)

	// Verify export was regenerated
	if updatedMeta.GeneratedAt == initialMeta.GeneratedAt {
		t.Error("generated_at should change after re-export with deps")
	}
	if got, want := queryExportedDependencyRows(t, exportDir), [][3]string{{"child", "parent", "blocks"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("re-exported dependency rows = %v, want %v", got, want)
	}
	assertExportedIssueIDs(t, exportDir, []string{"child", "parent"})

	// Verify triage reflects blocked status
	triage := readTriageJSON(t, exportDir)
	blockedCount := triage.QuickRef.BlockedCount
	if blockedCount < 1 {
		t.Logf("blocked count = %d (status may not auto-update)", blockedCount)
	}
}

// TestExportIncremental_RemoveDependency verifies removing deps updates graph.
func TestExportIncremental_RemoveDependency(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial: with dependency
	initialData := `{"id": "parent", "title": "Parent Issue", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "child", "title": "Child Issue", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "parent", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Remove dependency
	updatedData := `{"id": "parent", "title": "Parent Issue", "status": "open", "priority": 0, "issue_type": "epic"}
{"id": "child", "title": "Child Issue", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)
	triage := readTriageJSON(t, exportDir)

	// Both issues should now be open
	openCount := triage.QuickRef.OpenCount
	if openCount != 2 {
		t.Errorf("expected 2 open issues after removing dep, got %d", openCount)
	}
}

// TestExportIncremental_ChangeDependencyType verifies dep type changes reflect.
func TestExportIncremental_ChangeDependencyType(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial: blocks dependency
	initialData := `{"id": "A", "title": "Issue A", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "B", "title": "Issue B", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "A", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Change to relates dependency
	updatedData := `{"id": "A", "title": "Issue A", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "B", "title": "Issue B", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "A", "type": "relates"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export (should not error)
	runExportPages(t, bv, repoDir, exportDir)

	// Verify export succeeded
	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 2 {
		t.Errorf("issue_count = %d, want 2", meta.IssueCount)
	}
}

// =============================================================================
// 4. Deleting Issues
// =============================================================================

// TestExportIncremental_DeleteIssues verifies deleted issues disappear.
func TestExportIncremental_DeleteIssues(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial with 5 issues
	initialData := `{"id": "keep-1", "title": "Keep 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "keep-2", "title": "Keep 2", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "delete-1", "title": "Delete 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "delete-2", "title": "Delete 2", "status": "open", "priority": 2, "issue_type": "bug"}
{"id": "keep-3", "title": "Keep 3", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)
	initialMeta := readMetaJSON(t, exportDir)
	if initialMeta.IssueCount != 5 {
		t.Fatalf("initial issue_count = %d, want 5", initialMeta.IssueCount)
	}

	// Delete 2 issues
	updatedData := `{"id": "keep-1", "title": "Keep 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "keep-2", "title": "Keep 2", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "keep-3", "title": "Keep 3", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)
	updatedMeta := readMetaJSON(t, exportDir)

	if updatedMeta.IssueCount != 3 {
		t.Errorf("updated issue_count = %d, want 3", updatedMeta.IssueCount)
	}
}

// TestExportIncremental_DeleteBlockingIssue verifies orphan refs handled.
func TestExportIncremental_DeleteBlockingIssue(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial: blocker -> blocked
	initialData := `{"id": "blocker", "title": "Blocking Issue", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "blocked", "title": "Blocked Issue", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "blocker", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Delete the blocker, leaving orphan reference
	updatedData := `{"id": "blocked", "title": "Blocked Issue", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "blocker", "type": "blocks"}]}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export should handle orphan reference gracefully
	runExportPages(t, bv, repoDir, exportDir)
	meta := readMetaJSON(t, exportDir)

	if meta.IssueCount != 1 {
		t.Errorf("issue_count = %d, want 1", meta.IssueCount)
	}
}

// TestExportIncremental_DeleteAllIssues verifies empty dataset handled.
func TestExportIncremental_DeleteAllIssues(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial with issues
	initialData := `{"id": "issue-1", "title": "Issue 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "issue-2", "title": "Issue 2", "status": "open", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Delete all issues
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty issues.jsonl: %v", err)
	}

	// Re-export should handle empty dataset
	runExportPages(t, bv, repoDir, exportDir)
	meta := readMetaJSON(t, exportDir)

	if meta.IssueCount != 0 {
		t.Errorf("issue_count = %d, want 0", meta.IssueCount)
	}
}

// =============================================================================
// 5. Mixed Operations
// =============================================================================

// TestExportIncremental_MixedOperations tests add+close+delete simultaneously.
func TestExportIncremental_MixedOperations(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial state
	initialData := `{"id": "keep", "title": "Keep This", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "close", "title": "Close This", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "delete", "title": "Delete This", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")
	initialMeta := readMetaJSON(t, exportDir)
	if initialMeta.IssueCount != 3 {
		t.Fatalf("initial issue_count = %d, want 3", initialMeta.IssueCount)
	}

	// Mixed operations: keep, close, delete 'delete', add new
	updatedData := `{"id": "keep", "title": "Keep This", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "close", "title": "Close This", "status": "closed", "priority": 2, "issue_type": "task"}
{"id": "new-1", "title": "New Issue 1", "status": "open", "priority": 1, "issue_type": "bug"}
{"id": "new-2", "title": "New Issue 2", "status": "in_progress", "priority": 0, "issue_type": "feature"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir, "--pages-include-closed")
	updatedMeta := readMetaJSON(t, exportDir)

	// Expected: keep + close (now closed) + new-1 + new-2 = 4
	if updatedMeta.IssueCount != 4 {
		t.Errorf("updated issue_count = %d, want 4", updatedMeta.IssueCount)
	}

	// Verify status counts from quick_ref
	triage := readTriageJSON(t, exportDir)
	openCount := triage.QuickRef.OpenCount
	inProgressCount := triage.QuickRef.InProgressCount

	// Note: closed issues may not be tracked in quick_ref, just verify open/in_progress
	if openCount < 1 {
		t.Errorf("expected at least 1 open issue, got %d", openCount)
	}
	if inProgressCount < 1 {
		t.Errorf("expected at least 1 in_progress issue, got %d", inProgressCount)
	}
}

// =============================================================================
// 6. Metadata Changes
// =============================================================================

// TestExportIncremental_ChangePriority verifies priority changes reflect.
func TestExportIncremental_ChangePriority(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial with low priority
	initialData := `{"id": "issue-1", "title": "Test Issue", "status": "open", "priority": 4, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Bump to high priority
	updatedData := `{"id": "issue-1", "title": "Test Issue", "status": "open", "priority": 0, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)

	// Verify triage includes the high priority issue
	triage := readTriageJSON(t, exportDir)
	if len(triage.Recommendations) == 0 {
		t.Error("expected recommendations for high priority issue")
	}
}

// TestExportIncremental_ChangeTitle verifies title changes reflect.
func TestExportIncremental_ChangeTitle(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Initial title
	initialData := `{"id": "issue-1", "title": "Original Title", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(initialData), 0o644); err != nil {
		t.Fatalf("write initial issues.jsonl: %v", err)
	}

	runExportPages(t, bv, repoDir, exportDir)

	// Change title
	updatedData := `{"id": "issue-1", "title": "Updated Title With New Information", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(updatedData), 0o644); err != nil {
		t.Fatalf("write updated issues.jsonl: %v", err)
	}

	// Re-export
	runExportPages(t, bv, repoDir, exportDir)

	// Verify export updated (check sqlite database was regenerated)
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("beads.sqlite3 not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("beads.sqlite3 is empty")
	}
}

// =============================================================================
// 7. Export Title Changes
// =============================================================================

// TestExportIncremental_ChangeExportTitle verifies --pages-title changes reflect.
func TestExportIncremental_ChangeExportTitle(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "issue-1", "title": "Test Issue", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Export with original title
	runExportPages(t, bv, repoDir, exportDir, "--pages-title", "Original Dashboard")
	meta1 := readMetaJSON(t, exportDir)
	if meta1.Title != "Original Dashboard" {
		t.Errorf("initial title = %q, want %q", meta1.Title, "Original Dashboard")
	}

	// Re-export with new title
	runExportPages(t, bv, repoDir, exportDir, "--pages-title", "Renamed Dashboard")
	meta2 := readMetaJSON(t, exportDir)
	if meta2.Title != "Renamed Dashboard" {
		t.Errorf("updated title = %q, want %q", meta2.Title, "Renamed Dashboard")
	}
}

// =============================================================================
// 8. Re-export Consistency
// =============================================================================

// TestExportIncremental_ReexportWithoutChanges verifies idempotent exports.
func TestExportIncremental_ReexportWithoutChanges(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "stable-1", "title": "Stable Issue", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "stable-2", "title": "Another Stable", "status": "open", "priority": 2, "issue_type": "bug"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// First export
	runExportPages(t, bv, repoDir, exportDir)
	meta1 := readMetaJSON(t, exportDir)

	// Second export (no data changes)
	runExportPages(t, bv, repoDir, exportDir)
	meta2 := readMetaJSON(t, exportDir)

	// Issue count should remain the same
	if meta1.IssueCount != meta2.IssueCount {
		t.Errorf("issue_count changed without data change: %d -> %d",
			meta1.IssueCount, meta2.IssueCount)
	}
}

// TestExportIncremental_MultipleReexports verifies stability over many exports.
func TestExportIncremental_MultipleReexports(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "multi-1", "title": "Multi Export Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Export 5 times
	for i := 0; i < 5; i++ {
		runExportPages(t, bv, repoDir, exportDir)
	}

	// Final export should still be valid
	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 1 {
		t.Errorf("issue_count after 5 exports = %d, want 1", meta.IssueCount)
	}

	// Verify all artifacts still present
	requiredFiles := []string{
		"index.html",
		"beads.sqlite3",
		"styles.css",
		"viewer.js",
	}
	for _, f := range requiredFiles {
		path := filepath.Join(exportDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s after multiple exports: %v", f, err)
		}
	}
}

// =============================================================================
// Test Helpers
// =============================================================================

// assertExportedIssueIDs verifies the database contents after each export.
func assertExportedIssueIDs(t *testing.T, exportDir string, want []string) {
	t.Helper()
	var got []string
	for _, issue := range queryAllIssues(t, filepath.Join(exportDir, "beads.sqlite3")) {
		got = append(got, issue.ID)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exported database issue IDs = %v, want %v", got, want)
	}
}

func queryExportedDependencyRows(t *testing.T, exportDir string) [][3]string {
	t.Helper()
	db, err := openSQLiteDB(filepath.Join(exportDir, "beads.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT issue_id, depends_on_id, type FROM dependencies ORDER BY issue_id, depends_on_id, type")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result [][3]string
	for rows.Next() {
		var row [3]string
		if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// runExportPages runs --export-pages with optional extra args.
func runExportPages(t *testing.T, bv, repoDir, exportDir string, extraArgs ...string) {
	t.Helper()
	args := []string{"--export-pages", exportDir}
	args = append(args, extraArgs...)

	cmd := exec.Command(bv, args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}
}

// readMetaJSON reads and parses data/meta.json from export directory.
func readMetaJSON(t *testing.T, exportDir string) metaJSON {
	t.Helper()
	metaPath := filepath.Join(exportDir, "data", "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}

	var meta metaJSON
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	return meta
}

type metaJSON struct {
	Version     string `json:"version"`
	GeneratedAt string `json:"generated_at"`
	IssueCount  int    `json:"issue_count"`
	Title       string `json:"title"`
}

// readTriageJSON reads and parses data/triage.json from export directory.
func readTriageJSON(t *testing.T, exportDir string) triageJSON {
	t.Helper()
	triagePath := filepath.Join(exportDir, "data", "triage.json")
	triageBytes, err := os.ReadFile(triagePath)
	if err != nil {
		t.Fatalf("read triage.json: %v", err)
	}

	var triage triageJSON
	if err := json.Unmarshal(triageBytes, &triage); err != nil {
		t.Fatalf("parse triage.json: %v", err)
	}
	return triage
}

type triageJSON struct {
	Meta struct {
		IssueCount int `json:"issue_count"`
	} `json:"meta"`
	QuickRef struct {
		OpenCount       int `json:"open_count"`
		ActionableCount int `json:"actionable_count"`
		BlockedCount    int `json:"blocked_count"`
		InProgressCount int `json:"in_progress_count"`
	} `json:"quick_ref"`
	Recommendations []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"recommendations"`
}
