package correlation

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSnapshotMatchesLegacyPatch is a differential test: extractViaSnapshots
// must produce the same BeadEvents as the legacy extractViaGitLogPatch on a real
// repo. Point it at a beads repo via BV_DIFFCHECK_REPO; skipped otherwise.
func TestSnapshotMatchesLegacyPatch(t *testing.T) {
	repo := os.Getenv("BV_DIFFCHECK_REPO")
	if repo == "" {
		t.Skip("set BV_DIFFCHECK_REPO to a beads git repo to run the differential check")
	}
	e := NewExtractor(repo)
	opts := ExtractOptions{Limit: 200}

	legacy, err := e.extractViaGitLogPatch(opts)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	snap, err := e.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	key := func(ev BeadEvent) string {
		return fmt.Sprintf("%s|%s|%s|%s", ev.CommitSHA, ev.BeadID, ev.EventType, ev.Timestamp.Format("2006-01-02T15:04:05Z07:00"))
	}
	ls := make([]string, 0, len(legacy))
	for _, ev := range legacy {
		ls = append(ls, key(ev))
	}
	ss := make([]string, 0, len(snap))
	for _, ev := range snap {
		ss = append(ss, key(ev))
	}
	sort.Strings(ls)
	sort.Strings(ss)

	t.Logf("legacy events=%d snapshot events=%d", len(ls), len(ss))
	if len(ls) != len(ss) {
		t.Fatalf("event count mismatch: legacy=%d snapshot=%d", len(ls), len(ss))
	}
	for i := range ls {
		if ls[i] != ss[i] {
			t.Fatalf("event mismatch at %d:\n legacy=%s\n snap  =%s", i, ls[i], ss[i])
		}
	}
}

func TestBlobReaderReusesOnlyRecycledBuffers(t *testing.T) {
	missingBeforeOID := strings.Repeat("a", 40)
	oldOID := strings.Repeat("b", 40)
	currentOID := strings.Repeat("c", 40)
	nextOID := strings.Repeat("d", 40)
	missingAfterOID := strings.Repeat("e", 40)
	contentForStatus := func(status string) []byte {
		padding := strings.Repeat("x", 64-len(status))
		return []byte(fmt.Sprintf("{\"id\":\"bv-a\",\"status\":%q,\"title\":%q}\n", status, padding))
	}
	oldContent := contentForStatus("open")
	currentContent := contentForStatus("in_progress")
	nextContent := contentForStatus("closed")

	var protocol bytes.Buffer
	fmt.Fprintf(&protocol, "%s missing\n", missingBeforeOID)
	for i, content := range [][]byte{oldContent, currentContent, nextContent} {
		oid := []string{oldOID, currentOID, nextOID}[i]
		fmt.Fprintf(&protocol, "%s blob %d\n", oid, len(content))
		protocol.Write(content)
		protocol.WriteByte('\n')
	}
	fmt.Fprintf(&protocol, "%s missing\n", missingAfterOID)

	var requests bytes.Buffer
	reader := &blobReader{
		w:           bufio.NewWriter(&requests),
		out:         bufio.NewReaderSize(&protocol, blobReaderBufferSize),
		arenaRunway: blobReaderArenaRunway,
	}

	if missing, err := reader.read(missingBeforeOID); err == nil || missing != nil {
		t.Fatalf("missing blob before first valid blob = (%q, %v), want nil/error", missing, err)
	}
	if reader.arenaRunway != blobReaderArenaRunway {
		t.Fatalf("missing blob consumed arena runway: got %d, want %d", reader.arenaRunway, blobReaderArenaRunway)
	}

	oldBlob, err := reader.read(oldOID)
	if err != nil {
		t.Fatalf("read old blob: %v", err)
	}
	if got, want := cap(oldBlob), len(oldBlob)+blobReaderArenaRunway; got != want {
		t.Fatalf("first valid blob capacity = %d, want payload %d + runway %d = %d", got, len(oldBlob), blobReaderArenaRunway, want)
	}
	if got, want := reader.out.Size()+cap(oldBlob), gitLogMaxScanTokenSize+len(oldBlob); got != want {
		t.Fatalf("transport + first arena capacity = %d, want prior transport + payload = %d", got, want)
	}
	if reader.arenaRunway != 0 {
		t.Fatalf("first valid blob left arena runway = %d, want 0", reader.arenaRunway)
	}
	oldSet := newRecordLineSet(oldBlob)
	currentBlob, err := reader.read(currentOID)
	if err != nil {
		t.Fatalf("read current blob: %v", err)
	}
	if &oldBlob[0] == &currentBlob[0] {
		t.Fatal("reader reused a buffer whose record-line set was still live")
	}
	currentSet := newRecordLineSet(currentBlob)
	claimed := NewExtractor("/tmp/test").parseDiff(
		synthesizeRecordDiff(oldSet, currentSet), commitInfo{}, "",
	)
	if len(claimed) != 1 || claimed[0].BeadID != "bv-a" || claimed[0].EventType != EventClaimed {
		t.Fatalf("old/current diff changed before recycle: %#v", claimed)
	}

	currentBefore := append([]byte(nil), currentBlob...)
	reader.recycle(oldBlob)
	nextBlob, err := reader.read(nextOID)
	if err != nil {
		t.Fatalf("read next blob: %v", err)
	}
	if &nextBlob[0] != &oldBlob[0] {
		t.Fatal("reader did not reuse the explicitly recycled buffer")
	}
	if cap(nextBlob) != cap(oldBlob) {
		t.Fatalf("reused buffer capacity = %d, want preserved arena capacity %d", cap(nextBlob), cap(oldBlob))
	}
	if !bytes.Equal(currentBlob, currentBefore) {
		t.Fatal("reusing the old buffer overwrote the still-live current blob")
	}
	closed := NewExtractor("/tmp/test").parseDiff(
		synthesizeRecordDiff(currentSet, newRecordLineSet(nextBlob)), commitInfo{}, "",
	)
	if len(closed) != 1 || closed[0].BeadID != "bv-a" || closed[0].EventType != EventClosed {
		t.Fatalf("current/next diff changed after reuse: %#v", closed)
	}

	reader.recycle(nextBlob)
	if missing, err := reader.read(missingAfterOID); err == nil || missing != nil {
		t.Fatalf("missing blob after recycle = (%q, %v), want nil/error", missing, err)
	}
	if cap(reader.spare) == 0 {
		t.Fatal("missing blob consumed the reusable buffer")
	}
}

