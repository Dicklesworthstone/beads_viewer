package export

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"modernc.org/sqlite"
)

// makeTestIssue creates a test issue with given parameters.
func makeTestIssue(id, title string, status model.Status, priority int, issueType model.IssueType) *model.Issue {
	now := time.Now()
	return &model.Issue{
		ID:          id,
		Title:       title,
		Description: "Test description for " + id,
		Status:      status,
		Priority:    priority,
		IssueType:   issueType,
		Labels:      []string{"test"},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestNewSQLiteExporter(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("test-1", "Test Issue", model.StatusOpen, 2, model.TypeTask),
	}
	deps := []*model.Dependency{}

	exp := NewSQLiteExporter(issues, deps, nil, nil)

	if exp == nil {
		t.Fatal("NewSQLiteExporter returned nil")
	}

	if len(exp.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(exp.Issues))
	}

	if exp.Config.ChunkThreshold != 5*1024*1024 {
		t.Errorf("Expected default chunk threshold 5MB, got %d", exp.Config.ChunkThreshold)
	}
}

func TestSetGitHash(t *testing.T) {
	exp := NewSQLiteExporter(nil, nil, nil, nil)
	exp.SetGitHash("abc123")

	if exp.gitHash != "abc123" {
		t.Errorf("Expected git hash abc123, got %s", exp.gitHash)
	}
}

func TestGraphLayoutDeterministicPositions(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("z-root", "Root", model.StatusOpen, 2, model.TypeTask),
		makeTestIssue("a-root", "Root", model.StatusOpen, 2, model.TypeTask),
		makeTestIssue("child", "Child", model.StatusOpen, 2, model.TypeTask),
	}
	deps := []*model.Dependency{{IssueID: "child", DependsOnID: "z-root", Type: model.DepBlocks}}
	want := map[string][2]float64{"a-root": {0, -40}, "z-root": {0, 40}, "child": {200, 0}}
	for i := 0; i < 20; i++ {
		issues[0], issues[2] = issues[2], issues[0]
		dir := t.TempDir()
		if err := NewSQLiteExporter(issues, deps, nil, nil).writeGraphLayout(openTestBundle(t, dir), "."); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "graph_layout.json"))
		if err != nil {
			t.Fatal(err)
		}
		var layout GraphLayout
		if err := json.Unmarshal(data, &layout); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(layout.Positions, want) {
			t.Fatalf("iteration %d: positions %v, want %v", i, layout.Positions, want)
		}
		if layout.NodeCount != 3 || layout.EdgeCount != 1 || !reflect.DeepEqual(layout.Links, [][2]string{{"z-root", "child"}}) {
			t.Fatalf("layout topology disagrees with exported issues: %+v", layout)
		}
	}
	t.Run("edgeless", func(t *testing.T) {
		dir := t.TempDir()
		if err := NewSQLiteExporter(issues, nil, nil, nil).writeGraphLayout(openTestBundle(t, dir), "."); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "graph_layout.json"))
		if err != nil {
			t.Fatal(err)
		}
		var layout GraphLayout
		if err := json.Unmarshal(data, &layout); err != nil {
			t.Fatal(err)
		}
		if layout.Links == nil || len(layout.Links) != 0 || layout.EdgeCount != 0 || len(layout.Positions) != 3 {
			t.Fatalf("edgeless layout must expose an empty link array: %+v", layout)
		}
	})
}

func TestGraphLayoutCentralityUsesExportedTopology(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("a", "A", model.StatusOpen, 2, model.TypeTask),
		makeTestIssue("b", "B", model.StatusClosed, 2, model.TypeTask),
	}
	deps := []*model.Dependency{
		{IssueID: "a", DependsOnID: "b", Type: model.DepBlocks},
		{IssueID: "b", DependsOnID: "missing", Type: model.DepBlocks},
		{IssueID: "a", DependsOnID: "ignored", Type: model.DepRelated},
	}
	// Deliberately unrelated supplied stats must never become cached metrics.
	unrelated := analysis.NewAnalyzer([]model.Issue{{ID: "other"}}).Analyze()
	exporter := NewSQLiteExporter(issues, deps, &unrelated, nil)
	dir := t.TempDir()
	if err := exporter.writeGraphLayout(openTestBundle(t, dir), "."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "graph_layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	var layout GraphLayout
	if err := json.Unmarshal(data, &layout); err != nil {
		t.Fatal(err)
	}
	got := layout.Centrality
	if got == nil || got.Version != 1 || got.Status.PageRank.State != "computed" || got.Status.Betweenness.State != "computed" {
		t.Fatalf("missing completed centrality: %+v", got)
	}
	if len(got.PageRank) != 3 || len(got.Betweenness) != 3 || got.Betweenness["b"] != 1 || got.Betweenness["a"] != 0 || got.Betweenness["missing"] != 0 {
		t.Fatalf("directed three-node chain with omitted endpoint: %+v", got)
	}
	if !(got.PageRank["missing"] > got.PageRank["b"] && got.PageRank["b"] > got.PageRank["a"]) {
		t.Fatalf("prerequisite rank ordering lost: %v", got.PageRank)
	}
	if math.Abs(got.PageRank["a"]+got.PageRank["b"]+got.PageRank["missing"]-1) > 0.000001 {
		t.Fatalf("rank mass not conserved: %v", got.PageRank)
	}
	if _, exists := got.PageRank["ignored"]; exists {
		t.Fatal("nonblocking edge polluted centrality")
	}
	if _, exists := layout.Positions["missing"]; exists {
		t.Fatal("analysis-only endpoint became a drawable issue")
	}
	if len(issues[0].Dependencies) != 0 {
		t.Fatal("centrality export mutated caller issues")
	}
}

func TestGraphLayoutFallbackLayers(t *testing.T) {
	for _, tc := range []struct {
		name        string
		links       [][2]string // prerequisite, dependent
		topological []string
		want        map[string][2]float64
	}{
		{
			name:  "reachable cycle",
			links: [][2]string{{"root", "left"}, {"root", "right"}, {"left", "right"}, {"right", "left"}, {"left", "leaf"}},
			want:  map[string][2]float64{"root": {0, 0}, "left": {200, -40}, "right": {200, 40}, "leaf": {400, 0}},
		},
		{
			name:  "shortcut",
			links: [][2]string{{"root", "left"}, {"left", "right"}, {"root", "right"}, {"right", "leaf"}},
			want:  map[string][2]float64{"root": {0, 0}, "left": {200, -40}, "right": {200, 40}, "leaf": {400, 0}},
		},
		{
			name:  "external dependent",
			links: [][2]string{{"root", "absent"}},
			want:  map[string][2]float64{"leaf": {0, -120}, "left": {0, -40}, "right": {0, 40}, "root": {0, 120}},
		},
		{
			name:  "rootless cycle",
			links: [][2]string{{"left", "right"}, {"right", "left"}, {"left", "leaf"}},
			want:  map[string][2]float64{"leaf": {0, -120}, "left": {0, -40}, "right": {0, 40}, "root": {0, 120}},
		},
		{
			name:        "topological longest path preserved",
			links:       [][2]string{{"root", "left"}, {"left", "right"}, {"root", "right"}, {"right", "leaf"}},
			topological: []string{"root", "left", "right", "leaf"},
			want:        map[string][2]float64{"root": {0, 0}, "left": {200, 0}, "right": {400, 0}, "leaf": {600, 0}},
		},
		{
			name:        "topological external prerequisite",
			links:       [][2]string{{"absent", "left"}},
			topological: []string{"absent", "left", "root", "right", "leaf"},
			want:        map[string][2]float64{"leaf": {0, -80}, "right": {0, 0}, "root": {0, 80}, "left": {200, 0}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for permutation := 0; permutation < 4; permutation++ {
				ids := []string{"root", "left", "right", "leaf"}
				var issues []*model.Issue
				for i := range ids {
					if permutation&1 != 0 {
						i = len(ids) - 1 - i
					}
					issues = append(issues, makeTestIssue(ids[i], ids[i], model.StatusOpen, 2, model.TypeTask))
				}
				var deps []*model.Dependency
				for i := range tc.links {
					if permutation&2 != 0 {
						i = len(tc.links) - 1 - i
					}
					link := tc.links[i]
					deps = append(deps, &model.Dependency{IssueID: link[1], DependsOnID: link[0], Type: model.DepBlocks})
				}
				dir := t.TempDir()
				var stats *analysis.GraphStats
				if tc.topological != nil {
					stats = &analysis.GraphStats{TopologicalOrder: tc.topological}
				}
				if err := NewSQLiteExporter(issues, deps, stats, nil).writeGraphLayout(openTestBundle(t, dir), "."); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(dir, "graph_layout.json"))
				if err != nil {
					t.Fatal(err)
				}
				var layout GraphLayout
				if err := json.Unmarshal(data, &layout); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(layout.Positions, tc.want) {
					t.Errorf("permutation %d: positions %v, want %v", permutation, layout.Positions, tc.want)
				}
			}
		})
	}
}

