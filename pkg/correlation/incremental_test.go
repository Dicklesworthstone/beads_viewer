package correlation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIncrementalThreshold(t *testing.T) {
	if IncrementalThreshold != 100 {
		t.Errorf("IncrementalThreshold = %d, want 100", IncrementalThreshold)
	}
}

func TestIncrementalOptionsSupportedOnlyForUnboundedAllHistory(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		opts CorrelatorOptions
		want bool
	}{
		{name: "all history", want: true},
		{name: "bead filter", opts: CorrelatorOptions{BeadID: "bv-1"}},
		{name: "since", opts: CorrelatorOptions{Since: &now}},
		{name: "until", opts: CorrelatorOptions{Until: &now}},
		{name: "limit", opts: CorrelatorOptions{Limit: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := incrementalOptionsSupported(tt.opts); got != tt.want {
				t.Fatalf("incrementalOptionsSupported()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIncrementalCorrelatorWithContextNilReceiverIsSafe(t *testing.T) {
	var correlator *IncrementalCorrelator
	if got := correlator.WithContext(context.Background()); got != nil {
		t.Fatalf("nil receiver returned %p, want nil", got)
	}
}

func TestBuildCacheKeyContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildCacheKeyContext(ctx, initTempGitRepo(t), nil, CorrelatorOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cache-key lookup error = %v, want context.Canceled", err)
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

	// Check merged report
	if want := hashBeads(beads); merged.DataHash != want {
		t.Errorf("DataHash = %s, want current bead hash %s", merged.DataHash, want)
	}

	if merged.GitRange != existing.GitRange {
		t.Errorf("GitRange = %s, want stable range %q", merged.GitRange, existing.GitRange)
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

func TestMergeReportsUpdatesLastAuthorFromLifecycleEvent(t *testing.T) {
	existing := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID:     "bv-1",
			LastAuthor: "Old Author",
			Events:     []BeadEvent{{BeadID: "bv-1", Author: "Old Author"}},
		},
	}}
	beads := []BeadInfo{{ID: "bv-1"}}
	newEvents := []BeadEvent{{BeadID: "bv-1", Author: "New Author", EventType: EventModified}}

	merged := mergeReports(existing, beads, newEvents, nil)
	if got := merged.Histories["bv-1"].LastAuthor; got != "New Author" {
		t.Fatalf("LastAuthor=%q, want lifecycle event author", got)
	}
}

func TestMergeReportsDoesNotLetOlderCommitOverrideNewerLifecycleAuthor(t *testing.T) {
	now := time.Now()
	existing := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID:  "bv-1",
			Commits: []CorrelatedCommit{{SHA: "old", Author: "Code Author", Timestamp: now.Add(-2 * time.Hour)}},
		},
	}}
	newEvents := []BeadEvent{{
		BeadID:    "bv-1",
		Author:    "Lifecycle Author",
		EventType: EventModified,
		Timestamp: now,
	}}

	merged := mergeReports(existing, []BeadInfo{{ID: "bv-1"}}, newEvents, nil)
	if got := merged.Histories["bv-1"].LastAuthor; got != "Lifecycle Author" {
		t.Fatalf("LastAuthor=%q, want newer lifecycle author", got)
	}
}

