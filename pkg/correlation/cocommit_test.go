package correlation

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoCommitExtractorConcurrentPublicCallsShareMemoSafely(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	repo := initTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\nconcurrent\n"), 0o644); err != nil {
		t.Fatalf("write concurrent co-commit fixture: %v", err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "update concurrent co-commit fixture")
	sha, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve concurrent fixture HEAD: %v", err)
	}
	event := BeadEvent{
		BeadID:    "concurrent-bead",
		CommitSHA: sha,
		EventType: EventClaimed,
		CommitMsg: "update concurrent co-commit fixture",
	}
	extractor := NewCoCommitExtractor(repo)

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(useBatch bool) {
			defer wg.Done()
			<-start
			if useBatch {
				commits, callErr := extractor.ExtractAllCoCommits([]BeadEvent{event})
				if callErr != nil {
					errs <- callErr
					return
				}
				if len(commits) != 1 || len(commits[0].Files) != 1 || commits[0].Files[0].Path != "README.md" {
					errs <- fmt.Errorf("unexpected batched result: %#v", commits)
				}
				return
			}
			files, callErr := extractor.ExtractCoCommittedFiles(event)
			if callErr != nil {
				errs <- callErr
				return
			}
			if len(files) != 1 || files[0].Path != "README.md" {
				errs <- fmt.Errorf("unexpected direct result: %#v", files)
			}
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()
	close(errs)
	for callErr := range errs {
		t.Error(callErr)
	}
}

func TestIsCodeFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Code files
		{"pkg/auth/login.go", true},
		{"src/app.py", true},
		{"index.js", true},
		{"app.tsx", true},
		{"main.rs", true},
		{"App.java", true},
		{"config.yaml", true},
		{"data.json", true},
		{"README.md", true},
		{"schema.sql", true},
		{"script.sh", true},

		// Non-code files
		{"image.png", false},
		{"photo.jpg", false},
		{"document.pdf", false},
		{"archive.zip", false},
		{"binary.exe", false},
		{"data.csv", false},

		// Edge cases
		{"Makefile", false}, // No extension
		{".gitignore", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isCodeFile(tt.path)
			if got != tt.want {
				t.Errorf("isCodeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsExcludedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Excluded paths
		{".beads/beads.jsonl", true},
		{".beads/issues.jsonl", true},
		{".bv/hooks.yaml", true},
		{".git/objects/abc", true},
		{"node_modules/lodash/index.js", true},
		{"vendor/github.com/pkg/errors/errors.go", true},
		{"__pycache__/module.pyc", true},
		{".venv/lib/python3.9/site.py", true},

		// Not excluded
		{"pkg/auth/login.go", false},
		{"src/components/Button.tsx", false},
		{"cmd/main.go", false},
		{"internal/service/user.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isExcludedPath(tt.path)
			if got != tt.want {
				t.Errorf("isExcludedPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestExcludePathspecArgs(t *testing.T) {
	args := excludePathspecArgs()

	// Must begin with the pathspec separator and the "." include so the
	// excludes are interpreted as pathspecs, not revisions.
	if len(args) < 2 || args[0] != "--" || args[1] != "." {
		t.Fatalf("expected args to start with [\"--\", \".\"], got %v", args[:min(len(args), 2)])
	}

	// Every excluded directory must produce a matching exclude pathspec so the
	// git show diff skips that content (the cost #160 was paying then discarding).
	joined := strings.Join(args, " ")
	for _, prefix := range excludedPaths {
		dir := strings.TrimSuffix(prefix, "/")
		want := ":(exclude,glob)" + dir + "/**"
		if !strings.Contains(joined, want) {
			t.Errorf("missing exclude pathspec for %q (want %q) in %v", prefix, want, args)
		}
	}

	// .beads/ is the dominant blob; assert it explicitly.
	if !strings.Contains(joined, ":(exclude,glob).beads/**") {
		t.Errorf("expected .beads exclusion in pathspec args, got %v", args)
	}
}

func TestCoCommitSHABatchesBoundEveryGitInvocation(t *testing.T) {
	shas := make([]string, maxCoCommitSHAsPerGitCommand*2+1)
	for i := range shas {
		shas[i] = strings.Repeat("a", 40)
	}
	batches := coCommitSHABatches(shas)
	if len(batches) != 3 {
		t.Fatalf("batch count=%d, want 3", len(batches))
	}
	wantSizes := []int{maxCoCommitSHAsPerGitCommand, maxCoCommitSHAsPerGitCommand, 1}
	consumed := 0
	for i, batch := range batches {
		if len(batch) != wantSizes[i] {
			t.Fatalf("batch %d size=%d, want %d", i, len(batch), wantSizes[i])
		}
		consumed += len(batch)
	}
	if consumed != len(shas) {
		t.Fatalf("batched %d SHAs, want %d", consumed, len(shas))
	}
}

func TestPrimeBatchRejectsNoncanonicalRevisionBeforeGit(t *testing.T) {
	c := NewCoCommitExtractor(t.TempDir())
	for _, sha := range []string{
		"--all",
		"ABCDEF0123456789ABCDEF0123456789ABCDEF01",
		strings.Repeat("A", 64),
		strings.Repeat("a", 39),
		strings.Repeat("a", 63),
	} {
		if err := c.primeBatch([]string{sha}); err == nil || !strings.Contains(err.Error(), "invalid co-commit SHA") {
			t.Fatalf("primeBatch(%q) error=%v, want invalid-SHA error", sha, err)
		}
	}
	if c.fileCache != nil && len(c.fileCache) != 0 {
		t.Fatalf("invalid revisions populated file cache: %#v", c.fileCache)
	}
}

func TestCanonicalCommitSHAAndBatchHeaderSupportSHA1AndSHA256(t *testing.T) {
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		if !isCanonicalCommitSHA(sha) {
			t.Fatalf("canonical %d-character object ID was rejected", len(sha))
		}
		payload := append([]byte(sha+"\x00\x00\n"), []byte("M\x00file.go\x00")...)
		got, err := parseBatchNameStatus(payload, []string{sha})
		if err != nil {
			t.Fatalf("parse %d-character batch header: %v", len(sha), err)
		}
		if files := got[sha]; len(files) != 1 || files[0] != (FileChange{Path: "file.go", Action: "M"}) {
			t.Fatalf("%d-character batch result=%#v", len(sha), got)
		}
	}
}

func TestCoCommitDiffPolicyPinsConfigSensitiveInputs(t *testing.T) {
	policy := append(repoGitPolicyArgs(), coCommitGitConfigArgs()...)
	joined := strings.Join(append(policy, coCommitDiffArgs("--name-status")...), "\x00")
	for _, want := range []string{
		"--no-replace-objects",
		"core.quotePath=true",
		"diff.renames=true",
		"diff.renameLimit=1000",
		"-z",
		"--find-renames=50%",
		"-l1000",
		"--no-rename-empty",
		"--diff-algorithm=default",
		"--no-indent-heuristic",
		"--no-ext-diff",
		"--no-textconv",
		"--text",
		"--ignore-submodules=none",
		"--submodule=short",
		"--diff-merges=first-parent",
		"--no-show-signature",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("co-commit Git policy omitted %q: %q", want, joined)
		}
	}
}

func TestRepoGitEnvironmentDropsAmbientRepositoryRouting(t *testing.T) {
	for _, name := range []string{
		"GIT_DIR",
		"GIT_WORK_TREE",
		"GIT_GRAFT_FILE",
		"GIT_QUARANTINE_PATH",
		"GIT_SHALLOW_FILE",
		"GIT_NO_REPLACE_OBJECTS",
		"GIT_INTERNAL_SUPER_PREFIX",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0",
		"GIT_CONFIG_VALUE_0",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_SYSTEM",
		"GIT_IMPLICIT_WORK_TREE",
		"GIT_LITERAL_PATHSPECS",
		"git_dir",
		"Git_Work_Tree",
		"gIt_CoNfIg_CoUnT",
		"git_config_key_1",
		"Git_Config_Value_1",
		"gIt_cOnFiG_NoSyStEm",
	} {
		t.Setenv(name, "hostile")
	}
	for _, entry := range repoGitEnvironment() {
		name, _, _ := strings.Cut(entry, "=")
		if gitEnvironmentOverridesRepository(name) {
			t.Fatalf("ambient Git variable %q survived sanitization", name)
		}
	}
}

func TestParseNameStatusUsesPostImageAndDeterministicOrder(t *testing.T) {
	payload := []byte("R095\x00z-old.go\x00a-renamed.go\x00C100\x00source.go\x00b-copy.go\x00M\x00c.go\x00")
	got, err := parseNameStatus(payload)
	if err != nil {
		t.Fatalf("parseNameStatus: %v", err)
	}
	want := []FileChange{
		{Path: "a-renamed.go", Action: "R"},
		{Path: "b-copy.go", Action: "C"},
		{Path: "c.go", Action: "M"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed files=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed file %d=%#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestNULParsersPreserveUnicodeTabsAndNewlines(t *testing.T) {
	unicodePath := "pkg/café.go"
	tabPath := "pkg/tab\tname.go"
	newlinePath := "pkg/line\nname.go"
	renameOld := "pkg/old\nname.go"
	renameNew := "pkg/new\tname.go"

	namePayload := make([]byte, 0)
	for _, field := range []string{
		"M", unicodePath,
		"A", tabPath,
		"D", newlinePath,
		"R100", renameOld, renameNew,
	} {
		namePayload = append(namePayload, field...)
		namePayload = append(namePayload, 0)
	}
	files, err := parseNameStatus(namePayload)
	if err != nil {
		t.Fatalf("parseNameStatus: %v", err)
	}
	byPath := make(map[string]string, len(files))
	for _, file := range files {
		byPath[file.Path] = file.Action
	}
	for path, action := range map[string]string{
		unicodePath: "M",
		tabPath:     "A",
		newlinePath: "D",
		renameNew:   "R",
	} {
		if got := byPath[path]; got != action {
			t.Errorf("name-status path %q action=%q, want %q; all=%#v", path, got, action, files)
		}
	}
	if _, retainedOld := byPath[renameOld]; retainedOld {
		t.Fatalf("rename retained pre-image path %q: %#v", renameOld, files)
	}

	numstatPayload := make([]byte, 0)
	numstatPayload = append(numstatPayload, "3\t2\t"+unicodePath...)
	numstatPayload = append(numstatPayload, 0)
	numstatPayload = append(numstatPayload, "4\t1\t"+tabPath...)
	numstatPayload = append(numstatPayload, 0)
	numstatPayload = append(numstatPayload, "5\t6\t"...)
	numstatPayload = append(numstatPayload, 0)
	numstatPayload = append(numstatPayload, renameOld...)
	numstatPayload = append(numstatPayload, 0)
	numstatPayload = append(numstatPayload, renameNew...)
	numstatPayload = append(numstatPayload, 0)

	stats, err := parseNumstat(numstatPayload)
	if err != nil {
		t.Fatalf("parseNumstat: %v", err)
	}
	for path, want := range map[string]lineStats{
		unicodePath: {insertions: 3, deletions: 2},
		tabPath:     {insertions: 4, deletions: 1},
		renameNew:   {insertions: 5, deletions: 6},
	} {
		if got := stats[path]; got != want {
			t.Errorf("numstat path %q=%+v, want %+v; all=%#v", path, got, want, stats)
		}
	}
	if _, retainedOld := stats[renameOld]; retainedOld {
		t.Fatalf("numstat rename retained pre-image path %q: %#v", renameOld, stats)
	}
}

func TestNULParsersRejectTruncationWithoutPartialResults(t *testing.T) {
	if files, err := parseNameStatus([]byte("M\x00good.go\x00R100\x00old.go\x00")); err == nil || files != nil {
		t.Fatalf("truncated name-status result=%#v error=%v, want nil/error", files, err)
	}
	if stats, err := parseNumstat([]byte("1\t0\tgood.go\x002\t1\tunterminated.go")); err == nil || stats != nil {
		t.Fatalf("truncated numstat result=%#v error=%v, want nil/error", stats, err)
	}

	sha := strings.Repeat("a", 40)
	batch := append([]byte(sha+"\x00\x00\n"), []byte("M\x00good.go\x00R100\x00old.go\x00")...)
	if files, err := parseBatchNameStatus(batch, []string{sha}); err == nil || files != nil {
		t.Fatalf("truncated batched name-status result=%#v error=%v, want nil/error", files, err)
	}

	missingSHA := strings.Repeat("b", 40)
	completePrefix := []byte(sha + "\x00\x00\nM\x00good.go\x00")
	if files, err := parseBatchNameStatus(completePrefix, []string{sha, missingSHA}); err == nil || files != nil {
		t.Fatalf("missing batched name-status header result=%#v error=%v, want nil/error", files, err)
	}
	completeNumstatPrefix := []byte(sha + "\x00\x00\n1\t0\tgood.go\x00")
	if stats, err := parseBatchNumstat(completeNumstatPrefix, []string{sha, missingSHA}); err == nil || stats != nil {
		t.Fatalf("missing batched numstat header result=%#v error=%v, want nil/error", stats, err)
	}
}

func TestCoCommitGitParsersPreserveRawPathBytes(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	repo := initTempGitRepo(t)
	paths := []string{"café.go", "tab\tname.go", "line\nname.go"}
	for _, path := range paths {
		if err := os.WriteFile(filepath.Join(repo, path), []byte("package fixture\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	gitAddArgs := append([]string{"add", "--"}, paths...)
	runGit(t, repo, gitAddArgs...)
	runGit(t, repo, "commit", "-m", "add unusual paths")
	shaBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve fixture HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	assertPaths := func(label string, files []FileChange, stats map[string]lineStats) {
		t.Helper()
		fileSet := make(map[string]bool, len(files))
		for _, file := range files {
			fileSet[file.Path] = true
		}
		for _, path := range paths {
			if !fileSet[path] {
				t.Errorf("%s name-status lost raw path %q: %#v", label, path, files)
			}
			if got, ok := stats[path]; !ok || got.insertions != 1 || got.deletions != 0 {
				t.Errorf("%s numstat path %q=%+v present=%t, want 1/0", label, path, got, ok)
			}
		}
	}

	fallback := NewCoCommitExtractor(repo)
	fallbackFiles, err := fallback.getFilesChanged(sha)
	if err != nil {
		t.Fatalf("fallback name-status: %v", err)
	}
	fallbackStats, err := fallback.getLineStats(sha)
	if err != nil {
		t.Fatalf("fallback numstat: %v", err)
	}
	assertPaths("fallback", fallbackFiles, fallbackStats)

	batched := NewCoCommitExtractor(repo)
	if err := batched.primeBatch([]string{sha}); err != nil {
		t.Fatalf("primeBatch: %v", err)
	}
	assertPaths("batch", batched.fileCache[sha], batched.statCache[sha])
}

func TestPrimeBatchTreatsExcludedOnlyCommitAsVerifiedEmpty(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	repo := initTempGitRepo(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create Beads fixture directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"bv-empty\",\"status\":\"closed\"}\n"), 0o644); err != nil {
		t.Fatalf("write Beads-only fixture: %v", err)
	}
	runGit(t, repo, "add", "--", ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "close bead without code changes")
	shaBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve Beads-only commit: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	extractor := NewCoCommitExtractor(repo)
	if err := extractor.primeBatch([]string{sha}); err != nil {
		t.Fatalf("prime Beads-only commit: %v", err)
	}
	if _, ok := extractor.batchedSHAs[sha]; !ok {
		t.Fatal("verified empty commit was not memoized")
	}
	if files := extractor.fileCache[sha]; len(files) != 0 {
		t.Fatalf("Beads-only commit files=%#v, want none", files)
	}
	if stats := extractor.statCache[sha]; len(stats) != 0 {
		t.Fatalf("Beads-only commit line stats=%#v, want none", stats)
	}
}

func TestCoCommitGitParsersSupportSHA256Repository(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	repo := t.TempDir()
	initCmd := exec.Command("git", "init", "--object-format=sha256")
	initCmd.Dir = repo
	if out, err := initCmd.CombinedOutput(); err != nil {
		message := strings.ToLower(string(out))
		if strings.Contains(message, "unknown option") || strings.Contains(message, "unsupported") || strings.Contains(message, "unknown object format") || strings.Contains(message, "unknown hash algorithm") || strings.Contains(message, "invalid object format") {
			t.Skipf("local Git does not support SHA-256 repositories: %s", out)
		}
		t.Fatalf("initialize SHA-256 repository: %v: %s", err, out)
	}
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	const path = "sha256.go"
	if err := os.WriteFile(filepath.Join(repo, path), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write SHA-256 fixture: %v", err)
	}
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create SHA-256 beads directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"sha-bead\",\"title\":\"SHA-256\",\"status\":\"open\"}\n"), 0o644); err != nil {
		t.Fatalf("write SHA-256 beads fixture: %v", err)
	}
	runGit(t, repo, "add", "--", path, ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "add SHA-256 fixture and bead")
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("{\"id\":\"sha-bead\",\"title\":\"SHA-256\",\"status\":\"closed\"}\n"), 0o644); err != nil {
		t.Fatalf("close SHA-256 bead fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, path), []byte("package fixture\nconst changed = true\n"), 0o644); err != nil {
		t.Fatalf("update SHA-256 code fixture: %v", err)
	}
	runGit(t, repo, "add", "--", path, ".beads/issues.jsonl")
	runGit(t, repo, "commit", "-m", "close SHA-256 bead and change code")
	shaBytes, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve SHA-256 HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))
	if len(sha) != 64 || !isCanonicalCommitSHA(sha) {
		t.Fatalf("SHA-256 HEAD=%q, want canonical 64-character object ID", sha)
	}

	assertResult := func(label string, files []FileChange, stats map[string]lineStats) {
		t.Helper()
		if len(files) != 1 || files[0] != (FileChange{Path: path, Action: "M"}) {
			t.Fatalf("%s name-status=%#v, want modified %q", label, files, path)
		}
		if got, ok := stats[path]; !ok || got.insertions != 1 || got.deletions != 0 {
			t.Fatalf("%s numstat[%q]=%+v present=%t, want 1/0", label, path, got, ok)
		}
	}

	fallback := NewCoCommitExtractor(repo)
	files, err := fallback.getFilesChanged(sha)
	if err != nil {
		t.Fatalf("SHA-256 fallback name-status: %v", err)
	}
	stats, err := fallback.getLineStats(sha)
	if err != nil {
		t.Fatalf("SHA-256 fallback numstat: %v", err)
	}
	assertResult("fallback", files, stats)

	batched := NewCoCommitExtractor(repo)
	if err := batched.primeBatch([]string{sha}); err != nil {
		t.Fatalf("SHA-256 primeBatch: %v", err)
	}
	assertResult("batch", batched.fileCache[sha], batched.statCache[sha])

	extractor := NewExtractor(repo)
	for label, extract := range map[string]func(ExtractOptions) ([]BeadEvent, error){
		"legacy":   extractor.extractViaGitLogPatch,
		"snapshot": extractor.extractViaSnapshots,
	} {
		events, extractErr := extract(ExtractOptions{})
		if extractErr != nil {
			t.Fatalf("SHA-256 %s lifecycle extraction: %v", label, extractErr)
		}
		if len(events) != 2 || events[0].BeadID != "sha-bead" || events[0].EventType != EventCreated || len(events[0].CommitSHA) != 64 || events[1].BeadID != "sha-bead" || events[1].EventType != EventClosed || len(events[1].CommitSHA) != 64 {
			t.Fatalf("SHA-256 %s lifecycle events=%#v", label, events)
		}
	}

	report, err := NewCorrelator(repo).GenerateReport([]BeadInfo{{ID: "sha-bead", Title: "SHA-256", Status: "closed"}}, CorrelatorOptions{})
	if err != nil {
		t.Fatalf("SHA-256 end-to-end report: %v", err)
	}
	history, ok := report.Histories["sha-bead"]
	if !ok || len(history.Events) != 2 || len(history.Commits) != 1 || len(history.Commits[0].SHA) != 64 {
		t.Fatalf("SHA-256 end-to-end history=%+v present=%t", history, ok)
	}
}

func TestCoCommitPersistentCacheIgnoresShallowRepository(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	source := initTempGitRepo(t)
	advanceGitHead(t, source, "second")
	shallow := filepath.Join(t.TempDir(), "shallow")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()
	clone := exec.Command("git", "clone", "--depth=1", sourceURL, shallow)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("create shallow clone: %v: %s", err, out)
	}
	if coCommitPersistentCacheSafe(nil, shallow) {
		t.Fatal("shallow repository was declared safe for persistent co-commit caching")
	}

	shaBytes, err := exec.Command("git", "-C", shallow, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve shallow HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))
	namespace := perCommitCoCommitCacheNamespace(shallow)
	storePerCommitCoCommit(namespace, map[string]perCommitCoCommitEntry{
		sha: {
			CreatedAt: time.Now().UTC(),
			Files:     []FileChange{{Path: "poison.go", Action: "M"}},
			LineStats: map[string]lineStatsWire{"poison.go": {Insertions: 99}},
		},
	})

	extractor := NewCoCommitExtractor(shallow)
	if err := extractor.primeBatch([]string{sha}); err != nil {
		t.Fatalf("prime shallow batch: %v", err)
	}
	for _, file := range extractor.fileCache[sha] {
		if file.Path == "poison.go" {
			t.Fatal("shallow extraction served a persistent cache hit")
		}
	}
	if _, ok := extractor.statCache[sha]["poison.go"]; ok {
		t.Fatal("shallow extraction served persistent cached line stats")
	}
}

func TestReusedCorrelatorResetsCoCommitMemoAfterSameHeadDeepen(t *testing.T) {
	t.Setenv("BV_NO_CACHE", "1")
	source := initTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("initial\nchanged\n"), 0o644); err != nil {
		t.Fatalf("modify boundary fixture: %v", err)
	}
	runGit(t, source, "add", "README.md")
	runGit(t, source, "commit", "-m", "modify boundary fixture")
	shallow := cloneShallowRepoForCacheTest(t, source)
	headSHA, err := getGitHead(shallow)
	if err != nil {
		t.Fatalf("resolve shallow HEAD: %v", err)
	}
	event := BeadEvent{
		BeadID:    "bv-boundary",
		EventType: EventClosed,
		CommitSHA: headSHA,
		CommitMsg: "close boundary fixture",
	}

	reused := NewCorrelator(shallow)
	shallowCommits, err := reused.coCommitter.ExtractAllCoCommits([]BeadEvent{event})
	if err != nil {
		t.Fatalf("extract shallow boundary diff: %v", err)
	}
	if len(shallowCommits) != 1 || len(shallowCommits[0].Files) != 1 || shallowCommits[0].Files[0].Action != "A" {
		t.Fatalf("shallow boundary diff=%+v, want README root addition", shallowCommits)
	}
	if reused.coCommitter.batchedSHAs != nil {
		t.Fatalf("unsafe shallow memo survived extraction: %#v", reused.coCommitter.batchedSHAs)
	}

	runGit(t, shallow, "fetch", "--unshallow", "origin")
	if currentHead, headErr := getGitHead(shallow); headErr != nil || currentHead != headSHA {
		t.Fatalf("deepen changed HEAD: before=%q after=%q error=%v", headSHA, currentHead, headErr)
	}
	got, err := reused.coCommitter.ExtractAllCoCommits([]BeadEvent{event})
	if err != nil {
		t.Fatalf("extract full diff with reused correlator: %v", err)
	}
	want, err := NewCorrelator(shallow).coCommitter.ExtractAllCoCommits([]BeadEvent{event})
	if err != nil {
		t.Fatalf("extract full diff with fresh correlator: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reused correlator differs after deepen:\n got=%+v\nwant=%+v", got, want)
	}
	if len(got) != 1 || len(got[0].Files) != 1 || got[0].Files[0].Action != "M" || got[0].Files[0].Insertions != 1 {
		t.Fatalf("full boundary diff=%+v, want one-line README modification", got)
	}
}

