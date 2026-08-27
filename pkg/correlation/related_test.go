package correlation

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestFindRelatedWork_NotFound(t *testing.T) {
	report := &HistoryReport{
		Histories:   make(map[string]BeadHistory),
		CommitIndex: make(CommitIndex),
	}

	result := report.FindRelatedWork("bv-notexist", DefaultRelatedWorkOptions())
	if result != nil {
		t.Errorf("Expected nil result for non-existent bead, got %+v", result)
	}
}

func TestFindRelatedWorkNilReport(t *testing.T) {
	var report *HistoryReport
	if result := report.FindRelatedWorkAt("anything", DefaultRelatedWorkOptions(), time.Time{}); result != nil {
		t.Fatalf("nil report returned %+v, want nil", result)
	}
}

func TestFindRelatedWorkAtPinsOpenWindowsAndMetadata(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"target": {
			BeadID:     "target",
			Title:      "Target",
			Status:     "open",
			Milestones: BeadMilestones{Created: &BeadEvent{Timestamp: pinned.Add(-48 * time.Hour)}},
		},
		"peer": {
			BeadID:     "peer",
			Title:      "Peer",
			Status:     "open",
			Milestones: BeadMilestones{Created: &BeadEvent{Timestamp: pinned.Add(-24 * time.Hour)}},
		},
	}}
	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0

	result := report.FindRelatedWorkAt("target", opts, pinned)
	if result == nil {
		t.Fatal("expected related-work result")
	}
	if !result.GeneratedAt.Equal(pinned) {
		t.Fatalf("generated_at = %v, want %v", result.GeneratedAt, pinned)
	}
	if len(result.Concurrent) != 1 || result.Concurrent[0].BeadID != "peer" {
		t.Fatalf("concurrent results = %#v, want peer", result.Concurrent)
	}

	zeroResult := report.FindRelatedWorkAt("target", opts, time.Time{})
	if !zeroResult.GeneratedAt.IsZero() {
		t.Fatalf("zero generated_at was replaced with %v", zeroResult.GeneratedAt)
	}
}

func TestFindRelatedWork_FileOverlap(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "abc123",
						Files: []FileChange{
							{Path: "pkg/auth/token.go"},
							{Path: "pkg/auth/session.go"},
						},
						Timestamp: now,
					},
				},
			},
			"bv-related": {
				BeadID: "bv-related",
				Title:  "Related Bead",
				Status: "in_progress",
				Commits: []CorrelatedCommit{
					{
						SHA: "def456",
						Files: []FileChange{
							{Path: "pkg/auth/token.go"}, // Same file
							{Path: "pkg/auth/handler.go"},
						},
						Timestamp: now.Add(-1 * time.Hour),
					},
				},
			},
			"bv-unrelated": {
				BeadID: "bv-unrelated",
				Title:  "Unrelated Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "ghi789",
						Files: []FileChange{
							{Path: "pkg/database/connection.go"}, // Different files
						},
						Timestamp: now,
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"abc123": {"bv-target"},
			"def456": {"bv-related"},
			"ghi789": {"bv-unrelated"},
		},
	}

	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0 // Accept all relevance scores

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.TargetBeadID != "bv-target" {
		t.Errorf("Expected target bead ID 'bv-target', got %s", result.TargetBeadID)
	}

	// Check file overlap includes related bead (shares token.go)
	foundRelated := false
	for _, rb := range result.FileOverlap {
		if rb.BeadID == "bv-related" {
			foundRelated = true
			if rb.RelationType != RelationFileOverlap {
				t.Errorf("Expected relation type file_overlap, got %s", rb.RelationType)
			}
			if rb.Relevance < 50 { // At least 50% overlap (1 of 2 files)
				t.Errorf("Expected relevance >= 50, got %d", rb.Relevance)
			}
		}
	}

	if !foundRelated {
		t.Error("Expected bv-related in FileOverlap results")
	}

	// Unrelated bead should not be in FileOverlap
	for _, rb := range result.FileOverlap {
		if rb.BeadID == "bv-unrelated" {
			t.Error("Unexpected bv-unrelated in FileOverlap results")
		}
	}
}

