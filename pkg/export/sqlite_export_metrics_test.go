package export

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"golang.org/x/text/unicode/norm"
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
				for _, endpoint := range []struct {
					id          string
					status      model.Status
					query       string
					counterpart string
				}{
					{"blocker", state.blocker, `SELECT dependent_count, blocks_ids FROM issue_overview_mv WHERE id = ?`, "dependent"},
					{"dependent", state.dependent, `SELECT blocker_count, blocked_by_ids FROM issue_overview_mv WHERE id = ?`, "blocker"},
				} {
					var count int
					var ids sql.NullString
					err := db.QueryRow(endpoint.query, endpoint.id).Scan(&count, &ids)
					if endpoint.status == model.StatusTombstone {
						if err != sql.ErrNoRows {
							t.Fatalf("tombstone endpoint %s leaked into issue_overview_mv: err=%v count=%d ids=%q", endpoint.id, err, count, ids.String)
						}
						var rawStatus string
						if err := db.QueryRow(`SELECT status FROM issues WHERE id = ?`, endpoint.id).Scan(&rawStatus); err != nil || rawStatus != string(model.StatusTombstone) {
							t.Fatalf("tombstone endpoint %s missing from issues table: status=%q err=%v", endpoint.id, rawStatus, err)
						}
						continue
					}
					if err != nil {
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

func hasLetterOrNumberRune(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func escapeSQLiteLikeForTest(raw string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(raw)
}

func buildFTSMatchQueryForTest(raw string) string {
	normalized := norm.NFC.String(strings.TrimSpace(raw))
	if normalized == "" {
		return ""
	}
	var parts []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		candidate := cur.String()
		cur.Reset()
		if hasLetterOrNumberRune(candidate) {
			parts = append(parts, candidate)
		}
	}
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r) || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	if len(parts) == 0 {
		return ""
	}
	quoted := make([]string, len(parts))
	for i, part := range parts {
		quoted[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"*`
	}
	return strings.Join(quoted, " ")
}

func TestSQLiteExporter_EigenvectorBreakdownFTSAndHybridWasmMeta(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	extRef := "GH-4242"
	issues := []*model.Issue{
		{
			ID:                 "root",
			Title:              "Gateway root",
			Description:        "Primary auth gateway",
			Notes:              "obsidian-note-probe naïve-über हिंदी unicode_word",
			Design:             "design-arch-v2",
			AcceptanceCriteria: "ac-criterion-7",
			ExternalRef:        &extRef,
			Status:             model.StatusOpen,
			Priority:           0,
			IssueType:          model.TypeBug,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		{
			ID:          "leaf",
			Title:       "Downstream consumer",
			Description: "Depends on root and has non-blocking metadata edges",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeFeature,
			CreatedAt:   now,
			UpdatedAt:   now,
			Dependencies: []*model.Dependency{
				{IssueID: "leaf", DependsOnID: "root", Type: model.DepBlocks, CreatedAt: now},
				{IssueID: "leaf", DependsOnID: "epic", Type: model.DepParentChild, CreatedAt: now},
				{IssueID: "leaf", DependsOnID: "rel", Type: model.DepRelated, CreatedAt: now},
				{IssueID: "leaf", DependsOnID: "disc", Type: model.DepDiscoveredFrom, CreatedAt: now},
				{IssueID: "leaf", DependsOnID: "closed-blk", Type: model.DepBlocks, CreatedAt: now},
				{IssueID: "leaf", DependsOnID: "tomb-blk", Type: model.DepBlocks, CreatedAt: now},
			},
		},
		{ID: "epic", Title: "Parent epic", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeEpic, CreatedAt: now, UpdatedAt: now},
		{ID: "rel", Title: "Related task", Notes: "foo___bar literal underscore marker", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "disc", Title: "Discovered origin", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "closed-blk", Title: "Resolved blocker", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
		{ID: "tomb-blk", Title: "Tombstoned blocker", Status: model.StatusTombstone, Priority: 2, IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now},
	}

	var valueIssues []model.Issue
	var allDeps []*model.Dependency
	for _, iss := range issues {
		valueIssues = append(valueIssues, *iss)
		allDeps = append(allDeps, iss.Dependencies...)
	}

	stats := analysis.NewAnalyzer(valueIssues).Analyze()
	stats.WaitForPhase2()
	triage := analysis.ComputeTriageWithOptionsAndTime(valueIssues, analysis.TriageOptions{
		UseFastConfig: true,
		WaitForPhase2: true,
	}, now)

	outDir := t.TempDir()
	exporter := NewSQLiteExporter(issues, allDeps, &stats, &triage)
	if err := exporter.Export(outDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Open sqlite db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 1. Eigenvector populated in issue_metrics and exposed in issue_overview_mv.
	var metricEV, mvEV float64
	if err := db.QueryRow(`SELECT eigenvector FROM issue_metrics WHERE issue_id = ?`, "root").Scan(&metricEV); err != nil {
		t.Fatalf("Scan issue_metrics.eigenvector: %v", err)
	}
	if err := db.QueryRow(`SELECT eigenvector FROM issue_overview_mv WHERE id = ?`, "root").Scan(&mvEV); err != nil {
		t.Fatalf("Scan issue_overview_mv.eigenvector: %v", err)
	}
	if metricEV <= 0 || math.Abs(metricEV-mvEV) > 1e-9 {
		t.Fatalf("expected positive matching eigenvector for root, got metric=%v mv=%v", metricEV, mvEV)
	}

	// 2. Triage breakdown JSON persisted in triage_recommendations.breakdown.
	var breakdownRaw string
	if err := db.QueryRow(`SELECT breakdown FROM triage_recommendations WHERE issue_id = ?`, "root").Scan(&breakdownRaw); err != nil {
		t.Fatalf("Scan triage_recommendations.breakdown: %v", err)
	}
	var breakdown map[string]any
	if err := json.Unmarshal([]byte(breakdownRaw), &breakdown); err != nil {
		t.Fatalf("Unmarshal triage breakdown %q: %v", breakdownRaw, err)
	}
	if prVal, ok := breakdown["pagerank"].(float64); !ok || prVal <= 0 {
		t.Fatalf("expected positive pagerank float in triage breakdown JSON, got %v", breakdown)
	}

	// 3. FTS5 indexes notes, design, acceptance_criteria, and external_ref with safe hyphenated + Unicode NFC/combining-mark MATCH queries & snippet(..., -1, ...).
	if got := buildFTSMatchQueryForTest("naïve-über"); got != `"naïve"* "über"*` {
		t.Fatalf("buildFTSMatchQueryForTest(naïve-über) = %q, want %q", got, `"naïve"* "über"*`)
	}
	if got := buildFTSMatchQueryForTest("nai\u0308ve"); got != `"naïve"*` {
		t.Fatalf("buildFTSMatchQueryForTest(NFD nai\\u0308ve) = %q, want %q", got, `"naïve"*`)
	}
	if got := buildFTSMatchQueryForTest("हिंदी"); got != `"हिंदी"*` {
		t.Fatalf("buildFTSMatchQueryForTest(हिंदी) = %q, want %q", got, `"हिंदी"*`)
	}
	if got := buildFTSMatchQueryForTest("___"); got != "" {
		t.Fatalf("buildFTSMatchQueryForTest(___) = %q, want empty string for LIKE fallback", got)
	}
	if got := buildFTSMatchQueryForTest("\u0308"); got != "" {
		t.Fatalf("buildFTSMatchQueryForTest(isolated combining mark) = %q, want empty string", got)
	}
	for _, rawQuery := range []string{"obsidian-note-probe", "GH-4242", "design-arch-v2", "ac-criterion-7", "naïve", "nai\u0308ve", "über", "naïve-über", "हिंदी"} {
		ftsQuery := buildFTSMatchQueryForTest(rawQuery)
		var matchedID, snippetText string
		if err := db.QueryRow(`SELECT id, snippet(issues_fts, -1, '<mark>', '</mark>', '...', 32) FROM issues_fts WHERE issues_fts MATCH ?`, ftsQuery).Scan(&matchedID, &snippetText); err != nil {
			t.Fatalf("FTS5 query %q (MATCH %q) failed: %v", rawQuery, ftsQuery, err)
		}
		if matchedID != "root" {
			t.Fatalf("FTS5 query %q matched %q, want root", rawQuery, matchedID)
		}
		if !strings.Contains(snippetText, "<mark>") {
			t.Fatalf("FTS5 snippet(..., -1, ...) for %q did not highlight matched column: %q", rawQuery, snippetText)
		}
	}
	// Underscore-only query returns "" from buildFTSMatchQueryForTest and triggers escaped LIKE fallback matching only "foo___bar".
	var likeMatchedIDs string
	if err := db.QueryRow(`SELECT COALESCE(GROUP_CONCAT(id), '') FROM issue_overview_mv WHERE COALESCE(notes, '') LIKE ? ESCAPE '\'`, "%"+escapeSQLiteLikeForTest("___")+"%").Scan(&likeMatchedIDs); err != nil {
		t.Fatalf("Underscore LIKE fallback query failed: %v", err)
	}
	if likeMatchedIDs != "rel" {
		t.Fatalf("Underscore LIKE fallback matched %q, want %q", likeMatchedIDs, "rel")
	}
	// Adversarial FTS5 operator/colon/hyphen query must never error.
	var advCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues_fts WHERE issues_fts MATCH ?`, buildFTSMatchQueryForTest("bv-123 AND NOT foo:bar")).Scan(&advCount); err != nil {
		t.Fatalf("Adversarial FTS5 query failed: %v", err)
	}

	// 3b. issue_overview_mv excludes tombstones while issues table preserves them; GetExportedIssues matches active blocking counts & eigenvector.
	var mvTombCount, rawTombCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_overview_mv WHERE id = 'tomb-blk' OR status = 'tombstone'`).Scan(&mvTombCount); err != nil {
		t.Fatalf("Query issue_overview_mv tombstone count: %v", err)
	}
	if mvTombCount != 0 {
		t.Fatalf("issue_overview_mv tombstone count = %d, want 0", mvTombCount)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE id = 'tomb-blk' AND status = 'tombstone'`).Scan(&rawTombCount); err != nil {
		t.Fatalf("Query issues tombstone count: %v", err)
	}
	if rawTombCount != 1 {
		t.Fatalf("issues tombstone count = %d, want 1", rawTombCount)
	}

	exportedIssues := exporter.GetExportedIssues()
	expByID := make(map[string]ExportIssue, len(exportedIssues))
	for _, ei := range exportedIssues {
		expByID[ei.ID] = ei
	}
	if math.Abs(expByID["root"].Eigenvector-metricEV) > 1e-9 || expByID["root"].Eigenvector <= 0 {
		t.Fatalf("GetExportedIssues root.Eigenvector = %v, want %v", expByID["root"].Eigenvector, metricEV)
	}
	if expByID["leaf"].BlockedByCount != 1 {
		t.Fatalf("GetExportedIssues leaf.BlockedByCount = %d, want 1 (active blocking only)", expByID["leaf"].BlockedByCount)
	}
	for _, nonBlockingID := range []string{"epic", "rel", "disc", "closed-blk", "tomb-blk"} {
		if expByID[nonBlockingID].BlocksCount != 0 {
			t.Fatalf("GetExportedIssues %s.BlocksCount = %d, want 0", nonBlockingID, expByID[nonBlockingID].BlocksCount)
		}
	}

	// 4. Non-blocking and resolved edges do NOT inflate blocker_count / dependent_count (even without issue_metrics).
	noMetricsOut := t.TempDir()
	noMetricsExp := NewSQLiteExporter(issues, allDeps, nil, nil)
	if err := noMetricsExp.Export(noMetricsOut); err != nil {
		t.Fatalf("Export without metrics failed: %v", err)
	}
	noMetricsDB, err := sql.Open("sqlite", filepath.Join(noMetricsOut, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Open noMetricsDB: %v", err)
	}
	t.Cleanup(func() { _ = noMetricsDB.Close() })

	var leafBlockers, epicDependents, relDependents, discDependents int
	if err := noMetricsDB.QueryRow(`SELECT blocker_count FROM issue_overview_mv WHERE id = 'leaf'`).Scan(&leafBlockers); err != nil {
		t.Fatalf("Scan leaf blocker_count: %v", err)
	}
	if leafBlockers != 1 {
		t.Fatalf("leaf blocker_count = %d, want 1 (only active 'root' blocks edge)", leafBlockers)
	}
	for id, gotPtr := range map[string]*int{"epic": &epicDependents, "rel": &relDependents, "disc": &discDependents} {
		if err := noMetricsDB.QueryRow(`SELECT dependent_count FROM issue_overview_mv WHERE id = ?`, id).Scan(gotPtr); err != nil {
			t.Fatalf("Scan %s dependent_count: %v", id, err)
		}
		if *gotPtr != 0 {
			t.Fatalf("%s dependent_count = %d, want 0 for non-blocking relationship", id, *gotPtr)
		}
	}

	// 5. has_hybrid_wasm written to export_meta and data/meta.json (false by default, true when bundled).
	var metaDBVal string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'has_hybrid_wasm'`).Scan(&metaDBVal); err != nil {
		t.Fatalf("Scan export_meta has_hybrid_wasm: %v", err)
	}
	if metaDBVal != "false" {
		t.Fatalf("export_meta has_hybrid_wasm = %q, want \"false\"", metaDBVal)
	}
	metaBytes, err := os.ReadFile(filepath.Join(outDir, "data", "meta.json"))
	if err != nil {
		t.Fatalf("Read data/meta.json: %v", err)
	}
	var metaPayload ExportMeta
	if err := json.Unmarshal(metaBytes, &metaPayload); err != nil {
		t.Fatalf("Unmarshal data/meta.json: %v", err)
	}
	if metaPayload.HasHybridWasm {
		t.Fatalf("meta.json has_hybrid_wasm = true, want false when wasm/bv_hybrid_scorer.js is absent")
	}

	withHybridOut := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withHybridOut, "wasm"), 0o755); err != nil {
		t.Fatalf("MkdirAll wasm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(withHybridOut, "wasm", "bv_hybrid_scorer.js"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatalf("WriteFile bv_hybrid_scorer.js: %v", err)
	}
	if err := NewSQLiteExporter(issues[:1], nil, nil, nil).Export(withHybridOut); err != nil {
		t.Fatalf("Export with hybrid wasm stub failed: %v", err)
	}
	withHybridMetaBytes, err := os.ReadFile(filepath.Join(withHybridOut, "data", "meta.json"))
	if err != nil {
		t.Fatalf("Read withHybrid data/meta.json: %v", err)
	}
	var withHybridMeta ExportMeta
	if err := json.Unmarshal(withHybridMetaBytes, &withHybridMeta); err != nil {
		t.Fatalf("Unmarshal withHybrid data/meta.json: %v", err)
	}
	if !withHybridMeta.HasHybridWasm {
		t.Fatalf("meta.json has_hybrid_wasm = false, want true when wasm/bv_hybrid_scorer.js is present")
	}
}
