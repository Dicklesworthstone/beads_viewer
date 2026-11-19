//! Articulation points (cut vertices) algorithm.
//!
//! Finds nodes whose removal disconnects the graph.
//! Uses Tarjan's algorithm on the undirected view.
//!
//! In issue tracking, articulation points are critical coordination
//! points - if those issues are blocked or deprioritized, they can
//! disconnect groups of related work.

use crate::graph::DiGraph;
use std::collections::HashSet;

/// Find articulation points (cut vertices) using Tarjan's algorithm.
///
/// An articulation point is a vertex whose removal increases the number
/// of connected components in the graph.
///
/// # Algorithm
/// Uses DFS to compute discovery times and low-link values:
/// - disc[v]: discovery time of vertex v
/// - low[v]: minimum discovery time reachable from subtree of v
///
/// A vertex v is an articulation point if:
/// 1. v is root of DFS tree and has >1 children, OR
/// 2. v is not root and has child u with low[u] >= disc[v]
///
/// # Returns
/// Vector of node indices that are articulation points.
pub fn articulation_points(graph: &DiGraph) -> Vec<usize> {
    let n = graph.len();
    if n == 0 {
        return Vec::new();
    }

    // Build undirected adjacency
    let neighbors = build_undirected_neighbors(graph);

    let mut dfs = ArticulationDfs {
        neighbors: &neighbors,
        disc: vec![0usize; n],
        low: vec![0usize; n],
        parent: vec![usize::MAX; n], // usize::MAX means no parent (root)
        visited: vec![false; n],
        is_ap: vec![false; n],
        time: 0usize,
    };

    // Run DFS from each unvisited node (handles disconnected components)
    for start in 0..n {
        if !dfs.visited[start] {
            dfs.visit(start);
        }
    }

    // Collect articulation points
    dfs.is_ap
        .iter()
        .enumerate()
        .filter_map(|(i, &ap)| if ap { Some(i) } else { None })
        .collect()
}

struct ArticulationDfs<'a> {
    neighbors: &'a [Vec<usize>],
    disc: Vec<usize>,
    low: Vec<usize>,
    parent: Vec<usize>,
    visited: Vec<bool>,
    is_ap: Vec<bool>,
    time: usize,
}

impl ArticulationDfs<'_> {
    /// DFS for Tarjan's articulation point algorithm.
    fn visit(&mut self, v: usize) {
        self.visited[v] = true;
        self.time += 1;
        self.disc[v] = self.time;
        self.low[v] = self.time;
        let mut children = 0;

        for &u in &self.neighbors[v] {
            if !self.visited[u] {
                children += 1;
                self.parent[u] = v;

                self.visit(u);

                // Update low-link
                self.low[v] = self.low[v].min(self.low[u]);

                // Check if v is an articulation point
                // Case 1: v is root with >1 DFS children
                if self.parent[v] == usize::MAX && children > 1 {
                    self.is_ap[v] = true;
                }

                // Case 2: v is not root and low[u] >= disc[v]
                // This means u (and its subtree) cannot reach any ancestor of v
                if self.parent[v] != usize::MAX && self.low[u] >= self.disc[v] {
                    self.is_ap[v] = true;
                }
            } else if u != self.parent[v] {
                // Back edge (not to parent)
                self.low[v] = self.low[v].min(self.disc[u]);
            }
        }
    }
}

/// Build undirected neighbor lists from directed graph.
fn build_undirected_neighbors(graph: &DiGraph) -> Vec<Vec<usize>> {
    let n = graph.len();
    let mut neighbors: Vec<HashSet<usize>> = vec![HashSet::new(); n];

    for u in 0..n {
        for &v in graph.successors_slice(u) {
            if u != v {
                // Skip self-loops
                neighbors[u].insert(v);
                neighbors[v].insert(u);
            }
        }
    }

    // Convert to sorted Vec<Vec> for deterministic DFS traversal
    neighbors
        .into_iter()
        .map(|s| {
            let mut list: Vec<usize> = s.into_iter().collect();
            list.sort_unstable();
            list
        })
        .collect()
}

/// Count bridges (cut edges) in the graph.
/// A bridge is an edge whose removal disconnects the graph.
pub fn bridges(graph: &DiGraph) -> Vec<(usize, usize)> {
    let n = graph.len();
    if n == 0 {
        return Vec::new();
    }

    let neighbors = build_undirected_neighbors(graph);

    let mut dfs = BridgeDfs {
        neighbors: &neighbors,
        disc: vec![0usize; n],
        low: vec![0usize; n],
        parent: vec![usize::MAX; n],
        visited: vec![false; n],
        bridges: Vec::new(),
        time: 0usize,
    };

    for start in 0..n {
        if !dfs.visited[start] {
            dfs.visit(start);
        }
    }

    dfs.bridges.sort_unstable();
    dfs.bridges
}

struct BridgeDfs<'a> {
    neighbors: &'a [Vec<usize>],
    disc: Vec<usize>,
    low: Vec<usize>,
    parent: Vec<usize>,
    visited: Vec<bool>,
    bridges: Vec<(usize, usize)>,
    time: usize,
}

