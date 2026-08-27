package analysis

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// DependencySuggestionConfig configures dependency suggestion generation
type DependencySuggestionConfig struct {
	// MinKeywordOverlap is the minimum number of shared keywords to suggest
	// Default: 2
	MinKeywordOverlap int

	// ExactMatchBonus is the confidence bonus for exact keyword matches
	// Default: 0.15
	ExactMatchBonus float64

	// LabelOverlapBonus is the confidence bonus per shared label
	// Default: 0.1
	LabelOverlapBonus float64

	// MinConfidence is the minimum confidence to report
	// Default: 0.5
	MinConfidence float64

	// MaxSuggestions limits the number of suggestions
	// Default: 20
	MaxSuggestions int

	// IgnoreExistingDeps skips pairs that already have dependencies
	// Default: true
	IgnoreExistingDeps bool
}

// DefaultDependencySuggestionConfig returns sensible defaults
func DefaultDependencySuggestionConfig() DependencySuggestionConfig {
	return DependencySuggestionConfig{
		MinKeywordOverlap:  2,
		ExactMatchBonus:    0.15,
		LabelOverlapBonus:  0.1,
		MinConfidence:      0.5,
		MaxSuggestions:     20,
		IgnoreExistingDeps: true,
	}
}

// DependencyMatch represents a potential dependency relationship
type DependencyMatch struct {
	From           string   `json:"from"`
	To             string   `json:"to"`
	Confidence     float64  `json:"confidence"`
	SharedKeywords []string `json:"shared_keywords"`
	SharedLabels   []string `json:"shared_labels,omitempty"`
	Reason         string   `json:"reason"`
}

// DetectMissingDependencies analyzes issues for potential missing dependencies
// Optimized with inverted index to avoid O(N^2) comparisons.
func DetectMissingDependencies(issues []model.Issue, config DependencySuggestionConfig) []Suggestion {
	if len(issues) < 2 || config.MaxSuggestions <= 0 {
		return nil
	}

	// 1. Build Inverted Index and Precompute Data
	keywords := make([][]string, len(issues))
	// issueLabels maps to set for fast overlap check
	issueLabels := make(map[int]map[string]bool, len(issues))
	// existingDeps maps to set for fast check
	existingDeps := make(map[int]map[int]bool, len(issues))

	// index[keyword] -> list of issue indices
	index := make(map[string][]int)

	for i := range issues {
		// Keywords
		kws := extractKeywords(issues[i].Title, issues[i].Description)
		keywords[i] = kws

		// Only index if we have enough keywords to possibly match
		if len(kws) >= config.MinKeywordOverlap {
			for _, w := range kws {
				index[w] = append(index[w], i)
			}
		}

		// Labels
		lbls := make(map[string]bool, len(issues[i].Labels))
		for _, l := range issues[i].Labels {
			lbls[strings.ToLower(l)] = true
		}
		issueLabels[i] = lbls

		// Existing Deps (store indices for speed)
		// We need a way to map ID -> Index.
		// Since we iterate by index, let's build ID map first.
	}

	// Build ID -> Index map
	idToIndex := make(map[string]int, len(issues))
	for i, issue := range issues {
		idToIndex[issue.ID] = i
	}

	// Fill existingDeps
	for i, issue := range issues {
		deps := make(map[int]bool)
		for _, dep := range issue.Dependencies {
			if dep != nil {
				if idx, ok := idToIndex[dep.DependsOnID]; ok {
					deps[idx] = true
				}
			}
		}
		existingDeps[i] = deps
	}

	var matches []DependencyMatch

	// 2. Iterate and Find Candidates
	for i := range issues {
		if len(keywords[i]) < config.MinKeywordOverlap {
			continue
		}

		// candidateIdx -> match count
		overlaps := make(map[int]int)
		for _, w := range keywords[i] {
			for _, matchIdx := range index[w] {
				if matchIdx > i { // Avoid duplicates and self
					overlaps[matchIdx]++
				}
			}
		}

		// 3. Evaluate Candidates
		for j, overlap := range overlaps {
			if overlap < config.MinKeywordOverlap {
				continue
			}

			// Check existing deps
			if config.IgnoreExistingDeps {
				if existingDeps[i][j] || existingDeps[j][i] {
					continue
				}
			}

			issue1 := &issues[i]
			issue2 := &issues[j]

			// Skip closed-like issues (no dependency suggestions for completed/tombstoned work)
			if isClosedLikeStatus(issue1.Status) || isClosedLikeStatus(issue2.Status) {
				continue
			}

			// Find shared keywords (we have count, need actual words for display)
			// Intersection of keywords[i] and keywords[j]
			sharedKW := intersectKeywords(keywords[i], keywords[j])

			// Find shared labels
			sharedLabels := findSharedKeys(issueLabels[i], issueLabels[j])

			// Calculate confidence
			baseConf := float64(len(sharedKW)) * 0.1
			if baseConf > 0.5 {
				baseConf = 0.5
			}

			// Check for exact title mentions / ID mentions
			title1Lower := strings.ToLower(issue1.Title)
			title2Lower := strings.ToLower(issue2.Title)
			id1Lower := strings.ToLower(issue1.ID)
			id2Lower := strings.ToLower(issue2.ID)
			desc1Lower := strings.ToLower(issue1.Description)
			desc2Lower := strings.ToLower(issue2.Description)

			// ID mentioned
			if containsExactIssueID(desc2Lower, id1Lower) || containsExactIssueID(desc1Lower, id2Lower) {
				baseConf += config.ExactMatchBonus * 2
			}

			// Give one symmetric title bonus when a shared substantive keyword
			// occurs in either title. Looking only from issue1 into issue2 made
			// confidence depend on the caller's input order.
			for _, word := range sharedKW {
				if len(word) >= 5 && (strings.Contains(title1Lower, word) || strings.Contains(title2Lower, word)) {
					baseConf += config.ExactMatchBonus
					break
				}
			}

			// Label overlap bonus
			baseConf += float64(len(sharedLabels)) * config.LabelOverlapBonus

			if baseConf > 0.95 {
				baseConf = 0.95
			}

			if baseConf < config.MinConfidence {
				continue
			}

			// Determine direction with a total, input-order-independent ranking.
			// Prefer the older issue as the likely prerequisite, then the more
			// urgent priority, then its ID. The previous pairwise OR rule reversed
			// direction when age and priority disagreed and the input was permuted.
			from, to := orderSuggestedDependency(issue1, issue2)

			reason := fmt.Sprintf("%d shared keywords", len(sharedKW))
			if len(sharedLabels) > 0 {
				reason += fmt.Sprintf(", %d shared labels", len(sharedLabels))
			}

			matches = append(matches, DependencyMatch{
				From:           from.ID,
				To:             to.ID,
				Confidence:     baseConf,
				SharedKeywords: sharedKW,
				SharedLabels:   sharedLabels,
				Reason:         reason,
			})
		}
	}

	// Sort by confidence and limit
	sortMatchesByConfidence(matches)
	if len(matches) > config.MaxSuggestions {
		matches = matches[:config.MaxSuggestions]
	}

	// Convert to suggestions
	suggestions := make([]Suggestion, 0, len(matches))
	for _, match := range matches {
		sug := NewSuggestion(
			SuggestionMissingDependency,
			match.From,
			fmt.Sprintf("May depend on %s", match.To),
			match.Reason,
			match.Confidence,
		).WithRelatedBead(match.To).
			WithMetadata("shared_keywords", match.SharedKeywords)
		fromArg, fromOK := quoteBeadsCommandID(match.From)
		toArg, toOK := quoteBeadsCommandID(match.To)
		if fromOK && toOK {
			if canAdd, cyclePath, warning := CheckDependencyAddition(issues, match.From, match.To); canAdd {
				sug = sug.WithAction(fmt.Sprintf("br dep add %s %s", fromArg, toArg))
			} else {
				sug = sug.WithMetadata("action_unavailable_reason", warning).
					WithMetadata("cycle_path", cyclePath)
			}
		}

		if len(match.SharedLabels) > 0 {
			sug = sug.WithMetadata("shared_labels", match.SharedLabels)
		}

		suggestions = append(suggestions, sug)
	}

	return suggestions
}

