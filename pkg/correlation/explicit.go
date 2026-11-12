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
	"sort"
	"strings"
	"sync"
	"time"
)

// ExplicitMatcher finds commits that explicitly reference bead IDs in messages.
//
// When known Bead IDs are supplied (via SetKnownBeadIDs, NewExplicitMatcherWithKnownIDs,
// Correlator, or OrphanDetector), ExplicitMatcher compiles a deterministic,
// zero-CGO Aho-Corasick (1975) trie + failure-link automaton over the lowered
// vocabulary V = lower(live Bead IDs ∪ historical JSONL event Bead IDs),
// scanning commit messages in a single O(|M| + |Z|) pass with strict
// identifier-token boundary algebra and maximal-munch dot-hierarchy rules.
// When V is empty (e.g. standalone unit callers with no dictionary), it falls
// back cleanly to compiled prefix regexes.
type ExplicitMatcher struct {
	mu                  sync.RWMutex
	repoPath            string
	patterns            []*regexp.Regexp
	builtinCount        int
	usesDefaultPatterns bool
	extraPatterns       []*regexp.Regexp

	knownIDs  []string
	automaton *ahoCorasick

	// ctx, when set via WithContext, bounds the git subprocesses spawned by
	// the matcher (issue #166). nil means context.Background().
	ctx context.Context
}

// WithContext binds ctx to the matcher so its git subprocesses are cancelled
// when ctx is done (issue #166). Returns the receiver for chaining.
func (m *ExplicitMatcher) WithContext(ctx context.Context) *ExplicitMatcher {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
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
	builtins := builtinPatterns()
	custom := CustomIDPatterns()
	patterns := make([]*regexp.Regexp, 0, len(builtins)+len(custom))
	patterns = append(patterns, builtins...)
	patterns = append(patterns, custom...)
	return &ExplicitMatcher{
		repoPath:            repoPath,
		patterns:            patterns,
		builtinCount:        len(builtins),
		usesDefaultPatterns: true,
		extraPatterns:       custom,
	}
}

// NewExplicitMatcherWithKnownIDs creates a matcher configured with an Aho-Corasick
// dictionary over knownIDs (plus any registered custom patterns).
func NewExplicitMatcherWithKnownIDs(repoPath string, knownIDs []string) *ExplicitMatcher {
	m := NewExplicitMatcher(repoPath)
	m.SetKnownBeadIDs(knownIDs)
	return m
}

// NewExplicitMatcherWithPatterns creates a matcher with custom patterns.
func NewExplicitMatcherWithPatterns(repoPath string, patterns []*regexp.Regexp) *ExplicitMatcher {
	copied := make([]*regexp.Regexp, len(patterns))
	copy(copied, patterns)
	return &ExplicitMatcher{
		repoPath:            repoPath,
		patterns:            copied,
		usesDefaultPatterns: false,
		extraPatterns:       copied,
	}
}

// AddPattern adds a custom pattern to the matcher.
func (m *ExplicitMatcher) AddPattern(pattern *regexp.Regexp) {
	if pattern == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patterns = append(m.patterns, pattern)
	m.extraPatterns = append(m.extraPatterns, pattern)
}

// SetKnownBeadIDs configures the finite vocabulary of known Bead IDs for the
// Aho-Corasick automaton. When ids is empty, the matcher falls back to its
// compiled regex patterns. Returns the receiver for chaining.
func (m *ExplicitMatcher) SetKnownBeadIDs(ids []string) *ExplicitMatcher {
	canonical := canonicalizeKnownIDList(ids)

	m.mu.RLock()
	same := stringSlicesEqual(m.knownIDs, canonical)
	m.mu.RUnlock()
	if same {
		return m
	}

	var ac *ahoCorasick
	if len(canonical) > 0 {
		ac = buildAhoCorasick(canonical)
	}

	m.mu.Lock()
	m.knownIDs = canonical
	m.automaton = ac
	m.mu.Unlock()
	return m
}

