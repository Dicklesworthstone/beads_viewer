package version

import (
	"runtime/debug"
	"strings"
)

// version is set at build time by GoReleaser or manual ldflags:
//
//	go build -ldflags "-X github.com/Dicklesworthstone/beads_viewer/pkg/version.version=v1.2.3"
//
// It starts empty so init() can distinguish "ldflags set it" from "no injection".
var version string

// fallback is the hardcoded version kept in sync with the latest release tag.
// Used only when both ldflags and debug.ReadBuildInfo fail to provide a version.
const fallback = "v0.23.0"

// Version is the resolved application version, populated by init().
var Version string

func init() {
	// Resolve version from the first source that yields a usable value.
	//
	// Prior to this fix, GoReleaser injected into the *exported* Version
	// variable directly.  When {{.Version}} resolved to an empty string
	// (e.g. snapshot builds or CI misconfiguration), Version was silently
	// set to "" and no fallback fired.  The v0.14.4 release shipped with
	// this bug — "bv --version" printed just "bv".
	//
	// The fix: inject into the *unexported* `version` variable, validate
	// the injected value, and always fall through to the hardcoded
	// fallback as a last resort so Version is never empty.
	switch {
	case isUsableVersion(version):
		// 1. Build-time ldflags injection (GoReleaser, Nix, manual).
		Version = normalizeVersion(version)
	case versionFromBuildInfo() != "":
		// 2. Module version from "go install ...@vX.Y.Z".
		Version = versionFromBuildInfo()
	default:
		// 3. Hardcoded fallback (always available, manually bumped per release).
		Version = fallback
	}

	// Defensive guarantee: Version must never be empty regardless of how
	// the above branches resolve.  This prevents a repeat of #126.
	if Version == "" {
		Version = fallback
	}
}

// isUsableVersion returns true if v looks like an actual version string
// rather than an empty/whitespace-only/bare-prefix injection artifact.
func isUsableVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "v" || v == "v." {
		return false
	}
	// Reject un-substituted build templates such as "${version}",
	// "v${version}", "{{.Version}}", or "v{{.Version}}". A real semantic
	// version never contains '$', '{', or '}', so any occurrence means a
	// release pipeline failed to expand its version template into the ldflag.
	//
	// The v0.17.0 Windows artifact shipped with the literal placeholder
	// "${version}" baked in. Previously that value passed this check (it is
	// non-empty and is not "", "v", or "v."), so it was accepted as the
	// version — which broke "bv --version" and drove a permanent false
	// "update available" banner (#174). By rejecting it here, resolution
	// falls through to debug.ReadBuildInfo and finally the hardcoded
	// `fallback`, so a mis-injected build still reports a sane version.
	if strings.ContainsAny(v, "${}") {
		return false
	}
	return true
}

// normalizeVersion ensures the version string has a "v" prefix and no
// surrounding whitespace.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// versionFromBuildInfo extracts the module version stamped by the Go toolchain
// when the binary is built via "go install ...@vX.Y.Z". Returns empty string
// for local development builds (which produce "(devel)" or pseudo-versions).
func versionFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		return ""
	}
	// Filter out pseudo-versions (e.g., v0.14.5-0.20260212...-abcdef123456)
	// and dirty builds. These come from local "go build" or "go run", not
	// from "go install ...@vX.Y.Z" which produces clean semver tags.
	if strings.Contains(v, "-0.") || strings.HasSuffix(v, "+dirty") {
		return ""
	}
	if v[0] != 'v' {
		v = "v" + v
	}
	return v
}
