package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/baseline"
	"github.com/Dicklesworthstone/beads_viewer/pkg/hooks"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"pgregory.net/rapid"
)

// Metamorphic Relation Strength Matrix (Fault Sensitivity × Independence / Cost ≥ 2.0):
//
// | ID   | Relation                                                        | F | I | C | Score | Target Defect Class                                 |
// |------|-----------------------------------------------------------------|---|---|---|-------|-----------------------------------------------------|
// | MR-1 | Graph, JSONL & Dependency-Slice Permutation Equivalence         | 5 | 5 | 1 | 25.0  | Map/slice-order tie-break non-determinism           |
// | MR-2 | Scope Containment & Out-of-Scope Readiness Non-Promotion        | 5 | 5 | 1 | 25.0  | Missing IsCandidate / cross-scope blocker promotion |
// | MR-3 | Work-Scale Equivariance, Graham Monotonicity & Archive Invar.   | 5 | 4 | 1 | 20.0  | ETA unit mismatch & closed-history score compression|
// | MR-4 | Exact Non-Tombstone Status-Partition Conservation Law           | 4 | 5 | 1 | 20.0  | Status bucket overlap/drop across triage/plan/drift |
// | MR-5 | Subdirectory CWD & Clean-HEAD --as-of Isomorphism (also E2E)    | 4 | 4 | 2 |  8.0  | Subdirectory os.Getwd() resolution drift            |

var metamorphicFixedNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

type metamorphicFailureDiagnostic struct {
	Relation          string         `json:"relation"`
	GraphJSON         string         `json:"shrunk_graph_json"`
	Scope             []string       `json:"scope,omitempty"`
	ExpectedRelation  string         `json:"expected_relation"`
	ObservedViolation string         `json:"observed_violation"`
	Extra             map[string]any `json:"extra,omitempty"`
}

func failMetamorphic(rt *rapid.T, relation string, issues []model.Issue, scope map[string]bool, expected, observed string, extra map[string]any) {
	rt.Helper()
	graphBytes, _ := json.Marshal(issues)
	var scopeList []string
	for id, ok := range scope {
		if ok {
			scopeList = append(scopeList, id)
		}
	}
	sort.Strings(scopeList)
	diag := metamorphicFailureDiagnostic{
		Relation:          relation,
		GraphJSON:         string(graphBytes),
		Scope:             scopeList,
		ExpectedRelation:  expected,
		ObservedViolation: observed,
		Extra:             extra,
	}
	payload, _ := json.Marshal(diag)
	rt.Fatalf("METAMORPHIC_VIOLATION %s", payload)
}

func drawMetamorphicGraph(rt *rapid.T, minNodes, maxNodes int, acyclicOnly bool, includeTombstone bool) []model.Issue {
	n := rapid.IntRange(minNodes, maxNodes).Draw(rt, "nodeCount")
	statuses := []model.Status{
		model.StatusOpen,
		model.StatusInProgress,
		model.StatusBlocked,
		model.StatusDeferred,
		model.StatusPinned,
		model.StatusDraft,
		model.StatusHooked,
		model.StatusReview,
		model.StatusClosed,
	}
	if includeTombstone {
		statuses = append(statuses, model.StatusTombstone)
	}
	types := []model.IssueType{
		model.TypeTask,
		model.TypeBug,
		model.TypeFeature,
		model.TypeChore,
		model.TypeEpic,
	}
	labelPool := []string{"backend", "frontend", "urgent", "security", "docs"}
	repoPool := []string{"api", "web", "cli"}
	depTypes := []model.DependencyType{
		model.DepBlocks,
		model.DepParentChild,
		model.DepRelated,
		model.DepDiscoveredFrom,
	}

	issues := make([]model.Issue, n)
	twinOf := make([]int, n)
	for i := range n {
		twinOf[i] = -1
		id := fmt.Sprintf("ISS-%02d", i+1)
		if i > 0 && twinOf[i-1] < 0 &&
			issues[i-1].Status != model.StatusClosed &&
			issues[i-1].Status != model.StatusTombstone &&
			rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("twin_%d", i)) == 0 {
			twinOf[i] = i - 1
			prev := issues[i-1]
			var clonedLabels []string
			if len(prev.Labels) > 0 {
				clonedLabels = append([]string(nil), prev.Labels...)
			}
			var clonedClosed *time.Time
			if prev.ClosedAt != nil {
				c := *prev.ClosedAt
				clonedClosed = &c
			}
			var clonedDefer *time.Time
			if prev.DeferUntil != nil {
				d := *prev.DeferUntil
				clonedDefer = &d
			}
			var clonedEst *int
			if prev.EstimatedMinutes != nil {
				e := *prev.EstimatedMinutes
				clonedEst = &e
			}
			issues[i] = model.Issue{
				ID:               id,
				Title:            fmt.Sprintf("Task %s in %s", id, prev.SourceRepo),
				Description:      prev.Description,
				Status:           prev.Status,
				Priority:         prev.Priority,
				IssueType:        prev.IssueType,
				Labels:           clonedLabels,
				SourceRepo:       prev.SourceRepo,
				CreatedAt:        prev.CreatedAt,
				UpdatedAt:        prev.UpdatedAt,
				ClosedAt:         clonedClosed,
				DeferUntil:       clonedDefer,
				EstimatedMinutes: clonedEst,
			}
			continue
		}

		st := statuses[rapid.IntRange(0, len(statuses)-1).Draw(rt, fmt.Sprintf("status_%d", i))]
		it := types[rapid.IntRange(0, len(types)-1).Draw(rt, fmt.Sprintf("type_%d", i))]
		prio := rapid.IntRange(0, 4).Draw(rt, fmt.Sprintf("prio_%d", i))
		// Draw from discrete timestamp buckets so generated graphs naturally produce
		// exact ImpactScore ties across equal-priority peers, stressing tie-break determinism.
		createdOffsetHours := rapid.SampledFrom([]int{48, 120, 360, 720}).Draw(rt, fmt.Sprintf("created_%d", i))
		updatedOffsetHours := rapid.SampledFrom([]int{2, 24, 48}).Draw(rt, fmt.Sprintf("updated_%d", i))
		createdAt := metamorphicFixedNow.Add(-time.Duration(createdOffsetHours) * time.Hour)
		updatedAt := metamorphicFixedNow.Add(-time.Duration(updatedOffsetHours) * time.Hour)

		var closedAt *time.Time
		if st == model.StatusClosed {
			c := updatedAt
			closedAt = &c
		}

		var deferUntil *time.Time
		switch rapid.IntRange(0, 4).Draw(rt, fmt.Sprintf("defer_mode_%d", i)) {
		case 1:
			future := metamorphicFixedNow.Add(48 * time.Hour)
			deferUntil = &future
		case 2:
			past := metamorphicFixedNow.Add(-48 * time.Hour)
			deferUntil = &past
		}

		var est *int
		if rapid.Bool().Draw(rt, fmt.Sprintf("has_est_%d", i)) {
			// Include intentional ties across issues to stress tie-break determinism.
			minutes := rapid.SampledFrom([]int{30, 60, 120, 240}).Draw(rt, fmt.Sprintf("est_%d", i))
			est = &minutes
		}

		var labels []string
		for _, lbl := range labelPool {
			if rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("lbl_%d_%s", i, lbl)) == 0 {
				labels = append(labels, lbl)
			}
		}

		descLen := rapid.SampledFrom([]int{0, 200, 1000}).Draw(rt, fmt.Sprintf("desc_len_%d", i))
		repoPrefix := repoPool[rapid.IntRange(0, len(repoPool)-1).Draw(rt, fmt.Sprintf("repo_%d", i))]

		issues[i] = model.Issue{
			ID:               id,
			Title:            fmt.Sprintf("Task %s in %s", id, repoPrefix),
			Description:      strings.Repeat("x", descLen),
			Status:           st,
			Priority:         prio,
			IssueType:        it,
			Labels:           labels,
			SourceRepo:       repoPrefix,
			CreatedAt:        createdAt,
			UpdatedAt:        updatedAt,
			ClosedAt:         closedAt,
			DeferUntil:       deferUntil,
			EstimatedMinutes: est,
		}
	}

	twinTarget := make([]int, n)
	for i := range n {
		twinTarget[i] = -1
	}
	for i := range n {
		if twinOf[i] >= 0 {
			twinTarget[twinOf[i]] = i
		}
	}

	// Draw directed dependencies (both acyclic DAGs and cyclic graphs when !acyclicOnly).
	for i := range n {
		if src := twinOf[i]; src >= 0 {
			var clonedDeps []*model.Dependency
			for _, dep := range issues[src].Dependencies {
				if dep.DependsOnID == issues[i].ID {
					continue
				}
				clonedDeps = append(clonedDeps, &model.Dependency{
					IssueID:     issues[i].ID,
					DependsOnID: dep.DependsOnID,
					Type:        dep.Type,
				})
			}
			issues[i].Dependencies = clonedDeps
			continue
		}
		var deps []*model.Dependency
		addedTarget := make(map[string]bool)
		for j := range n {
			if i == j || twinOf[j] >= 0 {
				continue
			}
			if acyclicOnly && j <= i {
				continue
			}
			if rapid.IntRange(0, 4).Draw(rt, fmt.Sprintf("edge_%d_%d", i, j)) == 0 {
				dt := depTypes[rapid.IntRange(0, len(depTypes)-1).Draw(rt, fmt.Sprintf("dep_type_%d_%d", i, j))]
				if acyclicOnly && dt == model.DepParentChild && j <= i {
					continue
				}
				if !addedTarget[issues[j].ID] {
					addedTarget[issues[j].ID] = true
					deps = append(deps, &model.Dependency{
						IssueID:     issues[i].ID,
						DependsOnID: issues[j].ID,
						Type:        dt,
					})
				}
				if mate := twinTarget[j]; mate >= 0 && mate != i && (!acyclicOnly || mate > i) {
					if !addedTarget[issues[mate].ID] {
						addedTarget[issues[mate].ID] = true
						deps = append(deps, &model.Dependency{
							IssueID:     issues[i].ID,
							DependsOnID: issues[mate].ID,
							Type:        dt,
						})
					}
				}
			}
		}
		issues[i].Dependencies = deps
	}
	return issues
}

