package search

import (
	"context"
	"strings"
)

// Provider identifies an embedding backend.
type Provider string

const (
	// ProviderHash is a pure-Go, dependency-free hashed-token embedder intended as a
	// deterministic fallback (and for tests). It is not a true "semantic" model.
	ProviderHash Provider = "hash"

	// ProviderPythonSentenceTransformers uses a Python subprocess running
	// sentence-transformers to generate high-quality embeddings (MVP choice for bv-9gf).
	ProviderPythonSentenceTransformers Provider = "python-sentence-transformers"

	// ProviderOpenAI uses a hosted embedding API.
	ProviderOpenAI Provider = "openai"
)

const DefaultEmbeddingDim = 384

const (
	EnvSemanticEmbedder = "BV_SEMANTIC_EMBEDDER"
	EnvSemanticModel    = "BV_SEMANTIC_MODEL"
	EnvSemanticDim      = "BV_SEMANTIC_DIM"
)

// EmbeddingConfig captures embedder selection/configuration.
// Provider implementations may ignore fields they don't use.
type EmbeddingConfig struct {
	Provider Provider
	Model    string
	Dim      int
}

func (c EmbeddingConfig) Normalized() EmbeddingConfig {
	c.Model = strings.TrimSpace(c.Model)
	if c.Dim <= 0 {
		c.Dim = DefaultEmbeddingDim
	}
	return c
}

// Embedder produces fixed-size dense vectors for text inputs.
type Embedder interface {
	Provider() Provider
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
