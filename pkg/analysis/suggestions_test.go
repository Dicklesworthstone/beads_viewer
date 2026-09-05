package analysis

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Synthetic origins exercise command construction only. Real tracker execution
// and exported-state readback are covered by the CLI suggestion tests.
func suggestionTestOrigin(id string) *model.IssueOrigin {
	return &model.IssueOrigin{LocalID: id, WorkingDirectory: "/work/owner's repo", TrackerDirectory: "/work/owner's repo/.beads",
		Database: "/work/owner's repo/.beads/beads.db", Tracker: "br", Executable: "/tools/br"}
}

func TestSuggestionMutationRoutes(t *testing.T) {
	for _, detector := range []struct {
		name string
		run  func([]model.Issue) []Suggestion
	}{
		{"dependency", func(issues []model.Issue) []Suggestion {
			cfg := DefaultDependencySuggestionConfig()
			cfg.MinConfidence = 0.1
			return DetectMissingDependencies(issues, cfg)
		}},
		{"duplicate", func(issues []model.Issue) []Suggestion { return DetectDuplicates(issues, DefaultDuplicateConfig()) }},
		{"label", func(issues []model.Issue) []Suggestion {
			cfg := DefaultLabelSuggestionConfig()
			cfg.MinConfidence = 0.1
			return SuggestLabels(issues, cfg)
		}},
		{"cycle", func(issues []model.Issue) []Suggestion {
			issues[0].Dependencies = []*model.Dependency{{DependsOnID: issues[1].ID, Type: model.DepBlocks}}
			issues[1].Dependencies = []*model.Dependency{{DependsOnID: issues[0].ID, Type: model.DepBlocks}}
			return DetectCycleWarnings(issues, DefaultCycleWarningConfig())
		}},
	} {
		for _, scope := range []string{"same tracker", "unknown", "historical", "partial", "different tracker"} {
			t.Run(detector.name+"/"+scope, func(t *testing.T) {
				issues := []model.Issue{
					{ID: "api-display-a", Title: "Fix authentication login bug", Status: model.StatusOpen, CreatedAt: time.Unix(1, 0), Origin: suggestionTestOrigin("local-a")},
					{ID: "api-display-b", Title: "Fix authentication login bug", Status: model.StatusOpen, CreatedAt: time.Unix(2, 0), Labels: []string{"bug", "auth"}, Origin: suggestionTestOrigin("local-b")},
				}
				switch scope {
				case "unknown":
					issues[0].Origin, issues[1].Origin = nil, nil
				case "historical", "partial":
					issues[0].Origin.ReadOnlyReason, issues[1].Origin.ReadOnlyReason = scope+" source", scope+" source"
				case "different tracker":
					issues[1].Origin.Database = "/other/beads.db"
				}
				suggestions := detector.run(issues)
				if len(suggestions) == 0 {
					t.Fatal("route refusal must retain useful analyses")
				}
				allowed := scope == "same tracker" || (scope == "different tracker" && detector.name == "label")
				for _, suggestion := range suggestions {
					if suggestion.Summary == "" || suggestion.Reason == "" || suggestion.Confidence <= 0 {
						t.Fatalf("lost useful analysis: %+v", suggestion)
					}
					if allowed {
						if suggestion.ActionCommand == "" || !strings.Contains(suggestion.ActionCommand, "local-") || strings.Contains(suggestion.ActionCommand, "api-display-") || !strings.Contains(suggestion.ActionCommand, "cd --") {
							t.Errorf("command must target original local ID and directory: %+v", suggestion)
						}
						if suggestion.Action == nil || suggestion.Action.WorkingDirectory != issues[0].Origin.WorkingDirectory || suggestion.Action.Shell != suggestion.ActionCommand || len(suggestion.Action.Argv) == 0 {
							t.Errorf("typed argv route missing: %+v", suggestion)
						}
						if detector.name == "cycle" && suggestion.Action != nil {
							path := suggestion.Metadata["cycle_path"].([]string)
							localIDs := map[string]string{"api-display-a": "local-a", "api-display-b": "local-b"}
							args := suggestion.Action.Argv
							if len(args) < 2 || args[len(args)-2] != localIDs[path[len(path)-1]] || args[len(args)-1] != localIDs[path[0]] {
								t.Fatalf("removal must use the last cycle edge's source, not the summary target: %+v", suggestion)
							}
						}
					} else if suggestion.ActionCommand != "" {
						t.Errorf("%s source emitted mutation: %+v", scope, suggestion)
					} else if suggestion.Action != nil || suggestion.Metadata["action_unavailable_reason"] == nil {
						t.Errorf("refusal must remove typed action and explain why: %+v", suggestion)
					}
				}
			})
		}
	}
}

