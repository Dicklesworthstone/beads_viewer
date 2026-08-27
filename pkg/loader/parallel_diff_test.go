package loader

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestReadParallelCandidateBoundedRestoresOffsetWhenLimitExceeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.jsonl")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open candidate: %v", err)
	}
	defer f.Close()
	if _, err := f.Seek(1, io.SeekStart); err != nil {
		t.Fatalf("position candidate: %v", err)
	}

	data, withinLimit, err := readParallelCandidateBounded(f, 4)
	if err != nil {
		t.Fatalf("bounded candidate read: %v", err)
	}
	if withinLimit || data != nil {
		t.Fatalf("over-limit candidate = %q, withinLimit=%v; want serial fallback", data, withinLimit)
	}
	next := make([]byte, 1)
	if _, err := io.ReadFull(f, next); err != nil {
		t.Fatalf("read after fallback: %v", err)
	}
	if string(next) != "b" {
		t.Fatalf("file offset was not restored: next byte = %q, want b", next)
	}
}

func TestReadParallelCandidateBoundedAcceptsInputAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.jsonl")
	if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open candidate: %v", err)
	}
	defer f.Close()

	data, withinLimit, err := readParallelCandidateBounded(f, 4)
	if err != nil {
		t.Fatalf("bounded candidate read: %v", err)
	}
	if !withinLimit || string(data) != "abcd" {
		t.Fatalf("at-limit candidate = %q, withinLimit=%v", data, withinLimit)
	}
}

// parseSerial runs the loader's serial path on data by feeding it through a
// *bytes.Reader (which is not *os.File, so parseIssuesWithOptions never takes
// the parallel fast path). It returns issues, poolRefs, stats, and the ordered
// warnings — the four things the parallel path must reproduce exactly.
func parseSerial(t *testing.T, data []byte, usePool bool, filter func(*model.Issue) bool) ([]model.Issue, []*model.Issue, ParseStats, []string) {
	t.Helper()
	var stats ParseStats
	var warns []string
	opts := ParseOptions{
		Stats:          &stats,
		IssueFilter:    filter,
		WarningHandler: func(msg string) { warns = append(warns, msg) },
	}
	issues, refs, err := parseIssuesWithOptions(bytes.NewReader(data), opts, usePool)
	if err != nil {
		t.Fatalf("serial parse error: %v", err)
	}
	return issues, refs, stats, warns
}

// parseParallel runs the dedicated parallel orchestrator directly on data so the
// differential test exercises the concurrent code path regardless of file size.
func parseParallel(t *testing.T, data []byte, usePool bool, filter func(*model.Issue) bool) ([]model.Issue, []*model.Issue, ParseStats, []string) {
	t.Helper()
	var stats ParseStats
	var warns []string
	opts := ParseOptions{
		Stats:          &stats,
		IssueFilter:    filter,
		WarningHandler: func(msg string) { warns = append(warns, msg) },
	}
	issues, refs, err := parseIssuesParallel(data, opts, usePool, DefaultMaxBufferSize)
	if err != nil {
		t.Fatalf("parallel parse error: %v", err)
	}
	return issues, refs, stats, warns
}

// assertDiffEqual checks that the serial and parallel outputs are identical in
// every observable dimension: the issue slice (value + order), the count, the
// stats, and the ordered warnings.
func assertDiffEqual(t *testing.T, label string, data []byte, usePool bool, filter func(*model.Issue) bool) {
	t.Helper()

	sIssues, sRefs, sStats, sWarns := parseSerial(t, data, usePool, filter)
	pIssues, pRefs, pStats, pWarns := parseParallel(t, data, usePool, filter)

	// Return pooled refs once compared so the pool stays balanced under -race.
	if usePool {
		defer ReturnIssuePtrsToPool(sRefs)
		defer ReturnIssuePtrsToPool(pRefs)
	}

	if len(sIssues) != len(pIssues) {
		t.Fatalf("%s: count mismatch: serial=%d parallel=%d", label, len(sIssues), len(pIssues))
	}
	if !reflect.DeepEqual(sIssues, pIssues) {
		// Pinpoint the first differing issue for a useful failure message.
		for i := range sIssues {
			if !reflect.DeepEqual(sIssues[i], pIssues[i]) {
				t.Fatalf("%s: issue[%d] differs (order/content):\n serial=%+v\n parall=%+v",
					label, i, sIssues[i], pIssues[i])
			}
		}
		t.Fatalf("%s: issue slices differ but no single index isolated", label)
	}
	if sStats != pStats {
		t.Fatalf("%s: stats mismatch: serial=%+v parallel=%+v", label, sStats, pStats)
	}
	if !reflect.DeepEqual(sWarns, pWarns) {
		t.Fatalf("%s: warnings mismatch:\n serial=%v\n parall=%v", label, sWarns, pWarns)
	}
}

