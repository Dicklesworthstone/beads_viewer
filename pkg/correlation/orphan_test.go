package correlation

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewOrphanDetector(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-test1": {
				Title:      "Test Bead 1",
				Status:     "closed",
				LastAuthor: "Test Author", // Required for author->beads mapping
				Milestones: BeadMilestones{
					Claimed: &BeadEvent{
						Timestamp: now.Add(-72 * time.Hour),
					},
					Closed: &BeadEvent{
						Timestamp: now.Add(-24 * time.Hour),
					},
				},
				Commits: []CorrelatedCommit{
					{
						SHA:         "abc123def456",
						ShortSHA:    "abc123d",
						Author:      "Test Author",
						AuthorEmail: "test@example.com",
						Timestamp:   now.Add(-48 * time.Hour),
					},
				},
			},
		},
		CommitIndex: map[string][]string{
			"abc123def456": {"bv-test1"},
		},
	}

	od := NewOrphanDetector(report, "/tmp/test-repo")

	if od == nil {
		t.Fatal("Expected non-nil OrphanDetector")
	}

	// Check that temporal windows were built
	if len(od.beadWindows) != 1 {
		t.Errorf("Expected 1 bead window, got %d", len(od.beadWindows))
	}

	// Check that author -> beads mapping was built
	if len(od.authorBeads["test@example.com"]) != 1 {
		t.Errorf("Expected 1 bead for author, got %d", len(od.authorBeads["test@example.com"]))
	}
}

func TestNewOrphanDetectorAtPinsOpenWindow(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	claimed := pinned.Add(-48 * time.Hour)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-open": {
			Title:      "Open work",
			Status:     "in_progress",
			Milestones: BeadMilestones{Claimed: &BeadEvent{Timestamp: claimed}},
		},
	}}

	detector := NewOrphanDetectorAt(report, "", pinned)
	window, ok := detector.beadWindows["bv-open"]
	if !ok {
		t.Fatal("open bead window missing")
	}
	if !window.End.Equal(pinned) {
		t.Fatalf("open window end = %v, want %v", window.End, pinned)
	}
	if !detector.now.Equal(pinned) {
		t.Fatalf("detector now = %v, want %v", detector.now, pinned)
	}

	zeroDetector := NewOrphanDetectorAt(report, "", time.Time{})
	zeroWindow := zeroDetector.beadWindows["bv-open"]
	if !zeroDetector.now.IsZero() || !zeroWindow.End.IsZero() {
		t.Fatalf("zero instant was replaced: detector=%v window_end=%v", zeroDetector.now, zeroWindow.End)
	}
}

func TestNewOrphanDetectorAtUsesReopenedWindowAndDataHash(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	claimed := pinned.Add(-72 * time.Hour)
	closed := pinned.Add(-48 * time.Hour)
	reopened := pinned.Add(-24 * time.Hour)
	report := &HistoryReport{DataHash: "source-hash", Histories: map[string]BeadHistory{
		"bv-reopened": {
			Status: " open ",
			Milestones: BeadMilestones{
				Claimed:  &BeadEvent{Timestamp: claimed},
				Closed:   &BeadEvent{Timestamp: closed},
				Reopened: &BeadEvent{Timestamp: reopened},
			},
		},
	}}
	detector := NewOrphanDetectorAt(report, "", pinned)
	window := detector.beadWindows["bv-reopened"]
	if !window.Start.Equal(reopened) || !window.End.Equal(pinned) {
		t.Fatalf("reopened window=%v..%v, want %v..%v", window.Start, window.End, reopened, pinned)
	}
	if detector.dataHash != "source-hash" {
		t.Fatalf("detector data hash=%q, want source-hash", detector.dataHash)
	}
}

