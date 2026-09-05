package main_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestExportPagesSourceAuthorityAndWatchReload(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)
	root := t.TempDir()
	beads := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beads, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beads, "issues.jsonl")
	valid := []byte(`{"id":"safe","title":"Exportable work","status":"open","issue_type":"task"}` + "\n")
	if err := os.WriteFile(path, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "export")
	logPath := filepath.Join(root, "watch.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(bv, "--export-pages", output, "--watch-export", "--pages-include-history=false", "--no-hooks")
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	type payload struct {
		DataHash      string `json:"data_hash"`
		AuthorityHash string `json:"authority_hash"`
		Authority     struct {
			State     string `json:"state"`
			ClaimSafe bool   `json:"claim_safe"`
		} `json:"source_authority"`
		Commands struct {
			ClaimTop string `json:"claim_top"`
		} `json:"commands"`
		Recommendations []struct {
			ID        string `json:"id"`
			Claimable bool   `json:"claimable"`
		} `json:"recommendations"`
	}
	waitForState := func(state string) payload {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(filepath.Join(output, "data", "triage.json"))
			var got payload
			if err == nil && json.Unmarshal(data, &got) == nil && got.Authority.State == state {
				complete := true
				for _, name := range []string{"meta.json", "project_health.json", "graph_layout.json"} {
					otherData, readErr := os.ReadFile(filepath.Join(output, "data", name))
					var other payload
					if readErr != nil || json.Unmarshal(otherData, &other) != nil || other.AuthorityHash != got.AuthorityHash {
						complete = false
						break
					}
				}
				if complete {
					return got
				}
			}
			select {
			case err := <-done:
				logs, _ := os.ReadFile(logPath)
				t.Fatalf("watch exited before %s: %v\n%s", state, err, logs)
			default:
			}
			time.Sleep(25 * time.Millisecond)
		}
		logs, _ := os.ReadFile(logPath)
		t.Fatalf("watch did not publish %s authority\n%s", state, logs)
		return payload{}
	}
	healthy := waitForState("complete")
	if !healthy.Authority.ClaimSafe || healthy.Commands.ClaimTop != "" || len(healthy.Recommendations) != 1 || healthy.Recommendations[0].ID != "safe" || !healthy.Recommendations[0].Claimable {
		t.Fatalf("healthy graph must retain the ready issue without inventing a tracker route: %+v", healthy)
	}
	if err := os.WriteFile(path, append(append([]byte(nil), valid...), []byte("{invalid\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	partial := waitForState("partial")
	if partial.Authority.ClaimSafe || partial.Commands.ClaimTop != "" || len(partial.Recommendations) != 1 || partial.Recommendations[0].ID != "safe" || partial.Recommendations[0].Claimable {
		t.Fatalf("partial export must retain useful work but withhold claims: %+v", partial)
	}
	if partial.DataHash != healthy.DataHash || partial.AuthorityHash == healthy.AuthorityHash {
		t.Fatalf("hidden parse loss did not trigger authority-aware re-export: healthy=%+v partial=%+v", healthy, partial)
	}
	for _, name := range []string{"meta.json", "project_health.json", "graph_layout.json"} {
		data, err := os.ReadFile(filepath.Join(output, "data", name))
		if err != nil {
			t.Fatal(err)
		}
		var exported payload
		if err := json.Unmarshal(data, &exported); err != nil {
			t.Fatal(err)
		}
		if exported.Authority.State != "partial" || exported.AuthorityHash != partial.AuthorityHash {
			t.Fatalf("%s lost current authority: %s", name, data)
		}
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState("unknown")
	if failed.Authority.ClaimSafe || failed.Commands.ClaimTop != "" {
		t.Fatalf("failed reload left proven claims in the bundle: %+v", failed)
	}
	if err := os.WriteFile(path, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	recovered := waitForState("complete")
	if !recovered.Authority.ClaimSafe || recovered.Commands.ClaimTop != "" || recovered.AuthorityHash != healthy.AuthorityHash || len(recovered.Recommendations) != 1 || recovered.Recommendations[0].ID != "safe" || !recovered.Recommendations[0].Claimable {
		t.Fatalf("restored source did not restore graph readiness without an unbound command: %+v", recovered)
	}
}

func TestPartialWorkspaceExportArtifactsRetainAuthority(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)
	root := writeWorkspaceFixture(t)
	config := filepath.Join(root, ".bv", "workspace.yaml")
	contents, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	contents = bytes.Replace(contents, []byte("path: apps/web"), []byte("path: missing-web"), 1)
	if err := os.WriteFile(config, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"pages", "graph", "brief", "script"} {
		t.Run(format, func(t *testing.T) {
			output := filepath.Join(root, format)
			args := []string{"--workspace", config}
			switch format {
			case "pages":
				args = append(args, "--export-pages", output, "--pages-include-history=false", "--no-hooks")
			case "graph":
				output += ".html"
				args = append(args, "--export-graph", output)
			case "brief":
				args = append(args, "--agent-brief", output)
			case "script":
				args = append(args, "--emit-script")
			}
			cmd := exec.Command(bv, args...)
			cmd.Dir = root
			logs, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, logs)
			}
			var data []byte
			switch format {
			case "pages":
				data, err = os.ReadFile(filepath.Join(output, "data", "triage.json"))
			case "graph":
				data, err = os.ReadFile(output)
				if err == nil {
					match := regexp.MustCompile(`const DATA = (\{[^\n]+\});`).FindSubmatch(data)
					if len(match) != 2 {
						t.Fatalf("missing embedded graph JSON")
					}
					data = match[1]
				}
			case "brief":
				data, err = os.ReadFile(filepath.Join(output, "triage.json"))
			case "script":
				if bytes.Contains(logs, []byte("--claim")) || !bytes.Contains(logs, []byte(`"claim_safe":false`)) || !bytes.Contains(logs, []byte("api-AUTH-1:")) || !bytes.Contains(logs, []byte("# No verified live tracker route")) {
					t.Fatalf("partial unbound script must preserve issue identity and withhold unverified commands:\n%s", logs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("%v\n%s", err, data)
			}
			authority, ok := payload["source_authority"].(map[string]any)
			if !ok || authority["state"] != "partial" || authority["claim_safe"] != false || !bytes.Contains(data, []byte("api-AUTH-1")) {
				t.Fatalf("export lost useful source data or diagnostics: %s", data)
			}
			if regexp.MustCompile(`"claimable"\s*:\s*true`).Match(data) || regexp.MustCompile(`"claim_top"\s*:\s*"[^\"]+"`).Match(data) {
				t.Fatalf("partial artifact emitted claim: %s", data)
			}
		})
	}
}

func TestExportPages_IncludesHistoryAndRunsHooks(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir, _ := createHistoryRepo(t)
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Configure hooks to prove pre/post phases run.
	if err := os.MkdirAll(filepath.Join(repoDir, ".bv"), 0o755); err != nil {
		t.Fatalf("mkdir .bv: %v", err)
	}
	hooksYAML := `hooks:
  pre-export:
    - name: pre
      command: 'mkdir -p "$BV_EXPORT_PATH" && echo pre > "$BV_EXPORT_PATH/pre-hook.txt"'
  post-export:
    - name: post
      command: 'echo post > "$BV_EXPORT_PATH/post-hook.txt"'
`
	if err := os.WriteFile(filepath.Join(repoDir, ".bv", "hooks.yaml"), []byte(hooksYAML), 0o644); err != nil {
		t.Fatalf("write hooks.yaml: %v", err)
	}

	cmd := exec.Command(bv,
		"--export-pages", exportDir,
		"--pages-include-history",
		"--pages-include-closed",
	)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Core artifacts.
	for _, p := range []string{
		filepath.Join(exportDir, "index.html"),
		filepath.Join(exportDir, "beads.sqlite3"),
		filepath.Join(exportDir, "beads.sqlite3.config.json"),
		filepath.Join(exportDir, "hybrid_scorer.js"),
		filepath.Join(exportDir, "wasm_loader.js"),
		filepath.Join(exportDir, "data", "meta.json"),
		filepath.Join(exportDir, "data", "triage.json"),
		filepath.Join(exportDir, "data", "history.json"),
		filepath.Join(exportDir, "pre-hook.txt"),
		filepath.Join(exportDir, "post-hook.txt"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing export artifact %s: %v", p, err)
		}
	}

	// Verify vendored scripts are present (all scripts are now local, not CDN)
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), "vendor/") {
		t.Fatalf("index.html missing vendored script references")
	}

	// History JSON should include at least one commit entry.
	historyBytes, err := os.ReadFile(filepath.Join(exportDir, "data", "history.json"))
	if err != nil {
		t.Fatalf("read history.json: %v", err)
	}
	var history struct {
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(historyBytes, &history); err != nil {
		t.Fatalf("history.json decode: %v", err)
	}
	if len(history.Commits) == 0 || history.Commits[0].SHA == "" {
		t.Fatalf("expected at least one commit in history.json, got %+v", history.Commits)
	}
}

func stageViewerAssets(t *testing.T, bvPath string) {
	t.Helper()
	root := findRepoRoot(t)
	src := filepath.Join(root, "pkg", "export", "viewer_assets")
	dst := filepath.Join(filepath.Dir(bvPath), "pkg", "export", "viewer_assets")

	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("stage viewer assets: %v", err)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found starting at %s", dir)
		}
		dir = parent
	}
}

