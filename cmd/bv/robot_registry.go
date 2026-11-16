package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	pflag "github.com/spf13/pflag"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/export"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/ui"
	"github.com/Dicklesworthstone/beads_viewer/pkg/version"
	"github.com/Dicklesworthstone/beads_viewer/pkg/workspace"
)

type RobotCommand struct {
	Name            string
	FlagName        string
	FlagPtr         interface{}
	RequiredCoFlags []string
	IsModifier      bool
	Handler         func(RobotContext) error
	Description     string
}

type RobotContext struct {
	Issues     []model.Issue
	FullIssues []model.Issue
	// PreRecipeIssues holds the repo/label-scoped issue slice captured right
	// before --recipe filtering so aggregate velocity metrics (e.g. under
	// --recipe actionable, which strips closed issues) retain closed-issue
	// history without exposing excluded issue IDs in candidate outputs.
	PreRecipeIssues []model.Issue
	// RecipeUnblockIDs restricts downstream unblock credit when --recipe is active
	// so domain-boundary filters (--label, --repo, tags, exclude_tags, id_prefix)
	// remain enforced while non-actionable downstream dependents in the same domain
	// remain eligible unblock targets.
	RecipeUnblockIDs map[string]bool
	// RecipeVelocityIssues retains closed history matching the recipe's domain
	// filters (tags, exclude_tags, priority, id_prefix, title_contains) without
	// leaking 1-hop cross-label context issues or unrelated domain closures.
	RecipeVelocityIssues   []model.Issue
	HasRecipeVelocityScope bool
	TombstoneIDs           []string
	ActiveRecipe           *recipe.Recipe
	DataHash               string
	// DataHashMatchesIssues is true when DataHash is the ComputeDataHash of the
	// exact Issues slice carried here (i.e. no label-scope or recipe filtering
	// changed Issues after DataHash was computed). When true, handlers may seed
	// analyzers with DataHash to avoid recomputing the identical hash.
	DataHashMatchesIssues bool
	Encoder               robotEncoder
	AsOf                  string
	AsOfCommit            string
	AsOfCommitTime        time.Time
	ForecastSprint        string
	LabelScope            string
	LabelContext          *analysis.LabelHealth
	Readiness             *model.ReadinessIndex
	CandidateIDs          map[string]bool
	// Command is the normalized robot flag being dispatched (set by
	// DispatchFlag) so Envelope can declare which scoping flags this command
	// cannot honour.
	Command string
	// SourcePath / SourceKind describe where Issues came from: the JSONL or
	// SQLite file selected by discovery, "<file>@<rev>" for --as-of, or the
	// workspace config. Every payload carries them so a consumer can see when a
	// different file than expected was analysed.
	SourcePath      string
	SourceKind      string
	SourceAuthority *RobotSourceAuthority
	// Recipe and Repo mirror the --recipe and --repo scoping already applied to
	// Issues (LabelScope covers --label).
	Recipe               string
	Repo                 string
	Stdout               io.Writer
	Stderr               io.Writer
	FinalizeBeforeExit   func()
	WorkDir              string
	ProjectDir           string
	BaselinePath         string
	HistoryLimit         int
	ForceFullAnalysis    bool
	EnvRobot             bool
	SearchOutput         *robotSearchOutput
	Diff                 *analysis.SnapshotDiff
	DiffHistoricalIssues []model.Issue
	DiffFromDataHash     string
	DiffResolvedRevision string
}

type RobotRegistry struct {
	commands []RobotCommand
}

var robotRegistry = newRobotRegistry()

type robotHandlerExitError struct {
	ExitCode        int
	Err             error
	AlreadyReported bool
}

type robotDispatchResult struct {
	Handled         bool
	ExitCode        int
	Err             error
	AlreadyReported bool
}

func (e *robotHandlerExitError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("robot handler exit %d", e.ExitCode)
}

func (e *robotHandlerExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type phaseOneRobotHandlerConfig struct {
	RobotHelpFlag         *bool
	RobotCapabilitiesFlag *bool
	RobotSchemaFlag       *bool
	RobotRecipesFlag      *bool
	RobotMetricsFlag      *bool
	RobotDocsFlag         *string
	VersionFlag           *bool
	SchemaCommand         *string
	RecipeLoader          func() *recipe.Loader
	StructuredOutput      func() bool
}

type phaseTwoRobotHandlerConfig struct {
	RobotPlanFlag       *bool
	RobotPriorityFlag   *bool
	RobotAlertsFlag     *bool
	RobotDriftFlag      *bool
	CheckDriftFlag      *bool
	RobotSuggestFlag    *bool
	RobotBurndownFlag   *string
	RobotForecastFlag   *string
	RobotSprintListFlag *bool
	RobotGraphFlag      *bool
	RobotSearchFlag     *bool
	RobotDiffFlag       *bool
	ForceFullAnalysis   *bool
	IncludeInactive     *bool
	GraphFormat         *string
	GraphRoot           *string
	GraphDepth          *int
	AlertSeverity       *string
	AlertType           *string
	AlertLabel          *string
	SuggestType         *string
	SuggestConfidence   *float64
	SuggestBead         *string
	RobotMinConf        *float64
	RobotMaxResults     *int
	RobotByLabel        *string
	RobotByAssignee     *string
	ForecastLabel       *string
	ForecastSprint      *string
	ForecastAgents      *int
}

type phaseThreeRobotHandlerConfig struct {
	RobotInsightsFlag       *bool
	RobotTriageFlag         *bool
	RobotTriageBriefFlag    *bool
	IncludeInactive         *bool
	RobotMaxResults         *int
	RobotTriageByTrackFlag  *bool
	RobotTriageByLabelFlag  *bool
	RobotNextFlag           *bool
	RobotHistoryFlag        *bool
	RobotLabelHealthFlag    *bool
	RobotLabelFlowFlag      *bool
	RobotLabelAttentionFlag *bool
	GraphRoot               *string // bv-140: scope triage to subgraph rooted at this issue
	BeadHistoryFlag         *string
	RobotExplainCorrFlag    *string
	RobotConfirmCorrFlag    *string
	RobotRejectCorrFlag     *string
	RobotIgnoreCorrFlag     *string
	RobotCorrStatsFlag      *bool
	CorrelationFeedbackBy   *string
	CorrelationReason       *string
	RobotOrphansFlag        *bool
	OrphansMinScore         *int
	RobotFileBeadsFlag      *string
	RobotCommitFlag         *string
	FileBeadsLimit          *int
	RobotFileHotspotsFlag   *bool
	HotspotsLimit           *int
	RobotImpactFlag         *string
	ForceFullAnalysis       *bool
	HistoryLimit            *int
	HistoryTimeoutMs        *int // #166: budget for the triage history prologue (-1 = unset, 0 = unbounded)
	HistorySince            *string
	MinConfidence           *float64
	RobotMinConf            *float64
	AttentionLimit          *int
	RelationsThreshold      *float64
	RelationsLimit          *int
	RelatedMinRelevance     *int
	RelatedMaxResults       *int
	RelatedIncludeClosed    *bool
	NetworkDepth            *int
	ForecastLabel           *string
	ForecastSprint          *string
	ForecastAgents          *int
	RobotFileRelationsFlag  *string
	RobotRelatedFlag        *string
	RobotBlockerChainFlag   *string
	RobotWhatIfFlag         *string
	RobotImpactNetworkFlag  *string
	RobotCausalityFlag      *string
	RobotSprintShowFlag     *string
	RobotCapacityFlag       *bool
	CapacityAgents          *int
	CapacityLabel           *string
	// NotReadyLabels (issue #173): opt-in label-class excluded from claimable
	// top picks. Resolved from --robot-not-ready-labels (comma-separated) with a
	// BV_ROBOT_NOT_READY_LABELS env fallback; nil/empty disables the gate.
	NotReadyLabels *string
}

func newRobotRegistry() RobotRegistry {
	return RobotRegistry{}
}

// ReferenceNow returns the reference instant for staleness, deferral, velocity,
// and forecast evaluation in this dispatch context. Pinned SOURCE_DATE_EPOCH
// overrides everything for byte-for-byte reproducibility; otherwise --as-of
// uses the resolved git commit timestamp so historical snapshots are evaluated
// as of that commit rather than wall-clock time.
func (ctx RobotContext) ReferenceNow() time.Time {
	if sourceDateEpochActive() {
		return robotNow()
	}
	if !ctx.AsOfCommitTime.IsZero() {
		return ctx.AsOfCommitTime.UTC()
	}
	return robotNow()
}

// VelocityIssues returns the issue slice to use for aggregate closure-velocity
// and historical ETA baselines. When --recipe filtered out closed issues (e.g.
// --recipe actionable), RecipeVelocityIssues preserves closed history matching
// the recipe's domain filters without leaking cross-label or cross-tag issues.
func (ctx RobotContext) VelocityIssues() []model.Issue {
	if ctx.CandidateIDs != nil && len(ctx.CandidateIDs) == 0 {
		return []model.Issue{}
	}
	if ctx.HasRecipeVelocityScope {
		return ctx.RecipeVelocityIssues
	}
	if ctx.ActiveRecipe != nil && len(ctx.PreRecipeIssues) > 0 {
		return ctx.PreRecipeIssues
	}
	return ctx.Issues
}

// GraphIssues returns the underlying topology slice for graph-metric, triage,
// plan, alert, forecast, and what-if evaluation. When --recipe narrowed
// CandidateIDs to a non-empty subset, PreRecipeIssues retains downstream
// blocked dependents and multi-hop structure while CandidateIDs +
// RecipeUnblockIDs restrict emitted candidates and unblock targets.
func (ctx RobotContext) GraphIssues() []model.Issue {
	if ctx.CandidateIDs != nil && len(ctx.CandidateIDs) == 0 {
		return ctx.Issues
	}
	if ctx.ActiveRecipe != nil && len(ctx.PreRecipeIssues) > 0 {
		return ctx.PreRecipeIssues
	}
	return ctx.Issues
}

// GraphAnalyzer builds an Analyzer over GraphIssues() with full-source
// readiness, candidate scoping, and recipe unblock-target scoping applied.
func (ctx RobotContext) GraphAnalyzer() *analysis.Analyzer {
	analyzer := analysis.NewAnalyzer(ctx.GraphIssues())
	analyzer.SetReadinessScope(ctx.Readiness, ctx.CandidateIDs)
	if ctx.RecipeUnblockIDs != nil {
		analyzer.SetUnblockScope(ctx.RecipeUnblockIDs)
	}
	analyzer.SetNow(ctx.ReferenceNow())
	return analyzer
}

// Analyzer uses the visible graph for metrics and the retained full source for
// readiness. Every registry handler must keep these two scopes distinct.
func (ctx RobotContext) Analyzer() *analysis.Analyzer {
	analyzer := analysis.NewAnalyzer(ctx.Issues)
	analyzer.SetReadinessScope(ctx.Readiness, ctx.CandidateIDs)
	if ctx.RecipeUnblockIDs != nil {
		analyzer.SetUnblockScope(ctx.RecipeUnblockIDs)
	}
	analyzer.SetNow(ctx.ReferenceNow())
	return analyzer
}

// Envelope builds the shared robot payload header for this dispatch: data
// hash, source, time-travel metadata, and active scoping.
func (ctx RobotContext) Envelope() RobotEnvelope {
	hash := ctx.DataHash
	if hash == "" && len(ctx.Issues) > 0 {
		hash = analysis.ComputeDataHash(ctx.Issues)
	}
	return ctx.EnvelopeWithHash(hash)
}

// EnvelopeWithHash is Envelope for handlers whose payload hashes a derived
// issue set (a history report, a scoped subgraph) rather than ctx.Issues.
func (ctx RobotContext) EnvelopeWithHash(dataHash string) RobotEnvelope {
	env := NewRobotEnvelope(dataHash)
	env.SourcePath = ctx.SourcePath
	env.SourceKind = ctx.SourceKind
	env.SourceAuthority = ctx.SourceAuthority
	if env.SourceAuthority == nil {
		env.SourceAuthority = newRobotSourceAuthority(nil)
	}
	env.AuthorityHash = robotAuthorityHash(env.SourceAuthority)
	env.LoadStats = robotLoadStats(ctx.SourceAuthority)
	ids := make([]string, 0, len(ctx.Issues))
	for _, issue := range ctx.Issues {
		if ctx.CandidateIDs == nil || ctx.CandidateIDs[issue.ID] {
			ids = append(ids, issue.ID)
		}
	}
	sort.Strings(ids)
	env.ScopeHash = robotScopeHash(ctx.LabelScope, ctx.Recipe, ctx.Repo, dataHash, ids)
	env.AsOf = ctx.AsOf
	env.AsOfCommit = ctx.AsOfCommit
	unsupported := unsupportedScopeFor(ctx.Command, ctx)
	if ctx.LabelScope != "" || ctx.Recipe != "" || ctx.Repo != "" || len(unsupported) > 0 {
		env.Scope = &RobotScope{
			Label:       ctx.LabelScope,
			Recipe:      ctx.Recipe,
			Repo:        ctx.Repo,
			Unsupported: unsupported,
		}
	}
	return env
}

func (ctx RobotContext) claimsProven() bool {
	return ctx.SourceAuthority != nil && ctx.SourceAuthority.ClaimSafe
}

func suppressUnprovenTriageClaims(triage *analysis.TriageResult) {
	triage.Commands.ClaimTop = ""
	triage.QuickRef.TopPicks = []analysis.TopPick{}
	triage.QuickWins = []analysis.QuickWin{}
	suppressRecommendations := func(recs []analysis.Recommendation) {
		for i := range recs {
			recs[i].Claimable = false
			recs[i].Actions.Claim = nil
			recs[i].Actions.UnavailableReason = "source authority is incomplete or stale"
			recs[i].Action = "Inspect source diagnostics before claiming work"
		}
	}
	suppressRecommendations(triage.Recommendations)
	for i := range triage.BlockersToClear {
		triage.BlockersToClear[i].Actionable = false
	}
	for i := range triage.RecommendationsByTrack {
		group := &triage.RecommendationsByTrack[i]
		group.TopPick, group.ClaimCommand = nil, ""
		suppressRecommendations(group.Recommendations)
	}
	for i := range triage.RecommendationsByLabel {
		group := &triage.RecommendationsByLabel[i]
		group.TopPick, group.ClaimCommand = nil, ""
		suppressRecommendations(group.Recommendations)
	}
}

// withEnvelope overlays the shared envelope onto a payload that already
// defines some of the same top-level keys (generated_at, data_hash) — for
// example the correlation package's result structs. Embedding both would make
// encoding/json drop the colliding keys silently; overlaying keeps every
// payload key and lets the envelope supply source, scope, and as_of metadata.
func withEnvelope(env RobotEnvelope, payload any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding payload: %w", err)
	}
	merged := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &merged); err != nil {
		return nil, fmt.Errorf("payload is not a JSON object: %w", err)
	}
	envRaw, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("encoding envelope: %w", err)
	}
	var envFields map[string]json.RawMessage
	if err := json.Unmarshal(envRaw, &envFields); err != nil {
		return nil, fmt.Errorf("envelope is not a JSON object: %w", err)
	}
	for k, v := range envFields {
		merged[k] = v
	}
	return merged, nil
}

// unsupportedScopeFor lists the scoping flags a command cannot honour for the
// given context. Commands that walk live git history or read sprint files from
// disk cannot be time-travelled with --as-of; declaring that beats silently
// answering from the wrong point in time.
func unsupportedScopeFor(command string, ctx RobotContext) []string {
	switch normalizeRobotFlagName(command) {
	case "robot-sprint-list", "robot-sprint-show":
		var out []string
		if ctx.AsOf != "" {
			out = append(out, "as_of")
		}
		if ctx.LabelScope != "" {
			out = append(out, "label")
		}
		if ctx.Recipe != "" {
			out = append(out, "recipe")
		}
		return out
	case "robot-forecast":
		if ctx.AsOf != "" && strings.TrimSpace(ctx.ForecastSprint) != "" {
			return []string{"as_of"}
		}
		return nil
	case "robot-history", "bead-history", "robot-orphans", "robot-file-beads", "robot-commit", "robot-file-hotspots",
		"robot-file-relations", "robot-impact-network", "robot-related",
		"robot-explain-correlation", "robot-confirm-correlation", "robot-reject-correlation", "robot-ignore-correlation",
		"robot-correlation-stats", "robot-impact",
		"robot-burndown":
		if ctx.AsOf != "" {
			return []string{"as_of"}
		}
	case "robot-causality":
		if ctx.AsOf != "" && ctx.AsOfCommit == "" {
			return []string{"as_of"}
		}
	}
	return nil
}

func (ctx RobotContext) StdoutOrDefault() io.Writer {
	if ctx.Stdout != nil {
		return ctx.Stdout
	}
	return os.Stdout
}

func (ctx RobotContext) StderrOrDefault() io.Writer {
	if ctx.Stderr != nil {
		return ctx.Stderr
	}
	return os.Stderr
}

func (ctx RobotContext) EncoderOrDefault() robotEncoder {
	if ctx.Encoder != nil {
		return ctx.Encoder
	}
	return newRobotEncoder(ctx.StdoutOrDefault())
}

func (ctx RobotContext) WorkDirOrDefault() (string, error) {
	if strings.TrimSpace(ctx.WorkDir) != "" {
		return ctx.WorkDir, nil
	}
	return os.Getwd()
}

func (ctx RobotContext) ProjectDirOrDefault() (string, error) {
	if strings.TrimSpace(ctx.ProjectDir) != "" {
		return ctx.ProjectDir, nil
	}
	return ctx.WorkDirOrDefault()
}

func (ctx RobotContext) BaselinePathOrDefault() (string, error) {
	if strings.TrimSpace(ctx.BaselinePath) != "" {
		return ctx.BaselinePath, nil
	}
	projectDir, err := ctx.ProjectDirOrDefault()
	if err != nil {
		return "", err
	}
	return baseline.DefaultPath(projectDir), nil
}

func (r *RobotRegistry) Register(cmd RobotCommand) {
	cmd.Name = strings.TrimSpace(cmd.Name)
	cmd.FlagName = normalizeRobotFlagName(cmd.FlagName)
	cmd.RequiredCoFlags = normalizeRobotFlagNames(cmd.RequiredCoFlags)

	if cmd.Name == "" {
		panic("robot command name must not be empty")
	}
	if cmd.FlagName == "" {
		panic("robot command flag name must not be empty")
	}
	if cmd.FlagPtr == nil {
		panic(fmt.Sprintf("robot command %q has nil FlagPtr", cmd.Name))
	}
	for _, existing := range r.commands {
		if strings.EqualFold(existing.Name, cmd.Name) {
			panic(fmt.Sprintf("robot command %q registered twice", cmd.Name))
		}
		if strings.EqualFold(existing.FlagName, cmd.FlagName) {
			panic(fmt.Sprintf("robot flag %q registered twice", formatRobotFlag(cmd.FlagName)))
		}
	}

	r.commands = append(r.commands, cmd)
}

func (r *RobotRegistry) AnyActive() bool {
	for _, cmd := range r.commands {
		if robotFlagActive(cmd.FlagPtr) {
			return true
		}
	}
	return false
}

// AnyRobotModeActive reports whether any primary robot command registered in r
// (excluding non-robot metadata commands like --version and modifiers) is active.
func (r *RobotRegistry) AnyRobotModeActive() bool {
	if r == nil {
		return false
	}
	for _, cmd := range r.commands {
		if cmd.IsModifier || cmd.FlagName == "version" {
			continue
		}
		if robotFlagActive(cmd.FlagPtr) {
			return true
		}
	}
	return false
}

// AllFlagNames returns the normalized CLI flag names registered in r
// (excluding modifiers) in deterministic sorted order.
func (r *RobotRegistry) AllFlagNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		if cmd.IsModifier {
			continue
		}
		names = append(names, cmd.FlagName)
	}
	sort.Strings(names)
	return names
}

// PrimaryRobotFlagNames returns the normalized CLI flag names of primary robot
// commands in r (excluding modifiers and --version) in deterministic sorted order.
func (r *RobotRegistry) PrimaryRobotFlagNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.commands))
	for _, cmd := range r.commands {
		if cmd.IsModifier || cmd.FlagName == "version" {
			continue
		}
		names = append(names, cmd.FlagName)
	}
	sort.Strings(names)
	return names
}

func anyRobotModeActive(registries ...*RobotRegistry) bool {
	for _, reg := range registries {
		if reg != nil && reg.AnyRobotModeActive() {
			return true
		}
	}
	return false
}

