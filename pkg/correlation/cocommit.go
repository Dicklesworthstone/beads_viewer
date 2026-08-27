// Package correlation provides extraction of co-committed files for bead correlation.
package correlation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// renamePattern matches git's brace notation for renames: {old => new}
var renamePattern = regexp.MustCompile(`\{[^}]* => ([^}]*)\}`)

// coCommitFetchedSHAsCounter counts the number of commit SHAs that primeBatch
// actually fetched from git (via the batched `git log` passes) across the
// process — i.e. SHAs that missed BOTH the in-memory and the persistent
// per-commit co-commit cache. It exists solely so tests can prove the incremental
// cache fetches only the NEW commits' co-commit data (and 0 when nothing is new).
// It is never read on any production path.
var coCommitFetchedSHAsCounter int64

// Keep each git invocation comfortably below even conservative OS argument
// limits. The largest supported revision (a SHA-256 object ID) consumes 65
// argument bytes including its terminator, so this leaves ample room for the
// environment, options, and pathspecs.
const (
	maxCoCommitSHAsPerGitCommand = 512
	coCommitRenameLimit          = 1000
	coCommitGitPolicyVersion     = "co-commit-diff-v4"
	// coCommitBatchHeaderFormat ends each SHA header with two NUL bytes. At a
	// record boundary that cannot be confused with either a name-status action
	// (which starts with a status letter) or a numstat record (which starts with
	// a decimal count or '-'). Paths are consumed according to their -z framing,
	// so even a path containing newlines and SHA-looking text stays opaque.
	coCommitBatchHeaderFormat = "%H%x00%x00"
)

// coCommitGitPolicyNamespaceInputs binds every option that can change the
// co-committed FileChange artifacts. Both the per-commit cache and the outer
// report/artifact caches must include this policy because an outer hit bypasses
// co-commit extraction entirely.
func coCommitGitPolicyNamespaceInputs() []string {
	inputs := []string{coCommitGitPolicyVersion}
	inputs = append(inputs, repoGitPolicyArgs()...)
	inputs = append(inputs, coCommitGitConfigArgs()...)
	inputs = append(inputs, coCommitDiffArgs("")...)
	inputs = append(inputs, excludePathspecArgs()...)
	return inputs
}

// coCommitGitConfigArgs fixes the config-controlled parts of Git's diff output.
// Keep this policy paired with coCommitDiffArgs and include both in the
// persistent-cache namespace; changing either policy must invalidate old data.
func coCommitGitConfigArgs() []string {
	return []string{
		"-c", "color.ui=false",
		"-c", "core.quotePath=true",
		"-c", "core.bigFileThreshold=512m",
		"-c", "diff.renames=true",
		"-c", fmt.Sprintf("diff.renameLimit=%d", coCommitRenameLimit),
		"-c", "diff.algorithm=default",
	}
}

// coCommitDiffArgs fixes every diff/log option that can alter cached file
// actions, paths, order-independent line counts, or merge behavior. --text
// intentionally bypasses repository-local diff attributes so the same blobs do
// not flip between numeric and binary numstat output across clones.
func coCommitDiffArgs(diffFlag string) []string {
	args := make([]string, 0, 15)
	if diffFlag != "" {
		args = append(args, diffFlag)
	}
	return append(args,
		"-z",
		"--find-renames=50%",
		fmt.Sprintf("-l%d", coCommitRenameLimit),
		"--no-rename-empty",
		"--diff-algorithm=default",
		"--no-indent-heuristic",
		"--no-ext-diff",
		"--no-textconv",
		"--text",
		"--ignore-submodules=none",
		"--submodule=short",
		"--no-relative",
		"--diff-merges=first-parent",
		"--root",
		"--no-show-signature",
		"--no-decorate",
	)
}

// coCommitGitCommand prevents ambient repository-routing and diff environment
// variables from silently changing the meaning of repoPath or the cached diff.
func coCommitGitCommand(ctx context.Context, repoPath string, args []string) *exec.Cmd {
	gitArgs := append(coCommitGitConfigArgs(), args...)
	return repoGitCommand(ctx, repoPath, gitArgs...)
}

const (
	coCommitHistoryStateFull        = "full-history-v1"
	coCommitHistoryStateShallow     = "shallow-history-v1"
	coCommitHistoryStateUnavailable = "unavailable-history-v1"
)

// coCommitRepositoryHistoryState returns the repository state that affects a
// boundary commit's first-parent diff. Callers bind this value into higher-level
// cache namespaces; they persist only the full-history state.
func coCommitRepositoryHistoryState(ctx context.Context, repoPath string) string {
	cmd := repoGitCommand(ctx, repoPath, "rev-parse", "--is-shallow-repository")
	out, err := cmd.Output()
	if err != nil {
		return coCommitHistoryStateUnavailable
	}
	switch strings.TrimSpace(string(out)) {
	case "false":
		return coCommitHistoryStateFull
	case "true":
		return coCommitHistoryStateShallow
	default:
		return coCommitHistoryStateUnavailable
	}
}

