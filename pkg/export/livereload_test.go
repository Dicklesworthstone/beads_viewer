package export

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewLiveReloadHub(t *testing.T) {
	dir := t.TempDir()

	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}
	defer hub.Stop()

	if hub.bundlePath != dir {
		t.Errorf("bundlePath = %q, want %q", hub.bundlePath, dir)
	}

	if hub.debounce != 200*time.Millisecond {
		t.Errorf("debounce = %v, want %v", hub.debounce, 200*time.Millisecond)
	}
}

func TestLiveReloadHub_StartStop(t *testing.T) {
	dir := t.TempDir()

	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}

	if err := hub.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Should be able to stop without error
	hub.Stop()
}

func TestLiveReloadHub_StopAfterStartFailure(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "missing")

	hub, err := NewLiveReloadHub(missingDir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}

	if err := hub.Start(); err == nil {
		hub.Stop()
		t.Fatal("Expected Start() to fail for missing bundle path")
	}

	// The preview setup path calls Stop after a failed Start to close the
	// watcher allocated by NewLiveReloadHub. This must remain safe.
	hub.Stop()
}

func TestLiveReloadHub_ClientCount(t *testing.T) {
	dir := t.TempDir()

	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}

	if err := hub.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer hub.Stop()

	if count := hub.ClientCount(); count != 0 {
		t.Errorf("ClientCount() = %d, want 0", count)
	}
}

func TestLiveReloadHub_SSEHandler(t *testing.T) {
	dir := t.TempDir()

	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}

	if err := hub.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer hub.Stop()

	handler := hub.SSEHandler()

	// Create a test request
	req := httptest.NewRequest("GET", "/__preview__/events", nil)
	rr := httptest.NewRecorder()

	// Run handler in goroutine (it blocks)
	done := make(chan bool)
	go func() {
		handler(rr, req)
		done <- true
	}()

	// Give it time to send the initial connected event
	time.Sleep(100 * time.Millisecond)

	// Cancel the request context (simulates client disconnect)
	// In real test, we'd need to create a cancelable context
	hub.Stop() // This should cause the handler to exit

	select {
	case <-done:
		// Handler exited
	case <-time.After(time.Second):
		t.Error("Handler did not exit after stop")
	}

	// Check response headers
	contentType := rr.Header().Get("Content-Type")
	if contentType != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", contentType, "text/event-stream")
	}

	// Check that connected event was sent
	body := rr.Body.String()
	if !strings.Contains(body, "event: connected") {
		t.Errorf("Response should contain 'event: connected', got: %s", body)
	}
}

func TestLiveReloadMiddleware_NonHTML(t *testing.T) {
	// Test that non-HTML files pass through unchanged
	handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("console.log('test');"))
	}))

	req := httptest.NewRequest("GET", "/app.js", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, LiveReloadScriptTag) {
		t.Errorf("Non-HTML response should not contain injected script")
	}
}

func TestLiveReloadMiddleware_HTML(t *testing.T) {
	// Test that HTML files get the script injected
	handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body><h1>Test</h1></body></html>"))
	}))

	req := httptest.NewRequest("GET", "/index.html", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	// Need to flush the injecting response writer
	if f, ok := rr.Result().Body.(interface{ Close() error }); ok {
		f.Close()
	}

	body := rr.Body.String()
	if !strings.Contains(body, LiveReloadScriptTag) {
		t.Errorf("HTML response should contain injected script, got: %s", body)
	}
	if !strings.Contains(body, "</body>") {
		t.Errorf("HTML response should still contain </body>, got: %s", body)
	}
}

func TestLiveReloadMiddleware_RemovesStaleContentLength(t *testing.T) {
	html := "<html><body><h1>Test</h1></body></html>"
	handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(html)))
		w.Write([]byte(html))
	}))

	req := httptest.NewRequest("GET", "/index.html", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty after script injection", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, LiveReloadScriptTag) {
		t.Fatalf("Expected injected live reload script, got: %s", body)
	}
	if len(body) <= len(html) {
		t.Fatalf("Expected injected response to grow beyond original length")
	}
}

