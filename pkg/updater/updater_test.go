package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// compareVersions tests
// ============================================================================

func TestCompareVersions_Basic(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int // 1 if v1>v2, -1 if v1<v2, 0 if equal
	}{
		// Basic comparisons
		{"equal versions", "v1.0.0", "v1.0.0", 0},
		{"v1 greater major", "v2.0.0", "v1.0.0", 1},
		{"v1 less major", "v1.0.0", "v2.0.0", -1},
		{"v1 greater minor", "v1.1.0", "v1.0.0", 1},
		{"v1 less minor", "v1.0.0", "v1.1.0", -1},
		{"v1 greater patch", "v1.0.1", "v1.0.0", 1},
		{"v1 less patch", "v1.0.0", "v1.0.1", -1},

		// Without 'v' prefix
		{"no v prefix equal", "1.0.0", "1.0.0", 0},
		{"no v prefix greater", "2.0.0", "1.0.0", 1},
		{"no v prefix less", "1.0.0", "2.0.0", -1},

		// Mixed v prefix
		{"mixed prefix v1 has v", "v1.0.0", "1.0.0", 0},
		{"mixed prefix v2 has v", "1.0.0", "v1.0.0", 0},
		{"mixed prefix different versions", "v2.0.0", "1.0.0", 1},

		// Input hygiene
		{"same version with surrounding spaces", " v1.0.0 ", "v1.0.0", 0},
		{"same version local with newline", "v1.0.0", "v1.0.0\n", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
			}
		})
	}
}

func TestCompareVersions_RealVersions(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		// Real version scenarios
		{"current vs older", "v0.9.0", "v0.8.0", 1},
		{"current vs newer", "v0.8.0", "v0.9.0", -1},
		{"major upgrade", "v1.0.0", "v0.9.0", 1},
		{"patch update", "v0.9.1", "v0.9.0", 1},
		{"double digit minor", "v0.10.0", "v0.9.0", 1},
		{"double digit patch", "v0.9.10", "v0.9.9", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
			}
		})
	}
}

func TestCompareVersions_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		// Partial versions (less than 3 parts)
		{"two part equal", "v1.0", "v1.0", 0},
		{"two part vs three part equal", "v1.0", "v1.0.0", 0},
		{"one part", "v1", "v1.0.0", 0},

		// Large version numbers
		{"large major", "v100.0.0", "v99.0.0", 1},
		{"large minor", "v1.100.0", "v1.99.0", 1},
		{"large patch", "v1.0.100", "v1.0.99", 1},

		// Zero versions
		{"all zeros", "v0.0.0", "v0.0.0", 0},
		{"zero vs one", "v0.0.0", "v0.0.1", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
			}
		})
	}
}

func TestCompareVersions_InvalidVersions(t *testing.T) {
	// Invalid input must fail closed. Lexical ordering can turn malformed release
	// metadata into a false update notification.
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		// Non-numeric versions
		{"alpha vs beta", "alpha", "beta", 0},
		{"beta vs alpha", "beta", "alpha", 0},
		{"same string", "alpha", "alpha", 0},
		{"four core components", "v1.2.3.4", "v1.2.3", 0},
		{"invalid core component", "v1.x.3", "v1.2.3", 0},
		{"empty prerelease", "v1.2.3-", "v1.2.3", 0},

		// Version with extra parts (semver with pre-release)
		{"prerelease lower", "1.0.0-alpha", "1.0.0", -1}, // prerelease should be lower

		// Empty strings
		{"empty vs empty", "", "", 0},
		{"empty vs version", "", "v1.0.0", 0},
		{"version vs empty", "v1.0.0", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
			}
		})
	}
}

func TestCompareVersions_BuildMetadataDoesNotAffectPrecedence(t *testing.T) {
	tests := []struct {
		v1, v2 string
		want   int
	}{
		{"v1.2.3+remote", "v1.2.3+local", 0},
		{"v1.2.3", "v1.2.3+dirty", 0},
		{"v1.2.4", "v1.2.3+dirty", 1},
		{"v1.2.3-alpha+build.2", "v1.2.3-alpha+build.1", 0},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.v1, tt.v2); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.want)
		}
	}
}