func TestCoCommitExtractionIgnoresRepositoryRenameConfig(t *testing.T) {
	repo := initTempGitRepo(t)
	runGit(t, repo, "mv", "README.md", "RENAMED.md")
	runGit(t, repo, "commit", "-m", "rename readme")
	shaCmd := gitCommand(nil, "rev-parse", "HEAD")
	shaCmd.Dir = repo
	shaBytes, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("resolve fixture HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	for _, setting := range []string{"false", "copies", "true"} {
		runGit(t, repo, "config", "diff.renames", setting)
		files, err := NewCoCommitExtractor(repo).getFilesChanged(sha)
		if err != nil {
			t.Fatalf("extract with diff.renames=%s: %v", setting, err)
		}
		if len(files) != 1 || files[0].Path != "RENAMED.md" || files[0].Action != "R" {
			t.Fatalf("diff.renames=%s produced %#v, want one deterministic rename", setting, files)
		}
	}
}

func TestContainsBeadID(t *testing.T) {
	tests := []struct {
		text   string
		beadID string
		want   bool
	}{
		{"fix: resolve issue bv-123", "bv-123", true},
		{"feat(auth): implement login for BV-123", "bv-123", true}, // Case insensitive
		{"chore: update deps", "bv-123", false},
		{"work for bv-1234", "bv-123", false},
		{"work for other-bv-123", "bv-123", false},
		{"work for bv-123-extra", "bv-123", false},
		{"work for acme.42.extra", "acme.42", false},
		{"work for (bv-123).", "bv-123", true},
		{"", "bv-123", false},
		{"some text", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := containsBeadID(tt.text, tt.beadID)
			if got != tt.want {
				t.Errorf("containsBeadID(%q, %q) = %v, want %v", tt.text, tt.beadID, got, tt.want)
			}
		})
	}
}

func TestAllTestFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []FileChange
		want  bool
	}{
		{
			name:  "empty list",
			files: []FileChange{},
			want:  false,
		},
		{
			name: "all go tests",
			files: []FileChange{
				{Path: "pkg/auth/login_test.go"},
				{Path: "pkg/auth/session_test.go"},
			},
			want: true,
		},
		{
			name: "all js tests",
			files: []FileChange{
				{Path: "src/app.test.js"},
				{Path: "src/utils.spec.ts"},
			},
			want: true,
		},
		{
			name: "mixed files",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
				{Path: "pkg/auth/login_test.go"},
			},
			want: false,
		},
		{
			name: "no test files",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
				{Path: "pkg/auth/session.go"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allTestFiles(tt.files)
			if got != tt.want {
				t.Errorf("allTestFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShortSHA(t *testing.T) {
	tests := []struct {
		sha  string
		want string
	}{
		{"abc123def456789012345678901234567890abcd", "abc123d"},
		{"abc123", "abc123"},
		{"abc", "abc"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.sha, func(t *testing.T) {
			got := shortSHA(tt.sha)
			if got != tt.want {
				t.Errorf("shortSHA(%q) = %q, want %q", tt.sha, got, tt.want)
			}
		})
	}
}

func TestExtractNewPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Simple rename
		{"old.go => new.go", "new.go"},
		// With braces
		{"pkg/{old => new}/file.go", "pkg/new/file.go"},
		// Complex braces
		{"{old => new}.go", "new.go"},
		// No rename
		{"regular/path.go", "regular/path.go"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractNewPath(tt.input)
			if got != tt.want {
				t.Errorf("extractNewPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCalculateConfidence(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")
	now := time.Now()

	tests := []struct {
		name      string
		event     BeadEvent
		files     []FileChange
		wantRange [2]float64 // [min, max] expected range
	}{
		{
			name: "base case",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "fix: some bug",
			},
			files: []FileChange{
				{Path: "file.go"},
			},
			wantRange: [2]float64{0.94, 0.96}, // ~0.95
		},
		{
			name: "commit mentions bead ID",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "fix: resolve bv-123",
			},
			files: []FileChange{
				{Path: "file.go"},
			},
			wantRange: [2]float64{0.98, 1.0}, // 0.95 + 0.04 = 0.99
		},
		{
			name: "shotgun commit",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "refactor: big change",
			},
			files:     make([]FileChange, 25), // >20 files
			wantRange: [2]float64{0.84, 0.86}, // 0.95 - 0.10 = 0.85
		},
		{
			name: "only test files",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "test: add tests",
			},
			files: []FileChange{
				{Path: "auth_test.go"},
				{Path: "user_test.go"},
			},
			wantRange: [2]float64{0.89, 0.91}, // 0.95 - 0.05 = 0.90
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.Timestamp = now
			got := c.calculateConfidence(tt.event, tt.files)
			if got < tt.wantRange[0] || got > tt.wantRange[1] {
				t.Errorf("calculateConfidence() = %v, want in range [%v, %v]", got, tt.wantRange[0], tt.wantRange[1])
			}
		})
	}
}

