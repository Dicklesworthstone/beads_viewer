package loader

import (
	"bufio"
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	json "github.com/goccy/go-json"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// beadsMetadata is the subset of .beads/metadata.json we care about.
type beadsMetadata struct {
	Backend     string `json:"backend"`
	Database    string `json:"database"`
	JSONLExport string `json:"jsonl_export"`
}

type trackerCapabilities struct {
	Executable string
	Claim      bool
	Error      string
}

var trackerCapabilityCache sync.Map

// installedTrackerCapabilities only runs help, never a command that opens or
// changes a tracker. Include executable identity in the cache key so replacing
// an installed version during a long-running TUI invalidates its capabilities.
func installedTrackerCapabilities(tracker string) trackerCapabilities {
	executable, err := exec.LookPath(tracker)
	if err != nil {
		return trackerCapabilities{Error: "tracker executable is unavailable: " + tracker}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return trackerCapabilities{Error: "cannot resolve tracker executable"}
	}
	info, err := os.Stat(executable)
	if err != nil {
		return trackerCapabilities{Error: "cannot inspect tracker executable"}
	}
	key := fmt.Sprintf("%s:%d:%d", executable, info.Size(), info.ModTime().UnixNano())
	if cached, ok := trackerCapabilityCache.Load(key); ok {
		return cached.(trackerCapabilities)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	help, err := exec.CommandContext(ctx, executable, "update", "--help").Output()
	caps := trackerCapabilities{Executable: executable}
	if err != nil {
		caps.Error = "cannot establish installed tracker capabilities"
	} else {
		fields := strings.Fields(string(help))
		has := func(flag string) bool {
			for _, field := range fields {
				if field == flag {
					return true
				}
			}
			return false
		}
		if !has("--db") || !has("--json") || (tracker == "br" && (!has("--no-auto-import") || !has("--no-auto-flush"))) {
			caps.Error = "installed tracker cannot bind the required explicit database route"
		}
		caps.Claim = has("--claim")
	}
	trackerCapabilityCache.Store(key, caps)
	return caps
}

// AttachIssueOrigins binds IDs to an actual metadata-declared source before
// any display namespace is applied. An arbitrary --db JSONL/SQLite input stays
// readable but cannot borrow a nearby tracker merely by containing matching IDs.
func AttachIssueOrigins(issues []model.Issue, sourcePath string, complete bool) {
	origin := resolveIssueOrigin(sourcePath)
	if !complete && origin.ReadOnlyReason == "" {
		origin.ReadOnlyReason = "source authority is incomplete or stale"
	}
	for i := range issues {
		local := origin
		local.LocalID = issues[i].ID
		issues[i].Origin = &local
	}
}

func resolveIssueOrigin(sourcePath string) model.IssueOrigin {
	origin := model.IssueOrigin{}
	refuse := func(reason string) model.IssueOrigin {
		origin.ReadOnlyReason = reason
		return origin
	}
	path, err := filepath.Abs(sourcePath)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return refuse("source path cannot be resolved to a live tracker")
	}
	beadsDir := filepath.Dir(path)
	metadata, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return refuse("source has no readable tracker metadata")
	}
	var meta beadsMetadata
	if err := stdjson.Unmarshal(metadata, &meta); err != nil {
		return refuse("source tracker metadata is invalid")
	}
	switch strings.ToLower(strings.TrimSpace(meta.Backend)) {
	case "", "sqlite", "dolt":
	default:
		return refuse("unsupported tracker backend: " + meta.Backend)
	}
	origin.TrackerDirectory, origin.WorkingDirectory = beadsDir, filepath.Dir(beadsDir)
	origin.Tracker = "br"
	resolve := func(name string) (string, error) {
		if name == "" {
			return "", fmt.Errorf("empty tracker path")
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(beadsDir, name)
		}
		return filepath.EvalSymlinks(name)
	}
	if IsBDWorkspace(beadsDir) {
		origin.Tracker = "bd"
		// The bd bridge specifically exports issues.jsonl from this Dolt
		// directory. An unrelated sidecar is not that live source.
		if filepath.Base(path) != "issues.jsonl" {
			return refuse("source is not the live bd compatibility export")
		}
		origin.Database = beadsDir
	} else {
		database, dbErr := resolve(meta.Database)
		if dbErr != nil {
			return refuse("metadata does not resolve to an existing tracker database")
		}
		info, err := os.Stat(database)
		if err != nil || !info.Mode().IsRegular() {
			return refuse("metadata database is not a regular file")
		}
		exportPath, _ := resolve(meta.JSONLExport)
		if path != database && path != exportPath {
			return refuse("source is not the metadata-declared live database or export")
		}
		origin.Database = database
	}
	caps := installedTrackerCapabilities(origin.Tracker)
	if caps.Error != "" {
		return refuse(caps.Error)
	}
	origin.Executable, origin.SupportsClaim = caps.Executable, caps.Claim
	return origin
}

// BeadsDirEnvVar is the name of the environment variable for custom beads directory
const BeadsDirEnvVar = "BEADS_DIR"

// BeadsDBEnvVar is the name of the environment variable for a specific database file
// or .beads directory path. Takes priority over BEADS_DIR.
// Can point to a specific file (e.g., /path/to/.beads/issues.jsonl) or a .beads directory.
const BeadsDBEnvVar = "BEADS_DB"

// PreferredJSONLNames defines the priority order for looking up beads data files.
// Priority order matches current br's canonical JSONL export first, with
// legacy bd/beads.jsonl workspaces still supported as a fallback.
var PreferredJSONLNames = []string{"issues.jsonl", "beads.jsonl", "beads.base.jsonl"}

// GetBeadsDir returns the beads directory path, with the following priority:
//  1. BEADS_DB env var (can point to a file or directory; if file, returns parent dir)
//  2. BEADS_DIR env var (used directly as the .beads directory)
//  3. .beads in the given repoPath (or cwd if empty)
//  4. .beads in the main git repository root (for worktrees)
func GetBeadsDir(repoPath string) (string, error) {
	beadsDir, _, err := GetBeadsDirWithTrace(repoPath)
	return beadsDir, err
}

// GetBeadsDirWithTrace resolves the same tracker authority as GetBeadsDir and
// also returns every redirect file whose present or future value can alter that
// route. The trace includes the terminal directory's absent redirect path so a
// watcher can observe a newly added hop.
func GetBeadsDirWithTrace(repoPath string) (string, []string, error) {
	// Check BEADS_DB environment variable first (highest priority after --db flag)
	if envDB := env.BeadsDB.Get(); envDB != "" {
		return resolveBeadsDBWithTrace(envDB)
	}

	// Check BEADS_DIR environment variable
	if envDir := env.BeadsDir.Get(); envDir != "" {
		return resolveBeadsRedirect(envDir)
	}

	// Fall back to .beads in repo path
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
	}

	// Check for .beads in the given path first
	beadsDir := filepath.Join(repoPath, ".beads")
	if _, err := os.Stat(beadsDir); err == nil {
		return resolveBeadsRedirect(beadsDir)
	} else if !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("inspect beads directory %s: %w", beadsDir, err)
	}

	// If not found, check if we're in a git worktree and look in the main repo
	mainRepoRoot, err := getMainRepoRoot(repoPath)
	if err == nil && mainRepoRoot != "" && mainRepoRoot != repoPath {
		mainBeadsDir := filepath.Join(mainRepoRoot, ".beads")
		if _, err := os.Stat(mainBeadsDir); err == nil {
			return resolveBeadsRedirect(mainBeadsDir)
		} else if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect main repository beads directory %s: %w", mainBeadsDir, err)
		}
	}

	// Return the original path even if .beads doesn't exist
	// (caller will handle the error)
	return beadsDir, []string{filepath.Join(beadsDir, "redirect")}, nil
}

// maxRedirectBytes and maxRedirectDepth bound .beads/redirect resolution,
// matching br's routing limits so bv and br agree on the target store.
const (
	maxRedirectBytes = 4096
	maxRedirectDepth = 10
)

