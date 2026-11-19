package analysis

import (
	"math"
	"sort"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// SuggestAllConfig configures the unified suggestion generator
type SuggestAllConfig struct {
	// Duplicates detection config
	Duplicates DuplicateConfig

	// Dependencies suggestion config
	Dependencies DependencySuggestionConfig

	// Labels suggestion config
	Labels LabelSuggestionConfig

	// Cycles warning config
	Cycles CycleWarningConfig

	// EnableDuplicates enables duplicate detection
	EnableDuplicates bool

	// EnableDependencies enables dependency suggestions
	EnableDependencies bool

	// EnableLabels enables label suggestions
	EnableLabels bool

	// EnableCycles enables cycle warnings
	EnableCycles bool

	// MinConfidence filters suggestions below this threshold
	MinConfidence float64

	// MaxSuggestions limits total suggestions
	MaxSuggestions int

	// FilterType only includes this suggestion type (empty = all)
	FilterType SuggestionType

	// FilterBead only includes suggestions for this bead ID
	FilterBead string

	// CandidateIDs restricts suggestions to in-scope candidate beads when non-empty,
	// excluding suggestions whose primary target is outside CandidateIDs or that
	// involve only out-of-scope boundary blockers.
	CandidateIDs map[string]bool
}

// DefaultSuggestAllConfig returns sensible defaults with all features enabled
func DefaultSuggestAllConfig() SuggestAllConfig {
	dups := DefaultDuplicateConfig()
	dups.IgnoreClosed = true
	return SuggestAllConfig{
		Duplicates:         dups,
		Dependencies:       DefaultDependencySuggestionConfig(),
		Labels:             DefaultLabelSuggestionConfig(),
		Cycles:             DefaultCycleWarningConfig(),
		EnableDuplicates:   true,
		EnableDependencies: true,
		EnableLabels:       true,
		EnableCycles:       true,
		MinConfidence:      0.0,
		MaxSuggestions:     50,
	}
}

// GenerateAllSuggestions runs all suggestion detectors and returns aggregated results
func GenerateAllSuggestions(issues []model.Issue, config SuggestAllConfig, dataHash string) SuggestionSet {
	return GenerateAllSuggestionsAt(issues, config, dataHash, time.Now())
}

// GenerateAllSuggestionsAt runs all suggestion detectors using one reference
// instant for every serialized timestamp. This is used by reproducible robot
// output while the convenience API above retains wall-clock behavior.
func GenerateAllSuggestionsAt(issues []model.Issue, config SuggestAllConfig, dataHash string, now time.Time) SuggestionSet {
	var allSuggestions []Suggestion

	dupsCfg := config.Duplicates
	depsCfg := config.Dependencies
	labelsCfg := config.Labels
	cyclesCfg := config.Cycles

	candidateInput := issues
	if config.CandidateIDs != nil {
		candidateInput = make([]model.Issue, 0, len(issues))
		for _, iss := range issues {
			if config.CandidateIDs[iss.ID] {
				candidateInput = append(candidateInput, iss)
			}
		}
		// Prevent out-of-scope boundary nodes from exhausting per-detector caps
		// before FilterSuggestionsByCandidates runs (non-positive caps still disable
		// the sub-detector per sub-detector contract).
		if dupsCfg.MaxSuggestions > 0 {
			dupsCfg.MaxSuggestions = math.MaxInt
		}
		if depsCfg.MaxSuggestions > 0 {
			depsCfg.MaxSuggestions = math.MaxInt
		}
		if labelsCfg.MaxTotalSuggestions > 0 {
			labelsCfg.MaxTotalSuggestions = math.MaxInt
		}
		labelsCfg.TargetIDs = config.CandidateIDs
		if cyclesCfg.MaxCycles > 0 && cyclesCfg.MaxCycles < len(issues) {
			cyclesCfg.MaxCycles = len(issues)
		}
	}

	// Run enabled detectors
	if config.EnableDuplicates && (config.FilterType == "" || config.FilterType == SuggestionPotentialDuplicate) {
		duplicates := DetectDuplicates(candidateInput, dupsCfg)
		if config.CandidateIDs != nil {
			duplicates = FilterSuggestionsByCandidates(duplicates, config.CandidateIDs)
			if config.Duplicates.MaxSuggestions > 0 && len(duplicates) > config.Duplicates.MaxSuggestions {
				duplicates = duplicates[:config.Duplicates.MaxSuggestions]
			}
		}
		allSuggestions = append(allSuggestions, duplicates...)
	}

	if config.EnableDependencies && (config.FilterType == "" || config.FilterType == SuggestionMissingDependency) {
		if depsCfg.MaxSuggestions > 0 {
			depsCfg.MaxSuggestions = math.MaxInt
		}
		dependencies := DetectMissingDependencies(issues, depsCfg)
		if config.CandidateIDs != nil {
			dependencies = FilterSuggestionsByCandidates(dependencies, config.CandidateIDs)
		}
		dependencies = GetCycleFreeSuggestions(issues, dependencies)
		if config.Dependencies.MaxSuggestions > 0 && len(dependencies) > config.Dependencies.MaxSuggestions {
			dependencies = dependencies[:config.Dependencies.MaxSuggestions]
		}
		allSuggestions = append(allSuggestions, dependencies...)
	}

	if config.EnableLabels && (config.FilterType == "" || config.FilterType == SuggestionLabelSuggestion) {
		labels := SuggestLabels(issues, labelsCfg)
		if config.CandidateIDs != nil {
			labels = FilterSuggestionsByCandidates(labels, config.CandidateIDs)
			if config.Labels.MaxTotalSuggestions > 0 && len(labels) > config.Labels.MaxTotalSuggestions {
				labels = labels[:config.Labels.MaxTotalSuggestions]
			}
		}
		allSuggestions = append(allSuggestions, labels...)
	}

	if config.EnableCycles && (config.FilterType == "" || config.FilterType == SuggestionCycleWarning) {
		cycles := DetectCycleWarnings(issues, cyclesCfg)
		if config.CandidateIDs != nil {
			cycles = FilterSuggestionsByCandidates(cycles, config.CandidateIDs)
			if config.Cycles.MaxCycles > 0 && len(cycles) > config.Cycles.MaxCycles {
				cycles = cycles[:config.Cycles.MaxCycles]
			}
		}
		allSuggestions = append(allSuggestions, cycles...)
	}

	// Apply filters
	filtered := make([]Suggestion, 0, len(allSuggestions))
	for _, sug := range allSuggestions {
		// Min confidence filter
		if config.MinConfidence > 0 && sug.Confidence < config.MinConfidence {
			continue
		}

		// Type filter
		if config.FilterType != "" && sug.Type != config.FilterType {
			continue
		}

		// Bead filter
		if config.FilterBead != "" && sug.TargetBead != config.FilterBead && sug.RelatedBead != config.FilterBead {
			continue
		}

		filtered = append(filtered, sug)
	}

	// Sort by confidence (highest first), then by stable semantic fields. Some
	// detectors build candidates through maps, so confidence alone would leave
	// equal-score output dependent on Go's randomized map iteration order.
	sort.Slice(filtered, func(i, j int) bool {
		left, right := filtered[i], filtered[j]
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.TargetBead != right.TargetBead {
			return left.TargetBead < right.TargetBead
		}
		if left.RelatedBead != right.RelatedBead {
			return left.RelatedBead < right.RelatedBead
		}
		if left.Summary != right.Summary {
			return left.Summary < right.Summary
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		return left.ActionCommand < right.ActionCommand
	})

	// Apply max limit
	if config.MaxSuggestions > 0 && len(filtered) > config.MaxSuggestions {
		filtered = filtered[:config.MaxSuggestions]
	}

	for i := range filtered {
		filtered[i].GeneratedAt = now
	}
	return NewSuggestionSetAt(filtered, dataHash, now)
}

// FilterSuggestionsByCandidates excludes suggestions outside candidateIDs when
// candidateIDs is non-nil (nil allows all issues; an empty map allows none):
//   - cycle warnings require at least one cycle participant to be a candidate
//     (excluding cycles formed solely by out-of-scope boundary blockers);
//   - all other suggestion types require their primary TargetBead (and any
//     non-empty RelatedBead) to be candidates.
func FilterSuggestionsByCandidates(suggestions []Suggestion, candidateIDs map[string]bool) []Suggestion {
	if candidateIDs == nil {
		return suggestions
	}
	out := make([]Suggestion, 0, len(suggestions))
	for _, sug := range suggestions {
		if suggestionMatchesCandidates(sug, candidateIDs) {
			out = append(out, sug)
		}
	}
	return out
}

func suggestionMatchesCandidates(sug Suggestion, candidateIDs map[string]bool) bool {
	if candidateIDs == nil {
		return true
	}
	switch sug.Type {
	case SuggestionCycleWarning:
		if candidateIDs[sug.TargetBead] || (sug.RelatedBead != "" && candidateIDs[sug.RelatedBead]) {
			return true
		}
		if rawPath, ok := sug.Metadata["cycle_path"]; ok {
			if path, ok := rawPath.([]string); ok {
				for _, id := range path {
					if candidateIDs[id] {
						return true
					}
				}
			}
		}
		return false
	default:
		return candidateIDs[sug.TargetBead] && (sug.RelatedBead == "" || candidateIDs[sug.RelatedBead])
	}
}

// RobotSuggestOutput is the JSON output structure for --robot-suggest
type RobotSuggestOutput struct {
	GeneratedAt string        `json:"generated_at"`
	DataHash    string        `json:"data_hash"`
	Filters     SuggestFilter `json:"filters"`
	Set         SuggestionSet `json:"suggestions"`
	UsageHints  []string      `json:"usage_hints"`
}

// SuggestFilter describes applied filters
type SuggestFilter struct {
	Type          string  `json:"type,omitempty"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
	BeadID        string  `json:"bead_id,omitempty"`
}

// GenerateRobotSuggestOutput creates the full robot-suggest output
func GenerateRobotSuggestOutput(issues []model.Issue, config SuggestAllConfig, dataHash string) RobotSuggestOutput {
	return GenerateRobotSuggestOutputAt(issues, config, dataHash, time.Now().UTC())
}

// GenerateRobotSuggestOutputAt creates robot-suggest output at a caller-owned
// reference instant so nested suggestion timestamps match the outer envelope.
func GenerateRobotSuggestOutputAt(issues []model.Issue, config SuggestAllConfig, dataHash string, now time.Time) RobotSuggestOutput {
	now = now.UTC()
	set := GenerateAllSuggestionsAt(issues, config, dataHash, now)

	return RobotSuggestOutput{
		GeneratedAt: now.Format(time.RFC3339),
		DataHash:    dataHash,
		Filters: SuggestFilter{
			Type:          string(config.FilterType),
			MinConfidence: config.MinConfidence,
			BeadID:        config.FilterBead,
		},
		Set: set,
		UsageHints: []string{
			"jq '.suggestions.suggestions[:5]' - Top 5 suggestions by confidence",
			"jq '.suggestions.suggestions[] | select(.type==\"potential_duplicate\")' - Filter duplicates",
			"jq '.suggestions.suggestions[] | select(.confidence >= 0.8)' - High-confidence only",
			"jq '.suggestions.stats.by_type' - Count by suggestion type",
			"jq '.suggestions.suggestions[].action_command' - All action commands",
			"--suggest-type=dependency - Filter to dependency suggestions",
			"--suggest-confidence=0.7 - Minimum confidence threshold",
			"--suggest-bead=<id> - Suggestions for specific bead",
		},
	}
}