func TestParseCatFileBatchHeaderRejectsMalformedResponses(t *testing.T) {
	oid := strings.Repeat("a", 40)
	otherOID := strings.Repeat("b", 40)
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name      string
		requested string
		header    string
		runway    int
	}{
		{name: "invalid requested object ID", requested: "not-an-object-id", header: "not-an-object-id missing\n"},
		{name: "unterminated", requested: oid, header: oid + " blob 0"},
		{name: "mismatched object ID", requested: oid, header: otherOID + " blob 0\n"},
		{name: "mismatched missing object ID", requested: oid, header: otherOID + " missing\n"},
		{name: "non-blob type", requested: oid, header: oid + " tree 0\n"},
		{name: "negative size", requested: oid, header: oid + " blob -1\n"},
		{name: "explicitly signed size", requested: oid, header: oid + " blob +1\n"},
		{name: "uint64 overflow", requested: oid, header: oid + " blob 999999999999999999999999999999999999\n"},
		{name: "arena runway overflow", requested: oid, header: oid + " blob " + strconv.Itoa(maxInt) + "\n", runway: 1},
		{name: "extra whitespace", requested: oid, header: oid + "  blob 0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if size, missing, err := parseCatFileBatchHeader(tt.requested, tt.header, tt.runway); err == nil {
				t.Fatalf("parseCatFileBatchHeader() = (%d, %t, nil), want error", size, missing)
			}
		})
	}

	if size, missing, err := parseCatFileBatchHeader(oid, oid+" blob 0\n", blobReaderArenaRunway); err != nil || size != 0 || missing {
		t.Fatalf("valid empty blob = (%d, %t, %v), want (0, false, nil)", size, missing, err)
	}
	if size, missing, err := parseCatFileBatchHeader(oid, oid+" missing\n", blobReaderArenaRunway); err != nil || size != 0 || !missing {
		t.Fatalf("valid missing blob = (%d, %t, %v), want (0, true, nil)", size, missing, err)
	}
}

