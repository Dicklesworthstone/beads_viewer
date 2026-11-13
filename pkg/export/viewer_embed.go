// Package export provides viewer asset embedding for static site generation.
package export

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// Bundle anchors all generated files to the output directory selected by the
// invoking user. Keep it open across an export so later path replacements cannot
// redirect writes, reads, or cleanup through a different directory.
type Bundle struct {
	root *os.Root
}

// OpenBundle creates the selected output directory if necessary and opens its
// resolved directory as the boundary for all subsequent bundle operations.
func OpenBundle(outputDir string) (*Bundle, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create export directory: %w", err)
	}
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return nil, fmt.Errorf("open export directory: %w", err)
	}
	return &Bundle{root: root}, nil
}

// openFileBundle anchors a standalone output file to its existing parent. Unlike
// a Pages bundle, writing a single file must not create missing parent folders
// or interpret a trailing separator as a filename.
func openFileBundle(path string) (*Bundle, string, error) {
	parent, name := filepath.Split(path)
	if name == "" {
		return nil, "", fmt.Errorf("export output must name a file: %q", path)
	}
	if parent == "" {
		parent = "."
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", fmt.Errorf("open export file directory: %w", err)
	}
	return &Bundle{root: root}, name, nil
}

// Close releases the bundle's directory handle.
func (b *Bundle) Close() error {
	return b.root.Close()
}

// MkdirAll creates relative directories without allowing traversal outside the bundle.
func (b *Bundle) MkdirAll(name string, perm os.FileMode) error {
	return b.root.MkdirAll(name, perm)
}

