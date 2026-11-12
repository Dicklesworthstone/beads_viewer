package correlation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func TestIncrementalThreshold(t *testing.T) {
	if IncrementalThreshold != 100 {
		t.Errorf("IncrementalThreshold = %d, want 100", IncrementalThreshold)
	}
}

func TestNewIncrementalCorrelator(t *testing.T) {
	ic := NewIncrementalCorrelator("/tmp/test")

	if ic.correlator == nil {
		t.Error("correlator should not be nil")
	}
	if ic.cache == nil {
		t.Error("cache should not be nil")
	}
	if ic.hits != 0 || ic.misses != 0 || ic.increments != 0 || ic.refreshes != 0 {
		t.Error("initial stats should all be 0")
	}
}

func TestNewIncrementalCorrelatorWithOptions(t *testing.T) {
	ic := NewIncrementalCorrelatorWithOptions("/tmp/test", 10*time.Minute, 20)

	if ic.cache.maxAge != 10*time.Minute {
		t.Errorf("maxAge = %v, want 10m", ic.cache.maxAge)
	}
	if ic.cache.maxSize != 20 {
		t.Errorf("maxSize = %d, want 20", ic.cache.maxSize)
	}
}

func TestNewIncrementalCorrelatorForwardsExplicitBeadsPath(t *testing.T) {
	repoPath := "/tmp/test"
	explicitPath := filepath.Join(repoPath, ".beads", "custom.jsonl")
	expectedPath := filepath.Join(".beads", "custom.jsonl")

	ic := NewIncrementalCorrelator(repoPath, explicitPath)

	if got := ic.correlator.extractor.primaryBeadsFile(); got != expectedPath {
		t.Errorf("primaryBeadsFile = %q, want %q", got, expectedPath)
	}
}

func TestNewIncrementalCorrelatorWithOptionsForwardsExplicitBeadsPath(t *testing.T) {
	repoPath := "/tmp/test"
	explicitPath := filepath.Join(repoPath, ".beads", "custom.jsonl")
	expectedPath := filepath.Join(".beads", "custom.jsonl")

	ic := NewIncrementalCorrelatorWithOptions(repoPath, 10*time.Minute, 20, explicitPath)

	if got := ic.correlator.extractor.primaryBeadsFile(); got != expectedPath {
		t.Errorf("primaryBeadsFile = %q, want %q", got, expectedPath)
	}
}

func TestIncrementalCorrelatorUsesConfiguredExtractorForUpdates(t *testing.T) {
	repoPath := "/tmp/test"
	explicitPath := filepath.Join(repoPath, ".beads", "custom.jsonl")
	expectedPath := filepath.Join(".beads", "custom.jsonl")

	ic := NewIncrementalCorrelator(repoPath, explicitPath)

	if got := ic.incrementalExtractor().primaryBeadsFile(); got != expectedPath {
		t.Errorf("incremental extractor primaryBeadsFile = %q, want %q", got, expectedPath)
	}
}

func TestIncrementalCorrelator_CacheStats(t *testing.T) {
	ic := NewIncrementalCorrelator("/tmp/test")

	stats := ic.CacheStats()

	if stats.Hits != 0 {
		t.Errorf("Hits = %d, want 0", stats.Hits)
	}
	if stats.Misses != 0 {
		t.Errorf("Misses = %d, want 0", stats.Misses)
	}
	if stats.IncrementalUpdates != 0 {
		t.Errorf("IncrementalUpdates = %d, want 0", stats.IncrementalUpdates)
	}
	if stats.FullRefreshes != 0 {
		t.Errorf("FullRefreshes = %d, want 0", stats.FullRefreshes)
	}
}