// TestParallelDiff_RealData proves byte-equivalence on the repo's own
// .beads/issues.jsonl (the production workload), for both the plain and pooled
// paths and with/without an issue filter.
func TestParallelDiff_RealData(t *testing.T) {
	candidates := []string{
		filepath.Join("..", "..", ".beads", "issues.jsonl"),
		filepath.Join("..", "..", ".beads", "beads.jsonl"),
	}
	var data []byte
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil && len(b) > 0 {
			data = b
			break
		}
	}
	if data == nil {
		t.Skip("no real .beads JSONL available")
	}

	assertDiffEqual(t, "real/plain", data, false, nil)
	assertDiffEqual(t, "real/pooled", data, true, nil)

	onlyOpen := func(i *model.Issue) bool { return strings.EqualFold(string(i.Status), "open") }
	assertDiffEqual(t, "real/plain+filter", data, false, onlyOpen)
	assertDiffEqual(t, "real/pooled+filter", data, true, onlyOpen)
}

// TestParallelDiff_EdgeFixtures exercises corrupt/edge inputs to prove the
// parallel path reproduces the serial path's warning text, ordering, stats, and
// skip semantics across chunk boundaries.
func TestParallelDiff_EdgeFixtures(t *testing.T) {
	good := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"title":"T-%s","status":"open","issue_type":"task","priority":1}`, id, id)
	}

	// Build a body large enough to span many parallel chunks (the orchestrator
	// targets 256KiB chunks), interleaving valid, malformed, invalid, non-issue,
	// empty, and unknown-_type lines so warnings land on many different lines.
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		switch i % 11 {
		case 3:
			b.WriteString(`{"id":"BAD",not json`) // malformed JSON
		case 5:
			b.WriteString(`{"title":"no id","status":"open","issue_type":"task"}`) // invalid: missing id
		case 7:
			b.WriteString(`{"_type":"memory","text":"a note"}`) // non-issue: silent skip
		case 9:
			b.WriteString("") // empty line
		case 10:
			b.WriteString(`{"_type":"totally_unknown_kind","x":1}`) // unknown _type: silent skip
		default:
			b.WriteString(good(fmt.Sprintf("ISSUE-%d", i)))
		}
		b.WriteByte('\n')
	}
	data := []byte(b.String())

	assertDiffEqual(t, "edge/plain", data, false, nil)
	assertDiffEqual(t, "edge/pooled", data, true, nil)
}

// TestParallelDiff_BOMAndCRLF proves the first-line BOM strip and CRLF trimming
// are handled identically when the input is split into parallel chunks.
func TestParallelDiff_BOMAndCRLF(t *testing.T) {
	good := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"title":"T","status":"open","issue_type":"task","priority":1}`, id)
	}
	var b bytes.Buffer
	b.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM on first line
	for i := 0; i < 3000; i++ {
		b.WriteString(good(fmt.Sprintf("CRLF-%d", i)))
		b.WriteString("\r\n") // CRLF endings
	}
	data := b.Bytes()

	assertDiffEqual(t, "bom-crlf/plain", data, false, nil)
	assertDiffEqual(t, "bom-crlf/pooled", data, true, nil)
}