// withMergedKnownIDs returns an ExplicitMatcher that recognizes the union of
// m's known IDs and groups without mutating m's own policy state.
func (m *ExplicitMatcher) withMergedKnownIDs(groups ...[]string) *ExplicitMatcher {
	m.mu.RLock()
	basePatterns := append([]*regexp.Regexp(nil), m.patterns...)
	extraPatterns := append([]*regexp.Regexp(nil), m.extraPatterns...)
	baseKnown := m.knownIDs
	baseAuto := m.automaton
	repoPath := m.repoPath
	builtinCount := m.builtinCount
	usesDefault := m.usesDefaultPatterns
	ctx := m.ctx
	m.mu.RUnlock()

	total := len(baseKnown)
	for _, g := range groups {
		total += len(g)
	}
	mergedInput := make([]string, 0, total)
	mergedInput = append(mergedInput, baseKnown...)
	for _, g := range groups {
		mergedInput = append(mergedInput, g...)
	}
	canonical := canonicalizeKnownIDList(mergedInput)

	ac := baseAuto
	if !stringSlicesEqual(baseKnown, canonical) {
		if len(canonical) > 0 {
			ac = buildAhoCorasick(canonical)
		} else {
			ac = nil
		}
	}

	return &ExplicitMatcher{
		repoPath:            repoPath,
		patterns:            basePatterns,
		builtinCount:        builtinCount,
		usesDefaultPatterns: usesDefault,
		extraPatterns:       extraPatterns,
		knownIDs:            canonical,
		automaton:           ac,
		ctx:                 ctx,
	}
}

