package main_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

type relevanceQuery struct {
	ID          string         `json:"id"`
	Split       string         `json:"split"`
	Class       string         `json:"class"`
	Query       string         `json:"query"`
	Intent      string         `json:"intent"`
	Rationale   string         `json:"rationale"`
	Relevant    map[string]int `json:"relevant"`
	ExactID     string         `json:"exact_id"`
	ExpectEmpty bool           `json:"expect_empty"`
	CLIProbe    bool           `json:"cli_probe"`
}

type relevanceFixture struct {
	Version             int              `json:"version"`
	JudgmentProvenance  string           `json:"judgment_provenance"`
	JudgmentPolicy      string           `json:"judgment_policy"`
	ReferenceTime       time.Time        `json:"reference_time"`
	DistractorPolicy    string           `json:"distractor_policy"`
	HumanQuestions      []string         `json:"human_review_questions"`
	DistractorTemplates []string         `json:"distractor_templates"`
	Issues              []model.Issue    `json:"issues"`
	Queries             []relevanceQuery `json:"queries"`
}

func relevanceHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err) // All callers hash concrete JSON fixture/result types.
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func loadRelevanceFixture(t *testing.T) (relevanceFixture, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "search_relevance.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	// Pin judgments written before the first baseline in every evaluation run,
	// including invocations that select only the CLI evaluation test.
	const frozen = "d684da76ee6e9d5bbfc2f6ea7e73c2fb70bceb2710701d94ea9ac6fe9b6eff3f"
	if hash != frozen {
		t.Fatalf("pre-ranking corpus/judgments changed: got %s want %s; independently review the labels before replacing this identity", hash, frozen)
	}
	var fixture relevanceFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture, hash
}

// judgedMetrics uses binary relevance for recall and graded gain 2^grade-1
// discounted by log2(rank+1) for nDCG. Duplicate hits consume a rank but earn
// no additional credit. Empty judgments have no defined retrieval metric;
// callers report empty-result accuracy separately rather than inventing 1.0.
func judgedMetrics(ids []string, judgments map[string]int, k int) (float64, float64) {
	if len(judgments) == 0 || k <= 0 {
		return 0, 0
	}
	grades := make([]int, 0, len(judgments))
	for _, grade := range judgments {
		if grade > 0 {
			grades = append(grades, grade)
		}
	}
	if len(grades) == 0 {
		return 0, 0
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	ideal := 0.0
	for rank, grade := range grades[:min(k, len(grades))] {
		ideal += (math.Exp2(float64(grade)) - 1) / math.Log2(float64(rank+2))
	}
	seen := make(map[string]bool)
	found, dcg := 0, 0.0
	for rank, id := range ids[:min(k, len(ids))] {
		if seen[id] {
			continue
		}
		seen[id] = true
		if grade := judgments[id]; grade > 0 {
			found++
			dcg += (math.Exp2(float64(grade)) - 1) / math.Log2(float64(rank+2))
		}
	}
	return float64(found) / float64(len(grades)), dcg / ideal
}

func TestSearchRelevanceMetricExamples(t *testing.T) {
	// Hand calculations: rank 1 discount=1, rank 2=0.6309297535714575,
	// rank 3=0.5; grade 3 gain=7, grade 2=3, grade 1=1.
	const second = 0.6309297535714575
	for _, tc := range []struct {
		name      string
		ids       []string
		judgments map[string]int
		k         int
		recall    float64
		ndcg      float64
	}{
		{"perfect", []string{"a", "b"}, map[string]int{"a": 3, "b": 1}, 10, 1, 1},
		{"reversed grades", []string{"b", "a"}, map[string]int{"a": 3, "b": 1}, 10, 1, (1 + 7*second) / (7 + second)},
		{"irrelevant first", []string{"x", "b", "a"}, map[string]int{"a": 3, "b": 1}, 10, 1, (second + 3.5) / (7 + second)},
		{"cutoff and missing", []string{"b", "x", "a"}, map[string]int{"a": 3, "b": 1, "c": 2}, 2, 1.0 / 3, 1 / (7 + 3*second)},
		{"duplicate earns once", []string{"a", "a", "b"}, map[string]int{"a": 3, "b": 1}, 10, 1, 7.5 / (7 + second)},
		{"missing all", []string{"x"}, map[string]int{"a": 3}, 10, 0, 0},
		{"empty ranking", nil, map[string]int{"a": 3}, 10, 0, 0},
		{"no judgments undefined", []string{"x"}, nil, 10, 0, 0},
		{"zero cutoff", []string{"a"}, map[string]int{"a": 3}, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recall, ndcg := judgedMetrics(tc.ids, tc.judgments, tc.k)
			if math.Abs(recall-tc.recall) > 1e-12 || math.Abs(ndcg-tc.ndcg) > 1e-12 {
				t.Fatalf("recall=%g nDCG=%g; hand calculation wants %g, %g", recall, ndcg, tc.recall, tc.ndcg)
			}
		})
	}
	goodRecall, goodNDCG := judgedMetrics([]string{"a", "b"}, map[string]int{"a": 3, "b": 1}, 10)
	zeroRecall, zeroNDCG := judgedMetrics([]string{"irrelevant"}, map[string]int{"a": 3, "b": 1}, 10)
	_, shuffledNDCG := judgedMetrics([]string{"b", "a"}, map[string]int{"a": 3, "b": 1}, 10)
	if zeroRecall >= goodRecall || zeroNDCG >= goodNDCG || shuffledNDCG >= goodNDCG {
		t.Fatal("broken ranking did not reduce independently calculated quality")
	}
}

