package drift

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"gopkg.in/yaml.v3"
)

// Config contains drift detection thresholds
type Config struct {
	// DensityWarningPct triggers warning when density increases by this percentage
	DensityWarningPct float64 `yaml:"density_warning_pct" json:"density_warning_pct"`

	// DensityInfoPct triggers info when density increases by this percentage
	DensityInfoPct float64 `yaml:"density_info_pct" json:"density_info_pct"`

	// NodeGrowthInfoPct triggers info when node count changes by this percentage
	NodeGrowthInfoPct float64 `yaml:"node_growth_info_pct" json:"node_growth_info_pct"`

	// EdgeGrowthInfoPct triggers info when edge count changes by this percentage
	EdgeGrowthInfoPct float64 `yaml:"edge_growth_info_pct" json:"edge_growth_info_pct"`

	// BlockedIncreaseThreshold triggers warning when blocked count increases by this amount
	BlockedIncreaseThreshold int `yaml:"blocked_increase_threshold" json:"blocked_increase_threshold"`

	// ActionableDecreaseWarningPct triggers warning when actionable decreases by this pct
	ActionableDecreaseWarningPct float64 `yaml:"actionable_decrease_warning_pct" json:"actionable_decrease_warning_pct"`

	// ActionableIncreaseInfoPct triggers info when actionable changes by this pct
	ActionableIncreaseInfoPct float64 `yaml:"actionable_increase_info_pct" json:"actionable_increase_info_pct"`

	// PageRankChangeWarningPct triggers warning when PageRank changes by this pct
	PageRankChangeWarningPct float64 `yaml:"pagerank_change_warning_pct" json:"pagerank_change_warning_pct"`

	// Staleness thresholds (days since last update)
	StaleWarningDays  int `yaml:"stale_warning_days" json:"stale_warning_days"`
	StaleCriticalDays int `yaml:"stale_critical_days" json:"stale_critical_days"`

	// In-progress multiplier: <1 tightens thresholds for in_progress items
	InProgressStaleMultiplier float64 `yaml:"in_progress_stale_multiplier" json:"in_progress_stale_multiplier"`

	// Blocking cascade thresholds
	BlockingCascadeInfo    int `yaml:"blocking_cascade_info_threshold" json:"blocking_cascade_info_threshold"`
	BlockingCascadeWarning int `yaml:"blocking_cascade_warning_threshold" json:"blocking_cascade_warning_threshold"`

	// Scope creep: open-issue growth (percent) over the baseline that triggers info
	ScopeCreepPct float64 `yaml:"scope_creep_pct" json:"scope_creep_pct"`

	// Velocity drop: closes in the last VelocityWindowDays fell by VelocityDropPct or
	// more versus the previous window, which must hold at least VelocityMinBaseline closes
	VelocityDropPct     float64 `yaml:"velocity_drop_pct" json:"velocity_drop_pct"`
	VelocityWindowDays  int     `yaml:"velocity_window_days" json:"velocity_window_days"`
	VelocityMinBaseline int     `yaml:"velocity_min_baseline" json:"velocity_min_baseline"`

	// High-impact unblock: actionable issue unblocking HighImpactUnblockMin+ items of which
	// at least one has priority <= HighImpactPriorityMax (P0=0 ... P4=4)
	HighImpactUnblockMin  int `yaml:"high_impact_unblock_min" json:"high_impact_unblock_min"`
	HighImpactPriorityMax int `yaml:"high_impact_priority_max" json:"high_impact_priority_max"`

	// Abandoned claim: in_progress issues with an assignee idle longer than
	// stale_warning_days x in_progress_stale_multiplier x this multiplier
	AbandonedClaimMultiplier float64 `yaml:"abandoned_claim_multiplier" json:"abandoned_claim_multiplier"`

	// Potential duplicate: keyword Jaccard similarity threshold and alert cap per run
	DuplicateJaccardThreshold float64 `yaml:"duplicate_jaccard_threshold" json:"duplicate_jaccard_threshold"`
	DuplicateMaxAlerts        int     `yaml:"duplicate_max_alerts" json:"duplicate_max_alerts"`

	// Priority mismatch: minimum recommendation confidence (0-1) that becomes a warning
	PriorityMismatchMinConfidence float64 `yaml:"priority_mismatch_min_confidence" json:"priority_mismatch_min_confidence"`

	// ProactiveMaxIssues caps the graph size for the expensive proactive checks
	// (priority_mismatch re-runs the full metric suite, potential_duplicate compares
	// titles pairwise). Above it those checks are skipped and reported in
	// skipped_checks; 0 means no cap.
	ProactiveMaxIssues int `yaml:"proactive_max_issues" json:"proactive_max_issues"`

	// Alert type enable/disable flags (bv-167)
	// Disabled alert types will not generate alerts
	DisabledAlerts []string `yaml:"disabled_alerts,omitempty" json:"disabled_alerts,omitempty"`

	// Per-label staleness overrides (bv-167)
	// Labels can have tighter or looser thresholds than the default
	LabelOverrides map[string]*LabelConfig `yaml:"label_overrides,omitempty" json:"label_overrides,omitempty"`

	// Sprint at-risk thresholds (bv-apal.28)
	// SprintBlockedDays flags blocked sprint items stale for >= N days (default: 2)
	SprintBlockedDays int `yaml:"sprint_blocked_days,omitempty" json:"sprint_blocked_days,omitempty"`
	// SprintInactiveDays flags in_progress sprint items with no updates for >= N days (default: 4)
	SprintInactiveDays int `yaml:"sprint_inactive_days,omitempty" json:"sprint_inactive_days,omitempty"`
}

