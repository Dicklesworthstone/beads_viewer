package search

import (
	"fmt"
	"time"
)

type hybridScorer struct {
	weights Weights
	cache   MetricsCache
	now     time.Time
}

// NewHybridScorer creates a scorer with the given weights and metrics cache.
func NewHybridScorer(weights Weights, cache MetricsCache) HybridScorer {
	return NewHybridScorerAt(weights, cache, time.Now())
}

// NewHybridScorerAt creates a scorer whose recency component is evaluated at
// referenceTime. Historical robot searches use their snapshot commit time so
// identical snapshots do not drift as wall time advances.
func NewHybridScorerAt(weights Weights, cache MetricsCache, referenceTime time.Time) HybridScorer {
	if referenceTime.IsZero() {
		// Capture the fallback clock once. Leaving it zero would make
		// normalizeRecencyAt call time.Now independently for every Score call, so
		// one scorer could rank an unchanged result set against moving timestamps.
		referenceTime = time.Now()
	}
	normalized := defaultHybridWeights()
	if err := weights.validateComponents(); err == nil {
		normalized = weights.Normalize()
	}
	if err := normalized.Validate(); err != nil {
		normalized = defaultHybridWeights()
	}
	return &hybridScorer{
		weights: normalized,
		cache:   cache,
		now:     referenceTime,
	}
}

func (s *hybridScorer) Score(issueID string, textScore float64) (HybridScore, error) {
	if issueID == "" {
		return HybridScore{}, fmt.Errorf("issueID is required")
	}

	if s.cache == nil {
		return HybridScore{
			IssueID:    issueID,
			FinalScore: textScore,
			TextScore:  textScore,
		}, nil
	}

	metrics, found := s.cache.Get(issueID)
	if !found {
		return HybridScore{
			IssueID:    issueID,
			FinalScore: textScore,
			TextScore:  textScore,
		}, nil
	}

	// Skip normalizations for zero-weight components to save computation
	var statusScore, priorityScore, impactScore, recencyScore float64
	if s.weights.Status > 0 {
		statusScore = normalizeStatus(metrics.Status)
	}
	if s.weights.Priority > 0 {
		priorityScore = normalizePriority(metrics.Priority)
	}
	if s.weights.Impact > 0 {
		impactScore = normalizeImpact(metrics.BlockerCount, s.cache.MaxBlockerCount())
	}
	if s.weights.Recency > 0 {
		recencyScore = normalizeRecencyAt(metrics.UpdatedAt, s.now)
	}

	final := s.weights.TextRelevance*textScore +
		s.weights.PageRank*metrics.PageRank +
		s.weights.Status*statusScore +
		s.weights.Impact*impactScore +
		s.weights.Priority*priorityScore +
		s.weights.Recency*recencyScore

	return HybridScore{
		IssueID:    issueID,
		FinalScore: final,
		TextScore:  textScore,
		ComponentScores: map[string]float64{
			"pagerank": metrics.PageRank,
			"status":   statusScore,
			"impact":   impactScore,
			"priority": priorityScore,
			"recency":  recencyScore,
		},
	}, nil
}

func defaultHybridWeights() Weights {
	if preset, err := GetPreset(PresetDefault); err == nil {
		return preset
	}
	return Weights{TextRelevance: 1.0}
}

func (s *hybridScorer) Configure(weights Weights) error {
	// Validate raw weights first (catches negative values before normalization can mask them)
	if err := weights.Validate(); err != nil {
		return err
	}
	s.weights = weights.Normalize()
	return nil
}

func (s *hybridScorer) GetWeights() Weights {
	return s.weights
}