func TestFindRelatedWork_FileOverlapIgnoresEmptyTargetPaths(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"target": {
			BeadID: "target",
			Status: "open",
			Commits: []CorrelatedCommit{{
				SHA:   "target-commit",
				Files: []FileChange{{Path: "./"}, {Path: "shared.go"}},
			}},
		},
		"related": {
			BeadID: "related",
			Status: "open",
			Commits: []CorrelatedCommit{{
				SHA:   "related-commit",
				Files: []FileChange{{Path: "shared.go"}},
			}},
		},
	}}
	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 75

	result := report.FindRelatedWorkAt("target", opts, time.Time{})
	if result == nil || len(result.FileOverlap) != 1 {
		t.Fatalf("file overlap=%+v, want one relation above threshold", result)
	}
	if got := result.FileOverlap[0]; got.BeadID != "related" || got.Relevance != 100 {
		t.Fatalf("file overlap=%+v, want related at 100 relevance", got)
	}
}

func TestFindRelatedWork_CommitOverlap(t *testing.T) {
	now := time.Now()
	sharedSHA := "shared123"

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: sharedSHA, Timestamp: now},
					{SHA: "unique456", Timestamp: now.Add(-1 * time.Hour)},
				},
			},
			"bv-shared": {
				BeadID: "bv-shared",
				Title:  "Shared Commit Bead",
				Status: "in_progress",
				Commits: []CorrelatedCommit{
					{SHA: sharedSHA, Timestamp: now}, // Same commit
				},
			},
		},
		CommitIndex: CommitIndex{
			sharedSHA:   {"bv-target", "bv-shared"},
			"unique456": {"bv-target"},
		},
	}

	opts := DefaultRelatedWorkOptions()
	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check commit overlap
	foundShared := false
	for _, rb := range result.CommitOverlap {
		if rb.BeadID == "bv-shared" {
			foundShared = true
			if rb.RelationType != RelationCommitOverlap {
				t.Errorf("Expected relation type commit_overlap, got %s", rb.RelationType)
			}
		}
	}

	if !foundShared {
		t.Error("Expected bv-shared in CommitOverlap results")
	}
}

func TestFindRelatedWork_CommitOverlapIgnoresEmptySHA(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"target": {
				BeadID:  "target",
				Status:  "open",
				Commits: []CorrelatedCommit{{SHA: " \t"}},
			},
			"related": {BeadID: "related", Status: "open"},
		},
		CommitIndex: CommitIndex{" \t": {"target", "related"}},
	}
	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0

	result := report.FindRelatedWorkAt("target", opts, time.Time{})
	if result == nil {
		t.Fatal("expected related-work result")
	}
	if len(result.CommitOverlap) != 0 {
		t.Fatalf("empty SHA created a commit-overlap relation: %+v", result.CommitOverlap)
	}
}

func TestFindRelatedWork_FileOverlapOrdering(t *testing.T) {
	now := time.Now()

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "c1",
						Files: []FileChange{
							{Path: "z.go"},
							{Path: "a.go"},
						},
						Timestamp: now,
					},
				},
			},
			"bv-both": {
				BeadID: "bv-both",
				Title:  "Shares Both",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "c2",
						Files: []FileChange{
							{Path: "a.go"},
							{Path: "z.go"},
						},
						Timestamp: now.Add(-1 * time.Hour),
					},
				},
			},
			"bv-aaa": {
				BeadID: "bv-aaa",
				Title:  "Shares A",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "c3",
						Files: []FileChange{
							{Path: "a.go"},
						},
						Timestamp: now.Add(-2 * time.Hour),
					},
				},
			},
			"bv-bbb": {
				BeadID: "bv-bbb",
				Title:  "Shares Z",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA: "c4",
						Files: []FileChange{
							{Path: "z.go"},
						},
						Timestamp: now.Add(-3 * time.Hour),
					},
				},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.FileOverlap) < 3 {
		t.Fatalf("Expected at least 3 file-overlap results, got %d", len(result.FileOverlap))
	}

	if result.FileOverlap[0].BeadID != "bv-both" {
		t.Fatalf("Expected highest relevance first (bv-both), got %s", result.FileOverlap[0].BeadID)
	}
	if result.FileOverlap[1].BeadID != "bv-aaa" || result.FileOverlap[2].BeadID != "bv-bbb" {
		t.Fatalf("Expected tie-break by bead ID (bv-aaa then bv-bbb), got %s, %s",
			result.FileOverlap[1].BeadID, result.FileOverlap[2].BeadID)
	}

	wantShared := []string{"a.go", "z.go"}
	if !reflect.DeepEqual(result.FileOverlap[0].SharedFiles, wantShared) {
		t.Fatalf("Expected shared files sorted %v, got %v", wantShared, result.FileOverlap[0].SharedFiles)
	}
}

