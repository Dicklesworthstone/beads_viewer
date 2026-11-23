// Package export provides data export functionality for bv.
//
// This file defines the SQLite export data structures following mcp_agent_mail's
// proven architecture for client-side sql.js WASM querying.
package export

import (
	"encoding/json"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ExportIssue extends model.Issue with computed graph metrics for export.
// The client builds search text on load by concatenating fields.
type ExportIssue struct {
	// Core issue data (embedded)
	ID                 string          `json:"id"`
	Title              string          `json:"title"`
	Description        string          `json:"description,omitempty"`
	Design             string          `json:"design,omitempty"`
	AcceptanceCriteria string          `json:"acceptance_criteria,omitempty"`
	Notes              string          `json:"notes,omitempty"`
	Status             model.Status    `json:"status"`
	Priority           int             `json:"priority"`
	IssueType          model.IssueType `json:"issue_type"`
	Assignee           string          `json:"assignee,omitempty"`
	Labels             []string        `json:"labels,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	ClosedAt           *time.Time      `json:"closed_at,omitempty"`
	DueDate            *time.Time      `json:"due_date,omitempty"`
	DeferUntil         *time.Time      `json:"defer_until,omitempty"`
	ExternalRef        string          `json:"external_ref,omitempty"`
	EstimatedMinutes   *int            `json:"estimated_minutes,omitempty"`
	CreatedBy          string          `json:"created_by,omitempty"`
	CloseReason        string          `json:"close_reason,omitempty"`
	SourceRepo         string          `json:"source_repo,omitempty"`

	// Computed graph metrics
	PageRank        float64  `json:"pagerank"`
	Betweenness     float64  `json:"betweenness,omitempty"`
	Eigenvector     float64  `json:"eigenvector,omitempty"`
	CriticalPath    int      `json:"critical_path_depth"`
	TriageScore     float64  `json:"triage_score,omitempty"`
	BlocksCount     int      `json:"blocks_count"`
	BlockedByCount  int      `json:"blocked_by_count"`
	BlocksIDs       []string `json:"blocks_ids,omitempty"`
	BlockedByIDs    []string `json:"blocked_by_ids,omitempty"`
	ETADays         *float64 `json:"eta_days,omitempty"`
	ETADate         string   `json:"eta_date,omitempty"`
	ETAConfidence   *float64 `json:"eta_confidence,omitempty"`
	RiskScore       float64  `json:"risk_score,omitempty"`
	RiskExplanation string   `json:"risk_explanation,omitempty"`
}

// ExportDependency represents a dependency relationship for export.
type ExportDependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// ExportMetrics holds computed metrics for a single issue.
type ExportMetrics struct {
	IssueID        string  `json:"issue_id"`
	PageRank       float64 `json:"pagerank"`
	Betweenness    float64 `json:"betweenness"`
	Eigenvector    float64 `json:"eigenvector"`
	CriticalPath   int     `json:"critical_path_depth"`
	TriageScore    float64 `json:"triage_score"`
	BlocksCount    int     `json:"blocks_count"`
	BlockedByCount int     `json:"blocked_by_count"`
}

// ExportMeta contains metadata about the export.
type ExportMeta struct {
	Version        string    `json:"version"`
	GeneratedAt    time.Time `json:"generated_at"`
	GitCommit      string    `json:"git_commit,omitempty"`
	IssueCount     int       `json:"issue_count"`
	DepCount       int       `json:"dependency_count"`
	DataHash       string    `json:"data_hash,omitempty"`
	Title          string    `json:"title,omitempty"`
	HasHybridWasm  bool      `json:"has_hybrid_wasm"`
	HasSprints     bool      `json:"has_sprints,omitempty"`
	HasSuggestions bool      `json:"has_suggestions,omitempty"`
	HasHistory     bool      `json:"has_history,omitempty"`
	HasLabels      bool      `json:"has_labels,omitempty"`
	HasForecast    bool      `json:"has_forecast,omitempty"`
}

// SQLiteExportConfig configures the SQLite export process.
type SQLiteExportConfig struct {
	// OutputDir is the directory to write export files
	OutputDir string

	// Title is a custom title for the static site
	Title string

	// ChunkThreshold is the file size (bytes) above which to chunk the database
	// Default: 5MB
	ChunkThreshold int64

	// ChunkSize is the size of each chunk when chunking is needed
	// Default: 1MB (optimal for httpvfs range requests)
	ChunkSize int64

	// IncludeRobotOutputs determines whether to write JSON robot outputs
	IncludeRobotOutputs bool
	// RobotEnvelope carries source completeness and scope metadata into every
	// exported robot JSON artifact without changing its existing payload shape.
	RobotEnvelope map[string]json.RawMessage
	// Readiness retains the full source independently of visible export rows.
	// When nil, Export derives it from the supplied Issues and Deps.
	Readiness *model.ReadinessIndex
	// ReadinessAt is the snapshot clock for deferral; zero uses export time.
	ReadinessAt time.Time

	// PageSize is the SQLite page size (optimal: 1024 for httpvfs)
	PageSize int

	// HasSprints records whether data/sprints.json will be published alongside this bundle.
	HasSprints bool
	// HasSuggestions records whether data/suggestions.json will be published alongside this bundle.
	HasSuggestions bool
	// HasHistory records whether data/history.json will be published alongside this bundle.
	HasHistory bool
	// HasLabels records whether data/labels.json will be published alongside this bundle.
	HasLabels bool
	// HasForecast records whether data/forecast.json will be published alongside this bundle.
	HasForecast bool
	// ETAContext optionally supplies a velocity-calibrated ETA context computed over
	// full-graph velocity issues + matching stats (preserving identical-units scaling
	// when --pages-include-closed=false or scoped recipes omit closed issues).
	ETAContext *analysis.ETAContext
}

// DefaultSQLiteExportConfig returns sensible defaults for export configuration.
func DefaultSQLiteExportConfig() SQLiteExportConfig {
	return SQLiteExportConfig{
		ChunkThreshold:      5 * 1024 * 1024, // 5MB
		ChunkSize:           1 * 1024 * 1024, // 1MB
		IncludeRobotOutputs: true,
		PageSize:            1024,
	}
}

// ChunkConfig describes how a large database was chunked.
type ChunkConfig struct {
	Chunked    bool        `json:"chunked"`
	ChunkCount int         `json:"chunk_count"`
	ChunkSize  int64       `json:"chunk_size"`
	TotalSize  int64       `json:"total_size"`
	Hash       string      `json:"hash,omitempty"`
	Chunks     []ChunkInfo `json:"chunks,omitempty"`
}

// ChunkInfo describes an individual chunk file.
type ChunkInfo struct {
	Path string `json:"path"`
	Hash string `json:"hash,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// TriageRecommendation represents a single triage recommendation for export.
type TriageRecommendation struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	IssueType    string   `json:"issue_type"`
	Status       string   `json:"status"`
	Priority     int      `json:"priority"`
	TriageScore  float64  `json:"triage_score"`
	BlocksCount  int      `json:"blocks_count"`
	Action       string   `json:"action"`
	Reasons      []string `json:"reasons"`
	UnblocksIDs  []string `json:"unblocks_ids,omitempty"`
	BlockedByIDs []string `json:"blocked_by_ids,omitempty"`
}

// QuickWin represents a quick win item from triage.
type QuickWin struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Score       float64  `json:"score"`
	Reason      string   `json:"reason"`
	UnblocksIDs []string `json:"unblocks_ids,omitempty"`
}

// ProjectHealth contains project health statistics.
type ProjectHealth struct {
	OpenCount       int            `json:"open_count"`
	ClosedCount     int            `json:"closed_count"`
	BlockedCount    int            `json:"blocked_count"`
	InProgressCount int            `json:"in_progress_count"`
	ActionableCount int            `json:"actionable_count"`
	ByStatus        map[string]int `json:"by_status"`
	ByType          map[string]int `json:"by_type"`
	ByPriority      map[int]int    `json:"by_priority"`
	CycleCount      int            `json:"cycle_count,omitempty"`
	Velocity        *Velocity      `json:"velocity,omitempty"`
}

// Velocity captures project-level throughput stats for exports/robots.
type Velocity struct {
	ClosedLast7Days  int            `json:"closed_last_7_days"`
	ClosedLast30Days int            `json:"closed_last_30_days"`
	AvgDaysToClose   float64        `json:"avg_days_to_close"`
	Weekly           []VelocityWeek `json:"weekly,omitempty"`
	Estimated        bool           `json:"estimated,omitempty"`
}

// VelocityWeek holds weekly bucketed closure counts (Monday-start UTC).
type VelocityWeek struct {
	WeekStart time.Time `json:"week_start"`
	Closed    int       `json:"closed"`
}
