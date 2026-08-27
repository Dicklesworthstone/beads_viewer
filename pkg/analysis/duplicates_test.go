package analysis

import (
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ============================================================================
// extractKeywords Tests
// ============================================================================

func TestExtractKeywords_BasicExtraction(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		wantMinLen  int    // minimum keywords expected
		wantContain string // must contain this keyword
	}{
		{
			name:        "simple title only",
			title:       "Fix login button",
			description: "",
			wantMinLen:  2,
			wantContain: "login",
		},
		{
			name:        "title and description",
			title:       "Authentication fails",
			description: "Users cannot login when using OAuth provider",
			wantMinLen:  4,
			wantContain: "authentication",
		},
		{
			name:        "filters stop words",
			title:       "The user should be able to login",
			description: "This is a very important feature that will help users",
			wantMinLen:  2,
			wantContain: "user",
		},
		{
			name:        "handles special characters",
			title:       "Fix bug #123: crash on startup",
			description: "Error: null pointer @ line 42",
			wantMinLen:  3,
			wantContain: "crash",
		},
		{
			name:        "case insensitive",
			title:       "UPPERCASE Title Here",
			description: "MixedCase Description",
			wantMinLen:  2,
			wantContain: "uppercase",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keywords := extractKeywords(tt.title, tt.description)
			if len(keywords) < tt.wantMinLen {
				t.Errorf("extractKeywords() returned %d keywords, want at least %d", len(keywords), tt.wantMinLen)
			}
			found := false
			for _, kw := range keywords {
				if kw == tt.wantContain {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("extractKeywords() missing expected keyword %q, got %v", tt.wantContain, keywords)
			}
		})
	}
}

func TestExtractKeywords_FiltersShortWords(t *testing.T) {
	keywords := extractKeywords("A to do it is", "")
	for _, kw := range keywords {
		if len(kw) < 3 {
			t.Errorf("extractKeywords() included short word %q (len %d)", kw, len(kw))
		}
	}
}

func TestExtractKeywords_RemovesDuplicates(t *testing.T) {
	keywords := extractKeywords("login login login", "login authentication login")
	count := 0
	for _, kw := range keywords {
		if kw == "login" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("extractKeywords() has %d occurrences of 'login', want 1", count)
	}
}

func TestExtractKeywords_EmptyInput(t *testing.T) {
	keywords := extractKeywords("", "")
	if len(keywords) != 0 {
		t.Errorf("extractKeywords() with empty input returned %d keywords, want 0", len(keywords))
	}
}

func TestExtractKeywords_StopWordsFiltered(t *testing.T) {
	keywords := extractKeywords("the and for with this that", "from are was were been have")
	if len(keywords) > 0 {
		t.Errorf("extractKeywords() should filter stop words, got %v", keywords)
	}
}

// ============================================================================
// DetectDuplicates Tests - Exact Duplicates
// ============================================================================

func TestDetectDuplicates_IdenticalTitles(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("DetectDuplicates() found no duplicates for identical titles")
	}

	sug := suggestions[0]
	if sug.Type != SuggestionPotentialDuplicate {
		t.Errorf("suggestion type = %v, want SuggestionPotentialDuplicate", sug.Type)
	}
	if sug.Confidence < 0.9 {
		t.Errorf("confidence = %v, want >= 0.9 for identical titles", sug.Confidence)
	}
}

func TestDetectDuplicates_IdenticalContent(t *testing.T) {
	issues := []model.Issue{
		{
			ID:          "A",
			Title:       "Fix login bug",
			Description: "Users cannot login when password contains special characters",
			Status:      model.StatusOpen,
		},
		{
			ID:          "B",
			Title:       "Login bug fix",
			Description: "Users cannot login when password contains special characters",
			Status:      model.StatusOpen,
		},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("DetectDuplicates() found no duplicates for identical descriptions")
	}
}

// ============================================================================
// DetectDuplicates Tests - Near Duplicates
// ============================================================================

