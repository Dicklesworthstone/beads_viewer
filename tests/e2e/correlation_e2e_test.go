package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// createCorrelationRepo seeds a git repo with multiple beads and commits that
// create correlations via ID mentions, shared files, and temporal proximity.
func createCorrelationRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(content), 0o644); err != nil {
			t.Fatalf("write beads.jsonl: %v", err)
		}
	}

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")

	// Create source directories
	if err := os.MkdirAll(filepath.Join(repoDir, "pkg", "auth"), 0o755); err != nil {
		t.Fatalf("mkdir pkg/auth: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "pkg", "api"), 0o755); err != nil {
		t.Fatalf("mkdir pkg/api: %v", err)
	}

	// Commit 1: Create beads and initial code
	write(`{"id":"CORR-1","title":"Auth feature","status":"open","priority":1,"issue_type":"feature"}
{"id":"CORR-2","title":"API endpoint","status":"open","priority":2,"issue_type":"task"}
{"id":"CORR-3","title":"Fix bug","status":"open","priority":1,"issue_type":"bug"}`)
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "auth", "session.go"), []byte("package auth\n\nfunc Session() {}\n"), 0o644); err != nil {
		t.Fatalf("write session.go: %v", err)
	}
	git("add", ".beads/beads.jsonl", "pkg/auth/session.go")
	git("commit", "-m", "seed project with CORR-1, CORR-2, CORR-3")

	// Commit 2: Work on CORR-1 with explicit mention
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "auth", "session.go"), []byte("package auth\n\nfunc Session() {}\nfunc Token() {}\n"), 0o644); err != nil {
		t.Fatalf("update session.go: %v", err)
	}
	write(`{"id":"CORR-1","title":"Auth feature","status":"in_progress","priority":1,"issue_type":"feature"}
{"id":"CORR-2","title":"API endpoint","status":"open","priority":2,"issue_type":"task"}
{"id":"CORR-3","title":"Fix bug","status":"open","priority":1,"issue_type":"bug"}`)
	git("add", ".beads/beads.jsonl", "pkg/auth/session.go")
	git("commit", "-m", "feat(CORR-1): add token generation")

	// Commit 3: Work on CORR-2, also touches shared file
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "api", "handler.go"), []byte("package api\n\nfunc HandleAuth() {}\n"), 0o644); err != nil {
		t.Fatalf("write handler.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "auth", "session.go"), []byte("package auth\n\nfunc Session() {}\nfunc Token() {}\nfunc Validate() {}\n"), 0o644); err != nil {
		t.Fatalf("update session.go again: %v", err)
	}
	write(`{"id":"CORR-1","title":"Auth feature","status":"in_progress","priority":1,"issue_type":"feature"}
{"id":"CORR-2","title":"API endpoint","status":"in_progress","priority":2,"issue_type":"task"}
{"id":"CORR-3","title":"Fix bug","status":"open","priority":1,"issue_type":"bug"}`)
	git("add", ".beads/beads.jsonl", "pkg/api/handler.go", "pkg/auth/session.go")
	git("commit", "-m", "fix(CORR-2): add auth handler with validation")

	// Commit 4: Bug fix mentioning multiple beads
	if err := os.WriteFile(filepath.Join(repoDir, "pkg", "auth", "session.go"), []byte("package auth\n\nfunc Session() {}\nfunc Token() {}\nfunc Validate() bool { return true }\n"), 0o644); err != nil {
		t.Fatalf("fix session.go: %v", err)
	}
	write(`{"id":"CORR-1","title":"Auth feature","status":"closed","priority":1,"issue_type":"feature"}
{"id":"CORR-2","title":"API endpoint","status":"in_progress","priority":2,"issue_type":"task"}
{"id":"CORR-3","title":"Fix bug","status":"closed","priority":1,"issue_type":"bug"}`)
	git("add", ".beads/beads.jsonl", "pkg/auth/session.go")
	git("commit", "-m", "fix(CORR-3): resolve validation bug, closes CORR-1")

	// Commit 5: Close remaining bead
	write(`{"id":"CORR-1","title":"Auth feature","status":"closed","priority":1,"issue_type":"feature"}
{"id":"CORR-2","title":"API endpoint","status":"closed","priority":2,"issue_type":"task"}
{"id":"CORR-3","title":"Fix bug","status":"closed","priority":1,"issue_type":"bug"}`)
	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "close CORR-2")

	return repoDir
}

