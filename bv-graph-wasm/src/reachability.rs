//! Reachability queries (from/to).
//!
//! Find all nodes reachable from or that can reach a given node.
//! Essential for impact analysis and dependency exploration.

#![cfg_attr(not(feature = "reachability"), allow(dead_code))]

use crate::graph::DiGraph;
use std::collections::VecDeque;

/// Find all nodes reachable from source (BFS forward).
/// Returns all nodes in the forward closure, including the source.
pub fn reachable_from(graph: &DiGraph, source: usize) -> Vec<usize> {
    let n = graph.len();
    if source >= n {
        return Vec::new();
    }

    let mut visited = vec![false; n];
    let mut queue = VecDeque::new();
    let mut result = Vec::new();

    queue.push_back(source);
    visited[source] = true;

    while let Some(v) = queue.pop_front() {
        result.push(v);
        for &w in graph.successors_slice(v) {
            if !visited[w] {
                visited[w] = true;
                queue.push_back(w);
            }
        }
    }

    result
}

/// Find all nodes that can reach target (BFS backward).
/// Returns all nodes in the backward closure, including the target.
pub fn reachable_to(graph: &DiGraph, target: usize) -> Vec<usize> {
    let n = graph.len();
    if target >= n {
        return Vec::new();
    }

    let mut visited = vec![false; n];
    let mut queue = VecDeque::new();
    let mut result = Vec::new();

    queue.push_back(target);
    visited[target] = true;

    while let Some(v) = queue.pop_front() {
        result.push(v);
        for &u in graph.predecessors_slice(v) {
            if !visited[u] {
                visited[u] = true;
                queue.push_back(u);
            }
        }
    }

    result
}

/// Get direct blockers (successors) of a node.
/// These are issues that must be completed before this node can start.
pub fn blockers(graph: &DiGraph, node: usize) -> Vec<usize> {
    graph.successors_slice(node).to_vec()
}

/// Get direct dependents (predecessors) of a node.
/// These are issues that depend on this node being completed.
pub fn dependents(graph: &DiGraph, node: usize) -> Vec<usize> {
    graph.predecessors_slice(node).to_vec()
}

/// Check if all prerequisites (successors) of node are in the closed set.
/// A node is actionable if all its blockers are closed.
pub fn is_actionable(graph: &DiGraph, node: usize, closed_set: &[bool]) -> bool {
    graph
        .successors_slice(node)
        .iter()
        .all(|&p| closed_set.get(p).copied().unwrap_or(false))
}

/// Get all actionable nodes (no open blockers).
/// An actionable node has all its prerequisites in the closed set.
pub fn actionable_nodes(graph: &DiGraph, closed_set: &[bool]) -> Vec<usize> {
    (0..graph.len())
        .filter(|&i| !closed_set.get(i).copied().unwrap_or(false))
        .filter(|&i| is_actionable(graph, i, closed_set))
        .collect()
}

/// Get open blockers for a node (successors not in closed set).
pub fn open_blockers(graph: &DiGraph, node: usize, closed_set: &[bool]) -> Vec<usize> {
    graph
        .successors_slice(node)
        .iter()
        .filter(|&&p| !closed_set.get(p).copied().unwrap_or(false))
        .copied()
        .collect()
}

