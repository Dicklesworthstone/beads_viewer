package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

var (
	reGeneratedAt = regexp.MustCompile(`"generated_at"\s*:\s*"[^"]+"`)
	reComputedAt  = regexp.MustCompile(`"computed_at"\s*:\s*"[^"]+"`)
)

func normalizeRobotTimestamps(raw []byte) string {
	s := reGeneratedAt.ReplaceAllString(string(raw), `"generated_at":"<PINNED>"`)
	s = reComputedAt.ReplaceAllString(s, `"computed_at":"<PINNED>"`)
	return s
}

func initMetamorphicGitRepo(t testing.TB, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "metamorphic@example.com"},
		{"config", "user.name", "Metamorphic Harness"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

func commitMetamorphicAll(t testing.TB, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", msg},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

func runMetamorphicBV(t testing.TB, bv, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bv, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"BV_NO_BROWSER=1",
		"BV_TEST_MODE=1",
		"BV_NO_SAVED_CONFIG=1",
		"SOURCE_DATE_EPOCH=1788264000",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bv %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return out
}

// TestMetamorphicRobot_MR2_ScopeAndNonPromotion exercises MR-2 end-to-end across
// --robot-triage, --robot-priority, --robot-plan, --robot-search, and --robot-diff
// under --label, --repo, and --recipe scoping.
func TestMetamorphicRobot_MR2_ScopeAndNonPromotion(t *testing.T) {
	bv := buildBvBinary(t)

	// Initialize one base git repository with a seed commit so --diff-since=HEAD
	// observes non-empty working-tree additions on every rapid check.
	dir := t.TempDir()
	initMetamorphicGitRepo(t, dir)
	writeBeads(t, dir, `{"id":"SEED-0","title":"Committed baseline","status":"closed","priority":3,"issue_type":"task","labels":["infra"],"source_repo":"infra/core","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-02T00:00:00Z","closed_at":"2026-07-02T00:00:00Z"}`+"\n")
	commitMetamorphicAll(t, dir, "seed baseline")

	// Run bounded rapid checks at the CLI boundary across --label, --repo, and --recipe.
	rapid.Check(t, func(rt *rapid.T) {
		// Construct randomized scopes & priorities around a cross-scope blocking edge:
		// EXT-BLK (frontend, apps/frontend, open) blocks IN-LOCKED (backend, services/backend, open),
		// while IN-READY (backend, services/backend, open) is unblocked.
		pReady := rapid.IntRange(0, 3).Draw(rt, "pReady")
		pLocked := rapid.IntRange(0, 2).Draw(rt, "pLocked")
		scopeKind := rapid.SampledFrom([]string{"label", "repo", "recipe"}).Draw(rt, "scopeKind")
		recipeName := rapid.SampledFrom([]string{"actionable", "high-impact"}).Draw(rt, "recipeName")

		var scopeFlag string
		excludesExt := true
		switch scopeKind {
		case "label":
			scopeFlag = "--label=backend"
		case "repo":
			scopeFlag = "--repo=backend"
		case "recipe":
			scopeFlag = "--recipe=" + recipeName
			excludesExt = false // EXT-BLK is open/ready so global recipes may include EXT-BLK, never blocked IN-LOCKED.
		}

		jsonl := strings.Join([]string{
			`{"id":"SEED-0","title":"Committed baseline","status":"closed","priority":3,"issue_type":"task","labels":["infra"],"source_repo":"infra/core","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-02T00:00:00Z","closed_at":"2026-07-02T00:00:00Z"}`,
			`{"id":"EXT-BLK","title":"Frontend auth gateway","status":"open","priority":0,"issue_type":"task","labels":["frontend"],"source_repo":"frontend/apps","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-20T00:00:00Z"}`,
			fmt.Sprintf(`{"id":"IN-LOCKED","title":"Backend auth consumer","status":"open","priority":%d,"issue_type":"task","labels":["backend"],"source_repo":"backend/services","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-21T00:00:00Z","dependencies":[{"issue_id":"IN-LOCKED","depends_on_id":"EXT-BLK","type":"blocks"}]}`, pLocked),
			fmt.Sprintf(`{"id":"IN-READY","title":"Backend auth cache","status":"open","priority":%d,"issue_type":"task","labels":["backend"],"source_repo":"backend/services","created_at":"2026-08-03T00:00:00Z","updated_at":"2026-08-22T00:00:00Z"}`, pReady),
		}, "\n") + "\n"
		writeBeads(t, dir, jsonl)

		// 1. --robot-triage: IN-LOCKED (blocked by EXT-BLK) is NEVER promoted; EXT-BLK excluded when out-of-scope.
		var triageResp struct {
			Triage struct {
				Recommendations []struct {
					ID string `json:"id"`
				} `json:"recommendations"`
			} `json:"triage"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-triage", scopeFlag), &triageResp); err != nil {
			rt.Fatalf("unmarshal triage (%s): %v", scopeFlag, err)
		}
		for _, rec := range triageResp.Triage.Recommendations {
			if rec.ID == "IN-LOCKED" || (excludesExt && rec.ID == "EXT-BLK") {
				rt.Fatalf("MR-2 violation in --robot-triage %s: unexpected %s in %+v", scopeFlag, rec.ID, triageResp.Triage.Recommendations)
			}
		}

		// 2. --robot-priority: zero blocked IDs ("IN-LOCKED") and zero out-of-scope IDs ("EXT-BLK" when scoped by label/repo).
		var prioResp struct {
			Recommendations []struct {
				IssueID string `json:"issue_id"`
			} `json:"recommendations"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-priority", scopeFlag), &prioResp); err != nil {
			rt.Fatalf("unmarshal priority (%s): %v", scopeFlag, err)
		}
		for _, rec := range prioResp.Recommendations {
			if rec.IssueID == "IN-LOCKED" || (excludesExt && rec.IssueID == "EXT-BLK") {
				rt.Fatalf("MR-2 violation in --robot-priority %s: non-ready or out-of-scope %s leaked", scopeFlag, rec.IssueID)
			}
		}

		// 3. --robot-plan: IN-LOCKED must NEVER appear as actionable in tracks; EXT-BLK excluded when out-of-scope.
		var planResp struct {
			Plan struct {
				Tracks []struct {
					Items []struct {
						ID string `json:"id"`
					} `json:"items"`
				} `json:"tracks"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-plan", scopeFlag), &planResp); err != nil {
			rt.Fatalf("unmarshal plan (%s): %v", scopeFlag, err)
		}
		for _, tr := range planResp.Plan.Tracks {
			for _, it := range tr.Items {
				if it.ID == "IN-LOCKED" || (excludesExt && it.ID == "EXT-BLK") {
					rt.Fatalf("MR-2 violation in --robot-plan %s: non-ready/out-of-scope %s in track", scopeFlag, it.ID)
				}
			}
		}

		// 4. --robot-search --search="auth": out-of-scope EXT-BLK excluded under label/repo; IN-LOCKED excluded under --recipe=actionable.
		var searchResp struct {
			Results []struct {
				IssueID string `json:"issue_id"`
			} `json:"results"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-search", "--search=auth", scopeFlag), &searchResp); err != nil {
			rt.Fatalf("unmarshal search (%s): %v", scopeFlag, err)
		}
		for _, r := range searchResp.Results {
			if excludesExt && r.IssueID == "EXT-BLK" {
				rt.Fatalf("MR-2 violation in --robot-search %s: out-of-scope %s leaked", scopeFlag, r.IssueID)
			}
			if scopeFlag == "--recipe=actionable" && r.IssueID == "IN-LOCKED" {
				rt.Fatalf("MR-2 violation in --robot-search --recipe=actionable: blocked %s leaked", r.IssueID)
			}
		}

		// 5. --robot-diff --diff-since=HEAD: non-empty diff against SEED-0 with zero out-of-scope leaks.
		var diffResp struct {
			Diff struct {
				NewIssues []struct {
					ID string `json:"id"`
				} `json:"new_issues"`
			} `json:"diff"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-diff", "--diff-since=HEAD", scopeFlag), &diffResp); err != nil {
			rt.Fatalf("unmarshal diff (%s): %v", scopeFlag, err)
		}
		if len(diffResp.Diff.NewIssues) == 0 {
			rt.Fatalf("expected non-empty --robot-diff new_issues under %s", scopeFlag)
		}
		for _, ni := range diffResp.Diff.NewIssues {
			if excludesExt && ni.ID == "EXT-BLK" {
				rt.Fatalf("MR-2 violation in --robot-diff %s: out-of-scope %s leaked", scopeFlag, ni.ID)
			}
			if scopeFlag == "--recipe=actionable" && ni.ID == "IN-LOCKED" {
				rt.Fatalf("MR-2 violation in --robot-diff --recipe=actionable: blocked %s leaked", ni.ID)
			}
		}
	})
}

// TestMetamorphicRobot_MR4_StatusConservationAcrossTriagePlanBaselineDrift verifies
// exact non-tombstone status-partition conservation and readiness_blocked_count agreement
// across --robot-triage, --robot-plan, --save-baseline, and --check-drift --robot-drift
// over randomized status distributions via rapid.Check.
func TestMetamorphicRobot_MR4_StatusConservationAcrossTriagePlanBaselineDrift(t *testing.T) {
	bv := buildBvBinary(t)
	baseDir := t.TempDir()
	caseSeq := 0

	rapid.Check(t, func(rt *rapid.T) {
		caseSeq++
		dir := filepath.Join(baseDir, fmt.Sprintf("mr4-%d", caseSeq))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		nOpen := rapid.IntRange(1, 3).Draw(rt, "nOpen")
		nInProg := rapid.IntRange(0, 2).Draw(rt, "nInProg")
		nDepBlk := rapid.IntRange(0, 2).Draw(rt, "nDepBlk")
		nLitBlk := rapid.IntRange(0, 2).Draw(rt, "nLitBlk")
		nParked := rapid.IntRange(0, 5).Draw(rt, "nParked")
		nClosed := rapid.IntRange(0, 2).Draw(rt, "nClosed")
		nTomb := rapid.IntRange(0, 2).Draw(rt, "nTomb")

		parkedStatuses := []string{"deferred", "pinned", "draft", "hooked", "review"}
		var lines []string
		for i := 0; i < nOpen; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-OPEN-%d","title":"Ready open %d","status":"open","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`, i, i))
		}
		for i := 0; i < nInProg; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-INPROG-%d","title":"Ready in_progress %d","status":"in_progress","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`, i, i))
		}
		for i := 0; i < nDepBlk; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-DEPBLK-%d","title":"Dependency-blocked open %d","status":"open","priority":2,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z","dependencies":[{"issue_id":"S-DEPBLK-%d","depends_on_id":"S-OPEN-0","type":"blocks"}]}`, i, i, i))
		}
		for i := 0; i < nLitBlk; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-LITBLK-%d","title":"Literal blocked %d","status":"blocked","priority":2,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`, i, i))
		}
		for i := 0; i < nParked; i++ {
			st := parkedStatuses[i%len(parkedStatuses)]
			lines = append(lines, fmt.Sprintf(`{"id":"S-PARK-%d","title":"Parked %s %d","status":%q,"priority":3,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`, i, st, i, st))
		}
		for i := 0; i < nClosed; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-CLOSED-%d","title":"Closed %d","status":"closed","priority":2,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z","closed_at":"2026-08-25T00:00:00Z"}`, i, i))
		}
		for i := 0; i < nTomb; i++ {
			lines = append(lines, fmt.Sprintf(`{"id":"S-TOMB-%d","title":"Tombstone %d","status":"tombstone","priority":4,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-25T00:00:00Z"}`, i, i))
		}
		writeBeads(t, dir, strings.Join(lines, "\n")+"\n")

		var triageResp struct {
			Triage struct {
				QuickRef struct {
					OpenCount             int `json:"open_count"`
					ActionableCount       int `json:"actionable_count"`
					BlockedCount          int `json:"blocked_count"`
					ReadinessBlockedCount int `json:"readiness_blocked_count"`
					DeferredOrParkedCount int `json:"deferred_or_parked_count"`
					InProgressCount       int `json:"in_progress_count"`
					NotClosedCount        int `json:"not_closed_count"`
					NotActionableCount    int `json:"not_actionable_count"`
				} `json:"quick_ref"`
				ProjectHealth struct {
					Counts struct {
						Total             int `json:"total"`
						Closed            int `json:"closed"`
						Actionable        int `json:"actionable"`
						Blocked           int `json:"blocked"`
						NotClosed         int `json:"not_closed"`
						DependencyBlocked int `json:"dependency_blocked"`
					} `json:"counts"`
				} `json:"project_health"`
			} `json:"triage"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-triage"), &triageResp); err != nil {
			rt.Fatalf("unmarshal triage: %v", err)
		}

		var planResp struct {
			Plan struct {
				TotalActionable       int `json:"total_actionable"`
				TotalBlocked          int `json:"total_blocked"`
				TotalDeferredOrParked int `json:"total_deferred_or_parked"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-plan"), &planResp); err != nil {
			rt.Fatalf("unmarshal plan: %v", err)
		}

		runMetamorphicBV(t, bv, dir, "--save-baseline", "mr4 check")
		baselineRaw, err := os.ReadFile(filepath.Join(dir, ".bv", "baseline.json"))
		if err != nil {
			rt.Fatalf("read baseline.json: %v", err)
		}
		var savedBL struct {
			Stats struct {
				NodeCount       int `json:"node_count"`
				ClosedCount     int `json:"closed_count"`
				BlockedCount    int `json:"blocked_count"`
				ActionableCount int `json:"actionable_count"`
			} `json:"stats"`
		}
		if err := json.Unmarshal(baselineRaw, &savedBL); err != nil {
			rt.Fatalf("unmarshal baseline.json: %v", err)
		}

		wantActionable := nOpen + nInProg
		wantReadinessBlocked := nDepBlk + nLitBlk
		wantStrictBlocked := nLitBlk
		wantDeferred := nParked
		wantNotActionable := wantReadinessBlocked + wantDeferred
		wantTotal := wantActionable + wantNotActionable + nClosed

		if triageResp.Triage.ProjectHealth.Counts.Total != wantTotal || savedBL.Stats.NodeCount != wantTotal {
			rt.Fatalf("non-tombstone total mismatch: triage=%d baseline=%d, want %d",
				triageResp.Triage.ProjectHealth.Counts.Total, savedBL.Stats.NodeCount, wantTotal)
		}
		if triageResp.Triage.QuickRef.BlockedCount != wantStrictBlocked ||
			triageResp.Triage.ProjectHealth.Counts.Blocked != wantStrictBlocked {
			rt.Fatalf("MR-4 strict status=blocked drift: triageQR=%d health=%d (want %d)",
				triageResp.Triage.QuickRef.BlockedCount,
				triageResp.Triage.ProjectHealth.Counts.Blocked,
				wantStrictBlocked)
		}
		if triageResp.Triage.QuickRef.ReadinessBlockedCount != wantReadinessBlocked ||
			planResp.Plan.TotalBlocked != wantReadinessBlocked ||
			savedBL.Stats.BlockedCount != wantReadinessBlocked {
			rt.Fatalf("MR-4 readiness_blocked_count drift: triageQR=%d plan=%d baseline=%d (want %d)",
				triageResp.Triage.QuickRef.ReadinessBlockedCount,
				planResp.Plan.TotalBlocked,
				savedBL.Stats.BlockedCount,
				wantReadinessBlocked)
		}
		if triageResp.Triage.QuickRef.DeferredOrParkedCount != wantDeferred ||
			planResp.Plan.TotalDeferredOrParked != wantDeferred {
			rt.Fatalf("MR-4 deferred_or_parked_count drift: triageQR=%d plan=%d (want %d)",
				triageResp.Triage.QuickRef.DeferredOrParkedCount,
				planResp.Plan.TotalDeferredOrParked,
				wantDeferred)
		}
		if triageResp.Triage.QuickRef.NotActionableCount != wantNotActionable ||
			triageResp.Triage.ProjectHealth.Counts.DependencyBlocked != wantNotActionable ||
			planResp.Plan.TotalBlocked+planResp.Plan.TotalDeferredOrParked != wantNotActionable {
			rt.Fatalf("MR-4 non-actionable complement drift: triageQR=%d health=%d plan(blocked+deferred)=%d (want %d)",
				triageResp.Triage.QuickRef.NotActionableCount,
				triageResp.Triage.ProjectHealth.Counts.DependencyBlocked,
				planResp.Plan.TotalBlocked+planResp.Plan.TotalDeferredOrParked,
				wantNotActionable)
		}
		if planResp.Plan.TotalActionable+planResp.Plan.TotalBlocked+planResp.Plan.TotalDeferredOrParked+savedBL.Stats.ClosedCount != wantTotal {
			rt.Fatalf("MR-4 conservation law failed: actionable=%d + blocked=%d + deferred=%d + closed=%d != %d",
				planResp.Plan.TotalActionable, planResp.Plan.TotalBlocked, planResp.Plan.TotalDeferredOrParked, savedBL.Stats.ClosedCount, wantTotal)
		}

		// --check-drift --robot-drift against identical state must see zero blocked_increase drift!
		driftCmd := exec.Command(bv, "--check-drift", "--robot-drift")
		driftCmd.Dir = dir
		driftCmd.Env = append(os.Environ(), "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_SAVED_CONFIG=1", "SOURCE_DATE_EPOCH=1788264000")
		driftOut, _ := driftCmd.CombinedOutput()
		if strings.Contains(string(driftOut), `"type": "blocked_increase"`) || strings.Contains(string(driftOut), `"type":"blocked_increase"`) {
			rt.Fatalf("MR-4 drift check reported false blocked_increase against identical snapshot:\n%s", driftOut)
		}
	})
}

