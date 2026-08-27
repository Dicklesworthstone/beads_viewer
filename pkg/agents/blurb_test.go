package agents

import (
	"fmt"
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
			name:     "has blurb v1",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v1 -->\nSome content\n<!-- end-bv-agent-instructions -->",
			expected: true,
		},
		{
			name:     "has blurb v2",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v2 -->\nSome content\n<!-- end-bv-agent-instructions -->",
			expected: true,
		},
		{
			name:     "has blurb v3",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v3 -->\nSome content\n<!-- end-bv-agent-instructions -->",
			expected: true,
		},
		{
			name:     "has blurb v4",
			content:  "# My AGENTS.md\n\n<!-- bv-agent-instructions-v4 -->\nSome content\n<!-- end-bv-agent-instructions -->",
			expected: true,
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
			content:  "<!-- bv-agent-instructions-v1 -->",
			expected: 1,
		},
		{
			name:     "version 2",
			content:  "<!-- bv-agent-instructions-v2 -->",
			expected: 2,
		},
		{
			name:     "version 3",
			content:  "<!-- bv-agent-instructions-v3 -->",
			expected: 3,
		},
		{
			name:     "version 4",
			content:  "<!-- bv-agent-instructions-v4 -->",
			expected: 4,
		},
		{
			name:     "version 10 (multi-digit)",
			content:  "<!-- bv-agent-instructions-v10 -->",
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
	if !strings.Contains(result, "br ready --json") {
		t.Error("AppendBlurb() result missing 'br ready --json' command")
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

func TestAppendBlurbPreservesLineEndingConvention(t *testing.T) {
	tests := []struct {
		name    string
		content string
		eol     string
	}{
		{name: "LF", content: "# Header\n\nText\n", eol: "\n"},
		{name: "CRLF", content: "# Header\r\n\r\nText\r\n", eol: "\r\n"},
		{name: "bare CR", content: "# Header\r\rText\r", eol: "\r"},
		{name: "no final newline", content: "# Header", eol: "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendBlurb(tt.content)
			wantBlurb := strings.ReplaceAll(AgentBlurb, "\n", tt.eol)
			if !strings.Contains(got, wantBlurb) {
				t.Fatalf("AppendBlurb() did not use %q throughout generated block", tt.eol)
			}
			withoutExpected := strings.ReplaceAll(got, tt.eol, "")
			if strings.ContainsAny(withoutExpected, "\r\n") {
				t.Fatalf("AppendBlurb() introduced mixed line endings: %q", got)
			}
			updated := UpdateBlurb(got)
			if second := UpdateBlurb(updated); second != updated {
				t.Fatal("UpdateBlurb() was not idempotent after preserving line endings")
			}
		})
	}
}

func TestAppendBlurbEmptyHasNoLeadingBlankLines(t *testing.T) {
	got := AppendBlurb("")
	want := AgentBlurb + "\n"
	if got != want {
		t.Fatalf("AppendBlurb(\"\") = %q, want generated blurb without a leading blank line", got)
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

func TestRemoveBlurbPreservesSurroundingLineBreak(t *testing.T) {
	content := "# My AGENTS.md\n\nBefore blurb.\n\n" +
		"<!-- bv-agent-instructions-v1 -->\nGenerated content\n<!-- end-bv-agent-instructions -->\n\n" +
		"After blurb.\n"

	result := RemoveBlurb(content)
	expected := "# My AGENTS.md\n\nBefore blurb.\nAfter blurb.\n"
	if result != expected {
		t.Fatalf("RemoveBlurb() = %q, want %q", result, expected)
	}
}

func TestRemoveBlurbRejectsEndMarkerBeforeStart(t *testing.T) {
	content := "# My AGENTS.md\n\n" +
		"Example marker that is not an injected blurb:\n" +
		BlurbEndMarker + "\n\n" +
		"Before blurb.\n\n" +
		"<!-- bv-agent-instructions-v1 -->\nGenerated content\n" +
		BlurbEndMarker + "\n\n" +
		"After blurb.\n"

	if result := RemoveBlurb(content); result != content {
		t.Fatalf("RemoveBlurb() changed malformed content: got %q, want %q", result, content)
	}
	if _, err := removeBlurbsChecked(content); err == nil {
		t.Fatal("checked removal accepted an end marker before a start marker")
	}
}

func TestRemoveBlurbPreservesCRLFSeparator(t *testing.T) {
	content := "# My AGENTS.md\r\n\r\nBefore blurb.\r\n\r\n" +
		"<!-- bv-agent-instructions-v1 -->\r\nGenerated content\r\n<!-- end-bv-agent-instructions -->\r\n\r\n" +
		"After blurb.\r\n"

	result := RemoveBlurb(content)
	expected := "# My AGENTS.md\r\n\r\nBefore blurb.\r\nAfter blurb.\r\n"
	if result != expected {
		t.Fatalf("RemoveBlurb() = %q, want %q", result, expected)
	}
}

func TestBareCRBlurbLinesPreserveExactOffsetsAndSeparator(t *testing.T) {
	content := "# My AGENTS.md\r\rBefore blurb.\r\r" +
		"<!-- bv-agent-instructions-v1 -->\rGenerated content\r<!-- end-bv-agent-instructions -->\r\r" +
		"After blurb.\r"

	if !ContainsBlurb(content) {
		t.Fatal("bare-CR versioned blurb was not detected")
	}
	if version := GetBlurbVersion(content); version != 1 {
		t.Fatalf("GetBlurbVersion()=%d for bare-CR content, want 1", version)
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}

	want := "# My AGENTS.md\r\rBefore blurb.\rAfter blurb.\r"
	if got := RemoveBlurb(content); got != want {
		t.Fatalf("RemoveBlurb() changed bare-CR offsets or separator:\n got: %q\nwant: %q", got, want)
	}
}

func TestInlineMarkersAreNotInstalledBlurbs(t *testing.T) {
	content := "before<!-- bv-agent-instructions-v1 -->generated<!-- end-bv-agent-instructions -->after"

	if ContainsBlurb(content) {
		t.Fatal("inline marker text must not be treated as an installed blurb")
	}
	if result := RemoveBlurb(content); result != content {
		t.Fatalf("RemoveBlurb() changed inline documentation: got %q, want %q", result, content)
	}
}

func TestMarkerAndLegacyExamplesInsideMultilineHTMLCommentsArePreserved(t *testing.T) {
	versioned := "# Documentation\n\n<!-- hidden example\n" +
		BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n-->\n\n# Keep\n"
	if ContainsAnyBlurb(versioned) {
		t.Fatal("marker-shaped example inside multiline HTML comment counted as installed")
	}
	if version := GetBlurbVersion(versioned); version != 0 {
		t.Fatalf("GetBlurbVersion()=%d for HTML-comment example, want 0", version)
	}
	if count, err := inspectBlurbStructure(versioned); err == nil || count != 0 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want fail-closed stray end marker", count, err)
	}
	if got := RemoveBlurb(versioned); got != versioned {
		t.Fatalf("RemoveBlurb() changed HTML-comment marker example:\n got: %q\nwant: %q", got, versioned)
	}

	legacy := "# Documentation\n\n<!-- hidden legacy example\n" +
		"### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n-->\n\n# Keep\n"
	if ContainsLegacyBlurb(legacy) {
		t.Fatal("legacy-shaped example inside multiline HTML comment counted as installed")
	}
	if got := RemoveLegacyBlurb(legacy); got != legacy {
		t.Fatalf("RemoveLegacyBlurb() changed HTML-comment legacy example:\n got: %q\nwant: %q", got, legacy)
	}
	if got := RemoveBlurb(legacy); got != legacy {
		t.Fatalf("RemoveBlurb() changed HTML-comment legacy example:\n got: %q\nwant: %q", got, legacy)
	}
}

func TestMarkerAndLegacyExamplesInsideHTMLRawBlocksArePreserved(t *testing.T) {
	tags := []struct {
		name    string
		opening string
		closing string
	}{
		{name: "pre", opening: `<PrE class="example">`, closing: "</pRE>"},
		{name: "script", opening: `<SCRIPT type="text/plain">`, closing: "</script>"},
		{name: "style", opening: `<Style media="screen">`, closing: "</STYLE>"},
		{name: "textarea", opening: `<TEXTAREA aria-label="example">`, closing: "</TextArea>"},
	}

	for _, tt := range tags {
		t.Run(tt.name+"/versioned", func(t *testing.T) {
			content := "# Documentation\n\n" + tt.opening + "\n" +
				BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n" +
				tt.closing + "\n\n# Keep\n"
			if ContainsAnyBlurb(content) {
				t.Fatal("marker-shaped raw-block example counted as installed")
			}
			if version := GetBlurbVersion(content); version != 0 {
				t.Fatalf("GetBlurbVersion()=%d for raw-block example, want 0", version)
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 0 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 0, nil", count, err)
			}
			if got := RemoveBlurb(content); got != content {
				t.Fatalf("RemoveBlurb() changed raw-block marker example:\n got: %q\nwant: %q", got, content)
			}
		})

		t.Run(tt.name+"/legacy", func(t *testing.T) {
			content := "# Documentation\n\n" + tt.opening + "\n" +
				"### Using bv as an AI sidecar\n\n" +
				"--robot-insights\n--robot-plan\n" +
				"bv already computes the hard parts for you.\n" +
				tt.closing + "\n\n# Keep\n"
			if ContainsLegacyBlurb(content) {
				t.Fatal("legacy-shaped raw-block example counted as installed")
			}
			if got := RemoveLegacyBlurb(content); got != content {
				t.Fatalf("RemoveLegacyBlurb() changed raw-block legacy example:\n got: %q\nwant: %q", got, content)
			}
			if got := RemoveBlurb(content); got != content {
				t.Fatalf("RemoveBlurb() changed raw-block legacy example:\n got: %q\nwant: %q", got, content)
			}
		})
	}

	real := BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(real) {
		t.Fatal("top-level standalone marker comments stopped being recognized")
	}
	if count, err := inspectBlurbStructure(real); err != nil || count != 1 {
		t.Fatalf("top-level marker structure count=%d err=%v, want 1, nil", count, err)
	}
}

func TestHTMLCommentBytesInFenceInfoDoNotHideFollowingBlurb(t *testing.T) {
	content := "```text <!-- documentation token\n" +
		"marker-shaped prose only\n" +
		"```\n\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"

	if !ContainsBlurb(content) {
		t.Fatal("HTML-comment bytes in a valid fence info string hid the installed blurb that followed")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestCommonMarkType7HTMLDoesNotHideFollowingInstalledBlurb(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "trailing prose is not complete tag", line: "<span> documentation"},
		{name: "complete tag cannot interrupt paragraph", line: "<span>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "paragraph continues\n" + tt.line + "\n" +
				BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
			if !ContainsBlurb(content) {
				t.Fatal("type-7-like paragraph content hid the installed blurb")
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
			}
		})
	}
}

func TestCommonMarkHTMLBlankRequiresOnlySpacesOrTabs(t *testing.T) {
	content := "<div>\n\u00a0\n" + BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n\n# Keep\n"
	if ContainsAnyBlurb(content) {
		t.Fatal("NBSP incorrectly terminated an HTML block and exposed a marker example")
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed marker documentation after NBSP:\n got: %q\nwant: %q", got, content)
	}
}

func TestHTMLBlockEndsWithItsMarkdownContainer(t *testing.T) {
	for _, opening := range []string{
		"- item\n  <div>\n",
		"> <div>\n",
	} {
		content := opening + BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
		if !ContainsBlurb(content) {
			t.Fatalf("HTML block escaped its container and hid installed blurb:\n%s", content)
		}
		if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
			t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
		}
	}
}

