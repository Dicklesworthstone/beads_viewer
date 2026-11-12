package correlation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func TestBuildHistories_Empty(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	histories := c.buildHistories(nil, nil, nil)

	if len(histories) != 0 {
		t.Errorf("expected empty histories, got %d", len(histories))
	}
}

func TestBuildHistories_Basic(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	beads := []BeadInfo{
		{ID: "bv-1", Title: "Task 1", Status: "open"},
		{ID: "bv-2", Title: "Task 2", Status: "closed"},
	}

	now := time.Now()
	events := []BeadEvent{
		{BeadID: "bv-1", EventType: EventCreated, Timestamp: now.Add(-24 * time.Hour), Author: "Alice"},
		{BeadID: "bv-1", EventType: EventClaimed, Timestamp: now.Add(-12 * time.Hour), Author: "Alice"},
		{BeadID: "bv-2", EventType: EventCreated, Timestamp: now.Add(-48 * time.Hour), Author: "Bob"},
		{BeadID: "bv-2", EventType: EventClosed, Timestamp: now.Add(-1 * time.Hour), Author: "Bob"},
	}

	histories := c.buildHistories(beads, events, nil)

	if len(histories) != 2 {
		t.Errorf("expected 2 histories, got %d", len(histories))
	}

	h1 := histories["bv-1"]
	if len(h1.Events) != 2 {
		t.Errorf("expected 2 events for bv-1, got %d", len(h1.Events))
	}
	if h1.Milestones.Created == nil {
		t.Error("expected bv-1 to have created milestone")
	}
	if h1.Milestones.Claimed == nil {
		t.Error("expected bv-1 to have claimed milestone")
	}

	h2 := histories["bv-2"]
	if len(h2.Events) != 2 {
		t.Errorf("expected 2 events for bv-2, got %d", len(h2.Events))
	}
	if h2.CycleTime == nil {
		t.Error("expected bv-2 to have cycle time (closed bead)")
	}
}

func TestBuildCommitIndex(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	histories := map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Commits: []CorrelatedCommit{
				{SHA: "abc123", Method: MethodCoCommitted},
				{SHA: "def456", Method: MethodCoCommitted},
			},
		},
		"bv-2": {
			BeadID: "bv-2",
			Commits: []CorrelatedCommit{
				{SHA: "abc123", Method: MethodCoCommitted}, // Same commit, different bead
				{SHA: "ghi789", Method: MethodCoCommitted},
			},
		},
	}

	index := c.buildCommitIndex(histories)

	if len(index) != 3 {
		t.Errorf("expected 3 unique commits in index, got %d", len(index))
	}

	// abc123 should reference both beads
	if len(index["abc123"]) != 2 {
		t.Errorf("expected abc123 to reference 2 beads, got %d", len(index["abc123"]))
	}
	if got := index["abc123"]; got[0] != "bv-1" || got[1] != "bv-2" {
		t.Fatalf("expected sorted abc123 bead IDs, got %v", got)
	}

	// Duplicate history entries must not produce duplicate reverse links.
	history := histories["bv-2"]
	history.Commits = append(history.Commits, CorrelatedCommit{SHA: "abc123"})
	histories["bv-2"] = history
	if got := BuildCommitIndex(histories)["abc123"]; len(got) != 2 {
		t.Fatalf("expected duplicate commit link to collapse, got %v", got)
	}
}

func TestCalculateStats_Empty(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	stats := c.calculateStats(make(map[string]BeadHistory), nil)

	if stats.TotalBeads != 0 {
		t.Errorf("expected 0 total beads, got %d", stats.TotalBeads)
	}
	if stats.BeadsWithCommits != 0 {
		t.Errorf("expected 0 beads with commits, got %d", stats.BeadsWithCommits)
	}
}

