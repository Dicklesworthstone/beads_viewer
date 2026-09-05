// Package export provides viewer asset embedding for static site generation.
package export

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Note: WriteGitHubActionsWorkflow is defined in github.go

// ViewerAssetsFS embeds the viewer_assets directory for static site export.
// This allows the bv binary to include all necessary HTML/JS/CSS assets
// without requiring them to exist on the filesystem.
//
//go:embed viewer_assets
var ViewerAssetsFS embed.FS

// CopyEmbeddedAssets copies all embedded viewer assets to the specified output directory.
// If title is provided, it replaces "Beads Viewer" in index.html.
func CopyEmbeddedAssets(outputDir, title string) error {
	var offlineFiles []string
	// Walk the embedded filesystem and copy all files
	err := fs.WalkDir(ViewerAssetsFS, "viewer_assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// embed.FS always uses forward slashes, so use strings.TrimPrefix for cross-platform safety
		// (filepath.Rel could have issues on Windows with mixed separators)
		relPath := strings.TrimPrefix(path, "viewer_assets/")
		if relPath == path {
			// This is the root "viewer_assets" directory itself
			return nil
		}

		// Skip development-only assets. graph-demo.html loads third-party
		// scripts from remote CDNs (unpkg/d3js.org) without SRI, which would
		// let CDN-served JavaScript run on the exported dashboard's origin
		// next to the exported project database. Test files are dev-only too.
		if !d.IsDir() && isDevOnlyAsset(relPath) {
			return nil
		}

		// Convert to platform-specific path separator for the destination
		destPath := filepath.Join(outputDir, filepath.FromSlash(relPath))

		if d.IsDir() {
			return os.MkdirAll(destPath, 0755)
		}

		// Read the embedded file
		content, err := ViewerAssetsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Special handling for index.html to replace the title and add cache-busting
		if relPath == "index.html" {
			contentStr := string(content)
			if title != "" {
				contentStr = replaceTitle(contentStr, title)
			}
			// Always add cache-busting to prevent CDN from serving stale JS files
			contentStr = AddScriptCacheBusting(contentStr)
			content = []byte(contentStr)
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}

		// Write the file
		if relPath != "coi-serviceworker.js" {
			offlineFiles = append(offlineFiles, relPath)
		}
		return os.WriteFile(destPath, content, 0644)
	})

	if err != nil {
		return err
	}

	// SQLite export precedes asset copying. Bind the worker's offline cache to
	// this exact bundle, including config/chunks, before a browser can activate it.
	for _, name := range []string{"beads.sqlite3", "beads.sqlite3.config.json", "chunks", "data"} {
		base := filepath.Join(outputDir, name)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue // CopyEmbeddedAssets also supports standalone asset callers.
		}
		if err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				rel, err := filepath.Rel(outputDir, p)
				if err != nil {
					return err
				}
				offlineFiles = append(offlineFiles, filepath.ToSlash(rel))
			}
			return nil
		}); err != nil {
			return fmt.Errorf("collecting offline database assets: %w", err)
		}
	}
	if err := bindOfflineAssets(outputDir, offlineFiles); err != nil {
		return err
	}

	// Always add GitHub Actions workflow for reliable Pages deployment
	// This ensures deployments trigger even if the built-in Pages workflow doesn't auto-trigger
	if wfErr := WriteGitHubActionsWorkflow(outputDir); wfErr != nil {
		// Non-fatal - just log a warning (fmt is already imported via other usage)
		fmt.Printf("  Warning: Could not add GitHub Actions workflow: %v\n", wfErr)
	}

	return nil
}

