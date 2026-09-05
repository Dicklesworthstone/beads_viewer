package ui

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	ansiparser "github.com/charmbracelet/x/ansi/parser"
	"github.com/charmbracelet/x/cellbuf"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/reflow/padding"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestMarkdownPaddingSingleRuneWidths(t *testing.T) {
	// Reflow already iterates decoded runes. Compare its old single-rune
	// string operation with the direct operation, without changing globals.
	for _, eastAsian := range []bool{false, true} {
		for _, strictEmoji := range []bool{false, true} {
			condition := &runewidth.Condition{EastAsianWidth: eastAsian, StrictEmojiNeutral: strictEmoji}
			for r := rune(0); r <= utf8.MaxRune; r++ {
				if !utf8.ValidRune(r) {
					continue
				}
				if got, want := condition.RuneWidth(r), condition.StringWidth(string(r)); got != want {
					t.Fatalf("%U eastAsian=%v strictEmoji=%v: direct width %d, single-rune string width %d", r, eastAsian, strictEmoji, got, want)
				}
			}
		}
	}
}

func TestMarkdownPaddingAllocationBound(t *testing.T) {
	var output bytes.Buffer
	writer := padding.NewWriterPipe(&output, 4, nil)
	if n, err := writer.Write([]byte("e\u0301界\n")); err != nil || n != len("e\u0301界\n") {
		t.Fatalf("padding write: bytes=%d error=%v", n, err)
	}
	if got, want := output.String(), "e\u0301界 \n"; got != want {
		t.Fatalf("padded Unicode content: got %q want %q", got, want)
	}

	input := []byte(strings.Repeat("\x1b[31m日本語 e\u0301 test\x1b[0m\n", 2048))
	allocations := testing.AllocsPerRun(3, func() {
		w := padding.NewWriterPipe(io.Discard, 40, nil)
		if n, err := w.Write(input); err != nil || n != len(input) {
			t.Fatalf("padding write: bytes=%d error=%v", n, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	})
	// The writer needs a few buffers; it must not allocate a temporary
	// single-rune string for each visible character. No wall-clock gate.
	if allocations > 16 {
		t.Fatalf("padding %d bytes allocated %.0f objects; limit 16", len(input), allocations)
	}
	t.Logf("input_bytes=%d allocations=%.0f limit=16", len(input), allocations)
}

// This controlled token source exercises the coalescer's public stream contract.
// Real registered lexers are exercised separately below; it is not live proof.
type coalesceTokenSource struct {
	chroma.Lexer
	tokens []chroma.Token
	err    error
}

func (l coalesceTokenSource) Tokenise(_ *chroma.TokeniseOptions, _ string) (chroma.Iterator, error) {
	if l.err != nil {
		return nil, l.err
	}
	return chroma.Literator(l.tokens...), nil
}

func originalCoalesce(tokens []chroma.Token) []chroma.Token {
	var result []chroma.Token
	var previous chroma.Token
	for _, token := range tokens {
		if token == chroma.EOF {
			break
		}
		if token.Value == "" {
			continue
		}
		if previous == chroma.EOF {
			previous = token
		} else if previous.Type == token.Type && len(previous.Value) < 8192 {
			previous.Value += token.Value
		} else {
			result = append(result, previous)
			previous = token
		}
	}
	if previous != chroma.EOF {
		result = append(result, previous)
	}
	return result
}

func collectCoalescedTokens(t *testing.T, lexer chroma.Lexer, source string) []chroma.Token {
	t.Helper()
	it, err := chroma.Coalesce(lexer).Tokenise(nil, source)
	if err != nil {
		t.Fatal(err)
	}
	var result []chroma.Token
	for token := it(); token != chroma.EOF; token = it() {
		result = append(result, token)
	}
	for i := 0; i < 3; i++ {
		if got := it(); got != chroma.EOF {
			t.Fatalf("iterator resumed after EOF: %#v", got)
		}
	}
	return result
}

func TestMarkdownCoalescerTokenBoundaries(t *testing.T) {
	textToken := func(s string) chroma.Token { return chroma.Token{Type: chroma.Text, Value: s} }
	keyword := chroma.Token{Type: chroma.Keyword, Value: "type"}
	for _, tc := range []struct {
		name string
		in   []chroma.Token
		want []chroma.Token
	}{
		{"empty", nil, nil},
		{"typed empty", []chroma.Token{{Type: chroma.Error}}, nil},
		{"skip empty between runs", []chroma.Token{textToken("a"), {Type: chroma.Keyword}, textToken("b")}, []chroma.Token{textToken("ab")}},
		{"type changes", []chroma.Token{textToken("a"), textToken("b"), keyword, textToken("c"), textToken("d")}, []chroma.Token{textToken("ab"), keyword, textToken("cd")}},
		{"8191 to exact boundary", []chroma.Token{textToken(strings.Repeat("a", 8191)), textToken("b"), textToken("c")}, []chroma.Token{textToken(strings.Repeat("a", 8191) + "b"), textToken("c")}},
		{"8192 is already full", []chroma.Token{textToken(strings.Repeat("a", 8192)), textToken("b")}, []chroma.Token{textToken(strings.Repeat("a", 8192)), textToken("b")}},
		{"8193 is already full", []chroma.Token{textToken(strings.Repeat("a", 8193)), textToken("b")}, []chroma.Token{textToken(strings.Repeat("a", 8193)), textToken("b")}},
		{"multibyte crosses boundary", []chroma.Token{textToken(strings.Repeat("a", 8191)), textToken("日"), textToken("b")}, []chroma.Token{textToken(strings.Repeat("a", 8191) + "日"), textToken("b")}},
		{"large incoming token retained whole", []chroma.Token{textToken("a"), textToken(strings.Repeat("b", 16384)), textToken("c")}, []chroma.Token{textToken("a" + strings.Repeat("b", 16384)), textToken("c")}},
		{"large first token retained whole", []chroma.Token{textToken(strings.Repeat("a", 16384)), textToken("b")}, []chroma.Token{textToken(strings.Repeat("a", 16384)), textToken("b")}},
		{"NUL invalid UTF8 preserved", []chroma.Token{textToken("\x00\xff"), textToken("e\u0301🚀")}, []chroma.Token{textToken("\x00\xffe\u0301🚀")}},
		{"final EOF flushes buffered group", []chroma.Token{textToken("a"), textToken("b"), chroma.EOF}, []chroma.Token{textToken("ab")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectCoalescedTokens(t, coalesceTokenSource{tokens: tc.in}, "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("complete token stream differs: got=%#v want=%#v", got, tc.want)
			}
			if !reflect.DeepEqual(got, originalCoalesce(tc.in)) {
				t.Fatal("hand-expected stream also differs from retained original rule")
			}
		})
	}
}

func TestMarkdownCoalescerEmittedAliasesAndErrors(t *testing.T) {
	sentinel := errors.New("underlying lexer error")
	it, err := chroma.Coalesce(coalesceTokenSource{err: sentinel}).Tokenise(nil, "anything")
	if it != nil || err != sentinel {
		t.Fatalf("underlying error identity changed: iterator=%v err=%v", it != nil, err)
	}
	var input []chroma.Token
	var want []chroma.Token
	for group := 0; group < 32; group++ {
		kind := chroma.Text
		if group%2 != 0 {
			kind = chroma.Keyword
		}
		value := strings.Repeat(fmt.Sprintf("%02d日", group), 300)
		input = append(input, chroma.Token{Type: kind, Value: value}, chroma.Token{Type: kind, Value: value})
		want = append(want, chroma.Token{Type: kind, Value: value + value})
	}
	// Retain every emitted string through all subsequent groups and repeated EOF.
	got := collectCoalescedTokens(t, coalesceTokenSource{tokens: input}, "")
	if !reflect.DeepEqual(got, want) {
		t.Fatal("emitted string alias mutated while later groups were assembled")
	}
}

func TestMarkdownCoalescerRealLexerStreams(t *testing.T) {
	for _, tc := range []struct{ language, source string }{
		{"text", strings.Repeat("node -> parent 日本語 e\u0301\n", 900)},
		{"go", "package main\n// 日本語\nfunc main() { println(\"hello\") }\n"},
		{"json", "{\"title\":\"🚀\",\"ids\":[1,2,3],\"empty\":null}\n"},
		{"python", "# 日本語\ndef twice(value):\n    return value * 2\n"},
		{"bash", "#!/bin/sh\nprintf '%s\\n' \"$HOME\" # literal source only\n"},
		{"markdown", "# title\n\n- **bold** and [link](https://example.com)\n"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			lexer := lexers.Get(tc.language)
			if lexer == nil {
				t.Fatalf("required real lexer %q unavailable", tc.language)
			}
			it, err := lexer.Tokenise(nil, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			var raw []chroma.Token
			for token := it(); token != chroma.EOF; token = it() {
				raw = append(raw, token)
			}
			got := collectCoalescedTokens(t, lexer, tc.source)
			want := originalCoalesce(raw)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("real lexer token boundaries/types/bytes differ: got=%#v want=%#v", got, want)
			}
			var content strings.Builder
			for _, token := range got {
				content.WriteString(token.Value)
			}
			if content.String() != tc.source {
				t.Fatal("real lexer round trip lost input bytes")
			}
		})
	}
}

func TestMarkdownCoalescerRealLexerAllocationBound(t *testing.T) {
	lexer := lexers.Get("text")
	if lexer == nil {
		t.Fatal("required real plaintext lexer unavailable")
	}
	source := strings.Repeat("a\n", 4096)
	// Compile/initialize the real regex lexer before observing extraction cost.
	want := []chroma.Token{{Type: chroma.Text, Value: source}}
	if got := collectCoalescedTokens(t, lexer, source); !reflect.DeepEqual(got, want) {
		t.Fatal("real lexer baseline has unexpected complete token stream")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := collectCoalescedTokens(t, lexer, source)
	runtime.ReadMemStats(&after)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("allocation bound cannot pass after dropping or splitting token content")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	// The real lexer itself allocates. This bound leaves room for that work while
	// rejecting the original ~32MiB of growing-prefix copies for 8192 tokens.
	const bound = uint64(16 << 20)
	t.Logf("real plaintext 8192-byte input: %d allocated bytes, %d allocations", allocated, after.Mallocs-before.Mallocs)
	if allocated > bound {
		t.Fatalf("coalescing repeated real plaintext tokens allocated %d bytes, limit %d", allocated, bound)
	}
}

func TestMarkdownCodeBlockLogicalBytes(t *testing.T) {
	type block struct{ code, language string }
	for _, tc := range []struct {
		name, markdown string
		want           []block
	}{
		{"plain fenced", "```text\nfirst\nsecond\n```\n", []block{{"first\nsecond\n", "text"}}},
		{"Unicode fenced", "```go extra\n日本語 🚀 café e\u0301\n```\n", []block{{"日本語 🚀 café e\u0301\n", "go"}}},
		{"CRLF fenced", "```text\r\nfirst\r\nsecond\r\n```\r\n", []block{{"first\r\nsecond\r\n", "text"}}},
		{"empty fenced", "~~~\n~~~\n", []block{{"", ""}}},
		{"blank lines fenced", "```text\n\n \n```\n", []block{{"\n \n", "text"}}},
		{"EOF fenced", "```text\nlast", []block{{"last\n", "text"}}},
		{"plain indented", "    first\n    second\n", []block{{"first\nsecond\n", ""}}},
		{"Unicode indented", "    日本語 🚀 café e\u0301\n", []block{{"日本語 🚀 café e\u0301\n", ""}}},
		{"CRLF indented", "    first\r\n    second\r\n", []block{{"first\r\nsecond\r\n", ""}}},
		{"EOF indented", "    last", []block{{"last\n", ""}}},
		{"partial tab indentation", "   ```text\n\tvalue\n   ```\n", []block{{" value\n", "text"}}},
		{"quoted fenced", "> ```text\n> quote\n> ```\n", []block{{"quote\n", "text"}}},
		{"quoted indented", ">     quote\n", []block{{"quote\n", ""}}},
		{"list fenced", "- item\n\n  ```go\n  nested\n  ```\n", []block{{"nested\n", "go"}}},
		{"list indented", "- item\n\n      nested\n      next\n", []block{{"nested\nnext\n", ""}}},
		{"multiple blocks", "```go\none\n```\n\nparagraph\n\n    two\n\n~~~text\nthree\n~~~\n", []block{{"one\n", "go"}, {"two\n", ""}, {"three\n", "text"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte(tc.markdown)
			document := goldmark.New().Parser().Parse(text.NewReader(source))
			renderer := glamouransi.NewRenderer(glamouransi.Options{})
			var got []block
			err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering || (node.Kind() != ast.KindCodeBlock && node.Kind() != ast.KindFencedCodeBlock) {
					return ast.WalkContinue, nil
				}
				element := renderer.NewElement(node, source)
				code, ok := element.Renderer.(*glamouransi.CodeBlockElement)
				if !ok || element.Entering != "\n" {
					t.Fatalf("code block renderer or entry delimiter changed: %+v", element)
				}
				got = append(got, block{code.Code, code.Language})
				return ast.WalkContinue, nil
			})
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("parsed blocks=%+v, want %+v; error=%v", got, tc.want, err)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("block%d bytes/language=%q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestMarkdownCodeBlockSegmentPaddingAndEOF(t *testing.T) {
	// Exercise logical segments directly: source slicing alone loses padding
	// and the synthetic newline required for an unterminated final code line.
	for _, fenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			source := []byte("first\nlast")
			var node ast.Node = ast.NewCodeBlock()
			if fenced {
				node = ast.NewFencedCodeBlock(nil)
			}
			renderer := glamouransi.NewRenderer(glamouransi.Options{})
			empty := renderer.NewElement(node, source).Renderer.(*glamouransi.CodeBlockElement)
			if empty.Code != "" || empty.Language != "" {
				t.Fatalf("empty block gained content: %+v", empty)
			}
			node.Lines().Append(text.NewSegmentPadding(0, 6, 2))
			node.Lines().Append(text.Segment{Start: 6, Stop: len(source), Padding: 1, ForceNewline: true})
			got := renderer.NewElement(node, source).Renderer.(*glamouransi.CodeBlockElement)
			if got.Code != "  first\n last\n" || got.Language != "" {
				t.Fatalf("logical segment bytes lost: code=%q language=%q", got.Code, got.Language)
			}
		})
	}
}

func TestMarkdownCodeBlockAllocationBound(t *testing.T) {
	// The old growing-string loop copied over 128MiB to assemble this 128KiB
	// block. Allow 16 times its logical size plus 64KiB for allocator overhead;
	// this is a byte-allocation regression bound, never a wall-clock deadline.
	const lines, lineBytes = 2048, 64
	code := strings.Repeat(strings.Repeat("x", lineBytes-1)+"\n", lines)
	for _, fenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			source := []byte(code)
			var node ast.Node = ast.NewCodeBlock()
			if fenced {
				node = ast.NewFencedCodeBlock(nil)
			}
			for i := 0; i < lines; i++ {
				node.Lines().Append(text.NewSegment(i*lineBytes, (i+1)*lineBytes))
			}
			renderer := glamouransi.NewRenderer(glamouransi.Options{})
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got := renderer.NewElement(node, source).Renderer.(*glamouransi.CodeBlockElement)
			runtime.ReadMemStats(&after)
			if got.Code != code || got.Language != "" {
				t.Fatal("large block content changed")
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			limit := uint64(16*len(code) + 64*1024)
			t.Logf("logical_bytes=%d allocated_bytes=%d limit_bytes=%d", len(code), allocated, limit)
			if allocated > limit {
				t.Fatalf("code-block assembly allocated %d bytes for %d logical bytes, limit%d", allocated, len(code), limit)
			}
		})
	}
}