// WriteFile atomically replaces a file relative to the selected bundle root.
// Replacing the directory entry avoids following a leaf symlink or modifying a
// hard-linked file outside the bundle. os.Root confines parent traversal too.
func (b *Bundle) WriteFile(name string, content []byte, perm os.FileMode) error {
	return b.writeFile(name, perm, func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

// CopyFile streams a file into an atomic replacement within the bundle.
func (b *Bundle) CopyFile(name string, source io.Reader, perm os.FileMode) error {
	return b.writeFile(name, perm, func(w io.Writer) error {
		_, err := io.Copy(w, source)
		return err
	})
}

func (b *Bundle) writeFile(name string, perm os.FileMode, write func(io.Writer) error) error {
	if !filepath.IsLocal(name) {
		return fmt.Errorf("export path must remain within the selected directory: %q", name)
	}
	if err := b.root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return fmt.Errorf("create export parent for %s: %w", name, err)
	}
	preserveMode := false
	if info, err := b.root.Lstat(name); err == nil {
		// In-place writes retained an existing file's permissions. Preserve
		// those bits when replacing a regular file, including a hard link,
		// without borrowing permissions from an untrusted symlink target.
		if info.Mode().IsRegular() {
			perm = info.Mode().Perm()
			preserveMode = true
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect export destination %s: %w", name, err)
	}
	temporary := ".bv-export-" + rand.Text()
	file, err := b.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create temporary export file: %w", err)
	}
	defer b.root.Remove(temporary)
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	if preserveMode {
		// OpenFile applies umask to a new inode. Restore the already existing
		// permissions on the held temporary file so replacement neither widens
		// private files nor removes explicitly configured group access.
		if err := file.Chmod(perm); err != nil {
			file.Close()
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := b.root.Rename(temporary, name); err != nil {
		return fmt.Errorf("publish export file %s: %w", name, err)
	}
	return nil
}

// EmbedAssets copies all embedded viewer assets to the specified output directory.
// If title is provided, it replaces "Beads Viewer" in index.html.
func EmbedAssets(outputDir, title string) error {
	return EmbedAssetsWithSubtitle(outputDir, title, "")
}

// EmbedAssetsWithSubtitle copies all embedded viewer assets to the specified output directory,
// replacing the default title when non-empty and rendering an optional subtitle beneath the navbar h1.
func EmbedAssetsWithSubtitle(outputDir, title, subtitle string) error {
	bundle, err := OpenBundle(outputDir)
	if err != nil {
		return err
	}
	defer bundle.Close()
	return bundle.CopyEmbeddedAssetsWithSubtitle(title, subtitle)
}

// CopyEmbeddedAssets copies all embedded viewer assets to the specified output directory.
// If title is provided, it replaces "Beads Viewer" in index.html.
func CopyEmbeddedAssets(outputDir, title string) error {
	return EmbedAssetsWithSubtitle(outputDir, title, "")
}

// CopyEmbeddedAssetsWithSubtitle copies all embedded viewer assets to the specified output directory,
// replacing the default title and rendering an optional subtitle beneath the navbar h1.
func CopyEmbeddedAssetsWithSubtitle(outputDir, title, subtitle string) error {
	return EmbedAssetsWithSubtitle(outputDir, title, subtitle)
}

// CopyEmbeddedAssets writes viewer assets through the held bundle boundary.
func (b *Bundle) CopyEmbeddedAssets(title string) error {
	return b.CopyEmbeddedAssetsWithSubtitle(title, "")
}

// CopyEmbeddedAssetsWithSubtitle writes viewer assets through the held bundle boundary,
// applying optional title and subtitle customization to index.html.
func (b *Bundle) CopyEmbeddedAssetsWithSubtitle(title, subtitle string) error {
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
		destPath := filepath.FromSlash(relPath)

		if d.IsDir() {
			return b.root.MkdirAll(destPath, 0755)
		}

		// Read the embedded file
		content, err := ViewerAssetsFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Special handling for index.html to replace the title/subtitle and add cache-busting
		if relPath == "index.html" {
			contentStr := string(content)
			if title != "" {
				contentStr = replaceTitle(contentStr, title)
			}
			if strings.TrimSpace(subtitle) != "" {
				contentStr = ReplaceSubtitle(contentStr, subtitle)
			}
			// Always add cache-busting to prevent CDN from serving stale JS files
			contentStr = AddScriptCacheBusting(contentStr)
			content = []byte(contentStr)
		}

		// Write the file
		if relPath != "coi-serviceworker.js" {
			offlineFiles = append(offlineFiles, relPath)
		}
		return b.WriteFile(destPath, content, 0644)
	})

	if err != nil {
		return err
	}

	// SQLite export precedes asset copying. Bind the worker's offline cache to
	// this exact bundle, including config/chunks, before a browser can activate it.
	for _, name := range []string{"beads.sqlite3", "beads.sqlite3.config.json", "chunks", "data"} {
		if _, err := b.root.Stat(name); os.IsNotExist(err) {
			continue // CopyEmbeddedAssets also supports standalone asset callers.
		} else if err != nil {
			return fmt.Errorf("inspect offline asset %s: %w", name, err)
		}
		if err := fs.WalkDir(b.root.FS(), name, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				if !d.Type().IsRegular() {
					return fmt.Errorf("offline asset %s is not a regular file", p)
				}
				offlineFiles = append(offlineFiles, p)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("collecting offline database assets: %w", err)
		}
	}
	if err := bindOfflineAssets(b, offlineFiles); err != nil {
		return err
	}

	// Always add GitHub Actions workflow for reliable Pages deployment
	// This ensures deployments trigger even if the built-in Pages workflow doesn't auto-trigger
	if wfErr := b.WriteFile(filepath.Join(".github", "workflows", "static.yml"), []byte(GitHubActionsWorkflowContent), 0644); wfErr != nil {
		// Non-fatal - just log a warning (fmt is already imported via other usage)
		fmt.Printf("  Warning: Could not add GitHub Actions workflow: %v\n", wfErr)
	}

	return nil
}

// bindOfflineAssets embeds the exact file hashes consumed by SW installation.
// A missing or changed asset prevents activation; it cannot create a verified
// partial offline bundle. This list exists only to serve the offline cache.
func bindOfflineAssets(bundle *Bundle, files []string) error {
	sort.Strings(files)
	type asset struct {
		Path string `json:"path"`
		Hash string `json:"sha256"`
	}
	assets := make([]asset, 0, len(files))
	for _, name := range files {
		data, err := bundle.root.ReadFile(filepath.FromSlash(name))
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
	return bundle.WriteFile("coi-serviceworker.js", []byte(content), 0644)
}

// isDevOnlyAsset reports whether a viewer asset is development-only and must
// not be included in production export bundles.
func isDevOnlyAsset(relPath string) bool {
	if relPath == "graph-demo.html" {
		return true
	}
	return strings.HasSuffix(relPath, ".test.js")
}

var (
	defaultNavbarH1Pattern = regexp.MustCompile(`(<h1\b[^>]*>)Beads Viewer(</h1>)`)
	firstNavbarH1Pattern   = regexp.MustCompile(`<h1\b[^>]*>[^<]*</h1>`)
)

// replaceTitle replaces the default title in HTML content with the provided title.
// It replaces both the <title> tag and the h1 header regardless of responsive classes.
// The title is HTML-escaped to prevent XSS and broken HTML.
func replaceTitle(content, title string) string {
	if title == "" {
		return content
	}

	// Escape the title for safe HTML insertion
	safeTitle := html.EscapeString(title)

	// Replace title in <title> tag
	content = strings.Replace(content, "<title>Beads Viewer</title>", "<title>"+safeTitle+"</title>", 1)

	// Replace title in navbar h1 header while preserving existing h1 attributes/classes
	if loc := defaultNavbarH1Pattern.FindStringSubmatchIndex(content); loc != nil {
		content = content[:loc[0]] + content[loc[2]:loc[3]] + safeTitle + content[loc[4]:loc[5]] + content[loc[1]:]
	}

	return content
}

// ReplaceSubtitle inserts an HTML-escaped subtitle paragraph directly beneath the navbar <h1>
// when subtitle is non-whitespace.
func ReplaceSubtitle(content, subtitle string) string {
	trimmed := strings.TrimSpace(subtitle)
	if trimmed == "" {
		return content
	}

	loc := firstNavbarH1Pattern.FindStringIndex(content)
	if loc == nil {
		return content
	}

	safeSubtitle := html.EscapeString(trimmed)
	subtitleEl := "\n              <p class=\"text-xs text-gray-500 dark:text-gray-400 truncate\">" + safeSubtitle + "</p>"
	return content[:loc[1]] + subtitleEl + content[loc[1]:]
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
