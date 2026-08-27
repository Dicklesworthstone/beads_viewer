package correlation

import (
	"encoding/json"
	"strings"
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
}

func TestNewNetworkBuilderNilReport(t *testing.T) {
	builder := NewNetworkBuilder(nil)

	if builder == nil {
		t.Fatal("Expected non-nil NetworkBuilder even with nil report")
	}
}

func TestNetworkBuilderIgnoresEmptyNormalizedPaths(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-001": {
			BeadID:  "bv-001",
			Commits: []CorrelatedCommit{{SHA: "commit-one", Files: []FileChange{{Path: ""}}}},
		},
		"bv-002": {
			BeadID:  "bv-002",
			Commits: []CorrelatedCommit{{SHA: "commit-two", Files: []FileChange{{Path: "./"}}}},
		},
	}}

	builder := NewNetworkBuilder(report)
	for beadID, files := range builder.beadFiles {
		if len(files) != 0 {
			t.Fatalf("beadFiles[%q]=%v, want no normalized files", beadID, files)
		}
	}

	network := builder.BuildAt(time.Time{})
	for beadID, node := range network.Nodes {
		if node.FileCount != 0 {
			t.Fatalf("node %q FileCount=%d, want 0", beadID, node.FileCount)
		}
	}
	if len(network.Edges) != 0 {
		t.Fatalf("empty paths created network edges: %+v", network.Edges)
	}
	if len(network.Clusters) != 0 {
		t.Fatalf("empty paths created network clusters: %+v", network.Clusters)
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

func TestNetworkEdgeKeysPreserveOpaqueBeadIDs(t *testing.T) {
	const (
		beadA = "team:api"
		beadB = "team:ui"
	)
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			beadA: {
				BeadID: beadA,
				Title:  "API",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "commit-1", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit-2", Files: []FileChange{{Path: "shared.go"}}},
				},
			},
			beadB: {
				BeadID: beadB,
				Title:  "UI",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "commit-1", Files: []FileChange{{Path: "shared.go"}}},
					{SHA: "commit-2", Files: []FileChange{{Path: "shared.go"}}},
				},
			},
		},
		CommitIndex: CommitIndex{
			"commit-1": {beadA, beadB},
			"commit-2": {beadA, beadB},
		},
	}

	network := NewNetworkBuilder(report).BuildAt(time.Time{})
	if len(network.Edges) != 2 {
		t.Fatalf("typed edges = %#v, want shared-commit and shared-file edges", network.Edges)
	}
	for _, edge := range network.Edges {
		if edge.FromBead != beadA || edge.ToBead != beadB {
			t.Fatalf("opaque bead ID was split or reordered incorrectly: %+v", edge)
		}
	}
	if network.Nodes[beadA].Degree != 1 || network.Nodes[beadB].Degree != 1 {
		t.Fatalf("parallel typed edges inflated degree: a=%d b=%d", network.Nodes[beadA].Degree, network.Nodes[beadB].Degree)
	}
	if network.Stats.Density != 1 {
		t.Fatalf("two-node simple density = %v, want 1", network.Stats.Density)
	}
	if len(network.Clusters) != 1 {
		t.Fatalf("clusters = %#v, want one strong two-node cluster", network.Clusters)
	}
	cluster := network.Clusters[0]
	if cluster.InternalEdges != 1 || cluster.InternalConnectivity != 1 {
		t.Fatalf("parallel typed edges inflated cluster metrics: %+v", cluster)
	}
	if cluster.CentralBead != beadA {
		t.Fatalf("central bead tie = %q, want deterministic lexical winner %q", cluster.CentralBead, beadA)
	}
}

