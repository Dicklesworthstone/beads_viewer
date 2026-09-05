package analysis

import (
	"strings"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func quoteBeadsCommandID(id string) (string, bool) {
	if id == "" || strings.HasPrefix(id, "-") || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return "", false
	}
	return quoteShellWord(id), true
}

// quoteBeadsFlagValue quotes a value that the caller concatenates after a
// complete flag name and '='. Unlike a positional ID, a leading '-' is safe in
// that position.
func quoteBeadsFlagValue(value string) (string, bool) {
	if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", false
	}
	return quoteShellWord(value), true
}

func quoteShellWord(word string) string {
	if strings.IndexFunc(word, func(r rune) bool {
		return !(r == '-' || r == '_' || r == '=' || r == '/' || r == '.' || r == ':' || r == ',' ||
			(r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
	}) < 0 {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
}

// SuggestionType categorizes the kind of suggestion
type SuggestionType string

const (
	// SuggestionMissingDependency suggests a dependency that should exist
	SuggestionMissingDependency SuggestionType = "missing_dependency"

	// SuggestionPotentialDuplicate suggests issues that may be duplicates
	SuggestionPotentialDuplicate SuggestionType = "potential_duplicate"

	// SuggestionLabelSuggestion suggests labels based on content analysis
	SuggestionLabelSuggestion SuggestionType = "label_suggestion"

	// SuggestionStaleCleanup suggests issues that may need attention due to age
	SuggestionStaleCleanup SuggestionType = "stale_cleanup"

	// SuggestionCycleWarning warns about potential dependency cycles
	SuggestionCycleWarning SuggestionType = "cycle_warning"
)

// Suggestion represents a smart recommendation for project hygiene
type Suggestion struct {
	// Type categorizes this suggestion
	Type SuggestionType `json:"type"`

	// TargetBead is the primary bead this suggestion applies to
	TargetBead string `json:"target_bead"`

	// RelatedBead is an optional secondary bead (e.g., for duplicates or dependencies)
	RelatedBead string `json:"related_bead,omitempty"`

	// Summary is a concise description of the suggestion
	Summary string `json:"summary"`

	// Reason explains why this suggestion was generated
	Reason string `json:"reason"`

	// Confidence is the strength of this suggestion (0.0-1.0)
	Confidence float64 `json:"confidence"`

	// ActionCommand is an optional CLI command to act on this suggestion
	ActionCommand string `json:"action_command,omitempty"`
	// Action preserves the source-bound argv and working directory. The shell
	// command above is a rendering of the same action, never parsed as input.
	Action *model.IssueCommand `json:"action,omitempty"`

	// GeneratedAt is when this suggestion was created
	GeneratedAt time.Time `json:"generated_at"`

	// Metadata holds type-specific extra information
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ConfidenceLevel represents human-readable confidence thresholds
type ConfidenceLevel string

const (
	// ConfidenceLow represents suggestions with low certainty (< 0.4)
	ConfidenceLow ConfidenceLevel = "low"

	// ConfidenceMedium represents suggestions with moderate certainty (0.4-0.7)
	ConfidenceMedium ConfidenceLevel = "medium"

	// ConfidenceHigh represents suggestions with high certainty (> 0.7)
	ConfidenceHigh ConfidenceLevel = "high"
)

// Confidence thresholds
const (
	// ConfidenceThresholdLow is the upper bound for low confidence
	ConfidenceThresholdLow = 0.4

	// ConfidenceThresholdHigh is the lower bound for high confidence
	ConfidenceThresholdHigh = 0.7

	// MinConfidenceDefault is the default minimum confidence for suggestions
	MinConfidenceDefault = 0.3
)

// GetConfidenceLevel returns the human-readable confidence level
func (s *Suggestion) GetConfidenceLevel() ConfidenceLevel {
	if s.Confidence < ConfidenceThresholdLow {
		return ConfidenceLow
	}
	if s.Confidence >= ConfidenceThresholdHigh {
		return ConfidenceHigh
	}
	return ConfidenceMedium
}

// IsActionable returns true if the suggestion has an associated action command
func (s *Suggestion) IsActionable() bool {
	return s.ActionCommand != ""
}

// SuggestionSet holds a collection of suggestions with metadata
type SuggestionSet struct {
	// Suggestions is the list of suggestions
	Suggestions []Suggestion `json:"suggestions"`

	// GeneratedAt is when this set was created
	GeneratedAt time.Time `json:"generated_at"`

	// DataHash identifies the data version used to generate suggestions
	DataHash string `json:"data_hash,omitempty"`

	// Stats provides summary statistics
	Stats SuggestionStats `json:"stats"`
}

// SuggestionStats summarizes a set of suggestions
type SuggestionStats struct {
	// Total is the total number of suggestions
	Total int `json:"total"`

	// ByType counts suggestions by type
	ByType map[SuggestionType]int `json:"by_type"`

	// ByConfidence counts suggestions by confidence level
	ByConfidence map[ConfidenceLevel]int `json:"by_confidence"`

	// HighConfidenceCount is the number of high-confidence suggestions
	HighConfidenceCount int `json:"high_confidence_count"`

	// ActionableCount is the number of suggestions with action commands
	ActionableCount int `json:"actionable_count"`
}

// NewSuggestion creates a new suggestion with current timestamp
func NewSuggestion(
	sugType SuggestionType,
	targetBead string,
	summary string,
	reason string,
	confidence float64,
) Suggestion {
	return Suggestion{
		Type:        sugType,
		TargetBead:  targetBead,
		Summary:     summary,
		Reason:      reason,
		Confidence:  confidence,
		GeneratedAt: time.Now(),
	}
}

// WithRelatedBead adds a related bead to the suggestion
func (s Suggestion) WithRelatedBead(beadID string) Suggestion {
	s.RelatedBead = beadID
	return s
}

// WithAction adds an action command to the suggestion
func (s Suggestion) WithAction(cmd *model.IssueCommand) Suggestion {
	s.Action = cmd
	s.ActionCommand = ""
	if cmd != nil {
		s.ActionCommand = cmd.Shell
	}
	return s
}

func (s Suggestion) withMutationAction(issue model.Issue, kind model.MutationKind, peer *model.Issue, value string) Suggestion {
	command, reason := issue.MutationAction(kind, peer, value)
	s = s.WithAction(command)
	if reason != "" {
		s = s.WithMetadata("action_unavailable_reason", reason)
	}
	return s
}

// WithMetadata adds metadata to the suggestion
func (s Suggestion) WithMetadata(key string, value interface{}) Suggestion {
	metadata := make(map[string]interface{}, len(s.Metadata)+1)
	for existingKey, existingValue := range s.Metadata {
		metadata[existingKey] = existingValue
	}
	metadata[key] = value
	s.Metadata = metadata
	return s
}

// NewSuggestionSet creates a new suggestion set and computes stats
func NewSuggestionSet(suggestions []Suggestion, dataHash string) SuggestionSet {
	return NewSuggestionSetAt(suggestions, dataHash, time.Now())
}

// NewSuggestionSetAt creates a suggestion set at a caller-supplied instant.
func NewSuggestionSetAt(suggestions []Suggestion, dataHash string, now time.Time) SuggestionSet {
	set := SuggestionSet{
		Suggestions: suggestions,
		GeneratedAt: now,
		DataHash:    dataHash,
	}
	set.computeStats()
	return set
}

// computeStats calculates summary statistics for the suggestion set
func (ss *SuggestionSet) computeStats() {
	ss.Stats = SuggestionStats{
		Total:        len(ss.Suggestions),
		ByType:       make(map[SuggestionType]int),
		ByConfidence: make(map[ConfidenceLevel]int),
	}

	for _, s := range ss.Suggestions {
		// Count by type
		ss.Stats.ByType[s.Type]++

		// Count by confidence level
		level := s.GetConfidenceLevel()
		ss.Stats.ByConfidence[level]++

		// Count high confidence
		if level == ConfidenceHigh {
			ss.Stats.HighConfidenceCount++
		}

		// Count actionable
		if s.IsActionable() {
			ss.Stats.ActionableCount++
		}
	}
}

// FilterByType returns suggestions of a specific type
func (ss *SuggestionSet) FilterByType(t SuggestionType) []Suggestion {
	result := make([]Suggestion, 0)
	for _, s := range ss.Suggestions {
		if s.Type == t {
			result = append(result, s)
		}
	}
	return result
}

// FilterByMinConfidence returns suggestions above a confidence threshold
func (ss *SuggestionSet) FilterByMinConfidence(minConf float64) []Suggestion {
	result := make([]Suggestion, 0)
	for _, s := range ss.Suggestions {
		if s.Confidence >= minConf {
			result = append(result, s)
		}
	}
	return result
}

// HighConfidenceSuggestions returns only high-confidence suggestions
func (ss *SuggestionSet) HighConfidenceSuggestions() []Suggestion {
	return ss.FilterByMinConfidence(ConfidenceThresholdHigh)
}
