package version

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestIsUsableVersion(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{" ", false},
		{"v", false},
		{"v.", false},
		{"  v  ", false},
		{"v0.14.4", true},
		{"0.14.4", true},
		{"v1.0.0", true},
		{"1.0.0-rc1", true},
		{"v0.14.5-0.20260212-abcdef", true}, // pseudo-version is still "usable" at this layer
		// Explicit ldflags labels take precedence over automatic module filtering.
		{"v0.0.0-20260906065919-0eb30ee2824b", true},
		{"v1.2.3-rc.1.0.20260906065919-0eb30ee2824b", true},
		{"v0.23.0+dirty", true},
		// Un-substituted release-pipeline templates must be rejected (#174) so
		// resolution falls through to build-info / the hardcoded fallback
		// instead of accepting a literal placeholder.
		{"${version}", false},
		{"v${version}", false},
		{"{{.Version}}", false},
		{"v{{.Version}}", false},
		{" v${version} ", false},
	}
	for _, tt := range tests {
		if got := isUsableVersion(tt.input); got != tt.want {
			t.Errorf("isUsableVersion(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"0.14.4", "v0.14.4"},
		{"v0.14.4", "v0.14.4"},
		{" v0.14.4 ", "v0.14.4"},
		{" 1.0.0 ", "v1.0.0"},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.input); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReleaseModuleVersion(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"no base tag", "v0.0.0-20260906065919-0eb30ee2824b", ""},
		{"no base major v2", "v2.0.0-20260906065919-0eb30ee2824b", ""},
		{"release base", "v1.2.4-0.20260906065919-0eb30ee2824b", ""},
		{"incompatible release base", "v2.2.4-0.20260906065919-0eb30ee2824b+incompatible", ""},
		{"prerelease base", "v1.2.3-rc.1.0.20260906065919-0eb30ee2824b", ""},
		{"incompatible prerelease base", "v2.2.3-rc.1.0.20260906065919-0eb30ee2824b+incompatible", ""},
		{"hyphenated prerelease base", "v1.2.3-pre-release.0.20260906065919-0eb30ee2824b", ""},
		{"svn revision", "v1.2.4-0.20260906065919-000000000042", ""},
		{"zero pseudo-version", "v0.0.0-00010101000000-000000000000", ""},
		{"empty", "", ""},
		{"devel", "(devel)", ""},
		{"dirty release", "v0.23.0+dirty", ""},
		{"dirty prerelease", "v1.2.3-rc.1+dirty", ""},
		{"dirty pseudo-version", "v0.0.0-20260906065919-0eb30ee2824b+dirty", ""},
		{"release", "v0.23.0", "v0.23.0"},
		{"unprefixed release", "0.23.0", "v0.23.0"},
		{"incompatible release", "v2.3.0+incompatible", "v2.3.0+incompatible"},
		{"alpha", "v1.2.3-alpha", "v1.2.3-alpha"},
		{"beta", "v1.2.3-beta.2", "v1.2.3-beta.2"},
		{"release candidate", "v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"zero prerelease", "v1.2.3-0.alpha", "v1.2.3-0.alpha"},
		{"short numeric prerelease", "v1.2.3-0.20260906-abcdef", "v1.2.3-0.20260906-abcdef"},
		{"timestamp without revision", "v1.2.3-0.20260906065919", "v1.2.3-0.20260906065919"},
		{"timestamp with other prefix", "v1.2.3-rc.20260906065919-abcdef", "v1.2.3-rc.20260906065919-abcdef"},
		{"release build metadata", "v1.2.3+build.42", "v1.2.3+build.42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := releaseModuleVersion(tt.input); got != tt.want {
				t.Fatalf("releaseModuleVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestVersionIsNeverEmpty(t *testing.T) {
	// After init() runs, Version must never be empty.
	if Version == "" {
		t.Fatal("Version is empty after init(); this is the #126 bug")
	}
}

func TestFallbackHasVPrefix(t *testing.T) {
	if fallback[0] != 'v' {
		t.Errorf("fallback constant %q must start with 'v'", fallback)
	}
}

func TestReleaseVersionSourcesStayAligned(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate version test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))

	flake, err := os.ReadFile(filepath.Join(repoRoot, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	match := regexp.MustCompile(`(?m)^\s*version = "([^"]+)";`).FindSubmatch(flake)
	if len(match) != 2 {
		t.Fatal("flake.nix must declare a literal package version")
	}
	if got, want := string(match[1]), strings.TrimPrefix(fallback, "v"); got != want {
		t.Fatalf("flake.nix version %q does not match fallback %q", got, want)
	}

	goreleaser, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	const injection = "pkg/version.version=v{{.Version}}"
	if !strings.Contains(string(goreleaser), injection) {
		t.Fatalf("GoReleaser must inject the validated version variable with %q", injection)
	}
}
