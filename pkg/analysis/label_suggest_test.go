package analysis

import (
	"sort"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ============================================================================
// DefaultLabelSuggestionConfig Tests
// ============================================================================

func TestDefaultLabelSuggestionConfig(t *testing.T) {
	config := DefaultLabelSuggestionConfig()

	if config.MinConfidence != 0.5 {
		t.Errorf("MinConfidence: got %v, want 0.5", config.MinConfidence)
	}
	if config.MaxSuggestionsPerIssue != 3 {
		t.Errorf("MaxSuggestionsPerIssue: got %d, want 3", config.MaxSuggestionsPerIssue)
	}
	if config.MaxTotalSuggestions != 30 {
		t.Errorf("MaxTotalSuggestions: got %d, want 30", config.MaxTotalSuggestions)
	}
	if !config.LearnFromExisting {
		t.Error("LearnFromExisting: got false, want true")
	}
	if !config.BuiltinMappings {
		t.Error("BuiltinMappings: got false, want true")
	}
}

// ============================================================================
// builtinLabelMappings Tests
// ============================================================================

func TestBuiltinLabelMappings_Coverage(t *testing.T) {
	// Verify key mappings exist
	expectedMappings := map[string][]string{
		"bug":      {"bug"},
		"fix":      {"bug"},
		"auth":     {"auth", "security"},
		"login":    {"auth"},
		"api":      {"api"},
		"test":     {"testing"},
		"feature":  {"feature"},
		"database": {"database", "db"},
		"refactor": {"refactoring"},
		"docs":     {"documentation"},
	}

	for keyword, expectedLabels := range expectedMappings {
		labels, ok := builtinLabelMappings[keyword]
		if !ok {
			t.Errorf("missing builtin mapping for keyword %q", keyword)
			continue
		}

		for _, expected := range expectedLabels {
			found := false
			for _, label := range labels {
				if label == expected {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("keyword %q: expected label %q not found in %v", keyword, expected, labels)
			}
		}
	}
}

func TestBuiltinLabelMappings_NoEmptyLabels(t *testing.T) {
	for keyword, labels := range builtinLabelMappings {
		if len(labels) == 0 {
			t.Errorf("keyword %q has empty label list", keyword)
		}
		for _, label := range labels {
			if label == "" {
				t.Errorf("keyword %q has empty string label", keyword)
			}
		}
	}
}

// ============================================================================
// SuggestLabels Tests - Basic Functionality
// ============================================================================

func TestSuggestLabels_EmptyIssues(t *testing.T) {
	config := DefaultLabelSuggestionConfig()
	suggestions := SuggestLabels(nil, config)

	if suggestions != nil {
		t.Errorf("expected nil for empty issues, got %v", suggestions)
	}

	suggestions = SuggestLabels([]model.Issue{}, config)
	if suggestions != nil {
		t.Errorf("expected nil for empty slice, got %v", suggestions)
	}
}

func TestSuggestLabels_SkipsClosedIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "CLOSED-1", Title: "Fix critical bug", Status: model.StatusClosed},
		{ID: "TOMBSTONE-1", Title: "Removed issue", Status: model.StatusTombstone},
		{ID: "OPEN-1", Title: "Fix another bug", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1 // Lower threshold
	suggestions := SuggestLabels(issues, config)

	// Closed issue should not appear in suggestions
	for _, sug := range suggestions {
		if sug.TargetBead == "CLOSED-1" || sug.TargetBead == "TOMBSTONE-1" {
			t.Errorf("suggestion should not be for closed/tombstone issue: %+v", sug)
		}
	}
}

func TestSuggestLabels_BuiltinMappings(t *testing.T) {
	// Create issues with keywords that match builtins
	// Plus issues with labels so the label pool exists
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix login error in auth module", Status: model.StatusOpen},
		{ID: "LABELED-1", Title: "Already labeled", Status: model.StatusOpen, Labels: []string{"bug", "auth"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.2 // Lower threshold for test
	config.LearnFromExisting = false

	suggestions := SuggestLabels(issues, config)

	// Should suggest labels for BUG-1 based on "login", "error", "auth"
	foundBugAuth := false
	for _, sug := range suggestions {
		if sug.TargetBead == "BUG-1" {
			if meta, ok := sug.Metadata["suggested_label"]; ok {
				label := meta.(string)
				if label == "bug" || label == "auth" {
					foundBugAuth = true
				}
			}
		}
	}

	if !foundBugAuth {
		t.Errorf("expected suggestions for BUG-1 with bug/auth labels, got: %+v", suggestions)
	}
}

func TestSuggestLabels_NoSuggestExistingLabels(t *testing.T) {
	// Issue already has "bug" label, shouldn't suggest it again
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix critical bug", Status: model.StatusOpen, Labels: []string{"bug"}},
		{ID: "OTHER-1", Title: "Another issue", Status: model.StatusOpen, Labels: []string{"other"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	for _, sug := range suggestions {
		if sug.TargetBead == "BUG-1" {
			if meta, ok := sug.Metadata["suggested_label"]; ok {
				if meta.(string) == "bug" {
					t.Errorf("should not suggest 'bug' label when issue already has it")
				}
			}
		}
	}
}

func TestSuggestLabels_OnlyExistingLabels(t *testing.T) {
	// Suggestions should only include labels that exist in the project
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix database migration error", Status: model.StatusOpen},
		{ID: "LABELED-1", Title: "Has label", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	config.LearnFromExisting = false

	suggestions := SuggestLabels(issues, config)

	// Should only suggest "bug" (exists), not "database"/"migration" (don't exist)
	for _, sug := range suggestions {
		if meta, ok := sug.Metadata["suggested_label"]; ok {
			label := meta.(string)
			if label != "bug" {
				t.Errorf("suggested label %q doesn't exist in project", label)
			}
		}
	}
}

// ============================================================================
// SuggestLabels Tests - Learned Mappings
// ============================================================================

func TestSuggestLabels_LearnedMappings(t *testing.T) {
	// Create issues where "payment" appears with "billing" label
	issues := []model.Issue{
		// Training data - "payment" associated with "billing"
		{ID: "HIST-1", Title: "Payment processing issue", Status: model.StatusOpen, Labels: []string{"billing"}},
		{ID: "HIST-2", Title: "Payment gateway timeout", Status: model.StatusOpen, Labels: []string{"billing"}},
		{ID: "HIST-3", Title: "Payment refund flow", Status: model.StatusOpen, Labels: []string{"billing"}},
		// Issue to get suggestions for
		{ID: "NEW-1", Title: "Payment integration needed", Status: model.StatusOpen},
	}

	config := DefaultLabelSuggestionConfig()
	// Note: scoring formula gives 0.1 + (count * 0.05), so 3 occurrences = 0.25
	// Using 0.2 threshold to allow detection with modest training data
	config.MinConfidence = 0.2
	config.BuiltinMappings = false // Only test learned mappings
	config.LearnFromExisting = true

	suggestions := SuggestLabels(issues, config)

	// Should suggest "billing" for NEW-1 based on learned pattern
	foundBilling := false
	for _, sug := range suggestions {
		if sug.TargetBead == "NEW-1" {
			if meta, ok := sug.Metadata["suggested_label"]; ok {
				if meta.(string) == "billing" {
					foundBilling = true
				}
			}
		}
	}

	if !foundBilling {
		t.Errorf("expected 'billing' suggestion for NEW-1 based on learned pattern")
	}
}

func TestSuggestLabels_DisabledLearnFromExisting(t *testing.T) {
	issues := []model.Issue{
		{ID: "HIST-1", Title: "Payment issue", Status: model.StatusOpen, Labels: []string{"billing"}},
		{ID: "HIST-2", Title: "Payment problem", Status: model.StatusOpen, Labels: []string{"billing"}},
		{ID: "NEW-1", Title: "Payment integration", Status: model.StatusOpen},
	}

	config := DefaultLabelSuggestionConfig()
	config.LearnFromExisting = false
	config.BuiltinMappings = false // Neither source enabled

	suggestions := SuggestLabels(issues, config)

	// Should have no suggestions without any source enabled
	for _, sug := range suggestions {
		if sug.TargetBead == "NEW-1" {
			t.Errorf("unexpected suggestion when learning disabled: %+v", sug)
		}
	}
}

// ============================================================================
// SuggestLabels Tests - Confidence and Limits
// ============================================================================

func TestSuggestLabels_MinConfidenceThreshold(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix bug", Status: model.StatusOpen}, // Single keyword match
		{ID: "LABELED-1", Title: "Has bug label", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	// High threshold should filter out low-confidence suggestions
	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.9
	config.LearnFromExisting = false

	suggestions := SuggestLabels(issues, config)

	// Single keyword match (0.3 from builtin) should be filtered
	if len(suggestions) > 0 {
		t.Errorf("expected no suggestions with 0.9 threshold, got %d", len(suggestions))
	}
}

func TestSuggestLabels_MaxSuggestionsPerIssue(t *testing.T) {
	// Create issue that could match many labels
	issues := []model.Issue{
		{ID: "MULTI-1", Title: "Fix bug in api auth login database cache", Status: model.StatusOpen},
		// Create issues with all these labels so they exist
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
		{ID: "L2", Title: "x", Status: model.StatusOpen, Labels: []string{"api"}},
		{ID: "L3", Title: "x", Status: model.StatusOpen, Labels: []string{"auth"}},
		{ID: "L4", Title: "x", Status: model.StatusOpen, Labels: []string{"database"}},
		{ID: "L5", Title: "x", Status: model.StatusOpen, Labels: []string{"cache"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	config.MaxSuggestionsPerIssue = 2

	suggestions := SuggestLabels(issues, config)

	// Count suggestions for MULTI-1
	count := 0
	for _, sug := range suggestions {
		if sug.TargetBead == "MULTI-1" {
			count++
		}
	}

	if count > 2 {
		t.Errorf("MaxSuggestionsPerIssue=2 but got %d suggestions for MULTI-1", count)
	}
}

func TestSuggestLabels_PrefersHighestScore(t *testing.T) {
	issues := []model.Issue{
		{ID: "MULTI-1", Title: "Bug login auth issue", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
		{ID: "L2", Title: "x", Status: model.StatusOpen, Labels: []string{"auth"}},
		{ID: "L3", Title: "x", Status: model.StatusOpen, Labels: []string{"security"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	config.MaxSuggestionsPerIssue = 1
	config.LearnFromExisting = false

	suggestions := SuggestLabels(issues, config)

	var labels []string
	for _, sug := range suggestions {
		if sug.TargetBead != "MULTI-1" {
			continue
		}
		meta, ok := sug.Metadata["suggested_label"]
		if !ok {
			t.Fatalf("missing suggested_label metadata")
		}
		labels = append(labels, meta.(string))
	}

	if len(labels) != 1 {
		t.Fatalf("expected 1 suggestion for MULTI-1, got %d (%v)", len(labels), labels)
	}
	if labels[0] != "auth" {
		t.Fatalf("expected highest-score label 'auth', got %q", labels[0])
	}
}

func TestSuggestLabels_MaxTotalSuggestions(t *testing.T) {
	// Create many issues that could get suggestions
	var issues []model.Issue
	for i := 0; i < 20; i++ {
		issues = append(issues, model.Issue{
			ID:     "BUG-" + string(rune('A'+i)),
			Title:  "Fix bug in auth",
			Status: model.StatusOpen,
		})
	}
	// Add issues with labels
	issues = append(issues, model.Issue{
		ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "auth"},
	})

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	config.MaxTotalSuggestions = 5

	suggestions := SuggestLabels(issues, config)

	if len(suggestions) > 5 {
		t.Errorf("MaxTotalSuggestions=5 but got %d suggestions", len(suggestions))
	}
}

func TestSuggestLabels_ConfidenceCapped(t *testing.T) {
	// Create scenario that could produce very high confidence
	var issues []model.Issue
	// Many training examples
	for i := 0; i < 50; i++ {
		issues = append(issues, model.Issue{
			ID:     "HIST-" + string(rune('A'+i)),
			Title:  "Payment issue " + string(rune('A'+i)),
			Status: model.StatusOpen,
			Labels: []string{"billing"},
		})
	}
	issues = append(issues, model.Issue{
		ID:     "NEW-1",
		Title:  "Payment integration",
		Status: model.StatusOpen,
	})

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	// Confidence should be capped at 0.95
	for _, sug := range suggestions {
		if sug.Confidence > 0.95 {
			t.Errorf("confidence %v exceeds cap of 0.95", sug.Confidence)
		}
	}
}

func TestSuggestLabels_NonPositiveLimitsAreSafe(t *testing.T) {
	issues := []model.Issue{
		{ID: "target", Title: "critical authentication bug", Status: model.StatusOpen},
		{ID: "training", Title: "authentication bug", Status: model.StatusClosed, Labels: []string{"bug"}},
	}
	for _, tt := range []struct {
		name     string
		perIssue int
		total    int
	}{
		{name: "zero per issue", perIssue: 0, total: 10},
		{name: "negative per issue", perIssue: -1, total: 10},
		{name: "zero total", perIssue: 10, total: 0},
		{name: "negative total", perIssue: 10, total: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultLabelSuggestionConfig()
			config.MinConfidence = 0
			config.MaxSuggestionsPerIssue = tt.perIssue
			config.MaxTotalSuggestions = tt.total
			if suggestions := SuggestLabels(issues, config); len(suggestions) != 0 {
				t.Fatalf("returned %d suggestions", len(suggestions))
			}
		})
	}
}

func TestSuggestLabels_LearnedConfidenceDeterministic(t *testing.T) {
	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0
	config.MaxSuggestionsPerIssue = 10
	config.MaxTotalSuggestions = 10
	config.BuiltinMappings = false

	issues := []model.Issue{
		{ID: "alpha-1", Title: "alpha", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "beta-1", Title: "beta first", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "beta-2", Title: "beta second", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-1", Title: "gamma first", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-2", Title: "gamma second", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-3", Title: "gamma third", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-4", Title: "gamma fourth", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-5", Title: "gamma fifth", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "gamma-6", Title: "gamma sixth", Status: model.StatusClosed, Labels: []string{"learned"}},
		{ID: "target", Title: "alpha beta gamma", Status: model.StatusOpen},
	}

	var want float64
	for run := 0; run < 100; run++ {
		suggestions := SuggestLabels(issues, config)
		if len(suggestions) != 1 {
			t.Fatalf("run %d returned %d suggestions, want one", run, len(suggestions))
		}
		if run == 0 {
			want = suggestions[0].Confidence
		} else if suggestions[0].Confidence != want {
			t.Fatalf("run %d confidence=%0.17g, want stable %0.17g", run, suggestions[0].Confidence, want)
		}
	}
}

// ============================================================================
// SuggestLabels Tests - Suggestion Output Format
// ============================================================================

func TestSuggestLabels_SuggestionFormat(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix critical bug in login", Status: model.StatusOpen},
		{ID: "LABELED-1", Title: "Has labels", Status: model.StatusOpen, Labels: []string{"bug", "auth"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("expected at least one suggestion")
	}

	sug := suggestions[0]

	// Verify suggestion structure
	if sug.Type != SuggestionLabelSuggestion {
		t.Errorf("Type: got %v, want SuggestionLabelSuggestion", sug.Type)
	}
	if sug.TargetBead == "" {
		t.Error("TargetBead is empty")
	}
	if sug.Summary == "" {
		t.Error("Summary is empty")
	}
	if sug.Reason == "" {
		t.Error("Reason is empty")
	}
	if sug.ActionCommand == "" {
		t.Error("ActionCommand is empty")
	}
	if !strings.Contains(sug.ActionCommand, "br update") {
		t.Errorf("ActionCommand should contain 'br update', got: %s", sug.ActionCommand)
	}
	if !strings.Contains(sug.ActionCommand, "--add-label") {
		t.Errorf("ActionCommand should contain '--add-label', got: %s", sug.ActionCommand)
	}

	// Verify metadata
	if _, ok := sug.Metadata["suggested_label"]; !ok {
		t.Error("Metadata missing 'suggested_label'")
	}
	if _, ok := sug.Metadata["matched_keywords"]; !ok {
		t.Error("Metadata missing 'matched_keywords'")
	}
}

// ============================================================================
// learnLabelMappings Tests
// ============================================================================

func TestLearnLabelMappings_BasicLearning(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Payment processing", Description: "", Labels: []string{"billing"}},
		{ID: "2", Title: "Payment gateway", Description: "", Labels: []string{"billing"}},
		{ID: "3", Title: "Unrelated issue", Description: "", Labels: []string{"other"}},
	}

	mappings := learnLabelMappings(issues)

	// "payment" should map to "billing"
	if labelCounts, ok := mappings["payment"]; ok {
		if count, ok := labelCounts["billing"]; !ok || count != 2 {
			t.Errorf("expected payment->billing count=2, got %v", labelCounts)
		}
	} else {
		t.Error("expected 'payment' keyword in mappings")
	}
}

func TestLearnLabelMappings_SkipsUnlabeled(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Payment issue", Labels: nil},
		{ID: "2", Title: "Payment problem", Labels: []string{}},
		{ID: "3", Title: "Payment gateway", Labels: []string{"billing"}},
	}

	mappings := learnLabelMappings(issues)

	// Only the labeled issue should contribute
	if labelCounts, ok := mappings["payment"]; ok {
		if count := labelCounts["billing"]; count != 1 {
			t.Errorf("expected payment->billing count=1 (only from labeled issue), got %d", count)
		}
	}
}

func TestLearnLabelMappings_CaseInsensitive(t *testing.T) {
	issues := []model.Issue{
		{ID: "1", Title: "Payment issue", Labels: []string{"BILLING"}},
		{ID: "2", Title: "payment problem", Labels: []string{"Billing"}},
	}

	mappings := learnLabelMappings(issues)

	// Both should map to lowercase "billing"
	if labelCounts, ok := mappings["payment"]; ok {
		if count := labelCounts["billing"]; count != 2 {
			t.Errorf("expected case-insensitive billing count=2, got %v", labelCounts)
		}
	}
}

func TestLearnLabelMappings_Empty(t *testing.T) {
	mappings := learnLabelMappings(nil)
	if len(mappings) != 0 {
		t.Errorf("expected empty mappings for nil, got %v", mappings)
	}

	mappings = learnLabelMappings([]model.Issue{})
	if len(mappings) != 0 {
		t.Errorf("expected empty mappings for empty slice, got %v", mappings)
	}
}

// ============================================================================
// uniqueStrings Tests
// ============================================================================

func TestUniqueStrings_Basic(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "no duplicates",
			input: []string{"a", "b", "c"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "with duplicates",
			input: []string{"a", "b", "a", "c", "b"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "all same",
			input: []string{"x", "x", "x"},
			want:  []string{"x"},
		},
		{
			name:  "empty",
			input: []string{},
			want:  []string{},
		},
		{
			name:  "nil",
			input: nil,
			want:  []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := uniqueStrings(tc.input)
			if len(got) != len(tc.want) {
				t.Errorf("length: got %d, want %d", len(got), len(tc.want))
				return
			}
			for i, v := range tc.want {
				if got[i] != v {
					t.Errorf("index %d: got %q, want %q", i, got[i], v)
				}
			}
		})
	}
}

func TestUniqueStrings_PreservesOrder(t *testing.T) {
	input := []string{"z", "a", "m", "a", "z"}
	got := uniqueStrings(input)
	want := []string{"z", "a", "m"}

	if len(got) != len(want) {
		t.Fatalf("length: got %d, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("order not preserved at %d: got %q, want %q", i, got[i], v)
		}
	}
}

// ============================================================================
// sortLabelMatchesByConfidence Tests
// ============================================================================

func TestSortLabelMatchesByConfidence_Basic(t *testing.T) {
	matches := []LabelMatch{
		{Label: "low", Confidence: 0.3},
		{Label: "high", Confidence: 0.9},
		{Label: "med", Confidence: 0.6},
	}

	sortLabelMatchesByConfidence(matches)

	if matches[0].Label != "high" || matches[0].Confidence != 0.9 {
		t.Errorf("first should be high (0.9), got %v", matches[0])
	}
	if matches[1].Label != "med" || matches[1].Confidence != 0.6 {
		t.Errorf("second should be med (0.6), got %v", matches[1])
	}
	if matches[2].Label != "low" || matches[2].Confidence != 0.3 {
		t.Errorf("third should be low (0.3), got %v", matches[2])
	}
}

func TestSortLabelMatchesByConfidence_EqualValues(t *testing.T) {
	matches := []LabelMatch{
		{Label: "a", Confidence: 0.5},
		{Label: "b", Confidence: 0.5},
		{Label: "c", Confidence: 0.5},
	}

	sortLabelMatchesByConfidence(matches)

	// All equal, should maintain some stable order (depends on sort.Slice stability)
	if len(matches) != 3 {
		t.Errorf("unexpected length after sort: %d", len(matches))
	}
}

func TestSortLabelMatchesByConfidence_Empty(t *testing.T) {
	var matches []LabelMatch
	sortLabelMatchesByConfidence(matches) // Should not panic

	matches = []LabelMatch{}
	sortLabelMatchesByConfidence(matches) // Should not panic
}

func TestSortLabelMatchesByConfidence_Single(t *testing.T) {
	matches := []LabelMatch{{Label: "only", Confidence: 0.5}}
	sortLabelMatchesByConfidence(matches)

	if len(matches) != 1 || matches[0].Label != "only" {
		t.Errorf("single element should remain unchanged")
	}
}

// ============================================================================
// LabelSuggestionDetector Tests
// ============================================================================

func TestNewLabelSuggestionDetector(t *testing.T) {
	config := LabelSuggestionConfig{
		MinConfidence:          0.7,
		MaxSuggestionsPerIssue: 5,
		MaxTotalSuggestions:    50,
		LearnFromExisting:      false,
		BuiltinMappings:        true,
	}

	detector := NewLabelSuggestionDetector(config)

	if detector == nil {
		t.Fatal("detector is nil")
	}
	if detector.config.MinConfidence != 0.7 {
		t.Errorf("config not stored correctly")
	}
}

func TestLabelSuggestionDetector_Detect(t *testing.T) {
	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	detector := NewLabelSuggestionDetector(config)

	issues := []model.Issue{
		{ID: "BUG-1", Title: "Fix critical bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	suggestions := detector.Detect(issues)

	// Should produce same results as direct SuggestLabels call
	directSuggestions := SuggestLabels(issues, config)

	if len(suggestions) != len(directSuggestions) {
		t.Errorf("Detect() returned %d suggestions, SuggestLabels() returned %d",
			len(suggestions), len(directSuggestions))
	}
}

func TestLabelSuggestionDetector_DetectEmpty(t *testing.T) {
	detector := NewLabelSuggestionDetector(DefaultLabelSuggestionConfig())

	suggestions := detector.Detect(nil)
	if suggestions != nil {
		t.Errorf("expected nil for empty input, got %v", suggestions)
	}
}

// ============================================================================
// Integration Tests
// ============================================================================

func TestSuggestLabels_RealisticScenario(t *testing.T) {
	// Simulate a real project with various issues
	issues := []model.Issue{
		// Historical labeled issues (training data)
		{ID: "OLD-1", Title: "Fix login timeout bug", Status: model.StatusClosed, Labels: []string{"bug", "auth"}},
		{ID: "OLD-2", Title: "Add password reset feature", Status: model.StatusClosed, Labels: []string{"feature", "auth"}},
		{ID: "OLD-3", Title: "Improve API response time", Status: model.StatusClosed, Labels: []string{"performance", "api"}},
		{ID: "OLD-4", Title: "Update API documentation", Status: model.StatusClosed, Labels: []string{"documentation", "api"}},

		// Current labeled issues
		{ID: "CUR-1", Title: "Refactor database queries", Status: model.StatusOpen, Labels: []string{"refactoring", "database"}},
		{ID: "CUR-2", Title: "Add caching layer", Status: model.StatusOpen, Labels: []string{"performance", "cache"}},

		// Issues needing label suggestions
		{ID: "NEW-1", Title: "Fix authentication error in login", Status: model.StatusOpen},
		{ID: "NEW-2", Title: "Improve API endpoint performance", Status: model.StatusOpen},
		{ID: "NEW-3", Title: "Add database migration script", Status: model.StatusOpen},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.25

	suggestions := SuggestLabels(issues, config)

	// Collect suggestions by issue
	suggestionsByIssue := make(map[string][]string)
	for _, sug := range suggestions {
		if label, ok := sug.Metadata["suggested_label"].(string); ok {
			suggestionsByIssue[sug.TargetBead] = append(suggestionsByIssue[sug.TargetBead], label)
		}
	}

	// NEW-1 should get auth/bug suggestions (login, error, authentication keywords)
	if labels := suggestionsByIssue["NEW-1"]; len(labels) == 0 {
		t.Error("NEW-1 should have label suggestions based on auth/bug keywords")
	}

	// NEW-2 should get api/performance suggestions
	if labels := suggestionsByIssue["NEW-2"]; len(labels) == 0 {
		t.Error("NEW-2 should have label suggestions based on api/performance keywords")
	}

	// NEW-3 should get database suggestions
	if labels := suggestionsByIssue["NEW-3"]; len(labels) == 0 {
		t.Error("NEW-3 should have label suggestions based on database keyword")
	}
}

func TestSuggestLabels_NoFalsePositives(t *testing.T) {
	// Issue with unrelated content shouldn't get random suggestions
	issues := []model.Issue{
		{ID: "RANDOM-1", Title: "Update README formatting", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.5

	suggestions := SuggestLabels(issues, config)

	// RANDOM-1 shouldn't get "bug" suggestion just because label exists
	for _, sug := range suggestions {
		if sug.TargetBead == "RANDOM-1" {
			if label, ok := sug.Metadata["suggested_label"].(string); ok {
				if label == "bug" {
					t.Error("shouldn't suggest 'bug' for unrelated issue")
				}
			}
		}
	}
}

func TestSuggestLabels_MultipleKeywordMatches(t *testing.T) {
	// Issue matching multiple keywords for same label should have higher confidence
	issues := []model.Issue{
		{ID: "MULTI-1", Title: "Fix critical bug error crash", Status: model.StatusOpen}, // bug, error, crash all map to "bug"
		{ID: "SINGLE-1", Title: "Fix minor issue", Status: model.StatusOpen},             // Only "fix" maps to bug
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1
	config.LearnFromExisting = false

	suggestions := SuggestLabels(issues, config)

	// Find confidence for MULTI-1 and SINGLE-1 bug suggestions
	var multiConf, singleConf float64
	for _, sug := range suggestions {
		if label, ok := sug.Metadata["suggested_label"].(string); ok && label == "bug" {
			if sug.TargetBead == "MULTI-1" {
				multiConf = sug.Confidence
			}
			if sug.TargetBead == "SINGLE-1" {
				singleConf = sug.Confidence
			}
		}
	}

	// Multiple keyword matches should yield higher confidence (or at least equal)
	if multiConf < singleConf {
		t.Errorf("multiple matches should have >= confidence: multi=%v, single=%v", multiConf, singleConf)
	}
}

// ============================================================================
// Edge Cases
// ============================================================================

func TestSuggestLabels_VeryLongLabels(t *testing.T) {
	longLabel := "this-is-a-very-long-label-name-that-might-cause-issues-in-some-systems"
	issues := []model.Issue{
		{ID: "LONG-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", longLabel}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	// Should still work with long labels
	if len(suggestions) == 0 {
		t.Error("should produce suggestions even with long labels in project")
	}
}

func TestSuggestLabels_SpecialCharactersInLabels(t *testing.T) {
	issues := []model.Issue{
		{ID: "SPEC-1", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "L1", Title: "x", Status: model.StatusOpen, Labels: []string{"bug", "bug/critical", "v2.0"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	// Should handle special characters in labels
	if len(suggestions) == 0 {
		t.Error("should produce suggestions with special character labels")
	}
}

func TestSuggestLabels_OnlyClosedIssues(t *testing.T) {
	issues := []model.Issue{
		{ID: "CLOSED-1", Title: "Fix bug", Status: model.StatusClosed, Labels: []string{"bug"}},
		{ID: "CLOSED-2", Title: "Fix another bug", Status: model.StatusClosed},
	}

	config := DefaultLabelSuggestionConfig()
	suggestions := SuggestLabels(issues, config)

	// No open issues to suggest labels for
	if len(suggestions) > 0 {
		t.Errorf("expected no suggestions when all issues closed, got %d", len(suggestions))
	}
}

func TestSuggestLabels_AllLabeled(t *testing.T) {
	issues := []model.Issue{
		{ID: "L1", Title: "Fix bug", Status: model.StatusOpen, Labels: []string{"bug"}},
		{ID: "L2", Title: "Fix another bug", Status: model.StatusOpen, Labels: []string{"bug"}},
	}

	config := DefaultLabelSuggestionConfig()
	config.MinConfidence = 0.1

	suggestions := SuggestLabels(issues, config)

	// Both issues already have the matching label, no new suggestions needed
	for _, sug := range suggestions {
		if label, ok := sug.Metadata["suggested_label"].(string); ok {
			issue := findIssue(issues, sug.TargetBead)
			if issue != nil && containsLabel(issue.Labels, label) {
				t.Errorf("suggested %q for %s which already has it", label, sug.TargetBead)
			}
		}
	}
}

// ============================================================================
// Helper Functions
// ============================================================================

func findIssue(issues []model.Issue, id string) *model.Issue {
	for i := range issues {
		if issues[i].ID == id {
			return &issues[i]
		}
	}
	return nil
}

func containsLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}
	return false
}

// ============================================================================
// LabelMatch Tests
// ============================================================================

func TestLabelMatch_Fields(t *testing.T) {
	match := LabelMatch{
		IssueID:      "TEST-1",
		Label:        "bug",
		Confidence:   0.75,
		Reason:       "keywords: fix, error",
		MatchedWords: []string{"fix", "error"},
	}

	if match.IssueID != "TEST-1" {
		t.Errorf("IssueID: got %q, want %q", match.IssueID, "TEST-1")
	}
	if match.Label != "bug" {
		t.Errorf("Label: got %q, want %q", match.Label, "bug")
	}
	if match.Confidence != 0.75 {
		t.Errorf("Confidence: got %v, want %v", match.Confidence, 0.75)
	}
	if match.Reason != "keywords: fix, error" {
		t.Errorf("Reason: got %q, want %q", match.Reason, "keywords: fix, error")
	}
	if len(match.MatchedWords) != 2 {
		t.Errorf("MatchedWords length: got %d, want 2", len(match.MatchedWords))
	}
}

// ============================================================================
// Benchmark Tests
// ============================================================================

func BenchmarkSuggestLabels_SmallSet(b *testing.B) {
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

	config := DefaultLabelSuggestionConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SuggestLabels(issues, config)
	}
}

func BenchmarkSuggestLabels_LargeSet(b *testing.B) {
	issues := make([]model.Issue, 500)
	for i := range issues {
		issues[i] = model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Title:  "Fix bug in auth login api database",
			Status: model.StatusOpen,
		}
	}
	// Add labeled issues
	labels := []string{"bug", "auth", "api", "database", "performance"}
	for i, label := range labels {
		issues = append(issues, model.Issue{
			ID: "L" + string(rune('0'+i)), Title: "x", Status: model.StatusOpen, Labels: []string{label},
		})
	}

	config := DefaultLabelSuggestionConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SuggestLabels(issues, config)
	}
}

func BenchmarkLearnLabelMappings(b *testing.B) {
	issues := make([]model.Issue, 200)
	for i := range issues {
		issues[i] = model.Issue{
			ID:     "ISSUE-" + string(rune('A'+i%26)),
			Title:  "Fix payment processing issue",
			Labels: []string{"billing", "payment"},
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		learnLabelMappings(issues)
	}
}

func BenchmarkSortLabelMatchesByConfidence(b *testing.B) {
	matches := make([]LabelMatch, 100)
	for i := range matches {
		matches[i] = LabelMatch{
			Label:      "label-" + string(rune('a'+i%26)),
			Confidence: float64(i%100) / 100.0,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Make a copy to avoid sorting already sorted
		matchesCopy := make([]LabelMatch, len(matches))
		copy(matchesCopy, matches)
		sortLabelMatchesByConfidence(matchesCopy)
	}
}

// Ensure sort is imported and used
var _ = sort.Slice