// coCommitPersistentCacheSafe reports whether a commit's first-parent diff is
// stable under the repository state that is not encoded in the per-commit key.
// A shallow boundary changes Git's parent view: a boundary commit is diffed as
// a root commit until the repository is deepened, even though its SHA is
// unchanged. Persistent hits and stores are therefore disabled for shallow
// repositories. A failed probe also fails closed; extraction still proceeds,
// but only through process-local memoization.
func coCommitPersistentCacheSafe(ctx context.Context, repoPath string) bool {
	return coCommitRepositoryHistoryState(ctx, repoPath) == coCommitHistoryStateFull
}

// CoCommitExtractor extracts files that were changed in the same commit as bead changes
type CoCommitExtractor struct {
	repoPath string

	// mu serializes the two public extraction entry points. CachedCorrelator's
	// singleflight is keyed by report inputs, so different keys may concurrently
	// share one Correlator and therefore this process-local memo.
	mu sync.Mutex

	// ctx, when set (via Correlator.WithContext or directly), bounds the git
	// subprocesses spawned during co-commit extraction (issue #166). nil means
	// context.Background().
	ctx context.Context

	// Memoized per-commit diff data, populated lazily by primeBatch. Once a SHA
	// is present in batchedSHAs, getFilesChanged/getLineStats serve it from these
	// maps instead of forking a per-commit `git show`. See #161 batch fan-out fix.
	fileCache   map[string][]FileChange
	statCache   map[string]map[string]lineStats
	batchedSHAs map[string]struct{}

	// memoizedHistoryState binds the process-local maps above to the repository
	// history shape under which Git produced them. A shallow boundary can change
	// after fetch --deepen without changing any commit SHA.
	memoizedHistoryState string
}

// NewCoCommitExtractor creates a new co-commit extractor
func NewCoCommitExtractor(repoPath string) *CoCommitExtractor {
	return &CoCommitExtractor{repoPath: repoPath}
}

func (c *CoCommitExtractor) resetMemoizedDiffs() {
	c.fileCache = nil
	c.statCache = nil
	c.batchedSHAs = nil
}

func (c *CoCommitExtractor) prepareMemoizedHistoryState() string {
	state := coCommitRepositoryHistoryState(c.ctx, c.repoPath)
	if state != coCommitHistoryStateFull || state != c.memoizedHistoryState {
		c.resetMemoizedDiffs()
	}
	c.memoizedHistoryState = state
	return state
}

// codeFileExtensions lists file extensions considered "code files"
var codeFileExtensions = map[string]bool{
	".go":    true,
	".py":    true,
	".js":    true,
	".ts":    true,
	".jsx":   true,
	".tsx":   true,
	".rs":    true,
	".java":  true,
	".kt":    true,
	".swift": true,
	".c":     true,
	".cpp":   true,
	".h":     true,
	".hpp":   true,
	".rb":    true,
	".php":   true,
	".cs":    true,
	".scala": true,
	".yaml":  true,
	".yml":   true,
	".json":  true,
	".toml":  true,
	".md":    true,
	".sql":   true,
	".sh":    true,
	".bash":  true,
	".zsh":   true,
}

// excludedPaths lists path prefixes that should be excluded
var excludedPaths = []string{
	".beads/",
	".bv/",
	".git/",
	"node_modules/",
	"vendor/",
	"__pycache__/",
	".venv/",
	"venv/",
	"dist/",
	"build/",
	".next/",
}

// ExtractCoCommittedFiles extracts code files changed in the same commit as a bead event
func (c *CoCommitExtractor) ExtractCoCommittedFiles(event BeadEvent) ([]FileChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.prepareMemoizedHistoryState()
	return c.extractCoCommittedFiles(event)
}

func (c *CoCommitExtractor) extractCoCommittedFiles(event BeadEvent) ([]FileChange, error) {
	// Get file list with status
	files, err := c.getFilesChanged(event.CommitSHA)
	if err != nil {
		return nil, err
	}

	// Get line stats
	stats, err := c.getLineStats(event.CommitSHA)
	if err != nil {
		return nil, fmt.Errorf("extracting co-commit line stats: %w", err)
	}

	// Filter to code files only
	var codeFiles []FileChange
	for _, f := range files {
		if !isCodeFile(f.Path) {
			continue
		}
		if isExcludedPath(f.Path) {
			continue
		}

		// Add line stats if available
		if s, ok := stats[f.Path]; ok {
			f.Insertions = s.insertions
			f.Deletions = s.deletions
		}

		codeFiles = append(codeFiles, f)
	}

	return codeFiles, nil
}

