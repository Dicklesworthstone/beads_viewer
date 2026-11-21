//! Core directed graph structure with adjacency lists.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use wasm_bindgen::prelude::*;

/// Directed graph optimized for graph algorithms.
/// Uses adjacency lists for O(1) neighbor access.
#[wasm_bindgen]
pub struct DiGraph {
    /// Node ID strings (issue IDs like "bv-123")
    nodes: Vec<String>,

    /// Reverse lookup: ID string -> index
    node_index: HashMap<String, usize>,

    /// Forward adjacency: adj[u] = vec of nodes that u points to
    /// (u depends on these nodes)
    adj: Vec<Vec<usize>>,

    /// Reverse adjacency: rev_adj[v] = vec of nodes pointing to v
    /// (these nodes depend on v)
    rev_adj: Vec<Vec<usize>>,

    /// Edge count (for density calculation)
    edge_count: usize,
}

/// Serializable graph snapshot for import/export.
#[derive(Serialize, Deserialize)]
pub struct GraphSnapshot {
    pub nodes: Vec<String>,
    pub edges: Vec<(usize, usize)>,
}

#[wasm_bindgen]
impl DiGraph {
    /// Create an empty graph.
    #[wasm_bindgen(constructor)]
    pub fn new() -> DiGraph {
        DiGraph {
            nodes: Vec::new(),
            node_index: HashMap::new(),
            adj: Vec::new(),
            rev_adj: Vec::new(),
            edge_count: 0,
        }
    }

    /// Create a graph with pre-allocated capacity.
    #[wasm_bindgen(js_name = withCapacity)]
    pub fn with_capacity(node_capacity: usize, edge_capacity: usize) -> DiGraph {
        let _ = edge_capacity; // Used for documentation, not pre-allocation
        DiGraph {
            nodes: Vec::with_capacity(node_capacity),
            node_index: HashMap::with_capacity(node_capacity),
            adj: Vec::with_capacity(node_capacity),
            rev_adj: Vec::with_capacity(node_capacity),
            edge_count: 0,
        }
    }

    /// Add a node, returns its index. Idempotent - returns existing index if already present.
    #[wasm_bindgen(js_name = addNode)]
    pub fn add_node(&mut self, id: &str) -> usize {
        if let Some(&idx) = self.node_index.get(id) {
            return idx;
        }
        let idx = self.nodes.len();
        self.nodes.push(id.to_string());
        self.node_index.insert(id.to_string(), idx);
        self.adj.push(Vec::new());
        self.rev_adj.push(Vec::new());
        idx
    }

    /// Add a directed edge from -> to. Idempotent.
    /// In a dependency graph, from is the dependent and to its prerequisite.
    #[wasm_bindgen(js_name = addEdge)]
    pub fn add_edge(&mut self, from: usize, to: usize) {
        // Check bounds
        if from >= self.nodes.len() || to >= self.nodes.len() {
            return; // Silently ignore invalid edges
        }

        // Check if edge already exists (linear scan is fine for typical degree)
        if self.adj[from].contains(&to) {
            return;
        }

        self.adj[from].push(to);
        self.rev_adj[to].push(from);
        self.edge_count += 1;
    }

    /// Number of nodes.
    #[wasm_bindgen(js_name = nodeCount)]
    pub fn node_count(&self) -> usize {
        self.nodes.len()
    }

    /// Number of edges.
    #[wasm_bindgen(js_name = edgeCount)]
    pub fn edge_count(&self) -> usize {
        self.edge_count
    }

    /// Graph density: edges / (nodes * (nodes - 1)).
    pub fn density(&self) -> f64 {
        let n = self.node_count() as f64;
        let e = self.edge_count() as f64;
        if n <= 1.0 {
            0.0
        } else {
            e / (n * (n - 1.0))
        }
    }

    /// Get node ID by index.
    #[wasm_bindgen(js_name = nodeId)]
    pub fn node_id(&self, idx: usize) -> Option<String> {
        self.nodes.get(idx).cloned()
    }

