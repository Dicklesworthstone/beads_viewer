package correlation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultHistoryLimit(t *testing.T) {
	if DefaultHistoryLimit != 500 {
		t.Errorf("DefaultHistoryLimit = %d, want 500", DefaultHistoryLimit)
	}
}

func TestNewStreamExtractor(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	if s.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", s.repoPath)
	}
	if len(s.beadsFiles) == 0 {
		t.Error("beadsFiles should not be empty")
	}
}

func TestNewStreamExtractorPrefersCanonicalBeadsJSONL(t *testing.T) {
	repoPath := t.TempDir()
	writeHistorySelectionFiles(t, repoPath)

	s := NewStreamExtractor(repoPath)
	if got, want := s.primaryBeadsFile(), ".beads/beads.jsonl"; got != want {
		t.Fatalf("primaryBeadsFile = %s, want %s", got, want)
	}
}

func TestNewStreamExtractorPrefersBDCompatibilityIssuesJSONL(t *testing.T) {
	repoPath := t.TempDir()
	beadsDir := writeHistorySelectionFiles(t, repoPath)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewStreamExtractor(repoPath)
	if got, want := s.primaryBeadsFile(), ".beads/issues.jsonl"; got != want {
		t.Fatalf("primaryBeadsFile = %s, want %s", got, want)
	}
}

func TestStreamExtractor_SetProgressCallback(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	called := false
	cb := func(processed, total int) {
		called = true
	}

	s.SetProgressCallback(cb)
	if s.progressCB == nil {
		t.Error("progressCB should be set")
	}

	s.progressCB(1, 10)
	if !called {
		t.Error("callback should have been called")
	}
}

func TestStreamExtractor_ProgressCallbackUsesExtractorDefault(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	defaultCalls := 0
	s.SetProgressCallback(func(processed, total int) {
		defaultCalls++
	})

	onProgress := s.progressCallback(StreamOptions{})
	if onProgress == nil {
		t.Fatal("progressCallback returned nil")
	}
	onProgress(1, 1)
	if defaultCalls == 0 {
		t.Fatal("extractor-level progress callback was not used")
	}
}

func TestStreamExtractor_ProgressCallbackOptionsOverrideDefault(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	defaultCalls := 0
	overrideCalls := 0
	s.SetProgressCallback(func(processed, total int) {
		defaultCalls++
	})
	opts := StreamOptions{
		OnProgress: func(processed, total int) {
			overrideCalls++
		},
	}

	onProgress := s.progressCallback(opts)
	if onProgress == nil {
		t.Fatal("progressCallback returned nil")
	}
	onProgress(1, 1)
	if overrideCalls == 0 {
		t.Fatal("per-call progress callback was not used")
	}
	if defaultCalls != 0 {
		t.Fatalf("extractor default progress callback was called %d times despite per-call override", defaultCalls)
	}
}

func TestStreamExtractor_BuildStreamCommandDisablesColor(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")
	cmd := s.buildStreamCommand(StreamOptions{Limit: 5}, 5)

	args := cmd.Args
	if len(args) < 4 {
		t.Fatalf("git command args too short: %#v", args)
	}
	if args[0] != "git" || args[1] != "-c" || args[2] != "color.ui=false" || args[3] != "log" {
		t.Fatalf("git command should disable color before log, got %#v", args)
	}
}

func TestParseCommitHeader(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantSHA string
		wantErr bool
	}{
		{
			name:    "valid header",
			line:    "abc123def456789012345678901234567890abcd" + "\x00" + "2025-12-15T10:30:00-05:00" + "\x00" + "John Doe" + "\x00" + "john@example.com" + "\x00" + "Fix bug",
			wantSHA: "abc123def456789012345678901234567890abcd",
			wantErr: false,
		},
		{
			name:    "invalid format",
			line:    "not a valid header",
			wantSHA: "",
			wantErr: true,
		},
		{
			name:    "invalid timestamp",
			line:    "abc123def456789012345678901234567890abcd" + "\x00" + "invalid" + "\x00" + "John" + "\x00" + "john@example.com" + "\x00" + "Fix",
			wantSHA: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := parseCommitHeader(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCommitHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && info.SHA != tt.wantSHA {
				t.Errorf("SHA = %s, want %s", info.SHA, tt.wantSHA)
			}
		})
	}
}

