// Package testutil provides test fixture generators for various graph topologies.
// All generators produce deterministic output for reproducible tests.
package testutil

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// GraphFixture represents an abstract graph for testing graph algorithms.
// This is the format used by testdata/graphs/*.json files.
type GraphFixture struct {
	Description string     `json:"description"`
	Nodes       []string   `json:"nodes"`
	Edges       [][2]int   `json:"edges"` // [from_idx, to_idx]
	Properties  Properties `json:"properties,omitempty"`
}

// Properties holds optional metadata about the fixture.
type Properties struct {
	HasCycles     bool `json:"has_cycles,omitempty"`
	IsConnected   bool `json:"is_connected,omitempty"`
	ExpectedDepth int  `json:"expected_depth,omitempty"`
}

// IssueFixture represents a set of issues for integration testing.
type IssueFixture struct {
	Description string        `json:"description"`
	Issues      []model.Issue `json:"issues"`
}

// GeneratorConfig controls issue generation.
type GeneratorConfig struct {
	Seed           int64             // Random seed for determinism (0 = use current time)
	IDPrefix       string            // Prefix for issue IDs (default: "TEST")
	BaseTime       time.Time         // Base time for timestamps (default: fixed time)
	IncludeLabels  bool              // Generate random labels
	IncludeMinutes bool              // Generate estimated_minutes
	StatusMix      []model.Status    // Status distribution (nil = all open)
	TypeMix        []model.IssueType // Type distribution (nil = all task)
}

// DefaultConfig returns a config suitable for most tests.
func DefaultConfig() GeneratorConfig {
	return GeneratorConfig{
		Seed:      42, // Deterministic
		IDPrefix:  "TEST",
		BaseTime:  time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),
		StatusMix: []model.Status{model.StatusOpen},
		TypeMix:   []model.IssueType{model.TypeTask},
	}
}

// Generator creates test fixtures with various topologies.
type Generator struct {
	cfg GeneratorConfig
	rng *rand.Rand
}

// New creates a Generator with the given config.
func New(cfg GeneratorConfig) *Generator {
	seed := cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	if cfg.BaseTime.IsZero() {
		cfg.BaseTime = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	}
	if cfg.IDPrefix == "" {
		cfg.IDPrefix = "TEST"
	}
	if len(cfg.StatusMix) == 0 {
		cfg.StatusMix = []model.Status{model.StatusOpen}
	}
	if len(cfg.TypeMix) == 0 {
		cfg.TypeMix = []model.IssueType{model.TypeTask}
	}
	return &Generator{
		cfg: cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// NewDefault creates a Generator with default config.
func NewDefault() *Generator {
	return New(DefaultConfig())
}

// ============================================================================
// Graph Topology Generators
// ============================================================================

// Chain creates a linear chain: n0 <- n1 <- n2 <- ... <- n{size-1}
// In dependency terms: n1 depends on n0, n2 depends on n1, etc.
// n0 is the root (no dependencies), n{size-1} is the leaf (depends on n{size-2})
// Properties: DAG, depth = size-1, single path
func (g *Generator) Chain(size int) GraphFixture {
	nodes := make([]string, size)
	edges := make([][2]int, 0, size-1)

	for i := 0; i < size; i++ {
		nodes[i] = fmt.Sprintf("n%d", i)
		if i > 0 {
			// Edge [i, i-1] means node i depends on node i-1
			edges = append(edges, [2]int{i, i - 1})
		}
	}

	return GraphFixture{
		Description: fmt.Sprintf("Linear chain of %d nodes: n0 -> n1 -> ... -> n%d", size, size-1),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: size - 1,
		},
	}
}

// Star creates a star topology with a central hub.
// Direction: spokes point TO hub (hub is the dependency)
// Properties: DAG, depth = 1, hub is authority
func (g *Generator) Star(spokes int) GraphFixture {
	size := spokes + 1
	nodes := make([]string, size)
	edges := make([][2]int, spokes)

	nodes[0] = "hub"
	for i := 1; i < size; i++ {
		nodes[i] = fmt.Sprintf("spoke%d", i)
		edges[i-1] = [2]int{i, 0} // spoke -> hub (spoke depends on hub)
	}

	return GraphFixture{
		Description: fmt.Sprintf("Star with hub and %d spokes; spokes depend on hub", spokes),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: 1,
		},
	}
}

