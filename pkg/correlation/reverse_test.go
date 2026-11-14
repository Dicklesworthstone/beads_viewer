package correlation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createTestReport() *HistoryReport {
	now := time.Now()

	return &HistoryReport{
		GeneratedAt: now,
		CommitIndex: CommitIndex{
			"abc123def456": []string{"bv-1", "bv-2"},
			"def456ghi789": []string{"bv-1"},
			"ghi789abc123": []string{"bv-3"},
		},
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID: "bv-1",
				Title:  "Fix authentication bug",
				Status: "closed",
				Commits: []CorrelatedCommit{
					{
						SHA:         "abc123def456",
						ShortSHA:    "abc123d",
						Message:     "fix: auth bug",
						Author:      "Dev One",
						AuthorEmail: "dev1@test.com",
						Timestamp:   now,
						Method:      MethodCoCommitted,
						Confidence:  0.95,
						Reason:      "Co-committed",
					},
					{
						SHA:         "def456ghi789",
						ShortSHA:    "def456g",
						Message:     "test: add auth tests",
						Author:      "Dev One",
						AuthorEmail: "dev1@test.com",
						Timestamp:   now.Add(-time.Hour),
						Method:      MethodExplicitID,
						Confidence:  0.90,
						Reason:      "Explicit ID",
					},
				},
			},
			"bv-2": {
				BeadID: "bv-2",
				Title:  "Add logging",
				Status: "open",
				Commits: []CorrelatedCommit{
					{
						SHA:         "abc123def456",
						ShortSHA:    "abc123d",
						Message:     "fix: auth bug",
						Author:      "Dev One",
						AuthorEmail: "dev1@test.com",
						Timestamp:   now,
						Method:      MethodTemporalAuthor,
						Confidence:  0.60,
						Reason:      "Temporal",
					},
				},
			},
			"bv-3": {
				BeadID: "bv-3",
				Title:  "Refactor database",
				Status: "in_progress",
				Commits: []CorrelatedCommit{
					{
						SHA:         "ghi789abc123",
						ShortSHA:    "ghi789a",
						Message:     "refactor: db layer",
						Author:      "Dev Two",
						AuthorEmail: "dev2@test.com",
						Timestamp:   now.Add(-2 * time.Hour),
						Method:      MethodCoCommitted,
						Confidence:  0.92,
						Reason:      "Co-committed",
					},
				},
			},
		},
	}
}

func TestNewReverseLookup(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	if rl == nil {
		t.Fatal("NewReverseLookup returned nil")
	}

	if len(rl.index) != 3 {
		t.Errorf("index has %d entries, want 3", len(rl.index))
	}

	if len(rl.beads) != 3 {
		t.Errorf("beads has %d entries, want 3", len(rl.beads))
	}
}

func TestNewReverseLookup_NilReport(t *testing.T) {
	rl := NewReverseLookup(nil)
	if rl == nil {
		t.Fatal("NewReverseLookup returned nil")
	}
	if len(rl.index) != 0 {
		t.Errorf("index has %d entries, want 0", len(rl.index))
	}
	if len(rl.beads) != 0 {
		t.Errorf("beads has %d entries, want 0", len(rl.beads))
	}

	result, err := rl.LookupByCommit("abc123")
	if err != nil {
		t.Fatalf("LookupByCommit on empty lookup failed: %v", err)
	}
	if !result.IsOrphan {
		t.Fatal("empty lookup should mark unknown commit as orphan")
	}
}

func TestLookupByCommit_Found(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit("abc123def456")
	if err != nil {
		t.Fatalf("LookupByCommit failed: %v", err)
	}

	if result.IsOrphan {
		t.Error("Should not be orphan")
	}

	if len(result.RelatedBeads) != 2 {
		t.Errorf("RelatedBeads has %d entries, want 2", len(result.RelatedBeads))
	}

	// Check that both beads are found
	beadIDs := make(map[string]bool)
	for _, rb := range result.RelatedBeads {
		beadIDs[rb.BeadID] = true
	}

	if !beadIDs["bv-1"] || !beadIDs["bv-2"] {
		t.Errorf("Expected bv-1 and bv-2, got %v", beadIDs)
	}
}

func TestLookupByCommit_ShortSHA(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	// Use short SHA prefix
	result, err := rl.LookupByCommit("abc123")
	if err != nil {
		t.Fatalf("LookupByCommit with short SHA failed: %v", err)
	}

	if result.IsOrphan {
		t.Error("Should not be orphan")
	}

	if len(result.RelatedBeads) != 2 {
		t.Errorf("RelatedBeads has %d entries, want 2", len(result.RelatedBeads))
	}
}