// followBeadsRedirect resolves a .beads/redirect chain to its terminal beads
// directory, mirroring `br where` so bv reads from the same store br writes to.
// When beadsDir has no redirect file, it is returned unchanged. A malformed
// redirect (oversized, non-UTF-8, loop, missing target, or a target that is not
// a .beads/_beads directory) is surfaced as an error rather than silently
// falling back to the local .beads, which would reintroduce the stale-read bug.
func followBeadsRedirect(beadsDir string) (string, error) {
	resolved, _, err := resolveBeadsRedirect(beadsDir)
	return resolved, err
}

func resolveBeadsRedirect(beadsDir string) (string, []string, error) {
	current := beadsDir
	if abs, err := filepath.Abs(current); err == nil {
		current = abs
	}
	start := current
	visited := map[string]bool{current: true}
	redirectFiles := make([]string, 0, 2)

	for depth := 0; ; depth++ {
		redirectFiles = append(redirectFiles, filepath.Join(current, "redirect"))
		target, ok, err := readBeadsRedirect(current)
		if err != nil {
			return "", redirectFiles, err
		}
		if !ok {
			break
		}
		if depth >= maxRedirectDepth {
			return "", redirectFiles, fmt.Errorf("redirect chain exceeds max depth (%d): %s", maxRedirectDepth, beadsDir)
		}
		if abs, err := filepath.Abs(target); err == nil {
			target = abs
		}
		if target == current {
			break
		}
		if visited[target] {
			return "", redirectFiles, fmt.Errorf("redirect loop detected: %s -> %s", current, target)
		}
		visited[target] = true
		current = target
	}

	// No redirect was followed: return the original directory untouched.
	if current == start {
		return beadsDir, redirectFiles, nil
	}

	info, err := os.Stat(current)
	if err != nil || !info.IsDir() {
		return "", redirectFiles, fmt.Errorf("redirect target not found: %s", current)
	}
	if base := filepath.Base(current); base != ".beads" && base != "_beads" {
		return "", redirectFiles, fmt.Errorf("redirect target must be a .beads or _beads directory: %s", current)
	}
	return current, redirectFiles, nil
}

// ResolveBeadsDir follows a checked .beads/redirect chain for an explicitly
// chosen tracker directory without consulting BEADS_DB or BEADS_DIR. Workspace
// loaders use this to preserve each repository's configured route instead of
// letting one ambient process-wide selector override every workspace entry.
func ResolveBeadsDir(beadsDir string) (string, error) {
	return followBeadsRedirect(beadsDir)
}

// ResolveBeadsDirWithTrace is ResolveBeadsDir plus the redirect-file trace used
// by long-lived watchers.
func ResolveBeadsDirWithTrace(beadsDir string) (string, []string, error) {
	return resolveBeadsRedirect(beadsDir)
}

// readBeadsRedirect reads the redirect file inside beadsDir. It returns the
// resolved target directory and true when a non-empty redirect exists. Relative
// targets resolve against beadsDir itself (so "." stays in place), matching br.
func readBeadsRedirect(beadsDir string) (string, bool, error) {
	redirectPath := filepath.Join(beadsDir, "redirect")
	pathInfo, err := os.Stat(redirectPath)
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
		// No redirect file, or beadsDir is not a directory at all: report no
		// redirect and let the caller's directory read produce the real error.
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect redirect file %s: %w", redirectPath, err)
	}
	if !pathInfo.Mode().IsRegular() {
		return "", false, fmt.Errorf("redirect path is not a regular file: %s", redirectPath)
	}
	if pathInfo.Size() > maxRedirectBytes {
		return "", false, fmt.Errorf("redirect file exceeds maximum size of %d bytes: %s", maxRedirectBytes, redirectPath)
	}

	file, err := os.Open(redirectPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to read redirect file %s: %w", redirectPath, err)
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect opened redirect file %s: %w", redirectPath, err)
	}
	if !sameFileSnapshot(pathInfo, openedInfo) {
		return "", false, fmt.Errorf("redirect file changed while being opened: %s", redirectPath)
	}

	data, err := io.ReadAll(io.LimitReader(file, maxRedirectBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("failed to read redirect file %s: %w", redirectPath, err)
	}
	if len(data) > maxRedirectBytes {
		return "", false, fmt.Errorf("redirect file exceeds maximum size of %d bytes: %s", maxRedirectBytes, redirectPath)
	}

	afterInfo, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("failed to inspect redirect file after reading %s: %w", redirectPath, err)
	}
	currentPathInfo, err := os.Stat(redirectPath)
	if err != nil || !sameFileSnapshot(openedInfo, afterInfo) || !sameFileSnapshot(afterInfo, currentPathInfo) {
		return "", false, fmt.Errorf("redirect file changed while being read: %s", redirectPath)
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("redirect file must be valid UTF-8: %s", redirectPath)
	}

	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", false, nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(beadsDir, target)
	}
	return target, true, nil
}

func sameFileSnapshot(a, b os.FileInfo) bool {
	return a != nil && b != nil &&
		os.SameFile(a, b) &&
		a.Mode() == b.Mode() &&
		a.Size() == b.Size() &&
		a.ModTime().Equal(b.ModTime())
}

// resolveBeadsDB interprets a BEADS_DB value which can be either:
//   - An absolute path to a specific file (e.g., /path/to/.beads/beads.{jsonl,db,sqlite3})
//   - An absolute path to a .beads directory
//
// If it points to a file, returns the parent directory.
// If it points to a directory, returns the directory itself.
func resolveBeadsDB(dbPath string) (string, error) {
	beadsDir, _, err := resolveBeadsDBWithTrace(dbPath)
	return beadsDir, err
}

func resolveBeadsDBWithTrace(dbPath string) (string, []string, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		// Path doesn't exist yet -- guess based on whether it looks like a file path
		if looksLikeBeadsDBFile(dbPath) {
			return filepath.Dir(dbPath), nil, nil
		}
		// Assume it's a directory
		return resolveBeadsRedirect(dbPath)
	}

	if info.IsDir() {
		return resolveBeadsRedirect(dbPath)
	}

	// It's a file -- return the parent directory
	return filepath.Dir(dbPath), nil, nil
}

func looksLikeBeadsDBFile(dbPath string) bool {
	switch strings.ToLower(filepath.Ext(dbPath)) {
	case ".jsonl", ".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

// IsBDWorkspace returns true when the given .beads directory belongs to a
// modern Dolt-native bd workspace. Detection is based on the presence of a
// .beads/dolt/ (server mode) or .beads/embeddeddolt/ (embedded mode, bd 1.1+)
// subdirectory, or a metadata.json declaring backend=dolt.
func IsBDWorkspace(beadsDir string) bool {
	if beadsDir == "" {
		return false
	}

	// Fast path: bd stores Dolt data under .beads/dolt/ (server mode) or
	// .beads/embeddeddolt/ (embedded mode, the bd 1.1+ default) (#189).
	for _, dir := range []string{"dolt", "embeddeddolt"} {
		if info, err := os.Stat(filepath.Join(beadsDir, dir)); err == nil && info.IsDir() {
			return true
		}
	}

	// Fallback: metadata.json may explicitly record the backend.
	metaPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return false
	}

	var meta beadsMetadata
	if err := stdjson.Unmarshal(data, &meta); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(meta.Backend), "dolt")
}

// PrepareWorkspaceForRead resolves the active JSONL file for the workspace.
// For bd workspaces it can refresh .beads/issues.jsonl by running
// `bd export -o .beads/issues.jsonl` before reading. For regular br workspaces
// it falls through to FindJSONLPath.
func PrepareWorkspaceForRead(repoPath string, refreshBDExport bool, warnFunc func(string)) (string, string, error) {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return "", "", err
	}
	jsonlPath, err := PrepareBeadsDirForRead(beadsDir, refreshBDExport, warnFunc)
	if err != nil {
		return "", "", err
	}
	return beadsDir, jsonlPath, nil
}