func TestGenerateReason(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClosed,
		CommitMsg: "fix: resolve bv-123",
	}

	files := []FileChange{{Path: "file.go"}}

	reason := c.generateReason(event, files, 0.99)

	if reason == "" {
		t.Error("reason should not be empty")
	}

	// Should mention the event type
	if !strings.Contains(reason, "closed") {
		t.Errorf("reason should mention event type, got: %s", reason)
	}

	// Should mention bead ID reference
	if !strings.Contains(reason, "bead ID") {
		t.Errorf("reason should mention bead ID reference, got: %s", reason)
	}
}

func TestCoCommitExplicitIDSignalRequiresExactToken(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")
	files := []FileChange{{Path: "file.go"}}

	prefixOnly := BeadEvent{
		BeadID:    "bv-42",
		EventType: EventClosed,
		CommitMsg: "work for bv-420",
	}
	if got := c.calculateConfidence(prefixOnly, files); got != 0.95 {
		t.Fatalf("prefix-only confidence = %v, want base confidence 0.95", got)
	}
	if reason := c.generateReason(prefixOnly, files, 0.95); strings.Contains(reason, "references bead ID") {
		t.Fatalf("prefix-only reason falsely reports an explicit ID: %q", reason)
	}

	exact := prefixOnly
	exact.CommitMsg = "work for bv-42"
	if got := c.calculateConfidence(exact, files); got != 0.99 {
		t.Fatalf("exact-ID confidence = %v, want 0.99", got)
	}
	if reason := c.generateReason(exact, files, 0.99); !strings.Contains(reason, "references bead ID") {
		t.Fatalf("exact-ID reason omits explicit ID: %q", reason)
	}
}

