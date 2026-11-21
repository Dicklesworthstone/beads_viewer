package correlation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
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
	contentForStatus := func(status string) []byte {
		padding := strings.Repeat("x", 64-len(status))
		return []byte(fmt.Sprintf("{\"id\":\"bv-a\",\"status\":%q,\"title\":%q}\n", status, padding))
	}
	oldContent := contentForStatus("open")
	currentContent := contentForStatus("in_progress")
	nextContent := contentForStatus("closed")

	var protocol bytes.Buffer
	protocol.WriteString("missing-before-first-valid missing\n")
	for i, content := range [][]byte{oldContent, currentContent, nextContent} {
		fmt.Fprintf(&protocol, "oid-%d blob %d\n", i, len(content))
		protocol.Write(content)
		protocol.WriteByte('\n')
	}
	protocol.WriteString("missing-after-recycle missing\n")

	var requests bytes.Buffer
	reader := &blobReader{
		w:           bufio.NewWriter(&requests),
		out:         bufio.NewReaderSize(&protocol, blobReaderBufferSize),
		arenaRunway: blobReaderArenaRunway,
	}

	missing, err := reader.read("missing-before-first-valid")
	if err != nil {
		t.Fatalf("read missing blob before first valid blob: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing blob before first valid blob = %q, want nil", missing)
	}
	if reader.arenaRunway != blobReaderArenaRunway {
		t.Fatalf("missing blob consumed arena runway: got %d, want %d", reader.arenaRunway, blobReaderArenaRunway)
	}

	oldBlob, err := reader.read("oid-0")
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
	currentBlob, err := reader.read("oid-1")
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
	nextBlob, err := reader.read("oid-2")
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
	missing, err = reader.read("missing-after-recycle")
	if err != nil {
		t.Fatalf("read missing blob: %v", err)
	}
	if missing != nil {
		t.Fatalf("missing blob = %q, want nil", missing)
	}
	if cap(reader.spare) == 0 {
		t.Fatal("missing blob consumed the reusable buffer")
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
			if entry == nil || entry.count != 2 || string(entry.text) != tc.wantFirst {
				t.Fatalf("colliding entry = %#v, want count 2 first representative %q", entry, tc.wantFirst)
			}
			if stats.hashedRecords != 1 || stats.reusedRecords != 1 {
				t.Fatalf("collision frontier work = %#v, want one hashed and one reused record", stats)
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
		if gotEntry.count != wantEntry.count || !bytes.Equal(gotEntry.text, wantEntry.text) {
			t.Fatalf(
				"entry %d = count %d text %q, want count %d text %q",
				hash, gotEntry.count, gotEntry.text, wantEntry.count, wantEntry.text,
			)
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

func runGitMR7(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Alice Dev",
		"GIT_AUTHOR_EMAIL=Alice@Example.com",
		"GIT_COMMITTER_NAME=Alice Dev",
		"GIT_COMMITTER_EMAIL=Alice@Example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestMR7_FullDeletionParityAcrossPatchSnapshotAndCausal(t *testing.T) {
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	for _, tc := range []struct {
		name    string
		padSize int
	}{
		{name: "small_under_64kb", padSize: 64},
		{name: "large_over_64kb", padSize: 70 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			runGitMR7(t, repo, "init", "-q")
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
			pad := strings.Repeat("x", tc.padSize)

			// Commit 1: create bv-del1 + padding record
			line1 := fmt.Sprintf(`{"id":"bv-del1","title":"Will be deleted","status":"open","description":%q}`+"\n", pad)
			if err := os.WriteFile(jsonlPath, []byte(line1), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitMR7(t, repo, "add", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "create bv-del1")

			// Commit 2: claim bv-del1 (open -> in_progress)
			lineClaim := fmt.Sprintf(`{"id":"bv-del1","title":"Will be deleted","status":"in_progress","description":%q}`+"\n", pad)
			if err := os.WriteFile(jsonlPath, []byte(lineClaim), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitMR7(t, repo, "add", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "claim bv-del1")

			// Commit 3: close bv-del1 (in_progress -> closed)
			lineClose := fmt.Sprintf(`{"id":"bv-del1","title":"Will be deleted","status":"closed","description":%q}`+"\n", pad)
			if err := os.WriteFile(jsonlPath, []byte(lineClose), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitMR7(t, repo, "add", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "close bv-del1")

			// Commit 4: reopen bv-del1 (closed -> open)
			if err := os.WriteFile(jsonlPath, []byte(line1), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitMR7(t, repo, "add", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "reopen bv-del1")

			// Commit 5: full-file deletion of .beads/issues.jsonl
			runGitMR7(t, repo, "rm", "-q", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "delete issues.jsonl")

			// Commit 6: recreate .beads/issues.jsonl with bv-keep1
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			line3 := fmt.Sprintf(`{"id":"bv-keep1","title":"Recreated","status":"open","description":%q}`+"\n", pad)
			if err := os.WriteFile(jsonlPath, []byte(line3), 0o644); err != nil {
				t.Fatal(err)
			}
			runGitMR7(t, repo, "add", ".beads/issues.jsonl")
			runGitMR7(t, repo, "commit", "-q", "-m", "recreate issues.jsonl")

			e := NewExtractor(repo, ".beads/issues.jsonl")
			opts := ExtractOptions{Limit: 50}
			patchEvents, err := e.extractViaGitLogPatch(opts)
			if err != nil {
				t.Fatalf("extractViaGitLogPatch: %v", err)
			}
			snapEvents, err := e.extractViaSnapshots(opts)
			if err != nil {
				t.Fatalf("extractViaSnapshots: %v", err)
			}
			patchJSON, err := json.Marshal(patchEvents)
			if err != nil {
				t.Fatalf("marshal patchEvents: %v", err)
			}
			snapJSON, err := json.Marshal(snapEvents)
			if err != nil {
				t.Fatalf("marshal snapEvents: %v", err)
			}
			if string(patchJSON) != string(snapJSON) {
				t.Fatalf("MR-7 byte-identical []BeadEvent mismatch (padSize=%d):\npatch=%s\nsnap=%s", tc.padSize, patchJSON, snapJSON)
			}
			seenTypes := map[EventType]bool{}
			for _, ev := range snapEvents {
				if ev.BeadID == "bv-del1" {
					seenTypes[ev.EventType] = true
				}
			}
			for _, wantType := range []EventType{EventCreated, EventClaimed, EventClosed, EventReopened, EventDeleted} {
				if !seenTypes[wantType] {
					t.Fatalf("missing expected lifecycle event %q for bv-del1; got %+v", wantType, snapEvents)
				}
			}

			// Verify extractCausalHistory also observes the deletion commit for bv-del1.
			causal, err := e.extractCausalHistory("bv-del1", ExtractOptions{Limit: 50})
			if err != nil {
				t.Fatalf("extractCausalHistory: %v", err)
			}
			sawVanish := false
			for _, obs := range causal.Observations {
				if obs.Before.Issue != nil && obs.After.Issue == nil {
					sawVanish = true
				}
			}
			if !sawVanish {
				t.Fatalf("extractCausalHistory dropped full-file deletion observation for bv-del1: %+v", causal.Observations)
			}
		})
	}
}

func TestRC36_OrphanConfirmFeedbackAndPerPairRejectionAndEdgeFixes(t *testing.T) {
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	repo := t.TempDir()
	runGitMR7(t, repo, "init", "-q")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"id":"bv-a1","title":"Bead A1","status":"open"}`+"\n"+`{"id":"bv-b2","title":"Bead B2","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitMR7(t, repo, "add", ".beads/issues.jsonl")
	runGitMR7(t, repo, "commit", "-q", "-m", "seed beads")

	// Unlinked orphan code commit
	if err := os.WriteFile(filepath.Join(repo, "orphan.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitMR7(t, repo, "add", "orphan.go")
	runGitMR7(t, repo, "commit", "-q", "-m", "refactor helper without bead id")
	orphanSHA := runGitMR7(t, repo, "rev-parse", "HEAD")

	// Commit mentioning bv-b2 explicitly
	if err := os.WriteFile(filepath.Join(repo, "b2.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitMR7(t, repo, "add", "b2.go")
	runGitMR7(t, repo, "commit", "-q", "-m", "implement bv-b2 feature")
	b2SHA := runGitMR7(t, repo, "rev-parse", "HEAD")

	// Commit mentioning bv-a1 explicitly (which we will reject to test per-pair rejection isolation)
	if err := os.WriteFile(filepath.Join(repo, "a1.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitMR7(t, repo, "add", "a1.go")
	runGitMR7(t, repo, "commit", "-q", "-m", "false match bv-a1")
	a1SHA := runGitMR7(t, repo, "rev-parse", "HEAD")

	beads := []BeadInfo{
		{ID: "bv-a1", Title: "Bead A1", Status: "open"},
		{ID: "bv-b2", Title: "Bead B2", Status: "open"},
	}

	// Verify ResolveCommitInRepo resolves real orphan commit and rejects bogus SHA
	resolved, err := ResolveCommitInRepo(context.Background(), repo, orphanSHA[:8])
	if err != nil || resolved.SHA != orphanSHA || len(resolved.Files) != 1 || resolved.Files[0].Path != "orphan.go" {
		t.Fatalf("ResolveCommitInRepo(%s) = %+v, err=%v", orphanSHA[:8], resolved, err)
	}
	if _, err := ResolveCommitInRepo(context.Background(), repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); err == nil {
		t.Fatal("ResolveCommitInRepo accepted nonexistent commit SHA")
	}

	// Confirm orphanSHA -> bv-a1 and reject a1SHA -> bv-a1
	fbStore := NewFeedbackStore(beadsDir)
	if err := fbStore.Confirm(orphanSHA, "bv-a1", "tester", 0.0, "linked orphan"); err != nil {
		t.Fatal(err)
	}
	if err := fbStore.Reject(a1SHA, "bv-a1", "tester", 0.85, "false positive"); err != nil {
		t.Fatal(err)
	}

	report, err := NewCorrelator(repo, ".beads/issues.jsonl").WithFeedbackStore(fbStore).GenerateReport(beads, CorrelatorOptions{Limit: 50})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// orphanSHA must now appear in bv-a1 history (Confidence 1.0, Confirmed true) and CommitIndex
	foundConfirmed := false
	for _, c := range report.Histories["bv-a1"].Commits {
		if c.SHA == orphanSHA && c.Confirmed && c.Confidence == 1.0 {
			foundConfirmed = true
		}
		if c.SHA == a1SHA {
			t.Fatalf("rejected commit %s still present in bv-a1 history", a1SHA)
		}
	}
	if !foundConfirmed {
		t.Fatalf("confirmed orphan %s not synthesized into bv-a1 commits: %+v", orphanSHA, report.Histories["bv-a1"].Commits)
	}
	if len(report.CommitIndex[orphanSHA]) != 1 || report.CommitIndex[orphanSHA][0] != "bv-a1" {
		t.Fatalf("CommitIndex[%s] = %v, want [bv-a1]", orphanSHA, report.CommitIndex[orphanSHA])
	}

	// ReverseLookup: orphanSHA is no longer orphan; a1SHA IS orphan (rejected); b2SHA is NOT orphan (per-pair isolation!)
	// Even if we delete b2SHA from CommitIndex to force explicit fallback, rejecting (a1SHA, bv-a1) must NOT disable explicit fallback for b2SHA!
	delete(report.CommitIndex, b2SHA)
	rl := NewReverseLookupWithRepo(report, repo)
	orphans, _, err := rl.FindOrphanCommits(ExtractOptions{Limit: 50})
	if err != nil {
		t.Fatalf("FindOrphanCommits: %v", err)
	}
	orphanSet := make(map[string]bool, len(orphans))
	for _, o := range orphans {
		orphanSet[o.SHA] = true
	}
	if orphanSet[orphanSHA] {
		t.Fatalf("confirmed orphan %s still flagged as orphan", orphanSHA)
	}
	if !orphanSet[a1SHA] {
		t.Fatalf("rejected commit %s should appear as orphan", a1SHA)
	}
	if orphanSet[b2SHA] {
		t.Fatalf("rejecting (a1SHA, bv-a1) globally disabled explicit fallback for unrelated commit %s", b2SHA)
	}

	// Verify OrphanDetector.checkTiming reports actual closed status & case-insensitive email match
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	closedStart := now.Add(-4 * time.Hour)
	closedEnd := now.Add(-1 * time.Hour)
	odReport := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-closed": {
				BeadID:     "bv-closed",
				Title:      "Closed Bead",
				Status:     "closed",
				LastAuthor: "Alice",
				Milestones: BeadMilestones{
					Claimed: &BeadEvent{Timestamp: closedStart},
					Closed:  &BeadEvent{Timestamp: closedEnd},
				},
				Commits: []CorrelatedCommit{
					{SHA: "1111111", Author: "Alice", AuthorEmail: "Alice@Example.COM", Timestamp: closedStart.Add(time.Minute)},
				},
			},
		},
		CommitIndex: CommitIndex{"1111111": {"bv-closed"}},
	}
	od := NewOrphanDetector(odReport, "")
	cand, err := od.analyzeOrphan(OrphanCommit{
		SHA:         "2222222",
		Message:     "follow-up work",
		Author:      "alice",
		AuthorEmail: "alice@example.com", // lowercase vs stored Alice@Example.COM
		Timestamp:   closedStart.Add(30 * time.Minute),
		Files:       []string{"main.go"},
	})
	if err != nil {
		t.Fatalf("analyzeOrphan: %v", err)
	}
	if len(cand.ProbableBeads) != 1 || cand.ProbableBeads[0].BeadStatus != "closed" {
		t.Fatalf("expected closed ProbableBead status (not hardcoded in_progress), got %+v", cand.ProbableBeads)
	}
	sawAuthorSignal := false
	for _, sig := range cand.Signals {
		if sig.Signal == SignalOrphanAuthor {
			sawAuthorSignal = true
		}
	}
	if !sawAuthorSignal {
		t.Fatalf("expected case-insensitive author match signal, got %+v", cand.Signals)
	}

	// Verify related.go runs commit_overlap before file_overlap and discovers sibling blockers
	relReport := &HistoryReport{
		Histories: map[string]BeadHistory{
			"T": {BeadID: "T", Status: "open", Commits: []CorrelatedCommit{{SHA: "shared1", Files: []FileChange{{Path: "a.go"}}}}},
			"S": {BeadID: "S", Status: "open", Commits: []CorrelatedCommit{{SHA: "shared1", Files: []FileChange{{Path: "a.go"}}}}},
			"P": {BeadID: "P", Status: "open"},
			"B": {BeadID: "B", Status: "open"},
		},
		CommitIndex: CommitIndex{"shared1": {"T", "S"}},
	}
	relOpts := DefaultRelatedWorkOptions()
	relOpts.DependencyGraph = map[string][]string{
		"T": {"P"},
		"B": {"P"}, // B is a sibling of T sharing blocker P!
	}
	rel := relReport.FindRelatedWork("T", relOpts)
	if len(rel.CommitOverlap) != 1 || rel.CommitOverlap[0].BeadID != "S" {
		t.Fatalf("expected S in CommitOverlap before FileOverlap, got commit=%+v file=%+v", rel.CommitOverlap, rel.FileOverlap)
	}
	foundSibling := false
	for _, d := range rel.DependencyCluster {
		if d.BeadID == "B" {
			foundSibling = true
		}
	}
	if !foundSibling {
		t.Fatalf("expected shared-blocker sibling B in DependencyCluster, got %+v", rel.DependencyCluster)
	}

	// Verify scorer.go MethodCoCommitted min 0.80 and no fabricated SignalProximity
	scorer := NewScorer()
	if !scorer.ValidateConfidence(MethodCoCommitted, 0.80) {
		t.Fatal("ValidateConfidence(MethodCoCommitted, 0.80) = false, want true")
	}
	expl := scorer.BuildExplanation(CorrelatedCommit{
		SHA:        "abc1234",
		Method:     MethodCoCommitted,
		Confidence: 0.95,
		Reason:     "co-committed with bead update",
	}, "bv-a1")
	for _, sig := range expl.Signals {
		if sig.Type == SignalProximity {
			t.Fatalf("fabricated SignalProximity without adjacency evidence: %+v", expl.Signals)
		}
	}

	// Verify bounded cache file reader rejects oversized file without OOM
	tmpFile, err := os.CreateTemp(t.TempDir(), "oversize-cache-*")
	if err != nil {
		t.Fatal(err)
	}
	defer tmpFile.Close()
	if _, err := tmpFile.Write([]byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCacheFileBounded(tmpFile, 4); ok {
		t.Fatal("readCacheFileBounded accepted file exceeding maxBytes")
	}
}