func TestCommonMarkHTMLCommentEndsAtFirstClosingToken(t *testing.T) {
	content := "<!-- documentation mentions <!-- token -->\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(content) {
		t.Fatal("nested-looking comment token hid installed blurb after first close")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestMarkerExamplesInsideAllHTMLBlockFormsArePreserved(t *testing.T) {
	tests := []struct {
		name    string
		opening string
		closing string
	}{
		{name: "block tag", opening: `<div class="example">`, closing: `</div>`},
		{name: "table tag", opening: `<table>`, closing: `</table>`},
		{name: "custom tag", opening: `<agent-example>`, closing: `</agent-example>`},
		{name: "processing instruction", opening: `<?agent example`, closing: `?>`},
		{name: "cdata", opening: `<![CDATA[`, closing: `]]>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "# Documentation\n\n" + tt.opening + "\n" +
				BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n" +
				tt.closing + "\n\n# Keep\n"
			if ContainsAnyBlurb(content) {
				t.Fatal("marker-shaped HTML-block example counted as installed")
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 0 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 0, nil", count, err)
			}
			if got := RemoveBlurb(content); got != content {
				t.Fatalf("RemoveBlurb() changed HTML-block documentation:\n got: %q\nwant: %q", got, content)
			}
		})
	}
}

func TestHTMLDeclarationEndsAtFirstGreaterThanAndMarkerContentFailsClosed(t *testing.T) {
	content := "<!AGENT example\n" + BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n>\n"
	if count, err := inspectBlurbStructure(content); err == nil || count != 0 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want fail-closed stray end marker", count, err)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed malformed declaration documentation:\n got: %q\nwant: %q", got, content)
	}
}

func TestInlineCodeCommentTokenDoesNotHideFollowingInstalledBlurb(t *testing.T) {
	content := "Document the token `<!--` in prose.\n\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"

	if !ContainsBlurb(content) {
		t.Fatal("inline-code comment token hid the installed blurb that followed")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
	want := "Document the token `<!--` in prose.\n"
	if got := RemoveBlurb(content); got != want {
		t.Fatalf("RemoveBlurb() = %q, want %q", got, want)
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
	// Start with the previous br-only blurb version.
	oldContent := "# My AGENTS.md\n\n<!-- bv-agent-instructions-v3 -->\nOld br-only blurb content\n<!-- end-bv-agent-instructions -->\n"
	result := UpdateBlurb(oldContent)

	// Should have exactly one blurb
	count := strings.Count(result, BlurbStartMarker)
	if count != 1 {
		t.Errorf("UpdateBlurb() resulted in %d blurbs, want 1", count)
	}

	// Should have current blurb content
	if !strings.Contains(result, "br ready --json") {
		t.Error("UpdateBlurb() result missing current blurb content")
	}
	if !strings.Contains(result, "bd update <id> --claim --json") {
		t.Error("UpdateBlurb() result missing Go bd workflow content")
	}
	if strings.Contains(result, "bv-agent-instructions-v3") {
		t.Error("UpdateBlurb() retained the obsolete v3 marker")
	}

	// Should preserve header
	if !strings.Contains(result, "# My AGENTS.md") {
		t.Error("UpdateBlurb() did not preserve original header")
	}
}

func TestUpdateBlurbMalformedMarkersFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "missing end marker",
			content: "# Header\n\n<!-- bv-agent-instructions-v1 -->\nUser content without an end marker",
		},
		{
			name: "nested start marker",
			content: "<!-- bv-agent-instructions-v1 -->\nOld content\n" +
				"<!-- bv-agent-instructions-v2 -->\nMore content\n<!-- end-bv-agent-instructions -->",
		},
		{
			name:    "unexpected end marker",
			content: "# Header\n<!-- end-bv-agent-instructions -->",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := UpdateBlurb(tt.content)
			second := UpdateBlurb(first)
			if first != tt.content || second != tt.content {
				t.Fatalf("malformed content changed across repeated updates:\nfirst: %q\nsecond: %q\nwant: %q", first, second, tt.content)
			}
		})
	}
}

func TestUpdateBlurbFutureVersionFailsClosed(t *testing.T) {
	content := "# Header\n\n" +
		"<!-- bv-agent-instructions-v5 -->\nnewer instructions\n<!-- end-bv-agent-instructions -->\n\n" +
		"<!-- bv-agent-instructions-v4 -->\ncurrent instructions\n<!-- end-bv-agent-instructions -->\n"

	if got := UpdateBlurb(content); got != content {
		t.Fatalf("UpdateBlurb() downgraded future instructions:\n got: %q\nwant: %q", got, content)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("checked update error=%v, want explicit future-version refusal", err)
	}
}

func TestUpdateBlurbFutureVersionRevealedByLegacyRemovalFailsClosed(t *testing.T) {
	content := "# Header\n\n" + LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v9 -->\n" +
		"```bash\nfuture command\n```\n" +
		"newer instructions\n<!-- end-bv-agent-instructions -->\n"

	if got := UpdateBlurb(content); got != content {
		t.Fatalf("UpdateBlurb() replaced a future blurb hidden by the legacy fence:\n got: %q\nwant: %q", got, content)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("checked update error=%v, want future-version refusal after legacy removal", err)
	}
}

func TestUpdateBlurbRejectsResultInsideEOFOpenFence(t *testing.T) {
	content := "# Header\n\n" +
		"<!-- bv-agent-instructions-v3 -->\nold instructions\n<!-- end-bv-agent-instructions -->\n\n" +
		"```markdown\nuser example continues to EOF\n"

	if got := UpdateBlurb(content); got != content {
		t.Fatalf("UpdateBlurb() wrote the replacement inside an EOF-open fence:\n got: %q\nwant: %q", got, content)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "validate updated") {
		t.Fatalf("checked update error=%v, want final-result validation failure", err)
	}
}

func TestRemoveBlurbMalformedMarkersFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "missing end marker",
			content: "# Header\n\n<!-- bv-agent-instructions-v1 -->\nUser content without an end marker",
		},
		{
			name: "nested start marker",
			content: "# Header\n<!-- bv-agent-instructions-v1 -->\nUser instructions\n" +
				"<!-- bv-agent-instructions-v2 -->\nMore user instructions\n<!-- end-bv-agent-instructions -->\n# Footer",
		},
		{
			name:    "unexpected end marker",
			content: "# Header\n<!-- end-bv-agent-instructions -->\n# Footer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("malformed removal changed content:\n got: %q\nwant: %q", got, tt.content)
			}
			if _, err := removeBlurbsChecked(tt.content); err == nil {
				t.Fatal("checked removal accepted malformed marker structure")
			}
		})
	}
}

