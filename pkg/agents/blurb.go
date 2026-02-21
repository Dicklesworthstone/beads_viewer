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
const BlurbStartMarker = "<!-- beads-agent-instructions-v2 -->"

// BlurbEndMarker marks the end of injected agent instructions.
const BlurbEndMarker = "<!-- end-beads-agent-instructions -->"

// AgentBlurb contains the instructions to be appended to AGENTS.md files.
const AgentBlurb = `<!-- beads-agent-instructions-v2 -->

---

## Beads Workflow Integration

This project uses [beads](https://github.com/steveyegge/beads) for issue tracking. Issues live in ` + "`" + `.beads/` + "`" + ` and are tracked in git.

Two CLIs: **bd** (issue CRUD) and **bv** (graph-aware triage, read-only).

### bd: Issue Management

` + "```" + `bash
bd ready              # Unblocked issues ready to work
bd list --status=open # All open issues
bd show <id>          # Full details with dependencies
bd create --title="..." --type=task --priority=2
bd update <id> --status=in_progress
bd close <id>         # Mark complete
bd close <id1> <id2>  # Close multiple
bd dep add <a> <b>    # a depends on b
bd sync               # Sync with git
` + "```" + `

### bv: Graph Analysis (read-only)

**NEVER run bare ` + "`" + `bv` + "`" + `** — it launches interactive TUI. Always use ` + "`" + `--robot-*` + "`" + ` flags:

` + "```" + `bash
bv --robot-triage     # Ranked picks, quick wins, blockers, health
bv --robot-next       # Single top pick + claim command
bv --robot-plan       # Parallel execution tracks
bv --robot-alerts     # Stale issues, cascades, mismatches
bv --robot-insights   # Full graph metrics: PageRank, betweenness, cycles
` + "```" + `

### Workflow

1. **Start**: ` + "`" + `bd ready` + "`" + ` (or ` + "`" + `bv --robot-triage` + "`" + ` for graph analysis)
2. **Claim**: ` + "`" + `bd update <id> --status=in_progress` + "`" + `
3. **Work**: Implement the task
4. **Complete**: ` + "`" + `bd close <id>` + "`" + `
5. **Sync**: ` + "`" + `bd sync` + "`" + ` at session end

### Session Close Protocol

` + "```" + `bash
git status            # Check what changed
git add <files>       # Stage code changes
bd sync               # Commit beads changes
git commit -m "..."   # Commit code
bd sync               # Commit any new beads changes
git push              # Push to remote
` + "```" + `

### Key Concepts

- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (numbers only)
- **Types**: task, bug, feature, epic, question, docs
- **Dependencies**: ` + "`" + `bd ready` + "`" + ` shows only unblocked work

<!-- end-beads-agent-instructions -->`

// SupportedAgentFiles lists the filenames that can contain agent instructions.
var SupportedAgentFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"agents.md",
	"claude.md",
}

// blurbVersionRegex extracts the version number from a blurb marker.
var blurbVersionRegex = regexp.MustCompile(`<!-- beads-agent-instructions-v(\d+) -->`)

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

// ContainsBlurb checks if the content contains the current beads agent blurb (v2+).
func ContainsBlurb(content string) bool {
	return strings.Contains(content, "<!-- beads-agent-instructions-v")
}

// ContainsLegacyBlurb checks if the content contains any old-format blurb:
// the bv- or br- HTML marker formats, or the very old pre-v1 no-marker format.
func ContainsLegacyBlurb(content string) bool {
	// Check for structured legacy formats (bv- and br- HTML markers)
	if strings.Contains(content, "<!-- bv-agent-instructions-v") ||
		strings.Contains(content, "<!-- br-agent-instructions-v") {
		return true
	}
	// Check for very old format (pre-v1, no HTML markers)
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
// Handles all known marker formats: beads- (current), bv- and br- (legacy).
func RemoveBlurb(content string) string {
	type markerPair struct {
		start string
		end   string
	}
	pairs := []markerPair{
		{"<!-- beads-agent-instructions-v", "<!-- end-beads-agent-instructions -->"},
		{"<!-- bv-agent-instructions-v", "<!-- end-bv-agent-instructions -->"},
		{"<!-- br-agent-instructions-v", "<!-- end-br-agent-instructions -->"},
	}
	for _, pair := range pairs {
		startIdx := strings.Index(content, pair.start)
		if startIdx == -1 {
			continue
		}
		endIdx := strings.Index(content, pair.end)
		if endIdx == -1 {
			continue
		}
		endIdx += len(pair.end)
		for endIdx < len(content) && (content[endIdx] == '\n' || content[endIdx] == '\r') {
			endIdx++
		}
		for startIdx > 0 && (content[startIdx-1] == '\n' || content[startIdx-1] == '\r') {
			startIdx--
		}
		return content[:startIdx] + content[endIdx:]
	}
	return content
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
