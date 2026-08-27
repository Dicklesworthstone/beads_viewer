package search

import (
	"math"
	"testing"
	"time"
)

func TestNormalizeStatus(t *testing.T) {
	cases := map[string]float64{
		"open":        1.0,
		"in_progress": 0.8,
		"hooked":      0.8,
		"review":      0.8,
		"pinned":      0.7,
		"blocked":     0.5,
		"draft":       0.2,
		"deferred":    0.2,
		"closed":      0.1,
		"tombstone":   0.0,
		"unknown":     0.5,
		" OPEN ":      1.0,
		"Review":      0.8,
	}
	for status, expected := range cases {
		if got := normalizeStatus(status); got != expected {
			t.Fatalf("status %q: expected %f, got %f", status, expected, got)
		}
	}
}

func TestNormalizePriority(t *testing.T) {
	cases := map[int]float64{
		0: 1.0,
		1: 0.8,
		2: 0.6,
		3: 0.4,
		4: 0.2,
		9: 0.5,
	}
	for priority, expected := range cases {
		if got := normalizePriority(priority); got != expected {
			t.Fatalf("priority %d: expected %f, got %f", priority, expected, got)
		}
	}
}

func TestNormalizeImpact(t *testing.T) {
	if got := normalizeImpact(1, 0); got != 0.5 {
		t.Fatalf("expected neutral impact for max=0, got %f", got)
	}
	if got := normalizeImpact(0, 5); got != 0 {
		t.Fatalf("expected 0 for blockerCount=0, got %f", got)
	}
	if got := normalizeImpact(5, 5); got != 1.0 {
		t.Fatalf("expected 1.0 for blockerCount=max, got %f", got)
	}
	if got := normalizeImpact(2, 4); got != 0.5 {
		t.Fatalf("expected 0.5 for blockerCount=2 max=4, got %f", got)
	}
}

func TestNormalizeRecency(t *testing.T) {
	if got := normalizeRecency(time.Time{}); got != 0.5 {
		t.Fatalf("expected neutral recency for zero time, got %f", got)
	}

	now := time.Now()
	if got := normalizeRecency(now); math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("expected recency ~1.0 for now, got %f", got)
	}

	thirtyDaysAgo := now.Add(-30 * 24 * time.Hour)
	expected := math.Exp(-1)
	if got := normalizeRecency(thirtyDaysAgo); math.Abs(got-expected) > 1e-6 {
		t.Fatalf("expected recency %f for 30 days ago, got %f", expected, got)
	}

	future := now.Add(24 * time.Hour)
	if got := normalizeRecency(future); math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("expected recency 1.0 for future time, got %f", got)
	}
}

func TestNormalizeRecencyAtUsesPinnedClock(t *testing.T) {
	reference := time.Date(2024, time.June, 30, 12, 0, 0, 0, time.UTC)
	updated := reference.Add(-30 * 24 * time.Hour)
	if got, want := normalizeRecencyAt(updated, reference), math.Exp(-1); math.Abs(got-want) > 1e-12 {
		t.Fatalf("normalizeRecencyAt() = %f, want %f", got, want)
	}
	if got := normalizeRecencyAt(reference.Add(time.Hour), reference); got != 1 {
		t.Fatalf("future update score = %f, want 1", got)
	}
}
