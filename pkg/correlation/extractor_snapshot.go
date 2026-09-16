package correlation

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/maphash"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// extractCausalHistory follows one first-parent history, retaining the full
// source while evaluating each target state. It deliberately does not apply a
// target -G filter: an unchanged target can become unblocked by another record.
// Only two parsed snapshots are resident; the artifact keeps compact target
// observations, not every issue in every commit.
func (e *Extractor) extractCausalHistory(target string, opts ExtractOptions) (*CausalHistory, error) {
	args := []string{"log", "--first-parent", "--diff-merges=first-parent", "--raw", "--no-abbrev", "--follow", "--format=" + gitLogHeaderFormat + "%x00%cI"}
	args = appendHistoryFilters(args, opts)
	args = append(args, "--", e.primaryBeadsFile())
	cmd := gitCommand(e.ctx, withNoColorGit(args)...)
	cmd.Dir = e.repoPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("causal git log: %w: %s", err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("causal git log: %w", err)
	}
	var commits []snapshotCommit
	var committed []time.Time
	locs := commitPattern.FindAllIndex(out, -1)
	for i, loc := range locs {
		end := len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := out[loc[0]:end]
		nl := bytes.IndexByte(chunk, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("incomplete causal commit header")
		}
		last := bytes.LastIndexByte(chunk[:nl], 0)
		if last < 0 {
			return nil, fmt.Errorf("missing causal committer timestamp")
		}
		info, err := parseCommitInfo(string(chunk[:last]))
		if err != nil {
			return nil, err
		}
		date, err := time.Parse(time.RFC3339, string(chunk[last+1:nl]))
		if err != nil {
			return nil, fmt.Errorf("causal committer timestamp: %w", err)
		}
		c := snapshotCommit{info: info}
		if parseRawDiffLines(chunk[nl+1:], &c) {
			commits = append(commits, c)
			committed = append(committed, date)
		}
	}
	result := &CausalHistory{BeadID: target, Observations: []CausalObservation{}}
	if opts.Revision != "" {
		// The revision may not change the beads file. Its own clocks, rather
		// than the last file change or today's clock, bound an ongoing wait.
		cmd := gitCommand(e.ctx, "show", "-s", "--format=%aI%x00%cI", opts.Revision)
		cmd.Dir = e.repoPath
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("causal reference commit: %w: %s", err, exitErr.Stderr)
			}
			return nil, fmt.Errorf("causal reference commit: %w", err)
		}
		parts := strings.Split(strings.TrimSpace(string(out)), "\x00")
		if len(parts) != 2 {
			return nil, fmt.Errorf("incomplete causal reference timestamps")
		}
		authored, err := time.Parse(time.RFC3339, parts[0])
		if err != nil {
			return nil, fmt.Errorf("causal reference author timestamp: %w", err)
		}
		committed, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			return nil, fmt.Errorf("causal reference committer timestamp: %w", err)
		}
		result.Revision = opts.Revision
		result.ReferenceTime = &authored
		result.ReferenceCommittedAt = &committed
	}
	if opts.Until != nil {
		until := *opts.Until
		result.Until = &until
	}
	reader, err := e.newBlobReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	read := func(sha string) (map[string]beadSnapshot, bool, error) {
		if sha == "" {
			return map[string]beadSnapshot{}, true, nil
		}
		data, err := reader.read(sha)
		if err != nil {
			return nil, false, err
		}
		if data == nil {
			return nil, false, nil
		}
		records := make(map[string]beadSnapshot)
		valid := true
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			record, ok := parseBeadJSON(string(line))
			if !ok {
				valid = false
				continue
			}
			if _, exists := records[record.ID]; exists {
				valid = false
			}
			records[record.ID] = record
		}
		return records, valid, nil
	}
	var previous map[string]beadSnapshot
	var previousSHA string
	var previousValid bool
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		before, validBefore := previous, previousValid
		if before == nil || previousSHA != c.oldSHA {
			before, validBefore, err = read(c.oldSHA)
			if err != nil {
				return nil, fmt.Errorf("reading causal parent %s: %w", c.info.SHA, err)
			}
		}
		after, validAfter, err := read(c.newSHA)
		if err != nil {
			return nil, fmt.Errorf("reading causal commit %s: %w", c.info.SHA, err)
		}
		oldState, oldRelevant := causalSnapshotState(before, validBefore, target)
		newState, newRelevant := causalSnapshotState(after, validAfter, target)
		for id := range newRelevant {
			oldRelevant[id] = true
		}
		ids := make([]string, 0, len(oldRelevant))
		for id := range oldRelevant {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		obs := CausalObservation{CommitSHA: c.info.SHA, Timestamp: c.info.Timestamp, CommittedAt: committed[i], Before: oldState, After: newState, Changes: []BeadEvent{}}
		for _, id := range ids {
			old, hadOld := before[id]
			current, hasNew := after[id]
			if hadOld == hasNew && equalHistoricalRecord(old, current) {
				continue
			}
			event := BeadEvent{BeadID: id, CommitSHA: c.info.SHA, Timestamp: c.info.Timestamp, CommitMsg: c.info.Message, Author: c.info.Author, AuthorEmail: c.info.AuthorEmail, EventType: EventModified, TransitionObserved: validBefore && validAfter}
			if hadOld {
				event.Before = old.historicalState()
			}
			if hasNew {
				event.After = current.historicalState()
			}
			switch {
			case !hadOld:
				event.EventType = EventCreated
			case !hasNew:
				event.EventType = EventDeleted
			case old.Status != current.Status:
				event.EventType = determineStatusEvent(old.Status, current.Status)
			}
			obs.Changes = append(obs.Changes, event)
		}
		result.Observations = append(result.Observations, obs)
		previous, previousValid, previousSHA = after, validAfter, c.newSHA
	}
	return result, nil
}