func permuteIssuesAndDeps(rt *rapid.T, issues []model.Issue) []model.Issue {
	cloned := make([]model.Issue, len(issues))
	for i := range issues {
		cloned[i] = issues[i].Clone()
		if len(cloned[i].Dependencies) > 1 {
			perm := rapid.Permutation(cloned[i].Dependencies).Draw(rt, fmt.Sprintf("dep_perm_%d", i))
			cloned[i].Dependencies = perm
		}
	}
	if len(cloned) > 1 {
		cloned = rapid.Permutation(cloned).Draw(rt, "issue_perm")
	}
	return cloned
}

func analyzeUncached(issues []model.Issue, readiness *model.ReadinessIndex, candidateIDs map[string]bool) (*Analyzer, *GraphStats) {
	a := NewAnalyzer(issues)
	a.SetNow(metamorphicFixedNow)
	if readiness != nil || candidateIDs != nil {
		a.SetReadinessScope(readiness, candidateIDs)
	}
	cfg := FullAnalysisConfig()
	cfg.DisableCache = true
	statsPtr := a.AnalyzeAsyncWithConfig(context.Background(), cfg)
	statsPtr.WaitForPhase2()
	return a, statsPtr
}

func approxFloatMapEqual(a, b map[string]float64, tol float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || math.Abs(va-vb) > tol {
			return false
		}
	}
	return true
}

func checkMR1PermutationEquivalence(rt *rapid.T, mutantUnsortedTieBreak bool) {
	issues := drawMetamorphicGraph(rt, 3, 16, false, false)
	permuted := permuteIssuesAndDeps(rt, issues)

	hashA := ComputeDataHash(issues)
	hashB := ComputeDataHash(permuted)
	if hashA != hashB {
		failMetamorphic(rt, "MR-1", issues, nil, "ComputeDataHash(G) == ComputeDataHash(pi(G))",
			fmt.Sprintf("hashA=%s hashB=%s", hashA, hashB), nil)
	}

	anA, statsA := analyzeUncached(issues, nil, nil)
	anB, statsB := analyzeUncached(permuted, nil, nil)

	if anA.DataHash() != anB.DataHash() || anA.DataHash() != hashA {
		failMetamorphic(rt, "MR-1", issues, nil, "Analyzer.DataHash permutation invariance",
			fmt.Sprintf("anA=%s anB=%s", anA.DataHash(), anB.DataHash()), nil)
	}

	// Phase 1 exact equivalence
	if statsA.NodeCount != statsB.NodeCount || statsA.EdgeCount != statsB.EdgeCount ||
		math.Abs(statsA.Density-statsB.Density) > 1e-12 ||
		!reflect.DeepEqual(statsA.OutDegree, statsB.OutDegree) ||
		!reflect.DeepEqual(statsA.InDegree, statsB.InDegree) ||
		!reflect.DeepEqual(statsA.TopologicalOrder, statsB.TopologicalOrder) {
		failMetamorphic(rt, "MR-1", issues, nil, "Phase 1 metrics & topological order permutation invariance",
			fmt.Sprintf("topoA=%v topoB=%v", statsA.TopologicalOrder, statsB.TopologicalOrder), nil)
	}

	// Phase 2 rank & structural equivalence
	if !reflect.DeepEqual(statsA.PageRankRank(), statsB.PageRankRank()) ||
		!reflect.DeepEqual(statsA.BetweennessRank(), statsB.BetweennessRank()) ||
		!reflect.DeepEqual(statsA.EigenvectorRank(), statsB.EigenvectorRank()) ||
		!reflect.DeepEqual(statsA.CoreNumber(), statsB.CoreNumber()) ||
		!reflect.DeepEqual(statsA.ArticulationPoints(), statsB.ArticulationPoints()) ||
		!reflect.DeepEqual(statsA.Cycles(), statsB.Cycles()) {
		failMetamorphic(rt, "MR-1", issues, nil, "Phase 2 rank vectors, k-core, articulation & SCC cycles invariance",
			fmt.Sprintf("cyclesA=%v cyclesB=%v prRankA=%v prRankB=%v", statsA.Cycles(), statsB.Cycles(), statsA.PageRankRank(), statsB.PageRankRank()), nil)
	}

	if !approxFloatMapEqual(statsA.PageRank(), statsB.PageRank(), 1e-12) ||
		!approxFloatMapEqual(statsA.Betweenness(), statsB.Betweenness(), 1e-12) ||
		!approxFloatMapEqual(statsA.Eigenvector(), statsB.Eigenvector(), 1e-12) ||
		!approxFloatMapEqual(statsA.CriticalPathScore(), statsB.CriticalPathScore(), 1e-12) ||
		!approxFloatMapEqual(statsA.Slack(), statsB.Slack(), 1e-12) {
		failMetamorphic(rt, "MR-1", issues, nil, "Phase 2 floating-point centralities invariance",
			"float centrality maps diverged beyond 1e-12", nil)
	}

	planA := anA.GetExecutionPlan()
	planB := anB.GetExecutionPlan()
	if !reflect.DeepEqual(planA, planB) {
		failMetamorphic(rt, "MR-1", issues, nil, "GetExecutionPlan(G) == GetExecutionPlan(pi(G))",
			fmt.Sprintf("planA=%+v planB=%+v", planA, planB), nil)
	}

	recsA := anA.GenerateRecommendationsFromStats(statsA, DefaultThresholds())
	recsB := anB.GenerateRecommendationsFromStats(statsB, DefaultThresholds())
	if !reflect.DeepEqual(recsA, recsB) {
		failMetamorphic(rt, "MR-1", issues, nil, "GenerateRecommendationsFromStats permutation invariance",
			fmt.Sprintf("recsA=%+v recsB=%+v", recsA, recsB), nil)
	}

	// Score ranking order under G vs pi(G): production breaks ImpactScore ties by IssueID;
	// Mutant M1 omits the secondary IssueID tie-break (preserving input slice order on equal scores),
	// and is killed purely by the MR-1 relation rankOrder(G) == rankOrder(pi(G)).
	rankImpactIDs := func(an *Analyzer, st *GraphStats, rawInput []model.Issue) []string {
		scores := an.ComputeImpactScoresFromStats(st, metamorphicFixedNow)
		if mutantUnsortedTieBreak {
			scoreByID := make(map[string]float64, len(scores))
			for _, sc := range scores {
				scoreByID[sc.IssueID] = sc.Score
			}
			var ordered []ImpactScore
			for _, iss := range rawInput {
				if s, ok := scoreByID[iss.ID]; ok {
					ordered = append(ordered, ImpactScore{IssueID: iss.ID, Score: s})
				}
			}
			sort.SliceStable(ordered, func(i, j int) bool {
				return ordered[i].Score > ordered[j].Score
			})
			scores = ordered
		}
		ids := make([]string, len(scores))
		for i := range scores {
			ids[i] = scores[i].IssueID
		}
		return ids
	}
	ordA := rankImpactIDs(anA, statsA, issues)
	ordB := rankImpactIDs(anB, statsB, permuted)
	if !reflect.DeepEqual(ordA, ordB) {
		failMetamorphic(rt, "MR-1", issues, nil, "ImpactScore ranking IDs must be identical under input permutation",
			fmt.Sprintf("ordA=%v ordB=%v", ordA, ordB), nil)
	}

	for _, actionableOnly := range []bool{true, false} {
		trA := ComputeTriageFromAnalyzer(anA, statsA, issues, TriageOptions{
			WaitForPhase2:  true,
			ActionableOnly: actionableOnly,
		}, metamorphicFixedNow)
		trB := ComputeTriageFromAnalyzer(anB, statsB, permuted, TriageOptions{
			WaitForPhase2:  true,
			ActionableOnly: actionableOnly,
		}, metamorphicFixedNow)
		if len(trA.Recommendations) != len(trB.Recommendations) {
			failMetamorphic(rt, "MR-1", issues, nil, "Triage recommendation count invariance",
				fmt.Sprintf("lenA=%d lenB=%d", len(trA.Recommendations), len(trB.Recommendations)), nil)
		}
		for i := range trA.Recommendations {
			ra, rb := trA.Recommendations[i], trB.Recommendations[i]
			if ra.ID != rb.ID || ra.Claimable != rb.Claimable || math.Abs(ra.Score-rb.Score) > 1e-12 ||
				!reflect.DeepEqual(ra.UnblocksIDs, rb.UnblocksIDs) {
				failMetamorphic(rt, "MR-1", issues, nil, "Triage recommendation sequence & score invariance",
					fmt.Sprintf("idx=%d recA=%+v recB=%+v", i, ra, rb), nil)
			}
		}
		if !reflect.DeepEqual(trA.BlockersToClear, trB.BlockersToClear) {
			failMetamorphic(rt, "MR-1", issues, nil, "Triage BlockersToClear permutation invariance",
				fmt.Sprintf("btcA=%+v btcB=%+v", trA.BlockersToClear, trB.BlockersToClear), nil)
		}
	}
}

