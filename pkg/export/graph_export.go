package export

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// GraphExportFormat specifies the output format for graph export.
type GraphExportFormat string

const (
	GraphFormatJSON    GraphExportFormat = "json"
	GraphFormatDOT     GraphExportFormat = "dot"
	GraphFormatMermaid GraphExportFormat = "mermaid"
)

// GraphExportConfig configures graph export behavior.
type GraphExportConfig struct {
	Format    GraphExportFormat     // Output format (json, dot, mermaid)
	Label     string                // Filter to specific label
	Root      string                // Subgraph from specific root
	Depth     int                   // Max depth for subgraph (0 = unlimited)
	DataHash  string                // Hash of input data for provenance
	Readiness *model.ReadinessIndex // Optional full-source readiness index (falls back to issues when nil)
}

// GraphExportResult contains the exported graph and metadata.
type GraphExportResult struct {
	Format         string            `json:"format"`
	Graph          string            `json:"graph,omitempty"`
	Nodes          int               `json:"nodes"`
	Edges          int               `json:"edges"`
	FiltersApplied map[string]string `json:"filters_applied,omitempty"`
	Explanation    GraphExplanation  `json:"explanation"`
	DataHash       string            `json:"data_hash,omitempty"`
	Adjacency      *AdjacencyGraph   `json:"adjacency,omitempty"`
}

// GraphExplanation provides context for AI agents.
type GraphExplanation struct {
	What        string `json:"what"`
	HowToRender string `json:"how_to_render,omitempty"`
	WhenToUse   string `json:"when_to_use"`
}

// AdjacencyGraph is the JSON adjacency list representation.
type AdjacencyGraph struct {
	Nodes []AdjacencyNode `json:"nodes"`
	Edges []AdjacencyEdge `json:"edges"`
}

// AdjacencyNode represents a node in the adjacency graph.
type AdjacencyNode struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Priority int      `json:"priority"`
	Labels   []string `json:"labels,omitempty"`
	PageRank float64  `json:"pagerank,omitempty"`
}

// AdjacencyEdge represents an edge in the adjacency graph.
type AdjacencyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"` // source dependency type: "blocks", "related", "parent-child", or "discovered-from"
}

// ExportGraph exports the dependency graph in the specified format.
func ExportGraph(issues []model.Issue, stats *analysis.GraphStats, config GraphExportConfig) (*GraphExportResult, error) {
	// Filter issues if needed
	filteredIssues := filterIssues(issues, config)

	if len(filteredIssues) == 0 {
		return &GraphExportResult{
			Format: string(config.Format),
			Nodes:  0,
			Edges:  0,
			Explanation: GraphExplanation{
				What:      "Empty graph - no issues match the filter criteria",
				WhenToUse: "Adjust filter parameters to include more issues",
			},
		}, nil
	}

	// Build issue ID set for edge filtering
	issueIDs := make(map[string]bool, len(filteredIssues))
	for _, i := range filteredIssues {
		issueIDs[i.ID] = true
	}

	// Count edges
	edgeCount := 0
	for _, i := range filteredIssues {
		for _, dep := range i.Dependencies {
			if dep != nil && issueIDs[dep.DependsOnID] {
				edgeCount++
			}
		}
	}

	// Build filters applied
	filtersApplied := make(map[string]string)
	if config.Label != "" {
		filtersApplied["label"] = config.Label
	}
	if config.Root != "" {
		filtersApplied["root"] = config.Root
	}
	if config.Depth > 0 {
		filtersApplied["depth"] = fmt.Sprintf("%d", config.Depth)
	}

	result := &GraphExportResult{
		Format:         string(config.Format),
		Nodes:          len(filteredIssues),
		Edges:          edgeCount,
		FiltersApplied: filtersApplied,
		DataHash:       config.DataHash,
	}

	readiness := config.Readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}

	switch config.Format {
	case GraphFormatDOT:
		graph := generateDOTWithReadiness(filteredIssues, issueIDs, stats, readiness)
		result.Graph = graph
		result.Explanation = GraphExplanation{
			What:        "Dependency graph in Graphviz DOT format",
			HowToRender: "Save to file.dot, run: dot -Tpng file.dot -o graph.png",
			WhenToUse:   "When you need a visual overview of dependencies for documentation or debugging",
		}

	case GraphFormatMermaid:
		graph := GenerateMermaidGraph(filteredIssues, issueIDs, MermaidConfig{
			ShowNoDependenciesNode: false,
			Readiness:              readiness,
		})
		result.Graph = graph
		result.Explanation = GraphExplanation{
			What:        "Dependency graph in Mermaid diagram format",
			HowToRender: "Paste into any Markdown renderer that supports Mermaid, or use mermaid.live",
			WhenToUse:   "When you need an embeddable diagram for documentation or GitHub issues",
		}

	case GraphFormatJSON:
		fallthrough
	default:
		result.Format = "json"
		adjacency := generateAdjacency(filteredIssues, issueIDs, stats)
		result.Adjacency = adjacency
		result.Explanation = GraphExplanation{
			What:      "Dependency graph as JSON adjacency list",
			WhenToUse: "When you need programmatic access to the graph structure",
		}
	}

	return result, nil
}

