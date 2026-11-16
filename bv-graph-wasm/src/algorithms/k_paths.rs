//! K-Shortest Critical Paths algorithm.
//!
//! Finds multiple critical paths through the dependency graph.
//! Uses topological ordering to compute longest paths efficiently.

use crate::algorithms::topo::topological_sort;
use crate::graph::DiGraph;
use serde::Serialize;
use std::cmp::Reverse;

/// A single critical path through the graph.
#[derive(Debug, Clone, Serialize)]
pub struct CriticalPath {
    /// Node indices in path order (source to sink)
    pub nodes: Vec<usize>,
    /// Length of the path (number of nodes)
    pub length: usize,
}

/// Result of K-shortest paths computation.
#[derive(Debug, Clone, Serialize)]
pub struct KPathsResult {
    /// The k longest paths found
    pub paths: Vec<CriticalPath>,
    /// Total number of nodes in graph
    pub total_nodes: usize,
    /// Maximum path length found
    pub max_length: usize,
}

/// Find k longest paths in a DAG.
///
/// This implementation finds the k paths with the longest distances,
/// ending at k different sink nodes. This is Option A from the bead description:
/// we find k distinct sinks by longest distance and reconstruct one path each.
///
/// # Arguments
/// * `graph` - The directed graph (should be a DAG)
/// * `k` - Maximum number of paths to return
///
/// # Returns
/// A vector of CriticalPath structs, sorted by length descending.
///
/// # Note
/// For cyclic graphs, returns empty result since topological sort fails.
pub fn k_critical_paths(graph: &DiGraph, k: usize) -> KPathsResult {
    let n = graph.node_count();

    if n == 0 {
        return KPathsResult {
            paths: Vec::new(),
            total_nodes: 0,
            max_length: 0,
        };
    }

    // Get topological order - returns None if graph has cycles
    let order = match topological_sort(graph) {
        Some(o) => o,
        None => {
            return KPathsResult {
                paths: Vec::new(),
                total_nodes: n,
                max_length: 0,
            }
        }
    };

    // Compute distances (longest path lengths) and predecessors
    // dist[v] = length of longest path ending at v
    let mut dist = vec![0usize; n];
    let mut pred: Vec<Option<usize>> = vec![None; n];

    // Process in topological order
    for &v in &order {
        for &u in graph.predecessors_slice(v) {
            if dist[u] + 1 > dist[v] {
                dist[v] = dist[u] + 1;
                pred[v] = Some(u);
            }
        }
    }

    // Restrict candidate endpoints to sink nodes (out-degree 0) with positive
    // incoming path length so a single chain emits 1 full sink-ending path
    // instead of nested intermediate prefixes.
    let mut candidates: Vec<(usize, usize)> = (0..n)
        .filter(|&v| graph.out_degree(v) == 0 && dist[v] > 0)
        .map(|v| (v, dist[v]))
        .collect();

    // Sort deterministically by distance descending, then sink index ascending.
    candidates.sort_by_key(|&(v, d)| (Reverse(d), v));
    candidates.truncate(k);

    // Find max length
    let max_length = candidates.first().map(|(_, d)| *d + 1).unwrap_or(0);

    // Reconstruct paths
    let paths: Vec<CriticalPath> = candidates
        .iter()
        .map(|&(sink, _)| {
            let mut path = vec![sink];
            let mut curr = sink;
            while let Some(p) = pred[curr] {
                path.push(p);
                curr = p;
            }
            path.reverse();
            CriticalPath {
                length: path.len(),
                nodes: path,
            }
        })
        .collect();

    KPathsResult {
        paths,
        total_nodes: n,
        max_length,
    }
}

/// Find k longest paths with default k=5.
pub fn k_critical_paths_default(graph: &DiGraph) -> KPathsResult {
    k_critical_paths(graph, 5)
}

