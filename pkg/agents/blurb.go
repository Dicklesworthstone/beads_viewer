// Package agents provides AGENTS.md integration for AI coding agents.
// It handles detection, content injection, and preference storage for
// automatically adding beads_viewer usage instructions to agent configuration files.
package agents

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// BlurbVersion is the current version of the agent instructions blurb.
// Increment this when making breaking changes to the blurb format.
const BlurbVersion = 4

// BlurbStartMarker marks the beginning of injected agent instructions.
const BlurbStartMarker = "<!-- bv-agent-instructions-v4 -->"

// BlurbEndMarker marks the end of injected agent instructions.
const BlurbEndMarker = "<!-- end-bv-agent-instructions -->"

const blurbStartPrefix = "<!-- bv-agent-instructions-v"

const (
	// A 16 MiB file can legally contain millions of one-byte lines. Keeping a
	// markdownLine for every one of them would turn the file-size limit into a
	// much larger memory allocation. Bound the parsed representation directly.
	maxMarkdownAnalysisLines = 1 << 18
	// Container parsing constructs a small descriptor for every nested list or
	// blockquote and several Markdown classifiers inspect the same prefix. Bound
	// both one-line depth and aggregate explicit-container work before any of
	// those classifiers allocate their slices.
	maxMarkdownContainerDepth    = 256
	maxMarkdownContainerPrefixes = 1 << 20

	// Legacy blurbs must be rediscovered after each removal because an old,
	// dangling fence can change how the remaining Markdown is parsed. Bound the
	// aggregate rescanned input so adversarial duplicate copies cannot make that
	// compatibility path quadratic without limit.
	maxLegacyBlurbRemovalScanBytes = 64 << 20
)

// AgentBlurb contains the instructions to be appended to AGENTS.md files.
// This is the v4 blurb that combines bd/br workflow commands with bv robot triage.
const AgentBlurb = `<!-- bv-agent-instructions-v4 -->

---

## Beads Workflow Integration

This project uses a Beads tracker—either the Go ` + "`" + `bd` + "`" + ` CLI or the Rust ` + "`" + `br` + "`" + ` CLI—for issue tracking, plus [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) (` + "`" + `bv` + "`" + `) for graph-aware triage. Issues are stored in ` + "`" + `.beads/` + "`" + `. ` + "`" + `bv` + "`" + ` auto-discovers supported JSONL exports, including ` + "`" + `.beads/issues.jsonl` + "`" + ` and legacy ` + "`" + `.beads/beads.jsonl` + "`" + `.

**Choose the tracker CLI from this repository's instructions and configuration.** Use ` + "`" + `bd` + "`" + ` commands in a Go Beads workspace and ` + "`" + `br` + "`" + ` commands in a beads_rust workspace. Do not run both trackers against the same workspace or infer the tracker solely from the JSONL filename.

### Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects. Instead of parsing .beads/issues.jsonl / .beads/beads.jsonl directly or hallucinating graph traversal, use robot flags for deterministic, dependency-aware outputs with precomputed metrics (PageRank, betweenness, critical path, cycles, HITS, eigenvector, k-core).

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). The selected tracker CLI (` + "`" + `bd` + "`" + ` or ` + "`" + `br` + "`" + `) handles creating, claiming, modifying, and closing beads.

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

Before claiming, verify current state with the selected tracker: ` + "`" + `br show <id> --json` + "`" + `/` + "`" + `br ready --json` + "`" + ` or ` + "`" + `bd show <id> --json` + "`" + `/` + "`" + `bd ready --json` + "`" + `. ` + "`" + `recommendations` + "`" + ` can include graph-important blocked or assigned work; only ` + "`" + `quick_ref.top_picks` + "`" + ` and non-empty ` + "`" + `claim_command` + "`" + ` fields represent claimable work.

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

### Tracker Commands for Issue Management

Use exactly one command family, matching the tracker configured for the repository.

#### Rust beads_rust (` + "`" + `br` + "`" + `)

` + "```" + `bash
br ready --json                       # Show issues ready to work (no blockers)
br list --status=open --json          # All open issues
br show <id> --json                   # Full issue details with dependencies
br create --title="..." --type=task --priority=2 --json
br update <id> --status=in_progress --json
br close <id> --reason="Completed" --json
br close <id1> <id2> --reason="Completed" --json
br sync --flush-only                  # Export DB to JSONL after Beads mutations
` + "```" + `

#### Go Beads (` + "`" + `bd` + "`" + `)

` + "```" + `bash
bd ready --json                       # Show issues ready to work
bd show <id> --json                   # Full issue details
bd create "..." -t task -p 2 --json
bd update <id> --claim --json         # Atomically claim work
bd close <id> --json
bd dep add <issue> <depends-on>
bd export -o .beads/issues.jsonl        # Refresh the compatibility export read by bv
` + "```" + `

### Workflow Pattern

1. **Triage**: Run ` + "`" + `bv --robot-triage` + "`" + ` to find the highest-impact actionable work
2. **Verify**: Check the selected tracker's ` + "`" + `show` + "`" + `/` + "`" + `ready` + "`" + ` output before claiming
3. **Claim**: Use ` + "`" + `br update <id> --status=in_progress --json` + "`" + ` or ` + "`" + `bd update <id> --claim --json` + "`" + `
4. **Work**: Implement the task
5. **Complete**: Use the selected tracker's ` + "`" + `close` + "`" + ` command
6. **Refresh for bv**: Run ` + "`" + `br sync --flush-only` + "`" + ` or the ` + "`" + `bd export` + "`" + ` command above so the JSONL export is current

### Key Concepts

- **Dependencies**: Issues can block other issues. ` + "`" + `br ready --json` + "`" + ` and ` + "`" + `bd ready --json` + "`" + ` show unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers 0-4, not words)
- **Types**: task, bug, feature, epic, chore, docs, question
- **Blocking**: Use ` + "`" + `br dep add <issue> <depends-on>` + "`" + ` or ` + "`" + `bd dep add <issue> <depends-on>` + "`" + ` to add dependencies

### Git Policy

Tracker commands do not grant permission to commit or push application code. Follow this repository's own git and tracker instructions before staging, committing, syncing, or pushing. If the repository says "commit only when asked," that rule overrides any generic workflow advice.

<!-- end-bv-agent-instructions -->`

// SupportedAgentFiles lists the filenames that can contain agent instructions.
var SupportedAgentFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"agents.md",
	"claude.md",
}

// blurbVersionRegex validates a complete, standalone version marker after the
// surrounding line whitespace has been removed.
var blurbVersionRegex = regexp.MustCompile(`^<!-- bv-agent-instructions-v(\d+) -->$`)

// LegacyBlurbPatterns are markers that identify the old blurb format (pre-v1, no HTML markers).
var LegacyBlurbPatterns = []string{
	"### Using bv as an AI sidecar",
	"--robot-insights",
	"--robot-plan",
	"bv already computes the hard parts",
}

type markdownLine struct {
	start        int
	end          int
	text         string
	outsideFence bool
	topLevel     bool
}

type markdownContainerKind uint8

const (
	markdownBlockquote markdownContainerKind = iota
	markdownList
)

type markdownContainer struct {
	kind         markdownContainerKind
	indent       int
	orderedStart int
}

type markdownFence struct {
	char       byte
	width      int
	containers []markdownContainer
}

type blurbMarkerKind uint8

const (
	blurbStart blurbMarkerKind = iota
	blurbEnd
	blurbInvalidStart
)

type blurbMarker struct {
	kind       blurbMarkerKind
	version    int
	byteOffset int
	lineStart  int
	lineEnd    int
}

type blurbBlock struct {
	start   int
	end     int
	version int
}

type legacyBlurbSpan struct {
	start int
	end   int
}

// ContainsBlurb checks for a standalone versioned start marker outside fenced
// Markdown. Marker examples in code fences and inline prose are documentation,
// not installed instructions. Because this compatibility API cannot return an
// error, it conservatively returns true when bounded Markdown analysis is
// indeterminate; checked file operations expose the diagnostic instead.
func ContainsBlurb(content string) bool {
	markers, err := scanBlurbMarkersChecked(content)
	if err != nil {
		// The boolean API cannot return analysis uncertainty. Fail closed so a
		// caller never interprets budget-limited Markdown as proof of absence;
		// checked file operations surface the underlying error.
		return true
	}
	for _, marker := range markers {
		if marker.kind == blurbStart || marker.kind == blurbInvalidStart {
			return true
		}
	}
	return false
}