// filterIssues applies label and root filters to the issue list.
func filterIssues(issues []model.Issue, config GraphExportConfig) []model.Issue {
	// Filter by label first
	filtered := issues
	if config.Label != "" {
		var labeled []model.Issue
		for _, i := range issues {
			for _, l := range i.Labels {
				if strings.EqualFold(l, config.Label) {
					labeled = append(labeled, i)
					break
				}
			}
		}
		filtered = labeled
	}

	// Filter by root (subgraph from root)
	if config.Root != "" {
		filtered = extractSubgraph(filtered, config.Root, config.Depth)
	}

	return filtered
}

// extractSubgraph extracts a subgraph starting from a root node.
func extractSubgraph(issues []model.Issue, rootID string, maxDepth int) []model.Issue {
	// Build issue map
	issueMap := make(map[string]model.Issue, len(issues))
	for _, i := range issues {
		issueMap[i.ID] = i
	}

	// Build reverse dependency map so focused exports include both sides of the
	// root: the prerequisites it depends on and the downstream issues it unblocks.
	dependentsByID := make(map[string][]string, len(issues))
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if _, ok := issueMap[dep.DependsOnID]; !ok {
				continue
			}
			dependentsByID[dep.DependsOnID] = append(dependentsByID[dep.DependsOnID], issue.ID)
		}
	}

	// BFS to find nodes reachable in either dependency direction.
	visited := make(map[string]bool)
	queue := []struct {
		id    string
		depth int
	}{{rootID, 0}}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if visited[curr.id] {
			continue
		}
		if maxDepth > 0 && curr.depth > maxDepth {
			continue
		}
		visited[curr.id] = true

		issue, ok := issueMap[curr.id]
		if !ok {
			continue
		}

		// Add dependencies to queue
		for _, dep := range issue.Dependencies {
			if dep != nil && !visited[dep.DependsOnID] {
				queue = append(queue, struct {
					id    string
					depth int
				}{dep.DependsOnID, curr.depth + 1})
			}
		}
		for _, dependentID := range dependentsByID[curr.id] {
			if !visited[dependentID] {
				queue = append(queue, struct {
					id    string
					depth int
				}{dependentID, curr.depth + 1})
			}
		}
	}

	// Collect visited issues
	var result []model.Issue
	for _, i := range issues {
		if visited[i.ID] {
			result = append(result, i)
		}
	}
	return result
}

// generateDOT creates a Graphviz DOT format graph.
func generateDOT(issues []model.Issue, issueIDs map[string]bool, stats *analysis.GraphStats) string {
	return generateDOTWithReadiness(issues, issueIDs, stats, nil)
}

func generateDOTWithReadiness(issues []model.Issue, issueIDs map[string]bool, stats *analysis.GraphStats, readiness *model.ReadinessIndex) string {
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}
	var sb strings.Builder

	sb.WriteString("digraph G {\n")
	sb.WriteString("    rankdir=LR;\n")
	sb.WriteString("    node [shape=box, fontname=\"Helvetica\", fontsize=10];\n")
	sb.WriteString("    edge [fontname=\"Helvetica\", fontsize=8];\n")
	sb.WriteString("\n")

	// Get PageRank for node sizing
	var pageRank map[string]float64
	if stats != nil {
		pageRank = stats.PageRank()
	}

	// Sort issues for deterministic output
	sortedIssues := make([]model.Issue, len(issues))
	copy(sortedIssues, issues)
	sort.Slice(sortedIssues, func(i, j int) bool {
		return sortedIssues[i].ID < sortedIssues[j].ID
	})

	// Nodes
	for _, i := range sortedIssues {
		// Truncate title first (runes) to avoid splitting UTF-8 sequences
		rawTitle := truncateRunes(i.Title, 30)

		title := escapeDOTString(rawTitle)
		escapedID := escapeDOTString(i.ID)

		// Status color (dependency-blocked active issues render with blocked red)
		depBlocked := !isClosedLikeStatus(i.Status) &&
			(i.Status == model.StatusOpen || i.Status == model.StatusInProgress || i.Status == "") &&
			len(readiness.Blockers(i.ID)) > 0
		color := dotStatusColor(i.Status)
		if depBlocked {
			color = "#FFCDD2"
		}

		// Label with ID, title, priority
		label := fmt.Sprintf("%s\\n%s\\nP%d %s", escapedID, title, i.Priority, i.Status)

		// PageRank affects penwidth
		penwidth := 1.0
		if pageRank != nil {
			if pr, ok := pageRank[i.ID]; ok && pr > 0 {
				penwidth = 1.0 + pr*3.0 // Scale for visibility
			}
		}

		sb.WriteString(fmt.Sprintf("    \"%s\" [label=\"%s\", fillcolor=\"%s\", style=filled, penwidth=%.1f];\n",
			sanitizeDOTID(i.ID), label, color, penwidth))
	}

	sb.WriteString("\n")

	// Edges
	for _, i := range sortedIssues {
		// Sort dependencies for deterministic output
		deps := make([]*model.Dependency, len(i.Dependencies))
		copy(deps, i.Dependencies)
		sort.Slice(deps, func(a, b int) bool {
			if deps[a] == nil {
				return false
			}
			if deps[b] == nil {
				return true
			}
			return deps[a].DependsOnID < deps[b].DependsOnID
		})

		for _, dep := range deps {
			if dep == nil || !issueIDs[dep.DependsOnID] {
				continue
			}

			style := "dashed"
			color := "#999999"
			if dep.Type.IsBlocking() {
				style = "bold"
				color = "#E53935" // Red for blocking
			}

			sb.WriteString(fmt.Sprintf("    \"%s\" -> \"%s\" [style=%s, color=\"%s\"];\n",
				sanitizeDOTID(i.ID), sanitizeDOTID(dep.DependsOnID), style, color))
		}
	}

	sb.WriteString("}\n")
	return sb.String()
}

