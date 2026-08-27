package main

import (
	"context"
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
	Issues []model.Issue
	// AuthoritativeIssues is the full loaded issue universe before repo, label,
	// graph-root, or recipe filters narrow Issues for ranking. Claim-emitting
	// handlers must validate candidates against this slice so a hidden blocker
	// or open child cannot turn a planning container into apparently-ready work.
	// Direct handler callers may leave it nil to use Issues as the authority.
	AuthoritativeIssues []model.Issue
	// LoadStats carries the parse accounting for the authoritative issue load.
	// A non-zero Errors count means that universe is incomplete, so robot-next
	// must fail closed instead of claiming from partial evidence.
	LoadStats *RobotLoadStats
	// AuthorityIncompleteReasons records source-level gaps that are not captured
	// by per-record LoadStats. Workspace mode, for example, intentionally returns
	// the repositories that loaded successfully even when another configured
	// repository failed; robot-next must not treat that partial aggregate as an
	// authoritative claim universe.
	AuthorityIncompleteReasons []string
	// ClaimCommandUnavailableReasons records cases where analysis is valid but
	// bv cannot map a recommendation to a safe live `br` mutation target. This
	// includes historical snapshots and viewer-namespaced workspace issues.
	ClaimCommandUnavailableReasons []string
	// RepositoryRouteUnavailableReasons records cases where the issue source
	// cannot be proven to belong to WorkDir. An explicit BEADS_DB/BEADS_DIR (or
	// --db) may point at another repository or at detached storage, so handlers
	// must not pair those issues with WorkDir Git history, baselines, feedback,
	// or sprint metadata merely because WorkDir is the process cwd.
	RepositoryRouteUnavailableReasons []string
	// WorkspaceMode is true when Issues is a multi-repository aggregate. Git
	// history handlers cannot safely pair that namespace with one WorkDir repo.
	WorkspaceMode bool
	DataHash      string
	// DataHashMatchesIssues is true when DataHash is the ComputeDataHash of the
	// exact Issues slice carried here (i.e. no label-scope or recipe filtering
	// changed Issues after DataHash was computed). When true, handlers may seed
	// analyzers with DataHash to avoid recomputing the identical hash.
	DataHashMatchesIssues bool
	Encoder               robotEncoder
	AsOf                  string
	AsOfCommit            string
	// AnalysisTime is the clock used for recency, deferral, staleness, and
	// forecasting semantics. Historical loads set it to the resolved commit's
	// committer time; live loads leave it zero and use robotNow().
	AnalysisTime         time.Time
	LabelScope           string
	LabelContext         *analysis.LabelHealth
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
	DiffFromLoadStats    *RobotLoadStats
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

func robotEnvelopeForContext(ctx RobotContext, dataHash string) RobotEnvelope {
	envelope := NewRobotEnvelope(dataHash)
	envelope.RobotSourceEvidence = robotSourceEvidenceForContext(ctx)
	return envelope
}

func robotSourceEvidenceForContext(ctx RobotContext) RobotSourceEvidence {
	return RobotSourceEvidence{
		LoadStats:                         ctx.LoadStats,
		AuthorityIncompleteReasons:        normalizedRobotAuthorityReasons(ctx.AuthorityIncompleteReasons),
		RepositoryRouteUnavailableReasons: normalizedRobotAuthorityReasons(ctx.RepositoryRouteUnavailableReasons),
		AsOf:                              ctx.AsOf,
		AsOfCommit:                        ctx.AsOfCommit,
		AnalysisTime:                      formatOptionalRobotTime(ctx.AnalysisTime),
	}
}

func formatOptionalRobotTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizedRobotAuthorityReasons(reasons []string) []string {
	normalized := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			normalized = append(normalized, reason)
		}
	}
	return normalized
}

func (ctx RobotContext) WorkDirOrDefault() (string, error) {
	if strings.TrimSpace(ctx.WorkDir) != "" {
		return ctx.WorkDir, nil
	}
	return os.Getwd()
}

