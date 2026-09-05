package loader

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestOpenIssuesFileRetriesChangedSnapshot(t *testing.T) {
	for _, change := range []string{"append", "replace"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.jsonl")
			if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var opened []*os.File
			openFile := func(path string) (*os.File, error) {
				// Perform the real filesystem mutation after production's Stat
				// and before its Open, making the narrow race deterministic.
				if len(opened) == 0 {
					if change == "append" {
						writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
						if err != nil {
							return nil, err
						}
						_, writeErr := writer.WriteString("new\n")
						closeErr := writer.Close()
						if writeErr != nil {
							return nil, writeErr
						}
						if closeErr != nil {
							return nil, closeErr
						}
					} else {
						replacement := path + ".next"
						if err := os.WriteFile(replacement, []byte("new\n"), 0o644); err != nil {
							return nil, err
						}
						if err := os.Rename(replacement, path); err != nil {
							return nil, err
						}
					}
				}
				file, err := os.Open(path)
				if err == nil {
					opened = append(opened, file)
				}
				return file, err
			}
			file, err := openIssuesFile(path, openFile)
			if err != nil {
				t.Fatalf("failed to reopen after %s: %v", change, err)
			}
			defer file.Close()
			if len(opened) != 2 {
				t.Fatalf("open attempts = %d, want one rejected snapshot and one fresh snapshot", len(opened))
			}
			if _, err := opened[0].Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("rejected handle was not closed: %v", err)
			}
			got, err := io.ReadAll(file)
			want := "new\n"
			if change == "append" {
				want = "old\nnew\n"
			}
			if err != nil || string(got) != want {
				t.Fatalf("read latest snapshot = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestOpenIssuesFileDoesNotHidePersistentFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.jsonl")
	other := path + ".other"
	for _, name := range []string{path, other} {
		if err := os.WriteFile(name, []byte("data\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var rejected []*os.File
	file, err := openIssuesFile(path, func(string) (*os.File, error) {
		handle, err := os.Open(other)
		if err == nil {
			rejected = append(rejected, handle)
		}
		return handle, err
	})
	if file != nil || err == nil || !strings.Contains(err.Error(), "changed while being opened") || len(rejected) != 3 {
		t.Fatalf("persistent replacement = file %v error %v attempts %d; want bounded refusal", file, err, len(rejected))
	}
	for _, handle := range rejected {
		if _, err := handle.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("persistent mismatch leaked a handle: %v", err)
		}
	}
	permissionCalls := 0
	file, err = openIssuesFile(path, func(path string) (*os.File, error) {
		permissionCalls++
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	})
	if file != nil || !errors.Is(err, os.ErrPermission) || permissionCalls != 1 {
		t.Fatalf("permission failure was retried or hidden: file %v error %v attempts %d", file, err, permissionCalls)
	}
}

// TestPooledIssueSliceIsolation verifies that after parsing with pooled issues,
// returning pool refs to the pool does NOT affect the returned issues.
// This tests the deep-copy fix for bv-fn4b.
func TestPooledIssueSliceIsolation(t *testing.T) {
	// Create JSONL with issues that have dependencies, comments, and labels
	jsonl := `{"id":"A","title":"Issue A","status":"open","issue_type":"task","labels":["bug","urgent"],"dependencies":[{"depends_on":"B","type":"blocks"}]}
{"id":"B","title":"Issue B","status":"open","issue_type":"task","labels":["feature"],"comments":[{"author":"user1","text":"First comment"}]}
{"id":"C","title":"Issue C","status":"open","issue_type":"task","labels":["docs","api"],"dependencies":[{"depends_on":"A","type":"related"}]}`

	// Parse with pooling enabled
	pooled, err := ParseIssuesWithOptionsPooled(strings.NewReader(jsonl), ParseOptions{})
	if err != nil {
		t.Fatalf("ParseIssuesWithOptionsPooled failed: %v", err)
	}

	if len(pooled.Issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(pooled.Issues))
	}

	// Capture original values before returning pool refs
	issueALabels := make([]string, len(pooled.Issues[0].Labels))
	copy(issueALabels, pooled.Issues[0].Labels)
	issueADeps := make([]*model.Dependency, len(pooled.Issues[0].Dependencies))
	copy(issueADeps, pooled.Issues[0].Dependencies)

	issueBComments := make([]*model.Comment, len(pooled.Issues[1].Comments))
	copy(issueBComments, pooled.Issues[1].Comments)

	// Log pointer addresses for debugging
	t.Logf("Before pool return:")
	t.Logf("  Issue A Labels slice: %p (len=%d)", pooled.Issues[0].Labels, len(pooled.Issues[0].Labels))
	t.Logf("  Issue A Deps slice: %p (len=%d)", pooled.Issues[0].Dependencies, len(pooled.Issues[0].Dependencies))
	t.Logf("  Issue B Comments slice: %p (len=%d)", pooled.Issues[1].Comments, len(pooled.Issues[1].Comments))
	if len(pooled.PoolRefs) > 0 {
		t.Logf("  PoolRef[0] Labels slice: %p", pooled.PoolRefs[0].Labels)
		t.Logf("  PoolRef[0] Deps slice: %p", pooled.PoolRefs[0].Dependencies)
	}

	// Return pool refs - this should NOT affect the issues in pooled.Issues
	ReturnIssuePtrsToPool(pooled.PoolRefs)

	t.Logf("After pool return:")
	t.Logf("  Issue A Labels slice: %p (len=%d)", pooled.Issues[0].Labels, len(pooled.Issues[0].Labels))
	t.Logf("  Issue A Deps slice: %p (len=%d)", pooled.Issues[0].Dependencies, len(pooled.Issues[0].Dependencies))

	// Verify issues are unchanged after pool return
	if len(pooled.Issues[0].Labels) != len(issueALabels) {
		t.Errorf("Issue A labels changed after pool return: expected %d, got %d",
			len(issueALabels), len(pooled.Issues[0].Labels))
	}
	for i, label := range pooled.Issues[0].Labels {
		if label != issueALabels[i] {
			t.Errorf("Issue A label[%d] changed: expected %q, got %q", i, issueALabels[i], label)
		}
	}

	if len(pooled.Issues[0].Dependencies) != len(issueADeps) {
		t.Errorf("Issue A deps changed after pool return: expected %d, got %d",
			len(issueADeps), len(pooled.Issues[0].Dependencies))
	}
	for i, dep := range pooled.Issues[0].Dependencies {
		if dep.DependsOnID != issueADeps[i].DependsOnID {
			t.Errorf("Issue A dep[%d].DependsOnID changed: expected %q, got %q",
				i, issueADeps[i].DependsOnID, dep.DependsOnID)
		}
	}

	if len(pooled.Issues[1].Comments) != len(issueBComments) {
		t.Errorf("Issue B comments changed after pool return: expected %d, got %d",
			len(issueBComments), len(pooled.Issues[1].Comments))
	}
}

func TestPooledIssueEmptySlicesDropPooledCapacity(t *testing.T) {
	jsonl := `{"id":"A","title":"Issue A","status":"open","issue_type":"task"}`

	pooled, err := ParseIssuesWithOptionsPooled(strings.NewReader(jsonl), ParseOptions{})
	if err != nil {
		t.Fatalf("ParseIssuesWithOptionsPooled failed: %v", err)
	}
	defer ReturnIssuePtrsToPool(pooled.PoolRefs)

	if len(pooled.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(pooled.Issues))
	}

	issue := pooled.Issues[0]
	if issue.Labels == nil || issue.Dependencies == nil || issue.Comments == nil {
		t.Fatalf("expected empty slices to remain non-nil after deep copy")
	}
	if cap(issue.Labels) != 0 {
		t.Fatalf("expected detached Labels capacity 0, got %d", cap(issue.Labels))
	}
	if cap(issue.Dependencies) != 0 {
		t.Fatalf("expected detached Dependencies capacity 0, got %d", cap(issue.Dependencies))
	}
	if cap(issue.Comments) != 0 {
		t.Fatalf("expected detached Comments capacity 0, got %d", cap(issue.Comments))
	}
}

func TestIssueStringInterner_DeduplicatesAndStaysBounded(t *testing.T) {
	var interner issueStringInterner
	first := strings.Clone("shared-label")
	second := strings.Clone("shared-label")

	if got := interner.intern(first); got != first {
		t.Fatalf("first intern=%q, want %q", got, first)
	}
	if got := interner.intern(second); got != first {
		t.Fatalf("duplicate intern=%q, want canonical %q", got, first)
	} else if unsafe.StringData(got) != unsafe.StringData(first) {
		t.Fatal("duplicate value did not reuse the canonical string backing storage")
	}

	occupied := 0
	for _, value := range interner.slots {
		if value != "" {
			occupied++
		}
	}
	if occupied != 1 {
		t.Fatalf("occupied slots=%d, want 1", occupied)
	}

	for i := 0; i < issueStringInternerSlots*2; i++ {
		value := fmt.Sprintf("unique-%d", i)
		if got := interner.intern(value); got != value {
			t.Fatalf("intern(%q)=%q", value, got)
		}
	}

	occupied = 0
	for _, value := range interner.slots {
		if value != "" {
			occupied++
		}
	}
	if occupied <= 0 || occupied > issueStringInternerSlots {
		t.Fatalf("occupied slots=%d, want within 1..%d", occupied, issueStringInternerSlots)
	}
	if issueStringInternerMaxProbes >= issueStringInternerSlots {
		t.Fatalf("probe budget=%d must remain below table capacity %d", issueStringInternerMaxProbes, issueStringInternerSlots)
	}
}

// TestPooledIssueRaceDetector runs concurrent operations on pooled issues and
// the returned issues slice to verify there are no data races.
// This test MUST pass with -race.
func TestPooledIssueRaceDetector(t *testing.T) {
	// Create JSONL with issues that have dependencies, comments, and labels
	jsonl := `{"id":"A","title":"Issue A","status":"open","issue_type":"task","labels":["bug","urgent","p0"],"dependencies":[{"depends_on":"B","type":"blocks"},{"depends_on":"C","type":"related"}]}
{"id":"B","title":"Issue B","status":"open","issue_type":"task","labels":["feature","backend"],"comments":[{"author":"user1","text":"First"},{"author":"user2","text":"Second"}]}
{"id":"C","title":"Issue C","status":"open","issue_type":"task","labels":["docs"]}`

	// Parse with pooling enabled
	pooled, err := ParseIssuesWithOptionsPooled(strings.NewReader(jsonl), ParseOptions{})
	if err != nil {
		t.Fatalf("ParseIssuesWithOptionsPooled failed: %v", err)
	}

	if len(pooled.Issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(pooled.Issues))
	}

	// Log initial state
	t.Logf("Parsed %d issues with %d pool refs", len(pooled.Issues), len(pooled.PoolRefs))
	for i, issue := range pooled.Issues {
		t.Logf("  Issue[%d] %s: labels=%p deps=%p comments=%p",
			i, issue.ID,
			issue.Labels, issue.Dependencies, issue.Comments)
	}

	// Create a copy of issues for the reader goroutine
	issues := pooled.Issues

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: Return pool refs (simulates pool cleanup after snapshot swap)
	go func() {
		defer wg.Done()
		ReturnIssuePtrsToPool(pooled.PoolRefs)
		t.Log("Pool refs returned")
	}()

	// Goroutine 2: Concurrently read all fields from the issues
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			for _, issue := range issues {
				// Read all slice fields
				for _, label := range issue.Labels {
					_ = label
				}
				for _, dep := range issue.Dependencies {
					if dep != nil {
						_ = dep.DependsOnID
						_ = dep.Type
					}
				}
				for _, comment := range issue.Comments {
					if comment != nil {
						_ = comment.Author
						_ = comment.Text
					}
				}
				// Read other fields
				_ = issue.ID
				_ = issue.Title
				_ = issue.Status
			}
		}
		t.Log("Reader completed 100 iterations")
	}()

	wg.Wait()
	t.Log("Test completed without race")

	// Verify issues still have their data
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue[%d] ID is empty after concurrent access", i)
		}
	}
}