// TestParallelDiff_NoTrailingNewline proves a final partial line (no trailing
// '\n') is parsed identically by both paths.
func TestParallelDiff_NoTrailingNewline(t *testing.T) {
	good := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"title":"T","status":"open","issue_type":"task","priority":1}`, id)
	}
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(good(fmt.Sprintf("NT-%d", i)))
		b.WriteByte('\n')
	}
	b.WriteString(good("NT-LAST")) // no trailing newline
	data := []byte(b.String())

	assertDiffEqual(t, "no-trailing-nl/plain", data, false, nil)
	assertDiffEqual(t, "no-trailing-nl/pooled", data, true, nil)
}

func TestParallelDiff_FinalBareCarriageReturnIsContent(t *testing.T) {
	data := []byte(`{"id":"CR","title":"bare CR is not CRLF","status":"open","issue_type":"task","priority":1}` + "\r")

	for _, usePool := range []bool{false, true} {
		assertDiffEqual(t, fmt.Sprintf("bare-cr/pooled=%v", usePool), data, usePool, nil)
	}
}

func TestParallelDiff_DuplicateIntegrityPrecedesFilteringAndWarningsStayOrdered(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"id":"same","title":"canonical","status":"open","issue_type":"task","priority":1}`,
		`{"id":"same","title":"duplicate","status":"open","issue_type":"task","priority":2}`,
		`{"id":"broken",not-json}`,
		`{"id":"other","title":"kept","status":"open","issue_type":"task","priority":3}`,
	}, "\n"))

	for _, usePool := range []bool{false, true} {
		newFilter := func(calls *[]string) func(*model.Issue) bool {
			return func(issue *model.Issue) bool {
				*calls = append(*calls, issue.ID)
				return issue.ID != "same"
			}
		}

		var serialCalls, parallelCalls []string
		sIssues, sRefs, sStats, sWarns := parseSerial(t, data, usePool, newFilter(&serialCalls))
		pIssues, pRefs, pStats, pWarns := parseParallel(t, data, usePool, newFilter(&parallelCalls))
		if usePool {
			defer ReturnIssuePtrsToPool(sRefs)
			defer ReturnIssuePtrsToPool(pRefs)
		}

		if !reflect.DeepEqual(sIssues, pIssues) || len(sIssues) != 1 || sIssues[0].ID != "other" {
			t.Fatalf("usePool=%v: serial/parallel retained issues differ: serial=%+v parallel=%+v", usePool, sIssues, pIssues)
		}
		wantCalls := []string{"same", "other"}
		if !reflect.DeepEqual(serialCalls, wantCalls) || !reflect.DeepEqual(parallelCalls, wantCalls) {
			t.Fatalf("usePool=%v: filters must run serially on canonical records in source order: serial=%v parallel=%v", usePool, serialCalls, parallelCalls)
		}
		wantStats := ParseStats{Valid: 2, Errors: 2}
		if sStats != wantStats || pStats != wantStats {
			t.Fatalf("usePool=%v: duplicate accounting changed with filtering: serial=%+v parallel=%+v want=%+v", usePool, sStats, pStats, wantStats)
		}
		if !reflect.DeepEqual(sWarns, pWarns) || len(sWarns) != 2 {
			t.Fatalf("usePool=%v: warning parity/order mismatch: serial=%v parallel=%v", usePool, sWarns, pWarns)
		}
		if !strings.Contains(sWarns[0], `duplicate issue ID "same" on line 2`) || !strings.Contains(sWarns[1], "malformed JSON on line 3") {
			t.Fatalf("usePool=%v: warnings are not in source order: %v", usePool, sWarns)
		}
	}
}

func TestParallelDiff_StatefulFilterRunsSequentiallyInSourceOrder(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, `{"id":"FILTER-%04d","title":"T","status":"open","issue_type":"task","priority":1}`+"\n", i)
	}
	data := []byte(b.String())

	newAlternatingFilter := func(calls *[]string) func(*model.Issue) bool {
		return func(issue *model.Issue) bool {
			*calls = append(*calls, issue.ID)
			return len(*calls)%3 == 1
		}
	}

	for _, usePool := range []bool{false, true} {
		var serialCalls, parallelCalls []string
		sIssues, sRefs, sStats, sWarns := parseSerial(t, data, usePool, newAlternatingFilter(&serialCalls))
		pIssues, pRefs, pStats, pWarns := parseParallel(t, data, usePool, newAlternatingFilter(&parallelCalls))
		if usePool {
			defer ReturnIssuePtrsToPool(sRefs)
			defer ReturnIssuePtrsToPool(pRefs)
		}

		if !reflect.DeepEqual(serialCalls, parallelCalls) || len(serialCalls) != 3000 {
			t.Fatalf("usePool=%v: filter call order differs: serial=%d calls parallel=%d calls", usePool, len(serialCalls), len(parallelCalls))
		}
		if !reflect.DeepEqual(sIssues, pIssues) || sStats != pStats || !reflect.DeepEqual(sWarns, pWarns) {
			t.Fatalf("usePool=%v: stateful filter changed observable results between serial and parallel paths", usePool)
		}
	}
}

