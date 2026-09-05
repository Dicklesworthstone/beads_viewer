package recipe_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasWarningContaining(warnings []string, parts ...string) bool {
	for _, w := range warnings {
		matched := true
		for _, p := range parts {
			if !strings.Contains(w, p) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func TestLoader_ProjectRecipeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	recipesDir := filepath.Join(tmpDir, ".beads", "recipes")

	// One recipe per file; the stem names it unless the file says otherwise.
	sprintPath := filepath.Join(recipesDir, "sprint.yaml")
	writeFile(t, sprintPath, `
description: "Current sprint work"
filters:
  status: [open, in_progress]
  tags: [sprint]
sort:
  field: priority
  secondary:
    field: updated
    direction: desc
view:
  max_items: 10
`)
	writeFile(t, filepath.Join(recipesDir, "review.yml"), `
name: sprint-review
description: "Named by the file, not the stem"
filters:
  status: [closed]
`)
	// A misspelt filter key must be reported by name, never silently dropped.
	writeFile(t, filepath.Join(recipesDir, "bad.yaml"), `
description: "typo"
filters:
  statuses: [open]
`)
	// Non-recipe entries are ignored.
	writeFile(t, filepath.Join(recipesDir, "README.md"), "# not a recipe\n")
	if err := os.MkdirAll(filepath.Join(recipesDir, "archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The project map is lower precedence than a project file of the same name.
	writeFile(t, filepath.Join(tmpDir, ".bv", "recipes.yaml"), `
recipes:
  sprint:
    description: "From the project map"
  map-only:
    description: "Only in the map"
`)

	loader := recipe.NewLoader(
		recipe.WithUserPath(filepath.Join(tmpDir, "no-user-config.yaml")),
		recipe.WithProjectDir(tmpDir),
	)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	sprint := loader.Get("sprint")
	if sprint == nil {
		t.Fatal("expected sprint recipe from .beads/recipes/sprint.yaml")
	}
	if sprint.Name != "sprint" || sprint.Description != "Current sprint work" {
		t.Fatalf("sprint recipe = %+v, want the project-file definition", sprint)
	}
	if got := loader.Source("sprint"); got != recipe.SourceProjectFile {
		t.Fatalf("sprint source = %q, want %q", got, recipe.SourceProjectFile)
	}
	if got := loader.Path("sprint"); got != sprintPath {
		t.Fatalf("sprint path = %q, want %q", got, sprintPath)
	}
	if sprint.Sort.Secondary == nil || sprint.Sort.Secondary.Field != "updated" || sprint.View.MaxItems != 10 {
		t.Fatalf("sprint recipe lost sort/view fields: %+v", sprint)
	}

	if r := loader.Get("sprint-review"); r == nil || r.Description != "Named by the file, not the stem" {
		t.Fatalf("expected sprint-review from review.yml's name field, got %+v", r)
	}
	if r := loader.Get("review"); r != nil {
		t.Fatalf("stem 'review' should not be registered when the file names itself: %+v", r)
	}
	if got := loader.Source("map-only"); got != recipe.SourceProject {
		t.Fatalf("map-only source = %q, want %q", got, recipe.SourceProject)
	}
	if r := loader.Get("bad"); r != nil {
		t.Fatalf("recipe with unknown key must not load: %+v", r)
	}
	if !hasWarningContaining(loader.Warnings(), "bad.yaml", "statuses") {
		t.Fatalf("expected a warning naming bad.yaml and the key 'statuses', got %v", loader.Warnings())
	}

	// Discovery output carries the source and defining path.
	var found bool
	for _, s := range loader.ListSummaries() {
		if s.Name != "sprint" {
			continue
		}
		found = true
		if s.Source != recipe.SourceProjectFile || s.Path != sprintPath {
			t.Fatalf("summary = %+v, want source project-file and path %s", s, sprintPath)
		}
	}
	if !found {
		t.Fatal("sprint missing from ListSummaries")
	}

	// And the recipe actually applies.
	issues := []model.Issue{
		{ID: "s-1", Status: model.StatusOpen, Priority: 2, Labels: []string{"sprint"}},
		{ID: "s-2", Status: model.StatusOpen, Priority: 1, Labels: []string{"sprint"}},
		{ID: "s-3", Status: model.StatusClosed, Priority: 0, Labels: []string{"sprint"}},
		{ID: "s-4", Status: model.StatusOpen, Priority: 0},
	}
	got, err := recipe.Apply(issues, recipe.Metrics{}, sprint, time.Now())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 2 || got[0].ID != "s-2" || got[1].ID != "s-1" {
		t.Fatalf("applied sprint recipe = %v, want [s-2 s-1]", got)
	}
}

func TestLoader_PathArgument(t *testing.T) {
	tmpDir := t.TempDir()
	loader := recipe.NewLoader(
		recipe.WithUserPath(filepath.Join(tmpDir, "no-user-config.yaml")),
		recipe.WithProjectDir(tmpDir),
	)
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, arg := range []string{"sprint.yaml", "SPRINT.YML", "./x.yaml", "/abs/path/x.yml", " a.yaml "} {
		if !recipe.IsPathArgument(arg) {
			t.Errorf("IsPathArgument(%q) = false, want true", arg)
		}
	}
	for _, arg := range []string{"actionable", "high-impact", "sprint.yaml.bak", "yaml", ""} {
		if recipe.IsPathArgument(arg) {
			t.Errorf("IsPathArgument(%q) = true, want false", arg)
		}
	}

	// A path outside any recipes dir, unnamed: the stem names it.
	unnamed := filepath.Join(tmpDir, "somewhere", "x.yaml")
	writeFile(t, unnamed, `
filters:
  status: [open]
sort:
  field: updated
  direction: desc
`)
	r, err := loader.Resolve(unnamed)
	if err != nil {
		t.Fatalf("Resolve(path): %v", err)
	}
	if r.Name != "x" || len(r.Filters.Status) != 1 || r.Sort.Field != "updated" {
		t.Fatalf("resolved recipe = %+v", r)
	}

	// The file's own name wins over the stem; .yml is accepted.
	named := filepath.Join(tmpDir, "named.yml")
	writeFile(t, named, "name: custom-name\ndescription: d\n")
	if r, err := loader.Resolve(named); err != nil || r.Name != "custom-name" {
		t.Fatalf("Resolve(named) = %+v, %v", r, err)
	}

	// Names still resolve.
	if r, err := loader.Resolve("actionable"); err != nil || r == nil || r.Name != "actionable" {
		t.Fatalf("Resolve(actionable) = %+v, %v", r, err)
	}

	// A missing path is a clear error naming the path.
	missing := filepath.Join(tmpDir, "missing.yaml")
	if _, err := loader.Resolve(missing); err == nil || !strings.Contains(err.Error(), "recipe file not found") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("Resolve(missing) error = %v, want 'recipe file not found: <path>'", err)
	}

	// An unknown name is an UnknownRecipeError carrying the available names.
	_, err = loader.Resolve("nope")
	var unknown *recipe.UnknownRecipeError
	if !errors.As(err, &unknown) {
		t.Fatalf("Resolve(nope) error = %T %v, want *UnknownRecipeError", err, err)
	}
	if unknown.Name != "nope" || len(unknown.Available) == 0 || !strings.Contains(err.Error(), `unknown recipe "nope"`) {
		t.Fatalf("UnknownRecipeError = %+v (%v)", unknown, err)
	}

	// Unknown keys fail with the key named.
	bad := filepath.Join(tmpDir, "bad.yaml")
	writeFile(t, bad, "filters:\n  statuses: [open]\n")
	if _, err := loader.Resolve(bad); err == nil || !strings.Contains(err.Error(), "statuses") || !strings.Contains(err.Error(), bad) {
		t.Fatalf("Resolve(bad) error = %v, want the key 'statuses' and the path", err)
	}

	// Unusable values fail through Validate.
	karma := filepath.Join(tmpDir, "karma.yaml")
	writeFile(t, karma, "sort:\n  field: karma\n")
	if _, err := loader.Resolve(karma); err == nil || !strings.Contains(err.Error(), `sort.field "karma"`) {
		t.Fatalf("Resolve(karma) error = %v", err)
	}

	// A recipes: map handed in as a path is rejected (it is not a single recipe).
	mapFile := filepath.Join(tmpDir, "map.yaml")
	writeFile(t, mapFile, "recipes:\n  a:\n    description: x\n")
	if _, err := loader.Resolve(mapFile); err == nil || !strings.Contains(err.Error(), "recipes") {
		t.Fatalf("Resolve(map file) error = %v, want the 'recipes' key named", err)
	}

	// Path recipes are not registered in the loader.
	if loader.Get("x") != nil || loader.Get("custom-name") != nil {
		t.Fatal("path-loaded recipes must not leak into the loader's table")
	}
}

func TestLoader_UnknownKeysRejectedInMapSources(t *testing.T) {
	tmpDir := t.TempDir()
	userPath := filepath.Join(tmpDir, "recipes.yaml")
	writeFile(t, userPath, `
recipes:
  good:
    description: "fine"
    filters:
      status: [open]
  typo:
    description: "misspelt key"
    filter:
      status: [open]
`)
	loader := recipe.NewLoader(recipe.WithUserPath(userPath), recipe.WithProjectDir(tmpDir))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Strict decoding rejects the whole document, so neither recipe loads and
	// the warning names the offending key.
	if loader.Get("typo") != nil || loader.Get("good") != nil {
		t.Fatalf("user file with an unknown key must be rejected; got typo=%v good=%v", loader.Get("typo"), loader.Get("good"))
	}
	if !hasWarningContaining(loader.Warnings(), "user config", "field filter not found") {
		t.Fatalf("expected warning naming the unknown key 'filter', got %v", loader.Warnings())
	}
	if loader.Get("default") == nil {
		t.Fatal("builtins must survive a bad user file")
	}

	// A recipe whose values fail Validate is skipped individually.
	writeFile(t, filepath.Join(tmpDir, ".bv", "recipes.yaml"), `
recipes:
  ok:
    filters:
      status: [open]
  broken:
    filters:
      updated_after: "last tuesday"
`)
	loader = recipe.NewLoader(recipe.WithUserPath(filepath.Join(tmpDir, "none.yaml")), recipe.WithProjectDir(tmpDir))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loader.Get("ok") == nil || loader.Get("broken") != nil {
		t.Fatalf("expected ok to load and broken to be skipped; ok=%v broken=%v", loader.Get("ok"), loader.Get("broken"))
	}
	if !hasWarningContaining(loader.Warnings(), "recipe broken", "filters.updated_after") {
		t.Fatalf("expected warning for broken.updated_after, got %v", loader.Warnings())
	}
}

func TestLoader_SourcePrecedence(t *testing.T) {
	tmpDir := t.TempDir()
	userPath := filepath.Join(tmpDir, "user.yaml")
	writeFile(t, userPath, `
recipes:
  default:
    description: "user"
  user-only:
    description: "user"
`)
	writeFile(t, filepath.Join(tmpDir, ".bv", "recipes.yaml"), `
recipes:
  default:
    description: "project"
  user-only:
    description: "project"
  project-only:
    description: "project"
`)
	writeFile(t, filepath.Join(tmpDir, ".beads", "recipes", "default.yaml"), "description: project-file\n")
	writeFile(t, filepath.Join(tmpDir, ".beads", "recipes", "project-only.yaml"), "description: project-file\n")

	loader := recipe.NewLoader(recipe.WithUserPath(userPath), recipe.WithProjectDir(tmpDir))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"default":      recipe.SourceProjectFile,
		"user-only":    recipe.SourceProject,
		"project-only": recipe.SourceProjectFile,
		"actionable":   recipe.SourceBuiltin,
	}
	for name, source := range want {
		r := loader.Get(name)
		if r == nil {
			t.Fatalf("recipe %s missing", name)
		}
		if got := loader.Source(name); got != source {
			t.Errorf("%s source = %q, want %q", name, got, source)
		}
		if source != recipe.SourceBuiltin && r.Description != strings.TrimSuffix(source, "") {
			t.Errorf("%s description = %q, want %q (highest-precedence source)", name, r.Description, source)
		}
	}
	if loader.Path("user-only") != "" {
		t.Errorf("map-sourced recipe should have no path, got %q", loader.Path("user-only"))
	}
}

func TestLoader_BuiltinsValidateAndApply(t *testing.T) {
	loader := recipe.NewLoader(recipe.WithUserPath("/nonexistent/recipes.yaml"), recipe.WithProjectDir("/nonexistent/project"))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, r := range loader.List() {
		if err := r.Validate(); err != nil {
			t.Errorf("builtin %s: %v", r.Name, err)
		}
		if _, err := recipe.Apply(nil, recipe.Metrics{}, &r, time.Now()); err != nil {
			t.Errorf("builtin %s Apply: %v", r.Name, err)
		}
	}
	// The metric builtins declare which sources they need.
	for name, wantGraph := range map[string]bool{"high-impact": true, "bottlenecks": true, "triage": false, "actionable": false} {
		if got := loader.Get(name).NeedsGraphMetrics(); got != wantGraph {
			t.Errorf("%s NeedsGraphMetrics = %v, want %v", name, got, wantGraph)
		}
	}
	if !loader.Get("triage").NeedsTriageScores() || loader.Get("high-impact").NeedsTriageScores() {
		t.Error("only the triage builtin should need triage scores")
	}
}

func TestLoader_InvalidPresentationValues(t *testing.T) {
	for _, tc := range []struct{ field, yaml string }{
		{"view.columns", "view:\n  columns: [assignee_typo]\n"},
		{"view.group_by", "view:\n  group_by: guessed\n"},
		{"view.truncate_title", "view:\n  truncate_title: -1\n"},
		{"view.collapsed", "view:\n  collapsed: true\n"},
		{"metrics", "metrics: [pagerank_typo]\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "invalid.yaml")
			writeFile(t, path, "name: invalid\n"+tc.yaml)
			l := recipe.NewLoader(recipe.WithUserPath(filepath.Join(dir, "none.yaml")), recipe.WithProjectDir(dir))
			if err := l.Load(); err != nil {
				t.Fatal(err)
			}
			if _, err := l.Resolve(path); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("explicit invalid recipe must fail with field %s: %v", tc.field, err)
			}
			writeFile(t, filepath.Join(dir, ".beads", "recipes", "invalid.yaml"), "name: invalid\n"+tc.yaml)
			if err := l.Load(); err != nil {
				t.Fatal(err)
			}
			if l.Get("invalid") != nil || !hasWarningContaining(l.Warnings(), tc.field) {
				t.Fatalf("discovered invalid recipe was accepted or silently ignored: warnings=%v", l.Warnings())
			}
		})
	}
}
