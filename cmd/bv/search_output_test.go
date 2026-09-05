package main

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/search"
)

func TestSearchMinScoreAndRankingIdentity(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-Inf", "1.01", "-1.01", "invalid"} {
		if _, err := parseSearchMinScore(raw); err == nil {
			t.Errorf("accepted minimum %q", raw)
		}
	}
	for _, raw := range []string{"-1", "0", "0.5", "1"} {
		if got, err := parseSearchMinScore(raw); err != nil || got == nil {
			t.Errorf("minimum %q: %v %v", raw, got, err)
		}
	}
	if got, err := parseSearchMinScore(""); err != nil || got != nil {
		t.Errorf("unset threshold: %v %v", got, err)
	}
	base := robotSearchOutput{RobotEnvelope: RobotEnvelope{DataHash: "data", SourcePath: "issues.jsonl", SourceKind: "jsonl_local"}, CandidateHash: "selected", IndexDataHash: "full", Query: "query", Mode: search.SearchModeText, Limit: 1}
	first, err := searchRankingHash(base)
	if err != nil {
		t.Fatal(err)
	}
	base.GeneratedAt = "a later invocation"
	base.Loaded = true
	again, err := searchRankingHash(base)
	if err != nil || first != again {
		t.Fatal("volatile invocation/cache state changed ranking identity")
	}
	for _, change := range []func(*robotSearchOutput){
		func(out *robotSearchOutput) { out.CandidateHash = "other selection" },
		func(out *robotSearchOutput) { out.IndexDataHash = "other source" },
		func(out *robotSearchOutput) { out.Query = "other query" },
		func(out *robotSearchOutput) { out.Mode = search.SearchModeHybrid },
		func(out *robotSearchOutput) { score := 0.5; out.MinScore = &score },
		func(out *robotSearchOutput) { out.Limit = 2 },
		func(out *robotSearchOutput) { now := time.Unix(1700000000, 0).UTC(); out.RankingTime = &now },
	} {
		changed := base
		change(&changed)
		hash, err := searchRankingHash(changed)
		if err != nil || hash == first {
			t.Fatalf("different retrieval configuration shares identity: %+v err=%v", changed, err)
		}
	}
}

// --search-preset used to be silently ignored unless --search-mode hybrid was
// also given: the flag "worked" while the ranking never changed. A preset now
// implies hybrid mode (text-only implies text), and an explicit text mode with
// a hybrid preset is rejected instead of ignored.
func TestApplySearchConfigOverrides_PresetImpliesHybrid(t *testing.T) {
	base := search.SearchConfig{Mode: search.SearchModeText, Preset: search.PresetDefault}

	cfg, err := applySearchConfigOverrides(base, "", "impact-first", "")
	if err != nil {
		t.Fatalf("preset alone: %v", err)
	}
	if cfg.Mode != search.SearchModeHybrid || cfg.Preset != search.PresetImpactFirst {
		t.Fatalf("preset alone: mode=%q preset=%q, want hybrid/impact-first", cfg.Mode, cfg.Preset)
	}

	cfg, err = applySearchConfigOverrides(base, "", "text-only", "")
	if err != nil {
		t.Fatalf("text-only: %v", err)
	}
	if cfg.Mode != search.SearchModeText {
		t.Fatalf("text-only preset: mode=%q, want text", cfg.Mode)
	}

	cfg, err = applySearchConfigOverrides(base, "hybrid", "bug-hunting", "")
	if err != nil || cfg.Mode != search.SearchModeHybrid || cfg.Preset != search.PresetBugHunting {
		t.Fatalf("explicit hybrid + preset: cfg=%+v err=%v", cfg, err)
	}

	if _, err := applySearchConfigOverrides(base, "text", "sprint-planning", ""); err == nil {
		t.Fatal("--search-mode text with a hybrid preset must be rejected")
	}

	// No preset: mode untouched.
	cfg, err = applySearchConfigOverrides(base, "", "", "")
	if err != nil || cfg.Mode != search.SearchModeText {
		t.Fatalf("no flags: cfg=%+v err=%v", cfg, err)
	}
}