// TestMetamorphic_MR1_PermutationEquivalence verifies Score 25.0 equivalence under
// arbitrary JSONL issue-slice and dependency-slice permutations.
func TestMetamorphic_MR1_PermutationEquivalence(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		checkMR1PermutationEquivalence(rt, false)
	})
}

func checkMR2ScopeContainmentAndNonPromotion(rt *rapid.T, mutantDropScopeAndReadiness bool) {
	issues := drawMetamorphicGraph(rt, 4, 16, false, false)
	authority := model.NewReadinessIndex(issues)

	scope := make(map[string]bool, len(issues))
	var scopedSlice []model.Issue
	for _, iss := range issues {
		if rapid.Bool().Draw(rt, "in_scope_"+iss.ID) {
			scope[iss.ID] = true
			scopedSlice = append(scopedSlice, iss.Clone())
		}
	}

	fullAn, _ := analyzeUncached(issues, authority, nil)
	fullReady := make(map[string]bool)
	for _, iss := range fullAn.GetActionableIssues() {
		fullReady[iss.ID] = true
	}
	fullReadyLeaf := fullAn.CandidateIssueSet(SelectionReadyLeaf)

	var scopedAn *Analyzer
	var scopedStats *GraphStats
	if mutantDropScopeAndReadiness {
		// Mutant M2 (516e96aa defect): constructs the analyzer on the pre-sliced
		// scoped subset alone without retaining full-graph ReadinessIndex or CandidateIDs,
		// causing out-of-scope open blockers to disappear and promote blocked issues!
		scopedAn, scopedStats = analyzeUncached(scopedSlice, nil, nil)
	} else {
		scopedAn, scopedStats = analyzeUncached(issues, authority, scope)
	}

	// 1. Readiness Non-Promotion: Ready(G, S) == Ready(G) ∩ S
	scopedReady := make(map[string]bool)
	for _, iss := range scopedAn.GetActionableIssues() {
		scopedReady[iss.ID] = true
		if !scope[iss.ID] {
			failMetamorphic(rt, "MR-2", issues, scope, "IDs(Ready(G, S)) ⊆ S",
				fmt.Sprintf("out-of-scope issue %s appeared in GetActionableIssues", iss.ID), nil)
		}
		if !fullReady[iss.ID] {
			failMetamorphic(rt, "MR-2", issues, scope, "Ready(G, S) ⊆ Ready(G)",
				fmt.Sprintf("issue %s promoted to ready under scope restriction", iss.ID), nil)
		}
	}
	for id := range fullReady {
		if scope[id] && !scopedReady[id] {
			failMetamorphic(rt, "MR-2", issues, scope, "Ready(G) ∩ S ⊆ Ready(G, S)",
				fmt.Sprintf("globally ready in-scope issue %s lost readiness", id), nil)
		}
	}

	// 2. Priority & WhatIf Containment and Actionable-Default Readiness:
	// Default includeInactive=false must satisfy IDs(EnhancedPriority(G, S)) ⊆ ReadyLeaf(G) ∩ S.
	recs := scopedAn.GenerateRecommendationsFromStats(scopedStats, DefaultThresholds())
	for _, rec := range recs {
		if !scope[rec.IssueID] {
			failMetamorphic(rt, "MR-2", issues, scope, "IDs(GenerateRecommendations(G, S)) ⊆ S",
				fmt.Sprintf("out-of-scope issue %s leaked into recommendations", rec.IssueID), nil)
		}
	}
	enhanced := scopedAn.GenerateEnhancedRecommendationsFromStats(scopedStats, DefaultThresholds(), 0, false)
	for _, rec := range enhanced {
		if !scope[rec.IssueID] {
			failMetamorphic(rt, "MR-2", issues, scope, "IDs(EnhancedPriority(G, S)) ⊆ S",
				fmt.Sprintf("out-of-scope issue %s leaked into priority recommendations", rec.IssueID), nil)
		}
		if !fullReadyLeaf[rec.IssueID] {
			failMetamorphic(rt, "MR-2", issues, scope, "Default EnhancedPriority(G, S) ⊆ ReadyLeaf(G) ∩ S",
				fmt.Sprintf("non-ready/container issue %s leaked into default priority recommendations", rec.IssueID), nil)
		}
		if rec.Explanation.WhatIf != nil {
			for _, unb := range rec.Explanation.WhatIf.UnblockedIssueIDs {
				if !scope[unb] {
					failMetamorphic(rt, "MR-2", issues, scope, "IDs(what_if.unblocked_issue_ids) ⊆ S",
						fmt.Sprintf("out-of-scope issue %s leaked into what_if for %s", unb, rec.IssueID), nil)
				}
			}
		}
	}

	// 3. Plan Containment & Non-Promotion
	plan := scopedAn.GetExecutionPlan()
	for _, tr := range plan.Tracks {
		for _, item := range tr.Items {
			if !scope[item.ID] || !fullReady[item.ID] {
				failMetamorphic(rt, "MR-2", issues, scope, "Plan items ⊆ Ready(G) ∩ S",
					fmt.Sprintf("invalid plan item %s (inScope=%v fullReady=%v)", item.ID, scope[item.ID], fullReady[item.ID]), nil)
			}
			for _, unb := range item.UnblocksIDs {
				if !scope[unb] {
					failMetamorphic(rt, "MR-2", issues, scope, "Plan item unblocks ⊆ S",
						fmt.Sprintf("out-of-scope unblock %s in plan item %s", unb, item.ID), nil)
				}
			}
		}
	}

	// 4. Triage Containment & Non-Promotion + Compound (MR-1 ∘ MR-2) Permutation-under-Scope
	triageScoped := ComputeTriageFromAnalyzer(scopedAn, scopedStats, issues, TriageOptions{
		WaitForPhase2:  true,
		ActionableOnly: true,
		Readiness:      authority,
		CandidateIDs:   scope,
	}, metamorphicFixedNow)
	for _, rec := range triageScoped.Recommendations {
		if !scope[rec.ID] {
			failMetamorphic(rt, "MR-2", issues, scope, "IDs(Triage(G, S)) ⊆ S",
				fmt.Sprintf("out-of-scope recommendation %s", rec.ID), nil)
		}
		if !fullReady[rec.ID] {
			failMetamorphic(rt, "MR-2", issues, scope, "Default Triage(G, S) recommendations ⊆ Ready(G) ∩ S",
				fmt.Sprintf("non-ready issue %s promoted in scoped triage", rec.ID), nil)
		}
		for _, unb := range rec.UnblocksIDs {
			if !scope[unb] {
				failMetamorphic(rt, "MR-2", issues, scope, "Triage rec.UnblocksIDs ⊆ S",
					fmt.Sprintf("out-of-scope unblock %s on %s", unb, rec.ID), nil)
			}
		}
	}

	// 5. Diff Containment over candidate-restricted snapshots
	prevSnap := NewSnapshotAt(scopedSlice, metamorphicFixedNow.Add(-time.Hour), "HEAD~1")
	currSnap := NewSnapshotAt(scopedSlice, metamorphicFixedNow, "HEAD")
	diff := CompareSnapshots(prevSnap, currSnap)
	for _, iss := range append(append(diff.NewIssues, diff.ClosedIssues...), diff.RemovedIssues...) {
		if !scope[iss.ID] {
			failMetamorphic(rt, "MR-2", issues, scope, "IDs(Diff(G, S)) ⊆ S",
				fmt.Sprintf("out-of-scope issue %s in diff", iss.ID), nil)
		}
	}

	// Compound check MR-1 ∘ MR-2: permuting G then scoping to S preserves scoped triage IDs.
	permIssues := permuteIssuesAndDeps(rt, issues)
	permAn, permStats := analyzeUncached(permIssues, model.NewReadinessIndex(permIssues), scope)
	triagePerm := ComputeTriageFromAnalyzer(permAn, permStats, permIssues, TriageOptions{
		WaitForPhase2:  true,
		ActionableOnly: true,
		Readiness:      permAn.Readiness(),
		CandidateIDs:   scope,
	}, metamorphicFixedNow)
	if len(triageScoped.Recommendations) != len(triagePerm.Recommendations) {
		failMetamorphic(rt, "MR-1∘MR-2", issues, scope, "Scoped triage count invariant under input permutation",
			fmt.Sprintf("lenScoped=%d lenPerm=%d", len(triageScoped.Recommendations), len(triagePerm.Recommendations)), nil)
	}
	for i := range triageScoped.Recommendations {
		if triageScoped.Recommendations[i].ID != triagePerm.Recommendations[i].ID {
			failMetamorphic(rt, "MR-1∘MR-2", issues, scope, "Scoped triage ordering invariant under input permutation",
				fmt.Sprintf("idx=%d idA=%s idB=%s", i, triageScoped.Recommendations[i].ID, triagePerm.Recommendations[i].ID), nil)
		}
	}
}