// ContainsLegacyBlurb checks if the content contains the old-format blurb (pre-v1, no HTML markers).
// All identifying text must occur, in template order, inside one bounded
// Markdown section. Scattered references in unrelated sections are not a blurb.
func ContainsLegacyBlurb(content string) bool {
	_, ok := findLegacyBlurb(content)
	return ok
}

// ContainsAnyBlurb checks if the content contains either the current or legacy blurb format.
func ContainsAnyBlurb(content string) bool {
	return ContainsBlurb(content) || ContainsLegacyBlurb(content)
}

// GetBlurbVersion returns the highest version advertised by standalone markers
// outside fenced Markdown. Taking the maximum prevents an older first block
// from hiding a later blurb written by a newer bv binary.
func GetBlurbVersion(content string) int {
	maxVersion := 0
	for _, marker := range scanBlurbMarkers(content) {
		if marker.kind == blurbStart && marker.version > maxVersion {
			maxVersion = marker.version
		}
	}
	return maxVersion
}

// NeedsUpdate checks whether the content needs normalization or an updated
// blurb. Malformed marker structure and multiple complete versioned blocks both
// require attention even when the first marker advertises the current version.
func NeedsUpdate(content string) bool {
	count, err := inspectBlurbStructure(content)
	if err != nil || count > 1 {
		return true
	}
	if ContainsLegacyBlurb(content) {
		return true
	}
	if count == 0 {
		return false
	}
	return GetBlurbVersion(content) != BlurbVersion
}

// AppendBlurb appends the agent blurb to the given content.
func AppendBlurb(content string) string {
	lineBreak := preferredLineBreak(content)
	if lineBreak == "" {
		lineBreak = "\n"
	}
	blurb := strings.ReplaceAll(AgentBlurb, "\n", lineBreak)
	if content == "" {
		return blurb + lineBreak
	}
	if !strings.HasSuffix(content, "\n") && !strings.HasSuffix(content, "\r") {
		content += lineBreak
	}
	content += lineBreak
	content += blurb
	content += lineBreak
	return content
}

// RemoveBlurb removes all structurally valid versioned and legacy blurbs from
// the content. Malformed versioned markers are left byte-for-byte unchanged;
// file-writing callers use removeBlurbsChecked so they can surface the error.
func RemoveBlurb(content string) string {
	updated, err := removeBlurbsChecked(content)
	if err != nil {
		return content
	}
	return updated
}

func removeFirstVersionedBlurb(content string) string {
	blocks, err := inspectBlurbBlocks(content)
	if err != nil || len(blocks) == 0 {
		return content
	}
	return removeDelimitedBlurb(content, blocks[0].start, blocks[0].end)
}

// RemoveLegacyBlurb removes the old-format blurb (pre-v1, no HTML markers) from content.
func RemoveLegacyBlurb(content string) string {
	span, ok := findLegacyBlurb(content)
	if !ok {
		return content
	}
	return removeDelimitedBlurb(content, span.start, span.end)
}

// UpdateBlurb replaces existing, structurally valid blurbs with the current
// version. Malformed markers are left byte-for-byte unchanged; file-writing
// callers use updateBlurbChecked so they can surface the validation error.
func UpdateBlurb(content string) string {
	updated, err := updateBlurbChecked(content)
	if err != nil {
		return content
	}
	return updated
}

func updateBlurbChecked(content string) (string, error) {
	var err error
	content, err = prepareBlurbMutation(content, "replace")
	if err != nil {
		return "", err
	}
	blocks, err := inspectBlurbBlocks(content)
	if err != nil {
		return "", err
	}
	content, err = removeVersionedBlurbBlocks(content, blocks)
	if err != nil {
		return "", err
	}
	updated := AppendBlurb(content)
	count, err := inspectBlurbStructure(updated)
	if err != nil {
		return "", fmt.Errorf("validate updated bv agent blurb: %w", err)
	}
	version := GetBlurbVersion(updated)
	if count != 1 || version != BlurbVersion {
		return "", fmt.Errorf("validate updated bv agent blurb: found %d standalone versioned blocks at v%d, want exactly one v%d block", count, version, BlurbVersion)
	}
	return updated, nil
}

func removeBlurbsChecked(content string) (string, error) {
	var err error
	content, err = prepareBlurbMutation(content, "remove")
	if err != nil {
		return "", err
	}
	blocks, err := inspectBlurbBlocks(content)
	if err != nil {
		return "", err
	}
	return removeVersionedBlurbBlocks(content, blocks)
}

// removeVersionedBlurbBlocks removes already-validated, ordered blocks in one
// pass. Repeatedly rescanning and copying the remaining document once per
// duplicate makes an otherwise valid size-bounded file quadratic.
func removeVersionedBlurbBlocks(content string, blocks []blurbBlock) (string, error) {
	if len(blocks) == 0 {
		return content, nil
	}

	type removalGroup struct {
		start      int
		end        int
		firstBlock int
		lastBlock  int
	}
	groups := make([]removalGroup, 0, len(blocks))
	previousBlockEnd := 0
	for i, block := range blocks {
		if block.start < previousBlockEnd || block.start < 0 || block.end < block.start || block.end > len(content) {
			return "", fmt.Errorf("malformed bv agent blurb: invalid validated block range [%d,%d)", block.start, block.end)
		}
		previousBlockEnd = block.end
		start := trimLineBreaksBefore(content, block.start)
		end := trimLineBreaksAfter(content, block.end)
		if len(groups) > 0 && start <= groups[len(groups)-1].end {
			group := &groups[len(groups)-1]
			if end > group.end {
				group.end = end
			}
			group.lastBlock = i
			continue
		}
		groups = append(groups, removalGroup{
			start:      start,
			end:        end,
			firstBlock: i,
			lastBlock:  i,
		})
	}

	var result strings.Builder
	result.Grow(len(content))
	cursor := 0
	for _, group := range groups {
		if group.start < cursor || group.end < group.start || group.end > len(content) {
			return "", fmt.Errorf("malformed bv agent blurb: overlapping removal range [%d,%d)", group.start, group.end)
		}
		result.WriteString(content[cursor:group.start])
		if group.start > 0 {
			// Preserve the original newline convention while excluding the blurb
			// bodies themselves. Adjacent blocks form one removal group, so their
			// intervening line breaks are considered exactly once and collapse to
			// the same single separator as repeated removals.
			var removedLineBreaks strings.Builder
			boundary := group.start
			for i := group.firstBlock; i <= group.lastBlock; i++ {
				block := blocks[i]
				if boundary < block.start {
					removedLineBreaks.WriteString(content[boundary:block.start])
				}
				boundary = block.end
			}
			if boundary < group.end {
				removedLineBreaks.WriteString(content[boundary:group.end])
			}
			result.WriteString(preferredLineBreak(removedLineBreaks.String()))
		}
		cursor = group.end
	}
	result.WriteString(content[cursor:])
	return result.String(), nil
}

// prepareBlurbMutation validates both the original Markdown view and a
// non-mutating view with recognized legacy blurbs removed. Historical legacy
// copies can end in a dangling bare fence: a later versioned blurb's own code
// fence may then make its end marker look stray in the original view. Inspect
// the legacy-free view before returning that original structural error so a
// hidden future-version blurb is still identified and protected. For all
// non-future content, the original structural error remains fail-closed.
func prepareBlurbMutation(content, action string) (string, error) {
	originalBlocks, originalStructureErr := inspectBlurbBlocks(content)
	if originalStructureErr == nil {
		if err := rejectFutureBlurbBlocks(originalBlocks, action); err != nil {
			return "", err
		}
	}

	withoutLegacy, ambiguousFencePreserved, realLegacyRemovals, err := removeLegacyBlurbsChecked(content)
	if err != nil {
		return "", err
	}
	revealedBlocks, revealedStructureErr := inspectBlurbBlocks(withoutLegacy)
	if err := rejectFutureBlurbBlocks(revealedBlocks, action); err != nil {
		return "", err
	}
	// An ambiguous adjacent fence is preserved by real removal because it may be
	// a user code-block opener. Inspect a hypothetical view that consumes that
	// historical delimiter, but never write it. Future-version refusal takes
	// precedence; any other marker visibility or legacy-removal-count ambiguity
	// fails closed rather than choosing which user bytes are installed content.
	if ambiguousFencePreserved || originalStructureErr != nil || revealedStructureErr != nil {
		analysisView, _, analysisLegacyRemovals, analysisErr := removeLegacyBlurbsCheckedWithPolicy(content, true)
		if analysisErr != nil {
			return "", analysisErr
		}
		analysisBlocks, analysisStructureErr := inspectBlurbBlocks(analysisView)
		if err := rejectFutureBlurbBlocks(analysisBlocks, action); err != nil {
			return "", err
		}
		if ambiguousFencePreserved && (len(scanBlurbMarkers(withoutLegacy)) > 0 || len(scanBlurbMarkers(analysisView)) > 0) {
			return "", fmt.Errorf("malformed bv agent blurb: ambiguous marker material hidden by preserved legacy fence; refusing to %s", action)
		}
		if ambiguousFencePreserved && realLegacyRemovals != analysisLegacyRemovals {
			return "", fmt.Errorf("malformed legacy bv agent blurb: ambiguous fence changes removal count from %d to %d; refusing to %s", realLegacyRemovals, analysisLegacyRemovals, action)
		}
		if analysisStructureErr != nil && originalStructureErr == nil && revealedStructureErr == nil {
			return "", analysisStructureErr
		}
	}
	if originalStructureErr != nil {
		return "", originalStructureErr
	}
	if revealedStructureErr != nil {
		return "", revealedStructureErr
	}
	return withoutLegacy, nil
}

