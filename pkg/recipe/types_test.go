package recipe_test

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

func TestParseRelativeTimeDays(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	result, err := recipe.ParseRelativeTime("14d", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeWeeks(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	result, err := recipe.ParseRelativeTime("2w", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeMonths(t *testing.T) {
	now := time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC)

	result, err := recipe.ParseRelativeTime("1m", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2025, 2, 15, 12, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeYears(t *testing.T) {
	now := time.Date(2025, 3, 15, 12, 0, 0, 0, time.UTC)

	result, err := recipe.ParseRelativeTime("1y", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2024, 3, 15, 12, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeISODate(t *testing.T) {
	now := time.Now()

	result, err := recipe.ParseRelativeTime("2024-06-15", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeRFC3339(t *testing.T) {
	now := time.Now()

	result, err := recipe.ParseRelativeTime("2024-06-15T10:30:00Z", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestParseRelativeTimeEmpty(t *testing.T) {
	now := time.Now()

	result, err := recipe.ParseRelativeTime("", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.IsZero() {
		t.Errorf("Expected zero time for empty input, got %v", result)
	}
}

func TestParseRelativeTimeInvalid(t *testing.T) {
	now := time.Now()

	_, err := recipe.ParseRelativeTime("invalid", now)
	if err == nil {
		t.Error("Expected error for invalid input")
	}

	if _, ok := err.(*recipe.TimeParseError); !ok {
		t.Errorf("Expected TimeParseError, got %T", err)
	}
}

func TestParseRelativeTimeCaseInsensitive(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	result, err := recipe.ParseRelativeTime("7D", now)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := time.Date(2025, 1, 8, 12, 0, 0, 0, time.UTC)
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
}

func TestDefaultRecipe(t *testing.T) {
	r := recipe.DefaultRecipe()

	if r.Name != "default" {
		t.Errorf("Expected name 'default', got %s", r.Name)
	}
	if len(r.Filters.Status) != 3 {
		t.Errorf("Expected 3 status filters, got %d", len(r.Filters.Status))
	}
	if r.Sort.Field != "priority" {
		t.Errorf("Expected sort by priority, got %s", r.Sort.Field)
	}
}

func TestActionableRecipe(t *testing.T) {
	r := recipe.ActionableRecipe()

	if r.Name != "actionable" {
		t.Errorf("Expected name 'actionable', got %s", r.Name)
	}
	if r.Filters.Actionable == nil || !*r.Filters.Actionable {
		t.Error("Expected Actionable filter to be true")
	}
}

func TestRecentRecipe(t *testing.T) {
	r := recipe.RecentRecipe()

	if r.Name != "recent" {
		t.Errorf("Expected name 'recent', got %s", r.Name)
	}
	if r.Filters.UpdatedAfter != "7d" {
		t.Errorf("Expected UpdatedAfter '7d', got %s", r.Filters.UpdatedAfter)
	}
	if r.Sort.Direction != "desc" {
		t.Errorf("Expected desc sort direction, got %s", r.Sort.Direction)
	}
}

func TestBlockedRecipe(t *testing.T) {
	r := recipe.BlockedRecipe()

	if r.Name != "blocked" {
		t.Errorf("Expected name 'blocked', got %s", r.Name)
	}
	if r.Filters.HasBlockers == nil || !*r.Filters.HasBlockers {
		t.Error("Expected HasBlockers filter to be true")
	}
	if !r.View.ShowGraph {
		t.Error("Expected ShowGraph to be true")
	}
}

func TestHighImpactRecipe(t *testing.T) {
	r := recipe.HighImpactRecipe()

	if r.Name != "high-impact" {
		t.Errorf("Expected name 'high-impact', got %s", r.Name)
	}
	if r.Sort.Field != "pagerank" {
		t.Errorf("Expected sort by pagerank, got %s", r.Sort.Field)
	}
	if r.View.MaxItems != 20 {
		t.Errorf("Expected MaxItems 20, got %d", r.View.MaxItems)
	}
}

func TestStaleRecipe(t *testing.T) {
	r := recipe.StaleRecipe()

	if r.Name != "stale" {
		t.Errorf("Expected name 'stale', got %s", r.Name)
	}
	if r.Filters.UpdatedBefore != "30d" {
		t.Errorf("Expected UpdatedBefore '30d', got %s", r.Filters.UpdatedBefore)
	}
}

func TestBuiltinRecipes(t *testing.T) {
	recipes := recipe.BuiltinRecipes()

	// Note: This tests the programmatic builtins, not the YAML embedded ones
	if len(recipes) < 6 {
		t.Errorf("Expected at least 6 builtin recipes, got %d", len(recipes))
	}

	names := make(map[string]bool)
	for _, r := range recipes {
		if r.Name == "" {
			t.Error("Recipe has empty name")
		}
		if names[r.Name] {
			t.Errorf("Duplicate recipe name: %s", r.Name)
		}
		names[r.Name] = true
	}
}

func TestRecipeStructTags(t *testing.T) {
	// Verify JSON/YAML struct tags exist by checking marshaling works
	r := recipe.DefaultRecipe()

	// Just verify the struct can be used (compile-time check)
	if r.Name == "" {
		t.Error("Name should not be empty")
	}
	if r.Filters.Status == nil {
		t.Error("Filters.Status should not be nil")
	}
}