func TestIsNewerVersionRejectsMalformedVersions(t *testing.T) {
	tests := []struct {
		name      string
		candidate string
		current   string
	}{
		{"malformed candidate", "v2.0.0.1", "v1.0.0"},
		{"arbitrary candidate", "latest", "v1.0.0"},
		{"arbitrary current", "v2.0.0", "banana"},
		{"marker prefix is not automatically development", "v2.0.0", "github"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if newer, err := isNewerVersion(tt.candidate, tt.current); err == nil {
				t.Fatalf("isNewerVersion(%q, %q) = %v, nil; want error", tt.candidate, tt.current, newer)
			}
		})
	}
}

func TestIsNewerVersionUnparseableDevelopmentBuildFailsClosed(t *testing.T) {
	newer, err := isNewerVersion("v99.0.0", "git-abc123")
	if err != nil {
		t.Fatalf("isNewerVersion returned error for development build: %v", err)
	}
	if newer {
		t.Fatal("development build should not receive an automatic update prompt")
	}
}

func TestCompareVersions_PrereleaseOrdering(t *testing.T) {
	t.Run("prerelease vs prerelease same base", func(t *testing.T) {
		if got := compareVersions("v1.0.0-alpha", "v1.0.0-beta"); got >= 0 {
			t.Errorf("expected alpha < beta when base equal; got %d", got)
		}
	})

	t.Run("prerelease vs release", func(t *testing.T) {
		if got := compareVersions("v1.2.3-rc1", "v1.2.3"); got >= 0 {
			t.Errorf("expected prerelease < release; got %d", got)
		}
	})
}

func TestCompareVersions_Symmetry(t *testing.T) {
	// Test that comparison is antisymmetric: if v1 > v2 then v2 < v1
	versions := []string{
		"v0.7.0",
		"v0.8.0",
		"v0.9.0",
		"v1.0.0",
		"v1.0.1",
		"v1.1.0",
		"v2.0.0",
	}

	for i, v1 := range versions {
		for j, v2 := range versions {
			result1 := compareVersions(v1, v2)
			result2 := compareVersions(v2, v1)

			// result1 and result2 should be negatives of each other
			if result1 != -result2 {
				t.Errorf("Symmetry violation: compareVersions(%q, %q) = %d but compareVersions(%q, %q) = %d",
					v1, v2, result1, v2, v1, result2)
			}

			// Check ordering consistency
			if i < j && result1 >= 0 {
				t.Errorf("Ordering violation: %q should be less than %q but got %d", v1, v2, result1)
			}
			if i > j && result1 <= 0 {
				t.Errorf("Ordering violation: %q should be greater than %q but got %d", v1, v2, result1)
			}
			if i == j && result1 != 0 {
				t.Errorf("Equality violation: %q should equal %q but got %d", v1, v2, result1)
			}
		}
	}
}

func TestCompareVersions_Transitivity(t *testing.T) {
	// Test transitivity: if v1 < v2 and v2 < v3 then v1 < v3
	testCases := []struct {
		v1, v2, v3 string
	}{
		{"v0.7.0", "v0.8.0", "v0.9.0"},
		{"v0.9.0", "v0.9.1", "v1.0.0"},
		{"v1.0.0", "v1.1.0", "v2.0.0"},
	}

	for _, tc := range testCases {
		r12 := compareVersions(tc.v1, tc.v2)
		r23 := compareVersions(tc.v2, tc.v3)
		r13 := compareVersions(tc.v1, tc.v3)

		if r12 != -1 || r23 != -1 || r13 != -1 {
			t.Errorf("Transitivity violation: %q < %q < %q should all hold, got %d, %d, %d",
				tc.v1, tc.v2, tc.v3, r12, r23, r13)
		}
	}
}