func equalHistoricalRecord(a, b beadSnapshot) bool {
	if a.ID != b.ID || a.Title != b.Title || a.Status != b.Status || len(a.Dependencies) != len(b.Dependencies) {
		return false
	}
	for i := range a.Dependencies {
		if a.Dependencies[i] != b.Dependencies[i] {
			return false
		}
	}
	return true
}

func causalSnapshotState(records map[string]beadSnapshot, valid bool, target string) (CausalState, map[string]bool) {
	state := CausalState{Known: valid, DependencyState: model.DependenciesUnknown, Blockers: []string{}}
	relevant := map[string]bool{target: true}
	issues := make([]model.Issue, 0, len(records))
	for _, record := range records {
		status := normalizeLifecycleStatus(record.Status)
		if !model.Status(status).IsValid() {
			state.Known = false
		}
		issue := model.Issue{ID: record.ID, Status: model.Status(status)}
		for _, dep := range record.Dependencies {
			if (dep.Type != "" && !model.DependencyType(dep.Type).IsValid()) || dep.DependsOnID == "" {
				state.Known = false
			}
			issue.Dependencies = append(issue.Dependencies, &model.Dependency{DependsOnID: dep.DependsOnID, Type: model.DependencyType(dep.Type)})
		}
		issues = append(issues, issue)
	}
	if record, ok := records[target]; ok {
		state.Issue = record.historicalState()
		state.DependencyState = model.NewReadinessIndex(issues).DependencyState(target)
	}
	blockers := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string)
	visit = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		record, ok := records[id]
		if !ok || isClosedLifecycleStatus(normalizeLifecycleStatus(record.Status)) {
			return
		}
		for _, dep := range record.Dependencies {
			t := model.DependencyType(dep.Type)
			if !t.IsBlocking() && t != model.DepParentChild {
				continue
			}
			relevant[dep.DependsOnID] = true
			other, exists := records[dep.DependsOnID]
			if !exists {
				blockers[dep.DependsOnID] = true
				continue
			}
			if isClosedLifecycleStatus(normalizeLifecycleStatus(other.Status)) {
				continue
			}
			if t.IsBlocking() {
				blockers[dep.DependsOnID] = true
			} else {
				visit(dep.DependsOnID)
			}
		}
	}
	visit(target)
	for id := range blockers {
		state.Blockers = append(state.Blockers, id)
	}
	sort.Strings(state.Blockers)
	if !state.Known {
		state.Reason = "malformed, duplicate, or invalid historical records"
	} else if state.Issue != nil && state.DependencyState == model.DependenciesUnknown {
		state.Reason = "missing dependency or unresolved parent cycle"
	}
	return state, relevant
}

