package loader

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"golang.org/x/sync/singleflight"
)

// GitLoader loads beads from git history
type GitLoader struct {
	repoPath string
	repoRoot string
	beadsDir string
	cache    *revisionCache
	flight   singleflight.Group
}

// revisionCache caches loaded issues by their resolved commit SHA
type revisionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	maxAge  time.Duration
	// now is optional and must remain immutable after cache use begins. A
	// supplied callback must be safe for concurrent reads; nil uses time.Now.
	now func() time.Time
}

// cacheEntry holds cached issues with metadata
type cacheEntry struct {
	issues       []model.Issue
	tombstoneIDs []string
	parseStats   ParseStats
	warnings     []string
	sourcePath   string
	loadedAt     time.Time
	commitSHA    string
	commitTime   time.Time
}

// GitLoadReport is an immutable-by-convention snapshot of one historical
// JSONL authority. LoadAtWithReport and its cache return caller-owned slices.
type GitLoadReport struct {
	Issues       []model.Issue
	TombstoneIDs []string // Valid deleted records retained as dependency authority.
	ParseStats   ParseStats
	Warnings     []string
	SourcePath   string
	CommitSHA    string
	CommitTime   time.Time
}

const maxGitLoadWarnings = 10

func (g *GitLoader) defaultRepoRoot() string {
	if g.repoRoot != "" {
		return g.repoRoot
	}
	return resolveGitLoaderRoot(g.repoPath)
}

func (g *GitLoader) gitCommandInDir(dir string, args ...string) *exec.Cmd {
	gitArgs := make([]string, 0, len(args)+1)
	gitArgs = append(gitArgs, "--no-replace-objects")
	gitArgs = append(gitArgs, args...)
	cmd := exec.Command("git", gitArgs...)
	if dir == "" {
		dir = g.defaultRepoRoot()
	}
	cmd.Dir = dir
	cmd.Env = gitLoaderEnvironment()
	return cmd
}

// gitCommand returns a read-only Git subprocess pinned to this loader's
// repository (or the explicitly configured BEADS_DB/BEADS_DIR/redirect target's
// repository). Ambient Git routing/configuration variables commonly leak from
// hooks and parent Git commands; retaining them would let a historical load
// resolve a revision in one object database while g.repoPath names another.
// Replacement refs are disabled because they can substitute different commit
// trees without changing the requested revision string or cache key.
func (g *GitLoader) gitCommand(args ...string) *exec.Cmd {
	if target, err := g.resolveTarget(); err == nil && target.repoRoot != "" {
		return g.gitCommandInDir(target.repoRoot, args...)
	}
	return g.gitCommandInDir(g.defaultRepoRoot(), args...)
}

func resolveGitLoaderRoot(repoPath string) string {
	target := repoPath
	if strings.TrimSpace(target) == "" {
		if wd, err := os.Getwd(); err == nil {
			target = wd
		}
	}
	if target == "" {
		return repoPath
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return repoPath
	}
	if root, err := GetGitWorktreeRoot(target); err == nil && root != "" {
		return root
	}
	if abs, err := filepath.Abs(target); err == nil {
		return filepath.Clean(abs)
	}
	return repoPath
}

// SanitizedGitEnv returns the process environment stripped of ambient Git
// repository/config routing overrides (GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR,
// GIT_CONFIG*, etc.) with LC_ALL=C pinned so Git subprocesses stay anchored to
// their explicit working directory.
func SanitizedGitEnv() []string {
	return gitLoaderEnvironment()
}

func gitLoaderEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		normalizedName := strings.ToUpper(name)
		if gitLoaderEnvironmentOverridesAuthority(normalizedName) || normalizedName == "LC_ALL" || normalizedName == "LANG" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "LC_ALL=C")
}

func gitLoaderEnvironmentOverridesAuthority(name string) bool {
	name = strings.ToUpper(name)
	switch name {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH",
		"GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS", "GIT_NAMESPACE",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_PREFIX", "GIT_INTERNAL_SUPER_PREFIX", "GIT_IMPLICIT_WORK_TREE",
		"GIT_ATTR_SOURCE", "GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS",
		"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_LITERAL_PATHSPECS",
		"GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS":
		return true
	default:
		return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
	}
}

// NewGitLoader creates a new git history loader for the given repo
func NewGitLoader(repoPath string) *GitLoader {
	return &GitLoader{
		repoPath: repoPath,
		repoRoot: resolveGitLoaderRoot(repoPath),
		cache: &revisionCache{
			entries: make(map[string]cacheEntry),
			maxAge:  5 * time.Minute,
		},
	}
}