// LabelConfig allows per-label threshold customization (bv-167)
type LabelConfig struct {
	// StaleWarningDays overrides the default for issues with this label
	StaleWarningDays int `yaml:"stale_warning_days,omitempty" json:"stale_warning_days,omitempty"`
	// StaleCriticalDays overrides the default for issues with this label
	StaleCriticalDays int `yaml:"stale_critical_days,omitempty" json:"stale_critical_days,omitempty"`
	// InProgressStaleMultiplier overrides the default for this label
	InProgressStaleMultiplier float64 `yaml:"in_progress_stale_multiplier,omitempty" json:"in_progress_stale_multiplier,omitempty"`
}

// DefaultConfig returns sensible default thresholds
func DefaultConfig() *Config {
	return &Config{
		DensityWarningPct:            50,  // 50% increase triggers warning
		DensityInfoPct:               20,  // 20% increase triggers info
		NodeGrowthInfoPct:            25,  // 25% node change triggers info
		EdgeGrowthInfoPct:            25,  // 25% edge change triggers info
		BlockedIncreaseThreshold:     5,   // 5+ more blocked issues triggers warning
		ActionableDecreaseWarningPct: 30,  // 30% decrease in actionable triggers warning
		ActionableIncreaseInfoPct:    20,  // 20% change in actionable triggers info
		PageRankChangeWarningPct:     50,  // 50% PageRank change triggers warning
		StaleWarningDays:             14,  // Warn after 14 days inactive
		StaleCriticalDays:            30,  // Critical after 30 days inactive
		InProgressStaleMultiplier:    0.5, // In-progress thresholds are half as long
		BlockingCascadeInfo:          3,   // Info alert when unblocks >=3
		BlockingCascadeWarning:       5,   // Warning when unblocks >=5

		ScopeCreepPct:                 20,   // Info when open issues grew 20%+ over the baseline
		VelocityDropPct:               50,   // Warning when closes fell 50%+ window over window
		VelocityWindowDays:            7,    // Compare the last 7 days with the 7 before
		VelocityMinBaseline:           5,    // ...but only when the prior window closed 5+
		HighImpactUnblockMin:          3,    // Unblocks 3+ items...
		HighImpactPriorityMax:         1,    // ...including at least one P0/P1
		AbandonedClaimMultiplier:      2,    // 2x the in-progress stale threshold (14d by default)
		DuplicateJaccardThreshold:     0.7,  // Keyword Jaccard similarity for potential_duplicate
		DuplicateMaxAlerts:            10,   // At most 10 duplicate alerts per run
		PriorityMismatchMinConfidence: 0.6,  // Recommendation confidence that becomes a warning
		ProactiveMaxIssues:            2000, // Skip the expensive checks above this many issues
		SprintBlockedDays:             2,    // Blocked sprint items flagged after 2 days
		SprintInactiveDays:            4,    // In-progress sprint items flagged after 4 days idle
	}
}

