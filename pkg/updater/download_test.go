package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestDownloadFile_SizeVerified_Success(t *testing.T) {
	body := []byte("hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := downloadFile(srv.URL, dest, 5); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("dest content=%q; want %q", string(got), "hello")
	}
}

func TestDownloadFile_SizeMismatch_Header(t *testing.T) {
	body := []byte("abcd")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := downloadFile(srv.URL, dest, 5); err == nil {
		t.Fatalf("expected size mismatch error")
	}
}

func TestDownloadFile_RejectsNegativeExpectedSize(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := downloadFile("http://127.0.0.1:1/file.bin", dest, -1); err == nil {
		t.Fatalf("expected negative size error")
	}
}

func TestDownloadFile_SizeMismatch_WrittenBytes(t *testing.T) {
	// Force chunked transfer so ContentLength is not available to the client;
	// then rely on the post-download byte-count check.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("abcd"))
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := downloadFile(srv.URL, dest, 5); err == nil {
		t.Fatalf("expected downloaded size mismatch error")
	}
}

func TestDownloadFile_SizeMismatchStopsAfterExpectedPlusOne(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "file.bin")
	if err := downloadFile(srv.URL, dest, 5); err == nil {
		t.Fatalf("expected downloaded size mismatch error")
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Size() > 6 {
		t.Fatalf("download wrote %d bytes; want at most expected size plus one", info.Size())
	}
}

type recordedByteProgress struct {
	mu      sync.Mutex
	lines   []string
	samples [][2]int64
}

func (r *recordedByteProgress) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.lines = append(r.lines, string(p))
	r.mu.Unlock()
	return len(p), nil
}

func (r *recordedByteProgress) ReportBytes(downloaded, total int64) {
	r.mu.Lock()
	r.samples = append(r.samples, [2]int64{downloaded, total})
	r.mu.Unlock()
}

func (r *recordedByteProgress) snapshotSamples() [][2]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]int64, len(r.samples))
	copy(out, r.samples)
	return out
}

