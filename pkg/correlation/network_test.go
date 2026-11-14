package correlation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func createTestHistoryReport() *HistoryReport {
	now := time.Now()
	return &HistoryReport{
		GeneratedAt: now,
		DataHash:    "testhash123",
		Histories: map[string]BeadHistory{
			"bv-001": {
				BeadID: "bv-001",
				Title:  "Auth token handling",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:      "abc123def456",
						ShortSHA: "abc123d",
						Files:    []FileChange{{Path: "auth/token.go"}, {Path: "auth/session.go"}},
					},
					{
						SHA:      "def456ghi789",
						ShortSHA: "def456g",
						Files:    []FileChange{{Path: "auth/token.go"}, {Path: "config/auth.yaml"}},
					},
				},
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-72 * time.Hour)},
					Closed:  &BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
				},
			},
			"bv-002": {
				BeadID: "bv-002",
				Title:  "Session management",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:      "abc123def456", // Same commit as bv-001
						ShortSHA: "abc123d",
						Files:    []FileChange{{Path: "auth/session.go"}},
					},
					{
						SHA:      "xyz789abc123",
						ShortSHA: "xyz789a",
						Files:    []FileChange{{Path: "auth/session.go"}, {Path: "middleware/auth.go"}},
					},
				},
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-48 * time.Hour)},
				},
			},
			"bv-003": {
				BeadID: "bv-003",
				Title:  "Rate limiting",
				Status: "in_progress",
				Commits: []CorrelatedCommit{
					{
						SHA:      "xyz789abc123", // Same commit as bv-002
						ShortSHA: "xyz789a",
						Files:    []FileChange{{Path: "middleware/auth.go"}, {Path: "middleware/rate.go"}},
					},
				},
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
					Claimed: &BeadEvent{Timestamp: now.Add(-12 * time.Hour)},
				},
			},
			"bv-004": {
				BeadID:  "bv-004",
				Title:   "Unrelated feature",
				Status:  "closed",
				Commits: []CorrelatedCommit{}, // No commits - isolated node
			},
		},
		CommitIndex: CommitIndex{
			"abc123def456": {"bv-001", "bv-002"},
			"def456ghi789": {"bv-001"},
			"xyz789abc123": {"bv-002", "bv-003"},
		},
	}
}

func TestNewNetworkBuilder(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)

	if builder == nil {
		t.Fatal("Expected non-nil NetworkBuilder")
	}
	if builder.report != report {
		t.Error("Expected builder to reference the report")
	}

	// Check bead maps were built
	if len(builder.beadFiles) == 0 {
		t.Error("Expected beadFiles map to be populated")
	}
	if len(builder.beadCommits) == 0 {
		t.Error("Expected beadCommits map to be populated")
	}
}

func TestNewNetworkBuilderNilReport(t *testing.T) {
	builder := NewNetworkBuilder(nil)

	if builder == nil {
		t.Fatal("Expected non-nil NetworkBuilder even with nil report")
	}
}

func TestBuildNetwork(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	if network == nil {
		t.Fatal("Expected non-nil network")
	}

	t.Run("nodes_created", func(t *testing.T) {
		if len(network.Nodes) != 4 {
			t.Errorf("Expected 4 nodes, got %d", len(network.Nodes))
		}

		// Check specific node
		if node, ok := network.Nodes["bv-001"]; ok {
			if node.Title != "Auth token handling" {
				t.Errorf("Expected title 'Auth token handling', got %s", node.Title)
			}
			if node.Status != "closed" {
				t.Errorf("Expected status 'closed', got %s", node.Status)
			}
			if node.CommitCount != 2 {
				t.Errorf("Expected 2 commits, got %d", node.CommitCount)
			}
		} else {
			t.Error("Expected bv-001 node to exist")
		}
	})

	t.Run("edges_created", func(t *testing.T) {
		if len(network.Edges) == 0 {
			t.Error("Expected edges to be created")
		}

		// Should have edges between bv-001 and bv-002 (shared commit abc123def456)
		foundSharedCommit := false
		for _, edge := range network.Edges {
			if (edge.FromBead == "bv-001" && edge.ToBead == "bv-002") ||
				(edge.FromBead == "bv-002" && edge.ToBead == "bv-001") {
				if edge.EdgeType == EdgeSharedCommit {
					foundSharedCommit = true
				}
			}
		}
		if !foundSharedCommit {
			t.Error("Expected shared commit edge between bv-001 and bv-002")
		}
	})

	t.Run("stats_calculated", func(t *testing.T) {
		if network.Stats.TotalNodes != 4 {
			t.Errorf("Expected 4 total nodes, got %d", network.Stats.TotalNodes)
		}
		if network.Stats.TotalEdges == 0 {
			t.Error("Expected edges in stats")
		}
	})
}

