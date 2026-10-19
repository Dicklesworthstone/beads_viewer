package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFuzzyScoreExactMatch(t *testing.T) {
	score := fuzzyScore("api", "api")
	if score != 1000 {
		t.Errorf("Expected exact match score 1000, got %d", score)
	}
}

func TestFuzzyScorePrefixMatch(t *testing.T) {
	score := fuzzyScore("backend", "back")
	if score < 500 {
		t.Errorf("Expected prefix match score >= 500, got %d", score)
	}
}

func TestFuzzyScoreContainsMatch(t *testing.T) {
	score := fuzzyScore("my-backend-api", "backend")
	if score < 200 {
		t.Errorf("Expected contains match score >= 200, got %d", score)
	}
}

func TestFuzzyScoreSubsequenceMatch(t *testing.T) {
	score := fuzzyScore("backend", "bnd")
	if score <= 0 {
		t.Errorf("Expected subsequence match score > 0, got %d", score)
	}
}

func TestFuzzyScoreNoMatch(t *testing.T) {
	score := fuzzyScore("api", "xyz")
	if score != 0 {
		t.Errorf("Expected no match score 0, got %d", score)
	}
}

func TestFuzzyScoreCaseInsensitive(t *testing.T) {
	score1 := fuzzyScore("API", "api")
	score2 := fuzzyScore("api", "API")
	if score1 != 1000 || score2 != 1000 {
		t.Errorf("Expected case-insensitive exact match, got scores %d and %d", score1, score2)
	}
}

func TestFuzzyScoreWordBoundaryBonus(t *testing.T) {
	// Word boundary matches should score higher
	score1 := fuzzyScore("my-api-service", "as") // "a" at boundary, "s" in "service"
	score2 := fuzzyScore("myapiservice", "as")   // "a" and "s" not at boundaries
	if score1 <= score2 {
		t.Errorf("Expected word boundary match to score higher: boundary=%d, no-boundary=%d", score1, score2)
	}
}

func TestNewLabelPickerModel(t *testing.T) {
	labels := []string{"zebra", "api", "backend", "core"}
	counts := map[string]int{
		"zebra":   5,
		"api":     10,
		"backend": 3,
		"core":    7,
	}
	picker := NewLabelPickerModel(labels, counts, Theme{})

	// Should be sorted by count descending: api(10), core(7), zebra(5), backend(3)
	if picker.allLabels[0] != "api" {
		t.Errorf("Expected first label to be 'api' (highest count), got %s", picker.allLabels[0])
	}
	if picker.allLabels[1] != "core" {
		t.Errorf("Expected second label to be 'core' (second highest), got %s", picker.allLabels[1])
	}
	if picker.allLabels[3] != "backend" {
		t.Errorf("Expected last label to be 'backend' (lowest count), got %s", picker.allLabels[3])
	}
}

func TestLabelPickerSetLabels(t *testing.T) {
	picker := NewLabelPickerModel([]string{"a"}, map[string]int{"a": 1}, Theme{})
	picker.SetLabels([]string{"z", "m", "a"}, map[string]int{"z": 10, "m": 5, "a": 1})

	if len(picker.allLabels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(picker.allLabels))
	}
	// Should be sorted by count descending: z(10), m(5), a(1)
	if picker.allLabels[0] != "z" {
		t.Errorf("Expected first label 'z' (highest count), got %s", picker.allLabels[0])
	}
}

func TestLabelPickerNavigation(t *testing.T) {
	labels := []string{"api", "backend", "core"}
	// All same count so sorted alphabetically for ties
	counts := map[string]int{"api": 5, "backend": 5, "core": 5}
	picker := NewLabelPickerModel(labels, counts, Theme{})

	if picker.SelectedLabel() != "api" {
		t.Errorf("Expected initial selection 'api', got %s", picker.SelectedLabel())
	}

	picker.MoveDown()
	if picker.SelectedLabel() != "backend" {
		t.Errorf("Expected 'backend' after MoveDown, got %s", picker.SelectedLabel())
	}

	picker.MoveDown()
	if picker.SelectedLabel() != "core" {
		t.Errorf("Expected 'core' after second MoveDown, got %s", picker.SelectedLabel())
	}

	// At end, MoveDown should stay at end
	picker.MoveDown()
	if picker.SelectedLabel() != "core" {
		t.Errorf("Expected 'core' at end boundary, got %s", picker.SelectedLabel())
	}

	picker.MoveUp()
	if picker.SelectedLabel() != "backend" {
		t.Errorf("Expected 'backend' after MoveUp, got %s", picker.SelectedLabel())
	}
}

