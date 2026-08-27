package correlation

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/maphash"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// blobsReadCounter counts the total number of blob object ids passed to
// readBlobs across the process. It exists solely so tests can prove the
// incremental per-commit cache reads only the NEW commits' blobs (and ~0 when
// nothing is new). It is never read on any production path.
var blobsReadCounter int64

// isNullObjectID reports whether oid is Git's all-zero SHA-1 or SHA-256 object
// ID, used in `--raw` output for a missing side of an add/delete.
func isNullObjectID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for i := range len(oid) {
		if oid[i] != '0' {
			return false
		}
	}
	return true
}

// snapshotCommit holds the metadata and the followed file's old/new blob object
// ids for a single commit that modified the followed beads file.
type snapshotCommit struct {
	info commitInfo
	// oldSHA is the followed file's blob in the commit's first parent, "" when the
	// file did not exist in the parent (an addition).
	oldSHA string
	// newSHA is the followed file's blob at this commit.
	newSHA string
}

// extractViaSnapshots reconstructs bead lifecycle events without asking git to
// produce a textual patch (`-p`) of the followed beads blob.
//
// Rationale (#160 follow-up / #161 / pass-3): `git log -p --follow --
// <beads.jsonl>` runs git's Myers diff over the entire multi-MB JSONL blob at
// every commit and then *streams the full patch text* (megabytes of +/- record
// lines) back to us. Measured on this repo (1.9 MB blob, 200 commits, warm
// cache) that subprocess alone is ~720 ms. The parser, however, only needs the
// set of added/removed `+{...}`/`-{...}` JSONL *record lines* per commit. Because
// the beads exporter writes one whole JSON record per line, that set is exactly
// the per-commit line-level set difference between the file's blob and its
// parent's blob.
//
// So instead we:
//
//  1. run a metadata-only `git log --raw --follow` (~20 ms) that yields, per
//     commit, the header plus the followed file's old+new blob object ids
//     directly — git's own rename-following picks the correct parent blob, so we
//     never have to resolve SHA^:path ourselves (the source of the earlier
//     parent-diff subtlety);
//  2. read each *unique* blob exactly once through a single streaming
//     `git cat-file --batch` (consecutive commits share their boundary blob, so
//     the 2xN referenced blobs collapse to ~N unique reads — ~half the I/O the
//     old SHA:path/SHA^:path scheme paid);
//  3. hash each blob's record lines once into a 64-bit-keyed multiset
//     (recordLineSetHashed, ~4x faster than a full-string-keyed map), and emit
//     the synthesized per-commit `+{...}`/`-{...}` diff for the *unchanged*
//     parseDiff, so event semantics (created/claimed/closed/reopened/modified)
//     are byte-identical to the `-p` path (proven by the differential test and
//     the golden artifacts).
//
// Net effect on this repo: ~720 ms git + ~270 ms parse on the `-p` path drops to
// ~310 ms git (raw log + dedup cat-file) + ~150 ms hash/diff. Since/Until/Limit
// and rename-following are honored by the same `--raw --follow` walk.
func (e *Extractor) extractViaSnapshots(opts ExtractOptions) ([]BeadEvent, error) {
	commits, err := e.snapshotCommits(opts)
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, nil
	}

	// INCREMENTAL per-commit cache: each commit's events are an immutable pure
	// function of its (oldSHA,newSHA) blob pair + commit metadata + BeadID filter
	// (see per_commit_event_cache.go). Load the cached contributions for this
	// (file,BeadID) namespace; reuse them for already-seen commits so a HEAD
	// advance only reads + diffs the NEW commits' blobs instead of all ~200.
	namespace := perCommitEventCacheNamespace(e.primaryBeadsFile(), opts.BeadID)
	cached := loadPerCommitEvents(namespace)

	// Per-commit events in git-log (newest-first) order; nil means "must compute".
	perCommitEvents := make([][]BeadEvent, len(commits))
	fresh := make(map[string]perCommitEventEntry)

	// Determine which commits must be computed and, for every blob OID an uncached
	// commit references, the LAST commit index (in git-log order) that still needs
	// it. Streaming the blobs in commit order and dropping each record-line set as
	// soon as its last user is processed bounds simultaneous blob residency to the
	// handful of blobs live across the current window — instead of holding EVERY
	// historical blob at once. On a full-rewrite JSONL history (br repos rewrite
	// the whole file each mutation) this is the difference between an
	// O(min(commits,500) x file_size) peak (multi-GiB RSS) and O(window x
	// file_size) (#182). Consecutive commits in the followed chain share their
	// boundary blob (newSHA[i] == oldSHA[i-1]), so the window is ~2-3 sets in
	// practice; the last-use map keeps residency correct even if that ever fails.
	// Cached commits contribute zero blob reads.
	lastUse := make(map[string]int, len(commits)+1)
	needsSnapshots := false
	noteUse := func(sha string, i int) {
		if sha == "" {
			return
		}
		if j, ok := lastUse[sha]; !ok || i > j {
			lastUse[sha] = i
		}
	}
	for i := range commits {
		c := commits[i]
		// A cache hit is valid only if the stored blob pair still matches the
		// followed file's parent/child OIDs for this commit (re-validation guards
		// against any rename-following anomaly; in practice always matches).
		if ce, ok := cached[c.info.SHA]; ok && ce.OldSHA == c.oldSHA && ce.NewSHA == c.newSHA && ce.Events != nil {
			perCommitEvents[i] = ce.Events
			continue
		}
		needsSnapshots = true
		noteUse(c.oldSHA, i)
		noteUse(c.newSHA, i)
	}
	composeEvents := func() []BeadEvent {
		var events []BeadEvent
		for i := range commits {
			events = append(events, perCommitEvents[i]...)
		}
		// snapshotCommits returns newest-first (git log order); match the
		// Extract contract, which returns chronological events.
		reverseEvents(events)
		return events
	}
	if !needsSnapshots {
		// A pure cache hit has already been revalidated against the live blob
		// pairs. Avoid an otherwise-unused cat-file subprocess: it adds latency
		// and could turn valid cached history into a process-startup failure.
		return composeEvents(), nil
	}

	// Streaming blob reader: a single long-lived `git cat-file --batch` fed one
	// OID at a time. Each unique uncached blob is read exactly once (guarded by
	// the live `sets` window plus the last-use eviction below), so total blob
	// reads equal the deduped uncached count — the incremental cache still reads
	// only the NEW commits' blobs (~0 when nothing is new), matching the previous
	// readBlobs behavior for the blobsReadCounter tests.
	reader, err := e.openSnapshotBlobReader()
	if err != nil {
		return nil, err
	}
	readerOpen := true
	defer func() {
		if readerOpen {
			_ = reader.Close()
		}
	}()

	// snapshots is the live window of record-line multisets, keyed by blob OID.
	// Each entry retains both the blob bytes its line slices alias and ordered
	// per-record descriptors. The descriptors let the paired older snapshot reuse
	// hashes for byte-identical prefix/suffix records without changing target-order
	// aggregation semantics. At last-use eviction the snapshot becomes unreachable
	// before its buffer is handed back to the reader, so live slices are never
	// overwritten.
	snapshots := make(map[string]recordLineSnapshot)
	getSnapshot := func(sha string, reference *recordLineSnapshot) (recordLineSnapshot, error) {
		if sha == "" {
			return recordLineSnapshot{}, nil
		}
		if snapshot, ok := snapshots[sha]; ok {
			return snapshot, nil
		}
		blob, err := reader.read(sha)
		if err != nil {
			return recordLineSnapshot{}, err
		}
		if blob == nil {
			return recordLineSnapshot{}, fmt.Errorf("cat-file returned no content for nonempty blob object ID %q", sha)
		}
		snapshot, _ := buildRecordLineSnapshot(blob, reference, hashRecordLine)
		snapshots[sha] = snapshot
		return snapshot, nil
	}

	// Compute the uncached commits' contributions in git-log order, evicting each
	// blob's set as soon as no later commit references it.
	for i := range commits {
		if perCommitEvents[i] != nil {
			continue // served from cache
		}
		c := commits[i]
		newSnapshot, err := getSnapshot(c.newSHA, nil)
		if err != nil {
			return nil, err
		}
		// Git log is newest-first, so a commit's new snapshot is normally already
		// live as the previous iteration's old boundary. Build the paired older
		// target against those actual live bytes; when no usable reference exists,
		// buildRecordLineSnapshot hashes the entire target.
		oldSnapshot, err := getSnapshot(c.oldSHA, &newSnapshot)
		if err != nil {
			return nil, err
		}
		var evs []BeadEvent
		diffText := synthesizeRecordDiff(oldSnapshot.lines, newSnapshot.lines)
		if len(diffText) > 0 {
			evs = e.parseDiff(diffText, c.info, opts.BeadID)
		}
		// Store an empty-but-non-nil slice so the compose loop and the cache both
		// treat "computed, no events" as a definitive result (never re-read).
		if evs == nil {
			evs = []BeadEvent{}
		}
		perCommitEvents[i] = evs
		fresh[c.info.SHA] = perCommitEventEntry{
			CreatedAt: time.Now().UTC(),
			OldSHA:    c.oldSHA,
			NewSHA:    c.newSHA,
			Events:    evs,
		}
		// Drop every blob whose last referencing commit is at or before i: it can
		// never be needed again (lastUse indices are all uncached-commit indices,
		// so this iteration is the last chance to free them), and its bytes — plus
		// the record-line set aliasing them — are released now rather than at end.
		for sha, live := range snapshots {
			if lastUse[sha] <= i {
				delete(snapshots, sha)
				reader.recycle(live.blob)
			}
		}
	}

	// Verify the long-lived cat-file process completed successfully before any
	// freshly derived contribution is published. In particular, cancellation can
	// arrive after the final response was read but before git exits; treating an
	// ignored Wait error as success would violate cancellation and seed a cache
	// with results from an unverified subprocess.
	closeErr := reader.Close()
	readerOpen = false
	if closeErr != nil {
		return nil, fmt.Errorf("closing blob reader: %w", closeErr)
	}

	// Persist only the freshly computed commits (pure hits are never re-written,
	// preserving the no-rewrite-on-pure-hit discipline when nothing is new).
	storePerCommitEvents(namespace, fresh)

	// Compose the full event slice in the same order as a cold extraction.
	return composeEvents(), nil
}

