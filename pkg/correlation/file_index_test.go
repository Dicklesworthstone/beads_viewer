package correlation

import (
	"strings"
	"testing"
	"time"
)

func stringMatches(got, want string) bool {
	return strings.Compare(got, want) == 0
}

func hasStringValue(values []string, want string) bool {
	for _, value := range values {
		if stringMatches(value, want) {
			return true
		}
	}
	return false
}

func TestBuildFileIndex(t *testing.T) {
	// Create a test history report
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-123": {
				BeadID: "bv-123",
				Title:  "Auth refactor",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "abc123",
						ShortSHA:  "abc123",
						Timestamp: now.Add(-24 * time.Hour),
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 10, Deletions: 5},
							{Path: "pkg/auth/session.go", Insertions: 20, Deletions: 0},
						},
					},
					{
						SHA:       "def456",
						ShortSHA:  "def456",
						Timestamp: now.Add(-12 * time.Hour),
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 5, Deletions: 2},
						},
					},
				},
			},
			"bv-456": {
				BeadID: "bv-456",
				Title:  "API update",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "ghi789",
						ShortSHA:  "ghi789",
						Timestamp: now.Add(-6 * time.Hour),
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 3, Deletions: 1},
							{Path: "pkg/api/routes.go", Insertions: 50, Deletions: 10},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"abc123": {"bv-123"},
			"def456": {"bv-123"},
			"ghi789": {"bv-456"},
		},
	}

	index := BuildFileIndex(report)

	// Check stats
	if index.Stats.TotalFiles != 3 {
		t.Errorf("Expected 3 files, got %d", index.Stats.TotalFiles)
	}

	// token.go should have 2 beads, others should have 1
	tokenRefs := index.FileToBeads["pkg/auth/token.go"]
	if len(tokenRefs) != 2 {
		t.Errorf("Expected 2 beads for token.go, got %d", len(tokenRefs))
	}

	// Verify bead references for token.go
	var foundBv123, foundBv456 bool
	for _, ref := range tokenRefs {
		if stringMatches(ref.BeadID, "bv-123") {
			foundBv123 = true
			if len(ref.CommitSHAs) != 2 {
				t.Errorf("Expected 2 commits for bv-123, got %d", len(ref.CommitSHAs))
			}
			if ref.TotalChanges != 22 { // 10+5+5+2
				t.Errorf("Expected 22 total changes for bv-123, got %d", ref.TotalChanges)
			}
		}
		if stringMatches(ref.BeadID, "bv-456") {
			foundBv456 = true
		}
	}
	if !foundBv123 || !foundBv456 {
		t.Error("Expected both bv-123 and bv-456 in token.go references")
	}

	// Check files with multiple beads
	if index.Stats.FilesWithMultipleBeads != 1 {
		t.Errorf("Expected 1 file with multiple beads, got %d", index.Stats.FilesWithMultipleBeads)
	}
}

func TestBuildFileIndexCommitIDsUseFullSHA(t *testing.T) {
	shaA := "abcdefg" + strings.Repeat("1", 33)
	shaB := "abcdefg" + strings.Repeat("2", 33)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Status: "open",
			Commits: []CorrelatedCommit{
				{SHA: shaA, ShortSHA: shaA[:7], Files: []FileChange{{Path: "shared.go"}}},
				{SHA: shaB, ShortSHA: shaB[:7], Files: []FileChange{{Path: "shared.go"}}},
			},
		},
	}}

	refs := BuildFileIndex(report).FileToBeads["shared.go"]
	if len(refs) != 1 {
		t.Fatalf("file references = %#v, want one bead", refs)
	}
	got := refs[0].CommitSHAs
	if len(got) != 2 || got[0] != shaA || got[1] != shaB {
		t.Fatalf("commit_shas = %#v, want sorted full SHAs [%q %q]", got, shaA, shaB)
	}
	if hasStringValue(got, shaA[:7]) {
		t.Fatalf("commit_shas retained collision-prone short SHA %q", shaA[:7])
	}
}

func TestBuildFileIndexEmpty(t *testing.T) {
	// Nil report
	index := BuildFileIndex(nil)
	if index == nil {
		t.Fatal("Expected non-nil index for nil report")
	}
	if len(index.FileToBeads) != 0 {
		t.Errorf("Expected empty FileToBeads, got %d entries", len(index.FileToBeads))
	}

	// Empty report
	emptyReport := &HistoryReport{
		Histories:   map[string]BeadHistory{},
		CommitIndex: CommitIndex{},
	}
	index = BuildFileIndex(emptyReport)
	if len(index.FileToBeads) != 0 {
		t.Errorf("Expected empty FileToBeads for empty report, got %d entries", len(index.FileToBeads))
	}
}

func TestBuildFileIndexSkipsEmptyNormalizedPaths(t *testing.T) {
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Status: "open",
			Commits: []CorrelatedCommit{{
				SHA: "full-sha",
				Files: []FileChange{
					{Path: ""},
					{Path: "./"},
					{Path: "real.go"},
				},
			}},
		},
	}}

	index := BuildFileIndex(report)
	if _, ok := index.FileToBeads[""]; ok {
		t.Fatal("empty normalized path was indexed")
	}
	if len(index.FileToBeads) != 1 || len(index.FileToBeads["real.go"]) != 1 {
		t.Fatalf("file index = %#v, want only real.go", index.FileToBeads)
	}
}