func canonicalizeKnownIDList(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// policyFingerprint identifies the matcher that actually extracts references.
// Pattern order matters: the first match for an ID determines its match type
// and confidence. Length prefixes preserve boundaries even when a custom
// expression contains separators. Version the domain if normalization or
// scoring semantics change without changing the expressions themselves.
func (m *ExplicitMatcher) policyFingerprint() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	h := sha256.New()
	h.Write([]byte("bv-explicit-matcher-v2\x00"))
	for _, pattern := range m.patterns {
		expression := pattern.String()
		fmt.Fprintf(h, "%d:%s", len(expression), expression)
	}
	h.Write([]byte("\x00known\x00"))
	for _, id := range m.knownIDs {
		fmt.Fprintf(h, "%d:%s", len(id), id)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// acUnusedClass marks bytes that do not appear in any dictionary pattern.
const acUnusedClass = uint8(0xFF)

type acPattern struct {
	canonicalID string
	patLen      int
	ambiguous   bool
}

// ahoCorasick implements Aho & Corasick (1975) multi-pattern string matching
// as a completed deterministic finite automaton with dictionary suffix links.
type ahoCorasick struct {
	charClass   [256]uint8
	alphaSize   int
	trans       []int32
	fail        []int32
	dictLink    []int32
	patIdx      []int32
	hasOutput   []bool
	patterns    []acPattern
	hasNonASCII bool
}

type acSpanMatch struct {
	l           int
	r           int
	canonicalID string
}

func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isASCIIAlnum(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// hasValidBeadIDBoundaries enforces the left and right identifier-token
// boundary algebra on message[l..r] (inclusive byte indices [l, r]):
//   - Left (l > 0): reject ASCII alphanumeric, '_', '-', '.', '/', '@'.
//   - Right (r+1 < len): reject ASCII alphanumeric, '_', '-', '/', '@', and
//     reject '.' ONLY when followed immediately by an ASCII alphanumeric byte
//     (allowing sentence-final "Refs bv-pzpv." and "Refs bv-apal.20." while
//     vetoing shorter prefix "bv-apal" inside "Fixes bv-apal.20").
func hasValidBeadIDBoundaries(message string, l, r int) bool {
	if l < 0 || r < l || r >= len(message) {
		return false
	}
	if l > 0 {
		prev := message[l-1]
		if isASCIIAlnum(prev) || prev == '_' || prev == '-' || prev == '.' || prev == '/' || prev == '@' {
			return false
		}
	}
	if r+1 < len(message) {
		next := message[r+1]
		if isASCIIAlnum(next) || next == '_' || next == '-' || next == '/' || next == '@' {
			return false
		}
		if next == '.' && r+2 < len(message) && isASCIIAlnum(message[r+2]) {
			return false
		}
	}
	return true
}

// buildAhoCorasick constructs the Aho-Corasick trie, failure links, dictionary
// output links, and completed transition table in O(sum_{w in V} |w|) time.
func buildAhoCorasick(sortedKnownIDs []string) *ahoCorasick {
	lowerToCanonical := make(map[string]string, len(sortedKnownIDs))
	ambiguous := make(map[string]bool)

	for _, id := range sortedKnownIDs {
		if id == "" {
			continue
		}
		low := strings.ToLower(id)
		if prev, exists := lowerToCanonical[low]; exists {
			if prev != id {
				ambiguous[low] = true
			}
		} else {
			lowerToCanonical[low] = id
		}
	}

	if len(lowerToCanonical) == 0 {
		return nil
	}

	// Register established numeric bead-<N> / beads-<N> aliases for unambiguous
	// bv-<N> IDs when those alias strings are not distinct IDs themselves.
	baseKeys := make([]string, 0, len(lowerToCanonical))
	for low := range lowerToCanonical {
		baseKeys = append(baseKeys, low)
	}
	sort.Strings(baseKeys)
	for _, low := range baseKeys {
		if ambiguous[low] {
			continue
		}
		digits, ok := strings.CutPrefix(low, "bv-")
		if !ok || !isAllASCIIDigits(digits) {
			continue
		}
		canon := lowerToCanonical[low]
		for _, prefix := range []string{"bead-", "beads-"} {
			alias := prefix + digits
			if _, exists := lowerToCanonical[alias]; !exists {
				lowerToCanonical[alias] = canon
			}
		}
	}

	allKeys := make([]string, 0, len(lowerToCanonical))
	totalBytes := 0
	var usedBytes [256]bool
	hasNonASCII := false
	for low := range lowerToCanonical {
		allKeys = append(allKeys, low)
		totalBytes += len(low)
		for i := 0; i < len(low); i++ {
			b := low[i]
			usedBytes[b] = true
			if b >= 0x80 {
				hasNonASCII = true
			}
		}
	}
	sort.Strings(allKeys)

	var charClass [256]uint8
	for i := range charClass {
		charClass[i] = acUnusedClass
	}
	alphaSize := 0
	for b := 0; b < 256; b++ {
		if usedBytes[b] {
			charClass[b] = uint8(alphaSize)
			alphaSize++
		}
	}
	if alphaSize == 0 {
		return nil
	}

	estNodes := totalBytes/2 + 16
	trans := make([]int32, 0, estNodes*alphaSize)
	fail := make([]int32, 0, estNodes)
	dictLink := make([]int32, 0, estNodes)
	patIdx := make([]int32, 0, estNodes)
	hasOutput := make([]bool, 0, estNodes)
	numNodes := int32(0)

	addNode := func() int32 {
		id := numNodes
		numNodes++
		for c := 0; c < alphaSize; c++ {
			trans = append(trans, -1)
		}
		fail = append(fail, 0)
		dictLink = append(dictLink, 0)
		patIdx = append(patIdx, -1)
		hasOutput = append(hasOutput, false)
		return id
	}

	addNode() // root = 0

	patterns := make([]acPattern, len(allKeys))
	for i, key := range allKeys {
		patterns[i] = acPattern{
			canonicalID: lowerToCanonical[key],
			patLen:      len(key),
			ambiguous:   ambiguous[key],
		}
		cur := int32(0)
		for j := 0; j < len(key); j++ {
			cls := int(charClass[key[j]])
			edgeIdx := int(cur)*alphaSize + cls
			next := trans[edgeIdx]
			if next == -1 {
				next = addNode()
				trans[edgeIdx] = next
			}
			cur = next
		}
		patIdx[cur] = int32(i)
		hasOutput[cur] = true
	}

	// BFS construction of failure links, dictionary suffix links, and DFA goto transitions.
	queue := make([]int32, 0, numNodes)
	for c := 0; c < alphaSize; c++ {
		if u := trans[c]; u != -1 {
			fail[u] = 0
			queue = append(queue, u)
		} else {
			trans[c] = 0
		}
	}

	for head := 0; head < len(queue); head++ {
		u := queue[head]
		f := fail[u]
		if patIdx[f] >= 0 {
			dictLink[u] = f
		} else {
			dictLink[u] = dictLink[f]
		}
		if dictLink[u] > 0 {
			hasOutput[u] = true
		}

		uBase := int(u) * alphaSize
		fBase := int(f) * alphaSize
		for c := 0; c < alphaSize; c++ {
			if v := trans[uBase+c]; v != -1 {
				fail[v] = trans[fBase+c]
				queue = append(queue, v)
			} else {
				trans[uBase+c] = trans[fBase+c]
			}
		}
	}

	return &ahoCorasick{
		charClass:   charClass,
		alphaSize:   alphaSize,
		trans:       trans,
		fail:        fail,
		dictLink:    dictLink,
		patIdx:      patIdx,
		hasOutput:   hasOutput,
		patterns:    patterns,
		hasNonASCII: hasNonASCII,
	}
}

// search scans message in O(|M| + |Z|) time and returns boundary-valid,
// maximal-munch non-overlapping matches in left-to-right message order.
func (ac *ahoCorasick) search(message string) []acSpanMatch {
	if ac == nil || len(message) == 0 {
		return nil
	}

	scanStr := message
	if ac.hasNonASCII {
		lowered := strings.ToLower(message)
		if len(lowered) == len(message) {
			scanStr = lowered
		}
	}

	var candidates []acSpanMatch
	state := int32(0)
	alphaSize := ac.alphaSize

	for r := 0; r < len(scanStr); r++ {
		b := scanStr[r]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		cls := ac.charClass[b]
		if cls == acUnusedClass {
			state = 0
			continue
		}
		state = ac.trans[int(state)*alphaSize+int(cls)]
		if !ac.hasOutput[state] {
			continue
		}
		for out := state; out > 0; out = ac.dictLink[out] {
			pIdx := ac.patIdx[out]
			if pIdx < 0 {
				continue
			}
			pat := ac.patterns[pIdx]
			if pat.ambiguous {
				continue
			}
			l := r - pat.patLen + 1
			if !hasValidBeadIDBoundaries(message, l, r) {
				continue
			}
			candidates = append(candidates, acSpanMatch{
				l:           l,
				r:           r,
				canonicalID: pat.canonicalID,
			})
		}
	}

	if len(candidates) <= 1 {
		return candidates
	}

	// Maximal-munch interval disambiguation: prefer strictly longer spans,
	// breaking ties by leftmost start index, and discard overlapped subspans.
	sort.Slice(candidates, func(i, j int) bool {
		lenI := candidates[i].r - candidates[i].l
		lenJ := candidates[j].r - candidates[j].l
		if lenI != lenJ {
			return lenI > lenJ
		}
		if candidates[i].l != candidates[j].l {
			return candidates[i].l < candidates[j].l
		}
		return candidates[i].canonicalID < candidates[j].canonicalID
	})

	selected := make([]acSpanMatch, 0, len(candidates))
	for _, cand := range candidates {
		overlaps := false
		for _, kept := range selected {
			if cand.l <= kept.r && kept.l <= cand.r {
				overlaps = true
				break
			}
		}
		if !overlaps {
			selected = append(selected, cand)
		}
	}

	sort.Slice(selected, func(i, j int) bool {
		return selected[i].l < selected[j].l
	})
	return selected
}

// ExplicitMatch represents a bead ID found in a commit message.
type ExplicitMatch struct {
	BeadID      string
	CommitSHA   string
	Message     string
	Author      string
	AuthorEmail string
	Timestamp   time.Time
	MatchType   string // "closes", "fixes", "resolves", "scope", "bead", "bracket", "refs", "generic"
	Confidence  float64
}

// ExtractIDsFromMessage extracts all bead IDs from a commit message.
// When known Bead IDs are configured on the matcher, it scans message in a
// single Aho-Corasick O(|M| + |Z|) pass with boundary algebra + maximal-munch
// disambiguation (plus any user-supplied custom patterns). When no known IDs
// are configured, it falls back cleanly to compiled prefix regexes.
func (m *ExplicitMatcher) ExtractIDsFromMessage(message string) []IDMatch {
	m.mu.RLock()
	ac := m.automaton
	patterns := m.patterns
	builtinCount := m.builtinCount
	usesDefault := m.usesDefaultPatterns
	extraPatterns := m.extraPatterns
	knownIDs := m.knownIDs
	m.mu.RUnlock()

	if ac != nil {
		var matches []IDMatch
		idxByLower := make(map[string]int)

		spans := ac.search(message)
		for _, sp := range spans {
			matchType, rawMatch := classifyContextualSpan(message, sp.l, sp.r, sp.canonicalID)
			key := strings.ToLower(sp.canonicalID)
			if existingIdx, ok := idxByLower[key]; ok {
				if CalculateConfidence(matchType, 1) > CalculateConfidence(matches[existingIdx].MatchType, 1) {
					matches[existingIdx].MatchType = matchType
					matches[existingIdx].RawMatch = rawMatch
				}
				continue
			}
			idxByLower[key] = len(matches)
			matches = append(matches, IDMatch{
				ID:        sp.canonicalID,
				MatchType: matchType,
				RawMatch:  rawMatch,
			})
		}

		if len(extraPatterns) > 0 {
			canonByLower := make(map[string]string, len(knownIDs))
			for _, kid := range knownIDs {
				low := strings.ToLower(kid)
				if prev, exists := canonByLower[low]; exists && prev != kid {
					canonByLower[low] = "" // ambiguous
				} else {
					canonByLower[low] = kid
				}
			}
			for _, pattern := range extraPatterns {
				found := pattern.FindAllStringSubmatch(message, -1)
				for _, match := range found {
					raw := ""
					if len(match) >= 2 && match[1] != "" {
						raw = match[1]
					} else if len(match) >= 1 {
						raw = match[0]
					}
					if raw == "" {
						continue
					}
					norm := normalizeBeadID(raw)
					key := strings.ToLower(norm)
					id := norm
					if canon, ok := canonByLower[key]; ok {
						if canon == "" {
							continue
						}
						id = canon
					}
					matchType := classifyMatch(match[0])
					if existingIdx, ok := idxByLower[key]; ok {
						if CalculateConfidence(matchType, 1) > CalculateConfidence(matches[existingIdx].MatchType, 1) {
							matches[existingIdx].MatchType = matchType
							matches[existingIdx].RawMatch = match[0]
						}
						continue
					}
					idxByLower[key] = len(matches)
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

	var matches []IDMatch
	seen := make(map[string]bool)

	for i, pattern := range patterns {
		isBuiltin := usesDefault && i < builtinCount
		if isBuiltin {
			expr := pattern.String()
			wholeTokenGroup0 := strings.Contains(expr, `(\d+)`)
			locs := pattern.FindAllStringSubmatchIndex(message, -1)
			for _, loc := range locs {
				raw := ""
				tokStart, tokEnd := -1, -1
				if len(loc) >= 4 && loc[2] >= 0 && loc[3] > loc[2] {
					raw = message[loc[2]:loc[3]]
					if wholeTokenGroup0 {
						tokStart, tokEnd = loc[0], loc[1]
					} else {
						tokStart, tokEnd = loc[2], loc[3]
					}
				} else if len(loc) >= 2 && loc[0] >= 0 && loc[1] > loc[0] {
					raw = message[loc[0]:loc[1]]
					tokStart, tokEnd = loc[0], loc[1]
				}
				if raw == "" || !hasValidBeadIDBoundaries(message, tokStart, tokEnd-1) {
					continue
				}
				fullRaw := message[loc[0]:loc[1]]
				id := normalizeBeadID(raw)
				if !seen[id] {
					seen[id] = true
					matchType := classifyMatch(fullRaw)
					matches = append(matches, IDMatch{
						ID:        id,
						MatchType: matchType,
						RawMatch:  fullRaw,
					})
				}
			}
			continue
		}

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

// classifyContextualSpan inspects surrounding message context around message[l..r]
// to assign contextual MatchType and RawMatch for an Aho-Corasick match.
func classifyContextualSpan(message string, l, r int, canonicalID string) (string, string) {
	idText := message[l : r+1]
	bracketed := l > 0 && message[l-1] == '[' && r+1 < len(message) && message[r+1] == ']'

	// 1. Action keyword immediately preceding optional '[' / '#' / ':' / whitespace:
	//    Closes, Fixes, Fixed, Fix, Resolves, Resolved, Resolve, Refs, Ref
	pos := l
	rawEnd := r + 1
	hadDelim := false
	if bracketed {
		pos--
		rawEnd = r + 2
		hadDelim = true
	}
	if pos > 0 && message[pos-1] == '#' {
		pos--
		hadDelim = true
	}
	wsEnd := pos
	for pos > 0 && (message[pos-1] == ' ' || message[pos-1] == '\t') {
		pos--
	}
	hadSep := pos < wsEnd || hadDelim
	if pos > 0 && message[pos-1] == ':' {
		pos--
		hadSep = true
	}
	if hadSep && pos > 0 {
		kwEnd := pos
		kwStart := kwEnd
		for kwStart > 0 && isASCIILetter(message[kwStart-1]) {
			kwStart--
		}
		if kwStart < kwEnd && (kwStart == 0 || (!isASCIIAlnum(message[kwStart-1]) && message[kwStart-1] != '_' && message[kwStart-1] != '-')) {
			switch strings.ToLower(message[kwStart:kwEnd]) {
			case "close", "closes", "closed":
				return "closes", message[kwStart:rawEnd]
			case "fix", "fixes", "fixed":
				return "fixes", message[kwStart:rawEnd]
			case "ref", "refs":
				return "refs", message[kwStart:rawEnd]
			case "resolve", "resolves", "resolved":
				return "resolves", message[kwStart:rawEnd]
			}
		}
	}

	// 2. Bracket syntax: [bv-apal.20]
	if bracketed {
		return "bracket", message[l-1 : r+2]
	}

	// 3. Conventional commit scope: <type>(<id>)!?:
	if l > 0 && message[l-1] == '(' && r+1 < len(message) && message[r+1] == ')' {
		afterParen := r + 2
		if afterParen < len(message) && message[afterParen] == '!' {
			afterParen++
		}
		if afterParen < len(message) && message[afterParen] == ':' {
			typeEnd := l - 1
			typeStart := typeEnd
			for typeStart > 0 && isASCIILetter(message[typeStart-1]) {
				typeStart--
			}
			if typeStart < typeEnd && (typeStart == 0 || message[typeStart-1] == '\n' || message[typeStart-1] == '\r' || message[typeStart-1] == ' ' || message[typeStart-1] == '\t') {
				return "scope", message[typeStart : afterParen+1]
			}
		}
	}

	// 4. Standalone Beads-prefixed vs generic reference
	lowRaw := strings.ToLower(idText)
	lowCanon := strings.ToLower(canonicalID)
	if strings.HasPrefix(lowRaw, "bv-") || strings.HasPrefix(lowRaw, "bd-") || strings.HasPrefix(lowRaw, "br-") || strings.HasPrefix(lowRaw, "bead") ||
		strings.HasPrefix(lowCanon, "bv-") || strings.HasPrefix(lowCanon, "bd-") || strings.HasPrefix(lowCanon, "br-") {
		return "bead", idText
	}
	return "generic", idText
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
	if isAllASCIIDigits(id) {
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

	// Bonus for action keywords and structured context
	switch matchType {
	case "closes", "fixes", "resolves":
		base += 0.05 // Strong intent signal
	case "scope":
		base += 0.04 // Conventional commit scope feat(id):
	case "bead":
		base += 0.03 // Project-specific format
	case "bracket":
		base += 0.02 // Explicit bracket [id]
	case "refs":
		base += 0.01 // Explicit reference
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
	if beadID == "" {
		return nil, nil
	}
	targetMatcher := m.withMergedKnownIDs([]string{beadID})
	patterns := targetMatcher.buildGrepPatterns(beadID)

	var allMatches []ExplicitMatch
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		matches, err := targetMatcher.searchWithGrepForBead(pattern, beadID, opts)
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

	// If it's a bv-XXX numeric style ID, also search for beads-XXX
	if strings.HasPrefix(id, "bv-") && isAllASCIIDigits(numericPart) {
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
	return m.searchWithGrepForBead(pattern, pattern, opts)
}

func (m *ExplicitMatcher) searchWithGrepForBead(pattern, targetBeadID string, opts ExtractOptions) ([]ExplicitMatch, error) {
	m.mu.RLock()
	ctx := m.ctx
	repoPath := m.repoPath
	m.mu.RUnlock()

	args := []string{
		"log",
		"--grep=" + pattern,
		"-F", // Fixed-string literal match (safe for dot-hierarchical IDs like bv-apal.20)
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

	cmd := gitCommand(ctx, args...)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// git log returns exit 0 even with no results, so this is a real error
			return nil, fmt.Errorf("git log --grep failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log --grep failed: %w", err)
	}

	return m.parseGrepOutputForBead(out, targetBeadID)
}

// parseGrepOutput parses git log output into ExplicitMatch structs.
func (m *ExplicitMatcher) parseGrepOutput(data []byte, searchPattern string) ([]ExplicitMatch, error) {
	return m.withMergedKnownIDs([]string{searchPattern}).parseGrepOutputForBead(data, searchPattern)
}

func (m *ExplicitMatcher) parseGrepOutputForBead(data []byte, targetBeadID string) ([]ExplicitMatch, error) {
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
		idMatches := m.ExtractIDsFromMessage(message)

		var matched *IDMatch
		for i := range idMatches {
			if strings.EqualFold(idMatches[i].ID, targetBeadID) {
				matched = &idMatches[i]
				break
			}
		}
		if matched == nil {
			continue
		}

		matches = append(matches, ExplicitMatch{
			BeadID:      matched.ID,
			CommitSHA:   info.SHA,
			Message:     message,
			Author:      info.Author,
			AuthorEmail: info.AuthorEmail,
			Timestamp:   info.Timestamp,
			MatchType:   matched.MatchType,
			Confidence:  CalculateConfidence(matched.MatchType, len(idMatches)),
		})
	}

	return matches, scanner.Err()
}

// CreateCorrelatedCommit converts an ExplicitMatch to a CorrelatedCommit.
// Uses ExtractNonExcludedFiles so commits explicitly naming a bead ID that
// touch non-code repository files (e.g. .gitignore, docs/RELEASING.md, README.md)
// are never dropped solely because they did not touch source code files.
func (m *ExplicitMatcher) CreateCorrelatedCommit(match ExplicitMatch, coCommitter *CoCommitExtractor) CorrelatedCommit {
	var files []FileChange
	if coCommitter != nil {
		files, _ = coCommitter.ExtractNonExcludedFiles(match.CommitSHA)
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