// snapshotCommits returns, newest-first, the commits that touched the followed
// beads file together with the followed file's old (parent) and new blob object
// ids. It uses a metadata-only `git log --raw --follow` (no `-p`): git's `--raw`
// output already carries both blob ids per change, and `--follow` makes git pick
// the correct parent blob across renames.
func (e *Extractor) snapshotCommits(opts ExtractOptions) ([]snapshotCommit, error) {
	primary := e.primaryBeadsFile()

	args := []string{
		"--raw",
		"--no-abbrev", // full SHA-1/SHA-256 blob ids so cat-file resolves them directly
		"--follow",
		"--format=" + gitLogHeaderFormat,
	}
	args = append(args, lifecycleHistoryOrderArgs()...)
	args = appendHistoryFilters(args, opts)
	args = append(args, "--", primary)

	cmd := lifecycleGitLogCommand(e.ctx, e.repoPath, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log --raw failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log --raw failed: %w", err)
	}

	return parseSnapshotLog(out)
}

// appendHistoryFilters appends --since/--until/-n filters (the same ones the
// legacy buildGitLogArgs honored) before the pathspec separator.
func appendHistoryFilters(args []string, opts ExtractOptions) []string {
	if opts.Since != nil {
		args = append(args, "--since="+opts.Since.Format(time.RFC3339))
	}
	if opts.Until != nil {
		args = append(args, "--until="+opts.Until.Format(time.RFC3339))
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", opts.Limit))
	}
	return args
}

