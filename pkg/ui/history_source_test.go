package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/datasource"
	"github.com/Dicklesworthstone/beads_viewer/pkg/correlation"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

func TestHistoryInputCapturePreservesRealGitReports(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonl := filepath.Join(beadsDir, "issues.jsonl")
	stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dev", "GIT_COMMITTER_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_EMAIL=dev@example.com", "GIT_AUTHOR_DATE="+stamp.Format(time.RFC3339), "GIT_COMMITTER_DATE="+stamp.Format(time.RFC3339))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeBead := func(status, assignee string) {
		write(".beads/issues.jsonl", fmt.Sprintf("{\"id\":\"bv-1\",\"title\":\"One\",\"status\":%q,\"priority\":1,\"issue_type\":\"task\",\"assignee\":%q}\n", status, assignee))
	}
	commit := func(message string) {
		runGit("add", ".")
		runGit("commit", "-m", message)
		stamp = stamp.Add(time.Hour)
	}
	runGit("init", "-b", "main")
	writeBead("open", "")
	commit("seed tracker")
	writeBead("in_progress", "dev@example.com")
	write("a.go", "package fixture\n// a\n")
	commit("feat(bv-1): start work")
	write("b.go", "package fixture\n// b\n")
	commit("wip: same author no reference")
	write("c.go", "package fixture\n// c\n")
	commit("fix bv-1 follow-up")
	writeBead("closed", "dev@example.com")
	commit("chore: close bv-1")

	issues := []model.Issue{
		{ID: "bv-1", Title: "Captured 日本語 title", Status: model.StatusInProgress, Description: "ignored body", Labels: []string{"captured"}, Dependencies: []*model.Dependency{{DependsOnID: "bv-2", Type: model.DepBlocks}}, Comments: []*model.Comment{{Text: "captured comment"}}},
		{ID: "bv-2", Title: "Present without Git events", Status: model.StatusOpen},
	}
	m := NewModel(issues, nil, jsonl)
	defer m.Stop()
	cmd := m.startHistoryLoad()
	if cmd == nil {
		t.Fatal("actual history load not scheduled")
	}
	dataGeneration, requestGeneration := m.historyLoadDataGeneration, m.historyLoadRequestGeneration
	// Later UI writes and row reordering must not change this owned request.
	m.issues[0].ID = "mutated-id"
	m.issues[0].Title = "mutated title"
	m.issues[0].Status = model.StatusClosed
	m.issues[0].Labels[0] = "mutated label"
	m.issues[0].Dependencies[0].DependsOnID = "mutated dependency"
	m.issues[0].Comments[0].Text = "mutated comment"
	m.issues[0], m.issues[1] = m.issues[1], m.issues[0]
	completed := make(chan HistoryLoadedMsg, 1)
	go func() { completed <- cmd().(HistoryLoadedMsg) }()
	for i := 0; i < 1000; i++ {
		m.issues[0].Title = fmt.Sprintf("concurrent title %d", i)
		runtime.Gosched()
	}
	loaded := <-completed
	if loaded.Error != nil || loaded.Report == nil {
		t.Fatalf("real history load: %v", loaded.Error)
	}
	if loaded.DataGeneration != dataGeneration || loaded.RequestGeneration != requestGeneration {
		t.Fatal("history request lost generation identity")
	}
	history, ok := loaded.Report.Histories["bv-1"]
	if !ok || history.Title != "Captured 日本語 title" || history.Status != "in_progress" {
		t.Fatalf("captured fields changed: %+v", history)
	}
	if len(loaded.Report.Histories) != 2 || loaded.Report.Histories["bv-2"].Title != "Present without Git events" || loaded.Report.Histories["bv-2"].Status != "open" {
		t.Fatal("missing or mutated event-free issue")
	}
	if len(history.Events) < 3 || len(history.Commits) < 3 {
		t.Fatalf("missing real lifecycle/correlations: events=%d commits=%d", len(history.Events), len(history.Commits))
	}
	for _, method := range []string{"co_committed", "explicit_id", "temporal_author"} {
		if loaded.Report.Stats.MethodDistribution[method] == 0 {
			t.Fatalf("actual %s positive missing: %v", method, loaded.Report.Stats.MethodDistribution)
		}
	}
	if len(loaded.Report.Stats.Strategies) != 3 {
		t.Fatal("not all history strategies ran")
	}

	m.issues = []model.Issue{{ID: "bv-1", Title: "Regenerated title", Status: model.StatusClosed}, {ID: "bv-2", Title: "Second current title", Status: model.StatusBlocked}}
	m.beginSemanticDatasetUpdate()
	next := m.startHistoryLoad()
	if next == nil {
		t.Fatal("updated data did not regenerate history")
	}
	refreshed := next().(HistoryLoadedMsg)
	if refreshed.Error != nil || refreshed.Report == nil {
		t.Fatalf("regenerated actual history: %v", refreshed.Error)
	}
	if h := refreshed.Report.Histories["bv-1"]; h.Title != "Regenerated title" || h.Status != "closed" {
		t.Fatalf("regenerated metadata stale: %+v", h)
	}
	if refreshed.Report.DataHash == loaded.Report.DataHash {
		t.Fatal("report hash ignored changed current metadata")
	}
	m.Update(refreshed)
	if !m.historyReportIsCurrent() || m.historyView.report != refreshed.Report || m.historyLoading {
		t.Fatal("actual current completion was not installed")
	}
	if path := os.Getenv("BV_HISTORY_REPORT_OUT"); path != "" {
		// Preserve raw volatile timestamps/durations. The external before/after
		// comparison names only those existing wall-clock fields explicitly.
		data, err := json.MarshalIndent([]*correlation.HistoryReport{loaded.Report, refreshed.Report}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryInputCaptureAllocationBound(t *testing.T) {
	issues, err := testutil.PerformanceIssues("unicode", 10000, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	m := &Model{issues: issues}
	defer m.cancelHistoryLoad()
	allocations := testing.AllocsPerRun(3, func() {
		m.cancelHistoryLoad()
		if cmd := m.startHistoryLoad(); cmd == nil {
			t.Fatal("history capture did not create its actual command")
		}
	})
	// History consumes three string fields. Capture must not clone every
	// issue's nested dependencies/comments. This is not a wall-clock gate.
	t.Logf("issues=%d allocations=%.0f limit=64", len(issues), allocations)
	if allocations > 64 {
		t.Fatalf("history input capture allocated %.0f objects; limit64", allocations)
	}
}

// writeFileAt writes content to path and sets its mtime (and atime) to the given
// time so tests can deterministically reproduce sub-second mtime skew between the
// SQLite DB and the JSONL export.
func writeFileAt(t *testing.T, path string, content []byte, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestResolveHistoryCorrelationPath_PrefersJSONLOverDB is the bv #171 regression:
// when the smart data-source selector hands the History view the SQLite DB path
// (because beads.db is a few milliseconds newer than issues.jsonl after a normal
// `br sync`), the correlator must still follow the git-tracked JSONL — git history
// of the binary DB yields zero lifecycle events, so every correlation would be
// lost. resolveHistoryCorrelationPath redirects DB (or any non-JSONL) selections
// to the sibling JSONL while leaving JSONL selections untouched.
func TestResolveHistoryCorrelationPath_PrefersJSONLOverDB(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	dbPath := filepath.Join(beadsDir, "beads.db")

	// Reproduce the exact trigger: DB mtime 41ms NEWER than the JSONL.
	base := time.Now().Add(-time.Hour)
	writeFileAt(t, jsonlPath, []byte(`{"id":"bv-1","title":"x","status":"open"}`+"\n"), base)
	writeFileAt(t, dbPath, []byte("SQLite format 3\x00"), base.Add(41*time.Millisecond))

	// Sanity check: confirm the freshest-mtime selector really does pick the DB
	// under this skew (the bug's trigger). If it didn't, the regression below
	// would pass vacuously.
	sources, err := datasource.DiscoverSources(datasource.DiscoveryOptions{
		BeadsDir: beadsDir,
		RepoPath: repo,
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	if len(sources) == 0 {
		t.Fatalf("expected at least one discovered source")
	}
	if sources[0].Path != dbPath {
		t.Fatalf("precondition not met: expected freshest-mtime selection to be the DB %q, got %q (sources=%+v)", dbPath, sources[0].Path, sources)
	}

	// The fix: even though the DB was selected, History correlation must follow
	// the JSONL.
	got := resolveHistoryCorrelationPath(dbPath, repo)
	if got != jsonlPath {
		t.Fatalf("expected correlation path %q (JSONL), got %q (DB-derived selection must redirect to JSONL)", jsonlPath, got)
	}
}

// TestResolveHistoryCorrelationPath_KeepsJSONLSelection verifies that when the
// selector already chose a JSONL (e.g. JSONL is the freshest source, or the
// `touch issues.jsonl` workaround was applied), the path is preserved unchanged.
func TestResolveHistoryCorrelationPath_KeepsJSONLSelection(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	writeFileAt(t, jsonlPath, []byte(`{"id":"bv-1","title":"x","status":"open"}`+"\n"), time.Now())

	if got := resolveHistoryCorrelationPath(jsonlPath, repo); got != jsonlPath {
		t.Fatalf("JSONL selection must be preserved: want %q, got %q", jsonlPath, got)
	}

	// Case-insensitive extension match (e.g. .JSONL) is also preserved.
	upper := filepath.Join(beadsDir, "issues.JSONL")
	if got := resolveHistoryCorrelationPath(upper, repo); got != upper {
		t.Fatalf("uppercase .JSONL selection must be preserved: want %q, got %q", upper, got)
	}
}

// TestResolveHistoryCorrelationPath_FallsBackWhenNoJSONL verifies graceful
// degradation: when the selected source is a DB but no JSONL exists alongside it
// (or anywhere standard), the original path is returned so the correlator's own
// default-file resolution still runs rather than panicking or returning "".
func TestResolveHistoryCorrelationPath_FallsBackWhenNoJSONL(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	writeFileAt(t, dbPath, []byte("SQLite format 3\x00"), time.Now())

	if got := resolveHistoryCorrelationPath(dbPath, repo); got != dbPath {
		t.Fatalf("with no JSONL present, original path must be preserved: want %q, got %q", dbPath, got)
	}
}

// TestResolveHistoryCorrelationPath_EmptyPath verifies that an empty selection
// (workspace mode) is passed straight through so the correlator discovers the
// standard beads files itself.
func TestResolveHistoryCorrelationPath_EmptyPath(t *testing.T) {
	if got := resolveHistoryCorrelationPath("", t.TempDir()); got != "" {
		t.Fatalf("empty path must be preserved, got %q", got)
	}
}

// TestLoadHistoryCmd_HonoursFeedbackStore proves the History view's data
// source applies stored correlation feedback exactly like --robot-history
// (C5): a rejected (commit, bead) pair is absent from the loaded report.
func TestLoadHistoryCmd_HonoursFeedbackStore(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	write := func(status string) {
		t.Helper()
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"bv-1","title":"One","status":"`+status+`","priority":1,"issue_type":"task"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write issues.jsonl: %v", err)
		}
	}
	git("init")
	write("open")
	git("add", ".beads/issues.jsonl")
	git("commit", "-m", "seed bv-1")
	write("in_progress")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	git("add", ".beads/issues.jsonl", "main.go")
	git("commit", "-m", "feat(bv-1): start work")
	workSHA := git("rev-parse", "HEAD")

	issues := []correlation.BeadInfo{{ID: "bv-1", Title: "One", Status: "in_progress"}}
	load := func() *correlation.HistoryReport {
		t.Helper()
		msg := LoadHistoryCmd(context.Background(), issues, jsonlPath, 1, 1)()
		loaded, ok := msg.(HistoryLoadedMsg)
		if !ok {
			t.Fatalf("LoadHistoryCmd returned %T, want HistoryLoadedMsg", msg)
		}
		if loaded.Error != nil {
			t.Fatalf("history load error: %v", loaded.Error)
		}
		return loaded.Report
	}

	before := load()
	var listed bool
	for _, c := range before.Histories["bv-1"].Commits {
		if c.SHA == workSHA {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("precondition: commit %s should correlate to bv-1 before feedback: %+v", workSHA[:7], before.Histories["bv-1"].Commits)
	}

	store := correlation.NewFeedbackStore(beadsDir)
	if err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	if err := store.Reject(workSHA, "bv-1", "tester", 0.9, "not really bv-1"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	after := load()
	for _, c := range after.Histories["bv-1"].Commits {
		if c.SHA == workSHA {
			t.Fatalf("History view still lists rejected commit %s for bv-1", workSHA[:7])
		}
	}
	if fa := after.Stats.FeedbackApplied; fa == nil || fa.Rejected != 1 {
		t.Fatalf("stats.feedback_applied=%+v; want rejected=1", fa)
	}
	if _, stillIndexed := after.CommitIndex[workSHA]; stillIndexed {
		t.Fatalf("commit_index still contains rejected commit %s: %v", workSHA[:7], after.CommitIndex[workSHA])
	}
}