func TestMergeReportsDoesNotAliasCachedHistoryInternals(t *testing.T) {
	event := BeadEvent{BeadID: "bv-1", EventType: EventCreated, Author: "Original"}
	file := FileChange{Path: "original.go"}
	existing := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID:     "bv-1",
			Events:     []BeadEvent{event},
			Milestones: GetBeadMilestones([]BeadEvent{event}),
			Commits:    []CorrelatedCommit{{SHA: "abc", Files: []FileChange{file}}},
		},
	}}

	merged := mergeReports(existing, []BeadInfo{{ID: "bv-1"}}, nil, nil)
	history := merged.Histories["bv-1"]
	history.Events[0].Author = "Changed"
	history.Commits[0].Files[0].Path = "changed.go"

	if got := existing.Histories["bv-1"].Events[0].Author; got != "Original" {
		t.Fatalf("cached event was mutated through merged report: %q", got)
	}
	if got := existing.Histories["bv-1"].Commits[0].Files[0].Path; got != "original.go" {
		t.Fatalf("cached file change was mutated through merged report: %q", got)
	}
	if history.Milestones.Created == existing.Histories["bv-1"].Milestones.Created {
		t.Fatal("merged milestone still aliases cached report milestone")
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

func TestGetCommitsBetweenPinsThroughAndRejectsDivergence(t *testing.T) {
	repo := initTempGitRepo(t)
	base, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve base HEAD: %v", err)
	}

	advanceGitHead(t, repo, "first")
	first, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve first HEAD: %v", err)
	}
	advanceGitHead(t, repo, "second")
	second, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve second HEAD: %v", err)
	}

	commits, err := getCommitsBetween(context.Background(), repo, base, first)
	if err != nil {
		t.Fatalf("get pinned commit range: %v", err)
	}
	if len(commits) != 1 || commits[0] != first {
		t.Fatalf("pinned range = %v, want only %s (current HEAD is %s)", commits, first, second)
	}
	if count, err := countCommitsBetween(context.Background(), repo, base, first); err != nil || count != 1 {
		t.Fatalf("pinned count = %d, %v; want 1, nil", count, err)
	}

	runGit(t, repo, "checkout", "--detach", base)
	advanceGitHead(t, repo, "diverged")
	diverged, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve diverged HEAD: %v", err)
	}
	if _, err := getCommitsBetween(context.Background(), repo, first, diverged); err == nil {
		t.Fatal("diverged cached commit was accepted as an incremental base")
	}
}

func TestIncrementalReportMatchesFullReportAfterMetadataChange(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_ROBOT", "1")

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	writeBeads := func(content, message string) {
		t.Helper()
		if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write beads fixture: %v", err)
		}
		runGit(t, repo, "add", ".beads/issues.jsonl")
		runGit(t, repo, "commit", "-m", message)
	}

	writeBeads("{\"id\":\"bv-1\",\"title\":\"Original\",\"status\":\"open\"}\n", "create bead")
	ic := NewIncrementalCorrelator(repo)
	initialBeads := []BeadInfo{{ID: "bv-1", Title: "Original", Status: "open"}}
	initial, err := ic.GenerateReportWithDetails(initialBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("initial full report: %v", err)
	}
	if initial.WasIncremental {
		t.Fatal("initial report unexpectedly incremental")
	}

	writeBeads("{\"id\":\"bv-1\",\"title\":\"Renamed\",\"status\":\"in_progress\"}\n", "claim bead")
	currentBeads := []BeadInfo{{ID: "bv-1", Title: "Renamed", Status: "in_progress"}}
	incremental, err := ic.GenerateReportWithDetails(currentBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("incremental report: %v", err)
	}
	if !incremental.WasIncremental || incremental.NewCommitCount != 1 {
		t.Fatalf("update details = %+v, want one-commit incremental update", incremental)
	}

	full, err := NewCorrelator(repo).GenerateReport(currentBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("comparison full report: %v", err)
	}
	got := *incremental.Report
	want := *full
	got.GeneratedAt = time.Time{}
	want.GeneratedAt = time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental report differs from full rebuild:\n incremental=%+v\n full=%+v", got, want)
	}
}

