package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testSHA256DigestA   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSHA256DigestB   = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testVersionProbeEnv = "BV_UPDATER_TEST_VERSION_PROBE"
)

// TestMain also provides a portable candidate binary for verifyBinaryVersion.
// The exact argument and environment gate keep ordinary test runs unaffected.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--version" && os.Getenv(testVersionProbeEnv) != "" {
		output := "bv v1.2.3\n"
		switch os.Getenv(testVersionProbeEnv) {
		case "normal":
		case "at-limit":
			output += strings.Repeat(" ", maxBinaryVersionOutputBytes-len(output))
		case "over-limit":
			output += strings.Repeat(" ", maxBinaryVersionOutputBytes+1-len(output))
		case "large-output":
			output += strings.Repeat(" ", 16*maxBinaryVersionOutputBytes-len(output))
		case "failing-stderr":
			_, err := io.WriteString(os.Stderr, strings.Repeat("E", 256<<10)+"unretained-stderr-tail")
			if err != nil {
				os.Exit(99)
			}
			os.Exit(17)
		default:
			os.Exit(98)
		}
		if _, err := io.WriteString(os.Stdout, output); err != nil {
			os.Exit(99)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRelease_FindPlatformAsset(t *testing.T) {
	rel := &Release{TagName: "v1.2.3"}
	target := stableAssetName()
	rel.Assets = []Asset{
		{Name: "other.tar.gz"},
		{Name: target, BrowserDownloadURL: "http://example.com/bv.tgz"},
	}

	asset := rel.FindPlatformAsset()
	if asset == nil {
		t.Fatalf("expected platform asset %q", target)
	}
	if asset.Name != target {
		t.Fatalf("expected %q, got %q", target, asset.Name)
	}
}

func TestRelease_FindPlatformAsset_LegacyVersionedFallback(t *testing.T) {
	rel := &Release{TagName: "v1.2.3"}
	legacyTarget := getAssetName(rel.TagName)
	rel.Assets = []Asset{
		{Name: "other.tar.gz"},
		{Name: legacyTarget, BrowserDownloadURL: "http://example.com/bv.tgz"},
	}

	asset := rel.FindPlatformAsset()
	if asset == nil {
		t.Fatalf("expected legacy platform asset %q", legacyTarget)
	}
	if asset.Name != legacyTarget {
		t.Fatalf("expected %q, got %q", legacyTarget, asset.Name)
	}
}

func TestRelease_FindPlatformAssetWithChecksumFallsBackToCheckedLegacyName(t *testing.T) {
	rel := &Release{TagName: "v1.2.3"}
	stableTarget := stableAssetName()
	legacyTarget := getAssetName(rel.TagName)
	rel.Assets = []Asset{
		{Name: stableTarget, BrowserDownloadURL: "http://example.com/stable"},
		{Name: legacyTarget, BrowserDownloadURL: "http://example.com/legacy"},
	}

	asset := rel.findPlatformAssetWithChecksum(map[string]string{
		legacyTarget: "hash",
	})
	if asset == nil {
		t.Fatalf("expected checked legacy asset %q", legacyTarget)
	}
	if asset.Name != legacyTarget {
		t.Fatalf("expected %q, got %q", legacyTarget, asset.Name)
	}
}

func TestRelease_FindPlatformAssetWithChecksumPrefersCheckedVersionedName(t *testing.T) {
	// Since #195 releases ship bv_<version>_<os>_<arch>; when a release still
	// carries the old unversioned archive as well, the versioned one wins.
	rel := &Release{TagName: "v1.2.3"}
	unversioned := stableAssetName()
	versioned := getAssetName(rel.TagName)
	rel.Assets = []Asset{
		{Name: unversioned, BrowserDownloadURL: "http://example.com/unversioned"},
		{Name: versioned, BrowserDownloadURL: "http://example.com/versioned"},
	}

	asset := rel.findPlatformAssetWithChecksum(map[string]string{
		unversioned: "hash",
		versioned:   "hash",
	})
	if asset == nil {
		t.Fatalf("expected checked versioned asset %q", versioned)
	}
	if asset.Name != versioned {
		t.Fatalf("expected %q, got %q", versioned, asset.Name)
	}
}

func TestRelease_FindChecksumAsset(t *testing.T) {
	rel := &Release{
		Assets: []Asset{
			{Name: "bv_v1.0.0_darwin_arm64.tar.gz"},
			{Name: "checksums.txt", BrowserDownloadURL: "http://example.com/checksums"},
		},
	}
	asset := rel.FindChecksumAsset()
	if asset == nil || asset.Name != "checksums.txt" {
		t.Fatalf("expected checksums.txt asset, got %#v", asset)
	}
}

func TestValidateReleaseForUpdate(t *testing.T) {
	newRelease := func() *Release {
		return &Release{
			TagName: "v99.0.0",
			HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v99.0.0",
			Assets: []Asset{
				{
					Name:               stableAssetName(),
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v99.0.0/" + stableAssetName(),
					Size:               1024,
					Digest:             testSHA256DigestA,
					State:              "uploaded",
				},
				{
					Name:               "checksums.txt",
					BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v99.0.0/checksums.txt",
					Size:               256,
					Digest:             testSHA256DigestB,
					State:              "uploaded",
				},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*Release)
	}{
		{"draft", func(release *Release) { release.Draft = true }},
		{"GitHub prerelease", func(release *Release) { release.Prerelease = true }},
		{"prerelease tag", func(release *Release) { release.TagName = "v99.0.0-rc.1" }},
		{"partial tag", func(release *Release) { release.TagName = "v99.0" }},
		{"malformed tag", func(release *Release) { release.TagName = "v99.0.0.1" }},
		{"non-GitHub release page", func(release *Release) { release.HTMLURL = "https://example.com/release" }},
		{"wrong release repository", func(release *Release) { release.HTMLURL = "https://github.com/other/repo/releases/tag/v99.0.0" }},
		{"wrong release tag path", func(release *Release) {
			release.HTMLURL = "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/v98.0.0"
		}},
		{"missing platform asset", func(release *Release) { release.Assets = release.Assets[1:] }},
		{"zero-sized platform asset", func(release *Release) { release.Assets[0].Size = 0 }},
		{"platform asset still uploading", func(release *Release) { release.Assets[0].State = "new" }},
		{"non-HTTPS platform asset", func(release *Release) { release.Assets[0].BrowserDownloadURL = "http://github.com/file" }},
		{"wrong platform repository", func(release *Release) {
			release.Assets[0].BrowserDownloadURL = "https://github.com/other/repo/releases/download/v99.0.0/" + stableAssetName()
		}},
		{"wrong platform tag path", func(release *Release) {
			release.Assets[0].BrowserDownloadURL = "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v98.0.0/" + stableAssetName()
		}},
		{"missing platform digest", func(release *Release) { release.Assets[0].Digest = "" }},
		{"malformed platform digest", func(release *Release) { release.Assets[0].Digest = "sha256:not-a-hash" }},
		{"missing checksum asset", func(release *Release) { release.Assets = release.Assets[:1] }},
		{"empty checksum asset", func(release *Release) { release.Assets[1].Size = 0 }},
		{"oversized checksum asset", func(release *Release) { release.Assets[1].Size = maxChecksumManifestBytes + 1 }},
		{"wrong checksum tag path", func(release *Release) {
			release.Assets[1].BrowserDownloadURL = "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v98.0.0/checksums.txt"
		}},
		{"missing checksum digest", func(release *Release) { release.Assets[1].Digest = "" }},
	}

	if err := ValidateReleaseForUpdate(newRelease()); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
	if err := ValidateReleaseForUpdate(nil); err == nil {
		t.Fatal("nil release accepted")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := newRelease()
			tt.mutate(release)
			if err := ValidateReleaseForUpdate(release); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestValidateReleaseIdentityRejectsTagWhitespace(t *testing.T) {
	release := &Release{
		TagName: " v99.0.0 ",
		HTMLURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/tag/%20v99.0.0%20",
	}
	if err := validateReleaseIdentity(release); err == nil {
		t.Fatal("release tag with surrounding whitespace was accepted")
	}
}

func TestReleaseFindersHandleNilReceiver(t *testing.T) {
	var release *Release
	if release.FindPlatformAsset() != nil {
		t.Fatal("nil release returned a platform asset")
	}
	if release.FindChecksumAsset() != nil {
		t.Fatal("nil release returned a checksum asset")
	}
}

func TestPerformUpdateRejectsNilRelease(t *testing.T) {
	if _, err := PerformUpdate(nil, nil); err == nil {
		t.Fatal("PerformUpdate accepted nil release metadata")
	}
}

func TestCheckedPlatformAssetRequiresDigestAgreement(t *testing.T) {
	assetName := stableAssetName()
	release := &Release{
		TagName: "v99.0.0",
		Assets: []Asset{
			{
				Name:               assetName,
				BrowserDownloadURL: "https://github.com/Dicklesworthstone/beads_viewer/releases/download/v99.0.0/" + assetName,
				Size:               1024,
				Digest:             testSHA256DigestA,
				State:              "uploaded",
			},
		},
	}
	wantDigest := testSHA256DigestA[len("sha256:"):]
	asset, digest, err := checkedPlatformAsset(release, map[string]string{assetName: wantDigest})
	if err != nil {
		t.Fatalf("matching digests rejected: %v", err)
	}
	if asset == nil || asset.Name != assetName || digest != wantDigest {
		t.Fatalf("unexpected checked asset: asset=%#v digest=%q", asset, digest)
	}
	if _, _, err := checkedPlatformAsset(release, map[string]string{assetName: testSHA256DigestB[len("sha256:"):]}); err == nil {
		t.Fatal("disagreement between checksums.txt and API digest accepted")
	}
	if _, _, err := checkedPlatformAsset(release, nil); err == nil {
		t.Fatal("missing checksum entry accepted")
	}
}

func TestCheckBinaryDirectoryWritablePreservesExistingProbeLikeFile(t *testing.T) {
	dir := t.TempDir()
	sentinelPath := filepath.Join(dir, ".bv-update-test")
	want := []byte("do not truncate")
	if err := os.WriteFile(sentinelPath, want, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := checkBinaryDirectoryWritable(dir); err != nil {
		t.Fatalf("writable directory rejected: %v", err)
	}
	got, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("sentinel changed: got %q want %q", got, want)
	}
}

func TestParseBinaryVersionOutput(t *testing.T) {
	version, err := parseBinaryVersionOutput([]byte("bv v1.2.3\n"))
	if err != nil {
		t.Fatalf("valid version output rejected: %v", err)
	}
	if version != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", version)
	}
	for _, output := range []string{"", "v1.2.3", "bv latest", "bv v1.2.3 extra"} {
		if _, err := parseBinaryVersionOutput([]byte(output)); err == nil {
			t.Errorf("invalid version output %q accepted", output)
		}
	}
}

func TestLimitedOutputBufferCapsData(t *testing.T) {
	var buffer limitedOutputBuffer
	data := make([]byte, maxBinaryVersionOutputBytes+1)
	written, err := buffer.Write(data)
	if err != nil {
		t.Fatalf("limited buffer write: %v", err)
	}
	if written != len(data) {
		t.Fatalf("Write reported %d bytes, want %d", written, len(data))
	}
	if !buffer.truncated || buffer.data.Len() != maxBinaryVersionOutputBytes {
		t.Fatalf("buffer state: truncated=%v len=%d", buffer.truncated, buffer.data.Len())
	}
}

func TestLimitedOutputBufferCapsIOCopy(t *testing.T) {
	for _, size := range []int{0, maxBinaryVersionOutputBytes, maxBinaryVersionOutputBytes + 1, 16 * maxBinaryVersionOutputBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := strings.Repeat("x", size)
			var buffer limitedOutputBuffer
			// LimitReader hides strings.Reader.WriteTo so io.Copy takes the same
			// destination-interface path used when os/exec drains child pipes.
			written, err := io.Copy(&buffer, io.LimitReader(strings.NewReader(payload), int64(size)))
			if err != nil || written != int64(size) {
				t.Fatalf("Copy = (%d, %v), want (%d, nil)", written, err, size)
			}
			wantSize := min(size, maxBinaryVersionOutputBytes)
			if buffer.data.Len() != wantSize || buffer.data.String() != payload[:wantSize] {
				t.Fatalf("stored %d bytes, want the first %d bytes", buffer.data.Len(), wantSize)
			}
			if buffer.truncated != (size > maxBinaryVersionOutputBytes) {
				t.Fatalf("truncated = %v for %d-byte output", buffer.truncated, size)
			}
		})
	}
}

func TestVerifyBinaryVersionBoundsChildOutput(t *testing.T) {
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	for _, mode := range []string{"normal", "at-limit", "over-limit", "large-output", "failing-stderr"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(testVersionProbeEnv, mode)
			err := verifyBinaryVersion(binaryPath, "v1.2.3")
			switch mode {
			case "normal", "at-limit":
				if err != nil {
					t.Fatalf("valid candidate rejected: %v", err)
				}
			case "over-limit", "large-output":
				if err == nil || !strings.Contains(err.Error(), "--version output exceeds") {
					t.Fatalf("oversized version output returned %v", err)
				}
			case "failing-stderr":
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
					t.Fatalf("candidate exit status was lost: %v", err)
				}
				if len(err.Error()) > maxBinaryVersionOutputBytes+256 {
					t.Fatalf("stderr diagnostic contains %d bytes, exceeding the capture limit", len(err.Error()))
				}
				if !strings.Contains(err.Error(), strings.Repeat("E", maxBinaryVersionOutputBytes)) {
					t.Fatal("stderr diagnostic lost the retained prefix")
				}
				if strings.Contains(err.Error(), "unretained-stderr-tail") {
					t.Fatal("stderr diagnostic includes output beyond the capture limit")
				}
			}
		})
	}
}