func TestNewFileLookupNil(t *testing.T) {
	// NewFileLookup should not panic with nil report
	lookup := NewFileLookup(nil)
	if lookup == nil {
		t.Fatal("Expected non-nil lookup for nil report")
	}

	// Should return empty results for any lookup
	result := lookup.LookupByFile("any/path.go")
	if result.TotalBeads != 0 {
		t.Errorf("Expected 0 beads for nil report lookup, got %d", result.TotalBeads)
	}

	// GetHotspots should return empty slice
	hotspots := lookup.GetHotspots(10)
	if len(hotspots) != 0 {
		t.Errorf("Expected 0 hotspots for nil report, got %d", len(hotspots))
	}

	// GetAllFiles should return empty slice
	files := lookup.GetAllFiles()
	if len(files) != 0 {
		t.Errorf("Expected 0 files for nil report, got %d", len(files))
	}

	// GetStats should return zero stats
	stats := lookup.GetStats()
	if stats.TotalFiles != 0 || stats.TotalBeadLinks != 0 {
		t.Errorf("Expected zero stats for nil report, got %+v", stats)
	}
}

func TestFileLookupByFile(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-123": {
				BeadID: "bv-123",
				Title:  "Auth fix",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "abc123",
						ShortSHA:  "abc123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 10, Deletions: 5},
						},
					},
				},
			},
			"bv-456": {
				BeadID: "bv-456",
				Title:  "Token refresh",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "def456",
						ShortSHA:  "def456",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 5, Deletions: 2},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"abc123": {"bv-123"},
			"def456": {"bv-456"},
		},
	}

	lookup := NewFileLookup(report)

	// Exact file lookup
	result := lookup.LookupByFile("pkg/auth/token.go")
	if result.TotalBeads != 2 {
		t.Errorf("Expected 2 total beads, got %d", result.TotalBeads)
	}
	if len(result.OpenBeads) != 1 {
		t.Errorf("Expected 1 open bead, got %d", len(result.OpenBeads))
	}
	if len(result.ClosedBeads) != 1 {
		t.Errorf("Expected 1 closed bead, got %d", len(result.ClosedBeads))
	}

	// Non-existent file
	result = lookup.LookupByFile("nonexistent.go")
	if result.TotalBeads != 0 {
		t.Errorf("Expected 0 beads for nonexistent file, got %d", result.TotalBeads)
	}
}

func TestFileLookupByFileReturnsCallerOwnedCommitSHAs(t *testing.T) {
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-owned": {
				BeadID: "bv-owned",
				Title:  "Owned lookup result",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:      "abcdef1234567890",
						ShortSHA: "abcdef1",
						Files:    []FileChange{{Path: "pkg/owned.go", Insertions: 1}},
					},
				},
			},
		},
	}
	lookup := NewFileLookup(report)
	first := lookup.LookupByFile("pkg/owned.go")
	if len(first.OpenBeads) != 1 || len(first.OpenBeads[0].CommitSHAs) != 1 {
		t.Fatalf("first lookup = %+v, want one open bead with one commit", first)
	}
	first.OpenBeads[0].CommitSHAs[0] = "caller-mutated"

	second := lookup.LookupByFile("pkg/owned.go")
	if got := second.OpenBeads[0].CommitSHAs[0]; got != "abcdef1234567890" {
		t.Fatalf("caller mutation leaked into exact-path index: got %q", got)
	}
}

func TestFileLookupByFile_ExcludesTombstone(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-open": {
				BeadID: "bv-open",
				Title:  "Open Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "open123",
						ShortSHA:  "open123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 1, Deletions: 0},
						},
					},
				},
			},
			"bv-closed": {
				BeadID: "bv-closed",
				Title:  "Closed Bead",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "closed123",
						ShortSHA:  "closed123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 2, Deletions: 1},
						},
					},
				},
			},
			"bv-tomb": {
				BeadID: "bv-tomb",
				Title:  "Tombstone Bead",
				Status: "tombstone",
				Commits: []CorrelatedCommit{
					{
						SHA:       "tomb123",
						ShortSHA:  "tomb123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 3, Deletions: 2},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"open123":   {"bv-open"},
			"closed123": {"bv-closed"},
			"tomb123":   {"bv-tomb"},
		},
	}

	lookup := NewFileLookup(report)

	result := lookup.LookupByFile("pkg/auth/token.go")
	if result.TotalBeads != 2 {
		t.Fatalf("Expected 2 total beads (tombstone excluded), got %d", result.TotalBeads)
	}
	if len(result.OpenBeads) != 1 || !stringMatches(result.OpenBeads[0].BeadID, "bv-open") {
		t.Fatalf("Expected open beads [bv-open], got %+v", result.OpenBeads)
	}
	if len(result.ClosedBeads) != 1 || !stringMatches(result.ClosedBeads[0].BeadID, "bv-closed") {
		t.Fatalf("Expected closed beads [bv-closed], got %+v", result.ClosedBeads)
	}
}