func (ctx RobotContext) AnalysisNowOrDefault() time.Time {
	if !ctx.AnalysisTime.IsZero() {
		return ctx.AnalysisTime
	}
	return robotNow()
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
func writeRobotHelp(out io.Writer) error {
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

Core commands:
  --robot-triage    Unified triage output (recommended entry point)
  --robot-next      Single top recommendation
  --robot-plan      Dependency-respecting execution tracks
  --robot-insights  Graph metrics and structural analysis
  --robot-capabilities  Machine-readable command/contract manifest
  --robot-schema    JSON Schema definitions for robot outputs

`)
	if err != nil {
		return fmt.Errorf("writing robot help intro: %w", err)
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
				RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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
			analyzer := analysis.NewAnalyzer(ctx.Issues)
			analyzer.SetNow(ctx.AnalysisNowOrDefault())
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
				GeneratedAt string `json:"generated_at"`
				DataHash    string `json:"data_hash"`
				RobotSourceEvidence
				AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
				Status         analysis.MetricStatus   `json:"status"`
				LabelScope     string                  `json:"label_scope,omitempty"`
				LabelContext   *analysis.LabelHealth   `json:"label_context,omitempty"`
				Plan           analysis.ExecutionPlan  `json:"plan"`
				UsageHints     []string                `json:"usage_hints"`
			}{
				GeneratedAt:         robotNow().Format(time.RFC3339),
				DataHash:            ctx.DataHash,
				RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
				AnalysisConfig:      config,
				Status:              stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:          ctx.LabelScope,
				LabelContext:        ctx.LabelContext,
				Plan:                plan,
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
			analyzer := analysis.NewAnalyzer(ctx.Issues)
			analyzer.SetNow(ctx.AnalysisNowOrDefault())
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
				GeneratedAt string `json:"generated_at"`
				DataHash    string `json:"data_hash"`
				RobotSourceEvidence
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
				GeneratedAt:         robotNow().Format(time.RFC3339),
				DataHash:            ctx.DataHash,
				RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
				AnalysisConfig:      config,
				Status:              stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
				LabelScope:          ctx.LabelScope,
				LabelContext:        ctx.LabelContext,
				Recommendations:     recommendations,
				FieldDescriptions:   analysis.DefaultFieldDescriptions(),
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
			analyzer := analysis.NewAnalyzer(ctx.Issues)
			analyzer.SetNow(ctx.AnalysisNowOrDefault())
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
				Label:    ctx.LabelScope,
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
			output := struct {
				GeneratedAt  string `json:"generated_at"`
				DataHash     string `json:"data_hash"`
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
				RobotSourceEvidence
				Format         string                  `json:"format"`
				Graph          string                  `json:"graph,omitempty"`
				Nodes          int                     `json:"nodes"`
				Edges          int                     `json:"edges"`
				FiltersApplied map[string]string       `json:"filters_applied,omitempty"`
				Explanation    export.GraphExplanation `json:"explanation"`
				Adjacency      *export.AdjacencyGraph  `json:"adjacency,omitempty"`
				AnalysisConfig analysis.AnalysisConfig `json:"analysis_config"`
				Status         analysis.MetricStatus   `json:"status"`
			}{
				GeneratedAt:         robotNow().Format(time.RFC3339),
				DataHash:            ctx.DataHash,
				OutputFormat:        robotOutputFormat,
				Version:             version.Version,
				RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
				Format:              result.Format,
				Graph:               result.Graph,
				Nodes:               result.Nodes,
				Edges:               result.Edges,
				FiltersApplied:      result.FiltersApplied,
				Explanation:         result.Explanation,
				Adjacency:           result.Adjacency,
				AnalysisConfig:      stats.Config,
				Status:              stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
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
			if err := requireLiveSingleRepoSideDataContext(ctx, "--robot-alerts", "saved baseline and drift configuration"); err != nil {
				return err
			}
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

			analyzer := analysis.NewAnalyzer(ctx.Issues)
			analyzer.SetNow(ctx.AnalysisNowOrDefault())
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
					found := false
					for _, detail := range alert.Details {
						if strings.Contains(strings.ToLower(detail), strings.ToLower(*cfg.AlertLabel)) {
							found = true
							break
						}
					}
					if !found && alert.Label != "" && !strings.Contains(strings.ToLower(alert.Label), strings.ToLower(*cfg.AlertLabel)) {
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
				UsageHints []string `json:"usage_hints"`
			}{
				RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
				Alerts:        driftResult.Alerts,
				UsageHints: []string{
					"--severity=warning --alert-type=stale_issue   # stale warnings only",
					"--alert-type=blocking_cascade                 # high-unblock opportunities",
					"jq '.alerts | map(.issue_id)'                # list impacted issues",
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

			result := analysis.GenerateRobotSuggestOutputAt(ctx.Issues, config, ctx.DataHash, ctx.AnalysisNowOrDefault())
			authoritativeIssues := ctx.AuthoritativeIssues
			if authoritativeIssues == nil {
				authoritativeIssues = ctx.Issues
			}
			cycleUnsafeActions := 0
			for i := range result.Set.Suggestions {
				suggestion := &result.Set.Suggestions[i]
				if suggestion.Type != analysis.SuggestionMissingDependency || suggestion.ActionCommand == "" {
					continue
				}
				canAdd, cyclePath, warning := analysis.CheckDependencyAddition(
					authoritativeIssues,
					suggestion.TargetBead,
					suggestion.RelatedBead,
				)
				if canAdd {
					continue
				}
				suggestion.ActionCommand = ""
				if suggestion.Metadata == nil {
					suggestion.Metadata = make(map[string]interface{})
				}
				suggestion.Metadata["action_unavailable_reason"] = warning
				suggestion.Metadata["cycle_path"] = cyclePath
				cycleUnsafeActions++
			}
			unsafeReasons, _ := robotNextAuthorityUnsafeReasons(ctx.LoadStats, ctx.AuthorityIncompleteReasons)
			unsafeReasons = append(unsafeReasons, normalizedRobotAuthorityReasons(ctx.ClaimCommandUnavailableReasons)...)
			var degraded []robotNextDegradation
			if cycleUnsafeActions > 0 {
				degraded = append(degraded, robotNextDegradation{
					Code:     "robot_suggest_cycle_unsafe_actions_removed",
					Severity: "warning",
					Message:  fmt.Sprintf("Removed %d dependency action command(s) that would create a cycle in the authoritative issue graph.", cycleUnsafeActions),
					Repair:   "Review the reported cycle_path before changing dependencies.",
				})
			}
			if len(unsafeReasons) > 0 {
				for i := range result.Set.Suggestions {
					result.Set.Suggestions[i].ActionCommand = ""
				}
				filteredHints := result.UsageHints[:0]
				for _, hint := range result.UsageHints {
					if !strings.Contains(hint, "action_command") {
						filteredHints = append(filteredHints, hint)
					}
				}
				result.UsageHints = filteredHints
				degraded = append(degraded, robotNextDegradation{
					Code:     "robot_suggest_actions_unavailable",
					Severity: "warning",
					Message:  strings.Join(unsafeReasons, "; "),
					Repair:   "Restore a complete live single-repository issue source, then rerun bv before applying suggestions.",
				})
			}
			result.Set.Stats.ActionableCount = 0
			for i := range result.Set.Suggestions {
				if result.Set.Suggestions[i].ActionCommand != "" {
					result.Set.Stats.ActionableCount++
				}
			}
			output := struct {
				analysis.RobotSuggestOutput
				OutputFormat string `json:"output_format,omitempty"`
				Version      string `json:"version,omitempty"`
				RobotSourceEvidence
				Degraded []robotNextDegradation `json:"degraded,omitempty"`
			}{
				RobotSuggestOutput:  result,
				OutputFormat:        robotOutputFormat,
				Version:             version.Version,
				RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
				Degraded:            degraded,
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
			if err := requireLiveSingleRepoSideDataContext(ctx, "--robot-sprint-list", "sprint metadata"); err != nil {
				return err
			}
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
				RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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
			if err := requireLiveSingleRepoSideDataContext(ctx, "--robot-burndown", "sprint metadata and Git scope history"); err != nil {
				return err
			}
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
			burndown.RobotEnvelope = robotEnvelopeForContext(ctx, ctx.DataHash)
			issueMap := make(map[string]model.Issue, len(ctx.Issues))
			for _, issue := range ctx.Issues {
				issueMap[issue.ID] = issue
			}
			if scopeChanges, err := computeSprintScopeChanges(workDir, targetSprint, issueMap, now); err == nil && len(scopeChanges) > 0 {
				burndown.ScopeChanges = scopeChanges
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

			analyzer := analysis.NewAnalyzer(ctx.Issues)
			analyzer.SetNow(ctx.AnalysisNowOrDefault())
			graphStats := analyzer.Analyze()

			targetIssues := make([]model.Issue, 0, len(ctx.Issues))
			var sprintBeadIDs map[string]bool
			if cfg.ForecastSprint != nil && strings.TrimSpace(*cfg.ForecastSprint) != "" {
				if err := requireLiveSingleRepoSideDataContext(ctx, "--robot-forecast --forecast-sprint", "sprint metadata"); err != nil {
					return err
				}
				sprints, err := loader.LoadSprints(workDir)
				if err != nil {
					return fmt.Errorf("loading sprints: %w", err)
				}
				for _, sprint := range sprints {
					if sprint.ID == *cfg.ForecastSprint {
						sprintBeadIDs = make(map[string]bool)
						for _, beadID := range sprint.BeadIDs {
							sprintBeadIDs[beadID] = true
						}
						break
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

			now := ctx.AnalysisNowOrDefault()
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
				RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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
		Description: "Output semantic search results as JSON",
		Handler: func(ctx RobotContext) error {
			if ctx.SearchOutput == nil {
				return fmt.Errorf("robot search output not initialized")
			}
			output := *ctx.SearchOutput
			output.RobotSourceEvidence = robotSourceEvidenceForContext(ctx)
			if err := ctx.EncoderOrDefault().Encode(output); err != nil {
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
			return handleRobotDiffAt(ctx, robotNow())
		},
	})
}

func handleRobotDiffAt(ctx RobotContext, now time.Time) error {
	if ctx.Diff == nil {
		return fmt.Errorf("diff output not initialized")
	}
	// Snapshot endpoint timestamps are source evidence: the from side is the
	// resolved historical commit time and the to side is either the live
	// analysis clock or a second historical commit time. Preserve both exactly;
	// generated_at describes only when this response was encoded.
	diff := *ctx.Diff
	output := struct {
		GeneratedAt      string `json:"generated_at"`
		ResolvedRevision string `json:"resolved_revision"`
		RobotSourceEvidence
		FromDataHash  string                 `json:"from_data_hash"`
		ToDataHash    string                 `json:"to_data_hash"`
		FromLoadStats *RobotLoadStats        `json:"from_load_stats,omitempty"`
		ToLoadStats   *RobotLoadStats        `json:"to_load_stats,omitempty"`
		Degraded      []robotNextDegradation `json:"degraded,omitempty"`
		Diff          *analysis.SnapshotDiff `json:"diff"`
	}{
		GeneratedAt:         now.Format(time.RFC3339Nano),
		ResolvedRevision:    ctx.DiffResolvedRevision,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		FromDataHash:        analysis.ComputeDataHash(ctx.DiffHistoricalIssues),
		ToDataHash:          ctx.DataHash,
		FromLoadStats:       ctx.DiffFromLoadStats,
		ToLoadStats:         ctx.LoadStats,
		Degraded:            robotDiffAuthorityDegradations(ctx),
		Diff:                &diff,
	}

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding diff: %w", err)
	}
	return nil
}

func robotDiffAuthorityDegradations(ctx RobotContext) []robotNextDegradation {
	degraded := make([]robotNextDegradation, 0, 3)
	if reasons := robotNextLoadUnsafeReasons(ctx.DiffFromLoadStats); len(reasons) > 0 {
		degraded = append(degraded, robotNextDegradation{
			Code:     "robot_diff_from_load_incomplete",
			Severity: "warning",
			Message:  strings.Join(reasons, "; "),
			Repair:   "Repair the historical source records and rerun the diff before treating changes as complete.",
		})
	}
	if reasons := robotNextLoadUnsafeReasons(ctx.LoadStats); len(reasons) > 0 {
		degraded = append(degraded, robotNextDegradation{
			Code:     "robot_diff_to_load_incomplete",
			Severity: "warning",
			Message:  strings.Join(reasons, "; "),
			Repair:   "Repair the current source records and rerun the diff before treating changes as complete.",
		})
	}
	for _, reason := range ctx.AuthorityIncompleteReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			degraded = append(degraded, robotNextDegradation{
				Code:     "robot_diff_to_authority_incomplete",
				Severity: "warning",
				Message:  reason,
				Repair:   "Repair every current workspace authority gap and rerun the diff before treating changes as complete.",
			})
		}
	}
	return degraded
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
	register("robot-file-hotspots", cfg.RobotFileHotspotsFlag, "Output files touched by the most beads as JSON", func(ctx RobotContext) error {
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
	results := analysis.ComputeAllLabelHealth(ctx.Issues, cfg, ctx.AnalysisNowOrDefault(), nil)

	output := struct {
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		RobotSourceEvidence
		AnalysisConfig analysis.LabelHealthConfig   `json:"analysis_config"`
		Results        analysis.LabelAnalysisResult `json:"results"`
		UsageHints     []string                     `json:"usage_hints"`
	}{
		GeneratedAt:         robotNow().Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		AnalysisConfig:      cfg,
		Results:             results,
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
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		RobotSourceEvidence
		Flow       analysis.CrossLabelFlow    `json:"flow"`
		Config     analysis.LabelHealthConfig `json:"analysis_config"`
		UsageHints []string                   `json:"usage_hints"`
	}{
		GeneratedAt:         robotNow().Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		Flow:                flow,
		Config:              cfg,
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
	result := analysis.ComputeLabelAttentionScores(ctx.Issues, analysis.DefaultLabelHealthConfig(), ctx.AnalysisNowOrDefault())

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
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		RobotSourceEvidence
		Limit       int              `json:"limit"`
		TotalLabels int              `json:"total_labels"`
		Labels      []attentionLabel `json:"labels"`
		UsageHints  []string         `json:"usage_hints"`
	}

	output := attentionOutput{
		GeneratedAt:         robotNow().Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		Limit:               limit,
		TotalLabels:         result.TotalLabels,
		Labels:              []attentionLabel{},
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
	analyzer := analysis.NewAnalyzer(ctx.Issues)
	analyzer.SetNow(ctx.AnalysisNowOrDefault())
	if ctx.DataHashMatchesIssues {
		analyzer.SeedDataHash(ctx.DataHash)
	}
	if cfg.ForceFullAnalysis != nil && *cfg.ForceFullAnalysis {
		fullConfig := analysis.FullAnalysisConfig()
		analyzer.SetConfig(&fullConfig)
	}
	stats := analyzer.Analyze()
	insights := stats.GenerateInsights(50)

	if velocity := analysis.ComputeProjectVelocity(ctx.Issues, ctx.AnalysisNowOrDefault(), 8); velocity != nil {
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
	if value := os.Getenv("BV_INSIGHTS_MAP_LIMIT"); value != "" {
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
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		RobotSourceEvidence
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
		GeneratedAt:         robotNow().Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		AnalysisConfig:      stats.Config,
		Status:              stabilizeRobotMetricStatusForPinnedClock(stats.Status()),
		LabelScope:          ctx.LabelScope,
		LabelContext:        ctx.LabelContext,
		Insights:            insights,
		FullStats:           fullStats,
		TopWhatIfs:          analyzer.TopWhatIfDeltasFromStats(&stats, 10),
		AdvancedInsights:    analyzer.GenerateAdvancedInsightsFromStats(&stats, analysis.DefaultAdvancedInsightsConfig()),
		UsageHints: []string{
			"jq '.Bottlenecks[:5] | map(.ID)' - Top 5 bottleneck IDs",
			"jq '.Keystones[:3]' - Top 3 critical path items",
			"jq '.top_what_ifs[] | select(.delta.direct_unblocks > 2)' - High-impact items",
			"jq '.full_stats.pagerank | to_entries | sort_by(-.value)[:5]' - Top PageRank",
			"jq '.full_stats.core_number | to_entries | sort_by(-.value)[:5]' - Strongly embedded nodes (k-core)",
			"jq '.full_stats.articulation_points' - Structural cut points",
			"jq '.Slack[:5]' - Nodes with slack (good parallel work candidates)",
			"jq '.Cycles | length' - Count of stored cycle representatives",
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
// --robot-triage (issue #166). The prologue is best-effort
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
	if env := strings.TrimSpace(os.Getenv("BV_ROBOT_HISTORY_TIMEOUT_MS")); env != "" {
		if ms, err := strconv.ParseInt(env, 10, 64); err == nil {
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
	} else if env := strings.TrimSpace(os.Getenv("BV_ROBOT_NOT_READY_LABELS")); env != "" {
		raw = env
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

func triageHistoryResultStatus(ctxErr, reportErr error) string {
	if ctxErr != nil {
		return "timeout"
	}
	if reportErr != nil {
		return "error"
	}
	return "ok"
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

	correlator := correlation.NewCorrelator(workDir, beadsPath).WithContext(histCtx)

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
		status := triageHistoryResultStatus(histCtx.Err(), res.err)
		if status != "ok" {
			return nil, status
		}
		return res.report, status
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

	if hasOpenIssues && (sourceDateEpochActive() || strings.TrimSpace(ctx.AsOf) != "" || ctx.WorkspaceMode || len(normalizedRobotAuthorityReasons(ctx.RepositoryRouteUnavailableReasons)) > 0) {
		historyStatus = "skipped"
	} else if hasOpenIssues {
		historyStatus = "error"
		workDir, err := ctx.WorkDirOrDefault()
		if err == nil {
			if _, beadsPath, err := resolveCorrelationBeadsPath(workDir); err == nil {
				limit := 500
				if cfg.HistoryLimit != nil {
					limit = *cfg.HistoryLimit
				}
				if limit == 500 {
					limit = 200
				}
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

	// bv-140: scope triage to a subgraph if --graph-root is specified
	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	analysisNow := ctx.AnalysisNowOrDefault()
	generatedAt := robotNow()
	seedHash := ""
	if ctx.DataHashMatchesIssues {
		seedHash = ctx.DataHash
	}
	triage := analysis.ComputeTriageWithOptionsAndTime(ctx.Issues, analysis.TriageOptions{
		GroupByTrack:   cfg.RobotTriageByTrackFlag != nil && *cfg.RobotTriageByTrackFlag,
		GroupByLabel:   cfg.RobotTriageByLabelFlag != nil && *cfg.RobotTriageByLabelFlag,
		WaitForPhase2:  true,
		UseFastConfig:  true,
		History:        historyReport,
		RootIssueID:    rootIssueID,
		SeedDataHash:   seedHash,
		NotReadyLabels: resolveNotReadyLabels(cfg),
	}, analysisNow)
	stabilizeRobotTriageForPinnedClock(&triage)
	triage.Meta.HistoryStatus = historyStatus
	degraded := applyRobotTriageAuthorityPolicy(ctx, &triage)

	// --brief (#183): emit only the decision-relevant fields agents actually
	// consume at session start (id/title/status, blockers/unblocks, claim
	// state) and skip the per-issue score breakdowns, project health, and
	// usage hints that dominate the full payload's token cost.
	if cfg.RobotTriageBriefFlag != nil && *cfg.RobotTriageBriefFlag {
		return encodeBriefTriage(ctx, triage, generatedAt, degraded)
	}

	var feedbackInfo *analysis.FeedbackJSON
	if !ctx.WorkspaceMode && strings.TrimSpace(ctx.AsOf) == "" && strings.TrimSpace(ctx.AsOfCommit) == "" && len(normalizedRobotAuthorityReasons(ctx.RepositoryRouteUnavailableReasons)) == 0 {
		if workDir, err := ctx.WorkDirOrDefault(); err == nil {
			if beadsDir, err := loader.GetBeadsDir(workDir); err == nil {
				if feedbackData, err := analysis.LoadFeedback(beadsDir); err == nil && len(feedbackData.Events) > 0 {
					info := feedbackData.ToJSON()
					feedbackInfo = &info
				}
			}
		}
	}

	output := struct {
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		RobotSourceEvidence
		Degraded   []robotNextDegradation `json:"degraded,omitempty"`
		Triage     analysis.TriageResult  `json:"triage"`
		Feedback   *analysis.FeedbackJSON `json:"feedback,omitempty"`
		UsageHints []string               `json:"usage_hints"`
	}{
		GeneratedAt:         generatedAt.Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		Degraded:            degraded,
		Triage:              triage,
		Feedback:            feedbackInfo,
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
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Assignee  string   `json:"assignee,omitempty"`
	Score     float64  `json:"score"`
	Unblocks  []string `json:"unblocks,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// briefTriageOutput is the compact --robot-triage --brief payload (#183).
// It keeps quick_ref (counts + top picks — the claimability signal),
// quick_wins, and blockers_to_clear (already lean), and reduces each
// recommendation to briefTriageRecommendation. Score breakdowns, project
// health, commands, feedback, and usage hints are omitted.
type briefTriageOutput struct {
	GeneratedAt string `json:"generated_at"`
	DataHash    string `json:"data_hash"`
	RobotSourceEvidence
	Degraded        []robotNextDegradation      `json:"degraded,omitempty"`
	Brief           bool                        `json:"brief"`
	QuickRef        analysis.QuickRef           `json:"quick_ref"`
	Recommendations []briefTriageRecommendation `json:"recommendations"`
	QuickWins       []analysis.QuickWin         `json:"quick_wins,omitempty"`
	BlockersToClear []analysis.BlockerItem      `json:"blockers_to_clear,omitempty"`
}

func encodeBriefTriage(ctx RobotContext, triage analysis.TriageResult, now time.Time, degraded []robotNextDegradation) error {
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
		})
	}
	output := briefTriageOutput{
		GeneratedAt:         now.Format(time.RFC3339),
		DataHash:            ctx.DataHash,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
		Degraded:            degraded,
		Brief:               true,
		QuickRef:            triage.QuickRef,
		Recommendations:     recs,
		QuickWins:           triage.QuickWins,
		BlockersToClear:     triage.BlockersToClear,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding robot-triage --brief: %w", err)
	}
	return nil
}

