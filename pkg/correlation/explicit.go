// Package correlation provides explicit bead ID matching from commit messages.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ExplicitMatcher finds commits that explicitly reference bead IDs in messages.
type ExplicitMatcher struct {
	repoPath string
	patterns []*regexp.Regexp

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// the matcher (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the matcher so its git subprocesses are cancelled
// when ctx is done (issue #166). Returns the receiver for chaining.
func (m *ExplicitMatcher) WithContext(ctx context.Context) *ExplicitMatcher {
	m.ctx = ctx
	return m
}

// customIDPatterns holds user-supplied bead ID patterns registered via
// SetCustomIDPatterns (the --id-pattern CLI flag, #188). Guarded by a RWMutex
// per the shared-state convention; writes happen once at CLI startup.
var (
	customIDPatternsMu sync.RWMutex
	customIDPatterns   []*regexp.Regexp
)

// SetCustomIDPatterns registers additional bead ID patterns used alongside the
// defaults by every message-based ID matcher (ExplicitMatcher, orphan
// detection). This is how the --id-pattern CLI flag supports trackers whose
// IDs don't have numeric suffixes (e.g. beadhive's bh-8g6cj) (#188).
// Patterns may capture the ID in group 1; patterns without a capture group
// match the ID as the whole expression.
func SetCustomIDPatterns(patterns []*regexp.Regexp) {
	customIDPatternsMu.Lock()
	defer customIDPatternsMu.Unlock()
	customIDPatterns = make([]*regexp.Regexp, len(patterns))
	copy(customIDPatterns, patterns)
}

// CustomIDPatterns returns the registered custom bead ID patterns (may be empty).
func CustomIDPatterns() []*regexp.Regexp {
	customIDPatternsMu.RLock()
	defer customIDPatternsMu.RUnlock()
	out := make([]*regexp.Regexp, len(customIDPatterns))
	copy(out, customIDPatterns)
	return out
}

// DefaultPatterns returns the default set of bead ID patterns, plus any
// custom patterns registered via SetCustomIDPatterns (#188).
func DefaultPatterns() []*regexp.Regexp {
	return append(builtinPatterns(), CustomIDPatterns()...)
}

// builtinPatterns returns the built-in bead ID patterns.
func builtinPatterns() []*regexp.Regexp {
	return []*regexp.Regexp{
		// [ID] format - very explicit
		regexp.MustCompile(`\[([A-Za-z]+-\d+)\]`),

		// Closes/Fixes/Refs keywords with optional # prefix
		// Note: Allow optional colon and whitespace after keyword
		regexp.MustCompile(`(?i)closes?:?\s*#?([A-Za-z]+-\d+)`),
		regexp.MustCompile(`(?i)fix(?:es|ed)?:?\s*#?([A-Za-z]+-\d+)`),
		regexp.MustCompile(`(?i)refs?:?\s*#?([A-Za-z]+-\d+)`),
		regexp.MustCompile(`(?i)resolves?:?\s*#?([A-Za-z]+-\d+)`),

		// beads-123 or bead-123 format (common for this project)
		regexp.MustCompile(`(?i)beads?[-_](\d+)`),
		regexp.MustCompile(`(?i)bv[-_](\d+)`),

		// Generic ID at word boundary (PROJECT-123 style)
		regexp.MustCompile(`\b([A-Z]{2,10}-\d+)\b`),
	}
}

// NewExplicitMatcher creates a new explicit matcher with default patterns.
func NewExplicitMatcher(repoPath string) *ExplicitMatcher {
	return &ExplicitMatcher{
		repoPath: repoPath,
		patterns: DefaultPatterns(),
	}
}

// NewExplicitMatcherWithPatterns creates a matcher with custom patterns.
func NewExplicitMatcherWithPatterns(repoPath string, patterns []*regexp.Regexp) *ExplicitMatcher {
	return &ExplicitMatcher{
		repoPath: repoPath,
		patterns: patterns,
	}
}

// AddPattern adds a custom pattern to the matcher.
func (m *ExplicitMatcher) AddPattern(pattern *regexp.Regexp) {
	m.patterns = append(m.patterns, pattern)
}

// ExplicitMatch represents a bead ID found in a commit message.
type ExplicitMatch struct {
	BeadID      string
	CommitSHA   string
	Message     string
	Author      string
	AuthorEmail string
	Timestamp   time.Time
	MatchType   string // "closes", "fixes", "refs", "bracket", "generic"
	Confidence  float64
}

// ExtractIDsFromMessage extracts all bead IDs from a commit message.
// Ordering: matches are returned in the order patterns are evaluated; we also keep stable ID ordering for predictability.
func (m *ExplicitMatcher) ExtractIDsFromMessage(message string) []IDMatch {
	var matches []IDMatch
	seen := make(map[string]bool)

	for _, pattern := range m.patterns {
		found := pattern.FindAllStringSubmatch(message, -1)
		for _, match := range found {
			// Prefer capture group 1 when the pattern defines one; custom
			// patterns without a capture group match the whole expression
			// as the ID (#188).
			raw := ""
			if len(match) >= 2 && match[1] != "" {
				raw = match[1]
			} else if len(match) >= 1 {
				raw = match[0]
			}
			if raw == "" {
				continue
			}
			id := normalizeBeadID(raw)
			if !seen[id] {
				seen[id] = true
				matchType := classifyMatch(match[0])
				matches = append(matches, IDMatch{
					ID:        id,
					MatchType: matchType,
					RawMatch:  match[0],
				})
			}
		}
	}

	return matches
}

// IDMatch represents a single ID match from a message.
type IDMatch struct {
	ID        string
	MatchType string
	RawMatch  string
}

// normalizeBeadID normalizes a bead ID to a consistent format.
func normalizeBeadID(id string) string {
	// Handle numeric-only IDs (from beads-123 pattern)
	numericOnly := id != ""
	for i := 0; i < len(id) && numericOnly; i++ {
		numericOnly = id[i] >= '0' && id[i] <= '9'
	}
	if numericOnly {
		return "bv-" + id
	}
	// Convert to lowercase for consistency
	return strings.ToLower(id)
}

// classifyMatch determines the type of match based on the raw match string.
func classifyMatch(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "close"):
		return "closes"
	case strings.Contains(lower, "fix"):
		return "fixes"
	case strings.Contains(lower, "ref"):
		return "refs"
	case strings.Contains(lower, "resolve"):
		return "resolves"
	case strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]"):
		return "bracket"
	case strings.HasPrefix(lower, "bead") || strings.HasPrefix(lower, "bv"):
		return "bead"
	default:
		return "generic"
	}
}