// ConfigFilename is the default config filename
const ConfigFilename = "drift.yaml"

// ConfigPath returns the default config path for a project
func ConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".bv", ConfigFilename)
}

// LoadConfig loads drift configuration from .bv/drift.yaml
// Returns default config if file doesn't exist
func LoadConfig(projectDir string) (*Config, error) {
	path := ConfigPath(projectDir)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return defaults if no config file
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading drift config: %w", err)
	}

	config := DefaultConfig() // Start with defaults
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parsing drift config: %w", err)
	}

	// Validate loaded config
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid drift config: %w", err)
	}

	return config, nil
}

// SaveConfig saves drift configuration to .bv/drift.yaml
func SaveConfig(projectDir string, config *Config) error {
	// Validate before saving
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	path := ConfigPath(projectDir)

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding drift config: %w", err)
	}

	// Add header comment
	header := "# Drift detection thresholds\n# See: bv --help for drift detection options\n\n"
	content := header + string(data)

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing drift config: %w", err)
	}

	return nil
}

// Validate checks that config values are sensible
func (c *Config) Validate() error {
	// Backfill optional fields to defaults when omitted (for backward compat)
	if c.StaleWarningDays == 0 {
		c.StaleWarningDays = DefaultConfig().StaleWarningDays
	}
	if c.StaleCriticalDays == 0 {
		c.StaleCriticalDays = DefaultConfig().StaleCriticalDays
	}
	if c.InProgressStaleMultiplier == 0 {
		c.InProgressStaleMultiplier = DefaultConfig().InProgressStaleMultiplier
	}
	// Proactive-check thresholds added later than the file format: an omitted
	// key means "default", a negative value is an error (checked below), and a
	// zero for the alert-cap style knobs means "unlimited" where noted.
	defaults := DefaultConfig()
	if c.ScopeCreepPct == 0 {
		c.ScopeCreepPct = defaults.ScopeCreepPct
	}
	if c.VelocityDropPct == 0 {
		c.VelocityDropPct = defaults.VelocityDropPct
	}
	if c.VelocityWindowDays == 0 {
		c.VelocityWindowDays = defaults.VelocityWindowDays
	}
	if c.VelocityMinBaseline == 0 {
		c.VelocityMinBaseline = defaults.VelocityMinBaseline
	}
	if c.HighImpactUnblockMin == 0 {
		c.HighImpactUnblockMin = defaults.HighImpactUnblockMin
	}
	if c.HighImpactPriorityMax == 0 && c.HighImpactUnblockMin == defaults.HighImpactUnblockMin {
		// P0-only is a legitimate setting; only backfill when the whole block was omitted.
		c.HighImpactPriorityMax = defaults.HighImpactPriorityMax
	}
	if c.AbandonedClaimMultiplier == 0 {
		c.AbandonedClaimMultiplier = defaults.AbandonedClaimMultiplier
	}
	if c.DuplicateJaccardThreshold == 0 {
		c.DuplicateJaccardThreshold = defaults.DuplicateJaccardThreshold
	}
	if c.DuplicateMaxAlerts == 0 {
		c.DuplicateMaxAlerts = defaults.DuplicateMaxAlerts
	}
	if c.PriorityMismatchMinConfidence == 0 {
		c.PriorityMismatchMinConfidence = defaults.PriorityMismatchMinConfidence
	}
	if c.SprintBlockedDays == 0 {
		c.SprintBlockedDays = defaults.SprintBlockedDays
	}
	if c.SprintInactiveDays == 0 {
		c.SprintInactiveDays = defaults.SprintInactiveDays
	}
	if c.SprintBlockedDays < 0 || c.SprintInactiveDays < 0 {
		return fmt.Errorf("sprint_blocked_days and sprint_inactive_days must be positive")
	}
	if c.ProactiveMaxIssues < 0 {
		return fmt.Errorf("proactive_max_issues must be non-negative (0 = no cap)")
	}
	if c.ScopeCreepPct < 0 || c.ScopeCreepPct > 1000 {
		return fmt.Errorf("scope_creep_pct must be between 0 and 1000")
	}
	if c.VelocityDropPct < 0 || c.VelocityDropPct > 100 {
		return fmt.Errorf("velocity_drop_pct must be between 0 and 100")
	}
	if c.VelocityWindowDays < 0 || c.VelocityMinBaseline < 0 {
		return fmt.Errorf("velocity_window_days and velocity_min_baseline must be non-negative")
	}
	if c.HighImpactUnblockMin < 0 {
		return fmt.Errorf("high_impact_unblock_min must be non-negative")
	}
	if c.HighImpactPriorityMax < 0 || c.HighImpactPriorityMax > 4 {
		return fmt.Errorf("high_impact_priority_max must be a priority between 0 and 4")
	}
	if c.AbandonedClaimMultiplier < 0 || c.AbandonedClaimMultiplier > 10 {
		return fmt.Errorf("abandoned_claim_multiplier must be between 0 and 10")
	}
	if c.DuplicateJaccardThreshold < 0 || c.DuplicateJaccardThreshold > 1 {
		return fmt.Errorf("duplicate_jaccard_threshold must be between 0 and 1")
	}
	if c.DuplicateMaxAlerts < 0 {
		return fmt.Errorf("duplicate_max_alerts must be non-negative")
	}
	if c.PriorityMismatchMinConfidence < 0 || c.PriorityMismatchMinConfidence > 1 {
		return fmt.Errorf("priority_mismatch_min_confidence must be between 0 and 1")
	}

	if c.DensityWarningPct < 0 || c.DensityWarningPct > 1000 {
		return fmt.Errorf("density_warning_pct must be between 0 and 1000")
	}
	if c.DensityInfoPct < 0 || c.DensityInfoPct > c.DensityWarningPct {
		return fmt.Errorf("density_info_pct must be between 0 and density_warning_pct")
	}
	if c.NodeGrowthInfoPct < 0 || c.NodeGrowthInfoPct > 1000 {
		return fmt.Errorf("node_growth_info_pct must be between 0 and 1000")
	}
	if c.EdgeGrowthInfoPct < 0 || c.EdgeGrowthInfoPct > 1000 {
		return fmt.Errorf("edge_growth_info_pct must be between 0 and 1000")
	}
	if c.BlockedIncreaseThreshold < 0 {
		return fmt.Errorf("blocked_increase_threshold must be non-negative")
	}
	if c.ActionableDecreaseWarningPct < 0 || c.ActionableDecreaseWarningPct > 100 {
		return fmt.Errorf("actionable_decrease_warning_pct must be between 0 and 100")
	}
	if c.ActionableIncreaseInfoPct < 0 || c.ActionableIncreaseInfoPct > 1000 {
		return fmt.Errorf("actionable_increase_info_pct must be between 0 and 1000")
	}
	if c.PageRankChangeWarningPct < 0 || c.PageRankChangeWarningPct > 1000 {
		return fmt.Errorf("pagerank_change_warning_pct must be between 0 and 1000")
	}
	if c.StaleWarningDays <= 0 || c.StaleCriticalDays <= 0 {
		return fmt.Errorf("stale_warning_days and stale_critical_days must be positive")
	}
	if c.StaleCriticalDays < c.StaleWarningDays {
		return fmt.Errorf("stale_critical_days must be >= stale_warning_days")
	}
	if c.InProgressStaleMultiplier <= 0 || c.InProgressStaleMultiplier > 5 {
		return fmt.Errorf("in_progress_stale_multiplier must be between 0 and 5")
	}
	if c.BlockingCascadeInfo < 0 || c.BlockingCascadeWarning < 0 {
		return fmt.Errorf("blocking cascade thresholds must be non-negative")
	}
	if c.BlockingCascadeWarning < c.BlockingCascadeInfo {
		return fmt.Errorf("blocking_cascade_warning_threshold must be >= blocking_cascade_info_threshold")
	}
	// Validate label overrides (bv-167)
	for label, lc := range c.LabelOverrides {
		if lc == nil {
			continue
		}
		if lc.StaleWarningDays < 0 || lc.StaleCriticalDays < 0 {
			return fmt.Errorf("label %q: stale days must be non-negative", label)
		}
		if lc.StaleWarningDays > 0 && lc.StaleCriticalDays > 0 && lc.StaleCriticalDays < lc.StaleWarningDays {
			return fmt.Errorf("label %q: stale_critical_days must be >= stale_warning_days", label)
		}
		if lc.InProgressStaleMultiplier < 0 || lc.InProgressStaleMultiplier > 5 {
			return fmt.Errorf("label %q: in_progress_stale_multiplier must be between 0 and 5", label)
		}
	}
	return nil
}