func TestSuggestionType_Constants(t *testing.T) {
	// Verify all suggestion types have expected string values
	tests := []struct {
		st   SuggestionType
		want string
	}{
		{SuggestionMissingDependency, "missing_dependency"},
		{SuggestionPotentialDuplicate, "potential_duplicate"},
		{SuggestionLabelSuggestion, "label_suggestion"},
		{SuggestionStaleCleanup, "stale_cleanup"},
		{SuggestionCycleWarning, "cycle_warning"},
	}

	for _, tt := range tests {
		if string(tt.st) != tt.want {
			t.Errorf("SuggestionType %v = %q, want %q", tt.st, string(tt.st), tt.want)
		}
	}
}

func TestConfidenceLevel_Constants(t *testing.T) {
	tests := []struct {
		level ConfidenceLevel
		want  string
	}{
		{ConfidenceLow, "low"},
		{ConfidenceMedium, "medium"},
		{ConfidenceHigh, "high"},
	}

	for _, tt := range tests {
		if string(tt.level) != tt.want {
			t.Errorf("ConfidenceLevel %v = %q, want %q", tt.level, string(tt.level), tt.want)
		}
	}
}

func TestConfidenceThresholds(t *testing.T) {
	// Verify threshold values are sensible
	if ConfidenceThresholdLow >= ConfidenceThresholdHigh {
		t.Error("Low threshold should be less than high threshold")
	}
	if ConfidenceThresholdLow < 0 || ConfidenceThresholdLow > 1 {
		t.Errorf("Low threshold %v out of range [0,1]", ConfidenceThresholdLow)
	}
	if ConfidenceThresholdHigh < 0 || ConfidenceThresholdHigh > 1 {
		t.Errorf("High threshold %v out of range [0,1]", ConfidenceThresholdHigh)
	}
	if MinConfidenceDefault < 0 || MinConfidenceDefault > 1 {
		t.Errorf("MinConfidenceDefault %v out of range [0,1]", MinConfidenceDefault)
	}
}

func TestNewSuggestion(t *testing.T) {
	before := time.Now()
	s := NewSuggestion(
		SuggestionMissingDependency,
		"BEAD-123",
		"Add dependency to BEAD-456",
		"Similar content detected",
		0.85,
	)
	after := time.Now()

	if s.Type != SuggestionMissingDependency {
		t.Errorf("Type = %v, want %v", s.Type, SuggestionMissingDependency)
	}
	if s.TargetBead != "BEAD-123" {
		t.Errorf("TargetBead = %v, want BEAD-123", s.TargetBead)
	}
	if s.Summary != "Add dependency to BEAD-456" {
		t.Errorf("Summary mismatch")
	}
	if s.Reason != "Similar content detected" {
		t.Errorf("Reason mismatch")
	}
	if s.Confidence != 0.85 {
		t.Errorf("Confidence = %v, want 0.85", s.Confidence)
	}
	if s.GeneratedAt.Before(before) || s.GeneratedAt.After(after) {
		t.Errorf("GeneratedAt %v out of expected range", s.GeneratedAt)
	}

	// Check default values
	if s.RelatedBead != "" {
		t.Errorf("RelatedBead should be empty by default, got %q", s.RelatedBead)
	}
	if s.ActionCommand != "" {
		t.Errorf("ActionCommand should be empty by default, got %q", s.ActionCommand)
	}
	if s.Metadata != nil {
		t.Errorf("Metadata should be nil by default")
	}
}

