// Package baseline provides storage and management for metrics snapshots.
// Baselines are used by the drift detection system to compare current
// state against a known-good reference point.
package baseline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

// Baseline represents a snapshot of project metrics at a point in time
type Baseline struct {
	// Version for schema compatibility
	Version int `json:"version"`

	// CreatedAt is when the baseline was saved
	CreatedAt time.Time `json:"created_at"`

	// CommitSHA is the git commit when baseline was created (if available)
	CommitSHA string `json:"commit_sha,omitempty"`

	// CommitMessage is the first line of the commit message
	CommitMessage string `json:"commit_message,omitempty"`

	// Branch is the git branch when baseline was created
	Branch string `json:"branch,omitempty"`

	// Description is an optional user-provided note
	Description string `json:"description,omitempty"`

	// Scope records the filter/workspace scope active when the baseline was saved
	Scope *BaselineScope `json:"scope,omitempty"`

	// Stats contains the graph statistics snapshot
	Stats GraphStats `json:"stats"`

	// TopMetrics contains top-N items for key metrics
	TopMetrics TopMetrics `json:"top_metrics"`

	// Cycles stores one representative cycle per detected cyclic component.
	Cycles [][]string `json:"cycles,omitempty"`
}

// BaselineScope records the filter/workspace scope active when a baseline was captured.
type BaselineScope struct {
	Label     string `json:"label,omitempty"`
	Recipe    string `json:"recipe,omitempty"`
	Repo      string `json:"repo,omitempty"`
	GraphRoot string `json:"graph_root,omitempty"`
	Workspace bool   `json:"workspace,omitempty"`
}

func scopesEqual(a, b BaselineScope) bool {
	return strings.TrimSpace(a.Label) == strings.TrimSpace(b.Label) &&
		strings.EqualFold(strings.TrimSpace(a.Recipe), strings.TrimSpace(b.Recipe)) &&
		strings.EqualFold(strings.TrimSpace(a.Repo), strings.TrimSpace(b.Repo)) &&
		strings.TrimSpace(a.GraphRoot) == strings.TrimSpace(b.GraphRoot) &&
		a.Workspace == b.Workspace
}

// MatchesScope reports whether b's recorded scope matches other. A nil *Baseline
// never matches; a non-nil *Baseline with nil Scope (legacy/unscoped baseline)
// is treated as equivalent to an empty BaselineScope{}. Repo and Recipe compare
// case-insensitively to match CLI filter normalization.
func (b *Baseline) MatchesScope(other BaselineScope) bool {
	if b == nil {
		return false
	}
	var self BaselineScope
	if b.Scope != nil {
		self = *b.Scope
	}
	return scopesEqual(self, other)
}

// GraphStats contains basic graph statistics
type GraphStats struct {
	NodeCount       int     `json:"node_count"`
	EdgeCount       int     `json:"edge_count"`
	Density         float64 `json:"density"`
	OpenCount       int     `json:"open_count"`
	ClosedCount     int     `json:"closed_count"`
	BlockedCount    int     `json:"blocked_count"`
	CycleCount      int     `json:"cycle_count"`
	ActionableCount int     `json:"actionable_count"`
}

// TopMetrics stores top-N items for comparison
type TopMetrics struct {
	// PageRank top items with scores
	PageRank []MetricItem `json:"pagerank,omitempty"`

	// Betweenness top items
	Betweenness []MetricItem `json:"betweenness,omitempty"`

	// CriticalPath top items
	CriticalPath []MetricItem `json:"critical_path,omitempty"`

	// Hubs from HITS
	Hubs []MetricItem `json:"hubs,omitempty"`

	// Authorities from HITS
	Authorities []MetricItem `json:"authorities,omitempty"`
}

// MetricItem represents a single metric value for an issue
type MetricItem struct {
	ID    string  `json:"id"`
	Value float64 `json:"value"`
}

// CurrentVersion is the schema version for new baselines
const CurrentVersion = 1

// DefaultFilename is the default baseline filename
const DefaultFilename = "baseline.json"

// DefaultPath returns the default baseline path for a project
func DefaultPath(projectDir string) string {
	return filepath.Join(projectDir, ".bv", DefaultFilename)
}

