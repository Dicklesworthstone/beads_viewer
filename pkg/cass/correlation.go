package cass

import (
	"context"
	"os"
	"path"
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
	searcher    *Searcher
	cache       *Cache
	workspace   string // Project workspace path for filtering
	maxSessions int
	cutoff      *time.Time

	// For testing: allow overriding time
	now func() time.Time
}

// NewCorrelator creates a new Correlator with the given dependencies.
func NewCorrelator(searcher *Searcher, cache *Cache, workspace string) *Correlator {
	return &Correlator{
		searcher:    searcher,
		cache:       cache,
		workspace:   workspace,
		maxSessions: MaxSessionsReturned,
		now:         time.Now,
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

// WithNow overrides the reference clock used for recency scoring and search windows.
func WithNow(now func() time.Time) CorrelatorOption {
	return func(c *Correlator) {
		if now != nil {
			c.now = now
		}
	}
}

// WithMaxSessions overrides the maximum number of sessions returned per correlation.
func WithMaxSessions(n int) CorrelatorOption {
	return func(c *Correlator) {
		if n > 0 {
			c.maxSessions = n
		}
	}
}

// WithCutoff sets an inclusive historical upper bound (e.g. --as-of commit timestamp).
// Sessions after cutoff or lacking a verifiable timestamp are excluded before ranking.
func WithCutoff(cutoff time.Time) CorrelatorOption {
	return func(c *Correlator) {
		if !cutoff.IsZero() {
			u := cutoff.UTC()
			c.cutoff = &u
		}
	}
}

func (c *Correlator) effectiveMaxSessions() int {
	if c != nil && c.maxSessions > 0 {
		return c.maxSessions
	}
	return MaxSessionsReturned
}

func (c *Correlator) rejectedByCutoff(ts time.Time) bool {
	return c != nil && c.cutoff != nil && (ts.IsZero() || ts.After(*c.cutoff))
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

func (c *Correlator) canUseSharedCache() bool {
	if c == nil || c.cache == nil {
		return false
	}
	if c.cutoff != nil {
		return false
	}
	if c.maxSessions > 0 && c.maxSessions != MaxSessionsReturned {
		return false
	}
	return true
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

	// Check cache first (only when default unfiltered parameters are in effect)
	if c.canUseSharedCache() {
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
		if c.canUseSharedCache() {
			c.cacheResult(issue.ID, &result)
		}
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
	idResults, idTotal, stop := c.searchByIssueIDs(ctx, issue, note)
	if len(idResults) > 0 {
		result.TotalFound = idTotal
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
		stop := note("keyword search", keywordMeta)
		if len(keywordResults) > 0 {
			result.TotalFound = keywordMeta.Total
			result.TopSessions = keywordResults
			result.Strategy = StrategyKeywords
			return finish()
		}
		if stop {
			return finish()
		}
	}

	// Strategy 3: Timestamp proximity (for closed or active beads with timestamps)
	if issue.ClosedAt != nil || !issue.UpdatedAt.IsZero() || !issue.CreatedAt.IsZero() {
		timestampResults, timestampMeta := c.searchByTimestamp(ctx, issue)
		note("timestamp search", timestampMeta)
		if len(timestampResults) > 0 {
			result.TotalFound = timestampMeta.Total
			result.TopSessions = timestampResults
			result.Strategy = StrategyTimestamp
		}
	}

	return finish()
}

func (c *Correlator) workspaceForIssue(issue *model.Issue) string {
	if issue != nil && issue.Origin != nil {
		if wd := strings.TrimSpace(issue.Origin.WorkingDirectory); wd != "" {
			return wd
		}
		if td := strings.TrimSpace(issue.Origin.TrackerDirectory); td != "" {
			cleanTD := filepath.Clean(td)
			base := filepath.Base(cleanTD)
			if base == ".beads" || base == "_beads" || strings.TrimSpace(issue.Origin.Tracker) != "" {
				if derived := strings.TrimSpace(WorkspaceFromBeadsPath(filepath.Join(cleanTD, "issues.jsonl"))); derived != "" {
					return derived
				}
			}
		}
	}
	return c.workspace
}

func candidateIssueIDs(issue *model.Issue) []string {
	if issue == nil {
		return nil
	}
	if issue.Origin != nil {
		if localID := strings.TrimSpace(issue.Origin.LocalID); localID != "" {
			if issue.ID != "" && issue.ID != localID {
				return []string{localID, issue.ID}
			}
			return []string{localID}
		}
	}
	if issue.ID != "" {
		return []string{issue.ID}
	}
	return nil
}

func isASCIIWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func isASCIIAlnumByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func containsTokenBoundedIssueID(combined, id string) bool {
	if id == "" || len(combined) < len(id) {
		return false
	}
	offset := 0
	for offset <= len(combined)-len(id) {
		rel := strings.Index(combined[offset:], id)
		if rel < 0 {
			return false
		}
		start := offset + rel
		end := start + len(id)
		offset = start + 1

		if start > 0 {
			prev := combined[start-1]
			if isASCIIWordByte(prev) || prev == '-' {
				continue
			}
			if prev == '.' && start >= 2 && isASCIIAlnumByte(combined[start-2]) {
				continue
			}
		}
		if end < len(combined) {
			next := combined[end]
			if isASCIIWordByte(next) {
				continue
			}
			if (next == '-' || next == '.') && end+1 < len(combined) && isASCIIAlnumByte(combined[end+1]) {
				continue
			}
		}
		return true
	}
	return false
}

func mentionsAnyIssueID(r SearchResult, ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	combined := strings.ToLower(r.Snippet + " " + r.Title)
	for _, id := range ids {
		trimmed := strings.ToLower(strings.TrimSpace(id))
		if trimmed != "" && containsTokenBoundedIssueID(combined, trimmed) {
			return true
		}
	}
	return false
}

func (c *Correlator) searchByIssueIDs(ctx context.Context, issue *model.Issue, note func(string, SearchMeta) bool) ([]ScoredResult, int, bool) {
	ws := c.workspaceForIssue(issue)
	ids := candidateIssueIDs(issue)
	if len(ids) == 0 {
		return nil, 0, false
	}

	type hitKey struct {
		path string
		line int
	}
	byKey := make(map[hitKey]ScoredResult)
	order := make([]hitKey, 0)
	totalFound := 0
	dupHits := 0
	stop := false

	for _, id := range ids {
		results, meta := c.searchByIDInWorkspaceRaw(ctx, id, ws)
		totalFound += meta.Total
		stop = note("ID search", meta)
		for _, sr := range results {
			k := hitKey{path: sr.SourcePath, line: sr.LineNumber}
			if prev, exists := byKey[k]; exists {
				dupHits++
				if sr.FinalScore > prev.FinalScore {
					byKey[k] = sr
				}
				continue
			}
			byKey[k] = sr
			order = append(order, k)
		}
		if stop {
			break
		}
	}

	if c.cutoff != nil {
		totalFound = len(order)
	} else if totalFound >= dupHits {
		totalFound -= dupHits
	}
	if len(order) == 0 {
		return nil, totalFound, stop
	}
	merged := make([]ScoredResult, 0, len(order))
	for _, k := range order {
		merged = append(merged, byKey[k])
	}
	return c.rankAndLimit(merged), totalFound, stop
}

func splitASCIIAlnumSubtokens(s string) []string {
	var tokens []string
	start := -1
	for i := 0; i < len(s); i++ {
		if isASCIIAlnumByte(s[i]) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			tokens = append(tokens, s[start:i])
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, s[start:])
	}
	return tokens
}

func findStandaloneAlnumToken(s, tok string, from int) int {
	if tok == "" || from < 0 || from+len(tok) > len(s) {
		return -1
	}
	offset := from
	for offset <= len(s)-len(tok) {
		rel := strings.Index(s[offset:], tok)
		if rel < 0 {
			return -1
		}
		start := offset + rel
		end := start + len(tok)
		leftOK := start == 0 || !isASCIIAlnumByte(s[start-1])
		rightOK := end == len(s) || !isASCIIAlnumByte(s[end])
		if leftOK && rightOK {
			return start
		}
		offset = start + 1
	}
	return -1
}

func hasInterveningASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if isASCIIAlnumByte(s[i]) {
			return true
		}
	}
	return false
}

func containsAllAlnumSubtokens(combinedLower, trimmedLower string) bool {
	subtokens := splitASCIIAlnumSubtokens(trimmedLower)
	if len(subtokens) < 2 {
		return false
	}
	const maxSubtokenGap = 32
	searchFrom := 0
	for {
		pos := findStandaloneAlnumToken(combinedLower, subtokens[0], searchFrom)
		if pos < 0 {
			return false
		}
		curEnd := pos + len(subtokens[0])
		matchedAll := true
		for _, tok := range subtokens[1:] {
			nextPos := findStandaloneAlnumToken(combinedLower, tok, curEnd)
			if nextPos <= curEnd || nextPos-curEnd > maxSubtokenGap || hasInterveningASCIIAlnum(combinedLower[curEnd:nextPos]) {
				matchedAll = false
				break
			}
			curEnd = nextPos + len(tok)
		}
		if matchedAll {
			return true
		}
		searchFrom = pos + 1
	}
}

func hasConflictingSubstringWithoutTokenMatch(r SearchResult, beadID string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(beadID))
	if trimmed == "" {
		return false
	}
	combined := strings.ToLower(r.Title + "\n" + r.Snippet)
	if !strings.Contains(combined, trimmed) && !containsAllAlnumSubtokens(combined, trimmed) {
		return false
	}
	return !mentionsAnyIssueID(r, []string{beadID})
}

