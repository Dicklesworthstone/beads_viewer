// Package correlation provides confidence scoring for bead-commit correlations.
package correlation

import (
	"fmt"
	"sort"
	"strings"
)

// ConfidenceRange defines the expected confidence bounds for a correlation method.
type ConfidenceRange struct {
	Method CorrelationMethod
	Min    float64
	Max    float64
	Desc   string // Human-readable description
}

// MethodRanges defines the expected confidence ranges for each correlation method.
var MethodRanges = map[CorrelationMethod]ConfidenceRange{
	MethodCoCommitted: {
		Method: MethodCoCommitted,
		Min:    0.85,
		Max:    0.99,
		Desc:   "Changed in same commit as bead update (direct causation)",
	},
	MethodExplicitID: {
		Method: MethodExplicitID,
		Min:    0.70,
		Max:    0.99,
		Desc:   "Commit message explicitly references bead ID (developer intent)",
	},
	MethodTemporalAuthor: {
		Method: MethodTemporalAuthor,
		Min:    0.20,
		Max:    0.85,
		Desc:   "By same author during bead's active window (temporal correlation)",
	},
}

// Scorer provides methods for calculating and combining confidence scores.
type Scorer struct{}

// NewScorer creates a new confidence scorer.
func NewScorer() *Scorer {
	return &Scorer{}
}

// ValidateConfidence checks if a confidence score is within expected range for the method.
func (s *Scorer) ValidateConfidence(method CorrelationMethod, confidence float64) bool {
	r, ok := MethodRanges[method]
	if !ok {
		return confidence >= 0.0 && confidence <= 1.0
	}
	return confidence >= r.Min && confidence <= r.Max
}

// ConfidenceSignal represents a single confidence signal from a correlation method.
type ConfidenceSignal struct {
	Method     CorrelationMethod
	Confidence float64
	Reason     string
}

// CombineConfidence combines multiple confidence signals into a single score.
// Uses the highest confidence as base and boosts slightly for additional signals.
func (s *Scorer) CombineConfidence(signals []ConfidenceSignal) float64 {
	if len(signals) == 0 {
		return 0.0
	}

	if len(signals) == 1 {
		return signals[0].Confidence
	}

	// Extract confidence values and sort descending
	scores := make([]float64, len(signals))
	for i, sig := range signals {
		scores[i] = sig.Confidence
	}
	sort.Float64s(scores)

	// Reverse to get descending order
	for i, j := 0, len(scores)-1; i < j; i, j = i+1, j-1 {
		scores[i], scores[j] = scores[j], scores[i]
	}

	// Use highest as base
	base := scores[0]

	// Boost for each additional signal
	// Each additional signal provides a diminishing boost
	for i := 1; i < len(scores); i++ {
		// Boost is proportional to remaining headroom and the signal strength
		headroom := 1.0 - base
		boost := headroom * 0.1 * scores[i]
		base += boost
	}

	// Cap at 0.99 (never be 100% confident)
	if base > 0.99 {
		base = 0.99
	}

	return base
}

// CombineReasons creates a combined human-readable reason from multiple signals.
func (s *Scorer) CombineReasons(signals []ConfidenceSignal) string {
	if len(signals) == 0 {
		return ""
	}

	if len(signals) == 1 {
		return signals[0].Reason
	}

	// Sort by confidence descending
	sorted := make([]ConfidenceSignal, len(signals))
	copy(sorted, signals)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Confidence > sorted[j].Confidence
	})

	// Build combined reason
	parts := make([]string, 0, len(sorted))
	for _, sig := range sorted {
		parts = append(parts, fmt.Sprintf("%s (%.0f%%)", sig.Reason, sig.Confidence*100))
	}

	return "Multiple signals: " + strings.Join(parts, "; ")
}

// ExplainConfidence generates a human-readable explanation for a confidence score.
func (s *Scorer) ExplainConfidence(method CorrelationMethod, confidence float64, details string) string {
	r, ok := MethodRanges[method]
	if !ok {
		return fmt.Sprintf("Unknown method: %.0f%% confidence", confidence*100)
	}

	// Determine confidence level
	var level string
	range_ := r.Max - r.Min
	if range_ == 0 {
		range_ = 1.0 // Avoid division by zero
	}
	normalized := (confidence - r.Min) / range_
	if normalized < 0 {
		normalized = 0
	}
	if normalized > 1 {
		normalized = 1
	}

	switch {
	case normalized >= 0.8:
		level = "very high"
	case normalized >= 0.6:
		level = "high"
	case normalized >= 0.4:
		level = "moderate"
	case normalized >= 0.2:
		level = "low"
	default:
		level = "very low"
	}

	// Capitalize first letter (avoiding deprecated strings.Title)
	levelTitle := strings.ToUpper(level[:1]) + level[1:]
	base := fmt.Sprintf("%s confidence (%.0f%%): %s", levelTitle, confidence*100, r.Desc)
	if details != "" {
		base += ". " + details
	}

	return base
}