// Save writes the baseline atomically via temp file + rename in the target directory.
func (b *Baseline) Save(path string) error {
	if b == nil {
		return fmt.Errorf("encoding baseline: nil baseline")
	}

	// Ensure parent directory exists with private permissions
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".baseline-*.tmp")
	if err != nil {
		return fmt.Errorf("writing baseline: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	cleanup := func() {
		if !closed {
			_ = tmp.Close()
			closed = true
		}
		_ = os.Remove(tmpName)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("writing baseline: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("writing baseline: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("writing baseline: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing baseline: %w", err)
	}
	closed = true

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing baseline: %w", err)
	}

	return nil
}

// Load reads a baseline from a file
func Load(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no baseline found at %s", path)
		}
		return nil, fmt.Errorf("reading baseline: %w", err)
	}

	var baseline Baseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		return nil, fmt.Errorf("parsing baseline: %w", err)
	}

	return &baseline, nil
}

// Exists checks if a baseline file exists
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetGitInfo returns current git commit and branch info
func GetGitInfo(dir string) (sha, message, branch string) {
	// Get commit SHA
	if out, err := runGit(dir, "rev-parse", "HEAD"); err == nil {
		sha = strings.TrimSpace(out)
	}

	// Get commit message (first line)
	if out, err := runGit(dir, "log", "-1", "--format=%s"); err == nil {
		message = strings.TrimSpace(out)
	}

	// Get branch name
	if out, err := runGit(dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = strings.TrimSpace(out)
	}

	return sha, message, branch
}

// runGit runs a git command with sanitized ambient GIT_* variables and replace-refs disabled.
func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gitArgs := make([]string, 0, len(args)+1)
	gitArgs = append(gitArgs, "--no-replace-objects")
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = dir
	cmd.Env = loader.SanitizedGitEnv()
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Summary returns a human-readable summary of the baseline
func (b *Baseline) Summary() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Baseline created: %s\n", b.CreatedAt.Format(time.RFC1123)))

	if b.CommitSHA != "" {
		shortSHA := b.CommitSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		sb.WriteString(fmt.Sprintf("Commit: %s", shortSHA))
		if b.Branch != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", b.Branch))
		}
		sb.WriteString("\n")
		if b.CommitMessage != "" {
			sb.WriteString(fmt.Sprintf("Message: %s\n", b.CommitMessage))
		}
	}

	if b.Description != "" {
		sb.WriteString(fmt.Sprintf("Note: %s\n", b.Description))
	}

	sb.WriteString(fmt.Sprintf("\nGraph: %d nodes, %d edges (density: %.4f)\n",
		b.Stats.NodeCount, b.Stats.EdgeCount, b.Stats.Density))

	sb.WriteString(fmt.Sprintf("Status: %d open, %d blocked, %d closed\n",
		b.Stats.OpenCount, b.Stats.BlockedCount, b.Stats.ClosedCount))

	sb.WriteString(fmt.Sprintf("Actionable: %d | Cycles: %d\n",
		b.Stats.ActionableCount, b.Stats.CycleCount))

	for _, section := range []struct {
		header string
		items  []MetricItem
	}{
		{"Top PageRank:", b.TopMetrics.PageRank},
		{"Top Betweenness:", b.TopMetrics.Betweenness},
		{"Top Critical Path:", b.TopMetrics.CriticalPath},
		{"Top Hubs:", b.TopMetrics.Hubs},
		{"Top Authorities:", b.TopMetrics.Authorities},
	} {
		if len(section.items) == 0 {
			continue
		}
		sb.WriteString("\n" + section.header + "\n")
		for i, item := range section.items {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("  %s: %.4f\n", item.ID, item.Value))
		}
	}

	return sb.String()
}

// New creates a new baseline with the given stats and metrics for the current working directory.
func New(stats GraphStats, top TopMetrics, cycles [][]string, description string) *Baseline {
	return NewForDir(".", stats, top, cycles, description)
}

func baselineNow() time.Time {
	if raw := strings.TrimSpace(env.SourceDateEpoch.Get()); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec >= 0 && sec < 253402300800 {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now().UTC()
}

// NewForDir creates a new baseline resolving git commit/branch metadata from projectDir.
func NewForDir(projectDir string, stats GraphStats, top TopMetrics, cycles [][]string, description string) *Baseline {
	if projectDir == "" {
		projectDir = "."
	}
	sha, msg, branch := GetGitInfo(projectDir)

	return &Baseline{
		Version:       CurrentVersion,
		CreatedAt:     baselineNow(),
		CommitSHA:     sha,
		CommitMessage: msg,
		Branch:        branch,
		Description:   description,
		Stats:         stats,
		TopMetrics:    top,
		Cycles:        cycles,
	}
}