func TestBlobReaderRejectsInvalidCatFileTrailer(t *testing.T) {
	oid := strings.Repeat("a", 40)
	protocol := bytes.NewBufferString(oid + " blob 1\nx!")
	reader := &blobReader{
		w:   bufio.NewWriter(&bytes.Buffer{}),
		out: bufio.NewReaderSize(protocol, blobReaderBufferSize),
	}
	if content, err := reader.read(oid); err == nil {
		t.Fatalf("read malformed trailer = %q, nil; want error", content)
	}
}

func TestBlobReaderReturnsNonNilEmptyBlobAfterArenaRunway(t *testing.T) {
	oid := strings.Repeat("a", 40)
	protocol := bytes.NewBufferString(oid + " blob 0\n\n")
	reader := &blobReader{
		w:   bufio.NewWriter(&bytes.Buffer{}),
		out: bufio.NewReaderSize(protocol, blobReaderBufferSize),
		// Zero models every read after the first real blob consumed the one-time
		// arena runway.
		arenaRunway: 0,
	}
	content, err := reader.read(oid)
	if err != nil {
		t.Fatalf("read empty blob: %v", err)
	}
	if content == nil || len(content) != 0 {
		t.Fatalf("empty blob = %#v, want non-nil zero-length slice", content)
	}
}

func TestSnapshotBufferReusePreservesOverlappingHistory(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	writeState := func(status, message string) {
		t.Helper()
		// Equal-size snapshots force each evicted buffer to be eligible for the
		// next read while the shared boundary blob remains live.
		padding := strings.Repeat("x", 128*1024-len(status))
		content := fmt.Sprintf("{\"id\":\"bv-a\",\"status\":%q,\"title\":%q}\n", status, padding)
		if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", status, err)
		}
		runGit(t, repo, "add", ".beads/issues.jsonl")
		runGit(t, repo, "commit", "-m", message)
	}

	writeState("open", "create bead")
	writeState("in_progress", "claim bead")
	writeState("closed", "close bead")
	writeState("open", "reopen bead")

	e := NewExtractor(repo)
	configuredReader, err := e.newBlobReader()
	if err != nil {
		t.Fatalf("create configured blob reader: %v", err)
	}
	if got := configuredReader.out.Size(); got != blobReaderBufferSize {
		t.Errorf("configured blob reader transport capacity = %d, want %d", got, blobReaderBufferSize)
	}
	if got := configuredReader.arenaRunway; got != blobReaderArenaRunway {
		t.Errorf("configured blob reader arena runway = %d, want %d", got, blobReaderArenaRunway)
	}
	if err := configuredReader.Close(); err != nil {
		t.Fatalf("close configured blob reader: %v", err)
	}

	opts := ExtractOptions{Limit: 10}
	legacy, err := e.extractViaGitLogPatch(opts)
	if err != nil {
		t.Fatalf("legacy extraction: %v", err)
	}
	snapshot, err := e.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("snapshot extraction: %v", err)
	}
	assertEventsByteIdentical(t, legacy, snapshot, "recycled overlapping window")

	wantTypes := []EventType{EventCreated, EventClaimed, EventClosed, EventReopened}
	if len(snapshot) != len(wantTypes) {
		t.Fatalf("snapshot event count = %d, want %d", len(snapshot), len(wantTypes))
	}
	for i, want := range wantTypes {
		if snapshot[i].BeadID != "bv-a" || snapshot[i].EventType != want {
			t.Fatalf("snapshot event %d = (%q, %q), want (bv-a, %q)", i, snapshot[i].BeadID, snapshot[i].EventType, want)
		}
	}
}