func TestParallelDiff_WarningAndFilterCallbacksObserveSourceOrder(t *testing.T) {
	data := []byte("{not-json}\n" +
		`{"id":"kept","title":"Kept","status":"open","issue_type":"task","priority":1}` + "\n")

	run := func(parallel bool) ([]model.Issue, ParseStats, []string) {
		var stats ParseStats
		var warnings []string
		allow := false
		opts := ParseOptions{
			Stats: &stats,
			WarningHandler: func(message string) {
				warnings = append(warnings, message)
				if strings.Contains(message, "malformed JSON on line 1") && stats.Errors == 1 {
					allow = true
				}
			},
			IssueFilter: func(*model.Issue) bool {
				return allow && stats.Valid == 1 && stats.Errors == 1
			},
		}
		var issues []model.Issue
		var err error
		if parallel {
			issues, _, err = parseIssuesParallel(data, opts, false, DefaultMaxBufferSize)
		} else {
			issues, _, err = parseIssuesWithOptions(bytes.NewReader(data), opts, false)
		}
		if err != nil {
			t.Fatalf("parallel=%v: parse error: %v", parallel, err)
		}
		return issues, stats, warnings
	}

	serialIssues, serialStats, serialWarnings := run(false)
	parallelIssues, parallelStats, parallelWarnings := run(true)
	if !reflect.DeepEqual(serialIssues, parallelIssues) || len(serialIssues) != 1 || serialIssues[0].ID != "kept" {
		t.Fatalf("callback timing changed output: serial=%+v parallel=%+v", serialIssues, parallelIssues)
	}
	if serialStats != parallelStats || !reflect.DeepEqual(serialWarnings, parallelWarnings) {
		t.Fatalf("callback timing changed accounting: serial=%+v %v parallel=%+v %v", serialStats, serialWarnings, parallelStats, parallelWarnings)
	}
}

func TestOverLimitLineIsCountedAsDroppedRecordOnSerialAndParallelPaths(t *testing.T) {
	const lineLimit = 128
	data := []byte(strings.Repeat("x", lineLimit) + "\n" +
		`{"id":"kept","title":"Kept","status":"open","issue_type":"task"}` + "\n")

	run := func(parallel bool) ([]model.Issue, ParseStats, []string) {
		t.Helper()
		var stats ParseStats
		var warnings []string
		opts := ParseOptions{
			BufferSize: lineLimit,
			Stats:      &stats,
			WarningHandler: func(message string) {
				if stats.Errors != 1 {
					t.Fatalf("parallel=%v: over-limit warning observed errors=%d, want 1", parallel, stats.Errors)
				}
				warnings = append(warnings, message)
			},
		}
		var issues []model.Issue
		var err error
		if parallel {
			issues, _, err = parseIssuesParallel(data, opts, false, lineLimit)
		} else {
			issues, _, err = parseIssuesWithOptions(bytes.NewReader(data), opts, false)
		}
		if err != nil {
			t.Fatalf("parallel=%v: parse error: %v", parallel, err)
		}
		return issues, stats, warnings
	}

	serialIssues, serialStats, serialWarnings := run(false)
	parallelIssues, parallelStats, parallelWarnings := run(true)
	wantStats := ParseStats{Valid: 1, Errors: 1}
	if !reflect.DeepEqual(serialIssues, parallelIssues) || len(serialIssues) != 1 || serialIssues[0].ID != "kept" {
		t.Fatalf("serial/parallel retained issues differ: serial=%+v parallel=%+v", serialIssues, parallelIssues)
	}
	if serialStats != wantStats || parallelStats != wantStats {
		t.Fatalf("over-limit accounting: serial=%+v parallel=%+v want=%+v", serialStats, parallelStats, wantStats)
	}
	if !reflect.DeepEqual(serialWarnings, parallelWarnings) || len(serialWarnings) != 1 || !strings.Contains(serialWarnings[0], "line too long") {
		t.Fatalf("over-limit warnings differ: serial=%v parallel=%v", serialWarnings, parallelWarnings)
	}
}

func TestParallelDiff_DuplicateWarningObservesReclassifiedStats(t *testing.T) {
	data := []byte(
		`{"id":"dup","title":"First","status":"open","issue_type":"task","priority":1}` + "\n" +
			`{"id":"dup","title":"Second","status":"open","issue_type":"task","priority":1}` + "\n",
	)

	run := func(parallel bool) (ParseStats, []ParseStats) {
		var stats ParseStats
		var observed []ParseStats
		opts := ParseOptions{
			Stats: &stats,
			WarningHandler: func(message string) {
				if strings.Contains(message, "skipping duplicate issue ID") {
					observed = append(observed, stats)
				}
			},
		}
		var err error
		if parallel {
			_, _, err = parseIssuesParallel(data, opts, false, DefaultMaxBufferSize)
		} else {
			_, _, err = parseIssuesWithOptions(bytes.NewReader(data), opts, false)
		}
		if err != nil {
			t.Fatalf("parallel=%v: parse error: %v", parallel, err)
		}
		return stats, observed
	}

	want := ParseStats{Valid: 1, Errors: 1}
	for _, parallel := range []bool{false, true} {
		stats, observed := run(parallel)
		if stats != want {
			t.Fatalf("parallel=%v: stats=%+v, want %+v", parallel, stats, want)
		}
		if len(observed) != 1 || observed[0] != want {
			t.Fatalf("parallel=%v: duplicate callback observed %+v, want [%+v]", parallel, observed, want)
		}
	}
}

