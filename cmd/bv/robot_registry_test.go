package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestRobotHistoryTimeoutFromMillisecondsChecked(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want time.Duration
		ok   bool
	}{
		{name: "negative is unset", ms: -1, want: 0, ok: false},
		{name: "zero remains unbounded sentinel", ms: 0, want: 0, ok: true},
		{name: "ordinary duration", ms: 1250, want: 1250 * time.Millisecond, ok: true},
		{
			name: "largest exact millisecond duration",
			ms:   maxRobotHistoryTimeoutMillis,
			want: time.Duration(maxRobotHistoryTimeoutMillis) * time.Millisecond,
			ok:   true,
		},
		{
			name: "one millisecond beyond duration range saturates",
			ms:   maxRobotHistoryTimeoutMillis + 1,
			want: time.Duration(math.MaxInt64),
			ok:   true,
		},
		{
			name: "largest parsed integer saturates",
			ms:   math.MaxInt64,
			want: time.Duration(math.MaxInt64),
			ok:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := robotHistoryTimeoutFromMilliseconds(tt.ms)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("robotHistoryTimeoutFromMilliseconds(%d) = (%s, %v), want (%s, %v)", tt.ms, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestResolveRobotHistoryTimeoutSaturatesOverflow(t *testing.T) {
	t.Setenv("BV_ROBOT_HISTORY_TIMEOUT_MS", strconv.FormatInt(maxRobotHistoryTimeoutMillis+1, 10))
	unset := -1
	if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &unset}); got != time.Duration(math.MaxInt64) {
		t.Fatalf("overflowing environment timeout = %s, want saturation at %s", got, time.Duration(math.MaxInt64))
	}
	if strconv.IntSize == 64 {
		overflowingMillis := int64(maxRobotHistoryTimeoutMillis + 1)
		overflowingFlag := int(overflowingMillis)
		if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &overflowingFlag}); got != time.Duration(math.MaxInt64) {
			t.Fatalf("overflowing flag timeout = %s, want saturation at %s", got, time.Duration(math.MaxInt64))
		}
	}

	flagValue := 25
	if got := resolveRobotHistoryTimeout(phaseThreeRobotHandlerConfig{HistoryTimeoutMs: &flagValue}); got != 25*time.Millisecond {
		t.Fatalf("explicit flag timeout = %s, want 25ms and precedence over environment", got)
	}
}

func TestTriageHistoryResultStatusPrefersTimeout(t *testing.T) {
	if got := triageHistoryResultStatus(context.DeadlineExceeded, errors.New("git log failed")); got != "timeout" {
		t.Fatalf("deadline plus report error status = %q, want timeout", got)
	}
	if got := triageHistoryResultStatus(nil, errors.New("git log failed")); got != "error" {
		t.Fatalf("report error status = %q, want error", got)
	}
	if got := triageHistoryResultStatus(nil, nil); got != "ok" {
		t.Fatalf("successful report status = %q, want ok", got)
	}
}

func TestRobotRegistryValidate_RejectsModifierAlone(t *testing.T) {
	var robotTriage bool
	robotByLabel := "backend"

	registry := newRobotRegistry()
	registry.Register(RobotCommand{
		Name:        "robot-triage",
		FlagName:    "robot-triage",
		FlagPtr:     &robotTriage,
		Description: "Unified triage output",
	})
	registry.Register(RobotCommand{
		Name:            "robot-by-label",
		FlagName:        "robot-by-label",
		FlagPtr:         &robotByLabel,
		RequiredCoFlags: []string{"robot-triage", "robot-insights", "robot-plan", "robot-priority"},
		IsModifier:      true,
		Description:     "Filter robot output by label",
	})
	registry.Register(RobotCommand{
		Name:        "robot-insights",
		FlagName:    "robot-insights",
		FlagPtr:     ptrTo(false),
		Description: "Insights output",
	})
	registry.Register(RobotCommand{
		Name:        "robot-plan",
		FlagName:    "robot-plan",
		FlagPtr:     ptrTo(false),
		Description: "Plan output",
	})
	registry.Register(RobotCommand{
		Name:        "robot-priority",
		FlagName:    "robot-priority",
		FlagPtr:     ptrTo(false),
		Description: "Priority output",
	})

	err := registry.Validate()
	if err == nil {
		t.Fatal("expected modifier-alone validation error")
	}
	if !strings.Contains(err.Error(), "--robot-by-label") {
		t.Fatalf("expected error to mention modifier flag, got %q", err)
	}
	if !strings.Contains(err.Error(), "--robot-triage") {
		t.Fatalf("expected error to mention required co-flag, got %q", err)
	}

	robotTriage = true
	if err := registry.Validate(); err != nil {
		t.Fatalf("expected modifier to validate once paired with primary flag: %v", err)
	}
}

