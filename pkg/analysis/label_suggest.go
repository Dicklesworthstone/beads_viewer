package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// LabelSuggestionConfig configures label suggestion generation
type LabelSuggestionConfig struct {
	// MinConfidence is the minimum confidence to report
	// Default: 0.5
	MinConfidence float64

	// MaxSuggestionsPerIssue limits suggestions per issue
	// Default: 3
	MaxSuggestionsPerIssue int

	// MaxTotalSuggestions limits total suggestions
	// Default: 30
	MaxTotalSuggestions int

	// LearnFromExisting uses existing labeled issues to learn patterns
	// Default: true
	LearnFromExisting bool

	// BuiltinMappings enables built-in keyword-to-label mappings
	// Default: true
	BuiltinMappings bool
}

// DefaultLabelSuggestionConfig returns sensible defaults
func DefaultLabelSuggestionConfig() LabelSuggestionConfig {
	return LabelSuggestionConfig{
		MinConfidence:          0.5,
		MaxSuggestionsPerIssue: 3,
		MaxTotalSuggestions:    30,
		LearnFromExisting:      true,
		BuiltinMappings:        true,
	}
}

// Built-in keyword to label mappings
var builtinLabelMappings = map[string][]string{
	// Technical areas
	"database":    {"database", "db"},
	"migration":   {"database", "migration"},
	"api":         {"api"},
	"endpoint":    {"api"},
	"rest":        {"api"},
	"graphql":     {"api", "graphql"},
	"auth":        {"auth", "security"},
	"login":       {"auth"},
	"password":    {"auth", "security"},
	"security":    {"security"},
	"test":        {"testing"},
	"tests":       {"testing"},
	"unittest":    {"testing"},
	"integration": {"testing", "integration"},
	"ui":          {"ui", "frontend"},
	"frontend":    {"frontend"},
	"backend":     {"backend"},
	"server":      {"backend"},
	"cli":         {"cli"},
	"command":     {"cli"},
	"config":      {"config"},
	"settings":    {"config"},
	"performance": {"performance"},
	"slow":        {"performance"},
	"fast":        {"performance"},
	"memory":      {"performance"},
	"cache":       {"performance", "cache"},
	"docs":        {"documentation"},
	"readme":      {"documentation"},
	"refactor":    {"refactoring"},
	"cleanup":     {"refactoring", "maintenance"},
	"dependency":  {"dependencies"},
	"deps":        {"dependencies"},

	// Issue types
	"bug":     {"bug"},
	"fix":     {"bug"},
	"broken":  {"bug"},
	"crash":   {"bug"},
	"error":   {"bug"},
	"feature": {"feature"},
	"enhance": {"enhancement"},
	"improve": {"enhancement"},
	"urgent":  {"urgent", "priority"},
	"hotfix":  {"urgent", "bug"},
}