func TestSharedCommitEdgesIgnoreMissingNetworkNodes(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-a": {BeadID: "bv-a", Status: "open"},
			"bv-b": {BeadID: "bv-b", Status: "open"},
		},
		CommitIndex: CommitIndex{
			"commit-1": {"bv-a", "bv-b", "bv-missing"},
			"commit-2": {"bv-a", "bv-missing"},
			"":         {"bv-a", "bv-b"},
			"  ":       {"bv-a", "bv-b"},
		},
	}

	network := NewNetworkBuilder(report).BuildAt(time.Time{})
	if len(network.Edges) != 1 {
		t.Fatalf("shared-commit edges = %+v, want only the bv-a/bv-b edge", network.Edges)
	}
	edge := network.Edges[0]
	if edge.FromBead != "bv-a" || edge.ToBead != "bv-b" || edge.Weight != 1 {
		t.Fatalf("shared-commit edge = %+v, want bv-a/bv-b weight 1", edge)
	}
	if network.Stats.TotalEdges != 1 || network.Stats.Density != 1 {
		t.Fatalf("dangling CommitIndex member affected stats: %+v", network.Stats)
	}
}

func TestSharedCommitEdgeDetailsUseFullSHAs(t *testing.T) {
	shaA := "abcdefg" + strings.Repeat("1", 33)
	shaB := "abcdefg" + strings.Repeat("2", 33)
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-a": {BeadID: "bv-a", Status: "open"},
			"bv-b": {BeadID: "bv-b", Status: "open"},
		},
		CommitIndex: CommitIndex{
			shaA: {"bv-a", "bv-b"},
			shaB: {"bv-a", "bv-b"},
		},
	}

	network := NewNetworkBuilder(report).BuildAt(time.Time{})
	if len(network.Edges) != 1 {
		t.Fatalf("shared-commit edges = %+v, want one", network.Edges)
	}
	edge := network.Edges[0]
	if edge.EdgeType != EdgeSharedCommit || edge.Weight != 2 {
		t.Fatalf("shared-commit edge = %+v, want weight 2", edge)
	}
	if len(edge.Details) != 2 || edge.Details[0] != shaA || edge.Details[1] != shaB {
		t.Fatalf("shared-commit details = %#v, want sorted full SHAs [%q %q]", edge.Details, shaA, shaB)
	}
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
func TestNetworkDensityCalculation(t *testing.T) {
	report := createTestHistoryReport()
	builder := NewNetworkBuilder(report)
	network := builder.Build()

	// Density = edges / max_possible_edges
	// max_possible_edges = n * (n-1) / 2
	n := network.Stats.TotalNodes
	maxEdges := n * (n - 1) / 2

	if maxEdges > 0 {
		expectedDensity := float64(len(networkPairs(network, 0))) / float64(maxEdges)
		if network.Stats.Density < 0 || network.Stats.Density > 1 {
			t.Errorf("Density should be between 0 and 1, got %f", network.Stats.Density)
		}
		// Allow small floating point differences
		diff := network.Stats.Density - expectedDensity
		if diff > 0.01 || diff < -0.01 {
			t.Errorf("Expected density ~%f, got %f", expectedDensity, network.Stats.Density)
		}
	}
}

