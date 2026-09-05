package export

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

//go:embed force-graph.min.js
var forceGraphJS string

//go:embed marked.min.js
var markedJS string

// InteractiveGraphOptions configures HTML graph generation
type InteractiveGraphOptions struct {
	Issues        []model.Issue
	Stats         *analysis.GraphStats
	Triage        *analysis.TriageResult     // Full triage output for display
	History       *correlation.HistoryReport // Git history correlation data
	Title         string
	DataHash      string
	Path          string // Output path - if empty, auto-generates based on project
	ProjectName   string // Project name for auto-naming
	RobotEnvelope map[string]json.RawMessage
}

// graphNode represents a node in the interactive graph with full bead data
type graphNode struct {
	// Identity
	ID    string `json:"id"`
	Title string `json:"title"`

	// Full content for hover panel (markdown)
	Description        string `json:"description,omitempty"`
	Design             string `json:"design,omitempty"`
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`
	Notes              string `json:"notes,omitempty"`

	// Metadata
	Status   string   `json:"status"`
	Priority int      `json:"priority"`
	Type     string   `json:"type"`
	Labels   []string `json:"labels"`
	Assignee string   `json:"assignee,omitempty"`

	// Timestamps
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	ClosedAt  string `json:"closed_at,omitempty"`
	DueDate   string `json:"due_date,omitempty"`

	// Dependencies
	BlockedBy []string `json:"blocked_by,omitempty"`
	Blocks    []string `json:"blocks,omitempty"`

	// Git history correlation
	CommitCount int                            `json:"commit_count,omitempty"`
	LastAuthor  string                         `json:"last_author,omitempty"`
	Commits     []correlation.CorrelatedCommit `json:"commits,omitempty"`

	// Graph metrics
	PageRank        float64 `json:"pagerank"`
	Betweenness     float64 `json:"betweenness"`
	Eigenvector     float64 `json:"eigenvector"`
	Hub             float64 `json:"hub"`
	Authority       float64 `json:"authority"`
	CriticalPath    float64 `json:"critical_path"`
	InDegree        int     `json:"in_degree"`
	OutDegree       int     `json:"out_degree"`
	CoreNumber      int     `json:"core_number"`
	Slack           float64 `json:"slack"`
	IsArticulation  bool    `json:"is_articulation"`
	PageRankRank    int     `json:"pagerank_rank"`
	BetweennessRank int     `json:"betweenness_rank"`
}

// graphLink represents an edge in the interactive graph
type graphLink struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	Type     string `json:"type"`
	Critical bool   `json:"critical"`
}

// GenerateInteractiveGraphFilename creates an auto-generated filename
// Format: {project}_graph_export__as_of__YYYY_MM_DD__HH_MM__git_head_hash__{gitshort}.html
func GenerateInteractiveGraphFilename(projectName string) string {
	now := time.Now()
	dateStr := now.Format("2006_01_02")
	timeStr := now.Format("15_04")

	// Get short git commit hash
	gitShort := "nogit"
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	if output, err := cmd.Output(); err == nil {
		gitShort = strings.TrimSpace(string(output))
	}

	// Clean project name
	safeName := strings.ReplaceAll(projectName, " ", "_")
	safeName = strings.ReplaceAll(safeName, "/", "_")

	return fmt.Sprintf("%s_graph_export__as_of__%s__%s__git_head_hash__%s.html", safeName, dateStr, timeStr, gitShort)
}

// GenerateInteractiveGraphHTML creates a self-contained HTML file with force-graph visualization
func GenerateInteractiveGraphHTML(opts InteractiveGraphOptions) (string, error) {
	if len(opts.Issues) == 0 {
		return "", fmt.Errorf("no issues to export")
	}

	// Build graph data with all metrics
	nodes := make([]graphNode, 0, len(opts.Issues))
	links := make([]graphLink, 0)

	// Create issue map for dependency lookup
	issueMap := make(map[string]bool)
	for _, iss := range opts.Issues {
		issueMap[iss.ID] = true
	}

	// Get all metrics if available
	var pageRank, betweenness, eigenvector, hubs, authorities, criticalPath, slack map[string]float64
	var coreNumber map[string]int
	var articulation []string
	var pageRankRank, betweennessRank map[string]int
	var inDegree, outDegree map[string]int

	if opts.Stats != nil {
		pageRank = opts.Stats.PageRank()
		betweenness = opts.Stats.Betweenness()
		eigenvector = opts.Stats.Eigenvector()
		hubs = opts.Stats.Hubs()
		authorities = opts.Stats.Authorities()
		criticalPath = opts.Stats.CriticalPathScore()
		slack = opts.Stats.Slack()
		coreNumber = opts.Stats.CoreNumber()
		articulation = opts.Stats.ArticulationPoints()
		pageRankRank = opts.Stats.PageRankRank()
		betweennessRank = opts.Stats.BetweennessRank()
		inDegree = opts.Stats.InDegree
		outDegree = opts.Stats.OutDegree
	}

	// Create articulation set for O(1) lookup
	articulationSet := make(map[string]bool)
	for _, id := range articulation {
		articulationSet[id] = true
	}

	// Build reverse dependency map (who blocks who)
	blocksMap := make(map[string][]string)
	for _, iss := range opts.Issues {
		for _, dep := range iss.Dependencies {
			if dep != nil && issueMap[dep.DependsOnID] && dep.Type.IsBlocking() {
				blocksMap[dep.DependsOnID] = append(blocksMap[dep.DependsOnID], iss.ID)
			}
		}
	}

	// Build nodes with full bead data
	for _, iss := range opts.Issues {
		// Compute blocked_by list
		var blockedBy []string
		for _, dep := range iss.Dependencies {
			if dep != nil && issueMap[dep.DependsOnID] && dep.Type.IsBlocking() {
				blockedBy = append(blockedBy, dep.DependsOnID)
			}
		}

		// Format timestamps
		createdAt := ""
		if !iss.CreatedAt.IsZero() {
			createdAt = iss.CreatedAt.Format("2006-01-02 15:04")
		}
		updatedAt := ""
		if !iss.UpdatedAt.IsZero() {
			updatedAt = iss.UpdatedAt.Format("2006-01-02 15:04")
		}
		closedAt := ""
		if iss.ClosedAt != nil {
			closedAt = iss.ClosedAt.Format("2006-01-02 15:04")
		}
		dueDate := ""
		if iss.DueDate != nil {
			dueDate = iss.DueDate.Format("2006-01-02")
		}

		// Get history data if available
		var commits []correlation.CorrelatedCommit
		var lastAuthor string
		commitCount := 0
		if opts.History != nil {
			if history, ok := opts.History.Histories[iss.ID]; ok {
				commits = history.Commits
				commitCount = len(commits)
				lastAuthor = history.LastAuthor
			}
		}

		node := graphNode{
			// Identity
			ID:    iss.ID,
			Title: iss.Title,

			// Full content for hover panel
			Description:        iss.Description,
			Design:             iss.Design,
			AcceptanceCriteria: iss.AcceptanceCriteria,
			Notes:              iss.Notes,

			// Metadata
			Status:   string(iss.Status),
			Priority: iss.Priority,
			Type:     string(iss.IssueType),
			Labels:   iss.Labels,
			Assignee: iss.Assignee,

			// Timestamps
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			ClosedAt:  closedAt,
			DueDate:   dueDate,

			// Dependencies
			BlockedBy: blockedBy,
			Blocks:    blocksMap[iss.ID],

			// Git history
			CommitCount: commitCount,
			LastAuthor:  lastAuthor,
			Commits:     commits,

			// Graph metrics
			PageRank:        pageRank[iss.ID],
			Betweenness:     betweenness[iss.ID],
			Eigenvector:     eigenvector[iss.ID],
			Hub:             hubs[iss.ID],
			Authority:       authorities[iss.ID],
			CriticalPath:    criticalPath[iss.ID],
			InDegree:        inDegree[iss.ID],
			OutDegree:       outDegree[iss.ID],
			CoreNumber:      coreNumber[iss.ID],
			Slack:           slack[iss.ID],
			IsArticulation:  articulationSet[iss.ID],
			PageRankRank:    pageRankRank[iss.ID],
			BetweennessRank: betweennessRank[iss.ID],
		}
		nodes = append(nodes, node)

		// Build links from dependencies
		for _, dep := range iss.Dependencies {
			if dep == nil || !issueMap[dep.DependsOnID] {
				continue
			}
			// Only mark as critical if we have stats AND both ends have zero slack
			isCritical := opts.Stats != nil && slack[iss.ID] == 0 && slack[dep.DependsOnID] == 0
			link := graphLink{
				Source:   iss.ID,
				Target:   dep.DependsOnID,
				Type:     string(dep.Type),
				Critical: isCritical,
			}
			links = append(links, link)
		}
	}

	// Sort nodes by ID for determinism
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].ID < nodes[j].ID
	})

	graphData := map[string]interface{}{
		"nodes": nodes,
		"links": links,
	}
	for key, value := range opts.RobotEnvelope {
		graphData[key] = value
	}

	// Add triage data if available
	if opts.Triage != nil {
		graphData["triage"] = opts.Triage
	}

	// Add history stats if available
	if opts.History != nil {
		graphData["history_stats"] = opts.History.Stats
		graphData["git_range"] = opts.History.GitRange
	}

	dataJSON, err := json.Marshal(graphData)
	if err != nil {
		return "", fmt.Errorf("marshal graph data: %w", err)
	}

	title := opts.Title
	if title == "" {
		title = "Dependency Graph"
	}

	// Generate filename if not provided
	outputPath := opts.Path
	if outputPath == "" {
		projectName := opts.ProjectName
		if projectName == "" {
			projectName = "graph"
		}
		outputPath = GenerateInteractiveGraphFilename(projectName)
	}

	// Ensure .html extension
	if !strings.HasSuffix(strings.ToLower(outputPath), ".html") {
		outputPath = strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".html"
	}

	html := generateUltimateHTML(title, opts.DataHash, string(dataJSON), len(nodes), len(links), opts.ProjectName, forceGraphJS, markedJS)

	// Ensure directory exists
	dir := filepath.Dir(outputPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create dir: %w", err)
		}
	}

	if err := os.WriteFile(outputPath, []byte(html), 0644); err != nil {
		return "", err
	}

	return outputPath, nil
}