// parseSnapshotLog parses the output of `git log --raw --no-abbrev --follow
// --format=<header>`.
//
// Per commit the stream is: a header line (NUL-separated %H..%s, terminated by
// '\n'), a blank line, then one or more `--raw` diff lines such as
//
//	:100644 100644 <oldsha> <newsha> M\t<path>
//	:000000 100644 <zero>   <newsha> A\t<path>
//	:100644 100644 <oldsha> <newsha> R100\t<oldpath>\t<newpath>
//
// We split the stream on the commit-header marker (commitPattern), reusing the
// same boundary detection as the streaming patch parser, then read the `--raw`
// line that follows each header. With --follow against a single pathspec there is
// exactly one diff entry per commit; we take the first usable one.
func parseSnapshotLog(out []byte) ([]snapshotCommit, error) {
	var commits []snapshotCommit
	objectIDWidth := 0

	locs := commitPattern.FindAllIndex(out, -1)
	if len(locs) == 0 && len(bytes.TrimSpace(out)) != 0 {
		return nil, fmt.Errorf("snapshot log contains no canonical commit headers")
	}
	if len(locs) > 0 && len(bytes.TrimSpace(out[:locs[0][0]])) != 0 {
		return nil, fmt.Errorf("snapshot log contains data before its first commit header")
	}
	for i, loc := range locs {
		start := loc[0]
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := out[start:end]

		// The header line ends at the first '\n'; bytes before it are the
		// NUL-delimited %H..%s header.
		nl := bytes.IndexByte(chunk, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("unterminated snapshot commit header")
		}
		info, err := parseCommitInfo(string(chunk[:nl]))
		if err != nil {
			return nil, fmt.Errorf("parsing snapshot commit header: %w", err)
		}
		if objectIDWidth == 0 {
			objectIDWidth = len(info.SHA)
		} else if len(info.SHA) != objectIDWidth {
			return nil, fmt.Errorf("mixed-width snapshot commit object IDs: got %d and %d characters", objectIDWidth, len(info.SHA))
		}

		sc := snapshotCommit{info: info}
		usable, err := parseRawDiffLines(chunk[nl+1:], &sc)
		if err != nil {
			return nil, fmt.Errorf("parsing raw diff for commit %s: %w", info.SHA, err)
		}
		if usable {
			commits = append(commits, sc)
		}
	}

	return commits, nil
}

