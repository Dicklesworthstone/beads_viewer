package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents a workspace configuration file (.bv/workspace.yaml)
type Config struct {
	// Name is the workspace display name
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Repos lists all repositories in this workspace
	Repos []RepoConfig `yaml:"repos" json:"repos"`

	// Discovery configures auto-discovery of repos
	Discovery DiscoveryConfig `yaml:"discovery,omitempty" json:"discovery,omitempty"`

	// Defaults sets default values for repos
	Defaults RepoDefaults `yaml:"defaults,omitempty" json:"defaults,omitempty"`
}

// RepoConfig represents a single repository in the workspace
type RepoConfig struct {
	// Name is the display name for this repo (default: directory name)
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Path is the path to the repository (relative to workspace root or absolute)
	Path string `yaml:"path" json:"path"`

	// Prefix is the ID prefix for issues from this repo (e.g., "api-" for api-123)
	// If empty, uses repo name + hyphen (e.g., "api-")
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`

	// BeadsPath is the path to .beads directory relative to repo (default: .beads)
	BeadsPath string `yaml:"beads_path,omitempty" json:"beads_path,omitempty"`

	// Enabled controls whether this repo is included (default: true)
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// DiscoveryConfig controls automatic repository discovery
type DiscoveryConfig struct {
	// Enabled turns on auto-discovery (default: false)
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Patterns are glob patterns to match directories containing .beads
	// Default: ["*", "packages/*", "apps/*", "services/*"]
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`

	// Exclude patterns for directories to skip
	Exclude []string `yaml:"exclude,omitempty" json:"exclude,omitempty"`

	// MaxDepth limits directory traversal depth (default: 2)
	MaxDepth int `yaml:"max_depth,omitempty" json:"max_depth,omitempty"`
}

// RepoDefaults provides default values for repos
type RepoDefaults struct {
	// BeadsPath default (default: .beads)
	BeadsPath string `yaml:"beads_path,omitempty" json:"beads_path,omitempty"`
}

// DefaultDiscoveryPatterns returns the standard patterns for common monorepo layouts
func DefaultDiscoveryPatterns() []string {
	return []string{
		"*",          // Direct children
		"packages/*", // npm/pnpm workspaces
		"apps/*",     // Next.js/Turborepo convention
		"services/*", // Microservices layout
		"libs/*",     // Library packages
		"modules/*",  // Go modules layout
	}
}

// DefaultExcludePatterns returns patterns to exclude from discovery
func DefaultExcludePatterns() []string {
	return []string{
		"node_modules",
		"vendor",
		".git",
		"dist",
		"build",
		"target",
	}
}

// Validate checks the configuration for errors
func (c *Config) Validate() error {
	if len(c.Repos) == 0 && !c.Discovery.Enabled {
		return fmt.Errorf("workspace must have at least one repo or enable discovery")
	}

	seen := make(map[string]bool)
	seenSourceRepos := make(map[string]bool)
	enabledCount := 0
	for i, repo := range c.Repos {
		if !repo.IsEnabled() {
			continue
		}
		enabledCount++
		if repo.Path == "" {
			return fmt.Errorf("repo[%d]: path is required", i)
		}

		prefix := strings.ToLower(repo.GetPrefix())
		if seen[prefix] {
			return fmt.Errorf("repo[%d]: duplicate prefix %q", i, prefix)
		}
		seen[prefix] = true

		sourceRepo := sourceRepoKeyFromPrefix(prefix)
		if sourceRepo == "" {
			return fmt.Errorf("repo[%d]: prefix %q has no usable source repository key", i, prefix)
		}
		if seenSourceRepos[sourceRepo] {
			return fmt.Errorf("repo[%d]: prefix %q duplicates normalized source repository key %q", i, prefix, sourceRepo)
		}
		seenSourceRepos[sourceRepo] = true
	}

	if enabledCount == 0 && !c.Discovery.Enabled {
		return fmt.Errorf("workspace must have at least one enabled repo or enable discovery")
	}

	return nil
}

// GetPrefix returns the effective prefix for a repo
func (r *RepoConfig) GetPrefix() string {
	if r.Prefix != "" {
		return r.Prefix
	}
	// Default: use repo name + hyphen
	name := r.Name
	if name == "" {
		name = filepath.Base(r.Path)
	}
	return strings.ToLower(name) + "-"
}

// GetName returns the effective name for a repo
func (r *RepoConfig) GetName() string {
	if r.Name != "" {
		return r.Name
	}
	return filepath.Base(r.Path)
}

// GetBeadsPath returns the effective beads directory path
func (r *RepoConfig) GetBeadsPath() string {
	if r.BeadsPath != "" {
		return r.BeadsPath
	}
	return ".beads"
}