// dotStatusColor returns a DOT-compatible color for a status.
func dotStatusColor(status model.Status) string {
	switch {
	case isClosedLikeStatus(status):
		return "#CFD8DC" // Light gray
	case status == model.StatusOpen || status == "":
		return "#C8E6C9" // Light green
	case status == model.StatusInProgress:
		return "#BBDEFB" // Light blue
	case status == model.StatusBlocked:
		return "#FFCDD2" // Light red
	case status == model.StatusDeferred:
		return "#FFE0B2" // Soft amber
	case status == model.StatusPinned:
		return "#E1BEE7" // Soft lavender
	case status == model.StatusHooked:
		return "#F8BBD0" // Soft pink
	case status == model.StatusReview:
		return "#FFF9C4" // Soft yellow
	default:
		return "#E2E8F0" // Slate custom status
	}
}

// sanitizeDOTID ensures an ID is valid for DOT format.
func sanitizeDOTID(id string) string {
	return escapeDOTString(id)
}

func escapeDOTString(s string) string {
	// DOT string literals need backslashes and quotes escaped; normalize newlines.
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(s)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// generateMermaid creates a Mermaid diagram format graph.
func generateMermaid(issues []model.Issue, issueIDs map[string]bool) string {
	return GenerateMermaidGraph(issues, issueIDs, MermaidConfig{
		ShowNoDependenciesNode: false,
	})
}

// generateAdjacency creates a JSON adjacency list representation.
func generateAdjacency(issues []model.Issue, issueIDs map[string]bool, stats *analysis.GraphStats) *AdjacencyGraph {
	// Get PageRank
	var pageRank map[string]float64
	if stats != nil {
		pageRank = stats.PageRank()
	}

	// Sort issues for deterministic output
	sortedIssues := make([]model.Issue, len(issues))
	copy(sortedIssues, issues)
	sort.Slice(sortedIssues, func(i, j int) bool {
		return sortedIssues[i].ID < sortedIssues[j].ID
	})

	// Build nodes
	nodes := make([]AdjacencyNode, 0, len(sortedIssues))
	for _, i := range sortedIssues {
		node := AdjacencyNode{
			ID:       i.ID,
			Title:    i.Title,
			Status:   string(i.Status),
			Priority: i.Priority,
			Labels:   i.Labels,
		}
		if pageRank != nil {
			if pr, ok := pageRank[i.ID]; ok {
				node.PageRank = pr
			}
		}
		nodes = append(nodes, node)
	}

	// Build edges
	var edges []AdjacencyEdge
	for _, i := range sortedIssues {
		// Sort dependencies
		deps := make([]*model.Dependency, len(i.Dependencies))
		copy(deps, i.Dependencies)
		sort.Slice(deps, func(a, b int) bool {
			if deps[a] == nil {
				return false
			}
			if deps[b] == nil {
				return true
			}
			return deps[a].DependsOnID < deps[b].DependsOnID
		})

		for _, dep := range deps {
			if dep == nil || !issueIDs[dep.DependsOnID] {
				continue
			}

			// Preserve the source dependency type so structural audits can
			// distinguish parent-child / discovered-from from true `related`
			// edges. An empty/unset Type is the legacy default for a blocking
			// edge (see DependencyType.IsBlocking), so normalize it to "blocks".
			edgeType := string(dep.Type)
			if edgeType == "" {
				edgeType = string(model.DepBlocks)
			}

			edges = append(edges, AdjacencyEdge{
				From: i.ID,
				To:   dep.DependsOnID,
				Type: edgeType,
			})
		}
	}

	return &AdjacencyGraph{
		Nodes: nodes,
		Edges: edges,
	}
}

// GraphExportResultJSON returns the result as JSON bytes.
func (r *GraphExportResult) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
