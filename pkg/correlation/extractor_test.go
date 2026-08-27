package correlation

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type closeFailingSnapshotBlobReader struct {
	blobs      map[string][]byte
	closeErr   error
	reads      int
	closeCalls int
}

func (r *closeFailingSnapshotBlobReader) read(sha string) ([]byte, error) {
	r.reads++
	blob, ok := r.blobs[sha]
	if !ok {
		return nil, errors.New("unexpected blob object ID")
	}
	return append([]byte(nil), blob...), nil
}

func (*closeFailingSnapshotBlobReader) recycle([]byte) {}

func (r *closeFailingSnapshotBlobReader) Close() error {
	r.closeCalls++
	return r.closeErr
}

func TestParseGitLogOutput(t *testing.T) {
	// Mock git log output with two commits
	data := []byte(`abc123def456789012345678901234567890abcd` + "\x00" + `2025-01-15T10:00:00Z` + "\x00" + `Alice` + "\x00" + `alice@example.com` + "\x00" + `First commit

diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
+{"id":"bv-001","title":"First bead","status":"open"}
def456789012345678901234567890abcdef1234` + "\x00" + `2025-01-16T11:00:00Z` + "\x00" + `Bob` + "\x00" + `bob@example.com` + "\x00" + `Second commit

diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
-{"id":"bv-001","title":"First bead","status":"open"}
+{"id":"bv-001","title":"First bead","status":"in_progress"}
`)

	e := NewExtractor("/tmp/test", "")
	events, err := e.parseGitLogOutput(bytes.NewReader(data), "")
	if err != nil {
		t.Fatalf("parseGitLogOutput failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("Expected 2 events, got %d", len(events))
	}

	// Check first event (parsed from second commit because of reverse order in git log? No, parseDiff returns in order, but Extract reverses at the end. Here we just call parseGitLogOutput)
	// Wait, parseGitLogOutput returns events in order of occurrence in the log (newest first usually).
	// The mock data has commit 1 then commit 2. Usually git log is newest first.
	// But let's check the content.

	// The first chunk in data is commit "abc...", timestamp 10:00. EventCreated.
	// The second chunk is commit "def...", timestamp 11:00. EventClaimed.

	// events[0] corresponds to the first chunk parsed.
	if events[0].EventType != EventCreated {
		t.Errorf("First event should be Created, got %v", events[0].EventType)
	}
	if events[0].CommitSHA != "abc123def456789012345678901234567890abcd" {
		t.Errorf("First event SHA mismatch")
	}

	if events[1].EventType != EventClaimed {
		t.Errorf("Second event should be Claimed, got %v", events[1].EventType)
	}
}

func TestParseCommitInfo(t *testing.T) {

	line := "abc123def456789012345678901234567890abcd" + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "Alice Smith" + "\x00" + "alice@example.com" + "\x00" + "feat: add login feature"

	info, err := parseCommitInfo(line)

	if err != nil {
		t.Fatalf("parseCommitInfo failed: %v", err)
	}

	if info.SHA != "abc123def456789012345678901234567890abcd" {
		t.Errorf("SHA mismatch: got %s", info.SHA)
	}
	if info.Author != "Alice Smith" {
		t.Errorf("Author mismatch: got %s", info.Author)
	}
	if info.AuthorEmail != "alice@example.com" {
		t.Errorf("AuthorEmail mismatch: got %s", info.AuthorEmail)
	}
	if info.Message != "feat: add login feature" {
		t.Errorf("Message mismatch: got %s", info.Message)
	}

	expectedTime, _ := time.Parse(time.RFC3339, "2025-01-15T10:30:00Z")
	if !info.Timestamp.Equal(expectedTime) {
		t.Errorf("Timestamp mismatch: got %v, want %v", info.Timestamp, expectedTime)
	}
}

func TestParseCommitInfoSupportsSHA256(t *testing.T) {
	sha := strings.Repeat("a", 64)
	line := sha + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "Alice" + "\x00" + "alice@example.com" + "\x00" + "SHA-256 commit"
	info, err := parseCommitInfo(line)
	if err != nil {
		t.Fatalf("parse SHA-256 commit info: %v", err)
	}
	if info.SHA != sha || !commitPattern.MatchString(line) {
		t.Fatalf("SHA-256 header was not preserved: info=%+v matched=%t", info, commitPattern.MatchString(line))
	}
}

func TestParseCommitInfo_InvalidFormat(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"missing parts", "abc123def456789012345678901234567890abcd" + "\x00" + "2025-01-15"},
		{"invalid timestamp", "abc123def456789012345678901234567890abcd" + "\x00" + "not-a-date" + "\x00" + "author" + "\x00" + "email" + "\x00" + "msg"},
		{"noncanonical object ID", strings.Repeat("a", 63) + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "author" + "\x00" + "email" + "\x00" + "msg"},
		{"uppercase object ID", strings.Repeat("A", 64) + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "author" + "\x00" + "email" + "\x00" + "msg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseCommitInfo(tt.line)
			if err == nil {
				t.Error("Expected error for invalid input")
			}
		})
	}
}