func TestRemoveBlurbFutureVersionFailsClosed(t *testing.T) {
	content := "# Header\n\n<!-- bv-agent-instructions-v8 -->\nnewer\n<!-- end-bv-agent-instructions -->\n"
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() removed future instructions:\n got: %q\nwant: %q", got, content)
	}
	if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("checked removal error=%v, want future-version refusal", err)
	}
}

func TestRemoveBlurbFutureVersionRevealedByLegacyRemovalFailsClosed(t *testing.T) {
	content := "# Header\n\n" + LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v8 -->\n" +
		"```bash\nfuture command\n```\n" +
		"newer\n<!-- end-bv-agent-instructions -->\n"
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() removed future instructions hidden by the legacy fence:\n got: %q\nwant: %q", got, content)
	}
	if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("checked removal error=%v, want future-version refusal after legacy removal", err)
	}
}

func TestRemoveBlurbRemovesLegacyAndAllVersionedBlocks(t *testing.T) {
	legacy := `### Using bv as an AI sidecar

--robot-insights
--robot-plan
bv already computes the hard parts for you.
`
	content := "# Header\n\n" + legacy + "\nPreserve between.\n\n" +
		"<!-- bv-agent-instructions-v1 -->\none\n<!-- end-bv-agent-instructions -->\n\n" +
		"Preserve after first.\n\n" +
		"<!-- bv-agent-instructions-v4 -->\ntwo\n<!-- end-bv-agent-instructions -->\n\n# Footer\n"

	removed, err := removeBlurbsChecked(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{blurbStartPrefix, BlurbEndMarker, "bv already computes the hard parts"} {
		if strings.Contains(removed, unwanted) {
			t.Fatalf("removed content still contains %q:\n%s", unwanted, removed)
		}
	}
	for _, preserved := range []string{"# Header", "Preserve between.", "Preserve after first.", "# Footer"} {
		if !strings.Contains(removed, preserved) {
			t.Fatalf("removal lost %q:\n%s", preserved, removed)
		}
	}
}

func TestUpdateBlurbCollapsesMultipleCompleteBlocks(t *testing.T) {
	content := "# Header\n\n" +
		"<!-- bv-agent-instructions-v1 -->\none\n<!-- end-bv-agent-instructions -->\n\n" +
		"User instructions\n\n" +
		"<!-- bv-agent-instructions-v2 -->\ntwo\n<!-- end-bv-agent-instructions -->\n"

	updated := UpdateBlurb(content)
	if got := strings.Count(updated, blurbStartPrefix); got != 1 {
		t.Fatalf("start marker count=%d, want 1", got)
	}
	if !strings.Contains(updated, "User instructions") {
		t.Fatal("update removed content between complete blurb blocks")
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
			name:     "single complete current version",
			content:  "<!-- bv-agent-instructions-v4 -->\ncontent\n<!-- end-bv-agent-instructions -->",
			expected: false, // v4 is current, no update needed
		},
		{
			name:     "unterminated current version",
			content:  "<!-- bv-agent-instructions-v4 -->\ncontent",
			expected: true,
		},
		{
			name: "duplicate current version",
			content: "<!-- bv-agent-instructions-v4 -->\none\n<!-- end-bv-agent-instructions -->\n" +
				"<!-- bv-agent-instructions-v4 -->\ntwo\n<!-- end-bv-agent-instructions -->",
			expected: true,
		},
		{
			name:     "old v3 needs update",
			content:  "<!-- bv-agent-instructions-v3 -->",
			expected: true, // v3 lacks Go bd command guidance
		},
		{
			name:     "old v2 needs update",
			content:  "<!-- bv-agent-instructions-v2 -->",
			expected: true, // v2 has stale JSONL/git guidance
		},
		{
			name:     "old v1 needs update",
			content:  "<!-- bv-agent-instructions-v1 -->",
			expected: true, // v1 is old, needs update to v4
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
	// Verify blurb contains essential bd, br, and bv commands.
	essentials := []string{
		"br ready --json",
		"br list --status=open --json",
		"br show <id> --json",
		"br create",
		"br update <id> --status=in_progress --json",
		"br close <id> --reason=\"Completed\" --json",
		"br sync",
		"br dep add",
		"bd ready --json",
		"bd show <id> --json",
		"bd create",
		"bd update <id> --claim --json",
		"bd close <id> --json",
		"bd dep add",
		"bd export -o .beads/issues.jsonl",
		"bv --robot-triage",
		"bv --robot-next",
		"bv --robot-plan",
		"bv --robot-insights",
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

	forbidden := []string{
		"(.beads/beads.jsonl)",
		"br ready              #",
		"br list --status=open #",
		"br show <id>          #",
		"git commit -m",
		"git push",
	}
	for _, text := range forbidden {
		if strings.Contains(AgentBlurb, text) {
			t.Errorf("AgentBlurb contains stale or overbroad guidance: %q", text)
		}
	}

	required := []string{
		".beads/issues.jsonl",
		"legacy `.beads/beads.jsonl`",
		"Do not run both trackers against the same workspace",
		"Follow this repository's own git and tracker instructions",
	}
	for _, text := range required {
		if !strings.Contains(AgentBlurb, text) {
			t.Errorf("AgentBlurb missing required v4 guidance: %q", text)
		}
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

// ambiguousSameVersionBlockSwapContent presents two complete v4 blocks whose
// visibility swaps depending on whether the legacy blurb's trailing fence is
// treated as its closer or as a user fence opener.
func ambiguousSameVersionBlockSwapContent() string {
	return LegacyBlurbContent + "\n" +
		BlurbStartMarker + "\nfirst physical block\n" + BlurbEndMarker + "\n" +
		"```\n" +
		BlurbStartMarker + "\nsecond physical block\n" + BlurbEndMarker + "\n"
}

// ambiguousTwoLegacyBlocksContent hides the second legacy section behind the
// first section's ambiguous trailing fence. The conservative removal view sees
// one legacy blurb while the hypothetical fence-consumed view sees two.
func ambiguousTwoLegacyBlocksContent() string {
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	return "# Header\n\n" + legacy + "```\n" + legacy + "```\n"
}

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

func TestLegacyHeadingRequiresWhitespaceBeforeClosingHashes(t *testing.T) {
	content := `# Documentation

### Using bv as an AI sidecar###

--robot-insights
--robot-plan
bv already computes the hard parts for you.

# Keep
`
	if ContainsLegacyBlurb(content) {
		t.Fatal("literal no-space trailing hashes were misparsed as an ATX closing sequence")
	}
	if got := RemoveLegacyBlurb(content); got != content {
		t.Fatalf("RemoveLegacyBlurb() changed a non-legacy heading:\n got: %q\nwant: %q", got, content)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed a non-legacy heading:\n got: %q\nwant: %q", got, content)
	}

	valid := strings.Replace(content, "sidecar###", "sidecar ###", 1)
	if !ContainsLegacyBlurb(valid) {
		t.Fatal("whitespace-delimited ATX closing hashes should still identify the legacy heading")
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

func TestRemoveLegacyBlurbPreservesLaterUserFence(t *testing.T) {
	legacy := `### Using bv as an AI sidecar

--robot-insights
--robot-plan
bv already computes the hard parts for you.
`
	userCode := "```\nuser code must retain both fences\n```\n"
	content := "# Header\n\n" + legacy + "\n" + userCode

	result := RemoveLegacyBlurb(content)
	if strings.Contains(result, "bv already computes the hard parts") {
		t.Fatalf("legacy blurb was not removed:\n%s", result)
	}
	if !strings.Contains(result, userCode) {
		t.Fatalf("legacy removal consumed a later user fence:\n got: %q\nwant to preserve: %q", result, userCode)
	}
}

func TestRemoveLegacyBlurbPreservesImmediatelyAdjacentUserFence(t *testing.T) {
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	userCode := "```\nuser code must retain both fences\n```\n"
	content := "# Header\n\n" + legacy + userCode

	result := RemoveLegacyBlurb(content)
	if strings.Contains(result, "bv already computes the hard parts") {
		t.Fatalf("legacy blurb was not removed:\n%s", result)
	}
	if !strings.HasSuffix(result, userCode) {
		t.Fatalf("legacy removal changed an adjacent user fence:\n got: %q\nwant suffix: %q", result, userCode)
	}
	if got := strings.Count(result, "```"); got != 2 {
		t.Fatalf("adjacent user fence delimiter count=%d, want 2", got)
	}
}

func TestRemoveLegacyBlurbPreservesIndentedLiteralFence(t *testing.T) {
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	literal := "    ```\nindented literal must remain\n"
	result := RemoveLegacyBlurb(legacy + literal)
	if !strings.HasSuffix(result, literal) {
		t.Fatalf("legacy removal consumed a four-space-indented literal:\n got: %q\nwant suffix: %q", result, literal)
	}
}

func TestRemoveLegacyBlurbPreservesUnclosedAdjacentUserFence(t *testing.T) {
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	userCode := "```\nunfinished user example\n"
	result := RemoveLegacyBlurb(legacy + userCode)
	if !strings.HasSuffix(result, userCode) {
		t.Fatalf("legacy removal consumed an unclosed adjacent user fence:\n got: %q\nwant suffix: %q", result, userCode)
	}
}

func TestRemoveLegacyBlurbPreservesUnclosedFenceWhoseBodyStartsWithHeading(t *testing.T) {
	legacy := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n--robot-plan\n" +
		"bv already computes the hard parts for you.\n"
	userCode := "```\n## literal heading inside unfinished fence\n"
	result := RemoveLegacyBlurb(legacy + userCode)
	if !strings.HasSuffix(result, userCode) {
		t.Fatalf("legacy removal consumed an unclosed user fence before a heading:\n got: %q\nwant suffix: %q", result, userCode)
	}
}

func TestCurrentBlurbHiddenByAmbiguousLegacyFenceFailsClosed(t *testing.T) {
	content := LegacyBlurbContent + "\n" +
		"<!-- bv-agent-instructions-v4 -->\n" +
		"```bash\ncurrent command\n```\n" +
		"current instructions\n<!-- end-bv-agent-instructions -->\n"

	if got := UpdateBlurb(content); got != content {
		t.Fatalf("UpdateBlurb rewrote ambiguous fenced content:\n got: %q\nwant: %q", got, content)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb rewrote ambiguous fenced content:\n got: %q\nwant: %q", got, content)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("checked update error=%v, want fail-closed malformed result", err)
	}
}

func TestVersionedBlurbsHiddenByAmbiguousLegacyFenceFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		version   int
		body      string
		wantError string
	}{
		{name: "current simple body", version: BlurbVersion, body: "current instructions\n", wantError: "malformed"},
		{name: "current bare fenced body", version: BlurbVersion, body: "```\ncurrent command\n```\ncurrent instructions\n", wantError: "malformed"},
		{name: "future simple body", version: BlurbVersion + 5, body: "future instructions\n", wantError: "refusing"},
		{name: "future bare fenced body", version: BlurbVersion + 5, body: "```\nfuture command\n```\nfuture instructions\n", wantError: "refusing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := LegacyBlurbContent + "\n" +
				fmt.Sprintf("<!-- bv-agent-instructions-v%d -->\n", tt.version) +
				tt.body + BlurbEndMarker + "\n"

			if got := UpdateBlurb(content); got != content {
				t.Fatalf("UpdateBlurb rewrote ambiguous content:\n got: %q\nwant: %q", got, content)
			}
			if got := RemoveBlurb(content); got != content {
				t.Fatalf("RemoveBlurb rewrote ambiguous content:\n got: %q\nwant: %q", got, content)
			}
			if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("checked update error=%v, want %q fail-closed error", err, tt.wantError)
			}
			if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("checked removal error=%v, want %q fail-closed error", err, tt.wantError)
			}
		})
	}
}

func TestAmbiguousLegacyInterpretationsFailClosedWithoutMutation(t *testing.T) {
	tests := []struct {
		name                    string
		content                 string
		wantError               string
		wantRealRemovals        int
		wantAnalysisRemovals    int
		wantRealVisibleBody     string
		wantAnalysisVisibleBody string
	}{
		{
			name:                    "same-version physical blocks swap visibility",
			content:                 ambiguousSameVersionBlockSwapContent(),
			wantError:               "ambiguous marker material",
			wantRealRemovals:        1,
			wantAnalysisRemovals:    1,
			wantRealVisibleBody:     "second physical block",
			wantAnalysisVisibleBody: "first physical block",
		},
		{
			name:                 "second legacy block is hidden",
			content:              ambiguousTwoLegacyBlocksContent(),
			wantError:            "removal count",
			wantRealRemovals:     1,
			wantAnalysisRemovals: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			realView, ambiguous, realRemovals, err := removeLegacyBlurbsChecked(tt.content)
			if err != nil {
				t.Fatalf("conservative legacy removal failed: %v", err)
			}
			if !ambiguous {
				t.Fatal("fixture did not preserve an ambiguous legacy fence")
			}
			analysisView, _, analysisRemovals, err := removeLegacyBlurbsCheckedWithPolicy(tt.content, true)
			if err != nil {
				t.Fatalf("hypothetical legacy removal failed: %v", err)
			}
			if realRemovals != tt.wantRealRemovals || analysisRemovals != tt.wantAnalysisRemovals {
				t.Fatalf("legacy removal counts real/analysis=%d/%d, want %d/%d", realRemovals, analysisRemovals, tt.wantRealRemovals, tt.wantAnalysisRemovals)
			}

			if tt.wantRealVisibleBody != "" {
				realBlocks, realErr := inspectBlurbBlocks(realView)
				analysisBlocks, analysisErr := inspectBlurbBlocks(analysisView)
				if realErr != nil || analysisErr != nil || len(realBlocks) != 1 || len(analysisBlocks) != 1 {
					t.Fatalf("fixture block structure real=%v/%d analysis=%v/%d, want one valid block in each view", realErr, len(realBlocks), analysisErr, len(analysisBlocks))
				}
				realBlock := realView[realBlocks[0].start:realBlocks[0].end]
				analysisBlock := analysisView[analysisBlocks[0].start:analysisBlocks[0].end]
				if !strings.Contains(realBlock, tt.wantRealVisibleBody) || !strings.Contains(analysisBlock, tt.wantAnalysisVisibleBody) {
					t.Fatalf("physical block visibility did not swap:\n real: %q\nanalysis: %q", realBlock, analysisBlock)
				}
				if realBlocks[0].version != BlurbVersion || analysisBlocks[0].version != BlurbVersion {
					t.Fatalf("visible block versions real/analysis=%d/%d, want v%d in both", realBlocks[0].version, analysisBlocks[0].version, BlurbVersion)
				}
			}

			if got := UpdateBlurb(tt.content); got != tt.content {
				t.Fatalf("UpdateBlurb changed ambiguous content:\n got: %q\nwant: %q", got, tt.content)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb changed ambiguous content:\n got: %q\nwant: %q", got, tt.content)
			}
			if _, err := updateBlurbChecked(tt.content); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("checked update error=%v, want %q", err, tt.wantError)
			}
			if _, err := removeBlurbsChecked(tt.content); err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("checked removal error=%v, want %q", err, tt.wantError)
			}
		})
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
	if !strings.Contains(result, "br ready --json") {
		t.Error("UpdateBlurb() from legacy missing current blurb content")
	}

	// Should NOT have legacy markers
	if strings.Contains(result, "bv already computes the hard parts") {
		t.Error("UpdateBlurb() from legacy still contains legacy end phrase")
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
			name:     "current blurb v4 no update",
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
			content:  "<!-- bv-agent-instructions-v0 -->",
			expected: 0, // v0 parses to 0
		},
		{
			name:     "v99 high version",
			content:  "<!-- bv-agent-instructions-v99 -->",
			expected: 99,
		},
		{
			name:     "v999 very high version",
			content:  "<!-- bv-agent-instructions-v999 -->",
			expected: 999,
		},
		{
			name:     "malformed non-numeric version",
			content:  "<!-- bv-agent-instructions-vX -->",
			expected: 0,
		},
		{
			name:     "malformed version with letters",
			content:  "<!-- bv-agent-instructions-v1a -->",
			expected: 0, // \d+ matches "1" but then pattern expects " -->" not "a -->"
		},
		{
			name:     "multiple version markers returns highest",
			content:  "<!-- bv-agent-instructions-v3 -->\nsome content\n<!-- bv-agent-instructions-v5 -->",
			expected: 5,
		},
		{
			name:     "version marker in middle of content",
			content:  "# Header\n\nSome text before\n\n<!-- bv-agent-instructions-v7 -->\n\nContent after",
			expected: 7,
		},
		{
			name:     "version marker with extra spaces (no match)",
			content:  "<!-- bv-agent-instructions-v 1 -->",
			expected: 0, // regex requires no space before digits
		},
		{
			name:     "partial marker (missing closing)",
			content:  "<!-- bv-agent-instructions-v1",
			expected: 0, // regex requires " -->"
		},
		{
			name:     "negative-looking version (just digits)",
			content:  "<!-- bv-agent-instructions-v-1 -->",
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
			content: `<!-- bv-agent-instructions-v1 -->
Content
<!-- end-bv-agent-instructions -->

## Real Section
`,
			expectPreserved: []string{"## Real Section"},
		},
		{
			name: "blurb with CRLF line endings",
			content: "# Header\r\n\r\n<!-- bv-agent-instructions-v1 -->\r\n" +
				"Content\r\n<!-- end-bv-agent-instructions -->\r\n\r\n## Footer\r\n",
			expectPreserved: []string{"# Header", "## Footer"},
		},
		{
			name: "blurb only content in file",
			content: `<!-- bv-agent-instructions-v1 -->
Content
<!-- end-bv-agent-instructions -->
`,
			expectPreserved: []string{}, // should be empty or nearly empty
		},
		{
			name:            "missing end marker",
			content:         "# Header\n\n<!-- bv-agent-instructions-v1 -->\nContent without end",
			expectPreserved: []string{"# Header", "Content without end"}, // unchanged
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RemoveBlurb(tt.content)

			// Should not contain markers
			if strings.Contains(result, "<!-- bv-agent-instructions") &&
				strings.Contains(result, "<!-- end-bv-agent-instructions -->") {
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

<!-- bv-agent-instructions-v1 -->
Current blurb
<!-- end-bv-agent-instructions -->
`,
			expected: true,
		},
		{
			name:     "only start marker no end",
			content:  "<!-- bv-agent-instructions-v1 -->\nContent",
			expected: true, // ContainsBlurb checks for start marker only
		},
		{
			name:     "only end marker",
			content:  "Content\n<!-- end-bv-agent-instructions -->",
			expected: false,
		},
		{
			name:     "marker inside code block",
			content:  "```\n<!-- bv-agent-instructions-v1 -->\n```",
			expected: false,
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

func TestCompleteMarkerExampleInsideFenceIsIgnoredAndPreserved(t *testing.T) {
	content := "# Documentation\n\n```markdown\n" +
		"<!-- bv-agent-instructions-v99 -->\nexample only\n<!-- end-bv-agent-instructions -->\n```\n"

	if ContainsAnyBlurb(content) {
		t.Fatal("complete fenced marker example must not count as an installed blurb")
	}
	if version := GetBlurbVersion(content); version != 0 {
		t.Fatalf("GetBlurbVersion()=%d for fenced example, want 0", version)
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 0 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 0, nil", count, err)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed fenced example:\n got: %q\nwant: %q", got, content)
	}
}

func TestCompleteMarkerExamplesInsideContainerFencesAreIgnoredAndPreserved(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "bullet list",
			content: "- ```markdown\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example only\n" +
				"  <!-- end-bv-agent-instructions -->\n" +
				"  ```\n",
		},
		{
			name: "ordered list",
			content: "1. ```markdown\n" +
				"   <!-- bv-agent-instructions-v99 -->\n" +
				"   example only\n" +
				"   <!-- end-bv-agent-instructions -->\n" +
				"   ```\n",
		},
		{
			name: "blockquote",
			content: "> ```markdown\n" +
				"> <!-- bv-agent-instructions-v99 -->\n" +
				"> example only\n" +
				"> <!-- end-bv-agent-instructions -->\n" +
				"> ```\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsAnyBlurb(tt.content) {
				t.Fatal("container-fenced marker example must not count as an installed blurb")
			}
			if version := GetBlurbVersion(tt.content); version != 0 {
				t.Fatalf("GetBlurbVersion()=%d for container-fenced example, want 0", version)
			}
			if count, err := inspectBlurbStructure(tt.content); err != nil || count != 0 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 0, nil", count, err)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed container-fenced example:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestCompleteMarkerExamplesInsideNakedContainersAreIgnoredAndPreserved(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "bullet continuation",
			content: "- Marker documentation:\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example only\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "ordered continuation",
			content: "1. Marker documentation:\n" +
				"   <!-- bv-agent-instructions-v99 -->\n" +
				"   example only\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "blockquote",
			content: "> <!-- bv-agent-instructions-v99 -->\n" +
				"> example only\n" +
				"> <!-- end-bv-agent-instructions -->\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsAnyBlurb(tt.content) {
				t.Fatal("container-nested marker example must not count as an installed blurb")
			}
			if version := GetBlurbVersion(tt.content); version != 0 {
				t.Fatalf("GetBlurbVersion()=%d for container-nested example, want 0", version)
			}
			if count, err := inspectBlurbStructure(tt.content); err != nil || count != 0 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 0, nil", count, err)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed container-nested example:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestNestedLegacyLookingSectionsAreIgnoredAndPreserved(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "bullet continuation",
			content: "- Historical documentation:\n" +
				"  ### Using bv as an AI sidecar\n" +
				"  --robot-insights\n" +
				"  --robot-plan\n" +
				"  bv already computes the hard parts for you.\n",
		},
		{
			name: "blockquote",
			content: "> ### Using bv as an AI sidecar\n" +
				"> --robot-insights\n" +
				"> --robot-plan\n" +
				"> bv already computes the hard parts for you.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsLegacyBlurb(tt.content) {
				t.Fatal("container-nested legacy example must not count as an installed blurb")
			}
			if got := RemoveLegacyBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveLegacyBlurb() changed nested documentation:\n got: %q\nwant: %q", got, tt.content)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed nested documentation:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestLegacyPatternsMustAppearInTemplateOrder(t *testing.T) {
	content := "### Using bv as an AI sidecar\n\n" +
		"--robot-plan\n" +
		"--robot-insights\n" +
		"bv already computes the hard parts for you.\n"

	if ContainsLegacyBlurb(content) {
		t.Fatal("out-of-order documentation was misclassified as the historical template")
	}
	if got := RemoveLegacyBlurb(content); got != content {
		t.Fatalf("RemoveLegacyBlurb() changed out-of-order documentation:\n got: %q\nwant: %q", got, content)
	}
}