// ReverseStar creates a star where hub points to all spokes.
// Direction: hub points TO spokes (spokes are dependencies)
// Properties: DAG, depth = 1, hub is hub (aggregator)
func (g *Generator) ReverseStar(spokes int) GraphFixture {
	size := spokes + 1
	nodes := make([]string, size)
	edges := make([][2]int, spokes)

	nodes[0] = "hub"
	for i := 1; i < size; i++ {
		nodes[i] = fmt.Sprintf("spoke%d", i)
		edges[i-1] = [2]int{0, i} // hub -> spoke (hub depends on spoke)
	}

	return GraphFixture{
		Description: fmt.Sprintf("Reverse star with hub depending on %d spokes", spokes),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: 1,
		},
	}
}

// Diamond creates a diamond dependency pattern.
// Shape: top -> left, top -> right, left -> bottom, right -> bottom
// Generalized: top connects to `width` middle nodes, all connect to bottom
func (g *Generator) Diamond(width int) GraphFixture {
	if width < 1 {
		width = 1
	}

	size := width + 2 // top + middle nodes + bottom
	nodes := make([]string, size)
	edges := make([][2]int, 0, width*2)

	nodes[0] = "top"
	nodes[size-1] = "bottom"

	for i := 1; i <= width; i++ {
		nodes[i] = fmt.Sprintf("mid%d", i)
		edges = append(edges, [2]int{0, i})        // top -> mid
		edges = append(edges, [2]int{i, size - 1}) // mid -> bottom
	}

	return GraphFixture{
		Description: fmt.Sprintf("Diamond with %d middle nodes: top -> mid1..mid%d -> bottom", width, width),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: 2,
		},
	}
}

// Cycle creates a circular dependency (invalid DAG).
// Shape: n0 -> n1 -> n2 -> ... -> n{size-1} -> n0
func (g *Generator) Cycle(size int) GraphFixture {
	nodes := make([]string, size)
	edges := make([][2]int, size)

	for i := 0; i < size; i++ {
		nodes[i] = fmt.Sprintf("n%d", i)
		edges[i] = [2]int{i, (i + 1) % size}
	}

	return GraphFixture{
		Description: fmt.Sprintf("Cycle of %d nodes: n0 -> n1 -> ... -> n%d -> n0", size, size-1),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:   true,
			IsConnected: true,
		},
	}
}

// SelfLoop creates a single node with a self-referential edge.
func (g *Generator) SelfLoop() GraphFixture {
	return GraphFixture{
		Description: "Single node with self-loop",
		Nodes:       []string{"n0"},
		Edges:       [][2]int{{0, 0}},
		Properties: Properties{
			HasCycles:   true,
			IsConnected: true,
		},
	}
}

// Tree creates a tree with given depth and branching factor.
// Each non-leaf node has `breadth` children.
func (g *Generator) Tree(depth, breadth int) GraphFixture {
	if depth < 1 {
		depth = 1
	}
	if breadth < 1 {
		breadth = 1
	}

	var nodes []string
	var edges [][2]int

	// BFS-style generation
	nodeID := 0
	nodes = append(nodes, fmt.Sprintf("n%d", nodeID))
	nodeID++

	// Track nodes at each level
	currentLevel := []int{0}

	for d := 0; d < depth; d++ {
		var nextLevel []int
		for _, parent := range currentLevel {
			for b := 0; b < breadth; b++ {
				child := nodeID
				nodes = append(nodes, fmt.Sprintf("n%d", child))
				edges = append(edges, [2]int{parent, child})
				nextLevel = append(nextLevel, child)
				nodeID++
			}
		}
		currentLevel = nextLevel
	}

	return GraphFixture{
		Description: fmt.Sprintf("Tree with depth=%d, breadth=%d (%d nodes)", depth, breadth, len(nodes)),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: depth,
		},
	}
}

