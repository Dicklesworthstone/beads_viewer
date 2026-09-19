package correlation

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type causalGitStep struct {
	authorHour, commitHour int
	records                string
}

func causalGitRepository(t *testing.T, steps []causalGitStep) (string, string, time.Time) {
	t.Helper()
	repo := t.TempDir()
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	git := func(step causalGitStep, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Causal Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Causal Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+start.Add(time.Duration(step.authorHour)*time.Hour).Format(time.RFC3339), "GIT_COMMITTER_DATE="+start.Add(time.Duration(step.commitHour)*time.Hour).Format(time.RFC3339))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(causalGitStep{}, "init", "-b", "main")
	if err := os.Mkdir(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, ".beads", "issues.jsonl")
	for i, step := range steps {
		if err := os.WriteFile(path, []byte(step.records), 0o644); err != nil {
			t.Fatal(err)
		}
		git(step, "add", ".beads/issues.jsonl")
		git(step, "commit", "--allow-empty", "-m", fmt.Sprintf("snapshot %d", i))
	}
	return repo, path, start
}

func causalRecord(id, status, deps string) string {
	if deps == "" {
		deps = "[]"
	}
	return fmt.Sprintf("{\"id\":%q,\"title\":%q,\"status\":%q,\"dependencies\":%s}\n", id, id, status, deps)
}

