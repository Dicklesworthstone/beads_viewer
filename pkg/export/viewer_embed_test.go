package export

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCopyEmbeddedAssets_BindsCompleteOfflineBundle(t *testing.T) {
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "chunks"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"beads.sqlite3.config.json": `{"chunked":true,"chunk_count":1}`,
		"chunks/00000.bin":          "database chunk",
	} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CopyEmbeddedAssets(out, "Offline test"); err != nil {
		t.Fatal(err)
	}
	worker, err := os.ReadFile(filepath.Join(out, "coi-serviceworker.js"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`const OFFLINE_ASSETS = (\[.*\]);`).FindSubmatch(worker)
	if len(match) != 2 {
		t.Fatal("exported worker has no offline file manifest")
	}
	var assets []struct {
		Path string `json:"path"`
		Hash string `json:"sha256"`
	}
	if err := json.Unmarshal(match[1], &assets); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, asset := range assets {
		if seen[asset.Path] {
			t.Fatalf("duplicate cache key %s", asset.Path)
		}
		seen[asset.Path] = true
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(asset.Path)))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if asset.Hash != fmt.Sprintf("%x", hash) {
			t.Errorf("%s hash does not bind exported bytes", asset.Path)
		}
	}
	for _, required := range []string{"index.html", "viewer.js", "vendor/sql-wasm.wasm", "vendor/bv_graph_bg.wasm", "beads.sqlite3.config.json", "chunks/00000.bin"} {
		if !seen[required] {
			t.Errorf("required offline asset missing: %s", required)
		}
	}
	if seen["coi-serviceworker.js"] || seen["graph-demo.html"] {
		t.Fatal("self-referential/development asset in offline manifest")
	}
	if strings.Contains(string(worker), "CACHE_REVISION = 'development'") {
		t.Fatal("offline cache has no bundle revision")
	}
}

// TestEmbeddedIndex_CSPHasNoInlineScripts guards the dashboard's script
// policy: script-src carries no 'unsafe-inline', no <script> block is inline,
// no element has an on*= handler, and every same-origin script or stylesheet
// the page references is present in the embedded assets (so moving code out
// of index.html cannot leave a dangling src).
func TestEmbeddedIndex_CSPHasNoInlineScripts(t *testing.T) {
	content, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	html := string(content)

	m := regexp.MustCompile(`(?is)http-equiv="Content-Security-Policy"\s+content="([^"]*)"`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("index.html has no Content-Security-Policy meta tag")
	}
	var scriptSrc string
	for _, directive := range strings.Split(m[1], ";") {
		fields := strings.Fields(directive)
		if len(fields) > 0 && fields[0] == "script-src" {
			scriptSrc = strings.Join(fields[1:], " ")
		}
	}
	if scriptSrc == "" {
		t.Fatalf("CSP has no script-src directive: %q", m[1])
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("script-src must not allow 'unsafe-inline': %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'wasm-unsafe-eval'") {
		t.Errorf("script-src must allow 'wasm-unsafe-eval' (sql.js, bv_graph_bg.wasm): %q", scriptSrc)
	}

	scriptTags := regexp.MustCompile(`(?is)<script\b[^>]*>`).FindAllString(html, -1)
	if len(scriptTags) == 0 {
		t.Fatal("index.html has no script tags at all")
	}
	srcAttr := regexp.MustCompile(`(?i)\bsrc\s*=\s*["']([^"']+)["']`)
	for _, tag := range scriptTags {
		sm := srcAttr.FindStringSubmatch(tag)
		if sm == nil {
			t.Errorf("inline script block is forbidden by the CSP: %s", tag)
			continue
		}
		assertEmbeddedAssetExists(t, sm[1])
	}
	if bad := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).FindString(html); bad != "" {
		t.Errorf("inline event handler attribute is forbidden by the CSP: %s", bad)
	}

	hrefAttr := regexp.MustCompile(`(?i)\bhref\s*=\s*["']([^"']+)["']`)
	for _, tag := range regexp.MustCompile(`(?is)<link\b[^>]*rel=["']stylesheet["'][^>]*>`).FindAllString(html, -1) {
		if hm := hrefAttr.FindStringSubmatch(tag); hm != nil {
			assertEmbeddedAssetExists(t, hm[1])
		}
	}

	// The bootstrap that used to be inline must be the file the page loads.
	if !strings.Contains(html, `<script src="head_init.js"></script>`) {
		t.Error("index.html must load head_init.js (theme default, tailwind.config, COI bootstrap)")
	}
}

func assertEmbeddedAssetExists(t *testing.T, ref string) {
	t.Helper()
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		t.Errorf("index.html references a remote asset %q; the bundle must be self-contained", ref)
		return
	}
	name := ref
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if _, err := ViewerAssetsFS.ReadFile("viewer_assets/" + name); err != nil {
		t.Errorf("index.html references %q but it is not an embedded asset: %v", ref, err)
	}
}

