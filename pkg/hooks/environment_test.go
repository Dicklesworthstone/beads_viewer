package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func hookEnvironmentValues(t *testing.T, env []string, windows bool) map[string]string {
	t.Helper()
	result := make(map[string]string, len(env))
	for _, entry := range env {
		name, value, ok := splitEnvironmentEntry(entry, windows)
		if !ok {
			t.Fatalf("malformed output environment entry %q", entry)
		}
		if windows {
			name = strings.ToUpper(name)
		}
		if _, exists := result[name]; exists {
			t.Fatalf("duplicate output environment variable %q", name)
		}
		result[name] = value
	}
	return result
}

func TestBuildHookEnvironmentCannotRecoverProtectedParentValues(t *testing.T) {
	for _, windows := range []bool{false, true} {
		t.Run(map[bool]string{false: "unix", true: "windows"}[windows], func(t *testing.T) {
			parent := []string{
				"PATH=/safe/bin", "NORMAL=ordinary", "BV_EXPORT_PATH=stale",
				"FAKE_SECRET=sentinel-secret", "GITHUB_TOKEN=sentinel-token",
				"SSH_AUTH_SOCK=sentinel-agent", "api_key=sentinel-api",
				"PASSWORD=sentinel-password", "ACCESS_KEY=sentinel-access",
				"PRIVATE_KEY=sentinel-private", "MY_CREDENTIAL=sentinel-credential",
			}
			overrides := map[string]string{
				"A": "${FAKE_SECRET}", "B": "prefix:${A}:suffix", "C": "$GITHUB_TOKEN",
				"D": "${SSH_AUTH_SOCK}", "E": "${api_key}", "F": "${PASSWORD}",
				"G": "${ACCESS_KEY}", "H": "${PRIVATE_KEY}", "I": "${MY_CREDENTIAL}",
				"GITHUB_TOKEN": "${GITHUB_TOKEN}", "J": "${GITHUB_TOKEN}",
				"NORMAL_COPY": "${NORMAL}", "OUTPUT": "${BV_EXPORT_PATH}",
			}
			env, err := buildHookEnvironment(parent, []string{"BV_EXPORT_PATH=report.md"}, overrides, nil, windows)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(env, "\n"), "sentinel-") {
				t.Fatalf("protected value leaked through inherited environment or YAML expansion: %v", env)
			}
			got := hookEnvironmentValues(t, env, windows)
			for _, key := range []string{"A", "C", "D", "E", "F", "G", "H", "I", "J", "GITHUB_TOKEN"} {
				if value, exists := got[key]; !exists || value != "" {
					t.Errorf("project alias %s = %q, present %v; want empty", key, value, exists)
				}
			}
			for key, want := range map[string]string{"B": "prefix::suffix", "NORMAL": "ordinary", "NORMAL_COPY": "ordinary", "OUTPUT": "report.md", "BV_EXPORT_PATH": "report.md", "PATH": "/safe/bin"} {
				if got[key] != want {
					t.Errorf("%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestBuildHookEnvironmentUserGrantsOnlySelectedVariables(t *testing.T) {
	for _, windows := range []bool{false, true} {
		t.Run(map[bool]string{false: "unix", true: "windows"}[windows], func(t *testing.T) {
			parent := []string{"GITHUB_TOKEN=granted-token", "FAKE_SECRET=withheld-secret", "SSH_AUTH_SOCK=withheld-agent", "NORMAL=value"}
			overrides := map[string]string{"A": "${GITHUB_TOKEN}", "B": "${A}", "C": "${FAKE_SECRET}", "D": "${SSH_AUTH_SOCK}"}
			env, err := buildHookEnvironment(parent, nil, overrides, []string{"GITHUB_TOKEN", "MISSING_SECRET", "NORMAL"}, windows)
			if err != nil {
				t.Fatal(err)
			}
			got := hookEnvironmentValues(t, env, windows)
			for _, key := range []string{"GITHUB_TOKEN", "A", "B"} {
				if got[key] != "granted-token" {
					t.Errorf("%s = %q; want granted token", key, got[key])
				}
			}
			if strings.Contains(strings.Join(env, "\n"), "withheld-") {
				t.Fatalf("an unrelated grant exposed another protected value: %v", env)
			}
			if _, exists := got["MISSING_SECRET"]; exists {
				t.Fatal("grant of absent parent variable should not fabricate a value")
			}
			if got["NORMAL"] != "value" || got["C"] != "" || got["D"] != "" {
				t.Fatalf("unexpected ordinary or withheld variables: %v", got)
			}
		})
	}
}

func TestBuildHookEnvironmentCredentialContainersRequireExactGrants(t *testing.T) {
	parent := []string{
		"DOCKER_AUTH_CONFIG=fake-docker-sentinel",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=fake-git-header-sentinel",
		"GIT_CONFIG_PARAMETERS=fake-git-parameters-sentinel",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=ordinary-config-path",
	}
	overrides := map[string]string{
		"A_DOCKER":     "${DOCKER_AUTH_CONFIG}",
		"B_COUNT":      "${GIT_CONFIG_COUNT}",
		"C_KEY":        "${GIT_CONFIG_KEY_0}",
		"D_VALUE":      "${GIT_CONFIG_VALUE_0}",
		"E_PARAMETERS": "${GIT_CONFIG_PARAMETERS}",
		"F_CHAIN":      "${D_VALUE}",
	}
	aliases := map[string]string{
		"DOCKER_AUTH_CONFIG":    "A_DOCKER",
		"GIT_CONFIG_COUNT":      "B_COUNT",
		"GIT_CONFIG_KEY_0":      "C_KEY",
		"GIT_CONFIG_VALUE_0":    "D_VALUE",
		"GIT_CONFIG_PARAMETERS": "E_PARAMETERS",
	}
	for _, windows := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			grants []string
		}{
			{name: "none"},
			{name: "docker_only", grants: []string{"DOCKER_AUTH_CONFIG"}},
			{name: "git_value_for_alias_only", grants: []string{"GIT_CONFIG_VALUE_0"}},
			{name: "git_count_is_not_a_value_grant", grants: []string{"GIT_CONFIG_COUNT"}},
			{name: "complete_indexed_git", grants: []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}},
			{name: "packed_git_only", grants: []string{"GIT_CONFIG_PARAMETERS"}},
		} {
			t.Run(map[bool]string{false: "unix", true: "windows"}[windows]+"/"+tc.name, func(t *testing.T) {
				env, err := buildHookEnvironment(parent, nil, overrides, tc.grants, windows)
				if err != nil {
					t.Fatal(err)
				}
				got := hookEnvironmentValues(t, env, windows)
				granted := make(map[string]bool, len(tc.grants))
				for _, name := range tc.grants {
					granted[name] = true
				}
				for _, entry := range parent[:5] {
					name, value, _ := strings.Cut(entry, "=")
					actual, exists := got[name]
					want := ""
					if granted[name] {
						want = value
					}
					if exists != granted[name] || actual != want {
						t.Errorf("source %s: value=%q, present=%v; want value=%q, present=%v", name, actual, exists, want, granted[name])
					}
					if got[aliases[name]] != want {
						t.Errorf("alias %s=%q, want %q", aliases[name], got[aliases[name]], want)
					}
				}
				if got["F_CHAIN"] != got["D_VALUE"] {
					t.Fatal("chained alias did not use the same filtered source")
				}
				if got["GIT_CONFIG_NOSYSTEM"] != "1" || got["GIT_CONFIG_GLOBAL"] != "ordinary-config-path" {
					t.Fatal("ordinary non-transport Git controls were removed")
				}
			})
		}
	}
}

func TestBuildHookEnvironmentCredentialContainerCaseFolding(t *testing.T) {
	for _, windows := range []bool{false, true} {
		parent := []string{"Docker_Auth_Config=docker-sentinel", "Git_Config_Value_12=git-sentinel"}
		overrides := map[string]string{"A": "${DOCKER_AUTH_CONFIG}", "B": "${GIT_CONFIG_VALUE_12}"}
		for _, grant := range []bool{false, true} {
			var grants []string
			if grant {
				grants = []string{"DOCKER_AUTH_CONFIG", "GIT_CONFIG_VALUE_12"}
			}
			env, err := buildHookEnvironment(parent, nil, overrides, grants, windows)
			if err != nil {
				t.Fatal(err)
			}
			got := hookEnvironmentValues(t, env, windows)
			if windows && grant {
				if got["A"] != "docker-sentinel" || got["B"] != "git-sentinel" {
					t.Fatalf("Windows exact-name grant failed across case variants: %v", got)
				}
			} else if strings.Contains(strings.Join(env, "\n"), "sentinel") {
				t.Fatalf("container grant did not follow platform name semantics (windows=%v, grant=%v): %v", windows, grant, got)
			}
		}
	}
}

func TestBuildHookEnvironmentGitTransportDoesNotLeaveOrphanCount(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable for credential transport subprocess regression")
	}
	dir := t.TempDir()
	globalConfig := filepath.Join(dir, "empty.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Read only these fake runtime values; no user or system Git config is used.
	parent := []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + globalConfig,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=bv.audit-auth",
		"GIT_CONFIG_VALUE_0=fake-indexed-sentinel",
		"GIT_CONFIG_PARAMETERS='bv.audit-auth=fake-packed-sentinel'",
	}
	for _, tc := range []struct {
		name   string
		grants []string
		want   string
	}{
		{name: "none"},
		{name: "value_only_is_not_active_config", grants: []string{"GIT_CONFIG_VALUE_0"}},
		{name: "complete_indexed", grants: []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}, want: "fake-indexed-sentinel"},
		{name: "packed", grants: []string{"GIT_CONFIG_PARAMETERS"}, want: "fake-packed-sentinel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := buildHookEnvironment(parent, nil, nil, tc.grants, runtime.GOOS == "windows")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(gitPath, "config", "--get", "bv.audit-auth")
			cmd.Dir = dir
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if tc.want == "" {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || len(output) != 0 {
					t.Fatalf("Git should report an absent key, without an orphaned config error: err=%v output=%q", err, output)
				}
			} else if err != nil || strings.TrimSpace(string(output)) != tc.want {
				t.Fatalf("explicitly granted Git config unavailable: err=%v output=%q want=%q", err, output, tc.want)
			}
		})
	}
}

