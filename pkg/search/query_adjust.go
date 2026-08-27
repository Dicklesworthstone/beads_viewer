package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	shortQueryTokenLimit        = 2
	shortQueryRuneLimit         = 12
	shortQueryMinTextWeight     = 0.55
	hybridCandidateMin          = 200
	hybridCandidateMinShort     = 300
	hybridCandidateDefaultLimit = 10
)

// QueryStats summarizes simple heuristics about a search query.
type QueryStats struct {
	Tokens  int
	Length  int
	IsShort bool
}

// AnalyzeQuery returns lightweight stats used to tune hybrid scoring heuristics.
func AnalyzeQuery(query string) QueryStats {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return QueryStats{Tokens: 0, Length: 0, IsShort: true}
	}
	tokens := countTokens(trimmed)
	length := utf8.RuneCountInString(trimmed)
	isShort := tokens <= shortQueryTokenLimit || length <= shortQueryRuneLimit
	return QueryStats{Tokens: tokens, Length: length, IsShort: isShort}
}

// IsShortQuery reports whether the query should favor literal text matching.
func IsShortQuery(query string) bool {
	return AnalyzeQuery(query).IsShort
}

// AdjustWeightsForQuery boosts text relevance for short queries to prevent
// unrelated high-impact items from dominating hybrid ranking.
func AdjustWeightsForQuery(weights Weights, query string) Weights {
	if !IsShortQuery(query) {
		return weights
	}
	if weights.TextRelevance >= shortQueryMinTextWeight {
		return weights
	}

	target := shortQueryMinTextWeight
	if target >= 1.0 {
		return Weights{TextRelevance: 1.0}
	}

	remaining := weights.sum() - weights.TextRelevance
	if remaining <= 0 {
		return Weights{TextRelevance: 1.0}
	}

	scale := (1.0 - target) / remaining
	adjusted := Weights{
		TextRelevance: target,
		PageRank:      weights.PageRank * scale,
		Status:        weights.Status * scale,
		Impact:        weights.Impact * scale,
		Priority:      weights.Priority * scale,
		Recency:       weights.Recency * scale,
	}
	return adjusted.Normalize()
}

// HybridCandidateLimit returns the number of candidates to consider for hybrid re-ranking.
// It widens the candidate pool for short queries to improve recall of literal matches.
func HybridCandidateLimit(limit int, total int, query string) int {
	if limit <= 0 {
		limit = hybridCandidateDefaultLimit
	}
	if total <= 0 {
		return 0
	}

	maxInt := int(^uint(0) >> 1)
	base := maxInt
	if limit <= maxInt/3 {
		base = limit * 3
	}
	min := hybridCandidateMin
	if IsShortQuery(query) {
		min = hybridCandidateMinShort
	}
	candidate := base
	if candidate < min {
		candidate = min
	}
	if candidate > total {
		candidate = total
	}
	return candidate
}

func countTokens(query string) int {
	inToken := false
	count := 0
	for _, r := range query {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inToken {
				count++
				inToken = true
			}
			continue
		}
		inToken = false
	}
	return count
}
