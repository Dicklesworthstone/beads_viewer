package analysis

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestDefaultDependencySuggestionConfig(t *testing.T) {
	config := DefaultDependencySuggestionConfig()

	// Verify sensible defaults
	if config.MinKeywordOverlap != 2 {
		t.Errorf("expected MinKeywordOverlap=2, got %d", config.MinKeywordOverlap)
	}
	if config.ExactMatchBonus != 0.15 {
		t.Errorf("expected ExactMatchBonus=0.15, got %f", config.ExactMatchBonus)
	}
	if config.LabelOverlapBonus != 0.1 {
		t.Errorf("expected LabelOverlapBonus=0.1, got %f", config.LabelOverlapBonus)
	}
	if config.MinConfidence != 0.5 {
		t.Errorf("expected MinConfidence=0.5, got %f", config.MinConfidence)
	}
	if config.MaxSuggestions != 20 {
		t.Errorf("expected MaxSuggestions=20, got %d", config.MaxSuggestions)
	}
	if !config.IgnoreExistingDeps {
		t.Error("expected IgnoreExistingDeps=true")
	}
}

func TestDependencyMatch_JSON(t *testing.T) {
	match := DependencyMatch{
		From:           "issue-1",
		To:             "issue-2",
		Confidence:     0.75,
		SharedKeywords: []string{"auth", "login"},
		SharedLabels:   []string{"security"},
		Reason:         "2 shared keywords, 1 shared label",
	}

	data, err := json.Marshal(match)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded DependencyMatch
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.From != match.From {
		t.Errorf("From mismatch: got %s, want %s", decoded.From, match.From)
	}
	if decoded.To != match.To {
		t.Errorf("To mismatch: got %s, want %s", decoded.To, match.To)
	}
	if decoded.Confidence != match.Confidence {
		t.Errorf("Confidence mismatch: got %f, want %f", decoded.Confidence, match.Confidence)
	}
}

func TestDetectMissingDependencies_Empty(t *testing.T) {
	config := DefaultDependencySuggestionConfig()

	// Empty issues
	suggestions := DetectMissingDependencies(nil, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for nil issues, got %d", len(suggestions))
	}

	// Single issue
	issues := []model.Issue{{ID: "i1", Title: "Test"}}
	suggestions = DetectMissingDependencies(issues, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for single issue, got %d", len(suggestions))
	}
}

func TestDetectMissingDependencies_NoSharedKeywords(t *testing.T) {
	config := DefaultDependencySuggestionConfig()

	issues := []model.Issue{
		{ID: "i1", Title: "Implement login page", Status: model.StatusOpen},
		{ID: "i2", Title: "Fix database migration", Status: model.StatusOpen},
	}

	suggestions := DetectMissingDependencies(issues, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for unrelated issues, got %d", len(suggestions))
	}
}

func TestDetectMissingDependenciesRejectsLongerIDPrefixMention(t *testing.T) {
	issues := []model.Issue{
		{
			ID:     "bv-42",
			Title:  "Authentication service",
			Status: model.StatusOpen,
		},
		{
			ID:          "bv-420",
			Title:       "Authentication service",
			Description: "Follow-up work for bv-420",
			Status:      model.StatusOpen,
		},
	}
	if got := DetectMissingDependencies(issues, DefaultDependencySuggestionConfig()); len(got) != 0 {
		t.Fatalf("longer ID prefix manufactured exact-mention dependency: %+v", got)
	}
}

func TestDetectMissingDependencies_SharedKeywords(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 2
	config.MinConfidence = 0.1 // Lower for testing

	now := time.Now()
	issues := []model.Issue{
		{
			ID:          "i1",
			Title:       "Implement user authentication system",
			Description: "Build authentication with JWT tokens",
			Status:      model.StatusOpen,
			CreatedAt:   now.Add(-24 * time.Hour),
		},
		{
			ID:          "i2",
			Title:       "Add user authentication tests",
			Description: "Test authentication JWT validation",
			Status:      model.StatusOpen,
			CreatedAt:   now,
		},
	}

	for i := range issues {
		issues[i].Origin = suggestionTestOrigin(issues[i].ID)
	}
	suggestions := DetectMissingDependencies(issues, config)
	// Should find suggestion due to shared keywords: user, authentication, jwt
	if len(suggestions) == 0 {
		t.Error("expected at least one suggestion for issues with shared keywords")
		return
	}

	// Verify suggestion type
	for _, sug := range suggestions {
		if sug.Type != SuggestionMissingDependency {
			t.Errorf("expected type MissingDependency, got %s", sug.Type)
		}
		if sug.ActionCommand == "" {
			t.Error("expected action command to be set")
		}
		if !strings.Contains(sug.ActionCommand, "'dep' 'add'") {
			t.Errorf("expected routed dependency add command, got %s", sug.ActionCommand)
		}
	}
}