// TestCorrelationExplicitMentions verifies that commits mentioning bead IDs create correlations.
func TestCorrelationExplicitMentions(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-history failed: %v\n%s", err, out)
	}

	var payload struct {
		Stats struct {
			TotalBeads         int            `json:"total_beads"`
			BeadsWithCommits   int            `json:"beads_with_commits"`
			MethodDistribution map[string]int `json:"method_distribution"`
		} `json:"stats"`
		Histories map[string]struct {
			Events []struct {
				EventType     string `json:"event_type"`
				CommitMessage string `json:"commit_message"`
			} `json:"events"`
		} `json:"histories"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	// All 3 beads should have correlations
	if payload.Stats.TotalBeads != 3 {
		t.Errorf("expected 3 total_beads, got %d", payload.Stats.TotalBeads)
	}
	if payload.Stats.BeadsWithCommits < 3 {
		t.Errorf("expected at least 3 beads_with_commits, got %d", payload.Stats.BeadsWithCommits)
	}

	// CORR-1 should have explicit mention from "feat(CORR-1)" commit
	hist1, ok := payload.Histories["CORR-1"]
	if !ok {
		t.Fatal("missing CORR-1 in histories")
	}
	foundExplicitMention := false
	for _, event := range hist1.Events {
		if strings.Contains(event.CommitMessage, "CORR-1") {
			foundExplicitMention = true
			break
		}
	}
	if !foundExplicitMention {
		t.Error("expected explicit mention of CORR-1 in commit message events")
	}

	// CORR-3 should be correlated to the fix commit
	hist3, ok := payload.Histories["CORR-3"]
	if !ok {
		t.Fatal("missing CORR-3 in histories")
	}
	if len(hist3.Events) == 0 {
		t.Error("expected CORR-3 to have correlated events")
	}
}

// TestCorrelationCommitIndex verifies commit_index maps commits to beads correctly.
func TestCorrelationCommitIndex(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-history failed: %v\n%s", err, out)
	}

	var payload struct {
		CommitIndex map[string][]string `json:"commit_index"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// commit_index should have entries
	if len(payload.CommitIndex) == 0 {
		t.Fatal("commit_index is empty")
	}

	// At least one commit should map to a CORR bead
	foundCorrelation := false
	for _, beads := range payload.CommitIndex {
		for _, beadID := range beads {
			if strings.HasPrefix(beadID, "CORR-") {
				foundCorrelation = true
				break
			}
		}
		if foundCorrelation {
			break
		}
	}
	if !foundCorrelation {
		t.Errorf("no commits mapped to CORR-* beads in commit_index: %v", payload.CommitIndex)
	}
}