func TestSuggestion_GetConfidenceLevel(t *testing.T) {
	tests := []struct {
		name       string
		confidence float64
		want       ConfidenceLevel
	}{
		// Low confidence (< 0.4)
		{"zero", 0.0, ConfidenceLow},
		{"very_low", 0.1, ConfidenceLow},
		{"low_boundary", 0.39, ConfidenceLow},

		// Medium confidence (0.4-0.7)
		{"at_low_threshold", 0.4, ConfidenceMedium},
		{"medium", 0.5, ConfidenceMedium},
		{"high_medium", 0.69, ConfidenceMedium},

		// High confidence (>= 0.7)
		{"at_high_threshold", 0.7, ConfidenceHigh},
		{"high", 0.85, ConfidenceHigh},
		{"very_high", 0.99, ConfidenceHigh},
		{"max", 1.0, ConfidenceHigh},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Suggestion{Confidence: tt.confidence}
			got := s.GetConfidenceLevel()
			if got != tt.want {
				t.Errorf("GetConfidenceLevel() for confidence=%v = %v, want %v", tt.confidence, got, tt.want)
			}
		})
	}
}

func TestSuggestion_IsActionable(t *testing.T) {
	tests := []struct {
		name   string
		action string
		want   bool
	}{
		{"no_action", "", false},
		{"with_action", "br dep add BEAD-123 BEAD-456", true},
		{"whitespace_action", "   ", true}, // Non-empty string is actionable
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Suggestion{ActionCommand: tt.action}
			if got := s.IsActionable(); got != tt.want {
				t.Errorf("IsActionable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSuggestion_WithRelatedBead(t *testing.T) {
	s := NewSuggestion(SuggestionPotentialDuplicate, "BEAD-1", "Dup", "Same title", 0.9)

	// Original should not be modified
	s2 := s.WithRelatedBead("BEAD-2")
	if s.RelatedBead != "" {
		t.Error("Original suggestion should not be modified")
	}
	if s2.RelatedBead != "BEAD-2" {
		t.Errorf("WithRelatedBead() RelatedBead = %q, want BEAD-2", s2.RelatedBead)
	}

	// Other fields preserved
	if s2.TargetBead != "BEAD-1" {
		t.Error("WithRelatedBead() should preserve TargetBead")
	}
	if s2.Type != SuggestionPotentialDuplicate {
		t.Error("WithRelatedBead() should preserve Type")
	}
}

func TestSuggestion_WithAction(t *testing.T) {
	s := NewSuggestion(SuggestionCycleWarning, "BEAD-1", "Break cycle", "Cycle detected", 0.95)

	s2 := s.WithAction(&model.IssueCommand{Shell: "br dep remove BEAD-1 BEAD-2"})
	if s.ActionCommand != "" {
		t.Error("Original suggestion should not be modified")
	}
	if s2.ActionCommand != "br dep remove BEAD-1 BEAD-2" {
		t.Errorf("WithAction() ActionCommand mismatch")
	}

	// Should be actionable now
	if !s2.IsActionable() {
		t.Error("Suggestion with action should be actionable")
	}
}

func TestSuggestion_WithMetadata(t *testing.T) {
	s := NewSuggestion(SuggestionLabelSuggestion, "BEAD-1", "Add label", "Content match", 0.6)

	// First metadata
	s2 := s.WithMetadata("suggested_label", "backend")
	if s.Metadata != nil {
		t.Error("Original suggestion should not be modified")
	}
	if s2.Metadata["suggested_label"] != "backend" {
		t.Error("WithMetadata() should add key")
	}

	// Chain metadata
	s3 := s2.WithMetadata("confidence_source", "keyword_match")
	if s3.Metadata["suggested_label"] != "backend" {
		t.Error("Chained WithMetadata() should preserve previous")
	}
	if s3.Metadata["confidence_source"] != "keyword_match" {
		t.Error("Chained WithMetadata() should add new key")
	}
	if _, exists := s2.Metadata["confidence_source"]; exists {
		t.Error("Chained WithMetadata() should not mutate the previous suggestion")
	}

	// Various types
	s4 := s.WithMetadata("count", 42).WithMetadata("score", 0.95).WithMetadata("active", true)
	if s4.Metadata["count"] != 42 {
		t.Error("WithMetadata() should handle int")
	}
	if s4.Metadata["score"] != 0.95 {
		t.Error("WithMetadata() should handle float")
	}
	if s4.Metadata["active"] != true {
		t.Error("WithMetadata() should handle bool")
	}
}

func TestSuggestion_JSON(t *testing.T) {
	s := NewSuggestion(SuggestionMissingDependency, "BEAD-1", "Add dep", "Content match", 0.75).
		WithRelatedBead("BEAD-2").
		WithAction(&model.IssueCommand{Shell: "br dep add BEAD-1 BEAD-2"}).
		WithMetadata("source", "content_analysis")

	// Serialize
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Deserialize
	var s2 Suggestion
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Verify round-trip
	if s2.Type != s.Type {
		t.Error("JSON round-trip: Type mismatch")
	}
	if s2.TargetBead != s.TargetBead {
		t.Error("JSON round-trip: TargetBead mismatch")
	}
	if s2.RelatedBead != s.RelatedBead {
		t.Error("JSON round-trip: RelatedBead mismatch")
	}
	if s2.Confidence != s.Confidence {
		t.Error("JSON round-trip: Confidence mismatch")
	}
	if s2.ActionCommand != s.ActionCommand {
		t.Error("JSON round-trip: ActionCommand mismatch")
	}
	if s2.Metadata["source"] != s.Metadata["source"] {
		t.Error("JSON round-trip: Metadata mismatch")
	}
}

func TestNewSuggestionSet_Empty(t *testing.T) {
	set := NewSuggestionSet([]Suggestion{}, "hash123")

	if len(set.Suggestions) != 0 {
		t.Error("Empty set should have no suggestions")
	}
	if set.DataHash != "hash123" {
		t.Errorf("DataHash = %q, want hash123", set.DataHash)
	}
	if set.Stats.Total != 0 {
		t.Error("Empty set should have Total=0")
	}
	if set.Stats.HighConfidenceCount != 0 {
		t.Error("Empty set should have HighConfidenceCount=0")
	}
	if set.Stats.ActionableCount != 0 {
		t.Error("Empty set should have ActionableCount=0")
	}
}

func TestNewSuggestionSet_SingleSuggestion(t *testing.T) {
	s := NewSuggestion(SuggestionMissingDependency, "BEAD-1", "Add dep", "Reason", 0.8).
		WithAction(&model.IssueCommand{Shell: "br dep add BEAD-1 BEAD-2"})

	set := NewSuggestionSet([]Suggestion{s}, "hash456")

	if set.Stats.Total != 1 {
		t.Errorf("Total = %d, want 1", set.Stats.Total)
	}
	if set.Stats.ByType[SuggestionMissingDependency] != 1 {
		t.Error("ByType should count missing_dependency")
	}
	if set.Stats.ByConfidence[ConfidenceHigh] != 1 {
		t.Error("ByConfidence should count high (0.8)")
	}
	if set.Stats.HighConfidenceCount != 1 {
		t.Error("HighConfidenceCount should be 1")
	}
	if set.Stats.ActionableCount != 1 {
		t.Error("ActionableCount should be 1")
	}
}

func TestNewSuggestionSet_MultipleSuggestions(t *testing.T) {
	suggestions := []Suggestion{
		NewSuggestion(SuggestionMissingDependency, "B1", "S1", "R1", 0.9).WithAction(&model.IssueCommand{Shell: "cmd1"}),
		NewSuggestion(SuggestionMissingDependency, "B2", "S2", "R2", 0.5),
		NewSuggestion(SuggestionPotentialDuplicate, "B3", "S3", "R3", 0.8).WithAction(&model.IssueCommand{Shell: "cmd2"}),
		NewSuggestion(SuggestionLabelSuggestion, "B4", "S4", "R4", 0.3),
		NewSuggestion(SuggestionCycleWarning, "B5", "S5", "R5", 0.95),
	}

	set := NewSuggestionSet(suggestions, "hash789")

	// Total
	if set.Stats.Total != 5 {
		t.Errorf("Total = %d, want 5", set.Stats.Total)
	}

	// By type
	if set.Stats.ByType[SuggestionMissingDependency] != 2 {
		t.Errorf("ByType[missing_dependency] = %d, want 2", set.Stats.ByType[SuggestionMissingDependency])
	}
	if set.Stats.ByType[SuggestionPotentialDuplicate] != 1 {
		t.Errorf("ByType[potential_duplicate] = %d, want 1", set.Stats.ByType[SuggestionPotentialDuplicate])
	}
	if set.Stats.ByType[SuggestionLabelSuggestion] != 1 {
		t.Errorf("ByType[label_suggestion] = %d, want 1", set.Stats.ByType[SuggestionLabelSuggestion])
	}
	if set.Stats.ByType[SuggestionCycleWarning] != 1 {
		t.Errorf("ByType[cycle_warning] = %d, want 1", set.Stats.ByType[SuggestionCycleWarning])
	}
	if set.Stats.ByType[SuggestionStaleCleanup] != 0 {
		t.Errorf("ByType[stale_cleanup] = %d, want 0", set.Stats.ByType[SuggestionStaleCleanup])
	}

	// By confidence: low(<0.4)=1, medium(0.4-0.7)=1, high(>=0.7)=3
	if set.Stats.ByConfidence[ConfidenceLow] != 1 {
		t.Errorf("ByConfidence[low] = %d, want 1", set.Stats.ByConfidence[ConfidenceLow])
	}
	if set.Stats.ByConfidence[ConfidenceMedium] != 1 {
		t.Errorf("ByConfidence[medium] = %d, want 1", set.Stats.ByConfidence[ConfidenceMedium])
	}
	if set.Stats.ByConfidence[ConfidenceHigh] != 3 {
		t.Errorf("ByConfidence[high] = %d, want 3", set.Stats.ByConfidence[ConfidenceHigh])
	}

	// High confidence count
	if set.Stats.HighConfidenceCount != 3 {
		t.Errorf("HighConfidenceCount = %d, want 3", set.Stats.HighConfidenceCount)
	}

	// Actionable count (2 have actions)
	if set.Stats.ActionableCount != 2 {
		t.Errorf("ActionableCount = %d, want 2", set.Stats.ActionableCount)
	}
}

func TestSuggestionSet_FilterByType(t *testing.T) {
	suggestions := []Suggestion{
		NewSuggestion(SuggestionMissingDependency, "B1", "S1", "R1", 0.9),
		NewSuggestion(SuggestionPotentialDuplicate, "B2", "S2", "R2", 0.8),
		NewSuggestion(SuggestionMissingDependency, "B3", "S3", "R3", 0.7),
		NewSuggestion(SuggestionLabelSuggestion, "B4", "S4", "R4", 0.6),
	}
	set := NewSuggestionSet(suggestions, "")

	// Filter missing_dependency
	deps := set.FilterByType(SuggestionMissingDependency)
	if len(deps) != 2 {
		t.Errorf("FilterByType(missing_dependency) = %d items, want 2", len(deps))
	}
	for _, s := range deps {
		if s.Type != SuggestionMissingDependency {
			t.Errorf("Filtered item has wrong type: %v", s.Type)
		}
	}

	// Filter non-existent type
	stale := set.FilterByType(SuggestionStaleCleanup)
	if len(stale) != 0 {
		t.Errorf("FilterByType(stale_cleanup) should return empty, got %d", len(stale))
	}

	// Filter single item type
	dups := set.FilterByType(SuggestionPotentialDuplicate)
	if len(dups) != 1 {
		t.Errorf("FilterByType(potential_duplicate) = %d items, want 1", len(dups))
	}
}

func TestSuggestionSet_FilterByMinConfidence(t *testing.T) {
	suggestions := []Suggestion{
		NewSuggestion(SuggestionMissingDependency, "B1", "S1", "R1", 0.95),
		NewSuggestion(SuggestionPotentialDuplicate, "B2", "S2", "R2", 0.75),
		NewSuggestion(SuggestionLabelSuggestion, "B3", "S3", "R3", 0.50),
		NewSuggestion(SuggestionCycleWarning, "B4", "S4", "R4", 0.25),
	}
	set := NewSuggestionSet(suggestions, "")

	tests := []struct {
		minConf float64
		want    int
	}{
		{0.0, 4},  // All
		{0.25, 4}, // At boundary, includes 0.25
		{0.26, 3}, // Excludes 0.25
		{0.50, 3}, // Includes 0.50
		{0.51, 2}, // Excludes 0.50
		{0.75, 2}, // Includes 0.75
		{0.76, 1}, // Only 0.95
		{0.95, 1}, // Includes 0.95
		{0.96, 0}, // None
		{1.0, 0},  // None
	}

	for _, tt := range tests {
		result := set.FilterByMinConfidence(tt.minConf)
		if len(result) != tt.want {
			t.Errorf("FilterByMinConfidence(%v) = %d items, want %d", tt.minConf, len(result), tt.want)
		}
	}
}

func TestSuggestionSet_HighConfidenceSuggestions(t *testing.T) {
	suggestions := []Suggestion{
		NewSuggestion(SuggestionMissingDependency, "B1", "S1", "R1", 0.95),
		NewSuggestion(SuggestionPotentialDuplicate, "B2", "S2", "R2", 0.70),
		NewSuggestion(SuggestionLabelSuggestion, "B3", "S3", "R3", 0.69),
		NewSuggestion(SuggestionCycleWarning, "B4", "S4", "R4", 0.30),
	}
	set := NewSuggestionSet(suggestions, "")

	high := set.HighConfidenceSuggestions()

	// Should include 0.95 and 0.70 (>= 0.7)
	if len(high) != 2 {
		t.Errorf("HighConfidenceSuggestions() = %d items, want 2", len(high))
	}

	for _, s := range high {
		if s.Confidence < ConfidenceThresholdHigh {
			t.Errorf("High confidence suggestion has low confidence: %v", s.Confidence)
		}
	}
}

func TestSuggestionSet_JSON(t *testing.T) {
	suggestions := []Suggestion{
		NewSuggestion(SuggestionMissingDependency, "B1", "S1", "R1", 0.9),
		NewSuggestion(SuggestionPotentialDuplicate, "B2", "S2", "R2", 0.5),
	}
	set := NewSuggestionSet(suggestions, "hash_abc")

	// Serialize
	data, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Deserialize
	var set2 SuggestionSet
	if err := json.Unmarshal(data, &set2); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Verify round-trip
	if len(set2.Suggestions) != 2 {
		t.Error("JSON round-trip: Suggestions count mismatch")
	}
	if set2.DataHash != "hash_abc" {
		t.Error("JSON round-trip: DataHash mismatch")
	}
	if set2.Stats.Total != 2 {
		t.Error("JSON round-trip: Stats.Total mismatch")
	}
}

func TestSuggestion_EdgeCases(t *testing.T) {
	// Empty strings
	s := NewSuggestion(SuggestionMissingDependency, "", "", "", 0.5)
	if s.TargetBead != "" {
		t.Error("Empty target bead should be preserved")
	}

	// Zero confidence
	s2 := NewSuggestion(SuggestionMissingDependency, "B1", "S", "R", 0.0)
	if s2.GetConfidenceLevel() != ConfidenceLow {
		t.Error("Zero confidence should be low")
	}

	// Negative confidence (edge case - shouldn't happen but test behavior)
	s3 := Suggestion{Confidence: -0.1}
	if s3.GetConfidenceLevel() != ConfidenceLow {
		t.Error("Negative confidence should be treated as low")
	}

	// Confidence > 1 (edge case)
	s4 := Suggestion{Confidence: 1.5}
	if s4.GetConfidenceLevel() != ConfidenceHigh {
		t.Error("Confidence > 1 should be treated as high")
	}

	// Very long strings
	longStr := make([]byte, 10000)
	for i := range longStr {
		longStr[i] = 'a'
	}
	s5 := NewSuggestion(SuggestionMissingDependency, string(longStr), string(longStr), string(longStr), 0.5)
	if len(s5.TargetBead) != 10000 {
		t.Error("Long strings should be preserved")
	}

	// Unicode in strings
	s6 := NewSuggestion(SuggestionMissingDependency, "BEAD-日本語", "サマリー", "理由", 0.5)
	if s6.TargetBead != "BEAD-日本語" {
		t.Error("Unicode in target bead should be preserved")
	}
}

func TestBeadsCommandArgumentQuoting(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
		ok   bool
	}{
		{name: "ordinary", id: "READY-1", want: "READY-1", ok: true},
		{name: "quoted", id: "team's item", want: "'team'\\''s item'", ok: true},
		{name: "option shaped", id: "--help", ok: false},
		{name: "newline", id: "READY-1\nprintf ignored", ok: false},
		{name: "nul", id: "READY-1\x00ignored", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := quoteBeadsCommandID(tt.id)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("quoteBeadsCommandID(%q) = (%q, %v), want (%q, %v)", tt.id, got, ok, tt.want, tt.ok)
			}
		})
	}

	if got, ok := quoteBeadsFlagValue("-needs-review"); !ok || got != "-needs-review" {
		t.Fatalf("flag value quoting = (%q, %v), want (-needs-review, true)", got, ok)
	}
	if _, ok := quoteBeadsFlagValue("unsafe\tlabel"); ok {
		t.Fatal("control-bearing label was accepted as a command value")
	}
}

