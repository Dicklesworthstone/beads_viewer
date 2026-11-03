package correlation

import (
	"regexp"
	"testing"
)

func TestExtractIDsFromMessage(t *testing.T) {
	m := NewExplicitMatcher("/tmp/test")

	tests := []struct {
		name     string
		message  string
		wantIDs  []string
		wantType string
	}{
		{
			name:     "bracket format",
			message:  "fix(auth): resolve login bug [AUTH-123]",
			wantIDs:  []string{"auth-123"},
			wantType: "bracket",
		},
		{
			name:     "closes keyword",
			message:  "Closes BV-42",
			wantIDs:  []string{"bv-42"},
			wantType: "closes",
		},
		{
			name:     "fixes keyword with hash",
			message:  "Fixes #AUTH-999",
			wantIDs:  []string{"auth-999"},
			wantType: "fixes",
		},
		{
			name:     "refs keyword",
			message:  "Refs: BV-100, BV-101",
			wantIDs:  []string{"bv-100", "bv-101"},
			wantType: "refs",
		},
		{
			name:     "beads format",
			message:  "Update beads-456 with new status",
			wantIDs:  []string{"bv-456"},
			wantType: "bead",
		},
		{
			name:     "bv format",
			message:  "Implement bv-67 feature",
			wantIDs:  []string{"bv-67"},
			wantType: "bead",
		},
		{
			name:     "generic PROJECT-123 format",
			message:  "Add feature for PROJ-789",
			wantIDs:  []string{"proj-789"},
			wantType: "generic",
		},
		{
			name:     "lowercase conventional commit scope",
			message:  "feat(hist-1): add history to export",
			wantIDs:  []string{"hist-1"},
			wantType: "generic",
		},
		{
			name:     "mixed case generic reference",
			message:  "Follow up on Hist-12 in this commit",
			wantIDs:  []string{"hist-12"},
			wantType: "generic",
		},
		{
			name:     "uppercase numeric alias is one reference",
			message:  "Update BEADS-456 with new status",
			wantIDs:  []string{"bv-456"},
			wantType: "bead",
		},
		{
			name:     "bracketed alias preserves canonical identity",
			message:  "Update [bead-456] with new status",
			wantIDs:  []string{"bv-456"},
			wantType: "bracket",
		},
		{
			name:    "generic ID embedded in identifiers",
			message: "prefix_hist-1 2hist-2 hist-3suffix hist-4_more",
		},
		{
			name:    "hash-style suffix is not a numeric ID",
			message: "feat(hist-1abc): mention bv-8a4r and PROJ-9abc",
		},
		{
			name:    "action keywords do not truncate identifiers",
			message: "Fixes hist-1abc and refs BV-8a4r, closes hist-2_more",
		},
		{
			name:    "numeric aliases embedded in identifiers",
			message: "prefix_bv-12 prefix_beads-34",
		},
		{
			name:    "generic prefix length is still bounded",
			message: "abcdefghijk-123",
		},
		{
			name:    "no IDs",
			message: "Just a regular commit message",
			wantIDs: nil,
		},
		{
			name:     "multiple formats in one message",
			message:  "fix: [AUTH-123] Closes BV-42 and refs PROJ-999",
			wantIDs:  []string{"auth-123", "bv-42", "proj-999"},
			wantType: "bracket", // First match type
		},
		{
			name:     "resolves keyword",
			message:  "Resolves BV-50",
			wantIDs:  []string{"bv-50"},
			wantType: "resolves",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := m.ExtractIDsFromMessage(tt.message)

			if len(tt.wantIDs) == 0 {
				if len(matches) != 0 {
					t.Errorf("expected no matches, got %d", len(matches))
				}
				return
			}

			if len(matches) != len(tt.wantIDs) {
				t.Errorf("expected %d matches, got %d", len(tt.wantIDs), len(matches))
				return
			}

			for i, wantID := range tt.wantIDs {
				if matches[i].ID != wantID {
					t.Errorf("match %d: expected ID %q, got %q", i, wantID, matches[i].ID)
				}
			}

			// Check first match type
			if len(matches) > 0 && matches[0].MatchType != tt.wantType {
				t.Errorf("expected match type %q, got %q", tt.wantType, matches[0].MatchType)
			}
		})
	}
}