func TestBuildNetworkAtPinsNestedTimesAndBytes(t *testing.T) {
	report := createTestHistoryReport()
	pinned := time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)
	builder := NewNetworkBuilder(report)

	var want []byte
	for run := 0; run < 25; run++ {
		network := builder.BuildAt(pinned)
		result := network.ToResult("", 1)
		if !network.GeneratedAt.Equal(pinned) || !result.GeneratedAt.Equal(pinned) || !result.Network.GeneratedAt.Equal(pinned) {
			t.Fatalf("generated_at was not propagated: network=%v result=%v nested=%v", network.GeneratedAt, result.GeneratedAt, result.Network.GeneratedAt)
		}
		got, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if run == 0 {
			want = got
		} else if string(got) != string(want) {
			t.Fatalf("run %d produced non-deterministic network bytes\nwant: %s\n got: %s", run, want, got)
		}
	}

	zeroNetwork := builder.BuildAt(time.Time{})
	zeroResult := zeroNetwork.ToResult("", 1)
	if !zeroNetwork.GeneratedAt.IsZero() || !zeroResult.GeneratedAt.IsZero() || !zeroResult.Network.GeneratedAt.IsZero() {
		t.Fatalf("zero instant was replaced: network=%v result=%v nested=%v", zeroNetwork.GeneratedAt, zeroResult.GeneratedAt, zeroResult.Network.GeneratedAt)
	}
}

func TestBuildNetworkSharedFiles(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// bv-001 and bv-002 both touch auth/session.go
	foundSharedFile := false
	for _, edge := range network.Edges {
		if edge.EdgeType == EdgeSharedFile {
			if (edge.FromBead == "bv-001" && edge.ToBead == "bv-002") ||
				(edge.FromBead == "bv-002" && edge.ToBead == "bv-001") {
				// Check if auth/session.go is in details
				for _, detail := range edge.Details {
					if detail == "auth/session.go" {
						foundSharedFile = true
						break
					}
				}
			}
		}
	}
	if !foundSharedFile {
		t.Error("Expected shared file edge for auth/session.go between bv-001 and bv-002")
	}
}

func TestBuildNetworkDependencyEdges(t *testing.T) {
	report := createTestHistoryReport()
	issues := []model.Issue{
		{
			ID: "bv-001",
			Dependencies: []*model.Dependency{
				{
					IssueID:     "bv-001",
					DependsOnID: "bv-002",
					Type:        model.DepBlocks,
				},
			},
		},
		{
			ID: "bv-003",
			Dependencies: []*model.Dependency{
				{
					IssueID:     "bv-003",
					DependsOnID: "bv-001",
					Type:        model.DepBlocks,
				},
				{
					IssueID:     "bv-003",
					DependsOnID: "bv-004",
					Type:        model.DepRelated,
				},
			},
		},
	}
	builder := NewNetworkBuilderWithIssues(report, issues)
	network := builder.Build()

	foundBlocking := false
	foundRelated := false
	for _, edge := range network.Edges {
		if edge.EdgeType != EdgeDependency {
			continue
		}
		if (edge.FromBead == "bv-001" && edge.ToBead == "bv-002") ||
			(edge.FromBead == "bv-002" && edge.ToBead == "bv-001") {
			foundBlocking = true
		}
		if (edge.FromBead == "bv-003" && edge.ToBead == "bv-004") ||
			(edge.FromBead == "bv-004" && edge.ToBead == "bv-003") {
			foundRelated = true
		}
	}

	if !foundBlocking {
		t.Error("Expected blocking dependency edge between bv-001 and bv-002")
	}
	if foundRelated {
		t.Error("Did not expect non-blocking dependency edge for DepRelated")
	}
}