// applyRobotTriageAuthorityPolicy removes every field that the triage contract
// presents as immediately claimable when the loaded issue universe is
// incomplete or when bv cannot route viewer IDs to a verified live Beads
// target. The scored recommendations remain available for diagnosis and
// planning, but they cannot silently become copy-paste mutation commands.
func applyRobotTriageAuthorityPolicy(ctx RobotContext, triage *analysis.TriageResult) []robotNextDegradation {
	if triage == nil {
		return nil
	}

	unsafeAuthorityReasons, sourceIncomplete := robotNextAuthorityUnsafeReasons(ctx.LoadStats, ctx.AuthorityIncompleteReasons)
	unavailableReasons := make([]string, 0, len(ctx.ClaimCommandUnavailableReasons))
	for _, reason := range ctx.ClaimCommandUnavailableReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			unavailableReasons = append(unavailableReasons, reason)
		}
	}
	metricReasons := robotNextMetricUnsafeReasons(triage.Meta.Phase2Ready, triage.Status)
	globalClaimUnsafe := len(unsafeAuthorityReasons) > 0 || len(unavailableReasons) > 0 || len(metricReasons) > 0
	if globalClaimUnsafe {
		scrubRobotTriageClaimSignals(triage)
	}

	degraded := make([]robotNextDegradation, 0, 4)
	if len(unsafeAuthorityReasons) > 0 {
		code := "robot_triage_load_incomplete"
		repair := "Repair the source records and rerun bv before treating recommendations as claimable work."
		if sourceIncomplete {
			code = "robot_triage_authority_incomplete"
			repair = "Repair every reported workspace authority gap and rerun bv before treating recommendations as claimable work."
		}
		degraded = append(degraded, robotNextDegradation{
			Code:     code,
			Severity: "warning",
			Message:  strings.Join(unsafeAuthorityReasons, "; "),
			Repair:   repair,
		})
	}
	if len(unavailableReasons) > 0 {
		degraded = append(degraded, robotNextDegradation{
			Code:     "robot_triage_claim_routing_unavailable",
			Severity: "warning",
			Message:  strings.Join(unavailableReasons, "; "),
			Repair:   "Run the authoritative Beads actionable queue and claim gate inside the recommendation's live source repository.",
		})
	}
	if len(metricReasons) > 0 {
		degraded = append(degraded, robotNextDegradation{
			Code:     "robot_triage_metric_incomplete",
			Severity: "warning",
			Message:  strings.Join(metricReasons, "; "),
			Repair:   "Retry bv --robot-triage after graph metrics are available, or use the authoritative Beads actionable queue plus claim gate.",
		})
	}
	if !globalClaimUnsafe {
		if unsafePickReasons := filterRobotTriageClaimsByAuthority(triage, ctx.AuthoritativeIssues, ctx.Issues, triage.Meta.GeneratedAt); len(unsafePickReasons) > 0 {
			degraded = append(degraded, robotNextDegradation{
				Code:     "robot_triage_claim_unsafe",
				Severity: "warning",
				Message:  strings.Join(unsafePickReasons, "; "),
				Repair:   "Use the authoritative Beads actionable queue plus claim gate before claiming filtered recommendations.",
			})
		}
	}
	return degraded
}