// Benchmarks

func BenchmarkNewSuggestion(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewSuggestion(SuggestionMissingDependency, "BEAD-123", "Summary", "Reason", 0.85)
	}
}

func BenchmarkSuggestion_GetConfidenceLevel(b *testing.B) {
	s := Suggestion{Confidence: 0.65}
	for i := 0; i < b.N; i++ {
		_ = s.GetConfidenceLevel()
	}
}

func BenchmarkSuggestion_WithMetadata(b *testing.B) {
	s := NewSuggestion(SuggestionMissingDependency, "B1", "S", "R", 0.5)
	for i := 0; i < b.N; i++ {
		_ = s.WithMetadata("key", "value")
	}
}

func BenchmarkNewSuggestionSet_100(b *testing.B) {
	suggestions := make([]Suggestion, 100)
	for i := 0; i < 100; i++ {
		suggestions[i] = NewSuggestion(SuggestionMissingDependency, "B", "S", "R", float64(i)/100)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewSuggestionSet(suggestions, "hash")
	}
}

func BenchmarkSuggestionSet_FilterByType(b *testing.B) {
	suggestions := make([]Suggestion, 1000)
	types := []SuggestionType{SuggestionMissingDependency, SuggestionPotentialDuplicate, SuggestionLabelSuggestion}
	for i := 0; i < 1000; i++ {
		suggestions[i] = NewSuggestion(types[i%3], "B", "S", "R", 0.5)
	}
	set := NewSuggestionSet(suggestions, "hash")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = set.FilterByType(SuggestionMissingDependency)
	}
}

func BenchmarkSuggestionSet_FilterByMinConfidence(b *testing.B) {
	suggestions := make([]Suggestion, 1000)
	for i := 0; i < 1000; i++ {
		suggestions[i] = NewSuggestion(SuggestionMissingDependency, "B", "S", "R", float64(i)/1000)
	}
	set := NewSuggestionSet(suggestions, "hash")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = set.FilterByMinConfidence(0.5)
	}
}