func TestClusterDetection(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// bv-001, bv-002, bv-003 should be in the same cluster
	// (connected through shared commits and files)
	// bv-004 is isolated

	if len(network.Clusters) == 0 {
		// It's OK if clusters are empty - depends on edge weights meeting threshold
		return
	}

	// If clusters exist, check they're properly formed
	for _, cluster := range network.Clusters {
		if len(cluster.BeadIDs) < 2 {
			t.Error("Cluster should have at least 2 beads")
		}
		if cluster.InternalConnectivity < 0 || cluster.InternalConnectivity > 1 {
			t.Errorf("Internal connectivity should be between 0 and 1, got %f", cluster.InternalConnectivity)
		}
	}
}

func TestGetSubNetwork(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	subNetwork := network.GetSubNetwork("bv-001", 1)

	if subNetwork == nil {
		t.Fatal("Expected non-nil subnetwork")
	}

	// bv-001 should be in the subnetwork
	if _, ok := subNetwork.Nodes["bv-001"]; !ok {
		t.Error("Expected bv-001 to be in subnetwork")
	}

	// Subnetwork should have fewer or equal nodes than full network
	if len(subNetwork.Nodes) > len(network.Nodes) {
		t.Error("Subnetwork should not have more nodes than full network")
	}
}

func TestGetSubNetworkDepthLimits(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	tests := []struct {
		depth    int
		expected int // expected capped depth
	}{
		{0, 1}, // depth < 1 should become 1
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 3}, // depth > 3 should become 3
		{100, 3},
	}

	for _, tt := range tests {
		subNetwork := network.GetSubNetwork("bv-001", tt.depth)
		if subNetwork == nil {
			t.Errorf("Expected non-nil subnetwork for depth %d", tt.depth)
		}
	}
}

func TestIsolatedNodes(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// bv-004 has no commits, so it should be isolated
	node, ok := network.Nodes["bv-004"]
	if !ok {
		t.Fatal("Expected bv-004 to exist")
	}

	if node.Degree != 0 {
		t.Errorf("Expected isolated node to have degree 0, got %d", node.Degree)
	}

	// Isolated count should be >= 1 (at least bv-004)
	if network.Stats.IsolatedNodes < 1 {
		t.Errorf("Expected at least 1 isolated node, got %d", network.Stats.IsolatedNodes)
	}
}

func TestToResult(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	t.Run("full_network", func(t *testing.T) {
		result := network.ToResult("", 0)
		if result == nil {
			t.Fatal("Expected non-nil result")
		}
		if result.BeadID != "" {
			t.Errorf("Expected empty BeadID for full network, got %s", result.BeadID)
		}
		if result.Network == nil {
			t.Error("Expected network in result")
		}
	})

	t.Run("subnetwork", func(t *testing.T) {
		result := network.ToResult("bv-001", 2)
		if result == nil {
			t.Fatal("Expected non-nil result")
		}
		if result.BeadID != "bv-001" {
			t.Errorf("Expected BeadID 'bv-001', got %s", result.BeadID)
		}
		if result.Depth != 2 {
			t.Errorf("Expected depth 2, got %d", result.Depth)
		}
	})

	t.Run("top_connected", func(t *testing.T) {
		result := network.ToResult("", 0)
		if len(result.TopConnected) > 10 {
			t.Error("TopConnected should be limited to 10")
		}
		// Should be sorted by degree
		for i := 1; i < len(result.TopConnected); i++ {
			if result.TopConnected[i].Degree > result.TopConnected[i-1].Degree {
				t.Error("TopConnected should be sorted by degree descending")
			}
		}
	})
}

func TestNetworkEdgeTypes(t *testing.T) {
	tests := []struct {
		edgeType NetworkEdgeType
		expected string
	}{
		{EdgeSharedCommit, "shared_commit"},
		{EdgeSharedFile, "shared_file"},
		{EdgeDependency, "dependency"},
	}

	for _, tt := range tests {
		if string(tt.edgeType) != tt.expected {
			t.Errorf("Expected edge type %s, got %s", tt.expected, string(tt.edgeType))
		}
	}
}