// IsAlertDisabled returns true if the given alert type is in the disabled list (bv-167)
func (c *Config) IsAlertDisabled(alertType string) bool {
	for _, disabled := range c.DisabledAlerts {
		if disabled == alertType {
			return true
		}
	}
	return false
}

// GetStalenessThresholds returns the staleness thresholds for an issue based on its labels (bv-167)
// Returns warn days, critical days, and in-progress multiplier.
// If multiple labels have overrides, the tightest (smallest) non-zero thresholds among them are used.
// If valid overrides exist, they supersede the global default (allowing looser thresholds).
// Unset (0) values in overrides inherit the global default.
func (c *Config) GetStalenessThresholds(labels []string) (warnDays, critDays int, inProgressMult float64) {
	// Identify applicable overrides
	var applicable []*LabelConfig
	for _, label := range labels {
		if lc, ok := c.LabelOverrides[label]; ok && lc != nil {
			applicable = append(applicable, lc)
		}
	}

	// If no overrides, return global defaults
	if len(applicable) == 0 {
		return c.StaleWarningDays, c.StaleCriticalDays, c.InProgressStaleMultiplier
	}

	// Helper to resolve a config value: use override if set, else global default
	resolve := func(lc *LabelConfig) (w, cr int, m float64) {
		w = lc.StaleWarningDays
		if w <= 0 {
			w = c.StaleWarningDays
		}
		cr = lc.StaleCriticalDays
		if cr <= 0 {
			cr = c.StaleCriticalDays
		}
		m = lc.InProgressStaleMultiplier
		if m <= 0 {
			m = c.InProgressStaleMultiplier
		}
		return
	}

	// Initialize with the first applicable override
	warnDays, critDays, inProgressMult = resolve(applicable[0])

	// Min-reduce against remaining overrides
	for i := 1; i < len(applicable); i++ {
		w, cr, m := resolve(applicable[i])
		if w < warnDays {
			warnDays = w
		}
		if cr < critDays {
			critDays = cr
		}
		if m < inProgressMult {
			inProgressMult = m
		}
	}

	return
}

