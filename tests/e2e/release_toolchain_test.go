package main_test

import (
	"fmt"
	goversion "go/version"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type goWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Env   map[string]string `yaml:"env"`
		Steps []struct {
			Uses string            `yaml:"uses"`
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// These workflows remain inputs to local DSR execution. An old setup-go pin
// must not appear to pass merely because Go automatically downloads another
// toolchain, which caused the release-build drift in GH #215.
func TestReleaseWorkflowsSelectDeclaredToolchain(t *testing.T) {
	root := findRepoRoot(t)
	want := declaredReleaseToolchain(t, root)
	workflowDir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if entry.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			count, err := validateGoWorkflow(data, want)
			if err != nil {
				t.Fatal(err)
			}
			seen[name] = count > 0
		})
	}
	for _, name := range []string{"ci.yml", "release.yml", "fuzz.yml"} {
		if !seen[name] {
			t.Errorf("%s must explicitly select and verify the declared Go toolchain", name)
		}
	}
}

func declaredReleaseToolchain(t *testing.T, root string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := releaseToolchainFromMod(mod)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// setup-go's go-version-file selects the toolchain directive when present,
// otherwise the go directive. Both forms must select an exact patched release.
func releaseToolchainFromMod(mod []byte) (string, error) {
	var minimum, toolchain string
	for _, line := range strings.Split(string(mod), "\n") {
		fields := strings.Fields(strings.SplitN(line, "//", 2)[0])
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "go":
			if len(fields) != 2 || minimum != "" {
				return "", fmt.Errorf("go.mod must have exactly one go directive")
			}
			minimum = fields[1]
		case "toolchain":
			if len(fields) != 2 || toolchain != "" {
				return "", fmt.Errorf("go.mod must have at most one toolchain directive")
			}
			toolchain = fields[1]
		}
	}
	if !goversion.IsValid("go" + minimum) {
		return "", fmt.Errorf("go.mod must declare a valid minimum Go version")
	}
	want := minimum
	if toolchain != "" {
		if !strings.HasPrefix(toolchain, "go") {
			return "", fmt.Errorf("go.mod toolchain must name an exact Go release")
		}
		want = strings.TrimPrefix(toolchain, "go")
	}
	if !regexp.MustCompile(`^1\.[0-9]+\.[0-9]+$`).MatchString(want) || goversion.Compare("go"+want, "go1.26.11") < 0 {
		return "", fmt.Errorf("release toolchain must be a full patch release at least Go 1.26.11, got %q", want)
	}
	if strings.HasPrefix(want, "1.27.") && goversion.Compare("go"+want, "go1.27.4") < 0 {
		return "", fmt.Errorf("Go 1.27 release toolchain must include the security fixes in 1.27.4, got %q", want)
	}
	if goversion.Compare("go"+want, "go"+minimum) < 0 {
		return "", fmt.Errorf("release toolchain %s is older than the go.mod minimum %s", want, minimum)
	}
	return want, nil
}

func TestReleaseToolchainDirectiveSelection(t *testing.T) {
	for _, tc := range []struct {
		name, mod, want string
	}{
		{"go directive", "go 1.26.11\n", "1.26.11"},
		{"toolchain directive", "go 1.26.0\ntoolchain go1.26.11\n", "1.26.11"},
		{"newer patch", "go 1.26.12 // security update\n", "1.26.12"},
		{"patched newer minor", "go 1.27.4\n", "1.27.4"},
		{"vulnerable newer minor", "go 1.27.0\n", ""},
		{"vulnerable newer patch", "go 1.27.3\n", ""},
		{"old patch", "go 1.26.10\n", ""},
		{"floating minor", "go 1.26\n", ""},
		{"old toolchain", "go 1.26.0\ntoolchain go1.26.10\n", ""},
		{"floating toolchain", "go 1.26.0\ntoolchain go1.26\n", ""},
		{"default toolchain", "go 1.26.11\ntoolchain default\n", ""},
		{"toolchain below minimum", "go 1.26.12\ntoolchain go1.26.11\n", ""},
		{"missing go directive", "toolchain go1.26.11\n", ""},
		{"duplicate go directive", "go 1.26.11\ngo 1.26.12\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := releaseToolchainFromMod([]byte(tc.mod))
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted invalid release toolchain %q", tc.mod)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got toolchain %q, err=%v; want %q", got, err, tc.want)
			}
		})
	}
}

func validateGoWorkflow(data []byte, want string) (int, error) {
	var workflow goWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return 0, fmt.Errorf("parse workflow: %w", err)
	}
	setups := 0
	for jobName, job := range workflow.Jobs {
		usesGo := false
		for index, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/setup-go@") {
				continue
			}
			usesGo = true
			setups++
			literal, file := step.With["go-version"], step.With["go-version-file"]
			if !((literal == want && file == "") || (literal == "" && file == "go.mod")) {
				return setups, fmt.Errorf("job %s must select Go %s literally or through go.mod, got %#v", jobName, want, step.With)
			}
			if index+1 >= len(job.Steps) || strings.TrimSpace(job.Steps[index+1].Run) != "bash scripts/release_gate.sh verify-toolchain" {
				return setups, fmt.Errorf("job %s must verify the installed toolchain immediately after setup", jobName)
			}
		}
		if !usesGo {
			continue
		}
		for key, expected := range map[string]string{"GOTOOLCHAIN": "local", "GOWORK": "off"} {
			effective := workflow.Env[key]
			if value, ok := job.Env[key]; ok {
				effective = value
			}
			if effective != expected {
				return setups, fmt.Errorf("job %s must inherit or set %s=%s explicitly, got %q", jobName, key, expected, effective)
			}
			for index, step := range job.Steps {
				if value, ok := step.Env[key]; ok && value != expected {
					return setups, fmt.Errorf("job %s step %d overrides %s with %q", jobName, index, key, value)
				}
			}
		}
	}
	return setups, nil
}