func TestSetextHeadingBoundsLegacyDetection(t *testing.T) {
	content := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n\n" +
		"Next Section\n" +
		"------------\n\n" +
		"--robot-plan\n" +
		"bv already computes the hard parts for you.\n"

	if ContainsLegacyBlurb(content) {
		t.Fatal("legacy signature crossed a top-level setext section boundary")
	}
	if got := RemoveLegacyBlurb(content); got != content {
		t.Fatalf("RemoveLegacyBlurb() crossed a setext boundary:\n got: %q\nwant: %q", got, content)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() crossed a setext boundary:\n got: %q\nwant: %q", got, content)
	}
}

func TestMultilineSetextHeadingBoundsLegacyDetection(t *testing.T) {
	content := "### Using bv as an AI sidecar\n\n" +
		"--robot-insights\n\n" +
		"New section uses --robot-plan\n" +
		"bv already computes the hard parts for you.\n" +
		"continued heading\n" +
		"-----------------\n"

	if ContainsLegacyBlurb(content) {
		t.Fatal("legacy signature crossed into multiline setext heading content")
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() crossed multiline setext heading content:\n got: %q\nwant: %q", got, content)
	}
}

func TestMalformedHTMLBlockTagPrefixDoesNotHideInstalledBlurb(t *testing.T) {
	for _, prefix := range []string{"<div/not-a-tag", "</div/not-a-tag"} {
		t.Run(prefix, func(t *testing.T) {
			content := prefix + "\n" +
				BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
			if !ContainsBlurb(content) {
				t.Fatal("malformed HTML tag prefix hid a top-level installed blurb")
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
			}
		})
	}
}