// Disconnected creates multiple isolated components.
// Each component is a small chain of `componentSize` nodes.
func (g *Generator) Disconnected(components, componentSize int) GraphFixture {
	var nodes []string
	var edges [][2]int

	nodeID := 0
	for c := 0; c < components; c++ {
		componentStart := nodeID
		for i := 0; i < componentSize; i++ {
			nodes = append(nodes, fmt.Sprintf("c%d_n%d", c, i))
			if i > 0 {
				edges = append(edges, [2]int{nodeID - 1, nodeID})
			}
			nodeID++
		}
		_ = componentStart // Start of each component
	}

	return GraphFixture{
		Description: fmt.Sprintf("%d disconnected components, each a chain of %d nodes", components, componentSize),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   false,
			ExpectedDepth: componentSize - 1,
		},
	}
}

// Complete creates a complete DAG where every earlier node points to every later node.
// This is a dense graph with n*(n-1)/2 edges.
func (g *Generator) Complete(size int) GraphFixture {
	nodes := make([]string, size)
	edges := make([][2]int, 0, size*(size-1)/2)

	for i := 0; i < size; i++ {
		nodes[i] = fmt.Sprintf("n%d", i)
		for j := i + 1; j < size; j++ {
			edges = append(edges, [2]int{i, j})
		}
	}

	return GraphFixture{
		Description: fmt.Sprintf("Complete DAG with %d nodes (%d edges)", size, len(edges)),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: size - 1,
		},
	}
}

// RandomDAG creates a random directed acyclic graph.
// density is the probability of an edge existing (0.0 to 1.0).
func (g *Generator) RandomDAG(size int, density float64) GraphFixture {
	if density < 0 {
		density = 0
	}
	if density > 1 {
		density = 1
	}

	nodes := make([]string, size)
	var edges [][2]int

	for i := 0; i < size; i++ {
		nodes[i] = fmt.Sprintf("n%d", i)
	}

	// Only add edges from lower index to higher index to ensure DAG
	for i := 0; i < size; i++ {
		for j := i + 1; j < size; j++ {
			if g.rng.Float64() < density {
				edges = append(edges, [2]int{i, j})
			}
		}
	}

	return GraphFixture{
		Description: fmt.Sprintf("Random DAG with %d nodes, density=%.2f (%d edges)", size, density, len(edges)),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:   false,
			IsConnected: false, // May or may not be connected
		},
	}
}

// Bipartite creates a bipartite graph with left nodes depending on right nodes.
func (g *Generator) Bipartite(leftSize, rightSize int) GraphFixture {
	nodes := make([]string, leftSize+rightSize)
	var edges [][2]int

	// Left nodes
	for i := 0; i < leftSize; i++ {
		nodes[i] = fmt.Sprintf("L%d", i)
	}
	// Right nodes
	for i := 0; i < rightSize; i++ {
		nodes[leftSize+i] = fmt.Sprintf("R%d", i)
	}
	// All left nodes depend on all right nodes
	for i := 0; i < leftSize; i++ {
		for j := 0; j < rightSize; j++ {
			edges = append(edges, [2]int{i, leftSize + j})
		}
	}

	return GraphFixture{
		Description: fmt.Sprintf("Bipartite graph: %d left nodes each depend on %d right nodes", leftSize, rightSize),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   leftSize > 0 && rightSize > 0,
			ExpectedDepth: 1,
		},
	}
}