func TestIncrementalCorrelator_InvalidateCache(t *testing.T) {
	ic := NewIncrementalCorrelator("/tmp/test")

	// Add something to cache manually
	key := CacheKey{HeadSHA: "abc", BeadsHash: "def", Options: "ghi"}
	ic.cache.Put(key, &HistoryReport{})

	if ic.cache.Size() != 1 {
		t.Fatal("cache should have 1 entry")
	}

	ic.InvalidateCache()

	if ic.cache.Size() != 0 {
		t.Errorf("cache size after invalidate = %d, want 0", ic.cache.Size())
	}
}

func TestMergeReports_Basic(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now().Add(-1 * time.Hour),
		DataHash:        "existinghash",
		GitRange:        "abc123..def456",
		LatestCommitSHA: "def456",
		Stats: HistoryStats{
			TotalBeads:         1,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Test Bead",
				Status: "open",
				Events: []BeadEvent{
					{BeadID: "bv-1", EventType: EventCreated, CommitSHA: "abc123"},
				},
				Commits: []CorrelatedCommit{},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{
		{ID: "bv-1", Title: "Test Bead Updated", Status: "in_progress"},
	}

	newEvents := []BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, CommitSHA: "ghi789", Timestamp: time.Now()},
	}

	merged := mergeReports(existing, beads, newEvents, nil)

	// DataHash must be recomputed accurately for the updated bead state.
	wantHash := hashBeads(beads)
	if merged.DataHash != wantHash {
		t.Errorf("DataHash = %s, want recomputed %s (stale was %s)", merged.DataHash, wantHash, existing.DataHash)
	}

	if merged.GitRange != "abc123..def456 (incremental)" {
		t.Errorf("GitRange = %s, want 'abc123..def456 (incremental)'", merged.GitRange)
	}

	// Check merged history
	h, ok := merged.Histories["bv-1"]
	if !ok {
		t.Fatal("bv-1 history not found")
	}

	if len(h.Events) != 2 {
		t.Errorf("Events count = %d, want 2", len(h.Events))
	}

	// Status should be updated from beads list
	if h.Status != "in_progress" {
		t.Errorf("Status = %s, want in_progress", h.Status)
	}

	// Title should be updated
	if h.Title != "Test Bead Updated" {
		t.Errorf("Title = %s, want 'Test Bead Updated'", h.Title)
	}
}

func TestMergeReportsDoesNotAliasNestedCachedHistory(t *testing.T) {
	created := BeadEvent{BeadID: "bv-1", EventType: EventCreated, Author: "original-event"}
	claimToClose := 2 * time.Hour
	existing := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID:     "bv-1",
				Events:     []BeadEvent{created},
				Milestones: BeadMilestones{Created: &created},
				Commits: []CorrelatedCommit{{
					SHA:   "commit-1",
					Files: []FileChange{{Path: "original.go"}},
				}},
				CycleTime: &CycleTime{ClaimToClose: &claimToClose},
			},
		},
	}

	merged := mergeReports(existing, []BeadInfo{{ID: "bv-1"}}, nil, nil)
	history := merged.Histories["bv-1"]
	history.Events[0].Author = "mutated-event"
	history.Milestones.Created.Author = "mutated-milestone"
	history.Commits[0].Files[0].Path = "mutated.go"
	*history.CycleTime.ClaimToClose = 99 * time.Hour

	original := existing.Histories["bv-1"]
	if original.Events[0].Author != "original-event" {
		t.Fatalf("merged event mutated cached event: %+v", original.Events[0])
	}
	if original.Milestones.Created.Author != "original-event" {
		t.Fatalf("merged milestone mutated cached milestone: %+v", original.Milestones.Created)
	}
	if got := original.Commits[0].Files[0].Path; got != "original.go" {
		t.Fatalf("merged commit files mutated cached files: %q", got)
	}
	if got := *original.CycleTime.ClaimToClose; got != 2*time.Hour {
		t.Fatalf("merged cycle time mutated cached cycle time: %v", got)
	}
}