// parseRawDiffLines reads the `--raw` diff line(s) for one commit and fills sc
// with the followed file's old/new blob object ids. Returns false when no usable
// raw entry is present. A raw line looks like:
//
//	:<oldmode> <newmode> <oldsha> <newsha> <status>\t<path>[\t<newpath>]
func parseRawDiffLines(payload []byte, sc *snapshotCommit) (bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64*1024), gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return false, fmt.Errorf("malformed commit header inside raw diff payload")
		}
		if len(line) == 0 || line[0] != ':' {
			continue
		}
		// The leading metadata (everything before the first TAB) is
		// space-separated: ":<oldmode> <newmode> <oldsha> <newsha> <status>".
		tab := strings.IndexByte(line, '\t')
		meta := line
		if tab >= 0 {
			meta = line[:tab]
		}
		fields := strings.Fields(meta)
		if len(fields) < 5 {
			return false, fmt.Errorf("malformed raw diff metadata %q", meta)
		}
		oldSHA := fields[2]
		newSHA := fields[3]
		objectIDWidth := len(sc.info.SHA)
		if !isCanonicalCommitSHA(oldSHA) || !isCanonicalCommitSHA(newSHA) || len(oldSHA) != objectIDWidth || len(newSHA) != objectIDWidth {
			return false, fmt.Errorf("invalid or mixed-width blob object IDs %q and %q for %d-character commit ID", oldSHA, newSHA, objectIDWidth)
		}
		if !isNullObjectID(oldSHA) {
			sc.oldSHA = oldSHA
		}
		if !isNullObjectID(newSHA) {
			sc.newSHA = newSHA
		}
		// We only act on commits where the followed file has a current blob; a
		// pure deletion (new == zero) carries no `+{...}` records to extract and
		// the legacy `-p` path likewise produced no events for it.
		return sc.newSHA != "", nil
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("scanning raw diff: %w", err)
	}
	return false, nil
}

// readBlobs reads the requested blob object ids via a single
// `git cat-file --batch` process and returns their contents keyed by object id.
// Object ids must already be unique. Every requested ID came from a nonzero
// raw-log side, so a missing response is repository corruption/unavailability,
// not an empty snapshot, and fails the whole batch.
//
// NOTE: extractViaSnapshots no longer uses this — it streams via blobReader to
// bound peak memory (#182), since holding every historical blob in one map is
// exactly what made the export path spike to multi-GiB RSS. readBlobs is kept as
// the batch primitive (identical cat-file framing) for callers that genuinely
// need all blobs resident at once.
func (e *Extractor) readBlobs(ids []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	for _, id := range ids {
		if !isCanonicalCommitSHA(id) {
			return nil, fmt.Errorf("invalid cat-file blob object ID %q", id)
		}
	}
	atomic.AddInt64(&blobsReadCounter, int64(len(ids)))

	// Sort for deterministic request ordering (purely cosmetic; output is keyed
	// by id so order does not affect correctness).
	sort.Strings(ids)

	cmd := repoGitCommand(e.ctx, e.repoPath, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting git cat-file: %w", err)
	}

	// Writer goroutine: feed all ids then close stdin.
	writeErr := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, s := range ids {
			if _, err := w.WriteString(s + "\n"); err != nil {
				writeErr <- err
				_ = stdin.Close()
				return
			}
		}
		flushErr := w.Flush()
		closeErr := stdin.Close()
		if flushErr != nil {
			writeErr <- flushErr
			return
		}
		writeErr <- closeErr
	}()

	reader := bufio.NewReaderSize(stdout, gitLogMaxScanTokenSize)
	parseErr := func() error {
		for _, s := range ids {
			// Response header: "<sha> <type> <size>\n" or "<id> missing\n".
			header, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("reading cat-file header for %q: %w", s, err)
			}
			size, missing, err := parseCatFileBatchHeader(s, header, 0)
			if err != nil {
				return err
			}
			if missing {
				return fmt.Errorf("cat-file reported nonempty blob object ID %q as missing", s)
			}
			content := make([]byte, size)
			if _, err := readFull(reader, content); err != nil {
				return fmt.Errorf("reading cat-file content for %q: %w", s, err)
			}
			if err := readCatFileBatchTrailer(reader, s); err != nil {
				return err
			}
			result[s] = content
		}
		return nil
	}()

	if parseErr != nil {
		// We stopped reading stdout mid-stream. If git's stdout pipe has filled,
		// git blocks writing → stops reading stdin → the writer goroutine blocks
		// on Flush/WriteString, so a plain `<-writeErr` here would deadlock on
		// large histories. Kill git first (mirroring the legacy `git log -p`
		// path's Process.Kill on parse error): that breaks the stdin pipe, the
		// writer returns, and Wait completes. Then surface the parse error.
		_ = cmd.Process.Kill()
		<-writeErr
		_ = cmd.Wait()
		return nil, parseErr
	}

	wErr := <-writeErr
	waitErr := cmd.Wait()
	if wErr != nil {
		return nil, fmt.Errorf("writing cat-file ids: %w", wErr)
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git cat-file failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git cat-file failed: %w", waitErr)
	}
	return result, nil
}

