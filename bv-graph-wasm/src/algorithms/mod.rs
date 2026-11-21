//! Graph algorithm implementations.
//!
//! This module contains ports of the Go graph algorithms to Rust WASM.

pub mod articulation;
pub mod betweenness;
pub mod coverage;
pub mod critical_path;
pub mod cycles;
#[cfg(feature = "eigenvector")]
pub mod eigenvector;
#[cfg(feature = "hits")]
pub mod hits;
pub mod k_paths;
#[cfg(feature = "kcore")]
pub mod kcore;
pub mod pagerank;
pub mod parallel_cut;
#[cfg(feature = "slack")]
pub mod slack;
pub mod subgraph;
pub mod topk_set;
pub mod topo;