func TestIncrementalRenameFallsBackAndMatchesFullHistory(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_ROBOT", "1")

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	legacyPath := filepath.Join(beadsDir, "beads.jsonl")
	writeLegacy := func(status, message string) {
		t.Helper()
		content := "{\"id\":\"bv-rename\",\"title\":\"Rename\",\"status\":\"" + status + "\"}\n"
		if err := os.WriteFile(legacyPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write legacy beads state %q: %v", status, err)
		}
		runGit(t, repo, "add", ".beads/beads.jsonl")
		runGit(t, repo, "commit", "-m", message)
	}

	writeLegacy("open", "create rename fixture bead")
	baseSHA, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve incremental base: %v", err)
	}
	openBeads := []BeadInfo{{ID: "bv-rename", Title: "Rename", Status: "open"}}
	baseReport, err := NewCorrelator(repo).GenerateReport(openBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("generate base report: %v", err)
	}

	// The lifecycle transition occurs on the legacy path before that path is
	// renamed. A current-path --no-walk query for issues.jsonl cannot see the
	// transition commit, while a full --follow extraction can.
	writeLegacy("closed", "close bead on legacy path")
	runGit(t, repo, "mv", ".beads/beads.jsonl", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "rename beads database")

	closedBeads := []BeadInfo{{ID: "bv-rename", Title: "Rename", Status: "closed"}}
	ic := NewIncrementalCorrelator(repo)
	if got := ic.correlator.extractor.primaryBeadsFile(); got != ".beads/issues.jsonl" {
		t.Fatalf("current primary Beads path=%q, want issues.jsonl", got)
	}
	currentKey, err := buildCacheKeyContext(context.Background(), repo, closedBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("build current cache key: %v", err)
	}
	baseKey := currentKey
	baseKey.HeadSHA = baseSHA
	ic.cache.Put(baseKey, baseReport)

	result, err := ic.GenerateReportWithDetails(closedBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("generate report across rename: %v", err)
	}
	if result.WasIncremental {
		t.Fatalf("rename range used unsafe incremental extraction: %+v", result)
	}

	full, err := NewCorrelator(repo).GenerateReport(closedBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("comparison full report: %v", err)
	}
	got := *result.Report
	want := *full
	got.GeneratedAt = time.Time{}
	want.GeneratedAt = time.Time{}
	// IncrementalCorrelator records the exact processed HEAD as its cache cursor;
	// a plain full report records the newest lifecycle-event SHA instead.
	want.LatestCommitSHA = got.LatestCommitSHA
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rename fallback differs from full rebuild:\n got=%+v\nwant=%+v", got, want)
	}
	history := got.Histories["bv-rename"]
	if len(history.Events) != 2 || history.Events[0].EventType != EventCreated || history.Events[1].EventType != EventClosed {
		t.Fatalf("rename fallback lost legacy-path lifecycle event: %+v", history.Events)
	}
}

func TestIncrementalCorrelatorBypassesShallowCacheAcrossSameHeadDeepen(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("BV_ROBOT", "1")

	source := initTempGitRepo(t)
	beadsDir := filepath.Join(source, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	writeState := func(status, message string) {
		t.Helper()
		content := "{\"id\":\"bv-shallow\",\"title\":\"Shallow\",\"status\":\"" + status + "\"}\n"
		if err := os.WriteFile(beadsPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write beads state %q: %v", status, err)
		}
		runGit(t, source, "add", ".beads/issues.jsonl")
		runGit(t, source, "commit", "-m", message)
	}
	writeState("open", "create shallow fixture bead")
	writeState("closed", "close shallow fixture bead")

	shallow := cloneShallowRepoForCacheTest(t, source)
	headBefore, err := getGitHead(shallow)
	if err != nil {
		t.Fatalf("resolve shallow HEAD: %v", err)
	}
	ic := NewIncrementalCorrelator(shallow)
	beads := []BeadInfo{{ID: "bv-shallow", Title: "Shallow", Status: "closed"}}

	for i := 0; i < 2; i++ {
		result, reportErr := ic.GenerateReportWithDetails(beads, CorrelatorOptions{})
		if reportErr != nil {
			t.Fatalf("shallow report %d: %v", i+1, reportErr)
		}
		if result.WasIncremental || result.RefreshReason != "repository history is shallow or unavailable" {
			t.Fatalf("shallow report %d details=%+v", i+1, result)
		}
	}
	if ic.cache.Size() != 0 {
		t.Fatalf("incremental cache retained %d shallow reports", ic.cache.Size())
	}

	runGit(t, shallow, "fetch", "--unshallow", "origin")
	if headAfter, headErr := getGitHead(shallow); headErr != nil || headAfter != headBefore {
		t.Fatalf("deepen changed HEAD: before=%q after=%q error=%v", headBefore, headAfter, headErr)
	}
	full, err := ic.GenerateReportWithDetails(beads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("full-history report after deepen: %v", err)
	}
	if full.WasIncremental {
		t.Fatalf("first post-deepen report unexpectedly used cache/incremental path: %+v", full)
	}
	history := full.Report.Histories["bv-shallow"]
	foundClosed := false
	for _, event := range history.Events {
		if event.EventType == EventClosed {
			foundClosed = true
		}
	}
	if !foundClosed {
		t.Fatalf("post-deepen report served incomplete shallow history: %+v", history)
	}
	if ic.cache.Size() != 1 {
		t.Fatalf("full-history report was not cached; size=%d", ic.cache.Size())
	}

	hit, err := ic.GenerateReportWithDetails(beads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("full-history cache hit: %v", err)
	}
	if !hit.WasIncremental || hit.NewCommitCount != 0 || hit.Report != full.Report {
		t.Fatalf("post-deepen exact hit=%+v, want cached full-history report", hit)
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