func TestCreateCorrelatedCommit(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")
	now := time.Now()

	event := BeadEvent{
		BeadID:      "bv-123",
		EventType:   EventClosed,
		Timestamp:   now,
		CommitSHA:   "abc123def456",
		CommitMsg:   "fix: close bv-123",
		Author:      "Test User",
		AuthorEmail: "test@example.com",
	}

	files := []FileChange{
		{Path: "pkg/auth/login.go", Action: "M", Insertions: 10, Deletions: 5},
	}

	commit := c.CreateCorrelatedCommit(event, files)

	if commit.SHA != event.CommitSHA {
		t.Errorf("SHA mismatch: got %s, want %s", commit.SHA, event.CommitSHA)
	}
	if commit.ShortSHA != "abc123d" {
		t.Errorf("ShortSHA mismatch: got %s", commit.ShortSHA)
	}
	if commit.Method != MethodCoCommitted {
		t.Errorf("Method should be MethodCoCommitted, got %s", commit.Method)
	}
	if commit.Confidence < 0.9 {
		t.Errorf("Confidence should be high for bead ID mention, got %v", commit.Confidence)
	}
	if len(commit.Files) != 1 {
		t.Errorf("Files count mismatch: got %d, want 1", len(commit.Files))
	}
	if commit.Author != event.Author {
		t.Errorf("Author mismatch: got %s, want %s", commit.Author, event.Author)
	}
}

