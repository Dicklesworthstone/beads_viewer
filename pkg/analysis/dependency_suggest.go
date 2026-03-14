package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/beadscli"
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
	if len(issues) < 2 {
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
			title2Lower := strings.ToLower(issue2.Title)
			id1Lower := strings.ToLower(issue1.ID)
			id2Lower := strings.ToLower(issue2.ID)
			desc1Lower := strings.ToLower(issue1.Description)
			desc2Lower := strings.ToLower(issue2.Description)

			// ID mentioned
			if strings.Contains(desc2Lower, id1Lower) || strings.Contains(desc1Lower, id2Lower) {
				baseConf += config.ExactMatchBonus * 2
			}

			// Title words of issue1 mentioned in issue2's title
			// Use the keywords map for O(1) check? No, iterating kws of issue1 is fast.
			for _, word := range keywords[i] {
				if len(word) >= 5 && strings.Contains(title2Lower, word) {
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

			// Determine direction
			var from, to *model.Issue
			if issue1.CreatedAt.Before(issue2.CreatedAt) || issue1.Priority < issue2.Priority {
				from, to = issue2, issue1
			} else {
				from, to = issue1, issue2
			}

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
			WithAction(beadscli.Shell("{tool} dep add %s %s", match.From, match.To)).
			WithMetadata("shared_keywords", match.SharedKeywords)

		if len(match.SharedLabels) > 0 {
			sug = sug.WithMetadata("shared_labels", match.SharedLabels)
		}

		suggestions = append(suggestions, sug)
	}

	return suggestions
}

// findSharedKeys returns keys present in both maps
func findSharedKeys(m1, m2 map[string]bool) []string {
	var shared []string
	for k := range m1 {
		if m2[k] {
			shared = append(shared, k)
		}
	}
	return shared
}

// sortMatchesByConfidence sorts matches by confidence (highest first)
// Uses sort.Slice for O(n log n) performance instead of bubble sort O(n²)
func sortMatchesByConfidence(matches []DependencyMatch) {
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Confidence > matches[j].Confidence
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
