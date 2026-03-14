// Package agents provides AGENTS.md integration for AI coding agents.
// It handles detection, content injection, and preference storage for
// automatically adding beads_viewer usage instructions to agent configuration files.
package agents

import (
	"regexp"
	"strconv"
	"strings"
)

// BlurbVersion is the current version of the agent instructions blurb.
// Increment this when making breaking changes to the blurb format.
const BlurbVersion = 2

// BlurbStartMarker marks the beginning of injected agent instructions.
const BlurbStartMarker = "<!-- bv-agent-instructions-v2 -->"

// BlurbEndMarker marks the end of injected agent instructions.
const BlurbEndMarker = "<!-- end-bv-agent-instructions -->"

// AgentBlurb contains the instructions to be appended to AGENTS.md files.
// This is the v2 blurb that combines beads CLI workflows with bv robot triage.
const AgentBlurb = `<!-- bv-agent-instructions-v2 -->

---

## Beads Workflow Integration

This project uses a Beads CLI (` + "`" + `bd` + "`" + ` for modern Dolt-based workspaces, or ` + "`" + `br` + "`" + ` for the beads_rust stack) together with [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (` + "`" + `bv` + "`" + `) for graph-aware triage. Issues are stored in ` + "`" + `.beads/` + "`" + ` and tracked in git.

### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects (` + "`" + `.beads/issues.jsonl` + "`" + ` with legacy fallbacks). Instead of parsing JSONL or hallucinating graph traversal, use robot flags for deterministic, dependency-aware outputs with precomputed metrics (PageRank, betweenness, critical path, cycles, HITS, eigenvector, k-core).

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). The local Beads CLI (` + "`" + `bd` + "`" + ` or ` + "`" + `br` + "`" + `) handles creating, modifying, and closing beads.

**CRITICAL: Use ONLY --robot-* flags. Bare bv launches an interactive TUI that blocks your session.**

#### The Workflow: Start With Triage

**` + "`" + `bv --robot-triage` + "`" + ` is your single entry point.** It returns everything you need in one call:
- ` + "`" + `quick_ref` + "`" + `: at-a-glance counts + top 3 picks
- ` + "`" + `recommendations` + "`" + `: ranked actionable items with scores, reasons, unblock info
- ` + "`" + `quick_wins` + "`" + `: low-effort high-impact items
- ` + "`" + `blockers_to_clear` + "`" + `: items that unblock the most downstream work
- ` + "`" + `project_health` + "`" + `: status/type/priority distributions, graph metrics
- ` + "`" + `commands` + "`" + `: copy-paste shell commands for next steps

` + "```" + `bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick + claim command

# Token-optimized output (TOON) for lower LLM context usage:
bv --robot-triage --format toon
` + "```" + `

#### Other bv Commands

| Command | Returns |
|---------|---------|
| ` + "`" + `--robot-plan` + "`" + ` | Parallel execution tracks with unblocks lists |
| ` + "`" + `--robot-priority` + "`" + ` | Priority misalignment detection with confidence |
| ` + "`" + `--robot-insights` + "`" + ` | Full metrics: PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core |
| ` + "`" + `--robot-alerts` + "`" + ` | Stale issues, blocking cascades, priority mismatches |
| ` + "`" + `--robot-suggest` + "`" + ` | Hygiene: duplicates, missing deps, label suggestions, cycle breaks |
| ` + "`" + `--robot-diff --diff-since <ref>` + "`" + ` | Changes since ref: new/closed/modified issues |
| ` + "`" + `--robot-graph [--graph-format=json\|dot\|mermaid]` + "`" + ` | Dependency graph export |

#### Scoping & Filtering

` + "```" + `bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work (no blockers)
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank scores
` + "```" + `

### Beads CLI Commands for Issue Management

` + "```" + `bash
# Replace ` + "`" + `br` + "`" + ` with ` + "`" + `bd` + "`" + ` in modern Dolt-based workspaces.
br ready              # Show issues ready to work (no blockers)
br list --status=open # All open issues
br show <id>          # Full issue details with dependencies
br create --title="..." --type=task --priority=2
br update <id> --status=in_progress
br close <id> --reason="Completed"
br close <id1> <id2>  # Close multiple issues at once
br sync --flush-only  # Export DB to JSONL
` + "```" + `

### Workflow Pattern

1. **Triage**: Run ` + "`" + `bv --robot-triage` + "`" + ` to find the highest-impact actionable work
2. **Claim**: Use ` + "`" + `br update <id> --status=in_progress` + "`" + `
3. **Work**: Implement the task
4. **Complete**: Use ` + "`" + `br close <id>` + "`" + `
5. **Sync**: Always run ` + "`" + `br sync --flush-only` + "`" + ` at session end

### Key Concepts

- **Dependencies**: Issues can block other issues. ` + "`" + `br ready` + "`" + ` shows only unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers 0-4, not words)
- **Types**: task, bug, feature, epic, chore, docs, question
- **Blocking**: ` + "`" + `br dep add <issue> <depends-on>` + "`" + ` to add dependencies

### Session Protocol

` + "```" + `bash
git status              # Check what changed
git add <files>         # Stage code changes
br sync --flush-only    # Export beads changes to JSONL
git commit -m "..."     # Commit everything
git push                # Push to remote
` + "```" + `

<!-- end-bv-agent-instructions -->`

