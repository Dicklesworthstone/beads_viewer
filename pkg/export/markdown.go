package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// ReportOverrides contains only explicitly supplied CLI settings. A pointer to
// false or an empty template deliberately overrides the recipe value.
type ReportOverrides struct {
	Format         *string
	IncludeGraph   *bool
	IncludeActions *bool
	Template       *string
	GroupBy        *string
}

type ReportOptions struct {
	Format            string
	IncludeGraph      bool
	IncludeActionPlan bool
	IncludeActions    *bool
	OmitActionPlan    bool
	actionPlanSet     bool
	Template          string
	GroupBy           string
	View              recipe.ViewConfig
	Metrics           []string
	GraphStats        *analysis.GraphStats
	ETAContext        *analysis.ETAContext
	Analyzer          *analysis.Analyzer
	VisibleIDs        map[string]bool
	Title             string
	GeneratedAt       time.Time
	Readiness         *model.ReadinessIndex
	AuthorityComplete bool
	GraphIssues       []model.Issue
	SourceAuthority   any
	AuthorityHash     string
	DataHash          string
	SourcePath        string
	SourceKind        string
	AsOf              string
	AsOfCommit        string
	NotReadyLabels    []string
	IncludeInactive   bool
}

// DefaultReportOptions returns initialized report defaults with actionPlanSet
// enabled so direct callers can flip opts.IncludeActionPlan = false without
// needing a pointer wrapper.
func DefaultReportOptions() ReportOptions {
	return ReportOptions{
		Format:            "markdown",
		Title:             "Beads Export",
		IncludeGraph:      true,
		IncludeActionPlan: true,
		actionPlanSet:     true,
	}
}

func (opts ReportOptions) includeActions() bool {
	if opts.IncludeActions != nil {
		return *opts.IncludeActions
	}
	if opts.OmitActionPlan {
		return false
	}
	if opts.actionPlanSet {
		return opts.IncludeActionPlan
	}
	return true
}

func ResolveReportOptions(defaults recipe.ExportConfig, explicit ReportOverrides) (ReportOptions, error) {
	opts := ReportOptions{
		Format:            "markdown",
		Title:             "Beads Export",
		IncludeActionPlan: true,
		actionPlanSet:     true,
	}
	if defaults.Format != "" {
		opts.Format = defaults.Format
	}
	if explicit.Format != nil {
		opts.Format = *explicit.Format
	}
	opts.IncludeGraph = opts.Format != "csv"
	if defaults.IncludeGraph != nil {
		opts.IncludeGraph = *defaults.IncludeGraph
	}
	if explicit.IncludeGraph != nil {
		opts.IncludeGraph = *explicit.IncludeGraph
	}
	if defaults.IncludeActions != nil {
		v := *defaults.IncludeActions
		opts.IncludeActionPlan = v
		opts.IncludeActions = &v
	}
	if explicit.IncludeActions != nil {
		v := *explicit.IncludeActions
		opts.IncludeActionPlan = v
		opts.IncludeActions = &v
	}
	opts.Template = defaults.Template
	if explicit.Template != nil {
		opts.Template = *explicit.Template
	}
	if explicit.GroupBy != nil {
		opts.GroupBy = strings.TrimSpace(*explicit.GroupBy)
	}
	if err := opts.validate(); err != nil {
		return ReportOptions{}, err
	}
	if opts.Template != "" && explicit.Template == nil && !defaults.TemplateAuthorizedByUser() {
		return ReportOptions{}, fmt.Errorf("recipe template %q requires explicit --export-template authorization; review the file and pass --export-template=PATH, or --export-template= to use the built-in report", opts.Template)
	}
	return opts, nil
}

func (opts ReportOptions) validate() error {
	switch opts.Format {
	case "markdown", "html", "json", "csv", "mermaid":
	default:
		return fmt.Errorf("export format %q must be markdown, html, json, csv or mermaid", opts.Format)
	}
	if opts.Format == "csv" && opts.IncludeGraph {
		return fmt.Errorf("CSV cannot include a graph; set --export-include-graph=false")
	}
	if opts.Format == "mermaid" && !opts.IncludeGraph {
		return fmt.Errorf("Mermaid export requires include_graph=true")
	}
	if opts.Template != "" && opts.Format != "markdown" {
		return fmt.Errorf("custom templates require markdown export")
	}
	return nil
}

var defaultReportMetricKeys = []string{
	"pagerank",
	"betweenness",
	"eigenvector",
	"hubs",
	"authorities",
	"critical_path",
	"in_degree",
	"out_degree",
}

func normalizeReportMetricKey(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pagerank", "page_rank", "pr":
		return "pagerank", true
	case "betweenness", "bw":
		return "betweenness", true
	case "eigenvector", "eigen":
		return "eigenvector", true
	case "hubs", "hub":
		return "hubs", true
	case "authorities", "authority", "auth":
		return "authorities", true
	case "critical_path", "critical_path_score", "criticalpath", "critical", "impact":
		return "critical_path", true
	case "kcore", "k_core", "core_number":
		return "kcore", true
	case "slack":
		return "slack", true
	case "in_degree", "indegree":
		return "in_degree", true
	case "out_degree", "outdegree":
		return "out_degree", true
	case "triage", "triage_score":
		return "triage", true
	case "risk", "risk_score":
		return "risk", true
	case "eta", "eta_days":
		return "eta", true
	default:
		return "", false
	}
}

func reportMetricHeader(key string) string {
	switch key {
	case "pagerank":
		return "PageRank"
	case "betweenness":
		return "Betweenness"
	case "eigenvector":
		return "Eigenvector"
	case "hubs":
		return "Hubs"
	case "authorities":
		return "Authorities"
	case "critical_path":
		return "Critical Path"
	case "kcore":
		return "K-Core"
	case "slack":
		return "Slack"
	case "in_degree":
		return "In-Degree"
	case "out_degree":
		return "Out-Degree"
	case "triage":
		return "Triage Score"
	case "risk":
		return "Risk"
	case "eta":
		return "ETA (d)"
	default:
		return key
	}
}

type reportMetricsAccessor struct {
	keys           []string
	pageRank       map[string]float64
	betweenness    map[string]float64
	eigenvector    map[string]float64
	hubs           map[string]float64
	authorities    map[string]float64
	criticalPath   map[string]float64
	kcore          map[string]int
	slack          map[string]float64
	slackAvailable bool
	resolvedIDs    map[string]bool
	inDegree       map[string]int
	outDegree      map[string]int
	triageScore    map[string]float64
	riskScore      map[string]float64
	etaDays        map[string]float64
}

func isDecisionIneligibleIssue(iss model.Issue) bool {
	return isClosedLikeStatus(iss.Status) || iss.IsTemplate || iss.Pinned || iss.Status == model.StatusPinned
}