// TestMetamorphic_MR2_ScopeContainmentAndReadinessNonPromotion verifies Score 25.0
// subset containment and non-promotion of out-of-scope blocked issues.
func TestMetamorphic_MR2_ScopeContainmentAndReadinessNonPromotion(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		checkMR2ScopeContainmentAndNonPromotion(rt, false)
	})
}

func checkMR3WorkScaleAndGrahamAndArchiveInvariance(rt *rapid.T, mutantRawVelocity bool, mutantClosedMaxCompression bool) {
	// Part A: Work-Scale Equivariance (T_hat(alpha * w) == T_hat(w) within 1e-9).
	// Generate DAG with >=1 open issue and >=1 recently-closed issue, using base estimates
	// aligned to 1000m so integer complexity conversion is exact across alpha in {0.5, 2, 5, 10}.
	dag := drawMetamorphicGraph(rt, 4, 12, true, false)
	closedAt := metamorphicFixedNow.Add(-5 * 24 * time.Hour)
	for i := range dag {
		units := rapid.IntRange(1, 8).Draw(rt, fmt.Sprintf("work_units_%d", i))
		mins := units * 1000
		dag[i].EstimatedMinutes = &mins
		dag[i].Description = strings.Repeat("z", rapid.SampledFrom([]int{0, 1000, 2000}).Draw(rt, fmt.Sprintf("exact_desc_%d", i)))
		if i == 0 {
			dag[i].Status = model.StatusOpen
			dag[i].ClosedAt = nil
		} else if i == len(dag)-1 {
			dag[i].Status = model.StatusClosed
			dag[i].ClosedAt = &closedAt
			dag[i].IssueType = model.TypeFeature
			dag[i].Description = strings.Repeat("z", 1000)
		}
	}

	_, baseStats := analyzeUncached(dag, nil, nil)
	agents := rapid.IntRange(1, 6).Draw(rt, "eta_agents")
	alpha := rapid.SampledFrom([]float64{0.5, 2.0, 5.0, 10.0}).Draw(rt, "alpha")

	scaled := make([]model.Issue, len(dag))
	for i := range dag {
		scaled[i] = dag[i].Clone()
		v := int(math.Round(float64(*dag[i].EstimatedMinutes) * alpha))
		scaled[i].EstimatedMinutes = &v
	}
	_, scaledStats := analyzeUncached(scaled, nil, nil)

	for _, iss := range dag {
		if iss.Status == model.StatusClosed {
			continue
		}
		etaBase, err := EstimateETAForIssue(dag, baseStats, iss.ID, agents, metamorphicFixedNow)
		if err != nil {
			rt.Fatalf("EstimateETAForIssue base error: %v", err)
		}
		etaScaled, err := EstimateETAForIssue(scaled, scaledStats, iss.ID, agents, metamorphicFixedNow)
		if err != nil {
			rt.Fatalf("EstimateETAForIssue scaled error: %v", err)
		}

		observedDays := etaBase.EstimatedDays

		// estimateComplexityMinutes truncates derived minutes via int(...), so IEEE-754 boundary products
		// (e.g. 1000 * 1.3 * 1.4 = 1819.9999999999998 -> 1819 vs 500 * 1.3 * 1.4 = 910) can differ by at most
		// 1 integer minute per issue. Assert exact linear scaling up to that 1-minute quantization quantum.
		minQuantum := math.Ceil(alpha) + 1.0
		if math.Abs(float64(etaScaled.EstimatedMinutes)-alpha*float64(etaBase.EstimatedMinutes)) > minQuantum+1e-9 {
			failMetamorphic(rt, "MR-3", dag, nil,
				"|EstimatedMinutes(alpha * w) - alpha * EstimatedMinutes(w)| <= ceil(alpha)+1",
				fmt.Sprintf("issue=%s alpha=%g baseMin=%d scaledMin=%d", iss.ID, alpha, etaBase.EstimatedMinutes, etaScaled.EstimatedMinutes), nil)
		}
		if math.Abs(etaScaled.VelocityMinutesPerDay-alpha*etaBase.VelocityMinutesPerDay) > minQuantum+1e-6 {
			failMetamorphic(rt, "MR-3", dag, nil,
				"|Velocity(alpha * w) - alpha * Velocity(w)| <= ceil(alpha)+1",
				fmt.Sprintf("issue=%s alpha=%g baseVel=%.9f scaledVel=%.9f", iss.ID, alpha, etaBase.VelocityMinutesPerDay, etaScaled.VelocityMinutesPerDay), nil)
		}
		numMin := math.Max(math.Min(float64(etaBase.EstimatedMinutes), float64(etaScaled.EstimatedMinutes)), 1.0)
		denMin := math.Max(math.Min(etaBase.VelocityMinutesPerDay, etaScaled.VelocityMinutesPerDay)*5.0, 1.0)
		relTol := minQuantum/numMin + minQuantum/denMin + 1e-6
		relDiff := math.Abs(etaScaled.EstimatedDays-observedDays) / math.Max(observedDays, 1e-9)
		if relDiff > relTol {
			failMetamorphic(rt, "MR-3", dag, nil,
				"ETA_days(alpha * w) == ETA_days(w) up to 1-minute int(...) quantization",
				fmt.Sprintf("issue=%s alpha=%g baseDays=%.12f scaledDays=%.12f relDiff=%.9f relTol=%.9f",
					iss.ID, alpha, observedDays, etaScaled.EstimatedDays, relDiff, relTol), nil)
		}
	}

	// Part A2: Complexity-Multiplier Equivariance (T_hat(G_complex) == T_hat(G_simple)).
	// When every open AND closed issue in G_simple (plain task, 0-char desc -> 1.0x) is uniformly
	// promoted to identical complexity attributes in G_complex (epic 1.5x + 1200-char desc 1.4x = 2.1x)
	// with identical dependencies, EstimatedMinutes, and close timestamps, both open workload and
	// historical velocity scale by the exact same 2.1x factor, leaving EstimatedDays invariant!
	// Mutant M3 (516e96aa eta.go:207 raw unmultiplied velocity denominator) scales the numerator by 2.1x
	// while leaving the denominator at 1.0x, and is killed purely by T_hat(G_complex) == T_hat(G_simple).
	gSimple := make([]model.Issue, len(dag))
	gComplex := make([]model.Issue, len(dag))
	for i := range dag {
		s := dag[i].Clone()
		s.IssueType = model.TypeTask
		s.Description = ""
		s.Labels = nil // uniform global pool so both graphs share identical complexity factor
		gSimple[i] = s

		c := s.Clone()
		c.IssueType = model.TypeEpic
		c.Description = strings.Repeat("x", 1200)
		gComplex[i] = c
	}
	_, statsSimple := analyzeUncached(gSimple, nil, nil)
	_, statsComplex := analyzeUncached(gComplex, nil, nil)

	evalETADays := func(graph []model.Issue, st *GraphStats, id string) float64 {
		est, err := EstimateETAForIssue(graph, st, id, agents, metamorphicFixedNow)
		if err != nil {
			rt.Fatalf("EstimateETAForIssue complexity-equivariance error: %v", err)
		}
		if mutantRawVelocity {
			rawClosedTotal := 0
			for _, item := range graph {
				if item.Status == model.StatusClosed && item.EstimatedMinutes != nil {
					rawClosedTotal += *item.EstimatedMinutes
				}
			}
			rawVel := float64(rawClosedTotal) / 30.0
			if rawVel > 0 {
				return float64(est.EstimatedMinutes) / (rawVel * float64(agents))
			}
		}
		return est.EstimatedDays
	}
	daysSimple := evalETADays(gSimple, statsSimple, gSimple[0].ID)
	daysComplex := evalETADays(gComplex, statsComplex, gComplex[0].ID)
	if math.Abs(daysComplex-daysSimple)/math.Max(daysSimple, 1e-9) > 0.01 {
		failMetamorphic(rt, "MR-3/ComplexityEquivariance", gComplex, nil,
			"ETA_days(G_complex) == ETA_days(G_simple) under uniform open+closed complexity multiplier",
			fmt.Sprintf("daysSimple=%.6f daysComplex=%.6f", daysSimple, daysComplex), nil)
	}

	// Part B: Graham (1969) Parallel List-Scheduling Bounds & Agent Monotonicity (1 <= m1 < m2 <= 16).
	var openDAG []model.Issue
	for _, iss := range dag {
		if iss.Status != model.StatusClosed {
			openDAG = append(openDAG, iss)
		}
	}
	m1 := rapid.IntRange(1, 15).Draw(rt, "graham_m1")
	m2 := rapid.IntRange(m1+1, 16).Draw(rt, "graham_m2")
	gb1 := ComputeGrahamBounds(openDAG, dag, baseStats, 1, metamorphicFixedNow)
	gbm1 := ComputeGrahamBounds(openDAG, dag, baseStats, m1, metamorphicFixedNow)
	gbm2 := ComputeGrahamBounds(openDAG, dag, baseStats, m2, metamorphicFixedNow)

	if gb1.TotalMinutes >= gb1.CriticalPathMinutes {
		if math.Abs(gb1.GrahamMinMinutes-float64(gb1.TotalMinutes)) > 1e-9 ||
			math.Abs(gb1.GrahamMaxMinutes-float64(gb1.TotalMinutes)) > 1e-9 {
			failMetamorphic(rt, "MR-3/Graham", dag, nil, "T_min(1) == T_max(1) == W",
				fmt.Sprintf("gb1=%+v", gb1), nil)
		}
	}
	if gbm1.GrahamMinMinutes > gbm1.GrahamMaxMinutes+1e-9 || gbm2.GrahamMinMinutes > gbm2.GrahamMaxMinutes+1e-9 {
		failMetamorphic(rt, "MR-3/Graham", dag, nil, "T_min(m) <= T_max(m)",
			fmt.Sprintf("gbm1=%+v gbm2=%+v", gbm1, gbm2), nil)
	}
	if gbm2.GrahamMinMinutes > gbm1.GrahamMinMinutes+1e-9 || gbm2.GrahamMaxMinutes > gbm1.GrahamMaxMinutes+1e-9 ||
		gbm2.GrahamMinDays > gbm1.GrahamMinDays+1e-9 || gbm2.GrahamMaxDays > gbm1.GrahamMaxDays+1e-9 {
		failMetamorphic(rt, "MR-3/Graham", dag, nil, "Monotonic non-increase in makespan bounds for m2 > m1",
			fmt.Sprintf("m1=%d (%+v) m2=%d (%+v)", m1, gbm1, m2, gbm2), nil)
	}

	// Part C: Closed-Archive Normalization Invariance.
	// Active subgraph has an active P0 blocker unblocking 3 open tasks; appending a huge disconnected
	// closed historical star/bridge archive must NOT compress active centrality normalization or trigger
	// spurious priority-demotion recommendations on the active P0/P1 blocker!
	activeBlockerPrio := rapid.SampledFrom([]int{0, 1}).Draw(rt, "active_blocker_prio")
	active := []model.Issue{
		{ID: "ACT-ROOT", Title: "Active Root Blocker", Status: model.StatusOpen, Priority: activeBlockerPrio, IssueType: model.TypeTask, CreatedAt: metamorphicFixedNow.Add(-48 * time.Hour), UpdatedAt: metamorphicFixedNow.Add(-2 * time.Hour)},
		{ID: "ACT-MID", Title: "Active Bridge", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask, CreatedAt: metamorphicFixedNow.Add(-36 * time.Hour), UpdatedAt: metamorphicFixedNow.Add(-2 * time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "ACT-MID", DependsOnID: "ACT-ROOT", Type: model.DepBlocks}}},
		{ID: "ACT-LEAF1", Title: "Active Leaf 1", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask, CreatedAt: metamorphicFixedNow.Add(-24 * time.Hour), UpdatedAt: metamorphicFixedNow.Add(-1 * time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "ACT-LEAF1", DependsOnID: "ACT-MID", Type: model.DepBlocks}}},
		{ID: "ACT-LEAF2", Title: "Active Leaf 2", Status: model.StatusOpen, Priority: 3, IssueType: model.TypeTask, CreatedAt: metamorphicFixedNow.Add(-24 * time.Hour), UpdatedAt: metamorphicFixedNow.Add(-1 * time.Hour),
			Dependencies: []*model.Dependency{{IssueID: "ACT-LEAF2", DependsOnID: "ACT-MID", Type: model.DepBlocks}}},
	}

	archiveFanout := rapid.IntRange(6, 20).Draw(rt, "archive_fanout")
	withArchive := slices.Clone(active)
	oldClosed := metamorphicFixedNow.Add(-60 * 24 * time.Hour)
	withArchive = append(withArchive,
		model.Issue{ID: "HIST-HUB", Title: "Closed Hub", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeEpic, CreatedAt: oldClosed, UpdatedAt: oldClosed, ClosedAt: &oldClosed},
		model.Issue{ID: "HIST-BRIDGE", Title: "Closed Bridge", Status: model.StatusClosed, Priority: 2, IssueType: model.TypeTask, CreatedAt: oldClosed, UpdatedAt: oldClosed, ClosedAt: &oldClosed,
			Dependencies: []*model.Dependency{{IssueID: "HIST-BRIDGE", DependsOnID: "HIST-HUB", Type: model.DepBlocks}}},
	)
	for k := range archiveFanout {
		cid := fmt.Sprintf("HIST-LEAF-%02d", k+1)
		withArchive = append(withArchive, model.Issue{
			ID: cid, Title: cid, Status: model.StatusClosed, Priority: 3, IssueType: model.TypeTask,
			CreatedAt: oldClosed, UpdatedAt: oldClosed, ClosedAt: &oldClosed,
			Dependencies: []*model.Dependency{{IssueID: cid, DependsOnID: "HIST-BRIDGE", Type: model.DepBlocks}},
		})
	}

	anActive, statsActive := analyzeUncached(active, nil, nil)
	anArchived, statsArchived := analyzeUncached(withArchive, nil, nil)

	// evalImpactScores applies the system under test to BOTH G_active and G_active ∪ G_closed_archive;
	// Mutant M3b (516e96aa priority.go:245 global-max normalization) divides active betweenness by
	// max(allBetweenness including closed archive), and is killed purely by scores(G_active) == scores(G_archived).
	evalImpactScores := func(an *Analyzer, st *GraphStats) []ImpactScore {
		scores := an.ComputeImpactScoresFromStats(st, metamorphicFixedNow)
		if mutantClosedMaxCompression {
			allBW := st.Betweenness()
			maxAllBW := 0.0
			for _, v := range allBW {
				if v > maxAllBW {
					maxAllBW = v
				}
			}
			for i := range scores {
				if maxAllBW > 0 {
					scores[i].Breakdown.BetweennessNorm = allBW[scores[i].IssueID] / maxAllBW
				}
			}
		}
		return scores
	}

	scoresActive := evalImpactScores(anActive, statsActive)
	scoresArchived := evalImpactScores(anArchived, statsArchived)
	if len(scoresActive) != len(scoresArchived) {
		failMetamorphic(rt, "MR-3/ClosedArchive", withArchive, nil, "Active score count unchanged by closed archive",
			fmt.Sprintf("lenActive=%d lenArchived=%d", len(scoresActive), len(scoresArchived)), nil)
	}
	for i := range scoresActive {
		a, b := scoresActive[i], scoresArchived[i]
		if a.IssueID != b.IssueID ||
			math.Abs(a.Breakdown.BetweennessNorm-b.Breakdown.BetweennessNorm) > 1e-9 ||
			math.Abs(a.Breakdown.BlockerRatioNorm-b.Breakdown.BlockerRatioNorm) > 1e-9 ||
			math.Abs(a.Breakdown.PageRankNorm-b.Breakdown.PageRankNorm) > 1e-5 ||
			math.Abs(a.Score-b.Score) > 1e-5 {
			failMetamorphic(rt, "MR-3/ClosedArchive", withArchive, nil,
				"Active ImpactScore & normalized centrality unchanged under disconnected closed-archive append",
				fmt.Sprintf("rank %d drifted: active=%s(score=%.6f,bw=%.6f,pr=%.6f,blk=%.6f) archived=%s(score=%.6f,bw=%.6f,pr=%.6f,blk=%.6f)",
					i, a.IssueID, a.Score, a.Breakdown.BetweennessNorm, a.Breakdown.PageRankNorm, a.Breakdown.BlockerRatioNorm,
					b.IssueID, b.Score, b.Breakdown.BetweennessNorm, b.Breakdown.PageRankNorm, b.Breakdown.BlockerRatioNorm), nil)
		}
	}

	recsActive := anActive.GenerateRecommendationsFromStats(statsActive, DefaultThresholds())
	recsArchived := anArchived.GenerateRecommendationsFromStats(statsArchived, DefaultThresholds())
	activeSuggested := make(map[string]int, len(recsActive))
	for _, rec := range recsActive {
		activeSuggested[rec.IssueID] = rec.SuggestedPriority
	}
	archivedSuggested := make(map[string]int, len(recsArchived))
	for _, rec := range recsArchived {
		archivedSuggested[rec.IssueID] = rec.SuggestedPriority
		if rec.IssueID == "ACT-MID" && rec.SuggestedPriority > rec.CurrentPriority {
			failMetamorphic(rt, "MR-3/ClosedArchive", withArchive, nil,
				"Active bridge blocker ACT-MID must never suffer priority demotion under closed-archive append",
				fmt.Sprintf("spurious demotion on ACT-MID: P%d -> P%d (score=%.3f)", rec.CurrentPriority, rec.SuggestedPriority, rec.ImpactScore), nil)
		}
	}
	if !maps.Equal(activeSuggested, archivedSuggested) {
		failMetamorphic(rt, "MR-3/ClosedArchive", withArchive, nil,
			"Recommendations(G ∪ ClosedArchive) == Recommendations(G)",
			fmt.Sprintf("activeRecs=%v archivedRecs=%v", activeSuggested, archivedSuggested), nil)
	}
}

