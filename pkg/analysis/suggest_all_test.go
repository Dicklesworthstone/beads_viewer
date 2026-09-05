package analysis

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ============================================================================
// DefaultSuggestAllConfig Tests
// ============================================================================

func TestDefaultSuggestAllConfig(t *testing.T) {
	config := DefaultSuggestAllConfig()

	// All features should be enabled by default
	if !config.EnableDuplicates {
		t.Error("EnableDuplicates should be true by default")
	}
	if !config.EnableDependencies {
		t.Error("EnableDependencies should be true by default")
	}
	if !config.EnableLabels {
		t.Error("EnableLabels should be true by default")
	}
	if !config.EnableCycles {
		t.Error("EnableCycles should be true by default")
	}

	// Default limits
	if config.MinConfidence != 0.0 {
		t.Errorf("MinConfidence: got %v, want 0.0", config.MinConfidence)
	}
	if config.MaxSuggestions != 50 {
		t.Errorf("MaxSuggestions: got %d, want 50", config.MaxSuggestions)
	}

	// No filters by default
	if config.FilterType != "" {
		t.Errorf("FilterType should be empty, got %q", config.FilterType)
	}
	if config.FilterBead != "" {
		t.Errorf("FilterBead should be empty, got %q", config.FilterBead)
	}

	// Nested configs should be populated
	if config.Duplicates.JaccardThreshold <= 0 {
		t.Error("Duplicates config should have sensible defaults")
	}
	if config.Labels.MinConfidence <= 0 {
		t.Error("Labels config should have sensible defaults")
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Empty/Basic Cases
// ============================================================================

func TestGenerateAllSuggestions_EmptyIssues(t *testing.T) {
	config := DefaultSuggestAllConfig()

	set := GenerateAllSuggestions(nil, config, "hash123")
	if set.Stats.Total != 0 {
		t.Errorf("expected 0 suggestions for nil issues, got %d", set.Stats.Total)
	}

	set = GenerateAllSuggestions([]model.Issue{}, config, "hash456")
	if set.Stats.Total != 0 {
		t.Errorf("expected 0 suggestions for empty issues, got %d", set.Stats.Total)
	}
}

func TestGenerateAllSuggestions_SingleIssue(t *testing.T) {
	issues := []model.Issue{
		{ID: "SINGLE-1", Title: "Only issue", Status: model.StatusOpen},
	}
	config := DefaultSuggestAllConfig()

	set := GenerateAllSuggestions(issues, config, "hash789")

	// Single issue cannot have duplicates, cycles, or dependencies
	// May have label suggestions if it matches keywords
	// This is a degenerate case - just ensure no crash
	if set.DataHash != "hash789" {
		t.Errorf("DataHash: got %q, want %q", set.DataHash, "hash789")
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Feature Enable/Disable
// ============================================================================

func TestGenerateAllSuggestions_OnlyDuplicates(t *testing.T) {
	// Create issues that would produce duplicates
	issues := []model.Issue{
		{ID: "DUP-1", Title: "Fix authentication bug in login", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix authentication bug in login page", Status: model.StatusOpen},
	}

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = true
	config.EnableDependencies = false
	config.EnableLabels = false
	config.EnableCycles = false
	config.Duplicates.JaccardThreshold = 0.3

	set := GenerateAllSuggestions(issues, config, "dup-hash")

	// All suggestions should be duplicate type
	for _, sug := range set.Suggestions {
		if sug.Type != SuggestionPotentialDuplicate {
			t.Errorf("expected only duplicate suggestions, got %s", sug.Type)
		}
	}
}

func TestGenerateAllSuggestions_OnlyLabels(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix critical bug in auth", Status: model.StatusOpen},
		{ID: "LABELED-1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"}},
	}

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = false
	config.EnableDependencies = false
	config.EnableLabels = true
	config.EnableCycles = false
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "label-hash")

	// All suggestions should be label type
	for _, sug := range set.Suggestions {
		if sug.Type != SuggestionLabelSuggestion {
			t.Errorf("expected only label suggestions, got %s", sug.Type)
		}
	}
}

func TestGenerateAllSuggestions_OnlyCycles(t *testing.T) {
	// Create issues with a cycle
	issues := []model.Issue{
		{
			ID: "CYCLE-A", Title: "Issue A", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "CYCLE-B", Type: model.DepBlocks}},
		},
		{
			ID: "CYCLE-B", Title: "Issue B", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "CYCLE-A", Type: model.DepBlocks}},
		},
	}

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = false
	config.EnableDependencies = false
	config.EnableLabels = false
	config.EnableCycles = true

	set := GenerateAllSuggestions(issues, config, "cycle-hash")

	// Should find the cycle
	foundCycle := false
	for _, sug := range set.Suggestions {
		if sug.Type == SuggestionCycleWarning {
			foundCycle = true
			break
		}
	}
	if !foundCycle && set.Stats.Total > 0 {
		// If there are suggestions but no cycle, check types
		t.Logf("Got %d suggestions but no cycle warning", set.Stats.Total)
	}
}

