package loader_test

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

func TestLoadRealIssues(t *testing.T) {
	files := []string{
		"../../tests/testdata/srps_issues.jsonl",
		"../../tests/testdata/cass_issues.jsonl",
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				t.Skipf("Test file %s not found, skipping", f)
			}

			issues, err := loader.LoadIssuesFromFile(f)
			if err != nil {
				t.Fatalf("Failed to load %s: %v", f, err)
			}
			if len(issues) == 0 {
				t.Fatalf("Expected issues in %s, got 0", f)
			}
			t.Logf("Loaded %d issues from %s", len(issues), f)

			// Basic validation of fields
			for _, issue := range issues {
				if issue.ID == "" {
					t.Errorf("Issue missing ID")
				}
				if issue.Title == "" {
					t.Errorf("Issue %s missing Title", issue.ID)
				}
			}
		})
	}
}