func TestDownloadFileWithProgressAndPerformUpdate_ChunkedMonotonicProgress(t *testing.T) {
	// 1. Direct downloadFileWithProgress against a chunked httptest.Server with explicit flushes.
	chunkSize := 4096
	numChunks := 6
	payload := bytes.Repeat([]byte("beads-progress-"), (chunkSize*numChunks)/15+1)[:chunkSize*numChunks]
	wantTotal := int64(len(payload))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for offset := 0; offset < len(payload); offset += chunkSize {
			end := min(len(payload), offset+chunkSize)
			_, _ = w.Write(payload[offset:end])
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	authorizeUpdaterHTTPTest(t)

	dest := filepath.Join(t.TempDir(), "chunked.bin")
	rec := &recordedByteProgress{}
	if err := downloadFileWithProgress(srv.URL, dest, wantTotal, rec); err != nil {
		t.Fatalf("downloadFileWithProgress: %v", err)
	}
	samples := rec.snapshotSamples()
	if len(samples) < 2 {
		t.Fatalf("expected initial 0-byte report plus at least 1 read report, got %v", samples)
	}
	if samples[0] != ([2]int64{0, wantTotal}) {
		t.Fatalf("initial sample = %v, want [0 %d]", samples[0], wantTotal)
	}
	prev := int64(0)
	for i, s := range samples {
		if s[1] != wantTotal {
			t.Fatalf("sample %d total = %d, want %d", i, s[1], wantTotal)
		}
		if s[0] < prev || (i > 0 && s[0] <= 0) {
			t.Fatalf("non-monotonic byte progress at sample %d: %v (prev=%d)", i, s, prev)
		}
		prev = s[0]
	}
	if samples[len(samples)-1][0] != wantTotal {
		t.Fatalf("final downloaded bytes = %d, want %d", samples[len(samples)-1][0], wantTotal)
	}

	// Planted negative control: a plain io.Writer without ReportBytes still downloads cleanly with 0 byte samples.
	var plainBuf bytes.Buffer
	destPlain := filepath.Join(t.TempDir(), "plain.bin")
	if err := downloadFileWithProgress(srv.URL, destPlain, wantTotal, &plainBuf); err != nil {
		t.Fatalf("downloadFileWithProgress plain writer: %v", err)
	}
	if gotBytes, err := os.ReadFile(destPlain); err != nil || int64(len(gotBytes)) != wantTotal {
		t.Fatalf("plain writer file size = %d err=%v, want %d", len(gotBytes), err, wantTotal)
	}

	// 2. End-to-end PerformUpdate streaming chunked archive bytes into ReportBytes.
	if runtime.GOOS == "windows" {
		return
	}
	const tag = "v99.1.0"
	assetName := getAssetName(tag)
	binaryDir := t.TempDir()
	execPath := filepath.Join(binaryDir, "bv")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\necho 'bv v0.26.1'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origGetBin := getCurrentBinaryPath
	getCurrentBinaryPath = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { getCurrentBinaryPath = origGetBin })

	// Pad script comment so archive exceeds multiple chunks.
	script := "#!/bin/sh\n# " + strings.Repeat("padding-", 2048) + "\necho 'bv " + tag + "'\n"
	archiveBytes := buildTestReleaseArchiveBytes(t, []byte(script))
	archiveSum := sha256.Sum256(archiveBytes)
	archiveDigest := hex.EncodeToString(archiveSum[:])
	checksumBody := fmt.Sprintf("%s  %s\n", archiveDigest, assetName)
	checksumSum := sha256.Sum256([]byte(checksumBody))

	updateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write([]byte(checksumBody))
		case strings.HasSuffix(r.URL.Path, "/"+assetName):
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			step := max(1, len(archiveBytes)/4)
			for off := 0; off < len(archiveBytes); off += step {
				end := min(len(archiveBytes), off+step)
				_, _ = w.Write(archiveBytes[off:end])
				if flusher != nil {
					flusher.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(updateSrv.Close)
	installMockUpdaterDownloads(t, updateSrv)

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

	updateRec := &recordedByteProgress{}
	res, err := PerformUpdate(rel, updateRec)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("PerformUpdate failed: res=%+v err=%v", res, err)
	}
	uSamples := updateRec.snapshotSamples()
	wantArchiveTotal := int64(len(archiveBytes))
	if len(uSamples) < 2 || uSamples[0] != ([2]int64{0, wantArchiveTotal}) || uSamples[len(uSamples)-1] != ([2]int64{wantArchiveTotal, wantArchiveTotal}) {
		t.Fatalf("PerformUpdate byte samples = %v, want [0 %d] ... [%d %d]", uSamples, wantArchiveTotal, wantArchiveTotal, wantArchiveTotal)
	}
	for i := 1; i < len(uSamples); i++ {
		if uSamples[i][0] < uSamples[i-1][0] || uSamples[i][1] != wantArchiveTotal {
			t.Fatalf("PerformUpdate non-monotonic sample at %d: %v after %v", i, uSamples[i], uSamples[i-1])
		}
	}
}

func TestVersionProbeScrubsRobotAndOutputFormatEnv(t *testing.T) {
	cleaned := cleanVersionProbeEnv([]string{
		"PATH=/usr/bin",
		"BV_ROBOT=1",
		"bv_robot=true",
		"BV_OUTPUT_FORMAT=json",
		"Bv_Output_Format=toon",
		"TOON_DEFAULT_FORMAT=toon",
		"toon_default_format=json",
		"BV_OTHER=keep",
	})
	if len(cleaned) != 2 || cleaned[0] != "PATH=/usr/bin" || cleaned[1] != "BV_OTHER=keep" {
		t.Fatalf("cleanVersionProbeEnv returned %v, want [PATH=/usr/bin BV_OTHER=keep]", cleaned)
	}
	if empty := cleanVersionProbeEnv([]string{"BV_ROBOT=1", "TOON_DEFAULT_FORMAT=toon"}); empty == nil || len(empty) != 0 {
		t.Fatalf("expected non-nil empty slice so exec.Cmd does not fall back to os.Environ(), got %#v", empty)
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell-script mock binary test runs on Unix")
	}

	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_OUTPUT_FORMAT", "json")
	t.Setenv("TOON_DEFAULT_FORMAT", "toon")

	const tag = "v0.99.0"
	assetName := getAssetName(tag)
	binaryDir := t.TempDir()
	execPath := filepath.Join(binaryDir, "bv")
	initialScript := []byte("#!/bin/sh\nif [ -n \"$BV_ROBOT\" ] || [ -n \"$BV_OUTPUT_FORMAT\" ] || [ -n \"$TOON_DEFAULT_FORMAT\" ]; then\n  echo '{\"version\":\"v0.26.1\"}'\n  exit 2\nfi\necho 'bv v0.26.1'\n")
	if err := os.WriteFile(execPath, initialScript, 0o755); err != nil {
		t.Fatal(err)
	}
	origGetBin := getCurrentBinaryPath
	getCurrentBinaryPath = func() (string, error) { return execPath, nil }
	t.Cleanup(func() { getCurrentBinaryPath = origGetBin })

	makeReleaseAndServer := func(t *testing.T, script string) *Release {
		t.Helper()
		archiveBytes := buildTestReleaseArchiveBytes(t, []byte(script))
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
		t.Cleanup(srv.Close)
		installMockUpdaterDownloads(t, srv)

		return &Release{
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
	}

	// Positive observable: candidate emits JSON when BV_ROBOT or BV_OUTPUT_FORMAT is non-empty,
	// and "bv v0.99.0" otherwise; PerformUpdate and Rollback both succeed under BV_ROBOT=1 BV_OUTPUT_FORMAT=json.
	validCandidate := "#!/bin/sh\nif [ -n \"$BV_ROBOT\" ] || [ -n \"$BV_OUTPUT_FORMAT\" ]; then\n  echo '{\"version\":\"v0.99.0\"}'\n  exit 0\nfi\necho 'bv v0.99.0'\n"
	validRel := makeReleaseAndServer(t, validCandidate)
	res, err := PerformUpdate(validRel, io.Discard)
	if err != nil || res == nil || !res.Success {
		t.Fatalf("expected PerformUpdate to scrub BV_ROBOT/BV_OUTPUT_FORMAT and succeed, got res=%+v err=%v", res, err)
	}
	if err := Rollback(); err != nil {
		t.Fatalf("expected Rollback to scrub BV_ROBOT/BV_OUTPUT_FORMAT and succeed, got %v", err)
	}
	restoredBytes, err := os.ReadFile(execPath)
	if err != nil || !bytes.Equal(restoredBytes, initialScript) {
		t.Fatalf("Rollback did not restore initial binary: got %q err=%v", restoredBytes, err)
	}

	// Planted negative control: candidate that unconditionally prints broken/JSON --version output is still rejected.
	brokenCandidate := "#!/bin/sh\necho '{\"version\":\"v0.99.0\"}'\nexit 0\n"
	brokenRel := makeReleaseAndServer(t, brokenCandidate)
	if brokenRes, brokenErr := PerformUpdate(brokenRel, io.Discard); brokenErr == nil || !strings.Contains(brokenErr.Error(), "unexpected --version output") {
		t.Fatalf("negative control failed: expected PerformUpdate rejection on broken --version output, got res=%+v err=%v", brokenRes, brokenErr)
	}
	stillInitial, err := os.ReadFile(execPath)
	if err != nil || !bytes.Equal(stillInitial, initialScript) {
		t.Fatalf("rejected PerformUpdate altered installed binary: got %q err=%v", stillInitial, err)
	}
}