// IsEnabled returns whether the repo is enabled
func (r *RepoConfig) IsEnabled() bool {
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

// LoadConfig loads a workspace configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if len(bytes.TrimSpace(data)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&config); err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("parsing workspace config: %w", err)
			}
		} else {
			var extra yaml.Node
			if err := dec.Decode(&extra); err != nil {
				if !errors.Is(err, io.EOF) {
					return nil, fmt.Errorf("parsing workspace config: %w", err)
				}
			} else {
				return nil, fmt.Errorf("parsing workspace config: line %d: multiple YAML documents are not supported", extra.Line)
			}
		}
	}

	// Apply defaults
	if config.Discovery.Enabled {
		if len(config.Discovery.Patterns) == 0 {
			config.Discovery.Patterns = DefaultDiscoveryPatterns()
		}
		if len(config.Discovery.Exclude) == 0 {
			config.Discovery.Exclude = DefaultExcludePatterns()
		}
		if config.Discovery.MaxDepth == 0 {
			config.Discovery.MaxDepth = 2
		}
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace config: %w", err)
	}

	return &config, nil
}

// FindWorkspaceConfig searches for .bv/workspace.yaml starting from dir,
// stopping before filesystem roots, temp-directory roots (os.TempDir(), /tmp,
// /var/tmp), and any absolute GIT_CEILING_DIRECTORIES boundaries (including
// symlink-aware boundary crossings).
func FindWorkspaceConfig(dir string) (string, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Clean(filepath.Dir(dir))
	}

	canonicalPath := func(p string) string {
		clean := filepath.Clean(p)
		if eval, err := filepath.EvalSymlinks(clean); err == nil {
			return filepath.Clean(eval)
		}
		return clean
	}

	tempRoots := make(map[string]bool)
	stopDirs := make(map[string]bool)
	addTempRoot := func(p string) {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return
		}
		clean := filepath.Clean(trimmed)
		tempRoots[clean] = true
		stopDirs[clean] = true
		if abs, err := filepath.Abs(clean); err == nil {
			absClean := filepath.Clean(abs)
			tempRoots[absClean] = true
			stopDirs[absClean] = true
			realClean := canonicalPath(absClean)
			tempRoots[realClean] = true
			stopDirs[realClean] = true
		}
		realClean := canonicalPath(clean)
		tempRoots[realClean] = true
		stopDirs[realClean] = true
	}
	for _, tmpRoot := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		addTempRoot(tmpRoot)
	}
	for _, ceiling := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		trimmed := strings.TrimSpace(ceiling)
		if trimmed == "" || !filepath.IsAbs(trimmed) {
			// Per git(1), non-absolute GIT_CEILING_DIRECTORIES entries are ignored.
			continue
		}
		clean := filepath.Clean(trimmed)
		stopDirs[clean] = true
		stopDirs[canonicalPath(clean)] = true
	}

	stopBoundaryList := make([]string, 0, len(stopDirs))
	for b := range stopDirs {
		stopBoundaryList = append(stopBoundaryList, b)
	}

	strictlyInside := func(path, boundary string) bool {
		if path == boundary {
			return false
		}
		if filepath.Dir(boundary) == boundary {
			return strings.HasPrefix(path, boundary)
		}
		return strings.HasPrefix(path, boundary+string(filepath.Separator))
	}

	curReal := canonicalPath(dir)
	if filepath.Dir(dir) == dir || filepath.Dir(curReal) == curReal || tempRoots[dir] || tempRoots[curReal] {
		return "", os.ErrNotExist
	}

	// Walk up the directory tree looking for .bv/workspace.yaml
	for {
		candidate := filepath.Join(dir, ".bv", "workspace.yaml")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Clean(filepath.Dir(dir))
		if parent == dir {
			break
		}
		parentReal := canonicalPath(parent)
		if parentReal == curReal ||
			filepath.Dir(parent) == parent ||
			filepath.Dir(parentReal) == parentReal ||
			stopDirs[dir] || stopDirs[curReal] ||
			stopDirs[parent] || stopDirs[parentReal] {
			break
		}
		crossed := false
		for _, boundary := range stopBoundaryList {
			if (strictlyInside(dir, boundary) && !strictlyInside(parent, boundary)) ||
				(strictlyInside(curReal, boundary) && !strictlyInside(parentReal, boundary)) {
				crossed = true
				break
			}
		}
		if crossed {
			break
		}
		dir = parent
		curReal = parentReal
	}

	return "", os.ErrNotExist
}

// DefaultConfig returns a sensible default configuration for a single-repo workspace
func DefaultConfig() Config {
	return Config{
		Repos: []RepoConfig{
			{
				Path:      ".",
				BeadsPath: ".beads",
			},
		},
	}
}