// CreateCorrelatedCommit creates a CorrelatedCommit with confidence scoring
func (c *CoCommitExtractor) CreateCorrelatedCommit(event BeadEvent, files []FileChange) CorrelatedCommit {
	confidence := c.calculateConfidence(event, files)
	reason := c.generateReason(event, files, confidence)

	return CorrelatedCommit{
		BeadID:      event.BeadID,
		SHA:         event.CommitSHA,
		ShortSHA:    shortSHA(event.CommitSHA),
		Message:     event.CommitMsg,
		Author:      event.Author,
		AuthorEmail: event.AuthorEmail,
		Timestamp:   event.Timestamp,
		Files:       files,
		Method:      MethodCoCommitted,
		Confidence:  confidence,
		Reason:      reason,
	}
}

// lineStats holds insertion/deletion counts for a file
type lineStats struct {
	insertions int
	deletions  int
}

// excludePathspecArgs builds git pathspec arguments that exclude the directories
// in excludedPaths. These are appended after a "--" separator so git skips diffing
// the (often large) excluded blobs (e.g. .beads/issues.jsonl) entirely instead of
// computing line stats for content the caller discards via isExcludedPath. See #160.
func excludePathspecArgs() []string {
	args := make([]string, 0, len(excludedPaths)+2)
	args = append(args, "--", ".")
	for _, prefix := range excludedPaths {
		// Trim trailing slash; ':(exclude,glob)dir/**' matches everything under dir.
		dir := strings.TrimSuffix(prefix, "/")
		args = append(args, fmt.Sprintf(":(exclude,glob)%s/**", dir))
	}
	return args
}

// primeBatch fetches name-status and numstat for every requested SHA in bounded
// pairs of `git log` invocations and memoizes the result, so subsequent
// getFilesChanged/getLineStats calls for those SHAs are served from memory
// instead of forking one `git show` per commit. SHAs already batched are
// skipped, keeping the call idempotent.
//
// We use `git log --no-walk=unsorted --sparse <SHAs>` rather than N×`git show`:
// --sparse keeps each explicitly requested commit's header even when the
// pathspec excludes its entire diff (the common metadata-only Beads commit), and
// each process streams every requested commit's first-parent diff (exactly what
// `git show` computes) for a bounded subset. Two passes per subset are
// required because git's --name-status and --numstat are mutually exclusive
// in one invocation (the last
// flag wins); the status letters live in one pass and the +/- line counts in the
// other, matching the two existing parsers byte-for-byte. The same exclude
// pathspecs are applied. Git still emits the explicit commit header when its
// filtered diff is empty, so the strict parser can distinguish a verified empty
// contribution from a truncated/missing commit. Either Git-pass or parse failure
// is returned before any missing SHA is marked complete or cached.
func (c *CoCommitExtractor) primeBatch(shas []string) error {
	objectIDWidth := 0
	for _, sha := range shas {
		if sha == "" {
			continue
		}
		if !isCanonicalCommitSHA(sha) {
			return fmt.Errorf("invalid co-commit SHA %q", sha)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(sha)
		} else if len(sha) != objectIDWidth {
			return fmt.Errorf("mixed-width co-commit SHAs: got %d and %d characters", objectIDWidth, len(sha))
		}
	}
	if objectIDWidth == 0 {
		return nil
	}
	historyState := c.prepareMemoizedHistoryState()
	if c.fileCache == nil {
		c.fileCache = make(map[string][]FileChange)
		c.statCache = make(map[string]map[string]lineStats)
		c.batchedSHAs = make(map[string]struct{})
	}

	want := make([]string, 0, len(shas))
	wantSet := make(map[string]struct{}, len(shas))
	for _, sha := range shas {
		if sha == "" {
			continue
		}
		if _, done := c.batchedSHAs[sha]; done {
			continue
		}
		if _, queued := wantSet[sha]; queued {
			continue
		}
		wantSet[sha] = struct{}{}
		want = append(want, sha)
	}
	if len(want) == 0 {
		return nil
	}

	// Persistent layer: within one repository namespace, a commit's
	// (files, lineStats) is fixed by the SHA, the explicit Git diff policy, and
	// the exclude-pathspec set — see per_commit_cocommit_cache.go. Serve
	// SHAs already on disk straight into the in-memory maps and run the batched
	// `git log` ONLY for SHAs missing from both memory and disk. A disk hit
	// populates fileCache/statCache identically to the git path: Files in stable
	// path/action order, LineStats reconstructed exactly via fromLineStatsMap,
	// and an empty diff memoized as nil files + empty stat map. So the in-memory
	// maps are semantically identical regardless of how many SHAs came from disk.
	namespace := perCommitCoCommitCacheNamespace(c.repoPath)
	persistentCacheSafe := correlationDiskCacheEnabled() && historyState == coCommitHistoryStateFull
	var disk map[string]perCommitCoCommitEntry
	if persistentCacheSafe {
		disk = loadPerCommitCoCommit(namespace)
	}

	missing := want
	if disk != nil {
		missing = make([]string, 0, len(want))
		for _, sha := range want {
			if e, ok := disk[sha]; ok {
				c.fileCache[sha] = e.Files
				c.statCache[sha] = fromLineStatsMap(e.LineStats)
				c.batchedSHAs[sha] = struct{}{}
				continue
			}
			missing = append(missing, sha)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	files := make(map[string][]FileChange, len(missing))
	stats := make(map[string]map[string]lineStats, len(missing))
	for _, batch := range coCommitSHABatches(missing) {
		batchFiles, err := c.batchFilesChanged(batch)
		if err != nil {
			return fmt.Errorf("batching co-commit name-status: %w", err)
		}
		batchStats, err := c.batchLineStats(batch)
		if err != nil {
			return fmt.Errorf("batching co-commit numstat: %w", err)
		}
		atomic.AddInt64(&coCommitFetchedSHAsCounter, int64(len(batch)))
		for sha, changed := range batchFiles {
			files[sha] = changed
		}
		for sha, lineCounts := range batchStats {
			stats[sha] = lineCounts
		}
	}

	fresh := make(map[string]perCommitCoCommitEntry, len(missing))
	now := time.Now().UTC()
	for _, sha := range missing {
		// Both strict batch parsers require a header for every requested SHA.
		// A header with no following records is a verified empty diff, represented
		// as nil files plus an empty stats map so future lookups do not re-fork Git.
		c.fileCache[sha] = files[sha]
		s, ok := stats[sha]
		if !ok {
			s = map[string]lineStats{}
		}
		c.statCache[sha] = s
		c.batchedSHAs[sha] = struct{}{}
		fresh[sha] = perCommitCoCommitEntry{
			CreatedAt: now,
			Files:     files[sha],
			LineStats: toLineStatsMap(s),
		}
	}
	// Persist only the freshly fetched SHAs (no-rewrite-on-pure-hit: when every
	// requested SHA was already on disk, missing is empty and we returned above).
	// Both git passes succeeded before any missing SHA was memoized, so a transient
	// failure can neither masquerade as an empty diff nor poison a higher-level
	// history artifact/report cache.
	if persistentCacheSafe && coCommitPersistentCacheSafe(c.ctx, c.repoPath) {
		storePerCommitCoCommit(namespace, fresh)
	}
	return nil
}

func coCommitSHABatches(shas []string) [][]string {
	if len(shas) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(shas)+maxCoCommitSHAsPerGitCommand-1)/maxCoCommitSHAsPerGitCommand)
	for start := 0; start < len(shas); start += maxCoCommitSHAsPerGitCommand {
		end := min(start+maxCoCommitSHAsPerGitCommand, len(shas))
		batches = append(batches, shas[start:end])
	}
	return batches
}

func isCanonicalCommitSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for i := range len(sha) {
		if (sha[i] < '0' || sha[i] > '9') && (sha[i] < 'a' || sha[i] > 'f') {
			return false
		}
	}
	return true
}

// batchLogArgs builds `git log --no-walk=unsorted <diffFlag> --format=<header>
// <SHAs> -- <exclude pathspecs>`, reusing the streaming-log header and exclude
// helpers shared with the snapshot extractor.
func batchLogArgs(diffFlag string, shas []string) []string {
	args := make([]string, 0, len(shas)+24)
	args = append(args, "log", "--no-walk=unsorted", "--sparse")
	args = append(args, coCommitDiffArgs(diffFlag)...)
	args = append(args, "--format="+coCommitBatchHeaderFormat, "--end-of-options")
	args = append(args, shas...)
	args = append(args, excludePathspecArgs()...)
	return args
}

// batchFilesChanged runs one `git log --name-status` over all SHAs and returns
// per-SHA FileChange lists using the same parsing as getFilesChanged. It
// returns an error (not just an empty map) on git failure so
// the caller can avoid persisting a poisoned "empty diff" entry for SHAs that
// were never actually inspected. A successful run with a genuinely-empty diff for
// some SHA still returns no error (the empty result is correct and cacheable).
func (c *CoCommitExtractor) batchFilesChanged(shas []string) (map[string][]FileChange, error) {
	cmd := coCommitGitCommand(c.ctx, c.repoPath, batchLogArgs("--name-status", shas))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files, err := parseBatchNameStatus(out, shas)
	if err != nil {
		return nil, fmt.Errorf("parsing batched name-status: %w", err)
	}
	return files, nil
}

// batchLineStats runs one `git log --numstat` over all SHAs and returns per-SHA
// line-stat maps using the same parsing as getLineStats. Like batchFilesChanged,
// a git failure is surfaced as an error so the caller skips persisting empties.
func (c *CoCommitExtractor) batchLineStats(shas []string) (map[string]map[string]lineStats, error) {
	cmd := coCommitGitCommand(c.ctx, c.repoPath, batchLogArgs("--numstat", shas))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	stats, err := parseBatchNumstat(out, shas)
	if err != nil {
		return nil, fmt.Errorf("parsing batched numstat: %w", err)
	}
	return stats, nil
}

// coCommitRecordPadding skips only Git's formatting newlines between the
// pretty-printed commit header and the first -z record (and between commits).
// It is called only at record boundaries; path bytes, including leading or
// trailing newlines, are consumed by the record parsers and never trimmed.
func coCommitRecordPadding(data []byte, pos int) int {
	for pos < len(data) && (data[pos] == '\n' || data[pos] == '\r') {
		pos++
	}
	return pos
}