func TestCausalityHistoricalIntervals(t *testing.T) {
	dep := `[{"depends_on_id":"B","type":"blocks"}]`
	both := `[{"depends_on_id":"B","type":"blocks"},{"depends_on_id":"C","type":"blocks"}]`
	parent := `[{"depends_on_id":"P","type":"parent-child"}]`
	a := func(status, deps string) string { return causalRecord("A", status, deps) }
	b := func(status string) string { return causalRecord("B", status, "") }
	c := func(status string) string { return causalRecord("C", status, "") }
	step := func(hour int, records string) causalGitStep { return causalGitStep{hour, hour, records} }
	for _, tc := range []struct {
		name                          string
		steps                         []causalGitStep
		status                        string
		limit                         int
		blocked, explicit, dependency time.Duration
		known                         bool
	}{
		{"unchanged target dependency", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open")), step(8, a("open", dep)+b("closed")), step(10, a("closed", dep)+b("closed"))}, "closed", 0, 6 * time.Hour, 0, 6 * time.Hour, true},
		{"explicit without dependency", []causalGitStep{step(0, a("open", "")), step(2, a("blocked", "")), step(8, a("open", "")), step(10, a("closed", ""))}, "closed", 0, 6 * time.Hour, 6 * time.Hour, 0, true},
		{"overlap is union", []causalGitStep{step(0, a("open", "")+b("open")+c("open")), step(2, a("open", dep)+b("open")+c("open")), step(4, a("open", both)+b("open")+c("open")), step(6, a("open", both)+b("closed")+c("open")), step(8, a("open", both)+b("closed")+c("closed")), step(10, a("closed", both)+b("closed")+c("closed"))}, "closed", 0, 6 * time.Hour, 0, 6 * time.Hour, true},
		{"edge removal", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open")), step(8, a("open", "")+b("open")), step(10, a("closed", "")+b("open"))}, "closed", 0, 6 * time.Hour, 0, 6 * time.Hour, true},
		{"blocker reopened", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open")), step(8, a("open", dep)+b("closed")), step(9, a("open", dep)+b("open")), step(10, a("open", dep)+b("closed")), step(12, a("closed", dep)+b("closed"))}, "closed", 0, 7 * time.Hour, 0, 7 * time.Hour, true},
		{"ongoing tail uses caller clock", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open"))}, "open", 0, 10 * time.Hour, 0, 10 * time.Hour, true},
		{"parent gate full authority", []causalGitStep{step(0, a("open", parent)+causalRecord("P", "open", "")+b("open")), step(2, a("open", parent)+causalRecord("P", "open", dep)+b("open")), step(8, a("open", parent)+causalRecord("P", "open", dep)+b("closed")), step(10, a("closed", parent)+causalRecord("P", "open", dep)+b("closed"))}, "closed", 0, 6 * time.Hour, 0, 6 * time.Hour, true},
		{"tombstone satisfies dependency", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open")), step(8, a("open", dep)+b("tombstone")), step(10, a("closed", dep)+b("tombstone"))}, "closed", 0, 6 * time.Hour, 0, 6 * time.Hour, true},
		{"related is not blocking", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", `[{"depends_on_id":"B","type":"related"}]`)+b("open")), step(10, a("closed", "")+b("open"))}, "closed", 0, 0, 0, 0, true},
		{"missing blocker unknown", []causalGitStep{step(0, a("open", "")), step(2, a("open", dep)), step(10, a("closed", dep))}, "closed", 0, 0, 0, 0, false},
		{"malformed authority unknown", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("open", dep)+b("open")+"{bad\n"), step(10, a("closed", dep)+b("closed"))}, "closed", 0, 0, 0, 0, false},
		{"truncated before creation", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("blocked", dep)+b("open")), step(8, a("open", dep)+b("closed")), step(10, a("closed", dep)+b("closed"))}, "closed", 2, 0, 0, 0, false},
		{"author clock reverses", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("blocked", dep)+b("open")), {1, 8, a("open", dep) + b("closed")}, step(10, a("closed", dep)+b("closed"))}, "closed", 0, 0, 0, 0, false},
		{"committer clock reverses", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("blocked", dep)+b("open")), {8, 1, a("open", dep) + b("closed")}, step(10, a("closed", dep)+b("closed"))}, "closed", 0, 0, 0, 0, false},
		{"same timestamp genuine zero", []causalGitStep{step(0, a("open", "")+b("open")), step(2, a("blocked", dep)+b("open")), step(2, a("open", dep)+b("closed")), step(10, a("closed", dep)+b("closed"))}, "closed", 0, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, path, start := causalGitRepository(t, tc.steps)
			opts := CorrelatorOptions{Limit: tc.limit, CausalityBeadID: "A"}
			report, err := NewCorrelator(repo, path).GenerateReport([]BeadInfo{{ID: "A", Title: "A", Status: tc.status}}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Histories) != 1 {
				t.Fatalf("authority leaked into selected histories: %v", report.Histories)
			}
			result := report.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
			i := result.Insights
			if i.BlockedDurationKnown != tc.known {
				t.Fatalf("known=%v, expected%v: %+v", i.BlockedDurationKnown, tc.known, i)
			}
			if tc.known && (i.BlockedDuration != tc.blocked || i.ExplicitBlockedDuration != tc.explicit || i.DependencyWaitDuration != tc.dependency) {
				t.Fatalf("wait union/explicit/dependency=%v/%v/%v expected%v/%v/%v", i.BlockedDuration, i.ExplicitBlockedDuration, i.DependencyWaitDuration, tc.blocked, tc.explicit, tc.dependency)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Insights map[string]any `json:"insights"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if !tc.known && wire.Insights["blocked_duration"] != nil {
				t.Fatalf("unknown duration fabricated as a number: %s", encoded)
			}
			if tc.known && wire.Insights["blocked_duration"] != float64(tc.blocked) {
				t.Fatalf("known duration missing: %s", encoded)
			}
			if i.EstimatedWithout != nil {
				t.Fatal("Git observations do not justify a minimum-time estimate")
			}
			if tc.name == "author clock reverses" {
				obs := report.CausalHistory.Observations
				if len(obs) != 4 || !obs[1].Timestamp.After(obs[2].Timestamp) || !obs[1].CommittedAt.Before(obs[2].CommittedAt) {
					t.Fatalf("Git transition order or clocks were rewritten: %+v", obs)
				}
				if i.Coverage != "inconsistent" {
					t.Fatalf("clock contradiction hidden: %+v", i)
				}
				if i.AvgTimeBetween != nil || i.LongestGap != nil {
					t.Fatalf("clock contradiction fabricated a gap: %+v", i)
				}
				if wire.Insights["critical_path_duration"] != nil {
					t.Fatalf("contradictory clocks fabricated a zero constraint-path duration: %s", encoded)
				}
				for _, event := range result.Chain.Events {
					if event.DurationNext != nil {
						t.Fatalf("clock contradiction fabricated a next duration: %+v", event)
					}
				}
			}
			if tc.name == "ongoing tail uses caller clock" && (i.CriticalPathDuration != 10*time.Hour || len(i.BlockedPeriods) != 1 || !i.BlockedPeriods[0].Ongoing) {
				t.Fatalf("ongoing wait was lost or fabricated as a release: %+v", result)
			}
			if tc.name == "ongoing tail uses caller clock" {
				last, err := json.Marshal(result.Chain.Events[len(result.Chain.Events)-1])
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(last), "committed_at") || strings.Contains(string(last), "commit_sha") {
					t.Fatalf("reference-clock observation fabricated a commit timestamp: %s", last)
				}
			}
		})
	}
}

func TestCausalityIncrementalCacheAdvancePreservesEvidence(t *testing.T) {
	for _, kind := range []string{"revision", "causal", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			repo, path, start := causalGitRepository(t, []causalGitStep{{0, 0, causalRecord("A", "open", "")}, {2, 2, causalRecord("A", "blocked", "")}})
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = repo
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+start.Add(3*time.Hour).Format(time.RFC3339), "GIT_COMMITTER_DATE="+start.Add(3*time.Hour).Format(time.RFC3339))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			opts := CorrelatorOptions{}
			if kind == "revision" {
				opts.Revision = git("rev-parse", "HEAD")
			}
			if kind == "causal" {
				opts.CausalityBeadID = "A"
			}
			beads := []BeadInfo{{ID: "A", Title: "A", Status: "blocked"}}
			cached := NewIncrementalCorrelator(repo, path)
			if _, err := cached.GenerateReport(beads, opts); err != nil {
				t.Fatal(err)
			}
			data := strings.ReplaceAll(causalRecord("A", "blocked", ""), `"title":"A"`, `"title":"changed"`)
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", ".beads/issues.jsonl")
			git("commit", "-m", "A: later state")
			result, err := cached.GenerateReportWithDetails(beads, opts)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "revision":
				if result.Report.LatestCommitSHA != opts.Revision || len(result.Report.Histories["A"].Events) != 2 {
					t.Fatalf("incremental cache appended beyond revision: %+v", result.Report)
				}
			case "causal":
				if result.Report.CausalHistory == nil || len(result.Report.CausalHistory.Observations) != 3 {
					t.Fatalf("incremental cache discarded full causal evidence: %+v", result.Report)
				}
			case "ordinary":
				if !result.WasIncremental || len(result.Report.Histories["A"].Events) != 3 {
					t.Fatalf("ordinary incremental positive changed: %+v", result)
				}
			}
		})
	}
}

func TestCausalityRevisionAnchorClocks(t *testing.T) {
	for _, anchor := range []causalGitStep{{3, 3, ""}, {1, 3, ""}, {3, 1, ""}} {
		t.Run(fmt.Sprintf("author%d_committer%d", anchor.authorHour, anchor.commitHour), func(t *testing.T) {
			open := causalRecord("A", "open", "")
			blocked := causalRecord("A", "blocked", "")
			anchor.records = blocked // code-only commit, absent from the beads path walk
			repo, path, start := causalGitRepository(t, []causalGitStep{{0, 0, open}, {2, 2, blocked}, anchor})
			cmd := exec.Command("git", "rev-parse", "HEAD")
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("resolving actual anchor: %v\n%s", err, out)
			}
			report, err := NewCorrelator(repo, path).GenerateReport([]BeadInfo{{ID: "A", Status: "blocked"}}, CorrelatorOptions{CausalityBeadID: "A", Revision: strings.TrimSpace(string(out))})
			if err != nil {
				t.Fatal(err)
			}
			result := report.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
			known := anchor.authorHour == 3 && anchor.commitHour == 3
			if len(report.CausalHistory.Observations) != 2 || result.Insights.BlockedDurationKnown != known {
				t.Fatalf("anchor chronology/availability wrong: %+v / %+v", report.CausalHistory, result.Insights)
			}
			if known {
				if result.Insights.BlockedDuration != time.Hour || !result.Chain.EndTime.Equal(start.Add(3*time.Hour)) {
					t.Fatalf("code-only anchor should retain one hour of ongoing wait: %+v", result)
				}
			} else {
				raw, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if result.Insights.Coverage != "inconsistent" || !strings.Contains(string(raw), `"blocked_duration":null`) || result.Insights.LongestGap != nil {
					t.Fatalf("contradictory anchor clocks fabricated duration: %s", raw)
				}
			}
		})
	}
}

func TestCausalityExtractorAndCacheEvidence(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	dep := `[{"depends_on_id":"B","type":"blocks"}]`
	records := func(status, deps, blocker string) string {
		return causalRecord("A", status, deps) + causalRecord("B", blocker, "")
	}
	repo, path, start := causalGitRepository(t, []causalGitStep{{0, 0, records("open", "", "open")}, {2, 2, records("blocked", dep, "open")}, {8, 8, records("open", dep, "closed")}, {10, 10, records("closed", dep, "closed")}})
	extractor := NewExtractor(repo, path)
	legacy, err := extractor.extractViaGitLogPatch(ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := extractor.extractViaSnapshots(ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := extractor.extractViaSnapshots(ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, snapshot) || !reflect.DeepEqual(snapshot, replayed) {
		t.Fatalf("patch/snapshot/cache evidence differs:\n%+v\n%+v\n%+v", legacy, snapshot, replayed)
	}
	var blocked *BeadEvent
	for i := range legacy {
		e := &legacy[i]
		if e.BeadID == "A" && e.After != nil && e.After.Status == "blocked" {
			blocked = e
		}
	}
	if blocked == nil || blocked.Before == nil || blocked.Before.Status != "open" || len(blocked.Before.Dependencies) != 0 || len(blocked.After.Dependencies) != 1 || blocked.After.Dependencies[0] != (HistoricalDependency{DependsOnID: "B", Type: "blocks"}) {
		t.Fatalf("shared extractor lost status/dependency transition: %+v", blocked)
	}
	boundedOpts := ExtractOptions{Revision: blocked.CommitSHA}
	boundedPatch, err := extractor.extractViaGitLogPatch(boundedOpts)
	if err != nil {
		t.Fatal(err)
	}
	boundedSnapshot, err := extractor.extractViaSnapshots(boundedOpts)
	if err != nil {
		t.Fatal(err)
	}
	boundedReplay, err := extractor.extractViaSnapshots(boundedOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(boundedPatch) != 3 || !reflect.DeepEqual(boundedPatch, boundedSnapshot) || !reflect.DeepEqual(boundedPatch, boundedReplay) {
		t.Fatalf("revision patch/snapshot/cache lost creation+blocked transitions: %+v / %+v / %+v", boundedPatch, boundedSnapshot, boundedReplay)
	}
	for _, event := range boundedPatch {
		if event.Timestamp.After(start.Add(2 * time.Hour)) {
			t.Fatalf("future patch event: %+v", event)
		}
	}
	corr := NewCorrelator(repo, path)
	beads := []BeadInfo{{ID: "A", Title: "A", Status: "closed"}}
	opts := CorrelatorOptions{CausalityBeadID: "A"}
	fresh, err := corr.GenerateReportCached(beads, opts)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := corr.GenerateReportCached(beads, opts)
	if err != nil {
		t.Fatal(err)
	}
	// A live display title edit forces report reassembly from the HEAD artifact.
	beads[0].Title = "changed live title"
	reassembled, err := corr.GenerateReportCached(beads, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fresh.CausalHistory, hit.CausalHistory) || !reflect.DeepEqual(fresh.CausalHistory, reassembled.CausalHistory) {
		t.Fatal("report/artifact caches lost historical authority")
	}
	for _, report := range []*HistoryReport{fresh, hit, reassembled} {
		result := report.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
		if result.Insights.BlockedDuration != 6*time.Hour || result.Insights.BlockedPercentage != 60 || !result.Insights.BlockedDurationKnown {
			t.Fatalf("cache consumer lost measured wait: %+v", result.Insights)
		}
	}
	ordinary, err := corr.GenerateReportCached(beads, CorrelatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.CausalHistory != nil || len(ordinary.Histories["A"].Events) != 4 {
		t.Fatal("causal cache key contaminated ordinary lifecycle output")
	}
	other, err := corr.GenerateReportCached([]BeadInfo{{ID: "B", Status: "closed"}}, CorrelatorOptions{CausalityBeadID: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if other.CausalHistory == nil || other.CausalHistory.BeadID != "B" {
		t.Fatal("target cache key reused another bead's observations")
	}
	boundedReportOpts := CorrelatorOptions{CausalityBeadID: "A", Revision: blocked.CommitSHA}
	if hashOptions(opts) == hashOptions(boundedReportOpts) {
		t.Fatal("revision missing from cache identity")
	}
	beads[0].Status = "blocked"
	var boundedHistory *CausalHistory
	for i := 0; i < 3; i++ {
		if i == 2 {
			beads[0].Title = "forces revision artifact reassembly"
		}
		r, err := corr.GenerateReportCached(beads, boundedReportOpts)
		if err != nil {
			t.Fatal(err)
		}
		if r.LatestCommitSHA != blocked.CommitSHA || r.Window.Revision != blocked.CommitSHA || r.Window.Commits != 2 || len(r.CausalHistory.Observations) != 2 {
			t.Fatalf("wrong revision/window: %+v / %+v", r.Window, r.CausalHistory)
		}
		if i == 0 {
			orphans, err := NewOrphanDetectorAt(r, repo, start.Add(12*time.Hour)).DetectOrphans(ExtractOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if orphans.Window.Revision != blocked.CommitSHA || orphans.Window.Commits != 2 || !strings.Contains(orphans.GitRange, blocked.CommitSHA) {
				t.Fatalf("history consumer discarded revision: %+v", orphans)
			}
		}
		if i == 0 {
			boundedHistory = r.CausalHistory
		} else if !reflect.DeepEqual(boundedHistory, r.CausalHistory) {
			t.Fatal("revision report/artifact cache changed evidence")
		}
		result := r.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
		if !result.Chain.EndTime.Equal(start.Add(2*time.Hour)) || !result.Insights.BlockedDurationKnown || result.Insights.BlockedDuration != 0 {
			t.Fatalf("revision cache lost known zero at observation boundary: %+v", result)
		}
	}
	// The unsupported old chronology assertion is an explicit negative control.
	raw, err := json.Marshal(fresh.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"observed_wait"`) || !strings.Contains(string(raw), `"kind":"dependency_transition"`) {
		t.Fatalf("public output is missing actual evidence links: %s", raw)
	}
	chain := fresh.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
	if chain.Insights.CriticalPathDuration != 6*time.Hour || len(chain.Insights.CriticalPath) >= len(chain.Chain.Events) {
		t.Fatalf("critical path is not the longest evidenced wait: %+v", chain)
	}
	if chain.Insights.LongestGap == nil || *chain.Insights.LongestGap != 6*time.Hour || chain.Insights.AvgTimeBetween == nil || chain.Insights.LongestGapDesc == "" {
		t.Fatalf("known transition chronology lost gap metrics: %+v", chain.Insights)
	}
	var nextSix bool
	for _, event := range chain.Chain.Events {
		if event.DurationNext != nil && *event.DurationNext == 6*time.Hour {
			nextSix = true
		}
	}
	if !nextSix {
		t.Fatal("known six-hour next-event duration was lost")
	}
}

