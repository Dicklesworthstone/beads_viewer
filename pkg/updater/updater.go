package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	osExec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
	"gopkg.in/yaml.v3"
)

const (
	repoOwner = "Dicklesworthstone"
	repoName  = "beads_viewer"
	baseURL   = "https://api.github.com/repos/" + repoOwner + "/" + repoName

	maxReleaseMetadataBytes  = 1 << 20
	maxChecksumManifestBytes = 1 << 20
	maxDownloadBytes         = 512 << 20
	githubAPIVersion         = "2022-11-28"
)

// maxExtractedBinaryBytes is a variable so tests can shrink the limit without
// constructing large archives. Production keeps it aligned with maxDownloadBytes.
var maxExtractedBinaryBytes int64 = maxDownloadBytes

// Environment variables that control the updater's network behaviour.
const (
	// EnvNoUpdateCheck overrides the TUI's startup release check. True or
	// invalid nonempty values disable it; explicit false values enable it.
	// Explicit --check-update / --update invocations ignore this setting.
	EnvNoUpdateCheck = "BV_NO_UPDATE_CHECK"
	// EnvUseToken overrides permission to send GITHUB_TOKEN / GH_TOKEN with
	// GitHub requests. Only recognized true values opt in; false or invalid
	// nonempty values revoke even a saved config grant.
	EnvUseToken = "BV_UPDATE_USE_TOKEN"
	// envNoSavedConfig is the project-wide switch that makes bv ignore every
	// file under the user config directory (read and write).
	envNoSavedConfig = "BV_NO_SAVED_CONFIG"

	userConfigFileName        = "config.yaml"
	startupDisclosureFileName = "update-check-disclosed"
)

// StartupCheckDisclosure is the footer text shown the first time bv performs
// its automatic release check, so the network access is never silent (#197).
const StartupCheckDisclosure = "checked github.com for updates (BV_NO_UPDATE_CHECK=1 to disable)"

// Preferences is the resolved update policy: defaults, then
// ~/.config/bv/config.yaml `updates:` keys, then environment overrides.
type Preferences struct {
	// CheckOnStartup controls the TUI's background release check.
	CheckOnStartup bool
	// UseAmbientToken allows GITHUB_TOKEN / GH_TOKEN to be attached to
	// requests that target GitHub hosts.
	UseAmbientToken bool
}

// userUpdateConfig is the subset of config.yaml the updater understands:
//
//	updates:
//	  check: false      # skip the startup release check
//	  use_token: true   # send the ambient GITHUB_TOKEN / GH_TOKEN
type userUpdateConfig struct {
	Updates struct {
		Check    *bool `yaml:"check"`
		UseToken *bool `yaml:"use_token"`
	} `yaml:"updates"`
}

// parseEnvBoolean accepts explicit boolean values without interpreting typos
// as consent. Callers apply the privacy-preserving policy on invalid input.
func parseEnvBoolean(value string) (enabled, valid bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

func savedConfigDisabled() bool {
	return os.Getenv(envNoSavedConfig) != ""
}

// UserConfigDir returns bv's user configuration directory:
// $XDG_CONFIG_HOME/bv when XDG_CONFIG_HOME is set, otherwise ~/.config/bv.
func UserConfigDir() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "bv"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving user config dir: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("resolving user config dir: empty home directory")
	}
	return filepath.Join(home, ".config", "bv"), nil
}

// loadUserUpdateConfig reads the `updates:` section of config.yaml. A missing
// or unreadable file, or BV_NO_SAVED_CONFIG, yields the zero value so callers
// fall back to defaults.
func loadUserUpdateConfig() userUpdateConfig {
	var cfg userUpdateConfig
	if savedConfigDisabled() {
		return cfg
	}
	dir, err := UserConfigDir()
	if err != nil {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(dir, userConfigFileName))
	if err != nil {
		return cfg
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return userUpdateConfig{}
	}
	return cfg
}

// LoadPreferences resolves the update policy. Defaults are "check on startup,
// never send the ambient token"; config.yaml can flip either, and the
// nonempty environment (BV_NO_UPDATE_CHECK, BV_UPDATE_USE_TOKEN) wins over both.
// Empty/unset values preserve saved policy. Invalid values suppress automatic
// checks and deny credentials, without logging potentially sensitive input.
func LoadPreferences() Preferences {
	prefs := Preferences{CheckOnStartup: true, UseAmbientToken: false}
	cfg := loadUserUpdateConfig()
	if cfg.Updates.Check != nil {
		prefs.CheckOnStartup = *cfg.Updates.Check
	}
	if cfg.Updates.UseToken != nil {
		prefs.UseAmbientToken = *cfg.Updates.UseToken
	}
	if value := strings.TrimSpace(os.Getenv(EnvNoUpdateCheck)); value != "" {
		disabled, valid := parseEnvBoolean(value)
		prefs.CheckOnStartup = valid && !disabled
	}
	if value := strings.TrimSpace(os.Getenv(EnvUseToken)); value != "" {
		enabled, valid := parseEnvBoolean(value)
		prefs.UseAmbientToken = valid && enabled
	}
	return prefs
}

// StartupCheckEnabled reports whether the TUI should run its automatic
// release check at startup.
func StartupCheckEnabled() bool {
	return LoadPreferences().CheckOnStartup
}

// StartupDisclosurePending reports whether the one-time "checked github.com"
// footer notice still has to be shown. The notice is recorded in the user
// config directory, so it appears once per config-directory lifetime; with
// BV_NO_SAVED_CONFIG nothing can be recorded and nothing is shown.
func StartupDisclosurePending() bool {
	if savedConfigDisabled() {
		return false
	}
	dir, err := UserConfigDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, startupDisclosureFileName))
	return os.IsNotExist(err)
}