// blobReader streams individual git blobs on demand through a single long-lived
// `git cat-file --batch` process. Unlike readBlobs — which loads every requested
// blob into one map up front and holds them all — it lets extractViaSnapshots
// read a blob, hash it into a record-line set, and drop it before reading the
// next, bounding peak memory to the live window rather than the whole history
// (#182). Requests and responses are strictly interleaved (one OID written and
// flushed, its single bounded response drained immediately), so we never queue
// more than one response's worth of output and the pipe cannot fill — there is
// no writer/reader deadlock of the kind readBlobs' concurrent-writer path guards
// against.
type blobReader struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	w           *bufio.Writer
	out         *bufio.Reader
	spare       []byte
	arenaRunway int
}

type snapshotBlobReadCloser interface {
	// read returns a non-nil slice for every present blob, including a
	// zero-byte blob. Nil without an error violates the snapshot contract.
	read(string) ([]byte, error)
	recycle([]byte)
	Close() error
}

const (
	// blobReaderBufferSize is deliberately small enough that large blob payloads
	// bypass bufio.Reader's transport buffer and read directly into their owned
	// arena. The capacity removed from the old 10 MiB transport buffer is moved,
	// byte-for-byte, to the first valid payload arena below. That preserves the
	// heap-goal runway which reduced GC frequency while avoiding an extra copy of
	// each multi-megabyte blob through bufio's buffer.
	blobReaderBufferSize  = 64 * 1024
	blobReaderArenaRunway = gitLogMaxScanTokenSize - blobReaderBufferSize
)

// newBlobReader starts the streaming cat-file process for the extractor's repo.
func (e *Extractor) newBlobReader() (*blobReader, error) {
	cmd := repoGitCommand(e.ctx, e.repoPath, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git cat-file stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting git cat-file: %w", err)
	}
	return &blobReader{
		cmd:         cmd,
		stdin:       stdin,
		w:           bufio.NewWriter(stdin),
		out:         bufio.NewReaderSize(stdout, blobReaderBufferSize),
		arenaRunway: blobReaderArenaRunway,
	}, nil
}

func (e *Extractor) openSnapshotBlobReader() (snapshotBlobReadCloser, error) {
	if e.blobReaderFactory != nil {
		return e.blobReaderFactory()
	}
	return e.newBlobReader()
}

