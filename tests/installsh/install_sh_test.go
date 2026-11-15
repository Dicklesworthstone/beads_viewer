// Package installsh tests the release-metadata helpers in the repository's
// install.sh by sourcing it in bash. The installer can parse GitHub release
// JSON with either python3 or jq; these tests hold both backends to the same
// results so a machine with only jq installs the same asset as one with python3.
package installsh

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const releaseJSON = `{
  "tag_name": "v9.9.9",
  "assets": [
    {"name": "bv_9.9.9_linux_amd64.tar.gz", "browser_download_url": ""},
    {"name": "bv_9.9.9_linux_amd64.tar.gz.sbom.json", "browser_download_url": "https://example.test/sbom"},
    {"name": "bv_9.9.9_linux_amd64.tar.gz", "browser_download_url": "https://example.test/linux_amd64.tar.gz"},
    {"name": "bv_9.9.9_windows_amd64.zip", "browser_download_url": "https://example.test/windows_amd64.zip"},
    {"name": "bv_9.9.9_darwinarm64.tar.gz", "browser_download_url": "https://example.test/darwinarm64.tar.gz"},
    {"name": "checksums.txt", "browser_download_url": "https://example.test/checksums.txt"}
  ]
}`

func repoInstallScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	path, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install.sh not found: %v", err)
	}
	return path
}

func bashPath(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	return bash
}

// backends returns the JSON backends usable on this machine, as shell
// snippets that pin install.sh to that backend after sourcing it.
func backends(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	if py, err := exec.LookPath("python3"); err == nil {
		out["python"] = `JSON_TOOL=python; PYTHON_CMD='` + py + `'`
	}
	if _, err := exec.LookPath("jq"); err == nil {
		out["jq"] = `JSON_TOOL=jq`
	}
	if len(out) == 0 {
		t.Skip("neither python3 nor jq available")
	}
	return out
}

// runInstallFunc sources install.sh (which does not run main when sourced),
// applies setup, then runs body. It returns stdout, stderr and the exit code.
func runInstallFunc(t *testing.T, env []string, setup, body, stdin string) (string, string, int) {
	t.Helper()
	script := `set -uo pipefail
source "$INSTALL_SH"
set +e
` + setup + `
` + body
	cmd := exec.Command(bashPath(t), "-c", script)
	cmd.Env = append([]string{"INSTALL_SH=" + repoInstallScript(t), "NO_COLOR=1", "HOME=" + t.TempDir()}, env...)
	if !hasPath(env) {
		cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run bash: %v", err)
	}
	return stdout.String(), stderr.String(), code
}

func hasPath(env []string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return true
		}
	}
	return false
}

func TestSelectReleaseAssetBackendsAgree(t *testing.T) {
	cases := []struct {
		platform string
		want     string
		code     int
	}{
		// Skips the same-named asset with an empty URL and the .sbom.json.
		{"linux_amd64", "v9.9.9\nhttps://example.test/linux_amd64.tar.gz\nbv_9.9.9_linux_amd64.tar.gz\n", 0},
		{"windows_amd64", "v9.9.9\nhttps://example.test/windows_amd64.zip\nbv_9.9.9_windows_amd64.zip\n", 0},
		// Underscore-insensitive fallback.
		{"darwin_arm64", "v9.9.9\nhttps://example.test/darwinarm64.tar.gz\nbv_9.9.9_darwinarm64.tar.gz\n", 0},
		{"freebsd_amd64", "v9.9.9\n\n\n", 1},
	}
	for name, setup := range backends(t) {
		for _, tc := range cases {
			out, stderr, code := runInstallFunc(t, nil, setup, `select_release_asset "`+tc.platform+`"`, releaseJSON)
			if out != tc.want || code != tc.code {
				t.Errorf("%s/%s: got (%q, exit %d), want (%q, exit %d); stderr=%s", name, tc.platform, out, code, tc.want, tc.code, stderr)
			}
		}
	}
}