// Ladder creates a ladder-like structure with two parallel chains connected by rungs.
func (g *Generator) Ladder(length int) GraphFixture {
	if length < 1 {
		length = 1
	}

	nodes := make([]string, length*2)
	var edges [][2]int

	// Create two parallel chains
	for i := 0; i < length; i++ {
		nodes[i] = fmt.Sprintf("A%d", i)
		nodes[length+i] = fmt.Sprintf("B%d", i)

		// Chain edges
		if i > 0 {
			edges = append(edges, [2]int{i - 1, i})                   // A chain
			edges = append(edges, [2]int{length + i - 1, length + i}) // B chain
		}
		// Rung edges (A depends on B at same level)
		edges = append(edges, [2]int{i, length + i})
	}

	return GraphFixture{
		Description: fmt.Sprintf("Ladder with %d rungs: two parallel chains A0..A%d and B0..B%d", length, length-1, length-1),
		Nodes:       nodes,
		Edges:       edges,
		Properties: Properties{
			HasCycles:     false,
			IsConnected:   true,
			ExpectedDepth: length,
		},
	}
}

// ============================================================================
// Issue Generators (convert graph fixtures to model.Issue slices)
// ============================================================================

// ToIssues converts a GraphFixture to a slice of model.Issue.
func (g *Generator) ToIssues(gf GraphFixture) []model.Issue {
	issues := make([]model.Issue, len(gf.Nodes))

	// Build node index map
	nodeIdx := make(map[string]int)
	for i, n := range gf.Nodes {
		nodeIdx[n] = i
	}

	// Build adjacency list (who depends on whom)
	// edges are [from, to] meaning "from depends on to"
	deps := make(map[int][]int)
	for _, e := range gf.Edges {
		deps[e[0]] = append(deps[e[0]], e[1])
	}

	for i, nodeName := range gf.Nodes {
		id := fmt.Sprintf("%s-%s", g.cfg.IDPrefix, nodeName)
		title := fmt.Sprintf("Issue %s", nodeName)

		issue := model.Issue{
			ID:        id,
			Title:     title,
			Status:    g.pickStatus(),
			Priority:  g.rng.Intn(5), // P0-P4
			IssueType: g.pickType(),
			CreatedAt: g.cfg.BaseTime.Add(time.Duration(i) * time.Hour),
			UpdatedAt: g.cfg.BaseTime.Add(time.Duration(i) * time.Hour),
		}

		// Add labels if configured
		if g.cfg.IncludeLabels {
			issue.Labels = g.pickLabels()
		}

		// Add estimated minutes if configured
		if g.cfg.IncludeMinutes {
			mins := (g.rng.Intn(8) + 1) * 30 // 30-240 minutes
			issue.EstimatedMinutes = &mins
		}

		// Add dependencies
		if depList, ok := deps[i]; ok {
			for _, depIdx := range depList {
				depID := fmt.Sprintf("%s-%s", g.cfg.IDPrefix, gf.Nodes[depIdx])
				issue.Dependencies = append(issue.Dependencies, &model.Dependency{
					IssueID:     id,
					DependsOnID: depID,
					Type:        model.DepBlocks,
					CreatedAt:   g.cfg.BaseTime,
				})
			}
		}

		issues[i] = issue
	}

	return issues
}