/// Count of open blockers for a node.
pub fn open_blocker_count(graph: &DiGraph, node: usize, closed_set: &[bool]) -> usize {
    graph
        .successors_slice(node)
        .iter()
        .filter(|&&p| !closed_set.get(p).copied().unwrap_or(false))
        .count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_reachable_from_empty() {
        let graph = DiGraph::new();
        assert!(reachable_from(&graph, 0).is_empty());
    }

    #[test]
    fn test_reachable_from_single() {
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let result = reachable_from(&graph, a);
        assert_eq!(result, vec![a]);
    }

    #[test]
    fn test_reachable_from_chain() {
        // a -> b -> c -> d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, d);

        let from_a = reachable_from(&graph, a);
        assert_eq!(from_a.len(), 4);
        assert!(from_a.contains(&a));
        assert!(from_a.contains(&b));
        assert!(from_a.contains(&c));
        assert!(from_a.contains(&d));

        let from_c = reachable_from(&graph, c);
        assert_eq!(from_c.len(), 2);
        assert!(from_c.contains(&c));
        assert!(from_c.contains(&d));
    }

    #[test]
    fn test_reachable_from_diamond() {
        //     a
        //    / \
        //   b   c
        //    \ /
        //     d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(b, d);
        graph.add_edge(c, d);

        let from_a = reachable_from(&graph, a);
        assert_eq!(from_a.len(), 4);

        let from_b = reachable_from(&graph, b);
        assert_eq!(from_b.len(), 2);
        assert!(from_b.contains(&b));
        assert!(from_b.contains(&d));
    }

    #[test]
    fn test_reachable_to_chain() {
        // a -> b -> c -> d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, d);

        let to_d = reachable_to(&graph, d);
        assert_eq!(to_d.len(), 4);
        assert!(to_d.contains(&a));
        assert!(to_d.contains(&b));
        assert!(to_d.contains(&c));
        assert!(to_d.contains(&d));

        let to_b = reachable_to(&graph, b);
        assert_eq!(to_b.len(), 2);
        assert!(to_b.contains(&a));
        assert!(to_b.contains(&b));
    }

    #[test]
    fn test_reachable_to_diamond() {
        //     a
        //    / \
        //   b   c
        //    \ /
        //     d
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(b, d);
        graph.add_edge(c, d);

        let to_d = reachable_to(&graph, d);
        assert_eq!(to_d.len(), 4);

        let to_c = reachable_to(&graph, c);
        assert_eq!(to_c.len(), 2);
        assert!(to_c.contains(&a));
        assert!(to_c.contains(&c));
    }

    #[test]
    fn test_reachable_invalid_node() {
        let mut graph = DiGraph::new();
        graph.add_node("a");
        assert!(reachable_from(&graph, 999).is_empty());
        assert!(reachable_to(&graph, 999).is_empty());
    }

    #[test]
    fn test_blockers_and_dependents() {
        // c -> a, c -> b (c has two prerequisites)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(c, a);
        graph.add_edge(c, b);

        let c_blockers = blockers(&graph, c);
        assert_eq!(c_blockers.len(), 2);
        assert!(c_blockers.contains(&a));
        assert!(c_blockers.contains(&b));

        let a_dependents = dependents(&graph, a);
        assert_eq!(a_dependents, vec![c]);
        assert!(blockers(&graph, a).is_empty());
        assert!(dependents(&graph, c).is_empty());
    }

    #[test]
    fn test_is_actionable() {
        // a <- b <- c (dependent -> prerequisite)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(b, a);
        graph.add_edge(c, b);

        // Nothing closed: only a is actionable (no blockers)
        let closed_none = vec![false, false, false];
        assert!(is_actionable(&graph, a, &closed_none));
        assert!(!is_actionable(&graph, b, &closed_none));
        assert!(!is_actionable(&graph, c, &closed_none));

        // a closed: b becomes actionable
        let closed_a = vec![true, false, false];
        assert!(is_actionable(&graph, b, &closed_a));
        assert!(!is_actionable(&graph, c, &closed_a));

        // a and b closed: c becomes actionable
        let closed_ab = vec![true, true, false];
        assert!(is_actionable(&graph, c, &closed_ab));
    }

    #[test]
    fn test_actionable_nodes() {
        // b -> a, c -> a; d depends on both b and c.
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(b, a);
        graph.add_edge(c, a);
        graph.add_edge(d, b);
        graph.add_edge(d, c);

        // Nothing closed: only a is actionable
        let closed_none = vec![false, false, false, false];
        let actionable = actionable_nodes(&graph, &closed_none);
        assert_eq!(actionable, vec![a]);

        // a closed: b and c become actionable
        let closed_a = vec![true, false, false, false];
        let actionable = actionable_nodes(&graph, &closed_a);
        assert_eq!(actionable.len(), 2);
        assert!(actionable.contains(&b));
        assert!(actionable.contains(&c));

        // a, b, c closed: d becomes actionable
        let closed_abc = vec![true, true, true, false];
        let actionable = actionable_nodes(&graph, &closed_abc);
        assert_eq!(actionable, vec![d]);

        assert!(actionable_nodes(&graph, &[true; 4]).is_empty());
    }

    #[test]
    fn test_open_blockers() {
        // c -> a, c -> b
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(c, a);
        graph.add_edge(c, b);

        // Nothing closed: c has 2 open blockers
        let closed_none = vec![false, false, false];
        let open = open_blockers(&graph, c, &closed_none);
        assert_eq!(open.len(), 2);
        assert!(open.contains(&a));
        assert!(open.contains(&b));

        // a closed: c has 1 open blocker (b)
        let closed_a = vec![true, false, false];
        let open = open_blockers(&graph, c, &closed_a);
        assert_eq!(open, vec![b]);

        // Both closed: c has 0 open blockers
        let closed_ab = vec![true, true, false];
        let open = open_blockers(&graph, c, &closed_ab);
        assert!(open.is_empty());
    }

    #[test]
    fn test_open_blocker_count() {
        // c -> a, c -> b
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(c, a);
        graph.add_edge(c, b);

        let closed_none = vec![false, false, false];
        assert_eq!(open_blocker_count(&graph, c, &closed_none), 2);

        let closed_a = vec![true, false, false];
        assert_eq!(open_blocker_count(&graph, c, &closed_a), 1);

        let closed_ab = vec![true, true, false];
        assert_eq!(open_blocker_count(&graph, c, &closed_ab), 0);
    }

    #[test]
    fn test_disconnected() {
        // a -> b, c (isolated)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);

        let from_a = reachable_from(&graph, a);
        assert_eq!(from_a.len(), 2);
        assert!(!from_a.contains(&c));

        let from_c = reachable_from(&graph, c);
        assert_eq!(from_c, vec![c]);
    }

    #[test]
    fn test_cycle() {
        // a -> b -> c -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        // All nodes reachable from any node
        let from_a = reachable_from(&graph, a);
        assert_eq!(from_a.len(), 3);

        let to_a = reachable_to(&graph, a);
        assert_eq!(to_a.len(), 3);

        // Generic traversal includes the cycle; scheduling has no open root.
        assert!(actionable_nodes(&graph, &[false; 3]).is_empty());
        assert_eq!(actionable_nodes(&graph, &[true, false, false]), vec![c]);
    }
}