func allPrimaryRobotFlagNames(registries ...*RobotRegistry) []string {
	seen := make(map[string]struct{})
	for _, reg := range registries {
		for _, name := range reg.PrimaryRobotFlagNames() {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// configuredPrimaryRobotFlagNames constructs an isolated dry-run copy of the
// Phase 1/2/3 robot registries so callers (including primaryRobotFlagNames()
// and contract tests) derive the complete primary command set from a single
// registration source of truth.
func configuredPrimaryRobotFlagNames() []string {
	var dummyBool bool
	var dummyStr string
	var dummyInt int
	var dummyFloat float64

	r1 := newRobotRegistry()
	registerPhaseOneRobotHandlers(&r1, phaseOneRobotHandlerConfig{
		RobotHelpFlag:         &dummyBool,
		RobotCapabilitiesFlag: &dummyBool,
		RobotSchemaFlag:       &dummyBool,
		RobotRecipesFlag:      &dummyBool,
		RobotMetricsFlag:      &dummyBool,
		RobotDocsFlag:         &dummyStr,
		VersionFlag:           &dummyBool,
		SchemaCommand:         &dummyStr,
	})
	r2 := newRobotRegistry()
	registerPhaseTwoRobotHandlers(&r2, phaseTwoRobotHandlerConfig{
		RobotPlanFlag:       &dummyBool,
		RobotPriorityFlag:   &dummyBool,
		RobotAlertsFlag:     &dummyBool,
		RobotDriftFlag:      &dummyBool,
		CheckDriftFlag:      &dummyBool,
		RobotSuggestFlag:    &dummyBool,
		RobotBurndownFlag:   &dummyStr,
		RobotForecastFlag:   &dummyStr,
		RobotSprintListFlag: &dummyBool,
		RobotGraphFlag:      &dummyBool,
		RobotSearchFlag:     &dummyBool,
		RobotDiffFlag:       &dummyBool,
		ForceFullAnalysis:   &dummyBool,
		IncludeInactive:     &dummyBool,
		GraphFormat:         &dummyStr,
		GraphRoot:           &dummyStr,
		GraphDepth:          &dummyInt,
		AlertSeverity:       &dummyStr,
		AlertType:           &dummyStr,
		AlertLabel:          &dummyStr,
		SuggestType:         &dummyStr,
		SuggestConfidence:   &dummyFloat,
		SuggestBead:         &dummyStr,
		RobotMinConf:        &dummyFloat,
		RobotMaxResults:     &dummyInt,
		RobotByLabel:        &dummyStr,
		RobotByAssignee:     &dummyStr,
		ForecastLabel:       &dummyStr,
		ForecastSprint:      &dummyStr,
		ForecastAgents:      &dummyInt,
	})
	r3 := newRobotRegistry()
	registerPhaseThreeRobotHandlers(&r3, phaseThreeRobotHandlerConfig{
		RobotInsightsFlag:       &dummyBool,
		RobotTriageFlag:         &dummyBool,
		RobotTriageBriefFlag:    &dummyBool,
		IncludeInactive:         &dummyBool,
		RobotMaxResults:         &dummyInt,
		RobotTriageByTrackFlag:  &dummyBool,
		RobotTriageByLabelFlag:  &dummyBool,
		RobotNextFlag:           &dummyBool,
		RobotHistoryFlag:        &dummyBool,
		RobotLabelHealthFlag:    &dummyBool,
		RobotLabelFlowFlag:      &dummyBool,
		RobotLabelAttentionFlag: &dummyBool,
		GraphRoot:               &dummyStr,
		BeadHistoryFlag:         &dummyStr,
		RobotExplainCorrFlag:    &dummyStr,
		RobotConfirmCorrFlag:    &dummyStr,
		RobotRejectCorrFlag:     &dummyStr,
		RobotIgnoreCorrFlag:     &dummyStr,
		RobotCorrStatsFlag:      &dummyBool,
		CorrelationFeedbackBy:   &dummyStr,
		CorrelationReason:       &dummyStr,
		RobotOrphansFlag:        &dummyBool,
		OrphansMinScore:         &dummyInt,
		RobotFileBeadsFlag:      &dummyStr,
		RobotCommitFlag:         &dummyStr,
		FileBeadsLimit:          &dummyInt,
		RobotFileHotspotsFlag:   &dummyBool,
		HotspotsLimit:           &dummyInt,
		RobotImpactFlag:         &dummyStr,
		ForceFullAnalysis:       &dummyBool,
		HistoryLimit:            &dummyInt,
		HistoryTimeoutMs:        &dummyInt,
		HistorySince:            &dummyStr,
		MinConfidence:           &dummyFloat,
		RobotMinConf:            &dummyFloat,
		AttentionLimit:          &dummyInt,
		RelationsThreshold:      &dummyFloat,
		RelationsLimit:          &dummyInt,
		RelatedMinRelevance:     &dummyInt,
		RelatedMaxResults:       &dummyInt,
		RelatedIncludeClosed:    &dummyBool,
		NetworkDepth:            &dummyInt,
		ForecastLabel:           &dummyStr,
		ForecastSprint:          &dummyStr,
		ForecastAgents:          &dummyInt,
		RobotFileRelationsFlag:  &dummyStr,
		RobotRelatedFlag:        &dummyStr,
		RobotBlockerChainFlag:   &dummyStr,
		RobotWhatIfFlag:         &dummyStr,
		RobotImpactNetworkFlag:  &dummyStr,
		RobotCausalityFlag:      &dummyStr,
		RobotSprintShowFlag:     &dummyStr,
		RobotCapacityFlag:       &dummyBool,
		CapacityAgents:          &dummyInt,
		CapacityLabel:           &dummyStr,
		NotReadyLabels:          &dummyStr,
	})
	return allPrimaryRobotFlagNames(&r1, &r2, &r3)
}

func (r *RobotRegistry) ActiveCommands() []RobotCommand {
	active := make([]RobotCommand, 0, len(r.commands))
	for _, cmd := range r.commands {
		if robotFlagActive(cmd.FlagPtr) {
			active = append(active, cmd)
		}
	}
	return active
}

func (r *RobotRegistry) DispatchFlag(flagName string, ctx RobotContext) (bool, error) {
	normalized := normalizeRobotFlagName(flagName)
	if normalized == "" {
		return false, nil
	}

	for _, cmd := range r.commands {
		if cmd.FlagName != normalized || !robotFlagActive(cmd.FlagPtr) {
			continue
		}
		if cmd.Handler == nil {
			return true, fmt.Errorf("robot command %q has no handler", cmd.Name)
		}
		ctx.Command = normalized
		return true, cmd.Handler(ctx)
	}

	return false, nil
}

func dispatchRobotFlagResult(registry *RobotRegistry, flagName string, ctx RobotContext) robotDispatchResult {
	if registry == nil {
		return robotDispatchResult{}
	}

	handled, err := registry.DispatchFlag(flagName, ctx)
	if !handled {
		return robotDispatchResult{}
	}
	result := robotDispatchResult{Handled: true}
	if err == nil {
		return result
	}

	result.ExitCode = 1
	var handlerErr *robotHandlerExitError
	if errors.As(err, &handlerErr) {
		if handlerErr.ExitCode != 0 {
			result.ExitCode = handlerErr.ExitCode
		}
		result.AlreadyReported = handlerErr.AlreadyReported
		err = handlerErr.Err
	}
	result.Err = err

	return result
}

func dispatchRobotFlagOrExit(registry *RobotRegistry, flagName string, ctx RobotContext) {
	result := dispatchRobotFlagResult(registry, flagName, ctx)
	if !result.Handled {
		return
	}

	// Only emit an "Error handling …" line when the handler actually failed
	// (non-zero exit or a returned err) AND that failure has not already been
	// reported by the handler itself. Previously we printed the error banner
	// on every success path too; callers that use cmd.CombinedOutput (like
	// TestCLIFlagCompatibility) saw it merged into stdout and treated the
	// output as invalid JSON.
	if !result.AlreadyReported && (result.Err != nil || result.ExitCode != 0) {
		if result.Err != nil {
			fmt.Fprintf(ctx.StderrOrDefault(), "Error handling %s: %v\n", formatRobotFlag(flagName), result.Err)
		} else {
			fmt.Fprintf(ctx.StderrOrDefault(), "Error handling %s\n", formatRobotFlag(flagName))
		}
	}
	if ctx.FinalizeBeforeExit != nil {
		ctx.FinalizeBeforeExit()
	}

	os.Exit(result.ExitCode)
}

func newReportedRobotHandlerExit(exitCode int) error {
	return &robotHandlerExitError{
		ExitCode:        exitCode,
		AlreadyReported: true,
	}
}

// writeRobotHelp outputs the robot help documentation.
// robotHelpRegistries holds every populated registry so --robot-help can list
// all commands. main sets it right after registering the phase handlers; a nil
// slice (unit tests, early failures) still renders the intro and key bindings.
var robotHelpRegistries []*RobotRegistry

func writeRobotHelp(out io.Writer) error {
	return writeRobotHelpFromRegistries(out, robotHelpRegistries...)
}

// writeRobotHelpFromRegistries renders --robot-help. The command list is
// generated from the registries so a new robot command is documented the
// moment it is registered; the hand-written list it replaced covered six of
// forty commands.
func writeRobotHelpFromRegistries(out io.Writer, registries ...*RobotRegistry) error {
	if out == nil {
		out = os.Stdout
	}

	writeln := func(args ...any) error {
		if _, err := fmt.Fprintln(out, args...); err != nil {
			return err
		}
		return nil
	}
	writef := func(format string, args ...any) error {
		if _, err := fmt.Fprintf(out, format, args...); err != nil {
			return err
		}
		return nil
	}

	_, err := io.WriteString(out, `bv (Beads Viewer) AI Agent Interface
====================================
Use --robot-* flags for deterministic automation output.
Bare bv launches the interactive TUI.

Start here:
  --robot-triage        Unified triage output (recommended entry point)
  --robot-next          Single top recommendation
  --robot-capabilities  Machine-readable command/contract manifest
  --robot-schema        JSON Schema definitions for robot outputs
  --robot-docs <topic>  Long-form agent documentation

Every payload carries: generated_at, data_hash, source_path, source_kind,
as_of/as_of_commit (with --as-of), scope (with --label/--recipe/--repo, plus
scope.unsupported for flags a command cannot honour), load_stats (when records
were dropped during load).
Issue-backed responses also carry source_authority, authority_hash, and
scope_hash. Partial or stale sources retain exploratory output with provisional
readiness; source_authority.claim_safe must be true before claiming work.

`)
	if err != nil {
		return fmt.Errorf("writing robot help intro: %w", err)
	}

	// Every registered command, generated so the list cannot drift from the
	// registry. Modifiers (flags that only adjust another command) are listed
	// under their own heading.
	if err := writeln("All robot commands:"); err != nil {
		return fmt.Errorf("writing robot help commands heading: %w", err)
	}
	if err := writeln("-------------------"); err != nil {
		return fmt.Errorf("writing robot help commands divider: %w", err)
	}
	var modifiers []RobotCommand
	seen := make(map[string]bool)
	for _, reg := range registries {
		if reg == nil {
			continue
		}
		for _, cmd := range reg.commands {
			if seen[cmd.FlagName] {
				continue
			}
			seen[cmd.FlagName] = true
			if cmd.IsModifier {
				modifiers = append(modifiers, cmd)
				continue
			}
			if err := writef("  %-28s %s\n", formatRobotFlag(cmd.FlagName), cmd.Description); err != nil {
				return fmt.Errorf("writing robot help command %q: %w", cmd.FlagName, err)
			}
		}
	}
	if len(modifiers) > 0 {
		if err := writeln("\nModifiers (combine with a command above):"); err != nil {
			return fmt.Errorf("writing robot help modifiers heading: %w", err)
		}
		for _, cmd := range modifiers {
			if err := writef("  %-28s %s\n", formatRobotFlag(cmd.FlagName), cmd.Description); err != nil {
				return fmt.Errorf("writing robot help modifier %q: %w", cmd.FlagName, err)
			}
		}
	}
	if err := writeln(); err != nil {
		return fmt.Errorf("writing robot help commands spacer: %w", err)
	}

	// Key bindings table (bv-xl6g)
	if err := writeln("TUI Key Bindings:"); err != nil {
		return fmt.Errorf("writing robot help key bindings heading: %w", err)
	}
	if err := writeln("-----------------"); err != nil {
		return fmt.Errorf("writing robot help key bindings divider: %w", err)
	}
	bindings := ui.GetKeyBindingDocs()

	// Group by category
	categories := make(map[string][]ui.KeyBindingDoc)
	categoryOrder := []string{}
	for _, b := range bindings {
		if _, exists := categories[b.Category]; !exists {
			categoryOrder = append(categoryOrder, b.Category)
		}
		categories[b.Category] = append(categories[b.Category], b)
	}

	for _, cat := range categoryOrder {
		if err := writef("\n[%s]\n", cat); err != nil {
			return fmt.Errorf("writing robot help category %q: %w", cat, err)
		}
		for _, b := range categories[cat] {
			if err := writef("  %-12s %-25s (%s)\n", b.Key, b.Desc, b.Context); err != nil {
				return fmt.Errorf("writing robot help binding %q: %w", b.Key, err)
			}
		}
	}

	if err := writeln(); err != nil {
		return fmt.Errorf("writing robot help footer spacer: %w", err)
	}
	if err := writeln("Run bv --help for all options."); err != nil {
		return fmt.Errorf("writing robot help footer: %w", err)
	}
	return nil
}

func registerPhaseOneRobotHandlers(registry *RobotRegistry, cfg phaseOneRobotHandlerConfig) {
	if registry == nil {
		panic("robot registry must not be nil")
	}

	registry.Register(RobotCommand{
		Name:        "robot-help",
		FlagName:    "robot-help",
		FlagPtr:     cfg.RobotHelpFlag,
		Description: "Show AI agent help",
		Handler: func(ctx RobotContext) error {
			if cfg.StructuredOutput != nil && cfg.StructuredOutput() {
				if err := ctx.EncoderOrDefault().Encode(generateRobotDocs("guide")); err != nil {
					return fmt.Errorf("encoding robot help: %w", err)
				}
				return nil
			}
			if err := writeRobotHelp(ctx.StdoutOrDefault()); err != nil {
				return fmt.Errorf("writing robot help: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "version",
		FlagName:    "version",
		FlagPtr:     cfg.VersionFlag,
		Description: "Show version",
		Handler: func(ctx RobotContext) error {
			if cfg.StructuredOutput != nil && cfg.StructuredOutput() {
				if err := ctx.EncoderOrDefault().Encode(map[string]interface{}{
					"generated_at":  robotNow().Format(time.RFC3339),
					"tool":          "bv",
					"version":       version.Version,
					"output_format": robotOutputFormat,
				}); err != nil {
					return fmt.Errorf("encoding version output: %w", err)
				}
				return nil
			}
			_, err := fmt.Fprintf(ctx.StdoutOrDefault(), "bv %s\n", version.Version)
			if err != nil {
				return fmt.Errorf("writing version output: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "robot-capabilities",
		FlagName:    "robot-capabilities",
		FlagPtr:     cfg.RobotCapabilitiesFlag,
		Description: "Output machine-readable command capabilities",
		Handler: func(ctx RobotContext) error {
			if err := ctx.EncoderOrDefault().Encode(generateRobotCapabilities()); err != nil {
				return fmt.Errorf("encoding robot capabilities: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "robot-recipes",
		FlagName:    "robot-recipes",
		FlagPtr:     cfg.RobotRecipesFlag,
		Description: "Output recipe summaries for AI agents",
		Handler: func(ctx RobotContext) error {
			var loader *recipe.Loader
			if cfg.RecipeLoader != nil {
				loader = cfg.RecipeLoader()
			}
			if loader == nil {
				return fmt.Errorf("recipe loader not initialized")
			}

			summaries := loader.ListSummaries()
			sort.Slice(summaries, func(i, j int) bool {
				return summaries[i].Name < summaries[j].Name
			})

			output := struct {
				GeneratedAt  string                 `json:"generated_at"`
				OutputFormat string                 `json:"output_format,omitempty"`
				Version      string                 `json:"version,omitempty"`
				Recipes      []recipe.RecipeSummary `json:"recipes"`
			}{
				GeneratedAt:  robotNow().Format(time.RFC3339),
				OutputFormat: robotOutputFormat,
				Version:      version.Version,
				Recipes:      summaries,
			}

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding recipes: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "robot-schema",
		FlagName:    "robot-schema",
		FlagPtr:     cfg.RobotSchemaFlag,
		Description: "Output JSON schema definitions for robot commands",
		Handler: func(ctx RobotContext) error {
			schemas := generateRobotSchemas()

			if cfg.SchemaCommand != nil && strings.TrimSpace(*cfg.SchemaCommand) != "" {
				commandName := strings.TrimSpace(*cfg.SchemaCommand)
				if schema, ok := schemas.Commands[commandName]; ok {
					singleOutput := map[string]interface{}{
						"schema_version": schemas.SchemaVersion,
						"generated_at":   schemas.GeneratedAt,
						"command":        commandName,
						"schema":         schema,
					}
					if err := ctx.EncoderOrDefault().Encode(singleOutput); err != nil {
						return fmt.Errorf("encoding schema: %w", err)
					}
					return nil
				}

				fmt.Fprintf(ctx.StderrOrDefault(), "Unknown command: %s\n", commandName)
				commandNames := make([]string, 0, len(schemas.Commands))
				for name := range schemas.Commands {
					commandNames = append(commandNames, name)
				}
				sort.Strings(commandNames)
				if suggestion := suggestClosest(commandName, commandNames); suggestion != "" {
					fmt.Fprintf(ctx.StderrOrDefault(), "Did you mean: %s\n", suggestion)
				}
				fmt.Fprintln(ctx.StderrOrDefault(), "Available commands:")
				for _, name := range commandNames {
					fmt.Fprintf(ctx.StderrOrDefault(), "  %s\n", name)
				}
				return newReportedRobotHandlerExit(1)
			}

			if err := ctx.EncoderOrDefault().Encode(schemas); err != nil {
				return fmt.Errorf("encoding schemas: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "robot-metrics",
		FlagName:    "robot-metrics",
		FlagPtr:     cfg.RobotMetricsFlag,
		Description: "Output runtime performance metrics",
		Handler: func(ctx RobotContext) error {
			if len(ctx.Issues) > 0 {
				analyzer := ctx.Analyzer()
				if ctx.DataHashMatchesIssues {
					analyzer.SeedDataHash(ctx.DataHash)
				}
				if ctx.ForceFullAnalysis {
					fullCfg := analysis.FullAnalysisConfig()
					analyzer.SetConfig(&fullCfg)
				}
				stats := analyzer.Analyze()
				stats.WaitForPhase2()
			}
			snapshot := metrics.GetAllMetrics()
			output := struct {
				RobotEnvelope
				Timing []metrics.TimingStats `json:"timing,omitempty"`
				Cache  []metrics.CacheStats  `json:"cache,omitempty"`
				Memory metrics.MemoryStats   `json:"memory"`
			}{
				RobotEnvelope: ctx.Envelope(),
				Timing:        snapshot.Timing,
				Cache:         snapshot.Cache,
				Memory:        snapshot.Memory,
			}
			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding metrics: %w", err)
			}
			return nil
		},
	})
	registry.Register(RobotCommand{
		Name:        "robot-docs",
		FlagName:    "robot-docs",
		FlagPtr:     cfg.RobotDocsFlag,
		Description: "Output machine-readable robot documentation",
		Handler: func(ctx RobotContext) error {
			topic := ""
			if cfg.RobotDocsFlag != nil {
				topic = strings.TrimSpace(*cfg.RobotDocsFlag)
			}

			docs := generateRobotDocs(topic)
			if err := ctx.EncoderOrDefault().Encode(docs); err != nil {
				return fmt.Errorf("encoding robot-docs: %w", err)
			}
			if _, hasErr := docs["error"]; hasErr {
				return newReportedRobotHandlerExit(2)
			}
			return nil
		},
	})
}

func registerPhaseTwoRobotHandlers(registry *RobotRegistry, cfg phaseTwoRobotHandlerConfig) {
	if registry == nil {
		panic("robot registry must not be nil")
	}

	registry.Register(RobotCommand{
		Name:        "robot-plan",
		FlagName:    "robot-plan",
		FlagPtr:     cfg.RobotPlanFlag,
		Description: "Output dependency-respecting execution plan",
		Handler: func(ctx RobotContext) error {
			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			if ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
				analyzer.SeedDataHash(ctx.DataHash)
			}
			config := analysis.ConfigForSize(len(graphIssues), countEdges(graphIssues))
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				config = analysis.FullAnalysisConfig()
			} else {
				const skipReason = "not computed for --robot-plan"
				config.ComputePageRank = false
				config.PageRankSkipReason = skipReason
				config.ComputeBetweenness = false
				config.BetweennessMode = analysis.BetweennessSkip
				config.BetweennessSkipReason = skipReason
				config.ComputeHITS = false
				config.HITSSkipReason = skipReason
				config.ComputeEigenvector = false
				config.ComputeCriticalPath = false
				config.ComputeCycles = false
				config.CyclesSkipReason = skipReason
			}

			plan := analyzer.GetExecutionPlan()
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
				remaining := *cfg.RobotMaxResults
				cappedTracks := make([]analysis.ExecutionTrack, 0, len(plan.Tracks))
				for _, track := range plan.Tracks {
					if remaining <= 0 {
						break
					}
					if len(track.Items) > remaining {
						track.Items = track.Items[:remaining]
					}
					if len(track.Items) > 0 {
						cappedTracks = append(cappedTracks, track)
						remaining -= len(track.Items)
					}
				}
				plan.Tracks = cappedTracks
			}
			stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
			stats.WaitForPhase2()

			output := struct {
				RobotEnvelope
				AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
				Status         analysis.MetricStatus   `json:"status"`
				LabelScope     string                  `json:"label_scope,omitempty"`
				LabelContext   *analysis.LabelHealth   `json:"label_context,omitempty"`
				Plan           analysis.ExecutionPlan  `json:"plan"`
				UsageHints     []string                `json:"usage_hints"`
			}{
				RobotEnvelope:  ctx.Envelope(),
				AnalysisConfig: config,
				Status:         stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:     ctx.LabelScope,
				LabelContext:   ctx.LabelContext,
				Plan:           plan,
				UsageHints: []string{
					"jq '.plan.tracks | length' - Number of parallel execution tracks",
					"jq '.plan.tracks[0].items | map(.id)' - First track item IDs",
					"jq '.plan.tracks[].items[] | select(.unblocks | length > 0)' - Items that unblock others",
					"jq '.plan.summary' - High-level execution summary",
					"jq '[.plan.tracks[].items[]] | length' - Total items across all tracks",
				},
			}

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding execution plan: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-priority",
		FlagName:    "robot-priority",
		FlagPtr:     cfg.RobotPriorityFlag,
		Description: "Output enhanced priority recommendations",
		Handler: func(ctx RobotContext) error {
			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			if _, w := loadRobotFeedbackForContext(ctx); w != nil {
				analyzer.SetWeights(*w)
			}
			if ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
				analyzer.SeedDataHash(ctx.DataHash)
			}
			config := analysis.ConfigForSize(len(graphIssues), countEdges(graphIssues))
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				config = analysis.FullAnalysisConfig()
			}
			analyzer.SetConfig(&config)
			stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
			stats.WaitForPhase2()

			includeInactive := cfg.IncludeInactive != nil && *cfg.IncludeInactive
			recommendations := analyzer.GenerateEnhancedRecommendationsFromStats(stats, analysis.DefaultThresholds(), 0, includeInactive)
			filtered := make([]analysis.EnhancedPriorityRecommendation, 0, len(recommendations))
			issueMap := make(map[string]model.Issue, len(graphIssues))
			for _, issue := range graphIssues {
				issueMap[issue.ID] = issue
			}
			for _, rec := range recommendations {
				if cfg.RobotMinConf != nil && *cfg.RobotMinConf > 0 && rec.Confidence < *cfg.RobotMinConf {
					continue
				}
				if cfg.RobotByLabel != nil && strings.TrimSpace(*cfg.RobotByLabel) != "" {
					issue, ok := issueMap[rec.IssueID]
					if !ok {
						continue
					}
					hasLabel := false
					for _, label := range issue.Labels {
						if label == *cfg.RobotByLabel {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						continue
					}
				}
				if cfg.RobotByAssignee != nil && strings.TrimSpace(*cfg.RobotByAssignee) != "" {
					issue, ok := issueMap[rec.IssueID]
					if !ok || issue.Assignee != *cfg.RobotByAssignee {
						continue
					}
				}
				filtered = append(filtered, rec)
			}
			recommendations = filtered

			maxResults := 10
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
				maxResults = *cfg.RobotMaxResults
			}
			if len(recommendations) > maxResults {
				recommendations = recommendations[:maxResults]
			}
			if recommendations == nil {
				recommendations = []analysis.EnhancedPriorityRecommendation{}
			}

			highConfidence := 0
			for _, rec := range recommendations {
				if rec.Confidence >= 0.7 {
					highConfidence++
				}
			}

			output := struct {
				RobotEnvelope
				AnalysisConfig    analysis.AnalysisConfig                   `json:"analysis_config"`
				Status            analysis.MetricStatus                     `json:"status"`
				LabelScope        string                                    `json:"label_scope,omitempty"`
				LabelContext      *analysis.LabelHealth                     `json:"label_context,omitempty"`
				Recommendations   []analysis.EnhancedPriorityRecommendation `json:"recommendations"`
				FieldDescriptions map[string]string                         `json:"field_descriptions"`
				Filters           struct {
					MinConfidence float64 `json:"min_confidence,omitempty"`
					MaxResults    int     `json:"max_results"`
					ByLabel       string  `json:"by_label,omitempty"`
					ByAssignee    string  `json:"by_assignee,omitempty"`
				} `json:"filters"`
				Summary struct {
					TotalIssues     int `json:"total_issues"`
					Recommendations int `json:"recommendations"`
					HighConfidence  int `json:"high_confidence"`
				} `json:"summary"`
				Usage []string `json:"usage_hints"`
			}{
				RobotEnvelope:     ctx.Envelope(),
				AnalysisConfig:    config,
				Status:            stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:        ctx.LabelScope,
				LabelContext:      ctx.LabelContext,
				Recommendations:   recommendations,
				FieldDescriptions: analysis.DefaultFieldDescriptions(),
				Usage: []string{
					"jq '.recommendations[] | select(.confidence > 0.7)' - Filter high confidence",
					"jq '.recommendations[] | {id: .issue_id, score: .impact_score, prio: .suggested_priority}' - Extract essentials",
					"jq '.summary' - Overview counts",
				},
			}
			if cfg.RobotMinConf != nil && *cfg.RobotMinConf > 0 {
				output.Filters.MinConfidence = *cfg.RobotMinConf
			}
			if cfg.RobotByLabel != nil && strings.TrimSpace(*cfg.RobotByLabel) != "" {
				output.Filters.ByLabel = *cfg.RobotByLabel
			}
			if cfg.RobotByAssignee != nil && strings.TrimSpace(*cfg.RobotByAssignee) != "" {
				output.Filters.ByAssignee = *cfg.RobotByAssignee
			}
			output.Filters.MaxResults = maxResults
			output.Summary.TotalIssues = analyzer.CountStatusPartition().Total
			output.Summary.Recommendations = len(recommendations)
			output.Summary.HighConfidence = highConfidence

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding priority recommendations: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-graph",
		FlagName:    "robot-graph",
		FlagPtr:     cfg.RobotGraphFlag,
		Description: "Output dependency graph in JSON, DOT, or Mermaid",
		Handler: func(ctx RobotContext) error {
			analyzer := ctx.Analyzer()
			analyzer.SetNow(ctx.ReferenceNow())
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				fullCfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&fullCfg)
			}
			stats := analyzer.Analyze()

			format := export.GraphFormatJSON
			if cfg.GraphFormat != nil {
				switch strings.ToLower(strings.TrimSpace(*cfg.GraphFormat)) {
				case "dot":
					format = export.GraphFormatDOT
				case "mermaid":
					format = export.GraphFormatMermaid
				}
			}

			config := export.GraphExportConfig{
				Format:   format,
				DataHash: ctx.DataHash,
			}
			if cfg.GraphRoot != nil {
				config.Root = *cfg.GraphRoot
			}
			if cfg.GraphDepth != nil {
				config.Depth = *cfg.GraphDepth
			}

			result, err := export.ExportGraph(ctx.Issues, &stats, config)
			if err != nil {
				return fmt.Errorf("exporting graph: %w", err)
			}
			// The loader already selected the label subgraph, including direct
			// dependency context. Filtering by label again would erase its edges.
			if ctx.LabelScope != "" {
				if result.FiltersApplied == nil {
					result.FiltersApplied = make(map[string]string)
				}
				result.FiltersApplied["label"] = ctx.LabelScope
			}
			// Same keys as before plus the shared envelope (source, scope,
			// as_of). GraphExportResult carries its own data_hash, so copy
			// fields instead of embedding two structs that both define it.
			output := struct {
				RobotEnvelope
				Format         string                  `json:"format"`
				Graph          string                  `json:"graph,omitempty"`
				Nodes          int                     `json:"nodes"`
				Edges          int                     `json:"edges"`
				FiltersApplied map[string]string       `json:"filters_applied,omitempty"`
				Explanation    export.GraphExplanation `json:"explanation"`
				Adjacency      *export.AdjacencyGraph  `json:"adjacency,omitempty"`
			}{
				RobotEnvelope:  ctx.Envelope(),
				Format:         result.Format,
				Graph:          result.Graph,
				Nodes:          result.Nodes,
				Edges:          result.Edges,
				FiltersApplied: result.FiltersApplied,
				Explanation:    result.Explanation,
				Adjacency:      result.Adjacency,
			}
			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding graph: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-alerts",
		FlagName:    "robot-alerts",
		FlagPtr:     cfg.RobotAlertsFlag,
		Description: "Output drift and proactive alerts",
		Handler: func(ctx RobotContext) error {
			projectDir, err := ctx.ProjectDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting project directory: %w", err)
			}
			baselinePath, err := ctx.BaselinePathOrDefault()
			if err != nil {
				return fmt.Errorf("getting baseline path: %w", err)
			}

			driftConfig, err := drift.LoadConfig(projectDir)
			if err != nil {
				return fmt.Errorf("loading drift config: %w", err)
			}

			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				fullCfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&fullCfg)
			}
			stats := analyzer.Analyze()

			partition := analyzer.CountStatusPartition()
			openCount := 0
			for _, issue := range graphIssues {
				if !analyzer.IsCandidate(issue.ID) {
					continue
				}
				if issue.Status == model.StatusOpen || issue.Status == model.StatusInProgress {
					openCount++
				}
			}
			cycles := filterCyclesForCandidates(stats.Cycles(), ctx.CandidateIDs)
			scopedNodes, scopedEdges, scopedDensity := scopedGraphTotalsForCLI(graphIssues, &stats, ctx.CandidateIDs)
			curStats := baseline.GraphStats{
				NodeCount:       scopedNodes,
				EdgeCount:       scopedEdges,
				Density:         scopedDensity,
				OpenCount:       openCount,
				ClosedCount:     partition.Closed,
				BlockedCount:    partition.Blocked,
				CycleCount:      len(cycles),
				ActionableCount: partition.Actionable,
			}

			bl := &baseline.Baseline{Stats: curStats}
			cur := &baseline.Baseline{Stats: curStats, Cycles: cycles}
			if baseline.Exists(baselinePath) {
				loaded, err := baseline.Load(baselinePath)
				if err != nil {
					if !ctx.EnvRobot {
						fmt.Fprintf(ctx.StderrOrDefault(), "Warning: Error loading baseline: %v\n", err)
					}
				} else {
					bl = loaded
					topMetrics := baseline.TopMetrics{
						PageRank:     buildScopedMetricItems(stats.PageRank(), 10, ctx.CandidateIDs),
						Betweenness:  buildScopedMetricItems(stats.Betweenness(), 10, ctx.CandidateIDs),
						CriticalPath: buildScopedMetricItems(stats.CriticalPathScore(), 10, ctx.CandidateIDs),
						Hubs:         buildScopedMetricItems(stats.Hubs(), 10, ctx.CandidateIDs),
						Authorities:  buildScopedMetricItems(stats.Authorities(), 10, ctx.CandidateIDs),
					}
					cur = &baseline.Baseline{Stats: curStats, TopMetrics: topMetrics, Cycles: cycles}
				}
			}

			calc := drift.NewCalculator(bl, cur, driftConfig)
			calc.SetNow(analyzer.Now())
			calc.SetIssues(graphIssues)
			if !calc.ReuseAnalyzer(analyzer) {
				return fmt.Errorf("preparing alerts: issue rows do not match the scoped analyzer")
			}
			attachSourceDiffsForContext(calc, ctx, driftConfig.SourceDriftMaxAlerts)
			driftResult := calc.Calculate()

			filtered := driftResult.Alerts[:0]
			for _, alert := range driftResult.Alerts {
				if cfg.AlertSeverity != nil && strings.TrimSpace(*cfg.AlertSeverity) != "" && string(alert.Severity) != *cfg.AlertSeverity {
					continue
				}
				if cfg.AlertType != nil && strings.TrimSpace(*cfg.AlertType) != "" && string(alert.Type) != *cfg.AlertType {
					continue
				}
				if cfg.AlertLabel != nil && strings.TrimSpace(*cfg.AlertLabel) != "" {
					want := strings.ToLower(strings.TrimSpace(*cfg.AlertLabel))
					found := false
					for _, label := range alert.Labels {
						if strings.ToLower(label) == want {
							found = true
							break
						}
					}
					if !found && alert.Label != "" && strings.ToLower(alert.Label) == want {
						found = true
					}
					if !found {
						for _, detail := range alert.Details {
							if strings.Contains(strings.ToLower(detail), want) {
								found = true
								break
							}
						}
					}
					if !found {
						continue
					}
				}
				filtered = append(filtered, alert)
			}
			driftResult.Alerts = filtered
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(driftResult.Alerts) > *cfg.RobotMaxResults {
				driftResult.Alerts = driftResult.Alerts[:*cfg.RobotMaxResults]
			}

			output := struct {
				RobotEnvelope
				Alerts  []drift.Alert `json:"alerts"`
				Summary struct {
					Total    int `json:"total"`
					Critical int `json:"critical"`
					Warning  int `json:"warning"`
					Info     int `json:"info"`
				} `json:"summary"`
				SkippedChecks []drift.SkippedCheck `json:"skipped_checks,omitempty"`
				UsageHints    []string             `json:"usage_hints"`
			}{
				RobotEnvelope: ctx.Envelope(),
				Alerts:        driftResult.Alerts,
				SkippedChecks: driftResult.SkippedChecks,
				UsageHints: []string{
					"--severity=warning --alert-type=stale_issue   # stale warnings only",
					"--alert-type=blocking_cascade                 # high-unblock opportunities",
					"--alert-type=high_impact_unblock|abandoned_claim|potential_duplicate|priority_mismatch|velocity_drop|source_inconsistency   # proactive checks (no baseline needed)",
					"--alert-type=new_cycle|density_growth|node_count_change|edge_count_change|scope_creep|blocked_increase|actionable_change|pagerank_change   # drift vs saved baseline (bv --save-baseline)",
					"--alert-label=backend                        # only alerts on issues carrying that label",
					"jq '.alerts | map({issue_id, type, suggested_action})'   # what to do about each",
					"thresholds: .bv/drift.yaml; every key and its default is listed in the README 'Alerts System' table",
				},
			}
			for _, alert := range driftResult.Alerts {
				switch alert.Severity {
				case drift.SeverityCritical:
					output.Summary.Critical++
				case drift.SeverityWarning:
					output.Summary.Warning++
				case drift.SeverityInfo:
					output.Summary.Info++
				}
				output.Summary.Total++
			}

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding alerts: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:            "robot-drift",
		FlagName:        "robot-drift",
		FlagPtr:         cfg.RobotDriftFlag,
		RequiredCoFlags: []string{"check-drift"},
		Description:     "Output drift check from saved baseline as JSON (with --check-drift)",
		Handler: func(ctx RobotContext) error {
			projectDir, err := ctx.ProjectDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting project directory: %w", err)
			}
			baselinePath, err := ctx.BaselinePathOrDefault()
			if err != nil {
				return fmt.Errorf("getting baseline path: %w", err)
			}
			if !baseline.Exists(baselinePath) {
				fmt.Fprintln(ctx.StderrOrDefault(), "Error: No baseline found.")
				fmt.Fprintln(ctx.StderrOrDefault(), "Create one with: bv --save-baseline \"description\"")
				return newReportedRobotHandlerExit(1)
			}

			bl, err := baseline.Load(baselinePath)
			if err != nil {
				fmt.Fprintf(ctx.StderrOrDefault(), "Error loading baseline: %v\n", err)
				return newReportedRobotHandlerExit(1)
			}

			driftNow := ctx.ReferenceNow()
			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			analyzer.SetNow(driftNow)
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				fullCfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&fullCfg)
			}
			stats := analyzer.Analyze()

			partition := analyzer.CountStatusPartition()
			openCount := 0
			for _, issue := range graphIssues {
				if !analyzer.IsCandidate(issue.ID) {
					continue
				}
				if issue.Status == model.StatusOpen || issue.Status == model.StatusInProgress {
					openCount++
				}
			}
			cycles := filterCyclesForCandidates(stats.Cycles(), ctx.CandidateIDs)
			scopedNodes, scopedEdges, scopedDensity := scopedGraphTotalsForCLI(graphIssues, &stats, ctx.CandidateIDs)
			currentStats := baseline.GraphStats{
				NodeCount:       scopedNodes,
				EdgeCount:       scopedEdges,
				Density:         scopedDensity,
				OpenCount:       openCount,
				ClosedCount:     partition.Closed,
				BlockedCount:    partition.Blocked,
				CycleCount:      len(cycles),
				ActionableCount: partition.Actionable,
			}
			currentMetrics := baseline.TopMetrics{
				PageRank:     buildScopedMetricItems(stats.PageRank(), 10, ctx.CandidateIDs),
				Betweenness:  buildScopedMetricItems(stats.Betweenness(), 10, ctx.CandidateIDs),
				CriticalPath: buildScopedMetricItems(stats.CriticalPathScore(), 10, ctx.CandidateIDs),
				Hubs:         buildScopedMetricItems(stats.Hubs(), 10, ctx.CandidateIDs),
				Authorities:  buildScopedMetricItems(stats.Authorities(), 10, ctx.CandidateIDs),
			}
			current := baseline.NewForDir(projectDir, currentStats, currentMetrics, cycles, "current")

			driftConfig, err := drift.LoadConfig(projectDir)
			if err != nil {
				if !ctx.EnvRobot {
					fmt.Fprintf(ctx.StderrOrDefault(), "Warning: Error loading drift config: %v\n", err)
				}
				driftConfig = drift.DefaultConfig()
			}

			calc := drift.NewCalculator(bl, current, driftConfig)
			calc.SetNow(driftNow)
			calc.SetIssues(graphIssues)
			if !calc.ReuseAnalyzer(analyzer) {
				return fmt.Errorf("preparing drift check: issue rows do not match the scoped analyzer")
			}
			attachSourceDiffsForContext(calc, ctx, driftConfig.SourceDriftMaxAlerts)
			result := calc.Calculate()
			alerts := result.Alerts
			if alerts == nil {
				alerts = []drift.Alert{}
			}

			output := struct {
				RobotEnvelope
				HasDrift bool `json:"has_drift"`
				ExitCode int  `json:"exit_code"`
				Summary  struct {
					Total    int `json:"total"`
					Critical int `json:"critical"`
					Warning  int `json:"warning"`
					Info     int `json:"info"`
				} `json:"summary"`
				Alerts        []drift.Alert        `json:"alerts"`
				SkippedChecks []drift.SkippedCheck `json:"skipped_checks,omitempty"`
				Baseline      struct {
					CreatedAt string `json:"created_at"`
					CommitSHA string `json:"commit_sha,omitempty"`
				} `json:"baseline"`
			}{
				RobotEnvelope: ctx.Envelope(),
				HasDrift:      result.HasDrift,
				ExitCode:      result.ExitCode(),
				Alerts:        alerts,
				SkippedChecks: result.SkippedChecks,
			}
			output.Summary.Total = len(alerts)
			output.Summary.Critical = result.CriticalCount
			output.Summary.Warning = result.WarningCount
			output.Summary.Info = result.InfoCount
			output.Baseline.CreatedAt = bl.CreatedAt.Format(time.RFC3339)
			output.Baseline.CommitSHA = bl.CommitSHA

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding drift result: %w", err)
			}
			if exitCode := result.ExitCode(); exitCode != 0 {
				return newReportedRobotHandlerExit(exitCode)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-suggest",
		FlagName:    "robot-suggest",
		FlagPtr:     cfg.RobotSuggestFlag,
		Description: "Output smart suggestions",
		Handler: func(ctx RobotContext) error {
			config := analysis.DefaultSuggestAllConfig()
			if cfg.SuggestConfidence != nil {
				config.MinConfidence = *cfg.SuggestConfidence
			}
			if cfg.RobotMinConf != nil && *cfg.RobotMinConf > config.MinConfidence {
				config.MinConfidence = *cfg.RobotMinConf
			}
			if cfg.SuggestBead != nil {
				config.FilterBead = *cfg.SuggestBead
			}
			maxSuggestions := config.MaxSuggestions
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
				maxSuggestions = *cfg.RobotMaxResults
				config.MaxSuggestions = maxSuggestions
			}
			if ctx.CandidateIDs != nil {
				config.CandidateIDs = ctx.CandidateIDs
				// Defer the cap until after scoping to CandidateIDs so out-of-scope
				// context issues cannot crowd out in-scope suggestions.
				config.MaxSuggestions = 0
			}

			suggestType := ""
			if cfg.SuggestType != nil {
				suggestType = strings.TrimSpace(*cfg.SuggestType)
			}
			switch strings.ToLower(suggestType) {
			case "duplicate", "duplicates", string(analysis.SuggestionPotentialDuplicate):
				config.FilterType = analysis.SuggestionPotentialDuplicate
			case "dependency", "dependencies", string(analysis.SuggestionMissingDependency):
				config.FilterType = analysis.SuggestionMissingDependency
			case "label", "labels", string(analysis.SuggestionLabelSuggestion):
				config.FilterType = analysis.SuggestionLabelSuggestion
			case "cycle", "cycles", string(analysis.SuggestionCycleWarning):
				config.FilterType = analysis.SuggestionCycleWarning
			case "":
			default:
				fmt.Fprintf(ctx.StderrOrDefault(), "Invalid suggest-type: %s (use: duplicate, dependency, label, cycle; for stale issues use: bv --robot-alerts --alert-type=stale_issue)\n", suggestType)
				return newReportedRobotHandlerExit(1)
			}

			suggest := analysis.GenerateRobotSuggestOutputAt(labelAnalysisIssuesForContext(ctx), config, ctx.DataHash, ctx.ReferenceNow())
			rebuildSet := false
			if maxSuggestions > 0 && len(suggest.Set.Suggestions) > maxSuggestions {
				suggest.Set.Suggestions = suggest.Set.Suggestions[:maxSuggestions]
				rebuildSet = true
			}
			if !ctx.claimsProven() || ctx.AsOf != "" {
				reason := "source authority is incomplete or unknown"
				if ctx.AsOf != "" {
					reason = "historical snapshots cannot authorize tracker mutations"
				}
				for i, suggestion := range suggest.Set.Suggestions {
					suggest.Set.Suggestions[i] = suggestion.WithAction(nil).WithMetadata("action_unavailable_reason", reason)
				}
				rebuildSet = true
			}
			if rebuildSet {
				suggest.Set = analysis.NewSuggestionSetAt(suggest.Set.Suggestions, suggest.Set.DataHash, suggest.Set.GeneratedAt)
			}
			// Re-host the payload under the shared envelope (same top-level
			// keys plus source/scope metadata). Embedding the analysis struct
			// directly would collide on generated_at/data_hash, which
			// encoding/json resolves by dropping both.
			output := struct {
				RobotEnvelope
				Filters    analysis.SuggestFilter `json:"filters"`
				Set        analysis.SuggestionSet `json:"suggestions"`
				UsageHints []string               `json:"usage_hints"`
			}{
				RobotEnvelope: ctx.Envelope(),
				Filters:       suggest.Filters,
				Set:           suggest.Set,
				UsageHints:    suggest.UsageHints,
			}
			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding suggestions: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-sprint-list",
		FlagName:    "robot-sprint-list",
		FlagPtr:     cfg.RobotSprintListFlag,
		Description: "Output all sprints as JSON",
		Handler: func(ctx RobotContext) error {
			workDir, err := ctx.WorkDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			sprints, err := loadSprintsForContext(ctx, workDir)
			if err != nil {
				return fmt.Errorf("loading sprints: %w", err)
			}

			output := struct {
				RobotEnvelope
				SprintCount int            `json:"sprint_count"`
				Sprints     []model.Sprint `json:"sprints"`
			}{
				RobotEnvelope: ctx.Envelope(),
				SprintCount:   len(sprints),
				Sprints:       sprints,
			}
			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding sprints: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-burndown",
		FlagName:    "robot-burndown",
		FlagPtr:     cfg.RobotBurndownFlag,
		Description: "Output sprint burndown as JSON",
		Handler: func(ctx RobotContext) error {
			workDir, err := ctx.WorkDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			sprints, err := loadSprintsForContext(ctx, workDir)
			if err != nil {
				return fmt.Errorf("loading sprints: %w", err)
			}

			target := ""
			if cfg.RobotBurndownFlag != nil {
				target = *cfg.RobotBurndownFlag
			}

			now := ctx.ReferenceNow()
			var targetSprint *model.Sprint
			if target == "current" {
				for i := range sprints {
					if sprints[i].IsActiveAt(now) {
						targetSprint = &sprints[i]
						break
					}
				}
				if targetSprint == nil {
					fmt.Fprintln(ctx.StderrOrDefault(), "No active sprint found")
					return newReportedRobotHandlerExit(1)
				}
			} else {
				targetSprint = matchSprintByID(sprints, target)
				if targetSprint == nil {
					fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", target)
					return newReportedRobotHandlerExit(1)
				}
			}

			projectDir, _ := ctx.ProjectDirOrDefault()
			driftCfg, cfgErr := drift.LoadConfig(projectDir)
			if cfgErr != nil || driftCfg == nil {
				driftCfg = drift.DefaultConfig()
			}
			scopedSprint := *targetSprint
			if ctx.CandidateIDs != nil {
				filteredBeads := make([]string, 0, len(scopedSprint.BeadIDs))
				for _, bid := range scopedSprint.BeadIDs {
					if ctx.CandidateIDs[bid] {
						filteredBeads = append(filteredBeads, bid)
					}
				}
				scopedSprint.BeadIDs = filteredBeads
			}
			burndownIssues := ctx.GraphIssues()
			if len(ctx.FullIssues) > 0 {
				burndownIssues = labelAnalysisIssuesForContext(ctx)
			}
			burndown := calculateBurndownAtWithThresholds(&scopedSprint, burndownIssues, now, driftCfg.AtRiskThresholds())
			burndown.RobotEnvelope = ctx.Envelope()
			issueMap := make(map[string]model.Issue, len(burndownIssues))
			for _, issue := range burndownIssues {
				issueMap[issue.ID] = issue
			}
			if scopeChanges, err := computeSprintScopeChangesForContext(ctx, workDir, targetSprint, issueMap, now); err == nil && len(scopeChanges) > 0 {
				if ctx.CandidateIDs != nil {
					filteredChanges := scopeChanges[:0]
					for _, ev := range scopeChanges {
						if ctx.CandidateIDs[ev.IssueID] {
							filteredChanges = append(filteredChanges, ev)
						}
					}
					scopeChanges = filteredChanges
				}
				if len(scopeChanges) > 0 {
					burndown.ScopeChanges = scopeChanges
					// Re-linearize the ideal trajectory at each scope change.
					burndown.IdealLine = generateIdealLineScoped(&scopedSprint, burndown.TotalIssues, scopeChanges)
				}
			}

			if err := ctx.EncoderOrDefault().Encode(burndown); err != nil {
				return fmt.Errorf("encoding burndown: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-forecast",
		FlagName:    "robot-forecast",
		FlagPtr:     cfg.RobotForecastFlag,
		Description: "Output ETA forecasts as JSON",
		Handler: func(ctx RobotContext) error {
			workDir, err := ctx.WorkDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}

			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				fullCfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&fullCfg)
			}
			graphStats := analyzer.Analyze()

			targetIssues := make([]model.Issue, 0, len(graphIssues))
			var sprintBeadIDs map[string]bool
			if cfg.ForecastSprint != nil && strings.TrimSpace(*cfg.ForecastSprint) != "" {
				sprints, err := loadSprintsForContext(ctx, workDir)
				if err == nil {
					if sprint := matchSprintByID(sprints, *cfg.ForecastSprint); sprint != nil {
						sprintBeadIDs = make(map[string]bool, len(sprint.BeadIDs))
						for _, beadID := range sprint.BeadIDs {
							sprintBeadIDs[beadID] = true
						}
					}
				}
				if sprintBeadIDs == nil {
					fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", *cfg.ForecastSprint)
					return newReportedRobotHandlerExit(1)
				}
			}

			for _, issue := range graphIssues {
				if !analyzer.IsCandidate(issue.ID) {
					continue
				}
				if cfg.ForecastLabel != nil && strings.TrimSpace(*cfg.ForecastLabel) != "" {
					wantLabel := strings.TrimSpace(*cfg.ForecastLabel)
					hasLabel := false
					for _, label := range issue.Labels {
						if strings.EqualFold(strings.TrimSpace(label), wantLabel) {
							hasLabel = true
							break
						}
					}
					if !hasLabel {
						continue
					}
				}
				if sprintBeadIDs != nil && !sprintBeadIDs[issue.ID] {
					continue
				}
				targetIssues = append(targetIssues, issue)
			}

			now := ctx.ReferenceNow()
			agents := 1
			if cfg.ForecastAgents != nil && *cfg.ForecastAgents > 0 {
				agents = *cfg.ForecastAgents
			}

			type ForecastSummary struct {
				TotalMinutes  int       `json:"total_minutes"`
				TotalDays     float64   `json:"total_days"`
				AvgConfidence float64   `json:"avg_confidence"`
				EarliestETA   time.Time `json:"earliest_eta"`
				LatestETA     time.Time `json:"latest_eta"`
			}
			type ForecastOutput struct {
				RobotEnvelope
				Agents        int                    `json:"agents"`
				Filters       map[string]string      `json:"filters,omitempty"`
				ForecastCount int                    `json:"forecast_count"`
				Forecasts     []analysis.ETAEstimate `json:"forecasts"`
				Summary       *ForecastSummary       `json:"summary,omitempty"`
			}

			forecastTarget := ""
			if cfg.RobotForecastFlag != nil {
				forecastTarget = *cfg.RobotForecastFlag
			}

			etaIssues := ctx.VelocityIssues()
			includeInactive := cfg.IncludeInactive != nil && *cfg.IncludeInactive
			forecasts := make([]analysis.ETAEstimate, 0)
			if forecastTarget == "all" {
				for _, issue := range targetIssues {
					if issue.Status.IsClosed() || issue.Status.IsTombstone() {
						continue
					}
					if !includeInactive && (issue.IsTemplate || issue.Pinned || issue.Status == model.StatusPinned) {
						continue
					}
					eta, err := analysis.EstimateETAForIssue(etaIssues, &graphStats, issue.ID, agents, now)
					if err != nil {
						continue
					}
					forecasts = append(forecasts, eta)
				}
				sort.Slice(forecasts, func(i, j int) bool {
					if !forecasts[i].ETADate.Equal(forecasts[j].ETADate) {
						return forecasts[i].ETADate.Before(forecasts[j].ETADate)
					}
					return forecasts[i].IssueID < forecasts[j].IssueID
				})
				if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(forecasts) > *cfg.RobotMaxResults {
					forecasts = forecasts[:*cfg.RobotMaxResults]
				}
			} else {
				selected := false
				for _, issue := range targetIssues {
					if issue.ID == forecastTarget {
						selected = true
						break
					}
				}
				if !selected {
					fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found in selected forecast scope: %s\n", forecastTarget)
					return newReportedRobotHandlerExit(1)
				}
				eta, err := analysis.EstimateETAForIssue(etaIssues, &graphStats, forecastTarget, agents, now)
				if err != nil {
					fmt.Fprintf(ctx.StderrOrDefault(), "Error: %v\n", err)
					return newReportedRobotHandlerExit(1)
				}
				forecasts = append(forecasts, eta)
			}

			var summary *ForecastSummary
			if len(forecasts) > 1 {
				totalMinutes := 0
				totalConfidence := 0.0
				earliest := forecasts[0].ETADate
				latest := forecasts[0].ETADate
				for _, forecast := range forecasts {
					totalMinutes += forecast.EstimatedMinutes
					totalConfidence += forecast.Confidence
					if forecast.ETADate.Before(earliest) {
						earliest = forecast.ETADate
					}
					if forecast.ETADate.After(latest) {
						latest = forecast.ETADate
					}
				}
				summary = &ForecastSummary{
					TotalMinutes:  totalMinutes,
					TotalDays:     float64(totalMinutes) / (60.0 * 8.0),
					AvgConfidence: totalConfidence / float64(len(forecasts)),
					EarliestETA:   earliest,
					LatestETA:     latest,
				}
			}

			filters := make(map[string]string)
			if cfg.ForecastLabel != nil && strings.TrimSpace(*cfg.ForecastLabel) != "" {
				filters["label"] = *cfg.ForecastLabel
			}
			if cfg.ForecastSprint != nil && strings.TrimSpace(*cfg.ForecastSprint) != "" {
				filters["sprint"] = *cfg.ForecastSprint
			}

			output := ForecastOutput{
				RobotEnvelope: ctx.Envelope(),
				Agents:        agents,
				ForecastCount: len(forecasts),
				Forecasts:     forecasts,
				Summary:       summary,
			}
			if len(filters) > 0 {
				output.Filters = filters
			}

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding forecast: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-search",
		FlagName:    "robot-search",
		FlagPtr:     cfg.RobotSearchFlag,
		Description: "Output keyword or hybrid search results as JSON",
		Handler: func(ctx RobotContext) error {
			if ctx.SearchOutput == nil {
				return fmt.Errorf("robot search output not initialized")
			}
			if err := writeRobotSearchOutput(ctx.StdoutOrDefault(), *ctx.SearchOutput); err != nil {
				return fmt.Errorf("encoding robot-search: %w", err)
			}
			return nil
		},
	})

	registry.Register(RobotCommand{
		Name:        "robot-diff",
		FlagName:    "robot-diff",
		FlagPtr:     cfg.RobotDiffFlag,
		Description: "Output snapshot diff as JSON",
		Handler: func(ctx RobotContext) error {
			if ctx.Diff == nil {
				return fmt.Errorf("diff output not initialized")
			}
			// The live-side snapshot is normally created with a wall-clock
			// timestamp before dispatch. Copy it before normalizing the nested
			// robot timestamp so reproducible output does not mutate caller state.
			diff := *ctx.Diff
			diff.ToTimestamp = ctx.ReferenceNow()
			fromDataHash := ctx.DiffFromDataHash
			if fromDataHash == "" {
				fromDataHash = analysis.ComputeDataHash(ctx.DiffHistoricalIssues)
			}
			output := struct {
				RobotEnvelope
				ResolvedRevision string                 `json:"resolved_revision"`
				FromDataHash     string                 `json:"from_data_hash"`
				ToDataHash       string                 `json:"to_data_hash"`
				Diff             *analysis.SnapshotDiff `json:"diff"`
			}{
				RobotEnvelope:    ctx.Envelope(),
				ResolvedRevision: ctx.DiffResolvedRevision,
				FromDataHash:     fromDataHash,
				ToDataHash:       ctx.DataHash,
				Diff:             &diff,
			}

			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
				return fmt.Errorf("encoding diff: %w", err)
			}
			return nil
		},
	})
}

func registerPhaseThreeRobotHandlers(registry *RobotRegistry, cfg phaseThreeRobotHandlerConfig) {
	if registry == nil {
		panic("robot registry must not be nil")
	}

	register := func(name string, flagPtr interface{}, description string, handler func(RobotContext) error) {
		registry.Register(RobotCommand{
			Name:        name,
			FlagName:    name,
			FlagPtr:     flagPtr,
			Description: description,
			Handler:     handler,
		})
	}

	register("robot-insights", cfg.RobotInsightsFlag, "Output deep graph analysis and insights", func(ctx RobotContext) error {
		return handleRobotInsights(ctx, cfg)
	})
	register("robot-next", cfg.RobotNextFlag, "Output only the single top recommendation", func(ctx RobotContext) error {
		return handleRobotNext(ctx, cfg)
	})
	register("robot-triage", cfg.RobotTriageFlag, "Output unified triage as JSON", func(ctx RobotContext) error {
		return handleRobotTriage(ctx, cfg)
	})
	register("robot-triage-by-track", cfg.RobotTriageByTrackFlag, "Output triage grouped by execution track", func(ctx RobotContext) error {
		return handleRobotTriage(ctx, cfg)
	})
	register("robot-triage-by-label", cfg.RobotTriageByLabelFlag, "Output triage grouped by label", func(ctx RobotContext) error {
		return handleRobotTriage(ctx, cfg)
	})
	register("robot-history", cfg.RobotHistoryFlag, "Output bead-to-commit correlations as JSON", func(ctx RobotContext) error {
		return handleRobotHistory(ctx, cfg)
	})
	register("bead-history", cfg.BeadHistoryFlag, "Output history for a specific bead as JSON", func(ctx RobotContext) error {
		return handleRobotHistory(ctx, cfg)
	})
	register("robot-correlation-stats", cfg.RobotCorrStatsFlag, "Output correlation feedback statistics as JSON", func(ctx RobotContext) error {
		return handleRobotCorrelationStats(ctx)
	})
	register("robot-explain-correlation", cfg.RobotExplainCorrFlag, "Explain why a commit is linked to a bead", func(ctx RobotContext) error {
		return handleRobotExplainCorrelation(ctx, cfg)
	})
	register("robot-confirm-correlation", cfg.RobotConfirmCorrFlag, "Confirm a correlation is correct", func(ctx RobotContext) error {
		return handleRobotCorrelationFeedback(ctx, cfg, correlation.FeedbackConfirm)
	})
	register("robot-reject-correlation", cfg.RobotRejectCorrFlag, "Reject an incorrect correlation", func(ctx RobotContext) error {
		return handleRobotCorrelationFeedback(ctx, cfg, correlation.FeedbackReject)
	})
	register("robot-ignore-correlation", cfg.RobotIgnoreCorrFlag, "Mark a correlation as ignored without accuracy penalty", func(ctx RobotContext) error {
		return handleRobotCorrelationFeedback(ctx, cfg, correlation.FeedbackIgnore)
	})
	register("robot-label-health", cfg.RobotLabelHealthFlag, "Output label health metrics as JSON", handleRobotLabelHealth)
	register("robot-label-flow", cfg.RobotLabelFlowFlag, "Output cross-label dependency flow as JSON", handleRobotLabelFlow)
	register("robot-label-attention", cfg.RobotLabelAttentionFlag, "Output attention-ranked labels as JSON", func(ctx RobotContext) error {
		return handleRobotLabelAttention(ctx, cfg)
	})
	register("robot-orphans", cfg.RobotOrphansFlag, "Output orphan commit candidates as JSON", func(ctx RobotContext) error {
		return handleRobotOrphans(ctx, cfg)
	})
	register("robot-file-beads", cfg.RobotFileBeadsFlag, "Output beads that touched a file path as JSON", func(ctx RobotContext) error {
		return handleRobotFileBeads(ctx, cfg)
	})
	register("robot-commit", cfg.RobotCommitFlag, "Output beads correlated with a git commit SHA or revision as JSON", func(ctx RobotContext) error {
		return handleRobotCommit(ctx, cfg)
	})
	register("robot-file-hotspots", cfg.RobotFileHotspotsFlag, "Output files touched by most beads as JSON", func(ctx RobotContext) error {
		return handleRobotFileHotspots(ctx, cfg)
	})
	register("robot-impact", cfg.RobotImpactFlag, "Analyze impact of modifying files", func(ctx RobotContext) error {
		return handleRobotImpact(ctx, cfg)
	})
	register("robot-file-relations", cfg.RobotFileRelationsFlag, "Output files that frequently co-change with a target file", func(ctx RobotContext) error {
		return handleRobotFileRelations(ctx, cfg)
	})
	register("robot-related", cfg.RobotRelatedFlag, "Output work related to a specific bead", func(ctx RobotContext) error {
		return handleRobotRelated(ctx, cfg)
	})
	register("robot-blocker-chain", cfg.RobotBlockerChainFlag, "Output blocker chain analysis for an issue", func(ctx RobotContext) error {
		return handleRobotBlockerChain(ctx, cfg)
	})
	register("robot-whatif", cfg.RobotWhatIfFlag, "Simulate completing an issue and output downstream unblock impact as JSON", func(ctx RobotContext) error {
		return handleRobotWhatIf(ctx, cfg)
	})
	register("robot-impact-network", cfg.RobotImpactNetworkFlag, "Output bead impact network as JSON", func(ctx RobotContext) error {
		return handleRobotImpactNetwork(ctx, cfg)
	})
	register("robot-causality", cfg.RobotCausalityFlag, "Output causal chain analysis for a bead", func(ctx RobotContext) error {
		return handleRobotCausality(ctx, cfg)
	})
	register("robot-sprint-show", cfg.RobotSprintShowFlag, "Output details for a specific sprint as JSON", func(ctx RobotContext) error {
		return handleRobotSprintShow(ctx, cfg)
	})
	register("robot-capacity", cfg.RobotCapacityFlag, "Output capacity simulation and projection as JSON", func(ctx RobotContext) error {
		return handleRobotCapacity(ctx, cfg)
	})
}

func attachSourceDiffsForContext(calc *drift.Calculator, ctx RobotContext, maxAlerts int) {
	if calc == nil {
		return
	}
	diffs := detectSourceDiffsForContext(ctx, maxAlerts)
	calc.SetSourceDiffs(diffs)
	if ctx.CandidateIDs != nil && len(diffs) > 0 {
		allowed := scopeMissingSourceIssuesForContext(ctx, diffs)
		if len(allowed) > 0 {
			calc.AllowCandidateIDs(allowed)
		}
	}
}

func scopeMissingSourceIssuesForContext(ctx RobotContext, diffs []datasource.SourceDiff) []string {
	baseSlice := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		baseSlice = ctx.FullIssues
	}
	primaryLiveIDs := make(map[string]bool, len(baseSlice))
	for _, iss := range baseSlice {
		primaryLiveIDs[iss.ID] = true
	}

	var recipeMetrics recipe.Metrics
	if ctx.ActiveRecipe != nil {
		byID := make(map[string]model.Issue, len(baseSlice)+len(ctx.TombstoneIDs)+8)
		combined := make([]model.Issue, 0, len(baseSlice)+len(ctx.TombstoneIDs)+8)
		for _, iss := range baseSlice {
			byID[iss.ID] = iss
		}
		for _, tid := range ctx.TombstoneIDs {
			if _, exists := byID[tid]; !exists {
				byID[tid] = model.Issue{ID: tid, Status: model.StatusTombstone}
			}
		}
		for _, d := range diffs {
			for id, iss := range d.MissingIssues {
				if primaryLiveIDs[id] {
					continue
				}
				byID[id] = iss
			}
		}
		allIDs := make([]string, 0, len(byID))
		for id := range byID {
			allIDs = append(allIDs, id)
		}
		sort.Strings(allIDs)
		for _, id := range allIDs {
			combined = append(combined, byID[id])
		}
		combinedReadiness := model.NewReadinessIndex(combined)
		ctxWithCombinedReadiness := ctx
		ctxWithCombinedReadiness.Readiness = combinedReadiness
		recipeMetrics = recipeMetricsForContext(ctxWithCombinedReadiness, combined, ctx.ActiveRecipe)
		recipeMetrics.Readiness = combinedReadiness
	}

	var allowed []string
	allowedIssueByID := make(map[string]model.Issue)
	seen := make(map[string]bool)
	for _, d := range diffs {
		for id, iss := range d.MissingIssues {
			if primaryLiveIDs[id] || seen[id] {
				continue
			}
			seen[id] = true
			if ctx.Repo != "" && len(filterByRepo([]model.Issue{iss}, ctx.Repo)) == 0 {
				continue
			}
			if ctx.LabelScope != "" {
				sg := analysis.ComputeLabelSubgraph([]model.Issue{iss}, ctx.LabelScope)
				if len(sg.CoreIssues) == 0 {
					continue
				}
			}
			if ctx.ActiveRecipe != nil {
				singleSlice := []model.Issue{iss}
				rCheck := *ctx.ActiveRecipe
				rCheck.View.MaxItems = 0
				applied, err := recipe.Apply(singleSlice, recipeMetrics, &rCheck, ctx.ReferenceNow())
				if err != nil || len(applied) == 0 {
					continue
				}
			}
			allowed = append(allowed, id)
			allowedIssueByID[id] = iss
		}
	}
	sort.Strings(allowed)
	if ctx.ActiveRecipe != nil && ctx.ActiveRecipe.View.MaxItems > 0 && len(allowed) > 0 {
		windowInput := make([]model.Issue, 0, len(ctx.Issues)+len(allowed))
		windowInput = append(windowInput, ctx.Issues...)
		for _, id := range allowed {
			windowInput = append(windowInput, allowedIssueByID[id])
		}
		if windowed, err := recipe.Apply(windowInput, recipeMetrics, ctx.ActiveRecipe, ctx.ReferenceNow()); err == nil {
			inWindow := make(map[string]bool, len(windowed))
			for _, iss := range windowed {
				inWindow[iss.ID] = true
			}
			capped := allowed[:0]
			for _, id := range allowed {
				if inWindow[id] {
					capped = append(capped, id)
				}
			}
			allowed = capped
		}
	}
	return allowed
}

func detectSourceDiffsForContext(ctx RobotContext, _ int) []datasource.SourceDiff {
	var empty []datasource.SourceDiff
	if ctx.AsOf != "" || ctx.SourceKind == "git" {
		return empty
	}
	if _, explicitFile, _ := datasource.ExplicitBeadsDBSource(); explicitFile {
		return empty
	}
	diffOpts := datasource.DiffOptions{
		// Leave MaxDifferences uncapped during discovery so out-of-scope issues
		// cannot starve in-scope issues under --label/--repo/--recipe and so
		// source_drift_max_alerts: 0 (unlimited) is honored cleanly.
		MaxDifferences: 0,
		CompareFields:  []string{"status", "priority", "title"},
	}

	if ctx.SourceKind == "workspace" {
		if ctx.SourceAuthority == nil || len(ctx.SourceAuthority.Sources) == 0 {
			return empty
		}
		knownPrefixes := make(map[string]bool, len(ctx.SourceAuthority.Sources))
		for _, s := range ctx.SourceAuthority.Sources {
			if s.RepoPrefix != "" {
				knownPrefixes[s.RepoPrefix] = true
			}
		}
		baseSlice := ctx.Issues
		if len(ctx.FullIssues) > 0 {
			baseSlice = ctx.FullIssues
		}
		var out []datasource.SourceDiff
		seenDirs := make(map[string]bool, len(ctx.SourceAuthority.Sources))
		for _, src := range ctx.SourceAuthority.Sources {
			if src.Status != "loaded" {
				continue
			}
			if ctx.Repo != "" && !sourceMatchesRepoFilter(src, ctx.Repo) {
				continue
			}
			beadsDir := resolveSourceBeadsDir(src)
			if beadsDir == "" || beadsDir == "." || seenDirs[beadsDir] || loader.IsBDWorkspace(beadsDir) {
				continue
			}
			seenDirs[beadsDir] = true
			sources, err := datasource.DiscoverSources(datasource.DiscoveryOptions{
				BeadsDir:               beadsDir,
				RepoPath:               src.RepoRoot,
				ValidateAfterDiscovery: true,
				SkipWorktreeSources:    true,
			})
			if err != nil || len(sources) < 2 {
				continue
			}
			repoDiffs, err := datasource.CheckAllSourcesConsistent(sources, diffOpts)
			if err != nil || len(repoDiffs) == 0 {
				continue
			}
			if prefix := src.RepoPrefix; prefix != "" {
				primaryRawIDs := make(map[string]bool)
				for _, iss := range baseSlice {
					if strings.HasPrefix(iss.ID, prefix) {
						primaryRawIDs[strings.TrimPrefix(iss.ID, prefix)] = true
					}
				}
				for _, tid := range ctx.TombstoneIDs {
					if strings.HasPrefix(tid, prefix) {
						primaryRawIDs[strings.TrimPrefix(tid, prefix)] = true
					}
				}
				for i := range repoDiffs {
					qualifySourceDiff(&repoDiffs[i], prefix, primaryRawIDs, knownPrefixes)
				}
			}
			out = append(out, repoDiffs...)
		}
		return out
	}

	projectDir, _ := ctx.ProjectDirOrDefault()
	beadsDir := ""
	if ctx.SourcePath != "" {
		beadsDir = filepath.Dir(ctx.SourcePath)
	} else if projectDir != "" {
		beadsDir, _ = loader.GetBeadsDir(projectDir)
	}
	if beadsDir == "" || loader.IsBDWorkspace(beadsDir) {
		return empty
	}
	sources, err := datasource.DiscoverSources(datasource.DiscoveryOptions{
		BeadsDir:               beadsDir,
		RepoPath:               projectDir,
		ValidateAfterDiscovery: true,
		SkipWorktreeSources:    true,
	})
	if err != nil || len(sources) < 2 {
		return empty
	}
	diffs, err := datasource.CheckAllSourcesConsistent(sources, diffOpts)
	if err != nil {
		return empty
	}
	return diffs
}

func hasAnyRepoPrefix(id string, prefixes map[string]bool) bool {
	for p := range prefixes {
		if p != "" && strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func qualifySourceDiff(d *datasource.SourceDiff, prefix string, primaryRawIDs, knownPrefixes map[string]bool) {
	if d == nil || prefix == "" {
		return
	}
	if d.IssueID != "" {
		d.IssueID = workspace.QualifyID(d.IssueID, prefix)
	}
	for i := range d.MissingInA {
		d.MissingInA[i] = workspace.QualifyID(d.MissingInA[i], prefix)
	}
	for i := range d.MissingInB {
		d.MissingInB[i] = workspace.QualifyID(d.MissingInB[i], prefix)
	}
	for i := range d.StatusMismatch {
		d.StatusMismatch[i].ID = workspace.QualifyID(d.StatusMismatch[i].ID, prefix)
	}
	for i := range d.FieldMismatches {
		d.FieldMismatches[i].ID = workspace.QualifyID(d.FieldMismatches[i].ID, prefix)
	}
	for i := range d.IssueDiffs {
		d.IssueDiffs[i].IssueID = workspace.QualifyID(d.IssueDiffs[i].IssueID, prefix)
	}
	if len(d.MissingIssues) > 0 {
		localRawIDs := make(map[string]bool, len(primaryRawIDs)+len(d.MissingIssues))
		for id := range primaryRawIDs {
			localRawIDs[id] = true
		}
		for rawID := range d.MissingIssues {
			localRawIDs[rawID] = true
		}
		normSourceRepo := strings.ToLower(strings.TrimRight(strings.TrimSpace(prefix), "-:_"))
		qualified := make(map[string]model.Issue, len(d.MissingIssues))
		for rawID, iss := range d.MissingIssues {
			cloned := iss.Clone()
			qid := workspace.QualifyID(rawID, prefix)
			cloned.ID = qid
			cloned.SourceRepo = normSourceRepo
			for _, dep := range cloned.Dependencies {
				if dep == nil {
					continue
				}
				dep.IssueID = workspace.QualifyID(dep.IssueID, prefix)
				if localRawIDs[dep.DependsOnID] || !hasAnyRepoPrefix(dep.DependsOnID, knownPrefixes) {
					dep.DependsOnID = workspace.QualifyID(dep.DependsOnID, prefix)
				}
			}
			for _, c := range cloned.Comments {
				if c != nil {
					c.IssueID = workspace.QualifyID(c.IssueID, prefix)
				}
			}
			qualified[qid] = cloned
		}
		d.MissingIssues = qualified
	}
}

func labelAnalysisIssuesForContext(ctx RobotContext) []model.Issue {
	if ctx.CandidateIDs != nil && len(ctx.FullIssues) > 0 {
		if ctx.Repo != "" {
			return filterByRepo(ctx.FullIssues, ctx.Repo)
		}
		return ctx.FullIssues
	}
	return ctx.Issues
}

func handleRobotLabelHealth(ctx RobotContext) error {
	cfg := analysis.DefaultLabelHealthConfig()
	baseIssues := labelAnalysisIssuesForContext(ctx)
	results := analysis.ComputeAllLabelHealthWithScope(baseIssues, cfg, ctx.ReferenceNow(), nil, ctx.Readiness, ctx.CandidateIDs)

	output := struct {
		RobotEnvelope
		AnalysisConfig analysis.LabelHealthConfig   `json:"analysis_config"`
		Results        analysis.LabelAnalysisResult `json:"results"`
		UsageHints     []string                     `json:"usage_hints"`
	}{
		RobotEnvelope:  ctx.Envelope(),
		AnalysisConfig: cfg,
		Results:        results,
		UsageHints: []string{
			"jq '.results.summaries | sort_by(.health) | .[:3]' - Critical labels",
			"jq '.results.labels[] | select(.health_level == \"critical\")' - Critical details",
			"jq '.results.cross_label_flow.bottleneck_labels' - Bottleneck labels",
			"jq '.results.attention_needed' - Labels needing attention",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding label health: %w", err)
	}
	return nil
}

func handleRobotLabelFlow(ctx RobotContext) error {
	now := ctx.ReferenceNow()
	cfg := analysis.DefaultLabelHealthConfig()
	baseIssues := labelAnalysisIssuesForContext(ctx)
	flow := analysis.ComputeCrossLabelFlowWithScope(baseIssues, cfg, ctx.CandidateIDs)
	cascade := analysis.ComputeBlockageCascadeAtWithScope(baseIssues, flow, cfg, now, ctx.Readiness, ctx.CandidateIDs)
	output := struct {
		RobotEnvelope
		Flow            analysis.CrossLabelFlow          `json:"flow"`
		BlockageCascade analysis.BlockageCascadeAnalysis `json:"blockage_cascade"`
		Config          analysis.LabelHealthConfig       `json:"analysis_config"`
		UsageHints      []string                         `json:"usage_hints"`
	}{
		RobotEnvelope:   ctx.Envelope(),
		Flow:            flow,
		BlockageCascade: cascade,
		Config:          cfg,
		UsageHints: []string{
			"jq '.flow.bottleneck_labels' - labels blocking the most others",
			"jq '.flow.dependencies[] | select(.issue_count > 0) | {from:.from_label,to:.to_label,count:.issue_count}'",
			"jq '.flow.flow_matrix' - raw matrix (row=from, col=to, align with .flow.labels)",
			"jq '.blockage_cascade.top_unblocks' - highest-leverage blockers to clear across downstream labels",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding label flow: %w", err)
	}
	return nil
}

func handleRobotLabelAttention(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	baseIssues := labelAnalysisIssuesForContext(ctx)
	result := analysis.ComputeLabelAttentionScoresWithScope(baseIssues, analysis.DefaultLabelHealthConfig(), ctx.ReferenceNow(), ctx.Readiness, ctx.CandidateIDs)

	limit := 5
	if cfg.AttentionLimit != nil {
		limit = *cfg.AttentionLimit
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > len(result.Labels) {
		limit = len(result.Labels)
	}

	type attentionLabel struct {
		Rank            int     `json:"rank"`
		Label           string  `json:"label"`
		AttentionScore  float64 `json:"attention_score"`
		NormalizedScore float64 `json:"normalized_score"`
		Reason          string  `json:"reason"`
		OpenCount       int     `json:"open_count"`
		BlockedCount    int     `json:"blocked_count"`
		StaleCount      int     `json:"stale_count"`
		PageRankSum     float64 `json:"pagerank_sum"`
		VelocityFactor  float64 `json:"velocity_factor"`
	}
	type attentionOutput struct {
		RobotEnvelope
		Limit       int              `json:"limit"`
		TotalLabels int              `json:"total_labels"`
		Labels      []attentionLabel `json:"labels"`
		UsageHints  []string         `json:"usage_hints"`
	}

	output := attentionOutput{
		RobotEnvelope: ctx.Envelope(),
		Limit:         limit,
		TotalLabels:   result.TotalLabels,
		Labels:        make([]attentionLabel, 0, limit),
		UsageHints: []string{
			"jq '.labels[0]' - top attention label details",
			"jq '.labels[] | select(.blocked_count > 0)' - labels with blocked issues",
			"jq '.labels[] | {label:.label,score:.attention_score,reason:.reason}'",
		},
	}

	for i := 0; i < limit; i++ {
		score := result.Labels[i]
		output.Labels = append(output.Labels, attentionLabel{
			Rank:            score.Rank,
			Label:           score.Label,
			AttentionScore:  score.AttentionScore,
			NormalizedScore: score.NormalizedScore,
			Reason:          buildAttentionReason(score),
			OpenCount:       score.OpenCount,
			BlockedCount:    score.BlockedCount,
			StaleCount:      score.StaleCount,
			PageRankSum:     score.PageRankSum,
			VelocityFactor:  score.VelocityFactor,
		})
	}

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding label attention: %w", err)
	}
	return nil
}

func buildRobotInsightsPayload(ctx RobotContext, includeInactive, forceFull bool, maxResults int) (any, error) {
	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	analyzer := ctx.GraphAnalyzer()
	analyzer.SetNow(now)
	if ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
		analyzer.SeedDataHash(ctx.DataHash)
	}
	if forceFull {
		fullConfig := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullConfig)
	}
	stats := analyzer.Analyze()
	insightLimit := 50
	if maxResults > 0 {
		insightLimit = maxResults
	}
	insights := analyzer.GenerateScopedInsights(&stats, insightLimit, includeInactive)

	if velocity := analysis.ComputeProjectVelocity(ctx.VelocityIssues(), now, 8); velocity != nil {
		snapshot := &analysis.VelocitySnapshot{
			Closed7:   velocity.ClosedLast7Days,
			Closed30:  velocity.ClosedLast30Days,
			AvgDays:   velocity.AvgDaysToClose,
			Estimated: velocity.Estimated,
		}
		if len(velocity.Weekly) > 0 {
			snapshot.Weekly = make([]int, len(velocity.Weekly))
			for i := range velocity.Weekly {
				snapshot.Weekly[i] = velocity.Weekly[i].Closed
			}
		}
		insights.Velocity = snapshot
	}

	limitMaps := func(m map[string]float64, limit int) map[string]float64 {
		if limit <= 0 || limit >= len(m) {
			return m
		}
		type kv struct {
			k string
			v float64
		}
		items := make([]kv, 0, len(m))
		for k, v := range m {
			items = append(items, kv{k: k, v: v})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].v == items[j].v {
				return items[i].k < items[j].k
			}
			return items[i].v > items[j].v
		})
		trimmed := make(map[string]float64, limit)
		for i := 0; i < limit && i < len(items); i++ {
			trimmed[items[i].k] = items[i].v
		}
		return trimmed
	}

	limitMapInt := func(m map[string]int, limit int) map[string]int {
		if limit <= 0 || len(m) <= limit {
			return m
		}
		type kv struct {
			k string
			v int
		}
		items := make([]kv, 0, len(m))
		for k, v := range m {
			items = append(items, kv{k: k, v: v})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].v == items[j].v {
				return items[i].k < items[j].k
			}
			return items[i].v > items[j].v
		})
		trimmed := make(map[string]int, limit)
		for i := 0; i < limit && i < len(items); i++ {
			trimmed[items[i].k] = items[i].v
		}
		return trimmed
	}

	limitSlice := func(in []string, limit int) []string {
		if limit <= 0 || len(in) <= limit {
			return in
		}
		return in[:limit]
	}

	mapLimit := 200
	if value := env.InsightsMapLimit.Get(); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			mapLimit = parsed
		}
	}

	filterFloatMap := func(m map[string]float64) map[string]float64 {
		if ctx.CandidateIDs == nil {
			return m
		}
		scoped := make(map[string]float64, len(ctx.CandidateIDs))
		for k, v := range m {
			if ctx.CandidateIDs[k] {
				scoped[k] = v
			}
		}
		return scoped
	}
	filterIntMap := func(m map[string]int) map[string]int {
		if ctx.CandidateIDs == nil {
			return m
		}
		scoped := make(map[string]int, len(ctx.CandidateIDs))
		for k, v := range m {
			if ctx.CandidateIDs[k] {
				scoped[k] = v
			}
		}
		return scoped
	}
	filterStringSlice := func(in []string) []string {
		if ctx.CandidateIDs == nil {
			return in
		}
		scoped := make([]string, 0, len(in))
		for _, id := range in {
			if ctx.CandidateIDs[id] {
				scoped = append(scoped, id)
			}
		}
		return scoped
	}

	if ctx.CandidateIDs != nil && insights.Stats != nil {
		scopedNodes, scopedEdges, scopedDensity := scopedGraphTotalsForCLI(graphIssues, &stats, ctx.CandidateIDs)
		insights.Stats = &analysis.GraphStats{
			OutDegree:        filterIntMap(insights.Stats.OutDegree),
			InDegree:         filterIntMap(insights.Stats.InDegree),
			TopologicalOrder: filterStringSlice(insights.Stats.TopologicalOrder),
			Density:          scopedDensity,
			NodeCount:        scopedNodes,
			EdgeCount:        scopedEdges,
			Config:           insights.Stats.Config,
		}
	}

	fullStats := struct {
		PageRank          map[string]float64 `json:"pagerank"`
		Betweenness       map[string]float64 `json:"betweenness"`
		Eigenvector       map[string]float64 `json:"eigenvector"`
		Hubs              map[string]float64 `json:"hubs"`
		Authorities       map[string]float64 `json:"authorities"`
		CriticalPathScore map[string]float64 `json:"critical_path_score"`
		CoreNumber        map[string]int     `json:"core_number"`
		Slack             map[string]float64 `json:"slack"`
		Articulation      []string           `json:"articulation_points"`
	}{
		PageRank:          limitMaps(filterFloatMap(stats.PageRank()), mapLimit),
		Betweenness:       limitMaps(filterFloatMap(stats.Betweenness()), mapLimit),
		Eigenvector:       limitMaps(filterFloatMap(stats.Eigenvector()), mapLimit),
		Hubs:              limitMaps(filterFloatMap(stats.Hubs()), mapLimit),
		Authorities:       limitMaps(filterFloatMap(stats.Authorities()), mapLimit),
		CriticalPathScore: limitMaps(filterFloatMap(stats.CriticalPathScore()), mapLimit),
		CoreNumber:        limitMapInt(filterIntMap(stats.CoreNumber()), mapLimit),
		Slack:             limitMaps(filterFloatMap(stats.Slack()), mapLimit),
		Articulation:      limitSlice(filterStringSlice(stats.ArticulationPoints()), mapLimit),
	}

	whatIfLimit := 10
	if maxResults > 0 && maxResults < whatIfLimit {
		whatIfLimit = maxResults
	}

	output := struct {
		RobotEnvelope
		AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
		Status         analysis.MetricStatus   `json:"status"`
		LabelScope     string                  `json:"label_scope,omitempty"`
		LabelContext   *analysis.LabelHealth   `json:"label_context,omitempty"`
		analysis.Insights
		FullStats        interface{}                `json:"full_stats"`
		TopWhatIfs       []analysis.WhatIfEntry     `json:"top_what_ifs,omitempty"`
		AdvancedInsights *analysis.AdvancedInsights `json:"advanced_insights,omitempty"`
		UsageHints       []string                   `json:"usage_hints"`
	}{
		RobotEnvelope:    ctx.Envelope(),
		AnalysisConfig:   stats.Config,
		Status:           stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
		LabelScope:       ctx.LabelScope,
		LabelContext:     ctx.LabelContext,
		Insights:         insights,
		FullStats:        fullStats,
		TopWhatIfs:       analyzer.TopWhatIfDeltasFromStats(&stats, whatIfLimit),
		AdvancedInsights: analyzer.GenerateAdvancedInsightsFromStats(&stats, analysis.DefaultAdvancedInsightsConfig()),
		UsageHints: []string{
			"jq '.Bottlenecks[:5] | map(.ID)' - Top 5 bottleneck IDs",
			"jq '.Keystones[:3]' - Top 3 critical path scores",
			"jq '.top_what_ifs[] | select(.delta.direct_unblocks > 2)' - High-impact items",
			"jq '.full_stats.pagerank | to_entries | sort_by(-.value)[:5]' - Top PageRank",
			"jq '.full_stats.core_number | to_entries | sort_by(-.value)[:5]' - Strongly embedded nodes (k-core)",
			"jq '.full_stats.articulation_points' - Structural cut points",
			"jq '.Slack[:5]' - Nodes with slack (good parallel work candidates)",
			"jq '.Cycles | length' - Count of detected cycles",
			"jq '.advanced_insights.cycle_break' - Cycle break suggestions (bv-181)",
			"BV_INSIGHTS_MAP_LIMIT=50 bv --robot-insights - Reduce map sizes",
		},
	}

	if sourceDateEpochActive() && output.AdvancedInsights != nil && output.AdvancedInsights.ParallelGain != nil {
		// Match the core metric status: measured wall time is not reproducible,
		// even when the analysis itself runs deterministically to completion.
		output.AdvancedInsights.ParallelGain.Status.DurationMs = 0
	}

	return output, nil
}

func handleRobotInsights(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	includeInactive := cfg.IncludeInactive != nil && *cfg.IncludeInactive
	forceFull := cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis
	maxResults := 0
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		maxResults = *cfg.RobotMaxResults
	}
	output, err := buildRobotInsightsPayload(ctx, includeInactive, forceFull, maxResults)
	if err != nil {
		return err
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding insights: %w", err)
	}
	return nil
}

// defaultRobotHistoryTimeout bounds the git-history correlation prologue of
// --robot-triage / --robot-next (issue #166). The prologue is best-effort
// enrichment (staleness metrics); it must never be able to hang the robot
// surface, which agents rely on as their single bounded entry point.
const defaultRobotHistoryTimeout = 10 * time.Second

// robotHistoryShutdownGrace bounds how long a timed-out triage invocation
// waits for a directly spawned, context-bound git child to be killed and
// reaped. Returning as soon as the deadline fires can let the short-lived bv
// process exit before exec.CommandContext's cancellation path finishes.
const robotHistoryShutdownGrace = 2 * time.Second

const maxRobotHistoryTimeoutMillis = int64(math.MaxInt64) / int64(time.Millisecond)

// robotHistoryTimeoutFromMilliseconds converts a user-supplied millisecond
// count without allowing duration overflow to turn a very large positive
// timeout into a negative (and therefore unbounded) duration. Values above
// time.Duration's range saturate at its largest representable duration.
func robotHistoryTimeoutFromMilliseconds(ms int64) (time.Duration, bool) {
	if ms < 0 {
		return 0, false
	}
	if ms > maxRobotHistoryTimeoutMillis {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(ms) * time.Millisecond, true
}

// resolveRobotHistoryTimeout returns the history-prologue budget. Precedence:
// the --robot-history-timeout-ms flag (when explicitly set, i.e. >= 0), then
// the BV_ROBOT_HISTORY_TIMEOUT_MS environment variable, then the 10s default.
// A value of 0 disables the bound entirely (legacy run-to-completion).
func resolveRobotHistoryTimeout(cfg phaseThreeRobotHandlerConfig) time.Duration {
	if cfg.HistoryTimeoutMs != nil && *cfg.HistoryTimeoutMs >= 0 {
		timeout, _ := robotHistoryTimeoutFromMilliseconds(int64(*cfg.HistoryTimeoutMs))
		return timeout
	}
	if envVal := strings.TrimSpace(env.RobotHistoryTimeoutMS.Get()); envVal != "" {
		if ms, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			if timeout, ok := robotHistoryTimeoutFromMilliseconds(ms); ok {
				return timeout
			}
		}
	}
	return defaultRobotHistoryTimeout
}

// resolveNotReadyLabels returns the opt-in not-ready label-class (issue #173)
// used to exclude graph-ready-but-not-work-ready beads from claimable robot top
// picks. Precedence: the --robot-not-ready-labels flag (comma-separated) when
// set, else the BV_ROBOT_NOT_READY_LABELS environment variable. Returns nil
// (gate disabled, zero behavior change) when neither is configured. Labels are
// trimmed; empty entries are dropped. Matching is case-insensitive (handled in
// the analysis layer), so labels are returned as written.
func resolveNotReadyLabels(cfg phaseThreeRobotHandlerConfig) []string {
	raw := ""
	if cfg.NotReadyLabels != nil && strings.TrimSpace(*cfg.NotReadyLabels) != "" {
		raw = *cfg.NotReadyLabels
	} else if envVal := strings.TrimSpace(env.RobotNotReadyLabels.Get()); envVal != "" {
		raw = envVal
	}
	if raw == "" {
		return nil
	}
	var labels []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	return labels
}

// generateTriageHistoryBounded runs the expensive git-history correlation
// prologue of --robot-triage under a hard time budget (issue #166).
//
// The report generation runs in a goroutine while this function selects on
// the result vs. the budget. On timeout it returns (nil, "timeout") and
// triage proceeds without history — the already-supported degradation path.
// Cancellation is propagated to directly spawned git subprocesses through
// exec.CommandContext. The shutdown grace below gives a currently running
// direct child and its cmd.Wait path a bounded chance to finish reaping before
// this function returns. It does not claim that every internal cache or lock
// wait is context-aware; this caller stops waiting for those paths when the
// shutdown grace expires, and their goroutine may finish later.
//
// The returned status is "ok", "error", or "timeout"; it is surfaced as
// meta.history_status in the triage output. The caller uses "skipped" instead
// when SOURCE_DATE_EPOCH requests reproducible output, because racing a git
// history walk against a wall-clock deadline cannot produce stable bytes.
func generateTriageHistoryBounded(workDir, beadsPath, asOfCommit string, beadInfos []correlation.BeadInfo, limit int, timeout time.Duration) (*correlation.HistoryReport, string) {
	histCtx := context.Background()
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		histCtx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()

	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return nil, "error"
	}
	correlator = correlator.WithContext(histCtx)

	type historyResult struct {
		report *correlation.HistoryReport
		err    error
	}
	resCh := make(chan historyResult, 1)
	go func() {
		report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{
			Limit:    limit,
			Revision: asOfCommit,
		})
		resCh <- historyResult{report: report, err: err}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, "error"
		}
		return res.report, "ok"
	case <-histCtx.Done():
		// Cancel explicitly before the deferred cancellation, then give the
		// report goroutine a bounded opportunity to finish cmd.Wait and reap a
		// directly spawned, context-killed git child before this short-lived bv
		// process exits.
		cancel()
		shutdownTimer := time.NewTimer(robotHistoryShutdownGrace)
		defer shutdownTimer.Stop()
		select {
		case <-resCh:
		case <-shutdownTimer.C:
		}
		return nil, "timeout"
	}
}