func TestFileLookupByFileGlob_ExcludesTombstone(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-open": {
				BeadID: "bv-open",
				Title:  "Open Bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "open123",
						ShortSHA:  "open123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 1, Deletions: 0},
						},
					},
				},
			},
			"bv-tomb": {
				BeadID: "bv-tomb",
				Title:  "Tombstone Bead",
				Status: "tombstone",
				Commits: []CorrelatedCommit{
					{
						SHA:       "tomb123",
						ShortSHA:  "tomb123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 3, Deletions: 2},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"open123": {"bv-open"},
			"tomb123": {"bv-tomb"},
		},
	}

	lookup := NewFileLookup(report)
	result := lookup.LookupByFileGlob("pkg/auth/*.go")
	if result.TotalBeads != 1 {
		t.Fatalf("Expected 1 total bead (tombstone excluded), got %d", result.TotalBeads)
	}
	if len(result.OpenBeads) != 1 || !stringMatches(result.OpenBeads[0].BeadID, "bv-open") {
		t.Fatalf("Expected open beads [bv-open], got %+v", result.OpenBeads)
	}
	if len(result.ClosedBeads) != 0 {
		t.Fatalf("Expected no closed beads, got %+v", result.ClosedBeads)
	}
}

func TestFileLookupByDirectory(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-123": {
				BeadID: "bv-123",
				Title:  "Auth fix",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "abc123",
						ShortSHA:  "abc123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 10, Deletions: 5},
							{Path: "pkg/auth/session.go", Insertions: 5, Deletions: 2},
						},
					},
				},
			},
			"bv-456": {
				BeadID: "bv-456",
				Title:  "API update",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "def456",
						ShortSHA:  "def456",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/api/routes.go", Insertions: 20, Deletions: 10},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"abc123": {"bv-123"},
			"def456": {"bv-456"},
		},
	}

	lookup := NewFileLookup(report)

	// Directory lookup for pkg/auth
	result := lookup.LookupByFile("pkg/auth")
	if result.TotalBeads != 1 {
		t.Errorf("Expected 1 bead for pkg/auth directory, got %d", result.TotalBeads)
	}
	if len(result.OpenBeads) != 1 || !stringMatches(result.OpenBeads[0].BeadID, "bv-123") {
		t.Error("Expected bv-123 in open beads for pkg/auth")
	}
}

func TestFileLookupByDirectory_SortsByLastTouch(t *testing.T) {
	now := time.Now()
	older := now.Add(-1 * time.Hour)

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-new": {
				BeadID: "bv-new",
				Title:  "Newest",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "new123",
						ShortSHA:  "new123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/new.go"},
						},
					},
				},
			},
			"bv-a": {
				BeadID: "bv-a",
				Title:  "Older A",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a123",
						ShortSHA:  "a123",
						Timestamp: older,
						Files: []FileChange{
							{Path: "pkg/auth/a.go"},
						},
					},
				},
			},
			"bv-b": {
				BeadID: "bv-b",
				Title:  "Older B",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "b123",
						ShortSHA:  "b123",
						Timestamp: older,
						Files: []FileChange{
							{Path: "pkg/auth/b.go"},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"new123": {"bv-new"},
			"a123":   {"bv-a"},
			"b123":   {"bv-b"},
		},
	}

	lookup := NewFileLookup(report)

	result := lookup.LookupByFile("pkg/auth")
	if len(result.OpenBeads) != 3 {
		t.Fatalf("Expected 3 open beads, got %d", len(result.OpenBeads))
	}

	// Sorted by LastTouch desc, then BeadID asc for ties.
	if !stringMatches(result.OpenBeads[0].BeadID, "bv-new") {
		t.Fatalf("Expected first bead bv-new, got %s", result.OpenBeads[0].BeadID)
	}
	if !stringMatches(result.OpenBeads[1].BeadID, "bv-a") || !stringMatches(result.OpenBeads[2].BeadID, "bv-b") {
		t.Fatalf("Expected tie-break order bv-a then bv-b, got %s, %s",
			result.OpenBeads[1].BeadID, result.OpenBeads[2].BeadID)
	}
}

func TestFileLookupAggregatesDuplicateBeadReferences(t *testing.T) {
	now := time.Now()
	older := now.Add(-2 * time.Hour)

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-merge": {
				BeadID: "bv-merge",
				Title:  "Touches multiple files",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "older123",
						ShortSHA:  "older123",
						Timestamp: older,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 3, Deletions: 2},
						},
					},
					{
						SHA:       "newer123",
						ShortSHA:  "newer123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/session.go", Insertions: 10, Deletions: 1},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"older123": {"bv-merge"},
			"newer123": {"bv-merge"},
		},
	}

	lookup := NewFileLookup(report)

	tests := []struct {
		name   string
		lookup func() *FileBeadLookupResult
	}{
		{
			name: "directory",
			lookup: func() *FileBeadLookupResult {
				return lookup.LookupByFile("pkg/auth")
			},
		},
		{
			name: "glob",
			lookup: func() *FileBeadLookupResult {
				return lookup.LookupByFileGlob("pkg/auth/*.go")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.lookup()

			if len(result.OpenBeads) != 1 {
				t.Fatalf("Expected one merged open bead, got %+v", result.OpenBeads)
			}
			ref := result.OpenBeads[0]
			if !stringMatches(ref.BeadID, "bv-merge") {
				t.Fatalf("Expected merged bead bv-merge, got %s", ref.BeadID)
			}
			if !ref.LastTouch.Equal(now) {
				t.Fatalf("Expected newest last touch %s, got %s", now, ref.LastTouch)
			}
			if ref.TotalChanges != 16 {
				t.Fatalf("Expected summed changes 16, got %d", ref.TotalChanges)
			}
			if len(ref.CommitSHAs) != 2 || !hasStringValue(ref.CommitSHAs, "older123") || !hasStringValue(ref.CommitSHAs, "newer123") {
				t.Fatalf("Expected both commit SHAs after merge, got %#v", ref.CommitSHAs)
			}
		})
	}
}