func TestReplaceTitle_Basic(t *testing.T) {
	html := `<html><head><title>Beads Viewer</title></head><body><h1 class="text-xl font-semibold">Beads Viewer</h1></body></html>`

	result := replaceTitle(html, "My Project")
	if !strings.Contains(result, "<title>My Project</title>") {
		t.Errorf("Expected title tag replacement, got: %s", result)
	}
	if !strings.Contains(result, `<h1 class="text-xl font-semibold">My Project</h1>`) {
		t.Errorf("Expected h1 replacement, got: %s", result)
	}
}

func TestCopyEmbeddedAssets_ExcludesDevOnlyAssets(t *testing.T) {
	outDir := t.TempDir()
	if err := CopyEmbeddedAssets(outDir, ""); err != nil {
		t.Fatalf("CopyEmbeddedAssets failed: %v", err)
	}

	// graph-demo.html loads remote CDN scripts (unpkg/d3js.org) that would run
	// on the exported dashboard's origin; it must never ship in a bundle.
	for _, excluded := range []string{"graph-demo.html", "hybrid_scorer.test.js"} {
		if _, err := os.Stat(filepath.Join(outDir, excluded)); !os.IsNotExist(err) {
			t.Errorf("dev-only asset %s must not be exported", excluded)
		}
	}

	// Production assets must still be present.
	for _, required := range []string{"index.html", "viewer.js", "graph.js", "hybrid_scorer.js"} {
		if _, err := os.Stat(filepath.Join(outDir, required)); err != nil {
			t.Errorf("expected production asset %s in bundle: %v", required, err)
		}
	}
}

func TestReplaceTitle_Empty(t *testing.T) {
	html := `<title>Beads Viewer</title>`
	result := replaceTitle(html, "")
	if result != html {
		t.Errorf("Empty title should return content unchanged, got: %s", result)
	}
}

func TestReplaceTitle_XSSPrevention(t *testing.T) {
	html := `<title>Beads Viewer</title>`
	result := replaceTitle(html, `<script>alert("xss")</script>`)
	if strings.Contains(result, "<script>") {
		t.Errorf("XSS not prevented: %s", result)
	}
	if !strings.Contains(result, "&lt;script&gt;") {
		t.Errorf("Expected HTML-escaped title, got: %s", result)
	}
}

func TestReplaceTitle_SpecialChars(t *testing.T) {
	html := `<title>Beads Viewer</title>`
	result := replaceTitle(html, `Tom & Jerry's "Project"`)
	if !strings.Contains(result, "Tom &amp; Jerry") {
		t.Errorf("Ampersand not escaped, got: %s", result)
	}
	if !strings.Contains(result, "&#34;Project&#34;") {
		t.Errorf("Quotes not escaped, got: %s", result)
	}
}

func TestReplaceTitle_NoMatch(t *testing.T) {
	html := `<title>Something Else</title>`
	result := replaceTitle(html, "My Project")
	// Should not modify content when the original title doesn't match
	if result != html {
		t.Errorf("Should not modify non-matching content, got: %s", result)
	}
}

func TestAddScriptCacheBusting_AllFiles(t *testing.T) {
	html := `<script src="viewer.js"></script>
<script src="charts.js"></script>
<script src="graph.js"></script>
<script src="hybrid_scorer.js"></script>
<script src="wasm_loader.js"></script>`

	result := AddScriptCacheBusting(html)

	// All five JS files should have cache-busting
	for _, jsFile := range []string{"viewer.js", "charts.js", "graph.js", "hybrid_scorer.js", "wasm_loader.js"} {
		if strings.Contains(result, `src="`+jsFile+`"`) {
			t.Errorf("File %s was not cache-busted", jsFile)
		}
		if !strings.Contains(result, jsFile+"?v=") {
			t.Errorf("File %s missing cache-buster parameter", jsFile)
		}
	}
}

func TestAddScriptCacheBusting_SingleQuotes(t *testing.T) {
	html := `<script src='viewer.js'></script>`
	result := AddScriptCacheBusting(html)

	if strings.Contains(result, `src='viewer.js'`) {
		t.Error("Single-quoted src should be cache-busted")
	}
	if !strings.Contains(result, "viewer.js?v=") {
		t.Error("Missing cache-buster for single-quoted src")
	}
}

func TestAddScriptCacheBusting_NoMatch(t *testing.T) {
	html := `<script src="vendor.js"></script>`
	result := AddScriptCacheBusting(html)

	// Vendor files should not be modified
	if result != html {
		t.Errorf("Vendor files should not be cache-busted, got: %s", result)
	}
}

func TestAddScriptCacheBusting_MultipleSameFile(t *testing.T) {
	html := `<script src="viewer.js"></script><script src="viewer.js"></script>`
	result := AddScriptCacheBusting(html)

	// Both instances should be cache-busted
	count := strings.Count(result, "viewer.js?v=")
	if count != 2 {
		t.Errorf("Expected 2 cache-busted instances, got %d", count)
	}
}

