//! TopK Set algorithm: Greedy submodular selection for maximum unlock.
//!
//! Finds the k issues that, when completed, maximize the total downstream
//! unlocks. Uses greedy submodular optimization where each iteration picks
//! the node with maximum marginal gain.

use crate::graph::DiGraph;
use crate::reachability::is_actionable;
use crate::whatif::what_if_close;
use serde::Serialize;

/// An item in the TopK Set result.
#[derive(Debug, Clone, Serialize)]
pub struct TopKSetItem {
    /// Node index in the graph
    pub node: usize,
    /// Marginal gain from selecting this node (transitive unblocks)
    pub marginal_gain: usize,
    /// IDs of nodes that become actionable after this selection
    pub unblocked_ids: Vec<usize>,
}

/// Result of the TopK Set algorithm.
#[derive(Debug, Clone, Serialize)]
pub struct TopKSetResult {
    /// Selected items in order of selection
    pub items: Vec<TopKSetItem>,
    /// Total gain across all selections
    pub total_gain: usize,
    /// Number of eligible open (non-closed) candidate nodes considered
    pub open_nodes: usize,
}

/// Greedy submodular selection for maximum unlock.
///
/// Iteratively selects nodes that maximize transitive unblocks.
/// Each iteration simulates closing the best node and updates
/// the closed set for the next iteration.
///
/// # Arguments
/// * `graph` - The directed dependency graph
/// * `closed_set` - Boolean array where true means the node is closed/completed
/// * `k` - Maximum number of items to select
/// * `candidate_set` - Optional eligibility mask for direct selections. Missing
///   entries are ineligible; excluded nodes remain in the dependency graph and
///   are not treated as closed. None considers all open nodes.
///
/// # Returns
/// TopKSetResult with selected items sorted by selection order.
///
/// # Complexity
/// O(n * k * n) = O(n²k) where n is node count and k is selection limit.
/// Each iteration evaluates all open nodes using what_if_close.
pub fn topk_set(
    graph: &DiGraph,
    closed_set: &[bool],
    k: usize,
    candidate_set: Option<&[u8]>,
) -> TopKSetResult {
    let n = graph.len();
    if n == 0 || k == 0 {
        return TopKSetResult {
            items: Vec::new(),
            total_gain: 0,
            open_nodes: 0,
        };
    }

    // Initialize working closed set
    let mut current_closed = closed_set.to_vec();
    current_closed.resize(n, false);

    let eligible = |i| candidate_set.is_none_or(|set| set.get(i).is_some_and(|&b| b != 0));
    let open_nodes = (0..n)
        .filter(|&i| !current_closed[i] && eligible(i))
        .count();

    let mut selected = Vec::new();
    let mut total_gain = 0;

    for _ in 0..k {
        // Find node with maximum marginal gain among open nodes
        let mut best_node: Option<usize> = None;
        let mut best_gain = 0;
        let mut best_unblocked: Vec<usize> = Vec::new();

        // Exclude ineligible or still-blocked choices before simulation:
        // step 1 never recommends a node whose own prerequisites are still open,
        // while step s > 1 can chain sequential unlocks once prior steps close blockers.
        let mut candidates: Vec<usize> = (0..n)
            .filter(|&i| !current_closed[i] && eligible(i) && is_actionable(graph, i, &current_closed))
            .collect();
        // Sort for determinism when gains are equal
        candidates.sort_unstable();

        for node in candidates {
            let result = what_if_close(graph, node, &current_closed);
            let gain = result.transitive_unblocks;

            // Prefer higher gain, or lower node index for determinism
            if gain > best_gain || (gain == best_gain && best_node.is_none()) {
                best_gain = gain;
                best_node = Some(node);
                best_unblocked = result.cascade_ids;
            }
        }

        match best_node {
            Some(node) if best_gain > 0 => {
                selected.push(TopKSetItem {
                    node,
                    marginal_gain: best_gain,
                    unblocked_ids: best_unblocked.clone(),
                });
                // Mark the selected node as closed
                current_closed[node] = true;
                // Also mark all cascade nodes as closed (they'll be completed transitively)
                for &cascade_node in &best_unblocked {
                    if cascade_node < n {
                        current_closed[cascade_node] = true;
                    }
                }
                total_gain += best_gain;
            }
            _ => break, // No more beneficial nodes
        }
    }

    TopKSetResult {
        items: selected,
        total_gain,
        open_nodes,
    }
}

