//! Betweenness centrality algorithm.
//!
//! Measures how often a node lies on shortest paths between other nodes.
//! High betweenness = bottleneck that many paths flow through.
//!
//! Includes both exact (Brandes' O(V*E)) and approximate (sampling) algorithms.

use crate::graph::DiGraph;
use std::collections::VecDeque;

/// Compute exact betweenness centrality using Brandes' algorithm.
///
/// Complexity: O(V*E) for unweighted graphs.
///
/// # Returns
/// Vector of betweenness scores indexed by node index.
pub fn betweenness(graph: &DiGraph) -> Vec<f64> {
    let n = graph.len();
    if n == 0 {
        return Vec::new();
    }

    let mut bc = vec![0.0; n];

    // Run single-source betweenness from each node
    for s in 0..n {
        single_source_betweenness(graph, s, &mut bc);
    }

    bc
}

/// Compute approximate betweenness using k pivot samples.
///
/// Instead of computing shortest paths from ALL nodes (O(V*E)), we sample k pivot
/// nodes and extrapolate. This is Brandes' approximation algorithm.
///
/// Error bounds with k samples (O(1/sqrt(k))):
/// - k=50: ~14% error
/// - k=100: ~10% error
/// - k=200: ~7% error
///
/// For ranking purposes (which node is most central), this is usually sufficient.
///
/// # Arguments
/// * `graph` - The directed graph
/// * `sample_size` - Number of pivot nodes to sample
/// * `seed` - Optional seed for deterministic sampling (None for random)
pub fn betweenness_approx(graph: &DiGraph, sample_size: usize, seed: Option<u64>) -> Vec<f64> {
    let n = graph.len();
    if n == 0 || sample_size == 0 {
        return vec![0.0; n];
    }

    // For small graphs or when sample size >= node count, use exact algorithm
    if sample_size >= n {
        return betweenness(graph);
    }

    let mut bc = vec![0.0; n];

    // Sample k random pivot nodes
    let pivots = sample_nodes(n, sample_size, seed);

    // Compute partial betweenness from sampled pivots only
    for &pivot in &pivots {
        single_source_betweenness(graph, pivot, &mut bc);
    }

    // Scale up: BC_approx = BC_partial * (n / k)
    // This extrapolates from the sample to the full graph
    let scale = n as f64 / sample_size as f64;
    for score in &mut bc {
        *score *= scale;
    }

    bc
}

/// Single-source betweenness contribution (Brandes' algorithm).
///
/// The algorithm performs BFS from the source and accumulates dependency scores
/// in a reverse topological order traversal.
fn single_source_betweenness(graph: &DiGraph, source: usize, bc: &mut [f64]) {
    let n = graph.len();

    // BFS data structures
    let mut stack: Vec<usize> = Vec::with_capacity(n);
    let mut pred: Vec<Vec<usize>> = vec![Vec::new(); n];
    let mut sigma = vec![0.0f64; n]; // Number of shortest paths through node
    let mut dist = vec![-1i32; n]; // Distance from source (-1 = unreachable)
    let mut delta = vec![0.0f64; n]; // Dependency of source on node

    // Initialize source
    sigma[source] = 1.0;
    dist[source] = 0;

    // BFS phase
    let mut queue = VecDeque::new();
    queue.push_back(source);

    while let Some(v) = queue.pop_front() {
        stack.push(v);

        for &w in graph.successors_slice(v) {
            // Path discovery: first visit to w
            if dist[w] < 0 {
                dist[w] = dist[v] + 1;
                queue.push_back(w);
            }

            // Path counting: is v on a shortest path to w?
            if dist[w] == dist[v] + 1 {
                sigma[w] += sigma[v];
                pred[w].push(v);
            }
        }
    }

    // Accumulation phase (reverse topological order)
    while let Some(w) = stack.pop() {
        for &v in &pred[w] {
            if sigma[w] > 0.0 {
                delta[v] += (sigma[v] / sigma[w]) * (1.0 + delta[w]);
            }
        }

        if w != source {
            bc[w] += delta[w];
        }
    }
}