// PrepareBeadsDirForRead resolves the active JSONL file for an explicit .beads
// directory. In bd workspaces the compatibility export at .beads/issues.jsonl
// is used (optionally refreshed). In regular br workspaces FindJSONLPath is
// used as before.
func PrepareBeadsDirForRead(beadsDir string, refreshBDExport bool, warnFunc func(string)) (string, error) {
	if IsBDWorkspace(beadsDir) {
		issuesPath := filepath.Join(beadsDir, "issues.jsonl")
		if refreshBDExport {
			if err := exportBDIssuesJSONL(beadsDir, issuesPath); err != nil {
				if _, statErr := os.Stat(issuesPath); statErr == nil {
					if warnFunc != nil {
						warnFunc(fmt.Sprintf("bd export failed, using existing issues.jsonl: %v", err))
					}
				} else {
					return "", fmt.Errorf("failed to refresh bd compatibility JSONL (run 'bd export -o .beads/issues.jsonl'): %w", err)
				}
			}
		}

		if _, err := os.Stat(issuesPath); err != nil {
			return "", fmt.Errorf("no compatibility JSONL found at %s; run 'bd export -o .beads/issues.jsonl'", issuesPath)
		}

		return issuesPath, nil
	}

	return FindJSONLPath(beadsDir)
}

// exportBDIssuesJSONL runs `bd export -o <issuesPath>` to produce a fresh
// JSONL compatibility file from the bd workspace's Dolt database.
func exportBDIssuesJSONL(beadsDir, issuesPath string) error {
	if _, err := exec.LookPath("bd"); err != nil {
		return fmt.Errorf("bd binary not found in PATH")
	}
	absBeadsDir, err := filepath.Abs(beadsDir)
	if err != nil {
		return fmt.Errorf("resolve bd workspace path %s: %w", beadsDir, err)
	}
	absIssuesPath, err := filepath.Abs(issuesPath)
	if err != nil {
		return fmt.Errorf("resolve bd export path %s: %w", issuesPath, err)
	}

	repoRoot := filepath.Dir(absBeadsDir)
	cmd := exec.Command("bd", "export", "-o", absIssuesPath)
	cmd.Dir = repoRoot
	env := withoutBeadsAuthorityEnv(os.Environ())
	cmd.Env = append(env, fmt.Sprintf("%s=%s", BeadsDirEnvVar, absBeadsDir))
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

func withoutBeadsAuthorityEnv(entries []string) []string {
	env := make([]string, 0, len(entries))
	for _, entry := range entries {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, BeadsDirEnvVar) || strings.EqualFold(key, BeadsDBEnvVar) {
			continue
		}
		env = append(env, entry)
	}
	return env
}

// getMainRepoRoot returns the root directory of the main git repository.
// For regular repos, this returns the repo root.
// For worktrees, this returns the main repository root (not the worktree root).
func getMainRepoRoot(repoPath string) (string, error) {
	// First, check if we're in a git repository at all
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = repoPath
	topLevelOut, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	worktreeRoot := strings.TrimSpace(string(topLevelOut))

	// Check if this is a worktree by looking at the git-common-dir
	// For regular repos: git-common-dir == git-dir
	// For worktrees: git-common-dir points to main repo's .git
	cmd = exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = repoPath
	commonDirOut, err := cmd.Output()
	if err != nil {
		// Fallback: not a worktree or old git version
		return worktreeRoot, nil
	}
	commonDir := strings.TrimSpace(string(commonDirOut))

	cmd = exec.Command("git", "rev-parse", "--path-format=absolute", "--git-dir")
	cmd.Dir = repoPath
	gitDirOut, err := cmd.Output()
	if err != nil {
		return worktreeRoot, nil
	}
	gitDir := strings.TrimSpace(string(gitDirOut))

	// If git-common-dir == git-dir, we're in a regular repo
	if commonDir == gitDir {
		return worktreeRoot, nil
	}

	// We're in a worktree. The main repo root is the parent of git-common-dir.
	// git-common-dir typically points to /path/to/main-repo/.git
	mainRepoRoot := filepath.Dir(commonDir)

	return mainRepoRoot, nil
}

// GetGitWorktreeRoot returns the top-level directory of the Git worktree that
// owns repoPath. Unlike getMainRepoRoot, it deliberately preserves linked
// worktree identity; caller-owned artifacts such as .bv indexes and baselines
// belong to the invoking checkout, not beside a redirected/shared tracker or
// in the main checkout. Ambient Git routing variables are removed so a parent
// hook cannot redirect the lookup into another repository.
func GetGitWorktreeRoot(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	cmd := exec.Command("git", "--no-replace-objects", "rev-parse", "--show-toplevel")
	cmd.Dir = repoPath
	cmd.Env = gitLoaderEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git worktree root from %s: %w", repoPath, err)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("resolve Git worktree root from %s: empty output", repoPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve absolute Git worktree root %s: %w", root, err)
	}
	return filepath.Clean(absRoot), nil
}

// GetGitDir returns the absolute Git administrative directory for repoPath.
// Ambient Git routing/configuration variables are removed so cmd.Dir remains
// the sole repository authority. In a linked worktree this intentionally
// returns that worktree's administrative directory, matching `git rev-parse
// --git-dir` and the location used for its beads-worktrees exports.
func GetGitDir(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %s: %w", repoPath, err)
	}
	cmd := exec.Command("git", "--no-replace-objects", "rev-parse", "--git-dir")
	cmd.Dir = absRepoPath
	cmd.Env = gitLoaderEnvironment()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git directory from %s: %w", repoPath, err)
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", fmt.Errorf("resolve Git directory from %s: empty output", repoPath)
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(absRepoPath, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

// FindJSONLPath locates the beads JSONL file in the given directory.
// Prefers issues.jsonl (current br) over beads.jsonl (legacy bd) and
// beads.base.jsonl (daemon/base export). Skips backup files and merge artifacts.
// Skips backup files and merge artifacts.
func FindJSONLPath(beadsDir string) (string, error) {
	return FindJSONLPathWithWarnings(beadsDir, nil)
}

// FindJSONLPathWithWarnings is like FindJSONLPath but optionally reports warnings
// about detected merge artifacts via the provided callback.
func FindJSONLPathWithWarnings(beadsDir string, warnFunc func(msg string)) (string, error) {
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return "", fmt.Errorf("failed to read beads directory: %w", err)
	}

	var candidates []string
	var mergeArtifacts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		// Must be a .jsonl file
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		// Skip backups, merge artifacts, and deletion manifests
		if strings.Contains(name, ".backup") ||
			strings.Contains(name, ".orig") ||
			strings.Contains(name, ".merge") ||
			name == "deletions.jsonl" {
			continue
		}

		// Skip git merge conflict artifacts (beads.left.jsonl, beads.right.jsonl)
		// These are OURS/THEIRS sides during a merge conflict
		if strings.HasPrefix(name, "beads.left") || strings.HasPrefix(name, "beads.right") {
			mergeArtifacts = append(mergeArtifacts, name)
			continue
		}

		candidates = append(candidates, name)
	}

	// Warn about detected merge artifacts
	if len(mergeArtifacts) > 0 && warnFunc != nil {
		warnFunc(fmt.Sprintf("Merge artifact files detected: %s. Clean them up before relying on the JSONL view.",
			strings.Join(mergeArtifacts, ", ")))
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no beads JSONL file found in %s", beadsDir)
	}

	// Priority order for beads files:
	// Current br stack: issues.jsonl -> beads.jsonl -> beads.base.jsonl
	// Legacy bd workspaces remain readable through the beads.jsonl fallback.
	preferredNames := PreferredJSONLNames
	isBD := IsBDWorkspace(beadsDir)
	if isBD {
		preferredNames = []string{"issues.jsonl", "beads.jsonl", "beads.base.jsonl"}
	}

	for _, preferred := range preferredNames {
		for _, name := range candidates {
			if name == preferred {
				path := filepath.Join(beadsDir, name)
				// Check if file has content (skip empty files)
				if info, err := os.Stat(path); err == nil && info.Size() > 0 {
					return path, nil
				}
			}
		}
	}

	// In a bd (Dolt-backed) workspace the issue data lives in the Dolt
	// database; never fall back to a stray non-issue JSONL (memories,
	// interactions, ...) — that silently reports an empty project (#189).
	// Accept an existing-but-empty compatibility export (a legitimately empty
	// project); otherwise require the export.
	if isBD {
		issuesPath := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(issuesPath); err == nil {
			return issuesPath, nil
		}
		return "", fmt.Errorf("no compatibility JSONL found at %s; run 'bd export -o .beads/issues.jsonl'", issuesPath)
	}

	// Fall back to first non-empty candidate
	for _, name := range candidates {
		path := filepath.Join(beadsDir, name)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, nil
		}
	}

	// Last resort: return first candidate even if empty
	return filepath.Join(beadsDir, candidates[0]), nil
}