// RecordStartupDisclosure marks the startup-check disclosure as shown by
// writing a marker file into the user config directory.
func RecordStartupDisclosure() error {
	if savedConfigDisabled() {
		return nil
	}
	dir, err := UserConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	body := "bv showed the startup update-check disclosure on " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(dir, startupDisclosureFileName), []byte(body), 0o644); err != nil {
		return fmt.Errorf("recording update-check disclosure: %w", err)
	}
	return nil
}

// ambientGitHubToken returns the GitHub token present in the environment,
// checking GITHUB_TOKEN first, then GH_TOKEN. Returns empty string if
// neither is set.
func ambientGitHubToken() string {
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		return tok
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

// githubToken returns the token to attach to GitHub requests, or "" when the
// user has not opted in via BV_UPDATE_USE_TOKEN=1 or config.yaml
// `updates: {use_token: true}`. Public release metadata needs no token; using
// one only raises the rate limit from 60 to 5,000 requests/hour (#117), so a
// credential that happens to be in the environment is never sent by default.
func githubToken() string {
	if !LoadPreferences().UseAmbientToken {
		return ""
	}
	return ambientGitHubToken()
}

// isGitHubHost returns true if the given URL points to a github.com or
// githubusercontent.com domain (including subdomains like api.github.com,
// objects.githubusercontent.com, etc.).
func isGitHubHost(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	return host == "github.com" || strings.HasSuffix(host, ".github.com") ||
		host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com")
}

// setGitHubAuth adds an Authorization header to a request when the user has
// opted in to using the ambient GITHUB_TOKEN / GH_TOKEN (see githubToken)
// and the request targets a GitHub domain. This prevents leaking tokens to
// non-GitHub hosts (e.g. CDN redirects).
func setGitHubAuth(req *http.Request) {
	// Re-evaluating policy must also revoke a header added by an earlier call.
	// This helper owns updater authentication; denial never preserves stale auth.
	req.Header.Del("Authorization")
	if tok := githubToken(); tok != "" && isGitHubHost(req.URL) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func setGitHubAPIHeaders(req *http.Request, userAgent string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", userAgent)
	setGitHubAuth(req)
}

func safeAssetName(name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("unsafe release asset name %q", name)
	}
	return name, nil
}

func decodeReleaseMetadata(body io.Reader) (Release, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxReleaseMetadataBytes+1))
	if err != nil {
		return Release{}, err
	}
	if int64(len(data)) > maxReleaseMetadataBytes {
		return Release{}, fmt.Errorf("release metadata exceeds %d bytes", maxReleaseMetadataBytes)
	}

	var rel Release
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&rel); err != nil {
		return Release{}, err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Release{}, fmt.Errorf("release metadata contains multiple JSON values")
		}
		return Release{}, fmt.Errorf("release metadata has trailing data: %w", err)
	}
	return rel, nil
}

// Release represents a GitHub release
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset represents a release asset (binary, checksum file, etc.)
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	State              string `json:"state"`
}

// UpdateResult contains information about an update operation
type UpdateResult struct {
	OldVersion  string `json:"old_version"`
	NewVersion  string `json:"new_version"`
	BackupPath  string `json:"backup_path,omitempty"`
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	RequireRoot bool   `json:"require_root,omitempty"`
}

// CheckForUpdates queries GitHub for the latest release.
// Returns the new version tag if an update is available, empty string otherwise.
func CheckForUpdates() (string, string, error) {
	// The TUI runs this command asynchronously, so allow enough time for slow
	// networks without delaying startup or input handling.
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	return checkForUpdates(client, baseURL+"/releases/latest")
}

func checkForUpdates(client *http.Client, url string) (string, string, error) {
	if client == nil {
		return "", "", fmt.Errorf("http client is nil")
	}
	timeout := client.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	setGitHubAPIHeaders(req, "beads-viewer-update-check")

	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return "", "", fmt.Errorf("github api returned status: %s", resp.Status)
	}

	rel, err := decodeReleaseMetadata(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil {
		return "", "", fmt.Errorf("decode latest release metadata: %w", err)
	}
	if closeErr != nil {
		return "", "", fmt.Errorf("close latest release response: %w", closeErr)
	}
	if err := validateReleaseIdentity(&rel); err != nil {
		return "", "", err
	}

	newer, err := isNewerVersion(rel.TagName, version.Version)
	if err != nil {
		return "", "", err
	}
	if !newer {
		return "", "", nil
	}

	if err := ValidateReleaseForUpdate(&rel); err != nil {
		return "", "", fmt.Errorf("latest release %s is not installable: %w", rel.TagName, err)
	}
	if err := validateRemoteChecksumManifest(ctx, client, &rel); err != nil {
		return "", "", fmt.Errorf("latest release %s has an unusable checksum manifest: %w", rel.TagName, err)
	}
	return rel.TagName, rel.HTMLURL, nil
}

// IsNewerThanCurrent reports whether candidate is newer than this binary's version.
func IsNewerThanCurrent(candidate string) bool {
	newer, err := CheckNewerThanCurrent(candidate)
	return err == nil && newer
}

// CheckNewerThanCurrent reports whether candidate is newer. It returns an
// error for a malformed candidate or non-development current version; known
// local/development version markers fail closed without advertising an update.
func CheckNewerThanCurrent(candidate string) (bool, error) {
	return isNewerVersion(candidate, version.Version)
}

