package analysis

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestDefaultCycleWarningConfig(t *testing.T) {
	config := DefaultCycleWarningConfig()

	if config.MaxCycles != 10 {
		t.Errorf("expected MaxCycles=10, got %d", config.MaxCycles)
	}
	if !config.IncludeSelfLoops {
		t.Error("expected IncludeSelfLoops=true")
	}
}

func TestDetectCycleWarnings_Empty(t *testing.T) {
	config := DefaultCycleWarningConfig()

	// Nil issues
	suggestions := DetectCycleWarnings(nil, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for nil issues, got %d", len(suggestions))
	}

	// Single issue
	issues := []model.Issue{{ID: "i1", Title: "Test"}}
	suggestions = DetectCycleWarnings(issues, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for single issue, got %d", len(suggestions))
	}
}

func TestDetectCycleWarnings_NoCycle(t *testing.T) {
	config := DefaultCycleWarningConfig()
	issues := testutil.QuickChain(5)

	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for acyclic graph, got %d", len(suggestions))
	}
}

func TestDetectCycleWarnings_SingleIssueSelfLoop(t *testing.T) {
	issues := []model.Issue{{
		ID:     "self",
		Title:  "Self loop",
		Status: model.StatusOpen,
		Dependencies: []*model.Dependency{{
			DependsOnID: "self",
			Type:        model.DepBlocks,
		}},
	}}

	config := DefaultCycleWarningConfig()
	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) != 1 || !strings.Contains(suggestions[0].Summary, "Self-loop") {
		t.Fatalf("self-loop suggestions=%v, want one self-loop warning", suggestions)
	}

	config.IncludeSelfLoops = false
	if suggestions := DetectCycleWarnings(issues, config); len(suggestions) != 0 {
		t.Fatalf("excluded self-loop produced %d suggestions", len(suggestions))
	}
}

func TestDetectCycleWarnings_SkippedSelfLoopsDoNotConsumeLimit(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "B", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "C", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "D", Type: model.DepBlocks}}},
		{ID: "D", Status: model.StatusOpen, Dependencies: []*model.Dependency{{DependsOnID: "C", Type: model.DepBlocks}}},
	}
	config := DefaultCycleWarningConfig()
	config.MaxCycles = 1
	config.IncludeSelfLoops = false

	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) != 1 || !strings.Contains(suggestions[0].Summary, "Direct cycle") {
		t.Fatalf("suggestions=%v, want the non-self direct cycle", suggestions)
	}
}

func TestDetectCycleWarnings_SimpleCycle(t *testing.T) {
	config := DefaultCycleWarningConfig()
	issues := testutil.QuickCycle(3)

	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) == 0 {
		t.Error("expected at least one cycle warning")
		return
	}

	// Verify suggestion type
	for _, sug := range suggestions {
		if sug.Type != SuggestionCycleWarning {
			t.Errorf("expected type CycleWarning, got %s", sug.Type)
		}
		if sug.ActionCommand == "" {
			t.Error("expected action command to be set")
		}
		if !strings.Contains(sug.ActionCommand, "br dep remove") {
			t.Errorf("expected action to contain 'br dep remove', got %s", sug.ActionCommand)
		}
	}
}

func TestDetectCycleWarnings_DirectCycle(t *testing.T) {
	// Create a direct cycle between two issues
	issues := []model.Issue{
		{
			ID:     "i1",
			Title:  "Issue 1",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "i2"},
			},
		},
		{
			ID:     "i2",
			Title:  "Issue 2",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "i1"},
			},
		},
	}

	config := DefaultCycleWarningConfig()
	suggestions := DetectCycleWarnings(issues, config)

	if len(suggestions) == 0 {
		t.Error("expected cycle warning for direct cycle")
		return
	}

	// Should mention both issues
	found := false
	for _, sug := range suggestions {
		if strings.Contains(sug.Summary, "Direct cycle") ||
			strings.Contains(sug.Summary, "cycle") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected summary to mention cycle")
	}
}