func scrubRobotTriageClaimSignals(triage *analysis.TriageResult) {
	triage.QuickRef.TopPicks = []analysis.TopPick{}
	triage.Commands = analysis.CommandHelpers{}
	sanitizeRecommendations := func(recommendations []analysis.Recommendation) {
		for i := range recommendations {
			sanitizeRobotTriageRecommendation(&recommendations[i], "⚠️ Live claim state is unavailable; this recommendation is diagnostic only")
		}
	}
	sanitizeRecommendations(triage.Recommendations)
	for i := range triage.BlockersToClear {
		triage.BlockersToClear[i].Actionable = false
	}
	for i := range triage.RecommendationsByTrack {
		triage.RecommendationsByTrack[i].TopPick = nil
		triage.RecommendationsByTrack[i].ClaimCommand = ""
		sanitizeRecommendations(triage.RecommendationsByTrack[i].Recommendations)
	}
	for i := range triage.RecommendationsByLabel {
		triage.RecommendationsByLabel[i].TopPick = nil
		triage.RecommendationsByLabel[i].ClaimCommand = ""
		sanitizeRecommendations(triage.RecommendationsByLabel[i].Recommendations)
	}
}

func sanitizeRobotTriageRecommendation(recommendation *analysis.Recommendation, diagnosticReason string) {
	if recommendation == nil {
		return
	}
	recommendation.Action = "Inspect only; verify the live Beads state in the source repository before acting"
	// Recommendation copies in grouped and top-level output may share the same
	// Reasons backing array. Build an isolated slice so sanitizing one view cannot
	// rewrite another through that alias.
	filteredReasons := make([]string, 0, len(recommendation.Reasons)+1)
	for _, reason := range recommendation.Reasons {
		lower := strings.ToLower(reason)
		if strings.Contains(lower, "claim") || strings.Contains(lower, "available for work") || strings.Contains(lower, "already being worked") {
			continue
		}
		filteredReasons = append(filteredReasons, reason)
	}
	recommendation.Reasons = append(filteredReasons, diagnosticReason)
}