// CalculateConfidence calculates confidence for an explicit match.
func CalculateConfidence(matchType string, totalMatches int) float64 {
	// Base confidence for explicit ID mention
	base := 0.90

	// Bonus for action keywords
	switch matchType {
	case "closes", "fixes", "resolves":
		base += 0.05 // Strong intent signal
	case "bracket":
		base += 0.02 // Explicit but no action
	case "refs":
		base += 0.01 // Just a reference
	case "bead":
		base += 0.03 // Project-specific format
	}

	// Penalty for multiple IDs in same message (less specific)
	if totalMatches > 1 {
		base -= 0.02 * float64(totalMatches-1)
	}

	// Clamp to reasonable bounds
	if base > 0.99 {
		base = 0.99
	}
	if base < 0.70 {
		base = 0.70
	}

	return base
}

// FindCommitsForBead finds all commits that explicitly reference a bead ID.
func (m *ExplicitMatcher) FindCommitsForBead(beadID string, opts ExtractOptions) ([]ExplicitMatch, error) {
	// Use git log --grep to efficiently find commits mentioning this ID
	patterns := m.buildGrepPatterns(beadID)

	var allMatches []ExplicitMatch
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		matches, err := m.searchWithGrep(beadID, pattern, opts)
		if err != nil {
			return nil, fmt.Errorf("searching commits for bead %q with pattern %q: %w", beadID, pattern, err)
		}

		for _, match := range matches {
			if !seen[match.CommitSHA] {
				seen[match.CommitSHA] = true
				allMatches = append(allMatches, match)
			}
		}
	}

	// Limit is a bound on accepted exact matches, not on the fixed-string grep
	// candidates. Git's fixed-string search also returns longer IDs (bv-420 for
	// bv-42), so applying -n before parseGrepOutput validates the wrong prefix
	// window and can hide an older exact match. Merge, order, and trim only after
	// exact-ID filtering and deduplication.
	sort.Slice(allMatches, func(i, j int) bool {
		if !allMatches[i].Timestamp.Equal(allMatches[j].Timestamp) {
			return allMatches[i].Timestamp.After(allMatches[j].Timestamp)
		}
		return allMatches[i].CommitSHA < allMatches[j].CommitSHA
	})
	if opts.Limit > 0 && len(allMatches) > opts.Limit {
		allMatches = allMatches[:opts.Limit]
	}

	return allMatches, nil
}

