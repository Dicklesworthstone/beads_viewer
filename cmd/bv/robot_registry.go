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
	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
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
	GraphRoot            string
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
	flagSet  *pflag.FlagSet
}

func (r *RobotRegistry) SetFlagSet(fs *pflag.FlagSet) {
	if r != nil {
		r.flagSet = fs
	}
}

func (r *RobotRegistry) isCommandActive(cmd RobotCommand) bool {
	if robotFlagActive(cmd.FlagPtr) {
		return true
	}
	if !cmd.IsModifier && r != nil && r.flagSet != nil && isStringFlagPtr(cmd.FlagPtr) && r.flagSet.Changed(cmd.FlagName) {
		return true
	}
	return false
}

func baselineScopeForContext(ctx RobotContext) *baseline.BaselineScope {
	scope := baseline.BaselineScope{
		Label:     strings.TrimSpace(ctx.LabelScope),
		Recipe:    strings.TrimSpace(ctx.Recipe),
		Repo:      strings.TrimSpace(ctx.Repo),
		GraphRoot: strings.TrimSpace(ctx.GraphRoot),
		Workspace: ctx.SourceKind == "workspace",
	}
	if scope == (baseline.BaselineScope{}) {
		return nil
	}
	return &scope
}

func baselineMatchesContext(bl *baseline.Baseline, ctx RobotContext) bool {
	if bl == nil {
		return false
	}
	var target baseline.BaselineScope
	if s := baselineScopeForContext(ctx); s != nil {
		target = *s
	}
	return bl.MatchesScope(target)
}

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
	RobotMaxResults       *int
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
	NotReadyLabels      *string
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
	RobotCheckDepFlag       *string
	RobotCassFlag           *string
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