func TestFindRelatedWork_CommitOverlapSharedCommitsSorted(t *testing.T) {
	now := time.Now()

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "b0000001", Timestamp: now},
					{SHA: "a0000002", Timestamp: now.Add(-1 * time.Hour)},
				},
			},
			"bv-shared": {
				BeadID: "bv-shared",
				Title:  "Shared Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "a0000002", Timestamp: now},
					{SHA: "b0000001", Timestamp: now},
				},
			},
		},
		CommitIndex: CommitIndex{
			"a0000002": {"bv-target", "bv-shared"},
			"b0000001": {"bv-target", "bv-shared"},
		},
	}

	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if len(result.CommitOverlap) == 0 {
		t.Fatal("Expected commit overlap results")
	}

	wantCommits := []string{"a0000002", "b0000001"}
	if !reflect.DeepEqual(result.CommitOverlap[0].SharedCommits, wantCommits) {
		t.Fatalf("Expected shared commits sorted %v, got %v", wantCommits, result.CommitOverlap[0].SharedCommits)
	}
}

func TestFindRelatedWork_DependencyCluster(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
			},
			"bv-direct-dep": {
				BeadID: "bv-direct-dep",
				Title:  "Direct Dependency",
				Status: "in_progress",
			},
			"bv-indirect-dep": {
				BeadID: "bv-indirect-dep",
				Title:  "Indirect Dependency",
				Status: "open",
			},
			"bv-depends-on-target": {
				BeadID: "bv-depends-on-target",
				Title:  "Depends On Target",
				Status: "open",
			},
		},
		CommitIndex: make(CommitIndex),
	}

	depGraph := map[string][]string{
		"bv-target":            {"bv-direct-dep"},   // target depends on direct-dep
		"bv-direct-dep":        {"bv-indirect-dep"}, // direct-dep depends on indirect-dep
		"bv-depends-on-target": {"bv-target"},       // depends-on-target depends on target
	}

	opts := DefaultRelatedWorkOptions()
	opts.DependencyGraph = depGraph
	opts.MinRelevance = 0

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check dependency cluster
	beadsByID := make(map[string]RelatedWorkBead)
	for _, rb := range result.DependencyCluster {
		beadsByID[rb.BeadID] = rb
	}

	// Direct dependency (1 hop) should have high relevance
	if directDep, ok := beadsByID["bv-direct-dep"]; ok {
		if directDep.Relevance != 80 {
			t.Errorf("Expected direct dep relevance 80, got %d", directDep.Relevance)
		}
	} else {
		t.Error("Expected bv-direct-dep in DependencyCluster")
	}

	// Reverse dependency (1 hop) should also have high relevance
	if reverseDep, ok := beadsByID["bv-depends-on-target"]; ok {
		if reverseDep.Relevance != 80 {
			t.Errorf("Expected reverse dep relevance 80, got %d", reverseDep.Relevance)
		}
	} else {
		t.Error("Expected bv-depends-on-target in DependencyCluster")
	}

	// Indirect dependency (2 hops) should have lower relevance
	if indirectDep, ok := beadsByID["bv-indirect-dep"]; ok {
		if indirectDep.Relevance != 40 {
			t.Errorf("Expected indirect dep relevance 40, got %d", indirectDep.Relevance)
		}
	} else {
		t.Error("Expected bv-indirect-dep in DependencyCluster")
	}
}

func TestFindRelatedWork_DependencyClusterTraversesReverseSecondHop(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"target":   {BeadID: "target", Status: "open"},
			"consumer": {BeadID: "consumer", Status: "open"},
			"outer":    {BeadID: "outer", Status: "open"},
		},
		CommitIndex: make(CommitIndex),
	}
	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0
	opts.DependencyGraph = map[string][]string{
		"consumer": {"target"},
		"outer":    {"consumer"},
	}

	result := report.FindRelatedWorkAt("target", opts, time.Time{})
	if result == nil {
		t.Fatal("expected related-work result")
	}
	byID := make(map[string]RelatedWorkBead)
	for _, bead := range result.DependencyCluster {
		byID[bead.BeadID] = bead
	}
	if got := byID["consumer"].Relevance; got != 80 {
		t.Fatalf("direct reverse dependency relevance = %d, want 80", got)
	}
	if got := byID["outer"].Relevance; got != 40 {
		t.Fatalf("second-hop reverse dependency relevance = %d, want 40", got)
	}
}

