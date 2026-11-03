// Package correlation provides explicit bead ID matching from commit messages.
package correlation

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"regexp"
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
		// Allow optional colon and whitespace, but do not treat a substring
		// such as "fix" in "suffix" as an action or truncate a hash-style ID.
		regexp.MustCompile(`(?i)\bcloses?\b:?\s*#?([A-Za-z]+-\d+)\b`),
		regexp.MustCompile(`(?i)\bfix(?:es|ed)?\b:?\s*#?([A-Za-z]+-\d+)\b`),
		regexp.MustCompile(`(?i)\brefs?\b:?\s*#?([A-Za-z]+-\d+)\b`),
		regexp.MustCompile(`(?i)\bresolves?\b:?\s*#?([A-Za-z]+-\d+)\b`),

		// beads-123 or bead-123 format (common for this project). The trailing
		// boundary keeps hash-style ids such as bv-8a4r from yielding a phantom
		// numeric id ("bv-8") that would link the commit to the wrong bead.
		regexp.MustCompile(`(?i)\bbeads?[-_](\d+)\b`),
		regexp.MustCompile(`(?i)\bbv[-_](\d+)\b`),

		// Generic numeric IDs, including lowercase conventional-commit scopes
		// such as feat(hist-1). Keep both word boundaries so a numeric prefix
		// inside a hash-style id or another identifier is never extracted.
		regexp.MustCompile(`\b([A-Za-z]{2,10}-\d+)\b`),
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

// policyFingerprint identifies the matcher that actually extracts references.
// Pattern order matters: the first match for an ID determines its match type
// and confidence. Length prefixes preserve boundaries even when a custom
// expression contains separators. Version the domain if normalization or
// scoring semantics change without changing the expressions themselves.
func (m *ExplicitMatcher) policyFingerprint() string {
	h := sha256.New()
	h.Write([]byte("bv-explicit-matcher-v1\x00"))
	for _, pattern := range m.patterns {
		expression := pattern.String()
		fmt.Fprintf(h, "%d:%s", len(expression), expression)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
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
	if _, err := fmt.Sscanf(id, "%d", new(int)); err == nil {
		return "bv-" + id
	}
	// The generic matcher also recognizes the established numeric bead/beads
	// aliases. Normalize them before deduplication so one reference cannot
	// create both bv-123 and beads-123 or lower its own confidence.
	id = strings.ToLower(id)
	for _, prefix := range []string{"bead-", "beads-"} {
		suffix, ok := strings.CutPrefix(id, prefix)
		if !ok || suffix == "" {
			continue
		}
		for _, digit := range suffix {
			if digit < '0' || digit > '9' {
				return id
			}
		}
		return "bv-" + suffix
	}
	return id
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
		matches, err := m.searchWithGrep(pattern, opts)
		if err != nil {
			// Non-fatal: try other patterns
			continue
		}

		for _, match := range matches {
			if !seen[match.CommitSHA] {
				seen[match.CommitSHA] = true
				allMatches = append(allMatches, match)
			}
		}
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

	patterns := []string{
		// Exact ID
		beadID,
		strings.ToUpper(beadID),
	}

	// If it's a bv-XXX style ID, also search for beads-XXX
	if strings.HasPrefix(id, "bv-") && numericPart != "" {
		patterns = append(patterns,
			"beads-"+numericPart,
			"bead-"+numericPart,
			"BEADS-"+numericPart,
			"BEAD-"+numericPart,
		)
	}

	return patterns
}

// searchWithGrep runs git log --grep and parses results.
func (m *ExplicitMatcher) searchWithGrep(pattern string, opts ExtractOptions) ([]ExplicitMatch, error) {
	args := []string{
		"log",
		"--grep=" + pattern,
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
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", opts.Limit))
	}
	if opts.Revision != "" {
		args = append(args, opts.Revision)
	}

	cmd := gitCommand(m.ctx, args...)
	cmd.Dir = m.repoPath

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// git log returns exit 0 even with no results, so this is a real error
			return nil, fmt.Errorf("git log --grep failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log --grep failed: %w", err)
	}

	return m.parseGrepOutput(out, pattern)
}

// parseGrepOutput parses git log output into ExplicitMatch structs.
func (m *ExplicitMatcher) parseGrepOutput(data []byte, searchPattern string) ([]ExplicitMatch, error) {
	var matches []ExplicitMatch

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
			continue
		}

		message := info.Message

		// Extract all IDs from this message
		idMatches := m.ExtractIDsFromMessage(message)

		// Calculate confidence based on match type and count
		confidence := 0.90
		var matchType string

		for _, idMatch := range idMatches {
			// Check if this ID matches what we searched for
			if strings.EqualFold(idMatch.ID, searchPattern) ||
				strings.Contains(strings.ToLower(idMatch.RawMatch), strings.ToLower(searchPattern)) {
				matchType = idMatch.MatchType
				confidence = CalculateConfidence(idMatch.MatchType, len(idMatches))
				break
			}
		}

		if matchType == "" {
			matchType = "generic"
		}

		matches = append(matches, ExplicitMatch{
			BeadID:      searchPattern,
			CommitSHA:   info.SHA,
			Message:     message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
			Timestamp:   info.Timestamp,
			MatchType:   matchType,
			Confidence:  confidence,
		})
	}

	return matches, scanner.Err()
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

// MatchWalkedCommits scans already-walked commits (subject and body) for bead
// ID references and returns one ExplicitMatch per (commit, id) pair, in walk
// order. It spawns no git subprocesses; the walk is the single bounded window
// shared with the temporal strategy and the orphan detector. beadFilter, when
// non-empty, keeps only matches for that id (case-insensitive) so a
// single-bead report does not carry every other bead's references. Unknown
// ids are NOT filtered here: the artifact is a pure function of HEAD, and
// assembleReport drops pairs whose bead does not exist in the current bead
// set.
func (m *ExplicitMatcher) MatchWalkedCommits(commits []walkedCommit, beadFilter string) []ExplicitMatch {
	var matches []ExplicitMatch
	for _, wc := range commits {
		ids := m.ExtractIDsFromMessage(wc.message())
		if len(ids) == 0 {
			continue
		}
		for _, id := range ids {
			if beadFilter != "" && !strings.EqualFold(id.ID, beadFilter) {
				continue
			}
			matches = append(matches, ExplicitMatch{
				BeadID:      id.ID,
				CommitSHA:   wc.SHA,
				Message:     wc.Subject,
				Author:      wc.Author,
				AuthorEmail: wc.AuthorEmail,
				Timestamp:   wc.Timestamp,
				MatchType:   id.MatchType,
				Confidence:  CalculateConfidence(id.MatchType, len(ids)),
			})
		}
	}
	return matches
}

// FindAllExplicitMatches finds explicit references for all known bead IDs.
func (m *ExplicitMatcher) FindAllExplicitMatches(beadIDs []string, opts ExtractOptions) (map[string][]ExplicitMatch, error) {
	results := make(map[string][]ExplicitMatch)

	for _, beadID := range beadIDs {
		matches, err := m.FindCommitsForBead(beadID, opts)
		if err != nil {
			// Non-fatal: continue with other beads
			continue
		}
		if len(matches) > 0 {
			results[beadID] = matches
		}
	}

	return results, nil
}