func TestParseGitLogOutputRejectsOversizedLineWithoutPartialEvents(t *testing.T) {
	e := NewExtractor(t.TempDir())
	sha := strings.Repeat("a", 40)
	header := sha + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "Alice" + "\x00" + "alice@example.com" + "\x00" + "oversized"
	record := "{\"id\":\"oversized\",\"status\":\"open\",\"title\":\"" + strings.Repeat("x", 10*1024*1024) + "\"}"
	input := header + "\n+" + record + "\n"
	events, err := e.parseGitLogOutput(strings.NewReader(input), "")
	if err == nil || events != nil {
		t.Fatalf("oversized lifecycle line result=%#v error=%v, want nil/error", events, err)
	}
	if !strings.Contains(err.Error(), "parser limit") {
		t.Fatalf("oversized lifecycle line error=%v, want parser-limit context", err)
	}
	info, err := parseCommitInfo(header)
	if err != nil {
		t.Fatalf("parse oversized fixture header: %v", err)
	}
	directEvents := e.parseDiff([]byte("+"+record+"\n"), info, "")
	if len(directEvents) != 1 || directEvents[0].BeadID != "oversized" || directEvents[0].EventType != EventCreated {
		t.Fatalf("snapshot-sized record parse=%#v, want one created event", directEvents)
	}
}

func TestParseGitLogOutputRejectsMixedObjectIDWidths(t *testing.T) {
	header := func(sha string) string {
		return sha + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "Alice" + "\x00" + "alice@example.com" + "\x00" + "mixed"
	}
	input := header(strings.Repeat("a", 40)) + "\n" + header(strings.Repeat("b", 64)) + "\n"
	events, err := NewExtractor(t.TempDir()).parseGitLogOutput(strings.NewReader(input), "")
	if err == nil || events != nil {
		t.Fatalf("mixed-width lifecycle stream result=%#v error=%v, want nil/error", events, err)
	}
}

func TestLifecycleGitPolicyPinsConfigAndAttributeSensitiveInputs(t *testing.T) {
	policyArgs := append(append(lifecycleGitConfigArgs(), lifecycleGitLogOutputArgs()...), lifecycleHistoryOrderArgs()...)
	joined := strings.Join(append(policyArgs, lifecycleGitDiffArgs()...), "\x00")
	for _, want := range []string{
		"core.quotePath=true",
		"diff.renames=true",
		"diff.renameLimit=1000",
		"diff.algorithm=default",
		"diff.indentHeuristic=false",
		"i18n.logOutputEncoding=UTF-8",
		"log.follow=false",
		"--encoding=UTF-8",
		"--no-use-mailmap",
		"--no-abbrev-commit",
		"--no-expand-tabs",
		"--no-show-signature",
		"--no-decorate",
		"--no-notes",
		"--root",
		"--topo-order",
		"--find-renames=50%",
		"-l1000",
		"--no-rename-empty",
		"--diff-algorithm=default",
		"--no-indent-heuristic",
		"--no-ext-diff",
		"--no-textconv",
		"--text",
		"--no-diff-merges",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("lifecycle Git policy omitted %q: %q", want, joined)
		}
	}
	if !strings.Contains(strings.Join(lifecycleGitPolicyNamespaceInputs(), "\x00"), lifecycleGitPolicyVersion) {
		t.Fatal("persistent-cache policy identity omitted the lifecycle policy version")
	}
}