func rejectFutureBlurbBlocks(blocks []blurbBlock, action string) error {
	for _, block := range blocks {
		if block.version > BlurbVersion {
			return fmt.Errorf("refusing to %s bv agent blurb v%d with older bv v%d", action, block.version, BlurbVersion)
		}
	}
	return nil
}

func removeLegacyBlurbsChecked(content string) (string, bool, int, error) {
	return removeLegacyBlurbsCheckedWithPolicy(content, false)
}

func removeLegacyBlurbsCheckedWithPolicy(content string, consumeAmbiguousFence bool) (string, bool, int, error) {
	ambiguousFencePreserved := false
	removed := 0
	scannedBytes := 0
	for {
		if len(content) > maxLegacyBlurbRemovalScanBytes-scannedBytes {
			return "", ambiguousFencePreserved, removed, fmt.Errorf(
				"markdown analysis limit exceeded after scanning %d bytes while removing duplicate legacy bv blurbs",
				scannedBytes,
			)
		}
		scannedBytes += len(content)
		span, ok, ambiguous, scanErr := findLegacyBlurbWithPolicyChecked(content, consumeAmbiguousFence)
		if scanErr != nil {
			return "", ambiguousFencePreserved, removed, scanErr
		}
		ambiguousFencePreserved = ambiguousFencePreserved || ambiguous
		if !ok {
			return content, ambiguousFencePreserved, removed, nil
		}
		withoutBlurb := removeDelimitedBlurb(content, span.start, span.end)
		if withoutBlurb == content {
			return "", ambiguousFencePreserved, removed, fmt.Errorf("malformed legacy bv agent blurb: unable to remove detected content")
		}
		content = withoutBlurb
		removed++
	}
}

func validateBlurbStructure(content string) error {
	_, err := inspectBlurbStructure(content)
	return err
}

func inspectBlurbStructure(content string) (int, error) {
	blocks, err := inspectBlurbBlocks(content)
	return len(blocks), err
}

func inspectBlurbBlocks(content string) ([]blurbBlock, error) {
	markers, scanErr := scanBlurbMarkersChecked(content)
	blocks := make([]blurbBlock, 0, len(markers)/2)
	var open *blurbMarker
	for i := range markers {
		marker := &markers[i]
		switch marker.kind {
		case blurbInvalidStart:
			return blocks, fmt.Errorf("malformed bv agent blurb: invalid start marker at byte %d", marker.byteOffset)
		case blurbStart:
			if open != nil {
				return blocks, fmt.Errorf("malformed bv agent blurb: nested start marker at byte %d", marker.byteOffset)
			}
			open = marker
		case blurbEnd:
			if open == nil {
				return blocks, fmt.Errorf("malformed bv agent blurb: end marker at byte %d has no start marker", marker.byteOffset)
			}
			blocks = append(blocks, blurbBlock{start: open.lineStart, end: marker.lineEnd, version: open.version})
			open = nil
		}
	}
	if open != nil {
		return blocks, fmt.Errorf("malformed bv agent blurb: start marker at byte %d has no end marker", open.byteOffset)
	}
	if scanErr != nil {
		return blocks, scanErr
	}
	return blocks, nil
}

func scanBlurbMarkers(content string) []blurbMarker {
	markers, _ := scanBlurbMarkersChecked(content)
	return markers
}

func scanBlurbMarkersChecked(content string) ([]blurbMarker, error) {
	lines, scanErr := scanMarkdownLinesChecked(content)
	markers := make([]blurbMarker, 0, 2)
	for _, line := range lines {
		if !line.outsideFence || !line.topLevel {
			continue
		}
		trimmed, indent, ok := standaloneMarkdownText(line.text)
		if !ok {
			continue
		}
		marker := blurbMarker{
			byteOffset: line.start + indent,
			lineStart:  line.start,
			lineEnd:    line.end,
		}
		switch {
		case trimmed == BlurbEndMarker:
			marker.kind = blurbEnd
			markers = append(markers, marker)
		case strings.HasPrefix(trimmed, blurbStartPrefix):
			matches := blurbVersionRegex.FindStringSubmatch(trimmed)
			if len(matches) != 2 {
				marker.kind = blurbInvalidStart
				markers = append(markers, marker)
				continue
			}
			version, err := strconv.Atoi(matches[1])
			if err != nil {
				marker.kind = blurbInvalidStart
				markers = append(markers, marker)
				continue
			}
			marker.kind = blurbStart
			marker.version = version
			markers = append(markers, marker)
		}
	}
	return markers, scanErr
}

func scanMarkdownLines(content string) []markdownLine {
	lines, _ := scanMarkdownLinesChecked(content)
	return lines
}

