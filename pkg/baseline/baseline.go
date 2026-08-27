// Package baseline provides storage and management for metrics snapshots.
// Baselines are used by the drift detection system to compare current
// state against a known-good reference point.
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

	// Stats contains the graph statistics snapshot
	Stats GraphStats `json:"stats"`

	// TopMetrics contains top-N items for key metrics
	TopMetrics TopMetrics `json:"top_metrics"`

	// Cycles stores one representative cycle per detected cyclic component.
	Cycles [][]string `json:"cycles,omitempty"`
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

// Save writes the baseline to a file
func (b *Baseline) Save(path string) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Marshal with indentation for readability
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding baseline: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
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

// runGit runs a git command and returns output
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
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

	if len(b.TopMetrics.PageRank) > 0 {
		sb.WriteString("\nTop PageRank:\n")
		for i, item := range b.TopMetrics.PageRank {
			if i >= 5 {
				break
			}
			sb.WriteString(fmt.Sprintf("  %s: %.4f\n", item.ID, item.Value))
		}
	}

	return sb.String()
}

// New creates a new baseline with the given stats and metrics
func New(stats GraphStats, top TopMetrics, cycles [][]string, description string) *Baseline {
	sha, msg, branch := GetGitInfo(".")

	return &Baseline{
		Version:       CurrentVersion,
		CreatedAt:     time.Now(),
		CommitSHA:     sha,
		CommitMessage: msg,
		Branch:        branch,
		Description:   description,
		Stats:         stats,
		TopMetrics:    top,
		Cycles:        cycles,
	}
}
