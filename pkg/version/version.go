package version

import (
	"regexp"
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
const fallback = "v0.26.1"

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
	// the injected value, distinguish untagged VCS development builds via
	// "-dev+<shortrev>[.dirty]" so local builds never impersonate a sealed
	// release tag, and always fall through to the hardcoded fallback as a
	// last resort so Version is never empty.
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(version, info, ok)
}

func resolveVersion(injected string, info *debug.BuildInfo, hasBuildInfo bool) string {
	switch {
	case isUsableVersion(injected):
		// 1. Build-time ldflags injection (GoReleaser, Nix, manual).
		return normalizeVersion(injected)
	case hasBuildInfo && info != nil:
		// 2. Tagged module version from "go install ...@vX.Y.Z".
		if rel := releaseModuleVersion(info.Main.Version); rel != "" {
			return rel
		}
		// 3. Untagged local / pseudo-version / dirty VCS build: never claim a sealed release tag.
		if dev := devVersionFromBuildInfo(info); dev != "" {
			return dev
		}
	}
	if fallback != "" {
		return fallback
	}
	return "v0.0.0-dev"
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

// Go stamps three pseudo-version forms: no base tag, a release base, or a
// prerelease base. Match the complete timestamp/revision form so ordinary
// prerelease tags such as v1.2.3-0.alpha remain usable.
// See https://go.dev/ref/mod#pseudo-versions. Build-info module versions have
// already been validated by Go; this pattern only classifies their form.
var pseudoVersionPattern = regexp.MustCompile(`^v[0-9]+\.(0\.0-|[0-9]+\.[0-9]+-([0-9A-Za-z-]+\.)*0\.)[0-9]{14}-[0-9A-Za-z]+(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// releaseModuleVersion accepts tagged module versions, excluding development builds.
func releaseModuleVersion(v string) string {
	if v == "" || v == "(devel)" {
		return ""
	}
	if v[0] != 'v' {
		v = "v" + v
	}
	if pseudoVersionPattern.MatchString(v) || strings.HasSuffix(v, "+dirty") {
		return ""
	}
	return v
}

// devVersionFromBuildInfo formats untagged local/pseudo-version/dirty builds as
// "<fallback>-dev" or "<fallback>-dev+<shortrev>[.dirty]" so an unsealed dev
// binary never impersonates a published release tag while remaining valid SemVer
// recognized as a development prerelease by pkg/updater.
func devVersionFromBuildInfo(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	mv := strings.TrimSpace(info.Main.Version)
	isDevModule := mv == "(devel)"
	if !isDevModule && mv != "" {
		norm := mv
		if norm[0] != 'v' {
			norm = "v" + norm
		}
		isDevModule = pseudoVersionPattern.MatchString(norm) || strings.HasSuffix(norm, "+dirty")
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = sanitizeSemverIdent(s.Value, 12)
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if !isDevModule && rev == "" && !dirty {
		return ""
	}
	base := fallback + "-dev"
	switch {
	case rev != "" && dirty:
		return base + "+" + rev + ".dirty"
	case rev != "":
		return base + "+" + rev
	case dirty:
		return base + "+dirty"
	default:
		return base
	}
}

func sanitizeSemverIdent(raw string, maxLen int) string {
	var b strings.Builder
	for _, r := range raw {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
			if maxLen > 0 && b.Len() >= maxLen {
				break
			}
		}
	}
	return b.String()
}