// buildGrepPatterns creates grep patterns for a bead ID.
func (m *ExplicitMatcher) buildGrepPatterns(beadID string) []string {
	// Normalize the ID
	id := strings.ToLower(beadID)

	// Extract numeric part if present
	var numericPart string
	if idx := strings.LastIndex(id, "-"); idx != -1 {
		numericPart = id[idx+1:]
	}

	candidates := []string{
		// Exact ID
		beadID,
	}

	// If it's a bv-XXX style ID, also search for beads-XXX
	if strings.HasPrefix(id, "bv-") && numericPart != "" {
		candidates = append(candidates,
			"beads-"+numericPart,
			"bead-"+numericPart,
		)
	}

	// searchWithGrep is case-insensitive; retain one spelling per semantic
	// pattern so aliases do not spawn redundant Git subprocesses.
	patterns := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		key := strings.ToLower(candidate)
		if !seen[key] {
			seen[key] = true
			patterns = append(patterns, candidate)
		}
	}
	return patterns
}

// searchWithGrep runs git log --grep and parses results.
func (m *ExplicitMatcher) searchWithGrep(beadID, pattern string, opts ExtractOptions) ([]ExplicitMatch, error) {
	args := []string{
		"--grep=" + pattern,
		"--fixed-strings",
		"-i", // Case insensitive
		"--format=" + gitLogHeaderFormat,
	}

	// Add time filters
	if opts.Since != nil {
		args = append(args, fmt.Sprintf("--since=%s", opts.Since.Format(time.RFC3339)))
	}
	if opts.Until != nil {
		args = append(args, fmt.Sprintf("--until=%s", opts.Until.Format(time.RFC3339)))
	}
	cmd := lifecycleGitLogCommand(m.ctx, m.repoPath, args...)

	out, err := cmd.Output()
	if err != nil {
		if m.ctx != nil {
			if ctxErr := m.ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("git log --grep canceled: %w", ctxErr)
			}
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			// git log returns exit 0 even with no results, so this is a real error
			return nil, fmt.Errorf("git log --grep failed: %w: %s", err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log --grep failed: %w", err)
	}

	return m.parseGrepOutput(out, beadID, pattern)
}

// parseGrepOutput parses git log output into ExplicitMatch structs.
func (m *ExplicitMatcher) parseGrepOutput(data []byte, beadID, _ string) ([]ExplicitMatch, error) {
	var matches []ExplicitMatch
	objectIDWidth := 0
	canonicalTarget := normalizeBeadID(beadID)

	scanner := bufio.NewScanner(bytes.NewReader(data))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		info, err := parseCommitInfo(line)
		if err != nil {
			return nil, fmt.Errorf("parse git grep commit header: %w", err)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(info.SHA)
		} else if len(info.SHA) != objectIDWidth {
			return nil, fmt.Errorf("mixed-width grep commit object IDs: got %d and %d characters", objectIDWidth, len(info.SHA))
		}

		message := info.Message

		// Extract all IDs from this message
		idMatches := m.ExtractIDsFromMessage(message)

		// Calculate confidence based on match type and count
		confidence := 0.90
		var matchType string

		for _, idMatch := range idMatches {
			// --grep is only a candidate prefilter: fixed-string search still
			// matches bv-42 inside bv-420. Accept the commit only when the ID
			// parser found an exact canonical target (including aliases such as
			// beads-42 -> bv-42).
			if strings.EqualFold(normalizeBeadID(idMatch.ID), canonicalTarget) {
				matchType = idMatch.MatchType
				confidence = CalculateConfidence(idMatch.MatchType, len(idMatches))
				break
			}
		}
		if matchType == "" && containsBeadID(message, canonicalTarget) {
			// The caller supplied a known canonical ID, while the built-in
			// discovery patterns intentionally recognize only common tracker
			// shapes. Accept an exact literal token even when that discovery
			// grammar cannot rediscover punctuation-bearing or lowercase IDs.
			matchType = "literal"
			confidence = CalculateConfidence(matchType, 1)
		}

		if matchType == "" {
			continue
		}

		matches = append(matches, ExplicitMatch{
			// searchPattern may be an alias (for example beads-42). Preserve the
			// canonical ID requested by FindCommitsForBead so downstream report
			// assembly never links the commit to a nonexistent alias bead.
			BeadID:      beadID,
			CommitSHA:   info.SHA,
			Message:     message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
			Timestamp:   info.Timestamp,
			MatchType:   matchType,
			Confidence:  confidence,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan git grep output: %w", err)
	}
	return matches, nil
}

// CreateCorrelatedCommit converts an ExplicitMatch to a CorrelatedCommit.
func (m *ExplicitMatcher) CreateCorrelatedCommit(match ExplicitMatch, coCommitter *CoCommitExtractor) CorrelatedCommit {
	// Create a BeadEvent to get file information
	event := BeadEvent{
		BeadID:      match.BeadID,
		CommitSHA:   match.CommitSHA,
		CommitMsg:   match.Message,
		Author:      match.Author,
		AuthorEmail: match.AuthorEmail,
		Timestamp:   match.Timestamp,
	}

	// Try to get file changes
	var files []FileChange
	if coCommitter != nil {
		files, _ = coCommitter.ExtractCoCommittedFiles(event)
	}

	reason := fmt.Sprintf("Commit message explicitly references %s (%s)", match.BeadID, match.MatchType)

	return CorrelatedCommit{
		BeadID:      match.BeadID,
		SHA:         match.CommitSHA,
		ShortSHA:    shortSHA(match.CommitSHA),
		Message:     match.Message,
		Author:      match.Author,
		AuthorEmail: match.AuthorEmail,
		Timestamp:   match.Timestamp,
		Files:       files,
		Method:      MethodExplicitID,
		Confidence:  match.Confidence,
		Reason:      reason,
	}
}

// FindAllExplicitMatches finds explicit references for all known bead IDs.
func (m *ExplicitMatcher) FindAllExplicitMatches(beadIDs []string, opts ExtractOptions) (map[string][]ExplicitMatch, error) {
	results := make(map[string][]ExplicitMatch)

	for _, beadID := range beadIDs {
		matches, err := m.FindCommitsForBead(beadID, opts)
		if err != nil {
			return nil, fmt.Errorf("finding explicit matches for bead %q: %w", beadID, err)
		}
		if len(matches) > 0 {
			results[beadID] = matches
		}
	}

	return results, nil
}
