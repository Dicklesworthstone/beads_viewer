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