func TestBuildHookEnvironmentPlatformCaseAndPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		windows   bool
		want      map[string]string
		wantCount int
	}{
		{"unix", false, map[string]string{"Path": "original", "PATH": "upper", "path": "project", "Gh_Token": "", "Alias": "", "After": "upper", "Context": "context", "Drive": ""}, 8},
		{"windows", true, map[string]string{"PATH": "project", "GH_TOKEN": "granted", "ALIAS": "granted", "AFTER": "upper", "CONTEXT": "context", "DRIVE": `C:\work`, "=C:": `C:\work`}, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := []string{"Path=original", "PATH=upper", "Gh_Token=granted", "BV_EXPORT_PATH=old", `=C:=C:\work`}
			overrides := map[string]string{"After": "${PATH}", "Alias": "${gh_token}", "Context": "${BV_EXPORT_PATH}", "Drive": "${=C:}", "path": "project"}
			env, err := buildHookEnvironment(parent, []string{"BV_EXPORT_PATH=context"}, overrides, []string{"GH_TOKEN"}, tc.windows)
			if err != nil {
				t.Fatal(err)
			}
			got := hookEnvironmentValues(t, env, tc.windows)
			for key, want := range tc.want {
				if got[key] != want {
					t.Errorf("%s = %q; want %q", key, got[key], want)
				}
			}
			if _, exists := got["Gh_Token"]; !tc.windows && exists {
				t.Fatal("Unix grant unexpectedly matched a differently cased secret name")
			}
			if len(got) != tc.wantCount {
				t.Errorf("got %d variables, want %d: %v", len(got), tc.wantCount, got)
			}
		})
	}
}