func isCLIFlagExplicitlySet(name string) bool {
	if pflag.CommandLine == nil {
		return false
	}
	return pflag.CommandLine.Changed(name)
}

func generateWorkspaceTriageHistoryBounded(ctx RobotContext, limit int, timeout time.Duration) (*correlation.HistoryReport, string) {
	histCtx := context.Background()
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		histCtx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()

	type historyResult struct {
		report *correlation.HistoryReport
		err    error
	}
	resCh := make(chan historyResult, 1)
	go func() {
		report, err := generateWorkspaceCorrelationReportWithContext(histCtx, ctx, correlation.CorrelatorOptions{
			Limit:    limit,
			Revision: ctx.AsOfCommit,
		}, false)
		resCh <- historyResult{report: report, err: err}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			return nil, "error"
		}
		return res.report, "ok"
	case <-histCtx.Done():
		cancel()
		shutdownTimer := time.NewTimer(robotHistoryShutdownGrace)
		defer shutdownTimer.Stop()
		select {
		case <-resCh:
		case <-shutdownTimer.C:
		}
		return nil, "timeout"
	}
}

func loadTriageHistoryForContext(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) (*correlation.HistoryReport, string) {
	triageIssues := ctx.GraphIssues()
	hasOpenIssues := false
	for _, issue := range triageIssues {
		if ctx.CandidateIDs != nil && !ctx.CandidateIDs[issue.ID] {
			continue
		}
		if issue.Status != model.StatusClosed && issue.Status != model.StatusTombstone {
			hasOpenIssues = true
			break
		}
	}
	if !hasOpenIssues {
		return nil, ""
	}
	if sourceDateEpochActive() {
		return nil, "skipped"
	}
	limit := 200
	if cfg.HistoryLimit != nil && (*cfg.HistoryLimit != 500 || isCLIFlagExplicitlySet("history-limit")) {
		limit = *cfg.HistoryLimit
	}
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		return generateWorkspaceTriageHistoryBounded(ctx, limit, resolveRobotHistoryTimeout(cfg))
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return nil, ""
	}
	beadsPath, err := resolveCorrelationBeadsPathForContext(ctx, workDir)
	if err != nil {
		return nil, ""
	}
	beadInfos := buildCorrelationBeadInfos(triageIssues)
	return generateTriageHistoryBounded(
		workDir, beadsPath, ctx.AsOfCommit, beadInfos, limit, resolveRobotHistoryTimeout(cfg))
}