func TestScoreMentionedBeadRejectsAmbiguousCaseCollision(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-AbCd": {BeadID: "bv-AbCd", Title: "First", Status: "open"},
		"BV-aBcD": {BeadID: "BV-aBcD", Title: "Second", Status: "open"},
	}}
	detector := NewOrphanDetectorAt(report, "", time.Time{})

	for _, mention := range []string{"bv-abcd", "bv-AbCd"} {
		scores := make(map[string]*probableBeadBuilder)
		detector.scoreMentionedBead(scores, mention)
		if len(scores) != 0 {
			t.Fatalf("ambiguous mention %q credited %+v", mention, scores)
		}
	}

	unique := NewOrphanDetectorAt(&HistoryReport{Histories: map[string]BeadHistory{
		"bv-AbCd": {BeadID: "bv-AbCd", Title: "Only", Status: "open"},
	}}, "", time.Time{})
	scores := make(map[string]*probableBeadBuilder)
	unique.scoreMentionedBead(scores, "BV-ABCD")
	if got := scores["bv-AbCd"]; got == nil || got.score != 35 {
		t.Fatalf("unique case-insensitive match was not credited: %+v", scores)
	}
}

func TestNewSmartOrphanDetector(t *testing.T) {
	report := &HistoryReport{
		Histories:   make(map[string]BeadHistory),
		CommitIndex: make(map[string][]string),
	}

	od := NewSmartOrphanDetector(report, "/tmp/test-repo")
	if od == nil {
		t.Fatal("Expected non-nil OrphanDetector from SmartOrphanDetector alias")
	}
}

func TestOrphanCandidate_JSONRoundtrip(t *testing.T) {
	now := time.Now()
	candidate := OrphanCandidate{
		SHA:            "abc123",
		ShortSHA:       "abc1",
		Message:        "fix: test commit",
		Author:         "Test",
		AuthorEmail:    "test@example.com",
		Timestamp:      now,
		Files:          []string{"file1.go", "file2.go"},
		SuspicionScore: 75,
		ProbableBeads: []ProbableBead{
			{
				BeadID:     "bv-test",
				BeadTitle:  "Test Bead",
				BeadStatus: "open",
				Confidence: 80,
				Reasons:    []string{"timing", "author"},
			},
		},
		Signals: []OrphanSignalHit{
			{
				Signal:  SignalOrphanTiming,
				Details: "Commit during active period",
				Weight:  30,
			},
		},
	}

	// Just verify the struct is properly constructed
	if candidate.SuspicionScore != 75 {
		t.Errorf("Expected SuspicionScore 75, got %d", candidate.SuspicionScore)
	}
	if len(candidate.ProbableBeads) != 1 {
		t.Errorf("Expected 1 probable bead, got %d", len(candidate.ProbableBeads))
	}
	if len(candidate.Signals) != 1 {
		t.Errorf("Expected 1 signal, got %d", len(candidate.Signals))
	}
}

func TestOrphanReportStats(t *testing.T) {
	stats := OrphanReportStats{
		TotalCommits:    100,
		CorrelatedCount: 80,
		OrphanCount:     20,
		CandidateCount:  5,
		OrphanRatio:     0.2,
		AvgSuspicion:    65.0,
	}

	if stats.OrphanRatio != 0.2 {
		t.Errorf("Expected OrphanRatio 0.2, got %f", stats.OrphanRatio)
	}
	if stats.CandidateCount != 5 {
		t.Errorf("Expected CandidateCount 5, got %d", stats.CandidateCount)
	}
}

func TestOrphanSignalConstants(t *testing.T) {
	signals := []OrphanSignal{
		SignalOrphanTiming,
		SignalOrphanFiles,
		SignalOrphanMessage,
		SignalOrphanAuthor,
	}

	expected := []string{"timing", "files", "message", "author"}
	for i, signal := range signals {
		if string(signal) != expected[i] {
			t.Errorf("Expected signal %s, got %s", expected[i], string(signal))
		}
	}
}