func TestLazyContainerParagraphDoesNotTurnTypeSevenHTMLIntoBlock(t *testing.T) {
	for _, prefix := range []string{"- paragraph", "> paragraph"} {
		t.Run(prefix, func(t *testing.T) {
			content := prefix + "\n" +
				"<agent-example>\n" +
				BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
			if !ContainsBlurb(content) {
				t.Fatal("lazy container paragraph incorrectly turned type-7 HTML into a marker-hiding block")
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
			}
		})
	}
}

func TestIndentedContinuationDoesNotInterruptParagraph(t *testing.T) {
	content := "paragraph\n" +
		"    continuation\n" +
		"<agent-example>\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(content) {
		t.Fatal("indented paragraph continuation incorrectly enabled a marker-hiding type-7 HTML block")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestLinkReferenceDefinitionsDoNotExposeMarkerDocumentation(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "definition followed immediately by type-seven HTML",
			content: "[reference]: /url\n" +
				"<agent-example>\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n\n",
		},
		{
			name: "multiline label contains marker example",
			content: "[\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n" +
				"]: /url\n",
		},
		{
			name: "multiline title contains marker example",
			content: "[reference]: /url\n" +
				"  \"title\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n" +
				"  continued\"\n",
		},
		{
			name: "spaces before newline separate unindented title",
			content: "[reference]: /url  \n" +
				"\"title\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n" +
				"continued\"\n",
		},
		{
			name: "angle destination permits tab",
			content: "[reference]: <my\turl>\n" +
				"<agent-example>\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n\n",
		},
		{
			name: "title permits non-line-ending control",
			content: "[reference]: /url \"title\fcontinued\"\n" +
				"<agent-example>\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n\n",
		},
		{
			name: "container definition does not leave a paragraph open",
			content: "- [reference]: /url\n" +
				"<agent-example>\n" +
				"<!-- bv-agent-instructions-v99 -->\n" +
				"example only\n" +
				"<!-- end-bv-agent-instructions -->\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsAnyBlurb(tt.content) {
				t.Fatal("marker documentation inside or after a link reference definition counted as installed")
			}
			if version := GetBlurbVersion(tt.content); version != 0 {
				t.Fatalf("GetBlurbVersion()=%d for link-reference documentation, want 0", version)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed link-reference documentation:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestReferenceLikeTextDoesNotCloseAnOpenParagraph(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{
			name:   "definition syntax cannot interrupt paragraph",
			prefix: "paragraph\n[reference]: /url\n",
		},
		{
			name:   "same-line trailing text invalidates definition",
			prefix: "[reference]: /url \"title\" trailing\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := tt.prefix + "<agent-example>\n" +
				BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
			if !ContainsBlurb(content) {
				t.Fatal("reference-like paragraph text incorrectly enabled a marker-hiding type-7 HTML block")
			}
			if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
			}
		})
	}
}