func TestCompareVersions_DevBuilds(t *testing.T) {
	tests := []struct {
		name     string
		v1       string // remote
		v2       string // local
		expected int    // 1 if remote>local (update needed), -1 if local>remote (no update)
	}{
		// Unparseable dev versions
		{"dev is newer than release", "v1.0.0", "dev", -1},
		{"dev with spaces is newer than release", "v1.0.0", " dev ", -1},
		{"nightly is newer than release", "v1.0.0", "nightly", -1},
		{"dirty string is newer than release", "v1.0.0", "dirty", -1},
		{"local is newer than release", "v1.0.0", "local", -1},
		{"snapshot is newer than release", "v1.0.0", "snapshot", -1},
		{"git-abc123 is newer than release", "v1.0.0", "git-abc123", -1},

		// Parseable dev builds with suffixes - THE BUG FIX CASES
		// These should NOT trigger update prompts when local is a dev build of the same version
		{"v0.11.2-dirty should not update to v0.11.2", "v0.11.2", "v0.11.2-dirty", -1},
		{"v0.11.2-dev should not update to v0.11.2", "v0.11.2", "v0.11.2-dev", -1},
		{"v0.11.2-local should not update to v0.11.2", "v0.11.2", "v0.11.2-local", -1},
		{"v0.11.2-nightly should not update to v0.11.2", "v0.11.2", "v0.11.2-nightly", -1},
		{"v0.11.2-snapshot should not update to v0.11.2", "v0.11.2", "v0.11.2-snapshot", -1},
		{"v0.11.2-git.abc123 should not update to v0.11.2", "v0.11.2", "v0.11.2-git.abc123", -1},
		{"v0.11.2-git-abc123 should not update to v0.11.2", "v0.11.2", "v0.11.2-git-abc123", -1},
		{"v0.11.2-dev123 should not update to v0.11.2", "v0.11.2", "v0.11.2-dev123", -1},

		// Dev builds with lower base version SHOULD prompt for update
		{"v0.11.3 should update from v0.11.2-dirty", "v0.11.3", "v0.11.2-dirty", 1},
		{"v1.0.0 should update from v0.11.2-dev", "v1.0.0", "v0.11.2-dev", 1},

		// Dev build with higher base version should NOT prompt
		{"v0.11.2 should not update from v0.11.3-dirty", "v0.11.2", "v0.11.3-dirty", -1},

		// Normal pre-releases (NOT dev builds) should still follow semver
		{"v0.11.2-alpha is lower than v0.11.2", "v0.11.2", "v0.11.2-alpha", 1},
		{"v0.11.2-beta is lower than v0.11.2", "v0.11.2", "v0.11.2-beta", 1},
		{"v0.11.2-rc1 is lower than v0.11.2", "v0.11.2", "v0.11.2-rc1", 1},
		{"v0.11.2-device is a normal prerelease", "v0.11.2", "v0.11.2-device", 1},
		{"v0.11.2-github is a normal prerelease", "v0.11.2", "v0.11.2-github", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareVersions(tt.v1, tt.v2)
			if got != tt.expected {
				t.Errorf("compareVersions(%q, %q) = %d; want %d", tt.v1, tt.v2, got, tt.expected)
			}
		})
	}
}

func TestIsNewerThanCurrent(t *testing.T) {
	if !IsNewerThanCurrent("v99.0.0") {
		t.Fatal("v99.0.0 should be newer than the test binary version")
	}
	if IsNewerThanCurrent("v0.0.0") {
		t.Fatal("v0.0.0 should not be newer than the test binary version")
	}
}

// ============================================================================
// Release struct tests
// ============================================================================

func TestRelease_Fields(t *testing.T) {
	// Test that Release struct can be properly instantiated
	rel := Release{
		TagName: "v0.9.0",
		HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v0.9.0",
	}

	if rel.TagName != "v0.9.0" {
		t.Errorf("Expected TagName v0.9.0, got %s", rel.TagName)
	}
	if rel.HTMLURL == "" {
		t.Error("Expected HTMLURL to be set")
	}
}

// ============================================================================
// Version comparison with current version (integration-like tests)
// ============================================================================

func TestCompareVersions_AgainstCurrentVersion(t *testing.T) {
	// These tests ensure version comparison works correctly with a sample version
	// Note: uses a fixed test version, not the actual app version from version.go
	currentVersion := "v0.10.2"

	tests := []struct {
		name       string
		newVersion string
		shouldBe   string // "newer", "older", "same"
	}{
		{"patch update", "v0.10.3", "newer"},
		{"minor update", "v0.11.0", "newer"},
		{"major update", "v1.0.0", "newer"},
		{"same version", "v0.10.2", "same"},
		{"older patch", "v0.10.1", "older"},
		{"older minor", "v0.9.0", "older"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compareVersions(tt.newVersion, currentVersion)

			switch tt.shouldBe {
			case "newer":
				if result != 1 {
					t.Errorf("Expected %q to be newer than %q, got %d", tt.newVersion, currentVersion, result)
				}
			case "older":
				if result != -1 {
					t.Errorf("Expected %q to be older than %q, got %d", tt.newVersion, currentVersion, result)
				}
			case "same":
				if result != 0 {
					t.Errorf("Expected %q to be same as %q, got %d", tt.newVersion, currentVersion, result)
				}
			}
		})
	}
}

// ============================================================================
// Benchmark tests
// ============================================================================