func TestLookupByCommit_UppercaseShortSHA(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit("ABC123")
	if err != nil {
		t.Fatalf("LookupByCommit with uppercase short SHA failed: %v", err)
	}
	if result.CommitSHA != "abc123def456" {
		t.Fatalf("CommitSHA = %q, want abc123def456", result.CommitSHA)
	}
	if result.IsOrphan {
		t.Fatal("uppercase short SHA should resolve to indexed commit")
	}
}

func TestLookupByCommit_EmptySHARejected(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit(" \t ")
	if err == nil {
		t.Fatal("expected empty SHA to be rejected")
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil on validation error", result)
	}
	if !strings.Contains(err.Error(), "commit SHA is required") {
		t.Fatalf("error = %q, want missing SHA message", err.Error())
	}
}

func TestLookupByCommit_AmbiguousShortSHARejected(t *testing.T) {
	now := time.Now()
	report := createTestReport()
	report.CommitIndex["abc123zzz999"] = []string{"bv-ambiguous"}
	report.Histories["bv-ambiguous"] = BeadHistory{
		BeadID: "bv-ambiguous",
		Title:  "Ambiguous short SHA",
		Status: "closed",
		Commits: []CorrelatedCommit{
			{
				SHA:         "abc123zzz999",
				ShortSHA:    "abc123z",
				Message:     "fix: ambiguous prefix",
				Author:      "Dev Three",
				AuthorEmail: "dev3@test.com",
				Timestamp:   now,
				Method:      MethodExplicitID,
				Confidence:  0.88,
				Reason:      "Explicit ID",
			},
		},
	}
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit("abc123")
	if err == nil {
		t.Fatal("expected ambiguous short SHA to be rejected")
	}
	if result != nil {
		t.Fatalf("result = %#v, want nil on ambiguity", result)
	}
	if !strings.Contains(err.Error(), "ambiguous commit SHA prefix") {
		t.Fatalf("error = %q, want ambiguity message", err.Error())
	}
}

func TestLookupByCommit_NotFound(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit("notfound123")
	if err != nil {
		t.Fatalf("LookupByCommit failed: %v", err)
	}

	if !result.IsOrphan {
		t.Error("Should be orphan")
	}

	if len(result.RelatedBeads) != 0 {
		t.Errorf("RelatedBeads should be empty, got %d", len(result.RelatedBeads))
	}
}

func TestLookupByCommit_IncludesDetails(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, err := rl.LookupByCommit("abc123def456")
	if err != nil {
		t.Fatalf("LookupByCommit failed: %v", err)
	}

	// Should have commit message from details
	if result.Message != "fix: auth bug" {
		t.Errorf("Message = %q, want %q", result.Message, "fix: auth bug")
	}

	if result.Author != "Dev One" {
		t.Errorf("Author = %q, want %q", result.Author, "Dev One")
	}

	// Check related bead details
	for _, rb := range result.RelatedBeads {
		if rb.BeadID == "bv-1" {
			if rb.BeadTitle != "Fix authentication bug" {
				t.Errorf("BeadTitle = %q, want %q", rb.BeadTitle, "Fix authentication bug")
			}
			if rb.Method != MethodCoCommitted {
				t.Errorf("Method = %v, want %v", rb.Method, MethodCoCommitted)
			}
			if rb.Confidence != 0.95 {
				t.Errorf("Confidence = %v, want 0.95", rb.Confidence)
			}
		}
	}
}

func TestGetCorrelatedCommitCount(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	count := rl.GetCorrelatedCommitCount()
	if count != 3 {
		t.Errorf("GetCorrelatedCommitCount() = %d, want 3", count)
	}
}

func TestGetAllBeadIDs(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	ids := rl.GetAllBeadIDs()
	if len(ids) != 3 {
		t.Errorf("GetAllBeadIDs() returned %d IDs, want 3", len(ids))
	}

	// Check all expected IDs are present
	idSet := make(map[string]bool)
	for _, id := range ids {
		idSet[id] = true
	}

	for _, want := range []string{"bv-1", "bv-2", "bv-3"} {
		if !idSet[want] {
			t.Errorf("GetAllBeadIDs() missing %q", want)
		}
	}
}