func TestLifecycleExtractionIgnoresHostileSameHeadGitConfig(t *testing.T) {
	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte(".beads/*.jsonl -diff\n"), 0o644); err != nil {
		t.Fatalf("write hostile attributes: %v", err)
	}
	oldPath := filepath.Join(beadsDir, "beads.jsonl")
	if err := os.WriteFile(oldPath, []byte("{\"id\":\"policy-bead\",\"title\":\"Policy\",\"status\":\"open\"}\n"), 0o644); err != nil {
		t.Fatalf("write initial beads history: %v", err)
	}
	runGit(t, repo, "add", ".gitattributes", ".beads/beads.jsonl")
	runGit(t, repo, "commit", "-m", "create policy bead")

	runGit(t, repo, "mv", ".beads/beads.jsonl", ".beads/issues.jsonl")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"policy-bead\",\"title\":\"Policy\",\"status\":\"closed\"}\n"), 0o644); err != nil {
		t.Fatalf("close renamed bead: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "close café policy bead")
	headBefore, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve policy fixture HEAD: %v", err)
	}

	extract := func(t *testing.T) map[string][]BeadEvent {
		t.Helper()
		e := NewExtractor(repo)
		results := make(map[string][]BeadEvent, 2)
		for label, fn := range map[string]func(ExtractOptions) ([]BeadEvent, error){
			"legacy":   e.extractViaGitLogPatch,
			"snapshot": e.extractViaSnapshots,
		} {
			events, extractErr := fn(ExtractOptions{})
			if extractErr != nil {
				t.Fatalf("%s lifecycle extraction: %v", label, extractErr)
			}
			if len(events) != 2 || events[0].EventType != EventCreated || events[1].EventType != EventClosed || events[1].CommitMsg != "close café policy bead" {
				t.Fatalf("%s lifecycle events=%#v", label, events)
			}
			results[label] = events
		}
		return results
	}

	baseline := extract(t)
	for key, value := range map[string]string{
		"core.bigFileThreshold":  "1",
		"diff.algorithm":         "histogram",
		"diff.indentHeuristic":   "true",
		"diff.renameLimit":       "1",
		"diff.renames":           "false",
		"i18n.logOutputEncoding": "ISO-8859-1",
		"log.decorate":           "full",
		"log.follow":             "true",
		"log.showRoot":           "false",
		"log.showSignature":      "true",
	} {
		runGit(t, repo, "config", key, value)
	}
	headAfter, err := getGitHead(repo)
	if err != nil || headAfter != headBefore {
		t.Fatalf("hostile config changed HEAD: before=%q after=%q error=%v", headBefore, headAfter, err)
	}
	got := extract(t)
	if !reflect.DeepEqual(got, baseline) {
		t.Fatalf("same-HEAD hostile Git config changed lifecycle extraction:\n got=%#v\nwant=%#v", got, baseline)
	}
}

func TestSnapshotExtractionDoesNotPublishBeforeBlobReaderExit(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := []byte("{\"id\":\"close-failure\",\"title\":\"Close failure\",\"status\":\"open\"}\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), content, 0o644); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add snapshot close fixture")

	extractor := NewExtractor(repo)
	commits, err := extractor.snapshotCommits(ExtractOptions{})
	if err != nil {
		t.Fatalf("resolve snapshot fixture commits: %v", err)
	}
	if len(commits) != 1 || commits[0].oldSHA != "" || commits[0].newSHA == "" {
		t.Fatalf("snapshot fixture commits=%+v, want one root addition", commits)
	}
	injectedErr := errors.New("injected cat-file exit failure")
	fake := &closeFailingSnapshotBlobReader{
		blobs:    map[string][]byte{commits[0].newSHA: content},
		closeErr: injectedErr,
	}
	extractor.blobReaderFactory = func() (snapshotBlobReadCloser, error) {
		return fake, nil
	}

	events, err := extractor.extractViaSnapshots(ExtractOptions{})
	if !errors.Is(err, injectedErr) || events != nil {
		t.Fatalf("snapshot close failure result=%#v error=%v, want nil/wrapped injected error", events, err)
	}
	if fake.reads != 1 {
		t.Fatalf("fake delivered %d blob responses, want 1", fake.reads)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("blob reader Close called %d times, want exactly once", fake.closeCalls)
	}
	namespace := perCommitEventCacheNamespace(extractor.primaryBeadsFile(), "")
	if cached := loadPerCommitEvents(namespace); len(cached) != 0 {
		t.Fatalf("failed cat-file process published per-commit events: %#v", cached)
	}
}

