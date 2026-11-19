package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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

	// Production assets and GitHub Pages .nojekyll marker must be present.
	for _, required := range []string{"index.html", "viewer.js", "graph.js", "hybrid_scorer.js", ".nojekyll", filepath.Join(".github", "workflows", "static.yml")} {
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

func TestCopyEmbeddedAssets_IncludesWasmInOfflineAssets(t *testing.T) {
	dir := t.TempDir()
	wasmDir := filepath.Join(dir, "wasm")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(wasmDir, 0o755); err != nil {
		t.Fatalf("MkdirAll wasm: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll data: %v", err)
	}

	jsBytes := []byte("// mock bv_graph.js glue\nexport default function init() {}\n")
	wasmBytes := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	metaBytes := []byte(`{"issue_count":1}`)

	if err := os.WriteFile(filepath.Join(wasmDir, "bv_graph.js"), jsBytes, 0o644); err != nil {
		t.Fatalf("WriteFile bv_graph.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wasmDir, "bv_graph_bg.wasm"), wasmBytes, 0o644); err != nil {
		t.Fatalf("WriteFile bv_graph_bg.wasm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("WriteFile meta.json: %v", err)
	}

	if err := CopyEmbeddedAssets(dir, "Offline WASM Test"); err != nil {
		t.Fatalf("CopyEmbeddedAssets: %v", err)
	}

	swRaw, err := os.ReadFile(filepath.Join(dir, "coi-serviceworker.js"))
	if err != nil {
		t.Fatalf("ReadFile coi-serviceworker.js: %v", err)
	}
	re := regexp.MustCompile(`const OFFLINE_ASSETS = (\[[^\n;]+\]);`)
	m := re.FindSubmatch(swRaw)
	if len(m) != 2 {
		t.Fatalf("coi-serviceworker.js missing bound OFFLINE_ASSETS array")
	}

	var assets []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(m[1], &assets); err != nil {
		t.Fatalf("Unmarshal OFFLINE_ASSETS: %v", err)
	}
	byPath := make(map[string]string, len(assets))
	for _, a := range assets {
		byPath[a.Path] = a.SHA256
	}

	wantJS := fmt.Sprintf("%x", sha256.Sum256(jsBytes))
	wantWasm := fmt.Sprintf("%x", sha256.Sum256(wasmBytes))
	if got := byPath["wasm/bv_graph.js"]; got != wantJS {
		t.Fatalf("OFFLINE_ASSETS[wasm/bv_graph.js] = %q, want %q", got, wantJS)
	}
	if got := byPath["wasm/bv_graph_bg.wasm"]; got != wantWasm {
		t.Fatalf("OFFLINE_ASSETS[wasm/bv_graph_bg.wasm] = %q, want %q", got, wantWasm)
	}
}

func TestAddScriptCacheBusting_HonorsSourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	htmlIn := `<script src="viewer.js"></script><script src='wasm_loader.js'></script><script src="vendor/alpine.min.js"></script>`

	first := AddScriptCacheBusting(htmlIn)
	second := AddScriptCacheBusting(htmlIn)
	if first != second {
		t.Fatalf("AddScriptCacheBusting with SOURCE_DATE_EPOCH not deterministic:\nfirst:  %s\nsecond: %s", first, second)
	}
	if !strings.Contains(first, "-1700000000\"") && !strings.Contains(first, "?v=1700000000\"") {
		t.Fatalf("expected SOURCE_DATE_EPOCH 1700000000 in viewer.js cache buster, got: %s", first)
	}
	if !strings.Contains(first, "-1700000000'") && !strings.Contains(first, "?v=1700000000'") {
		t.Fatalf("expected SOURCE_DATE_EPOCH 1700000000 in single-quoted wasm_loader.js cache buster, got: %s", first)
	}
	if !strings.Contains(first, `src="vendor/alpine.min.js"`) {
		t.Fatalf("vendor scripts must remain untouched, got: %s", first)
	}
}

func TestBundle_HybridWasmPreparedAndSkipsDotfilesInOfflineAssets(t *testing.T) {
	var nilBundle *Bundle
	if nilBundle.HybridWasmPrepared() {
		t.Fatal("nil Bundle must report HybridWasmPrepared() == false")
	}
	nilBundle.MarkHybridWasmPrepared() // no-op, must not panic

	tmpDir := t.TempDir()
	bundle, err := OpenBundle(tmpDir)
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	defer bundle.Close()

	if bundle.HybridWasmPrepared() {
		t.Fatal("fresh Bundle must default to HybridWasmPrepared() == false")
	}
	bundle.MarkHybridWasmPrepared()
	if !bundle.HybridWasmPrepared() {
		t.Fatal("Bundle must report HybridWasmPrepared() == true after MarkHybridWasmPrepared()")
	}

	if err := bundle.WriteFile("data/meta.json", []byte(`{"issue_count":1}`), 0o644); err != nil {
		t.Fatalf("write data/meta.json: %v", err)
	}
	if err := bundle.WriteFile("data/.gitignore", []byte("*\n"), 0o644); err != nil {
		t.Fatalf("write data/.gitignore: %v", err)
	}
	if err := bundle.WriteFile("wasm/.hidden/secret.txt", []byte("skip"), 0o644); err != nil {
		t.Fatalf("write wasm/.hidden/secret.txt: %v", err)
	}
	if err := bundle.CopyEmbeddedAssets("Dotfile Test"); err != nil {
		t.Fatalf("CopyEmbeddedAssets: %v", err)
	}

	swRaw, err := os.ReadFile(filepath.Join(tmpDir, "coi-serviceworker.js"))
	if err != nil {
		t.Fatalf("read coi-serviceworker.js: %v", err)
	}
	swStr := string(swRaw)
	if !strings.Contains(swStr, `"data/meta.json"`) {
		t.Fatalf("expected data/meta.json in OFFLINE_ASSETS, got:\n%s", swStr)
	}
	if strings.Contains(swStr, ".gitignore") || strings.Contains(swStr, ".hidden") {
		t.Fatalf("dotfiles/dotdirs must be excluded from OFFLINE_ASSETS, got:\n%s", swStr)
	}
}

func TestAddScriptCacheBusting_StylesCSSAndHybridBuildScriptDefaultOutDir(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	htmlIn := `<link rel="stylesheet" href="styles.css"><link rel='stylesheet' href='styles.css'><link rel="stylesheet" href="vendor/keep.css"><script src="viewer.js"></script>`
	out := AddScriptCacheBusting(htmlIn)

	// Positive observables: both double- and single-quoted styles.css hrefs receive ?v= cache-buster.
	if strings.Contains(out, `href="styles.css"`) || strings.Contains(out, `href='styles.css'`) {
		t.Fatalf("styles.css href was not cache-busted: %s", out)
	}
	if !strings.Contains(out, `href="styles.css?v=`) || !strings.Contains(out, `href='styles.css?v=`) {
		t.Fatalf("expected styles.css?v= in both double- and single-quoted links, got: %s", out)
	}
	// Planted negative control: vendor stylesheets remain untouched.
	if !strings.Contains(out, `href="vendor/keep.css"`) {
		t.Fatalf("vendor stylesheet should remain untouched, got: %s", out)
	}

	// Verify scripts/build_hybrid_wasm.sh defaults outside pkg/ (never creating pkg/export/viewer_assets/wasm).
	scriptPath := filepath.Clean(filepath.Join("..", "..", "scripts", "build_hybrid_wasm.sh"))
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("stat %s: %v", scriptPath, err)
	}
	scriptRaw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", scriptPath, err)
	}
	if strings.Contains(string(scriptRaw), "pkg/export/viewer_assets/wasm") {
		t.Fatalf("build_hybrid_wasm.sh must not default to pkg/export/viewer_assets/wasm")
	}

	if runtime.GOOS != "windows" {
		binDir := t.TempDir()
		outLog := filepath.Join(t.TempDir(), "wasm-pack-out.txt")
		stub := "#!/usr/bin/env bash\nfor ((i=1; i<=$#; i++)); do\n  if [[ \"${!i}\" == \"--out-dir\" ]]; then\n    next=$((i+1))\n    printf '%s' \"${!next}\" > " + fmt.Sprintf("%q", outLog) + "\n  fi\ndone\nexit 0\n"
		if err := os.WriteFile(filepath.Join(binDir, "wasm-pack"), []byte(stub), 0o755); err != nil {
			t.Fatalf("write wasm-pack stub: %v", err)
		}
		customHybridDir := filepath.Join(t.TempDir(), "hybrid-out")
		cmd := exec.Command("bash", scriptPath)
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BV_HYBRID_WASM_DIR="+customHybridDir,
		)
		if cmdOut, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build_hybrid_wasm.sh with stub wasm-pack failed: %v (%s)", err, string(cmdOut))
		}
		recordedOut, err := os.ReadFile(outLog)
		if err != nil {
			t.Fatalf("read wasm-pack outLog: %v", err)
		}
		if string(recordedOut) != customHybridDir {
			t.Fatalf("build_hybrid_wasm.sh --out-dir = %q, want %q", string(recordedOut), customHybridDir)
		}

		// Also verify default (no BV_HYBRID_WASM_DIR, no flags) targets .bv/wasm/hybrid outside pkg/export:
		defaultCmd := exec.Command("bash", scriptPath)
		var cleanEnv []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "BV_HYBRID_WASM_DIR=") && !strings.HasPrefix(kv, "PATH=") {
				cleanEnv = append(cleanEnv, kv)
			}
		}
		cleanEnv = append(cleanEnv, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		defaultCmd.Env = cleanEnv
		if cmdOut, err := defaultCmd.CombinedOutput(); err != nil {
			t.Fatalf("build_hybrid_wasm.sh default invocation failed: %v (%s)", err, string(cmdOut))
		}
		recordedDefaultOut, err := os.ReadFile(outLog)
		if err != nil {
			t.Fatalf("read wasm-pack default outLog: %v", err)
		}
		if !strings.HasSuffix(filepath.ToSlash(string(recordedDefaultOut)), "/.bv/wasm/hybrid") || strings.Contains(filepath.ToSlash(string(recordedDefaultOut)), "/pkg/export/") {
			t.Fatalf("build_hybrid_wasm.sh default --out-dir = %q, want suffix /.bv/wasm/hybrid outside pkg/export", string(recordedDefaultOut))
		}
	}
}

