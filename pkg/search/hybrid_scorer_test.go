package search

import (
	"math"
	"testing"
	"time"
)

type stubMetricsCache struct {
	metrics         map[string]IssueMetrics
	maxBlockerCount int
	missing         bool
}

func (s *stubMetricsCache) Get(issueID string) (IssueMetrics, bool) {
	if s.missing {
		return IssueMetrics{}, false
	}
	metric, ok := s.metrics[issueID]
	return metric, ok
}

func (s *stubMetricsCache) GetBatch(issueIDs []string) map[string]IssueMetrics {
	out := make(map[string]IssueMetrics, len(issueIDs))
	for _, id := range issueIDs {
		metric, ok := s.Get(id)
		if ok {
			out[id] = metric
		}
	}
	return out
}

func (s *stubMetricsCache) Refresh() error {
	return nil
}

func (s *stubMetricsCache) DataHash() string {
	return ""
}

func (s *stubMetricsCache) MaxBlockerCount() int {
	return s.maxBlockerCount
}

func TestHybridScorer_Score(t *testing.T) {
	cache := &stubMetricsCache{
		metrics: map[string]IssueMetrics{
			"A": {
				IssueID:      "A",
				PageRank:     0.8,
				Status:       "open",
				Priority:     1,
				BlockerCount: 2,
				UpdatedAt:    time.Now(),
			},
		},
		maxBlockerCount: 4,
	}

	weights := Weights{
		TextRelevance: 0.5,
		PageRank:      0.1,
		Status:        0.1,
		Impact:        0.1,
		Priority:      0.1,
		Recency:       0.1,
	}

	scorer := NewHybridScorer(weights, cache)
	result, err := scorer.Score("A", 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	impactScore := normalizeImpact(2, 4)
	priorityScore := normalizePriority(1)
	statusScore := normalizeStatus("open")
	recencyScore := normalizeRecency(cache.metrics["A"].UpdatedAt)

	expected := 0.5*0.6 + 0.1*0.8 + 0.1*statusScore + 0.1*impactScore + 0.1*priorityScore + 0.1*recencyScore
	if math.Abs(result.FinalScore-expected) > 1e-6 {
		t.Fatalf("expected final score %f, got %f", expected, result.FinalScore)
	}

	if result.ComponentScores["pagerank"] != 0.8 {
		t.Fatalf("expected pagerank component 0.8, got %f", result.ComponentScores["pagerank"])
	}
	if result.ComponentScores["impact"] != impactScore {
		t.Fatalf("expected impact component %f, got %f", impactScore, result.ComponentScores["impact"])
	}
}

func TestHybridScorer_Score_TextOnlyOnMissingMetrics(t *testing.T) {
	cache := &stubMetricsCache{missing: true}
	scorer := NewHybridScorer(Weights{TextRelevance: 1.0}, cache)

	result, err := scorer.Score("A", 0.42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalScore != 0.42 {
		t.Fatalf("expected text-only final score 0.42, got %f", result.FinalScore)
	}
	if len(result.ComponentScores) != 0 {
		t.Fatalf("expected no component scores on missing metrics")
	}
}

func TestHybridScorer_Configure(t *testing.T) {
	cache := &stubMetricsCache{}
	scorer := NewHybridScorer(Weights{TextRelevance: 1.0}, cache).(*hybridScorer)

	if err := scorer.Configure(Weights{TextRelevance: -1}); err == nil {
		t.Fatal("expected error for invalid weights")
	}

	if scorer.weights.TextRelevance != 1.0 {
		t.Fatalf("expected weights unchanged after invalid configure")
	}

	valid := Weights{
		TextRelevance: 0.4,
		PageRank:      0.2,
		Status:        0.1,
		Impact:        0.1,
		Priority:      0.1,
		Recency:       0.1,
	}
	if err := scorer.Configure(valid); err != nil {
		t.Fatalf("unexpected error for valid weights: %v", err)
	}
	if scorer.weights.TextRelevance != 0.4 {
		t.Fatalf("expected weights updated")
	}
}

func TestNewHybridScorer_FallsBackBeforeNormalizingInvalidWeights(t *testing.T) {
	scorer := NewHybridScorer(Weights{TextRelevance: -1, PageRank: -1}, nil).(*hybridScorer)
	expected, err := GetPreset(PresetDefault)
	if err != nil {
		t.Fatalf("default preset: %v", err)
	}
	if scorer.weights != expected {
		t.Fatalf("expected default weights, got %+v", scorer.weights)
	}
}

func TestHybridScorerAtUsesPinnedRecencyClock(t *testing.T) {
	reference := time.Date(2024, time.June, 30, 12, 0, 0, 0, time.UTC)
	cache := &stubMetricsCache{
		metrics: map[string]IssueMetrics{
			"A": {IssueID: "A", UpdatedAt: reference.Add(-30 * 24 * time.Hour)},
		},
	}
	scorer := NewHybridScorerAt(Weights{Recency: 1}, cache, reference)
	result, err := scorer.Score("A", 0)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got, want := result.ComponentScores["recency"], math.Exp(-1); math.Abs(got-want) > 1e-12 {
		t.Fatalf("recency component = %f, want %f", got, want)
	}
}

func TestHybridScorerAtCapturesZeroReferenceClockOnce(t *testing.T) {
	scorer := NewHybridScorerAt(Weights{Recency: 1}, nil, time.Time{}).(*hybridScorer)
	if scorer.now.IsZero() {
		t.Fatal("zero reference clock was not captured at construction")
	}
}