func filterRobotTriageClaimsByAuthority(triage *analysis.TriageResult, authoritativeIssues, scopedIssues []model.Issue, now time.Time) []string {
	if triage == nil {
		return nil
	}
	if authoritativeIssues == nil {
		authoritativeIssues = scopedIssues
	}
	authority := newRobotClaimAuthority(authoritativeIssues, now)
	unsafeReasons := make([]string, 0)
	seenReasons := make(map[string]bool)
	unsafeRecommendationIDs := make(map[string]bool)
	appendReasons := func(reasons []string) {
		for _, reason := range reasons {
			if !seenReasons[reason] {
				seenReasons[reason] = true
				unsafeReasons = append(unsafeReasons, reason)
			}
		}
	}
	markUnsafeRecommendations := func(recommendations []analysis.Recommendation) {
		for _, recommendation := range recommendations {
			pick := analysis.TopPick{
				ID:       recommendation.ID,
				Title:    recommendation.Title,
				Score:    recommendation.Score,
				Reasons:  recommendation.Reasons,
				Unblocks: len(recommendation.UnblocksIDs),
			}
			if reasons := authority.claimabilityReasons(pick); len(reasons) > 0 {
				unsafeRecommendationIDs[recommendation.ID] = true
				appendReasons(reasons)
			}
		}
	}

	// A recommendation is action-language, not merely a score row. Validate
	// every copy against the complete issue universe: lower-ranked rows can be
	// unsafe even when they never appear in quick_ref or as a group top pick.
	markUnsafeRecommendations(triage.Recommendations)
	for i := range triage.RecommendationsByTrack {
		markUnsafeRecommendations(triage.RecommendationsByTrack[i].Recommendations)
	}
	for i := range triage.RecommendationsByLabel {
		markUnsafeRecommendations(triage.RecommendationsByLabel[i].Recommendations)
	}

	safePicks := make([]analysis.TopPick, 0, len(triage.QuickRef.TopPicks))
	for _, pick := range triage.QuickRef.TopPicks {
		if reasons := authority.claimabilityReasons(pick); len(reasons) > 0 {
			appendReasons(reasons)
			unsafeRecommendationIDs[pick.ID] = true
			continue
		}
		safePicks = append(safePicks, pick)
	}
	triage.QuickRef.TopPicks = safePicks
	setRobotTriageTopCommands(&triage.Commands, safePicks)

	for i := range triage.RecommendationsByTrack {
		group := &triage.RecommendationsByTrack[i]
		if group.TopPick == nil {
			group.ClaimCommand = ""
			continue
		}
		if reasons := authority.claimabilityReasons(*group.TopPick); len(reasons) > 0 {
			appendReasons(reasons)
			unsafeRecommendationIDs[group.TopPick.ID] = true
			group.TopPick = nil
			group.ClaimCommand = ""
			continue
		}
		group.ClaimCommand = robotTriageClaimCommand(group.TopPick.ID)
	}
	for i := range triage.RecommendationsByLabel {
		group := &triage.RecommendationsByLabel[i]
		if group.TopPick == nil {
			group.ClaimCommand = ""
			continue
		}
		if reasons := authority.claimabilityReasons(*group.TopPick); len(reasons) > 0 {
			appendReasons(reasons)
			unsafeRecommendationIDs[group.TopPick.ID] = true
			group.TopPick = nil
			group.ClaimCommand = ""
			continue
		}
		group.ClaimCommand = robotTriageClaimCommand(group.TopPick.ID)
	}
	sanitizeUnsafeRecommendations := func(recommendations []analysis.Recommendation) {
		for i := range recommendations {
			if unsafeRecommendationIDs[recommendations[i].ID] {
				sanitizeRobotTriageRecommendation(
					&recommendations[i],
					"⚠️ The authoritative issue graph does not support claiming this filtered recommendation",
				)
			}
		}
	}
	sanitizeUnsafeRecommendations(triage.Recommendations)
	for i := range triage.RecommendationsByTrack {
		sanitizeUnsafeRecommendations(triage.RecommendationsByTrack[i].Recommendations)
	}
	for i := range triage.RecommendationsByLabel {
		sanitizeUnsafeRecommendations(triage.RecommendationsByLabel[i].Recommendations)
	}
	return unsafeReasons
}

func setRobotTriageTopCommands(commands *analysis.CommandHelpers, safePicks []analysis.TopPick) {
	if commands == nil {
		return
	}
	commands.ClaimTop = ""
	commands.ShowTop = ""
	if len(safePicks) == 0 {
		return
	}
	idArg, ok := shellCommandBeadID(safePicks[0].ID)
	if !ok {
		return
	}
	commands.ClaimTop = fmt.Sprintf("CI=1 br update %s --status in_progress --json", idArg)
	commands.ShowTop = fmt.Sprintf("CI=1 br show %s --json", idArg)
}

func robotTriageClaimCommand(id string) string {
	idArg, ok := shellCommandBeadID(id)
	if !ok {
		return ""
	}
	return fmt.Sprintf("CI=1 br update %s --status in_progress --json", idArg)
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
	Degraded          []robotNextDegradation   `json:"degraded,omitempty"`
	UsageHints        []string                 `json:"usage_hints,omitempty"`
}

type robotClaimAuthority struct {
	issueByID               map[string]model.Issue
	actionableIDs           map[string]bool
	parentsWithOpenChildren map[string]bool
	now                     time.Time
}

func newRobotClaimAuthority(issues []model.Issue, now time.Time) robotClaimAuthority {
	issueByID := robotNextIssueIndex(issues)
	analyzer := analysis.NewAnalyzer(issues)
	analyzer.SetNow(now)
	actionableIDs := make(map[string]bool, len(issues))
	for _, issue := range analyzer.GetActionableIssues() {
		actionableIDs[issue.ID] = true
	}
	return robotClaimAuthority{
		issueByID:               issueByID,
		actionableIDs:           actionableIDs,
		parentsWithOpenChildren: analyzer.ParentsWithOpenChildren(),
		now:                     now,
	}
}

func (a robotClaimAuthority) claimabilityReasons(pick analysis.TopPick) []string {
	reasons := robotNextClaimabilityReasons(pick, a.issueByID, a.now)
	if a.parentsWithOpenChildren[pick.ID] {
		reasons = append(reasons, fmt.Sprintf("%s has open child work in the authoritative issue graph", pick.ID))
	}
	if _, exists := a.issueByID[pick.ID]; exists && !a.actionableIDs[pick.ID] && len(reasons) == 0 {
		reasons = append(reasons, fmt.Sprintf("%s is not actionable in the authoritative issue graph", pick.ID))
	}
	return reasons
}

func robotNextIssueIndex(issues []model.Issue) map[string]model.Issue {
	issueByID := make(map[string]model.Issue, len(issues))
	for _, issue := range issues {
		issueByID[issue.ID] = issue
	}
	return issueByID
}