// ExampleConfig returns an example multi-repo workspace configuration
func ExampleConfig() Config {
	enabled := true
	return Config{
		Name: "my-workspace",
		Repos: []RepoConfig{
			{
				Name:    "api",
				Path:    "services/api",
				Prefix:  "api-",
				Enabled: &enabled,
			},
			{
				Name:    "web",
				Path:    "apps/web",
				Prefix:  "web-",
				Enabled: &enabled,
			},
			{
				Name:   "shared",
				Path:   "packages/shared",
				Prefix: "lib-",
			},
		},
		Discovery: DiscoveryConfig{
			Enabled:  false,
			Patterns: DefaultDiscoveryPatterns(),
			Exclude:  DefaultExcludePatterns(),
			MaxDepth: 2,
		},
	}
}

// NamespacedID represents an issue ID with its namespace
type NamespacedID struct {
	Namespace string // The repo prefix (e.g., "api-")
	LocalID   string // The original issue ID (e.g., "AUTH-123")
}

// String returns the full namespaced ID (e.g., "api-AUTH-123")
func (n NamespacedID) String() string {
	return n.Namespace + n.LocalID
}

// ParseNamespacedID parses a namespaced ID into its components
// If no known prefix matches, returns the whole ID as LocalID with empty Namespace
func ParseNamespacedID(id string, knownPrefixes []string) NamespacedID {
	bestPrefix := ""
	for _, prefix := range knownPrefixes {
		if prefix != "" && len(prefix) > len(bestPrefix) && strings.HasPrefix(id, prefix) {
			bestPrefix = prefix
		}
	}
	if bestPrefix != "" {
		return NamespacedID{
			Namespace: bestPrefix,
			LocalID:   strings.TrimPrefix(id, bestPrefix),
		}
	}
	// No known prefix found - treat as local ID
	return NamespacedID{
		Namespace: "",
		LocalID:   id,
	}
}

// QualifyID adds a namespace prefix to a local ID
func QualifyID(localID string, prefix string) string {
	if localID == "" || prefix == "" {
		return localID
	}
	if strings.HasPrefix(localID, prefix) {
		return localID // Already qualified
	}
	return prefix + localID
}

// UnqualifyID removes a namespace prefix from a namespaced ID
func UnqualifyID(namespacedID string, prefix string) string {
	return strings.TrimPrefix(namespacedID, prefix)
}

// IDResolver provides methods for resolving IDs across repos
type IDResolver struct {
	prefixes      []string          // All known prefixes
	prefixToRepo  map[string]string // prefix -> repo name mapping
	currentPrefix string            // Current repo's prefix for default resolution
}

// NewIDResolver creates an ID resolver from a workspace config
func NewIDResolver(config *Config, currentRepoName string) *IDResolver {
	resolver := &IDResolver{
		prefixToRepo: make(map[string]string),
	}

	for _, repo := range config.Repos {
		if !repo.IsEnabled() {
			continue
		}
		prefix := repo.GetPrefix()
		resolver.prefixes = append(resolver.prefixes, prefix)
		resolver.prefixToRepo[prefix] = repo.GetName()

		if repo.GetName() == currentRepoName || repo.Path == currentRepoName {
			resolver.currentPrefix = prefix
		}
	}

	return resolver
}

// Resolve parses an ID and returns the namespace info
func (r *IDResolver) Resolve(id string) NamespacedID {
	return ParseNamespacedID(id, r.prefixes)
}

// Qualify adds the current repo's prefix to a local ID
func (r *IDResolver) Qualify(localID string) string {
	return QualifyID(localID, r.currentPrefix)
}

// RepoForPrefix returns the repo name for a given prefix
func (r *IDResolver) RepoForPrefix(prefix string) string {
	return r.prefixToRepo[prefix]
}

// CurrentPrefix returns the current repo's prefix
func (r *IDResolver) CurrentPrefix() string {
	return r.currentPrefix
}

// Prefixes returns all known prefixes
func (r *IDResolver) Prefixes() []string {
	return r.prefixes
}

// IsCrossRepo checks if an ID references a different repo than current
func (r *IDResolver) IsCrossRepo(id string) bool {
	nsID := r.Resolve(id)
	return nsID.Namespace != "" && nsID.Namespace != r.currentPrefix
}

// DisplayID returns the ID formatted for display in current context
// If the ID is from the current repo, returns just the local ID
// If from another repo, returns the full namespaced ID
func (r *IDResolver) DisplayID(id string) string {
	nsID := r.Resolve(id)
	if nsID.Namespace == "" || nsID.Namespace == r.currentPrefix {
		return nsID.LocalID
	}
	return nsID.String()
}
