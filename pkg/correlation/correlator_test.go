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

func TestCorrelator_DeletedAndTombstonedTargetBeads(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init")

	baseTime := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	commitAt := func(offset time.Duration, msg string) string {
		t.Helper()
		runGit(t, repo, "add", "-A")
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
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

	// Commit 1: create bv-live, bv-del, and bv-tomb
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-live","title":"Live Active Bead","status":"open"}`+"\n"+
			`{"id":"bv-del","title":"Deleted Historical Bead","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Tombstoned Historical Bead","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(0, "chore: seed beads")

	// Commit 2: explicit-ID code commit for bv-del (without touching .beads/)
	if err := os.WriteFile(filepath.Join(repo, "del_feature.go"), []byte("package main\nfunc DelFeature() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delExplicitSHA := commitAt(1*time.Hour, "[bv-del] implement feature")

	// Commit 3: close bv-del in .beads/issues.jsonl + explicit-ID code commit for bv-tomb
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-live","title":"Live Active Bead","status":"open"}`+"\n"+
			`{"id":"bv-del","title":"Deleted Historical Bead","status":"closed"}`+"\n"+
			`{"id":"bv-tomb","title":"Tombstoned Historical Bead","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(2*time.Hour, "chore: close bv-del")

	if err := os.WriteFile(filepath.Join(repo, "tomb_fix.go"), []byte("package main\nfunc TombFix() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tombExplicitSHA := commitAt(3*time.Hour, "Fix bv-tomb edge case")

	// Commit 4: delete bv-del from JSONL and set bv-tomb to status=tombstone
	if err := os.WriteFile(jsonl, []byte(
		`{"id":"bv-live","title":"Live Active Bead","status":"open"}`+"\n"+
			`{"id":"bv-tomb","title":"Tombstoned Historical Bead","status":"tombstone"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAt(4*time.Hour, "chore: prune bv-del and tombstone bv-tomb")

	liveBeads := []BeadInfo{{ID: "bv-live", Title: "Live Active Bead", Status: "open"}}
	c := NewCorrelator(repo, jsonl)

	// 1. Unfiltered report must NOT synthesize deleted or tombstoned beads.
	unfiltered, err := c.GenerateReportCached(liveBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("unfiltered GenerateReportCached: %v", err)
	}
	if _, ok := unfiltered.Histories["bv-del"]; ok {
		t.Fatal("unfiltered report unexpectedly synthesized deleted bead bv-del")
	}
	if _, ok := unfiltered.Histories["bv-tomb"]; ok {
		t.Fatal("unfiltered report unexpectedly synthesized tombstoned bead bv-tomb")
	}

	// 2. Targeted deleted bead (both uncached and cached twice to hit disk cache)
	delOpts := CorrelatorOptions{BeadID: "bv-del", CausalityBeadID: "bv-del"}
	for pass, fn := range []func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error){
		c.GenerateReport,
		c.GenerateReportCached,
		c.GenerateReportCached,
	} {
		rep, err := fn(liveBeads, delOpts)
		if err != nil {
			t.Fatalf("pass %d deleted GenerateReport: %v", pass, err)
		}
		h, ok := rep.Histories["bv-del"]
		if !ok {
			t.Fatalf("pass %d missing synthesized history for deleted bead bv-del", pass)
		}
		if h.Title != "Deleted Historical Bead" {
			t.Fatalf("pass %d Title = %q, want %q", pass, h.Title, "Deleted Historical Bead")
		}
		if h.Status != "deleted" {
			t.Fatalf("pass %d Status = %q, want deleted", pass, h.Status)
		}
		if len(h.Events) != 3 {
			t.Fatalf("pass %d Events len = %d, want 3 (created, closed, deleted): %+v", pass, len(h.Events), h.Events)
		}
		foundDelCommit := false
		for _, cc := range h.Commits {
			if cc.SHA == delExplicitSHA {
				foundDelCommit = true
			}
		}
		if !foundDelCommit {
			t.Fatalf("pass %d explicit commit %s missing for deleted bead: %+v", pass, delExplicitSHA, h.Commits)
		}
		if rep.CausalHistory == nil || rep.CausalHistory.BeadID != "bv-del" {
			t.Fatalf("pass %d expected non-nil CausalHistory for bv-del, got %+v", pass, rep.CausalHistory)
		}
		if chain := rep.BuildCausalityChainAt("bv-del", DefaultCausalityOptions(), baseTime.Add(5*time.Hour)); chain == nil {
			t.Fatalf("pass %d BuildCausalityChainAt returned nil for deleted bead bv-del", pass)
		}
	}

	// 3. Targeted tombstoned bead (both uncached and cached twice)
	tombOpts := CorrelatorOptions{BeadID: "bv-tomb", CausalityBeadID: "bv-tomb"}
	for pass, fn := range []func([]BeadInfo, CorrelatorOptions) (*HistoryReport, error){
		c.GenerateReport,
		c.GenerateReportCached,
		c.GenerateReportCached,
	} {
		rep, err := fn(liveBeads, tombOpts)
		if err != nil {
			t.Fatalf("pass %d tombstoned GenerateReport: %v", pass, err)
		}
		h, ok := rep.Histories["bv-tomb"]
		if !ok {
			t.Fatalf("pass %d missing synthesized history for tombstoned bead bv-tomb", pass)
		}
		if h.Title != "Tombstoned Historical Bead" {
			t.Fatalf("pass %d Title = %q, want %q", pass, h.Title, "Tombstoned Historical Bead")
		}
		if h.Status != "tombstone" {
			t.Fatalf("pass %d Status = %q, want tombstone", pass, h.Status)
		}
		foundTombCommit := false
		for _, cc := range h.Commits {
			if cc.SHA == tombExplicitSHA {
				foundTombCommit = true
			}
		}
		if !foundTombCommit {
			t.Fatalf("pass %d explicit commit %s missing for tombstoned bead: %+v", pass, tombExplicitSHA, h.Commits)
		}
		if rep.CausalHistory == nil || rep.CausalHistory.BeadID != "bv-tomb" {
			t.Fatalf("pass %d expected non-nil CausalHistory for bv-tomb, got %+v", pass, rep.CausalHistory)
		}
		if chain := rep.BuildCausalityChainAt("bv-tomb", DefaultCausalityOptions(), baseTime.Add(5*time.Hour)); chain == nil {
			t.Fatalf("pass %d BuildCausalityChainAt returned nil for tombstoned bead bv-tomb", pass)
		}
	}

	// 4. Nonexistent bead with zero git evidence must NOT synthesize a phantom history.
	missingRep, err := c.GenerateReportCached(liveBeads, CorrelatorOptions{BeadID: "bv-does-not-exist", CausalityBeadID: "bv-does-not-exist"})
	if err != nil {
		t.Fatalf("nonexistent GenerateReportCached: %v", err)
	}
	if _, ok := missingRep.Histories["bv-does-not-exist"]; ok {
		t.Fatal("nonexistent bead synthesized phantom BeadHistory")
	}
	if chain := missingRep.BuildCausalityChainAt("bv-does-not-exist", DefaultCausalityOptions(), baseTime.Add(5*time.Hour)); chain != nil {
		t.Fatalf("expected nil causal chain for nonexistent bead, got %+v", chain)
	}
}

func TestFindGitRoot_ValidateRepositoryWithBeadsDir_AndMonorepoCorrelator(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	nonGitDir := t.TempDir()
	if _, err := FindGitRoot(nonGitDir); err == nil {
		t.Fatalf("expected FindGitRoot(%s) to fail outside git repo", nonGitDir)
	}

	// Git worktree/submodule .git file support
	wtRoot := t.TempDir()
	wtSub := filepath.Join(wtRoot, "packages", "core")
	if err := os.MkdirAll(wtSub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtRoot, ".git"), []byte("gitdir: /tmp/fake-worktree-gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gotWT, err := FindGitRoot(wtSub)
	if err != nil || gotWT != filepath.Clean(wtRoot) {
		t.Fatalf("FindGitRoot(worktree subdir) = (%q, %v), want (%q, nil)", gotWT, err, filepath.Clean(wtRoot))
	}

	// Monorepo with root .git and two independent .beads stores (no root .beads/)
	mono := t.TempDir()
	runGit(t, mono, "init")

	apiDir := filepath.Join(mono, "services", "api")
	webDir := filepath.Join(mono, "services", "web")
	apiBeadsDir := filepath.Join(apiDir, ".beads")
	webBeadsDir := filepath.Join(webDir, ".beads")
	if err := os.MkdirAll(apiBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(webBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	apiJSONL := filepath.Join(apiBeadsDir, "issues.jsonl")
	webJSONL := filepath.Join(webBeadsDir, "issues.jsonl")
	apiCode := filepath.Join(apiDir, "handler.go")
	webCode := filepath.Join(webDir, "app.ts")

	if err := os.WriteFile(apiJSONL, []byte(`{"id":"api-1","title":"API auth endpoint","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiCode, []byte("package api\nfunc Auth() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webJSONL, []byte(`{"id":"web-1","title":"Web login view","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webCode, []byte("export const Login = () => null;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseTime := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	commitMono := func(offset time.Duration, msg string, paths ...string) {
		t.Helper()
		args := append([]string{"add", "--"}, paths...)
		runGit(t, mono, args...)
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = mono
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit %q: %v\n%s", msg, err, out)
		}
	}

	commitMono(0, "feat(api): seed api-1", "services/api/.beads/issues.jsonl", "services/api/handler.go")
	commitMono(time.Hour, "feat(web): seed web-1", "services/web/.beads/issues.jsonl", "services/web/app.ts")

	// FindGitRoot from subdirectory and file path
	if root, err := FindGitRoot(apiDir); err != nil || root != filepath.Clean(mono) {
		t.Fatalf("FindGitRoot(apiDir) = (%q, %v), want (%q, nil)", root, err, filepath.Clean(mono))
	}
	if root, err := FindGitRoot(apiJSONL); err != nil || root != filepath.Clean(mono) {
		t.Fatalf("FindGitRoot(apiJSONL) = (%q, %v), want (%q, nil)", root, err, filepath.Clean(mono))
	}

	// ValidateRepositoryWithBeadsDir & ValidateRepository
	if err := ValidateRepositoryWithBeadsDir(apiDir, apiBeadsDir); err != nil {
		t.Fatalf("ValidateRepositoryWithBeadsDir(apiDir, apiBeadsDir) failed: %v", err)
	}
	if err := ValidateRepositoryWithBeadsDir(mono, apiBeadsDir); err != nil {
		t.Fatalf("ValidateRepositoryWithBeadsDir(mono, apiBeadsDir) failed: %v", err)
	}
	if err := ValidateRepository(apiDir); err != nil {
		t.Fatalf("ValidateRepository(apiDir) failed: %v", err)
	}
	if err := ValidateRepository(mono); err == nil {
		t.Fatal("expected ValidateRepository(mono) to fail when root .beads/ does not exist")
	}

	// Correlator constructed from subdirectory (with and without explicit beadsFilePath) isolates store
	for _, apiCorr := range []*Correlator{
		NewCorrelator(apiDir),
		NewCorrelator(apiDir, apiJSONL),
		NewCorrelator(mono, apiJSONL),
	} {
		for _, tracked := range apiCorr.extractor.beadsFiles {
			if !strings.HasPrefix(tracked, "services/api/.beads/") {
				t.Fatalf("api extractor leaked cross-store path %q in %v", tracked, apiCorr.extractor.beadsFiles)
			}
		}
		rep, err := apiCorr.GenerateReportCached([]BeadInfo{
			{ID: "api-1", Title: "API auth endpoint", Status: "open"},
			{ID: "web-1", Title: "Web login view", Status: "open"},
		}, CorrelatorOptions{})
		if err != nil {
			t.Fatalf("api GenerateReportCached: %v", err)
		}
		if len(rep.Histories["api-1"].Events) != 1 || len(rep.Histories["api-1"].Commits) != 1 {
			t.Fatalf("expected 1 event and 1 co-commit on api-1, got %+v", rep.Histories["api-1"])
		}
		if len(rep.Histories["web-1"].Events) != 0 {
			t.Fatalf("api correlator leaked web-1 events from sibling store: %+v", rep.Histories["web-1"].Events)
		}
	}

	webCorr := NewCorrelator(webDir, webJSONL)
	webRep, err := webCorr.GenerateReportCached([]BeadInfo{
		{ID: "api-1", Title: "API auth endpoint", Status: "open"},
		{ID: "web-1", Title: "Web login view", Status: "open"},
	}, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("web GenerateReportCached: %v", err)
	}
	if len(webRep.Histories["web-1"].Events) != 1 || len(webRep.Histories["web-1"].Commits) != 1 {
		t.Fatalf("expected 1 event and 1 co-commit on web-1, got %+v", webRep.Histories["web-1"])
	}
	if len(webRep.Histories["api-1"].Events) != 0 {
		t.Fatalf("web correlator leaked api-1 events from sibling store: %+v", webRep.Histories["api-1"].Events)
	}
}

func TestCorrelator_CustomNonDotBeadsDirExcludesTrackerFilesAndPromotesBD(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")

	customDir := filepath.Join(repo, "custom-beads")
	if err := os.MkdirAll(filepath.Join(customDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	subBDDir := filepath.Join(repo, "services", "api", ".beads")
	if err := os.MkdirAll(filepath.Join(subBDDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "pkg", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write bd workspace metadata + both legacy beads.jsonl and issues.jsonl to verify promotion.
	if err := os.WriteFile(filepath.Join(customDir, "metadata.json"), []byte(`{"backend":"dolt","database":"dolt"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "config.yaml"), []byte("issue_prefix: bd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "beads.jsonl"), []byte(`{"id":"stale-legacy","title":"Legacy stale","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	customIssues := filepath.Join(customDir, "issues.jsonl")
	if err := os.WriteFile(customIssues, []byte(`{"id":"bd-1","title":"Custom store auth bead","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subBDDir, "metadata.json"), []byte(`{"backend":"dolt","database":"dolt"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify orderBeadsFilesForWorkspace promotes <storeDir>/issues.jsonl for both custom-beads and subdirectory .beads.
	gotCustomOrder := orderBeadsFilesForWorkspace(repo, []string{
		"custom-beads/beads.jsonl",
		"custom-beads/issues.jsonl",
		"custom-beads/beads.base.jsonl",
	})
	if len(gotCustomOrder) == 0 || gotCustomOrder[0] != "custom-beads/issues.jsonl" {
		t.Fatalf("orderBeadsFilesForWorkspace(custom-beads) = %v, want custom-beads/issues.jsonl first", gotCustomOrder)
	}
	gotSubOrder := orderBeadsFilesForWorkspace(repo, []string{
		"services/api/.beads/beads.jsonl",
		"services/api/.beads/issues.jsonl",
	})
	if len(gotSubOrder) == 0 || gotSubOrder[0] != "services/api/.beads/issues.jsonl" {
		t.Fatalf("orderBeadsFilesForWorkspace(services/api/.beads) = %v, want services/api/.beads/issues.jsonl first", gotSubOrder)
	}

	authFile := filepath.Join(repo, "pkg", "auth", "token.go")
	if err := os.WriteFile(authFile, []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseTime := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	commitFiles := func(offset time.Duration, msg string, paths ...string) string {
		t.Helper()
		args := append([]string{"add", "--"}, paths...)
		runGit(t, repo, args...)
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
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

	// Commit 1: initial seed commit creating bd-1 in custom-beads.
	commitFiles(0, "chore: seed custom-beads",
		"custom-beads/issues.jsonl",
		"custom-beads/beads.jsonl",
		"custom-beads/metadata.json",
		"custom-beads/config.yaml",
	)

	// Commit 2: pure custom-beads tracker commit claiming bd-1 AND mentioning [bd-1]
	// (must NEVER correlate as explicit_id or co_committed code commit!).
	if err := os.WriteFile(customIssues, []byte(`{"id":"bd-1","title":"Custom store auth bead","status":"in_progress","assignee":"dev@example.com"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "config.yaml"), []byte("issue_prefix: bd\nsync_branch: main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trackerOnlySHA := commitFiles(10*time.Minute, "chore [bd-1]: claim bead in custom tracker",
		"custom-beads/issues.jsonl",
		"custom-beads/config.yaml",
	)

	// Commit 3: mixed co_committed status-close commit touching custom-beads/* AND pkg/auth/token.go
	// (no bead ID in commit message -> correlates purely via co_committed, excluding custom-beads/*).
	if err := os.WriteFile(customIssues, []byte(`{"id":"bd-1","title":"Custom store auth bead","status":"closed","assignee":"dev@example.com"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "metadata.json"), []byte(`{"backend":"dolt","database":"dolt","version":2}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mixedCoCommitSHA := commitFiles(20*time.Minute, "chore: ship auth package and close tracker item",
		"custom-beads/issues.jsonl",
		"custom-beads/metadata.json",
		"pkg/auth/token.go",
	)

	// Commit 4: mixed explicit_id commit touching custom-beads/config.yaml AND pkg/auth/token.go.
	if err := os.WriteFile(filepath.Join(customDir, "config.yaml"), []byte("issue_prefix: bd\nsync_branch: main\nupdated: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authFile, []byte("package auth\nfunc Verify() bool { return true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codeExplicitSHA := commitFiles(30*time.Minute, "feat(bd-1): verify token signature",
		"custom-beads/config.yaml",
		"pkg/auth/token.go",
	)

	corr := NewCorrelator(repo, customIssues)
	if got := corr.storeDirRel(); got != "custom-beads" {
		t.Fatalf("corr.storeDirRel() = %q, want %q", got, "custom-beads")
	}

	rep, err := corr.GenerateReportCached([]BeadInfo{
		{ID: "bd-1", Title: "Custom store auth bead", Status: "closed"},
	}, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("GenerateReportCached: %v", err)
	}

	h := rep.Histories["bd-1"]
	if len(h.Events) != 3 {
		t.Fatalf("bd-1 events = %d, want 3: %+v", len(h.Events), h.Events)
	}
	seenSHAs := make(map[string]CorrelatedCommit, len(h.Commits))
	for _, cc := range h.Commits {
		seenSHAs[cc.SHA] = cc
		for _, f := range cc.Files {
			if strings.HasPrefix(f.Path, "custom-beads/") || f.Path == "custom-beads" {
				t.Fatalf("tracker file %q leaked into correlated commit %s (%s): %+v", f.Path, cc.ShortSHA, cc.Method, cc.Files)
			}
		}
	}
	if _, leaked := seenSHAs[trackerOnlySHA]; leaked {
		t.Fatalf("pure custom-beads tracker commit %s leaked as correlated code commit: %+v", trackerOnlySHA, seenSHAs[trackerOnlySHA])
	}
	if mixed, ok := seenSHAs[mixedCoCommitSHA]; !ok || len(mixed.Files) != 1 || mixed.Files[0].Path != "pkg/auth/token.go" {
		t.Fatalf("mixed co-commit %s (ok=%v) files = %+v, want [pkg/auth/token.go]", mixedCoCommitSHA, ok, mixed.Files)
	}
	if explicitMixed, ok := seenSHAs[codeExplicitSHA]; !ok || len(explicitMixed.Files) != 1 || explicitMixed.Files[0].Path != "pkg/auth/token.go" {
		t.Fatalf("mixed explicit commit %s (ok=%v) files = %+v, want [pkg/auth/token.go]", codeExplicitSHA, ok, explicitMixed.Files)
	}

	// ReverseLookup.FindOrphanCommits with WithStoreDir("custom-beads") ignores both pure custom-beads commits (Commit 1 & Commit 2).
	rl := NewReverseLookupWithRepo(rep, repo).WithStoreDir(corr.storeDirRel())
	orphans, orphanStats, err := rl.FindOrphanCommits(ExtractOptions{Limit: 20})
	if err != nil {
		t.Fatalf("FindOrphanCommits: %v", err)
	}
	if orphanStats.BeadsOnlyCommits != 2 {
		t.Fatalf("OrphanStats.BeadsOnlyCommits = %d, want 2", orphanStats.BeadsOnlyCommits)
	}
	for _, orphan := range orphans {
		if orphan.SHA == trackerOnlySHA {
			t.Fatalf("pure custom-beads commit %s leaked into orphan commits: %+v", trackerOnlySHA, orphan)
		}
	}
}

func TestCorrelator_WorkspaceAliasesAndSharedGitSubtreeDisambiguation(t *testing.T) {
	t.Setenv("BV_CORRELATION_CACHE_DIR", t.TempDir())

	mono := t.TempDir()
	runGit(t, mono, "init")

	rootBeadsDir := filepath.Join(mono, ".beads")
	apiDir := filepath.Join(mono, "services", "api")
	apiBeadsDir := filepath.Join(apiDir, ".beads")
	webDir := filepath.Join(mono, "services", "web")
	webBeadsDir := filepath.Join(webDir, ".beads")
	for _, d := range []string{rootBeadsDir, apiBeadsDir, webBeadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rootJSONL := filepath.Join(rootBeadsDir, "issues.jsonl")
	apiJSONL := filepath.Join(apiBeadsDir, "issues.jsonl")
	webJSONL := filepath.Join(webBeadsDir, "issues.jsonl")

	baseTime := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	commitMono := func(offset time.Duration, msg string, paths ...string) string {
		t.Helper()
		args := append([]string{"add", "--"}, paths...)
		runGit(t, mono, args...)
		ts := baseTime.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = mono
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com",
			"GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit %q: %v\n%s", msg, err, out)
		}
		out, err := exec.Command("git", "-C", mono, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}

	// Commit 1: seed root, api (with numeric "1", colliding "bd-1", and soon-to-be-deleted "99" depending on "1"), and web ("bd-1").
	if err := os.WriteFile(rootJSONL, []byte(`{"id":"bd-1","title":"Root umbrella task","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(apiJSONL, []byte(
		`{"id":"1","title":"API numeric bead","status":"closed"}`+"\n"+
			`{"id":"bd-1","title":"API colliding bead","status":"open"}`+"\n"+
			`{"id":"99","title":"Deleted API bead","status":"open","dependencies":[{"issue_id":"99","depends_on_id":"1","type":"blocks"}]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webJSONL, []byte(`{"id":"bd-1","title":"Web colliding bead","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mono, "README.md"), []byte("# Monorepo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "handler.go"), []byte("package api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "app.ts"), []byte("export const v = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitMono(0, "chore: seed monorepo stores",
		".beads/issues.jsonl",
		"services/api/.beads/issues.jsonl",
		"services/web/.beads/issues.jsonl",
		"README.md",
		"services/api/handler.go",
		"services/web/app.ts",
	)

	// Commit 2: workspace-qualified ExplicitIDAlias "api-1" touching root README.md (outside services/api/).
	// Must match BeadID "1" unambiguously despite StoreSubtreePrefix="services/api/".
	if err := os.WriteFile(filepath.Join(mono, "README.md"), []byte("# Monorepo\nDocument api-1.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasCrossSubtreeSHA := commitMono(10*time.Minute, "docs(api-1): document API contract at root", "README.md")

	// Commit 3: unprefixed colliding "bd-1" touching ONLY services/web/app.ts.
	if err := os.WriteFile(filepath.Join(webDir, "app.ts"), []byte("export const v = 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	webUnprefixedSHA := commitMono(20*time.Minute, "fix(bd-1): fix web login", "services/web/app.ts")

	// Commit 4: unprefixed colliding "bd-1" touching ONLY services/api/handler.go.
	if err := os.WriteFile(filepath.Join(apiDir, "handler.go"), []byte("package api\nvar ok = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apiUnprefixedSHA := commitMono(30*time.Minute, "fix(bd-1): fix api handler", "services/api/handler.go")

	// Commit 5: unprefixed colliding "bd-1" touching ONLY root README.md.
	if err := os.WriteFile(filepath.Join(mono, "README.md"), []byte("# Monorepo\nDocument api-1.\nRoot fix.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootUnprefixedSHA := commitMono(40*time.Minute, "fix(bd-1): update root umbrella docs", "README.md")

	// Commit 6: code commit referencing workspace-qualified "api-99", followed by Commit 7 deleting "99" from services/api/.beads/issues.jsonl.
	if err := os.WriteFile(filepath.Join(apiDir, "handler.go"), []byte("package api\nvar ok = true\nvar v99 = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	api99SHA := commitMono(50*time.Minute, "feat(api-99): implement endpoint 99", "services/api/handler.go")

	if err := os.WriteFile(apiJSONL, []byte(
		`{"id":"1","title":"API numeric bead","status":"closed"}`+"\n"+
			`{"id":"bd-1","title":"API colliding bead","status":"open"}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	commitMono(60*time.Minute, "chore: prune deleted bead 99", "services/api/.beads/issues.jsonl")

	apiCorr := NewCorrelator(apiDir, apiJSONL)
	apiLiveBeads := []BeadInfo{
		{ID: "1", Title: "API numeric bead", Status: "closed"},
		{ID: "bd-1", Title: "API colliding bead", Status: "open"},
	}
	apiRep, err := apiCorr.GenerateReportCached(apiLiveBeads, CorrelatorOptions{
		ExplicitIDAliases: map[string]string{
			"api-1":    "1",
			"api-bd-1": "bd-1",
		},
		AmbiguousUnprefixedIDs: map[string]bool{"bd-1": true},
		StoreSubtreePrefix:     "services/api/",
	})
	if err != nil {
		t.Fatalf("api GenerateReportCached: %v", err)
	}

	// Verify ExplicitIDAliases matched aliasCrossSubtreeSHA onto canonical "1" even outside services/api/.
	foundAliasOn1 := false
	for _, cc := range apiRep.Histories["1"].Commits {
		if cc.SHA == aliasCrossSubtreeSHA && cc.Method == MethodExplicitID {
			foundAliasOn1 = true
		}
	}
	if !foundAliasOn1 {
		t.Fatalf("expected ExplicitIDAlias commit %s on bead '1', got %+v", aliasCrossSubtreeSHA, apiRep.Histories["1"].Commits)
	}

	// Verify API store accepted apiUnprefixedSHA and rejected webUnprefixedSHA & rootUnprefixedSHA on colliding "bd-1".
	apiBD1Explicit := make(map[string]bool)
	for _, cc := range apiRep.Histories["bd-1"].Commits {
		if cc.Method == MethodExplicitID {
			apiBD1Explicit[cc.SHA] = true
		}
	}
	if !apiBD1Explicit[apiUnprefixedSHA] || apiBD1Explicit[webUnprefixedSHA] || apiBD1Explicit[rootUnprefixedSHA] {
		t.Fatalf("api colliding bd-1 explicit SHAs = %v, want ONLY %s", apiBD1Explicit, apiUnprefixedSHA)
	}

	// Verify Web store accepted webUnprefixedSHA and rejected apiUnprefixedSHA & rootUnprefixedSHA.
	webCorr := NewCorrelator(webDir, webJSONL)
	webRep, err := webCorr.GenerateReportCached([]BeadInfo{
		{ID: "bd-1", Title: "Web colliding bead", Status: "open"},
	}, CorrelatorOptions{
		AmbiguousUnprefixedIDs: map[string]bool{"bd-1": true},
		StoreSubtreePrefix:     "services/web/",
	})
	if err != nil {
		t.Fatalf("web GenerateReportCached: %v", err)
	}
	webBD1Explicit := make(map[string]bool)
	for _, cc := range webRep.Histories["bd-1"].Commits {
		if cc.Method == MethodExplicitID {
			webBD1Explicit[cc.SHA] = true
		}
	}
	if !webBD1Explicit[webUnprefixedSHA] || webBD1Explicit[apiUnprefixedSHA] || webBD1Explicit[rootUnprefixedSHA] {
		t.Fatalf("web colliding bd-1 explicit SHAs = %v, want ONLY %s", webBD1Explicit, webUnprefixedSHA)
	}

	// Verify Root umbrella store with SiblingSubtreePrefixes accepted rootUnprefixedSHA and rejected api & web subtree commits!
	rootCorr := NewCorrelator(mono, rootJSONL)
	rootRep, err := rootCorr.GenerateReportCached([]BeadInfo{
		{ID: "bd-1", Title: "Root umbrella task", Status: "open"},
	}, CorrelatorOptions{
		AmbiguousUnprefixedIDs: map[string]bool{"bd-1": true},
		SiblingSubtreePrefixes: []string{"services/api/", "services/web/"},
	})
	if err != nil {
		t.Fatalf("root GenerateReportCached: %v", err)
	}
	rootBD1Explicit := make(map[string]bool)
	for _, cc := range rootRep.Histories["bd-1"].Commits {
		if cc.Method == MethodExplicitID {
			rootBD1Explicit[cc.SHA] = true
		}
	}
	if !rootBD1Explicit[rootUnprefixedSHA] || rootBD1Explicit[apiUnprefixedSHA] || rootBD1Explicit[webUnprefixedSHA] {
		t.Fatalf("root umbrella colliding bd-1 explicit SHAs = %v, want ONLY %s", rootBD1Explicit, rootUnprefixedSHA)
	}

	// Verify TargetBeadAliases ("99" + "api-99") in GenerateReportCached + CausalityBeadID
	// resolves historical snapshot ID "99", synthesizes its deleted history, and attaches feat(api-99).
	targetRep, err := apiCorr.GenerateReportCached(apiLiveBeads, CorrelatorOptions{
		BeadID:            "api-99",
		CausalityBeadID:   "api-99",
		TargetBeadAliases: []string{"99", "api-99"},
		ExplicitIDAliases: map[string]string{"api-99": "99"},
	})
	if err != nil {
		t.Fatalf("target alias GenerateReportCached: %v", err)
	}
	h99, ok := targetRep.Histories["99"]
	if !ok {
		t.Fatalf("expected synthesized history under historical local ID '99', got keys %+v", targetRep.Histories)
	}
	if h99.Status != "deleted" || h99.Title != "Deleted API bead" {
		t.Fatalf("h99 Status=%q Title=%q, want deleted / Deleted API bead", h99.Status, h99.Title)
	}
	found99Commit := false
	for _, cc := range h99.Commits {
		if cc.SHA == api99SHA {
			found99Commit = true
		}
	}
	if !found99Commit {
		t.Fatalf("expected feat(api-99) commit %s on synthesized '99', got %+v", api99SHA, h99.Commits)
	}
	if targetRep.CausalHistory == nil || targetRep.CausalHistory.BeadID != "99" {
		t.Fatalf("CausalHistory = %+v, want BeadID='99'", targetRep.CausalHistory)
	}
	if len(targetRep.CausalHistory.Observations) == 0 {
		t.Fatalf("expected historical causal observations for '99', got empty Observations: %+v", targetRep.CausalHistory)
	}
}

func TestCorrelator_ReopenedAndOpenClaimedTemporalWindowsBoundedByLatestCommit(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	base := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	commitAt := func(offset time.Duration, author, email, msg string, files map[string]string) string {
		t.Helper()
		for rel, content := range files {
			full := filepath.Join(repo, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		runAdd := exec.Command("git", "-C", repo, "add", "-A")
		if out, err := runAdd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v (%s)", err, out)
		}
		ts := base.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "-C", repo, "-c", "user.name="+author, "-c", "user.email="+email, "commit", "-m", msg)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+author,
			"GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+author,
			"GIT_COMMITTER_EMAIL="+email,
			"GIT_AUTHOR_DATE="+ts,
			"GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %q: %v (%s)", msg, err, out)
		}
		shaOut, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			t.Fatalf("git rev-parse: %v", err)
		}
		return strings.TrimSpace(string(shaOut))
	}

	if out, err := exec.Command("git", "-C", repo, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}

	// T+0h: Create & claim bv-reopen (in_progress), bv-open (in_progress), and bv-closed (in_progress) with uppercase email.
	_ = commitAt(0, "Alice Dev", "ALICE@Example.com", "claim tasks", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-reopen","title":"Fix auth handler","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-open","title":"Update login service","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-closed","title":"Harden auth session","status":"in_progress","assignee":"alice"}` + "\n",
	})
	// T+1h: Close bv-reopen prematurely.
	_ = commitAt(1*time.Hour, "Alice Dev", "alice@example.com", "close bv-reopen", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-reopen","title":"Fix auth handler","status":"closed","assignee":"alice"}` + "\n" +
			`{"id":"bv-open","title":"Update login service","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-closed","title":"Harden auth session","status":"in_progress","assignee":"alice"}` + "\n",
	})
	// T+2h: Reopen bv-reopen back to in_progress!
	_ = commitAt(2*time.Hour, "Alice Dev", "alice@example.com", "reopen bv-reopen", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-reopen","title":"Fix auth handler","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-open","title":"Update login service","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-closed","title":"Harden auth session","status":"in_progress","assignee":"alice"}` + "\n",
	})
	// T+3h: Alice pushes a code commit without explicit bead ID in commit message (lowercase email vs uppercase claim).
	aliceCodeSHA := commitAt(3*time.Hour, "Alice Dev", "alice@example.com", "refactor auth handler logic", map[string]string{
		"pkg/auth/handler.go": "package auth\n",
	})
	// T+4h: Planted negative control — Bob pushes a code commit during the same window.
	bobCodeSHA := commitAt(4*time.Hour, "Bob Dev", "bob@example.com", "unrelated billing tweak", map[string]string{
		"pkg/billing/charge.go": "package billing\n",
	})
	// T+5h: Close bv-closed (leaving bv-open and reopened bv-reopen still in_progress).
	_ = commitAt(5*time.Hour, "Alice Dev", "alice@example.com", "close bv-closed", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-reopen","title":"Fix auth handler","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-open","title":"Update login service","status":"in_progress","assignee":"alice"}` + "\n" +
			`{"id":"bv-closed","title":"Harden auth session","status":"closed","assignee":"alice"}` + "\n",
	})

	liveBeads := []BeadInfo{
		{ID: "bv-reopen", Title: "Fix auth handler", Status: "in_progress"},
		{ID: "bv-open", Title: "Update login service", Status: "in_progress"},
		{ID: "bv-closed", Title: "Harden auth session", Status: "closed"},
	}

	// Verify walk-based Correlator correlates aliceCodeSHA to completed bv-closed (with ActiveBeads=3
	// counting concurrent open/reopened claims!), does NOT falsely treat reopened bv-reopen's stale T+1h
	// close as closed, and rejects bobCodeSHA.
	corr := NewCorrelator(repo, jsonlPath)
	rep, err := corr.GenerateReportCached(liveBeads, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("GenerateReportCached: %v", err)
	}

	closedHist := rep.Histories["bv-closed"]
	var foundAliceOnClosed, foundBob bool
	for _, c := range closedHist.Commits {
		if c.SHA == aliceCodeSHA && c.Method == MethodTemporalAuthor {
			foundAliceOnClosed = true
		}
		if c.SHA == bobCodeSHA {
			foundBob = true
		}
	}
	if !foundAliceOnClosed {
		t.Fatalf("bv-closed missed temporal commit %s (commits=%+v)", aliceCodeSHA, closedHist.Commits)
	}
	if foundBob {
		t.Fatalf("negative control failed: bv-closed correlated Bob's commit %s", bobCodeSHA)
	}
	if len(rep.Histories["bv-reopen"].Commits) != 0 || len(rep.Histories["bv-open"].Commits) != 0 {
		t.Fatalf("still-open/reopened beads must remain unlinked in automatic history index so OrphanDetector preserves orphan visibility: reopen=%+v open=%+v", rep.Histories["bv-reopen"].Commits, rep.Histories["bv-open"].Commits)
	}

	// Also verify TemporalCorrelator.ExtractAllTemporalCorrelations queries latest repo commit timestamp
	// (t+5h > latest open milestone) and correlates aliceCodeSHA across bv-closed, bv-open, AND reopened bv-reopen
	// in deterministic beadID order while rejecting bobCodeSHA.
	tc := NewTemporalCorrelator(repo)
	tcCommits, err := tc.ExtractAllTemporalCorrelations(rep.Histories)
	if err != nil {
		t.Fatalf("ExtractAllTemporalCorrelations: %v", err)
	}
	seenByBead := make(map[string]bool)
	for i, c := range tcCommits {
		if i > 0 && tcCommits[i-1].BeadID > c.BeadID {
			t.Fatalf("ExtractAllTemporalCorrelations output not sorted by BeadID: %s before %s", tcCommits[i-1].BeadID, c.BeadID)
		}
		if c.SHA == bobCodeSHA {
			t.Fatalf("negative control failed: ExtractAllTemporalCorrelations matched Bob's commit %s", bobCodeSHA)
		}
		if c.SHA == aliceCodeSHA {
			seenByBead[c.BeadID] = true
		}
	}
	if !seenByBead["bv-closed"] || !seenByBead["bv-open"] || !seenByBead["bv-reopen"] {
		t.Fatalf("ExtractAllTemporalCorrelations missed aliceCodeSHA on closed/open/reopened beads: seen=%v", seenByBead)
	}
}

func TestCorrelator_TrimmedEmailWhitespaceFallbackAndSameSecondReopenEndToEnd(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")

	base := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	commitAt := func(offset time.Duration, author, email, msg string, files map[string]string) string {
		t.Helper()
		for rel, content := range files {
			full := filepath.Join(repo, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("git", "-C", repo, "add", "-A").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v (%s)", err, out)
		}
		ts := base.Add(offset).Format(time.RFC3339)
		cmd := exec.Command("git", "-C", repo, "-c", "user.name="+author, "-c", "user.email="+email, "commit", "-m", msg)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+author,
			"GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+author,
			"GIT_COMMITTER_EMAIL="+email,
			"GIT_AUTHOR_DATE="+ts,
			"GIT_COMMITTER_DATE="+ts,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit %q: %v (%s)", msg, err, out)
		}
		shaOut, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").CombinedOutput()
		if err != nil {
			t.Fatalf("git rev-parse: %v", err)
		}
		return strings.TrimSpace(string(shaOut))
	}

	if out, err := exec.Command("git", "-C", repo, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}

	// T+0: Claim bv-same-open and bv-same-closed.
	_ = commitAt(0, "Alice Dev", "alice@example.com", "claim both", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-same-open","title":"Auth fix","status":"in_progress"}` + "\n" +
			`{"id":"bv-same-closed","title":"Session fix","status":"in_progress"}` + "\n",
	})
	// T+30m: Alice code commit inside [T+0, T+1h].
	earlySHA := commitAt(30*time.Minute, "Alice Dev", "alice@example.com", "early work", map[string]string{
		"pkg/auth/early.go": "package auth\n",
	})
	// T+1h (exact same second!):
	// Commit A at T+1h closes bv-same-open and opens/reopens bv-same-closed? First close both at T+1h:
	_ = commitAt(1*time.Hour, "Alice Dev", "alice@example.com", "close both at T+1h", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-same-open","title":"Auth fix","status":"closed"}` + "\n" +
			`{"id":"bv-same-closed","title":"Session fix","status":"closed"}` + "\n",
	})
	// Commit B at exact same timestamp T+1h: reopen BOTH at T+1h!
	_ = commitAt(1*time.Hour, "Alice Dev", "alice@example.com", "reopen both at T+1h", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-same-open","title":"Auth fix","status":"open"}` + "\n" +
			`{"id":"bv-same-closed","title":"Session fix","status":"in_progress"}` + "\n",
	})
	// Commit C at exact same timestamp T+1h: re-close ONLY bv-same-closed at T+1h (leaving bv-same-open reopened!).
	_ = commitAt(1*time.Hour, "Alice Dev", "alice@example.com", "reclose bv-same-closed at T+1h", map[string]string{
		".beads/issues.jsonl": `{"id":"bv-same-open","title":"Auth fix","status":"open"}` + "\n" +
			`{"id":"bv-same-closed","title":"Session fix","status":"closed"}` + "\n",
	})
	// T+2h: Alice pushes a post-reopen code commit.
	postReopenSHA := commitAt(2*time.Hour, "Alice Dev", "alice@example.com", "post-reopen work", map[string]string{
		"pkg/auth/late.go": "package auth\n",
	})

	corr := NewCorrelator(repo, jsonlPath)
	rep, err := corr.GenerateReportCached([]BeadInfo{
		{ID: "bv-same-open", Title: "Auth fix", Status: "open"},
		{ID: "bv-same-closed", Title: "Session fix", Status: "closed"},
	}, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("GenerateReportCached: %v", err)
	}

	// [A-4] Positive observable: bv-same-open (EventClosed@T+1h -> EventReopened@T+1h, Status=open) must NOT auto-link as closed!
	if len(rep.Histories["bv-same-open"].Commits) != 0 {
		t.Fatalf("[A-4] same-second reopened bead falsely auto-linked as closed: %+v", rep.Histories["bv-same-open"].Commits)
	}
	// [A-4] Planted negative control: bv-same-closed (EventReopened@T+1h -> EventClosed@T+1h, Status=closed) IS closed at T+1h,
	// so it links earlySHA (T+30m) and does NOT link postReopenSHA (T+2h).
	var closedGotEarly, closedGotLate bool
	for _, c := range rep.Histories["bv-same-closed"].Commits {
		if c.SHA == earlySHA {
			closedGotEarly = true
		}
		if c.SHA == postReopenSHA {
			closedGotLate = true
		}
	}
	if !closedGotEarly || closedGotLate {
		t.Fatalf("[A-4] bv-same-closed commits = %+v, want earlySHA=true postReopenSHA=false", rep.Histories["bv-same-closed"].Commits)
	}

	// Verify ExtractAllTemporalCorrelations bounds bv-same-open to T+2h (correlating postReopenSHA)
	// while keeping bv-same-closed bounded at T+1h (excluding postReopenSHA).
	tc := NewTemporalCorrelator(repo)
	tcOut, err := tc.ExtractAllTemporalCorrelations(rep.Histories)
	if err != nil {
		t.Fatalf("ExtractAllTemporalCorrelations: %v", err)
	}
	var openGotPostReopen, closedGotPostReopen bool
	for _, c := range tcOut {
		if c.SHA == postReopenSHA && c.BeadID == "bv-same-open" {
			openGotPostReopen = true
		}
		if c.SHA == postReopenSHA && c.BeadID == "bv-same-closed" {
			closedGotPostReopen = true
		}
	}
	if !openGotPostReopen || closedGotPostReopen {
		t.Fatalf("[A-4] ExtractAllTemporalCorrelations postReopenSHA: open=%v closed=%v (want true / false)", openGotPostReopen, closedGotPostReopen)
	}

	// [A-3] Direct test of extractTemporalCandidatesWithOptions with padded "  alice@example.com  "
	// and whitespace-only "   " email falling back to author name (plus distinct-author negative control).
	a3Events := []BeadEvent{
		{BeadID: "bv-padded", EventType: EventClaimed, Timestamp: base, Author: "  Alice Dev  ", AuthorEmail: "  ALICE@Example.com  "},
		{BeadID: "bv-padded", EventType: EventClosed, Timestamp: base.Add(2 * time.Hour), Author: "Alice Dev", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-ws-email", EventType: EventClaimed, Timestamp: base, Author: "  Solo Author  ", AuthorEmail: "   "},
		{BeadID: "bv-ws-email", EventType: EventClosed, Timestamp: base.Add(2 * time.Hour), Author: "Solo Author", AuthorEmail: "   "},
	}
	a3Walk := []walkedCommit{
		{
			SHA:         earlySHA,
			Author:      "  Alice Dev  ",
			AuthorEmail: "  alice@example.com  ",
			Timestamp:   base.Add(30 * time.Minute),
			Files:       []string{"pkg/auth/early.go"},
		},
		{
			SHA:         postReopenSHA,
			Author:      "  solo author  ",
			AuthorEmail: "   ",
			Timestamp:   base.Add(45 * time.Minute),
			Files:       []string{"pkg/auth/late.go"},
		},
		{
			// Planted negative control: whitespace-only email with a different author name must NOT match Solo Author or Alice Dev.
			SHA:         earlySHA,
			Author:      "Unrelated Person",
			AuthorEmail: "   ",
			Timestamp:   base.Add(45 * time.Minute),
			Files:       []string{"pkg/auth/early.go"},
		},
	}
	cands, err := corr.extractTemporalCandidatesWithOptions(a3Events, a3Walk, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("extractTemporalCandidatesWithOptions: %v", err)
	}
	matchedPairs := make(map[string]bool)
	for _, cand := range cands {
		if cand.Author == "Unrelated Person" {
			t.Fatalf("[A-3] negative control failed: whitespace-only unrelated author matched %+v", cand)
		}
		matchedPairs[cand.BeadID+":"+cand.SHA] = true
	}
	if !matchedPairs["bv-padded:"+earlySHA] {
		t.Fatalf("[A-3] padded email failed to match in extractTemporalCandidatesWithOptions: %+v", cands)
	}
	if !matchedPairs["bv-ws-email:"+postReopenSHA] {
		t.Fatalf("[A-3] whitespace-only email failed to fall back to author name: %+v", cands)
	}
}