func TestFileLookupByGlob(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-123": {
				BeadID: "bv-123",
				Title:  "Go files fix",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "abc123",
						ShortSHA:  "abc123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/token.go", Insertions: 10, Deletions: 5},
							{Path: "pkg/api/routes.go", Insertions: 20, Deletions: 10},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"abc123": {"bv-123"},
		},
	}

	lookup := NewFileLookup(report)

	// Glob pattern for all .go files in pkg/auth
	result := lookup.LookupByFileGlob("pkg/auth/*.go")
	if result.TotalBeads != 1 {
		t.Errorf("Expected 1 bead for glob pattern, got %d", result.TotalBeads)
	}
}

func TestFileLookupByGlob_SortsByLastTouch(t *testing.T) {
	now := time.Now()
	older := now.Add(-2 * time.Hour)

	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-new": {
				BeadID: "bv-new",
				Title:  "Newest",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "new123",
						ShortSHA:  "new123",
						Timestamp: now,
						Files: []FileChange{
							{Path: "pkg/auth/new.go"},
						},
					},
				},
			},
			"bv-old": {
				BeadID: "bv-old",
				Title:  "Older",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "old123",
						ShortSHA:  "old123",
						Timestamp: older,
						Files: []FileChange{
							{Path: "pkg/auth/old.go"},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"new123": {"bv-new"},
			"old123": {"bv-old"},
		},
	}

	lookup := NewFileLookup(report)

	result := lookup.LookupByFileGlob("pkg/auth/*.go")
	if len(result.OpenBeads) != 2 {
		t.Fatalf("Expected 2 open beads, got %d", len(result.OpenBeads))
	}
	if !stringMatches(result.OpenBeads[0].BeadID, "bv-new") || !stringMatches(result.OpenBeads[1].BeadID, "bv-old") {
		t.Fatalf("Expected order bv-new then bv-old, got %s then %s",
			result.OpenBeads[0].BeadID, result.OpenBeads[1].BeadID)
	}
}

func TestGetHotspots(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Fix 1",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a",
						ShortSHA:  "a",
						Timestamp: now,
						Files:     []FileChange{{Path: "hot.go"}},
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "Fix 2",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "b",
						ShortSHA:  "b",
						Timestamp: now,
						Files:     []FileChange{{Path: "hot.go"}},
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "Fix 3",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "c",
						ShortSHA:  "c",
						Timestamp: now,
						Files:     []FileChange{{Path: "hot.go"}, {Path: "cold.go"}},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"a": {"bv-1"},
			"b": {"bv-2"},
			"c": {"bv-3"},
		},
	}

	lookup := NewFileLookup(report)

	hotspots := lookup.GetHotspots(2)
	if len(hotspots) != 2 {
		t.Fatalf("Expected 2 hotspots, got %d", len(hotspots))
	}

	// hot.go should be first with 3 beads
	if !stringMatches(hotspots[0].FilePath, "hot.go") {
		t.Errorf("Expected hot.go as top hotspot, got %s", hotspots[0].FilePath)
	}
	if hotspots[0].TotalBeads != 3 {
		t.Errorf("Expected 3 beads for hot.go, got %d", hotspots[0].TotalBeads)
	}
	if hotspots[0].OpenBeads != 2 {
		t.Errorf("Expected 2 open beads for hot.go, got %d", hotspots[0].OpenBeads)
	}
	if hotspots[0].ClosedBeads != 1 {
		t.Errorf("Expected 1 closed bead for hot.go, got %d", hotspots[0].ClosedBeads)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"./pkg/auth/token.go", "pkg/auth/token.go"},
		{".\\pkg\\auth\\token.go", "pkg/auth/token.go"},
		{"pkg/auth/token.go", "pkg/auth/token.go"},
		{"pkg/auth/", "pkg/auth"},
		{"./", ""},
	}

	for _, tt := range tests {
		result := normalizePath(tt.input)
		if !stringMatches(result, tt.expected) {
			t.Errorf("normalizePath(%q) = %q, expected %q", tt.input, result, tt.expected)
		}
	}
}

func TestGetAllFiles(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Fix 1",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a",
						ShortSHA:  "a",
						Timestamp: now,
						Files: []FileChange{
							{Path: "b.go"},
							{Path: "a.go"},
							{Path: "c.go"},
						},
					},
				},
			},
		},
		CommitIndex: CommitIndex{"a": {"bv-1"}},
	}

	lookup := NewFileLookup(report)
	files := lookup.GetAllFiles()

	if len(files) != 3 {
		t.Fatalf("Expected 3 files, got %d", len(files))
	}

	// Should be sorted
	expected := []string{"a.go", "b.go", "c.go"}
	for i, f := range files {
		if !stringMatches(f, expected[i]) {
			t.Errorf("File %d: expected %q, got %q", i, expected[i], f)
		}
	}
}

func TestImpactAnalysisEmpty(t *testing.T) {
	lookup := NewFileLookup(nil)
	result := lookup.ImpactAnalysis([]string{})
	if !stringMatches(result.Summary, "No files to analyze") {
		t.Errorf("Expected 'No files to analyze', got %q", result.Summary)
	}
	if len(result.AffectedBeads) != 0 {
		t.Errorf("Expected 0 affected beads, got %d", len(result.AffectedBeads))
	}
}