func TestFenceLookingLineInsideLinkTitleDoesNotLeakFenceState(t *testing.T) {
	content := "[reference]: /url\n" +
		"  \"title\n" +
		"```\n" +
		"  continued\"\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(content) {
		t.Fatal("fence-looking link-title content hid the installed blurb after the definition")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestUnindentedNextLineLinkReferenceTitleProtectsMarkerDocumentation(t *testing.T) {
	// CommonMark 0.31.2 example 195 permits the destination and title on
	// successive unindented lines. The line ending is sufficient separation.
	content := "[reference]:\n" +
		"<my url>\n" +
		"\"title\n" +
		"<!-- bv-agent-instructions-v99 -->\n" +
		"example only\n" +
		"<!-- end-bv-agent-instructions -->\n" +
		"continued\"\n"
	if ContainsAnyBlurb(content) {
		t.Fatal("valid unindented next-line title exposed marker documentation")
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed a valid unindented next-line title:\n got: %q\nwant: %q", got, content)
	}
}

func TestTrailingTextInvalidatesNextLineLinkReferenceTitle(t *testing.T) {
	// CommonMark 0.31.2 example 210 leaves a would-be next-line title in the
	// following paragraph when non-whitespace trails its closing delimiter.
	content := "[reference]: /url\n" +
		"\"ordinary paragraph\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n" +
		"continued\" ok\n"
	if !ContainsBlurb(content) {
		t.Fatal("invalid next-line title consumed a following paragraph and hid installed markers")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestLinkReferenceLabelLimitCountsUnicodeCharacters(t *testing.T) {
	t.Run("999 multibyte characters remain a valid definition", func(t *testing.T) {
		content := "[" + strings.Repeat("é", 999) + "]: /url\n" +
			"<agent-example>\n" +
			"<!-- bv-agent-instructions-v99 -->\nexample only\n<!-- end-bv-agent-instructions -->\n\n"
		if ContainsAnyBlurb(content) {
			t.Fatal("valid 999-character multibyte label exposed marker documentation")
		}
		if got := RemoveBlurb(content); got != content {
			t.Fatalf("RemoveBlurb() changed documentation after a valid multibyte label:\n got: %q\nwant: %q", got, content)
		}
	})

	t.Run("1000 characters are not a definition", func(t *testing.T) {
		content := "[" + strings.Repeat("é", 1000) + "]: /url\n" +
			"<agent-example>\n" + BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
		if !ContainsBlurb(content) {
			t.Fatal("over-limit multibyte label incorrectly enabled a marker-hiding type-7 HTML block")
		}
		if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
			t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
		}
	})
}

func TestOverBudgetLinkReferenceDefinitionFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "byte budget",
			content: "[reference]: /url\n\"title\n" +
				strings.Repeat("x", maxCommonMarkReferenceDefinitionBytes+1) + "\n" +
				BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n" +
				"continued title\"\n",
		},
		{
			name: "destination depth budget",
			content: "[reference]: " +
				strings.Repeat("(", maxCommonMarkDestinationParenDepth+1) + "url" +
				strings.Repeat(")", maxCommonMarkDestinationParenDepth+1) + "\n\"title\n" +
				BlurbStartMarker + "\nexample only\n" + BlurbEndMarker + "\n" +
				"continued title\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !ContainsAnyBlurb(tt.content) {
				t.Fatal("boolean detection treated budget-limited Markdown as proof that no blurb is present")
			}
			if count, err := inspectBlurbStructure(tt.content); err == nil || count != 0 || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want an explicit analysis-limit error", count, err)
			}
			if _, err := removeBlurbsChecked(tt.content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
				t.Fatalf("removeBlurbsChecked() error=%v, want an explicit analysis-limit error", err)
			}
			if _, err := updateBlurbChecked(tt.content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
				t.Fatalf("updateBlurbChecked() error=%v, want an explicit analysis-limit error", err)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("compatibility RemoveBlurb() changed indeterminate content: got %d bytes, want exact %d bytes", len(got), len(tt.content))
			}
		})
	}
}

