package workspace

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// LoadResult contains the result of loading a single repository
type LoadResult struct {
	// RepoName is the name of the repository
	RepoName string

	// Prefix is the namespace prefix used for IDs
	Prefix string

	// RepoPath is the configured repository path, retained so diagnostics stay
	// unambiguous even when display names are duplicated or contain delimiters.
	RepoPath string

	// Issues are the loaded issues with namespaced IDs
	Issues []model.Issue

	// ParseStats records source-order JSONL accounting for this repository.
	// A successful load with Errors > 0 is usable for exploratory workspace
	// views, but it is not a complete authority for claim-emitting robot output.
	ParseStats loader.ParseStats

	// AuthorityWarnings records source-selection fallbacks that kept this
	// repository readable but may have made its issue snapshot stale.
	AuthorityWarnings []string

	// Error is set if loading failed
	Error error
}

// AggregateLoader loads issues from multiple repositories in a workspace
type AggregateLoader struct {
	config                 *Config
	workspaceRoot          string
	logger                 *log.Logger
	parseWarningsUseLogger bool
}

// NewAggregateLoader creates a new aggregate loader for the given workspace config
func NewAggregateLoader(config *Config, workspaceRoot string) *AggregateLoader {
	return &AggregateLoader{
		config:        config,
		workspaceRoot: workspaceRoot,
		// Silence aggregate progress/error logs by default. Per-record parse
		// warnings still use the loader's default interactive-stderr/robot-quiet
		// behavior until a caller explicitly routes them with SetLogger.
		logger: log.New(io.Discard, "", 0),
	}
}

// SetLogger sets a custom logger for aggregate diagnostics and per-record parse
// warnings. Passing nil explicitly routes both to a discard logger.
func (l *AggregateLoader) SetLogger(logger *log.Logger) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	l.logger = logger
	l.parseWarningsUseLogger = true
}

// LoadAll loads issues from all enabled repositories in the workspace.
// Returns the merged list of issues with namespaced IDs.
// Failed repos are logged and tolerated as long as at least one repo loads.
func (l *AggregateLoader) LoadAll(ctx context.Context) ([]model.Issue, []LoadResult, error) {
	if l.config == nil {
		return nil, nil, fmt.Errorf("workspace config is nil")
	}

	// Collect enabled repos
	enabledRepos, err := l.getEnabledRepos()
	if err != nil {
		return nil, nil, err
	}
	if len(enabledRepos) == 0 {
		return nil, nil, fmt.Errorf("no enabled repositories in workspace")
	}

	// Load repos in parallel using errgroup
	results, err := l.loadReposParallel(ctx, enabledRepos)
	if err != nil {
		return nil, results, fmt.Errorf("fatal error during parallel loading: %w", err)
	}

	// Merge all successfully loaded issues
	var allIssues []model.Issue
	var failedRepoNames []string
	var firstRepoErr error
	issueSource := make(map[string]string)
	collisionSources := make(map[string]map[string]bool)
	for _, result := range results {
		if result.Error != nil {
			// Log but continue - individual repo failures don't break the whole load
			l.logRepoError(result.RepoName, result.Error)
			failedRepoNames = append(failedRepoNames, result.RepoName)
			if firstRepoErr == nil {
				firstRepoErr = result.Error
			}
			continue
		}
		identity := workspaceLoadResultIdentity(result)
		for _, issue := range result.Issues {
			if previous, exists := issueSource[issue.ID]; exists {
				if collisionSources[issue.ID] == nil {
					collisionSources[issue.ID] = map[string]bool{previous: true}
				}
				collisionSources[issue.ID][identity] = true
			} else {
				issueSource[issue.ID] = identity
			}
		}
		allIssues = append(allIssues, result.Issues...)
	}

	if len(failedRepoNames) == len(results) {
		return nil, results, fmt.Errorf("all %d enabled repositories failed to load (%s): %w",
			len(results), strings.Join(failedRepoNames, ", "), firstRepoErr)
	}
	if len(collisionSources) > 0 {
		ids := make([]string, 0, len(collisionSources))
		for id := range collisionSources {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		details := make([]string, 0, len(ids))
		for _, id := range ids {
			sources := make([]string, 0, len(collisionSources[id]))
			for source := range collisionSources[id] {
				sources = append(sources, source)
			}
			sort.Strings(sources)
			details = append(details, fmt.Sprintf("%q from %s", id, strings.Join(sources, ", ")))
		}
		return nil, results, fmt.Errorf("workspace namespacing produced duplicate issue IDs: %s", strings.Join(details, "; "))
	}

	return allIssues, results, nil
}

// getEnabledRepos returns all explicitly configured and discovered enabled repos.
func (l *AggregateLoader) getEnabledRepos() ([]RepoConfig, error) {
	var enabled []RepoConfig
	seenPaths := make(map[string]bool)
	seenPrefixes := make(map[string]bool)
	seenSourceRepos := make(map[string]bool)

	addRepo := func(repo RepoConfig) error {
		repo = l.applyDefaults(repo)

		pathKey, err := l.repoPathKey(repo)
		if err != nil {
			return err
		}
		if seenPaths[pathKey] {
			return nil
		}
		seenPaths[pathKey] = true

		if !repo.IsEnabled() {
			return nil
		}

		prefixKey := strings.ToLower(repo.GetPrefix())
		if seenPrefixes[prefixKey] {
			return fmt.Errorf("duplicate workspace prefix %q", repo.GetPrefix())
		}
		sourceRepo := sourceRepoKeyFromPrefix(prefixKey)
		if sourceRepo == "" {
			return fmt.Errorf("workspace prefix %q has no usable source repository key", repo.GetPrefix())
		}
		if seenSourceRepos[sourceRepo] {
			return fmt.Errorf("workspace prefix %q duplicates normalized source repository key %q", repo.GetPrefix(), sourceRepo)
		}

		seenPrefixes[prefixKey] = true
		seenSourceRepos[sourceRepo] = true
		enabled = append(enabled, repo)
		return nil
	}

	for _, repo := range l.config.Repos {
		if err := addRepo(repo); err != nil {
			return nil, err
		}
	}

	if l.config.Discovery.Enabled {
		discovered, err := l.discoverRepos()
		if err != nil {
			return nil, err
		}
		for _, repo := range discovered {
			if err := addRepo(repo); err != nil {
				return nil, err
			}
		}
	}

	return enabled, nil
}

func (l *AggregateLoader) applyDefaults(repo RepoConfig) RepoConfig {
	if repo.BeadsPath == "" && l.config != nil && l.config.Defaults.BeadsPath != "" {
		repo.BeadsPath = l.config.Defaults.BeadsPath
	}
	return repo
}

func (l *AggregateLoader) defaultBeadsPath() string {
	if l.config != nil && l.config.Defaults.BeadsPath != "" {
		return l.config.Defaults.BeadsPath
	}
	return ".beads"
}

func (l *AggregateLoader) repoPathKey(repo RepoConfig) (string, error) {
	path := l.resolveRepoPath(repo.Path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve repo path %q: %w", repo.Path, err)
	}
	return filepath.Clean(abs), nil
}

func (l *AggregateLoader) resolveRepoPath(repoPath string) string {
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(l.workspaceRoot, repoPath)
	}
	return repoPath
}

