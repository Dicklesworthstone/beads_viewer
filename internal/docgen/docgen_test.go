package docgen_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/Dicklesworthstone/beads_viewer/internal/docgen"
	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
)

func TestDocgen_RenderEnvTable(t *testing.T) {
	table := docgen.RenderEnvTable(nil)
	if !strings.Contains(table, "| Variable | Description | Default |") {
		t.Errorf("missing header in env table")
	}
	for _, v := range env.All() {
		if !strings.Contains(table, "`"+v.Name+"`") {
			t.Errorf("env table missing variable %s", v.Name)
		}
	}
}

func TestDocgen_RenderAlertsTables(t *testing.T) {
	table := docgen.RenderAlertsTables(nil)
	for _, typ := range drift.AllAlertTypes() {
		if !strings.Contains(table, "`"+string(typ)+"`") {
			t.Errorf("alerts table missing alert type %s", typ)
		}
	}
}

func TestDocgen_RenderRecipesTable(t *testing.T) {
	table := docgen.RenderRecipesTable()
	for _, expected := range []string{"default", "actionable", "recent", "blocked", "high-impact", "stale", "triage", "closed", "release-cut", "quick-wins", "bottlenecks"} {
		if !strings.Contains(table, "`"+expected+"`") {
			t.Errorf("recipes table missing recipe %s", expected)
		}
	}
}

func TestDocgen_RenderPresetsTable(t *testing.T) {
	table := docgen.RenderPresetsTable()
	for _, preset := range []string{"default", "bug-hunting", "sprint-planning", "impact-first", "text-only"} {
		if !strings.Contains(table, "`"+preset+"`") {
			t.Errorf("presets table missing preset %s", preset)
		}
	}
}

func TestDocgen_RenderKeysTable(t *testing.T) {
	table := docgen.RenderKeysTable()
	if !strings.Contains(table, "| Context | Key | Action |") {
		t.Errorf("keys table missing header")
	}
}

func TestDocgen_GenerateConstantsJSON(t *testing.T) {
	data, err := docgen.GenerateConstantsJSON()
	if err != nil {
		t.Fatalf("failed to generate constants JSON: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}

	for _, key := range []string{"impact_weights", "label_health", "timeout_tiers", "staleness_thresholds", "correlation_ranges"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("constants JSON missing key %q", key)
		}
	}
}

func TestDocgen_RenderFlagsTable(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("db", "", "Database path")
	fs.Bool("robot-triage", false, "Triage flag")
	fs.String("cpu-profile", "", "Profile flag")

	table := docgen.RenderFlagsTable(fs, nil)
	if !strings.Contains(table, "`--db`") {
		t.Errorf("flags table missing --db")
	}
	if !strings.Contains(table, "`--robot-triage`") {
		t.Errorf("flags table missing --robot-triage")
	}
	if !strings.Contains(table, "`--cpu-profile`") {
		t.Errorf("flags table missing --cpu-profile")
	}
	if !strings.Contains(table, "Debug Flags") {
		t.Errorf("flags table missing Debug Flags group")
	}
}

func TestDocgen_ReplaceBetweenMarkers(t *testing.T) {
	input := `# Header

<!-- bv:generated:test -->
old content
<!-- /bv:generated -->

Footer`

	expected := `# Header

<!-- bv:generated:test -->
new content
<!-- /bv:generated -->

Footer`

	result, ok := docgen.ReplaceBetweenMarkers(input, "test", "new content")
	if !ok {
		t.Fatalf("ReplaceBetweenMarkers returned false")
	}
	if result != expected {
		t.Errorf("expected:\n%q\ngot:\n%q", expected, result)
	}

	// Test missing marker
	_, ok2 := docgen.ReplaceBetweenMarkers(input, "nonexistent", "new content")
	if ok2 {
		t.Errorf("expected false for nonexistent marker")
	}
}