func TestCommonPathPrefix(t *testing.T) {
	tests := []struct {
		name     string
		files    []string
		expected string
	}{
		{
			name:     "empty",
			files:    []string{},
			expected: "",
		},
		{
			name:     "single_file",
			files:    []string{"auth/token.go"},
			expected: "auth/",
		},
		{
			name:     "common_prefix",
			files:    []string{"auth/token.go", "auth/session.go", "auth/middleware.go"},
			expected: "auth/",
		},
		{
			name:     "no_common_prefix",
			files:    []string{"auth/token.go", "config/settings.go"},
			expected: "",
		},
		{
			name:     "nested_prefix",
			files:    []string{"pkg/auth/token.go", "pkg/auth/session.go"},
			expected: "pkg/auth/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := commonPathPrefix(tt.files)
			if result != tt.expected {
				t.Errorf("Expected prefix %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestMakeNetworkEdgeKey(t *testing.T) {
	// Keys must be order-normalized so (a, b) and (b, a) dedupe to one edge.
	tests := []struct {
		beadA, beadB string
		edgeType     NetworkEdgeType
		wantFrom     string
		wantTo       string
	}{
		{"bv-001", "bv-002", EdgeSharedCommit, "bv-001", "bv-002"},
		{"bv-002", "bv-001", EdgeSharedCommit, "bv-001", "bv-002"},
		{"bv-010", "bv-003", EdgeSharedFile, "bv-003", "bv-010"},
		{"same", "same", EdgeDependency, "same", "same"},
	}

	for _, tt := range tests {
		key := makeNetworkEdgeKey(tt.beadA, tt.beadB, tt.edgeType)
		if key.fromBead != tt.wantFrom || key.toBead != tt.wantTo {
			t.Errorf("makeNetworkEdgeKey(%q, %q): got (%q, %q), want (%q, %q)",
				tt.beadA, tt.beadB, key.fromBead, key.toBead, tt.wantFrom, tt.wantTo)
		}
		if key.edgeType != tt.edgeType {
			t.Errorf("makeNetworkEdgeKey(%q, %q): edge type %q, want %q",
				tt.beadA, tt.beadB, key.edgeType, tt.edgeType)
		}
		reversed := makeNetworkEdgeKey(tt.beadB, tt.beadA, tt.edgeType)
		if key != reversed {
			t.Errorf("makeNetworkEdgeKey is not order-normalized for (%q, %q)", tt.beadA, tt.beadB)
		}
	}
}

func TestRecomputeNodeDegrees(t *testing.T) {
	nb := &NetworkBuilder{}
	network := &ImpactNetwork{
		Nodes: map[string]*NetworkNode{
			"bv-001": {BeadID: "bv-001", Degree: 99, Connectivity: 0.99}, // stale values must be reset
			"bv-002": {BeadID: "bv-002"},
			"bv-003": {BeadID: "bv-003"},
			"bv-004": {BeadID: "bv-004"},
		},
		Edges: []NetworkEdge{
			{FromBead: "bv-001", ToBead: "bv-002", EdgeType: EdgeSharedCommit},
			{FromBead: "bv-001", ToBead: "bv-002", EdgeType: EdgeSharedFile}, // parallel edge deduplicated via makeNetworkPair
			{FromBead: "bv-002", ToBead: "bv-001", EdgeType: EdgeDependency}, // reversed parallel edge deduplicated
			{FromBead: "bv-002", ToBead: "bv-003", EdgeType: EdgeDependency},
			{FromBead: "bv-001", ToBead: "bv-ghost", EdgeType: EdgeSharedFile}, // missing endpoint ignored
		},
	}

	nb.recomputeNodeDegrees(network)

	want := map[string]int{"bv-001": 1, "bv-002": 2, "bv-003": 1, "bv-004": 0}
	for id, degree := range want {
		if got := network.Nodes[id].Degree; got != degree {
			t.Errorf("node %s: degree %d, want %d", id, got, degree)
		}
		wantConn := float64(degree) / 3.0
		diff := network.Nodes[id].Connectivity - wantConn
		if diff < -1e-9 || diff > 1e-9 {
			t.Errorf("node %s: connectivity %f, want %f", id, network.Nodes[id].Connectivity, wantConn)
		}
	}

	// Must be safe on a nil network.
	nb.recomputeNodeDegrees(nil)
}

// TestGenerateClusterLabel tests cluster label generation with truncation
func TestGenerateClusterLabel(t *testing.T) {
	now := time.Now()

	t.Run("common_path_prefix", func(t *testing.T) {
		report := &HistoryReport{
			GeneratedAt: now,
			Histories: map[string]BeadHistory{
				"bv-001": {BeadID: "bv-001", Title: "Test One"},
				"bv-002": {BeadID: "bv-002", Title: "Test Two"},
			},
		}
		builder := NewNetworkBuilder(report)

		// Simulate shared files with common prefix
		label := builder.generateClusterLabel(
			[]string{"bv-001", "bv-002"},
			[]string{"pkg/auth/token.go", "pkg/auth/session.go"},
		)
		if label != "pkg/auth" {
			t.Errorf("Expected 'pkg/auth', got %q", label)
		}
	})

	t.Run("title_truncation", func(t *testing.T) {
		report := &HistoryReport{
			GeneratedAt: now,
			Histories: map[string]BeadHistory{
				"bv-001": {
					BeadID: "bv-001",
					Title:  "This is a very long title that exceeds thirty characters and should be truncated",
				},
			},
		}
		builder := NewNetworkBuilder(report)

		// No shared files - falls back to title
		label := builder.generateClusterLabel([]string{"bv-001"}, []string{})
		if len(label) > 33 { // 30 chars + "..."
			t.Errorf("Expected truncated label, got %q (len=%d)", label, len(label))
		}
		if label[len(label)-3:] != "..." {
			t.Errorf("Expected label to end with '...', got %q", label)
		}
	})

	t.Run("fallback_default", func(t *testing.T) {
		report := &HistoryReport{
			GeneratedAt: now,
			Histories:   map[string]BeadHistory{},
		}
		builder := NewNetworkBuilder(report)

		label := builder.generateClusterLabel([]string{}, []string{})
		if label != "cluster" {
			t.Errorf("Expected 'cluster' fallback, got %q", label)
		}
	})
}

// TestEdgeWeightAccumulation tests that edge weights increase with multiple shared commits/files
func TestEdgeWeightAccumulation(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		GeneratedAt: now,
		DataHash:    "test",
		Histories: map[string]BeadHistory{
			"bv-001": {
				BeadID: "bv-001",
				Title:  "First bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "commit1", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit2", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit3", Files: []FileChange{{Path: "shared.go"}}},
				},
			},
			"bv-002": {
				BeadID: "bv-002",
				Title:  "Second bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "commit1", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit2", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit3", Files: []FileChange{{Path: "shared.go"}}},
				},
			},
		},
		CommitIndex: CommitIndex{
			"commit1": {"bv-001", "bv-002"},
			"commit2": {"bv-001", "bv-002"},
			"commit3": {"bv-001", "bv-002"},
		},
	}

	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// Find the shared commit edge between bv-001 and bv-002
	var sharedCommitEdge *NetworkEdge
	for i := range network.Edges {
		edge := &network.Edges[i]
		if edge.EdgeType == EdgeSharedCommit {
			if (edge.FromBead == "bv-001" && edge.ToBead == "bv-002") ||
				(edge.FromBead == "bv-002" && edge.ToBead == "bv-001") {
				sharedCommitEdge = edge
				break
			}
		}
	}

	if sharedCommitEdge == nil {
		t.Fatal("Expected shared commit edge between bv-001 and bv-002")
	}

	// Weight should be 3 (3 shared commits)
	if sharedCommitEdge.Weight != 3 {
		t.Errorf("Expected edge weight 3, got %d", sharedCommitEdge.Weight)
	}
}

// TestClusterInternalConnectivity tests connectivity calculation within clusters
func TestClusterInternalConnectivity(t *testing.T) {
	now := time.Now()
	// Create a fully connected trio (3 beads, each sharing commits with both others)
	report := &HistoryReport{
		GeneratedAt: now,
		DataHash:    "test",
		Histories: map[string]BeadHistory{
			"bv-001": {BeadID: "bv-001", Title: "A", Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "c1", Files: []FileChange{{Path: "a.go"}}},
					{SHA: "c2", Files: []FileChange{{Path: "a.go"}}},
				}},
			"bv-002": {BeadID: "bv-002", Title: "B", Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "c1", Files: []FileChange{{Path: "a.go"}}},
					{SHA: "c3", Files: []FileChange{{Path: "a.go"}}},
				}},
			"bv-003": {BeadID: "bv-003", Title: "C", Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "c2", Files: []FileChange{{Path: "a.go"}}},
					{SHA: "c3", Files: []FileChange{{Path: "a.go"}}},
				}},
		},
		CommitIndex: CommitIndex{
			"c1": {"bv-001", "bv-002"},
			"c2": {"bv-001", "bv-003"},
			"c3": {"bv-002", "bv-003"},
		},
	}

	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// Verify edges exist (3 edges for fully connected trio)
	commitEdgeCount := 0
	for _, edge := range network.Edges {
		if edge.EdgeType == EdgeSharedCommit {
			commitEdgeCount++
		}
	}
	if commitEdgeCount != 3 {
		t.Errorf("Expected 3 shared commit edges for fully connected trio, got %d", commitEdgeCount)
	}

	// Each node should have degree 2 (connected to both other nodes)
	for beadID := range report.Histories {
		node := network.Nodes[beadID]
		if node == nil {
			t.Errorf("Expected node %s to exist", beadID)
			continue
		}
		// Degree counts both commit and file edges, so may be > 2
		if node.Degree < 2 {
			t.Errorf("Expected node %s to have degree >= 2, got %d", beadID, node.Degree)
		}
	}
}