func TestMarkdownCodeBlockOutputDistinguishesContent(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		t.Run(fmt.Sprintf("fenced=%v", fenced), func(t *testing.T) {
			markdown := func(code string) string {
				if fenced {
					return "```text\n" + code + "```\n"
				}
				return "    " + strings.ReplaceAll(strings.TrimSuffix(code, "\n"), "\n", "\n    ") + "\n"
			}
			renderer := NewMarkdownRendererWithTheme(80, DefaultTheme(lipgloss.DefaultRenderer()))
			original, err := renderer.Render(markdown("first\nsecond\n"))
			if err != nil {
				t.Fatal(err)
			}
			for _, changed := range []string{"changed\nsecond\n", "firstsecond\n"} {
				got, err := renderer.Render(markdown(changed))
				if err != nil || got == original || xansi.Strip(got) == xansi.Strip(original) {
					t.Fatalf("changed text/newline was lost: changed=%q error=%v", changed, err)
				}
			}
		})
	}
}

func TestNewMarkdownRenderer(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	if mr == nil {
		t.Fatal("NewMarkdownRenderer returned nil")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
	if mr.useTheme {
		t.Error("expected useTheme to be false for NewMarkdownRenderer")
	}
	if mr.theme != nil {
		t.Error("expected theme to be nil for NewMarkdownRenderer")
	}
}

func TestNewMarkdownRendererWithTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)
	if mr == nil {
		t.Fatal("NewMarkdownRendererWithTheme returned nil")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("expected useTheme to be true for NewMarkdownRendererWithTheme")
	}
	if mr.theme == nil {
		t.Error("expected theme to be stored")
	}
}

