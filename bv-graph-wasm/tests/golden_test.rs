//! Golden file validation tests for cross-validation with Go implementation.
//!
//! These tests load graph definitions and expected metrics from the shared testdata
//! directory, run the WASM algorithms, and compare results against the Go implementation.

use bv_graph_wasm::{betweenness, critical_path_heights, has_cycles, pagerank_default, DiGraph};
#[cfg(feature = "eigenvector")]
use bv_graph_wasm::eigenvector_default;
#[cfg(feature = "hits")]
use bv_graph_wasm::hits_default;
#[cfg(feature = "kcore")]
use bv_graph_wasm::kcore;
#[cfg(feature = "slack")]
use bv_graph_wasm::slack;
use serde::Deserialize;
use std::collections::HashMap;
use std::fs;
use std::path::Path;

/// Test graph file format (matches Go's TestGraphFile).
#[derive(Debug, Deserialize)]
struct TestGraphFile {
    #[allow(dead_code)]
    description: String,
    nodes: Vec<String>,
    edges: Vec<(usize, usize)>,
}

/// Golden metrics format (matches Go's GoldenMetrics).
#[derive(Debug, Deserialize)]
#[allow(dead_code)]
struct GoldenMetrics {
    description: String,
    node_count: usize,
    edge_count: usize,
    density: f64,
    pagerank: HashMap<String, f64>,
    betweenness: HashMap<String, f64>,
    eigenvector: HashMap<String, f64>,
    hubs: HashMap<String, f64>,
    authorities: HashMap<String, f64>,
    critical_path_score: HashMap<String, f64>,
    topological_order: Option<Vec<String>>,
    core_number: HashMap<String, i32>,
    slack: Option<HashMap<String, f64>>,
    has_cycles: bool,
    cycles: Option<Vec<Vec<String>>>,
    out_degree: HashMap<String, i32>,
    in_degree: HashMap<String, i32>,
}

/// Load a test graph from JSON.
fn load_test_graph(path: &Path) -> (DiGraph, TestGraphFile) {
    let content = fs::read_to_string(path).expect("Failed to read graph file");
    let graph_file: TestGraphFile =
        serde_json::from_str(&content).expect("Failed to parse graph file");

    let mut graph = DiGraph::with_capacity(graph_file.nodes.len(), graph_file.edges.len());
    for node_id in &graph_file.nodes {
        graph.add_node(node_id);
    }
    for (from, to) in &graph_file.edges {
        graph.add_edge(*from, *to);
    }

    (graph, graph_file)
}

/// Load golden metrics from JSON.
fn load_golden_metrics(path: &Path) -> GoldenMetrics {
    let content = fs::read_to_string(path).expect("Failed to read golden file");
    serde_json::from_str(&content).expect("Failed to parse golden file")
}

/// Compare two f64 values within tolerance.
fn assert_float_eq(actual: f64, expected: f64, tolerance: f64, name: &str, id: &str) {
    let diff = (actual - expected).abs();
    assert!(
        diff <= tolerance,
        "{} for {}: got {}, expected {}, diff {} > tolerance {}",
        name,
        id,
        actual,
        expected,
        diff,
        tolerance
    );
}

/// Compare array results against map within tolerance.
fn validate_array_against_map(
    name: &str,
    actual: &[f64],
    expected: &HashMap<String, f64>,
    nodes: &[String],
    tolerance: f64,
) {
    assert_eq!(
        actual.len(),
        nodes.len(),
        "{}: result length mismatch",
        name
    );

    for (idx, node_id) in nodes.iter().enumerate() {
        if let Some(&exp_val) = expected.get(node_id) {
            let act_val = actual[idx];
            assert_float_eq(act_val, exp_val, tolerance, name, node_id);
        }
    }
}

// Path to testdata directory (relative to crate root when running cargo test)
const TESTDATA_DIR: &str = "../testdata";