func TestBuildHookEnvironmentWindowsUnicodeCredentialFold(t *testing.T) {
	// Unicode Kelvin signs lower-case to ASCII k, but upper-casing the original
	// name leaves them unchanged. Windows lookup/dedup therefore recognizes a
	// credential name that a classifier using only the original spelling misses.
	parent := []string{
		"BV_TOKEN=kelvin-token-sentinel",
		"BV_API_KEY=kelvin-key-sentinel",
		"SSH_AUTH_SOCK=kelvin-agent-sentinel",
	}
	overrides := map[string]string{
		"TOKEN_ALIAS": "${BV_TOKEN}",
		"KEY_ALIAS":   "${BV_API_KEY}",
		"SOCK_ALIAS":  "${SSH_AUTH_SOCK}",
	}
	tests := []struct {
		name    string
		windows bool
		grants  []string
		visible bool
	}{
		{name: "windows_withheld", windows: true},
		{name: "windows_canonical_user_grants", windows: true, grants: []string{"BV_TOKEN", "BV_API_KEY", "SSH_AUTH_SOCK"}, visible: true},
		{name: "unix_distinct_names", windows: false},
		{name: "unix_ascii_grant_does_not_conflate_names", windows: false, grants: []string{"BV_TOKEN", "BV_API_KEY", "SSH_AUTH_SOCK"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := buildHookEnvironment(parent, nil, overrides, tc.grants, tc.windows)
			if err != nil {
				t.Fatal(err)
			}
			if tc.windows && !tc.visible && strings.Contains(strings.Join(env, "\n"), "sentinel") {
				t.Fatalf("Windows case folding recovered an ungranted protected value: %v", env)
			}
			got := hookEnvironmentValues(t, env, tc.windows)
			for name, value := range map[string]string{"TOKEN_ALIAS": "kelvin-token-sentinel", "KEY_ALIAS": "kelvin-key-sentinel", "SOCK_ALIAS": "kelvin-agent-sentinel"} {
				want := ""
				if tc.visible {
					want = value
				}
				if got[name] != want {
					t.Errorf("%s = %q, want %q", name, got[name], want)
				}
			}
			if !tc.windows && got["BV_TOKEN"] != "kelvin-token-sentinel" {
				t.Fatal("Unix names must retain case-sensitive, non-folding semantics")
			}
		})
	}
}