/// Greedy TopK Set with default limit of 5.
pub fn topk_set_default(graph: &DiGraph, closed_set: &[bool]) -> TopKSetResult {
    topk_set(graph, closed_set, 5, None)
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
        let result = topk_set(&g, &[], 5, None);
        assert!(result.items.is_empty());
        assert_eq!(result.total_gain, 0);
    }

    #[test]
    fn test_single_node() {
        let mut g = DiGraph::new();
        g.add_node("a");
        let result = topk_set(&g, &[false], 5, None);
        // Single node with no dependents has no gain
        assert!(result.items.is_empty());
        assert_eq!(result.total_gain, 0);
    }

    #[test]
    fn test_simple_chain() {
        // 0 <- 1 <- 2 <- 3 (dependent -> prerequisite)
        // Completing 0 unblocks 1,2,3 (gain=3)
        // Simulating completion of the cascade leaves no further gain.
        let g = make_graph(&[(1, 0), (2, 1), (3, 2)]);
        let result = topk_set(&g, &[false; 4], 5, None);

        assert_eq!(result.items.len(), 1);
        // First selection should be 0 (highest impact)
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].marginal_gain, 3);
        assert_eq!(result.items[0].unblocked_ids, vec![1, 2, 3]);
    }

    #[test]
    fn test_fork_pattern() {
        // A, B, C point to Hub (hub unblocks 3).
        let g = make_graph(&[(1, 0), (2, 0), (3, 0)]);
        let result = topk_set(&g, &[false; 4], 5, None);

        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].node, 0); // Hub
        assert_eq!(result.items[0].marginal_gain, 3);
        assert_eq!(result.total_gain, 3);
    }

    #[test]
    fn test_multiple_hubs() {
        // A, B depend on Hub1; X, Y, Z depend on Hub2.
        // Hub2 should be picked first (3 > 2)
        let g = make_graph(&[(1, 0), (2, 0), (4, 3), (5, 3), (6, 3)]);
        let result = topk_set(&g, &[false; 7], 5, None);

        assert_eq!(result.items.len(), 2);
        // Node 3 (Hub2) has more impact (3) than Node 0 (Hub1, 2)
        assert_eq!(result.items[0].node, 3);
        assert_eq!(result.items[0].marginal_gain, 3);
        assert_eq!(result.items[1].node, 0);
        assert_eq!(result.items[1].marginal_gain, 2);
        assert_eq!(result.total_gain, 5);
    }

    #[test]
    fn test_submodularity() {
        // Chain: 0 <- 1 <- 2 <- 3 <- 4
        // First selection: 0 (gain=4)
        // After that, no more nodes with positive gain (all unblocked)
        let g = make_graph(&[(1, 0), (2, 1), (3, 2), (4, 3)]);
        let result = topk_set(&g, &[false; 5], 5, None);

        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].marginal_gain, 4);
    }

    #[test]
    fn test_partially_closed() {
        // 2 -> 0, 2 -> 1, 3 -> 2
        // If 0 is already closed, closing 1 unblocks 2 (then 3)
        let g = make_graph(&[(2, 0), (2, 1), (3, 2)]);
        let closed = vec![true, false, false, false];
        let result = topk_set(&g, &closed, 5, None);

        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].node, 1);
        assert_eq!(result.items[0].marginal_gain, 2); // 2 and 3
    }

    #[test]
    fn test_limit_respected() {
        // Many independent chains
        let g = make_graph(&[(1, 0), (3, 2), (5, 4), (7, 6), (9, 8)]);
        let result = topk_set(&g, &[false; 10], 2, None);

        assert_eq!(result.items.len(), 2);
    }

    #[test]
    fn test_deterministic() {
        // Same gains - should pick by node index for determinism
        // 1 -> 0, 3 -> 2 (0 and 2 both have gain 1)
        let g = make_graph(&[(1, 0), (3, 2)]);

        // Run multiple times
        for _ in 0..5 {
            let result = topk_set(&g, &[false; 4], 5, None);
            // Should consistently pick 0 first (lower index)
            assert_eq!(result.items[0].node, 0);
        }
    }

    #[test]
    fn test_monotonic_marginal_gains() {
        // Submodularity: marginal gains should be non-increasing
        // 1, 2, 3 depend on 0; 4 and 5 depend on 1.
        let g = make_graph(&[(1, 0), (2, 0), (3, 0), (4, 1), (5, 1)]);
        let result = topk_set(&g, &[false; 6], 5, None);

        if result.items.len() >= 2 {
            for i in 1..result.items.len() {
                assert!(
                    result.items[i].marginal_gain <= result.items[i - 1].marginal_gain,
                    "Marginal gains should be monotonically non-increasing"
                );
            }
        }
    }

    #[test]
    fn test_open_nodes_count() {
        let g = make_graph(&[(1, 0), (2, 1)]);
        let closed = vec![true, false, false];
        let result = topk_set(&g, &closed, 5, None);

        // 1 node closed, 2 open
        assert_eq!(result.open_nodes, 2);
    }

    #[test]
    fn test_all_closed() {
        let g = make_graph(&[(1, 0), (2, 1)]);
        let closed = vec![true, true, true];
        let result = topk_set(&g, &closed, 5, None);

        assert!(result.items.is_empty());
        assert_eq!(result.open_nodes, 0);
    }

    #[test]
    fn test_deep_cascade() {
        // Deep chain: 0 <- 1 <- 2 <- 3 <- 4 <- 5 <- 6 <- 7 <- 8 <- 9
        let mut edges = Vec::new();
        for i in 0..9 {
            edges.push((i + 1, i));
        }
        let g = make_graph(&edges);
        let result = topk_set(&g, &[false; 10], 5, None);

        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].marginal_gain, 9);
    }

    #[test]
    fn excluded_prerequisite_is_not_selected_or_presumed_closed() {
        let g = make_graph(&[(1, 0), (2, 6), (3, 6), (4, 6), (5, 0), (5, 6)]);
        let closed = [false; 7];
        let mask = [1, 1, 1, 1, 1, 1, 0];
        let unrestricted = topk_set(&g, &closed, 5, None);
        assert_eq!(unrestricted.items[0].node, 6);
        assert_eq!(unrestricted.items[1].marginal_gain, 2);

        let result = topk_set(&g, &closed, 5, Some(&mask));
        assert_eq!(result.open_nodes, 6);
        assert_eq!(result.items.len(), 1);
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].unblocked_ids, vec![1]);
        assert_eq!(result.items[0].marginal_gain, 1);
        assert_eq!(result.total_gain, 1);
        assert_eq!(closed, [false; 7]);
        assert_eq!(mask, [1, 1, 1, 1, 1, 1, 0]);
    }

    #[test]
    fn candidate_mask_boundaries_and_ties() {
        let g = make_graph(&[(1, 0), (3, 2)]);
        for (mask, closed, k, want) in [
            (vec![], vec![], 5, vec![]),
            (vec![0; 4], vec![], 5, vec![]),
            (vec![1], vec![], 5, vec![0]),
            (vec![0, 0, 2], vec![], 5, vec![2]),
            (vec![1; 8], vec![], 5, vec![0, 2]),
            (vec![1; 4], vec![true], 5, vec![2]),
            (vec![1; 4], vec![], 1, vec![0]),
            (vec![1; 4], vec![], 0, vec![]),
        ] {
            let result = topk_set(&g, &closed, k, Some(&mask));
            let nodes: Vec<_> = result.items.iter().map(|i| i.node).collect();
            assert_eq!(nodes, want, "mask={mask:?} closed={closed:?} k={k}");
        }
        assert_eq!(
            serde_json::to_value(topk_set(&g, &[], 5, None)).unwrap(),
            serde_json::to_value(topk_set(&g, &[], 5, Some(&[1; 4]))).unwrap()
        );
    }

    #[test]
    fn candidate_mask_does_not_truncate_cascades() {
        let g = make_graph(&[(1, 0), (2, 1)]);
        let result = topk_set(&g, &[], 5, Some(&[1]));
        assert_eq!(result.open_nodes, 1);
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].unblocked_ids, vec![1, 2]);
        assert_eq!(result.total_gain, 2);
    }

    #[test]
    fn step_one_never_selects_blocked_node_and_step_two_chains_unlocks() {
        // Node 2 depends on BOTH 0 and 1. Nodes 3, 4, 5 depend on 2.
        // Node 6 depends only on 0.
        // At step 1: 2 has high fan-out (3,4,5) if closed directly, MUST NOT be selected
        // because 0 and 1 are still open! Only 0 and 1 are actionable:
        // - 0 directly unblocks 6 (gain 1)
        // - 1 directly unblocks nothing yet (gain 0)
        // So step 1 picks 0 (closing 0 and 6).
        // At step 2: 0 is now closed, so actionable node 1 now unblocks 2 -> 3,4,5 (gain 4)!
        let g = make_graph(&[(2, 0), (2, 1), (3, 2), (4, 2), (5, 2), (6, 0)]);
        let result = topk_set(&g, &[false; 7], 5, None);

        assert_eq!(result.items.len(), 2);
        assert_eq!(result.items[0].node, 0);
        assert_eq!(result.items[0].marginal_gain, 1);
        assert_eq!(result.items[1].node, 1);
        assert_eq!(result.items[1].marginal_gain, 4);
        assert_eq!(result.total_gain, 5);
    }

    #[test]
    fn blocked_intermediate_not_selected_when_prerequisite_excluded() {
        // 1 depends on excluded 0; 2, 3, 4 depend on 1.
        // Because 0 is open and excluded, 1 is not actionable and must never be recommended.
        let g = make_graph(&[(1, 0), (2, 1), (3, 1), (4, 1)]);
        let mask = [0, 1, 1, 1, 1];
        let result = topk_set(&g, &[false; 5], 5, Some(&mask));
        assert!(result.items.is_empty());
        assert_eq!(result.total_gain, 0);
        assert_eq!(result.open_nodes, 4);
    }
}