func newReportMetricsAccessor(opts ReportOptions, issueSlices ...[]model.Issue) *reportMetricsAccessor {
	if (opts.GraphStats == nil && opts.Analyzer == nil && opts.ETAContext == nil) || (!opts.View.ShowMetrics && len(opts.Metrics) == 0) {
		return nil
	}
	var keys []string
	seen := make(map[string]bool)
	if len(opts.Metrics) == 0 {
		keys = append(keys, defaultReportMetricKeys...)
	} else {
		for _, raw := range opts.Metrics {
			trimmed := strings.ToLower(strings.TrimSpace(raw))
			if trimmed == "all" || trimmed == "*" {
				for _, defKey := range defaultReportMetricKeys {
					if !seen[defKey] {
						seen[defKey] = true
						keys = append(keys, defKey)
					}
				}
				continue
			}
			if norm, ok := normalizeReportMetricKey(trimmed); ok && !seen[norm] {
				seen[norm] = true
				keys = append(keys, norm)
			}
		}
		if len(keys) == 0 && opts.View.ShowMetrics {
			keys = append(keys, defaultReportMetricKeys...)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	resolvedIDs := make(map[string]bool)
	decisionExcluded := make(map[string]bool)
	issueByID := make(map[string]model.Issue)
	collectIssues := func(list []model.Issue) {
		for _, iss := range list {
			if _, exists := issueByID[iss.ID]; !exists {
				issueByID[iss.ID] = iss
			}
			if isClosedLikeStatus(iss.Status) {
				if !opts.IncludeInactive {
					resolvedIDs[iss.ID] = true
				}
				decisionExcluded[iss.ID] = true
			} else if isDecisionIneligibleIssue(iss) {
				decisionExcluded[iss.ID] = true
			}
		}
	}
	if opts.Readiness != nil {
		for _, id := range opts.Readiness.ResolvedIDs() {
			if !opts.IncludeInactive {
				resolvedIDs[id] = true
			}
			decisionExcluded[id] = true
		}
	}
	collectIssues(opts.GraphIssues)
	for _, slice := range issueSlices {
		collectIssues(slice)
	}

	acc := &reportMetricsAccessor{
		keys:        keys,
		resolvedIDs: resolvedIDs,
	}
	if opts.GraphStats != nil {
		betweenness := opts.GraphStats.ActiveBetweenness()
		kcore := opts.GraphStats.ActiveCoreNumber()
		if opts.IncludeInactive {
			betweenness = opts.GraphStats.Betweenness()
			kcore = opts.GraphStats.CoreNumber()
		}
		slack := opts.GraphStats.Slack()
		acc.pageRank = opts.GraphStats.PageRank()
		acc.betweenness = betweenness
		acc.eigenvector = opts.GraphStats.Eigenvector()
		acc.hubs = opts.GraphStats.Hubs()
		acc.authorities = opts.GraphStats.Authorities()
		acc.criticalPath = opts.GraphStats.CriticalPathScore()
		acc.kcore = kcore
		acc.slack = slack
		acc.slackAvailable = len(slack) > 0 && len(opts.GraphStats.Cycles()) == 0
		acc.inDegree = opts.GraphStats.InDegree
		acc.outDegree = opts.GraphStats.OutDegree
	}

	now := opts.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	if seen["triage"] {
		an := opts.Analyzer
		if an == nil && len(issueByID) > 0 {
			all := make([]model.Issue, 0, len(issueByID))
			for _, iss := range issueByID {
				all = append(all, iss)
			}
			an = analysis.NewAnalyzer(all)
			an.SetNow(now)
			if opts.Readiness != nil {
				an.SetReadinessScope(opts.Readiness, opts.VisibleIDs)
			}
		}
		if an != nil {
			scores := an.ComputeImpactScores()
			acc.triageScore = make(map[string]float64, len(scores))
			for _, sc := range scores {
				if !decisionExcluded[sc.IssueID] {
					acc.triageScore[sc.IssueID] = sc.Score
				}
			}
		}
	}
	if seen["risk"] && opts.GraphStats != nil && len(issueByID) > 0 {
		issuePtrMap := make(map[string]model.Issue, len(issueByID))
		for id, iss := range issueByID {
			issuePtrMap[id] = iss
		}
		readiness := opts.Readiness
		if readiness == nil {
			all := make([]model.Issue, 0, len(issueByID))
			for _, iss := range issueByID {
				all = append(all, iss)
			}
			readiness = model.NewReadinessIndex(all)
		}
		weights := analysis.DefaultRiskWeights()
		acc.riskScore = make(map[string]float64, len(issueByID))
		for id, iss := range issueByID {
			if decisionExcluded[id] {
				continue
			}
			issCopy := iss
			sig := analysis.ComputeRiskSignalsWithReadiness(&issCopy, opts.GraphStats, issuePtrMap, readiness, now, weights)
			acc.riskScore[id] = sig.CompositeRisk
		}
	}
	if seen["eta"] && opts.ETAContext != nil && len(issueByID) > 0 {
		acc.etaDays = make(map[string]float64, len(issueByID))
		for id, iss := range issueByID {
			if decisionExcluded[id] {
				continue
			}
			eta := opts.ETAContext.EstimateForIssue(iss, 1)
			if eta.EstimatedDays > 0 && !eta.ETADate.IsZero() {
				acc.etaDays[id] = eta.EstimatedDays
			}
		}
	}
	return acc
}

func (m *reportMetricsAccessor) betweennessForIssue(issueID string) (float64, bool) {
	if m == nil || m.betweenness == nil || m.resolvedIDs[issueID] {
		return 0, false
	}
	val, ok := m.betweenness[issueID]
	return val, ok
}

func (m *reportMetricsAccessor) kcoreForIssue(issueID string) (int, bool) {
	if m == nil || m.kcore == nil || m.resolvedIDs[issueID] {
		return 0, false
	}
	val, ok := m.kcore[issueID]
	return val, ok
}

func (m *reportMetricsAccessor) slackForIssue(issueID string) (float64, bool) {
	if m == nil || !m.slackAvailable || m.slack == nil || m.resolvedIDs[issueID] {
		return 0, false
	}
	val, ok := m.slack[issueID]
	return val, ok
}

func (m *reportMetricsAccessor) floatValue(key, issueID string) float64 {
	if m == nil {
		return 0
	}
	switch key {
	case "pagerank":
		return m.pageRank[issueID]
	case "betweenness":
		val, _ := m.betweennessForIssue(issueID)
		return val
	case "eigenvector":
		return m.eigenvector[issueID]
	case "hubs":
		return m.hubs[issueID]
	case "authorities":
		return m.authorities[issueID]
	case "critical_path":
		return m.criticalPath[issueID]
	case "kcore":
		val, _ := m.kcoreForIssue(issueID)
		return float64(val)
	case "slack":
		val, _ := m.slackForIssue(issueID)
		return val
	case "in_degree":
		return float64(m.inDegree[issueID])
	case "out_degree":
		return float64(m.outDegree[issueID])
	case "triage":
		return m.triageScore[issueID]
	case "risk":
		return m.riskScore[issueID]
	case "eta":
		return m.etaDays[issueID]
	default:
		return 0
	}
}

func (m *reportMetricsAccessor) formatValue(key, issueID string) string {
	if m == nil {
		return "0"
	}
	switch key {
	case "kcore":
		val, _ := m.kcoreForIssue(issueID)
		return fmt.Sprintf("%d", val)
	case "in_degree":
		return fmt.Sprintf("%d", m.inDegree[issueID])
	case "out_degree":
		return fmt.Sprintf("%d", m.outDegree[issueID])
	case "slack":
		val, ok := m.slackForIssue(issueID)
		if !ok {
			return "N/A"
		}
		return fmt.Sprintf("%.2f", val)
	case "triage":
		val, ok := m.triageScore[issueID]
		if !ok {
			return "N/A"
		}
		return fmt.Sprintf("%.3f", val)
	case "risk":
		val, ok := m.riskScore[issueID]
		if !ok {
			return "N/A"
		}
		return fmt.Sprintf("%.2f", val)
	case "eta":
		val, ok := m.etaDays[issueID]
		if !ok {
			return "N/A"
		}
		return fmt.Sprintf("%.1f", val)
	case "critical_path":
		return fmt.Sprintf("%.2f", m.floatValue(key, issueID))
	default:
		return fmt.Sprintf("%.4f", m.floatValue(key, issueID))
	}
}

func (m *reportMetricsAccessor) mapForIssue(issueID string) map[string]float64 {
	if m == nil || len(m.keys) == 0 {
		return nil
	}
	out := make(map[string]float64, len(m.keys))
	for _, k := range m.keys {
		switch k {
		case "slack":
			val, ok := m.slackForIssue(issueID)
			if !ok {
				continue
			}
			out[k] = val
		case "triage":
			val, ok := m.triageScore[issueID]
			if !ok {
				continue
			}
			out[k] = val
		case "risk":
			val, ok := m.riskScore[issueID]
			if !ok {
				continue
			}
			out[k] = val
		case "eta":
			val, ok := m.etaDays[issueID]
			if !ok {
				continue
			}
			out[k] = val
		default:
			out[k] = m.floatValue(k, issueID)
		}
	}
	return out
}

func buildBlockingReverseMap(issues []model.Issue) map[string][]string {
	blocksSetByID := make(map[string]map[string]bool, len(issues))
	for _, iss := range issues {
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() || dep.DependsOnID == "" {
				continue
			}
			if blocksSetByID[dep.DependsOnID] == nil {
				blocksSetByID[dep.DependsOnID] = make(map[string]bool)
			}
			blocksSetByID[dep.DependsOnID][iss.ID] = true
		}
	}
	blocksByID := make(map[string][]string, len(blocksSetByID))
	for blockerID, set := range blocksSetByID {
		list := make([]string, 0, len(set))
		for depID := range set {
			list = append(list, depID)
		}
		sort.Strings(list)
		blocksByID[blockerID] = list
	}
	return blocksByID
}

func combineReportIssues(selected, context []model.Issue) []model.Issue {
	if len(context) == 0 {
		return selected
	}
	combined := make([]model.Issue, 0, len(selected)+len(context))
	seen := make(map[string]bool, len(selected)+len(context))
	for _, iss := range selected {
		seen[iss.ID] = true
		combined = append(combined, iss)
	}
	for _, iss := range context {
		if !seen[iss.ID] {
			seen[iss.ID] = true
			combined = append(combined, iss)
		}
	}
	return combined
}

// GenerateReport renders selected bodies in their supplied recipe order. The
// context is consulted only for dependency graph closure and readiness; its
// other issue bodies never become selected output rows.
func GenerateReport(selected, context []model.Issue, opts ReportOptions) ([]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if opts.Title == "" {
		opts.Title = "Beads Export"
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now().UTC()
	}
	hadCallerReadiness := opts.Readiness != nil
	if opts.IncludeGraph {
		if opts.Readiness == nil {
			opts.Readiness = model.NewReadinessIndex(combineReportIssues(selected, context))
		}
		opts.GraphIssues = reportGraphContext(selected, context)
	}
	reportOpts := opts
	if !hadCallerReadiness {
		reportOpts.AuthorityComplete = false
	}
	metricsAcc := newReportMetricsAccessor(opts, selected, context)
	switch opts.Format {
	case "markdown":
		if opts.Template != "" {
			return renderReportTemplate(selected, reportOpts)
		}
		content, err := GenerateMarkdown(selected, opts.Title, reportOpts)
		return []byte(content), err
	case "html":
		content, err := GenerateHTMLReport(selected, opts.Title, reportOpts)
		return []byte(content), err
	case "json":
		type row struct {
			model.Issue
			Actions model.IssueActions `json:"actions"`
			Metrics map[string]float64 `json:"metrics,omitempty"`
		}
		rows := make([]row, 0, len(selected))
		for _, issue := range selected {
			var actions model.IssueActions
			if opts.includeActions() {
				claimable := opts.AuthorityComplete && hadCallerReadiness && opts.Readiness.Claimable(issue.ID, opts.GeneratedAt) && !hasNotReadyLabel(issue.Labels, opts.NotReadyLabels)
				actions = issue.Actions(claimable)
			}
			rows = append(rows, row{
				Issue:   issue,
				Actions: actions,
				Metrics: metricsAcc.mapForIssue(issue.ID),
			})
		}
		var graph *GraphExportResult
		if opts.IncludeGraph {
			var err error
			graph, err = ExportGraph(opts.GraphIssues, opts.GraphStats, GraphExportConfig{
				Format:       GraphFormatJSON,
				DataHash:     analysis.ComputeDataHash(opts.GraphIssues),
				Readiness:    opts.Readiness,
				ReferenceNow: opts.GeneratedAt,
			})
			if err != nil {
				return nil, err
			}
		}
		return json.MarshalIndent(struct {
			Title           string             `json:"title"`
			GeneratedAt     time.Time          `json:"generated_at"`
			SourceAuthority any                `json:"source_authority"`
			AuthorityHash   string             `json:"authority_hash,omitempty"`
			DataHash        string             `json:"data_hash,omitempty"`
			SourcePath      string             `json:"source_path,omitempty"`
			SourceKind      string             `json:"source_kind,omitempty"`
			AsOf            string             `json:"as_of,omitempty"`
			AsOfCommit      string             `json:"as_of_commit,omitempty"`
			Issues          []row              `json:"issues"`
			Graph           *GraphExportResult `json:"graph,omitempty"`
		}{opts.Title, opts.GeneratedAt, opts.SourceAuthority, opts.AuthorityHash, opts.DataHash, opts.SourcePath, opts.SourceKind, opts.AsOf, opts.AsOfCommit, rows, graph}, "", "  ")
	case "csv":
		return writeCSVReport(selected, context, opts, metricsAcc)
	case "mermaid":
		return []byte(reportMermaid(opts.GraphIssues, opts.Readiness)), nil
	}
	return nil, fmt.Errorf("unhandled export format %q", opts.Format)
}

func writeCSVReport(selected, context []model.Issue, opts ReportOptions, metricsAcc *reportMetricsAccessor) ([]byte, error) {
	allForBlocks := selected
	if len(context) > 0 {
		combined := make([]model.Issue, 0, len(context)+len(selected))
		seen := make(map[string]bool, len(context)+len(selected))
		for _, iss := range selected {
			seen[iss.ID] = true
			combined = append(combined, iss)
		}
		for _, iss := range context {
			if !seen[iss.ID] {
				seen[iss.ID] = true
				combined = append(combined, iss)
			}
		}
		allForBlocks = combined
	}
	blocksByID := buildBlockingReverseMap(allForBlocks)

	type csvColSpec struct {
		key    string
		header string
		render func(i model.Issue) string
	}

	resolveCSVCol := func(raw string) (csvColSpec, bool) {
		norm := strings.ToLower(strings.TrimSpace(raw))
		switch norm {
		case "id":
			return csvColSpec{key: "id", header: "id", render: func(i model.Issue) string { return i.ID }}, true
		case "title":
			return csvColSpec{
				key:    "title",
				header: "title",
				render: func(i model.Issue) string {
					if opts.View.TruncateTitle > 0 {
						return truncateString(i.Title, opts.View.TruncateTitle)
					}
					return i.Title
				},
			}, true
		case "status":
			return csvColSpec{key: "status", header: "status", render: func(i model.Issue) string { return string(i.Status) }}, true
		case "priority":
			return csvColSpec{key: "priority", header: "priority", render: func(i model.Issue) string { return fmt.Sprint(i.Priority) }}, true
		case "type", "issue_type":
			return csvColSpec{key: "issue_type", header: "issue_type", render: func(i model.Issue) string { return string(i.IssueType) }}, true
		case "description":
			return csvColSpec{key: "description", header: "description", render: func(i model.Issue) string { return i.Description }}, true
		case "label", "labels", "tag", "tags":
			return csvColSpec{key: "labels", header: "labels", render: func(i model.Issue) string { return strings.Join(i.Labels, ";") }}, true
		case "assignee":
			return csvColSpec{key: "assignee", header: "assignee", render: func(i model.Issue) string { return strings.TrimSpace(i.Assignee) }}, true
		case "repo", "source_repo":
			return csvColSpec{key: "source_repo", header: "source_repo", render: func(i model.Issue) string { return strings.TrimSpace(i.SourceRepo) }}, true
		case "created", "created_at":
			return csvColSpec{
				key:    "created_at",
				header: "created_at",
				render: func(i model.Issue) string {
					if i.CreatedAt.IsZero() {
						return ""
					}
					return i.CreatedAt.Format(time.RFC3339)
				},
			}, true
		case "updated", "updated_at":
			return csvColSpec{
				key:    "updated_at",
				header: "updated_at",
				render: func(i model.Issue) string {
					if i.UpdatedAt.IsZero() {
						return ""
					}
					return i.UpdatedAt.Format(time.RFC3339)
				},
			}, true
		case "closed", "closed_at":
			return csvColSpec{
				key:    "closed_at",
				header: "closed_at",
				render: func(i model.Issue) string {
					if i.ClosedAt == nil || i.ClosedAt.IsZero() {
						return ""
					}
					return i.ClosedAt.Format(time.RFC3339)
				},
			}, true
		case "created_by":
			return csvColSpec{key: "created_by", header: "created_by", render: func(i model.Issue) string { return i.CreatedBy }}, true
		case "close_reason":
			return csvColSpec{key: "close_reason", header: "close_reason", render: func(i model.Issue) string { return i.CloseReason }}, true
		case "estimated_minutes":
			return csvColSpec{
				key:    "estimated_minutes",
				header: "estimated_minutes",
				render: func(i model.Issue) string {
					if i.EstimatedMinutes == nil {
						return ""
					}
					return fmt.Sprint(*i.EstimatedMinutes)
				},
			}, true
		case "blocked_by", "blockers":
			return csvColSpec{
				key:    "blocked_by",
				header: "blocked_by",
				render: func(i model.Issue) string {
					var blockers []string
					seen := make(map[string]bool)
					for _, dep := range i.Dependencies {
						if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID != "" && !seen[dep.DependsOnID] {
							seen[dep.DependsOnID] = true
							blockers = append(blockers, dep.DependsOnID)
						}
					}
					return strings.Join(blockers, ";")
				},
			}, true
		case "blocks", "unblocks":
			return csvColSpec{
				key:    "blocks",
				header: "blocks",
				render: func(i model.Issue) string {
					return strings.Join(blocksByID[i.ID], ";")
				},
			}, true
		default:
			if opts.GraphStats != nil || opts.Analyzer != nil || opts.ETAContext != nil {
				if mKey, ok := normalizeReportMetricKey(norm); ok {
					acc := metricsAcc
					if acc == nil {
						colOpts := opts
						colOpts.Metrics = []string{mKey}
						acc = newReportMetricsAccessor(colOpts, selected, context)
					}
					if acc != nil {
						return csvColSpec{
							key:    mKey,
							header: mKey,
							render: func(i model.Issue) string { return acc.formatValue(mKey, i.ID) },
						}, true
					}
				}
			}
			return csvColSpec{}, false
		}
	}

	var specs []csvColSpec
	seenKey := make(map[string]bool)
	for _, col := range opts.View.Columns {
		if spec, ok := resolveCSVCol(col); ok && !seenKey[spec.key] {
			seenKey[spec.key] = true
			specs = append(specs, spec)
		}
	}
	if len(specs) == 0 {
		for _, defCol := range []string{"id", "title", "status", "priority", "issue_type", "description", "labels"} {
			if spec, ok := resolveCSVCol(defCol); ok && !seenKey[spec.key] {
				seenKey[spec.key] = true
				specs = append(specs, spec)
			}
		}
	}
	if metricsAcc != nil {
		for _, mKey := range metricsAcc.keys {
			if !seenKey[mKey] {
				seenKey[mKey] = true
				keyCopy := mKey
				specs = append(specs, csvColSpec{
					key:    keyCopy,
					header: keyCopy,
					render: func(i model.Issue) string { return metricsAcc.formatValue(keyCopy, i.ID) },
				})
			}
		}
	}

	var content bytes.Buffer
	writer := csv.NewWriter(&content)
	headers := make([]string, len(specs))
	for idx, s := range specs {
		headers[idx] = s.header
	}
	if err := writer.Write(headers); err != nil {
		return nil, err
	}
	for _, issue := range selected {
		row := make([]string, len(specs))
		for idx, s := range specs {
			row[idx] = s.render(issue)
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return content.Bytes(), writer.Error()
}

func reportGraphContext(selected, context []model.Issue) []model.Issue {
	byID := make(map[string]model.Issue, len(context)+len(selected))
	for _, issue := range context {
		byID[issue.ID] = issue
	}
	queue := make([]string, 0, len(selected))
	for _, issue := range selected {
		byID[issue.ID] = issue
		queue = append(queue, issue.ID)
	}
	seen := make(map[string]bool)
	result := make([]model.Issue, 0, len(selected))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		issue, ok := byID[id]
		if !ok || issue.Status == model.StatusTombstone {
			continue
		}
		result = append(result, issue)
		for _, dep := range issue.Dependencies {
			if dep != nil {
				queue = append(queue, dep.DependsOnID)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func reportMermaid(issues []model.Issue, readiness *model.ReadinessIndex) string {
	ids := make(map[string]bool, len(issues))
	for _, issue := range issues {
		ids[issue.ID] = true
	}
	return GenerateMermaidGraph(issues, ids, MermaidConfig{
		ShowNoDependenciesNode: true,
		Readiness:              readiness,
	})
}

// Template data has no methods or function-valued fields. No filesystem,
// environment, process or custom function is exposed to template execution.
func renderReportTemplate(issues []model.Issue, opts ReportOptions) ([]byte, error) {
	file, err := os.Open(opts.Template)
	if err != nil {
		return nil, fmt.Errorf("read export template: %w", err)
	}
	defer file.Close()
	const maxTemplate = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, maxTemplate+1))
	if err != nil {
		return nil, fmt.Errorf("read export template: %w", err)
	}
	if len(raw) > maxTemplate {
		return nil, fmt.Errorf("export template exceeds %d bytes", maxTemplate)
	}
	tmpl, err := template.New("report").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse export template: %w", err)
	}
	type issueData struct {
		ID, Title, Status, IssueType, Description string
		Priority                                  int
		Labels                                    []string
	}
	data := struct {
		Title, GeneratedAt, Graph string
		Issues                    []issueData
	}{Title: escapeReportText(opts.Title), GeneratedAt: opts.GeneratedAt.Format(time.RFC3339), Issues: make([]issueData, 0, len(issues))}
	if opts.IncludeGraph {
		data.Graph = reportMermaid(opts.GraphIssues, opts.Readiness)
	}
	for _, issue := range issues {
		labels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			labels = append(labels, escapeReportText(label))
		}
		data.Issues = append(data.Issues, issueData{escapeReportText(issue.ID), escapeReportText(issue.Title), escapeReportText(string(issue.Status)), escapeReportText(string(issue.IssueType)), escapeReportText(issue.Description), issue.Priority, labels})
	}
	writer := &reportTemplateWriter{remaining: 16 << 20}
	if err := tmpl.Execute(writer, data); err != nil {
		return nil, fmt.Errorf("render export template: %w", err)
	}
	return writer.Bytes(), nil
}

type reportTemplateWriter struct {
	bytes.Buffer
	remaining int
}

func (w *reportTemplateWriter) Write(raw []byte) (int, error) {
	if len(raw) > w.remaining {
		return 0, fmt.Errorf("rendered export template exceeds 16 MiB")
	}
	w.remaining -= len(raw)
	return w.Buffer.Write(raw)
}

func escapeReportText(value string) string {
	// Escape Markdown before HTML so numeric entities introduced for quotes
	// are not broken by escaping their '#'. HTML-sensitive punctuation is
	// handled by EscapeString; the remaining punctuation stays literal even
	// in list, heading, fence, link and GFM strikethrough positions.
	const markdownPunctuation = "\\`*_[]|#!$%()+,-./:;=?@^{}~"
	var markdown strings.Builder
	markdown.Grow(len(value))
	for _, r := range value {
		if strings.ContainsRune(markdownPunctuation, r) {
			markdown.WriteByte('\\')
		}
		markdown.WriteRune(r)
	}
	escaped := html.EscapeString(markdown.String())
	var literal strings.Builder
	literal.Grow(len(escaped))
	lineStart := true
	for _, r := range escaped {
		if lineStart && (r == ' ' || r == '\t') {
			// Character references preserve the text while preventing field
			// indentation from starting a Markdown code block.
			if r == ' ' {
				literal.WriteString("&#32;")
			} else {
				literal.WriteString("&#9;")
			}
			continue
		}
		literal.WriteRune(r)
		lineStart = r == '\n' || r == '\r'
	}
	return literal.String()
}

// Package-level compiled regex for slug creation (avoids recompilation per call)
var slugNonAlphanumericRegex = regexp.MustCompile(`[^a-z0-9]+`)

var tocLinkTextReplacer = strings.NewReplacer(
	"[", "\\[",
	"]", "\\]",
	"\n", " ",
	"\r", "",
)

func sanitizeInlineMarkdown(value string) string {
	var collapsed strings.Builder
	collapsed.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			collapsed.WriteByte(' ')
		case unicode.IsControl(r):
			continue
		default:
			collapsed.WriteRune(r)
		}
	}
	return escapeReportText(strings.TrimSpace(collapsed.String()))
}