type parsedVersion struct {
	core           [3]string
	coreComponents int
	prerelease     []string
}

func parseVersion(raw string) (parsedVersion, error) {
	var parsed parsedVersion
	raw = strings.TrimSpace(raw)
	if len(raw) > 128 {
		return parsed, fmt.Errorf("version is too long")
	}
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" {
		return parsed, fmt.Errorf("version is empty")
	}

	versionAndBuild := strings.Split(raw, "+")
	if len(versionAndBuild) > 2 {
		return parsed, fmt.Errorf("version %q contains multiple build metadata separators", raw)
	}
	if len(versionAndBuild) == 2 {
		if err := validateVersionIdentifiers(versionAndBuild[1], false); err != nil {
			return parsed, fmt.Errorf("invalid build metadata in version %q: %w", raw, err)
		}
	}
	raw = versionAndBuild[0]

	versionAndPre := strings.SplitN(raw, "-", 2)
	if len(versionAndPre) == 2 {
		if err := validateVersionIdentifiers(versionAndPre[1], true); err != nil {
			return parsed, fmt.Errorf("invalid prerelease in version %q: %w", raw, err)
		}
		parsed.prerelease = strings.Split(versionAndPre[1], ".")
	}

	core := strings.Split(versionAndPre[0], ".")
	if len(core) == 0 || len(core) > len(parsed.core) {
		return parsed, fmt.Errorf("version %q must have one to three numeric components", raw)
	}
	for i := range parsed.core {
		parsed.core[i] = "0"
	}
	for i, component := range core {
		if !isNumericIdentifier(component) {
			return parsed, fmt.Errorf("version %q has non-numeric core component %q", raw, component)
		}
		if len(component) > 1 && component[0] == '0' {
			return parsed, fmt.Errorf("version %q has a leading zero in core component %q", raw, component)
		}
		parsed.core[i] = component
	}
	parsed.coreComponents = len(core)
	return parsed, nil
}

func validateVersionIdentifiers(value string, rejectNumericLeadingZeros bool) error {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return fmt.Errorf("identifier is empty")
		}
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') &&
				(char < 'a' || char > 'z') && char != '-' {
				return fmt.Errorf("identifier %q contains an invalid character", identifier)
			}
		}
		if rejectNumericLeadingZeros && len(identifier) > 1 && identifier[0] == '0' && isNumericIdentifier(identifier) {
			return fmt.Errorf("numeric identifier %q has a leading zero", identifier)
		}
	}
	return nil
}

func isNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func compareNumericIdentifiers(v1, v2 string) int {
	if len(v1) != len(v2) {
		if len(v1) > len(v2) {
			return 1
		}
		return -1
	}
	return strings.Compare(v1, v2)
}

func compareVersionCore(v1, v2 parsedVersion) int {
	for i := range v1.core {
		if result := compareNumericIdentifiers(v1.core[i], v2.core[i]); result != 0 {
			return result
		}
	}
	return 0
}

func compareParsedVersions(v1, v2 parsedVersion) int {
	if result := compareVersionCore(v1, v2); result != 0 {
		return result
	}
	if len(v1.prerelease) == 0 && len(v2.prerelease) == 0 {
		return 0
	}
	if len(v1.prerelease) == 0 {
		return 1
	}
	if len(v2.prerelease) == 0 {
		return -1
	}

	limit := len(v1.prerelease)
	if len(v2.prerelease) < limit {
		limit = len(v2.prerelease)
	}
	for i := 0; i < limit; i++ {
		part1 := v1.prerelease[i]
		part2 := v2.prerelease[i]
		numeric1 := isNumericIdentifier(part1)
		numeric2 := isNumericIdentifier(part2)
		switch {
		case numeric1 && numeric2:
			if result := compareNumericIdentifiers(part1, part2); result != 0 {
				return result
			}
		case numeric1:
			return -1
		case numeric2:
			return 1
		default:
			if result := strings.Compare(part1, part2); result != 0 {
				return result
			}
		}
	}
	if len(v1.prerelease) > len(v2.prerelease) {
		return 1
	}
	if len(v1.prerelease) < len(v2.prerelease) {
		return -1
	}
	return 0
}

func isDevelopmentVersion(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) > 0 && (raw[0] == 'v' || raw[0] == 'V') {
		raw = raw[1:]
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, marker := range []string{"dev", "dirty", "nightly", "local", "snapshot", "git"} {
		if hasDevelopmentMarker(raw, marker) {
			return true
		}
	}
	return false
}

func isDevelopmentPrerelease(parsed parsedVersion) bool {
	for _, identifier := range parsed.prerelease {
		identifier = strings.ToLower(identifier)
		for _, marker := range []string{"dev", "dirty", "nightly", "local", "snapshot", "git"} {
			if hasDevelopmentMarker(identifier, marker) {
				return true
			}
		}
	}
	return false
}

func hasDevelopmentMarker(value, marker string) bool {
	if value == marker || strings.HasPrefix(value, marker+"-") {
		return true
	}
	if len(value) <= len(marker) || !strings.HasPrefix(value, marker) {
		return false
	}
	next := value[len(marker)]
	return next >= '0' && next <= '9'
}

