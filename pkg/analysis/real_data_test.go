package analysis_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// Helper to parse JSONL directly
func loadRealIssues(t *testing.T, filename string) []model.Issue {
	// Tests run from pkg/analysis, so root is ../..
	path := filepath.Join("..", "..", "tests", "testdata", "real", filename)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read test data %s: %v", filename, err)
	}

	var issues []model.Issue
	scanner := bufio.NewScanner(bytes.NewReader(content))
	// Increase buffer for large lines
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var issue model.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			// Skip comments or malformed lines in test data if any (though real data should be valid)
			continue
		}
		issues = append(issues, issue)
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Scanner error: %v", err)
	}

	return issues
}

func TestRealData_Cass(t *testing.T) {
	issues := loadRealIssues(t, "cass.jsonl")
	if len(issues) == 0 {
		t.Fatal("Failed to load issues from cass.jsonl")
	}

	// 1. Run Analysis
	an := analysis.NewAnalyzer(issues)
	stats := an.Analyze()

	// Assert basic stats
	if len(stats.PageRank) != len(issues) {
		t.Errorf("PageRank count mismatch: got %d, want %d", len(stats.PageRank), len(issues))
	}

	// 2. Generate Execution Plan
	plan := an.GetExecutionPlan()

	// Cass data has dependencies, so we expect some structure
	if plan.TotalActionable == 0 && plan.TotalBlocked == 0 {
		t.Error("Plan is empty (no actionable or blocked)")
	}

	// 3. Check for specific known structures if possible, or just sanity check
	// Ensure tracks are valid
	for _, track := range plan.Tracks {
		if len(track.Items) == 0 {
			t.Error("Found empty track in plan")
		}
		for _, item := range track.Items {
			if item.ID == "" {
				t.Error("Plan item has empty ID")
			}
		}
	}

	// 4. Priority Recommendations
	recs := an.GenerateRecommendations()
	// We expect at least some recommendations in a real project
	// (unless everything is perfectly prioritized)
	if len(recs) == 0 {
		t.Log("No priority recommendations found for cass.jsonl (this might be valid)")
	} else {
		for _, rec := range recs {
			if rec.Confidence < 0 || rec.Confidence > 1 {
				t.Errorf("Invalid confidence score: %f", rec.Confidence)
			}
		}
	}
}

func TestRealData_Srps(t *testing.T) {
	issues := loadRealIssues(t, "srps.jsonl")
	if len(issues) == 0 {
		t.Fatal("Failed to load issues from srps.jsonl")
	}

	an := analysis.NewAnalyzer(issues)

	// 1. Check Impact Scores
	scores := an.ComputeImpactScores()
	if len(scores) == 0 {
		// Might be empty if all closed?
		openCount := 0
		for _, i := range issues {
			if i.Status != model.StatusClosed {
				openCount++
			}
		}
		if openCount > 0 {
			t.Error("Expected impact scores for open issues")
		}
	}

	// 2. Check for cycles (Srps might have cycles?)
	stats := an.Analyze()
	if len(stats.Cycles) > 0 {
		t.Logf("Found %d cycles in srps.jsonl", len(stats.Cycles))
	}
}

func TestRealData_ProjectBeads(t *testing.T) {
	// Try to load the project's own beads file from root .beads/beads.jsonl
	// This makes the test depend on the repo state, which is good for "eating your own dogfood"
	// but might be flaky if the repo state is broken. We'll soft-fail or skip if file missing.

	path := filepath.Join("..", "..", ".beads", "beads.jsonl")
	// Also check issues.jsonl or beads.base.jsonl
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join("..", "..", ".beads", "issues.jsonl")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join("..", "..", ".beads", "beads.base.jsonl")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("Project .beads file not found, skipping self-test")
	}

	// Read file manually
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read project beads: %v", err)
	}

	var issues []model.Issue
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var issue model.Issue
		if err := json.Unmarshal(line, &issue); err == nil {
			issues = append(issues, issue)
		}
	}

	if len(issues) == 0 {
		t.Skip("Project beads file is empty")
	}

	// Analyze our own project
	an := analysis.NewAnalyzer(issues)
	plan := an.GetExecutionPlan()

	t.Logf("Project Self-Analysis: %d issues, %d actionable, %d blocked, %d tracks",
		len(issues), plan.TotalActionable, plan.TotalBlocked, len(plan.Tracks))

	if plan.Summary.HighestImpact != "" {

		t.Logf("Highest Impact: %s (Unblocks %d)",

			plan.Summary.HighestImpact, plan.Summary.UnblocksCount)

	}

}

func TestRealData_Combined(t *testing.T) {

	// Load all available real data

	cass := loadRealIssues(t, "cass.jsonl")

	srps := loadRealIssues(t, "srps.jsonl")

	// Load project beads if available

	var project []model.Issue

	path := filepath.Join("..", "..", ".beads", "beads.jsonl")

	if _, err := os.Stat(path); err == nil {

		content, _ := os.ReadFile(path)

		scanner := bufio.NewScanner(bytes.NewReader(content))

		for scanner.Scan() {

			var issue model.Issue

			if err := json.Unmarshal(scanner.Bytes(), &issue); err == nil {

				project = append(project, issue)

			}

		}

	}

	// Combine all

	var combined []model.Issue

	combined = append(combined, cass...)

	combined = append(combined, srps...)

	combined = append(combined, project...)

	if len(combined) == 0 {

		t.Fatal("No real data loaded for combined test")

	}

	// Analyze aggregate

	an := analysis.NewAnalyzer(combined)

	stats := an.Analyze()

	plan := an.GetExecutionPlan()

	t.Logf("Combined Analysis: %d issues total", len(combined))

	t.Logf("- Actionable: %d", plan.TotalActionable)

	t.Logf("- Blocked: %d", plan.TotalBlocked)

	t.Logf("- Tracks: %d", len(plan.Tracks))

	t.Logf("- Cycles: %d", len(stats.Cycles))

	if len(stats.Cycles) > 0 {

		for i, cycle := range stats.Cycles {

			t.Logf("  Cycle %d: %v", i+1, cycle)

		}

	}

	// Assert robustness

	// The analysis shouldn't panic even with disjoint IDs (from different projects)

	// or potential ID collisions (if any)

	if len(stats.PageRank) != len(combined) {

		// Note: PageRank might be missing for isolated nodes if the algo filters them?

		// No, PageRank typically covers all nodes.

		// Actually, our Analyzer implementation initializes all nodes.

		// But duplicate IDs (collisions) would merge.

		// Let's check for unique IDs count

		uniqueIDs := make(map[string]bool)

		for _, i := range combined {

			uniqueIDs[i.ID] = true

		}

		if len(stats.PageRank) != len(uniqueIDs) {

			t.Errorf("PageRank count %d != Unique Issue Count %d", len(stats.PageRank), len(uniqueIDs))

		}

	}

}