func TestRobotRegistryAnyActive_MatchesOldLogic(t *testing.T) {
	var (
		robotHelp       bool
		robotInsights   bool
		robotTriage     bool
		robotSearch     bool
		robotFileBeads  string
		robotByLabel    string
		robotByAssignee string
		robotDocs       string
	)

	registry := newRobotRegistry()
	registry.Register(RobotCommand{Name: "robot-help", FlagName: "robot-help", FlagPtr: &robotHelp, Description: "Help"})
	registry.Register(RobotCommand{Name: "robot-insights", FlagName: "robot-insights", FlagPtr: &robotInsights, Description: "Insights"})
	registry.Register(RobotCommand{Name: "robot-triage", FlagName: "robot-triage", FlagPtr: &robotTriage, Description: "Triage"})
	registry.Register(RobotCommand{Name: "robot-search", FlagName: "robot-search", FlagPtr: &robotSearch, Description: "Search"})
	registry.Register(RobotCommand{Name: "robot-file-beads", FlagName: "robot-file-beads", FlagPtr: &robotFileBeads, Description: "File beads"})
	registry.Register(RobotCommand{
		Name:            "robot-by-label",
		FlagName:        "robot-by-label",
		FlagPtr:         &robotByLabel,
		RequiredCoFlags: []string{"robot-insights", "robot-triage"},
		IsModifier:      true,
		Description:     "Label filter",
	})
	registry.Register(RobotCommand{
		Name:            "robot-by-assignee",
		FlagName:        "robot-by-assignee",
		FlagPtr:         &robotByAssignee,
		RequiredCoFlags: []string{"robot-insights", "robot-triage"},
		IsModifier:      true,
		Description:     "Assignee filter",
	})
	registry.Register(RobotCommand{Name: "robot-docs", FlagName: "robot-docs", FlagPtr: &robotDocs, Description: "Docs"})

	oldLogic := func() bool {
		return robotHelp ||
			robotInsights ||
			robotTriage ||
			robotSearch ||
			robotFileBeads != "" ||
			robotByLabel != "" ||
			robotByAssignee != "" ||
			robotDocs != ""
	}

	tests := []struct {
		name  string
		setup func()
	}{
		{name: "none active", setup: func() {}},
		{name: "help active", setup: func() { robotHelp = true }},
		{name: "primary robot command active", setup: func() { robotTriage = true }},
		{name: "string command active", setup: func() { robotFileBeads = "pkg/ui/model.go" }},
		{name: "modifier alone still enables robot mode", setup: func() { robotByLabel = "backend" }},
		{name: "docs topic active", setup: func() { robotDocs = "commands" }},
		{name: "multiple mixed flags", setup: func() {
			robotSearch = true
			robotByAssignee = "alice"
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			robotHelp = false
			robotInsights = false
			robotTriage = false
			robotSearch = false
			robotFileBeads = ""
			robotByLabel = ""
			robotByAssignee = ""
			robotDocs = ""

			tt.setup()

			if got, want := registry.AnyActive(), oldLogic(); got != want {
				t.Fatalf("AnyActive()=%v, want %v", got, want)
			}
		})
	}
}