func isNewerVersion(candidate, current string) (bool, error) {
	parsedCandidate, err := parseVersion(candidate)
	if err != nil {
		return false, fmt.Errorf("invalid candidate version %q: %w", candidate, err)
	}
	parsedCurrent, err := parseVersion(current)
	if err != nil {
		if isDevelopmentVersion(current) {
			return false, nil
		}
		return false, fmt.Errorf("invalid current version %q: %w", current, err)
	}
	if isDevelopmentPrerelease(parsedCurrent) && compareVersionCore(parsedCandidate, parsedCurrent) <= 0 {
		return false, nil
	}
	return compareParsedVersions(parsedCandidate, parsedCurrent) > 0, nil
}

// compareVersions compares semantic versions with an optional leading v. It
// accepts one-to-three numeric core components for compatibility with older bv
// tags and ignores build metadata as required by Semantic Versioning. Invalid
// inputs compare equal so callers fail closed instead of announcing an update.
func compareVersions(v1, v2 string) int {
	p1, err1 := parseVersion(v1)
	p2, err2 := parseVersion(v2)
	if err2 != nil && isDevelopmentVersion(v2) {
		return -1
	}
	if err1 != nil || err2 != nil {
		return 0
	}
	if isDevelopmentPrerelease(p2) && compareVersionCore(p1, p2) <= 0 {
		return -1
	}
	return compareParsedVersions(p1, p2)
}

// GetLatestRelease fetches full release info including assets
func GetLatestRelease() (*Release, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	setGitHubAPIHeaders(req, "beads-viewer-updater")

	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("failed to fetch release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned status: %s", resp.Status)
	}

	rel, err := decodeReleaseMetadata(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse release info: %w", err)
	}
	if err := validateReleaseIdentity(&rel); err != nil {
		return nil, err
	}

	return &rel, nil
}

func platformArchiveExtension(goos string) string {
	if goos == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

// stableAssetName is the unversioned archive name (bv_<os>_<arch>.<ext>)
// that releases up to v0.22.0 used; kept as a fallback so the updater can
// still install those releases and any release published by an older
// goreleaser configuration.
func stableAssetName() string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	return fmt.Sprintf("bv_%s_%s%s", goos, goarch, platformArchiveExtension(goos))
}

// getAssetName returns the versioned archive name (bv_<version>_<os>_<arch>.<ext>)
// that .goreleaser.yaml produces (#195).
func getAssetName(version string) string {
	ver := strings.TrimPrefix(version, "v")
	return fmt.Sprintf("bv_%s_%s_%s%s", ver, runtime.GOOS, runtime.GOARCH, platformArchiveExtension(runtime.GOOS))
}

// platformAssetNames lists the archive names to try for this OS/arch, most
// specific first: the versioned name, then the unversioned legacy name, then
// the old Windows tar.gz variant.
func platformAssetNames(version string) []string {
	ver := strings.TrimPrefix(version, "v")

	var names []string
	if ver != "" {
		names = append(names, getAssetName(version))
	}
	names = append(names, stableAssetName())
	if ver != "" && runtime.GOOS == "windows" {
		names = append(names, fmt.Sprintf("bv_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH))
	}
	return names
}

// FindPlatformAsset finds the appropriate asset for the current OS/arch
func (r *Release) FindPlatformAsset() *Asset {
	if r == nil {
		return nil
	}
	for _, targetName := range platformAssetNames(r.TagName) {
		for i := range r.Assets {
			if r.Assets[i].Name == targetName {
				return &r.Assets[i]
			}
		}
	}
	return nil
}

func (r *Release) findPlatformAssetWithChecksum(checksums map[string]string) *Asset {
	if r == nil {
		return nil
	}
	for _, targetName := range platformAssetNames(r.TagName) {
		if _, ok := checksums[targetName]; !ok {
			continue
		}
		for i := range r.Assets {
			if r.Assets[i].Name == targetName {
				return &r.Assets[i]
			}
		}
	}
	return nil
}

// FindChecksumAsset finds the checksums file
func (r *Release) FindChecksumAsset() *Asset {
	if r == nil {
		return nil
	}
	for i := range r.Assets {
		if r.Assets[i].Name == "checksums.txt" {
			return &r.Assets[i]
		}
	}
	return nil
}

func parseGitHubHTTPSURL(rawURL, field string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid %s URL: %w", field, err)
	}
	if parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" || parsed.Port() != "" {
		return nil, fmt.Errorf("%s URL must use HTTPS on github.com", field)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s URL must not contain credentials, a query, or a fragment", field)
	}
	return parsed, nil
}

func validateReleaseIdentity(release *Release) error {
	if release == nil {
		return fmt.Errorf("release metadata is nil")
	}
	if release.TagName != strings.TrimSpace(release.TagName) {
		return fmt.Errorf("release tag %q has surrounding whitespace", release.TagName)
	}
	if release.Draft {
		return fmt.Errorf("release %q is still a draft", release.TagName)
	}
	if release.Prerelease {
		return fmt.Errorf("release %q is marked as a prerelease", release.TagName)
	}
	parsed, err := parseVersion(release.TagName)
	if err != nil {
		return fmt.Errorf("invalid release tag %q: %w", release.TagName, err)
	}
	if len(parsed.prerelease) != 0 {
		return fmt.Errorf("release tag %q is a semantic-version prerelease", release.TagName)
	}
	if parsed.coreComponents != 3 {
		return fmt.Errorf("release tag %q must use major.minor.patch", release.TagName)
	}
	releaseURL, err := parseGitHubHTTPSURL(release.HTMLURL, "release page")
	if err != nil {
		return err
	}
	expectedPath := fmt.Sprintf("/%s/%s/releases/tag/%s", repoOwner, repoName, release.TagName)
	if releaseURL.Path != expectedPath {
		return fmt.Errorf("release page URL path %q does not match %q", releaseURL.Path, expectedPath)
	}
	return nil
}