func (l *AggregateLoader) discoverRepos() ([]RepoConfig, error) {
	root := l.workspaceRoot
	if root == "" {
		root = "."
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %q: %w", root, err)
	}

	patterns := l.config.Discovery.Patterns
	if len(patterns) == 0 {
		patterns = DefaultDiscoveryPatterns()
	}
	excludes := l.config.Discovery.Exclude
	if len(excludes) == 0 {
		excludes = DefaultExcludePatterns()
	}
	maxDepth := l.config.Discovery.MaxDepth
	if maxDepth == 0 {
		maxDepth = 2
	}

	var repos []RepoConfig
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		glob := filepath.Join(rootAbs, filepath.FromSlash(pattern))
		matches, err := filepath.Glob(glob)
		if err != nil {
			return nil, fmt.Errorf("invalid discovery pattern %q: %w", pattern, err)
		}
		sort.Strings(matches)

		for _, match := range matches {
			info, err := os.Stat(match)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect discovered repository %q: %w", match, err)
			}
			if !info.IsDir() {
				continue
			}
			rel, err := filepath.Rel(rootAbs, match)
			if err != nil {
				return nil, fmt.Errorf("resolve discovered repo %q: %w", match, err)
			}
			rel = filepath.ToSlash(filepath.Clean(rel))
			if rel == "." {
				rel = "."
			}
			if discoveryDepth(rel) > maxDepth || discoveryExcluded(rel, excludes) {
				continue
			}
			if seen[rel] {
				continue
			}

			beadsPath := l.defaultBeadsPath()
			beadsDir, err := loader.ResolveBeadsDir(filepath.Join(match, beadsPath))
			if err != nil {
				return nil, fmt.Errorf("resolve tracker for discovered repository %q: %w", match, err)
			}
			beadsInfo, err := os.Stat(beadsDir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect tracker for discovered repository %q: %w", match, err)
			}
			if !beadsInfo.IsDir() {
				continue
			}
			// A Dolt-native tracker is discoverable even before its compatibility
			// export exists; loadSingleRepo refreshes that export. For JSONL-backed
			// trackers, require a selectable issue file at the resolved redirect
			// target rather than incorrectly probing only the local redirect stub.
			if !loader.IsBDWorkspace(beadsDir) {
				if _, err := loader.FindJSONLPath(beadsDir); err != nil {
					continue
				}
			}

			repos = append(repos, RepoConfig{
				Path:      rel,
				BeadsPath: beadsPath,
			})
			seen[rel] = true
		}
	}

	return repos, nil
}