func TestSnapshotExtractionRejectsMissingNonemptyBlobWithoutPublishing(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := []byte("{\"id\":\"missing-blob\",\"title\":\"Missing blob\",\"status\":\"open\"}\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), content, 0o644); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add missing blob fixture")

	extractor := NewExtractor(repo)
	commits, err := extractor.snapshotCommits(ExtractOptions{})
	if err != nil {
		t.Fatalf("resolve snapshot fixture commits: %v", err)
	}
	if len(commits) != 1 || commits[0].newSHA == "" {
		t.Fatalf("snapshot fixture commits=%+v, want one nonempty new blob", commits)
	}
	fake := &closeFailingSnapshotBlobReader{
		blobs: map[string][]byte{commits[0].newSHA: nil},
	}
	extractor.blobReaderFactory = func() (snapshotBlobReadCloser, error) {
		return fake, nil
	}

	events, err := extractor.extractViaSnapshots(ExtractOptions{})
	if err == nil || events != nil || !strings.Contains(err.Error(), "no content for nonempty blob") {
		t.Fatalf("missing blob result=%#v error=%v, want nil/error", events, err)
	}
	if fake.closeCalls != 1 {
		t.Fatalf("missing-blob reader Close called %d times, want exactly once", fake.closeCalls)
	}
	namespace := perCommitEventCacheNamespace(extractor.primaryBeadsFile(), "")
	if cached := loadPerCommitEvents(namespace); len(cached) != 0 {
		t.Fatalf("missing blob published per-commit events: %#v", cached)
	}
}

func TestSnapshotExtractionPureCacheHitDoesNotStartBlobReader(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := []byte("{\"id\":\"cached\",\"title\":\"Cached\",\"status\":\"open\"}\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), content, 0o644); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add cached snapshot fixture")

	extractor := NewExtractor(repo)
	want, err := extractor.extractViaSnapshots(ExtractOptions{})
	if err != nil {
		t.Fatalf("prime per-commit snapshot cache: %v", err)
	}
	factoryCalls := 0
	injectedErr := errors.New("pure cache hit started blob reader")
	extractor.blobReaderFactory = func() (snapshotBlobReadCloser, error) {
		factoryCalls++
		return nil, injectedErr
	}

	got, err := extractor.extractViaSnapshots(ExtractOptions{})
	if err != nil {
		t.Fatalf("pure per-commit cache hit: %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("pure cache hit started blob reader %d times", factoryCalls)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pure cache hit events=%#v, want %#v", got, want)
	}
}