func TestFindRelatedWork_Concurrent(t *testing.T) {
	now := time.Now()

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-3 * 24 * time.Hour)},
				},
			},
			"bv-concurrent": {
				BeadID: "bv-concurrent",
				Title:  "Concurrent Bead",
				Status: "in_progress",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-5 * 24 * time.Hour)},
				},
			},
			"bv-old": {
				BeadID: "bv-old",
				Title:  "Old Bead",
				Status: "closed",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-60 * 24 * time.Hour)},
					Closed:  &BeadEvent{Timestamp: now.Add(-30 * 24 * time.Hour)},
				},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	opts := DefaultRelatedWorkOptions()
	opts.ConcurrencyWindow = 7 * 24 * time.Hour // 1 week window
	opts.MinRelevance = 0
	opts.IncludeClosed = false

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Concurrent bead should be found
	foundConcurrent := false
	for _, rb := range result.Concurrent {
		if rb.BeadID == "bv-concurrent" {
			foundConcurrent = true
			if rb.RelationType != RelationConcurrent {
				t.Errorf("Expected relation type concurrent, got %s", rb.RelationType)
			}
		}
	}

	if !foundConcurrent {
		t.Error("Expected bv-concurrent in Concurrent results")
	}

	// Old closed bead should NOT be found (not included and outside window)
	for _, rb := range result.Concurrent {
		if rb.BeadID == "bv-old" {
			t.Error("Unexpected bv-old in Concurrent results (should be excluded)")
		}
	}
}

func TestFindRelatedWork_ConcurrentDoesNotBridgeClosedReopenGap(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"target": {
				BeadID: "target",
				Status: "open",
				Milestones: BeadMilestones{
					Created:  &BeadEvent{Timestamp: now.Add(-20 * 24 * time.Hour)},
					Closed:   &BeadEvent{Timestamp: now.Add(-10 * 24 * time.Hour)},
					Reopened: &BeadEvent{Timestamp: now.Add(-2 * 24 * time.Hour)},
				},
			},
			"during-gap": {
				BeadID: "during-gap",
				Status: "closed",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-8 * 24 * time.Hour)},
					Closed:  &BeadEvent{Timestamp: now.Add(-6 * 24 * time.Hour)},
				},
			},
			"after-reopen": {
				BeadID: "after-reopen",
				Status: "open",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-24 * time.Hour)},
				},
			},
		},
		CommitIndex: make(CommitIndex),
	}
	opts := DefaultRelatedWorkOptions()
	opts.ConcurrencyWindow = 0
	opts.MinRelevance = 0
	opts.IncludeClosed = true

	result := report.FindRelatedWorkAt("target", opts, now)
	if result == nil {
		t.Fatal("expected related-work result")
	}
	byID := make(map[string]RelatedWorkBead)
	for _, bead := range result.Concurrent {
		byID[bead.BeadID] = bead
	}
	if _, exists := byID["during-gap"]; exists {
		t.Fatal("bead active only during the closed/reopen gap was marked concurrent")
	}
	if _, exists := byID["after-reopen"]; !exists {
		t.Fatal("bead active after reopen was not marked concurrent")
	}
}