// LoadIssues reads issues from the beads directory.
// Respects BEADS_DIR environment variable, otherwise uses .beads in repoPath.
// Automatically finds the correct JSONL file (issues.jsonl preferred, beads.jsonl fallback).
func LoadIssues(repoPath string) ([]model.Issue, error) {
	beadsDir, err := GetBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}

	jsonlPath, err := FindJSONLPath(beadsDir)
	if err != nil {
		return nil, err
	}

	return LoadIssuesFromFile(jsonlPath)
}

// DefaultMaxBufferSize is the default buffer size for the scanner (10MB).
const DefaultMaxBufferSize = 1024 * 1024 * 10

// MaxLineSizeEnvVar overrides DefaultMaxBufferSize; the value is in megabytes.
const MaxLineSizeEnvVar = "BV_MAX_LINE_SIZE_MB"

// maxLineSizeMB caps MaxLineSizeEnvVar so a typo cannot ask for terabytes.
const maxLineSizeMB = 1024

// MaxLineSizeFromEnv returns the maximum JSONL line size in bytes configured
// through BV_MAX_LINE_SIZE_MB, or 0 when the variable is unset or invalid
// (ParseOptions treats 0 as DefaultMaxBufferSize). Every loading path — TUI
// and robot/datasource alike — must consult this so the documented variable
// has one meaning.
func MaxLineSizeFromEnv() int {
	raw := strings.TrimSpace(env.MaxLineSizeMB.Get())
	if raw == "" {
		return 0
	}
	mb, err := strconv.Atoi(raw)
	if err != nil || mb <= 0 {
		return 0
	}
	if mb > maxLineSizeMB {
		mb = maxLineSizeMB
	}
	return mb * 1024 * 1024
}

// ParseOptions configures the behavior of ParseIssues.
type ParseOptions struct {
	// WarningHandler is called with warning messages (e.g., malformed JSON).
	// If nil, warnings are printed to os.Stderr.
	WarningHandler func(string)

	// BufferSize sets the maximum line size (in bytes) to read at once.
	// Lines longer than this are skipped with a warning.
	// If 0, uses DefaultMaxBufferSize (10MB).
	BufferSize int

	// IssueFilter optionally filters parsed issues. Return true to include.
	// When nil, all valid issues are included.
	IssueFilter func(*model.Issue) bool

	// Stats, when non-nil, receives source-order per-line accounting as the
	// stream is parsed. This lets a single fused loader pass also serve as the
	// validation pass (issue count + malformed-error-rate gate) so the
	// 1.9MB issues.jsonl is read once instead of validate-then-load.
	// Issue-shaped records and over-limit lines that could not be classified are
	// accounted; recognized non-issue `_type` records are Skipped, while empty
	// lines are ignored. Treating an unreadable over-limit line as an error keeps
	// callers from mistaking a partial issue universe for a complete load.
	Stats *ParseStats

	// WarningCount, when non-nil, is incremented once for every warning the
	// parser encounters, even when WarningHandler is nil or a higher-level
	// caller summarizes the warning text. This is separate from Stats.Errors:
	// source-authority callers can add non-parse warnings to the same count.
	WarningCount *int
}

// ParseStats accumulates per-line accounting for a single parse pass so a load
// can also produce the corruption verdict (malformed-error-rate gate) without a
// second read of the file. The categories mirror datasource.validateJSONL: a line
// counts toward Valid when its JSON decodes AND the resulting issue passes model
// validation (which subsumes the required id/title/status check), and toward
// Errors when the JSON is malformed, the issue fails validation, a later
// validated record repeats an earlier issue ID, OR an over-limit line cannot be
// parsed far enough to classify. Duplicate records are removed from Valid and
// added to Errors. Empty lines are not accounted. Recognized non-issue `_type`
// records (and unknown `_type`) count toward Skipped — they are not errors, but
// a file made ENTIRELY of them yielded zero issues, which callers use to reject a
// wrong/non-issue source (e.g. a stray sprints.jsonl) rather than treat it as a
// valid empty project.
type ParseStats struct {
	// Valid is the number of unique issue-shaped lines that parsed and validated.
	Valid int
	// Errors is the number of issue-shaped lines that were malformed JSON or
	// failed model validation (e.g. missing required fields), plus duplicate IDs
	// and lines dropped for exceeding the per-line byte cap. Every dropped
	// record counts here so load_stats can surface it.
	Errors int
	// Skipped is the number of recognized non-issue `_type` records (memory,
	// sprint, forecast, burndown, ignore) plus unknown `_type` records — content
	// that was present but is not an issue.
	Skipped int
}

// ErrorRate returns the fraction of accounted issue lines that were errors.
// Returns 0 when no issue lines were seen (an empty file is valid).
func (s ParseStats) ErrorRate() float64 {
	total := s.Valid + s.Errors
	if total == 0 {
		return 0
	}
	return float64(s.Errors) / float64(total)
}

// openIssuesFile opens a regular file whose identity, mode, size and mtime match
// its inspected snapshot. Concurrent append or atomic replacement can invalidate
// that snapshot; discard the handle and inspect/open again without ever reading
// an unverified handle. Other errors remain immediate, and continuous changes
// are refused after a bounded number of attempts.
func openIssuesFile(path string, openFile func(string) (*os.File, error)) (*os.File, error) {
	const maxOpenAttempts = 3
	for attempt := 0; attempt < maxOpenAttempts; attempt++ {
		pathInfo, err := os.Stat(path)
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no beads issues found at %s", path)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to inspect issues file %s: %w", path, err)
		}
		if !pathInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("issues path is not a regular file: %s", path)
		}

		file, err := openFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open issues file: %w", err)
		}
		openedInfo, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("failed to inspect opened issues file %s: %w", path, err)
		}
		if sameFileSnapshot(pathInfo, openedInfo) {
			return file, nil
		}
		_ = file.Close()
	}
	return nil, fmt.Errorf("issues file changed while being opened: %s", path)
}

// LoadIssuesFromFileWithOptions reads issues from a file with custom options.
func LoadIssuesFromFileWithOptions(path string, opts ParseOptions) ([]model.Issue, error) {
	defer metrics.Timer(metrics.LoaderParse)()
	file, err := openIssuesFile(path, os.Open)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	return ParseIssuesWithOptions(file, opts)
}

// LoadIssuesFromFileWithOptionsPooled reads issues from a file with pooling enabled.
// The caller must return pooled issues via ReturnIssuePtrsToPool when no longer needed.
func LoadIssuesFromFileWithOptionsPooled(path string, opts ParseOptions) (PooledIssues, error) {
	defer metrics.Timer(metrics.LoaderParse)()
	file, err := openIssuesFile(path, os.Open)
	if err != nil {
		return PooledIssues{}, err
	}
	defer func() { _ = file.Close() }()

	return ParseIssuesWithOptionsPooled(file, opts)
}

