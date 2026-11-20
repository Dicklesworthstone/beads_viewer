package correlation

import (
	"strings"
	"testing"
	"time"
)

func TestValidateConfidence(t *testing.T) {
	s := NewScorer()

	tests := []struct {
		method     CorrelationMethod
		confidence float64
		want       bool
	}{
		// CoCommitted: 0.80-0.99 (matches CoCommitExtractor.calculateConfidence min 0.95 - 0.10 - 0.05 = 0.80)
		{MethodCoCommitted, 0.90, true},
		{MethodCoCommitted, 0.85, true},
		{MethodCoCommitted, 0.80, true},
		{MethodCoCommitted, 0.99, true},
		{MethodCoCommitted, 0.79, false},
		{MethodCoCommitted, 1.00, false},

		// ExplicitID: 0.70-0.99
		{MethodExplicitID, 0.90, true},
		{MethodExplicitID, 0.70, true},
		{MethodExplicitID, 0.69, false},

		// TemporalAuthor: 0.20-0.85
		{MethodTemporalAuthor, 0.50, true},
		{MethodTemporalAuthor, 0.20, true},
		{MethodTemporalAuthor, 0.85, true},
		{MethodTemporalAuthor, 0.19, false},
		{MethodTemporalAuthor, 0.86, false},

		// Unknown method - any valid range
		{CorrelationMethod("unknown"), 0.50, true},
		{CorrelationMethod("unknown"), 1.01, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			got := s.ValidateConfidence(tt.method, tt.confidence)
			if got != tt.want {
				t.Errorf("ValidateConfidence(%s, %.2f) = %v, want %v", tt.method, tt.confidence, got, tt.want)
			}
		})
	}
}

func TestCombineConfidence_SingleSignal(t *testing.T) {
	s := NewScorer()

	signals := []ConfidenceSignal{
		{Method: MethodCoCommitted, Confidence: 0.95, Reason: "test"},
	}

	got := s.CombineConfidence(signals)
	if got != 0.95 {
		t.Errorf("CombineConfidence(single) = %v, want 0.95", got)
	}
}

func TestCombineConfidence_Empty(t *testing.T) {
	s := NewScorer()

	got := s.CombineConfidence(nil)
	if got != 0.0 {
		t.Errorf("CombineConfidence(empty) = %v, want 0.0", got)
	}
}

func TestCombineConfidence_MultipleSignals(t *testing.T) {
	s := NewScorer()

	tests := []struct {
		name      string
		signals   []ConfidenceSignal
		wantRange [2]float64
	}{
		{
			name: "two high signals",
			signals: []ConfidenceSignal{
				{Method: MethodCoCommitted, Confidence: 0.95},
				{Method: MethodExplicitID, Confidence: 0.90},
			},
			wantRange: [2]float64{0.95, 0.99}, // Boosted from 0.95
		},
		{
			name: "high and low signals",
			signals: []ConfidenceSignal{
				{Method: MethodCoCommitted, Confidence: 0.95},
				{Method: MethodTemporalAuthor, Confidence: 0.40},
			},
			wantRange: [2]float64{0.95, 0.98}, // Small boost from low signal
		},
		{
			name: "three signals",
			signals: []ConfidenceSignal{
				{Method: MethodCoCommitted, Confidence: 0.95},
				{Method: MethodExplicitID, Confidence: 0.90},
				{Method: MethodTemporalAuthor, Confidence: 0.60},
			},
			wantRange: [2]float64{0.95, 0.99}, // Small boost from additional signals
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.CombineConfidence(tt.signals)
			if got < tt.wantRange[0] || got > tt.wantRange[1] {
				t.Errorf("CombineConfidence() = %v, want in range [%v, %v]", got, tt.wantRange[0], tt.wantRange[1])
			}
		})
	}
}

func TestCombineConfidence_NeverExceed99(t *testing.T) {
	s := NewScorer()

	// Even with many high signals, should cap at 0.99
	signals := []ConfidenceSignal{
		{Confidence: 0.99},
		{Confidence: 0.99},
		{Confidence: 0.99},
		{Confidence: 0.99},
	}

	got := s.CombineConfidence(signals)
	if got > 0.99 {
		t.Errorf("CombineConfidence() = %v, should not exceed 0.99", got)
	}
}

func TestCombineReasons_SingleReason(t *testing.T) {
	s := NewScorer()

	signals := []ConfidenceSignal{
		{Reason: "Test reason"},
	}

	got := s.CombineReasons(signals)
	if got != "Test reason" {
		t.Errorf("CombineReasons(single) = %q, want %q", got, "Test reason")
	}
}