func copyDirRecursive(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDirRecursive(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// ============================================================================
// Static Bundle Validation Tests (bv-ct7m)
// ============================================================================

// TestExportPages_HTMLStructure validates the HTML5 document structure
func TestExportPages_HTMLStructure(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 5)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// HTML5 doctype (case-insensitive check)
	if !strings.Contains(strings.ToLower(html), "<!doctype html>") {
		t.Error("missing HTML5 doctype")
	}

	// Required meta tags
	checks := []struct {
		name    string
		pattern string
	}{
		{"charset meta", `charset="UTF-8"`},
		{"viewport meta", `name="viewport"`},
		{"html lang attribute", `<html lang=`},
		{"title tag", `<title>`},
	}
	for _, c := range checks {
		if !strings.Contains(html, c.pattern) {
			t.Errorf("missing %s (pattern: %s)", c.name, c.pattern)
		}
	}

	// Security headers (CSP). The exported index must forbid inline scripts:
	// every script is a same-origin file, so an injected <script> or on*=
	// handler cannot run even if a rendering bug reintroduces an XSS sink.
	if !strings.Contains(html, "Content-Security-Policy") {
		t.Error("missing Content-Security-Policy meta tag")
	}
	scriptSrc := cspDirective(t, html, "script-src")
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("script-src must not allow 'unsafe-inline': %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'wasm-unsafe-eval'") {
		t.Errorf("script-src must allow 'wasm-unsafe-eval' for sql.js and bv_graph_bg.wasm: %q", scriptSrc)
	}
	for _, tag := range regexp.MustCompile(`(?is)<script\b[^>]*>`).FindAllString(html, -1) {
		if !regexp.MustCompile(`(?i)\bsrc\s*=`).MatchString(tag) {
			t.Errorf("exported index.html still contains an inline script block: %s", tag)
		}
	}
	if m := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).FindString(html); m != "" {
		t.Errorf("exported index.html contains an inline event handler attribute: %s", m)
	}
}

// cspDirective returns the value of one directive from the exported page's
// Content-Security-Policy meta tag (the directives are separated by ';').
func cspDirective(t *testing.T, html, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?is)http-equiv="Content-Security-Policy"\s+content="([^"]*)"`).FindStringSubmatch(html)
	if m == nil {
		t.Fatal("cannot locate the Content-Security-Policy meta content")
	}
	for _, directive := range strings.Split(m[1], ";") {
		fields := strings.Fields(directive)
		if len(fields) > 0 && fields[0] == name {
			return strings.Join(fields[1:], " ")
		}
	}
	t.Fatalf("CSP has no %s directive: %q", name, m[1])
	return ""
}

func TestExportPages_IssueOverviewMetrics(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createRepoWithDeps(t)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	db, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open database %s: %v", dbPath, err)
	}
	defer db.Close()

	rows, err := db.Query("PRAGMA table_info(issue_overview_mv)")
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error: %v", err)
	}

	required := []string{"pagerank", "betweenness", "blocker_count", "dependent_count", "critical_depth", "in_cycle"}
	for _, col := range required {
		if !columns[col] {
			t.Fatalf("missing column %q in issue_overview_mv", col)
		}
	}

	type metricsRow struct {
		blockerCount   int
		dependentCount int
		criticalDepth  int
		inCycle        int
	}

	assertMetrics := func(id string, wantBlockers, wantDependents int) {
		t.Helper()
		var row metricsRow
		err := db.QueryRow(`SELECT blocker_count, dependent_count, critical_depth, in_cycle FROM issue_overview_mv WHERE id = ?`, id).
			Scan(&row.blockerCount, &row.dependentCount, &row.criticalDepth, &row.inCycle)
		if err != nil {
			t.Fatalf("query metrics for %s: %v", id, err)
		}
		if row.blockerCount != wantBlockers {
			t.Fatalf("%s blocker_count=%d, want %d", id, row.blockerCount, wantBlockers)
		}
		if row.dependentCount != wantDependents {
			t.Fatalf("%s dependent_count=%d, want %d", id, row.dependentCount, wantDependents)
		}
		if row.criticalDepth < 0 {
			t.Fatalf("%s critical_depth=%d, want >= 0", id, row.criticalDepth)
		}
		if row.inCycle != 0 {
			t.Fatalf("%s in_cycle=%d, want 0", id, row.inCycle)
		}
	}

	assertMetrics("root-a", 0, 1)
	assertMetrics("child-b", 1, 1)
	assertMetrics("leaf-c", 1, 0)
}

// TestExportPages_CSSPresent validates CSS files are included
func TestExportPages_CSSPresent(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check styles.css exists
	stylesPath := filepath.Join(exportDir, "styles.css")
	info, err := os.Stat(stylesPath)
	if err != nil {
		t.Fatalf("styles.css not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("styles.css is empty")
	}

	// Check index.html references the stylesheet
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), `href="styles.css"`) {
		t.Error("index.html doesn't reference styles.css")
	}
}

// TestExportPages_JavaScriptFiles validates JS files are present
func TestExportPages_JavaScriptFiles(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Required JS files (charts.js is embedded in index.html, not separate)
	jsFiles := []string{
		"viewer.js",
		"graph.js",
		"coi-serviceworker.js",
	}

	for _, jsFile := range jsFiles {
		path := filepath.Join(exportDir, jsFile)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s not found: %v", jsFile, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", jsFile)
		}
	}

	// Vendor files
	vendorFiles := []string{
		"vendor/bv_graph.js",
		"vendor/bv_graph_bg.wasm",
	}
	for _, vf := range vendorFiles {
		path := filepath.Join(exportDir, vf)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("vendor file %s not found: %v", vf, err)
		}
	}
}