// TestMetamorphicRobot_MR5_SubdirAndCleanHeadAsOfIsomorphism verifies Score 8.0
// byte-for-byte output isomorphism (after timestamp normalization) between <repo>
// and randomized nested subdirectories across --robot-triage, --robot-priority,
// --robot-plan, --hooks-info, and --as-of HEAD --robot-triage via rapid.Check.
func TestMetamorphicRobot_MR5_SubdirAndCleanHeadAsOfIsomorphism(t *testing.T) {
	bv := buildBvBinary(t)
	baseDir := t.TempDir()
	caseSeq := 0

	rapid.Check(t, func(rt *rapid.T) {
		caseSeq++
		dir := filepath.Join(baseDir, fmt.Sprintf("mr5-%d", caseSeq))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		initMetamorphicGitRepo(t, dir)

		p1 := rapid.IntRange(0, 3).Draw(rt, "p1")
		p2 := rapid.IntRange(1, 4).Draw(rt, "p2")
		relSub := rapid.SampledFrom([]string{"sub", "sub/nested", "a/b/c", "pkg/deep/leaf"}).Draw(rt, "subdir")

		writeBeads(t, dir, strings.Join([]string{
			fmt.Sprintf(`{"id":"R-1","title":"Root issue","status":"open","priority":%d,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-20T00:00:00Z"}`, p1),
			fmt.Sprintf(`{"id":"R-2","title":"Dependent issue","status":"open","priority":%d,"issue_type":"feature","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-21T00:00:00Z","dependencies":[{"issue_id":"R-2","depends_on_id":"R-1","type":"blocks"}]}`, p2),
		}, "\n")+"\n")

		if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".bv", "hooks.yaml"), []byte("hooks:\n  pre-export:\n    - command: echo ok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(dir, filepath.FromSlash(relSub))
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "keep.txt"), []byte("subdir\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		commitMetamorphicAll(t, dir, "committed repo with hooks and subdir")

		argSets := [][]string{
			{"--robot-triage"},
			{"--robot-priority"},
			{"--robot-plan"},
			{"--hooks-info"},
			{"--as-of", "HEAD", "--robot-triage"},
		}

		for _, args := range argSets {
			rootOut := normalizeRobotTimestamps(runMetamorphicBV(t, bv, dir, args...))
			subOut := normalizeRobotTimestamps(runMetamorphicBV(t, bv, nested, args...))
			if rootOut != subOut {
				rt.Fatalf("MR-5 CWD isomorphism failed for %v (%s):\n--- root ---\n%s\n--- %s ---\n%s", args, relSub, rootOut, relSub, subOut)
			}
		}
	})
}