func TestGenerateAllSuggestions_AllDisabled(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "BUG-2", Title: "Fix bug", Status: model.StatusOpen}, // duplicate
	}

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = false
	config.EnableDependencies = false
	config.EnableLabels = false
	config.EnableCycles = false

	set := GenerateAllSuggestions(issues, config, "disabled-hash")

	if set.Stats.Total != 0 {
		t.Errorf("expected 0 suggestions with all features disabled, got %d", set.Stats.Total)
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Filtering
// ============================================================================

func TestGenerateAllSuggestions_MinConfidenceFilter(t *testing.T) {
	// Create issues with potential label suggestions
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	// Low threshold - should get suggestions
	configLow := DefaultSuggestAllConfig()
	configLow.EnableDuplicates = false
	configLow.EnableDependencies = false
	configLow.EnableCycles = false
	configLow.Labels.MinConfidence = 0.1
	configLow.MinConfidence = 0.1

	setLow := GenerateAllSuggestions(issues, configLow, "low-hash")
	lowCount := setLow.Stats.Total

	// High threshold - should filter out more
	configHigh := DefaultSuggestAllConfig()
	configHigh.EnableDuplicates = false
	configHigh.EnableDependencies = false
	configHigh.EnableCycles = false
	configHigh.Labels.MinConfidence = 0.1
	configHigh.MinConfidence = 0.9

	setHigh := GenerateAllSuggestions(issues, configHigh, "high-hash")
	highCount := setHigh.Stats.Total

	// Higher threshold should result in fewer or equal suggestions
	if highCount > lowCount {
		t.Errorf("higher threshold should have <= suggestions: low=%d, high=%d", lowCount, highCount)
	}
}

func TestGenerateAllSuggestions_TypeFilter(t *testing.T) {
	// Create issues that could trigger multiple suggestion types
	issues := []model.Issue{
		{ID: "DUP-1", Title: "Fix bug in login", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix bug in login", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	// Filter to only duplicates
	config := DefaultSuggestAllConfig()
	config.FilterType = SuggestionPotentialDuplicate
	config.Duplicates.JaccardThreshold = 0.3

	set := GenerateAllSuggestions(issues, config, "type-hash")

	for _, sug := range set.Suggestions {
		if sug.Type != SuggestionPotentialDuplicate {
			t.Errorf("type filter should only return %s, got %s", SuggestionPotentialDuplicate, sug.Type)
		}
	}
}

func TestGenerateAllSuggestions_BeadFilter(t *testing.T) {
	issues := []model.Issue{
		{ID: "TARGET-1", Title: "Fix bug in login", Status: model.StatusOpen},
		{ID: "TARGET-2", Title: "Fix bug in login page", Status: model.StatusOpen},
		{ID: "OTHER-1", Title: "Something else", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultSuggestAllConfig()
	config.FilterBead = "TARGET-1"
	config.Duplicates.JaccardThreshold = 0.3

	set := GenerateAllSuggestions(issues, config, "bead-hash")

	for _, sug := range set.Suggestions {
		if sug.TargetBead != "TARGET-1" && sug.RelatedBead != "TARGET-1" {
			t.Errorf("bead filter should only return suggestions involving TARGET-1, got target=%s related=%s",
				sug.TargetBead, sug.RelatedBead)
		}
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Sorting and Limits
// ============================================================================

func TestGenerateAllSuggestions_SortedByConfidence(t *testing.T) {
	// Create issues that produce multiple suggestions with varying confidence
	issues := []model.Issue{
		{ID: "DUP-1", Title: "Fix authentication bug in login", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix authentication bug in login page", Status: model.StatusOpen},
		{ID: "DUP-3", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"}},
	}

	config := DefaultSuggestAllConfig()
	config.Duplicates.JaccardThreshold = 0.2
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "sort-hash")

	if len(set.Suggestions) < 2 {
		t.Skip("not enough suggestions to test sorting")
	}

	// Verify sorted by confidence (descending)
	for i := 1; i < len(set.Suggestions); i++ {
		if set.Suggestions[i].Confidence > set.Suggestions[i-1].Confidence {
			t.Errorf("suggestions not sorted by confidence at index %d: %v > %v",
				i, set.Suggestions[i].Confidence, set.Suggestions[i-1].Confidence)
		}
	}
}

func TestGenerateAllSuggestions_MaxSuggestionsLimit(t *testing.T) {
	// Create many issues to generate many suggestions
	var issues []model.Issue
	for i := 0; i < 20; i++ {
		issues = append(issues, model.Issue{
			ID:     "BUG-" + string(rune('A'+i)),
			Title:  "Fix bug in auth",
			Status: model.StatusOpen,
		})
	}
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"},
	})

	config := DefaultSuggestAllConfig()
	config.MaxSuggestions = 5
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "limit-hash")

	if set.Stats.Total > 5 {
		t.Errorf("MaxSuggestions=5 but got %d suggestions", set.Stats.Total)
	}
}

func TestGenerateAllSuggestions_MaxSuggestionsZero(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultSuggestAllConfig()
	config.MaxSuggestions = 0 // Zero means no limit
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "nolimit-hash")

	// Should return all suggestions (no limit applied)
	// Just verify it doesn't crash and returns something reasonable
	if set.DataHash != "nolimit-hash" {
		t.Errorf("DataHash mismatch")
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Deduplication
// ============================================================================

func TestGenerateAllSuggestions_NoDuplicateSuggestions(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix critical bug in auth login", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"}},
	}

	config := DefaultSuggestAllConfig()
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "dedup-hash")

	// Semantic identity survives an unavailable mutation route. Distinct label
	// recommendations must not collapse into one empty action command.
	seen := make(map[string]bool)
	for _, sug := range set.Suggestions {
		key := sug.TargetBead + "|" + string(sug.Type) + "|" + sug.RelatedBead + "|" + sug.Summary
		if seen[key] {
			t.Errorf("duplicate suggestion found: %s", key)
		}
		seen[key] = true
	}
}

// ============================================================================
// GenerateAllSuggestions Tests - Integration
// ============================================================================

func TestGenerateAllSuggestions_MixedSuggestionTypes(t *testing.T) {
	// Create a scenario that could trigger multiple suggestion types
	issues := []model.Issue{
		// Potential duplicates
		{ID: "DUP-1", Title: "Fix login timeout bug", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix login timeout issue", Status: model.StatusOpen},

		// Cycle
		{
			ID: "CYCLE-A", Title: "Refactor auth module", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "CYCLE-B", Type: model.DepBlocks}},
		},
		{
			ID: "CYCLE-B", Title: "Update auth tests", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{DependsOnID: "CYCLE-A", Type: model.DepBlocks}},
		},

		// Label candidates
		{ID: "UNLABELED-1", Title: "Fix database query performance", Status: model.StatusOpen},

		// Labeled issues (for label pool)
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "performance", "database"}},
	}

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = true
	config.EnableDependencies = true
	config.EnableLabels = true
	config.EnableCycles = true
	config.Duplicates.JaccardThreshold = 0.3
	config.Labels.MinConfidence = 0.1

	set := GenerateAllSuggestions(issues, config, "mixed-hash")

	// Categorize suggestions by type
	typeCount := make(map[SuggestionType]int)
	for _, sug := range set.Suggestions {
		typeCount[sug.Type]++
	}

	// Log what we got
	t.Logf("Suggestion counts by type: %v", typeCount)

	// Should have at least some suggestions
	if set.Stats.Total == 0 {
		t.Error("expected some suggestions from mixed scenario")
	}

	// Stats should be populated
	if set.Stats.ByType == nil {
		t.Error("Stats.ByType should be populated")
	}
}