// TestExportPages_SQLiteDatabase validates the SQLite export
func TestExportPages_SQLiteDatabase(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 10)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check database exists and is non-empty
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("beads.sqlite3 not found: %v", err)
	}
	if info.Size() < 1024 {
		t.Errorf("beads.sqlite3 suspiciously small: %d bytes", info.Size())
	}

	// Check config.json exists
	configPath := filepath.Join(exportDir, "beads.sqlite3.config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("beads.sqlite3.config.json not found: %v", err)
	}

	var config struct {
		Chunked   bool  `json:"chunked"`
		TotalSize int64 `json:"total_size"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	if config.TotalSize == 0 {
		t.Error("config.json reports total_size of 0")
	}
}

// TestExportPages_TriageJSON validates triage data export
func TestExportPages_TriageJSON(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 5)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check triage.json exists and has expected structure
	triagePath := filepath.Join(exportDir, "data", "triage.json")
	triageBytes, err := os.ReadFile(triagePath)
	if err != nil {
		t.Fatalf("triage.json not found: %v", err)
	}

	var triage struct {
		Recommendations []struct {
			ID    string  `json:"id"`
			Score float64 `json:"score"`
		} `json:"recommendations"`
		ProjectHealth struct {
			StatusCounts map[string]int `json:"status_counts"`
		} `json:"project_health"`
	}
	if err := json.Unmarshal(triageBytes, &triage); err != nil {
		t.Fatalf("parse triage.json: %v", err)
	}

	// Should have recommendations for open issues
	if len(triage.Recommendations) == 0 {
		t.Error("triage.json has no recommendations")
	}
}

// TestExportPages_MetaJSON validates metadata export
func TestExportPages_MetaJSON(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 5)
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Use --pages-include-closed to include all 5 issues
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-title", "Test Dashboard", "--pages-include-closed")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	metaPath := filepath.Join(exportDir, "data", "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	var meta struct {
		Version     string `json:"version"`
		GeneratedAt string `json:"generated_at"`
		IssueCount  int    `json:"issue_count"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}

	if meta.Version == "" {
		t.Error("meta.json missing version")
	}
	if meta.GeneratedAt == "" {
		t.Error("meta.json missing generated_at")
	}
	if meta.IssueCount != 5 {
		t.Errorf("meta.json issue_count = %d, want 5", meta.IssueCount)
	}
	if meta.Title != "Test Dashboard" {
		t.Errorf("meta.json title = %q, want %q", meta.Title, "Test Dashboard")
	}
}

// TestExportPages_DependencyGraph validates graph data for issues with deps
func TestExportPages_DependencyGraph(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createRepoWithDeps(t)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Triage should show blocked issues
	triagePath := filepath.Join(exportDir, "data", "triage.json")
	triageBytes, err := os.ReadFile(triagePath)
	if err != nil {
		t.Fatalf("triage.json not found: %v", err)
	}

	var triage struct {
		ProjectHealth struct {
			StatusCounts map[string]int `json:"status_counts"`
		} `json:"project_health"`
	}
	if err := json.Unmarshal(triageBytes, &triage); err != nil {
		t.Fatalf("parse triage.json: %v", err)
	}

	// Our test data has blocked issues
	if triage.ProjectHealth.StatusCounts["blocked"] == 0 {
		t.Log("Note: No blocked issues in triage (might be expected if deps don't cause blocked status)")
	}
}

// TestExportPages_DataScale_10Issues tests with 10 issues
func TestExportPages_DataScale_10Issues(t *testing.T) {
	testExportPagesWithScale(t, 10)
}

// TestExportPages_DataScale_100Issues tests with 100 issues
func TestExportPages_DataScale_100Issues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large scale test in short mode")
	}
	testExportPagesWithScale(t, 100)
}

func testExportPagesWithScale(t *testing.T, issueCount int) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, issueCount)
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Use --pages-include-closed to include all issues
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--export-pages failed with %d issues: %v\n%s", issueCount, err, out)
	}

	// Verify meta.json has correct count
	metaPath := filepath.Join(exportDir, "data", "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("meta.json not found: %v", err)
	}

	var meta struct {
		IssueCount int `json:"issue_count"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if meta.IssueCount != issueCount {
		t.Errorf("issue_count = %d, want %d", meta.IssueCount, issueCount)
	}

	// Verify database size scales appropriately
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("beads.sqlite3 not found: %v", err)
	}
	// Rough check: db should be at least 100 bytes per issue
	minExpectedSize := int64(issueCount * 100)
	if info.Size() < minExpectedSize {
		t.Errorf("database size %d bytes seems too small for %d issues (expected at least %d)",
			info.Size(), issueCount, minExpectedSize)
	}
}

// TestExportPages_DarkModeSupport validates dark mode CSS classes
func TestExportPages_DarkModeSupport(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Check for dark mode infrastructure
	darkModeIndicators := []string{
		"darkMode",             // Tailwind darkMode config
		"dark:",                // Tailwind dark: prefix classes
		"dark-mode",            // Generic dark mode references
		"prefers-color-scheme", // Media query detection
	}

	found := false
	for _, indicator := range darkModeIndicators {
		if strings.Contains(html, indicator) {
			found = true
			break
		}
	}
	if !found {
		t.Error("no dark mode support indicators found in index.html")
	}
}

// TestExportPages_NoXSSVulnerabilities checks for basic XSS protections
func TestExportPages_NoXSSVulnerabilities(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	// Create repo with potentially dangerous content
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Issue with XSS attempt in title
	jsonl := `{"id": "xss-1", "title": "<script>alert('xss')</script>", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "xss-2", "title": "Normal issue", "description": "<img onerror='alert(1)' src='x'>", "status": "open", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Check that CSP header is present (provides XSS protection)
	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(indexBytes), "Content-Security-Policy") {
		t.Error("missing Content-Security-Policy for XSS protection")
	}
}

// TestExportPages_ResponsiveLayout checks for responsive design markers
func TestExportPages_ResponsiveLayout(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	indexBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(indexBytes)

	// Check for viewport meta tag (essential for responsive design)
	if !strings.Contains(html, "viewport") {
		t.Error("missing viewport meta tag")
	}

	// Check for responsive classes (Tailwind breakpoints)
	responsiveIndicators := []string{
		"sm:",    // Small breakpoint
		"md:",    // Medium breakpoint
		"lg:",    // Large breakpoint
		"max-w-", // Max width containers
	}

	foundResponsive := 0
	for _, indicator := range responsiveIndicators {
		if strings.Contains(html, indicator) {
			foundResponsive++
		}
	}
	if foundResponsive < 2 {
		t.Errorf("only found %d responsive design indicators, expected at least 2", foundResponsive)
	}
}

// ============================================================================
// Test Helpers for bv-ct7m
// ============================================================================

// createSimpleRepo creates a test repo with N simple issues
func createSimpleRepo(t *testing.T, issueCount int) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	var issues strings.Builder
	for i := 1; i <= issueCount; i++ {
		status := "open"
		if i%5 == 0 {
			status = "closed"
		} else if i%3 == 0 {
			status = "in_progress"
		}
		priority := i % 5
		issueType := "task"
		if i%7 == 0 {
			issueType = "bug"
		} else if i%10 == 0 {
			issueType = "feature"
		}

		line := `{"id": "issue-` + itoa(i) + `", "title": "Test Issue ` + itoa(i) + `", "description": "Description for issue ` + itoa(i) + `", "status": "` + status + `", "priority": ` + itoa(priority) + `, "issue_type": "` + issueType + `"}` + "\n"
		issues.WriteString(line)
	}

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(issues.String()), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return repoDir
}

