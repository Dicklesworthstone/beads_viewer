package loader

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// GitLoader loads beads from git history
type GitLoader struct {
	repoPath string
	cache    *revisionCache
}

// revisionCache caches loaded issues by their resolved commit SHA
type revisionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	maxAge  time.Duration
}

// cacheEntry holds cached issues with metadata
type cacheEntry struct {
	issues    []model.Issue
	loadedAt  time.Time
	commitSHA string
}

// NewGitLoader creates a new git history loader for the given repo
func NewGitLoader(repoPath string) *GitLoader {
	return &GitLoader{
		repoPath: repoPath,
		cache: &revisionCache{
			entries: make(map[string]cacheEntry),
			maxAge:  5 * time.Minute,
		},
	}
}

// NewGitLoaderWithCacheTTL creates a loader with custom cache TTL
func NewGitLoaderWithCacheTTL(repoPath string, cacheTTL time.Duration) *GitLoader {
	return &GitLoader{
		repoPath: repoPath,
		cache: &revisionCache{
			entries: make(map[string]cacheEntry),
			maxAge:  cacheTTL,
		},
	}
}

// LoadAt loads issues from a specific git revision
// revision can be: SHA, branch name, tag name, HEAD~N, or date expression
func (g *GitLoader) LoadAt(revision string) ([]model.Issue, error) {
	// Resolve to commit SHA for caching
	sha, err := g.resolveRevision(revision)
	if err != nil {
		return nil, fmt.Errorf("resolving revision %q: %w", revision, err)
	}

	// Check cache
	if issues, ok := g.cache.get(sha); ok {
		return issues, nil
	}

	// Load from git
	issues, err := g.loadFromGit(sha)
	if err != nil {
		return nil, err
	}

	// Cache the result
	g.cache.set(sha, issues)

	return issues, nil
}

// LoadAtDate loads issues from the state at a specific date/time
// Uses git rev-list to find the commit at or before the given time
func (g *GitLoader) LoadAtDate(t time.Time) ([]model.Issue, error) {
	revision := fmt.Sprintf("HEAD@{%s}", t.Format("2006-01-02 15:04:05"))
	return g.LoadAt(revision)
}

// ResolveRevision resolves any git revision to its commit SHA
func (g *GitLoader) ResolveRevision(revision string) (string, error) {
	return g.resolveRevision(revision)
}

// ListRevisions returns commits that modified beads files
func (g *GitLoader) ListRevisions(limit int) ([]RevisionInfo, error) {
	args := []string{
		"log",
		"--format=%H|%aI|%s",
		"--",
		".beads/beads.base.jsonl",
		".beads/beads.jsonl",
		".beads/issues.jsonl",
	}
	if limit > 0 {
		// Insert -n limit after "log"
		args = append([]string{"log", fmt.Sprintf("-n%d", limit)}, args[1:]...)
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = g.repoPath

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

		timestamp, _ := time.Parse(time.RFC3339, parts[1])
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
	cmd := exec.Command("git", "rev-parse", revision)
	cmd.Dir = g.repoPath

	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	// If rev-parse failed, try to interpret the revision as a date.
	if t, ok := parseDateString(revision); ok {
		dateSpec := fmt.Sprintf("HEAD@{%s}", t.Format("2006-01-02 15:04:05"))
		cmd = exec.Command("git", "rev-parse", dateSpec)
		cmd.Dir = g.repoPath
		if out, dateErr := cmd.Output(); dateErr == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}

	return "", fmt.Errorf("git rev-parse failed: %w", err)
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
	// Try known beads file paths in order
	paths := []string{
		".beads/beads.jsonl",
		".beads/beads.base.jsonl",
		".beads/issues.jsonl",
	}

	var lastErr error
	for _, path := range paths {
		issues, err := g.loadFileFromGit(sha, path)
		if err == nil {
			return issues, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("no beads file found at %s: %w", sha, lastErr)
}

// loadFileFromGit loads a specific file from git at a commit
func (g *GitLoader) loadFileFromGit(sha, path string) ([]model.Issue, error) {
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:%s", sha, path))
	cmd.Dir = g.repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s:%s failed: %w", sha, path, err)
	}

	return parseJSONL(out)
}

// parseJSONL parses JSONL content into issues
func parseJSONL(data []byte) ([]model.Issue, error) {
	var issues []model.Issue
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Increase buffer size for large lines
	const maxCapacity = 1024 * 1024 * 10 // 10MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var issue model.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			// Skip malformed lines
			continue
		}
		issues = append(issues, issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning JSONL: %w", err)
	}

	return issues, nil
}

// Cache methods

func (c *revisionCache) get(sha string) ([]model.Issue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[sha]
	if !ok {
		return nil, false
	}

	// Check if entry is still valid
	if time.Since(entry.loadedAt) > c.maxAge {
		return nil, false
	}

	// Return a copy to prevent mutation
	issues := make([]model.Issue, len(entry.issues))
	copy(issues, entry.issues)
	return issues, true
}

func (c *revisionCache) set(sha string, issues []model.Issue) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Make a copy for storage
	stored := make([]model.Issue, len(issues))
	copy(stored, issues)

	c.entries[sha] = cacheEntry{
		issues:    stored,
		loadedAt:  time.Now(),
		commitSHA: sha,
	}
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

	valid := 0
	for _, entry := range g.cache.entries {
		if time.Since(entry.loadedAt) <= g.cache.maxAge {
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
	// Resolve revisions
	fromSHA, err := g.resolveRevision(fromRev)
	if err != nil {
		return nil, fmt.Errorf("resolving from revision: %w", err)
	}

	toSHA, err := g.resolveRevision(toRev)
	if err != nil {
		return nil, fmt.Errorf("resolving to revision: %w", err)
	}

	cmd := exec.Command("git", "log",
		"--format=%H|%aI|%s",
		fmt.Sprintf("%s..%s", fromSHA, toSHA),
		"--",
		".beads/beads.base.jsonl",
		".beads/beads.jsonl",
		".beads/issues.jsonl",
	)
	cmd.Dir = g.repoPath

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

		timestamp, _ := time.Parse(time.RFC3339, parts[1])
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
	sha, err := g.resolveRevision(revision)
	if err != nil {
		return false, err
	}

	paths := []string{
		".beads/beads.jsonl",
		".beads/beads.base.jsonl",
		".beads/issues.jsonl",
	}

	for _, path := range paths {
		cmd := exec.Command("git", "cat-file", "-e", fmt.Sprintf("%s:%s", sha, path))
		cmd.Dir = g.repoPath
		if err := cmd.Run(); err == nil {
			return true, nil
		}
	}

	return false, nil
}
