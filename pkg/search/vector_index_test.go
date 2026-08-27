package search

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestVectorIndex_SaveLoad_RoundTrip(t *testing.T) {
	idx := NewVectorIndex(4)

	if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := idx.Upsert("B", ComputeContentHash("b"), []float32{0, 1, 0, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	path := filepath.Join(t.TempDir(), "semantic", "index.bvvi")
	if err := idx.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := LoadVectorIndex(path)
	if err != nil {
		t.Fatalf("LoadVectorIndex failed: %v", err)
	}
	if loaded.Dim != 4 {
		t.Fatalf("Dim mismatch: got %d want %d", loaded.Dim, 4)
	}
	if loaded.Size() != 2 {
		t.Fatalf("Size mismatch: got %d want %d", loaded.Size(), 2)
	}

	a, ok := loaded.Get("A")
	if !ok {
		t.Fatalf("Expected entry A")
	}
	if a.ContentHash != ComputeContentHash("a") {
		t.Fatalf("Content hash mismatch for A")
	}
	if len(a.Vector) != 4 || a.Vector[0] != 1 {
		t.Fatalf("Vector mismatch for A: %#v", a.Vector)
	}
}

func TestVectorIndexSaveRejectsMutatedInvalidDimension(t *testing.T) {
	idx := NewVectorIndex(2)
	idx.Dim = 0
	if err := idx.Save(filepath.Join(t.TempDir(), "index.bvvi")); err == nil {
		t.Fatal("expected invalid exported dimension to be rejected")
	}
}

func TestLoadVectorIndexRejectsImpossibleDeclaredBodyBeforeAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.bvvi")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(vectorIndexMagic); err != nil {
		t.Fatalf("write magic: %v", err)
	}
	for _, value := range []any{vectorIndexVersion, uint16(0), uint32(math.MaxUint32), uint32(1)} {
		if err := binary.Write(f, binary.LittleEndian, value); err != nil {
			t.Fatalf("write header: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := LoadVectorIndex(path); err == nil {
		t.Fatal("expected impossible declared body to be rejected")
	}
}

func TestLoadVectorIndexRejectsDuplicateIDsAndTrailingData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, path string)
	}{
		{
			name: "duplicate IDs",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile: %v", err)
				}
				binary.LittleEndian.PutUint32(data[12:16], 2)
				data = append(data, data[16:]...)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			},
		},
		{
			name: "trailing data",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatalf("OpenFile: %v", err)
				}
				if _, err := f.Write([]byte{0xff}); err != nil {
					_ = f.Close()
					t.Fatalf("append: %v", err)
				}
				if err := f.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.bvvi")
			idx := NewVectorIndex(2)
			if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 0}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			if err := idx.Save(path); err != nil {
				t.Fatalf("Save: %v", err)
			}
			tc.mutate(t, path)

			if _, err := LoadVectorIndex(path); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestVectorIndex_SearchTopK_OrderAndTieBreak(t *testing.T) {
	idx := NewVectorIndex(2)

	// Both entries have equal similarity to query; tie-break should be IssueID ascending.
	if err := idx.Upsert("B", ComputeContentHash("b"), []float32{1, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	results, err := idx.SearchTopK([]float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("SearchTopK failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Expected 2 results, got %d", len(results))
	}
	if results[0].IssueID != "A" || results[1].IssueID != "B" {
		t.Fatalf("Unexpected order: %#v", results)
	}
}

func TestVectorIndex_SearchTopKWithExactIDIncludesLowScoringOpaqueID(t *testing.T) {
	idx := NewVectorIndex(2)
	for _, entry := range []struct {
		id     string
		vector []float32
	}{
		{id: "A", vector: []float32{1, 0}},
		{id: "B", vector: []float32{0.5, 0}},
		{id: "bv-9gf.3", vector: []float32{0, 1}},
	} {
		if err := idx.Upsert(entry.id, ComputeContentHash(entry.id), entry.vector); err != nil {
			t.Fatalf("Upsert(%q) failed: %v", entry.id, err)
		}
	}

	results, err := idx.SearchTopKWithExactID([]float32{1, 0}, 2, " BV-9GF.3 ")
	if err != nil {
		t.Fatalf("SearchTopKWithExactID failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2: %#v", len(results), results)
	}
	if results[0].IssueID != "bv-9gf.3" {
		t.Fatalf("exact opaque ID was not first: %#v", results)
	}
	if results[0].Score != 0 {
		t.Fatalf("exact result score = %v, want its real vector score 0", results[0].Score)
	}
	if results[1].IssueID != "A" {
		t.Fatalf("best semantic result was not retained: %#v", results)
	}
}

func TestVectorIndex_SearchTopKWithExactIDRejectsAmbiguousCaseFold(t *testing.T) {
	idx := NewVectorIndex(2)
	for _, entry := range []struct {
		id     string
		vector []float32
	}{
		{id: "other", vector: []float32{1, 0}},
		{id: "ABC", vector: []float32{0, 1}},
		{id: "abc", vector: []float32{0, 1}},
	} {
		if err := idx.Upsert(entry.id, ComputeContentHash(entry.id), entry.vector); err != nil {
			t.Fatalf("Upsert(%q) failed: %v", entry.id, err)
		}
	}

	ambiguous, err := idx.SearchTopKWithExactID([]float32{1, 0}, 1, "AbC")
	if err != nil {
		t.Fatalf("ambiguous SearchTopKWithExactID failed: %v", err)
	}
	if len(ambiguous) != 1 || ambiguous[0].IssueID != "other" {
		t.Fatalf("ambiguous folded ID displaced semantic winner: %#v", ambiguous)
	}

	exactCase, err := idx.SearchTopKWithExactID([]float32{1, 0}, 1, "abc")
	if err != nil {
		t.Fatalf("exact-case SearchTopKWithExactID failed: %v", err)
	}
	if len(exactCase) != 1 || exactCase[0].IssueID != "abc" {
		t.Fatalf("exact-case ID was not promoted: %#v", exactCase)
	}
}

func TestVectorIndex_SearchTopKBoundsOversizedLimitToIndex(t *testing.T) {
	idx := NewVectorIndex(2)
	if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	maxInt := int(^uint(0) >> 1)
	results, err := idx.SearchTopK([]float32{1, 0}, maxInt)
	if err != nil {
		t.Fatalf("SearchTopK failed: %v", err)
	}
	if len(results) != 1 || results[0].IssueID != "A" {
		t.Fatalf("oversized-limit results = %#v, want only indexed issue A", results)
	}
}

func TestVectorIndex_GetReturnsCopy(t *testing.T) {
	idx := NewVectorIndex(2)
	if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 0}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	entry, ok := idx.Get("A")
	if !ok {
		t.Fatalf("Expected entry A")
	}
	entry.Vector[0] = 99

	entryAgain, ok := idx.Get("A")
	if !ok {
		t.Fatalf("Expected entry A")
	}
	if entryAgain.Vector[0] != 1 {
		t.Fatalf("Get exposed internal vector storage: got %f", entryAgain.Vector[0])
	}
}

func TestVectorIndex_Errors(t *testing.T) {
	idx := NewVectorIndex(3)

	if err := idx.Upsert("A", ComputeContentHash("a"), []float32{1, 2}); err == nil {
		t.Fatalf("Expected dim mismatch error on Upsert")
	}
	if _, err := idx.SearchTopK([]float32{1, 2}, 1); err == nil {
		t.Fatalf("Expected dim mismatch error on SearchTopK")
	}
	if err := idx.Upsert("nan", ComputeContentHash("nan"), []float32{1, float32(math.NaN()), 0}); err == nil {
		t.Fatalf("Expected non-finite vector error on Upsert")
	}
	if _, err := idx.SearchTopK([]float32{1, float32(math.Inf(1)), 0}, 1); err == nil {
		t.Fatalf("Expected non-finite query error on SearchTopK")
	}
}

func TestContentHash_HexRoundTrip(t *testing.T) {
	h := ComputeContentHash("hello world")
	hexStr := h.Hex()
	if len(hexStr) != 64 {
		t.Fatalf("Expected 64-char hex, got %d", len(hexStr))
	}
	parsed, err := ParseContentHashHex(hexStr)
	if err != nil {
		t.Fatalf("ParseContentHashHex failed: %v", err)
	}
	if parsed != h {
		t.Fatalf("Hash round-trip mismatch")
	}
}