func BenchmarkCompareVersions_SemVer(b *testing.B) {
	for i := 0; i < b.N; i++ {
		compareVersions("v1.2.3", "v1.2.4")
	}
}

func BenchmarkCompareVersions_Fallback(b *testing.B) {
	for i := 0; i < b.N; i++ {
		compareVersions("alpha-1.2.3", "alpha-1.2.4")
	}
}

func TestCompareVersions_SemVerNumericIdentifiers(t *testing.T) {
	// Standard SemVer: identifiers consisting of only digits are compared numerically.
	// rc.2 < rc.10
	// Currently, lexicographic comparison makes "rc.10" < "rc.2" (FALSE)

	v1 := "v1.0.0-rc.2"
	v2 := "v1.0.0-rc.10"

	// Expect v2 > v1 -> returns -1
	expected := -1
	got := compareVersions(v1, v2)

	if got != expected {
		t.Errorf("compareVersions(%q, %q) = %d; want %d (numeric prerelease comparison failure)", v1, v2, got, expected)
	}
}

func installMockUpdaterDownloads(t *testing.T, srv *httptest.Server) {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	origFactory := updaterHTTPClientFactory
	updaterHTTPClientFactory = func(timeout time.Duration) *http.Client {
		client := srv.Client()
		client.Timeout = timeout
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clonedURL := *req.URL
			clone.URL = &clonedURL
			if req.URL.Hostname() == "github.com" && strings.HasPrefix(req.URL.Path, "/Dicklesworthstone/beads_viewer/releases/download/") {
				clone.URL.Scheme = target.Scheme
				clone.URL.Host = target.Host
			}
			return base.RoundTrip(clone)
		})
		return client
	}
	t.Cleanup(func() {
		updaterHTTPClientFactory = origFactory
	})
}