func TestImpactAnalysisWithOpenBeads(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "In progress work",
				Status: "in_progress",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a",
						ShortSHA:  "a",
						Timestamp: now.Add(-1 * time.Hour),
						Files:     []FileChange{{Path: "auth/token.go", Insertions: 10}},
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "Open task",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "b",
						ShortSHA:  "b",
						Timestamp: now.Add(-2 * time.Hour),
						Files:     []FileChange{{Path: "auth/token.go", Insertions: 5}},
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "Closed task",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "c",
						ShortSHA:  "c",
						Timestamp: now.Add(-24 * time.Hour),
						Files:     []FileChange{{Path: "auth/token.go", Insertions: 3}},
					},
				},
			},
		},
		CommitIndex: CommitIndex{
			"a": {"bv-1"},
			"b": {"bv-2"},
			"c": {"bv-3"},
		},
	}

	lookup := NewFileLookup(report)
	result := lookup.ImpactAnalysis([]string{"auth/token.go"})

	// Should have high risk due to in_progress bead
	if !stringMatches(result.RiskLevel, "high") && !stringMatches(result.RiskLevel, "critical") {
		t.Errorf("Expected high or critical risk, got %q", result.RiskLevel)
	}

	// Should have warnings
	if len(result.Warnings) == 0 {
		t.Error("Expected warnings for in_progress bead")
	}

	// in_progress should be first
	if len(result.AffectedBeads) < 1 || !stringMatches(result.AffectedBeads[0].Status, "in_progress") {
		t.Error("Expected in_progress bead to be first")
	}
}

func TestImpactAnalysisNormalizesStatusForRiskAndOrdering(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	mkHistory := func(id, status, sha string) BeadHistory {
		return BeadHistory{
			BeadID: id,
			Title:  id,
			Status: status,
			Commits: []CorrelatedCommit{{
				SHA:       sha,
				ShortSHA:  sha,
				Timestamp: now.Add(-time.Hour),
				Files:     []FileChange{{Path: "shared.go"}},
			}},
		}
	}
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-progress": mkHistory("bv-progress", " IN_PROGRESS ", "progress-sha"),
		"bv-open":     mkHistory("bv-open", " Open ", "open-sha"),
		"bv-closed":   mkHistory("bv-closed", " CLOSED ", "closed-sha"),
	}}

	result := NewFileLookup(report).ImpactAnalysisAt([]string{"shared.go"}, now)
	if diff := result.RiskScore - 0.65; result.RiskLevel != "high" || diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("normalized status risk = %s/%v, want high/0.65: %+v", result.RiskLevel, result.RiskScore, result)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("warnings = %#v, want active-work and open-bead warnings", result.Warnings)
	}
	wantOrder := []string{"bv-progress", "bv-open", "bv-closed"}
	if len(result.AffectedBeads) != len(wantOrder) {
		t.Fatalf("affected beads = %#v, want %v", result.AffectedBeads, wantOrder)
	}
	for i, want := range wantOrder {
		if result.AffectedBeads[i].BeadID != want {
			t.Fatalf("affected_beads[%d] = %q, want %q", i, result.AffectedBeads[i].BeadID, want)
		}
	}
}

func TestImpactAnalysisTreatsOtherLiveStatusesAsOpen(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-blocked": {
			BeadID: "bv-blocked",
			Status: " Blocked ",
			Commits: []CorrelatedCommit{{
				SHA:       "blocked-sha",
				ShortSHA:  "blocked",
				Timestamp: now,
				Files:     []FileChange{{Path: "shared.go"}},
			}},
		},
	}}

	result := NewFileLookup(report).ImpactAnalysisAt([]string{"shared.go"}, now)
	if result.RiskLevel != "medium" || result.RiskScore != 0.2 {
		t.Fatalf("live nonstandard status was scored as closed: %+v", result)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "Open beads") {
		t.Fatalf("live nonstandard status did not produce open-work warning: %#v", result.Warnings)
	}
	if !strings.Contains(result.Summary, "1 open bead") {
		t.Fatalf("live nonstandard status summary = %q, want open bead", result.Summary)
	}
}

func TestImpactAnalysisRiskLevels(t *testing.T) {
	now := time.Now()

	// Test low risk (only old closed beads)
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Old closed",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a",
						ShortSHA:  "a",
						Timestamp: now.Add(-10 * 24 * time.Hour), // 10 days ago
						Files:     []FileChange{{Path: "old.go"}},
					},
				},
			},
		},
		CommitIndex: CommitIndex{"a": {"bv-1"}},
	}

	lookup := NewFileLookup(report)
	result := lookup.ImpactAnalysis([]string{"old.go"})

	// Should be low risk since the closed bead is > 7 days old
	if !stringMatches(result.RiskLevel, "low") {
		t.Errorf("Expected low risk for old closed beads, got %q", result.RiskLevel)
	}
}

func TestImpactAnalysisEmptyStringsFiltered(t *testing.T) {
	lookup := NewFileLookup(nil)

	// Empty strings should be filtered out
	result := lookup.ImpactAnalysis([]string{"", "  ", ""})
	if !stringMatches(result.Summary, "No valid files to analyze") {
		t.Errorf("Expected 'No valid files to analyze' for empty strings, got %q", result.Summary)
	}
}