func handleRobotTriage(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	historyReport, historyStatus := loadTriageHistoryForContext(ctx, cfg)

	// bv-140: scope triage to a subgraph if --graph-root is specified
	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	seedHash := ""
	if ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
		seedHash = ctx.DataHash
	}
	maxResults := 0
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		maxResults = *cfg.RobotMaxResults
	}
	// bv-90: accept/ignore feedback tunes the factor weights once enough
	// samples exist; the payload's feedback block reports whether it applied.
	feedbackData, feedbackWeights := loadRobotFeedbackForContext(ctx)
	forceFull := cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis
	triage := analysis.ComputeTriageWithOptionsAndTime(graphIssues, analysis.TriageOptions{
		TopN:              maxResults,
		ActionableOnly:    cfg.IncludeInactive == nil || !*cfg.IncludeInactive,
		GroupByTrack:      cfg.RobotTriageByTrackFlag != nil && *cfg.RobotTriageByTrackFlag,
		Readiness:         ctx.Readiness,
		CandidateIDs:      ctx.CandidateIDs,
		UnblockIDs:        ctx.RecipeUnblockIDs,
		VelocityIssues:    ctx.VelocityIssues(),
		GroupByLabel:      cfg.RobotTriageByLabelFlag != nil && *cfg.RobotTriageByLabelFlag,
		WaitForPhase2:     true,
		UseFastConfig:     !forceFull,
		ForceFullAnalysis: forceFull,
		History:           historyReport,
		RootIssueID:       rootIssueID,
		SeedDataHash:      seedHash,
		NotReadyLabels:    resolveNotReadyLabels(cfg),
		Weights:           feedbackWeights,
	}, now)
	if maxResults > 0 && len(triage.QuickRef.TopPicks) > maxResults {
		triage.QuickRef.TopPicks = triage.QuickRef.TopPicks[:maxResults]
	}
	stabilizeRobotTriageForPinnedClock(&triage)
	triage.Meta.HistoryStatus = historyStatus
	if !ctx.claimsProven() {
		suppressUnprovenTriageClaims(&triage)
	}

	// --brief (#183): emit only the decision-relevant fields agents actually
	// consume at session start (id/title/status, blockers/unblocks, claim
	// state) and skip the per-issue score breakdowns, project health, and
	// usage hints that dominate the full payload's token cost.
	if cfg.RobotTriageBriefFlag != nil && *cfg.RobotTriageBriefFlag {
		return encodeBriefTriage(ctx, triage, now)
	}

	var feedbackInfo *analysis.FeedbackJSON
	if feedbackData != nil && len(feedbackData.Events) > 0 {
		info := feedbackData.ToJSON()
		feedbackInfo = &info
	}

	output := struct {
		RobotEnvelope
		Triage     analysis.TriageResult  `json:"triage"`
		Feedback   *analysis.FeedbackJSON `json:"feedback,omitempty"`
		UsageHints []string               `json:"usage_hints"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Triage:        triage,
		Feedback:      feedbackInfo,
		UsageHints: []string{
			"jq '.triage.quick_ref.top_picks[:3]' - Top 3 picks for immediate work",
			"jq '.triage.recommendations[3:10] | map({id,title,score})' - Next candidates after top picks",
			"jq '.triage.blockers_to_clear | map(.id)' - High-impact blockers to clear",
			"jq '.triage.recommendations[] | select(.type == \"bug\")' - Bug-focused recommendations",
			"jq '.triage.quick_ref.top_picks[] | select(.unblocks > 2)' - High-impact picks",
			"jq '.triage.quick_wins' - Low-effort, high-impact items",
			"--robot-next - Get only the single top recommendation",
			"--brief - Compact output: only id/title/status/assignee/blockers/unblocks (#183)",
			"--robot-include-inactive - Include planning diagnostics without enabling claims",
			"--robot-triage-by-track - Group by execution track for multi-agent coordination",
			"--robot-triage-by-label - Group by label for area-focused agents",
			"jq '.triage.recommendations_by_track[].top_pick' - Top pick per track",
			"jq '.triage.recommendations_by_label[].claim_command' - Claim commands per label",
			"jq '.feedback.weight_adjustments' - View feedback-adjusted weights (bv-90)",
			"--graph-root <id> - Scope triage to subgraph rooted at a specific epic (bv-140)",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding robot-triage: %w", err)
	}
	return nil
}

// briefTriageRecommendation carries only the fields agents use for work
// selection (#183): identity, claim state, and the dependency edges.
type briefTriageRecommendation struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Status    string             `json:"status"`
	Assignee  string             `json:"assignee,omitempty"`
	Score     float64            `json:"score"`
	Unblocks  []string           `json:"unblocks,omitempty"`
	BlockedBy []string           `json:"blocked_by,omitempty"`
	Actions   model.IssueActions `json:"actions"`
}

type briefTrackRecommendationGroup struct {
	TrackID         string                      `json:"track_id"`
	Reason          string                      `json:"reason"`
	Recommendations []briefTriageRecommendation `json:"recommendations"`
	TopPick         *analysis.TopPick           `json:"top_pick,omitempty"`
	ClaimCommand    string                      `json:"claim_command,omitempty"`
	TotalUnblocks   int                         `json:"total_unblocks"`
}

type briefLabelRecommendationGroup struct {
	Label           string                      `json:"label"`
	Recommendations []briefTriageRecommendation `json:"recommendations"`
	TopPick         *analysis.TopPick           `json:"top_pick,omitempty"`
	ClaimCommand    string                      `json:"claim_command,omitempty"`
	TotalUnblocks   int                         `json:"total_unblocks"`
}

// briefTriageOutput is the compact --robot-triage --brief payload (#183).
// It keeps quick_ref (counts + top picks — the claimability signal),
// quick_wins, and blockers_to_clear (already lean), and reduces each
// recommendation to briefTriageRecommendation. Score breakdowns, project
// health, commands, feedback, and usage hints are omitted.
type briefTriageOutput struct {
	RobotEnvelope
	Brief                  bool                            `json:"brief"`
	QuickRef               analysis.QuickRef               `json:"quick_ref"`
	Recommendations        []briefTriageRecommendation     `json:"recommendations"`
	RecommendationsByTrack []briefTrackRecommendationGroup `json:"recommendations_by_track,omitempty"`
	RecommendationsByLabel []briefLabelRecommendationGroup `json:"recommendations_by_label,omitempty"`
	QuickWins              []analysis.QuickWin             `json:"quick_wins,omitempty"`
	BlockersToClear        []analysis.BlockerItem          `json:"blockers_to_clear,omitempty"`
}

func compactBriefTriageRecommendations(in []analysis.Recommendation) []briefTriageRecommendation {
	out := make([]briefTriageRecommendation, 0, len(in))
	for _, rec := range in {
		out = append(out, briefTriageRecommendation{
			ID:        rec.ID,
			Title:     rec.Title,
			Status:    rec.Status,
			Assignee:  rec.Assignee,
			Score:     rec.Score,
			Unblocks:  rec.UnblocksIDs,
			BlockedBy: rec.BlockedBy,
			Actions:   rec.Actions,
		})
	}
	return out
}

func encodeBriefTriage(ctx RobotContext, triage analysis.TriageResult, now time.Time) error {
	recs := compactBriefTriageRecommendations(triage.Recommendations)
	var byTrack []briefTrackRecommendationGroup
	if len(triage.RecommendationsByTrack) > 0 {
		byTrack = make([]briefTrackRecommendationGroup, 0, len(triage.RecommendationsByTrack))
		for _, grp := range triage.RecommendationsByTrack {
			byTrack = append(byTrack, briefTrackRecommendationGroup{
				TrackID:         grp.TrackID,
				Reason:          grp.Reason,
				Recommendations: compactBriefTriageRecommendations(grp.Recommendations),
				TopPick:         grp.TopPick,
				ClaimCommand:    grp.ClaimCommand,
				TotalUnblocks:   grp.TotalUnblocks,
			})
		}
	}
	var byLabel []briefLabelRecommendationGroup
	if len(triage.RecommendationsByLabel) > 0 {
		byLabel = make([]briefLabelRecommendationGroup, 0, len(triage.RecommendationsByLabel))
		for _, grp := range triage.RecommendationsByLabel {
			byLabel = append(byLabel, briefLabelRecommendationGroup{
				Label:           grp.Label,
				Recommendations: compactBriefTriageRecommendations(grp.Recommendations),
				TopPick:         grp.TopPick,
				ClaimCommand:    grp.ClaimCommand,
				TotalUnblocks:   grp.TotalUnblocks,
			})
		}
	}
	output := briefTriageOutput{
		RobotEnvelope:          ctx.Envelope(),
		Brief:                  true,
		QuickRef:               triage.QuickRef,
		Recommendations:        recs,
		RecommendationsByTrack: byTrack,
		RecommendationsByLabel: byLabel,
		QuickWins:              triage.QuickWins,
		BlockersToClear:        triage.BlockersToClear,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding robot-triage --brief: %w", err)
	}
	return nil
}

type robotNextDegradation struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Repair   string `json:"repair,omitempty"`
}

type robotNextDiagnosticPick struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Score    float64  `json:"score"`
	Reasons  []string `json:"reasons"`
	Unblocks int      `json:"unblocks"`
}

type robotNextOutput struct {
	RobotEnvelope
	Actionable        bool                     `json:"actionable"`
	Phase2Ready       bool                     `json:"phase2_ready"`
	Status            analysis.MetricStatus    `json:"status"`
	Message           string                   `json:"message,omitempty"`
	ID                string                   `json:"id,omitempty"`
	Title             string                   `json:"title,omitempty"`
	Score             float64                  `json:"score,omitempty"`
	Reasons           []string                 `json:"reasons,omitempty"`
	Unblocks          int                      `json:"unblocks,omitempty"`
	DiagnosticTopPick *robotNextDiagnosticPick `json:"diagnostic_top_pick,omitempty"`
	ClaimCmd          string                   `json:"claim_command,omitempty"`
	ShowCmd           string                   `json:"show_command,omitempty"`
	Actions           *model.IssueActions      `json:"actions,omitempty"`
	Degraded          []robotNextDegradation   `json:"degraded,omitempty"`
	UsageHints        []string                 `json:"usage_hints,omitempty"`
}

func robotNextIssueIndex(issues []model.Issue) map[string]model.Issue {
	issueByID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		issueByID[issue.ID] = issue
	}
	return issueByID
}

func robotNextClaimabilityReasons(pick analysis.TopPick, issueByID map[string]model.Issue, readiness *model.ReadinessIndex, now time.Time) []string {
	issue, ok := issueByID[pick.ID]
	if !ok {
		return []string{fmt.Sprintf("%s is absent from loaded Beads records", pick.ID)}
	}

	var reasons []string
	if !strings.EqualFold(strings.TrimSpace(string(issue.Status)), string(model.StatusOpen)) {
		reasons = append(reasons, fmt.Sprintf("%s status is %q", pick.ID, issue.Status))
	}
	if strings.EqualFold(strings.TrimSpace(string(issue.IssueType)), string(model.TypeEpic)) {
		reasons = append(reasons, fmt.Sprintf("%s is an epic", pick.ID))
	}
	if assignee := strings.TrimSpace(issue.Assignee); assignee != "" {
		reasons = append(reasons, fmt.Sprintf("%s is already assigned to %s", pick.ID, assignee))
	}
	if issue.Ephemeral {
		reasons = append(reasons, fmt.Sprintf("%s is ephemeral", pick.ID))
	}
	if issue.Pinned {
		reasons = append(reasons, fmt.Sprintf("%s is pinned", pick.ID))
	}
	if issue.IsTemplate {
		reasons = append(reasons, fmt.Sprintf("%s is a template", pick.ID))
	}
	// Scheduler deferral (issue #191): a future defer_until withholds the bead
	// from claiming, exactly as `br ready` hides it.
	if issue.IsDeferredAt(now) {
		reasons = append(reasons, fmt.Sprintf("%s is deferred until %s", pick.ID, issue.DeferUntil.UTC().Format(time.RFC3339)))
	}

	openBlockers := readiness.Blockers(issue.ID)
	if len(openBlockers) > 0 {
		reasons = append(reasons, fmt.Sprintf("%s is blocked by %s", pick.ID, strings.Join(openBlockers, ", ")))
	}
	if readiness.DependencyState(issue.ID) == model.DependenciesUnknown {
		reasons = append(reasons, "dependency authority is missing or unresolved")
	}
	if readiness.HasOpenChildren(issue.ID) {
		reasons = append(reasons, "parent still has open children")
	}

	return reasons
}

func robotNextDiagnosticFromPick(pick analysis.TopPick) robotNextDiagnosticPick {
	return robotNextDiagnosticPick{
		ID:       pick.ID,
		Title:    pick.Title,
		Score:    pick.Score,
		Reasons:  pick.Reasons,
		Unblocks: pick.Unblocks,
	}
}

func robotNextClaimablePick(picks []analysis.TopPick, issues []model.Issue, readiness *model.ReadinessIndex, now time.Time) (analysis.TopPick, *robotNextDiagnosticPick, []string, bool) {
	if len(picks) == 0 {
		return analysis.TopPick{}, nil, nil, false
	}

	issueByID := robotNextIssueIndex(issues)
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}
	firstDiagnostic := robotNextDiagnosticFromPick(picks[0])
	var firstUnsafeReasons []string
	for _, pick := range picks {
		reasons := robotNextClaimabilityReasons(pick, issueByID, readiness, now)
		if len(reasons) == 0 {
			return pick, &firstDiagnostic, nil, true
		}
		if len(firstUnsafeReasons) == 0 {
			firstUnsafeReasons = reasons
		}
	}

	return analysis.TopPick{}, &firstDiagnostic, firstUnsafeReasons, false
}

// loadRobotFeedback loads .beads/feedback.json for the current workspace and
// returns it together with the feedback-adjusted factor weights when enough
// accept/ignore samples exist to apply them (analysis.MinFeedbackSamples).
// A missing or unreadable file yields (nil, nil) so scoring uses the defaults.
// Every scoring surface (triage, next, priority, TUI hints) goes through the
// same rule so an agent's accept/ignore history changes what it is told next.
func loadRobotFeedback() (*analysis.FeedbackData, *analysis.Weights) {
	return loadRobotFeedbackForDir("")
}

func loadRobotFeedbackForContext(ctx RobotContext) (*analysis.FeedbackData, *analysis.Weights) {
	if ctx.AsOf != "" || ctx.SourceKind == "workspace" {
		return nil, nil
	}
	dir := ctx.ProjectDir
	if dir == "" {
		dir = ctx.WorkDir
	}
	return loadRobotFeedbackForDir(dir)
}

func loadRobotFeedbackForDir(projectDir string) (*analysis.FeedbackData, *analysis.Weights) {
	beadsDir, err := loader.GetBeadsDir(projectDir)
	if err != nil {
		return nil, nil
	}
	fb, err := analysis.LoadFeedback(beadsDir)
	if err != nil || fb == nil {
		return nil, nil
	}
	if !fb.Applies() {
		return fb, nil
	}
	w := fb.Weights()
	return fb, &w
}

func handleRobotNext(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	historyReport, _ := loadTriageHistoryForContext(ctx, cfg)

	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	seedHash := ""
	if ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
		seedHash = ctx.DataHash
	}
	_, feedbackWeights := loadRobotFeedbackForContext(ctx)
	forceFull := cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis
	notReadyLabels := resolveNotReadyLabels(cfg)
	triage := analysis.ComputeTriageWithOptionsAndTime(graphIssues, analysis.TriageOptions{
		WaitForPhase2:     true,
		Readiness:         ctx.Readiness,
		CandidateIDs:      ctx.CandidateIDs,
		UnblockIDs:        ctx.RecipeUnblockIDs,
		VelocityIssues:    ctx.VelocityIssues(),
		UseFastConfig:     !forceFull,
		ForceFullAnalysis: forceFull,
		History:           historyReport,
		RootIssueID:       rootIssueID,
		SeedDataHash:      seedHash,
		NotReadyLabels:    notReadyLabels,
		Weights:           feedbackWeights,
	}, now)
	stabilizeRobotTriageForPinnedClock(&triage)

	topDiagnosticCandidate := func() *robotNextDiagnosticPick {
		if len(triage.QuickRef.TopPicks) > 0 {
			diag := robotNextDiagnosticFromPick(triage.QuickRef.TopPicks[0])
			return &diag
		}
		if len(triage.Recommendations) > 0 {
			rec := triage.Recommendations[0]
			diag := robotNextDiagnosticPick{
				ID:       rec.ID,
				Title:    rec.Title,
				Score:    rec.Score,
				Reasons:  rec.Reasons,
				Unblocks: len(rec.UnblocksIDs),
			}
			return &diag
		}
		return nil
	}

	output := robotNextOutput{
		RobotEnvelope: ctx.Envelope(),
		Phase2Ready:   triage.Meta.Phase2Ready,
		Status:        triage.Status,
		UsageHints: []string{
			"Use scripts/br_retry.sh actionable --json plus the claim gate before mutating Beads state in crowded swarms.",
			"No claim_command is emitted unless the item is open, unblocked, unassigned, and triage metrics are ready.",
			"Inspect .status for skipped, timeout, or pending graph phases.",
		},
	}
	if !ctx.claimsProven() {
		output.Message = "No claim command emitted because source authority is incomplete or stale"
		output.DiagnosticTopPick = topDiagnosticCandidate()
		output.Degraded = []robotNextDegradation{{Code: "source_authority_incomplete", Severity: "warning",
			Message: "Readiness is provisional; inspect source_authority for failed sources, dropped records, or stale fallback.",
			Repair:  "Restore or refresh the affected sources and rerun the command before claiming work."}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	if len(triage.QuickRef.TopPicks) == 0 {
		output.Message = "No proven actionable item available"
		diag := topDiagnosticCandidate()
		output.DiagnosticTopPick = diag
		msg := "No open, unblocked, unassigned non-epic recommendation passed the robot-next claimability filter."
		if diag != nil {
			issueByID := robotNextIssueIndex(graphIssues)
			reasons := robotNextClaimabilityReasons(analysis.TopPick{
				ID:       diag.ID,
				Title:    diag.Title,
				Score:    diag.Score,
				Reasons:  diag.Reasons,
				Unblocks: diag.Unblocks,
			}, issueByID, ctx.Readiness, now)
			if iss, ok := issueByID[diag.ID]; ok && len(notReadyLabels) > 0 {
				for _, nr := range notReadyLabels {
					for _, lbl := range iss.Labels {
						if strings.EqualFold(strings.TrimSpace(lbl), nr) {
							reasons = append(reasons, fmt.Sprintf("%s carries not-ready label %q", diag.ID, lbl))
						}
					}
				}
			}
			if len(reasons) > 0 {
				msg = fmt.Sprintf("%s Top candidate %s withheld: %s.", msg, diag.ID, strings.Join(reasons, "; "))
			}
		}
		output.Degraded = []robotNextDegradation{{
			Code:     "no_actionable_recommendation",
			Severity: "info",
			Message:  msg,
			Repair:   "Use br ready --json or scripts/br_retry.sh actionable --json for authoritative claim candidates.",
		}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	top, diagnostic, unsafePickReasons, ok := robotNextClaimablePick(triage.QuickRef.TopPicks, graphIssues, ctx.Readiness, now)
	if !ok {
		output.Message = "No claim command emitted because the top recommendation was not claim-safe"
		output.DiagnosticTopPick = diagnostic
		output.Degraded = []robotNextDegradation{{
			Code:     "robot_next_claim_unsafe",
			Severity: "warning",
			Message:  strings.Join(unsafePickReasons, "; "),
			Repair:   "Use the authoritative Beads actionable queue plus claim gate before claiming work.",
		}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	if unsafeReasons := triage.Status.ClaimUnsafeReasons(); len(unsafeReasons) > 0 {
		output.Message = "No claim command emitted because triage metrics were incomplete"
		output.DiagnosticTopPick = diagnostic
		output.Degraded = []robotNextDegradation{{
			Code:     "robot_next_metric_incomplete",
			Severity: "warning",
			Message:  strings.Join(unsafeReasons, "; "),
			Repair:   "Retry bv --robot-next after graph metrics are available, or use the authoritative Beads actionable queue plus claim gate.",
		}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	issue := robotNextIssueIndex(graphIssues)[top.ID]
	actions := issue.Actions(true)
	output.Actions = &actions
	if actions.Show != nil {
		output.ShowCmd = actions.Show.Shell
	}
	if actions.Claim == nil {
		output.Message = "No claim command emitted: " + actions.UnavailableReason
		output.DiagnosticTopPick = diagnostic
		output.Degraded = []robotNextDegradation{{Code: "live_action_route_unavailable", Severity: "info", Message: actions.UnavailableReason}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}
	output.Actionable = true
	output.ID = top.ID
	output.Title = top.Title
	output.Score = top.Score
	output.Reasons = top.Reasons
	output.Unblocks = top.Unblocks
	output.ClaimCmd = actions.Claim.Shell

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding robot-next: %w", err)
	}
	return nil
}

func handleRobotHistory(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	opts := correlation.CorrelatorOptions{Limit: 500, Revision: ctx.AsOfCommit}
	if cfg.BeadHistoryFlag != nil {
		opts.BeadID = *cfg.BeadHistoryFlag
	}
	if cfg.HistoryLimit != nil {
		opts.Limit = *cfg.HistoryLimit
	}
	if cfg.HistorySince != nil && strings.TrimSpace(*cfg.HistorySince) != "" {
		since, err := recipe.ParseRelativeTime(*cfg.HistorySince, ctx.ReferenceNow())
		if err != nil {
			return fmt.Errorf("parsing --history-since: %w", err)
		}
		if !since.IsZero() {
			opts.Since = &since
		}
	}

	report, err := generateCorrelationReportForContext(ctx, workDir, opts)
	if err != nil {
		return err
	}
	report.GeneratedAt = ctx.ReferenceNow()

	effectiveMinConf := 0.0
	if cfg.MinConfidence != nil && *cfg.MinConfidence > effectiveMinConf {
		effectiveMinConf = *cfg.MinConfidence
	}
	if cfg.RobotMinConf != nil && *cfg.RobotMinConf > effectiveMinConf {
		effectiveMinConf = *cfg.RobotMinConf
	}
	if effectiveMinConf > 0 {
		scorer := correlation.NewScorer()
		report.Histories = scorer.FilterHistoriesByConfidence(report.Histories, effectiveMinConf)
		report.CommitIndex = correlation.BuildCommitIndex(report.Histories)
		report.Stats = recomputeHistoryStatsFiltered(report.Histories, report.Stats.Strategies, report.Stats.FeedbackApplied, true)
	}

	// Same top-level keys as the report plus the shared envelope (source,
	// scope, as_of). Copy fields rather than embedding: HistoryReport defines
	// generated_at and data_hash too, and colliding embedded keys are dropped.
	output := struct {
		RobotEnvelope
		GitRange        string                             `json:"git_range"`
		LatestCommitSHA string                             `json:"latest_commit_sha,omitempty"`
		Window          *correlation.HistoryWindow         `json:"window,omitempty"`
		Stats           correlation.HistoryStats           `json:"stats"`
		Histories       map[string]correlation.BeadHistory `json:"histories"`
		CommitIndex     correlation.CommitIndex            `json:"commit_index"`
	}{
		RobotEnvelope:   ctx.EnvelopeWithHash(report.DataHash),
		GitRange:        report.GitRange,
		LatestCommitSHA: report.LatestCommitSHA,
		Window:          report.Window,
		Stats:           report.Stats,
		Histories:       report.Histories,
		CommitIndex:     report.CommitIndex,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding history report: %w", err)
	}
	return nil
}

func hasSQLiteOrMetadataBeadsStore(beadsDir string) bool {
	for _, name := range []string{"beads.db", "metadata.json"} {
		if info, err := os.Stat(filepath.Join(beadsDir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func resolveSQLiteStoreJSONLPath(beadsDir string) string {
	if raw, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json")); err == nil {
		var meta struct {
			JSONLExport string `json:"jsonl_export"`
		}
		if err := json.Unmarshal(raw, &meta); err == nil {
			if exportName := strings.TrimSpace(meta.JSONLExport); exportName != "" {
				if filepath.IsAbs(exportName) {
					return exportName
				}
				return filepath.Join(beadsDir, exportName)
			}
		}
	}
	return filepath.Join(beadsDir, "issues.jsonl")
}

func resolveCorrelationBeadsPath(workDir string) (string, string, error) {
	beadsDir, err := loader.GetBeadsDir(workDir)
	if err != nil {
		return "", "", fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		if hasSQLiteOrMetadataBeadsStore(beadsDir) {
			return beadsDir, resolveSQLiteStoreJSONLPath(beadsDir), nil
		}
		return "", "", fmt.Errorf("finding beads file: %w", err)
	}
	return beadsDir, beadsPath, nil
}

func resolveCorrelationBeadsPathForContext(ctx RobotContext, workDir string) (string, error) {
	if ctx.AsOfCommit != "" {
		// The historical loader has already selected the authoritative file at
		// this revision. Today's preferred filename may not exist there, and
		// no working-tree beads file is required for a historical query.
		var beadsPath string
		if ctx.SourceAuthority != nil {
			for _, source := range ctx.SourceAuthority.Sources {
				path, matches := strings.CutSuffix(source.SourcePath, "@"+ctx.AsOfCommit)
				if source.SourceKind == "git" && source.Status == "loaded" && matches && path != "" {
					if beadsPath != "" {
						return "", fmt.Errorf("multiple historical sources for causal analysis")
					}
					beadsPath = path
				}
			}
			if beadsPath == "" {
				return "", fmt.Errorf("missing resolved historical source for causal analysis")
			}
			return beadsPath, nil
		}
	}
	beadsDir, _ := loader.GetBeadsDir(workDir)
	if err := correlation.ValidateRepositoryWithBeadsDir(workDir, beadsDir); err != nil {
		return "", err
	}
	_, beadsPath, err := resolveCorrelationBeadsPath(workDir)
	if err != nil {
		return "", err
	}
	return beadsPath, nil
}

func buildCorrelationBeadInfos(issues []model.Issue) []correlation.BeadInfo {
	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{
			ID:     issue.ID,
			Title:  issue.Title,
			Status: string(issue.Status),
		}
	}
	return beadInfos
}

// generateCorrelationReport builds the feedback-aware history report every
// read path (history, explain, orphans, related, impact network, causality)
// consumes: stored rejections are absent from it and confirmations are pinned.
func generateCorrelationReport(workDir string, issues []model.Issue, opts correlation.CorrelatorOptions) (*correlation.HistoryReport, error) {
	_, beadsPath, err := resolveCorrelationBeadsPath(workDir)
	if err != nil {
		return nil, err
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return nil, err
	}
	report, err := correlator.GenerateReportCached(buildCorrelationBeadInfos(issues), opts)
	if err != nil {
		return nil, fmt.Errorf("generating history report: %w", err)
	}
	return report, nil
}

func recomputeHistoryStats(histories map[string]correlation.BeadHistory, strategies []correlation.StrategyRun, fb *correlation.FeedbackApplied) correlation.HistoryStats {
	return recomputeHistoryStatsFiltered(histories, strategies, fb, false)
}

func recomputeHistoryStatsFiltered(histories map[string]correlation.BeadHistory, strategies []correlation.StrategyRun, fb *correlation.FeedbackApplied, onlyWithCommits bool) correlation.HistoryStats {
	stats := correlation.HistoryStats{
		TotalBeads:         len(histories),
		MethodDistribution: make(map[string]int),
		Strategies:         strategies,
		FeedbackApplied:    fb,
	}
	authors := make(map[string]bool)
	uniqueCommits := make(map[string]bool)
	var cycleTimes []time.Duration

	for _, history := range histories {
		if len(history.Commits) > 0 {
			stats.BeadsWithCommits++
		} else if onlyWithCommits {
			continue
		}
		for _, commit := range history.Commits {
			uniqueCommits[commit.SHA] = true
			if commit.Author != "" {
				authors[commit.Author] = true
			}
			for _, method := range commit.AllMethods() {
				stats.MethodDistribution[method]++
			}
			if commit.Confirmed {
				stats.MethodDistribution[correlation.MethodDistributionConfirmedByFeedback]++
			}
		}
		for _, event := range history.Events {
			if event.Author != "" {
				authors[event.Author] = true
			}
		}
		if history.CycleTime != nil && history.CycleTime.ClaimToClose != nil {
			cycleTimes = append(cycleTimes, *history.CycleTime.ClaimToClose)
		}
	}

	stats.TotalCommits = len(uniqueCommits)
	stats.UniqueAuthors = len(authors)
	if stats.BeadsWithCommits > 0 {
		stats.AvgCommitsPerBead = float64(stats.TotalCommits) / float64(stats.BeadsWithCommits)
	}
	if len(cycleTimes) > 0 {
		var total time.Duration
		for _, ct := range cycleTimes {
			total += ct
		}
		avgDays := total.Hours() / 24 / float64(len(cycleTimes))
		stats.AvgCycleTimeDays = &avgDays
	}
	return stats
}

type workspaceCorrelationSource struct {
	source    RobotSourceReport
	repoRoot  string
	beadsPath string
	repoLabel string
}

func findEnclosingGitRoot(startPath string) (string, error) {
	if strings.TrimSpace(startPath) == "" {
		return "", fmt.Errorf("empty start path")
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(abs); statErr == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	dir := filepath.Clean(abs)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a git repository: %s", startPath)
		}
		dir = parent
	}
}

func validateWorkspaceCorrelationStore(gitRoot, beadsDir string) error {
	if strings.TrimSpace(gitRoot) == "" {
		return fmt.Errorf("empty git root")
	}
	if _, err := os.Stat(filepath.Join(gitRoot, ".git")); err != nil {
		return fmt.Errorf("missing .git at %s: %w", gitRoot, err)
	}
	if strings.TrimSpace(beadsDir) == "" {
		return fmt.Errorf("empty beads directory")
	}
	for _, marker := range []string{"issues.jsonl", "beads.jsonl", "beads.base.jsonl", "beads.db", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(beadsDir, marker)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no tracker files found in %s", beadsDir)
}

func resolveSourceBeadsDir(src RobotSourceReport) string {
	sp := strings.TrimSpace(src.SourcePath)
	if sp != "" {
		if info, err := os.Stat(sp); err == nil {
			if info.IsDir() {
				return filepath.Clean(sp)
			}
			return filepath.Clean(filepath.Dir(sp))
		}
		lower := strings.ToLower(filepath.Base(sp))
		if strings.HasSuffix(lower, ".jsonl") || strings.HasSuffix(lower, ".db") {
			return filepath.Clean(filepath.Dir(sp))
		}
		return filepath.Clean(sp)
	}
	if strings.TrimSpace(src.RepoRoot) != "" {
		return filepath.Clean(filepath.Join(src.RepoRoot, ".beads"))
	}
	return ""
}

func resolveWorkspaceCorrelationSources(ctx RobotContext) ([]workspaceCorrelationSource, bool) {
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		return nil, false
	}
	var resolved []workspaceCorrelationSource
	distinctRoots := make(map[string]bool)
	for _, src := range ctx.SourceAuthority.Sources {
		if src.Status != "loaded" {
			continue
		}
		beadsDir := resolveSourceBeadsDir(src)
		startPath := strings.TrimSpace(src.RepoRoot)
		if startPath == "" {
			startPath = beadsDir
		}
		gitRoot, err := findEnclosingGitRoot(startPath)
		if err != nil {
			continue
		}
		var beadsPath string
		if strings.HasSuffix(strings.ToLower(src.SourcePath), ".jsonl") {
			beadsPath = src.SourcePath
		} else if beadsDir != "" {
			beadsPath = resolveSQLiteStoreJSONLPath(beadsDir)
		} else if _, bp, err := resolveCorrelationBeadsPath(gitRoot); err == nil {
			beadsPath = bp
			beadsDir = filepath.Dir(bp)
		}
		if beadsPath == "" || validateWorkspaceCorrelationStore(gitRoot, beadsDir) != nil {
			continue
		}
		label := strings.TrimSpace(src.Name)
		if label == "" {
			label = src.SourceRepo
		}
		distinctRoots[filepath.Clean(gitRoot)] = true
		resolved = append(resolved, workspaceCorrelationSource{
			source:    src,
			repoRoot:  gitRoot,
			beadsPath: beadsPath,
			repoLabel: label,
		})
	}
	return resolved, len(distinctRoots) > 1
}

func localBeadIDForWorkspaceIssue(issue model.Issue, src RobotSourceReport) string {
	if issue.Origin != nil && strings.TrimSpace(issue.Origin.LocalID) != "" {
		return issue.Origin.LocalID
	}
	if src.RepoPrefix != "" && len(issue.ID) > len(src.RepoPrefix) && strings.EqualFold(issue.ID[:len(src.RepoPrefix)], src.RepoPrefix) {
		return issue.ID[len(src.RepoPrefix):]
	}
	return issue.ID
}

func qualifyWorkspaceHistoricalID(localID string, src RobotSourceReport, localToQualified map[string]string, knownQualified map[string]bool) string {
	if localID == "" {
		return ""
	}
	if q, ok := localToQualified[localID]; ok {
		return q
	}
	if knownQualified[localID] {
		return localID
	}
	return workspace.QualifyID(localID, src.RepoPrefix)
}

func remapHistoricalIssueState(state *correlation.HistoricalIssueState, src RobotSourceReport, localToQualified map[string]string, knownQualified map[string]bool) *correlation.HistoricalIssueState {
	if state == nil {
		return nil
	}
	cp := *state
	cp.ID = qualifyWorkspaceHistoricalID(cp.ID, src, localToQualified, knownQualified)
	if len(state.Dependencies) > 0 {
		cp.Dependencies = make([]correlation.HistoricalDependency, len(state.Dependencies))
		for i, dep := range state.Dependencies {
			cp.Dependencies[i] = correlation.HistoricalDependency{
				DependsOnID: qualifyWorkspaceHistoricalID(dep.DependsOnID, src, localToQualified, knownQualified),
				Type:        dep.Type,
			}
		}
	}
	return &cp
}

func remapWorkspaceBeadEvent(ev correlation.BeadEvent, src RobotSourceReport, localToQualified map[string]string, knownQualified map[string]bool) correlation.BeadEvent {
	ev.BeadID = qualifyWorkspaceHistoricalID(ev.BeadID, src, localToQualified, knownQualified)
	ev.Before = remapHistoricalIssueState(ev.Before, src, localToQualified, knownQualified)
	ev.After = remapHistoricalIssueState(ev.After, src, localToQualified, knownQualified)
	return ev
}

func generateWorkspaceCorrelationReport(ctx RobotContext, opts correlation.CorrelatorOptions, raw bool) (*correlation.HistoryReport, error) {
	return generateWorkspaceCorrelationReportWithContext(context.Background(), ctx, opts, raw)
}

type workspaceSharedGitMeta struct {
	ambiguousByRoot map[string]map[string]bool
	subtreeByKey    map[string]string
	siblingsByKey   map[string][]string
	storeDirsByRoot map[string][]string
}

func workspaceSourceIdentityKey(ws workspaceCorrelationSource) string {
	return filepath.Clean(ws.repoRoot) + "|" + filepath.Clean(ws.beadsPath) + "|" + strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
}

func storeDirRelForWorkspaceSource(ws workspaceCorrelationSource) string {
	cleanRoot := filepath.Clean(ws.repoRoot)
	storeDir := filepath.Clean(filepath.Dir(ws.beadsPath))
	rel, err := filepath.Rel(cleanRoot, storeDir)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Trim(filepath.ToSlash(rel), "/")
}

func storeSubtreePrefixForWorkspaceSource(ws workspaceCorrelationSource) string {
	cleanRoot := filepath.Clean(ws.repoRoot)
	storeDir := filepath.Clean(filepath.Dir(ws.beadsPath))
	containerDir := filepath.Dir(storeDir)
	if filepath.Base(containerDir) == ".beads" {
		containerDir = filepath.Dir(containerDir)
	}
	rel, err := filepath.Rel(cleanRoot, containerDir)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Trim(filepath.ToSlash(rel), "/") + "/"
}

func buildWorkspaceSharedGitMeta(sources []workspaceCorrelationSource, allIssues []model.Issue) workspaceSharedGitMeta {
	meta := workspaceSharedGitMeta{
		ambiguousByRoot: make(map[string]map[string]bool),
		subtreeByKey:    make(map[string]string, len(sources)),
		siblingsByKey:   make(map[string][]string, len(sources)),
		storeDirsByRoot: make(map[string][]string),
	}
	issuesByRepo := make(map[string][]model.Issue)
	for _, issue := range allIssues {
		rk := strings.ToLower(strings.TrimSpace(issue.SourceRepo))
		issuesByRepo[rk] = append(issuesByRepo[rk], issue)
	}
	byRoot := make(map[string][]workspaceCorrelationSource)
	for _, ws := range sources {
		cleanRoot := filepath.Clean(ws.repoRoot)
		byRoot[cleanRoot] = append(byRoot[cleanRoot], ws)
		idKey := workspaceSourceIdentityKey(ws)
		meta.subtreeByKey[idKey] = storeSubtreePrefixForWorkspaceSource(ws)
		if dirRel := storeDirRelForWorkspaceSource(ws); dirRel != "" {
			meta.storeDirsByRoot[cleanRoot] = append(meta.storeDirsByRoot[cleanRoot], dirRel)
		}
	}
	for cleanRoot, group := range byRoot {
		if len(group) <= 1 {
			continue
		}
		localIDStoreCounts := make(map[string]int)
		var nonEmptySubtrees []string
		seenSubtree := make(map[string]bool)
		for _, ws := range group {
			idKey := workspaceSourceIdentityKey(ws)
			if sub := meta.subtreeByKey[idKey]; sub != "" && !seenSubtree[sub] {
				seenSubtree[sub] = true
				nonEmptySubtrees = append(nonEmptySubtrees, sub)
			}
			rk := strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
			seenInStore := make(map[string]bool)
			for _, issue := range issuesByRepo[rk] {
				localID := strings.ToLower(strings.TrimSpace(localBeadIDForWorkspaceIssue(issue, ws.source)))
				if localID != "" && !seenInStore[localID] {
					seenInStore[localID] = true
					localIDStoreCounts[localID]++
				}
			}
		}
		sort.Strings(nonEmptySubtrees)
		amb := make(map[string]bool)
		for lid, count := range localIDStoreCounts {
			if count > 1 {
				amb[lid] = true
			}
		}
		if len(amb) > 0 {
			meta.ambiguousByRoot[cleanRoot] = amb
		}
		for _, ws := range group {
			idKey := workspaceSourceIdentityKey(ws)
			if meta.subtreeByKey[idKey] == "" && len(nonEmptySubtrees) > 0 {
				meta.siblingsByKey[idKey] = nonEmptySubtrees
			}
		}
	}
	return meta
}

func buildExplicitAliasesForWorkspaceBeads(issues []model.Issue, src RobotSourceReport) map[string]string {
	var aliases map[string]string
	for _, issue := range issues {
		localID := localBeadIDForWorkspaceIssue(issue, src)
		if issue.ID != "" && localID != "" && !strings.EqualFold(issue.ID, localID) {
			if aliases == nil {
				aliases = make(map[string]string, len(issues))
			}
			aliases[issue.ID] = localID
		}
	}
	return aliases
}

func generateWorkspaceCorrelationReportWithContext(runCtx context.Context, ctx RobotContext, opts correlation.CorrelatorOptions, raw bool) (*correlation.HistoryReport, error) {
	sources, prefixFiles := resolveWorkspaceCorrelationSources(ctx)
	if len(sources) == 0 {
		return nil, fmt.Errorf("no git repositories available in workspace")
	}

	allWorkspaceIssues := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		allWorkspaceIssues = ctx.FullIssues
	}
	sharedMeta := buildWorkspaceSharedGitMeta(sources, allWorkspaceIssues)

	knownQualified := make(map[string]bool, len(allWorkspaceIssues))
	fullIssueByID := make(map[string]model.Issue, len(allWorkspaceIssues))
	for _, issue := range allWorkspaceIssues {
		knownQualified[issue.ID] = true
		fullIssueByID[issue.ID] = issue
	}

	issuesBySourceRepo := make(map[string][]model.Issue)
	for _, issue := range ctx.Issues {
		repoKey := strings.ToLower(strings.TrimSpace(issue.SourceRepo))
		issuesBySourceRepo[repoKey] = append(issuesBySourceRepo[repoKey], issue)
	}

	targetFilterID := strings.TrimSpace(opts.BeadID)
	targetCausalID := strings.TrimSpace(opts.CausalityBeadID)

	mergedHistories := make(map[string]correlation.BeadHistory)
	var allStrategies []correlation.StrategyRun
	var mergedFeedback *correlation.FeedbackApplied
	var mergedCausal *correlation.CausalHistory
	var gitRange string
	var latestSHA string
	var latestTime time.Time
	totalWalkedCommits := 0
	countedWindowRoots := make(map[string]bool)

	for _, ws := range sources {
		if ctx.Repo != "" && !sourceMatchesRepoFilter(ws.source, ctx.Repo) {
			continue
		}
		repoKey := strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
		repoIssues := issuesBySourceRepo[repoKey]

		localToQualified := make(map[string]string, len(repoIssues)+4)
		localBeads := make([]correlation.BeadInfo, 0, len(repoIssues))
		for _, issue := range repoIssues {
			localID := localBeadIDForWorkspaceIssue(issue, ws.source)
			localToQualified[localID] = issue.ID
			localBeads = append(localBeads, correlation.BeadInfo{
				ID:     localID,
				Title:  issue.Title,
				Status: string(issue.Status),
			})
		}

		resolveLocalTarget := func(targetID string) (string, []string, bool) {
			if targetIssue, exists := fullIssueByID[targetID]; exists {
				if !strings.EqualFold(strings.TrimSpace(targetIssue.SourceRepo), repoKey) {
					return "", nil, false
				}
				return localBeadIDForWorkspaceIssue(targetIssue, ws.source), nil, true
			}
			prefix := ws.source.RepoPrefix
			if prefix != "" {
				if len(targetID) > len(prefix) && strings.EqualFold(targetID[:len(prefix)], prefix) {
					stripped := targetID[len(prefix):]
					if !strings.EqualFold(stripped, targetID) {
						return stripped, []string{targetID}, true
					}
					return stripped, nil, true
				}
				return "", nil, false
			}
			return targetID, nil, true
		}

		cleanRoot := filepath.Clean(ws.repoRoot)
		idKey := workspaceSourceIdentityKey(ws)
		repoOpts := opts
		repoOpts.ExplicitIDAliases = buildExplicitAliasesForWorkspaceBeads(repoIssues, ws.source)
		repoOpts.AmbiguousUnprefixedIDs = sharedMeta.ambiguousByRoot[cleanRoot]
		repoOpts.StoreSubtreePrefix = sharedMeta.subtreeByKey[idKey]
		repoOpts.SiblingSubtreePrefixes = sharedMeta.siblingsByKey[idKey]

		if targetFilterID != "" {
			localTarget, aliases, ok := resolveLocalTarget(targetFilterID)
			if !ok {
				continue
			}
			repoOpts.BeadID = localTarget
			repoOpts.TargetBeadAliases = append(append([]string(nil), repoOpts.TargetBeadAliases...), aliases...)
			if localTarget != "" {
				if _, exists := localToQualified[localTarget]; !exists {
					localToQualified[localTarget] = workspace.QualifyID(localTarget, ws.source.RepoPrefix)
				}
			}
			for _, al := range aliases {
				if _, exists := localToQualified[al]; !exists {
					localToQualified[al] = workspace.QualifyID(al, ws.source.RepoPrefix)
				}
			}
		}
		if targetCausalID != "" {
			localTarget, aliases, ok := resolveLocalTarget(targetCausalID)
			if !ok {
				continue
			}
			repoOpts.CausalityBeadID = localTarget
			for _, al := range aliases {
				repoOpts.TargetBeadAliases = append(repoOpts.TargetBeadAliases, al)
				if _, exists := localToQualified[al]; !exists {
					localToQualified[al] = workspace.QualifyID(al, ws.source.RepoPrefix)
				}
			}
			if localTarget != "" {
				if _, exists := localToQualified[localTarget]; !exists {
					localToQualified[localTarget] = workspace.QualifyID(localTarget, ws.source.RepoPrefix)
				}
			}
		}

		var (
			correlator *correlation.Correlator
			err        error
		)
		if raw {
			correlator = correlation.NewCorrelator(ws.repoRoot, ws.beadsPath)
		} else {
			correlator, err = newCorrelatorWithFeedback(ws.repoRoot, ws.beadsPath)
			if err != nil {
				return nil, err
			}
		}
		if runCtx != nil {
			correlator = correlator.WithContext(runCtx)
		}

		part, err := correlator.GenerateReportCached(localBeads, repoOpts)
		if err != nil {
			return nil, fmt.Errorf("correlating workspace repository %q: %w", ws.repoLabel, err)
		}
		if gitRange == "" {
			gitRange = part.GitRange
		}
		if part.Window != nil && !countedWindowRoots[cleanRoot] {
			countedWindowRoots[cleanRoot] = true
			totalWalkedCommits += part.Window.Commits
		}
		allStrategies = append(allStrategies, part.Stats.Strategies...)
		if part.Stats.FeedbackApplied != nil {
			if mergedFeedback == nil {
				mergedFeedback = &correlation.FeedbackApplied{}
			}
			mergedFeedback.Confirmed += part.Stats.FeedbackApplied.Confirmed
			mergedFeedback.Rejected += part.Stats.FeedbackApplied.Rejected
			mergedFeedback.Ignored += part.Stats.FeedbackApplied.Ignored
		}

		for localID, hist := range part.Histories {
			qualID := qualifyWorkspaceHistoricalID(localID, ws.source, localToQualified, knownQualified)
			hist.BeadID = qualID
			events := make([]correlation.BeadEvent, len(hist.Events))
			for i, ev := range hist.Events {
				events[i] = remapWorkspaceBeadEvent(ev, ws.source, localToQualified, knownQualified)
				if events[i].Timestamp.After(latestTime) {
					latestTime = events[i].Timestamp
					latestSHA = events[i].CommitSHA
				}
			}
			hist.Events = events
			hist.Milestones = correlation.GetBeadMilestones(hist.Events)

			commits := make([]correlation.CorrelatedCommit, len(hist.Commits))
			for i, cc := range hist.Commits {
				cc.BeadID = qualID
				if prefixFiles && ws.repoLabel != "" && len(cc.Files) > 0 {
					files := make([]correlation.FileChange, len(cc.Files))
					for j, f := range cc.Files {
						f.Path = path.Join(ws.repoLabel, strings.TrimPrefix(filepath.ToSlash(f.Path), "./"))
						files[j] = f
					}
					cc.Files = files
				}
				if cc.Timestamp.After(latestTime) {
					latestTime = cc.Timestamp
					latestSHA = cc.SHA
				}
				commits[i] = cc
			}
			hist.Commits = commits
			mergedHistories[qualID] = hist
		}

		if part.CausalHistory != nil {
			ch := *part.CausalHistory
			ch.BeadID = qualifyWorkspaceHistoricalID(ch.BeadID, ws.source, localToQualified, knownQualified)
			obs := make([]correlation.CausalObservation, len(ch.Observations))
			for i, o := range ch.Observations {
				o.Before.Issue = remapHistoricalIssueState(o.Before.Issue, ws.source, localToQualified, knownQualified)
				if len(o.Before.Blockers) > 0 {
					b := make([]string, len(o.Before.Blockers))
					for j, id := range o.Before.Blockers {
						b[j] = qualifyWorkspaceHistoricalID(id, ws.source, localToQualified, knownQualified)
					}
					o.Before.Blockers = b
				}
				o.After.Issue = remapHistoricalIssueState(o.After.Issue, ws.source, localToQualified, knownQualified)
				if len(o.After.Blockers) > 0 {
					b := make([]string, len(o.After.Blockers))
					for j, id := range o.After.Blockers {
						b[j] = qualifyWorkspaceHistoricalID(id, ws.source, localToQualified, knownQualified)
					}
					o.After.Blockers = b
				}
				if len(o.Changes) > 0 {
					changes := make([]correlation.BeadEvent, len(o.Changes))
					for j, ev := range o.Changes {
						changes[j] = remapWorkspaceBeadEvent(ev, ws.source, localToQualified, knownQualified)
					}
					o.Changes = changes
				}
				obs[i] = o
			}
			ch.Observations = obs
			mergedCausal = &ch
		}

		if latestSHA == "" && part.LatestCommitSHA != "" {
			latestSHA = part.LatestCommitSHA
		}
	}

	if opts.Revision != "" {
		latestSHA = opts.Revision
	}
	dataHash := ctx.DataHash
	if dataHash == "" {
		dataHash = analysis.ComputeDataHash(ctx.Issues)
	}
	commitIndex := correlation.BuildCommitIndex(mergedHistories)
	stats := recomputeHistoryStats(mergedHistories, allStrategies, mergedFeedback)

	return &correlation.HistoryReport{
		GeneratedAt:     robotNow(),
		DataHash:        dataHash,
		GitRange:        gitRange,
		LatestCommitSHA: latestSHA,
		Window: &correlation.HistoryWindow{
			Revision: opts.Revision,
			Limit:    opts.Limit,
			Since:    opts.Since,
			Until:    opts.Until,
			Commits:  totalWalkedCommits,
		},
		Stats:         stats,
		Histories:     mergedHistories,
		CommitIndex:   commitIndex,
		CausalHistory: mergedCausal,
	}, nil
}

func resolveWorkspaceFeedbackStoreForBead(ctx RobotContext, workDir, beadID string) (*correlation.FeedbackStore, string, error) {
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		store, err := loadCorrelationFeedbackStore(workDir)
		return store, beadID, err
	}
	sources, _ := resolveWorkspaceCorrelationSources(ctx)
	allIssues := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		allIssues = ctx.FullIssues
	}
	for _, issue := range allIssues {
		if issue.ID != beadID {
			continue
		}
		for _, ws := range sources {
			if strings.EqualFold(strings.TrimSpace(issue.SourceRepo), strings.TrimSpace(ws.source.SourceRepo)) {
				store, err := loadCorrelationFeedbackStoreForBeadsPath(ws.repoRoot, ws.beadsPath)
				if err != nil {
					return nil, "", err
				}
				return store, localBeadIDForWorkspaceIssue(issue, ws.source), nil
			}
		}
	}
	// Fallback: match by workspace source prefix for deleted/tombstoned beads absent from allIssues.
	for _, ws := range sources {
		prefix := ws.source.RepoPrefix
		if prefix != "" && len(beadID) > len(prefix) && strings.EqualFold(beadID[:len(prefix)], prefix) {
			store, err := loadCorrelationFeedbackStoreForBeadsPath(ws.repoRoot, ws.beadsPath)
			if err != nil {
				return nil, "", err
			}
			stripped := beadID[len(prefix):]
			if !strings.EqualFold(stripped, beadID) {
				if part, corrErr := correlation.NewCorrelator(ws.repoRoot, ws.beadsPath).GenerateReportCached(nil, correlation.CorrelatorOptions{
					BeadID:            stripped,
					TargetBeadAliases: []string{beadID},
					Revision:          ctx.AsOfCommit,
				}); corrErr == nil {
					if _, ok := part.Histories[beadID]; ok {
						return store, beadID, nil
					}
				}
			}
			return store, stripped, nil
		}
	}
	return nil, "", fmt.Errorf("workspace repository not found for bead %q", beadID)
}

func loadWorkspaceCorrelationFeedbackStats(ctx RobotContext, workDir string) (correlation.FeedbackStats, error) {
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		store, err := loadCorrelationFeedbackStore(workDir)
		if err != nil {
			return correlation.FeedbackStats{}, err
		}
		return store.GetStats(), nil
	}
	sources, _ := resolveWorkspaceCorrelationSources(ctx)
	if len(sources) == 0 {
		return correlation.FeedbackStats{}, fmt.Errorf("no git repositories available in workspace")
	}
	var agg correlation.FeedbackStats
	var confirmConfSum, rejectConfSum float64
	seenDirs := make(map[string]bool)
	for _, ws := range sources {
		if ctx.Repo != "" && !sourceMatchesRepoFilter(ws.source, ctx.Repo) {
			continue
		}
		var beadsDir string
		if strings.TrimSpace(ws.beadsPath) != "" {
			beadsDir = filepath.Dir(ws.beadsPath)
		} else {
			var err error
			beadsDir, err = loader.GetBeadsDir(ws.repoRoot)
			if err != nil {
				continue
			}
		}
		cleanDir := filepath.Clean(beadsDir)
		if seenDirs[cleanDir] {
			continue
		}
		seenDirs[cleanDir] = true
		store := correlation.NewFeedbackStore(cleanDir)
		if err := store.Load(); err != nil {
			return correlation.FeedbackStats{}, fmt.Errorf("loading feedback for %s: %w", ws.repoLabel, err)
		}
		st := store.GetStats()
		agg.TotalFeedback += st.TotalFeedback
		agg.Confirmed += st.Confirmed
		agg.Rejected += st.Rejected
		agg.Ignored += st.Ignored
		confirmConfSum += st.AvgConfirmConf * float64(st.Confirmed)
		rejectConfSum += st.AvgRejectConf * float64(st.Rejected)
	}
	if denom := agg.Confirmed + agg.Rejected; denom > 0 {
		agg.AccuracyRate = float64(agg.Confirmed) / float64(denom)
	}
	if agg.Confirmed > 0 {
		agg.AvgConfirmConf = confirmConfSum / float64(agg.Confirmed)
	}
	if agg.Rejected > 0 {
		agg.AvgRejectConf = rejectConfSum / float64(agg.Rejected)
	}
	return agg, nil
}

func detectWorkspaceOrphans(ctx RobotContext, limit int) (*correlation.OrphanReport, error) {
	sources, prefixFiles := resolveWorkspaceCorrelationSources(ctx)
	if len(sources) == 0 {
		return nil, fmt.Errorf("no git repositories available in workspace")
	}
	allWorkspaceIssues := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		allWorkspaceIssues = ctx.FullIssues
	}
	sharedMeta := buildWorkspaceSharedGitMeta(sources, allWorkspaceIssues)

	knownQualified := make(map[string]bool, len(allWorkspaceIssues))
	fullIssuesBySourceRepo := make(map[string][]model.Issue)
	for _, issue := range allWorkspaceIssues {
		knownQualified[issue.ID] = true
		key := strings.ToLower(strings.TrimSpace(issue.SourceRepo))
		fullIssuesBySourceRepo[key] = append(fullIssuesBySourceRepo[key], issue)
	}

	scopedIssueIDs := make(map[string]bool, len(ctx.Issues))
	for _, issue := range ctx.Issues {
		scopedIssueIDs[issue.ID] = true
	}

	var (
		allCandidates []correlation.OrphanCandidate
		byBead        = make(map[string][]string)
		aggStats      correlation.OrphanReportStats
		aggWindow     correlation.OrphanWindow
		gitRange      string
		usageHints    []string
	)
	var rootOrder []string
	sourcesByRoot := make(map[string][]workspaceCorrelationSource)
	for _, ws := range sources {
		cleanRoot := filepath.Clean(ws.repoRoot)
		if _, exists := sourcesByRoot[cleanRoot]; !exists {
			rootOrder = append(rootOrder, cleanRoot)
		}
		sourcesByRoot[cleanRoot] = append(sourcesByRoot[cleanRoot], ws)
	}
	for _, cleanRoot := range rootOrder {
		group := sourcesByRoot[cleanRoot]
		if ctx.Repo != "" {
			anyMatch := false
			for _, ws := range group {
				if sourceMatchesRepoFilter(ws.source, ctx.Repo) {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				continue
			}
		}

		primary := group[0]
		for _, ws := range group {
			if ctx.Repo == "" || sourceMatchesRepoFilter(ws.source, ctx.Repo) {
				primary = ws
				break
			}
		}
		rootHistories := make(map[string]correlation.BeadHistory)
		var rootWindow *correlation.HistoryWindow
		var rootGitRange string
		var rootFeedback *correlation.FeedbackApplied
		var matchedSubtrees []string
		var unmatchedSubtrees []string
		matchedRootStore := false

		for _, ws := range group {
			idKey := workspaceSourceIdentityKey(ws)
			sub := sharedMeta.subtreeByKey[idKey]
			if ctx.Repo != "" {
				if sourceMatchesRepoFilter(ws.source, ctx.Repo) {
					if sub == "" {
						matchedRootStore = true
					} else {
						matchedSubtrees = append(matchedSubtrees, sub)
					}
				} else if sub != "" {
					unmatchedSubtrees = append(unmatchedSubtrees, sub)
				}
			}

			repoKey := strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
			repoIssues := fullIssuesBySourceRepo[repoKey]
			localToQualified := make(map[string]string, len(repoIssues))
			localBeads := make([]correlation.BeadInfo, 0, len(repoIssues))
			for _, issue := range repoIssues {
				localID := localBeadIDForWorkspaceIssue(issue, ws.source)
				localToQualified[localID] = issue.ID
				localBeads = append(localBeads, correlation.BeadInfo{
					ID:     localID,
					Title:  issue.Title,
					Status: string(issue.Status),
				})
			}

			correlator, err := newCorrelatorWithFeedback(cleanRoot, ws.beadsPath)
			if err != nil {
				return nil, err
			}
			part, err := correlator.GenerateReportCached(localBeads, correlation.CorrelatorOptions{
				Limit:                  limit,
				Revision:               ctx.AsOfCommit,
				ExplicitIDAliases:      buildExplicitAliasesForWorkspaceBeads(repoIssues, ws.source),
				AmbiguousUnprefixedIDs: sharedMeta.ambiguousByRoot[cleanRoot],
				StoreSubtreePrefix:     sub,
				SiblingSubtreePrefixes: sharedMeta.siblingsByKey[idKey],
			})
			if err != nil {
				return nil, fmt.Errorf("correlating workspace repository %q: %w", ws.repoLabel, err)
			}
			if rootGitRange == "" {
				rootGitRange = part.GitRange
			}
			if rootWindow == nil && part.Window != nil {
				cp := *part.Window
				rootWindow = &cp
			}
			if part.Stats.FeedbackApplied != nil {
				if rootFeedback == nil {
					rootFeedback = &correlation.FeedbackApplied{}
				}
				rootFeedback.Confirmed += part.Stats.FeedbackApplied.Confirmed
				rootFeedback.Rejected += part.Stats.FeedbackApplied.Rejected
				rootFeedback.Ignored += part.Stats.FeedbackApplied.Ignored
			}
			for localID, hist := range part.Histories {
				qualID := qualifyWorkspaceHistoricalID(localID, ws.source, localToQualified, knownQualified)
				hist.BeadID = qualID
				events := make([]correlation.BeadEvent, len(hist.Events))
				for i, ev := range hist.Events {
					events[i] = remapWorkspaceBeadEvent(ev, ws.source, localToQualified, knownQualified)
				}
				hist.Events = events
				hist.Milestones = correlation.GetBeadMilestones(hist.Events)

				commits := make([]correlation.CorrelatedCommit, len(hist.Commits))
				for i, cc := range hist.Commits {
					cc.BeadID = qualID
					commits[i] = cc
				}
				hist.Commits = commits
				rootHistories[qualID] = hist
			}
		}

		scopedHistories := rootHistories
		if len(scopedIssueIDs) < len(allWorkspaceIssues) {
			scopedHistories = make(map[string]correlation.BeadHistory, len(scopedIssueIDs))
			for qualID, hist := range rootHistories {
				if scopedIssueIDs[qualID] {
					scopedHistories[qualID] = hist
				}
			}
		}

		rootReport := &correlation.HistoryReport{
			GeneratedAt: robotNow(),
			DataHash:    ctx.DataHash,
			GitRange:    rootGitRange,
			Window:      rootWindow,
			Stats:       correlation.HistoryStats{FeedbackApplied: rootFeedback},
			Histories:   scopedHistories,
			CommitIndex: correlation.BuildCommitIndex(rootHistories),
		}
		detector := correlation.NewOrphanDetectorAt(rootReport, cleanRoot, ctx.ReferenceNow()).
			WithStoreDirs(sharedMeta.storeDirsByRoot[cleanRoot]...)
		if ctx.Repo != "" && len(group) > 1 {
			if matchedRootStore && len(unmatchedSubtrees) > 0 {
				detector.WithSubtreeScope(nil, unmatchedSubtrees)
			} else if !matchedRootStore && len(matchedSubtrees) > 0 {
				detector.WithSubtreeScope(matchedSubtrees, nil)
			}
		}
		partOrphans, err := detector.DetectOrphans(correlation.ExtractOptions{Limit: limit, Revision: ctx.AsOfCommit})
		if err != nil {
			return nil, fmt.Errorf("detecting orphans in %q: %w", primary.repoLabel, err)
		}
		if gitRange == "" {
			gitRange = partOrphans.GitRange
		}
		if len(usageHints) == 0 {
			usageHints = partOrphans.UsageHints
		}
		if aggWindow.Source == "" {
			aggWindow = partOrphans.Window
		} else {
			aggWindow.Commits += partOrphans.Window.Commits
		}

		droppedScopedOrphans := 0
		for _, cand := range partOrphans.Candidates {
			if ctx.Repo != "" {
				filteredProbable := make([]correlation.ProbableBead, 0, len(cand.ProbableBeads))
				for _, pb := range cand.ProbableBeads {
					if scopedIssueIDs[pb.BeadID] {
						filteredProbable = append(filteredProbable, pb)
					}
				}
				cand.ProbableBeads = filteredProbable

				if len(group) > 1 && len(cand.Files) > 0 {
					touchesInScope := false
					if matchedRootStore {
						for _, f := range cand.Files {
							normF := strings.TrimPrefix(filepath.ToSlash(f), "./")
							inSibling := false
							for _, sib := range unmatchedSubtrees {
								if strings.HasPrefix(normF, sib) {
									inSibling = true
									break
								}
							}
							if !inSibling {
								touchesInScope = true
								break
							}
						}
					} else if len(matchedSubtrees) > 0 {
						for _, f := range cand.Files {
							normF := strings.TrimPrefix(filepath.ToSlash(f), "./")
							for _, sub := range matchedSubtrees {
								if strings.HasPrefix(normF, sub) {
									touchesInScope = true
									break
								}
							}
							if touchesInScope {
								break
							}
						}
					} else {
						touchesInScope = true
					}
					if !touchesInScope {
						droppedScopedOrphans++
						continue
					}
				}
			}

			if prefixFiles && len(group) == 1 && primary.repoLabel != "" && len(cand.Files) > 0 {
				files := make([]string, len(cand.Files))
				for i, f := range cand.Files {
					files[i] = path.Join(primary.repoLabel, strings.TrimPrefix(filepath.ToSlash(f), "./"))
				}
				cand.Files = files
			}
			for i := range cand.ProbableBeads {
				qualID := cand.ProbableBeads[i].BeadID
				byBead[qualID] = append(byBead[qualID], cand.ShortSHA)
			}
			allCandidates = append(allCandidates, cand)
		}

		aggStats.TotalCommits += partOrphans.Stats.TotalCommits - droppedScopedOrphans
		aggStats.CorrelatedCount += partOrphans.Stats.CorrelatedCount
		aggStats.OrphanCount += partOrphans.Stats.OrphanCount - droppedScopedOrphans
		aggStats.BeadsOnlyCommits += partOrphans.Stats.BeadsOnlyCommits
	}

	sort.Slice(allCandidates, func(i, j int) bool {
		if allCandidates[i].SuspicionScore != allCandidates[j].SuspicionScore {
			return allCandidates[i].SuspicionScore > allCandidates[j].SuspicionScore
		}
		if !allCandidates[i].Timestamp.Equal(allCandidates[j].Timestamp) {
			return allCandidates[i].Timestamp.After(allCandidates[j].Timestamp)
		}
		return allCandidates[i].SHA < allCandidates[j].SHA
	})
	aggStats.CandidateCount = len(allCandidates)
	if aggStats.TotalCommits > 0 {
		aggStats.OrphanRatio = float64(aggStats.OrphanCount) / float64(aggStats.TotalCommits)
	}
	if len(allCandidates) > 0 {
		totalSusp := 0
		for _, c := range allCandidates {
			totalSusp += c.SuspicionScore
		}
		aggStats.AvgSuspicion = float64(totalSusp) / float64(len(allCandidates))
	}
	dataHash := ctx.DataHash
	if dataHash == "" {
		dataHash = analysis.ComputeDataHash(ctx.Issues)
	}
	if allCandidates == nil {
		allCandidates = []correlation.OrphanCandidate{}
	}
	return &correlation.OrphanReport{
		GeneratedAt: robotNow(),
		GitRange:    gitRange,
		DataHash:    dataHash,
		Window:      aggWindow,
		Stats:       aggStats,
		Candidates:  allCandidates,
		ByBead:      byBead,
		UsageHints:  usageHints,
	}, nil
}

func scopedCorrelationIssues(ctx RobotContext) []model.Issue {
	if ctx.CandidateIDs == nil {
		return ctx.Issues
	}
	scoped := make([]model.Issue, 0, len(ctx.CandidateIDs))
	for _, iss := range ctx.Issues {
		if ctx.CandidateIDs[iss.ID] {
			scoped = append(scoped, iss)
		}
	}
	return scoped
}

func generateCorrelationReportForContext(ctx RobotContext, workDir string, opts correlation.CorrelatorOptions) (*correlation.HistoryReport, error) {
	if opts.Revision == "" {
		opts.Revision = ctx.AsOfCommit
	}
	scopedIssues := scopedCorrelationIssues(ctx)
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		wsCtx := ctx
		wsCtx.Issues = scopedIssues
		return generateWorkspaceCorrelationReport(wsCtx, opts, false)
	}
	beadsPath, err := resolveCorrelationBeadsPathForContext(ctx, workDir)
	if err != nil {
		return nil, err
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return nil, err
	}
	report, err := correlator.GenerateReportCached(buildCorrelationBeadInfos(scopedIssues), opts)
	if err != nil {
		return nil, fmt.Errorf("generating history report: %w", err)
	}
	return report, nil
}

// generateRawCorrelationReport builds the history report WITHOUT applying the
// feedback store. Only the confirm/reject handler uses it: feedback is a
// decision about a raw correlation, so the target must still be resolvable
// after an earlier rejection (letting a rejection be flipped) and orig_conf
// must record the strategy confidence, not a previously pinned 1.0.
func generateRawCorrelationReport(ctx RobotContext, workDir string, issues []model.Issue, opts correlation.CorrelatorOptions) (*correlation.HistoryReport, error) {
	if opts.Revision == "" {
		opts.Revision = ctx.AsOfCommit
	}
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		wsCtx := ctx
		wsCtx.Issues = issues
		return generateWorkspaceCorrelationReport(wsCtx, opts, true)
	}
	beadsPath, err := resolveCorrelationBeadsPathForContext(ctx, workDir)
	if err != nil {
		return nil, err
	}
	report, err := correlation.NewCorrelator(workDir, beadsPath).GenerateReportCached(buildCorrelationBeadInfos(issues), opts)
	if err != nil {
		return nil, fmt.Errorf("generating history report: %w", err)
	}
	return report, nil
}

// newCorrelatorWithFeedback builds the correlator used by every read path, with
// the correlation feedback store attached so confirm/reject decisions shape
// histories, the commit index and stats (feedback loop, C4).
func newCorrelatorWithFeedback(workDir, beadsPath string) (*correlation.Correlator, error) {
	feedbackStore, err := loadCorrelationFeedbackStoreForBeadsPath(workDir, beadsPath)
	if err != nil {
		return nil, err
	}
	return correlation.NewCorrelator(workDir, beadsPath).WithFeedbackStore(feedbackStore), nil
}

func loadCorrelationFeedbackStore(workDir string) (*correlation.FeedbackStore, error) {
	return loadCorrelationFeedbackStoreForBeadsPath(workDir, "")
}

func loadCorrelationFeedbackStoreForBeadsPath(workDir, beadsPath string) (*correlation.FeedbackStore, error) {
	var beadsDir string
	if strings.TrimSpace(beadsPath) != "" {
		candidate := filepath.Dir(beadsPath)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			beadsDir = candidate
		}
	}
	if beadsDir == "" {
		var err error
		beadsDir, err = loader.GetBeadsDir(workDir)
		if err != nil {
			return nil, fmt.Errorf("getting beads directory: %w", err)
		}
	}
	feedbackStore := correlation.NewFeedbackStore(beadsDir)
	if err := feedbackStore.Load(); err != nil {
		return nil, fmt.Errorf("loading feedback: %w", err)
	}
	return feedbackStore, nil
}

func parseCorrelationArg(arg string) (string, string, error) {
	parts := strings.SplitN(strings.TrimSpace(arg), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected format: SHA:beadID, got: %q", arg)
	}
	commitSHA := strings.TrimSpace(parts[0])
	beadID := strings.TrimSpace(parts[1])
	if commitSHA == "" || beadID == "" {
		return "", "", fmt.Errorf("expected non-empty SHA and bead ID in format SHA:beadID, got: %q", arg)
	}
	return commitSHA, beadID, nil
}

func resolveCorrelatedCommit(commits []correlation.CorrelatedCommit, sha string) (*correlation.CorrelatedCommit, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if sha == "" {
		return nil, fmt.Errorf("commit SHA is required")
	}

	type commitMatch struct {
		index int
		sha   string
	}
	matches := make([]commitMatch, 0, 1)
	seen := make(map[string]bool)
	for i := range commits {
		commitSHA := strings.ToLower(commits[i].SHA)
		if commitSHA == sha {
			return &commits[i], nil
		}
		shortSHA := strings.ToLower(commits[i].ShortSHA)
		if (shortSHA == sha || strings.HasPrefix(commitSHA, sha)) && !seen[commitSHA] {
			matches = append(matches, commitMatch{index: i, sha: commits[i].SHA})
			seen[commitSHA] = true
		}
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &commits[matches[0].index], nil
	default:
		matchedSHAs := make([]string, len(matches))
		for i, match := range matches {
			matchedSHAs[i] = match.sha
		}
		sort.Strings(matchedSHAs)
		return nil, fmt.Errorf("ambiguous commit SHA prefix %q matches %d commits: %s", sha, len(matchedSHAs), strings.Join(matchedSHAs, ", "))
	}
}

func handleRobotCorrelationStats(ctx RobotContext) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	stats, err := loadWorkspaceCorrelationFeedbackStats(ctx, workDir)
	if err != nil {
		return err
	}

	env := ctx.Envelope()
	output := struct {
		correlation.FeedbackStats
		GeneratedAt     string                `json:"generated_at"`
		OutputFormat    string                `json:"output_format,omitempty"`
		Version         string                `json:"version,omitempty"`
		SourcePath      string                `json:"source_path,omitempty"`
		SourceKind      string                `json:"source_kind,omitempty"`
		AsOf            string                `json:"as_of,omitempty"`
		AsOfCommit      string                `json:"as_of_commit,omitempty"`
		Scope           *RobotScope           `json:"scope,omitempty"`
		LoadStats       *RobotLoadStats       `json:"load_stats,omitempty"`
		SourceAuthority *RobotSourceAuthority `json:"source_authority,omitempty"`
		AuthorityHash   string                `json:"authority_hash,omitempty"`
		ScopeHash       string                `json:"scope_hash,omitempty"`
	}{
		FeedbackStats:   stats,
		GeneratedAt:     env.GeneratedAt,
		OutputFormat:    env.OutputFormat,
		Version:         env.Version,
		SourcePath:      env.SourcePath,
		SourceKind:      env.SourceKind,
		AsOf:            env.AsOf,
		AsOfCommit:      env.AsOfCommit,
		Scope:           env.Scope,
		LoadStats:       env.LoadStats,
		SourceAuthority: env.SourceAuthority,
		AuthorityHash:   env.AuthorityHash,
		ScopeHash:       env.ScopeHash,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding stats: %w", err)
	}
	return nil
}

func handleRobotExplainCorrelation(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotExplainCorrFlag == nil {
		return fmt.Errorf("robot explain correlation flag not configured")
	}
	commitSHA, beadID, err := parseCorrelationArg(*cfg.RobotExplainCorrFlag)
	if err != nil {
		return err
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	feedbackStore, fbBeadID, err := resolveWorkspaceFeedbackStoreForBead(ctx, workDir, beadID)
	if err != nil {
		return err
	}

	// Explain the raw strategy score: a rejected pair is removed from the
	// feedback-applied report, but the user asking "why was this correlated?"
	// still needs the signals plus the stored decision.
	report, err := generateRawCorrelationReport(ctx, workDir, scopedCorrelationIssues(ctx), correlation.CorrelatorOptions{BeadID: beadID, Revision: ctx.AsOfCommit})
	if err != nil {
		return fmt.Errorf("generating report: %w", err)
	}

	history, ok := report.Histories[beadID]
	if !ok {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", beadID)
		return newReportedRobotHandlerExit(1)
	}

	targetCommit, err := resolveCorrelatedCommit(history.Commits, commitSHA)
	if err != nil {
		return err
	}
	if targetCommit == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
		return newReportedRobotHandlerExit(1)
	}

	explanation := correlation.NewScorer().BuildExplanation(*targetCommit, beadID)
	if fb, ok := feedbackStore.Get(targetCommit.SHA, fbBeadID); ok {
		fbCopy := fb
		fbCopy.BeadID = beadID
		explanation.Feedback = &fbCopy
		explanation.Recommendation = describeCorrelationFeedback(fbCopy)
	}
	enveloped, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), explanation)
	if err != nil {
		return fmt.Errorf("explain correlation payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(enveloped); err != nil {
		return fmt.Errorf("encoding explanation: %w", err)
	}
	return nil
}

func handleRobotCorrelationFeedback(ctx RobotContext, cfg phaseThreeRobotHandlerConfig, feedbackType correlation.FeedbackType) error {
	if ctx.AsOf != "" {
		fmt.Fprintln(ctx.StderrOrDefault(), "Error: correlation feedback commands cannot be combined with --as-of (historical snapshots are read-only)")
		return newReportedRobotHandlerExit(1)
	}
	var flagPtr *string
	status := "confirmed"
	switch feedbackType {
	case correlation.FeedbackConfirm:
		flagPtr = cfg.RobotConfirmCorrFlag
		status = "confirmed"
	case correlation.FeedbackReject:
		flagPtr = cfg.RobotRejectCorrFlag
		status = "rejected"
	case correlation.FeedbackIgnore:
		flagPtr = cfg.RobotIgnoreCorrFlag
		status = "ignored"
	default:
		return fmt.Errorf("unsupported correlation feedback type %q", feedbackType)
	}
	if flagPtr == nil {
		return fmt.Errorf("robot correlation feedback flag not configured")
	}

	commitSHA, beadID, err := parseCorrelationArg(*flagPtr)
	if err != nil {
		return err
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	feedbackStore, fbBeadID, err := resolveWorkspaceFeedbackStoreForBead(ctx, workDir, beadID)
	if err != nil {
		return err
	}

	report, err := generateRawCorrelationReport(ctx, workDir, scopedCorrelationIssues(ctx), correlation.CorrelatorOptions{BeadID: beadID, Revision: ctx.AsOfCommit})
	if err != nil {
		return fmt.Errorf("generating report: %w", err)
	}

	history, ok := report.Histories[beadID]
	if !ok {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", beadID)
		return newReportedRobotHandlerExit(1)
	}
	targetCommit, err := resolveCorrelatedCommit(history.Commits, commitSHA)
	if err != nil {
		return err
	}
	if targetCommit == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
		return newReportedRobotHandlerExit(1)
	}
	originalConf := targetCommit.Confidence
	commitSHA = targetCommit.SHA

	feedbackBy := "cli"
	if cfg.CorrelationFeedbackBy != nil && strings.TrimSpace(*cfg.CorrelationFeedbackBy) != "" {
		feedbackBy = strings.TrimSpace(*cfg.CorrelationFeedbackBy)
	}
	reason := ""
	if cfg.CorrelationReason != nil {
		reason = *cfg.CorrelationReason
	}

	switch feedbackType {
	case correlation.FeedbackReject:
		if err := feedbackStore.Reject(commitSHA, fbBeadID, feedbackBy, originalConf, reason); err != nil {
			return fmt.Errorf("saving feedback: %w", err)
		}
	case correlation.FeedbackIgnore:
		if err := feedbackStore.Ignore(commitSHA, fbBeadID, feedbackBy, originalConf, reason); err != nil {
			return fmt.Errorf("saving feedback: %w", err)
		}
	default:
		if err := feedbackStore.Confirm(commitSHA, fbBeadID, feedbackBy, originalConf, reason); err != nil {
			return fmt.Errorf("saving feedback: %w", err)
		}
	}

	result := map[string]interface{}{
		"status":    status,
		"commit":    commitSHA,
		"bead":      beadID,
		"by":        feedbackBy,
		"reason":    reason,
		"orig_conf": originalConf,
	}
	enveloped, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), result)
	if err != nil {
		return fmt.Errorf("correlation feedback payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(enveloped); err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}
	return nil
}

func handleRobotFileRelations(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	threshold := 0.0
	if cfg.RelationsThreshold != nil {
		threshold = *cfg.RelationsThreshold
	}
	maxResults := 10
	if cfg.RelationsLimit != nil {
		maxResults = *cfg.RelationsLimit
	}
	fileLookup := correlation.NewFileLookup(report)
	result := fileLookup.GetRelatedFiles(*cfg.RobotFileRelationsFlag, threshold, maxResults)
	if result.TotalCommits == 0 && ctx.SourceKind == "workspace" {
		if matches := resolveWorkspaceReportFilePaths(report, *cfg.RobotFileRelationsFlag); len(matches) == 1 {
			result = fileLookup.GetRelatedFiles(matches[0], threshold, maxResults)
		}
	}

	output := struct {
		RobotEnvelope
		FilePath     string                      `json:"file_path"`
		TotalCommits int                         `json:"total_commits"`
		Threshold    float64                     `json:"threshold"`
		RelatedFiles []correlation.CoChangeEntry `json:"related_files"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(report.DataHash),
		FilePath:      result.FilePath,
		TotalCommits:  result.TotalCommits,
		Threshold:     result.Threshold,
		RelatedFiles:  result.RelatedFiles,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding file relations: %w", err)
	}
	return nil
}

