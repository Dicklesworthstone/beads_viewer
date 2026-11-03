package export

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPreviewRequestGuard(t *testing.T) {
	for _, tc := range []struct {
		name, host, method string
		origins            []string
		want               int
	}{
		{name: "IPv4", host: "127.0.0.1:9000", want: 200},
		{name: "localhost", host: "localhost:9000", origins: []string{"http://localhost:9000"}, want: 200},
		{name: "domain case", host: "LOCALHOST:9000", origins: []string{"http://localhost:9000"}, want: 200},
		{name: "IPv6", host: "[::1]:9000", origins: []string{"http://[::1]:9000"}, want: 200},
		{name: "same origin", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000"}, want: 200},
		{name: "HEAD", host: "127.0.0.1:9000", method: "HEAD", want: 200},
		{name: "rebinding", host: "rebind.invalid:9000", origins: []string{"http://rebind.invalid:9000"}, want: 403},
		{name: "rebinding without origin", host: "rebind.invalid:9000", want: 403},
		{name: "localhost suffix", host: "localhost.evil.invalid:9000", want: 403},
		{name: "nonloopback", host: "192.0.2.1:9000", want: 403},
		{name: "wrong port", host: "127.0.0.1:9001", want: 403},
		{name: "missing port", host: "localhost", want: 403},
		{name: "signed port", host: "127.0.0.1:+9000", want: 403},
		{name: "empty authority", want: 403},
		{name: "host userinfo", host: "user@localhost:9000", want: 403},
		{name: "remote origin", host: "127.0.0.1:9000", origins: []string{"https://untrusted.invalid"}, want: 403},
		{name: "null origin", host: "127.0.0.1:9000", origins: []string{"null"}, want: 403},
		{name: "empty origin", host: "127.0.0.1:9000", origins: []string{""}, want: 403},
		{name: "different local origin", host: "127.0.0.1:9000", origins: []string{"http://localhost:9000"}, want: 403},
		{name: "origin port", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9001"}, want: 403},
		{name: "origin scheme", host: "127.0.0.1:9000", origins: []string{"https://127.0.0.1:9000"}, want: 403},
		{name: "origin credentials", host: "127.0.0.1:9000", origins: []string{"http://user@127.0.0.1:9000"}, want: 403},
		{name: "origin path", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000/"}, want: 403},
		{name: "origin query", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000?"}, want: 403},
		{name: "origin fragment", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000#"}, want: 403},
		{name: "multiple origins", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000", "https://untrusted.invalid"}, want: 403},
		{name: "origin list", host: "127.0.0.1:9000", origins: []string{"http://127.0.0.1:9000 https://untrusted.invalid"}, want: 403},
		{name: "POST", host: "127.0.0.1:9000", method: "POST", want: 405},
		{name: "OPTIONS", host: "127.0.0.1:9000", method: "OPTIONS", want: 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "http://127.0.0.1:9000/beads.sqlite3", nil)
			req.Host = tc.host
			for _, origin := range tc.origins {
				req.Header.Add("Origin", origin)
			}
			recorder := httptest.NewRecorder()
			called := false
			previewRequestGuard(9000, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				_, _ = io.WriteString(w, "fake private project data")
			})).ServeHTTP(recorder, req)
			if recorder.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("status=%d handler called=%v want=%d", recorder.Code, called, tc.want)
			}
			if recorder.Header().Get("Access-Control-Allow-Origin") != "" || recorder.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
				t.Fatalf("unexpected browser policy headers: %v", recorder.Header())
			}
			if tc.want != 200 && strings.Contains(recorder.Body.String(), "fake private") {
				t.Fatal("denied request exposed project bytes")
			}
		})
	}
}