func validateReleaseAsset(release *Release, asset *Asset, description string, maxSize int64) error {
	if release == nil {
		return fmt.Errorf("release metadata is nil")
	}
	if asset == nil {
		return fmt.Errorf("%s is missing", description)
	}
	assetName, err := safeAssetName(asset.Name)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", description, err)
	}
	if asset.State != "uploaded" {
		return fmt.Errorf("%s %q is not uploaded (state %q)", description, asset.Name, asset.State)
	}
	if asset.Size <= 0 {
		return fmt.Errorf("%s %q has invalid size %d", description, asset.Name, asset.Size)
	}
	if maxSize <= 0 {
		return fmt.Errorf("%s has invalid maximum size %d", description, maxSize)
	}
	if asset.Size > maxSize {
		return fmt.Errorf("%s %q exceeds maximum size %d", description, asset.Name, maxSize)
	}
	assetURL, err := parseGitHubHTTPSURL(asset.BrowserDownloadURL, description)
	if err != nil {
		return err
	}
	expectedPath := fmt.Sprintf("/%s/%s/releases/download/%s/%s", repoOwner, repoName, release.TagName, assetName)
	if assetURL.Path != expectedPath {
		return fmt.Errorf("%s URL path %q does not match %q", description, assetURL.Path, expectedPath)
	}
	if _, err := assetSHA256Digest(asset); err != nil {
		return fmt.Errorf("invalid %s digest: %w", description, err)
	}
	return nil
}

func assetSHA256Digest(asset *Asset) (string, error) {
	if asset == nil {
		return "", fmt.Errorf("asset is nil")
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(asset.Digest, prefix) {
		return "", fmt.Errorf("asset %q digest must start with %q", asset.Name, prefix)
	}
	digest := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(asset.Digest, prefix)))
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("asset %q has invalid sha256 digest length %d", asset.Name, len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", fmt.Errorf("asset %q has invalid sha256 digest: %w", asset.Name, err)
	}
	return digest, nil
}

func releaseAssetsForUpdate(release *Release) (*Asset, *Asset, error) {
	if err := validateReleaseIdentity(release); err != nil {
		return nil, nil, err
	}
	asset := release.FindPlatformAsset()
	if asset == nil {
		return nil, nil, fmt.Errorf("no binary available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := validateReleaseAsset(release, asset, "platform asset", maxDownloadBytes); err != nil {
		return nil, nil, err
	}
	checksumAsset := release.FindChecksumAsset()
	if checksumAsset == nil {
		return nil, nil, fmt.Errorf("checksums.txt asset is missing")
	}
	if err := validateReleaseAsset(release, checksumAsset, "checksum asset", maxChecksumManifestBytes); err != nil {
		return nil, nil, err
	}
	return asset, checksumAsset, nil
}

func checkedPlatformAsset(release *Release, checksums map[string]string) (*Asset, string, error) {
	asset := release.findPlatformAssetWithChecksum(checksums)
	if asset == nil {
		return nil, "", fmt.Errorf("checksums.txt has no entry for a %s/%s release asset", runtime.GOOS, runtime.GOARCH)
	}
	if err := validateReleaseAsset(release, asset, "checksummed platform asset", maxDownloadBytes); err != nil {
		return nil, "", err
	}
	expectedHash, ok := checksums[asset.Name]
	if !ok {
		return nil, "", fmt.Errorf("no checksum found for %s", asset.Name)
	}
	assetDigest, err := assetSHA256Digest(asset)
	if err != nil {
		return nil, "", err
	}
	if expectedHash != assetDigest {
		return nil, "", fmt.Errorf("checksums.txt digest for %s disagrees with GitHub release metadata", asset.Name)
	}
	return asset, assetDigest, nil
}

// ValidateReleaseForUpdate verifies that a release is stable and has bounded
// assets for this platform plus the checksum manifest required by the automatic
// installer. URLs must match this repository and tag, and both assets must
// expose valid GitHub SHA-256 digests.
func ValidateReleaseForUpdate(release *Release) error {
	_, _, err := releaseAssetsForUpdate(release)
	return err
}

func downloadReleaseAssetBytes(ctx context.Context, client *http.Client, asset *Asset, maxSize int64) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is nil")
	}
	if asset == nil {
		return nil, fmt.Errorf("release asset is nil")
	}
	if asset.Size <= 0 || asset.Size > maxSize {
		return nil, fmt.Errorf("release asset %q has invalid size %d", asset.Name, asset.Size)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "beads-viewer-update-check")
	setGitHubAuth(req)

	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status: %s", resp.Status)
	}
	if resp.ContentLength > 0 && resp.ContentLength != asset.Size {
		return nil, fmt.Errorf("size mismatch: expected %d, got header %d", asset.Size, resp.ContentLength)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != asset.Size {
		return nil, fmt.Errorf("downloaded size mismatch: expected %d, got %d", asset.Size, len(data))
	}
	expectedDigest, err := assetSHA256Digest(asset)
	if err != nil {
		return nil, err
	}
	actualDigest := sha256.Sum256(data)
	if hex.EncodeToString(actualDigest[:]) != expectedDigest {
		return nil, fmt.Errorf("digest mismatch for %s", asset.Name)
	}
	return data, nil
}

