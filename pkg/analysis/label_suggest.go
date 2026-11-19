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

	// TargetIDs optionally restricts which non-closed issues receive label
	// suggestions while still learning canonical label vocabulary from issues.
	TargetIDs map[string]bool
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

type labelVocabulary struct {
	allLabels        map[string]bool
	canonicalByLower map[string]string
	learnedMappings  map[string]map[string]int
}

func cleanValidLabel(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsRune(trimmed, '\x00') {
		return "", false
	}
	return trimmed, true
}

func buildLabelVocabulary(issues []model.Issue, learnFromExisting bool) *labelVocabulary {
	allLabels := make(map[string]bool)
	rawCounts := make(map[string]map[string]int)
	for _, issue := range issues {
		for _, label := range issue.Labels {
			trimmed, ok := cleanValidLabel(label)
			if !ok {
				continue
			}
			lower := strings.ToLower(trimmed)
			allLabels[lower] = true
			if rawCounts[lower] == nil {
				rawCounts[lower] = make(map[string]int)
			}
			rawCounts[lower][trimmed]++
		}
	}

	canonicalByLower := make(map[string]string, len(rawCounts))
	for lower, variants := range rawCounts {
		bestRaw := ""
		bestCount := -1
		for raw, count := range variants {
			if count > bestCount || (count == bestCount && raw < bestRaw) {
				bestRaw = raw
				bestCount = count
			}
		}
		canonicalByLower[lower] = bestRaw
	}

	var learned map[string]map[string]int
	if learnFromExisting {
		learned = learnLabelMappings(issues)
	}

	return &labelVocabulary{
		allLabels:        allLabels,
		canonicalByLower: canonicalByLower,
		learnedMappings:  learned,
	}
}

// SuggestLabels analyzes issues for potential label suggestions
func SuggestLabels(issues []model.Issue, config LabelSuggestionConfig) []Suggestion {
	if len(issues) == 0 || config.MaxSuggestionsPerIssue <= 0 || config.MaxTotalSuggestions <= 0 {
		return nil
	}

	hasMixedRepos := false
	firstRepo := strings.TrimSpace(issues[0].SourceRepo)
	for i := 1; i < len(issues); i++ {
		if strings.TrimSpace(issues[i].SourceRepo) != firstRepo {
			hasMixedRepos = true
			break
		}
	}

	vocabByRepo := make(map[string]*labelVocabulary)
	getVocab := func(sourceRepo string) *labelVocabulary {
		repo := strings.TrimSpace(sourceRepo)
		if cached, ok := vocabByRepo[repo]; ok {
			return cached
		}
		scopeIssues := issues
		if hasMixedRepos || repo != firstRepo {
			scopeIssues = make([]model.Issue, 0, len(issues))
			for _, iss := range issues {
				if strings.TrimSpace(iss.SourceRepo) == repo {
					scopeIssues = append(scopeIssues, iss)
				}
			}
		}
		built := buildLabelVocabulary(scopeIssues, config.LearnFromExisting)
		vocabByRepo[repo] = built
		return built
	}

	var matches []LabelMatch

	for _, issue := range issues {
		// Skip closed/tombstone issues
		if isClosedLikeStatus(issue.Status) {
			continue
		}
		if config.TargetIDs != nil && !config.TargetIDs[issue.ID] {
			continue
		}

		vocab := getVocab(issue.SourceRepo)
		allLabels := vocab.allLabels
		learnedMappings := vocab.learnedMappings

		// Get existing labels for this issue (case-insensitive suppression)
		existingLabels := make(map[string]bool, len(issue.Labels))
		for _, l := range issue.Labels {
			if trimmed, ok := cleanValidLabel(l); ok {
				existingLabels[strings.ToLower(trimmed)] = true
			}
		}

		// Extract keywords
		keywords := extractKeywords(issue.Title, issue.Description)

		// Score potential labels (keyed by lowercased label)
		labelScores := make(map[string]float64)
		labelReasons := make(map[string][]string)

		// Check builtin mappings
		if config.BuiltinMappings {
			for _, keyword := range keywords {
				if labels, ok := builtinLabelMappings[keyword]; ok {
					for _, label := range labels {
						lower := strings.ToLower(label)
						if !existingLabels[lower] && allLabels[lower] {
							labelScores[lower] += 0.3
							labelReasons[lower] = append(labelReasons[lower], keyword)
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

			canonicalLabel := vocab.canonicalByLower[candidate.label]
			if canonicalLabel == "" {
				canonicalLabel = candidate.label
			}

			reasons := labelReasons[candidate.label]
			uniqueReasons := uniqueStrings(reasons)
			sort.Strings(uniqueReasons)
			reason := fmt.Sprintf("keywords: %s", strings.Join(uniqueReasons, ", "))

			matches = append(matches, LabelMatch{
				IssueID:      issue.ID,
				Label:        canonicalLabel,
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
	byID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	for _, match := range matches {
		sug := NewSuggestion(
			SuggestionLabelSuggestion,
			match.IssueID,
			fmt.Sprintf("Consider adding label '%s'", match.Label),
			match.Reason,
			match.Confidence,
		).WithMetadata("suggested_label", match.Label).
			WithMetadata("matched_keywords", match.MatchedWords)
		sug = sug.withMutationAction(byID[match.IssueID], model.MutationAddLabel, nil, match.Label)

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

		seenLabels := make(map[string]bool, len(issue.Labels))
		uniqueLowerLabels := make([]string, 0, len(issue.Labels))
		for _, label := range issue.Labels {
			trimmed, ok := cleanValidLabel(label)
			if !ok {
				continue
			}
			lower := strings.ToLower(trimmed)
			if !seenLabels[lower] {
				seenLabels[lower] = true
				uniqueLowerLabels = append(uniqueLowerLabels, lower)
			}
		}
		if len(uniqueLowerLabels) == 0 {
			continue
		}

		keywords := extractKeywords(issue.Title, issue.Description)
		for _, kw := range keywords {
			if mappings[kw] == nil {
				mappings[kw] = make(map[string]int)
			}
			for _, lower := range uniqueLowerLabels {
				mappings[kw][lower]++
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