func TestParallelDiff_LineCapacityBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		bufferSize int
	}{
		{name: "lf accepted just below cap", data: []byte(strings.Repeat("x", 15) + "\n"), bufferSize: 16},
		{name: "lf rejected at cap", data: []byte(strings.Repeat("x", 16) + "\n"), bufferSize: 16},
		{name: "crlf accepted when terminator fits", data: []byte(strings.Repeat("x", 14) + "\r\n"), bufferSize: 16},
		{name: "crlf rejected when cr fills cap", data: []byte(strings.Repeat("x", 15) + "\r\n"), bufferSize: 16},
		{name: "sub-minimum buffer uses bufio floor", data: []byte(strings.Repeat("x", 15) + "\n"), bufferSize: 1},
		{name: "whitespace-only lines are ignored", data: []byte("   \n\t\r\n"), bufferSize: 16},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var serialStats, parallelStats ParseStats
			var serialWarnings, parallelWarnings []string
			serialOpts := ParseOptions{
				BufferSize:     tc.bufferSize,
				Stats:          &serialStats,
				WarningHandler: func(message string) { serialWarnings = append(serialWarnings, message) },
			}
			parallelOpts := ParseOptions{
				BufferSize:     tc.bufferSize,
				Stats:          &parallelStats,
				WarningHandler: func(message string) { parallelWarnings = append(parallelWarnings, message) },
			}

			serialIssues, _, serialErr := parseIssuesWithOptions(bytes.NewReader(tc.data), serialOpts, false)
			if serialErr != nil {
				t.Fatal(serialErr)
			}
			parallelIssues, _, parallelErr := parseIssuesParallel(tc.data, parallelOpts, false, tc.bufferSize)
			if parallelErr != nil {
				t.Fatal(parallelErr)
			}
			if !reflect.DeepEqual(serialIssues, parallelIssues) || serialStats != parallelStats || !reflect.DeepEqual(serialWarnings, parallelWarnings) {
				t.Fatalf("boundary mismatch: serial=%+v %+v %v parallel=%+v %+v %v", serialIssues, serialStats, serialWarnings, parallelIssues, parallelStats, parallelWarnings)
			}
		})
	}
}

// TestParallelParse_AutoDispatchMatchesSerial proves the public entry point's
// size-gated auto-dispatch actually takes the parallel branch (when the file
// exceeds parallelParseMinBytes) and that its result is identical to forcing
// the serial path over the same bytes. It synthesizes a file above the
// threshold from the real data so the test is independent of the repo's current
// store size.
func TestParallelParse_AutoDispatchMatchesSerial(t *testing.T) {
	src := filepath.Join("..", "..", ".beads", "issues.jsonl")
	base, err := os.ReadFile(src)
	if err != nil || len(base) == 0 {
		t.Skip("no real .beads/issues.jsonl available")
	}

	// Replicate the real data until comfortably above the parallel threshold so
	// the *os.File entry point takes the concurrent branch.
	var buf bytes.Buffer
	for int64(buf.Len()) <= parallelParseMinBytes+(1<<20) {
		buf.Write(base)
	}
	data := buf.Bytes()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write big fixture: %v", err)
	}

	// Auto path: through the *os.File entry point (takes the parallel branch
	// because the file is above parallelParseMinBytes).
	autoIssues, err := LoadIssuesFromFile(path)
	if err != nil {
		t.Fatalf("LoadIssuesFromFile: %v", err)
	}

	// Reference: forced serial over the identical bytes.
	refIssues, _, _, _ := parseSerial(t, data, false, nil)

	if len(autoIssues) != len(refIssues) {
		t.Fatalf("auto-dispatch count differs: auto=%d ref=%d", len(autoIssues), len(refIssues))
	}
	if !reflect.DeepEqual(autoIssues, refIssues) {
		t.Fatalf("auto-dispatch result differs from serial")
	}
	if runtime.NumCPU() < 1 {
		t.Fatal("unexpected NumCPU")
	}
}