// coCommitBatchPadding also accepts Git's optional NUL commit separator. This
// is used only between fully consumed records; a missing path field is detected
// inside parseNameStatusRecord/parseNumstatRecord before control returns here.
func coCommitBatchPadding(data []byte, pos int) int {
	for pos < len(data) && (data[pos] == 0 || data[pos] == '\n' || data[pos] == '\r') {
		pos++
	}
	return pos
}

func parseCoCommitBatchHeader(data []byte, pos int) (string, int, bool) {
	for _, objectIDLen := range [...]int{40, 64} {
		headerLen := objectIDLen + 2
		if len(data)-pos < headerLen || data[pos+objectIDLen] != 0 || data[pos+objectIDLen+1] != 0 {
			continue
		}
		sha := string(data[pos : pos+objectIDLen])
		if isCanonicalCommitSHA(sha) {
			return sha, pos + headerLen, true
		}
	}
	return "", pos, false
}

func expectedCoCommitSHAs(shas []string) map[string]struct{} {
	expected := make(map[string]struct{}, len(shas))
	for _, sha := range shas {
		expected[sha] = struct{}{}
	}
	return expected
}

func validateCoCommitSHASet(shas []string) error {
	objectIDWidth := 0
	for _, sha := range shas {
		if !isCanonicalCommitSHA(sha) {
			return fmt.Errorf("invalid co-commit SHA %q", sha)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(sha)
		} else if len(sha) != objectIDWidth {
			return fmt.Errorf("mixed-width co-commit SHAs: got %d and %d characters", objectIDWidth, len(sha))
		}
	}
	return nil
}

func normalizedNameStatusAction(raw []byte) (string, error) {
	if len(raw) == 0 || !strings.ContainsRune("ACDMRTUXB", rune(raw[0])) {
		return "", fmt.Errorf("invalid name-status action %q", raw)
	}
	if len(raw) > 1 {
		// Git documents a similarity score for R/C and may attach a
		// dissimilarity score to M when rewrite detection is active.
		if raw[0] != 'R' && raw[0] != 'C' && raw[0] != 'M' {
			return "", fmt.Errorf("invalid name-status action %q", raw)
		}
		for _, digit := range raw[1:] {
			if digit < '0' || digit > '9' {
				return "", fmt.Errorf("invalid name-status action %q", raw)
			}
		}
	}
	return string(raw[0]), nil
}

func nextNULTerminatedField(data []byte, pos int, field string) ([]byte, int, error) {
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("truncated %s", field)
	}
	relEnd := bytes.IndexByte(data[pos:], 0)
	if relEnd < 0 {
		return nil, pos, fmt.Errorf("unterminated %s", field)
	}
	end := pos + relEnd
	return data[pos:end], end + 1, nil
}

func parseNameStatusRecord(data []byte, pos int) (FileChange, int, error) {
	rawAction, next, err := nextNULTerminatedField(data, pos, "name-status action")
	if err != nil {
		return FileChange{}, pos, err
	}
	action, err := normalizedNameStatusAction(rawAction)
	if err != nil {
		return FileChange{}, pos, err
	}

	oldPath, next, err := nextNULTerminatedField(data, next, "name-status path")
	if err != nil {
		return FileChange{}, pos, err
	}
	if len(oldPath) == 0 {
		return FileChange{}, pos, fmt.Errorf("empty name-status path for action %q", rawAction)
	}
	path := oldPath
	if action == "R" || action == "C" {
		path, next, err = nextNULTerminatedField(data, next, "name-status post-image path")
		if err != nil {
			return FileChange{}, pos, err
		}
		if len(path) == 0 {
			return FileChange{}, pos, fmt.Errorf("empty name-status post-image path for action %q", rawAction)
		}
	}

	return FileChange{Path: string(path), Action: action}, next, nil
}

func sortFileChanges(files []FileChange) {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Path == files[j].Path {
			return files[i].Action < files[j].Action
		}
		return files[i].Path < files[j].Path
	})
}

// parseNameStatus parses `git --name-status -z` output. Git uses NUL for every
// field boundary, so paths are returned byte-for-byte without core.quotePath
// quoting and may safely contain tabs or newlines.
func parseNameStatus(payload []byte) ([]FileChange, error) {
	var files []FileChange
	for pos := 0; ; {
		pos = coCommitRecordPadding(payload, pos)
		if pos == len(payload) {
			break
		}
		file, next, err := parseNameStatusRecord(payload, pos)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
		pos = next
	}
	sortFileChanges(files)
	return files, nil
}