func inferSourceRepoForTombstone(tid string, authority *RobotSourceAuthority) string {
	if authority == nil {
		return ""
	}
	bestLen := -1
	bestSourceRepo := ""
	tidLower := strings.ToLower(strings.TrimSpace(tid))
	for _, src := range authority.Sources {
		if src.RepoPrefix != "" {
			prefLower := strings.ToLower(src.RepoPrefix)
			if len(tidLower) > len(prefLower) && strings.HasPrefix(tidLower, prefLower) && len(prefLower) > bestLen {
				bestLen = len(prefLower)
				bestSourceRepo = src.SourceRepo
			}
		} else if bestLen < 0 && src.SourceRepo != "" {
			bestLen = 0
			bestSourceRepo = src.SourceRepo
		}
	}
	return bestSourceRepo
}

func isCommitAncestorOfRevision(repoRoot, sha, rev string) bool {
	if repoRoot == "" || sha == "" || strings.HasPrefix(sha, "-") || strings.ContainsRune(sha, 0) {
		return false
	}
	targetRev := strings.TrimSpace(rev)
	if targetRev == "" {
		targetRev = "HEAD"
	}
	if strings.HasPrefix(targetRev, "-") || strings.ContainsRune(targetRev, 0) {
		return false
	}
	cmd := exec.Command("git", "--no-replace-objects", "merge-base", "--is-ancestor", "--end-of-options", sha, targetRev)
	cmd.Dir = repoRoot
	cmd.Env = sanitizedGitEnvForCLI()
	return cmd.Run() == nil
}