func TestReleaseWorkflowRejectsToolchainDrift(t *testing.T) {
	const valid = `env:
  GOTOOLCHAIN: local
  GOWORK: 'off'
jobs:
  test:
    steps:
      - uses: actions/setup-go@0123456789012345678901234567890123456789
        with:
          go-version-file: go.mod
      - run: bash scripts/release_gate.sh verify-toolchain
      - run: go test ./...
`
	for name, data := range map[string]string{
		"file pin and workflow env": valid,
		"literal patch pin":         strings.Replace(valid, "go-version-file: go.mod", "go-version: '1.26.11'", 1),
		"job env":                   strings.Replace(valid, "env:\n  GOTOOLCHAIN: local\n  GOWORK: 'off'\njobs:\n  test:", "jobs:\n  test:\n    env:\n      GOTOOLCHAIN: local\n      GOWORK: 'off'", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if count, err := validateGoWorkflow([]byte(data), "1.26.11"); err != nil || count != 1 {
				t.Fatalf("valid exact toolchain rejected: setups=%d err=%v", count, err)
			}
		})
	}
	for _, tc := range []struct {
		name, old, replacement string
	}{
		{"old patch", "go-version-file: go.mod", "go-version: '1.26.10'"},
		{"floating minor", "go-version-file: go.mod", "go-version: '1.26'"},
		{"missing pin", "go-version-file: go.mod", "cache: true"},
		{"different version file", "go-version-file: go.mod", "go-version-file: go.work"},
		{"literal overrides file", "go-version-file: go.mod", "go-version-file: go.mod\n          go-version: '1.26.10'"},
		{"automatic selection", "GOTOOLCHAIN: local", "GOTOOLCHAIN: auto"},
		{"workspace override", "GOWORK: 'off'", "GOWORK: ../go.work"},
		{"job override", "  test:\n    steps:", "  test:\n    env:\n      GOTOOLCHAIN: auto\n    steps:"},
		{"step override", "- run: go test ./...", "- run: go test ./...\n        env:\n          GOTOOLCHAIN: auto"},
		{"step workspace override", "- run: go test ./...", "- run: go test ./...\n        env:\n          GOWORK: ../go.work"},
		{"missing verification", "bash scripts/release_gate.sh verify-toolchain", "go version"},
		{"late verification", "- run: bash scripts/release_gate.sh verify-toolchain", "- run: go test ./...\n      - run: bash scripts/release_gate.sh verify-toolchain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(valid, tc.old, tc.replacement, 1)
			if _, err := validateGoWorkflow([]byte(changed), "1.26.11"); err == nil {
				t.Fatalf("accepted workflow regression %q", tc.name)
			}
		})
	}
}

// This checks declaration drift, not Nix evaluation or a native Nix build.
// The flake separately checks the actual compiler and installed binary.
func TestReleaseNixPinsMatchSource(t *testing.T) {
	root := findRepoRoot(t)
	flake, err := os.ReadFile(filepath.Join(root, "flake.nix"))
	if err != nil {
		t.Fatal(err)
	}
	wantGo := declaredReleaseToolchain(t, root)
	goPins := regexp.MustCompile(`(?m)^\s*version = "(1\.[0-9]+\.[0-9]+)";$`).FindAllStringSubmatch(string(flake), -1)
	sourcePins := regexp.MustCompile(`https://go\.dev/dl/go(1\.[0-9]+\.[0-9]+)\.src\.tar\.gz`).FindAllStringSubmatch(string(flake), -1)
	if len(goPins) != 1 || goPins[0][1] != wantGo || len(sourcePins) != 1 || sourcePins[0][1] != wantGo {
		t.Fatalf("Nix compiler and source must pin the go.mod toolchain %s, got version=%v source=%v", wantGo, goPins, sourcePins)
	}
	source, err := os.ReadFile(filepath.Join(root, "pkg", "version", "version.go"))
	if err != nil {
		t.Fatal(err)
	}
	fallback := regexp.MustCompile(`(?m)^const fallback = "v([0-9]+\.[0-9]+\.[0-9]+)"$`).FindStringSubmatch(string(source))
	release := regexp.MustCompile(`(?m)^\s*bvVersion = "([0-9]+\.[0-9]+\.[0-9]+)";$`).FindAllStringSubmatch(string(flake), -1)
	if len(fallback) != 2 || len(release) != 1 || fallback[1] != release[0][1] {
		t.Fatalf("Nix release version differs from the source fallback: Nix=%v source=%v", release, fallback)
	}
}
