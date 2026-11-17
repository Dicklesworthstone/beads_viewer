//! High-performance graph algorithms for bv static viewer.
//!
//! This crate provides WASM-compiled graph algorithms that run in the browser,
//! enabling fast dependency analysis without server roundtrips.

use wasm_bindgen::prelude::*;

mod advanced;
pub mod algorithms;
mod graph;
mod reachability;
mod subgraph;
mod whatif;

pub use graph::DiGraph;

// Re-export key algorithm functions for testing
pub use algorithms::betweenness::{betweenness, betweenness_approx};
pub use algorithms::critical_path::{
    critical_path_heights, critical_path_length, critical_path_nodes,
};
pub use algorithms::cycles::{has_cycles, tarjan_scc};
pub use algorithms::eigenvector::{eigenvector, eigenvector_default, EigenvectorConfig};
pub use algorithms::hits::{hits, hits_default, HITSConfig};
pub use algorithms::kcore::{degeneracy, kcore};
pub use algorithms::pagerank::{pagerank, pagerank_default, PageRankConfig};
pub use algorithms::parallel_cut::{
    parallel_cut_default, parallel_cut_filtered, parallel_cut_suggestions,
};
pub use algorithms::slack::{slack, total_float};
pub use algorithms::topk_set::{topk_set, topk_set_default};
pub use reachability::{reachable_from, reachable_to};

/// Initialize panic hook for better error messages in browser console.
#[wasm_bindgen(start)]
pub fn init() {
    #[cfg(feature = "console_error_panic_hook")]
    console_error_panic_hook::set_once();
}

/// Get the crate version.
#[wasm_bindgen]
pub fn version() -> String {
    env!("CARGO_PKG_VERSION").to_string()
}