func TestNewCoCommitExtractor(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")
	if c.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", c.repoPath)
	}
}

func TestExtractAllCoCommits_Empty(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")

	// Empty events
	commits, err := c.ExtractAllCoCommits(nil)
	if err != nil {
		t.Fatalf("ExtractAllCoCommits(nil) failed: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("len(commits) = %d, want 0", len(commits))
	}
}

func TestExtractAllCoCommits_NonStatusEvents(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")

	// Only non-status events (created, modified)
	events := []BeadEvent{
		{BeadID: "bv-1", EventType: EventCreated, CommitSHA: "abc"},
		{BeadID: "bv-2", EventType: EventModified, CommitSHA: "def"},
	}

	commits, err := c.ExtractAllCoCommits(events)
	if err != nil {
		t.Fatalf("ExtractAllCoCommits failed: %v", err)
	}
	// Should skip non-status events
	if len(commits) != 0 {
		t.Errorf("len(commits) = %d, want 0 (non-status events)", len(commits))
	}
}

func TestExtractAllCoCommits_PropagatesBatchFailureWithoutMemoizing(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	sha := strings.Repeat("a", 40)
	c := NewCoCommitExtractor(t.TempDir()) // Existing directory, intentionally not a Git repository.
	commits, err := c.ExtractAllCoCommits([]BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, CommitSHA: sha},
	})
	if err == nil {
		t.Fatal("expected the failed git batch to propagate")
	}
	if !strings.Contains(err.Error(), "priming co-commit batch") {
		t.Fatalf("error = %q, want co-commit batch context", err)
	}
	if commits != nil {
		t.Fatalf("commits = %#v, want nil on batch failure", commits)
	}
	if _, ok := c.batchedSHAs[sha]; ok {
		t.Fatal("failed SHA was marked as successfully batched")
	}
	if _, ok := c.fileCache[sha]; ok {
		t.Fatal("failed SHA was memoized in the file cache")
	}
	if _, ok := c.statCache[sha]; ok {
		t.Fatal("failed SHA was memoized in the line-stat cache")
	}
	if disk := loadPerCommitCoCommit(perCommitCoCommitCacheNamespace(c.repoPath)); disk != nil {
		if _, ok := disk[sha]; ok {
			t.Fatal("failed SHA was persisted in the per-commit cache")
		}
	}

	if _, err := c.ExtractAllCoCommits([]BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, CommitSHA: sha},
	}); err == nil {
		t.Fatal("second attempt unexpectedly used a poisoned cache entry")
	}
}

