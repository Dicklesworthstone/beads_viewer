package datasource

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// WorkspaceInfo represents a discovered beads workspace
type WorkspaceInfo struct {
	BeadsDir string
	RepoPath string
	RepoName string
}

// DiscoverBeadsWorkspaces recursively finds all .beads directories
// starting from root, up to maxDepth levels deep.
func DiscoverBeadsWorkspaces(root string, maxDepth int) []WorkspaceInfo {
	var workspaces []WorkspaceInfo
	root = filepath.Clean(root)
	sep := string(os.PathSeparator)

	// Directories to skip during traversal
	skip := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		"target":       true,
		".cache":       true,
		".bv":          true,
		".idea":        true,
		".vscode":      true,
		".venv":        true,
		"venv":         true,
		"__pycache__":  true,
		".cargo":       true,
	}

	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		name := d.Name()
		if skip[name] && path != root {
			return fs.SkipDir
		}

		rel := strings.TrimPrefix(strings.TrimPrefix(path, root), sep)
		if rel != "" {
			if depth := len(strings.Split(rel, sep)); depth > maxDepth {
				return fs.SkipDir
			}
		}

		if name == ".beads" {
			repoPath := filepath.Dir(path)
			repoName := filepath.Base(repoPath)

			workspaces = append(workspaces, WorkspaceInfo{
				BeadsDir: path,
				RepoPath: repoPath,
				RepoName: repoName,
			})
			return fs.SkipDir
		}
		return nil
	})

	return workspaces
}

// LoadIssuesRecursive discovers all .beads directories in the current folder
// and its children (up to maxDepth), loads issues from each, and returns
// them all aggregated with repository metadata.
func LoadIssuesRecursive(repoPath string, maxDepth int) ([]model.Issue, error) {
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory: %w", err)
		}
	}

	workspaces := DiscoverBeadsWorkspaces(repoPath, maxDepth)

	if len(workspaces) == 0 {
		return nil, fmt.Errorf("no .beads directories found in %s (max depth: %d)", repoPath, maxDepth)
	}

	var allIssues []model.Issue
	for _, ws := range workspaces {
		issues, err := LoadIssuesFromDir(ws.BeadsDir)
		if err != nil {
			// Log but continue with other workspaces
			continue
		}

		// Tag each issue with repository information
		for i := range issues {
			// Add repository name as a label if not already present
			repoLabel := fmt.Sprintf("repo:%s", ws.RepoName)
			if !containsLabel(issues[i].Labels, repoLabel) {
				issues[i].Labels = append(issues[i].Labels, repoLabel)
			}

			// Store repository metadata in Tags field
			if issues[i].Tags == nil {
				issues[i].Tags = make([]string, 0)
			}
			// Add repository path as metadata
			repoPathTag := fmt.Sprintf("_repo_path:%s", ws.RepoPath)
			if !containsTag(issues[i].Tags, repoPathTag) {
				issues[i].Tags = append(issues[i].Tags, repoPathTag)
			}
		}

		allIssues = append(allIssues, issues...)
	}

	if len(allIssues) == 0 {
		return nil, fmt.Errorf("no issues found in %d workspaces", len(workspaces))
	}

	return allIssues, nil
}

// LoadIssuesWithDiscovery loads issues using recursive discovery if enabled,
// otherwise falls back to the standard single-directory load.
func LoadIssuesWithDiscovery(repoPath string, enableRecursive bool, maxDepth int) ([]model.Issue, error) {
	if enableRecursive {
		return LoadIssuesRecursive(repoPath, maxDepth)
	}
	return LoadIssues(repoPath)
}

// Helper functions

func containsLabel(labels []string, target string) bool {
	for _, label := range labels {
		if label == target {
			return true
		}
	}
	return false
}

func containsTag(tags []string, target string) bool {
	for _, tag := range tags {
		if tag == target {
			return true
		}
	}
	return false
}