func TestFindRelatedWorkConcurrentStartsAtClaimRatherThanCreation(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"target": {
			BeadID: "target",
			Status: "in_progress",
			Milestones: BeadMilestones{
				Created: &BeadEvent{Timestamp: now.Add(-20 * 24 * time.Hour)},
				Claimed: &BeadEvent{Timestamp: now.Add(-10 * 24 * time.Hour)},
			},
		},
		"backlog-only-peer": {
			BeadID: "backlog-only-peer",
			Status: "closed",
			Milestones: BeadMilestones{
				Created: &BeadEvent{Timestamp: now.Add(-16 * 24 * time.Hour)},
				Claimed: &BeadEvent{Timestamp: now.Add(-16 * 24 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-15 * 24 * time.Hour)},
			},
		},
		"active-peer": {
			BeadID: "active-peer",
			Status: "closed",
			Milestones: BeadMilestones{
				Created: &BeadEvent{Timestamp: now.Add(-12 * 24 * time.Hour)},
				Claimed: &BeadEvent{Timestamp: now.Add(-9 * 24 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-8 * 24 * time.Hour)},
			},
		},
	}}
	opts := DefaultRelatedWorkOptions()
	opts.ConcurrencyWindow = 0
	opts.MinRelevance = 0
	opts.IncludeClosed = true

	result := report.FindRelatedWorkAt("target", opts, now)
	if result == nil {
		t.Fatal("expected related-work result")
	}
	byID := make(map[string]bool, len(result.Concurrent))
	for _, bead := range result.Concurrent {
		byID[bead.BeadID] = true
	}
	if byID["backlog-only-peer"] {
		t.Fatal("peer completed before target claim was manufactured as concurrent")
	}
	if !byID["active-peer"] {
		t.Fatal("peer active after target claim was not concurrent")
	}
}

func TestFindRelatedWorkConcurrentUsesLatestActivationFromFullEventHistory(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"target": {
			BeadID: "target",
			Status: "in_progress",
			Events: []BeadEvent{
				{EventType: EventClaimed, Timestamp: now.Add(-20 * 24 * time.Hour)},
				{EventType: EventClosed, Timestamp: now.Add(-15 * 24 * time.Hour)},
				{EventType: EventReopened, Timestamp: now.Add(-10 * 24 * time.Hour)},
				{EventType: EventClaimed, Timestamp: now.Add(-8 * 24 * time.Hour)},
			},
		},
		"before-final-claim": {
			BeadID: "before-final-claim",
			Status: "closed",
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{Timestamp: now.Add(-9 * 24 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-9 * 24 * time.Hour)},
			},
		},
		"after-final-claim": {
			BeadID: "after-final-claim",
			Status: "closed",
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{Timestamp: now.Add(-7 * 24 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-6 * 24 * time.Hour)},
			},
		},
	}}
	opts := DefaultRelatedWorkOptions()
	opts.ConcurrencyWindow = 0
	opts.MinRelevance = 0
	opts.IncludeClosed = true

	result := report.FindRelatedWorkAt("target", opts, now)
	if result == nil {
		t.Fatal("expected related-work result")
	}
	byID := make(map[string]bool, len(result.Concurrent))
	for _, bead := range result.Concurrent {
		byID[bead.BeadID] = true
	}
	if byID["before-final-claim"] {
		t.Fatal("peer completed before the final claim was marked concurrent")
	}
	if !byID["after-final-claim"] {
		t.Fatal("peer active after the final claim was not concurrent")
	}
}

func TestFindRelatedWork_ConcurrentTreatsNegativeWindowAsZero(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"target": {
			BeadID: "target",
			Status: "closed",
			Milestones: BeadMilestones{
				Created: &BeadEvent{Timestamp: now.Add(-4 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-2 * time.Hour)},
			},
		},
		"overlap": {
			BeadID: "overlap",
			Status: "closed",
			Milestones: BeadMilestones{
				Created: &BeadEvent{Timestamp: now.Add(-3 * time.Hour)},
				Closed:  &BeadEvent{Timestamp: now.Add(-time.Hour)},
			},
		},
	}}
	opts := DefaultRelatedWorkOptions()
	opts.IncludeClosed = true
	opts.MinRelevance = 0
	opts.ConcurrencyWindow = -24 * time.Hour

	result := report.FindRelatedWorkAt("target", opts, now)
	if result == nil || len(result.Concurrent) != 1 || result.Concurrent[0].BeadID != "overlap" {
		t.Fatalf("negative concurrency tolerance removed a real overlap: %+v", result)
	}
}

func TestFormatIntRelatedHandlesMinimumInt(t *testing.T) {
	minInt := -int(^uint(0)>>1) - 1
	if got, want := formatIntRelated(minInt), strconv.Itoa(minInt); got != want {
		t.Fatalf("formatIntRelated(minInt) = %q, want %q", got, want)
	}
}