// createRepoWithDeps creates a test repo with dependency relationships
func createRepoWithDeps(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create a dependency chain: A <- B <- C (C blocked by B, B blocked by A)
	// NOTE: dependencies use "depends_on_id" field (not "target_id")
	jsonl := `{"id": "root-a", "title": "Root Task A", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "child-b", "title": "Child Task B", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"depends_on_id": "root-a", "type": "blocks"}]}
{"id": "leaf-c", "title": "Leaf Task C", "status": "blocked", "priority": 2, "issue_type": "task", "dependencies": [{"depends_on_id": "child-b", "type": "blocks"}]}
{"id": "independent-d", "title": "Independent Task D", "status": "open", "priority": 1, "issue_type": "bug"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return repoDir
}

// itoa is a simple int to string helper
func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// ============================================================================
// bv-qnlb: E2E Tests for Pages Export Options
// Tests for --pages-include-closed and --pages-include-history flags
// ============================================================================

// TestExportPages_ExcludeClosed_SQLiteVerification verifies closed issues
// are NOT in the SQLite database when --pages-include-closed=false.
func TestExportPages_ExcludeClosed_SQLiteVerification(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create mix of open and closed issues
	issueData := `{"id": "open-1", "title": "Open Issue One", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "open-2", "title": "Open Issue Two", "status": "open", "priority": 2, "issue_type": "bug"}
{"id": "closed-1", "title": "Closed Issue One", "status": "closed", "priority": 1, "issue_type": "task"}
{"id": "closed-2", "title": "Closed Issue Two", "status": "closed", "priority": 2, "issue_type": "feature"}
{"id": "inprogress-1", "title": "In Progress Issue", "status": "in_progress", "priority": 1, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")

	// Export with --pages-include-closed=false
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed=false")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify SQLite database content
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	issues := queryAllIssues(t, dbPath)

	// Should have 3 non-closed issues (2 open + 1 in_progress)
	if len(issues) != 3 {
		t.Errorf("SQLite issue count = %d, want 3 (excluding 2 closed)", len(issues))
	}

	// Verify closed issues are NOT in database
	for _, issue := range issues {
		if issue.Status == "closed" {
			t.Errorf("Found closed issue %s in database, should be excluded", issue.ID)
		}
	}

	// Verify open issues ARE in database
	foundOpen1 := false
	foundOpen2 := false
	foundInProgress := false
	for _, issue := range issues {
		switch issue.ID {
		case "open-1":
			foundOpen1 = true
		case "open-2":
			foundOpen2 = true
		case "inprogress-1":
			foundInProgress = true
		}
	}
	if !foundOpen1 || !foundOpen2 || !foundInProgress {
		t.Errorf("Missing expected issues: open-1=%v, open-2=%v, inprogress-1=%v",
			foundOpen1, foundOpen2, foundInProgress)
	}
}

// TestExportPages_ExcludeHistory verifies history.json is absent
// when --pages-include-history=false.
func TestExportPages_ExcludeHistory(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir, _ := createHistoryRepo(t)
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Export with --pages-include-history=false
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-history=false")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify history.json does NOT exist
	historyPath := filepath.Join(exportDir, "data", "history.json")
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Error("history.json should NOT exist when --pages-include-history=false")
	}

	// Verify other core files still exist
	for _, p := range []string{
		filepath.Join(exportDir, "index.html"),
		filepath.Join(exportDir, "beads.sqlite3"),
		filepath.Join(exportDir, "data", "meta.json"),
		filepath.Join(exportDir, "data", "triage.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing expected artifact %s: %v", p, err)
		}
	}
}

func TestExportPages_WithoutGitHistory(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)
	repo := createSimpleRepo(t, 2)
	if _, err := os.Stat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected fixture without local Git history, got %v", err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	cmd := exec.Command(bv, "--export-pages", out)
	cmd.Dir = repo
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("export without Git history failed: %v\n%s", err, combined)
	}
	warning := "Warning: failed to generate history: not a git repository: " + repo
	if !strings.Contains(string(combined), warning) {
		t.Fatalf("missing optional-history warning %q:\n%s", warning, combined)
	}
	if _, err := os.Stat(filepath.Join(out, "data", "history.json")); !os.IsNotExist(err) {
		t.Fatalf("history.json should be absent without Git history, got %v", err)
	}
	for _, name := range []string{"index.html", "beads.sqlite3", "data/meta.json", "data/triage.json", "data/graph_layout.json"} {
		info, err := os.Stat(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("required bundle file %s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("required bundle file %s is empty", name)
		}
	}
	issues := queryAllIssues(t, filepath.Join(out, "beads.sqlite3"))
	ids := make(map[string]bool, len(issues))
	for _, issue := range issues {
		ids[issue.ID] = true
	}
	if len(issues) != 2 || !ids["issue-1"] || !ids["issue-2"] {
		t.Fatalf("export without history lost fixture issues: %+v", issues)
	}
}

// TestExportPages_BothExcluded verifies minimal export with both flags false.
func TestExportPages_BothExcluded(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir, _ := createHistoryRepo(t)
	exportDir := filepath.Join(repoDir, "bv-pages")

	// Export with both exclusions
	cmd := exec.Command(bv, "--export-pages", exportDir,
		"--pages-include-closed=false",
		"--pages-include-history=false")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify history.json does NOT exist
	historyPath := filepath.Join(exportDir, "data", "history.json")
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Error("history.json should NOT exist")
	}

	// Verify SQLite has no closed issues
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	issues := queryAllIssues(t, dbPath)
	for _, issue := range issues {
		if issue.Status == "closed" {
			t.Errorf("Found closed issue %s in database, should be excluded", issue.ID)
		}
	}
}