func TestRobotRegistryDispatchFlag_RunsActiveHandler(t *testing.T) {
	var robotHelp bool
	var called int

	registry := newRobotRegistry()
	registry.Register(RobotCommand{
		Name:     "robot-help",
		FlagName: "robot-help",
		FlagPtr:  &robotHelp,
		Handler: func(ctx RobotContext) error {
			called++
			if got := ctx.StdoutOrDefault(); got != ctx.Stdout {
				t.Fatalf("expected dispatch to preserve stdout writer")
			}
			return nil
		},
	})

	stdout := &bytes.Buffer{}
	ctx := RobotContext{Stdout: stdout}

	handled, err := registry.DispatchFlag("robot-help", ctx)
	if err != nil {
		t.Fatalf("inactive flag should not error: %v", err)
	}
	if handled {
		t.Fatal("inactive flag should not dispatch")
	}

	robotHelp = true
	handled, err = registry.DispatchFlag("robot-help", ctx)
	if err != nil {
		t.Fatalf("dispatch returned error: %v", err)
	}
	if !handled {
		t.Fatal("active flag should dispatch")
	}
	if called != 1 {
		t.Fatalf("handler call count = %d, want 1", called)
	}
}

func TestRobotDiffHandlerPreservesSnapshotTimestampsWithoutMutatingInput(t *testing.T) {
	pinned := time.Date(2026, 8, 26, 12, 34, 56, 0, time.UTC)
	t.Setenv("SOURCE_DATE_EPOCH", strconv.FormatInt(pinned.Unix(), 10))

	active := true
	registry := newRobotRegistry()
	registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotDiffFlag: &active})

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	originalTo := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	diff := &analysis.SnapshotDiff{FromTimestamp: from, ToTimestamp: originalTo}
	var output bytes.Buffer
	handled, err := registry.DispatchFlag("robot-diff", RobotContext{
		DataHash:             "current-hash",
		Diff:                 diff,
		DiffResolvedRevision: "abc123",
		DiffHistoricalIssues: nil,
		Encoder:              json.NewEncoder(&output),
	})
	if err != nil {
		t.Fatalf("dispatch robot-diff: %v", err)
	}
	if !handled {
		t.Fatal("robot-diff handler was not dispatched")
	}
	var decoded struct {
		GeneratedAt string `json:"generated_at"`
		Diff        struct {
			FromTimestamp time.Time `json:"from_timestamp"`
			ToTimestamp   time.Time `json:"to_timestamp"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode robot-diff output: %v\n%s", err, output.String())
	}
	if decoded.GeneratedAt != pinned.Format(time.RFC3339) {
		t.Fatalf("generated_at = %q, want %v", decoded.GeneratedAt, pinned)
	}
	if !decoded.Diff.ToTimestamp.Equal(originalTo) {
		t.Fatalf("to timestamp = %v, want preserved endpoint %v", decoded.Diff.ToTimestamp, originalTo)
	}
	if !decoded.Diff.FromTimestamp.Equal(from) {
		t.Fatalf("from timestamp = %v, want %v", decoded.Diff.FromTimestamp, from)
	}
	if !diff.ToTimestamp.Equal(originalTo) {
		t.Fatalf("handler mutated input diff timestamp to %v", diff.ToTimestamp)
	}
}

func TestRobotDiffGeneratedAndSnapshotTimestampsPreserveIndependentPrecision(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 34, 56, 123_456_789, time.UTC)
	originalTo := now.Add(time.Hour)
	diff := &analysis.SnapshotDiff{ToTimestamp: originalTo}
	var output bytes.Buffer
	if err := handleRobotDiffAt(RobotContext{
		DataHash: "current-hash",
		Diff:     diff,
		Encoder:  json.NewEncoder(&output),
	}, now); err != nil {
		t.Fatalf("encode robot-diff at precise instant: %v", err)
	}

	var decoded struct {
		GeneratedAt string `json:"generated_at"`
		Diff        struct {
			ToTimestamp string `json:"to_timestamp"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode robot-diff output: %v\n%s", err, output.String())
	}
	wantGenerated := "2026-08-26T12:34:56.123456789Z"
	wantEndpoint := originalTo.Format(time.RFC3339Nano)
	if decoded.GeneratedAt != wantGenerated || decoded.Diff.ToTimestamp != wantEndpoint {
		t.Fatalf("serialized instants = generated %q, endpoint %q; want %q and %q", decoded.GeneratedAt, decoded.Diff.ToTimestamp, wantGenerated, wantEndpoint)
	}
	if !diff.ToTimestamp.Equal(originalTo) {
		t.Fatalf("handler mutated input diff timestamp to %v", diff.ToTimestamp)
	}
}