func TestFormatGitRange(t *testing.T) {
	tests := []struct {
		name string
		opts ExtractOptions
		want string
	}{
		{
			name: "empty options",
			opts: ExtractOptions{},
			want: "all history",
		},
		{
			name: "with limit",
			opts: ExtractOptions{Limit: 100},
			want: "limit 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatGitRange(tt.opts)
			if got != tt.want {
				t.Errorf("formatGitRange() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendUnique(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  int
	}{
		{
			name:  "append to empty",
			slice: []string{},
			s:     "a",
			want:  1,
		},
		{
			name:  "append unique",
			slice: []string{"a", "b"},
			s:     "c",
			want:  3,
		},
		{
			name:  "append duplicate",
			slice: []string{"a", "b"},
			s:     "a",
			want:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendUnique(tt.slice, tt.s)
			if len(got) != tt.want {
				t.Errorf("appendUnique() length = %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestProbableBead_Fields(t *testing.T) {
	pb := ProbableBead{
		BeadID:     "bv-123",
		BeadTitle:  "Test Title",
		BeadStatus: "in_progress",
		Confidence: 85,
		Reasons:    []string{"timing match", "file overlap"},
	}

	if pb.BeadID != "bv-123" {
		t.Errorf("Expected BeadID 'bv-123', got %s", pb.BeadID)
	}
	if pb.Confidence != 85 {
		t.Errorf("Expected Confidence 85, got %d", pb.Confidence)
	}
	if len(pb.Reasons) != 2 {
		t.Errorf("Expected 2 reasons, got %d", len(pb.Reasons))
	}
}

func TestOrphanReport_Fields(t *testing.T) {
	now := time.Now()
	report := OrphanReport{
		GeneratedAt: now,
		GitRange:    "last 30 days",
		DataHash:    "abc123",
		Stats: OrphanReportStats{
			TotalCommits: 50,
			OrphanCount:  10,
		},
		Candidates: []OrphanCandidate{},
		ByBead:     map[string][]string{"bv-1": {"sha1", "sha2"}},
	}

	if report.GitRange != "last 30 days" {
		t.Errorf("Expected GitRange 'last 30 days', got %s", report.GitRange)
	}
	if len(report.ByBead["bv-1"]) != 2 {
		t.Errorf("Expected 2 commits for bv-1, got %d", len(report.ByBead["bv-1"]))
	}
}

func TestOrphanDetector_CustomIDPatternMatchesProbableBead(t *testing.T) {
	SetCustomIDPatterns([]*regexp.Regexp{regexp.MustCompile(`\bbh-[a-z0-9]{5}\b`)})
	t.Cleanup(func() { SetCustomIDPatterns(nil) })

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bh-8g6cj": {
				Title:  "Flush ordering",
				Status: "open",
			},
		},
		CommitIndex: map[string][]string{},
	}
	od := NewOrphanDetector(report, "/tmp/test-repo")

	candidate := &OrphanCandidate{
		SHA:     "abc123def456",
		Message: "fix flush ordering for bh-8g6cj",
	}
	beadScores := make(map[string]*probableBeadBuilder)
	od.checkMessage(candidate, beadScores)

	builder, ok := beadScores["bh-8g6cj"]
	if !ok {
		t.Fatalf("expected custom-pattern bead ID to be scored, got %#v", beadScores)
	}
	if builder.score < 35 {
		t.Errorf("expected mention score >= 35, got %d", builder.score)
	}

	// The custom pattern should also register as a message signal.
	foundSignal := false
	for _, sig := range candidate.Signals {
		if sig.Signal == SignalOrphanMessage {
			foundSignal = true
		}
	}
	if !foundSignal {
		t.Errorf("expected message signal from custom pattern, got %#v", candidate.Signals)
	}
}

// TestOrphanDetector_WindowMatchesIndex (D4): orphan detection scans exactly
// the commit window the correlation index was built from, reports it with
// source "history_index" regardless of the options passed, skips beads-only
// bookkeeping commits, and treats commits linked by any strategy (co-commit
// or explicit bead id in the message) as correlated rather than orphaned.
func TestOrphanDetector_WindowMatchesIndex(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	jsonl := filepath.Join(beadsDir, "issues.jsonl")
	writeBead := func(status string) {
		t.Helper()
		if err := os.WriteFile(jsonl, []byte(`{"id":"bv-1","title":"One","status":"`+status+`","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}
	}
	writeCode := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
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

	writeBead("open") // 1: beads-only seed
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "seed tracker")

	writeBead("in_progress") // 2: code + tracker in one commit (co-commit)
	writeCode("a.go", "package a\n")
	runGit(t, repo, "add", ".beads/issues.jsonl", "a.go")
	runGit(t, repo, "commit", "-m", "start work")
	coCommitSHA := head()

	// 3: code only by a different author, so neither co-commit, explicit id
	// nor temporal-author overlap can link it: the one true orphan.
	writeCode("b.go", "package b\n")
	runGit(t, repo, "add", "b.go")
	otherAuthor := exec.Command("git", "commit", "-m", "wip: unrelated tidy-up")
	otherAuthor.Dir = repo
	otherAuthor.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Other", "GIT_AUTHOR_EMAIL=other@example.com",
		"GIT_COMMITTER_NAME=Other", "GIT_COMMITTER_EMAIL=other@example.com",
	)
	if out, err := otherAuthor.CombinedOutput(); err != nil {
		t.Fatalf("commit as other author: %v\n%s", err, out)
	}
	orphanSHA := head()

	writeBead("closed") // 4: beads-only bookkeeping (br sync)
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "chore: sync beads")

	writeCode("c.go", "package c\n") // 5: code only but names the bead
	runGit(t, repo, "add", "c.go")
	runGit(t, repo, "commit", "-m", "fix(bv-1): follow-up")
	explicitSHA := head()

	beads := []BeadInfo{{ID: "bv-1", Title: "One", Status: "closed"}}
	report, err := NewCorrelator(repo, jsonl).GenerateReport(beads, CorrelatorOptions{Limit: 500})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.Window == nil || report.Window.Commits != 5 {
		t.Fatalf("report.Window=%+v; want the 5-commit walk recorded", report.Window)
	}
	if _, ok := report.CommitIndex[explicitSHA]; !ok {
		t.Fatalf("explicit-id commit %s missing from the index (D1 strategy): %v", explicitSHA[:7], report.CommitIndex)
	}

	// Deliberately different options: the index window must win.
	orphans, err := NewOrphanDetectorAt(report, repo, time.Now()).DetectOrphans(ExtractOptions{Limit: 2})
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	t.Logf("window=%+v stats=%+v", orphans.Window, orphans.Stats)
	if orphans.Window.Source != "history_index" || orphans.Window.Commits != 5 || orphans.Window.Limit != 500 {
		t.Fatalf("orphan window=%+v; want source=history_index commits=5 limit=500", orphans.Window)
	}
	s := orphans.Stats
	if s.BeadsOnlyCommits != 2 || s.TotalCommits != 3 || s.CorrelatedCount != 2 || s.OrphanCount != 1 {
		t.Fatalf("stats=%+v; want beads_only=2 total=3 correlated=2 orphans=1", s)
	}
	if s.TotalCommits+s.BeadsOnlyCommits != orphans.Window.Commits {
		t.Fatalf("total_commits+beads_only_commits=%d must equal window.commits=%d", s.TotalCommits+s.BeadsOnlyCommits, orphans.Window.Commits)
	}
	// Candidates are the scored subset of orphans; whatever scores, it can
	// only ever be the unlinked commit.
	for _, c := range orphans.Candidates {
		if c.SHA != orphanSHA {
			t.Fatalf("correlated commit %s reported as orphan candidate (orphan is %s)", c.SHA[:7], orphanSHA[:7])
		}
	}
	if coCommitSHA == orphanSHA || explicitSHA == orphanSHA {
		t.Fatalf("fixture SHAs collided")
	}
	var sawHint bool
	for _, h := range orphans.UsageHints {
		if strings.Contains(h, "beads_only_commits") {
			sawHint = true
		}
	}
	if !sawHint {
		t.Fatalf("usage_hints should document beads_only_commits: %v", orphans.UsageHints)
	}
}

func TestOrphanDetector_SharesExplicitMatcherParityForBase36AndDotIDs(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "alice@example.com")
	runGit(t, repo, "config", "user.name", "Alice")

	jsonl := filepath.Join(beadsDir, "issues.jsonl")
	seed := `{"id":"bv-pzpv","title":"Correlation overhaul","status":"open","priority":1,"issue_type":"task"}` + "\n" +
		`{"id":"bv-apal.20","title":"Dot child","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(jsonl, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "chore: seed beads")

	writeCommit := func(path, content, msg string) string {
		t.Helper()
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", path)
		cmd := exec.Command("git", "commit", "-m", msg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Other", "GIT_AUTHOR_EMAIL=other@example.com",
			"GIT_COMMITTER_NAME=Other", "GIT_COMMITTER_EMAIL=other@example.com",
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

	shaBase36 := writeCommit("pkg/a.go", "package pkg\n", "fix(bv-pzpv): wire Aho-Corasick matcher")
	shaDot := writeCommit("pkg/b.go", "package pkg\n", "Closes [BV-APAL.20].")
	shaNegPath := writeCommit("pkg/bv-pzpv/c.go", "package bvpzpv\n", "chore: refactor pkg/bv-pzpv/c.go and https://example.com/bv-apal.20")

	beads := []BeadInfo{
		{ID: "bv-pzpv", Title: "Correlation overhaul", Status: "open"},
		{ID: "bv-apal.20", Title: "Dot child", Status: "open"},
	}
	report, err := NewCorrelator(repo, jsonl).GenerateReport(beads, CorrelatorOptions{Limit: 100})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}

	if _, ok := report.CommitIndex[shaBase36]; !ok {
		t.Fatalf("expected base36 explicit commit %s to be correlated", shaBase36[:7])
	}
	if _, ok := report.CommitIndex[shaDot]; !ok {
		t.Fatalf("expected dot-hierarchical explicit commit %s to be correlated", shaDot[:7])
	}
	if _, ok := report.CommitIndex[shaNegPath]; ok {
		t.Fatalf("negative-control path/URL commit %s must not false-correlate", shaNegPath[:7])
	}

	detector := NewOrphanDetectorAt(report, repo, time.Now())
	orphans, err := detector.DetectOrphans(ExtractOptions{Limit: 100})
	if err != nil {
		t.Fatalf("DetectOrphans: %v", err)
	}
	if orphans.Stats.CorrelatedCount != 2 || orphans.Stats.OrphanCount != 1 {
		t.Fatalf("orphan stats = %+v, want correlated=2 orphan=1", orphans.Stats)
	}
	for _, cand := range orphans.Candidates {
		if cand.SHA == shaBase36 || cand.SHA == shaDot {
			t.Fatalf("explicitly correlated commit %s was flagged as orphan", cand.SHA[:7])
		}
		for _, sig := range cand.Signals {
			if sig.Signal == SignalOrphanMessage {
				t.Fatalf("negative-control path/URL commit %s received false SignalOrphanMessage: %+v", cand.SHA[:7], sig)
			}
		}
	}

	// Verify ReverseLookup.FindOrphanCommits also excludes base36 & dot-child commits
	// even when CommitIndex is unpopulated (exercising shared ExplicitMatcher directly).
	unindexedReport := &HistoryReport{
		Histories:   report.Histories,
		CommitIndex: CommitIndex{},
	}
	rl := NewReverseLookupWithRepo(unindexedReport, repo)
	rawOrphans, rlStats, err := rl.FindOrphanCommits(ExtractOptions{Limit: 100})
	if err != nil {
		t.Fatalf("FindOrphanCommits: %v", err)
	}
	if rlStats.CorrelatedCmts != 2 || rlStats.OrphanCommits != 1 {
		t.Fatalf("ReverseLookup stats = %+v, want correlated=2 orphan=1", rlStats)
	}
	for _, oc := range rawOrphans {
		if oc.SHA == shaBase36 || oc.SHA == shaDot {
			t.Fatalf("FindOrphanCommits returned explicitly referenced commit %s", oc.SHA[:7])
		}
	}
}