// TestExportPages_FTS5Searchable verifies the FTS5 index is created and searchable.
func TestExportPages_FTS5Searchable(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create issues with searchable content
	issueData := `{"id": "auth-1", "title": "Implement OAuth2 authentication", "description": "Add Google and GitHub OAuth providers", "status": "open", "priority": 1, "issue_type": "feature"}
{"id": "api-1", "title": "REST API rate limiting", "description": "Implement token bucket algorithm for rate limiting", "status": "open", "priority": 2, "issue_type": "task"}
{"id": "bug-1", "title": "Fix login redirect bug", "description": "Users are redirected incorrectly after OAuth callback", "status": "open", "priority": 1, "issue_type": "bug"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Test FTS5 search queries
	dbPath := filepath.Join(exportDir, "beads.sqlite3")

	// Search for "OAuth" - should find 2 issues (auth-1 and bug-1)
	oauthResults := searchFTS(t, dbPath, "OAuth")
	if len(oauthResults) != 2 {
		t.Errorf("FTS search for 'OAuth' returned %d results, want 2", len(oauthResults))
	}

	// Search for "rate limiting" - should find 1 issue (api-1)
	rateResults := searchFTS(t, dbPath, "rate limiting")
	if len(rateResults) != 1 {
		t.Errorf("FTS search for 'rate limiting' returned %d results, want 1", len(rateResults))
	}

	// Search for "nonexistent term" - should find 0 issues
	emptyResults := searchFTS(t, dbPath, "nonexistent_xyz_term")
	if len(emptyResults) != 0 {
		t.Errorf("FTS search for nonexistent term returned %d results, want 0", len(emptyResults))
	}
}

// TestExportPages_EmptyProject verifies export handles empty project gracefully.
func TestExportPages_EmptyProject(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create empty issues file
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(""), 0o644); err != nil {
		t.Fatalf("write empty issues.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()

	// Empty project should either succeed with 0 issues or fail gracefully
	if err != nil {
		// Acceptable: might fail with "no issues" error
		t.Logf("Export with empty project failed (acceptable): %v\n%s", err, out)
		return
	}

	// If it succeeded, verify meta.json shows 0 issues
	metaPath := filepath.Join(exportDir, "data", "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}

	var meta struct {
		IssueCount int `json:"issue_count"`
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	if meta.IssueCount != 0 {
		t.Errorf("issue_count = %d, want 0 for empty project", meta.IssueCount)
	}
}

// TestExportPages_OnlyClosedIssues verifies export when all issues are closed
// and --pages-include-closed=false results in empty export.
func TestExportPages_OnlyClosedIssues(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create only closed issues
	issueData := `{"id": "closed-1", "title": "Completed Task 1", "status": "closed", "priority": 1, "issue_type": "task"}
{"id": "closed-2", "title": "Completed Task 2", "status": "closed", "priority": 2, "issue_type": "task"}
{"id": "closed-3", "title": "Completed Bug Fix", "status": "closed", "priority": 1, "issue_type": "bug"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed=false")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()

	// Should either succeed with 0 issues or fail gracefully
	if err != nil {
		t.Logf("Export with only closed issues (excluded) failed (acceptable): %v\n%s", err, out)
		return
	}

	// If succeeded, verify SQLite has 0 issues
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	issues := queryAllIssues(t, dbPath)
	if len(issues) != 0 {
		t.Errorf("SQLite has %d issues, want 0 (all closed and excluded)", len(issues))
	}
}

// TestExportPages_UnicodeContent verifies export handles Unicode correctly.
func TestExportPages_UnicodeContent(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create issues with Unicode content
	issueData := `{"id": "unicode-1", "title": "日本語タイトル", "description": "説明文はこちら", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "unicode-2", "title": "Émoji test 🚀🎉✨", "description": "Contains emojis: 👍 🔥 💯", "status": "open", "priority": 2, "issue_type": "feature"}
{"id": "unicode-3", "title": "Ü̶n̶i̶c̶o̶d̶e̶ special chars", "description": "Test: é à ü ñ ø æ ß", "status": "open", "priority": 1, "issue_type": "bug"}
{"id": "unicode-4", "title": "中文标题测试", "description": "中文描述内容", "status": "open", "priority": 2, "issue_type": "task"}`
	if err := os.WriteFile(filepath.Join(beadsPath, "issues.jsonl"), []byte(issueData), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify all issues are in SQLite with correct titles
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	issues := queryAllIssues(t, dbPath)

	if len(issues) != 4 {
		t.Fatalf("SQLite has %d issues, want 4", len(issues))
	}

	// Verify Unicode titles are preserved
	expectedTitles := map[string]string{
		"unicode-1": "日本語タイトル",
		"unicode-2": "Émoji test 🚀🎉✨",
		"unicode-3": "Ü̶n̶i̶c̶o̶d̶e̶ special chars",
		"unicode-4": "中文标题测试",
	}

	for _, issue := range issues {
		expected, ok := expectedTitles[issue.ID]
		if !ok {
			t.Errorf("Unexpected issue ID: %s", issue.ID)
			continue
		}
		if issue.Title != expected {
			t.Errorf("Issue %s title mismatch:\n  got:  %q\n  want: %q", issue.ID, issue.Title, expected)
		}
	}

	// Test FTS search with Unicode
	// Note: The porter tokenizer may not handle CJK characters well,
	// so we test with Latin characters that have diacritics instead
	emojiResults := searchFTS(t, dbPath, "Émoji")
	if len(emojiResults) != 1 {
		// Diacritics might be normalized, try without
		emojiResults = searchFTS(t, dbPath, "emoji")
		if len(emojiResults) != 1 {
			t.Logf("FTS search for 'emoji' returned %d results (tokenizer may not handle accented chars)", len(emojiResults))
		}
	}

	// CJK search may not work with porter tokenizer - just log, don't fail
	japaneseResults := searchFTS(t, dbPath, "日本語")
	if len(japaneseResults) == 0 {
		t.Log("Note: FTS5 porter tokenizer doesn't support CJK search (expected)")
	}
}

// ============================================================================
// Helper functions for bv-qnlb tests
// ============================================================================

// sqliteIssue represents an issue row from the SQLite database.
type sqliteIssue struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    int
	IssueType   string
}

// queryAllIssues queries all issues from the SQLite database.
func queryAllIssues(t *testing.T, dbPath string) []sqliteIssue {
	t.Helper()

	db, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open database %s: %v", dbPath, err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, title, COALESCE(description, ''), status, priority, issue_type FROM issues")
	if err != nil {
		t.Fatalf("query issues: %v", err)
	}
	defer rows.Close()

	var issues []sqliteIssue
	for rows.Next() {
		var issue sqliteIssue
		if err := rows.Scan(&issue.ID, &issue.Title, &issue.Description, &issue.Status, &issue.Priority, &issue.IssueType); err != nil {
			t.Fatalf("scan issue: %v", err)
		}
		issues = append(issues, issue)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}

	return issues
}

// searchFTS performs an FTS5 search and returns matching issue IDs.
func searchFTS(t *testing.T, dbPath, query string) []string {
	t.Helper()

	db, err := openSQLiteDB(dbPath)
	if err != nil {
		t.Fatalf("open database %s: %v", dbPath, err)
	}
	defer db.Close()

	// FTS5 search query
	rows, err := db.Query("SELECT id FROM issues_fts WHERE issues_fts MATCH ?", query)
	if err != nil {
		// FTS5 might not be available, log and return empty
		t.Logf("FTS5 query failed (might not be available): %v", err)
		return nil
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan FTS result: %v", err)
		}
		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}

	return ids
}

// openSQLiteDB opens a SQLite database for testing.
// Uses the same driver as the export code (modernc.org/sqlite).
func openSQLiteDB(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", dbPath)
}

// =============================================================================
// DETAIL PANE AND GRAPH LAYOUT TESTS (bv-mhfz)
// =============================================================================

// TestExportPages_DetailPaneMarkup verifies detail pane markup exists in index.html
func TestExportPages_DetailPaneMarkup(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 5)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read index.html
	htmlBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(htmlBytes)

	// Verify detail pane markup exists
	detailPaneMarkers := []string{
		"graphDetailNode",            // Alpine state variable
		"x-show=\"graphDetailNode\"", // Conditional display
		"graphDetailNode?.title",     // Title binding
		"graphDetailNode?.status",    // Status binding
		"graphDetailNode?.id",        // ID binding
	}

	for _, marker := range detailPaneMarkers {
		if !strings.Contains(html, marker) {
			t.Errorf("index.html missing detail pane marker: %s", marker)
		}
	}

	// Verify detail pane close button exists
	if !strings.Contains(html, "graphDetailNode = null") {
		t.Error("index.html missing detail pane close button handler")
	}
}

// TestExportPages_GraphLayoutStructure verifies graph_layout.json has correct structure
func TestExportPages_GraphLayoutStructure(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createRepoWithDeps(t) // Use repo with dependencies for interesting layout
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read graph_layout.json
	layoutPath := filepath.Join(exportDir, "data", "graph_layout.json")
	layoutBytes, err := os.ReadFile(layoutPath)
	if err != nil {
		t.Fatalf("read graph_layout.json: %v", err)
	}

	var layout struct {
		Positions   map[string][2]float64 `json:"positions"`
		Metrics     map[string][5]float64 `json:"metrics"`
		Links       [][2]string           `json:"links"`
		Cycles      [][]string            `json:"cycles"`
		Version     string                `json:"version"`
		GeneratedAt string                `json:"generated_at"`
		NodeCount   int                   `json:"node_count"`
		EdgeCount   int                   `json:"edge_count"`
	}

	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("json decode graph_layout.json: %v", err)
	}

	// Verify structure fields
	if layout.Version == "" {
		t.Error("graph_layout.json missing version")
	}
	if layout.GeneratedAt == "" {
		t.Error("graph_layout.json missing generated_at")
	}
	if layout.NodeCount == 0 {
		t.Error("graph_layout.json has zero node_count")
	}

	// Verify positions exist for all nodes
	if len(layout.Positions) == 0 {
		t.Fatal("graph_layout.json has no positions")
	}
	if len(layout.Positions) != layout.NodeCount {
		t.Errorf("positions count (%d) doesn't match node_count (%d)",
			len(layout.Positions), layout.NodeCount)
	}

	// Verify each position has valid x,y coordinates
	for id, pos := range layout.Positions {
		// Positions should be finite numbers
		if pos[0] != pos[0] || pos[1] != pos[1] { // NaN check
			t.Errorf("position for %s contains NaN: %v", id, pos)
		}
	}

	// Verify metrics exist for all nodes
	if len(layout.Metrics) != layout.NodeCount {
		t.Errorf("metrics count (%d) doesn't match node_count (%d)",
			len(layout.Metrics), layout.NodeCount)
	}

	// Verify metrics have expected 5 elements (pagerank, betweenness, inDegree, outDegree, inCycle)
	for id, m := range layout.Metrics {
		// All metrics should be non-negative
		for i, val := range m {
			if val < 0 {
				t.Errorf("metric[%d] for %s is negative: %v", i, id, val)
			}
		}
	}

	// Verify links array exists
	// Links may be empty for repos without dependencies or may contain [source, target] pairs
	t.Logf("graph_layout.json has %d nodes, %d edges",
		layout.NodeCount, layout.EdgeCount)
}