// LoadIssuesFromFile reads issues directly from a specific JSONL file path.
func LoadIssuesFromFile(path string) ([]model.Issue, error) {
	return LoadIssuesFromFileWithOptions(path, ParseOptions{})
}

// LoadIssuesFromFilePooled reads issues directly from a JSONL file path with pooling enabled.
func LoadIssuesFromFilePooled(path string) (PooledIssues, error) {
	return LoadIssuesFromFileWithOptionsPooled(path, ParseOptions{})
}

// ParseIssues parses JSONL content from a reader into issues.
// Handles UTF-8 BOM stripping, large lines, and validation.
func ParseIssues(r io.Reader) ([]model.Issue, error) {
	return ParseIssuesWithOptions(r, ParseOptions{})
}

// ParseIssuesWithOptions parses JSONL content with custom options.
func ParseIssuesWithOptions(r io.Reader, opts ParseOptions) ([]model.Issue, error) {
	issues, _, err := parseIssuesWithOptions(r, opts, false)
	return issues, err
}

// ParseIssuesWithOptionsPooled parses JSONL content with pooling enabled.
// The caller must return pooled issues via ReturnIssuePtrsToPool when no longer needed.
func ParseIssuesWithOptionsPooled(r io.Reader, opts ParseOptions) (PooledIssues, error) {
	issues, poolRefs, err := parseIssuesWithOptions(r, opts, true)
	if err != nil {
		return PooledIssues{}, err
	}
	return PooledIssues{Issues: issues, PoolRefs: poolRefs}, nil
}

func parseIssuesWithOptions(r io.Reader, opts ParseOptions, usePool bool) ([]model.Issue, []*model.Issue, error) {
	// Determine buffer size (the 10MB-default per-line cap).
	maxCapacity := effectiveMaxCapacity(opts.BufferSize)

	// Parallel fast path: for large on-disk files, JSONL is line-independent
	// (one JSON object per line), so the decode is embarrassingly parallel.
	// We read the file once, split it into line-aligned chunks, decode the
	// chunks across a bounded worker pool, and reassemble in ORIGINAL ORDER.
	// This is the alien-graveyard §8.2 "morsel-driven parallelism" pattern:
	// fixed-size morsels pulled by a bounded set of workers, with results
	// stitched back deterministically. The path is behavior-equivalent to the
	// serial loop below (same BOM strip, same _type dispatch, same warnings in
	// original line order, same sequential filter calls, same ParseStats, and
	// the same pooled deep-copy semantics);
	// see parseIssuesParallel and the differential test in loader_test.go.
	if f, ok := r.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Size() >= parallelParseMinBytes && info.Size() <= parallelParseMaxBytes {
			data, withinLimit, rerr := readParallelCandidateBounded(f, parallelParseMaxBytes)
			if rerr != nil {
				return nil, nil, fmt.Errorf("error reading issues stream: %w", rerr)
			}
			if withinLimit {
				if countLines(data) >= parallelParseMinLines {
					return parseIssuesParallel(data, opts, usePool, maxCapacity)
				}
				// Small line count after all: fall back to the serial reader over
				// the bytes we already slurped (avoids a second read).
				r = bytes.NewReader(data)
			}
		}
	}

	var issues []model.Issue
	var poolRefs []*model.Issue
	if f, ok := r.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			est := estimateIssueCap(info.Size())
			if est > 0 {
				issues = make([]model.Issue, 0, est)
				if usePool {
					poolRefs = make([]*model.Issue, 0, est)
				}
			}
		}
	}

	reader := bufio.NewReaderSize(r, maxCapacity)

	warn := resolveWarnHandler(opts.WarningHandler, opts.WarningCount)
	decodeOpts := opts
	decodeOpts.IssueFilter = nil
	seenIDs := make(map[string]struct{})

	lineNum := 0
	for {
		lineNum++
		// ReadLine returns a single line, not including the end-of-line bytes.
		// If the line was too long for the buffer then isPrefix is set and the
		// beginning of the line is returned.
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			if usePool {
				ReturnIssuePtrsToPool(poolRefs)
			}
			return nil, nil, fmt.Errorf("error reading issues stream at line %d: %w", lineNum, err)
		}

		if isPrefix {
			// Line too long. Discard the rest of the line. It is a dropped
			// record, so it counts as an error: robot load_stats must surface
			// it just like malformed JSON (#190). Count before warning so a
			// handler that inspects Stats sees the record already accounted.
			if opts.Stats != nil {
				opts.Stats.Errors++
			}
			warn(fmt.Sprintf("skipping line %d: line too long (exceeds %d bytes)", lineNum, maxCapacity))
			for isPrefix {
				_, isPrefix, err = reader.ReadLine()
				if err != nil && err != io.EOF {
					if usePool {
						ReturnIssuePtrsToPool(poolRefs)
					}
					return nil, nil, fmt.Errorf("error skipping long line at line %d: %w", lineNum, err)
				}
				if err == io.EOF {
					break
				}
			}
			continue
		}

		// Match datasource validation semantics: blank lines containing spaces or
		// tabs are formatting, not malformed issue records.
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		// Strip UTF-8 BOM if present on the first line
		if lineNum == 1 {
			line = stripBOM(line)
		}

		before := len(issues)
		issues, poolRefs = processIssueLine(line, lineNum, decodeOpts, usePool, issues, poolRefs, opts.Stats, warn)
		if len(issues) != before && !keepParsedIssue(
			&issues[len(issues)-1], pooledRefAt(poolRefs, len(issues)-1), lineNum,
			opts, seenIDs, warn,
		) {
			clear(issues[len(issues)-1:])
			issues = issues[:len(issues)-1]
			if usePool {
				clear(poolRefs[len(poolRefs)-1:])
				poolRefs = poolRefs[:len(poolRefs)-1]
			}
		}
	}

	issues, poolRefs = normalizeEmptyIssueResults(issues, poolRefs)
	internRepeatedIssueStrings(issues, poolRefs)
	return issues, poolRefs, nil
}

// keepParsedIssue applies the order-sensitive policies that must remain serial:
// duplicate rejection and the caller-provided filter. Duplicate accounting is
// intentionally performed before filtering, so every validated record
// participates in data-integrity checks even when its canonical record is not
// selected for the returned view.
func keepParsedIssue(
	issue *model.Issue,
	poolRef *model.Issue,
	lineNum int,
	opts ParseOptions,
	seenIDs map[string]struct{},
	warn func(string),
) bool {
	if _, exists := seenIDs[issue.ID]; exists {
		if opts.Stats != nil {
			if opts.Stats.Valid > 0 {
				opts.Stats.Valid--
			}
			opts.Stats.Errors++
		}
		// Match malformed and validation warnings: callbacks observe the
		// accounting state after this source line has been fully classified.
		warn(fmt.Sprintf("skipping duplicate issue ID %q on line %d", issue.ID, lineNum))
		if poolRef != nil {
			PutIssue(poolRef)
		}
		return false
	}
	seenIDs[issue.ID] = struct{}{}

	filterTarget := issue
	if poolRef != nil {
		filterTarget = poolRef
	}
	if opts.IssueFilter != nil && !opts.IssueFilter(filterTarget) {
		if poolRef != nil {
			PutIssue(poolRef)
		}
		return false
	}
	if poolRef != nil {
		*issue = *poolRef
		DeepCopyIssueSlices(issue)
	}
	return true
}

func pooledRefAt(poolRefs []*model.Issue, index int) *model.Issue {
	if index >= 0 && index < len(poolRefs) {
		return poolRefs[index]
	}
	return nil
}

func normalizeEmptyIssueResults(issues []model.Issue, poolRefs []*model.Issue) ([]model.Issue, []*model.Issue) {
	if len(issues) == 0 {
		issues = nil
	}
	if len(poolRefs) == 0 {
		poolRefs = nil
	}
	return issues, poolRefs
}