// TestDeepCopyIssueSlices verifies the deep copy helper works correctly.
func TestDeepCopyIssueSlices(t *testing.T) {
	issue := &model.Issue{
		ID:     "test-1",
		Title:  "Test Issue",
		Labels: []string{"bug", "urgent"},
		Dependencies: []*model.Dependency{
			{DependsOnID: "dep-1", Type: model.DepBlocks},
			{DependsOnID: "dep-2", Type: model.DepRelated},
		},
		Comments: []*model.Comment{
			{Author: "user1", Text: "Comment 1"},
		},
	}

	// Capture original backing array pointers
	origLabels := issue.Labels
	origDeps := issue.Dependencies
	origComments := issue.Comments

	t.Logf("Before deep copy:")
	t.Logf("  Labels: %p (cap=%d)", origLabels, cap(origLabels))
	t.Logf("  Deps: %p (cap=%d)", origDeps, cap(origDeps))
	t.Logf("  Comments: %p (cap=%d)", origComments, cap(origComments))

	// Perform deep copy
	DeepCopyIssueSlices(issue)

	t.Logf("After deep copy:")
	t.Logf("  Labels: %p (cap=%d)", issue.Labels, cap(issue.Labels))
	t.Logf("  Deps: %p (cap=%d)", issue.Dependencies, cap(issue.Dependencies))
	t.Logf("  Comments: %p (cap=%d)", issue.Comments, cap(issue.Comments))

	// Verify slices are different backing arrays
	if fmt.Sprintf("%p", issue.Labels) == fmt.Sprintf("%p", origLabels) {
		t.Error("Labels slice should have a new backing array")
	}
	if fmt.Sprintf("%p", issue.Dependencies) == fmt.Sprintf("%p", origDeps) {
		t.Error("Dependencies slice should have a new backing array")
	}
	if fmt.Sprintf("%p", issue.Comments) == fmt.Sprintf("%p", origComments) {
		t.Error("Comments slice should have a new backing array")
	}

	// Verify content is preserved
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "urgent" {
		t.Errorf("Labels content changed: %v", issue.Labels)
	}
	if len(issue.Dependencies) != 2 || issue.Dependencies[0].DependsOnID != "dep-1" {
		t.Errorf("Dependencies content changed: %v", issue.Dependencies)
	}
	if len(issue.Comments) != 1 || issue.Comments[0].Author != "user1" {
		t.Errorf("Comments content changed: %v", issue.Comments)
	}

	// Verify modifying original slices doesn't affect the copied issue
	origLabels[0] = "modified"
	if issue.Labels[0] == "modified" {
		t.Error("Modifying original slice affected the copied issue - deep copy failed")
	}
}