func TestMarkdownRenderer_Render(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	result, err := mr.Render("# Hello\n\nWorld")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	// Should contain "Hello" somewhere in the rendered output
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected result to contain 'Hello', got: %s", result)
	}
}

// Compare the complete layout and the style of every emitted rune. Using
// parser callbacks keeps combining marks with the style at their emission;
// cellbuf.SetContent defers isolated zero-width runes to a later printable cell.
// This is an ANSI-output oracle, not proof of native terminal painting.
func markdownVisibleDifference(want, got string) error {
	if want == got {
		return nil
	}
	if xansi.Strip(want) != xansi.Strip(got) {
		return fmt.Errorf("plain text differs")
	}
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	if len(wantLines) != len(gotLines) {
		return fmt.Errorf("line count differs: %d != %d", len(wantLines), len(gotLines))
	}
	for i, line := range wantLines {
		w, g := xansi.StringWidth(line), xansi.StringWidth(gotLines[i])
		if w != g {
			return fmt.Errorf("line %d width differs: %d != %d", i, w, g)
		}
	}
	wantRunes, err := markdownStyledRunes(want)
	if err != nil {
		return err
	}
	gotRunes, err := markdownStyledRunes(got)
	if err != nil {
		return err
	}
	if len(wantRunes) != len(gotRunes) {
		return fmt.Errorf("parsed rune counts differ: %d != %d", len(wantRunes), len(gotRunes))
	}
	for i, a := range wantRunes {
		b := gotRunes[i]
		if a.Equal(&b) {
			continue
		}
		// Only undecorated ASCII spaces may lose foreground. In
		// particular, underline makes even a space's foreground visible.
		if a.Rune == ' ' && b.Rune == ' ' && a.Width == 1 && b.Width == 1 && a.Link.Empty() && b.Link.Empty() {
			plainA, plainB := a.Style, b.Style
			plainA.Fg, plainB.Fg = nil, nil
			if plainA.Empty() && plainB.Empty() {
				continue
			}
		}
		return fmt.Errorf("styled rune %d differs: %#v != %#v", i, a, b)
	}
	return nil
}