// processIssueLine applies the full per-line loader semantics to a single
// (BOM-stripped, non-empty, end-of-line-trimmed) JSONL line and appends any
// resulting issue. It is the single source of truth shared by the serial reader
// loop and the parallel chunk workers, guaranteeing the two paths are
// behavior-equivalent: same `_type` dispatch, same malformed/invalid handling,
// same
// warning text keyed by lineNum, same ParseStats accounting, and the same
// pooled deep-copy semantics (bv-fn4b). It returns the (possibly grown) issues
// and poolRefs slices. stats may be nil; warn must be non-nil.
func processIssueLine(
	line []byte,
	lineNum int,
	opts ParseOptions,
	usePool bool,
	issues []model.Issue,
	poolRefs []*model.Issue,
	stats *ParseStats,
	warn func(string),
) ([]model.Issue, []*model.Issue) {
	// Dispatch by `_type` so non-issue records in beads JSONL
	// (e.g. memories, sprints, future record kinds) don't get parsed
	// as issues and warn-skipped with "issue ID cannot be empty"
	// on every load (issue #145). Empty / missing `_type` is the
	// historical "issue" shape and stays the default.
	switch recordTypeOf(line) {
	case recordTypeIssue:
		// fall through to the issue parser below
	case recordTypeMemory, recordTypeSprint, recordTypeForecast, recordTypeBurndown, recordTypeIgnore:
		// Recognized non-issue record. The viewer doesn't surface
		// these yet, so silently skip — we just need to not warn.
		if stats != nil {
			stats.Skipped++
		}
		return issues, poolRefs
	default:
		// Unknown _type: don't fail, but don't pretend it was an
		// issue either. A debug-level breadcrumb is enough; the
		// noisy "issue ID cannot be empty" warning was the actual
		// bug being reported.
		if stats != nil {
			stats.Skipped++
		}
		return issues, poolRefs
	}

	if usePool {
		issue := GetIssue()
		if err := json.Unmarshal(line, issue); err != nil {
			PutIssue(issue)
			if stats != nil {
				stats.Errors++
			}
			// Skip malformed lines but warn
			warn(fmt.Sprintf("skipping malformed JSON on line %d: %v", lineNum, err))
			return issues, poolRefs
		}

		normalizeLoadedIssue(issue)

		// Validate issue
		if err := issue.Validate(); err != nil {
			PutIssue(issue)
			if stats != nil {
				stats.Errors++
			}
			// Skip invalid issues
			warn(fmt.Sprintf("skipping invalid issue on line %d: %v", lineNum, err))
			return issues, poolRefs
		}
		if stats != nil {
			stats.Valid++
		}

		if opts.IssueFilter != nil && !opts.IssueFilter(issue) {
			PutIssue(issue)
			return issues, poolRefs
		}

		// Defer the deep copy until duplicate/filter policy accepts this record.
		// Until then the value intentionally shares slice storage with poolRef;
		// rejected rows can return to the pool without allocating a throwaway copy.
		issues = append(issues, *issue)
		poolRefs = append(poolRefs, issue)
		return issues, poolRefs
	}

	var issue model.Issue
	if err := json.Unmarshal(line, &issue); err != nil {
		if stats != nil {
			stats.Errors++
		}
		// Skip malformed lines but warn
		warn(fmt.Sprintf("skipping malformed JSON on line %d: %v", lineNum, err))
		return issues, poolRefs
	}

	normalizeLoadedIssue(&issue)

	// Validate issue
	if err := issue.Validate(); err != nil {
		if stats != nil {
			stats.Errors++
		}
		// Skip invalid issues
		warn(fmt.Sprintf("skipping invalid issue on line %d: %v", lineNum, err))
		return issues, poolRefs
	}
	if stats != nil {
		stats.Valid++
	}

	if opts.IssueFilter != nil && !opts.IssueFilter(&issue) {
		return issues, poolRefs
	}

	issues = append(issues, issue)
	return issues, poolRefs
}

// Parallel-parse tuning. JSONL is line-independent, so for large files the
// JSON decode is embarrassingly parallel. Below these thresholds the goroutine
// + reassembly overhead outweighs the win, so we keep the serial path. The
// Byte thresholds bound the io.ReadAll buffer: we only slurp the whole file
// when it is large enough to benefit and small enough not to turn the fast
// path into a second, unbounded in-memory copy of a huge tracker export.
const (
	// parallelParseMinBytes is the file-size floor for attempting the parallel
	// path, set from the MEASURED crossover. The JSONL parse is dominated by
	// allocation/GC (per-issue decode + Validate), not raw CPU, so concurrency
	// only pays once the per-issue work outweighs the parallel path's extra
	// allocation (per-chunk slices + the order-preserving reassembly copy).
	// Measured on the project host (64c, Go 1.25.5), warm in-process:
	//   1.9MB  serial 13.4ms  vs parallel 15.3ms  (serial wins)
	//   4MB    serial 37.5ms  vs parallel 37.1ms  (crossover)
	//   8MB    serial 62.9ms  vs parallel 56.4ms  (parallel +10%)
	//   40MB   serial 246ms   vs parallel 203ms   (parallel +21%)
	// The repo's own ~1.9MB issues.jsonl therefore stays on the (faster) serial
	// path — no warm-path regression — while genuinely large stores (multi-MB
	// monorepo exports) get the parallel speedup. The threshold sits just below
	// the crossover so we never knowingly pick the slower path.
	parallelParseMinBytes = 4 * 1024 * 1024
	// parallelParseMaxBytes caps the extra whole-file allocation made solely for
	// parallel parsing. Larger files retain the streaming serial path, whose
	// memory use is bounded by the line buffer plus the materialized issues. The
	// measured large-file speedup extends through 40MB, so 128MB leaves ample
	// headroom without making arbitrary-size exports candidates for io.ReadAll.
	parallelParseMaxBytes = 128 * 1024 * 1024
	// parallelParseMinLines is the line-count floor; a few huge lines should
	// not trigger a parallel split that cannot actually distribute work.
	parallelParseMinLines = 512
	// parallelParseMinChunkBytes is the smallest chunk we will create. Chunks
	// smaller than this are dominated by per-chunk fixed costs (goroutine
	// hand-off, slice pre-sizing, result reassembly), so we never subdivide
	// below it even when there are many idle cores.
	parallelParseMinChunkBytes = 64 * 1024
	// parallelParseChunksPerWorker controls oversubscription: aiming for a few
	// chunks per worker keeps the morsel pool balanced (a worker that draws an
	// expensive chunk does not stall the whole pass) while bounding scheduling
	// overhead. The actual chunk size is derived from the file size so the
	// available cores are actually used instead of being starved by a fixed,
	// too-large chunk target.
	parallelParseChunksPerWorker = 3
)

// readParallelCandidateBounded reads at most maxBytes+1 bytes from the file's
// current offset. When the file grew past the stat-based eligibility check, it
// restores the original offset and tells the caller to retain the streaming
// path. The reset preserves ParseIssues' existing behavior for callers that
// intentionally pass an os.File positioned somewhere other than byte zero.
func readParallelCandidateBounded(f *os.File, maxBytes int64) ([]byte, bool, error) {
	if f == nil || maxBytes < 0 {
		return nil, false, nil
	}
	start, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, false, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) <= maxBytes {
		return data, true, nil
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("restore file offset after parallel-read limit: %w", err)
	}
	return nil, false, nil
}

// estimateIssueCap mirrors the serial pre-sizing heuristic: average issue line
// ~2KB, conservatively under-estimated, clamped to [64, 200k].
func estimateIssueCap(size int64) int {
	const avgIssueBytes = 2 * 1024
	const minCap = 64
	const maxCap = 200_000

	est := int(size / avgIssueBytes)
	if est < minCap && size > 0 {
		est = minCap
	}
	if est > maxCap {
		est = maxCap
	}
	return est
}