func escapeTableText(value string) string {
	clean := strings.ReplaceAll(value, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", " ")
	return strings.ReplaceAll(clean, "|", "\\|")
}

// sanitizeMermaidID ensures an ID is valid for Mermaid diagrams.
// Mermaid node IDs must be alphanumeric with hyphens/underscores.
func sanitizeMermaidID(id string) string {
	var sb strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			sb.WriteRune(r)
		}
	}
	result := sb.String()
	if result == "" {
		return "node"
	}
	return result
}

// sanitizeMermaidText prepares text for use in Mermaid node labels.
// Removes/escapes characters that break Mermaid syntax.
func sanitizeMermaidText(text string) string {
	// Remove or replace problematic characters
	replacer := strings.NewReplacer(
		"\"", "'",
		"[", "(",
		"]", ")",
		"{", "(",
		"}", ")",
		"<", "&lt;",
		">", "&gt;",
		"|", "/",
		"`", "'",
		"\n", " ",
		"\r", "",
	)
	result := replacer.Replace(text)

	// Remove any remaining control characters
	result = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, result)

	result = strings.TrimSpace(result)

	// Truncate if too long (UTF-8 safe using runes)
	runes := []rune(result)
	if len(runes) > 40 {
		result = string(runes[:37]) + "..."
	}

	return result
}

type reportHighRiskItem struct {
	IssueID     string
	RiskScore   float64
	Explanation string
}

type reportDecisionIntel struct {
	riskByID      map[string]analysis.RiskSignals
	whatIfByID    map[string]*analysis.WhatIfResult
	priorityByID  map[string]analysis.PriorityRecommendation
	highRisk      []reportHighRiskItem
	priorityHints []analysis.PriorityRecommendation
	cycleBreaks   []analysis.CycleBreakItem
}

func buildReportDecisionIntel(issues []model.Issue, opts ReportOptions) *reportDecisionIntel {
	if (opts.Analyzer == nil && opts.GraphStats == nil) || len(issues) == 0 {
		return nil
	}
	visibleSet := opts.VisibleIDs
	if visibleSet == nil {
		visibleSet = make(map[string]bool, len(issues))
		for _, iss := range issues {
			visibleSet[iss.ID] = true
		}
	}
	now := opts.GeneratedAt
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	di := &reportDecisionIntel{
		riskByID:     make(map[string]analysis.RiskSignals),
		whatIfByID:   make(map[string]*analysis.WhatIfResult),
		priorityByID: make(map[string]analysis.PriorityRecommendation),
	}

	if opts.GraphStats != nil {
		issueMap := make(map[string]model.Issue, len(opts.GraphIssues)+len(issues))
		for _, iss := range opts.GraphIssues {
			issueMap[iss.ID] = iss
		}
		for _, iss := range issues {
			if _, ok := issueMap[iss.ID]; !ok {
				issueMap[iss.ID] = iss
			}
		}
		readiness := opts.Readiness
		if readiness == nil {
			all := make([]model.Issue, 0, len(issueMap))
			for _, iss := range issueMap {
				all = append(all, iss)
			}
			readiness = model.NewReadinessIndex(all)
		}
		weights := analysis.DefaultRiskWeights()
		for _, iss := range issues {
			if !visibleSet[iss.ID] || isDecisionIneligibleIssue(iss) {
				continue
			}
			issCopy := iss
			sig := analysis.ComputeRiskSignalsWithReadiness(&issCopy, opts.GraphStats, issueMap, readiness, now, weights)
			if sig.CompositeRisk > 0 {
				di.riskByID[iss.ID] = sig
				if sig.CompositeRisk >= 0.50 {
					di.highRisk = append(di.highRisk, reportHighRiskItem{
						IssueID:     iss.ID,
						RiskScore:   sig.CompositeRisk,
						Explanation: sig.Explanation,
					})
				}
			}
		}
		sort.Slice(di.highRisk, func(i, j int) bool {
			if di.highRisk[i].RiskScore != di.highRisk[j].RiskScore {
				return di.highRisk[i].RiskScore > di.highRisk[j].RiskScore
			}
			return di.highRisk[i].IssueID < di.highRisk[j].IssueID
		})
		if len(di.highRisk) > 5 {
			di.highRisk = di.highRisk[:5]
		}
	}

	if opts.Analyzer != nil {
		for _, rec := range opts.Analyzer.GenerateRecommendations() {
			if !visibleSet[rec.IssueID] || rec.SuggestedPriority == rec.CurrentPriority {
				continue
			}
			di.priorityByID[rec.IssueID] = rec
			di.priorityHints = append(di.priorityHints, rec)
		}
		sort.Slice(di.priorityHints, func(i, j int) bool {
			if di.priorityHints[i].Confidence != di.priorityHints[j].Confidence {
				return di.priorityHints[i].Confidence > di.priorityHints[j].Confidence
			}
			return di.priorityHints[i].IssueID < di.priorityHints[j].IssueID
		})
		if len(di.priorityHints) > 5 {
			di.priorityHints = di.priorityHints[:5]
		}

		if opts.GraphStats != nil {
			for _, iss := range issues {
				if !visibleSet[iss.ID] || isClosedLikeStatus(iss.Status) {
					continue
				}
				wi := opts.Analyzer.ComputeWhatIf(iss.ID, opts.GraphStats)
				if wi == nil || (wi.Delta.DirectUnblocks <= 0 && wi.Delta.TransitiveUnblocks <= 0) {
					continue
				}
				filteredIDs := make([]string, 0, len(wi.Delta.UnblockedIssueIDs))
				for _, uid := range wi.Delta.UnblockedIssueIDs {
					if visibleSet[uid] {
						filteredIDs = append(filteredIDs, uid)
					}
				}
				wiCopy := *wi
				wiCopy.Delta.UnblockedIssueIDs = filteredIDs
				if opts.VisibleIDs != nil {
					if len(filteredIDs) == 0 {
						continue
					}
					if wiCopy.Delta.DirectUnblocks > len(filteredIDs) {
						wiCopy.Delta.DirectUnblocks = len(filteredIDs)
					}
				}
				di.whatIfByID[iss.ID] = &wiCopy
			}

			adv := opts.Analyzer.GenerateAdvancedInsightsFromStats(opts.GraphStats, analysis.DefaultAdvancedInsightsConfig())
			if adv != nil && adv.CycleBreak != nil {
				for _, cb := range adv.CycleBreak.Suggestions {
					if !visibleSet[cb.EdgeFrom] || !visibleSet[cb.EdgeTo] {
						continue
					}
					di.cycleBreaks = append(di.cycleBreaks, cb)
					if len(di.cycleBreaks) >= 5 {
						break
					}
				}
			}
		}
	}

	return di
}

func (di *reportDecisionIntel) hasExecutiveSummary() bool {
	return di != nil && (len(di.priorityHints) > 0 || len(di.cycleBreaks) > 0 || len(di.highRisk) > 0)
}

func writeMarkdownDecisionIntel(sb *strings.Builder, di *reportDecisionIntel) {
	if !di.hasExecutiveSummary() {
		return
	}
	sb.WriteString("## Decision Intelligence\n\n")
	if len(di.highRisk) > 0 {
		sb.WriteString("### High Operational Risk\n\n")
		sb.WriteString("| Issue | Risk | Rationale |\n|-------|------|-----------|\n")
		for _, hr := range di.highRisk {
			sb.WriteString(fmt.Sprintf("| `%s` | %.0f%% | %s |\n",
				escapeTableCodeSpan(hr.IssueID), hr.RiskScore*100, escapeTableText(hr.Explanation)))
		}
		sb.WriteString("\n")
	}
	if len(di.priorityHints) > 0 {
		sb.WriteString("### Priority Alignment Hints\n\n")
		sb.WriteString("| Issue | Change | Confidence | Reasoning |\n|-------|--------|------------|-----------|\n")
		for _, ph := range di.priorityHints {
			arrow := "↓"
			if ph.Direction == "increase" {
				arrow = "↑"
			}
			reason := ""
			if len(ph.Reasoning) > 0 {
				reason = ph.Reasoning[0]
			}
			sb.WriteString(fmt.Sprintf("| `%s` | %s P%d → P%d | %.0f%% | %s |\n",
				escapeTableCodeSpan(ph.IssueID), arrow, ph.CurrentPriority, ph.SuggestedPriority, ph.Confidence*100, escapeTableText(reason)))
		}
		sb.WriteString("\n")
	}
	if len(di.cycleBreaks) > 0 {
		sb.WriteString("### Cycle-Break Suggestions\n\n")
		sb.WriteString("| Remove Edge | Cycles Broken | Collateral | Rationale |\n|-------------|---------------|------------|-----------|\n")
		for _, cb := range di.cycleBreaks {
			sb.WriteString(fmt.Sprintf("| `%s` → `%s` | %d | %d | %s |\n",
				escapeTableCodeSpan(cb.EdgeFrom), escapeTableCodeSpan(cb.EdgeTo), cb.Impact, cb.Collateral, escapeTableText(cb.Rationale)))
		}
		sb.WriteString("\n")
	}
}

func writeHTMLDecisionIntel(sb *strings.Builder, di *reportDecisionIntel) {
	if !di.hasExecutiveSummary() {
		return
	}
	sb.WriteString("<section class=\"decision-intelligence\">\n<h2>Decision Intelligence</h2>\n")
	if len(di.highRisk) > 0 {
		sb.WriteString("<h3>High Operational Risk</h3>\n<table><thead><tr><th>Issue</th><th>Risk</th><th>Rationale</th></tr></thead><tbody>\n")
		for _, hr := range di.highRisk {
			sb.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td>%.0f%%</td><td>%s</td></tr>\n",
				html.EscapeString(hr.IssueID), hr.RiskScore*100, html.EscapeString(hr.Explanation)))
		}
		sb.WriteString("</tbody></table>\n")
	}
	if len(di.priorityHints) > 0 {
		sb.WriteString("<h3>Priority Alignment Hints</h3>\n<table><thead><tr><th>Issue</th><th>Change</th><th>Confidence</th><th>Reasoning</th></tr></thead><tbody>\n")
		for _, ph := range di.priorityHints {
			arrow := "↓"
			if ph.Direction == "increase" {
				arrow = "↑"
			}
			reason := ""
			if len(ph.Reasoning) > 0 {
				reason = ph.Reasoning[0]
			}
			sb.WriteString(fmt.Sprintf("<tr><td><code>%s</code></td><td>%s P%d → P%d</td><td>%.0f%%</td><td>%s</td></tr>\n",
				html.EscapeString(ph.IssueID), arrow, ph.CurrentPriority, ph.SuggestedPriority, ph.Confidence*100, html.EscapeString(reason)))
		}
		sb.WriteString("</tbody></table>\n")
	}
	if len(di.cycleBreaks) > 0 {
		sb.WriteString("<h3>Cycle-Break Suggestions</h3>\n<table><thead><tr><th>Remove Edge</th><th>Cycles Broken</th><th>Collateral</th><th>Rationale</th></tr></thead><tbody>\n")
		for _, cb := range di.cycleBreaks {
			sb.WriteString(fmt.Sprintf("<tr><td><code>%s</code> → <code>%s</code></td><td>%d</td><td>%d</td><td>%s</td></tr>\n",
				html.EscapeString(cb.EdgeFrom), html.EscapeString(cb.EdgeTo), cb.Impact, cb.Collateral, html.EscapeString(cb.Rationale)))
		}
		sb.WriteString("</tbody></table>\n")
	}
	sb.WriteString("</section>\n")
}