// ToJSONL converts issues to JSONL format (one JSON object per line).
func ToJSONL(issues []model.Issue) string {
	var sb strings.Builder
	for _, issue := range issues {
		data, err := json.Marshal(issue)
		if err != nil {
			continue
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// Helper methods

func (g *Generator) pickStatus() model.Status {
	return g.cfg.StatusMix[g.rng.Intn(len(g.cfg.StatusMix))]
}

func (g *Generator) pickType() model.IssueType {
	return g.cfg.TypeMix[g.rng.Intn(len(g.cfg.TypeMix))]
}

var sampleLabels = []string{"backend", "frontend", "api", "database", "ui", "auth", "performance", "security", "docs", "testing"}

func (g *Generator) pickLabels() []string {
	count := g.rng.Intn(3) + 1 // 1-3 labels
	labels := make([]string, 0, count)
	used := make(map[int]bool)
	for len(labels) < count {
		idx := g.rng.Intn(len(sampleLabels))
		if !used[idx] {
			used[idx] = true
			labels = append(labels, sampleLabels[idx])
		}
	}
	return labels
}

// ============================================================================
// Convenience Functions
// ============================================================================

// QuickChain creates a chain fixture with default settings.
func QuickChain(size int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Chain(size))
}

// QuickStar creates a star fixture with default settings.
func QuickStar(spokes int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Star(spokes))
}

// QuickDiamond creates a diamond fixture with default settings.
func QuickDiamond(width int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Diamond(width))
}

// QuickCycle creates a cycle fixture with default settings.
func QuickCycle(size int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Cycle(size))
}

// QuickTree creates a tree fixture with default settings.
func QuickTree(depth, breadth int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Tree(depth, breadth))
}

// QuickDisconnected creates disconnected components with default settings.
func QuickDisconnected(components, size int) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.Disconnected(components, size))
}

// QuickRandom creates a random DAG with default settings.
func QuickRandom(size int, density float64) []model.Issue {
	gen := NewDefault()
	return gen.ToIssues(gen.RandomDAG(size, density))
}

// PerformanceWorkloadNames identifies the frozen workload families shared by
// CLI and UI measurements. Refresh is an execution mode applied to each family.
func PerformanceWorkloadNames() []string {
	return []string{"realistic", "deep-chain", "wide-dag", "cyclic-dense", "mostly-closed", "unicode"}
}

// PerformanceIssues generates bounded, reproducible project data. Its fixed
// clock and explicit seed are part of the measurement identity; callers also
// hash the serialized issues so a generator change cannot reuse old results.
func PerformanceIssues(kind string, size int, seed int64) ([]model.Issue, error) {
	if size < 2 {
		return nil, fmt.Errorf("performance workload needs at least two issues, got %d", size)
	}
	known := false
	for _, name := range PerformanceWorkloadNames() {
		known = known || name == kind
	}
	if !known {
		return nil, fmt.Errorf("unknown performance workload %q", kind)
	}
	rng := rand.New(rand.NewSource(seed))
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	issues := make([]model.Issue, size)
	for i := range issues {
		issues[i] = model.Issue{
			ID:          fmt.Sprintf("PERF-%06d", i),
			Title:       fmt.Sprintf("Work item %06d: implement and verify component", i),
			Description: "## Acceptance\nImplement the behavior and verify dependency handling.\n",
			Status:      model.StatusOpen,
			Priority:    rng.Intn(5),
			IssueType:   model.TypeTask,
			CreatedAt:   clock.Add(-30 * 24 * time.Hour),
			UpdatedAt:   clock.Add(-time.Duration(i%14) * 24 * time.Hour),
			Labels:      []string{kind, fmt.Sprintf("team-%d", i%6)},
		}
		if kind == "mostly-closed" && i%20 != 0 {
			issues[i].Status = model.StatusClosed
			closed := clock.Add(-24 * time.Hour)
			issues[i].ClosedAt = &closed
		} else if kind == "realistic" && i%5 == 0 {
			issues[i].Status = model.StatusClosed
			closed := clock.Add(-48 * time.Hour)
			issues[i].ClosedAt = &closed
		}
		if kind == "unicode" {
			issues[i].Title = fmt.Sprintf("Work item %06d: 日本語 🚀 café e\u0301 العربية", i)
			issues[i].Description = strings.Repeat("### 日本語 🚀 café e\u0301 العربية\nUnicode text with **Markdown**, `code`, and a dependency explanation.\n", 48)
		}
	}
	for i := range issues {
		parents := make(map[int]bool)
		switch kind {
		case "deep-chain":
			if i > 0 {
				parents[i-1] = true
			}
		case "wide-dag":
			if i >= 32 {
				parents[i%32] = true
				parents[(i+7)%32] = true
			}
		case "cyclic-dense":
			start := i / 32 * 32
			end := min(start+32, size)
			for step := 1; step <= 12 && step < end-start; step++ {
				parents[start+(i-start+step)%(end-start)] = true
			}
		default:
			if i > 0 && i%7 != 0 {
				for j := 0; j < 3; j++ {
					parents[rng.Intn(i)] = true
				}
			}
		}
		// Iterate IDs in order rather than map order: exact serialized bytes and
		// dependency traversal order must remain reproducible across processes.
		ordered := make([]int, 0, len(parents))
		for parent := range parents {
			ordered = append(ordered, parent)
		}
		sort.Ints(ordered)
		for _, parent := range ordered {
			issues[i].Dependencies = append(issues[i].Dependencies, &model.Dependency{
				IssueID: issues[i].ID, DependsOnID: issues[parent].ID, Type: model.DepBlocks,
			})
		}
	}
	return issues, nil
}

