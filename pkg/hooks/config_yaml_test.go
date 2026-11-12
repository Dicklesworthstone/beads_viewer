package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestHookUnmarshalYAML(t *testing.T) {
	tests := []struct {
		name        string
		yamlData    string
		wantTimeout time.Duration
		wantErr     bool
	}{
		{
			name: "valid duration string",
			yamlData: `
name: test
command: echo
timeout: 10s
`,
			wantTimeout: 10 * time.Second,
		},
		{
			name: "valid duration string minutes",
			yamlData: `
name: test
command: echo
timeout: 1m30s
`,
			wantTimeout: 90 * time.Second,
		},
		{
			name: "numeric timeout",
			yamlData: `
name: test
command: echo
timeout: 30
`,
			wantTimeout: 30 * time.Second,
		},
		{
			name: "invalid duration",
			yamlData: `
name: test
command: echo
timeout: invalid
`,
			wantErr: true,
		},
		{
			name: "invalid numeric suffix",
			yamlData: `
name: test
command: echo
timeout: 30seconds
`,
			wantErr: true,
		},
		{
			name: "negative numeric timeout",
			yamlData: `
name: test
command: echo
timeout: -1
`,
			wantErr: true,
		},
		{
			name: "negative duration timeout",
			yamlData: `
name: test
command: echo
timeout: -1s
`,
			wantErr: true,
		},
		{
			name: "overflow numeric timeout",
			yamlData: `
name: test
command: echo
timeout: 999999999999
`,
			wantErr: true,
		},
		{
			name: "empty timeout",
			yamlData: `
name: test
command: echo
`,
			wantTimeout: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h Hook
			err := yaml.Unmarshal([]byte(tt.yamlData), &h)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalYAML() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && h.Timeout != tt.wantTimeout {
				t.Errorf("Timeout = %v, want %v", h.Timeout, tt.wantTimeout)
			}
		})
	}
}

func TestLoaderLoadResetsWarnings(t *testing.T) {
	tmpDir := t.TempDir()
	bvDir := filepath.Join(tmpDir, ".bv")
	if err := os.MkdirAll(bvDir, 0755); err != nil {
		t.Fatalf("failed to create .bv dir: %v", err)
	}
	configPath := filepath.Join(bvDir, "hooks.yaml")

	if err := os.WriteFile(configPath, []byte(`
hooks:
  pre-export:
    - command: ""
`), 0644); err != nil {
		t.Fatalf("failed to write invalid config: %v", err)
	}

	loader := NewLoader(WithProjectDir(tmpDir))
	if err := loader.Load(); err != nil {
		t.Fatalf("failed to load config with empty command: %v", err)
	}
	if warnings := strings.Join(loader.Warnings(), "\n"); !strings.Contains(warnings, "empty command") {
		t.Fatalf("expected empty-command warning, got %q", warnings)
	}

	if err := os.WriteFile(configPath, []byte(`
hooks:
  pre-export:
    - command: echo ok
`), 0644); err != nil {
		t.Fatalf("failed to write valid config: %v", err)
	}

	if err := loader.Load(); err != nil {
		t.Fatalf("failed to reload valid config: %v", err)
	}
	if warnings := loader.Warnings(); len(warnings) != 0 {
		t.Fatalf("expected warnings to reset after clean reload, got %v", warnings)
	}
}