// blobsReadCounter counts the total number of blob object ids passed to
// readBlobs across the process. It exists solely so tests can prove the
// incremental per-commit cache reads only the NEW commits' blobs (and ~0 when
// nothing is new). It is never read on any production path.
var blobsReadCounter int64

// nullBlobSHA is git's all-zero object id, used in `--raw` diff lines for the
// side of a change where the blob does not exist (a pure addition has an all-zero
// "old" blob; a deletion an all-zero "new" blob).
const nullBlobSHA = "0000000000000000000000000000000000000000"

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
	var unfilteredCache map[string]perCommitEventEntry
	unfilteredLoaded := false

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
		if ce, ok := cached[c.info.SHA]; ok && ce.OldSHA == c.oldSHA && ce.NewSHA == c.newSHA {
			perCommitEvents[i] = ce.Events
			continue
		}
		if opts.BeadID != "" {
			// A prior full-history request already parsed these exact records.
			// parseDiff's bead filter selects events; it does not change their
			// contents or the whole-diff TransitionObserved flag. Reuse only in
			// this direction, retaining freshness and both blob-OID checks.
			if !unfilteredLoaded {
				unfilteredCache = loadPerCommitEvents(perCommitEventCacheNamespace(e.primaryBeadsFile(), ""))
				unfilteredLoaded = true
			}
			if ce, ok := unfilteredCache[c.info.SHA]; ok && ce.OldSHA == c.oldSHA && ce.NewSHA == c.newSHA {
				selected := make([]BeadEvent, 0)
				for _, event := range ce.Events {
					if event.BeadID == opts.BeadID {
						selected = append(selected, event)
					}
				}
				// Non-nil marks even an empty selection as a cache hit. Do not
				// write a second persisted copy under the filtered namespace.
				perCommitEvents[i] = selected
				continue
			}
		}
		noteUse(c.oldSHA, i)
		noteUse(c.newSHA, i)
	}

	// Streaming blob reader: a single long-lived `git cat-file --batch` fed one
	// OID at a time. Each unique uncached blob is read exactly once (guarded by
	// the live `sets` window plus the last-use eviction below), so total blob
	// reads equal the deduped uncached count — the incremental cache still reads
	// only the NEW commits' blobs (~0 when nothing is new), matching the previous
	// readBlobs behavior for the blobsReadCounter tests.
	reader, err := e.newBlobReader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

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

	// Persist only the freshly computed commits (pure hits are never re-written,
	// preserving the no-rewrite-on-pure-hit discipline when nothing is new).
	storePerCommitEvents(namespace, fresh)

	// Compose the full event slice in the SAME ORDER a full extraction produces:
	// concatenate per-commit events in git-log (newest-first) order, exactly as
	// the original single loop did, then reverse to chronological.
	var events []BeadEvent
	for i := range commits {
		events = append(events, perCommitEvents[i]...)
	}

	// snapshotCommits returns newest-first (git log order); match the legacy
	// Extract contract which sorts chronologically before returning.
	reverseEvents(events)
	return events, nil
}