func parseBatchNameStatus(out []byte, shas []string) (map[string][]FileChange, error) {
	if err := validateCoCommitSHASet(shas); err != nil {
		return nil, err
	}
	files := make(map[string][]FileChange, len(shas))
	expected := expectedCoCommitSHAs(shas)
	seenHeaders := make(map[string]struct{}, len(shas))
	currentSHA := ""

	for pos := 0; ; {
		pos = coCommitBatchPadding(out, pos)
		if pos == len(out) {
			break
		}
		if sha, next, ok := parseCoCommitBatchHeader(out, pos); ok {
			if _, ok := expected[sha]; !ok {
				return nil, fmt.Errorf("unexpected commit header %s", sha)
			}
			if _, duplicate := seenHeaders[sha]; duplicate {
				return nil, fmt.Errorf("duplicate commit header %s", sha)
			}
			seenHeaders[sha] = struct{}{}
			currentSHA = sha
			files[sha] = nil
			pos = next
			continue
		}
		if currentSHA == "" {
			return nil, fmt.Errorf("name-status record before commit header at byte %d", pos)
		}
		file, next, err := parseNameStatusRecord(out, pos)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", currentSHA, err)
		}
		files[currentSHA] = append(files[currentSHA], file)
		pos = next
	}
	for sha := range files {
		sortFileChanges(files[sha])
	}
	for _, sha := range shas {
		if _, seen := seenHeaders[sha]; !seen {
			return nil, fmt.Errorf("missing commit header %s", sha)
		}
	}
	return files, nil
}

func parseNumstatCount(raw []byte, field string) (int, error) {
	if bytes.Equal(raw, []byte("-")) {
		return 0, nil
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("empty numstat %s", field)
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid numstat %s %q", field, raw)
		}
	}
	value, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid numstat %s %q: %w", field, raw, err)
	}
	return value, nil
}

func parseNumstatRecord(data []byte, pos int) (string, lineStats, int, error) {
	firstTab := bytes.IndexByte(data[pos:], '\t')
	if firstTab < 0 {
		return "", lineStats{}, pos, fmt.Errorf("unterminated numstat insertion count")
	}
	firstTab += pos
	secondTab := bytes.IndexByte(data[firstTab+1:], '\t')
	if secondTab < 0 {
		return "", lineStats{}, pos, fmt.Errorf("unterminated numstat deletion count")
	}
	secondTab += firstTab + 1

	insertions, err := parseNumstatCount(data[pos:firstTab], "insertion count")
	if err != nil {
		return "", lineStats{}, pos, err
	}
	deletions, err := parseNumstatCount(data[firstTab+1:secondTab], "deletion count")
	if err != nil {
		return "", lineStats{}, pos, err
	}

	pathStart := secondTab + 1
	if pathStart >= len(data) {
		return "", lineStats{}, pos, fmt.Errorf("truncated numstat path")
	}
	var path []byte
	var next int
	if data[pathStart] == 0 {
		// Under -z, rename/copy numstat records encode an empty path field,
		// followed by separately NUL-terminated pre-image and post-image paths.
		oldPath, afterOld, fieldErr := nextNULTerminatedField(data, pathStart+1, "numstat pre-image path")
		if fieldErr != nil {
			return "", lineStats{}, pos, fieldErr
		}
		if len(oldPath) == 0 {
			return "", lineStats{}, pos, fmt.Errorf("empty numstat pre-image path")
		}
		path, next, fieldErr = nextNULTerminatedField(data, afterOld, "numstat post-image path")
		if fieldErr != nil {
			return "", lineStats{}, pos, fieldErr
		}
	} else {
		path, next, err = nextNULTerminatedField(data, pathStart, "numstat path")
		if err != nil {
			return "", lineStats{}, pos, err
		}
	}
	if len(path) == 0 {
		return "", lineStats{}, pos, fmt.Errorf("empty numstat path")
	}
	return string(path), lineStats{insertions: insertions, deletions: deletions}, next, nil
}

// parseNumstat parses `git --numstat -z` output into a per-path lineStats map.
func parseNumstat(payload []byte) (map[string]lineStats, error) {
	stats := make(map[string]lineStats)
	for pos := 0; ; {
		pos = coCommitRecordPadding(payload, pos)
		if pos == len(payload) {
			break
		}
		path, counts, next, err := parseNumstatRecord(payload, pos)
		if err != nil {
			return nil, err
		}
		stats[path] = counts
		pos = next
	}
	return stats, nil
}

func parseBatchNumstat(out []byte, shas []string) (map[string]map[string]lineStats, error) {
	if err := validateCoCommitSHASet(shas); err != nil {
		return nil, err
	}
	stats := make(map[string]map[string]lineStats, len(shas))
	expected := expectedCoCommitSHAs(shas)
	seenHeaders := make(map[string]struct{}, len(shas))
	currentSHA := ""

	for pos := 0; ; {
		pos = coCommitBatchPadding(out, pos)
		if pos == len(out) {
			break
		}
		if sha, next, ok := parseCoCommitBatchHeader(out, pos); ok {
			if _, ok := expected[sha]; !ok {
				return nil, fmt.Errorf("unexpected commit header %s", sha)
			}
			if _, duplicate := seenHeaders[sha]; duplicate {
				return nil, fmt.Errorf("duplicate commit header %s", sha)
			}
			seenHeaders[sha] = struct{}{}
			currentSHA = sha
			stats[sha] = make(map[string]lineStats)
			pos = next
			continue
		}
		if currentSHA == "" {
			return nil, fmt.Errorf("numstat record before commit header at byte %d", pos)
		}
		path, counts, next, err := parseNumstatRecord(out, pos)
		if err != nil {
			return nil, fmt.Errorf("commit %s: %w", currentSHA, err)
		}
		stats[currentSHA][path] = counts
		pos = next
	}
	for _, sha := range shas {
		if _, seen := seenHeaders[sha]; !seen {
			return nil, fmt.Errorf("missing commit header %s", sha)
		}
	}
	return stats, nil
}

