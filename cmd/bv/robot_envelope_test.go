package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestRobotSuggestionsRequireCompleteLiveAuthority(t *testing.T) {
	for _, name := range []string{"complete", "absent", "unknown", "partial", "stale", "historical"} {
		t.Run(name, func(t *testing.T) {
			report := RobotSourceReport{SourceKind: "memory", Status: "loaded", Valid: 2, Visible: 2}
			if name == "partial" {
				report.Errors = 1
			}
			if name == "stale" {
				report.Stale = true
			}
			authority := newRobotSourceAuthority([]RobotSourceReport{report})
			if name == "absent" {
				authority = nil
			} else if name == "unknown" {
				authority = newRobotSourceAuthority(nil)
			}
			issues := []model.Issue{{ID: "display-a", Title: "Implement user authentication system", Status: model.StatusOpen}, {ID: "display-b", Title: "Implement user authentication system", Status: model.StatusOpen}}
			for i := range issues {
				issues[i].Origin = &model.IssueOrigin{LocalID: "local-" + issues[i].ID, WorkingDirectory: "/tracker", TrackerDirectory: "/tracker/.beads", Database: "/tracker/.beads/beads.db", Tracker: "br", Executable: "/tools/br"}
			}
			var output bytes.Buffer
			ctx := RobotContext{Issues: issues, SourceAuthority: authority, Encoder: json.NewEncoder(&output)}
			if name == "historical" {
				ctx.AsOf = "HEAD~1"
			}
			active, kind := true, "duplicate"
			registry := newRobotRegistry()
			registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotSuggestFlag: &active, SuggestType: &kind})
			if handled, err := registry.DispatchFlag("robot-suggest", ctx); err != nil || !handled {
				t.Fatalf("dispatch handled=%v error=%v", handled, err)
			}
			var result struct {
				Set analysis.SuggestionSet `json:"suggestions"`
			}
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Set.Suggestions) != 1 || result.Set.Stats.Total != 1 {
				t.Fatalf("authority guard erased useful analysis: %s", output.String())
			}
			suggestion := result.Set.Suggestions[0]
			if name == "complete" {
				if suggestion.Action == nil || suggestion.ActionCommand == "" || result.Set.Stats.ActionableCount != 1 {
					t.Fatalf("complete live source lost mutation: %s", output.String())
				}
			} else if suggestion.Action != nil || suggestion.ActionCommand != "" || result.Set.Stats.ActionableCount != 0 || suggestion.Metadata["action_unavailable_reason"] == nil {
				t.Fatalf("incomplete/historical context authorized mutation: %s", output.String())
			}
		})
	}
}

