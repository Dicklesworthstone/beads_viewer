package model

import (
	"encoding/json"
	"testing"
	"time"
)

// Issue #191: defer_until must decode from beads JSONL (including a non-UTC
// offset), deep-copy on Clone, and gate readiness strictly on "still in the
// future".
func TestIssue_DeferUntilDecodeCloneAndIsDeferredAt(t *testing.T) {
	raw := `{"id":"D-1","title":"Later","status":"open","issue_type":"task","defer_until":"2027-02-03T09:30:00-05:00"}`
	var issue Issue
	if err := json.Unmarshal([]byte(raw), &issue); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := time.Date(2027, 2, 3, 14, 30, 0, 0, time.UTC) // 09:30-05:00
	if issue.DeferUntil == nil || !issue.DeferUntil.Equal(want) {
		t.Fatalf("defer_until = %v, want %v", issue.DeferUntil, want)
	}

	if !issue.IsDeferredAt(want.Add(-time.Nanosecond)) {
		t.Fatal("an instant before defer_until must still be deferred")
	}
	if issue.IsDeferredAt(want) {
		t.Fatal("defer_until reached exactly must no longer be deferred")
	}
	if issue.IsDeferredAt(want.Add(time.Hour)) {
		t.Fatal("an instant after defer_until must not be deferred")
	}
	// Same instant expressed in another zone: instant comparison, not wall time.
	if issue.IsDeferredAt(want.In(time.FixedZone("X", -10*3600))) {
		t.Fatal("zone of the reference instant must not affect the verdict")
	}

	clone := issue.Clone()
	if clone.DeferUntil == nil || clone.DeferUntil == issue.DeferUntil {
		t.Fatal("Clone must deep-copy defer_until")
	}
	if !clone.DeferUntil.Equal(*issue.DeferUntil) {
		t.Fatalf("clone defer_until = %v, want %v", clone.DeferUntil, issue.DeferUntil)
	}

	// Round-trip keeps the field (and omits it when unset).
	out, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Issue
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back.DeferUntil == nil || !back.DeferUntil.Equal(want) {
		t.Fatalf("round-trip defer_until = %v, want %v", back.DeferUntil, want)
	}
}

func TestIssue_IsDeferredAtWithoutDeferUntil(t *testing.T) {
	issue := Issue{ID: "N-1", Title: "No deferral", Status: StatusOpen}
	if issue.IsDeferredAt(time.Now()) {
		t.Fatal("issue without defer_until must never be deferred")
	}
	if issue.IsDeferredAt(time.Time{}) {
		t.Fatal("issue without defer_until must never be deferred, even at the zero instant")
	}
	out, err := json.Marshal(issue)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) == "" || json.Valid(out) && containsKey(out, "defer_until") {
		t.Fatalf("unset defer_until must be omitted from JSON, got %s", out)
	}
}

func containsKey(doc []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func TestReadinessIndex_WithholdsEphemeralPinnedAndTemplateIssues(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	rawJSON := `{"id":"TPL-1","title":"Template","status":"open","issue_type":"task","created_by":"alice","close_reason":"done","ephemeral":true,"pinned":true,"is_template":true}`
	var decoded Issue
	if err := json.Unmarshal([]byte(rawJSON), &decoded); err != nil {
		t.Fatalf("unmarshal special flags: %v", err)
	}
	if decoded.CreatedBy != "alice" || decoded.CloseReason != "done" || !decoded.Ephemeral || !decoded.Pinned || !decoded.IsTemplate {
		t.Fatalf("decoded fields mismatch: %+v", decoded)
	}
	cloned := decoded.Clone()
	if cloned.CreatedBy != "alice" || cloned.CloseReason != "done" || !cloned.Ephemeral || !cloned.Pinned || !cloned.IsTemplate {
		t.Fatalf("Clone() dropped special fields: %+v", cloned)
	}

	issues := []Issue{
		{ID: "BLOCKER", Title: "Blocker", Status: StatusOpen, IssueType: TypeTask},
		{ID: "READY-NORMAL", Title: "Normal ready", Status: StatusOpen, IssueType: TypeTask},
		{ID: "EPHEMERAL-OPEN", Title: "Ephemeral", Status: StatusOpen, IssueType: TypeTask, Ephemeral: true},
		{ID: "PINNED-OPEN", Title: "Pinned", Status: StatusOpen, IssueType: TypeTask, Pinned: true},
		{ID: "TEMPLATE-OPEN", Title: "Template", Status: StatusOpen, IssueType: TypeTask, IsTemplate: true},
		{
			ID: "PINNED-GATED", Title: "Pinned behind blocker", Status: StatusOpen, IssueType: TypeTask, Pinned: true,
			Dependencies: []*Dependency{{IssueID: "PINNED-GATED", DependsOnID: "BLOCKER", Type: DepBlocks}},
		},
		{
			ID: "NORMAL-GATED", Title: "Normal behind blocker", Status: StatusOpen, IssueType: TypeTask,
			Dependencies: []*Dependency{{IssueID: "NORMAL-GATED", DependsOnID: "BLOCKER", Type: DepBlocks}},
		},
	}

	idx := NewReadinessIndex(issues)
	if !idx.Ready("READY-NORMAL", now) || !idx.Claimable("READY-NORMAL", now) {
		t.Fatal("normal unblocked open issue must be ready and claimable")
	}
	for _, withheldID := range []string{"EPHEMERAL-OPEN", "PINNED-OPEN", "TEMPLATE-OPEN"} {
		if idx.Ready(withheldID, now) {
			t.Fatalf("%s must not be Ready()", withheldID)
		}
		if idx.Claimable(withheldID, now) {
			t.Fatalf("%s must not be Claimable()", withheldID)
		}
	}
	if !idx.ReadyAfterSingle("NORMAL-GATED", now, "BLOCKER") {
		t.Fatal("NORMAL-GATED must become ready after BLOCKER closes")
	}
	if idx.ReadyAfterSingle("PINNED-GATED", now, "BLOCKER") {
		t.Fatal("PINNED-GATED must remain withheld in ReadyAfterSingle even after BLOCKER closes")
	}
	completed := map[string]bool{"BLOCKER": true}
	if !idx.ReadyAfter("NORMAL-GATED", now, completed) {
		t.Fatal("NORMAL-GATED must become ready in ReadyAfter")
	}
	if idx.ReadyAfter("PINNED-GATED", now, completed) {
		t.Fatal("PINNED-GATED must remain withheld in ReadyAfter")
	}
}