// getFilesChanged returns the name-status file list for a commit. When the SHA
// was primed via primeBatch it is served from the in-memory cache; otherwise it
// falls back to a per-commit `git show --name-status`.
func (c *CoCommitExtractor) getFilesChanged(sha string) ([]FileChange, error) {
	if !isCanonicalCommitSHA(sha) {
		return nil, fmt.Errorf("invalid co-commit SHA %q", sha)
	}
	if c.fileCache != nil {
		if _, ok := c.batchedSHAs[sha]; ok {
			return c.fileCache[sha], nil
		}
	}

	gitArgs := append([]string{"show"}, coCommitDiffArgs("--name-status")...)
	gitArgs = append(gitArgs, "--format=", "--end-of-options", sha)
	gitArgs = append(gitArgs, excludePathspecArgs()...)
	cmd := coCommitGitCommand(c.ctx, c.repoPath, gitArgs)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show --name-status failed: %w", err)
	}

	files, err := parseNameStatus(out)
	if err != nil {
		return nil, fmt.Errorf("parsing git show --name-status: %w", err)
	}
	return files, nil
}

// getLineStats returns insertion/deletion counts per file for a commit. When the
// SHA was primed via primeBatch it is served from the in-memory cache; otherwise
// it falls back to a per-commit `git show --numstat`.
func (c *CoCommitExtractor) getLineStats(sha string) (map[string]lineStats, error) {
	if !isCanonicalCommitSHA(sha) {
		return nil, fmt.Errorf("invalid co-commit SHA %q", sha)
	}
	if c.statCache != nil {
		if _, ok := c.batchedSHAs[sha]; ok {
			return c.statCache[sha], nil
		}
	}

	gitArgs := append([]string{"show"}, coCommitDiffArgs("--numstat")...)
	gitArgs = append(gitArgs, "--format=", "--end-of-options", sha)
	gitArgs = append(gitArgs, excludePathspecArgs()...)
	cmd := coCommitGitCommand(c.ctx, c.repoPath, gitArgs)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show --numstat failed: %w", err)
	}

	stats, err := parseNumstat(out)
	if err != nil {
		return nil, fmt.Errorf("parsing git show --numstat: %w", err)
	}
	return stats, nil
}

// extractNewPath handles git's rename notation in numstat output
func extractNewPath(path string) string {
	// Handle "{prefix/}{old => new}{/suffix}" format
	if strings.Contains(path, "{") {
		// Complex case: "pkg/{old => new}/file.go"
		path = renamePattern.ReplaceAllString(path, "$1")
		// Fix potential double slashes if a segment was removed (e.g. "{old => }")
		return strings.ReplaceAll(path, "//", "/")
	}

	// Simple case: "old => new"
	if idx := strings.Index(path, " => "); idx != -1 {
		return path[idx+4:]
	}

	return path
}

// calculateConfidence computes the confidence score for a co-commit correlation
func (c *CoCommitExtractor) calculateConfidence(event BeadEvent, files []FileChange) float64 {
	// Base confidence for co-committed files
	confidence := 0.95

	// Bonus: commit message mentions bead ID
	if containsBeadID(event.CommitMsg, event.BeadID) {
		confidence += 0.04
	}

	// Penalty: shotgun commit (>20 files)
	if len(files) > 20 {
		confidence -= 0.10
	}

	// Penalty: only test files
	if allTestFiles(files) {
		confidence -= 0.05
	}

	// Clamp to [0, 1]
	if confidence > 1.0 {
		confidence = 1.0
	}
	if confidence < 0.0 {
		confidence = 0.0
	}

	return confidence
}

// generateReason creates a human-readable explanation for the correlation
func (c *CoCommitExtractor) generateReason(event BeadEvent, files []FileChange, confidence float64) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Co-committed with bead status change to %s", event.EventType))

	if containsBeadID(event.CommitMsg, event.BeadID) {
		parts = append(parts, "commit message references bead ID")
	}

	if len(files) > 20 {
		parts = append(parts, fmt.Sprintf("large commit (%d files)", len(files)))
	}

	if allTestFiles(files) {
		parts = append(parts, "contains only test files")
	}

	return strings.Join(parts, "; ")
}