// formatRobotNextTime preserves the exact instant used by the claimability
// gate. Second-only values retain their existing RFC3339 representation, while
// fractional defer_until values cannot collapse onto the same displayed second
// as generated_at and make a correct claim/no-claim decision look contradictory.
func formatRobotNextTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func robotNextClaimabilityReasons(pick analysis.TopPick, issueByID map[string]model.Issue, now time.Time) []string {
	issue, ok := issueByID[pick.ID]
	if !ok {
		return []string{fmt.Sprintf("%s is absent from loaded Beads records", pick.ID)}
	}

	var reasons []string
	if !commandBeadIDSafe(pick.ID) {
		reasons = append(reasons, fmt.Sprintf("%q cannot be represented safely as a br command argument", pick.ID))
	}
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
		reasons = append(reasons, fmt.Sprintf("%s is deferred until %s", pick.ID, formatRobotNextTime(*issue.DeferUntil)))
	}

	var openBlockers []string
	for _, dep := range issue.Dependencies {
		if dep == nil || !dep.Type.IsBlocking() {
			continue
		}
		blockerID := strings.TrimSpace(dep.DependsOnID)
		if blockerID == "" {
			openBlockers = append(openBlockers, "<missing blocker id>")
			continue
		}
		blocker, ok := issueByID[blockerID]
		if !ok {
			openBlockers = append(openBlockers, blockerID+" (missing)")
			continue
		}
		if blocker.Status != model.StatusClosed && blocker.Status != model.StatusTombstone {
			openBlockers = append(openBlockers, blockerID)
		}
	}
	if len(openBlockers) > 0 {
		sort.Strings(openBlockers)
		reasons = append(reasons, fmt.Sprintf("%s is blocked by %s", pick.ID, strings.Join(openBlockers, ", ")))
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

func robotNextClaimablePick(picks []analysis.TopPick, issues []model.Issue, now time.Time) (analysis.TopPick, *robotNextDiagnosticPick, []string, bool) {
	if len(picks) == 0 {
		return analysis.TopPick{}, nil, nil, false
	}

	authority := newRobotClaimAuthority(issues, now)
	firstDiagnostic := robotNextDiagnosticFromPick(picks[0])
	var firstUnsafeReasons []string
	for _, pick := range picks {
		reasons := authority.claimabilityReasons(pick)
		if len(reasons) == 0 {
			return pick, &firstDiagnostic, nil, true
		}
		if len(firstUnsafeReasons) == 0 {
			firstUnsafeReasons = reasons
		}
	}

	return analysis.TopPick{}, &firstDiagnostic, firstUnsafeReasons, false
}

func robotNextMetricUnsafeReasons(phase2Ready bool, status analysis.MetricStatus) []string {
	reasons := status.ClaimUnsafeReasons()
	if !phase2Ready {
		reasons = append([]string{"phase 2 analysis is not ready"}, reasons...)
	}
	return reasons
}

func robotNextLoadUnsafeReasons(loadStats *RobotLoadStats) []string {
	if loadStats == nil || loadStats.Errors <= 0 {
		return nil
	}
	reason := fmt.Sprintf("authoritative issue load dropped %d record(s)", loadStats.Errors)
	if source := strings.TrimSpace(loadStats.SourcePath); source != "" {
		reason += " from " + source
	}
	return []string{reason}
}

func robotNextAuthorityUnsafeReasons(loadStats *RobotLoadStats, incompleteReasons []string) ([]string, bool) {
	reasons := robotNextLoadUnsafeReasons(loadStats)
	sourceIncomplete := false
	for _, reason := range incompleteReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			reasons = append(reasons, reason)
			sourceIncomplete = true
		}
	}
	return reasons, sourceIncomplete
}

func handleRobotNext(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	return handleRobotNextAt(ctx, cfg, robotNow())
}

// handleRobotNextAt keeps the exact decision clock explicit so generated_at,
// defer_until evaluation, and tests all share one instant without sleeping or
// racing a wall-clock boundary.
func handleRobotNextAt(ctx RobotContext, cfg phaseThreeRobotHandlerConfig, now time.Time) error {
	var rootIssueID string
	if cfg.GraphRoot != nil && *cfg.GraphRoot != "" {
		rootIssueID = *cfg.GraphRoot
	}

	analysisNow := now
	if !ctx.AnalysisTime.IsZero() {
		analysisNow = ctx.AnalysisTime
	}
	triage := analysis.ComputeTriageWithOptionsAndTime(ctx.Issues, analysis.TriageOptions{
		WaitForPhase2:  true,
		UseFastConfig:  true,
		RootIssueID:    rootIssueID,
		NotReadyLabels: resolveNotReadyLabels(cfg),
	}, analysisNow)
	stabilizeRobotTriageForPinnedClock(&triage)

	envelope := robotEnvelopeForContext(ctx, ctx.DataHash)
	// The context describes this command's authoritative load. Do not retain a
	// process-global report from an earlier/unrelated load when the authority
	// came from workspace aggregation or a historical snapshot.
	envelope.LoadStats = ctx.LoadStats
	// Keep the envelope on the same instant used to evaluate defer-until and
	// other claimability conditions, even if execution crosses a wall-clock
	// second before encoding.
	envelope.GeneratedAt = formatRobotNextTime(now)
	output := robotNextOutput{
		RobotEnvelope: envelope,
		Phase2Ready:   triage.Meta.Phase2Ready,
		Status:        triage.Status,
		UsageHints: []string{
			"Use scripts/br_retry.sh actionable --json plus the claim gate before mutating Beads state in crowded swarms.",
			"No claim_command is emitted unless the item is open, unblocked, unassigned, and triage metrics are ready.",
			"Inspect .status for skipped, timeout, or pending graph phases.",
		},
	}
	unsafeAuthorityReasons, sourceIncomplete := robotNextAuthorityUnsafeReasons(output.LoadStats, ctx.AuthorityIncompleteReasons)
	if len(unsafeAuthorityReasons) > 0 {
		output.Message = "No claim command emitted because the authoritative issue load was incomplete"
		if len(triage.QuickRef.TopPicks) > 0 {
			diagnostic := robotNextDiagnosticFromPick(triage.QuickRef.TopPicks[0])
			output.DiagnosticTopPick = &diagnostic
		}
		code := "robot_next_load_incomplete"
		repair := "Repair the source records and rerun bv before claiming work."
		if sourceIncomplete {
			code = "robot_next_authority_incomplete"
			repair = "Repair every reported workspace authority gap and rerun bv before claiming work."
		}
		output.Degraded = []robotNextDegradation{{
			Code:     code,
			Severity: "warning",
			Message:  strings.Join(unsafeAuthorityReasons, "; "),
			Repair:   repair,
		}}
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

	claimValidationIssues := ctx.AuthoritativeIssues
	if claimValidationIssues == nil {
		claimValidationIssues = ctx.Issues
	}
	top, diagnostic, unsafePickReasons, ok := robotNextClaimablePick(triage.QuickRef.TopPicks, claimValidationIssues, analysisNow)
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

	var unavailableReasons []string
	for _, reason := range ctx.ClaimCommandUnavailableReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			unavailableReasons = append(unavailableReasons, reason)
		}
	}
	if len(unavailableReasons) > 0 {
		output.Message = "No claim command emitted because bv could not route the recommendation to a safe live Beads target"
		output.DiagnosticTopPick = diagnostic
		output.Degraded = []robotNextDegradation{{
			Code:     "robot_next_claim_routing_unavailable",
			Severity: "warning",
			Message:  strings.Join(unavailableReasons, "; "),
			Repair:   "Run the authoritative Beads actionable queue and claim gate inside the recommendation's live source repository.",
		}}
		if err := ctx.EncoderOrDefault().Encode(output); err != nil {
			return fmt.Errorf("encoding robot-next: %w", err)
		}
		return nil
	}

	if unsafeReasons := robotNextMetricUnsafeReasons(triage.Meta.Phase2Ready, triage.Status); len(unsafeReasons) > 0 {
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

	output.Actionable = true
	output.ID = top.ID
	output.Title = top.Title
	output.Score = top.Score
	output.Reasons = top.Reasons
	output.Unblocks = top.Unblocks
	output.ClaimCmd = joinCommandWords([]string{"br", "update", top.ID, "--status=in_progress"})
	output.ShowCmd = joinCommandWords([]string{"br", "show", top.ID})

	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding robot-next: %w", err)
	}
	return nil
}

