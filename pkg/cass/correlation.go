package cass

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Scoring constants - all values are tunable based on real-world usage.
const (
	// Base scores by match type
	ScoreIDMention      = 100 // Definitive evidence
	ScoreExactKeyword   = 50  // Strong thematic link
	ScorePartialKeyword = 30  // Probable relation

	// Multipliers
	MultiplierSameWorkspace = 2.0 // Critical for relevance

	// Time decay bonuses
	BonusRecent24h = 20  // Session within 24 hours of bead activity
	BonusRecent7d  = 10  // Session within 7 days
	PenaltyOld30d  = -10 // Session older than 30 days

	// Thresholds
	MinScoreThreshold    = 25 // Below this, don't show session
	MaxSessionsReturned  = 3  // Top N sessions per bead
	MaxKeywordsExtracted = 5  // Keywords from title
)

// stopWords contains common words to filter out during keyword extraction.
var stopWords = map[string]bool{
	// Articles and prepositions
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"for": true, "to": true, "in": true, "on": true, "at": true,
	"of": true, "with": true, "by": true, "from": true, "as": true,
	"is": true, "it": true, "be": true, "was": true, "are": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true,
	"this": true, "that": true, "these": true, "those": true,
	"not": true, "no": true, "but": true, "if": true, "then": true,

	// Common development action words (too generic)
	"fix": true, "add": true, "update": true, "remove": true,
	"implement": true, "create": true, "delete": true, "change": true,
	"make": true, "use": true, "set": true, "get": true,
	"refactor": true, "clean": true, "move": true, "rename": true,

	// Common development nouns (too generic)
	"bug": true, "issue": true, "feature": true, "task": true,
	"file": true, "code": true, "test": true, "error": true,
	"new": true, "old": true, "all": true, "some": true,
}

// CorrelationStrategy identifies which strategy produced a correlation.
type CorrelationStrategy string

const (
	StrategyIDMention CorrelationStrategy = "id_mention"
	StrategyKeywords  CorrelationStrategy = "keywords"
	StrategyTimestamp CorrelationStrategy = "timestamp"
	StrategyCombined  CorrelationStrategy = "combined"
)

// ScoredResult wraps a SearchResult with correlation scoring metadata.
type ScoredResult struct {
	SearchResult
	FinalScore float64             // Combined score after all adjustments
	BaseScore  float64             // Score before multipliers
	Strategy   CorrelationStrategy // Which strategy matched
	Keywords   []string            // Keywords that matched (if strategy = keywords)
}

// CorrelationResult contains the full correlation output for a bead.
type CorrelationResult struct {
	BeadID        string              // Which bead this is for
	TopSessions   []ScoredResult      // Up to MaxSessionsReturned best matches
	TotalFound    int                 // Total results before filtering
	Strategy      CorrelationStrategy // Primary strategy that produced results
	Keywords      []string            // Keywords used (for display)
	ComputeTimeMs int                 // Time spent computing correlation
	Error         string              // Diagnostic if any search failed or returned partial results
	// TimedOut reports that the caller's time budget ran out before the
	// strategies finished, so an empty result is not "no sessions".
	TimedOut bool
}

// Correlator intelligently matches cass sessions to beads using multiple strategies.
type Correlator struct {
	searcher  *Searcher
	cache     *Cache
	workspace string // Project workspace path for filtering

	// For testing: allow overriding time
	now func() time.Time
}

// NewCorrelator creates a new Correlator with the given dependencies.
func NewCorrelator(searcher *Searcher, cache *Cache, workspace string) *Correlator {
	return &Correlator{
		searcher:  searcher,
		cache:     cache,
		workspace: workspace,
		now:       time.Now,
	}
}

// CorrelatorOption configures a Correlator.
type CorrelatorOption func(*Correlator)

// WithWorkspace sets the workspace path for filtering.
func WithWorkspace(path string) CorrelatorOption {
	return func(c *Correlator) {
		c.workspace = path
	}
}

