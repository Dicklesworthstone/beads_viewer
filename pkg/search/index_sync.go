package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Dicklesworthstone/beads_viewer/pkg/metrics"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultIndexPath returns the default semantic index path under the given project directory.
// The filename is keyed by provider(+model)+dim to avoid mixing incompatible embeddings.
func DefaultIndexPath(projectDir string, cfg EmbeddingConfig) string {
	cfg = cfg.Normalized()
	provider := cfg.Provider
	if provider == "" {
		provider = ProviderHash
	}
	safeProvider := sanitizeFilenameSlug(string(provider), "hash")
	if cfg.Model == "" {
		return filepath.Join(projectDir, ".bv", "semantic", fmt.Sprintf("index-%s-%d.bvvi", safeProvider, cfg.Dim))
	}
	safeModel := sanitizeModelFilenameSlug(cfg.Model)
	return filepath.Join(projectDir, ".bv", "semantic", fmt.Sprintf("index-%s-%s-%d.bvvi", safeProvider, safeModel, cfg.Dim))
}

// IndexPathForScope returns the semantic index path for an optional scopeKey
// (such as a multi-repo --workspace or custom --db/--beads-file source).
// When scopeKey is empty/whitespace, it returns DefaultIndexPath unchanged so
// default single-repo callers continue to use the canonical .bvvi file.
func IndexPathForScope(projectDir string, cfg EmbeddingConfig, scopeKey string) string {
	trimmedScope := strings.TrimSpace(scopeKey)
	if trimmedScope == "" {
		return DefaultIndexPath(projectDir, cfg)
	}
	basePath := DefaultIndexPath(projectDir, cfg)
	sum := sha256.Sum256([]byte(trimmedScope))
	shortHash := hex.EncodeToString(sum[:6])
	return strings.TrimSuffix(basePath, ".bvvi") + "-" + shortHash + ".bvvi"
}

func sanitizeFilenameSlug(raw string, fallback string) string {
	var b strings.Builder
	b.Grow(len(raw))
	lastUnderscore := false
	for _, r := range raw {
		isSafe := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_'
		if isSafe {
			b.WriteRune(r)
			lastUnderscore = (r == '_')
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	slug := strings.Trim(b.String(), "_.")
	if slug == "" {
		return fallback
	}
	return slug
}

func sanitizeModelFilenameSlug(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	slug := sanitizeFilenameSlug(model, "model")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "_-")
		if slug == "" {
			slug = "model"
		}
	}
	// Always append a deterministic FNV-1a hash suffix of the trimmed model name
	// so sanitized slugs (e.g. "org/v1" -> "org_v1-<h1 >") can never collide with
	// literal model identifiers matching that sanitized prefix ("org_v1-<h1 >" -> "org_v1-<h1 >-<h2 >").
	return fmt.Sprintf("%s-%08x", slug, uint32(hashModelSeed(model)))
}

type IndexSyncStats struct {
	Total    int `json:"total"`
	Added    int `json:"added"`
	Updated  int `json:"updated"`
	Removed  int `json:"removed"`
	Skipped  int `json:"skipped"`
	Embedded int `json:"embedded"`
}

func (s IndexSyncStats) Changed() bool {
	return s.Added+s.Updated+s.Removed > 0
}

// LoadOrNewVectorIndex loads an existing vector index if present, otherwise creates a new one.
// If loading fails due to corruption, it backs up the corrupt file and returns a new empty index.
func LoadOrNewVectorIndex(path string, dim int) (*VectorIndex, bool, error) {
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	idx, err := LoadVectorIndex(path)
	if err == nil && idx.Dim == dim {
		metrics.SearchCache.Hit()
		return idx, true, nil
	}
	if err == nil {
		// A stored index of another dimension is unusable for this embedder;
		// treat it like a corrupt file so it is backed up and rebuilt.
		err = fmt.Errorf("index dim %d does not match requested dim %d", idx.Dim, dim)
	}
	metrics.SearchCache.Miss()

	if os.IsNotExist(err) {
		return NewVectorIndex(dim), false, nil
	}

	// File exists but failed to load - likely corrupt
	// Attempt to back it up
	// Preserve every recovery artifact. A seconds-only suffix let two corrupt
	// loads in the same second reuse the same destination; on Unix, Rename then
	// replaced the first backup and destroyed the best evidence of the earlier
	// failure.
	backupPath := path + ".corrupt-" + fmt.Sprintf("%d", time.Now().UnixNano())
	if renameErr := os.Rename(path, backupPath); renameErr == nil {
		// Successfully backed up, return new index
		// Note: We return error as nil here because we recovered, but the caller might want to know?
		// We'll return false for loaded, and nil for error, effectively resetting.
		// Ideally we'd log this, but this function is low-level.
		// Let's rely on the fact that 'loaded=false' implies we started fresh.
		return NewVectorIndex(dim), false, nil
	}

	// If rename failed (e.g. permissions), we return the original error
	return nil, false, fmt.Errorf("load vector index (and backup failed): %w", err)
}