func TestRobotNextRequiresExplicitCompleteSourceAuthority(t *testing.T) {
	t.Setenv("BEADS_DIR", t.TempDir())
	t.Setenv("BEADS_DB", "")
	for _, tc := range []struct {
		name      string
		authority *RobotSourceAuthority
		complete  bool
	}{
		{"absent", nil, false},
		{"unknown", newRobotSourceAuthority(nil), false},
		{"incomplete", newRobotSourceAuthority([]RobotSourceReport{{Status: "loaded", Valid: 1, Visible: 1, Errors: 1}}), false},
		{"stale", newRobotSourceAuthority([]RobotSourceReport{{Status: "loaded", Valid: 1, Visible: 1, Stale: true}}), false},
		{"complete_in_memory", newRobotSourceAuthority([]RobotSourceReport{{SourceKind: "memory", Status: "loaded", Valid: 1, Visible: 1}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := RobotContext{Issues: []model.Issue{{ID: "safe", Title: "Known candidate", Status: model.StatusOpen, IssueType: model.TypeTask}},
				SourceAuthority: tc.authority, Encoder: json.NewEncoder(&output)}
			if err := handleRobotNext(ctx, phaseThreeRobotHandlerConfig{}); err != nil {
				t.Fatal(err)
			}
			var result robotNextOutput
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			// Complete graph authority does not manufacture a live tracker route
			// for an in-memory issue. The ready candidate remains diagnostic.
			if result.Actionable || result.ClaimCmd != "" || result.ShowCmd != "" || result.ID != "" {
				t.Fatalf("unbound in-memory source emitted a live action: %+v", result)
			}
			if result.SourceAuthority == nil || result.SourceAuthority.ClaimSafe != tc.complete {
				t.Fatalf("missing explicit authority: %+v", result)
			}
			if result.DiagnosticTopPick == nil || result.DiagnosticTopPick.ID != "safe" {
				t.Fatalf("source authority must preserve the same useful diagnostic candidate: %+v", result)
			}
			if result.Actions != nil && (result.Actions.Claim != nil || result.Actions.Show != nil) {
				t.Fatalf("unbound source emitted a nested live action: %+v", result.Actions)
			}
		})
	}
}

// Every robot payload embeds the envelope built from the dispatch context, so
// the source, time-travel, and scoping metadata cannot be forgotten per handler.
func TestRobotContext_EnvelopeCarriesSourceScopeAndAsOf(t *testing.T) {
	ctx := RobotContext{
		DataHash:   "abc",
		SourcePath: "/repo/.beads/issues.jsonl",
		SourceKind: "jsonl_local",
	}
	env := ctx.Envelope()
	if env.DataHash != "abc" || env.SourcePath != "/repo/.beads/issues.jsonl" || env.SourceKind != "jsonl_local" {
		t.Fatalf("envelope = %+v", env)
	}
	if env.Scope != nil {
		t.Fatalf("no scoping flags -> scope must be omitted, got %+v", env.Scope)
	}
	if env.AsOf != "" || env.AsOfCommit != "" {
		t.Fatalf("no --as-of -> as_of fields empty, got %+v", env)
	}

	ctx.LabelScope = "backend"
	ctx.Recipe = "actionable"
	ctx.Repo = "api"
	env = ctx.Envelope()
	if env.Scope == nil || env.Scope.Label != "backend" || env.Scope.Recipe != "actionable" || env.Scope.Repo != "api" {
		t.Fatalf("scope not propagated: %+v", env.Scope)
	}
	if len(env.Scope.Unsupported) != 0 {
		t.Fatalf("without --as-of nothing is unsupported, got %v", env.Scope.Unsupported)
	}

	// A derived hash replaces the context hash but keeps everything else.
	if got := ctx.EnvelopeWithHash("zzz"); got.DataHash != "zzz" || got.SourcePath != ctx.SourcePath || got.Scope == nil {
		t.Fatalf("EnvelopeWithHash = %+v", got)
	}
}

func TestRobotContext_EnvelopeDeclaresUnsupportedAsOf(t *testing.T) {
	ctx := RobotContext{DataHash: "h", AsOf: "HEAD~5", AsOfCommit: "0123456789abcdef", SourceKind: "git", SourcePath: ".beads@HEAD~5"}

	// Commands that analyse ctx.Issues honour --as-of: no unsupported list.
	for _, cmd := range []string{"robot-triage", "robot-plan", "robot-insights", "robot-blocker-chain", "robot-priority", "robot-forecast", "robot-capacity"} {
		ctx.Command = cmd
		env := ctx.Envelope()
		if env.AsOf != "HEAD~5" || env.AsOfCommit != "0123456789abcdef" {
			t.Fatalf("%s: as_of metadata missing: %+v", cmd, env)
		}
		if env.Scope != nil && len(env.Scope.Unsupported) > 0 {
			t.Fatalf("%s must honour --as-of, got unsupported %v", cmd, env.Scope.Unsupported)
		}
	}

	// Commands that read sprint files from disk or walk live git history cannot.
	for _, cmd := range []string{"robot-burndown", "robot-sprint-list", "robot-sprint-show", "robot-history", "robot-orphans", "robot-file-beads", "robot-file-hotspots"} {
		ctx.Command = cmd
		env := ctx.Envelope()
		if env.Scope == nil || !reflect.DeepEqual(env.Scope.Unsupported, []string{"as_of"}) {
			t.Fatalf("%s must declare as_of unsupported, got %+v", cmd, env.Scope)
		}
	}

	// The declaration only exists while time-travelling.
	ctx.AsOf, ctx.AsOfCommit = "", ""
	ctx.Command = "robot-burndown"
	if env := ctx.Envelope(); env.Scope != nil {
		t.Fatalf("no --as-of: burndown scope must be omitted, got %+v", env.Scope)
	}
}

// DispatchFlag stamps the normalized command onto the context so the envelope
// can consult the per-command capability table.
func TestRobotRegistry_DispatchFlagSetsCommand(t *testing.T) {
	active := true
	var seen string
	reg := &RobotRegistry{}
	reg.Register(RobotCommand{
		Name:     "probe",
		FlagName: "robot-probe",
		FlagPtr:  &active,
		Handler: func(ctx RobotContext) error {
			seen = ctx.Command
			return nil
		},
	})
	handled, err := reg.DispatchFlag("--robot-probe", RobotContext{})
	if err != nil || !handled {
		t.Fatalf("dispatch: handled=%v err=%v", handled, err)
	}
	if seen != "robot-probe" {
		t.Fatalf("ctx.Command = %q, want robot-probe", seen)
	}
}