func TestParseCommitHeader_ParsesAllFields(t *testing.T) {
	line := "abc123def456789012345678901234567890abcd" + "\x00" + "2025-12-15T10:30:00-05:00" + "\x00" + "John Doe" + "\x00" + "john@example.com" + "\x00" + "Fix the bug in login"
	info, err := parseCommitHeader(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if info.SHA != "abc123def456789012345678901234567890abcd" {
		t.Errorf("SHA = %s", info.SHA)
	}
	if info.Author != "John Doe" {
		t.Errorf("Author = %s, want John Doe", info.Author)
	}
	if info.AuthorEmail != "john@example.com" {
		t.Errorf("AuthorEmail = %s, want john@example.com", info.AuthorEmail)
	}
	if info.Message != "Fix the bug in login" {
		t.Errorf("Message = %s", info.Message)
	}
	if info.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"abc123", true},
		{"0123456789abcdef", true},
		{"ABC123", false}, // uppercase not valid
		{"abc123g", false},
		{"", true},
		{"abc 123", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isHexString(tt.input); got != tt.want {
				t.Errorf("isHexString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterCodeFiles(t *testing.T) {
	files := []FileChange{
		{Path: "main.go", Action: "M"},
		{Path: "README.md", Action: "M"},
		{Path: "image.png", Action: "A"},
		{Path: ".beads/issues.jsonl", Action: "M"},
		{Path: "node_modules/lodash/index.js", Action: "M"},
		{Path: "src/app.py", Action: "A"},
	}

	filtered := filterCodeFiles(files)

	// Should include: main.go, README.md, src/app.py
	// Should exclude: image.png (not code), .beads/ (excluded), node_modules/ (excluded)
	if len(filtered) != 3 {
		t.Errorf("len(filtered) = %d, want 3", len(filtered))
	}

	expectedPaths := map[string]bool{"main.go": true, "README.md": true, "src/app.py": true}
	for _, f := range filtered {
		if !expectedPaths[f.Path] {
			t.Errorf("unexpected file in filtered: %s", f.Path)
		}
	}
}

func TestNewBatchFileStatsExtractor(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")

	if b.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", b.repoPath)
	}
	if b.batchSize != 50 {
		t.Errorf("batchSize = %d, want 50", b.batchSize)
	}
	if b.cache == nil {
		t.Error("cache should not be nil")
	}
}

func TestBatchFileStatsExtractor_SetBatchSize(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")

	b.SetBatchSize(100)
	if b.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100", b.batchSize)
	}

	// Should ignore invalid sizes
	b.SetBatchSize(0)
	if b.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100 (unchanged)", b.batchSize)
	}

	b.SetBatchSize(-5)
	if b.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100 (unchanged)", b.batchSize)
	}
}

func TestBatchFileStatsExtractor_ClearCache(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")

	// Add something to cache
	b.cache["abc123"] = []FileChange{{Path: "test.go"}}

	if len(b.cache) != 1 {
		t.Error("cache should have 1 entry")
	}

	b.ClearCache()

	if len(b.cache) != 0 {
		t.Errorf("cache should be empty after clear, has %d entries", len(b.cache))
	}
}