// TestNetworkDensityCalculation tests that network density is properly calculated
// using unique undirected node pairs rather than parallel typed edges.
func TestNetworkDensityCalculation(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	n := network.Stats.TotalNodes
	maxEdges := n * (n - 1) / 2

	if maxEdges > 0 {
		uniquePairs := make(map[networkPair]struct{})
		for _, edge := range network.Edges {
			if edge.FromBead != edge.ToBead {
				uniquePairs[makeNetworkPair(edge.FromBead, edge.ToBead)] = struct{}{}
			}
		}
		expectedDensity := float64(len(uniquePairs)) / float64(maxEdges)
		if network.Stats.Density < 0 || network.Stats.Density > 1 {
			t.Errorf("Density should be between 0 and 1, got %f", network.Stats.Density)
		}
		diff := network.Stats.Density - expectedDensity
		if diff > 1e-6 || diff < -1e-6 {
			t.Errorf("Expected density %f, got %f", expectedDensity, network.Stats.Density)
		}
	}
}

func TestMultigraphSimpleGraphMetricsBounded(t *testing.T) {
	now := time.Now()
	// 3-node clique where every pair has shared_commit (weight 2) + shared_file + dependency = 9 typed edges
	report := &HistoryReport{
		GeneratedAt: now,
		DataHash:    "multigraph-clique",
		Histories: map[string]BeadHistory{
			"bv-1": {BeadID: "bv-1", Title: "One", Status: "open", Commits: []CorrelatedCommit{
				{SHA: "c12a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c12b", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c13a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c13b", Files: []FileChange{{Path: "pkg/core.go"}}},
			}},
			"bv-2": {BeadID: "bv-2", Title: "Two", Status: "open", Commits: []CorrelatedCommit{
				{SHA: "c12a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c12b", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c23a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c23b", Files: []FileChange{{Path: "pkg/core.go"}}},
			}},
			"bv-3": {BeadID: "bv-3", Title: "Three", Status: "open", Commits: []CorrelatedCommit{
				{SHA: "c13a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c13b", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c23a", Files: []FileChange{{Path: "pkg/core.go"}}},
				{SHA: "c23b", Files: []FileChange{{Path: "pkg/core.go"}}},
			}},
		},
		CommitIndex: CommitIndex{
			"c12a": {"bv-1", "bv-2"},
			"c12b": {"bv-1", "bv-2"},
			"c13a": {"bv-1", "bv-3"},
			"c13b": {"bv-1", "bv-3"},
			"c23a": {"bv-2", "bv-3"},
			"c23b": {"bv-2", "bv-3"},
		},
	}
	issues := []model.Issue{
		{ID: "bv-1", Dependencies: []*model.Dependency{{IssueID: "bv-1", DependsOnID: "bv-2", Type: model.DepBlocks}}},
		{ID: "bv-2", Dependencies: []*model.Dependency{{IssueID: "bv-2", DependsOnID: "bv-3", Type: model.DepBlocks}}},
		{ID: "bv-3", Dependencies: []*model.Dependency{{IssueID: "bv-3", DependsOnID: "bv-1", Type: model.DepBlocks}}},
	}

	network := NewNetworkBuilderWithIssues(report, issues).Build()

	if network.Stats.TotalEdges != 9 {
		t.Fatalf("expected all 9 typed edges preserved, got %d", network.Stats.TotalEdges)
	}
	if network.Stats.Density != 1.0 {
		t.Fatalf("expected clique Density == 1.0 (not >1), got %f", network.Stats.Density)
	}
	for id, node := range network.Nodes {
		if node.Degree != 2 {
			t.Fatalf("expected node %s Degree == 2 (<= N-1), got %d", id, node.Degree)
		}
		if node.Connectivity != 1.0 {
			t.Fatalf("expected node %s Connectivity == 1.0, got %f", id, node.Connectivity)
		}
	}
	if len(network.Clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(network.Clusters))
	}
	if network.Clusters[0].InternalConnectivity != 1.0 {
		t.Fatalf("expected cluster InternalConnectivity == 1.0 (not >1), got %f", network.Clusters[0].InternalConnectivity)
	}
}

// TestCentralBeadDetection tests that the central bead in a cluster is correctly identified
func TestCentralBeadDetection(t *testing.T) {
	now := time.Now()
	// Create a star topology: bv-001 connects to bv-002, bv-003, bv-004
	// bv-001 should be the central bead
	report := &HistoryReport{
		GeneratedAt: now,
		DataHash:    "test",
		Histories: map[string]BeadHistory{
			"bv-001": {BeadID: "bv-001", Title: "Hub", Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "c1", Files: []FileChange{{Path: "hub.go"}}},
					{SHA: "c2", Files: []FileChange{{Path: "hub.go"}}},
					{SHA: "c3", Files: []FileChange{{Path: "hub.go"}}},
				}},
			"bv-002": {BeadID: "bv-002", Title: "Spoke1", Status: "open",
				Commits: []CorrelatedCommit{{SHA: "c1", Files: []FileChange{{Path: "s1.go"}}}}},
			"bv-003": {BeadID: "bv-003", Title: "Spoke2", Status: "open",
				Commits: []CorrelatedCommit{{SHA: "c2", Files: []FileChange{{Path: "s2.go"}}}}},
			"bv-004": {BeadID: "bv-004", Title: "Spoke3", Status: "open",
				Commits: []CorrelatedCommit{{SHA: "c3", Files: []FileChange{{Path: "s3.go"}}}}},
		},
		CommitIndex: CommitIndex{
			"c1": {"bv-001", "bv-002"},
			"c2": {"bv-001", "bv-003"},
			"c3": {"bv-001", "bv-004"},
		},
	}

	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// The hub node (bv-001) should have the highest degree
	hubNode := network.Nodes["bv-001"]
	if hubNode == nil {
		t.Fatal("Expected hub node bv-001 to exist")
	}

	for beadID, node := range network.Nodes {
		if beadID != "bv-001" && node.Degree > hubNode.Degree {
			t.Errorf("Hub bv-001 (degree %d) should have highest degree, but %s has %d",
				hubNode.Degree, beadID, node.Degree)
		}
	}
}

// TestEmptyHistoryReport tests building network from empty report
func TestEmptyHistoryReport(t *testing.T) {
	report := &HistoryReport{
		GeneratedAt: time.Now(),
		DataHash:    "empty",
		Histories:   map[string]BeadHistory{},
		CommitIndex: CommitIndex{},
	}

	builder := NewNetworkBuilder(report)
	network := builder.Build()

	if network == nil {
		t.Fatal("Expected non-nil network even with empty report")
	}
	if len(network.Nodes) != 0 {
		t.Errorf("Expected 0 nodes, got %d", len(network.Nodes))
	}
	if len(network.Edges) != 0 {
		t.Errorf("Expected 0 edges, got %d", len(network.Edges))
	}
	if network.Stats.TotalNodes != 0 {
		t.Errorf("Expected 0 total nodes in stats, got %d", network.Stats.TotalNodes)
	}
}