// GenerateMarkdown creates a comprehensive markdown report of all issues
func GenerateMarkdown(issues []model.Issue, title string, options ...ReportOptions) (string, error) {
	opts := ReportOptions{IncludeGraph: true, GeneratedAt: time.Now(), GraphIssues: issues}
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now()
	}
	if opts.IncludeGraph && opts.GraphIssues == nil {
		opts.GraphIssues = issues
	}
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("# %s\n\n", title))
	sb.WriteString(fmt.Sprintf("*Generated: %s*\n\n", opts.GeneratedAt.Format(time.RFC1123)))

	// Summary Statistics
	sb.WriteString("## Summary\n\n")

	open, inProgress, blocked, closed, deferredOther := 0, 0, 0, 0, 0
	for _, i := range issues {
		if isClosedLikeStatus(i.Status) {
			closed++
			continue
		}
		switch i.Status {
		case model.StatusOpen, "":
			open++
		case model.StatusInProgress:
			inProgress++
		case model.StatusBlocked:
			blocked++
		default:
			deferredOther++
		}
	}

	sb.WriteString("| Metric | Count |\n|--------|-------|\n")
	sb.WriteString(fmt.Sprintf("| **Total** | %d |\n", len(issues)))
	sb.WriteString(fmt.Sprintf("| Open | %d |\n", open))
	sb.WriteString(fmt.Sprintf("| In Progress | %d |\n", inProgress))
	sb.WriteString(fmt.Sprintf("| Blocked | %d |\n", blocked))
	if deferredOther > 0 {
		sb.WriteString(fmt.Sprintf("| Deferred / Other | %d |\n", deferredOther))
	}
	sb.WriteString(fmt.Sprintf("| Closed | %d |\n\n", closed))
	metricsAcc := newReportMetricsAccessor(opts, issues)
	decisionIntel := buildReportDecisionIntel(issues, opts)
	writeSummaryTable(&sb, issues, opts, metricsAcc)
	writeMarkdownDecisionIntel(&sb, decisionIntel)

	// Quick Actions Section
	sb.WriteString(generateQuickActions(issues, opts))

	// Precompute stable, unique slugs for TOC anchors and headings.
	slugCounts := make(map[string]int, len(issues))
	issueSlugs := make([]string, len(issues))
	for idx, i := range issues {
		base := createSlug(issueHeadingText(i))
		issueSlugs[idx] = uniqueSlug(base, slugCounts)
	}

	// Table of Contents
	sb.WriteString("## Table of Contents\n\n")
	for idx, i := range issues {
		slug := issueSlugs[idx]
		statusIcon := getStatusEmoji(string(i.Status))
		sb.WriteString(fmt.Sprintf("- [%s %s %s](#%s)\n",
			statusIcon,
			tocLinkTextReplacer.Replace(i.ID),
			tocLinkTextReplacer.Replace(i.Title),
			slug,
		))
	}
	sb.WriteString("\n---\n\n")

	// Dependency Graph (Mermaid)
	if opts.IncludeGraph {
		sb.WriteString("## Dependency Graph\n\n")
		sb.WriteString("```mermaid\n")

		graph := reportMermaid(opts.GraphIssues, opts.Readiness)
		sb.WriteString(graph)

		sb.WriteString("```\n\n")
		sb.WriteString("---\n\n")
	}

	// Individual Issues (optionally grouped when GroupBy or View.GroupBy is configured)
	groupBy := effectiveReportGroupBy(opts)
	if groups := groupMarkdownIssues(issues, groupBy); len(groups) > 0 {
		for _, grp := range groups {
			sb.WriteString(fmt.Sprintf("## Group: %s (%d)\n\n", grp.Label, len(grp.Indices)))
			for _, idx := range grp.Indices {
				writeMarkdownIssueSection(&sb, issues[idx], issueSlugs[idx], opts, metricsAcc, decisionIntel)
			}
		}
	} else {
		for idx, i := range issues {
			writeMarkdownIssueSection(&sb, i, issueSlugs[idx], opts, metricsAcc, decisionIntel)
		}
	}

	return sb.String(), nil
}

type markdownIssueGroup struct {
	Key     string
	SortOrd int
	Label   string
	Indices []int
}

func effectiveReportGroupBy(opts ReportOptions) string {
	if g := strings.ToLower(strings.TrimSpace(opts.GroupBy)); g != "" {
		return g
	}
	return strings.ToLower(strings.TrimSpace(opts.View.GroupBy))
}

func sanitizeGroupHeading(s string) string {
	clean := strings.ReplaceAll(s, "\n", " ")
	clean = strings.ReplaceAll(clean, "\r", "")
	return strings.TrimSpace(clean)
}

func groupMarkdownIssues(issues []model.Issue, groupBy string) []markdownIssueGroup {
	if len(issues) == 0 || groupBy == "" || groupBy == "none" {
		return nil
	}
	byKey := make(map[string]*markdownIssueGroup)
	var groups []*markdownIssueGroup

	addToBucket := func(key string, ord int, label string, idx int) {
		grp, ok := byKey[key]
		if !ok {
			grp = &markdownIssueGroup{Key: key, SortOrd: ord, Label: sanitizeGroupHeading(label)}
			byKey[key] = grp
			groups = append(groups, grp)
		}
		grp.Indices = append(grp.Indices, idx)
	}

	switch groupBy {
	case "priority":
		for idx, iss := range issues {
			key := fmt.Sprintf("p-%04d", iss.Priority)
			addToBucket(key, iss.Priority, getPriorityLabel(iss.Priority), idx)
		}
	case "status":
		statusOrder := func(s model.Status) int {
			switch s {
			case model.StatusOpen, "":
				return 0
			case model.StatusInProgress:
				return 1
			case model.StatusBlocked:
				return 2
			case model.StatusClosed:
				return 10
			case model.StatusTombstone:
				return 11
			default:
				return 5
			}
		}
		for idx, iss := range issues {
			rawStatus := string(iss.Status)
			if strings.TrimSpace(rawStatus) == "" {
				rawStatus = string(model.StatusOpen)
			}
			label := fmt.Sprintf("%s %s", getStatusEmoji(rawStatus), rawStatus)
			addToBucket(rawStatus, statusOrder(iss.Status), label, idx)
		}
	case "type", "issue_type":
		typeOrder := func(t string) int {
			switch t {
			case "epic":
				return 0
			case "bug":
				return 1
			case "feature":
				return 2
			case "task":
				return 3
			case "chore":
				return 4
			default:
				return 5
			}
		}
		for idx, iss := range issues {
			rawType := strings.TrimSpace(string(iss.IssueType))
			if rawType == "" {
				rawType = "other"
			}
			label := fmt.Sprintf("%s %s", getTypeEmoji(rawType), rawType)
			addToBucket(rawType, typeOrder(rawType), label, idx)
		}
	case "assignee":
		for idx, iss := range issues {
			clean := sanitizeGroupHeading(iss.Assignee)
			if clean == "" {
				addToBucket("~unassigned", 1, "Unassigned", idx)
			} else {
				addToBucket(strings.ToLower(clean), 0, "@"+clean, idx)
			}
		}
	case "label", "labels", "tag", "tags":
		for idx, iss := range issues {
			var cleaned []string
			for _, l := range iss.Labels {
				if c := sanitizeGroupHeading(l); c != "" {
					cleaned = append(cleaned, c)
				}
			}
			sort.Strings(cleaned)
			if len(cleaned) == 0 {
				addToBucket("~unlabeled", 1, "Unlabeled", idx)
			} else {
				primary := cleaned[0]
				addToBucket(strings.ToLower(primary), 0, primary, idx)
			}
		}
	case "repo", "source_repo":
		for idx, iss := range issues {
			clean := sanitizeGroupHeading(iss.SourceRepo)
			if clean == "" {
				addToBucket("~default-repo", 1, "Default Repo", idx)
			} else {
				addToBucket(strings.ToLower(clean), 0, clean, idx)
			}
		}
	default:
		return nil
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].SortOrd != groups[j].SortOrd {
			return groups[i].SortOrd < groups[j].SortOrd
		}
		return groups[i].Key < groups[j].Key
	})

	out := make([]markdownIssueGroup, len(groups))
	for i, g := range groups {
		out[i] = *g
	}
	return out
}

type summaryColumnSpec struct {
	header string
	render func(i model.Issue) string
}