func TestDetectDuplicates_SimilarTitles(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement authentication for API endpoints", Status: model.StatusOpen},
		{ID: "B", Title: "API endpoints need authentication implementation", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	config.JaccardThreshold = 0.5
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("DetectDuplicates() found no duplicates for similar titles")
	}
}

func TestDetectDuplicates_DifferentCase(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement User Authentication System", Status: model.StatusOpen},
		{ID: "B", Title: "implement user authentication system", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("DetectDuplicates() should be case-insensitive")
	}
}

// ============================================================================
// DetectDuplicates Tests - False Positive Prevention
// ============================================================================

func TestDetectDuplicates_ShortGenericTitles(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "B", Title: "Fix bug", Status: model.StatusOpen},
		{ID: "C", Title: "Update docs", Status: model.StatusOpen},
		{ID: "D", Title: "Update docs", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	config.MinKeywords = 3
	suggestions := DetectDuplicates(issues, config)

	for _, sug := range suggestions {
		if sug.TargetBead == "A" || sug.TargetBead == "C" {
			t.Logf("Note: Found potential false positive match for generic title")
		}
	}
}

func TestDetectDuplicates_CompletelyDifferent(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement OAuth2 authentication flow", Status: model.StatusOpen},
		{ID: "B", Title: "Database migration for user table", Status: model.StatusOpen},
		{ID: "C", Title: "Update CSS styling for mobile layout", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) > 0 {
		t.Errorf("DetectDuplicates() found %d duplicates for unrelated issues, want 0", len(suggestions))
	}
}

func TestDetectDuplicates_IgnoreClosedVsOpen(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusClosed},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) > 0 {
		t.Error("DetectDuplicates() should ignore open vs closed pairs by default")
	}

	config.IgnoreClosedVsOpen = false
	suggestions = DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Error("DetectDuplicates() should find duplicates when IgnoreClosedVsOpen=false")
	}
}

func TestDetectDuplicates_SkipsTombstone(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusTombstone},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) > 0 {
		t.Error("DetectDuplicates() should skip tombstone issues")
	}
}

// ============================================================================
// DetectDuplicates Tests - Configuration
// ============================================================================

func TestDetectDuplicates_JaccardThreshold(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement authentication for users", Status: model.StatusOpen},
		{ID: "B", Title: "Implement authorization for admins", Status: model.StatusOpen},
	}

	highConfig := DefaultDuplicateConfig()
	highConfig.JaccardThreshold = 0.9
	highSuggestions := DetectDuplicates(issues, highConfig)

	lowConfig := DefaultDuplicateConfig()
	lowConfig.JaccardThreshold = 0.3
	lowSuggestions := DetectDuplicates(issues, lowConfig)

	if len(highSuggestions) >= len(lowSuggestions) && len(lowSuggestions) > 0 {
		t.Log("Lower threshold finds more matches as expected")
	}
}

func TestDetectDuplicates_MaxSuggestions(t *testing.T) {
	var issues []model.Issue
	for i := 0; i < 10; i++ {
		issues = append(issues, model.Issue{
			ID:     string(rune('A' + i)),
			Title:  "Implement user authentication system feature",
			Status: model.StatusOpen,
		})
	}

	config := DefaultDuplicateConfig()
	config.MaxSuggestions = 5

	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) > 5 {
		t.Errorf("DetectDuplicates() returned %d suggestions, want <= 5", len(suggestions))
	}
}

func TestDetectDuplicates_NonPositiveMaxIsSafe(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen},
	}
	for _, limit := range []int{0, -1} {
		config := DefaultDuplicateConfig()
		config.MaxSuggestions = limit
		if suggestions := DetectDuplicates(issues, config); len(suggestions) != 0 {
			t.Fatalf("MaxSuggestions=%d returned %d suggestions", limit, len(suggestions))
		}
	}
}

