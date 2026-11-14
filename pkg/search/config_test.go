package search

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

// =============================================================================
// EmbeddingConfigFromEnv Tests
// =============================================================================

func TestEmbeddingConfigFromEnv(t *testing.T) {
	// Helper to save and restore environment
	saveEnv := func() func() {
		embedder := os.Getenv(EnvSemanticEmbedder)
		model := os.Getenv(EnvSemanticModel)
		dim := os.Getenv(EnvSemanticDim)
		return func() {
			os.Setenv(EnvSemanticEmbedder, embedder)
			os.Setenv(EnvSemanticModel, model)
			os.Setenv(EnvSemanticDim, dim)
		}
	}

	tests := []struct {
		name             string
		envEmbedder      string
		envModel         string
		envDim           string
		expectedProvider Provider
		expectedModel    string
		expectedDim      int
	}{
		{
			name:             "all defaults",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "hash provider explicit",
			envEmbedder:      "hash",
			envModel:         "",
			envDim:           "",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "hash provider uppercase",
			envEmbedder:      "HASH",
			envModel:         "",
			envDim:           "",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "hash provider with whitespace",
			envEmbedder:      "  hash  ",
			envModel:         "",
			envDim:           "",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "openai provider",
			envEmbedder:      "openai",
			envModel:         "text-embedding-3-small",
			envDim:           "1536",
			expectedProvider: ProviderOpenAI,
			expectedModel:    "text-embedding-3-small",
			expectedDim:      1536,
		},
		{
			name:             "python-sentence-transformers provider",
			envEmbedder:      "python-sentence-transformers",
			envModel:         "all-MiniLM-L6-v2",
			envDim:           "384",
			expectedProvider: ProviderPythonSentenceTransformers,
			expectedModel:    "all-MiniLM-L6-v2",
			expectedDim:      384,
		},
		{
			name:             "custom dimension",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "512",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      512,
		},
		{
			name:             "model with whitespace",
			envEmbedder:      "",
			envModel:         "  my-model  ",
			envDim:           "",
			expectedProvider: ProviderHash,
			expectedModel:    "my-model",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "invalid dimension non-integer",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "not-a-number",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "invalid dimension float",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "128.5",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "negative dimension normalized to default",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "-1",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "zero dimension normalized to default",
			envEmbedder:      "",
			envModel:         "",
			envDim:           "0",
			expectedProvider: ProviderHash,
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
		{
			name:             "unknown provider passes through",
			envEmbedder:      "custom-provider",
			envModel:         "",
			envDim:           "",
			expectedProvider: "custom-provider",
			expectedModel:    "",
			expectedDim:      DefaultEmbeddingDim,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			restore := saveEnv()
			defer restore()

			os.Setenv(EnvSemanticEmbedder, tt.envEmbedder)
			os.Setenv(EnvSemanticModel, tt.envModel)
			os.Setenv(EnvSemanticDim, tt.envDim)

			cfg := EmbeddingConfigFromEnv()

			if cfg.Provider != tt.expectedProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tt.expectedProvider)
			}
			if cfg.Model != tt.expectedModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.expectedModel)
			}
			if cfg.Dim != tt.expectedDim {
				t.Errorf("Dim = %d, want %d", cfg.Dim, tt.expectedDim)
			}
		})
	}
}

// =============================================================================
// SearchConfigFromEnv Tests
// =============================================================================

func TestSearchConfigFromEnv_Defaults(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)

	os.Unsetenv(EnvSearchMode)
	os.Unsetenv(EnvSearchPreset)
	os.Unsetenv(EnvSearchWeights)

	cfg, err := SearchConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != SearchModeText {
		t.Fatalf("expected default mode text, got %q", cfg.Mode)
	}
	if cfg.Preset != PresetDefault {
		t.Fatalf("expected default preset %q, got %q", PresetDefault, cfg.Preset)
	}
	if cfg.HasWeights {
		t.Fatalf("expected no custom weights by default")
	}
}