func TestMergeReports_NewBeads(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         1,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {BeadID: "bv-1", Title: "Existing", Status: "open"},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{
		{ID: "bv-1", Title: "Existing", Status: "open"},
		{ID: "bv-2", Title: "New Bead", Status: "open"}, // New bead
	}

	merged := mergeReports(existing, beads, nil, nil)

	if len(merged.Histories) != 2 {
		t.Errorf("Histories count = %d, want 2", len(merged.Histories))
	}

	if _, ok := merged.Histories["bv-2"]; !ok {
		t.Error("New bead bv-2 should be in merged report")
	}
}

func TestMergeReports_CommitMerge(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         1,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID:  "bv-1",
				Commits: []CorrelatedCommit{{SHA: "commit1", Author: "Alice"}},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{{ID: "bv-1"}}

	newEvents := []BeadEvent{
		{BeadID: "bv-1", CommitSHA: "commit2"},
	}

	newCommits := []CorrelatedCommit{
		{SHA: "commit2", BeadID: "bv-1", Author: "Bob", Timestamp: time.Now()},
	}

	merged := mergeReports(existing, beads, newEvents, newCommits)

	h := merged.Histories["bv-1"]
	if len(h.Commits) != 2 {
		t.Errorf("Commits count = %d, want 2", len(h.Commits))
	}

	// Last author should be updated
	if h.LastAuthor != "Bob" {
		t.Errorf("LastAuthor = %s, want Bob", h.LastAuthor)
	}
}

func TestMergeReports_CommitDedup(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         1,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID:  "bv-1",
				Commits: []CorrelatedCommit{{SHA: "commit1"}},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{{ID: "bv-1"}}

	newEvents := []BeadEvent{
		{BeadID: "bv-1", CommitSHA: "commit1"}, // Same commit
	}

	newCommits := []CorrelatedCommit{
		{SHA: "commit1", BeadID: "bv-1"}, // Duplicate
	}

	merged := mergeReports(existing, beads, newEvents, newCommits)

	h := merged.Histories["bv-1"]
	if len(h.Commits) != 1 {
		t.Errorf("Commits should be deduped: got %d, want 1", len(h.Commits))
	}
}

func TestMergeReportsUsesCommitOwnedBeadLinkage(t *testing.T) {
	existing := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-a": {BeadID: "bv-a"},
		"bv-b": {BeadID: "bv-b"},
	}}
	beads := []BeadInfo{{ID: "bv-a"}, {ID: "bv-b"}}
	events := []BeadEvent{
		{BeadID: "bv-a", CommitSHA: "same"},
		{BeadID: "bv-b", CommitSHA: "same"},
	}
	commits := []CorrelatedCommit{
		{SHA: "same", BeadID: "bv-a", Reason: "reason-a"},
		{SHA: "same", BeadID: "bv-b", Reason: "reason-b"},
	}

	merged := mergeReports(existing, beads, events, commits)
	if got := merged.Histories["bv-a"].Commits; len(got) != 1 || got[0].Reason != "reason-a" {
		t.Fatalf("bv-a commits = %+v, want only its owned correlation", got)
	}
	if got := merged.Histories["bv-b"].Commits; len(got) != 1 || got[0].Reason != "reason-b" {
		t.Fatalf("bv-b commits = %+v, want only its owned correlation", got)
	}
}

func TestMergeReportsThroughAdvancesCodeOnlyCursor(t *testing.T) {
	existing := &HistoryReport{LatestCommitSHA: "old", Histories: map[string]BeadHistory{}}
	merged := mergeReportsThrough(existing, nil, nil, nil, "new-code-only-head")
	if merged.LatestCommitSHA != "new-code-only-head" {
		t.Fatalf("LatestCommitSHA=%q, want processed code-only cursor", merged.LatestCommitSHA)
	}
}

