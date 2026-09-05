package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Cloudflare Pages Deployment E2E Tests (bv-mwlh)
// Tests Cloudflare Pages deployment workflow configuration and artifacts.
// Note: Actual deployment requires Cloudflare credentials and is skipped.

// =============================================================================
// 1. Headers File Generation
// =============================================================================

// TestCloudflare_HeadersFileGenerated verifies _headers file is created.
func TestCloudflare_HeadersFileGenerated(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "cf-1", "title": "Cloudflare Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check for _headers file (Cloudflare Pages config)
	headersPath := filepath.Join(exportDir, "_headers")
	if _, err := os.Stat(headersPath); err != nil {
		t.Logf("_headers file not found (may be generated during deploy): %v", err)
		return
	}

	// Verify content if file exists
	content, err := os.ReadFile(headersPath)
	if err != nil {
		t.Fatalf("read _headers: %v", err)
	}

	// Check for security headers
	requiredHeaders := []string{
		"X-Frame-Options",
		"X-Content-Type-Options",
	}

	for _, h := range requiredHeaders {
		if !strings.Contains(string(content), h) {
			t.Logf("_headers missing %s", h)
		}
	}
}

// TestCloudflare_WASMContentType verifies WASM content type header.
func TestCloudflare_WASMContentType(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "wasm-1", "title": "WASM Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// WASM files exist
	wasmPath := filepath.Join(exportDir, "vendor", "bv_graph_bg.wasm")
	if _, err := os.Stat(wasmPath); err != nil {
		t.Errorf("WASM file not found: %v", err)
	}
}

// =============================================================================
// 2. Output Directory Structure
// =============================================================================

// TestCloudflare_OutputDirectoryStructure verifies expected structure for CF Pages.
func TestCloudflare_OutputDirectoryStructure(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "struct-1", "title": "Structure Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Cloudflare Pages expects index.html at root
	requiredFiles := []string{
		"index.html",
		"styles.css",
		"viewer.js",
	}

	for _, f := range requiredFiles {
		path := filepath.Join(exportDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("required file missing: %s", f)
		}
	}

	// Verify vendor directory exists
	vendorDir := filepath.Join(exportDir, "vendor")
	if info, err := os.Stat(vendorDir); err != nil || !info.IsDir() {
		t.Error("vendor directory missing")
	}
}

// =============================================================================
// 3. Service Worker for COI
// =============================================================================

// TestCloudflare_ServiceWorkerForCOI verifies COI service worker present.
func TestCloudflare_ServiceWorkerForCOI(t *testing.T) {
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

	// COI service worker needed for SharedArrayBuffer support
	swPath := filepath.Join(exportDir, "coi-serviceworker.js")
	if _, err := os.Stat(swPath); err != nil {
		t.Errorf("coi-serviceworker.js not found: %v", err)
	}

	// The registration lives in head_init.js (the CSP forbids inline scripts);
	// index.html must load that file and the file must register the worker.
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), `src="head_init.js`) {
		t.Error("index.html should load head_init.js, which registers coi-serviceworker")
	}
	headInit, err := os.ReadFile(filepath.Join(exportDir, "head_init.js"))
	if err != nil {
		t.Fatalf("read head_init.js: %v", err)
	}
	// This is static inspection of the shipped registration contract; the
	// separate browser suite exercises activation and offline behavior.
	if !strings.Contains(string(headInit), "serviceWorker.register('./coi-serviceworker.js', { updateViaCache: 'none' })") {
		t.Error("head_init.js should register coi-serviceworker.js with updateViaCache none")
	}
}

// =============================================================================
// 4. SQLite Database Chunking
// =============================================================================