// NewGitLoaderWithBeadsDir creates a git history loader pinned to a specific
// beads directory (for example a shared-.git monorepo sub-project such as
// services/api/.beads), bypassing ambient BEADS_DIR / BEADS_DB discovery.
// When beadsDir is empty, it behaves identically to NewGitLoader(repoPath).
func NewGitLoaderWithBeadsDir(repoPath, beadsDir string) *GitLoader {
	g := NewGitLoader(repoPath)
	trimmed := strings.TrimSpace(beadsDir)
	if trimmed == "" {
		return g
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		base := strings.TrimSpace(repoPath)
		if base == "" {
			base = g.repoRoot
		}
		if base != "" {
			candidate = filepath.Join(base, candidate)
		}
	}
	if g.repoRoot != "" {
		if rel, ok := repoRelativeSlashDir(g.repoRoot, candidate); ok {
			g.beadsDir = rel
			return g
		}
	}
	g.beadsDir = canonicalLocalPath(candidate)
	return g
}

// NewGitLoaderWithCacheTTL creates a loader with custom cache TTL
func NewGitLoaderWithCacheTTL(repoPath string, cacheTTL time.Duration) *GitLoader {
	return &GitLoader{
		repoPath: repoPath,
		repoRoot: resolveGitLoaderRoot(repoPath),
		cache: &revisionCache{
			entries: make(map[string]cacheEntry),
			maxAge:  cacheTTL,
		},
	}
}

// LoadAt loads issues from a specific git revision
// revision can be: SHA, branch name, tag name, HEAD~N, or date expression
func (g *GitLoader) LoadAt(revision string) ([]model.Issue, error) {
	report, err := g.LoadAtWithReport(revision)
	return report.Issues, err
}

// LoadAtWithStats loads issues from a specific Git revision and returns the
// same per-record accounting as a live JSONL load. Claim-emitting callers need
// the stats to distinguish a genuinely absent historical issue from one that
// the tolerant parser dropped.
func (g *GitLoader) LoadAtWithStats(revision string) ([]model.Issue, ParseStats, error) {
	report, err := g.LoadAtWithReport(revision)
	return report.Issues, report.ParseStats, err
}

// LoadAtWithReport loads a complete historical authority report. Parse
// accounting and bounded warnings are cached with the issues so cache hits
// cannot erase evidence that the tolerant JSONL parser dropped records.
func (g *GitLoader) LoadAtWithReport(revision string) (GitLoadReport, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return GitLoadReport{}, err
	}

	// Resolve to commit SHA for caching
	sha, err := g.resolveRevisionInDir(target.repoRoot, revision)
	if err != nil {
		return GitLoadReport{}, fmt.Errorf("resolving revision %q: %w", revision, err)
	}

	cacheKey := g.cacheKeyForTarget(sha, target)

	// Check cache
	if report, ok := g.cache.getReport(cacheKey); ok {
		return report, nil
	}

	// Coalesce concurrent misses for the same immutable commit. Historical
	// callers (notably multiple robot views initialized together) otherwise run
	// the same Git subprocesses and parse the same JSONL independently. Recheck
	// the cache inside the flight because another caller may have populated it
	// between the optimistic lookup above and flight admission.
	loaded, err, _ := g.flight.Do(cacheKey, func() (any, error) {
		if report, ok := g.cache.getReport(cacheKey); ok {
			return report, nil
		}

		report, err := g.loadFromGitTargetWithReport(target, sha)
		if err != nil {
			return report, err
		}
		commitTime, err := g.resolveCommitTimeInDir(target.repoRoot, sha)
		if err != nil {
			return GitLoadReport{}, err
		}
		report.CommitTime = commitTime
		g.cache.setReportForKey(cacheKey, report)
		return report, nil
	})
	if err != nil {
		if report, ok := loaded.(GitLoadReport); ok {
			return cloneGitLoadReport(report), err
		}
		return GitLoadReport{}, err
	}
	report, ok := loaded.(GitLoadReport)
	if !ok {
		return GitLoadReport{}, fmt.Errorf("loading historical report for %s: unexpected shared result type %T", sha, loaded)
	}
	// A singleflight result is shared by all waiters. Preserve the public
	// caller-owned-slice contract by cloning after leaving the flight.
	return cloneGitLoadReport(report), nil
}

// LoadAtDate loads issues from the state at a specific date/time
// Uses git rev-list to find the commit at or before the given time
func (g *GitLoader) LoadAtDate(t time.Time) ([]model.Issue, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return nil, err
	}
	sha, err := g.resolveDateRevisionInDir(target.repoRoot, t)
	if err != nil {
		return nil, err
	}
	return g.LoadAt(sha)
}

