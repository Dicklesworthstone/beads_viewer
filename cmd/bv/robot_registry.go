package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

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
	Issues   []model.Issue
	DataHash string
	// DataHashMatchesIssues is true when DataHash is the ComputeDataHash of the
	// exact Issues slice carried here (i.e. no label-scope or recipe filtering
	// changed Issues after DataHash was computed). When true, handlers may seed
	// analyzers with DataHash to avoid recomputing the identical hash.
	DataHashMatchesIssues bool
	Encoder               robotEncoder
	AsOf                  string
	AsOfCommit            string
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
	EnvRobot             bool
	SearchOutput         *robotSearchOutput
	Diff                 *analysis.SnapshotDiff
	DiffHistoricalIssues []model.Issue
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
	RobotSuggestFlag    *bool
	RobotBurndownFlag   *string
	RobotForecastFlag   *string
	RobotSprintListFlag *bool
	RobotGraphFlag      *bool
	RobotSearchFlag     *bool
	RobotDiffFlag       *bool
	ForceFullAnalysis   *bool
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
	RobotCorrStatsFlag      *bool
	CorrelationFeedbackBy   *string
	CorrelationReason       *string
	RobotOrphansFlag        *bool
	OrphansMinScore         *int
	RobotFileBeadsFlag      *string
	FileBeadsLimit          *int
	RobotFileHotspotsFlag   *bool
	HotspotsLimit           *int
	RobotImpactFlag         *string
	ForceFullAnalysis       *bool
	HistoryLimit            *int
	HistoryTimeoutMs        *int // #166: budget for the triage history prologue (-1 = unset, 0 = unbounded)
	HistorySince            *string
	MinConfidence           *float64
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

// Analyzer uses the visible graph for metrics and the retained full source for
// readiness. Every registry handler must keep these two scopes distinct.
func (ctx RobotContext) Analyzer() *analysis.Analyzer {
	analyzer := analysis.NewAnalyzer(ctx.Issues)
	analyzer.SetReadinessScope(ctx.Readiness, ctx.CandidateIDs)
	analyzer.SetNow(robotNow())
	return analyzer
}

