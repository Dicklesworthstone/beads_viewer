package recipe_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

func TestLoaderBuiltinRecipes(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath(""),   // Disable user config
		recipe.WithProjectDir(""), // Disable project config
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	// Should have builtin recipes
	names := loader.Names()
	if len(names) == 0 {
		t.Error("Expected builtin recipes, got none")
	}

	// Check for expected builtins (core recipes)
	expectedRecipes := []string{"default", "actionable", "recent", "blocked", "high-impact", "stale", "triage", "closed", "release-cut", "quick-wins", "bottlenecks"}
	for _, name := range expectedRecipes {
		r := loader.Get(name)
		if r == nil {
			t.Errorf("Expected builtin recipe %q", name)
		} else {
			if loader.Source(name) != "builtin" {
				t.Errorf("Expected source 'builtin' for %q, got %q", name, loader.Source(name))
			}
		}
	}
}

func TestLoaderGetRecipe(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath(""),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	r := loader.Get("default")
	if r == nil {
		t.Fatal("Expected default recipe")
	}

	if r.Name != "default" {
		t.Errorf("Expected name 'default', got %q", r.Name)
	}

	if r.Description == "" {
		t.Error("Expected non-empty description")
	}
}

func TestLoaderGetNonExistent(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath(""),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	r := loader.Get("nonexistent")
	if r != nil {
		t.Error("Expected nil for nonexistent recipe")
	}
}

func TestLoaderUserOverride(t *testing.T) {
	// Create temp user config
	tmpDir := t.TempDir()
	userPath := filepath.Join(tmpDir, "recipes.yaml")

	userConfig := `
recipes:
  custom:
    description: "Custom user recipe"
    filters:
      status: [open]
    sort:
      field: title
  default:
    description: "Overridden default"
    filters:
      status: [closed]
`
	if err := os.WriteFile(userPath, []byte(userConfig), 0644); err != nil {
		t.Fatal(err)
	}

	loader := recipe.NewLoader(
		recipe.WithUserPath(userPath),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	// Check custom recipe was added
	custom := loader.Get("custom")
	if custom == nil {
		t.Fatal("Expected custom recipe")
	}
	if custom.Description != "Custom user recipe" {
		t.Errorf("Expected custom description, got %q", custom.Description)
	}
	if loader.Source("custom") != "user" {
		t.Errorf("Expected source 'user' for custom, got %q", loader.Source("custom"))
	}

	// Check default was overridden
	def := loader.Get("default")
	if def == nil {
		t.Fatal("Expected default recipe")
	}
	if def.Description != "Overridden default" {
		t.Errorf("Expected overridden description, got %q", def.Description)
	}
	if loader.Source("default") != "user" {
		t.Errorf("Expected source 'user' for overridden default, got %q", loader.Source("default"))
	}
}

func TestLoaderProjectOverride(t *testing.T) {
	tmpDir := t.TempDir()

	// Create project config
	projectDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	projectConfig := `
recipes:
  project-local:
    description: "Project-specific recipe"
    filters:
      id_prefix: "proj-"
`
	if err := os.WriteFile(filepath.Join(projectDir, "recipes.yaml"), []byte(projectConfig), 0644); err != nil {
		t.Fatal(err)
	}

	loader := recipe.NewLoader(
		recipe.WithUserPath(""),
		recipe.WithProjectDir(tmpDir),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	r := loader.Get("project-local")
	if r == nil {
		t.Fatal("Expected project-local recipe")
	}
	if loader.Source("project-local") != "project" {
		t.Errorf("Expected source 'project', got %q", loader.Source("project-local"))
	}
}

func TestLoaderDisableRecipe(t *testing.T) {
	tmpDir := t.TempDir()
	userPath := filepath.Join(tmpDir, "recipes.yaml")

	// Disable the 'stale' recipe with null
	userConfig := `
recipes:
  stale: null
`
	if err := os.WriteFile(userPath, []byte(userConfig), 0644); err != nil {
		t.Fatal(err)
	}

	loader := recipe.NewLoader(
		recipe.WithUserPath(userPath),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	// stale should be disabled
	if r := loader.Get("stale"); r != nil {
		t.Error("Expected stale recipe to be disabled")
	}

	// Other builtins should still exist
	if r := loader.Get("default"); r == nil {
		t.Error("Expected default recipe to still exist")
	}
}

func TestLoaderListSummaries(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath(""),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	summaries := loader.ListSummaries()
	if len(summaries) == 0 {
		t.Error("Expected summaries")
	}

	for _, s := range summaries {
		if s.Name == "" {
			t.Error("Summary has empty name")
		}
		if s.Source == "" {
			t.Error("Summary has empty source")
		}
	}
}

func TestLoaderMissingFiles(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath("/nonexistent/path/recipes.yaml"),
		recipe.WithProjectDir("/nonexistent/project"),
	)

	// Should not error on missing optional files
	if err := loader.Load(); err != nil {
		t.Errorf("Should not error on missing files: %v", err)
	}

	// Should still have builtins
	if r := loader.Get("default"); r == nil {
		t.Error("Expected builtin recipes despite missing files")
	}

	// Should have no warnings for nonexistent files (expected)
	warnings := loader.Warnings()
	for _, w := range warnings {
		t.Logf("Warning: %s", w)
	}
}

func TestLoaderInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	userPath := filepath.Join(tmpDir, "recipes.yaml")

	// Write invalid YAML
	if err := os.WriteFile(userPath, []byte("invalid: [yaml: {"), 0644); err != nil {
		t.Fatal(err)
	}

	loader := recipe.NewLoader(
		recipe.WithUserPath(userPath),
		recipe.WithProjectDir(""),
	)

	// Should not error, but add warning
	if err := loader.Load(); err != nil {
		t.Errorf("Should not error on invalid user config: %v", err)
	}

	// Should have warning
	warnings := loader.Warnings()
	if len(warnings) == 0 {
		t.Error("Expected warning for invalid YAML")
	}

	// Should still have builtins
	if r := loader.Get("default"); r == nil {
		t.Error("Expected builtin recipes despite invalid user config")
	}
}

func TestLoadDefault(t *testing.T) {
	loader, err := recipe.LoadDefault()
	if err != nil {
		t.Fatalf("LoadDefault failed: %v", err)
	}

	if r := loader.Get("default"); r == nil {
		t.Error("Expected default recipe from LoadDefault")
	}
}

func TestLoaderList(t *testing.T) {
	loader := recipe.NewLoader(
		recipe.WithUserPath(""),
		recipe.WithProjectDir(""),
	)

	if err := loader.Load(); err != nil {
		t.Fatalf("Failed to load: %v", err)
	}

	list := loader.List()
	names := loader.Names()

	if len(list) != len(names) {
		t.Errorf("List length %d != Names length %d", len(list), len(names))
	}

	if len(list) == 0 {
		t.Error("Expected non-empty list")
	}
}