func validateRemoteChecksumManifest(ctx context.Context, client *http.Client, release *Release) error {
	_, checksumAsset, err := releaseAssetsForUpdate(release)
	if err != nil {
		return err
	}
	data, err := downloadReleaseAssetBytes(ctx, client, checksumAsset, maxChecksumManifestBytes)
	if err != nil {
		return err
	}
	checksums, err := parseChecksumData(data)
	if err != nil {
		return err
	}
	_, _, err = checkedPlatformAsset(release, checksums)
	return err
}

// downloadFile downloads a file from URL to a local path.
//
// If expectedSize is > 0, the download is size-verified against the HTTP Content-Length
// (when present) and the number of bytes written.
func downloadFile(url, destPath string, expectedSize int64) error {
	if expectedSize < 0 {
		return fmt.Errorf("download size cannot be negative: %d", expectedSize)
	}
	if expectedSize > maxDownloadBytes {
		return fmt.Errorf("download size %d exceeds maximum %d", expectedSize, maxDownloadBytes)
	}

	client := &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// Strip Authorization header when redirecting to non-GitHub hosts
			// to avoid leaking tokens to third-party CDNs.
			if !isGitHubHost(req.URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "beads-viewer-updater")
	setGitHubAuth(req)

	resp, err := client.Do(req)
	if err != nil {
		// When CheckRedirect returns a non-nil error, resp may be non-nil
		// with an unclosed Body. Close it to avoid leaking connections.
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status: %s", resp.Status)
	}

	if expectedSize > 0 && resp.ContentLength > 0 && resp.ContentLength != expectedSize {
		return fmt.Errorf("size mismatch: expected %d, got header %d", expectedSize, resp.ContentLength)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}

	// Cap download size to prevent unbounded disk writes from malicious/corrupted responses.
	var limit int64 = maxDownloadBytes + 1
	if expectedSize > 0 {
		limit = expectedSize + 1
	}

	n, err := io.Copy(out, io.LimitReader(resp.Body, limit))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	if expectedSize > 0 && n != expectedSize {
		return fmt.Errorf("downloaded size mismatch: expected %d, got %d", expectedSize, n)
	}
	if expectedSize == 0 && n > maxDownloadBytes {
		return fmt.Errorf("download exceeded maximum size %d", maxDownloadBytes)
	}

	return nil
}

// parseChecksums parses the checksums.txt file and returns a map of filename -> sha256
func parseChecksums(checksumPath string) (map[string]string, error) {
	file, err := os.Open(checksumPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxChecksumManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxChecksumManifestBytes {
		return nil, fmt.Errorf("checksum manifest exceeds %d bytes", maxChecksumManifestBytes)
	}
	return parseChecksumData(data)
}

func parseChecksumData(data []byte) (map[string]string, error) {
	if int64(len(data)) > maxChecksumManifestBytes {
		return nil, fmt.Errorf("checksum manifest exceeds %d bytes", maxChecksumManifestBytes)
	}
	checksums := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "<sha256> <whitespace> <filename (may include spaces)>"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		rawHash := parts[0]
		hash := strings.ToLower(rawHash)
		if len(hash) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		if len(line) < len(rawHash) || !strings.HasPrefix(line, rawHash) {
			continue
		}

		filename := strings.TrimSpace(line[len(rawHash):])
		if filename == "" {
			continue
		}

		if _, exists := checksums[filename]; exists {
			return nil, fmt.Errorf("duplicate checksum entry for %s", filename)
		}
		checksums[filename] = hash
	}
	return checksums, nil
}

// verifyChecksum verifies the SHA256 checksum of a file
func verifyChecksum(filePath, expectedHash string) error {
	expectedHash = strings.ToLower(strings.TrimSpace(expectedHash))
	if len(expectedHash) != sha256.Size*2 {
		return fmt.Errorf("invalid sha256 checksum length: got %d, want %d", len(expectedHash), sha256.Size*2)
	}
	if _, err := hex.DecodeString(expectedHash); err != nil {
		return fmt.Errorf("invalid sha256 checksum %q: %w", expectedHash, err)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualHash := hex.EncodeToString(h.Sum(nil))
	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	return nil
}

// extractBinary extracts the bv binary from a .tar.gz or .zip archive.
func extractBinary(archivePath, destPath string) error {
	if strings.EqualFold(filepath.Ext(archivePath), ".zip") {
		return extractBinaryFromZip(archivePath, destPath)
	}
	return extractBinaryFromTarGz(archivePath, destPath)
}

func extractBinaryFromTarGz(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Look for the bv binary (might be ./bv, bv, or just bv)
		name := filepath.Base(header.Name)
		if name == "bv" || name == "bv.exe" {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				continue
			}
			return copyExtractedBinary(tr, destPath, header.Size)
		}
	}
	return fmt.Errorf("binary not found in archive")
}

func extractBinaryFromZip(archivePath, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open zip archive: %w", err)
	}
	defer zr.Close()

	for _, file := range zr.File {
		name := filepath.Base(file.Name)
		if name != "bv" && name != "bv.exe" {
			continue
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if maxExtractedBinaryBytes < 0 {
			return fmt.Errorf("extracted binary size limit cannot be negative: %d", maxExtractedBinaryBytes)
		}
		if file.UncompressedSize64 > uint64(maxExtractedBinaryBytes) {
			return fmt.Errorf("extracted binary size %d exceeds maximum %d", file.UncompressedSize64, maxExtractedBinaryBytes)
		}

		src, err := file.Open()
		if err != nil {
			return fmt.Errorf("failed to open zipped binary: %w", err)
		}

		copyErr := copyExtractedBinary(src, destPath, int64(file.UncompressedSize64))
		srcCloseErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		if srcCloseErr != nil {
			return fmt.Errorf("failed to close zipped binary: %w", srcCloseErr)
		}
		return nil
	}

	return fmt.Errorf("binary not found in archive")
}