func TestSnapshotExtractionEmptyContributionIsDefinitiveCacheHit(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	content := []byte("{\"id\":\"other\",\"title\":\"Other\",\"status\":\"open\"}\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), content, 0o644); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add unrelated snapshot fixture")

	opts := ExtractOptions{BeadID: "target"}
	extractor := NewExtractor(repo)
	if events, err := extractor.extractViaSnapshots(opts); err != nil || len(events) != 0 {
		t.Fatalf("prime empty per-commit contribution: events=%#v err=%v", events, err)
	}
	factoryCalls := 0
	injectedErr := errors.New("empty cache hit started blob reader")
	extractor.blobReaderFactory = func() (snapshotBlobReadCloser, error) {
		factoryCalls++
		return nil, injectedErr
	}

	events, err := extractor.extractViaSnapshots(opts)
	if err != nil {
		t.Fatalf("empty per-commit cache hit: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("empty per-commit cache hit returned events: %#v", events)
	}
	if factoryCalls != 0 {
		t.Fatalf("empty per-commit cache hit started blob reader %d times", factoryCalls)
	}
}

func TestParseSnapshotLogSupportsSHA256AndRejectsMixedObjectIDWidths(t *testing.T) {
	commitSHA := strings.Repeat("a", 64)
	newBlobSHA := strings.Repeat("b", 64)
	header := commitSHA + "\x00" + "2025-01-15T10:30:00Z" + "\x00" + "Alice" + "\x00" + "alice@example.com" + "\x00" + "snapshot"
	valid := []byte(header + "\n\n:000000 100644 " + strings.Repeat("0", 64) + " " + newBlobSHA + " A\t.beads/issues.jsonl\n")
	commits, err := parseSnapshotLog(valid)
	if err != nil {
		t.Fatalf("parse SHA-256 snapshot log: %v", err)
	}
	if len(commits) != 1 || commits[0].info.SHA != commitSHA || commits[0].oldSHA != "" || commits[0].newSHA != newBlobSHA {
		t.Fatalf("SHA-256 snapshot parse=%+v", commits)
	}

	mixed := []byte(header + "\n\n:000000 100644 " + strings.Repeat("0", 40) + " " + strings.Repeat("b", 40) + " A\t.beads/issues.jsonl\n")
	if commits, err := parseSnapshotLog(mixed); err == nil || commits != nil {
		t.Fatalf("mixed-width snapshot result=%+v error=%v, want nil/error", commits, err)
	}
}

func TestParseBeadJSON(t *testing.T) {
	tests := []struct {
		name   string
		json   string
		wantID string
		wantOK bool
	}{
		{
			name:   "valid bead",
			json:   `{"id":"bv-123","title":"Test","status":"open"}`,
			wantID: "bv-123",
			wantOK: true,
		},
		{
			name:   "valid bead with extra fields",
			json:   `{"id":"bv-456","title":"Feature","status":"closed","priority":1,"labels":["urgent"]}`,
			wantID: "bv-456",
			wantOK: true,
		},
		{
			name:   "missing id",
			json:   `{"title":"No ID","status":"open"}`,
			wantID: "",
			wantOK: false,
		},
		{
			name:   "invalid json",
			json:   `{not valid json}`,
			wantID: "",
			wantOK: false,
		},
		{
			name:   "empty object",
			json:   `{}`,
			wantID: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap, ok := parseBeadJSON(tt.json)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && snap.ID != tt.wantID {
				t.Errorf("ID = %s, want %s", snap.ID, tt.wantID)
			}
		})
	}
}

func TestDetermineStatusEvent(t *testing.T) {
	tests := []struct {
		oldStatus string
		newStatus string
		want      EventType
	}{
		{"open", "in_progress", EventClaimed},
		{"in_progress", "closed", EventClosed},
		{"open", "closed", EventClosed},
		{"closed", "open", EventReopened},
		{"closed", "in_progress", EventReopened},
		{"open", "tombstone", EventClosed},
		{"in_progress", " tombstone ", EventClosed},
		{"tombstone", "open", EventReopened},
		{"TOMBSTONE", " In_Progress ", EventReopened},
		{"open", "blocked", EventModified},
		{"in_progress", "open", EventModified},
	}

	for _, tt := range tests {
		t.Run(tt.oldStatus+"->"+tt.newStatus, func(t *testing.T) {
			got := determineStatusEvent(tt.oldStatus, tt.newStatus)
			if got != tt.want {
				t.Errorf("determineStatusEvent(%s, %s) = %v, want %v", tt.oldStatus, tt.newStatus, got, tt.want)
			}
		})
	}
}