func scanMarkdownLinesChecked(content string) ([]markdownLine, error) {
	lineCount, err := markdownPhysicalLineCount(content)
	if err != nil {
		return nil, err
	}
	lines := make([]markdownLine, 0, lineCount)
	var fence markdownFence
	linkReferenceEnd := -1
	htmlCommentDepth := 0
	htmlRawTag := ""
	htmlTerminator := ""
	htmlBlankTerminated := false
	var htmlContainers []markdownContainer
	var listContainers []markdownContainer
	emptyListParentDepth := -1
	paragraphOpen := false
	for start := 0; start < len(content); {
		end := start
		for end < len(content) && content[end] != '\n' && content[end] != '\r' {
			end++
		}
		next := end
		if next < len(content) {
			if content[next] == '\r' && next+1 < len(content) && content[next+1] == '\n' {
				next += 2
			} else {
				next++
			}
		}
		text := content[start:end]
		blank := isMarkdownBlankLine(text)
		paragraphAtLineStart := paragraphOpen
		topLevel := markdownLineIsTopLevel(text, listContainers)
		// A definition recognized on an earlier line owns all of its continuation
		// lines. Do this before looking for fenced-block openers so a literal ``` in
		// a multiline link title cannot leak fence state beyond the definition.
		outside := start >= linkReferenceEnd
		topLevelReferenceLine := !outside
		if outside && fence.char != 0 {
			if blank {
				// Blank lines remain inside list and blockquote fenced blocks even
				// when their container prefix is omitted.
				outside = false
			} else if remainder, ok := stripMarkdownContainers(text, fence.containers); ok {
				outside = false
				if char, width, rest, isFence := markdownFenceRun(remainder); isFence && char == fence.char && width >= fence.width && isMarkdownBlankLine(rest) {
					fence = markdownFence{}
				}
			} else {
				// An unclosed fenced block inside a list/blockquote ends when its
				// containing block ends. Reprocess this line as top-level Markdown.
				fence = markdownFence{}
			}
		}
		// A valid fence opener takes precedence over comment-like bytes in its
		// info string. Otherwise ```text <!-- example could incorrectly start an
		// HTML comment and hide marker lines after the fence closes.
		if outside && htmlCommentDepth == 0 && htmlRawTag == "" && htmlTerminator == "" && !htmlBlankTerminated {
			if char, width, rest, containers, ok := markdownFenceOpening(text); ok {
				if len(containers) > maxMarkdownContainerDepth {
					return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d: container depth %d exceeds %d", start, len(containers), maxMarkdownContainerDepth)
				}
				// Backtick info strings cannot contain backticks in CommonMark.
				if char != '`' || !strings.Contains(rest, "`") {
					outside = false
					fence = markdownFence{char: char, width: width, containers: containers}
				}
			}
		}
		// A top-level indented code block is protected documentation just like a
		// fenced code block. Indented code cannot interrupt a paragraph, so retain
		// paragraph continuations as ordinary Markdown text.
		if outside && htmlCommentDepth == 0 && htmlRawTag == "" && htmlTerminator == "" && !htmlBlankTerminated &&
			topLevel && !paragraphAtLineStart && markdownLineStartsIndentedCode(text) {
			outside = false
		}
		// Link reference definitions are block material, not paragraphs. In
		// particular, a complete definition may be followed immediately by a type-7
		// HTML block. Protect every physical line in the bounded top-level definition
		// so marker-shaped label or title examples remain documentation. A definition
		// never interrupts a paragraph, so only start this lookahead at a block
		// boundary. Container-nested single-line definitions are handled separately;
		// their prefixes make a safe multiline lookahead context-dependent.
		if outside && htmlCommentDepth == 0 && htmlRawTag == "" && htmlTerminator == "" && !htmlBlankTerminated &&
			!paragraphAtLineStart {
			switch {
			case topLevel:
				definitionEnd, ok := commonMarkLinkReferenceDefinitionEnd(content, start)
				if definitionEnd == commonMarkScanBudgetExceeded {
					return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d while parsing a link reference definition", start)
				}
				if ok {
					linkReferenceEnd = definitionEnd
					topLevelReferenceLine = true
					outside = false
				}
			default:
				openingText, openingContainers := markdownHTMLBlockOpening(text, listContainers)
				if len(openingContainers) > maxMarkdownContainerDepth {
					return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d: container depth %d exceeds %d", start, len(openingContainers), maxMarkdownContainerDepth)
				}
				definitionEnd, ok := commonMarkLinkReferenceDefinitionEnd(openingText, 0)
				if definitionEnd == commonMarkScanBudgetExceeded {
					return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d while parsing a nested link reference definition", start)
				}
				if ok && definitionEnd == len(openingText) {
					outside = false
				}
			}
		}

		// HTML leaf blocks end at the end of their containing list/blockquote.
		// If the current line no longer belongs to the recorded container, clear
		// the HTML state and process this same line as ordinary top-level Markdown.
		htmlText := text
		if outside && (htmlCommentDepth > 0 || htmlRawTag != "" || htmlTerminator != "" || htmlBlankTerminated) && len(htmlContainers) > 0 {
			if scoped, ok := stripMarkdownContainers(text, htmlContainers); ok {
				htmlText = scoped
			} else if blank && markdownContainersAreLists(htmlContainers) {
				htmlText = text
			} else {
				htmlCommentDepth = 0
				htmlRawTag = ""
				htmlTerminator = ""
				htmlBlankTerminated = false
				htmlContainers = nil
			}
		}
		if outside && htmlCommentDepth > 0 {
			htmlCommentDepth, _ = advanceHTMLCommentDepth(htmlText, htmlCommentDepth)
			if htmlCommentDepth == 0 {
				htmlContainers = nil
			}
			outside = false
		} else if outside && htmlRawTag != "" {
			if closesHTMLRawBlock(htmlText, htmlRawTag) {
				htmlRawTag = ""
				htmlContainers = nil
			}
			outside = false
		} else if outside && htmlTerminator != "" {
			if strings.Contains(htmlText, htmlTerminator) {
				htmlTerminator = ""
				htmlContainers = nil
			}
			outside = false
		} else if outside && htmlBlankTerminated {
			if isMarkdownBlankLine(htmlText) {
				htmlBlankTerminated = false
				htmlContainers = nil
			} else {
				outside = false
			}
		} else if outside {
			openingText, openingContainers := markdownHTMLBlockOpening(text, listContainers)
			if len(openingContainers) > maxMarkdownContainerDepth {
				return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d: container depth %d exceeds %d", start, len(openingContainers), maxMarkdownContainerDepth)
			}
			if tag := opensHTMLRawBlock(openingText); tag != "" {
				htmlRawTag = tag
				htmlContainers = append([]markdownContainer(nil), openingContainers...)
				if closesHTMLRawBlock(openingText, tag) {
					htmlRawTag = ""
					htmlContainers = nil
				}
				outside = false
			} else if opensHTMLCommentBlock(openingText) && !standaloneBlurbMarkerText(openingText) {
				htmlCommentDepth, _ = advanceHTMLCommentDepth(openingText, 0)
				htmlContainers = append([]markdownContainer(nil), openingContainers...)
				if htmlCommentDepth == 0 {
					htmlContainers = nil
				}
				// Versioned blurb delimiters are themselves complete HTML comments,
				// so recognize a standalone delimiter at top level. Everything inside
				// an already-open multiline comment remains documentation, including
				// nested marker-shaped examples.
				outside = false
			} else if terminator := opensHTMLDelimitedBlock(openingText); terminator != "" {
				htmlTerminator = terminator
				htmlContainers = append([]markdownContainer(nil), openingContainers...)
				if strings.Contains(openingText, terminator) {
					htmlTerminator = ""
					htmlContainers = nil
				}
				outside = false
			} else if opensHTMLBlankTerminatedBlock(openingText, paragraphOpen) {
				htmlBlankTerminated = true
				htmlContainers = append([]markdownContainer(nil), openingContainers...)
				outside = false
			}
		}
		lines = append(lines, markdownLine{
			start:        start,
			end:          end,
			text:         text,
			outsideFence: outside,
			topLevel:     topLevel,
		})

		// Carry list continuation indentation so an HTML block opened on a later
		// indented line remains scoped to that list, not the whole document.
		if topLevelReferenceLine {
			// List-looking bytes inside a multiline label or title are inline
			// definition content. A top-level definition also ends any lazy list
			// continuation that preceded it.
			listContainers = nil
			emptyListParentDepth = -1
		} else if blank && emptyListParentDepth >= 0 {
			// An empty list marker has no paragraph whose continuation can span a
			// blank line. End that innermost list without discarding any ancestor
			// container that still owns the following indented content.
			listContainers = listContainers[:emptyListParentDepth]
			emptyListParentDepth = -1
		} else if remainder, explicitContainers, parentDepth, ok := stripMarkdownOpeningContainersWithActive(text, listContainers); ok &&
			containsMarkdownList(explicitContainers) &&
			!(paragraphAtLineStart && (isMarkdownBlankLine(remainder) || firstNewContainerCannotInterruptParagraph(explicitContainers, listContainers))) {
			if len(explicitContainers) > maxMarkdownContainerDepth {
				return lines, fmt.Errorf("markdown analysis limit exceeded at byte %d: container depth %d exceeds %d", start, len(explicitContainers), maxMarkdownContainerDepth)
			}
			listContainers = append([]markdownContainer(nil), explicitContainers...)
			emptyListParentDepth = -1
			if isMarkdownBlankLine(remainder) {
				emptyListParentDepth = parentDepth
			}
		} else if !blank && len(listContainers) > 0 {
			if _, matched := stripMarkdownContainerPrefix(text, listContainers); matched > 0 {
				listContainers = listContainers[:matched]
				emptyListParentDepth = -1
			} else {
				if !(paragraphAtLineStart && outside && !startsNonParagraphMarkdownBlock(text, true)) {
					listContainers = nil
					emptyListParentDepth = -1
				}
			}
		}

		switch {
		case blank:
			paragraphOpen = false
		case !outside, standaloneBlurbMarkerText(text), startsNonParagraphMarkdownBlock(text, paragraphOpen):
			paragraphOpen = false
		default:
			paragraphOpen = true
		}
		start = next
	}
	return lines, nil
}

// markdownPhysicalLineCount mirrors scanMarkdownLinesChecked's CR, LF, and
// CRLF splitting without allocating a line table. Rejecting before allocation
// is important for newline-dense files at the accepted byte-size boundary.
func markdownPhysicalLineCount(content string) (int, error) {
	lineCount := 0
	containerPrefixes := 0
	for start := 0; start < len(content); {
		lineCount++
		if lineCount > maxMarkdownAnalysisLines {
			return 0, fmt.Errorf(
				"markdown analysis limit exceeded at byte %d: more than %d physical lines",
				start,
				maxMarkdownAnalysisLines,
			)
		}
		end := start
		for end < len(content) && content[end] != '\n' && content[end] != '\r' {
			end++
		}
		depth := markdownOpeningContainerDepth(content[start:end], maxMarkdownContainerDepth+1)
		if depth > maxMarkdownContainerDepth {
			return 0, fmt.Errorf(
				"markdown analysis limit exceeded at byte %d: container depth exceeds %d",
				start,
				maxMarkdownContainerDepth,
			)
		}
		if depth > maxMarkdownContainerPrefixes-containerPrefixes {
			return 0, fmt.Errorf(
				"markdown analysis limit exceeded at byte %d: more than %d explicit container prefixes",
				start,
				maxMarkdownContainerPrefixes,
			)
		}
		containerPrefixes += depth
		if end == len(content) {
			break
		}
		if content[end] == '\r' && end+1 < len(content) && content[end+1] == '\n' {
			start = end + 2
		} else {
			start = end + 1
		}
	}
	return lineCount, nil
}

