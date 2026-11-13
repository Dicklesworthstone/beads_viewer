package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func openTestBundle(t *testing.T, dir string) *Bundle {
	t.Helper()
	bundle, err := OpenBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bundle.Close() })
	return bundle
}

func TestBundleAtomicWritesPreserveOutsideFiles(t *testing.T) {
	const original = "FAKE-PRIVATE-OUTSIDE-CONTENT"
	for _, kind := range []string{"leaf symlink", "parent symlink", "hardlink", "existing private file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "bundle")
			outside := filepath.Join(dir, "outside")
			if err := os.MkdirAll(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			bundle := openTestBundle(t, out)
			target := filepath.Join(outside, "report.md")
			if err := os.WriteFile(target, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			name := "report.md"
			var linkErr error
			switch kind {
			case "leaf symlink":
				linkErr = os.Symlink(target, filepath.Join(out, name))
			case "parent symlink":
				linkErr = os.Symlink(outside, filepath.Join(out, "nested"))
				name = filepath.Join("nested", name)
			case "hardlink":
				linkErr = os.Link(target, filepath.Join(out, name))
			case "existing private file":
				if err := os.WriteFile(filepath.Join(out, name), []byte("old report"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if linkErr != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("link unavailable: %v", linkErr)
				}
				t.Fatal(linkErr)
			}
			err := bundle.WriteFile(name, []byte("new report"), 0o644)
			if kind == "parent symlink" {
				if err == nil {
					t.Fatal("outside parent symlink was accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(filepath.Join(out, name))
				if err != nil || string(content) != "new report" {
					t.Fatalf("bundle replacement = %q, %v", content, err)
				}
				info, err := os.Lstat(filepath.Join(out, name))
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("replacement must be a regular file: %v", err)
				}
				if runtime.GOOS != "windows" && (kind == "hardlink" || kind == "existing private file") && info.Mode().Perm() != 0o600 {
					t.Fatalf("replacement widened private-file permissions to %o", info.Mode().Perm())
				}
			}
			if content, err := os.ReadFile(target); err != nil || string(content) != original {
				t.Fatalf("outside file changed: %q, %v", content, err)
			}
		})
	}
}

func TestBundleFailedWritePreservesPreviousFile(t *testing.T) {
	out := t.TempDir()
	bundle := openTestBundle(t, out)
	if err := bundle.WriteFile("report.md", []byte("previous report"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("fake incomplete source")
	err := bundle.writeFile("report.md", 0o644, func(w io.Writer) error {
		if _, err := io.WriteString(w, "incomplete report"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("lost source error: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(out, "report.md")); err != nil || string(content) != "previous report" {
		t.Fatalf("failed write changed previous report: %q, %v", content, err)
	}
	entries, err := os.ReadDir(out)
	if err != nil || len(entries) != 1 || entries[0].Name() != "report.md" {
		t.Fatalf("failed write left temporary artifacts: %v, %v", entries, err)
	}
}

func TestBundleHeldRootAndParentReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture renames open directories and creates symlinks")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle")
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := openTestBundle(t, out)
	moved := filepath.Join(dir, "original-bundle")
	if err := os.Rename(out, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, out); err != nil {
		t.Fatal(err)
	}
	if err := bundle.CopyFile("anchored.txt", bytes.NewBufferString("held root"), 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(moved, "anchored.txt")); err != nil || string(content) != "held root" {
		t.Fatalf("held root did not retain selected directory: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "anchored.txt")); !os.IsNotExist(err) {
		t.Fatalf("replacement root received a write: %v", err)
	}
	if err := bundle.MkdirAll("nested", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "item.txt"), []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := bundle.writeFile(filepath.Join("nested", "item.txt"), 0o600, func(w io.Writer) error {
		// Swap the parent after directory preparation but before publication.
		// A path precheck followed by ordinary Rename would escape here.
		if err := os.Rename(filepath.Join(moved, "nested"), filepath.Join(moved, "original-nested")); err != nil {
			return err
		}
		if err := os.Symlink(outside, filepath.Join(moved, "nested")); err != nil {
			return err
		}
		_, err := io.WriteString(w, "must not escape")
		return err
	})
	if err == nil {
		t.Fatal("parent replacement escaped the held root")
	}
	if content, err := os.ReadFile(filepath.Join(outside, "item.txt")); err != nil || string(content) != "outside sentinel" {
		t.Fatalf("outside target changed during parent replacement: %q, %v", content, err)
	}
}

func TestCopyEmbeddedAssetsConfinesPaths(t *testing.T) {
	for _, kind := range []string{"leaf symlink", "hardlink", "vendor parent", "offline data symlink"} {
		t.Run(kind, func(t *testing.T) {
			out := t.TempDir()
			outside := t.TempDir()
			target := filepath.Join(outside, "index.html")
			const sentinel = "FAKE-OUTSIDE-EXPORT-CONTENT"
			if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			var linkErr error
			switch kind {
			case "leaf symlink":
				linkErr = os.Symlink(target, filepath.Join(out, "index.html"))
			case "hardlink":
				linkErr = os.Link(target, filepath.Join(out, "index.html"))
			case "vendor parent":
				linkErr = os.Symlink(outside, filepath.Join(out, "vendor"))
			case "offline data symlink":
				if err := os.Mkdir(filepath.Join(out, "data"), 0o700); err != nil {
					t.Fatal(err)
				}
				linkErr = os.Symlink(target, filepath.Join(out, "data", "private.json"))
			}
			if linkErr != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("link unavailable: %v", linkErr)
				}
				t.Fatal(linkErr)
			}
			err := CopyEmbeddedAssets(out, "Safe bundle")
			if kind == "vendor parent" || kind == "offline data symlink" {
				if err == nil {
					t.Fatal("unsafe asset path accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(target); err != nil || string(content) != sentinel {
				t.Fatalf("outside asset target changed: %q, %v", content, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("export added files outside its root: %v, %v", entries, err)
			}
		})
	}
}

func TestDeploymentMetadataReplacesHardlinks(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		write      func(string) error
	}{
		{"workflow", filepath.Join(".github", "workflows", "static.yml"), WriteGitHubActionsWorkflow},
		{"headers", "_headers", GenerateHeadersFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := t.TempDir()
			target := filepath.Join(t.TempDir(), "private-fixture")
			if err := os.WriteFile(target, []byte("outside sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(out, tc.path)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, filepath.Join(out, tc.path)); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("hardlink unavailable: %v", err)
				}
				t.Fatal(err)
			}
			if err := tc.write(out); err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(target); err != nil || string(content) != "outside sentinel" {
				t.Fatalf("deployment metadata changed outside file: %q, %v", content, err)
			}
		})
	}
}

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

// TestEmbeddedViewerScripts_NoInlineEventHandlers: markup the viewer's own
// scripts build at runtime is subject to the same CSP, so an onclick="..."
// attribute in it never runs (the label legend's close button was dead).
func TestEmbeddedViewerScripts_NoInlineEventHandlers(t *testing.T) {
	entries, err := ViewerAssetsFS.ReadDir("viewer_assets")
	if err != nil {
		t.Fatalf("read embedded viewer_assets: %v", err)
	}
	handler := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=\s*["']`)
	scanned := 0
	for _, entry := range entries {
		// graph-demo.html and *.test.js are excluded from exports.
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") || strings.HasSuffix(entry.Name(), ".test.js") {
			continue
		}
		content, err := ViewerAssetsFS.ReadFile("viewer_assets/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		scanned++
		if bad := handler.FindString(string(content)); bad != "" {
			t.Errorf("%s builds an inline event handler, which the CSP blocks: %s", entry.Name(), bad)
		}
	}
	if scanned == 0 {
		t.Fatal("no viewer scripts scanned")
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

func TestViewerJS_NoExternalCDNFallbackUnderSelfCSP(t *testing.T) {
	js, err := ViewerAssetsFS.ReadFile("viewer_assets/viewer.js")
	if err != nil {
		t.Fatalf("read embedded viewer.js: %v", err)
	}
	viewerJS := string(js)
	if strings.Contains(viewerJS, "cdn.jsdelivr.net") {
		t.Fatal("viewer.js must not inject cdn.jsdelivr.net scripts under 'self' CSP")
	}
	if !strings.Contains(viewerJS, "./vendor/sql-wasm.js") {
		t.Fatal("viewer.js must load ./vendor/sql-wasm.js locally")
	}

	htmlBytes, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if !strings.Contains(string(htmlBytes), "script-src 'self'") {
		t.Fatal("index.html must retain strict script-src 'self' CSP")
	}
}

func TestViewerJS_MarkdownStripsInlineHandlersBeforeDOMPurify(t *testing.T) {
	js, err := ViewerAssetsFS.ReadFile("viewer_assets/viewer.js")
	if err != nil {
		t.Fatalf("read embedded viewer.js: %v", err)
	}
	viewerJS := string(js)
	if !strings.Contains(viewerJS, "new marked.Marked(") || !strings.Contains(viewerJS, "stripInlineEventAttributes") {
		t.Fatal("viewer.js must strip inline event-handler attributes via a custom marked.Marked renderer before DOMPurify sanitization")
	}
	if strings.Contains(viewerJS, "marked.parse(text)") || strings.Contains(viewerJS, "marked.parseInline(text)") {
		t.Fatal("viewer.js must not call default marked.parse/parseInline with raw inline event-handler attributes")
	}
}

func TestReplaceTitle_ActualEmbeddedIndexHTML(t *testing.T) {
	raw, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}

	result := replaceTitle(string(raw), "Sprint 42 <Live>")
	if !strings.Contains(result, "<title>Sprint 42 &lt;Live&gt;</title>") {
		t.Fatal("expected <title> in embedded index.html to be updated and HTML-escaped")
	}
	if !strings.Contains(result, `<h1 class="text-lg sm:text-xl font-semibold">Sprint 42 &lt;Live&gt;</h1>`) {
		t.Fatal("expected responsive navbar <h1> in embedded index.html to be updated and HTML-escaped")
	}
	if strings.Contains(result, ">Beads Viewer</h1>") {
		t.Fatal("expected default Beads Viewer navbar heading to be replaced")
	}
}

func TestEmbedAssetsWithSubtitle(t *testing.T) {
	outWithSub := t.TempDir()
	if err := EmbedAssetsWithSubtitle(outWithSub, "Sprint 42", "  Team <Alpha> & Ops  "); err != nil {
		t.Fatalf("EmbedAssetsWithSubtitle failed: %v", err)
	}

	indexBytes, err := os.ReadFile(filepath.Join(outWithSub, "index.html"))
	if err != nil {
		t.Fatalf("read exported index.html: %v", err)
	}
	indexHTML := string(indexBytes)

	wantBlock := `<h1 class="text-lg sm:text-xl font-semibold">Sprint 42</h1>` + "\n" +
		`              <p class="text-xs text-gray-500 dark:text-gray-400 truncate">Team &lt;Alpha&gt; &amp; Ops</p>`
	if !strings.Contains(indexHTML, wantBlock) {
		t.Fatalf("expected escaped subtitle paragraph directly beneath navbar <h1>, got snippet missing %q", wantBlock)
	}

	outWithoutSub := t.TempDir()
	if err := EmbedAssets(outWithoutSub, "Sprint 42"); err != nil {
		t.Fatalf("EmbedAssets failed: %v", err)
	}
	noSubBytes, err := os.ReadFile(filepath.Join(outWithoutSub, "index.html"))
	if err != nil {
		t.Fatalf("read exported index.html without subtitle: %v", err)
	}
	if strings.Contains(string(noSubBytes), `<p class="text-xs text-gray-500 dark:text-gray-400 truncate">`) {
		t.Fatal("expected no subtitle paragraph when subtitle is empty")
	}
	if got := ReplaceSubtitle(`<h1 class="text-xl">Title</h1>`, "   "); got != `<h1 class="text-xl">Title</h1>` {
		t.Fatalf("ReplaceSubtitle with whitespace modified content: %q", got)
	}
}