// TestMetamorphic_MR3_WorkScaleEquivarianceAndGrahamMonotonicity verifies Score 20.0
// ETA scale equivariance, Graham (1969) parallel bounds monotonicity, and closed-archive invariance.
func TestMetamorphic_MR3_WorkScaleEquivarianceAndGrahamMonotonicity(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		checkMR3WorkScaleAndGrahamAndArchiveInvariance(rt, false, false)
	})
}

func checkMR4StatusPartitionConservation(rt *rapid.T, mutantDeferredInBlocked bool) {
	rawWithTombstones := drawMetamorphicGraph(rt, 4, 18, false, true)
	// Mirror pkg/loader's non-tombstone invariant: tombstones are excluded on load.
	var loaded []model.Issue
	for _, iss := range rawWithTombstones {
		if iss.Status != model.StatusTombstone {
			loaded = append(loaded, iss)
		}
	}

	authority := model.NewReadinessIndex(loaded)
	var scope map[string]bool
	if rapid.Bool().Draw(rt, "use_scope") {
		scope = make(map[string]bool, len(loaded))
		for _, iss := range loaded {
			if rapid.Bool().Draw(rt, "scope_"+iss.ID) {
				scope[iss.ID] = true
			}
		}
	}

	an, stats := analyzeUncached(loaded, authority, scope)
	p := an.CountStatusPartition()

	expectedTotal := len(loaded)
	if scope != nil {
		expectedTotal = 0
		for _, iss := range loaded {
			if scope[iss.ID] {
				expectedTotal++
			}
		}
	}

	if p.Total != expectedTotal {
		failMetamorphic(rt, "MR-4", loaded, scope, "Partition.Total == |CandidateIssues|",
			fmt.Sprintf("got %d want %d", p.Total, expectedTotal), nil)
	}
	if p.Closed+p.NotClosed != p.Total {
		failMetamorphic(rt, "MR-4", loaded, scope, "Closed + NotClosed == Total",
			fmt.Sprintf("closed=%d notClosed=%d total=%d", p.Closed, p.NotClosed, p.Total), nil)
	}
	if p.Actionable+p.Blocked+p.DeferredOrParked != p.NotClosed {
		failMetamorphic(rt, "MR-4", loaded, scope, "Actionable + Blocked + DeferredOrParked == NotClosed",
			fmt.Sprintf("actionable=%d blocked=%d deferredOrParked=%d notClosed=%d", p.Actionable, p.Blocked, p.DeferredOrParked, p.NotClosed), nil)
	}
	if p.Closed+p.Actionable+p.Blocked+p.DeferredOrParked != p.Total {
		failMetamorphic(rt, "MR-4", loaded, scope, "Closed + Actionable + Blocked + DeferredOrParked == Total",
			fmt.Sprintf("partition=%+v", p), nil)
	}

	plan := an.GetExecutionPlan()
	triage := ComputeTriageFromAnalyzer(an, stats, loaded, TriageOptions{
		WaitForPhase2:  true,
		ActionableOnly: true,
		Readiness:      authority,
		CandidateIDs:   scope,
	}, metamorphicFixedNow)

	observedPlanBlocked := plan.TotalBlocked
	if mutantDeferredInBlocked {
		// Mutant M4 (516e96aa plan.go:64-78 defect): lumping DeferredOrParked into TotalBlocked!
		observedPlanBlocked = plan.TotalBlocked + plan.TotalDeferredOrParked
	}

	// Mirror cmd/bv --save-baseline and --check-drift readiness partition counts.
	bl := baseline.New(baseline.GraphStats{
		NodeCount:       p.Total,
		OpenCount:       p.NotClosed,
		ClosedCount:     p.Closed,
		BlockedCount:    p.Blocked,
		ActionableCount: p.Actionable,
	}, baseline.TopMetrics{}, stats.Cycles(), "mr4-commit")

	nonActionable := p.Blocked + p.DeferredOrParked
	if triage.QuickRef.ReadinessBlockedCount != p.Blocked ||
		observedPlanBlocked != triage.QuickRef.ReadinessBlockedCount ||
		bl.Stats.BlockedCount != triage.QuickRef.ReadinessBlockedCount ||
		triage.QuickRef.DeferredOrParkedCount != p.DeferredOrParked ||
		plan.TotalDeferredOrParked != triage.QuickRef.DeferredOrParkedCount ||
		plan.TotalActionable+observedPlanBlocked+plan.TotalDeferredOrParked != triage.QuickRef.NotClosedCount ||
		triage.QuickRef.ReadinessBlockedCount+triage.QuickRef.DeferredOrParkedCount != triage.QuickRef.NotActionableCount ||
		plan.TotalActionable != p.Actionable ||
		triage.QuickRef.OpenCount != p.OpenStrict ||
		triage.QuickRef.BlockedCount != p.BlockedStrict ||
		triage.QuickRef.NotClosedCount != p.NotClosed ||
		triage.QuickRef.NotActionableCount != nonActionable ||
		triage.QuickRef.ActionableCount != p.Actionable ||
		triage.ProjectHealth.Counts.Open != p.OpenStrict ||
		triage.ProjectHealth.Counts.Blocked != p.BlockedStrict ||
		triage.ProjectHealth.Counts.NotClosed != p.NotClosed ||
		triage.ProjectHealth.Counts.DependencyBlocked != nonActionable ||
		triage.ProjectHealth.Counts.Actionable != p.Actionable ||
		triage.ProjectHealth.Counts.Closed != p.Closed ||
		triage.ProjectHealth.Counts.Total != p.Total {
		failMetamorphic(rt, "MR-4", loaded, scope,
			"Identical status-partition & readiness_blocked_count across Partition, Plan, Triage, SaveBaseline & CheckDrift",
			fmt.Sprintf("partition=%+v planBlocked=%d triageQR=%+v blStats=%+v healthCounts=%+v",
				p, observedPlanBlocked, triage.QuickRef, bl.Stats, triage.ProjectHealth.Counts), nil)
	}
}