// isCorrelationTargetInScope reports whether an explicit correlation target beadID
// is allowed under the active --repo / --label / --recipe scope. When only --repo
// is active (without --label or --recipe), deleted/tombstoned historical beads
// absent from ctx.FullIssues remain inspectable when their ID matches ctx.Repo.
func isCorrelationTargetInScope(ctx RobotContext, beadID string) bool {
	if ctx.CandidateIDs == nil || ctx.CandidateIDs[beadID] {
		return true
	}
	if ctx.LabelScope != "" || ctx.ActiveRecipe != nil || strings.TrimSpace(ctx.GraphRoot) != "" || strings.TrimSpace(ctx.Repo) == "" {
		return false
	}
	for _, iss := range ctx.FullIssues {
		if iss.ID == beadID {
			return false
		}
	}
	if ctx.SourceKind == "workspace" && ctx.SourceAuthority != nil {
		for _, src := range ctx.SourceAuthority.Sources {
			if !sourceMatchesRepoFilter(src, ctx.Repo) {
				continue
			}
			prefix := src.RepoPrefix
			if prefix != "" && len(beadID) > len(prefix) && strings.EqualFold(beadID[:len(prefix)], prefix) {
				return true
			}
		}
		return false
	}
	return len(filterByRepo([]model.Issue{{ID: beadID}}, ctx.Repo)) > 0
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

// GraphRootSourceIssues returns the topology slice used to validate and extract
// --graph-root subgraphs in triage/next: when --recipe is active, PreRecipeIssues
// retains open container epics even when all children are currently blocked
// (len(CandidateIDs) == 0), while isGraphRootInActiveScope enforces domain/status rules.
func (ctx RobotContext) GraphRootSourceIssues() []model.Issue {
	if ctx.ActiveRecipe != nil && len(ctx.PreRecipeIssues) > 0 {
		return ctx.PreRecipeIssues
	}
	return ctx.GraphIssues()
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
	env.ScopeHash = robotScopeHash(ctx.LabelScope, ctx.Recipe, ctx.Repo, ctx.GraphRoot, dataHash, ids)
	env.AsOf = ctx.AsOf
	env.AsOfCommit = ctx.AsOfCommit
	unsupported := unsupportedScopeFor(ctx.Command, ctx)
	if ctx.LabelScope != "" || ctx.Recipe != "" || ctx.Repo != "" || ctx.GraphRoot != "" || len(unsupported) > 0 {
		env.Scope = &RobotScope{
			Label:       ctx.LabelScope,
			Recipe:      ctx.Recipe,
			Repo:        ctx.Repo,
			GraphRoot:   ctx.GraphRoot,
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
	case "robot-sprint-list":
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
		if strings.TrimSpace(ctx.GraphRoot) != "" {
			out = append(out, "graph_root")
		}
		return out
	case "robot-sprint-show":
		if ctx.AsOf != "" {
			return []string{"as_of"}
		}
		return nil
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
	if r == nil {
		return false
	}
	for _, cmd := range r.commands {
		if r.isCommandActive(cmd) {
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
		if r.isCommandActive(cmd) {
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
		RobotMaxResults:       &dummyInt,
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
		RobotCheckDepFlag:       &dummyStr,
		RobotCassFlag:           &dummyStr,
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
	if r == nil {
		return nil
	}
	active := make([]RobotCommand, 0, len(r.commands))
	for _, cmd := range r.commands {
		if r.isCommandActive(cmd) {
			active = append(active, cmd)
		}
	}
	return active
}

func (r *RobotRegistry) DispatchFlag(flagName string, ctx RobotContext) (bool, error) {
	if r == nil {
		return false, nil
	}
	normalized := normalizeRobotFlagName(flagName)
	if normalized == "" {
		return false, nil
	}

	for _, cmd := range r.commands {
		if cmd.FlagName != normalized || !r.isCommandActive(cmd) {
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
as_of/as_of_commit (with --as-of), scope (with --label/--recipe/--repo/--graph-root,
plus scope.unsupported for flags a command cannot honour), load_stats (when records
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
			if cmd.FlagName == "version" {
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
			if (cfg.StructuredOutput != nil && cfg.StructuredOutput()) || ctx.EnvRobot {
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
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(summaries) > *cfg.RobotMaxResults {
				summaries = summaries[:*cfg.RobotMaxResults]
			}

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

func annotatePlanItemsWithActions(plan *analysis.ExecutionPlan, graphIssues []model.Issue, claimsProven bool) {
	if plan == nil {
		return
	}
	issueByID := make(map[string]model.Issue, len(graphIssues))
	for _, iss := range graphIssues {
		issueByID[iss.ID] = iss
	}
	for ti := range plan.Tracks {
		for ii := range plan.Tracks[ti].Items {
			item := &plan.Tracks[ti].Items[ii]
			iss, ok := issueByID[item.ID]
			if !ok {
				continue
			}
			if !claimsProven {
				item.Claimable = false
				act := iss.Actions(false)
				act.Claim = nil
				act.UnavailableReason = "source authority is incomplete or stale"
				item.Actions = &act
			} else {
				act := iss.Actions(item.Claimable)
				if item.NotReady && (act.UnavailableReason == "" || act.UnavailableReason == "snapshot does not establish claim readiness") {
					act.UnavailableReason = "marked not-ready by label"
				}
				item.Actions = &act
			}
		}
	}
}

func capExecutionPlanTracks(plan *analysis.ExecutionPlan, maxResults int) {
	if plan == nil || maxResults <= 0 {
		return
	}
	remaining := maxResults
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

func buildPlanCapacity(openIssues, velocityIssues []model.Issue, stats *analysis.GraphStats, visibleIDs map[string]bool, now time.Time) *analysis.PlanCapacity {
	activeOpen := make([]model.Issue, 0, len(openIssues))
	for _, iss := range openIssues {
		if iss.Status.IsClosed() || iss.Status.IsTombstone() || iss.IsTemplate || iss.Pinned || iss.Status == model.StatusPinned {
			continue
		}
		activeOpen = append(activeOpen, iss)
	}
	if len(activeOpen) == 0 {
		return nil
	}
	g1 := analysis.ComputeGrahamBounds(activeOpen, velocityIssues, stats, 1, now)
	if g1.TotalMinutes <= 0 {
		return nil
	}
	g2 := analysis.ComputeGrahamBounds(activeOpen, velocityIssues, stats, 2, now)
	g4 := analysis.ComputeGrahamBounds(activeOpen, velocityIssues, stats, 4, now)
	parPct := float64(max(0, g1.TotalMinutes-g1.CriticalPathMinutes)) / float64(g1.TotalMinutes) * 100.0
	var critIDs []string
	for _, id := range g1.CriticalPathIDs {
		if visibleIDs == nil || visibleIDs[id] {
			critIDs = append(critIDs, id)
		}
	}
	return &analysis.PlanCapacity{
		OpenCount:           len(activeOpen),
		TotalMinutes:        g1.TotalMinutes,
		CriticalPathMinutes: g1.CriticalPathMinutes,
		ParallelizablePct:   math.Round(parPct*10) / 10,
		Days1Agent:          math.Round(g1.GrahamMinDays*100) / 100,
		Days2AgentsMin:      math.Round(g2.GrahamMinDays*100) / 100,
		Days2AgentsMax:      math.Round(g2.GrahamMaxDays*100) / 100,
		Days4AgentsMin:      math.Round(g4.GrahamMinDays*100) / 100,
		Days4AgentsMax:      math.Round(g4.GrahamMaxDays*100) / 100,
		CriticalPathIDs:     critIDs,
	}
}

func enrichExecutionPlan(plan *analysis.ExecutionPlan, ctx RobotContext, analyzer *analysis.Analyzer, stats *analysis.GraphStats, graphIssues []model.Issue) {
	if plan == nil || analyzer == nil {
		return
	}
	now := ctx.ReferenceNow().UTC()
	issueByID := make(map[string]model.Issue, len(graphIssues))
	openScoped := make([]model.Issue, 0, len(graphIssues))
	for _, iss := range graphIssues {
		issueByID[iss.ID] = iss
		if analyzer.IsCandidate(iss.ID) && !iss.Status.IsClosed() && !iss.Status.IsTombstone() && !iss.IsTemplate && !iss.Pinned && iss.Status != model.StatusPinned {
			openScoped = append(openScoped, iss)
		}
	}

	velIssues := ctx.VelocityIssues()
	plan.Capacity = buildPlanCapacity(openScoped, velIssues, stats, ctx.CandidateIDs, now)

	etaCtx := analysis.NewETAContext(velIssues, stats, now)
	riskWeights := analysis.DefaultRiskWeights()
	readiness := analyzer.Readiness()

	for ti := range plan.Tracks {
		for ii := range plan.Tracks[ti].Items {
			item := &plan.Tracks[ti].Items[ii]
			iss, ok := issueByID[item.ID]
			if !ok {
				continue
			}
			est := etaCtx.EstimateForIssue(iss, 1)
			if est.EstimatedMinutes > 0 && !est.ETADate.IsZero() {
				days := math.Round(est.EstimatedDays*100) / 100
				item.ETADays = &days
				item.ETADate = est.ETADate.UTC().Format(time.RFC3339)
				conf := math.Round(est.Confidence*100) / 100
				item.ETAConfidence = &conf
			}
			rs := analysis.ComputeRiskSignalsWithReadiness(&iss, stats, issueByID, readiness, now, riskWeights)
			if rs.CompositeRisk > 0 {
				item.RiskScore = math.Round(rs.CompositeRisk*1000) / 1000
				item.RiskExplanation = rs.Explanation
			}
			if delta, err := analyzer.ComputeWhatIfDeltaFromStats(item.ID, stats); err == nil && delta != nil {
				if delta.TransitiveUnblocks > 0 {
					item.TransitiveUnblocks = delta.TransitiveUnblocks
				}
				if delta.EstimatedDaysSaved > 0 {
					item.EstimatedDaysSaved = math.Round(delta.EstimatedDaysSaved*100) / 100
				}
			}
		}
	}
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
				config.ComputeCycles = false
				config.CyclesSkipReason = skipReason
			}

			notReadyLabels := resolveNotReadyLabels(phaseThreeRobotHandlerConfig{NotReadyLabels: cfg.NotReadyLabels})
			plan := analyzer.GetExecutionPlanWithOptions(notReadyLabels)
			annotatePlanItemsWithActions(&plan, graphIssues, ctx.claimsProven())
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
				capExecutionPlanTracks(&plan, *cfg.RobotMaxResults)
			}
			stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
			stats.WaitForPhase2()
			enrichExecutionPlan(&plan, ctx, analyzer, stats, graphIssues)

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
				AnalysisConfig: stats.EffectiveConfig(),
				Status:         stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:     ctx.LabelScope,
				LabelContext:   ctx.LabelContext,
				Plan:           plan,
				UsageHints: []string{
					"jq '.plan.tracks | length' - Number of parallel execution tracks",
					"jq '.plan.tracks[0].items | map(.id)' - First track item IDs",
					"jq '.plan.tracks[].items[] | select(.claimable) | {id, title, eta_days, risk_score, claim: .actions.claim}' - Claimable track items with ETA and risk",
					"jq '.plan.tracks[].items[] | select(.unblocks | length > 0) | {id, unblocks, transitive_unblocks, estimated_days_saved}' - High-leverage unblocking items",
					"jq '.plan.capacity' - Graham multi-agent parallel capacity bounds (1a/2a/4a)",
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
			feedbackData, feedbackWeights := loadRobotFeedbackForContext(ctx)
			if feedbackWeights != nil {
				analyzer.SetWeights(*feedbackWeights)
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
					wantLabel := strings.TrimSpace(*cfg.RobotByLabel)
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
				if cfg.RobotByAssignee != nil && strings.TrimSpace(*cfg.RobotByAssignee) != "" {
					issue, ok := issueMap[rec.IssueID]
					if !ok || issue.Assignee != *cfg.RobotByAssignee {
						continue
					}
				}
				iss, ok := issueMap[rec.IssueID]
				attachPriorityRecommendationAction(&rec.PriorityRecommendation, iss, ok, ctx)
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

			var feedbackInfo *analysis.FeedbackJSON
			if feedbackData != nil && len(feedbackData.Events) > 0 {
				info := feedbackData.ToJSON()
				feedbackInfo = &info
			}

			output := struct {
				RobotEnvelope
				AnalysisConfig    analysis.AnalysisConfig                   `json:"analysis_config"`
				Status            analysis.MetricStatus                     `json:"status"`
				LabelScope        string                                    `json:"label_scope,omitempty"`
				LabelContext      *analysis.LabelHealth                     `json:"label_context,omitempty"`
				Feedback          *analysis.FeedbackJSON                    `json:"feedback,omitempty"`
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
				AnalysisConfig:    stats.EffectiveConfig(),
				Status:            stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:        ctx.LabelScope,
				LabelContext:      ctx.LabelContext,
				Feedback:          feedbackInfo,
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
			var rootIssueID string
			if cfg.GraphRoot != nil {
				rootIssueID = strings.TrimSpace(*cfg.GraphRoot)
			}
			var graphDepth int
			if cfg.GraphDepth != nil {
				graphDepth = *cfg.GraphDepth
			}

			exportIssues := ctx.Issues
			if rootIssueID != "" {
				if ctx.ActiveRecipe != nil && ctx.ActiveRecipe.View.MaxItems > 0 {
					preIssues := ctx.GraphRootSourceIssues()
					if ctx.LabelScope != "" {
						labelFiltered := make([]model.Issue, 0, len(preIssues))
						for _, iss := range preIssues {
							for _, lbl := range iss.Labels {
								if lbl == ctx.LabelScope {
									labelFiltered = append(labelFiltered, iss)
									break
								}
							}
						}
						preIssues = labelFiltered
					}
					if containsIssueID(preIssues, rootIssueID) {
						// P32: Apply uncapped recipe BEFORE subgraph BFS so recipe-excluded nodes
						// cannot act as bridges, then cap via connectivity-preserving frontier expansion.
						subMetrics := recipeMetricsForContext(ctx, ctx.FullIssues, ctx.ActiveRecipe)
						subMetrics.Readiness = ctx.Readiness
						uncappedRecipe := *ctx.ActiveRecipe
						uncappedRecipe.View.MaxItems = 0
						if uncappedApplied, rErr := recipe.Apply(preIssues, subMetrics, &uncappedRecipe, ctx.ReferenceNow()); rErr == nil && containsIssueID(uncappedApplied, rootIssueID) {
							subGraph := extractBidirectionalSubgraphIssues(uncappedApplied, rootIssueID, graphDepth)
							maxItems := ctx.ActiveRecipe.View.MaxItems
							reapplied := subGraph
							if maxItems > 0 && len(subGraph) > maxItems {
								rankByID := make(map[string]int, len(uncappedApplied))
								for idx, iss := range uncappedApplied {
									rankByID[iss.ID] = idx
								}
								subSet := make(map[string]bool, len(subGraph))
								for _, iss := range subGraph {
									subSet[iss.ID] = true
								}
								adj := make(map[string][]string, len(subGraph))
								for _, iss := range subGraph {
									for _, dep := range iss.Dependencies {
										if dep == nil || !subSet[dep.DependsOnID] {
											continue
										}
										adj[iss.ID] = append(adj[iss.ID], dep.DependsOnID)
										adj[dep.DependsOnID] = append(adj[dep.DependsOnID], iss.ID)
									}
								}
								selected := map[string]bool{rootIssueID: true}
								frontier := make(map[string]bool)
								for _, nb := range adj[rootIssueID] {
									if !selected[nb] {
										frontier[nb] = true
									}
								}
								for len(selected) < maxItems && len(frontier) > 0 {
									bestID := ""
									bestRank := len(uncappedApplied) + 1
									for cand := range frontier {
										r := rankByID[cand]
										if bestID == "" || r < bestRank || (r == bestRank && cand < bestID) {
											bestID = cand
											bestRank = r
										}
									}
									if bestID == "" {
										break
									}
									delete(frontier, bestID)
									selected[bestID] = true
									for _, nb := range adj[bestID] {
										if !selected[nb] {
											frontier[nb] = true
										}
									}
								}
								capped := make([]model.Issue, 0, len(selected))
								for _, iss := range uncappedApplied {
									if selected[iss.ID] {
										capped = append(capped, iss)
									}
								}
								reapplied = capped
							}
							nextCands := make(map[string]bool, len(reapplied))
							for _, iss := range reapplied {
								nextCands[iss.ID] = true
							}
							ctx.Issues = reapplied
							ctx.CandidateIDs = nextCands
							if ctx.RecipeUnblockIDs != nil {
								nextUnblock := make(map[string]bool, len(ctx.RecipeUnblockIDs)+len(reapplied))
								for id := range ctx.RecipeUnblockIDs {
									nextUnblock[id] = true
								}
								for _, iss := range reapplied {
									nextUnblock[iss.ID] = true
								}
								ctx.RecipeUnblockIDs = nextUnblock
							}
							exportIssues = reapplied
						}
					}
				}
				if !isGraphRootInActiveScope(ctx, rootIssueID) || !containsIssueID(ctx.Issues, rootIssueID) {
					fmt.Fprintf(ctx.StderrOrDefault(), "Root issue not found: %s\n", rootIssueID)
					return newReportedRobotHandlerExit(1)
				}
				exportIssues = extractBidirectionalSubgraphIssues(ctx.Issues, rootIssueID, graphDepth)
				ctx.GraphRoot = rootIssueID
				ctx.Issues = exportIssues
				if ctx.CandidateIDs != nil {
					scopedCands := make(map[string]bool, len(exportIssues))
					for _, iss := range exportIssues {
						if ctx.CandidateIDs[iss.ID] {
							scopedCands[iss.ID] = true
						}
					}
					ctx.CandidateIDs = scopedCands
				}
				ctx.DataHash = analysis.ComputeDataHash(exportIssues)
				ctx.DataHashMatchesIssues = false
			}

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
				Format:             format,
				DataHash:           ctx.DataHash,
				Readiness:          ctx.Readiness,
				ReferenceNow:       ctx.ReferenceNow(),
				SuppressActionable: !ctx.claimsProven(),
				Root:               rootIssueID,
				Depth:              graphDepth,
			}

			result, err := export.ExportGraph(exportIssues, &stats, config)
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
			graphIssues := ctx.GraphIssues()
			analyzer := ctx.GraphAnalyzer()
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				fullCfg := analysis.FullAnalysisConfig()
				analyzer.SetConfig(&fullCfg)
			}
			stats := analyzer.Analyze()
			driftResult, scopeMismatchSkipped, err := computeDriftResultForContext(ctx, analyzer, &stats, graphIssues)
			if err != nil {
				return err
			}
			if scopeMismatchSkipped {
				driftResult.SkippedChecks = append(driftResult.SkippedChecks, drift.SkippedCheck{
					Type:   drift.AlertType("baseline_scope_mismatch"),
					Reason: "saved baseline scope does not match active CLI scope; aggregate baseline deltas skipped",
				})
			}

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
				Betweenness:  buildScopedMetricItems(stats.ActiveBetweenness(), 10, ctx.CandidateIDs),
				CriticalPath: buildScopedMetricItems(stats.CriticalPathScore(), 10, ctx.CandidateIDs),
				Hubs:         buildScopedMetricItems(stats.Hubs(), 10, ctx.CandidateIDs),
				Authorities:  buildScopedMetricItems(stats.Authorities(), 10, ctx.CandidateIDs),
			}
			current := baseline.NewForDir(projectDir, currentStats, currentMetrics, cycles, "current")
			current.Scope = baselineScopeForContext(ctx)

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
			if !baselineMatchesContext(bl, ctx) {
				result.SkippedChecks = append(result.SkippedChecks, drift.SkippedCheck{
					Type:   drift.AlertType("baseline_scope_mismatch"),
					Reason: "saved baseline scope does not match active CLI scope; aggregate baseline deltas skipped",
				})
			}
			hasAlertFilters := (cfg.AlertSeverity != nil && strings.TrimSpace(*cfg.AlertSeverity) != "") ||
				(cfg.AlertType != nil && strings.TrimSpace(*cfg.AlertType) != "") ||
				(cfg.AlertLabel != nil && strings.TrimSpace(*cfg.AlertLabel) != "") ||
				(cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0)
			if hasAlertFilters {
				filtered := result.Alerts[:0]
				for _, alert := range result.Alerts {
					if cfg.AlertSeverity != nil && strings.TrimSpace(*cfg.AlertSeverity) != "" && string(alert.Severity) != strings.TrimSpace(*cfg.AlertSeverity) {
						continue
					}
					if cfg.AlertType != nil && strings.TrimSpace(*cfg.AlertType) != "" && string(alert.Type) != strings.TrimSpace(*cfg.AlertType) {
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
				if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(filtered) > *cfg.RobotMaxResults {
					filtered = filtered[:*cfg.RobotMaxResults]
				}
				result.Alerts = filtered
				result.CriticalCount, result.WarningCount, result.InfoCount = 0, 0, 0
				for _, a := range filtered {
					switch a.Severity {
					case drift.SeverityCritical:
						result.CriticalCount++
					case drift.SeverityWarning:
						result.WarningCount++
					case drift.SeverityInfo:
						result.InfoCount++
					}
				}
				result.HasDrift = result.CriticalCount > 0 || result.WarningCount > 0
			}
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
			if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(sprints) > *cfg.RobotMaxResults {
				sprints = sprints[:*cfg.RobotMaxResults]
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
			target := ""
			if cfg.RobotBurndownFlag != nil {
				target = strings.TrimSpace(*cfg.RobotBurndownFlag)
			}
			if target == "" {
				return fmt.Errorf("--robot-burndown requires a sprint ID or 'current'")
			}

			workDir, err := ctx.WorkDirOrDefault()
			if err != nil {
				return fmt.Errorf("getting current directory: %w", err)
			}
			sprints, err := loadSprintsForContext(ctx, workDir)
			if err != nil {
				return fmt.Errorf("loading sprints: %w", err)
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
			burndown := calculateBurndownAtWithThresholds(&scopedSprint, burndownIssues, ctx.Readiness, now, driftCfg.AtRiskThresholds())
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

			// Compute Graham parallel capacity bounds for open sprint beads (W37-3).
			openSprintIssues := make([]model.Issue, 0, len(scopedSprint.BeadIDs))
			seenSprintBead := make(map[string]bool, len(scopedSprint.BeadIDs))
			for _, bid := range scopedSprint.BeadIDs {
				if seenSprintBead[bid] {
					continue
				}
				seenSprintBead[bid] = true
				iss, ok := issueMap[bid]
				if !ok || iss.Status.IsClosed() || iss.Status.IsTombstone() || iss.IsTemplate || iss.Pinned || iss.Status == model.StatusPinned {
					continue
				}
				openSprintIssues = append(openSprintIssues, iss)
			}
			if len(openSprintIssues) > 0 {
				var capStats *analysis.GraphStats
				if capAnalyzer := ctx.GraphAnalyzer(); capAnalyzer != nil {
					capIssues := ctx.GraphIssues()
					capCfg := analysis.ConfigForSize(len(capIssues), countEdges(capIssues))
					if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
						capCfg = analysis.FullAnalysisConfig()
					} else {
						capCfg.ComputePageRank = false
						capCfg.ComputeBetweenness = false
						capCfg.BetweennessMode = analysis.BetweennessSkip
						capCfg.ComputeHITS = false
						capCfg.ComputeEigenvector = false
						capCfg.ComputeCycles = false
					}
					s := capAnalyzer.AnalyzeAsyncWithConfig(context.Background(), capCfg)
					s.WaitForPhase2()
					capStats = s
				}
				burndown.Capacity = buildPlanCapacity(openSprintIssues, ctx.VelocityIssues(), capStats, ctx.CandidateIDs, now)
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
			forecastTarget := ""
			if cfg.RobotForecastFlag != nil {
				forecastTarget = strings.TrimSpace(*cfg.RobotForecastFlag)
			}
			if forecastTarget == "" {
				return fmt.Errorf("--robot-forecast requires a bead ID or 'all'")
			}

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
				TotalMinutes          int       `json:"total_minutes"`
				TotalDays             float64   `json:"total_days"`
				CriticalPathMinutes   int       `json:"critical_path_minutes,omitempty"`
				CriticalPathIDs       []string  `json:"critical_path_ids,omitempty"`
				VelocityMinutesPerDay float64   `json:"velocity_minutes_per_day,omitempty"`
				ParallelizablePct     *float64  `json:"parallelizable_pct,omitempty"`
				GrahamMinDays         float64   `json:"graham_min_days,omitempty"`
				GrahamMaxDays         float64   `json:"graham_max_days,omitempty"`
				EstimatedDays         float64   `json:"estimated_days,omitempty"`
				AvgConfidence         float64   `json:"avg_confidence"`
				EarliestETA           time.Time `json:"earliest_eta"`
				LatestETA             time.Time `json:"latest_eta"`
			}
			type ForecastOutput struct {
				RobotEnvelope
				Agents        int                    `json:"agents"`
				Filters       map[string]string      `json:"filters,omitempty"`
				ForecastCount int                    `json:"forecast_count"`
				Forecasts     []analysis.ETAEstimate `json:"forecasts"`
				Summary       *ForecastSummary       `json:"summary,omitempty"`
			}

			etaIssues := ctx.VelocityIssues()
			includeInactive := cfg.IncludeInactive != nil && *cfg.IncludeInactive
			forecasts := make([]analysis.ETAEstimate, 0)
			issueByID := make(map[string]model.Issue, len(targetIssues))
			if forecastTarget == "all" {
				etaCtx := analysis.NewETAContext(etaIssues, &graphStats, now)
				for _, issue := range targetIssues {
					if issue.Status.IsClosed() || issue.Status.IsTombstone() {
						continue
					}
					if !includeInactive && (issue.IsTemplate || issue.Pinned || issue.Status == model.StatusPinned) {
						continue
					}
					issueByID[issue.ID] = issue
					forecasts = append(forecasts, etaCtx.EstimateForIssue(issue, agents))
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
				emittedOpenIssues := make([]model.Issue, 0, len(forecasts))
				for _, forecast := range forecasts {
					totalMinutes += forecast.EstimatedMinutes
					totalConfidence += forecast.Confidence
					if forecast.ETADate.Before(earliest) {
						earliest = forecast.ETADate
					}
					if forecast.ETADate.After(latest) {
						latest = forecast.ETADate
					}
					if iss, ok := issueByID[forecast.IssueID]; ok {
						emittedOpenIssues = append(emittedOpenIssues, iss)
					}
				}
				summary = &ForecastSummary{
					TotalMinutes:  totalMinutes,
					TotalDays:     float64(totalMinutes) / (60.0 * 8.0),
					AvgConfidence: totalConfidence / float64(len(forecasts)),
					EarliestETA:   earliest,
					LatestETA:     latest,
				}
				// W38-2: Velocity-calibrated Graham parallel makespan bounds honoring --forecast-agents & blocking chains.
				if len(emittedOpenIssues) > 0 {
					graham := analysis.ComputeGrahamBounds(emittedOpenIssues, etaIssues, &graphStats, agents, now)
					if graham.TotalMinutes > 0 {
						parPct := float64(max(0, graham.TotalMinutes-graham.CriticalPathMinutes)) / float64(graham.TotalMinutes) * 100.0
						var critIDs []string
						for _, cid := range graham.CriticalPathIDs {
							if ctx.CandidateIDs == nil || ctx.CandidateIDs[cid] {
								critIDs = append(critIDs, cid)
							}
						}
						summary.CriticalPathMinutes = graham.CriticalPathMinutes
						summary.CriticalPathIDs = critIDs
						summary.VelocityMinutesPerDay = math.Round(graham.VelocityMinutesPerDay*100) / 100
						roundedParPct := math.Round(parPct*10) / 10
						summary.ParallelizablePct = &roundedParPct
						summary.GrahamMinDays = math.Round(graham.GrahamMinDays*100) / 100
						summary.GrahamMaxDays = math.Round(graham.GrahamMaxDays*100) / 100
						summary.EstimatedDays = summary.GrahamMaxDays
					}
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
	register("robot-label-health", cfg.RobotLabelHealthFlag, "Output label health metrics as JSON", func(ctx RobotContext) error {
		return handleRobotLabelHealth(ctx, cfg)
	})
	register("robot-label-flow", cfg.RobotLabelFlowFlag, "Output cross-label dependency flow as JSON", func(ctx RobotContext) error {
		return handleRobotLabelFlow(ctx, cfg)
	})
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
	register("robot-whatif", cfg.RobotWhatIfFlag, "Simulate completing one or more comma-separated issues and output downstream unblock impact as JSON", func(ctx RobotContext) error {
		return handleRobotWhatIf(ctx, cfg)
	})
	register("robot-check-dep", cfg.RobotCheckDepFlag, "Validate proposed blocking dependency edge addition(s) or removal(s) (remove:FROM:TO) for cycles and readiness impact", func(ctx RobotContext) error {
		return handleRobotCheckDep(ctx, cfg)
	})
	register("robot-cass", cfg.RobotCassFlag, "Output correlated coding-agent sessions (cass) for a bead as JSON", func(ctx RobotContext) error {
		return handleRobotCass(ctx, cfg)
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
	diffOpts := datasource.DefaultDiffOptions()
	// Leave MaxDifferences uncapped during discovery so out-of-scope issues
	// cannot starve in-scope issues under --label/--repo/--recipe and so
	// source_drift_max_alerts: 0 (unlimited) is honored cleanly.
	diffOpts.MaxDifferences = 0

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

func handleRobotLabelHealth(ctx RobotContext, hcfg phaseThreeRobotHandlerConfig) error {
	cfg := analysis.DefaultLabelHealthConfig()
	baseIssues := labelAnalysisIssuesForContext(ctx)
	results := analysis.ComputeAllLabelHealthWithScope(baseIssues, cfg, ctx.ReferenceNow(), nil, ctx.Readiness, ctx.CandidateIDs)
	maxN := 0
	if hcfg.RobotMaxResults != nil && *hcfg.RobotMaxResults > 0 {
		maxN = *hcfg.RobotMaxResults
		if len(results.Labels) > maxN {
			results.Labels = results.Labels[:maxN]
		}
		if len(results.Summaries) > maxN {
			results.Summaries = results.Summaries[:maxN]
		}
		if len(results.AttentionNeeded) > maxN {
			results.AttentionNeeded = results.AttentionNeeded[:maxN]
		}
	}

	// W30-5 / P18A-14: Compute scoped label co-occurrence matrix and optional --label subgraph keystones.
	scopedIssues := baseIssues
	if ctx.CandidateIDs != nil {
		scopedIssues = make([]model.Issue, 0, len(ctx.CandidateIDs))
		for _, iss := range baseIssues {
			if ctx.CandidateIDs[iss.ID] {
				scopedIssues = append(scopedIssues, iss)
			}
		}
	}
	var cooc map[string]map[string]int
	if rawCooc := analysis.GetLabelCooccurrence(scopedIssues); len(rawCooc) > 0 {
		cleanPeers := func(label string, peers map[string]int) map[string]int {
			out := make(map[string]int, len(peers))
			for peer, cnt := range peers {
				if peer == label || cnt <= 0 {
					continue
				}
				out[peer] = cnt
			}
			if len(out) == 0 {
				return nil
			}
			return out
		}
		if targetLabel := strings.TrimSpace(ctx.LabelScope); targetLabel != "" {
			if cleaned := cleanPeers(targetLabel, rawCooc[targetLabel]); len(cleaned) > 0 {
				cooc = map[string]map[string]int{targetLabel: cleaned}
			}
		} else {
			var retainedLabels map[string]bool
			if maxN > 0 {
				retainedLabels = make(map[string]bool, len(results.Labels))
				for _, lh := range results.Labels {
					retainedLabels[lh.Label] = true
				}
			}
			filtered := make(map[string]map[string]int, len(rawCooc))
			for lbl, peers := range rawCooc {
				if retainedLabels != nil && !retainedLabels[lbl] {
					continue
				}
				if cleaned := cleanPeers(lbl, peers); len(cleaned) > 0 {
					filtered[lbl] = cleaned
				}
			}
			if len(filtered) > 0 {
				cooc = filtered
			}
		}
	}
	var subgraphPR *analysis.LabelPageRankResult
	var subgraphCP *analysis.LabelCriticalPathResult
	if targetLabel := strings.TrimSpace(ctx.LabelScope); targetLabel != "" && len(scopedIssues) > 0 {
		// P19-2: Keep 1-hop cross-label dependency context neighbors when building the label subgraph
		// (while excluding any non-candidate issues that carry targetLabel so recipe/scope filtering holds).
		subgraphInput := baseIssues
		if ctx.CandidateIDs != nil {
			subgraphInput = make([]model.Issue, 0, len(baseIssues))
			for _, iss := range baseIssues {
				if ctx.CandidateIDs[iss.ID] {
					subgraphInput = append(subgraphInput, iss)
					continue
				}
				hasTarget := false
				for _, l := range iss.Labels {
					if l == targetLabel {
						hasTarget = true
						break
					}
				}
				if !hasTarget {
					subgraphInput = append(subgraphInput, iss)
				}
			}
		}
		if sg := analysis.ComputeLabelSubgraph(subgraphInput, targetLabel); !sg.IsEmpty() {
			pr := analysis.ComputeLabelPageRank(sg)
			if maxN > 0 && len(pr.TopIssues) > maxN {
				pr.TopIssues = pr.TopIssues[:maxN]
			}
			cp := analysis.ComputeLabelCriticalPath(sg)
			subgraphPR = &pr
			subgraphCP = &cp
		}
	}

	output := struct {
		RobotEnvelope
		AnalysisConfig       analysis.LabelHealthConfig        `json:"analysis_config"`
		Results              analysis.LabelAnalysisResult      `json:"results"`
		Cooccurrence         map[string]map[string]int         `json:"cooccurrence,omitempty"`
		SubgraphPageRank     *analysis.LabelPageRankResult     `json:"subgraph_pagerank,omitempty"`
		SubgraphCriticalPath *analysis.LabelCriticalPathResult `json:"subgraph_critical_path,omitempty"`
		UsageHints           []string                          `json:"usage_hints"`
	}{
		RobotEnvelope:        ctx.Envelope(),
		AnalysisConfig:       cfg,
		Results:              results,
		Cooccurrence:         cooc,
		SubgraphPageRank:     subgraphPR,
		SubgraphCriticalPath: subgraphCP,
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

func handleRobotLabelFlow(ctx RobotContext, hcfg phaseThreeRobotHandlerConfig) error {
	now := ctx.ReferenceNow()
	cfg := analysis.DefaultLabelHealthConfig()
	baseIssues := labelAnalysisIssuesForContext(ctx)
	flow := analysis.ComputeCrossLabelFlowWithScope(baseIssues, cfg, ctx.CandidateIDs)
	cascade := analysis.ComputeBlockageCascadeAtWithScope(baseIssues, flow, cfg, now, ctx.Readiness, ctx.CandidateIDs)
	if hcfg.RobotMaxResults != nil && *hcfg.RobotMaxResults > 0 {
		maxN := *hcfg.RobotMaxResults
		if len(flow.Dependencies) > maxN {
			flow.Dependencies = flow.Dependencies[:maxN]
		}
		if len(flow.BottleneckLabels) > maxN {
			flow.BottleneckLabels = flow.BottleneckLabels[:maxN]
		}
		if len(cascade.Cascades) > maxN {
			cascade.Cascades = cascade.Cascades[:maxN]
		}
		if len(cascade.TopUnblocks) > maxN {
			cascade.TopUnblocks = cascade.TopUnblocks[:maxN]
		}
	}
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
	if !pflag.CommandLine.Changed("attention-limit") && cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		limit = *cfg.RobotMaxResults
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
		if ctx.ActiveRecipe == nil || ctx.CandidateIDs == nil {
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
		if ctx.ActiveRecipe == nil || ctx.CandidateIDs == nil {
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
		if ctx.ActiveRecipe == nil || ctx.CandidateIDs == nil {
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
		filterCandidateIntMap := func(m map[string]int) map[string]int {
			scoped := make(map[string]int, len(ctx.CandidateIDs))
			for k, v := range m {
				if ctx.CandidateIDs[k] {
					scoped[k] = v
				}
			}
			return scoped
		}
		filterCandidateSlice := func(in []string) []string {
			scoped := make([]string, 0, len(in))
			for _, id := range in {
				if ctx.CandidateIDs[id] {
					scoped = append(scoped, id)
				}
			}
			return scoped
		}
		scopedNodes, scopedEdges, scopedDensity := scopedGraphTotalsForCLI(graphIssues, &stats, ctx.CandidateIDs)
		insights.Stats = &analysis.GraphStats{
			OutDegree:        filterCandidateIntMap(insights.Stats.OutDegree),
			InDegree:         filterCandidateIntMap(insights.Stats.InDegree),
			TopologicalOrder: filterCandidateSlice(insights.Stats.TopologicalOrder),
			Density:          scopedDensity,
			NodeCount:        scopedNodes,
			EdgeCount:        scopedEdges,
			Config:           insights.Stats.EffectiveConfig(),
		}
	}

	betweennessMap := stats.ActiveBetweenness()
	coreNumberMap := stats.ActiveCoreNumber()
	articulationSlice := stats.ActiveArticulationPoints()
	criticalPathMap := stats.ActiveCriticalPathScore()
	slackMap := stats.ActiveSlack()
	if includeInactive {
		betweennessMap = stats.Betweenness()
		coreNumberMap = stats.CoreNumber()
		articulationSlice = stats.ArticulationPoints()
		criticalPathMap = stats.CriticalPathScore()
		slackMap = stats.Slack()
	}

	// W31-3: Compute scoped operational risk signals for active eligible candidates.
	riskIssueMap := make(map[string]model.Issue, len(graphIssues))
	for _, iss := range graphIssues {
		riskIssueMap[iss.ID] = iss
	}
	rawRisk := analysis.ComputeAllRiskSignalsWithReadiness(riskIssueMap, &stats, analyzer.Readiness(), ctx.ReferenceNow())
	type robotInsightsRiskItem struct {
		IssueID string `json:"issue_id"`
		Title   string `json:"title"`
		Status  string `json:"status"`
		analysis.RiskSignals
	}
	riskScoreMap := make(map[string]float64, len(rawRisk))
	highRisk := make([]robotInsightsRiskItem, 0, len(rawRisk))
	for _, iss := range graphIssues {
		if ctx.CandidateIDs != nil && !ctx.CandidateIDs[iss.ID] {
			continue
		}
		if iss.Status.IsClosed() || iss.Status.IsTombstone() || iss.IsTemplate || iss.Pinned || iss.Status == model.StatusPinned || iss.Ephemeral {
			continue
		}
		sig, ok := rawRisk[iss.ID]
		if !ok {
			continue
		}
		riskScoreMap[iss.ID] = sig.CompositeRisk
		if sig.CompositeRisk > 0 {
			highRisk = append(highRisk, robotInsightsRiskItem{
				IssueID:     iss.ID,
				Title:       iss.Title,
				Status:      string(iss.Status),
				RiskSignals: sig,
			})
		}
	}
	sort.Slice(highRisk, func(i, j int) bool {
		if highRisk[i].CompositeRisk != highRisk[j].CompositeRisk {
			return highRisk[i].CompositeRisk > highRisk[j].CompositeRisk
		}
		return highRisk[i].IssueID < highRisk[j].IssueID
	})
	riskLimit := 10
	if maxResults > 0 && maxResults < riskLimit {
		riskLimit = maxResults
	}
	if len(highRisk) > riskLimit {
		highRisk = highRisk[:riskLimit]
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
		RiskScore         map[string]float64 `json:"risk_score,omitempty"`
	}{
		PageRank:          limitMaps(filterFloatMap(stats.PageRank()), mapLimit),
		Betweenness:       limitMaps(filterFloatMap(betweennessMap), mapLimit),
		Eigenvector:       limitMaps(filterFloatMap(stats.Eigenvector()), mapLimit),
		Hubs:              limitMaps(filterFloatMap(stats.Hubs()), mapLimit),
		Authorities:       limitMaps(filterFloatMap(stats.Authorities()), mapLimit),
		CriticalPathScore: limitMaps(filterFloatMap(criticalPathMap), mapLimit),
		CoreNumber:        limitMapInt(filterIntMap(coreNumberMap), mapLimit),
		Slack:             limitMaps(filterFloatMap(slackMap), mapLimit),
		Articulation:      limitSlice(filterStringSlice(articulationSlice), mapLimit),
		RiskScore:         limitMaps(riskScoreMap, mapLimit),
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
		HighRisk         []robotInsightsRiskItem    `json:"high_risk,omitempty"`
		CrossRepoFlow    *analysis.CrossRepoFlow    `json:"cross_repo_flow,omitempty"`
		AdvancedInsights *analysis.AdvancedInsights `json:"advanced_insights,omitempty"`
		UsageHints       []string                   `json:"usage_hints"`
	}{
		RobotEnvelope:    ctx.Envelope(),
		AnalysisConfig:   stats.EffectiveConfig(),
		Status:           stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
		LabelScope:       ctx.LabelScope,
		LabelContext:     ctx.LabelContext,
		Insights:         insights,
		FullStats:        fullStats,
		TopWhatIfs:       analyzer.TopWhatIfDeltasFromStats(&stats, whatIfLimit),
		HighRisk:         highRisk,
		CrossRepoFlow:    analysis.ComputeCrossRepoFlow(graphIssues, ctx.CandidateIDs),
		AdvancedInsights: analyzer.GenerateAdvancedInsightsFromStats(&stats, analysis.DefaultAdvancedInsightsConfig()),
		UsageHints: []string{
			"jq '.Bottlenecks[:5] | map(.ID)' - Top 5 bottleneck IDs",
			"jq '.Keystones[:3]' - Top 3 critical path scores",
			"jq '.top_what_ifs[] | select(.delta.direct_unblocks > 2)' - High-impact items",
			"jq '.high_risk[:5] | map({issue_id, composite_risk, explanation})' - Top operational risk issues",
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
func generateTriageHistoryBounded(workDir, beadsPath, asOfCommit string, skipFeedback bool, beadInfos []correlation.BeadInfo, limit int, timeout time.Duration) (*correlation.HistoryReport, string) {
	histCtx := context.Background()
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		histCtx, cancel = context.WithTimeout(context.Background(), timeout)
	}
	defer cancel()

	var correlator *correlation.Correlator
	if skipFeedback {
		correlator = correlation.NewCorrelator(workDir, beadsPath)
	} else {
		var err error
		correlator, err = newCorrelatorWithFeedback(workDir, beadsPath)
		if err != nil {
			return nil, "error"
		}
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
		workDir, beadsPath, ctx.AsOfCommit, ctx.AsOf != "", beadInfos, limit, resolveRobotHistoryTimeout(cfg))
}

func handleRobotTriage(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	// bv-140: validate & scope --graph-root BEFORE spawning git history correlation
	// so invalid roots fail fast without paying history extraction latency.
	var rootIssueID string
	if cfg.GraphRoot != nil && strings.TrimSpace(*cfg.GraphRoot) != "" {
		rootIssueID = strings.TrimSpace(*cfg.GraphRoot)
	}

	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	velocityIssues := ctx.VelocityIssues()
	if rootIssueID != "" && ctx.GraphRoot != rootIssueID {
		rootSource := ctx.GraphRootSourceIssues()
		subgraphIssues, found := extractScopedGraphRootIssues(ctx, rootSource, rootIssueID)
		if !found {
			fmt.Fprintf(ctx.StderrOrDefault(), "Root issue not found: %s\n", rootIssueID)
			return newReportedRobotHandlerExit(1)
		}
		subSet := make(map[string]bool, len(subgraphIssues))
		for _, iss := range subgraphIssues {
			subSet[iss.ID] = true
		}
		if ctx.CandidateIDs == nil {
			ctx.CandidateIDs = subSet
		} else {
			intersected := make(map[string]bool, len(subSet))
			for id := range subSet {
				if ctx.CandidateIDs[id] {
					intersected[id] = true
				}
			}
			ctx.CandidateIDs = intersected
		}
		graphIssues = rootSource
		velocityIssues = filterVelocityIssuesToSubgraph(velocityIssues, subgraphIssues, rootIssueID)
		ctx.GraphRoot = rootIssueID
		ctx.Issues = subgraphIssues
		ctx.PreRecipeIssues = append([]model.Issue(nil), subgraphIssues...)
		ctx.DataHash = analysis.ComputeDataHash(subgraphIssues)
		ctx.DataHashMatchesIssues = false
	}

	historyReport, historyStatus := loadTriageHistoryForContext(ctx, cfg)
	seedHash := ""
	if rootIssueID == "" && ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
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
		VelocityIssues:    velocityIssues,
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

	// W37-1: Merge full proactive + saved-baseline drift alerts into --robot-triage
	// (matching data/triage.json, agent-brief/triage.json, --robot-alerts, and TUI Alerts).
	attachDriftAlertsToTriage(&triage, ctx, nil, nil, ctx.GraphIssues(), forceFull)

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
// loadRobotFeedbackForContext loads .beads/feedback.json from ctx's project and
// returns it together with the feedback-adjusted factor weights when enough
// accept/ignore samples exist to apply them (analysis.MinFeedbackSamples).
// A missing or unreadable file yields (nil, nil) so scoring uses the defaults.
// Every scoring surface (triage, next, priority, TUI hints) goes through the
// same rule so an agent's accept/ignore history changes what it is told next.
func isGraphRootInActiveScope(ctx RobotContext, rootIssueID string) bool {
	if rootIssueID == "" {
		return true
	}
	if ctx.Recipe != "" && ctx.RecipeUnblockIDs != nil {
		return ctx.RecipeUnblockIDs[rootIssueID]
	}
	if ctx.CandidateIDs != nil {
		return ctx.CandidateIDs[rootIssueID]
	}
	return true
}

func containsIssueID(issues []model.Issue, targetID string) bool {
	for _, iss := range issues {
		if iss.ID == targetID {
			return true
		}
	}
	return false
}

func extractScopedGraphRootIssues(ctx RobotContext, issues []model.Issue, rootID string) ([]model.Issue, bool) {
	if !isGraphRootInActiveScope(ctx, rootID) {
		return nil, false
	}
	return extractDescendantSubgraphIssues(issues, rootID)
}

func extractBidirectionalSubgraphIssues(issues []model.Issue, rootID string, maxDepth int) []model.Issue {
	if rootID == "" {
		return issues
	}
	issueMap := make(map[string]model.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}
	dependentsByID := make(map[string][]string, len(issues))
	for _, iss := range issues {
		for _, dep := range iss.Dependencies {
			if dep == nil {
				continue
			}
			if _, ok := issueMap[dep.DependsOnID]; !ok {
				continue
			}
			dependentsByID[dep.DependsOnID] = append(dependentsByID[dep.DependsOnID], iss.ID)
		}
	}
	type nodeDepth struct {
		id    string
		depth int
	}
	visited := make(map[string]bool)
	queue := []nodeDepth{{id: rootID, depth: 0}}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if visited[curr.id] {
			continue
		}
		if maxDepth > 0 && curr.depth > maxDepth {
			continue
		}
		visited[curr.id] = true
		iss, ok := issueMap[curr.id]
		if !ok {
			continue
		}
		for _, dep := range iss.Dependencies {
			if dep != nil && !visited[dep.DependsOnID] {
				queue = append(queue, nodeDepth{id: dep.DependsOnID, depth: curr.depth + 1})
			}
		}
		for _, depID := range dependentsByID[curr.id] {
			if !visited[depID] {
				queue = append(queue, nodeDepth{id: depID, depth: curr.depth + 1})
			}
		}
	}
	out := make([]model.Issue, 0, len(visited))
	for _, iss := range issues {
		if visited[iss.ID] {
			out = append(out, iss)
		}
	}
	return out
}

func extractDescendantSubgraphIssues(issues []model.Issue, rootID string) ([]model.Issue, bool) {
	if rootID == "" {
		return issues, true
	}
	issueMap := make(map[string]model.Issue, len(issues))
	children := make(map[string][]string, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
		for _, dep := range iss.Dependencies {
			if dep != nil && (dep.Type.IsBlocking() || dep.Type == model.DepParentChild) {
				children[dep.DependsOnID] = append(children[dep.DependsOnID], iss.ID)
			}
		}
	}
	if _, exists := issueMap[rootID]; !exists {
		return nil, false
	}
	visited := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, childID := range children[curr] {
			if visited[childID] {
				continue
			}
			if _, ok := issueMap[childID]; ok {
				visited[childID] = true
				queue = append(queue, childID)
			}
		}
	}
	out := make([]model.Issue, 0, len(visited))
	for _, iss := range issues {
		if visited[iss.ID] {
			out = append(out, iss)
		}
	}
	return out, true
}

func filterVelocityIssuesToSubgraph(velocityIssues, subgraphIssues []model.Issue, rootID string) []model.Issue {
	if len(velocityIssues) == 0 || rootID == "" {
		return velocityIssues
	}
	subSet := make(map[string]bool, len(subgraphIssues))
	for _, iss := range subgraphIssues {
		subSet[iss.ID] = true
	}
	if subVel, ok := extractDescendantSubgraphIssues(velocityIssues, rootID); ok {
		for _, iss := range subVel {
			subSet[iss.ID] = true
		}
	}
	out := make([]model.Issue, 0, len(subSet))
	for _, iss := range velocityIssues {
		if subSet[iss.ID] {
			out = append(out, iss)
		}
	}
	return out
}

func workspaceFeedbackDirForContext(ctx RobotContext) string {
	if strings.TrimSpace(ctx.SourcePath) != "" {
		cfgDir := filepath.Dir(ctx.SourcePath)
		if filepath.Base(cfgDir) == ".bv" {
			return cfgDir
		}
		base := workspace.ResolveWorkspaceBaseDir(ctx.SourcePath)
		if base != "" {
			return filepath.Join(base, ".bv")
		}
	}
	dir := ctx.ProjectDir
	if dir == "" {
		dir = ctx.WorkDir
	}
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".bv")
}

func loadRobotFeedbackForContext(ctx RobotContext) (*analysis.FeedbackData, *analysis.Weights) {
	if ctx.AsOf != "" {
		return nil, nil
	}
	if ctx.SourceKind == "workspace" {
		fbDir := workspaceFeedbackDirForContext(ctx)
		if fbDir == "" {
			return nil, nil
		}
		fb, err := analysis.LoadFeedback(fbDir)
		if err != nil || fb == nil {
			return nil, nil
		}
		if !fb.Applies() {
			return fb, nil
		}
		w := fb.Weights()
		return fb, &w
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
	// Validate & scope --graph-root BEFORE spawning git history correlation.
	var rootIssueID string
	if cfg.GraphRoot != nil && strings.TrimSpace(*cfg.GraphRoot) != "" {
		rootIssueID = strings.TrimSpace(*cfg.GraphRoot)
	}

	now := ctx.ReferenceNow()
	graphIssues := ctx.GraphIssues()
	velocityIssues := ctx.VelocityIssues()
	if rootIssueID != "" && ctx.GraphRoot != rootIssueID {
		rootSource := ctx.GraphRootSourceIssues()
		subgraphIssues, found := extractScopedGraphRootIssues(ctx, rootSource, rootIssueID)
		if !found {
			fmt.Fprintf(ctx.StderrOrDefault(), "Root issue not found: %s\n", rootIssueID)
			return newReportedRobotHandlerExit(1)
		}
		subSet := make(map[string]bool, len(subgraphIssues))
		for _, iss := range subgraphIssues {
			subSet[iss.ID] = true
		}
		if ctx.CandidateIDs == nil {
			ctx.CandidateIDs = subSet
		} else {
			intersected := make(map[string]bool, len(subSet))
			for id := range subSet {
				if ctx.CandidateIDs[id] {
					intersected[id] = true
				}
			}
			ctx.CandidateIDs = intersected
		}
		graphIssues = rootSource
		velocityIssues = filterVelocityIssuesToSubgraph(velocityIssues, subgraphIssues, rootIssueID)
		ctx.GraphRoot = rootIssueID
		ctx.Issues = subgraphIssues
		ctx.PreRecipeIssues = append([]model.Issue(nil), subgraphIssues...)
		ctx.DataHash = analysis.ComputeDataHash(subgraphIssues)
		ctx.DataHashMatchesIssues = false
	}

	historyReport, _ := loadTriageHistoryForContext(ctx, cfg)
	seedHash := ""
	if rootIssueID == "" && ctx.DataHashMatchesIssues && len(graphIssues) == len(ctx.Issues) {
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
		VelocityIssues:    velocityIssues,
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

	hasBrRetryScript := false
	if projectDir, dirErr := ctx.ProjectDirOrDefault(); dirErr == nil && projectDir != "" {
		if info, statErr := os.Stat(filepath.Join(projectDir, "scripts", "br_retry.sh")); statErr == nil && !info.IsDir() {
			hasBrRetryScript = true
		}
	}
	actionableHint := "Use br ready --json plus the claim gate before mutating Beads state in crowded swarms."
	noActionableRepair := "Use br ready --json for authoritative claim candidates."
	if hasBrRetryScript {
		actionableHint = "Use scripts/br_retry.sh actionable --json plus the claim gate before mutating Beads state in crowded swarms."
		noActionableRepair = "Use br ready --json or scripts/br_retry.sh actionable --json for authoritative claim candidates."
	}

	output := robotNextOutput{
		RobotEnvelope: ctx.Envelope(),
		Phase2Ready:   triage.Meta.Phase2Ready,
		Status:        triage.Status,
		UsageHints: []string{
			actionableHint,
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
			Repair:   noActionableRepair,
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
	if ctx.Command == "bead-history" && (cfg.BeadHistoryFlag == nil || strings.TrimSpace(*cfg.BeadHistoryFlag) == "") {
		return fmt.Errorf("--bead-history requires a bead ID")
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	opts := correlation.CorrelatorOptions{Limit: 500, Revision: ctx.AsOfCommit}
	targetOutOfScope := false
	if cfg.BeadHistoryFlag != nil && strings.TrimSpace(*cfg.BeadHistoryFlag) != "" {
		beadTarget := strings.TrimSpace(*cfg.BeadHistoryFlag)
		if !isCorrelationTargetInScope(ctx, beadTarget) {
			targetOutOfScope = true
		} else {
			opts.BeadID = beadTarget
		}
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
	if targetOutOfScope {
		report.Histories = map[string]correlation.BeadHistory{}
		report.CommitIndex = correlation.CommitIndex{}
		report.Stats = recomputeHistoryStatsFiltered(report.Histories, report.Stats.Strategies, report.Stats.FeedbackApplied, true)
	}

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

	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		maxN := *cfg.RobotMaxResults
		if cfg.BeadHistoryFlag != nil && *cfg.BeadHistoryFlag != "" {
			for id, hist := range report.Histories {
				if len(hist.Commits) > maxN {
					hist.Commits = hist.Commits[:maxN]
					report.Histories[id] = hist
				}
			}
		} else if len(report.Histories) > maxN {
			ids := make([]string, 0, len(report.Histories))
			for id := range report.Histories {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			capped := make(map[string]correlation.BeadHistory, maxN)
			for _, id := range ids[:maxN] {
				capped[id] = report.Histories[id]
			}
			report.Histories = capped
		}
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
	if resolved, evalErr := filepath.EvalSymlinks(dir); evalErr == nil && resolved != "" {
		dir = filepath.Clean(resolved)
	}

	ceilings := make(map[string]bool)
	for _, raw := range filepath.SplitList(os.Getenv("GIT_CEILING_DIRECTORIES")) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || !filepath.IsAbs(trimmed) {
			continue
		}
		clean := filepath.Clean(trimmed)
		ceilings[clean] = true
		if resolved, evalErr := filepath.EvalSymlinks(clean); evalErr == nil && resolved != "" {
			ceilings[filepath.Clean(resolved)] = true
		}
	}
	tempBoundaries := make(map[string]bool)
	for _, rawTmp := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		if strings.TrimSpace(rawTmp) == "" {
			continue
		}
		cleanTmp := filepath.Clean(rawTmp)
		tempBoundaries[cleanTmp] = true
		if resolved, evalErr := filepath.EvalSymlinks(cleanTmp); evalErr == nil && resolved != "" {
			tempBoundaries[filepath.Clean(resolved)] = true
		}
	}
	canonicalOrClean := func(p string) string {
		c := filepath.Clean(p)
		if resolved, evalErr := filepath.EvalSymlinks(c); evalErr == nil && resolved != "" {
			return filepath.Clean(resolved)
		}
		return c
	}
	crossedBoundary := func(cur, parent string, boundaries map[string]bool) bool {
		sep := string(filepath.Separator)
		for b := range boundaries {
			if b == "" || b == sep || b == "." {
				continue
			}
			prefix := b + sep
			if strings.HasPrefix(cur, prefix) && !strings.HasPrefix(parent, prefix) {
				return true
			}
		}
		return false
	}

	if tempBoundaries[dir] || tempBoundaries[canonicalOrClean(dir)] {
		return "", fmt.Errorf("not a git repository: %s", startPath)
	}
	for {
		curReal := canonicalOrClean(dir)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		if ceilings[dir] || ceilings[curReal] {
			return "", fmt.Errorf("not a git repository (stopped at GIT_CEILING_DIRECTORIES): %s", startPath)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a git repository: %s", startPath)
		}
		parentReal := canonicalOrClean(parent)
		if tempBoundaries[parent] || tempBoundaries[parentReal] || crossedBoundary(curReal, parentReal, tempBoundaries) {
			return "", fmt.Errorf("not a git repository: %s", startPath)
		}
		if ceilings[parent] || ceilings[parentReal] || crossedBoundary(curReal, parentReal, ceilings) {
			return "", fmt.Errorf("not a git repository (stopped at GIT_CEILING_DIRECTORIES): %s", startPath)
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

func cleanRobotSourcePath(src RobotSourceReport) string {
	sp := strings.TrimSpace(src.SourcePath)
	if sp == "" {
		return ""
	}
	if src.AsOfCommit != "" && strings.HasSuffix(sp, "@"+src.AsOfCommit) {
		sp = strings.TrimSuffix(sp, "@"+src.AsOfCommit)
	} else if at := strings.LastIndex(sp, "@"); at > 0 {
		lowerPrefix := strings.ToLower(sp[:at])
		if strings.HasSuffix(lowerPrefix, ".jsonl") || strings.HasSuffix(lowerPrefix, ".db") || strings.HasSuffix(lowerPrefix, ".beads") {
			sp = sp[:at]
		}
	}
	if sp != "" && !filepath.IsAbs(sp) && strings.TrimSpace(src.RepoRoot) != "" {
		sp = filepath.Join(src.RepoRoot, filepath.FromSlash(sp))
	}
	return filepath.Clean(sp)
}

func workspaceSourceRevision(ctx RobotContext, src RobotSourceReport, defaultRev string) string {
	if src.AsOfCommit != "" && (defaultRev == "" || defaultRev == ctx.AsOfCommit || defaultRev == ctx.AsOf) {
		return src.AsOfCommit
	}
	if ctx.AsOf != "" && defaultRev == ctx.AsOfCommit && src.AsOfCommit == "" {
		return ctx.AsOf
	}
	return defaultRev
}

func resolveSourceBeadsDir(src RobotSourceReport) string {
	sp := cleanRobotSourcePath(src)
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
		cleanSP := cleanRobotSourcePath(src)
		var beadsPath string
		if strings.HasSuffix(strings.ToLower(cleanSP), ".jsonl") {
			beadsPath = cleanSP
		} else if beadsDir != "" {
			beadsPath = resolveSQLiteStoreJSONLPath(beadsDir)
		} else if _, bp, err := resolveCorrelationBeadsPath(gitRoot); err == nil {
			beadsPath = bp
			beadsDir = filepath.Dir(bp)
		}
		if beadsPath == "" {
			continue
		}
		if ctx.AsOf == "" && src.SourceKind != "git" {
			if validateWorkspaceCorrelationStore(gitRoot, beadsDir) != nil {
				continue
			}
		} else if strings.TrimSpace(beadsDir) == "" {
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

func canonicalWorkspacePath(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil && resolvedDir != "" {
		return filepath.Clean(filepath.Join(resolvedDir, filepath.Base(clean)))
	}
	return clean
}

func workspaceSourceIdentityKey(ws workspaceCorrelationSource) string {
	return canonicalWorkspacePath(ws.repoRoot) + "|" + canonicalWorkspacePath(ws.beadsPath) + "|" + strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
}

func storeDirRelForWorkspaceSource(ws workspaceCorrelationSource) string {
	cleanRoot := canonicalWorkspacePath(ws.repoRoot)
	storeDir := canonicalWorkspacePath(filepath.Dir(ws.beadsPath))
	rel, err := filepath.Rel(cleanRoot, storeDir)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	return strings.Trim(filepath.ToSlash(rel), "/")
}

func storeSubtreePrefixForWorkspaceSource(ws workspaceCorrelationSource) string {
	cleanRoot := canonicalWorkspacePath(ws.repoRoot)
	storeDir := canonicalWorkspacePath(filepath.Dir(ws.beadsPath))
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
		repoOpts.Revision = workspaceSourceRevision(ctx, ws.source, opts.Revision)
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
		if raw || ctx.AsOf != "" {
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
	store, fbBeadID, _, err := resolveWorkspaceFeedbackStoreAndRepoForBead(ctx, workDir, beadID)
	return store, fbBeadID, err
}

func resolveWorkspaceFeedbackStoreAndRepoForBead(ctx RobotContext, workDir, beadID string) (*correlation.FeedbackStore, string, string, error) {
	if ctx.SourceKind != "workspace" || ctx.SourceAuthority == nil {
		store, err := loadCorrelationFeedbackStore(workDir)
		return store, beadID, workDir, err
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
			if strings.TrimSpace(ctx.Repo) != "" && !sourceMatchesRepoFilter(ws.source, ctx.Repo) {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(issue.SourceRepo), strings.TrimSpace(ws.source.SourceRepo)) {
				store, err := loadCorrelationFeedbackStoreForBeadsPath(ws.repoRoot, ws.beadsPath)
				if err != nil {
					return nil, "", "", err
				}
				return store, localBeadIDForWorkspaceIssue(issue, ws.source), ws.repoRoot, nil
			}
		}
	}
	// Fallback: match by longest workspace source prefix for deleted/tombstoned
	// beads absent from allIssues (so api-v2- beats api-).
	var bestMatch *workspaceCorrelationSource
	bestPrefixLen := 0
	for i := range sources {
		ws := &sources[i]
		if strings.TrimSpace(ctx.Repo) != "" && !sourceMatchesRepoFilter(ws.source, ctx.Repo) {
			continue
		}
		prefix := ws.source.RepoPrefix
		if prefix != "" && len(beadID) > len(prefix) && strings.EqualFold(beadID[:len(prefix)], prefix) {
			if len(prefix) > bestPrefixLen {
				bestPrefixLen = len(prefix)
				bestMatch = ws
			}
		}
	}
	if bestMatch != nil {
		ws := *bestMatch
		prefix := ws.source.RepoPrefix
		store, err := loadCorrelationFeedbackStoreForBeadsPath(ws.repoRoot, ws.beadsPath)
		if err != nil {
			return nil, "", "", err
		}
		stripped := beadID[len(prefix):]
		if !strings.EqualFold(stripped, beadID) {
			if part, corrErr := correlation.NewCorrelator(ws.repoRoot, ws.beadsPath).GenerateReportCached(nil, correlation.CorrelatorOptions{
				BeadID:            stripped,
				TargetBeadAliases: []string{beadID},
				Revision:          workspaceSourceRevision(ctx, ws.source, ctx.AsOfCommit),
			}); corrErr == nil {
				if _, ok := part.Histories[beadID]; ok {
					return store, beadID, ws.repoRoot, nil
				}
			}
		}
		return store, stripped, ws.repoRoot, nil
	}
	return nil, "", "", fmt.Errorf("workspace repository not found for bead %q", beadID)
}

func loadWorkspaceCorrelationFeedbackStats(ctx RobotContext, workDir string) (correlation.FeedbackStats, error) {
	if ctx.AsOf != "" {
		return correlation.FeedbackStats{}, nil
	}
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

	var scopedIssueIDs map[string]bool
	if ctx.CandidateIDs != nil {
		scopedIssueIDs = make(map[string]bool, len(ctx.CandidateIDs))
		for id, ok := range ctx.CandidateIDs {
			if ok {
				scopedIssueIDs[id] = true
			}
		}
	} else {
		scopedIssueIDs = make(map[string]bool, len(ctx.Issues))
		for _, issue := range ctx.Issues {
			scopedIssueIDs[issue.ID] = true
		}
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

		if ctx.CandidateIDs != nil {
			anyScoped := false
			for _, ws := range group {
				rk := strings.ToLower(strings.TrimSpace(ws.source.SourceRepo))
				for _, iss := range fullIssuesBySourceRepo[rk] {
					if scopedIssueIDs[iss.ID] {
						anyScoped = true
						break
					}
				}
				if anyScoped {
					break
				}
			}
			if !anyScoped {
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
		type partRejectSource struct {
			report  *correlation.HistoryReport
			qualify func(string) string
		}
		var partRejectSources []partRejectSource

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

			var correlator *correlation.Correlator
			if ctx.AsOf != "" {
				correlator = correlation.NewCorrelator(cleanRoot, ws.beadsPath)
			} else {
				var err error
				correlator, err = newCorrelatorWithFeedback(cleanRoot, ws.beadsPath)
				if err != nil {
					return nil, err
				}
			}
			part, err := correlator.GenerateReportCached(localBeads, correlation.CorrelatorOptions{
				Limit:                  limit,
				Revision:               workspaceSourceRevision(ctx, ws.source, ctx.AsOfCommit),
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
			partRejectSources = append(partRejectSources, partRejectSource{
				report: part,
				qualify: func(localID string) string {
					return qualifyWorkspaceHistoricalID(localID, ws.source, localToQualified, knownQualified)
				},
			})
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

		rootReport := &correlation.HistoryReport{
			GeneratedAt: robotNow(),
			DataHash:    ctx.DataHash,
			GitRange:    rootGitRange,
			Window:      rootWindow,
			Stats:       correlation.HistoryStats{FeedbackApplied: rootFeedback},
			Histories:   rootHistories,
			CommitIndex: correlation.BuildCommitIndex(rootHistories),
		}
		for _, prs := range partRejectSources {
			rootReport.MergeRejectedPairs(prs.report, prs.qualify)
		}
		isWorkspaceScoped := len(scopedIssueIDs) < len(allWorkspaceIssues)
		detector := correlation.NewOrphanDetectorAt(rootReport, cleanRoot, ctx.ReferenceNow()).
			WithStoreDirs(sharedMeta.storeDirsByRoot[cleanRoot]...)
		if isWorkspaceScoped {
			detector.WithScopeFilter(scopedIssueIDs)
		}
		if ctx.Repo != "" && len(group) > 1 {
			if matchedRootStore && len(unmatchedSubtrees) > 0 {
				detector.WithSubtreeScope(nil, unmatchedSubtrees)
			} else if !matchedRootStore && len(matchedSubtrees) > 0 {
				detector.WithSubtreeScope(matchedSubtrees, nil)
			}
		}
		partOrphans, err := detector.DetectOrphans(correlation.ExtractOptions{Limit: limit, Revision: workspaceSourceRevision(ctx, primary.source, ctx.AsOfCommit)})
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
			if isWorkspaceScoped {
				if cand.ExcludedByScope {
					droppedScopedOrphans++
					continue
				}
				origProbableCount := len(cand.ProbableBeads)
				filteredProbable := make([]correlation.ProbableBead, 0, origProbableCount)
				for _, pb := range cand.ProbableBeads {
					if scopedIssueIDs[pb.BeadID] {
						filteredProbable = append(filteredProbable, pb)
					}
				}
				// P30-8 / P31-4: Drop commits whose probable beads were exclusively outside the active scope.
				if origProbableCount > 0 && len(filteredProbable) == 0 {
					droppedScopedOrphans++
					continue
				}
				cand.ProbableBeads = filteredProbable
				// P31-4 / P32: Strip out-of-scope bead signals and recompute suspicion score with 35-pt message cap.
				if len(cand.Signals) > 0 {
					keptSignals := make([]correlation.OrphanSignalHit, 0, len(cand.Signals))
					for _, sig := range cand.Signals {
						if sig.BeadID != "" && !scopedIssueIDs[sig.BeadID] {
							continue
						}
						keptSignals = append(keptSignals, sig)
					}
					cand.Signals = keptSignals
					cand.SuspicionScore = correlation.ComputeOrphanSuspicionScore(keptSignals)
				}
			}
			if ctx.Repo != "" {
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
	var correlator *correlation.Correlator
	if ctx.AsOf != "" {
		correlator = correlation.NewCorrelator(workDir, beadsPath)
	} else {
		correlator, err = newCorrelatorWithFeedback(workDir, beadsPath)
		if err != nil {
			return nil, err
		}
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
	if !isCorrelationTargetInScope(ctx, beadID) {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", beadID)
		return newReportedRobotHandlerExit(1)
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	var (
		feedbackStore *correlation.FeedbackStore
		fbBeadID      string
	)
	if ctx.AsOf == "" {
		feedbackStore, fbBeadID, err = resolveWorkspaceFeedbackStoreForBead(ctx, workDir, beadID)
		if err != nil {
			return err
		}
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
	if feedbackStore != nil {
		if fb, ok := feedbackStore.Get(targetCommit.SHA, fbBeadID); ok {
			fbCopy := fb
			fbCopy.BeadID = beadID
			explanation.Feedback = &fbCopy
			explanation.Recommendation = describeCorrelationFeedback(fbCopy)
		}
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
	if !isCorrelationTargetInScope(ctx, beadID) {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", beadID)
		return newReportedRobotHandlerExit(1)
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	feedbackStore, fbBeadID, targetRepoDir, err := resolveWorkspaceFeedbackStoreAndRepoForBead(ctx, workDir, beadID)
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
	var originalConf float64
	if targetCommit == nil {
		if feedbackType != correlation.FeedbackConfirm {
			fmt.Fprintf(ctx.StderrOrDefault(), "Commit %s not found in bead %s correlations\n", commitSHA, beadID)
			return newReportedRobotHandlerExit(1)
		}
		resolved, resolveErr := correlation.ResolveCommitInRepo(context.Background(), targetRepoDir, commitSHA)
		if resolveErr != nil {
			fmt.Fprintf(ctx.StderrOrDefault(), "Commit %s not found in bead %s correlations or repository: %v\n", commitSHA, beadID, resolveErr)
			return newReportedRobotHandlerExit(1)
		}
		originalConf = 0.0
		commitSHA = resolved.SHA
	} else {
		originalConf = targetCommit.Confidence
		commitSHA = targetCommit.SHA
	}

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
	if cfg.RobotFileRelationsFlag == nil || strings.TrimSpace(*cfg.RobotFileRelationsFlag) == "" {
		return fmt.Errorf("--robot-file-relations requires a file path")
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

	threshold := 0.0
	if cfg.RelationsThreshold != nil {
		threshold = *cfg.RelationsThreshold
	}
	maxResults := 10
	if cfg.RelationsLimit != nil {
		maxResults = *cfg.RelationsLimit
	}
	if !pflag.CommandLine.Changed("relations-limit") && cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		maxResults = *cfg.RobotMaxResults
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

	effectiveAsOfCommit := ctx.AsOfCommit
	if len(matchedWorkspaceSources) > 0 {
		effectiveAsOfCommit = workspaceSourceRevision(ctx, matchedWorkspaceSources[0], ctx.AsOfCommit)
	}
	opts := correlation.CorrelatorOptions{Limit: 500, Revision: effectiveAsOfCommit}
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

	inAsOfAncestry := effectiveAsOfCommit == "" || isCommitAncestorOfRevision(matchedGitRoot, resolvedSHA, effectiveAsOfCommit)

	// If resolvedSHA fell outside the bounded --history-limit window (or lives on
	// a side branch not reachable from the primary walk revision), evaluate
	// single-commit correlation directly at resolvedSHA while retaining full
	// .beads lifecycle event windows so temporal_author and older commits work.
	if result.IsOrphan && len(result.RelatedBeads) == 0 && !result.BeadsOnly && inAsOfAncestry &&
		(opts.Since == nil || result.Timestamp.IsZero() || !result.Timestamp.Before(*opts.Since)) {
		windowTruncated := opts.Limit > 0 && (report.Window == nil || report.Window.Commits >= opts.Limit)
		runFallback := false
		fallbackRev := effectiveAsOfCommit
		if effectiveAsOfCommit != "" {
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
		detector := correlation.NewOrphanDetectorAt(report, orphanGitRoot, ctx.ReferenceNow()).
			WithStoreDirs(orphanStoreDirs...)
		if ctx.CandidateIDs != nil {
			detector.WithScopeFilter(ctx.CandidateIDs)
		}
		orphanReport, err = detector.DetectOrphans(correlation.ExtractOptions{Limit: limit, Revision: ctx.AsOfCommit})
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

	for _, candidate := range orphanReport.Candidates {
		if candidateIDs != nil {
			if candidate.ExcludedByScope {
				if orphanReport.Stats.OrphanCount > 0 {
					orphanReport.Stats.OrphanCount--
				}
				continue
			}
			if len(candidate.ProbableBeads) > 0 {
				scopedProb := make([]correlation.ProbableBead, 0, len(candidate.ProbableBeads))
				for _, pb := range candidate.ProbableBeads {
					if candidateIDs[pb.BeadID] {
						scopedProb = append(scopedProb, pb)
					}
				}
				// P29-5: Drop commits whose probable beads were exclusively outside the active scope.
				if len(scopedProb) == 0 {
					if orphanReport.Stats.OrphanCount > 0 {
						orphanReport.Stats.OrphanCount--
					}
					continue
				}
				candidate.ProbableBeads = scopedProb
			}
			// P31-4 / P32: Strip out-of-scope bead signals so hidden IDs never leak into signals[] or inflate suspicion_score.
			if len(candidate.Signals) > 0 {
				keptSignals := make([]correlation.OrphanSignalHit, 0, len(candidate.Signals))
				for _, sig := range candidate.Signals {
					if sig.BeadID != "" && !candidateIDs[sig.BeadID] {
						continue
					}
					keptSignals = append(keptSignals, sig)
				}
				candidate.Signals = keptSignals
				candidate.SuspicionScore = correlation.ComputeOrphanSuspicionScore(keptSignals)
			}
		}
		if candidate.SuspicionScore < minScore {
			continue
		}
		filtered = append(filtered, candidate)
	}

	// P32: Re-sort by post-scope SuspicionScore BEFORE applying maxResults and aggregating stats.
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].SuspicionScore != filtered[j].SuspicionScore {
			return filtered[i].SuspicionScore > filtered[j].SuspicionScore
		}
		if !filtered[i].Timestamp.Equal(filtered[j].Timestamp) {
			return filtered[i].Timestamp.After(filtered[j].Timestamp)
		}
		return filtered[i].SHA < filtered[j].SHA
	})
	if maxResults > 0 && len(filtered) > maxResults {
		filtered = filtered[:maxResults]
	}

	byBead := make(map[string][]string)
	totalSuspicion := 0
	for _, candidate := range filtered {
		totalSuspicion += candidate.SuspicionScore
		for _, bead := range candidate.ProbableBeads {
			byBead[bead.BeadID] = append(byBead[bead.BeadID], candidate.ShortSHA)
		}
	}

	orphanReport.Candidates = filtered
	orphanReport.ByBead = byBead
	orphanReport.Stats.CandidateCount = len(filtered)
	if orphanReport.Stats.TotalCommits > 0 {
		orphanReport.Stats.OrphanRatio = float64(orphanReport.Stats.OrphanCount) / float64(orphanReport.Stats.TotalCommits)
	} else {
		orphanReport.Stats.OrphanRatio = 0
	}
	orphanReport.Stats.AvgSuspicion = 0
	if len(filtered) > 0 {
		orphanReport.Stats.AvgSuspicion = float64(totalSuspicion) / float64(len(filtered))
	}
}

func handleRobotFileBeads(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if cfg.RobotFileBeadsFlag == nil || strings.TrimSpace(*cfg.RobotFileBeadsFlag) == "" {
		return fmt.Errorf("--robot-file-beads requires a file path or glob")
	}

	query := strings.TrimSpace(*cfg.RobotFileBeadsFlag)
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
	if !pflag.CommandLine.Changed("file-beads-limit") && cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		closedLimit = *cfg.RobotMaxResults
	}
	if closedLimit < 0 {
		closedLimit = 0
	}
	if len(result.ClosedBeads) > closedLimit {
		result.ClosedBeads = result.ClosedBeads[:closedLimit]
	}
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(result.OpenBeads) > *cfg.RobotMaxResults {
		result.OpenBeads = result.OpenBeads[:*cfg.RobotMaxResults]
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
	hasTargetFile := false
	for _, rf := range strings.Split(*cfg.RobotImpactFlag, ",") {
		if strings.TrimSpace(rf) != "" {
			hasTargetFile = true
			break
		}
	}
	if !hasTargetFile {
		return fmt.Errorf("--robot-impact requires at least one file path")
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
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(impactResult.AffectedBeads) > *cfg.RobotMaxResults {
		impactResult.AffectedBeads = impactResult.AffectedBeads[:*cfg.RobotMaxResults]
	}

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
	if cfg.RobotRelatedFlag == nil || strings.TrimSpace(*cfg.RobotRelatedFlag) == "" {
		return fmt.Errorf("--robot-related requires a bead ID")
	}
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
	if !pflag.CommandLine.Changed("related-max-results") && cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		options.MaxResults = *cfg.RobotMaxResults
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
	targetID := ""
	if cfg.RobotBlockerChainFlag != nil {
		targetID = strings.TrimSpace(*cfg.RobotBlockerChainFlag)
	}
	if targetID == "" {
		return fmt.Errorf("--robot-blocker-chain requires an issue ID")
	}
	inScope := false
	if ctx.CandidateIDs != nil {
		inScope = ctx.CandidateIDs[targetID]
	} else {
		for _, issue := range issues {
			if issue.ID == targetID {
				inScope = true
				break
			}
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
	rawSpec := ""
	if cfg.RobotWhatIfFlag != nil {
		rawSpec = strings.TrimSpace(*cfg.RobotWhatIfFlag)
	}
	if rawSpec == "" {
		return fmt.Errorf("--robot-whatif requires an issue ID (or comma-separated IDs)")
	}
	var targetIDs []string
	seenID := make(map[string]bool)
	for _, part := range strings.Split(rawSpec, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		targetIDs = append(targetIDs, id)
	}
	if len(targetIDs) == 0 {
		return fmt.Errorf("--robot-whatif requires an issue ID (or comma-separated IDs)")
	}
	analyzer := ctx.GraphAnalyzer()
	analyzer.SetNow(ctx.ReferenceNow())
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullCfg := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullCfg)
	}
	stats := analyzer.Analyze()

	// Preserve the exact single-ID JSON contract; emit batch_result when simulating multiple IDs (W31-2).
	if len(targetIDs) == 1 {
		targetID := targetIDs[0]
		result := analyzer.ComputeWhatIf(targetID, &stats)
		if result == nil {
			fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", targetID)
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

	batch := analyzer.ComputeWhatIfBatch(targetIDs, &stats)
	if batch == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issues not found: %s\n", strings.Join(targetIDs, ", "))
		return newReportedRobotHandlerExit(1)
	}
	if len(batch.MissingIDs) > 0 {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue(s) not found: %s\n", strings.Join(batch.MissingIDs, ", "))
		return newReportedRobotHandlerExit(1)
	}
	output := struct {
		RobotEnvelope
		BatchResult *analysis.WhatIfBatchResult `json:"batch_result"`
		UsageHints  []string                    `json:"usage_hints"`
	}{
		RobotEnvelope: ctx.Envelope(),
		BatchResult:   batch,
		UsageHints: []string{
			"jq '.batch_result.delta' - Joint direct/transitive unblock impact",
			"jq '.batch_result.synergy_issue_ids' - Issues unlocked only jointly by completing the full batch",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding batch what-if impact: %w", err)
	}
	return nil
}

type robotCheckDepEdgeResult struct {
	Operation            string   `json:"operation,omitempty"`
	FromID               string   `json:"from_id"`
	ToID                 string   `json:"to_id"`
	CanAdd               bool     `json:"can_add"`
	CanRemove            *bool    `json:"can_remove,omitempty"`
	Exists               *bool    `json:"exists,omitempty"`
	AlreadyExists        bool     `json:"already_exists"`
	WouldCycle           bool     `json:"would_cycle"`
	CyclePath            []string `json:"cycle_path,omitempty"`
	CyclesBefore         *int     `json:"cycles_before,omitempty"`
	CyclesAfter          *int     `json:"cycles_after,omitempty"`
	CyclesBroken         *int     `json:"cycles_broken,omitempty"`
	WouldBreakAllCycles  *bool    `json:"would_break_all_cycles,omitempty"`
	CyclesTruncated      *bool    `json:"cycles_truncated,omitempty"`
	NewlyUnblocked       []string `json:"newly_unblocked,omitempty"`
	WouldBlockActionable bool     `json:"would_block_actionable"`
	CrossRepo            bool     `json:"cross_repo"`
	MissingIDs           []string `json:"missing_ids,omitempty"`
	Warning              string   `json:"warning,omitempty"`
	Command              string   `json:"command,omitempty"`
}

func isSafeDepCLIArg(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	return true
}

func handleRobotCheckDep(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	rawSpec := ""
	if cfg.RobotCheckDepFlag != nil {
		rawSpec = strings.TrimSpace(*cfg.RobotCheckDepFlag)
	}
	if rawSpec == "" {
		return fmt.Errorf("--robot-check-dep requires one or more fromID:toID or remove:fromID:toID pairs")
	}

	rawPairs := strings.Split(rawSpec, ",")
	type depPair struct {
		from   string
		to     string
		remove bool
	}
	pairs := make([]depPair, 0, len(rawPairs))
	for _, token := range rawPairs {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		isRemove := false
		body := trimmed
		if len(trimmed) > len("remove:") &&
			strings.EqualFold(trimmed[:len("remove:")], "remove:") &&
			strings.Contains(trimmed[len("remove:"):], ":") {
			isRemove = true
			body = strings.TrimSpace(trimmed[len("remove:"):])
		}
		fromID, toID, ok := strings.Cut(body, ":")
		fromID = strings.TrimSpace(fromID)
		toID = strings.TrimSpace(toID)
		if !ok || fromID == "" || toID == "" {
			return fmt.Errorf("invalid --robot-check-dep edge %q: expected fromID:toID or remove:fromID:toID", trimmed)
		}
		pairs = append(pairs, depPair{from: fromID, to: toID, remove: isRemove})
	}
	if len(pairs) == 0 {
		return fmt.Errorf("--robot-check-dep requires one or more fromID:toID or remove:fromID:toID pairs")
	}

	// P16A-2: Always validate cycles and readiness against the full issue graph so
	// --repo / --label / --recipe filters never hide multi-hop intermediate paths.
	baseIssues := ctx.FullIssues
	if len(baseIssues) == 0 {
		baseIssues = ctx.GraphIssues()
	}
	workingIssues := make([]model.Issue, len(baseIssues))
	idxByID := make(map[string]int, len(baseIssues))
	for i, iss := range baseIssues {
		cloned := iss
		if len(iss.Dependencies) > 0 {
			cloned.Dependencies = make([]*model.Dependency, len(iss.Dependencies))
			for j, d := range iss.Dependencies {
				if d != nil {
					dc := *d
					cloned.Dependencies[j] = &dc
				}
			}
		}
		workingIssues[i] = cloned
		idxByID[iss.ID] = i
	}
	readiness := ctx.Readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(workingIssues)
	}
	var resolvedStubs []model.Issue
	if ctx.Readiness != nil {
		for _, rid := range ctx.Readiness.ResolvedIDs() {
			if _, ok := idxByID[rid]; !ok {
				resolvedStubs = append(resolvedStubs, model.Issue{ID: rid, Status: model.StatusTombstone})
			}
		}
	}
	rebuildReadiness := func() {
		if len(resolvedStubs) == 0 {
			readiness = model.NewReadinessIndex(workingIssues)
			return
		}
		combined := make([]model.Issue, 0, len(workingIssues)+len(resolvedStubs))
		combined = append(combined, workingIssues...)
		combined = append(combined, resolvedStubs...)
		readiness = model.NewReadinessIndex(combined)
	}
	refNow := ctx.ReferenceNow()
	canEmitCommand := ctx.AsOf == "" && ctx.AsOfCommit == "" && ctx.claimsProven()

	// Build parent -> children map for DepParentChild readiness propagation checks (P17-4).
	parentChildren := make(map[string][]string)
	for _, iss := range workingIssues {
		for _, dep := range iss.Dependencies {
			if dep != nil && dep.Type == model.DepParentChild && dep.DependsOnID != "" {
				parentChildren[dep.DependsOnID] = append(parentChildren[dep.DependsOnID], iss.ID)
			}
		}
	}
	hasReadySelfOrDescendant := func(rootID string) bool {
		if readiness.Ready(rootID, refNow) {
			return true
		}
		visited := map[string]bool{rootID: true}
		queue := append([]string(nil), parentChildren[rootID]...)
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			if visited[curr] {
				continue
			}
			visited[curr] = true
			// P18A-5: Closed/tombstone intermediate parents stop DepParentChild readiness propagation.
			if idx, ok := idxByID[curr]; ok && (workingIssues[idx].Status.IsClosed() || workingIssues[idx].Status.IsTombstone()) {
				continue
			}
			if readiness.Ready(curr, refNow) {
				return true
			}
			queue = append(queue, parentChildren[curr]...)
		}
		return false
	}

	buildVerifiedEdgeCommand := func(verb string, fromIss, toIss *model.Issue, rawFrom, rawTo string, crossRepo bool) string {
		if !canEmitCommand {
			return ""
		}
		if crossRepo && ctx.SourceKind == "workspace" {
			return ""
		}
		cmdFrom, cmdTo := rawFrom, rawTo
		trackerCLI := "br"
		if fromIss.Origin != nil || toIss.Origin != nil {
			o, pOrig := fromIss.Origin, toIss.Origin
			if o == nil || pOrig == nil ||
				strings.TrimSpace(o.ReadOnlyReason) != "" ||
				strings.TrimSpace(pOrig.ReadOnlyReason) != "" {
				return ""
			}
			sameTracker := o.Tracker == pOrig.Tracker &&
				o.Database == pOrig.Database &&
				o.WorkingDirectory == pOrig.WorkingDirectory &&
				o.TrackerDirectory == pOrig.TrackerDirectory &&
				o.Executable == pOrig.Executable
			if !sameTracker {
				return ""
			}
			if lid := strings.TrimSpace(o.LocalID); lid != "" {
				cmdFrom = lid
			}
			if lid := strings.TrimSpace(pOrig.LocalID); lid != "" {
				cmdTo = lid
			}
			if verb == "add" && cmdFrom == cmdTo {
				return ""
			}
			if strings.EqualFold(strings.TrimSpace(o.Tracker), "bd") {
				trackerCLI = "bd"
			}
		} else if ctx.SourceKind == "workspace" {
			return ""
		}
		// P21-4: Block leading-'-' flag injection and ASCII/C1 control chars.
		if !isSafeDepCLIArg(cmdFrom) || !isSafeDepCLIArg(cmdTo) {
			return ""
		}
		if ctx.SourceKind == "workspace" {
			if fromIss.Origin == nil || strings.TrimSpace(fromIss.Origin.WorkingDirectory) == "" {
				return ""
			}
			return fmt.Sprintf("cd -- %s && %s dep %s %s %s",
				shellQuoteWord(fromIss.Origin.WorkingDirectory),
				trackerCLI,
				verb,
				shellQuoteWord(cmdFrom),
				shellQuoteWord(cmdTo))
		}
		return fmt.Sprintf("%s dep %s %s %s", trackerCLI, verb, shellQuoteWord(cmdFrom), shellQuoteWord(cmdTo))
	}

	var scopedGraphIDs map[string]bool
	if ctx.CandidateIDs != nil {
		scopedGraph := ctx.GraphIssues()
		scopedGraphIDs = make(map[string]bool, len(scopedGraph)+len(ctx.CandidateIDs))
		for id := range ctx.CandidateIDs {
			scopedGraphIDs[id] = true
		}
		for _, iss := range scopedGraph {
			scopedGraphIDs[iss.ID] = true
		}
	}

	results := make([]robotCheckDepEdgeResult, 0, len(pairs))
	allSafe := true
	cycleCount := 0
	for _, p := range pairs {
		res := robotCheckDepEdgeResult{
			FromID: p.from,
			ToID:   p.to,
		}
		if p.remove {
			res.Operation = "remove"
		}
		fromIdx, fromOK := idxByID[p.from]
		toIdx, toOK := idxByID[p.to]
		var outOfScopeIDs []string
		if !fromOK {
			res.MissingIDs = append(res.MissingIDs, p.from)
		}
		if !toOK && p.to != p.from {
			res.MissingIDs = append(res.MissingIDs, p.to)
		}
		if len(res.MissingIDs) == 0 && ctx.CandidateIDs != nil {
			// Require at least one endpoint to belong to the active candidate set AND
			// both endpoints to stay inside the active scoped graph (allowing 1-hop
			// boundary neighbors under --label while rejecting unrelated out-of-scope IDs).
			if !scopedGraphIDs[p.from] {
				outOfScopeIDs = append(outOfScopeIDs, p.from)
			}
			if p.to != p.from && !scopedGraphIDs[p.to] {
				outOfScopeIDs = append(outOfScopeIDs, p.to)
			}
			if len(outOfScopeIDs) == 0 && !ctx.CandidateIDs[p.from] && !ctx.CandidateIDs[p.to] {
				outOfScopeIDs = append(outOfScopeIDs, p.from)
				if p.to != p.from {
					outOfScopeIDs = append(outOfScopeIDs, p.to)
				}
			}
		}
		if len(res.MissingIDs) > 0 || len(outOfScopeIDs) > 0 {
			res.CanAdd = false
			if p.remove {
				f := false
				res.CanRemove = &f
				res.Exists = &f
			}
			if len(res.MissingIDs) > 0 {
				res.Warning = fmt.Sprintf("Unknown issue ID(s): %s", strings.Join(res.MissingIDs, ", "))
			} else {
				res.MissingIDs = append(res.MissingIDs, outOfScopeIDs...)
				res.Warning = fmt.Sprintf("Issue ID(s) outside active scope: %s", strings.Join(outOfScopeIDs, ", "))
			}
			allSafe = false
			results = append(results, res)
			continue
		}
		fromIss := &workingIssues[fromIdx]
		toIss := &workingIssues[toIdx]
		fromRepo := strings.TrimSpace(fromIss.SourceRepo)
		toRepo := strings.TrimSpace(toIss.SourceRepo)
		res.CrossRepo = fromRepo != "" && toRepo != "" && !strings.EqualFold(fromRepo, toRepo)

		if p.remove {
			remIssues := workingIssues
			if len(resolvedStubs) > 0 {
				remIssues = make([]model.Issue, 0, len(workingIssues)+len(resolvedStubs))
				remIssues = append(remIssues, workingIssues...)
				remIssues = append(remIssues, resolvedStubs...)
			}
			remCheck := analysis.CheckDependencyRemovalWithScope(remIssues, p.from, p.to, ctx.CandidateIDs, refNow)
			existsVal := remCheck.Exists
			canRemVal := remCheck.Exists
			beforeVal := remCheck.CyclesBefore
			afterVal := remCheck.CyclesAfter
			brokenVal := remCheck.CyclesBroken
			breakAllVal := remCheck.WouldBreakAllCycles
			res.Exists = &existsVal
			res.CanRemove = &canRemVal
			res.Warning = remCheck.Warning
			if remCheck.Exists {
				res.CyclesBefore = &beforeVal
				res.CyclesAfter = &afterVal
				res.CyclesBroken = &brokenVal
				res.WouldBreakAllCycles = &breakAllVal
				if remCheck.CyclesTruncated {
					truncVal := true
					res.CyclesTruncated = &truncVal
				}
				res.NewlyUnblocked = remCheck.NewlyUnblocked
				res.Command = buildVerifiedEdgeCommand("remove", fromIss, toIss, p.from, p.to, res.CrossRepo)
				// Mutate workingIssues so sequential batches evaluate accurately.
				filtered := make([]*model.Dependency, 0, len(fromIss.Dependencies))
				for _, dep := range fromIss.Dependencies {
					if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID == p.to {
						continue
					}
					filtered = append(filtered, dep)
				}
				fromIss.Dependencies = filtered
				rebuildReadiness()
			} else {
				allSafe = false
			}
			results = append(results, res)
			continue
		}

		if p.from == p.to {
			res.CanAdd = false
			res.WouldCycle = true
			res.CyclePath = []string{p.from, p.to}
			res.Warning = fmt.Sprintf("Self-dependency %s → %s would create a 1-node cycle", p.from, p.to)
			allSafe = false
			cycleCount++
			results = append(results, res)
			continue
		}
		for _, dep := range fromIss.Dependencies {
			if dep != nil && dep.Type.IsBlocking() && dep.DependsOnID == p.to {
				res.AlreadyExists = true
				break
			}
		}
		if res.AlreadyExists {
			// P16A-3: An existing edge is a no-op and never newly blocks an actionable issue.
			res.CanAdd = true
			res.Warning = fmt.Sprintf("Blocking dependency %s → %s already exists", p.from, p.to)
			results = append(results, res)
			continue
		}
		res.WouldBlockActionable = !fromIss.Status.IsClosed() &&
			!fromIss.Status.IsTombstone() &&
			!toIss.Status.IsClosed() &&
			!toIss.Status.IsTombstone() &&
			hasReadySelfOrDescendant(p.from)

		canAdd, cyclePath, warning := analysis.CheckDependencyAddition(workingIssues, p.from, p.to)
		res.CanAdd = canAdd
		res.WouldCycle = !canAdd
		res.CyclePath = cyclePath
		res.Warning = warning
		if canAdd {
			res.Command = buildVerifiedEdgeCommand("add", fromIss, toIss, p.from, p.to, res.CrossRepo)
			// Accumulate safe edges so multi-edge batch requests detect compound cycles (e.g. A:B,B:C,C:A)
			// and keep sequential readiness accurate (P16A-3).
			fromIss.Dependencies = append(fromIss.Dependencies, &model.Dependency{
				IssueID:     p.from,
				DependsOnID: p.to,
				Type:        model.DepBlocks,
			})
			if !toIss.Status.IsClosed() && !toIss.Status.IsTombstone() {
				rebuildReadiness()
			}
		} else {
			allSafe = false
			cycleCount++
		}
		results = append(results, res)
	}

	output := struct {
		RobotEnvelope
		AllSafe      bool                      `json:"all_safe"`
		CheckedCount int                       `json:"checked_count"`
		CycleCount   int                       `json:"cycle_count"`
		Results      []robotCheckDepEdgeResult `json:"results"`
	}{
		RobotEnvelope: ctx.Envelope(),
		AllSafe:       allSafe,
		CheckedCount:  len(results),
		CycleCount:    cycleCount,
		Results:       results,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding check-dep result: %w", err)
	}
	return nil
}

type robotCassSession struct {
	SourcePath    string   `json:"source_path"`
	LineNumber    int      `json:"line_number"`
	Agent         string   `json:"agent"`
	Title         string   `json:"title"`
	Snippet       string   `json:"snippet"`
	Timestamp     string   `json:"timestamp"`
	Workspace     string   `json:"workspace,omitempty"`
	MatchType     string   `json:"match_type,omitempty"`
	BaseScore     float64  `json:"base_score"`
	FinalScore    float64  `json:"final_score"`
	Strategy      string   `json:"strategy"`
	Keywords      []string `json:"keywords,omitempty"`
	ViewCommand   string   `json:"view_command,omitempty"`
	ExpandCommand string   `json:"expand_command,omitempty"`
}

func shellQuoteCassArg(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func resolveCassTargetIssue(ctx RobotContext, rawID string) *model.Issue {
	targetID := strings.TrimSpace(rawID)
	if targetID == "" {
		return nil
	}
	candidates := scopedCorrelationIssues(ctx)
	for i := range candidates {
		if candidates[i].ID == targetID {
			return &candidates[i]
		}
	}
	var uniqueLocal *model.Issue
	for i := range candidates {
		if candidates[i].Origin != nil && strings.TrimSpace(candidates[i].Origin.LocalID) == targetID {
			if uniqueLocal != nil {
				return nil
			}
			uniqueLocal = &candidates[i]
		}
	}
	return uniqueLocal
}

func cassStatusCode(s cass.Status) string {
	switch s {
	case cass.StatusHealthy:
		return "healthy"
	case cass.StatusNeedsIndex:
		return "needs_indexing"
	case cass.StatusNotInstalled:
		return "not_installed"
	default:
		return "unknown"
	}
}

func cassLookupBudget() time.Duration {
	timeoutMs := env.CassSearchTimeoutMS.Int(3000)
	if timeoutMs <= 0 {
		timeoutMs = 3000
	} else if timeoutMs < 100 {
		timeoutMs = 100
	} else if timeoutMs > 3600000 {
		timeoutMs = 3600000
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

func handleRobotCass(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	rawTarget := ""
	if cfg.RobotCassFlag != nil {
		rawTarget = strings.TrimSpace(*cfg.RobotCassFlag)
	}
	if rawTarget == "" {
		return fmt.Errorf("--robot-cass requires a bead ID")
	}
	targetIssue := resolveCassTargetIssue(ctx, rawTarget)
	if targetIssue == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", rawTarget)
		return newReportedRobotHandlerExit(1)
	}

	workDir, err := ctx.ProjectDirOrDefault()
	if err != nil || strings.TrimSpace(workDir) == "" {
		workDir, _ = ctx.WorkDirOrDefault()
	}

	budget := cassLookupBudget()

	detector := cass.NewDetectorWithOptions(cass.WithHealthTimeout(min(budget, 2*time.Second)))
	status := detector.Check()
	available := status == cass.StatusHealthy || status == cass.StatusNeedsIndex

	corr := cass.CorrelationResult{
		BeadID:   targetIssue.ID,
		Keywords: cass.ExtractKeywords(targetIssue.Title),
	}
	if available {
		searcher := cass.NewSearcherWithOptions(detector, cass.WithSearchTimeout(budget))
		corrOpts := []cass.CorrelatorOption{
			cass.WithWorkspace(workDir),
			cass.WithNow(func() time.Time { return ctx.ReferenceNow() }),
		}
		if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
			corrOpts = append(corrOpts, cass.WithMaxSessions(*cfg.RobotMaxResults))
		}
		if ctx.AsOf != "" {
			cutoff := ctx.AsOfCommitTime.UTC()
			if cutoff.IsZero() {
				cutoff = ctx.ReferenceNow().UTC()
			}
			corrOpts = append(corrOpts,
				cass.WithCutoff(cutoff),
				cass.WithNow(func() time.Time { return cutoff }),
			)
		}
		correlator := cass.NewCorrelatorWithOptions(searcher, nil, corrOpts...)
		func() {
			corrCtx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			corr = correlator.Correlate(corrCtx, targetIssue)
		}()
	}

	keywords := corr.Keywords
	if keywords == nil {
		keywords = []string{}
	}

	sessions := make([]robotCassSession, 0, len(corr.TopSessions))
	for _, hit := range corr.TopSessions {
		ts := ""
		if !hit.Timestamp.IsZero() {
			ts = hit.Timestamp.UTC().Format(time.RFC3339)
		}
		var viewCmd, expandCmd string
		if strings.TrimSpace(hit.SourcePath) != "" {
			quoted := shellQuoteCassArg(hit.SourcePath)
			if hit.LineNumber > 0 {
				viewCmd = fmt.Sprintf("cass view %s -n %d --json", quoted, hit.LineNumber)
				expandCmd = fmt.Sprintf("cass expand %s -n %d -C 3 --json", quoted, hit.LineNumber)
			} else {
				viewCmd = fmt.Sprintf("cass view %s --json", quoted)
			}
		}
		sessions = append(sessions, robotCassSession{
			SourcePath:    hit.SourcePath,
			LineNumber:    hit.LineNumber,
			Agent:         hit.Agent,
			Title:         hit.Title,
			Snippet:       hit.Snippet,
			Timestamp:     ts,
			Workspace:     hit.Workspace,
			MatchType:     hit.MatchType,
			BaseScore:     hit.BaseScore,
			FinalScore:    hit.FinalScore,
			Strategy:      string(hit.Strategy),
			Keywords:      hit.Keywords,
			ViewCommand:   viewCmd,
			ExpandCommand: expandCmd,
		})
	}
	totalFound := corr.TotalFound
	if len(sessions) == 0 && ctx.AsOf != "" {
		totalFound = 0
		corr.Strategy = ""
	}
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && len(sessions) > *cfg.RobotMaxResults {
		sessions = sessions[:*cfg.RobotMaxResults]
	}

	output := struct {
		RobotEnvelope
		BeadID     string             `json:"bead_id"`
		BeadTitle  string             `json:"bead_title"`
		CassStatus string             `json:"cass_status"`
		Available  bool               `json:"available"`
		Strategy   string             `json:"strategy"`
		Keywords   []string           `json:"keywords"`
		TotalFound int                `json:"total_found"`
		TimedOut   bool               `json:"timed_out"`
		Diagnostic string             `json:"diagnostic,omitempty"`
		Sessions   []robotCassSession `json:"sessions"`
		UsageHints []string           `json:"usage_hints"`
	}{
		RobotEnvelope: ctx.Envelope(),
		BeadID:        targetIssue.ID,
		BeadTitle:     targetIssue.Title,
		CassStatus:    cassStatusCode(status),
		Available:     available,
		Strategy:      string(corr.Strategy),
		Keywords:      keywords,
		TotalFound:    totalFound,
		TimedOut:      corr.TimedOut,
		Diagnostic:    corr.Error,
		Sessions:      sessions,
		UsageHints: []string{
			"jq '.sessions[0].expand_command' - Command to expand surrounding transcript turns",
			"jq '.sessions[] | {agent, title, final_score, strategy, view_command}' - Ranked prior agent sessions",
			"Set BV_CASS_SEARCH_TIMEOUT_MS=8000 if timed_out is true on a large session archive",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding cass sessions: %w", err)
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
	if cfg.RobotImpactNetworkFlag != nil {
		trimmed := strings.TrimSpace(*cfg.RobotImpactNetworkFlag)
		if trimmed != "all" {
			beadID = trimmed
		}
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

	netResult := network.ToResult(beadID, depth)
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 && netResult != nil {
		maxN := *cfg.RobotMaxResults
		if len(netResult.TopClusters) > maxN {
			netResult.TopClusters = netResult.TopClusters[:maxN]
		}
		if len(netResult.TopConnected) > maxN {
			netResult.TopConnected = netResult.TopConnected[:maxN]
		}
		if netResult.Network != nil {
			netCopy := *netResult.Network
			if len(netCopy.Edges) > maxN {
				netCopy.Edges = netCopy.Edges[:maxN]
			}
			if len(netCopy.Clusters) > maxN {
				netCopy.Clusters = netCopy.Clusters[:maxN]
			}
			netResult.Network = &netCopy
		}
	}

	output, err := withEnvelope(ctx.EnvelopeWithHash(report.DataHash), netResult)
	if err != nil {
		return fmt.Errorf("impact network payload: %w", err)
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding impact network: %w", err)
	}
	return nil
}

func handleRobotCausality(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	targetID := ""
	if cfg.RobotCausalityFlag != nil {
		targetID = strings.TrimSpace(*cfg.RobotCausalityFlag)
	}
	if targetID == "" {
		return fmt.Errorf("--robot-causality requires a bead ID")
	}
	if !isCorrelationTargetInScope(ctx, targetID) {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}
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
	opts := correlation.CorrelatorOptions{Limit: limit, CausalityBeadID: targetID, Revision: ctx.AsOfCommit}
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
	result := report.BuildCausalityChainAt(targetID, correlation.CausalityOptions{
		IncludeCommits: true,
		BlockerTitles:  blockerTitles,
	}, ctx.ReferenceNow())
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", targetID)
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

// attachPriorityRecommendationAction binds an authority-verified MutationSetPriority
// command to a priority recommendation when SuggestedPriority != CurrentPriority (W38-7).
func attachPriorityRecommendationAction(rec *analysis.PriorityRecommendation, iss model.Issue, ok bool, ctx RobotContext) {
	if rec == nil || rec.SuggestedPriority == rec.CurrentPriority || !ok {
		return
	}
	if ctx.AsOf != "" || ctx.AsOfCommit != "" {
		rec.Action = nil
		rec.ActionCommand = ""
		rec.ActionUnavailableReason = "historical snapshot is read-only"
		return
	}
	if !ctx.claimsProven() {
		rec.Action = nil
		rec.ActionCommand = ""
		rec.ActionUnavailableReason = "source authority is incomplete or stale"
		return
	}
	cmd, reason := iss.MutationAction(model.MutationSetPriority, nil, strconv.Itoa(rec.SuggestedPriority))
	if cmd != nil {
		rec.Action = cmd
		rec.ActionCommand = cmd.Shell
		rec.ActionUnavailableReason = ""
	} else {
		rec.Action = nil
		rec.ActionCommand = ""
		rec.ActionUnavailableReason = reason
	}
}

func handleRobotSprintShow(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	targetID := ""
	if cfg.RobotSprintShowFlag != nil {
		targetID = strings.TrimSpace(*cfg.RobotSprintShowFlag)
	}
	if targetID == "" {
		return fmt.Errorf("--robot-sprint-show requires a sprint ID")
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	sprints, err := loadSprintsForContext(ctx, workDir)
	if err != nil {
		return fmt.Errorf("loading sprints: %w", err)
	}

	found := matchSprintByID(sprints, targetID)
	if found == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}

	// W38-4: Honor active CandidateIDs scope and enrich --robot-sprint-show with computed
	// status, burndown summary, at-risk items, Graham capacity, scope changes, and per-bead state.
	now := ctx.ReferenceNow()
	scopedSprint := *found
	if ctx.CandidateIDs != nil {
		filteredBeads := make([]string, 0, len(scopedSprint.BeadIDs))
		for _, bid := range scopedSprint.BeadIDs {
			if ctx.CandidateIDs[bid] {
				filteredBeads = append(filteredBeads, bid)
			}
		}
		scopedSprint.BeadIDs = filteredBeads
	}
	sprintGraphIssues := ctx.GraphIssues()
	if len(ctx.FullIssues) > 0 {
		sprintGraphIssues = labelAnalysisIssuesForContext(ctx)
	}
	issueMap := make(map[string]model.Issue, len(sprintGraphIssues))
	for _, iss := range sprintGraphIssues {
		issueMap[iss.ID] = iss
	}
	readiness := ctx.Readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(sprintGraphIssues)
	}
	projectDir, _ := ctx.ProjectDirOrDefault()
	driftCfg, cfgErr := drift.LoadConfig(projectDir)
	if cfgErr != nil || driftCfg == nil {
		driftCfg = drift.DefaultConfig()
	}

	openSprintIssues := make([]model.Issue, 0, len(scopedSprint.BeadIDs))
	dedupedBeadIDs := make([]string, 0, len(scopedSprint.BeadIDs))
	beads := make([]exportSprintBead, 0, len(scopedSprint.BeadIDs))
	seenBead := make(map[string]bool, len(scopedSprint.BeadIDs))
	for _, bid := range scopedSprint.BeadIDs {
		if seenBead[bid] {
			continue
		}
		seenBead[bid] = true
		dedupedBeadIDs = append(dedupedBeadIDs, bid)
		iss, ok := issueMap[bid]
		if !ok {
			continue
		}
		isClosed := iss.Status.IsClosed() || iss.Status.IsTombstone()
		if !isClosed && !iss.IsTemplate && !iss.Pinned && iss.Status != model.StatusPinned {
			openSprintIssues = append(openSprintIssues, iss)
		}
		isBlocked := !isClosed && (iss.Status == model.StatusBlocked || len(readiness.Blockers(iss.ID)) > 0)
		beads = append(beads, exportSprintBead{
			ID:               iss.ID,
			Title:            iss.Title,
			Status:           string(iss.Status),
			Priority:         iss.Priority,
			IssueType:        string(iss.IssueType),
			Assignee:         iss.Assignee,
			EstimatedMinutes: iss.EstimatedMinutes,
			IsBlocked:        isBlocked,
		})
	}
	scopedSprint.BeadIDs = dedupedBeadIDs
	burndown := calculateBurndownAtWithThresholds(&scopedSprint, sprintGraphIssues, readiness, now, driftCfg.AtRiskThresholds())
	var scopeChanges []ScopeChangeEvent
	if changes, sErr := computeSprintScopeChangesForContext(ctx, workDir, found, issueMap, now); sErr == nil && len(changes) > 0 {
		if ctx.CandidateIDs != nil {
			filteredChanges := changes[:0]
			for _, ev := range changes {
				if ctx.CandidateIDs[ev.IssueID] {
					filteredChanges = append(filteredChanges, ev)
				}
			}
			changes = filteredChanges
		}
		if len(changes) > 0 {
			scopeChanges = changes
		}
	}
	var capInfo *analysis.PlanCapacity
	if len(openSprintIssues) > 0 {
		var capStats *analysis.GraphStats
		if capAnalyzer := ctx.GraphAnalyzer(); capAnalyzer != nil {
			capIssues := ctx.GraphIssues()
			capCfg := analysis.ConfigForSize(len(capIssues), countEdges(capIssues))
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				capCfg = analysis.FullAnalysisConfig()
			} else {
				capCfg.ComputePageRank = false
				capCfg.ComputeBetweenness = false
				capCfg.BetweennessMode = analysis.BetweennessSkip
				capCfg.ComputeHITS = false
				capCfg.ComputeEigenvector = false
				capCfg.ComputeCycles = false
			}
			s := capAnalyzer.AnalyzeAsyncWithConfig(context.Background(), capCfg)
			s.WaitForPhase2()
			capStats = s
		}
		capInfo = buildPlanCapacity(openSprintIssues, ctx.VelocityIssues(), capStats, ctx.CandidateIDs, now)
	}
	isActive := scopedSprint.IsActiveAt(now)
	sprintStatus := "active"
	if !scopedSprint.StartDate.IsZero() && now.Before(scopedSprint.StartDate) {
		sprintStatus = "upcoming"
	} else if !scopedSprint.EndDate.IsZero() && now.After(scopedSprint.EndDate) {
		sprintStatus = "completed"
	}
	pctComplete := 0.0
	if burndown.TotalIssues > 0 {
		pctComplete = math.Round(float64(burndown.CompletedIssues)/float64(burndown.TotalIssues)*1000) / 10
	}
	type sprintShowSummary struct {
		TotalDays         int        `json:"total_days"`
		ElapsedDays       int        `json:"elapsed_days"`
		RemainingDays     int        `json:"remaining_days"`
		TotalIssues       int        `json:"total_issues"`
		CompletedIssues   int        `json:"completed_issues"`
		RemainingIssues   int        `json:"remaining_issues"`
		PercentComplete   float64    `json:"percent_complete"`
		IdealBurnRate     float64    `json:"ideal_burn_rate"`
		ActualBurnRate    float64    `json:"actual_burn_rate"`
		ProjectedComplete *time.Time `json:"projected_complete,omitempty"`
		OnTrack           bool       `json:"on_track"`
	}

	output := struct {
		RobotEnvelope
		Sprint       *model.Sprint          `json:"sprint"`
		Status       string                 `json:"status"`
		IsActive     bool                   `json:"is_active"`
		Summary      sprintShowSummary      `json:"summary"`
		AtRisk       []analysis.AtRiskItem  `json:"at_risk"`
		Capacity     *analysis.PlanCapacity `json:"capacity,omitempty"`
		ScopeChanges []ScopeChangeEvent     `json:"scope_changes,omitempty"`
		Beads        []exportSprintBead     `json:"beads"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Sprint:        &scopedSprint,
		Status:        sprintStatus,
		IsActive:      isActive,
		Summary: sprintShowSummary{
			TotalDays:         burndown.TotalDays,
			ElapsedDays:       burndown.ElapsedDays,
			RemainingDays:     burndown.RemainingDays,
			TotalIssues:       burndown.TotalIssues,
			CompletedIssues:   burndown.CompletedIssues,
			RemainingIssues:   burndown.RemainingIssues,
			PercentComplete:   pctComplete,
			IdealBurnRate:     burndown.IdealBurnRate,
			ActualBurnRate:    burndown.ActualBurnRate,
			ProjectedComplete: burndown.ProjectedComplete,
			OnTrack:           burndown.OnTrack,
		},
		AtRisk:       burndown.AtRisk,
		Capacity:     capInfo,
		ScopeChanges: scopeChanges,
		Beads:        beads,
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

	includeInactive := cfg.IncludeInactive != nil && *cfg.IncludeInactive
	openIssues := make([]model.Issue, 0, len(targetIssues))
	for _, issue := range targetIssues {
		if issue.Status.IsClosed() || issue.Status.IsTombstone() {
			continue
		}
		if !includeInactive && (issue.IsTemplate || issue.Pinned || issue.Status == model.StatusPinned) {
			continue
		}
		openIssues = append(openIssues, issue)
	}

	agents := 1
	if cfg.CapacityAgents != nil && *cfg.CapacityAgents > 0 {
		agents = *cfg.CapacityAgents
	}

	readiness := analyzer.Readiness()
	actionable := make([]string, 0)
	for _, issue := range openIssues {
		if readiness.Ready(issue.ID, now) {
			actionable = append(actionable, issue.ID)
		}
	}

	graham := analysis.ComputeGrahamBounds(openIssues, ctx.VelocityIssues(), &graphStats, agents, now)
	totalMinutes := graham.TotalMinutes
	serialMinutes := graham.CriticalPathMinutes
	longestChain := graham.CriticalPathIDs
	if longestChain == nil {
		longestChain = []string{}
	}

	parallelMinutes := totalMinutes - serialMinutes
	parallelizablePct := 0.0
	if totalMinutes > 0 {
		parallelizablePct = float64(parallelMinutes) / float64(totalMinutes) * 100
	}
	velPerDay := graham.VelocityMinutesPerDay
	if velPerDay <= 0 {
		velPerDay = 60.0 * 8.0
	}
	effectiveMinutes := float64(serialMinutes) + float64(parallelMinutes)/float64(agents)
	estimatedDays := effectiveMinutes / velPerDay
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
	openSet := make(map[string]bool, len(openIssues))
	for _, issue := range openIssues {
		openSet[issue.ID] = true
	}
	blocks := make(map[string][]string, len(openIssues))
	for _, issue := range openIssues {
		seen := make(map[string]bool)
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() || dep.DependsOnID == issue.ID || seen[dep.DependsOnID] {
				continue
			}
			if openSet[dep.DependsOnID] {
				seen[dep.DependsOnID] = true
				blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], issue.ID)
			}
		}
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
	bottleneckLimit := 5
	if cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		bottleneckLimit = *cfg.RobotMaxResults
		if len(actionable) > bottleneckLimit {
			actionable = actionable[:bottleneckLimit]
		}
	}
	if len(bottlenecks) > bottleneckLimit {
		bottlenecks = bottlenecks[:bottleneckLimit]
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

// longestWeightedCapacityChain selects the simple blocking path maximizing
// cumulative work minutes (matching Graham critical_path_minutes), breaking
// ties by hop count and seed/neighbor order.
func scopedGraphTotalsForCLI(issues []model.Issue, stats *analysis.GraphStats, candidateIDs map[string]bool) (int, int, float64) {
	if stats == nil {
		return 0, 0, 0
	}
	if candidateIDs == nil {
		return stats.NodeCount, stats.EdgeCount, stats.Density
	}
	present := make(map[string]bool, len(candidateIDs))
	for _, iss := range issues {
		if iss.ID != "" && candidateIDs[iss.ID] {
			present[iss.ID] = true
		}
	}
	nodeCount := len(present)
	edgeCount := 0
	for _, iss := range issues {
		if !present[iss.ID] {
			continue
		}
		seen := make(map[string]bool)
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() || dep.DependsOnID == iss.ID || seen[dep.DependsOnID] {
				continue
			}
			if present[dep.DependsOnID] {
				seen[dep.DependsOnID] = true
				edgeCount++
			}
		}
	}
	density := 0.0
	if nodeCount > 1 {
		density = float64(edgeCount) / float64(nodeCount*(nodeCount-1))
	}
	return nodeCount, edgeCount, density
}

func sanitizedGitEnvForCLI() []string {
	return loader.SanitizedGitEnv()
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

func isStringFlagPtr(flagPtr interface{}) bool {
	if flagPtr == nil {
		return false
	}
	value := reflect.ValueOf(flagPtr)
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		return value.Type().Elem().Kind() == reflect.String
	}
	return value.Kind() == reflect.String
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
	if !pflag.CommandLine.Changed("hotspots-limit") && cfg.RobotMaxResults != nil && *cfg.RobotMaxResults > 0 {
		hotspotsLimit = *cfg.RobotMaxResults
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
		resolvedSprintPath := resolveSprintPathForRel(origin.sprintsFilePath)
		relPath, err := filepath.Rel(gitRoot, resolvedSprintPath)
		if err != nil || relPath == "" || relPath == "." || strings.HasPrefix(relPath, "..") {
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

// computeDriftResultForContext evaluates all pkg/drift detectors (with optional
// saved baseline comparison and live source inconsistency checks) against the
// provided scoped analyzer and graph statistics.
func computeDriftResultForContext(ctx RobotContext, analyzer *analysis.Analyzer, stats *analysis.GraphStats, graphIssues []model.Issue) (*drift.Result, bool, error) {
	projectDir, err := ctx.ProjectDirOrDefault()
	if err != nil {
		return nil, false, fmt.Errorf("getting project directory: %w", err)
	}
	baselinePath, err := ctx.BaselinePathOrDefault()
	if err != nil {
		return nil, false, fmt.Errorf("getting baseline path: %w", err)
	}

	driftConfig, err := drift.LoadConfig(projectDir)
	if err != nil {
		return nil, false, fmt.Errorf("loading drift config: %w", err)
	}

	if analyzer == nil {
		analyzer = ctx.GraphAnalyzer()
	}
	if stats == nil {
		analyzed := analyzer.Analyze()
		stats = &analyzed
	}

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
	scopedNodes, scopedEdges, scopedDensity := scopedGraphTotalsForCLI(graphIssues, stats, ctx.CandidateIDs)
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

	curScope := baselineScopeForContext(ctx)
	bl := &baseline.Baseline{Stats: curStats, Scope: curScope}
	cur := &baseline.Baseline{Stats: curStats, Cycles: cycles, Scope: curScope}
	scopeMismatchSkipped := false
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
				Betweenness:  buildScopedMetricItems(stats.ActiveBetweenness(), 10, ctx.CandidateIDs),
				CriticalPath: buildScopedMetricItems(stats.CriticalPathScore(), 10, ctx.CandidateIDs),
				Hubs:         buildScopedMetricItems(stats.Hubs(), 10, ctx.CandidateIDs),
				Authorities:  buildScopedMetricItems(stats.Authorities(), 10, ctx.CandidateIDs),
			}
			cur = &baseline.Baseline{Stats: curStats, TopMetrics: topMetrics, Cycles: cycles, Scope: curScope}
			if !baselineMatchesContext(loaded, ctx) {
				scopeMismatchSkipped = true
			}
		}
	}

	calc := drift.NewCalculator(bl, cur, driftConfig)
	calc.SetNow(analyzer.Now())
	calc.SetIssues(graphIssues)
	if !calc.ReuseAnalyzer(analyzer) {
		return nil, false, fmt.Errorf("preparing alerts: issue rows do not match the scoped analyzer")
	}
	attachSourceDiffsForContext(calc, ctx, driftConfig.SourceDriftMaxAlerts)
	return calc.Calculate(), scopeMismatchSkipped, nil
}

// attachDriftAlertsToTriage merges full pkg/drift alerts (stale_issue,
// blocking_cascade, high_impact_unblock, potential_duplicate, priority_mismatch,
// scope_creep, baseline regressions, source_inconsistency, etc.) into
// triage.Alerts for static-site exports so the exported dashboard matches
// --robot-alerts and the TUI Alerts panel.
func attachDriftAlertsToTriage(triage *analysis.TriageResult, ctx RobotContext, analyzer *analysis.Analyzer, stats *analysis.GraphStats, graphIssues []model.Issue, forceFullAnalysis bool) *drift.Result {
	if triage == nil {
		return nil
	}
	if analyzer == nil {
		analyzer = ctx.GraphAnalyzer()
		if forceFullAnalysis && analyzer != nil {
			fullCfg := analysis.FullAnalysisConfig()
			analyzer.SetConfig(&fullCfg)
		}
		stats = nil
	}
	driftResult, scopeMismatchSkipped, err := computeDriftResultForContext(ctx, analyzer, stats, graphIssues)
	if err != nil || driftResult == nil {
		return nil
	}
	if scopeMismatchSkipped {
		driftResult.SkippedChecks = append(driftResult.SkippedChecks, drift.SkippedCheck{
			Type:   drift.AlertType("baseline_scope_mismatch"),
			Reason: "saved baseline scope does not match active CLI scope; aggregate baseline deltas skipped",
		})
	}
	if len(driftResult.Alerts) == 0 {
		return driftResult
	}

	seenExact := make(map[string]bool, len(triage.Alerts))
	seenTypeIssue := make(map[string]bool, len(triage.Alerts))
	hasType := make(map[string]bool, len(triage.Alerts))
	for _, existing := range triage.Alerts {
		hasType[existing.Type] = true
		if existing.IssueID != "" {
			seenTypeIssue[existing.Type+"\x00"+existing.IssueID] = true
		}
		for _, id := range existing.IssueIDs {
			if id != "" {
				seenTypeIssue[existing.Type+"\x00"+id] = true
			}
		}
		seenExact[existing.Type+"\x00"+existing.IssueID+"\x00"+strings.Join(existing.IssueIDs, ",")+"\x00"+existing.Message] = true
	}

	for _, da := range driftResult.Alerts {
		alertType := string(da.Type)
		if da.Type == drift.AlertVelocityDrop && hasType["velocity_drop"] {
			continue
		}
		// Deduplicate single-issue abandoned_claim overlap against built-in triage summary.
		if da.Type == drift.AlertAbandonedClaim && seenTypeIssue[alertType+"\x00"+da.IssueID] {
			continue
		}

		mapped := analysis.Alert{
			Type:     alertType,
			Severity: string(da.Severity),
			Message:  da.Message,
			IssueID:  da.IssueID,
		}
		if da.RelatedIssueID != "" {
			if da.IssueID != "" {
				mapped.IssueIDs = []string{da.IssueID, da.RelatedIssueID}
			} else {
				mapped.IssueIDs = []string{da.RelatedIssueID}
			}
		}
		exactKey := mapped.Type + "\x00" + mapped.IssueID + "\x00" + strings.Join(mapped.IssueIDs, ",") + "\x00" + mapped.Message
		if seenExact[exactKey] {
			continue
		}
		seenExact[exactKey] = true
		if da.IssueID != "" {
			seenTypeIssue[alertType+"\x00"+da.IssueID] = true
		}
		for _, id := range mapped.IssueIDs {
			if id != "" {
				seenTypeIssue[alertType+"\x00"+id] = true
			}
		}
		hasType[alertType] = true
		triage.Alerts = append(triage.Alerts, mapped)
	}
	return driftResult
}