func markdownStyledRunes(output string) ([]cellbuf.Cell, error) {
	var cells []cellbuf.Cell
	var printed strings.Builder
	var style cellbuf.Style
	var link cellbuf.Link
	var parseErr error
	reject := func(kind string) { parseErr = fmt.Errorf("unsupported %s in differing output", kind) }
	printRune := func(r rune) {
		printed.WriteRune(r)
		cells = append(cells, cellbuf.Cell{Rune: r, Width: xansi.StringWidth(string(r)), Style: style, Link: link})
	}
	p := xansi.NewParser()
	p.SetDataSize(len(output))
	p.SetHandler(xansi.Handler{
		Print: printRune,
		Execute: func(b byte) {
			if b != '\n' && b != '\r' && b != '\t' {
				reject("control")
			}
			printRune(rune(b))
		},
		HandleCsi: func(cmd xansi.Cmd, params xansi.Params) {
			if cmd != 'm' {
				reject("CSI")
				return
			}
			// ReadStyle intentionally ignores unsupported SGR. Refuse those
			// inputs here instead of silently blessing unknown attributes.
			for i := 0; i < len(params); i++ {
				v, more, _ := params.Param(i, 0)
				switch {
				case v == 38 || v == 48 || v == 58:
					var c color.Color
					n := cellbuf.ReadStyleColor(params[i:], &c)
					if n == 0 || c == nil {
						reject("SGR color")
						return
					}
					i += n - 1
				case v == 4 && more:
					u, continued, ok := params.Param(i+1, 0)
					if !ok || continued || u < 0 || u > 5 {
						reject("SGR underline")
						return
					}
					i++
				case !more && (v >= 0 && v <= 9 || v >= 22 && v <= 25 || v >= 27 && v <= 37 || v == 39 || v >= 40 && v <= 47 || v == 49 || v == 59 || v >= 90 && v <= 97 || v >= 100 && v <= 107):
				default:
					reject("SGR")
					return
				}
			}
			cellbuf.ReadStyle(params, &style)
		},
		HandleOsc: func(cmd int, data []byte) {
			if cmd != 8 || strings.Count(string(data), ";") != 2 {
				reject("OSC")
				return
			}
			cellbuf.ReadLink(data, &link)
		},
		HandleEsc: func(xansi.Cmd) { reject("ESC") },
		HandleDcs: func(xansi.Cmd, xansi.Params, []byte) { reject("DCS") },
		HandlePm:  func([]byte) { reject("PM") },
		HandleApc: func([]byte) { reject("APC") },
		HandleSos: func([]byte) { reject("SOS") },
	})
	for i := range len(output) {
		p.Advance(output[i])
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if p.State() != ansiparser.GroundState || printed.String() != xansi.Strip(output) {
		return nil, fmt.Errorf("ANSI parser did not preserve complete stripped output")
	}
	// A sentinel compares final style and hyperlink state strictly, including
	// a hyperlink opened after the final printable rune.
	return append(cells, cellbuf.Cell{Style: style, Link: link}), nil
}

func TestMarkdownVisibleDifferenceRejectsVisibleChanges(t *testing.T) {
	for _, tc := range []struct {
		name, want, got string
		equal           bool
	}{
		{"identical", "日本語 é", "日本語 é", true},
		{"plain-space-foreground", "\x1b[31m \x1b[0m", " ", true},
		{"underlined-space-foreground", "\x1b[4;31m \x1b[0m", "\x1b[4;32m \x1b[0m", false},
		{"space-background", "\x1b[41m \x1b[0m", "\x1b[42m \x1b[0m", false},
		{"nonblank-foreground", "\x1b[31mX\x1b[0m", "\x1b[32mX\x1b[0m", false},
		{"inverse-space-foreground", "\x1b[7;31m \x1b[0m", "\x1b[7;32m \x1b[0m", false},
		{"struck-space-foreground", "\x1b[9;31m \x1b[0m", "\x1b[9;32m \x1b[0m", false},
		{"blinking-space-foreground", "\x1b[5;31m \x1b[0m", "\x1b[5;32m \x1b[0m", false},
		{"linked-space-foreground", "\x1b]8;;https://example.com\x1b\\\x1b[31m \x1b[0m\x1b]8;;\x1b\\", "\x1b]8;;https://example.com\x1b\\\x1b[32m \x1b[0m\x1b]8;;\x1b\\", false},
		{"link-target", "\x1b]8;;https://example.com/a\x1b\\X\x1b]8;;\x1b\\", "\x1b]8;;https://example.com/b\x1b\\X\x1b]8;;\x1b\\", false},
		{"non-ascii-space", "\x1b[31m\u00a0\x1b[0m", "\u00a0", false},
		{"combining-space", "\x1b[31m \u0301\x1b[0m", " \u0301", false},
		{"ending-style", "X\x1b[31m", "X", false},
		{"ending-link", "X\x1b]8;;https://example.com\x1b\\", "X", false},
		{"unsupported-sgr", "\x1b[98mX\x1b[0m", "X", false},
		{"unsupported-csi", "\x1b[2KX", "X", false},
		{"line-layout", "A\nB", "AB\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := markdownVisibleDifference(tc.want, tc.got)
			if (err == nil) != tc.equal {
				t.Fatalf("visible equality=%v, want %v: %v", err == nil, tc.equal, err)
			}
		})
	}
}