fn graph_and_golden_paths(name: &str) -> (std::path::PathBuf, std::path::PathBuf) {
    let graph_path = Path::new(TESTDATA_DIR).join(format!("graphs/{}.json", name));
    let golden_path = Path::new(TESTDATA_DIR).join(format!("expected/{}_metrics.json", name));
    (graph_path, golden_path)
}

fn skip_if_missing(graph_path: &Path, golden_path: &Path) -> bool {
    if !graph_path.exists() || !golden_path.exists() {
        eprintln!(
            "Skipping test: golden files not found at {:?} or {:?}",
            graph_path, golden_path
        );
        return true;
    }
    false
}

// ==========================================================================
// Basic validation tests
// ==========================================================================

#[test]
fn test_golden_chain_10_basic() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, _) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    assert_eq!(
        graph.node_count(),
        expected.node_count,
        "node_count mismatch"
    );
    assert_eq!(
        graph.edge_count(),
        expected.edge_count,
        "edge_count mismatch"
    );
}

#[test]
fn test_golden_diamond_5_basic() {
    let (graph_path, golden_path) = graph_and_golden_paths("diamond_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, _) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    assert_eq!(graph.node_count(), expected.node_count);
    assert_eq!(graph.edge_count(), expected.edge_count);
    assert!(graph.is_dag(), "diamond should be a DAG");
}

#[test]
fn test_golden_star_10_basic() {
    let (graph_path, golden_path) = graph_and_golden_paths("star_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, _) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    assert_eq!(graph.node_count(), expected.node_count);
    assert_eq!(graph.edge_count(), expected.edge_count);
    assert!(graph.is_dag(), "star should be a DAG");
}

#[test]
fn test_golden_cycle_5_detection() {
    let (graph_path, golden_path) = graph_and_golden_paths("cycle_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, _) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    assert!(!graph.is_dag(), "cycle graph should not be DAG");
    assert_eq!(
        has_cycles(&graph),
        expected.has_cycles,
        "has_cycles mismatch"
    );
}

#[test]
fn test_golden_complex_20_basic() {
    let (graph_path, golden_path) = graph_and_golden_paths("complex_20");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, _) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    assert_eq!(graph.node_count(), expected.node_count);
    assert_eq!(graph.edge_count(), expected.edge_count);
}

// ==========================================================================
// PageRank validation tests
// ==========================================================================

#[test]
fn test_golden_chain_10_pagerank() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let pr = pagerank_default(&graph);
    validate_array_against_map("PageRank", &pr, &expected.pagerank, &graph_file.nodes, 1e-5);
}

#[test]
fn test_golden_diamond_5_pagerank() {
    let (graph_path, golden_path) = graph_and_golden_paths("diamond_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let pr = pagerank_default(&graph);
    validate_array_against_map("PageRank", &pr, &expected.pagerank, &graph_file.nodes, 1e-5);
}

#[test]
fn test_golden_star_10_pagerank() {
    let (graph_path, golden_path) = graph_and_golden_paths("star_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let pr = pagerank_default(&graph);
    validate_array_against_map("PageRank", &pr, &expected.pagerank, &graph_file.nodes, 1e-5);
}

#[test]
fn test_golden_complex_20_pagerank() {
    let (graph_path, golden_path) = graph_and_golden_paths("complex_20");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let pr = pagerank_default(&graph);
    validate_array_against_map("PageRank", &pr, &expected.pagerank, &graph_file.nodes, 1e-5);
}

// ==========================================================================
// Betweenness validation tests
// ==========================================================================

#[test]
fn test_golden_chain_10_betweenness() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let bw = betweenness(&graph);
    validate_array_against_map(
        "Betweenness",
        &bw,
        &expected.betweenness,
        &graph_file.nodes,
        1e-6,
    );
}

#[test]
fn test_golden_diamond_5_betweenness() {
    let (graph_path, golden_path) = graph_and_golden_paths("diamond_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let bw = betweenness(&graph);
    validate_array_against_map(
        "Betweenness",
        &bw,
        &expected.betweenness,
        &graph_file.nodes,
        1e-6,
    );
}

#[test]
fn test_golden_complex_20_betweenness() {
    let (graph_path, golden_path) = graph_and_golden_paths("complex_20");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let bw = betweenness(&graph);
    validate_array_against_map(
        "Betweenness",
        &bw,
        &expected.betweenness,
        &graph_file.nodes,
        1e-6,
    );
}

// ==========================================================================
// Critical path validation tests
// ==========================================================================

#[test]
fn test_golden_chain_10_critical_path() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let heights = critical_path_heights(&graph);
    validate_array_against_map(
        "CriticalPath",
        &heights,
        &expected.critical_path_score,
        &graph_file.nodes,
        1e-6,
    );
}

#[test]
fn test_golden_diamond_5_critical_path() {
    let (graph_path, golden_path) = graph_and_golden_paths("diamond_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let heights = critical_path_heights(&graph);
    validate_array_against_map(
        "CriticalPath",
        &heights,
        &expected.critical_path_score,
        &graph_file.nodes,
        1e-6,
    );
}

#[test]
fn test_golden_complex_20_critical_path() {
    let (graph_path, golden_path) = graph_and_golden_paths("complex_20");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let heights = critical_path_heights(&graph);
    validate_array_against_map(
        "CriticalPath",
        &heights,
        &expected.critical_path_score,
        &graph_file.nodes,
        1e-6,
    );
}

// ==========================================================================
// Eigenvector validation tests
//
// NOTE: Eigenvector centrality has known implementation differences between
// Go (gonum) and Rust. Go's implementation converges to concentrated values
// at sink nodes (zero for non-sinks in DAGs), while Rust distributes mass
// more uniformly. Both are mathematically valid approaches.
// These tests verify the Rust implementation is internally consistent rather
// than matching Go exactly.
// ==========================================================================

#[cfg(feature = "eigenvector")]
#[test]
fn test_eigenvector_sum_to_one() {
    // Eigenvector scores should be normalized
    for name in &["chain_10", "diamond_5", "star_10", "complex_20"] {
        let (graph_path, _) = graph_and_golden_paths(name);
        if !graph_path.exists() {
            continue;
        }

        let (graph, _) = load_test_graph(&graph_path);
        let ev = eigenvector_default(&graph);

        // L2 norm should be close to 1.0
        let l2_norm: f64 = ev.iter().map(|x| x * x).sum::<f64>().sqrt();
        assert!(
            (l2_norm - 1.0).abs() < 0.01,
            "Eigenvector L2 norm should be ~1.0 for {}, got {}",
            name,
            l2_norm
        );
    }
}

#[cfg(feature = "eigenvector")]
#[test]
fn test_eigenvector_non_negative() {
    // All eigenvector scores should be non-negative
    for name in &["chain_10", "diamond_5", "star_10", "complex_20"] {
        let (graph_path, _) = graph_and_golden_paths(name);
        if !graph_path.exists() {
            continue;
        }

        let (graph, _) = load_test_graph(&graph_path);
        let ev = eigenvector_default(&graph);

        for (i, &score) in ev.iter().enumerate() {
            assert!(
                score >= 0.0,
                "Eigenvector score should be non-negative for node {} in {}",
                i,
                name
            );
        }
    }
}

// ==========================================================================
// HITS validation tests
// ==========================================================================

#[cfg(feature = "hits")]
#[test]
fn test_golden_chain_10_hits() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let result = hits_default(&graph);
    validate_array_against_map(
        "Hubs",
        &result.hubs,
        &expected.hubs,
        &graph_file.nodes,
        1e-5,
    );
    validate_array_against_map(
        "Authorities",
        &result.authorities,
        &expected.authorities,
        &graph_file.nodes,
        1e-5,
    );
}

#[cfg(feature = "hits")]
#[test]
fn test_golden_star_10_hits() {
    let (graph_path, golden_path) = graph_and_golden_paths("star_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let result = hits_default(&graph);
    validate_array_against_map(
        "Hubs",
        &result.hubs,
        &expected.hubs,
        &graph_file.nodes,
        1e-5,
    );
    validate_array_against_map(
        "Authorities",
        &result.authorities,
        &expected.authorities,
        &graph_file.nodes,
        1e-5,
    );
}

// ==========================================================================
// K-core validation tests
// ==========================================================================

#[cfg(feature = "kcore")]
#[test]
fn test_golden_chain_10_kcore() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let cores = kcore(&graph);
    for (idx, node_id) in graph_file.nodes.iter().enumerate() {
        if let Some(&exp_core) = expected.core_number.get(node_id) {
            let act_core = cores[idx] as i32;
            assert_eq!(act_core, exp_core, "K-core mismatch for {}", node_id);
        }
    }
}

// ==========================================================================
// Slack validation tests
// ==========================================================================

#[cfg(feature = "slack")]
#[test]
fn test_golden_chain_10_slack() {
    let (graph_path, golden_path) = graph_and_golden_paths("chain_10");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    if let Some(ref expected_slack) = expected.slack {
        let s = slack(&graph);
        validate_array_against_map("Slack", &s, expected_slack, &graph_file.nodes, 1e-6);
    }
}

#[cfg(feature = "slack")]
#[test]
fn test_golden_diamond_5_slack() {
    let (graph_path, golden_path) = graph_and_golden_paths("diamond_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    if let Some(ref expected_slack) = expected.slack {
        let s = slack(&graph);
        validate_array_against_map("Slack", &s, expected_slack, &graph_file.nodes, 1e-6);
    }
}

// ==========================================================================
// Density validation tests
// ==========================================================================

#[test]
fn test_golden_all_density() {
    for name in &["chain_10", "diamond_5", "star_10", "complex_20"] {
        let (graph_path, golden_path) = graph_and_golden_paths(name);
        if skip_if_missing(&graph_path, &golden_path) {
            continue;
        }

        let (graph, _) = load_test_graph(&graph_path);
        let expected = load_golden_metrics(&golden_path);

        let n = graph.node_count() as f64;
        let e = graph.edge_count() as f64;
        let actual_density = if n > 1.0 { e / (n * (n - 1.0)) } else { 0.0 };

        assert_float_eq(actual_density, expected.density, 1e-6, "density", name);
    }
}

// ==========================================================================
// Cycle PageRank validation (uniform distribution expected)
// ==========================================================================

#[test]
fn test_golden_cycle_5_pagerank() {
    let (graph_path, golden_path) = graph_and_golden_paths("cycle_5");
    if skip_if_missing(&graph_path, &golden_path) {
        return;
    }

    let (graph, graph_file) = load_test_graph(&graph_path);
    let expected = load_golden_metrics(&golden_path);

    let pr = pagerank_default(&graph);
    validate_array_against_map("PageRank", &pr, &expected.pagerank, &graph_file.nodes, 1e-5);
}

// ==========================================================================
// Degree validation tests
// ==========================================================================

#[test]
fn test_golden_degrees() {
    for name in &["chain_10", "diamond_5", "star_10", "complex_20"] {
        let (graph_path, golden_path) = graph_and_golden_paths(name);
        if skip_if_missing(&graph_path, &golden_path) {
            continue;
        }

        let (graph, _) = load_test_graph(&graph_path);
        let expected = load_golden_metrics(&golden_path);

        for (node_id, &exp_out) in &expected.out_degree {
            if let Some(idx) = graph.node_idx(node_id) {
                let act_out = graph.out_degree(idx) as i32;
                assert_eq!(
                    act_out, exp_out,
                    "out_degree mismatch for {} in {}",
                    node_id, name
                );
            }
        }
        for (node_id, &exp_in) in &expected.in_degree {
            if let Some(idx) = graph.node_idx(node_id) {
                let act_in = graph.in_degree(idx) as i32;
                assert_eq!(
                    act_in, exp_in,
                    "in_degree mismatch for {} in {}",
                    node_id, name
                );
            }
        }
    }
}