// AtRiskThresholds returns sprint burndown at-risk thresholds configured via
// .bv/drift.yaml, falling back to DefaultAtRiskThresholds for unset fields.
func (c *Config) AtRiskThresholds() analysis.AtRiskThresholds {
	defaults := analysis.DefaultAtRiskThresholds()
	if c == nil {
		return defaults
	}
	if c.SprintBlockedDays > 0 {
		defaults.BlockedDays = c.SprintBlockedDays
	}
	if c.SprintInactiveDays > 0 {
		defaults.InactiveDays = c.SprintInactiveDays
	}
	return defaults
}

// ExampleConfig returns an example configuration with comments
func ExampleConfig() string {
	return `# Drift detection thresholds configuration
# All percentage values are relative to baseline values

# Graph density thresholds (higher density = more interconnected)
density_warning_pct: 50    # Warn if density increases by 50%+
density_info_pct: 20       # Info if density increases by 20%+

# Node and edge count thresholds
node_growth_info_pct: 25   # Info if nodes change by 25%+
edge_growth_info_pct: 25   # Info if edges change by 25%+

# Issue status thresholds
blocked_increase_threshold: 5    # Warn if 5+ more issues are blocked
actionable_decrease_warning_pct: 30  # Warn if actionable drops 30%+
actionable_increase_info_pct: 20     # Info if actionable changes 20%+

# Metric change thresholds
pagerank_change_warning_pct: 50  # Warn if PageRank changes 50%+

# Staleness thresholds (days since last update)
stale_warning_days: 14           # Warn if an issue is inactive for 14+ days
stale_critical_days: 30          # Critical if inactive for 30+ days
in_progress_stale_multiplier: 0.5  # In-progress items age twice as fast

# Blocking cascade thresholds (downstream items)
blocking_cascade_info_threshold: 3   # Info alert if completing an issue unblocks 3+ items
blocking_cascade_warning_threshold: 5 # Warning if unblocks 5+ items

# Scope creep (needs a saved baseline)
scope_creep_pct: 20              # Info if open issues grew 20%+ since the baseline

# Velocity drop (closes in the last window vs the window before)
velocity_drop_pct: 50            # Warning if closes fell 50%+
velocity_window_days: 7          # Window length in days
velocity_min_baseline: 5         # Only alert when the prior window closed 5+ issues

# High-impact unblock (blocking cascade with a priority signal)
high_impact_unblock_min: 3       # Actionable issue unblocks 3+ items...
high_impact_priority_max: 1      # ...including at least one P0/P1 (two or more => warning)

# Abandoned claim (in_progress with an assignee, idle)
abandoned_claim_multiplier: 2    # Idle longer than stale_warning_days x in_progress_stale_multiplier x 2

# Potential duplicates (keyword similarity)
duplicate_jaccard_threshold: 0.7 # Pairs at or above this Jaccard score
duplicate_max_alerts: 10         # Cap per run

# Priority mismatch (from --robot-priority recommendations)
priority_mismatch_min_confidence: 0.6  # Warning at this confidence or above

# Graph-size cap for the expensive proactive checks (priority_mismatch, potential_duplicate);
# larger graphs skip them and report skipped_checks. 0 disables the cap.
proactive_max_issues: 2000

# Sprint burndown at-risk thresholds (bv-apal.28)
sprint_blocked_days: 2           # Blocked sprint items stale for >= N days
sprint_inactive_days: 4          # In-progress sprint items idle for >= N days

# Disable specific alert types (bv-167)
# Uncomment to disable:
# disabled_alerts:
#   - stale_issue
#   - new_cycle
#   - blocking_cascade

# Per-label staleness overrides (bv-167)
# Use tighter thresholds for urgent/priority labels
# label_overrides:
#   urgent:
#     stale_warning_days: 3
#     stale_critical_days: 7
#   low-priority:
#     stale_warning_days: 30
#     stale_critical_days: 60
`
}
