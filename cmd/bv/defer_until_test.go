package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
)

// Issue #191: `--recipe actionable` (filters.actionable: true) must hide a
// bead whose defer_until is still in the future — the closest bv analog to
// `br ready` — while an elapsed or absent deferral keeps it visible.
func TestApplyRecipeFilters_ActionableHonoursDeferUntil(t *testing.T) {
	// The fixture and CLI must share a clock even when the build environment
	// supplies SOURCE_DATE_EPOCH (as Nix does).
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(now.Unix(), 10))
	future := now.Add(90 * 24 * time.Hour)
	elapsed := now.Add(-time.Minute)
	// RFC3339 with a non-UTC offset, parsed the way JSONL records arrive.
	var offsetIssue model.Issue
	if err := json.Unmarshal([]byte(`{"id":"OFFSET","title":"Offset deferral","status":"open","issue_type":"task","defer_until":"`+future.In(time.FixedZone("Kolkata", 5*3600+1800)).Format(time.RFC3339)+`"}`), &offsetIssue); err != nil {
		t.Fatalf("unmarshal offset issue: %v", err)
	}

	issues := []model.Issue{
		{ID: "FUTURE", Title: "Buried for a while", Status: model.StatusOpen, Priority: 0, DeferUntil: &future},
		{ID: "ELAPSED", Title: "Deferral over", Status: model.StatusOpen, Priority: 1, DeferUntil: &elapsed},
		{ID: "PLAIN", Title: "Never deferred", Status: model.StatusOpen, Priority: 2},
		offsetIssue,
	}

	r := &recipe.Recipe{Filters: recipe.FilterConfig{Actionable: ptrBool(true)}}
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "ELAPSED", "PLAIN")

	// Without the actionable gate the deferred beads are plain open issues.
	r.Filters.Actionable = nil
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "FUTURE", "ELAPSED", "PLAIN", "OFFSET")

	// has_blockers is strictly about blockers; deferral does not count as one.
	r.Filters.HasBlockers = ptrBool(false)
	requireIssueIDs(t, mustApplyRecipe(t, issues, r), "FUTURE", "ELAPSED", "PLAIN", "OFFSET")
}

func TestRobotNextClaimablePickSkipsDeferredTopPick(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	picks := []analysis.TopPick{
		{ID: "DEFERRED-1", Title: "Parked", Score: 100},
		{ID: "READY-1", Title: "Ready", Score: 90},
	}
	issues := []model.Issue{
		{ID: "DEFERRED-1", Title: "Parked", Status: model.StatusOpen, IssueType: model.TypeTask, DeferUntil: &future},
		{ID: "READY-1", Title: "Ready", Status: model.StatusOpen, IssueType: model.TypeTask},
	}

	top, diagnostic, reasons, ok := robotNextClaimablePick(picks, issues, nil, now)
	if !ok || top.ID != "READY-1" {
		t.Fatalf("expected READY-1 to be the claimable pick, got ok=%v top=%+v reasons=%v", ok, top, reasons)
	}
	if diagnostic == nil || diagnostic.ID != "DEFERRED-1" {
		t.Fatalf("diagnostic should describe the skipped top pick, got %+v", diagnostic)
	}

	// Only the deferred bead on offer: no claim, with an explicit deferral reason.
	_, _, reasons, ok = robotNextClaimablePick(picks[:1], issues, nil, now)
	if ok {
		t.Fatal("deferred top pick must not be claimable")
	}
	joined := strings.Join(reasons, "; ")
	if !strings.Contains(joined, "deferred until "+future.Format(time.RFC3339)) {
		t.Fatalf("reasons = %v, want deferral reason", reasons)
	}

	// At the deferral instant the bead is claimable again.
	top, _, _, ok = robotNextClaimablePick(picks[:1], issues, nil, future)
	if !ok || top.ID != "DEFERRED-1" {
		t.Fatalf("expected DEFERRED-1 claimable once defer_until is reached, got ok=%v top=%+v", ok, top)
	}
}