func TestRecordLineSnapshotFrontierMatchesFullBuild(t *testing.T) {
	tests := []struct {
		name           string
		reference      string
		target         string
		wantHashed     int
		wantReused     int
		wantHashBytes  int
		wantReuseBytes int
	}{
		{
			name:       "changed middle",
			reference:  "{\"id\":\"a\"}\n{\"id\":\"middle\",\"v\":1}\n{\"id\":\"z\"}\n",
			target:     "{\"id\":\"a\"}\n{\"id\":\"middle\",\"v\":2}\n{\"id\":\"z\"}\n",
			wantHashed: 1,
			wantReused: 2,
		},
		{
			name:       "equal length unequal records",
			reference:  "{\"id\":\"a\",\"v\":1}\n",
			target:     "{\"id\":\"b\",\"v\":2}\n",
			wantHashed: 1,
			wantReused: 0,
		},
		{
			name:       "single record cannot overlap prefix and suffix",
			reference:  "{\"id\":\"a\"}\n",
			target:     "{\"id\":\"a\"}\n",
			wantHashed: 0,
			wantReused: 1,
		},
		{
			name:       "duplicate boundary records",
			reference:  "{\"id\":\"a\"}\n{\"id\":\"a\"}\n{\"id\":\"z\"}\n",
			target:     "{\"id\":\"a\"}\n{\"id\":\"b\"}\n{\"id\":\"a\"}\n{\"id\":\"z\"}\n",
			wantHashed: 1,
			wantReused: 3,
		},
		{
			name:           "non records CRLF and no final LF",
			reference:      "metadata\r\n{\"id\":\"a\"}\r\nskip\n{\"id\":\"middle\",\"v\":1}\n{\"id\":\"z\"}",
			target:         "different metadata\r\n{\"id\":\"a\"}\r\nskip again\n{\"id\":\"middle\",\"v\":2}\n{\"id\":\"z\"}",
			wantHashed:     1,
			wantReused:     2,
			wantHashBytes:  len("{\"id\":\"middle\",\"v\":2}"),
			wantReuseBytes: len("{\"id\":\"a\"}\r") + len("{\"id\":\"z\"}"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reference, _ := buildRecordLineSnapshot([]byte(tc.reference), nil, hashRecordLine)
			frontier, stats := buildRecordLineSnapshot([]byte(tc.target), &reference, hashRecordLine)
			full, _ := buildRecordLineSnapshot([]byte(tc.target), nil, hashRecordLine)
			assertRecordLineSnapshotsEqual(t, full, frontier)

			if stats.hashedRecords != tc.wantHashed || stats.reusedRecords != tc.wantReused {
				t.Fatalf(
					"frontier work = hashed %d, reused %d; want hashed %d, reused %d",
					stats.hashedRecords, stats.reusedRecords, tc.wantHashed, tc.wantReused,
				)
			}
			if tc.wantHashBytes > 0 && stats.hashedBytes != tc.wantHashBytes {
				t.Fatalf("hashed bytes = %d, want %d", stats.hashedBytes, tc.wantHashBytes)
			}
			if tc.wantReuseBytes > 0 && stats.reusedBytes != tc.wantReuseBytes {
				t.Fatalf("reused bytes = %d, want %d", stats.reusedBytes, tc.wantReuseBytes)
			}
		})
	}
}

func TestRecordLineSnapshotFrontierPreservesForcedCollisionOrder(t *testing.T) {
	constantHash := func([]byte) uint64 { return 7 }
	tests := []struct {
		name      string
		reference string
		target    string
		wantFirst string
	}{
		{
			name:      "reused prefix remains first",
			reference: "{\"id\":\"a\"}\n",
			target:    "{\"id\":\"a\"}\n{\"id\":\"b\"}\n",
			wantFirst: "{\"id\":\"a\"}",
		},
		{
			name:      "hashed prefix precedes reused suffix",
			reference: "{\"id\":\"a\"}\n",
			target:    "{\"id\":\"b\"}\n{\"id\":\"a\"}\n",
			wantFirst: "{\"id\":\"b\"}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reference, _ := buildRecordLineSnapshot([]byte(tc.reference), nil, constantHash)
			frontier, stats := buildRecordLineSnapshot([]byte(tc.target), &reference, constantHash)
			full, _ := buildRecordLineSnapshot([]byte(tc.target), nil, constantHash)
			assertRecordLineSnapshotsEqual(t, full, frontier)

			entry := frontier.lines[constantHash(nil)]
			if entry == nil || entry.count != 1 || string(entry.text) != tc.wantFirst || entry.next == nil || entry.next.count != 1 || entry.next.next != nil {
				t.Fatalf("colliding bucket = %#v, want two distinct count-1 entries beginning with %q", entry, tc.wantFirst)
			}
			if stats.hashedRecords != 1 || stats.reusedRecords != 1 {
				t.Fatalf("collision frontier work = %#v, want one hashed and one reused record", stats)
			}
		})
	}
}