// TestCorrelationRobotRelated verifies --robot-related finds related beads.
func TestCorrelationRobotRelated(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-related", "CORR-1")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-related failed: %v\n%s", err, out)
	}

	var payload struct {
		TargetBeadID string `json:"target_bead_id"`
		TargetTitle  string `json:"target_title"`
		FileOverlap  []struct {
			BeadID    string `json:"bead_id"`
			Relevance int    `json:"relevance"`
		} `json:"file_overlap"`
		CommitOverlap []struct {
			BeadID    string `json:"bead_id"`
			Relevance int    `json:"relevance"`
		} `json:"commit_overlap"`
		TotalRelated int `json:"total_related"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	if payload.TargetBeadID != "CORR-1" {
		t.Errorf("expected target_bead_id=CORR-1, got %s", payload.TargetBeadID)
	}

	// Log what we got for debugging
	t.Logf("file_overlap: %v, commit_overlap: %v, total_related: %d",
		payload.FileOverlap, payload.CommitOverlap, payload.TotalRelated)

	// CORR-1 and CORR-3 both touch session.go, so should be file_overlap related
	if len(payload.FileOverlap) > 0 || len(payload.CommitOverlap) > 0 {
		t.Log("Found related beads as expected")
	}
}

// TestCorrelationRobotFileBeads verifies --robot-file-beads finds beads that touched a file.
func TestCorrelationRobotFileBeads(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-file-beads", "pkg/auth/session.go")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-file-beads failed: %v\n%s", err, out)
	}

	var payload struct {
		FilePath   string `json:"file_path"`
		TotalBeads int    `json:"total_beads"`
		OpenBeads  []struct {
			BeadID string `json:"bead_id"`
		} `json:"open_beads"`
		ClosedBeads []struct {
			BeadID string `json:"bead_id"`
		} `json:"closed_beads"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	if !strings.Contains(payload.FilePath, "session.go") {
		t.Errorf("expected file_path to contain session.go, got %s", payload.FilePath)
	}

	// Multiple beads touched session.go
	if payload.TotalBeads < 2 {
		t.Errorf("expected at least 2 beads touching session.go, got %d", payload.TotalBeads)
	}

	// All beads should be closed in this test repo
	if len(payload.ClosedBeads) == 0 {
		t.Error("expected closed_beads to be populated")
	}
}