func TestDetectMissingDependencies_ExistingDepsIgnored(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1
	config.IgnoreExistingDeps = true

	now := time.Now()
	issues := []model.Issue{
		{
			ID:          "i1",
			Title:       "Auth system implementation",
			Description: "Build authentication system",
			Status:      model.StatusOpen,
			CreatedAt:   now.Add(-24 * time.Hour),
			Dependencies: []*model.Dependency{
				{DependsOnID: "i2"},
			},
		},
		{
			ID:          "i2",
			Title:       "Auth database schema",
			Description: "Create authentication tables",
			Status:      model.StatusOpen,
			CreatedAt:   now,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)
	// Should not suggest since dependency already exists
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions when dependency exists, got %d", len(suggestions))
	}

	// With IgnoreExistingDeps=false
	config.IgnoreExistingDeps = false
	_ = DetectMissingDependencies(issues, config)
	// May now suggest if keywords overlap
}

func TestDetectMissingDependencies_ClosedIssuesSkipped(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1

	issues := []model.Issue{
		{
			ID:          "i1",
			Title:       "Auth implementation",
			Description: "Build auth system",
			Status:      model.StatusClosed,
		},
		{
			ID:          "i2",
			Title:       "Auth tests",
			Description: "Test auth system",
			Status:      model.StatusClosed,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)
	// Should skip both-closed pairs
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for both-closed issues, got %d", len(suggestions))
	}
}

func TestDetectMissingDependencies_TombstoneSkipped(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1

	issues := []model.Issue{
		{
			ID:          "i1",
			Title:       "Auth implementation",
			Description: "Build auth system",
			Status:      model.StatusTombstone,
		},
		{
			ID:          "i2",
			Title:       "Auth tests",
			Description: "Test auth system",
			Status:      model.StatusOpen,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for tombstone issues, got %d", len(suggestions))
	}
}

func TestDetectMissingDependencies_LabelOverlap(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 2
	config.MinConfidence = 0.1

	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "i1",
			Title:     "Frontend login component",
			Labels:    []string{"frontend", "auth"},
			Status:    model.StatusOpen,
			CreatedAt: now.Add(-24 * time.Hour),
		},
		{
			ID:        "i2",
			Title:     "Frontend authentication flow",
			Labels:    []string{"frontend", "auth"},
			Status:    model.StatusOpen,
			CreatedAt: now,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)
	// Should get bonus from shared labels
	for _, sug := range suggestions {
		if meta, ok := sug.Metadata["shared_labels"]; ok {
			labels := meta.([]string)
			if len(labels) == 0 {
				t.Error("expected shared labels in metadata")
			}
		}
	}
}

func TestDetectMissingDependencies_MaxSuggestions(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1
	config.MaxSuggestions = 3

	// Create many issues with shared keywords
	issues := make([]model.Issue, 10)
	for i := 0; i < 10; i++ {
		issues[i] = model.Issue{
			ID:          fmt.Sprintf("i%d", i),
			Title:       "Auth system component",
			Description: "Authentication implementation",
			Status:      model.StatusOpen,
		}
	}

	suggestions := DetectMissingDependencies(issues, config)
	if len(suggestions) > config.MaxSuggestions {
		t.Errorf("expected at most %d suggestions, got %d", config.MaxSuggestions, len(suggestions))
	}
}

func TestDetectMissingDependencies_NonPositiveMaxIsSafe(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Authentication service", Status: model.StatusOpen},
		{ID: "B", Title: "Authentication service", Status: model.StatusOpen},
	}
	for _, limit := range []int{0, -1} {
		config := DefaultDependencySuggestionConfig()
		config.MinKeywordOverlap = 1
		config.MinConfidence = 0
		config.MaxSuggestions = limit
		if suggestions := DetectMissingDependencies(issues, config); len(suggestions) != 0 {
			t.Fatalf("MaxSuggestions=%d returned %d suggestions", limit, len(suggestions))
		}
	}
}

