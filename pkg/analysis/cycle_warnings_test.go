package analysis

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	for i := range issues {
		issues[i].Origin = suggestionTestOrigin(issues[i].ID)
	}

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
		if !strings.Contains(sug.ActionCommand, "'dep' 'remove'") {
			t.Errorf("expected routed dependency removal, got %s", sug.ActionCommand)
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

func TestWouldCreateCycle_ParentChildNonBlocking(t *testing.T) {
	issues := []model.Issue{
		{
			ID:     "EPIC-1",
			Title:  "Parent epic",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "EPIC-1", DependsOnID: "TASK-1", Type: model.DepParentChild},
			},
		},
		{
			ID:     "TASK-1",
			Title:  "Child task",
			Status: model.StatusOpen,
		},
	}

	if wouldCycle, path := WouldCreateCycle(issues, "TASK-1", "EPIC-1"); wouldCycle {
		t.Fatalf("non-blocking DepParentChild must not trigger cycle detection, got path %v", path)
	}
}

func TestGetCycleFreeSuggestions_FiltersDirectTransitiveAndMutualCycles(t *testing.T) {
	issues := []model.Issue{
		{
			ID:     "A",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "A", DependsOnID: "B", Type: model.DepBlocks},
			},
		},
		{
			ID:     "B",
			Status: model.StatusOpen,
			Dependencies: []*model.Dependency{
				{IssueID: "B", DependsOnID: "C", Type: model.DepBlocks},
			},
		},
		{ID: "C", Status: model.StatusOpen},
		{ID: "X", Status: model.StatusOpen},
		{ID: "Y", Status: model.StatusOpen},
	}

	suggestions := []Suggestion{
		{Type: SuggestionLabelSuggestion, TargetBead: "C", Confidence: 0.99},
		{Type: SuggestionMissingDependency, TargetBead: "B", RelatedBead: "A", Confidence: 0.95}, // Direct cycle with A->B
		{Type: SuggestionMissingDependency, TargetBead: "C", RelatedBead: "A", Confidence: 0.92}, // Transitive cycle with A->B->C
		{Type: SuggestionMissingDependency, TargetBead: "X", RelatedBead: "X", Confidence: 0.91}, // Self-loop
		{Type: SuggestionMissingDependency, TargetBead: "X", RelatedBead: "Y", Confidence: 0.90}, // Valid
		{Type: SuggestionMissingDependency, TargetBead: "Y", RelatedBead: "X", Confidence: 0.85}, // Mutual cycle with X->Y
	}

	got := GetCycleFreeSuggestions(issues, suggestions)
	if len(got) != 2 {
		t.Fatalf("expected 2 cycle-free suggestions (label + X->Y), got %+v", got)
	}
	if got[0].Type != SuggestionLabelSuggestion || got[1].TargetBead != "X" || got[1].RelatedBead != "Y" {
		t.Fatalf("unexpected filtered suggestions: %+v", got)
	}
}

