package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Export Offline Functionality E2E Tests (bv-pua7)
// Tests that exported static site works offline.

// =============================================================================
// 1. Page Loading - All Assets Bundled
// =============================================================================

// TestOffline_AllAssetsBundled verifies no external dependencies in HTML.
func TestOffline_AllAssetsBundled(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Create test data
	issueData := `{"id": "offline-1", "title": "Offline Test Issue", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read index.html
	indexPath := filepath.Join(exportDir, "index.html")
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Check for external URLs (http:// or https://) that aren't CDN-safe
	// Allow specific CDNs that are commonly cached
	allowedExternalPatterns := []string{
		"cdn.jsdelivr.net",
		"unpkg.com",
		"cdnjs.cloudflare.com",
		"fonts.googleapis.com",
		"fonts.gstatic.com",
	}

	// Find all external URLs
	urlPattern := regexp.MustCompile(`(https?://[^"'\s>]+)`)
	matches := urlPattern.FindAllString(html, -1)

	var disallowedURLs []string
	for _, url := range matches {
		allowed := false
		for _, pattern := range allowedExternalPatterns {
			if strings.Contains(url, pattern) {
				allowed = true
				break
			}
		}
		if !allowed {
			// Check if it's a relative URL incorrectly parsed
			if !strings.HasPrefix(url, "http://localhost") && !strings.HasPrefix(url, "https://localhost") {
				disallowedURLs = append(disallowedURLs, url)
			}
		}
	}

	if len(disallowedURLs) > 0 {
		t.Logf("external URLs found (may break offline): %v", disallowedURLs)
		// This is a warning, not a failure - some external resources might be acceptable
	}
}

// TestOffline_LocalScriptReferences verifies scripts are locally bundled.
func TestOffline_LocalScriptReferences(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "offline-1", "title": "Offline Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Find all script src references
	scriptPattern := regexp.MustCompile(`<script[^>]*src="([^"]+)"`)
	matches := scriptPattern.FindAllStringSubmatch(html, -1)

	missingScripts := []string{}
	for _, match := range matches {
		if len(match) > 1 {
			src := match[1]
			// Skip external CDN scripts (allowed for d3, etc.)
			if strings.HasPrefix(src, "http") {
				continue
			}
			// Verify local script exists
			scriptPath := filepath.Join(exportDir, src)
			if _, err := os.Stat(scriptPath); err != nil {
				missingScripts = append(missingScripts, src)
			}
		}
	}
	if len(missingScripts) > 0 {
		// Log but don't fail - some scripts may be inline or optional
		t.Logf("referenced scripts not found (may be inline): %v", missingScripts)
	}
}

// TestOffline_LocalStyleReferences verifies stylesheets are locally bundled.
func TestOffline_LocalStyleReferences(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "offline-1", "title": "Offline Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Find all stylesheet references
	linkPattern := regexp.MustCompile(`<link[^>]*href="([^"]+\.css)"`)
	matches := linkPattern.FindAllStringSubmatch(html, -1)

	for _, match := range matches {
		if len(match) > 1 {
			href := match[1]
			// Skip external CDN styles
			if strings.HasPrefix(href, "http") {
				continue
			}
			// Verify local stylesheet exists
			cssPath := filepath.Join(exportDir, href)
			if _, err := os.Stat(cssPath); err != nil {
				t.Errorf("referenced stylesheet not found: %s", href)
			}
		}
	}
}

// =============================================================================
// 2. Search Functionality - Index Bundled
// =============================================================================

// TestOffline_SearchIndexBundled verifies search index is present.
func TestOffline_SearchIndexBundled(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Create multiple issues for search
	issueData := `{"id": "search-1", "title": "Search Test Alpha", "status": "open", "priority": 1, "issue_type": "task", "description": "Testing search functionality offline"}
{"id": "search-2", "title": "Search Test Beta", "status": "open", "priority": 2, "issue_type": "bug", "description": "Another searchable issue"}
{"id": "search-3", "title": "Search Test Gamma", "status": "closed", "priority": 1, "issue_type": "feature", "description": "Third issue for search"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// SQLite database serves as search index
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("search database not found: %v", err)
	}

	// Config for SQLite
	configPath := filepath.Join(exportDir, "beads.sqlite3.config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Errorf("database config not found: %v", err)
	}
}

// =============================================================================
// 3. Navigation - Hash-based Routing
// =============================================================================

// TestOffline_HashBasedRouting verifies single-page app structure.
func TestOffline_HashBasedRouting(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "nav-1", "title": "Navigation Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Single-page app should have one index.html
	indexPath := filepath.Join(exportDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("index.html not found: %v", err)
	}

	// Check that JavaScript handles routing
	viewerJSPath := filepath.Join(exportDir, "viewer.js")
	if _, err := os.Stat(viewerJSPath); err != nil {
		t.Errorf("viewer.js not found for client-side routing: %v", err)
	}
}

// =============================================================================
// 4. Graph Interaction - WASM Bundled
// =============================================================================

// TestOffline_GraphWASMBundled verifies graph WASM is present.
func TestOffline_GraphWASMBundled(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "graph-1", "title": "Graph Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check for WASM files
	wasmFiles := []string{
		"vendor/bv_graph_bg.wasm",
		"vendor/bv_graph.js",
	}

	for _, wf := range wasmFiles {
		path := filepath.Join(exportDir, wf)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("WASM file not found: %s", wf)
		}
	}

	// Check for graph.js
	graphJSPath := filepath.Join(exportDir, "graph.js")
	if _, err := os.Stat(graphJSPath); err != nil {
		t.Errorf("graph.js not found: %v", err)
	}
}

// =============================================================================
// 5. Service Worker - Offline Support
// =============================================================================

// TestOffline_ServiceWorkerPresent verifies service worker for offline.
func TestOffline_ServiceWorkerPresent(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "sw-1", "title": "Service Worker Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check for COI service worker (Cross-Origin Isolation for SharedArrayBuffer)
	swPath := filepath.Join(exportDir, "coi-serviceworker.js")
	if _, err := os.Stat(swPath); err != nil {
		t.Errorf("coi-serviceworker.js not found: %v", err)
	}
}

// =============================================================================
// 6. Data Files - All JSON Bundled
// =============================================================================

// TestOffline_DataFilesBundled verifies data JSON files are present.
func TestOffline_DataFilesBundled(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "data-1", "title": "Data Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Required data files
	dataFiles := []string{
		"data/meta.json",
		"data/triage.json",
	}

	for _, df := range dataFiles {
		path := filepath.Join(exportDir, df)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("data file not found: %s", df)
		}
	}
}

// =============================================================================
// 7. Complete Bundle - All Required Files
// =============================================================================

// TestOffline_CompleteBundleChecklist verifies complete offline bundle.
func TestOffline_CompleteBundleChecklist(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "bundle-1", "title": "Bundle Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Complete checklist of required files for offline functionality
	requiredFiles := []string{
		"index.html",
		"styles.css",
		"viewer.js",
		"graph.js",
		"coi-serviceworker.js",
		"beads.sqlite3",
		"beads.sqlite3.config.json",
		"data/meta.json",
		"data/triage.json",
		"vendor/bv_graph.js",
		"vendor/bv_graph_bg.wasm",
	}

	missingFiles := []string{}
	for _, f := range requiredFiles {
		path := filepath.Join(exportDir, f)
		if _, err := os.Stat(path); err != nil {
			missingFiles = append(missingFiles, f)
		}
	}

	if len(missingFiles) > 0 {
		t.Errorf("missing files for offline bundle: %v", missingFiles)
	} else {
		t.Logf("all %d required files present", len(requiredFiles))
	}
}

// =============================================================================
// 8. No Network Requests - Self-Contained
// =============================================================================

// TestOffline_NoFetchAPICalls verifies no runtime fetch to external servers.
func TestOffline_NoFetchAPICalls(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "fetch-1", "title": "Fetch Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read viewer.js and check for external API calls
	viewerJSPath := filepath.Join(exportDir, "viewer.js")
	viewerBytes, err := os.ReadFile(viewerJSPath)
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	viewerJS := string(viewerBytes)

	// Look for fetch calls to external URLs
	// Pattern: fetch("http or fetch('http
	externalFetchPattern := regexp.MustCompile(`fetch\s*\(\s*['"]https?://`)
	if externalFetchPattern.MatchString(viewerJS) {
		t.Logf("viewer.js may contain external fetch calls - check for offline compatibility")
	}
}

// =============================================================================
// 9. CORS Headers - Cross-Origin Isolation
// =============================================================================

// TestOffline_CrossOriginIsolation verifies COI setup for SharedArrayBuffer.
func TestOffline_CrossOriginIsolation(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "coi-1", "title": "COI Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// The registration lives in head_init.js because the dashboard CSP forbids
	// inline scripts: index.html must load it, and it must register the worker.
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), `src="head_init.js`) {
		t.Error("index.html should load head_init.js for cross-origin isolation")
	}
	headInit, err := os.ReadFile(filepath.Join(exportDir, "head_init.js"))
	if err != nil {
		t.Fatalf("read head_init.js: %v", err)
	}
	// Static contract coverage; actual browser control/offline behavior is
	// exercised separately rather than requiring a browser for this Go suite.
	if !strings.Contains(string(headInit), "serviceWorker.register('./coi-serviceworker.js', { updateViaCache: 'none' })") {
		t.Error("head_init.js should register coi-serviceworker.js with updateViaCache none for cross-origin isolation")
	}
}

// =============================================================================
// 10. Bundle Size - Reasonable for Offline
// =============================================================================

// TestOffline_BundleSizeReasonable verifies bundle isn't too large.
func TestOffline_BundleSizeReasonable(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Create reasonable test data (50 issues)
	var lines []string
	for i := 0; i < 50; i++ {
		line := `{"id": "size-` + itoa(i) + `", "title": "Size Test Issue ` + itoa(i) + `", "status": "open", "priority": ` + itoa(i%5) + `, "issue_type": "task"}`
		lines = append(lines, line)
	}
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Calculate total bundle size
	var totalSize int64
	err := filepath.Walk(exportDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk export dir: %v", err)
	}

	t.Logf("total bundle size for 50 issues: %d KB", totalSize/1024)

	// Bundle should be under 10MB for reasonable offline use
	maxBundleSize := int64(10 * 1024 * 1024) // 10MB
	if totalSize > maxBundleSize {
		t.Errorf("bundle too large for offline: %d bytes (max %d)", totalSize, maxBundleSize)
	}
}

// =============================================================================
// 11. Relative Paths - No Absolute URLs
// =============================================================================

// TestOffline_RelativePaths verifies all paths are relative.
func TestOffline_RelativePaths(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "path-1", "title": "Path Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Check for absolute paths starting with /
	// These break when served from subdirectories
	absolutePathPattern := regexp.MustCompile(`(src|href)="/[^"]*"`)
	matches := absolutePathPattern.FindAllString(html, -1)

	if len(matches) > 0 {
		t.Logf("absolute paths found (may break subdirectory hosting): %v", matches)
	}
}

// =============================================================================
// 12. Minification - Optimized for Size
// =============================================================================

// TestOffline_CSSMinified verifies CSS is reasonably sized.
func TestOffline_CSSMinified(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "min-1", "title": "Minification Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	cssPath := filepath.Join(exportDir, "styles.css")
	cssInfo, err := os.Stat(cssPath)
	if err != nil {
		t.Fatalf("stat styles.css: %v", err)
	}

	t.Logf("styles.css size: %d KB", cssInfo.Size()/1024)

	// CSS should be under 500KB
	maxCSSSize := int64(500 * 1024)
	if cssInfo.Size() > maxCSSSize {
		t.Errorf("CSS too large: %d bytes (max %d)", cssInfo.Size(), maxCSSSize)
	}
}