func TestCalculateStats_WithData(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	claimToClose := 24 * time.Hour
	histories := map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Events: []BeadEvent{
				{Author: "Alice"},
			},
			Commits: []CorrelatedCommit{
				{SHA: "abc123", Author: "Alice", Method: MethodCoCommitted},
			},
			CycleTime: &CycleTime{ClaimToClose: &claimToClose},
		},
		"bv-2": {
			BeadID: "bv-2",
			Events: []BeadEvent{
				{Author: "Bob"},
			},
			Commits: []CorrelatedCommit{
				{SHA: "def456", Author: "Bob", Method: MethodExplicitID},
			},
		},
	}

	stats := c.calculateStats(histories, nil)

	if stats.TotalBeads != 2 {
		t.Errorf("expected 2 total beads, got %d", stats.TotalBeads)
	}
	if stats.BeadsWithCommits != 2 {
		t.Errorf("expected 2 beads with commits, got %d", stats.BeadsWithCommits)
	}
	if stats.TotalCommits != 2 {
		t.Errorf("expected 2 total commits, got %d", stats.TotalCommits)
	}
	if stats.UniqueAuthors != 2 {
		t.Errorf("expected 2 unique authors, got %d", stats.UniqueAuthors)
	}
	if stats.MethodDistribution["co_committed"] != 1 {
		t.Errorf("expected 1 co_committed, got %d", stats.MethodDistribution["co_committed"])
	}
	if stats.MethodDistribution["explicit_id"] != 1 {
		t.Errorf("expected 1 explicit_id, got %d", stats.MethodDistribution["explicit_id"])
	}
	if stats.AvgCycleTimeDays == nil {
		t.Error("expected avg cycle time to be set")
	}
}

func TestDescribeGitRange(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	tests := []struct {
		name     string
		opts     CorrelatorOptions
		expected string
	}{
		{
			name:     "no filters",
			opts:     CorrelatorOptions{},
			expected: "all history",
		},
		{
			name:     "with limit",
			opts:     CorrelatorOptions{Limit: 100},
			expected: "limit 100 commits",
		},
		{
			name: "with since",
			opts: func() CorrelatorOptions {
				since := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
				return CorrelatorOptions{Since: &since}
			}(),
			expected: "since 2024-01-15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.describeGitRange(tt.opts)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestCalculateDataHash(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	beads1 := []BeadInfo{
		{ID: "bv-1", Status: "open"},
		{ID: "bv-2", Status: "closed"},
	}

	beads2 := []BeadInfo{
		{ID: "bv-1", Status: "open"},
		{ID: "bv-2", Status: "open"}, // Different status
	}
	beads3 := []BeadInfo{
		{ID: "bv-2", Status: "closed"},
		{ID: "bv-1", Status: "open"},
	}

	hash1 := c.calculateDataHash(beads1)
	hash2 := c.calculateDataHash(beads2)
	hash3 := c.calculateDataHash(beads3)

	if hash1 == hash2 {
		t.Error("different bead data should produce different hashes")
	}
	if hash1 != hash3 {
		t.Error("equivalent bead data in different order should produce the same hash")
	}

	// Same data should produce same hash
	hash1Again := c.calculateDataHash(beads1)
	if hash1 != hash1Again {
		t.Error("same bead data should produce same hash")
	}
}

func TestDedupCommits(t *testing.T) {
	commits := []CorrelatedCommit{
		{SHA: "abc123", Message: "First"},
		{SHA: "def456", Message: "Second"},
		{SHA: "abc123", Message: "First duplicate"}, // Duplicate SHA
		{SHA: "ghi789", Message: "Third"},
	}

	result := dedupCommits(commits)

	if len(result) != 3 {
		t.Errorf("expected 3 unique commits, got %d", len(result))
	}

	// First occurrence should be kept
	if result[0].Message != "First" {
		t.Errorf("expected first commit message to be 'First', got %s", result[0].Message)
	}
}

func TestNewCorrelator(t *testing.T) {
	c := NewCorrelator("/tmp/test")
	if c.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", c.repoPath)
	}
	if c.extractor == nil {
		t.Error("extractor should not be nil")
	}
	if c.coCommitter == nil {
		t.Error("coCommitter should not be nil")
	}
}

func TestValidateRepository_NoGitDir(t *testing.T) {
	err := ValidateRepository("/nonexistent/path")
	if err == nil {
		t.Error("ValidateRepository should fail for nonexistent path")
	}
}

func TestValidateRepository_NoBeadsFile(t *testing.T) {
	// Use temp dir that exists but has no beads
	err := ValidateRepository("/tmp")
	if err == nil {
		t.Error("ValidateRepository should fail without beads file")
	}
}