func TestGetBeadCommitSummaries(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	summaries := rl.GetBeadCommitSummaries()
	if len(summaries) != 3 {
		t.Errorf("GetBeadCommitSummaries() returned %d summaries, want 3", len(summaries))
	}

	// Find bv-1 summary
	var bv1Summary *BeadCommitsSummary
	for i := range summaries {
		if summaries[i].BeadID == "bv-1" {
			bv1Summary = &summaries[i]
			break
		}
	}

	if bv1Summary == nil {
		t.Fatal("bv-1 summary not found")
	}

	if bv1Summary.CommitCount != 2 {
		t.Errorf("bv-1 CommitCount = %d, want 2", bv1Summary.CommitCount)
	}

	// Average confidence should be (0.95 + 0.90) / 2 = 0.925
	expectedAvg := 0.925
	if bv1Summary.AvgConfid != expectedAvg {
		t.Errorf("bv-1 AvgConfid = %v, want %v", bv1Summary.AvgConfid, expectedAvg)
	}
}

func TestNormalizeSHA(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	tests := []struct {
		input string
		want  string
	}{
		{"abc123def456", "abc123def456"}, // Full SHA in index
		{"abc123", "abc123def456"},       // Short prefix expands
		{"def456", "def456ghi789"},       // Another short prefix
		{"  def456  ", "def456ghi789"},   // Surrounding whitespace is ignored
		{"", ""},                         // Empty input cannot be expanded
		{"unknown", "unknown"},           // Unknown stays as-is
	}

	for _, tt := range tests {
		got := rl.normalizeSHA(tt.input)
		if got != tt.want {
			t.Errorf("normalizeSHA(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRelatedBead_AllFields(t *testing.T) {
	report := createTestReport()
	rl := NewReverseLookup(report)

	result, _ := rl.LookupByCommit("ghi789abc123")

	if len(result.RelatedBeads) != 1 {
		t.Fatalf("Expected 1 related bead, got %d", len(result.RelatedBeads))
	}

	rb := result.RelatedBeads[0]

	if rb.BeadID != "bv-3" {
		t.Errorf("BeadID = %q, want bv-3", rb.BeadID)
	}

	if rb.BeadTitle != "Refactor database" {
		t.Errorf("BeadTitle = %q, want 'Refactor database'", rb.BeadTitle)
	}

	if rb.BeadStatus != "in_progress" {
		t.Errorf("BeadStatus = %q, want 'in_progress'", rb.BeadStatus)
	}

	if rb.Method != MethodCoCommitted {
		t.Errorf("Method = %v, want MethodCoCommitted", rb.Method)
	}

	if rb.Confidence != 0.92 {
		t.Errorf("Confidence = %v, want 0.92", rb.Confidence)
	}

	if rb.Reason != "Co-committed" {
		t.Errorf("Reason = %q, want 'Co-committed'", rb.Reason)
	}
}

func TestOrphanStats_Empty(t *testing.T) {
	stats := &OrphanStats{
		TotalCommits:   0,
		OrphanCommits:  0,
		CorrelatedCmts: 0,
		OrphanRatio:    0,
	}

	if stats.OrphanRatio != 0 {
		t.Errorf("OrphanRatio should be 0 for empty stats")
	}
}

func TestCommitBeadResult_EmptyRelatedBeads(t *testing.T) {
	result := &CommitBeadResult{
		CommitSHA:    "test123",
		RelatedBeads: []RelatedBead{},
		IsOrphan:     true,
	}

	if len(result.RelatedBeads) != 0 {
		t.Error("RelatedBeads should be empty")
	}

	if !result.IsOrphan {
		t.Error("Should be marked as orphan")
	}
}

func TestReverseLookup_WithSubtreeScopeIgnoresTrackerPathsOnCodeCommitsAndNormalizesPaths(t *testing.T) {
	mono := t.TempDir()
	runGit(t, mono, "init")
	runGit(t, mono, "config", "user.email", "test@example.com")
	runGit(t, mono, "config", "user.name", "Test User")
	for _, d := range []string{
		filepath.Join(mono, ".beads"),
		filepath.Join(mono, "services", "api", ".beads"),
		filepath.Join(mono, "services", "web", ".beads"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(mono, ".beads", "sprints.jsonl"), []byte(`{"id":"s1","name":"Root sprint"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mono, "services", "api", ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"API task","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mono, "services", "web", ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"Web task","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mono, "add", ".")
	runGit(t, mono, "commit", "-m", "chore: seed trackers only")

	// Commit 2: root tracker + web code file -> must belong ONLY to web, NEVER to root (excludeSubtrees).
	if err := os.WriteFile(filepath.Join(mono, ".beads", "sprints.jsonl"), []byte(`{"id":"s1","name":"Root sprint v2"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mono, "services", "web", "app.ts"), []byte("export const a = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mono, "add", ".beads/sprints.jsonl", "services/web/app.ts")
	runGit(t, mono, "commit", "-m", "fix: web code with root sprint update")

	// Commit 3: api tracker + web code file -> must belong ONLY to web, NEVER to api (includeSubtrees).
	if err := os.WriteFile(filepath.Join(mono, "services", "api", ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"API task updated","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mono, "services", "web", "app.ts"), []byte("export const a = 2;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mono, "add", "services/api/.beads/issues.jsonl", "services/web/app.ts")
	runGit(t, mono, "commit", "-m", "fix: web code with api tracker update")

	// Commit 4: pure api tracker commit -> attributed to api's BeadsOnlyCommits (and NOT root).
	if err := os.WriteFile(filepath.Join(mono, "services", "api", ".beads", "issues.jsonl"), []byte(`{"id":"1","title":"API task v3","status":"open"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, mono, "add", "services/api/.beads/issues.jsonl")
	runGit(t, mono, "commit", "-m", "chore: pure api tracker update")

	emptyReport := &HistoryReport{
		CommitIndex: make(CommitIndex),
		Histories:   make(map[string]BeadHistory),
	}

	// 1. Root umbrella scope (excludeSubtrees) with uncleaned paths ("./services//api/", "services/web/sub/..").
	rlRoot := NewReverseLookupWithRepo(emptyReport, mono).
		WithStoreDirs(".beads", "services/api/.beads", "services/web/.beads").
		WithSubtreeScope(nil, []string{"./services//api/", "services/web/sub/..", "..", "../outside", "/outside", "."})
	rootOrphans, rootStats, err := rlRoot.FindOrphanCommits(ExtractOptions{Limit: 20})
	if err != nil {
		t.Fatalf("root FindOrphanCommits: %v", err)
	}
	if len(rootOrphans) != 0 || rootStats.TotalCommits != 0 || rootStats.OrphanCommits != 0 {
		t.Fatalf("root excludeSubtrees contaminated by web code commit touching .beads/sprints.jsonl: orphans=%+v stats=%+v", rootOrphans, rootStats)
	}
	if rootStats.BeadsOnlyCommits != 1 {
		t.Fatalf("root BeadsOnlyCommits = %d, want 1 (seed commit touching .beads/)", rootStats.BeadsOnlyCommits)
	}

	// 2. API scope (includeSubtrees) with uncleaned path "./services//api" + traversal rejection.
	rlAPI := NewReverseLookupWithRepo(emptyReport, mono).
		WithStoreDirs(".beads", "services/api/.beads", "services/web/.beads").
		WithSubtreeScope([]string{"./services//api", "../outside"}, nil)
	apiOrphans, apiStats, err := rlAPI.FindOrphanCommits(ExtractOptions{Limit: 20})
	if err != nil {
		t.Fatalf("api FindOrphanCommits: %v", err)
	}
	if len(apiOrphans) != 0 || apiStats.TotalCommits != 0 || apiStats.OrphanCommits != 0 {
		t.Fatalf("api includeSubtrees leaked web code commit touching services/api/.beads: orphans=%+v stats=%+v", apiOrphans, apiStats)
	}
	if apiStats.BeadsOnlyCommits != 2 {
		t.Fatalf("api BeadsOnlyCommits = %d, want 2 (seed commit + pure api tracker commit)", apiStats.BeadsOnlyCommits)
	}

	// 3. Web scope (includeSubtrees) sees both web orphan commits cleanly.
	rlWeb := NewReverseLookupWithRepo(emptyReport, mono).
		WithStoreDirs(".beads", "services/api/.beads", "services/web/.beads").
		WithSubtreeScope([]string{"./services/web/"}, nil)
	webOrphans, webStats, err := rlWeb.FindOrphanCommits(ExtractOptions{Limit: 20})
	if err != nil {
		t.Fatalf("web FindOrphanCommits: %v", err)
	}
	if len(webOrphans) != 2 || webStats.TotalCommits != 2 || webStats.OrphanCommits != 2 {
		t.Fatalf("web includeSubtrees expected 2 orphan commits, got orphans=%+v stats=%+v", webOrphans, webStats)
	}
}