func TestSQLiteExportFullSourceReadiness(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	dep := func(id string, kind model.DependencyType) *model.Dependency {
		return &model.Dependency{DependsOnID: id, Type: kind}
	}
	cases := []struct {
		issue model.Issue
		state model.DependencyState
		ready bool
	}{
		{model.Issue{ID: "open", Status: model.StatusOpen}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "progress", Status: model.StatusInProgress, Assignee: "agent"}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "deferred", Status: model.StatusOpen, DeferUntil: &later}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "due-now", Status: model.StatusOpen, DeferUntil: &now}, model.DependenciesSatisfied, true},
		{model.Issue{ID: "blocked-status", Status: model.StatusBlocked}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "custom-status", Status: "qa-review"}, model.DependenciesSatisfied, false},
		{model.Issue{ID: "missing", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("absent", model.DepBlocks)}}, model.DependenciesUnknown, false},
		{model.Issue{ID: "missing-parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("absent", model.DepParentChild)}}, model.DependenciesUnknown, false},
		{model.Issue{ID: "filtered-blocker", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-open", model.DepWaitsFor)}}, model.DependenciesUnsatisfied, false},
		{model.Issue{ID: "inherited", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-parent", model.DepParentChild)}}, model.DependenciesUnsatisfied, false},
		{model.Issue{ID: "resolved", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-closed", model.DepBlocks), dep("hidden-deleted", model.DepWaitsFor)}}, model.DependenciesSatisfied, true},
	}
	source := []model.Issue{
		{ID: "hidden-open", Status: model.StatusOpen},
		{ID: "hidden-parent", Status: model.StatusOpen, Dependencies: []*model.Dependency{dep("hidden-open", model.DepConditionalBlocks)}},
		{ID: "hidden-closed", Status: model.StatusClosed},
		{ID: "hidden-deleted", Status: model.StatusTombstone},
	}
	var visible []*model.Issue
	for i := range cases {
		cases[i].issue.Title = cases[i].issue.ID
		cases[i].issue.IssueType = model.TypeTask
		source = append(source, cases[i].issue)
		visible = append(visible, &cases[i].issue)
	}
	exporter := NewSQLiteExporter(visible, nil, nil, nil)
	exporter.Config.Readiness = model.NewReadinessIndex(source)
	exporter.Config.ReadinessAt = now
	output := t.TempDir()
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(output, "beads.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range cases {
		var state string
		var ready bool
		if err := db.QueryRow(`SELECT dependency_state, is_actionable FROM issue_overview_mv WHERE id = ?`, tc.issue.ID).Scan(&state, &ready); err != nil {
			t.Fatal(err)
		}
		if state != string(tc.state) || ready != tc.ready {
			t.Errorf("%s: state=%s ready=%v, want %s/%v", tc.issue.ID, state, ready, tc.state, tc.ready)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues WHERE id LIKE 'hidden-%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("context rows leaked into display: count=%d err=%v", count, err)
	}
	var clock string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key='readiness_at'`).Scan(&clock); err != nil || clock != now.Format(time.RFC3339Nano) {
		t.Fatalf("readiness clock=%q err=%v", clock, err)
	}
}

func TestSQLiteExportStandaloneReadinessRefresh(t *testing.T) {
	prerequisite := makeTestIssue("root", "Root", model.StatusOpen, 1, model.TypeTask)
	child := makeTestIssue("child", "Child", model.StatusOpen, 1, model.TypeTask)
	child.Dependencies = []*model.Dependency{{IssueID: "child", DependsOnID: "root", Type: model.DepBlocks}}
	// Supply the same edge both ways, as real callers may do, plus a separate
	// missing prerequisite. Neither merging nor a later export may mutate input.
	missing := makeTestIssue("missing-child", "Missing", model.StatusOpen, 1, model.TypeTask)
	exporter := NewSQLiteExporter([]*model.Issue{prerequisite, child, missing}, []*model.Dependency{
		child.Dependencies[0], {IssueID: "missing-child", DependsOnID: "absent", Type: model.DepBlocks},
	}, nil, nil)
	for _, closed := range []bool{false, true} {
		if closed {
			prerequisite.Status = model.StatusClosed
		}
		output := t.TempDir()
		if err := exporter.Export(output); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(output, "beads.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		var ready bool
		if err := db.QueryRow(`SELECT is_actionable FROM issue_overview_mv WHERE id='child'`).Scan(&ready); err != nil || ready != closed {
			t.Errorf("after root closed=%v child ready=%v err=%v", closed, ready, err)
		}
		var state string
		if err := db.QueryRow(`SELECT dependency_state, is_actionable FROM issue_overview_mv WHERE id='missing-child'`).Scan(&state, &ready); err != nil || state != "unknown" || ready {
			t.Errorf("missing prerequisite: state=%q ready=%v err=%v", state, ready, err)
		}
		db.Close()
	}
	if len(child.Dependencies) != 1 || len(missing.Dependencies) != 0 {
		t.Fatal("export mutated caller dependency slices")
	}
}

func TestSQLiteExportMetadataRollsBackFailedInsert(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata-rollback.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := CreateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := InsertMetaValue(db, "sentinel", "preserve me"); err != nil {
		t.Fatal(err)
	}
	// Reject the second new metadata row regardless of Go map iteration
	// order. ABORT rolls back only that statement, so the caller must roll
	// back the earlier successful write as part of its own transaction.
	if _, err := db.Exec(`CREATE TRIGGER reject_second_metadata
		BEFORE INSERT ON export_meta
		WHEN (SELECT COUNT(*) FROM export_meta WHERE key <> 'sentinel') >= 1
		BEGIN
			SELECT RAISE(ABORT, 'reject second metadata row');
		END`); err != nil {
		t.Fatal(err)
	}
	exporter := NewSQLiteExporter(nil, nil, nil, nil)
	exporter.readiness = model.NewReadinessIndex(nil)
	exporter.readinessAt = time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	err = exporter.insertMeta(db)
	var sqliteErr *sqlite.Error
	if err == nil || !strings.Contains(err.Error(), "reject second metadata row") || !errors.As(err, &sqliteErr) {
		t.Fatalf("expected SQLite trigger refusal after the first metadata write, got %v", err)
	}
	if db.Stats().InUse != 0 {
		t.Fatal("failed metadata insertion retained its connection")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM export_meta`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed metadata insertion left %d rows; want only the original sentinel", count)
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'sentinel'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "preserve me" {
		t.Fatalf("existing metadata changed: %q", value)
	}
}

func TestSQLiteExportFailedRebuildPreservesPublishedDatabase(t *testing.T) {
	output := t.TempDir()
	issue := makeTestIssue("published", "Original", model.StatusOpen, 1, model.TypeTask)
	exporter := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(output, "beads.sqlite3")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A real primary-key violation fails after schema creation and partial
	// inserts. The previously published snapshot must remain byte-for-byte intact.
	broken := NewSQLiteExporter([]*model.Issue{issue, issue}, nil, nil, nil)
	if err := broken.Export(output); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: issues.id") {
		t.Fatalf("expected duplicate issue primary-key failure, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed rebuild changed published database: %v", err)
	}
	issue.Title = "Rebuilt"
	if err := exporter.Export(output); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var title string
	if err := db.QueryRow(`SELECT title FROM issues WHERE id='published'`).Scan(&title); err != nil || title != "Rebuilt" {
		t.Fatalf("successful rebuild not published: title=%q err=%v", title, err)
	}
	remaining, err := filepath.Glob(filepath.Join(output, ".beads-*"))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("temporary databases remain: %v err=%v", remaining, err)
	}
}

func TestSQLiteExportConfinesFilesAndChunkCleanup(t *testing.T) {
	for _, kind := range []string{"data parent", "chunks parent", "metadata hardlink", "chunk hardlink", "stale chunk hardlink", "database hardlink"} {
		t.Run(kind, func(t *testing.T) {
			out := t.TempDir()
			outside := t.TempDir()
			const sentinel = "FAKE-PRIVATE-OUTSIDE-DATABASE-CONTENT"
			target := filepath.Join(outside, "00000.bin")
			if kind == "data parent" {
				target = filepath.Join(outside, "meta.json")
			}
			if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
			exporter := NewSQLiteExporter([]*model.Issue{makeTestIssue("safe-1", "Safe issue", model.StatusOpen, 1, model.TypeTask)}, nil, nil, nil)
			var linkErr error
			switch kind {
			case "data parent":
				linkErr = os.Symlink(outside, filepath.Join(out, "data"))
			case "chunks parent":
				linkErr = os.Symlink(outside, filepath.Join(out, "chunks"))
			case "metadata hardlink":
				if err := os.Mkdir(filepath.Join(out, "data"), 0o700); err != nil {
					t.Fatal(err)
				}
				linkErr = os.Link(target, filepath.Join(out, "data", "meta.json"))
			case "chunk hardlink", "stale chunk hardlink":
				if err := os.Mkdir(filepath.Join(out, "chunks"), 0o700); err != nil {
					t.Fatal(err)
				}
				linkErr = os.Link(target, filepath.Join(out, "chunks", "00000.bin"))
				if kind == "chunk hardlink" {
					exporter.Config.ChunkThreshold = 1
					exporter.Config.ChunkSize = 4096
				}
			case "database hardlink":
				linkErr = os.Link(target, filepath.Join(out, "beads.sqlite3"))
			}
			if linkErr != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("link unavailable: %v", linkErr)
				}
				t.Fatal(linkErr)
			}
			err := exporter.Export(out)
			if kind == "data parent" || kind == "chunks parent" {
				if err == nil {
					t.Fatal("outside parent symlink was accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(target); err != nil || string(content) != sentinel {
				t.Fatalf("export or cleanup changed outside target: %q, %v", content, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("export wrote outside its root: %v, %v", entries, err)
			}
			if kind == "chunk hardlink" {
				config := readChunkConfig(t, out)
				database, err := os.ReadFile(filepath.Join(out, "beads.sqlite3"))
				if err != nil || !bytes.Equal(database, reassembleChunks(t, out, config)) {
					t.Fatalf("safe chunk replacement changed reconstructed database: %v", err)
				}
			}
			if kind == "stale chunk hardlink" {
				if _, err := os.Lstat(filepath.Join(out, "chunks", "00000.bin")); !os.IsNotExist(err) {
					t.Fatalf("owned stale chunk was not removed: %v", err)
				}
			}
		})
	}
}

func TestSQLiteExportRespectsUmask(t *testing.T) {
	const directoryEnv = "BV_TEST_EXPORT_UMASK_DIR"
	const maskEnv = "BV_TEST_EXPORT_UMASK"
	if directory := os.Getenv(directoryEnv); directory != "" {
		mask := os.Getenv(maskEnv)
		want := os.FileMode(0644)
		wantJSON := os.FileMode(0644)
		if mask == "077" {
			want = 0600
			wantJSON = 0600
		} else if mask == "002" {
			wantJSON = 0664
		} else if mask != "022" {
			t.Fatalf("unsupported test umask %q", mask)
		}
		issue := makeTestIssue("private", "Private issue", model.StatusOpen, 1, model.TypeTask)
		exporter := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)
		for range 2 {
			if err := exporter.Export(directory); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(directory, "beads.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Fatalf("database mode under umask %s = %04o, want %04o", mask, got, want)
			}
			info, err = os.Stat(filepath.Join(directory, "data", "meta.json"))
			if err != nil || info.Mode().Perm() != wantJSON {
				t.Fatalf("JSON mode under umask %s: info=%v err=%v, want %04o", mask, info, err, wantJSON)
			}
		}
		// Atomic replacement must retain explicit group access even when the
		// current process umask would remove it from a newly created inode.
		shared := filepath.Join(directory, "shared-report.md")
		if err := os.WriteFile(shared, []byte("previous report"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(shared, 0o660); err != nil {
			t.Fatal(err)
		}
		if err := WriteReportFile(shared, []byte("updated report")); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(shared)
		if err != nil || info.Mode().Perm() != 0o660 {
			t.Fatalf("replacement changed configured group access under umask %s: info=%v err=%v", mask, info, err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix umask semantics")
	}
	for _, mask := range []string{"077", "022", "002"} {
		t.Run(mask, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0755); err != nil {
				t.Fatal(err)
			}
			// Change the mask only in a subprocess, never in the concurrent test runner.
			command := exec.Command("sh", "-c", `umask "$1"; shift; exec "$@"`, "sh", mask, os.Args[0], "-test.run=^TestSQLiteExportRespectsUmask$") // ubs:ignore — fixed shell program; literal test masks and executable are passed as quoted positional arguments.
			command.Env = append(os.Environ(), directoryEnv+"="+directory, maskEnv+"="+mask)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("umask %s export failed: %v\n%s", mask, err, output)
			}
		})
	}
}

func TestWriteRobotJSONPreservesPayloadTimestamp(t *testing.T) {
	const precise = "2026-09-05T02:00:00.123456789Z"
	const envelopeTime = "2026-09-05T02:00:00Z"
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"precise payload", map[string]any{"generated_at": precise, "issue_count": 2}, precise},
		{"envelope fallback", map[string]any{"issue_count": 2}, envelopeTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := NewSQLiteExporter(nil, nil, nil, nil)
			exporter.Config.RobotEnvelope = map[string]json.RawMessage{
				"generated_at":     json.RawMessage(`"` + envelopeTime + `"`),
				"source_authority": json.RawMessage(`{"state":"partial","claim_safe":false}`),
				"authority_hash":   json.RawMessage(`"source-fingerprint"`),
			}
			path := filepath.Join(t.TempDir(), "meta.json")
			if err := exporter.writeRobotJSON(openTestBundle(t, filepath.Dir(path)), filepath.Base(path), tc.payload); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				GeneratedAt   string `json:"generated_at"`
				IssueCount    int    `json:"issue_count"`
				AuthorityHash string `json:"authority_hash"`
				Authority     struct {
					State     string `json:"state"`
					ClaimSafe bool   `json:"claim_safe"`
				} `json:"source_authority"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.GeneratedAt != tc.want || got.IssueCount != 2 || got.AuthorityHash != "source-fingerprint" || got.Authority.State != "partial" || got.Authority.ClaimSafe {
				t.Fatalf("payload timestamp or shared authority changed: got %s, want timestamp %q, count2 and partial authority", data, tc.want)
			}
		})
	}
}

func TestExport_CreatesDatabase(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("exp-1", "Export Test 1", model.StatusOpen, 1, model.TypeBug),
		makeTestIssue("exp-2", "Export Test 2", model.StatusInProgress, 2, model.TypeFeature),
	}

	deps := []*model.Dependency{
		{IssueID: "exp-2", DependsOnID: "exp-1", Type: model.DepBlocks},
	}

	exp := NewSQLiteExporter(issues, deps, nil, nil)
	exp.SetGitHash("test123")

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify database file exists
	dbPath := filepath.Join(tmpDir, "beads.sqlite3")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}

	// Verify we can query the database
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Failed to open exported database: %v", err)
	}
	defer db.Close()

	// Check issues
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count); err != nil {
		t.Fatalf("Query issues count failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 issues, got %d", count)
	}

	// Check dependencies
	if err := db.QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&count); err != nil {
		t.Fatalf("Query dependencies count failed: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 dependency, got %d", count)
	}

	// Check metadata
	var version string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatalf("Query version failed: %v", err)
	}
	if version != "1.0.0" {
		t.Errorf("Expected version 1.0.0, got %s", version)
	}

	// Check git hash in metadata
	var gitCommit string
	if err := db.QueryRow(`SELECT value FROM export_meta WHERE key = 'git_commit'`).Scan(&gitCommit); err != nil {
		t.Fatalf("Query git_commit failed: %v", err)
	}
	if gitCommit != "test123" {
		t.Errorf("Expected git_commit test123, got %s", gitCommit)
	}
}

func TestExport_CreatesDataDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("data-1", "Data Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify data directory exists
	dataDir := filepath.Join(tmpDir, "data")
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		t.Error("Data directory was not created")
	}

	// Verify meta.json exists
	metaPath := filepath.Join(dataDir, "meta.json")
	if _, err := os.Stat(metaPath); os.IsNotExist(err) {
		t.Error("meta.json was not created")
	}

	// Read and verify meta.json
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("Failed to read meta.json: %v", err)
	}

	var meta ExportMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Failed to parse meta.json: %v", err)
	}

	if meta.IssueCount != 1 {
		t.Errorf("Expected issue_count 1, got %d", meta.IssueCount)
	}
}

func TestExport_MaterializedView(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("mv-1", "MV Test", model.StatusOpen, 1, model.TypeBug),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Verify materialized view exists and has data
	var title string
	err = db.QueryRow(`SELECT title FROM issue_overview_mv WHERE id = ?`, "mv-1").Scan(&title)
	if err != nil {
		t.Fatalf("Query materialized view failed: %v", err)
	}
	if title != "MV Test" {
		t.Errorf("Expected title 'MV Test', got '%s'", title)
	}
}

// TestExport_RichTextFieldsRoundTrip guards against the #170 regression where
// notes (and the sibling design / acceptance_criteria fields) were silently
// dropped from the static-site export — they were never written to the issues
// table nor surfaced in issue_overview_mv, so the web UI could never display
// them. The web viewer reads a single issue via `SELECT * FROM issue_overview_mv`,
// so we assert these fields round-trip through the materialized view (not just
// the base table).
func TestExport_RichTextFieldsRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	now := time.Now()
	issues := []*model.Issue{
		{
			ID:                 "rich-1",
			Title:              "Issue with rich text",
			Description:        "The description.",
			Design:             "The design notes.",
			AcceptanceCriteria: "The acceptance criteria.",
			Notes:              "The notes that must be visible in the web UI.",
			Status:             model.StatusOpen,
			Priority:           1,
			IssueType:          model.TypeTask,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)
	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// The web UI's getIssue() runs `SELECT * FROM issue_overview_mv`, so the
	// rich-text fields must be present in the materialized view to be displayed.
	var design, acceptance, notes string
	err = db.QueryRow(
		`SELECT design, acceptance_criteria, notes FROM issue_overview_mv WHERE id = ?`,
		"rich-1",
	).Scan(&design, &acceptance, &notes)
	if err != nil {
		t.Fatalf("Query rich-text fields from materialized view failed: %v", err)
	}

	if design != "The design notes." {
		t.Errorf("design not exported: got %q", design)
	}
	if acceptance != "The acceptance criteria." {
		t.Errorf("acceptance_criteria not exported: got %q", acceptance)
	}
	if notes != "The notes that must be visible in the web UI." {
		t.Errorf("notes not exported (regression of #170): got %q", notes)
	}
}

func TestExport_ChunkConfigCreated(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("chunk-1", "Chunk Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify chunk config exists
	configPath := filepath.Join(tmpDir, "beads.sqlite3.config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("Failed to read chunk config: %v", err)
	}

	var config ChunkConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to parse chunk config: %v", err)
	}

	// Small database should not be chunked
	if config.Chunked {
		t.Error("Small database should not be chunked")
	}

	if config.TotalSize <= 0 {
		t.Error("Total size should be positive")
	}

	// Hash must be present for OPFS cache invalidation (bv-pages-cache-fix)
	// Without this, all deployments use cache key "default" and old data persists
	if config.Hash == "" {
		t.Error("Config hash is required for OPFS cache invalidation but was empty")
	}

	// Hash should be 64 hex chars (SHA-256)
	if len(config.Hash) != 64 {
		t.Errorf("Expected SHA-256 hash (64 chars), got %d chars: %s", len(config.Hash), config.Hash)
	}
}

func TestGetExportedIssues(t *testing.T) {
	issues := []*model.Issue{
		makeTestIssue("get-1", "Get Test 1", model.StatusOpen, 1, model.TypeBug),
		makeTestIssue("get-2", "Get Test 2", model.StatusInProgress, 2, model.TypeFeature),
	}

	deps := []*model.Dependency{
		{IssueID: "get-2", DependsOnID: "get-1", Type: model.DepBlocks},
	}

	exp := NewSQLiteExporter(issues, deps, nil, nil)
	exported := exp.GetExportedIssues()

	if len(exported) != 2 {
		t.Fatalf("Expected 2 exported issues, got %d", len(exported))
	}

	// Verify blocking relationships:
	// get-2 depends on get-1, so get-1 blocks get-2
	var foundGet1, foundGet2 bool
	for _, e := range exported {
		switch e.ID {
		case "get-1":
			foundGet1 = true
			// get-1 blocks get-2
			if e.BlocksCount != 1 {
				t.Errorf("get-1: expected blocks_count 1, got %d", e.BlocksCount)
			}
			if len(e.BlocksIDs) != 1 || e.BlocksIDs[0] != "get-2" {
				t.Errorf("get-1: expected blocks_ids ['get-2'], got %v", e.BlocksIDs)
			}
			if e.BlockedByCount != 0 {
				t.Errorf("get-1: expected blocked_by_count 0, got %d", e.BlockedByCount)
			}
		case "get-2":
			foundGet2 = true
			// get-2 is blocked by get-1
			if e.BlockedByCount != 1 {
				t.Errorf("get-2: expected blocked_by_count 1, got %d", e.BlockedByCount)
			}
			if len(e.BlockedByIDs) != 1 || e.BlockedByIDs[0] != "get-1" {
				t.Errorf("get-2: expected blocked_by_ids ['get-1'], got %v", e.BlockedByIDs)
			}
			if e.BlocksCount != 0 {
				t.Errorf("get-2: expected blocks_count 0, got %d", e.BlocksCount)
			}
		}
	}

	if !foundGet1 {
		t.Error("Issue get-1 not found in exported issues")
	}
	if !foundGet2 {
		t.Error("Issue get-2 not found in exported issues")
	}
}

func TestExportToJSON(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("json-1", "JSON Test", model.StatusOpen, 2, model.TypeTask),
	}

	exp := NewSQLiteExporter(issues, nil, nil, nil)
	exp.SetGitHash("jsonhash")

	jsonPath := filepath.Join(tmpDir, "export.json")
	if err := exp.ExportToJSON(jsonPath); err != nil {
		t.Fatalf("ExportToJSON failed: %v", err)
	}

	// Read and verify JSON
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("Failed to read export.json: %v", err)
	}

	var output struct {
		Meta   ExportMeta    `json:"meta"`
		Issues []ExportIssue `json:"issues"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("Failed to parse export.json: %v", err)
	}

	if output.Meta.IssueCount != 1 {
		t.Errorf("Expected issue_count 1, got %d", output.Meta.IssueCount)
	}

	if output.Meta.GitCommit != "jsonhash" {
		t.Errorf("Expected git_commit 'jsonhash', got '%s'", output.Meta.GitCommit)
	}

	if len(output.Issues) != 1 {
		t.Errorf("Expected 1 issue, got %d", len(output.Issues))
	}
}

func TestExport_WithLabels(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("label-1", "Label Test", model.StatusOpen, 2, model.TypeTask)
	issue.Labels = []string{"bug", "urgent", "backend"}

	exp := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var labels string
	if err := db.QueryRow(`SELECT labels FROM issues WHERE id = ?`, "label-1").Scan(&labels); err != nil {
		t.Fatalf("Query labels failed: %v", err)
	}

	// Labels should be JSON array
	var parsedLabels []string
	if err := json.Unmarshal([]byte(labels), &parsedLabels); err != nil {
		t.Fatalf("Failed to parse labels JSON: %v", err)
	}

	if len(parsedLabels) != 3 {
		t.Errorf("Expected 3 labels, got %d", len(parsedLabels))
	}
}

func TestExport_WithClosedAt(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("closed-1", "Closed Test", model.StatusClosed, 2, model.TypeTask)
	closedTime := time.Now().Add(-time.Hour)
	issue.ClosedAt = &closedTime

	exp := NewSQLiteExporter([]*model.Issue{issue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var closedAt sql.NullString
	if err := db.QueryRow(`SELECT closed_at FROM issues WHERE id = ?`, "closed-1").Scan(&closedAt); err != nil {
		t.Fatalf("Query closed_at failed: %v", err)
	}

	if !closedAt.Valid {
		t.Error("closed_at should not be null for closed issue")
	}
}

// TestExport_WithComments tests the comments export functionality (bv-52)
func TestExport_WithComments(t *testing.T) {
	tmpDir := t.TempDir()

	issue := makeTestIssue("comments-1", "Issue with comments", model.StatusOpen, 2, model.TypeTask)
	now := time.Now()
	issue.Comments = []*model.Comment{
		{ID: "1", IssueID: "comments-1", Author: "alice", Text: "First comment", CreatedAt: now.Add(-time.Hour)},
		{ID: "2", IssueID: "comments-1", Author: "bob", Text: "Second comment", CreatedAt: now},
	}
	otherIssue := makeTestIssue("comments-2", "Another issue with a reused comment ID", model.StatusOpen, 2, model.TypeTask)
	otherIssue.Comments = []*model.Comment{
		{ID: "1", IssueID: "comments-2", Author: "carol", Text: "Same local ID, different issue", CreatedAt: now},
	}

	exp := NewSQLiteExporter([]*model.Issue{issue, otherIssue}, nil, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Verify comments were inserted
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments WHERE issue_id = ?`, "comments-1").Scan(&count); err != nil {
		t.Fatalf("Query comments count failed: %v", err)
	}
	if count != 2 {
		t.Errorf("Expected 2 comments, got %d", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&count); err != nil {
		t.Fatalf("Query total comments count failed: %v", err)
	}
	if count != 3 {
		t.Errorf("Expected 3 total comments with reused per-issue IDs, got %d", count)
	}

	// Verify comment content (id is now composite: issue_id:comment_id)
	var author, text string
	if err := db.QueryRow(`SELECT author, text FROM comments WHERE id = ?`, "comments-1:1").Scan(&author, &text); err != nil {
		t.Fatalf("Query comment 1 failed: %v", err)
	}
	if author != "alice" || text != "First comment" {
		t.Errorf("Comment 1: expected author='alice', text='First comment', got author='%s', text='%s'", author, text)
	}
	if err := db.QueryRow(`SELECT author, text FROM comments WHERE id = ?`, "comments-2:1").Scan(&author, &text); err != nil {
		t.Fatalf("Query reused comment ID failed: %v", err)
	}
	if author != "carol" || text != "Same local ID, different issue" {
		t.Errorf("Reused comment ID: got author=%q, text=%q", author, text)
	}

	// Verify comment_count in materialized view
	var commentCount int
	if err := db.QueryRow(`SELECT comment_count FROM issue_overview_mv WHERE id = ?`, "comments-1").Scan(&commentCount); err != nil {
		t.Fatalf("Query comment_count from MV failed: %v", err)
	}
	if commentCount != 2 {
		t.Errorf("Expected comment_count=2 in MV, got %d", commentCount)
	}
}

func TestExport_EmptyData(t *testing.T) {
	tmpDir := t.TempDir()

	exp := NewSQLiteExporter([]*model.Issue{}, []*model.Dependency{}, nil, nil)

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export with empty data failed: %v", err)
	}

	// Should still create a valid database
	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&count); err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 issues, got %d", count)
	}
}

func TestExport_DisableRobotOutputs(t *testing.T) {
	tmpDir := t.TempDir()

	exp := NewSQLiteExporter([]*model.Issue{
		makeTestIssue("robot-1", "Robot Test", model.StatusOpen, 2, model.TypeTask),
	}, nil, nil, nil)

	exp.Config.IncludeRobotOutputs = false

	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// meta.json should not exist when robot outputs are disabled
	metaPath := filepath.Join(tmpDir, "data", "meta.json")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Error("meta.json should not exist when robot outputs are disabled")
	}
}

func TestStringSliceContains(t *testing.T) {
	tests := []struct {
		slice    []string
		val      string
		expected bool
	}{
		{[]string{"a", "b", "c"}, "b", true},
		{[]string{"a", "b", "c"}, "B", true}, // case-insensitive
		{[]string{"a", "b", "c"}, "d", false},
		{[]string{}, "a", false},
		{nil, "a", false},
	}

	for _, tc := range tests {
		result := stringSliceContains(tc.slice, tc.val)
		if result != tc.expected {
			t.Errorf("stringSliceContains(%v, %s) = %v, expected %v", tc.slice, tc.val, result, tc.expected)
		}
	}
}

func TestExport_ExplicitMetricsWithoutStats(t *testing.T) {
	tmpDir := t.TempDir()

	issues := []*model.Issue{
		makeTestIssue("met-1", "Root Blocker", model.StatusOpen, 0, model.TypeTask),
		makeTestIssue("met-2", "Dependent Task", model.StatusOpen, 1, model.TypeFeature),
	}
	deps := []*model.Dependency{
		{IssueID: "met-2", DependsOnID: "met-1", Type: model.DepBlocks},
	}
	metrics := map[string]*model.IssueMetrics{
		"met-1": {
			PageRank:          0.75,
			Betweenness:       0.40,
			CriticalPathDepth: 3,
			TriageScore:       0.92,
		},
		"met-2": {
			PageRank:          0.25,
			Betweenness:       0.10,
			CriticalPathDepth: 1,
			TriageScore:       0.55,
		},
	}

	exp := NewSQLiteExporter(issues, deps, metrics, nil)
	if exp.Stats != nil {
		t.Fatal("expected Stats to be nil when explicit Metrics map is provided")
	}
	if err := exp.Export(tmpDir); err != nil {
		t.Fatalf("Export with explicit Metrics failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("open exported sqlite db: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_metrics`).Scan(&count); err != nil {
		t.Fatalf("count issue_metrics: %v", err)
	}
	if count != 2 {
		t.Fatalf("issue_metrics count = %d, want 2", count)
	}

	var pr, bw, score float64
	var cp, blocks, blockedBy int
	if err := db.QueryRow(`
		SELECT pagerank, betweenness, critical_path_depth, triage_score, blocks_count, blocked_by_count
		FROM issue_overview_mv
		WHERE id = ?
	`, "met-1").Scan(&pr, &bw, &cp, &score, &blocks, &blockedBy); err != nil {
		t.Fatalf("query issue_overview_mv for met-1: %v", err)
	}
	if pr != 0.75 || bw != 0.40 || cp != 3 || score != 0.92 || blocks != 1 || blockedBy != 0 {
		t.Fatalf("met-1 overview metrics = (pr=%v, bw=%v, cp=%d, score=%v, blocks=%d, blockedBy=%d), want (0.75, 0.4, 3, 0.92, 1, 0)",
			pr, bw, cp, score, blocks, blockedBy)
	}
}

func TestSQLiteExporter_SourceDateEpochDeterminismAndChunkForwardSlashes(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	fixedTime := time.Unix(1700000000, 0).UTC()

	buildExporter := func() *SQLiteExporter {
		i1 := &model.Issue{
			ID:          "det-1",
			Title:       "Deterministic Root Issue",
			Description: "Ensures reproducible SQLite and offline manifest exports",
			Status:      model.StatusOpen,
			Priority:    0,
			IssueType:   model.TypeFeature,
			Labels:      []string{"export", "determinism"},
			CreatedAt:   fixedTime,
			UpdatedAt:   fixedTime,
		}
		i2 := &model.Issue{
			ID:          "det-2",
			Title:       "Deterministic Dependent Issue",
			Description: "Depends on det-1",
			Status:      model.StatusOpen,
			Priority:    1,
			IssueType:   model.TypeTask,
			Labels:      []string{"export"},
			CreatedAt:   fixedTime,
			UpdatedAt:   fixedTime,
			Dependencies: []*model.Dependency{
				{IssueID: "det-2", DependsOnID: "det-1", Type: model.DepBlocks},
			},
		}
		deps := []*model.Dependency{
			{IssueID: "det-2", DependsOnID: "det-1", Type: model.DepBlocks, CreatedAt: fixedTime},
		}
		stats := analysis.NewAnalyzer([]model.Issue{*i1, *i2}).AnalyzeWithConfig(analysis.FullAnalysisConfig())
		triage := analysis.ComputeTriageWithOptionsAndTime([]model.Issue{*i1, *i2}, analysis.TriageOptions{
			UseFastConfig: true,
			WaitForPhase2: true,
		}, fixedTime)
		exp := NewSQLiteExporter([]*model.Issue{i1, i2}, deps, nil, &triage)
		exp.Stats = &stats
		exp.SetGitHash("0123456789abcdef")
		exp.Config.Title = "Deterministic Dashboard"
		exp.Config.ChunkThreshold = 1 // Force chunking so ChunkInfo.Path forward-slash format is verified too
		exp.Config.ChunkSize = 8192
		return exp
	}

	dir1 := filepath.Join(t.TempDir(), "bundle1")
	if err := buildExporter().Export(dir1); err != nil {
		t.Fatalf("first Export failed: %v", err)
	}
	if err := EmbedAssetsWithSubtitle(dir1, "Deterministic Dashboard", ""); err != nil {
		t.Fatalf("first EmbedAssetsWithSubtitle failed: %v", err)
	}

	// Advance wall clock across at least one Unix second boundary.
	time.Sleep(1100 * time.Millisecond)

	dir2 := filepath.Join(t.TempDir(), "bundle2")
	if err := buildExporter().Export(dir2); err != nil {
		t.Fatalf("second Export failed: %v", err)
	}
	if err := EmbedAssetsWithSubtitle(dir2, "Deterministic Dashboard", ""); err != nil {
		t.Fatalf("second EmbedAssetsWithSubtitle failed: %v", err)
	}

	hashFile := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}

	for _, rel := range []string{
		"beads.sqlite3",
		"beads.sqlite3.config.json",
		filepath.Join("data", "meta.json"),
		filepath.Join("data", "graph_layout.json"),
		"index.html",
		"coi-serviceworker.js",
	} {
		h1 := hashFile(filepath.Join(dir1, rel))
		h2 := hashFile(filepath.Join(dir2, rel))
		if h1 != h2 {
			t.Fatalf("SOURCE_DATE_EPOCH export drift in %s: hash1=%s != hash2=%s", rel, h1, h2)
		}
	}

	cfgBytes, err := os.ReadFile(filepath.Join(dir1, "beads.sqlite3.config.json"))
	if err != nil {
		t.Fatalf("ReadFile chunk config: %v", err)
	}
	var chunkCfg ChunkConfig
	if err := json.Unmarshal(cfgBytes, &chunkCfg); err != nil {
		t.Fatalf("Unmarshal ChunkConfig: %v", err)
	}
	if !chunkCfg.Chunked || len(chunkCfg.Chunks) == 0 {
		t.Fatalf("expected chunked export with >= 1 chunk, got %+v", chunkCfg)
	}
	for _, ch := range chunkCfg.Chunks {
		if strings.Contains(ch.Path, `\`) || !strings.HasPrefix(ch.Path, "chunks/") {
			t.Fatalf("ChunkInfo.Path must use URL forward slashes (\"chunks/...\"), got %q", ch.Path)
		}
	}
}

func TestSQLiteExport_IsTemplateAndPinnedColumns(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	tmpl := &model.Issue{
		ID:         "tmpl-1",
		Title:      "Template Issue",
		Status:     model.StatusOpen,
		Priority:   1,
		IssueType:  model.TypeTask,
		IsTemplate: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	pinned := &model.Issue{
		ID:        "pin-1",
		Title:     "Pinned Issue",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		Pinned:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	ready := &model.Issue{
		ID:        "ready-1",
		Title:     "Ready Issue",
		Status:    model.StatusOpen,
		Priority:  1,
		IssueType: model.TypeTask,
		CreatedAt: now,
		UpdatedAt: now,
	}

	outDir := filepath.Join(t.TempDir(), "bundle")
	exp := NewSQLiteExporter([]*model.Issue{tmpl, pinned, ready}, nil, nil, nil)
	if err := exp.Export(outDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"issues", "issue_overview_mv"} {
		var gotTmpl, gotPinned int
		err := db.QueryRow(fmt.Sprintf(`SELECT is_template, pinned FROM %s WHERE id = ?`, table), "tmpl-1").Scan(&gotTmpl, &gotPinned)
		if err != nil || gotTmpl != 1 || gotPinned != 0 {
			t.Fatalf("%s[tmpl-1] (is_template, pinned) = (%d, %d), err=%v; want (1, 0)", table, gotTmpl, gotPinned, err)
		}
		err = db.QueryRow(fmt.Sprintf(`SELECT is_template, pinned FROM %s WHERE id = ?`, table), "pin-1").Scan(&gotTmpl, &gotPinned)
		if err != nil || gotTmpl != 0 || gotPinned != 1 {
			t.Fatalf("%s[pin-1] (is_template, pinned) = (%d, %d), err=%v; want (0, 1)", table, gotTmpl, gotPinned, err)
		}
		err = db.QueryRow(fmt.Sprintf(`SELECT is_template, pinned FROM %s WHERE id = ?`, table), "ready-1").Scan(&gotTmpl, &gotPinned)
		if err != nil || gotTmpl != 0 || gotPinned != 0 {
			t.Fatalf("%s[ready-1] (is_template, pinned) = (%d, %d), err=%v; want (0, 0)", table, gotTmpl, gotPinned, err)
		}
	}

	for id, wantActionable := range map[string]int{"tmpl-1": 0, "pin-1": 0, "ready-1": 1} {
		var got int
		if err := db.QueryRow(`SELECT is_actionable FROM issue_overview_mv WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("SELECT is_actionable[%s]: %v", id, err)
		}
		if got != wantActionable {
			t.Fatalf("issue_overview_mv[%s].is_actionable = %d, want %d", id, got, wantActionable)
		}
	}
}

func TestSQLiteExport_AllScoresPopulatesRank11Plus(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	var ptrIssues []*model.Issue
	var valIssues []model.Issue
	for i := 1; i <= 15; i++ {
		iss := model.Issue{
			ID:        fmt.Sprintf("rank-%02d", i),
			Title:     fmt.Sprintf("Ranked Issue %02d", i),
			Status:    model.StatusOpen,
			Priority:  i % 4,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		}
		valIssues = append(valIssues, iss)
		cp := iss
		ptrIssues = append(ptrIssues, &cp)
	}

	triage := analysis.ComputeTriageWithOptionsAndTime(valIssues, analysis.TriageOptions{
		TopN:          10,
		UseFastConfig: true,
		WaitForPhase2: true,
	}, now)
	if len(triage.Recommendations) != 10 {
		t.Fatalf("len(Recommendations) = %d, want 10", len(triage.Recommendations))
	}
	if len(triage.AllScores) != 15 {
		t.Fatalf("len(AllScores) = %d, want 15", len(triage.AllScores))
	}

	stats := analysis.NewAnalyzer(valIssues).Analyze()
	for _, tc := range []struct {
		name  string
		stats *analysis.GraphStats
	}{
		{name: "with_stats", stats: &stats},
		{name: "without_stats", stats: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := filepath.Join(t.TempDir(), "bundle")
			exp := NewSQLiteExporter(ptrIssues, nil, tc.stats, &triage)
			if err := exp.Export(outDir); err != nil {
				t.Fatalf("Export failed: %v", err)
			}

			db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer db.Close()

			rows, err := db.Query(`SELECT id, triage_score FROM issue_overview_mv ORDER BY id`)
			if err != nil {
				t.Fatalf("SELECT triage_score: %v", err)
			}
			defer rows.Close()
			seen := 0
			for rows.Next() {
				var id string
				var score float64
				if err := rows.Scan(&id, &score); err != nil {
					t.Fatalf("Scan: %v", err)
				}
				seen++
				if score <= 0 {
					t.Fatalf("issue_overview_mv[%s].triage_score = %v, want > 0 (AllScores parity for #11+)", id, score)
				}
			}
			if seen != 15 {
				t.Fatalf("issue_overview_mv row count = %d, want 15", seen)
			}

			exportedIssues := exp.GetExportedIssues()
			if len(exportedIssues) != 15 {
				t.Fatalf("GetExportedIssues len = %d, want 15", len(exportedIssues))
			}
			for _, iss := range exportedIssues {
				if iss.TriageScore <= 0 {
					t.Fatalf("GetExportedIssues[%s].TriageScore = %v, want > 0", iss.ID, iss.TriageScore)
				}
			}
		})
	}
}

func TestSQLiteExport_TriageZeroActionableNoLeakAndProvisionalScrub(t *testing.T) {
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	t.Run("zero actionable recommendations never re-runs unscoped fallback", func(t *testing.T) {
		// Issue is open with label "frontend", simulating --label backend export where 0 backend items were actionable
		// or all scoped items were blocked.
		unscoped := &model.Issue{
			ID:        "fe-1",
			Title:     "Frontend Open Item",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			Labels:    []string{"frontend"},
			CreatedAt: now,
			UpdatedAt: now,
		}
		emptyScopedTriage := &analysis.TriageResult{
			Recommendations: []analysis.Recommendation{},
		}
		outDir := filepath.Join(t.TempDir(), "zero-actionable")
		exp := NewSQLiteExporter([]*model.Issue{unscoped}, nil, nil, emptyScopedTriage)
		if err := exp.Export(outDir); err != nil {
			t.Fatalf("Export failed: %v", err)
		}

		raw, err := os.ReadFile(filepath.Join(outDir, "data", "triage.json"))
		if err != nil {
			t.Fatalf("ReadFile triage.json: %v", err)
		}
		var parsed analysis.TriageResult
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("Unmarshal triage.json: %v", err)
		}
		if len(parsed.RecommendationsByTrack) != 0 || len(parsed.RecommendationsByLabel) != 0 {
			t.Fatalf("expected 0 leaked grouped recommendations when Recommendations is empty, got tracks=%d labels=%d",
				len(parsed.RecommendationsByTrack), len(parsed.RecommendationsByLabel))
		}
	})

	t.Run("provisional source_authority scrubs pre-grouped triage without mutating caller", func(t *testing.T) {
		iss := &model.Issue{
			ID:        "prov-1",
			Title:     "Provisional Item",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			Labels:    []string{"core"},
			CreatedAt: now,
			UpdatedAt: now,
		}
		rec := analysis.Recommendation{
			ID:        "prov-1",
			Title:     "Provisional Item",
			Score:     0.91,
			Claimable: true,
			Action:    "Start work on this issue now",
			Actions: model.IssueActions{
				Claim: &model.IssueCommand{Shell: "br update prov-1 --status=in_progress"},
				Show:  &model.IssueCommand{Shell: "br show prov-1"},
			},
		}
		preGrouped := &analysis.TriageResult{
			QuickRef: analysis.QuickRef{
				TopPicks: []analysis.TopPick{{ID: "prov-1", Title: "Provisional Item", Score: 0.91}},
			},
			Recommendations: []analysis.Recommendation{rec},
			QuickWins:       []analysis.QuickWin{{ID: "prov-1", Title: "Provisional Item", Score: 0.91}},
			BlockersToClear: []analysis.BlockerItem{{ID: "prov-1", Title: "Provisional Item", Actionable: true}},
			Commands:        analysis.CommandHelpers{ClaimTop: "br update prov-1 --status=in_progress"},
			RecommendationsByTrack: []analysis.TrackRecommendationGroup{{
				TrackID:         "track-A",
				TopPick:         &analysis.TopPick{ID: "prov-1", Title: "Provisional Item"},
				ClaimCommand:    "br update prov-1 --status=in_progress",
				Recommendations: []analysis.Recommendation{rec},
			}},
			RecommendationsByLabel: []analysis.LabelRecommendationGroup{{
				Label:           "core",
				TopPick:         &analysis.TopPick{ID: "prov-1", Title: "Provisional Item"},
				ClaimCommand:    "br update prov-1 --status=in_progress",
				Recommendations: []analysis.Recommendation{rec},
			}},
		}

		outDir := filepath.Join(t.TempDir(), "provisional")
		exp := NewSQLiteExporter([]*model.Issue{iss}, nil, nil, preGrouped)
		exp.Config.RobotEnvelope = map[string]json.RawMessage{
			"source_authority": json.RawMessage(`{"claim_safe":false,"state":"stale_db"}`),
		}
		if err := exp.Export(outDir); err != nil {
			t.Fatalf("Export failed: %v", err)
		}

		// Caller's input struct must remain untouched!
		if preGrouped.Commands.ClaimTop == "" || len(preGrouped.QuickRef.TopPicks) == 0 || !preGrouped.Recommendations[0].Claimable {
			t.Fatalf("Export mutated caller's TriageResult in-place: %+v", preGrouped)
		}

		raw, err := os.ReadFile(filepath.Join(outDir, "data", "triage.json"))
		if err != nil {
			t.Fatalf("ReadFile triage.json: %v", err)
		}
		var scrubbed analysis.TriageResult
		if err := json.Unmarshal(raw, &scrubbed); err != nil {
			t.Fatalf("Unmarshal triage.json: %v", err)
		}
		if scrubbed.Commands.ClaimTop != "" {
			t.Fatalf("scrubbed Commands.ClaimTop = %q, want empty", scrubbed.Commands.ClaimTop)
		}
		if len(scrubbed.QuickRef.TopPicks) != 0 {
			t.Fatalf("scrubbed QuickRef.TopPicks = %+v, want empty", scrubbed.QuickRef.TopPicks)
		}
		if len(scrubbed.QuickWins) != 0 {
			t.Fatalf("scrubbed QuickWins = %+v, want empty", scrubbed.QuickWins)
		}
		if len(scrubbed.BlockersToClear) != 1 || scrubbed.BlockersToClear[0].Actionable {
			t.Fatalf("scrubbed BlockersToClear = %+v, want Actionable=false", scrubbed.BlockersToClear)
		}
		if len(scrubbed.Recommendations) != 1 || scrubbed.Recommendations[0].Claimable || scrubbed.Recommendations[0].Actions.Claim != nil {
			t.Fatalf("scrubbed Recommendations[0] = %+v, want Claimable=false and Actions.Claim=nil", scrubbed.Recommendations[0])
		}
		if scrubbed.Recommendations[0].Actions.Show == nil {
			t.Fatalf("scrubbed Recommendations[0].Actions.Show should be preserved")
		}
		if scrubbed.RecommendationsByTrack[0].ClaimCommand != "" || scrubbed.RecommendationsByTrack[0].TopPick != nil || scrubbed.RecommendationsByTrack[0].Recommendations[0].Actions.Claim != nil {
			t.Fatalf("scrubbed RecommendationsByTrack[0] = %+v", scrubbed.RecommendationsByTrack[0])
		}
		if scrubbed.RecommendationsByLabel[0].ClaimCommand != "" || scrubbed.RecommendationsByLabel[0].TopPick != nil || scrubbed.RecommendationsByLabel[0].Recommendations[0].Actions.Claim != nil {
			t.Fatalf("scrubbed RecommendationsByLabel[0] = %+v", scrubbed.RecommendationsByLabel[0])
		}
	})
}

func TestExport_EphemeralRoundTripAndLayoutActiveBetweenness(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	closedAt := now
	issues := []*model.Issue{
		{
			ID:        "open-root",
			Title:     "Open Root",
			Status:    model.StatusOpen,
			Priority:  0,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "open-bridge",
			Title:     "Active Bridge",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "open-bridge", DependsOnID: "open-root", Type: model.DepBlocks},
			},
		},
		{
			ID:        "open-top",
			Title:     "Active Top Dependent",
			Status:    model.StatusOpen,
			Priority:  2,
			IssueType: model.TypeTask,
			Ephemeral: true,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "open-top", DependsOnID: "open-bridge", Type: model.DepBlocks},
			},
		},
		{
			ID:        "closed-hub",
			Title:     "Historical Closed Hub",
			Status:    model.StatusClosed,
			ClosedAt:  &closedAt,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "closed-hub", DependsOnID: "open-root", Type: model.DepBlocks},
			},
		},
		{
			ID:        "open-after-closed",
			Title:     "Ready After Closed Hub",
			Status:    model.StatusOpen,
			Priority:  1,
			IssueType: model.TypeTask,
			CreatedAt: now,
			UpdatedAt: now,
			Dependencies: []*model.Dependency{
				{IssueID: "open-after-closed", DependsOnID: "closed-hub", Type: model.DepBlocks},
			},
		},
	}

	var valIssues []model.Issue
	var deps []*model.Dependency
	for _, iss := range issues {
		valIssues = append(valIssues, *iss)
		deps = append(deps, iss.Dependencies...)
	}
	stats := analysis.NewAnalyzer(valIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	if stats.GetBetweennessScore("closed-hub") <= 0 {
		t.Fatalf("planted negative control invalid: full-graph betweenness for closed-hub = %v, want > 0", stats.GetBetweennessScore("closed-hub"))
	}

	outDir := t.TempDir()
	exp := NewSQLiteExporter(issues, deps, &stats, nil)
	if err := exp.Export(outDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// 1. Ephemeral round-trip in issues & issue_overview_mv (+ non-ephemeral control).
	var ephTbl, ephMV, ephAct int
	if err := db.QueryRow(`SELECT ephemeral FROM issues WHERE id = 'open-top'`).Scan(&ephTbl); err != nil || ephTbl != 1 {
		t.Fatalf("open-top issues.ephemeral = %d (err=%v), want 1", ephTbl, err)
	}
	if err := db.QueryRow(`SELECT ephemeral, is_actionable FROM issue_overview_mv WHERE id = 'open-top'`).Scan(&ephMV, &ephAct); err != nil || ephMV != 1 || ephAct != 0 {
		t.Fatalf("open-top mv = (ephemeral=%d, is_actionable=%d, err=%v), want (1, 0, nil)", ephMV, ephAct, err)
	}
	var ctrlTbl, ctrlMV, ctrlAct int
	if err := db.QueryRow(`SELECT ephemeral FROM issues WHERE id = 'open-after-closed'`).Scan(&ctrlTbl); err != nil || ctrlTbl != 0 {
		t.Fatalf("open-after-closed issues.ephemeral = %d (err=%v), want 0", ctrlTbl, err)
	}
	if err := db.QueryRow(`SELECT ephemeral, is_actionable FROM issue_overview_mv WHERE id = 'open-after-closed'`).Scan(&ctrlMV, &ctrlAct); err != nil || ctrlMV != 0 || ctrlAct != 1 {
		t.Fatalf("open-after-closed mv = (ephemeral=%d, is_actionable=%d, err=%v), want (0, 1, nil)", ctrlMV, ctrlAct, err)
	}

	// 2. graph_layout.json: layout.Metrics uses activeBetweennessMap (0 for closed-hub, >0 for open-bridge)
	// while layout.Centrality preserves full exported topology contract.
	rawLayout, err := os.ReadFile(filepath.Join(outDir, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("ReadFile graph_layout.json: %v", err)
	}
	var layout GraphLayout
	if err := json.Unmarshal(rawLayout, &layout); err != nil {
		t.Fatalf("Unmarshal graph_layout.json: %v", err)
	}
	if got := layout.Metrics["closed-hub"][1]; got != 0 {
		t.Fatalf("layout.Metrics[closed-hub][1] (betweenness) = %v, want 0", got)
	}
	if got := layout.Metrics["open-bridge"][1]; got <= 0 {
		t.Fatalf("layout.Metrics[open-bridge][1] (betweenness) = %v, want > 0", got)
	}
	if layout.Centrality == nil || layout.Centrality.Betweenness["closed-hub"] <= 0 {
		t.Fatalf("layout.Centrality.Betweenness[closed-hub] = %v, want > 0 (exported topology centrality preserved)", layout.Centrality)
	}
}

func TestSQLiteExportSourceRepoColumnAndFTS(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	issues := []*model.Issue{
		{
			ID: "api-1", Title: "API Auth", Status: model.StatusOpen, Priority: 1,
			IssueType: model.TypeTask, SourceRepo: "api", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "web-1", Title: "Web Dashboard", Status: model.StatusOpen, Priority: 2,
			IssueType: model.TypeFeature, SourceRepo: "web", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "local-1", Title: "Local Single Repo", Status: model.StatusOpen, Priority: 2,
			IssueType: model.TypeTask, CreatedAt: now, UpdatedAt: now,
		},
	}

	outDir := t.TempDir()
	exp := NewSQLiteExporter(issues, nil, nil, nil)
	if err := exp.Export(outDir); err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// Verify source_repo persisted in issues and issue_overview_mv (NULL when empty).
	var repoAPI, repoWeb string
	var repoLocal sql.NullString
	if err := db.QueryRow(`SELECT source_repo FROM issues WHERE id = 'api-1'`).Scan(&repoAPI); err != nil || repoAPI != "api" {
		t.Fatalf("issues.source_repo(api-1) = %q (err=%v), want api", repoAPI, err)
	}
	if err := db.QueryRow(`SELECT source_repo FROM issue_overview_mv WHERE id = 'web-1'`).Scan(&repoWeb); err != nil || repoWeb != "web" {
		t.Fatalf("issue_overview_mv.source_repo(web-1) = %q (err=%v), want web", repoWeb, err)
	}
	if err := db.QueryRow(`SELECT source_repo FROM issue_overview_mv WHERE id = 'local-1'`).Scan(&repoLocal); err != nil || repoLocal.Valid {
		t.Fatalf("issue_overview_mv.source_repo(local-1) = %+v (err=%v), want NULL", repoLocal, err)
	}

	// Verify source_repo is indexed in issues_fts and compatible with 10-column bm25().
	var matchedID string
	var bm25Score float64
	if err := db.QueryRow(`
		SELECT i.id, bm25(issues_fts, 3.0, 2.0, 1.0, 1.5, 0.5, 0.8, 0.8, 0.8, 1.0, 0.8)
		FROM issues_fts
		JOIN issue_overview_mv i ON issues_fts.id = i.id
		WHERE issues_fts MATCH '"web"*'
		LIMIT 1
	`).Scan(&matchedID, &bm25Score); err != nil || matchedID != "web-1" {
		t.Fatalf("FTS5 10-col bm25 match for web: id=%q score=%v err=%v", matchedID, bm25Score, err)
	}

	// Verify GetExportedIssues() populates ExportIssue.SourceRepo for issues.json parity.
	exported := exp.GetExportedIssues()
	repoByID := make(map[string]string, len(exported))
	for _, ei := range exported {
		repoByID[ei.ID] = ei.SourceRepo
	}
	if repoByID["api-1"] != "api" || repoByID["web-1"] != "web" || repoByID["local-1"] != "" {
		t.Fatalf("GetExportedIssues() SourceRepo mismatch: %+v", repoByID)
	}
}

func TestWave29_A15_4_And_W29_4_SQLiteRiskAndETASuppression(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now.Add(-24 * time.Hour)
	est := 120
	rawIssues := []model.Issue{
		{ID: "api-blk", Title: "Stale cross-repo blocker", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, SourceRepo: "api", EstimatedMinutes: &est, CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now.Add(-20 * 24 * time.Hour)},
		{
			ID: "web-risky", Title: "Blocked high-churn web issue", Status: model.StatusInProgress, Priority: 0, IssueType: model.TypeBug, SourceRepo: "web", EstimatedMinutes: &est,
			CreatedAt: now.Add(-14 * 24 * time.Hour), UpdatedAt: now.Add(-10 * 24 * time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "web-risky", DependsOnID: "api-blk", Type: model.DepBlocks}},
			Comments: []*model.Comment{
				{ID: "1", Author: "a", Text: "churn 1", CreatedAt: now.Add(-13 * 24 * time.Hour)},
				{ID: "2", Author: "b", Text: "churn 2", CreatedAt: now.Add(-12 * 24 * time.Hour)},
				{ID: "3", Author: "c", Text: "churn 3", CreatedAt: now.Add(-11 * 24 * time.Hour)},
			},
		},
		{ID: "tmpl-1", Title: "Checklist template", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, IsTemplate: true, CreatedAt: now.Add(-5 * 24 * time.Hour), UpdatedAt: now},
		{ID: "pin-1", Title: "Pinned anchor", Status: model.StatusPinned, Priority: 2, IssueType: model.TypeTask, Pinned: true, CreatedAt: now.Add(-5 * 24 * time.Hour), UpdatedAt: now},
		{ID: "closed-1", Title: "Closed history sample", Status: model.StatusClosed, Priority: 1, IssueType: model.TypeTask, CreatedAt: now.Add(-10 * 24 * time.Hour), UpdatedAt: closedAt, ClosedAt: &closedAt},
	}
	an := analysis.NewAnalyzer(rawIssues)
	an.SetNow(now)
	stats := an.AnalyzeWithConfig(analysis.FullAnalysisConfig())

	ptrs := make([]*model.Issue, len(rawIssues))
	var deps []*model.Dependency
	for i := range rawIssues {
		iss := rawIssues[i]
		ptrs[i] = &iss
		deps = append(deps, iss.Dependencies...)
	}
	outDir := t.TempDir()
	exp := NewSQLiteExporter(ptrs, deps, &stats, nil)
	exp.Config.Readiness = an.Readiness()
	exp.Config.ReadinessAt = now
	if err := exp.Export(outDir); err != nil {
		t.Fatalf("SQLite Export failed: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(outDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	// W29-4: Active high-risk issue gets positive risk_score & non-empty risk_explanation in issue_overview_mv.
	var riskyScore float64
	var riskyExpl sql.NullString
	var riskyETA sql.NullFloat64
	if err := db.QueryRow(`SELECT risk_score, risk_explanation, eta_days FROM issue_overview_mv WHERE id = 'web-risky'`).Scan(&riskyScore, &riskyExpl, &riskyETA); err != nil {
		t.Fatalf("query web-risky: %v", err)
	}
	if riskyScore <= 0.25 || !riskyExpl.Valid || riskyExpl.String == "" {
		t.Fatalf("W29-4 regression: web-risky expected risk_score>0.25 and explanation, got score=%v expl=%+v", riskyScore, riskyExpl)
	}
	if !riskyETA.Valid || riskyETA.Float64 <= 0 {
		t.Fatalf("expected active web-risky to have positive eta_days, got %+v", riskyETA)
	}

	// A15-4 + W29-4: Templates, pinned, and closed issues must suppress BOTH ETA and operational risk.
	for _, id := range []string{"tmpl-1", "pin-1", "closed-1"} {
		var rScore float64
		var rExpl sql.NullString
		var etaDays sql.NullFloat64
		if err := db.QueryRow(`SELECT risk_score, risk_explanation, eta_days FROM issue_overview_mv WHERE id = ?`, id).Scan(&rScore, &rExpl, &etaDays); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if etaDays.Valid {
			t.Fatalf("A15-4 regression: %s must suppress eta_days (NULL), got %v", id, etaDays.Float64)
		}
		if rScore != 0 || rExpl.Valid {
			t.Fatalf("W29-4 regression: %s must suppress risk_score/risk_explanation, got score=%v expl=%+v", id, rScore, rExpl)
		}
	}

	// P16B-6 & P16B-7: Programmatic callers passing edges ONLY in e.Deps (not issue.Dependencies)
	// still compute cross-repo risk, and GetExportedIssues() exposes risk & ETA parity.
	depOnlyPtrs := make([]*model.Issue, len(rawIssues))
	for i := range rawIssues {
		issCopy := rawIssues[i]
		issCopy.Dependencies = nil
		depOnlyPtrs[i] = &issCopy
	}
	depOnlyExp := NewSQLiteExporter(depOnlyPtrs, deps, &stats, nil)
	depOnlyExp.Config.Readiness = an.Readiness()
	depOnlyExp.Config.ReadinessAt = now
	depOnlyDir := t.TempDir()
	if err := depOnlyExp.Export(depOnlyDir); err != nil {
		t.Fatalf("dep-only Export failed: %v", err)
	}
	depOnlyDB, err := sql.Open("sqlite", filepath.Join(depOnlyDir, "beads.sqlite3"))
	if err != nil {
		t.Fatalf("sql.Open depOnlyDB: %v", err)
	}
	defer depOnlyDB.Close()
	var depOnlyRisk float64
	if err := depOnlyDB.QueryRow(`SELECT risk_score FROM issue_overview_mv WHERE id = 'web-risky'`).Scan(&depOnlyRisk); err != nil {
		t.Fatalf("query depOnlyDB web-risky: %v", err)
	}
	if math.Abs(depOnlyRisk-riskyScore) > 1e-9 {
		t.Fatalf("P16B-6 regression: e.Deps-only risk_score=%v differed from inline Dependencies risk_score=%v", depOnlyRisk, riskyScore)
	}
	exportedIssues := depOnlyExp.GetExportedIssues()
	expByID := make(map[string]ExportIssue, len(exportedIssues))
	for _, ei := range exportedIssues {
		expByID[ei.ID] = ei
	}
	if math.Abs(expByID["web-risky"].RiskScore-riskyScore) > 1e-9 || expByID["web-risky"].RiskExplanation == "" || expByID["web-risky"].ETADays == nil {
		t.Fatalf("P16B-7 regression: GetExportedIssues() missing risk/ETA parity on web-risky: %+v", expByID["web-risky"])
	}
	if expByID["tmpl-1"].RiskScore != 0 || expByID["tmpl-1"].ETADays != nil || expByID["closed-1"].RiskScore != 0 || expByID["closed-1"].ETADays != nil {
		t.Fatalf("P16B-7 regression: GetExportedIssues() leaked risk/ETA on template/closed: tmpl=%+v closed=%+v", expByID["tmpl-1"], expByID["closed-1"])
	}
}