func TestSearchRelevanceFixtureContract(t *testing.T) {
	fixture, _ := loadRelevanceFixture(t)
	if fixture.Version != 1 || fixture.ReferenceTime.IsZero() || !strings.Contains(fixture.JudgmentProvenance, "No human relevance review") || len(fixture.HumanQuestions) == 0 {
		t.Fatal("fixture lacks provenance or explicit unperformed human review")
	}
	ids := make(map[string]bool)
	for _, issue := range fixture.Issues {
		if err := issue.Validate(); err != nil || ids[issue.ID] {
			t.Fatalf("invalid or duplicate fixture issue %q: %v", issue.ID, err)
		}
		ids[issue.ID] = true
	}
	classes, queryIDs := make(map[string]int), make(map[string]bool)
	evaluation, tuning := 0, 0
	for _, query := range fixture.Queries {
		if query.ID == "" || queryIDs[query.ID] || query.Intent == "" || query.Rationale == "" {
			t.Fatalf("query lacks independent reviewable intent/rationale: %+v", query)
		}
		queryIDs[query.ID] = true
		switch query.Split {
		case "evaluation":
			evaluation++
			classes[query.Class]++
		case "tuning":
			tuning++
		default:
			t.Fatalf("query %s lacks explicit split", query.ID)
		}
		if query.ExpectEmpty != (len(query.Relevant) == 0) || (query.ExactID != "" && query.Relevant[query.ExactID] != 3) {
			t.Fatalf("inconsistent relevance intent: %+v", query)
		}
		for id, grade := range query.Relevant {
			if !ids[id] || grade < 1 || grade > 3 {
				t.Fatalf("query %s has invalid judgment %s=%d", query.ID, id, grade)
			}
		}
	}
	if evaluation < 30 || tuning == 0 || len(fixture.DistractorTemplates) < 2 {
		t.Fatalf("insufficient coverage: evaluation=%d tuning=%d", evaluation, tuning)
	}
	for _, class := range []string{"exact_id", "multiword", "prefix", "short", "unicode", "similar_titles", "priority", "status", "no_match", "empty"} {
		if classes[class] == 0 {
			t.Errorf("no held-out %s query", class)
		}
	}
}

type relevanceCLIOutput struct {
	IndexDataHash string             `json:"index_data_hash"`
	CandidateHash string             `json:"candidate_hash"`
	RankingHash   string             `json:"ranking_hash"`
	Mode          string             `json:"mode"`
	Preset        string             `json:"preset"`
	Provider      string             `json:"provider"`
	Dim           int                `json:"dim"`
	Weights       map[string]float64 `json:"weights,omitempty"`
	Results       []struct {
		ID        string  `json:"issue_id"`
		Score     float64 `json:"score"`
		TextScore float64 `json:"text_score"`
	} `json:"results"`
}

type relevanceObservation struct {
	QueryID       string             `json:"query_id"`
	Split         string             `json:"split"`
	Class         string             `json:"class"`
	QueryHash     string             `json:"query_hash"`
	JudgmentHash  string             `json:"judgment_hash"`
	CorpusHash    string             `json:"corpus_hash"`
	ConfigHash    string             `json:"config_hash"`
	Distractors   int                `json:"distractors"`
	CorpusSize    int                `json:"corpus_size"`
	Configuration string             `json:"configuration"`
	IDs           []string           `json:"ids"`
	RecallAt10    *float64           `json:"recall_at_10"`
	NDCGAt10      *float64           `json:"ndcg_at_10"`
	ExactIDFirst  *bool              `json:"exact_id_rank_one,omitempty"`
	EmptyCorrect  *bool              `json:"empty_correct,omitempty"`
	ZeroRecall    *float64           `json:"zero_ranker_recall_at_10"`
	ZeroNDCG      *float64           `json:"zero_ranker_ndcg_at_10"`
	MissingIDs    []string           `json:"missing_judged_ids,omitempty"`
	Stderr        string             `json:"stderr,omitempty"`
	ExitCode      int                `json:"exit_code"`
	RejectedBlank bool               `json:"rejected_blank_input,omitempty"`
	Output        relevanceCLIOutput `json:"cli_output"`
}

type relevanceAggregate struct {
	Queries        int     `json:"queries"`
	JudgedQueries  int     `json:"judged_queries"`
	RecallAt10     float64 `json:"mean_recall_at_10"`
	NDCGAt10       float64 `json:"mean_ndcg_at_10"`
	ZeroRecall     float64 `json:"zero_mean_recall_at_10"`
	ZeroNDCG       float64 `json:"zero_mean_ndcg_at_10"`
	ExactQueries   int     `json:"exact_queries"`
	ExactSuccesses int     `json:"exact_rank_one_successes"`
	EmptyQueries   int     `json:"empty_queries"`
	EmptySuccesses int     `json:"empty_successes"`
}

