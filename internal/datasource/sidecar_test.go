package datasource

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

const sidecarIssueA = `{"id":"bv-a","title":"alpha","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
const sidecarIssueB = `{"id":"bv-b","title":"beta","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
const sidecarIssueC = `{"id":"bv-c","title":"gamma (only in stale snapshot)","status":"closed","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`

// writeSidecarFixture creates .beads with a two-issue issues.jsonl and the given
// sidecar files, then bumps every sidecar's mtime so it is strictly newer than
// issues.jsonl — the exact condition under which a sidecar used to win.
func writeSidecarFixture(t *testing.T, sidecars map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	issuesPath := filepath.Join(beads, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(sidecarIssueA+"\n"+sidecarIssueB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(issuesPath, old, old); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(time.Hour)
	for name, content := range sidecars {
		p := filepath.Join(beads, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, newer, newer); err != nil {
			t.Fatal(err)
		}
	}
	return beads
}

// captureStderr runs fn while capturing everything written to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

var sidecarSet = map[string]string{
	// br's sync base: a full, VALID, stale snapshot — the dangerous one.
	"sync_base.jsonl": sidecarIssueA + "\n" + sidecarIssueB + "\n" + sidecarIssueC + "\n",
	// bv's own sprint file: no _type, no title — used to print a loader warning.
	"sprints.jsonl": `{"id":"sprint-1","name":"Sprint 1","start_date":"2026-01-01T00:00:00Z","end_date":"2026-01-14T00:00:00Z","bead_ids":["bv-a"]}` + "\n",
	// bv's correlation feedback store.
	"correlation_feedback.jsonl": `{"commit_sha":"abc","bead_id":"bv-a","type":"confirm"}` + "\n",
	// bd sidecars.
	"memories.jsonl":     `{"_type":"memory","id":"m1"}` + "\n",
	"interactions.jsonl": `{"_type":"interaction","id":"i1"}` + "\n",
	// merge artifacts and backups.
	"issues.jsonl.backup": sidecarIssueC + "\n",
	"beads.left.jsonl":    sidecarIssueC + "\n",
}

func TestDiscoverSources_IgnoresSidecars(t *testing.T) {
	beads := writeSidecarFixture(t, sidecarSet)
	var logged []string
	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir: beads,
		Verbose:  true,
		Logger:   func(msg string) { logged = append(logged, msg) },
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}
	var jsonl []string
	for _, s := range sources {
		if s.Type == SourceTypeJSONLLocal {
			jsonl = append(jsonl, filepath.Base(s.Path))
		}
	}
	if len(jsonl) != 1 || jsonl[0] != "issues.jsonl" {
		t.Fatalf("JSONL candidates = %v, want exactly [issues.jsonl]", jsonl)
	}
	for name := range sidecarSet {
		if !strings.HasSuffix(name, ".jsonl") {
			continue // non-.jsonl names are filtered before the allowlist and never logged
		}
		found := false
		for _, msg := range logged {
			if strings.Contains(msg, "Skipping "+name) {
				found = true
			}
		}
		if !found {
			t.Errorf("verbose log has no skip line for %s; got %v", name, logged)
		}
	}
}

func TestDiscoverSources_AllowlistNamesAreCandidates(t *testing.T) {
	for _, name := range loader.PreferredJSONLNames {
		if !isIssueFileName(name) {
			t.Errorf("%s must be an issue file name", name)
		}
	}
	for name := range sidecarSet {
		if isIssueFileName(name) {
			t.Errorf("%s must not be an issue file name", name)
		}
	}
}

func TestLoadIssues_FresherSyncBaseDoesNotWin(t *testing.T) {
	beads := writeSidecarFixture(t, sidecarSet)
	wantHash := func() string {
		clean, err := loader.LoadIssuesFromFileWithOptions(filepath.Join(beads, "issues.jsonl"), loader.ParseOptions{WarningHandler: func(string) {}})
		if err != nil {
			t.Fatal(err)
		}
		return analysis.ComputeDataHash(clean)
	}()

	var loaded LoadResult
	stderr := captureStderr(t, func() {
		var err error
		loaded, err = LoadIssuesFromDir(beads)
		got := loaded.Issues
		if err != nil {
			t.Fatalf("LoadIssuesFromDir: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d issues, want 2 (a fresher sync_base.jsonl with 3 issues must not win)", len(got))
		}
		if h := analysis.ComputeDataHash(got); h != wantHash {
			t.Errorf("data hash %s != issues.jsonl hash %s", h, wantHash)
		}
	})
	if strings.Contains(stderr, "skipping invalid issue") || strings.Contains(stderr, "Warning") {
		t.Errorf("probing sidecars leaked warnings to stderr:\n%s", stderr)
	}
	rep := &loaded.Report
	if filepath.Base(rep.Path) != "issues.jsonl" {
		t.Fatalf("load report = %+v, want issues.jsonl", rep)
	}
	if rep.Errors != 0 || rep.Valid != 2 {
		t.Errorf("load report = %+v, want 2 valid / 0 errors", rep)
	}
}

// A legitimately named but corrupt candidate that loses the freshness race must
// not leak its warnings; the selected source's own warnings still print.
func TestLoadIssues_RejectedCandidateIsSilentSelectedStillWarns(t *testing.T) {
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	// issues.jsonl: one good record, one malformed line (must warn once).
	issuesPath := filepath.Join(beads, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(sidecarIssueA+"\n{not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(issuesPath, old, old)
	// beads.jsonl: newer, allowlisted name, but every line is garbage so the
	// error-rate gate rejects it and loadSmart falls through to issues.jsonl.
	badPath := filepath.Join(beads, "beads.jsonl")
	if err := os.WriteFile(badPath, []byte("{bad 1\n{bad 2\n{bad 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(time.Hour)
	_ = os.Chtimes(badPath, newer, newer)

	t.Setenv("BV_ROBOT", "")
	var got int
	var loaded LoadResult
	stderr := captureStderr(t, func() {
		var err error
		loaded, err = LoadIssuesFromDir(beads)
		if err != nil {
			t.Fatalf("LoadIssuesFromDir: %v", err)
		}
		got = len(loaded.Issues)
	})
	if got != 1 {
		t.Fatalf("got %d issues, want 1 from issues.jsonl", got)
	}
	if strings.Contains(stderr, "bad 1") || strings.Count(stderr, "Warning:") != 1 {
		t.Errorf("stderr should carry exactly the selected source's one warning, got:\n%s", stderr)
	}
	if rep := loaded.Report; filepath.Base(rep.Path) != "issues.jsonl" || rep.Errors != 1 {
		t.Errorf("load report = %+v, want issues.jsonl with 1 error", rep)
	}
}

func TestLoadIssues_HonoursMaxLineSizeEnv(t *testing.T) {
	dir := t.TempDir()
	beads := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	// One normal record plus one whose description is ~3 MB.
	big := strings.Repeat("x", 3*1024*1024)
	huge := `{"id":"bv-huge","title":"huge","description":"` + big + `","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(beads, "issues.jsonl"), []byte(sidecarIssueA+"\n"+huge+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(loader.MaxLineSizeEnvVar, "2")
	t.Setenv("BV_ROBOT", "1") // keep the oversized-line warning out of the test output
	loaded, err := LoadIssuesFromDir(beads)
	got := loaded.Issues
	if err != nil {
		t.Fatalf("with 2MB cap: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("with 2MB cap got %d issues, want 1 (oversized line dropped)", len(got))
	}
	if rep := loaded.Report; rep.Errors == 0 {
		t.Errorf("dropped oversized line must be counted in the load report, got %+v", rep)
	}

	t.Setenv(loader.MaxLineSizeEnvVar, "4")
	loaded, err = LoadIssuesFromDir(beads)
	got = loaded.Issues
	if err != nil {
		t.Fatalf("with 4MB cap: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("with 4MB cap got %d issues, want 2", len(got))
	}
}

func TestMaxLineSizeFromEnv_Validation(t *testing.T) {
	cases := map[string]int{
		"":     0,
		"abc":  0,
		"0":    0,
		"-3":   0,
		"7":    7 * 1024 * 1024,
		"9999": 1024 * 1024 * 1024,
	}
	for raw, want := range cases {
		t.Setenv(loader.MaxLineSizeEnvVar, raw)
		if got := loader.MaxLineSizeFromEnv(); got != want {
			t.Errorf("BV_MAX_LINE_SIZE_MB=%q -> %d, want %d", raw, got, want)
		}
	}
}