func TestGenerateAllSuggestions_DataHashPreserved(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
	}

	testHash := "unique-test-hash-abc123"
	config := DefaultSuggestAllConfig()

	set := GenerateAllSuggestions(issues, config, testHash)

	if set.DataHash != testHash {
		t.Errorf("DataHash: got %q, want %q", set.DataHash, testHash)
	}
}

// ============================================================================
// RobotSuggestOutput Tests
// ============================================================================

func TestGenerateRobotSuggestOutput_Structure(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug in auth", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultSuggestAllConfig()
	config.Labels.MinConfidence = 0.1
	config.FilterType = SuggestionLabelSuggestion
	config.MinConfidence = 0.2
	config.FilterBead = "BUG-1"

	output := GenerateRobotSuggestOutput(issues, config, "robot-hash")

	// Verify structure
	if output.DataHash != "robot-hash" {
		t.Errorf("DataHash: got %q, want %q", output.DataHash, "robot-hash")
	}

	// GeneratedAt should be valid RFC3339
	_, err := time.Parse(time.RFC3339, output.GeneratedAt)
	if err != nil {
		t.Errorf("GeneratedAt not valid RFC3339: %v", err)
	}

	// Filters should reflect config
	if output.Filters.Type != string(SuggestionLabelSuggestion) {
		t.Errorf("Filters.Type: got %q, want %q", output.Filters.Type, SuggestionLabelSuggestion)
	}
	if output.Filters.MinConfidence != 0.2 {
		t.Errorf("Filters.MinConfidence: got %v, want 0.2", output.Filters.MinConfidence)
	}
	if output.Filters.BeadID != "BUG-1" {
		t.Errorf("Filters.BeadID: got %q, want %q", output.Filters.BeadID, "BUG-1")
	}

	// UsageHints should be populated
	if len(output.UsageHints) == 0 {
		t.Error("UsageHints should not be empty")
	}
}