func startPreviewTestServer(t *testing.T, bundle string, live bool) (*PreviewServer, <-chan struct{}) {
	t.Helper()
	server := NewPreviewServer(bundle, 0)
	done := make(chan struct{})
	var serveErr error
	go func() {
		serveErr = server.startWithGracefulShutdown(PreviewConfig{Quiet: true, LiveReload: live})
		close(done)
	}()
	t.Cleanup(func() {
		if err := server.Stop(); err != nil {
			t.Errorf("stop preview: %v", err)
		}
		select {
		case <-done:
			if serveErr != nil {
				t.Errorf("preview serve: %v", serveErr)
			}
		case <-time.After(6 * time.Second):
			t.Error("preview owner did not finish")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for server.Port() == 0 {
		select {
		case <-done:
			t.Fatalf("preview exited before publishing its port: %v", serveErr)
		case <-deadline.C:
			t.Fatal("preview did not publish its bound port")
		case <-poll.C:
		}
	}
	return server, done
}

func TestPreviewServerLocalOriginAndRequests(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprintf("live=%t", live), func(t *testing.T) {
			bundle := t.TempDir()
			const data = "FAKE-PREVIEW-DATABASE-SENTINEL"
			for name, content := range map[string]string{
				"index.html":    "<!doctype html><html><body>Preview fixture</body></html>",
				"beads.sqlite3": data,
			} {
				if err := os.WriteFile(filepath.Join(bundle, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			server, done := startPreviewTestServer(t, bundle, live)
			client := &http.Client{Timeout: 2 * time.Second}
			for _, target := range []string{"/", "/beads.sqlite3", "/__preview__/status", "/__preview__/events", LiveReloadScriptPath} {
				for _, hostileHost := range []bool{true, false} {
					req, err := http.NewRequest(http.MethodGet, server.URL()+target, nil)
					if err != nil {
						t.Fatal(err)
					}
					if hostileHost {
						req.Host = fmt.Sprintf("rebind.invalid:%d", server.Port())
						req.Header.Set("Origin", "http://"+req.Host)
					} else {
						req.Header.Set("Origin", "https://untrusted.invalid")
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
					resp.Body.Close()
					if readErr != nil || resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), data) || strings.Contains(string(body), bundle) {
						t.Fatalf("hostile request %s: status=%d body=%q error=%v", target, resp.StatusCode, body, readErr)
					}
				}
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req, err := http.NewRequest(method, server.URL()+"/beads.sqlite3", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Host = fmt.Sprintf("localhost:%d", server.Port())
				req.Header.Set("Origin", "http://"+req.Host)
				if method == http.MethodGet {
					req.Header.Set("Range", "bytes=2-5")
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if method == http.MethodGet && (resp.StatusCode != http.StatusPartialContent || string(body) != data[2:6]) {
					t.Fatalf("range request: status=%d body=%q", resp.StatusCode, body)
				}
				if method == http.MethodHead && (resp.StatusCode != http.StatusOK || len(body) != 0 || resp.ContentLength != int64(len(data))) {
					t.Fatalf("HEAD request: status=%d body=%q length=%d", resp.StatusCode, body, resp.ContentLength)
				}
			}
			if live {
				resp, err := client.Get(server.URL() + "/__preview__/events")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				line, err := bufio.NewReader(resp.Body).ReadString('\n')
				if err != nil || line != "event: connected\n" || resp.Header.Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("same-origin SSE: line=%q headers=%v err=%v", line, resp.Header, err)
				}
				// Stop must close SSE before waiting for handlers and releasing
				// the root. Keep this connection open while stopping the server.
				if err := server.Stop(); err != nil {
					t.Fatalf("shutdown with active SSE: %v", err)
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("active SSE prevented preview owner from finishing")
				}
				if err := os.Rename(bundle, bundle+"-closed"); err != nil {
					t.Fatalf("preview retained the bundle directory after shutdown: %v", err)
				}
			}
		})
	}
}

func TestPreviewFileSystemKeepsSelectedRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents renaming open root directories")
	}
	base := t.TempDir()
	bundle := filepath.Join(base, "selected")
	if err := os.MkdirAll(filepath.Join(bundle, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "data/issue.json"} {
		if err := os.WriteFile(filepath.Join(bundle, name), []byte("selected bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previewFS, err := validatePreviewBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer previewFS.Close()
	moved := filepath.Join(base, "moved")
	if err := os.Rename(bundle, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("unselected replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := previewFS.Open("/index.html")
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(file)
	file.Close()
	if readErr != nil || string(content) != "selected bundle" {
		t.Fatalf("root replacement redirected preview: %q, %v", content, readErr)
	}
	recorder := httptest.NewRecorder()
	NewPreviewServer(bundle, 9000).statusHandler(previewFS)(recorder, httptest.NewRequest("GET", "/__preview__/status", nil))
	var status struct {
		HasIndex  bool `json:"has_index"`
		FileCount int  `json:"file_count"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil || !status.HasIndex || status.FileCount != 2 {
		t.Fatalf("status changed its root after replacement: %s (%v)", recorder.Body.String(), err)
	}
	if err := os.Rename(filepath.Join(moved, "data"), filepath.Join(moved, "old-data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bundle, filepath.Join(moved, "data")); err != nil {
		t.Fatal(err)
	}
	file, err = previewFS.Open("/data/index.html")
	if err == nil {
		file.Close()
		t.Fatal("parent replacement let preview escape its held root")
	}
}

func TestPreviewFileSystemAllowsContainedRelativeSymlinks(t *testing.T) {
	base := t.TempDir()
	bundle := filepath.Join(base, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "page.html"), []byte("contained page"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, target := range map[string]string{
		filepath.Join(bundle, "index.html"):            "page.html",
		filepath.Join(bundle, "assets", "linked.html"): "../page.html",
		filepath.Join(base, "selected"):                "bundle",
	} {
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	previewFS, err := validatePreviewBundle(filepath.Join(base, "selected"))
	if err != nil {
		t.Fatal(err)
	}
	defer previewFS.Close()
	for _, name := range []string{"/index.html", "/assets/linked.html"} {
		file, err := previewFS.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		content, readErr := io.ReadAll(file)
		file.Close()
		if readErr != nil || string(content) != "contained page" {
			t.Fatalf("contained relative link %s: %q (%v)", name, content, readErr)
		}
	}
}

func TestNewPreviewServer(t *testing.T) {
	server := NewPreviewServer("/tmp/test", 8080)

	if server == nil {
		t.Fatal("NewPreviewServer returned nil")
	}

	if server.bundlePath != "/tmp/test" {
		t.Errorf("Expected bundlePath '/tmp/test', got %s", server.bundlePath)
	}

	if server.port != 8080 {
		t.Errorf("Expected port 8080, got %d", server.port)
	}
}

func TestPreviewServer_Port(t *testing.T) {
	server := NewPreviewServer("/tmp/test", 9001)

	if server.Port() != 9001 {
		t.Errorf("Expected Port() to return 9001, got %d", server.Port())
	}
}

func TestPreviewServer_URL(t *testing.T) {
	port := 9002
	server := NewPreviewServer("/tmp", port)
	expected := fmt.Sprintf("http://127.0.0.1:%d", port)
	if got := server.URL(); got != expected {
		t.Errorf("Expected URL() to return %s, got %s", expected, got)
	}
}

func TestFindAvailablePort(t *testing.T) {
	// This should find an available port in the range
	port, err := FindAvailablePort(19000, 19100)
	if err != nil {
		t.Errorf("FindAvailablePort failed: %v", err)
	}

	if port < 19000 || port > 19100 {
		t.Errorf("Port %d is outside expected range 19000-19100", port)
	}
}

func TestFindAvailablePort_NoAvailable(t *testing.T) {
	// Try to find in a very narrow range that's likely already in use
	// This is a bit tricky to test reliably, so we just verify the function exists
	// and returns the expected type
	port, err := FindAvailablePort(19200, 19200)
	if err == nil {
		// Port was available, which is fine
		if port != 19200 {
			t.Errorf("Expected port 19200, got %d", port)
		}
	}
}

func TestDefaultPreviewConfig(t *testing.T) {
	config := DefaultPreviewConfig()

	if config.Port != 0 {
		t.Errorf("Expected Port 0 (auto-select), got %d", config.Port)
	}

	if !config.OpenBrowser {
		t.Error("Expected OpenBrowser to be true")
	}

	if config.Quiet {
		t.Error("Expected Quiet to be false")
	}
}

func TestPreviewConfig(t *testing.T) {
	config := PreviewConfig{
		BundlePath:  "/tmp/bundle",
		Port:        8888,
		OpenBrowser: false,
		Quiet:       true,
	}

	if config.BundlePath != "/tmp/bundle" {
		t.Errorf("Expected BundlePath '/tmp/bundle', got %s", config.BundlePath)
	}

	if config.Port != 8888 {
		t.Errorf("Expected Port 8888, got %d", config.Port)
	}

	if config.OpenBrowser {
		t.Error("Expected OpenBrowser to be false")
	}

	if !config.Quiet {
		t.Error("Expected Quiet to be true")
	}
}

func TestPreviewServer_Start_MissingBundle(t *testing.T) {
	server := NewPreviewServer("/nonexistent/path/12345", 19050)

	err := server.Start()
	if err == nil {
		t.Error("Expected error for missing bundle path")
	}
}

func TestPreviewServer_Start_MissingIndex(t *testing.T) {
	// Create a temp directory without index.html
	tmpDir := t.TempDir()

	server := NewPreviewServer(tmpDir, 19051)

	err := server.Start()
	if err == nil {
		t.Error("Expected error for missing index.html")
	}
}

func TestStartPreviewWithConfig_MissingIndexReturnsError(t *testing.T) {
	// Create a temp directory without index.html
	tmpDir := t.TempDir()

	cfg := PreviewConfig{
		BundlePath:  tmpDir,
		Port:        0,
		OpenBrowser: false,
		Quiet:       true,
	}

	err := StartPreviewWithConfig(cfg)
	if err == nil {
		t.Fatal("Expected error for missing index.html")
	}
	if !strings.Contains(err.Error(), "no index.html found") {
		t.Fatalf("Expected missing index error, got: %v", err)
	}
}

func TestPreviewFileServer_RejectsEscapedSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	bundleDir := filepath.Join(tmpDir, "bundle")
	if err := os.Mkdir(bundleDir, 0755); err != nil {
		t.Fatalf("Failed to create bundle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<html>ok</html>"), 0644); err != nil {
		t.Fatalf("Failed to create index.html: %v", err)
	}

	secretPath := filepath.Join(tmpDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("do not serve"), 0644); err != nil {
		t.Fatalf("Failed to create secret file: %v", err)
	}
	if err := os.Symlink(secretPath, filepath.Join(bundleDir, "leak.txt")); err != nil {
		t.Skipf("Symlinks are not available on this platform: %v", err)
	}

	previewFS, err := newSafePreviewFileSystem(bundleDir)
	if err != nil {
		t.Fatalf("Failed to create preview filesystem: %v", err)
	}
	defer previewFS.Close()
	server := httptest.NewServer(noCacheMiddleware(http.FileServer(previewFS)))
	defer server.Close()

	resp, err := http.Get(server.URL + "/leak.txt")
	if err != nil {
		t.Fatalf("Failed to request escaped symlink: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "do not serve") {
		t.Fatalf("Preview server served a symlink outside the bundle: status=%d body=%q", resp.StatusCode, string(body))
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Expected escaped symlink to be forbidden or hidden, got status %d body %q", resp.StatusCode, string(body))
	}
}

func TestSafePreviewDir_RejectsBackslashTraversalOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("backslash path traversal is Windows-specific")
	}

	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<html>ok</html>"), 0644); err != nil {
		t.Fatalf("Failed to create index.html: %v", err)
	}

	previewFS, err := newSafePreviewFileSystem(bundleDir)
	if err != nil {
		t.Fatalf("Failed to create preview filesystem: %v", err)
	}
	defer previewFS.Close()
	file, err := previewFS.Open(`/..\secret.txt`)
	if err == nil {
		file.Close()
		t.Fatal("Expected Windows backslash traversal path to be rejected")
	}
	if !errors.Is(err, iofs.ErrPermission) {
		t.Fatalf("Expected permission error for backslash traversal, got: %v", err)
	}
}

func TestValidatePreviewBundle_RejectsEscapedIndexSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	bundleDir := filepath.Join(tmpDir, "bundle")
	if err := os.Mkdir(bundleDir, 0755); err != nil {
		t.Fatalf("Failed to create bundle dir: %v", err)
	}

	outsideIndex := filepath.Join(tmpDir, "index.html")
	if err := os.WriteFile(outsideIndex, []byte("<html>outside</html>"), 0644); err != nil {
		t.Fatalf("Failed to create outside index: %v", err)
	}
	if err := os.Symlink(outsideIndex, filepath.Join(bundleDir, "index.html")); err != nil {
		t.Skipf("Symlinks are not available on this platform: %v", err)
	}

	_, err := validatePreviewBundle(bundleDir)
	if err == nil {
		t.Fatal("Expected escaped index.html symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "index.html escapes bundle") {
		t.Fatalf("Expected escaped index error, got: %v", err)
	}
}

func TestValidatePreviewBundle_RejectsIndexDirectory(t *testing.T) {
	bundleDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundleDir, "index.html"), 0755); err != nil {
		t.Fatalf("Failed to create index.html directory: %v", err)
	}

	_, err := validatePreviewBundle(bundleDir)
	if err == nil {
		t.Fatal("Expected index.html directory to be rejected")
	}
	if !strings.Contains(err.Error(), "index.html is a directory") {
		t.Fatalf("Expected index directory error, got: %v", err)
	}
}

func TestValidatePreviewBundle_RejectsFileBundlePath(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "bundle.html")
	if err := os.WriteFile(bundlePath, []byte("<html>not a directory</html>"), 0644); err != nil {
		t.Fatalf("Failed to create bundle file: %v", err)
	}

	_, err := validatePreviewBundle(bundlePath)
	if err == nil {
		t.Fatal("Expected file bundle path to be rejected")
	}
	if !strings.Contains(err.Error(), "bundle path is not a directory") {
		t.Fatalf("Expected non-directory bundle error, got: %v", err)
	}
}

func TestPreviewServer_StatusHandler_DoesNotCountEscapedIndexSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	bundleDir := filepath.Join(tmpDir, "bundle")
	if err := os.Mkdir(bundleDir, 0755); err != nil {
		t.Fatalf("Failed to create bundle dir: %v", err)
	}

	outsideIndex := filepath.Join(tmpDir, "index.html")
	if err := os.WriteFile(outsideIndex, []byte("<html>outside</html>"), 0644); err != nil {
		t.Fatalf("Failed to create outside index: %v", err)
	}
	if err := os.Symlink(outsideIndex, filepath.Join(bundleDir, "index.html")); err != nil {
		t.Skipf("Symlinks are not available on this platform: %v", err)
	}

	server := NewPreviewServer(bundleDir, 1234)
	previewFS, err := newSafePreviewFileSystem(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	defer previewFS.Close()
	req := httptest.NewRequest(http.MethodGet, "/__preview__/status", nil)
	rec := httptest.NewRecorder()
	server.statusHandler(previewFS)(rec, req)

	type statusResponse struct {
		HasIndex bool `json:"has_index"`
	}
	var got statusResponse
	if err := json.NewDecoder(io.LimitReader(rec.Body, 1024*1024)).Decode(&got); err != nil {
		t.Fatalf("Expected valid status JSON, got error: %v", err)
	}
	if got.HasIndex {
		t.Fatal("Expected escaped index.html symlink not to count as a valid index")
	}
}

func TestPreviewServer_Integration(t *testing.T) {
	// Create a temp bundle directory
	tmpDir := t.TempDir()

	// Create index.html
	indexContent := `<!DOCTYPE html><html><head><title>Test</title></head><body>Hello</body></html>`
	if err := os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644); err != nil {
		t.Fatalf("Failed to create index.html: %v", err)
	}

	// Create a data file
	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("Failed to create data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "meta.json"), []byte(`{"test": true}`), 0644); err != nil {
		t.Fatalf("Failed to create meta.json: %v", err)
	}

	// Find available port
	port, err := FindAvailablePort(19060, 19080)
	if err != nil {
		t.Fatalf("Failed to find available port: %v", err)
	}

	server := NewPreviewServer(tmpDir, port)

	// Start server in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Test index.html
	resp, err := http.Get(server.URL())
	if err != nil {
		t.Fatalf("Failed to GET index.html: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Check no-cache headers
	cacheControl := resp.Header.Get("Cache-Control")
	if cacheControl == "" {
		t.Error("Expected Cache-Control header")
	}

	pragma := resp.Header.Get("Pragma")
	if pragma != "no-cache" {
		t.Errorf("Expected Pragma: no-cache, got %s", pragma)
	}

	// Check body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}

	if string(body) != indexContent {
		t.Errorf("Expected body %q, got %q", indexContent, string(body))
	}

	// Test status endpoint
	statusResp, err := http.Get(server.URL() + "/__preview__/status")
	if err != nil {
		t.Fatalf("Failed to GET status: %v", err)
	}
	defer statusResp.Body.Close()

	if statusResp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", statusResp.StatusCode)
	}

	statusBody, err := io.ReadAll(io.LimitReader(statusResp.Body, 1024*1024))
	if err != nil {
		t.Fatalf("Failed to read status body: %v", err)
	}
	if len(statusBody) == 0 {
		t.Error("Expected non-empty status response")
	}

	// Clean shutdown
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}

func TestPreviewServer_StatusHandler_EmitsValidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Include a control character in the bundle path. fmt's %q uses Go escapes (e.g. \x01),
	// which are NOT valid JSON escapes, so this ensures we don't hand-roll JSON here.
	// Windows generally forbids control characters in file paths.
	if runtime.GOOS == "windows" {
		t.Skip("control-character paths are not supported on windows")
	}

	segment := "x" + string([]byte{0x01}) + "y"
	bundleDir := filepath.Join(tmpDir, segment)
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		t.Skipf("skipping control-char path test (mkdir failed): %v", err)
	}

	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html><title>ok</title>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "data.json"), []byte(`{"ok":true}`), 0644); err != nil {
		t.Fatalf("WriteFile data.json: %v", err)
	}

	server := NewPreviewServer(bundleDir, 1234)
	previewFS, err := newSafePreviewFileSystem(bundleDir)
	if err != nil {
		t.Fatal(err)
	}
	defer previewFS.Close()

	req := httptest.NewRequest(http.MethodGet, "/__preview__/status", nil)
	rec := httptest.NewRecorder()
	server.statusHandler(previewFS)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	type statusResponse struct {
		Status     string `json:"status"`
		Port       int    `json:"port"`
		BundlePath string `json:"bundle_path"`
		HasIndex   bool   `json:"has_index"`
		FileCount  int    `json:"file_count"`
	}

	var got statusResponse
	if err := json.NewDecoder(io.LimitReader(rec.Body, 1024*1024)).Decode(&got); err != nil {
		t.Fatalf("expected valid JSON, got error: %v (body=%q)", err, rec.Body.String())
	}

	if got.Status != "running" {
		t.Fatalf("status=%q, want %q", got.Status, "running")
	}
	if got.Port != 1234 {
		t.Fatalf("port=%d, want %d", got.Port, 1234)
	}
	if !got.HasIndex {
		t.Fatalf("has_index=%v, want true", got.HasIndex)
	}
	if got.FileCount < 2 {
		t.Fatalf("file_count=%d, want >= 2", got.FileCount)
	}

	if got.BundlePath != bundleDir {
		t.Fatalf("bundle_path=%q, want %q", got.BundlePath, bundleDir)
	}
}

func TestNoCacheMiddleware(t *testing.T) {
	// Create a simple handler
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test"))
	})

	// Wrap with no-cache middleware
	handler := noCacheMiddleware(inner)

	// Create a test request
	req, _ := http.NewRequest("GET", "/", nil)
	rec := &testResponseWriter{headers: make(http.Header)}

	handler.ServeHTTP(rec, req)

	// Check headers
	if rec.headers.Get("Cache-Control") == "" {
		t.Error("Expected Cache-Control header")
	}

	if rec.headers.Get("Pragma") != "no-cache" {
		t.Errorf("Expected Pragma: no-cache, got %s", rec.headers.Get("Pragma"))
	}

	if rec.headers.Get("Expires") != "0" {
		t.Errorf("Expected Expires: 0, got %s", rec.headers.Get("Expires"))
	}

	// Verify no CORS headers
	if rec.headers.Get("Access-Control-Allow-Origin") != "" {
		t.Error("Unexpected CORS header Access-Control-Allow-Origin found")
	}
}

func TestPreviewServer_StatusJSONEncoding(t *testing.T) {
	invalidPath := string([]byte{0xff, 0xfe, 'b'})
	server := NewPreviewServer(invalidPath, 19090)

	req := httptest.NewRequest(http.MethodGet, "/__preview__/status", nil)
	rec := httptest.NewRecorder()

	server.statusHandler(nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected valid JSON, got error: %v", err)
	}

	if payload["status"] != "running" {
		t.Errorf("expected status=running, got %v", payload["status"])
	}
}

// testResponseWriter is a simple ResponseWriter for testing
type testResponseWriter struct {
	headers    http.Header
	body       []byte
	statusCode int
}

func (w *testResponseWriter) Header() http.Header {
	return w.headers
}

func (w *testResponseWriter) Write(data []byte) (int, error) {
	w.body = append(w.body, data...)
	return len(data), nil
}

func (w *testResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}