// markdownOpeningContainerDepth follows the same prefix grammar as
// stripMarkdownOpeningContainers without retaining descriptors. limit makes a
// single adversarial line bounded even before the caller can reject it.
func markdownOpeningContainerDepth(line string, limit int) int {
	line = strings.TrimSuffix(line, "\r")
	depth := 0
	pos := 0
	for depth < limit {
		spaces := countLeadingSpaces(line[pos:])
		if spaces > 3 {
			return depth
		}
		pos += spaces
		if pos >= len(line) {
			return depth
		}
		if line[pos] == '>' {
			depth++
			pos++
			if pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
				pos++
			}
			continue
		}
		markerWidth, gapBytes, _, _, isList := markdownListMarker(line[pos:], spaces)
		if !isList {
			return depth
		}
		depth++
		pos += markerWidth + gapBytes
	}
	return depth
}

// markdownLineIsTopLevel reports whether line is outside every explicit or
// continued list/blockquote container. Installed blurb delimiters and legacy
// headings are document-level material; marker-shaped examples nested in a
// list or blockquote must remain ordinary user documentation.
func markdownLineIsTopLevel(line string, activeListContainers []markdownContainer) bool {
	if len(activeListContainers) > 0 {
		if _, matched := stripMarkdownContainerPrefix(line, activeListContainers); matched > 0 {
			return false
		}
	}
	_, explicitContainers, ok := stripMarkdownOpeningContainers(line)
	return !ok || len(explicitContainers) == 0
}

func opensHTMLCommentBlock(line string) bool {
	trimmed, _, ok := standaloneMarkdownText(line)
	return ok && strings.HasPrefix(trimmed, "<!--")
}

func advanceHTMLCommentDepth(line string, depth int) (int, bool) {
	start := 0
	if depth == 0 {
		start = strings.Index(line, "<!--")
		if start < 0 {
			return 0, false
		}
		start += len("<!--")
	}
	// CommonMark type-2 HTML blocks are not nested. The first closing token
	// ends the block even when another opening token precedes it.
	if strings.Contains(line[start:], "-->") {
		return 0, true
	}
	return 1, true
}

func standaloneBlurbMarkerText(line string) bool {
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok {
		return false
	}
	return trimmed == BlurbEndMarker || strings.HasPrefix(trimmed, blurbStartPrefix)
}

func opensHTMLRawBlock(line string) string {
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok {
		return ""
	}
	lower := strings.ToLower(trimmed)
	for _, tag := range [...]string{"pre", "script", "style", "textarea"} {
		prefix := "<" + tag
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if len(lower) == len(prefix) {
			return tag
		}
		switch lower[len(prefix)] {
		case ' ', '\t', '>':
			return tag
		}
	}
	return ""
}

func closesHTMLRawBlock(line, tag string) bool {
	return strings.Contains(strings.ToLower(line), "</"+tag+">")
}

func opensHTMLDelimitedBlock(line string) string {
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok {
		return ""
	}
	switch {
	case strings.HasPrefix(trimmed, "<?"):
		return "?>"
	case strings.HasPrefix(trimmed, "<![CDATA["):
		return "]]>"
	case len(trimmed) >= 3 && strings.HasPrefix(trimmed, "<!") && isASCIIAlpha(trimmed[2]):
		return ">"
	default:
		return ""
	}
}