// NewCorrelatorWithOptions creates a Correlator with custom options.
func NewCorrelatorWithOptions(searcher *Searcher, cache *Cache, opts ...CorrelatorOption) *Correlator {
	c := NewCorrelator(searcher, cache, "")
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// GetCached returns the cached correlation hint for a bead without triggering a new search.
// Returns nil if not cached or expired. This is useful for status bar indicators
// where we don't want to block on network/process calls.
func (c *Correlator) GetCached(beadID string) *CorrelationHint {
	if c.cache == nil {
		return nil
	}
	return c.cache.Get(beadID)
}

// Correlate finds sessions relevant to the given bead.
// It tries multiple strategies in order of confidence:
// 1. ID mention (highest confidence)
// 2. Keyword extraction (medium confidence)
// 3. Timestamp proximity (lower confidence)
func (c *Correlator) Correlate(ctx context.Context, issue *model.Issue) CorrelationResult {
	if issue == nil {
		return CorrelationResult{}
	}

	start := c.now()

	// Check cache first
	if c.cache != nil {
		if hint := c.cache.Get(issue.ID); hint != nil {
			return CorrelationResult{
				BeadID:        issue.ID,
				TopSessions:   hint.Results,
				TotalFound:    hint.ResultCount,
				Strategy:      CorrelationStrategy(hint.QueryUsed),
				Keywords:      hint.Keywords,
				ComputeTimeMs: 0, // Cache hit
			}
		}
	}

	result := CorrelationResult{
		BeadID: issue.ID,
	}
	var diagnostics []string
	finish := func() CorrelationResult {
		result.Error = strings.Join(diagnostics, "; ")
		result.ComputeTimeMs = int(c.now().Sub(start).Milliseconds())
		c.cacheResult(issue.ID, &result)
		return result
	}
	// note records a strategy's diagnostic and reports whether the budget is
	// spent: the strategies share ctx, so later ones cannot run either.
	note := func(strategy string, meta SearchMeta) bool {
		if meta.Error != "" {
			diagnostics = append(diagnostics, strategy+": "+meta.Error)
		}
		if meta.TimedOut {
			result.TimedOut = true
		}
		return ctx.Err() != nil
	}

	// Strategy 1: ID mention search (definitive match)
	idResults, idMeta := c.searchByID(ctx, issue.ID)
	result.TotalFound = idMeta.Total
	stop := note("ID search", idMeta)
	if len(idResults) > 0 {
		result.TopSessions = idResults
		result.Strategy = StrategyIDMention
		return finish()
	}
	if stop {
		return finish()
	}

	// Strategy 2: Keyword extraction and search
	keywords := ExtractKeywords(issue.Title)
	result.Keywords = keywords

	if len(keywords) > 0 {
		keywordResults, keywordMeta := c.searchByKeywords(ctx, issue, keywords)
		result.TotalFound = keywordMeta.Total
		stop := note("keyword search", keywordMeta)
		if len(keywordResults) > 0 {
			result.TopSessions = keywordResults
			result.Strategy = StrategyKeywords
			return finish()
		}
		if stop {
			return finish()
		}
	}

	// Strategy 3: Timestamp proximity (for closed beads)
	if issue.ClosedAt != nil || !issue.CreatedAt.IsZero() {
		timestampResults, timestampMeta := c.searchByTimestamp(ctx, issue)
		result.TotalFound = timestampMeta.Total
		note("timestamp search", timestampMeta)
		if len(timestampResults) > 0 {
			result.TopSessions = timestampResults
			result.Strategy = StrategyTimestamp
		}
	}

	return finish()
}

// searchByID searches for the bead ID literally in sessions.
func (c *Correlator) searchByID(ctx context.Context, beadID string) ([]ScoredResult, SearchMeta) {
	// Quote the ID for exact matching
	query := `"` + beadID + `"`

	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     query,
		Limit:     MaxSessionsReturned,
		Workspace: c.workspace,
	})

	if len(resp.Results) == 0 {
		return nil, resp.Meta
	}

	scored := make([]ScoredResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		score := float64(ScoreIDMention)
		score = c.applyTimeDecay(score, r.Timestamp)
		score = c.applyWorkspaceBoost(score, r.Workspace)

		if score >= MinScoreThreshold {
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    float64(ScoreIDMention),
				Strategy:     StrategyIDMention,
			})
		}
	}

	return c.rankAndLimit(scored), resp.Meta
}

// searchByKeywords searches for extracted keywords with time filtering.
func (c *Correlator) searchByKeywords(ctx context.Context, issue *model.Issue, keywords []string) ([]ScoredResult, SearchMeta) {
	// Build query from keywords
	query := strings.Join(keywords, " ")
	if query == "" {
		return nil, SearchMeta{}
	}

	// Calculate search window based on bead activity
	days := c.calculateSearchDays(issue)

	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     query,
		Limit:     MaxSessionsReturned * 2, // Get extra to filter
		Days:      days,
		Workspace: c.workspace,
	})

	if len(resp.Results) == 0 {
		return nil, resp.Meta
	}

	scored := make([]ScoredResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		// Score based on how well keywords match
		baseScore := c.scoreKeywordMatch(r, keywords)
		score := c.applyTimeDecay(baseScore, r.Timestamp)
		score = c.applyWorkspaceBoost(score, r.Workspace)

		if score >= MinScoreThreshold {
			matchedKeywords := c.findMatchedKeywords(r, keywords)
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    baseScore,
				Strategy:     StrategyKeywords,
				Keywords:     matchedKeywords,
			})
		}
	}

	return c.rankAndLimit(scored), resp.Meta
}