func TestMarkdownRenderer_PaddingPreservesVisibleOutput(t *testing.T) {
	previousDark := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(previousDark) })
	// The assignee marker and fenced dependency labels are ordinary generated
	// detail content; they must not be mistaken for email or reference links.
	body := "# Selected 日本語 🚀 café é\n\n| ID | Assignee |\n|---|---|\n| PERF-1 | @ |\n\n" +
		strings.Repeat("Paragraph **strong _nested_** and ~~strike~~ with `code`.\n\n- First item\n- Second item\n\n", 8) +
		"```text\nPERF-1 [root]\n└── PERF-2 [blocks]\n```\n"
	for _, dark := range []bool{false, true} {
		lipgloss.SetHasDarkBackground(dark)
		for _, width := range []int{17, 72} {
			t.Run(fmt.Sprintf("dark=%v/width=%d", dark, width), func(t *testing.T) {
				theme := DefaultTheme(lipgloss.DefaultRenderer())
				mr := NewMarkdownRendererWithTheme(width, theme)
				check := func() {
					t.Helper()
					want, err := mr.renderer.Render(body)
					if err != nil {
						t.Fatal(err)
					}
					got, err := mr.Render(body)
					if err != nil {
						t.Fatal(err)
					}
					if err := markdownVisibleDifference(want, got); err != nil {
						t.Fatal(err)
					}
					if len(got) >= len(want) {
						t.Fatalf("eligible document kept redundant ANSI: got %d bytes, original %d", len(got), len(want))
					}
					if allocations := testing.AllocsPerRun(3, func() {
						repeated, err := mr.Render(body)
						if err != nil || repeated != got {
							t.Fatalf("exact memo changed output: %v", err)
						}
					}); allocations != 0 {
						t.Fatalf("same custom-theme render allocated %.0f objects", allocations)
					}
				}
				check()
				mr.SetWidth(width + 11)
				check()
				theme.Primary = lipgloss.AdaptiveColor{Light: "#654321", Dark: "#123456"}
				theme.Feature = lipgloss.AdaptiveColor{Light: "#337755", Dark: "#22aa66"}
				mr.SetWidthWithTheme(mr.width, theme)
				check()
			})
		}
	}
}

