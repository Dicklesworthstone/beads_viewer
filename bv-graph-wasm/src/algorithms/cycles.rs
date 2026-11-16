//! Cycle Detection algorithms.
//!
//! Provides:
//! - Tarjan's SCC algorithm for fast cycle presence check
//! - Johnson's algorithm for full cycle enumeration

use crate::graph::DiGraph;
use serde::Serialize;
use std::collections::HashSet;

/// Result of Strongly Connected Components analysis.
#[derive(Serialize, Clone)]
pub struct SCCResult {
    /// List of strongly connected components (each is a list of node indices)
    pub components: Vec<Vec<usize>>,
    /// True if any SCC has a cycle (size > 1 or 1-node self-loop)
    pub has_cycles: bool,
    /// Number of cyclic SCCs (size > 1 or 1-node self-loop)
    pub cycle_count: usize,
}

#[inline]
fn scc_has_cycle(graph: &DiGraph, comp: &[usize]) -> bool {
    comp.len() > 1 || (comp.len() == 1 && graph.successors_slice(comp[0]).contains(&comp[0]))
}

/// Tarjan's algorithm for finding strongly connected components.
///
/// An SCC with more than one node, or a single node with a self-loop, indicates a cycle.
/// Complexity: O(V + E)
pub fn tarjan_scc(graph: &DiGraph) -> SCCResult {
    let n = graph.len();
    if n == 0 {
        return SCCResult {
            components: Vec::new(),
            has_cycles: false,
            cycle_count: 0,
        };
    }

    let mut state = TarjanState {
        graph,
        index: 0usize,
        indices: vec![usize::MAX; n],
        lowlink: vec![usize::MAX; n],
        on_stack: vec![false; n],
        stack: Vec::new(),
        components: Vec::new(),
    };

    for v in 0..n {
        if state.indices[v] == usize::MAX {
            state.strongconnect(v);
        }
    }

    let cycle_count = state
        .components
        .iter()
        .filter(|c| scc_has_cycle(graph, c))
        .count();

    SCCResult {
        components: state.components,
        has_cycles: cycle_count > 0,
        cycle_count,
    }
}

struct TarjanState<'a> {
    graph: &'a DiGraph,
    index: usize,
    indices: Vec<usize>,
    lowlink: Vec<usize>,
    on_stack: Vec<bool>,
    stack: Vec<usize>,
    components: Vec<Vec<usize>>,
}

impl TarjanState<'_> {
    fn strongconnect(&mut self, v: usize) {
        self.indices[v] = self.index;
        self.lowlink[v] = self.index;
        self.index += 1;
        self.stack.push(v);
        self.on_stack[v] = true;

        for &w in self.graph.successors_slice(v) {
            if self.indices[w] == usize::MAX {
                // Not visited
                self.strongconnect(w);
                self.lowlink[v] = self.lowlink[v].min(self.lowlink[w]);
            } else if self.on_stack[w] {
                // On stack = in current SCC
                self.lowlink[v] = self.lowlink[v].min(self.indices[w]);
            }
        }

        // If v is a root node, pop the stack to get SCC
        if self.lowlink[v] == self.indices[v] {
            let mut component = Vec::new();
            loop {
                let w = self.stack.pop().unwrap();
                self.on_stack[w] = false;
                component.push(w);
                if w == v {
                    break;
                }
            }
            self.components.push(component);
        }
    }
}

/// Check if graph has any cycles.
pub fn has_cycles(graph: &DiGraph) -> bool {
    tarjan_scc(graph).has_cycles
}

