package baseline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBaselineSaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, ".bv", "baseline.json")

	original := &Baseline{
		Version:       CurrentVersion,
		CreatedAt:     time.Now().Truncate(time.Second),
		CommitSHA:     "abc123def456",
		CommitMessage: "Test commit",
		Branch:        "main",
		Description:   "Test baseline",
		Stats: GraphStats{
			NodeCount:       100,
			EdgeCount:       250,
			Density:         0.025,
			OpenCount:       50,
			ClosedCount:     40,
			BlockedCount:    10,
			CycleCount:      2,
			ActionableCount: 30,
		},
		TopMetrics: TopMetrics{
			PageRank: []MetricItem{
				{ID: "TASK-1", Value: 0.15},
				{ID: "TASK-2", Value: 0.12},
			},
			Betweenness: []MetricItem{
				{ID: "TASK-3", Value: 0.8},
			},
		},
		Cycles: [][]string{
			{"A", "B", "C", "A"},
		},
	}

	// Save
	if err := original.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if !Exists(path) {
		t.Error("baseline file should exist after save")
	}

	// Load
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Compare
	if loaded.Version != original.Version {
		t.Errorf("version mismatch: got %d, want %d", loaded.Version, original.Version)
	}

	if loaded.CommitSHA != original.CommitSHA {
		t.Errorf("commit SHA mismatch: got %s, want %s", loaded.CommitSHA, original.CommitSHA)
	}

	if loaded.Stats.NodeCount != original.Stats.NodeCount {
		t.Errorf("node count mismatch: got %d, want %d",
			loaded.Stats.NodeCount, original.Stats.NodeCount)
	}

	if loaded.Stats.Density != original.Stats.Density {
		t.Errorf("density mismatch: got %f, want %f",
			loaded.Stats.Density, original.Stats.Density)
	}

	if len(loaded.TopMetrics.PageRank) != len(original.TopMetrics.PageRank) {
		t.Errorf("pagerank count mismatch: got %d, want %d",
			len(loaded.TopMetrics.PageRank), len(original.TopMetrics.PageRank))
	}

	if len(loaded.Cycles) != 1 {
		t.Errorf("cycles count mismatch: got %d, want 1", len(loaded.Cycles))
	}
}