// containsExactIssueID reports a case-normalized ID token, not a raw prefix.
// A substring check treats bv-42 as an exact mention inside bv-420 and can turn
// ordinary keyword overlap into a high-confidence dependency false positive.
func containsExactIssueID(text, id string) bool {
	if id == "" {
		return false
	}
	for searchFrom := 0; searchFrom <= len(text)-len(id); {
		relative := strings.Index(text[searchFrom:], id)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		end := start + len(id)
		beforeIsID := false
		if start > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:start])
			beforeIsID = isIssueIDRune(r)
		}
		afterIsID := false
		if end < len(text) {
			r, _ := utf8.DecodeRuneInString(text[end:])
			afterIsID = isIssueIDRune(r)
		}
		if !beforeIsID && !afterIsID {
			return true
		}
		searchFrom = start + 1
	}
	return false
}

func isIssueIDRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' || r == '.'
}

// orderSuggestedDependency returns the issue likely to depend on the other
// first, followed by its likely prerequisite.
func orderSuggestedDependency(first, second *model.Issue) (from, to *model.Issue) {
	firstIsTarget := false
	switch {
	case !first.CreatedAt.Equal(second.CreatedAt):
		firstIsTarget = first.CreatedAt.Before(second.CreatedAt)
	case first.Priority != second.Priority:
		firstIsTarget = first.Priority < second.Priority
	default:
		firstIsTarget = first.ID < second.ID
	}
	if firstIsTarget {
		return second, first
	}
	return first, second
}

// findSharedKeys returns keys present in both maps
func findSharedKeys(m1, m2 map[string]bool) []string {
	var shared []string
	for k := range m1 {
		if m2[k] {
			shared = append(shared, k)
		}
	}
	sort.Strings(shared)
	return shared
}

// sortMatchesByConfidence sorts matches by confidence (highest first)
// Uses sort.Slice for O(n log n) performance instead of bubble sort O(n²)
func sortMatchesByConfidence(matches []DependencyMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Confidence != matches[j].Confidence {
			return matches[i].Confidence > matches[j].Confidence
		}
		if matches[i].From != matches[j].From {
			return matches[i].From < matches[j].From
		}
		return matches[i].To < matches[j].To
	})
}

// DependencySuggestionDetector provides stateful dependency suggestion detection
type DependencySuggestionDetector struct {
	config DependencySuggestionConfig
}

// NewDependencySuggestionDetector creates a new detector with the given config
func NewDependencySuggestionDetector(config DependencySuggestionConfig) *DependencySuggestionDetector {
	return &DependencySuggestionDetector{
		config: config,
	}
}

// Detect finds missing dependency suggestions
func (d *DependencySuggestionDetector) Detect(issues []model.Issue) []Suggestion {
	return DetectMissingDependencies(issues, d.config)
}