/// Enumerate elementary cycles using Johnson's algorithm.
///
/// Reference: Donald B. Johnson, "Finding All the Elementary Circuits of a Directed Graph"
/// SIAM J. Computing, Vol. 4, No. 1, March 1975
///
/// # Arguments
/// * `graph` - The directed graph
/// * `max_cycles` - Maximum number of cycles to find (prevents exponential blowup)
///
/// # Returns
/// Vector of cycles, each cycle is a vector of node indices in order
pub fn enumerate_cycles(graph: &DiGraph, max_cycles: usize) -> Vec<Vec<usize>> {
    let n = graph.len();
    if n == 0 || max_cycles == 0 {
        return Vec::new();
    }

    let mut cycles: Vec<Vec<usize>> = Vec::new();
    let mut blocked = vec![false; n];
    let mut blocked_map: Vec<HashSet<usize>> = vec![HashSet::new(); n];
    let mut stack: Vec<usize> = Vec::new();

    // Helper: unblock a node and recursively unblock dependents
    fn unblock(u: usize, blocked: &mut [bool], blocked_map: &mut [HashSet<usize>]) {
        blocked[u] = false;
        let dependents: Vec<usize> = blocked_map[u].drain().collect();
        for w in dependents {
            if blocked[w] {
                unblock(w, blocked, blocked_map);
            }
        }
    }

    // Run Johnson's algorithm starting from each node
    for start in 0..n {
        if cycles.len() >= max_cycles {
            break;
        }

        // Reset blocked state
        blocked.fill(false);
        for s in &mut blocked_map {
            s.clear();
        }

        let mut state = CircuitState {
            graph,
            blocked: &mut blocked,
            blocked_map: &mut blocked_map,
            stack: &mut stack,
            cycles: &mut cycles,
            max_cycles,
            min_node: start,
        };
        state.circuit(start, start, &mut |u, blocked, blocked_map| {
            unblock(u, blocked, blocked_map)
        });
    }

    cycles
}

struct CircuitState<'a> {
    graph: &'a DiGraph,
    blocked: &'a mut [bool],
    blocked_map: &'a mut [HashSet<usize>],
    stack: &'a mut Vec<usize>,
    cycles: &'a mut Vec<Vec<usize>>,
    max_cycles: usize,
    min_node: usize,
}

impl CircuitState<'_> {
    // Circuit search from start vertex.
    fn circuit(
        &mut self,
        v: usize,
        start: usize,
        unblock: &mut impl FnMut(usize, &mut [bool], &mut [HashSet<usize>]),
    ) -> bool {
        if self.cycles.len() >= self.max_cycles {
            return false;
        }

        let mut found = false;
        self.stack.push(v);
        self.blocked[v] = true;

        for &w in self.graph.successors_slice(v) {
            // Only consider nodes >= min_node (Johnson's optimization)
            if w < self.min_node {
                continue;
            }

            if w == start {
                // Found a cycle
                self.cycles.push(self.stack.clone());
                found = true;
                if self.cycles.len() >= self.max_cycles {
                    self.stack.pop();
                    return found;
                }
            } else if !self.blocked[w] && self.circuit(w, start, unblock) {
                found = true;
            }
        }

        if found {
            unblock(v, self.blocked, self.blocked_map);
        } else {
            for &w in self.graph.successors_slice(v) {
                if w >= self.min_node {
                    self.blocked_map[w].insert(v);
                }
            }
        }

        self.stack.pop();
        found
    }
}

/// Result of cycle enumeration with metadata.
#[derive(Serialize)]
pub struct CycleEnumerationResult {
    /// List of cycles found
    pub cycles: Vec<Vec<usize>>,
    /// Whether max_cycles limit was reached
    pub truncated: bool,
    /// Number of cycles found
    pub count: usize,
}

/// Enumerate cycles with metadata about truncation.
pub fn enumerate_cycles_with_info(graph: &DiGraph, max_cycles: usize) -> CycleEnumerationResult {
    if max_cycles == 0 {
        return CycleEnumerationResult {
            cycles: Vec::new(),
            truncated: has_cycles(graph),
            count: 0,
        };
    }

    let probe_limit = max_cycles.saturating_add(1);
    let mut cycles = enumerate_cycles(graph, probe_limit);
    let truncated = cycles.len() > max_cycles;
    if truncated {
        cycles.truncate(max_cycles);
    }
    let count = cycles.len();
    CycleEnumerationResult {
        cycles,
        truncated,
        count,
    }
}

// ============================================================================
// Cycle Break Suggestions
// ============================================================================

/// A suggestion for which edge to remove to break cycles.
#[derive(Debug, Clone, Serialize)]
pub struct CycleBreakItem {
    /// Source node of the edge
    pub from: usize,
    /// Target node of the edge
    pub to: usize,
    /// Number of cycles this edge appears in
    pub cycles_broken: usize,
    /// Collateral damage score (sum of degree changes)
    pub collateral: usize,
    /// Node IDs for display
    pub from_id: Option<String>,
    /// Node ID for target
    pub to_id: Option<String>,
}