/// Sample k unique indices from 0..n using Fisher-Yates shuffle.
fn sample_nodes(n: usize, k: usize, seed: Option<u64>) -> Vec<usize> {
    let mut indices: Vec<usize> = (0..n).collect();

    // Use getrandom for better randomness in WASM, or seed for testing
    let mut rng_state = match seed {
        Some(s) => s,
        None => {
            let mut buf = [0u8; 8];
            // getrandom works in WASM with the wasm_js feature
            let _ = getrandom::fill(&mut buf);
            u64::from_le_bytes(buf)
        }
    };

    // LCG for shuffling (simple but sufficient for sampling)
    let lcg = |state: &mut u64| -> usize {
        *state = state.wrapping_mul(6364136223846793005).wrapping_add(1);
        (*state >> 33) as usize
    };

    // Fisher-Yates shuffle for first k elements
    let k = k.min(n);
    for i in 0..k {
        let j = i + lcg(&mut rng_state) % (n - i);
        indices.swap(i, j);
    }

    indices.truncate(k);
    indices
}

/// Recommend sample size based on graph characteristics.
///
/// Balances accuracy vs. speed.
pub fn recommend_sample_size(node_count: usize) -> usize {
    match node_count {
        0..=99 => node_count,                // Small: use exact algorithm
        100..=499 => 50.max(node_count / 5), // Medium: 20% sample
        500..=1999 => 100,                   // Large: fixed sample for ~10% error
        _ => 200,                            // XL: larger fixed sample
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_betweenness_empty() {
        let graph = DiGraph::new();
        let bc = betweenness(&graph);
        assert!(bc.is_empty());
    }

    #[test]
    fn test_betweenness_single_node() {
        let mut graph = DiGraph::new();
        graph.add_node("a");
        let bc = betweenness(&graph);
        assert_eq!(bc.len(), 1);
        assert_eq!(bc[0], 0.0); // No paths through a single node
    }

    #[test]
    fn test_betweenness_two_nodes() {
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);

        let bc = betweenness(&graph);
        assert_eq!(bc.len(), 2);
        // Neither node is on a path between other nodes
        assert_eq!(bc[a], 0.0);
        assert_eq!(bc[b], 0.0);
    }

    #[test]
    fn test_betweenness_line() {
        // a -> b -> c
        // b is on the path a -> c
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let bc = betweenness(&graph);
        // b is on the path a->c, so betweenness(b) = 1
        assert!(bc[b] > bc[a], "b should have higher betweenness than a");
        assert!(bc[b] > bc[c], "b should have higher betweenness than c");
        assert_eq!(bc[a], 0.0);
        assert_eq!(bc[c], 0.0);
        assert!((bc[b] - 1.0).abs() < 0.001);
    }

    #[test]
    fn test_betweenness_diamond() {
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

        let bc = betweenness(&graph);
        // Two equal-length paths a->d, so b and c share the betweenness
        // Each is on half the shortest paths
        assert!(
            (bc[b] - bc[c]).abs() < 0.001,
            "b and c should have equal betweenness"
        );
        assert!(bc[b] > 0.0, "b should have positive betweenness");
    }

    #[test]
    fn test_betweenness_star_inward() {
        // Inward star: s1->hub, s2->hub, s3->hub, hub->out
        // Hub is on all paths from s1/s2/s3 to out
        let mut graph = DiGraph::new();
        let hub = graph.add_node("hub");
        let s1 = graph.add_node("s1");
        let s2 = graph.add_node("s2");
        let s3 = graph.add_node("s3");
        let out = graph.add_node("out");
        graph.add_edge(s1, hub);
        graph.add_edge(s2, hub);
        graph.add_edge(s3, hub);
        graph.add_edge(hub, out);

        let bc = betweenness(&graph);
        // Hub is on 3 paths (s1->out, s2->out, s3->out)
        assert!(bc[hub] > bc[s1]);
        assert!(bc[hub] > bc[s2]);
        assert!(bc[hub] > bc[s3]);
        assert!(bc[hub] > bc[out]);
    }

    #[test]
    fn test_betweenness_cycle() {
        // a -> b -> c -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let bc = betweenness(&graph);
        // In a cycle, each node is on some paths
        // All should have equal betweenness due to symmetry
        let diff = (bc[a] - bc[b]).abs() + (bc[b] - bc[c]).abs();
        assert!(diff < 0.01, "Cycle nodes should have similar betweenness");
    }

    #[test]
    fn test_betweenness_approx_fallback() {
        // When sample_size >= n, should fall back to exact
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let exact = betweenness(&graph);
        let approx = betweenness_approx(&graph, 10, Some(42)); // sample > node count

        assert_eq!(exact.len(), approx.len());
        for (e, a) in exact.iter().zip(approx.iter()) {
            assert!((e - a).abs() < 0.001);
        }
    }

    #[test]
    fn test_betweenness_approx_deterministic() {
        // Same seed should give same results
        let mut graph = DiGraph::new();
        for i in 0..20 {
            graph.add_node(&format!("n{}", i));
        }
        for i in 0..19 {
            graph.add_edge(i, i + 1);
        }

        let bc1 = betweenness_approx(&graph, 5, Some(12345));
        let bc2 = betweenness_approx(&graph, 5, Some(12345));

        for (a, b) in bc1.iter().zip(bc2.iter()) {
            assert_eq!(a, b, "Same seed should give same results");
        }
    }

    #[test]
    fn test_betweenness_chain() {
        // a -> b -> c -> d -> e
        // Middle nodes should have highest betweenness
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        let e = graph.add_node("e");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, d);
        graph.add_edge(d, e);

        let bc = betweenness(&graph);

        // c should have highest (on paths a->d, a->e, b->d, b->e = 4 paths)
        // b should be next (on paths a->c, a->d, a->e = 3 paths)
        // d should be next (on paths a->e, b->e, c->e = 3 paths)
        assert!(bc[c] >= bc[b]);
        assert!(bc[c] >= bc[d]);
        assert_eq!(bc[a], 0.0);
        assert_eq!(bc[e], 0.0);
    }

    #[test]
    fn test_recommend_sample_size() {
        assert_eq!(recommend_sample_size(50), 50); // Small: exact
        assert_eq!(recommend_sample_size(200), 50); // Medium: 20% but min 50
        assert_eq!(recommend_sample_size(500), 100); // Large: fixed
        assert_eq!(recommend_sample_size(5000), 200); // XL: larger fixed
    }

    #[test]
    fn test_betweenness_approx_zero_samples_returns_zeros() {
        let empty = DiGraph::new();
        assert!(betweenness_approx(&empty, 0, Some(1)).is_empty());

        let mut graph = DiGraph::new();
        for i in 0..5 {
            graph.add_node(&format!("n{}", i));
        }
        for i in 0..4 {
            graph.add_edge(i, i + 1);
        }

        // Zero samples must return finite 0.0 scores rather than 0.0 * (n / 0.0) == NaN.
        let zero_scores = betweenness_approx(&graph, 0, Some(1));
        assert_eq!(zero_scores.len(), 5);
        for &score in &zero_scores {
            assert!(score.is_finite(), "expected finite score, got {}", score);
            assert_eq!(score, 0.0);
        }

        // Positive control: positive sample size yields positive finite interior scores.
        let pos_scores = betweenness_approx(&graph, 3, Some(1));
        assert!(pos_scores.iter().all(|s| s.is_finite()));
        assert!(pos_scores.iter().any(|&s| s > 0.0));
    }
}