func buildTestReleaseArchiveBytes(t *testing.T, binaryPayload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("bv.exe")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(binaryPayload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	if err := tw.WriteHeader(&tar.Header{
		Name: "bv",
		Mode: 0o755,
		Size: int64(len(binaryPayload)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binaryPayload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestVerifyReleaseAssetChecksumEntry(t *testing.T) {
	const tag = "v99.0.0"
	assetName := getAssetName(tag)
	wantDigest := strings.Repeat("a", 64)

	makeReleaseWithChecksum := func(checksumBody string) *Release {
		checksumSum := sha256.Sum256([]byte(checksumBody))
		return &Release{
			TagName: tag,
			HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/" + tag,
			Assets: []Asset{
				{
					Name:               assetName,
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/" + tag + "/" + assetName,
					Size:               1024,
					Digest:             "sha256:" + wantDigest,
					State:              "uploaded",
				},
				{
					Name:               "checksums.txt",
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/" + tag + "/checksums.txt",
					Size:               int64(len(checksumBody)),
					Digest:             "sha256:" + hex.EncodeToString(checksumSum[:]),
					State:              "uploaded",
				},
			},
		}
	}

	t.Run("valid_checksums_txt", func(t *testing.T) {
		body := fmt.Sprintf("%s  %s\n%s  other_asset.tar.gz\n", wantDigest, assetName, strings.Repeat("b", 64))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
				_, _ = w.Write([]byte(body))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		installMockUpdaterDownloads(t, srv)

		rel := makeReleaseWithChecksum(body)
		got, err := VerifyReleaseAssetChecksumEntry(rel, assetName)
		if err != nil {
			t.Fatalf("VerifyReleaseAssetChecksumEntry valid failed: %v", err)
		}
		if got != wantDigest {
			t.Fatalf("expected digest %s, got %s", wantDigest, got)
		}

		gotAuto, err := VerifyReleaseAssetChecksumEntry(rel, "")
		if err != nil || gotAuto != wantDigest {
			t.Fatalf("VerifyReleaseAssetChecksumEntry auto asset = (%q, %v), want (%q, nil)", gotAuto, err, wantDigest)
		}
	})

	t.Run("http_404_fails_closed", func(t *testing.T) {
		body := fmt.Sprintf("%s  %s\n", wantDigest, assetName)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()
		installMockUpdaterDownloads(t, srv)

		if _, err := VerifyReleaseAssetChecksumEntry(makeReleaseWithChecksum(body), assetName); err == nil {
			t.Fatal("expected HTTP 404 on checksums.txt to fail closed")
		}
	})

	t.Run("missing_asset_entry_fails_closed", func(t *testing.T) {
		body := fmt.Sprintf("%s  unrelated_platform.tar.gz\n", strings.Repeat("b", 64))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		installMockUpdaterDownloads(t, srv)

		if _, err := VerifyReleaseAssetChecksumEntry(makeReleaseWithChecksum(body), assetName); err == nil {
			t.Fatal("expected missing asset entry in checksums.txt to fail closed")
		}
	})

	t.Run("malformed_digest_fails_closed", func(t *testing.T) {
		body := fmt.Sprintf("not-a-64-hex-sha256  %s\n%s  other.tar.gz\n", assetName, strings.Repeat("c", 64))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		defer srv.Close()
		installMockUpdaterDownloads(t, srv)

		if _, err := VerifyReleaseAssetChecksumEntry(makeReleaseWithChecksum(body), assetName); err == nil {
			t.Fatal("expected malformed digest in checksums.txt to fail closed")
		}
	})
}

func assertNoUpdaterStageRemnants(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".bv-update-") || strings.HasPrefix(name, ".bv-backup-") || strings.HasPrefix(name, ".bv-rollback-") {
			t.Fatalf("unexpected leftover updater temp file %q in %s", name, dir)
		}
	}
}

func TestPerformUpdate_StagesInBinaryDirAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script mock binary test runs on Unix")
	}

	const tag = "v99.0.0"
	assetName := getAssetName(tag)

	runScenario := func(t *testing.T, candidateScript string, wantErr bool) {
		t.Helper()
		binaryDir := t.TempDir()
		execPath := filepath.Join(binaryDir, "bv")
		originalScript := []byte("#!/bin/sh\necho 'bv v0.26.1'\n")
		if err := os.WriteFile(execPath, originalScript, 0o755); err != nil {
			t.Fatal(err)
		}

		origGetBin := getCurrentBinaryPath
		getCurrentBinaryPath = func() (string, error) { return execPath, nil }
		t.Cleanup(func() { getCurrentBinaryPath = origGetBin })

		archiveBytes := buildTestReleaseArchiveBytes(t, []byte(candidateScript))
		archiveSum := sha256.Sum256(archiveBytes)
		archiveDigest := hex.EncodeToString(archiveSum[:])
		checksumBody := fmt.Sprintf("%s  %s\n", archiveDigest, assetName)
		checksumSum := sha256.Sum256([]byte(checksumBody))

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
				_, _ = w.Write([]byte(checksumBody))
			case strings.HasSuffix(r.URL.Path, "/"+assetName):
				_, _ = w.Write(archiveBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		installMockUpdaterDownloads(t, srv)

		rel := &Release{
			TagName: tag,
			HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/" + tag,
			Assets: []Asset{
				{
					Name:               assetName,
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/" + tag + "/" + assetName,
					Size:               int64(len(archiveBytes)),
					Digest:             "sha256:" + archiveDigest,
					State:              "uploaded",
				},
				{
					Name:               "checksums.txt",
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/" + tag + "/checksums.txt",
					Size:               int64(len(checksumBody)),
					Digest:             "sha256:" + hex.EncodeToString(checksumSum[:]),
					State:              "uploaded",
				},
			},
		}

		res, err := PerformUpdate(rel, nil)
		assertNoUpdaterStageRemnants(t, binaryDir)

		if wantErr {
			if err == nil {
				t.Fatal("expected PerformUpdate to fail verification")
			}
			gotExec, readErr := os.ReadFile(execPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(gotExec, originalScript) {
				t.Fatalf("failed PerformUpdate modified existing executable: got %q, want %q", gotExec, originalScript)
			}
			return
		}

		if err != nil {
			t.Fatalf("PerformUpdate failed: %v", err)
		}
		if res == nil || !res.Success {
			t.Fatalf("unexpected UpdateResult: %#v", res)
		}
		gotExec, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotExec) != candidateScript {
			t.Fatalf("updated binary content = %q, want %q", gotExec, candidateScript)
		}
		gotBackup, err := os.ReadFile(GetBackupPath(execPath))
		if err != nil {
			t.Fatalf("expected backup file: %v", err)
		}
		if !bytes.Equal(gotBackup, originalScript) {
			t.Fatalf("backup content = %q, want %q", gotBackup, originalScript)
		}
	}

	t.Run("success_stages_and_replaces_atomically", func(t *testing.T) {
		// Verify the candidate script ran from inside binaryDir ($0 starts with binaryDir+"/.bv-update-")
		candidate := "#!/bin/sh\ncase \"$0\" in\n  */.bv-update-*|*/bv) echo 'bv v99.0.0'; exit 0 ;;\n  *) echo 'staged outside binaryDir' >&2; exit 2 ;;\nesac\n"
		runScenario(t, candidate, false)
	})

	t.Run("verification_failure_leaves_zero_remnants_and_preserves_binary", func(t *testing.T) {
		corruptCandidate := "#!/bin/sh\necho 'broken binary' >&2\nexit 1\n"
		runScenario(t, corruptCandidate, true)
	})
}

func TestRollback_ValidatesBackupBeforeReplacingExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script mock binary test runs on Unix")
	}

	setupExec := func(t *testing.T) (dir, execPath, backupPath string, workingBytes []byte) {
		t.Helper()
		dir = t.TempDir()
		execPath = filepath.Join(dir, "bv")
		backupPath = GetBackupPath(execPath)
		workingBytes = []byte("#!/bin/sh\necho 'bv v0.26.1'\n")
		if err := os.WriteFile(execPath, workingBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		origGetBin := getCurrentBinaryPath
		getCurrentBinaryPath = func() (string, error) { return execPath, nil }
		t.Cleanup(func() { getCurrentBinaryPath = origGetBin })
		return dir, execPath, backupPath, workingBytes
	}

	assertExecIntact := func(t *testing.T, dir, execPath string, workingBytes []byte) {
		t.Helper()
		got, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatalf("read working executable: %v", err)
		}
		if !bytes.Equal(got, workingBytes) {
			t.Fatalf("working executable was corrupted by rejected Rollback: got %q, want %q", got, workingBytes)
		}
		assertNoUpdaterStageRemnants(t, dir)
	}

	t.Run("rejects_symlink_backup", func(t *testing.T) {
		dir, execPath, backupPath, workingBytes := setupExec(t)
		target := filepath.Join(dir, "elsewhere.bin")
		if err := os.WriteFile(target, []byte("#!/bin/sh\necho 'bv v0.25.0'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, backupPath); err != nil {
			t.Fatal(err)
		}

		if err := Rollback(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("expected symlink backup rejection, got %v", err)
		}
		assertExecIntact(t, dir, execPath, workingBytes)
	})

	t.Run("rejects_zero_byte_backup", func(t *testing.T) {
		dir, execPath, backupPath, workingBytes := setupExec(t)
		if err := os.WriteFile(backupPath, nil, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := Rollback(); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("expected 0-byte backup rejection, got %v", err)
		}
		assertExecIntact(t, dir, execPath, workingBytes)
	})

	t.Run("rejects_non_executable_backup", func(t *testing.T) {
		dir, execPath, backupPath, workingBytes := setupExec(t)
		if err := os.WriteFile(backupPath, []byte("#!/bin/sh\necho 'bv v0.25.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if err := Rollback(); err == nil || !strings.Contains(err.Error(), "not executable") {
			t.Fatalf("expected non-executable backup rejection, got %v", err)
		}
		assertExecIntact(t, dir, execPath, workingBytes)
	})

	t.Run("rejects_corrupt_or_nonzero_exiting_backup", func(t *testing.T) {
		dir, execPath, backupPath, workingBytes := setupExec(t)
		if err := os.WriteFile(backupPath, []byte("#!/bin/sh\necho 'segfault' >&2\nexit 42\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := Rollback(); err == nil || !strings.Contains(err.Error(), "smoke probe failed") {
			t.Fatalf("expected non-zero --version backup rejection, got %v", err)
		}
		assertExecIntact(t, dir, execPath, workingBytes)
	})

	t.Run("succeeds_on_valid_executable_backup", func(t *testing.T) {
		dir, execPath, backupPath, _ := setupExec(t)
		validBackup := []byte("#!/bin/sh\necho 'bv v0.25.0'\n")
		if err := os.WriteFile(backupPath, validBackup, 0o755); err != nil {
			t.Fatal(err)
		}
		beforeInfo, err := os.Stat(execPath)
		if err != nil {
			t.Fatal(err)
		}

		if err := Rollback(); err != nil {
			t.Fatalf("expected valid Rollback to succeed, got %v", err)
		}
		assertNoUpdaterStageRemnants(t, dir)

		got, err := os.ReadFile(execPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, validBackup) {
			t.Fatalf("restored binary = %q, want %q", got, validBackup)
		}
		afterInfo, err := os.Stat(execPath)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(beforeInfo, afterInfo) {
			t.Fatal("expected Rollback to replace executable via atomic rename onto a new inode")
		}
	})
}