func TestCausalityDoesNotAttributeUnrelatedDependencyChanges(t *testing.T) {
	dep := `[{"depends_on_id":"B","type":"blocks"}]`
	repo, path, start := causalGitRepository(t, []causalGitStep{
		{0, 0, causalRecord("A", "open", dep) + causalRecord("B", "closed", "")},
		// Removing a satisfied edge while claiming A and reopening B does not
		// make B's reopening a cause of A's claim. Both dependency gates are clear.
		{2, 2, causalRecord("A", "in_progress", "") + causalRecord("B", "open", "")},
		{10, 10, causalRecord("A", "closed", "") + causalRecord("B", "open", "")},
	})
	report, err := NewCorrelator(repo, path).GenerateReport([]BeadInfo{{ID: "A", Status: "closed"}}, CorrelatorOptions{CausalityBeadID: "A"})
	if err != nil {
		t.Fatal(err)
	}
	result := report.BuildCausalityChainAt("A", DefaultCausalityOptions(), start.Add(12*time.Hour))
	if result.Insights.BlockedDuration != 0 || !result.Insights.BlockedDurationKnown {
		t.Fatalf("satisfied dependency was reported as blocked: %+v", result.Insights)
	}
	if len(result.Chain.Links) != 0 {
		t.Fatalf("unrelated dependency change was promoted to causal evidence: %+v", result.Chain.Links)
	}
	var claimed bool
	for _, event := range result.Chain.Events {
		if event.Type == CausalClaimed {
			claimed = true
		}
	}
	if !claimed {
		t.Fatal("claim lifecycle was lost while withholding unsupported causation")
	}
}