func TestSearchConfigFromEnv_Overrides(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)

	os.Setenv(EnvSearchMode, "hybrid")
	os.Setenv(EnvSearchPreset, "impact-first")
	os.Setenv(EnvSearchWeights, `{"text":0.5,"pagerank":0.2,"status":0.1,"impact":0.1,"priority":0.05,"recency":0.05}`)

	cfg, err := SearchConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != SearchModeHybrid {
		t.Fatalf("expected mode hybrid, got %q", cfg.Mode)
	}
	if cfg.Preset != PresetImpactFirst {
		t.Fatalf("expected preset %q, got %q", PresetImpactFirst, cfg.Preset)
	}
	if !cfg.HasWeights {
		t.Fatalf("expected HasWeights true")
	}
	if cfg.Weights.TextRelevance != 0.5 {
		t.Fatalf("expected text weight 0.5, got %f", cfg.Weights.TextRelevance)
	}
}

func TestSearchConfigFromEnv_InvalidMode(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)

	os.Setenv(EnvSearchMode, "bogus")
	_, err := SearchConfigFromEnv()
	if err == nil {
		t.Fatalf("expected error for invalid mode")
	}
}

func TestSearchConfigFromEnv_InvalidPreset(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)

	os.Setenv(EnvSearchPreset, "not-a-preset")
	_, err := SearchConfigFromEnv()
	if err == nil {
		t.Fatalf("expected error for invalid preset")
	}
}

func TestParseWeightsJSON(t *testing.T) {
	valid := `{"text":0.4,"pagerank":0.2,"status":0.15,"impact":0.1,"priority":0.1,"recency":0.05}`
	weights, err := ParseWeightsJSON(valid)
	if err != nil {
		t.Fatalf("unexpected error parsing valid JSON: %v", err)
	}
	if weights.PageRank != 0.2 {
		t.Fatalf("expected PageRank 0.2, got %f", weights.PageRank)
	}

	_, err = ParseWeightsJSON(`{"text":1}`)
	if err == nil {
		t.Fatalf("expected error for missing keys")
	}

	_, err = ParseWeightsJSON(`{"text":0.5,"pagerank":0.2,"status":0.1,"impact":0.1,"priority":0.05,"recency":0.05,"extra":0.0}`)
	if err == nil {
		t.Fatalf("expected error for unknown key")
	}

	_, err = ParseWeightsJSON("{")
	if err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

func saveSearchEnv() func() {
	mode := os.Getenv(EnvSearchMode)
	preset := os.Getenv(EnvSearchPreset)
	weights := os.Getenv(EnvSearchWeights)
	return func() {
		os.Setenv(EnvSearchMode, mode)
		os.Setenv(EnvSearchPreset, preset)
		os.Setenv(EnvSearchWeights, weights)
	}
}

// A preset only affects hybrid ranking, so BV_SEARCH_PRESET alone must switch
// the mode to hybrid instead of being silently ignored under the text default.
func TestSearchConfigFromEnv_PresetImpliesHybrid(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)
	os.Unsetenv(EnvSearchMode)
	os.Unsetenv(EnvSearchWeights)

	os.Setenv(EnvSearchPreset, string(PresetImpactFirst))
	cfg, err := SearchConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != SearchModeHybrid || cfg.Preset != PresetImpactFirst {
		t.Fatalf("preset alone: mode=%q preset=%q, want hybrid/impact-first", cfg.Mode, cfg.Preset)
	}

	os.Setenv(EnvSearchPreset, string(PresetTextOnly))
	cfg, err = SearchConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != SearchModeText {
		t.Fatalf("text-only preset: mode=%q, want text", cfg.Mode)
	}

	// An explicit hybrid mode with a preset is unchanged.
	os.Setenv(EnvSearchMode, string(SearchModeHybrid))
	os.Setenv(EnvSearchPreset, string(PresetBugHunting))
	cfg, err = SearchConfigFromEnv()
	if err != nil || cfg.Mode != SearchModeHybrid || cfg.Preset != PresetBugHunting {
		t.Fatalf("explicit hybrid + preset: cfg=%+v err=%v", cfg, err)
	}

	// An explicit text mode with a hybrid preset is a contradiction.
	os.Setenv(EnvSearchMode, string(SearchModeText))
	if _, err := SearchConfigFromEnv(); err == nil {
		t.Fatal("BV_SEARCH_MODE=text with a hybrid preset must be rejected")
	}
}

