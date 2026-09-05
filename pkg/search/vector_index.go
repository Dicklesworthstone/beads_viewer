package search

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/beads_viewer/pkg/util/topk"
)

const (
	vectorIndexMagic        = "BVVI"
	vectorIndexVersion      = uint16(1)
	vectorIndexHeaderSize   = int64(16)
	maxVectorIndexDimension = uint32(1 << 20) // 4 MiB per float32 vector
	maxVectorIndexEntries   = uint32(1 << 20)
	maxVectorIndexFileSize  = int64(512 << 20)
	minVectorEntryOverhead  = int64(2 + 1 + sha256.Size) // id length, non-empty id, content hash
)

type ContentHash [32]byte

func ComputeContentHash(text string) ContentHash {
	return sha256.Sum256([]byte(text))
}

func (h ContentHash) Hex() string {
	return hex.EncodeToString(h[:])
}

func ParseContentHashHex(s string) (ContentHash, error) {
	var out ContentHash
	if len(s) != 64 {
		return out, fmt.Errorf("invalid content hash length: %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("decode content hash: %w", err)
	}
	copy(out[:], b)
	return out, nil
}

type VectorEntry struct {
	ContentHash ContentHash
	Vector      []float32
}

type VectorIndex struct {
	Dim int

	mu       sync.RWMutex
	entries  map[string]VectorEntry
	idsCache []string
	idsDirty bool
	mutation uint64
}

func NewVectorIndex(dim int) *VectorIndex {
	if dim <= 0 {
		dim = DefaultEmbeddingDim
	}
	return &VectorIndex{
		Dim:      dim,
		entries:  make(map[string]VectorEntry),
		idsDirty: true,
	}
}

func LoadVectorIndex(path string) (*VectorIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat vector index: %w", err)
	}
	if info.Size() < vectorIndexHeaderSize {
		return nil, fmt.Errorf("vector index too small: %d bytes", info.Size())
	}
	if info.Size() > maxVectorIndexFileSize {
		return nil, fmt.Errorf("vector index too large: %d bytes (max %d)", info.Size(), maxVectorIndexFileSize)
	}

	r := bufio.NewReader(f)

	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if string(magic[:]) != vectorIndexMagic {
		return nil, fmt.Errorf("invalid magic %q", string(magic[:]))
	}

	var version uint16
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version != vectorIndexVersion {
		return nil, fmt.Errorf("unsupported version %d", version)
	}

	// Reserved uint16
	var _reserved uint16
	if err := binary.Read(r, binary.LittleEndian, &_reserved); err != nil {
		return nil, fmt.Errorf("read reserved: %w", err)
	}

	var dimU32 uint32
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &dimU32); err != nil {
		return nil, fmt.Errorf("read dim: %w", err)
	}
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("read count: %w", err)
	}
	if dimU32 == 0 {
		return nil, fmt.Errorf("invalid dim 0")
	}
	if dimU32 > maxVectorIndexDimension {
		return nil, fmt.Errorf("vector dimension %d exceeds maximum %d", dimU32, maxVectorIndexDimension)
	}
	if count > maxVectorIndexEntries {
		return nil, fmt.Errorf("vector entry count %d exceeds maximum %d", count, maxVectorIndexEntries)
	}
	if count > 0 {
		minEntrySize := minVectorEntryOverhead + int64(dimU32)*4
		available := info.Size() - vectorIndexHeaderSize
		if int64(count) > available/minEntrySize {
			return nil, fmt.Errorf("vector index header declares %d entries but file can contain at most %d", count, available/minEntrySize)
		}
	}

	idx := NewVectorIndex(int(dimU32))
	for i := uint32(0); i < count; i++ {
		var idLen uint16
		if err := binary.Read(r, binary.LittleEndian, &idLen); err != nil {
			return nil, fmt.Errorf("read id len: %w", err)
		}
		if idLen == 0 {
			return nil, fmt.Errorf("empty issue id")
		}

		idBytes := make([]byte, idLen)
		if _, err := io.ReadFull(r, idBytes); err != nil {
			return nil, fmt.Errorf("read id: %w", err)
		}
		issueID := string(idBytes)
		if _, exists := idx.entries[issueID]; exists {
			return nil, fmt.Errorf("duplicate issue id %q", issueID)
		}

		var ch ContentHash
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return nil, fmt.Errorf("read content hash: %w", err)
		}

		vec := make([]float32, idx.Dim)
		for j := 0; j < idx.Dim; j++ {
			var bits uint32
			if err := binary.Read(r, binary.LittleEndian, &bits); err != nil {
				return nil, fmt.Errorf("read vector: %w", err)
			}
			vec[j] = math.Float32frombits(bits)
		}

		if err := idx.Upsert(issueID, ch, vec); err != nil {
			return nil, err
		}
	}
	var trailing [1]byte
	if n, err := r.Read(trailing[:]); n != 0 {
		return nil, fmt.Errorf("unexpected trailing data")
	} else if err != io.EOF {
		return nil, fmt.Errorf("check trailing data: %w", err)
	}

	return idx, nil
}