// This fixture exercises the real Git extractor and public report producer.
// Its expected waiting interval is specified independently of the implementation.
func TestCausalityRecordedBlockingFromGit(t *testing.T) {
	repo := t.TempDir()
	git := func(date time.Time, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Causal Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Causal Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE="+date.Format(time.RFC3339), "GIT_COMMITTER_DATE="+date.Format(time.RFC3339))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	git(start, "init", "-b", "main")
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "issues.jsonl")
	for _, step := range []struct {
		hour          int
		target, block string
		dependency    bool
	}{
		{0, "open", "open", false},
		{2, "blocked", "open", true},
		{8, "open", "closed", true},
		{10, "closed", "closed", true},
	} {
		deps := "[]"
		if step.dependency {
			deps = `[{"depends_on_id":"rc-blocker","type":"blocks"}]`
		}
		data := fmt.Sprintf("{\"id\":\"rc-target\",\"title\":\"Target\",\"status\":%q,\"dependencies\":%s}\n{\"id\":\"rc-blocker\",\"title\":\"Blocker\",\"status\":%q}\n", step.target, deps, step.block)
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		git(start.Add(time.Duration(step.hour)*time.Hour), "add", ".beads/issues.jsonl")
		git(start.Add(time.Duration(step.hour)*time.Hour), "commit", "-m", fmt.Sprintf("record step %d", step.hour))
	}
	report, err := NewCorrelator(repo, path).GenerateReport([]BeadInfo{{ID: "rc-target", Title: "Target", Status: "closed"}}, CorrelatorOptions{CausalityBeadID: "rc-target"})
	if err != nil {
		t.Fatal(err)
	}
	result := report.BuildCausalityChainAt("rc-target", DefaultCausalityOptions(), start.Add(12*time.Hour))
	if result == nil || !result.Chain.IsComplete || result.Chain.TotalTime != 10*time.Hour {
		t.Fatalf("ordinary lifecycle lost: %+v", result)
	}
	if result.Insights.BlockedDuration != 6*time.Hour || result.Insights.BlockedPercentage != 60 || result.Insights.ActiveDuration != 4*time.Hour {
		t.Fatalf("recorded 02:00–08:00 wait must be 6h/60%% with 4h nonblocked elapsed; got %+v", result.Insights)
	}
}

