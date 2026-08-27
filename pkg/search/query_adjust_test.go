package search

import (
	"math"
	"testing"
)

func TestAdjustWeightsForQuery_ShortQueryBoostsText(t *testing.T) {
	weights, err := GetPreset(PresetImpactFirst)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	adjusted := AdjustWeightsForQuery(weights, "benchmarks")
	if adjusted.TextRelevance < shortQueryMinTextWeight {
		t.Fatalf("expected text weight >= %.2f, got %.2f", shortQueryMinTextWeight, adjusted.TextRelevance)
	}
	if adjusted.TextRelevance <= weights.TextRelevance {
		t.Fatalf("expected text weight to increase for short query")
	}
	if adjusted.PageRank >= weights.PageRank {
		t.Fatalf("expected pagerank weight to decrease for short query")
	}
	sum := adjusted.sum()
	if math.Abs(sum-1.0) > 1e-6 {
		t.Fatalf("expected weights to sum to 1.0, got %.6f", sum)
	}
}

func TestAdjustWeightsForQuery_LongQueryNoChange(t *testing.T) {
	weights, err := GetPreset(PresetDefault)
	if err != nil {
		t.Fatalf("preset: %v", err)
	}
	query := "document steps to reproduce oauth login regression in staging"
	adjusted := AdjustWeightsForQuery(weights, query)
	if adjusted != weights {
		t.Fatalf("expected weights unchanged for long query")
	}
}

func TestHybridCandidateLimit(t *testing.T) {
	shortLimit := HybridCandidateLimit(5, 1000, "benchmarks")
	if shortLimit < hybridCandidateMinShort {
		t.Fatalf("expected short-query candidate limit >= %d, got %d", hybridCandidateMinShort, shortLimit)
	}
	longLimit := HybridCandidateLimit(5, 1000, "long descriptive query for hybrid search relevance")
	if longLimit < hybridCandidateMin {
		t.Fatalf("expected long-query candidate limit >= %d, got %d", hybridCandidateMin, longLimit)
	}
	capped := HybridCandidateLimit(5, 20, "benchmarks")
	if capped != 20 {
		t.Fatalf("expected candidate limit capped by total, got %d", capped)
	}
	if empty := HybridCandidateLimit(5, 0, "benchmarks"); empty != 0 {
		t.Fatalf("expected zero candidates for an empty index, got %d", empty)
	}
	maxInt := int(^uint(0) >> 1)
	if overflowSafe := HybridCandidateLimit(maxInt, 1000, "long descriptive query for ranking"); overflowSafe != 1000 {
		t.Fatalf("expected oversized limit to saturate at total 1000, got %d", overflowSafe)
	}
}
