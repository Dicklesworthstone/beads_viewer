package loader_test

import (
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

func TestLoadRealIssuesBenchmark(t *testing.T) {
	// Look for files in tests/testdata/real
	files, _ := filepath.Glob("../../tests/testdata/real/*.jsonl")

	if len(files) == 0 {
		t.Skip("No real test data found in tests/testdata/real/")
	}

	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			issues, err := loader.LoadIssuesFromFile(f)
			if err != nil {
				t.Fatalf("Failed to load %s: %v", f, err)
			}
			if len(issues) == 0 {
				t.Fatalf("Expected issues in %s, got 0", f)
			}
			t.Logf("Loaded %d issues from %s", len(issues), f)

			// Validate content of random issue
			first := issues[0]
			if first.ID == "" {
				t.Error("Issue missing ID")
			}
		})
	}
}

func BenchmarkLoadLargeFile(b *testing.B) {
	// Setup a large synthetic file if real ones aren't huge enough,
	// but for now we assume the real ones exist for the purpose of this specific request
	files, _ := filepath.Glob("../../tests/testdata/real/*.jsonl")
	if len(files) == 0 {
		b.Skip("No real test data found")
	}
	f := files[0]

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = loader.LoadIssuesFromFile(f)
	}
}
