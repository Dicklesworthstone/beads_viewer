// Package correlation provides the bounded commit walk shared by the
// message-based correlation strategies and the orphan detector.
package correlation

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// commitWalkFormat is the `git log --format` used by walkCommits. Each record
// starts with a record separator (0x1e), carries the header fields separated
// by NUL (the same fields as gitLogHeaderFormat) followed by the full body,
// and ends with a unit separator (0x1f). With --name-only git then prints the
// changed paths, one per line, until the next record separator. The body can
// contain newlines and NULs are impossible in git metadata, so the two
// separators delimit records unambiguously without a per-line regex.
const commitWalkFormat = "%x1e%H%x00%aI%x00%an%x00%ae%x00%s%x00%b%x1f"

// walkedCommit is one commit from the bounded metadata walk: enough to match
// bead IDs in the message, place the commit in a bead's active window, and
// tell beads-only bookkeeping commits from code commits. Files carry every
// changed path (including .beads/) without stats; strategies that need line
// stats fetch them through the CoCommitExtractor's batched cache.
type walkedCommit struct {
	SHA         string
	Timestamp   time.Time
	Author      string
	AuthorEmail string
	Subject     string
	Body        string
	Files       []string
}

// message returns the text the explicit-ID matcher scans: subject and body.
func (w walkedCommit) message() string {
	if w.Body == "" {
		return w.Subject
	}
	return w.Subject + "\n" + w.Body
}

func normalizeRepoSlashPath(p string) string {
	if len(p) > 2 && p[0] == '"' && p[len(p)-1] == '"' {
		p = p[1 : len(p)-1]
	}
	norm := strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(norm, "./") {
		norm = strings.TrimPrefix(norm, "./")
	}
	norm = strings.TrimSuffix(norm, "/")
	return norm
}

func normalizeStoreDirRel(storeDirRel string) string {
	clean := normalizeRepoSlashPath(storeDirRel)
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return ""
	}
	return clean
}

func isBeadsDirPath(path string) bool {
	clean := normalizeRepoSlashPath(path)
	return clean == ".beads" || strings.HasPrefix(clean, ".beads/") || strings.Contains(clean, "/.beads/")
}

// isTrackerPath reports whether path belongs to a Beads tracker directory —
// either any `.beads/` directory or the configured custom storeDirRel (e.g.
// "custom-beads" or "services/api/.beads").
func isTrackerPath(path, storeDirRel string) bool {
	if isBeadsDirPath(path) {
		return true
	}
	cleanStore := normalizeStoreDirRel(storeDirRel)
	if cleanStore == "" {
		return false
	}
	cleanPath := normalizeRepoSlashPath(path)
	return cleanPath == cleanStore || strings.HasPrefix(cleanPath, cleanStore+"/")
}

// touchesBeadsDir reports whether the commit changed anything under .beads/.
func (w walkedCommit) touchesBeadsDir() bool {
	return w.touchesTrackerDir("")
}

// touchesTrackerDir reports whether the commit changed anything under .beads/
// or storeDirRel.
func (w walkedCommit) touchesTrackerDir(storeDirRel string) bool {
	for _, f := range w.Files {
		if isTrackerPath(f, storeDirRel) {
			return true
		}
	}
	return false
}

// beadsOnly reports whether every changed path lives under .beads/ (a pure
// tracker bookkeeping commit). A commit with no paths at all (empty commit)
// is not beads-only; it is simply not a code commit either.
func (w walkedCommit) beadsOnly() bool {
	return w.trackerOnly("")
}

// trackerOnly reports whether every changed path lives under .beads/ or
// storeDirRel (a pure tracker bookkeeping commit).
func (w walkedCommit) trackerOnly(storeDirRel string) bool {
	if len(w.Files) == 0 {
		return false
	}
	for _, f := range w.Files {
		if !isTrackerPath(f, storeDirRel) {
			return false
		}
	}
	return true
}

// walkCommits runs ONE metadata-only `git log --no-merges --name-only` over the
// last opts.Limit commits reachable from HEAD (bounded by Since/Until; Limit 0
// walks everything) and returns them oldest-first. It is the single window the
// explicit-ID and temporal strategies correlate against and the window the
// orphan detector reports, so the three always agree on which commits were
// considered. It spawns no per-commit subprocesses: the walk over 500 commits
// of this repository costs ~50 ms.
func walkCommits(ctx context.Context, repoPath string, opts ExtractOptions) ([]walkedCommit, error) {
	if opts.Revision != "" {
		if err := validateGitRefArg(opts.Revision); err != nil {
			return nil, err
		}
	}
	args := []string{
		"log",
		"--no-merges",
		"--name-only",
		"--format=" + commitWalkFormat,
	}
	args = appendHistoryFilters(args, opts)

	cmd := gitCommand(ctx, withNoColorGit(args)...)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("git log walk: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git log walk failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git log walk failed: %w", err)
	}
	commits, err := parseCommitWalk(out)
	if err != nil {
		return nil, err
	}
	// git log emits newest first; strategies and the orphan detector want a
	// chronological, deterministic order.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// parseCommitWalk decodes the output of walkCommits' git log invocation.
func parseCommitWalk(out []byte) ([]walkedCommit, error) {
	var commits []walkedCommit
	for _, record := range bytes.Split(out, []byte{0x1e}) {
		if len(bytes.TrimSpace(record)) == 0 {
			continue
		}
		end := bytes.IndexByte(record, 0x1f)
		if end < 0 {
			return nil, fmt.Errorf("parsing git log walk: record without terminator: %.80q", record)
		}
		header := string(record[:end])
		parts := strings.SplitN(header, "\x00", 6)
		if len(parts) != 6 {
			return nil, fmt.Errorf("parsing git log walk: malformed header: %.80q", header)
		}
		timestamp, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			return nil, fmt.Errorf("parsing git log walk: invalid timestamp %q: %w", parts[1], err)
		}
		wc := walkedCommit{
			SHA:         parts[0],
			Timestamp:   timestamp,
			Author:      parts[2],
			AuthorEmail: parts[3],
			Subject:     parts[4],
			Body:        strings.TrimRight(parts[5], "\n"),
		}
		for _, line := range strings.Split(string(record[end+1:]), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			wc.Files = append(wc.Files, line)
		}
		commits = append(commits, wc)
	}
	return commits, nil
}