func TestBatchFileStatsExtractor_CacheHit(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")

	// Pre-populate cache
	b.cache["abc123"] = []FileChange{{Path: "cached.go", Action: "M"}}
	b.cache["def456"] = []FileChange{{Path: "also_cached.go", Action: "A"}}

	// Request cached SHAs
	result, err := b.ExtractBatch([]string{"abc123", "def456"})
	if err != nil {
		t.Fatalf("ExtractBatch failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("len(result) = %d, want 2", len(result))
	}

	if len(result["abc123"]) != 1 || result["abc123"][0].Path != "cached.go" {
		t.Error("abc123 result incorrect")
	}
	if len(result["def456"]) != 1 || result["def456"][0].Path != "also_cached.go" {
		t.Error("def456 result incorrect")
	}
}

func TestBatchFileStatsExtractor_CacheHitReturnsCopy(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")
	b.cache["abc123"] = []FileChange{{Path: "cached.go", Action: "M"}}

	result, err := b.ExtractBatch([]string{"abc123"})
	if err != nil {
		t.Fatalf("ExtractBatch failed: %v", err)
	}
	result["abc123"][0].Path = "mutated.go"

	result, err = b.ExtractBatch([]string{"abc123"})
	if err != nil {
		t.Fatalf("ExtractBatch failed: %v", err)
	}
	if got := result["abc123"][0].Path; got != "cached.go" {
		t.Fatalf("cached path = %s, want cached.go", got)
	}
}

func TestBatchFileStatsExtractor_StoredBatchReturnsCopy(t *testing.T) {
	b := NewBatchFileStatsExtractor("/tmp/test")
	result := make(map[string][]FileChange)

	b.storeBatchResult(result, map[string][]FileChange{
		"abc123": {{Path: "cached.go", Action: "M"}},
	})
	result["abc123"][0].Path = "mutated.go"

	result, err := b.ExtractBatch([]string{"abc123"})
	if err != nil {
		t.Fatalf("ExtractBatch failed: %v", err)
	}
	if got := result["abc123"][0].Path; got != "cached.go" {
		t.Fatalf("cached path = %s, want cached.go", got)
	}
}

func TestStreamOptions_Defaults(t *testing.T) {
	opts := StreamOptions{}

	if opts.Limit != 0 {
		t.Errorf("default Limit = %d, want 0", opts.Limit)
	}
	if opts.Since != nil {
		t.Error("default Since should be nil")
	}
	if opts.Until != nil {
		t.Error("default Until should be nil")
	}
	if opts.ClosedSince != nil {
		t.Error("default ClosedSince should be nil")
	}
}

func TestStreamExtractor_ParseBufferedDiff_Created(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")
	info := commitInfo{
		SHA:         "abc123",
		Timestamp:   time.Now(),
		Author:      "Test",
		AuthorEmail: "test@example.com",
		Message:     "Add bead",
	}

	lines := []string{
		`+{"id":"bv-1","status":"open","title":"Test"}`,
	}

	events := s.parseBufferedDiff(lines, info, "", nil)

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	if events[0].EventType != EventCreated {
		t.Errorf("EventType = %s, want created", events[0].EventType)
	}
	if events[0].BeadID != "bv-1" {
		t.Errorf("BeadID = %s, want bv-1", events[0].BeadID)
	}
}

func TestStreamExtractor_ParseBufferedDiff_StatusChange(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")
	info := commitInfo{
		SHA:         "abc123",
		Timestamp:   time.Now(),
		Author:      "Test",
		AuthorEmail: "test@example.com",
		Message:     "Close bead",
	}

	lines := []string{
		`-{"id":"bv-1","status":"in_progress","title":"Test"}`,
		`+{"id":"bv-1","status":"closed","title":"Test"}`,
	}

	events := s.parseBufferedDiff(lines, info, "", nil)

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	if events[0].EventType != EventClosed {
		t.Errorf("EventType = %s, want closed", events[0].EventType)
	}
}

func TestStreamExtractor_ParseBufferedDiff_FilterByBead(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")
	info := commitInfo{
		SHA:       "abc123",
		Timestamp: time.Now(),
	}

	lines := []string{
		`+{"id":"bv-1","status":"open","title":"Test1"}`,
		`+{"id":"bv-2","status":"open","title":"Test2"}`,
	}

	// Filter to only bv-1
	events := s.parseBufferedDiff(lines, info, "bv-1", nil)

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].BeadID != "bv-1" {
		t.Errorf("BeadID = %s, want bv-1", events[0].BeadID)
	}
}

