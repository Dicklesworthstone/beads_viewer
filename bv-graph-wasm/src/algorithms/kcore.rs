//! K-Core Decomposition algorithm implementation.
//!
//! K-core decomposition finds the maximal subgraph where every node has degree >= k.
//! A node's core number is the highest k for which it's in the k-core.
//! High core numbers indicate densely connected regions.

use crate::graph::DiGraph;
use std::collections::HashSet;

/// Compute k-core numbers for all nodes.
///
/// Uses undirected view: edge u→v is treated as u--v.
/// Employs the standard "k-peeling" algorithm.
///
/// Returns vector of core numbers in node index order.
pub fn kcore(graph: &DiGraph) -> Vec<u32> {
    let n = graph.len();
    if n == 0 {
        return Vec::new();
    }

    // Build undirected degree and neighbor lists
    // For directed graph, treat as undirected (symmetric edges)
    let mut neighbors: Vec<HashSet<usize>> = vec![HashSet::new(); n];

    for u in 0..n {
        for &v in graph.successors_slice(u) {
            if u != v {
                neighbors[u].insert(v);
                neighbors[v].insert(u);
            }
        }
    }

    // Compute undirected degree
    let mut degree: Vec<usize> = neighbors.iter().map(|s| s.len()).collect();

    // Use bucket-based k-peeling (more efficient for sparse graphs)
    let max_deg = degree.iter().cloned().max().unwrap_or(0);

    // Build degree buckets
    let mut bucket: Vec<Vec<usize>> = vec![Vec::new(); max_deg + 1];
    for i in 0..n {
        bucket[degree[i]].push(i);
    }

    let mut core = vec![0u32; n];
    let mut processed = vec![false; n];
    let mut current_core = 0usize;

    for _ in 0..n {
        // Find node with minimum degree (from lowest non-empty bucket)
        while current_core <= max_deg && bucket[current_core].is_empty() {
            current_core += 1;
        }

        if current_core > max_deg {
            break;
        }

        // Get a node from this bucket
        let v = bucket[current_core].pop().unwrap();
        if processed[v] {
            continue;
        }

        processed[v] = true;
        core[v] = current_core as u32;

        // Update neighbors' degrees
        for &nbr in &neighbors[v] {
            if processed[nbr] {
                continue;
            }

            let old_deg = degree[nbr];
            if old_deg > current_core {
                // Remove from old bucket (if still there)
                bucket[old_deg].retain(|&x| x != nbr);
                degree[nbr] -= 1;
                let new_deg = degree[nbr];
                // Add to new bucket (may go below current_core)
                bucket[new_deg].push(nbr);
            }
        }
    }

    core
}

/// Get the maximum core number (degeneracy of the graph).
pub fn degeneracy(graph: &DiGraph) -> u32 {
    kcore(graph).into_iter().max().unwrap_or(0)
}