/// Result of cycle break analysis.
#[derive(Debug, Clone, Serialize)]
pub struct CycleBreakResult {
    /// Suggested edges to remove
    pub suggestions: Vec<CycleBreakItem>,
    /// Total cycles in the graph
    pub total_cycles: usize,
    /// Whether cycle enumeration was truncated
    pub truncated: bool,
}

/// Analyze cycles and suggest edges to remove to break them.
///
/// For each edge within an SCC (cycle-containing component), calculates:
/// - How many cycles it participates in
/// - The collateral damage (in-degree + out-degree of incident nodes)
///
/// Suggestions are sorted by: cycles_broken desc, then collateral asc
/// (prefer edges that break many cycles with minimal disruption)
///
/// # Arguments
/// * `graph` - The directed graph
/// * `limit` - Maximum suggestions to return
/// * `max_cycles_to_enumerate` - Max cycles to enumerate for scoring (default 100)
pub fn cycle_break_suggestions(
    graph: &DiGraph,
    limit: usize,
    max_cycles_to_enumerate: usize,
) -> CycleBreakResult {
    let scc = tarjan_scc(graph);
    if !scc.has_cycles {
        return CycleBreakResult {
            suggestions: Vec::new(),
            total_cycles: 0,
            truncated: false,
        };
    }

    // Enumerate actual cycles to count edge participation
    let cycle_info = enumerate_cycles_with_info(graph, max_cycles_to_enumerate);
    let cycles = &cycle_info.cycles;

    // Build a map of edge -> cycles it appears in
    let mut edge_cycle_count: std::collections::HashMap<(usize, usize), usize> =
        std::collections::HashMap::new();

    for cycle in cycles {
        if cycle.is_empty() {
            continue;
        }
        // Count edges in this cycle (including 1-node self-loops u -> u)
        for i in 0..cycle.len() {
            let from = cycle[i];
            let to = cycle[(i + 1) % cycle.len()];
            *edge_cycle_count.entry((from, to)).or_insert(0) += 1;
        }
    }

    // Find all intra-SCC edges within cyclic SCCs
    let mut suggestions: Vec<CycleBreakItem> = Vec::new();

    for comp in scc.components.iter().filter(|c| scc_has_cycle(graph, c)) {
        let comp_nodes: HashSet<usize> = comp.iter().copied().collect();
        for &from in comp {
            for &to in graph.successors_slice(from) {
                if comp_nodes.contains(&to) {
                    let cycles_broken = edge_cycle_count.get(&(from, to)).copied().unwrap_or(0);
                    let collateral =
                        graph.successors_slice(from).len() + graph.predecessors_slice(to).len();

                    suggestions.push(CycleBreakItem {
                        from,
                        to,
                        cycles_broken,
                        collateral,
                        from_id: graph.node_id(from),
                        to_id: graph.node_id(to),
                    });
                }
            }
        }
    }

    // Sort by: cycles_broken desc, collateral asc
    suggestions.sort_by(|a, b| match b.cycles_broken.cmp(&a.cycles_broken) {
        std::cmp::Ordering::Equal => a.collateral.cmp(&b.collateral),
        other => other,
    });

    suggestions.truncate(limit);

    CycleBreakResult {
        suggestions,
        total_cycles: cycle_info.count,
        truncated: cycle_info.truncated,
    }
}