// Helper to create test timestamps
func testTime(offsetHours int) time.Time {
	base := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(offsetHours) * time.Hour)
}

func TestBuildCausalityChain_BasicChain(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(2)},
					{EventType: EventClosed, Timestamp: testTime(10)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: "Fix bug", Timestamp: testTime(5)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-test", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check chain structure
	if result.Chain.BeadID != "bv-test" {
		t.Errorf("Expected bead ID 'bv-test', got '%s'", result.Chain.BeadID)
	}

	if result.Chain.Status != "closed" {
		t.Errorf("Expected status 'closed', got '%s'", result.Chain.Status)
	}

	if !result.Chain.IsComplete {
		t.Error("Expected IsComplete to be true for closed bead")
	}

	// Should have 4 events: created, claimed, commit, closed
	if len(result.Chain.Events) != 4 {
		t.Errorf("Expected 4 events, got %d", len(result.Chain.Events))
	}

	// Check event order (should be sorted by timestamp)
	expectedOrder := []CausalEventType{CausalCreated, CausalClaimed, CausalCommit, CausalClosed}
	for i, expected := range expectedOrder {
		if result.Chain.Events[i].Type != expected {
			t.Errorf("Event %d: expected type '%s', got '%s'", i, expected, result.Chain.Events[i].Type)
		}
	}
}

// TestBuildCausalityChain_RecordsDeletion covers the simple chronology path
// (no recorded CausalHistory). The extractor emits EventDeleted for a bead
// removed from the source, the rich path and the dashboard export both surface
// it, and this path previously dropped it via the default:continue arm, so a
// deleted bead's chronology ended silently at its last claim. The deletion must
// appear as a CausalDeleted event and the chain must end at its timestamp.
func TestBuildCausalityChain_RecordsDeletion(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-gone": {
				BeadID: "bv-gone",
				Title:  "Removed Bead",
				Status: "tombstone",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(2)},
					{EventType: EventDeleted, Timestamp: testTime(6)},
				},
			},
		},
	}

	result := report.BuildCausalityChain("bv-gone", DefaultCausalityOptions())
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Chain.IsComplete {
		t.Error("a tombstoned bead should be complete, not treated as ongoing")
	}
	types := make([]CausalEventType, len(result.Chain.Events))
	for i, e := range result.Chain.Events {
		types[i] = e.Type
	}
	want := []CausalEventType{CausalCreated, CausalClaimed, CausalDeleted}
	if len(types) != len(want) {
		t.Fatalf("expected %v, got %v", want, types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d: expected %q, got %q (full: %v)", i, want[i], types[i], types)
		}
	}
	// The chain must end at the deletion, not at the last claim.
	if !result.Chain.EndTime.Equal(testTime(6)) {
		t.Errorf("expected chain to end at the deletion timestamp %v, got %v", testTime(6), result.Chain.EndTime)
	}
}