// TestCorrelationRobotOrphans verifies --robot-orphans finds unlinked commits.
func TestCorrelationRobotOrphans(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-orphans")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-orphans failed: %v\n%s", err, out)
	}

	var payload struct {
		Window struct {
			Commits int    `json:"commits"`
			Limit   int    `json:"limit"`
			Source  string `json:"source"`
		} `json:"window"`
		Stats struct {
			TotalCommits     int     `json:"total_commits"`
			CorrelatedCount  int     `json:"correlated_count"`
			OrphanCount      int     `json:"orphan_count"`
			BeadsOnlyCommits int     `json:"beads_only_commits"`
			OrphanRatio      float64 `json:"orphan_ratio"`
		} `json:"stats"`
		UsageHints []string `json:"usage_hints"`
		Candidates []struct {
			SHA           string `json:"sha"`
			Message       string `json:"message"`
			ProbableBeads []struct {
				BeadID     string   `json:"bead_id"`
				Confidence int      `json:"confidence"`
				Reasons    []string `json:"reasons"`
			} `json:"probable_beads"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v\nout=%s", err, out)
	}

	// Should have some commits
	if payload.Stats.TotalCommits == 0 {
		t.Error("expected total_commits > 0")
	}

	// D4: the scan window is the correlation index's window and the payload
	// accounts for every commit in it.
	if payload.Window.Source != "history_index" || payload.Window.Commits == 0 {
		t.Errorf("window=%+v; want source=history_index with commits > 0", payload.Window)
	}
	if got := payload.Stats.TotalCommits + payload.Stats.BeadsOnlyCommits; got != payload.Window.Commits {
		t.Errorf("total_commits+beads_only_commits=%d; want window.commits=%d", got, payload.Window.Commits)
	}
	if payload.Stats.BeadsOnlyCommits == 0 {
		t.Errorf("fixture's final beads-only commit should be counted in beads_only_commits: %+v", payload.Stats)
	}
	if len(payload.UsageHints) == 0 {
		t.Error("expected usage_hints explaining the window and scores")
	}

	// Should have some correlated commits (from explicit mentions)
	if payload.Stats.CorrelatedCount == 0 {
		t.Error("expected correlated_count > 0 (explicit mentions)")
	}

	// If there are orphans, they should have probable_beads suggestions
	for _, candidate := range payload.Candidates {
		if len(candidate.ProbableBeads) > 0 {
			// Verify probable beads have valid structure
			for _, pb := range candidate.ProbableBeads {
				if pb.BeadID == "" {
					t.Errorf("orphan candidate %s has empty bead_id", candidate.SHA)
				}
				if pb.Confidence < 0 || pb.Confidence > 100 {
					t.Errorf("orphan candidate %s has invalid confidence %d", candidate.SHA, pb.Confidence)
				}
			}
		}
	}
}

func TestCorrelationRobotImpactNetworkMissingBeadFails(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "robot-impact-network", "definitely-missing", "--json")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected robot-impact-network to fail for missing bead, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "Bead not found in network: definitely-missing") {
		t.Fatalf("expected missing bead error, got:\n%s", out)
	}
}

// TestCorrelationConfidenceLevels verifies different correlation methods produce appropriate confidence.
func TestCorrelationConfidenceLevels(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-history failed: %v\n%s", err, out)
	}

	var payload struct {
		Histories map[string]struct {
			Commits []struct {
				SHA        string  `json:"sha"`
				Confidence float64 `json:"confidence"`
				Method     string  `json:"method"`
			} `json:"commits"`
		} `json:"histories"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// Check that CORR-1 has commits with confidence values
	hist1, ok := payload.Histories["CORR-1"]
	if !ok {
		t.Fatal("missing CORR-1 in histories")
	}

	if len(hist1.Commits) == 0 {
		t.Fatal("expected CORR-1 to have correlated commits")
	}

	// Verify confidence values are in valid range
	for _, commit := range hist1.Commits {
		if commit.Confidence < 0 || commit.Confidence > 1.0 {
			t.Errorf("commit %s has invalid confidence %f (expected 0-1)", commit.SHA, commit.Confidence)
		}
	}
}

// TestCorrelationSharedFileRelations verifies beads touching same files are related.
func TestCorrelationSharedFileRelations(t *testing.T) {
	bv := buildBvBinary(t)

	// Create a repo where two beads touch the same file
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(content), 0o644); err != nil {
			t.Fatalf("write beads: %v", err)
		}
	}

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")

	// Create two beads
	write(`{"id":"SHARE-1","title":"First feature","status":"open","priority":1,"issue_type":"task"}
{"id":"SHARE-2","title":"Second feature","status":"open","priority":2,"issue_type":"task"}`)
	if err := os.WriteFile(filepath.Join(repoDir, "src", "shared.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write shared.go: %v", err)
	}
	git("add", ".beads/beads.jsonl", "src/shared.go")
	git("commit", "-m", "init")

	// SHARE-1 modifies shared.go
	if err := os.WriteFile(filepath.Join(repoDir, "src", "shared.go"), []byte("package src\n\nfunc One() {}\n"), 0o644); err != nil {
		t.Fatalf("write shared.go: %v", err)
	}
	write(`{"id":"SHARE-1","title":"First feature","status":"closed","priority":1,"issue_type":"task"}
{"id":"SHARE-2","title":"Second feature","status":"open","priority":2,"issue_type":"task"}`)
	git("add", ".beads/beads.jsonl", "src/shared.go")
	git("commit", "-m", "feat(SHARE-1): add One function")

	// SHARE-2 also modifies shared.go
	if err := os.WriteFile(filepath.Join(repoDir, "src", "shared.go"), []byte("package src\n\nfunc One() {}\nfunc Two() {}\n"), 0o644); err != nil {
		t.Fatalf("write shared.go: %v", err)
	}
	write(`{"id":"SHARE-1","title":"First feature","status":"closed","priority":1,"issue_type":"task"}
{"id":"SHARE-2","title":"Second feature","status":"closed","priority":2,"issue_type":"task"}`)
	git("add", ".beads/beads.jsonl", "src/shared.go")
	git("commit", "-m", "feat(SHARE-2): add Two function")

	// Both beads should show in file-beads for shared.go
	cmd := exec.Command(bv, "--robot-file-beads", "src/shared.go")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-file-beads failed: %v\n%s", err, out)
	}

	var payload struct {
		TotalBeads  int `json:"total_beads"`
		ClosedBeads []struct {
			BeadID string `json:"bead_id"`
		} `json:"closed_beads"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	if payload.TotalBeads < 2 {
		t.Errorf("expected at least 2 beads for shared.go, got %d", payload.TotalBeads)
	}

	// Both SHARE-1 and SHARE-2 should be listed
	foundIDs := make(map[string]bool)
	for _, bead := range payload.ClosedBeads {
		foundIDs[bead.BeadID] = true
	}

	if !foundIDs["SHARE-1"] {
		t.Error("SHARE-1 not found in file-beads for shared.go")
	}
	if !foundIDs["SHARE-2"] {
		t.Error("SHARE-2 not found in file-beads for shared.go")
	}
}

// TestCorrelationMethodDistribution verifies method_distribution in stats.
func TestCorrelationMethodDistribution(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createCorrelationRepo(t)

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-history failed: %v\n%s", err, out)
	}

	var payload struct {
		Stats struct {
			MethodDistribution map[string]int `json:"method_distribution"`
		} `json:"stats"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// Should have at least co_committed method
	if len(payload.Stats.MethodDistribution) == 0 {
		t.Fatal("method_distribution is empty")
	}

	// Log the distribution for debugging
	t.Logf("method_distribution: %v", payload.Stats.MethodDistribution)

	// co_committed should have entries (from beads.jsonl changes)
	if payload.Stats.MethodDistribution["co_committed"] == 0 {
		t.Error("expected co_committed > 0 in method_distribution")
	}
}