/// Quick check for edges that could break cycles.
///
/// A simplified version that only looks at SCC membership without
/// full cycle enumeration. Faster but less precise scoring.
pub fn quick_cycle_break_edges(graph: &DiGraph, limit: usize) -> Vec<CycleBreakItem> {
    let scc = tarjan_scc(graph);
    if !scc.has_cycles {
        return Vec::new();
    }

    let mut suggestions: Vec<CycleBreakItem> = Vec::new();

    for comp in scc.components.iter().filter(|c| scc_has_cycle(graph, c)) {
        let comp_nodes: HashSet<usize> = comp.iter().copied().collect();
        for &from in comp {
            for &to in graph.successors_slice(from) {
                if comp_nodes.contains(&to) {
                    // Heuristic: edges with low total degree are better to remove
                    let collateral =
                        graph.successors_slice(from).len() + graph.predecessors_slice(to).len();

                    suggestions.push(CycleBreakItem {
                        from,
                        to,
                        cycles_broken: 1, // Unknown without enumeration
                        collateral,
                        from_id: graph.node_id(from),
                        to_id: graph.node_id(to),
                    });
                }
            }
        }
    }

    // Sort by collateral (prefer low-impact edges)
    suggestions.sort_by_key(|s| s.collateral);
    suggestions.truncate(limit);
    suggestions
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_scc_empty() {
        let graph = DiGraph::new();
        let result = tarjan_scc(&graph);
        assert!(result.components.is_empty());
        assert!(!result.has_cycles);
    }

    #[test]
    fn test_scc_single_node() {
        let mut graph = DiGraph::new();
        graph.add_node("a");
        let result = tarjan_scc(&graph);
        assert_eq!(result.components.len(), 1);
        assert_eq!(result.components[0].len(), 1);
        assert!(!result.has_cycles);
    }

    #[test]
    fn test_scc_self_loop() {
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        graph.add_edge(a, a);
        let result = tarjan_scc(&graph);
        assert!(result.has_cycles);
        assert_eq!(result.cycle_count, 1);
        assert_eq!(result.components, vec![vec![a]]);
    }

    #[test]
    fn test_scc_simple_cycle() {
        // a -> b -> c -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let result = tarjan_scc(&graph);
        assert!(result.has_cycles);
        assert_eq!(result.cycle_count, 1);
        // One SCC with all 3 nodes
        let big_scc = result.components.iter().find(|c| c.len() > 1);
        assert!(big_scc.is_some());
        assert_eq!(big_scc.unwrap().len(), 3);
    }

    #[test]
    fn test_scc_dag() {
        // a -> b -> c (no cycles)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let result = tarjan_scc(&graph);
        assert!(!result.has_cycles);
        // Each node is its own SCC
        assert_eq!(result.components.len(), 3);
    }

    #[test]
    fn test_scc_two_cycles() {
        // Cycle 1: a -> b -> a
        // Cycle 2: c -> d -> c
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, a);
        graph.add_edge(c, d);
        graph.add_edge(d, c);

        let result = tarjan_scc(&graph);
        assert!(result.has_cycles);
        assert_eq!(result.cycle_count, 2);
    }

    #[test]
    fn test_enumerate_empty() {
        let graph = DiGraph::new();
        let cycles = enumerate_cycles(&graph, 100);
        assert!(cycles.is_empty());
    }

    #[test]
    fn test_enumerate_dag() {
        // a -> b -> c (no cycles)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let cycles = enumerate_cycles(&graph, 100);
        assert!(cycles.is_empty());
    }

    #[test]
    fn test_enumerate_simple_cycle() {
        // a -> b -> c -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let cycles = enumerate_cycles(&graph, 100);
        assert_eq!(cycles.len(), 1);
        assert_eq!(cycles[0].len(), 3);
    }

    #[test]
    fn test_enumerate_two_node_cycle() {
        // a -> b -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);
        graph.add_edge(b, a);

        let cycles = enumerate_cycles(&graph, 100);
        assert_eq!(cycles.len(), 1);
        assert_eq!(cycles[0].len(), 2);
    }

    #[test]
    fn test_enumerate_max_limit() {
        // Create graph with multiple cycles
        // a <-> b <-> c <-> d with interconnections
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, a);
        graph.add_edge(b, c);
        graph.add_edge(c, b);
        graph.add_edge(c, d);
        graph.add_edge(d, c);
        graph.add_edge(d, a);
        graph.add_edge(a, d);

        // Limit to 2 cycles
        let cycles = enumerate_cycles(&graph, 2);
        assert_eq!(cycles.len(), 2);
    }

    #[test]
    fn test_enumerate_diamond_with_back_edge() {
        //     a
        //    / \
        //   b   c
        //    \ /
        //     d -> a (back edge creates cycle)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(b, d);
        graph.add_edge(c, d);
        graph.add_edge(d, a);

        let cycles = enumerate_cycles(&graph, 100);
        // Two cycles: a->b->d->a and a->c->d->a
        assert_eq!(cycles.len(), 2);
    }

    #[test]
    fn test_enumerate_with_info() {
        // a -> b -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);
        graph.add_edge(b, a);

        let result = enumerate_cycles_with_info(&graph, 100);
        assert_eq!(result.count, 1);
        assert!(!result.truncated);

        // With limit of 1, we should get exactly 1 cycle and not be truncated
        // (since there's only 1 cycle to find)
        let result_one = enumerate_cycles_with_info(&graph, 1);
        assert_eq!(result_one.count, 1);
        assert!(!result_one.truncated);

        // With limit of 0 on a cyclic graph, truncated must be true (at least 1 cycle exists)
        let result_zero = enumerate_cycles_with_info(&graph, 0);
        assert_eq!(result_zero.count, 0);
        assert!(result_zero.truncated);

        // With limit of 0 on an acyclic DAG, truncated must remain false
        let mut dag = DiGraph::new();
        let d0 = dag.add_node("d0");
        let d1 = dag.add_node("d1");
        dag.add_edge(d0, d1);
        let dag_zero = enumerate_cycles_with_info(&dag, 0);
        assert_eq!(dag_zero.count, 0);
        assert!(!dag_zero.truncated);

        // Add a second cycle (b <-> c): now total cycles == 2 > limit 1 -> truncated == true
        let c = graph.add_node("c");
        graph.add_edge(b, c);
        graph.add_edge(c, b);
        let result_truncated = enumerate_cycles_with_info(&graph, 1);
        assert_eq!(result_truncated.count, 1);
        assert!(result_truncated.truncated);

        let result_exact_two = enumerate_cycles_with_info(&graph, 2);
        assert_eq!(result_exact_two.count, 2);
        assert!(!result_exact_two.truncated);
    }

    #[test]
    fn test_has_cycles() {
        let mut dag = DiGraph::new();
        let a = dag.add_node("a");
        let b = dag.add_node("b");
        dag.add_edge(a, b);
        assert!(!has_cycles(&dag));

        let mut cyclic = DiGraph::new();
        let x = cyclic.add_node("x");
        let y = cyclic.add_node("y");
        cyclic.add_edge(x, y);
        cyclic.add_edge(y, x);
        assert!(has_cycles(&cyclic));
    }

    #[test]
    fn test_complex_graph() {
        // Multiple interconnected cycles
        let mut graph = DiGraph::new();
        for i in 0..5 {
            graph.add_node(&format!("n{}", i));
        }
        // Create some cycles
        graph.add_edge(0, 1);
        graph.add_edge(1, 2);
        graph.add_edge(2, 0); // Cycle: 0->1->2->0
        graph.add_edge(2, 3);
        graph.add_edge(3, 4);
        graph.add_edge(4, 2); // Cycle: 2->3->4->2

        let scc = tarjan_scc(&graph);
        assert!(scc.has_cycles);

        let cycles = enumerate_cycles(&graph, 100);
        assert!(cycles.len() >= 2);
    }

    // ========================================================================
    // Cycle Break Suggestion Tests
    // ========================================================================

    #[test]
    fn test_cycle_break_dag() {
        // a -> b -> c (no cycles)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert!(result.suggestions.is_empty());
        assert_eq!(result.total_cycles, 0);
        assert!(!result.truncated);
    }

    #[test]
    fn test_cycle_break_simple() {
        // a -> b -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);
        graph.add_edge(b, a);

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert_eq!(result.total_cycles, 1);
        assert_eq!(result.suggestions.len(), 2); // Both edges are candidates

        // Both edges participate in 1 cycle
        for s in &result.suggestions {
            assert_eq!(s.cycles_broken, 1);
        }
    }

    #[test]
    fn test_cycle_break_triangle() {
        // a -> b -> c -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        graph.add_edge(a, b);
        graph.add_edge(b, c);
        graph.add_edge(c, a);

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert_eq!(result.total_cycles, 1);
        assert_eq!(result.suggestions.len(), 3); // 3 edges in cycle

        // All edges participate in 1 cycle
        for s in &result.suggestions {
            assert_eq!(s.cycles_broken, 1);
        }
    }

    #[test]
    fn test_cycle_break_shared_edge() {
        //     a
        //    / \
        //   b   c
        //    \ /
        //     d -> a (creates two cycles sharing d->a)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(a, c);
        graph.add_edge(b, d);
        graph.add_edge(c, d);
        graph.add_edge(d, a); // Shared back edge

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert_eq!(result.total_cycles, 2);

        // d->a should be ranked first (breaks 2 cycles)
        let best = &result.suggestions[0];
        assert_eq!(best.from, d);
        assert_eq!(best.to, a);
        assert_eq!(best.cycles_broken, 2);
    }

    #[test]
    fn test_cycle_break_includes_ids() {
        // a -> b -> a
        let mut graph = DiGraph::new();
        graph.add_node("issue-1");
        graph.add_node("issue-2");
        graph.add_edge(0, 1);
        graph.add_edge(1, 0);

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert!(!result.suggestions.is_empty());

        let s = &result.suggestions[0];
        assert!(s.from_id.is_some());
        assert!(s.to_id.is_some());
    }

    #[test]
    fn test_cycle_break_limit() {
        // Many edges in cycle
        let mut graph = DiGraph::new();
        for i in 0..10 {
            graph.add_node(&format!("n{}", i));
        }
        // Create a 10-node cycle
        for i in 0..10 {
            graph.add_edge(i, (i + 1) % 10);
        }

        let result = cycle_break_suggestions(&graph, 3, 100);
        assert_eq!(result.suggestions.len(), 3); // Limited to 3
    }

    #[test]
    fn test_quick_cycle_break() {
        // a -> b -> a
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);
        graph.add_edge(b, a);

        let suggestions = quick_cycle_break_edges(&graph, 10);
        assert_eq!(suggestions.len(), 2);

        // Sorted by collateral
        for s in &suggestions {
            assert_eq!(s.cycles_broken, 1); // Heuristic value
        }
    }

    #[test]
    fn test_quick_cycle_break_dag() {
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        graph.add_edge(a, b);

        let suggestions = quick_cycle_break_edges(&graph, 10);
        assert!(suggestions.is_empty());
    }

    #[test]
    fn test_cycle_break_disconnected_cycles() {
        // Two separate cycles
        // Cycle 1: a -> b -> a
        // Cycle 2: c -> d -> c
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, a);
        graph.add_edge(c, d);
        graph.add_edge(d, c);

        let result = cycle_break_suggestions(&graph, 10, 100);
        assert_eq!(result.total_cycles, 2);
        assert_eq!(result.suggestions.len(), 4); // 2 edges per cycle
    }

    #[test]
    fn test_cycle_break_collateral_ordering() {
        // a -> b -> a  (small cycle)
        // a -> c -> d -> a (larger cycle through same node)
        let mut graph = DiGraph::new();
        let a = graph.add_node("a");
        let b = graph.add_node("b");
        let c = graph.add_node("c");
        let d = graph.add_node("d");
        graph.add_edge(a, b);
        graph.add_edge(b, a);
        graph.add_edge(a, c);
        graph.add_edge(c, d);
        graph.add_edge(d, a);

        let result = cycle_break_suggestions(&graph, 10, 100);
        // Should have suggestions sorted by cycles_broken desc, then collateral asc
        // Check that suggestions are not empty
        assert!(!result.suggestions.is_empty());

        // Verify ordering: if same cycles_broken, lower collateral first
        for i in 1..result.suggestions.len() {
            let prev = &result.suggestions[i - 1];
            let curr = &result.suggestions[i];
            if prev.cycles_broken == curr.cycles_broken {
                assert!(prev.collateral <= curr.collateral);
            } else {
                assert!(prev.cycles_broken >= curr.cycles_broken);
            }
        }
    }

    #[test]
    fn test_self_loop_enumeration_and_break_suggestions() {
        // Node u -> u self-loop bridged to node v -> v self-loop via DAG edge u -> v
        let mut graph = DiGraph::new();
        let u = graph.add_node("self-u");
        let v = graph.add_node("self-v");
        graph.add_edge(u, u);
        graph.add_edge(u, v);
        graph.add_edge(v, v);

        assert!(has_cycles(&graph));
        let scc = tarjan_scc(&graph);
        assert!(scc.has_cycles);
        assert_eq!(scc.cycle_count, 2);

        let enum_res = enumerate_cycles_with_info(&graph, 10);
        assert_eq!(enum_res.count, 2);
        assert!(!enum_res.truncated);
        assert_eq!(enum_res.cycles, vec![vec![u], vec![v]]);

        let enum_zero = enumerate_cycles_with_info(&graph, 0);
        assert_eq!(enum_zero.count, 0);
        assert!(enum_zero.truncated);

        let brk = cycle_break_suggestions(&graph, 10, 100);
        assert_eq!(brk.total_cycles, 2);
        assert!(!brk.truncated);
        // Only the two self-edges (u, u) and (v, v) belong to cyclic SCCs; bridge (u, v) must not appear.
        assert_eq!(brk.suggestions.len(), 2);
        for item in &brk.suggestions {
            assert_eq!(item.from, item.to);
            assert_eq!(item.cycles_broken, 1);
        }
    }
}
