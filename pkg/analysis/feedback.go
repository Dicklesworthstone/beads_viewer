// Package analysis contains feedback loop implementation for recommendation tuning (bv-90)
package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FeedbackFile is the name of the feedback sidecar file
const FeedbackFile = "feedback.json"

// FeedbackEvent represents a single feedback action
type FeedbackEvent struct {
	IssueID   string    `json:"issue_id"`
	Action    string    `json:"action"` // "accept" or "ignore"
	Score     float64   `json:"score"`  // Score at time of feedback
	Timestamp time.Time `json:"timestamp"`
}

// WeightAdjustment tracks smoothed weight adjustments
type WeightAdjustment struct {
	Name        string    `json:"name"`
	Adjustment  float64   `json:"adjustment"` // Multiplier (0.5-2.0 range)
	Samples     int       `json:"samples"`    // Number of feedback events
	LastUpdated time.Time `json:"last_updated"`
}

// FeedbackData holds all feedback information for a repository
type FeedbackData struct {
	Version     string             `json:"version"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Events      []FeedbackEvent    `json:"events"`
	Adjustments []WeightAdjustment `json:"adjustments"`
	Stats       FeedbackStats      `json:"stats"`
	mu          sync.RWMutex       `json:"-"`
}

// FeedbackStats tracks aggregate feedback metrics
type FeedbackStats struct {
	TotalAccepted  int     `json:"total_accepted"`
	TotalIgnored   int     `json:"total_ignored"`
	AvgAcceptScore float64 `json:"avg_accept_score"`
	AvgIgnoreScore float64 `json:"avg_ignore_score"`
}

// DefaultFeedbackData returns initialized feedback data
func DefaultFeedbackData() *FeedbackData {
	return &FeedbackData{
		Version:     "1.0",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Events:      []FeedbackEvent{},
		Adjustments: defaultWeightAdjustments(),
		Stats:       FeedbackStats{},
	}
}

// defaultWeightAdjustments returns the initial weight adjustments (all 1.0 = no adjustment)
func defaultWeightAdjustments() []WeightAdjustment {
	weights := []string{
		"PageRank", "Betweenness", "BlockerRatio", "Staleness",
		"PriorityBoost", "TimeToImpact", "Urgency", "Risk",
	}
	adjustments := make([]WeightAdjustment, len(weights))
	for i, name := range weights {
		adjustments[i] = WeightAdjustment{
			Name:        name,
			Adjustment:  1.0,
			Samples:     0,
			LastUpdated: time.Now(),
		}
	}
	return adjustments
}

// LoadFeedback loads feedback data from the beads directory
func LoadFeedback(beadsDir string) (*FeedbackData, error) {
	path := filepath.Join(beadsDir, FeedbackFile)

	// Return defaults if file doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return DefaultFeedbackData(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read feedback file: %w", err)
	}

	var feedback FeedbackData
	if err := json.Unmarshal(data, &feedback); err != nil {
		return nil, fmt.Errorf("failed to parse feedback file: %w", err)
	}
	feedback.normalizeLoaded()

	return &feedback, nil
}

func (f *FeedbackData) normalizeLoaded() {
	now := time.Now()
	if f.Version == "" {
		f.Version = "1.0"
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = f.CreatedAt
	}
	if f.Events == nil {
		f.Events = []FeedbackEvent{}
	}
	f.Adjustments = normalizeWeightAdjustments(f.Adjustments, f.UpdatedAt)
	f.Stats = calculateFeedbackStats(f.Events)
}

func normalizeWeightAdjustments(loaded []WeightAdjustment, fallbackTime time.Time) []WeightAdjustment {
	byName := make(map[string]WeightAdjustment, len(loaded))
	for _, adj := range loaded {
		if adj.Name != "" {
			byName[adj.Name] = adj
		}
	}

	adjustments := defaultWeightAdjustments()
	for i := range adjustments {
		loadedAdj, ok := byName[adjustments[i].Name]
		if !ok {
			continue
		}
		if loadedAdj.Adjustment > 0 && !math.IsNaN(loadedAdj.Adjustment) && !math.IsInf(loadedAdj.Adjustment, 0) {
			adjustments[i].Adjustment = math.Max(0.5, math.Min(2.0, loadedAdj.Adjustment))
		}
		if loadedAdj.Samples > 0 {
			adjustments[i].Samples = loadedAdj.Samples
		}
		if !loadedAdj.LastUpdated.IsZero() {
			adjustments[i].LastUpdated = loadedAdj.LastUpdated
		} else if !fallbackTime.IsZero() {
			adjustments[i].LastUpdated = fallbackTime
		}
	}
	return adjustments
}

func calculateFeedbackStats(events []FeedbackEvent) FeedbackStats {
	var stats FeedbackStats
	var acceptedScoreTotal float64
	var ignoredScoreTotal float64

	for _, event := range events {
		switch event.Action {
		case "accept":
			stats.TotalAccepted++
			acceptedScoreTotal += event.Score
		case "ignore":
			stats.TotalIgnored++
			ignoredScoreTotal += event.Score
		}
	}

	if stats.TotalAccepted > 0 {
		stats.AvgAcceptScore = acceptedScoreTotal / float64(stats.TotalAccepted)
	}
	if stats.TotalIgnored > 0 {
		stats.AvgIgnoreScore = ignoredScoreTotal / float64(stats.TotalIgnored)
	}
	return stats
}

// Save persists feedback data atomically to the beads directory.
func (f *FeedbackData) Save(beadsDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal feedback: %w", err)
	}

	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		return fmt.Errorf("failed to create beads directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(beadsDir, ".feedback-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp feedback file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp feedback file: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write temp feedback file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp feedback file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp feedback file: %w", err)
	}

	path := filepath.Join(beadsDir, FeedbackFile)
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to write feedback file: %w", err)
	}
	cleanupTmp = false

	return nil
}

// RecordFeedback adds a feedback event and updates weights using exponential smoothing
func (f *FeedbackData) RecordFeedback(issueID, action string, score float64, breakdown ScoreBreakdown) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if action != "accept" && action != "ignore" {
		return fmt.Errorf("invalid action: %s (must be 'accept' or 'ignore')", action)
	}

	// Add event
	event := FeedbackEvent{
		IssueID:   issueID,
		Action:    action,
		Score:     score,
		Timestamp: time.Now(),
	}
	f.Events = append(f.Events, event)

	// Update stats
	if action == "accept" {
		f.Stats.TotalAccepted++
		f.Stats.AvgAcceptScore = updateRunningAverage(f.Stats.AvgAcceptScore, score, f.Stats.TotalAccepted)
	} else {
		f.Stats.TotalIgnored++
		f.Stats.AvgIgnoreScore = updateRunningAverage(f.Stats.AvgIgnoreScore, score, f.Stats.TotalIgnored)
	}

	// Apply exponential smoothing to weight adjustments based on breakdown
	// If accepted: boost weights that contributed most
	// If ignored: reduce weights that contributed most
	f.updateWeightAdjustments(action, breakdown)

	return nil
}

// updateRunningAverage computes a running average
func updateRunningAverage(currentAvg, newValue float64, count int) float64 {
	if count <= 1 {
		return newValue
	}
	return currentAvg + (newValue-currentAvg)/float64(count)
}

// updateWeightAdjustments applies exponential smoothing to adjust weights
// Alpha controls the learning rate (0.1 = slow adaptation, 0.5 = fast)
const smoothingAlpha = 0.2

func (f *FeedbackData) updateWeightAdjustments(action string, breakdown ScoreBreakdown) {
	// Direction: accept = boost high contributors, ignore = reduce them
	direction := 1.0
	if action == "ignore" {
		direction = -1.0
	}

	// Extract normalized contributions from breakdown
	contributions := map[string]float64{
		"PageRank":      breakdown.PageRankNorm,
		"Betweenness":   breakdown.BetweennessNorm,
		"BlockerRatio":  breakdown.BlockerRatioNorm,
		"Staleness":     breakdown.StalenessNorm,
		"PriorityBoost": breakdown.PriorityBoostNorm,
		"TimeToImpact":  breakdown.TimeToImpactNorm,
		"Urgency":       breakdown.UrgencyNorm,
		"Risk":          breakdown.RiskNorm,
	}

	for i := range f.Adjustments {
		adj := &f.Adjustments[i]
		contribution, exists := contributions[adj.Name]
		if !exists {
			continue
		}

		// Calculate adjustment delta based on contribution
		// High contribution to accepted item -> increase weight
		// High contribution to ignored item -> decrease weight
		// Delta is proportional to contribution
		delta := direction * contribution * 0.1 // Max 10% change per feedback

		// Apply exponential smoothing: new = alpha * observed + (1-alpha) * old
		// Here, observed is the target adjustment based on this feedback
		targetAdjustment := adj.Adjustment + delta

		// Clamp to reasonable range (0.5x to 2.0x)
		targetAdjustment = math.Max(0.5, math.Min(2.0, targetAdjustment))

		// Smooth the update
		adj.Adjustment = smoothingAlpha*targetAdjustment + (1-smoothingAlpha)*adj.Adjustment
		adj.Samples++
		adj.LastUpdated = time.Now()
	}
}

// GetAdjustedWeights returns the current weight adjustments as a map
func (f *FeedbackData) GetAdjustedWeights() map[string]float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.getAdjustedWeightsLocked()
}

// getAdjustedWeightsLocked is the internal version that assumes lock is already held
func (f *FeedbackData) getAdjustedWeightsLocked() map[string]float64 {
	weights := make(map[string]float64)
	for _, adj := range f.Adjustments {
		weights[adj.Name] = adj.Adjustment
	}
	return weights
}

// MinFeedbackSamples is the number of accept/ignore events required before the
// adjusted weights are applied to scoring. Below it the adjustments are still
// tracked and reported, but scoring uses the defaults so one stray click cannot
// reorder a project's triage.
const MinFeedbackSamples = 3

// Applies reports whether enough feedback exists for the adjusted weights to
// be used in scoring.
func (f *FeedbackData) Applies() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.Events) >= MinFeedbackSamples
}

// Weights returns the effective (adjusted, normalized) factor weights as the
// struct an Analyzer accepts via SetWeights. It does not check Applies; callers
// decide whether the sample size justifies using them.
func (f *FeedbackData) Weights() Weights {
	return WeightsFromMap(f.GetEffectiveWeights()).Normalized()
}

// GetEffectiveWeights returns the original weights multiplied by adjustments
func (f *FeedbackData) GetEffectiveWeights() map[string]float64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.getEffectiveWeightsLocked()
}

// getEffectiveWeightsLocked is the internal version that assumes lock is already held
func (f *FeedbackData) getEffectiveWeightsLocked() map[string]float64 {
	baseWeights := map[string]float64{
		"PageRank":      WeightPageRank,
		"Betweenness":   WeightBetweenness,
		"BlockerRatio":  WeightBlockerRatio,
		"Staleness":     WeightStaleness,
		"PriorityBoost": WeightPriorityBoost,
		"TimeToImpact":  WeightTimeToImpact,
		"Urgency":       WeightUrgency,
		"Risk":          WeightRisk,
	}

	effective := make(map[string]float64)
	adjustments := f.getAdjustedWeightsLocked() // Use internal version to avoid deadlock

	for name, base := range baseWeights {
		if adj, ok := adjustments[name]; ok {
			effective[name] = base * adj
		} else {
			effective[name] = base
		}
	}

	// Normalize so weights still sum to ~1.0
	var total float64
	for _, w := range effective {
		total += w
	}
	if total > 0 {
		for name := range effective {
			effective[name] /= total
		}
	}

	return effective
}

// Reset clears all feedback data, returning to defaults
func (f *FeedbackData) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Events = []FeedbackEvent{}
	f.Adjustments = defaultWeightAdjustments()
	f.Stats = FeedbackStats{}
	f.UpdatedAt = time.Now()
}

// Summary returns a human-readable summary of the feedback state
func (f *FeedbackData) Summary() string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.Events) == 0 {
		return "No feedback recorded yet. Use --feedback-accept or --feedback-ignore to provide feedback."
	}

	return fmt.Sprintf(
		"Feedback: %d accepted (avg score %.2f), %d ignored (avg score %.2f), %d total events",
		f.Stats.TotalAccepted, f.Stats.AvgAcceptScore,
		f.Stats.TotalIgnored, f.Stats.AvgIgnoreScore,
		len(f.Events),
	)
}

// FeedbackJSON returns the feedback data formatted for robot output
type FeedbackJSON struct {
	Enabled bool `json:"enabled"`
	// Applied is true when TotalEvents >= MinFeedbackSamples, i.e. the
	// effective weights below were actually used to score this output.
	Applied           bool               `json:"applied"`
	MinSamples        int                `json:"min_samples"`
	TotalEvents       int                `json:"total_events"`
	AcceptedCount     int                `json:"accepted_count"`
	IgnoredCount      int                `json:"ignored_count"`
	AvgAcceptScore    float64            `json:"avg_accept_score"`
	AvgIgnoreScore    float64            `json:"avg_ignore_score"`
	WeightAdjustments map[string]float64 `json:"weight_adjustments"`
	EffectiveWeights  map[string]float64 `json:"effective_weights"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// ToJSON returns feedback data formatted for robot output
func (f *FeedbackData) ToJSON() FeedbackJSON {
	f.mu.RLock()
	defer f.mu.RUnlock()

	return FeedbackJSON{
		Enabled:           len(f.Events) > 0,
		Applied:           len(f.Events) >= MinFeedbackSamples,
		MinSamples:        MinFeedbackSamples,
		TotalEvents:       len(f.Events),
		AcceptedCount:     f.Stats.TotalAccepted,
		IgnoredCount:      f.Stats.TotalIgnored,
		AvgAcceptScore:    f.Stats.AvgAcceptScore,
		AvgIgnoreScore:    f.Stats.AvgIgnoreScore,
		WeightAdjustments: f.getAdjustedWeightsLocked(),  // Use internal version to avoid deadlock
		EffectiveWeights:  f.getEffectiveWeightsLocked(), // Use internal version to avoid deadlock
		UpdatedAt:         f.UpdatedAt,
	}
}
