// Package env provides a centralized registry for all environment variables
// used by beads_viewer (BV_* and BEADS_*).
//
// Every call site in production code must read these variables through this
// package rather than calling raw os.Getenv directly.
package env

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// Var represents a registered environment variable with documentation and default values.
type Var struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     string `json:"default"`
}

// Get returns the value of the environment variable via os.Getenv.
func (v Var) Get() string {
	return os.Getenv(v.Name)
}

// Lookup retrieves the value of the environment variable via os.LookupEnv.
func (v Var) Lookup() (string, bool) {
	return os.LookupEnv(v.Name)
}

// IsSet reports whether the environment variable is present in the environment.
func (v Var) IsSet() bool {
	_, ok := os.LookupEnv(v.Name)
	return ok
}

// Bool reports whether the variable is set to a truthy value ("1", "true", "yes", "on").
func (v Var) Bool() bool {
	val := strings.TrimSpace(strings.ToLower(v.Get()))
	return val == "1" || val == "true" || val == "yes" || val == "on"
}

// Int parses the variable value as an integer. Returns defaultValue if unset or invalid.
func (v Var) Int(defaultValue int) int {
	val := strings.TrimSpace(v.Get())
	if val == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultValue
	}
	return n
}

// Registered environment variables.
var (
	BeadsDB = register(Var{
		Name:        "BEADS_DB",
		Description: "Path to a beads database file or `.beads` directory. Overrides `BEADS_DIR`; overridden by `--db`.",
		Default:     "(unset)",
	})

	BeadsDir = register(Var{
		Name:        "BEADS_DIR",
		Description: "Custom beads directory path. When set, overrides the default `.beads` directory lookup.",
		Default:     "`.beads` in cwd",
	})

	BackgroundMode = register(Var{
		Name:        "BV_BACKGROUND_MODE",
		Description: "Startup default for the background snapshot worker (`1` on, `0` off). At runtime the TUI promotes itself to the worker after any synchronous reload that takes 1 s or longer; `0` pins synchronous reload and disables that promotion.",
		Default:     "sync at startup, auto-promote after a slow reload",
	})

	BuildHybridWasm = register(Var{
		Name:        "BV_BUILD_HYBRID_WASM",
		Description: "Set to `1` to build the hybrid search WASM scorer during `--export-pages` (requires wasm-pack).",
		Default:     "(skip)",
	})

	CacheDir = register(Var{
		Name:        "BV_CACHE_DIR",
		Description: "Base directory for the disk caches (`analysis_cache/` and correlation caches live under it).",
		Default:     "`<user cache dir>/bv`",
	})

	DebounceMS = register(Var{
		Name:        "BV_DEBOUNCE_MS",
		Description: "Debounce window (milliseconds) for live reload events in background mode.",
		Default:     "`200`",
	})

	Debug = register(Var{
		Name:        "BV_DEBUG",
		Description: "Any value: write `[BV_DEBUG]` diagnostics to stderr.",
		Default:     "(off)",
	})

	ForcePoll = register(Var{
		Name:        "BV_FORCE_POLL",
		Description: "Alias for `BV_FORCE_POLLING`.",
		Default:     "(auto)",
	})

	ForcePolling = register(Var{
		Name:        "BV_FORCE_POLLING",
		Description: "Force polling-based live reload (useful on NFS/SMB/SSHFS/FUSE or any setup where filesystem events are unreliable) (`1`/`0`).",
		Default:     "(auto)",
	})

	FreshnessStaleS = register(Var{
		Name:        "BV_FRESHNESS_STALE_S",
		Description: "Snapshot staleness critical threshold (seconds).",
		Default:     "`120`",
	})

	FreshnessWarnS = register(Var{
		Name:        "BV_FRESHNESS_WARN_S",
		Description: "Snapshot staleness warning threshold (seconds).",
		Default:     "`30`",
	})

	HeartbeatIntervalS = register(Var{
		Name:        "BV_HEARTBEAT_INTERVAL_S",
		Description: "Background worker heartbeat interval (seconds).",
		Default:     "`5`",
	})

	InsightsMapLimit = register(Var{
		Name:        "BV_INSIGHTS_MAP_LIMIT",
		Description: "Cap on the number of entries in each `--robot-insights` metric map.",
		Default:     "(all)",
	})

	MaxLineSizeMB = register(Var{
		Name:        "BV_MAX_LINE_SIZE_MB",
		Description: "Max JSONL line size in MB (lines larger than this are skipped with a warning). Applies to the TUI, the background worker, and robot loads.",
		Default:     "`10`",
	})

	Metrics = register(Var{
		Name:        "BV_METRICS",
		Description: "Set to `0` to disable internal timing metrics collection (`--robot-metrics`).",
		Default:     "(enabled)",
	})

	NoBrowser = register(Var{
		Name:        "BV_NO_BROWSER",
		Description: "Any value: never open a browser after exports or deployments.",
		Default:     "(unset)",
	})

	NoCache = register(Var{
		Name:        "BV_NO_CACHE",
		Description: "Set to `1` to bypass the robot analysis and correlation disk caches (`--no-cache` sets it).",
		Default:     "(cache on)",
	})

	NoGitignore = register(Var{
		Name:        "BV_NO_GITIGNORE",
		Description: "Disable automatic ignore-file management for `.bv/` entirely (any non-empty value). See [Automatic `.bv/` ignore handling](#automatic-bv-ignore-handling).",
		Default:     "(enabled)",
	})

	NoSavedConfig = register(Var{
		Name:        "BV_NO_SAVED_CONFIG",
		Description: "Any value: the `--pages` wizard ignores the saved deployment configuration.",
		Default:     "(unset)",
	})

	NoUpdateCheck = register(Var{
		Name:        "BV_NO_UPDATE_CHECK",
		Description: "Set to `1` to skip the TUI's startup release check (`updates: {check: false}` in `~/.config/bv/config.yaml` does the same); explicit `--check-update` / `--update` still work.",
		Default:     "(check on)",
	})

	OutputFormat = register(Var{
		Name:        "BV_OUTPUT_FORMAT",
		Description: "Default robot output format: `json` or `toon` (overridden by `--format`).",
		Default:     "`json`",
	})

	Phase2TimeoutS = register(Var{
		Name:        "BV_PHASE2_TIMEOUT_S",
		Description: "Override per-metric Phase 2 timeouts (seconds).",
		Default:     "(size-based)",
	})

	PrettyJSON = register(Var{
		Name:        "BV_PRETTY_JSON",
		Description: "Set to `1` for indented JSON output.",
		Default:     "(compact)",
	})

	Robot = register(Var{
		Name:        "BV_ROBOT",
		Description: "Set to `1` to force robot mode (clean stdout, JSON logs, disk cache on). Every `--robot-*` flag sets it.",
		Default:     "(unset)",
	})

	RobotHistoryTimeoutMS = register(Var{
		Name:        "BV_ROBOT_HISTORY_TIMEOUT_MS",
		Description: "Bound on the git-history prologue of `--robot-triage` in milliseconds; `0` = unbounded.",
		Default:     "`10000`",
	})

	RobotNotReadyLabels = register(Var{
		Name:        "BV_ROBOT_NOT_READY_LABELS",
		Description: "Comma-separated labels marking a bead not-ready; excluded from claimable `--robot-next`/`--robot-triage` top picks (`--robot-not-ready-labels` overrides).",
		Default:     "(none)",
	})

	SearchMode = register(Var{
		Name:        "BV_SEARCH_MODE",
		Description: "Default search mode: `text` or `hybrid` (`--search-mode` overrides).",
		Default:     "`text`",
	})

	SearchPreset = register(Var{
		Name:        "BV_SEARCH_PRESET",
		Description: "Default hybrid preset: `default`, `bug-hunting`, `sprint-planning`, `impact-first`, `text-only`; setting one implies hybrid mode.",
		Default:     "`default`",
	})

	SearchWeights = register(Var{
		Name:        "BV_SEARCH_WEIGHTS",
		Description: "JSON weight map for hybrid search; overrides the preset.",
		Default:     "(preset)",
	})

	SemanticDim = register(Var{
		Name:        "BV_SEMANTIC_DIM",
		Description: "Embedding dimension for the hashed search index.",
		Default:     "`384`",
	})

	SemanticEmbedder = register(Var{
		Name:        "BV_SEMANTIC_EMBEDDER",
		Description: "Embedding provider for `bv --search` and TUI search. Only `hash` (FNV-1a keyword feature hashing) is implemented; `python-sentence-transformers` and `openai` are reserved names that fail with \"not implemented\".",
		Default:     "`hash`",
	})

	SemanticModel = register(Var{
		Name:        "BV_SEMANTIC_MODEL",
		Description: "Model name for a future non-hash provider; ignored by `hash`.",
		Default:     "(empty)",
	})

	SkipPhase2 = register(Var{
		Name:        "BV_SKIP_PHASE2",
		Description: "Skip Phase 2 graph metrics (centrality, cycles, critical path) (`1`/`0`).",
		Default:     "(disabled)",
	})

	TestMode = register(Var{
		Name:        "BV_TEST_MODE",
		Description: "Any value: test harness mode; suppresses browser opening, terminal capability queries, and the background worker's idle GC tuning.",
		Default:     "(unset)",
	})

	Theme = register(Var{
		Name:        "BV_THEME",
		Description: "Pin the TUI palette: `light` or `dark` (overridden by `--theme`).",
		Default:     "(auto-detect)",
	})

	TUIAutocloseMS = register(Var{
		Name:        "BV_TUI_AUTOCLOSE_MS",
		Description: "Quit the TUI automatically after this many milliseconds (for automated tests).",
		Default:     "(unset)",
	})

	UpdateUseToken = register(Var{
		Name:        "BV_UPDATE_USE_TOKEN",
		Description: "Set to `1` to let the update check and `--update` send the ambient `GITHUB_TOKEN` / `GH_TOKEN` to api.github.com (`updates: {use_token: true}` in config.yaml does the same).",
		Default:     "(never sent)",
	})

	WatchdogIntervalS = register(Var{
		Name:        "BV_WATCHDOG_INTERVAL_S",
		Description: "Background worker watchdog interval (seconds).",
		Default:     "`10`",
	})

	WorkerLogLevel = register(Var{
		Name:        "BV_WORKER_LOG_LEVEL",
		Description: "Log level for the background snapshot worker.",
		Default:     "(default)",
	})

	WorkerMetrics = register(Var{
		Name:        "BV_WORKER_METRICS",
		Description: "Truthy value: the background worker records its own metrics.",
		Default:     "(off)",
	})

	WorkerTrace = register(Var{
		Name:        "BV_WORKER_TRACE",
		Description: "Path to a trace file the background worker appends to.",
		Default:     "(off)",
	})
)

var (
	registry = make(map[string]Var)
	allVars  []Var
)

func register(v Var) Var {
	registry[v.Name] = v
	allVars = append(allVars, v)
	return v
}

// Get returns the environment variable value by name.
func Get(name string) string {
	if v, ok := registry[name]; ok {
		return v.Get()
	}
	return os.Getenv(name)
}

// Lookup retrieves the environment variable value and presence by name.
func Lookup(name string) (string, bool) {
	if v, ok := registry[name]; ok {
		return v.Lookup()
	}
	return os.LookupEnv(name)
}

// Find looks up a Var definition by name.
func Find(name string) (Var, bool) {
	v, ok := registry[name]
	return v, ok
}

// All returns all registered environment variables sorted by name.
func All() []Var {
	out := make([]Var, len(allVars))
	copy(out, allVars)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