func TestImpactAnalysisDuplicatesRemoved(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Test bead",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:       "a",
						ShortSHA:  "a",
						Timestamp: now,
						Files:     []FileChange{{Path: "test.go", Insertions: 10}},
					},
				},
			},
		},
		CommitIndex: CommitIndex{"a": {"bv-1"}},
	}

	lookup := NewFileLookup(report)

	// Pass same file twice - should only appear once in OverlapFiles
	result := lookup.ImpactAnalysis([]string{"test.go", "test.go", "./test.go"})

	if len(result.AffectedBeads) != 1 {
		t.Fatalf("Expected 1 affected bead, got %d", len(result.AffectedBeads))
	}

	// OverlapFiles should only have one entry (duplicates removed)
	if len(result.AffectedBeads[0].OverlapFiles) != 1 {
		t.Errorf("Expected 1 overlap file (deduped), got %d: %v",
			len(result.AffectedBeads[0].OverlapFiles),
			result.AffectedBeads[0].OverlapFiles)
	}
}

// Co-change detection tests

func TestBuildCoChangeMatrix(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Commits: []CorrelatedCommit{
					{
						SHA:       "commit1",
						ShortSHA:  "c1",
						Timestamp: now,
						Files: []FileChange{
							{Path: "auth/token.go"},
							{Path: "auth/session.go"},
							{Path: "config/auth.yaml"},
						},
					},
					{
						SHA:       "commit2",
						ShortSHA:  "c2",
						Timestamp: now.Add(-24 * time.Hour),
						Files: []FileChange{
							{Path: "auth/token.go"},
							{Path: "auth/session.go"},
						},
					},
				},
			},
		},
	}

	matrix := BuildCoChangeMatrix(report)

	// token.go should have 2 commits
	if matrix.FileCommitCounts["auth/token.go"] != 2 {
		t.Errorf("Expected 2 commits for token.go, got %d", matrix.FileCommitCounts["auth/token.go"])
	}

	// session.go should also have 2 commits
	if matrix.FileCommitCounts["auth/session.go"] != 2 {
		t.Errorf("Expected 2 commits for session.go, got %d", matrix.FileCommitCounts["auth/session.go"])
	}

	// config/auth.yaml should have 1 commit
	if matrix.FileCommitCounts["config/auth.yaml"] != 1 {
		t.Errorf("Expected 1 commit for auth.yaml, got %d", matrix.FileCommitCounts["config/auth.yaml"])
	}

	// token.go -> session.go should be 2 (they changed together twice)
	if matrix.Matrix["auth/token.go"]["auth/session.go"] != 2 {
		t.Errorf("Expected 2 co-changes between token.go and session.go, got %d",
			matrix.Matrix["auth/token.go"]["auth/session.go"])
	}

	// token.go -> auth.yaml should be 1 (only in commit1)
	if matrix.Matrix["auth/token.go"]["config/auth.yaml"] != 1 {
		t.Errorf("Expected 1 co-change between token.go and auth.yaml, got %d",
			matrix.Matrix["auth/token.go"]["config/auth.yaml"])
	}
}

func TestBuildCoChangeMatrixSamplesUseFullSHA(t *testing.T) {
	shaA := "abcdefg" + strings.Repeat("1", 33)
	shaB := "abcdefg" + strings.Repeat("2", 33)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-1": {
			BeadID: "bv-1",
			Commits: []CorrelatedCommit{
				{SHA: shaA, ShortSHA: shaA[:7], Files: []FileChange{{Path: "source.go"}, {Path: "related.go"}}},
				{SHA: shaB, ShortSHA: shaB[:7], Files: []FileChange{{Path: "source.go"}, {Path: "related.go"}}},
			},
		},
	}}

	matrix := BuildCoChangeMatrix(report)
	if len(matrix.CommitFiles) != 2 {
		t.Fatalf("CommitFiles has %d entries, want 2 distinct full SHAs: %#v", len(matrix.CommitFiles), matrix.CommitFiles)
	}
	if _, exists := matrix.CommitFiles[shaA[:7]]; exists {
		t.Fatalf("CommitFiles retained colliding short-SHA key %q", shaA[:7])
	}

	result := matrix.GetRelatedFiles("source.go", 0.5, 10)
	if len(result.RelatedFiles) != 1 {
		t.Fatalf("related files = %#v, want one entry", result.RelatedFiles)
	}
	got := result.RelatedFiles[0].SampleCommits
	if len(got) != 2 || got[0] != shaA || got[1] != shaB {
		t.Fatalf("sample commits = %#v, want sorted full SHAs [%q %q]", got, shaA, shaB)
	}
}

func TestBuildCoChangeMatrixUnionsComplementarySameSHAObservations(t *testing.T) {
	sha := strings.Repeat("a", 40)
	report := &HistoryReport{Histories: map[string]BeadHistory{
		"bv-source": {
			BeadID: "bv-source",
			Commits: []CorrelatedCommit{{
				SHA: sha, ShortSHA: sha[:7], Files: []FileChange{{Path: "./source.go"}},
			}},
		},
		"bv-related": {
			BeadID: "bv-related",
			Commits: []CorrelatedCommit{{
				SHA: sha, ShortSHA: sha[:7], Files: []FileChange{{Path: "related.go"}},
			}},
		},
	}}

	matrix := BuildCoChangeMatrix(report)
	files := matrix.CommitFiles[sha]
	if len(files) != 2 || files[0] != "related.go" || files[1] != "source.go" {
		t.Fatalf("same-SHA file union = %#v, want [related.go source.go]", files)
	}
	if matrix.FileCommitCounts["source.go"] != 1 || matrix.FileCommitCounts["related.go"] != 1 {
		t.Fatalf("same commit was counted more than once: %#v", matrix.FileCommitCounts)
	}
	if matrix.Matrix["source.go"]["related.go"] != 1 || matrix.Matrix["related.go"]["source.go"] != 1 {
		t.Fatalf("complementary observations did not form co-change pair: %#v", matrix.Matrix)
	}
	result := matrix.GetRelatedFiles("source.go", 1, 10)
	if len(result.RelatedFiles) != 1 || len(result.RelatedFiles[0].SampleCommits) != 1 || result.RelatedFiles[0].SampleCommits[0] != sha {
		t.Fatalf("related-file sample did not retain full SHA: %+v", result)
	}
}