func (c *Correlator) searchByIDInWorkspace(ctx context.Context, beadID, workspace string) ([]ScoredResult, SearchMeta) {
	scored, meta := c.searchByIDInWorkspaceRaw(ctx, beadID, workspace)
	return c.rankAndLimit(scored), meta
}

func (c *Correlator) searchByIDInWorkspaceRaw(ctx context.Context, beadID, workspace string) ([]ScoredResult, SearchMeta) {
	// Quote the ID for exact matching
	query := `"` + beadID + `"`

	limit := c.effectiveMaxSessions() * 2
	if c.cutoff != nil {
		limit = max(c.effectiveMaxSessions()*8, 50)
	}
	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     query,
		Limit:     limit,
		Workspace: workspace,
	})

	if len(resp.Results) == 0 {
		if c.cutoff != nil {
			resp.Meta.Total = 0
		}
		return nil, resp.Meta
	}

	meta := resp.Meta
	scored := make([]ScoredResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if c.rejectedByCutoff(r.Timestamp) || hasConflictingSubstringWithoutTokenMatch(r, beadID) {
			if meta.Total > 0 {
				meta.Total--
			}
			continue
		}
		score := float64(ScoreIDMention)
		score = c.applyTimeDecay(score, r.Timestamp)
		score = c.applyWorkspaceBoostFor(score, r.Workspace, workspace)

		if score >= MinScoreThreshold {
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    float64(ScoreIDMention),
				Strategy:     StrategyIDMention,
			})
		} else if meta.Total > 0 {
			meta.Total--
		}
	}
	if c.cutoff != nil {
		meta.Total = len(scored)
	}

	return scored, meta
}