func TestLabelPickerEmptySelection(t *testing.T) {
	picker := NewLabelPickerModel([]string{}, map[string]int{}, Theme{})
	if picker.SelectedLabel() != "" {
		t.Errorf("Expected empty selection from empty labels, got %s", picker.SelectedLabel())
	}
}

func TestLabelPickerFilteredCount(t *testing.T) {
	labels := []string{"api", "api-v2", "backend", "core"}
	counts := map[string]int{"api": 5, "api-v2": 3, "backend": 2, "core": 1}
	picker := NewLabelPickerModel(labels, counts, Theme{})

	if picker.FilteredCount() != 4 {
		t.Errorf("Expected 4 filtered labels initially, got %d", picker.FilteredCount())
	}
}

func TestLabelPickerReset(t *testing.T) {
	labels := []string{"api", "backend"}
	counts := map[string]int{"api": 5, "backend": 5}
	picker := NewLabelPickerModel(labels, counts, Theme{})
	picker.MoveDown()
	picker.Reset()

	if picker.InputValue() != "" {
		t.Errorf("Expected empty input after Reset, got %s", picker.InputValue())
	}
	if picker.selectedIndex != 0 {
		t.Errorf("Expected selectedIndex 0 after Reset, got %d", picker.selectedIndex)
	}
}

// TestLabelPickerUpdateInputRefiltersOnlyOnQueryChange covers #213 at the
// model level: a message that leaves the query unchanged keeps the selection,
// while an edited query highlights its best match.
func TestLabelPickerUpdateInputRefiltersOnlyOnQueryChange(t *testing.T) {
	labels := []string{"api", "backend", "core"}
	counts := map[string]int{"api": 5, "backend": 5, "core": 5}
	picker := NewLabelPickerModel(labels, counts, Theme{})
	_ = picker.Focus()

	picker.MoveDown()
	picker.MoveDown()
	type unrelatedMsg struct{}
	_ = picker.UpdateInput(unrelatedMsg{})
	if got := picker.SelectedLabel(); got != "core" {
		t.Fatalf("selection after a non-editing message = %q, want %q", got, "core")
	}

	_ = picker.UpdateInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if got := picker.SelectedLabel(); got != "api" {
		t.Fatalf("selection after typing %q = %q, want best match %q", "a", got, "api")
	}
}

// TestLabelPickerSetLabelsKeepsSelectedLabel: reloading the labels while the
// query is unchanged keeps the highlighted label, even when its row moves.
func TestLabelPickerSetLabelsKeepsSelectedLabel(t *testing.T) {
	picker := NewLabelPickerModel(
		[]string{"api", "backend", "core"},
		map[string]int{"api": 5, "backend": 5, "core": 5},
		Theme{},
	)
	picker.MoveDown()
	if got := picker.SelectedLabel(); got != "backend" {
		t.Fatalf("selected = %q, want backend", got)
	}

	picker.SetLabels(
		[]string{"api", "backend", "core", "aaa"},
		map[string]int{"api": 5, "backend": 5, "core": 5, "aaa": 9},
	)
	if got := picker.SelectedLabel(); got != "backend" {
		t.Fatalf("selected after reload = %q, want backend", got)
	}

	// A selected label that disappears falls back to a valid row.
	picker.SetLabels([]string{"api"}, map[string]int{"api": 1})
	if got := picker.SelectedLabel(); got != "api" {
		t.Fatalf("selected after removal = %q, want api", got)
	}
}

func TestItoaHelper(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{100, "100"},
		{-5, "-5"},
	}

	for _, tc := range tests {
		result := itoa(tc.input)
		if result != tc.expected {
			t.Errorf("itoa(%d) = %s, want %s", tc.input, result, tc.expected)
		}
	}
}