func handleRobotHistory(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-history"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	_, beadsPath, err := resolveCorrelationBeadsPath(workDir)
	if err != nil {
		return err
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

	report, err := correlation.NewCorrelator(workDir, beadsPath).GenerateReportCached(beadInfos, opts)
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.GeneratedAt = robotNow()
	report.DataHash = ctx.DataHash

	if cfg.MinConfidence != nil && *cfg.MinConfidence > 0 {
		scorer := correlation.NewScorer()
		report.Histories = scorer.FilterHistoriesByConfidence(report.Histories, *cfg.MinConfidence)
		report.RecalculateDerivedFields()
	}

	output := struct {
		correlation.HistoryReport
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
		RobotSourceEvidence
	}{
		HistoryReport:       *report,
		OutputFormat:        robotOutputFormat,
		Version:             version.Version,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
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

func generateCorrelationReport(workDir string, issues []model.Issue, opts correlation.CorrelatorOptions) (*correlation.HistoryReport, error) {
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

func requireLiveSingleRepoCorrelationContext(ctx RobotContext, command string) error {
	if strings.TrimSpace(ctx.AsOf) != "" || strings.TrimSpace(ctx.AsOfCommit) != "" {
		return fmt.Errorf("%s cannot safely correlate --as-of data yet: history extraction follows live HEAD; rerun without --as-of", command)
	}
	if ctx.WorkspaceMode {
		return fmt.Errorf("%s cannot safely correlate a multi-repository workspace through one Git work directory; run it inside one repository", command)
	}
	if reasons := normalizedRobotAuthorityReasons(ctx.RepositoryRouteUnavailableReasons); len(reasons) > 0 {
		return fmt.Errorf("%s cannot safely pair the selected issue source with working-directory Git history: %s", command, strings.Join(reasons, "; "))
	}
	return nil
}

func requireLiveSingleRepoSideDataContext(ctx RobotContext, command, sideData string) error {
	if strings.TrimSpace(ctx.AsOf) != "" || strings.TrimSpace(ctx.AsOfCommit) != "" {
		return fmt.Errorf("%s cannot safely combine historical issues with live %s; rerun without --as-of", command, sideData)
	}
	if ctx.WorkspaceMode {
		return fmt.Errorf("%s cannot safely combine a multi-repository workspace with single-repository %s; run it inside one repository", command, sideData)
	}
	if reasons := normalizedRobotAuthorityReasons(ctx.RepositoryRouteUnavailableReasons); len(reasons) > 0 {
		return fmt.Errorf("%s cannot safely pair the selected issue source with working-directory %s: %s", command, sideData, strings.Join(reasons, "; "))
	}
	return nil
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
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-explain-correlation"); err != nil {
		return err
	}
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

	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{BeadID: beadID})
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
		explanation.Recommendation = fmt.Sprintf("Already has feedback: %s", fb.Type)
	}
	output := struct {
		RobotEnvelope
		correlation.CorrelationExplanation
	}{
		RobotEnvelope:          robotEnvelopeForContext(ctx, ctx.DataHash),
		CorrelationExplanation: explanation,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding explanation: %w", err)
	}
	return nil
}

func handleRobotCorrelationFeedback(ctx RobotContext, cfg phaseThreeRobotHandlerConfig, reject bool) error {
	command := "--robot-confirm-correlation"
	if reject {
		command = "--robot-reject-correlation"
	}
	if err := requireLiveSingleRepoCorrelationContext(ctx, command); err != nil {
		return err
	}
	if reasons, _ := robotNextAuthorityUnsafeReasons(ctx.LoadStats, ctx.AuthorityIncompleteReasons); len(reasons) > 0 {
		return fmt.Errorf("%s requires a complete authoritative issue load: %s", command, strings.Join(reasons, "; "))
	}
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

	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{BeadID: beadID})
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

	result := struct {
		RobotEnvelope
		Status   string  `json:"status"`
		Commit   string  `json:"commit"`
		Bead     string  `json:"bead"`
		By       string  `json:"by"`
		Reason   string  `json:"reason"`
		OrigConf float64 `json:"orig_conf"`
	}{
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
		Status:        status,
		Commit:        commitSHA,
		Bead:          beadID,
		By:            feedbackBy,
		Reason:        reason,
		OrigConf:      originalConf,
	}
	if err := ctx.EncoderOrDefault().Encode(result); err != nil {
		return fmt.Errorf("encoding result: %w", err)
	}
	return nil
}

func handleRobotFileRelations(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-file-relations"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.DataHash = ctx.DataHash

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
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-orphans"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}
	report.DataHash = ctx.DataHash

	orphanReport, err := correlation.NewOrphanDetectorAt(report, workDir, robotNow()).DetectOrphans(correlation.ExtractOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("detecting orphans: %w", err)
	}

	minScore := 30
	if cfg.OrphansMinScore != nil {
		minScore = *cfg.OrphansMinScore
	}
	filterOrphanReportByMinScore(orphanReport, minScore)

	output := struct {
		*correlation.OrphanReport
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
		RobotSourceEvidence
	}{
		OrphanReport:        orphanReport,
		OutputFormat:        robotOutputFormat,
		Version:             version.Version,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding orphan report: %w", err)
	}
	return nil
}

func filterOrphanReportByMinScore(orphanReport *correlation.OrphanReport, minScore int) {
	filtered := make([]correlation.OrphanCandidate, 0, len(orphanReport.Candidates))
	byBead := make(map[string][]string)
	totalSuspicion := 0
	probableCandidateCount := 0

	for _, candidate := range orphanReport.Candidates {
		if candidate.SuspicionScore < minScore {
			continue
		}
		filtered = append(filtered, candidate)
		totalSuspicion += candidate.SuspicionScore
		if len(candidate.ProbableBeads) > 0 {
			probableCandidateCount++
		}
		for _, bead := range candidate.ProbableBeads {
			byBead[bead.BeadID] = append(byBead[bead.BeadID], candidate.SHA)
		}
	}

	orphanReport.Candidates = filtered
	orphanReport.ByBead = byBead
	orphanReport.Stats.CandidateCount = probableCandidateCount
	orphanReport.Stats.AvgSuspicion = 0
	if len(filtered) > 0 {
		orphanReport.Stats.AvgSuspicion = float64(totalSuspicion) / float64(len(filtered))
	}
}

func handleRobotFileBeads(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-file-beads"); err != nil {
		return err
	}
	if cfg.RobotFileBeadsFlag == nil {
		return fmt.Errorf("robot file beads flag not configured")
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}
	report.DataHash = ctx.DataHash

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
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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

func handleRobotFileHotspots(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-file-hotspots"); err != nil {
		return err
	}

	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	historyLimit := 500
	if cfg.HistoryLimit != nil {
		historyLimit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: historyLimit})
	if err != nil {
		return err
	}
	report.DataHash = ctx.DataHash

	hotspotsLimit := 10
	if cfg.HotspotsLimit != nil {
		hotspotsLimit = *cfg.HotspotsLimit
	}
	if hotspotsLimit < 0 {
		hotspotsLimit = 0
	}
	fileLookup := correlation.NewFileLookup(report)
	output := struct {
		RobotEnvelope
		Hotspots []correlation.FileHotspot  `json:"hotspots"`
		Stats    correlation.FileIndexStats `json:"stats"`
	}{
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
		Hotspots:      fileLookup.GetHotspots(hotspotsLimit),
		Stats:         fileLookup.GetStats(),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding file hotspots: %w", err)
	}
	return nil
}

func handleRobotImpact(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-impact"); err != nil {
		return err
	}
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
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return err
	}
	report.DataHash = ctx.DataHash

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
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
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
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-related"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.DataHash = ctx.DataHash

	depGraph := make(map[string][]string)
	for _, issue := range ctx.Issues {
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

	output := struct {
		*correlation.RelatedWorkResult
		DataHash     string `json:"data_hash"`
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
		RobotSourceEvidence
	}{
		RelatedWorkResult:   result,
		DataHash:            ctx.DataHash,
		OutputFormat:        robotOutputFormat,
		Version:             version.Version,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding related work: %w", err)
	}
	return nil
}