func copyExtractedBinary(src io.Reader, destPath string, declaredSize int64) error {
	if maxExtractedBinaryBytes < 0 {
		return fmt.Errorf("extracted binary size limit cannot be negative: %d", maxExtractedBinaryBytes)
	}
	if declaredSize < 0 {
		return fmt.Errorf("extracted binary size cannot be negative: %d", declaredSize)
	}
	if declaredSize > maxExtractedBinaryBytes {
		return fmt.Errorf("extracted binary size %d exceeds maximum %d", declaredSize, maxExtractedBinaryBytes)
	}

	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create binary: %w", err)
	}

	n, copyErr := io.Copy(out, io.LimitReader(src, maxExtractedBinaryBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("failed to extract binary: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to flush extracted binary: %w", closeErr)
	}
	if n > maxExtractedBinaryBytes {
		return fmt.Errorf("extracted binary exceeds maximum size %d", maxExtractedBinaryBytes)
	}
	return nil
}

// GetCurrentBinaryPath returns the path to the currently running binary
func GetCurrentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// GetBackupPath returns the path for the backup binary
func GetBackupPath(binaryPath string) string {
	return binaryPath + ".backup"
}

func checkBinaryDirectoryWritable(binaryDir string) error {
	probe, err := os.CreateTemp(binaryDir, ".bv-update-test-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if closeErr != nil {
		return fmt.Errorf("close update permission probe: %w", closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove update permission probe: %w", removeErr)
	}
	return nil
}

// PerformUpdate downloads and installs a new version of bv. Human-readable
// progress is written to progress; pass nil to suppress output (for example,
// while Bubble Tea owns the terminal).
func PerformUpdate(release *Release, progress io.Writer) (*UpdateResult, error) {
	if release == nil {
		return nil, fmt.Errorf("release metadata is nil")
	}
	if progress == nil {
		progress = io.Discard
	}
	result := &UpdateResult{
		OldVersion: version.Version,
		NewVersion: release.TagName,
	}

	// Check if update is needed
	newer, err := isNewerVersion(release.TagName, version.Version)
	if err != nil {
		return nil, err
	}
	if !newer {
		result.Success = true
		result.Message = fmt.Sprintf("Already at version %s (latest: %s)", version.Version, release.TagName)
		return result, nil
	}

	_, checksumAsset, err := releaseAssetsForUpdate(release)
	if err != nil {
		return nil, fmt.Errorf("release %s is not installable: %w", release.TagName, err)
	}

	// Get current binary path
	binaryPath, err := GetCurrentBinaryPath()
	if err != nil {
		return nil, fmt.Errorf("cannot determine binary path: %w", err)
	}

	// Check write permissions
	binaryDir := filepath.Dir(binaryPath)
	if err := checkBinaryDirectoryWritable(binaryDir); err != nil {
		result.RequireRoot = os.IsPermission(err)
		if result.RequireRoot {
			return result, fmt.Errorf("no write permission to %s (try running with sudo): %w", binaryDir, err)
		}
		return result, fmt.Errorf("cannot prepare update in %s: %w", binaryDir, err)
	}

	// Create temp directory for download
	tmpDir, err := os.MkdirTemp("", "bv-update-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	checksumPath := filepath.Join(tmpDir, "checksums.txt")
	if err := downloadFile(checksumAsset.BrowserDownloadURL, checksumPath, checksumAsset.Size); err != nil {
		return nil, fmt.Errorf("checksum download failed: %w", err)
	}
	checksumDigest, err := assetSHA256Digest(checksumAsset)
	if err != nil {
		return nil, err
	}
	if err := verifyChecksum(checksumPath, checksumDigest); err != nil {
		return nil, fmt.Errorf("checksum manifest verification failed: %w", err)
	}

	checksums, err := parseChecksums(checksumPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse checksums: %w", err)
	}
	asset, assetDigest, err := checkedPlatformAsset(release, checksums)
	if err != nil {
		return nil, err
	}

	// Download archive
	assetName, err := safeAssetName(asset.Name)
	if err != nil {
		return nil, err
	}
	archivePath := filepath.Join(tmpDir, assetName)
	fmt.Fprintf(progress, "Downloading %s...\n", release.TagName)
	if err := downloadFile(asset.BrowserDownloadURL, archivePath, asset.Size); err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}

	fmt.Fprintln(progress, "Verifying checksum...")
	if err := verifyChecksum(archivePath, assetDigest); err != nil {
		return nil, fmt.Errorf("checksum verification failed: %w", err)
	}

	// Extract binary to temp location
	newBinaryPath := filepath.Join(tmpDir, "bv-new")
	if runtime.GOOS == "windows" {
		newBinaryPath += ".exe"
	}
	fmt.Fprintln(progress, "Extracting...")
	if err := extractBinary(archivePath, newBinaryPath); err != nil {
		return nil, fmt.Errorf("extraction failed: %w", err)
	}

	// Verify new binary works
	fmt.Fprintln(progress, "Verifying new binary...")
	if err := verifyBinaryVersion(newBinaryPath, release.TagName); err != nil {
		return nil, fmt.Errorf("new binary verification failed: %w", err)
	}

	// Create backup of current binary
	backupPath := GetBackupPath(binaryPath)
	fmt.Fprintf(progress, "Backing up current binary to %s...\n", backupPath)

	// Move the current binary out of the way. This avoids ETXTBSY on Linux
	// and "file in use" errors on Windows when writing the new binary.
	// If a backup already exists and rename cannot replace it, fall back to
	// copyFile so the previous backup is not removed before a fresh backup
	// is successfully written.
	movedForBackup := false
	if err := os.Rename(binaryPath, backupPath); err == nil {
		movedForBackup = true
	} else if err := copyFile(binaryPath, backupPath); err != nil {
		return nil, fmt.Errorf("backup failed: %w", err)
	}
	result.BackupPath = backupPath

	// Replace binary
	fmt.Fprintln(progress, "Installing new version...")
	if err := os.Rename(newBinaryPath, binaryPath); err != nil {
		// If binaryPath still exists (copy fallback above), try to remove it first
		if !movedForBackup {
			_ = os.Remove(binaryPath)
		}
		// On some systems, rename across filesystems doesn't work
		if err := copyFile(newBinaryPath, binaryPath); err != nil {
			// Restore from backup
			restoreErr := os.Rename(backupPath, binaryPath)
			if restoreErr != nil {
				restoreErr = copyFile(backupPath, binaryPath)
			}
			if restoreErr != nil {
				return nil, fmt.Errorf("installation failed: %w (restore also failed: %v; manual recovery: mv %s %s)", err, restoreErr, backupPath, binaryPath)
			}
			return nil, fmt.Errorf("installation failed (restored from backup): %w", err)
		}
	}

	// Ensure executable permissions
	if err := os.Chmod(binaryPath, 0755); err != nil {
		// Not fatal, but log it
		fmt.Fprintf(progress, "Warning: could not set permissions: %v\n", err)
	}

	result.Success = true
	result.Message = fmt.Sprintf("Successfully updated from %s to %s", version.Version, release.TagName)
	return result, nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	info, err := sourceFile.Stat()
	if err != nil {
		return err
	}

	destFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}

	_, err = io.Copy(destFile, sourceFile)
	if closeErr := destFile.Close(); err == nil {
		err = closeErr
	}
	return err
}