func TestSearchConfigFromEnv_WeightsImpliesHybridAndRejectsExplicitText(t *testing.T) {
	restore := saveSearchEnv()
	t.Cleanup(restore)

	validWeights := `{"text":0.4,"pagerank":0.2,"status":0.15,"impact":0.1,"priority":0.1,"recency":0.05}`

	os.Unsetenv(EnvSearchMode)
	os.Unsetenv(EnvSearchPreset)
	os.Setenv(EnvSearchWeights, validWeights)

	cfg, err := SearchConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error for weights without explicit mode: %v", err)
	}
	if cfg.Mode != SearchModeHybrid {
		t.Fatalf("expected weights alone to auto-upgrade mode to hybrid, got %q", cfg.Mode)
	}
	if !cfg.HasWeights {
		t.Fatal("expected HasWeights=true")
	}

	// Explicit BV_SEARCH_MODE=text alongside BV_SEARCH_WEIGHTS is a contradiction.
	os.Setenv(EnvSearchMode, string(SearchModeText))
	if _, err := SearchConfigFromEnv(); err == nil {
		t.Fatal("expected error when BV_SEARCH_MODE=text is combined with BV_SEARCH_WEIGHTS")
	}
}

func TestNewEmbedderFromConfig_ModelDistinctEmbeddings(t *testing.T) {
	ctx := context.Background()
	texts := []string{"authentication token refresh bug"}

	embDefault, err := NewEmbedderFromConfig(EmbeddingConfig{Provider: ProviderHash, Dim: 64})
	if err != nil {
		t.Fatalf("default embedder: %v", err)
	}
	embDirect := NewHashEmbedder(64)

	embModelA, err := NewEmbedderFromConfig(EmbeddingConfig{Provider: ProviderHash, Model: "model-alpha", Dim: 64})
	if err != nil {
		t.Fatalf("model-alpha embedder: %v", err)
	}
	embModelB, err := NewEmbedderFromConfig(EmbeddingConfig{Provider: ProviderHash, Model: "model-beta", Dim: 64})
	if err != nil {
		t.Fatalf("model-beta embedder: %v", err)
	}

	vDefault, err := embDefault.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("embed default: %v", err)
	}
	vDirect, err := embDirect.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("embed direct: %v", err)
	}
	if !reflect.DeepEqual(vDefault, vDirect) {
		t.Fatal("empty-model embedder must match NewHashEmbedder bit-for-bit")
	}

	vA, err := embModelA.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("embed model-alpha: %v", err)
	}
	vB, err := embModelB.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("embed model-beta: %v", err)
	}

	if reflect.DeepEqual(vDefault, vA) {
		t.Fatal("expected non-empty BV_SEMANTIC_MODEL to produce distinct embeddings from default")
	}
	if reflect.DeepEqual(vA, vB) {
		t.Fatal("expected distinct BV_SEMANTIC_MODEL values to produce distinct embeddings")
	}
}

// =============================================================================
// NewEmbedderFromConfig Tests
// =============================================================================

func TestNewEmbedderFromConfig(t *testing.T) {
	tests := []struct {
		name        string
		cfg         EmbeddingConfig
		wantErr     bool
		errContains string
		checkEmbed  func(t *testing.T, e Embedder)
	}{
		{
			name:    "empty provider defaults to hash",
			cfg:     EmbeddingConfig{Provider: "", Dim: 256},
			wantErr: false,
			checkEmbed: func(t *testing.T, e Embedder) {
				if e.Provider() != ProviderHash {
					t.Errorf("Provider() = %q, want %q", e.Provider(), ProviderHash)
				}
				if e.Dim() != 256 {
					t.Errorf("Dim() = %d, want 256", e.Dim())
				}
			},
		},
		{
			name:    "hash provider explicit",
			cfg:     EmbeddingConfig{Provider: ProviderHash, Dim: 128},
			wantErr: false,
			checkEmbed: func(t *testing.T, e Embedder) {
				if e.Provider() != ProviderHash {
					t.Errorf("Provider() = %q, want %q", e.Provider(), ProviderHash)
				}
				if e.Dim() != 128 {
					t.Errorf("Dim() = %d, want 128", e.Dim())
				}
			},
		},
		{
			name:    "hash provider with zero dim uses default",
			cfg:     EmbeddingConfig{Provider: ProviderHash, Dim: 0},
			wantErr: false,
			checkEmbed: func(t *testing.T, e Embedder) {
				if e.Dim() != DefaultEmbeddingDim {
					t.Errorf("Dim() = %d, want %d", e.Dim(), DefaultEmbeddingDim)
				}
			},
		},
		{
			name:        "python-sentence-transformers not implemented",
			cfg:         EmbeddingConfig{Provider: ProviderPythonSentenceTransformers, Dim: 384},
			wantErr:     true,
			errContains: "not implemented",
		},
		{
			name:        "openai not implemented",
			cfg:         EmbeddingConfig{Provider: ProviderOpenAI, Dim: 1536},
			wantErr:     true,
			errContains: "not implemented",
		},
		{
			name:        "unknown provider error",
			cfg:         EmbeddingConfig{Provider: "unknown-provider", Dim: 384},
			wantErr:     true,
			errContains: "unknown semantic embedder",
		},
		{
			name:        "error message suggests hash fallback",
			cfg:         EmbeddingConfig{Provider: ProviderOpenAI},
			wantErr:     true,
			errContains: ProviderHash.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := NewEmbedderFromConfig(tt.cfg)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if e == nil {
				t.Fatal("expected embedder, got nil")
			}
			if tt.checkEmbed != nil {
				tt.checkEmbed(t, e)
			}
		})
	}
}