// read returns the content of one blob object ID. A missing response is an
// error: intentional absence is represented by an empty old/new SHA before this
// layer. Each call is counted in blobsReadCounter so the incremental-cache tests
// can prove only the NEW commits' blobs are read.
func (b *blobReader) read(sha string) ([]byte, error) {
	if !isCanonicalCommitSHA(sha) {
		return nil, fmt.Errorf("invalid cat-file blob object ID %q", sha)
	}
	atomic.AddInt64(&blobsReadCounter, 1)
	if _, err := b.w.WriteString(sha + "\n"); err != nil {
		return nil, fmt.Errorf("writing cat-file id %q: %w", sha, err)
	}
	if err := b.w.Flush(); err != nil {
		return nil, fmt.Errorf("flushing cat-file id %q: %w", sha, err)
	}
	header, err := b.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("reading cat-file header for %q: %w", sha, err)
	}
	size, missing, err := parseCatFileBatchHeader(sha, header, b.arenaRunway)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, fmt.Errorf("cat-file reported nonempty blob object ID %q as missing", sha)
	}
	// A missing response returns above without consuming arenaRunway. Move the
	// capacity removed from the transport buffer to exactly the first real blob
	// allocation; that arena then remains in the existing one-spare recycle
	// lifecycle. Limiting the full-slice capacity also makes the byte-for-byte
	// capacity invariant explicit when a sufficiently large spare is supplied.
	runway := b.arenaRunway
	capacity := size + runway
	content := b.spare
	b.spare = nil
	if cap(content) < capacity {
		content = make([]byte, size, capacity)
	} else if runway > 0 {
		content = content[:size:capacity]
	} else if content == nil {
		// Preserve nil as the reader-contract sentinel for "no blob returned".
		// A legitimate zero-byte blob is present and must therefore use a
		// non-nil empty slice even after the one-time arena runway is consumed.
		content = make([]byte, 0)
	} else {
		content = content[:size]
	}
	b.arenaRunway = 0
	if _, err := readFull(b.out, content); err != nil {
		return nil, fmt.Errorf("reading cat-file content for %q: %w", sha, err)
	}
	if err := readCatFileBatchTrailer(b.out, sha); err != nil {
		return nil, err
	}
	return content, nil
}

// parseCatFileBatchHeader validates one response from `git cat-file --batch`.
// Binding the echoed object ID to the request is essential: accepting a
// well-shaped response for a different request would silently attribute the
// wrong historical snapshot to a commit. Exact type, unsigned size, and runway
// checks keep malformed or desynchronized output from reaching an allocation.
func parseCatFileBatchHeader(requested, header string, arenaRunway int) (size int, missing bool, err error) {
	if !isCanonicalCommitSHA(requested) {
		return 0, false, fmt.Errorf("invalid cat-file blob object ID %q", requested)
	}
	if arenaRunway < 0 {
		return 0, false, fmt.Errorf("invalid cat-file arena runway %d for %q", arenaRunway, requested)
	}
	if !strings.HasSuffix(header, "\n") {
		return 0, false, fmt.Errorf("unterminated cat-file header %q for %q", header, requested)
	}
	trimmed := strings.TrimSuffix(header, "\n")
	parts := strings.Split(trimmed, " ")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, false, fmt.Errorf("unexpected cat-file header %q for %q", trimmed, requested)
	}
	if parts[0] != requested {
		return 0, false, fmt.Errorf("cat-file response object ID %q does not match request %q", parts[0], requested)
	}
	if len(parts) == 2 {
		if parts[1] != "missing" {
			return 0, false, fmt.Errorf("unexpected cat-file header %q for %q", trimmed, requested)
		}
		return 0, true, nil
	}
	if parts[1] != "blob" {
		return 0, false, fmt.Errorf("cat-file response for %q has object type %q, want blob", requested, parts[1])
	}
	if parts[2] == "" {
		return 0, false, fmt.Errorf("empty cat-file size for %q", requested)
	}
	for i := range len(parts[2]) {
		if parts[2][i] < '0' || parts[2][i] > '9' {
			return 0, false, fmt.Errorf("invalid cat-file size %q for %q", parts[2], requested)
		}
	}
	size64, parseErr := strconv.ParseUint(parts[2], 10, 64)
	if parseErr != nil {
		return 0, false, fmt.Errorf("parsing cat-file size %q for %q: %w", parts[2], requested, parseErr)
	}
	maxInt := int(^uint(0) >> 1)
	if size64 > uint64(maxInt-arenaRunway) {
		return 0, false, fmt.Errorf("cat-file size %q for %q exceeds safe allocation limit with runway %d", parts[2], requested, arenaRunway)
	}
	return int(size64), false, nil
}

func readCatFileBatchTrailer(reader *bufio.Reader, requested string) error {
	trailer, err := reader.ReadByte()
	if err != nil {
		return fmt.Errorf("reading cat-file trailer for %q: %w", requested, err)
	}
	if trailer != '\n' {
		return fmt.Errorf("invalid cat-file trailer %q for %q, want newline", trailer, requested)
	}
	return nil
}

// recycle retains at most one no-longer-live blob buffer for the next read.
// Callers must first remove every recordLineSet whose entries alias content.
// Keeping only the largest returned capacity bounds idle retained memory to one
// blob while avoiding allocation and zeroing for the usual rewrite history.
func (b *blobReader) recycle(content []byte) {
	if cap(content) > cap(b.spare) {
		b.spare = content[:0]
	}
}