    /// Get node index by ID.
    #[wasm_bindgen(js_name = nodeIdx)]
    pub fn node_idx(&self, id: &str) -> Option<usize> {
        self.node_index.get(id).copied()
    }

    /// Get all node IDs as JSON array.
    #[wasm_bindgen(js_name = nodeIds)]
    pub fn node_ids(&self) -> JsValue {
        serde_wasm_bindgen::to_value(&self.nodes).unwrap_or(JsValue::NULL)
    }

    /// Out-degree of a node (number of dependencies).
    #[wasm_bindgen(js_name = outDegree)]
    pub fn out_degree(&self, node: usize) -> usize {
        self.adj.get(node).map_or(0, |v| v.len())
    }

    /// In-degree of a node (number of dependents).
    #[wasm_bindgen(js_name = inDegree)]
    pub fn in_degree(&self, node: usize) -> usize {
        self.rev_adj.get(node).map_or(0, |v| v.len())
    }

    /// All out-degrees as a vector (JSON array).
    #[wasm_bindgen(js_name = outDegrees)]
    pub fn out_degrees(&self) -> JsValue {
        let degrees: Vec<usize> = self.adj.iter().map(|v| v.len()).collect();
        serde_wasm_bindgen::to_value(&degrees).unwrap_or(JsValue::NULL)
    }

    /// All in-degrees as a vector (JSON array).
    #[wasm_bindgen(js_name = inDegrees)]
    pub fn in_degrees(&self) -> JsValue {
        let degrees: Vec<usize> = self.rev_adj.iter().map(|v| v.len()).collect();
        serde_wasm_bindgen::to_value(&degrees).unwrap_or(JsValue::NULL)
    }

    /// Export graph as JSON snapshot.
    #[wasm_bindgen(js_name = toJson)]
    pub fn to_json(&self) -> String {
        let snapshot = GraphSnapshot {
            nodes: self.nodes.clone(),
            edges: self.edges_vec(),
        };
        serde_json::to_string(&snapshot).unwrap_or_default()
    }

    /// Import graph from JSON snapshot.
    #[wasm_bindgen(js_name = fromJson)]
    pub fn from_json(json: &str) -> Result<DiGraph, JsError> {
        let snapshot: GraphSnapshot =
            serde_json::from_str(json).map_err(|e| JsError::new(&e.to_string()))?;

        let mut graph = DiGraph::with_capacity(snapshot.nodes.len(), snapshot.edges.len());
        for id in snapshot.nodes {
            graph.add_node(&id);
        }
        for (from, to) in snapshot.edges {
            graph.add_edge(from, to);
        }
        Ok(graph)
    }

    /// Get successors of a node as JSON array of indices.
    pub fn successors(&self, node: usize) -> JsValue {
        let succs = self.adj.get(node).map_or(&[][..], |v| v.as_slice());
        serde_wasm_bindgen::to_value(succs).unwrap_or(JsValue::NULL)
    }

    /// Get predecessors of a node as JSON array of indices.
    pub fn predecessors(&self, node: usize) -> JsValue {
        let preds = self.rev_adj.get(node).map_or(&[][..], |v| v.as_slice());
        serde_wasm_bindgen::to_value(preds).unwrap_or(JsValue::NULL)
    }

    /// Topological sort using Kahn's algorithm.
    /// Returns node indices in topological order, or null if graph has cycles.
    #[wasm_bindgen(js_name = topologicalSort)]
    pub fn topological_sort(&self) -> JsValue {
        use crate::algorithms::topo;
        match topo::topological_sort(self) {
            Some(order) => serde_wasm_bindgen::to_value(&order).unwrap_or(JsValue::NULL),
            None => JsValue::NULL,
        }
    }

    /// Check if graph is a DAG (directed acyclic graph).
    #[wasm_bindgen(js_name = isDag)]
    pub fn is_dag(&self) -> bool {
        use crate::algorithms::topo;
        topo::is_dag(self)
    }