func TestNormalizeBeadID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AUTH-123", "auth-123"},
		{"BV-42", "bv-42"},
		{"123", "bv-123"}, // Numeric only gets bv- prefix
		{"456", "bv-456"},
		{"proj-999", "proj-999"},
		{"bead-123", "bv-123"},
		{"BEADS-456", "bv-456"},
		{"beads-001", "bv-001"},
		{"beads-8a4r", "beads-8a4r"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeBeadID(tt.input)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestClassifyMatch(t *testing.T) {
	tests := []struct {
		raw      string
		expected string
	}{
		{"Closes BV-42", "closes"},
		{"Close BV-42", "closes"},
		{"Fixes AUTH-123", "fixes"},
		{"Fixed AUTH-123", "fixes"},
		{"fix AUTH-123", "fixes"},
		{"Refs: BV-100", "refs"},
		{"Ref BV-100", "refs"},
		{"Resolves BV-50", "resolves"},
		{"[AUTH-123]", "bracket"},
		{"beads-456", "bead"},
		{"bv-67", "bead"},
		{"BV-67", "bead"},
		{"PROJ-789", "generic"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			result := classifyMatch(tt.raw)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestCalculateExplicitConfidence(t *testing.T) {
	tests := []struct {
		matchType    string
		totalMatches int
		minExpected  float64
		maxExpected  float64
	}{
		{"closes", 1, 0.94, 0.96},   // 0.90 + 0.05 = 0.95
		{"fixes", 1, 0.94, 0.96},    // 0.90 + 0.05 = 0.95
		{"resolves", 1, 0.94, 0.96}, // 0.90 + 0.05 = 0.95
		{"bracket", 1, 0.91, 0.93},  // 0.90 + 0.02 = 0.92
		{"refs", 1, 0.90, 0.92},     // 0.90 + 0.01 = 0.91
		{"bead", 1, 0.92, 0.94},     // 0.90 + 0.03 = 0.93
		{"generic", 1, 0.89, 0.91},  // 0.90 base
		{"closes", 3, 0.89, 0.92},   // 0.95 - 0.04 = 0.91
		{"generic", 5, 0.80, 0.84},  // 0.90 - 0.08 = 0.82
	}

	for _, tt := range tests {
		t.Run(tt.matchType, func(t *testing.T) {
			result := CalculateConfidence(tt.matchType, tt.totalMatches)
			if result < tt.minExpected || result > tt.maxExpected {
				t.Errorf("expected confidence between %.2f and %.2f, got %.2f",
					tt.minExpected, tt.maxExpected, result)
			}
		})
	}
}

func TestBuildGrepPatterns(t *testing.T) {
	m := NewExplicitMatcher("/tmp/test")

	tests := []struct {
		beadID   string
		wantLen  int
		contains []string
	}{
		{
			beadID:   "bv-42",
			wantLen:  6, // bv-42, BV-42, beads-42, bead-42, BEADS-42, BEAD-42
			contains: []string{"bv-42", "BV-42", "beads-42"},
		},
		{
			beadID:   "AUTH-123",
			wantLen:  2, // AUTH-123, auth-123
			contains: []string{"AUTH-123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.beadID, func(t *testing.T) {
			patterns := m.buildGrepPatterns(tt.beadID)

			if len(patterns) != tt.wantLen {
				t.Errorf("expected %d patterns, got %d: %v", tt.wantLen, len(patterns), patterns)
			}

			for _, want := range tt.contains {
				found := false
				for _, p := range patterns {
					if p == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected pattern %q in %v", want, patterns)
				}
			}
		})
	}
}

func TestDefaultPatterns(t *testing.T) {
	patterns := DefaultPatterns()

	if len(patterns) == 0 {
		t.Error("expected default patterns, got none")
	}

	// Test that patterns are valid by matching known formats
	testCases := []struct {
		text    string
		matches bool
	}{
		{"[AUTH-123]", true},
		{"Closes BV-42", true},
		{"Fixes #PROJ-999", true},
		{"Refs: beads-100", true},
		{"bv-67", true},
		{"random text", false},
	}

	for _, tc := range testCases {
		matched := false
		for _, p := range patterns {
			if p.MatchString(tc.text) {
				matched = true
				break
			}
		}
		if matched != tc.matches {
			t.Errorf("text %q: expected match=%v, got %v", tc.text, tc.matches, matched)
		}
	}
}

func TestAddPattern(t *testing.T) {
	m := NewExplicitMatcher("/tmp/test")
	initialCount := len(m.patterns)

	// Add a custom pattern
	customPattern := regexp.MustCompile(`JIRA-\d+`)
	m.AddPattern(customPattern)

	if len(m.patterns) != initialCount+1 {
		t.Errorf("expected %d patterns after add, got %d", initialCount+1, len(m.patterns))
	}

	// Verify the pattern works
	matches := m.ExtractIDsFromMessage("Working on JIRA-1234")
	if len(matches) == 0 {
		t.Error("expected match for custom pattern")
	}
}

func TestDuplicateIDsInMessage(t *testing.T) {
	m := NewExplicitMatcher("/tmp/test")

	// Message with same ID referenced multiple times
	message := "Closes BV-42 and refs BV-42 again [BV-42]"
	matches := m.ExtractIDsFromMessage(message)

	// Should deduplicate
	if len(matches) != 1 {
		t.Errorf("expected 1 unique match, got %d", len(matches))
	}
}

func TestCustomIDPatterns_NoCaptureGroup(t *testing.T) {
	SetCustomIDPatterns([]*regexp.Regexp{regexp.MustCompile(`\bbh-[a-z0-9]{5}\b`)})
	t.Cleanup(func() { SetCustomIDPatterns(nil) })

	m := NewExplicitMatcher("/tmp/test")
	matches := m.ExtractIDsFromMessage("fix flush ordering (bh-8g6cj)")

	found := false
	for _, match := range matches {
		if match.ID == "bh-8g6cj" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected custom pattern to match bh-8g6cj, got %#v", matches)
	}
}

func TestCustomIDPatterns_WithCaptureGroup(t *testing.T) {
	SetCustomIDPatterns([]*regexp.Regexp{regexp.MustCompile(`(?i)ref\s+(bhui-[a-z]{3})\b`)})
	t.Cleanup(func() { SetCustomIDPatterns(nil) })

	m := NewExplicitMatcher("/tmp/test")
	matches := m.ExtractIDsFromMessage("board polish, ref BHUI-PBB done")

	found := false
	for _, match := range matches {
		if match.ID == "bhui-pbb" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected capture group 1 to be extracted as the ID, got %#v", matches)
	}
}

func TestCustomIDPatterns_DefaultsUnaffectedWhenEmpty(t *testing.T) {
	SetCustomIDPatterns(nil)
	base := len(builtinPatterns())
	if got := len(DefaultPatterns()); got != base {
		t.Errorf("DefaultPatterns() = %d patterns with no custom set, want %d", got, base)
	}

	SetCustomIDPatterns([]*regexp.Regexp{regexp.MustCompile(`x-[0-9a-f]{4}`)})
	t.Cleanup(func() { SetCustomIDPatterns(nil) })
	if got := len(DefaultPatterns()); got != base+1 {
		t.Errorf("DefaultPatterns() = %d patterns with one custom set, want %d", got, base+1)
	}
}

func TestExplicitMatcherPolicyFingerprintUsesActualOrderedPatterns(t *testing.T) {
	previous := CustomIDPatterns()
	SetCustomIDPatterns(nil)
	t.Cleanup(func() { SetCustomIDPatterns(previous) })

	original := NewExplicitMatcher("")
	fingerprint := original.policyFingerprint()
	custom := regexp.MustCompile(`\b(custom-[a-z]+)\b`)
	SetCustomIDPatterns([]*regexp.Regexp{custom})
	if got := original.policyFingerprint(); got != fingerprint {
		t.Fatal("global registration changed an existing matcher's policy identity")
	}
	withCustom := NewExplicitMatcher("")
	if withCustom.policyFingerprint() == fingerprint {
		t.Fatal("custom pattern did not change matcher identity")
	}
	if NewExplicitMatcher("").policyFingerprint() != withCustom.policyFingerprint() {
		t.Fatal("equivalent matcher policies produced different identities")
	}
	left := regexp.MustCompile(`\b(custom-[a-z]+)\b`)
	right := regexp.MustCompile(`\[([a-z]+-[a-z]+)\]`)
	forward := NewExplicitMatcherWithPatterns("", []*regexp.Regexp{left, right})
	reverse := NewExplicitMatcherWithPatterns("", []*regexp.Regexp{right, left})
	if forward.policyFingerprint() == reverse.policyFingerprint() {
		t.Fatal("pattern order was omitted from matcher identity")
	}
}