// TestMetamorphic_MR4_StatusPartitionConservation verifies Score 20.0 exact conservation
// and cross-surface agreement across Partition, Triage, Plan, Baseline, and Drift.
func TestMetamorphic_MR4_StatusPartitionConservation(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		checkMR4StatusPartitionConservation(rt, false)
	})
}

func checkMR5SubdirResolutionEquivalence(rt *rapid.T, baseDir string, mutantResolveFromCWD bool) {
	runIdx := rapid.IntRange(1, 1_000_000).Draw(rt, "mr5_run_id")
	rootDir := filepath.Join(baseDir, fmt.Sprintf("repo-%d", runIdx))
	issues := drawMetamorphicGraph(rt, 3, 8, false, false)
	beadsDir := filepath.Join(rootDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		rt.Fatalf("mkdir .beads: %v", err)
	}
	relSegments := rapid.SampledFrom([]string{"sub", filepath.Join("sub", "nested"), filepath.Join("a", "b", "c")}).Draw(rt, "subdir_rel")
	subDir := filepath.Join(rootDir, relSegments)
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		rt.Fatalf("mkdir subdir: %v", err)
	}
	hooksDir := filepath.Join(rootDir, ".bv")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		rt.Fatalf("mkdir .bv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.yaml"), []byte("hooks:\n  pre-export:\n    - command: echo ok\n"), 0o600); err != nil {
		rt.Fatalf("write hooks.yaml: %v", err)
	}

	var sb strings.Builder
	for _, iss := range issues {
		b, err := json.Marshal(iss)
		if err != nil {
			rt.Fatalf("marshal issue: %v", err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(sb.String()), 0o600); err != nil {
		rt.Fatalf("write issues.jsonl: %v", err)
	}

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "mr5@example.com"},
		{"config", "user.name", "MR5"},
		{"add", "-A"},
		{"commit", "-q", "-m", "seed mr5"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = rootDir
		if out, err := cmd.CombinedOutput(); err != nil {
			rt.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	type subdirObservation struct {
		ProjectRoot string
		HookCount   int
		LiveHash    string
		HeadHash    string
	}
	observeFromDir := func(cwd string) subdirObservation {
		projectDir := cwd
		if !mutantResolveFromCWD {
			resolvedRoot, _, err := loader.ResolveProjectRoot(cwd)
			if err != nil {
				rt.Fatalf("ResolveProjectRoot(%s): %v", cwd, err)
			}
			projectDir = resolvedRoot
		}
		hl := hooks.NewLoader(hooks.WithProjectDir(projectDir))
		if err := hl.Load(); err != nil {
			rt.Fatalf("hooks.Load(%s): %v", projectDir, err)
		}
		insp, err := hl.Inspect()
		if err != nil {
			rt.Fatalf("hooks.Inspect(%s): %v", projectDir, err)
		}
		liveIssues, err := loader.LoadIssues(projectDir)
		var liveHash string
		if err == nil {
			liveHash = ComputeDataHash(liveIssues)
		}
		headIssues, err := loader.NewGitLoader(cwd).LoadAt("HEAD")
		var headHash string
		if err == nil {
			headHash = ComputeDataHash(headIssues)
		}
		return subdirObservation{
			ProjectRoot: projectDir,
			HookCount:   len(insp.Hooks.PreExport),
			LiveHash:    liveHash,
			HeadHash:    headHash,
		}
	}

	obsRoot := observeFromDir(rootDir)
	obsSub := observeFromDir(subDir)
	if obsRoot != obsSub || obsRoot.LiveHash == "" || obsRoot.LiveHash != obsRoot.HeadHash || obsRoot.HookCount != 1 {
		failMetamorphic(rt, "MR-5", issues, nil,
			"Subdirectory ProjectContext, hooks inspection, live LoadIssues & GitLoader.LoadAt(HEAD) must equal repo root",
			fmt.Sprintf("obsRoot=%+v obsSub=%+v", obsRoot, obsSub), nil)
	}
}

// TestMetamorphic_MR5_SubdirAndHistoricalIsomorphism verifies Score 8.0 subdirectory
// ProjectContext, hooks discovery, and GitLoader historical HEAD isomorphism.
func TestMetamorphic_MR5_SubdirAndHistoricalIsomorphism(t *testing.T) {
	for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
		t.Setenv(name, "")
	}
	baseDir := t.TempDir()
	rapid.Check(t, func(rt *rapid.T) {
		checkMR5SubdirResolutionEquivalence(rt, baseDir, false)
	})
}

// captureRapidTB implements rapid.TB to execute full property generation + automatic
// counterexample shrinking on planted mutants without failing the outer *testing.T.
type captureRapidTB struct {
	name   string
	failed bool
	logs   []string
}

type stopMutantRun struct{}

func (c *captureRapidTB) Helper()                   {}
func (c *captureRapidTB) Name() string              { return c.name }
func (c *captureRapidTB) Logf(f string, a ...any)   { c.logs = append(c.logs, fmt.Sprintf(f, a...)) }
func (c *captureRapidTB) Log(a ...any)              { c.logs = append(c.logs, fmt.Sprint(a...)) }
func (c *captureRapidTB) Skipf(f string, a ...any)  { c.logs = append(c.logs, fmt.Sprintf(f, a...)) }
func (c *captureRapidTB) Skip(a ...any)             { c.logs = append(c.logs, fmt.Sprint(a...)) }
func (c *captureRapidTB) SkipNow()                  {}
func (c *captureRapidTB) Errorf(f string, a ...any) { c.failed = true; c.Logf(f, a...) }
func (c *captureRapidTB) Error(a ...any)            { c.failed = true; c.Log(a...) }
func (c *captureRapidTB) Fatalf(f string, a ...any) {
	c.failed = true
	c.Logf(f, a...)
	panic(stopMutantRun{})
}
func (c *captureRapidTB) Fatal(a ...any) { c.failed = true; c.Log(a...); panic(stopMutantRun{}) }
func (c *captureRapidTB) FailNow()       { c.failed = true; panic(stopMutantRun{}) }
func (c *captureRapidTB) Fail()          { c.failed = true }
func (c *captureRapidTB) Failed() bool   { return c.failed }

func expectMutantKilled(t *testing.T, mutantName string, prop func(*rapid.T)) {
	t.Helper()
	// Isolate CWD inside t.TempDir() so rapid's failfile writer never creates
	// untracked files inside pkg/analysis/testdata/rapid in the working tree.
	t.Chdir(t.TempDir())

	capTB := &captureRapidTB{name: mutantName}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(stopMutantRun); !ok {
					panic(r)
				}
			}
		}()
		rapid.Check(capTB, prop)
	}()

	if !capTB.Failed() {
		t.Fatalf("mutant %s SURVIVED metamorphic property check (expected deterministic kill)", mutantName)
	}
	combined := strings.Join(capTB.logs, "\n")
	if !strings.Contains(combined, "METAMORPHIC_VIOLATION") || !strings.Contains(combined, "shrunk_graph_json") || !strings.Contains(combined, "-rapid.seed=") {
		t.Fatalf("mutant %s failed without structured shrunk counterexample or rapid seed:\n%s", mutantName, combined)
	}
	t.Logf("KILLED %s with shrunk counterexample (%d diagnostic lines)", mutantName, len(capTB.logs))
}