func discoveryDepth(rel string) int {
	if rel == "" || rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

func discoveryExcluded(rel string, excludes []string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	base := filepath.Base(rel)
	parts := strings.Split(rel, "/")

	for _, raw := range excludes {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		pattern = filepath.ToSlash(filepath.Clean(pattern))
		if pattern == rel || pattern == base {
			return true
		}
		if ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(rel)); ok {
			return true
		}
		if ok, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(base)); ok {
			return true
		}
		for _, part := range parts {
			if pattern == part {
				return true
			}
		}
	}

	return false
}

// loadReposParallel loads issues from all repos concurrently using errgroup
func (l *AggregateLoader) loadReposParallel(ctx context.Context, repos []RepoConfig) ([]LoadResult, error) {
	results := make([]LoadResult, len(repos))
	knownPrefixes := knownRepoPrefixes(repos)

	g, ctx := errgroup.WithContext(ctx)
	// Limit concurrency to avoid resource exhaustion (file descriptors, memory)
	g.SetLimit(32)

	for i, repo := range repos {
		i, repo := i, repo // capture loop variables

		g.Go(func() error {
			select {
			case <-ctx.Done():
				results[i] = LoadResult{
					RepoName: repo.GetName(),
					Prefix:   repo.GetPrefix(),
					RepoPath: repo.Path,
					Error:    ctx.Err(),
				}
				return nil // Don't propagate context errors as fatal
			default:
			}

			issues, parseStats, authorityWarnings, err := l.loadSingleRepo(repo, knownPrefixes)

			results[i] = LoadResult{
				RepoName:          repo.GetName(),
				Prefix:            repo.GetPrefix(),
				RepoPath:          repo.Path,
				Issues:            issues,
				ParseStats:        parseStats,
				AuthorityWarnings: authorityWarnings,
				Error:             err,
			}

			return nil // Individual repo errors are captured in results, not propagated
		})
	}

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return results, err
	}

	if l.logger != nil {
		l.logger.Printf("Finished parallel loading of %d repos", len(repos))
	}

	return results, nil
}

// loadSingleRepo loads issues from a single repository and namespaced them
func (l *AggregateLoader) loadSingleRepo(repo RepoConfig, knownPrefixes map[string]bool) ([]model.Issue, loader.ParseStats, []string, error) {
	// Resolve the repo path relative to workspace root
	repo = l.applyDefaults(repo)
	repoPath := l.resolveRepoPath(repo.Path)

	// Load raw issues from the repo, respecting custom beads path if provided
	beadsDir, err := loader.ResolveBeadsDir(filepath.Join(repoPath, repo.GetBeadsPath()))
	if err != nil {
		return nil, loader.ParseStats{}, nil, fmt.Errorf("failed to resolve tracker for %s: %w", repo.GetName(), err)
	}
	var authorityWarnings []string
	warnSourceFallback := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		authorityWarnings = append(authorityWarnings, message)
		if l.parseWarningsUseLogger {
			if l.logger != nil {
				l.logger.Printf("repository %q: %s", repo.GetName(), message)
			}
		} else if os.Getenv("BV_ROBOT") != "1" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", message)
		}
	}
	jsonlPath, err := loader.PrepareBeadsDirForRead(beadsDir, true, warnSourceFallback)
	if err != nil {
		return nil, loader.ParseStats{}, authorityWarnings, fmt.Errorf("failed to load issues from %s: %w", repo.GetName(), err)
	}
	var parseStats loader.ParseStats
	parseOptions := loader.ParseOptions{Stats: &parseStats}
	if l.parseWarningsUseLogger {
		parseOptions.WarningHandler = func(message string) {
			if l.logger != nil {
				l.logger.Printf("repository %q: %s", repo.GetName(), message)
			}
		}
	}
	issues, err := loader.LoadIssuesFromFileWithOptions(jsonlPath, parseOptions)
	if err != nil {
		return nil, parseStats, authorityWarnings, fmt.Errorf("failed to load issues from %s: %w", repo.GetName(), err)
	}
	if parseStats.Valid == 0 && parseStats.Errors+parseStats.Skipped > 0 {
		return nil, parseStats, authorityWarnings, fmt.Errorf("failed to load issues from %s: no issue records (%d non-issue/error lines, 0 valid issues)",
			repo.GetName(), parseStats.Errors+parseStats.Skipped)
	}
	// ParseStats describes the source faithfully, including valid tombstone
	// records, while the viewer's issue universe consistently excludes those
	// soft-deleted records before namespacing, graph construction, and collision
	// detection.
	visibleIssues := issues[:0]
	for i := range issues {
		if !issues[i].Status.IsTombstone() {
			visibleIssues = append(visibleIssues, issues[i])
		}
	}
	clear(issues[len(visibleIssues):])
	issues = visibleIssues

	// Build map of local IDs for conflict resolution
	localIDs := make(map[string]bool, len(issues))
	for _, issue := range issues {
		localIDs[issue.ID] = true
	}

	// Apply namespacing to all IDs
	prefix := repo.GetPrefix()
	namespacedIssues := l.namespaceIssues(issues, prefix, localIDs, knownPrefixes)

	return namespacedIssues, parseStats, authorityWarnings, nil
}

