package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
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
		{
			name: "duplicate prefix case-insensitive",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Path: "api", Prefix: "App-"},
					{Path: "web", Prefix: "app-"},
				},
			},
			wantErr: true,
		},
		{
			name: "distinct prefixes with duplicate normalized source repo",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Path: "api", Prefix: "api-"},
					{Path: "web", Prefix: "api:"},
				},
			},
			wantErr: true,
		},
		{
			name: "prefix without usable source repo key",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{{Path: "api", Prefix: "-_"}},
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
	// This test asserts that no ancestor config exists. Use the platform temp
	// root, since RCH's TMPDIR can be below a source checkout with .bv config.
	// TestFindWorkspaceConfig retains real parent traversal within its fixture.
	t.Setenv("TMPDIR", "")
	t.Setenv("TMP", "")
	t.Setenv("TEMP", "")
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
		{"", "api-", ""}, // Missing optional references stay missing
		{"AUTH-123", "", "AUTH-123"},
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

func TestConfigValidateDisabledRepos(t *testing.T) {
	disabled := false
	enabled := true

	tests := []struct {
		name    string
		config  workspace.Config
		wantErr bool
	}{
		{
			name: "disabled repo with duplicate prefix passes Validate",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api-active", Path: "services/api", Prefix: "api-", Enabled: &enabled},
					{Name: "api-retired", Path: "legacy/api", Prefix: "api-", Enabled: &disabled},
				},
			},
			wantErr: false,
		},
		{
			name: "disabled repo with duplicate normalized source repo key passes Validate",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api", Path: "services/api", Prefix: "api-", Enabled: &enabled},
					{Name: "api-old", Path: "services/api-old", Prefix: "api_", Enabled: &disabled},
				},
			},
			wantErr: false,
		},
		{
			name: "disabled repo with empty path passes Validate",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api", Path: "services/api", Prefix: "api-", Enabled: &enabled},
					{Name: "retired-no-path", Path: "", Prefix: "old-", Enabled: &disabled},
				},
			},
			wantErr: false,
		},
		{
			name: "all disabled repos without discovery fails Validate",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api-retired", Path: "legacy/api", Prefix: "api-", Enabled: &disabled},
				},
			},
			wantErr: true,
		},
		{
			name: "all disabled repos with discovery enabled passes Validate",
			config: workspace.Config{
				Discovery: workspace.DiscoveryConfig{Enabled: true},
				Repos: []workspace.RepoConfig{
					{Name: "api-retired", Path: "legacy/api", Prefix: "api-", Enabled: &disabled},
				},
			},
			wantErr: false,
		},
		{
			name: "two enabled repos with duplicate prefix fails Validate",
			config: workspace.Config{
				Repos: []workspace.RepoConfig{
					{Name: "api-1", Path: "services/api1", Prefix: "api-", Enabled: &enabled},
					{Name: "api-2", Path: "services/api2", Prefix: "api-", Enabled: &enabled},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWorkspaceAndDriftConfigRejectUnknownYAMLFields(t *testing.T) {
	writeWorkspaceYAML := func(content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "workspace.yaml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unknown top-level workspace key",
			yaml: "name: mono\ndiscovery_typo: true\nrepos:\n  - path: services/api\n",
			want: "field discovery_typo not found",
		},
		{
			name: "unknown nested repo key",
			yaml: "repos:\n  - path: services/api\n    prefx: api-\n",
			want: "field prefx not found",
		},
		{
			name: "multi-document workspace YAML",
			yaml: "repos:\n  - path: services/api\n---\nrepos:\n  - path: services/web\n",
			want: "multiple YAML documents",
		},
		{
			name: "comment-only workspace YAML fails validation cleanly",
			yaml: "# comment only\n",
			want: "workspace must have at least one repo or enable discovery",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := workspace.LoadConfig(writeWorkspaceYAML(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("workspace.LoadConfig() error = %v, want %q", err, tc.want)
			}
		})
	}

	// Planted negative control: valid workspace.yaml with comments loads cleanly.
	validWS := writeWorkspaceYAML("# Workspace config\nname: mono\nrepos:\n  - name: api\n    path: services/api\n    prefix: api-\n")
	wsCfg, err := workspace.LoadConfig(validWS)
	if err != nil {
		t.Fatalf("valid workspace.yaml failed: %v", err)
	}
	if wsCfg.Name != "mono" || len(wsCfg.Repos) != 1 || wsCfg.Repos[0].Prefix != "api-" {
		t.Fatalf("unexpected workspace.Config: %+v", wsCfg)
	}

	// Also verify drift.LoadConfig rejects unknown YAML keys & multi-document streams while accepting comment-only files.
	driftDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(driftDir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	driftFile := filepath.Join(driftDir, ".bv", "drift.yaml")
	if err := os.WriteFile(driftFile, []byte("unknown_drift_key: 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := drift.LoadConfig(driftDir); err == nil || !strings.Contains(err.Error(), "field unknown_drift_key not found") {
		t.Fatalf("drift.LoadConfig() unknown field error = %v, want field not found", err)
	}
	if err := os.WriteFile(driftFile, []byte("# comment only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := drift.LoadConfig(driftDir); err != nil {
		t.Fatalf("drift.LoadConfig() comment-only failed: %v", err)
	}
}

func TestAggregateLoader_JSONLOnlySubRepoPopulatesOriginWorkingDirectory(t *testing.T) {
	wsRoot := t.TempDir()
	apiDir := filepath.Join(wsRoot, "services", "api")
	apiBeads := filepath.Join(apiDir, ".beads")
	if err := os.MkdirAll(apiBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	// JSONL-only sub-repo (no metadata.json, no beads.db).
	if err := os.WriteFile(filepath.Join(apiBeads, "issues.jsonl"), []byte(`{"id":"bv-1","title":"Sub-repo task","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantApiDir := apiDir
	if eval, err := filepath.EvalSymlinks(apiDir); err == nil {
		wantApiDir = filepath.Clean(eval)
	}

	cfg := &workspace.Config{
		Name: "mono",
		Repos: []workspace.RepoConfig{
			{Name: "api", Path: "services/api", Prefix: "api-"},
		},
	}
	agg := workspace.NewAggregateLoader(cfg, wsRoot)
	issues, _, err := agg.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("AggregateLoader.LoadAll failed: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("loaded %d issues, want 1", len(issues))
	}
	if issues[0].Origin == nil {
		t.Fatal("expected non-nil IssueOrigin on workspace issue")
	}
	if issues[0].Origin.LocalID != "bv-1" {
		t.Fatalf("Origin.LocalID = %q, want %q", issues[0].Origin.LocalID, "bv-1")
	}
	if issues[0].Origin.WorkingDirectory != wantApiDir {
		t.Fatalf("Origin.WorkingDirectory = %q, want %q", issues[0].Origin.WorkingDirectory, wantApiDir)
	}
	actions := issues[0].Actions(true)
	if actions.Show != nil || actions.Claim != nil || actions.UnavailableReason == "" {
		t.Fatalf("JSONL-only workspace sub-repo must remain read-only for CLI actions: %+v", actions)
	}

	// [R2-A1]: Custom non-.beads beads_path ("trackers/beads") and external .beads/redirect both resolve Origin.WorkingDirectory to the sub-repo root.
	customDir := filepath.Join(wsRoot, "services", "custom")
	customTracker := filepath.Join(customDir, "trackers", "beads")
	if err := os.MkdirAll(customTracker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customTracker, "issues.jsonl"), []byte(`{"id":"c-1","title":"Custom tracker","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	redirRepoDir := filepath.Join(wsRoot, "services", "redir")
	redirBeads := filepath.Join(redirRepoDir, ".beads")
	centralStore := filepath.Join(wsRoot, "central-stores", "redir", ".beads")
	if err := os.MkdirAll(redirBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(centralStore, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirBeads, "redirect"), []byte(centralStore+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(centralStore, "issues.jsonl"), []byte(`{"id":"r-1","title":"Redirected tracker","status":"open","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg2 := &workspace.Config{
		Name: "mono2",
		Repos: []workspace.RepoConfig{
			{Name: "custom", Path: "services/custom", BeadsPath: "trackers/beads", Prefix: "cust-"},
			{Name: "redir", Path: "services/redir", Prefix: "red-"},
		},
	}
	issues2, _, err := workspace.NewAggregateLoader(cfg2, wsRoot).LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll(cfg2) failed: %v", err)
	}
	wantCustom, _ := filepath.EvalSymlinks(customDir)
	wantRedir, _ := filepath.EvalSymlinks(redirRepoDir)
	gotDirs := map[string]string{}
	for _, iss := range issues2 {
		if iss.Origin != nil {
			gotDirs[iss.ID] = iss.Origin.WorkingDirectory
		}
	}
	if gotDirs["cust-c-1"] != filepath.Clean(wantCustom) {
		t.Fatalf("custom beads_path Origin.WorkingDirectory = %q, want %q", gotDirs["cust-c-1"], filepath.Clean(wantCustom))
	}
	if gotDirs["red-r-1"] != filepath.Clean(wantRedir) {
		t.Fatalf("redirected .beads Origin.WorkingDirectory = %q, want %q", gotDirs["red-r-1"], filepath.Clean(wantRedir))
	}
}

func TestFindWorkspaceConfig_RespectsCeilingAndTempRootBoundaries(t *testing.T) {
	writeWS := func(root string) string {
		t.Helper()
		cfgPath := filepath.Join(root, ".bv", "workspace.yaml")
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, []byte("discovery:\n  enabled: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return cfgPath
	}

	// 1. GIT_CEILING_DIRECTORIES boundary: parent .bv/workspace.yaml at or above ceiling must NOT be returned.
	outer := t.TempDir()
	outerCfg := writeWS(outer)
	ceilingDir := filepath.Join(outer, "ceiling")
	ceilingCfg := writeWS(ceilingDir)
	workSub := filepath.Join(ceilingDir, "project", "pkg")
	if err := os.MkdirAll(workSub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_CEILING_DIRECTORIES", ceilingDir)
	if got, err := workspace.FindWorkspaceConfig(workSub); !os.IsNotExist(err) {
		t.Fatalf("FindWorkspaceConfig crossed GIT_CEILING_DIRECTORIES (%s): got %q (outer=%q ceiling=%q), err=%v", ceilingDir, got, outerCfg, ceilingCfg, err)
	}

	// Positive observable: a .bv/workspace.yaml strictly inside the ceiling IS discovered!
	innerProject := filepath.Join(ceilingDir, "project")
	wantInnerCfg := writeWS(innerProject)
	gotInner, err := workspace.FindWorkspaceConfig(workSub)
	if err != nil {
		t.Fatalf("FindWorkspaceConfig within ceiling failed: %v", err)
	}
	if filepath.Clean(gotInner) != filepath.Clean(wantInnerCfg) {
		t.Fatalf("FindWorkspaceConfig within ceiling = %q, want %q", gotInner, wantInnerCfg)
	}

	// 2. os.TempDir() boundary: when TMPDIR sits below an ancestor with .bv/workspace.yaml (or itself contains .bv/workspace.yaml),
	// walking upward from inside TMPDIR/child must stop BEFORE TMPDIR, and starting at TMPDIR itself must return os.ErrNotExist.
	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	hostWithWS := t.TempDir()
	_ = writeWS(hostWithWS)
	customTmpRoot := filepath.Join(hostWithWS, "custom-tmp")
	_ = writeWS(customTmpRoot)
	nestedScratch := filepath.Join(customTmpRoot, "scratch-job", "sub")
	if err := os.MkdirAll(nestedScratch, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", customTmpRoot)
	t.Setenv("TMP", customTmpRoot)
	t.Setenv("TEMP", customTmpRoot)

	if got, err := workspace.FindWorkspaceConfig(nestedScratch); !os.IsNotExist(err) {
		t.Fatalf("FindWorkspaceConfig walked into/past os.TempDir() (%s): got %q, err=%v", customTmpRoot, got, err)
	}
	if got, err := workspace.FindWorkspaceConfig(customTmpRoot); !os.IsNotExist(err) {
		t.Fatalf("FindWorkspaceConfig at os.TempDir() root (%s) should return os.ErrNotExist, got %q, err=%v", customTmpRoot, got, err)
	}

	// 3. Relative entries in GIT_CEILING_DIRECTORIES must be ignored per git(1), and a regular-file start path normalizes to its parent dir.
	t.Setenv("TMPDIR", "")
	t.Setenv("TMP", "")
	t.Setenv("TEMP", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", strings.Join([]string{".", "services", "../relative"}, string(os.PathListSeparator)))
	startFile := filepath.Join(workSub, "handler.go")
	if err := os.WriteFile(startFile, []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workSub)
	gotFromFile, err := workspace.FindWorkspaceConfig(startFile)
	if err != nil || filepath.Clean(gotFromFile) != filepath.Clean(wantInnerCfg) {
		t.Fatalf("FindWorkspaceConfig(file %s) with relative GIT_CEILING_DIRECTORIES = (%q, %v), want %q", startFile, gotFromFile, err, wantInnerCfg)
	}

	// 4. Symlink escape across GIT_CEILING_DIRECTORIES and os.TempDir() must be blocked!
	escapeOuter := t.TempDir()
	_ = writeWS(escapeOuter)
	escapeCeiling := filepath.Join(escapeOuter, "jailed-ceiling")
	escapeLeaf := filepath.Join(escapeCeiling, "sub", "leaf")
	if err := os.MkdirAll(escapeLeaf, 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlink inside escapeOuter pointing into escapeCeiling/sub: lexical parent of escape_link is escapeOuter!
	escapeLink := filepath.Join(escapeOuter, "escape_link")
	if err := os.Symlink(filepath.Join(escapeCeiling, "sub"), escapeLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", escapeCeiling)
	if got, err := workspace.FindWorkspaceConfig(filepath.Join(escapeLink, "leaf")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape crossed canonical GIT_CEILING_DIRECTORIES (%s) into outer workspace: got %q, err=%v", escapeCeiling, got, err)
	}

	// Same symlink escape across os.TempDir():
	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	tmpEscapeLink := filepath.Join(hostWithWS, "tmp_escape_link")
	if err := os.Symlink(filepath.Join(customTmpRoot, "scratch-job"), tmpEscapeLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", customTmpRoot)
	t.Setenv("TMP", customTmpRoot)
	t.Setenv("TEMP", customTmpRoot)
	if got, err := workspace.FindWorkspaceConfig(filepath.Join(tmpEscapeLink, "sub")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape crossed canonical os.TempDir() (%s) into host workspace: got %q, err=%v", customTmpRoot, got, err)
	}
}