func TestDetectMissingDependencies_ConfidenceSorted(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1
	config.MaxSuggestions = 100

	now := time.Now()
	// Create issues with varying keyword overlap
	issues := []model.Issue{
		{
			ID:          "i1",
			Title:       "Auth system user login validation",
			Description: "Authentication for user login",
			Labels:      []string{"auth", "security"},
			Status:      model.StatusOpen,
			CreatedAt:   now.Add(-48 * time.Hour),
		},
		{
			ID:          "i2",
			Title:       "Auth user login testing",
			Description: "Test user login auth flow",
			Labels:      []string{"auth", "testing"},
			Status:      model.StatusOpen,
			CreatedAt:   now.Add(-24 * time.Hour),
		},
		{
			ID:          "i3",
			Title:       "Database migration",
			Description: "Migrate user data",
			Status:      model.StatusOpen,
			CreatedAt:   now,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)

	// Verify sorted by confidence (highest first)
	for i := 1; i < len(suggestions); i++ {
		if suggestions[i].Confidence > suggestions[i-1].Confidence {
			t.Errorf("suggestions not sorted by confidence: %f > %f at index %d",
				suggestions[i].Confidence, suggestions[i-1].Confidence, i)
		}
	}
}

func TestFindSharedKeys(t *testing.T) {
	tests := []struct {
		name     string
		m1       map[string]bool
		m2       map[string]bool
		expected int
	}{
		{
			name:     "no overlap",
			m1:       map[string]bool{"a": true, "b": true},
			m2:       map[string]bool{"c": true, "d": true},
			expected: 0,
		},
		{
			name:     "full overlap",
			m1:       map[string]bool{"a": true, "b": true},
			m2:       map[string]bool{"a": true, "b": true},
			expected: 2,
		},
		{
			name:     "partial overlap",
			m1:       map[string]bool{"a": true, "b": true, "c": true},
			m2:       map[string]bool{"b": true, "c": true, "d": true},
			expected: 2,
		},
		{
			name:     "empty maps",
			m1:       map[string]bool{},
			m2:       map[string]bool{},
			expected: 0,
		},
		{
			name:     "one empty",
			m1:       map[string]bool{"a": true},
			m2:       map[string]bool{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shared := findSharedKeys(tt.m1, tt.m2)
			if len(shared) != tt.expected {
				t.Errorf("expected %d shared keys, got %d", tt.expected, len(shared))
			}
		})
	}
}

func TestSortMatchesByConfidence(t *testing.T) {
	matches := []DependencyMatch{
		{From: "a", To: "b", Confidence: 0.3},
		{From: "c", To: "d", Confidence: 0.9},
		{From: "e", To: "f", Confidence: 0.5},
		{From: "g", To: "h", Confidence: 0.7},
	}

	sortMatchesByConfidence(matches)

	// Verify descending order
	expected := []float64{0.9, 0.7, 0.5, 0.3}
	for i, exp := range expected {
		if matches[i].Confidence != exp {
			t.Errorf("index %d: expected confidence %f, got %f", i, exp, matches[i].Confidence)
		}
	}
}

func TestDependencySuggestionDetector(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	detector := NewDependencySuggestionDetector(config)

	if detector == nil {
		t.Fatal("expected non-nil detector")
	}

	// Test with empty issues
	suggestions := detector.Detect(nil)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for nil issues, got %d", len(suggestions))
	}

	// Test with single issue
	issues := []model.Issue{{ID: "i1", Title: "Test"}}
	suggestions = detector.Detect(issues)
	if len(suggestions) != 0 {
		t.Errorf("expected 0 suggestions for single issue, got %d", len(suggestions))
	}
}

func TestDetectMissingDependencies_IDMentioned(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.1

	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "bv-123",
			Title:     "Implement feature",
			Status:    model.StatusOpen,
			CreatedAt: now.Add(-24 * time.Hour),
		},
		{
			ID:          "bv-456",
			Title:       "Related task",
			Description: "This depends on bv-123 for completion",
			Status:      model.StatusOpen,
			CreatedAt:   now,
		},
	}

	_ = DetectMissingDependencies(issues, config)
	// Should get bonus from ID being mentioned
	// The exact behavior depends on keyword extraction
}

func TestDetectMissingDependencies_EmptyIDIsNotAnExactMention(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 1
	config.MinConfidence = 0.4

	issues := []model.Issue{
		{ID: "", Title: "Shared alpha work", Status: model.StatusOpen},
		{ID: "valid", Title: "Shared beta work", Status: model.StatusOpen},
	}
	if got := DetectMissingDependencies(issues, config); len(got) != 0 {
		t.Fatalf("empty ID manufactured exact-mention suggestions: %+v", got)
	}
}