func TestMarkdownRenderer_PaddingFallbackKeepsExactBytes(t *testing.T) {
	previousDark := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(previousDark) })
	for _, dark := range []bool{false, true} {
		lipgloss.SetHasDarkBackground(dark)
		for _, tc := range []struct{ name, source string }{
			{"wrapped-link-image", "[link   words](https://example.com/path \"title\") ![alt   words](image.png)\n"},
			{"nested-link", "**[link   words](https://example.com/path)**\n"},
			{"reference-link", "[link   words][ref]\n\n[ref]: https://example.com/path\n"},
			{"url-email", "https://example.com/path and person@example.com\n"},
			{"root-html", "<div>HTML 日本語 text</div>\n"},
			{"root-definition", "Term 日本語\n: Definition **strong** text\n"},
			{"nested-definition", "> Term\n> : Definition text\n"},
			{"raw-control", "Before \x1b[4;41m   \x1b[0m after\n"},
			{"raw-link", "Before \x1b]8;;https://example.com\x1b\\ linked  \n"},
			{"c1-control", "Before \u009b4m   \u009b0m after\n"},
			{"encoded-control", "Before &#x1b;[4m   &#x1b;[24m after\n"},
			{"named-entity", "**Before &NewLine; after**\n"},
			{"builtin", "# Builtin\n\nPlain **strong** text\n"},
		} {
			t.Run(fmt.Sprintf("%s/dark=%v", tc.name, dark), func(t *testing.T) {
				mr := NewMarkdownRendererWithTheme(17, DefaultTheme(lipgloss.DefaultRenderer()))
				if tc.name == "builtin" {
					mr = NewMarkdownRenderer(17)
				}
				want, err := mr.renderer.Render(tc.source)
				if err != nil {
					t.Fatal(err)
				}
				got, err := mr.Render(tc.source)
				if err != nil || got != want {
					t.Fatalf("fallback changed authoritative ANSI bytes: error=%v", err)
				}
			})
		}
	}
}