// TestDeepCopyIssueSlices_EmptySlices verifies deep copy handles empty slices.
func TestDeepCopyIssueSlices_EmptySlices(t *testing.T) {
	issue := &model.Issue{
		ID:           "test-1",
		Labels:       []string{},
		Dependencies: []*model.Dependency{},
		Comments:     []*model.Comment{},
	}

	// Should not panic
	DeepCopyIssueSlices(issue)

	if issue.Labels == nil || issue.Dependencies == nil || issue.Comments == nil {
		t.Error("Empty slices should remain non-nil after deep copy")
	}
}

// TestDeepCopyIssueSlices_NilSlices verifies deep copy handles nil slices.
func TestDeepCopyIssueSlices_NilSlices(t *testing.T) {
	issue := &model.Issue{
		ID:           "test-1",
		Labels:       nil,
		Dependencies: nil,
		Comments:     nil,
	}

	// Should not panic
	DeepCopyIssueSlices(issue)

	// Nil slices should remain nil (no allocation for empty data)
	if issue.Labels != nil || issue.Dependencies != nil || issue.Comments != nil {
		t.Error("Nil slices should remain nil after deep copy")
	}
}

// TestDeepCopyIssueSlices_NilIssue verifies deep copy handles nil issue.
func TestDeepCopyIssueSlices_NilIssue(t *testing.T) {
	// Should not panic
	DeepCopyIssueSlices(nil)
}