func TestSynthesizedRecordDiffPreservesForcedHashCollision(t *testing.T) {
	constantHash := func([]byte) uint64 { return 7 }
	oldLine := []byte("{\"id\":\"bv-collision\",\"title\":\"Collision\",\"status\":\"open\"}")
	newLine := []byte("{\"id\":\"bv-collision\",\"title\":\"Collision\",\"status\":\"closed\"}")
	oldSnapshot, _ := buildRecordLineSnapshot(append(append([]byte(nil), oldLine...), '\n'), nil, constantHash)
	newSnapshot, _ := buildRecordLineSnapshot(append(append([]byte(nil), newLine...), '\n'), nil, constantHash)

	diff := synthesizeRecordDiff(oldSnapshot.lines, newSnapshot.lines)
	if !bytes.Contains(diff, append([]byte{'-'}, oldLine...)) || !bytes.Contains(diff, append([]byte{'+'}, newLine...)) {
		t.Fatalf("colliding record replacement disappeared from synthesized diff: %q", diff)
	}
	events := NewExtractor("/tmp/test").parseDiff(diff, commitInfo{}, "")
	if len(events) != 1 || events[0].BeadID != "bv-collision" || events[0].EventType != EventClosed {
		t.Fatalf("colliding record replacement produced events %#v, want one closed event", events)
	}
}

func TestDiffParsersPreserveLeadingWhitespaceJSONLRecords(t *testing.T) {
	oldLine := `{"id":"bv-whitespace","title":"Whitespace","status":"open"}`
	newLine := `{"id":"bv-whitespace","title":"Whitespace","status":"closed"}`
	diffLines := []string{"-\t" + oldLine, "+  " + newLine}
	info := commitInfo{SHA: strings.Repeat("a", 40)}

	resident := NewExtractor("/tmp/test").parseDiff([]byte(strings.Join(diffLines, "\n")+"\n"), info, "")
	streamed := NewStreamExtractor("/tmp/test").parseBufferedDiff(diffLines, info, "", nil)
	for name, events := range map[string][]BeadEvent{"resident": resident, "streamed": streamed} {
		if len(events) != 1 || events[0].BeadID != "bv-whitespace" || events[0].EventType != EventClosed {
			t.Fatalf("%s parser produced %#v, want one closed event", name, events)
		}
	}
}

func TestDiffParsersPreserveBOMPrefixedFirstRecord(t *testing.T) {
	oldLine := `{"id":"bv-bom","title":"BOM","status":"open"}`
	newLine := `{"id":"bv-bom","title":"BOM","status":"closed"}`
	diffLines := []string{"-\uFEFF" + oldLine, "+\uFEFF" + newLine}
	info := commitInfo{SHA: strings.Repeat("b", 40)}

	resident := NewExtractor("/tmp/test").parseDiff([]byte(strings.Join(diffLines, "\n")+"\n"), info, "")
	streamed := NewStreamExtractor("/tmp/test").parseBufferedDiff(diffLines, info, "", nil)
	for name, events := range map[string][]BeadEvent{"resident": resident, "streamed": streamed} {
		if len(events) != 1 || events[0].BeadID != "bv-bom" || events[0].EventType != EventClosed {
			t.Fatalf("%s parser produced %#v, want one closed event", name, events)
		}
	}
}

func TestSynthesizedRecordDiffPreservesLoaderCompatibleRecordPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{name: "leading whitespace", prefix: " \t"},
		{name: "first-line UTF-8 BOM", prefix: "\uFEFF"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldLine := []byte(tt.prefix + `{"id":"bv-prefixed","title":"Prefixed","status":"open"}`)
			newLine := []byte(tt.prefix + `{"id":"bv-prefixed","title":"Prefixed","status":"closed"}`)
			oldSnapshot, _ := buildRecordLineSnapshot(append(append([]byte(nil), oldLine...), '\n'), nil, hashRecordLine)
			newSnapshot, _ := buildRecordLineSnapshot(append(append([]byte(nil), newLine...), '\n'), nil, hashRecordLine)

			if len(oldSnapshot.records) != 1 || !bytes.Equal(oldSnapshot.lines[hashRecordLine(oldLine)].text, oldLine) {
				t.Fatalf("old snapshot did not retain the full loader-compatible record: %#v", oldSnapshot)
			}
			diff := synthesizeRecordDiff(oldSnapshot.lines, newSnapshot.lines)
			if !bytes.Contains(diff, append([]byte{'-'}, oldLine...)) || !bytes.Contains(diff, append([]byte{'+'}, newLine...)) {
				t.Fatalf("synthesized diff lost loader-compatible physical lines: %q", diff)
			}
			events := NewExtractor("/tmp/test").parseDiff(diff, commitInfo{}, "")
			if len(events) != 1 || events[0].BeadID != "bv-prefixed" || events[0].EventType != EventClosed {
				t.Fatalf("synthesized prefixed record diff produced %#v, want one closed event", events)
			}
		})
	}
}