func TestMarkdownRenderer_RepeatedExactContentDoesNotRenderAgain(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	content := "# Selected issue\n\n" + strings.Repeat("日本語 🚀 café **Markdown** and dependency details.\n\n", 30)
	want, err := mr.Render(content)
	if err != nil {
		t.Fatal(err)
	}
	allocations := testing.AllocsPerRun(3, func() {
		got, err := mr.Render(content)
		if err != nil || got != want {
			t.Fatalf("identical detail changed: error=%v", err)
		}
	})
	if allocations != 0 {
		t.Fatalf("unchanged selected detail allocated %.0f objects; want zero repeated rendering work", allocations)
	}
}

func TestMarkdownRenderer_ReusedOutputMatchesFreshAfterChanges(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)
	base := "# Original selected issue\n\n" + strings.Repeat("日本語 café Markdown text for wrapping. ", 10)
	if _, err := mr.Render(base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		width         int
		changeTheme   bool
	}{
		{"changed-selected-body", base + "\n\nNew dependency and changed body", 80, false},
		{"new-selection", "# Different issue\n\nCompletely different description", 80, false},
		{"narrower-width", base, 37, false},
		{"same-width-new-theme", base, 37, true},
		{"return-to-previous-body", base, 80, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.changeTheme {
				theme.Primary = lipgloss.AdaptiveColor{Light: "#ff0000", Dark: "#00ff00"}
				mr.SetWidthWithTheme(tc.width, theme)
			} else {
				mr.SetWidth(tc.width)
			}
			fresh := NewMarkdownRendererWithTheme(tc.width, theme)
			want, err := fresh.Render(tc.content)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mr.Render(tc.content)
			if err != nil || got != want {
				t.Fatalf("output differs from a fresh renderer after %s: error=%v", tc.name, err)
			}
		})
	}
}

func TestMarkdownRenderer_LastOutputMemoryIsBounded(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom=%v", custom), func(t *testing.T) {
			mr := NewMarkdownRenderer(80)
			if custom {
				mr = NewMarkdownRendererWithTheme(80, DefaultTheme(lipgloss.DefaultRenderer()))
			}
			if _, err := mr.Render("# Small selected issue"); err != nil {
				t.Fatal(err)
			}
			if mr.lastRenderer == nil {
				t.Fatal("small exact output was not retained")
			}
			large := strings.Repeat("large description ", maxRememberedMarkdownBytes/10)
			if _, err := mr.Render(large); err != nil {
				t.Fatal(err)
			}
			if mr.lastRenderer != nil || len(mr.lastMarkdown)+len(mr.lastRendered) != 0 {
				t.Fatal("oversized detail retained additional cache memory")
			}
		})
	}
}

func TestSelectedDependencyTreeUsesPlainTextWithoutChangingOutput(t *testing.T) {
	for _, kind := range []string{"realistic", "unicode"} {
		t.Run(kind, func(t *testing.T) {
			issues, err := testutil.PerformanceIssues(kind, 64, 20260904)
			if err != nil {
				t.Fatal(err)
			}
			m := settledPerformanceModel(t, issues)
			for i, value := range m.list.Items() {
				if item, ok := value.(IssueItem); ok && len(item.Issue.Dependencies) > 0 {
					m.list.Select(i)
					break
				}
			}
			m.updateViewportContent()
			markdown, rendered := m.renderer.lastMarkdown, m.renderer.lastRendered
			if !strings.Contains(markdown, "```text\n") {
				t.Fatal("generated dependency tree still asks the highlighter to infer a language")
			}
			before := strings.Replace(markdown, "```text\n", "```\n", 1)
			fresh := NewMarkdownRendererWithTheme(m.renderer.width, *m.renderer.theme)
			want, err := fresh.Render(before)
			if err != nil || rendered != want {
				t.Fatalf("explicit plain-text dependency tree changed terminal output: error=%v", err)
			}
		})
	}
}

func TestMarkdownRenderer_RenderNilRenderer(t *testing.T) {
	mr := &MarkdownRenderer{
		renderer: nil,
		width:    80,
	}
	result, err := mr.Render("# Test")
	if err != nil {
		t.Fatalf("Render with nil renderer should not error: %v", err)
	}
	if result != "# Test" {
		t.Errorf("expected raw markdown when renderer is nil, got: %s", result)
	}
}