func (idx *VectorIndex) Save(path string) error {
	// Hold a single write lock for the entire operation to prevent a TOCTOU race:
	// previously, sortedIDs() released its lock before Save re-acquired RLock,
	// so a concurrent Remove could make the header entry count disagree with the
	// number of entries actually written, corrupting the file on Load.
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.Dim <= 0 || uint64(idx.Dim) > uint64(math.MaxUint32) {
		return fmt.Errorf("index dim %d is outside serializable uint32 range", idx.Dim)
	}
	if uint64(len(idx.entries)) > uint64(math.MaxUint32) {
		return fmt.Errorf("index entry count %d exceeds serializable uint32 range", len(idx.entries))
	}

	// Rebuild the sorted-ID cache inline (mirrors sortedIDs logic).
	if idx.idsDirty || idx.idsCache == nil {
		ids := make([]string, 0, len(idx.entries))
		for id := range idx.entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		idx.idsCache = ids
		idx.idsDirty = false
	}
	ids := idx.idsCache
	if idx.Dim <= 0 || uint64(idx.Dim) > uint64(maxVectorIndexDimension) {
		return fmt.Errorf("vector dimension %d exceeds supported range 1..%d", idx.Dim, maxVectorIndexDimension)
	}
	if uint64(len(ids)) > uint64(maxVectorIndexEntries) {
		return fmt.Errorf("vector entry count %d exceeds maximum %d", len(ids), maxVectorIndexEntries)
	}

	encodedSize := vectorIndexHeaderSize
	for _, issueID := range ids {
		entry, ok := idx.entries[issueID]
		if !ok {
			continue
		}
		if len(issueID) > math.MaxUint16 {
			return fmt.Errorf("issue id too long: %d", len(issueID))
		}
		if len(entry.Vector) != idx.Dim {
			return fmt.Errorf("vector dim mismatch for %s: %d != %d", issueID, len(entry.Vector), idx.Dim)
		}
		encodedSize += int64(2+len(issueID)+sha256.Size) + int64(idx.Dim)*4
		if encodedSize > maxVectorIndexFileSize {
			return fmt.Errorf("encoded vector index exceeds maximum size %d", maxVectorIndexFileSize)
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "bvvi-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	w := bufio.NewWriter(tmp)

	if _, err := w.WriteString(vectorIndexMagic); err != nil {
		return fmt.Errorf("write magic: %w", err)
	}
	if err := binary.Write(w, binary.LittleEndian, vectorIndexVersion); err != nil {
		return fmt.Errorf("write version: %w", err)
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(0)); err != nil {
		return fmt.Errorf("write reserved: %w", err)
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(idx.Dim)); err != nil {
		return fmt.Errorf("write dim: %w", err)
	}

	if err := binary.Write(w, binary.LittleEndian, uint32(len(ids))); err != nil {
		return fmt.Errorf("write count: %w", err)
	}

	for _, issueID := range ids {
		entry, ok := idx.entries[issueID]
		if !ok {
			continue
		}

		if err := binary.Write(w, binary.LittleEndian, uint16(len(issueID))); err != nil {
			return fmt.Errorf("write id len: %w", err)
		}
		if _, err := w.WriteString(issueID); err != nil {
			return fmt.Errorf("write id: %w", err)
		}
		if _, err := w.Write(entry.ContentHash[:]); err != nil {
			return fmt.Errorf("write content hash: %w", err)
		}
		for _, v := range entry.Vector {
			if err := binary.Write(w, binary.LittleEndian, math.Float32bits(v)); err != nil {
				return fmt.Errorf("write vector: %w", err)
			}
		}
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// os.Rename doesn't replace existing files on Windows. Since the index is deterministic
		// and can be rebuilt, fall back to removing the destination and retrying.
		if runtime.GOOS == "windows" {
			if _, statErr := os.Stat(path); statErr == nil {
				if rmErr := os.Remove(path); rmErr != nil {
					return fmt.Errorf("remove existing index: %w", rmErr)
				}
				if err2 := os.Rename(tmpPath, path); err2 == nil {
					return nil
				} else {
					return fmt.Errorf("rename: %w", err2)
				}
			}
		}
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (idx *VectorIndex) Upsert(issueID string, hash ContentHash, vec []float32) error {
	if issueID == "" {
		return fmt.Errorf("issue id cannot be empty")
	}
	if len(vec) != idx.Dim {
		return fmt.Errorf("vector dim mismatch: %d != %d", len(vec), idx.Dim)
	}
	if err := validateFiniteVector(vec); err != nil {
		return err
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	_, exists := idx.entries[issueID]
	cp := make([]float32, len(vec))
	copy(cp, vec)
	idx.entries[issueID] = VectorEntry{
		ContentHash: hash,
		Vector:      cp,
	}
	idx.mutation++
	if !exists {
		idx.idsDirty = true
	}
	return nil
}

func (idx *VectorIndex) Remove(issueID string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, ok := idx.entries[issueID]; !ok {
		return
	}
	delete(idx.entries, issueID)
	idx.mutation++
	idx.idsDirty = true
}

func (idx *VectorIndex) Get(issueID string) (VectorEntry, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	e, ok := idx.entries[issueID]
	if !ok {
		return VectorEntry{}, false
	}
	e.Vector = cloneVector(e.Vector)
	return e, true
}

func (idx *VectorIndex) contentHash(issueID string) (ContentHash, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	e, ok := idx.entries[issueID]
	if !ok {
		return ContentHash{}, false
	}
	return e.ContentHash, true
}

func (idx *VectorIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

func (idx *VectorIndex) sortedIDs() []string {
	idx.mu.RLock()
	if !idx.idsDirty && idx.idsCache != nil {
		defer idx.mu.RUnlock()
		return idx.idsCache
	}
	idx.mu.RUnlock()

	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Double check after acquiring write lock
	if !idx.idsDirty && idx.idsCache != nil {
		return idx.idsCache
	}

	ids := make([]string, 0, len(idx.entries))
	for id := range idx.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	idx.idsCache = ids
	idx.idsDirty = false
	return ids
}

type SearchResult struct {
	IssueID string  `json:"issue_id"`
	Score   float64 `json:"score"`
	// ExactIDMatch survives subsequent lexical/hybrid ranking without resolving
	// a potentially ambiguous case-fold match from an already truncated list.
	ExactIDMatch bool `json:"-"`
}

// VectorSearchOptions restricts eligible records before scoring and top-k.
// Nil Eligible selects the entire index; an empty map selects no records.
// MinScore is an inclusive raw text-similarity threshold, before hybrid scoring.
// ScoreBoosts adds finite nonnegative lexical evidence after that threshold and
// before top-k selection, so a prefix match cannot be discarded before ranking.
type VectorSearchOptions struct {
	Eligible    map[string]bool
	ExactID     string
	MinScore    *float64
	ScoreBoosts map[string]float64
}

func (idx *VectorIndex) SearchTopK(query []float32, k int) ([]SearchResult, error) {
	return idx.SearchTopKWithOptions(query, k, VectorSearchOptions{})
}

// SearchTopKWithExactID returns the top semantic matches while guaranteeing
// that an exact issue-ID match is present and ranked first. Issue IDs are
// opaque, so matching makes no assumptions about their punctuation or
// tracker-specific shape. A case-insensitive match is promoted only when it is
// unambiguous; an exact-case match always wins.
func (idx *VectorIndex) SearchTopKWithExactID(query []float32, k int, exactID string) ([]SearchResult, error) {
	return idx.SearchTopKWithOptions(query, k, VectorSearchOptions{ExactID: exactID})
}

func (idx *VectorIndex) SearchTopKWithOptions(query []float32, k int, opts VectorSearchOptions) ([]SearchResult, error) {
	exactID := strings.TrimSpace(opts.ExactID)
	if opts.MinScore != nil && (math.IsNaN(*opts.MinScore) || math.IsInf(*opts.MinScore, 0)) {
		return nil, fmt.Errorf("minimum score must be finite")
	}
	for id, boost := range opts.ScoreBoosts {
		if math.IsNaN(boost) || math.IsInf(boost, 0) || boost < 0 {
			return nil, fmt.Errorf("score boost for %q must be finite and nonnegative", id)
		}
	}
	if k <= 0 {
		return nil, nil
	}
	if len(query) != idx.Dim {
		return nil, fmt.Errorf("query dim mismatch: %d != %d", len(query), idx.Dim)
	}
	if err := validateFiniteVector(query); err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	// sortedIDs now handles its own locking safely
	ids := idx.sortedIDs()
	// k is caller-controlled in CLI use. Bound heap capacity to the number of
	// indexed entries so an oversized limit cannot trigger an enormous allocation.
	if k > len(ids) {
		k = len(ids)
	}
	if k == 0 {
		return nil, nil
	}

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// Use heap-based top-K collector: O(n log k) vs O(nk) for linear insert
	collector := topk.New[SearchResult](k, func(a, b SearchResult) bool {
		return a.IssueID < b.IssueID // Deterministic tie-breaking: smaller ID wins
	})
	var exactCaseResult SearchResult
	hasExactCase := false
	var foldedResult SearchResult
	foldedMatches := 0

	for _, issueID := range ids {
		if opts.Eligible != nil && !opts.Eligible[issueID] {
			continue
		}
		entry, ok := idx.entries[issueID]
		if !ok {
			// This can happen if the issue was removed concurrently between sortedIDs() and RLock()
			continue
		}
		score := dotFloat32(query, entry.Vector)
		if opts.MinScore != nil && score < *opts.MinScore {
			continue
		}
		score += opts.ScoreBoosts[issueID]
		result := SearchResult{IssueID: issueID, Score: score}
		collector.Add(result, score)
		if exactID != "" {
			switch {
			case issueID == exactID:
				exactCaseResult = result
				hasExactCase = true
			case strings.EqualFold(issueID, exactID):
				foldedResult = result
				foldedMatches++
			}
		}
	}

	results := collector.Results()
	exactResult := exactCaseResult
	if !hasExactCase {
		if foldedMatches != 1 {
			return results, nil
		}
		exactResult = foldedResult
	}
	if exactResult.IssueID == "" {
		return results, nil
	}
	exactResult.ExactIDMatch = true

	exactIndex := -1
	for i := range results {
		if results[i].IssueID == exactResult.IssueID {
			exactIndex = i
			break
		}
	}
	if exactIndex < 0 {
		if len(results) < k {
			results = append(results, exactResult)
			exactIndex = len(results) - 1
		} else {
			results[len(results)-1] = exactResult
			exactIndex = len(results) - 1
		}
	}
	if exactIndex > 0 {
		copy(results[1:exactIndex+1], results[:exactIndex])
		results[0] = exactResult
	} else if exactIndex == 0 {
		results[0] = exactResult
	}
	return results, nil
}

func validateFiniteVector(vec []float32) error {
	for i, value := range vec {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("vector component %d must be finite", i)
		}
	}
	return nil
}

func dotFloat32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func cloneVector(vec []float32) []float32 {
	if len(vec) == 0 {
		return nil
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	return cp
}