func workspaceLoadResultIdentity(result LoadResult) string {
	if strings.TrimSpace(result.RepoPath) != "" {
		return fmt.Sprintf("%q (path %q)", result.RepoName, result.RepoPath)
	}
	return fmt.Sprintf("%q", result.RepoName)
}

func knownRepoPrefixes(repos []RepoConfig) map[string]bool {
	prefixes := make(map[string]bool, len(repos))
	for _, repo := range repos {
		prefix := repo.GetPrefix()
		if prefix != "" {
			prefixes[prefix] = true
		}
	}
	return prefixes
}

func sourceRepoKeyFromPrefix(prefix string) string {
	key := strings.TrimSpace(prefix)
	key = strings.TrimRight(key, "-:_")
	return strings.ToLower(key)
}

// namespaceIssues adds the prefix to all issue IDs and dependency references
// It mutates the issues slice in place to reduce allocations.
func (l *AggregateLoader) namespaceIssues(issues []model.Issue, prefix string, localIDs map[string]bool, knownPrefixes map[string]bool) []model.Issue {
	sourceRepo := sourceRepoKeyFromPrefix(prefix)

	for i := range issues {
		// Mutate issue in place
		issue := &issues[i]
		issue.ID = QualifyID(issue.ID, prefix)
		issue.SourceRepo = sourceRepo

		// Namespace dependency references in place
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			dep.IssueID = QualifyID(dep.IssueID, prefix)

			// Resolve DependsOnID
			if localIDs[dep.DependsOnID] {
				dep.DependsOnID = QualifyID(dep.DependsOnID, prefix)
			} else if hasKnownPrefix(dep.DependsOnID, knownPrefixes) {
				// External reference, keep as is
			} else {
				// Assume local
				dep.DependsOnID = QualifyID(dep.DependsOnID, prefix)
			}
		}

		// Namespace comment issue references in place
		for _, comment := range issue.Comments {
			if comment == nil {
				continue
			}
			comment.IssueID = QualifyID(comment.IssueID, prefix)
		}
	}

	return issues
}

// hasKnownPrefix checks if an ID already has a known namespace prefix.
func hasKnownPrefix(id string, knownPrefixes map[string]bool) bool {
	for prefix := range knownPrefixes {
		if len(id) > len(prefix) && id[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// logRepoError logs an error for a repo that failed to load
func (l *AggregateLoader) logRepoError(repoName string, err error) {
	if l.logger != nil {
		l.logger.Printf("WARNING: Failed to load repo %q: %v", repoName, err)
	}
}

// LoadAllFromConfig is a convenience function that loads a workspace config and all its repos
func LoadAllFromConfig(ctx context.Context, configPath string) ([]model.Issue, []LoadResult, error) {
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load workspace config: %w", err)
	}

	workspaceRoot := filepath.Dir(filepath.Dir(configPath)) // .bv/workspace.yaml -> workspace root
	loader := NewAggregateLoader(config, workspaceRoot)

	return loader.LoadAll(ctx)
}

// Summary returns a summary of load results
type LoadSummary struct {
	TotalRepos      int
	SuccessfulRepos int
	FailedRepos     int
	TotalIssues     int
	FailedRepoNames []string
	RepoPrefixes    []string // Prefixes of successfully loaded repos
}

// Summarize returns a summary of the load results
func Summarize(results []LoadResult) LoadSummary {
	summary := LoadSummary{
		TotalRepos: len(results),
	}

	for _, result := range results {
		if result.Error != nil {
			summary.FailedRepos++
			summary.FailedRepoNames = append(summary.FailedRepoNames, result.RepoName)
		} else {
			summary.SuccessfulRepos++
			summary.TotalIssues += len(result.Issues)
			if result.Prefix != "" {
				summary.RepoPrefixes = append(summary.RepoPrefixes, result.Prefix)
			}
		}
	}

	return summary
}