// =============================================================================
// EmbeddingConfig.Normalized Tests
// =============================================================================

func TestEmbeddingConfig_Normalized(t *testing.T) {
	tests := []struct {
		name        string
		cfg         EmbeddingConfig
		expectedDim int
	}{
		{
			name:        "zero dim becomes default",
			cfg:         EmbeddingConfig{Dim: 0},
			expectedDim: DefaultEmbeddingDim,
		},
		{
			name:        "negative dim becomes default",
			cfg:         EmbeddingConfig{Dim: -100},
			expectedDim: DefaultEmbeddingDim,
		},
		{
			name:        "positive dim unchanged",
			cfg:         EmbeddingConfig{Dim: 512},
			expectedDim: 512,
		},
		{
			name:        "dim of 1 unchanged",
			cfg:         EmbeddingConfig{Dim: 1},
			expectedDim: 1,
		},
		{
			name:        "large dim unchanged",
			cfg:         EmbeddingConfig{Dim: 4096},
			expectedDim: 4096,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.cfg.Normalized()
			if result.Dim != tt.expectedDim {
				t.Errorf("Normalized().Dim = %d, want %d", result.Dim, tt.expectedDim)
			}
		})
	}
}

// =============================================================================
// Provider Constants Tests
// =============================================================================

func TestProviderConstants(t *testing.T) {
	// Verify constants have expected values for documentation
	tests := []struct {
		provider Provider
		expected string
	}{
		{ProviderHash, "hash"},
		{ProviderPythonSentenceTransformers, "python-sentence-transformers"},
		{ProviderOpenAI, "openai"},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			if string(tt.provider) != tt.expected {
				t.Errorf("Provider constant = %q, want %q", tt.provider, tt.expected)
			}
		})
	}
}

func TestEnvironmentVariableConstants(t *testing.T) {
	// Verify env var names follow expected pattern
	if EnvSemanticEmbedder != "BV_SEMANTIC_EMBEDDER" {
		t.Errorf("EnvSemanticEmbedder = %q, want %q", EnvSemanticEmbedder, "BV_SEMANTIC_EMBEDDER")
	}
	if EnvSemanticModel != "BV_SEMANTIC_MODEL" {
		t.Errorf("EnvSemanticModel = %q, want %q", EnvSemanticModel, "BV_SEMANTIC_MODEL")
	}
	if EnvSemanticDim != "BV_SEMANTIC_DIM" {
		t.Errorf("EnvSemanticDim = %q, want %q", EnvSemanticDim, "BV_SEMANTIC_DIM")
	}
	if EnvSearchMode != "BV_SEARCH_MODE" {
		t.Errorf("EnvSearchMode = %q, want %q", EnvSearchMode, "BV_SEARCH_MODE")
	}
	if EnvSearchPreset != "BV_SEARCH_PRESET" {
		t.Errorf("EnvSearchPreset = %q, want %q", EnvSearchPreset, "BV_SEARCH_PRESET")
	}
	if EnvSearchWeights != "BV_SEARCH_WEIGHTS" {
		t.Errorf("EnvSearchWeights = %q, want %q", EnvSearchWeights, "BV_SEARCH_WEIGHTS")
	}
}

func TestDefaultEmbeddingDim(t *testing.T) {
	// Verify default dim is 384 (common sentence-transformers dimension)
	if DefaultEmbeddingDim != 384 {
		t.Errorf("DefaultEmbeddingDim = %d, want 384", DefaultEmbeddingDim)
	}
}

// Helper for Provider string conversion in tests
func (p Provider) String() string {
	return string(p)
}