func TestWave32_CollateralRankedCycleBreakAndRemovalPreflight(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	// Cycle: A -> B -> C -> A.
	// Extra dependents D1, D2 also depend on B (so B has 3 active dependents: A, D1, D2),
	// whereas C only has 1 active dependent (B), and A has 2 (C, D3).
	// Minimum-collateral cut edge MUST be B -> C (collateral 1) on BOTH CycleBreak AND DetectCycleWarnings!
	issues := []model.Issue{
		{ID: "A", Title: "Node A", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "A", DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "B", Title: "Node B", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "B", DependsOnID: "C", Type: model.DepBlocks}}},
		{ID: "C", Title: "Node C", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "C", DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "D1", Title: "Extra dep 1 on B", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "D1", DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D2", Title: "Extra dep 2 on B", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "D2", DependsOnID: "B", Type: model.DepBlocks}}},
		{ID: "D3", Title: "Extra dep 3 on A", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "D3", DependsOnID: "A", Type: model.DepBlocks}}},
	}
	for i := range issues {
		issues[i].Origin = suggestionTestOrigin(issues[i].ID)
	}

	an := NewAnalyzer(issues)
	an.SetNow(now)
	adv := an.GenerateAdvancedInsights(DefaultAdvancedInsightsConfig())
	if adv.CycleBreak == nil || len(adv.CycleBreak.Suggestions) == 0 {
		t.Fatalf("expected CycleBreak suggestions, got %+v", adv.CycleBreak)
	}
	top := adv.CycleBreak.Suggestions[0]
	if top.EdgeFrom != "B" || top.EdgeTo != "C" || top.Collateral != 1 {
		t.Fatalf("W32-2 regression: CycleBreak top cut = %s->%s (collateral=%d), want B->C (collateral=1); all=%+v",
			top.EdgeFrom, top.EdgeTo, top.Collateral, adv.CycleBreak.Suggestions)
	}

	warnings := DetectCycleWarnings(issues, DefaultCycleWarningConfig())
	if len(warnings) != 1 {
		t.Fatalf("expected 1 cycle warning, got %+v", warnings)
	}
	if !strings.Contains(warnings[0].ActionCommand, "'B' 'C'") {
		t.Fatalf("W32-2 regression: DetectCycleWarnings did not choose lowest-collateral edge B->C, got action=%q meta=%+v",
			warnings[0].ActionCommand, warnings[0].Metadata)
	}

	// CheckDependencyRemoval positive control: removing B -> C breaks the cycle and unblocks B!
	rem := CheckDependencyRemoval(issues, "B", "C", now)
	if !rem.Exists || rem.CyclesBefore != 1 || rem.CyclesAfter != 0 || rem.CyclesBroken != 1 || !rem.WouldBreakAllCycles {
		t.Fatalf("W32-2 CheckDependencyRemoval(B->C) unexpected result: %+v", rem)
	}
	foundB := false
	for _, id := range rem.NewlyUnblocked {
		if id == "B" {
			foundB = true
		}
	}
	if !foundB {
		t.Fatalf("W32-2 CheckDependencyRemoval(B->C) expected B in NewlyUnblocked, got %+v", rem.NewlyUnblocked)
	}

	// Negative control: non-existent edge.
	missing := CheckDependencyRemoval(issues, "A", "C", now)
	if missing.Exists || missing.Warning == "" {
		t.Fatalf("W32-2 CheckDependencyRemoval negative control should report Exists=false + warning, got %+v", missing)
	}

	// P22A-3: Last-record-wins dedup in CheckDependencyRemoval — superseded earlier edge does NOT trigger Exists=true
	// and duplicate records for an unblocked issue never emit duplicate NewlyUnblocked IDs!
	dupLog := []model.Issue{
		{ID: "SUP-1", Title: "Superseded", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "SUP-1", DependsOnID: "SUP-2", Type: model.DepBlocks}}},
		{ID: "SUP-1", Title: "Latest without edge", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now},
		{ID: "SUP-2", Title: "Target", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now},
	}
	if sup := CheckDependencyRemoval(dupLog, "SUP-1", "SUP-2", now); sup.Exists {
		t.Fatalf("P22A-3 regression: superseded earlier edge triggered Exists=true: %+v", sup)
	}
	dupUnblock := []model.Issue{
		{ID: "U-1", Title: "Blocker", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now},
		{ID: "U-2", Title: "Old record", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "U-2", DependsOnID: "U-1", Type: model.DepBlocks}}},
		{ID: "U-2", Title: "Latest record", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "U-2", DependsOnID: "U-1", Type: model.DepBlocks}}},
	}
	uRes := CheckDependencyRemoval(dupUnblock, "U-2", "U-1", now)
	if !uRes.Exists || len(uRes.NewlyUnblocked) != 1 || uRes.NewlyUnblocked[0] != "U-2" {
		t.Fatalf("P22A-3 regression: duplicate records emitted duplicate NewlyUnblocked: %+v", uRes)
	}
}