func TestFindRelatedWork_ExcludesClosed(t *testing.T) {
	now := time.Now()

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-target": {
				BeadID: "bv-target",
				Title:  "Target Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "abc123", Files: []FileChange{{Path: "shared.go"}}, Timestamp: now},
				},
			},
			"bv-closed": {
				BeadID: "bv-closed",
				Title:  "Closed Bead",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{SHA: "def456", Files: []FileChange{{Path: "shared.go"}}, Timestamp: now},
				},
			},
			"bv-tombstone": {
				BeadID: "bv-tombstone",
				Title:  "Tombstone Bead",
				Status: "tombstone",
				Commits: []CorrelatedCommit{
					{SHA: "ghi789", Files: []FileChange{{Path: "shared.go"}}, Timestamp: now},
				},
			},
		},
		CommitIndex: make(CommitIndex),
	}

	// Without IncludeClosed
	opts := DefaultRelatedWorkOptions()
	opts.MinRelevance = 0

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	beadsByID := make(map[string]RelatedWorkBead)
	for _, rb := range result.FileOverlap {
		beadsByID[rb.BeadID] = rb
	}
	if _, ok := beadsByID["bv-closed"]; ok {
		t.Error("Closed bead should be excluded when IncludeClosed=false")
	}
	if _, ok := beadsByID["bv-tombstone"]; ok {
		t.Error("Tombstone bead should be excluded even when IncludeClosed=false")
	}

	// With IncludeClosed
	opts.IncludeClosed = true
	result = report.FindRelatedWork("bv-target", opts)

	beadsByID = make(map[string]RelatedWorkBead)
	for _, rb := range result.FileOverlap {
		beadsByID[rb.BeadID] = rb
	}
	if _, ok := beadsByID["bv-closed"]; !ok {
		t.Error("Closed bead should be included when IncludeClosed=true")
	}
	if _, ok := beadsByID["bv-tombstone"]; ok {
		t.Error("Tombstone bead should be excluded even when IncludeClosed=true")
	}
}

func TestFindRelatedWork_MaxResults(t *testing.T) {
	now := time.Now()

	histories := map[string]BeadHistory{
		"bv-target": {
			BeadID: "bv-target",
			Title:  "Target",
			Status: "open",
			Commits: []CorrelatedCommit{
				{SHA: "shared", Files: []FileChange{{Path: "shared.go"}}, Timestamp: now},
			},
		},
	}

	// Create 20 related beads
	for i := 0; i < 20; i++ {
		id := "bv-related-" + formatIntRelated(i)
		histories[id] = BeadHistory{
			BeadID: id,
			Title:  "Related " + formatIntRelated(i),
			Status: "open",
			Commits: []CorrelatedCommit{
				{SHA: "commit" + formatIntRelated(i), Files: []FileChange{{Path: "shared.go"}}, Timestamp: now},
			},
		}
	}

	report := &HistoryReport{
		Histories:   histories,
		CommitIndex: make(CommitIndex),
	}

	opts := DefaultRelatedWorkOptions()
	opts.MaxResults = 5
	opts.MinRelevance = 0

	result := report.FindRelatedWork("bv-target", opts)
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if len(result.FileOverlap) > 5 {
		t.Errorf("Expected max 5 results, got %d", len(result.FileOverlap))
	}
}

func TestDefaultRelatedWorkOptions(t *testing.T) {
	opts := DefaultRelatedWorkOptions()

	if opts.MinRelevance != 20 {
		t.Errorf("Expected MinRelevance 20, got %d", opts.MinRelevance)
	}

	if opts.MaxResults != 10 {
		t.Errorf("Expected MaxResults 10, got %d", opts.MaxResults)
	}

	if opts.ConcurrencyWindow != 7*24*time.Hour {
		t.Errorf("Expected ConcurrencyWindow 7 days, got %v", opts.ConcurrencyWindow)
	}

	if opts.IncludeClosed {
		t.Error("Expected IncludeClosed false by default")
	}
}

func TestRelationType_Values(t *testing.T) {
	tests := []struct {
		rt   RelationType
		want string
	}{
		{RelationFileOverlap, "file_overlap"},
		{RelationCommitOverlap, "commit_overlap"},
		{RelationDependencyCluster, "dependency_cluster"},
		{RelationConcurrent, "concurrent"},
	}

	for _, tt := range tests {
		if string(tt.rt) != tt.want {
			t.Errorf("Expected %s, got %s", tt.want, string(tt.rt))
		}
	}
}