/// Get nodes in the k-core (nodes with core number >= k).
pub fn nodes_in_kcore(graph: &DiGraph, k: u32) -> Vec<usize> {
    kcore(graph)
        .into_iter()
        .enumerate()
        .filter(|(_, c)| *c >= k)
        .map(|(i, _)| i)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_kcore_empty() {
        let graph = DiGraph::new();
        let cores = kcore(&graph);
        assert!(cores.is_empty());
    }

    #[test]
    fn test_kcore_single_node() {
        let mut graph = DiGraph::new();
        graph.add_node("a");
        let cores = kcore(&graph);
        assert_eq!(cores, vec![0]); // Isolated node has core 0
    }

    #[test]
    fn test_kcore_chain() {
        // a -> b -> c (linear chain)
        // All nodes have undirected degree 1 or 2, so max core is 1
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let cores = kcore(&graph);
        // a has degree 1, b has degree 2, c has degree 1
        // In k-peeling: k=1 peels off all (since all connected)
        // Actually, a and c have degree 1, b has degree 2
        // Start with k=1: a removed (deg 1 < 2), then b's deg drops to 1, then c
        // But k=1 means we keep nodes with degree >= 1
        // So all nodes are in 1-core
        assert_eq!(cores[a], 1);
        assert_eq!(cores[b], 1);
        assert_eq!(cores[c], 1);
    }

    #[test]
    fn test_kcore_triangle() {
        // a -> b, b -> c, c -> a (triangle/cycle)
        // Each node has undirected degree 2, so all are in 2-core
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let cores = kcore(&graph);
        // All nodes have degree 2 in undirected view
        assert_eq!(cores[a], 2);
        assert_eq!(cores[b], 2);
        assert_eq!(cores[c], 2);
    }

    #[test]
    fn test_kcore_star() {
        // Hub with 3 spokes: a -> b, a -> c, a -> d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(a, d);

        let cores = kcore(&graph);
        // Hub a has degree 3, spokes have degree 1
        // All nodes are in 1-core (spokes have deg 1)
        assert_eq!(cores[a], 1);
        assert_eq!(cores[b], 1);
        assert_eq!(cores[c], 1);
        assert_eq!(cores[d], 1);
    }

    #[test]
    fn test_kcore_clique_with_pendant() {
        // Clique of 4: a-b-c-d all connected
        // Plus pendant e connected only to a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        let e = graph.add_node("e");

        // Make clique (all pairs connected)
        graph.add_edge(a, b);
        graph.add_edge(b, a);
        graph.add_edge(a, c);
        graph.add_edge(c, a);
        graph.add_edge(a, d);
        graph.add_edge(d, a);
        graph.add_edge(b, c);
        graph.add_edge(c, b);
        graph.add_edge(b, d);
        graph.add_edge(d, b);
        graph.add_edge(c, d);
        graph.add_edge(d, c);

        // Add pendant
        graph.add_edge(a, e);

        let cores = kcore(&graph);
        // a, b, c, d form a 3-core (each has degree >= 3 in the clique)
        // e has degree 1, so it's in 1-core
        assert_eq!(cores[e], 1);
        assert!(cores[a] >= 3, "a should be in 3-core, got {}", cores[a]);
        assert!(cores[b] >= 3, "b should be in 3-core, got {}", cores[b]);
        assert!(cores[c] >= 3, "c should be in 3-core, got {}", cores[c]);
        assert!(cores[d] >= 3, "d should be in 3-core, got {}", cores[d]);
    }

    #[test]
    fn test_kcore_disconnected() {
        // Two disconnected nodes
        let mut graph = DiGraph::new();
        graph.add_node("a");
        graph.add_node("b");

        let cores = kcore(&graph);
        // Both isolated, core 0
        assert_eq!(cores, vec![0, 0]);
    }

    #[test]
    fn test_degeneracy() {
        // Triangle has degeneracy 2
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        assert_eq!(degeneracy(&graph), 2);
    }

    #[test]
    fn test_nodes_in_kcore() {
        // Triangle plus isolated node
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d"); // Isolated
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let in_2core = nodes_in_kcore(&graph, 2);
        assert_eq!(in_2core.len(), 3);
        assert!(in_2core.contains(&a));
        assert!(in_2core.contains(&b));
        assert!(in_2core.contains(&c));
        assert!(!in_2core.contains(&d));

        let in_1core = nodes_in_kcore(&graph, 1);
        // Only the triangle nodes are in 1-core (d is isolated, core 0)
        assert_eq!(in_1core.len(), 3);
    }

    #[test]
    fn test_kcore_diamond() {
        //     a
        //    / \
        //   b---c
        //    \ /
        //     d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(b, c);
        graph.add_edge(b, d);
        graph.add_edge(c, d);

        let cores = kcore(&graph);
        // All nodes connected in diamond shape
        // a: connected to b, c (deg 2)
        // b: connected to a, c, d (deg 3)
        // c: connected to a, b, d (deg 3)
        // d: connected to b, c (deg 2)
        // 2-core includes all nodes
        assert!(cores.iter().all(|&c| c >= 2), "All should be in 2-core");
    }

    #[test]
    fn test_kcore_ignores_self_loops() {
        let mut graph = DiGraph::new();
        let solo = graph.add_node("solo");
        let leaf = graph.add_node("leaf");
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");

        // Self-loops must not inflate degree or core number
        graph.add_edge(solo, solo);
        graph.add_edge(leaf, leaf);
        graph.add_edge(leaf, a);
        graph.add_edge(a, a);

        // Positive control: genuine 3-clique remains 2-core
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let cores = kcore(&graph);
        assert_eq!(cores[solo], 0, "isolated self-loop node must have k-core 0");
        assert_eq!(cores[leaf], 1, "pendant node with self-loop must stay in 1-core");
        assert_eq!(cores[a], 2);
        assert_eq!(cores[b], 2);
        assert_eq!(cores[c], 2);
    }
}