func TestBuildCausalityChainAtPinsOpenDurationAndTieOrder(t *testing.T) {
	pinned := testTime(24)
	start := testTime(0)
	report := &HistoryReport{
		DataHash: "pinned-hash",
		Histories: map[string]BeadHistory{
			"bv-open": {
				BeadID: "bv-open",
				Title:  "Open work",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventClaimed, Timestamp: start},
					{EventType: EventCreated, Timestamp: start},
				},
				Commits: []CorrelatedCommit{{ShortSHA: "abc1234", Message: "work", Timestamp: start}},
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-open", CausalityOptions{IncludeCommits: true}, pinned)
	if result == nil {
		t.Fatal("expected causality result")
	}
	if !result.GeneratedAt.Equal(pinned) || !result.Chain.EndTime.Equal(pinned) {
		t.Fatalf("pinned times = generated %v, end %v; want %v", result.GeneratedAt, result.Chain.EndTime, pinned)
	}
	if got, want := result.Chain.TotalTime, pinned.Sub(start); got != want {
		t.Fatalf("total time = %v, want %v", got, want)
	}
	wantTypes := []CausalEventType{CausalCreated, CausalClaimed, CausalCommit}
	for i, want := range wantTypes {
		if result.Chain.Events[i].Type != want {
			t.Fatalf("event %d type = %s, want %s", i, result.Chain.Events[i].Type, want)
		}
	}

	zeroResult := report.BuildCausalityChainAt("bv-open", CausalityOptions{IncludeCommits: true}, time.Time{})
	if !zeroResult.GeneratedAt.IsZero() {
		t.Fatalf("zero generated_at was replaced with %v", zeroResult.GeneratedAt)
	}
	if !zeroResult.Chain.EndTime.Equal(start) || zeroResult.Chain.TotalTime != 0 {
		t.Fatalf("pre-event zero instant should clamp deterministically: end=%v total=%v", zeroResult.Chain.EndTime, zeroResult.Chain.TotalTime)
	}
}

func TestBuildCausalityChain_CausalLinks(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
					{EventType: EventClosed, Timestamp: testTime(2)},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}
	result := report.BuildCausalityChain("bv-test", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// These fixtures retain chronology only. The previous implementation falsely
	// labeled every predecessor as a cause; real constraint links are covered by
	// the Git-backed blocking test instead.
	if len(result.Chain.Events) != 3 || result.Chain.EdgeCount != 0 || len(result.Insights.CriticalPath) != 0 {
		t.Fatalf("chronology must survive without fabricated causation: %+v", result)
	}
	for _, event := range result.Chain.Events {
		if event.CausedByID != nil || len(event.EnablesIDs) > 0 {
			t.Fatalf("unsupported causal link: %+v", event)
		}
	}
	if result.Insights.Coverage != "unavailable" || result.Insights.BlockedDurationKnown {
		t.Fatalf("missing constraint evidence was presented as known: %+v", result.Insights)
	}
}

func TestBuildCausalityChain_NotFound(t *testing.T) {
	report := &HistoryReport{
		DataHash:  "test-hash",
		Histories: map[string]BeadHistory{},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("nonexistent", opts)

	if result != nil {
		t.Error("Expected nil result for nonexistent bead")
	}
}

func TestBuildCausalityChainAtClosedWithoutRetainedEventsIsComplete(t *testing.T) {
	pinned := testTime(24)
	report := &HistoryReport{
		DataHash: "empty-history-hash",
		Histories: map[string]BeadHistory{
			"bv-closed": {
				BeadID: "bv-closed",
				Title:  "Closed without retained events",
				Status: "closed",
			},
		},
	}

	result := report.BuildCausalityChainAt("bv-closed", CausalityOptions{IncludeCommits: false}, pinned)
	if result == nil {
		t.Fatal("expected causality result")
	}
	if !result.Chain.IsComplete {
		t.Fatal("closed status must remain complete when no lifecycle events were retained")
	}
	if result.Chain.TotalTime != 0 {
		t.Fatalf("empty closed history total time = %v, want 0", result.Chain.TotalTime)
	}
	if got, want := result.Insights.Summary, "Lifecycle chronology only; blocking duration is unavailable"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestBuildCausalityChain_WithCommits(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: "First commit", Timestamp: testTime(2)},
					{ShortSHA: "def5678", Message: "Second commit", Timestamp: testTime(3)},
				},
			},
		},
	}

	// With commits
	optsWithCommits := CausalityOptions{IncludeCommits: true}
	resultWith := report.BuildCausalityChain("bv-test", optsWithCommits)

	if resultWith.Insights.CommitCount != 2 {
		t.Errorf("Expected 2 commits, got %d", resultWith.Insights.CommitCount)
	}

	// Without commits
	optsNoCommits := CausalityOptions{IncludeCommits: false}
	resultWithout := report.BuildCausalityChain("bv-test", optsNoCommits)

	if resultWithout.Insights.CommitCount != 0 {
		t.Errorf("Expected 0 commits when IncludeCommits=false, got %d", resultWithout.Insights.CommitCount)
	}
}

