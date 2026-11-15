package correlation

import (
	"os"
	"os/exec"
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

func TestReverseLookup_LookupByCommitMergeAndBeadsOnly(t *testing.T) {
	repo := t.TempDir()
	revParse := func() string {
		t.Helper()
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git rev-parse HEAD: %v\n%s", err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(`{"id":"bv-1","title":"Auth login","status":"in_progress"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "chore: seed beads only")
	beadsOnlySHA := revParse()

	// Create a feature branch and merge with --no-ff to produce a real merge commit
	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "auth.go"), []byte("package main\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "auth.go")
	runGit(t, repo, "commit", "-m", "fix auth login session")
	orphanCodeSHA := revParse()

	runGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "docs: readme")
	runGit(t, repo, "merge", "--no-ff", "feature", "-m", "Merge branch 'feature'")
	mergeSHA := revParse()

	now := time.Now().UTC()
	report := &HistoryReport{
		CommitIndex: make(CommitIndex),
		Histories: map[string]BeadHistory{
			"bv-1": {
				BeadID:     "bv-1",
				Title:      "Auth login",
				Status:     "in_progress",
				LastAuthor: "Test User",
				Milestones: BeadMilestones{
					Created: &BeadEvent{Timestamp: now.Add(-2 * time.Hour)},
					Claimed: &BeadEvent{Timestamp: now.Add(-1 * time.Hour)},
				},
				Commits: []CorrelatedCommit{
					{
						SHA:       "0000000000000000000000000000000000000001",
						Author:    "Test User",
						Timestamp: now.Add(-30 * time.Minute),
						Files:     []FileChange{{Path: "auth.go"}},
					},
				},
			},
		},
	}

	rl := NewReverseLookupWithRepo(report, repo)

	// 1. .beads/-only commit -> IsOrphan=false, BeadsOnly=true
	boRes, err := rl.LookupByCommit(beadsOnlySHA)
	if err != nil {
		t.Fatalf("LookupByCommit(beadsOnlySHA): %v", err)
	}
	if boRes.IsOrphan || !boRes.BeadsOnly {
		t.Fatalf("expected beads-only commit IsOrphan=false & BeadsOnly=true, got %+v", boRes)
	}

	// 2. Merge commit -> IsOrphan=false (excluded just like FindOrphanCommits --no-merges)
	mergeRes, err := rl.LookupByCommit(mergeSHA)
	if err != nil {
		t.Fatalf("LookupByCommit(mergeSHA): %v", err)
	}
	if mergeRes.IsOrphan {
		t.Fatalf("expected merge commit IsOrphan=false, got %+v", mergeRes)
	}

	// 3. Genuine unlinked code commit -> IsOrphan=true, and AnalyzeCommit rescues bv-1
	orphanRes, err := rl.LookupByCommit(orphanCodeSHA)
	if err != nil {
		t.Fatalf("LookupByCommit(orphanCodeSHA): %v", err)
	}
	if !orphanRes.IsOrphan || orphanRes.BeadsOnly {
		t.Fatalf("expected unlinked code commit IsOrphan=true & BeadsOnly=false, got %+v", orphanRes)
	}
	detector := NewOrphanDetectorAt(report, repo, now)
	cand, beadsOnly, err := detector.AnalyzeCommit(orphanCodeSHA)
	if err != nil {
		t.Fatalf("AnalyzeCommit: %v", err)
	}
	if beadsOnly || cand == nil || len(cand.ProbableBeads) == 0 || cand.ProbableBeads[0].BeadID != "bv-1" {
		t.Fatalf("expected AnalyzeCommit to rescue bv-1, got beadsOnly=%v cand=%+v", beadsOnly, cand)
	}

	// 4. Library-API git option injection must fail closed without touching filesystem
	injectTarget := filepath.Join(t.TempDir(), "should-not-exist.txt")
	injectArg := "--output=" + injectTarget
	if _, err := rl.LookupByCommit(injectArg); err == nil {
		t.Fatalf("LookupByCommit(%q) must reject leading-dash git option injection", injectArg)
	}
	if _, _, err := detector.AnalyzeCommit(injectArg); err == nil {
		t.Fatalf("AnalyzeCommit(%q) must reject leading-dash git option injection", injectArg)
	}
	if _, err := walkCommitsBySHAs(nil, repo, []string{injectArg}); err == nil {
		t.Fatalf("walkCommitsBySHAs(%q) must reject leading-dash git option injection", injectArg)
	}
	co := NewCoCommitExtractor(repo)
	if _, err := co.ExtractCoCommittedFiles(BeadEvent{CommitSHA: injectArg}); err == nil {
		t.Fatalf("CoCommitExtractor(%q) must reject leading-dash git option injection", injectArg)
	}
	if _, statErr := os.Stat(injectTarget); !os.IsNotExist(statErr) {
		t.Fatalf("git option injection created %s!", injectTarget)
	}
}

func TestWave10Round3GitRefValidationIncrementalTargetCommitAndZeroScoreOrphan(t *testing.T) {
	repo := t.TempDir()
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)

	runGit := func(ts time.Time, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Alice",
			"GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=Alice",
			"GIT_COMMITTER_EMAIL=alice@example.com",
			"GIT_AUTHOR_DATE="+ts.Format(time.RFC3339),
			"GIT_COMMITTER_DATE="+ts.Format(time.RFC3339),
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit(now.Add(-3*time.Hour), "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "issues.jsonl"), []byte(`{"id":"bv-1","title":"First task","status":"closed","priority":1,"issue_type":"task","created_at":"2026-11-15T01:00:00Z","updated_at":"2026-11-15T01:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "one.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(now.Add(-3*time.Hour), "add", ".")
	runGit(now.Add(-3*time.Hour), "commit", "-m", "bv-1 initial commit")
	firstSHA := runGit(now.Add(-3*time.Hour), "rev-parse", "HEAD")

	// Second commit touching completely unrelated file by different author outside window -> zero-score orphan
	if err := os.WriteFile(filepath.Join(repo, "unrelated.go"), []byte("package main\nfunc x() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd2 := exec.Command("git", "add", "unrelated.go")
	cmd2.Dir = repo
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, string(out))
	}
	cmd3 := exec.Command("git", "commit", "-m", "completely unrelated note")
	cmd3.Dir = repo
	cmd3.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Zoe",
		"GIT_AUTHOR_EMAIL=zoe@example.com",
		"GIT_COMMITTER_NAME=Zoe",
		"GIT_COMMITTER_EMAIL=zoe@example.com",
		"GIT_AUTHOR_DATE="+now.Format(time.RFC3339),
		"GIT_COMMITTER_DATE="+now.Format(time.RFC3339),
	)
	if out, err := cmd3.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, string(out))
	}
	zeroOrphanSHA := runGit(now, "rev-parse", "HEAD")

	// 1. R3-D1: Revision & batch SHA validation across all Extractor / ExplicitMatcher / Stream / Incremental helpers
	injectTarget := filepath.Join(t.TempDir(), "r3-should-not-exist.txt")
	injectArg := "--output=" + injectTarget
	ext := NewExtractor(repo)
	if _, err := ext.Extract(ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("Extractor.Extract must reject flag-like Revision")
	}
	if _, err := ext.extractViaGitLogPatch(ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("Extractor.extractViaGitLogPatch must reject flag-like Revision")
	}
	if _, err := ext.extractViaSnapshots(ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("Extractor.extractViaSnapshots must reject flag-like Revision")
	}
	if _, err := ext.snapshotCommits(ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("Extractor.snapshotCommits must reject flag-like Revision")
	}
	if _, err := walkCommits(nil, repo, ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("walkCommits must reject flag-like Revision")
	}
	em := NewExplicitMatcherWithKnownIDs(repo, []string{"bv-1"})
	if _, err := em.searchWithGrepForBead("bv-1", "bv-1", ExtractOptions{Revision: injectArg}); err == nil {
		t.Fatalf("searchWithGrepForBead must reject flag-like Revision")
	}
	bfs := NewBatchFileStatsExtractor(repo)
	if _, err := bfs.extractBatchFiles([]string{injectArg}); err == nil {
		t.Fatalf("extractBatchFiles must reject flag-like SHA")
	}
	if _, err := getCommitsSince(nil, repo, injectArg); err == nil {
		t.Fatalf("getCommitsSince must reject flag-like SHA")
	}
	if _, err := countCommitsSince(nil, repo, injectArg); err == nil {
		t.Fatalf("countCommitsSince must reject flag-like SHA")
	}
	if _, err := extractEventsFromCommits(ext, []string{injectArg}, ""); err == nil {
		t.Fatalf("extractEventsFromCommits must reject flag-like SHA")
	}
	if isCommitAncestor(nil, repo, injectArg, "HEAD") || isCommitAncestor(nil, repo, "HEAD", injectArg) {
		t.Fatalf("isCommitAncestor must reject flag-like SHA")
	}
	if _, statErr := os.Stat(injectTarget); !os.IsNotExist(statErr) {
		t.Fatalf("git option injection created %s!", injectTarget)
	}

	// Verify buildGitLogArgs places -G before --end-of-options and Revision
	logArgs := ext.buildGitLogArgs(ExtractOptions{BeadID: "bv-1", Revision: "HEAD"})
	idxG, idxEnd, idxRev, idxDashDash := -1, -1, -1, -1
	for i, a := range logArgs {
		if strings.HasPrefix(a, "-G") {
			idxG = i
		} else if a == "--end-of-options" {
			idxEnd = i
		} else if a == "HEAD" {
			idxRev = i
		} else if a == "--" {
			idxDashDash = i
		}
	}
	if !(idxG > 0 && idxG < idxEnd && idxEnd < idxRev && idxRev < idxDashDash) {
		t.Fatalf("expected -G < --end-of-options < HEAD < -- in buildGitLogArgs, got %v", logArgs)
	}

	// 2. R3-D3: IncrementalCorrelator must not reuse unfiltered Limit:0 report when TargetCommitSHA is set
	beads := []BeadInfo{{ID: "bv-1", Title: "First task", Status: "closed"}}
	inc := NewIncrementalCorrelator(repo, filepath.Join(repo, ".beads", "issues.jsonl"))
	fullRep, err := inc.GenerateReport(beads, CorrelatorOptions{Limit: 0})
	if err != nil {
		t.Fatalf("full GenerateReport: %v", err)
	}
	if len(fullRep.CommitIndex[firstSHA]) == 0 {
		t.Fatalf("expected firstSHA in full report CommitIndex")
	}
	singleRep, err := inc.GenerateReport(beads, CorrelatorOptions{Limit: 0, TargetCommitSHA: zeroOrphanSHA})
	if err != nil {
		t.Fatalf("TargetCommitSHA GenerateReport: %v", err)
	}
	if len(singleRep.CommitIndex[firstSHA]) != 0 {
		t.Fatalf("TargetCommitSHA=%s leaked unrelated commit %s from incremental cache!", zeroOrphanSHA, firstSHA)
	}

	// 3. R3-D4: Zero-score orphan commit returns nil candidate from AnalyzeCommit
	det := NewOrphanDetectorAt(fullRep, repo, now)
	cand, beadsOnly, err := det.AnalyzeCommit(zeroOrphanSHA)
	if err != nil {
		t.Fatalf("AnalyzeCommit(zeroOrphanSHA): %v", err)
	}
	if beadsOnly || cand != nil {
		t.Fatalf("expected zero-score orphan AnalyzeCommit to return cand=nil, got beadsOnly=%v cand=%+v", beadsOnly, cand)
	}
}