func TestGenerateRobotSuggestOutput_UsageHints(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
	}

	config := DefaultSuggestAllConfig()
	output := GenerateRobotSuggestOutput(issues, config, "hints-hash")

	// Should have meaningful jq hints
	foundJq := false
	foundFlag := false
	for _, hint := range output.UsageHints {
		if strings.Contains(hint, "jq") {
			foundJq = true
		}
		if strings.Contains(hint, "--suggest") {
			foundFlag = true
		}
	}

	if !foundJq {
		t.Error("UsageHints should include jq examples")
	}
	if !foundFlag {
		t.Error("UsageHints should include CLI flag examples")
	}
}

func TestGenerateRobotSuggestOutputAt_PinsNestedTimesAndBytes(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)
	issues := []model.Issue{
		{ID: "DUP-3", Title: "Fix OAuth login failure", Status: model.StatusOpen},
		{ID: "DUP-1", Title: "Fix OAuth login failure", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix OAuth login failure", Status: model.StatusOpen},
	}
	config := DefaultSuggestAllConfig()
	config.EnableDependencies = false
	config.EnableLabels = false
	config.EnableCycles = false
	config.Duplicates.JaccardThreshold = 0.1

	var want []byte
	for run := 0; run < 25; run++ {
		output := GenerateRobotSuggestOutputAt(issues, config, "pinned-hash", pinned)
		if output.GeneratedAt != pinned.Format(time.RFC3339) {
			t.Fatalf("outer generated_at = %q, want %q", output.GeneratedAt, pinned.Format(time.RFC3339))
		}
		if !output.Set.GeneratedAt.Equal(pinned) {
			t.Fatalf("set generated_at = %v, want %v", output.Set.GeneratedAt, pinned)
		}
		if len(output.Set.Suggestions) == 0 {
			t.Fatal("expected duplicate suggestions")
		}
		for _, suggestion := range output.Set.Suggestions {
			if !suggestion.GeneratedAt.Equal(pinned) {
				t.Fatalf("suggestion %s generated_at = %v, want %v", suggestion.TargetBead, suggestion.GeneratedAt, pinned)
			}
		}

		got, err := json.Marshal(output)
		if err != nil {
			t.Fatalf("marshal output: %v", err)
		}
		if run == 0 {
			want = got
		} else if string(got) != string(want) {
			t.Fatalf("run %d produced non-deterministic bytes\nwant: %s\n got: %s", run, want, got)
		}
	}
}

