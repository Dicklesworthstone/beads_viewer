package export

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func loadEmbeddedVendorScript(name string) (string, error) {
	raw, err := ViewerAssetsFS.ReadFile("viewer_assets/vendor/" + name)
	if err != nil {
		return "", fmt.Errorf("read embedded vendor script %s: %w", name, err)
	}
	return string(raw), nil
}

func resolveInteractiveExportTime(exportedAt time.Time) time.Time {
	if !exportedAt.IsZero() {
		return exportedAt
	}
	if ts, ok := sourceDateEpochTime(); ok {
		return ts
	}
	return time.Now()
}

// InteractiveGraphOptions configures HTML graph generation
type InteractiveGraphOptions struct {
	Issues        []model.Issue
	Stats         *analysis.GraphStats
	Readiness     *model.ReadinessIndex      // Full-source readiness index (preserved across recipe filters)
	Triage        *analysis.TriageResult     // Full triage output for display
	History       *correlation.HistoryReport // Git history correlation data
	Title         string
	DataHash      string
	Path          string // Output path - if empty, auto-generates based on project
	ProjectName   string // Project name for auto-naming
	ExportedAt    time.Time
	GitHash       string
	WorkDir       string
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
	Status          string   `json:"status"`
	Priority        int      `json:"priority"`
	Type            string   `json:"type"`
	Labels          []string `json:"labels"`
	Assignee        string   `json:"assignee,omitempty"`
	SourceRepo      string   `json:"source_repo,omitempty"`
	IsActionable    bool     `json:"is_actionable"`
	DependencyState string   `json:"dependency_state"`
	BlockerCount    int      `json:"blocker_count"`
	IsTemplate      bool     `json:"is_template,omitempty"`
	Pinned          bool     `json:"pinned,omitempty"`
	Ephemeral       bool     `json:"ephemeral,omitempty"`

	// Timestamps & external reference
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	ClosedAt    string `json:"closed_at,omitempty"`
	DueDate     string `json:"due_date,omitempty"`
	DeferUntil  string `json:"defer_until,omitempty"`
	IsDeferred  bool   `json:"is_deferred,omitempty"`
	ExternalRef string `json:"external_ref,omitempty"`

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
	SlackAvailable  bool    `json:"slack_available"`
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
	return GenerateInteractiveGraphFilenameWithOpts(projectName, resolveInteractiveExportTime(time.Time{}), "", "")
}