// TestWS5_CapacitySkipPhase2DriftAndArchivedBottlenecks exercises WS-5 fixes end-to-end
// through the compiled CLI binary:
//  1. --robot-capacity --agents=2 Graham (1969) bounds [graham_min_days, graham_max_days]
//  2. BV_SKIP_PHASE2=1 --robot-insights skipping KCore, Articulation, Slack, and Phase-2 centralities
//  3. .bv/drift.yaml sprint_blocked_days / sprint_inactive_days / stale_warning_days altering
//     both --robot-burndown at_risk and --robot-alerts stale_issue outputs
//  4. --robot-insights surfacing active open bottlenecks without closed-archive pollution
func TestWS5_CapacitySkipPhase2DriftAndArchivedBottlenecks(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()

	// SOURCE_DATE_EPOCH=1788264000 => 2026-09-01T12:00:00Z.
	// Build 120 closed historical hub/spoke issues (closed within last 20 days for positive velocity)
	// plus an active open diamond/bridge topology and two sprint items (SP-BLK blocked 3d, SP-IDLE idle 5d).
	var lines []string
	lines = append(lines, `{"id":"HIST-HUB","title":"Archived hub","status":"closed","priority":1,"issue_type":"epic","created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-15T00:00:00Z","closed_at":"2026-08-15T00:00:00Z"}`)
	for i := 0; i < 120; i++ {
		lines = append(lines, fmt.Sprintf(`{"id":"HIST-%03d","title":"Archived spoke %d","status":"closed","priority":2,"issue_type":"task","created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-20T00:00:00Z","closed_at":"2026-08-20T00:00:00Z","dependencies":[{"issue_id":"HIST-%03d","depends_on_id":"HIST-HUB","type":"blocks"}]}`, i, i, i))
	}
	lines = append(lines,
		`{"id":"ACT-ROOT","title":"Active root","status":"open","priority":1,"issue_type":"task","estimated_minutes":240,"created_at":"2026-08-25T00:00:00Z","updated_at":"2026-08-31T00:00:00Z"}`,
		`{"id":"ACT-BRIDGE","title":"Active bridge bottleneck","status":"open","priority":1,"issue_type":"feature","estimated_minutes":360,"created_at":"2026-08-25T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","dependencies":[{"issue_id":"ACT-BRIDGE","depends_on_id":"ACT-ROOT","type":"blocks"}]}`,
		`{"id":"ACT-LEAF-1","title":"Active leaf 1","status":"open","priority":2,"issue_type":"task","estimated_minutes":240,"created_at":"2026-08-25T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","dependencies":[{"issue_id":"ACT-LEAF-1","depends_on_id":"ACT-BRIDGE","type":"blocks"}]}`,
		`{"id":"ACT-LEAF-2","title":"Active leaf 2","status":"open","priority":2,"issue_type":"task","estimated_minutes":240,"created_at":"2026-08-25T00:00:00Z","updated_at":"2026-08-31T00:00:00Z","dependencies":[{"issue_id":"ACT-LEAF-2","depends_on_id":"ACT-BRIDGE","type":"blocks"}]}`,
		`{"id":"SP-BLK","title":"Blocked sprint item (3d)","status":"blocked","priority":2,"issue_type":"task","estimated_minutes":120,"created_at":"2026-08-20T00:00:00Z","updated_at":"2026-08-29T00:00:00Z","dependencies":[{"issue_id":"SP-BLK","depends_on_id":"ACT-ROOT","type":"blocks"}]}`,
		`{"id":"SP-IDLE","title":"Idle in_progress sprint item (5d)","status":"in_progress","priority":2,"issue_type":"task","estimated_minutes":120,"created_at":"2026-08-20T00:00:00Z","updated_at":"2026-08-27T00:00:00Z"}`,
	)
	writeBeads(t, dir, strings.Join(lines, "\n")+"\n")

	sprintLine := `{"id":"sprint-1","name":"Sprint 1","start_date":"2026-08-25T00:00:00Z","end_date":"2026-09-08T00:00:00Z","bead_ids":["SP-BLK","SP-IDLE"]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".beads", "sprints.jsonl"), []byte(sprintLine), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. --robot-capacity --agents=2 must satisfy Graham (1969) bounds with positive velocity & critical path.
	var capResp struct {
		EstimatedDays         float64 `json:"estimated_days"`
		CriticalPathMinutes   int     `json:"critical_path_minutes"`
		VelocityMinutesPerDay float64 `json:"velocity_minutes_per_day"`
		GrahamMinDays         float64 `json:"graham_min_days"`
		GrahamMaxDays         float64 `json:"graham_max_days"`
	}
	if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-capacity", "--agents=2"), &capResp); err != nil {
		t.Fatalf("unmarshal capacity: %v", err)
	}
	if capResp.CriticalPathMinutes <= 0 || capResp.VelocityMinutesPerDay <= 0 || capResp.GrahamMinDays <= 0 {
		t.Fatalf("expected positive critical_path/velocity/graham_min_days, got %+v", capResp)
	}
	if capResp.EstimatedDays+1e-9 < capResp.GrahamMinDays || capResp.EstimatedDays-1e-9 > capResp.GrahamMaxDays {
		t.Fatalf("Graham parallel bound violated: estimated_days=%f outside [%f, %f]",
			capResp.EstimatedDays, capResp.GrahamMinDays, capResp.GrahamMaxDays)
	}

	// 2. Active bottlenecks in --robot-insights must surface ACT-BRIDGE and exclude 121 closed HIST-* nodes.
	var insResp struct {
		Bottlenecks []struct {
			ID string `json:"ID"`
		} `json:"Bottlenecks"`
		Cores []struct {
			ID string `json:"ID"`
		} `json:"Cores"`
		Articulation []string `json:"Articulation"`
	}
	if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-insights"), &insResp); err != nil {
		t.Fatalf("unmarshal insights: %v", err)
	}
	foundActiveBridge := false
	for _, b := range insResp.Bottlenecks {
		if strings.HasPrefix(b.ID, "HIST-") {
			t.Fatalf("closed historical node %s polluted default --robot-insights Bottlenecks", b.ID)
		}
		if b.ID == "ACT-BRIDGE" {
			foundActiveBridge = true
		}
	}
	if !foundActiveBridge {
		t.Fatalf("expected ACT-BRIDGE in --robot-insights Bottlenecks, got %+v", insResp.Bottlenecks)
	}
	foundActiveArt := false
	for _, id := range insResp.Articulation {
		if strings.HasPrefix(id, "HIST-") {
			t.Fatalf("closed historical node %s polluted default --robot-insights Articulation", id)
		}
		if id == "ACT-BRIDGE" {
			foundActiveArt = true
		}
	}
	if !foundActiveArt {
		t.Fatalf("expected ACT-BRIDGE in --robot-insights Articulation, got %+v", insResp.Articulation)
	}

	// 3. BV_SKIP_PHASE2=1 --robot-insights must mark Phase-2 metrics (including KCore, Articulation, Slack) skipped.
	skipCmd := exec.Command(bv, "--robot-insights")
	skipCmd.Dir = dir
	skipCmd.Env = append(os.Environ(),
		"BV_NO_BROWSER=1",
		"BV_TEST_MODE=1",
		"BV_NO_SAVED_CONFIG=1",
		"BV_SKIP_PHASE2=1",
		"SOURCE_DATE_EPOCH=1788264000",
	)
	skipOut, err := skipCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("BV_SKIP_PHASE2=1 --robot-insights failed: %v\n%s", err, skipOut)
	}
	var skipIns struct {
		Cores        []any    `json:"Cores"`
		Articulation []string `json:"Articulation"`
		Slack        []any    `json:"Slack"`
		Status       map[string]struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(skipOut, &skipIns); err != nil {
		t.Fatalf("unmarshal skipped insights: %v", err)
	}
	for _, metric := range []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Cycles", "KCore", "Articulation", "Slack"} {
		if skipIns.Status[metric].State != "skipped" {
			t.Fatalf("BV_SKIP_PHASE2=1 expected status[%s].state == \"skipped\", got %q", metric, skipIns.Status[metric].State)
		}
	}
	if len(skipIns.Cores) != 0 || len(skipIns.Articulation) != 0 || len(skipIns.Slack) != 0 {
		t.Fatalf("BV_SKIP_PHASE2=1 expected empty Cores/Articulation/Slack, got cores=%d art=%d slack=%d",
			len(skipIns.Cores), len(skipIns.Articulation), len(skipIns.Slack))
	}

	// 4. .bv/drift.yaml sprint_blocked_days / sprint_inactive_days / stale_warning_days wiring.
	var defaultBurndown struct {
		AtRisk []struct {
			ID string `json:"issue_id"`
		} `json:"at_risk"`
	}
	if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-burndown", "sprint-1"), &defaultBurndown); err != nil {
		t.Fatalf("unmarshal default burndown: %v", err)
	}
	if len(defaultBurndown.AtRisk) != 2 {
		t.Fatalf("expected 2 default at_risk sprint items (SP-BLK 3d>=2d, SP-IDLE 5d>=4d), got %+v", defaultBurndown.AtRisk)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".bv"), 0o755); err != nil {
		t.Fatal(err)
	}
	customDrift := "sprint_blocked_days: 10\nsprint_inactive_days: 10\nstale_warning_days: 2\nstale_critical_days: 30\n"
	if err := os.WriteFile(filepath.Join(dir, ".bv", "drift.yaml"), []byte(customDrift), 0o600); err != nil {
		t.Fatal(err)
	}

	var relaxedBurndown struct {
		AtRisk []struct {
			ID string `json:"issue_id"`
		} `json:"at_risk"`
	}
	if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-burndown", "sprint-1"), &relaxedBurndown); err != nil {
		t.Fatalf("unmarshal custom-threshold burndown: %v", err)
	}
	if len(relaxedBurndown.AtRisk) != 0 {
		t.Fatalf("expected custom sprint_blocked_days=10/sprint_inactive_days=10 to clear at_risk, got %+v", relaxedBurndown.AtRisk)
	}

	var alertsResp struct {
		Alerts []struct {
			Type    string `json:"type"`
			IssueID string `json:"issue_id"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(runMetamorphicBV(t, bv, dir, "--robot-alerts"), &alertsResp); err != nil {
		t.Fatalf("unmarshal --robot-alerts: %v", err)
	}
	foundStaleAlert := false
	for _, al := range alertsResp.Alerts {
		if al.Type == "stale_issue" && (al.IssueID == "SP-IDLE" || al.IssueID == "SP-BLK") {
			foundStaleAlert = true
		}
	}
	if !foundStaleAlert {
		t.Fatalf("expected stale_warning_days=2 in .bv/drift.yaml to emit stale_issue alert in --robot-alerts, got %+v", alertsResp.Alerts)
	}
}