func TestHasEmbeddedAssets(t *testing.T) {
	// The binary has embedded assets
	result := HasEmbeddedAssets()
	if !result {
		t.Error("Expected HasEmbeddedAssets() to return true (assets are embedded)")
	}
}

func TestEmbeddedViewerInitializesAlpineAppOnce(t *testing.T) {
	content, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}

	html := string(content)
	if count := strings.Count(html, `x-data="beadsApp()"`); count != 1 {
		t.Fatalf("expected one beadsApp component, got %d", count)
	}
	if strings.Contains(html, `x-init="init()"`) {
		t.Fatal("beadsApp init must not be called through x-init; Alpine invokes it automatically")
	}
}

func TestEmbeddedViewerSupportsDirectGraphPath(t *testing.T) {
	content, err := ViewerAssetsFS.ReadFile("viewer_assets/viewer.js")
	if err != nil {
		t.Fatalf("read embedded viewer.js: %v", err)
	}

	viewerJS := string(content)
	for _, marker := range []string{
		"function routeHashFromPathname(pathname)",
		"const DIRECT_VIEW_ROUTES = new Set(['issues', 'insights', 'graph'])",
		"parseRoute(hash || routeHashFromPathname(window.location.pathname))",
		"// Handle the initial hash route or a host-rewritten clean path after",
		"routeHashFromPathname,",
	} {
		if !strings.Contains(viewerJS, marker) {
			t.Errorf("viewer.js missing direct-route marker %q", marker)
		}
	}
	if strings.Contains(viewerJS, "if (window.location.hash) {\n          this.handleHashChange();") {
		t.Error("clean-path routing must run even when the URL has no hash")
	}
}

func TestCopyEmbeddedAssets(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	err := CopyEmbeddedAssets(outputDir, "Test Project")
	if err != nil {
		t.Fatalf("CopyEmbeddedAssets failed: %v", err)
	}

	// Verify index.html exists
	indexPath := filepath.Join(outputDir, "index.html")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("Failed to read index.html: %v", err)
	}

	contentStr := string(content)

	// Verify title was replaced
	if strings.Contains(contentStr, "<title>Beads Viewer</title>") {
		t.Error("Title should have been replaced")
	}
	if !strings.Contains(contentStr, "<title>Test Project</title>") {
		t.Error("Expected custom title in index.html")
	}

	// Verify cache-busting was applied
	if strings.Contains(contentStr, `src="viewer.js"`) {
		t.Error("viewer.js should have cache-busting parameter")
	}
}

func TestCopyEmbeddedAssets_NoTitle(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	err := CopyEmbeddedAssets(outputDir, "")
	if err != nil {
		t.Fatalf("CopyEmbeddedAssets failed: %v", err)
	}

	// Verify index.html still has default title
	indexPath := filepath.Join(outputDir, "index.html")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("Failed to read index.html: %v", err)
	}

	if !strings.Contains(string(content), "<title>Beads Viewer</title>") {
		t.Error("Default title should be preserved when no custom title provided")
	}
}

func TestAddGitHubWorkflowToBundle(t *testing.T) {
	tmpDir := t.TempDir()

	err := AddGitHubWorkflowToBundle(tmpDir)
	if err != nil {
		t.Fatalf("AddGitHubWorkflowToBundle failed: %v", err)
	}

	// Verify workflow was created
	workflowPath := filepath.Join(tmpDir, ".github", "workflows", "static.yml")
	if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
		t.Error("Workflow file was not created")
	}
}

// TestEmbeddedViewerRendersComments verifies the static-site viewer surfaces
// issue comments (bv-52 comments table) in the issue detail modal (#187).
func TestEmbeddedViewerRendersComments(t *testing.T) {
	js, err := ViewerAssetsFS.ReadFile("viewer_assets/viewer.js")
	if err != nil {
		t.Fatalf("read embedded viewer.js: %v", err)
	}
	viewerJS := string(js)
	if !strings.Contains(viewerJS, "function getIssueComments(") {
		t.Fatal("viewer.js must define getIssueComments for the detail modal (#187)")
	}
	if !strings.Contains(viewerJS, "FROM comments WHERE issue_id") {
		t.Fatal("viewer.js must query the exported comments table (#187)")
	}
	if !strings.Contains(viewerJS, "issue.comments = getIssueComments(id)") {
		t.Fatal("getIssue must attach comments to the selected issue (#187)")
	}

	htmlBytes, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, "selectedIssue.comments") {
		t.Fatal("index.html must render selectedIssue.comments in the detail modal (#187)")
	}
	if !strings.Contains(html, "Comments (<span") {
		t.Fatal("index.html must show a comment count header (#187)")
	}
}