// TestExportPages_GraphJSSelectNodeHandler verifies selectNode handler exists in graph.js
func TestExportPages_GraphJSSelectNodeHandler(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read graph.js
	graphJSBytes, err := os.ReadFile(filepath.Join(exportDir, "graph.js"))
	if err != nil {
		t.Fatalf("read graph.js: %v", err)
	}
	graphJS := string(graphJSBytes)

	// Verify selectNode function exists
	handlers := []string{
		"function selectNode",        // Function definition
		"export function selectNode", // Or exported
		"bv-graph:nodeClick",         // Custom event dispatch
	}

	foundSelectNode := false
	for _, handler := range handlers[:2] {
		if strings.Contains(graphJS, handler) {
			foundSelectNode = true
			break
		}
	}
	if !foundSelectNode {
		t.Error("graph.js missing selectNode function")
	}

	// Verify nodeClick event dispatch (uses template: `bv-graph:${name}`)
	if !strings.Contains(graphJS, "nodeClick") {
		t.Error("graph.js missing nodeClick event handler/dispatch")
	}
	// The event dispatching uses a helper function with template literal
	if !strings.Contains(graphJS, "dispatchEvent") && !strings.Contains(graphJS, "CustomEvent") {
		t.Error("graph.js missing event dispatch mechanism")
	}

	// Verify refresh/redraw capability
	refreshHandlers := []string{
		"refreshGraph",            // Custom helper
		".graphData(",             // ForceGraph redraw pattern
		"graphInstance.graphData", // Alternative pattern
	}

	foundRefresh := false
	for _, h := range refreshHandlers {
		if strings.Contains(graphJS, h) {
			foundRefresh = true
			break
		}
	}
	if !foundRefresh {
		t.Error("graph.js missing graph refresh/redraw capability")
	}
}

// TestExportPages_GraphLayoutNodesCovered verifies all nodes get positions
func TestExportPages_GraphLayoutNodesCovered(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 5)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export failed: %v\n%s", err, out)
	}

	// Read layout
	layoutBytes, err := os.ReadFile(filepath.Join(exportDir, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("read layout: %v", err)
	}

	var layout struct {
		Positions map[string][2]float64 `json:"positions"`
		Metrics   map[string][5]float64 `json:"metrics"`
		NodeCount int                   `json:"node_count"`
	}
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("decode layout: %v", err)
	}

	// Verify all nodes have positions and metrics
	if len(layout.Positions) != layout.NodeCount {
		t.Errorf("not all nodes have positions: %d positions for %d nodes",
			len(layout.Positions), layout.NodeCount)
	}
	if len(layout.Metrics) != layout.NodeCount {
		t.Errorf("not all nodes have metrics: %d metrics for %d nodes",
			len(layout.Metrics), layout.NodeCount)
	}

	// Verify each node has both position and metrics
	for id := range layout.Positions {
		if _, ok := layout.Metrics[id]; !ok {
			t.Errorf("node %s has position but no metrics", id)
		}
	}
	for id := range layout.Metrics {
		if _, ok := layout.Positions[id]; !ok {
			t.Errorf("node %s has metrics but no position", id)
		}
	}
}

// TestExportPages_PrecomputedLayoutUsedByViewer verifies viewer.js loads precomputed layout
func TestExportPages_PrecomputedLayoutUsedByViewer(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read viewer.js
	viewerJSBytes, err := os.ReadFile(filepath.Join(exportDir, "viewer.js"))
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	viewerJS := string(viewerJSBytes)

	// Verify precomputed layout loading (viewer uses precomputedLayout variable)
	if !strings.Contains(viewerJS, "precomputedLayout") &&
		!strings.Contains(viewerJS, "graph_layout") {
		t.Error("viewer.js doesn't reference precomputed layout")
	}

	// Verify ForceGraph integration markers
	forceGraphMarkers := []string{
		"ForceGraph",         // Library reference
		"forceGraphModule",   // Module instance
		"initForceGraphView", // Init function
	}

	for _, marker := range forceGraphMarkers {
		if !strings.Contains(viewerJS, marker) {
			t.Errorf("viewer.js missing ForceGraph marker: %s", marker)
		}
	}
}

// TestExportPages_DetailPaneAllProperties verifies all detail pane property bindings exist
func TestExportPages_DetailPaneAllProperties(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createRepoWithDeps(t) // Use deps repo for rich data
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read index.html
	htmlBytes, err := os.ReadFile(filepath.Join(exportDir, "index.html"))
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(htmlBytes)

	// Verify ALL detail pane property bindings for comprehensive UI
	propertyBindings := []struct {
		property    string
		description string
	}{
		{"graphDetailNode?.id", "Issue ID binding"},
		{"graphDetailNode?.title", "Issue title binding"},
		{"graphDetailNode?.status", "Status binding"},
		{"graphDetailNode?.priority", "Priority binding"},
		{"graphDetailNode?.type", "Issue type binding"},
		{"graphDetailNode?.assignee", "Assignee binding"},
		{"graphDetailNode?.blockerCount", "Blocker count binding"},
		{"graphDetailNode?.dependentCount", "Dependent count binding"},
		{"graphDetailNode?.labels", "Labels binding"},
		{"graphDetailNode?.pagerank", "PageRank metric binding"},
		{"graphDetailNode?.betweenness", "Betweenness metric binding"},
		{"graphDetailNode?.description", "Description binding"},
		{"graphDetailNode?.createdAt", "Created date binding"},
		{"graphDetailNode?.updatedAt", "Updated date binding"},
	}

	for _, pb := range propertyBindings {
		if !strings.Contains(html, pb.property) {
			t.Errorf("index.html missing %s: %s", pb.description, pb.property)
		}
	}

	// Verify inCycle indicator exists for cycle detection UI
	if !strings.Contains(html, "graphDetailNode?.inCycle") {
		t.Error("index.html missing inCycle indicator binding")
	}
}

