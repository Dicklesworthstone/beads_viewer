package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	t.Setenv("SOURCE_DATE_EPOCH", "")
	now := time.Now()
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
	requireIssueIDs(t, applyRecipeFilters(issues, r), "ELAPSED", "PLAIN")

	// Without the actionable gate the deferred beads are plain open issues.
	r.Filters.Actionable = nil
	requireIssueIDs(t, applyRecipeFilters(issues, r), "FUTURE", "ELAPSED", "PLAIN", "OFFSET")

	// has_blockers is strictly about blockers; deferral does not count as one.
	r.Filters.HasBlockers = ptrBool(false)
	requireIssueIDs(t, applyRecipeFilters(issues, r), "FUTURE", "ELAPSED", "PLAIN", "OFFSET")
}

func TestRobotNextClaimablePickSkipsDeferredTopPick(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
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

	top, diagnostic, reasons, ok := robotNextClaimablePick(picks, issues, now)
	if !ok || top.ID != "READY-1" {
		t.Fatalf("expected READY-1 to be the claimable pick, got ok=%v top=%+v reasons=%v", ok, top, reasons)
	}
	if diagnostic == nil || diagnostic.ID != "DEFERRED-1" {
		t.Fatalf("diagnostic should describe the skipped top pick, got %+v", diagnostic)
	}

	// Only the deferred bead on offer: no claim, with an explicit deferral reason.
	_, _, reasons, ok = robotNextClaimablePick(picks[:1], issues, now)
	if ok {
		t.Fatal("deferred top pick must not be claimable")
	}
	joined := strings.Join(reasons, "; ")
	if !strings.Contains(joined, "deferred until "+future.Format(time.RFC3339)) {
		t.Fatalf("reasons = %v, want deferral reason", reasons)
	}

	// At the deferral instant the bead is claimable again.
	top, _, _, ok = robotNextClaimablePick(picks[:1], issues, future)
	if !ok || top.ID != "DEFERRED-1" {
		t.Fatalf("expected DEFERRED-1 claimable once defer_until is reached, got ok=%v top=%+v", ok, top)
	}
}

func TestRobotNextTimestampsPreserveSubsecondClaimBoundary(t *testing.T) {
	// The handler waits for phase 2 and requires completed claim metrics. Pin a
	// valid robot epoch so the analysis config runs deterministic algorithms to
	// completion rather than racing short wall-clock metric deadlines.
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787392800")
	t.Setenv("BV_SKIP_PHASE2", "")
	t.Setenv("BV_PHASE2_TIMEOUT_S", "")
	t.Setenv("BV_NO_CACHE", "1")

	now := time.Date(2026, 8, 22, 12, 0, 0, 500_000_000, time.UTC)
	deferUntil := now.Add(250 * time.Millisecond)
	pick := analysis.TopPick{ID: "DEFERRED-SUBSECOND", Title: "Briefly parked", Score: 100}
	issue := model.Issue{
		ID:         pick.ID,
		Title:      pick.Title,
		Status:     model.StatusOpen,
		IssueType:  model.TypeTask,
		DeferUntil: &deferUntil,
	}

	reasons := robotNextClaimabilityReasons(pick, map[string]model.Issue{issue.ID: issue}, now)
	if len(reasons) != 1 {
		t.Fatalf("subsecond deferral reasons = %v, want exactly one", reasons)
	}
	generatedAt := formatRobotNextTime(now)
	wantDeferUntil := formatRobotNextTime(deferUntil)
	if generatedAt != "2026-08-22T12:00:00.5Z" || wantDeferUntil != "2026-08-22T12:00:00.75Z" {
		t.Fatalf("precise times = generated %q, defer %q", generatedAt, wantDeferUntil)
	}
	if !strings.Contains(reasons[0], wantDeferUntil) {
		t.Fatalf("deferral reason %q does not preserve %q", reasons[0], wantDeferUntil)
	}
	if generatedAt >= wantDeferUntil {
		t.Fatalf("serialized claim clock %q must precede deferral %q", generatedAt, wantDeferUntil)
	}
	runAt := func(decisionTime time.Time) robotNextOutput {
		t.Helper()
		var output bytes.Buffer
		if err := handleRobotNextAt(RobotContext{
			DataHash: "subsecond-fixture",
			Issues:   []model.Issue{issue},
			Encoder:  json.NewEncoder(&output),
		}, phaseThreeRobotHandlerConfig{}, decisionTime); err != nil {
			t.Fatalf("handleRobotNextAt(%s): %v", decisionTime.Format(time.RFC3339Nano), err)
		}
		var decoded robotNextOutput
		if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
			t.Fatalf("decode robot-next output: %v\n%s", err, output.String())
		}
		return decoded
	}

	decoded := runAt(now)
	if decoded.GeneratedAt != generatedAt {
		t.Fatalf("robot-next generated_at = %q, want exact decision clock %q", decoded.GeneratedAt, generatedAt)
	}
	if decoded.Actionable || decoded.ClaimCmd != "" {
		t.Fatalf("pre-boundary robot-next output is claimable: %+v", decoded)
	}
	if len(decoded.Degraded) != 1 || decoded.Degraded[0].Code != "no_actionable_recommendation" {
		t.Fatalf("pre-boundary degradation = %+v, want no_actionable_recommendation", decoded.Degraded)
	}

	if reasons := robotNextClaimabilityReasons(pick, map[string]model.Issue{issue.ID: issue}, deferUntil); len(reasons) != 0 {
		t.Fatalf("exact defer_until instant must be claimable, got reasons %v", reasons)
	}
	boundary := runAt(deferUntil)
	if boundary.GeneratedAt != wantDeferUntil {
		t.Fatalf("boundary generated_at = %q, want %q", boundary.GeneratedAt, wantDeferUntil)
	}
	if !boundary.Actionable || boundary.ID != issue.ID || boundary.ClaimCmd != "br update DEFERRED-SUBSECOND --status=in_progress" {
		t.Fatalf("boundary robot-next output is not the expected claim: %+v", boundary)
	}
	if len(boundary.Degraded) != 0 {
		t.Fatalf("boundary degradation = %+v, want none", boundary.Degraded)
	}
}

// End-to-end through the built binary: a P0 bead deferred into the future
// must not be handed out by --robot-next, must not appear in --robot-triage
// top picks or the --robot-plan, and must drop out of `--recipe actionable`.
func TestRobotCommandsHonourDeferUntilEndToEnd(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
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
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(beads), 0o644); err != nil {
		t.Fatalf("write beads: %v", err)
	}
	writeCurrentBRMetadata(t, beadsDir)

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

	// --robot-next uses the pinned clock for deterministic ranking, but must not
	// emit a live mutation command from that non-live clock.
	var next struct {
		Actionable bool                   `json:"actionable"`
		ID         string                 `json:"id"`
		Degraded   []robotNextDegradation `json:"degraded"`
	}
	nextOut := run("--robot-next")
	if err := json.Unmarshal(nextOut, &next); err != nil {
		t.Fatalf("robot-next json: %v\n%s", err, nextOut)
	}
	if next.Actionable || next.ID != "" || len(next.Degraded) == 0 {
		t.Fatalf("robot-next = %+v, want a pinned-clock no-claim result; payload=%s", next, nextOut)
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
	if len(triage.Triage.QuickRef.TopPicks) != 0 {
		t.Fatalf("pinned-clock triage exposed claimable top picks: %s", triageOut)
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
		if !strings.Contains(rec.Action, "Inspect only") {
			t.Fatalf("FUTURE-1 action = %q, want non-mutating pinned-clock guidance", rec.Action)
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