func handleRobotBlockerChain(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	targetID := *cfg.RobotBlockerChainFlag
	targetInScope := false
	for i := range ctx.Issues {
		if ctx.Issues[i].ID == targetID {
			targetInScope = true
			break
		}
	}
	if !targetInScope {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}
	authoritativeIssues := ctx.AuthoritativeIssues
	if authoritativeIssues == nil {
		authoritativeIssues = ctx.Issues
	}
	analyzer := analysis.NewAnalyzer(authoritativeIssues)
	analyzer.SetNow(ctx.AnalysisNowOrDefault())
	result := analyzer.GetBlockerChain(targetID)
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Issue not found: %s\n", targetID)
		return newReportedRobotHandlerExit(1)
	}
	resultHash := ctx.DataHash
	if ctx.AuthoritativeIssues != nil {
		resultHash = analysis.ComputeDataHash(authoritativeIssues)
	}
	unsafeReasons, _ := robotNextAuthorityUnsafeReasons(ctx.LoadStats, ctx.AuthorityIncompleteReasons)
	unsafeReasons = append(unsafeReasons, normalizedRobotAuthorityReasons(ctx.ClaimCommandUnavailableReasons)...)
	if len(unsafeReasons) > 0 {
		for i := range result.RootBlockers {
			result.RootBlockers[i].Actionable = false
		}
		for i := range result.Chain {
			result.Chain[i].Actionable = false
		}
	}

	output := struct {
		RobotEnvelope
		Result   *analysis.BlockerChainResult `json:"result"`
		Degraded []robotNextDegradation       `json:"degraded,omitempty"`
	}{
		RobotEnvelope: robotEnvelopeForContext(ctx, resultHash),
		Result:        result,
	}
	if len(unsafeReasons) > 0 {
		output.Degraded = []robotNextDegradation{{
			Code:     "robot_blocker_chain_authority_incomplete",
			Severity: "warning",
			Message:  strings.Join(unsafeReasons, "; "),
			Repair:   "Restore a complete live routable issue source and rerun before treating any chain node as actionable.",
		}}
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding blocker chain: %w", err)
	}
	return nil
}

func handleRobotImpactNetwork(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-impact-network"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.DataHash = ctx.DataHash

	network := correlation.NewNetworkBuilderWithIssues(report, ctx.Issues).BuildAt(robotNow())
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

	output := struct {
		*correlation.ImpactNetworkResult
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
		RobotSourceEvidence
	}{
		ImpactNetworkResult: network.ToResult(beadID, depth),
		OutputFormat:        robotOutputFormat,
		Version:             version.Version,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding impact network: %w", err)
	}
	return nil
}

func handleRobotCausality(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoCorrelationContext(ctx, "--robot-causality"); err != nil {
		return err
	}
	workDir, err := ctx.WorkDirOrDefault()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	limit := 500
	if cfg.HistoryLimit != nil {
		limit = *cfg.HistoryLimit
	}
	report, err := generateCorrelationReport(workDir, ctx.Issues, correlation.CorrelatorOptions{Limit: limit})
	if err != nil {
		return fmt.Errorf("generating history report: %w", err)
	}
	report.DataHash = ctx.DataHash

	result := report.BuildCausalityChainAt(*cfg.RobotCausalityFlag, correlation.CausalityOptions{
		IncludeCommits: true,
	}, robotNow())
	if result == nil {
		fmt.Fprintf(ctx.StderrOrDefault(), "Bead not found: %s\n", *cfg.RobotCausalityFlag)
		return newReportedRobotHandlerExit(1)
	}

	output := struct {
		*correlation.CausalityResult
		OutputFormat string `json:"output_format,omitempty"`
		Version      string `json:"version,omitempty"`
		RobotSourceEvidence
	}{
		CausalityResult:     result,
		OutputFormat:        robotOutputFormat,
		Version:             version.Version,
		RobotSourceEvidence: robotSourceEvidenceForContext(ctx),
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding causality result: %w", err)
	}
	return nil
}

func handleRobotSprintShow(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
	if err := requireLiveSingleRepoSideDataContext(ctx, "--robot-sprint-show", "sprint metadata"); err != nil {
		return err
	}
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
		RobotEnvelope: robotEnvelopeForContext(ctx, ctx.DataHash),
		Sprint:        found,
	}
	if err := ctx.EncoderOrDefault().Encode(output); err != nil {
		return fmt.Errorf("encoding sprint: %w", err)
	}
	return nil
}

func handleRobotCapacity(ctx RobotContext, cfg phaseThreeRobotHandlerConfig) error {
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
	now := ctx.AnalysisNowOrDefault()
	authoritativeIssues := ctx.AuthoritativeIssues
	if authoritativeIssues == nil {
		authoritativeIssues = ctx.Issues
	}
	analyzer := analysis.NewAnalyzer(authoritativeIssues)
	analyzer.SetNow(now)
	graphStats := analyzer.Analyze()

	openIssues := make([]model.Issue, 0)
	issueMap := make(map[string]model.Issue, len(targetIssues))
	targetIDs := make(map[string]struct{}, len(targetIssues))
	for _, issue := range targetIssues {
		issueMap[issue.ID] = issue
		targetIDs[issue.ID] = struct{}{}
		if issue.Status != model.StatusClosed && issue.Status != model.StatusTombstone {
			openIssues = append(openIssues, issue)
		}
	}

	agents := 1
	if cfg.CapacityAgents != nil && *cfg.CapacityAgents > 0 {
		agents = *cfg.CapacityAgents
	}

	totalMinutes := 0
	for _, issue := range openIssues {
		eta, err := analysis.EstimateETAForIssue(authoritativeIssues, &graphStats, issue.ID, 1, now)
		if err == nil {
			totalMinutes += eta.EstimatedMinutes
		}
	}

	blocks := make(map[string][]string)
	for _, issue := range openIssues {
		for _, dep := range issue.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if _, exists := issueMap[dep.DependsOnID]; exists {
				blocks[dep.DependsOnID] = append(blocks[dep.DependsOnID], issue.ID)
			}
		}
	}
	for id := range blocks {
		sort.Strings(blocks[id])
	}

	actionable := make([]string, 0)
	for _, issue := range analyzer.GetActionableIssues() {
		if _, inScope := targetIDs[issue.ID]; inScope {
			actionable = append(actionable, issue.ID)
		}
	}

	longestChain := make([]string, 0)
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
		eta, err := analysis.EstimateETAForIssue(authoritativeIssues, &graphStats, id, 1, now)
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
		if bottlenecks[i].BlocksCount != bottlenecks[j].BlocksCount {
			return bottlenecks[i].BlocksCount > bottlenecks[j].BlocksCount
		}
		return bottlenecks[i].ID < bottlenecks[j].ID
	})
	if len(bottlenecks) > 5 {
		bottlenecks = bottlenecks[:5]
	}

	unsafeReasons, _ := robotNextAuthorityUnsafeReasons(ctx.LoadStats, ctx.AuthorityIncompleteReasons)
	unsafeReasons = append(unsafeReasons, normalizedRobotAuthorityReasons(ctx.ClaimCommandUnavailableReasons)...)
	var degraded []robotNextDegradation
	if len(unsafeReasons) > 0 {
		actionable = []string{}
		degraded = []robotNextDegradation{{
			Code:     "robot_capacity_actionability_unavailable",
			Severity: "warning",
			Message:  strings.Join(unsafeReasons, "; "),
			Repair:   "Restore a complete live routable issue source and rerun before treating capacity items as actionable.",
		}}
	}

	output := struct {
		RobotEnvelope
		Agents            int                    `json:"agents"`
		Label             string                 `json:"label,omitempty"`
		OpenIssueCount    int                    `json:"open_issue_count"`
		TotalMinutes      int                    `json:"total_minutes"`
		TotalDays         float64                `json:"total_days"`
		SerialMinutes     int                    `json:"serial_minutes"`
		ParallelMinutes   int                    `json:"parallel_minutes"`
		ParallelizablePct float64                `json:"parallelizable_pct"`
		EstimatedDays     float64                `json:"estimated_days"`
		CriticalPathLen   int                    `json:"critical_path_length"`
		CriticalPath      []string               `json:"critical_path"`
		ActionableCount   int                    `json:"actionable_count"`
		Actionable        []string               `json:"actionable"`
		Bottlenecks       []bottleneck           `json:"bottlenecks,omitempty"`
		Degraded          []robotNextDegradation `json:"degraded,omitempty"`
	}{
		RobotEnvelope: robotEnvelopeForContext(ctx, func() string {
			if ctx.AuthoritativeIssues != nil {
				return analysis.ComputeDataHash(authoritativeIssues)
			}
			return ctx.DataHash
		}()),
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
		Degraded:          degraded,
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