func TestFindLatestCommitSHA_Empty(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	sha := c.findLatestCommitSHA(nil, nil)
	if sha != "" {
		t.Errorf("findLatestCommitSHA with empty inputs should return empty, got %s", sha)
	}
}

func TestFindLatestCommitSHA_FromEvents(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	now := time.Now()
	events := []BeadEvent{
		{CommitSHA: "older", Timestamp: now.Add(-1 * time.Hour)},
		{CommitSHA: "newest", Timestamp: now},
		{CommitSHA: "middle", Timestamp: now.Add(-30 * time.Minute)},
	}

	sha := c.findLatestCommitSHA(events, nil)
	if sha != "newest" {
		t.Errorf("findLatestCommitSHA = %s, want 'newest'", sha)
	}
}

func TestFindLatestCommitSHA_FromCommits(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	now := time.Now()
	commits := []CorrelatedCommit{
		{SHA: "commit_old", Timestamp: now.Add(-1 * time.Hour)},
		{SHA: "commit_newest", Timestamp: now},
	}

	sha := c.findLatestCommitSHA(nil, commits)
	if sha != "commit_newest" {
		t.Errorf("findLatestCommitSHA = %s, want 'commit_newest'", sha)
	}
}

func TestFindLatestCommitSHA_Mixed(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	now := time.Now()
	events := []BeadEvent{
		{CommitSHA: "event_sha", Timestamp: now.Add(-1 * time.Hour)},
	}
	commits := []CorrelatedCommit{
		{SHA: "commit_sha", Timestamp: now}, // This is newer
	}

	sha := c.findLatestCommitSHA(events, commits)
	if sha != "commit_sha" {
		t.Errorf("findLatestCommitSHA = %s, want 'commit_sha' (newer)", sha)
	}
}

func TestBuildHistories_WithCommits(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	beads := []BeadInfo{
		{ID: "bv-1", Title: "Task 1", Status: "in_progress"},
	}

	now := time.Now()
	events := []BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, Timestamp: now, CommitSHA: "abc123"},
	}

	commits := []CorrelatedCommit{
		{SHA: "abc123", Author: "Test Author", BeadID: "bv-1"},
	}

	histories := c.buildHistories(beads, events, commits)

	h := histories["bv-1"]
	if len(h.Commits) != 1 {
		t.Errorf("expected 1 commit, got %d", len(h.Commits))
	}
	if h.LastAuthor != "Test Author" {
		t.Errorf("LastAuthor = %s, want 'Test Author'", h.LastAuthor)
	}
}

func TestCalculateStats_AvgCommitsPerBead(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	histories := map[string]BeadHistory{
		"bv-1": {
			Commits: []CorrelatedCommit{{SHA: "a1"}, {SHA: "a2"}},
		},
		"bv-2": {
			Commits: []CorrelatedCommit{{SHA: "b1"}},
		},
		"bv-3": {
			Commits: nil, // No commits
		},
	}

	stats := c.calculateStats(histories, nil)

	// 3 commits / 2 beads with commits = 1.5
	if stats.AvgCommitsPerBead != 1.5 {
		t.Errorf("AvgCommitsPerBead = %v, want 1.5", stats.AvgCommitsPerBead)
	}
}

func TestDescribeGitRange_Combined(t *testing.T) {
	c := NewCorrelator("/tmp/test")

	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
	opts := CorrelatorOptions{
		Since: &since,
		Until: &until,
		Limit: 100,
	}

	result := c.describeGitRange(opts)

	if result != "since 2024-01-01, until 2024-12-31, limit 100 commits" {
		t.Errorf("unexpected result: %s", result)
	}
}

