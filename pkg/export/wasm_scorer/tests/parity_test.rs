use bv_hybrid_scorer::{HybridScorer, IssueMetrics, Weights};
use serde::Deserialize;

#[derive(Debug, Deserialize)]
struct ScoredResult {
    id: String,
    score: f64,
    text_score: f64,
    components: [f64; 5],
}

fn assert_close(actual: f64, expected: f64, tolerance: f64, label: &str) {
    let diff = (actual - expected).abs();
    assert!(
        diff <= tolerance,
        "{}: got {}, want {}, diff {}",
        label,
        actual,
        expected,
        diff
    );
}

fn default_weights() -> Weights {
    Weights {
        text: 0.40,
        pagerank: 0.20,
        status: 0.15,
        impact: 0.10,
        priority: 0.10,
        recency: 0.05,
    }
}

#[test]
fn score_batch_basic_parity() {
    let weights = default_weights();
    let metrics = vec![
        IssueMetrics {
            id: "A".to_string(),
            text_score: 0.8,
            pagerank: 0.5,
            status: 0,
            priority: 1,
            blocker_count: 3,
            days_since_update: 0.0,
        },
        IssueMetrics {
            id: "B".to_string(),
            text_score: 0.8,
            pagerank: 0.1,
            status: 2,
            priority: 3,
            blocker_count: 0,
            days_since_update: 10.0,
        },
    ];

    let mut scorer = HybridScorer::new(&serde_json::to_string(&weights).unwrap());
    let raw = scorer.score_batch(&serde_json::to_string(&metrics).unwrap());
    let results: Vec<ScoredResult> = serde_json::from_str(&raw).expect("parse results");

    assert_eq!(results.len(), 2);
    assert_eq!(results[0].id, "A");
    assert_eq!(results[1].id, "B");

    let status_score_a = 1.0;
    let priority_score_a = 0.8;
    let impact_score_a = 1.0;
    let recency_score_a = 1.0;
    let expected_a = 0.4 * 0.8
        + 0.2 * 0.5
        + 0.15 * status_score_a
        + 0.1 * impact_score_a
        + 0.1 * priority_score_a
        + 0.05 * recency_score_a;

    assert_close(results[0].score, expected_a, 1e-6, "score A");
    assert_close(results[0].text_score, 0.8, 1e-6, "text A");
    assert_close(results[0].components[0], 0.5, 1e-6, "pagerank A");
    assert_close(results[0].components[1], status_score_a, 1e-6, "status A");
    assert_close(results[0].components[2], impact_score_a, 1e-6, "impact A");
    assert_close(
        results[0].components[3],
        priority_score_a,
        1e-6,
        "priority A",
    );
    assert_close(results[0].components[4], recency_score_a, 1e-6, "recency A");
}

#[test]
fn score_batch_tie_breaks_by_id() {
    let weights = default_weights();
    let metrics = vec![
        IssueMetrics {
            id: "B".to_string(),
            text_score: 0.5,
            pagerank: 0.5,
            status: 0,
            priority: 2,
            blocker_count: 1,
            days_since_update: 0.0,
        },
        IssueMetrics {
            id: "A".to_string(),
            text_score: 0.5,
            pagerank: 0.5,
            status: 0,
            priority: 2,
            blocker_count: 1,
            days_since_update: 0.0,
        },
    ];

    let mut scorer = HybridScorer::new(&serde_json::to_string(&weights).unwrap());
    let raw = scorer.score_batch(&serde_json::to_string(&metrics).unwrap());
    let results: Vec<ScoredResult> = serde_json::from_str(&raw).expect("parse results");

    assert_eq!(results.len(), 2);
    assert_eq!(results[0].id, "A");
    assert_eq!(results[1].id, "B");
}

#[test]
fn score_batch_handles_negative_recency() {
    let weights = default_weights();
    let metrics = vec![IssueMetrics {
        id: "A".to_string(),
        text_score: 0.2,
        pagerank: 0.2,
        status: 255,
        priority: 9,
        blocker_count: 0,
        days_since_update: -1.0,
    }];

    let mut scorer = HybridScorer::new(&serde_json::to_string(&weights).unwrap());
    let raw = scorer.score_batch(&serde_json::to_string(&metrics).unwrap());
    let results: Vec<ScoredResult> = serde_json::from_str(&raw).expect("parse results");

    assert_eq!(results.len(), 1);
    assert_close(results[0].components[4], 0.5, 1e-6, "recency fallback");
}

#[test]
fn score_batch_honors_configured_max_blocker_count() {
    let metrics = vec![IssueMetrics {
        id: "cap-auth-ui".to_string(),
        text_score: 0.9,
        pagerank: 0.5,
        status: 0,
        priority: 1,
        blocker_count: 1,
        days_since_update: 0.0,
    }];
    let metrics_json = serde_json::to_string(&metrics).unwrap();

    // Via constructor JSON max_blocker_count=4 -> 1 / 4 = 0.25 (instead of single-item 1 / 1 = 1.0)
    let mut scorer = HybridScorer::new(
        r#"{"text":0.4,"pagerank":0.2,"status":0.15,"impact":0.1,"priority":0.1,"recency":0.05,"max_blocker_count":4}"#,
    );
    let results: Vec<ScoredResult> =
        serde_json::from_str(&scorer.score_batch(&metrics_json)).expect("parse results");
    assert_eq!(results.len(), 1);
    assert_close(results[0].components[2], 0.25, 1e-12, "configured max_blocker_count=4");

    // Via score_batch_with_max(..., 2) -> 1 / 2 = 0.5
    let mut scorer2 = HybridScorer::new("{}");
    let results2: Vec<ScoredResult> = serde_json::from_str(
        &scorer2.score_batch_with_max(&metrics_json, 2),
    )
    .expect("parse results2");
    assert_close(results2[0].components[2], 0.5, 1e-12, "score_batch_with_max=2");
}
