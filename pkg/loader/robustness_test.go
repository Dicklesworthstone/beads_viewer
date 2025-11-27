package loader_test

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

func TestLoadIssuesRobustness(t *testing.T) {
	// Create a temporary file with some bad lines
	f, err := os.CreateTemp("", "beads_robustness_*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	content := `{"id": "1", "title": "Good Issue", "status": "open", "priority": 1}
{INVALID JSON}
{"id": "2", "title": "Another Good Issue", "status": "closed", "priority": 2}
`
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()

	issues, err := loader.LoadIssuesFromFile(f.Name())
	if err != nil {
		t.Fatalf("Expected success even with bad lines, got error: %v", err)
	}

	if len(issues) != 2 {
		t.Errorf("Expected 2 valid issues, got %d", len(issues))
	}

	if issues[0].ID != "1" || issues[1].ID != "2" {
		t.Errorf("Issues loaded in incorrect order or content mismatch")
	}
}
