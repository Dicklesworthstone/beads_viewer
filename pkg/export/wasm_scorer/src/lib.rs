use serde::{Deserialize, Serialize};
use wasm_bindgen::prelude::*;

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct Weights {
    pub text: f64,
    pub pagerank: f64,
    pub status: f64,
    pub impact: f64,
    pub priority: f64,
    pub recency: f64,
}

impl Default for Weights {
    fn default() -> Self {
        Self {
            text: 0.40,
            pagerank: 0.20,
            status: 0.15,
            impact: 0.10,
            priority: 0.10,
            recency: 0.05,
        }
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct IssueMetrics {
    pub id: String,
    pub text_score: f64,
    pub pagerank: f64,
    pub status: u8,
    pub priority: u8,
    pub blocker_count: u32,
    pub days_since_update: f64,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ScoredResult {
    pub id: String,
    pub score: f64,
    pub text_score: f64,
    pub components: [f64; 5],
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct ScorerConfig {
    #[serde(default = "default_weight_text")]
    text: f64,
    #[serde(default = "default_weight_pagerank")]
    pagerank: f64,
    #[serde(default = "default_weight_status")]
    status: f64,
    #[serde(default = "default_weight_impact")]
    impact: f64,
    #[serde(default = "default_weight_priority")]
    priority: f64,
    #[serde(default = "default_weight_recency")]
    recency: f64,
    #[serde(default, alias = "maxBlockerCount")]
    max_blocker_count: Option<u32>,
}

fn default_weight_text() -> f64 {
    0.40
}
fn default_weight_pagerank() -> f64 {
    0.20
}
fn default_weight_status() -> f64 {
    0.15
}
fn default_weight_impact() -> f64 {
    0.10
}
fn default_weight_priority() -> f64 {
    0.10
}
fn default_weight_recency() -> f64 {
    0.05
}

impl Default for ScorerConfig {
    fn default() -> Self {
        Self {
            text: default_weight_text(),
            pagerank: default_weight_pagerank(),
            status: default_weight_status(),
            impact: default_weight_impact(),
            priority: default_weight_priority(),
            recency: default_weight_recency(),
            max_blocker_count: None,
        }
    }
}

#[wasm_bindgen]
pub struct HybridScorer {
    weights: Weights,
    max_blocker_count: u32,
    configured_max_blocker_count: Option<u32>,
}

#[wasm_bindgen]
impl HybridScorer {
    #[wasm_bindgen(constructor)]
    pub fn new(weights_json: &str) -> HybridScorer {
        let cfg: ScorerConfig = serde_json::from_str(weights_json).unwrap_or_default();
        HybridScorer {
            weights: Weights {
                text: cfg.text,
                pagerank: cfg.pagerank,
                status: cfg.status,
                impact: cfg.impact,
                priority: cfg.priority,
                recency: cfg.recency,
            },
            max_blocker_count: 0,
            configured_max_blocker_count: cfg.max_blocker_count,
        }
    }

    #[wasm_bindgen]
    pub fn score_batch(&mut self, metrics_json: &str) -> String {
        self.score_batch_internal(metrics_json, self.configured_max_blocker_count)
    }

    #[wasm_bindgen]
    pub fn score_batch_with_max(
        &mut self,
        metrics_json: &str,
        max_blocker_count: u32,
    ) -> String {
        self.score_batch_internal(metrics_json, Some(max_blocker_count))
    }
}

impl HybridScorer {
    fn score_batch_internal(
        &mut self,
        metrics_json: &str,
        configured_max: Option<u32>,
    ) -> String {
        let metrics: Vec<IssueMetrics> = match serde_json::from_str(metrics_json) {
            Ok(metrics) => metrics,
            Err(_) => return "[]".to_string(),
        };

        let batch_max = metrics.iter().map(|m| m.blocker_count).max().unwrap_or(0);
        self.max_blocker_count = match configured_max {
            Some(cfg_max) => batch_max.max(cfg_max),
            None => batch_max,
        };

        let mut results: Vec<ScoredResult> = metrics.iter().map(|m| self.score_single(m)).collect();

        results.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| a.id.cmp(&b.id))
        });

        serde_json::to_string(&results).unwrap_or_else(|_| "[]".to_string())
    }
    fn score_single(&self, m: &IssueMetrics) -> ScoredResult {
        let status_score = normalize_status(m.status);
        let priority_score = normalize_priority(m.priority);
        let impact_score = normalize_impact(m.blocker_count, self.max_blocker_count);
        let recency_score = normalize_recency(m.days_since_update);
        let pagerank = normalize_pagerank(m.pagerank);

        let score = self.weights.text * safe_number(m.text_score)
            + self.weights.pagerank * pagerank
            + self.weights.status * status_score
            + self.weights.impact * impact_score
            + self.weights.priority * priority_score
            + self.weights.recency * recency_score;

        ScoredResult {
            id: m.id.clone(),
            score,
            text_score: safe_number(m.text_score),
            components: [
                pagerank,
                status_score,
                impact_score,
                priority_score,
                recency_score,
            ],
        }
    }
}

fn normalize_status(status: u8) -> f64 {
    match status {
        0 => 1.0, // open
        1 => 0.8, // in_progress
        2 => 0.5, // blocked
        3 => 0.1, // closed
        4 => 0.8, // review
        5 => 0.8, // hooked
        6 => 0.2, // pinned
        7 => 0.2, // deferred
        8 => 0.2, // draft
        9 => 0.0, // tombstone
        _ => 0.5, // custom / unknown
    }
}

fn normalize_priority(priority: u8) -> f64 {
    match priority {
        0 => 1.0,
        1 => 0.8,
        2 => 0.6,
        3 => 0.4,
        4 => 0.2,
        _ => 0.5,
    }
}

fn normalize_impact(blocker_count: u32, max_blocker_count: u32) -> f64 {
    if max_blocker_count == 0 {
        return 0.5;
    }
    if blocker_count == 0 {
        return 0.0;
    }
    if blocker_count >= max_blocker_count {
        return 1.0;
    }
    blocker_count as f64 / max_blocker_count as f64
}

fn normalize_recency(days_since_update: f64) -> f64 {
    if !days_since_update.is_finite() || days_since_update < 0.0 {
        return 0.5;
    }
    (-days_since_update / 30.0).exp()
}

fn normalize_pagerank(pagerank: f64) -> f64 {
    if pagerank.is_finite() {
        pagerank.clamp(0.0, 1.0)
    } else {
        0.5
    }
}

fn safe_number(value: f64) -> f64 {
    if value.is_finite() {
        value
    } else {
        0.0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_weights_match_go_normalizer() {
        let expected = [
            (0_u8, 1.0),   // open
            (1_u8, 0.8),   // in_progress
            (2_u8, 0.5),   // blocked
            (3_u8, 0.1),   // closed
            (4_u8, 0.8),   // review
            (5_u8, 0.8),   // hooked
            (6_u8, 0.2),   // pinned
            (7_u8, 0.2),   // deferred
            (8_u8, 0.2),   // draft
            (9_u8, 0.0),   // tombstone
            (255_u8, 0.5), // unknown custom status
        ];
        for (code, want) in expected {
            let got = normalize_status(code);
            assert!(
                (got - want).abs() < 1e-12,
                "status code {code}: got {got}, want {want}"
            );
        }
    }

    #[test]
    fn score_batch_ranks_and_tie_breaks_by_id() {
        let mut scorer = HybridScorer::new("{}");
        let payload = serde_json::json!([
            {
                "id": "bv-20",
                "text_score": 0.75,
                "pagerank": 0.4,
                "status": 4,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            },
            {
                "id": "bv-10",
                "text_score": 0.75,
                "pagerank": 0.4,
                "status": 5,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            },
            {
                "id": "bv-30",
                "text_score": 0.75,
                "pagerank": 0.4,
                "status": 7,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            }
        ]);
        let raw = scorer.score_batch(&payload.to_string());
        let parsed: Vec<ScoredResult> = serde_json::from_str(&raw).expect("valid JSON output");
        let ids: Vec<&str> = parsed.iter().map(|r| r.id.as_str()).collect();
        assert_eq!(ids, vec!["bv-10", "bv-20", "bv-30"]);
        assert!((parsed[0].components[1] - 0.8).abs() < 1e-12);
        assert!((parsed[1].components[1] - 0.8).abs() < 1e-12);
        assert!((parsed[2].components[1] - 0.2).abs() < 1e-12);
    }

    #[test]
    fn impact_and_recency_neutral_defaults() {
        assert!((normalize_impact(0, 0) - 0.5).abs() < 1e-12);
        assert!((normalize_impact(0, 4) - 0.0).abs() < 1e-12);
        assert!((normalize_impact(2, 4) - 0.5).abs() < 1e-12);
        assert!((normalize_impact(4, 4) - 1.0).abs() < 1e-12);
        assert!((normalize_recency(-1.0) - 0.5).abs() < 1e-12);
        assert!((normalize_recency(f64::NAN) - 0.5).abs() < 1e-12);
        assert!((normalize_recency(0.0) - 1.0).abs() < 1e-12);
    }

    #[test]
    fn pagerank_clamps_out_of_range_and_defaults_non_finite() {
        assert!((normalize_pagerank(2.5) - 1.0).abs() < 1e-12);
        assert!((normalize_pagerank(-0.5) - 0.0).abs() < 1e-12);
        assert!((normalize_pagerank(0.4) - 0.4).abs() < 1e-12);
        assert!((normalize_pagerank(f64::NAN) - 0.5).abs() < 1e-12);
        assert!((normalize_pagerank(f64::INFINITY) - 0.5).abs() < 1e-12);
        assert!((normalize_pagerank(f64::NEG_INFINITY) - 0.5).abs() < 1e-12);

        let mut scorer = HybridScorer::new("{}");
        let payload = serde_json::json!([
            {
                "id": "pr-over",
                "text_score": 0.8,
                "pagerank": 2.5,
                "status": 0,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            },
            {
                "id": "pr-unit",
                "text_score": 0.8,
                "pagerank": 1.0,
                "status": 0,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            },
            {
                "id": "pr-neg",
                "text_score": 0.8,
                "pagerank": -0.5,
                "status": 0,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            },
            {
                "id": "pr-zero",
                "text_score": 0.8,
                "pagerank": 0.0,
                "status": 0,
                "priority": 1,
                "blocker_count": 2,
                "days_since_update": 0.0
            }
        ]);
        let raw = scorer.score_batch(&payload.to_string());
        let parsed: Vec<ScoredResult> = serde_json::from_str(&raw).expect("valid JSON output");
        let by_id: std::collections::HashMap<&str, &ScoredResult> =
            parsed.iter().map(|r| (r.id.as_str(), r)).collect();
        assert!((by_id["pr-over"].components[0] - 1.0).abs() < 1e-12);
        assert!((by_id["pr-over"].score - by_id["pr-unit"].score).abs() < 1e-12);
        assert!((by_id["pr-neg"].components[0] - 0.0).abs() < 1e-12);
        assert!((by_id["pr-neg"].score - by_id["pr-zero"].score).abs() < 1e-12);
    }
}