func TestLiveReloadMiddleware_HTMLWithoutBody(t *testing.T) {
	handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Test</title></head></html>"))
	}))

	req := httptest.NewRequest("GET", "/index.html", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, LiveReloadScriptTag) {
		t.Fatalf("HTML without </body> should still receive injected script, got: %s", body)
	}
	if strings.Index(body, LiveReloadScriptTag) > strings.Index(body, "</html>") {
		t.Fatalf("Expected script before </html>, got: %s", body)
	}
}

// TestLiveReloadScriptHandler_ServesScriptAsFile: the dashboard's CSP has no
// 'unsafe-inline' for scripts, so live reload must be a same-origin file the
// injected tag points at, never an inline block.
func TestLiveReloadScriptHandler_ServesScriptAsFile(t *testing.T) {
	if strings.Contains(LiveReloadScript, "<script") {
		t.Fatalf("LiveReloadScript must be plain JavaScript, got: %s", LiveReloadScript)
	}
	if LiveReloadScriptTag != `<script src="`+LiveReloadScriptPath+`"></script>` {
		t.Fatalf("injected tag must reference %s, got %s", LiveReloadScriptPath, LiveReloadScriptTag)
	}

	req := httptest.NewRequest("GET", LiveReloadScriptPath, nil)
	rr := httptest.NewRecorder()
	liveReloadScriptHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("Content-Type = %q, want application/javascript", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "new EventSource('/__preview__/events')") {
		t.Fatalf("served script must subscribe to the SSE endpoint, got: %s", body)
	}

	post := httptest.NewRequest("POST", LiveReloadScriptPath, nil)
	rr = httptest.NewRecorder()
	liveReloadScriptHandler(rr, post)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rr.Code)
	}
}