func TestBuildHookEnvironmentDuplicateInheritedValuesAndLiteralOverrides(t *testing.T) {
	for _, windows := range []bool{false, true} {
		parent := []string{"NORMAL=old", "NORMAL=last", "TEST_SECRET=old-secret", "TEST_SECRET=last-secret", "CHAIN=$TEST_SECRET"}
		overrides := map[string]string{"A": "${NORMAL}", "B": "${CHAIN}", "C": "${A}/child", "LOCAL_SECRET": "project-literal"}
		before := append([]string(nil), parent...)
		env, err := buildHookEnvironment(parent, nil, overrides, []string{"TEST_SECRET", "TEST_SECRET"}, windows)
		if err != nil {
			t.Fatal(err)
		}
		got := hookEnvironmentValues(t, env, windows)
		if got["A"] != "last" || got["C"] != "last/child" || got["TEST_SECRET"] != "last-secret" || got["LOCAL_SECRET"] != "project-literal" {
			t.Fatalf("duplicate/override precedence incorrect (windows=%v): %v", windows, got)
		}
		if got["B"] != "$TEST_SECRET" {
			t.Fatalf("expansion must not recursively interpret the resulting value: %q", got["B"])
		}
		if !reflect.DeepEqual(before, parent) || overrides["A"] != "${NORMAL}" {
			t.Fatal("environment builder mutated caller-owned input")
		}
		again, err := buildHookEnvironment(parent, nil, overrides, []string{"TEST_SECRET"}, windows)
		if err != nil || !reflect.DeepEqual(env, again) {
			t.Fatalf("environment output is not deterministic: %v, %v", env, again)
		}
	}
}

func TestBuildHookEnvironmentRejectsInvalidGrants(t *testing.T) {
	for _, name := range []string{"", "*", "GH_*", "NAME=value", "1NAME", "NAME-NAME", "NAME NAME", "NAME\n", "NAME\x00", "${NAME}", "%NAME%", "=C:", "名前"} {
		for _, windows := range []bool{false, true} {
			if env, err := buildHookEnvironment([]string{"GITHUB_TOKEN=sentinel"}, nil, nil, []string{name}, windows); err == nil || env != nil {
				t.Errorf("invalid grant %q accepted (windows=%v): %v, %v", name, windows, env, err)
			}
		}
	}
	if err := validateEnvGrants([]string{"_", "_A1", "a", "PATH", "Gh_Token", "FOO123"}); err != nil {
		t.Fatalf("valid portable variable name rejected: %v", err)
	}
	if err := validateEnvGrants([]string{"TOKEN=accidental-secret-value"}); err == nil || strings.Contains(err.Error(), "accidental-secret-value") {
		t.Fatalf("invalid assignment must fail without echoing its value: %v", err)
	}
}