func TestStreamExtractor_ParseBufferedDiff_MultipleBeadsHaveStableOrder(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")
	info := commitInfo{SHA: "abc123", Timestamp: time.Now()}
	inputIDs := []string{"bv-08", "bv-03", "bv-10", "bv-01", "bv-06", "bv-04", "bv-09", "bv-02", "bv-07", "bv-05"}
	lines := make([]string, 0, len(inputIDs))
	for _, id := range inputIDs {
		lines = append(lines, fmt.Sprintf(`+{"id":%q,"status":"open","title":"Test"}`, id))
	}
	want := []string{"bv-01", "bv-02", "bv-03", "bv-04", "bv-05", "bv-06", "bv-07", "bv-08", "bv-09", "bv-10"}

	for iteration := 0; iteration < 32; iteration++ {
		events := s.parseBufferedDiff(lines, info, "", nil)
		if len(events) != len(want) {
			t.Fatalf("iteration %d: len(events) = %d, want %d", iteration, len(events), len(want))
		}
		for i, event := range events {
			if event.BeadID != want[i] {
				t.Fatalf("iteration %d: event[%d].BeadID = %q, want %q", iteration, i, event.BeadID, want[i])
			}
		}
	}
}

func TestStreamExtractor_ParseBufferedDiff_ClosedSince(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	oldTime := time.Now().Add(-48 * time.Hour)
	recentTime := time.Now().Add(-1 * time.Hour)
	cutoff := time.Now().Add(-24 * time.Hour)

	// Old closed event (should be filtered)
	oldInfo := commitInfo{
		SHA:       "old123",
		Timestamp: oldTime,
	}
	oldLines := []string{
		`-{"id":"bv-1","status":"in_progress","title":"Old"}`,
		`+{"id":"bv-1","status":"closed","title":"Old"}`,
	}
	oldEvents := s.parseBufferedDiff(oldLines, oldInfo, "", &cutoff)
	if len(oldEvents) != 0 {
		t.Errorf("old closed event should be filtered: got %d events", len(oldEvents))
	}

	oldTombstoneLines := []string{
		`-{"id":"bv-1","status":"in_progress","title":"Old"}`,
		`+{"id":"bv-1","status":"tombstone","title":"Old"}`,
	}
	oldTombstoneEvents := s.parseBufferedDiff(oldTombstoneLines, oldInfo, "", &cutoff)
	if len(oldTombstoneEvents) != 0 {
		t.Errorf("old tombstone event should be filtered as closed: got %d events", len(oldTombstoneEvents))
	}

	// Recent closed event (should pass)
	recentInfo := commitInfo{
		SHA:       "recent123",
		Timestamp: recentTime,
	}
	recentLines := []string{
		`-{"id":"bv-2","status":"in_progress","title":"Recent"}`,
		`+{"id":"bv-2","status":"closed","title":"Recent"}`,
	}
	recentEvents := s.parseBufferedDiff(recentLines, recentInfo, "", &cutoff)
	if len(recentEvents) != 1 {
		t.Errorf("recent closed event should pass: got %d events", len(recentEvents))
	}
}

func TestStreamExtractor_StreamEvents_InGitRepo(t *testing.T) {
	// Skip if not in a git repo
	if _, err := getGitHead("."); err != nil {
		t.Skip("Not in a git repository")
	}

	s := NewStreamExtractor(".")
	opts := StreamOptions{
		Limit: 10,
	}

	events, err := s.StreamEvents(opts)
	if err != nil {
		// Accept error if beads file doesn't exist
		if strings.Contains(err.Error(), "does not have any commits") {
			t.Skip("No beads commits in repo")
		}
		t.Fatalf("StreamEvents failed: %v", err)
	}

	// Just verify it returns without error
	t.Logf("Got %d events from stream extraction", len(events))
}

func TestStreamExtractor_StreamEventsWithGitColorAlways(t *testing.T) {
	repoPath := initTempGitRepo(t)
	runGit(t, repoPath, "config", "color.ui", "always")

	writeStreamFixtureBead(t, repoPath, "bv-color")

	events, err := NewStreamExtractor(repoPath).StreamEvents(StreamOptions{Limit: 5})
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	for _, event := range events {
		if event.BeadID == "bv-color" && event.EventType == EventCreated {
			return
		}
	}
	t.Fatalf("expected created event for bv-color with color.ui=always, got %#v", events)
}