// TestRobotSearchJudgedRelevance is an evaluation, not a score snapshot gate.
// It uses frozen agent judgments and the real CLI pipeline at all three scales.
// It asserts navigation/determinism and detection of an intentionally broken
// ranker. Other misses remain visible in the report without inventing a quality
// threshold from this first baseline. No production tuning is done here.
func TestRobotSearchJudgedRelevance(t *testing.T) {
	fixture, fixtureHash := loadRelevanceFixture(t)
	bv := buildBvBinary(t)
	binary, err := os.ReadFile(bv)
	if err != nil {
		t.Fatal(err)
	}
	// Each scale owns its source and caches. Pass the fixed search environment
	// only to child processes so independent scales can run concurrently without
	// changing the test process environment or reducing the evaluation matrix.
	environment := append(os.Environ(),
		"BEADS_DIR=", "BEADS_DB=", "BD_DB=", "BV_SEARCH_MODE=",
		"BV_SEARCH_PRESET=", "BV_SEARCH_WEIGHTS=", "BV_SEMANTIC_MODEL=",
		"SOURCE_DATE_EPOCH="+fmt.Sprint(fixture.ReferenceTime.Unix()),
		"BV_SEMANTIC_EMBEDDER=hash", "BV_SEMANTIC_DIM=2048",
	)
	runSearch := func(dir string, args ...string) scopedRun {
		cmd := exec.Command(bv, args...)
		cmd.Dir, cmd.Env = dir, environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return scopedRun{exit: err, stdout: stdout.String(), stderr: stderr.String()}
	}
	report := struct {
		FixtureHash  string                         `json:"fixture_hash"`
		BinaryHash   string                         `json:"binary_hash"`
		Provenance   string                         `json:"judgment_provenance"`
		HumanReview  string                         `json:"human_review"`
		Questions    []string                       `json:"human_review_questions"`
		MetricPolicy string                         `json:"metric_policy"`
		Observations []relevanceObservation         `json:"observations"`
		ByClass      map[string]*relevanceAggregate `json:"by_scale_configuration_split_class"`
	}{fixtureHash, fmt.Sprintf("%x", sha256.Sum256(binary)), fixture.JudgmentProvenance, "unperformed", fixture.HumanQuestions,
		"Recall denominator is every positive judgment; nDCG uses gain 2^grade-1 and log2(rank+1), k=10. No-match queries have null retrieval metrics and separate empty accuracy. Each aggregate reports its denominator; tuning and evaluation never combine. Zero ranker assigns every corpus ID score 0 and breaks ties by ID.", nil, make(map[string]*relevanceAggregate)}
	defer func() {
		for _, aggregate := range report.ByClass {
			if aggregate.JudgedQueries > 0 {
				n := float64(aggregate.JudgedQueries)
				aggregate.RecallAt10 /= n
				aggregate.NDCGAt10 /= n
				aggregate.ZeroRecall /= n
				aggregate.ZeroNDCG /= n
			}
		}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if path := os.Getenv("BV_SEARCH_RELEVANCE_REPORT"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Errorf("create new relevance report: %v", err)
				return
			}
			_, writeErr := file.Write(raw)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				t.Errorf("write relevance report: %v; close: %v", writeErr, closeErr)
			}
			t.Logf("per-query relevance evidence: %s", path)
		}
		for key, aggregate := range report.ByClass {
			summary, _ := json.Marshal(aggregate)
			t.Logf("QUALITY %s %s", key, summary)
		}
	}()
	var scales [3]struct {
		observations []relevanceObservation
		byClass      map[string]*relevanceAggregate
	}
	t.Run("scales", func(t *testing.T) {
		for scaleIndex, distractors := range []int{0, 5000, 10000} {
			t.Run(fmt.Sprint(distractors), func(t *testing.T) {
				t.Parallel()
				scale := &scales[scaleIndex]
				scale.byClass = make(map[string]*relevanceAggregate)
				dir := t.TempDir()
				issues := append([]model.Issue(nil), fixture.Issues...)
				for i := 0; i < distractors; i++ {
					issues = append(issues, model.Issue{ID: fmt.Sprintf("distractor-%05d", i),
						Title:       fmt.Sprintf("%s %d", fixture.DistractorTemplates[i%len(fixture.DistractorTemplates)], i),
						Description: "Administrative physical inventory record; no software implementation work.",
						Status:      []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusClosed}[i%3], Priority: i % 5, IssueType: model.TypeTask})
				}
				var corpus bytes.Buffer
				zeroIDs := make([]string, 0, len(issues))
				for _, issue := range issues {
					if err := json.NewEncoder(&corpus).Encode(issue); err != nil {
						t.Fatal(err)
					}
					zeroIDs = append(zeroIDs, issue.ID)
				}
				sort.Strings(zeroIDs)
				zeroIDs = zeroIDs[:min(10, len(zeroIDs))]
				corpusHash := fmt.Sprintf("%x", sha256.Sum256(corpus.Bytes()))
				writeIssuesJSONL(t, dir, corpus.String())
				indexHash := ""
				for _, configuration := range []string{"text", "default", "bug-hunting", "sprint-planning", "impact-first"} {
					actualNDCG, brokenNDCG := 0.0, 0.0
					for _, query := range fixture.Queries {
						args := []string{"--robot-search", "--search", query.Query, "--search-limit", "10", "--search-mode", "text"}
						if configuration != "text" {
							args = []string{"--robot-search", "--search", query.Query, "--search-limit", "10", "--search-mode", "hybrid", "--search-preset", configuration}
						}
						run := runSearch(dir, args...)
						// The CLI rejects blank input before opening an index. Record
						// that explicit input contract, not a fabricated empty JSON
						// search response or a successful retrieval observation.
						exitCode := 0
						if exitErr, ok := run.exit.(*exec.ExitError); ok {
							exitCode = exitErr.ExitCode()
						}
						rejectedBlank := strings.TrimSpace(query.Query) == "" && exitCode == 1 && run.stdout == "" && strings.Contains(run.stderr, "--robot-search requires --search")
						if run.exit != nil && !rejectedBlank {
							t.Fatalf("scale=%d config=%s query=%s argv=%q: %v\nstdout=%s\nstderr=%s", distractors, configuration, query.ID, args, run.exit, run.stdout, run.stderr)
						}
						observation := relevanceObservation{QueryID: query.ID, Split: query.Split, Class: query.Class,
							QueryHash: relevanceHash(struct{ ID, Query, Intent string }{query.ID, query.Query, query.Intent}),
							JudgmentHash: relevanceHash(struct {
								Relevant  map[string]int
								Rationale string
							}{query.Relevant, query.Rationale}),
							CorpusHash: corpusHash, Distractors: distractors, CorpusSize: len(issues), Configuration: configuration, Stderr: run.stderr,
							ExitCode: exitCode, RejectedBlank: rejectedBlank,
							IDs: make([]string, 0, 10)}
						if !rejectedBlank {
							if err := json.Unmarshal([]byte(run.stdout), &observation.Output); err != nil {
								t.Fatal(err)
							}
						}
						out := observation.Output
						if indexHash == "" {
							indexHash = out.IndexDataHash
						}
						if !rejectedBlank && (out.IndexDataHash == "" || out.IndexDataHash != indexHash || out.CandidateHash != indexHash || out.RankingHash == "" || out.Provider != "hash" || out.Dim != 2048 || len(out.Results) > 10) {
							t.Fatalf("missing/inconsistent real CLI identity for %s: %s", query.ID, run.stdout)
						}
						observation.ConfigHash = relevanceHash(struct {
							Arguments              []string
							Mode, Preset, Provider string
							Dim                    int
							Weights                map[string]float64
							Time                   time.Time
						}{args, out.Mode, out.Preset, out.Provider, out.Dim, out.Weights, fixture.ReferenceTime})
						seen := make(map[string]bool)
						for i, result := range out.Results {
							if seen[result.ID] || math.IsNaN(result.Score) || math.IsInf(result.Score, 0) {
								t.Fatalf("invalid ranked result: %+v", result)
							}
							if i > 0 && !(i == 1 && query.ExactID != "" && out.Results[0].ID == query.ExactID) {
								previous := out.Results[i-1]
								if previous.Score < result.Score || (previous.Score == result.Score && previous.ID > result.ID) {
									t.Fatalf("score ordering or deterministic ID tie-break failed: query=%s previous=%+v next=%+v", query.ID, previous, result)
								}
							}
							seen[result.ID] = true
							observation.IDs = append(observation.IDs, result.ID)
						}
						for id := range query.Relevant {
							if !seen[id] {
								observation.MissingIDs = append(observation.MissingIDs, id)
							}
						}
						sort.Strings(observation.MissingIDs)
						if len(query.Relevant) > 0 {
							recall, ndcg := judgedMetrics(observation.IDs, query.Relevant, 10)
							zeroRecall, zeroNDCG := judgedMetrics(zeroIDs, query.Relevant, 10)
							observation.RecallAt10, observation.NDCGAt10 = &recall, &ndcg
							observation.ZeroRecall, observation.ZeroNDCG = &zeroRecall, &zeroNDCG
							if query.Split == "evaluation" {
								actualNDCG += ndcg
								brokenNDCG += zeroNDCG
							}
						}
						if query.ExactID != "" {
							correct := len(observation.IDs) > 0 && observation.IDs[0] == query.ExactID
							observation.ExactIDFirst = &correct
							if !correct {
								t.Errorf("exact-ID contract failed: scale=%d config=%s query=%s want=%s got=%v", distractors, configuration, query.ID, query.ExactID, observation.IDs)
							}
						}
						if query.ExpectEmpty {
							correct := len(observation.IDs) == 0
							observation.EmptyCorrect = &correct
						}
						if query.CLIProbe {
							repeat := runSearch(dir, args...)
							var replay relevanceCLIOutput
							if rejectedBlank {
								if repeatedExit, ok := repeat.exit.(*exec.ExitError); !ok || repeatedExit.ExitCode() != 1 || repeat.stdout != "" || repeat.stderr != run.stderr {
									t.Errorf("blank-query rejection changed: first=%+v second=%+v", run, repeat)
								}
							} else if repeat.exit != nil || json.Unmarshal([]byte(repeat.stdout), &replay) != nil || !reflect.DeepEqual(replay, out) {
								t.Errorf("cached CLI ranking changed: scale=%d config=%s query=%s\nfirst=%s\nsecond=%s\nstderr=%s", distractors, configuration, query.ID, run.stdout, repeat.stdout, repeat.stderr)
							}
						}
						scale.observations = append(scale.observations, observation)
						key := fmt.Sprintf("%d/%s/%s/%s", distractors, configuration, query.Split, query.Class)
						aggregate := scale.byClass[key]
						if aggregate == nil {
							aggregate = &relevanceAggregate{}
							scale.byClass[key] = aggregate
						}
						aggregate.Queries++
						if observation.RecallAt10 != nil {
							aggregate.JudgedQueries++
							aggregate.RecallAt10 += *observation.RecallAt10
							aggregate.NDCGAt10 += *observation.NDCGAt10
							aggregate.ZeroRecall += *observation.ZeroRecall
							aggregate.ZeroNDCG += *observation.ZeroNDCG
						}
						if observation.ExactIDFirst != nil {
							aggregate.ExactQueries++
							if *observation.ExactIDFirst {
								aggregate.ExactSuccesses++
							}
						}
						if observation.EmptyCorrect != nil {
							aggregate.EmptyQueries++
							if *observation.EmptyCorrect {
								aggregate.EmptySuccesses++
							}
						}
						raw, _ := json.Marshal(observation)
						t.Logf("QUERY %s", raw)
					}
					if actualNDCG <= brokenNDCG {
						t.Errorf("zero-score ID-sorted negative control was not worse at scale=%d config=%s: actual=%g broken=%g", distractors, configuration, actualNDCG, brokenNDCG)
					}
				}
			})
		}
	})
	// The parent waits for every parallel scale, then preserves the original
	// scale/configuration/query order in the report regardless of scheduling.
	for _, scale := range scales {
		report.Observations = append(report.Observations, scale.observations...)
		for key, aggregate := range scale.byClass {
			report.ByClass[key] = aggregate
		}
	}
	if len(report.Observations) != 600 {
		t.Fatalf("incomplete evaluation: got %d observations, want 40 queries across 5 configurations and 3 scales", len(report.Observations))
	}
}