// ============================================================================
// SuggestAllConfig Tests
// ============================================================================

func TestSuggestAllConfig_FilterTypeValues(t *testing.T) {
	// Test each filter type
	types := []SuggestionType{
		SuggestionPotentialDuplicate,
		SuggestionMissingDependency,
		SuggestionLabelSuggestion,
		SuggestionCycleWarning,
	}

	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "BUG-2", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	for _, filterType := range types {
		config := DefaultSuggestAllConfig()
		config.FilterType = filterType
		config.Duplicates.JaccardThreshold = 0.3
		config.Labels.MinConfidence = 0.1

		set := GenerateAllSuggestions(issues, config, "filter-"+string(filterType))

		// All suggestions should match filter type
		for _, sug := range set.Suggestions {
			if sug.Type != filterType {
				t.Errorf("filter %s: got suggestion type %s", filterType, sug.Type)
			}
		}
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

func TestGenerateAllSuggestions_AllClosedIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "CLOSED-1", Title: "Fix bug", Status: model.StatusClosed},
		{ID: "CLOSED-2", Title: "Fix bug", Status: model.StatusClosed},
	}

	config := DefaultSuggestAllConfig()
	set := GenerateAllSuggestions(issues, config, "closed-hash")

	// Closed issues generally shouldn't generate suggestions
	// (though some suggestion types might still apply)
	t.Logf("Got %d suggestions for closed-only issues", set.Stats.Total)
}

func TestGenerateAllSuggestions_VeryLargeIssueSet(t *testing.T) {
	// Create 100 issues
	var issues []model.Issue
	for i := 0; i < 100; i++ {
		issues = append(issues, model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i/26)) + string(rune('A'+i%26)),
			Title:  "Task number " + string(rune('A'+i%26)),
			Status: model.StatusOpen,
		})
	}
	// Add labeled issue
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"},
	})

	config := DefaultSuggestAllConfig()
	config.MaxSuggestions = 20

	start := time.Now()
	set := GenerateAllSuggestions(issues, config, "large-hash")
	elapsed := time.Since(start)

	// Should complete in reasonable time
	if elapsed > 5*time.Second {
		t.Errorf("took too long for 100 issues: %v", elapsed)
	}

	// Should respect limit
	if set.Stats.Total > 20 {
		t.Errorf("exceeded MaxSuggestions: got %d", set.Stats.Total)
	}
}