func writeSummaryTable(sb *strings.Builder, issues []model.Issue, opts ReportOptions, metricsAcc *reportMetricsAccessor) {
	if len(issues) == 0 {
		return
	}

	blocksByID := buildBlockingReverseMap(issues)

	resolveColumn := func(raw string) (summaryColumnSpec, string, bool) {
		norm := strings.ToLower(strings.TrimSpace(raw))
		switch norm {
		case "id":
			return summaryColumnSpec{
				header: "ID",
				render: func(i model.Issue) string { return escapeTableText(i.ID) },
			}, "id", true
		case "title":
			return summaryColumnSpec{
				header: "Title",
				render: func(i model.Issue) string {
					t := i.Title
					if opts.View.TruncateTitle > 0 {
						t = truncateString(t, opts.View.TruncateTitle)
					}
					return escapeTableText(t)
				},
			}, "title", true
		case "type", "issue_type":
			return summaryColumnSpec{
				header: "Type",
				render: func(i model.Issue) string {
					return fmt.Sprintf("%s %s", getTypeEmoji(string(i.IssueType)), escapeTableText(string(i.IssueType)))
				},
			}, "type", true
		case "priority":
			return summaryColumnSpec{
				header: "Priority",
				render: func(i model.Issue) string {
					return fmt.Sprintf("P%d", i.Priority)
				},
			}, "priority", true
		case "status":
			return summaryColumnSpec{
				header: "Status",
				render: func(i model.Issue) string {
					return fmt.Sprintf("%s %s", getStatusEmoji(string(i.Status)), escapeTableText(string(i.Status)))
				},
			}, "status", true
		case "assignee":
			return summaryColumnSpec{
				header: "Assignee",
				render: func(i model.Issue) string {
					clean := strings.TrimSpace(i.Assignee)
					if clean == "" {
						return "-"
					}
					return "@" + escapeTableText(clean)
				},
			}, "assignee", true
		case "repo", "source_repo":
			return summaryColumnSpec{
				header: "Repo",
				render: func(i model.Issue) string {
					clean := strings.TrimSpace(i.SourceRepo)
					if clean == "" {
						return "-"
					}
					return escapeTableText(clean)
				},
			}, "source_repo", true
		case "label", "labels", "tag", "tags":
			return summaryColumnSpec{
				header: "Labels",
				render: func(i model.Issue) string {
					if len(i.Labels) == 0 {
						return "-"
					}
					escaped := make([]string, len(i.Labels))
					for idx, l := range i.Labels {
						escaped[idx] = escapeTableText(l)
					}
					return strings.Join(escaped, ", ")
				},
			}, "labels", true
		case "created", "created_at":
			return summaryColumnSpec{
				header: "Created",
				render: func(i model.Issue) string {
					if i.CreatedAt.IsZero() {
						return "-"
					}
					return i.CreatedAt.Format("2006-01-02")
				},
			}, "created", true
		case "updated", "updated_at":
			return summaryColumnSpec{
				header: "Updated",
				render: func(i model.Issue) string {
					if i.UpdatedAt.IsZero() {
						return "-"
					}
					return i.UpdatedAt.Format("2006-01-02")
				},
			}, "updated", true
		case "blocked_by", "blockers":
			return summaryColumnSpec{
				header: "Blocked By",
				render: func(i model.Issue) string {
					var blockers []string
					seen := make(map[string]bool)
					for _, dep := range i.Dependencies {
						if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID != "" && !seen[dep.DependsOnID] {
							seen[dep.DependsOnID] = true
							blockers = append(blockers, escapeTableText(dep.DependsOnID))
						}
					}
					if len(blockers) == 0 {
						return "-"
					}
					return strings.Join(blockers, ", ")
				},
			}, "blocked_by", true
		case "blocks", "unblocks":
			return summaryColumnSpec{
				header: "Blocks",
				render: func(i model.Issue) string {
					targets := blocksByID[i.ID]
					if len(targets) == 0 {
						return "-"
					}
					escaped := make([]string, len(targets))
					for idx, t := range targets {
						escaped[idx] = escapeTableText(t)
					}
					return strings.Join(escaped, ", ")
				},
			}, "blocks", true
		default:
			if opts.GraphStats != nil || opts.Analyzer != nil || opts.ETAContext != nil {
				if mKey, ok := normalizeReportMetricKey(norm); ok {
					acc := metricsAcc
					if acc == nil {
						colOpts := opts
						colOpts.Metrics = []string{mKey}
						acc = newReportMetricsAccessor(colOpts, issues)
					}
					if acc != nil {
						return summaryColumnSpec{
							header: reportMetricHeader(mKey),
							render: func(i model.Issue) string { return acc.formatValue(mKey, i.ID) },
						}, mKey, true
					}
				}
			}
			return summaryColumnSpec{}, "", false
		}
	}

	var specs []summaryColumnSpec
	seenKey := make(map[string]bool)
	for _, col := range opts.View.Columns {
		if spec, key, ok := resolveColumn(col); ok && !seenKey[key] {
			seenKey[key] = true
			specs = append(specs, spec)
		}
	}
	if len(specs) == 0 {
		for _, defCol := range []string{"id", "title", "type", "priority", "status"} {
			if spec, key, ok := resolveColumn(defCol); ok && !seenKey[key] {
				seenKey[key] = true
				specs = append(specs, spec)
			}
		}
	}
	if metricsAcc != nil {
		for _, mKey := range metricsAcc.keys {
			if !seenKey[mKey] {
				seenKey[mKey] = true
				keyCopy := mKey
				specs = append(specs, summaryColumnSpec{
					header: reportMetricHeader(keyCopy),
					render: func(i model.Issue) string { return metricsAcc.formatValue(keyCopy, i.ID) },
				})
			}
		}
	}

	headers := make([]string, len(specs))
	separators := make([]string, len(specs))
	for idx, spec := range specs {
		headers[idx] = spec.header
		separators[idx] = "---"
	}
	sb.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	sb.WriteString("| " + strings.Join(separators, " | ") + " |\n")
	for _, iss := range issues {
		cells := make([]string, len(specs))
		for idx, spec := range specs {
			cells[idx] = spec.render(iss)
		}
		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	sb.WriteString("\n")
}

func formatEstimatedDuration(minutes int) string {
	if minutes <= 0 {
		return ""
	}
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	rem := minutes % 60
	if rem == 0 {
		return fmt.Sprintf("%dh (%dm)", hours, minutes)
	}
	return fmt.Sprintf("%dh %dm (%dm)", hours, rem, minutes)
}

func writeMarkdownIssueSection(sb *strings.Builder, i model.Issue, slug string, opts ReportOptions, metricsAcc *reportMetricsAccessor, decisionIntel *reportDecisionIntel) {
	typeIcon := getTypeEmoji(string(i.IssueType))
	sb.WriteString(fmt.Sprintf("<a id=\"%s\"></a>\n\n", slug))
	sb.WriteString(fmt.Sprintf("## %s\n\n", issueHeadingText(i)))

	// Metadata Table
	sb.WriteString("| Property | Value |\n|----------|-------|\n")
	sb.WriteString(fmt.Sprintf("| **Type** | %s %s |\n", typeIcon, escapeTableText(string(i.IssueType))))
	sb.WriteString(fmt.Sprintf("| **Priority** | %s |\n", escapeTableText(getPriorityLabel(i.Priority))))
	sb.WriteString(fmt.Sprintf("| **Status** | %s %s |\n", getStatusEmoji(string(i.Status)), escapeTableText(string(i.Status))))
	if i.Assignee != "" {
		sb.WriteString(fmt.Sprintf("| **Assignee** | @%s |\n", escapeTableText(i.Assignee)))
	}
	if strings.TrimSpace(i.SourceRepo) != "" {
		sb.WriteString(fmt.Sprintf("| **Repo** | %s |\n", escapeTableText(i.SourceRepo)))
	}
	if strings.TrimSpace(i.CreatedBy) != "" {
		sb.WriteString(fmt.Sprintf("| **Created By** | %s |\n", escapeTableText(i.CreatedBy)))
	}
	if i.EstimatedMinutes != nil && *i.EstimatedMinutes > 0 {
		sb.WriteString(fmt.Sprintf("| **Estimated** | %s |\n", formatEstimatedDuration(*i.EstimatedMinutes)))
	}
	sb.WriteString(fmt.Sprintf("| **Created** | %s |\n", i.CreatedAt.Format("2006-01-02 15:04")))
	sb.WriteString(fmt.Sprintf("| **Updated** | %s |\n", i.UpdatedAt.Format("2006-01-02 15:04")))
	if i.DueDate != nil {
		sb.WriteString(fmt.Sprintf("| **Due** | %s |\n", i.DueDate.Format("2006-01-02")))
	}
	if i.DeferUntil != nil {
		sb.WriteString(fmt.Sprintf("| **Deferred Until** | %s |\n", i.DeferUntil.Format("2006-01-02")))
	}
	if i.ClosedAt != nil {
		sb.WriteString(fmt.Sprintf("| **Closed** | %s |\n", i.ClosedAt.Format("2006-01-02 15:04")))
	}
	if strings.TrimSpace(i.CloseReason) != "" {
		sb.WriteString(fmt.Sprintf("| **Close Reason** | %s |\n", escapeTableText(i.CloseReason)))
	}
	if i.ExternalRef != nil && *i.ExternalRef != "" {
		escapedRef := strings.ReplaceAll(escapeTableText(*i.ExternalRef), "`", "'")
		sb.WriteString(fmt.Sprintf("| **External Ref** | `%s` |\n", escapedRef))
	}
	if len(i.Labels) > 0 {
		escapedLabels := make([]string, len(i.Labels))
		for idx, label := range i.Labels {
			escapedLabels[idx] = escapeTableText(label)
		}
		sb.WriteString(fmt.Sprintf("| **Labels** | %s |\n", strings.Join(escapedLabels, ", ")))
	}
	if metricsAcc != nil {
		for _, mKey := range metricsAcc.keys {
			sb.WriteString(fmt.Sprintf("| **%s** | %s |\n", reportMetricHeader(mKey), metricsAcc.formatValue(mKey, i.ID)))
		}
	}
	if !isClosedLikeStatus(i.Status) {
		if opts.ETAContext != nil && !i.IsTemplate && !i.Pinned && i.Status != model.StatusPinned {
			eta := opts.ETAContext.EstimateForIssue(i, 1)
			if eta.EstimatedDays > 0 && !eta.ETADate.IsZero() {
				sb.WriteString(fmt.Sprintf("| **ETA Forecast** | ~%.1fd (%s, %.0f%% conf) |\n",
					eta.EstimatedDays, eta.ETADate.UTC().Format("2006-01-02"), eta.Confidence*100))
			}
		}
		if opts.Analyzer != nil {
			if chain := opts.Analyzer.GetBlockerChain(i.ID); chain != nil && chain.IsBlocked && (len(chain.RootBlockers) > 0 || chain.HasCycle) {
				var rootParts []string
				for _, rb := range chain.RootBlockers {
					if opts.VisibleIDs != nil && !opts.VisibleIDs[rb.ID] {
						continue
					}
					if len(rootParts) >= 4 {
						break
					}
					if rb.Status == "missing" {
						rootParts = append(rootParts, fmt.Sprintf("`%s` (missing)", escapeTableCodeSpan(rb.ID)))
					} else {
						rootParts = append(rootParts, fmt.Sprintf("`%s` (P%d)", escapeTableCodeSpan(rb.ID), rb.Priority))
					}
				}
				if len(rootParts) > 0 || chain.HasCycle {
					val := strings.Join(rootParts, ", ")
					if chain.HasCycle {
						if val != "" {
							val += " · ⚠ cycle detected"
						} else {
							val = "⚠ dependency cycle detected"
						}
					}
					sb.WriteString(fmt.Sprintf("| **Start Here (Root Blockers)** | %s |\n", val))
				}
			}
		}
		if decisionIntel != nil {
			if sig, ok := decisionIntel.riskByID[i.ID]; ok && sig.CompositeRisk >= 0.25 {
				riskVal := fmt.Sprintf("%.0f%%", sig.CompositeRisk*100)
				if sig.Explanation != "" {
					riskVal += " — " + escapeTableText(sig.Explanation)
				}
				sb.WriteString(fmt.Sprintf("| **Operational Risk** | %s |\n", riskVal))
			}
			if wi := decisionIntel.whatIfByID[i.ID]; wi != nil && (wi.Delta.DirectUnblocks > 0 || wi.Delta.TransitiveUnblocks > 0) {
				ub := fmt.Sprintf("+%d direct", wi.Delta.DirectUnblocks)
				if wi.Delta.TransitiveUnblocks > wi.Delta.DirectUnblocks {
					ub += fmt.Sprintf(" (+%d total)", wi.Delta.TransitiveUnblocks)
				}
				if len(wi.Delta.UnblockedIssueIDs) > 0 {
					limit := min(4, len(wi.Delta.UnblockedIssueIDs))
					idSpans := make([]string, 0, limit)
					for k := 0; k < limit; k++ {
						idSpans = append(idSpans, "`"+escapeTableCodeSpan(wi.Delta.UnblockedIssueIDs[k])+"`")
					}
					ub += " → " + strings.Join(idSpans, ", ")
					if len(wi.Delta.UnblockedIssueIDs) > limit {
						ub += fmt.Sprintf(" (+%d more)", len(wi.Delta.UnblockedIssueIDs)-limit)
					}
				}
				sb.WriteString(fmt.Sprintf("| **What-If Unblocks** | %s |\n", ub))
			}
			if ph, ok := decisionIntel.priorityByID[i.ID]; ok {
				arrow := "↓"
				if ph.Direction == "increase" {
					arrow = "↑"
				}
				hintVal := fmt.Sprintf("%s P%d → P%d (%.0f%%)", arrow, ph.CurrentPriority, ph.SuggestedPriority, ph.Confidence*100)
				if len(ph.Reasoning) > 0 {
					hintVal += " — " + escapeTableText(ph.Reasoning[0])
				}
				sb.WriteString(fmt.Sprintf("| **Priority Hint** | %s |\n", hintVal))
			}
		}
	}
	sb.WriteString("\n")

	if i.Description != "" {
		sb.WriteString("### Description\n\n")
		sb.WriteString(i.Description + "\n\n")
	}

	if i.AcceptanceCriteria != "" {
		sb.WriteString("### Acceptance Criteria\n\n")
		sb.WriteString(i.AcceptanceCriteria + "\n\n")
	}

	if i.Design != "" {
		sb.WriteString("### Design\n\n")
		sb.WriteString(i.Design + "\n\n")
	}

	if i.Notes != "" {
		sb.WriteString("### Notes\n\n")
		sb.WriteString(i.Notes + "\n\n")
	}

	if len(i.Dependencies) > 0 {
		sb.WriteString("### Dependencies\n\n")
		for _, dep := range i.Dependencies {
			if dep == nil {
				continue
			}
			icon := "🔗"
			if dep.Type.IsBlocking() {
				icon = "⛔"
			}
			sb.WriteString(fmt.Sprintf("- %s **%s**: `%s`\n", icon, dep.Type, dep.DependsOnID))
		}
		sb.WriteString("\n")
	}

	if len(i.Comments) > 0 {
		sb.WriteString("### Comments\n\n")
		for _, c := range i.Comments {
			if c == nil {
				continue
			}
			escapedText := strings.ReplaceAll(c.Text, "\n", "\n> ")
			sb.WriteString(fmt.Sprintf("> **%s** (%s)\n>\n> %s\n\n",
				c.Author, c.CreatedAt.Format("2006-01-02"), escapedText))
		}
	}

	// Per-issue command snippets
	sb.WriteString(generateIssueCommands(i, opts))

	sb.WriteString("---\n\n")
}

// GenerateHTMLReport renders a self-contained, HTML-escaped report of selected issues.
func GenerateHTMLReport(issues []model.Issue, title string, opts ReportOptions) (string, error) {
	if title == "" {
		title = "Beads Export"
	}
	if opts.GeneratedAt.IsZero() {
		opts.GeneratedAt = time.Now().UTC()
	}
	if opts.IncludeGraph && opts.GraphIssues == nil {
		opts.GraphIssues = issues
	}

	metricsAcc := newReportMetricsAccessor(opts, issues)
	decisionIntel := buildReportDecisionIntel(issues, opts)
	blocksByID := buildBlockingReverseMap(issues)

	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n<meta charset=\"UTF-8\">\n")
	sb.WriteString(fmt.Sprintf("<title>%s</title>\n", html.EscapeString(title)))
	sb.WriteString("<style>body{font-family:system-ui,-apple-system,sans-serif;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#111827;line-height:1.5}table{border-collapse:collapse;width:100%;margin:1rem 0}th,td{border:1px solid #d1d5db;padding:0.5rem 0.75rem;text-align:left;font-size:0.9rem}th{background:#f3f4f6;font-weight:600}section.issue-group{border-top:2px solid #9ca3af;padding-top:1rem;margin-top:2rem}section.issue{border-top:1px solid #e5e7eb;padding-top:1rem;margin-top:1.5rem}pre{background:#f9fafb;padding:0.75rem;border-radius:6px;overflow-x:auto}</style>\n")
	sb.WriteString("</head>\n<body>\n")
	sb.WriteString(fmt.Sprintf("<h1>%s</h1>\n", html.EscapeString(title)))
	sb.WriteString(fmt.Sprintf("<p><em>Generated: %s</em></p>\n", html.EscapeString(opts.GeneratedAt.Format(time.RFC1123))))

	// Summary table
	type htmlCol struct {
		key    string
		header string
		render func(i model.Issue) string
	}
	resolveHTMLCol := func(raw string) (htmlCol, bool) {
		norm := strings.ToLower(strings.TrimSpace(raw))
		switch norm {
		case "id":
			return htmlCol{key: "id", header: "ID", render: func(i model.Issue) string { return html.EscapeString(i.ID) }}, true
		case "title":
			return htmlCol{
				key:    "title",
				header: "Title",
				render: func(i model.Issue) string {
					t := i.Title
					if opts.View.TruncateTitle > 0 {
						t = truncateString(t, opts.View.TruncateTitle)
					}
					return html.EscapeString(t)
				},
			}, true
		case "type", "issue_type":
			return htmlCol{key: "type", header: "Type", render: func(i model.Issue) string { return html.EscapeString(string(i.IssueType)) }}, true
		case "priority":
			return htmlCol{key: "priority", header: "Priority", render: func(i model.Issue) string { return fmt.Sprintf("P%d", i.Priority) }}, true
		case "status":
			return htmlCol{key: "status", header: "Status", render: func(i model.Issue) string { return html.EscapeString(string(i.Status)) }}, true
		case "assignee":
			return htmlCol{
				key:    "assignee",
				header: "Assignee",
				render: func(i model.Issue) string {
					clean := strings.TrimSpace(i.Assignee)
					if clean == "" {
						return "-"
					}
					return "@" + html.EscapeString(clean)
				},
			}, true
		case "repo", "source_repo":
			return htmlCol{
				key:    "source_repo",
				header: "Repo",
				render: func(i model.Issue) string {
					clean := strings.TrimSpace(i.SourceRepo)
					if clean == "" {
						return "-"
					}
					return html.EscapeString(clean)
				},
			}, true
		case "label", "labels", "tag", "tags":
			return htmlCol{
				key:    "labels",
				header: "Labels",
				render: func(i model.Issue) string {
					if len(i.Labels) == 0 {
						return "-"
					}
					return html.EscapeString(strings.Join(i.Labels, ", "))
				},
			}, true
		case "created", "created_at":
			return htmlCol{
				key:    "created",
				header: "Created",
				render: func(i model.Issue) string {
					if i.CreatedAt.IsZero() {
						return "-"
					}
					return i.CreatedAt.Format("2006-01-02")
				},
			}, true
		case "updated", "updated_at":
			return htmlCol{
				key:    "updated",
				header: "Updated",
				render: func(i model.Issue) string {
					if i.UpdatedAt.IsZero() {
						return "-"
					}
					return i.UpdatedAt.Format("2006-01-02")
				},
			}, true
		case "blocked_by", "blockers":
			return htmlCol{
				key:    "blocked_by",
				header: "Blocked By",
				render: func(i model.Issue) string {
					var blockers []string
					seen := make(map[string]bool)
					for _, dep := range i.Dependencies {
						if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID != "" && !seen[dep.DependsOnID] {
							seen[dep.DependsOnID] = true
							blockers = append(blockers, dep.DependsOnID)
						}
					}
					if len(blockers) == 0 {
						return "-"
					}
					return html.EscapeString(strings.Join(blockers, ", "))
				},
			}, true
		case "blocks", "unblocks":
			return htmlCol{
				key:    "blocks",
				header: "Blocks",
				render: func(i model.Issue) string {
					targets := blocksByID[i.ID]
					if len(targets) == 0 {
						return "-"
					}
					return html.EscapeString(strings.Join(targets, ", "))
				},
			}, true
		default:
			if opts.GraphStats != nil || opts.Analyzer != nil || opts.ETAContext != nil {
				if mKey, ok := normalizeReportMetricKey(norm); ok {
					acc := metricsAcc
					if acc == nil {
						colOpts := opts
						colOpts.Metrics = []string{mKey}
						acc = newReportMetricsAccessor(colOpts, issues)
					}
					if acc != nil {
						return htmlCol{
							key:    mKey,
							header: reportMetricHeader(mKey),
							render: func(i model.Issue) string { return html.EscapeString(acc.formatValue(mKey, i.ID)) },
						}, true
					}
				}
			}
			return htmlCol{}, false
		}
	}

	var cols []htmlCol
	seenCol := make(map[string]bool)
	for _, col := range opts.View.Columns {
		if spec, ok := resolveHTMLCol(col); ok && !seenCol[spec.key] {
			seenCol[spec.key] = true
			cols = append(cols, spec)
		}
	}
	if len(cols) == 0 {
		for _, defCol := range []string{"id", "title", "type", "priority", "status"} {
			if spec, ok := resolveHTMLCol(defCol); ok && !seenCol[spec.key] {
				seenCol[spec.key] = true
				cols = append(cols, spec)
			}
		}
	}
	if metricsAcc != nil {
		for _, mKey := range metricsAcc.keys {
			if !seenCol[mKey] {
				seenCol[mKey] = true
				keyCopy := mKey
				cols = append(cols, htmlCol{
					key:    keyCopy,
					header: reportMetricHeader(keyCopy),
					render: func(i model.Issue) string { return html.EscapeString(metricsAcc.formatValue(keyCopy, i.ID)) },
				})
			}
		}
	}

	sb.WriteString("<h2>Summary</h2>\n<table class=\"issues-summary\">\n<thead><tr>")
	for _, c := range cols {
		sb.WriteString("<th>" + html.EscapeString(c.header) + "</th>")
	}
	sb.WriteString("</tr></thead>\n<tbody>\n")
	for _, iss := range issues {
		sb.WriteString("<tr>")
		for _, c := range cols {
			sb.WriteString("<td>" + c.render(iss) + "</td>")
		}
		sb.WriteString("</tr>\n")
	}
	sb.WriteString("</tbody>\n</table>\n")
	writeHTMLDecisionIntel(&sb, decisionIntel)

	if opts.includeActions() {
		var quickCmds []string
		for _, iss := range issues {
			if isClosedLikeStatus(iss.Status) {
				continue
			}
			actions := iss.Actions(false)
			if actions.Show != nil {
				quickCmds = append(quickCmds, actions.Show.Shell)
			}
		}
		if len(quickCmds) > 0 {
			sb.WriteString("<section class=\"quick-actions\">\n<h2>Quick Actions</h2>\n<p>Inspect the current tracker before acting on this snapshot:</p>\n<pre><code>")
			sb.WriteString(html.EscapeString(strings.Join(quickCmds, "\n") + "\n"))
			sb.WriteString("</code></pre>\n</section>\n")
		}
	}

	if opts.IncludeGraph {
		sb.WriteString("<h2>Dependency Graph</h2>\n<pre class=\"mermaid\">")
		sb.WriteString(html.EscapeString(reportMermaid(opts.GraphIssues, opts.Readiness)))
		sb.WriteString("</pre>\n")
	}

	slugCounts := make(map[string]int, len(issues))
	issueSlugs := make([]string, len(issues))
	for idx, iss := range issues {
		issueSlugs[idx] = uniqueSlug(createSlug(issueHeadingText(iss)), slugCounts)
	}

	writeHTMLIssueSection := func(iss model.Issue, slug string) {
		sb.WriteString(fmt.Sprintf("<section class=\"issue\" id=\"%s\">\n", html.EscapeString(slug)))
		sb.WriteString(fmt.Sprintf("<h2>%s</h2>\n", html.EscapeString(issueHeadingText(iss))))
		sb.WriteString("<table><tbody>\n")
		sb.WriteString(fmt.Sprintf("<tr><th>Type</th><td>%s</td></tr>\n", html.EscapeString(string(iss.IssueType))))
		sb.WriteString(fmt.Sprintf("<tr><th>Priority</th><td>%s</td></tr>\n", html.EscapeString(getPriorityLabel(iss.Priority))))
		sb.WriteString(fmt.Sprintf("<tr><th>Status</th><td>%s</td></tr>\n", html.EscapeString(string(iss.Status))))
		if iss.Assignee != "" {
			sb.WriteString(fmt.Sprintf("<tr><th>Assignee</th><td>@%s</td></tr>\n", html.EscapeString(iss.Assignee)))
		}
		if strings.TrimSpace(iss.SourceRepo) != "" {
			sb.WriteString(fmt.Sprintf("<tr><th>Repo</th><td>%s</td></tr>\n", html.EscapeString(iss.SourceRepo)))
		}
		if strings.TrimSpace(iss.CreatedBy) != "" {
			sb.WriteString(fmt.Sprintf("<tr><th>Created By</th><td>%s</td></tr>\n", html.EscapeString(iss.CreatedBy)))
		}
		if iss.EstimatedMinutes != nil && *iss.EstimatedMinutes > 0 {
			sb.WriteString(fmt.Sprintf("<tr><th>Estimated</th><td>%s</td></tr>\n", html.EscapeString(formatEstimatedDuration(*iss.EstimatedMinutes))))
		}
		if !iss.CreatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("<tr><th>Created</th><td>%s</td></tr>\n", html.EscapeString(iss.CreatedAt.Format("2006-01-02 15:04"))))
		}
		if !iss.UpdatedAt.IsZero() {
			sb.WriteString(fmt.Sprintf("<tr><th>Updated</th><td>%s</td></tr>\n", html.EscapeString(iss.UpdatedAt.Format("2006-01-02 15:04"))))
		}
		if iss.DueDate != nil {
			sb.WriteString(fmt.Sprintf("<tr><th>Due</th><td>%s</td></tr>\n", html.EscapeString(iss.DueDate.Format("2006-01-02"))))
		}
		if iss.DeferUntil != nil {
			sb.WriteString(fmt.Sprintf("<tr><th>Deferred Until</th><td>%s</td></tr>\n", html.EscapeString(iss.DeferUntil.Format("2006-01-02"))))
		}
		if iss.ClosedAt != nil {
			sb.WriteString(fmt.Sprintf("<tr><th>Closed</th><td>%s</td></tr>\n", html.EscapeString(iss.ClosedAt.Format("2006-01-02 15:04"))))
		}
		if strings.TrimSpace(iss.CloseReason) != "" {
			sb.WriteString(fmt.Sprintf("<tr><th>Close Reason</th><td>%s</td></tr>\n", html.EscapeString(iss.CloseReason)))
		}
		if iss.ExternalRef != nil && *iss.ExternalRef != "" {
			sb.WriteString(fmt.Sprintf("<tr><th>External Ref</th><td><code>%s</code></td></tr>\n", html.EscapeString(*iss.ExternalRef)))
		}
		if len(iss.Labels) > 0 {
			sb.WriteString(fmt.Sprintf("<tr><th>Labels</th><td>%s</td></tr>\n", html.EscapeString(strings.Join(iss.Labels, ", "))))
		}
		if metricsAcc != nil {
			for _, mKey := range metricsAcc.keys {
				sb.WriteString(fmt.Sprintf("<tr><th>%s</th><td>%s</td></tr>\n",
					html.EscapeString(reportMetricHeader(mKey)),
					html.EscapeString(metricsAcc.formatValue(mKey, iss.ID))))
			}
		}
		if !isClosedLikeStatus(iss.Status) {
			if opts.ETAContext != nil && !iss.IsTemplate && !iss.Pinned && iss.Status != model.StatusPinned {
				eta := opts.ETAContext.EstimateForIssue(iss, 1)
				if eta.EstimatedDays > 0 && !eta.ETADate.IsZero() {
					sb.WriteString(fmt.Sprintf("<tr><th>ETA Forecast</th><td>~%.1fd (%s, %.0f%% conf)</td></tr>\n",
						eta.EstimatedDays, html.EscapeString(eta.ETADate.UTC().Format("2006-01-02")), eta.Confidence*100))
				}
			}
			if opts.Analyzer != nil {
				if chain := opts.Analyzer.GetBlockerChain(iss.ID); chain != nil && chain.IsBlocked && (len(chain.RootBlockers) > 0 || chain.HasCycle) {
					var rootParts []string
					for _, rb := range chain.RootBlockers {
						if opts.VisibleIDs != nil && !opts.VisibleIDs[rb.ID] {
							continue
						}
						if len(rootParts) >= 4 {
							break
						}
						if rb.Status == "missing" {
							rootParts = append(rootParts, fmt.Sprintf("<code>%s</code> (missing)", html.EscapeString(rb.ID)))
						} else {
							rootParts = append(rootParts, fmt.Sprintf("<code>%s</code> (P%d)", html.EscapeString(rb.ID), rb.Priority))
						}
					}
					if len(rootParts) > 0 || chain.HasCycle {
						val := strings.Join(rootParts, ", ")
						if chain.HasCycle {
							if val != "" {
								val += " · ⚠ cycle detected"
							} else {
								val = "⚠ dependency cycle detected"
							}
						}
						sb.WriteString(fmt.Sprintf("<tr><th>Start Here (Root Blockers)</th><td>%s</td></tr>\n", val))
					}
				}
			}
			if decisionIntel != nil {
				if sig, ok := decisionIntel.riskByID[iss.ID]; ok && sig.CompositeRisk >= 0.25 {
					riskVal := fmt.Sprintf("%.0f%%", sig.CompositeRisk*100)
					if sig.Explanation != "" {
						riskVal += " — " + html.EscapeString(sig.Explanation)
					}
					sb.WriteString(fmt.Sprintf("<tr><th>Operational Risk</th><td>%s</td></tr>\n", riskVal))
				}
				if wi := decisionIntel.whatIfByID[iss.ID]; wi != nil && (wi.Delta.DirectUnblocks > 0 || wi.Delta.TransitiveUnblocks > 0) {
					ub := fmt.Sprintf("+%d direct", wi.Delta.DirectUnblocks)
					if wi.Delta.TransitiveUnblocks > wi.Delta.DirectUnblocks {
						ub += fmt.Sprintf(" (+%d total)", wi.Delta.TransitiveUnblocks)
					}
					if len(wi.Delta.UnblockedIssueIDs) > 0 {
						limit := min(4, len(wi.Delta.UnblockedIssueIDs))
						idSpans := make([]string, 0, limit)
						for k := 0; k < limit; k++ {
							idSpans = append(idSpans, "<code>"+html.EscapeString(wi.Delta.UnblockedIssueIDs[k])+"</code>")
						}
						ub += " → " + strings.Join(idSpans, ", ")
						if len(wi.Delta.UnblockedIssueIDs) > limit {
							ub += fmt.Sprintf(" (+%d more)", len(wi.Delta.UnblockedIssueIDs)-limit)
						}
					}
					sb.WriteString(fmt.Sprintf("<tr><th>What-If Unblocks</th><td>%s</td></tr>\n", ub))
				}
				if ph, ok := decisionIntel.priorityByID[iss.ID]; ok {
					arrow := "↓"
					if ph.Direction == "increase" {
						arrow = "↑"
					}
					hintVal := fmt.Sprintf("%s P%d → P%d (%.0f%%)", arrow, ph.CurrentPriority, ph.SuggestedPriority, ph.Confidence*100)
					if len(ph.Reasoning) > 0 {
						hintVal += " — " + html.EscapeString(ph.Reasoning[0])
					}
					sb.WriteString(fmt.Sprintf("<tr><th>Priority Hint</th><td>%s</td></tr>\n", hintVal))
				}
			}
		}
		sb.WriteString("</tbody></table>\n")
		if iss.Description != "" {
			sb.WriteString(fmt.Sprintf("<h3>Description</h3><pre>%s</pre>\n", html.EscapeString(iss.Description)))
		}
		if iss.AcceptanceCriteria != "" {
			sb.WriteString(fmt.Sprintf("<h3>Acceptance Criteria</h3><pre>%s</pre>\n", html.EscapeString(iss.AcceptanceCriteria)))
		}
		if iss.Design != "" {
			sb.WriteString(fmt.Sprintf("<h3>Design</h3><pre>%s</pre>\n", html.EscapeString(iss.Design)))
		}
		if iss.Notes != "" {
			sb.WriteString(fmt.Sprintf("<h3>Notes</h3><pre>%s</pre>\n", html.EscapeString(iss.Notes)))
		}
		var nonNilDeps []*model.Dependency
		for _, dep := range iss.Dependencies {
			if dep != nil {
				nonNilDeps = append(nonNilDeps, dep)
			}
		}
		if len(nonNilDeps) > 0 {
			sb.WriteString("<h3>Dependencies</h3><ul>\n")
			for _, dep := range nonNilDeps {
				icon := "🔗"
				if dep.Type.IsBlocking() {
					icon = "⛔"
				}
				sb.WriteString(fmt.Sprintf("<li>%s <strong>%s</strong>: <code>%s</code></li>\n",
					icon,
					html.EscapeString(string(dep.Type)),
					html.EscapeString(dep.DependsOnID)))
			}
			sb.WriteString("</ul>\n")
		}
		var nonNilComments []*model.Comment
		for _, c := range iss.Comments {
			if c != nil {
				nonNilComments = append(nonNilComments, c)
			}
		}
		if len(nonNilComments) > 0 {
			sb.WriteString("<h3>Comments</h3>\n")
			for _, c := range nonNilComments {
				sb.WriteString(fmt.Sprintf("<blockquote><p><strong>%s</strong> (%s)</p><pre>%s</pre></blockquote>\n",
					html.EscapeString(c.Author),
					html.EscapeString(c.CreatedAt.Format("2006-01-02")),
					html.EscapeString(c.Text)))
			}
		}
		if opts.includeActions() && !isClosedLikeStatus(iss.Status) {
			claimable := opts.AuthorityComplete && opts.Readiness != nil && opts.Readiness.Claimable(iss.ID, opts.GeneratedAt) && !hasNotReadyLabel(iss.Labels, opts.NotReadyLabels)
			actions := iss.Actions(claimable)
			if actions.Show != nil {
				var cmdBuf strings.Builder
				if actions.Claim != nil {
					cmdBuf.WriteString("# Atomically claim; the live tracker may reject a stale recommendation\n")
					cmdBuf.WriteString(actions.Claim.Shell + "\n\n")
				}
				cmdBuf.WriteString("# View full details\n")
				cmdBuf.WriteString(actions.Show.Shell + "\n")
				sb.WriteString("<details class=\"issue-commands\"><summary>Commands</summary><pre><code>")
				sb.WriteString(html.EscapeString(cmdBuf.String()))
				sb.WriteString("</code></pre></details>\n")
			}
		}
		sb.WriteString("</section>\n")
	}

	groupBy := effectiveReportGroupBy(opts)
	if groups := groupMarkdownIssues(issues, groupBy); len(groups) > 0 {
		for _, grp := range groups {
			sb.WriteString(fmt.Sprintf("<section class=\"issue-group\" data-group=\"%s\">\n", html.EscapeString(grp.Key)))
			sb.WriteString(fmt.Sprintf("<h2>Group: %s (%d)</h2>\n", html.EscapeString(grp.Label), len(grp.Indices)))
			for _, idx := range grp.Indices {
				writeHTMLIssueSection(issues[idx], issueSlugs[idx])
			}
			sb.WriteString("</section>\n")
		}
	} else {
		for idx, iss := range issues {
			writeHTMLIssueSection(iss, issueSlugs[idx])
		}
	}

	sb.WriteString("</body>\n</html>\n")
	return sb.String(), nil
}