/// Get just the path node indices.
pub fn k_path_nodes(graph: &DiGraph, k: usize) -> Vec<Vec<usize>> {
    k_critical_paths(graph, k)
        .paths
        .into_iter()
        .map(|p| p.nodes)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_graph(edges: &[(usize, usize)]) -> DiGraph {
        let mut g = DiGraph::new();
        let max_node = edges.iter().flat_map(|(a, b)| [*a, *b]).max().unwrap_or(0);
        for i in 0..=max_node {
            g.add_node(&format!("n{}", i));
        }
        for (from, to) in edges {
            g.add_edge(*from, *to);
        }
        g
    }

    #[test]
    fn test_empty_graph() {
        let g = DiGraph::new();
        let result = k_critical_paths(&g, 5);
        assert!(result.paths.is_empty());
        assert_eq!(result.total_nodes, 0);
    }

    #[test]
    fn test_single_node() {
        let mut g = DiGraph::new();
        g.add_node("a");
        let result = k_critical_paths(&g, 5);
        assert!(result.paths.is_empty()); // No paths for isolated node
        assert_eq!(result.total_nodes, 1);
    }

    #[test]
    fn test_single_edge() {
        let g = make_graph(&[(0, 1)]);
        let result = k_critical_paths(&g, 5);

        assert_eq!(result.paths.len(), 1);
        assert_eq!(result.paths[0].nodes, vec![0, 1]);
        assert_eq!(result.paths[0].length, 2);
        assert_eq!(result.max_length, 2);
    }

    #[test]
    fn test_chain() {
        // Chain: 0 -> 1 -> 2 -> 3
        let g = make_graph(&[(0, 1), (1, 2), (2, 3)]);
        let result = k_critical_paths(&g, 5);

        assert_eq!(result.paths.len(), 1);
        // The longest path should be the entire chain
        assert_eq!(result.paths[0].nodes, vec![0, 1, 2, 3]);
        assert_eq!(result.paths[0].length, 4);
        assert_eq!(result.max_length, 4);
    }

    #[test]
    fn test_single_six_node_chain_emits_one_full_sink_path() {
        // Chain: 0 -> 1 -> 2 -> 3 -> 4 -> 5
        let g = make_graph(&[(0, 1), (1, 2), (2, 3), (3, 4), (4, 5)]);
        let result = k_critical_paths(&g, 5);

        assert_eq!(result.paths.len(), 1);
        assert_eq!(result.paths[0].nodes, vec![0, 1, 2, 3, 4, 5]);
        assert_eq!(result.paths[0].length, 6);
        assert_eq!(result.max_length, 6);
    }

    #[test]
    fn test_multi_sink_dag_emits_k_distinct_sink_paths_deterministically() {
        // Sink 5: 0 -> 1 -> 2 -> 5 (len 4)
        // Sink 6: 0 -> 3 -> 6      (len 3)
        // Sink 7: 0 -> 4 -> 7      (len 3, tied with sink 6 -> deterministic order 6 before 7)
        // Sink 8: 0 -> 8           (len 2)
        let g = make_graph(&[
            (0, 1),
            (1, 2),
            (2, 5),
            (0, 3),
            (3, 6),
            (0, 4),
            (4, 7),
            (0, 8),
        ]);
        let result = k_critical_paths(&g, 3);

        assert_eq!(result.paths.len(), 3);
        assert_eq!(result.paths[0].nodes, vec![0, 1, 2, 5]);
        assert_eq!(result.paths[1].nodes, vec![0, 3, 6]);
        assert_eq!(result.paths[2].nodes, vec![0, 4, 7]);
        assert_eq!(result.max_length, 4);
    }

    #[test]
    fn test_diamond() {
        // Diamond: 0 -> 1 -> 3, 0 -> 2 -> 3
        let g = make_graph(&[(0, 1), (0, 2), (1, 3), (2, 3)]);
        let result = k_critical_paths(&g, 5);

        // Should find one path of length 3
        assert!(!result.paths.is_empty());
        assert_eq!(result.paths[0].length, 3);
        assert_eq!(result.max_length, 3);
    }

    #[test]
    fn test_multiple_paths() {
        // Two independent chains:
        // 0 -> 1 -> 2 (length 3)
        // 3 -> 4 (length 2)
        let g = make_graph(&[(0, 1), (1, 2), (3, 4)]);
        let result = k_critical_paths(&g, 5);

        assert!(result.paths.len() >= 2);
        // First path should be the longer one
        assert_eq!(result.paths[0].length, 3);
        assert!(result.paths[1].length >= 2);
    }

    #[test]
    fn test_k_limit() {
        // Create many paths
        let g = make_graph(&[(0, 1), (2, 3), (4, 5), (6, 7), (8, 9)]);
        let result = k_critical_paths(&g, 2);

        assert!(result.paths.len() <= 2);
    }

    #[test]
    fn test_cycle_returns_empty() {
        // Cycle: 0 -> 1 -> 2 -> 0
        let g = make_graph(&[(0, 1), (1, 2), (2, 0)]);
        let result = k_critical_paths(&g, 5);

        // Should return empty for cyclic graph
        assert!(result.paths.is_empty());
    }
}