func TestWave32_ComputeCrossRepoFlow(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	// Negative control 1: single-repo project returns nil.
	single := []model.Issue{
		{ID: "S-1", Title: "One", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now},
		{ID: "S-2", Title: "Two", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "S-2", DependsOnID: "S-1", Type: model.DepBlocks}}},
	}
	if flow := ComputeCrossRepoFlow(single, nil); flow != nil {
		t.Fatalf("single-repo ComputeCrossRepoFlow must return nil, got %+v", flow)
	}

	// Multi-repo with active cross-repo blockers + closed cross-repo edge (ignored) + hidden candidate scope.
	multi := []model.Issue{
		{ID: "api-1", Title: "Auth token", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now},
		{ID: "api-closed", Title: "Done API", Status: model.StatusClosed, SourceRepo: "api", CreatedAt: now, UpdatedAt: now},
		{ID: "web-1", Title: "Login page", Status: model.StatusOpen, SourceRepo: "web", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks},
				{IssueID: "web-1", DependsOnID: "api-closed", Type: model.DepBlocks},
			}},
		{ID: "mob-1", Title: "Mobile login", Status: model.StatusOpen, SourceRepo: "mobile", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "mob-1", DependsOnID: "api-1", Type: model.DepBlocks}}},
	}

	flow := ComputeCrossRepoFlow(multi, nil)
	if flow == nil || flow.TotalCrossRepoDeps != 2 || len(flow.Dependencies) != 2 {
		t.Fatalf("expected 2 active cross-repo edges, got %+v", flow)
	}
	if len(flow.BottleneckRepos) == 0 || flow.BottleneckRepos[0] != "api" {
		t.Fatalf("expected api as top bottleneck repo, got %+v", flow.BottleneckRepos)
	}
	if len(flow.TopHandoffBlockers) != 1 || flow.TopHandoffBlockers[0].IssueID != "api-1" || flow.TopHandoffBlockers[0].BlockedCount != 2 {
		t.Fatalf("expected api-1 blocking 2 cross-repo issues, got %+v", flow.TopHandoffBlockers)
	}

	// Candidate scoping: restricting to {api-1, web-1} must exclude mob-1 and mobile repo.
	scoped := ComputeCrossRepoFlow(multi, map[string]bool{"api-1": true, "web-1": true})
	if scoped == nil || scoped.TotalCrossRepoDeps != 1 || len(scoped.Dependencies) != 1 || scoped.Dependencies[0].ToRepo != "web" {
		t.Fatalf("scoped ComputeCrossRepoFlow leaked out-of-scope repo: %+v", scoped)
	}

	// P22A-1: Case-insensitive canonical repo normalization + tombstone/superseded exclusion!
	mixedCase := []model.Issue{
		{ID: "tomb-1", Title: "Tomb", Status: model.StatusTombstone, SourceRepo: "LegacyRepo", CreatedAt: now, UpdatedAt: now},
		{ID: "api-1", Title: "API Upper", Status: model.StatusOpen, SourceRepo: "API-", CreatedAt: now, UpdatedAt: now},
		{ID: "api-2", Title: "API Lower", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now},
		{ID: "web-1", Title: "Web Mixed", Status: model.StatusOpen, SourceRepo: "Web", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks},
				{IssueID: "web-1", DependsOnID: "api-2", Type: model.DepBlocks},
			}},
	}
	mcFlow := ComputeCrossRepoFlow(mixedCase, nil)
	if mcFlow == nil || len(mcFlow.Repos) != 2 || mcFlow.Repos[0] != "api" || mcFlow.Repos[1] != "web" {
		t.Fatalf("P22A-1 regression: mixed-case/tombstone repos not canonicalized: %+v", mcFlow)
	}
	if len(mcFlow.Dependencies) != 1 || mcFlow.Dependencies[0].FromRepo != "api" || mcFlow.Dependencies[0].ToRepo != "web" || mcFlow.Dependencies[0].IssueCount != 2 {
		t.Fatalf("P22A-1 regression: mixed-case repo edges split instead of merging: %+v", mcFlow.Dependencies)
	}
}