func TestRobotSearchScopeAndCache(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()
	lines := []string{
		`{"id":"api-open","title":"Authentication needle","status":"open","priority":2,"issue_type":"task","labels":["backend"]}`,
		`{"id":"api-closed","title":"Authentication","status":"closed","priority":2,"issue_type":"task","labels":["backend"]}`,
		`{"id":"api-defer","title":"Authentication","status":"deferred","priority":2,"issue_type":"task","labels":["backend"]}`,
		`{"id":"api-blocked","title":"Authentication","status":"open","priority":2,"issue_type":"task","labels":["backend"],"dependencies":[{"depends_on_id":"web-root","type":"blocks"}]}`,
		`{"id":"web-root","title":"Authentication needle needle needle needle","status":"open","priority":0,"issue_type":"task","labels":["frontend"]}`,
		`{"id":"ops-root","title":"Authentication","status":"open","priority":2,"issue_type":"task","labels":["ops"]}`,
	}
	writeIssuesJSONL(t, dir, strings.Join(lines, "\n")+"\n")
	type result struct {
		IssueID    string             `json:"issue_id"`
		Components map[string]float64 `json:"component_scores"`
	}
	type output struct {
		GeneratedAt   string         `json:"generated_at"`
		DataHash      string         `json:"data_hash"`
		SourcePath    string         `json:"source_path"`
		SourceKind    string         `json:"source_kind"`
		Scope         map[string]any `json:"scope"`
		CandidateHash string         `json:"candidate_hash"`
		IndexDataHash string         `json:"index_data_hash"`
		RankingHash   string         `json:"ranking_hash"`
		IndexPath     string         `json:"index_path"`
		Loaded        bool           `json:"loaded"`
		Index         struct {
			Total   int `json:"total"`
			Removed int `json:"removed"`
		} `json:"index"`
		Results []result `json:"results"`
	}
	var cacheBytes []byte
	var initial output
	for _, mode := range []string{"text", "hybrid"} {
		for _, tc := range []struct {
			name  string
			flags []string
			want  []string
		}{
			{"all", nil, []string{"api-blocked", "api-closed", "api-defer", "api-open", "ops-root", "web-root"}},
			{"label", []string{"--label", "backend"}, []string{"api-blocked", "api-closed", "api-defer", "api-open"}},
			{"actionable", []string{"--recipe", "actionable"}, []string{"api-open", "ops-root", "web-root"}},
			{"repo", []string{"--repo", "api"}, []string{"api-blocked", "api-closed", "api-defer", "api-open"}},
			{"combined", []string{"--repo", "api", "--label", "backend", "--recipe", "actionable"}, []string{"api-open"}},
			{"empty", []string{"--label", "absent"}, []string{}},
			{"excluded leaders top k", []string{"--label", "backend", "--recipe", "actionable", "--search-limit", "1"}, []string{"api-open"}},
			{"all again", nil, []string{"api-blocked", "api-closed", "api-defer", "api-open", "ops-root", "web-root"}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				args := append([]string{"--search", "Authentication", "--robot-search", "--search-mode", mode}, tc.flags...)
				run := runScoped(t, bv, dir, args...)
				t.Logf("argv=%q exit=%v stdout=%s stderr=%s", args, run.exit, run.stdout, run.stderr)
				if run.exit != nil {
					t.Fatalf("search: %v", run.exit)
				}
				var out output
				if err := json.Unmarshal([]byte(run.stdout), &out); err != nil {
					t.Fatal(err)
				}
				ids := make([]string, 0, len(out.Results))
				for _, item := range out.Results {
					ids = append(ids, item.IssueID)
					if mode == "hybrid" && len(item.Components) == 0 {
						t.Errorf("hybrid result missing components: %+v", item)
					}
				}
				sort.Strings(ids)
				if !reflect.DeepEqual(ids, tc.want) {
					t.Errorf("IDs=%v want %v", ids, tc.want)
				}
				if out.GeneratedAt == "" || out.DataHash == "" || out.SourceKind == "" || out.SourcePath != filepath.Join(dir, ".beads", "issues.jsonl") || out.CandidateHash == "" || out.IndexDataHash == "" || out.RankingHash == "" {
					t.Errorf("incomplete search identity: %+v", out)
				}
				if len(tc.flags) > 0 && len(out.Scope) == 0 {
					t.Error("missing declared scope")
				}
				if out.Index.Total != len(lines) || out.Index.Removed != 0 {
					t.Errorf("scope rewrote full index: %+v", out.Index)
				}
				data, err := os.ReadFile(out.IndexPath)
				if err != nil {
					t.Fatal(err)
				}
				if cacheBytes == nil {
					cacheBytes = data
					initial = out
				} else if !out.Loaded || !bytes.Equal(data, cacheBytes) {
					t.Error("alternating scopes changed the persistent full index")
				}
				if out.IndexDataHash != initial.IndexDataHash {
					t.Error("scope changed full index identity")
				}
			})
		}
	}
	// Source order must not alter ranking/cache identity.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	writeIssuesJSONL(t, dir, strings.Join(lines, "\n")+"\n")
	run := runScoped(t, bv, dir, "--search", "Authentication", "--robot-search", "--search-mode", "text")
	if run.exit != nil {
		t.Fatalf("reordered search: %v %s", run.exit, run.stderr)
	}
	var reordered output
	if err := json.Unmarshal([]byte(run.stdout), &reordered); err != nil {
		t.Fatal(err)
	}
	if reordered.RankingHash != initial.RankingHash || reordered.CandidateHash != initial.CandidateHash || reordered.Index.Removed != 0 {
		t.Errorf("reordered source changed identity: %+v", reordered)
	}
	// Recovery preserves the damaged cache and rebuilds the full corpus even
	// when the request only searches one label.
	corrupt := []byte("not a vector index")
	if err := os.WriteFile(initial.IndexPath, corrupt, 0o644); err != nil {
		t.Fatal(err)
	}
	run = runScoped(t, bv, dir, "--search", "Authentication", "--robot-search", "--label", "backend")
	if run.exit != nil {
		t.Fatalf("malformed cache recovery failed: %+v", run)
	}
	var recovered output
	if err := json.Unmarshal([]byte(run.stdout), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Loaded || recovered.Index.Total != len(lines) || recovered.IndexDataHash != initial.IndexDataHash {
		t.Errorf("scoped recovery did not rebuild the full corpus: %+v", recovered)
	}
	ids := make([]string, 0, len(recovered.Results))
	for _, item := range recovered.Results {
		ids = append(ids, item.IssueID)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"api-blocked", "api-closed", "api-defer", "api-open"}) {
		t.Errorf("recovered results escaped scope: %v", ids)
	}
	after, err := os.ReadFile(initial.IndexPath)
	if err != nil || bytes.Equal(after, corrupt) {
		t.Error("recovery did not write a valid replacement index")
	}
	backups, err := filepath.Glob(initial.IndexPath + ".corrupt-*")
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one preserved corrupt cache, got %v: %v", backups, err)
	}
	preserved, err := os.ReadFile(backups[0])
	if err != nil || !bytes.Equal(preserved, corrupt) {
		t.Error("recovery lost the original corrupt cache bytes")
	}
}

