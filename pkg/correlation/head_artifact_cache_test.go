package correlation

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
)

func TestHigherLayerCachesRejectPreDeterministicDiffPolicyVersions(t *testing.T) {
	if correlationDiskCacheVersion <= 2 || headArtifactCacheVersion <= 2 {
		t.Fatalf("higher-layer versions were not advanced: report=%d artifact=%d", correlationDiskCacheVersion, headArtifactCacheVersion)
	}
	for _, tt := range []struct {
		name string
		read func(*os.File) (int, int)
	}{
		{
			name: "report",
			read: func(f *os.File) (int, int) {
				got := readCorrelationDiskCacheLocked(f)
				return got.Version, len(got.Entries)
			},
		},
		{
			name: "head artifact",
			read: func(f *os.File) (int, int) {
				got := readHeadArtifactCacheLocked(f)
				return got.Version, len(got.Entries)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache.json")
			if err := os.WriteFile(path, []byte(`{"version":2,"entries":{"stale":{}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			version, entries := tt.read(f)
			if entries != 0 {
				t.Fatalf("pre-policy cache retained %d entries", entries)
			}
			if tt.name == "report" && version != correlationDiskCacheVersion {
				t.Fatalf("empty report cache version=%d, want %d", version, correlationDiskCacheVersion)
			}
			if tt.name == "head artifact" && version != headArtifactCacheVersion {
				t.Fatalf("empty artifact cache version=%d, want %d", version, headArtifactCacheVersion)
			}
		})
	}
}

// TestHistoryArtifactRoundTripPreservesBeadID guards a real bug: CorrelatedCommit.BeadID
// is tagged json:"-" (hidden from the public report), but the HEAD-artifact disk cache
// serializes the pre-assembly Commits slice. Without a custom codec, BeadID is dropped on
// round-trip and assembleReport (which groups commits onto beads by BeadID) returns
// commit-less histories on the middle-tier "edit a bead, re-triage" path.
func TestHistoryArtifactRoundTripPreservesBeadID(t *testing.T) {
	in := &historyArtifact{
		Events: []BeadEvent{{BeadID: "bv-9", EventType: EventCreated}},
		Commits: []CorrelatedCommit{
			{SHA: "aaa", BeadID: "bv-9", Method: "explicit", Confidence: 0.9},
			{SHA: "bbb", BeadID: "bv-7"},
			{SHA: "ccc", BeadID: ""}, // unlinked commit: empty must stay empty
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out historyArtifact
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Commits) != 3 {
		t.Fatalf("commit count: got %d want 3", len(out.Commits))
	}
	want := []string{"bv-9", "bv-7", ""}
	for i, w := range want {
		if out.Commits[i].BeadID != w {
			t.Errorf("Commits[%d].BeadID: got %q want %q", i, out.Commits[i].BeadID, w)
		}
	}
	// Other fields must still round-trip (regression guard for the wire struct).
	if out.Commits[0].SHA != "aaa" || out.Commits[0].Method != "explicit" || out.Commits[0].Confidence != 0.9 {
		t.Errorf("non-BeadID fields not preserved: %+v", out.Commits[0])
	}
	if len(out.Events) != 1 || out.Events[0].BeadID != "bv-9" {
		t.Errorf("events not preserved: %+v", out.Events)
	}
}

func TestPersistentCacheFreshnessRejectsFutureTimestamps(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		createdAt time.Time
		wantFresh bool
	}{
		{name: "fresh", createdAt: now.Add(-time.Hour), wantFresh: true},
		{name: "age boundary", createdAt: now.Add(-correlationDiskCacheMaxAge), wantFresh: true},
		{name: "stale", createdAt: now.Add(-correlationDiskCacheMaxAge - time.Nanosecond)},
		{name: "future", createdAt: now.Add(time.Nanosecond)},
		{name: "zero", createdAt: time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheCreatedAtIsFresh(tt.createdAt, now, correlationDiskCacheMaxAge); got != tt.wantFresh {
				t.Fatalf("cacheCreatedAtIsFresh()=%v, want %v", got, tt.wantFresh)
			}
		})
	}

	reportEntries := map[string]correlationDiskCacheEntry{
		"fresh":  {CreatedAt: now.Add(-time.Hour)},
		"future": {CreatedAt: now.Add(time.Hour)},
		"stale":  {CreatedAt: now.Add(-correlationDiskCacheMaxAge - time.Second)},
		"zero":   {},
	}
	pruneCorrelationDiskCacheEntries(now, reportEntries)
	if len(reportEntries) != 1 {
		t.Fatalf("report prune retained %d entries, want only fresh", len(reportEntries))
	}
	if _, ok := reportEntries["fresh"]; !ok {
		t.Fatal("report prune removed fresh entry")
	}

	artifactEntries := map[string]headArtifactCacheEntry{
		"fresh":  {CreatedAt: now.Add(-time.Hour)},
		"future": {CreatedAt: now.Add(time.Hour)},
		"stale":  {CreatedAt: now.Add(-headArtifactCacheMaxAge - time.Second)},
		"zero":   {},
	}
	pruneHeadArtifactCacheEntries(now, artifactEntries)
	if len(artifactEntries) != 1 {
		t.Fatalf("artifact prune retained %d entries, want only fresh", len(artifactEntries))
	}
	if _, ok := artifactEntries["fresh"]; !ok {
		t.Fatal("artifact prune removed fresh entry")
	}
}

// TestAssembleReportGroupsCommitsAfterRoundTrip verifies the end-to-end consequence:
// after a cache round-trip, assembleReport still attaches commits to their beads.
func TestAssembleReportGroupsCommitsAfterRoundTrip(t *testing.T) {
	art := &historyArtifact{
		Commits: []CorrelatedCommit{
			{SHA: "aaa", BeadID: "bv-9", Method: "explicit", Confidence: 0.9},
			{SHA: "bbb", BeadID: "bv-9", Method: "temporal", Confidence: 0.5},
		},
	}
	b, _ := json.Marshal(art)
	var rt historyArtifact
	if err := json.Unmarshal(b, &rt); err != nil {
		t.Fatal(err)
	}
	c := &Correlator{}
	beads := []BeadInfo{{ID: "bv-9", Title: "t", Status: "open"}}
	report := c.assembleReport(beads, CorrelatorOptions{}, &rt)
	h, ok := report.Histories["bv-9"]
	if !ok {
		t.Fatalf("no history for bv-9")
	}
	if len(h.Commits) != 2 {
		t.Errorf("bv-9 commits after round-trip: got %d want 2 (BeadID grouping broken)", len(h.Commits))
	}
	if len(report.CommitIndex) == 0 {
		t.Errorf("CommitIndex empty after round-trip (reverse lookup broken)")
	}
}

func TestCorrelationCacheNamespaceCanonicalAndIsolated(t *testing.T) {
	repo := t.TempDir()
	relPrimary := filepath.Join(".beads", "issues.jsonl")
	absPrimary := filepath.Join(repo, relPrimary)

	relNamespace := correlationCacheNamespace(repo, relPrimary)
	absNamespace := correlationCacheNamespace(filepath.Join(repo, "."), absPrimary)
	if relNamespace != absNamespace {
		t.Fatalf("equivalent primary paths produced different namespaces: relative=%q absolute=%q", relNamespace, absNamespace)
	}

	legacyNamespace := correlationCacheNamespace(repo, filepath.Join(".beads", "beads.jsonl"))
	if relNamespace == legacyNamespace {
		t.Fatal("different selected Beads histories shared a cache namespace")
	}

	otherRepoNamespace := correlationCacheNamespace(t.TempDir(), relPrimary)
	if relNamespace == otherRepoNamespace {
		t.Fatal("different repositories shared a cache namespace")
	}

	// Reconstruct the pre-fix lifecycle-only namespace. The outer report and
	// HEAD-artifact caches embed co-commit file/line data, so they must not share
	// a namespace with artifacts that omit the co-commit policy identity.
	repoCanonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("canonicalize fixture repository: %v", err)
	}
	legacyInputs := []string{filepath.ToSlash(filepath.Clean(repoCanonical)), filepath.ToSlash(filepath.Clean(relPrimary))}
	legacyInputs = append(legacyInputs, lifecycleGitPolicyNamespaceInputs()...)
	legacySum := sha256.Sum256([]byte(strings.Join(legacyInputs, "\x00")))
	if relNamespace == hex.EncodeToString(legacySum[:]) {
		t.Fatal("outer correlation namespace omitted the co-commit diff policy")
	}
	if !strings.Contains(strings.Join(coCommitGitPolicyNamespaceInputs(), "\x00"), coCommitGitPolicyVersion) {
		t.Fatal("co-commit policy namespace omitted its semantic version")
	}
}

func TestPersistentCorrelationCachesIsolateNamespaces(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	const (
		namespaceA = "repo-a:issues"
		namespaceB = "repo-b:issues"
		headSHA    = "same-head"
		beadsHash  = "same-beads"
		optsHash   = "same-options"
	)

	putCorrelationDiskCachedReport(namespaceA, headSHA, beadsHash, optsHash, &HistoryReport{
		DataHash: beadsHash,
		Histories: map[string]BeadHistory{
			"report-a": {BeadID: "report-a"},
		},
	})
	putCorrelationDiskCachedReport(namespaceB, headSHA, beadsHash, optsHash, &HistoryReport{
		DataHash: beadsHash,
		Histories: map[string]BeadHistory{
			"report-b": {BeadID: "report-b"},
		},
	})
	for namespace, want := range map[string]string{namespaceA: "report-a", namespaceB: "report-b"} {
		got, ok := getCorrelationDiskCachedReport(namespace, headSHA, beadsHash, optsHash)
		if !ok {
			t.Fatalf("report cache namespace %q missed", namespace)
		}
		if got.DataHash != beadsHash {
			t.Fatalf("report cache namespace %q returned data hash %q, want validated input hash %q", namespace, got.DataHash, beadsHash)
		}
		if history, exists := got.Histories[want]; !exists || history.BeadID != want || len(got.Histories) != 1 {
			t.Fatalf("report cache namespace %q = %+v, want isolated history %q", namespace, got, want)
		}
	}

	putHeadArtifactCached(namespaceA, headSHA, optsHash, &historyArtifact{Events: []BeadEvent{{BeadID: "artifact-a"}}})
	putHeadArtifactCached(namespaceB, headSHA, optsHash, &historyArtifact{Events: []BeadEvent{{BeadID: "artifact-b"}}})
	for namespace, want := range map[string]string{namespaceA: "artifact-a", namespaceB: "artifact-b"} {
		got, ok := getHeadArtifactCached(namespace, headSHA, optsHash)
		if !ok || len(got.Events) != 1 || got.Events[0].BeadID != want {
			t.Fatalf("artifact cache namespace %q = (%+v, %v), want bead %q", namespace, got, ok, want)
		}
	}
}

func TestCorrelationGitReadsIgnoreAmbientRepositoryRedirect(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	writeRepo := func(beadID string) (string, string) {
		t.Helper()
		repo := initTempGitRepo(t)
		beadsDir := filepath.Join(repo, ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatalf("create %s beads directory: %v", beadID, err)
		}
		content := "{\"id\":\"" + beadID + "\",\"title\":\"Fixture\",\"status\":\"open\"}\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s beads fixture: %v", beadID, err)
		}
		runGit(t, repo, "add", ".beads/issues.jsonl")
		runGit(t, repo, "commit", "-m", "create "+beadID)
		head, err := getGitHead(repo)
		if err != nil {
			t.Fatalf("resolve %s HEAD: %v", beadID, err)
		}
		return repo, head
	}

	target, targetHead := writeRepo("target-bead")
	redirect, redirectHead := writeRepo("redirect-bead")
	if targetHead == redirectHead {
		t.Fatal("redirect fixture unexpectedly shares target HEAD")
	}
	t.Setenv("GIT_DIR", filepath.Join(redirect, ".git"))
	t.Setenv("GIT_WORK_TREE", redirect)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(redirect, ".git", "objects"))

	if got, err := getGitHeadContext(nil, target); err != nil || got != targetHead {
		t.Fatalf("repo-pinned HEAD=%q error=%v, want target %q (redirect %q)", got, err, targetHead, redirectHead)
	}
	extractor := NewExtractor(target)
	for label, extract := range map[string]func(ExtractOptions) ([]BeadEvent, error){
		"legacy":   extractor.extractViaGitLogPatch,
		"snapshot": extractor.extractViaSnapshots,
	} {
		events, err := extract(ExtractOptions{})
		if err != nil {
			t.Fatalf("%s extraction under redirect: %v", label, err)
		}
		if len(events) != 1 || events[0].BeadID != "target-bead" || events[0].CommitSHA != targetHead {
			t.Fatalf("%s extraction observed redirected repository: %#v", label, events)
		}
	}

	report, err := NewCorrelator(target).GenerateReportCached(
		[]BeadInfo{{ID: "target-bead", Title: "Fixture", Status: "open"}},
		CorrelatorOptions{},
	)
	if err != nil {
		t.Fatalf("cached report under redirect: %v", err)
	}
	history, ok := report.Histories["target-bead"]
	if !ok || len(history.Events) != 1 || history.Events[0].CommitSHA != targetHead {
		t.Fatalf("cached report mixed redirected Git state: history=%+v present=%t", history, ok)
	}
}

func TestPersistentCorrelationCachesFailClosedAcrossShallowDeepen(t *testing.T) {
	for _, cacheLayer := range []string{"report", "head artifact"} {
		t.Run(cacheLayer, func(t *testing.T) {
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

			correlator := NewCorrelator(shallow)
			if state := coCommitRepositoryHistoryState(nil, shallow); state != coCommitHistoryStateShallow {
				t.Fatalf("initial history state=%q, want %q", state, coCommitHistoryStateShallow)
			}
			shallowNamespace := correlator.persistentCacheNamespace()
			beads := []BeadInfo{{ID: "expected", Title: "Expected", Status: "open"}}
			opts := CorrelatorOptions{}

			// A real shallow invocation must create neither higher-level cache.
			report, err := correlator.GenerateReportCached(beads, opts)
			if err != nil {
				t.Fatalf("generate uncached shallow report: %v", err)
			}
			if _, ok := report.Histories["expected"]; !ok {
				t.Fatalf("uncached shallow report omitted expected bead: %+v", report)
			}
			for label, pathFn := range map[string]func(bool) (string, error){
				"report":        correlationDiskCachePath,
				"head artifact": headArtifactCachePath,
			} {
				path, pathErr := pathFn(false)
				if pathErr != nil {
					t.Fatalf("resolve %s cache path: %v", label, pathErr)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("shallow invocation created %s cache %q (stat error %v)", label, path, statErr)
				}
			}

			headSHA, err := getGitHeadContext(nil, shallow)
			if err != nil {
				t.Fatalf("resolve shallow HEAD: %v", err)
			}
			beadsHash := hashBeads(beads)
			optsHash := hashOptions(opts)
			switch cacheLayer {
			case "report":
				putCorrelationDiskCachedReport(shallowNamespace, headSHA, beadsHash, optsHash, &HistoryReport{
					DataHash: beadsHash,
					Histories: map[string]BeadHistory{
						"poison": {BeadID: "poison"},
					},
				})
			case "head artifact":
				putHeadArtifactCached(shallowNamespace, headSHA, optsHash, &historyArtifact{
					Events: []BeadEvent{{
						BeadID:    "expected",
						EventType: EventModified,
						Timestamp: time.Now().UTC(),
						CommitSHA: "poison",
					}},
				})
			default:
				t.Fatalf("unknown cache layer %q", cacheLayer)
			}

			assertNoPoison := func(stage string, got *HistoryReport) {
				t.Helper()
				if _, poisoned := got.Histories["poison"]; poisoned {
					t.Fatalf("%s served poisoned outer report: %+v", stage, got)
				}
				history, ok := got.Histories["expected"]
				if !ok {
					t.Fatalf("%s omitted expected bead: %+v", stage, got)
				}
				for _, event := range history.Events {
					if event.CommitSHA == "poison" {
						t.Fatalf("%s served poisoned HEAD artifact: %+v", stage, got)
					}
				}
			}

			// Even a deliberately seeded entry cannot be loaded while shallow.
			report, err = correlator.GenerateReportCached(beads, opts)
			if err != nil {
				t.Fatalf("generate shallow report with seeded poison: %v", err)
			}
			assertNoPoison("shallow", report)

			// Deepen without moving HEAD. The full-history namespace must differ,
			// and neither higher layer may inherit the shallow entry.
			runGit(t, shallow, "fetch", "--unshallow", "origin")
			if state := coCommitRepositoryHistoryState(nil, shallow); state != coCommitHistoryStateFull {
				t.Fatalf("deepened history state=%q, want %q", state, coCommitHistoryStateFull)
			}
			if currentHead, headErr := getGitHeadContext(nil, shallow); headErr != nil || currentHead != headSHA {
				t.Fatalf("deepen changed HEAD: before=%q after=%q error=%v", headSHA, currentHead, headErr)
			}
			fullNamespace := correlator.persistentCacheNamespace()
			if fullNamespace == shallowNamespace || !strings.Contains(fullNamespace, coCommitHistoryStateFull) {
				t.Fatalf("history-state namespace did not change: shallow=%q full=%q", shallowNamespace, fullNamespace)
			}

			report, err = correlator.GenerateReportCached(beads, opts)
			if err != nil {
				t.Fatalf("generate deepened report: %v", err)
			}
			assertNoPoison("after same-HEAD deepen", report)
		})
	}
}