func TestCoChangeMatrixNil(t *testing.T) {
	matrix := BuildCoChangeMatrix(nil)

	if len(matrix.Matrix) != 0 {
		t.Error("Expected empty matrix for nil report")
	}

	if len(matrix.FileCommitCounts) != 0 {
		t.Error("Expected empty file commit counts for nil report")
	}
}

func TestGetRelatedFiles(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Commits: []CorrelatedCommit{
					{SHA: "a", ShortSHA: "a", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, {Path: "auth/session.go"}, {Path: "config/auth.yaml"},
					}},
					{SHA: "b", ShortSHA: "b", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, {Path: "auth/session.go"},
					}},
					{SHA: "c", ShortSHA: "c", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, {Path: "auth/session.go"},
					}},
					{SHA: "d", ShortSHA: "d", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, {Path: "auth/session.go"},
					}},
					{SHA: "e", ShortSHA: "e", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, // Only token.go
					}},
				},
			},
		},
	}

	lookup := NewFileLookup(report)
	result := lookup.GetRelatedFiles("auth/token.go", 0.5, 10)

	// token.go has 5 commits
	if result.TotalCommits != 5 {
		t.Errorf("Expected 5 total commits, got %d", result.TotalCommits)
	}

	// session.go should be related (4/5 = 0.8 correlation)
	foundSession := false
	for _, entry := range result.RelatedFiles {
		if stringMatches(entry.FilePath, "auth/session.go") {
			foundSession = true
			if entry.CoChangeCount != 4 {
				t.Errorf("Expected 4 co-changes for session.go, got %d", entry.CoChangeCount)
			}
			// 4/5 = 0.8
			if entry.Correlation < 0.79 || entry.Correlation > 0.81 {
				t.Errorf("Expected ~0.8 correlation, got %f", entry.Correlation)
			}
		}
	}
	if !foundSession {
		t.Error("Expected session.go in related files")
	}

	// config/auth.yaml should NOT be in results with 0.5 threshold (1/5 = 0.2 < 0.5)
	for _, entry := range result.RelatedFiles {
		if stringMatches(entry.FilePath, "config/auth.yaml") {
			t.Error("config/auth.yaml should not appear with 0.5 threshold (correlation is 0.2)")
		}
	}
}

func TestGetRelatedFilesWithLowThreshold(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Commits: []CorrelatedCommit{
					{SHA: "a", ShortSHA: "a", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"}, {Path: "config/auth.yaml"},
					}},
					{SHA: "b", ShortSHA: "b", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"},
					}},
					{SHA: "c", ShortSHA: "c", Timestamp: now, Files: []FileChange{
						{Path: "auth/token.go"},
					}},
				},
			},
		},
	}

	lookup := NewFileLookup(report)

	// With 0.5 threshold, auth.yaml shouldn't appear (1/3 = 0.33)
	result := lookup.GetRelatedFiles("auth/token.go", 0.5, 10)
	if len(result.RelatedFiles) != 0 {
		t.Errorf("Expected no related files with 0.5 threshold, got %d", len(result.RelatedFiles))
	}

	// With 0.3 threshold, auth.yaml should appear
	result = lookup.GetRelatedFiles("auth/token.go", 0.3, 10)
	if len(result.RelatedFiles) != 1 {
		t.Errorf("Expected 1 related file with 0.3 threshold, got %d", len(result.RelatedFiles))
	}
}

func TestGetRelatedFilesLimit(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Commits: []CorrelatedCommit{
					{SHA: "a", ShortSHA: "a", Timestamp: now, Files: []FileChange{
						{Path: "main.go"}, {Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}, {Path: "d.go"},
					}},
				},
			},
		},
	}

	lookup := NewFileLookup(report)
	result := lookup.GetRelatedFiles("main.go", 0.5, 2) // Limit to 2

	if len(result.RelatedFiles) > 2 {
		t.Errorf("Expected at most 2 related files due to limit, got %d", len(result.RelatedFiles))
	}
}

// tombstoneAgreementReport builds a report where three tombstoned beads and no
// live beads touch AGENTS.md via correlated commits — the exact shape behind
// issue #184, where --robot-file-hotspots reported 3 beads for a path while
// --robot-file-beads reported 0 and --robot-impact said "safe to proceed".
func tombstoneAgreementReport(now time.Time) *HistoryReport {
	mkHistory := func(id, status, sha string) BeadHistory {
		return BeadHistory{
			BeadID: id,
			Title:  "Bead " + id,
			Status: status,
			Commits: []CorrelatedCommit{
				{
					SHA:       sha,
					ShortSHA:  sha,
					Timestamp: now,
					Files:     []FileChange{{Path: "AGENTS.md", Insertions: 1}},
				},
			},
		}
	}
	return &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-t1": mkHistory("bv-t1", "tombstone", "sha1"),
			"bv-t2": mkHistory("bv-t2", "tombstone", "sha2"),
			"bv-t3": mkHistory("bv-t3", "tombstone", "sha3"),
		},
		CommitIndex: CommitIndex{
			"sha1": {"bv-t1"},
			"sha2": {"bv-t2"},
			"sha3": {"bv-t3"},
		},
	}
}