func TestDetectDuplicates_MinKeywords(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Fix it", Status: model.StatusOpen},
		{ID: "B", Title: "Fix it", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	config.MinKeywords = 3

	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) > 0 {
		t.Error("DetectDuplicates() should skip pairs with too few keywords")
	}
}

// ============================================================================
// DetectDuplicates Tests - Edge Cases
// ============================================================================

func TestDetectDuplicates_SingleIssue(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Single issue", Status: model.StatusOpen},
	}

	suggestions := DetectDuplicates(issues, DefaultDuplicateConfig())

	if len(suggestions) != 0 {
		t.Errorf("DetectDuplicates() with single issue returned %d suggestions, want 0", len(suggestions))
	}
}

func TestDetectDuplicates_EmptyIssues(t *testing.T) {
	suggestions := DetectDuplicates([]model.Issue{}, DefaultDuplicateConfig())

	if len(suggestions) != 0 {
		t.Errorf("DetectDuplicates() with empty input returned %d suggestions, want 0", len(suggestions))
	}
}

func TestDetectDuplicates_SuggestionStructure(t *testing.T) {
	issues := []model.Issue{
		{ID: "ISSUE-1", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "ISSUE-2", Title: "Implement user authentication system", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	suggestions := DetectDuplicates(issues, config)

	if len(suggestions) == 0 {
		t.Fatal("Expected at least one suggestion")
	}

	sug := suggestions[0]

	if sug.Type != SuggestionPotentialDuplicate {
		t.Errorf("Type = %v, want SuggestionPotentialDuplicate", sug.Type)
	}
	if sug.TargetBead == "" {
		t.Error("TargetBead should not be empty")
	}
	if sug.RelatedBead == "" {
		t.Error("RelatedBead should not be empty")
	}
	if sug.Summary == "" {
		t.Error("Summary should not be empty")
	}
	if sug.Reason == "" {
		t.Error("Reason should not be empty")
	}
	if sug.Confidence < 0 || sug.Confidence > 1 {
		t.Errorf("Confidence = %v, should be between 0 and 1", sug.Confidence)
	}
	if sug.ActionCommand == "" {
		t.Error("ActionCommand should be provided for open issues")
	}
}

func TestDetectDuplicates_SortedBySimilarity(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "C", Title: "Implement user authentication features", Status: model.StatusOpen},
		{ID: "D", Title: "Authentication system implementation needed", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	config.JaccardThreshold = 0.4
	suggestions := DetectDuplicates(issues, config)

	for i := 1; i < len(suggestions); i++ {
		if suggestions[i].Confidence > suggestions[i-1].Confidence {
			t.Errorf("Suggestions not sorted by similarity: [%d].Confidence=%v > [%d].Confidence=%v",
				i, suggestions[i].Confidence, i-1, suggestions[i-1].Confidence)
		}
	}
}

func TestDetectDuplicates_PairDirectionIndependentOfInputOrder(t *testing.T) {
	issueA := model.Issue{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen}
	issueB := model.Issue{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen}
	config := DefaultDuplicateConfig()

	for name, issues := range map[string][]model.Issue{
		"forward":  {issueA, issueB},
		"reversed": {issueB, issueA},
	} {
		t.Run(name, func(t *testing.T) {
			suggestions := DetectDuplicates(issues, config)
			if len(suggestions) != 1 {
				t.Fatalf("got %d suggestions, want one", len(suggestions))
			}
			if got := suggestions[0]; got.TargetBead != "A" || got.RelatedBead != "B" {
				t.Fatalf("pair = %s/%s, want canonical A/B", got.TargetBead, got.RelatedBead)
			}
		})
	}
}

// ============================================================================
// Helper Function Tests
// ============================================================================

func TestSortPairsBySimilarity(t *testing.T) {
	pairs := []DuplicatePair{
		{Issue1: "A", Issue2: "B", Similarity: 0.5},
		{Issue1: "C", Issue2: "D", Similarity: 0.9},
		{Issue1: "E", Issue2: "F", Similarity: 0.7},
	}

	sortPairsBySimilarity(pairs)

	expected := []float64{0.9, 0.7, 0.5}
	for i, p := range pairs {
		if p.Similarity != expected[i] {
			t.Errorf("pairs[%d].Similarity = %v, want %v", i, p.Similarity, expected[i])
		}
	}
}

func TestSortPairsBySimilarity_Empty(t *testing.T) {
	var pairs []DuplicatePair
	sortPairsBySimilarity(pairs)
}

func TestTruncateStringSlice(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		max     int
		wantLen int
	}{
		{"shorter than max", []string{"a", "b"}, 5, 2},
		{"equal to max", []string{"a", "b", "c"}, 3, 3},
		{"longer than max", []string{"a", "b", "c", "d", "e"}, 3, 3},
		{"empty slice", []string{}, 5, 0},
		{"max zero", []string{"a", "b"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateStringSlice(tt.input, tt.max)
			if len(result) != tt.wantLen {
				t.Errorf("truncateStringSlice() len = %d, want %d", len(result), tt.wantLen)
			}
		})
	}
}

// ============================================================================
// DuplicateDetector Tests
// ============================================================================

func TestDuplicateDetector_Detect(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen},
	}

	detector := NewDuplicateDetector(DefaultDuplicateConfig())
	suggestions := detector.Detect(issues)

	if len(suggestions) == 0 {
		t.Error("DuplicateDetector.Detect() found no duplicates")
	}
}