func TestMarkdownRenderer_SetWidth(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	originalRenderer := mr.renderer

	// Same width should not recreate renderer
	mr.SetWidth(80)
	if mr.renderer != originalRenderer {
		t.Error("SetWidth with same width should not recreate renderer")
	}

	// Invalid width should not change anything
	mr.SetWidth(0)
	if mr.width != 80 {
		t.Error("SetWidth with 0 should not change width")
	}
	mr.SetWidth(-1)
	if mr.width != 80 {
		t.Error("SetWidth with negative should not change width")
	}

	// Different width should update
	mr.SetWidth(100)
	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
}

func TestMarkdownRenderer_SetWidthPreservesTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)

	if !mr.useTheme {
		t.Fatal("expected useTheme to be true")
	}

	// SetWidth should preserve theme
	mr.SetWidth(100)
	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("SetWidth should preserve useTheme flag")
	}
	if mr.theme == nil {
		t.Error("SetWidth should preserve theme")
	}
}

func TestMarkdownRenderer_SetWidthWithTheme(t *testing.T) {
	mr := NewMarkdownRenderer(80)

	if mr.useTheme {
		t.Fatal("expected useTheme to be false initially")
	}

	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr.SetWidthWithTheme(100, theme)

	if mr.width != 100 {
		t.Errorf("expected width 100, got %d", mr.width)
	}
	if !mr.useTheme {
		t.Error("SetWidthWithTheme should set useTheme to true")
	}
	if mr.theme == nil {
		t.Error("SetWidthWithTheme should store theme")
	}
}

func TestMarkdownRenderer_SetWidthWithThemeSameWidth(t *testing.T) {
	// SetWidthWithTheme should allow updating theme even with same width
	theme := DefaultTheme(lipgloss.DefaultRenderer())
	mr := NewMarkdownRendererWithTheme(80, theme)

	originalRenderer := mr.renderer

	// Same width but (conceptually) different theme should recreate renderer
	mr.SetWidthWithTheme(80, theme)

	// Renderer should be recreated (different instance)
	if mr.renderer == originalRenderer {
		t.Error("SetWidthWithTheme with same width should still recreate renderer")
	}
	if mr.width != 80 {
		t.Errorf("expected width 80, got %d", mr.width)
	}
}

func TestMarkdownRenderer_SetWidthWithThemeInvalidWidth(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	originalRenderer := mr.renderer

	mr.SetWidthWithTheme(0, DefaultTheme(lipgloss.DefaultRenderer()))
	if mr.width != 80 {
		t.Error("SetWidthWithTheme with width 0 should not change width")
	}
	if mr.renderer != originalRenderer {
		t.Error("SetWidthWithTheme with width 0 should not change renderer")
	}

	mr.SetWidthWithTheme(-1, DefaultTheme(lipgloss.DefaultRenderer()))
	if mr.width != 80 {
		t.Error("SetWidthWithTheme with negative width should not change width")
	}
}

func TestMarkdownRenderer_IsDarkMode(t *testing.T) {
	mr := NewMarkdownRenderer(80)
	// Just verify it returns a boolean without panicking
	_ = mr.IsDarkMode()
}

func TestExtractHex(t *testing.T) {
	ac := lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#000000"}

	lightHex := extractHex(ac, false)
	if lightHex != "#ffffff" {
		t.Errorf("expected #ffffff for light mode, got %s", lightHex)
	}

	darkHex := extractHex(ac, true)
	if darkHex != "#000000" {
		t.Errorf("expected #000000 for dark mode, got %s", darkHex)
	}
}

func TestBuildStyleFromTheme(t *testing.T) {
	theme := DefaultTheme(lipgloss.DefaultRenderer())

	// Test dark mode
	darkConfig := buildStyleFromTheme(theme, true)
	if darkConfig.Document.Color == nil {
		t.Error("expected Document.Color to be set")
	}
	if *darkConfig.Document.Color != "#f8f8f2" {
		t.Errorf("expected dark mode doc color #f8f8f2, got %s", *darkConfig.Document.Color)
	}
	// Dark mode background should be nil (transparent) to avoid Solarized/16-color
	// terminal issues where hex colors get downconverted to wrong ANSI slots (#101)
	if darkConfig.Document.BackgroundColor != nil {
		t.Errorf("expected dark mode BackgroundColor to be nil (transparent), got %v", *darkConfig.Document.BackgroundColor)
	}

	// Test light mode
	lightConfig := buildStyleFromTheme(theme, false)
	if *lightConfig.Document.Color != "#000000" {
		t.Errorf("expected light mode doc color #000000, got %s", *lightConfig.Document.Color)
	}
	// Light mode should have nil background (use terminal default)
	if lightConfig.Document.BackgroundColor != nil {
		t.Errorf("expected light mode BackgroundColor to be nil, got %v", lightConfig.Document.BackgroundColor)
	}
}