func TestBuildCausalityChain_InProgress(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "in_progress",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(1)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-test", opts)

	if result.Chain.IsComplete {
		t.Error("Expected IsComplete to be false for in_progress bead")
	}

	// EndTime should be after StartTime for in-progress beads
	if !result.Chain.EndTime.After(result.Chain.StartTime) {
		t.Error("EndTime should be after StartTime")
	}
}

func TestCausalInsights_BlockedPercentage(t *testing.T) {
	// Test the blocked percentage calculation
	insights := CausalInsights{
		TotalDuration:   10 * time.Hour,
		BlockedDuration: 5 * time.Hour,
	}

	// Recalculate active duration and blocked percentage
	insights.ActiveDuration = insights.TotalDuration - insights.BlockedDuration
	if insights.TotalDuration > 0 {
		insights.BlockedPercentage = float64(insights.BlockedDuration) / float64(insights.TotalDuration) * 100
	}

	if insights.BlockedPercentage != 50 {
		t.Errorf("Expected 50%% blocked, got %.1f%%", insights.BlockedPercentage)
	}

	if insights.ActiveDuration != 5*time.Hour {
		t.Errorf("Expected 5h active, got %v", insights.ActiveDuration)
	}
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Minute, "30m"},
		{90 * time.Minute, "1h"},
		{5 * time.Hour, "5h"},
		{25 * time.Hour, "1d"},
		{3 * 24 * time.Hour, "3d"},
		{10 * 24 * time.Hour, "1w"},
		{35 * 24 * time.Hour, "1mo"},
	}

	for _, tt := range tests {
		result := formatDurationShort(tt.duration)
		if result != tt.expected {
			t.Errorf("formatDurationShort(%v) = '%s', expected '%s'", tt.duration, result, tt.expected)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	tests := []struct {
		pct      float64
		expected string
	}{
		{0, "0%"},
		{50, "50%"},
		{100, "100%"},
		{33.7, "33%"}, // Truncates to int
	}

	for _, tt := range tests {
		result := formatPercent(tt.pct)
		if result != tt.expected {
			t.Errorf("formatPercent(%.1f) = '%s', expected '%s'", tt.pct, result, tt.expected)
		}
	}
}

func TestFormatInt(t *testing.T) {
	tests := []struct {
		n        int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{123, "123"},
		{-5, "-5"},
	}

	for _, tt := range tests {
		result := formatInt(tt.n)
		if result != tt.expected {
			t.Errorf("formatInt(%d) = '%s', expected '%s'", tt.n, result, tt.expected)
		}
	}
}

func TestBuildSummary_Completed(t *testing.T) {
	chain := &CausalChain{
		IsComplete: true,
		TotalTime:  6 * time.Hour,
	}
	insights := &CausalInsights{
		TotalDuration:     6 * time.Hour,
		CommitCount:       3,
		BlockedPercentage: 10,
	}

	summary := buildSummary(chain, insights)

	// Should mention completion and commit count
	if summary == "" {
		t.Error("Expected non-empty summary")
	}
}

func TestBuildSummary_InProgress(t *testing.T) {
	chain := &CausalChain{
		IsComplete: false,
		TotalTime:  2 * 24 * time.Hour,
	}
	insights := &CausalInsights{
		TotalDuration:     2 * 24 * time.Hour,
		CommitCount:       5,
		BlockedPercentage: 0,
	}

	summary := buildSummary(chain, insights)

	if summary == "" {
		t.Error("Expected non-empty summary")
	}
}

func TestGenerateRecommendations_HighBlockedPercentage(t *testing.T) {
	chain := &CausalChain{IsComplete: false}
	insights := &CausalInsights{
		TotalDuration:     24 * time.Hour,
		BlockedPercentage: 60,
	}

	recs := generateRecommendations(chain, insights)

	found := false
	for _, rec := range recs {
		if rec != "" && len(rec) > 10 {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected at least one meaningful recommendation for high blocked percentage")
	}
}

func TestGenerateRecommendations_LongGap(t *testing.T) {
	chain := &CausalChain{IsComplete: true}
	longGap := 10 * 24 * time.Hour
	insights := &CausalInsights{
		TotalDuration:     14 * 24 * time.Hour,
		BlockedPercentage: 0,
		LongestGap:        &longGap,
	}

	recs := generateRecommendations(chain, insights)

	found := false
	for _, rec := range recs {
		if rec != "" && len(rec) > 10 {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected at least one recommendation for long gap")
	}
}

func TestGenerateRecommendations_NoIssues(t *testing.T) {
	chain := &CausalChain{IsComplete: true}
	insights := &CausalInsights{
		TotalDuration:     2 * 24 * time.Hour,
		BlockedPercentage: 0,
		CommitCount:       5,
	}

	recs := generateRecommendations(chain, insights)

	// Should have the "no issues" recommendation
	hasNoIssues := false
	for _, rec := range recs {
		if rec == "No significant issues detected in the causal flow" {
			hasNoIssues = true
			break
		}
	}

	if !hasNoIssues {
		t.Error("Expected 'no issues' recommendation for healthy flow")
	}
}

func TestCausalEventTypes(t *testing.T) {
	// Verify all event types are distinct
	types := []CausalEventType{
		CausalCreated,
		CausalClaimed,
		CausalCommit,
		CausalBlocked,
		CausalUnblocked,
		CausalClosed,
		CausalReopened,
	}

	seen := make(map[CausalEventType]bool)
	for _, et := range types {
		if seen[et] {
			t.Errorf("Duplicate event type: %s", et)
		}
		seen[et] = true
	}
}

func TestChainDurations(t *testing.T) {
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-test": {
				BeadID: "bv-test",
				Title:  "Test Bead",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClaimed, Timestamp: testTime(2)},
					{EventType: EventClosed, Timestamp: testTime(10)},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}
	result := report.BuildCausalityChain("bv-test", opts)

	// Check duration calculations
	// Created at hour 0, claimed at hour 2 = 2 hours between
	if result.Chain.Events[0].DurationNext == nil {
		t.Error("Expected non-nil DurationNext for first event")
	} else if *result.Chain.Events[0].DurationNext != 2*time.Hour {
		t.Errorf("Expected 2h between created and claimed, got %v", *result.Chain.Events[0].DurationNext)
	}

	// Claimed at hour 2, closed at hour 10 = 8 hours between
	if result.Chain.Events[1].DurationNext == nil {
		t.Error("Expected non-nil DurationNext for second event")
	} else if *result.Chain.Events[1].DurationNext != 8*time.Hour {
		t.Errorf("Expected 8h between claimed and closed, got %v", *result.Chain.Events[1].DurationNext)
	}

	// Total time should be 10 hours
	if result.Chain.TotalTime != 10*time.Hour {
		t.Errorf("Expected total time of 10h, got %v", result.Chain.TotalTime)
	}
}

func TestDefaultCausalityOptions(t *testing.T) {
	opts := DefaultCausalityOptions()

	if !opts.IncludeCommits {
		t.Error("Expected IncludeCommits to be true by default")
	}
}

// TestBuildCausalityChain_SameTimestamps tests the edge case where all events
// have the same timestamp (gap = 0 between all events). This previously caused
// an array index out of bounds panic.
func TestBuildCausalityChain_SameTimestamps(t *testing.T) {
	// All events at the same timestamp
	sameTime := testTime(0)
	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-same": {
				BeadID: "bv-same",
				Title:  "Same Timestamp Test",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: sameTime},
					{EventType: EventClaimed, Timestamp: sameTime},
					{EventType: EventClosed, Timestamp: sameTime},
				},
			},
		},
	}

	opts := CausalityOptions{IncludeCommits: false}

	// This should not panic (previously it would cause index out of bounds)
	result := report.BuildCausalityChain("bv-same", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Check insights were computed without panic
	if result.Insights == nil {
		t.Fatal("Expected non-nil insights")
	}

	// With same timestamps, all gaps are 0
	if result.Insights.LongestGap != nil && *result.Insights.LongestGap != 0 {
		t.Errorf("Expected longest gap of 0, got %v", *result.Insights.LongestGap)
	}

	// LongestGapDesc should be computed without error
	if result.Insights.LongestGapDesc == "" {
		t.Error("Expected non-empty LongestGapDesc even with 0 gap")
	}
}

