//! Parallel Cut analysis algorithm.
//!
//! Identifies nodes whose completion would increase opportunities for
//! parallel work by unblocking multiple dependents.

use crate::graph::DiGraph;
use crate::reachability::is_actionable;
use serde::Serialize;
use std::collections::HashSet;

/// A node that could increase parallelization when completed.
#[derive(Debug, Clone, Serialize)]
pub struct ParallelCutItem {
    /// Node index in the graph
    pub node: usize,
    /// Net gain in parallelization (dependents newly unblocked - 1)
    pub parallel_gain: i32,
    /// Number of dependents that would become actionable
    pub new_actionable: usize,
}

/// Result of parallel cut analysis.
#[derive(Debug, Clone, Serialize)]
pub struct ParallelCutResult {
    /// Nodes sorted by parallel gain descending
    pub items: Vec<ParallelCutItem>,
    /// Total open (non-closed) nodes considered
    pub open_nodes: usize,
    /// Current number of actionable nodes (before any changes)
    pub current_actionable: usize,
}

/// Find nodes that would increase parallelization when completed.
///
/// A node has positive parallel gain if completing it would unblock
/// more than one dependent (hence increasing parallel work opportunities).
///
/// # Arguments
/// * `graph` - The directed graph
/// * `closed_set` - Boolean array where true means the node is closed/completed
/// * `limit` - Maximum number of suggestions to return
///
/// # Returns
/// ParallelCutResult with nodes sorted by parallel gain.
pub fn parallel_cut_suggestions(
    graph: &DiGraph,
    closed_set: &[bool],
    limit: usize,
) -> ParallelCutResult {
    parallel_cut_filtered(graph, closed_set, limit, None)
}

/// Find eligible, currently actionable nodes that would increase parallelization
/// when completed.
///
/// Only currently actionable nodes (`is_actionable(graph, v, closed_set)`) that
/// also satisfy `candidate_set` (when provided) can be suggested or counted in
/// `current_actionable`.
pub fn parallel_cut_filtered(
    graph: &DiGraph,
    closed_set: &[bool],
    limit: usize,
    candidate_set: Option<&HashSet<usize>>,
) -> ParallelCutResult {
    let n = graph.node_count();
    let is_eligible = |v: usize| candidate_set.is_none_or(|s| s.contains(&v));

    let is_open_eligible =
        |v: usize| !closed_set.get(v).copied().unwrap_or(false) && is_eligible(v);

    // Count current actionable eligible nodes
    let current_actionable = (0..n)
        .filter(|&v| is_open_eligible(v) && is_actionable(graph, v, closed_set))
        .count();

    // Count eligible open nodes
    let open_nodes = (0..n).filter(|&v| is_open_eligible(v)).count();

    // Calculate parallel gain ONLY for currently actionable eligible open nodes
    let mut suggestions: Vec<ParallelCutItem> = (0..n)
        .filter(|&v| is_open_eligible(v) && is_actionable(graph, v, closed_set))
        .map(|v| {
            // Count how many dependents would become actionable if v is closed
            let new_actionable = graph
                .predecessors_slice(v)
                .iter()
                .filter(|&&w| {
                    w != v
                        // w must be open
                        && !closed_set.get(w).copied().unwrap_or(false)
                        // All of w's other prerequisites must be closed
                        && graph
                            .successors_slice(w)
                            .iter()
                            .filter(|&&p| p != v)
                            .all(|&p| closed_set.get(p).copied().unwrap_or(false))
                })
                .count();

            // Parallel gain = newly actionable - 1 (the node itself becomes unavailable)
            // Positive means net increase in parallel work
            ParallelCutItem {
                node: v,
                parallel_gain: new_actionable as i32 - 1,
                new_actionable,
            }
        })
        .filter(|item| item.parallel_gain > 0)
        .collect();

    // Sort by parallel gain descending, then by node ID for determinism
    suggestions.sort_by(|a, b| {
        b.parallel_gain
            .cmp(&a.parallel_gain)
            .then_with(|| a.node.cmp(&b.node))
    });
    suggestions.truncate(limit);

    ParallelCutResult {
        items: suggestions,
        open_nodes,
        current_actionable,
    }
}