// A machine with jq but no python must use jq; one with neither must say that
// either tool would do.
func TestEnsureJSONToolFallsBackToJQ(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not available")
	}
	onlyJQ := t.TempDir()
	if err := os.Symlink(jq, filepath.Join(onlyJQ, "jq")); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runInstallFunc(t, []string{"PATH=" + onlyJQ}, "", `ensure_json_tool && printf '%s\n' "$JSON_TOOL"`, "")
	if code != 0 || out != "jq\n" {
		t.Fatalf("got (%q, exit %d), want jq; stderr=%s", out, code, stderr)
	}

	empty := t.TempDir()
	out, stderr, code = runInstallFunc(t, []string{"PATH=" + empty}, "", `ensure_json_tool`, "")
	if code == 0 {
		t.Fatalf("ensure_json_tool succeeded with no python3 or jq on PATH: %q", out)
	}
	if !strings.Contains(stderr, "python3 or jq is required") {
		t.Errorf("error should say python3 or jq is required, got stderr=%q", stderr)
	}
}

func TestVersionGE(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21", "1.21", 0},
		{"1.22.3", "1.21", 0},
		{"1.21.0", "1.21", 0},
		{"1.20.14", "1.21", 1},
		{"2", "1.21", 0},
		{"1.9", "1.21", 1},
	}
	for _, tc := range cases {
		_, stderr, code := runInstallFunc(t, nil, "", `version_ge "`+tc.a+`" "`+tc.b+`"`, "")
		if code != tc.want {
			t.Errorf("version_ge %s %s: exit %d, want %d; stderr=%s", tc.a, tc.b, code, tc.want, stderr)
		}
	}
}

func TestSourceToolchainSecurityFloor(t *testing.T) {
	cases := []struct {
		name, module, want string
	}{
		{"go directive", "go 1.26.11\n", "go1.26.11\n"},
		{"toolchain directive", "go 1.26.0\ntoolchain go1.26.11\n", "go1.26.11\n"},
		{"older source tag", "go 1.26.0\ntoolchain go1.26.10\n", "go1.26.11\n"},
		{"older language version", "go 1.25\n", "go1.26.11\n"},
		{"vulnerable newer branch", "go 1.27.0\ntoolchain go1.27.3\n", "go1.27.4\n"},
		{"patched newer branch", "go 1.27.4\n", "go1.27.4\n"},
		{"missing", "module example.test/missing\n", ""},
		{"development compiler", "go 1.26.0\ntoolchain go1.27rc1\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			module := filepath.Join(t.TempDir(), "go.mod")
			if err := os.WriteFile(module, []byte(tc.module), 0o600); err != nil {
				t.Fatal(err)
			}
			out, stderr, code := runInstallFunc(t, []string{"MODULE_FIXTURE=" + module}, "", `source_toolchain "$MODULE_FIXTURE"`, "")
			if tc.want == "" {
				if code == 0 {
					t.Fatalf("invalid compiler declaration accepted: %q", out)
				}
			} else if code != 0 || out != tc.want {
				t.Fatalf("got (%q, exit %d), want %q; stderr=%s", out, code, tc.want, stderr)
			}
		})
	}
}

