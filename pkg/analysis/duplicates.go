package analysis

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Package-level compiled regex for performance (avoids recompilation on each call)
var nonWordRegex = regexp.MustCompile(`[^\w\s]`)

// Package-level stop words map for performance (avoids recreation on each call)
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true,
	"this": true, "that": true, "from": true, "are": true,
	"was": true, "were": true, "been": true, "have": true,
	"has": true, "had": true, "does": true, "did": true,
	"will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "can": true, "not": true,
	"all": true, "any": true, "some": true, "each": true,
	"when": true, "where": true, "what": true, "which": true,
	"how": true, "why": true, "who": true, "its": true,
	"also": true, "just": true, "only": true, "more": true,
	"than": true, "then": true, "now": true, "here": true,
	"there": true, "these": true, "those": true, "such": true,
	"into": true, "over": true, "after": true, "before": true,
	"being": true, "other": true, "about": true, "like": true,
	"very": true, "most": true, "make": true, "use": true,
}

func isClosedLikeDuplicateStatus(status model.Status) bool {
	return status == model.StatusClosed || status == model.StatusTombstone
}

// DuplicateConfig configures duplicate detection behavior
type DuplicateConfig struct {
	// JaccardThreshold is the minimum similarity score (0.0-1.0)
	// Default: 0.7
	JaccardThreshold float64

	// MinKeywords is the minimum number of keywords needed to compare
	// Default: 2
	MinKeywords int

	// IgnoreClosedVsOpen skips pairs where one is closed and one is open
	// Default: true
	IgnoreClosedVsOpen bool

	// MaxSuggestions limits the number of duplicate suggestions
	// Default: 20
	MaxSuggestions int
}

// DefaultDuplicateConfig returns sensible defaults
func DefaultDuplicateConfig() DuplicateConfig {
	return DuplicateConfig{
		JaccardThreshold:   0.7,
		MinKeywords:        2,
		IgnoreClosedVsOpen: true,
		MaxSuggestions:     20,
	}
}

// DuplicatePair represents a potential duplicate pair
type DuplicatePair struct {
	Issue1     string   `json:"issue1"`
	Issue2     string   `json:"issue2"`
	Similarity float64  `json:"similarity"`
	Method     string   `json:"method"` // "jaccard" or "semantic"
	Keywords   []string `json:"common_keywords,omitempty"`
}

// DetectDuplicates finds potential duplicate issues using keyword-based Jaccard similarity
// Optimized with an inverted index for performance on large repositories.
func DetectDuplicates(issues []model.Issue, config DuplicateConfig) []Suggestion {
	if len(issues) < 2 || config.MaxSuggestions <= 0 {
		return nil
	}

	// 1. Extract keywords for each issue and build Inverted Index
	// keywords[i] = unique keywords for issue i
	keywords := make([][]string, len(issues))
	// index[word] = list of issue indices containing that word
	index := make(map[string][]int)

	for i := range issues {
		kws := extractKeywords(issues[i].Title, issues[i].Description)
		keywords[i] = kws

		// Only index if enough keywords to matter
		if len(kws) >= config.MinKeywords {
			for _, w := range kws {
				index[w] = append(index[w], i)
			}
		}
	}

	var pairs []DuplicatePair

	// 2. Iterate through issues and find candidates
	for i := range issues {
		// Skip if this issue doesn't have enough keywords
		if len(keywords[i]) < config.MinKeywords {
			continue
		}

		// Count overlaps with other issues
		// candidateIdx -> intersection count
		overlaps := make(map[int]int)

		for _, w := range keywords[i] {
			for _, matchIdx := range index[w] {
				// Only look at j > i to avoid duplicates and self-compare
				if matchIdx > i {
					overlaps[matchIdx]++
				}
			}
		}

		// 3. Evaluate candidates
		for j, overlap := range overlaps {
			// Skip if other doesn't have enough keywords (redundant if index logic is strict, but safe)
			if len(keywords[j]) < config.MinKeywords {
				continue
			}

			// Jaccard = Intersection / Union
			// Union = |A| + |B| - |Intersection|
			union := len(keywords[i]) + len(keywords[j]) - overlap
			similarity := float64(overlap) / float64(union)

			if similarity < config.JaccardThreshold {
				continue
			}

			issue1 := &issues[i]
			issue2 := &issues[j]

			if issue1.Status == model.StatusTombstone || issue2.Status == model.StatusTombstone {
				continue
			}

			// Skip closed vs open pairs if configured
			if config.IgnoreClosedVsOpen {
				if isClosedLikeDuplicateStatus(issue1.Status) != isClosedLikeDuplicateStatus(issue2.Status) {
					continue
				}
			}

			// Reconstruct common keywords for display (only for passing pairs)
			common := intersectKeywords(keywords[i], keywords[j])

			issue1ID, issue2ID := issue1.ID, issue2.ID
			if issue2ID < issue1ID {
				issue1ID, issue2ID = issue2ID, issue1ID
			}
			pairs = append(pairs, DuplicatePair{
				Issue1:     issue1ID,
				Issue2:     issue2ID,
				Similarity: similarity,
				Method:     "jaccard",
				Keywords:   common,
			})
		}
	}

	// Sort by similarity (highest first) and limit
	sortPairsBySimilarity(pairs)
	if len(pairs) > config.MaxSuggestions {
		pairs = pairs[:config.MaxSuggestions]
	}

	// Issue lookup map for constructing suggestions
	issueMap := make(map[string]*model.Issue, len(issues))
	for i := range issues {
		issueMap[issues[i].ID] = &issues[i]
	}

	// Convert to suggestions
	suggestions := make([]Suggestion, 0, len(pairs))
	for _, pair := range pairs {
		issue1 := issueMap[pair.Issue1]
		issue2 := issueMap[pair.Issue2]

		sug := NewSuggestion(
			SuggestionPotentialDuplicate,
			pair.Issue1,
			fmt.Sprintf("Potential duplicate of %s", pair.Issue2),
			fmt.Sprintf("%.0f%% keyword similarity; common: %s",
				pair.Similarity*100,
				strings.Join(truncateStringSlice(pair.Keywords, 5), ", ")),
			pair.Similarity,
		).WithRelatedBead(pair.Issue2).WithMetadata("method", pair.Method)

		// Add action command if both are open
		if !isClosedLikeDuplicateStatus(issue1.Status) && !isClosedLikeDuplicateStatus(issue2.Status) {
			issue1Arg, issue1OK := quoteBeadsCommandID(pair.Issue1)
			issue2Arg, issue2OK := quoteBeadsCommandID(pair.Issue2)
			if issue1OK && issue2OK {
				sug = sug.WithAction(fmt.Sprintf("br dep add %s %s --type=related", issue1Arg, issue2Arg))
			}
		}

		suggestions = append(suggestions, sug)
	}

	return suggestions
}