// snapshotCommits returns, newest-first, the commits that touched the followed
// beads file together with the followed file's old (parent) and new blob object
// ids. It uses a metadata-only `git log --raw --follow` (no `-p`): git's `--raw`
// output already carries both blob ids per change, and `--follow` makes git pick
// the correct parent blob across renames.
func (e *Extractor) snapshotCommits(opts ExtractOptions) ([]snapshotCommit, error) {
	primary := e.primaryBeadsFile()

	args := []string{
		"log",
		"--raw",
		"--no-abbrev", // full 40-char blob ids so cat-file resolves them directly
		"--follow",
		"--format=" + gitLogHeaderFormat,
	}
	args = appendHistoryFilters(args, opts)
	args = append(args, "--", primary)

	cmd := gitCommand(e.ctx, withNoColorGit(args)...)
	cmd.Dir = e.repoPath
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log --raw failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git log --raw failed: %w", err)
	}

	return parseSnapshotLog(out)
}

// appendHistoryFilters appends the time/count filters and resolved revision
// before the pathspec separator. A revision bounds ancestry, not author dates.
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
	if opts.Revision != "" {
		args = append(args, opts.Revision)
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

	locs := commitPattern.FindAllIndex(out, -1)
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
			continue
		}
		info, err := parseCommitInfo(string(chunk[:nl]))
		if err != nil {
			return nil, fmt.Errorf("parsing snapshot commit header: %w", err)
		}

		sc := snapshotCommit{info: info}
		if parseRawDiffLines(chunk[nl+1:], &sc) {
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
func parseRawDiffLines(payload []byte, sc *snapshotCommit) bool {
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	scanner.Buffer(make([]byte, 64*1024), gitLogMaxScanTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
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
			continue
		}
		oldSHA := fields[2]
		newSHA := fields[3]
		if oldSHA != nullBlobSHA {
			sc.oldSHA = oldSHA
		}
		if newSHA != nullBlobSHA {
			sc.newSHA = newSHA
		}
		// We only act on commits where the followed file has a current blob; a
		// pure deletion (new == zero) carries no `+{...}` records to extract and
		// the legacy `-p` path likewise produced no events for it.
		return sc.newSHA != ""
	}
	return false
}