// Close flushes and closes stdin (signalling EOF to git) then waits for exit. It
// first drains any remaining stdout so that a git blocked mid-write on a full
// pipe (possible only if read errored partway through a large object) is
// unblocked and can exit cleanly rather than leaving Wait to hang. Safe to defer
// even after a read error.
func (b *blobReader) Close() error {
	_ = b.w.Flush()
	closeErr := b.stdin.Close()
	_, _ = io.Copy(io.Discard, b.out)
	waitErr := b.cmd.Wait()
	if waitErr != nil {
		return waitErr
	}
	return closeErr
}

// readFull reads exactly len(buf) bytes from r.
func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// recordLineSetSeed gives the per-process maphash a stable seed so the same line
// hashes consistently within a single extraction (the only place these hashes are
// compared). It is never persisted, so cross-process determinism is unnecessary.
var recordLineSetSeed = maphash.MakeSeed()

// hashRecordLine is the production record-line hasher. Passing the hasher into
// buildRecordLineSnapshot gives tests a deterministic collision seam without a
// mutable package-global hook.
func hashRecordLine(line []byte) uint64 {
	return maphash.Bytes(recordLineSetSeed, line)
}

// recordLineDescriptor locates one eligible JSON record in its owning blob and
// carries the digest used by recordLineSet. start:end excludes the line-feed but
// deliberately includes a preceding carriage return, matching the old scanner.
type recordLineDescriptor struct {
	start int
	end   int
	hash  uint64
}

// recordLineSnapshot is the complete live index for one blob. Both entry text
// and descriptors refer only to blob. indexed distinguishes a valid empty index
// from a cache/reference hole, for which frontier reuse must fall back to full
// hashing.
type recordLineSnapshot struct {
	lines   recordLineSet
	records []recordLineDescriptor
	blob    []byte
	indexed bool
}

// recordLineBuildStats makes the frontier's work directly observable in tests
// and benchmarks without affecting production decisions.
type recordLineBuildStats struct {
	hashedRecords int
	hashedBytes   int
	reusedRecords int
	reusedBytes   int
}

// recordLineEntry is one exact record in a recordLineSet: how many times the
// record line occurs in the blob, plus a representative copy of the line bytes
// (needed to emit the synthesized diff for changed records). next links the rare
// records with the same 64-bit digest; exact bytes, never the digest alone,
// determine line identity.
type recordLineEntry struct {
	count int
	text  []byte
	next  *recordLineEntry
}

// recordLineSet is the multiset of a blob's loader-eligible JSON record lines,
// including their leading whitespace and an optional first-line UTF-8 BOM,
// indexed by a 64-bit hash of the full physical line. Beads writes one whole
// JSON record per line, so identity of a record is exact line identity. The hash
// is only a fast bucket selector: colliding lines remain separate linked
// entries. This avoids full multi-KB string map keys without treating a digest
// collision as an unchanged lifecycle record.
type recordLineSet map[uint64]*recordLineEntry

// newRecordLineSet builds the record-line multiset for a blob. Non-record lines
// are ignored, matching the loader's JSON-object eligibility after its exact
// first-line BOM and JSON-whitespace normalization. The returned entries retain
// the full physical line and alias into blob, which the caller retains for the
// duration of the extraction.
func newRecordLineSet(blob []byte) recordLineSet {
	snapshot, _ := buildRecordLineSnapshot(blob, nil, hashRecordLine)
	return snapshot.lines
}

