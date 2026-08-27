// Package correlation provides impact network analysis for bead relationships.
package correlation

import (
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// NetworkEdgeType categorizes the types of connections between beads.
type NetworkEdgeType string

const (
	// EdgeSharedCommit indicates beads are linked via a common commit
	EdgeSharedCommit NetworkEdgeType = "shared_commit"
	// EdgeSharedFile indicates beads touched the same file
	EdgeSharedFile NetworkEdgeType = "shared_file"
	// EdgeDependency indicates an explicit blocking/dependency relationship
	EdgeDependency NetworkEdgeType = "dependency"
)

// NetworkEdge represents a connection between two beads.
type NetworkEdge struct {
	FromBead string          `json:"from_bead"`
	ToBead   string          `json:"to_bead"`
	EdgeType NetworkEdgeType `json:"edge_type"`
	Weight   int             `json:"weight"`  // Number of shared commits/files
	Details  []string        `json:"details"` // Sample full commit SHAs, file paths, or dependency descriptions
}

// networkEdgeKey is the lossless internal identity of a typed edge. Bead IDs
// are opaque strings and may themselves contain punctuation such as ':', so
// delimiter-joined keys cannot be parsed safely.
type networkEdgeKey struct {
	fromBead string
	toBead   string
	edgeType NetworkEdgeType
}

// networkPair is the identity of an unordered relationship between two beads.
// Typed edges remain distinct in ImpactNetwork.Edges, while simple-graph
// metrics (degree, density, and connectivity) count each pair only once.
type networkPair struct {
	first  string
	second string
}

func makeNetworkEdgeKey(beadA, beadB string, edgeType NetworkEdgeType) networkEdgeKey {
	if beadA > beadB {
		beadA, beadB = beadB, beadA
	}
	return networkEdgeKey{fromBead: beadA, toBead: beadB, edgeType: edgeType}
}

func makeNetworkPair(beadA, beadB string) networkPair {
	if beadA > beadB {
		beadA, beadB = beadB, beadA
	}
	return networkPair{first: beadA, second: beadB}
}

// networkPairs returns the valid unordered node pairs represented by the
// network. Multiple typed edges between the same two beads deliberately
// collapse to one pair for simple-graph metrics. When minWeight is positive,
// a pair is included if at least one of its typed edges meets the threshold.
func networkPairs(network *ImpactNetwork, minWeight int) map[networkPair]struct{} {
	pairs := make(map[networkPair]struct{})
	if network == nil {
		return pairs
	}
	for _, edge := range network.Edges {
		if edge.FromBead == edge.ToBead || (minWeight > 0 && edge.Weight < minWeight) {
			continue
		}
		fromNode, fromOK := network.Nodes[edge.FromBead]
		toNode, toOK := network.Nodes[edge.ToBead]
		if !fromOK || !toOK || fromNode == nil || toNode == nil {
			continue
		}
		pairs[makeNetworkPair(edge.FromBead, edge.ToBead)] = struct{}{}
	}
	return pairs
}

func networkAdjacency(pairs map[networkPair]struct{}) map[string][]string {
	adj := make(map[string][]string)
	for pair := range pairs {
		adj[pair.first] = append(adj[pair.first], pair.second)
		adj[pair.second] = append(adj[pair.second], pair.first)
	}
	for beadID := range adj {
		sort.Strings(adj[beadID])
	}
	return adj
}

// NetworkNode represents a bead in the impact network.
type NetworkNode struct {
	BeadID       string    `json:"bead_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Priority     int       `json:"priority"`
	LastActivity time.Time `json:"last_activity"`
	Degree       int       `json:"degree"`       // Number of connections
	ClusterID    int       `json:"cluster_id"`   // Cluster membership (-1 if none)
	CommitCount  int       `json:"commit_count"` // Number of associated commits
	FileCount    int       `json:"file_count"`   // Number of touched files
	Connectivity float64   `json:"connectivity"` // Ratio of edges to potential edges in cluster
}

// BeadCluster represents a group of tightly connected beads.
type BeadCluster struct {
	ClusterID            int      `json:"cluster_id"`
	BeadIDs              []string `json:"bead_ids"`
	Label                string   `json:"label"`                 // Auto-generated or user-provided label
	InternalEdges        int      `json:"internal_edges"`        // Edges within cluster
	ExternalEdges        int      `json:"external_edges"`        // Edges to other clusters
	InternalConnectivity float64  `json:"internal_connectivity"` // internal_edges / max_possible
	CentralBead          string   `json:"central_bead"`          // Bead with highest degree in cluster
	SharedFiles          []string `json:"shared_files"`          // Common files across cluster beads
	TotalCommits         int      `json:"total_commits"`         // Sum of commits across cluster
}

// ImpactNetwork represents the full network graph of bead relationships.
type ImpactNetwork struct {
	GeneratedAt time.Time               `json:"generated_at"`
	DataHash    string                  `json:"data_hash"`
	Nodes       map[string]*NetworkNode `json:"nodes"`
	Edges       []NetworkEdge           `json:"edges"`
	Clusters    []BeadCluster           `json:"clusters"`
	Stats       NetworkStats            `json:"stats"`
}

// NetworkStats provides aggregate statistics about the network.
type NetworkStats struct {
	TotalNodes     int     `json:"total_nodes"`
	TotalEdges     int     `json:"total_edges"` // Typed edge records in ImpactNetwork.Edges
	ClusterCount   int     `json:"cluster_count"`
	AvgDegree      float64 `json:"avg_degree"`
	MaxDegree      int     `json:"max_degree"`
	Density        float64 `json:"density"`         // unique bead pairs / max possible pairs
	IsolatedNodes  int     `json:"isolated_nodes"`  // Nodes with no connections
	LargestCluster int     `json:"largest_cluster"` // Size of largest cluster
}

// NetworkBuilder constructs an impact network from correlation data.
type NetworkBuilder struct {
	report     *HistoryReport
	fileIndex  *FileBeadIndex
	beadFiles  map[string]map[string]bool // beadID -> set of file paths
	issues     []model.Issue
	issueIndex map[string]model.Issue
}

// NewNetworkBuilder creates a new network builder from a history report.
func NewNetworkBuilder(report *HistoryReport) *NetworkBuilder {
	return NewNetworkBuilderWithIssues(report, nil)
}

// NewNetworkBuilderWithIssues creates a new network builder from a history report and issues.
func NewNetworkBuilderWithIssues(report *HistoryReport, issues []model.Issue) *NetworkBuilder {
	nb := &NetworkBuilder{
		report:    report,
		beadFiles: make(map[string]map[string]bool),
		issues:    issues,
	}

	if report != nil {
		nb.fileIndex = BuildFileIndex(report)
		nb.buildBeadMaps()
	}
	if len(issues) > 0 {
		nb.issueIndex = make(map[string]model.Issue, len(issues))
		for _, issue := range issues {
			if issue.ID == "" {
				continue
			}
			nb.issueIndex[issue.ID] = issue
		}
	}

	return nb
}

// buildBeadMaps creates reverse indexes from beads to their files/commits.
func (nb *NetworkBuilder) buildBeadMaps() {
	if nb.report == nil {
		return
	}

	for beadID, history := range nb.report.Histories {
		nb.beadFiles[beadID] = make(map[string]bool)

		for _, commit := range history.Commits {
			for _, file := range commit.Files {
				normalizedPath := normalizePath(file.Path)
				if normalizedPath == "" {
					continue
				}
				nb.beadFiles[beadID][normalizedPath] = true
			}
		}
	}
}

// Build constructs the full impact network.
func (nb *NetworkBuilder) Build() *ImpactNetwork {
	return nb.BuildAt(time.Now())
}

// BuildAt constructs the full impact network at a caller-owned reference
// instant. The instant is metadata only; graph contents come from the report.
// The zero instant is valid and is preserved.
func (nb *NetworkBuilder) BuildAt(now time.Time) *ImpactNetwork {
	network := &ImpactNetwork{
		GeneratedAt: now,
		Nodes:       make(map[string]*NetworkNode),
		Edges:       []NetworkEdge{},
		Clusters:    []BeadCluster{},
	}

	if nb.report == nil {
		return network
	}

	network.DataHash = nb.report.DataHash

	// Build nodes
	for beadID, history := range nb.report.Histories {
		// Get priority from somewhere (default to 2 if not available)
		priority := 2 // Default medium priority
		if issue, ok := nb.issueIndex[beadID]; ok {
			priority = issue.Priority
		}

		node := &NetworkNode{
			BeadID:      beadID,
			Title:       history.Title,
			Status:      history.Status,
			Priority:    priority,
			Degree:      0,
			ClusterID:   -1,
			CommitCount: len(history.Commits),
			FileCount:   len(nb.beadFiles[beadID]),
		}

		node.LastActivity = latestHistoryActivity(history)

		network.Nodes[beadID] = node
	}

	// Build edges from shared commits
	nb.addSharedCommitEdges(network)

	// Build edges from shared files
	nb.addSharedFileEdges(network)

	// Build edges from explicit blocking dependencies
	nb.addDependencyEdges(network)
	sort.Slice(network.Edges, func(i, j int) bool {
		if network.Edges[i].FromBead != network.Edges[j].FromBead {
			return network.Edges[i].FromBead < network.Edges[j].FromBead
		}
		if network.Edges[i].ToBead != network.Edges[j].ToBead {
			return network.Edges[i].ToBead < network.Edges[j].ToBead
		}
		return network.Edges[i].EdgeType < network.Edges[j].EdgeType
	})
	nb.recomputeNodeDegrees(network)

	// Detect clusters using connected components with edge weight threshold
	nb.detectClusters(network)

	// Calculate statistics
	nb.calculateStats(network)

	return network
}

// recomputeNodeDegrees derives degrees from unique unordered bead pairs. Edge
// types remain distinct in network.Edges, but cannot multiply a node's degree.
func (nb *NetworkBuilder) recomputeNodeDegrees(network *ImpactNetwork) {
	if network == nil {
		return
	}
	for _, node := range network.Nodes {
		if node != nil {
			node.Degree = 0
		}
	}
	for pair := range networkPairs(network, 0) {
		network.Nodes[pair.first].Degree++
		network.Nodes[pair.second].Degree++
	}
}

func latestHistoryActivity(history BeadHistory) time.Time {
	var latest time.Time
	consider := func(timestamp time.Time) {
		if timestamp.After(latest) {
			latest = timestamp
		}
	}
	for _, milestone := range []*BeadEvent{
		history.Milestones.Created,
		history.Milestones.Claimed,
		history.Milestones.Closed,
		history.Milestones.Reopened,
	} {
		if milestone != nil {
			consider(milestone.Timestamp)
		}
	}
	for _, event := range history.Events {
		consider(event.Timestamp)
	}
	for _, commit := range history.Commits {
		consider(commit.Timestamp)
	}
	return latest
}

// addSharedCommitEdges adds edges for beads that share commits.
func (nb *NetworkBuilder) addSharedCommitEdges(network *ImpactNetwork) {
	// Build commit -> beads index
	commitToBeads := make(map[string][]string)
	for sha, beadIDs := range nb.report.CommitIndex {
		commitToBeads[sha] = beadIDs
	}

	// Track edges we've already added (to avoid duplicates)
	edgeSet := make(map[networkEdgeKey]struct{})
	edgeWeights := make(map[networkEdgeKey]int)
	edgeDetails := make(map[networkEdgeKey][]string)

	for sha, beadIDs := range commitToBeads {
		if len(beadIDs) < 2 || sha == "" || sha != strings.TrimSpace(sha) {
			continue
		}

		// A malformed or hand-built CommitIndex may repeat a bead ID. Normalize
		// each membership list so a commit contributes once per distinct pair.
		uniqueBeads := uniqueSortedStrings(beadIDs)
		for i := 0; i < len(uniqueBeads); i++ {
			for j := i + 1; j < len(uniqueBeads); j++ {
				fromNode, fromOK := network.Nodes[uniqueBeads[i]]
				toNode, toOK := network.Nodes[uniqueBeads[j]]
				if !fromOK || !toOK || fromNode == nil || toNode == nil {
					continue
				}
				key := makeNetworkEdgeKey(uniqueBeads[i], uniqueBeads[j], EdgeSharedCommit)

				edgeWeights[key]++
				edgeSet[key] = struct{}{}
				// Robot-visible identities must remain lossless. Distinct commits can
				// share a seven-character prefix in large repositories.
				edgeDetails[key] = append(edgeDetails[key], sha)
			}
		}
	}

	// Convert to edge list
	for _, key := range sortedNetworkEdgeKeys(edgeSet) {
		details := append([]string(nil), edgeDetails[key]...)
		sort.Strings(details)
		details = limitStrings(details, 5)
		network.Edges = append(network.Edges, NetworkEdge{
			FromBead: key.fromBead,
			ToBead:   key.toBead,
			EdgeType: key.edgeType,
			Weight:   edgeWeights[key],
			Details:  details,
		})
	}
}

// addSharedFileEdges adds edges for beads that touch the same files.
func (nb *NetworkBuilder) addSharedFileEdges(network *ImpactNetwork) {
	// Track edges we've already added (to avoid duplicates and combine with commit edges)
	edgeSet := make(map[networkEdgeKey]struct{})
	edgeWeights := make(map[networkEdgeKey]int)
	edgeDetails := make(map[networkEdgeKey][]string)

	// For each file, find all beads that touched it
	for filePath, refs := range nb.fileIndex.FileToBeads {
		if len(refs) < 2 {
			continue
		}

		// Create edges between all pairs of beads touching this file
		for i := 0; i < len(refs); i++ {
			for j := i + 1; j < len(refs); j++ {
				if refs[i].BeadID == refs[j].BeadID {
					continue
				}
				key := makeNetworkEdgeKey(refs[i].BeadID, refs[j].BeadID, EdgeSharedFile)

				edgeWeights[key]++
				edgeSet[key] = struct{}{}
				edgeDetails[key] = append(edgeDetails[key], filePath)
			}
		}
	}

	// Convert to edge list
	for _, key := range sortedNetworkEdgeKeys(edgeSet) {
		details := append([]string(nil), edgeDetails[key]...)
		sort.Strings(details)
		details = limitStrings(details, 5)
		network.Edges = append(network.Edges, NetworkEdge{
			FromBead: key.fromBead,
			ToBead:   key.toBead,
			EdgeType: key.edgeType,
			Weight:   edgeWeights[key],
			Details:  details,
		})
	}
}

// addDependencyEdges adds edges for explicit blocking dependencies.
func (nb *NetworkBuilder) addDependencyEdges(network *ImpactNetwork) {
	if nb == nil || len(nb.issues) == 0 || network == nil {
		return
	}

	edgeSet := make(map[networkEdgeKey]struct{})
	edgeWeights := make(map[networkEdgeKey]int)
	edgeDetails := make(map[networkEdgeKey][]string)

	for _, issue := range nb.issues {
		fromID := issue.ID
		if fromID == "" {
			continue
		}
		if _, ok := network.Nodes[fromID]; !ok {
			continue
		}
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			toID := dep.DependsOnID
			if toID == "" || toID == fromID {
				continue
			}
			if _, ok := network.Nodes[toID]; !ok {
				continue
			}

			key := makeNetworkEdgeKey(fromID, toID, EdgeDependency)

			edgeWeights[key]++
			edgeSet[key] = struct{}{}
			edgeDetails[key] = append(edgeDetails[key], fromID+" -> "+toID)
		}
	}

	for _, key := range sortedNetworkEdgeKeys(edgeSet) {
		details := append([]string(nil), edgeDetails[key]...)
		sort.Strings(details)
		details = limitStrings(details, 5)
		network.Edges = append(network.Edges, NetworkEdge{
			FromBead: key.fromBead,
			ToBead:   key.toBead,
			EdgeType: key.edgeType,
			Weight:   edgeWeights[key],
			Details:  details,
		})
	}
}

func sortedNetworkEdgeKeys(edgeSet map[networkEdgeKey]struct{}) []networkEdgeKey {
	keys := make([]networkEdgeKey, 0, len(edgeSet))
	for key := range edgeSet {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].fromBead != keys[j].fromBead {
			return keys[i].fromBead < keys[j].fromBead
		}
		if keys[i].toBead != keys[j].toBead {
			return keys[i].toBead < keys[j].toBead
		}
		return keys[i].edgeType < keys[j].edgeType
	})
	return keys
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// detectClusters uses connected components to find clusters of related beads.
// Only considers edges with weight >= minWeight.
func (nb *NetworkBuilder) detectClusters(network *ImpactNetwork) {
	const minWeight = 2 // Minimum edge weight to be considered for clustering

	if network == nil {
		return
	}
	network.Clusters = network.Clusters[:0]
	for _, node := range network.Nodes {
		if node != nil {
			node.ClusterID = -1
			node.Connectivity = 0
		}
	}

	// Build a simple adjacency list from strong typed edges. Parallel typed
	// edges may establish strength, but never duplicate a neighbor.
	adj := networkAdjacency(networkPairs(network, minWeight))

	// Find connected components using DFS
	visited := make(map[string]bool)
	clusterID := 0

	nodeIDs := make([]string, 0, len(network.Nodes))
	for beadID := range network.Nodes {
		nodeIDs = append(nodeIDs, beadID)
	}
	sort.Strings(nodeIDs)
	for _, beadID := range nodeIDs {
		if visited[beadID] {
			continue
		}
		if len(adj[beadID]) == 0 {
			// Isolated node - no strong connections
			continue
		}

		// DFS to find all nodes in this component
		component := []string{}
		stack := []string{beadID}

		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if visited[current] {
				continue
			}
			visited[current] = true
			component = append(component, current)

			for _, neighbor := range adj[current] {
				if !visited[neighbor] {
					stack = append(stack, neighbor)
				}
			}
		}

		// Only create cluster if it has multiple beads
		if len(component) >= 2 {
			sort.Strings(component)
			cluster := nb.buildCluster(clusterID, component, network)
			network.Clusters = append(network.Clusters, cluster)

			// Update node cluster IDs
			for _, bid := range component {
				if node, ok := network.Nodes[bid]; ok {
					node.ClusterID = clusterID
				}
			}

			clusterID++
		}
	}

	// Sort clusters by size (largest first)
	sort.Slice(network.Clusters, func(i, j int) bool {
		if len(network.Clusters[i].BeadIDs) != len(network.Clusters[j].BeadIDs) {
			return len(network.Clusters[i].BeadIDs) > len(network.Clusters[j].BeadIDs)
		}
		return network.Clusters[i].BeadIDs[0] < network.Clusters[j].BeadIDs[0]
	})

	// Re-number cluster IDs after sorting
	for i := range network.Clusters {
		oldID := network.Clusters[i].ClusterID
		network.Clusters[i].ClusterID = i
		for _, bid := range network.Clusters[i].BeadIDs {
			if node, ok := network.Nodes[bid]; ok && node.ClusterID == oldID {
				node.ClusterID = i
			}
		}
	}
}

// buildCluster creates a cluster from a set of bead IDs.
func (nb *NetworkBuilder) buildCluster(id int, beadIDs []string, network *ImpactNetwork) BeadCluster {
	cluster := BeadCluster{
		ClusterID:   id,
		BeadIDs:     beadIDs,
		SharedFiles: []string{},
	}

	// Create set of cluster beads for quick lookup
	clusterSet := make(map[string]bool)
	for _, bid := range beadIDs {
		clusterSet[bid] = true
	}

	// Count unique bead pairs, not typed edge records. A shared-commit edge and
	// a shared-file edge between the same beads are one simple relationship for
	// connectivity metrics.
	internalDegrees := make(map[string]int, len(beadIDs))
	for pair := range networkPairs(network, 0) {
		fromIn := clusterSet[pair.first]
		toIn := clusterSet[pair.second]

		if fromIn && toIn {
			cluster.InternalEdges++
			internalDegrees[pair.first]++
			internalDegrees[pair.second]++
		} else if fromIn || toIn {
			cluster.ExternalEdges++
		}
	}

	// Calculate internal connectivity
	n := len(beadIDs)
	maxEdges := n * (n - 1) / 2
	if maxEdges > 0 {
		cluster.InternalConnectivity = float64(cluster.InternalEdges) / float64(maxEdges)
	}

	// Find central bead (highest degree within cluster)
	maxDegree := -1
	for _, bid := range beadIDs {
		if node, ok := network.Nodes[bid]; ok {
			internalDegree := internalDegrees[bid]
			if n > 1 {
				node.Connectivity = float64(internalDegree) / float64(n-1)
			}
			if internalDegree > maxDegree ||
				(internalDegree == maxDegree && (cluster.CentralBead == "" || bid < cluster.CentralBead)) {
				maxDegree = internalDegree
				cluster.CentralBead = bid
			}
			cluster.TotalCommits += node.CommitCount
		}
	}

	// Find shared files (files touched by multiple beads in cluster)
	fileCount := make(map[string]int)
	for _, bid := range beadIDs {
		for file := range nb.beadFiles[bid] {
			fileCount[file]++
		}
	}

	for file, count := range fileCount {
		if count >= 2 { // File touched by at least 2 beads in cluster
			cluster.SharedFiles = append(cluster.SharedFiles, file)
		}
	}
	sort.Strings(cluster.SharedFiles)

	// Generate label from common path prefix or central bead title
	cluster.Label = nb.generateClusterLabel(beadIDs, cluster.SharedFiles)

	return cluster
}

// generateClusterLabel creates a descriptive label for a cluster.
func (nb *NetworkBuilder) generateClusterLabel(beadIDs []string, sharedFiles []string) string {
	// Try to find common path prefix from shared files
	if len(sharedFiles) > 0 {
		prefix := commonPathPrefix(sharedFiles)
		if prefix != "" && len(prefix) > 2 {
			// Clean up trailing slashes
			if prefix[len(prefix)-1] == '/' {
				prefix = prefix[:len(prefix)-1]
			}
			return prefix
		}
	}

	// Fall back to first bead's title (truncated)
	if len(beadIDs) > 0 && nb != nil && nb.report != nil {
		if history, ok := nb.report.Histories[beadIDs[0]]; ok {
			title := history.Title
			if len([]rune(title)) > 30 {
				title = string([]rune(title)[:27]) + "..."
			}
			return title
		}
	}

	return "cluster"
}

// commonPathPrefix finds the common directory prefix of a set of file paths.
func commonPathPrefix(files []string) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		// Return directory portion
		for i := len(files[0]) - 1; i >= 0; i-- {
			if files[0][i] == '/' {
				return files[0][:i+1]
			}
		}
		return ""
	}

	// Start with the directory portion of the first file
	prefix := ""
	for i := len(files[0]) - 1; i >= 0; i-- {
		if files[0][i] == '/' {
			prefix = files[0][:i+1]
			break
		}
	}

	if prefix == "" {
		return ""
	}

	for _, file := range files[1:] {
		for len(prefix) > 0 && !hasPrefix(file, prefix) {
			// Shorten prefix to previous directory boundary (excluding trailing /)
			// First, strip the trailing slash if present
			searchPrefix := prefix
			if len(searchPrefix) > 0 && searchPrefix[len(searchPrefix)-1] == '/' {
				searchPrefix = searchPrefix[:len(searchPrefix)-1]
			}
			// Find the previous slash
			found := false
			for i := len(searchPrefix) - 1; i >= 0; i-- {
				if searchPrefix[i] == '/' {
					prefix = searchPrefix[:i+1]
					found = true
					break
				}
			}
			if !found {
				prefix = ""
			}
		}
	}

	return prefix
}

// hasPrefix checks if str has the given prefix.
func hasPrefix(str, prefix string) bool {
	if len(prefix) > len(str) {
		return false
	}
	return str[:len(prefix)] == prefix
}

// calculateStats computes aggregate statistics for the network.
func (nb *NetworkBuilder) calculateStats(network *ImpactNetwork) {
	if network == nil {
		return
	}
	network.Stats = NetworkStats{}
	stats := &network.Stats
	stats.TotalEdges = len(network.Edges)
	stats.ClusterCount = len(network.Clusters)

	// Calculate degree statistics
	totalDegree := 0
	for _, node := range network.Nodes {
		if node == nil {
			continue
		}
		stats.TotalNodes++
		totalDegree += node.Degree
		if node.Degree > stats.MaxDegree {
			stats.MaxDegree = node.Degree
		}
		if node.Degree == 0 {
			stats.IsolatedNodes++
		}
	}

	if stats.TotalNodes > 0 {
		stats.AvgDegree = float64(totalDegree) / float64(stats.TotalNodes)
	}

	// Calculate simple-graph density (unique bead pairs / max possible pairs).
	if stats.TotalNodes > 1 {
		maxEdges := stats.TotalNodes * (stats.TotalNodes - 1) / 2
		stats.Density = float64(len(networkPairs(network, 0))) / float64(maxEdges)
	}

	// Find largest cluster
	for _, cluster := range network.Clusters {
		if len(cluster.BeadIDs) > stats.LargestCluster {
			stats.LargestCluster = len(cluster.BeadIDs)
		}
	}
}

// GetSubNetwork returns a subnetwork centered on a specific bead with given depth.
func (network *ImpactNetwork) GetSubNetwork(beadID string, depth int) *ImpactNetwork {
	if network == nil {
		return &ImpactNetwork{
			Nodes:    make(map[string]*NetworkNode),
			Edges:    []NetworkEdge{},
			Clusters: []BeadCluster{},
		}
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 3 {
		depth = 3 // Cap depth to avoid huge subnetworks
	}

	// BFS to find all beads within depth
	visited := make(map[string]bool)
	queue := []struct {
		bead  string
		level int
	}{{beadID, 0}}

	beadSet := make(map[string]bool)

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if visited[current.bead] {
			continue
		}
		visited[current.bead] = true
		if node, ok := network.Nodes[current.bead]; !ok || node == nil {
			continue
		}
		beadSet[current.bead] = true

		if current.level >= depth {
			continue
		}

		// Find neighbors
		for _, edge := range network.Edges {
			if edge.FromBead == current.bead && !visited[edge.ToBead] && network.Nodes[edge.ToBead] != nil {
				queue = append(queue, struct {
					bead  string
					level int
				}{edge.ToBead, current.level + 1})
			}
			if edge.ToBead == current.bead && !visited[edge.FromBead] && network.Nodes[edge.FromBead] != nil {
				queue = append(queue, struct {
					bead  string
					level int
				}{edge.FromBead, current.level + 1})
			}
		}
	}

	// Build subnetwork
	subNetwork := &ImpactNetwork{
		GeneratedAt: network.GeneratedAt,
		DataHash:    network.DataHash,
		Nodes:       make(map[string]*NetworkNode),
		Edges:       []NetworkEdge{},
		Clusters:    []BeadCluster{},
	}

	// Copy relevant nodes
	for bid := range beadSet {
		if node, ok := network.Nodes[bid]; ok {
			nodeCopy := *node
			nodeCopy.Degree = 0
			nodeCopy.ClusterID = -1
			nodeCopy.Connectivity = 0
			subNetwork.Nodes[bid] = &nodeCopy
		}
	}

	// Copy relevant edges
	for _, edge := range network.Edges {
		if subNetwork.Nodes[edge.FromBead] != nil && subNetwork.Nodes[edge.ToBead] != nil {
			edge.Details = append([]string(nil), edge.Details...)
			subNetwork.Edges = append(subNetwork.Edges, edge)
		}
	}

	// Recalculate all graph-derived state for the induced subgraph. Copying the
	// full-network degree and cluster membership would describe edges/nodes that
	// are absent from this result.
	subHistories := make(map[string]BeadHistory, len(subNetwork.Nodes))
	subBeadFiles := make(map[string]map[string]bool, len(subNetwork.Nodes))
	for bid, node := range subNetwork.Nodes {
		subHistories[bid] = BeadHistory{BeadID: bid, Title: node.Title, Status: node.Status}
		subBeadFiles[bid] = make(map[string]bool)
	}
	// ImpactNetwork carries only the bounded shared-file samples stored on its
	// edges, not the source report's complete per-bead file sets. Preserve every
	// available sample in recomputed cluster metadata without pretending the
	// resulting SharedFiles list is exhaustive.
	for _, edge := range subNetwork.Edges {
		if edge.EdgeType != EdgeSharedFile {
			continue
		}
		for _, file := range edge.Details {
			if file == "" {
				continue
			}
			subBeadFiles[edge.FromBead][file] = true
			subBeadFiles[edge.ToBead][file] = true
		}
	}
	nb := &NetworkBuilder{
		report:    &HistoryReport{Histories: subHistories},
		beadFiles: subBeadFiles,
	}
	nb.recomputeNodeDegrees(subNetwork)
	nb.detectClusters(subNetwork)
	nb.calculateStats(subNetwork)

	return subNetwork
}

// ImpactNetworkResult is the robot command output structure.
type ImpactNetworkResult struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	DataHash     string         `json:"data_hash"`
	BeadID       string         `json:"bead_id,omitempty"` // Set if queried for specific bead
	Depth        int            `json:"depth,omitempty"`   // Set if using subnetwork
	Network      *ImpactNetwork `json:"network,omitempty"` // Full or sub network
	Stats        NetworkStats   `json:"stats"`
	TopClusters  []BeadCluster  `json:"top_clusters,omitempty"`  // Top 5 clusters
	TopConnected []NetworkNode  `json:"top_connected,omitempty"` // Top 10 most connected beads
}

// ToResult converts the network to a robot command result.
func (network *ImpactNetwork) ToResult(beadID string, depth int) *ImpactNetworkResult {
	if network == nil {
		network = &ImpactNetwork{
			Nodes:    make(map[string]*NetworkNode),
			Edges:    []NetworkEdge{},
			Clusters: []BeadCluster{},
		}
	}
	result := &ImpactNetworkResult{
		GeneratedAt: network.GeneratedAt,
		DataHash:    network.DataHash,
		BeadID:      beadID,
		Depth:       depth,
		Stats:       network.Stats,
	}

	// If specific bead requested, return subnetwork
	if beadID != "" {
		result.Network = network.GetSubNetwork(beadID, depth)
		result.Stats = result.Network.Stats
	} else {
		result.Network = network
	}

	// Top 5 clusters (always from full network for context)
	clusterLimit := 5
	if len(network.Clusters) < clusterLimit {
		clusterLimit = len(network.Clusters)
	}
	result.TopClusters = network.Clusters[:clusterLimit]

	// Top 10 most connected beads (from subnetwork if beadID specified, else full network)
	sourceNodes := network.Nodes
	if beadID != "" && result.Network != nil {
		sourceNodes = result.Network.Nodes
	}
	nodes := make([]NetworkNode, 0, len(sourceNodes))
	for _, node := range sourceNodes {
		if node == nil {
			continue
		}
		nodes = append(nodes, *node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Degree != nodes[j].Degree {
			return nodes[i].Degree > nodes[j].Degree
		}
		return nodes[i].BeadID < nodes[j].BeadID
	})

	nodeLimit := 10
	if len(nodes) < nodeLimit {
		nodeLimit = len(nodes)
	}
	result.TopConnected = nodes[:nodeLimit]

	return result
}