func TestCopyEmbeddedAssetsWithSubtitle_ExcludesDevOnlyAssetsFromDiskAndOfflineManifest(t *testing.T) {
	outDir := t.TempDir()
	// Pre-populate optional wasm/ directory with both runtime WASM and dev-only wasm-pack artifacts.
	wasmDir := filepath.Join(outDir, "wasm")
	if err := os.MkdirAll(wasmDir, 0o755); err != nil {
		t.Fatalf("MkdirAll wasm: %v", err)
	}
	for name, body := range map[string]string{
		"bv_hybrid_scorer.js":           "export default function init() {}",
		"bv_hybrid_scorer_bg.wasm":      "\x00asm\x01\x00\x00\x00",
		"bv_hybrid_scorer.d.ts":         "export class HybridScorer {}",
		"bv_hybrid_scorer_bg.wasm.d.ts": "export const memory: WebAssembly.Memory;",
		"package.json":                  `{"name":"bv-hybrid-scorer"}`,
		"scorer.test.js":                "throw new Error('dev test');",
	} {
		if err := os.WriteFile(filepath.Join(wasmDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}

	if err := CopyEmbeddedAssetsWithSubtitle(outDir, "Audit Title", "Audit Subtitle"); err != nil {
		t.Fatalf("CopyEmbeddedAssetsWithSubtitle failed: %v", err)
	}

	// Planted negative controls: dev-only embedded files must never exist on disk after CopyEmbeddedAssetsWithSubtitle.
	for _, devFile := range []string{"hybrid_scorer.test.js", "graph-demo.html"} {
		if _, err := os.Stat(filepath.Join(outDir, devFile)); !os.IsNotExist(err) {
			t.Fatalf("dev-only asset %s must not exist after CopyEmbeddedAssetsWithSubtitle (err=%v)", devFile, err)
		}
	}

	// Positive observables: production viewer assets exist and coi-serviceworker.js binds runtime wasm, never dev-only files.
	for _, prodFile := range []string{"index.html", "viewer.js", "graph.js", "hybrid_scorer.js", "coi-serviceworker.js"} {
		if _, err := os.Stat(filepath.Join(outDir, prodFile)); err != nil {
			t.Fatalf("expected production asset %s: %v", prodFile, err)
		}
	}
	swBytes, err := os.ReadFile(filepath.Join(outDir, "coi-serviceworker.js"))
	if err != nil {
		t.Fatalf("read coi-serviceworker.js: %v", err)
	}
	swText := string(swBytes)
	if !strings.Contains(swText, `"wasm/bv_hybrid_scorer.js"`) || !strings.Contains(swText, `"wasm/bv_hybrid_scorer_bg.wasm"`) {
		t.Fatalf("expected runtime wasm files in OFFLINE_ASSETS, got:\n%s", swText)
	}
	for _, forbidden := range []string{"hybrid_scorer.test.js", "graph-demo.html", ".d.ts", "package.json", "scorer.test.js"} {
		if strings.Contains(swText, forbidden) {
			t.Fatalf("dev-only asset %q leaked into coi-serviceworker.js OFFLINE_ASSETS:\n%s", forbidden, swText)
		}
	}
}

func TestBundleHybridWasmEvaluatedAndPrepared(t *testing.T) {
	var nilBundle *Bundle
	if nilBundle.HybridWasmEvaluated() || nilBundle.HybridWasmPrepared() {
		t.Fatal("nil Bundle must report HybridWasmEvaluated=false and HybridWasmPrepared=false")
	}
	// Calling Mark* on nil Bundle must be a safe no-op.
	nilBundle.MarkHybridWasmEvaluated()
	nilBundle.MarkHybridWasmPrepared()
	if nilBundle.HybridWasmEvaluated() || nilBundle.HybridWasmPrepared() {
		t.Fatal("nil Bundle must remain false after MarkHybridWasmEvaluated/MarkHybridWasmPrepared")
	}

	bEvalOnly, err := OpenBundle(t.TempDir())
	if err != nil {
		t.Fatalf("OpenBundle(evalOnly): %v", err)
	}
	defer bEvalOnly.Close()
	if bEvalOnly.HybridWasmEvaluated() || bEvalOnly.HybridWasmPrepared() {
		t.Fatal("fresh Bundle must start with HybridWasmEvaluated=false and HybridWasmPrepared=false")
	}
	bEvalOnly.MarkHybridWasmEvaluated()
	if !bEvalOnly.HybridWasmEvaluated() {
		t.Fatal("MarkHybridWasmEvaluated must set HybridWasmEvaluated=true")
	}
	if bEvalOnly.HybridWasmPrepared() {
		t.Fatal("MarkHybridWasmEvaluated must leave HybridWasmPrepared=false when no WASM was staged")
	}

	bPrepared, err := OpenBundle(t.TempDir())
	if err != nil {
		t.Fatalf("OpenBundle(prepared): %v", err)
	}
	defer bPrepared.Close()
	bPrepared.MarkHybridWasmPrepared()
	if !bPrepared.HybridWasmPrepared() || !bPrepared.HybridWasmEvaluated() {
		t.Fatalf("MarkHybridWasmPrepared must set both Prepared=true (%v) and Evaluated=true (%v)", bPrepared.HybridWasmPrepared(), bPrepared.HybridWasmEvaluated())
	}
}