func TestRobotDiffSurfacesBothSnapshotLoadGaps(t *testing.T) {
	diff := &analysis.SnapshotDiff{}
	var output bytes.Buffer
	if err := handleRobotDiffAt(RobotContext{
		DataHash: "current-hash",
		LoadStats: &RobotLoadStats{
			SourcePath: ".beads/issues.jsonl",
			Valid:      10,
			Errors:     1,
		},
		Diff: diff,
		DiffFromLoadStats: &RobotLoadStats{
			SourcePath: "abc123:.beads/issues.jsonl",
			Valid:      9,
			Errors:     2,
		},
		DiffResolvedRevision: "abc123",
		Encoder:              json.NewEncoder(&output),
	}, time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("encode incomplete-authority robot-diff: %v", err)
	}
	var decoded struct {
		FromLoadStats *RobotLoadStats        `json:"from_load_stats"`
		ToLoadStats   *RobotLoadStats        `json:"to_load_stats"`
		Degraded      []robotNextDegradation `json:"degraded"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode incomplete-authority robot-diff: %v\n%s", err, output.String())
	}
	if decoded.FromLoadStats == nil || decoded.FromLoadStats.Errors != 2 {
		t.Fatalf("from_load_stats = %+v, want two dropped historical records", decoded.FromLoadStats)
	}
	if decoded.ToLoadStats == nil || decoded.ToLoadStats.Errors != 1 {
		t.Fatalf("to_load_stats = %+v, want one dropped current record", decoded.ToLoadStats)
	}
	codes := make(map[string]bool, len(decoded.Degraded))
	for _, degradation := range decoded.Degraded {
		codes[degradation.Code] = true
	}
	if !codes["robot_diff_from_load_incomplete"] || !codes["robot_diff_to_load_incomplete"] {
		t.Fatalf("degradations = %+v, want distinct from/to load warnings", decoded.Degraded)
	}
}

func TestRepositoryRouteGapRejectsWorkingDirectorySideData(t *testing.T) {
	ctx := RobotContext{RepositoryRouteUnavailableReasons: []string{
		"BEADS_DB selects issue storage without proving its repository",
	}}
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "correlation", err: requireLiveSingleRepoCorrelationContext(ctx, "--robot-history")},
		{name: "side data", err: requireLiveSingleRepoSideDataContext(ctx, "--robot-alerts", "saved baseline")},
	} {
		if test.err == nil || !strings.Contains(test.err.Error(), "BEADS_DB") {
			t.Fatalf("%s route guard error = %v, want explicit repository-routing evidence", test.name, test.err)
		}
	}
}

func TestSanitizeRobotTriageRecommendationDoesNotMutateSharedReasonBacking(t *testing.T) {
	shared := []string{"available for work", "high impact"}
	first := analysis.Recommendation{Reasons: shared}
	second := analysis.Recommendation{Reasons: shared}

	sanitizeRobotTriageRecommendation(&first, "diagnostic only")
	if got := strings.Join(first.Reasons, "|"); got != "high impact|diagnostic only" {
		t.Fatalf("sanitized reasons = %q", got)
	}
	if got := strings.Join(second.Reasons, "|"); got != "available for work|high impact" {
		t.Fatalf("sanitizing one recommendation mutated shared peer reasons: %q", got)
	}
}

func TestAnalysisHandlersEmitRepositoryRouteAndHistoricalClockEvidence(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	t.Setenv("SOURCE_DATE_EPOCH", "1787745600")
	analysisTime := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	issue := model.Issue{ID: "READY-1", Title: "Ready", Status: model.StatusOpen, IssueType: model.TypeTask}
	baseCtx := RobotContext{
		Issues:                            []model.Issue{issue},
		AuthoritativeIssues:               []model.Issue{issue},
		DataHash:                          analysis.ComputeDataHash([]model.Issue{issue}),
		DataHashMatchesIssues:             true,
		AsOf:                              "release-2024",
		AsOfCommit:                        "0123456789abcdef",
		AnalysisTime:                      analysisTime,
		RepositoryRouteUnavailableReasons: []string{"explicit source route is unverified"},
	}

	tests := []struct {
		name     string
		command  string
		registry func() *RobotRegistry
	}{
		{
			name: "plan", command: "robot-plan",
			registry: func() *RobotRegistry {
				active := true
				registry := newRobotRegistry()
				registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotPlanFlag: &active})
				return &registry
			},
		},
		{
			name: "priority", command: "robot-priority",
			registry: func() *RobotRegistry {
				active := true
				registry := newRobotRegistry()
				registerPhaseTwoRobotHandlers(&registry, phaseTwoRobotHandlerConfig{RobotPriorityFlag: &active})
				return &registry
			},
		},
		{
			name: "insights", command: "robot-insights",
			registry: func() *RobotRegistry {
				active := true
				registry := newRobotRegistry()
				registerPhaseThreeRobotHandlers(&registry, phaseThreeRobotHandlerConfig{RobotInsightsFlag: &active})
				return &registry
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := baseCtx
			ctx.Encoder = json.NewEncoder(&output)
			handled, err := test.registry().DispatchFlag(test.command, ctx)
			if err != nil {
				t.Fatalf("dispatch %s: %v", test.command, err)
			}
			if !handled {
				t.Fatalf("%s was not dispatched", test.command)
			}
			var decoded map[string]any
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatalf("decode %s: %v\n%s", test.command, err, output.String())
			}
			routes, ok := decoded["repository_route_unavailable_reasons"].([]any)
			if !ok || len(routes) != 1 || routes[0] != "explicit source route is unverified" {
				t.Fatalf("%s route evidence = %#v", test.command, decoded["repository_route_unavailable_reasons"])
			}
			if decoded["analysis_time"] != analysisTime.Format(time.RFC3339Nano) {
				t.Fatalf("%s analysis_time = %#v, want %s", test.command, decoded["analysis_time"], analysisTime.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestDispatchRobotFlagResult_ReturnsComposableOutcome(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var robotHelp bool

		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return nil
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if result.Handled {
			t.Fatal("inactive flag should not dispatch")
		}
		if result.ExitCode != 0 {
			t.Fatalf("inactive flag exit code = %d, want 0", result.ExitCode)
		}

		robotHelp = true
		result = dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 0 {
			t.Fatalf("successful dispatch exit code = %d, want 0", result.ExitCode)
		}
		if result.Err != nil {
			t.Fatalf("successful dispatch should not return error: %v", result.Err)
		}
		if result.AlreadyReported {
			t.Fatal("successful dispatch should not be marked reported")
		}
	})

	t.Run("handler error", func(t *testing.T) {
		var robotHelp = true
		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return errors.New("boom")
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 1 {
			t.Fatalf("error dispatch exit code = %d, want 1", result.ExitCode)
		}
		if result.Err == nil || !strings.Contains(result.Err.Error(), "boom") {
			t.Fatalf("error dispatch returned err = %v, want boom", result.Err)
		}
		if result.AlreadyReported {
			t.Fatal("plain handler errors should not be marked reported")
		}
	})

	t.Run("reported exit", func(t *testing.T) {
		var robotHelp = true
		registry := newRobotRegistry()
		registry.Register(RobotCommand{
			Name:     "robot-help",
			FlagName: "robot-help",
			FlagPtr:  &robotHelp,
			Handler: func(RobotContext) error {
				return newReportedRobotHandlerExit(2)
			},
		})

		result := dispatchRobotFlagResult(&registry, "robot-help", RobotContext{})
		if !result.Handled {
			t.Fatal("active flag should dispatch")
		}
		if result.ExitCode != 2 {
			t.Fatalf("reported dispatch exit code = %d, want 2", result.ExitCode)
		}
		if result.Err != nil {
			t.Fatalf("reported exit should not retain wrapped error: %v", result.Err)
		}
		if !result.AlreadyReported {
			t.Fatal("reported exit should preserve AlreadyReported")
		}
	})
}

func TestWriteRobotHelp_ReturnsWriterError(t *testing.T) {
	err := writeRobotHelp(failingWriter{err: errors.New("write failed")})
	if err == nil {
		t.Fatal("expected writer error")
	}
	if !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected wrapped writer error, got %v", err)
	}
}

func TestWriteRobotHelp_ReturnsWriterErrorAfterIntro(t *testing.T) {
	writer := &failAfterNWritesWriter{
		failAfter: 1,
		err:       errors.New("write failed after intro"),
	}

	err := writeRobotHelp(writer)
	if err == nil {
		t.Fatal("expected writer error after intro")
	}
	if !strings.Contains(err.Error(), "key bindings") {
		t.Fatalf("expected contextual error for later write, got %v", err)
	}
	if !strings.Contains(err.Error(), "write failed after intro") {
		t.Fatalf("expected underlying writer error, got %v", err)
	}
}

func TestFilterOrphanReportByMinScoreRebuildsDerivedFields(t *testing.T) {
	shaA := "aaaaaaa111111111111111111111111111111111"
	shaB := "aaaaaaa222222222222222222222222222222222"
	report := &correlation.OrphanReport{
		Stats: correlation.OrphanReportStats{
			CandidateCount: 3,
			AvgSuspicion:   65,
		},
		Candidates: []correlation.OrphanCandidate{
			{
				SHA:            shaA,
				ShortSHA:       "aaaaaaa",
				SuspicionScore: 90,
				ProbableBeads:  []correlation.ProbableBead{{BeadID: "bv-keep"}},
			},
			{
				SHA:            shaB,
				ShortSHA:       "aaaaaaa",
				SuspicionScore: 80,
				ProbableBeads:  []correlation.ProbableBead{{BeadID: "bv-keep"}},
			},
			{
				SHA:            "ccccccc333333333333333333333333333333333",
				ShortSHA:       "ccccccc",
				SuspicionScore: 70,
			},
			{
				SHA:            "bbbbbbb444444444444444444444444444444444",
				ShortSHA:       "bbbbbbb",
				SuspicionScore: 20,
				ProbableBeads:  []correlation.ProbableBead{{BeadID: "bv-drop"}},
			},
		},
		ByBead: map[string][]string{
			"bv-keep": {shaA, shaB},
			"bv-drop": {"bbbbbbb444444444444444444444444444444444"},
		},
	}

	filterOrphanReportByMinScore(report, 50)

	if len(report.Candidates) != 3 {
		t.Fatalf("candidate count = %d, want 3", len(report.Candidates))
	}
	if strings.Compare(report.Candidates[0].ShortSHA, "aaaaaaa") != 0 {
		t.Fatalf("candidate short SHA = %q, want aaaaaaa", report.Candidates[0].ShortSHA)
	}
	if report.Stats.CandidateCount != 2 {
		t.Fatalf("stats candidate count = %d, want 2 probable candidates", report.Stats.CandidateCount)
	}
	if report.Stats.AvgSuspicion != 80 {
		t.Fatalf("avg suspicion = %v, want 80", report.Stats.AvgSuspicion)
	}
	if got := report.ByBead["bv-keep"]; len(got) != 2 || got[0] != shaA || got[1] != shaB {
		t.Fatalf("kept by_bead entry = %#v, want both full collision-safe SHAs", got)
	}
	if dropped := report.ByBead["bv-drop"]; dropped != nil {
		t.Fatalf("dropped candidate still present in by_bead: %#v", dropped)
	}

	filterOrphanReportByMinScore(report, 101)
	if len(report.Candidates) != 0 {
		t.Fatalf("candidate count after filtering all = %d, want 0", len(report.Candidates))
	}
	if report.Stats.CandidateCount != 0 {
		t.Fatalf("stats candidate count after filtering all = %d, want 0", report.Stats.CandidateCount)
	}
	if report.Stats.AvgSuspicion != 0 {
		t.Fatalf("avg suspicion after filtering all = %v, want 0", report.Stats.AvgSuspicion)
	}
	if len(report.ByBead) != 0 {
		t.Fatalf("by_bead after filtering all = %#v, want empty", report.ByBead)
	}
}

func TestParseCorrelationArgTrimsAndRejectsEmptyParts(t *testing.T) {
	commitSHA, beadID, err := parseCorrelationArg("  abc123 : bv-1  ")
	if err != nil {
		t.Fatalf("parseCorrelationArg returned error: %v", err)
	}
	if commitSHA != "abc123" {
		t.Fatalf("commit SHA = %q, want abc123", commitSHA)
	}
	if beadID != "bv-1" {
		t.Fatalf("bead ID = %q, want bv-1", beadID)
	}

	tests := []string{
		"",
		"abc123",
		":bv-1",
		"abc123:",
		"   :   ",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, _, err := parseCorrelationArg(input); err == nil {
				t.Fatalf("parseCorrelationArg(%q) succeeded, want error", input)
			}
		})
	}
}

func TestResolveCorrelatedCommitRejectsAmbiguousPrefix(t *testing.T) {
	commits := []correlation.CorrelatedCommit{
		{SHA: "abc123def456", ShortSHA: "abc123d", Confidence: 0.8},
		{SHA: "abc123fff000", ShortSHA: "abc123f", Confidence: 0.7},
	}

	commit, err := resolveCorrelatedCommit(commits, "abc123d")
	if err != nil {
		t.Fatalf("resolveCorrelatedCommit returned error: %v", err)
	}
	if commit == nil || commit.SHA != "abc123def456" {
		t.Fatalf("resolved commit = %#v, want abc123def456", commit)
	}

	commit, err = resolveCorrelatedCommit(commits, "ABC123F")
	if err != nil {
		t.Fatalf("resolveCorrelatedCommit uppercase short SHA returned error: %v", err)
	}
	if commit == nil || commit.SHA != "abc123fff000" {
		t.Fatalf("uppercase resolved commit = %#v, want abc123fff000", commit)
	}

	commit, err = resolveCorrelatedCommit(commits, "abc123")
	if err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if commit != nil {
		t.Fatalf("commit = %#v, want nil on ambiguity", commit)
	}
	if !strings.Contains(err.Error(), "ambiguous commit SHA prefix") {
		t.Fatalf("error = %q, want ambiguity message", err.Error())
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failAfterNWritesWriter struct {
	failAfter int
	writes    int
	err       error
}

func (w *failAfterNWritesWriter) Write(p []byte) (int, error) {
	if w.writes >= w.failAfter {
		return 0, w.err
	}
	w.writes++
	return len(p), nil
}