func TestReverseEvents(t *testing.T) {
	events := []BeadEvent{
		{BeadID: "a", EventType: EventCreated},
		{BeadID: "b", EventType: EventClaimed},
		{BeadID: "c", EventType: EventClosed},
	}

	reverseEvents(events)

	if events[0].BeadID != "c" || events[1].BeadID != "b" || events[2].BeadID != "a" {
		t.Errorf("reverseEvents failed: got %v, %v, %v", events[0].BeadID, events[1].BeadID, events[2].BeadID)
	}
}

func TestGetBeadMilestones(t *testing.T) {
	now := time.Now()
	events := []BeadEvent{
		{BeadID: "bv-1", EventType: EventCreated, Timestamp: now},
		{BeadID: "bv-1", EventType: EventClaimed, Timestamp: now.Add(time.Hour)},
		{BeadID: "bv-1", EventType: EventClosed, Timestamp: now.Add(2 * time.Hour)},
		{BeadID: "bv-1", EventType: EventReopened, Timestamp: now.Add(3 * time.Hour)},
		{BeadID: "bv-1", EventType: EventClosed, Timestamp: now.Add(4 * time.Hour)},
	}

	milestones := GetBeadMilestones(events)

	if milestones.Created == nil {
		t.Error("Created should not be nil")
	}
	if milestones.Claimed == nil {
		t.Error("Claimed should not be nil")
	}
	if milestones.Closed == nil {
		t.Error("Closed should not be nil")
	}
	if milestones.Reopened == nil {
		t.Error("Reopened should not be nil")
	}

	// Check that Closed is the latest close event
	if !milestones.Closed.Timestamp.Equal(now.Add(4 * time.Hour)) {
		t.Error("Closed should be the latest close event")
	}
}

func TestCalculateCycleTime(t *testing.T) {
	now := time.Now()
	created := BeadEvent{EventType: EventCreated, Timestamp: now}
	claimed := BeadEvent{EventType: EventClaimed, Timestamp: now.Add(24 * time.Hour)}
	closed := BeadEvent{EventType: EventClosed, Timestamp: now.Add(48 * time.Hour)}

	t.Run("with all milestones", func(t *testing.T) {
		milestones := BeadMilestones{
			Created: &created,
			Claimed: &claimed,
			Closed:  &closed,
		}

		ct := CalculateCycleTime(milestones)

		if ct == nil {
			t.Fatal("CycleTime should not be nil")
		}
		if ct.ClaimToClose == nil {
			t.Error("ClaimToClose should not be nil")
		}
		if ct.CreateToClose == nil {
			t.Error("CreateToClose should not be nil")
		}
		if ct.CreateToClaim == nil {
			t.Error("CreateToClaim should not be nil")
		}

		expectedClaimToClose := 24 * time.Hour
		if *ct.ClaimToClose != expectedClaimToClose {
			t.Errorf("ClaimToClose = %v, want %v", *ct.ClaimToClose, expectedClaimToClose)
		}
	})

	t.Run("without closed milestone", func(t *testing.T) {
		milestones := BeadMilestones{
			Created: &created,
			Claimed: &claimed,
		}

		ct := CalculateCycleTime(milestones)

		if ct != nil {
			t.Error("CycleTime should be nil for unclosed beads")
		}
	})
}

func TestInsertBefore(t *testing.T) {
	slice := []string{"a", "b", "--", "c", "d"}

	result := insertBefore(slice, "--", "x")

	expected := []string{"a", "b", "x", "--", "c", "d"}
	if len(result) != len(expected) {
		t.Fatalf("length mismatch: got %d, want %d", len(result), len(expected))
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %s, want %s", i, result[i], v)
		}
	}
}

func TestInsertBefore_NoMarker(t *testing.T) {
	slice := []string{"a", "b", "c"}

	result := insertBefore(slice, "--", "x")

	// Should return original slice unchanged
	if len(result) != len(slice) {
		t.Errorf("length changed when marker not found: got %d, want %d", len(result), len(slice))
	}
}