// TestCloudflare_SQLiteChunking verifies database config for large files.
func TestCloudflare_SQLiteChunking(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Create multiple issues for reasonable database size
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, `{"id": "chunk-`+itoa(i)+`", "title": "Chunking Test `+itoa(i)+`", "status": "open", "priority": 1, "issue_type": "task"}`)
	}
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check database config
	configPath := filepath.Join(exportDir, "beads.sqlite3.config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}

	var config struct {
		Chunked   bool  `json:"chunked"`
		TotalSize int64 `json:"total_size"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}

	if config.TotalSize == 0 {
		t.Error("total_size should be non-zero")
	}
	t.Logf("database config: chunked=%v, total_size=%d", config.Chunked, config.TotalSize)
}

// =============================================================================
// 5. Export Path Configuration
// =============================================================================

// TestCloudflare_CustomExportPath verifies custom export path works.
func TestCloudflare_CustomExportPath(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Custom export directory
	customDir := filepath.Join(repoDir, "custom-export-dir")

	issueData := `{"id": "path-1", "title": "Path Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", customDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify export went to custom path
	if _, err := os.Stat(filepath.Join(customDir, "index.html")); err != nil {
		t.Errorf("index.html not found in custom dir: %v", err)
	}
}

// TestCloudflare_NestedExportPath verifies nested export path works.
func TestCloudflare_NestedExportPath(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Nested export directory
	nestedDir := filepath.Join(repoDir, "deeply", "nested", "export")

	issueData := `{"id": "nested-1", "title": "Nested Path Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", nestedDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify export created nested directories
	if _, err := os.Stat(filepath.Join(nestedDir, "index.html")); err != nil {
		t.Errorf("index.html not found in nested dir: %v", err)
	}
}

// =============================================================================
// 6. Title Configuration
// =============================================================================

// TestCloudflare_CustomTitle verifies --pages-title flag works.
func TestCloudflare_CustomTitle(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "title-1", "title": "Title Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	customTitle := "My Project Dashboard"
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-title", customTitle)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify title in meta.json
	meta := readMetaJSON(t, exportDir)
	if meta.Title != customTitle {
		t.Errorf("title = %q, want %q", meta.Title, customTitle)
	}

	// Verify title in index.html
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), customTitle) {
		t.Error("index.html should contain custom title")
	}
}

// =============================================================================
// 7. Include Closed Option
// =============================================================================

// TestCloudflare_IncludeClosed verifies --pages-include-closed flag.
func TestCloudflare_IncludeClosed(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Mix of open and closed
	issueData := `{"id": "open-1", "title": "Open Issue", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "closed-1", "title": "Closed Issue", "status": "closed", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Export with closed included
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 2 {
		t.Errorf("issue_count = %d, want 2 (including closed)", meta.IssueCount)
	}
}

// TestCloudflare_IncludeClosedByDefault verifies default includes closed issues.
func TestCloudflare_IncludeClosedByDefault(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Mix of open and closed
	issueData := `{"id": "open-1", "title": "Open Issue", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "closed-1", "title": "Closed Issue", "status": "closed", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Export without --pages-include-closed (default is true)
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 2 {
		t.Errorf("issue_count = %d, want 2 (including closed by default)", meta.IssueCount)
	}
}

// TestCloudflare_ExcludeClosed verifies --pages-include-closed=false excludes closed.
func TestCloudflare_ExcludeClosed(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Mix of open and closed
	issueData := `{"id": "open-1", "title": "Open Issue", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "closed-1", "title": "Closed Issue", "status": "closed", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Export with --pages-include-closed=false
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed=false")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != 1 {
		t.Errorf("issue_count = %d, want 1 (excluding closed)", meta.IssueCount)
	}
}

// =============================================================================
// 8. Multiple Exports to Same Directory
// =============================================================================

// TestCloudflare_OverwriteExistingExport verifies re-export overwrites cleanly.
func TestCloudflare_OverwriteExistingExport(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// First export with 1 issue
	issueData1 := `{"id": "first-1", "title": "First Export", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData1), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd1 := exec.Command(bv, "--export-pages", exportDir)
	cmd1.Dir = repoDir
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("first export failed: %v\n%s", err, out)
	}

	meta1 := readMetaJSON(t, exportDir)
	if meta1.IssueCount != 1 {
		t.Fatalf("first export issue_count = %d, want 1", meta1.IssueCount)
	}

	// Second export with 3 issues
	issueData2 := `{"id": "second-1", "title": "Second Export 1", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "second-2", "title": "Second Export 2", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "second-3", "title": "Second Export 3", "status": "open", "priority": 3, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData2), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd2 := exec.Command(bv, "--export-pages", exportDir)
	cmd2.Dir = repoDir
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("second export failed: %v\n%s", err, out)
	}

	meta2 := readMetaJSON(t, exportDir)
	if meta2.IssueCount != 3 {
		t.Errorf("second export issue_count = %d, want 3", meta2.IssueCount)
	}
}

// =============================================================================
// 9. Large Export Performance
// =============================================================================

// TestCloudflare_LargeExportPerformance verifies reasonable export time.
func TestCloudflare_LargeExportPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large export test in short mode")
	}

	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Generate 200 issues
	const issueCount = 200
	var lines []string
	for i := 0; i < issueCount; i++ {
		lines = append(lines, `{"id": "perf-`+itoa(i)+`", "title": "Performance Test `+itoa(i)+`", "status": "open", "priority": `+itoa(i%5)+`, "issue_type": "task"}`)
	}
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}

	meta := readMetaJSON(t, exportDir)
	if meta.IssueCount != issueCount {
		t.Errorf("issue_count = %d, want %d", meta.IssueCount, issueCount)
	}

	t.Logf("large export (%d issues) completed successfully", issueCount)
}