// TestAssembleReport_AppliesFeedback proves the feedback loop changes report
// output (C4): a rejected (commit, bead) pair disappears from the bead's
// commits and the commit index, a confirmed pair is pinned to confidence 1.0,
// untouched pairs and other beads are unchanged, and the cached artifact the
// report was assembled from is never mutated.
func TestAssembleReport_AppliesFeedback(t *testing.T) {
	store := NewFeedbackStore(t.TempDir())
	if err := store.Load(); err != nil {
		t.Fatalf("load empty store: %v", err)
	}
	if err := store.Reject("aaa", "bv-9", "tester", 0.9, "unrelated refactor"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if err := store.Confirm("bbb", "bv-9", "tester", 0.5, ""); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// Feedback about a pair the report does not contain must be ignored, not
	// invented into the output.
	if err := store.Confirm("zzz", "bv-9", "tester", 0.5, ""); err != nil {
		t.Fatalf("confirm stray: %v", err)
	}

	art := &historyArtifact{
		Commits: []CorrelatedCommit{
			{SHA: "aaa", BeadID: "bv-9", Method: "explicit", Confidence: 0.9, Author: "a"},
			{SHA: "bbb", BeadID: "bv-9", Method: "temporal", Confidence: 0.5, Author: "b"},
			{SHA: "ccc", BeadID: "bv-9", Method: "cocommit", Confidence: 0.7, Author: "c"},
			{SHA: "aaa", BeadID: "bv-10", Method: "explicit", Confidence: 0.9, Author: "a"},
		},
	}
	beads := []BeadInfo{{ID: "bv-9", Title: "nine", Status: "open"}, {ID: "bv-10", Title: "ten", Status: "open"}}

	withStore := (&Correlator{}).WithFeedbackStore(store)
	report := withStore.assembleReport(beads, CorrelatorOptions{}, art)

	nine := report.Histories["bv-9"]
	if len(nine.Commits) != 2 {
		t.Fatalf("bv-9 commits after feedback = %d (%+v); want 2 (aaa rejected)", len(nine.Commits), nine.Commits)
	}
	for _, c := range nine.Commits {
		switch c.SHA {
		case "aaa":
			t.Fatalf("rejected commit aaa still listed for bv-9: %+v", c)
		case "bbb":
			if c.Confidence != 1.0 || !c.Confirmed {
				t.Fatalf("confirmed commit bbb: confidence=%v confirmed=%v; want 1.0/true", c.Confidence, c.Confirmed)
			}
			if !strings.Contains(c.Reason, "confirmed by feedback (tester)") {
				t.Fatalf("confirmed commit reason should say so: %q", c.Reason)
			}
		case "ccc":
			if c.Confidence != 0.7 || c.Confirmed {
				t.Fatalf("untouched commit ccc changed: %+v", c)
			}
		}
	}
	if beadsForAAA := report.CommitIndex["aaa"]; len(beadsForAAA) != 1 || beadsForAAA[0] != "bv-10" {
		t.Fatalf("commit_index[aaa]=%v; want only bv-10 (bv-9 rejected, bv-10 untouched)", beadsForAAA)
	}
	if ten := report.Histories["bv-10"]; len(ten.Commits) != 1 || ten.Commits[0].Confidence != 0.9 {
		t.Fatalf("bv-10 must be unaffected by bv-9 feedback: %+v", ten.Commits)
	}
	if fa := report.Stats.FeedbackApplied; fa == nil || fa.Confirmed != 1 || fa.Rejected != 1 || fa.Ignored != 0 {
		t.Fatalf("stats.feedback_applied=%+v; want confirmed=1 rejected=1 ignored=0", fa)
	}

	// The artifact (what the disk/HEAD caches hold) is untouched, so feedback
	// never needs a cache invalidation of the git walk.
	if len(art.Commits) != 4 || art.Commits[0].Confidence != 0.9 || art.Commits[1].Confidence != 0.5 {
		t.Fatalf("assembleReport mutated the artifact: %+v", art.Commits)
	}

	// Without a store nothing is applied and the payload says so (nil).
	plain := (&Correlator{}).assembleReport(beads, CorrelatorOptions{}, art)
	if plain.Stats.FeedbackApplied != nil {
		t.Fatalf("no store attached: feedback_applied should be nil, got %+v", plain.Stats.FeedbackApplied)
	}
	if len(plain.Histories["bv-9"].Commits) != 3 {
		t.Fatalf("no store attached: bv-9 should keep all 3 commits, got %d", len(plain.Histories["bv-9"].Commits))
	}
}

// rootCorrelationRepo reproduces bv-pzpv: the only commit creates the bead
// alongside a code file. There is no claimed/closed event to supply a co-commit
// correlation, so the message matcher must recognize the explicit reference.
func rootCorrelationRepo(t *testing.T, beadID, message string) (repo, jsonl, sha string) {
	t.Helper()
	repo = t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "alice@example.com")
	runGit(t, repo, "config", "user.name", "Alice")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	jsonl = filepath.Join(beadsDir, "issues.jsonl")
	data, err := json.Marshal(map[string]any{
		"id": beadID, "title": "One", "status": "open", "priority": 1, "issue_type": "task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl", "feature.go")
	runGit(t, repo, "commit", "-m", message)
	sha, err = getGitHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	return repo, jsonl, sha
}

func TestCorrelator_CreatingCommitExplicitNumericID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    int
	}{
		{"lowercase scope", "feat(hist-1): add export history", 1},
		{"mixed case scope", "feat(Hist-1): add export history", 1},
		{"body reference", "feat: add export history\n\nImplements the work tracked in hist-1.", 1},
		{"no reference", "feat: add export history", 0},
		{"different identifier", "feat(hist-1abc): add unrelated work", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, jsonl, sha := rootCorrelationRepo(t, "hist-1", tc.message)
			report, err := NewCorrelator(repo, jsonl).GenerateReport(
				[]BeadInfo{{ID: "hist-1", Title: "One", Status: "open"}}, CorrelatorOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			history := report.Histories["hist-1"]
			if len(history.Events) != 1 || history.Events[0].EventType != EventCreated || history.Events[0].CommitSHA != sha {
				t.Fatalf("fixture did not retain its creating commit: %+v", history.Events)
			}
			if len(history.Commits) != tc.want || report.Stats.TotalCommits != tc.want || report.Stats.BeadsWithCommits != tc.want {
				t.Fatalf("creating-commit correlations=%d stats=%+v, want %d", len(history.Commits), report.Stats, tc.want)
			}
			if tc.want == 1 {
				commit := history.Commits[0]
				if commit.SHA != sha || commit.Method != MethodExplicitID || len(commit.Files) != 1 || commit.Files[0].Path != "feature.go" {
					t.Fatalf("creating commit lost its explicit evidence or code files: %+v", commit)
				}
				if ids := report.CommitIndex[sha]; len(ids) != 1 || ids[0] != "hist-1" {
					t.Fatalf("reverse index lost creating-commit link: %v", ids)
				}
			}
			if report.Stats.MethodDistribution[MethodCoCommitted.String()] != 0 {
				t.Fatal("creation alone synthesized a co-commit correlation")
			}
		})
	}
}