// TestExportPages_GraphLayoutCycleDetection verifies cycles are detected in graph_layout.json
func TestExportPages_GraphLayoutCycleDetection(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	// Create repo with a dependency cycle: A -> B -> C -> A
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create circular dependency: A depends on C, B depends on A, C depends on B -> forms cycle
	jsonl := `{"id": "cycle-a", "title": "Cycle Node A", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"issue_id": "cycle-a", "depends_on_id": "cycle-c", "type": "blocks"}]}
{"id": "cycle-b", "title": "Cycle Node B", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"issue_id": "cycle-b", "depends_on_id": "cycle-a", "type": "blocks"}]}
{"id": "cycle-c", "title": "Cycle Node C", "status": "open", "priority": 1, "issue_type": "task", "dependencies": [{"issue_id": "cycle-c", "depends_on_id": "cycle-b", "type": "blocks"}]}
{"id": "non-cycle", "title": "Non-Cycle Node", "status": "open", "priority": 2, "issue_type": "task"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read graph_layout.json
	layoutBytes, err := os.ReadFile(filepath.Join(exportDir, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("read graph_layout.json: %v", err)
	}

	var layout struct {
		Cycles    [][]string            `json:"cycles"`
		Metrics   map[string][5]float64 `json:"metrics"`
		NodeCount int                   `json:"node_count"`
	}
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("decode graph_layout.json: %v", err)
	}

	// Verify cycles were detected
	if len(layout.Cycles) == 0 {
		t.Error("graph_layout.json should detect the circular dependency cycle")
	}

	// Verify cycle contains expected nodes
	foundCycle := false
	for _, cycle := range layout.Cycles {
		// Check if this cycle contains our cycle nodes
		hasCycleA := false
		hasCycleB := false
		hasCycleC := false
		for _, id := range cycle {
			switch id {
			case "cycle-a":
				hasCycleA = true
			case "cycle-b":
				hasCycleB = true
			case "cycle-c":
				hasCycleC = true
			}
		}
		if hasCycleA && hasCycleB && hasCycleC {
			foundCycle = true
			break
		}
	}
	if !foundCycle {
		t.Errorf("cycle not found containing all expected nodes: %v", layout.Cycles)
	}

	// Verify metrics show inCycle flag (index 4) for cycle nodes
	cycleNodeIDs := []string{"cycle-a", "cycle-b", "cycle-c"}
	for _, id := range cycleNodeIDs {
		metrics, ok := layout.Metrics[id]
		if !ok {
			t.Errorf("missing metrics for cycle node %s", id)
			continue
		}
		// Index 4 is the inCycle flag (0 or 1)
		if metrics[4] != 1 {
			t.Errorf("node %s should have inCycle=1, got %v", id, metrics[4])
		}
	}

	// Non-cycle node should have inCycle=0
	if metrics, ok := layout.Metrics["non-cycle"]; ok {
		if metrics[4] != 0 {
			t.Errorf("non-cycle node should have inCycle=0, got %v", metrics[4])
		}
	}
}

// TestExportPages_GraphLayoutLargeScale tests graph layout with 50 nodes
func TestExportPages_GraphLayoutLargeScale(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large scale test in short mode")
	}

	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	// Create repo with 50 nodes and chain dependencies
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	var issues strings.Builder
	nodeCount := 50
	for i := 1; i <= nodeCount; i++ {
		status := "open"
		if i%5 == 0 {
			status = "closed"
		}
		priority := i % 4
		issueType := "task"
		if i%7 == 0 {
			issueType = "bug"
		}

		// Create chain: node-1 <- node-2 <- node-3 ... (every 3rd node depends on previous)
		deps := ""
		if i > 1 && i%3 == 0 {
			deps = fmt.Sprintf(`, "dependencies": [{"target_id": "scale-%d", "type": "blocks"}]`, i-1)
		}

		line := fmt.Sprintf(`{"id": "scale-%d", "title": "Scale Test Issue %d", "status": "%s", "priority": %d, "issue_type": "%s"%s}`,
			i, i, status, priority, issueType, deps)
		issues.WriteString(line + "\n")
	}

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(issues.String()), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir, "--pages-include-closed")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read graph_layout.json
	layoutBytes, err := os.ReadFile(filepath.Join(exportDir, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("read graph_layout.json: %v", err)
	}

	var layout struct {
		Positions map[string][2]float64 `json:"positions"`
		Metrics   map[string][5]float64 `json:"metrics"`
		Links     [][2]string           `json:"links"`
		NodeCount int                   `json:"node_count"`
		EdgeCount int                   `json:"edge_count"`
	}
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("decode graph_layout.json: %v", err)
	}

	// Verify all nodes have positions
	if layout.NodeCount != nodeCount {
		t.Errorf("node_count = %d, want %d", layout.NodeCount, nodeCount)
	}
	if len(layout.Positions) != nodeCount {
		t.Errorf("positions count = %d, want %d", len(layout.Positions), nodeCount)
	}
	if len(layout.Metrics) != nodeCount {
		t.Errorf("metrics count = %d, want %d", len(layout.Metrics), nodeCount)
	}

	// Verify positions are spread out (not all at same point)
	var sumX, sumY float64
	for _, pos := range layout.Positions {
		sumX += pos[0]
		sumY += pos[1]
	}
	avgX := sumX / float64(nodeCount)
	avgY := sumY / float64(nodeCount)

	// At least some nodes should be away from average (spread check)
	spreadCount := 0
	threshold := 50.0 // pixels from center
	for _, pos := range layout.Positions {
		dx := pos[0] - avgX
		dy := pos[1] - avgY
		dist := dx*dx + dy*dy
		if dist > threshold*threshold {
			spreadCount++
		}
	}
	if spreadCount < nodeCount/4 {
		t.Errorf("positions not well spread: only %d/%d nodes are away from center", spreadCount, nodeCount)
	}

	t.Logf("Large scale test: %d nodes, %d edges, %d spread from center",
		layout.NodeCount, layout.EdgeCount, spreadCount)
}

// TestExportPages_DetailPaneIntegration verifies detail pane works with actual data
func TestExportPages_DetailPaneIntegration(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	// Create a repo with rich data for detail pane testing
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create issues with all properties that detail pane displays
	jsonl := `{"id": "detail-1", "title": "Feature: User Authentication", "description": "Implement OAuth2 login flow with Google and GitHub providers.\n\n## Tasks\n- Setup OAuth app\n- Implement callback handler\n- Add session management", "status": "in_progress", "priority": 0, "issue_type": "feature", "labels": ["auth", "security", "p0"], "assignee": "alice@example.com", "created_at": "2025-01-15T10:00:00Z", "updated_at": "2025-01-18T15:30:00Z"}
{"id": "detail-2", "title": "Bug: Login redirect fails", "description": "Users are redirected to wrong page after OAuth callback.", "status": "blocked", "priority": 1, "issue_type": "bug", "labels": ["auth", "bug"], "dependencies": [{"issue_id": "detail-2", "depends_on_id": "detail-1", "type": "blocks"}], "created_at": "2025-01-16T09:00:00Z", "updated_at": "2025-01-17T11:00:00Z"}
{"id": "detail-3", "title": "Task: Write auth tests", "description": "Add unit and integration tests for auth module.", "status": "open", "priority": 2, "issue_type": "task", "labels": ["testing"], "dependencies": [{"issue_id": "detail-3", "depends_on_id": "detail-2", "type": "blocks"}], "created_at": "2025-01-17T14:00:00Z"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}

	exportDir := filepath.Join(repoDir, "bv-pages")
	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Verify SQLite has issues with correct data
	dbPath := filepath.Join(exportDir, "beads.sqlite3")
	issues := queryAllIssues(t, dbPath)

	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(issues))
	}

	// Verify graph_layout.json has correct metrics
	layoutBytes, err := os.ReadFile(filepath.Join(exportDir, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("read graph_layout.json: %v", err)
	}

	var layout struct {
		Metrics   map[string][5]float64 `json:"metrics"`
		Links     [][2]string           `json:"links"`
		NodeCount int                   `json:"node_count"`
		EdgeCount int                   `json:"edge_count"`
	}
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("decode graph_layout.json: %v", err)
	}

	// Verify dependency chain is captured in links
	if layout.EdgeCount != 2 {
		t.Errorf("expected 2 edges (dependency chain), got %d", layout.EdgeCount)
	}

	// Verify detail-1 has high PageRank (blocks others, no blockers)
	if metrics, ok := layout.Metrics["detail-1"]; ok {
		pagerank := metrics[0]
		if pagerank == 0 {
			t.Error("detail-1 should have non-zero PageRank as it blocks other issues")
		}
	} else {
		t.Error("missing metrics for detail-1")
	}

	// Verify viewer.js references graph_layout fetch
	viewerJS, err := os.ReadFile(filepath.Join(exportDir, "viewer.js"))
	if err != nil {
		t.Fatalf("read viewer.js: %v", err)
	}
	if !strings.Contains(string(viewerJS), "graphDetailNode") {
		t.Error("viewer.js missing graphDetailNode Alpine state")
	}

	// Verify graph.js has event handlers for node selection
	graphJS, err := os.ReadFile(filepath.Join(exportDir, "graph.js"))
	if err != nil {
		t.Fatalf("read graph.js: %v", err)
	}
	if !strings.Contains(string(graphJS), "graph_layout.json") {
		t.Error("graph.js missing graph_layout.json fetch")
	}
}

