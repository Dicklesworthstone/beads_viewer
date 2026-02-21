package agents

import (
	"strings"
	"testing"
)

func TestContainsBlurb(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "empty content",
			content:  "",
			expected: false,
		},
		{
			name:     "no blurb",
			content:  "# My AGENTS.md\n\nSome other content.",
			expected: false,
		},
		{
			name:     "has blurb v2",
			content:  "# My AGENTS.md\n\n<!-- beads-agent-instructions-v2 -->\nSome content\n<!-- end-beads-agent-instructions -->",
			expected: true,
		},
		{
			name:     "has blurb v3 (future)",
			content:  "# My AGENTS.md\n\n<!-- beads-agent-instructions-v3 -->\nSome content\n<!-- end-beads-agent-instructions -->",
			expected: true,
		},
		{
			name:     "bv marker is legacy (not ContainsBlurb)",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v1 -->\nSome content\n<!-- end-bv-agent-instructions -->",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsBlurb(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsBlurb() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetBlurbVersion(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{
			name:     "no blurb",
			content:  "# My AGENTS.md",
			expected: 0,
		},
		{
			name:     "version 1",
			content:  "<!-- beads-agent-instructions-v1 -->",
			expected: 1,
		},
		{
			name:     "version 2 (current)",
			content:  "<!-- beads-agent-instructions-v2 -->",
			expected: 2,
		},
		{
			name:     "version 10 (multi-digit)",
			content:  "<!-- beads-agent-instructions-v10 -->",
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetBlurbVersion(tt.content)
			if result != tt.expected {
				t.Errorf("GetBlurbVersion() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAppendBlurb(t *testing.T) {
	content := "# My AGENTS.md\n\nSome existing content."
	result := AppendBlurb(content)

	// Should contain the start marker
	if !strings.Contains(result, BlurbStartMarker) {
		t.Error("AppendBlurb() result missing start marker")
	}

	// Should contain the end marker
	if !strings.Contains(result, BlurbEndMarker) {
		t.Error("AppendBlurb() result missing end marker")
	}

	// Should contain key content
	if !strings.Contains(result, "bd ready") {
		t.Error("AppendBlurb() result missing 'bd ready' command")
	}

	// Should preserve original content
	if !strings.Contains(result, "Some existing content.") {
		t.Error("AppendBlurb() did not preserve original content")
	}

	// Original content should come first
	origIdx := strings.Index(result, "Some existing content.")
	blurbIdx := strings.Index(result, BlurbStartMarker)
	if origIdx >= blurbIdx {
		t.Error("AppendBlurb() should place blurb after original content")
	}
}

func TestRemoveBlurb(t *testing.T) {
	// Content with blurb
	withBlurb := "# My AGENTS.md\n\nSome content.\n\n" + AgentBlurb + "\n"
	result := RemoveBlurb(withBlurb)

	// Should not contain markers
	if strings.Contains(result, BlurbStartMarker) {
		t.Error("RemoveBlurb() result still contains start marker")
	}
	if strings.Contains(result, BlurbEndMarker) {
		t.Error("RemoveBlurb() result still contains end marker")
	}

	// Should preserve original content
	if !strings.Contains(result, "Some content.") {
		t.Error("RemoveBlurb() did not preserve original content")
	}
}

func TestRemoveBlurbNoBlurb(t *testing.T) {
	content := "# My AGENTS.md\n\nNo blurb here."
	result := RemoveBlurb(content)

	// Should be unchanged
	if result != content {
		t.Errorf("RemoveBlurb() modified content without blurb: got %q, want %q", result, content)
	}
}

func TestUpdateBlurb(t *testing.T) {
	// Start with content containing old blurb
	oldContent := "# My AGENTS.md\n\n<!-- beads-agent-instructions-v1 -->\nOld blurb content\n<!-- end-bv-agent-instructions -->\n"
	result := UpdateBlurb(oldContent)

	// Should have exactly one blurb
	count := strings.Count(result, BlurbStartMarker)
	if count != 1 {
		t.Errorf("UpdateBlurb() resulted in %d blurbs, want 1", count)
	}

	// Should have current blurb content
	if !strings.Contains(result, "bd ready") {
		t.Error("UpdateBlurb() result missing current blurb content")
	}

	// Should preserve header
	if !strings.Contains(result, "# My AGENTS.md") {
		t.Error("UpdateBlurb() did not preserve original header")
	}
}

func TestNeedsUpdate(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "no blurb",
			content:  "# No blurb",
			expected: false,
		},
		{
			name:     "current version",
			content:  "<!-- beads-agent-instructions-v2 -->",
			expected: false, // v2 is current, no update needed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NeedsUpdate(tt.content)
			if result != tt.expected {
				t.Errorf("NeedsUpdate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestAgentBlurbContent(t *testing.T) {
	// Verify blurb contains essential commands
	essentials := []string{
		"bd ready",
		"bd list",
		"bd show",
		"bd create",
		"bd update",
		"bd close",
		"bd sync",
		"bd dep add",
	}

	for _, cmd := range essentials {
		if !strings.Contains(AgentBlurb, cmd) {
			t.Errorf("AgentBlurb missing essential command: %s", cmd)
		}
	}

	// Verify markers
	if !strings.HasPrefix(AgentBlurb, BlurbStartMarker) {
		t.Error("AgentBlurb should start with BlurbStartMarker")
	}
	if !strings.HasSuffix(strings.TrimSpace(AgentBlurb), BlurbEndMarker) {
		t.Error("AgentBlurb should end with BlurbEndMarker")
	}
}

func TestSupportedAgentFiles(t *testing.T) {
	// Should support common variations
	expected := map[string]bool{
		"AGENTS.md": true,
		"CLAUDE.md": true,
		"agents.md": true,
		"claude.md": true,
	}

	for _, file := range SupportedAgentFiles {
		if !expected[file] {
			t.Errorf("Unexpected file in SupportedAgentFiles: %s", file)
		}
		delete(expected, file)
	}

	for missing := range expected {
		t.Errorf("Missing expected file in SupportedAgentFiles: %s", missing)
	}
}

// LegacyBlurbContent is a sample of the old-format blurb (pre-v1, without HTML markers)
const LegacyBlurbContent = `### Using bv as an AI sidecar

If you're an AI agent (like Claude, GPT, Codex, etc.), bv can serve as your
external memory and decision-support system for handling complex multi-part
coding tasks.

**Entry point**: Always start with ` + "`" + `bv --robot-triage` + "`" + `

**Available robot flags**:
- ` + "`" + `--robot-triage` + "`" + ` - Get structured task overview and priorities
- ` + "`" + `--robot-insights` + "`" + ` - Deep analysis with recommendations
- ` + "`" + `--robot-plan` + "`" + ` - Generate actionable task breakdown

**Why use robot flags?**
bv already computes the hard parts for you.
` + "```"

func TestContainsLegacyBlurb(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "empty content",
			content:  "",
			expected: false,
		},
		{
			name:     "no blurb",
			content:  "# My AGENTS.md\n\nSome other content.",
			expected: false,
		},
		{
			name:     "has legacy blurb",
			content:  "# My AGENTS.md\n\n" + LegacyBlurbContent,
			expected: true,
		},
		{
			name:     "has current blurb (not legacy)",
			content:  "# My AGENTS.md\n\n" + AgentBlurb,
			expected: false,
		},
		{
			name:     "bv HTML marker is legacy",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v1 -->\nContent\n<!-- end-bv-agent-instructions -->",
			expected: true,
		},
		{
			name:     "br HTML marker is legacy",
			content:  "# My AGENTS.md\n\n<!-- br-agent-instructions-v1 -->\nContent\n<!-- end-br-agent-instructions -->",
			expected: true,
		},
		{
			name:     "partial legacy (missing patterns)",
			content:  "# My AGENTS.md\n\n### Using bv as an AI sidecar\nJust a header.",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsLegacyBlurb(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsLegacyBlurb() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestContainsAnyBlurb(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "no blurb",
			content:  "# My AGENTS.md",
			expected: false,
		},
		{
			name:     "has current blurb",
			content:  "# AGENTS.md\n\n" + AgentBlurb,
			expected: true,
		},
		{
			name:     "has legacy blurb",
			content:  "# AGENTS.md\n\n" + LegacyBlurbContent,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsAnyBlurb(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsAnyBlurb() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRemoveLegacyBlurb(t *testing.T) {
	// Content with legacy blurb
	withLegacy := "# My AGENTS.md\n\nSome content.\n\n" + LegacyBlurbContent + "\n\n## Other Section\n"
	result := RemoveLegacyBlurb(withLegacy)

	// Should not contain legacy markers
	if strings.Contains(result, "### Using bv as an AI sidecar") {
		t.Error("RemoveLegacyBlurb() result still contains legacy header")
	}
	if strings.Contains(result, "--robot-insights") {
		t.Error("RemoveLegacyBlurb() result still contains robot flags")
	}

	// Should preserve original content before and after
	if !strings.Contains(result, "Some content.") {
		t.Error("RemoveLegacyBlurb() did not preserve content before blurb")
	}
	if !strings.Contains(result, "## Other Section") {
		t.Error("RemoveLegacyBlurb() did not preserve content after blurb")
	}
}

func TestRemoveLegacyBlurbNoLegacy(t *testing.T) {
	content := "# My AGENTS.md\n\nNo legacy blurb here."
	result := RemoveLegacyBlurb(content)

	// Should be unchanged
	if result != content {
		t.Errorf("RemoveLegacyBlurb() modified content without legacy: got %q, want %q", result, content)
	}
}

func TestRemoveLegacyBlurbNoTrailingBackticks(t *testing.T) {
	// Legacy content WITHOUT trailing triple backticks (regression test for regex fix)
	legacyNoBackticks := `# My AGENTS.md

### Using bv as an AI sidecar

Some description here.

**Available robot flags**:
- --robot-insights - Analysis
- --robot-plan - Planning

bv already computes the hard parts for you.

## Next Section
`
	result := RemoveLegacyBlurb(legacyNoBackticks)

	// Should not contain legacy markers
	if strings.Contains(result, "### Using bv as an AI sidecar") {
		t.Error("RemoveLegacyBlurb() did not remove legacy header (no trailing backticks case)")
	}
	if strings.Contains(result, "--robot-insights") {
		t.Error("RemoveLegacyBlurb() did not remove robot flags (no trailing backticks case)")
	}
	if strings.Contains(result, "bv already computes the hard parts") {
		t.Error("RemoveLegacyBlurb() did not remove end phrase (no trailing backticks case)")
	}

	// Should preserve surrounding content
	if !strings.Contains(result, "# My AGENTS.md") {
		t.Error("RemoveLegacyBlurb() did not preserve header")
	}
	if !strings.Contains(result, "## Next Section") {
		t.Error("RemoveLegacyBlurb() did not preserve next section")
	}
}

func TestUpdateBlurbFromLegacy(t *testing.T) {
	// Start with content containing legacy blurb
	legacyContent := "# My AGENTS.md\n\n" + LegacyBlurbContent + "\n"
	result := UpdateBlurb(legacyContent)

	// Should have exactly one current blurb
	count := strings.Count(result, BlurbStartMarker)
	if count != 1 {
		t.Errorf("UpdateBlurb() from legacy resulted in %d blurbs, want 1", count)
	}

	// Should have current blurb content
	if !strings.Contains(result, "bd ready") {
		t.Error("UpdateBlurb() from legacy missing current blurb content")
	}

	// Should NOT have legacy content (unique phrase only in old format)
	if strings.Contains(result, "bv already computes the hard parts") {
		t.Error("UpdateBlurb() from legacy still contains legacy content")
	}

	// Should preserve header
	if !strings.Contains(result, "# My AGENTS.md") {
		t.Error("UpdateBlurb() from legacy did not preserve original header")
	}
}

func TestNeedsUpdateLegacy(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name:     "legacy blurb needs update",
			content:  "# AGENTS.md\n\n" + LegacyBlurbContent,
			expected: true,
		},
		{
			name:     "current blurb no update",
			content:  "# AGENTS.md\n\n" + AgentBlurb,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NeedsUpdate(tt.content)
			if result != tt.expected {
				t.Errorf("NeedsUpdate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// ============================================================================
// Edge Case Tests for bv-efrq: Legacy Blurb Migration
// ============================================================================

// TestContainsLegacyBlurbEdgeCases tests boundary conditions for legacy detection.
func TestContainsLegacyBlurbEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "only 2 of 4 patterns (header + one flag)",
			content: `# AGENTS.md

### Using bv as an AI sidecar

Some description that mentions --robot-insights but nothing else.
`,
			expected: false,
		},
		{
			name: "3 of 4 patterns (missing key differentiator)",
			// Has: header, --robot-insights, --robot-plan
			// Missing: "bv already computes the hard parts"
			content: `# AGENTS.md

### Using bv as an AI sidecar

Use these flags:
- --robot-insights for analysis
- --robot-plan for planning
`,
			expected: false,
		},
		{
			name: "documentation about flags (like this project's AGENTS.md)",
			// Content similar to what appears in bv's own AGENTS.md
			// Has 3 patterns but NOT "bv already computes the hard parts"
			content: `# AGENTS.md

### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects.

**Available robot flags**:
| Command | Returns |
|---------|---------|
| --robot-insights | Full metrics: PageRank, betweenness, HITS |
| --robot-plan | Parallel execution tracks |

Use bv instead of parsing beads.jsonl—it computes PageRank deterministically.
`,
			expected: false,
		},
		{
			name: "patterns without start header",
			// Has all the patterns but not the "### Using bv as an AI sidecar" header
			content: `# AGENTS.md

## Some Other Section

Mentions --robot-insights and --robot-plan.
bv already computes the hard parts for you.
`,
			expected: false,
		},
		{
			name: "header with ## instead of ### (not legacy)",
			// LegacyBlurbPatterns[0] requires exactly "### Using bv as an AI sidecar" (3 #)
			// while legacyBlurbStartPattern regex allows 2-3 #, the string match requires 3 #
			content: `# AGENTS.md

## Using bv as an AI sidecar

Some description.
- --robot-insights
- --robot-plan

bv already computes the hard parts for you.
`,
			expected: false, // Pattern match requires exact "### Using..." string
		},
		{
			name: "all 4 patterns present (true positive)",
			content: `# AGENTS.md

### Using bv as an AI sidecar

Full legacy blurb with:
- --robot-insights
- --robot-plan
bv already computes the hard parts for you.
`,
			expected: true,
		},
		{
			name: "patterns scattered across unrelated sections",
			content: `# AGENTS.md

### Using bv as an AI sidecar

Intro only.

## Section About Search

Use --robot-insights for search results.

## Section About Planning

Use --robot-plan to get plans.

## Footer

Note: bv already computes the hard parts - use it!
`,
			expected: true, // All patterns are present, even if scattered
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsLegacyBlurb(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsLegacyBlurb() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestRemoveLegacyBlurbEdgeCases tests boundary conditions for legacy removal.
func TestRemoveLegacyBlurbEdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		expectRemoved   []string // strings that should NOT be in result
		expectPreserved []string // strings that should be in result
	}{
		{
			name: "legacy blurb at file start",
			content: `### Using bv as an AI sidecar

Some description.
--robot-insights
--robot-plan
bv already computes the hard parts for you.

## Real Content

This should be preserved.
`,
			expectRemoved:   []string{"### Using bv as an AI sidecar", "--robot-insights"},
			expectPreserved: []string{"## Real Content", "This should be preserved"},
		},
		{
			name: "legacy blurb at file end (no trailing content)",
			content: `# AGENTS.md

Some intro content.

### Using bv as an AI sidecar

Description.
--robot-insights
--robot-plan
bv already computes the hard parts for you.
`,
			expectRemoved:   []string{"### Using bv as an AI sidecar", "--robot-insights"},
			expectPreserved: []string{"# AGENTS.md", "Some intro content"},
		},
		{
			name: "legacy blurb with CRLF line endings",
			content: "# AGENTS.md\r\n\r\n### Using bv as an AI sidecar\r\n\r\n" +
				"Description.\r\n--robot-insights\r\n--robot-plan\r\n" +
				"bv already computes the hard parts for you.\r\n\r\n" +
				"## Next Section\r\n",
			expectRemoved:   []string{"### Using bv as an AI sidecar", "--robot-insights"},
			expectPreserved: []string{"# AGENTS.md", "## Next Section"},
		},
		{
			name: "legacy blurb with mixed LF and CRLF",
			content: "# AGENTS.md\n\n### Using bv as an AI sidecar\r\n\n" +
				"Description.\n--robot-insights\r\n--robot-plan\n" +
				"bv already computes the hard parts for you.\n\n" +
				"## Next Section\n",
			expectRemoved:   []string{"### Using bv as an AI sidecar", "--robot-insights"},
			expectPreserved: []string{"# AGENTS.md", "## Next Section"},
		},
		{
			name: "legacy blurb only content in file",
			content: `### Using bv as an AI sidecar

--robot-insights
--robot-plan
bv already computes the hard parts for you.
`,
			expectRemoved:   []string{"### Using bv as an AI sidecar"},
			expectPreserved: []string{}, // file should be nearly empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RemoveLegacyBlurb(tt.content)

			for _, s := range tt.expectRemoved {
				if strings.Contains(result, s) {
					t.Errorf("RemoveLegacyBlurb() result still contains %q", s)
				}
			}

			for _, s := range tt.expectPreserved {
				if !strings.Contains(result, s) {
					t.Errorf("RemoveLegacyBlurb() result missing expected content %q", s)
				}
			}
		})
	}
}

// TestGetBlurbVersionEdgeCases tests boundary conditions for version extraction.
func TestGetBlurbVersionEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected int
	}{
		{
			name:     "v0 marker",
			content:  "<!-- beads-agent-instructions-v0 -->",
			expected: 0, // v0 parses to 0
		},
		{
			name:     "v99 high version",
			content:  "<!-- beads-agent-instructions-v99 -->",
			expected: 99,
		},
		{
			name:     "v999 very high version",
			content:  "<!-- beads-agent-instructions-v999 -->",
			expected: 999,
		},
		{
			name:     "malformed non-numeric version",
			content:  "<!-- beads-agent-instructions-vX -->",
			expected: 0,
		},
		{
			name:     "malformed version with letters",
			content:  "<!-- beads-agent-instructions-v1a -->",
			expected: 0, // \d+ matches "1" but then pattern expects " -->" not "a -->"
		},
		{
			name:     "multiple version markers returns first",
			content:  "<!-- beads-agent-instructions-v3 -->\nsome content\n<!-- beads-agent-instructions-v5 -->",
			expected: 3, // FindStringSubmatch returns first match
		},
		{
			name:     "version marker in middle of content",
			content:  "# Header\n\nSome text before\n\n<!-- beads-agent-instructions-v7 -->\n\nContent after",
			expected: 7,
		},
		{
			name:     "version marker with extra spaces (no match)",
			content:  "<!-- beads-agent-instructions-v 1 -->",
			expected: 0, // regex requires no space before digits
		},
		{
			name:     "partial marker (missing closing)",
			content:  "<!-- beads-agent-instructions-v1",
			expected: 0, // regex requires " -->"
		},
		{
			name:     "negative-looking version (just digits)",
			content:  "<!-- beads-agent-instructions-v-1 -->",
			expected: 0, // \d+ doesn't match "-1"
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetBlurbVersion(tt.content)
			if result != tt.expected {
				t.Errorf("GetBlurbVersion() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestRemoveBlurbEdgeCases tests boundary conditions for current blurb removal.
func TestRemoveBlurbEdgeCases(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		expectPreserved []string
	}{
		{
			name: "blurb at very start of file",
			content: `<!-- beads-agent-instructions-v1 -->
Content
<!-- end-beads-agent-instructions -->

## Real Section
`,
			expectPreserved: []string{"## Real Section"},
		},
		{
			name: "blurb with CRLF line endings",
			content: "# Header\r\n\r\n<!-- beads-agent-instructions-v1 -->\r\n" +
				"Content\r\n<!-- end-beads-agent-instructions -->\r\n\r\n## Footer\r\n",
			expectPreserved: []string{"# Header", "## Footer"},
		},
		{
			name: "blurb only content in file",
			content: `<!-- beads-agent-instructions-v1 -->
Content
<!-- end-beads-agent-instructions -->
`,
			expectPreserved: []string{}, // should be empty or nearly empty
		},
		{
			name:            "missing end marker",
			content:         "# Header\n\n<!-- beads-agent-instructions-v1 -->\nContent without end",
			expectPreserved: []string{"# Header", "Content without end"}, // unchanged
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RemoveBlurb(tt.content)

			// Should not contain markers (checks all supported formats)
			hasStart := strings.Contains(result, "<!-- beads-agent-instructions") ||
				strings.Contains(result, "<!-- bv-agent-instructions") ||
				strings.Contains(result, "<!-- br-agent-instructions")
			hasEnd := strings.Contains(result, "<!-- end-beads-agent-instructions -->") ||
				strings.Contains(result, "<!-- end-bv-agent-instructions -->") ||
				strings.Contains(result, "<!-- end-br-agent-instructions -->")
			if hasStart && hasEnd {
				t.Error("RemoveBlurb() result still contains both markers")
			}

			for _, s := range tt.expectPreserved {
				if !strings.Contains(result, s) {
					t.Errorf("RemoveBlurb() result missing expected content %q", s)
				}
			}
		})
	}
}

// TestContainsAnyBlurbEdgeCases tests edge cases for combined detection.
func TestContainsAnyBlurbEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "both legacy and current (should not happen but test anyway)",
			content: `# AGENTS.md

### Using bv as an AI sidecar

--robot-insights
--robot-plan
bv already computes the hard parts for you.

<!-- beads-agent-instructions-v1 -->
Current blurb
<!-- end-bv-agent-instructions -->
`,
			expected: true,
		},
		{
			name:     "only start marker no end",
			content:  "<!-- beads-agent-instructions-v1 -->\nContent",
			expected: true, // ContainsBlurb checks for start marker only
		},
		{
			name:     "only end marker",
			content:  "Content\n<!-- end-bv-agent-instructions -->",
			expected: false,
		},
		{
			name:     "marker inside code block",
			content:  "```\n<!-- beads-agent-instructions-v1 -->\n```",
			expected: true, // simple string check doesn't parse markdown
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsAnyBlurb(tt.content)
			if result != tt.expected {
				t.Errorf("ContainsAnyBlurb() = %v, want %v", result, tt.expected)
			}
		})
	}
}
