package export

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestSQLiteExporter_InsertsMetricsAndTriageRecommendations(t *testing.T) {
	now := time.Now().UTC()
	issues := []*model.Issue{
		{
			ID:          "A",
			Title:       "Issue A",
			Description: "A desc",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeTask,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "B",
			Title:       "Issue B",
			Description: "B desc",
			Status:      model.StatusOpen,
			Priority:    2,
			IssueType:   model.TypeTask,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	deps := []*model.Dependency{
		{
			IssueID:     "A",
			DependsOnID: "B",
			Type:        model.DepBlocks,
			CreatedAt:   now,
			CreatedBy:   "test",
		},
	}

	stats := analysis.NewGraphStatsForTest(
		map[string]float64{"A": 0.25, "B": 0.75}, // pageRank
		map[string]float64{"A": 0.10, "B": 0.20}, // betweenness
		nil, nil, nil,
		map[string]float64{"A": 2, "B": 1}, // criticalPathScore
		map[string]int{"A": 1, "B": 0},     // outDegree
		map[string]int{"A": 0, "B": 1},     // inDegree
		nil, 0, []string{"B", "A"},         // cycles, density, topo
	)

	triage := &analysis.TriageResult{
		Recommendations: []analysis.Recommendation{
			{
				ID:          "A",
				Title:       "Issue A",
				Type:        "task",
				Status:      "open",
				Priority:    1,
				Score:       0.9,
				Action:      "work",
				Reasons:     []string{"high impact", "unblocks B"},
				UnblocksIDs: []string{"X"},
				BlockedBy:   []string{"B"},
			},
		},
	}

	exporter := NewSQLiteExporter(issues, deps, stats, triage)
	exporter.Config.IncludeRobotOutputs = false

	outDir := t.TempDir()
	if err := exporter.Export(outDir); err != nil {
		t.Fatalf("Export returned error: %v", err)
	}

	dbPath := filepath.Join(outDir, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	var pr, bw, score float64
	var cp, blocks, blockedBy int
	row := db.QueryRow(`SELECT pagerank, betweenness, critical_path_depth, triage_score, blocks_count, blocked_by_count FROM issue_metrics WHERE issue_id = ?`, "A")
	if err := row.Scan(&pr, &bw, &cp, &score, &blocks, &blockedBy); err != nil {
		t.Fatalf("Scan issue_metrics for A: %v", err)
	}
	if math.Abs(pr-0.25) > 1e-9 {
		t.Fatalf("pagerank expected 0.25, got %v", pr)
	}
	if math.Abs(bw-0.10) > 1e-9 {
		t.Fatalf("betweenness expected 0.10, got %v", bw)
	}
	if cp != 2 {
		t.Fatalf("critical_path_depth expected 2, got %d", cp)
	}
	if math.Abs(score-0.9) > 1e-9 {
		t.Fatalf("triage_score expected 0.9, got %v", score)
	}
	// A depends on B, so A is blocked by B (blocks=0, blockedBy=1)
	if blocks != 0 {
		t.Fatalf("blocks_count for A expected 0, got %d", blocks)
	}
	if blockedBy != 1 {
		t.Fatalf("blocked_by_count for A expected 1, got %d", blockedBy)
	}

	row = db.QueryRow(`SELECT triage_score, blocks_count, blocked_by_count FROM issue_metrics WHERE issue_id = ?`, "B")
	if err := row.Scan(&score, &blocks, &blockedBy); err != nil {
		t.Fatalf("Scan issue_metrics for B: %v", err)
	}
	if math.Abs(score-0.0) > 1e-9 {
		t.Fatalf("triage_score for B expected 0.0, got %v", score)
	}
	// A depends on B, so B blocks A (blocks=1, blockedBy=0)
	if blocks != 1 {
		t.Fatalf("blocks_count for B expected 1, got %d", blocks)
	}
	if blockedBy != 0 {
		t.Fatalf("blocked_by_count for B expected 0, got %d", blockedBy)
	}

	var action string
	var reasonsJSON, unblocksJSON, blockedByJSON string
	row = db.QueryRow(`SELECT action, reasons, unblocks_ids, blocked_by_ids FROM triage_recommendations WHERE issue_id = ?`, "A")
	if err := row.Scan(&action, &reasonsJSON, &unblocksJSON, &blockedByJSON); err != nil {
		t.Fatalf("Scan triage_recommendations for A: %v", err)
	}
	if action != "work" {
		t.Fatalf("action expected %q, got %q", "work", action)
	}
	var reasons []string
	if err := json.Unmarshal([]byte(reasonsJSON), &reasons); err != nil {
		t.Fatalf("Unmarshal reasons: %v", err)
	}
	if len(reasons) != 2 {
		t.Fatalf("expected 2 reasons, got %d", len(reasons))
	}
	var unblocks []string
	if err := json.Unmarshal([]byte(unblocksJSON), &unblocks); err != nil {
		t.Fatalf("Unmarshal unblocks_ids: %v", err)
	}
	if len(unblocks) != 1 || unblocks[0] != "X" {
		t.Fatalf("unexpected unblocks_ids: %+v", unblocks)
	}
	var blockedByIDs []string
	if err := json.Unmarshal([]byte(blockedByJSON), &blockedByIDs); err != nil {
		t.Fatalf("Unmarshal blocked_by_ids: %v", err)
	}
	if len(blockedByIDs) != 1 || blockedByIDs[0] != "B" {
		t.Fatalf("unexpected blocked_by_ids: %+v", blockedByIDs)
	}
}

// Exported ID lists must agree with counts for every blocking type and both
// endpoint lifecycles; raw informational relationships still survive export.
func TestSQLiteExporter_BlockingVocabularyMatchesOverview(t *testing.T) {
	for _, edge := range []struct {
		kind     model.DependencyType
		blocking bool
	}{{"", true}, {model.DepBlocks, true}, {model.DepConditionalBlocks, true}, {model.DepWaitsFor, true},
		{model.DepRelated, false}, {model.DepParentChild, false}, {"relates-to", false}, {"team-reference", false}} {
		for _, state := range []struct {
			name               string
			blocker, dependent model.Status
			active             bool
		}{{"active", model.StatusOpen, model.StatusOpen, true},
			{"closed-blocker", model.StatusClosed, model.StatusOpen, false},
			{"tombstone-blocker", model.StatusTombstone, model.StatusOpen, false},
			{"closed-dependent", model.StatusOpen, model.StatusClosed, false},
			{"tombstone-dependent", model.StatusOpen, model.StatusTombstone, false}} {
			t.Run(string(edge.kind)+"/"+state.name, func(t *testing.T) {
				issues := []*model.Issue{
					makeTestIssue("blocker", "Prerequisite", state.blocker, 1, model.TypeTask),
					makeTestIssue("dependent", "Dependent", state.dependent, 1, model.TypeTask),
				}
				deps := []*model.Dependency{{IssueID: "dependent", DependsOnID: "blocker", Type: edge.kind}}
				issues[1].Dependencies = deps
				stats := analysis.NewAnalyzer([]model.Issue{*issues[0], *issues[1]}).Analyze()
				stats.WaitForPhase2()
				out := t.TempDir()
				if err := NewSQLiteExporter(issues, deps, &stats, nil).Export(out); err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", filepath.Join(out, "beads.sqlite3"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				var kind string
				if err := db.QueryRow(`SELECT type FROM dependencies`).Scan(&kind); err != nil || kind != string(edge.kind) {
					t.Fatalf("raw relationship changed: kind=%q err=%v", kind, err)
				}
				want := 0
				if edge.blocking && state.active {
					want = 1
				}
				for _, endpoint := range []struct{ id, query, counterpart string }{
					{"blocker", `SELECT dependent_count, blocks_ids FROM issue_overview_mv WHERE id = ?`, "dependent"},
					{"dependent", `SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = ?`, "blocker"},
				} {
					var count int
					var ids sql.NullString
					if err := db.QueryRow(endpoint.query, endpoint.id).Scan(&count, &ids); err != nil {
						t.Fatal(err)
					}
					wantIDs := ""
					if want == 1 {
						wantIDs = endpoint.counterpart
					}
					if count != want || ids.String != wantIDs {
						t.Errorf("type=%q states=%s/%s endpoint=%s count=%d ids=%q; want count=%d ids=%q", edge.kind, state.blocker, state.dependent, endpoint.id, count, ids.String, want, wantIDs)
					}
				}
			})
		}
	}
}

// Closing a blocker drops its dependent's count and active ID list together.
func TestSQLiteExporter_ResolvedBlockerExcludedFromCounts(t *testing.T) {
	now := time.Now().UTC()
	issues := []*model.Issue{
		// A is open and historically depends on B
		{
			ID: "A", Title: "A", Status: model.StatusOpen, Priority: 1,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
		// B was the blocker, but is now closed — it should NOT count
		// against A's blocked_by_count anymore.
		{
			ID: "B", Title: "B", Status: model.StatusClosed, Priority: 2,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
		// C still actively blocks A — should count.
		{
			ID: "C", Title: "C", Status: model.StatusOpen, Priority: 2,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
	}
	deps := []*model.Dependency{
		{IssueID: "A", DependsOnID: "B", Type: model.DepBlocks, CreatedAt: now, CreatedBy: "test"},
		{IssueID: "A", DependsOnID: "C", Type: model.DepBlocks, CreatedAt: now, CreatedBy: "test"},
	}

	stats := analysis.NewGraphStatsForTest(
		map[string]float64{"A": 0.3, "B": 0.3, "C": 0.4},
		map[string]float64{"A": 0, "B": 0, "C": 0},
		nil, nil, nil,
		map[string]float64{"A": 1, "B": 0, "C": 0},
		map[string]int{"A": 2, "B": 0, "C": 0},
		map[string]int{"A": 0, "B": 1, "C": 1},
		nil, 0, []string{"B", "C", "A"},
	)

	exporter := NewSQLiteExporter(issues, deps, stats, nil)
	exporter.Config.IncludeRobotOutputs = false

	outDir := t.TempDir()
	if err := exporter.Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}

	dbPath := filepath.Join(outDir, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// In issue_metrics: A is blocked by 1 active blocker (C only — B is closed)
	var blockedByA, blocksA int
	if err := db.QueryRow(
		`SELECT blocks_count, blocked_by_count FROM issue_metrics WHERE issue_id = ?`, "A",
	).Scan(&blocksA, &blockedByA); err != nil {
		t.Fatalf("Scan A: %v", err)
	}
	if blockedByA != 1 {
		t.Fatalf("A.blocked_by_count: closed B should be excluded, expected 1 (C only), got %d", blockedByA)
	}

	// B is closed: it no longer blocks A.
	var blocksB int
	if err := db.QueryRow(
		`SELECT blocks_count FROM issue_metrics WHERE issue_id = ?`, "B",
	).Scan(&blocksB); err != nil {
		t.Fatalf("Scan B: %v", err)
	}
	if blocksB != 0 {
		t.Fatalf("B.blocks_count: closed B should not block anything, expected 0, got %d", blocksB)
	}

	// C still blocks A.
	var blocksC int
	if err := db.QueryRow(
		`SELECT blocks_count FROM issue_metrics WHERE issue_id = ?`, "C",
	).Scan(&blocksC); err != nil {
		t.Fatalf("Scan C: %v", err)
	}
	if blocksC != 1 {
		t.Fatalf("C.blocks_count: should still block A, expected 1, got %d", blocksC)
	}

	// Materialized view: A's blocked_by_ids list excludes B, includes C.
	var blockedByIDs sql.NullString
	if err := db.QueryRow(
		`SELECT blocked_by_ids FROM issue_overview_mv WHERE id = ?`, "A",
	).Scan(&blockedByIDs); err != nil {
		t.Fatalf("Scan mv A: %v", err)
	}
	if !blockedByIDs.Valid || blockedByIDs.String != "C" {
		t.Fatalf("A.blocked_by_ids in mv: expected only \"C\" (closed B excluded), got valid=%v value=%q",
			blockedByIDs.Valid, blockedByIDs.String)
	}
}

// TestSQLiteExporter_DanglingDepIgnored locks the consistency contract
// between the Go-side issue_metrics counts and the SQL-side
// issue_overview_mv id lists: a dependency edge whose other endpoint
// isn't in the export's issue universe must be excluded from both
// surfaces. Pre-fix, the materialized view's `JOIN issues` dropped
// dangling refs but the Go counter happily counted them, so an issue
// could end up with `blocked_by_count = 2` while
// `blocked_by_ids = "C"` (length 1) for the same row.
func TestSQLiteExporter_DanglingDepIgnored(t *testing.T) {
	now := time.Now().UTC()
	issues := []*model.Issue{
		{
			ID: "A", Title: "A", Status: model.StatusOpen, Priority: 1,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "C", Title: "C", Status: model.StatusOpen, Priority: 2,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
	}
	deps := []*model.Dependency{
		// dangling: GHOST is referenced but not present in `issues`.
		{IssueID: "A", DependsOnID: "GHOST", Type: model.DepBlocks, CreatedAt: now, CreatedBy: "test"},
		// real edge: should still count.
		{IssueID: "A", DependsOnID: "C", Type: model.DepBlocks, CreatedAt: now, CreatedBy: "test"},
	}

	stats := analysis.NewGraphStatsForTest(
		map[string]float64{"A": 0.5, "C": 0.5},
		map[string]float64{"A": 0, "C": 0},
		nil, nil, nil,
		map[string]float64{"A": 1, "C": 0},
		map[string]int{"A": 2, "C": 0},
		map[string]int{"A": 0, "C": 1},
		nil, 0, []string{"C", "A"},
	)

	exporter := NewSQLiteExporter(issues, deps, stats, nil)
	exporter.Config.IncludeRobotOutputs = false
	outDir := t.TempDir()
	if err := exporter.Export(outDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	dbPath := filepath.Join(outDir, "beads.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var blockedByA int
	if err := db.QueryRow(
		`SELECT blocked_by_count FROM issue_metrics WHERE issue_id = ?`, "A",
	).Scan(&blockedByA); err != nil {
		t.Fatalf("Scan A metrics: %v", err)
	}
	if blockedByA != 1 {
		t.Fatalf("A.blocked_by_count: dangling GHOST must be excluded, expected 1 (C only), got %d", blockedByA)
	}

	var ids sql.NullString
	if err := db.QueryRow(
		`SELECT blocked_by_ids FROM issue_overview_mv WHERE id = ?`, "A",
	).Scan(&ids); err != nil {
		t.Fatalf("Scan mv: %v", err)
	}
	if !ids.Valid || ids.String != "C" {
		t.Fatalf("blocked_by_ids in mv: expected \"C\", got valid=%v value=%q", ids.Valid, ids.String)
	}
}

func TestSQLiteExporter_writeRobotOutputs_WritesExpectedFiles(t *testing.T) {
	now := time.Now().UTC()
	issues := []*model.Issue{{
		ID:          "A",
		Title:       "Issue A",
		Description: "A desc",
		Status:      model.StatusOpen,
		Priority:    1,
		IssueType:   model.TypeTask,
		CreatedAt:   now,
		UpdatedAt:   now,
	}}

	triage := &analysis.TriageResult{
		ProjectHealth: analysis.ProjectHealth{
			Counts: analysis.HealthCounts{
				Total: 1,
				Open:  1,
			},
		},
		Recommendations: []analysis.Recommendation{
			{
				ID:     "A",
				Title:  "Issue A",
				Score:  0.5,
				Action: "work",
			},
		},
	}

	exporter := NewSQLiteExporter(issues, nil, (*analysis.GraphStats)(nil), triage)
	exporter.Config.Title = "Test Title"
	exporter.SetGitHash("deadbeef")

	dataDir := t.TempDir()
	if err := exporter.writeRobotOutputs(openTestBundle(t, dataDir), "."); err != nil {
		t.Fatalf("writeRobotOutputs returned error: %v", err)
	}

	for _, name := range []string{"triage.json", "project_health.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err != nil {
			t.Fatalf("Expected %s to exist: %v", name, err)
		}
	}
}