// resolveWarnHandler returns the effective warning sink: the caller's handler,
// or the default stderr printer (suppressed under BV_ROBOT=1).
func resolveWarnHandler(h func(string), warningCount *int) func(string) {
	sink := h
	if sink == nil {
		if env.Robot.Bool() {
			sink = func(string) {}
		} else {
			sink = func(msg string) {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
			}
		}
	}
	return func(msg string) {
		if warningCount != nil {
			*warningCount = *warningCount + 1
		}
		sink(msg)
	}
}

// countLines counts newline-delimited records in data, matching the number of
// lineNum values the serial reader would assign (a trailing partial line with
// no newline still counts as one line).
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++ // trailing line without a terminating newline
	}
	return n
}

func effectiveMaxCapacity(requested int) int {
	if requested <= 0 {
		return DefaultMaxBufferSize
	}
	// bufio.Reader clamps smaller buffers to its internal minimum. Use the same
	// effective cap in the parallel path so boundary behavior matches.
	if requested < 16 {
		return 16
	}
	return requested
}

type parsedLineEvent struct {
	lineNum  int
	stats    ParseStats
	warns    []string
	hasIssue bool
}

// chunkResult holds one chunk's decoded output in original intra-chunk order,
// plus its ordered per-line events. Each worker owns its result
// exclusively (no shared mutable state), so there are no data races.
type chunkResult struct {
	issues   []model.Issue
	poolRefs []*model.Issue
	events   []parsedLineEvent
}

// parseIssuesParallel decodes a whole JSONL buffer concurrently while remaining
// byte-equivalent to the serial parser. It splits data into line-aligned chunks
// at newline boundaries, decodes each chunk on a bounded worker pool via the
// shared processIssueLine, then reassembles issues/poolRefs in original order
// (chunk index, then intra-chunk index) and replays warnings in global line
// order. BOM is stripped from the first line of the first chunk only; the 10MB
// per-line cap, _type filtering, tombstone/normalize/validate semantics, and
// ParseStats accounting all match the serial path exactly.
func parseIssuesParallel(data []byte, opts ParseOptions, usePool bool, maxCapacity int) ([]model.Issue, []*model.Issue, error) {
	maxCapacity = effectiveMaxCapacity(maxCapacity)
	warn := resolveWarnHandler(opts.WarningHandler, opts.WarningCount)
	decodeOpts := opts
	decodeOpts.IssueFilter = nil
	decodeOpts.Stats = nil

	// Build line-aligned chunk boundaries. Each chunk is [start,end) over data,
	// ending exactly after a '\n' (except possibly the last). We also record the
	// 1-based starting line number for each chunk so per-line warnings keep the
	// serial lineNum semantics.
	type chunkSpan struct {
		start, end int
		startLine  int // 1-based line number of the first line in this chunk
	}
	// Pick the worker count first, then derive a chunk size that actually
	// spreads the file across the available cores (a fixed, large chunk target
	// starves cores on mid-sized files). We aim for a few chunks per worker for
	// load balance, but never go below parallelParseMinChunkBytes.
	maxWorkers := runtime.GOMAXPROCS(0)
	if n := runtime.NumCPU(); n < maxWorkers {
		maxWorkers = n
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	targetChunk := len(data) / (maxWorkers * parallelParseChunksPerWorker)
	if targetChunk < parallelParseMinChunkBytes {
		targetChunk = parallelParseMinChunkBytes
	}

	var spans []chunkSpan
	pos := 0
	line := 1
	for pos < len(data) {
		end := pos + targetChunk
		if end >= len(data) {
			end = len(data)
		} else {
			// Extend to the next newline so chunks never split a JSON object.
			nl := bytes.IndexByte(data[end:], '\n')
			if nl < 0 {
				end = len(data)
			} else {
				end = end + nl + 1 // include the newline in this chunk
			}
		}
		spans = append(spans, chunkSpan{start: pos, end: end, startLine: line})
		// Count lines consumed by this chunk to seed the next chunk's startLine.
		line += countLines(data[pos:end])
		pos = end
	}

	results := make([]chunkResult, len(spans))

	// Bound concurrency to the usable CPUs, capped by the number of chunks
	// (no point spawning more workers than there is work to pull).
	workers := maxWorkers
	if workers > len(spans) {
		workers = len(spans)
	}

	// Central dispatcher: workers pull chunk indices off a buffered channel
	// (morsel-driven). Per-chunk pre-sizing keeps peak memory bounded — we never
	// materialize more than the per-chunk decoded issues plus the final slices.
	idxCh := make(chan int, len(spans))
	for i := range spans {
		idxCh <- i
	}
	close(idxCh)

	// worker pulls chunk indices off idxCh until it is drained and decodes each
	// chunk into its own results[ci] slot. It captures no loop variable: ci is a
	// fresh per-iteration range variable and the slots are disjoint, so there is
	// no shared mutable state (verified under `go test -race`). Defined once
	// outside the spawn loop so each goroutine shares this single closure.
	var wg sync.WaitGroup
	worker := func() {
		defer wg.Done()
		for ci := range idxCh {
			span := spans[ci]
			res := &results[ci]
			// Pre-size to the chunk's byte span (avg ~2KB/issue).
			if est := estimateIssueCap(int64(span.end - span.start)); est > 0 {
				res.issues = make([]model.Issue, 0, est)
				if usePool {
					res.poolRefs = make([]*model.Issue, 0, est)
				}
			}
			parseChunkLines(data[span.start:span.end], span.startLine, ci == 0, decodeOpts, usePool, maxCapacity, res)
		}
	}
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go worker()
	}
	wg.Wait()

	// Reassemble in original order and replay warnings in global line order.
	total := 0
	totalRefs := 0
	for i := range results {
		total += len(results[i].issues)
		totalRefs += len(results[i].poolRefs)
	}

	var issues []model.Issue
	if total > 0 {
		issues = make([]model.Issue, 0, total)
	}
	var poolRefs []*model.Issue
	if usePool && totalRefs > 0 {
		poolRefs = make([]*model.Issue, 0, totalRefs)
	}
	for i := range results {
		issues = append(issues, results[i].issues...)
		if usePool {
			poolRefs = append(poolRefs, results[i].poolRefs...)
		}
	}
	decodedEvents := 0
	for i := range results {
		for _, event := range results[i].events {
			if event.hasIssue {
				decodedEvents++
			}
		}
	}
	if decodedEvents != len(issues) || (usePool && len(poolRefs) != len(issues)) {
		// Validate the worker result before policy replay starts returning
		// rejected objects to the pool. That keeps this defensive failure path
		// from ever double-returning a reference after in-place compaction.
		if usePool {
			ReturnIssuePtrsToPool(poolRefs)
		}
		return nil, nil, fmt.Errorf(
			"internal loader error: decoded %d issues, %d issue events, and %d pooled references",
			len(issues), decodedEvents, len(poolRefs),
		)
	}

	seenIDs := make(map[string]struct{}, len(issues))
	keptIssues := issues[:0]
	var keptRefs []*model.Issue
	if usePool {
		keptRefs = poolRefs[:0]
	}
	issueCursor := 0
	for i := range results {
		for _, event := range results[i].events {
			if opts.Stats != nil {
				opts.Stats.Valid += event.stats.Valid
				opts.Stats.Errors += event.stats.Errors
				opts.Stats.Skipped += event.stats.Skipped
			}
			for _, message := range event.warns {
				warn(message)
			}
			if !event.hasIssue {
				continue
			}
			ref := pooledRefAt(poolRefs, issueCursor)
			if keepParsedIssue(&issues[issueCursor], ref, event.lineNum, opts, seenIDs, warn) {
				keptIssues = append(keptIssues, issues[issueCursor])
				if usePool {
					keptRefs = append(keptRefs, ref)
				}
			}
			issueCursor++
		}
	}
	clear(issues[len(keptIssues):])
	issues = keptIssues
	if usePool {
		clear(poolRefs[len(keptRefs):])
		poolRefs = keptRefs
	}
	issues, poolRefs = normalizeEmptyIssueResults(issues, poolRefs)

	internRepeatedIssueStrings(issues, poolRefs)
	return issues, poolRefs, nil
}