func opensHTMLBlankTerminatedBlock(line string, paragraphOpen bool) bool {
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok || len(trimmed) < 3 || trimmed[0] != '<' {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, tag := range [...]string{
		"address", "article", "aside", "base", "basefont", "blockquote", "body", "caption", "center",
		"col", "colgroup", "dd", "details", "dialog", "dir", "div", "dl", "dt", "fieldset",
		"figcaption", "figure", "footer", "form", "frame", "frameset", "h1", "h2", "h3", "h4",
		"h5", "h6", "head", "header", "hr", "html", "iframe", "legend", "li", "link", "main",
		"menu", "menuitem", "nav", "noframes", "ol", "optgroup", "option", "p", "param", "search",
		"section", "summary", "table", "tbody", "td", "tfoot", "th", "thead", "title", "tr", "track", "ul",
	} {
		if htmlTagPrefix(lower, tag) {
			return true
		}
	}

	// CommonMark type 7 accepts a complete open or closing tag and runs until
	// the next blank line, but it cannot interrupt a paragraph and the complete
	// tag must be the only non-space/tab content on its line.
	return !paragraphOpen && isCompleteCommonMarkHTMLTag(trimmed)
}

func htmlTagPrefix(lower, tag string) bool {
	for _, prefix := range [...]string{"<" + tag, "</" + tag} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if len(lower) == len(prefix) {
			return true
		}
		switch lower[len(prefix)] {
		case ' ', '\t', '>':
			return true
		case '/':
			return len(lower) > len(prefix)+1 && lower[len(prefix)+1] == '>'
		}
	}
	return false
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

const (
	maxCommonMarkReferenceDefinitionBytes = 16 << 10
	maxCommonMarkReferenceLabelCharacters = 999
	maxCommonMarkDestinationParenDepth    = 32
	commonMarkScanBudgetExceeded          = -1
)

// commonMarkLinkReferenceDefinitionEnd recognizes a deliberately bounded
// CommonMark link reference definition beginning at a physical line boundary.
// It accepts multiline labels and titles. Definite syntax errors reject the
// candidate normally. If a plausible candidate exhausts a fixed analysis
// budget, the function returns commonMarkScanBudgetExceeded with ok=false so
// checked callers can report uncertainty instead of claiming that all remaining
// content is either documentation or installed instructions. For a recognized
// definition, the returned offset is the end of its last physical line before
// the line ending.
func commonMarkLinkReferenceDefinitionEnd(content string, start int) (int, bool) {
	if start < 0 || start >= len(content) {
		return 0, false
	}
	limit := len(content)
	if remaining := len(content) - start; remaining > maxCommonMarkReferenceDefinitionBytes {
		limit = start + maxCommonMarkReferenceDefinitionBytes
	}

	pos := start
	indent := 0
	for pos < limit && content[pos] == ' ' && indent < 4 {
		pos++
		indent++
	}
	if indent > 3 || pos >= limit || content[pos] != '[' {
		return 0, false
	}
	pos++

	labelCharacters := 0
	labelHasContent := false
	lineHasContent := true // The opening '[' makes the first physical line nonblank.
	escaped := false
	for {
		if pos >= limit {
			if limit < len(content) {
				return commonMarkScanBudgetExceeded, false
			}
			return 0, false
		}
		if width := markdownLineEndingWidth(content, pos); width > 0 {
			if !lineHasContent {
				return 0, false
			}
			if pos+width > limit {
				if limit < len(content) {
					return commonMarkScanBudgetExceeded, false
				}
				return 0, false
			}
			labelCharacters++
			if labelCharacters > maxCommonMarkReferenceLabelCharacters {
				return 0, false
			}
			pos += width
			lineHasContent = false
			escaped = false
			continue
		}

		value := content[pos]
		if !escaped && value == ']' {
			pos++
			break
		}
		if !escaped && value == '[' {
			return 0, false
		}
		characterWidth := 1
		if value >= utf8.RuneSelf {
			_, characterWidth = utf8.DecodeRuneInString(content[pos:limit])
		}
		labelCharacters++
		if labelCharacters > maxCommonMarkReferenceLabelCharacters {
			return 0, false
		}
		if value != ' ' && value != '\t' {
			labelHasContent = true
			lineHasContent = true
		}
		if escaped {
			escaped = false
		} else if value == '\\' {
			escaped = true
		}
		pos += characterWidth
	}
	if !labelHasContent || pos >= limit || content[pos] != ':' {
		return 0, false
	}
	pos++
	pos = skipMarkdownSpacesTabs(content, pos, limit)
	if width := markdownLineEndingWidth(content, pos); width > 0 {
		if pos+width > limit {
			if limit < len(content) {
				return commonMarkScanBudgetExceeded, false
			}
			return 0, false
		}
		pos += width
		pos = skipMarkdownSpacesTabs(content, pos, limit)
	}
	if pos >= limit {
		if limit < len(content) {
			return commonMarkScanBudgetExceeded, false
		}
		return 0, false
	}

	destinationEnd, ok := commonMarkLinkDestinationEnd(content, pos, limit)
	if !ok {
		if destinationEnd == commonMarkScanBudgetExceeded || destinationEnd == limit && limit < len(content) {
			return commonMarkScanBudgetExceeded, false
		}
		return 0, false
	}
	pos = destinationEnd
	spaceStart := pos
	pos = skipMarkdownSpacesTabs(content, pos, limit)
	hasTitleSeparator := pos > spaceStart
	if pos == len(content) {
		return pos, true
	}
	if pos >= limit {
		if limit < len(content) {
			return commonMarkScanBudgetExceeded, false
		}
		return 0, false
	}

	if width := markdownLineEndingWidth(content, pos); width > 0 {
		definitionEnd := pos
		if pos+width > limit {
			if limit < len(content) {
				return commonMarkScanBudgetExceeded, false
			}
			return definitionEnd, true
		}
		titleStart := skipMarkdownSpacesTabs(content, pos+width, limit)
		if titleStart >= limit {
			if limit < len(content) {
				return commonMarkScanBudgetExceeded, false
			}
			return definitionEnd, true
		}
		if !isCommonMarkLinkTitleDelimiter(content[titleStart]) {
			return definitionEnd, true
		}
		titleEnd, titleOK := commonMarkLinkTitleEnd(content, titleStart, limit)
		if !titleOK {
			if titleEnd == limit && limit < len(content) {
				return commonMarkScanBudgetExceeded, false
			}
			// A malformed would-be title on the next line is an ordinary block;
			// the preceding destination-only definition remains valid.
			return definitionEnd, true
		}
		tail := skipMarkdownSpacesTabs(content, titleEnd, limit)
		if tail == limit && limit < len(content) {
			return commonMarkScanBudgetExceeded, false
		}
		if tail == len(content) || tail < limit && markdownLineEndingWidth(content, tail) > 0 {
			return tail, true
		}
		return definitionEnd, true
	}

	if !hasTitleSeparator || !isCommonMarkLinkTitleDelimiter(content[pos]) {
		return 0, false
	}
	titleEnd, ok := commonMarkLinkTitleEnd(content, pos, limit)
	if !ok {
		if titleEnd == limit && limit < len(content) {
			return commonMarkScanBudgetExceeded, false
		}
		return 0, false
	}
	tail := skipMarkdownSpacesTabs(content, titleEnd, limit)
	if tail == limit && limit < len(content) {
		return commonMarkScanBudgetExceeded, false
	}
	if tail == len(content) || tail < limit && markdownLineEndingWidth(content, tail) > 0 {
		return tail, true
	}
	return 0, false
}

func commonMarkLinkDestinationEnd(content string, start, limit int) (int, bool) {
	if start >= limit || markdownLineEndingWidth(content, start) > 0 {
		return 0, false
	}
	if content[start] == '<' {
		for pos := start + 1; pos < limit; pos++ {
			value := content[pos]
			if markdownLineEndingWidth(content, pos) > 0 || value == '<' {
				return 0, false
			}
			if value == '\\' && pos+1 < limit && isASCIICommonMarkPunctuation(content[pos+1]) {
				pos++
				continue
			}
			if value == '>' {
				return pos + 1, true
			}
		}
		return limit, false
	}

	depth := 0
	pos := start
	for pos < limit {
		value := content[pos]
		if value == ' ' || value == '\t' || markdownLineEndingWidth(content, pos) > 0 {
			break
		}
		if value < 0x20 || value == 0x7f || value == '<' {
			return 0, false
		}
		if value == '\\' && pos+1 < limit && isASCIICommonMarkPunctuation(content[pos+1]) {
			pos += 2
			continue
		}
		switch value {
		case '(':
			depth++
			if depth > maxCommonMarkDestinationParenDepth {
				return commonMarkScanBudgetExceeded, false
			}
		case ')':
			if depth == 0 {
				return 0, false
			}
			depth--
		}
		pos++
	}
	return pos, pos > start && depth == 0
}

func commonMarkLinkTitleEnd(content string, start, limit int) (int, bool) {
	if start >= limit || !isCommonMarkLinkTitleDelimiter(content[start]) {
		return 0, false
	}
	opener := content[start]
	closer := opener
	if opener == '(' {
		closer = ')'
	}
	lineHasContent := true // The opening delimiter makes this line nonblank.
	for pos := start + 1; pos < limit; {
		if width := markdownLineEndingWidth(content, pos); width > 0 {
			if !lineHasContent {
				return 0, false
			}
			if pos+width > limit {
				return limit, false
			}
			pos += width
			lineHasContent = false
			continue
		}
		value := content[pos]
		if value == '\\' && pos+1 < limit && isASCIICommonMarkPunctuation(content[pos+1]) {
			lineHasContent = true
			pos += 2
			continue
		}
		if value == closer {
			return pos + 1, true
		}
		if opener == '(' && value == '(' {
			return 0, false
		}
		if value != ' ' && value != '\t' {
			lineHasContent = true
		}
		pos++
	}
	return limit, false
}

func skipMarkdownSpacesTabs(content string, start, limit int) int {
	for start < limit && (content[start] == ' ' || content[start] == '\t') {
		start++
	}
	return start
}

func markdownLineEndingWidth(content string, pos int) int {
	if pos < 0 || pos >= len(content) {
		return 0
	}
	switch content[pos] {
	case '\n':
		return 1
	case '\r':
		if pos+1 < len(content) && content[pos+1] == '\n' {
			return 2
		}
		return 1
	default:
		return 0
	}
}

func isCommonMarkLinkTitleDelimiter(value byte) bool {
	return value == '\'' || value == '"' || value == '('
}

func isASCIICommonMarkPunctuation(value byte) bool {
	return value >= '!' && value <= '/' || value >= ':' && value <= '@' ||
		value >= '[' && value <= '`' || value >= '{' && value <= '~'
}

func isMarkdownBlankLine(line string) bool {
	return strings.Trim(line, " \t") == ""
}

func containsMarkdownList(containers []markdownContainer) bool {
	for _, container := range containers {
		if container.kind == markdownList {
			return true
		}
	}
	return false
}

func firstNewContainerCannotInterruptParagraph(containers, active []markdownContainer) bool {
	start := markdownContainerPrefixLength(containers, active)
	if start >= len(containers) {
		return false
	}
	first := containers[start]
	return first.kind == markdownList && first.orderedStart >= 0 && first.orderedStart != 1
}

func markdownContainerPrefixLength(containers, prefix []markdownContainer) int {
	if len(prefix) > len(containers) {
		return 0
	}
	for i := range prefix {
		if prefix[i] != containers[i] {
			return 0
		}
	}
	return len(prefix)
}

func markdownContainersAreLists(containers []markdownContainer) bool {
	if len(containers) == 0 {
		return false
	}
	for _, container := range containers {
		if container.kind != markdownList {
			return false
		}
	}
	return true
}

func markdownHTMLBlockOpening(line string, listContainers []markdownContainer) (string, []markdownContainer) {
	if remainder, containers, _, ok := stripMarkdownOpeningContainersWithActive(line, listContainers); ok {
		return remainder, containers
	}
	return line, nil
}

func stripMarkdownOpeningContainersWithActive(line string, active []markdownContainer) (string, []markdownContainer, int, bool) {
	if len(active) > 0 {
		if remainder, matched := stripMarkdownContainerPrefix(line, active); matched > 0 {
			tail, added, ok := stripMarkdownOpeningContainers(remainder)
			if ok {
				// A sibling nested-list marker matches its ancestor containers but
				// not the previous child container. Preserve that matched prefix so
				// later continuation content remains scoped to the ancestor item.
				containers := append([]markdownContainer(nil), active[:matched]...)
				containers = append(containers, added...)
				return tail, containers, matched, true
			}
		}
	}
	remainder, containers, ok := stripMarkdownOpeningContainers(line)
	return remainder, containers, 0, ok
}

// isCompleteCommonMarkHTMLTag recognizes the complete open/closing-tag shape
// used by CommonMark HTML block type 7. It deliberately parses the small tag
// grammar instead of accepting the first later '>', which would misclassify
// prose such as "<span> explanation" as an HTML block.
func isCompleteCommonMarkHTMLTag(line string) bool {
	if len(line) < 3 || line[0] != '<' {
		return false
	}
	pos := 1
	closing := false
	if line[pos] == '/' {
		closing = true
		pos++
	}
	nameStart := pos
	if pos >= len(line) || !isASCIIAlpha(line[pos]) {
		return false
	}
	pos++
	for pos < len(line) && (isASCIIAlpha(line[pos]) || isASCIIDigit(line[pos]) || line[pos] == '-') {
		pos++
	}
	tagName := strings.ToLower(line[nameStart:pos])
	if tagName == "pre" || tagName == "script" || tagName == "style" || tagName == "textarea" {
		return false
	}
	if closing {
		for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
			pos++
		}
		return pos == len(line)-1 && line[pos] == '>'
	}

	for {
		spaceStart := pos
		for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
			pos++
		}
		if pos == len(line)-1 && line[pos] == '>' {
			return true
		}
		if pos+1 == len(line)-1 && line[pos] == '/' && line[pos+1] == '>' {
			return true
		}
		if pos >= len(line) || pos == spaceStart || !isHTMLAttributeNameStart(line[pos]) {
			return false
		}
		pos++
		for pos < len(line) && isHTMLAttributeNameByte(line[pos]) {
			pos++
		}
		for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
			pos++
		}
		if pos >= len(line) || line[pos] != '=' {
			continue
		}
		pos++
		for pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
			pos++
		}
		if pos >= len(line) {
			return false
		}
		if line[pos] == '\'' || line[pos] == '"' {
			quote := line[pos]
			pos++
			for pos < len(line) && line[pos] != quote {
				pos++
			}
			if pos >= len(line) {
				return false
			}
			pos++
			continue
		}
		valueStart := pos
		for pos < len(line) && line[pos] != ' ' && line[pos] != '\t' && line[pos] != '>' {
			switch line[pos] {
			case '"', '\'', '=', '<', '`':
				return false
			}
			pos++
		}
		if pos == valueStart {
			return false
		}
	}
}