// searchByKeywords searches for extracted keywords with time filtering.
func (c *Correlator) searchByKeywords(ctx context.Context, issue *model.Issue, keywords []string) ([]ScoredResult, SearchMeta) {
	// Build query from keywords
	query := strings.Join(keywords, " ")
	if query == "" {
		return nil, SearchMeta{}
	}

	// Calculate search window based on bead activity.
	// External `cass search --days` filters relative to wall-clock today; in
	// historical cutoff mode (--as-of), omit `--days` so older commits are not
	// dropped while rejectedByCutoff enforces the upper bound strictly.
	days := c.calculateSearchDays(issue)
	limit := c.effectiveMaxSessions() * 2
	if c.cutoff != nil {
		days = 0
		limit = max(c.effectiveMaxSessions()*8, 50)
	}
	workspace := c.workspaceForIssue(issue)
	ids := candidateIssueIDs(issue)

	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     query,
		Limit:     limit, // Get extra to filter
		Days:      days,
		Workspace: workspace,
	})

	if len(resp.Results) == 0 {
		if c.cutoff != nil {
			resp.Meta.Total = 0
		}
		return nil, resp.Meta
	}

	scored := make([]ScoredResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		if c.rejectedByCutoff(r.Timestamp) {
			if resp.Meta.Total > 0 {
				resp.Meta.Total--
			}
			continue
		}
		// Score based on how well keywords match (elevating literal mentions of LocalID or ID)
		baseScore := c.scoreKeywordMatch(r, keywords)
		if mentionsAnyIssueID(r, ids) && baseScore < float64(ScoreIDMention) {
			baseScore = float64(ScoreIDMention)
		}
		score := c.applyTimeDecay(baseScore, r.Timestamp)
		score = c.applyWorkspaceBoostFor(score, r.Workspace, workspace)

		if score >= MinScoreThreshold {
			matchedKeywords := c.findMatchedKeywords(r, keywords)
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    baseScore,
				Strategy:     StrategyKeywords,
				Keywords:     matchedKeywords,
			})
		} else if resp.Meta.Total > 0 {
			resp.Meta.Total--
		}
	}
	if c.cutoff != nil {
		resp.Meta.Total = len(scored)
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
	limit := c.effectiveMaxSessions() * 3
	if c.cutoff != nil {
		days = 0
		limit = max(c.effectiveMaxSessions()*8, 50)
	}
	workspace := c.workspaceForIssue(issue)
	ids := candidateIssueIDs(issue)

	// Search broadly - we're relying on timestamp for relevance
	resp := c.searcher.Search(ctx, SearchOptions{
		Query:     "*", // Match all within time window
		Limit:     limit,
		Days:      days,
		Workspace: workspace,
	})

	if len(resp.Results) == 0 {
		if days > 7 || c.cutoff != nil {
			resp.Meta.Total = 0
		}
		return nil, resp.Meta
	}

	scored := make([]ScoredResult, 0, len(resp.Results))
	rejected := 0
	for _, r := range resp.Results {
		if c.rejectedByCutoff(r.Timestamp) {
			rejected++
			if resp.Meta.Total > 0 {
				resp.Meta.Total--
			}
			continue
		}
		baseScore := c.scoreTimestampProximity(r.Timestamp, issue)
		if mentionsAnyIssueID(r, ids) && baseScore < float64(ScoreIDMention) {
			baseScore = float64(ScoreIDMention)
		}
		score := baseScore
		if baseScore > 0 {
			score = c.applyWorkspaceBoostFor(baseScore, r.Workspace, workspace)
		}

		if score >= MinScoreThreshold {
			scored = append(scored, ScoredResult{
				SearchResult: r,
				FinalScore:   score,
				BaseScore:    baseScore,
				Strategy:     StrategyTimestamp,
			})
		} else {
			rejected++
			if resp.Meta.Total > 0 {
				resp.Meta.Total--
			}
		}
	}

	filtered := c.rankAndLimit(scored)
	if c.cutoff != nil || days > 7 || rejected > 0 {
		resp.Meta.Total = len(scored)
	}
	return filtered, resp.Meta
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

// calculateSearchDays determines the time window (in days back from now) for searching cass sessions.
// Because `cass search --days N` filters relative to wall-clock today, both open and closed
// beads must measure from issue.CreatedAt to c.now() (+ 7d buffer), clamped to [7, 365].
func (c *Correlator) calculateSearchDays(issue *model.Issue) int {
	if issue == nil || issue.CreatedAt.IsZero() {
		return 30 // Default
	}

	nowFn := c.now
	if nowFn == nil {
		nowFn = time.Now
	}
	days := int(nowFn().Sub(issue.CreatedAt).Hours()/24) + 7
	if days < 7 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	return days
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
	if sessionTime.IsZero() || issue == nil {
		return 0
	}
	var referenceTime time.Time

	// Prefer closed_at if available, otherwise updated_at, then created_at
	if issue.ClosedAt != nil && !issue.ClosedAt.IsZero() {
		referenceTime = *issue.ClosedAt
	} else if !issue.UpdatedAt.IsZero() {
		referenceTime = issue.UpdatedAt
	} else if !issue.CreatedAt.IsZero() {
		referenceTime = issue.CreatedAt
	} else {
		return 0
	}

	// Calculate time difference
	diff := sessionTime.Sub(referenceTime)
	if diff < 0 {
		diff = -diff // Absolute value
	}

	hours := diff.Hours()

	// Score based on proximity (only sessions within 7d qualify on timestamp alone)
	if hours <= 24 {
		return float64(ScorePartialKeyword) + float64(BonusRecent24h)
	}
	if hours <= 24*7 {
		return float64(ScorePartialKeyword) + float64(BonusRecent7d)
	}

	return 0
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
	return c.applyWorkspaceBoostFor(score, sourcePath, c.workspace)
}

func (c *Correlator) applyWorkspaceBoostFor(score float64, sourcePath, targetWorkspace string) float64 {
	if targetWorkspace == "" || sourcePath == "" {
		return score
	}

	// Normalize paths for comparison
	workspace := filepath.Clean(targetWorkspace)
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
	maxSess := c.effectiveMaxSessions()
	if len(results) > maxSess {
		results = results[:maxSess]
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
// Empty paths and git:<rev> historical pseudo-paths resolve via os.Getwd() so
// TUI --as-of sessions stay anchored to the repository instead of resolving to "..".
func WorkspaceFromBeadsPath(beadsPath string) string {
	trimmed := strings.TrimSpace(beadsPath)
	if trimmed == "" || strings.HasPrefix(trimmed, "git:") {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		return cwd
	}
	if isWindowsDrivePath(trimmed) {
		normalized := path.Clean(strings.ReplaceAll(trimmed, "\\", "/"))
		dir := path.Dir(normalized)
		return path.Dir(dir)
	}
	if !filepath.IsAbs(trimmed) {
		if abs, err := filepath.Abs(trimmed); err == nil {
			trimmed = abs
		}
	}
	// /path/to/project/.beads/beads.jsonl → /path/to/project
	dir := filepath.Dir(trimmed) // → /path/to/project/.beads
	return filepath.Dir(dir)     // → /path/to/project
}

func isWindowsDrivePath(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	return ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) &&
		p[1] == ':' && (p[2] == '/' || p[2] == '\\')
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
