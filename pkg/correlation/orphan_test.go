package correlation

import (
	"context"
	"regexp"
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
				LastAuthor: "Test Author",
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

func TestNewOrphanDetectorNilReportIsSafe(t *testing.T) {
	detector := NewOrphanDetectorAt(nil, "", time.Time{})
	if detector == nil {
		t.Fatal("expected a detector for a nil report")
	}
	if detector.dataHash != "" || len(detector.beadWindows) != 0 || len(detector.authorBeads) != 0 {
		t.Fatalf("nil report populated detector state: %#v", detector)
	}
	if _, err := detector.DetectOrphans(ExtractOptions{}); err == nil {
		t.Fatal("expected missing-repository error, got nil")
	}
}

func TestNewOrphanDetectorBuildsAuthorMapWithoutLastAuthor(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-work": {
			Commits: []CorrelatedCommit{{AuthorEmail: "worker@example.com"}},
		},
	}}
	detector := NewOrphanDetectorAt(report, "", time.Time{})
	if got := detector.authorBeads["worker@example.com"]; len(got) != 1 || got[0] != "bv-work" {
		t.Fatalf("author mapping = %v, want [bv-work]", got)
	}
}

func TestNewOrphanDetectorExcludesTombstonesFromProbableBeadEvidence(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-deleted": {
			Title:  "Deleted work",
			Status: " TOMBSTONE ",
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{Timestamp: now.Add(-48 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
			},
			Commits: []CorrelatedCommit{{AuthorEmail: "worker@example.com"}},
		},
	}}

	detector := NewOrphanDetectorAt(report, "", now)
	if _, exists := detector.beadWindows["bv-deleted"]; exists {
		t.Fatal("tombstoned bead retained a timing window")
	}
	if got := detector.authorBeads["worker@example.com"]; len(got) != 0 {
		t.Fatalf("tombstoned bead retained author evidence: %v", got)
	}

	scores := make(map[string]*probableBeadBuilder)
	detector.scoreMentionedBead(scores, "BV-DELETED")
	if len(scores) != 0 {
		t.Fatalf("message mention resurrected tombstoned bead: %#v", scores)
	}
}

func TestNewOrphanDetectorSkipsClosedWindowWithoutCloseEvent(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-closed": {
			Status: "closed",
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{Timestamp: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
			},
		},
	}}
	detector := NewOrphanDetectorAt(report, "", time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	if _, exists := detector.beadWindows["bv-closed"]; exists {
		t.Fatal("closed bead without a close event received an open-ended activity window")
	}
}

func TestOrphanDetectorWithContextPropagatesToLookup(t *testing.T) {
	detector := NewOrphanDetectorAt(&HistoryReport{}, "", time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if got := detector.WithContext(ctx); got != detector {
		t.Fatalf("WithContext returned %p, want receiver %p", got, detector)
	}
	if detector.ctx != ctx || detector.lookup.ctx != ctx {
		t.Fatal("context was not propagated to orphan Git lookups")
	}
	if (*OrphanDetector)(nil).WithContext(ctx) != nil {
		t.Fatal("nil receiver should remain nil")
	}
}

func TestNilOrphanDetectorDetectOrphansReturnsError(t *testing.T) {
	var detector *OrphanDetector
	if _, err := detector.DetectOrphans(ExtractOptions{}); err == nil {
		t.Fatal("expected nil-detector error, got nil")
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
	windows := detector.beadWindows["bv-open"]
	if len(windows) != 1 {
		t.Fatal("open bead window missing")
	}
	window := windows[0]
	if !window.End.Equal(pinned) {
		t.Fatalf("open window end = %v, want %v", window.End, pinned)
	}
	if !detector.now.Equal(pinned) {
		t.Fatalf("detector now = %v, want %v", detector.now, pinned)
	}

	zeroDetector := NewOrphanDetectorAt(report, "", time.Time{})
	zeroWindows := zeroDetector.beadWindows["bv-open"]
	if !zeroDetector.now.IsZero() || len(zeroWindows) != 0 {
		t.Fatalf("zero instant was replaced or created a future window: detector=%v windows=%v", zeroDetector.now, zeroWindows)
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
	window := detector.beadWindows["bv-reopened"][0]
	if !window.Start.Equal(reopened) || !window.End.Equal(pinned) {
		t.Fatalf("reopened window=%v..%v, want %v..%v", window.Start, window.End, reopened, pinned)
	}
	if detector.dataHash != "source-hash" {
		t.Fatalf("detector data hash=%q, want source-hash", detector.dataHash)
	}
}

func TestNewOrphanDetectorUsesAllIntervalsFromFullEventHistory(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	firstClaim := now.Add(-10 * 24 * time.Hour)
	firstClose := now.Add(-8 * 24 * time.Hour)
	reopened := now.Add(-4 * 24 * time.Hour)
	finalClaim := now.Add(-3 * 24 * time.Hour)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-reclaimed": {
			Title:  "Reclaimed work",
			Status: "in_progress",
			Milestones: BeadMilestones{
				Claimed:  &BeadEvent{Timestamp: firstClaim},
				Closed:   &BeadEvent{Timestamp: firstClose},
				Reopened: &BeadEvent{Timestamp: reopened},
			},
			Events: []BeadEvent{
				{EventType: EventClaimed, Timestamp: firstClaim},
				{EventType: EventClosed, Timestamp: firstClose},
				{EventType: EventReopened, Timestamp: reopened},
				{EventType: EventClaimed, Timestamp: finalClaim},
			},
		},
	}}

	windows := NewOrphanDetectorAt(report, "", now).beadWindows["bv-reclaimed"]
	if len(windows) != 2 {
		t.Fatalf("activity windows = %d, want both completed and current intervals", len(windows))
	}
	if !windows[0].Start.Equal(firstClaim) || !windows[0].End.Equal(firstClose) {
		t.Fatalf("first orphan activity window = %v..%v, want %v..%v", windows[0].Start, windows[0].End, firstClaim, firstClose)
	}
	if !windows[1].Start.Equal(finalClaim) || !windows[1].End.Equal(now) {
		t.Fatalf("current orphan activity window = %v..%v, want %v..%v", windows[1].Start, windows[1].End, finalClaim, now)
	}
}

