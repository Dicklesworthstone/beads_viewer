package recipe

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed defaults/recipes.yaml
var defaultRecipesFS embed.FS

// Recipe sources, in ascending precedence: a later source overrides an
// earlier one that defines the same name.
const (
	SourceBuiltin     = "builtin"      // embedded defaults/recipes.yaml
	SourceUser        = "user"         // ~/.config/bv/recipes.yaml (recipes: map)
	SourceProject     = "project"      // <project>/.bv/recipes.yaml (recipes: map)
	SourceProjectFile = "project-file" // <project>/.beads/recipes/<name>.yaml (one recipe per file)
)

// RecipeFile represents the structure of a recipes YAML file
type RecipeFile struct {
	Recipes map[string]*Recipe `yaml:"recipes"`
}

// RecipeSummary is a lightweight representation for discovery
type RecipeSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`         // one of the Source* constants
	Path        string `json:"path,omitempty"` // file that defined a project-file recipe
}

// UnknownRecipeError is returned by Resolve when the argument names neither a
// recipe file nor a loaded recipe.
type UnknownRecipeError struct {
	Name      string
	Available []string
}

func (e *UnknownRecipeError) Error() string {
	return fmt.Sprintf("unknown recipe %q", e.Name)
}

// Loader handles loading and merging recipes from multiple sources
type Loader struct {
	recipes    map[string]Recipe
	sources    map[string]string // recipe name -> source
	paths      map[string]string // recipe name -> defining file (project-file only)
	userPath   string
	projectDir string
	warnings   []string
}

// LoaderOption configures the loader
type LoaderOption func(*Loader)

// WithUserPath sets a custom user config path (default: ~/.config/bv/recipes.yaml)
func WithUserPath(path string) LoaderOption {
	return func(l *Loader) {
		l.userPath = path
	}
}

// WithProjectDir sets the project directory (default: current directory)
func WithProjectDir(dir string) LoaderOption {
	return func(l *Loader) {
		l.projectDir = dir
	}
}

// NewLoader creates a new recipe loader with options
func NewLoader(opts ...LoaderOption) *Loader {
	l := &Loader{
		recipes: make(map[string]Recipe),
		sources: make(map[string]string),
		paths:   make(map[string]string),
	}

	for _, opt := range opts {
		opt(l)
	}

	// Set defaults
	if l.userPath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			l.userPath = filepath.Join(home, ".config", "bv", "recipes.yaml")
		}
	}

	if l.projectDir == "" {
		l.projectDir, _ = os.Getwd()
	}

	return l
}

// Load loads recipes from all sources in ascending precedence:
// builtin < user < project map < project files. Missing optional files are
// fine; unreadable or invalid ones become Warnings and are skipped.
func (l *Loader) Load() error {
	// 1. Load embedded defaults
	if err := l.loadBuiltin(); err != nil {
		return fmt.Errorf("loading builtin recipes: %w", err)
	}

	// 2. Load user config (optional, no error if missing)
	if l.userPath != "" {
		if err := l.loadFromFile(l.userPath, SourceUser); err != nil {
			// Only add warning, don't fail
			if !os.IsNotExist(err) {
				l.warnings = append(l.warnings, fmt.Sprintf("user config: %v", err))
			}
		}
	}

	// 3. Load project config (optional, no error if missing)
	if l.projectDir != "" {
		projectPath := filepath.Join(l.projectDir, ".bv", "recipes.yaml")
		if err := l.loadFromFile(projectPath, SourceProject); err != nil {
			if !os.IsNotExist(err) {
				l.warnings = append(l.warnings, fmt.Sprintf("project config: %v", err))
			}
		}

		// 4. Load per-recipe project files (optional)
		l.loadProjectFiles(filepath.Join(l.projectDir, ".beads", "recipes"))
	}

	return nil
}

// loadBuiltin loads the embedded default recipes
func (l *Loader) loadBuiltin() error {
	data, err := defaultRecipesFS.ReadFile("defaults/recipes.yaml")
	if err != nil {
		return err
	}

	var file RecipeFile
	if err := decodeStrict(data, &file); err != nil {
		return fmt.Errorf("parsing embedded defaults: %w", err)
	}

	for name, recipe := range file.Recipes {
		if recipe == nil {
			continue
		}
		recipe.Name = name
		if err := recipe.Validate(); err != nil {
			return fmt.Errorf("builtin recipe %s: %w", name, err)
		}
		l.recipes[name] = *recipe
		l.sources[name] = SourceBuiltin
	}

	return nil
}

// loadFromFile loads a `recipes:` map from a YAML file and merges it.
// Individual recipes that fail Validate are skipped with a warning.
func (l *Loader) loadFromFile(path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var file RecipeFile
	if err := decodeStrict(data, &file); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	for name, recipe := range file.Recipes {
		if recipe == nil {
			// Explicit null means "disable this recipe"
			l.remove(name)
			continue
		}
		recipe.Name = name
		if err := recipe.Validate(); err != nil {
			l.warnings = append(l.warnings, fmt.Sprintf("recipe %s in %s: %v", name, path, err))
			continue
		}
		if source == SourceUser && recipe.Export.Template != "" {
			// A user default must not select a file relative to an untrusted
			// project's working directory. Keep it anchored to the user config.
			templatePath := recipe.Export.Template
			if !filepath.IsAbs(templatePath) {
				templatePath = filepath.Join(filepath.Dir(path), templatePath)
			}
			templatePath, err := filepath.Abs(templatePath)
			if err != nil {
				return fmt.Errorf("resolving template for user recipe %s: %w", name, err)
			}
			recipe.Export.Template = templatePath
			recipe.Export.authorizedTemplate = templatePath
		}
		l.set(name, *recipe, source, "")
	}

	return nil
}

// loadProjectFiles registers every *.yaml / *.yml under dir as one recipe
// with source "project-file". Files are visited in name order so a duplicate
// name is resolved deterministically (later file wins, with a warning).
func (l *Loader) loadProjectFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			l.warnings = append(l.warnings, fmt.Sprintf("project recipes dir %s: %v", dir, err))
		}
		return
	}

	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !IsPathArgument(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)

	for _, path := range paths {
		recipe, err := LoadFile(path)
		if err != nil {
			l.warnings = append(l.warnings, err.Error())
			continue
		}
		if prev, ok := l.paths[recipe.Name]; ok && prev != path {
			l.warnings = append(l.warnings, fmt.Sprintf("project recipe file %s: recipe %q already defined by %s; using %s", path, recipe.Name, prev, path))
		}
		l.set(recipe.Name, *recipe, SourceProjectFile, path)
	}
}

func (l *Loader) set(name string, recipe Recipe, source, path string) {
	if source != SourceUser {
		recipe.Export.authorizedTemplate = ""
	}
	l.recipes[name] = recipe
	l.sources[name] = source
	if path != "" {
		l.paths[name] = path
	} else {
		delete(l.paths, name)
	}
}

func (l *Loader) remove(name string) {
	delete(l.recipes, name)
	delete(l.sources, name)
	delete(l.paths, name)
}

// decodeStrict unmarshals YAML rejecting keys the target type does not
// declare, so a misspelt filter is reported (with its name) instead of being
// silently dropped. An empty document leaves out untouched.
func decodeStrict(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// IsPathArgument reports whether a --recipe argument names a YAML file
// (ends in .yaml or .yml) rather than a recipe name.
func IsPathArgument(arg string) bool {
	lower := strings.ToLower(strings.TrimSpace(arg))
	return strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")
}

// LoadFile parses a single-recipe YAML file such as
// .beads/recipes/sprint-review.yaml. The recipe's name comes from its `name`
// field, defaulting to the file stem. Unknown keys and invalid values are
// errors that name the offending field.
func LoadFile(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("recipe file not found: %s", path)
		}
		return nil, fmt.Errorf("reading recipe file %s: %w", path, err)
	}

	var recipe Recipe
	if err := decodeStrict(data, &recipe); err != nil {
		return nil, fmt.Errorf("parsing recipe file %s: %w", path, err)
	}
	if strings.TrimSpace(recipe.Name) == "" {
		recipe.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if err := recipe.Validate(); err != nil {
		return nil, fmt.Errorf("recipe file %s: %w", path, err)
	}
	return &recipe, nil
}

// Resolve turns a --recipe argument into a recipe: a .yaml/.yml argument is
// loaded from that path, anything else is looked up by name. A missing name
// yields *UnknownRecipeError carrying the available names.
func (l *Loader) Resolve(arg string) (*Recipe, error) {
	if IsPathArgument(arg) {
		return LoadFile(arg)
	}
	if recipe := l.Get(arg); recipe != nil {
		return recipe, nil
	}
	return nil, &UnknownRecipeError{Name: arg, Available: l.Names()}
}

// Get returns a recipe by name, or nil if not found
func (l *Loader) Get(name string) *Recipe {
	if recipe, ok := l.recipes[name]; ok {
		return &recipe
	}
	return nil
}

// List returns all available recipes sorted by name
func (l *Loader) List() []Recipe {
	var names []string
	for name := range l.recipes {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]Recipe, 0, len(names))
	for _, name := range names {
		result = append(result, l.recipes[name])
	}
	return result
}

// ListSummaries returns lightweight recipe summaries for discovery, sorted by name
func (l *Loader) ListSummaries() []RecipeSummary {
	var names []string
	for name := range l.recipes {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]RecipeSummary, 0, len(names))
	for _, name := range names {
		result = append(result, RecipeSummary{
			Name:        name,
			Description: l.recipes[name].Description,
			Source:      l.sources[name],
			Path:        l.paths[name],
		})
	}
	return result
}

// Names returns all recipe names sorted alphabetically
func (l *Loader) Names() []string {
	names := make([]string, 0, len(l.recipes))
	for name := range l.recipes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Warnings returns any warnings from loading
func (l *Loader) Warnings() []string {
	return l.warnings
}

// Source returns the source of a recipe (one of the Source* constants)
func (l *Loader) Source(name string) string {
	return l.sources[name]
}

// Path returns the file that defined a project-file recipe, or "" for
// recipes from map sources.
func (l *Loader) Path(name string) string {
	return l.paths[name]
}

// LoadDefault creates a loader and loads with default settings
func LoadDefault() (*Loader, error) {
	loader := NewLoader()
	if err := loader.Load(); err != nil {
		return nil, err
	}
	return loader, nil
}