// LatencyDistribution reports empirical nearest-rank quantiles. These describe
// the observed cohort, not a population confidence bound or a frame rate.
type LatencyDistribution struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	P99MS   float64 `json:"p99_ms"`
	MaxMS   float64 `json:"max_ms"`
}

func SummarizeLatency(samples []time.Duration) (LatencyDistribution, error) {
	if len(samples) == 0 {
		return LatencyDistribution{}, fmt.Errorf("cannot summarize an empty latency cohort")
	}
	ordered := append([]time.Duration(nil), samples...)
	for _, elapsed := range ordered {
		if elapsed < 0 {
			return LatencyDistribution{}, fmt.Errorf("negative latency %s", elapsed)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	quantile := func(percent int) float64 {
		return float64(ordered[(percent*len(ordered)+99)/100-1]) / float64(time.Millisecond)
	}
	return LatencyDistribution{len(samples), quantile(50), quantile(95), quantile(99), quantile(100)}, nil
}

// CheckInteractionLatency applies the reference-host interaction SLO. The
// 16.67ms frame budget is a separate target; Update+View does not measure paint.
func CheckInteractionLatency(samples []time.Duration) error {
	stats, err := SummarizeLatency(samples)
	if err != nil {
		return err
	}
	if stats.P99MS > 50 {
		return fmt.Errorf("interaction p99 %.3fms exceeds 50ms SLO (%d samples)", stats.P99MS, stats.Samples)
	}
	return nil
}

// PerformanceMetricStates strips only each metric's measured elapsed time.
// States, reasons and approximation sample sizes remain part of parity. A
// missing/unknown metric is an error; skipped/timeout metrics remain explicit.
func PerformanceMetricStates(raw []byte) (json.RawMessage, error) {
	var entries map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("decode metric status: %w", err)
	}
	for _, name := range []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"} {
		entry, ok := entries[name]
		if !ok {
			return nil, fmt.Errorf("missing metric status %s", name)
		}
		var state string
		if err := json.Unmarshal(entry["state"], &state); err != nil {
			return nil, fmt.Errorf("decode %s state: %w", name, err)
		}
		switch state {
		case "computed", "approx", "timeout", "skipped":
		default:
			return nil, fmt.Errorf("unknown %s state %q", name, state)
		}
		delete(entry, "ms")
	}
	return json.Marshal(entries)
}

// Empty returns an empty issue slice for edge case testing.
func Empty() []model.Issue {
	return []model.Issue{}
}

// Single returns a single issue with no dependencies.
func Single() []model.Issue {
	gen := NewDefault()
	return []model.Issue{{
		ID:        fmt.Sprintf("%s-single", gen.cfg.IDPrefix),
		Title:     "Single Issue",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		CreatedAt: gen.cfg.BaseTime,
		UpdatedAt: gen.cfg.BaseTime,
	}}
}