func isHexGitHash(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func sanitizeGraphFilenameToken(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// GenerateInteractiveGraphFilenameWithOpts creates an auto-generated filename using
// an explicit export timestamp, git working directory, and optional pre-resolved git hash.
func GenerateInteractiveGraphFilenameWithOpts(projectName string, exportTime time.Time, workDir, gitHash string) string {
	exportTime = resolveInteractiveExportTime(exportTime)
	dateStr := exportTime.Format("2006_01_02")
	timeStr := exportTime.Format("15_04")

	// Get short git commit hash
	gitShort := strings.TrimSpace(gitHash)
	if gitShort == "" {
		gitShort = "nogit"
		cmd := exec.Command("git", "--no-replace-objects", "rev-parse", "--short", "HEAD")
		cmd.Env = loader.SanitizedGitEnv()
		if trimmedDir := strings.TrimSpace(workDir); trimmedDir != "" {
			cmd.Dir = trimmedDir
		}
		if output, err := cmd.Output(); err == nil {
			if parsed := strings.TrimSpace(string(output)); parsed != "" {
				gitShort = parsed
			}
		}
	}
	if len(gitShort) > 12 && isHexGitHash(gitShort) {
		gitShort = gitShort[:8]
	}
	gitShort = sanitizeGraphFilenameToken(gitShort)
	if gitShort == "" {
		gitShort = "nogit"
	}

	// Clean project name
	safeName := sanitizeGraphFilenameToken(projectName)

	return fmt.Sprintf("%s_graph_export__as_of__%s__%s__git_head_hash__%s.html", safeName, dateStr, timeStr, gitShort)
}

func generateInteractiveGraphFilenameAt(projectName string, exportTime time.Time) string {
	return GenerateInteractiveGraphFilenameWithOpts(projectName, exportTime, "", "")
}

func hasClosedOrTombstoneIssues(issues []model.Issue) bool {
	for i := range issues {
		if issues[i].Status.IsClosed() || issues[i].Status.IsTombstone() {
			return true
		}
	}
	return false
}

// canReusePrepopulatedActiveStats verifies that stats completed Phase 2 over the
// exact same issue set and blocking-edge topology, and already carries pre-populated
// active-subgraph metrics when closed/tombstone issues are present.
func canReusePrepopulatedActiveStats(
	stats *analysis.GraphStats,
	issues []model.Issue,
	extraDeps []*model.Dependency,
	wantBW, wantKCore, wantArt bool,
) bool {
	if stats == nil || !stats.IsPhase2Ready() || stats.NodeCount == 0 || stats.NodeCount != len(issues) {
		return false
	}
	issueSet := make(map[string]bool, len(issues))
	closedSet := make(map[string]bool)
	activeCount := 0
	for i := range issues {
		id := issues[i].ID
		if id == "" || issueSet[id] {
			return false
		}
		issueSet[id] = true
		if issues[i].Status.IsClosed() || issues[i].Status.IsTombstone() {
			closedSet[id] = true
		} else {
			activeCount++
		}
	}
	if len(issueSet) != stats.NodeCount {
		return false
	}

	outDeg := make(map[string]int, len(issues))
	inDeg := make(map[string]int, len(issues))
	seenEdge := make(map[[2]string]bool)
	edgeCount := 0
	recordEdge := func(fromID, toID string, depType model.DependencyType) bool {
		if !depType.IsBlocking() || fromID == "" || toID == "" || fromID == toID {
			return true
		}
		if !issueSet[fromID] || !issueSet[toID] {
			return false
		}
		key := [2]string{fromID, toID}
		if seenEdge[key] {
			return true
		}
		seenEdge[key] = true
		outDeg[fromID]++
		inDeg[toID]++
		edgeCount++
		return true
	}
	for i := range issues {
		for _, dep := range issues[i].Dependencies {
			if dep == nil {
				continue
			}
			fromID := dep.IssueID
			if fromID == "" {
				fromID = issues[i].ID
			}
			if !recordEdge(fromID, dep.DependsOnID, dep.Type) {
				return false
			}
		}
	}
	for _, dep := range extraDeps {
		if dep == nil {
			continue
		}
		if !recordEdge(dep.IssueID, dep.DependsOnID, dep.Type) {
			return false
		}
	}
	if edgeCount != stats.EdgeCount {
		return false
	}
	for id := range issueSet {
		sOut, okOut := stats.OutDegree[id]
		sIn, okIn := stats.InDegree[id]
		if !okOut || !okIn || sOut != outDeg[id] || sIn != inDeg[id] {
			return false
		}
	}

	status := stats.Status()
	if wantBW && status.Betweenness.State != "computed" && status.Betweenness.State != "approx" {
		return false
	}
	if wantKCore && status.KCore.State != "computed" {
		return false
	}
	if wantArt && status.Articulation.State != "computed" {
		return false
	}

	if len(closedSet) > 0 {
		if wantBW {
			bw := stats.ActiveBetweenness()
			if len(bw) != activeCount {
				return false
			}
			for cid := range closedSet {
				if _, exists := bw[cid]; exists {
					return false
				}
			}
		}
		if wantKCore {
			core := stats.ActiveCoreNumber()
			for cid := range closedSet {
				if _, exists := core[cid]; exists {
					return false
				}
			}
		}
		if wantArt {
			for _, id := range stats.ActiveArticulationPoints() {
				if closedSet[id] {
					return false
				}
			}
		}
	}

	return true
}

func computeActiveBetweennessRanks(m map[string]float64) map[string]int {
	if m == nil {
		return nil
	}
	ranks := make(map[string]int, len(m))
	type kv struct {
		k string
		v float64
	}
	sorted := make([]kv, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, kv{k: k, v: v})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].v == sorted[j].v {
			return sorted[i].k < sorted[j].k
		}
		return sorted[i].v > sorted[j].v
	})
	for i, item := range sorted {
		ranks[item.k] = i + 1
	}
	return ranks
}

func computeActiveScoreRanks(m map[string]float64, isActive func(string) bool) map[string]int {
	if m == nil {
		return nil
	}
	filtered := make(map[string]float64)
	for k, v := range m {
		if isActive(k) {
			filtered[k] = v
		}
	}
	return computeActiveBetweennessRanks(filtered)
}