func TestRecordEligibilityRejectsPrefixesTheLoaderRejects(t *testing.T) {
	record := []byte(`{"id":"bv-prefix"}`)
	tests := []struct {
		name              string
		prefix            []byte
		firstPhysicalLine bool
		want              bool
	}{
		{name: "plain", firstPhysicalLine: false, want: true},
		{name: "JSON whitespace", prefix: []byte(" \t\r"), firstPhysicalLine: false, want: true},
		{name: "first-line BOM", prefix: []byte{0xEF, 0xBB, 0xBF}, firstPhysicalLine: true, want: true},
		{name: "first-line BOM then whitespace", prefix: []byte{0xEF, 0xBB, 0xBF, ' ', '\t'}, firstPhysicalLine: true, want: true},
		{name: "whitespace before first-line BOM", prefix: []byte{' ', 0xEF, 0xBB, 0xBF}, firstPhysicalLine: true, want: false},
		{name: "BOM after first line", prefix: []byte{0xEF, 0xBB, 0xBF}, firstPhysicalLine: false, want: false},
		{name: "non-JSON Unicode whitespace", prefix: []byte{0xC2, 0xA0}, firstPhysicalLine: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := append(append([]byte(nil), tt.prefix...), record...)
			if got := isBeadRecordLine(line, tt.firstPhysicalLine); got != tt.want {
				t.Fatalf("isBeadRecordLine(%q, %t) = %t, want %t", line, tt.firstPhysicalLine, got, tt.want)
			}
		})
	}

	for _, line := range []string{
		"+ \uFEFF" + string(record),
		"+\u00A0" + string(record),
	} {
		if _, _, ok := beadJSONFromDiffLine(line); ok {
			t.Fatalf("beadJSONFromDiffLine(%q) accepted a prefix rejected by the loader", line)
		}
	}
}

func TestDiffParsersHonorPhysicalFirstLineForBOM(t *testing.T) {
	record := `{"id":"bv-bom-position","title":"BOM position","status":"open"}`
	tests := []struct {
		name      string
		hunk      string
		wantEvent bool
	}{
		{name: "physical line one", hunk: "@@ -0,0 +1 @@", wantEvent: true},
		{name: "physical line two", hunk: "@@ -0,0 +2 @@", wantEvent: false},
	}
	info := commitInfo{SHA: strings.Repeat("c", 40)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := []string{tt.hunk, "+\uFEFF" + record}
			resident := NewExtractor("/tmp/test").parseDiff([]byte(strings.Join(lines, "\n")+"\n"), info, "")
			streamed := NewStreamExtractor("/tmp/test").parseBufferedDiff(lines, info, "", nil)
			for name, events := range map[string][]BeadEvent{"resident": resident, "streamed": streamed} {
				if tt.wantEvent {
					if len(events) != 1 || events[0].BeadID != "bv-bom-position" || events[0].EventType != EventCreated {
						t.Fatalf("%s parser produced %#v, want one created event", name, events)
					}
				} else if len(events) != 0 {
					t.Fatalf("%s parser accepted non-first-line BOM record: %#v", name, events)
				}
			}
		})
	}
}