func TestRC39_LiveReloadTrailingEdgeAtomicRenameAndNewSubdir(t *testing.T) {
	dir := t.TempDir()
	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub: %v", err)
	}
	hub.debounce = 60 * time.Millisecond
	if err := hub.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer hub.Stop()

	clientCh := make(chan struct{}, 4)
	hub.addClient(clientCh)
	defer hub.removeClient(clientCh)

	// 1. Writing .bv-export-* temp file alone must NOT trigger a premature reload.
	tmpFile := filepath.Join(dir, ".bv-export-staging123")
	if err := os.WriteFile(tmpFile, []byte("partial"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	select {
	case <-clientCh:
		t.Fatal("staging .bv-export-* write triggered premature live-reload event")
	case <-time.After(140 * time.Millisecond):
	}

	// 2. Atomic rename into place MUST trigger trailing-edge reload after completion.
	finalFile := filepath.Join(dir, "index.html")
	if err := os.Rename(tmpFile, finalFile); err != nil {
		t.Fatalf("rename temp file: %v", err)
	}
	select {
	case <-clientCh:
	case <-time.After(time.Second):
		t.Fatal("atomic rename did not trigger trailing-edge live-reload notification")
	}

	// Drain any residual burst notification before testing dynamic subdir watch.
	time.Sleep(120 * time.Millisecond)
	for len(clientCh) > 0 {
		<-clientCh
	}

	// 3. Dynamically created subdirectory must be watched automatically.
	subDir := filepath.Join(dir, "data-dynamic")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	// Wait for subdir watch registration and clear the directory-create notification.
	select {
	case <-clientCh:
	case <-time.After(time.Second):
		t.Fatal("subdir creation did not notify")
	}
	time.Sleep(120 * time.Millisecond)
	for len(clientCh) > 0 {
		<-clientCh
	}

	// Write a file inside the newly created subdirectory and verify notification arrives.
	if err := os.WriteFile(filepath.Join(subDir, "meta.json"), []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	select {
	case <-clientCh:
	case <-time.After(time.Second):
		t.Fatal("file write inside newly created subdirectory did not trigger live-reload")
	}
}

func TestRC39_ViewerAssetsExclusionReportOptionsStandaloneCSPAndPreviewFD(t *testing.T) {
	// 1. Dev-only files must NOT be embedded in ViewerAssetsFS; runtime files MUST be.
	for _, excluded := range []string{"viewer_assets/graph-demo.html", "viewer_assets/hybrid_scorer.test.js"} {
		if _, err := ViewerAssetsFS.Open(excluded); err == nil {
			t.Errorf("ViewerAssetsFS unexpectedly embeds dev-only file %q", excluded)
		}
	}
	for _, required := range []string{"viewer_assets/index.html", "viewer_assets/viewer.js", "viewer_assets/vendor/bv_graph_bg.wasm"} {
		f, err := ViewerAssetsFS.Open(required)
		if err != nil {
			t.Errorf("ViewerAssetsFS missing required runtime asset %q: %v", required, err)
			continue
		}
		_ = f.Close()
	}

	// 2. Direct callers can disable action plan cleanly via DefaultReportOptions() or OmitActionPlan.
	optsDefault := DefaultReportOptions()
	optsDefault.IncludeActionPlan = false
	if optsDefault.includeActions() {
		t.Fatal("DefaultReportOptions() with IncludeActionPlan=false still enabled actions")
	}
	optsOmit := ReportOptions{Format: "markdown", OmitActionPlan: true}
	if optsOmit.includeActions() {
		t.Fatal("ReportOptions{OmitActionPlan: true} still enabled actions")
	}

	// 3. Standalone interactive HTML graph includes offline CSP meta tag.
	htmlOut := generateUltimateHTML("Test Graph", "abc123", `{"nodes":[],"links":[]}`, 0, 0, "proj", "", "")
	if !strings.Contains(htmlOut, `http-equiv="Content-Security-Policy"`) || !strings.Contains(htmlOut, `default-src 'none'`) || !strings.Contains(htmlOut, `connect-src 'none'`) {
		t.Errorf("standalone HTML graph missing offline CSP meta tag")
	}

	// 4. validatePreviewBundle returned handle closes cleanly.
	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	previewDir, err := validatePreviewBundle(bundleDir)
	if err != nil {
		t.Fatalf("validatePreviewBundle: %v", err)
	}
	if err := previewDir.Close(); err != nil {
		t.Fatalf("previewDir.Close(): %v", err)
	}
}

func TestLiveReloadHub_FileChangeNotification(t *testing.T) {
	dir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(dir, "test.html")
	if err := os.WriteFile(testFile, []byte("<html></html>"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	hub, err := NewLiveReloadHub(dir)
	if err != nil {
		t.Fatalf("NewLiveReloadHub() error = %v", err)
	}

	if err := hub.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer hub.Stop()

	// Register a client channel manually
	clientCh := make(chan struct{}, 1)
	hub.addClient(clientCh)

	// Modify the file
	time.Sleep(300 * time.Millisecond) // Wait for debounce period to pass
	if err := os.WriteFile(testFile, []byte("<html><body>updated</body></html>"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Wait for notification
	select {
	case <-clientCh:
		// Got notification - success
	case <-time.After(2 * time.Second):
		t.Error("Did not receive notification within timeout")
	}
}

func TestFindLastIndex(t *testing.T) {
	tests := []struct {
		haystack string
		needle   string
		want     int
	}{
		{"hello world", "world", 6},
		{"hello world", "hello", 0},
		{"hello hello", "hello", 6},
		{"abc", "xyz", -1},
		{"", "test", -1},
		{"test", "", -1}, // Empty needle returns -1 due to loop condition
	}

	for _, tt := range tests {
		got := findLastIndex([]byte(tt.haystack), []byte(tt.needle))
		if got != tt.want {
			t.Errorf("findLastIndex(%q, %q) = %d, want %d", tt.haystack, tt.needle, got, tt.want)
		}
	}
}

func TestLiveReloadMiddleware_Skips404JsonAndServiceWorkerRequests(t *testing.T) {
	t.Run("extensionless 404 plain text is never injected", func(t *testing.T) {
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		req := httptest.NewRequest(http.MethodGet, "/missing-route", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rr.Code)
		}
		if strings.Contains(rr.Body.String(), LiveReloadScriptTag) {
			t.Fatalf("404 response must not contain LiveReloadScriptTag: %q", rr.Body.String())
		}
	})

	t.Run("extensionless 200 JSON preserves Content-Length and skips script injection", func(t *testing.T) {
		const payload = `{"status":"ok","count":42}`
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write([]byte(payload))
		}))
		req := httptest.NewRequest(http.MethodGet, "/api-status", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if got := rr.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
			t.Fatalf("Content-Length = %q, want %d", got, len(payload))
		}
		if rr.Body.String() != payload {
			t.Fatalf("JSON body = %q, want %q", rr.Body.String(), payload)
		}
	})

	t.Run("sniffed non-HTML extensionless 200 skips script injection", func(t *testing.T) {
		const payload = `{"sniffed":true}`
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(payload))
		}))
		req := httptest.NewRequest(http.MethodGet, "/sniffed-json", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Body.String() != payload {
			t.Fatalf("sniffed non-HTML body = %q, want %q", rr.Body.String(), payload)
		}
	})

	t.Run("service worker and fetch subresource requests receive unmodified HTML bytes", func(t *testing.T) {
		const exactHTML = "<!doctype html><html><body><h1>Offline Hash Target</h1></body></html>"
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(exactHTML))
		}))

		headers := []struct {
			key, val string
		}{
			{"X-BV-SW-PreCache", "1"},
			{"Service-Worker", "script"},
			{"Sec-Fetch-Dest", "empty"},
			{"Sec-Fetch-Dest", "serviceworker"},
		}
		for _, h := range headers {
			req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
			req.Header.Set(h.key, h.val)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Body.String() != exactHTML {
				t.Fatalf("header %s=%s mutated HTML bytes:\ngot:  %q\nwant: %q", h.key, h.val, rr.Body.String(), exactHTML)
			}
		}
	})

	t.Run("HEAD requests skip HTML buffering and preserve upstream Content-Length", func(t *testing.T) {
		const exactHTML = "<!doctype html><html><body><h1>HEAD Probe</h1></body></html>"
		wantLen := strconv.Itoa(len(exactHTML))
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", wantLen)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodHead, "/index.html", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if got := rr.Header().Get("Content-Length"); got != wantLen {
			t.Fatalf("HEAD Content-Length = %q, want %q", got, wantLen)
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("HEAD body len = %d (%q), want 0", rr.Body.Len(), rr.Body.String())
		}
	})

	t.Run("Content-Encoding responses pass through untouched without script injection", func(t *testing.T) {
		compressedPayload := []byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad, 0xbe, 0xef}
		wantLen := strconv.Itoa(len(compressedPayload))
		handler := liveReloadMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", wantLen)
			_, _ = w.Write(compressedPayload)
		}))

		req := httptest.NewRequest(http.MethodGet, "/index.html", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if got := rr.Header().Get("Content-Length"); got != wantLen {
			t.Fatalf("gzip Content-Length = %q, want %q", got, wantLen)
		}
		if string(rr.Body.Bytes()) != string(compressedPayload) {
			t.Fatalf("gzip payload mutated: got %x, want %x", rr.Body.Bytes(), compressedPayload)
		}
		if strings.Contains(rr.Body.String(), LiveReloadScriptTag) {
			t.Fatalf("compressed response must not receive script injection")
		}
	})
}