func TestRobotSearchPrefixBeforeCandidateCutoff(t *testing.T) {
	bv := buildBvBinary(t)
	t.Setenv("BV_SEMANTIC_EMBEDDER", "hash")
	t.Setenv("BV_SEMANTIC_DIM", "2048")
	for _, query := range []struct{ prefix, word, title string }{
		{"certif", "certificate", "Certificate rotation"},
		{"normaliz", "normalization", "Normalization of Unicode"},
	} {
		dir := t.TempDir()
		var corpus bytes.Buffer
		for i := 0; i < 400; i++ {
			if err := json.NewEncoder(&corpus).Encode(model.Issue{ID: fmt.Sprintf("a-%04d", i), Title: "Garden inventory", Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2, Labels: []string{"unrelated"}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := json.NewEncoder(&corpus).Encode(model.Issue{ID: "z-target", Title: query.title, Status: model.StatusOpen, IssueType: model.TypeTask, Priority: 2, Labels: []string{"target"}}); err != nil {
			t.Fatal(err)
		}
		writeIssuesJSONL(t, dir, corpus.String())
		for _, mode := range []string{"text", "hybrid"} {
			for _, tc := range []struct {
				name, query string
				flags       []string
				want        string
			}{
				{"prefix", query.prefix, nil, "z-target"},
				{"complete token", query.word, nil, "z-target"},
				{"eligible prefix", query.prefix, []string{"--label", "target"}, "z-target"},
				{"raw threshold excludes prefix", query.prefix, []string{"--label", "target", "--search-min-score", "0.01"}, ""},
				{"scope excludes prefix", query.prefix, []string{"--label", "unrelated"}, "excluded"},
			} {
				t.Run(query.prefix+"/"+mode+"/"+tc.name, func(t *testing.T) {
					args := append([]string{"--robot-search", "--search", tc.query, "--search-mode", mode, "--search-limit", "1"}, tc.flags...)
					run := runScoped(t, bv, dir, args...)
					if run.exit != nil {
						t.Fatalf("argv=%q: %v stdout=%s stderr=%s", args, run.exit, run.stdout, run.stderr)
					}
					var out relevanceCLIOutput
					if err := json.Unmarshal([]byte(run.stdout), &out); err != nil {
						t.Fatal(err)
					}
					switch tc.want {
					case "":
						if len(out.Results) != 0 {
							t.Fatalf("lexical boost bypassed raw threshold: %s", run.stdout)
						}
					case "excluded":
						if len(out.Results) != 1 || out.Results[0].ID == "z-target" {
							t.Fatalf("prefix escaped candidate scope: %s", run.stdout)
						}
					default:
						if len(out.Results) != 1 || out.Results[0].ID != tc.want {
							t.Fatalf("prefix candidate lost before ranking: want=%s stdout=%s", tc.want, run.stdout)
						}
					}
				})
			}
		}
	}
}

func TestRobotSearchExactIDScopeAndThreshold(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, `{"id":"Component/Auth:v2","title":"Unrelated implementation","description":"storage backups migrations disk snapshots replicas","status":"open","priority":2,"issue_type":"task","labels":["backend"]}
{"id":"web-1","title":"Component Auth v2","status":"open","priority":2,"issue_type":"task","labels":["frontend"]}
`)
	for _, mode := range []string{"text", "hybrid"} {
		for _, tc := range []struct {
			name  string
			query string
			flags []string
			want  string
		}{
			{"opaque exact", "Component/Auth:v2", nil, "Component/Auth:v2"},
			{"scoped exact", "Component/Auth:v2", []string{"--label", "backend"}, "Component/Auth:v2"},
			{"case folded", "COMPONENT/AUTH:V2", []string{"--label", "backend"}, "Component/Auth:v2"},
			{"excluded exact", "Component/Auth:v2", []string{"--label", "frontend"}, "web-1"},
			{"threshold excludes exact", "Component/Auth:v2", []string{"--label", "backend", "--search-min-score", "1"}, ""},
			{"inclusive low threshold", "Component/Auth:v2", []string{"--label", "backend", "--search-min-score", "-1"}, "Component/Auth:v2"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				args := append([]string{"--search", tc.query, "--robot-search", "--search-mode", mode, "--search-limit", "1"}, tc.flags...)
				r := runScoped(t, bv, dir, args...)
				if r.exit != nil {
					t.Fatalf("%q: %v %s", args, r.exit, r.stderr)
				}
				var out struct {
					Results []struct {
						ID string `json:"issue_id"`
					} `json:"results"`
				}
				if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
					t.Fatal(err)
				}
				if tc.want == "" {
					if len(out.Results) != 0 {
						t.Fatalf("exact ID bypassed threshold: %s", r.stdout)
					}
				} else if len(out.Results) != 1 || out.Results[0].ID != tc.want {
					t.Fatalf("want only %q: %s", tc.want, r.stdout)
				}
			})
		}
	}
	for _, value := range []string{"NaN", "Inf", "-Inf", "1.001", "-1.001", "invalid"} {
		r := runScoped(t, bv, dir, "--search", "Auth", "--robot-search", "--search-min-score", value)
		exitErr, ok := r.exit.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 2 || !strings.Contains(r.stderr, "--search-min-score") || r.stdout != "" {
			t.Errorf("invalid threshold %q must fail explicitly with exit 2: %+v", value, r)
		}
	}
	r := runScoped(t, bv, dir, "--search-min-score", "0.5")
	if r.exit == nil || !strings.Contains(r.stderr, "--search") {
		t.Errorf("threshold without search must be rejected: %+v", r)
	}
}