// searchByTimestamp finds sessions near bead activity dates.
func (c *Correlator) searchByTimestamp(ctx context.Context, issue *model.Issue) ([]ScoredResult, SearchMeta) {
	// Use a broader search when we have no keywords
	days := c.calculateSearchDays(issue)
	if days == 0 {
		days = 7 // Default to a week
	}

	// Search broadly - we're relying on timestamp for relevance
	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     "*", // Match all within time window
		Limit:     MaxSessionsReturned * 3,
		Days:      days,
		Workspace: c.workspace,
	})

	if len(resp.Results) == 0 {
		return nil, resp.Meta
	}

	scored := make([]ScoredResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		baseScore := c.scoreTimestampProximity(r.Timestamp, issue)
		score := c.applyWorkspaceBoost(baseScore, r.Workspace)

		if score >= MinScoreThreshold {
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    baseScore,
				Strategy:     StrategyTimestamp,
			})
		}
	}

	return c.rankAndLimit(scored), resp.Meta
}

// ExtractKeywords extracts meaningful search keywords from text.
// It removes stop words, short words, and limits to MaxKeywordsExtracted.
func ExtractKeywords(text string) []string {
	if text == "" {
		return nil
	}

	// Normalize: lowercase and split on non-alphanumeric
	text = strings.ToLower(text)
	words := splitIntoWords(text)

	// Filter and dedupe
	seen := make(map[string]bool)
	keywords := make([]string, 0, len(words))

	for _, word := range words {
		// Skip short words
		if len(word) < 3 {
			continue
		}

		// Skip stop words
		if stopWords[word] {
			continue
		}

		// Skip if already seen
		if seen[word] {
			continue
		}
		seen[word] = true

		keywords = append(keywords, word)

		// Limit keywords
		if len(keywords) >= MaxKeywordsExtracted {
			break
		}
	}

	return keywords
}