func TestGenerateAllSuggestions_UnicodeContent(t *testing.T) {
	issues := []model.Issue{
		{ID: "UNICODE-1", Title: "修复登录错误", Status: model.StatusOpen},
		{ID: "UNICODE-2", Title: "修复登录问题", Status: model.StatusOpen},
		{ID: "EMOJI-1", Title: "Fix bug 🐛", Status: model.StatusOpen},
	}

	config := DefaultSuggestAllConfig()

	// Should not panic with unicode
	set := GenerateAllSuggestions(issues, config, "unicode-hash")
	t.Logf("Got %d suggestions for unicode issues", set.Stats.Total)
}

func TestGenerateAllSuggestions_EmptyTitles(t *testing.T) {
	issues := []model.Issue{
		{ID: "EMPTY-1", Title: "", Status: model.StatusOpen},
		{ID: "EMPTY-2", Title: "", Status: model.StatusOpen},
		{ID: "NORMAL-1", Title: "Normal issue", Status: model.StatusOpen},
	}

	config := DefaultSuggestAllConfig()

	// Should not panic with empty titles
	set := GenerateAllSuggestions(issues, config, "empty-hash")
	t.Logf("Got %d suggestions for issues with empty titles", set.Stats.Total)
}

// ============================================================================
// Determinism Tests
// ============================================================================

func TestGenerateAllSuggestions_Deterministic(t *testing.T) {
	issues := []model.Issue{
		{ID: "DUP-1", Title: "Fix bug in login", Status: model.StatusOpen},
		{ID: "DUP-2", Title: "Fix bug in login page", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultSuggestAllConfig()
	config.Duplicates.JaccardThreshold = 0.3
	config.Labels.MinConfidence = 0.1

	// Run twice with same input
	set1 := GenerateAllSuggestions(issues, config, "det-hash")
	set2 := GenerateAllSuggestions(issues, config, "det-hash")

	// Should have same count
	if set1.Stats.Total != set2.Stats.Total {
		t.Errorf("non-deterministic: run1=%d, run2=%d", set1.Stats.Total, set2.Stats.Total)
	}

	// Should have same suggestions in same order
	if len(set1.Suggestions) != len(set2.Suggestions) {
		t.Fatalf("different suggestion counts")
	}

	for i := range set1.Suggestions {
		if set1.Suggestions[i].TargetBead != set2.Suggestions[i].TargetBead {
			t.Errorf("non-deterministic order at %d: %s vs %s",
				i, set1.Suggestions[i].TargetBead, set2.Suggestions[i].TargetBead)
		}
	}
}

// ============================================================================
// Benchmark Tests
// ============================================================================

func BenchmarkGenerateAllSuggestions_Small(b *testing.B) {
	issues := make([]model.Issue, 20)
	for i := range issues {
		issues[i] = model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i)),
			Title:  "Task description here",
			Status: model.StatusOpen,
		}
	}
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"},
	})

	config := DefaultSuggestAllConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateAllSuggestions(issues, config, "bench-hash")
	}
}

func BenchmarkGenerateAllSuggestions_Medium(b *testing.B) {
	issues := make([]model.Issue, 100)
	for i := range issues {
		issues[i] = model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i/26)) + string(rune('A'+i%26)),
			Title:  "Task description with more words here",
			Status: model.StatusOpen,
		}
	}
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"},
	})

	config := DefaultSuggestAllConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateAllSuggestions(issues, config, "bench-hash")
	}
}

func BenchmarkGenerateAllSuggestions_OnlyLabels(b *testing.B) {
	issues := make([]model.Issue, 50)
	for i := range issues {
		issues[i] = model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i)),
			Title:  "Fix bug in auth login",
			Status: model.StatusOpen,
		}
	}
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"},
	})

	config := DefaultSuggestAllConfig()
	config.EnableDuplicates = false
	config.EnableDependencies = false
	config.EnableCycles = false

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateAllSuggestions(issues, config, "bench-hash")
	}
}

// Ensure sort is used
var _ = sort.Slice