// buildRecordLineSnapshot indexes target. When reference is a complete live
// index, byte-identical record prefixes and suffixes reuse its per-record hashes;
// the unmatched middle is hashed normally. Prefix and suffix never overlap in
// either side. Missing, stale, or incomplete references take the full-hash path.
//
// Hash reuse is only an indexing shortcut. Every target descriptor is aggregated
// afterward in target order, and every representative aliases target, so even a
// deliberately colliding hasher produces exactly the same counts and first
// representative as a full target build.
func buildRecordLineSnapshot(
	target []byte,
	reference *recordLineSnapshot,
	hashLine func([]byte) uint64,
) (recordLineSnapshot, recordLineBuildStats) {
	records := scanRecordLineDescriptors(target)
	stats := recordLineBuildStats{}

	prefix := 0
	suffix := 0
	if reference != nil && reference.indexed && reference.blob != nil {
		limit := min(len(records), len(reference.records))
		for prefix < limit {
			targetRecord := records[prefix]
			referenceRecord := reference.records[prefix]
			targetLine := target[targetRecord.start:targetRecord.end]
			referenceLine := reference.blob[referenceRecord.start:referenceRecord.end]
			if !bytes.Equal(targetLine, referenceLine) {
				break
			}
			records[prefix].hash = referenceRecord.hash
			stats.reusedRecords++
			stats.reusedBytes += len(targetLine)
			prefix++
		}
		for suffix < limit-prefix {
			targetIndex := len(records) - 1 - suffix
			referenceIndex := len(reference.records) - 1 - suffix
			targetRecord := records[targetIndex]
			referenceRecord := reference.records[referenceIndex]
			targetLine := target[targetRecord.start:targetRecord.end]
			referenceLine := reference.blob[referenceRecord.start:referenceRecord.end]
			if !bytes.Equal(targetLine, referenceLine) {
				break
			}
			records[targetIndex].hash = referenceRecord.hash
			stats.reusedRecords++
			stats.reusedBytes += len(targetLine)
			suffix++
		}
	}

	for i := prefix; i < len(records)-suffix; i++ {
		record := records[i]
		line := target[record.start:record.end]
		records[i].hash = hashLine(line)
		stats.hashedRecords++
		stats.hashedBytes += len(line)
	}

	set := make(recordLineSet, len(records))
	for _, record := range records {
		line := target[record.start:record.end]
		var previous *recordLineEntry
		for entry := set[record.hash]; entry != nil; entry = entry.next {
			if bytes.Equal(entry.text, line) {
				entry.count++
				previous = nil
				break
			}
			previous = entry
		}
		if previous != nil {
			previous.next = &recordLineEntry{count: 1, text: line}
		} else if set[record.hash] == nil {
			set[record.hash] = &recordLineEntry{count: 1, text: line}
		}
	}

	return recordLineSnapshot{
		lines:   set,
		records: records,
		blob:    target,
		indexed: true,
	}, stats
}

// scanRecordLineDescriptors records the JSON-object lines accepted by the
// loader: JSON whitespace may precede an object, and a UTF-8 BOM is stripped
// only when it begins the first physical line. Descriptors still retain the full
// physical line so hashing, equality, and synthesized diffs remain byte-exact.
// A final record without a line-feed is included; empty and non-record lines do
// not consume a record ordinal, which makes prefix/suffix comparison match
// recordLineSet semantics rather than physical line numbers.
func scanRecordLineDescriptors(blob []byte) []recordLineDescriptor {
	var records []recordLineDescriptor
	for start := 0; start < len(blob); {
		relativeNewline := bytes.IndexByte(blob[start:], '\n')
		end := len(blob)
		next := len(blob)
		if relativeNewline >= 0 {
			end = start + relativeNewline
			next = end + 1
		}
		if isBeadRecordLine(blob[start:end], start == 0) {
			records = append(records, recordLineDescriptor{start: start, end: end})
		}
		start = next
	}
	return records
}

func isBeadRecordLine(line []byte, firstPhysicalLine bool) bool {
	normalized := line
	if firstPhysicalLine {
		normalized = bytes.TrimPrefix(normalized, []byte{0xEF, 0xBB, 0xBF})
	}
	normalized = bytes.Trim(normalized, " \t\r\n")
	return len(normalized) > 0 && normalized[0] == '{'
}

// synthesizeRecordDiff produces the same +/- physical record lines that a
// zero-context Git patch would emit for one commit, by computing the line-level set
// difference between the parent blob's record set (old) and the commit blob's
// record set (new). A nil oldSet means "no parent" — every record reads as an
// addition. A record present in exactly one side marks it added/removed; a
// modified record appears as the old line removed and the new line added. The
// downstream parser applies the same JSON-whitespace and first-line BOM
// normalization as the loader to every record admitted by this index.
func synthesizeRecordDiff(oldSet, newSet recordLineSet) []byte {
	var buf bytes.Buffer
	// Removed: present in old, absent (or fewer) in new.
	for h, bucket := range oldSet {
		for oe := bucket; oe != nil; oe = oe.next {
			newCount := recordLineCount(newSet[h], oe.text)
			for i := 0; i < oe.count-newCount; i++ {
				buf.WriteByte('-')
				buf.Write(oe.text)
				buf.WriteByte('\n')
			}
		}
	}
	// Added: present in new, absent (or fewer) in old.
	for h, bucket := range newSet {
		for ne := bucket; ne != nil; ne = ne.next {
			oldCount := recordLineCount(oldSet[h], ne.text)
			for i := 0; i < ne.count-oldCount; i++ {
				buf.WriteByte('+')
				buf.Write(ne.text)
				buf.WriteByte('\n')
			}
		}
	}
	return buf.Bytes()
}

func recordLineCount(bucket *recordLineEntry, text []byte) int {
	for entry := bucket; entry != nil; entry = entry.next {
		if bytes.Equal(entry.text, text) {
			return entry.count
		}
	}
	return 0
}
