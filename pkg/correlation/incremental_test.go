package correlation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestIncrementalCorrelator_DeletedAndTombstonedTargetsAndConcurrentGenerateReport(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")

	baseTime := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
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
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-del","title":"Will be deleted","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Will be tombstoned","status":"open"}`+"\n"+
			`{"id":"bv-live","title":"Remains live","status":"open"}`+"\n"+
			`{"id":"101","title":"Deleted numeric A","status":"open"}`+"\n"+
			`{"id":"102","title":"Deleted numeric B","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(0, "chore: seed beads")

	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package app\nvar a = 101\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha101 := commitAt(5*time.Minute, "fix [101]: numeric target A")

	if err := os.WriteFile(filepath.Join(repo, "b.go"), []byte("package app\nvar b = 102\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha102 := commitAt(10*time.Minute, "fix [102]: numeric target B")

	seedBeads := []BeadInfo{
		{ID: "bv-del", Title: "Will be deleted", Status: "open"},
		{ID: "bv-tomb", Title: "Will be tombstoned", Status: "open"},
		{ID: "bv-live", Title: "Remains live", Status: "open"},
	}

	// Prime two IncrementalCorrelators before incremental deletion/tombstone commits:
	// - icRaw exercises tryIncrementalUpdateFromRaw
	// - icReportOnly exercises tryIncrementalUpdate / mergeReportsWithOptions (rawStates cleared)
	icRaw := NewIncrementalCorrelator(repo, jsonl)
	if _, err := icRaw.GenerateReport(seedBeads, CorrelatorOptions{}); err != nil {
		t.Fatalf("seed icRaw: %v", err)
	}
	if _, err := icRaw.GenerateReport(seedBeads, CorrelatorOptions{BeadID: "bv-del"}); err != nil {
		t.Fatalf("seed icRaw bv-del: %v", err)
	}
	if _, err := icRaw.GenerateReport(seedBeads, CorrelatorOptions{BeadID: "bv-tomb"}); err != nil {
		t.Fatalf("seed icRaw bv-tomb: %v", err)
	}

	icReportOnly := NewIncrementalCorrelator(repo, jsonl)
	if _, err := icReportOnly.GenerateReport(seedBeads, CorrelatorOptions{}); err != nil {
		t.Fatalf("seed icReportOnly: %v", err)
	}
	if _, err := icReportOnly.GenerateReport(seedBeads, CorrelatorOptions{BeadID: "bv-del"}); err != nil {
		t.Fatalf("seed icReportOnly bv-del: %v", err)
	}
	if _, err := icReportOnly.GenerateReport(seedBeads, CorrelatorOptions{BeadID: "bv-tomb"}); err != nil {
		t.Fatalf("seed icReportOnly bv-tomb: %v", err)
	}
	if _, err := icReportOnly.GenerateReport(seedBeads, CorrelatorOptions{BeadID: "999"}); err != nil {
		t.Fatalf("seed icReportOnly 999: %v", err)
	}

	// Incremental Commit 1: create numeric bead "999", then touch code mentioning [999].
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-del","title":"Will be deleted","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Will be tombstoned","status":"open"}`+"\n"+
			`{"id":"bv-live","title":"Remains live","status":"open"}`+"\n"+
			`{"id":"999","title":"Ephemeral incremental target","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(20*time.Minute, "chore: create 999")

	if err := os.WriteFile(filepath.Join(repo, "ephemeral.go"), []byte("package app\nvar e = 999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha999 := commitAt(25*time.Minute, "feat [999]: implement incremental target")

	// Incremental Commit 2: delete bv-del and 999, tombstone bv-tomb.
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-tomb","title":"Will be tombstoned","status":"tombstone"}`+"\n"+
			`{"id":"bv-live","title":"Remains live","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(30*time.Minute, "chore: delete bv-del & 999 and tombstone bv-tomb")

	// Clear rawStates on icReportOnly so it must use tryIncrementalUpdate + mergeReportsWithOptions.
	icReportOnly.rawMu.Lock()
	icReportOnly.rawStates = make(map[string]*incrementalRawState)
	icReportOnly.rawMu.Unlock()

	liveBeads := []BeadInfo{
		{ID: "bv-live", Title: "Remains live", Status: "open"},
	}

	for name, ic := range map[string]*IncrementalCorrelator{
		"raw_incremental":         icRaw,
		"report_only_incremental": icReportOnly,
	} {
		t.Run(name, func(t *testing.T) {
			delRes, err := ic.GenerateReportWithDetails(liveBeads, CorrelatorOptions{BeadID: "bv-del"})
			if err != nil {
				t.Fatalf("bv-del incremental: %v", err)
			}
			if !delRes.WasIncremental {
				t.Fatalf("expected WasIncremental=true for bv-del, got false (%s)", delRes.RefreshReason)
			}
			if got := delRes.Report.Histories["bv-del"].Status; got != "deleted" {
				t.Fatalf("bv-del Status = %q, want %q", got, "deleted")
			}

			tombRes, err := ic.GenerateReportWithDetails(liveBeads, CorrelatorOptions{BeadID: "bv-tomb"})
			if err != nil {
				t.Fatalf("bv-tomb incremental: %v", err)
			}
			if !tombRes.WasIncremental {
				t.Fatalf("expected WasIncremental=true for bv-tomb, got false (%s)", tombRes.RefreshReason)
			}
			if got := tombRes.Report.Histories["bv-tomb"].Status; got != "tombstone" {
				t.Fatalf("bv-tomb Status = %q, want %q", got, "tombstone")
			}

			synthRes, err := ic.GenerateReportWithDetails(liveBeads, CorrelatorOptions{BeadID: "999"})
			if err != nil {
				t.Fatalf("999 incremental: %v", err)
			}
			h999, ok := synthRes.Report.Histories["999"]
			if !ok {
				t.Fatalf("expected synthesized history for created+deleted bead 999, got %+v", synthRes.Report.Histories)
			}
			if h999.Status != "deleted" || h999.Title != "Ephemeral incremental target" {
				t.Fatalf("999 synthesized Status=%q Title=%q, want deleted / Ephemeral incremental target", h999.Status, h999.Title)
			}
			if len(h999.Commits) != 1 || h999.Commits[0].SHA != sha999 {
				t.Fatalf("999 commits = %+v, want [%s]", h999.Commits, sha999)
			}
		})
	}

	// Concurrent GenerateReport calls with distinct deleted numeric opts.BeadID values
	// on shared Correlator, CachedCorrelator, and IncrementalCorrelator must not race or clobber.
	sharedCorr := NewCorrelator(repo, jsonl)
	sharedCached := NewCachedCorrelator(repo, jsonl)
	sharedInc := NewIncrementalCorrelator(repo, jsonl)

	var wg sync.WaitGroup
	errCh := make(chan error, 48)
	targets := []struct {
		id      string
		wantSHA string
	}{
		{id: "101", wantSHA: sha101},
		{id: "102", wantSHA: sha102},
		{id: "999", wantSHA: sha999},
	}

	for round := 0; round < 16; round++ {
		tc := targets[round%len(targets)]
		wg.Add(3)
		go func(id, wantSHA string) {
			defer wg.Done()
			rep, err := sharedCorr.GenerateReport(liveBeads, CorrelatorOptions{BeadID: id})
			if err != nil {
				errCh <- err
				return
			}
			h := rep.Histories[id]
			if len(h.Commits) != 1 || h.Commits[0].SHA != wantSHA {
				t.Errorf("sharedCorr[%s] commits = %+v, want [%s]", id, h.Commits, wantSHA)
			}
		}(tc.id, tc.wantSHA)
		go func(id, wantSHA string) {
			defer wg.Done()
			rep, err := sharedCached.GenerateReport(liveBeads, CorrelatorOptions{BeadID: id})
			if err != nil {
				errCh <- err
				return
			}
			h := rep.Histories[id]
			if len(h.Commits) != 1 || h.Commits[0].SHA != wantSHA {
				t.Errorf("sharedCached[%s] commits = %+v, want [%s]", id, h.Commits, wantSHA)
			}
		}(tc.id, tc.wantSHA)
		go func(id, wantSHA string) {
			defer wg.Done()
			rep, err := sharedInc.GenerateReport(liveBeads, CorrelatorOptions{BeadID: id})
			if err != nil {
				errCh <- err
				return
			}
			h := rep.Histories[id]
			if len(h.Commits) != 1 || h.Commits[0].SHA != wantSHA {
				t.Errorf("sharedInc[%s] commits = %+v, want [%s]", id, h.Commits, wantSHA)
			}
		}(tc.id, tc.wantSHA)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent GenerateReport error: %v", err)
	}
}

func TestIncrementalCorrelator_ReportOnlyFallbackPrunesDeletedAndRefreshesWiderSlice(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")

	baseTime := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
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
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-1","title":"Live bead one","status":"open"}`+"\n"+
			`{"id":"bv-b9c8","title":"Live hash bead","status":"open"}`+"\n"+
			`{"id":"bv-del","title":"Deleted later","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Tombstoned later","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(0, "chore: seed four beads")

	if err := os.WriteFile(filepath.Join(repo, "one.go"), []byte("package p\nvar one = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1 := commitAt(5*time.Minute, "feat(bv-1): implement one")

	if err := os.WriteFile(filepath.Join(repo, "two.go"), []byte("package p\nvar two = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	shaHash := commitAt(10*time.Minute, "fix(bv-b9c8): implement hash bead")

	// Part A: Narrow [bv-1] -> wider [bv-1, bv-b9c8] on report-only fallback must NOT
	// reuse the narrower report and drop bv-b9c8's historical commits.
	icNarrowToWide := NewIncrementalCorrelator(repo, jsonl)
	if _, err := icNarrowToWide.GenerateReport([]BeadInfo{{ID: "bv-1", Title: "Live bead one", Status: "open"}}, CorrelatorOptions{}); err != nil {
		t.Fatalf("seed narrow report: %v", err)
	}
	icNarrowToWide.rawMu.Lock()
	icNarrowToWide.rawStates = make(map[string]*incrementalRawState)
	icNarrowToWide.rawMu.Unlock()

	widerBeads := []BeadInfo{
		{ID: "bv-1", Title: "Live bead one", Status: "open"},
		{ID: "bv-b9c8", Title: "Live hash bead", Status: "open"},
	}
	wideRes, err := icNarrowToWide.GenerateReportWithDetails(widerBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("wider GenerateReportWithDetails: %v", err)
	}
	if wideRes.WasIncremental {
		t.Fatal("widening bead slice on report-only fallback must trigger full refresh rather than reusing incomplete narrow report")
	}
	if len(wideRes.Report.Histories["bv-1"].Commits) != 1 || wideRes.Report.Histories["bv-1"].Commits[0].SHA != sha1 {
		t.Fatalf("bv-1 commits = %+v, want [%s]", wideRes.Report.Histories["bv-1"].Commits, sha1)
	}
	if len(wideRes.Report.Histories["bv-b9c8"].Commits) != 1 || wideRes.Report.Histories["bv-b9c8"].Commits[0].SHA != shaHash {
		t.Fatalf("bv-b9c8 commits = %+v, want [%s]", wideRes.Report.Histories["bv-b9c8"].Commits, shaHash)
	}

	// Same ID set with Title/Status edit and cleared rawStates MUST hit report-only fallback incrementally.
	icNarrowToWide.rawMu.Lock()
	icNarrowToWide.rawStates = make(map[string]*incrementalRawState)
	icNarrowToWide.rawMu.Unlock()
	retitledBeads := []BeadInfo{
		{ID: "bv-1", Title: "Retitled one", Status: "in_progress"},
		{ID: "bv-b9c8", Title: "Live hash bead", Status: "closed"},
	}
	metaRes, err := icNarrowToWide.GenerateReportWithDetails(retitledBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("retitled GenerateReportWithDetails: %v", err)
	}
	if !metaRes.WasIncremental {
		t.Fatalf("expected report-only fallback hit on metadata edit with identical ID set, got full refresh (%s)", metaRes.RefreshReason)
	}
	if metaRes.Report.Histories["bv-1"].Title != "Retitled one" || len(metaRes.Report.Histories["bv-b9c8"].Commits) != 1 {
		t.Fatalf("unexpected retitled incremental result: %+v", metaRes.Report.Histories)
	}

	// Part B: Unscoped icReportOnly must prune deleted & tombstoned beads from Histories/TotalBeads.
	icPrune := NewIncrementalCorrelator(repo, jsonl)
	seedAll := []BeadInfo{
		{ID: "bv-1", Title: "Live bead one", Status: "open"},
		{ID: "bv-b9c8", Title: "Live hash bead", Status: "open"},
		{ID: "bv-del", Title: "Deleted later", Status: "open"},
		{ID: "bv-tomb", Title: "Tombstoned later", Status: "open"},
	}
	if _, err := icPrune.GenerateReport(seedAll, CorrelatorOptions{}); err != nil {
		t.Fatalf("seed icPrune: %v", err)
	}
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-1","title":"Live bead one","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Tombstoned later","status":"tombstone"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(20*time.Minute, "chore: delete bv-del & bv-b9c8 and tombstone bv-tomb")

	icPrune.rawMu.Lock()
	icPrune.rawStates = make(map[string]*incrementalRawState)
	icPrune.rawMu.Unlock()

	liveOnly := []BeadInfo{{ID: "bv-1", Title: "Live bead one", Status: "open"}}
	prunedRes, err := icPrune.GenerateReportWithDetails(liveOnly, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("unscoped icPrune GenerateReportWithDetails: %v", err)
	}
	if !prunedRes.WasIncremental {
		t.Fatalf("expected WasIncremental=true when shrinking beads slice on deletion, got false (%s)", prunedRes.RefreshReason)
	}
	if prunedRes.Report.Stats.TotalBeads != 1 || len(prunedRes.Report.Histories) != 1 {
		t.Fatalf("expected TotalBeads=1 and Histories=[bv-1], got TotalBeads=%d keys=%+v",
			prunedRes.Report.Stats.TotalBeads, prunedRes.Report.Histories)
	}
	if _, leakedDel := prunedRes.Report.Histories["bv-del"]; leakedDel {
		t.Fatal("deleted bead bv-del leaked into unscoped report-only incremental Histories")
	}
	if _, leakedTomb := prunedRes.Report.Histories["bv-tomb"]; leakedTomb {
		t.Fatal("tombstoned bead bv-tomb leaked into unscoped report-only incremental Histories")
	}
}