// intersectKeywords finds common strings between two sorted/unsorted slices.
// Since extractKeywords returns unsorted unique lists, we can use a map or loops.
// Since we only call this on high-similarity pairs, performance is less critical than the main loop.
func intersectKeywords(a, b []string) []string {
	m := make(map[string]bool, len(a))
	for _, w := range a {
		m[w] = true
	}
	var common []string
	for _, w := range b {
		if m[w] {
			common = append(common, w)
		}
	}
	sort.Strings(common)
	return common
}

// extractKeywords extracts meaningful keywords from text
func extractKeywords(title, description string) []string {
	text := strings.ToLower(title + " " + description)

	// Remove common markdown/code artifacts (uses package-level compiled regex)
	text = nonWordRegex.ReplaceAllString(text, " ")

	words := strings.Fields(text)

	// Filter out stop words and short words
	keywords := make([]string, 0, len(words)/2)
	seen := make(map[string]bool)

	for _, word := range words {
		// Skip short words
		if len(word) < 3 {
			continue
		}

		// Skip common stop words (uses package-level map)
		if stopWords[word] {
			continue
		}

		// Deduplicate
		if seen[word] {
			continue
		}
		seen[word] = true
		keywords = append(keywords, word)
	}

	return keywords
}

// sortPairsBySimilarity sorts duplicate pairs by similarity (highest first)
// Uses sort.Slice for O(n log n) performance instead of bubble sort O(n²)
func sortPairsBySimilarity(pairs []DuplicatePair) {
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Similarity != pairs[j].Similarity {
			return pairs[i].Similarity > pairs[j].Similarity
		}
		if pairs[i].Issue1 != pairs[j].Issue1 {
			return pairs[i].Issue1 < pairs[j].Issue1
		}
		return pairs[i].Issue2 < pairs[j].Issue2
	})
}

// truncateStringSlice truncates a string slice to max length
func truncateStringSlice(s []string, max int) []string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// DuplicateDetector provides stateful duplicate detection with caching
type DuplicateDetector struct {
	config     DuplicateConfig
	mu         sync.RWMutex
	lastRun    time.Time
	lastResult []Suggestion
}

// NewDuplicateDetector creates a new detector with the given config
func NewDuplicateDetector(config DuplicateConfig) *DuplicateDetector {
	return &DuplicateDetector{
		config: config,
	}
}

// Detect finds potential duplicates
func (d *DuplicateDetector) Detect(issues []model.Issue) []Suggestion {
	result := DetectDuplicates(issues, d.config)
	d.mu.Lock()
	d.lastRun = time.Now()
	d.lastResult = result
	d.mu.Unlock()
	return result
}

// LastRun returns when detection was last performed
func (d *DuplicateDetector) LastRun() time.Time {
	d.mu.RLock()
	lastRun := d.lastRun
	d.mu.RUnlock()
	return lastRun
}