func TestExtractionPreservesBOMEligibilityWhenRecordMovesToFirstLine(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	dummy := []byte(`{"id":"bv-dummy","title":"Dummy","status":"open"}` + "\n")
	target := []byte(`{"id":"bv-bom-shift","title":"BOM shift","status":"open"}` + "\n")
	bomTarget := append([]byte{0xEF, 0xBB, 0xBF}, target...)
	initial := append(append([]byte(nil), dummy...), bomTarget...)
	if err := os.WriteFile(beadsPath, initial, 0o644); err != nil {
		t.Fatalf("write initial BOM-shift fixture: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add dummy before invalid BOM record")

	if err := os.WriteFile(beadsPath, bomTarget, 0o644); err != nil {
		t.Fatalf("move BOM record to first line: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "remove dummy before BOM record")

	extractor := NewExtractor(repo)
	extractors := map[string]func() ([]BeadEvent, error){
		"legacy patch": func() ([]BeadEvent, error) {
			return extractor.extractViaGitLogPatch(ExtractOptions{})
		},
		"snapshot": func() ([]BeadEvent, error) {
			return extractor.extractViaSnapshots(ExtractOptions{})
		},
		"public dispatch": func() ([]BeadEvent, error) {
			return extractor.Extract(ExtractOptions{})
		},
		"stream": func() ([]BeadEvent, error) {
			return NewStreamExtractor(repo).StreamEvents(StreamOptions{})
		},
	}
	wantIDs := []string{"bv-dummy", "bv-bom-shift"}
	for name, extract := range extractors {
		events, err := extract()
		if err != nil {
			t.Fatalf("%s extraction: %v", name, err)
		}
		if len(events) != len(wantIDs) {
			t.Fatalf("%s events = %#v, want created events for %v", name, events, wantIDs)
		}
		for i, wantID := range wantIDs {
			if events[i].BeadID != wantID || events[i].EventType != EventCreated {
				t.Fatalf("%s event %d = (%q, %q), want (%q, created)", name, i, events[i].BeadID, events[i].EventType, wantID)
			}
		}
	}

	filteredExtractors := map[string]func() ([]BeadEvent, error){
		"filtered legacy patch": func() ([]BeadEvent, error) {
			return extractor.extractViaGitLogPatch(ExtractOptions{BeadID: "bv-bom-shift"})
		},
		"ExtractForBead": func() ([]BeadEvent, error) {
			return extractor.ExtractForBead("bv-bom-shift", ExtractOptions{})
		},
	}
	for name, extract := range filteredExtractors {
		events, err := extract()
		if err != nil {
			t.Fatalf("%s extraction: %v", name, err)
		}
		if len(events) != 1 || events[0].BeadID != "bv-bom-shift" || events[0].EventType != EventCreated {
			t.Fatalf("%s events = %#v, want one created event for bv-bom-shift", name, events)
		}
	}
}

func TestExtractForcedSnapshotPreservesLoaderCompatibleRecordPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{name: "leading whitespace", prefix: " \t"},
		{name: "first-line UTF-8 BOM", prefix: "\uFEFF"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initTempGitRepo(t)
			t.Setenv("BV_ROBOT", "1")
			t.Setenv("BV_NO_CACHE", "1")
			t.Setenv("BV_CACHE_DIR", t.TempDir())

			beadsDir := filepath.Join(repo, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("create beads directory: %v", err)
			}
			beadsPath := filepath.Join(beadsDir, "issues.jsonl")
			writeState := func(status, message string) {
				t.Helper()
				padding := strings.Repeat("x", snapshotBlobSizeThreshold+1024)
				content := fmt.Sprintf("%s{\"id\":\"bv-prefixed\",\"title\":%q,\"status\":%q}\n", tt.prefix, padding, status)
				if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
					t.Fatalf("write %s snapshot: %v", status, err)
				}
				runGit(t, repo, "add", ".beads/issues.jsonl")
				runGit(t, repo, "commit", "-m", message)
			}
			writeState("open", "create prefixed bead")
			writeState("closed", "close prefixed bead")

			extractor := NewExtractor(repo)
			if !extractor.preferSnapshotPath() {
				t.Fatal("fixture did not force the public Extract method onto the snapshot path")
			}
			events, err := extractor.Extract(ExtractOptions{})
			if err != nil {
				t.Fatalf("extract forced snapshot history: %v", err)
			}
			wantTypes := []EventType{EventCreated, EventClosed}
			if len(events) != len(wantTypes) {
				t.Fatalf("forced snapshot events = %#v, want created then closed", events)
			}
			for i, want := range wantTypes {
				if events[i].BeadID != "bv-prefixed" || events[i].EventType != want {
					t.Fatalf("forced snapshot event %d = (%q, %q), want (bv-prefixed, %q)", i, events[i].BeadID, events[i].EventType, want)
				}
			}
		})
	}
}