func handleRobotCommit(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotCommitFlag == nil {
		return fmt.Errorf("robot commit flag not configured")
	}
	rawRef := strings.TrimSpace(*cfg.RobotCommitFlag)
	if rawRef == "" || strings.HasPrefix(rawRef, "-") {
		return fmt.Errorf("--robot-commit requires a commit SHA or git revision")
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	var resolvedSHA string
	var matchedGitRoot string
	var commitStoreDirs []string
	var matchedWorkspaceSources []RobotSourceReport
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		sources, _ := resolveWorkspaceCorrelationSources(ctx)
		if len(sources) == 0 {
			return fmt.Errorf("no git repositories available in workspace")
		}
		seenRoots := make(map[string]bool)
		rootBySHA := make(map[string]string)
		for _, ws := range sources {
			if ctx.Repo != "" && !sourceMatchesRepoFilter(ws.source, ctx.Repo) {
				continue
			}
			cleanRoot := filepath.Clean(ws.repoRoot)
			if seenRoots[cleanRoot] {
				continue
			}
			seenRoots[cleanRoot] = true
			sha, revErr := loader.NewGitLoader(cleanRoot).ResolveRevision(rawRef)
			if revErr == nil && strings.TrimSpace(sha) != "" {
				sha = strings.ToLower(strings.TrimSpace(sha))
				if _, exists := rootBySHA[sha]; !exists {
					rootBySHA[sha] = cleanRoot
				}
			}
		}
		switch len(rootBySHA) {
		case 0:
			fmt.Fprintf(ctx.StderrOrDefault(), "Commit not found: %s\n", rawRef)
			return newReportedRobotHandlerExit(1)
		case 1:
			for sha, root := range rootBySHA {
				resolvedSHA = sha
				matchedGitRoot = root
			}
		default:
			fmt.Fprintf(ctx.StderrOrDefault(), "Ambiguous commit revision %q matches %d workspace repositories; pass --repo <prefix> or an unambiguous commit SHA\n", rawRef, len(rootBySHA))
			return newReportedRobotHandlerExit(1)
		}
		for _, ws := range sources {
			if filepath.Clean(ws.repoRoot) != filepath.Clean(matchedGitRoot) {
				continue
			}
			matchedWorkspaceSources = append(matchedWorkspaceSources, ws.source)
			if storeDir := resolveSourceBeadsDir(ws.source); storeDir != "" {
				if rel, relErr := filepath.Rel(filepath.Clean(matchedGitRoot), filepath.Clean(storeDir)); relErr == nil && rel != "." && rel != "" && !strings.HasPrefix(rel, "..") {
					commitStoreDirs = append(commitStoreDirs, filepath.ToSlash(rel))
				}
			}
		}
	} else {
		matchedGitRoot = workDir
		if found, rootErr := correlation.FindGitRoot(workDir); rootErr == nil {
			matchedGitRoot = found
		} else if resolvedBeadsPath, bpErr := resolveCorrelationBeadsPathForContext(ctx, workDir); bpErr == nil && resolvedBeadsPath != "" {
			beadsParent := filepath.Clean(filepath.Dir(resolvedBeadsPath))
			if !filepath.IsAbs(beadsParent) {
				beadsParent = filepath.Clean(filepath.Join(workDir, beadsParent))
			}
			if found, rootErr2 := correlation.FindGitRoot(beadsParent); rootErr2 == nil {
				matchedGitRoot = found
			}
		}
		sha, revErr := loader.NewGitLoader(matchedGitRoot).ResolveRevision(rawRef)
		if revErr != nil || strings.TrimSpace(sha) == "" {
			fmt.Fprintf(ctx.StderrOrDefault(), "Commit not found: %s\n", rawRef)
			return newReportedRobotHandlerExit(1)
		}
		resolvedSHA = strings.ToLower(strings.TrimSpace(sha))
		if resolvedBeadsPath, bpErr := resolveCorrelationBeadsPathForContext(ctx, workDir); bpErr == nil && resolvedBeadsPath != "" {
			storeDir := filepath.Clean(filepath.Dir(resolvedBeadsPath))
			if !filepath.IsAbs(storeDir) {
				storeDir = filepath.Clean(filepath.Join(workDir, storeDir))
			}
			if rel, relErr := filepath.Rel(filepath.Clean(matchedGitRoot), storeDir); relErr == nil && rel != "." && rel != "" && !strings.HasPrefix(rel, "..") {
				commitStoreDirs = append(commitStoreDirs, filepath.ToSlash(rel))
			}
		}
	}

	opts := correlation.CorrelatorOptions{Limit: 500, Revision: ctx.AsOfCommit}
	if cfg.HistoryLimit != nil {
		opts.Limit = *cfg.HistoryLimit
	}
	if cfg.HistorySince != nil && strings.TrimSpace(*cfg.HistorySince) != "" {
		since, err := recipe.ParseRelativeTime(*cfg.HistorySince, ctx.ReferenceNow())
		if err != nil {
			return fmt.Errorf("parsing --history-since: %w", err)
		}
		if !since.IsZero() {
			opts.Since = &since
		}
	}

	// Correlate over the full unscoped bead set (including tombstones and sibling
	// shared-.git workspace repos) first so true orphan status is never corrupted
	// by display/scope filters (--label, --recipe, --repo, --min-confidence).
	commitCtx := ctx
	if len(matchedWorkspaceSources) > 0 && commitCtx.SourceAuthority != nil {
		scopedAuth := *commitCtx.SourceAuthority
		scopedAuth.Sources = matchedWorkspaceSources
		commitCtx.SourceAuthority = &scopedAuth
	}
	baseIssues := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		baseIssues = ctx.FullIssues
	}
	if len(ctx.TombstoneIDs) > 0 {
		seenIDs := make(map[string]bool, len(baseIssues))
		for _, iss := range baseIssues {
			seenIDs[iss.ID] = true
		}
		merged := make([]model.Issue, len(baseIssues), len(baseIssues)+len(ctx.TombstoneIDs))
		copy(merged, baseIssues)
		for _, tid := range ctx.TombstoneIDs {
			if !seenIDs[tid] {
				merged = append(merged, model.Issue{
					ID:         tid,
					Title:      "(tombstoned)",
					Status:     model.StatusTombstone,
					SourceRepo: inferSourceRepoForTombstone(tid, ctx.SourceAuthority),
				})
			}
		}
		baseIssues = merged
	}
	commitCtx.Issues = baseIssues
	commitCtx.FullIssues = baseIssues
	commitCtx.Repo = ""
	commitCtx.LabelScope = ""
	commitCtx.Recipe = ""
	commitCtx.CandidateIDs = nil

	report, err := generateCorrelationReportForContext(commitCtx, matchedGitRoot, opts)
	if err != nil {
		return err
	}

	lookup := correlation.NewReverseLookupWithRepo(report, matchedGitRoot).WithStoreDirs(commitStoreDirs...)
	result, err := lookup.LookupByCommit(resolvedSHA)
	if err != nil {
		return err
	}

	inAsOfAncestry := ctx.AsOfCommit == "" || isCommitAncestorOfRevision(matchedGitRoot, resolvedSHA, ctx.AsOfCommit)

	// If resolvedSHA fell outside the bounded --history-limit window (or lives on
	// a side branch not reachable from the primary walk revision), evaluate
	// single-commit correlation directly at resolvedSHA while retaining full
	// .beads lifecycle event windows so temporal_author and older commits work.
	if result.IsOrphan && len(result.RelatedBeads) == 0 && !result.BeadsOnly && inAsOfAncestry &&
		(opts.Since == nil || result.Timestamp.IsZero() || !result.Timestamp.Before(*opts.Since)) {
		windowTruncated := opts.Limit > 0 && (report.Window == nil || report.Window.Commits >= opts.Limit)
		runFallback := false
		fallbackRev := ctx.AsOfCommit
		if ctx.AsOfCommit != "" {
			if windowTruncated {
				runFallback = true
			}
		} else {
			if !isCommitAncestorOfRevision(matchedGitRoot, resolvedSHA, "") {
				runFallback = true
				fallbackRev = resolvedSHA
			} else if windowTruncated {
				runFallback = true
				fallbackRev = ""
			}
		}
		if runFallback {
			singleReport, singleErr := generateCorrelationReportForContext(commitCtx, matchedGitRoot, correlation.CorrelatorOptions{
				Limit:           0,
				Revision:        fallbackRev,
				Since:           opts.Since,
				Until:           opts.Until,
				TargetCommitSHA: resolvedSHA,
			})
			if singleErr == nil && singleReport != nil {
				singleLookup := correlation.NewReverseLookupWithRepo(singleReport, matchedGitRoot).WithStoreDirs(commitStoreDirs...)
				if singleRes, lkErr := singleLookup.LookupByCommit(resolvedSHA); lkErr == nil {
					if len(singleRes.RelatedBeads) > 0 || singleRes.BeadsOnly || !singleRes.IsOrphan {
						result = singleRes
					}
				}
			}
		}
	}

	effectiveMinConf := 0.0
	if cfg.MinConfidence != nil && *cfg.MinConfidence > effectiveMinConf {
		effectiveMinConf = *cfg.MinConfidence
	}
	if cfg.RobotMinConf != nil && *cfg.RobotMinConf > effectiveMinConf {
		effectiveMinConf = *cfg.RobotMinConf
	}

	unfilteredCount := len(result.RelatedBeads)
	trueIsOrphan := unfilteredCount == 0 && result.IsOrphan && !result.BeadsOnly && inAsOfAncestry

	scopeIDs := ctx.CandidateIDs
	if scopeIDs == nil && ctx.ActiveRecipe != nil {
		scopeIDs = make(map[string]bool, len(ctx.Issues))
		for _, iss := range ctx.Issues {
			scopeIDs[iss.ID] = true
		}
	}

	filtered := make([]correlation.RelatedBead, 0, unfilteredCount)
	for _, rb := range result.RelatedBeads {
		if scopeIDs != nil && !scopeIDs[rb.BeadID] {
			continue
		}
		if effectiveMinConf > 0 && rb.Confidence < effectiveMinConf {
			continue
		}
		filtered = append(filtered, rb)
	}
	result.FilteredOutCount = unfilteredCount - len(filtered)
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Confidence != filtered[j].Confidence {
			return filtered[i].Confidence > filtered[j].Confidence
		}
		return filtered[i].BeadID < filtered[j].BeadID
	})
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(filtered) > *cfg.RobotMaxResults {
		filtered = filtered[:*cfg.RobotMaxResults]
	}
	result.RelatedBeads = filtered
	result.IsOrphan = trueIsOrphan

	if result.IsOrphan {
		orphanReport := report
		if scopeIDs != nil && report != nil {
			scopedCopy := *report
			scopedHistories := make(map[string]correlation.BeadHistory, len(report.Histories))
			for id, h := range report.Histories {
				if scopeIDs[id] {
					scopedHistories[id] = h
				}
			}
			scopedCopy.Histories = scopedHistories
			orphanReport = &scopedCopy
		}
		detector := correlation.NewOrphanDetectorAt(orphanReport, matchedGitRoot, ctx.ReferenceNow()).
			WithStoreDirs(commitStoreDirs...)
		cand, beadsOnly, candErr := detector.AnalyzeCommit(resolvedSHA)
		if beadsOnly {
			result.BeadsOnly = true
			result.IsOrphan = false
		} else if candErr == nil && cand != nil {
			if scopeIDs != nil {
				scopedProb := make([]correlation.ProbableBead, 0, len(cand.ProbableBeads))
				for _, pb := range cand.ProbableBeads {
					if scopeIDs[pb.BeadID] {
						scopedProb = append(scopedProb, pb)
					}
				}
				cand.ProbableBeads = scopedProb
				if len(cand.ProbableBeads) == 0 {
					cand = nil
				}
			}
			if cand != nil {
				if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(cand.ProbableBeads) > *cfg.RobotMaxResults {
					cand.ProbableBeads = cand.ProbableBeads[:*cfg.RobotMaxResults]
				}
				result.OrphanCandidate = cand
				result.OrphanCandidates = cand.ProbableBeads
			}
		}
	}

	envHash := ctx.DataHash
	if envHash == "" {
		envHash = report.DataHash
	}
	enveloped, err := withEnvelope(ctx.EnvelopeWithHash(envHash), result)
	if err != nil {
		return fmt.Errorf("commit lookup payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(enveloped); err != nil {
		return fmt.Errorf("encoding commit lookup: %w", err)
	}
	return nil
}