// LabelMatch represents a potential label suggestion
type LabelMatch struct {
	IssueID      string   `json:"issue_id"`
	Label        string   `json:"label"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
	MatchedWords []string `json:"matched_words,omitempty"`
}

// SuggestLabels analyzes issues for potential label suggestions
func SuggestLabels(issues []model.Issue, config LabelSuggestionConfig) []Suggestion {
	if len(issues) == 0 || config.MaxSuggestionsPerIssue <= 0 || config.MaxTotalSuggestions <= 0 {
		return nil
	}

	// Build learned mappings from existing labeled issues
	learnedMappings := make(map[string]map[string]int) // keyword -> label -> count
	if config.LearnFromExisting {
		learnedMappings = learnLabelMappings(issues)
	}

	// Find all existing labels for validation
	allLabels := make(map[string]bool)
	for _, issue := range issues {
		for _, label := range issue.Labels {
			allLabels[strings.ToLower(label)] = true
		}
	}

	var matches []LabelMatch

	for _, issue := range issues {
		// Skip closed/tombstone issues
		if isClosedLikeStatus(issue.Status) {
			continue
		}

		// Get existing labels for this issue
		existingLabels := make(map[string]bool)
		for _, l := range issue.Labels {
			existingLabels[strings.ToLower(l)] = true
		}

		// Extract keywords
		keywords := extractKeywords(issue.Title, issue.Description)

		// Score potential labels
		labelScores := make(map[string]float64)
		labelReasons := make(map[string][]string)

		// Check builtin mappings
		if config.BuiltinMappings {
			for _, keyword := range keywords {
				if labels, ok := builtinLabelMappings[keyword]; ok {
					for _, label := range labels {
						if !existingLabels[label] && allLabels[label] {
							labelScores[label] += 0.3
							labelReasons[label] = append(labelReasons[label], keyword)
						}
					}
				}
			}
		}

		// Check learned mappings
		if config.LearnFromExisting {
			// Preserve keyword order while accumulating floating-point bonuses.
			// Iterating a keyword map randomized the addition order and could change
			// the final confidence by an ulp between otherwise identical runs.
			for _, keyword := range keywords {
				if labelCounts, ok := learnedMappings[keyword]; ok {
					for label, count := range labelCounts {
						if !existingLabels[label] && allLabels[label] {
							// Weight by frequency (more occurrences = more reliable)
							bonus := 0.1 + (float64(count) * 0.05)
							if bonus > 0.4 {
								bonus = 0.4
							}
							labelScores[label] += bonus
							labelReasons[label] = append(labelReasons[label], keyword)
						}
					}
				}
			}
		}

		// Convert scores to matches, sorted by confidence for deterministic top picks.
		type labelCandidate struct {
			label string
			score float64
		}
		candidates := make([]labelCandidate, 0, len(labelScores))
		for label, score := range labelScores {
			candidates = append(candidates, labelCandidate{label: label, score: score})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].score == candidates[j].score {
				return candidates[i].label < candidates[j].label
			}
			return candidates[i].score > candidates[j].score
		})

		issueMatches := 0
		for _, candidate := range candidates {
			if candidate.score < config.MinConfidence {
				continue
			}
			score := candidate.score
			if score > 0.95 {
				score = 0.95
			}
			if issueMatches >= config.MaxSuggestionsPerIssue {
				break
			}

			reasons := labelReasons[candidate.label]
			uniqueReasons := uniqueStrings(reasons)
			sort.Strings(uniqueReasons)
			reason := fmt.Sprintf("keywords: %s", strings.Join(uniqueReasons, ", "))

			matches = append(matches, LabelMatch{
				IssueID:      issue.ID,
				Label:        candidate.label,
				Confidence:   score,
				Reason:       reason,
				MatchedWords: uniqueReasons,
			})
			issueMatches++
		}
	}

	// Sort by confidence and limit
	sortLabelMatchesByConfidence(matches)
	if len(matches) > config.MaxTotalSuggestions {
		matches = matches[:config.MaxTotalSuggestions]
	}

	// Convert to suggestions
	suggestions := make([]Suggestion, 0, len(matches))
	for _, match := range matches {
		sug := NewSuggestion(
			SuggestionLabelSuggestion,
			match.IssueID,
			fmt.Sprintf("Consider adding label '%s'", match.Label),
			match.Reason,
			match.Confidence,
		).WithMetadata("suggested_label", match.Label).
			WithMetadata("matched_keywords", match.MatchedWords)
		issueArg, issueOK := quoteBeadsCommandID(match.IssueID)
		labelArg, labelOK := quoteBeadsFlagValue(match.Label)
		if issueOK && labelOK {
			sug = sug.WithAction(fmt.Sprintf("br update %s --add-label=%s", issueArg, labelArg))
		}

		suggestions = append(suggestions, sug)
	}

	return suggestions
}

// learnLabelMappings extracts keyword-to-label patterns from existing issues
func learnLabelMappings(issues []model.Issue) map[string]map[string]int {
	mappings := make(map[string]map[string]int)

	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}

		keywords := extractKeywords(issue.Title, issue.Description)
		for _, kw := range keywords {
			if mappings[kw] == nil {
				mappings[kw] = make(map[string]int)
			}
			for _, label := range issue.Labels {
				mappings[kw][strings.ToLower(label)]++
			}
		}
	}

	return mappings
}

// uniqueStrings returns unique strings from a slice
func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

// sortLabelMatchesByConfidence sorts matches by confidence (highest first)
// Uses sort.Slice for O(n log n) performance instead of bubble sort O(n²)
func sortLabelMatchesByConfidence(matches []LabelMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Confidence != matches[j].Confidence {
			return matches[i].Confidence > matches[j].Confidence
		}
		if matches[i].IssueID != matches[j].IssueID {
			return matches[i].IssueID < matches[j].IssueID
		}
		return matches[i].Label < matches[j].Label
	})
}

// LabelSuggestionDetector provides stateful label suggestion detection
type LabelSuggestionDetector struct {
	config LabelSuggestionConfig
}

// NewLabelSuggestionDetector creates a new detector with the given config
func NewLabelSuggestionDetector(config LabelSuggestionConfig) *LabelSuggestionDetector {
	return &LabelSuggestionDetector{
		config: config,
	}
}

// Detect finds label suggestions
func (d *LabelSuggestionDetector) Detect(issues []model.Issue) []Suggestion {
	return SuggestLabels(issues, d.config)
}