func TestGetAssetName_UsesRuntimeAndTrimsV(t *testing.T) {
	name := getAssetName("v9.8.7")
	want := "bv_9.8.7_" + runtime.GOOS + "_" + runtime.GOARCH + platformArchiveExtension(runtime.GOOS)
	if name != want {
		t.Fatalf("getAssetName mismatch: got %q want %q", name, want)
	}
}

func TestPlatformArchiveExtension(t *testing.T) {
	if got := platformArchiveExtension("windows"); got != ".zip" {
		t.Fatalf("windows extension=%q, want .zip", got)
	}
	if got := platformArchiveExtension("linux"); got != ".tar.gz" {
		t.Fatalf("linux extension=%q, want .tar.gz", got)
	}
}

func TestParseChecksums(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "checksums.txt")

	content := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  bv_1.0.0_darwin_arm64.tar.gz\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  checksums.txt\n" +
		"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	m, err := parseChecksums(path)
	if err != nil {
		t.Fatalf("parseChecksums failed: %v", err)
	}
	if got := m["bv_1.0.0_darwin_arm64.tar.gz"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected checksum for archive: %q", got)
	}
	if got := m["checksums.txt"]; got != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("unexpected checksum for checksums.txt: %q", got)
	}
}

func TestParseChecksums_FilenamesWithSpaces(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "checksums.txt")

	content := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  bv 1.0.0 windows amd64.tar.gz\n" +
		"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	m, err := parseChecksums(path)
	if err != nil {
		t.Fatalf("parseChecksums failed: %v", err)
	}
	if got := m["bv 1.0.0 windows amd64.tar.gz"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected checksum for spaced filename: %q", got)
	}
}