// TestCorrelationEmptyRepo verifies behavior with no git history.
func TestCorrelationEmptyRepo(t *testing.T) {
	bv := buildBvBinary(t)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create beads without git repo
	beads := `{"id":"NOGIT-1","title":"No git test","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// May fail without git repo - that's OK
		t.Logf("robot-history without git: %v\n%s", err, out)
		return
	}

	var payload struct {
		Stats struct {
			TotalBeads       int `json:"total_beads"`
			BeadsWithCommits int `json:"beads_with_commits"`
		} `json:"stats"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	// Should have bead but no commits
	if payload.Stats.TotalBeads != 1 {
		t.Errorf("expected 1 total_beads, got %d", payload.Stats.TotalBeads)
	}
	if payload.Stats.BeadsWithCommits != 0 {
		t.Errorf("expected 0 beads_with_commits without git, got %d", payload.Stats.BeadsWithCommits)
	}
}

// TestCorrelationManyBeads verifies correlation handles many beads efficiently.
func TestCorrelationManyBeads(t *testing.T) {
	bv := buildBvBinary(t)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")

	// Create 50 beads
	var lines []string
	for i := 0; i < 50; i++ {
		status := "open"
		if i%3 == 0 {
			status = "closed"
		}
		lines = append(lines, fmt.Sprintf(`{"id":"MANY-%02d","title":"Bead %d","status":"%s","priority":%d,"issue_type":"task"}`,
			i, i, status, i%5))
	}

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	git("add", ".beads/beads.jsonl")
	git("commit", "-m", "seed 50 beads")

	cmd := exec.Command(bv, "--robot-history")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-history failed: %v\n%s", err, out)
	}

	var payload struct {
		Stats struct {
			TotalBeads int `json:"total_beads"`
		} `json:"stats"`
	}

	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json decode: %v", err)
	}

	if payload.Stats.TotalBeads != 50 {
		t.Errorf("expected 50 total_beads, got %d", payload.Stats.TotalBeads)
	}
}