// FilterByConfidence filters commits by minimum confidence threshold.
func (s *Scorer) FilterByConfidence(commits []CorrelatedCommit, minConfidence float64) []CorrelatedCommit {
	if minConfidence <= 0 {
		if commits == nil {
			return []CorrelatedCommit{}
		}
		return commits
	}

	filtered := make([]CorrelatedCommit, 0, len(commits))
	for _, c := range commits {
		if c.Confidence >= minConfidence {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// FilterHistoriesByConfidence filters bead histories, removing low-confidence commits.
func (s *Scorer) FilterHistoriesByConfidence(histories map[string]BeadHistory, minConfidence float64) map[string]BeadHistory {
	if minConfidence <= 0 {
		return histories
	}

	filtered := make(map[string]BeadHistory)
	for id, h := range histories {
		h.Commits = s.FilterByConfidence(h.Commits, minConfidence)
		filtered[id] = h
	}
	return filtered
}

// MergeCommits combines commits from multiple sources, deduplicating by SHA+BeadID
// and combining signals when the same commit is found via multiple methods.
func (s *Scorer) MergeCommits(sources ...[]CorrelatedCommit) []CorrelatedCommit {
	// Group by SHA + BeadID
	type key struct {
		SHA    string
		BeadID string
	}
	byKey := make(map[key][]CorrelatedCommit)
	for _, commits := range sources {
		for _, c := range commits {
			k := key{SHA: c.SHA, BeadID: c.BeadID}
			byKey[k] = append(byKey[k], c)
		}
	}

	// Merge duplicates
	var merged []CorrelatedCommit
	for _, commits := range byKey {
		if len(commits) == 1 {
			merged = append(merged, commits[0])
			continue
		}

		// Multiple methods found this commit - combine signals
		signals := make([]ConfidenceSignal, len(commits))
		for i, c := range commits {
			signals[i] = ConfidenceSignal{
				Method:     c.Method,
				Confidence: c.Confidence,
				Reason:     c.Reason,
			}
		}

		// Use the first commit as base and update confidence/reason
		result := commits[0]
		result.Confidence = s.CombineConfidence(signals)
		result.Reason = s.CombineReasons(signals)

		// If multiple methods, note it in the method field description
		// Keep the highest-confidence method as the primary
		highestIdx := 0
		for i, sig := range signals {
			if sig.Confidence > signals[highestIdx].Confidence {
				highestIdx = i
			}
		}
		result.Method = commits[highestIdx].Method

		// Merge file changes (dedupe by path)
		seenFiles := make(map[string]bool)
		var allFiles []FileChange
		for _, c := range commits {
			for _, f := range c.Files {
				if !seenFiles[f.Path] {
					seenFiles[f.Path] = true
					allFiles = append(allFiles, f)
				}
			}
		}
		result.Files = allFiles

		merged = append(merged, result)
	}

	// Sort by confidence descending
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Confidence > merged[j].Confidence
	})

	return merged
}

// ConfidenceStats provides aggregate statistics about confidence scores.
type ConfidenceStats struct {
	Total           int                `json:"total"`
	ByMethod        map[string]int     `json:"by_method"`
	ByConfidenceGrp map[string]int     `json:"by_confidence_group"` // high/medium/low
	AverageByMethod map[string]float64 `json:"average_by_method"`
}

// CalculateStats computes aggregate confidence statistics.
func (s *Scorer) CalculateStats(commits []CorrelatedCommit) ConfidenceStats {
	stats := ConfidenceStats{
		Total:           len(commits),
		ByMethod:        make(map[string]int),
		ByConfidenceGrp: make(map[string]int),
		AverageByMethod: make(map[string]float64),
	}

	if len(commits) == 0 {
		return stats
	}

	// Track sums for averages
	sumByMethod := make(map[string]float64)

	for _, c := range commits {
		method := c.Method.String()
		stats.ByMethod[method]++
		sumByMethod[method] += c.Confidence

		// Categorize by confidence level
		switch {
		case c.Confidence >= 0.8:
			stats.ByConfidenceGrp["high"]++
		case c.Confidence >= 0.5:
			stats.ByConfidenceGrp["medium"]++
		default:
			stats.ByConfidenceGrp["low"]++
		}
	}

	// Calculate averages
	for method, sum := range sumByMethod {
		count := stats.ByMethod[method]
		if count > 0 {
			stats.AverageByMethod[method] = sum / float64(count)
		}
	}

	return stats
}

// ConfidenceLevel returns a human-readable confidence level.
func ConfidenceLevel(confidence float64) string {
	switch {
	case confidence >= 0.9:
		return "very high"
	case confidence >= 0.75:
		return "high"
	case confidence >= 0.5:
		return "moderate"
	case confidence >= 0.3:
		return "low"
	default:
		return "very low"
	}
}

// FormatConfidence returns a formatted confidence string.
func FormatConfidence(confidence float64) string {
	return fmt.Sprintf("%.0f%%", confidence*100)
}

// BuildExplanation generates a detailed CorrelationExplanation for a commit-bead correlation
func (s *Scorer) BuildExplanation(commit CorrelatedCommit, beadID string) CorrelationExplanation {
	signals := s.ExtractSignals(commit)
	totalWeight := 0
	for _, sig := range signals {
		totalWeight += sig.Weight
	}

	level := ConfidenceLevel(commit.Confidence)
	summary := s.buildSummary(commit, signals)
	recommendation := s.buildRecommendation(commit.Confidence, len(signals))

	return CorrelationExplanation{
		CommitSHA:      commit.SHA,
		BeadID:         beadID,
		Confidence:     commit.Confidence,
		ConfidencePct:  int(commit.Confidence * 100),
		Level:          level,
		Method:         commit.Method,
		Signals:        signals,
		TotalWeight:    totalWeight,
		Summary:        summary,
		Recommendation: recommendation,
	}
}

// ExtractSignals derives individual signals from a CorrelatedCommit. Every
// method that matched the commit (Methods; falling back to Method) contributes
// its signal, so a commit found by two strategies explains both.
func (s *Scorer) ExtractSignals(commit CorrelatedCommit) []CorrelationSignal {
	var signals []CorrelationSignal

	for _, method := range commit.AllMethods() {
		switch CorrelationMethod(method) {
		case MethodCoCommitted:
			signals = append(signals, CorrelationSignal{
				Type:   SignalCoCommit,
				Weight: 50,
				Detail: "Commit modified both code and beads file together (direct causation)",
			})
		case MethodExplicitID:
			signals = append(signals, CorrelationSignal{
				Type:   SignalMessageMatch,
				Weight: 40,
				Detail: "Commit message contains bead ID reference",
			})
		case MethodTemporalAuthor:
			signals = append(signals, CorrelationSignal{
				Type:   SignalTiming,
				Weight: 25,
				Detail: "Commit within bead's active time window",
			})
			signals = append(signals, CorrelationSignal{
				Type:   SignalAuthorMatch,
				Weight: 15,
				Detail: fmt.Sprintf("By assignee: %s", commit.Author),
			})
		}
	}

	// File-based signals
	if len(commit.Files) > 0 {
		fileWeight := minInt(len(commit.Files)*5, 15)
		signals = append(signals, CorrelationSignal{
			Type:   SignalFileOverlap,
			Weight: fileWeight,
			Detail: fmt.Sprintf("%d file(s) in commit scope", len(commit.Files)),
		})
	}

	// Confidence adjustment signals
	// If confidence is higher than base method suggests, there may be proximity signal
	baseRange, ok := MethodRanges[commit.Method]
	if ok && commit.Confidence > baseRange.Max*0.9 {
		signals = append(signals, CorrelationSignal{
			Type:   SignalProximity,
			Weight: 7,
			Detail: "Adjacent to other confirmed linked commits",
		})
	}

	return signals
}

// buildSummary creates a one-line summary of the correlation, naming every
// method that matched.
func (s *Scorer) buildSummary(commit CorrelatedCommit, signals []CorrelationSignal) string {
	descs := make([]string, 0, 3)
	for _, method := range commit.AllMethods() {
		switch CorrelationMethod(method) {
		case MethodCoCommitted:
			descs = append(descs, "Co-committed with bead update")
		case MethodExplicitID:
			descs = append(descs, "Explicitly references bead ID")
		case MethodTemporalAuthor:
			descs = append(descs, "Temporal+author correlation")
		}
	}
	methodDesc := strings.Join(descs, " + ")
	if commit.Confirmed {
		methodDesc += ", confirmed by feedback"
	}

	return fmt.Sprintf("%s (%.0f%% confidence, %d signals)",
		methodDesc, commit.Confidence*100, len(signals))
}

// buildRecommendation suggests what to do about this correlation
func (s *Scorer) buildRecommendation(confidence float64, _ int) string {
	switch {
	case confidence >= 0.85:
		return "High confidence - likely correct, no action needed"
	case confidence >= 0.65:
		return "Moderate confidence - review if accuracy matters"
	case confidence >= 0.45:
		return "Low confidence - manual verification recommended"
	default:
		return "Very low confidence - consider rejecting if incorrect"
	}
}

// ExplainMultiple generates explanations for all commits related to a bead
func (s *Scorer) ExplainMultiple(commits []CorrelatedCommit, beadID string) []CorrelationExplanation {
	explanations := make([]CorrelationExplanation, len(commits))
	for i, commit := range commits {
		explanations[i] = s.BuildExplanation(commit, beadID)
	}
	return explanations
}

// minInt returns the minimum of two ints
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