func TestRobotSearchCLIOverridesInvalidEnvironment(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()
	writeIssuesJSONL(t, dir, `{"id":"auth-1","title":"Authentication","status":"open","priority":1,"issue_type":"task"}`+"\n")
	weights := `{"text":1,"pagerank":0,"status":0,"impact":0,"priority":0,"recency":0}`
	for _, mode := range []string{"text", "invalid"} {
		t.Setenv("BV_SEARCH_MODE", mode)
		t.Setenv("BV_SEARCH_PRESET", "impact-first")
		t.Setenv("BV_SEARCH_WEIGHTS", "invalid JSON")
		r := runScoped(t, bv, dir, "--search", "Authentication", "--robot-search", "--search-mode", "hybrid", "--search-preset", "bug-hunting", "--search-weights", weights)
		if r.exit != nil {
			t.Fatalf("overridden environment rejected: %+v", r)
		}
		var out struct {
			Mode, Preset string
			Weights      map[string]float64
			Results      []json.RawMessage
		}
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatal(err)
		}
		if out.Mode != "hybrid" || out.Preset != "custom" || out.Weights["text"] != 1 || len(out.Results) != 1 {
			t.Fatalf("flags did not determine effective config: %s", r.stdout)
		}
	}
	// Inherited invalid settings still fail if the caller does not replace them.
	r := runScoped(t, bv, dir, "--search", "Authentication", "--robot-search")
	if r.exit == nil || !strings.Contains(r.stderr, "invalid search mode") {
		t.Errorf("unoverridden invalid mode accepted: %+v", r)
	}
	t.Setenv("BV_SEARCH_MODE", "text")
	t.Setenv("BV_SEARCH_WEIGHTS", "")
	r = runScoped(t, bv, dir, "--search", "Authentication", "--robot-search", "--search-preset", "bug-hunting")
	if r.exit != nil || string(r.payload["mode"]) != `"hybrid"` || string(r.payload["preset"]) != `"bug-hunting"` {
		t.Errorf("explicit preset did not override inherited mode: exit=%v stdout=%s stderr=%s", r.exit, r.stdout, r.stderr)
	}
	t.Setenv("BV_SEARCH_PRESET", "not-a-preset")
	r = runScoped(t, bv, dir, "--search", "Authentication", "--robot-search", "--search-mode", "text")
	if r.exit != nil {
		t.Errorf("explicit text mode rejected irrelevant inherited preset: %+v", r)
	}
}