// multiStrategyRepo builds a git fixture where one code commit is found by
// two strategies at once (it changes the beads file AND names the bead in
// its subject), another is explicit-only, and a third is temporal-only
// (same author, inside the bead's active window, no bead reference).
func multiStrategyRepo(t *testing.T) (repo, jsonl string, shas map[string]string) {
	t.Helper()
	repo = t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "dev@example.com")
	runGit(t, repo, "config", "user.name", "Dev")
	jsonl = filepath.Join(beadsDir, "issues.jsonl")
	writeBead := func(status, assignee string) {
		t.Helper()
		line := `{"id":"bv-1","title":"One","status":"` + status + `","priority":1,"issue_type":"task","assignee":"` + assignee + `"}` + "\n"
		if err := os.WriteFile(jsonl, []byte(line), 0o644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}
	}
	writeCode := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package x\n// "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	head := func() string {
		t.Helper()
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repo
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	shas = map[string]string{}

	writeBead("open", "")
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "seed tracker")

	writeBead("in_progress", "dev@example.com") // claim
	writeCode("a.go")
	runGit(t, repo, "add", ".beads/issues.jsonl", "a.go")
	runGit(t, repo, "commit", "-m", "feat(bv-1): start work") // co-commit + explicit
	shas["both"] = head()

	writeCode("b.go")
	runGit(t, repo, "add", "b.go")
	runGit(t, repo, "commit", "-m", "wip: same author, no reference") // temporal only
	shas["temporal"] = head()

	writeCode("c.go")
	runGit(t, repo, "add", "c.go")
	runGit(t, repo, "commit", "-m", "fix bv-1 follow-up") // explicit only (temporal may also match)
	shas["explicit"] = head()

	writeBead("closed", "dev@example.com")
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "chore: close bv-1")
	return repo, jsonl, shas
}