// isCodeFile checks if a file path is a code file based on extension
func isCodeFile(path string) bool {
	// Handle git quoting (e.g. "path/with spaces.go")
	if len(path) > 2 && path[0] == '"' && path[len(path)-1] == '"' {
		// Basic unquote: strip quotes.
		// Git might use C-style escapes (e.g. \t, \n, \"), but for extension checking
		// simply stripping the surrounding quotes handles the common case of spaces.
		// For complex escapes, we accept that filepath.Ext might be imperfect,
		// but this covers 99% of "filename with space.go" cases.
		path = path[1 : len(path)-1]
	}

	ext := strings.ToLower(filepath.Ext(path))
	return codeFileExtensions[ext]
}

// isExcludedPath checks if a path should be excluded
func isExcludedPath(path string) bool {
	// Check for direct prefix (fast path for root dirs)
	for _, prefix := range excludedPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	// Check for nested directories (e.g. src/node_modules/...)
	// We look for "/dirname/" in the path
	for _, prefix := range excludedPaths {
		// Only check directory exclusions (ending in /)
		if strings.HasSuffix(prefix, "/") {
			// Check for "/prefix" anywhere in path
			// We prepend / to ensure we match a directory boundary
			if strings.Contains(path, "/"+prefix) {
				return true
			}
		}
	}
	return false
}

// containsBeadID reports an exact, case-insensitive bead-ID token. A raw
// substring match would treat bv-42 as an explicit reference inside bv-420 and
// incorrectly increase the correlation confidence.
func containsBeadID(text, beadID string) bool {
	if beadID == "" {
		return false
	}
	lowerText := strings.ToLower(text)
	lowerID := strings.ToLower(beadID)
	for searchFrom := 0; searchFrom <= len(lowerText)-len(lowerID); {
		relative := strings.Index(lowerText[searchFrom:], lowerID)
		if relative < 0 {
			return false
		}
		start := searchFrom + relative
		end := start + len(lowerID)

		beforeIsID := false
		if start > 0 {
			r, _ := utf8.DecodeLastRuneInString(lowerText[:start])
			beforeIsID = isBeadIDRune(r)
		}
		afterIsID := false
		if end < len(lowerText) {
			r, _ := utf8.DecodeRuneInString(lowerText[end:])
			afterIsID = isBeadIDRune(r)
		}
		if !beforeIsID && !afterIsID {
			return true
		}
		searchFrom = start + 1
	}
	return false
}

func isBeadIDRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_' || r == '.'
}

// allTestFiles returns true if all files are test files
func allTestFiles(files []FileChange) bool {
	if len(files) == 0 {
		return false
	}

	testPatterns := []string{"_test.go", ".test.js", ".test.ts", ".spec.js", ".spec.ts", "_test.py", "test_"}

	for _, f := range files {
		isTest := false
		lowerPath := strings.ToLower(f.Path)
		for _, pattern := range testPatterns {
			if strings.Contains(lowerPath, pattern) {
				isTest = true
				break
			}
		}
		if !isTest {
			return false
		}
	}
	return true
}

// shortSHA returns the first 7 characters of a SHA
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ExtractAllCoCommits extracts co-committed files for all events with status changes
func (c *CoCommitExtractor) ExtractAllCoCommits(events []BeadEvent) ([]CorrelatedCommit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var commits []CorrelatedCommit
	fileCache := make(map[string][]FileChange) // Cache file lookups by SHA

	// Batch all relevant commit SHAs through a single pair of `git log` calls so
	// the per-event ExtractCoCommittedFiles below reads from memory instead of
	// forking two `git show` processes per commit (#161). Collect the same SHAs
	// the loop will actually request (status-change events only).
	batchSHAs := make([]string, 0, len(events))
	for _, event := range events {
		if event.EventType != EventClaimed && event.EventType != EventClosed {
			continue
		}
		batchSHAs = append(batchSHAs, event.CommitSHA)
	}
	if err := c.primeBatch(batchSHAs); err != nil {
		return nil, fmt.Errorf("priming co-commit batch: %w", err)
	}
	if c.memoizedHistoryState != coCommitHistoryStateFull {
		// Unsafe-state memoization is useful only inside this one extraction.
		// Discard it before a future invocation can observe a moved shallow
		// boundary with the same commit SHA.
		defer c.resetMemoizedDiffs()
	}

	for _, event := range events {
		// Only process status change events
		if event.EventType != EventClaimed && event.EventType != EventClosed {
			continue
		}

		// Use cached files if available, otherwise fetch from git
		files, cached := fileCache[event.CommitSHA]
		if !cached {
			var err error
			files, err = c.extractCoCommittedFiles(event)
			if err != nil {
				return nil, fmt.Errorf("extracting co-committed files for %s: %w", event.CommitSHA, err)
			}
			fileCache[event.CommitSHA] = files
		}

		// Only create correlation if there are code files
		if len(files) == 0 {
			continue
		}

		commit := c.CreateCorrelatedCommit(event, files)
		commits = append(commits, commit)
	}

	return commits, nil
}