func handleRobotOrphans(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	var orphanReport *correlation.OrphanReport
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		orphanReport, err = detectWorkspaceOrphans(ctx, limit)
		if err != nil {
			return err
		}
	} else {
		orphanCtx := ctx
		orphanCtx.Issues = labelAnalysisIssuesForContext(ctx)
		orphanCtx.CandidateIDs = nil
		report, err := generateCorrelationReportForContext(orphanCtx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
		if err != nil {
			return err
		}
		if ctx.DataHash != "" {
			report.DataHash = ctx.DataHash
		}
		orphanGitRoot := workDir
		if found, rootErr := correlation.FindGitRoot(workDir); rootErr == nil {
			orphanGitRoot = found
		}
		var orphanStoreDirs []string
		if resolvedBeadsPath, bpErr := resolveCorrelationBeadsPathForContext(ctx, workDir); bpErr == nil && resolvedBeadsPath != "" {
			storeDir := filepath.Clean(filepath.Dir(resolvedBeadsPath))
			if !filepath.IsAbs(storeDir) {
				storeDir = filepath.Clean(filepath.Join(workDir, storeDir))
			}
			if rel, relErr := filepath.Rel(filepath.Clean(orphanGitRoot), storeDir); relErr == nil && rel != "." && rel != "" && !strings.HasPrefix(rel, "..") {
				orphanStoreDirs = append(orphanStoreDirs, filepath.ToSlash(rel))
			}
		}
		orphanReport, err = correlation.NewOrphanDetectorAt(report, orphanGitRoot, ctx.ReferenceNow()).
			WithStoreDirs(orphanStoreDirs...).
			DetectOrphans(correlation.ExtractOptions{Limit: limit, Revision: ctx.AsOfCommit})
		if err != nil {
			return fmt.Errorf("detecting orphans: %w", err)
		}
	}

	minScore := 30
	if cfg.OrphansMinScore != nil {
		minScore = *cfg.OrphansMinScore
	}
	maxResults := 0
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		maxResults = *cfg.RobotMaxResults
	}
	filterOrphanReportWithScope(orphanReport, minScore, maxResults, ctx.CandidateIDs)

	// Copy fields rather than embedding: OrphanReport defines generated_at and
	// data_hash too, and encoding/json drops colliding embedded keys silently.
	output := struct {
		RobotEnvelope
		GitRange   string                        `json:"git_range"`
		Window     correlation.OrphanWindow      `json:"window"`
		Stats      correlation.OrphanReportStats `json:"stats"`
		Candidates []correlation.OrphanCandidate `json:"candidates"`
		ByBead     map[string][]string           `json:"by_bead,omitempty"`
		UsageHints []string                      `json:"usage_hints"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(orphanReport.DataHash),
		GitRange:      orphanReport.GitRange,
		Window:        orphanReport.Window,
		Stats:         orphanReport.Stats,
		Candidates:    orphanReport.Candidates,
		ByBead:        orphanReport.ByBead,
		UsageHints:    orphanReport.UsageHints,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding orphans: %w", err)
	}
	return nil
}

func filterOrphanReportByMinScore(orphanReport *correlation.OrphanReport, minScore int) {
	filterOrphanReport(orphanReport, minScore, 0)
}

func filterOrphanReport(orphanReport *correlation.OrphanReport, minScore, maxResults int) {
	filterOrphanReportWithScope(orphanReport, minScore, maxResults, nil)
}

func filterOrphanReportWithScope(orphanReport *correlation.OrphanReport, minScore, maxResults int, candidateIDs map[string]bool) {
	filtered := make([]correlation.OrphanCandidate, 0, len(orphanReport.Candidates))
	byBead := make(map[string][]string)
	totalSuspicion := 0

	for _, candidate := range orphanReport.Candidates {
		if candidateIDs != nil && len(candidate.ProbableBeads) > 0 {
			scopedProb := make([]correlation.ProbableBead, 0, len(candidate.ProbableBeads))
			for _, pb := range candidate.ProbableBeads {
				if candidateIDs[pb.BeadID] {
					scopedProb = append(scopedProb, pb)
				}
			}
			candidate.ProbableBeads = scopedProb
		}
		if candidate.SuspicionScore < minScore {
			continue
		}
		if maxResults > 0 && len(filtered) >= maxResults {
			break
		}
		filtered = append(filtered, candidate)
		totalSuspicion += candidate.SuspicionScore
		for _, bead := range candidate.ProbableBeads {
			byBead[bead.BeadID] = append(byBead[bead.BeadID], candidate.ShortSHA)
		}
	}

	orphanReport.Candidates = filtered
	orphanReport.ByBead = byBead
	orphanReport.Stats.CandidateCount = len(filtered)
	orphanReport.Stats.AvgSuspicion = 0
	if len(filtered) > 0 {
		orphanReport.Stats.AvgSuspicion = float64(totalSuspicion) / float64(len(filtered))
	}
}

func handleRobotFileBeads(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotFileBeadsFlag == nil {
		return fmt.Errorf("robot file beads flag not configured")
	}

	query := *cfg.RobotFileBeadsFlag
	isGlob := strings.ContainsAny(query, "*?[")
	if isGlob {
		normalizedGlob := strings.TrimPrefix(strings.ReplaceAll(query, "\\", "/"), "./")
		if _, err := path.Match(normalizedGlob, ""); err != nil {
			return fmt.Errorf("invalid file glob %q: %w", query, err)
		}
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	fileLookup := correlation.NewFileLookup(report)
	var result *correlation.FileBeadLookupResult
	if isGlob {
		result = fileLookup.LookupByFileGlob(query)
		if result.TotalBeads == 0 && ctx.SourceKind == "workspace" {
			cleanGlob := strings.TrimPrefix(strings.ReplaceAll(query, "\\", "/"), "./")
			if !strings.HasPrefix(cleanGlob, "*/") {
				if fallback := fileLookup.LookupByFileGlob("*/" + cleanGlob); fallback.TotalBeads > 0 {
					result = fallback
				}
			}
		}
	} else {
		result = fileLookup.LookupByFile(query)
		if result.TotalBeads == 0 && ctx.SourceKind == "workspace" {
			clean := strings.TrimPrefix(strings.ReplaceAll(query, "\\", "/"), "./")
			if fallback := fileLookup.LookupByFileGlob("*/" + clean); fallback.TotalBeads > 0 {
				result = fallback
			}
		}
	}
	closedLimit := 20
	if cfg.FileBeadsLimit != nil {
		closedLimit = *cfg.FileBeadsLimit
	}
	if closedLimit < 0 {
		closedLimit = 0
	}
	if len(result.ClosedBeads) > closedLimit {
		result.ClosedBeads = result.ClosedBeads[:closedLimit]
	}

	output := struct {
		RobotEnvelope
		FilePath    string                      `json:"file_path"`
		TotalBeads  int                         `json:"total_beads"`
		OpenBeads   []correlation.BeadReference `json:"open_beads"`
		ClosedBeads []correlation.BeadReference `json:"closed_beads"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(report.DataHash),
		FilePath:      *cfg.RobotFileBeadsFlag,
		TotalBeads:    result.TotalBeads,
		OpenBeads:     result.OpenBeads,
		ClosedBeads:   result.ClosedBeads,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding file beads: %w", err)
	}
	return nil
}