    /// Compute critical path heights (depth in DAG).
    /// Returns heights as JSON array, or zeros for cyclic graphs.
    #[wasm_bindgen(js_name = criticalPathHeights)]
    pub fn critical_path_heights(&self) -> JsValue {
        use crate::algorithms::critical_path;
        let heights = critical_path::critical_path_heights(self);
        serde_wasm_bindgen::to_value(&heights).unwrap_or(JsValue::NULL)
    }

    /// Get nodes on the critical path (those with maximum height).
    #[wasm_bindgen(js_name = criticalPathNodes)]
    pub fn critical_path_nodes(&self) -> JsValue {
        use crate::algorithms::critical_path;
        let nodes = critical_path::critical_path_nodes(self);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get the maximum height (critical path length).
    #[wasm_bindgen(js_name = criticalPathLength)]
    pub fn critical_path_length(&self) -> f64 {
        use crate::algorithms::critical_path::critical_path_length;
        critical_path_length(self)
    }

    /// Compute PageRank scores for all nodes.
    /// Returns array of scores in node index order.
    #[wasm_bindgen(js_name = pagerank)]
    pub fn pagerank(&self, damping: f64, max_iterations: u32) -> JsValue {
        use crate::algorithms::pagerank::{pagerank, PageRankConfig};
        let config = PageRankConfig {
            damping,
            max_iterations,
            tolerance: 1e-6,
        };
        let scores = pagerank(self, &config);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }

    /// Compute PageRank with default parameters (damping=0.85, max_iterations=100).
    #[wasm_bindgen(js_name = pagerankDefault)]
    pub fn pagerank_default(&self) -> JsValue {
        use crate::algorithms::pagerank::pagerank_default;
        let scores = pagerank_default(self);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }

    /// Compute exact betweenness centrality using Brandes' algorithm.
    /// Returns array of scores in node index order.
    /// Complexity: O(V*E) - use betweenness_approx for large graphs.
    #[wasm_bindgen(js_name = betweenness)]
    pub fn betweenness(&self) -> JsValue {
        use crate::algorithms::betweenness::betweenness;
        let scores = betweenness(self);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }

    /// Compute approximate betweenness centrality using deterministic pivot sampling.
    /// Returns array of scores in node index order.
    /// Error: O(1/sqrt(k)) - with k=100, ~10% error in ranking.
    #[wasm_bindgen(js_name = betweennessApprox)]
    pub fn betweenness_approx(&self, sample_size: usize) -> JsValue {
        let scores = self.betweenness_approx_seeded(sample_size);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }

    /// Find articulation points (cut vertices) in the graph.
    /// These are nodes whose removal disconnects the graph.
    /// Returns array of node indices.
    #[wasm_bindgen(js_name = articulationPoints)]
    pub fn articulation_points(&self) -> JsValue {
        use crate::algorithms::articulation::articulation_points;
        let ap = articulation_points(self);
        serde_wasm_bindgen::to_value(&ap).unwrap_or(JsValue::NULL)
    }

    /// Find bridges (cut edges) in the graph.
    /// These are edges whose removal disconnects the graph.
    /// Returns array of [from, to] pairs.
    #[wasm_bindgen(js_name = bridges)]
    pub fn bridges(&self) -> JsValue {
        use crate::algorithms::articulation::bridges;
        let br = bridges(self);
        serde_wasm_bindgen::to_value(&br).unwrap_or(JsValue::NULL)
    }

    /// Find strongly connected components using Tarjan's algorithm.
    /// Returns JSON: { components: number[][], has_cycles: bool, cycle_count: number }
    #[wasm_bindgen(js_name = tarjanScc)]
    pub fn tarjan_scc(&self) -> JsValue {
        use crate::algorithms::cycles::tarjan_scc;
        let result = tarjan_scc(self);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Check if graph has any cycles.
    #[wasm_bindgen(js_name = hasCycles)]
    pub fn has_cycles(&self) -> bool {
        use crate::algorithms::cycles::has_cycles;
        has_cycles(self)
    }

    /// Enumerate all elementary cycles using Johnson's algorithm.
    /// Returns JSON: { cycles: number[][], truncated: bool, count: number }
    #[wasm_bindgen(js_name = enumerateCycles)]
    pub fn enumerate_cycles(&self, max_cycles: usize) -> JsValue {
        use crate::algorithms::cycles::enumerate_cycles_with_info;
        let result = enumerate_cycles_with_info(self, max_cycles);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Suggest edges to remove to break cycles.
    /// Returns JSON: { suggestions: [{from, to, cycles_broken, collateral, from_id, to_id}], total_cycles, truncated }
    /// Suggestions are sorted by cycles_broken desc, then collateral asc.
    #[wasm_bindgen(js_name = cycleBreakSuggestions)]
    pub fn cycle_break_suggestions(&self, limit: usize, max_cycles_to_enumerate: usize) -> JsValue {
        use crate::algorithms::cycles::cycle_break_suggestions;
        let result = cycle_break_suggestions(self, limit, max_cycles_to_enumerate);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Quick cycle break suggestions (faster, less precise).
    /// Only uses SCC membership without full cycle enumeration.
    /// Returns JSON array of { from, to, collateral, from_id, to_id }.
    #[wasm_bindgen(js_name = quickCycleBreakEdges)]
    pub fn quick_cycle_break_edges(&self, limit: usize) -> JsValue {
        use crate::algorithms::cycles::quick_cycle_break_edges;
        let result = quick_cycle_break_edges(self, limit);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Compute coverage set (greedy vertex cover).
    /// Finds nodes that collectively "cover" all edges in the graph.
    /// Returns JSON: { items: [{node, edges_added}], edges_covered, total_edges, coverage_ratio }
    #[wasm_bindgen(js_name = coverageSet)]
    pub fn coverage_set(&self, limit: usize) -> JsValue {
        use crate::algorithms::coverage::coverage_set;
        let result = coverage_set(self, limit);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Compute coverage set with default limit of 10.
    #[wasm_bindgen(js_name = coverageSetDefault)]
    pub fn coverage_set_default(&self) -> JsValue {
        use crate::algorithms::coverage::coverage_set_default;
        let result = coverage_set_default(self);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Get just the node indices from coverage set computation.
    #[wasm_bindgen(js_name = coverageNodes)]
    pub fn coverage_nodes(&self, limit: usize) -> JsValue {
        use crate::algorithms::coverage::coverage_nodes;
        let nodes = coverage_nodes(self, limit);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Find k longest paths through the DAG.
    /// Returns JSON: { paths: [{nodes, length}], total_nodes, max_length }
    #[wasm_bindgen(js_name = kCriticalPaths)]
    pub fn k_critical_paths(&self, k: usize) -> JsValue {
        use crate::algorithms::k_paths::k_critical_paths;
        let result = k_critical_paths(self, k);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Find k longest paths with default k=5.
    #[wasm_bindgen(js_name = kCriticalPathsDefault)]
    pub fn k_critical_paths_default(&self) -> JsValue {
        use crate::algorithms::k_paths::k_critical_paths_default;
        let result = k_critical_paths_default(self);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Find nodes that increase parallelization when completed.
    /// Returns JSON: { items: [{node, parallel_gain, new_actionable}], open_nodes, current_actionable }
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = parallelCutSuggestions)]
    pub fn parallel_cut_suggestions(&self, closed_set: &[u8], limit: usize) -> JsValue {
        use crate::algorithms::parallel_cut::parallel_cut_suggestions;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = parallel_cut_suggestions(self, &closed, limit);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Find eligible actionable nodes that increase parallelization when completed.
    /// Returns JSON: { items: [{node, parallel_gain, new_actionable}], open_nodes, current_actionable }
    /// closed_js is an array of bytes where non-zero means closed.
    /// candidate_js is an optional eligibility byte mask where non-zero means eligible.
    #[wasm_bindgen(js_name = parallelCutFiltered)]
    pub fn parallel_cut_filtered(
        &self,
        closed_js: &[u8],
        limit: usize,
        candidate_js: Option<Vec<u8>>,
    ) -> JsValue {
        use crate::algorithms::parallel_cut::parallel_cut_filtered;
        use std::collections::HashSet;
        let closed: Vec<bool> = closed_js.iter().map(|&b| b != 0).collect();
        let candidates: Option<HashSet<usize>> = candidate_js.map(|mask| {
            mask.into_iter()
                .enumerate()
                .filter_map(|(idx, flag)| (flag != 0).then_some(idx))
                .collect()
        });
        let result = parallel_cut_filtered(self, &closed, limit, candidates.as_ref());
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Find parallel cut suggestions with default limit of 10.
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = parallelCutDefault)]
    pub fn parallel_cut_default(&self, closed_set: &[u8]) -> JsValue {
        use crate::algorithms::parallel_cut::parallel_cut_default;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = parallel_cut_default(self, &closed);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Get nodes ranked by how many dependents they unblock.
    /// Returns array of [node_index, unblock_count] pairs.
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = unblockRanking)]
    pub fn unblock_ranking(&self, closed_set: &[u8], limit: usize) -> JsValue {
        use crate::algorithms::parallel_cut::unblock_ranking;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = unblock_ranking(self, &closed, limit);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Extract a subgraph containing only the specified node indices.
    /// Returns a new DiGraph with renumbered indices.
    #[wasm_bindgen(js_name = subgraph)]
    pub fn subgraph(&self, indices: &[usize]) -> DiGraph {
        use crate::algorithms::subgraph::extract_subgraph;
        extract_subgraph(self, indices)
    }

    // ========================================================================
    // What-If simulation (cascade impact analysis)
    // ========================================================================

    /// What-if analysis: compute cascade impact of closing a node.
    /// Returns JSON with direct_unblocks, transitive_unblocks, unblocked_ids, cascade_ids, parallel_gain.
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = whatIfClose)]
    pub fn what_if_close(&self, node: usize, closed_set: &[u8]) -> JsValue {
        use crate::whatif::what_if_close;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = what_if_close(self, node, &closed);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Batch what-if: compute impact of closing multiple nodes together.
    /// Returns JSON with combined cascade impact.
    #[wasm_bindgen(js_name = whatIfCloseBatch)]
    pub fn what_if_close_batch(&self, nodes: &[usize], closed_set: &[u8]) -> JsValue {
        use crate::whatif::what_if_close_batch;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = what_if_close_batch(self, nodes, &closed);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Top N issues by cascade impact.
    /// Only considers currently actionable nodes.
    /// An optional candidate mask further restricts selection, without resolving
    /// excluded prerequisites. Missing mask entries are ineligible.
    /// Returns JSON array of {node, result} sorted by transitive_unblocks.
    #[wasm_bindgen(js_name = topWhatIf)]
    pub fn top_what_if(
        &self,
        closed_set: &[u8],
        limit: usize,
        candidate_set: Option<Vec<u8>>,
    ) -> JsValue {
        use crate::whatif::top_what_if;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let results = top_what_if(self, &closed, limit, candidate_set.as_deref());
        serde_wasm_bindgen::to_value(&results).unwrap_or(JsValue::NULL)
    }

    /// All issues with cascade impact, sorted by impact.
    /// Considers all open nodes (not just actionable).
    /// Returns JSON array of {node, result} sorted by transitive_unblocks.
    #[wasm_bindgen(js_name = allWhatIf)]
    pub fn all_what_if(&self, closed_set: &[u8], limit: usize) -> JsValue {
        use crate::whatif::all_what_if;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let results = all_what_if(self, &closed, limit);
        serde_wasm_bindgen::to_value(&results).unwrap_or(JsValue::NULL)
    }

    // ========================================================================
    // TopK Set (greedy submodular selection for maximum unlock)
    // ========================================================================

    /// Greedy submodular selection for maximum unlock.
    /// Finds k issues that, when completed, maximize total downstream unlocks.
    /// Returns JSON: { items: [{node, marginal_gain, unblocked_ids}], total_gain, open_nodes }
    /// closed_set is an array of bytes where non-zero means closed.
    /// Optional candidate_set restricts direct choices, not dependency context.
    /// Non-zero entries are eligible; missing entries are ineligible.
    #[wasm_bindgen(js_name = topkSet)]
    pub fn topk_set(&self, closed_set: &[u8], k: usize, candidate_set: Option<Vec<u8>>) -> JsValue {
        use crate::algorithms::topk_set::topk_set;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = topk_set(self, &closed, k, candidate_set.as_deref());
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// TopK Set with default k=5.
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = topkSetDefault)]
    pub fn topk_set_default(&self, closed_set: &[u8]) -> JsValue {
        use crate::algorithms::topk_set::topk_set_default;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let result = topk_set_default(self, &closed);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }
}

#[cfg(feature = "eigenvector")]
#[wasm_bindgen]
impl DiGraph {
    /// Compute eigenvector centrality using power iteration.
    /// Returns array of scores in node index order, normalized to unit length.
    #[wasm_bindgen(js_name = eigenvector)]
    pub fn eigenvector(&self, iterations: u32) -> JsValue {
        use crate::algorithms::eigenvector::{eigenvector, EigenvectorConfig};
        let config = EigenvectorConfig {
            iterations,
            tolerance: 1e-6,
        };
        let scores = eigenvector(self, &config);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }

    /// Compute eigenvector centrality with default parameters (50 iterations).
    #[wasm_bindgen(js_name = eigenvectorDefault)]
    pub fn eigenvector_default(&self) -> JsValue {
        use crate::algorithms::eigenvector::eigenvector_default;
        let scores = eigenvector_default(self);
        serde_wasm_bindgen::to_value(&scores).unwrap_or(JsValue::NULL)
    }
}

#[cfg(feature = "hits")]
#[wasm_bindgen]
impl DiGraph {
    /// Compute HITS hub and authority scores.
    /// Returns JSON object: { hubs: number[], authorities: number[], iterations: number }
    #[wasm_bindgen(js_name = hits)]
    pub fn hits(&self, tolerance: f64, max_iterations: u32) -> JsValue {
        use crate::algorithms::hits::{hits, HITSConfig};
        let config = HITSConfig {
            tolerance,
            max_iterations,
        };
        let result = hits(self, &config);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }

    /// Compute HITS with default parameters (tolerance=1e-6, max_iterations=100).
    #[wasm_bindgen(js_name = hitsDefault)]
    pub fn hits_default(&self) -> JsValue {
        use crate::algorithms::hits::hits_default;
        let result = hits_default(self);
        serde_wasm_bindgen::to_value(&result).unwrap_or(JsValue::NULL)
    }
}

#[cfg(feature = "kcore")]
#[wasm_bindgen]
impl DiGraph {
    /// Compute k-core numbers for all nodes.
    /// Uses undirected view of the graph.
    /// Returns array of core numbers in node index order.
    #[wasm_bindgen(js_name = kcore)]
    pub fn kcore(&self) -> JsValue {
        use crate::algorithms::kcore::kcore;
        let cores = kcore(self);
        serde_wasm_bindgen::to_value(&cores).unwrap_or(JsValue::NULL)
    }

    /// Get the degeneracy of the graph (maximum core number).
    #[wasm_bindgen(js_name = degeneracy)]
    pub fn degeneracy(&self) -> u32 {
        use crate::algorithms::kcore::degeneracy;
        degeneracy(self)
    }
}

#[cfg(feature = "slack")]
#[wasm_bindgen]
impl DiGraph {
    /// Compute slack for each node in the DAG.
    /// Slack = critical_path_length - longest_path_through_node.
    /// Zero slack means the node is on the critical path.
    /// Returns array of slack values, or zeros for cyclic graphs.
    #[wasm_bindgen(js_name = slack)]
    pub fn slack(&self) -> JsValue {
        use crate::algorithms::slack::slack;
        let s = slack(self);
        serde_wasm_bindgen::to_value(&s).unwrap_or(JsValue::NULL)
    }

    /// Get the total float (maximum slack) in the graph.
    #[wasm_bindgen(js_name = totalFloat)]
    pub fn total_float(&self) -> f64 {
        use crate::algorithms::slack::total_float;
        total_float(self)
    }
}

#[cfg(feature = "reachability")]
#[wasm_bindgen]
impl DiGraph {
    /// Get all node indices reachable from a source node (outgoing direction).
    #[wasm_bindgen(js_name = reachableFrom)]
    pub fn reachable_from(&self, source: usize) -> JsValue {
        use crate::algorithms::subgraph::reachable_from;
        let nodes = reachable_from(self, source);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get all node indices that can reach a target node (incoming direction).
    #[wasm_bindgen(js_name = reachableTo)]
    pub fn reachable_to(&self, target: usize) -> JsValue {
        use crate::algorithms::subgraph::reachable_to;
        let nodes = reachable_to(self, target);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get all nodes in the dependency cone (ancestors + node + descendants).
    #[wasm_bindgen(js_name = dependencyCone)]
    pub fn dependency_cone(&self, node: usize) -> JsValue {
        use crate::algorithms::subgraph::dependency_cone;
        let nodes = dependency_cone(self, node);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get direct blockers (successors) of a node.
    /// These are issues that must be completed before this node can start.
    #[wasm_bindgen(js_name = blockers)]
    pub fn blockers(&self, node: usize) -> JsValue {
        use crate::reachability::blockers;
        let nodes = blockers(self, node);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get direct dependents (predecessors) of a node.
    /// These are issues that depend on this node being completed.
    #[wasm_bindgen(js_name = dependents)]
    pub fn dependents(&self, node: usize) -> JsValue {
        use crate::reachability::dependents;
        let nodes = dependents(self, node);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get all actionable nodes (open nodes with all prerequisites in closed_set).
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = actionableNodes)]
    pub fn actionable_nodes(&self, closed_set: &[u8]) -> JsValue {
        use crate::reachability::actionable_nodes;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let nodes = actionable_nodes(self, &closed);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get open blockers for a node (successors not in closed_set).
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = openBlockers)]
    pub fn open_blockers(&self, node: usize, closed_set: &[u8]) -> JsValue {
        use crate::reachability::open_blockers;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        let nodes = open_blockers(self, node, &closed);
        serde_wasm_bindgen::to_value(&nodes).unwrap_or(JsValue::NULL)
    }

    /// Get count of open blockers for a node.
    /// closed_set is an array of bytes where non-zero means closed.
    #[wasm_bindgen(js_name = openBlockerCount)]
    pub fn open_blocker_count(&self, node: usize, closed_set: &[u8]) -> usize {
        use crate::reachability::open_blocker_count;
        let closed: Vec<bool> = closed_set.iter().map(|&b| b != 0).collect();
        open_blocker_count(self, node, &closed)
    }
}

// Internal methods (not exposed to WASM)
impl DiGraph {
    /// Get successors slice (internal use).
    pub(crate) fn successors_slice(&self, node: usize) -> &[usize] {
        self.adj.get(node).map_or(&[], |v| v.as_slice())
    }

    /// Get predecessors slice (internal use).
    pub(crate) fn predecessors_slice(&self, node: usize) -> &[usize] {
        self.rev_adj.get(node).map_or(&[], |v| v.as_slice())
    }

    /// Iterate over all edges (internal use).
    pub(crate) fn edges(&self) -> impl Iterator<Item = (usize, usize)> + '_ {
        self.adj
            .iter()
            .enumerate()
            .flat_map(|(from, tos)| tos.iter().map(move |&to| (from, to)))
    }

    /// Collect edges as vec (for serialization).
    fn edges_vec(&self) -> Vec<(usize, usize)> {
        self.edges().collect()
    }

    /// Get node count (internal, non-WASM).
    pub(crate) fn len(&self) -> usize {
        self.nodes.len()
    }

    /// Check if graph is empty.
    #[allow(dead_code)]
    pub(crate) fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }

    /// Deterministic seeded approximate betweenness backing `betweennessApprox`.
    pub(crate) fn betweenness_approx_seeded(&self, sample_size: usize) -> Vec<f64> {
        crate::algorithms::betweenness::betweenness_approx(self, sample_size, Some(1))
    }
}

impl Default for DiGraph {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_graph() {
        let g = DiGraph::new();
        assert_eq!(g.node_count(), 0);
        assert_eq!(g.edge_count(), 0);
    }

    #[test]
    fn test_add_node_idempotent() {
        let mut g = DiGraph::new();
        let idx1 = g.add_node("bv-1");
        let idx2 = g.add_node("bv-1");
        assert_eq!(idx1, idx2);
        assert_eq!(g.node_count(), 1);
    }

    #[test]
    fn test_add_edge_idempotent() {
        let mut g = DiGraph::new();
        let a = g.add_node("a");
        let b = g.add_node("b");
        g.add_edge(a, b);
        g.add_edge(a, b); // Should be idempotent
        assert_eq!(g.edge_count(), 1);
    }

    #[test]
    fn test_degrees() {
        let mut g = DiGraph::new();
        let a = g.add_node("a");
        let b = g.add_node("b");
        let c = g.add_node("c");
        g.add_edge(a, b);
        g.add_edge(a, c);
        g.add_edge(b, c);

        assert_eq!(g.out_degree(a), 2);
        assert_eq!(g.out_degree(b), 1);
        assert_eq!(g.out_degree(c), 0);

        assert_eq!(g.in_degree(a), 0);
        assert_eq!(g.in_degree(b), 1);
        assert_eq!(g.in_degree(c), 2);
    }

    #[test]
    fn test_density() {
        let mut g = DiGraph::new();
        assert_eq!(g.density(), 0.0);

        g.add_node("a");
        assert_eq!(g.density(), 0.0); // 1 node, no edges possible

        let a = 0;
        let b = g.add_node("b");
        g.add_edge(a, b);
        // 2 nodes, 1 edge: 1 / (2 * 1) = 0.5
        assert!((g.density() - 0.5).abs() < 0.001);
    }

    #[test]
    fn test_json_roundtrip() {
        let mut g = DiGraph::new();
        let a = g.add_node("a");
        let b = g.add_node("b");
        g.add_edge(a, b);

        let json = g.to_json();
        let g2 = DiGraph::from_json(&json).unwrap();

        assert_eq!(g2.node_count(), 2);
        assert_eq!(g2.edge_count(), 1);
        assert_eq!(g2.node_id(0), Some("a".to_string()));
        assert_eq!(g2.node_id(1), Some("b".to_string()));
    }

    #[test]
    fn test_betweenness_approx_seeded_deterministic() {
        let mut g = DiGraph::new();
        for i in 0..40 {
            g.add_node(&format!("n-{i}"));
        }
        // Build a two-cluster hourglass through bridge 19 -> 20
        for i in 0..19 {
            g.add_edge(i, 19);
        }
        g.add_edge(19, 20);
        for i in 21..40 {
            g.add_edge(20, i);
        }

        let first = g.betweenness_approx_seeded(10);
        for _ in 0..10 {
            assert_eq!(g.betweenness_approx_seeded(10), first);
        }
        assert!(first[19] > 0.0 && first[20] > 0.0, "bridge endpoints must receive positive approx betweenness");
        assert_eq!(first[0], 0.0, "source leaf must have zero betweenness");
    }
}