// SyncVectorIndex updates idx to match docs using embedder, incrementally embedding only changed items.
//
// This is intended for offline, deterministic embedding providers. Callers should persist idx
// with (*VectorIndex).Save when desired.
func SyncVectorIndex(ctx context.Context, idx *VectorIndex, embedder Embedder, docs map[string]string, batchSize int) (IndexSyncStats, error) {
	stats := IndexSyncStats{Total: len(docs)}
	if idx == nil {
		return stats, fmt.Errorf("index cannot be nil")
	}
	if embedder == nil {
		return stats, fmt.Errorf("embedder cannot be nil")
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	if batchSize <= 0 {
		batchSize = 32
	}

	// Snapshot the current index once. Embedding may involve multiple batches or
	// an external provider, so do not mutate the live index until every vector
	// needed for the new state has been produced and validated successfully.
	idx.mu.RLock()
	indexDim := idx.Dim
	indexMutation := idx.mutation
	existingEntries := make(map[string]VectorEntry, len(idx.entries))
	for id, entry := range idx.entries {
		existingEntries[id] = entry
	}
	idx.mu.RUnlock()

	embedderDim := embedder.Dim()
	if indexDim != embedderDim {
		return stats, fmt.Errorf("index dim %d does not match embedder dim %d", indexDim, embedderDim)
	}

	// Determine which docs need embedding (deterministic order).
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	toEmbedIDs := make([]string, 0)
	toEmbedTexts := make([]string, 0)
	toEmbedHashes := make([]ContentHash, 0)
	targetEntries := make(map[string]VectorEntry, len(docs))

	for _, id := range ids {
		if id == "" {
			return stats, fmt.Errorf("issue id cannot be empty")
		}
		text := docs[id]
		ch := ComputeContentHash(text)
		existing, ok := existingEntries[id]
		if ok && existing.ContentHash == ch {
			stats.Skipped++
			targetEntries[id] = existing
			continue
		}
		if ok {
			stats.Updated++
		} else {
			stats.Added++
		}
		toEmbedIDs = append(toEmbedIDs, id)
		toEmbedTexts = append(toEmbedTexts, text)
		toEmbedHashes = append(toEmbedHashes, ch)
	}
	stats.Removed = len(existingEntries) - (stats.Skipped + stats.Updated)
	if err := ctx.Err(); err != nil {
		return IndexSyncStats{Total: stats.Total}, err
	}
	if !stats.Changed() {
		idx.mu.RLock()
		unchanged := idx.Dim == indexDim && idx.mutation == indexMutation
		idx.mu.RUnlock()
		if !unchanged {
			return IndexSyncStats{Total: stats.Total}, fmt.Errorf("index changed concurrently during sync")
		}
		return stats, nil
	}

	// Embed in batches, staging validated copies in the target map. This keeps a
	// provider error, cancellation, or malformed vector from exposing a partially
	// synchronized in-memory index.
	for start := 0; start < len(toEmbedTexts); start += batchSize {
		if err := ctx.Err(); err != nil {
			return IndexSyncStats{Total: stats.Total}, err
		}
		end := start + batchSize
		if end > len(toEmbedTexts) {
			end = len(toEmbedTexts)
		}
		vecs, err := embedder.Embed(ctx, toEmbedTexts[start:end])
		if err != nil {
			return IndexSyncStats{Total: stats.Total}, fmt.Errorf("embed issues %d-%d: %w", start, end, err)
		}
		if len(vecs) != end-start {
			return IndexSyncStats{Total: stats.Total}, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), end-start)
		}
		for i, vec := range vecs {
			if len(vec) != indexDim {
				return IndexSyncStats{Total: stats.Total}, fmt.Errorf("vector dim mismatch for %s: %d != %d", toEmbedIDs[start+i], len(vec), indexDim)
			}
			if err := validateFiniteVector(vec); err != nil {
				return IndexSyncStats{Total: stats.Total}, fmt.Errorf("invalid vector for %s: %w", toEmbedIDs[start+i], err)
			}
			targetEntries[toEmbedIDs[start+i]] = VectorEntry{
				ContentHash: toEmbedHashes[start+i],
				Vector:      cloneVector(vec),
			}
			stats.Embedded++
		}
	}
	if err := ctx.Err(); err != nil {
		return IndexSyncStats{Total: stats.Total}, err
	}

	// Publish the complete target state with one lock acquisition. Besides the
	// all-or-nothing error behavior, this avoids one lock/unlock pair per changed
	// or removed issue on large synchronizations.
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.Dim != indexDim {
		return IndexSyncStats{Total: stats.Total}, fmt.Errorf("index dim changed during sync: %d != %d", idx.Dim, indexDim)
	}
	if idx.mutation != indexMutation {
		return IndexSyncStats{Total: stats.Total}, fmt.Errorf("index changed concurrently during sync")
	}
	idx.entries = targetEntries
	idx.idsCache = nil
	idx.idsDirty = true
	idx.mutation++
	return stats, nil
}