// TestBuildCausalityChain_UnicodeCommitMessage tests that commit messages
// with Unicode characters are truncated correctly by runes, not bytes.
func TestBuildCausalityChain_UnicodeCommitMessage(t *testing.T) {
	// Unicode message that would be broken if truncated by bytes
	unicodeMsg := "修复中文测试问题，这是一个很长的提交消息，需要被正确截断" // Chinese characters

	report := &HistoryReport{
		DataHash: "test-hash",
		Histories: map[string]BeadHistory{
			"bv-unicode": {
				BeadID: "bv-unicode",
				Title:  "Unicode Test",
				Status: "closed",
				Events: []BeadEvent{
					{EventType: EventCreated, Timestamp: testTime(0)},
					{EventType: EventClosed, Timestamp: testTime(1)},
				},
				Commits: []CorrelatedCommit{
					{ShortSHA: "abc1234", Message: unicodeMsg, Timestamp: testTime(0)},
				},
			},
		},
	}

	opts := DefaultCausalityOptions()
	result := report.BuildCausalityChain("bv-unicode", opts)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Find the commit event
	var commitEvent *CausalEvent
	for i := range result.Chain.Events {
		if result.Chain.Events[i].Type == CausalCommit {
			commitEvent = &result.Chain.Events[i]
			break
		}
	}

	if commitEvent == nil {
		t.Fatal("Expected to find commit event")
	}

	// Description should be valid UTF-8 (not broken by mid-byte truncation)
	desc := commitEvent.Description
	if !isValidUTF8(desc) {
		t.Errorf("Commit description has invalid UTF-8: %q", desc)
	}

	// Should end with "..." if truncated
	if len([]rune(unicodeMsg)) > 50 && !endsWithEllipsis(desc) {
		t.Error("Expected truncated description to end with '...'")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' { // Replacement character indicates invalid UTF-8
			return false
		}
	}
	return true
}

func endsWithEllipsis(s string) bool {
	runes := []rune(s)
	if len(runes) < 3 {
		return false
	}
	last3 := string(runes[len(runes)-3:])
	return last3 == "..."
}