func handleRobotImpact(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotImpactFlag == nil {
		return fmt.Errorf("robot impact flag not configured")
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	fileLookup := correlation.NewFileLookup(report)
	rawFiles := strings.Split(*cfg.RobotImpactFlag, ",")
	files := make([]string, 0, len(rawFiles))
	for _, rf := range rawFiles {
		trimmed := strings.TrimSpace(rf)
		if trimmed == "" {
			continue
		}
		if ctx.SourceKind == "workspace" {
			if matches := resolveWorkspaceReportFilePaths(report, trimmed); len(matches) > 0 {
				files = append(files, matches...)
				continue
			}
		}
		files = append(files, trimmed)
	}
	impactResult := fileLookup.ImpactAnalysisAt(files, ctx.ReferenceNow())

	output := struct {
		RobotEnvelope
		Files         []string                   `json:"files"`
		RiskLevel     string                     `json:"risk_level"`
		RiskScore     float64                    `json:"risk_score"`
		Summary       string                     `json:"summary"`
		Warnings      []string                   `json:"warnings"`
		AffectedBeads []correlation.AffectedBead `json:"affected_beads"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(report.DataHash),
		Files:         impactResult.Files,
		RiskLevel:     impactResult.RiskLevel,
		RiskScore:     impactResult.RiskScore,
		Summary:       impactResult.Summary,
		Warnings:      impactResult.Warnings,
		AffectedBeads: impactResult.AffectedBeads,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding impact analysis: %w", err)
	}
	return nil
}

func handleRobotRelated(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Use the dispatch context's candidate-scoped issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping without leaking 1-hop label neighbors.
	issues := scopedCorrelationIssues(ctx)
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	candSet := ctx.CandidateIDs
	depGraph := make(map[string][]string)
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if candSet != nil && !candSet[dep.DependsOnID] {
				continue
			}
			depGraph[issue.ID] = append(depGraph[issue.ID], dep.DependsOnID)
		}
	}

	options := correlation.RelatedWorkOptions{
		ConcurrencyWindow: 7 * 24 * time.Hour,
		DependencyGraph:   depGraph,
	}
	if cfg.RelatedMinRelevance != nil {
		options.MinRelevance = *cfg.RelatedMinRelevance
	}
	if cfg.RelatedMaxResults != nil {
		options.MaxResults = *cfg.RelatedMaxResults
	}
	if cfg.RelatedIncludeClosed != nil {
		options.IncludeClosed = *cfg.RelatedIncludeClosed
	}

	result := report.FindRelatedWorkAt(*cfg.RobotRelatedFlag, options, ctx.ReferenceNow())
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found in history: %s\n", *cfg.RobotRelatedFlag)
		return newReportedRobotHandlerExit(1)
	}

	output, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), result)
	if err != nil {
		return fmt.Errorf("related work payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding related work: %w", err)
	}
	return nil
}

func handleRobotBlockerChain(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	// Verify the requested target belongs to the visible scoped issue set, then
	// traverse upstream blockers across FullIssues (when retained) so multi-hop
	// cross-label/repo/recipe chains reach their true root blockers instead of
	// stopping at the 1-hop context boundary.
	issues := ctx.Issues
	targetID := *cfg.RobotBlockerChainFlag
	inScope := false
	for _, issue := range issues {
		if issue.ID == targetID {
			inScope = true
			break
		}
	}
	if !inScope {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}

	graphIssues := issues
	if len(ctx.FullIssues) > 0 {
		graphIssues = ctx.FullIssues
	}
	analyzer := analysis.NewAnalyzer(graphIssues)
	analyzer.SetReadinessScope(ctx.Readiness, ctx.CandidateIDs)
	analyzer.SetNow(ctx.ReferenceNow())
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullCfg := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullCfg)
		_ = analyzer.Analyze()
	}
	result := analyzer.GetBlockerChain(targetID)
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		RobotEnvelope
		Result *analysis.BlockerChainResult `json:"result"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Result:        result,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding blocker chain: %w", err)
	}
	return nil
}

func handleRobotWhatIf(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	analyzer := ctx.GraphAnalyzer()
	analyzer.SetNow(ctx.ReferenceNow())
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullCfg := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullCfg)
	}
	stats := analyzer.Analyze()

	result := analyzer.ComputeWhatIf(*cfg.RobotWhatIfFlag, &stats)
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", *cfg.RobotWhatIfFlag)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		RobotEnvelope
		Result *analysis.WhatIfResult `json:"result"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Result:        result,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding what-if impact: %w", err)
	}
	return nil
}

func handleRobotImpactNetwork(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Use the dispatch context's candidate-scoped issue set so 1-hop label neighbors
	// never leak into network nodes.
	issues := scopedCorrelationIssues(ctx)
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	network := correlation.NewNetworkBuilderWithIssues(report, issues).BuildAt(ctx.ReferenceNow())
	beadID := ""
	if *cfg.RobotImpactNetworkFlag != "all" {
		beadID = *cfg.RobotImpactNetworkFlag
	}
	if beadID != "" {
		if _, ok := network.Nodes[beadID]; !ok {
			fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found in network: %s\n", beadID)
			return newReportedRobotHandlerExit(1)
		}
	}
	depth := 1
	if cfg.NetworkDepth != nil {
		depth = *cfg.NetworkDepth
	}
	if depth < 1 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}

	output, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), network.ToResult(beadID, depth))
	if err != nil {
		return fmt.Errorf("impact network payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding impact network: %w", err)
	}
	return nil
}

func handleRobotCausality(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Use the dispatch context's candidate-scoped issue set so 1-hop label neighbors
	// never leak into network nodes.
	issues := scopedCorrelationIssues(ctx)
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	opts := correlation.CorrelatorOptions{Limit: limit, CausalityBeadID: *cfg.RobotCausalityFlag, Revision: ctx.AsOfCommit}
	if cfg.HistorySince != nil && strings.TrimSpace(*cfg.HistorySince) != "" {
		since, err := recipe.ParseRelativeTime(*cfg.HistorySince, ctx.ReferenceNow())
		if err != nil {
			return fmt.Errorf("parsing --history-since: %w", err)
		}
		if !since.IsZero() {
			opts.Since = &since
		}
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, opts)
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}

	blockerTitleSource := ctx.FullIssues
	if len(blockerTitleSource) == 0 {
		blockerTitleSource = issues
	}
	blockerTitles := make(map[string]string, len(blockerTitleSource)+len(issues))
	for _, issue := range blockerTitleSource {
		blockerTitles[issue.ID] = issue.Title
	}
	for _, issue := range issues {
		blockerTitles[issue.ID] = issue.Title
	}
	result := report.BuildCausalityChainAt(*cfg.RobotCausalityFlag, correlation.CausalityOptions{
		IncludeCommits: true,
		BlockerTitles:  blockerTitles,
	}, ctx.ReferenceNow())
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", *cfg.RobotCausalityFlag)
		return newReportedRobotHandlerExit(1)
	}

	output, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), result)
	if err != nil {
		return fmt.Errorf("causality payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding causality result: %w", err)
	}
	return nil
}

func handleRobotSprintShow(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	sprints, err := loadSprintsForContext(ctx, workDir)
	if err != nil {
		return fmt.Errorf("loading sprints: %w", err)
	}

	found := matchSprintByID(sprints, *cfg.RobotSprintShowFlag)
	if found == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", *cfg.RobotSprintShowFlag)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		RobotEnvelope
		Sprint *model.Sprint `json:"sprint"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Sprint:        found,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding sprint: %w", err)
	}
	return nil
}

func handleRobotCapacity(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	analyzer := ctx.GraphAnalyzer()
	analyzer.SetNow(now)
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullCfg := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullCfg)
	}
	graphStats := analyzer.Analyze()

	targetIssues := make([]model.Issue, 0, len(graphIssues))
	for _, issue := range graphIssues {
		if !analyzer.IsCandidate(issue.ID) {
			continue
		}
		if cfg.CapacityLabel != nil && strings.TrimSpace(*cfg.CapacityLabel) != "" {
			wantLabel := strings.TrimSpace(*cfg.CapacityLabel)
			matches := false
			for _, label := range issue.Labels {
				if strings.EqualFold(strings.TrimSpace(label), wantLabel) {
					matches = true
					break
				}
			}
			if !matches {
				continue
			}
		}
		targetIssues = append(targetIssues, issue)
	}
	sort.Slice(targetIssues, func(i, j int) bool { return targetIssues[i].ID < targetIssues[j].ID })

	openIssues := make([]model.Issue, 0)
	issueMap := make(map[string]model.Issue, len(targetIssues))
	for _, issue := range targetIssues {
		if !issue.Status.IsClosed() && !issue.Status.IsTombstone() {
			issueMap[issue.ID] = issue
			openIssues = append(openIssues, issue)
		}
	}

	agents := 1
	if cfg.CapacityAgents != nil && *cfg.CapacityAgents > 0 {
		agents = *cfg.CapacityAgents
	}

	etaIssues := ctx.VelocityIssues()
	totalMinutes := 0
	for _, issue := range openIssues {
		eta, err := analysis.EstimateETAForIssue(etaIssues, &graphStats, issue.ID, 1, now)
		if err == nil {
			totalMinutes += eta.EstimatedMinutes
		}
	}

	// Readiness retains full-source gates; the duration heuristic below uses
	// only distinct blocking edges within the selected unresolved backlog.
	readiness := analyzer.Readiness()
	blocks := make(map[string][]string)
	for _, issue := range openIssues {
		seen := make(map[string]bool)
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() || seen[dep.DependsOnID] {
				continue
			}
			if _, exists := issueMap[dep.DependsOnID]; exists {
				seen[dep.DependsOnID] = true
				blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], issue.ID)
			}
		}
	}

	actionable := make([]string, 0)
	for _, issue := range openIssues {
		if readiness.Ready(issue.ID, now) {
			actionable = append(actionable, issue.ID)
		}
	}

	var chainStarts []string
	if len(openIssues) > 0 {
		// Seed from every in-set root (open issues with no open blocker inside
		// openIssues). This includes all actionable roots plus externally-gated
		// sub-DAG roots even when unrelated actionable leaf tasks exist.
		hasInSetBlocker := make(map[string]bool, len(openIssues))
		for _, downstreamIDs := range blocks {
			for _, downstreamID := range downstreamIDs {
				hasInSetBlocker[downstreamID] = true
			}
		}
		for _, issue := range openIssues {
			if !hasInSetBlocker[issue.ID] {
				chainStarts = append(chainStarts, issue.ID)
			}
		}
		if len(chainStarts) == 0 {
			for _, issue := range openIssues {
				chainStarts = append(chainStarts, issue.ID)
			}
		}
	}

	longestChain := longestCapacityChain(chainStarts, blocks)
	if longestChain == nil {
		longestChain = []string{}
	}

	serialMinutes := 0
	for _, id := range longestChain {
		eta, err := analysis.EstimateETAForIssue(etaIssues, &graphStats, id, 1, now)
		if err == nil {
			serialMinutes += eta.EstimatedMinutes
		}
	}

	parallelMinutes := totalMinutes - serialMinutes
	parallelizablePct := 0.0
	if totalMinutes > 0 {
		parallelizablePct = float64(parallelMinutes) / float64(totalMinutes) * 100
	}
	graham := analysis.ComputeGrahamBounds(openIssues, ctx.VelocityIssues(), &graphStats, agents, now)
	velPerDay := graham.VelocityMinutesPerDay
	if velPerDay <= 0 {
		velPerDay = 60.0 * 8.0
	}
	effectiveMinutes := serialMinutes + parallelMinutes/agents
	estimatedDays := float64(effectiveMinutes) / velPerDay
	if len(openIssues) > 0 {
		if estimatedDays < graham.GrahamMinDays {
			estimatedDays = graham.GrahamMinDays
		}
		if estimatedDays > graham.GrahamMaxDays {
			estimatedDays = graham.GrahamMaxDays
		}
	}

	type bottleneck struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		BlocksCount int      `json:"blocks_count"`
		Blocks      []string `json:"blocks,omitempty"`
	}
	bottlenecks := make([]bottleneck, 0)
	for _, issue := range openIssues {
		if len(blocks[issue.ID]) > 1 {
			bottlenecks = append(bottlenecks, bottleneck{
				ID:          issue.ID,
				Title:       issue.Title,
				BlocksCount: len(blocks[issue.ID]),
				Blocks:      blocks[issue.ID],
			})
		}
	}
	sort.Slice(bottlenecks, func(i, j int) bool {
		if bottlenecks[i].BlocksCount == bottlenecks[j].BlocksCount {
			return bottlenecks[i].ID < bottlenecks[j].ID
		}
		return bottlenecks[i].BlocksCount > bottlenecks[j].BlocksCount
	})
	if len(bottlenecks) > 5 {
		bottlenecks = bottlenecks[:5]
	}

	output := struct {
		RobotEnvelope
		Agents              int          `json:"agents"`
		Label               string       `json:"label,omitempty"`
		OpenIssueCount      int          `json:"open_issue_count"`
		TotalMinutes        int          `json:"total_minutes"`
		TotalDays           float64      `json:"total_days"`
		SerialMinutes       int          `json:"serial_minutes"`
		ParallelMinutes     int          `json:"parallel_minutes"`
		ParallelizablePct   float64      `json:"parallelizable_pct"`
		EstimatedDays       float64      `json:"estimated_days"`
		CriticalPathMinutes int          `json:"critical_path_minutes"`
		VelocityMinutesDay  float64      `json:"velocity_minutes_per_day"`
		GrahamMinDays       float64      `json:"graham_min_days"`
		GrahamMaxDays       float64      `json:"graham_max_days"`
		CriticalPathLen     int          `json:"critical_path_length"`
		CriticalPath        []string     `json:"critical_path"`
		ActionableCount     int          `json:"actionable_count"`
		Actionable          []string     `json:"actionable"`
		Bottlenecks         []bottleneck `json:"bottlenecks,omitempty"`
	}{
		RobotEnvelope:       ctx.Envelope(),
		Agents:              agents,
		OpenIssueCount:      len(openIssues),
		TotalMinutes:        totalMinutes,
		TotalDays:           float64(totalMinutes) / (60.0 * 8.0),
		SerialMinutes:       serialMinutes,
		ParallelMinutes:     parallelMinutes,
		ParallelizablePct:   parallelizablePct,
		EstimatedDays:       estimatedDays,
		CriticalPathMinutes: graham.CriticalPathMinutes,
		VelocityMinutesDay:  graham.VelocityMinutesPerDay,
		GrahamMinDays:       graham.GrahamMinDays,
		GrahamMaxDays:       graham.GrahamMaxDays,
		CriticalPathLen:     len(longestChain),
		CriticalPath:        longestChain,
		ActionableCount:     len(actionable),
		Actionable:          actionable,
		Bottlenecks:         bottlenecks,
	}
	if cfg.CapacityLabel != nil && strings.TrimSpace(*cfg.CapacityLabel) != "" {
		output.Label = strings.TrimSpace(*cfg.CapacityLabel)
	}

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding capacity: %w", err)
	}
	return nil
}

// longestCapacityChain preserves the first longest path in seed/neighbor order.
// Acyclic reachable graphs share suffix lengths instead of enumerating paths.
// Reachable cycles retain the exhaustive simple-path behavior of this heuristic.
func longestCapacityChain(starts []string, blocks map[string][]string) []string {
	if len(starts) == 0 {
		return nil
	}
	state := make(map[string]uint8)
	var postorder []string
	hasCycle := false
	var visit func(string)
	visit = func(id string) {
		if state[id] == 1 {
			hasCycle = true
			return
		}
		if state[id] == 2 {
			return
		}
		state[id] = 1
		for _, nextID := range blocks[id] {
			visit(nextID)
		}
		state[id] = 2
		postorder = append(postorder, id)
	}
	for _, id := range starts {
		visit(id)
	}
	if !hasCycle {
		length := make(map[string]int, len(postorder))
		next := make(map[string]string, len(postorder))
		for _, id := range postorder {
			length[id] = 1
			for _, nextID := range blocks[id] {
				if candidate := 1 + length[nextID]; candidate > length[id] {
					length[id] = candidate
					next[id] = nextID
				}
			}
		}
		start := starts[0]
		for _, id := range starts[1:] {
			if length[id] > length[start] {
				start = id
			}
		}
		path := make([]string, 0, length[start])
		for id, remaining := start, length[start]; remaining > 0; id, remaining = next[id], remaining-1 {
			path = append(path, id)
		}
		return path
	}

	var longest []string
	visited := make(map[string]bool)
	var dfs func(string, []string)
	dfs = func(id string, path []string) {
		if visited[id] {
			return
		}
		visited[id] = true
		path = append(path, id)
		if len(path) > len(longest) {
			longest = append([]string(nil), path...)
		}
		for _, nextID := range blocks[id] {
			dfs(nextID, path)
		}
		visited[id] = false
	}
	for _, id := range starts {
		dfs(id, nil)
	}
	return longest
}

func (r *RobotRegistry) Validate() error {
	registered := make(map[string]struct{}, len(r.commands))
	active := make(map[string]struct{}, len(r.commands))

	for _, cmd := range r.commands {
		registered[cmd.FlagName] = struct{}{}
		if robotFlagActive(cmd.FlagPtr) {
			active[cmd.FlagName] = struct{}{}
		}
	}

	for _, cmd := range r.commands {
		for _, coFlag := range cmd.RequiredCoFlags {
			if _, ok := registered[coFlag]; !ok {
				return fmt.Errorf("%s requires unregistered co-flag %s", formatRobotFlag(cmd.FlagName), formatRobotFlag(coFlag))
			}
		}
		if _, ok := active[cmd.FlagName]; !ok || len(cmd.RequiredCoFlags) == 0 {
			continue
		}
		if hasAnyRobotFlag(active, cmd.RequiredCoFlags) {
			continue
		}

		if len(cmd.RequiredCoFlags) == 1 {
			return fmt.Errorf("%s requires %s", formatRobotFlag(cmd.FlagName), formatRobotFlag(cmd.RequiredCoFlags[0]))
		}
		return fmt.Errorf("%s requires one of %s", formatRobotFlag(cmd.FlagName), joinRobotFlags(cmd.RequiredCoFlags))
	}

	return nil
}

func normalizeRobotFlagName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "--")
}

func normalizeRobotFlagNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := normalizeRobotFlagName(name)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func formatRobotFlag(name string) string {
	normalized := normalizeRobotFlagName(name)
	if normalized == "" {
		return "--"
	}
	return "--" + normalized
}

func joinRobotFlags(names []string) string {
	flags := make([]string, 0, len(names))
	for _, name := range names {
		flags = append(flags, formatRobotFlag(name))
	}
	return strings.Join(flags, ", ")
}

func hasAnyRobotFlag(active map[string]struct{}, names []string) bool {
	for _, name := range names {
		if _, ok := active[normalizeRobotFlagName(name)]; ok {
			return true
		}
	}
	return false
}

func robotFlagActive(flagPtr interface{}) bool {
	if flagPtr == nil {
		return false
	}

	value := reflect.ValueOf(flagPtr)
	if !value.IsValid() {
		return false
	}

	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		return robotValueActive(value.Elem())
	}

	return robotValueActive(value)
}

func robotValueActive(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}

	switch value.Kind() {
	case reflect.Bool:
		return value.Bool()
	case reflect.String:
		return strings.TrimSpace(value.String()) != ""
	case reflect.Slice, reflect.Array, reflect.Map:
		return value.Len() > 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return value.Float() != 0
	case reflect.Interface:
		if value.IsNil() {
			return false
		}
		return robotFlagActive(value.Interface())
	default:
		zero := reflect.Zero(value.Type()).Interface()
		return !reflect.DeepEqual(value.Interface(), zero)
	}
}

// describeCorrelationFeedback renders a stored confirm/reject/ignore decision
// as the explanation's recommendation line, e.g.
// "rejected by feedback (cli): touched the file by accident".
func describeCorrelationFeedback(fb correlation.CorrelationFeedback) string {
	var verb string
	switch fb.Type {
	case correlation.FeedbackConfirm:
		verb = "confirmed"
	case correlation.FeedbackReject:
		verb = "rejected"
	case correlation.FeedbackIgnore:
		verb = "ignored"
	default:
		verb = string(fb.Type)
	}
	s := verb + " by feedback"
	if by := strings.TrimSpace(fb.FeedbackBy); by != "" {
		s += " (" + by + ")"
	}
	if reason := strings.TrimSpace(fb.Reason); reason != "" {
		s += ": " + reason
	}
	return s
}

// handleRobotFileHotspots answers "which files do the most beads touch?" from
// the same shared correlation report as --robot-file-beads and --robot-impact,
// so the three surfaces never disagree (#184).
func handleRobotFileHotspots(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotFileHotspotsFlag == nil {
		return fmt.Errorf("robot file hotspots flag not configured")
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReportForContext(ctx, workDir, correlation.CorrelatorOptions{Limit: limit, Revision: ctx.AsOfCommit})
	if err != nil {
		return err
	}

	hotspotsLimit := 10
	if cfg.HotspotsLimit != nil {
		hotspotsLimit = *cfg.HotspotsLimit
	}
	fileLookup := correlation.NewFileLookup(report)
	output := struct {
		RobotEnvelope
		Hotspots []correlation.FileHotspot  `json:"hotspots"`
		Stats    correlation.FileIndexStats `json:"stats"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(report.DataHash),
		Hotspots:      fileLookup.GetHotspots(hotspotsLimit),
		Stats:         fileLookup.GetStats(),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding hotspots: %w", err)
	}
	return nil
}

func filterCyclesForCandidates(cycles [][]string, candidateIDs map[string]bool) [][]string {
	if candidateIDs == nil || len(cycles) == 0 {
		return cycles
	}
	filtered := make([][]string, 0, len(cycles))
	for _, cycle := range cycles {
		for _, id := range cycle {
			if candidateIDs[id] {
				filtered = append(filtered, cycle)
				break
			}
		}
	}
	return filtered
}

func resolveWorkspaceReportFilePaths(report *correlation.HistoryReport, rawQuery string) []string {
	if report == nil {
		return nil
	}
	clean := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(rawQuery), "\\", "/"), "./")
	if clean == "" {
		return nil
	}
	suffix := "/" + clean
	seen := make(map[string]bool)
	var matches []string
	for _, hist := range report.Histories {
		for _, cc := range hist.Commits {
			for _, fc := range cc.Files {
				norm := strings.TrimPrefix(strings.ReplaceAll(fc.Path, "\\", "/"), "./")
				if norm == clean {
					// Exact path exists in the report; caller should query directly.
					return nil
				}
				if strings.HasSuffix(norm, suffix) && strings.Count(strings.TrimSuffix(norm, suffix), "/") == 0 {
					if !seen[fc.Path] {
						seen[fc.Path] = true
						matches = append(matches, fc.Path)
					}
				}
			}
		}
	}
	sort.Strings(matches)
	return matches
}

type loadedSprintOrigin struct {
	sprint          model.Sprint
	rawID           string
	repoName        string
	sprintsFilePath string
	qualifyID       func(string) string
}

func sourceMatchesRepoFilter(src RobotSourceReport, repoFilter string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(repoFilter))
	if trimmed == "" {
		return true
	}
	trimmedBase := strings.TrimRight(trimmed, "-:_")
	for _, rawCandidate := range []string{src.Name, src.SourceRepo, src.RepoPrefix} {
		cand := strings.ToLower(strings.TrimSpace(rawCandidate))
		if cand == "" {
			continue
		}
		candBase := strings.TrimRight(cand, "-:_")
		if cand == trimmed || candBase == trimmedBase {
			return true
		}
		if strings.HasPrefix(cand, trimmed) || (trimmedBase != "" && strings.HasPrefix(candBase, trimmedBase)) {
			return true
		}
	}
	return false
}

func loadSprintsForContext(ctx RobotContext, workDir string) ([]model.Sprint, error) {
	origins, err := loadSprintsWithOriginsForContext(ctx, workDir)
	if err != nil {
		return nil, err
	}
	sprints := make([]model.Sprint, len(origins))
	for i, o := range origins {
		sprints[i] = o.sprint
	}
	return sprints, nil
}

func loadSprintsWithOriginsForContext(ctx RobotContext, workDir string) ([]loadedSprintOrigin, error) {
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		loaded, err := loader.LoadSprints(workDir)
		if err != nil {
			return nil, err
		}
		sprintsFilePath := ""
		if beadsDir, dirErr := loader.GetBeadsDir(workDir); dirErr == nil {
			sprintsFilePath = filepath.Join(beadsDir, loader.SprintsFileName)
		}
		origins := make([]loadedSprintOrigin, len(loaded))
		for i, sp := range loaded {
			origins[i] = loadedSprintOrigin{
				sprint:          sp,
				rawID:           sp.ID,
				sprintsFilePath: sprintsFilePath,
			}
		}
		return origins, nil
	}

	allIssues := ctx.Issues
	if len(ctx.FullIssues) > 0 {
		allIssues = ctx.FullIssues
	}
	knownQualified := make(map[string]bool, len(allIssues))
	issuesBySourceRepo := make(map[string][]model.Issue)
	for _, iss := range allIssues {
		knownQualified[iss.ID] = true
		key := strings.ToLower(strings.TrimSpace(iss.SourceRepo))
		issuesBySourceRepo[key] = append(issuesBySourceRepo[key], iss)
	}

	var entries []loadedSprintOrigin
	seenBeadsDirs := make(map[string]bool)

	addBeadsDir := func(beadsDir string, src *RobotSourceReport) error {
		if strings.TrimSpace(beadsDir) == "" {
			return nil
		}
		cleanBeadsDir := filepath.Clean(beadsDir)
		if absDir, absErr := filepath.Abs(cleanBeadsDir); absErr == nil {
			cleanBeadsDir = filepath.Clean(absDir)
		}
		if seenBeadsDirs[cleanBeadsDir] {
			return nil
		}
		seenBeadsDirs[cleanBeadsDir] = true
		sprintsFilePath := filepath.Join(cleanBeadsDir, loader.SprintsFileName)
		loaded, err := loader.LoadSprintsFromFile(sprintsFilePath)
		if err != nil {
			return err
		}
		var qualifyFn func(string) string
		repoLabel := ""
		if src != nil {
			srcCopy := *src
			repoLabel = strings.TrimSpace(srcCopy.Name)
			if repoLabel == "" {
				repoLabel = strings.TrimSpace(srcCopy.SourceRepo)
			}
			if repoLabel == "" && srcCopy.RepoPrefix != "" {
				repoLabel = strings.TrimRight(srcCopy.RepoPrefix, "-:_")
			}
			repoIssues := issuesBySourceRepo[strings.ToLower(strings.TrimSpace(srcCopy.SourceRepo))]
			localToQualified := make(map[string]string, len(repoIssues))
			for _, iss := range repoIssues {
				localID := localBeadIDForWorkspaceIssue(iss, srcCopy)
				localToQualified[localID] = iss.ID
			}
			qualifyFn = func(rawID string) string {
				return qualifyWorkspaceHistoricalID(rawID, srcCopy, localToQualified, knownQualified)
			}
		}
		for _, sp := range loaded {
			rawID := sp.ID
			if qualifyFn != nil && len(sp.BeadIDs) > 0 {
				qualifiedBeads := make([]string, len(sp.BeadIDs))
				for i, bid := range sp.BeadIDs {
					qualifiedBeads[i] = qualifyFn(bid)
				}
				sp.BeadIDs = qualifiedBeads
			}
			entries = append(entries, loadedSprintOrigin{
				sprint:          sp,
				rawID:           rawID,
				repoName:        repoLabel,
				sprintsFilePath: sprintsFilePath,
				qualifyID:       qualifyFn,
			})
		}
		return nil
	}

	// Iterate configured/discovered workspace sources FIRST so root-repo (`path: "."`)
	// sprints get proper ID qualification, `<repo>:<id>` disambiguation, and `--repo` scoping.
	for i := range ctx.SourceAuthority.Sources {
		src := &ctx.SourceAuthority.Sources[i]
		if src.Status != "loaded" {
			continue
		}
		beadsDir := resolveSourceBeadsDir(*src)
		if beadsDir == "" {
			continue
		}
		// Always mark every configured source's beads directory as owned by a repo
		// source so the umbrella fallback below never re-loads an excluded repo when `--repo` is set.
		if ctx.Repo != "" && !sourceMatchesRepoFilter(*src, ctx.Repo) {
			cleanSkipped := filepath.Clean(beadsDir)
			if absDir, absErr := filepath.Abs(cleanSkipped); absErr == nil {
				cleanSkipped = filepath.Clean(absDir)
			}
			seenBeadsDirs[cleanSkipped] = true
			continue
		}
		if err := addBeadsDir(beadsDir, src); err != nil {
			return nil, err
		}
	}

	// Only load workspace-umbrella sprints from `workDir` when `workDir` has its own
	// `.beads` directory that was NOT already loaded as a workspace repo source and `--repo` is unset.
	if workDir != "" && strings.TrimSpace(ctx.Repo) == "" {
		if rootBeadsDir, dirErr := loader.GetBeadsDir(workDir); dirErr == nil {
			if err := addBeadsDir(rootBeadsDir, nil); err != nil {
				return nil, err
			}
		}
	}

	idCounts := make(map[string]int, len(entries))
	for _, e := range entries {
		idCounts[e.sprint.ID]++
	}
	for i := range entries {
		if idCounts[entries[i].sprint.ID] > 1 && entries[i].repoName != "" {
			entries[i].sprint.ID = entries[i].repoName + ":" + entries[i].sprint.ID
			if entries[i].sprint.Name != "" && !strings.HasPrefix(entries[i].sprint.Name, "["+entries[i].repoName+"]") {
				entries[i].sprint.Name = "[" + entries[i].repoName + "] " + entries[i].sprint.Name
			}
		}
	}
	return entries, nil
}

func computeSprintScopeChangesForContext(
	ctx RobotContext,
	workDir string,
	targetSprint *model.Sprint,
	issueMap map[string]model.Issue,
	now time.Time,
) ([]ScopeChangeEvent, error) {
	if targetSprint == nil || targetSprint.ID == "" {
		return nil, nil
	}
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		return computeSprintScopeChanges(workDir, targetSprint, issueMap, now)
	}
	origins, err := loadSprintsWithOriginsForContext(ctx, workDir)
	if err != nil {
		return nil, err
	}
	for _, origin := range origins {
		if origin.sprint.ID != targetSprint.ID {
			continue
		}
		if origin.sprintsFilePath == "" {
			return nil, nil
		}
		gitRoot, err := findEnclosingGitRoot(filepath.Dir(origin.sprintsFilePath))
		if err != nil {
			return nil, nil
		}
		relPath, err := filepath.Rel(gitRoot, origin.sprintsFilePath)
		if err != nil {
			return nil, nil
		}
		return computeSprintScopeChangesAtPath(
			gitRoot,
			relPath,
			origin.rawID,
			targetSprint,
			issueMap,
			now,
			origin.qualifyID,
		)
	}
	return computeSprintScopeChanges(workDir, targetSprint, issueMap, now)
}

func matchSprintByID(sprints []model.Sprint, target string) *model.Sprint {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	for i := range sprints {
		if sprints[i].ID == target {
			return &sprints[i]
		}
	}
	var suffixMatch *model.Sprint
	for i := range sprints {
		if idx := strings.IndexByte(sprints[i].ID, ':'); idx >= 0 && sprints[i].ID[idx+1:] == target {
			if suffixMatch != nil {
				// Ambiguous across multiple workspace repositories; require repo:id.
				return nil
			}
			suffixMatch = &sprints[i]
		}
	}
	return suffixMatch
}