func TestRobotSearchHistoricalCache(t *testing.T) {
	bv := buildBvBinary(t)
	dir := t.TempDir()
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	old := `{"id":"hist-old","title":"Authentication before change","status":"open","issue_type":"task","priority":2,"labels":["backend"],"updated_at":"2023-11-13T22:13:20Z"}`
	current := `{"id":"hist-new","title":"Authentication after change","status":"open","issue_type":"task","priority":2,"labels":["frontend"]}`
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeIssuesJSONL(t, dir, old+"\n")
	git("init", "-q", "-b", "main")
	git("add", ".beads/issues.jsonl")
	git("commit", "-q", "-m", "historical issue")
	oldSHA := git("rev-parse", "HEAD")
	writeIssuesJSONL(t, dir, old+"\n"+current+"\n")
	git("add", ".beads/issues.jsonl")
	git("commit", "-q", "-m", "current issue")
	type output struct {
		IndexPath   string `json:"index_path"`
		IndexHash   string `json:"index_data_hash"`
		RankingHash string `json:"ranking_hash"`
		RankingTime string `json:"ranking_time"`
		SourceKind  string `json:"source_kind"`
		AsOfCommit  string `json:"as_of_commit"`
		Loaded      bool   `json:"loaded"`
		Index       struct {
			Total int `json:"total"`
		} `json:"index"`
		Results []struct {
			ID         string             `json:"issue_id"`
			Components map[string]float64 `json:"component_scores"`
		} `json:"results"`
	}
	run := func(flags ...string) output {
		t.Helper()
		args := append([]string{"--search", "Authentication", "--robot-search"}, flags...)
		r := runScoped(t, bv, dir, args...)
		if r.exit != nil {
			t.Fatalf("%q: %v %s", args, r.exit, r.stderr)
		}
		var out output
		if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The first request is scoped and cold, but its cache must include both rows.
	live := run("--label", "frontend")
	if live.Loaded || live.Index.Total != 2 || len(live.Results) != 1 || live.Results[0].ID != "hist-new" {
		t.Fatalf("cold scoped index: %+v", live)
	}
	liveBytes, err := os.ReadFile(live.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	var historicalPath string
	for _, mode := range []string{"text", "hybrid"} {
		history := run("--as-of", oldSHA, "--label", "backend", "--search-mode", mode)
		if history.SourceKind != "git" || history.AsOfCommit != oldSHA || history.Index.Total != 1 || len(history.Results) != 1 || history.Results[0].ID != "hist-old" {
			t.Fatalf("historical scope: %+v", history)
		}
		if history.IndexPath == live.IndexPath || history.IndexHash == live.IndexHash || history.RankingHash == live.RankingHash {
			t.Fatalf("historical and live identity overlap: live=%+v history=%+v", live, history)
		}
		if historicalPath != "" && (!history.Loaded || history.IndexPath != historicalPath) {
			t.Fatalf("historical cache was not reused: %+v", history)
		}
		historicalPath = history.IndexPath
		if mode == "hybrid" {
			if history.RankingTime != "2023-11-14T22:13:20Z" || math.Abs(history.Results[0].Components["recency"]-math.Exp(-1.0/30)) > 1e-9 {
				t.Fatalf("hybrid scoring ignored its reported reference clock: %+v", history)
			}
			repeated := run("--as-of", oldSHA, "--label", "backend", "--search-mode", mode)
			if repeated.RankingHash != history.RankingHash || !reflect.DeepEqual(repeated.Results, history.Results) {
				t.Fatalf("pinned historical ranking is not repeatable: before=%+v after=%+v", history, repeated)
			}
		}
		after, err := os.ReadFile(live.IndexPath)
		if err != nil || !bytes.Equal(after, liveBytes) {
			t.Fatal("historical search rewrote the live index")
		}
	}
	all := run()
	if !all.Loaded || all.Index.Total != 2 || len(all.Results) != 2 || all.IndexHash != live.IndexHash {
		t.Fatalf("live full corpus after historical search: %+v", all)
	}
	missing := run("--as-of", oldSHA, "--label", "frontend")
	if len(missing.Results) != 0 || missing.Index.Total != 1 {
		t.Fatalf("empty historical scope widened: %+v", missing)
	}
}

func TestRobotSearchContract(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()
	// Use a very distinctive token with many repeats to make hashed-vector ranking stable.
	writeBeads(t, env, `{"id":"A","title":"Semantic search target","description":"interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken interstellarkraken","status":"open","priority":1,"issue_type":"task"}
{"id":"B","title":"Unrelated docs","description":"readme changelog docs","status":"open","priority":2,"issue_type":"task"}`)

	cmd := exec.Command(bv, "--search", "interstellarkraken", "--robot-search")
	cmd.Dir = env
	cmd.Env = append(os.Environ(),
		"BV_SEMANTIC_EMBEDDER=hash",
		"BV_SEMANTIC_DIM=2048",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("robot-search failed: %v\n%s", err, out)
	}

	var payload struct {
		GeneratedAt string `json:"generated_at"`
		DataHash    string `json:"data_hash"`
		Query       string `json:"query"`
		Provider    string `json:"provider"`
		Dim         int    `json:"dim"`
		IndexPath   string `json:"index_path"`
		Loaded      bool   `json:"loaded"`
		Limit       int    `json:"limit"`
		Index       struct {
			Total    int `json:"total"`
			Added    int `json:"added"`
			Updated  int `json:"updated"`
			Removed  int `json:"removed"`
			Skipped  int `json:"skipped"`
			Embedded int `json:"embedded"`
		} `json:"index"`
		Results []struct {
			IssueID string  `json:"issue_id"`
			Score   float64 `json:"score"`
			Title   string  `json:"title"`
		} `json:"results"`
		UsageHints []string `json:"usage_hints"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("robot-search json decode: %v\nout=%s", err, out)
	}

	if payload.GeneratedAt == "" || payload.DataHash == "" {
		t.Fatalf("robot-search missing metadata: generated_at=%q data_hash=%q", payload.GeneratedAt, payload.DataHash)
	}
	if payload.Query != "interstellarkraken" {
		t.Fatalf("unexpected query: %q", payload.Query)
	}
	if payload.Provider != "hash" {
		t.Fatalf("unexpected provider: %q", payload.Provider)
	}
	if payload.Dim != 2048 {
		t.Fatalf("unexpected dim: %d", payload.Dim)
	}
	if payload.IndexPath == "" {
		t.Fatalf("missing index_path")
	}
	if payload.Limit <= 0 {
		t.Fatalf("missing/invalid limit: %d", payload.Limit)
	}
	if len(payload.Results) == 0 {
		t.Fatalf("expected at least one result")
	}
	if payload.Results[0].IssueID != "A" {
		t.Fatalf("expected top match A, got %s (%+v)", payload.Results[0].IssueID, payload.Results)
	}
	if len(payload.UsageHints) == 0 {
		t.Fatalf("expected usage_hints")
	}
}