func TestMergeReports_CommitIndex(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         2,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {BeadID: "bv-1", Commits: []CorrelatedCommit{{SHA: "c1"}}},
			"bv-2": {BeadID: "bv-2", Commits: []CorrelatedCommit{{SHA: "c2"}}},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{{ID: "bv-1"}, {ID: "bv-2"}}

	merged := mergeReports(existing, beads, nil, nil)

	if len(merged.CommitIndex["c1"]) != 1 || merged.CommitIndex["c1"][0] != "bv-1" {
		t.Error("CommitIndex for c1 incorrect")
	}
	if len(merged.CommitIndex["c2"]) != 1 || merged.CommitIndex["c2"][0] != "bv-2" {
		t.Error("CommitIndex for c2 incorrect")
	}
}

func TestMergeReports_Stats(t *testing.T) {
	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         2,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID:  "bv-1",
				Events:  []BeadEvent{{Author: "Alice"}},
				Commits: []CorrelatedCommit{{SHA: "c1", Author: "Alice", Method: MethodCoCommitted}},
			},
			"bv-2": {
				BeadID:  "bv-2",
				Events:  []BeadEvent{{Author: "Bob"}},
				Commits: []CorrelatedCommit{{SHA: "c2", Author: "Bob", Method: MethodExplicitID}},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{{ID: "bv-1"}, {ID: "bv-2"}}

	merged := mergeReports(existing, beads, nil, nil)

	if merged.Stats.TotalBeads != 2 {
		t.Errorf("TotalBeads = %d, want 2", merged.Stats.TotalBeads)
	}
	if merged.Stats.TotalCommits != 2 {
		t.Errorf("TotalCommits = %d, want 2", merged.Stats.TotalCommits)
	}
	if merged.Stats.UniqueAuthors != 2 {
		t.Errorf("UniqueAuthors = %d, want 2", merged.Stats.UniqueAuthors)
	}
	if merged.Stats.BeadsWithCommits != 2 {
		t.Errorf("BeadsWithCommits = %d, want 2", merged.Stats.BeadsWithCommits)
	}
}

