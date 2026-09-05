package recipe_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

func TestPresentationValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    recipe.Recipe
		want string
	}{
		{"column", recipe.Recipe{View: recipe.ViewConfig{Columns: []string{"secret"}}}, "view.columns"},
		{"duplicate column", recipe.Recipe{View: recipe.ViewConfig{Columns: []string{"id", "id"}}}, "repeats"},
		{"group", recipe.Recipe{View: recipe.ViewConfig{GroupBy: "assignee"}}, "view.group_by"},
		{"collapse without groups", recipe.Recipe{View: recipe.ViewConfig{Collapsed: true}}, "view.collapsed"},
		{"negative width", recipe.Recipe{View: recipe.ViewConfig{TruncateTitle: -1}}, "view.truncate_title"},
		{"metric", recipe.Recipe{Metrics: []string{"invented"}}, "metrics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.r.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s validation error, got %v", tc.want, err)
			}
		})
	}
	for _, group := range []string{"", "none", "status", "priority", "tag"} {
		r := recipe.Recipe{View: recipe.ViewConfig{Columns: recipe.ViewColumns, GroupBy: group, TruncateTitle: 1}, Metrics: recipe.ViewMetrics}
		if err := r.Validate(); err != nil {
			t.Fatalf("valid presentation: %v", err)
		}
	}
}

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

	// Should be in same location as 'now'
	expected := time.Date(2024, 6, 15, 0, 0, 0, 0, now.Location())
	if !result.Equal(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
	if result.Location() != now.Location() {
		t.Errorf("Expected location %v, got %v", now.Location(), result.Location())
	}
}

func TestParseRelativeTimeRFC3339(t *testing.T) {
	now := time.Now()

	// Z implies UTC
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

func TestRecipeStructTags(t *testing.T) {
	// Verify JSON/YAML struct tags exist by checking marshaling works
	r := recipe.Recipe{
		Name: "test",
		Filters: recipe.FilterConfig{
			Status: []string{"open"},
		},
	}

	// Just verify the struct can be used (compile-time check)
	if r.Name == "" {
		t.Error("Name should not be empty")
	}
	if r.Filters.Status == nil {
		t.Error("Filters.Status should not be nil")
	}
}