func TestBuildHookEnvironmentRejectsAmbiguousOrInvalidOverrides(t *testing.T) {
	for _, overrides := range []map[string]string{{"": "value"}, {"A=B": "value"}, {"A\x00": "value"}, {"A": "value\x00tail"}, {"=C:": "value"}, {"A\xff": "value"}, {"A": "value\xff"}} {
		for _, windows := range []bool{false, true} {
			if env, err := buildHookEnvironment(nil, nil, overrides, nil, windows); err == nil || env != nil {
				t.Errorf("invalid override accepted (windows=%v): %v, %v", windows, env, err)
			}
		}
	}
	caseCollision := map[string]string{"Name": "upper", "name": "lower"}
	if env, err := buildHookEnvironment(nil, nil, caseCollision, nil, true); err == nil || env != nil {
		t.Fatalf("Windows case-colliding declarations accepted: %v, %v", env, err)
	}
	env, err := buildHookEnvironment(nil, nil, caseCollision, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	got := hookEnvironmentValues(t, env, false)
	if got["Name"] != "upper" || got["name"] != "lower" {
		t.Fatalf("distinct Unix names were conflated: %v", got)
	}
}

func TestBuildHookEnvironmentMalformedParentAndContext(t *testing.T) {
	for _, windows := range []bool{false, true} {
		env, err := buildHookEnvironment([]string{"INVALID", "=invalid", "NUL=value\x00", "GOOD=with=equals", "EMPTY="}, nil, nil, nil, windows)
		if err != nil {
			t.Fatal(err)
		}
		got := hookEnvironmentValues(t, env, windows)
		if !reflect.DeepEqual(got, map[string]string{"GOOD": "with=equals", "EMPTY": ""}) {
			t.Fatalf("malformed parent entries were retained: %v", got)
		}
		for _, contextEntry := range []string{"INVALID", "=invalid", "NUL=value\x00"} {
			if env, err := buildHookEnvironment(nil, []string{contextEntry}, nil, nil, windows); err == nil || env != nil {
				t.Errorf("invalid context entry accepted: %q", contextEntry)
			}
		}
	}
}

// This re-executes the test binary to check the actual OS child environment,
// including Windows' native case-insensitive environment block behavior.
func TestBuildHookEnvironmentSubprocess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []bool{false, true} {
		t.Run(map[bool]string{false: "withheld", true: "granted"}[grant], func(t *testing.T) {
			parent := append(os.Environ(), "BV_FAKE_SECRET=bounded-sentinel", "BV_HOOK_ENV_PROBE=1", "BV_ORDINARY=normal")
			var grants []string
			if grant {
				grants = []string{"BV_FAKE_SECRET"}
			}
			env, err := buildHookEnvironment(parent, nil, map[string]string{
				"BV_ALIAS": "${BV_FAKE_SECRET}", "BV_CHAIN": "${BV_ALIAS}",
			}, grants, runtime.GOOS == "windows")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestHookEnvironmentSubprocessProbe$")
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child environment probe failed: %v\n%s", err, output)
			}
			var got map[string]string
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("invalid probe result: %v\n%s", err, output)
			}
			want := ""
			if grant {
				want = "bounded-sentinel"
			}
			for _, key := range []string{"BV_FAKE_SECRET", "BV_ALIAS", "BV_CHAIN"} {
				if got[key] != want {
					t.Errorf("child %s = %q, want %q", key, got[key], want)
				}
			}
			if got["BV_ORDINARY"] != "normal" {
				t.Errorf("ordinary child environment lost: %v", got)
			}
		})
	}
}

func TestHookEnvironmentSubprocessProbe(t *testing.T) {
	if os.Getenv("BV_HOOK_ENV_PROBE") != "1" {
		return
	}
	values := make(map[string]string)
	for _, key := range []string{"BV_FAKE_SECRET", "BV_ALIAS", "BV_CHAIN", "BV_ORDINARY"} {
		values[key] = os.Getenv(key)
	}
	if err := json.NewEncoder(os.Stdout).Encode(values); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