func TestMergeReports_MilestonesRecalculated(t *testing.T) {
	createdTime := time.Now().Add(-2 * time.Hour)
	claimedTime := time.Now().Add(-1 * time.Hour)
	closedTime := time.Now()

	existing := &HistoryReport{
		GeneratedAt:     time.Now(),
		DataHash:        "hash",
		LatestCommitSHA: "abc123",
		Stats: HistoryStats{
			TotalBeads:         1,
			MethodDistribution: make(map[string]int),
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Events: []BeadEvent{
					{BeadID: "bv-1", EventType: EventCreated, Timestamp: createdTime},
					{BeadID: "bv-1", EventType: EventClaimed, Timestamp: claimedTime},
				},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	beads := []BeadInfo{{ID: "bv-1", Status: "closed"}}

	newEvents := []BeadEvent{
		{BeadID: "bv-1", EventType: EventClosed, Timestamp: closedTime, CommitSHA: "def456"},
	}

	merged := mergeReports(existing, beads, newEvents, nil)

	h := merged.Histories["bv-1"]

	// Check milestones were recalculated
	if h.Milestones.Created == nil {
		t.Error("Created milestone should exist")
	}
	if h.Milestones.Claimed == nil {
		t.Error("Claimed milestone should exist")
	}
	if h.Milestones.Closed == nil {
		t.Error("Closed milestone should exist after merge")
	}

	// Check cycle time was calculated
	if h.CycleTime == nil {
		t.Error("CycleTime should be calculated after close")
	}
	if h.CycleTime.ClaimToClose == nil {
		t.Error("ClaimToClose should be set")
	}
}

func TestIncrementalUpdateResult_Fields(t *testing.T) {
	result := IncrementalUpdateResult{
		Report:            &HistoryReport{},
		WasIncremental:    true,
		NewCommitCount:    5,
		MergedEventCount:  3,
		MergedCommitCount: 2,
		RefreshReason:     "",
	}

	if !result.WasIncremental {
		t.Error("WasIncremental should be true")
	}
	if result.NewCommitCount != 5 {
		t.Errorf("NewCommitCount = %d, want 5", result.NewCommitCount)
	}
	if result.RefreshReason != "" {
		t.Errorf("RefreshReason should be empty for incremental")
	}
}

func TestCanUpdateIncrementally_NoReport(t *testing.T) {
	ok, count, err := CanUpdateIncrementally("/tmp", nil)
	if ok {
		t.Error("Should return false for nil report")
	}
	if count != 0 {
		t.Errorf("Count should be 0, got %d", count)
	}
	if err != nil {
		t.Errorf("Should not return error for nil report, got %v", err)
	}
}

func TestCanUpdateIncrementally_EmptySHA(t *testing.T) {
	report := &HistoryReport{LatestCommitSHA: ""}
	ok, count, err := CanUpdateIncrementally("/tmp", report)
	if ok {
		t.Error("Should return false for empty SHA")
	}
	if count != 0 {
		t.Errorf("Count should be 0, got %d", count)
	}
	if err != nil {
		t.Errorf("Should not return error for empty SHA, got %v", err)
	}
}

func TestIncrementalCorrelator_GenerateReport_FullRefresh(t *testing.T) {
	// Skip if not in a git repo
	if _, err := getGitHead("."); err != nil {
		t.Skip("Not in a git repository")
	}

	ic := NewIncrementalCorrelator(".")
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	// First call should do full refresh
	result, err := ic.GenerateReportWithDetails(beads, opts)
	if err != nil {
		t.Fatalf("GenerateReportWithDetails failed: %v", err)
	}

	if result.WasIncremental {
		t.Error("First call should not be incremental")
	}

	stats := ic.CacheStats()
	if stats.FullRefreshes != 1 {
		t.Errorf("FullRefreshes = %d, want 1", stats.FullRefreshes)
	}
}

func TestIncrementalCorrelator_GenerateReport_CacheHit(t *testing.T) {
	// Skip if not in a git repo
	if _, err := getGitHead("."); err != nil {
		t.Skip("Not in a git repository")
	}

	ic := NewIncrementalCorrelator(".")
	beads := []BeadInfo{{ID: "test-1", Status: "open"}}
	opts := CorrelatorOptions{Limit: 10}

	// First call
	_, err := ic.GenerateReport(beads, opts)
	if err != nil {
		t.Fatalf("First GenerateReport failed: %v", err)
	}

	// Second call should hit cache
	_, err = ic.GenerateReport(beads, opts)
	if err != nil {
		t.Fatalf("Second GenerateReport failed: %v", err)
	}

	stats := ic.CacheStats()
	if stats.Hits != 1 {
		t.Errorf("Hits = %d, want 1", stats.Hits)
	}
}

func TestCalculateMergedStats_CycleTime(t *testing.T) {
	claimTime := time.Now().Add(-2 * time.Hour)
	closeTime := time.Now()
	duration := closeTime.Sub(claimTime)

	histories := map[string]BeadHistory{
		"bv-1": {
			BeadID:  "bv-1",
			Commits: []CorrelatedCommit{{SHA: "c1", Author: "Alice", Method: MethodCoCommitted}},
			Events:  []BeadEvent{{Author: "Alice"}},
			CycleTime: &CycleTime{
				ClaimToClose: &duration,
			},
		},
	}

	stats := calculateMergedStats(histories, nil)

	if stats.AvgCycleTimeDays == nil {
		t.Error("AvgCycleTimeDays should be set when cycle times exist")
	}
}

func TestIncrementalCorrelator_MultiStrategyFeedbackAndDataHashParity(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	commitAt := func(offset time.Duration, msg string) string {
		t.Helper()
		runGit(t, repo, "add", "--", ".")
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=Alice", "GIT_COMMITTER_EMAIL=alice@example.com",
			"GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit %q: %v\n%s", msg, err, out)
		}
		out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	jsonl := filepath.Join(beadsDir, "issues.jsonl")

	// Initial commit 1: bv-pzpv created directly in_progress, bv-apal.20 open.
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Overhaul correlation","status":"in_progress","assignee":"alice@example.com"}`+"\n"+
			`{"id":"bv-apal.20","title":"Dot child task","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "init.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedSHA := commitAt(0, "feat(bv-pzpv): initial setup")

	// Commit 2: explicit commit that we will reject via FeedbackStore.
	if err := os.WriteFile(filepath.Join(repo, "reject_me.go"), []byte("package p\nvar r = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rejectedSHA := commitAt(15*time.Minute, "refs bv-pzpv noisy commit")

	store := NewFeedbackStore(beadsDir)
	if err := store.Confirm(seedSHA, "bv-pzpv", "alice", 0.9, "seed link"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reject(rejectedSHA, "bv-pzpv", "alice", 0.9, "noise"); err != nil {
		t.Fatal(err)
	}

	initialBeads := []BeadInfo{
		{ID: "bv-pzpv", Title: "Overhaul correlation", Status: "in_progress"},
		{ID: "bv-apal.20", Title: "Dot child task", Status: "open"},
	}
	opts := CorrelatorOptions{Limit: 100}

	ic := NewIncrementalCorrelator(repo, jsonl).WithFeedbackStore(store)
	firstRes, err := ic.GenerateReportWithDetails(initialBeads, opts)
	if err != nil {
		t.Fatalf("initial GenerateReportWithDetails: %v", err)
	}
	if firstRes.WasIncremental {
		t.Fatal("first run should be a full refresh")
	}

	// Incremental commits after initial cache population:
	// Commit 3: explicit_id on dot-child bv-apal.20 touching non-Go files (.gitignore + Makefile).
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), []byte("all:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dotExplicitSHA := commitAt(30*time.Minute, "Closes [BV-APAL.20].")

	// Commit 4: mixed code + .beads/ commit inside bv-pzpv's active window (temporal_author).
	if err := os.WriteFile(filepath.Join(repo, "worker.zig"), []byte("pub fn main() void {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Overhaul correlation","status":"in_progress","assignee":"alice@example.com"}`+"\n"+
			`{"id":"bv-apal.20","title":"Dot child task","status":"open"}`+"\n"+
			`{"id":"bv-empty","title":"Zero commit bead","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	temporalMixedSHA := commitAt(45*time.Minute, "wip: add zig worker")

	// Commit 5: close bv-pzpv and create single-commit closed bv-fast alongside schema.graphql (co_committed).
	if err := os.WriteFile(filepath.Join(repo, "schema.graphql"), []byte("type Query { ok: Boolean! }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Overhaul correlation complete","status":"closed","assignee":"alice@example.com"}`+"\n"+
			`{"id":"bv-apal.20","title":"Dot child task","status":"closed"}`+"\n"+
			`{"id":"bv-empty","title":"Zero commit bead","status":"open"}`+"\n"+
			`{"id":"bv-fast","title":"Fast graphql schema","status":"closed","assignee":"alice@example.com"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	coCommitSHA := commitAt(60*time.Minute, "chore: finalize beads and schema")

	updatedBeads := []BeadInfo{
		{ID: "bv-pzpv", Title: "Overhaul correlation complete", Status: "closed"},
		{ID: "bv-apal.20", Title: "Dot child task", Status: "closed"},
		{ID: "bv-empty", Title: "Zero commit bead", Status: "open"},
		{ID: "bv-fast", Title: "Fast graphql schema", Status: "closed"},
	}

	incRes, err := ic.GenerateReportWithDetails(updatedBeads, opts)
	if err != nil {
		t.Fatalf("incremental GenerateReportWithDetails: %v", err)
	}
	if !incRes.WasIncremental {
		t.Fatalf("expected WasIncremental=true, got false (reason=%q)", incRes.RefreshReason)
	}
	if incRes.NewCommitCount != 3 {
		t.Fatalf("NewCommitCount = %d, want 3", incRes.NewCommitCount)
	}

	freshReport, err := NewCorrelator(repo, jsonl).WithFeedbackStore(store).GenerateReport(updatedBeads, opts)
	if err != nil {
		t.Fatalf("fresh Correlator.GenerateReport: %v", err)
	}

	incReport := incRes.Report
	if incReport.DataHash != hashBeads(updatedBeads) || incReport.DataHash != freshReport.DataHash {
		t.Fatalf("incremental DataHash = %q, want %q", incReport.DataHash, freshReport.DataHash)
	}

	// Verify feedback overrides survived incremental update: seedSHA confirmed (1.0), rejectedSHA absent.
	pzpvCommits := make(map[string]CorrelatedCommit)
	for _, cc := range incReport.Histories["bv-pzpv"].Commits {
		pzpvCommits[cc.SHA] = cc
	}
	if _, ok := pzpvCommits[rejectedSHA]; ok {
		t.Fatalf("rejected commit %s reappeared after incremental update", rejectedSHA[:7])
	}
	if seed, ok := pzpvCommits[seedSHA]; !ok || !seed.Confirmed || seed.Confidence != 1.0 {
		t.Fatalf("confirmed commit %s lost confirmation override: %+v (exists=%v)", seedSHA[:7], seed, ok)
	}
	if _, ok := pzpvCommits[temporalMixedSHA]; !ok {
		t.Fatalf("incremental update missed temporal_author mixed commit %s", temporalMixedSHA[:7])
	}

	// Verify explicit_id on dot-child bv-apal.20 and co_committed on bv-fast.
	apalCommits := make(map[string]CorrelatedCommit)
	for _, cc := range incReport.Histories["bv-apal.20"].Commits {
		apalCommits[cc.SHA] = cc
	}
	if _, ok := apalCommits[dotExplicitSHA]; !ok {
		t.Fatalf("incremental update missed explicit_id commit %s on bv-apal.20", dotExplicitSHA[:7])
	}
	if fast := incReport.Histories["bv-fast"].Commits; len(fast) != 1 || fast[0].SHA != coCommitSHA {
		t.Fatalf("incremental update missed co_committed commit %s on bv-fast: %+v", coCommitSHA[:7], fast)
	}

	// Differential parity check across all beads vs fresh full Correlator.
	for _, b := range updatedBeads {
		gotH := incReport.Histories[b.ID]
		wantH := freshReport.Histories[b.ID]
		if len(gotH.Events) != len(wantH.Events) {
			t.Fatalf("bead %s event count = %d, want %d", b.ID, len(gotH.Events), len(wantH.Events))
		}
		if len(gotH.Commits) != len(wantH.Commits) {
			t.Fatalf("bead %s commit count = %d (%+v), want %d (%+v)", b.ID, len(gotH.Commits), gotH.Commits, len(wantH.Commits), wantH.Commits)
		}
		for i := range wantH.Commits {
			if gotH.Commits[i].SHA != wantH.Commits[i].SHA ||
				gotH.Commits[i].Method != wantH.Commits[i].Method ||
				gotH.Commits[i].Confirmed != wantH.Commits[i].Confirmed {
				t.Errorf("bead %s commit[%d] = %+v, want %+v", b.ID, i, gotH.Commits[i], wantH.Commits[i])
			}
		}
	}

	// Verify non-null "commits":[] serialization in incremental report.
	if incReport.Histories["bv-empty"].Commits == nil {
		t.Fatal("bv-empty Commits slice in incremental report must not be nil")
	}
	raw, err := json.Marshal(incReport)
	if err != nil {
		t.Fatalf("json.Marshal(incReport): %v", err)
	}
	if strings.Contains(string(raw), `"commits":null`) || !strings.Contains(string(raw), `"commits":[]`) {
		t.Fatalf("incremental report JSON violated non-null commits invariant: %s", raw)
	}
}