// ResolveRevision resolves any git revision to its commit SHA
func (g *GitLoader) ResolveRevision(revision string) (string, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return "", err
	}
	return g.resolveRevisionInDir(target.repoRoot, revision)
}

func (g *GitLoader) resolveCommitTime(sha string) (time.Time, error) {
	return g.resolveCommitTimeInDir(g.defaultRepoRoot(), sha)
}

func (g *GitLoader) resolveCommitTimeInDir(repoRoot, sha string) (time.Time, error) {
	// sha is the already-resolved object ID, so it cannot inject options. Use
	// the committer timestamp because it identifies when this exact repository
	// snapshot became history.
	cmd := g.gitCommandInDir(repoRoot, "show", "-s", "--format=%cI", sha)
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("reading commit time for %s: %w", sha, err)
	}
	value := strings.TrimSpace(string(out))
	commitTime, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing commit time %q for %s: %w", value, sha, err)
	}
	return commitTime, nil
}

// ListRevisions returns commits that modified beads files
func (g *GitLoader) ListRevisions(limit int) ([]RevisionInfo, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return nil, err
	}
	args := []string{
		"log",
		"--format=%H|%aI|%s",
		"--",
	}
	args = append(args, target.gitPaths(nil)...)
	if limit > 0 {
		// Insert -n limit after "log"
		args = append([]string{"log", fmt.Sprintf("-n%d", limit)}, args[1:]...)
	}

	cmd := g.gitCommandInDir(target.repoRoot, args...)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing git history: %w", err)
	}

	var revisions []RevisionInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue // skip revisions with unparseable timestamps
		}
		revisions = append(revisions, RevisionInfo{
			SHA:       parts[0],
			Timestamp: timestamp,
			Message:   parts[2],
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parsing git log output: %w", err)
	}

	return revisions, nil
}