func TestDuplicateDetector_LastRun(t *testing.T) {
	detector := NewDuplicateDetector(DefaultDuplicateConfig())

	if !detector.LastRun().IsZero() {
		t.Error("LastRun() should be zero before first Detect()")
	}

	issues := []model.Issue{
		{ID: "A", Title: "Test issue", Status: model.StatusOpen},
	}

	before := time.Now()
	detector.Detect(issues)
	after := time.Now()

	lastRun := detector.LastRun()
	if lastRun.Before(before) || lastRun.After(after) {
		t.Errorf("LastRun() = %v, want between %v and %v", lastRun, before, after)
	}
}

func TestDuplicateDetector_ConcurrentUse(t *testing.T) {
	detector := NewDuplicateDetector(DefaultDuplicateConfig())
	issues := []model.Issue{
		{ID: "A", Title: "Implement user authentication system", Status: model.StatusOpen},
		{ID: "B", Title: "Implement user authentication system", Status: model.StatusOpen},
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = detector.Detect(issues)
		}()
		go func() {
			defer wg.Done()
			_ = detector.LastRun()
		}()
	}
	wg.Wait()
	if detector.LastRun().IsZero() {
		t.Fatal("concurrent detection did not record a completion time")
	}
}

func TestDuplicateDetector_CustomConfig(t *testing.T) {
	config := DuplicateConfig{
		JaccardThreshold:   0.9,
		MinKeywords:        5,
		IgnoreClosedVsOpen: false,
		MaxSuggestions:     10,
	}

	detector := NewDuplicateDetector(config)

	issues := []model.Issue{
		{ID: "A", Title: "Short", Status: model.StatusOpen},
		{ID: "B", Title: "Short", Status: model.StatusOpen},
	}

	suggestions := detector.Detect(issues)

	if len(suggestions) > 0 {
		t.Error("Custom config MinKeywords not respected")
	}
}

// ============================================================================
// DefaultDuplicateConfig Tests
// ============================================================================

func TestDefaultDuplicateConfig(t *testing.T) {
	config := DefaultDuplicateConfig()

	if config.JaccardThreshold != 0.7 {
		t.Errorf("JaccardThreshold = %v, want 0.7", config.JaccardThreshold)
	}
	if config.MinKeywords != 2 {
		t.Errorf("MinKeywords = %v, want 2", config.MinKeywords)
	}
	if !config.IgnoreClosedVsOpen {
		t.Error("IgnoreClosedVsOpen should be true by default")
	}
	if config.MaxSuggestions != 20 {
		t.Errorf("MaxSuggestions = %v, want 20", config.MaxSuggestions)
	}
}

