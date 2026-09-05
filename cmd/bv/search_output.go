package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
)

type robotSearchResult struct {
	IssueID         string             `json:"issue_id"`
	Score           float64            `json:"score"`
	TextScore       float64            `json:"text_score,omitempty"`
	Title           string             `json:"title,omitempty"`
	ComponentScores map[string]float64 `json:"component_scores,omitempty"`
}

type robotSearchOutput struct {
	RobotEnvelope
	IndexDataHash string                `json:"index_data_hash"`
	CandidateHash string                `json:"candidate_hash"`
	RankingHash   string                `json:"ranking_hash"`
	RankingTime   *time.Time            `json:"ranking_time,omitempty"`
	MinScore      *float64              `json:"min_score,omitempty"`
	Query         string                `json:"query"`
	Provider      search.Provider       `json:"provider"`
	Model         string                `json:"model,omitempty"`
	Dim           int                   `json:"dim"`
	IndexPath     string                `json:"index_path"`
	Index         search.IndexSyncStats `json:"index"`
	Loaded        bool                  `json:"loaded"`
	Limit         int                   `json:"limit"`
	Mode          search.SearchMode     `json:"mode"`
	Preset        search.PresetName     `json:"preset,omitempty"`
	Weights       *search.Weights       `json:"weights,omitempty"`
	Results       []robotSearchResult   `json:"results"`
	UsageHints    []string              `json:"usage_hints,omitempty"`
}

func searchRankingHash(out robotSearchOutput) (string, error) {
	identity := map[string]any{
		"index_data_hash": out.IndexDataHash, "candidate_hash": out.CandidateHash,
		"scope": out.Scope, "source_path": out.SourcePath, "source_kind": out.SourceKind,
		"as_of_commit": out.AsOfCommit, "query": out.Query, "mode": out.Mode,
		"preset": out.Preset, "weights": out.Weights, "min_score": out.MinScore,
		"limit": out.Limit, "provider": out.Provider, "model": out.Model, "dim": out.Dim,
		"ranking_time": out.RankingTime,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode search ranking identity: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func parseSearchMinScore(raw string) (*float64, error) {
	if raw == "" {
		return nil, nil
	}
	score, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < -1 || score > 1 {
		return nil, fmt.Errorf("invalid --search-min-score %q (expected a finite number from -1 to 1)", raw)
	}
	return &score, nil
}

func writeRobotSearchOutput(w io.Writer, out robotSearchOutput) error {
	enc := newRobotEncoder(w)
	return enc.Encode(out)
}

func resolveSearchConfig(modeFlag, presetFlag, weightsFlag string) (search.SearchConfig, error) {
	mode, preset, weights := os.Getenv(search.EnvSearchMode), os.Getenv(search.EnvSearchPreset), os.Getenv(search.EnvSearchWeights)
	// Validate only inherited values. A selected preset owns the implied mode,
	// and explicit text mode makes an inherited hybrid preset irrelevant.
	if modeFlag != "" || presetFlag != "" {
		mode = ""
	}
	if presetFlag != "" || strings.EqualFold(modeFlag, "text") {
		preset = ""
	}
	if weightsFlag != "" {
		weights = ""
	}
	cfg, err := search.ParseSearchConfig(mode, preset, weights)
	if err != nil {
		return search.SearchConfig{}, err
	}
	return applySearchConfigOverrides(cfg, modeFlag, presetFlag, weightsFlag)
}

func applySearchConfigOverrides(cfg search.SearchConfig, modeFlag, presetFlag, weightsFlag string) (search.SearchConfig, error) {
	if modeFlag != "" {
		switch search.SearchMode(strings.ToLower(modeFlag)) {
		case search.SearchModeText, search.SearchModeHybrid:
			cfg.Mode = search.SearchMode(strings.ToLower(modeFlag))
		default:
			return search.SearchConfig{}, fmt.Errorf("invalid --search-mode: %q (expected text|hybrid)", modeFlag)
		}
	}

	if presetFlag != "" {
		name := search.PresetName(strings.ToLower(presetFlag))
		if _, err := search.GetPreset(name); err != nil {
			return search.SearchConfig{}, err
		}
		cfg.Preset = name
		// A preset only affects hybrid ranking, so an explicit preset implies
		// hybrid mode (text-only implies text). Silently ignoring the preset
		// under the default text mode was a trap: the flag appeared to work
		// while the ranking never changed.
		switch {
		case name == search.PresetTextOnly:
			if modeFlag == "" {
				cfg.Mode = search.SearchModeText
			}
		case modeFlag == "":
			cfg.Mode = search.SearchModeHybrid
		case cfg.Mode == search.SearchModeText:
			return search.SearchConfig{}, fmt.Errorf("--search-preset %q needs hybrid mode; drop --search-mode text or use --search-preset text-only", presetFlag)
		}
	}

	if weightsFlag != "" {
		weights, err := search.ParseWeightsJSON(weightsFlag)
		if err != nil {
			return search.SearchConfig{}, err
		}
		cfg.Weights = weights
		cfg.HasWeights = true
	}

	return cfg, nil
}

func resolveSearchWeights(cfg search.SearchConfig) (search.Weights, search.PresetName, error) {
	if cfg.HasWeights {
		return cfg.Weights, search.PresetName("custom"), nil
	}

	weights, err := search.GetPreset(cfg.Preset)
	if err != nil {
		return search.Weights{}, "", err
	}
	return weights, cfg.Preset, nil
}

func buildHybridScores(results []search.SearchResult, scorer search.HybridScorer) ([]search.HybridScore, error) {
	out := make([]search.HybridScore, 0, len(results))
	for _, result := range results {
		scored, err := scorer.Score(result.IssueID, result.Score)
		if err != nil {
			return nil, err
		}
		out = append(out, scored)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].FinalScore == out[j].FinalScore {
			return out[i].IssueID < out[j].IssueID
		}
		return out[i].FinalScore > out[j].FinalScore
	})

	return out, nil
}

func promoteExactSearchResult(query string, results []search.SearchResult) []search.SearchResult {
	needle := strings.TrimSpace(query)
	if needle == "" || len(results) == 0 {
		return results
	}
	for i := range results {
		if results[i].IssueID == needle {
			if i == 0 {
				return results
			}
			match := results[i]
			copy(results[1:i+1], results[0:i])
			results[0] = match
			return results
		}
	}
	return results
}

func promoteExactHybridResult(query string, results []search.HybridScore) []search.HybridScore {
	needle := strings.TrimSpace(query)
	if needle == "" || len(results) == 0 {
		return results
	}
	for i := range results {
		if results[i].IssueID == needle {
			if i == 0 {
				return results
			}
			match := results[i]
			copy(results[1:i+1], results[0:i])
			results[0] = match
			return results
		}
	}
	return results
}