// Exercise the production source installer's promotion boundary with controlled
// compiler metadata. A reported old/missing compiler must preserve an installed
// binary, even when compilation itself returned success.
func TestSourceInstallVerifiesBuiltToolchain(t *testing.T) {
	for _, builtGo := range []string{"go1.26.11", "go1.26.10", "go1.27.3", ""} {
		t.Run("metadata="+builtGo, func(t *testing.T) {
			dir := t.TempDir()
			buildDir := filepath.Join(dir, "build with spaces")
			installDir := filepath.Join(dir, "install with spaces")
			for _, path := range []string{buildDir, installDir} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			installed := filepath.Join(installDir, "bv")
			if err := os.WriteFile(installed, []byte("existing installation\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			setup := `
ensure_go() { printf '1.26.11'; }
make_tmp_dir() { printf '%s\n' "$BUILD_FIXTURE"; }
git() { mkdir -p "$5"; printf 'module example.test/bv\ngo 1.26.11\n' > "$5/go.mod"; }
go() {
    if [ "$1" = version ] && [ "$2" = -m ]; then
        printf '%s: %s\n' "$3" "$BUILT_GO"
        return 0
    fi
    [ "$1" = build ] || return 93
    [ "$GOTOOLCHAIN" = go1.26.11 ] || return 94
    [ "$GOWORK" = off ] || return 95
    [ "$2" = -o ] || return 96
    printf 'candidate installation\n' > "$3"
}`
			out, stderr, code := runInstallFunc(t, []string{
				"BUILD_FIXTURE=" + buildDir, "INSTALL_DIR=" + installDir,
				"BUILT_GO=" + builtGo, "GOTOOLCHAIN=local",
			}, setup, `try_go_install; [ "$GOTOOLCHAIN" = local ]`, "")
			content, err := os.ReadFile(installed)
			if err != nil {
				t.Fatal(err)
			}
			if builtGo == "go1.26.11" {
				if code != 0 || string(content) != "candidate installation\n" {
					t.Fatalf("verified compiler was not installed: exit=%d, file=%q, out=%s, stderr=%s", code, content, out, stderr)
				}
			} else if code == 0 || string(content) != "existing installation\n" || !strings.Contains(stderr, "expected go1.26.11") {
				t.Fatalf("compiler mismatch did not preserve existing binary: exit=%d, file=%q, out=%s, stderr=%s", code, content, out, stderr)
			}
		})
	}
}

// go.dev's ?mode=json has no URL field (files are served from
// https://go.dev/dl/<filename>); the fixture has the real shape. Both backends
// must build the URL from the filename and pass the SHA-256 through.
func TestFetchLatestGoPkgBackendsAgree(t *testing.T) {
	goJSON := `[{"version":"go1.99rc1","stable":false,"files":[]},` +
		`{"version":"go1.98.2","stable":true,"files":[` +
		`{"filename":"go1.98.2.darwin-arm64.tar.gz","os":"darwin","arch":"arm64","sha256":"aaaa","kind":"archive"},` +
		`{"filename":"go1.98.2.darwin-arm64.pkg","os":"darwin","arch":"arm64","sha256":"bbbb","kind":"installer"},` +
		`{"filename":"go1.98.2.darwin-amd64.pkg","os":"darwin","arch":"amd64","sha256":"cccc","kind":"installer"}]}]`
	for name, backend := range backends(t) {
		setup := backend + `
uname() { echo arm64; }
curl() { printf '%s' "$GO_JSON"; }`
		out, stderr, code := runInstallFunc(t, []string{"GO_JSON=" + goJSON, "PATH=" + os.Getenv("PATH")}, setup, `fetch_latest_go_pkg`, "")
		want := "go1.98.2\nhttps://go.dev/dl/go1.98.2.darwin-arm64.pkg\nbbbb\n"
		if code != 0 || out != want {
			t.Errorf("%s: got (%q, exit %d), want %q; stderr=%s", name, out, code, want, stderr)
		}
	}
}

// install_go_from_pkg must read every line fetch_latest_go_pkg prints; it used
// to read only the first and always gave up before downloading.
func TestInstallGoFromPkgReadsVersionAndURL(t *testing.T) {
	setup := `fetch_latest_go_pkg() { printf 'go1.98.2\nhttps://example.test/arm64.pkg\nbbbb\n'; }
curl() { echo "curl $*" >&2; return 22; }`
	_, stderr, code := runInstallFunc(t, nil, setup, `install_go_from_pkg`, "")
	if code == 0 {
		t.Fatal("expected failure from the stubbed download")
	}
	if !strings.Contains(stderr, "curl -fsSL https://example.test/arm64.pkg") {
		t.Errorf("install_go_from_pkg did not try to download the pkg URL; stderr=%s", stderr)
	}
}

// The .pkg runs under sudo, so a download that does not match go.dev's SHA-256
// must never reach the installer.
func TestInstallGoFromPkgRefusesChecksumMismatch(t *testing.T) {
	setup := `fetch_latest_go_pkg() { printf 'go1.98.2\nhttps://example.test/arm64.pkg\n%064d\n' 0; }
curl() { local out=""; while [ $# -gt 0 ]; do [ "$1" = "-o" ] && out="$2"; shift; done; printf 'tampered' > "$out"; }
sudo() { echo "SUDO $*" >&2; return 0; }`
	stdout, stderr, code := runInstallFunc(t, nil, setup, `install_go_from_pkg`, "")
	if code == 0 {
		t.Fatalf("install_go_from_pkg accepted a pkg with the wrong checksum; stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.Contains(stderr, "SUDO") {
		t.Errorf("sudo installer ran on a pkg with the wrong checksum; stderr=%s", stderr)
	}
	if !strings.Contains(stdout+stderr, "checksum mismatch") {
		t.Errorf("expected a checksum mismatch error; stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestEnsureGoKeepsStdoutPureWhenInstallingViaHomebrew(t *testing.T) {
	setup := `
has_controlling_tty() { return 1; }
installed_go=0
brew() {
    echo "homebrew noisy progress line on stdout"
    installed_go=1
    return 0
}
go() {
    if [ "$installed_go" -eq 1 ]; then
        printf 'go version go1.26.11 darwin/arm64\n'
    else
        printf 'go version go1.21.0 darwin/arm64\n'
    fi
}`
	out, stderr, code := runInstallFunc(t, nil, setup, `ver=$(ensure_go); printf 'captured=%s\n' "$ver"`, "")
	if code != 0 || out != "captured=1.26.11\n" {
		t.Fatalf("ensure_go polluted stdout or failed: exit=%d, stdout=%q, stderr=%s", code, out, stderr)
	}
	for _, wantErr := range []string{"Go 1.26.11 or later is required", "homebrew noisy progress line", "Installed Go 1.26.11 via Homebrew"} {
		if !strings.Contains(stderr, wantErr) {
			t.Errorf("expected %q in stderr, got stderr=%q", wantErr, stderr)
		}
	}
}

func TestParseArgsVersionAndFromSource(t *testing.T) {
	out, stderr, code := runInstallFunc(t, nil, "", `parse_args --version 0.26.0 --from-source && printf '%s|%s\n' "$TARGET_VERSION" "$FROM_SOURCE"`, "")
	if code != 0 || out != "v0.26.0|1\n" {
		t.Fatalf("parse_args failed: exit=%d, out=%q, stderr=%s", code, out, stderr)
	}

	_, stderr, code = runInstallFunc(t, nil, "", `parse_args --version ../evil`, "")
	if code == 0 || !strings.Contains(stderr, "Invalid release version") {
		t.Fatalf("expected invalid --version rejection, got exit=%d stderr=%q", code, stderr)
	}

	_, stderr, code = runInstallFunc(t, nil, "", `parse_args --bogus-flag`, "")
	if code == 0 || !strings.Contains(stderr, "Unknown option") {
		t.Fatalf("expected unknown option rejection, got exit=%d stderr=%q", code, stderr)
	}
}

func TestSourceInstallTarballFallbackStampsSourceVersion(t *testing.T) {
	realVersionGo, err := os.ReadFile(filepath.Join("..", "..", "pkg", "version", "version.go"))
	if err != nil {
		t.Fatalf("read real pkg/version/version.go: %v", err)
	}

	t.Run("unverified main tarball compiles real pkg/version to -source", func(t *testing.T) {
		dir := t.TempDir()
		buildDir := filepath.Join(dir, "build")
		installDir := filepath.Join(dir, "install")
		fixturePkg := filepath.Join(buildDir, "srcpkg", "beads_viewer-main")
		for _, p := range []string{
			filepath.Join(fixturePkg, "pkg", "version"),
			filepath.Join(fixturePkg, "cmd", "bv"),
			installDir,
		} {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		hostGoVer := strings.TrimPrefix(runtime.Version(), "go")
		goMod := "module github.com/Dicklesworthstone/beads_viewer\n\ngo " + hostGoVer + "\n"
		if err := os.WriteFile(filepath.Join(fixturePkg, "go.mod"), []byte(goMod), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixturePkg, "pkg", "version", "version.go"), realVersionGo, 0o600); err != nil {
			t.Fatal(err)
		}
		stubMain := `package main

import (
	"fmt"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
)

func main() {
	fmt.Println(version.Version)
}
`
		if err := os.WriteFile(filepath.Join(fixturePkg, "cmd", "bv", "main.go"), []byte(stubMain), 0o600); err != nil {
			t.Fatal(err)
		}
		setup := `
ensure_go() { printf '1.26.11'; }
make_tmp_dir() { printf '%s\n' "$BUILD_FIXTURE"; }
git() { return 1; }
download_file() {
    tar -czf "$2" -C "$BUILD_FIXTURE/srcpkg" beads_viewer-main
}`
		out, stderr, code := runInstallFunc(t, []string{
			"BUILD_FIXTURE=" + buildDir,
			"INSTALL_DIR=" + installDir,
			"GOTOOLCHAIN=local",
		}, setup, `try_go_install`, "")
		if code != 0 {
			t.Fatalf("try_go_install tarball fallback failed: exit=%d out=%s stderr=%s", code, out, stderr)
		}
		verOut, err := exec.Command(filepath.Join(installDir, "bv")).CombinedOutput()
		if err != nil {
			t.Fatalf("execute built bv: %v (%s)", err, verOut)
		}
		gotVer := strings.TrimSpace(string(verOut))
		if !strings.HasSuffix(gotVer, "-source") || gotVer == "v0.0.0-source" {
			t.Fatalf("expected unverified main tarball binary to report <release>-source (not sealed release or v0.0.0-source), got %q", gotVer)
		}
	})

	t.Run("pinned --version --from-source compiles real pkg/version to exact tag", func(t *testing.T) {
		dir := t.TempDir()
		buildDir := filepath.Join(dir, "build")
		installDir := filepath.Join(dir, "install")
		fixturePkg := filepath.Join(buildDir, "srcpkg", "beads_viewer-0.25.0")
		for _, p := range []string{
			filepath.Join(fixturePkg, "pkg", "version"),
			filepath.Join(fixturePkg, "cmd", "bv"),
			installDir,
		} {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		hostGoVer := strings.TrimPrefix(runtime.Version(), "go")
		goMod := "module github.com/Dicklesworthstone/beads_viewer\n\ngo " + hostGoVer + "\n"
		if err := os.WriteFile(filepath.Join(fixturePkg, "go.mod"), []byte(goMod), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixturePkg, "pkg", "version", "version.go"), realVersionGo, 0o600); err != nil {
			t.Fatal(err)
		}
		stubMain := `package main

import (
	"fmt"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
)

func main() {
	fmt.Println(version.Version)
}
`
		if err := os.WriteFile(filepath.Join(fixturePkg, "cmd", "bv", "main.go"), []byte(stubMain), 0o600); err != nil {
			t.Fatal(err)
		}
		setup := `
ensure_go() { printf '1.26.11'; }
make_tmp_dir() { printf '%s\n' "$BUILD_FIXTURE"; }
git() { return 1; }
download_file() {
    tar -czf "$2" -C "$BUILD_FIXTURE/srcpkg" beads_viewer-0.25.0
}`
		out, stderr, code := runInstallFunc(t, []string{
			"BUILD_FIXTURE=" + buildDir,
			"INSTALL_DIR=" + installDir,
			"GOTOOLCHAIN=local",
		}, setup, `parse_args --from-source --version v0.25.0 && try_go_install`, "")
		if code != 0 {
			t.Fatalf("try_go_install --version v0.25.0 failed: exit=%d out=%s stderr=%s", code, out, stderr)
		}
		verOut, err := exec.Command(filepath.Join(installDir, "bv")).CombinedOutput()
		if err != nil {
			t.Fatalf("execute built bv: %v (%s)", err, verOut)
		}
		if gotVer := strings.TrimSpace(string(verOut)); gotVer != "v0.25.0" {
			t.Fatalf("expected pinned --version v0.25.0 binary to report v0.25.0, got %q", gotVer)
		}
	})
}

func TestHasControllingTTYAndBrewStdinIsolation(t *testing.T) {
	// Piped stdin (curl ... | bash) must NOT be consumed by brew install go, and
	// detached non-TTY execution must not abort with /dev/tty ENXIO.
	setup := `
installed_go=0
brew() {
    local leaked=""
    read -r leaked || true
    if [ -n "$leaked" ]; then
        echo "LEAKED_STDIN:$leaked" >&2
        return 99
    fi
    installed_go=1
    return 0
}
go() {
    if [ "$installed_go" -eq 1 ]; then
        printf 'go version go1.26.11 linux/amd64\n'
    else
        printf 'go version go1.20.0 linux/amd64\n'
    fi
}`
	out, stderr, code := runInstallFunc(t, nil, setup, `ver=$(ensure_go); rest=$(cat); printf '%s|%s\n' "$ver" "$rest"`, "remaining_script_line\n")
	if code != 0 || out != "1.26.11|remaining_script_line\n" {
		t.Fatalf("ensure_go consumed piped stdin or failed: exit=%d out=%q stderr=%s", code, out, stderr)
	}
	if strings.Contains(stderr, "LEAKED_STDIN") {
		t.Fatalf("brew read from piped stdin: %s", stderr)
	}
}