func TestGenerateReason_LargeCommit(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClaimed,
		CommitMsg: "big change",
	}

	// Create > 20 files to trigger large commit message
	files := make([]FileChange, 25)
	for i := range files {
		files[i] = FileChange{Path: "file" + string(rune('a'+i)) + ".go"}
	}

	reason := c.generateReason(event, files, 0.85)

	if !strings.Contains(reason, "large commit") {
		t.Errorf("reason should mention large commit, got: %s", reason)
	}
}

func TestGenerateReason_OnlyTestFiles(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClaimed,
		CommitMsg: "add tests",
	}

	files := []FileChange{
		{Path: "auth_test.go"},
		{Path: "login_test.go"},
	}

	reason := c.generateReason(event, files, 0.90)

	if !strings.Contains(reason, "test files") {
		t.Errorf("reason should mention test files, got: %s", reason)
	}
}

func TestCalculateConfidence_Combined(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	// Test combination: shotgun commit with bead ID mention
	event := BeadEvent{
		BeadID:    "bv-123",
		CommitMsg: "big refactor bv-123",
	}

	files := make([]FileChange, 30)
	for i := range files {
		files[i] = FileChange{Path: "file" + string(rune('a'+i)) + ".go"}
	}

	confidence := c.calculateConfidence(event, files)

	// Base 0.95 + 0.04 (bead ID) - 0.10 (shotgun) = 0.89
	if confidence < 0.88 || confidence > 0.90 {
		t.Errorf("Combined confidence = %v, expected ~0.89", confidence)
	}
}

func TestExtractNewPath_DoubleSlashBug(t *testing.T) {
	// Git output for renaming "pkg/old/file.go" to "pkg/file.go"
	// is "pkg/{old => }/file.go"
	input := "pkg/{old => }/file.go"

	// We expect "pkg/file.go"
	expected := "pkg/file.go"

	got := extractNewPath(input)

	if got != expected {
		t.Errorf("extractNewPath(%q) = %q; want %q", input, got, expected)
	}
}

func TestExtractNewPath_ComplexCases(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"{old => new}", "new"},
		{"src/{old => new}/main.go", "src/new/main.go"},
		{"src/{ => new}/main.go", "src/new/main.go"}, // Addition
		{"src/{old => }/main.go", "src/main.go"},     // Deletion - vulnerable case
		{"old => new", "new"},
	}

	for _, tc := range cases {
		got := extractNewPath(tc.input)
		if got != tc.expected {
			t.Errorf("extractNewPath(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}
