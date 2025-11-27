package ui

import (
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"

	"github.com/charmbracelet/lipgloss"
)

func TestRecipePickerSelection(t *testing.T) {
	recipes := []recipe.Recipe{
		{Name: "Triage", Description: "Focus on blockers"},
		{Name: "Release", Description: "Prep for release"},
		{Name: "Cleanup", Description: "Debt sweep"},
	}

	m := NewRecipePickerModel(recipes, DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(80, 24)

	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Triage" {
		t.Fatalf("expected initial selection Triage, got %+v", sel)
	}

	m.MoveDown()
	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Release" {
		t.Fatalf("expected selection Release after MoveDown, got %+v", sel)
	}

	m.MoveUp()
	if sel := m.SelectedRecipe(); sel == nil || sel.Name != "Triage" {
		t.Fatalf("expected back to Triage after MoveUp, got %+v", sel)
	}
}

func TestRecipePickerViewContainsNames(t *testing.T) {
	recipes := []recipe.Recipe{
		{Name: "Alpha", Description: "First"},
	}
	m := NewRecipePickerModel(recipes, DefaultTheme(lipgloss.NewRenderer(nil)))
	m.SetSize(60, 20)

	out := m.View()
	if !strings.Contains(out, "Alpha") {
		t.Fatalf("expected view to contain recipe name, got:\n%s", out)
	}
	if !strings.Contains(out, "Select Recipe") {
		t.Fatalf("expected view title, got:\n%s", out)
	}
}

func TestFormatRecipeInfo(t *testing.T) {
	if got := FormatRecipeInfo(nil); got != "" {
		t.Fatalf("expected empty string for nil recipe, got %q", got)
	}
	r := recipe.Recipe{Name: "Demo"}
	if got := FormatRecipeInfo(&r); got != "Recipe: Demo" {
		t.Fatalf("unexpected format: %s", got)
	}
}