func activeSubgraphStatsForIssues(issues []model.Issue, computeBetweenness, computeKCore, computeArticulation bool) *analysis.GraphStats {
	activeSet := make(map[string]bool, len(issues))
	for i := range issues {
		if !issues[i].Status.IsClosed() && !issues[i].Status.IsTombstone() {
			activeSet[issues[i].ID] = true
		}
	}
	if len(activeSet) == 0 {
		return nil
	}
	activeIssues := make([]model.Issue, 0, len(activeSet))
	activeEdges := 0
	for i := range issues {
		iss := issues[i]
		if !activeSet[iss.ID] {
			continue
		}
		var filteredDeps []*model.Dependency
		for _, dep := range iss.Dependencies {
			if dep != nil && dep.Type.IsBlocking() && activeSet[dep.DependsOnID] {
				filteredDeps = append(filteredDeps, dep)
				activeEdges++
			}
		}
		iss.Dependencies = filteredDeps
		activeIssues = append(activeIssues, iss)
	}
	cfg := analysis.ConfigForSize(len(activeIssues), activeEdges)
	cfg.ComputePageRank = false
	cfg.ComputeHITS = false
	cfg.ComputeEigenvector = false
	cfg.ComputeCriticalPath = false
	cfg.ComputeCycles = false
	cfg.ComputeSlack = false
	if !computeBetweenness {
		cfg.ComputeBetweenness = false
	}
	cfg.ComputeKCore = computeKCore
	cfg.ComputeArticulation = computeArticulation
	stats := analysis.NewAnalyzer(activeIssues).AnalyzeWithConfig(cfg)
	return &stats
}