func TestCombineReasons_MultipleReasons(t *testing.T) {
	s := NewScorer()

	signals := []ConfidenceSignal{
		{Confidence: 0.90, Reason: "First"},
		{Confidence: 0.95, Reason: "Second"},
	}

	got := s.CombineReasons(signals)

	// Should contain "Multiple signals" prefix
	if !strings.HasPrefix(got, "Multiple signals:") {
		t.Errorf("CombineReasons() = %q, should start with 'Multiple signals:'", got)
	}

	// Should contain both reasons
	if !strings.Contains(got, "First") || !strings.Contains(got, "Second") {
		t.Errorf("CombineReasons() = %q, should contain both reasons", got)
	}

	// Higher confidence should come first
	firstIdx := strings.Index(got, "Second")
	secondIdx := strings.Index(got, "First")
	if firstIdx > secondIdx {
		t.Errorf("CombineReasons() = %q, higher confidence should come first", got)
	}
}

func TestExplainConfidence(t *testing.T) {
	s := NewScorer()

	tests := []struct {
		method     CorrelationMethod
		confidence float64
		details    string
		contains   []string
	}{
		{
			method:     MethodCoCommitted,
			confidence: 0.98,
			details:    "1 file changed",
			contains:   []string{"confidence", "98%", "same commit", "1 file changed"},
		},
		{
			method:     MethodTemporalAuthor,
			confidence: 0.40,
			details:    "",
			contains:   []string{"confidence", "40%", "author"},
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			got := s.ExplainConfidence(tt.method, tt.confidence, tt.details)

			for _, want := range tt.contains {
				if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
					t.Errorf("ExplainConfidence() = %q, should contain %q", got, want)
				}
			}
		})
	}
}

func TestFilterByConfidence(t *testing.T) {
	s := NewScorer()

	commits := []CorrelatedCommit{
		{SHA: "aaa", Confidence: 0.90},
		{SHA: "bbb", Confidence: 0.50},
		{SHA: "ccc", Confidence: 0.30},
		{SHA: "ddd", Confidence: 0.80},
	}

	tests := []struct {
		minConf  float64
		wantSHAs []string
	}{
		{0.0, []string{"aaa", "bbb", "ccc", "ddd"}},
		{0.5, []string{"aaa", "bbb", "ddd"}},
		{0.8, []string{"aaa", "ddd"}},
		{0.95, []string{}},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := s.FilterByConfidence(commits, tt.minConf)

			if len(got) != len(tt.wantSHAs) {
				t.Errorf("FilterByConfidence(%.2f) got %d commits, want %d", tt.minConf, len(got), len(tt.wantSHAs))
			}
		})
	}
}

func TestMergeCommits_NoDuplicates(t *testing.T) {
	s := NewScorer()

	source1 := []CorrelatedCommit{
		{SHA: "aaa", Confidence: 0.90, Method: MethodCoCommitted},
	}
	source2 := []CorrelatedCommit{
		{SHA: "bbb", Confidence: 0.80, Method: MethodExplicitID},
	}

	got := s.MergeCommits(source1, source2)

	if len(got) != 2 {
		t.Errorf("MergeCommits() got %d commits, want 2", len(got))
	}
}

func TestMergeCommits_WithDuplicates(t *testing.T) {
	s := NewScorer()

	source1 := []CorrelatedCommit{
		{SHA: "aaa", Confidence: 0.90, Method: MethodCoCommitted, Reason: "Co-committed"},
	}
	source2 := []CorrelatedCommit{
		{SHA: "aaa", Confidence: 0.85, Method: MethodExplicitID, Reason: "Explicit ID"},
	}

	got := s.MergeCommits(source1, source2)

	if len(got) != 1 {
		t.Fatalf("MergeCommits() got %d commits, want 1", len(got))
	}

	// Combined confidence should be higher than either individual
	if got[0].Confidence <= 0.90 {
		t.Errorf("MergeCommits() confidence = %v, should be > 0.90", got[0].Confidence)
	}

	// Reason should mention multiple signals
	if !strings.Contains(got[0].Reason, "Multiple") {
		t.Errorf("MergeCommits() reason = %q, should mention multiple signals", got[0].Reason)
	}
}

func TestMergeCommits_SortedByConfidence(t *testing.T) {
	s := NewScorer()

	source := []CorrelatedCommit{
		{SHA: "low", Confidence: 0.40, Method: MethodTemporalAuthor},
		{SHA: "high", Confidence: 0.95, Method: MethodCoCommitted},
		{SHA: "mid", Confidence: 0.70, Method: MethodExplicitID},
	}

	got := s.MergeCommits(source)

	// Should be sorted descending by confidence
	for i := 0; i < len(got)-1; i++ {
		if got[i].Confidence < got[i+1].Confidence {
			t.Errorf("MergeCommits() not sorted by confidence: %v", got)
		}
	}
}