impl BridgeDfs<'_> {
    /// DFS for bridge detection.
    fn visit(&mut self, v: usize) {
        self.visited[v] = true;
        self.time += 1;
        self.disc[v] = self.time;
        self.low[v] = self.time;

        for &u in &self.neighbors[v] {
            if !self.visited[u] {
                self.parent[u] = v;
                self.visit(u);

                self.low[v] = self.low[v].min(self.low[u]);

                // Bridge condition: if low[u] > disc[v], edge v-u is a bridge
                if self.low[u] > self.disc[v] {
                    self.bridges.push((v.min(u), v.max(u))); // Canonical order
                }
            } else if u != self.parent[v] {
                self.low[v] = self.low[v].min(self.disc[u]);
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_articulation_empty() {
        let graph = DiGraph::new();
        let ap = articulation_points(&graph);
        assert!(ap.is_empty());
    }

    #[test]
    fn test_articulation_single_node() {
        let mut graph = DiGraph::new();
        graph.add_node("a");
        let ap = articulation_points(&graph);
        assert!(ap.is_empty()); // Single node is not an articulation point
    }

    #[test]
    fn test_articulation_two_nodes() {
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);

        let ap = articulation_points(&graph);
        // Neither node is an articulation point (removing either leaves isolated node)
        assert!(ap.is_empty());
    }

    #[test]
    fn test_articulation_chain() {
        // a -> b -> c
        // b is an articulation point
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let ap = articulation_points(&graph);
        assert_eq!(ap.len(), 1);
        assert!(ap.contains(&b));
    }

    #[test]
    fn test_articulation_triangle() {
        // a -> b -> c -> a (cycle/triangle)
        // No articulation points
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let ap = articulation_points(&graph);
        assert!(ap.is_empty());
    }

    #[test]
    fn test_articulation_star() {
        // Hub with 3 leaves
        // Hub is an articulation point if it connects >1 component
        let mut graph = DiGraph::new();
        let hub = graph.add_node("hub");
        let l1 = graph.add_node("l1");
        let l2 = graph.add_node("l2");
        let l3 = graph.add_node("l3");
        graph.add_edge(hub, l1);
        graph.add_edge(hub, l2);
        graph.add_edge(hub, l3);

        let ap = articulation_points(&graph);
        // Hub is articulation point (root with >1 children)
        assert_eq!(ap.len(), 1);
        assert!(ap.contains(&hub));
    }

    #[test]
    fn test_articulation_bowtie() {
        // Two triangles connected by a single node
        //     a       d
        //    / \     / \
        //   b---c---e---f
        // c is the articulation point
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        let e = graph.add_node("e");
        let f = graph.add_node("f");

        // Left triangle
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        // Bridge
        graph.add_edge(c, e);

        // Right triangle
        graph.add_edge(d, e);
        graph.add_edge(e, f);
        graph.add_edge(f, d);

        let ap = articulation_points(&graph);
        // c and e are both articulation points
        assert!(ap.contains(&c) || ap.contains(&e));
    }

    #[test]
    fn test_articulation_bridge_chain() {
        // a-b-c-d (simple path)
        // b and c are articulation points
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, d);

        let ap = articulation_points(&graph);
        assert_eq!(ap.len(), 2);
        assert!(ap.contains(&b));
        assert!(ap.contains(&c));
    }

    #[test]
    fn test_bridges_chain() {
        // a-b-c
        // Two bridges: (a,b) and (b,c)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let br = bridges(&graph);
        assert_eq!(br.len(), 2);
    }

    #[test]
    fn test_bridges_triangle() {
        // Triangle has no bridges
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let br = bridges(&graph);
        assert!(br.is_empty());
    }

    #[test]
    fn test_articulation_disconnected() {
        // Two disconnected triangles - no articulation points
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        let e = graph.add_node("e");
        let f = graph.add_node("f");

        // Triangle 1
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        // Triangle 2
        graph.add_edge(d, e);
        graph.add_edge(e, f);
        graph.add_edge(f, d);

        let ap = articulation_points(&graph);
        assert!(ap.is_empty());
    }

    #[test]
    fn test_bridges_deterministic_sorted_order() {
        for _ in 0..24 {
            let mut graph = DiGraph::new();
            let n0 = graph.add_node("n0");
            let n1 = graph.add_node("n1");
            let n2 = graph.add_node("n2");
            let n3 = graph.add_node("n3");
            let n4 = graph.add_node("n4");
            let n5 = graph.add_node("n5");

            // Add edges in varying order; (3,4,5) form a triangle (non-bridges)
            graph.add_edge(n0, n3);
            graph.add_edge(n3, n4);
            graph.add_edge(n4, n5);
            graph.add_edge(n5, n3);
            graph.add_edge(n0, n1);
            graph.add_edge(n1, n2);

            let br = bridges(&graph);
            assert_eq!(br, vec![(n0, n1), (n0, n3), (n1, n2)]);
        }
    }
}