func reportFor(t *testing.T, repo, jsonl string) *HistoryReport {
	t.Helper()
	report, err := NewCorrelator(repo, jsonl).GenerateReport([]BeadInfo{{ID: "bv-1", Title: "One", Status: "closed"}}, CorrelatorOptions{Limit: 100})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	return report
}

// TestCorrelator_MergeTwoStrategiesOneCommit (D5): a commit matched by two
// strategies appears once, lists both methods, keeps the stronger one as the
// primary, and its combined confidence sits between the strongest single
// score and 1.0.
func TestCorrelator_MergeTwoStrategiesOneCommit(t *testing.T) {
	repo, jsonl, shas := multiStrategyRepo(t)
	report := reportFor(t, repo, jsonl)
	history := report.Histories["bv-1"]
	t.Logf("repo=%s shas=%v methods=%v", repo, shas, report.Stats.MethodDistribution)

	seen := map[string]int{}
	var both *CorrelatedCommit
	for i := range history.Commits {
		c := &history.Commits[i]
		seen[c.SHA]++
		if c.SHA == shas["both"] {
			both = c
		}
	}
	for sha, n := range seen {
		if n != 1 {
			t.Fatalf("commit %s listed %d times; strategies must merge, not duplicate", sha[:7], n)
		}
	}
	if both == nil {
		t.Fatalf("the co-commit+explicit commit %s is missing: %+v", shas["both"][:7], history.Commits)
	}
	methods := strings.Join(both.Methods, ",")
	if !strings.Contains(methods, "co_committed") || !strings.Contains(methods, "explicit_id") {
		t.Fatalf("merged commit should list both strategies, got methods=%v", both.Methods)
	}
	if both.Confidence > 1.0 || both.Confidence < 0.85 {
		t.Fatalf("combined confidence %.2f must stay within [strongest single, 1.0]", both.Confidence)
	}
	if both.Method != MethodCoCommitted && both.Method != MethodExplicitID {
		t.Fatalf("primary method should be one of the matched strategies, got %s", both.Method)
	}
	if _, ok := seen[shas["temporal"]]; !ok {
		t.Fatalf("temporal-only commit %s (same author, in window) missing: %+v", shas["temporal"][:7], history.Commits)
	}
	if _, ok := seen[shas["explicit"]]; !ok {
		t.Fatalf("explicit-only commit %s missing", shas["explicit"][:7])
	}
	dist := report.Stats.MethodDistribution
	if dist["co_committed"] == 0 || dist["explicit_id"] == 0 || dist["temporal_author"] == 0 {
		t.Fatalf("every strategy should contribute on this fixture: %v", dist)
	}
	if len(report.Stats.Strategies) != 3 {
		t.Fatalf("stats.strategies should record the three runs, got %+v", report.Stats.Strategies)
	}
}