func TestWave33_Pass23_ScopedCrossRepoTriageAndRemovalEnvOverrides(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	// P23-4: ComputeTriageFromAnalyzer with SetReadinessScope (opts.CandidateIDs == nil)
	// must NOT leak hidden out-of-scope repos into ProjectHealth.CrossRepoFlow.
	multi := []model.Issue{
		{ID: "api-1", Title: "API core", Status: model.StatusOpen, Priority: 1, SourceRepo: "api", CreatedAt: now, UpdatedAt: now},
		{ID: "web-1", Title: "Web UI", Status: model.StatusOpen, Priority: 1, SourceRepo: "web", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "web-1", DependsOnID: "api-1", Type: model.DepBlocks}}},
		{ID: "mob-1", Title: "Mobile app", Status: model.StatusOpen, Priority: 1, SourceRepo: "mobile", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "mob-1", DependsOnID: "api-1", Type: model.DepBlocks}}},
	}
	an := NewAnalyzer(multi)
	an.SetNow(now)
	an.SetReadinessScope(model.NewReadinessIndex(multi), map[string]bool{"api-1": true, "web-1": true})
	stats := an.Analyze()
	triage := ComputeTriageFromAnalyzer(an, &stats, multi, TriageOptions{}, now)
	if triage.ProjectHealth.CrossRepoFlow == nil {
		t.Fatalf("P23-4 expected scoped CrossRepoFlow for api->web, got nil")
	}
	if triage.ProjectHealth.CrossRepoFlow.TotalCrossRepoDeps != 1 || len(triage.ProjectHealth.CrossRepoFlow.Dependencies) != 1 {
		t.Fatalf("P23-4 regression: leaked hidden mobile repo into CrossRepoFlow: %+v", triage.ProjectHealth.CrossRepoFlow)
	}
	for _, r := range triage.ProjectHealth.CrossRepoFlow.Repos {
		if r == "mobile" {
			t.Fatalf("P23-4 regression: mobile repo leaked into Repos: %+v", triage.ProjectHealth.CrossRepoFlow.Repos)
		}
	}

	// P23-5: CheckDependencyRemoval honors ApplyEnvOverrides (BV_SKIP_PHASE2=1) without crashing
	// or fabricating WouldBreakAllCycles=true when cycle analysis is skipped, and emits an inconclusive warning.
	cyc := []model.Issue{
		{ID: "C-1", Title: "Cycle 1", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "C-1", DependsOnID: "C-2", Type: model.DepBlocks}}},
		{ID: "C-2", Title: "Cycle 2", Status: model.StatusOpen, CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "C-2", DependsOnID: "C-1", Type: model.DepBlocks}}},
	}
	t.Setenv(EnvSkipPhase2, "1")
	noCycRes := CheckDependencyRemoval(cyc, "C-1", "C-2", now)
	if !noCycRes.Exists || noCycRes.CyclesBefore != 0 || noCycRes.CyclesAfter != 0 || noCycRes.WouldBreakAllCycles || !strings.Contains(noCycRes.Warning, "skipped") {
		t.Fatalf("P23-5 regression: CheckDependencyRemoval ignored BV_SKIP_PHASE2=1: %+v", noCycRes)
	}
	t.Setenv(EnvSkipPhase2, "")
	withCycRes := CheckDependencyRemoval(cyc, "C-1", "C-2", now)
	if !withCycRes.Exists || withCycRes.CyclesBefore != 1 || withCycRes.CyclesAfter != 0 || !withCycRes.WouldBreakAllCycles || withCycRes.Warning != "" {
		t.Fatalf("P23-5 positive control failed: %+v", withCycRes)
	}

	// P24A-3: CheckDependencyRemovalWithScope excludes hidden-repo cycles and hidden-repo newly-unblocked IDs.
	scopedCyc := []model.Issue{
		{ID: "api-1", Title: "API 1", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "api-1", DependsOnID: "api-2", Type: model.DepBlocks}}},
		{ID: "api-2", Title: "API 2", Status: model.StatusOpen, SourceRepo: "api", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "api-2", DependsOnID: "api-1", Type: model.DepBlocks}}},
		{ID: "hid-child", Title: "Hidden child of api-1", Status: model.StatusOpen, SourceRepo: "mobile", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "hid-child", DependsOnID: "api-1", Type: model.DepParentChild}}},
		{ID: "mob-1", Title: "Mobile cyc 1", Status: model.StatusOpen, SourceRepo: "mobile", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "mob-1", DependsOnID: "mob-2", Type: model.DepBlocks}}},
		{ID: "mob-2", Title: "Mobile cyc 2", Status: model.StatusOpen, SourceRepo: "mobile", CreatedAt: now, UpdatedAt: now,
			Dependencies: []*model.Dependency{{IssueID: "mob-2", DependsOnID: "mob-1", Type: model.DepBlocks}}},
	}
	scRes := CheckDependencyRemovalWithScope(scopedCyc, "api-1", "api-2", map[string]bool{"api-1": true, "api-2": true}, now)
	if !scRes.Exists || scRes.CyclesBefore != 1 || scRes.CyclesAfter != 0 || scRes.CyclesBroken != 1 || !scRes.WouldBreakAllCycles {
		t.Fatalf("P24A-3 CheckDependencyRemovalWithScope leaked hidden mobile cycle into counts: %+v", scRes)
	}
	for _, id := range scRes.NewlyUnblocked {
		if id == "hid-child" {
			t.Fatalf("P24A-3 CheckDependencyRemovalWithScope leaked hidden-repo newly-unblocked child: %+v", scRes.NewlyUnblocked)
		}
	}
}

