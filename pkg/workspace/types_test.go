package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"
)

func TestRepoConfigGetPrefix(t *testing.T) {
	tests := []struct {
		name     string
		repo     workspace.RepoConfig
		expected string
	}{
		{
			name:     "explicit prefix",
			repo:     workspace.RepoConfig{Path: "services/api", Prefix: "backend-"},
			expected: "backend-",
		},
		{
			name:     "from name",
			repo:     workspace.RepoConfig{Path: "services/api", Name: "API"},
			expected: "api-",
		},
		{
			name:     "from path",
			repo:     workspace.RepoConfig{Path: "services/api"},
			expected: "api-",
		},
		{
			name:     "nested path",
			repo:     workspace.RepoConfig{Path: "packages/shared/utils"},
			expected: "utils-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.repo.GetPrefix()
			if got != tt.expected {
				t.Errorf("GetPrefix() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRepoConfigGetName(t *testing.T) {
	tests := []struct {
		name     string
		repo     workspace.RepoConfig
		expected string
	}{
		{
			name:     "explicit name",
			repo:     workspace.RepoConfig{Path: "services/api", Name: "Backend API"},
			expected: "Backend API",
		},
		{
			name:     "from path",
			repo:     workspace.RepoConfig{Path: "services/api"},
			expected: "api",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.repo.GetName()
			if got != tt.expected {
				t.Errorf("GetName() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRepoConfigGetBeadsPath(t *testing.T) {
	tests := []struct {
		name     string
		repo     workspace.RepoConfig
		expected string
	}{
		{
			name:     "default",
			repo:     workspace.RepoConfig{Path: "api"},
			expected: ".beads",
		},
		{
			name:     "custom",
			repo:     workspace.RepoConfig{Path: "api", BeadsPath: "tracker"},
			expected: "tracker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.repo.GetBeadsPath()
			if got != tt.expected {
				t.Errorf("GetBeadsPath() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestRepoConfigIsEnabled(t *testing.T) {
	enabled := true
	disabled := false

	tests := []struct {
		name     string
		repo     workspace.RepoConfig
		expected bool
	}{
		{
			name:     "default (nil) is enabled",
			repo:     workspace.RepoConfig{Path: "api"},
			expected: true,
		},
		{
			name:     "explicit true",
			repo:     workspace.RepoConfig{Path: "api", Enabled: &enabled},
			expected: true,
		},
		{
			name:     "explicit false",
			repo:     workspace.RepoConfig{Path: "api", Enabled: &disabled},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.repo.IsEnabled()
			if got != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  workspace.Config
		wantErr bool
	}{
		{
			name: "valid single repo",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Path: "api"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid multiple repos",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Path: "api", Prefix: "api-"},
					{Path: "web", Prefix: "web-"},
				},
			},
			wantErr: false,
		},
		{
			name: "empty repos without discovery",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{},
			},
			wantErr: true,
		},
		{
			name: "empty repos with discovery",
			config: workspace.Config{
				Repos:     []workspace.RepoConfig{},
				Discovery: workspace.DiscoveryConfig{Enabled: true},
			},
			wantErr: false,
		},
		{
			name: "repo without path",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api"},
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate prefix",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Path: "api", Prefix: "app-"},
					{Path: "web", Prefix: "app-"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "workspace.yaml")

	// Write test config
	configContent := `
name: test-workspace
repos:
  - name: api
    path: services/api
    prefix: api-
  - name: web
    path: apps/web
    prefix: web-
discovery:
  enabled: false
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	config, err := workspace.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if config.Name != "test-workspace" {
		t.Errorf("Name = %q, want %q", config.Name, "test-workspace")
	}

	if len(config.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2", len(config.Repos))
	}

	if config.Repos[0].GetPrefix() != "api-" {
		t.Errorf("Repos[0].GetPrefix() = %q, want %q", config.Repos[0].GetPrefix(), "api-")
	}
}

func TestLoadConfigWithDiscovery(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "workspace.yaml")

	// Write test config with discovery enabled
	configContent := `
discovery:
  enabled: true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	config, err := workspace.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if !config.Discovery.Enabled {
		t.Error("Discovery.Enabled should be true")
	}

	// Should have default patterns
	if len(config.Discovery.Patterns) == 0 {
		t.Error("Discovery.Patterns should have defaults")
	}

	// Should have default excludes
	if len(config.Discovery.Exclude) == 0 {
		t.Error("Discovery.Exclude should have defaults")
	}

	// Should have default max depth
	if config.Discovery.MaxDepth != 2 {
		t.Errorf("Discovery.MaxDepth = %d, want 2", config.Discovery.MaxDepth)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "workspace.yaml")

	// Write invalid config (no repos, no discovery)
	if err := os.WriteFile(configPath, []byte("name: empty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := workspace.LoadConfig(configPath)
	if err == nil {
		t.Error("LoadConfig() should error on invalid config")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	_, err := workspace.LoadConfig("/nonexistent/path/workspace.yaml")
	if err == nil {
		t.Error("LoadConfig() should error on missing file")
	}
}

func TestFindWorkspaceConfig(t *testing.T) {
	tmpDir := t.TempDir()

	// Create .bv directory and workspace.yaml
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(bvDir, "workspace.yaml")
	if err := os.WriteFile(configPath, []byte("discovery:\n  enabled: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory to search from
	subDir := filepath.Join(tmpDir, "services", "api")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Should find the config from subdirectory
	found, err := workspace.FindWorkspaceConfig(subDir)
	if err != nil {
		t.Fatalf("FindWorkspaceConfig() error = %v", err)
	}

	if found != configPath {
		t.Errorf("FindWorkspaceConfig() = %q, want %q", found, configPath)
	}
}

func TestFindWorkspaceConfigNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := workspace.FindWorkspaceConfig(tmpDir)
	if !os.IsNotExist(err) {
		t.Errorf("FindWorkspaceConfig() error = %v, want os.ErrNotExist", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	config := workspace.DefaultConfig()

	if len(config.Repos) != 1 {
		t.Errorf("len(Repos) = %d, want 1", len(config.Repos))
	}

	if config.Repos[0].Path != "." {
		t.Errorf("Repos[0].Path = %q, want %q", config.Repos[0].Path, ".")
	}

	if err := config.Validate(); err != nil {
		t.Errorf("DefaultConfig() should be valid: %v", err)
	}
}

func TestExampleConfig(t *testing.T) {
	config := workspace.ExampleConfig()

	if config.Name == "" {
		t.Error("ExampleConfig should have a name")
	}

	if len(config.Repos) < 2 {
		t.Error("ExampleConfig should have multiple repos")
	}

	if err := config.Validate(); err != nil {
		t.Errorf("ExampleConfig() should be valid: %v", err)
	}
}

func TestDefaultDiscoveryPatterns(t *testing.T) {
	patterns := workspace.DefaultDiscoveryPatterns()
	if len(patterns) == 0 {
		t.Error("DefaultDiscoveryPatterns should not be empty")
	}

	// Should include common patterns
	hasPackages := false
	for _, p := range patterns {
		if p == "packages/*" {
			hasPackages = true
		}
	}
	if !hasPackages {
		t.Error("DefaultDiscoveryPatterns should include 'packages/*'")
	}
}

func TestDefaultExcludePatterns(t *testing.T) {
	excludes := workspace.DefaultExcludePatterns()
	if len(excludes) == 0 {
		t.Error("DefaultExcludePatterns should not be empty")
	}

	// Should include node_modules
	hasNodeModules := false
	for _, e := range excludes {
		if e == "node_modules" {
			hasNodeModules = true
		}
	}
	if !hasNodeModules {
		t.Error("DefaultExcludePatterns should include 'node_modules'")
	}
}

func TestNamespacedIDString(t *testing.T) {
	tests := []struct {
		nsID     workspace.NamespacedID
		expected string
	}{
		{workspace.NamespacedID{Namespace: "api-", LocalID: "AUTH-123"}, "api-AUTH-123"},
		{workspace.NamespacedID{Namespace: "", LocalID: "AUTH-123"}, "AUTH-123"},
		{workspace.NamespacedID{Namespace: "web-", LocalID: "UI-1"}, "web-UI-1"},
	}

	for _, tt := range tests {
		got := tt.nsID.String()
		if got != tt.expected {
			t.Errorf("String() = %q, want %q", got, tt.expected)
		}
	}
}

func TestParseNamespacedID(t *testing.T) {
	prefixes := []string{"api-", "web-", "lib-"}

	tests := []struct {
		id              string
		expectedNS      string
		expectedLocalID string
	}{
		{"api-AUTH-123", "api-", "AUTH-123"},
		{"web-UI-1", "web-", "UI-1"},
		{"lib-shared-utils", "lib-", "shared-utils"},
		{"unknown-123", "", "unknown-123"}, // No matching prefix
		{"AUTH-123", "", "AUTH-123"},       // No prefix
	}

	for _, tt := range tests {
		got := workspace.ParseNamespacedID(tt.id, prefixes)
		if got.Namespace != tt.expectedNS {
			t.Errorf("ParseNamespacedID(%q).Namespace = %q, want %q", tt.id, got.Namespace, tt.expectedNS)
		}
		if got.LocalID != tt.expectedLocalID {
			t.Errorf("ParseNamespacedID(%q).LocalID = %q, want %q", tt.id, got.LocalID, tt.expectedLocalID)
		}
	}
}

func TestQualifyID(t *testing.T) {
	tests := []struct {
		localID  string
		prefix   string
		expected string
	}{
		{"AUTH-123", "api-", "api-AUTH-123"},
		{"api-AUTH-123", "api-", "api-AUTH-123"}, // Already qualified
		{"UI-1", "web-", "web-UI-1"},
	}

	for _, tt := range tests {
		got := workspace.QualifyID(tt.localID, tt.prefix)
		if got != tt.expected {
			t.Errorf("QualifyID(%q, %q) = %q, want %q", tt.localID, tt.prefix, got, tt.expected)
		}
	}
}

func TestUnqualifyID(t *testing.T) {
	tests := []struct {
		namespacedID string
		prefix       string
		expected     string
	}{
		{"api-AUTH-123", "api-", "AUTH-123"},
		{"AUTH-123", "api-", "AUTH-123"}, // No prefix to remove
		{"web-UI-1", "web-", "UI-1"},
	}

	for _, tt := range tests {
		got := workspace.UnqualifyID(tt.namespacedID, tt.prefix)
		if got != tt.expected {
			t.Errorf("UnqualifyID(%q, %q) = %q, want %q", tt.namespacedID, tt.prefix, got, tt.expected)
		}
	}
}

func TestIDResolver(t *testing.T) {
	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "services/api", Prefix: "api-"},
			{Name: "web", Path: "apps/web", Prefix: "web-"},
			{Name: "lib", Path: "packages/lib", Prefix: "lib-"},
		},
	}

	resolver := workspace.NewIDResolver(config, "api")

	// Test CurrentPrefix
	if resolver.CurrentPrefix() != "api-" {
		t.Errorf("CurrentPrefix() = %q, want %q", resolver.CurrentPrefix(), "api-")
	}

	// Test Prefixes
	prefixes := resolver.Prefixes()
	if len(prefixes) != 3 {
		t.Errorf("len(Prefixes()) = %d, want 3", len(prefixes))
	}

	// Test Resolve
	nsID := resolver.Resolve("web-UI-123")
	if nsID.Namespace != "web-" || nsID.LocalID != "UI-123" {
		t.Errorf("Resolve failed: got %+v", nsID)
	}

	// Test Qualify
	qualified := resolver.Qualify("AUTH-123")
	if qualified != "api-AUTH-123" {
		t.Errorf("Qualify() = %q, want %q", qualified, "api-AUTH-123")
	}

	// Test RepoForPrefix
	repo := resolver.RepoForPrefix("web-")
	if repo != "web" {
		t.Errorf("RepoForPrefix() = %q, want %q", repo, "web")
	}

	// Test IsCrossRepo
	if resolver.IsCrossRepo("api-AUTH-123") {
		t.Error("IsCrossRepo should return false for current repo")
	}
	if !resolver.IsCrossRepo("web-UI-123") {
		t.Error("IsCrossRepo should return true for different repo")
	}

	// Test DisplayID
	if resolver.DisplayID("api-AUTH-123") != "AUTH-123" {
		t.Errorf("DisplayID for current repo should strip prefix")
	}
	if resolver.DisplayID("web-UI-123") != "web-UI-123" {
		t.Errorf("DisplayID for other repo should keep full ID")
	}
}

func TestIDResolverDisabledRepos(t *testing.T) {
	disabled := false
	config := &workspace.Config{
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "services/api", Prefix: "api-"},
			{Name: "web", Path: "apps/web", Prefix: "web-", Enabled: &disabled},
		},
	}

	resolver := workspace.NewIDResolver(config, "api")

	// Should only have api prefix (web is disabled)
	if len(resolver.Prefixes()) != 1 {
		t.Errorf("len(Prefixes()) = %d, want 1 (disabled repo excluded)", len(resolver.Prefixes()))
	}

	// web- should not be recognized
	nsID := resolver.Resolve("web-UI-123")
	if nsID.Namespace != "" {
		t.Error("Disabled repo prefix should not be recognized")
	}
}