/// Find parallel cut suggestions with default limit of 10.
pub fn parallel_cut_default(graph: &DiGraph, closed_set: &[bool]) -> ParallelCutResult {
    parallel_cut_suggestions(graph, closed_set, 10)
}

/// Get nodes sorted by how many dependents they unblock.
/// Unlike parallel_cut_suggestions, this includes nodes with gain <= 0.
pub fn unblock_ranking(graph: &DiGraph, closed_set: &[bool], limit: usize) -> Vec<(usize, usize)> {
    let n = graph.node_count();

    let mut ranking: Vec<(usize, usize)> = (0..n)
        .filter(|&v| !closed_set.get(v).copied().unwrap_or(false))
        .map(|v| {
            let unblocks = graph
                .predecessors_slice(v)
                .iter()
                .filter(|&&w| {
                    !closed_set.get(w).copied().unwrap_or(false)
                        && graph
                            .successors_slice(w)
                            .iter()
                            .filter(|&&p| p != v)
                            .all(|&p| closed_set.get(p).copied().unwrap_or(false))
                })
                .count();
            (v, unblocks)
        })
        .collect();

    // Sort by unblocks descending, then by node ID for determinism
    ranking.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    ranking.truncate(limit);
    ranking
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
        let closed: Vec<bool> = vec![];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        assert!(result.items.is_empty());
        assert_eq!(result.open_nodes, 0);
    }

    #[test]
    fn test_single_node() {
        let mut g = DiGraph::new();
        g.add_node("a");
        let closed = vec![false];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        // Single node has no dependents, so no parallel gain
        assert!(result.items.is_empty());
        assert_eq!(result.open_nodes, 1);
    }

    #[test]
    fn test_chain_no_gain() {
        // Chain: 0 <- 1 <- 2 <- 3 (dependent -> prerequisite)
        // Completing any node only unblocks 1 dependent (gain = 0)
        let g = make_graph(&[(1, 0), (2, 1), (3, 2)]);
        let closed = vec![false; 4];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        // No node has gain > 0 in a simple chain
        assert!(result.items.is_empty());
        assert_eq!(result.current_actionable, 1);
        assert_eq!(
            unblock_ranking(&g, &closed, 4),
            vec![(0, 1), (1, 1), (2, 1), (3, 0)]
        );
    }

    #[test]
    fn test_fork_has_gain() {
        // Fork: 1 -> 0, 2 -> 0, 3 -> 0
        // Completing 0 unblocks 3 nodes, gain = 3 - 1 = 2
        let g = make_graph(&[(1, 0), (2, 0), (3, 0)]);
        let closed = vec![false; 4];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].parallel_gain, 2); // 3 unblocked - 1 = 2
        assert_eq!(result.items[0].new_actionable, 3);
    }

    #[test]
    fn test_partially_closed() {
        // Diamond: 1 -> 0, 2 -> 0, 3 -> 1, 3 -> 2
        // If 0 is closed, then 1 and 2 are actionable
        // Completing either 1 or 2 alone unblocks zero nodes.
        let g = make_graph(&[(1, 0), (2, 0), (3, 1), (3, 2)]);
        let closed = vec![true, false, false, false];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        // Neither 1 nor 2 alone unblocks 3 (needs both)
        // So no positive gain
        assert!(result.items.is_empty());
        assert_eq!(result.current_actionable, 2);
        assert_eq!(
            unblock_ranking(&g, &closed, 4),
            vec![(1, 0), (2, 0), (3, 0)]
        );
    }

    #[test]
    fn test_diamond_with_one_closed() {
        // Diamond: 1 -> 0, 2 -> 0, 3 -> 1, 3 -> 2
        // If 0 and 1 are closed, completing 2 unblocks 3
        let g = make_graph(&[(1, 0), (2, 0), (3, 1), (3, 2)]);
        let closed = vec![true, true, false, false];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        // Node 2 is actionable and completing it unblocks 3
        // But gain = 1 - 1 = 0 (not positive)
        assert!(result.items.is_empty());
        assert_eq!(unblock_ranking(&g, &closed, 4), vec![(2, 1), (3, 0)]);
    }

    #[test]
    fn test_multiple_forks() {
        // Two forks: {1,2} depend on 0; {4,5,6} depend on 3.
        // Node 0 has gain = 2 - 1 = 1
        // Node 3 has gain = 3 - 1 = 2
        let g = make_graph(&[(1, 0), (2, 0), (4, 3), (5, 3), (6, 3)]);
        let closed = vec![false; 7];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        assert!(!result.items.is_empty());
        // Node 3 should be first (higher gain)
        assert_eq!(result.items[0].node, 3);
        assert_eq!(result.items[0].parallel_gain, 2);
    }

    #[test]
    fn test_limit_respected() {
        // Many forks
        let g = make_graph(&[
            (1, 0),
            (2, 0),
            (4, 3),
            (5, 3),
            (7, 6),
            (8, 6),
            (10, 9),
            (11, 9),
        ]);
        let closed = vec![false; 12];
        let result = parallel_cut_suggestions(&g, &closed, 2);

        assert_eq!(result.items.len(), 2);
    }

    #[test]
    fn test_current_actionable_count() {
        // Fork: 1 -> 0, 2 -> 0
        // Initially only 0 is actionable
        let g = make_graph(&[(1, 0), (2, 0)]);
        let closed = vec![false; 3];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        assert_eq!(result.current_actionable, 1); // Only node 0
        assert_eq!(result.open_nodes, 3);
    }

    #[test]
    fn test_blocked_fanout_node_never_suggested() {
        // A (0) <- B (1) <- {C (2), D (3), E (4)}
        // Node 1 has 3 direct dependents, so if closed in isolation it would unlock 3,
        // BUT node 1 is blocked by open prerequisite node 0!
        // parallel_cut_suggestions MUST NOT suggest blocked node 1.
        let g = make_graph(&[(1, 0), (2, 1), (3, 1), (4, 1)]);
        let closed = vec![false; 5];
        let result = parallel_cut_suggestions(&g, &closed, 10);

        assert!(result.items.is_empty());
        assert_eq!(result.current_actionable, 1);

        // Once prerequisite 0 is closed, node 1 becomes actionable and IS suggested.
        let closed_after_0 = vec![true, false, false, false, false];
        let result_after = parallel_cut_suggestions(&g, &closed_after_0, 10);
        assert_eq!(result_after.items.len(), 1);
        assert_eq!(result_after.items[0].node, 1);
        assert_eq!(result_after.items[0].parallel_gain, 2);
        assert_eq!(result_after.items[0].new_actionable, 3);
    }

    #[test]
    fn test_parallel_cut_filtered_respects_candidate_set() {
        // Two actionable hubs:
        // - Node 0 (e.g. deferred/template/ephemeral, excluded from candidate_set) unlocks {1, 2, 3}
        // - Node 4 (eligible actionable) unlocks {5, 6}
        let g = make_graph(&[(1, 0), (2, 0), (3, 0), (5, 4), (6, 4)]);
        let closed = vec![false; 7];
        let candidates: HashSet<usize> = [4usize].into_iter().collect();

        let filtered = parallel_cut_filtered(&g, &closed, 10, Some(&candidates));
        assert_eq!(filtered.current_actionable, 1);
        assert_eq!(filtered.open_nodes, 1);
        assert_eq!(filtered.items.len(), 1);
        assert_eq!(filtered.items[0].node, 4);
        assert_eq!(filtered.items[0].parallel_gain, 1);
        assert_eq!(filtered.items[0].new_actionable, 2);

        // Negative control: unfiltered sees both hubs with node 0 ranked first.
        let unfiltered = parallel_cut_suggestions(&g, &closed, 10);
        assert_eq!(unfiltered.current_actionable, 2);
        assert_eq!(
            unfiltered.items.iter().map(|i| i.node).collect::<Vec<_>>(),
            vec![0, 4]
        );
    }
}