// parseChunkLines decodes one line-aligned chunk into res. It replicates the
// serial reader's per-line treatment: it splits on '\n', trims a trailing '\r'
// (bufio.Reader.ReadLine drops the CR of a CRLF), skips empty lines without
// consuming logic, strips the BOM from the very first line when isFirstChunk,
// enforces the per-line byte cap (lines longer than maxCapacity are skipped
// with the identical "line too long" warning and consume exactly one lineNum),
// and otherwise defers to processIssueLine. Per-line stats and warnings are
// buffered for source-order replay by the caller before its filter callback.
func parseChunkLines(chunk []byte, startLine int, isFirstChunk bool, opts ParseOptions, usePool bool, maxCapacity int, res *chunkResult) {
	lineNum := startLine - 1
	for len(chunk) > 0 {
		lineNum++
		nl := bytes.IndexByte(chunk, '\n')
		var line []byte
		hasNewline := nl >= 0
		if nl < 0 {
			line = chunk
			chunk = nil
		} else {
			line = chunk[:nl]
			chunk = chunk[nl+1:]
		}
		// Per-line byte cap. The serial path uses bufio.Reader.ReadLine, which
		// sets isPrefix (→ skip) when bytes before '\n' fill the buffer. Check the
		// raw line before trimming CR so CRLF consumes the same capacity as serial.
		if len(line) >= maxCapacity {
			message := fmt.Sprintf("skipping line %d: line too long (exceeds %d bytes)", lineNum, maxCapacity)
			// A dropped record is an error, matching the serial path (#190).
			res.events = append(res.events, parsedLineEvent{lineNum: lineNum, stats: ParseStats{Errors: 1}, warns: []string{message}})
			continue
		}

		// bufio.Reader.ReadLine strips CR only when it is immediately before a
		// consumed newline. A bare CR on the final unterminated line is content.
		if n := len(line); hasNewline && n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}

		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		if isFirstChunk && lineNum == 1 {
			line = stripBOM(line)
		}

		before := len(res.issues)
		var lineStats ParseStats
		var lineWarns []string
		res.issues, res.poolRefs = processIssueLine(
			line, lineNum, opts, usePool,
			res.issues, res.poolRefs, &lineStats,
			func(msg string) { lineWarns = append(lineWarns, msg) },
		)
		res.events = append(res.events, parsedLineEvent{
			lineNum:  lineNum,
			stats:    lineStats,
			warns:    lineWarns,
			hasIssue: len(res.issues) != before,
		})
	}
}

// stripBOM removes the UTF-8 Byte Order Mark if present
func stripBOM(b []byte) []byte {
	if bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}) {
		return b[3:]
	}
	return b
}

// recordType identifies the kind of record a beads JSONL line carries.
// `bd export` writes mixed records — issues by default plus memories,
// sprints, forecasts, etc. — and tags each line with a `_type` field
// (absent on the historical issue-only shape). Dispatching on this
// before unmarshalling lets the loader stop warning on every memory
// record (issue #145).
type recordType int

const (
	// recordTypeIssue is the default when `_type` is missing or "issue".
	recordTypeIssue recordType = iota
	recordTypeMemory
	recordTypeSprint
	recordTypeForecast
	recordTypeBurndown
	// recordTypeIgnore catches records the viewer currently has no use
	// for but that are valid beads output (e.g. `_type:"epic_link"`
	// in some forks); they should be skipped silently rather than
	// emit a malformed-JSON warning.
	recordTypeIgnore
	recordTypeUnknown
)

// recordTypeOf returns the record kind for a JSONL line by parsing
// only the `_type` field. Returns recordTypeIssue when `_type` is
// missing (the historical shape) or set to "issue".
func recordTypeOf(line []byte) recordType {
	// Fast path: most production lines are pre-v1.0-style issues with
	// no `_type` field at all. A bytes.Contains check avoids a JSON
	// decode for the common case.
	if !bytes.Contains(line, []byte(`"_type"`)) {
		return recordTypeIssue
	}
	var probe struct {
		Type string `json:"_type"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		// Couldn't even parse the discriminator — fall through to
		// recordTypeIssue so the regular issue parser produces the
		// usual "skipping malformed JSON" warning at the existing
		// site, instead of being silently swallowed here.
		return recordTypeIssue
	}
	switch probe.Type {
	case "", "issue":
		return recordTypeIssue
	case "memory":
		return recordTypeMemory
	case "sprint":
		return recordTypeSprint
	case "forecast":
		return recordTypeForecast
	case "burndown":
		return recordTypeBurndown
	default:
		return recordTypeUnknown
	}
}

func normalizeIssueStatus(status model.Status) model.Status {
	trimmed := strings.TrimSpace(string(status))
	if trimmed == "" {
		return ""
	}
	return model.Status(strings.ToLower(trimmed))
}

func normalizeLoadedIssue(issue *model.Issue) {
	issue.Status = normalizeIssueStatus(issue.Status)
	for _, dep := range issue.Dependencies {
		if dep == nil {
			continue
		}
		if dep.IssueID == "" {
			dep.IssueID = issue.ID
		}
	}
}

const (
	issueStringInternerSlots     = 128
	issueStringInternerMaxProbes = 8
)

// issueStringInterner is a bounded, stack-friendly table for the low-cardinality
// strings repeated across issues. A fixed table avoids both a process-global
// retention leak and a fresh map allocation on every reload.
type issueStringInterner struct {
	slots [issueStringInternerSlots]string
}

func (in *issueStringInterner) intern(value string) string {
	if value == "" {
		return ""
	}

	const fnvOffset64 = uint64(14695981039346656037)
	const fnvPrime64 = uint64(1099511628211)
	hash := fnvOffset64
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= fnvPrime64
	}

	start := int(hash & (issueStringInternerSlots - 1))
	for probe := 0; probe < issueStringInternerMaxProbes; probe++ {
		index := (start + probe) & (issueStringInternerSlots - 1)
		canonical := in.slots[index]
		if canonical == value {
			return canonical
		}
		if canonical == "" {
			in.slots[index] = value
			return value
		}
	}

	// This hash cluster exceeded the fixed probe budget. Preserve correctness and
	// skip interning this value: high-cardinality input must not turn every later
	// string into a full-table scan.
	return value
}

// internRepeatedIssueStrings shares immutable string storage within one parsed
// snapshot. The table is deliberately parse-scoped: labels, assignees, repo
// names, enums, and dependency targets repeat heavily, while a process-global
// interner would retain arbitrary user input forever.
func internRepeatedIssueStrings(issues []model.Issue, poolRefs []*model.Issue) {
	if len(issues) == 0 {
		return
	}

	var interner issueStringInterner
	for i := range issues {
		issue := &issues[i]
		issue.Status = model.Status(interner.intern(string(issue.Status)))
		issue.IssueType = model.IssueType(interner.intern(string(issue.IssueType)))
		issue.Assignee = interner.intern(issue.Assignee)
		issue.SourceRepo = interner.intern(issue.SourceRepo)
		for labelIndex := range issue.Labels {
			issue.Labels[labelIndex] = interner.intern(issue.Labels[labelIndex])
		}

		if i < len(poolRefs) && poolRefs[i] != nil {
			ref := poolRefs[i]
			ref.Status = issue.Status
			ref.IssueType = issue.IssueType
			ref.Assignee = issue.Assignee
			ref.SourceRepo = issue.SourceRepo
			for labelIndex := range ref.Labels {
				ref.Labels[labelIndex] = issue.Labels[labelIndex]
			}
		}
	}

	for i := range issues {
		issue := &issues[i]
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			dep.Type = model.DependencyType(interner.intern(string(dep.Type)))
			dep.CreatedBy = interner.intern(dep.CreatedBy)
		}
		for _, comment := range issue.Comments {
			if comment == nil {
				continue
			}
			comment.IssueID = interner.intern(comment.IssueID)
			comment.Author = interner.intern(comment.Author)
		}
	}
}
