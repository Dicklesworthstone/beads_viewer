package search

import (
	"bytes"
	"log"
	"math"
	"testing"
)

func TestWeightsValidate_Presets(t *testing.T) {
	for _, preset := range ListPresets() {
		weights, err := GetPreset(preset)
		if err != nil {
			t.Fatalf("expected preset %q, got error: %v", preset, err)
		}
		if err := weights.Validate(); err != nil {
			t.Fatalf("preset %q should validate, got error: %v", preset, err)
		}
	}
}

func TestWeightsValidate_Negative(t *testing.T) {
	weights := Weights{
		TextRelevance: -0.1,
		PageRank:      0.4,
		Status:        0.2,
		Impact:        0.2,
		Priority:      0.2,
		Recency:       0.1,
	}
	if err := weights.Validate(); err == nil {
		t.Fatal("expected error for negative weights")
	}
}

func TestWeightsValidate_NonFinite(t *testing.T) {
	cases := []struct {
		name    string
		weights Weights
	}{
		{
			name: "nan",
			weights: Weights{
				TextRelevance: math.NaN(),
				PageRank:      1.0,
			},
		},
		{
			name: "infinity",
			weights: Weights{
				TextRelevance: math.Inf(1),
				PageRank:      1.0,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.weights.Validate(); err == nil {
				t.Fatal("expected error for non-finite weight")
			}
		})
	}
}

func TestWeightsValidate_SumTolerance(t *testing.T) {
	weights := Weights{
		TextRelevance: 0.2,
		PageRank:      0.2,
		Status:        0.2,
		Impact:        0.2,
		Priority:      0.2,
		Recency:       0.2,
	}
	if err := weights.Validate(); err == nil {
		t.Fatal("expected error for weights summing above tolerance")
	}
}

func TestWeightsValidate_LowTextWarns(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(orig)
	})

	weights := Weights{
		TextRelevance: 0.05,
		PageRank:      0.20,
		Status:        0.20,
		Impact:        0.20,
		Priority:      0.20,
		Recency:       0.15,
	}
	if err := weights.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected silent validation without raw log output, got %q", buf.String())
	}
}

func TestWeightsNormalize(t *testing.T) {
	weights := Weights{
		TextRelevance: 1,
		PageRank:      2,
		Status:        3,
		Impact:        4,
		Priority:      5,
		Recency:       6,
	}
	normalized := weights.Normalize()
	sum := normalized.TextRelevance + normalized.PageRank + normalized.Status + normalized.Impact + normalized.Priority + normalized.Recency
	if math.Abs(sum-1.0) > 1e-9 {
		t.Fatalf("expected normalized weights to sum to 1.0, got %f", sum)
	}
}

func TestWeightsNormalize_ZeroSum(t *testing.T) {
	weights := Weights{}
	normalized := weights.Normalize()
	if normalized != weights {
		t.Fatalf("expected zero-sum weights to remain unchanged")
	}
}