func TestNewlineDenseMaximumSizeContentFailsClosedBeforeLineTableAllocation(t *testing.T) {
	content := strings.Repeat("\n", maxAgentFileBytes)
	if len(content) != maxAgentFileBytes {
		t.Fatalf("fixture size=%d, want exact accepted file limit %d", len(content), maxAgentFileBytes)
	}

	if lines, err := scanMarkdownLinesChecked(content); err == nil || lines != nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("scanMarkdownLinesChecked() lines=%d err=%v, want pre-allocation line-budget refusal", len(lines), err)
	}
	if count, err := inspectBlurbStructure(content); err == nil || count != 0 || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want explicit line-budget refusal", count, err)
	}
	if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("removeBlurbsChecked() error=%v, want explicit line-budget refusal", err)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("updateBlurbChecked() error=%v, want explicit line-budget refusal", err)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatal("compatibility removal changed line-budget-limited content")
	}
}

func TestContainerDenseMaximumSizeContentFailsClosedBeforeContainerAllocation(t *testing.T) {
	content := strings.Repeat("> ", maxAgentFileBytes/2)
	if len(content) != maxAgentFileBytes {
		t.Fatalf("fixture size=%d, want exact accepted file limit %d", len(content), maxAgentFileBytes)
	}

	if lines, err := scanMarkdownLinesChecked(content); err == nil || lines != nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("scanMarkdownLinesChecked() lines=%d err=%v, want pre-allocation container-budget refusal", len(lines), err)
	}
	if count, err := inspectBlurbStructure(content); err == nil || count != 0 || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want explicit container-budget refusal", count, err)
	}
	if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("removeBlurbsChecked() error=%v, want explicit container-budget refusal", err)
	}
	if _, err := updateBlurbChecked(content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("updateBlurbChecked() error=%v, want explicit container-budget refusal", err)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatal("compatibility removal changed container-budget-limited content")
	}
}

func TestAggregateMarkdownContainerWorkBudgetFailsClosed(t *testing.T) {
	line := strings.Repeat("> ", maxMarkdownContainerDepth) + "text\n"
	lineCount := maxMarkdownContainerPrefixes/maxMarkdownContainerDepth + 1
	content := strings.Repeat(line, lineCount)

	if _, err := scanMarkdownLinesChecked(content); err == nil || !strings.Contains(err.Error(), "explicit container prefixes") {
		t.Fatalf("scanMarkdownLinesChecked() error=%v, want aggregate container-work refusal", err)
	}
	if _, err := removeBlurbsChecked(content); err == nil || !strings.Contains(err.Error(), "markdown analysis limit exceeded") {
		t.Fatalf("removeBlurbsChecked() error=%v, want aggregate container-work refusal", err)
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatal("compatibility removal changed aggregate-budget-limited content")
	}
}