func TestCalculateStats(t *testing.T) {
	s := NewScorer()

	commits := []CorrelatedCommit{
		{SHA: "a", Method: MethodCoCommitted, Confidence: 0.95},
		{SHA: "b", Method: MethodCoCommitted, Confidence: 0.90},
		{SHA: "c", Method: MethodExplicitID, Confidence: 0.85},
		{SHA: "d", Method: MethodTemporalAuthor, Confidence: 0.40},
	}

	stats := s.CalculateStats(commits)

	if stats.Total != 4 {
		t.Errorf("Total = %d, want 4", stats.Total)
	}

	if stats.ByMethod["co_committed"] != 2 {
		t.Errorf("ByMethod[co_committed] = %d, want 2", stats.ByMethod["co_committed"])
	}

	if stats.ByMethod["explicit_id"] != 1 {
		t.Errorf("ByMethod[explicit_id] = %d, want 1", stats.ByMethod["explicit_id"])
	}

	// Check confidence groups
	if stats.ByConfidenceGrp["high"] != 3 { // 0.95, 0.90, 0.85 are >= 0.8
		t.Errorf("ByConfidenceGrp[high] = %d, want 3", stats.ByConfidenceGrp["high"])
	}

	if stats.ByConfidenceGrp["low"] != 1 { // 0.40 is < 0.5
		t.Errorf("ByConfidenceGrp[low] = %d, want 1", stats.ByConfidenceGrp["low"])
	}

	// Check averages
	avgCoCommitted := stats.AverageByMethod["co_committed"]
	expectedAvg := (0.95 + 0.90) / 2
	if avgCoCommitted != expectedAvg {
		t.Errorf("AverageByMethod[co_committed] = %v, want %v", avgCoCommitted, expectedAvg)
	}
}

func TestScorer_CalculateStats_Empty(t *testing.T) {
	s := NewScorer()

	stats := s.CalculateStats(nil)

	if stats.Total != 0 {
		t.Errorf("Total = %d, want 0", stats.Total)
	}
}

func TestConfidenceLevel(t *testing.T) {
	tests := []struct {
		confidence float64
		want       string
	}{
		{0.95, "very high"},
		{0.80, "high"},
		{0.60, "moderate"},
		{0.35, "low"},
		{0.20, "very low"},
	}

	for _, tt := range tests {
		got := ConfidenceLevel(tt.confidence)
		if got != tt.want {
			t.Errorf("ConfidenceLevel(%v) = %q, want %q", tt.confidence, got, tt.want)
		}
	}
}

func TestFormatConfidence(t *testing.T) {
	tests := []struct {
		confidence float64
		want       string
	}{
		{0.95, "95%"},
		{0.5, "50%"},
		{0.0, "0%"},
		{1.0, "100%"},
	}

	for _, tt := range tests {
		got := FormatConfidence(tt.confidence)
		if got != tt.want {
			t.Errorf("FormatConfidence(%v) = %q, want %q", tt.confidence, got, tt.want)
		}
	}
}

func TestFilterHistoriesByConfidence(t *testing.T) {
	s := NewScorer()

	now := time.Now()
	histories := map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Commits: []CorrelatedCommit{
				{SHA: "a", Confidence: 0.95, Timestamp: now},
				{SHA: "b", Confidence: 0.40, Timestamp: now},
			},
		},
		"bv-2": {
			BeadID: "bv-2",
			Commits: []CorrelatedCommit{
				{SHA: "c", Confidence: 0.30, Timestamp: now},
			},
		},
	}

	filtered := s.FilterHistoriesByConfidence(histories, 0.5)

	// bv-1 should have only 1 commit left
	if len(filtered["bv-1"].Commits) != 1 {
		t.Errorf("bv-1 commits = %d, want 1", len(filtered["bv-1"].Commits))
	}

	// bv-2 should have no commits
	if len(filtered["bv-2"].Commits) != 0 {
		t.Errorf("bv-2 commits = %d, want 0", len(filtered["bv-2"].Commits))
	}
}

func TestMergeCommits_FilesDeduped(t *testing.T) {
	s := NewScorer()

	source1 := []CorrelatedCommit{
		{
			SHA:        "aaa",
			Confidence: 0.90,
			Method:     MethodCoCommitted,
			Files: []FileChange{
				{Path: "file1.go"},
				{Path: "file2.go"},
			},
		},
	}
	source2 := []CorrelatedCommit{
		{
			SHA:        "aaa",
			Confidence: 0.85,
			Method:     MethodExplicitID,
			Files: []FileChange{
				{Path: "file2.go"}, // Duplicate
				{Path: "file3.go"},
			},
		},
	}

	got := s.MergeCommits(source1, source2)

	if len(got) != 1 {
		t.Fatalf("MergeCommits() got %d commits, want 1", len(got))
	}

	// Should have 3 unique files
	if len(got[0].Files) != 3 {
		t.Errorf("MergeCommits() files = %d, want 3", len(got[0].Files))
	}
}