func TestParseConfigAndLoaderKnownFields(t *testing.T) {
	tests := []struct {
		name          string
		yamlData      string
		wantErrSubstr string
		wantPreCount  int
		wantPostCount int
	}{
		{
			name:          "empty config file is valid",
			yamlData:      "",
			wantPreCount:  0,
			wantPostCount: 0,
		},
		{
			name: "comment only config file is valid",
			yamlData: `
# Comment-only .bv/hooks.yaml
   # Another comment
`,
			wantPreCount:  0,
			wantPostCount: 0,
		},
		{
			name: "valid pre-export and post-export config",
			yamlData: `
hooks:
  pre-export:
    - name: check
      command: echo pre
      timeout: 15s
      on_error: fail
      env:
        FOO: bar
  post-export:
    - name: notify
      argv: ["echo", "post"]
      timeout: 10s
      on_error: continue
`,
			wantPreCount:  1,
			wantPostCount: 1,
		},
		{
			name: "rejects unknown top-level key",
			yamlData: `
hookz:
  pre-export:
    - command: echo nope
`,
			wantErrSubstr: "field hookz not found",
		},
		{
			name: "rejects misspelled hook phase pre_exprot",
			yamlData: `
hooks:
  pre_exprot:
    - command: echo nope
`,
			wantErrSubstr: "field pre_exprot not found",
		},
		{
			name: "rejects underscore hook phase pre_export",
			yamlData: `
hooks:
  pre_export:
    - command: echo nope
`,
			wantErrSubstr: "field pre_export not found",
		},
		{
			name: "rejects hyphenated hook field on-error",
			yamlData: `
hooks:
  pre-export:
    - name: bad-on-error
      command: echo nope
      on-error: fail
`,
			wantErrSubstr: "field on-error not found",
		},
		{
			name: "rejects misspelled hook field timout",
			yamlData: `
hooks:
  pre-export:
    - name: bad-timeout
      command: echo nope
      timout: 5s
`,
			wantErrSubstr: "field timout not found",
		},
		{
			name: "rejects duplicate hook field",
			yamlData: `
hooks:
  pre-export:
    - command: echo one
      command: echo two
`,
			wantErrSubstr: "command",
		},
		{
			name: "rejects multiple YAML documents",
			yamlData: `
hooks:
  pre-export:
    - command: echo one
---
hooks:
  post-export:
    - command: echo two
`,
			wantErrSubstr: "multiple YAML documents",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			bvDir := filepath.Join(tmpDir, ".bv")
			if err := os.MkdirAll(bvDir, 0o755); err != nil {
				t.Fatalf("mkdir .bv: %v", err)
			}
			cfgPath := filepath.Join(bvDir, "hooks.yaml")
			if err := os.WriteFile(cfgPath, []byte(tt.yamlData), 0o644); err != nil {
				t.Fatalf("write hooks.yaml: %v", err)
			}

			loader := NewLoader(WithProjectDir(tmpDir))
			err := loader.Load()
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("Load() succeeded, want error containing %q", tt.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("Load() error = %q, want substring %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if got := len(loader.GetHooks(PreExport)); got != tt.wantPreCount {
				t.Fatalf("PreExport hooks = %d, want %d", got, tt.wantPreCount)
			}
			if got := len(loader.GetHooks(PostExport)); got != tt.wantPostCount {
				t.Fatalf("PostExport hooks = %d, want %d", got, tt.wantPostCount)
			}
		})
	}
}

func TestLoaderDiscoversProjectRootFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooksYAML := `
hooks:
  pre-export:
    - name: root-hook
      command: echo from-root
`
	if err := os.WriteFile(filepath.Join(root, ".bv", "hooks.yaml"), []byte(hooksYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	loader := NewLoader(WithProjectDir(subDir))
	if err := loader.Load(); err != nil {
		t.Fatalf("Load() from subdirectory failed: %v", err)
	}
	if !loader.HasHooks() || len(loader.GetHooks(PreExport)) != 1 || loader.GetHooks(PreExport)[0].Name != "root-hook" {
		t.Fatalf("expected root-hook from parent project root, got %+v", loader.GetHooks(PreExport))
	}

	// Explicit subdirectory config overrides parent discovery when present.
	if err := os.MkdirAll(filepath.Join(subDir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	subYAML := `
hooks:
  pre-export:
    - name: sub-hook
      command: echo from-sub
`
	if err := os.WriteFile(filepath.Join(subDir, ".bv", "hooks.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	subLoader := NewLoader(WithProjectDir(subDir))
	if err := subLoader.Load(); err != nil {
		t.Fatalf("Load() with explicit subDir hooks.yaml failed: %v", err)
	}
	if got := subLoader.GetHooks(PreExport); len(got) != 1 || got[0].Name != "sub-hook" {
		t.Fatalf("expected sub-hook from explicit subDir config, got %+v", got)
	}
}