// End-to-end through the built binary: a P0 bead deferred into the future
// must not be handed out by --robot-next, must not appear in --robot-triage
// top picks or the --robot-plan, and must drop out of `--recipe actionable`.
func TestRobotCommandsHonourDeferUntilEndToEnd(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// SOURCE_DATE_EPOCH pins the robot clock to 2026-08-21T12:00:00Z.
	const pinnedEpoch = "1787313600"
	beads := `{"id":"FUTURE-1","title":"Parked P0","status":"open","priority":0,"issue_type":"task","defer_until":"2026-12-01T00:00:00Z"}
{"id":"ELAPSED-1","title":"Deferral over","status":"open","priority":3,"issue_type":"task","defer_until":"2026-01-01T00:00:00Z"}
{"id":"READY-1","title":"Ready work","status":"open","priority":2,"issue_type":"task"}
`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}

	exe := buildTestBinary(t)
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(exe, append(args, "--format=json")...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH="+pinnedEpoch, "BV_NO_CACHE=1")
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("%v failed: %v\nstderr:\n%s", args, err, ee.Stderr)
			}
			t.Fatalf("%v failed: %v", args, err)
		}
		return out
	}

	// READY-1 (P2) wins over the parked P0 and the P3. This JSONL-only
	// fixture proves readiness ordering; it has no verified live tracker route.
	var next robotNextOutput
	nextOut := run("--robot-next")
	if err := json.Unmarshal(nextOut, &next); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, nextOut)
	}
	if next.DiagnosticTopPick == nil || next.DiagnosticTopPick.ID != "READY-1" || next.SourceAuthority == nil || !next.SourceAuthority.ClaimSafe {
		t.Fatalf("robot-next = %+v, want complete readiness with diagnostic READY-1; payload=%s", next, nextOut)
	}
	if next.Actionable || next.ID != "" || next.ClaimCmd != "" || (next.Actions != nil && next.Actions.Claim != nil) {
		t.Fatalf("JSONL-only fixture emitted a live claim: %s", nextOut)
	}

	// --robot-triage: top picks exclude FUTURE-1; its recommendation carries
	// defer_until and wait guidance.
	var triage struct {
		Triage struct {
			QuickRef struct {
				Actionable int                `json:"actionable_count"`
				TopPicks   []analysis.TopPick `json:"top_picks"`
			} `json:"quick_ref"`
			Recommendations []analysis.Recommendation `json:"recommendations"`
		} `json:"triage"`
	}
	triageOut := run("--robot-triage")
	if err := json.Unmarshal(triageOut, &triage); err != nil {
		t.Fatalf("robot-triage json: %v\n%s", err, triageOut)
	}
	if triage.Triage.QuickRef.Actionable != 2 {
		t.Fatalf("quick_ref.actionable = %d, want 2\n%s", triage.Triage.QuickRef.Actionable, triageOut)
	}
	for _, pick := range triage.Triage.QuickRef.TopPicks {
		if pick.ID == "FUTURE-1" {
			t.Fatalf("robot-triage leaked deferred bead into top_picks: %s", triageOut)
		}
	}
	sawFuture := false
	for _, rec := range triage.Triage.Recommendations {
		if rec.ID != "FUTURE-1" {
			continue
		}
		sawFuture = true
		if rec.DeferUntil == nil || !rec.DeferUntil.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("FUTURE-1 recommendation defer_until = %v", rec.DeferUntil)
		}
		if !strings.Contains(rec.Action, "Deferred until 2026-12-01T00:00:00Z") {
			t.Fatalf("FUTURE-1 action = %q, want deferral guidance", rec.Action)
		}
	}
	if !sawFuture {
		t.Fatalf("FUTURE-1 should still be listed as a recommendation: %s", triageOut)
	}

	// --robot-plan: the deferred bead is not actionable work.
	var plan struct {
		Plan struct {
			TotalActionable int `json:"total_actionable"`
			Tracks          []struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			} `json:"tracks"`
		} `json:"plan"`
	}
	planOut := run("--robot-plan")
	if err := json.Unmarshal(planOut, &plan); err != nil {
		t.Fatalf("robot-plan json: %v\n%s", err, planOut)
	}
	if plan.Plan.TotalActionable != 2 {
		t.Fatalf("plan.total_actionable = %d, want 2\n%s", plan.Plan.TotalActionable, planOut)
	}
	for _, track := range plan.Plan.Tracks {
		for _, item := range track.Items {
			if item.ID == "FUTURE-1" {
				t.Fatalf("robot-plan leaked deferred bead: %s", planOut)
			}
		}
	}

	// --recipe actionable: the reported symptom. Recipe filters apply to the
	// robot issue set before triage, so the deferred bead must vanish from the
	// recommendations entirely while the elapsed and plain ones stay.
	var filtered struct {
		Triage struct {
			Recommendations []analysis.Recommendation `json:"recommendations"`
		} `json:"triage"`
	}
	filteredOut := run("--recipe", "actionable", "--robot-triage")
	if err := json.Unmarshal(filteredOut, &filtered); err != nil {
		t.Fatalf("recipe robot-triage json: %v\n%s", err, filteredOut)
	}
	got := map[string]bool{}
	for _, rec := range filtered.Triage.Recommendations {
		got[rec.ID] = true
	}
	if got["FUTURE-1"] {
		t.Fatalf("--recipe actionable still lists the deferred bead:\n%s", filteredOut)
	}
	for _, want := range []string{"ELAPSED-1", "READY-1"} {
		if !got[want] {
			t.Fatalf("--recipe actionable dropped %s:\n%s", want, filteredOut)
		}
	}
}