// TestCorrelator_ReportIsDeterministic (D5): three runs over the same fixture
// produce byte-identical JSON once the wall-clock fields (generated_at and
// per-strategy durations) are blanked.
func TestCorrelator_ReportIsDeterministic(t *testing.T) {
	repo, jsonl, _ := multiStrategyRepo(t)
	normalize := func(r *HistoryReport) []byte {
		t.Helper()
		r.GeneratedAt = time.Time{}
		for i := range r.Stats.Strategies {
			r.Stats.Strategies[i].DurationMS = 0
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}
	first := normalize(reportFor(t, repo, jsonl))
	for run := 2; run <= 3; run++ {
		if got := normalize(reportFor(t, repo, jsonl)); string(got) != string(first) {
			t.Fatalf("run %d differs from run 1:\n%s\n---\n%s", run, first, got)
		}
	}
}

// TestCorrelator_CancelledContextReturnsFast (D5): a cancelled context stops
// report generation promptly with the context error and leaves no goroutines
// behind.
func TestCorrelator_CancelledContextReturnsFast(t *testing.T) {
	repo, jsonl, _ := multiStrategyRepo(t)
	settle := func() int {
		n := runtime.NumGoroutine()
		for i := 0; i < 20; i++ {
			time.Sleep(10 * time.Millisecond)
			if m := runtime.NumGoroutine(); m <= n {
				n = m
			}
		}
		return n
	}
	before := settle()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := NewCorrelator(repo, jsonl).WithContext(ctx).GenerateReport([]BeadInfo{{ID: "bv-1", Title: "One", Status: "closed"}}, CorrelatorOptions{Limit: 100})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("cancelled context should fail report generation")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("error should carry the context cancellation, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("cancelled report took %s; want under 200ms", elapsed)
	}
	if after := settle(); after > before {
		t.Fatalf("goroutines leaked after cancellation: before=%d after=%d", before, after)
	}
}

func TestCorrelator_CreatedDirectlyInProgressAndClosedMilestonesAndTemporalMixedCommit(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")

	baseTime := time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)
	commitAt := func(offset time.Duration, msg string, paths ...string) string {
		t.Helper()
		runGit(t, repo, "add", "--", ".")
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit %q (%v): %v\n%s", msg, paths, err, out)
		}
		out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	jsonl := filepath.Join(beadsDir, "issues.jsonl")

	// Commit 1 (t+0h): bv-pzpv created directly as in_progress!
	if err := os.WriteFile(jsonl, []byte(`{"id":"bv-pzpv","title":"Implement service handler","status":"in_progress","assignee":"dev@example.com"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(0, "chore: create bv-pzpv directly in_progress", ".beads/issues.jsonl")

	// Commit 2 (t+1h): Mixed commit touching code AND .beads/issues.jsonl (e.g. adding unrelated open bead bv-empty), with NO bead ID in message!
	if err := os.WriteFile(filepath.Join(repo, "service.go"), []byte("package main\nfunc Handle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Implement service handler","status":"in_progress","assignee":"dev@example.com"}`+"\n"+
			`{"id":"bv-empty","title":"Untouched backlog item","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	mixedTemporalSHA := commitAt(1*time.Hour, "wip: implement handler logic", "service.go", ".beads/issues.jsonl")

	// Commit 3 (t+90m): Pure .beads/-only bookkeeping commit inside window (negative control: must NOT correlate as temporal_author).
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Implement service handler","status":"in_progress","assignee":"dev@example.com"}`+"\n"+
			`{"id":"bv-empty","title":"Untouched backlog item retitled","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	pureBeadsSHA := commitAt(90*time.Minute, "chore: update backlog title", ".beads/issues.jsonl")

	// Commit 4 (t+2h): Close bv-pzpv AND introduce single-commit bv-one-shot directly as closed alongside Dockerfile + App.svelte!
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "App.svelte"), []byte("<script>export let ok = true;</script>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-pzpv","title":"Implement service handler","status":"closed","assignee":"dev@example.com"}`+"\n"+
			`{"id":"bv-empty","title":"Untouched backlog item retitled","status":"open"}`+"\n"+
			`{"id":"bv-one-shot","title":"Containerize svelte app","status":"closed","assignee":"dev@example.com"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	oneShotSHA := commitAt(2*time.Hour, "chore: close work and ship container", "Dockerfile", "App.svelte", ".beads/issues.jsonl")

	// Commit 5 (t+3h): Explicit-ID commit touching ONLY non-code repo files (.gitignore and docs/RELEASING.txt).
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "RELEASING.txt"), []byte("Release notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	explicitDocsSHA := commitAt(3*time.Hour, "docs(bv-pzpv): document release hygiene.", ".gitignore", "docs/RELEASING.txt")

	beads := []BeadInfo{
		{ID: "bv-pzpv", Title: "Implement service handler", Status: "closed"},
		{ID: "bv-empty", Title: "Untouched backlog item retitled", Status: "open"},
		{ID: "bv-one-shot", Title: "Containerize svelte app", Status: "closed"},
	}

	report, err := NewCorrelator(repo, jsonl).GenerateReport(beads, CorrelatorOptions{Limit: 100})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	// Verify Fix 2 + Fix 3 + Fix 5 on bv-pzpv
	pzpv := report.Histories["bv-pzpv"]
	if pzpv.Milestones.Created == nil || pzpv.Milestones.Claimed == nil || pzpv.Milestones.Closed == nil {
		t.Fatalf("expected Created, Claimed, and Closed milestones on bv-pzpv, got %+v", pzpv.Milestones)
	}
	if pzpv.CycleTime == nil || pzpv.CycleTime.ClaimToClose == nil || *pzpv.CycleTime.ClaimToClose != 2*time.Hour {
		t.Fatalf("expected ClaimToClose == 2h on bv-pzpv, got %+v", pzpv.CycleTime)
	}

	commitsBySHA := make(map[string]CorrelatedCommit)
	for _, cc := range pzpv.Commits {
		commitsBySHA[cc.SHA] = cc
	}
	if cc, ok := commitsBySHA[mixedTemporalSHA]; !ok || cc.Method != MethodTemporalAuthor {
		t.Fatalf("expected mixed code+.beads/ commit %s to correlate via temporal_author, got %+v (exists=%v)", mixedTemporalSHA[:7], cc, ok)
	}
	if _, ok := commitsBySHA[pureBeadsSHA]; ok {
		t.Fatalf("pure .beads/-only commit %s must NOT correlate via temporal_author", pureBeadsSHA[:7])
	}
	if cc, ok := commitsBySHA[explicitDocsSHA]; !ok || cc.Method != MethodExplicitID || len(cc.Files) != 2 {
		t.Fatalf("expected explicit_id non-code commit %s with 2 files (.gitignore, docs/RELEASING.txt), got %+v (exists=%v)", explicitDocsSHA[:7], cc, ok)
	}

	// Also verify standalone TemporalCorrelator admits mixed code+.beads/ commit and rejects pure .beads/ commit.
	tc := NewTemporalCorrelator(repo)
	win := ExtractWindowFromMilestones("bv-pzpv", pzpv.Title, pzpv.Milestones)
	if win == nil {
		t.Fatal("ExtractWindowFromMilestones returned nil")
	}
	standaloneTemporal, err := tc.FindCommitsInWindow(*win)
	if err != nil {
		t.Fatalf("FindCommitsInWindow: %v", err)
	}
	foundStandaloneMixed := false
	for _, cc := range standaloneTemporal {
		if cc.SHA == pureBeadsSHA {
			t.Fatalf("standalone TemporalCorrelator correlated pure .beads/ commit %s", pureBeadsSHA[:7])
		}
		if cc.SHA == mixedTemporalSHA {
			foundStandaloneMixed = true
		}
	}
	if !foundStandaloneMixed {
		t.Fatalf("standalone TemporalCorrelator missed mixed code+.beads/ commit %s", mixedTemporalSHA[:7])
	}

	// Verify Fix 2 + Fix 5 on single-commit closed bead bv-one-shot
	oneShot := report.Histories["bv-one-shot"]
	if oneShot.Milestones.Created == nil || oneShot.Milestones.Closed == nil {
		t.Fatalf("expected Created and Closed milestones on bv-one-shot, got %+v", oneShot.Milestones)
	}
	if len(oneShot.Commits) != 1 || oneShot.Commits[0].SHA != oneShotSHA || oneShot.Commits[0].Method != MethodCoCommitted {
		t.Fatalf("expected single-commit closed bead bv-one-shot to correlate via co_committed, got %+v", oneShot.Commits)
	}
	if len(oneShot.Commits[0].Files) != 2 {
		t.Fatalf("expected Dockerfile + App.svelte (2 files) on bv-one-shot co-commit, got %+v", oneShot.Commits[0].Files)
	}

	// Verify Fix 4: Non-null "commits":[] JSON serialization on 0-commit beads (and after filtering)
	emptyHist := report.Histories["bv-empty"]
	if emptyHist.Commits == nil {
		t.Fatal("bv-empty Commits slice is nil, want non-nil []CorrelatedCommit{}")
	}
	rawJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report): %v", err)
	}
	if strings.Contains(string(rawJSON), `"commits":null`) {
		t.Fatalf("serialized HistoryReport contains \"commits\":null: %s", rawJSON)
	}
	if !strings.Contains(string(rawJSON), `"commits":[]`) {
		t.Fatalf("serialized HistoryReport missing \"commits\":[]: %s", rawJSON)
	}

	filtered := NewScorer().FilterHistoriesByConfidence(report.Histories, 0.999)
	filteredJSON, err := json.Marshal(filtered)
	if err != nil {
		t.Fatalf("json.Marshal(filtered): %v", err)
	}
	if strings.Contains(string(filteredJSON), `"commits":null`) {
		t.Fatalf("confidence-filtered histories serialized \"commits\":null: %s", filteredJSON)
	}
}