// splitIntoWords splits text into individual words.
func splitIntoWords(text string) []string {
	// Split on any non-letter, non-digit character
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// calculateSearchDays determines the time window for searching.
func (c *Correlator) calculateSearchDays(issue *model.Issue) int {
	// For closed issues, search the window between creation and closure
	if issue.ClosedAt != nil && !issue.CreatedAt.IsZero() {
		days := int(issue.ClosedAt.Sub(issue.CreatedAt).Hours() / 24)
		if days < 1 {
			days = 1
		}
		// Add buffer
		return days + 7
	}

	// For open issues, use time since creation
	if !issue.CreatedAt.IsZero() {
		days := int(c.now().Sub(issue.CreatedAt).Hours() / 24)
		if days < 1 {
			days = 7
		}
		if days > 90 {
			days = 90 // Cap at 90 days
		}
		return days
	}

	return 30 // Default
}

// scoreKeywordMatch scores how well a result matches keywords.
func (c *Correlator) scoreKeywordMatch(result SearchResult, keywords []string) float64 {
	if len(keywords) == 0 {
		return 0
	}

	// Check how many keywords are present in title or snippet
	snippet := strings.ToLower(result.Snippet)
	title := strings.ToLower(result.Title)
	combined := snippet + " " + title

	matchCount := 0
	for _, kw := range keywords {
		if strings.Contains(combined, kw) {
			matchCount++
		}
	}

	if matchCount == 0 {
		return 0
	}

	// Score based on match percentage
	matchRatio := float64(matchCount) / float64(len(keywords))

	if matchRatio >= 0.8 {
		return float64(ScoreExactKeyword)
	}
	return float64(ScorePartialKeyword) * matchRatio
}

// findMatchedKeywords finds which keywords are in the result.
func (c *Correlator) findMatchedKeywords(result SearchResult, keywords []string) []string {
	combined := strings.ToLower(result.Snippet + " " + result.Title)
	matched := make([]string, 0)

	for _, kw := range keywords {
		if strings.Contains(combined, kw) {
			matched = append(matched, kw)
		}
	}

	return matched
}

// scoreTimestampProximity scores based on how close the session is to bead activity.
func (c *Correlator) scoreTimestampProximity(sessionTime time.Time, issue *model.Issue) float64 {
	var referenceTime time.Time

	// Prefer closed_at if available, otherwise updated_at, then created_at
	if issue.ClosedAt != nil {
		referenceTime = *issue.ClosedAt
	} else if !issue.UpdatedAt.IsZero() {
		referenceTime = issue.UpdatedAt
	} else if !issue.CreatedAt.IsZero() {
		referenceTime = issue.CreatedAt
	} else {
		return float64(ScorePartialKeyword) / 2 // Low baseline
	}

	// Calculate time difference
	diff := sessionTime.Sub(referenceTime)
	if diff < 0 {
		diff = -diff // Absolute value
	}

	hours := diff.Hours()

	// Score based on proximity
	if hours <= 24 {
		return float64(ScorePartialKeyword) + float64(BonusRecent24h)
	}
	if hours <= 24*7 {
		return float64(ScorePartialKeyword) + float64(BonusRecent7d)
	}
	if hours > 24*30 {
		return float64(ScorePartialKeyword) + float64(PenaltyOld30d)
	}

	return float64(ScorePartialKeyword)
}

// applyTimeDecay adjusts score based on session recency.
func (c *Correlator) applyTimeDecay(score float64, sessionTime time.Time) float64 {
	if sessionTime.IsZero() {
		return score
	}

	hours := c.now().Sub(sessionTime).Hours()

	if hours <= 24 {
		return score + float64(BonusRecent24h)
	}
	if hours <= 24*7 {
		return score + float64(BonusRecent7d)
	}
	if hours > 24*30 {
		return score + float64(PenaltyOld30d)
	}

	return score
}

// applyWorkspaceBoost multiplies score if session is in the same workspace.
func (c *Correlator) applyWorkspaceBoost(score float64, sourcePath string) float64 {
	if c.workspace == "" || sourcePath == "" {
		return score
	}

	// Normalize paths for comparison
	workspace := filepath.Clean(c.workspace)
	source := filepath.Clean(sourcePath)

	// Check if sourcePath is under workspace (primary check)
	if strings.HasPrefix(source, workspace+string(filepath.Separator)) || source == workspace {
		return score * MultiplierSameWorkspace
	}

	// Fallback: check if the workspace directory name appears as a path component
	// This handles cases where paths are relative or have different roots
	workspaceBase := filepath.Base(workspace)
	if workspaceBase != "" && workspaceBase != "." && workspaceBase != "/" {
		// Check for the base name as a complete path component (bounded by separators)
		sep := string(filepath.Separator)
		if strings.Contains(source, sep+workspaceBase+sep) ||
			strings.HasPrefix(source, workspaceBase+sep) ||
			strings.HasSuffix(source, sep+workspaceBase) {
			return score * MultiplierSameWorkspace
		}
	}

	return score
}

// rankAndLimit sorts by score descending and limits to MaxSessionsReturned.
func (c *Correlator) rankAndLimit(results []ScoredResult) []ScoredResult {
	if len(results) == 0 {
		return nil
	}

	// Sort by FinalScore descending
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].FinalScore > results[i].FinalScore {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	// Limit
	if len(results) > MaxSessionsReturned {
		results = results[:MaxSessionsReturned]
	}

	return results
}

// cacheResult stores correlation result in cache.
func (c *Correlator) cacheResult(beadID string, result *CorrelationResult) {
	if c.cache == nil || result.Error != "" {
		return
	}

	// Convert to CorrelationHint for caching
	hint := &CorrelationHint{
		BeadID:      beadID,
		Results:     result.TopSessions,
		QueryUsed:   string(result.Strategy),
		Keywords:    result.Keywords,
		ResultCount: result.TotalFound,
	}

	c.cache.Set(beadID, hint)
}

// WorkspaceFromBeadsPath extracts the workspace directory from a beads.jsonl path.
func WorkspaceFromBeadsPath(beadsPath string) string {
	// /path/to/project/.beads/beads.jsonl → /path/to/project
	dir := filepath.Dir(beadsPath) // → /path/to/project/.beads
	return filepath.Dir(dir)       // → /path/to/project
}

// beadIDRegex matches bead IDs like "bv-abc123"
var beadIDRegex = regexp.MustCompile(`\b(bv-[a-z0-9]+)\b`)

// FindBeadIDMentions finds all bead ID mentions in text.
func FindBeadIDMentions(text string) []string {
	matches := beadIDRegex.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	// Dedupe
	seen := make(map[string]bool)
	unique := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
	}

	return unique
}