func TestNewOrphanDetectorAtUsesReopenWithoutClaim(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	reopened := pinned.Add(-24 * time.Hour)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-reopened": {
			Status: "open",
			Milestones: BeadMilestones{
				Reopened: &BeadEvent{Timestamp: reopened},
			},
		},
	}}

	detector := NewOrphanDetectorAt(report, "", pinned)
	windows := detector.beadWindows["bv-reopened"]
	if len(windows) != 1 {
		t.Fatal("reopened bead without an earlier claim received no activity window")
	}
	window := windows[0]
	if !window.Start.Equal(reopened) || !window.End.Equal(pinned) {
		t.Fatalf("reopened window=%v..%v, want %v..%v", window.Start, window.End, reopened, pinned)
	}
}

func TestOrphanDetectorScoresEveryActivityIntervalAndKeepsCurrentStatus(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	firstClaim := now.Add(-20 * 24 * time.Hour)
	firstClose := now.Add(-18 * 24 * time.Hour)
	secondClaim := now.Add(-4 * 24 * time.Hour)
	secondClose := now.Add(-2 * 24 * time.Hour)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-repeated": {
			Title:  "Repeated work",
			Status: "closed",
			Events: []BeadEvent{
				{EventType: EventClaimed, Timestamp: firstClaim},
				{EventType: EventClosed, Timestamp: firstClose},
				{EventType: EventReopened, Timestamp: secondClaim},
				{EventType: EventClosed, Timestamp: secondClose},
			},
		},
	}}
	detector := NewOrphanDetectorAt(report, "", now)

	for _, timestamp := range []time.Time{firstClaim, firstClose, secondClaim, secondClose} {
		candidate := &OrphanCandidate{Timestamp: timestamp}
		scores := make(map[string]*probableBeadBuilder)
		detector.checkTiming(candidate, scores)
		if len(candidate.Signals) != 1 {
			t.Fatalf("timestamp %v produced %d timing signals, want 1", timestamp, len(candidate.Signals))
		}
		if got := scores["bv-repeated"]; got == nil || got.score != 30 || got.status != "closed" {
			t.Fatalf("timestamp %v score = %+v, want score 30 with current closed status", timestamp, got)
		}
	}
}

func TestOrphanDetectorAuthorEmailIsCaseInsensitive(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-owned": {
			Title:   "Owned work",
			Status:  "in_progress",
			Events:  []BeadEvent{{EventType: EventClaimed, Timestamp: now.Add(-time.Hour)}},
			Commits: []CorrelatedCommit{{AuthorEmail: " Dev@Example.COM "}},
		},
	}}
	detector := NewOrphanDetectorAt(report, "", now)
	candidate := &OrphanCandidate{AuthorEmail: "dev@example.com", Timestamp: now}
	scores := make(map[string]*probableBeadBuilder)
	detector.checkAuthor(candidate, scores)
	if len(candidate.Signals) != 1 {
		t.Fatalf("case-insensitive author match produced %d signals, want 1", len(candidate.Signals))
	}
	if got := scores["bv-owned"]; got == nil || got.score != 15 {
		t.Fatalf("case-insensitive author score = %+v, want 15", got)
	}
}

func TestNewOrphanDetectorAtSkipsCloseBeforeLatestReopen(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	closed := pinned.Add(-48 * time.Hour)
	reopened := pinned.Add(-24 * time.Hour)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-reopened": {
			Status: "closed",
			Milestones: BeadMilestones{
				Closed:   &BeadEvent{Timestamp: closed},
				Reopened: &BeadEvent{Timestamp: reopened},
			},
			Commits: []CorrelatedCommit{{AuthorEmail: "worker@example.com"}},
		},
	}}

	detector := NewOrphanDetectorAt(report, "", pinned)
	if _, ok := detector.beadWindows["bv-reopened"]; ok {
		t.Fatal("close before the latest reopen produced an inverted activity window")
	}
	candidate := &OrphanCandidate{
		AuthorEmail: "worker@example.com",
		Timestamp:   closed.Add(12 * time.Hour),
	}
	scores := make(map[string]*probableBeadBuilder)
	detector.checkAuthor(candidate, scores)
	if len(candidate.Signals) != 0 || len(scores) != 0 {
		t.Fatalf("inverted lifecycle emitted author evidence: signals=%v scores=%v", candidate.Signals, scores)
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

func TestCheckMessageMatchesShortAndHierarchicalBeadIDs(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1":     {BeadID: "bv-1", Title: "Short", Status: "open"},
		"bv-ab.2":  {BeadID: "bv-ab.2", Title: "Child", Status: "open"},
		"bv-ab-c3": {BeadID: "bv-ab-c3", Title: "Hyphenated", Status: "open"},
	}}
	detector := NewOrphanDetectorAt(report, "", time.Time{})

	for _, beadID := range []string{"bv-1", "bv-ab.2", "bv-ab-c3"} {
		t.Run(beadID, func(t *testing.T) {
			candidate := &OrphanCandidate{Message: "fix " + beadID}
			scores := make(map[string]*probableBeadBuilder)
			detector.checkMessage(candidate, scores)
			if got := scores[beadID]; got == nil || got.score != 35 {
				t.Fatalf("message score for %s = %+v, want 35", beadID, got)
			}
		})
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