// bindOfflineAssets embeds the exact file hashes consumed by SW installation.
// A missing or changed asset prevents activation; it cannot create a verified
// partial offline bundle. This list exists only to serve the offline cache.
func bindOfflineAssets(outputDir string, files []string) error {
	sort.Strings(files)
	type asset struct {
		Path string `json:"path"`
		Hash string `json:"sha256"`
	}
	assets := make([]asset, 0, len(files))
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("hashing offline asset %s: %w", name, err)
		}
		assets = append(assets, asset{name, fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	encoded, err := json.Marshal(assets)
	if err != nil {
		return fmt.Errorf("encoding offline assets: %w", err)
	}
	worker, err := ViewerAssetsFS.ReadFile("viewer_assets/coi-serviceworker.js")
	if err != nil {
		return fmt.Errorf("reading offline worker: %w", err)
	}
	hash := sha256.New()
	hash.Write(worker)
	hash.Write(encoded)
	content := strings.Replace(string(worker), "const OFFLINE_ASSETS = [];", "const OFFLINE_ASSETS = "+string(encoded)+";", 1)
	content = strings.Replace(content, "const CACHE_REVISION = 'development';", fmt.Sprintf("const CACHE_REVISION = '%x';", hash.Sum(nil)), 1)
	return os.WriteFile(filepath.Join(outputDir, "coi-serviceworker.js"), []byte(content), 0644)
}

// isDevOnlyAsset reports whether a viewer asset is development-only and must
// not be included in production export bundles.
func isDevOnlyAsset(relPath string) bool {
	if relPath == "graph-demo.html" {
		return true
	}
	return strings.HasSuffix(relPath, ".test.js")
}

// replaceTitle replaces the default title in HTML content with the provided title.
// It replaces both the <title> tag and the h1 header.
// The title is HTML-escaped to prevent XSS and broken HTML.
func replaceTitle(content, title string) string {
	if title == "" {
		return content
	}

	// Escape the title for safe HTML insertion
	safeTitle := html.EscapeString(title)

	// Replace title in <title> tag
	content = strings.Replace(content, "<title>Beads Viewer</title>", "<title>"+safeTitle+"</title>", 1)

	// Replace title in h1 header
	content = strings.Replace(content, `<h1 class="text-xl font-semibold">Beads Viewer</h1>`, `<h1 class="text-xl font-semibold">`+safeTitle+`</h1>`, 1)

	return content
}

// AddScriptCacheBusting adds a cache-busting query parameter to script src attributes.
// This ensures browsers fetch fresh JS files after deployments, preventing stale code
// from being served by CDN caches (which was causing the "Test Issue 1/2/3" bug where
// old cached viewer.js would use OPFS-cached stale data).
func AddScriptCacheBusting(content string) string {
	// Generate timestamp for cache-busting
	cacheBuster := fmt.Sprintf("?v=%d", time.Now().Unix())

	// List of our JS files that need cache-busting (not vendor files which rarely change)
	jsFiles := []string{
		"head_init.js",
		"viewer.js",
		"charts.js",
		"graph.js",
		"hybrid_scorer.js",
		"wasm_loader.js",
	}

	for _, jsFile := range jsFiles {
		// Replace both src="file.js" and src='file.js' patterns
		oldSrc := fmt.Sprintf(`src="%s"`, jsFile)
		newSrc := fmt.Sprintf(`src="%s%s"`, jsFile, cacheBuster)
		content = strings.Replace(content, oldSrc, newSrc, -1)

		oldSrcSingle := fmt.Sprintf(`src='%s'`, jsFile)
		newSrcSingle := fmt.Sprintf(`src='%s%s'`, jsFile, cacheBuster)
		content = strings.Replace(content, oldSrcSingle, newSrcSingle, -1)
	}

	return content
}

// HasEmbeddedAssets returns true if viewer assets are embedded in the binary.
func HasEmbeddedAssets() bool {
	// Check if we can read the index.html from the embedded FS
	_, err := ViewerAssetsFS.ReadFile("viewer_assets/index.html")
	return err == nil
}

// AddGitHubWorkflowToBundle adds the GitHub Actions workflow to an exported bundle.
// This should be called after CopyEmbeddedAssets to ensure the workflow is present.
func AddGitHubWorkflowToBundle(outputDir string) error {
	return WriteGitHubActionsWorkflow(outputDir)
}
