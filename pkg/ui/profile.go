package ui

import (
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
)

// TUIStartupProfile captures detailed timing information for TUI initialization.
// Use NewModelWithProfile to populate this structure.
type TUIStartupProfile struct {
	// Data characteristics
	IssueCount      int `json:"issue_count"`
	DependencyCount int `json:"dependency_count"`

	// Phase 1 - Data Loading (captured externally)
	LoadJSONL time.Duration `json:"load_jsonl"`

	// Phase 2 - Analysis (from analysis.StartupProfile)
	Analysis *analysis.StartupProfile `json:"analysis,omitempty"`

	// Phase 3 - TUI Component Initialization
	ThemeInit       time.Duration `json:"theme_init"`
	ListSetup       time.Duration `json:"list_setup"`
	GlamourInit     time.Duration `json:"glamour_init"`
	BoardInit       time.Duration `json:"board_init"`
	InsightsInit    time.Duration `json:"insights_init"`
	GraphViewInit   time.Duration `json:"graph_view_init"`
	RecipeLoader    time.Duration `json:"recipe_loader"`
	RecipePicker    time.Duration `json:"recipe_picker"`
	TextInputInit   time.Duration `json:"text_input_init"`
	FileWatcherInit time.Duration `json:"file_watcher_init"`
	SortIssues      time.Duration `json:"sort_issues"`
	BuildLookup     time.Duration `json:"build_lookup"`
	ComputeStats    time.Duration `json:"compute_stats"`

	// Totals
	TUIComponentsTotal time.Duration `json:"tui_components_total"`
	NewModelTotal      time.Duration `json:"new_model_total"`
	TotalStartup       time.Duration `json:"total_startup"`
}

// SetLoadDuration sets the JSONL load duration (captured before NewModel).
func (p *TUIStartupProfile) SetLoadDuration(d time.Duration) {
	p.LoadJSONL = d
}

// ComputeTotals calculates the total fields based on individual timings.
func (p *TUIStartupProfile) ComputeTotals() {
	p.TUIComponentsTotal = p.ThemeInit + p.ListSetup + p.GlamourInit +
		p.BoardInit + p.InsightsInit + p.GraphViewInit +
		p.RecipeLoader + p.RecipePicker + p.TextInputInit +
		p.FileWatcherInit

	if p.Analysis != nil {
		p.TotalStartup = p.LoadJSONL + p.Analysis.Total + p.TUIComponentsTotal
	} else {
		p.TotalStartup = p.LoadJSONL + p.NewModelTotal
	}
}