// RevisionInfo describes a git commit
type RevisionInfo struct {
	SHA       string    `json:"sha"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
}

// resolveRevision converts any revision specifier to a commit SHA
func (g *GitLoader) resolveRevision(revision string) (string, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return "", err
	}
	return g.resolveRevisionInDir(target.repoRoot, revision)
}

func (g *GitLoader) resolveRevisionInDir(repoRoot, revision string) (string, error) {
	// Peel the revision to a commit: later historical-load steps require a tree
	// and a committer timestamp, so accepting an arbitrary blob/tree/tag object
	// here only defers the error and can put a non-commit object into the cache
	// identity. Annotated tags resolve to the commit they name.
	// Use --end-of-options to prevent argument injection (e.g. revision starting with -)
	cmd := g.gitCommandInDir(repoRoot, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")

	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	// If rev-parse failed, try to interpret the revision as a date.
	if t, ok := parseDateString(revision); ok {
		return g.resolveDateRevisionInDir(repoRoot, t)
	}

	return "", fmt.Errorf("git rev-parse failed: %w", err)
}

func (g *GitLoader) resolveDateRevision(t time.Time) (string, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return "", err
	}
	return g.resolveDateRevisionInDir(target.repoRoot, t)
}

func (g *GitLoader) resolveDateRevisionInDir(repoRoot string, t time.Time) (string, error) {
	cmd := g.gitCommandInDir(repoRoot, "rev-list", "-1", fmt.Sprintf("--before=%s", t.Format(time.RFC3339)), "HEAD")

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list before %s failed: %w", t.Format(time.RFC3339), err)
	}

	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("no commit found at or before %s", t.Format(time.RFC3339))
	}

	return sha, nil
}

// parseDateString attempts to parse common date/time formats used by users.
// Returns the parsed time and true on success.
func parseDateString(s string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02",
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
	}

	for _, layout := range layouts {
		switch layout {
		case time.RFC3339:
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		default:
			// For layouts without zone information, assume local time to match git's
			// interpretation of HEAD@{<date>} which is evaluated in local time.
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return t, true
			}
		}
	}

	return time.Time{}, false
}

// loadFromGit loads issues from a specific commit SHA
func (g *GitLoader) loadFromGit(sha string) ([]model.Issue, error) {
	issues, _, err := g.loadFromGitWithStats(sha)
	return issues, err
}

func (g *GitLoader) loadFromGitWithStats(sha string) ([]model.Issue, ParseStats, error) {
	report, err := g.loadFromGitWithReport(sha)
	return report.Issues, report.ParseStats, err
}

func canonicalLocalPath(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	clean := filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	// Resolve symlinks on the deepest existing ancestor when leaf components
	// only exist in historical commits (e.g. symlinked temp/parent roots).
	cur := clean
	var suffix []string
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
	}
}

func repoRelativeSlashDir(repoRoot, targetDir string) (string, bool) {
	rootClean := canonicalLocalPath(repoRoot)
	dirClean := canonicalLocalPath(targetDir)
	if rootClean == "" || dirClean == "" {
		return "", false
	}
	rel, err := filepath.Rel(rootClean, dirClean)
	if err != nil || rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	return strings.Trim(filepath.ToSlash(rel), "/"), true
}

func joinRelSlash(dir, filename string) string {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	if dir == "" || dir == "." {
		return filename
	}
	return dir + "/" + filename
}

type gitLoaderTarget struct {
	repoRoot  string
	dirs      []string
	fileNames []string
	explicit  bool
}

func (t gitLoaderTarget) gitPaths(names []string) []string {
	if len(names) == 0 {
		names = t.fileNames
	}
	if len(names) == 0 {
		names = PreferredJSONLNames
	}
	paths := make([]string, 0, len(t.dirs)*len(names))
	for _, dir := range t.dirs {
		for _, name := range names {
			paths = append(paths, joinRelSlash(dir, name))
		}
	}
	return paths
}

func escapedTempBoundary(targetPath, gitRoot string) bool {
	tempRoot := canonicalLocalPath(os.TempDir())
	if tempRoot == "" {
		return false
	}
	relTarget, targetInTemp := repoRelativeSlashDir(tempRoot, targetPath)
	if !targetInTemp || relTarget == "." {
		return false
	}
	_, rootInTemp := repoRelativeSlashDir(tempRoot, gitRoot)
	return !rootInTemp
}

func resolveExplicitTargetGitRoot(beadsDir string) (string, error) {
	trimmed := strings.TrimSpace(beadsDir)
	if trimmed == "" {
		return "", fmt.Errorf("configured beads directory is empty")
	}
	absTarget, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolving beads directory %q: %w", beadsDir, err)
	}
	probe := filepath.Clean(absTarget)
	if info, statErr := os.Stat(probe); statErr == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("configured beads path %s is not a directory", beadsDir)
		}
	} else if os.IsNotExist(statErr) {
		for {
			parent := filepath.Dir(probe)
			if parent == probe {
				return "", fmt.Errorf("configured beads directory %s is not in a git repository", beadsDir)
			}
			probe = parent
			info, pErr := os.Stat(probe)
			if pErr == nil {
				if !info.IsDir() {
					return "", fmt.Errorf("configured beads ancestor %s is not a directory", probe)
				}
				break
			}
			if !os.IsNotExist(pErr) {
				return "", fmt.Errorf("inspecting beads directory %s: %w", probe, pErr)
			}
		}
	} else {
		return "", fmt.Errorf("inspecting beads directory %s: %w", beadsDir, statErr)
	}

	gitRoot, err := GetGitWorktreeRoot(probe)
	if err != nil {
		return "", fmt.Errorf("configured beads directory %s is not in a git repository: %w", beadsDir, err)
	}
	if escapedTempBoundary(absTarget, gitRoot) {
		return "", fmt.Errorf("configured beads directory %s is not in a git repository", beadsDir)
	}
	return gitRoot, nil
}

func buildExplicitGitTarget(beadsDir string, fileNames []string) (gitLoaderTarget, error) {
	gitRoot, err := resolveExplicitTargetGitRoot(beadsDir)
	if err != nil {
		return gitLoaderTarget{}, err
	}
	rel, ok := repoRelativeSlashDir(gitRoot, beadsDir)
	if !ok {
		return gitLoaderTarget{}, fmt.Errorf("configured beads directory %s is outside git repository %s", beadsDir, gitRoot)
	}
	if len(fileNames) == 0 {
		fileNames = PreferredJSONLNames
	}
	return gitLoaderTarget{
		repoRoot:  gitRoot,
		dirs:      []string{rel},
		fileNames: fileNames,
		explicit:  true,
	}, nil
}

func (g *GitLoader) discoverRepoBeadsDirWithTrace() (string, []string, error) {
	repoPath := g.repoPath
	if strings.TrimSpace(repoPath) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", nil, fmt.Errorf("failed to get current working directory: %w", err)
		}
		repoPath = wd
	}
	info, err := os.Stat(repoPath)
	if err != nil || !info.IsDir() {
		return filepath.Join(repoPath, ".beads"), nil, nil
	}

	beadsDir := filepath.Join(repoPath, ".beads")
	if _, statErr := os.Stat(beadsDir); statErr == nil {
		return resolveBeadsRedirect(beadsDir)
	} else if !os.IsNotExist(statErr) {
		return "", nil, fmt.Errorf("inspect beads directory %s: %w", beadsDir, statErr)
	}

	worktreeRoot := g.defaultRepoRoot()
	_, hasDotGitErr := os.Stat(filepath.Join(worktreeRoot, ".git"))
	if hasDotGitErr == nil {
		curr := canonicalLocalPath(repoPath)
		rootClean := canonicalLocalPath(worktreeRoot)
		for curr != "" && curr != rootClean {
			parent := filepath.Dir(curr)
			if parent == curr {
				break
			}
			candidate := filepath.Join(parent, ".beads")
			if cInfo, cErr := os.Stat(candidate); cErr == nil && cInfo.IsDir() {
				return resolveBeadsRedirect(candidate)
			} else if cErr != nil && !os.IsNotExist(cErr) {
				return "", nil, fmt.Errorf("inspect beads directory %s: %w", candidate, cErr)
			}
			curr = parent
		}
	} else {
		if ancestorBeadsDir, ancErr := findAncestorBeadsDir(repoPath); ancErr != nil {
			return "", nil, ancErr
		} else if ancestorBeadsDir != "" {
			return resolveBeadsRedirect(ancestorBeadsDir)
		}
	}

	if mainRepoRoot, mainErr := getMainRepoRoot(repoPath); mainErr == nil && mainRepoRoot != "" && canonicalLocalPath(mainRepoRoot) != canonicalLocalPath(repoPath) {
		mainBeadsDir := filepath.Join(mainRepoRoot, ".beads")
		if _, mStatErr := os.Stat(mainBeadsDir); mStatErr == nil {
			return resolveBeadsRedirect(mainBeadsDir)
		} else if !os.IsNotExist(mStatErr) {
			return "", nil, fmt.Errorf("inspect main repository beads directory %s: %w", mainBeadsDir, mStatErr)
		}
	}

	return beadsDir, nil, nil
}

func (g *GitLoader) resolveTarget() (gitLoaderTarget, error) {
	if pinned := strings.TrimSpace(g.beadsDir); pinned != "" {
		targetDir := pinned
		if !filepath.IsAbs(targetDir) {
			if root := g.defaultRepoRoot(); root != "" {
				targetDir = filepath.Join(root, filepath.FromSlash(targetDir))
			} else if strings.TrimSpace(g.repoPath) != "" {
				targetDir = filepath.Join(g.repoPath, filepath.FromSlash(targetDir))
			}
		}
		resolvedDir, _, err := resolveBeadsRedirect(targetDir)
		if err != nil {
			return gitLoaderTarget{}, fmt.Errorf("resolving configured beads directory %q: %w", g.beadsDir, err)
		}
		return buildExplicitGitTarget(resolvedDir, PreferredJSONLNames)
	}

	if envDB := env.BeadsDB.Get(); envDB != "" {
		fileNames := PreferredJSONLNames
		if info, statErr := os.Stat(envDB); statErr == nil && !info.IsDir() {
			if strings.HasSuffix(strings.ToLower(envDB), ".jsonl") {
				fileNames = []string{filepath.Base(envDB)}
			}
		} else if statErr != nil && strings.HasSuffix(strings.ToLower(envDB), ".jsonl") {
			fileNames = []string{filepath.Base(envDB)}
		}
		beadsDir, _, err := resolveBeadsDBWithTrace(envDB)
		if err != nil {
			return gitLoaderTarget{}, fmt.Errorf("resolving BEADS_DB %q: %w", envDB, err)
		}
		return buildExplicitGitTarget(beadsDir, fileNames)
	}

	if envDir := env.BeadsDir.Get(); envDir != "" {
		beadsDir, _, err := resolveBeadsRedirect(envDir)
		if err != nil {
			return gitLoaderTarget{}, fmt.Errorf("resolving BEADS_DIR %q: %w", envDir, err)
		}
		return buildExplicitGitTarget(beadsDir, PreferredJSONLNames)
	}

	beadsDir, trace, err := g.discoverRepoBeadsDirWithTrace()
	if err != nil {
		return gitLoaderTarget{}, fmt.Errorf("resolving beads directory: %w", err)
	}
	if len(trace) > 1 {
		return buildExplicitGitTarget(beadsDir, PreferredJSONLNames)
	}

	root := g.defaultRepoRoot()
	var dirs []string
	seen := make(map[string]struct{}, 3)
	addDir := func(d string) {
		if d != "." {
			d = strings.Trim(filepath.ToSlash(d), "/")
		}
		if d == "" {
			return
		}
		if _, exists := seen[d]; !exists {
			seen[d] = struct{}{}
			dirs = append(dirs, d)
		}
	}

	if root != "" {
		if beadsDir != "" {
			if rel, ok := repoRelativeSlashDir(root, beadsDir); ok {
				addDir(rel)
			}
		}
		if strings.TrimSpace(g.repoPath) != "" {
			if relSub, ok := repoRelativeSlashDir(root, filepath.Join(g.repoPath, ".beads")); ok {
				addDir(relSub)
			}
		}
	}
	addDir(".beads")
	return gitLoaderTarget{
		repoRoot:  root,
		dirs:      dirs,
		fileNames: PreferredJSONLNames,
		explicit:  false,
	}, nil
}

func (g *GitLoader) candidateBeadsDirs() []string {
	target, err := g.resolveTarget()
	if err != nil {
		return nil
	}
	return target.dirs
}

func (g *GitLoader) candidateBeadsGitPaths(names []string) []string {
	target, err := g.resolveTarget()
	if err != nil {
		return nil
	}
	return target.gitPaths(names)
}

func (g *GitLoader) cacheKey(sha string) string {
	target, err := g.resolveTarget()
	if err != nil {
		return sha
	}
	return g.cacheKeyForTarget(sha, target)
}

func (g *GitLoader) cacheKeyForTarget(sha string, target gitLoaderTarget) string {
	if !target.explicit && len(target.dirs) == 1 && target.dirs[0] == ".beads" &&
		canonicalLocalPath(target.repoRoot) == canonicalLocalPath(g.defaultRepoRoot()) {
		return sha
	}
	return fmt.Sprintf("%s|%s|%s|%s",
		sha,
		canonicalLocalPath(target.repoRoot),
		strings.Join(target.dirs, ","),
		strings.Join(target.fileNames, ","),
	)
}

func (g *GitLoader) loadFromGitWithReport(sha string) (GitLoadReport, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return GitLoadReport{}, err
	}
	return g.loadFromGitTargetWithReport(target, sha)
}

func (g *GitLoader) loadFromGitTargetWithReport(target gitLoaderTarget, sha string) (GitLoadReport, error) {
	// Try known beads file paths in order, matching loader.go precedence
	paths := target.gitPaths(nil)

	for _, path := range paths {
		exists, err := g.historicalPathExistsInDir(target.repoRoot, sha, path)
		if err != nil {
			return GitLoadReport{}, fmt.Errorf("inspect historical beads path %s:%s: %w", sha, path, err)
		}
		if !exists {
			continue
		}

		report, err := g.loadFileFromGitInDirWithReport(target.repoRoot, sha, path)
		if err != nil {
			// Once the highest-precedence existing authority is selected, any read
			// or parse failure is fatal. Falling through to a lower-precedence legacy
			// file would turn corruption into a plausible but stale snapshot.
			return report, err
		}
		// A non-empty source made entirely of non-issue or invalid records is
		// not an empty historical project. Reject it here, before cache
		// insertion, so callers cannot mistake a partial/wrong authority for a
		// valid snapshot. A genuinely empty file remains valid.
		if report.ParseStats.Valid == 0 && report.ParseStats.Errors+report.ParseStats.Skipped > 0 {
			return report, fmt.Errorf("%s:%s: no issue records (%d non-issue/error lines, 0 valid issues)",
				sha, path, report.ParseStats.Errors+report.ParseStats.Skipped)
		}
		return report, nil
	}

	return GitLoadReport{}, fmt.Errorf("no beads file found at %s", sha)
}

func (g *GitLoader) historicalPathExists(sha, path string) (bool, error) {
	return g.historicalPathExistsInDir(g.defaultRepoRoot(), sha, path)
}

func (g *GitLoader) historicalPathExistsInDir(repoRoot, sha, path string) (bool, error) {
	// Unlike `git show <commit>:<path>`, ls-tree distinguishes absence (empty,
	// successful output) from repository/object failures (non-zero exit). This
	// lets precedence fallback happen only when a candidate path truly is absent.
	cmd := g.gitCommandInDir(repoRoot, "ls-tree", "--name-only", "--full-tree", "-z", sha, "--", path)
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(out) != 0, nil
}

// loadFileFromGit loads a specific file from git at a commit
func (g *GitLoader) loadFileFromGit(sha, path string) ([]model.Issue, error) {
	issues, _, err := g.loadFileFromGitWithStats(sha, path)
	return issues, err
}

func (g *GitLoader) loadFileFromGitWithStats(sha, path string) ([]model.Issue, ParseStats, error) {
	report, err := g.loadFileFromGitWithReport(sha, path)
	return report.Issues, report.ParseStats, err
}

func (g *GitLoader) loadFileFromGitWithReport(sha, path string) (GitLoadReport, error) {
	return g.loadFileFromGitInDirWithReport(g.defaultRepoRoot(), sha, path)
}

func (g *GitLoader) loadFileFromGitInDirWithReport(repoRoot, sha, path string) (GitLoadReport, error) {
	cmd := g.gitCommandInDir(repoRoot, "show", fmt.Sprintf("%s:%s", sha, path))

	out, err := cmd.Output()
	if err != nil {
		return GitLoadReport{}, fmt.Errorf("git show %s:%s failed: %w", sha, path, err)
	}

	var stats ParseStats
	var warnings []string
	defaultWarning := resolveWarnHandler(nil, nil)
	issues, err := ParseIssuesWithOptions(bytes.NewReader(out), ParseOptions{
		Stats: &stats,
		WarningHandler: func(message string) {
			if len(warnings) < maxGitLoadWarnings {
				warnings = append(warnings, message)
			}
			defaultWarning(message)
		},
	})
	if err != nil {
		return GitLoadReport{}, err
	}
	// Match the live datasource contract: tombstones are valid source records
	// (and therefore remain in ParseStats.Valid) but are not part of the visible
	// issue universe. Without this filter --as-of search/TUI paths can resurrect
	// soft-deleted issues that the same JSONL authority excludes when loaded live.
	visible := issues[:0]
	var tombstoneIDs []string
	for i := range issues {
		if issues[i].Status.IsTombstone() {
			tombstoneIDs = append(tombstoneIDs, issues[i].ID)
		} else {
			visible = append(visible, issues[i])
		}
	}
	clear(issues[len(visible):])
	return GitLoadReport{
		Issues:       visible,
		TombstoneIDs: tombstoneIDs,
		ParseStats:   stats,
		Warnings:     warnings,
		SourcePath:   path,
		CommitSHA:    sha,
	}, nil
}

// Cache methods

func (c *revisionCache) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *revisionCache) get(sha string) ([]model.Issue, bool) {
	report, ok := c.getReport(sha)
	return report.Issues, ok
}

func (c *revisionCache) getWithStats(sha string) ([]model.Issue, ParseStats, bool) {
	report, ok := c.getReport(sha)
	return report.Issues, report.ParseStats, ok
}

func (c *revisionCache) getReport(sha string) (GitLoadReport, bool) {
	c.mu.RLock()
	entry, ok := c.entries[sha]
	if !ok {
		c.mu.RUnlock()
		return GitLoadReport{}, false
	}

	// Check if entry is still valid. A future timestamp can arise after a wall-
	// clock correction; treating its negative age as fresh would extend the
	// entry's lifetime until the clock caught up and then for another full TTL.
	now := c.currentTime()
	if !revisionCacheEntryIsFresh(entry.loadedAt, now, c.maxAge) {
		c.mu.RUnlock()
		// Evict expired entry
		c.mu.Lock()
		// Re-check under write lock (another goroutine may have already evicted)
		if e, still := c.entries[sha]; still && !revisionCacheEntryIsFresh(e.loadedAt, c.currentTime(), c.maxAge) {
			delete(c.entries, sha)
		}
		c.mu.Unlock()
		return GitLoadReport{}, false
	}

	c.mu.RUnlock()

	// Cache entries are immutable after insertion, so deletion or replacement of
	// the map slot cannot mutate the copied entry's reachable data. Clone outside
	// the shared lock: a large historical report must not stall unrelated cache
	// hits merely while caller ownership is established.
	return gitLoadReportFromCacheEntry(entry), true
}

func revisionCacheEntryIsFresh(loadedAt, now time.Time, maxAge time.Duration) bool {
	return !loadedAt.IsZero() && !loadedAt.After(now) && now.Sub(loadedAt) <= maxAge
}

func (c *revisionCache) set(sha string, issues []model.Issue) {
	c.setWithStats(sha, issues, ParseStats{})
}

func (c *revisionCache) setWithStats(sha string, issues []model.Issue, stats ParseStats) {
	c.setReport(GitLoadReport{Issues: issues, ParseStats: stats, CommitSHA: sha})
}

func (c *revisionCache) setReport(report GitLoadReport) {
	c.setReportForKey(report.CommitSHA, report)
}

func (c *revisionCache) setReportForKey(key string, report GitLoadReport) {
	// Establish immutable cache ownership before taking the exclusive metadata
	// lock. Deep-cloning a large report does not need to block independent hits.
	stored := cloneGitLoadReport(report)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = cacheEntry{
		issues:       stored.Issues,
		tombstoneIDs: stored.TombstoneIDs,
		parseStats:   stored.ParseStats,
		warnings:     stored.Warnings,
		sourcePath:   stored.SourcePath,
		loadedAt:     c.currentTime(),
		commitSHA:    stored.CommitSHA,
		commitTime:   stored.CommitTime,
	}
}

func gitLoadReportFromCacheEntry(entry cacheEntry) GitLoadReport {
	return cloneGitLoadReport(GitLoadReport{
		Issues:       entry.issues,
		TombstoneIDs: entry.tombstoneIDs,
		ParseStats:   entry.parseStats,
		Warnings:     entry.warnings,
		SourcePath:   entry.sourcePath,
		CommitSHA:    entry.commitSHA,
		CommitTime:   entry.commitTime,
	})
}

func cloneGitLoadReport(report GitLoadReport) GitLoadReport {
	cloned := report
	cloned.Issues = make([]model.Issue, len(report.Issues))
	for i, issue := range report.Issues {
		cloned.Issues[i] = issue.Clone()
	}
	cloned.Warnings = append([]string(nil), report.Warnings...)
	cloned.TombstoneIDs = append([]string(nil), report.TombstoneIDs...)
	return cloned
}

// ClearCache removes all cached entries
func (g *GitLoader) ClearCache() {
	g.cache.mu.Lock()
	defer g.cache.mu.Unlock()
	g.cache.entries = make(map[string]cacheEntry)
}

// CacheStats returns cache statistics
func (g *GitLoader) CacheStats() CacheStats {
	g.cache.mu.RLock()
	defer g.cache.mu.RUnlock()

	now := g.cache.currentTime()
	valid := 0
	for _, entry := range g.cache.entries {
		if revisionCacheEntryIsFresh(entry.loadedAt, now, g.cache.maxAge) {
			valid++
		}
	}

	return CacheStats{
		TotalEntries: len(g.cache.entries),
		ValidEntries: valid,
		MaxAge:       g.cache.maxAge,
	}
}

// CacheStats holds cache statistics
type CacheStats struct {
	TotalEntries int           `json:"total_entries"`
	ValidEntries int           `json:"valid_entries"`
	MaxAge       time.Duration `json:"max_age"`
}

// GetCommitsBetween returns commits between two revisions
func (g *GitLoader) GetCommitsBetween(fromRev, toRev string) ([]RevisionInfo, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return nil, err
	}

	// Resolve revisions
	fromSHA, err := g.resolveRevisionInDir(target.repoRoot, fromRev)
	if err != nil {
		return nil, fmt.Errorf("resolving from revision: %w", err)
	}

	toSHA, err := g.resolveRevisionInDir(target.repoRoot, toRev)
	if err != nil {
		return nil, fmt.Errorf("resolving to revision: %w", err)
	}

	gitArgs := []string{
		"log",
		"--format=%H|%aI|%s",
		fmt.Sprintf("%s..%s", fromSHA, toSHA),
		"--",
	}
	gitArgs = append(gitArgs, target.gitPaths(nil)...)
	cmd := g.gitCommandInDir(target.repoRoot, gitArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing commits between revisions: %w", err)
	}

	var revisions []RevisionInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}

		timestamp, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue // skip revisions with unparseable timestamps
		}
		revisions = append(revisions, RevisionInfo{
			SHA:       parts[0],
			Timestamp: timestamp,
			Message:   parts[2],
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parsing git log output: %w", err)
	}

	return revisions, nil
}

// HasBeadsAtRevision checks if beads files exist at a given revision
func (g *GitLoader) HasBeadsAtRevision(revision string) (bool, error) {
	target, err := g.resolveTarget()
	if err != nil {
		return false, err
	}
	sha, err := g.resolveRevisionInDir(target.repoRoot, revision)
	if err != nil {
		return false, err
	}

	paths := target.gitPaths(nil)
	// Ask Git once for all candidate paths. The previous per-path cat-file loop
	// spawned up to three subprocesses and treated every failure (including an
	// unreadable/corrupt repository) as ordinary absence. ls-tree distinguishes a
	// successful empty result from an operational failure while preserving exact
	// path matching.
	args := []string{"ls-tree", "--name-only", "--full-tree", sha, "--"}
	args = append(args, paths...)
	out, err := g.gitCommandInDir(target.repoRoot, args...).Output()
	if err != nil {
		return false, fmt.Errorf("checking beads files at %s: %w", sha, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}