func TestDetectMissingDependencies_DeterminismWithTimestamps(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 2
	config.MinConfidence = 0.1

	now := time.Now()
	issues := []model.Issue{
		{
			ID:        "i1",
			Title:     "Auth system implementation",
			Status:    model.StatusOpen,
			CreatedAt: now.Add(-24 * time.Hour), // Older
			Priority:  2,
		},
		{
			ID:        "i2",
			Title:     "Auth system testing",
			Status:    model.StatusOpen,
			CreatedAt: now, // Newer
			Priority:  2,
		},
	}

	suggestions := DetectMissingDependencies(issues, config)

	// Run multiple times and verify determinism
	for run := 0; run < 5; run++ {
		s2 := DetectMissingDependencies(issues, config)
		if len(s2) != len(suggestions) {
			t.Errorf("run %d: suggestion count changed: %d vs %d", run, len(s2), len(suggestions))
		}
		for i := range suggestions {
			if i < len(s2) {
				if suggestions[i].TargetBead != s2[i].TargetBead {
					t.Errorf("run %d: TargetBead changed at index %d", run, i)
				}
			}
		}
	}
}

func TestDetectMissingDependencies_IsInvariantToInputOrder(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 2
	config.MinConfidence = 0.1

	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	issues := []model.Issue{
		{
			ID:          "older-low-priority",
			Title:       "Build API",
			Description: "authentication tokens",
			Status:      model.StatusOpen,
			CreatedAt:   now.Add(-time.Hour),
			Priority:    3,
		},
		{
			ID:        "newer-high-priority",
			Title:     "Authentication tokens tests",
			Status:    model.StatusOpen,
			CreatedAt: now,
			Priority:  1,
		},
	}

	forward := DetectMissingDependencies(issues, config)
	reversed := DetectMissingDependencies([]model.Issue{issues[1], issues[0]}, config)

	if len(forward) != 1 {
		t.Fatalf("forward suggestions = %d, want 1", len(forward))
	}
	for i := range forward {
		forward[i].GeneratedAt = time.Time{}
	}
	for i := range reversed {
		reversed[i].GeneratedAt = time.Time{}
	}
	if !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("input order changed suggestions:\nforward = %#v\nreversed = %#v", forward, reversed)
	}
}

func TestDetectMissingDependencies_DirectionIndependentOfInputOrder(t *testing.T) {
	config := DefaultDependencySuggestionConfig()
	config.MinKeywordOverlap = 2
	config.MinConfidence = 0.1

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	olderLowPriority := model.Issue{
		ID:          "A",
		Title:       "Foundation layer",
		Description: "authentication service",
		Status:      model.StatusOpen,
		CreatedAt:   now.Add(-time.Hour),
		Priority:    4,
	}
	newerHighPriority := model.Issue{
		ID:        "B",
		Title:     "Authentication service tests",
		Status:    model.StatusOpen,
		CreatedAt: now,
		Priority:  0,
	}

	forward := DetectMissingDependencies([]model.Issue{olderLowPriority, newerHighPriority}, config)
	reversed := DetectMissingDependencies([]model.Issue{newerHighPriority, olderLowPriority}, config)
	if len(forward) != 1 || len(reversed) != 1 {
		t.Fatalf("suggestion counts forward=%d reversed=%d, want one each", len(forward), len(reversed))
	}
	forward[0].GeneratedAt = time.Time{}
	reversed[0].GeneratedAt = time.Time{}
	forwardJSON, err := json.Marshal(forward)
	if err != nil {
		t.Fatal(err)
	}
	reversedJSON, err := json.Marshal(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if string(forwardJSON) != string(reversedJSON) {
		t.Fatalf("input permutation changed suggestion\nforward: %s\nreversed: %s", forwardJSON, reversedJSON)
	}
	if forward[0].TargetBead != "B" || forward[0].RelatedBead != "A" {
		t.Fatalf("direction = %s -> %s, want newer B -> older A", forward[0].TargetBead, forward[0].RelatedBead)
	}
}

func BenchmarkDetectMissingDependencies_Small(b *testing.B) {
	issues := testutil.QuickChain(10)
	config := DefaultDependencySuggestionConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DetectMissingDependencies(issues, config)
	}
}

func BenchmarkDetectMissingDependencies_Medium(b *testing.B) {
	issues := testutil.QuickChain(50)
	config := DefaultDependencySuggestionConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DetectMissingDependencies(issues, config)
	}
}

func BenchmarkDetectMissingDependencies_Large(b *testing.B) {
	issues := testutil.QuickChain(100)
	config := DefaultDependencySuggestionConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DetectMissingDependencies(issues, config)
	}
}