func TestLoadNonExistent(t *testing.T) {
	_, err := Load("/nonexistent/path/baseline.json")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestExistsNonExistent(t *testing.T) {
	if Exists("/nonexistent/path/baseline.json") {
		t.Error("Exists should return false for non-existent file")
	}
}

func TestDefaultPath(t *testing.T) {
	path := DefaultPath("/project")
	expected := filepath.Join("/project", ".bv", "baseline.json")
	if path != expected {
		t.Errorf("got %s, want %s", path, expected)
	}
}

func TestBaselineSummary(t *testing.T) {
	b := &Baseline{
		Version:       CurrentVersion,
		CreatedAt:     time.Date(2025, 11, 30, 10, 0, 0, 0, time.UTC),
		CommitSHA:     "abc123def456789",
		CommitMessage: "Fix the bug",
		Branch:        "feature-x",
		Description:   "Before refactoring",
		Stats: GraphStats{
			NodeCount:       100,
			EdgeCount:       200,
			Density:         0.02,
			OpenCount:       60,
			ClosedCount:     30,
			BlockedCount:    10,
			ActionableCount: 45,
			CycleCount:      1,
		},
		TopMetrics: TopMetrics{
			PageRank: []MetricItem{
				{ID: "TASK-1", Value: 0.15},
				{ID: "TASK-2", Value: 0.10},
			},
		},
	}

	summary := b.Summary()

	// Check that key info is present
	if !strings.Contains(summary, "abc123de") {
		t.Error("summary should contain shortened commit SHA")
	}
	if !strings.Contains(summary, "feature-x") {
		t.Error("summary should contain branch name")
	}
	if !strings.Contains(summary, "100 nodes") {
		t.Error("summary should contain node count")
	}
	if !strings.Contains(summary, "TASK-1") {
		t.Error("summary should contain top PageRank item")
	}
}

func TestBaselineSummaryIncludesAllTopMetrics(t *testing.T) {
	b := &Baseline{
		Version:   CurrentVersion,
		CreatedAt: time.Date(2025, 11, 30, 10, 0, 0, 0, time.UTC),
		Stats:     GraphStats{NodeCount: 10, EdgeCount: 12},
		TopMetrics: TopMetrics{
			PageRank:     []MetricItem{{ID: "PR-1", Value: 0.25}},
			Betweenness:  []MetricItem{{ID: "BW-1", Value: 0.40}},
			CriticalPath: []MetricItem{{ID: "CP-1", Value: 4.0}},
			Hubs:         []MetricItem{{ID: "HB-1", Value: 0.60}},
			Authorities:  []MetricItem{{ID: "AU-1", Value: 0.70}},
		},
	}

	summary := b.Summary()
	for _, token := range []string{
		"Top PageRank:", "PR-1",
		"Top Betweenness:", "BW-1",
		"Top Critical Path:", "CP-1",
		"Top Hubs:", "HB-1",
		"Top Authorities:", "AU-1",
	} {
		if !strings.Contains(summary, token) {
			t.Errorf("expected baseline summary to contain %q, got:\n%s", token, summary)
		}
	}
}

func TestNew(t *testing.T) {
	stats := GraphStats{
		NodeCount: 50,
		EdgeCount: 100,
	}
	top := TopMetrics{
		PageRank: []MetricItem{{ID: "X", Value: 0.5}},
	}
	cycles := [][]string{{"A", "B", "A"}}

	b := New(stats, top, cycles, "Test description")

	if b.Version != CurrentVersion {
		t.Errorf("version should be %d, got %d", CurrentVersion, b.Version)
	}

	if b.Stats.NodeCount != 50 {
		t.Errorf("node count should be 50, got %d", b.Stats.NodeCount)
	}

	if b.Description != "Test description" {
		t.Errorf("description mismatch: got %s", b.Description)
	}

	if b.CreatedAt.IsZero() {
		t.Error("created_at should be set")
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	deepPath := filepath.Join(tmpDir, "a", "b", "c", "baseline.json")

	b := &Baseline{
		Version:   CurrentVersion,
		CreatedAt: time.Now(),
		Stats:     GraphStats{NodeCount: 1},
	}

	if err := b.Save(deepPath); err != nil {
		t.Fatalf("Save should create directories: %v", err)
	}

	if !Exists(deepPath) {
		t.Error("file should exist after save")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalid.json")

	// Write invalid JSON
	if err := os.WriteFile(path, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestNewForDir_HonorsSourceDateEpoch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	b := NewForDir(tmpDir, GraphStats{NodeCount: 3}, TopMetrics{}, nil, "deterministic")
	want := time.Unix(1700000000, 0).UTC()
	if !b.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %s, want %s", b.CreatedAt.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
	if b.CreatedAt.Location() != time.UTC {
		t.Fatalf("expected UTC location, got %v", b.CreatedAt.Location())
	}

	// Invalid / negative SOURCE_DATE_EPOCH falls back to current UTC wall clock.
	t.Setenv("SOURCE_DATE_EPOCH", "-5")
	before := time.Now().UTC().Add(-2 * time.Second)
	bFallback := NewForDir(tmpDir, GraphStats{NodeCount: 1}, TopMetrics{}, nil, "fallback")
	after := time.Now().UTC().Add(2 * time.Second)
	if bFallback.CreatedAt.Before(before) || bFallback.CreatedAt.After(after) {
		t.Fatalf("expected fallback CreatedAt in [%s, %s], got %s", before, after, bFallback.CreatedAt)
	}

	// Year >= 10000 (>= 253402300800) must also fall back to wall-clock time so json.Marshal never fails.
	for _, huge := range []string{"253402300800", "9999999999999"} {
		t.Setenv("SOURCE_DATE_EPOCH", huge)
		bHuge := NewForDir(tmpDir, GraphStats{NodeCount: 1}, TopMetrics{}, nil, "huge-epoch")
		if bHuge.CreatedAt.Year() < 0 || bHuge.CreatedAt.Year() >= 10000 {
			t.Fatalf("SOURCE_DATE_EPOCH=%s produced unencodable year %d", huge, bHuge.CreatedAt.Year())
		}
		if _, err := json.Marshal(bHuge); err != nil {
			t.Fatalf("json.Marshal failed for SOURCE_DATE_EPOCH=%s: %v", huge, err)
		}
	}
}
