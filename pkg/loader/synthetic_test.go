package loader_test

import (
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

func TestLoadSyntheticComplex(t *testing.T) {
	f := "../../tests/testdata/synthetic_complex.jsonl"
	issues, err := loader.LoadIssuesFromFile(f)
	if err != nil {
		t.Fatalf("Failed to load synthetic data: %v", err)
	}

	if len(issues) != 6 {
		t.Errorf("Expected 6 issues, got %d", len(issues))
	}

	// Validate specific rich content
	foundMarkdown := false
	foundDeps := false
	for _, i := range issues {
		if i.ID == "bd-101" {
			if i.Assignee != "alice" {
				t.Errorf("bd-101 assignee wrong")
			}
			if len(i.Comments) != 1 {
				t.Errorf("bd-101 should have 1 comment")
			}
			foundMarkdown = true
		}
		if i.ID == "bd-103" {
			if len(i.Dependencies) != 1 {
				t.Errorf("bd-103 should have 1 dependency")
			}
			foundDeps = true
		}
	}

	if !foundMarkdown || !foundDeps {
		t.Error("Failed to validate rich content structure")
	}
}