func TestCheckDependencyRemoval_W35_3_CyclesTruncatedDisclosure(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	// Build 102 disjoint 2-cycles (102 cyclic SCCs > maxCycles=100 cap) so Tarjan SCC
	// cycle enumeration genuinely hits truncation on both before and after removal.
	const numCycles = 102
	issues := make([]model.Issue, 0, numCycles*2)
	for i := 0; i < numCycles; i++ {
		a := fmt.Sprintf("C%03d-A", i)
		b := fmt.Sprintf("C%03d-B", i)
		issues = append(issues,
			model.Issue{
				ID: a, Title: "Cycle A " + a, Status: model.StatusOpen, Priority: 1,
				CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: a, DependsOnID: b, Type: model.DepBlocks}},
			},
			model.Issue{
				ID: b, Title: "Cycle B " + b, Status: model.StatusOpen, Priority: 1,
				CreatedAt: now, UpdatedAt: now,
				Dependencies: []*model.Dependency{{IssueID: b, DependsOnID: a, Type: model.DepBlocks}},
			},
		)
	}
	res := CheckDependencyRemoval(issues, "C000-A", "C000-B", now)
	if !res.Exists {
		t.Fatalf("expected C000-A -> C000-B to exist: %+v", res)
	}
	if !res.CyclesTruncated {
		t.Fatalf("W35-3 expected CyclesTruncated=true with 102 disjoint SCC cycles (>100 cap), got %+v", res)
	}
	if res.WouldBreakAllCycles || res.CyclesBroken != 0 {
		t.Fatalf("W35-3 removing 1 of 102 cycles with afterTruncated=true must report CyclesBroken=0 & WouldBreakAllCycles=false: %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Warning), "inconclusive") {
		t.Fatalf("W35-3 expected inconclusive truncation warning, got %+v", res)
	}

	// Scoped negative control: scope strictly to C101 (pushed past the 100-cycle cap before removal,
	// shifted into the 100-cycle window after removing C000!). CyclesBroken must NEVER go negative
	// and WouldBreakAllCycles must remain false while afterTruncated is true.
	scoped := CheckDependencyRemovalWithScope(issues, "C000-A", "C000-B", map[string]bool{"C101-A": true, "C101-B": true}, now)
	if !scoped.CyclesTruncated || scoped.CyclesBroken < 0 || scoped.WouldBreakAllCycles {
		t.Fatalf("W35-3 scoped truncated check produced invalid counts/verdict: %+v", scoped)
	}
}