func TestStreamExtractor_StreamEventsUsesExtractorProgressCallback(t *testing.T) {
	repoPath := initTempGitRepo(t)
	writeStreamFixtureBead(t, repoPath, "bv-progress")

	progressCalls := 0
	s := NewStreamExtractor(repoPath)
	s.SetProgressCallback(func(processed, total int) {
		progressCalls++
	})

	events, err := s.StreamEvents(StreamOptions{Limit: 5})
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	if progressCalls == 0 {
		t.Fatal("extractor-level progress callback was not called")
	}
	assertCreatedEvent(t, events, "bv-progress")
}

func TestStreamExtractor_StreamEventsOptionsProgressOverridesExtractorDefault(t *testing.T) {
	repoPath := initTempGitRepo(t)
	writeStreamFixtureBead(t, repoPath, "bv-progress-override")

	defaultCalls := 0
	overrideCalls := 0
	s := NewStreamExtractor(repoPath)
	s.SetProgressCallback(func(processed, total int) {
		defaultCalls++
	})

	events, err := s.StreamEvents(StreamOptions{
		Limit: 5,
		OnProgress: func(processed, total int) {
			overrideCalls++
		},
	})
	if err != nil {
		t.Fatalf("StreamEvents failed: %v", err)
	}
	if overrideCalls == 0 {
		t.Fatal("per-call progress callback was not called")
	}
	if defaultCalls != 0 {
		t.Fatalf("extractor default progress callback was called %d times despite per-call override", defaultCalls)
	}
	assertCreatedEvent(t, events, "bv-progress-override")
}

func writeStreamFixtureBead(t *testing.T, repoPath, beadID string) {
	t.Helper()

	beadsDir := filepath.Join(repoPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "beads.jsonl")
	content := `{"id":"` + beadID + `","status":"open","title":"Stream fixture"}` + "\n"
	if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write beads file: %v", err)
	}
	runGit(t, repoPath, "add", ".beads/beads.jsonl")
	runGit(t, repoPath, "commit", "-m", "add stream fixture bead")
}

func assertCreatedEvent(t *testing.T, events []BeadEvent, beadID string) {
	t.Helper()

	for _, event := range events {
		if event.BeadID == beadID && event.EventType == EventCreated {
			return
		}
	}
	t.Fatalf("expected created event for %s, got %#v", beadID, events)
}

func TestProgressCallback_CalledDuringParsing(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	progressCalls := 0
	callback := func(processed, total int) {
		progressCalls++
	}

	// Create mock input with multiple commits
	// This tests the parseStream function directly
	input := strings.NewReader(`abc123def456789012345678901234567890abcd` + "\x00" + `2025-12-15T10:30:00Z` + "\x00" + `John` + "\x00" + `john@test.com` + "\x00" + `Commit 1
+{"id":"bv-1","status":"open"}
def456abc123789012345678901234567890abcd` + "\x00" + `2025-12-15T10:31:00Z` + "\x00" + `Jane` + "\x00" + `jane@test.com` + "\x00" + `Commit 2
+{"id":"bv-2","status":"open"}
`)

	_, err := s.parseStream(input, "", nil, 2, callback)
	if err != nil {
		t.Fatalf("parseStream failed: %v", err)
	}

	// Final call should always happen
	if progressCalls == 0 {
		t.Error("progress callback should have been called")
	}
}

func TestStreamExtractor_ParseStreamInvalidHeaderReturnsError(t *testing.T) {
	s := NewStreamExtractor("/tmp/test")

	input := strings.NewReader(`abc123def456789012345678901234567890abcd` + "\x00" + `not-a-time` + "\x00" + `John` + "\x00" + `john@test.com` + "\x00" + `Commit
+{"id":"bv-1","status":"open"}
`)

	_, err := s.parseStream(input, "", nil, 1, nil)
	if err == nil {
		t.Fatalf("parseStream accepted a malformed commit header")
	}
	if !strings.Contains(err.Error(), "parsing commit header") {
		t.Fatalf("parseStream error = %v, want commit header context", err)
	}
}