func TestRecordLineSnapshotFrontierFallsBackWithoutLiveReference(t *testing.T) {
	target := []byte("{\"id\":\"a\"}\n{\"id\":\"b\"}\n")
	hashCalls := 0
	countingHash := func(line []byte) uint64 {
		hashCalls++
		return uint64(len(line) + hashCalls)
	}

	hole := recordLineSnapshot{blob: append([]byte(nil), target...)}
	_, stats := buildRecordLineSnapshot(target, &hole, countingHash)
	if hashCalls != 2 || stats.hashedRecords != 2 || stats.reusedRecords != 0 {
		t.Fatalf("unindexed reference reused hashes: calls=%d stats=%#v", hashCalls, stats)
	}

	reference, _ := buildRecordLineSnapshot([]byte("{\"id\":\"a\"}\n"), nil, hashRecordLine)
	copy(reference.blob, []byte("{\"id\":\"z\"}\n")) // Model an incorrectly recycled reference arena.
	hashCalls = 0
	_, stats = buildRecordLineSnapshot([]byte("{\"id\":\"a\"}\n"), &reference, countingHash)
	if hashCalls != 1 || stats.hashedRecords != 1 || stats.reusedRecords != 0 {
		t.Fatalf("stale reference digest reused without live-byte equality: calls=%d stats=%#v", hashCalls, stats)
	}
}

func assertRecordLineSnapshotsEqual(t *testing.T, want, got recordLineSnapshot) {
	t.Helper()
	if len(got.lines) != len(want.lines) {
		t.Fatalf("line-set size = %d, want %d", len(got.lines), len(want.lines))
	}
	for hash, wantEntry := range want.lines {
		gotEntry, ok := got.lines[hash]
		if !ok {
			t.Fatalf("line set missing hash %d", hash)
		}
		for bucketIndex := 0; wantEntry != nil || gotEntry != nil; bucketIndex++ {
			if wantEntry == nil || gotEntry == nil {
				t.Fatalf("entry %d collision bucket length differs at index %d", hash, bucketIndex)
			}
			if gotEntry.count != wantEntry.count || !bytes.Equal(gotEntry.text, wantEntry.text) {
				t.Fatalf(
					"entry %d bucket %d = count %d text %q, want count %d text %q",
					hash, bucketIndex, gotEntry.count, gotEntry.text, wantEntry.count, wantEntry.text,
				)
			}
			wantEntry = wantEntry.next
			gotEntry = gotEntry.next
		}
	}
	if len(got.records) != len(want.records) {
		t.Fatalf("record descriptor count = %d, want %d", len(got.records), len(want.records))
	}
	for i := range want.records {
		if got.records[i] != want.records[i] {
			t.Fatalf("record descriptor %d = %#v, want %#v", i, got.records[i], want.records[i])
		}
	}
}

var benchmarkRecordLineSnapshotSink recordLineSnapshot

func BenchmarkRecordLineSetFrontier(b *testing.B) {
	const (
		recordCount = 8192
		middleStart = recordCount/2 - 8
		middleEnd   = recordCount/2 + 8
	)
	payload := strings.Repeat("record payload ", 16)
	var referenceBlob bytes.Buffer
	var targetBlob bytes.Buffer
	for i := 0; i < recordCount; i++ {
		status := "open"
		targetStatus := status
		if i >= middleStart && i < middleEnd {
			targetStatus = "done"
		}
		fmt.Fprintf(&referenceBlob, "{\"id\":\"bv-%06d\",\"status\":%q,\"title\":%q}\n", i, status, payload)
		fmt.Fprintf(&targetBlob, "{\"id\":\"bv-%06d\",\"status\":%q,\"title\":%q}\n", i, targetStatus, payload)
	}
	referenceBytes := referenceBlob.Bytes()
	targetBytes := targetBlob.Bytes()
	reference, _ := buildRecordLineSnapshot(referenceBytes, nil, hashRecordLine)

	b.Run("full", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(targetBytes)))
		var snapshot recordLineSnapshot
		var stats recordLineBuildStats
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			snapshot, stats = buildRecordLineSnapshot(targetBytes, nil, hashRecordLine)
		}
		b.StopTimer()
		benchmarkRecordLineSnapshotSink = snapshot
		b.ReportMetric(float64(stats.hashedBytes), "hashed_B/op")
		b.ReportMetric(float64(stats.reusedBytes), "reused_B/op")
	})

	b.Run("frontier", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(targetBytes)))
		var snapshot recordLineSnapshot
		var stats recordLineBuildStats
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			snapshot, stats = buildRecordLineSnapshot(targetBytes, &reference, hashRecordLine)
		}
		b.StopTimer()
		benchmarkRecordLineSnapshotSink = snapshot
		b.ReportMetric(float64(stats.hashedBytes), "hashed_B/op")
		b.ReportMetric(float64(stats.reusedBytes), "reused_B/op")
	})
}