func TestDetectCycleWarnings_MaxCycles(t *testing.T) {
	config := DefaultCycleWarningConfig()
	config.MaxCycles = 2

	// Create multiple cycles (multiple disconnected cycles)
	issues := []model.Issue{
		// Cycle 1: i1 -> i2 -> i1
		{ID: "i1", Title: "Issue 1", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i2"}}},
		{ID: "i2", Title: "Issue 2", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i1"}}},
		// Cycle 2: i3 -> i4 -> i3
		{ID: "i3", Title: "Issue 3", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i4"}}},
		{ID: "i4", Title: "Issue 4", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i3"}}},
		// Cycle 3: i5 -> i6 -> i5
		{ID: "i5", Title: "Issue 5", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i6"}}},
		{ID: "i6", Title: "Issue 6", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i5"}}},
	}

	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) > config.MaxCycles {
		t.Errorf("expected at most %d cycle warnings, got %d", config.MaxCycles, len(suggestions))
	}
}

func TestDetectCycleWarnings_ConfidenceByLength(t *testing.T) {
	config := DefaultCycleWarningConfig()
	config.MaxCycles = 100

	// Create cycle of length 3
	cycle3 := testutil.QuickCycle(3)

	suggestions := DetectCycleWarnings(cycle3, config)
	if len(suggestions) == 0 {
		t.Error("expected cycle warning")
		return
	}

	// Shorter cycles should have higher confidence
	for _, sug := range suggestions {
		if sug.Confidence < 0.5 {
			t.Errorf("expected confidence >= 0.5, got %f", sug.Confidence)
		}
		if sug.Confidence > 1.0 {
			t.Errorf("expected confidence <= 1.0, got %f", sug.Confidence)
		}
	}
}

func TestDetectCycleWarnings_MetadataIncluded(t *testing.T) {
	config := DefaultCycleWarningConfig()
	issues := testutil.QuickCycle(4)

	suggestions := DetectCycleWarnings(issues, config)
	if len(suggestions) == 0 {
		t.Error("expected cycle warning")
		return
	}

	sug := suggestions[0]

	// Check for cycle_length metadata
	if _, ok := sug.Metadata["cycle_length"]; !ok {
		t.Error("expected cycle_length in metadata")
	}

	// Check for cycle_path metadata
	if _, ok := sug.Metadata["cycle_path"]; !ok {
		t.Error("expected cycle_path in metadata")
	}
}

func TestFormatCyclePath(t *testing.T) {
	tests := []struct {
		name     string
		cycle    []string
		expected string
	}{
		{
			name:     "empty",
			cycle:    []string{},
			expected: "",
		},
		{
			name:     "single",
			cycle:    []string{"a"},
			expected: "a",
		},
		{
			name:     "two",
			cycle:    []string{"a", "b"},
			expected: "a → b",
		},
		{
			name:     "full cycle",
			cycle:    []string{"a", "b", "c", "a"},
			expected: "a → b → c → a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCyclePath(tt.cycle)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestWouldCreateCycle_NoCycle(t *testing.T) {
	issues := testutil.QuickChain(3) // Linear: n0 <- n1 <- n2 (n1 depends on n0, n2 depends on n1)

	// Adding n2 -> n0 (leaf depends on root) is just a transitive redundancy
	// Existing path: n2 -> n1 -> n0. Adding n2 -> n0 is safe.
	wouldCycle, path := WouldCreateCycle(issues, "TEST-n2", "TEST-n0")
	if wouldCycle {
		t.Errorf("expected no cycle, but got path: %v", path)
	}
}

func TestWouldCreateCycle_CreatesCycle(t *testing.T) {
	// Linear chain: n0 <- n1 <- n2
	issues := testutil.QuickChain(3)

	// Adding n0 -> n2 creates: n0 -> n2 -> n1 -> n0 (cycle!)
	wouldCycle, path := WouldCreateCycle(issues, "TEST-n0", "TEST-n2")
	if !wouldCycle {
		t.Error("expected cycle to be detected")
		return
	}
	if len(path) == 0 {
		t.Error("expected non-empty path")
	}
}

func TestWouldCreateCycle_SelfLoop(t *testing.T) {
	issues := []model.Issue{
		{ID: "i1", Title: "Issue 1", Status: model.StatusOpen},
		{ID: "i2", Title: "Issue 2", Status: model.StatusOpen},
	}

	// Self-loop: i1 depends on i1
	wouldCycle, path := WouldCreateCycle(issues, "i1", "i1")
	if !wouldCycle {
		t.Error("expected self-loop to be detected as cycle")
		return
	}
	if len(path) < 2 {
		t.Errorf("expected path length >= 2 for self-loop, got %d", len(path))
	}
}

func TestWouldCreateCycle_ExistingCycle(t *testing.T) {
	// Already has a cycle
	issues := testutil.QuickCycle(3)

	// Adding another edge shouldn't break detection
	wouldCycle, _ := WouldCreateCycle(issues, "TEST-n0", "TEST-n1")
	// May or may not create additional cycle, depending on existing structure
	_ = wouldCycle
}

func TestCheckDependencyAddition_Valid(t *testing.T) {
	issues := []model.Issue{
		{ID: "i1", Title: "Issue 1", Status: model.StatusOpen},
		{ID: "i2", Title: "Issue 2", Status: model.StatusOpen},
	}

	canAdd, path, warning := CheckDependencyAddition(issues, "i1", "i2")
	if !canAdd {
		t.Errorf("expected canAdd=true, got false. Warning: %s", warning)
	}
	if len(path) != 0 {
		t.Errorf("expected empty path for valid addition, got %v", path)
	}
	if warning != "" {
		t.Errorf("expected empty warning for valid addition, got %s", warning)
	}
}

func TestCheckDependencyAddition_WouldCreateCycle(t *testing.T) {
	// Chain: i0 <- i1 <- i2
	issues := []model.Issue{
		{ID: "i0", Title: "Issue 0", Status: model.StatusOpen},
		{ID: "i1", Title: "Issue 1", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i0"}}},
		{ID: "i2", Title: "Issue 2", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i1"}}},
	}

	// Adding i0 -> i2 creates cycle
	canAdd, path, warning := CheckDependencyAddition(issues, "i0", "i2")
	if canAdd {
		t.Error("expected canAdd=false for cycle-creating dependency")
	}
	if len(path) == 0 {
		t.Error("expected non-empty path for cycle")
	}
	if warning == "" {
		t.Error("expected warning message for cycle")
	}
	if !strings.Contains(warning, "would create cycle") {
		t.Errorf("expected warning to mention 'would create cycle', got: %s", warning)
	}
}

func TestCycleWarningDetector(t *testing.T) {
	config := DefaultCycleWarningConfig()
	detector := NewCycleWarningDetector(config)

	if detector == nil {
		t.Fatal("expected non-nil detector")
	}

	// Test Detect method
	issues := testutil.QuickCycle(3)
	suggestions := detector.Detect(issues)
	if len(suggestions) == 0 {
		t.Error("expected cycle suggestions from detector")
	}
}

func TestCycleWarningDetector_ValidateNewDependency(t *testing.T) {
	config := DefaultCycleWarningConfig()
	detector := NewCycleWarningDetector(config)

	// Chain: i0 <- i1 <- i2
	issues := []model.Issue{
		{ID: "i0", Title: "Issue 0", Status: model.StatusOpen},
		{ID: "i1", Title: "Issue 1", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i0"}}},
		{ID: "i2", Title: "Issue 2", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "i1"}}},
	}

	// Valid addition: i2 -> i0 is just a shortcut (i2 already transitively depends on i0)
	canAdd, warning := detector.ValidateNewDependency(issues, "i2", "i0")
	if !canAdd {
		t.Errorf("expected valid dependency, got warning: %s", warning)
	}

	// Invalid addition (creates cycle): i0 -> i2 means i0 depends on i2
	canAdd, warning = detector.ValidateNewDependency(issues, "i0", "i2")
	if canAdd {
		t.Error("expected invalid dependency for cycle-creating edge")
	}
	if warning == "" {
		t.Error("expected warning for invalid dependency")
	}
}

func TestDetectCycleWarnings_SelfLoopConfig(t *testing.T) {
	// Note: gonum's graph library doesn't allow self-loops during graph construction.
	// So we test the IncludeSelfLoops config with a 2-cycle instead.
	issues := []model.Issue{
		{
			ID:     "i1",
			Title:  "Issue 1",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "i2"},
			},
		},
		{
			ID:     "i2",
			Title:  "Issue 2",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{DependsOnID: "i1"},
			},
		},
	}

	// Test basic cycle detection (2-cycle, not self-loop)
	config := DefaultCycleWarningConfig()
	suggestions := DetectCycleWarnings(issues, config)

	if len(suggestions) == 0 {
		t.Error("expected to detect the 2-cycle")
	}

	// Verify cycle is detected correctly
	cycleFound := false
	for _, sug := range suggestions {
		if strings.Contains(sug.Summary, "cycle") || strings.Contains(sug.Summary, "Direct") {
			cycleFound = true
			break
		}
	}
	if !cycleFound {
		t.Error("expected cycle warning in suggestions")
	}
}

func TestDetectCycleWarnings_Determinism(t *testing.T) {
	config := DefaultCycleWarningConfig()
	issues := testutil.QuickCycle(5)

	// Run multiple times
	var firstResult []Suggestion
	for i := 0; i < 5; i++ {
		suggestions := DetectCycleWarnings(issues, config)
		if firstResult == nil {
			firstResult = suggestions
		} else {
			if len(suggestions) != len(firstResult) {
				t.Errorf("run %d: result count changed: %d vs %d",
					i, len(suggestions), len(firstResult))
			}
		}
	}
}

func BenchmarkDetectCycleWarnings_Small(b *testing.B) {
	issues := testutil.QuickCycle(5)
	config := DefaultCycleWarningConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DetectCycleWarnings(issues, config)
	}
}

func BenchmarkDetectCycleWarnings_Medium(b *testing.B) {
	issues := testutil.QuickCycle(20)
	config := DefaultCycleWarningConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DetectCycleWarnings(issues, config)
	}
}

func BenchmarkWouldCreateCycle_Chain(b *testing.B) {
	issues := testutil.QuickChain(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		WouldCreateCycle(issues, "n0", "n49")
	}
}

func BenchmarkCheckDependencyAddition(b *testing.B) {
	issues := testutil.QuickStar(20)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CheckDependencyAddition(issues, "n1", "n2")
	}
}
