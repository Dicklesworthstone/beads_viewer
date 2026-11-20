package search

import (
	"context"
	"math"
	"strings"
	"unicode"
)

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// HashEmbedder is a dependency-free embedding fallback.
// It uses feature hashing over tokens into a fixed-size dense vector.
type HashEmbedder struct {
	dim   int
	model string
	seed  uint64
}

func NewHashEmbedder(dim int) *HashEmbedder {
	return NewHashEmbedderWithModel(dim, "")
}

// NewHashEmbedderWithModel constructs a HashEmbedder whose feature-hash token
// seeding incorporates model when non-empty. When model is empty, token hashing
// uses the standard FNV-1a offset basis for bit-identical default embeddings.
func NewHashEmbedderWithModel(dim int, model string) *HashEmbedder {
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	model = strings.TrimSpace(model)
	seed := hashModelSeed(model)
	return &HashEmbedder{
		dim:   dim,
		model: model,
		seed:  seed,
	}
}

func hashModelSeed(model string) uint64 {
	if model == "" {
		return fnvOffset64
	}
	h := fnvOffset64
	for i := 0; i < len(model); i++ {
		h ^= uint64(model[i])
		h *= fnvPrime64
	}
	h ^= 0
	h *= fnvPrime64
	return h
}

func (*HashEmbedder) Provider() Provider { return ProviderHash }
func (h *HashEmbedder) Dim() int         { return h.dim }
func (h *HashEmbedder) Model() string    { return h.model }

func (h *HashEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	seed := h.seed
	if seed == 0 && h.model == "" {
		seed = fnvOffset64
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vec := make([]float32, h.dim)
		hashEmbedIntoWithSeed(vec, seed, text)
		normalizeL2(vec)
		out[i] = vec
	}
	return out, nil
}

func hashEmbedIntoWithSeed(vec []float32, seed uint64, text string) {
	if len(vec) == 0 {
		return
	}
	dim := uint64(len(vec))

	tokenStart := -1
	for idx, r := range text {
		isTokenChar := unicode.IsLetter(r) || unicode.IsDigit(r)
		if isTokenChar {
			if tokenStart == -1 {
				tokenStart = idx
			}
			continue
		}
		if tokenStart != -1 {
			addHashedTokenWithSeed(vec, dim, seed, text[tokenStart:idx])
			tokenStart = -1
		}
	}
	if tokenStart != -1 {
		addHashedTokenWithSeed(vec, dim, seed, text[tokenStart:])
	}
}

func addHashedTokenWithSeed(vec []float32, dim uint64, seed uint64, token string) {
	// FNV-1a 64-bit over UTF-8 bytes, seeded by model when configured.
	h := seed
	for i := 0; i < len(token); i++ {
		b := token[i]
		// Lowercase ASCII fast-path; unicode case folding isn't worth it for a fallback embedder.
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		h ^= uint64(b)
		h *= fnvPrime64
	}

	idx := int(h % dim)
	sign := float32(1.0)
	if (h>>63)&1 == 1 {
		sign = -1.0
	}
	vec[idx] += sign
}

func normalizeL2(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return
	}
	scale := float32(1.0 / math.Sqrt(sum))
	for i := range vec {
		vec[i] *= scale
	}
}