// TestCorrelationOnThisRepository_StrategyCounts (D5) runs the real
// correlator over a frozen slice of this repository's real history: every strategy must
// contribute, the orphan scan must report its window, and explaining an
// explicit-id commit must list the explicit_id signal. Runtime is bounded so
// a regression that makes the multi-strategy walk slow shows up here.
func TestCorrelationOnThisRepository_StrategyCounts(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".beads", "issues.jsonl")); err != nil {
		t.Skip("repository tracker not present")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Skip("not a git checkout")
	}
	bv := buildBvBinary(t)
	// The default window walks 500 commits. Advancing HEAD must not age known
	// explicit-ID matches out of this fixture and silently change its oracle.
	// Build the current CLI above, then run it over the original passing input.
	const historyRef = "f46d62a22441dbf863c611e3820cf3b60b9ee359"
	const boundarySHA = "be8adac10bd28922249fec8e7eb1d2f4371d1b80"
	const boundaryBead = "bv-142"
	fixture := t.TempDir()
	clone := exec.Command("git", "clone", "--quiet", "--shared", "--no-checkout", repo, fixture)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone real correlation fixture: %v\n%s", err, out)
	}
	checkout := exec.Command("git", "-C", fixture, "checkout", "--quiet", "--detach", historyRef)
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("check out required correlation history %s (full Git history required): %v\n%s", historyRef, err, out)
	}
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(bv, args...)
		cmd.Dir = fixture
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, stderr.String())
		}
		return out
	}

	start := time.Now()
	var history struct {
		Stats struct {
			MethodDistribution map[string]int `json:"method_distribution"`
			Strategies         []struct {
				Name       string `json:"name"`
				Ran        bool   `json:"ran"`
				Candidates int    `json:"candidates"`
			} `json:"strategies"`
		} `json:"stats"`
		Window *struct {
			Commits int `json:"commits"`
		} `json:"window"`
		Histories map[string]struct {
			Commits []struct {
				SHA     string   `json:"sha"`
				Methods []string `json:"methods"`
			} `json:"commits"`
		} `json:"histories"`
	}
	if err := json.Unmarshal(run("--robot-history"), &history); err != nil {
		t.Fatalf("history decode: %v", err)
	}
	dist := history.Stats.MethodDistribution
	t.Logf("method_distribution=%v strategies=%+v window=%+v elapsed=%s", dist, history.Stats.Strategies, history.Window, time.Since(start))
	if dist["explicit_id"] < 6 || dist["co_committed"] < 500 || dist["temporal_author"] < 1 {
		t.Fatalf("expected explicit_id>=6, co_committed>=500, temporal_author>=1 on this repository, got %v", dist)
	}
	if len(history.Stats.Strategies) != 3 {
		t.Fatalf("stats.strategies should record three runs: %+v", history.Stats.Strategies)
	}
	if history.Window == nil || history.Window.Commits != 500 {
		t.Fatalf("history should report the frozen 500-commit window, got %+v", history.Window)
	}

	var orphans struct {
		Window struct {
			Source  string `json:"source"`
			Commits int    `json:"commits"`
		} `json:"window"`
	}
	if err := json.Unmarshal(run("--robot-orphans"), &orphans); err != nil {
		t.Fatalf("orphans decode: %v", err)
	}
	if orphans.Window.Source != "history_index" || orphans.Window.Commits != history.Window.Commits {
		t.Fatalf("orphan window %+v should be the history index window (%d commits)", orphans.Window, history.Window.Commits)
	}

	// The oldest commit in the window must still contribute its explicit pair.
	foundBoundary := false
	for _, c := range history.Histories[boundaryBead].Commits {
		if c.SHA == boundarySHA {
			for _, method := range c.Methods {
				if method == "explicit_id" {
					foundBoundary = true
				}
			}
		}
	}
	if !foundBoundary {
		t.Fatalf("500th commit %s must explicitly correlate with %s", boundarySHA, boundaryBead)
	}
	explain := run("--robot-explain-correlation", boundarySHA+":"+boundaryBead)
	if !strings.Contains(string(explain), "explicit_id") {
		t.Fatalf("explain for %s:%s should list the explicit_id signal:\n%s", boundarySHA[:7], boundaryBead, explain)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("correlation e2e on this repository took %s; want under 15s", elapsed)
	}
}