// Envelope builds the shared robot payload header for this dispatch: data
// hash, source, time-travel metadata, and active scoping.
func (ctx RobotContext) Envelope() RobotEnvelope {
	return ctx.EnvelopeWithHash(ctx.DataHash)
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
	if ctx.AsOf == "" {
		return nil
	}
	switch normalizeRobotFlagName(command) {
	case "robot-history", "robot-orphans", "robot-file-beads", "robot-file-hotspots",
		"robot-file-relations", "robot-impact-network", "robot-related", "robot-causality",
		"robot-explain-correlation", "robot-confirm-correlation", "robot-reject-correlation",
		"robot-correlation-stats", "robot-impact",
		"robot-sprint-list", "robot-sprint-show", "robot-burndown":
		return []string{"as_of"}
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
			analyzer := ctx.Analyzer()
			analyzer.SetNow(robotNow())
			if ctx.DataHashMatchesIssues {
				analyzer.SeedDataHash(ctx.DataHash)
			}
			config := analysis.ConfigForSize(len(ctx.Issues), countEdges(ctx.Issues))
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
			analyzer := ctx.Analyzer()
			analyzer.SetNow(robotNow())
			if _, w := loadRobotFeedback(); w != nil {
				analyzer.SetWeights(*w)
			}
			if ctx.DataHashMatchesIssues {
				analyzer.SeedDataHash(ctx.DataHash)
			}
			config := analysis.ConfigForSize(len(ctx.Issues), countEdges(ctx.Issues))
			if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
				config = analysis.FullAnalysisConfig()
			}
			analyzer.SetConfig(&config)
			stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
			stats.WaitForPhase2()

			recommendations := analyzer.GenerateEnhancedRecommendations()
			filtered := make([]analysis.EnhancedPriorityRecommendation, 0, len(recommendations))
			issueMap := make(map[string]model.Issue, len(ctx.Issues))
			for _, issue := range ctx.Issues {
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
			output.Summary.TotalIssues = len(ctx.Issues)
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
			analyzer.SetNow(robotNow())
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

			analyzer := ctx.Analyzer()
			analyzer.SetNow(robotNow())
			stats := analyzer.Analyze()

			openCount, closedCount, blockedCount := 0, 0, 0
			for _, issue := range ctx.Issues {
				switch issue.Status {
				case model.StatusClosed:
					closedCount++
				case model.StatusBlocked:
					blockedCount++
				case model.StatusOpen, model.StatusInProgress:
					openCount++
				}
			}
			actionableCount := len(analyzer.GetActionableIssues())
			cycles := stats.Cycles()
			curStats := baseline.GraphStats{
				NodeCount:       stats.NodeCount,
				EdgeCount:       stats.EdgeCount,
				Density:         stats.Density,
				OpenCount:       openCount,
				ClosedCount:     closedCount,
				BlockedCount:    blockedCount,
				CycleCount:      len(cycles),
				ActionableCount: actionableCount,
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
						PageRank:     buildMetricItems(stats.PageRank(), 10),
						Betweenness:  buildMetricItems(stats.Betweenness(), 10),
						CriticalPath: buildMetricItems(stats.CriticalPathScore(), 10),
						Hubs:         buildMetricItems(stats.Hubs(), 10),
						Authorities:  buildMetricItems(stats.Authorities(), 10),
					}
					cur = &baseline.Baseline{Stats: curStats, TopMetrics: topMetrics, Cycles: cycles}
				}
			}

			calc := drift.NewCalculator(bl, cur, driftConfig)
			calc.SetNow(robotNow())
			calc.SetIssues(ctx.Issues)
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
					"--alert-type=high_impact_unblock|abandoned_claim|potential_duplicate|priority_mismatch|velocity_drop   # proactive checks (no baseline needed)",
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
		Name:        "robot-suggest",
		FlagName:    "robot-suggest",
		FlagPtr:     cfg.RobotSuggestFlag,
		Description: "Output smart suggestions",
		Handler: func(ctx RobotContext) error {
			config := analysis.DefaultSuggestAllConfig()
			if cfg.SuggestConfidence != nil {
				config.MinConfidence = *cfg.SuggestConfidence
			}
			if cfg.SuggestBead != nil {
				config.FilterBead = *cfg.SuggestBead
			}

			suggestType := ""
			if cfg.SuggestType != nil {
				suggestType = strings.TrimSpace(*cfg.SuggestType)
			}
			switch suggestType {
			case "duplicate", "duplicates":
				config.FilterType = analysis.SuggestionPotentialDuplicate
			case "dependency", "dependencies":
				config.FilterType = analysis.SuggestionMissingDependency
			case "label", "labels":
				config.FilterType = analysis.SuggestionLabelSuggestion
			case "cycle", "cycles":
				config.FilterType = analysis.SuggestionCycleWarning
			case "":
			default:
				fmt.Fprintf(ctx.StderrOrDefault(), "Invalid suggest-type: %s (use: duplicate, dependency, label, cycle)\n", suggestType)
				return newReportedRobotHandlerExit(1)
			}

			suggest := analysis.GenerateRobotSuggestOutputAt(ctx.Issues, config, ctx.DataHash, robotNow())
			if !ctx.claimsProven() || ctx.AsOf != "" {
				reason := "source authority is incomplete or unknown"
				if ctx.AsOf != "" {
					reason = "historical snapshots cannot authorize tracker mutations"
				}
				for i, suggestion := range suggest.Set.Suggestions {
					suggest.Set.Suggestions[i] = suggestion.WithAction(nil).WithMetadata("action_unavailable_reason", reason)
				}
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
			sprints, err := loader.LoadSprints(workDir)
			if err != nil {
				return fmt.Errorf("loading sprints: %w", err)
			}

			output := struct {
				RobotEnvelope
				SprintCount int            `json:"sprint_count"`
				Sprints     []model.Sprint `json:"sprints"`
			}{
				RobotEnvelope: ctx.EnvelopeWithHash(analysis.ComputeDataHash(ctx.Issues)),
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
			sprints, err := loader.LoadSprints(workDir)
			if err != nil {
				return fmt.Errorf("loading sprints: %w", err)
			}

			target := ""
			if cfg.RobotBurndownFlag != nil {
				target = *cfg.RobotBurndownFlag
			}

			var targetSprint *model.Sprint
			if target == "current" {
				for i := range sprints {
					if sprints[i].IsActive() {
						targetSprint = &sprints[i]
						break
					}
				}
				if targetSprint == nil {
					fmt.Fprintln(ctx.StderrOrDefault(), "No active sprint found")
					return newReportedRobotHandlerExit(1)
				}
			} else {
				for i := range sprints {
					if sprints[i].ID == target {
						targetSprint = &sprints[i]
						break
					}
				}
				if targetSprint == nil {
					fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", target)
					return newReportedRobotHandlerExit(1)
				}
			}

			now := robotNow()
			burndown := calculateBurndownAt(targetSprint, ctx.Issues, now)
			burndown.RobotEnvelope = ctx.EnvelopeWithHash(analysis.ComputeDataHash(ctx.Issues))
			issueMap := make(map[string]model.Issue, len(ctx.Issues))
			for _, issue := range ctx.Issues {
				issueMap[issue.ID] = issue
			}
			if scopeChanges, err := computeSprintScopeChanges(workDir, targetSprint, issueMap, now); err == nil && len(scopeChanges) > 0 {
				burndown.ScopeChanges = scopeChanges
				// Re-linearize the ideal trajectory at each scope change.
				burndown.IdealLine = generateIdealLineScoped(targetSprint, burndown.TotalIssues, scopeChanges)
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

			analyzer := ctx.Analyzer()
			analyzer.SetNow(robotNow())
			graphStats := analyzer.Analyze()

			targetIssues := make([]model.Issue, 0, len(ctx.Issues))
			var sprintBeadIDs map[string]bool
			if cfg.ForecastSprint != nil && strings.TrimSpace(*cfg.ForecastSprint) != "" {
				sprints, err := loader.LoadSprints(workDir)
				if err == nil {
					for _, sprint := range sprints {
						if sprint.ID == *cfg.ForecastSprint {
							sprintBeadIDs = make(map[string]bool)
							for _, beadID := range sprint.BeadIDs {
								sprintBeadIDs[beadID] = true
							}
							break
						}
					}
				}
				if sprintBeadIDs == nil {
					fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", *cfg.ForecastSprint)
					return newReportedRobotHandlerExit(1)
				}
			}

			for _, issue := range ctx.Issues {
				if cfg.ForecastLabel != nil && strings.TrimSpace(*cfg.ForecastLabel) != "" {
					hasLabel := false
					for _, label := range issue.Labels {
						if label == *cfg.ForecastLabel {
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

			now := robotNow()
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

			forecasts := make([]analysis.ETAEstimate, 0)
			if forecastTarget == "all" {
				for _, issue := range targetIssues {
					if issue.Status == model.StatusClosed {
						continue
					}
					eta, err := analysis.EstimateETAForIssue(ctx.Issues, &graphStats, issue.ID, agents, now)
					if err != nil {
						continue
					}
					forecasts = append(forecasts, eta)
				}
			} else {
				eta, err := analysis.EstimateETAForIssue(ctx.Issues, &graphStats, forecastTarget, agents, now)
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
				RobotEnvelope: ctx.EnvelopeWithHash(analysis.ComputeDataHash(ctx.Issues)),
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
			diff.ToTimestamp = robotNow()
			output := struct {
				RobotEnvelope
				ResolvedRevision string                 `json:"resolved_revision"`
				FromDataHash     string                 `json:"from_data_hash"`
				ToDataHash       string                 `json:"to_data_hash"`
				Diff             *analysis.SnapshotDiff `json:"diff"`
			}{
				RobotEnvelope:    ctx.Envelope(),
				ResolvedRevision: ctx.DiffResolvedRevision,
				FromDataHash:     analysis.ComputeDataHash(ctx.DiffHistoricalIssues),
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
		return handleRobotCorrelationFeedback(ctx, cfg, false)
	})
	register("robot-reject-correlation", cfg.RobotRejectCorrFlag, "Reject an incorrect correlation", func(ctx RobotContext) error {
		return handleRobotCorrelationFeedback(ctx, cfg, true)
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

func handleRobotLabelHealth(ctx RobotContext) error {
	cfg := analysis.DefaultLabelHealthConfig()
	results := analysis.ComputeAllLabelHealth(ctx.Issues, cfg, robotNow(), nil)

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
	cfg := analysis.DefaultLabelHealthConfig()
	flow := analysis.ComputeCrossLabelFlow(ctx.Issues, cfg)
	output := struct {
		RobotEnvelope
		Flow       analysis.CrossLabelFlow    `json:"flow"`
		Config     analysis.LabelHealthConfig `json:"analysis_config"`
		UsageHints []string                   `json:"usage_hints"`
	}{
		RobotEnvelope: ctx.Envelope(),
		Flow:          flow,
		Config:        cfg,
		UsageHints: []string{
			"jq '.flow.bottleneck_labels' - labels blocking the most others",
			"jq '.flow.dependencies[] | select(.issue_count > 0) | {from:.from_label,to:.to_label,count:.issue_count}'",
			"jq '.flow.flow_matrix' - raw matrix (row=from, col=to, align with .flow.labels)",
		},
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding label flow: %w", err)
	}
	return nil
}

func handleRobotLabelAttention(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	result := analysis.ComputeLabelAttentionScores(ctx.Issues, analysis.DefaultLabelHealthConfig(), robotNow())

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

func handleRobotInsights(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	analyzer := ctx.Analyzer()
	analyzer.SetNow(robotNow())
	if ctx.DataHashMatchesIssues {
		analyzer.SeedDataHash(ctx.DataHash)
	}
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullConfig := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullConfig)
	}
	stats := analyzer.Analyze()
	insights := stats.GenerateInsights(50)

	if velocity := analysis.ComputeProjectVelocity(ctx.Issues, robotNow(), 8); velocity != nil {
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
		PageRank:          limitMaps(stats.PageRank(), mapLimit),
		Betweenness:       limitMaps(stats.Betweenness(), mapLimit),
		Eigenvector:       limitMaps(stats.Eigenvector(), mapLimit),
		Hubs:              limitMaps(stats.Hubs(), mapLimit),
		Authorities:       limitMaps(stats.Authorities(), mapLimit),
		CriticalPathScore: limitMaps(stats.CriticalPathScore(), mapLimit),
		CoreNumber:        limitMapInt(stats.CoreNumber(), mapLimit),
		Slack:             limitMaps(stats.Slack(), mapLimit),
		Articulation:      limitSlice(stats.ArticulationPoints(), mapLimit),
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
		TopWhatIfs:       analyzer.TopWhatIfDeltasFromStats(&stats, 10),
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
func generateTriageHistoryBounded(workDir, beadsPath string, beadInfos []correlation.BeadInfo, limit int, timeout time.Duration) (*correlation.HistoryReport, string) {
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
		report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{Limit: limit})
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

func handleRobotTriage(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	var historyReport *correlation.HistoryReport
	historyStatus := "" // empty = history generation not attempted (#166)
	hasOpenIssues := false
	for _, issue := range ctx.Issues {
		if issue.Status != model.StatusClosed && issue.Status != model.StatusTombstone {
			hasOpenIssues = true
			break
		}
	}

	if hasOpenIssues && sourceDateEpochActive() {
		historyStatus = "skipped"
	} else if hasOpenIssues {
		workDir, err := ctx.WorkDirOrDefault()
		if err == nil {
			if beadsDir, err := loader.GetBeadsDir(""); err == nil {
				if beadsPath, err := loader.FindJSONLPath(beadsDir); err == nil {
					limit := 500
					if cfg.HistoryLimit != nil {
						limit = *cfg.HistoryLimit
					}
					if limit == 500 {
						limit = 200
					}
					if correlation.ValidateRepository(workDir) == nil {
						beadInfos := make([]correlation.BeadInfo, len(ctx.Issues))
						for i, issue := range ctx.Issues {
							beadInfos[i] = correlation.BeadInfo{
								ID:     issue.ID,
								Title:  issue.Title,
								Status: string(issue.Status),
							}
						}
						historyReport, historyStatus = generateTriageHistoryBounded(
							workDir, beadsPath, beadInfos, limit, resolveRobotHistoryTimeout(cfg))
					}
				}
			}
		}
	}

	// bv-140: scope triage to a subgraph if --graph-root is specified
	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	now := robotNow()
	seedHash := ""
	if ctx.DataHashMatchesIssues {
		seedHash = ctx.DataHash
	}
	// bv-90: accept/ignore feedback tunes the factor weights once enough
	// samples exist; the payload's feedback block reports whether it applied.
	feedbackData, feedbackWeights := loadRobotFeedback()
	triage := analysis.ComputeTriageWithOptionsAndTime(ctx.Issues, analysis.TriageOptions{
		GroupByTrack:   cfg.RobotTriageByTrackFlag != nil && *cfg.RobotTriageByTrackFlag,
		Readiness:      ctx.Readiness,
		CandidateIDs:   ctx.CandidateIDs,
		GroupByLabel:   cfg.RobotTriageByLabelFlag != nil && *cfg.RobotTriageByLabelFlag,
		WaitForPhase2:  true,
		UseFastConfig:  true,
		History:        historyReport,
		RootIssueID:    rootIssueID,
		SeedDataHash:   seedHash,
		NotReadyLabels: resolveNotReadyLabels(cfg),
		Weights:        feedbackWeights,
	}, now)
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

// briefTriageOutput is the compact --robot-triage --brief payload (#183).
// It keeps quick_ref (counts + top picks — the claimability signal),
// quick_wins, and blockers_to_clear (already lean), and reduces each
// recommendation to briefTriageRecommendation. Score breakdowns, project
// health, commands, feedback, and usage hints are omitted.
type briefTriageOutput struct {
	RobotEnvelope
	Brief           bool                        `json:"brief"`
	QuickRef        analysis.QuickRef           `json:"quick_ref"`
	Recommendations []briefTriageRecommendation `json:"recommendations"`
	QuickWins       []analysis.QuickWin         `json:"quick_wins,omitempty"`
	BlockersToClear []analysis.BlockerItem      `json:"blockers_to_clear,omitempty"`
}

func encodeBriefTriage(ctx RobotContext, triage analysis.TriageResult, now time.Time) error {
	recs := make([]briefTriageRecommendation, 0, len(triage.Recommendations))
	for _, rec := range triage.Recommendations {
		recs = append(recs, briefTriageRecommendation{
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
	output := briefTriageOutput{
		RobotEnvelope:   ctx.Envelope(),
		Brief:           true,
		QuickRef:        triage.QuickRef,
		Recommendations: recs,
		QuickWins:       triage.QuickWins,
		BlockersToClear: triage.BlockersToClear,
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
	beadsDir, err := loader.GetBeadsDir("")
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
	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	now := robotNow()
	_, feedbackWeights := loadRobotFeedback()
	triage := analysis.ComputeTriageWithOptionsAndTime(ctx.Issues, analysis.TriageOptions{
		WaitForPhase2:  true,
		Readiness:      ctx.Readiness,
		CandidateIDs:   ctx.CandidateIDs,
		UseFastConfig:  true,
		RootIssueID:    rootIssueID,
		NotReadyLabels: resolveNotReadyLabels(cfg),
		Weights:        feedbackWeights,
	}, now)
	stabilizeRobotTriageForPinnedClock(&triage)

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
		if len(triage.QuickRef.TopPicks) > 0 {
			diagnostic := robotNextDiagnosticFromPick(triage.QuickRef.TopPicks[0])
			output.DiagnosticTopPick = &diagnostic
		}
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
		output.Degraded = []robotNextDegradation{{
			Code:     "no_actionable_recommendation",
			Severity: "info",
			Message:  "No open, unblocked, unassigned non-epic recommendation passed the robot-next claimability filter.",
			Repair:   "Use br ready --json or scripts/br_retry.sh actionable --json for authoritative claim candidates.",
		}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	top, diagnostic, unsafePickReasons, ok := robotNextClaimablePick(triage.QuickRef.TopPicks, ctx.Issues, ctx.Readiness, now)
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

	issue := robotNextIssueIndex(ctx.Issues)[top.ID]
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
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return fmt.Errorf("finding beads file: %w", err)
	}

	opts := correlation.CorrelatorOptions{Limit: 500}
	if cfg.BeadHistoryFlag != nil {
		opts.BeadID = *cfg.BeadHistoryFlag
	}
	if cfg.HistoryLimit != nil {
		opts.Limit = *cfg.HistoryLimit
	}
	if cfg.HistorySince != nil && strings.TrimSpace(*cfg.HistorySince) != "" {
		since, err := recipe.ParseRelativeTime(*cfg.HistorySince, robotNow())
		if err != nil {
			return fmt.Errorf("parsing --history-since: %w", err)
		}
		if !since.IsZero() {
			opts.Since = &since
		}
	}

	beadInfos := make([]correlation.BeadInfo, len(ctx.Issues))
	for i, issue := range ctx.Issues {
		beadInfos[i] = correlation.BeadInfo{
			ID:     issue.ID,
			Title:  issue.Title,
			Status: string(issue.Status),
		}
	}

	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return err
	}
	report, err := correlator.GenerateReportCached(beadInfos, opts)
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.GeneratedAt = robotNow()

	if cfg.MinConfidence != nil && *cfg.MinConfidence > 0 {
		scorer := correlation.NewScorer()
		report.Histories = scorer.FilterHistoriesByConfidence(report.Histories, *cfg.MinConfidence)
		report.CommitIndex = correlation.BuildCommitIndex(report.Histories)
		report.Stats.BeadsWithCommits = 0
		for _, history := range report.Histories {
			if len(history.Commits) > 0 {
				report.Stats.BeadsWithCommits++
			}
		}
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

func resolveCorrelationBeadsPath(workDir string) (string, string, error) {
	beadsDir, err := loader.GetBeadsDir(workDir)
	if err != nil {
		return "", "", fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return "", "", fmt.Errorf("finding beads file: %w", err)
	}
	return beadsDir, beadsPath, nil
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

// generateRawCorrelationReport builds the history report WITHOUT applying the
// feedback store. Only the confirm/reject handler uses it: feedback is a
// decision about a raw correlation, so the target must still be resolvable
// after an earlier rejection (letting a rejection be flipped) and orig_conf
// must record the strategy confidence, not a previously pinned 1.0.
func generateRawCorrelationReport(workDir string, issues []model.Issue, opts correlation.CorrelatorOptions) (*correlation.HistoryReport, error) {
	_, beadsPath, err := resolveCorrelationBeadsPath(workDir)
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
	feedbackStore, err := loadCorrelationFeedbackStore(workDir)
	if err != nil {
		return nil, err
	}
	return correlation.NewCorrelator(workDir, beadsPath).WithFeedbackStore(feedbackStore), nil
}

func loadCorrelationFeedbackStore(workDir string) (*correlation.FeedbackStore, error) {
	beadsDir, err := loader.GetBeadsDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("getting beads directory: %w", err)
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

	feedbackStore, err := loadCorrelationFeedbackStore(workDir)
	if err != nil {
		return err
	}

	output := struct {
		correlation.FeedbackStats
		GeneratedAt  string `json:"generated_at"`
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
	}{
		FeedbackStats: feedbackStore.GetStats(),
		GeneratedAt:   robotNow().Format(time.RFC3339),
		OutputFormat:  robotOutputFormat,
		Version:       version.Version,
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

	feedbackStore, err := loadCorrelationFeedbackStore(workDir)
	if err != nil {
		return err
	}

	// Explain the raw strategy score: a rejected pair is removed from the
	// feedback-applied report, but the user asking "why was this correlated?"
	// still needs the signals plus the stored decision.
	report, err := generateRawCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{BeadID: beadID})
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
	if fb, ok := feedbackStore.Get(targetCommit.SHA, beadID); ok {
		explanation.Feedback = &fb
		explanation.Recommendation = describeCorrelationFeedback(fb)
	}
	if err := ctx.EncoderOrDefault().Encode(explanation); err != nil {
		return fmt.Errorf("encoding explanation: %w", err)
	}
	return nil
}

func handleRobotCorrelationFeedback(ctx RobotContext, cfg phaseThreeRobotHandlerConfig, reject bool) error {
	flagPtr := cfg.RobotConfirmCorrFlag
	status := "confirmed"
	if reject {
		flagPtr = cfg.RobotRejectCorrFlag
		status = "rejected"
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

	feedbackStore, err := loadCorrelationFeedbackStore(workDir)
	if err != nil {
		return err
	}

	report, err := generateRawCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{BeadID: beadID})
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

	if reject {
		if err := feedbackStore.Reject(commitSHA, beadID, feedbackBy, originalConf, reason); err != nil {
			return fmt.Errorf("saving feedback: %w", err)
		}
	} else {
		if err := feedbackStore.Confirm(commitSHA, beadID, feedbackBy, originalConf, reason); err != nil {
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
	if err := ctx.EncoderOrDefault().Encode(result); err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}
	return nil
}

func handleRobotFileRelations(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	// Use the dispatch context's issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping. Reloading the working tree here
	// silently bypassed all four (reality check 2026-09-01, gap 2).
	issues := ctx.Issues
	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return fmt.Errorf("finding beads file: %w", err)
	}

	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return err
	}
	report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}

	threshold := 0.0
	if cfg.RelationsThreshold != nil {
		threshold = *cfg.RelationsThreshold
	}
	maxResults := 10
	if cfg.RelationsLimit != nil {
		maxResults = *cfg.RelationsLimit
	}
	result := correlation.NewFileLookup(report).GetRelatedFiles(*cfg.RobotFileRelationsFlag, threshold, maxResults)

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

func handleRobotOrphans(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}

	orphanReport, err := correlation.NewOrphanDetectorAt(report, workDir, robotNow()).DetectOrphans(correlation.ExtractOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("detecting orphans: %w", err)
	}

	minScore := 30
	if cfg.OrphansMinScore != nil {
		minScore = *cfg.OrphansMinScore
	}
	filterOrphanReportByMinScore(orphanReport, minScore)

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
	filtered := make([]correlation.OrphanCandidate, 0, len(orphanReport.Candidates))
	byBead := make(map[string][]string)
	totalSuspicion := 0

	for _, candidate := range orphanReport.Candidates {
		if candidate.SuspicionScore < minScore {
			continue
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

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}

	fileLookup := correlation.NewFileLookup(report)
	result := fileLookup.LookupByFile(*cfg.RobotFileBeadsFlag)
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
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}

	fileLookup := correlation.NewFileLookup(report)
	files := strings.Split(*cfg.RobotImpactFlag, ",")
	for i := range files {
		files[i] = strings.TrimSpace(files[i])
	}
	impactResult := fileLookup.ImpactAnalysisAt(files, robotNow())

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
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	// Use the dispatch context's issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping. Reloading the working tree here
	// silently bypassed all four (reality check 2026-09-01, gap 2).
	issues := ctx.Issues
	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return fmt.Errorf("finding beads file: %w", err)
	}

	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return err
	}
	report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}

	depGraph := make(map[string][]string)
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil {
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

	result := report.FindRelatedWorkAt(*cfg.RobotRelatedFlag, options, robotNow())
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
	// Use the dispatch context's issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping. Reloading the working tree here
	// silently bypassed all four (reality check 2026-09-01, gap 2).
	issues := ctx.Issues

	analyzer := analysis.NewAnalyzer(issues)
	analyzer.SetNow(robotNow())
	result := analyzer.GetBlockerChain(*cfg.RobotBlockerChainFlag)
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", *cfg.RobotBlockerChainFlag)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		RobotEnvelope
		Result *analysis.BlockerChainResult `json:"result"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(analysis.ComputeDataHash(issues)),
		Result:        result,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding blocker chain: %w", err)
	}
	return nil
}

func handleRobotImpactNetwork(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return fmt.Errorf("finding beads file: %w", err)
	}
	// Use the dispatch context's issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping. Reloading the working tree here
	// silently bypassed all four (reality check 2026-09-01, gap 2).
	issues := ctx.Issues

	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return err
	}
	report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}

	network := correlation.NewNetworkBuilderWithIssues(report, issues).BuildAt(robotNow())
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
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	// Use the dispatch context's issue set: it already carries --as-of,
	// --label, --recipe, and --repo scoping. Reloading the working tree here
	// silently bypassed all four (reality check 2026-09-01, gap 2).
	issues := ctx.Issues
	beadsDir, err := loader.GetBeadsDir("")
	if err != nil {
		return fmt.Errorf("getting beads directory: %w", err)
	}
	beadsPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return fmt.Errorf("finding beads file: %w", err)
	}

	beadInfos := make([]correlation.BeadInfo, len(issues))
	for i, issue := range issues {
		beadInfos[i] = correlation.BeadInfo{ID: issue.ID, Title: issue.Title, Status: string(issue.Status)}
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	correlator, err := newCorrelatorWithFeedback(workDir, beadsPath)
	if err != nil {
		return err
	}
	report, err := correlator.GenerateReportCached(beadInfos, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}

	blockerTitles := make(map[string]string, len(issues))
	for _, issue := range issues {
		blockerTitles[issue.ID] = issue.Title
	}
	result := report.BuildCausalityChainAt(*cfg.RobotCausalityFlag, correlation.CausalityOptions{
		IncludeCommits: true,
		BlockerTitles:  blockerTitles,
	}, robotNow())
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
	sprints, err := loader.LoadSprints(workDir)
	if err != nil {
		return fmt.Errorf("loading sprints: %w", err)
	}

	var found *model.Sprint
	for i := range sprints {
		if sprints[i].ID == *cfg.RobotSprintShowFlag {
			found = &sprints[i]
			break
		}
	}
	if found == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Sprint not found: %s\n", *cfg.RobotSprintShowFlag)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		RobotEnvelope
		Sprint *model.Sprint `json:"sprint"`
	}{
		RobotEnvelope: ctx.EnvelopeWithHash(analysis.ComputeDataHash(ctx.Issues)),
		Sprint:        found,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding sprint: %w", err)
	}
	return nil
}

func handleRobotCapacity(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	analyzer := ctx.Analyzer()
	analyzer.SetNow(robotNow())
	graphStats := analyzer.Analyze()

	targetIssues := ctx.Issues
	if cfg.CapacityLabel != nil && strings.TrimSpace(*cfg.CapacityLabel) != "" {
		filtered := make([]model.Issue, 0)
		for _, issue := range ctx.Issues {
			for _, label := range issue.Labels {
				if label == *cfg.CapacityLabel {
					filtered = append(filtered, issue)
					break
				}
			}
		}
		targetIssues = filtered
	}

	openIssues := make([]model.Issue, 0)
	issueMap := make(map[string]model.Issue, len(targetIssues))
	for _, issue := range targetIssues {
		issueMap[issue.ID] = issue
		if issue.Status != model.StatusClosed {
			openIssues = append(openIssues, issue)
		}
	}

	now := robotNow()
	agents := 1
	if cfg.CapacityAgents != nil && *cfg.CapacityAgents > 0 {
		agents = *cfg.CapacityAgents
	}

	totalMinutes := 0
	for _, issue := range openIssues {
		eta, err := analysis.EstimateETAForIssue(targetIssues, &graphStats, issue.ID, 1, now)
		if err == nil {
			totalMinutes += eta.EstimatedMinutes
		}
	}

	blockedBy := make(map[string][]string)
	blocks := make(map[string][]string)
	for _, issue := range openIssues {
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if _, exists := issueMap[dep.DependsOnID]; exists {
				blockedBy[issue.ID] = append(blockedBy[issue.ID], dep.DependsOnID)
				blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], issue.ID)
			}
		}
	}

	actionable := make([]string, 0)
	for _, issue := range openIssues {
		hasOpenBlocker := false
		for _, depID := range blockedBy[issue.ID] {
			if dep, ok := issueMap[depID]; ok && dep.Status != model.StatusClosed {
				hasOpenBlocker = true
				break
			}
		}
		if !hasOpenBlocker {
			actionable = append(actionable, issue.ID)
		}
	}

	var longestChain []string
	visited := make(map[string]bool)
	var dfs func(string, []string)
	dfs = func(id string, path []string) {
		if visited[id] {
			return
		}
		visited[id] = true
		path = append(path, id)
		if len(path) > len(longestChain) {
			longestChain = append([]string(nil), path...)
		}
		for _, nextID := range blocks[id] {
			if dep, ok := issueMap[nextID]; ok && dep.Status != model.StatusClosed {
				dfs(nextID, path)
			}
		}
		visited[id] = false
	}
	for _, startID := range actionable {
		dfs(startID, nil)
	}

	serialMinutes := 0
	for _, id := range longestChain {
		eta, err := analysis.EstimateETAForIssue(targetIssues, &graphStats, id, 1, now)
		if err == nil {
			serialMinutes += eta.EstimatedMinutes
		}
	}

	parallelMinutes := totalMinutes - serialMinutes
	parallelizablePct := 0.0
	if totalMinutes > 0 {
		parallelizablePct = float64(parallelMinutes) / float64(totalMinutes) * 100
	}
	effectiveMinutes := serialMinutes + parallelMinutes/agents
	estimatedDays := float64(effectiveMinutes) / (60.0 * 8.0)

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
		return bottlenecks[i].BlocksCount > bottlenecks[j].BlocksCount
	})
	if len(bottlenecks) > 5 {
		bottlenecks = bottlenecks[:5]
	}

	output := struct {
		RobotEnvelope
		Agents            int          `json:"agents"`
		Label             string       `json:"label,omitempty"`
		OpenIssueCount    int          `json:"open_issue_count"`
		TotalMinutes      int          `json:"total_minutes"`
		TotalDays         float64      `json:"total_days"`
		SerialMinutes     int          `json:"serial_minutes"`
		ParallelMinutes   int          `json:"parallel_minutes"`
		ParallelizablePct float64      `json:"parallelizable_pct"`
		EstimatedDays     float64      `json:"estimated_days"`
		CriticalPathLen   int          `json:"critical_path_length"`
		CriticalPath      []string     `json:"critical_path,omitempty"`
		ActionableCount   int          `json:"actionable_count"`
		Actionable        []string     `json:"actionable,omitempty"`
		Bottlenecks       []bottleneck `json:"bottlenecks,omitempty"`
	}{
		RobotEnvelope:     ctx.EnvelopeWithHash(analysis.ComputeDataHash(ctx.Issues)),
		Agents:            agents,
		OpenIssueCount:    len(openIssues),
		TotalMinutes:      totalMinutes,
		TotalDays:         float64(totalMinutes) / (60.0 * 8.0),
		SerialMinutes:     serialMinutes,
		ParallelMinutes:   parallelMinutes,
		ParallelizablePct: parallelizablePct,
		EstimatedDays:     estimatedDays,
		CriticalPathLen:   len(longestChain),
		CriticalPath:      longestChain,
		ActionableCount:   len(actionable),
		Actionable:        actionable,
		Bottlenecks:       bottlenecks,
	}
	if cfg.CapacityLabel != nil && strings.TrimSpace(*cfg.CapacityLabel) != "" {
		output.Label = *cfg.CapacityLabel
	}

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding capacity: %w", err)
	}
	return nil
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
	if err := correlation.ValidateRepository(workDir); err != nil {
		return err
	}

	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
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