func TestBuildGitLogArgs(t *testing.T) {
	e := NewExtractor("/test/repo", "")

	t.Run("basic args", func(t *testing.T) {
		args := e.buildGitLogArgs(ExtractOptions{})

		// Should contain -p and --format; --follow is valid because the
		// extractor appends exactly one primary beads pathspec.
		foundP := false
		for _, arg := range args {
			if arg == "-p" {
				foundP = true
			}
		}
		if !foundP {
			t.Error("missing -p flag")
		}
	})

	t.Run("with limit", func(t *testing.T) {
		args := e.buildGitLogArgs(ExtractOptions{Limit: 10})

		found := false
		for _, arg := range args {
			if arg == "-n10" {
				found = true
				break
			}
		}
		if !found {
			t.Error("missing -n10 flag")
		}
	})

	t.Run("with time filters", func(t *testing.T) {
		since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		until := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)

		args := e.buildGitLogArgs(ExtractOptions{
			Since: &since,
			Until: &until,
		})

		foundSince := false
		foundUntil := false
		for _, arg := range args {
			if len(arg) > 8 && arg[:8] == "--since=" {
				foundSince = true
			}
			if len(arg) > 8 && arg[:8] == "--until=" {
				foundUntil = true
			}
		}
		if !foundSince {
			t.Error("missing --since flag")
		}
		if !foundUntil {
			t.Error("missing --until flag")
		}
	})
}

func TestIsIgnorableDiffMetadataLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"", true},
		{"@@ -1 +1 @@", true},
		{"diff --git a/file b/file", true},
		{"index 123..456", true},
		{"new file mode 100644", true},
		{`+{"id":"bv-1","status":"open"}`, false},
		{`-{"id":"bv-1","status":"closed"}`, false},
		{" context", false},
	}

	for _, tt := range tests {
		if got := isIgnorableDiffMetadataLine(tt.line); got != tt.want {
			t.Fatalf("isIgnorableDiffMetadataLine(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

// TestParseDiff tests the diff parsing logic with mock data
func TestParseDiff(t *testing.T) {
	e := NewExtractor("/test/repo", "")

	info := commitInfo{
		SHA:         "abc123",
		Timestamp:   time.Now(),
		Author:      "Test",
		AuthorEmail: "test@test.com",
		Message:     "Test commit",
	}

	t.Run("new bead creation", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
+{"id":"bv-new","title":"New bead","status":"open"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if events[0].EventType != EventCreated {
			t.Errorf("Expected EventCreated, got %v", events[0].EventType)
		}
		if events[0].BeadID != "bv-new" {
			t.Errorf("Expected bv-new, got %s", events[0].BeadID)
		}
	})

	t.Run("status change to in_progress", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
-{"id":"bv-123","title":"Test","status":"open"}
+{"id":"bv-123","title":"Test","status":"in_progress"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if events[0].EventType != EventClaimed {
			t.Errorf("Expected EventClaimed, got %v", events[0].EventType)
		}
	})

	t.Run("status change to closed", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
-{"id":"bv-123","title":"Test","status":"in_progress"}
+{"id":"bv-123","title":"Test","status":"closed"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if events[0].EventType != EventClosed {
			t.Errorf("Expected EventClosed, got %v", events[0].EventType)
		}
	})

	t.Run("reopen closed bead", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
--- a/.beads/beads.jsonl
+++ b/.beads/beads.jsonl
-{"id":"bv-123","title":"Test","status":"closed"}
+{"id":"bv-123","title":"Test","status":"open"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if events[0].EventType != EventReopened {
			t.Errorf("Expected EventReopened, got %v", events[0].EventType)
		}
	})

	t.Run("filter by bead ID", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
+{"id":"bv-001","title":"First","status":"open"}
+{"id":"bv-002","title":"Second","status":"open"}
`)

		events := e.parseDiff(diffData, info, "bv-001")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event (filtered), got %d", len(events))
		}
		if events[0].BeadID != "bv-001" {
			t.Errorf("Expected bv-001, got %s", events[0].BeadID)
		}
	})

	t.Run("multiple beads in one commit", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
+{"id":"bv-001","title":"First","status":"open"}
+{"id":"bv-002","title":"Second","status":"open"}
+{"id":"bv-003","title":"Third","status":"open"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 3 {
			t.Fatalf("Expected 3 events, got %d", len(events))
		}
	})

	t.Run("malformed JSON skipped", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
+{"id":"bv-good","title":"Good","status":"open"}
+{malformed json here}
+{"id":"bv-also-good","title":"Also Good","status":"open"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 2 {
			t.Fatalf("Expected 2 events (skipping malformed), got %d", len(events))
		}
	})

	t.Run("modification without status change", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
-{"id":"bv-123","title":"Old Title","status":"open"}
+{"id":"bv-123","title":"New Title","status":"open"}
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 1 {
			t.Fatalf("Expected 1 event, got %d", len(events))
		}
		if events[0].EventType != EventModified {
			t.Errorf("Expected EventModified, got %v", events[0].EventType)
		}
	})

	t.Run("empty diff", func(t *testing.T) {
		diffData := []byte(`diff --git a/.beads/beads.jsonl b/.beads/beads.jsonl
`)

		events := e.parseDiff(diffData, info, "")

		if len(events) != 0 {
			t.Errorf("Expected 0 events for empty diff, got %d", len(events))
		}
	})
}

func TestNewExtractor(t *testing.T) {
	e := NewExtractor("/tmp/test", "")

	if e.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", e.repoPath)
	}
	if len(e.beadsFiles) == 0 {
		t.Error("beadsFiles should not be empty")
	}
}

func writeHistorySelectionFiles(t *testing.T, repoPath string) string {
	t.Helper()

	beadsDir := filepath.Join(repoPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(`{"id":"canonical"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

func TestNewExtractorPrefersCanonicalBeadsJSONL(t *testing.T) {
	repoPath := t.TempDir()
	writeHistorySelectionFiles(t, repoPath)

	e := NewExtractor(repoPath, "")
	if got, want := e.primaryBeadsFile(), ".beads/beads.jsonl"; got != want {
		t.Fatalf("primaryBeadsFile = %s, want %s", got, want)
	}
}

func TestNewExtractorPrefersBDCompatibilityIssuesJSONL(t *testing.T) {
	repoPath := t.TempDir()
	beadsDir := writeHistorySelectionFiles(t, repoPath)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := NewExtractor(repoPath, "")
	if got, want := e.primaryBeadsFile(), ".beads/issues.jsonl"; got != want {
		t.Fatalf("primaryBeadsFile = %s, want %s", got, want)
	}
}

func TestPickBeadsFilesDoesNotInjectBDCompatibilityCandidate(t *testing.T) {
	repoPath := t.TempDir()
	beadsDir := filepath.Join(repoPath, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	candidates := []string{".beads/beads.jsonl"}
	got := pickBeadsFiles(repoPath, candidates)
	if len(got) != len(candidates) {
		t.Fatalf("pickBeadsFiles injected candidates: got %#v, want %#v", got, candidates)
	}
	if got[0] != candidates[0] {
		t.Fatalf("pickBeadsFiles = %#v, want %#v", got, candidates)
	}
}

func TestCalculateCycleTime_NoCreatedMilestone(t *testing.T) {
	now := time.Now()
	claimed := BeadEvent{EventType: EventClaimed, Timestamp: now}
	closed := BeadEvent{EventType: EventClosed, Timestamp: now.Add(24 * time.Hour)}

	milestones := BeadMilestones{
		Claimed: &claimed,
		Closed:  &closed,
	}

	ct := CalculateCycleTime(milestones)

	if ct == nil {
		t.Fatal("CycleTime should not be nil")
	}
	if ct.ClaimToClose == nil {
		t.Error("ClaimToClose should be set")
	}
	if ct.CreateToClose != nil {
		t.Error("CreateToClose should be nil when no Created milestone")
	}
}

func TestReverseEvents_Empty(t *testing.T) {
	events := []BeadEvent{}
	reverseEvents(events)
	if len(events) != 0 {
		t.Error("reverseEvents of empty should stay empty")
	}
}

func TestReverseEvents_Single(t *testing.T) {
	events := []BeadEvent{{BeadID: "a"}}
	reverseEvents(events)
	if events[0].BeadID != "a" {
		t.Error("reverseEvents of single should keep it")
	}
}