// readBlobs reads the requested blob object ids via a single
// `git cat-file --batch` process and returns their contents keyed by object id.
// Object ids must already be unique. Missing blobs map to nil (treated as empty
// by the caller).
//
// NOTE: extractViaSnapshots no longer uses this — it streams via blobReader to
// bound peak memory (#182), since holding every historical blob in one map is
// exactly what made the export path spike to multi-GiB RSS. readBlobs is kept as
// the batch primitive (identical cat-file framing) for callers that genuinely
// need all blobs resident at once.
func (e *Extractor) readBlobs(ids []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(ids))
	atomic.AddInt64(&blobsReadCounter, int64(len(ids)))
	if len(ids) == 0 {
		return result, nil
	}

	// Sort for deterministic request ordering (purely cosmetic; output is keyed
	// by id so order does not affect correctness).
	sort.Strings(ids)

	cmd := gitCommand(e.ctx, "cat-file", "--batch")
	cmd.Dir = e.repoPath
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
			header = strings.TrimRight(header, "\n")
			parts := strings.Fields(header)
			if len(parts) == 2 && parts[1] == "missing" {
				result[s] = nil
				continue
			}
			if len(parts) != 3 {
				return fmt.Errorf("unexpected cat-file header %q for %q", header, s)
			}
			var size int
			if _, err := fmt.Sscanf(parts[2], "%d", &size); err != nil {
				return fmt.Errorf("parsing cat-file size %q: %w", parts[2], err)
			}
			content := make([]byte, size)
			if _, err := readFull(reader, content); err != nil {
				return fmt.Errorf("reading cat-file content for %q: %w", s, err)
			}
			// Trailing newline after each object's content.
			if _, err := reader.Discard(1); err != nil {
				return fmt.Errorf("discarding cat-file trailer for %q: %w", s, err)
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
	cmd := gitCommand(e.ctx, "cat-file", "--batch")
	cmd.Dir = e.repoPath
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

// read returns the content of one blob object id (nil if git reports it
// missing). Each call is counted in blobsReadCounter so the incremental-cache
// tests can prove only the NEW commits' blobs are read.
func (b *blobReader) read(sha string) ([]byte, error) {
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
	trimmed := strings.TrimRight(header, "\n")
	parts := strings.Fields(trimmed)
	if len(parts) == 2 && parts[1] == "missing" {
		return nil, nil
	}
	if len(parts) != 3 {
		return nil, fmt.Errorf("unexpected cat-file header %q for %q", trimmed, sha)
	}
	var size int
	if _, err := fmt.Sscanf(parts[2], "%d", &size); err != nil {
		return nil, fmt.Errorf("parsing cat-file size %q: %w", parts[2], err)
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
	} else {
		content = content[:size]
	}
	b.arenaRunway = 0
	if _, err := readFull(b.out, content); err != nil {
		return nil, fmt.Errorf("reading cat-file content for %q: %w", sha, err)
	}
	// Trailing newline after each object's content.
	if _, err := b.out.Discard(1); err != nil {
		return nil, fmt.Errorf("discarding cat-file trailer for %q: %w", sha, err)
	}
	return content, nil
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

// recordLineEntry is one entry of a recordLineSet: how many times the record line
// occurs in the blob, plus a representative copy of the line bytes (needed to emit
// the synthesized diff for changed records).
type recordLineEntry struct {
	count int
	text  []byte
}

// recordLineSet is the multiset of a blob's JSON record lines (those beginning
// with '{'), keyed by a 64-bit hash of the line. Beads writes one whole JSON
// record per line, so identity of a record is line identity; keying by hash
// avoids retaining/hashing the full multi-KB line strings as map keys
// (~4x faster than a map[string]int over the same blob).
type recordLineSet map[uint64]*recordLineEntry

// newRecordLineSet builds the record-line multiset for a blob. Non-record lines
// (not starting with '{') are ignored, matching parseDiff which only acts on
// lines beginning with '{'. The returned entries alias into blob, which the
// caller retains for the duration of the extraction.
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
		if entry, ok := set[record.hash]; ok {
			entry.count++
		} else {
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

// scanRecordLineDescriptors records only lines whose first byte is '{'. A final
// record without a line-feed is included; empty and non-record lines do not
// consume a record ordinal, which is what makes prefix/suffix comparison match
// the recordLineSet semantics rather than physical line numbers.
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
		if end > start && blob[start] == '{' {
			records = append(records, recordLineDescriptor{start: start, end: end})
		}
		start = next
	}
	return records
}

// synthesizeRecordDiff produces the same `+{...}`/`-{...}` record lines that
// `git log -p --unified=0` would emit for one commit, by computing the line-level
// set difference between the parent blob's record set (old) and the commit blob's
// record set (new). A nil oldSet means "no parent" — every record reads as an
// addition. A record present in exactly one side marks it added/removed; a
// modified record appears as the old line removed and the new line added
// (identical to git's unified=0 output for the downstream parser, which only
// consumes lines beginning with '{').
func synthesizeRecordDiff(oldSet, newSet recordLineSet) []byte {
	var buf bytes.Buffer
	// Removed: present in old, absent (or fewer) in new.
	for h, oe := range oldSet {
		newCount := 0
		if ne, ok := newSet[h]; ok {
			newCount = ne.count
		}
		for i := 0; i < oe.count-newCount; i++ {
			buf.WriteByte('-')
			buf.Write(oe.text)
			buf.WriteByte('\n')
		}
	}
	// Added: present in new, absent (or fewer) in old.
	for h, ne := range newSet {
		oldCount := 0
		if oe, ok := oldSet[h]; ok {
			oldCount = oe.count
		}
		for i := 0; i < ne.count-oldCount; i++ {
			buf.WriteByte('+')
			buf.Write(ne.text)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}