// GenerateInteractiveGraphHTML creates a self-contained HTML file with force-graph visualization
func GenerateInteractiveGraphHTML(opts InteractiveGraphOptions) (string, error) {
	if len(opts.Issues) == 0 {
		return "", fmt.Errorf("no issues to export")
	}

	forceGraphLib, err := loadEmbeddedVendorScript("force-graph.min.js")
	if err != nil {
		return "", err
	}
	markedLib, err := loadEmbeddedVendorScript("marked.min.js")
	if err != nil {
		return "", err
	}
	exportTime := resolveInteractiveExportTime(opts.ExportedAt)
	readiness := opts.Readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(opts.Issues)
	}

	// Build graph data with all metrics
	nodes := make([]graphNode, 0, len(opts.Issues))
	links := make([]graphLink, 0)

	// Create issue map for dependency and lifecycle lookup
	issueMap := make(map[string]bool, len(opts.Issues))
	issueByID := make(map[string]model.Issue, len(opts.Issues))
	for _, iss := range opts.Issues {
		issueMap[iss.ID] = true
		issueByID[iss.ID] = iss
	}
	isActiveIssue := func(id string) bool {
		iss, ok := issueByID[id]
		if !ok {
			return false
		}
		return iss.Status != model.StatusClosed && iss.Status != model.StatusTombstone
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
		if hasClosedOrTombstoneIssues(opts.Issues) {
			pageRankRank = computeActiveScoreRanks(pageRank, isActiveIssue)
			if canReusePrepopulatedActiveStats(opts.Stats, opts.Issues, nil, true, true, true) {
				betweenness = opts.Stats.ActiveBetweenness()
				betweennessRank = computeActiveBetweennessRanks(betweenness)
				coreNumber = opts.Stats.ActiveCoreNumber()
				articulation = opts.Stats.ActiveArticulationPoints()
			} else if activeStats := activeSubgraphStatsForIssues(opts.Issues, true, true, true); activeStats != nil {
				betweenness = activeStats.Betweenness()
				betweennessRank = activeStats.BetweennessRank()
				coreNumber = activeStats.CoreNumber()
				articulation = activeStats.ArticulationPoints()
			} else {
				betweenness = nil
				betweennessRank = nil
				coreNumber = nil
				articulation = nil
			}
		}
	}
	slackAvailable := opts.Stats != nil && len(slack) > 0 && len(opts.Stats.Cycles()) == 0

	// Create articulation set for O(1) lookup (active issues only)
	articulationSet := make(map[string]bool)
	for _, id := range articulation {
		if isActiveIssue(id) {
			articulationSet[id] = true
		}
	}

	// Build reverse dependency map (who actively blocks who)
	blocksMap := make(map[string][]string)
	for _, iss := range opts.Issues {
		if !isActiveIssue(iss.ID) {
			continue
		}
		for _, dep := range iss.Dependencies {
			if dep != nil && dep.Type.IsBlocking() && isActiveIssue(dep.DependsOnID) {
				blocksMap[dep.DependsOnID] = append(blocksMap[dep.DependsOnID], iss.ID)
			}
		}
	}

	// Build nodes with full bead data
	for _, iss := range opts.Issues {
		isActionable := readiness.Ready(iss.ID, exportTime)
		depState := string(readiness.DependencyState(iss.ID))
		blockedBy := readiness.Blockers(iss.ID)
		blockerCount := len(blockedBy)

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
		deferUntil := ""
		isDeferred := iss.Status == model.StatusDeferred
		if iss.DeferUntil != nil {
			deferUntil = iss.DeferUntil.Format("2006-01-02")
			if iss.DeferUntil.After(exportTime) {
				isDeferred = true
			}
		}
		externalRef := ""
		if iss.ExternalRef != nil {
			externalRef = *iss.ExternalRef
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
			Status:          string(iss.Status),
			Priority:        iss.Priority,
			Type:            string(iss.IssueType),
			Labels:          iss.Labels,
			Assignee:        iss.Assignee,
			SourceRepo:      iss.SourceRepo,
			IsActionable:    isActionable,
			DependencyState: depState,
			BlockerCount:    blockerCount,
			IsTemplate:      iss.IsTemplate,
			Pinned:          iss.Pinned,
			Ephemeral:       iss.Ephemeral,

			// Timestamps & external reference
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			ClosedAt:    closedAt,
			DueDate:     dueDate,
			DeferUntil:  deferUntil,
			IsDeferred:  isDeferred,
			ExternalRef: externalRef,

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
			IsArticulation:  isActiveIssue(iss.ID) && articulationSet[iss.ID],
			PageRankRank:    pageRankRank[iss.ID],
			BetweennessRank: betweennessRank[iss.ID],
		}
		if !isActiveIssue(iss.ID) {
			node.Betweenness = 0
			node.CoreNumber = 0
			node.PageRankRank = 0
			node.BetweennessRank = 0
		}
		if slackAvailable && isActiveIssue(iss.ID) {
			if s, ok := slack[iss.ID]; ok {
				node.Slack = s
				node.SlackAvailable = true
			}
		}
		nodes = append(nodes, node)

		// Build links from dependencies
		for _, dep := range iss.Dependencies {
			if dep == nil || !issueMap[dep.DependsOnID] {
				continue
			}
			// Only mark as critical if blocking between active issues, slack is valid (no cycles), AND both ends exist in slack with zero slack
			s1, ok1 := slack[iss.ID]
			s2, ok2 := slack[dep.DependsOnID]
			isCritical := dep.Type.IsBlocking() && isActiveIssue(iss.ID) && isActiveIssue(dep.DependsOnID) && slackAvailable && ok1 && ok2 && s1 == 0 && s2 == 0
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

	hasCycles := opts.Stats != nil && len(opts.Stats.Cycles()) > 0
	graphData := map[string]interface{}{
		"nodes":           nodes,
		"links":           links,
		"slack_available": slackAvailable,
		"has_cycles":      hasCycles,
		"exported_at_ms":  exportTime.UTC().UnixMilli(),
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
		outputPath = GenerateInteractiveGraphFilenameWithOpts(projectName, exportTime, opts.WorkDir, opts.GitHash)
	} else if err := validateExistingPathNotSymlinkOrDir(outputPath); err != nil {
		return "", err
	}

	// Ensure .html extension
	if !strings.HasSuffix(strings.ToLower(outputPath), ".html") {
		outputPath = strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".html"
	}

	html := generateUltimateHTMLWithTime(title, opts.DataHash, string(dataJSON), len(nodes), len(links), opts.ProjectName, forceGraphLib, markedLib, exportTime)

	if err := SafeWriteOutput(outputPath, []byte(html), 0o644); err != nil {
		return "", err
	}

	return outputPath, nil
}