// TestFileSurfacesAgree_TombstoneOnly locks in the #184 fix: when every bead
// touching a file is tombstoned, hotspots, per-file lookup, impact analysis,
// and the index stats must all agree that zero beads touch the file.
func TestFileSurfacesAgree_TombstoneOnly(t *testing.T) {
	lookup := NewFileLookup(tombstoneAgreementReport(time.Now()))

	hotspots := lookup.GetHotspots(10)
	if len(hotspots) != 0 {
		t.Errorf("Expected no hotspots when all beads are tombstoned, got %+v", hotspots)
	}

	stats := lookup.GetStats()
	if stats.TotalFiles != 0 || stats.TotalBeadLinks != 0 || stats.FilesWithMultipleBeads != 0 {
		t.Errorf("Expected empty stats when all beads are tombstoned, got %+v", stats)
	}

	fileResult := lookup.LookupByFile("AGENTS.md")
	if fileResult.TotalBeads != 0 {
		t.Errorf("Expected 0 beads from LookupByFile, got %d", fileResult.TotalBeads)
	}

	impact := lookup.ImpactAnalysis([]string{"AGENTS.md"})
	if len(impact.AffectedBeads) != 0 || impact.RiskLevel != "low" {
		t.Errorf("Expected no affected beads / low risk, got %+v", impact)
	}
}

// TestFileSurfacesAgree_MixedStatuses verifies that hotspot bead counts equal
// the LookupByFile bead counts for the same path when open, closed, and
// tombstoned beads are mixed (with case/whitespace variance in statuses).
func TestFileSurfacesAgree_MixedStatuses(t *testing.T) {
	now := time.Now()
	mkHistory := func(id, status, sha string) BeadHistory {
		return BeadHistory{
			BeadID: id,
			Title:  "Bead " + id,
			Status: status,
			Commits: []CorrelatedCommit{
				{
					SHA:       sha,
					ShortSHA:  sha,
					Timestamp: now,
					Files:     []FileChange{{Path: "pkg/core/engine.go", Insertions: 1}},
				},
			},
		}
	}
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-open":   mkHistory("bv-open", "open", "s1"),
			"bv-closed": mkHistory("bv-closed", "Closed ", "s2"), // case+space variance
			"bv-tomb":   mkHistory("bv-tomb", "tombstone", "s3"),
		},
		CommitIndex: CommitIndex{
			"s1": {"bv-open"},
			"s2": {"bv-closed"},
			"s3": {"bv-tomb"},
		},
	}

	lookup := NewFileLookup(report)

	hotspots := lookup.GetHotspots(10)
	if len(hotspots) != 1 {
		t.Fatalf("Expected 1 hotspot, got %+v", hotspots)
	}
	hs := hotspots[0]
	if hs.TotalBeads != 2 || hs.OpenBeads != 1 || hs.ClosedBeads != 1 {
		t.Errorf("Expected hotspot total=2 open=1 closed=1, got %+v", hs)
	}

	fileResult := lookup.LookupByFile("pkg/core/engine.go")
	if fileResult.TotalBeads != hs.TotalBeads {
		t.Errorf("Hotspots (%d) and LookupByFile (%d) disagree on bead count", hs.TotalBeads, fileResult.TotalBeads)
	}
	if len(fileResult.OpenBeads) != hs.OpenBeads || len(fileResult.ClosedBeads) != hs.ClosedBeads {
		t.Errorf("Open/closed split disagrees: hotspot %+v vs lookup open=%d closed=%d",
			hs, len(fileResult.OpenBeads), len(fileResult.ClosedBeads))
	}

	stats := lookup.GetStats()
	if stats.TotalBeadLinks != 2 {
		t.Errorf("Expected stats.TotalBeadLinks=2 (tombstone excluded), got %d", stats.TotalBeadLinks)
	}
}

// TestGetHotspots_DeterministicTieBreak verifies stable path ordering when
// bead counts tie.
func TestGetHotspots_DeterministicTieBreak(t *testing.T) {
	now := time.Now()
	report := &HistoryReport{
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Status: "open",
				Commits: []CorrelatedCommit{
					{SHA: "a", ShortSHA: "a", Timestamp: now, Files: []FileChange{
						{Path: "zeta.go"}, {Path: "alpha.go"}, {Path: "mid.go"},
					}},
				},
			},
		},
		CommitIndex: CommitIndex{"a": {"bv-1"}},
	}

	lookup := NewFileLookup(report)
	hotspots := lookup.GetHotspots(0)
	if len(hotspots) != 3 {
		t.Fatalf("Expected 3 hotspots, got %d", len(hotspots))
	}
	want := []string{"alpha.go", "mid.go", "zeta.go"}
	for i, w := range want {
		if hotspots[i].FilePath != w {
			t.Errorf("hotspots[%d] = %s, want %s (deterministic tie-break)", i, hotspots[i].FilePath, w)
		}
	}
}