func isHTMLAttributeNameStart(value byte) bool {
	return isASCIIAlpha(value) || value == '_' || value == ':'
}

func isHTMLAttributeNameByte(value byte) bool {
	return isHTMLAttributeNameStart(value) || isASCIIDigit(value) || value == '.' || value == '-'
}

func startsNonParagraphMarkdownBlock(line string, paragraphAlreadyOpen bool) bool {
	line = strings.TrimSuffix(line, "\r")
	if remainder, containers, ok := stripMarkdownOpeningContainers(line); ok && len(containers) > 0 {
		line = remainder
	}
	if !paragraphAlreadyOpen {
		if definitionEnd, ok := commonMarkLinkReferenceDefinitionEnd(line, 0); definitionEnd == commonMarkScanBudgetExceeded || ok && definitionEnd == len(line) {
			return true
		}
	}
	if markdownLineStartsIndentedCode(line) {
		// Indented code cannot interrupt a paragraph; in that state it is an
		// ordinary continuation line rather than a new block.
		return !paragraphAlreadyOpen
	}
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok {
		return true
	}
	if trimmed == "" {
		return !paragraphAlreadyOpen
	}
	if standaloneBlurbMarkerText(line) {
		return true
	}
	if trimmed[0] == '#' {
		width := 0
		for width < len(trimmed) && width < 7 && trimmed[width] == '#' {
			width++
		}
		if width >= 1 && width <= 6 && (width == len(trimmed) || trimmed[width] == ' ' || trimmed[width] == '\t') {
			return true
		}
	}
	nonSpace := strings.ReplaceAll(strings.ReplaceAll(trimmed, " ", ""), "\t", "")
	if nonSpace != "" {
		allSame := true
		for i := 1; i < len(nonSpace); i++ {
			if nonSpace[i] != nonSpace[0] {
				allSame = false
				break
			}
		}
		if allSame && nonSpace[0] == '=' {
			return paragraphAlreadyOpen
		}
		if allSame && nonSpace[0] == '-' {
			return paragraphAlreadyOpen || len(nonSpace) >= 3
		}
		if allSame && len(nonSpace) >= 3 && (nonSpace[0] == '*' || nonSpace[0] == '_') {
			return true
		}
	}
	return false
}

func markdownLineStartsIndentedCode(line string) bool {
	line = strings.TrimSuffix(line, "\r")
	leading := countLeadingSpaces(line)
	return leading >= 4 || leading < len(line) && line[leading] == '\t'
}

func markdownFenceOpening(line string) (byte, int, string, []markdownContainer, bool) {
	remainder, containers, ok := stripMarkdownOpeningContainers(line)
	if !ok {
		return 0, 0, "", nil, false
	}
	char, width, rest, ok := markdownFenceRun(remainder)
	return char, width, rest, containers, ok
}

// stripMarkdownOpeningContainers removes list and blockquote prefixes from a
// possible fence-opening line. List continuation indentation is retained as a
// sequence so closing fences and content can be recognized without treating
// ordinary four-space-indented code as a top-level fence.
func stripMarkdownOpeningContainers(line string) (string, []markdownContainer, bool) {
	line = strings.TrimSuffix(line, "\r")
	containers := make([]markdownContainer, 0, 2)
	pos := 0
	for {
		spaces := countLeadingSpaces(line[pos:])
		if spaces > 3 {
			if len(containers) > 0 {
				return line[pos:], containers, true
			}
			return "", nil, false
		}
		pos += spaces
		if pos >= len(line) {
			return line[pos:], containers, true
		}

		if line[pos] == '>' {
			if len(containers) >= maxMarkdownContainerDepth {
				return "", nil, false
			}
			containers = append(containers, markdownContainer{kind: markdownBlockquote})
			pos++
			if pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
				pos++
			}
			continue
		}

		markerWidth, gapBytes, continuationIndent, orderedStart, isList := markdownListMarker(line[pos:], spaces)
		if isList {
			if len(containers) >= maxMarkdownContainerDepth {
				return "", nil, false
			}
			containers = append(containers, markdownContainer{
				kind:         markdownList,
				indent:       continuationIndent,
				orderedStart: orderedStart,
			})
			pos += markerWidth + gapBytes
			continue
		}

		return line[pos:], containers, true
	}
}

func stripMarkdownContainers(line string, containers []markdownContainer) (string, bool) {
	remainder, matched := stripMarkdownContainerPrefix(line, containers)
	return remainder, matched == len(containers)
}

func stripMarkdownContainerPrefix(line string, containers []markdownContainer) (string, int) {
	line = strings.TrimSuffix(line, "\r")
	pos := 0
	for index, container := range containers {
		containerStart := pos
		switch container.kind {
		case markdownBlockquote:
			spaces := countLeadingSpaces(line[pos:])
			if spaces > 3 {
				return line[containerStart:], index
			}
			pos += spaces
			if pos >= len(line) || line[pos] != '>' {
				return line[containerStart:], index
			}
			pos++
			if pos < len(line) && (line[pos] == ' ' || line[pos] == '\t') {
				pos++
			}
		case markdownList:
			if container.indent <= 0 || len(line)-pos < container.indent {
				return line[containerStart:], index
			}
			for i := 0; i < container.indent; i++ {
				if line[pos+i] != ' ' {
					return line[containerStart:], index
				}
			}
			pos += container.indent
		}
	}
	return line[pos:], len(containers)
}

func markdownListMarker(line string, markerColumn int) (int, int, int, int, bool) {
	if len(line) == 0 {
		return 0, 0, 0, 0, false
	}
	markerWidth := 0
	orderedStart := -1
	switch line[0] {
	case '-', '+', '*':
		markerWidth = 1
	default:
		for markerWidth < len(line) && markerWidth < 9 && line[markerWidth] >= '0' && line[markerWidth] <= '9' {
			markerWidth++
		}
		if markerWidth == 0 || markerWidth >= len(line) || (line[markerWidth] != '.' && line[markerWidth] != ')') {
			return 0, 0, 0, 0, false
		}
		orderedStart, _ = strconv.Atoi(line[:markerWidth])
		markerWidth++
	}
	if markerWidth == len(line) {
		return markerWidth, 0, markerColumn + markerWidth + 1, orderedStart, true
	}
	if strings.Trim(line[markerWidth:], " \t") == "" {
		return markerWidth, len(line) - markerWidth, markerColumn + markerWidth + 1, orderedStart, true
	}
	if line[markerWidth] != ' ' && line[markerWidth] != '\t' {
		return 0, 0, 0, 0, false
	}
	afterMarker := markerColumn + markerWidth
	column := afterMarker
	whitespaceEnd := markerWidth
	for whitespaceEnd < len(line) && (line[whitespaceEnd] == ' ' || line[whitespaceEnd] == '\t') {
		if line[whitespaceEnd] == ' ' {
			column++
		} else {
			column += 4 - column%4
		}
		whitespaceEnd++
	}
	paddingColumns := column - afterMarker
	gapBytes := whitespaceEnd - markerWidth
	if paddingColumns > 4 {
		gapBytes = 1
		if line[markerWidth] == ' ' {
			paddingColumns = 1
		} else {
			paddingColumns = 4 - afterMarker%4
		}
	}
	return markerWidth, gapBytes, afterMarker + paddingColumns, orderedStart, true
}