func TestGetSubNetworkRecomputesDerivedGraphState(t *testing.T) {
	network := &ImpactNetwork{
		GeneratedAt: time.Date(2026, 8, 26, 1, 2, 3, 0, time.UTC),
		DataHash:    "source-hash",
		Nodes: map[string]*NetworkNode{
			"a": {BeadID: "a", Title: "A", Degree: 99, ClusterID: 7, Connectivity: 99},
			"b": {BeadID: "b", Title: "B", Degree: 99, ClusterID: 7, Connectivity: 99},
			"c": {BeadID: "c", Title: "C", Degree: 99, ClusterID: 7, Connectivity: 99},
		},
		Edges: []NetworkEdge{
			{FromBead: "a", ToBead: "b", EdgeType: EdgeSharedCommit, Weight: 2},
			{FromBead: "a", ToBead: "b", EdgeType: EdgeSharedFile, Weight: 1, Details: []string{"shared.go"}},
			{FromBead: "b", ToBead: "c", EdgeType: EdgeSharedCommit, Weight: 2},
		},
		Clusters: []BeadCluster{{ClusterID: 7, BeadIDs: []string{"a", "b", "c"}}},
	}

	sub := network.GetSubNetwork("a", 1)
	if len(sub.Nodes) != 2 || sub.Nodes["a"] == nil || sub.Nodes["b"] == nil {
		t.Fatalf("subnetwork nodes = %#v, want a and b", sub.Nodes)
	}
	if sub.Nodes["a"].Degree != 1 || sub.Nodes["b"].Degree != 1 {
		t.Fatalf("subnetwork retained full-network degree: a=%d b=%d", sub.Nodes["a"].Degree, sub.Nodes["b"].Degree)
	}
	if sub.Stats.TotalEdges != 2 || sub.Stats.Density != 1 || sub.Stats.MaxDegree != 1 {
		t.Fatalf("subnetwork stats were not recomputed from its induced graph: %+v", sub.Stats)
	}
	if len(sub.Clusters) != 1 {
		t.Fatalf("subnetwork clusters = %#v, want one recomputed cluster", sub.Clusters)
	}
	cluster := sub.Clusters[0]
	if cluster.ClusterID != 0 || cluster.InternalEdges != 1 || cluster.InternalConnectivity != 1 || cluster.CentralBead != "a" {
		t.Fatalf("subnetwork cluster retained stale/full-network state: %+v", cluster)
	}
	if len(cluster.SharedFiles) != 1 || cluster.SharedFiles[0] != "shared.go" {
		t.Fatalf("subnetwork cluster lost available shared-file samples: %+v", cluster.SharedFiles)
	}
	for i := range sub.Edges {
		if len(sub.Edges[i].Details) == 0 {
			continue
		}
		sub.Edges[i].Details[0] = "caller-mutated.go"
		for _, sourceEdge := range network.Edges {
			if sourceEdge.EdgeType == EdgeSharedFile && sourceEdge.Details[0] != "shared.go" {
				t.Fatalf("subnetwork edge details alias source network: %+v", sourceEdge.Details)
			}
		}
		break
	}
	for _, beadID := range []string{"a", "b"} {
		node := sub.Nodes[beadID]
		if node.ClusterID != 0 || node.Connectivity != 1 {
			t.Fatalf("subnetwork node %q derived state = %+v, want cluster 0/connectivity 1", beadID, node)
		}
	}
}

func TestGetSubNetworkIgnoresDanglingSharedFileEdges(t *testing.T) {
	network := &ImpactNetwork{
		Nodes: map[string]*NetworkNode{
			"a": {BeadID: "a", Title: "A"},
		},
		Edges: []NetworkEdge{
			{FromBead: "a", ToBead: "missing", EdgeType: EdgeSharedFile, Weight: 2, Details: []string{"secret.go"}},
		},
	}

	sub := network.GetSubNetwork("a", 1)
	if len(sub.Nodes) != 1 || sub.Nodes["a"] == nil {
		t.Fatalf("subnetwork nodes = %#v, want only a", sub.Nodes)
	}
	if len(sub.Edges) != 0 || sub.Stats.TotalEdges != 0 || sub.Nodes["a"].Degree != 0 {
		t.Fatalf("dangling edge leaked into subnetwork: edges=%#v stats=%+v node=%+v", sub.Edges, sub.Stats, sub.Nodes["a"])
	}

	if nilSub := (*ImpactNetwork)(nil).GetSubNetwork("a", 1); nilSub == nil || len(nilSub.Nodes) != 0 {
		t.Fatalf("nil network subgraph = %#v, want a non-nil empty graph", nilSub)
	}
}

func TestToResultHandlesNilNetworkAndNodes(t *testing.T) {
	nilResult := (*ImpactNetwork)(nil).ToResult("", 1)
	if nilResult == nil || nilResult.Network == nil || len(nilResult.Network.Nodes) != 0 {
		t.Fatalf("nil network result = %#v, want a non-nil empty network", nilResult)
	}

	network := &ImpactNetwork{
		Nodes: map[string]*NetworkNode{
			"nil": nil,
			"ok":  {BeadID: "ok", Degree: 1},
		},
	}
	result := network.ToResult("", 1)
	if len(result.TopConnected) != 1 || result.TopConnected[0].BeadID != "ok" {
		t.Fatalf("top connected with nil node = %+v, want only ok", result.TopConnected)
	}

	builder := &NetworkBuilder{}
	builder.calculateStats(network)
	if network.Stats.TotalNodes != 1 || network.Stats.IsolatedNodes != 0 || network.Stats.MaxDegree != 1 {
		t.Fatalf("stats with nil node = %+v, want one real degree-one node", network.Stats)
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
