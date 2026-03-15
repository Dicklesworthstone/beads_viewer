package workspace

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"

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

	// Issues are the loaded issues with namespaced IDs
	Issues []model.Issue

	// Error is set if loading failed
	Error error
}

// AggregateLoader loads issues from multiple repositories in a workspace
type AggregateLoader struct {
	config        *Config
	workspaceRoot string
	logger        *log.Logger
}

// NewAggregateLoader creates a new aggregate loader for the given workspace config
func NewAggregateLoader(config *Config, workspaceRoot string) *AggregateLoader {
	return &AggregateLoader{
		config:        config,
		workspaceRoot: workspaceRoot,
		// Silence by default. Callers can opt-in via SetLogger.
		// This avoids polluting stderr (e.g., breaking robot JSON consumers that
		// capture combined stdout/stderr).
		logger: log.New(io.Discard, "", 0),
	}
}

// SetLogger sets a custom logger for error reporting
func (l *AggregateLoader) SetLogger(logger *log.Logger) {
	l.logger = logger
}

// LoadAll loads issues from all enabled repositories in the workspace.
// Returns the merged list of issues with namespaced IDs.
// Failed repos are logged but don't break the overall loading process.
func (l *AggregateLoader) LoadAll(ctx context.Context) ([]model.Issue, []LoadResult, error) {
	if l.config == nil {
		return nil, nil, fmt.Errorf("workspace config is nil")
	}

	// Collect enabled repos
	enabledRepos := l.getEnabledRepos()
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
	for _, result := range results {
		if result.Error != nil {
			// Log but continue - individual repo failures don't break the whole load
			l.logRepoError(result.RepoName, result.Error)
			continue
		}
		allIssues = append(allIssues, result.Issues...)
	}

	return allIssues, results, nil
}

// getEnabledRepos returns all enabled repos from the config
func (l *AggregateLoader) getEnabledRepos() []RepoConfig {
	var enabled []RepoConfig
	for _, repo := range l.config.Repos {
		if repo.IsEnabled() {
			enabled = append(enabled, repo)
		}
	}
	return enabled
}

// loadReposParallel loads issues from all repos concurrently using errgroup
func (l *AggregateLoader) loadReposParallel(ctx context.Context, repos []RepoConfig) ([]LoadResult, error) {
	results := make([]LoadResult, len(repos))

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
					Error:    ctx.Err(),
				}
				return nil // Don't propagate context errors as fatal
			default:
			}

			issues, err := l.loadSingleRepo(repo)

			results[i] = LoadResult{
				RepoName: repo.GetName(),
				Prefix:   repo.GetPrefix(),
				Issues:   issues,
				Error:    err,
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
func (l *AggregateLoader) loadSingleRepo(repo RepoConfig) ([]model.Issue, error) {
	// Resolve the repo path relative to workspace root
	repoPath := repo.Path
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(l.workspaceRoot, repoPath)
	}

	// Load raw issues from the repo, respecting custom beads path if provided
	beadsDir := filepath.Join(repoPath, repo.GetBeadsPath())
	jsonlPath, err := loader.PrepareBeadsDirForRead(beadsDir, true, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to load issues from %s: %w", repo.GetName(), err)
	}
	issues, err := loader.LoadIssuesFromFile(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load issues from %s: %w", repo.GetName(), err)
	}

	// Build map of local IDs for conflict resolution
	localIDs := make(map[string]bool, len(issues))
	for _, issue := range issues {
		localIDs[issue.ID] = true
	}

	// Apply namespacing to all IDs
	prefix := repo.GetPrefix()
	namespacedIssues := l.namespaceIssues(issues, prefix, localIDs)

	return namespacedIssues, nil
}

// namespaceIssues adds the prefix to all issue IDs and dependency references
// It mutates the issues slice in place to reduce allocations.
func (l *AggregateLoader) namespaceIssues(issues []model.Issue, prefix string, localIDs map[string]bool) []model.Issue {
	for i := range issues {
		// Mutate issue in place
		issue := &issues[i]
		issue.ID = QualifyID(issue.ID, prefix)

		// Namespace dependency references in place
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			dep.IssueID = QualifyID(dep.IssueID, prefix)

			// Resolve DependsOnID
			if localIDs[dep.DependsOnID] {
				dep.DependsOnID = QualifyID(dep.DependsOnID, prefix)
			} else if l.hasKnownPrefix(dep.DependsOnID) {
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

// hasKnownPrefix checks if an ID already has a known namespace prefix
func (l *AggregateLoader) hasKnownPrefix(id string) bool {
	for _, repo := range l.config.Repos {
		prefix := repo.GetPrefix()
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