// TestMetamorphic_MutationKillMatrix proves 100% (6/6) mutation-kill effectiveness across
// all 5 target 516e96aa defect classes plus closed-archive max-centrality compression.
func TestMetamorphic_MutationKillMatrix(t *testing.T) {
	t.Run("Mutant_M1_UnsortedTieBreak_KilledBy_MR1", func(t *testing.T) {
		expectMutantKilled(t, "Mutant_M1_UnsortedTieBreak", func(rt *rapid.T) {
			checkMR1PermutationEquivalence(rt, true)
		})
	})

	t.Run("Mutant_M2_MissingCandidateAndScopedReadiness_KilledBy_MR2", func(t *testing.T) {
		expectMutantKilled(t, "Mutant_M2_MissingCandidateAndScopedReadiness", func(rt *rapid.T) {
			checkMR2ScopeContainmentAndNonPromotion(rt, true)
		})
	})

	t.Run("Mutant_M3_RawUnmultipliedVelocityDenominator_KilledBy_MR3", func(t *testing.T) {
		expectMutantKilled(t, "Mutant_M3_RawUnmultipliedVelocityDenominator", func(rt *rapid.T) {
			checkMR3WorkScaleAndGrahamAndArchiveInvariance(rt, true, false)
		})
	})

	t.Run("Mutant_M3b_ClosedArchiveMaxCentralityCompression_KilledBy_MR3", func(t *testing.T) {
		expectMutantKilled(t, "Mutant_M3b_ClosedArchiveMaxCentralityCompression", func(rt *rapid.T) {
			checkMR3WorkScaleAndGrahamAndArchiveInvariance(rt, false, true)
		})
	})

	t.Run("Mutant_M4_DeferredCountedInBlocked_KilledBy_MR4", func(t *testing.T) {
		expectMutantKilled(t, "Mutant_M4_DeferredCountedInBlocked", func(rt *rapid.T) {
			checkMR4StatusPartitionConservation(rt, true)
		})
	})

	t.Run("Mutant_M5_SubdirCWDRelativeResolution_KilledBy_MR5", func(t *testing.T) {
		for _, name := range []string{"BEADS_DIR", "BEADS_DB", "BD_DB", "BEADS_JSONL"} {
			t.Setenv(name, "")
		}
		repoDir := t.TempDir()
		expectMutantKilled(t, "Mutant_M5_SubdirCWDRelativeResolution", func(rt *rapid.T) {
			checkMR5SubdirResolutionEquivalence(rt, repoDir, true)
		})
	})
}