func TestParseChecksums_NormalizesUppercaseAndSkipsInvalidHashes(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "checksums.txt")

	content := "" +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  bv_1.0.0_linux_amd64.tar.gz\n" +
		"not-a-sha256  ignored.tar.gz\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	m, err := parseChecksums(path)
	if err != nil {
		t.Fatalf("parseChecksums failed: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected one valid checksum, got %d: %#v", len(m), m)
	}
	if got := m["bv_1.0.0_linux_amd64.tar.gz"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected normalized checksum: %q", got)
	}
}

func TestParseChecksums_RejectsDuplicateFilename(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "checksums.txt")

	content := "" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  bv.tar.gz\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  bv.tar.gz\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}

	if _, err := parseChecksums(path); err == nil {
		t.Fatalf("expected duplicate checksum entry error")
	}
}

func TestParseChecksums_RejectsOversizedManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	if err := os.WriteFile(path, make([]byte, maxChecksumManifestBytes+1), 0o644); err != nil {
		t.Fatalf("write oversized checksums: %v", err)
	}
	if _, err := parseChecksums(path); err == nil {
		t.Fatal("oversized checksum manifest accepted")
	}
}

func TestVerifyChecksum(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "file.bin")
	data := []byte("hello updater")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	sum := sha256.Sum256(data)
	okHash := hex.EncodeToString(sum[:])

	if err := verifyChecksum(path, okHash); err != nil {
		t.Fatalf("verifyChecksum expected ok, got %v", err)
	}
	if err := verifyChecksum(path, "deadbeef"); err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
}