func parseBinaryVersionOutput(output []byte) (string, error) {
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != "bv" {
		return "", fmt.Errorf("unexpected --version output %q", strings.TrimSpace(string(output)))
	}
	if _, err := parseVersion(fields[1]); err != nil {
		return "", fmt.Errorf("invalid version in --version output: %w", err)
	}
	return fields[1], nil
}

const maxBinaryVersionOutputBytes = 4 << 10

type limitedOutputBuffer struct {
	bytes.Buffer
	truncated bool
}

func (buffer *limitedOutputBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := maxBinaryVersionOutputBytes - buffer.Len()
	if remaining <= 0 {
		buffer.truncated = true
		return written, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.truncated = true
	}
	_, err := buffer.Buffer.Write(data)
	return written, err
}

func verifyBinaryVersion(binaryPath, expectedVersion string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stdout limitedOutputBuffer
	var stderr limitedOutputBuffer
	cmd := osExec.CommandContext(ctx, binaryPath, "--version")
	cmd.WaitDelay = time.Second
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("--version timed out: %w", ctx.Err())
		}
		return fmt.Errorf("run --version: %w (stderr: %q)", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.truncated {
		return fmt.Errorf("--version output exceeds %d bytes", maxBinaryVersionOutputBytes)
	}
	reportedVersion, err := parseBinaryVersionOutput(stdout.Bytes())
	if err != nil {
		return err
	}
	if _, err := parseVersion(expectedVersion); err != nil {
		return err
	}
	normalize := func(value string) string {
		return "v" + strings.TrimPrefix(strings.TrimSpace(value), "v")
	}
	if normalize(reportedVersion) != normalize(expectedVersion) {
		return fmt.Errorf("downloaded binary reports %s, expected %s", reportedVersion, expectedVersion)
	}
	return nil
}

// Rollback restores the previous version from backup
func Rollback() error {
	binaryPath, err := GetCurrentBinaryPath()
	if err != nil {
		return fmt.Errorf("cannot determine binary path: %w", err)
	}

	backupPath := GetBackupPath(binaryPath)
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("no backup found at %s", backupPath)
	}

	fmt.Printf("Rolling back from backup at %s...\n", backupPath)

	// Move the currently running binary out of the way to avoid ETXTBSY / file-in-use
	badPath := binaryPath + ".bad"
	_ = os.Remove(badPath)
	movedToBad := false
	if err := os.Rename(binaryPath, badPath); err == nil {
		movedToBad = true
	}

	if err := os.Rename(backupPath, binaryPath); err != nil {
		if !movedToBad {
			_ = os.Remove(binaryPath)
		}
		if copyErr := copyFile(backupPath, binaryPath); copyErr != nil {
			// Try to restore the bad binary if rollback completely failed
			if movedToBad {
				_ = os.Rename(badPath, binaryPath)
			}
			return fmt.Errorf("rollback failed: %w", copyErr)
		}
	}

	// Clean up the bad binary if we successfully renamed it out of the way
	if movedToBad {
		_ = os.Remove(badPath) // May fail on Windows if it's currently executing, but that's fine
	}

	fmt.Println("Rollback complete")
	return nil
}

// CheckUpdateAvailable is a convenience wrapper that checks and returns update info
func CheckUpdateAvailable() (available bool, newVersion string, releaseURL string, err error) {
	newVersion, releaseURL, err = CheckForUpdates()
	if err != nil {
		return false, "", "", err
	}
	return newVersion != "", newVersion, releaseURL, nil
}