func sanitizeHeadingLine(value string) string {
	clean := strings.ReplaceAll(value, "\r\n", " ")
	clean = strings.ReplaceAll(clean, "\r", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	return strings.TrimSpace(clean)
}

func issueHeadingText(i model.Issue) string {
	typeIcon := getTypeEmoji(string(i.IssueType))
	id := sanitizeHeadingLine(i.ID)
	title := sanitizeHeadingLine(i.Title)
	if title == "" {
		return fmt.Sprintf("%s %s", typeIcon, id)
	}
	return fmt.Sprintf("%s %s %s", typeIcon, id, title)
}

func uniqueSlug(base string, counts map[string]int) string {
	if base == "" {
		base = "section"
	}
	if count, ok := counts[base]; ok {
		count++
		counts[base] = count
		return fmt.Sprintf("%s-%d", base, count)
	}
	counts[base] = 0
	return base
}

// createSlug creates a URL-friendly slug from heading text.
func createSlug(text string) string {
	// Convert to lowercase and replace non-alphanumeric with hyphens
	slug := strings.ToLower(text)
	slug = slugNonAlphanumericRegex.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	return slug
}

func getStatusEmoji(status string) string {
	switch status {
	case "open":
		return "🟢"
	case "in_progress":
		return "🔵"
	case "blocked":
		return "🔴"
	case "closed", "tombstone":
		return "⚫"
	default:
		return "⚪"
	}
}

func isClosedLikeStatus(status model.Status) bool {
	return status == model.StatusClosed || status == model.StatusTombstone
}

func getTypeEmoji(issueType string) string {
	switch issueType {
	case "bug":
		return "🐛"
	case "feature":
		return "✨"
	case "task":
		return "📋"
	case "epic":
		return "🚀" // Use rocket instead of mountain - VS-16 variation selector causes width issues
	case "chore":
		return "🧹"
	default:
		return "•"
	}
}

func getPriorityLabel(priority int) string {
	switch priority {
	case 0:
		return "🔥 Critical (P0)"
	case 1:
		return "⚡ High (P1)"
	case 2:
		return "🔹 Medium (P2)"
	case 3:
		return "☕ Low (P3)"
	case 4:
		return "💤 Backlog (P4)"
	default:
		return fmt.Sprintf("P%d", priority)
	}
}

// WriteReportFile replaces a report without following a leaf symlink or
// modifying a hard-linked target. Its explicitly selected parent must exist.
func WriteReportFile(path string, content []byte) error {
	bundle, name, err := openFileBundle(path)
	if err != nil {
		return err
	}
	defer bundle.Close()
	return bundle.WriteFile(name, content, 0o644)
}

// SaveMarkdownToFile writes the generated markdown to a file
func SaveMarkdownToFile(issues []model.Issue, filename string) error {
	// Make a copy to avoid mutating the caller's slice
	issuesCopy := make([]model.Issue, len(issues))
	copy(issuesCopy, issues)

	// Sort issues for the report: Open first, then priority, then date
	sort.Slice(issuesCopy, func(i, j int) bool {
		iClosed := isClosedLikeStatus(issuesCopy[i].Status)
		jClosed := isClosedLikeStatus(issuesCopy[j].Status)
		if iClosed != jClosed {
			return !iClosed
		}
		if issuesCopy[i].Priority != issuesCopy[j].Priority {
			return issuesCopy[i].Priority < issuesCopy[j].Priority
		}
		return issuesCopy[i].CreatedAt.After(issuesCopy[j].CreatedAt)
	})

	content, err := GenerateMarkdown(issuesCopy, "Beads Export")
	if err != nil {
		return err
	}
	return WriteReportFile(filename, []byte(content))
}

// generateQuickActions offers inspection commands bound to each live tracker.
// A report does not establish that every open issue is ready to be closed.
func generateQuickActions(issues []model.Issue, options ReportOptions) string {
	if !options.includeActions() {
		return ""
	}
	var sb strings.Builder
	for _, issue := range issues {
		if isClosedLikeStatus(issue.Status) {
			continue
		}
		actions := issue.Actions(false)
		if actions.Show != nil {
			if sb.Len() == 0 {
				sb.WriteString("## Quick Actions\n\nInspect the current tracker before acting on this snapshot:\n\n```bash\n")
			}
			sb.WriteString(actions.Show.Shell + "\n")
		}
	}
	if sb.Len() > 0 {
		sb.WriteString("```\n\n")
	}
	return sb.String()
}

func hasNotReadyLabel(issueLabels, notReadyLabels []string) bool {
	if len(notReadyLabels) == 0 || len(issueLabels) == 0 {
		return false
	}
	for _, rawWanted := range notReadyLabels {
		wanted := strings.ToLower(strings.TrimSpace(rawWanted))
		if wanted == "" {
			continue
		}
		for _, rawLabel := range issueLabels {
			if strings.ToLower(strings.TrimSpace(rawLabel)) == wanted {
				return true
			}
		}
	}
	return false
}

// generateIssueCommands creates command snippets for a single issue
func generateIssueCommands(issue model.Issue, options ReportOptions) string {
	if !options.includeActions() {
		return ""
	}
	var sb strings.Builder

	// Skip command snippets for closed issues
	if isClosedLikeStatus(issue.Status) {
		return ""
	}

	claimable := options.AuthorityComplete && options.Readiness != nil && options.Readiness.Claimable(issue.ID, options.GeneratedAt) && !hasNotReadyLabel(issue.Labels, options.NotReadyLabels)
	actions := issue.Actions(claimable)
	if actions.Show == nil {
		return ""
	}

	sb.WriteString("<details>\n<summary>📋 Commands</summary>\n\n")
	sb.WriteString("```bash\n")

	if actions.Claim != nil {
		sb.WriteString("# Atomically claim; the live tracker may reject a stale recommendation\n")
		sb.WriteString(actions.Claim.Shell + "\n\n")
	}

	sb.WriteString("# View full details\n")
	sb.WriteString(actions.Show.Shell + "\n")

	sb.WriteString("```\n\n")
	sb.WriteString("</details>\n\n")

	return sb.String()
}

// shellEscape escapes a string for safe use in shell commands.
// Uses single quotes and escapes any single quotes within the string.
func shellEscape(s string) string {
	// If the string contains no special characters, return as-is
	if isShellSafe(s) {
		return s
	}
	// Otherwise, wrap in single quotes and escape any single quotes
	escaped := strings.ReplaceAll(s, "'", "'\"'\"'")
	return "'" + escaped + "'"
}

// isShellSafe returns true if the string is safe to use unquoted in shell
func isShellSafe(s string) bool {
	for _, r := range s {
		if !isShellSafeChar(r) {
			return false
		}
	}
	return len(s) > 0
}

// isShellSafeChar returns true if the character is safe in unquoted shell strings
func isShellSafeChar(r rune) bool {
	// Allow alphanumeric, hyphen, underscore, period, and some punctuation
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '-' || r == '_' || r == '.' || r == ':'
}

// ============================================================================
// Priority Brief Export (bv-96)
// ============================================================================

// PriorityBriefCapacity holds optional Graham-bound parallel capacity metrics
// rendered into priority/agent briefs when non-nil.
type PriorityBriefCapacity struct {
	OpenCount           int
	TotalMinutes        int
	CriticalPathMinutes int
	ParallelizablePct   float64
	Days1Agent          float64
	Days2AgentsMin      float64
	Days2AgentsMax      float64
	Days4AgentsMin      float64
	Days4AgentsMax      float64
	CriticalPathIDs     []string
}

// PriorityBriefConfig configures the priority brief generation
type PriorityBriefConfig struct {
	MaxRecommendations      int                               // Max recommendations to include (default: 5)
	MaxQuickWins            int                               // Max quick wins to include (default: 3)
	MaxBlockers             int                               // Max blockers to include (default: 3)
	MaxPriorityAlerts       int                               // Max priority misalignment alerts (default: 5)
	MaxCycleBreaks          int                               // Max cycle-break suggestions (default: 5)
	IncludeWhatIf           bool                              // Include what-if deltas
	IncludeLegend           bool                              // Include metric legend
	DataHash                string                            // Optional data hash for verification
	GlobalCapacity          *PriorityBriefCapacity            // Optional global Graham-bound capacity summary
	PriorityRecommendations []analysis.PriorityRecommendation // Optional priority misalignment alerts
	CycleBreaks             []analysis.CycleBreakItem         // Optional cycle-break edge suggestions
}

func escapeTableCodeSpan(s string) string {
	return strings.ReplaceAll(escapeTableText(s), "`", "'")
}

// DefaultPriorityBriefConfig returns sensible defaults for the priority brief
func DefaultPriorityBriefConfig() PriorityBriefConfig {
	return PriorityBriefConfig{
		MaxRecommendations: 5,
		MaxQuickWins:       3,
		MaxBlockers:        3,
		IncludeWhatIf:      true,
		IncludeLegend:      true,
	}
}

// GeneratePriorityBriefFromTriage creates a priority brief from a TriageResult (bv-96)
// This is the production version that takes proper triage data
func GeneratePriorityBriefFromTriageJSON(triageJSON []byte, config PriorityBriefConfig) (string, error) {
	// Parse the JSON
	var triage struct {
		Meta struct {
			Version     string    `json:"version"`
			GeneratedAt time.Time `json:"generated_at"`
			Phase2Ready bool      `json:"phase2_ready"`
			IssueCount  int       `json:"issue_count"`
		} `json:"meta"`
		QuickRef struct {
			OpenCount       int `json:"open_count"`
			ActionableCount int `json:"actionable_count"`
			BlockedCount    int `json:"blocked_count"`
			InProgressCount int `json:"in_progress_count"`
			TopPicks        []struct {
				ID       string   `json:"id"`
				Title    string   `json:"title"`
				Score    float64  `json:"score"`
				Reasons  []string `json:"reasons"`
				Unblocks int      `json:"unblocks"`
			} `json:"top_picks"`
		} `json:"quick_ref"`
		Recommendations []struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			Type        string   `json:"type"`
			Status      string   `json:"status"`
			Priority    int      `json:"priority"`
			Score       float64  `json:"score"`
			Action      string   `json:"action"`
			Reasons     []string `json:"reasons"`
			UnblocksIDs []string `json:"unblocks_ids"`
			Breakdown   struct {
				PageRankNorm     float64               `json:"pagerank_norm"`
				BetweennessNorm  float64               `json:"betweenness_norm"`
				TimeToImpactNorm float64               `json:"time_to_impact_norm"`
				RiskNorm         float64               `json:"risk_norm"`
				RiskSignals      *analysis.RiskSignals `json:"risk_signals,omitempty"`
			} `json:"breakdown"`
		} `json:"recommendations"`
		QuickWins []struct {
			ID     string  `json:"id"`
			Title  string  `json:"title"`
			Score  float64 `json:"score"`
			Reason string  `json:"reason"`
		} `json:"quick_wins"`
		BlockersToClear []struct {
			ID            string   `json:"id"`
			Title         string   `json:"title"`
			UnblocksCount int      `json:"unblocks_count"`
			UnblocksIDs   []string `json:"unblocks_ids"`
			Actionable    bool     `json:"actionable"`
		} `json:"blockers_to_clear"`
		Alerts []struct {
			Type     string `json:"type"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"alerts"`
	}

	if err := json.Unmarshal(triageJSON, &triage); err != nil {
		return "", fmt.Errorf("failed to parse triage JSON: %w", err)
	}

	var sb strings.Builder

	// Header
	sb.WriteString("# 📊 Priority Brief\n\n")
	sb.WriteString(fmt.Sprintf("*Generated: %s*  \n", triage.Meta.GeneratedAt.Format("2006-01-02 15:04")))
	sb.WriteString(fmt.Sprintf("*Version: %s | Issues: %d*\n\n", triage.Meta.Version, triage.Meta.IssueCount))

	// Data hash
	if config.DataHash != "" {
		sb.WriteString(fmt.Sprintf("**Hash:** `%s`\n\n", config.DataHash))
	}

	// Summary stats
	sb.WriteString("## 📈 Summary\n\n")
	sb.WriteString("| Open | In Progress | Blocked | Actionable |\n")
	sb.WriteString("|:----:|:-----------:|:-------:|:----------:|\n")
	sb.WriteString(fmt.Sprintf("| %d | %d | %d | %d |\n\n",
		triage.QuickRef.OpenCount,
		triage.QuickRef.InProgressCount,
		triage.QuickRef.BlockedCount,
		triage.QuickRef.ActionableCount))

	if len(triage.Alerts) > 0 {
		sb.WriteString("## ⚠️ Alerts\n\n")
		for _, alert := range triage.Alerts {
			icon := "ℹ️"
			switch alert.Severity {
			case "critical", "error":
				icon = "🔴"
			case "warning":
				icon = "🟡"
			}
			sb.WriteString(fmt.Sprintf("- %s **%s**: %s\n", icon, sanitizeInlineMarkdown(alert.Type), sanitizeInlineMarkdown(alert.Message)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n\n")

	// Top Recommendations
	sb.WriteString("## 🎯 Top Recommendations\n\n")
	type highRiskEntry struct {
		id          string
		title       string
		score       float64
		explanation string
	}
	var highRiskItems []highRiskEntry
	recRiskScore := func(riskNorm float64, sig *analysis.RiskSignals) float64 {
		if sig != nil && sig.CompositeRisk > 0 {
			return sig.CompositeRisk
		}
		return riskNorm
	}
	if len(triage.Recommendations) == 0 {
		sb.WriteString("*No recommendations available.*\n\n")
	} else {
		sb.WriteString("| # | Issue | Type | P | Score | PR | BW | TI | Risk | Top Reason |\n")
		sb.WriteString("|:-:|-------|:----:|:-:|:-----:|:--:|:--:|:--:|:----:|------------|\n")

		limit := config.MaxRecommendations
		if limit > len(triage.Recommendations) {
			limit = len(triage.Recommendations)
		}

		for i := 0; i < limit; i++ {
			rec := triage.Recommendations[i]
			typeIcon := getTypeIcon(rec.Type)
			reason := "-"
			if len(rec.Reasons) > 0 {
				reason = truncateString(rec.Reasons[0], 30)
			}
			riskVal := recRiskScore(rec.Breakdown.RiskNorm, rec.Breakdown.RiskSignals)
			riskCell := "-"
			if riskVal >= 0.60 {
				riskCell = fmt.Sprintf("🔴 %.0f%%", riskVal*100)
			} else if riskVal >= 0.35 {
				riskCell = fmt.Sprintf("🟡 %.0f%%", riskVal*100)
			} else if riskVal > 0 {
				riskCell = fmt.Sprintf("%.0f%%", riskVal*100)
			}
			sb.WriteString(fmt.Sprintf("| %d | **%s** %s | %s | P%d | %.2f | %s | %s | %s | %s | %s |\n",
				i+1,
				escapeTableText(rec.ID),
				escapeTableText(truncateString(rec.Title, 25)),
				typeIcon,
				rec.Priority,
				rec.Score,
				barChart(rec.Breakdown.PageRankNorm),
				barChart(rec.Breakdown.BetweennessNorm),
				barChart(rec.Breakdown.TimeToImpactNorm),
				riskCell,
				escapeTableText(reason),
			))
		}
		sb.WriteString("\n")
	}
	for _, rec := range triage.Recommendations {
		riskVal := recRiskScore(rec.Breakdown.RiskNorm, rec.Breakdown.RiskSignals)
		if riskVal >= 0.50 {
			expl := "Elevated operational volatility"
			if rec.Breakdown.RiskSignals != nil && strings.TrimSpace(rec.Breakdown.RiskSignals.Explanation) != "" {
				expl = rec.Breakdown.RiskSignals.Explanation
			}
			highRiskItems = append(highRiskItems, highRiskEntry{
				id:          rec.ID,
				title:       rec.Title,
				score:       riskVal,
				explanation: expl,
			})
		}
	}
	if len(highRiskItems) > 0 {
		sort.SliceStable(highRiskItems, func(i, j int) bool {
			return highRiskItems[i].score > highRiskItems[j].score
		})
		limit := min(len(highRiskItems), max(config.MaxRecommendations, 5))
		sb.WriteString("## 🚨 High-Risk Watchlist\n\n")
		sb.WriteString("| Issue | Risk | Assessment |\n")
		sb.WriteString("|-------|:----:|------------|\n")
		for i := 0; i < limit; i++ {
			hr := highRiskItems[i]
			icon := "🟡"
			if hr.score >= 0.60 {
				icon = "🔴"
			}
			sb.WriteString(fmt.Sprintf("| **%s** %s | %s %.0f%% | %s |\n",
				escapeTableText(hr.id),
				escapeTableText(truncateString(hr.title, 28)),
				icon,
				hr.score*100,
				escapeTableText(truncateString(hr.explanation, 55)),
			))
		}
		sb.WriteString("\n")
	}

	// Quick Wins
	sb.WriteString("## ⚡ Quick Wins\n\n")
	if len(triage.QuickWins) == 0 {
		sb.WriteString("*No quick wins identified.*\n\n")
	} else {
		sb.WriteString("| Issue | Reason |\n")
		sb.WriteString("|-------|--------|\n")

		limit := config.MaxQuickWins
		if limit > len(triage.QuickWins) {
			limit = len(triage.QuickWins)
		}

		for i := 0; i < limit; i++ {
			qw := triage.QuickWins[i]
			sb.WriteString(fmt.Sprintf("| **%s** %s | %s |\n",
				escapeTableText(qw.ID),
				escapeTableText(truncateString(qw.Title, 30)),
				escapeTableText(truncateString(qw.Reason, 40)),
			))
		}
		sb.WriteString("\n")
	}

	// Blockers
	sb.WriteString("## 🚧 Blockers to Clear\n\n")
	if len(triage.BlockersToClear) == 0 {
		sb.WriteString("*No critical blockers.*\n\n")
	} else {
		sb.WriteString("| Issue | Unblocks | Ready? |\n")
		sb.WriteString("|-------|:--------:|:------:|\n")

		limit := config.MaxBlockers
		if limit > len(triage.BlockersToClear) {
			limit = len(triage.BlockersToClear)
		}

		for i := 0; i < limit; i++ {
			b := triage.BlockersToClear[i]
			ready := "❌"
			if b.Actionable {
				ready = "✅"
			}
			sb.WriteString(fmt.Sprintf("| **%s** %s | %d | %s |\n",
				escapeTableText(b.ID),
				escapeTableText(truncateString(b.Title, 30)),
				b.UnblocksCount,
				ready,
			))
		}
		sb.WriteString("\n")
	}

	// What-If Unblock Impact
	if config.IncludeWhatIf {
		type whatIfRow struct {
			id      string
			title   string
			targets []string
		}
		var rows []whatIfRow
		seen := make(map[string]bool)
		for _, rec := range triage.Recommendations {
			if len(rec.UnblocksIDs) > 0 && !seen[rec.ID] {
				seen[rec.ID] = true
				rows = append(rows, whatIfRow{id: rec.ID, title: rec.Title, targets: rec.UnblocksIDs})
			}
		}
		for _, b := range triage.BlockersToClear {
			if len(b.UnblocksIDs) > 0 && !seen[b.ID] {
				seen[b.ID] = true
				rows = append(rows, whatIfRow{id: b.ID, title: b.Title, targets: b.UnblocksIDs})
			}
		}
		if len(rows) > 0 {
			sb.WriteString("## 🔮 What-If Impact\n\n")
			sb.WriteString("| Completing | Directly Unblocks | Downstream IDs |\n")
			sb.WriteString("|------------|:-----------------:|----------------|\n")
			limit := config.MaxBlockers
			if limit <= 0 {
				limit = 3
			}
			if limit > len(rows) {
				limit = len(rows)
			}
			for i := 0; i < limit; i++ {
				r := rows[i]
				shown := r.targets
				suffix := ""
				if len(shown) > 3 {
					suffix = fmt.Sprintf(" (+%d more)", len(shown)-3)
					shown = shown[:3]
				}
				escapedShown := make([]string, len(shown))
				for idx, targetID := range shown {
					escapedShown[idx] = escapeTableText(targetID)
				}
				sb.WriteString(fmt.Sprintf("| **%s** %s | %d | %s%s |\n",
					escapeTableText(r.id),
					escapeTableText(truncateString(r.title, 25)),
					len(r.targets),
					strings.Join(escapedShown, ", "),
					suffix,
				))
			}
			sb.WriteString("\n")
		}
	}

	// Global Backlog Capacity (Graham Bounds)
	if config.GlobalCapacity != nil && config.GlobalCapacity.TotalMinutes > 0 {
		cap := config.GlobalCapacity
		critPathDur := formatEstimatedDuration(cap.CriticalPathMinutes)
		if critPathDur == "" {
			critPathDur = "0m"
		}
		sb.WriteString("## ⚡ Global Backlog Capacity (Graham Bounds)\n\n")
		sb.WriteString("| Open | Total Work | Critical Path | Parallelizable | 1 Agent | 2 Agents | 4 Agents |\n")
		sb.WriteString("|:----:|:----------:|:-------------:|:--------------:|:-------:|:--------:|:--------:|\n")
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %.0f%% | %.1fd | %.1f–%.1fd | %.1f–%.1fd |\n\n",
			cap.OpenCount,
			formatEstimatedDuration(cap.TotalMinutes),
			critPathDur,
			cap.ParallelizablePct,
			cap.Days1Agent,
			cap.Days2AgentsMin, cap.Days2AgentsMax,
			cap.Days4AgentsMin, cap.Days4AgentsMax,
		))
		if len(cap.CriticalPathIDs) > 0 {
			escapedCP := make([]string, 0, min(len(cap.CriticalPathIDs), 6))
			for _, id := range cap.CriticalPathIDs {
				if len(escapedCP) >= 6 {
					break
				}
				escapedCP = append(escapedCP, "`"+escapeTableCodeSpan(id)+"`")
			}
			sb.WriteString(fmt.Sprintf("**Critical Chain:** %s\n\n", strings.Join(escapedCP, " → ")))
		}
	}

	// Priority Misalignment Alerts
	if len(config.PriorityRecommendations) > 0 {
		sb.WriteString("## ⚖️ Priority Misalignment Alerts\n\n")
		sb.WriteString("| Issue | Change | Confidence | Primary Signal |\n")
		sb.WriteString("|-------|:------:|:----------:|----------------|\n")
		maxAlerts := config.MaxPriorityAlerts
		if maxAlerts <= 0 {
			maxAlerts = 5
		}
		limit := min(len(config.PriorityRecommendations), maxAlerts)
		for i := 0; i < limit; i++ {
			pr := config.PriorityRecommendations[i]
			arrow := "↓"
			if pr.Direction == "increase" {
				arrow = "↑"
			}
			reason := "-"
			if len(pr.Reasoning) > 0 {
				reason = truncateString(pr.Reasoning[0], 45)
			}
			sb.WriteString(fmt.Sprintf("| **%s** %s | %s P%d→P%d | %.0f%% | %s |\n",
				escapeTableText(pr.IssueID),
				escapeTableText(truncateString(pr.Title, 28)),
				arrow, pr.CurrentPriority, pr.SuggestedPriority,
				pr.Confidence*100,
				escapeTableText(reason),
			))
		}
		sb.WriteString("\n")
	}

	// Cycle Break Suggestions
	if len(config.CycleBreaks) > 0 {
		sb.WriteString("## ↻ Cycle Break Suggestions\n\n")
		sb.WriteString("| Remove Edge | Cycles Broken | Collateral | Rationale |\n")
		sb.WriteString("|-------------|:-------------:|:----------:|-----------|\n")
		maxBreaks := config.MaxCycleBreaks
		if maxBreaks <= 0 {
			maxBreaks = 5
		}
		limit := min(len(config.CycleBreaks), maxBreaks)
		for i := 0; i < limit; i++ {
			cb := config.CycleBreaks[i]
			sb.WriteString(fmt.Sprintf("| `%s` → `%s` | %d | %d | %s |\n",
				escapeTableCodeSpan(cb.EdgeFrom),
				escapeTableCodeSpan(cb.EdgeTo),
				cb.Impact,
				cb.Collateral,
				escapeTableText(truncateString(cb.Rationale, 45)),
			))
		}
		sb.WriteString("\n")
	}

	// Legend
	if config.IncludeLegend {
		sb.WriteString("---\n\n")
		sb.WriteString("## 📖 Legend\n\n")
		sb.WriteString("| Symbol | Meaning |\n")
		sb.WriteString("|:------:|:--------|\n")
		sb.WriteString("| **PR** | PageRank - dependency importance |\n")
		sb.WriteString("| **BW** | Betweenness - critical path frequency |\n")
		sb.WriteString("| **TI** | Time-to-Impact - urgency factor |\n")
		sb.WriteString("| **Risk** | Operational volatility/risk composite (🔴 ≥60%, 🟡 ≥35%) |\n")
		sb.WriteString("| █░░░ | Low (0-25%) |\n")
		sb.WriteString("| ██░░ | Medium (25-50%) |\n")
		sb.WriteString("| ███░ | High (50-75%) |\n")
		sb.WriteString("| ████ | Very High (75-100%) |\n")
	}

	return sb.String(), nil
}

// barChart creates a mini ASCII bar chart for a 0-1 value
func barChart(value float64) string {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	filled := int(value * 4)
	switch filled {
	case 0:
		return "░░░░"
	case 1:
		return "█░░░"
	case 2:
		return "██░░"
	case 3:
		return "███░"
	default:
		return "████"
	}
}

// truncateString truncates a string to maxLen runes with ellipsis.
// Uses rune-based counting to safely handle UTF-8 multi-byte characters.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}

// getTypeIcon returns a compact icon for issue type (for tables)
func getTypeIcon(issueType string) string {
	switch issueType {
	case "bug":
		return "🐛"
	case "feature":
		return "✨"
	case "task":
		return "📋"
	case "epic":
		return "🚀"
	case "chore":
		return "🧹"
	default:
		return "•"
	}
}