func countLeadingSpaces(line string) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}

func standaloneMarkdownText(line string) (string, int, bool) {
	line = strings.TrimSuffix(line, "\r")
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || (indent < len(line) && line[indent] == '\t') {
		return "", 0, false
	}
	return strings.Trim(line[indent:], " \t"), indent, true
}

func markdownFenceRun(line string) (byte, int, string, bool) {
	trimmed, _, ok := standaloneMarkdownText(line)
	if !ok || len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, "", false
	}
	char := trimmed[0]
	width := 0
	for width < len(trimmed) && trimmed[width] == char {
		width++
	}
	if width < 3 {
		return 0, 0, "", false
	}
	return char, width, trimmed[width:], true
}

func markdownHeading(line markdownLine) (int, string, bool) {
	if !line.outsideFence || !line.topLevel {
		return 0, "", false
	}
	trimmed, _, ok := standaloneMarkdownText(line.text)
	if !ok || trimmed == "" || trimmed[0] != '#' {
		return 0, "", false
	}
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level > 6 || level == len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
		return 0, "", false
	}
	body := strings.TrimRight(trimmed[level:], " \t")
	closingStart := len(body)
	for closingStart > 0 && body[closingStart-1] == '#' {
		closingStart--
	}
	// CommonMark only treats a trailing # run as an ATX closing sequence when
	// whitespace separates it from the heading text. Without that separator,
	// the hashes are literal title bytes and must participate in legacy matching.
	if closingStart < len(body) && closingStart > 0 && (body[closingStart-1] == ' ' || body[closingStart-1] == '\t') {
		body = strings.TrimRight(body[:closingStart], " \t")
	}
	title := strings.TrimSpace(body)
	return level, title, true
}

// markdownSetextHeading recognizes a top-level CommonMark setext underline at
// underlineIndex and returns the heading level. The caller uses the preceding
// line as the start of the new section; recognizing that boundary prevents a
// legacy signature from being assembled across unrelated Markdown sections.
func markdownSetextHeading(lines []markdownLine, underlineIndex int) (int, int, bool) {
	if underlineIndex <= 0 || underlineIndex >= len(lines) {
		return 0, 0, false
	}
	underline := lines[underlineIndex]
	previous := lines[underlineIndex-1]
	if !underline.outsideFence || !underline.topLevel || !previous.outsideFence || !previous.topLevel {
		return 0, 0, false
	}
	if isMarkdownBlankLine(previous.text) || startsNonParagraphMarkdownBlock(previous.text, false) {
		return 0, 0, false
	}
	trimmed, _, ok := standaloneMarkdownText(underline.text)
	if !ok || trimmed == "" {
		return 0, 0, false
	}
	marker := trimmed[0]
	if marker != '=' && marker != '-' {
		return 0, 0, false
	}
	for i := 1; i < len(trimmed); i++ {
		if trimmed[i] != marker {
			return 0, 0, false
		}
	}
	paragraphStart := underlineIndex - 1
	for paragraphStart > 0 {
		candidate := lines[paragraphStart-1]
		if !candidate.outsideFence || !candidate.topLevel || isMarkdownBlankLine(candidate.text) || startsNonParagraphMarkdownBlock(candidate.text, false) {
			break
		}
		paragraphStart--
	}
	if marker == '=' {
		return 1, paragraphStart, true
	}
	return 2, paragraphStart, true
}

func findLegacyBlurb(content string) (legacyBlurbSpan, bool) {
	span, ok, _, _ := findLegacyBlurbWithPolicyChecked(content, false)
	return span, ok
}

func findLegacyBlurbWithPolicy(content string, consumeAmbiguousFence bool) (legacyBlurbSpan, bool, bool) {
	span, ok, ambiguous, _ := findLegacyBlurbWithPolicyChecked(content, consumeAmbiguousFence)
	return span, ok, ambiguous
}

func findLegacyBlurbWithPolicyChecked(content string, consumeAmbiguousFence bool) (legacyBlurbSpan, bool, bool, error) {
	lines, scanErr := scanMarkdownLinesChecked(content)
	if scanErr != nil {
		return legacyBlurbSpan{}, false, false, scanErr
	}
	for i, line := range lines {
		level, title, ok := markdownHeading(line)
		if !ok || level != 3 || title != "Using bv as an AI sidecar" {
			continue
		}

		sectionEnd := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if nextLevel, _, isHeading := markdownHeading(lines[j]); isHeading && nextLevel <= level {
				sectionEnd = j
				break
			}
			if nextLevel, headingStart, isHeading := markdownSetextHeading(lines, j); isHeading && nextLevel <= level {
				sectionEnd = headingStart
				break
			}
		}

		nextPattern := 1 // The heading above already matched LegacyBlurbPatterns[0].
		endLine := -1
		for j := i + 1; j < sectionEnd; j++ {
			if !lines[j].outsideFence {
				continue
			}
			text := lines[j].text
			searchFrom := 0
			for nextPattern < len(LegacyBlurbPatterns) {
				offset := strings.Index(text[searchFrom:], LegacyBlurbPatterns[nextPattern])
				if offset < 0 {
					break
				}
				searchFrom += offset + len(LegacyBlurbPatterns[nextPattern])
				nextPattern++
				if nextPattern == len(LegacyBlurbPatterns) {
					endLine = j
					break
				}
			}
			if endLine >= 0 {
				break
			}
		}
		if endLine == -1 {
			continue
		}

		end := lines[endLine].end
		ambiguousFencePreserved := false
		// Some historical copies have a bare closing delimiter immediately
		// after the identifying sentence. Do not skip blank lines looking for
		// one: a later bare fence is an unrelated user code-block opener and
		// must be preserved.
		if next := endLine + 1; next < sectionEnd {
			char, width, rest, isFence := markdownFenceRun(lines[next].text)
			if isFence && strings.TrimSpace(rest) == "" {
				clearlyTrailing := legacyFenceIsClearlyTrailing(lines, next, sectionEnd, char, width)
				if consumeAmbiguousFence || clearlyTrailing {
					end = lines[next].end
				} else {
					ambiguousFencePreserved = true
				}
			}
		}
		return legacyBlurbSpan{start: line.start, end: end}, true, ambiguousFencePreserved, nil
	}
	return legacyBlurbSpan{}, false, false, nil
}

func legacyFenceIsClearlyTrailing(lines []markdownLine, opener, sectionEnd int, char byte, width int) bool {
	if hasMatchingFenceClose(lines, opener+1, sectionEnd, char, width) {
		return false
	}
	for i := opener + 1; i < sectionEnd; i++ {
		if strings.TrimSpace(lines[i].text) == "" {
			continue
		}
		// Any content after an unmatched candidate may belong to an unfinished
		// user fence, including lines that look like Markdown headings.
		return false
	}
	return true
}

func hasMatchingFenceClose(lines []markdownLine, start, end int, char byte, width int) bool {
	for i := start; i < end; i++ {
		candidateChar, candidateWidth, rest, ok := markdownFenceRun(lines[i].text)
		if ok && candidateChar == char && candidateWidth >= width && strings.TrimSpace(rest) == "" {
			return true
		}
	}
	return false
}

func removeDelimitedBlurb(content string, startIdx, endIdx int) string {
	prefixEnd := trimLineBreaksBefore(content, startIdx)
	suffixStart := trimLineBreaksAfter(content, endIdx)
	if prefixEnd > 0 {
		separator := preferredLineBreak(content[prefixEnd:startIdx] + content[endIdx:suffixStart])
		return content[:prefixEnd] + separator + content[suffixStart:]
	}
	return content[:prefixEnd] + content[suffixStart:]
}

func trimLineBreaksBefore(content string, idx int) int {
	for idx > 0 {
		switch content[idx-1] {
		case '\n':
			idx--
			if idx > 0 && content[idx-1] == '\r' {
				idx--
			}
		case '\r':
			idx--
		default:
			return idx
		}
	}
	return idx
}

func trimLineBreaksAfter(content string, idx int) int {
	for idx < len(content) {
		switch content[idx] {
		case '\r':
			idx++
			if idx < len(content) && content[idx] == '\n' {
				idx++
			}
		case '\n':
			idx++
		default:
			return idx
		}
	}
	return idx
}

func preferredLineBreak(removedWhitespace string) string {
	if !strings.ContainsAny(removedWhitespace, "\r\n") {
		return ""
	}
	if strings.Contains(removedWhitespace, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(removedWhitespace, "\r") && !strings.Contains(removedWhitespace, "\n") {
		return "\r"
	}
	return "\n"
}