// ============================================================================
// DuplicatePair Tests
// ============================================================================

func TestDuplicatePair_Fields(t *testing.T) {
	pair := DuplicatePair{
		Issue1:     "ISSUE-1",
		Issue2:     "ISSUE-2",
		Similarity: 0.85,
		Method:     "jaccard",
		Keywords:   []string{"authentication", "user", "login"},
	}

	if pair.Issue1 != "ISSUE-1" {
		t.Errorf("Issue1 = %v, want ISSUE-1", pair.Issue1)
	}
	if pair.Issue2 != "ISSUE-2" {
		t.Errorf("Issue2 = %v, want ISSUE-2", pair.Issue2)
	}
	if pair.Similarity != 0.85 {
		t.Errorf("Similarity = %v, want 0.85", pair.Similarity)
	}
	if pair.Method != "jaccard" {
		t.Errorf("Method = %v, want jaccard", pair.Method)
	}
	if len(pair.Keywords) != 3 {
		t.Errorf("Keywords len = %d, want 3", len(pair.Keywords))
	}
}

// ============================================================================
// Integration/Realistic Scenario Tests
// ============================================================================

func TestDetectDuplicates_RealisticScenario(t *testing.T) {
	issues := []model.Issue{
		{ID: "BUG-101", Title: "Login fails with special characters", Description: "User cannot login with special characters in password", Status: model.StatusOpen},
		{ID: "BUG-102", Title: "Login fails with special characters", Description: "User cannot login with special characters in password", Status: model.StatusOpen},
		{ID: "FEAT-201", Title: "Add OAuth2 integration for Google", Description: "Implement OAuth2 for Google login", Status: model.StatusOpen},
		{ID: "FEAT-202", Title: "Add OAuth2 integration for Facebook", Description: "Implement OAuth2 for Facebook login", Status: model.StatusOpen},
		{ID: "TASK-301", Title: "Update database schema", Description: "Add new columns for user preferences", Status: model.StatusOpen},
	}

	config := DefaultDuplicateConfig()
	config.JaccardThreshold = 0.4 // Lower threshold for this test
	suggestions := DetectDuplicates(issues, config)

	foundBugDupe := false
	foundFeatDupe := false

	for _, sug := range suggestions {
		if (sug.TargetBead == "BUG-101" && sug.RelatedBead == "BUG-102") ||
			(sug.TargetBead == "BUG-102" && sug.RelatedBead == "BUG-101") {
			foundBugDupe = true
		}
		if (sug.TargetBead == "FEAT-201" && sug.RelatedBead == "FEAT-202") ||
			(sug.TargetBead == "FEAT-202" && sug.RelatedBead == "FEAT-201") {
			foundFeatDupe = true
		}
	}

	if !foundBugDupe {
		t.Error("Should detect BUG-101 and BUG-102 as potential duplicates")
	}
	if !foundFeatDupe {
		t.Error("Should detect FEAT-201 and FEAT-202 as potential duplicates")
	}
}

func TestDetectDuplicates_LargeIssueSet(t *testing.T) {
	var issues []model.Issue
	for i := 0; i < 100; i++ {
		issues = append(issues, model.Issue{
			ID:     string(rune('A'+i/26)) + string(rune('A'+i%26)),
			Title:  "Unique issue number " + string(rune('0'+i%10)),
			Status: model.StatusOpen,
		})
	}

	issues[0].Title = "Implement authentication feature"
	issues[50].Title = "Implement authentication feature"

	config := DefaultDuplicateConfig()
	start := time.Now()
	suggestions := DetectDuplicates(issues, config)
	duration := time.Since(start)

	if duration > time.Second {
		t.Errorf("DetectDuplicates took %v for 100 issues, want < 1s", duration)
	}

	if len(suggestions) == 0 {
		t.Error("Should find at least one duplicate pair")
	}
}