func TestDuplicateVersionedBlurbRemovalUsesOneLinearRewrite(t *testing.T) {
	const blockCount = 8192
	oldBlock := "<!-- bv-agent-instructions-v1 -->\r\nold\r\n" + BlurbEndMarker + "\r\n\r\n"
	content := "# Header\r\n\r\n" + strings.Repeat(oldBlock, blockCount) + "Tail\r\n"

	removed, err := removeBlurbsChecked(content)
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Header\r\nTail\r\n"; removed != want {
		t.Fatalf("linear duplicate removal produced %d bytes, want %q", len(removed), want)
	}

	updated, err := updateBlurbChecked(content)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(updated, BlurbStartMarker); got != 1 {
		t.Fatalf("updated start-marker count=%d, want 1", got)
	}
	if !strings.HasPrefix(updated, "# Header\r\nTail\r\n") {
		t.Fatalf("linear duplicate update lost surrounding content: prefix=%q", updated[:min(len(updated), 64)])
	}
	if withoutCRLF := strings.ReplaceAll(updated, "\r\n", ""); strings.ContainsAny(withoutCRLF, "\r\n") {
		t.Fatal("linear duplicate update introduced mixed line endings")
	}
}

func TestBareEqualsLineStartsParagraph(t *testing.T) {
	content := "===\n" +
		"<agent-example>\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(content) {
		t.Fatal("bare equals text incorrectly enabled a marker-hiding type-7 HTML block")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestMarkerExamplesRespectLessObviousListContainers(t *testing.T) {
	nested := []struct {
		name    string
		content string
	}{
		{
			name: "empty bullet immediate content",
			content: "-\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "empty ordered immediate content",
			content: "1.\n" +
				"   <!-- bv-agent-instructions-v99 -->\n" +
				"   example\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "space-only empty bullet uses one-column padding",
			content: "-   \n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "tab-only empty bullet uses one-column padding",
			content: "-\t\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "five-space code starts list with one-space padding",
			content: "-     code\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "lazy continuation retains bullet",
			content: "- Marker docs\n" +
				"lazy continuation\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "nested list pops back to outer item",
			content: "- outer\n" +
				"  - nested\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "nested ordered list pops back to outer item",
			content: "1. outer\n" +
				"   1. nested\n" +
				"   <!-- bv-agent-instructions-v99 -->\n" +
				"   example\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
	}
	for _, tt := range nested {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsAnyBlurb(tt.content) {
				t.Fatal("list-nested marker documentation counted as installed")
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed list-nested documentation:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestListLikeLinesThatDoNotContainFollowingMarkers(t *testing.T) {
	topLevel := []struct {
		name    string
		content string
	}{
		{
			name: "empty item followed by blank",
			content: "-\n\n" +
				"  <!-- bv-agent-instructions-v4 -->\n" +
				"  installed\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "non-one ordered marker cannot interrupt paragraph",
			content: "paragraph\n2. documentation\n" +
				"   <!-- bv-agent-instructions-v4 -->\n" +
				"   installed\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "zero ordered marker cannot interrupt paragraph",
			content: "paragraph\n0. documentation\n" +
				"   <!-- bv-agent-instructions-v4 -->\n" +
				"   installed\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "tabbed bullet has four-column content indent",
			content: "-\ttext\n" +
				"  <!-- bv-agent-instructions-v4 -->\n" +
				"  installed\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "tabbed ordered marker has four-column content indent",
			content: "1.\ttext\n" +
				"   <!-- bv-agent-instructions-v4 -->\n" +
				"   installed\n" +
				"   <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "mixed space-tab padding reaches four columns",
			content: "- \ttext\n" +
				"  <!-- bv-agent-instructions-v4 -->\n" +
				"  installed\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
	}
	for _, tt := range topLevel {
		t.Run(tt.name, func(t *testing.T) {
			if !ContainsBlurb(tt.content) {
				t.Fatal("top-level marker block was incorrectly absorbed by list state")
			}
			if count, err := inspectBlurbStructure(tt.content); err != nil || count != 1 {
				t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
			}
		})
	}
}

func TestEmptyListMarkerCannotInterruptParagraph(t *testing.T) {
	content := "paragraph\n*\n" +
		"<agent-example>\n" +
		BlurbStartMarker + "\ninstalled\n" + BlurbEndMarker + "\n"
	if !ContainsBlurb(content) {
		t.Fatal("empty list marker incorrectly interrupted a paragraph and hid installed markers")
	}
	if count, err := inspectBlurbStructure(content); err != nil || count != 1 {
		t.Fatalf("inspectBlurbStructure() count=%d err=%v, want 1, nil", count, err)
	}
}

func TestNestedEmptyMarkerDoesNotDiscardOuterList(t *testing.T) {
	content := "- outer\n" +
		"  *\n\n" +
		"  <!-- bv-agent-instructions-v99 -->\n" +
		"  example\n" +
		"  <!-- end-bv-agent-instructions -->\n"
	if ContainsAnyBlurb(content) {
		t.Fatal("empty nested marker discarded outer list scope")
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed documentation inside outer list:\n got: %q\nwant: %q", got, content)
	}
}

func TestNestedEmptyListAfterBlankPreservesOuterScope(t *testing.T) {
	content := "- outer\n\n" +
		"  *\n\n" +
		"  <!-- bv-agent-instructions-v99 -->\n" +
		"  example\n" +
		"  <!-- end-bv-agent-instructions -->\n"
	if ContainsAnyBlurb(content) {
		t.Fatal("ending an empty nested list discarded its outer list scope")
	}
	if got := RemoveBlurb(content); got != content {
		t.Fatalf("RemoveBlurb() changed documentation inside outer list:\n got: %q\nwant: %q", got, content)
	}
}

func TestSiblingNestedListsPreserveParentScope(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "unordered siblings before versioned example",
			content: "- parent\n" +
				"  - first child\n" +
				"  - second child\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
		{
			name: "ordered siblings before legacy example",
			content: "1. parent\n" +
				"   1. first child\n" +
				"   2. second child\n" +
				"   ### Using bv as an AI sidecar\n" +
				"   --robot-insights\n" +
				"   --robot-plan\n" +
				"   bv already computes the hard parts for you.\n",
		},
		{
			name: "empty sibling ends at parent depth",
			content: "- parent\n" +
				"  - first child\n" +
				"  -\n\n" +
				"  <!-- bv-agent-instructions-v99 -->\n" +
				"  example\n" +
				"  <!-- end-bv-agent-instructions -->\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsAnyBlurb(tt.content) {
				t.Fatal("parent-scoped documentation after nested siblings counted as installed")
			}
			if got := RemoveLegacyBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveLegacyBlurb() changed parent-scoped documentation:\n got: %q\nwant: %q", got, tt.content)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed parent-scoped documentation:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}

func TestLegacyPatternsInsideTopLevelIndentedCodeArePreserved(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "insights pattern",
			content: "### Using bv as an AI sidecar\n\n" +
				"    --robot-insights\n" +
				"--robot-plan\n" +
				"bv already computes the hard parts for you.\n",
		},
		{
			name: "plan pattern",
			content: "### Using bv as an AI sidecar\n\n" +
				"--robot-insights\n\n" +
				"    --robot-plan\n" +
				"bv already computes the hard parts for you.\n",
		},
		{
			name: "differentiator pattern",
			content: "### Using bv as an AI sidecar\n\n" +
				"--robot-insights\n" +
				"--robot-plan\n\n" +
				"    bv already computes the hard parts for you.\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ContainsLegacyBlurb(tt.content) {
				t.Fatal("legacy pattern inside a top-level indented code block counted as installed")
			}
			if got := RemoveLegacyBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveLegacyBlurb() changed indented-code documentation:\n got: %q\nwant: %q", got, tt.content)
			}
			if got := RemoveBlurb(tt.content); got != tt.content {
				t.Fatalf("RemoveBlurb() changed indented-code documentation:\n got: %q\nwant: %q", got, tt.content)
			}
		})
	}
}