// SupportedAgentFiles lists the filenames that can contain agent instructions.
var SupportedAgentFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"agents.md",
	"claude.md",
}

// blurbVersionRegex extracts the version number from a blurb marker.
var blurbVersionRegex = regexp.MustCompile(`<!-- bv-agent-instructions-v(\d+) -->`)

// LegacyBlurbPatterns are markers that identify the old blurb format (pre-v1, no HTML markers).
var LegacyBlurbPatterns = []string{
	"### Using bv as an AI sidecar",
	"--robot-insights",
	"--robot-plan",
	"bv already computes the hard parts",
}

// legacyBlurbStartPattern matches the beginning of the legacy blurb.
var legacyBlurbStartPattern = regexp.MustCompile(`(?m)^#{2,3}\s*Using bv as an AI sidecar`)

// legacyBlurbEndPattern matches content near the end of the legacy blurb.
// Uses non-capturing group to make the entire triple-backtick sequence optional.
var legacyBlurbEndPattern = regexp.MustCompile(`(?m)bv already computes the hard parts[^\n]*(?:\n*` + "```" + `)?\n*`)

// legacyBlurbNextSectionPattern matches the start of a new section after the legacy blurb.
// Used as fallback when the end pattern isn't found.
var legacyBlurbNextSectionPattern = regexp.MustCompile(`(?m)^#{1,2}\s+[^#]`)

// ContainsBlurb checks if the content already contains a beads_viewer agent blurb.
func ContainsBlurb(content string) bool {
	return strings.Contains(content, "<!-- bv-agent-instructions-v")
}

// ContainsLegacyBlurb checks if the content contains the old-format blurb (pre-v1, no HTML markers).
// Requires all 4 legacy patterns to match to avoid false positives on content that
// merely references robot flags (like the current AGENTS.md documentation).
func ContainsLegacyBlurb(content string) bool {
	if !legacyBlurbStartPattern.MatchString(content) {
		return false
	}
	matchCount := 0
	for _, pattern := range LegacyBlurbPatterns {
		if strings.Contains(content, pattern) {
			matchCount++
		}
	}
	// Require all patterns - the key differentiator is "bv already computes the hard parts"
	// which only appears in the legacy blurb, not in current documentation
	return matchCount == len(LegacyBlurbPatterns)
}

// ContainsAnyBlurb checks if the content contains either the current or legacy blurb format.
func ContainsAnyBlurb(content string) bool {
	return ContainsBlurb(content) || ContainsLegacyBlurb(content)
}

// GetBlurbVersion extracts the version number from existing blurb content.
func GetBlurbVersion(content string) int {
	matches := blurbVersionRegex.FindStringSubmatch(content)
	if len(matches) < 2 {
		return 0
	}
	version, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0
	}
	return version
}

// NeedsUpdate checks if the content has an older version of the blurb that should be updated.
func NeedsUpdate(content string) bool {
	if ContainsLegacyBlurb(content) {
		return true
	}
	if !ContainsBlurb(content) {
		return false
	}
	return GetBlurbVersion(content) < BlurbVersion
}

// AppendBlurb appends the agent blurb to the given content.
func AppendBlurb(content string) string {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n"
	content += AgentBlurb
	content += "\n"
	return content
}

// RemoveBlurb removes an existing blurb from the content.
func RemoveBlurb(content string) string {
	startIdx := strings.Index(content, "<!-- bv-agent-instructions-v")
	if startIdx == -1 {
		return content
	}
	endIdx := strings.Index(content, BlurbEndMarker)
	if endIdx == -1 {
		return content
	}
	endIdx += len(BlurbEndMarker)
	for endIdx < len(content) && (content[endIdx] == '\n' || content[endIdx] == '\r') {
		endIdx++
	}
	for startIdx > 0 && (content[startIdx-1] == '\n' || content[startIdx-1] == '\r') {
		startIdx--
	}
	return content[:startIdx] + content[endIdx:]
}

// RemoveLegacyBlurb removes the old-format blurb (pre-v1, no HTML markers) from content.
func RemoveLegacyBlurb(content string) string {
	if !ContainsLegacyBlurb(content) {
		return content
	}
	startLoc := legacyBlurbStartPattern.FindStringIndex(content)
	if startLoc == nil {
		return content
	}
	startIdx := startLoc[0]
	endLoc := legacyBlurbEndPattern.FindStringIndex(content[startIdx:])
	var endIdx int
	if endLoc != nil {
		endIdx = startIdx + endLoc[1]
	} else {
		// Fallback: find the next major section heading
		nextLoc := legacyBlurbNextSectionPattern.FindStringIndex(content[startIdx+10:])
		if nextLoc != nil {
			endIdx = startIdx + 10 + nextLoc[0]
		} else {
			endIdx = len(content)
		}
	}
	for endIdx < len(content) && (content[endIdx] == '\n' || content[endIdx] == '\r') {
		endIdx++
	}
	for startIdx > 0 && (content[startIdx-1] == '\n' || content[startIdx-1] == '\r') {
		startIdx--
	}
	if startIdx > 0 {
		startIdx++
	}
	return content[:startIdx] + content[endIdx:]
}

// UpdateBlurb replaces an existing blurb with the current version.
func UpdateBlurb(content string) string {
	content = RemoveLegacyBlurb(content)
	content = RemoveBlurb(content)
	return AppendBlurb(content)
}