// TestExportPages_GraphLayoutFetch verifies graph.js fetches layout correctly
func TestExportPages_GraphLayoutFetch(t *testing.T) {
	bv := buildBvBinary(t)
	stageViewerAssets(t, bv)

	repoDir := createSimpleRepo(t, 3)
	exportDir := filepath.Join(repoDir, "bv-pages")

	cmd := exec.Command(bv, "--export-pages", exportDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-pages failed: %v\n%s", err, out)
	}

	// Read graph.js
	graphJSBytes, err := os.ReadFile(filepath.Join(exportDir, "graph.js"))
	if err != nil {
		t.Fatalf("read graph.js: %v", err)
	}
	graphJS := string(graphJSBytes)

	// Verify fetch call for graph_layout.json
	if !strings.Contains(graphJS, "data/graph_layout.json") {
		t.Error("graph.js missing fetch for data/graph_layout.json")
	}

	// Verify position application from layout
	positionMarkers := []string{
		"positions", // Layout positions object
		"fx",        // Fixed x position (ForceGraph)
		"fy",        // Fixed y position (ForceGraph)
	}

	foundPositionHandling := 0
	for _, marker := range positionMarkers {
		if strings.Contains(graphJS, marker) {
			foundPositionHandling++
		}
	}
	if foundPositionHandling < 2 {
		t.Errorf("graph.js missing position handling markers (found %d/3)", foundPositionHandling)
	}
}

// TestExportPages_HybridWasmHookRunsInBuiltBinary (I4): BV_BUILD_HYBRID_WASM
// must be honoured by the released binary, which ships embedded viewer
// assets. Without wasm-pack on PATH the export has to fail loudly instead of
// silently skipping the build; with wasm-pack present the hook would build,
// so the test skips rather than spend minutes compiling Rust.
func TestExportPages_HybridWasmHookRunsInBuiltBinary(t *testing.T) {
	if _, err := exec.LookPath("wasm-pack"); err == nil {
		t.Skip("wasm-pack is installed; the hook would run a real build")
	}
	bv := buildBvBinary(t)
	env := t.TempDir()
	writeBeads(t, env, `{"id":"W-1","title":"one","status":"open","priority":1,"issue_type":"task"}`)
	out := filepath.Join(env, "bundle")

	cmd := exec.Command(bv, "--export-pages", out)
	cmd.Dir = env
	cmd.Env = append(os.Environ(), "BV_BUILD_HYBRID_WASM=1")
	combined, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("export should fail when the hybrid build was requested but wasm-pack is missing:\n%s", combined)
	}
	if !strings.Contains(string(combined), "wasm-pack") {
		t.Fatalf("failure should name wasm-pack:\n%s", combined)
	}

	// Without the flag the same export succeeds and ships no wasm/ directory
	// unless the assets carried one.
	cmd = exec.Command(bv, "--export-pages", out)
	cmd.Dir = env
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plain export failed: %v\n%s", err, combined)
	}
	if _, err := os.Stat(filepath.Join(out, "index.html")); err != nil {
		t.Fatalf("bundle missing index.html: %v", err)
	}
}

// TestExportPages_RecordsLoadSizes (I4) exports this repository's dashboard
// and measures what a viewer has to download before first render. With
// BV_RECORD_PERF=1 it writes tests/artifacts/perf/pages_load.json (the source
// of the README's bundle-size claims); otherwise it guards that record
// against a bundle that grew by more than a quarter. Time-to-first-render
// needs a browser and is only measured when BV_HEADLESS_BROWSER names one;
// without it the JSON says so instead of inventing a number.
func TestExportPages_RecordsLoadSizes(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".beads", "issues.jsonl")); err != nil {
		t.Skip("repository tracker not present")
	}
	bv := buildBvBinary(t)
	out := filepath.Join(t.TempDir(), "bundle")
	cmd := exec.Command(bv, "--export-pages", out)
	cmd.Dir = repo
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, combined)
	}

	size := func(rel string) int64 {
		info, err := os.Stat(filepath.Join(out, rel))
		if err != nil {
			t.Fatalf("bundle file %s: %v", rel, err)
		}
		return info.Size()
	}
	// A source archive has no local Git history. Production still exports the
	// repository's complete current graph and warns that time-travel is absent.
	// With Git present, a missing history file remains a regression.
	var historyBytes *int64
	historyStatus := "available"
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		n := size(filepath.Join("data", "history.json"))
		historyBytes = &n
	} else if os.IsNotExist(err) {
		historyStatus = "unavailable_no_git"
		warning := "Warning: failed to generate history: not a git repository: " + repo
		if !strings.Contains(string(combined), warning) {
			t.Fatalf("missing optional-history warning %q:\n%s", warning, combined)
		}
		if _, err := os.Stat(filepath.Join(out, "data", "history.json")); !os.IsNotExist(err) {
			t.Fatalf("history.json should be absent without Git history, got %v", err)
		}
	} else {
		t.Fatalf("stat repository Git metadata: %v", err)
	}
	var total, vendor int64
	if err := filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		total += info.Size()
		if strings.Contains(path, string(filepath.Separator)+"vendor"+string(filepath.Separator)) {
			vendor += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	layoutBytes, err := os.ReadFile(filepath.Join(out, "data", "graph_layout.json"))
	if err != nil {
		t.Fatalf("graph_layout.json: %v", err)
	}
	var layout struct {
		NodeCount int `json:"node_count"`
		EdgeCount int `json:"edge_count"`
	}
	if err := json.Unmarshal(layoutBytes, &layout); err != nil {
		t.Fatalf("graph_layout.json decode: %v", err)
	}

	record := map[string]any{
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"issues":         layout.NodeCount,
		"edges":          layout.EdgeCount,
		"history_status": historyStatus,
		"bytes": map[string]any{
			"index_html":        size("index.html"),
			"beads_sqlite3":     size("beads.sqlite3"),
			"graph_layout_json": int64(len(layoutBytes)),
			"triage_json":       size(filepath.Join("data", "triage.json")),
			"history_json":      historyBytes,
			"vendor_total":      vendor,
			"bundle_total":      total,
		},
		"first_render_ms": nil,
		"note":            "first_render_ms is measured only when BV_HEADLESS_BROWSER names a browser; absent here",
	}
	if browser := os.Getenv("BV_HEADLESS_BROWSER"); browser != "" {
		record["note"] = "BV_HEADLESS_BROWSER is set but timing is not implemented yet; sizes only"
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pages bundle for %d issues / %d edges: %s", layout.NodeCount, layout.EdgeCount, data)
	recordPath := filepath.Join(repo, "tests", "artifacts", "perf", "pages_load.json")
	if os.Getenv("BV_RECORD_PERF") == "1" {
		// Deliberate re-measurement: rewrite the committed record.
		if err := os.MkdirAll(filepath.Dir(recordPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recordPath, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		// Ordinary runs guard the committed record instead of rewriting it
		// (a rewrite on every e2e run dirtied the tree after each gate).
		// A bundle that grew by more than a quarter is a regression to record
		// on purpose, not silently.
		committed, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatalf("%s is missing; run this test with BV_RECORD_PERF=1 to record it: %v", recordPath, err)
		}
		var prior struct {
			Bytes struct {
				BundleTotal int64 `json:"bundle_total"`
			} `json:"bytes"`
		}
		if err := json.Unmarshal(committed, &prior); err != nil {
			t.Fatalf("decode %s: %v", recordPath, err)
		}
		if prior.Bytes.BundleTotal > 0 && total > prior.Bytes.BundleTotal*5/4 {
			t.Fatalf("bundle_total grew from %d to %d bytes (more than 25%%); re-record with BV_RECORD_PERF=1 if intended", prior.Bytes.BundleTotal, total)
		}
	}
	if layout.NodeCount == 0 || size("beads.sqlite3") == 0 {
		t.Fatalf("bundle should carry the repository's issues")
	}
}