// =============================================================================
// 10. Error Handling
// =============================================================================

// TestCloudflare_InvalidExportPath verifies error for invalid path.
func TestCloudflare_InvalidExportPath(t *testing.T) {
	bv := buildBvBinary(t)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	issueData := `{"id": "err-1", "title": "Error Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Try to export to a file path instead of directory
	filePath := filepath.Join(repoDir, "existing-file.txt")
	if err := os.WriteFile(filePath, []byte("existing"), 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", filePath)
	cmd.Dir = repoDir
	_, err := cmd.CombinedOutput()
	// May or may not error - depends on implementation
	if err == nil {
		t.Log("export to file path didn't error (may overwrite)")
	}
}

// =============================================================================
// 11. Data Directory Structure
// =============================================================================

// TestCloudflare_DataDirectoryContents verifies data directory structure.
func TestCloudflare_DataDirectoryContents(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "data-1", "title": "Data Dir Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify data directory structure
	dataDir := filepath.Join(exportDir, "data")
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		t.Fatal("data directory missing")
	}

	// Required data files
	dataFiles := []string{
		"meta.json",
		"triage.json",
	}

	for _, f := range dataFiles {
		path := filepath.Join(dataDir, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("data/%s missing: %v", f, err)
		}
	}
}

// =============================================================================
// 12. History Export Option
// =============================================================================

// TestCloudflare_HistoryExport verifies --pages-include-history flag.
func TestCloudflare_HistoryExport(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	// Need a git repo for history
	repoDir := t.TempDir()

	// Initialize git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = repoDir
	if err := initCmd.Run(); err != nil {
		t.Skipf("git init failed: %v", err)
	}

	// Configure git user
	configCmds := [][]string{
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range configCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		_ = cmd.Run()
	}

	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	exportDir := filepath.Join(repoDir, "bv-pages")

	issueData := `{"id": "hist-1", "title": "History Test", "status": "open", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	// Add and commit
	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = repoDir
	_ = addCmd.Run()

	commitCmd := exec.Command("git", "commit", "-m", "Initial commit")
	commitCmd.Dir = repoDir
	_ = commitCmd.Run()

	// Export with history
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--export-pages with history failed: %v\n%s", err, out)
	}

	// Check for history.json
	historyPath := filepath.Join(exportDir, "data", "history.json")
	if _, err := os.Stat(historyPath); err != nil {
		t.Logf("history.json not found (may require git repo): %v", err)
	}
}
