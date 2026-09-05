package search

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// Encode the published version-1 layout independently, using the original
// scalar binary writer. Comparing bytes catches a matching reader/writer bug
// that a round trip alone would miss.
func scalarVectorIndexBytes(t *testing.T, dim int, ids []string, vectors [][]float32) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("BVVI")
	write := func(value any) {
		t.Helper()
		if err := binary.Write(&out, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	write(uint16(1))
	write(uint16(0))
	write(uint32(dim))
	write(uint32(len(ids)))
	for i, id := range ids {
		write(uint16(len(id)))
		out.WriteString(id)
		hash := ComputeContentHash(id)
		out.Write(hash[:])
		for _, value := range vectors[i] {
			write(math.Float32bits(value))
		}
	}
	return out.Bytes()
}

func TestVectorIndexBinaryFormatExact(t *testing.T) {
	for _, dim := range []int{1, 17, 2048} {
		t.Run(fmt.Sprint(dim), func(t *testing.T) {
			ids := []string{"A", "z-last", "é/third"}
			patterns := []uint32{0, 0x80000000, 1, 0x80000001, 0x007fffff, 0x00800000, 0x3f800000, 0xbf800000, 0x7f7fffff, 0xff7fffff, 0x3eaaaaab}
			vectors := make([][]float32, len(ids))
			idx := NewVectorIndex(dim)
			for i := len(ids) - 1; i >= 0; i-- {
				vectors[i] = make([]float32, dim)
				for j := range vectors[i] {
					vectors[i][j] = math.Float32frombits(patterns[(i+j)%len(patterns)])
				}
				if err := idx.Upsert(ids[i], ComputeContentHash(ids[i]), vectors[i]); err != nil {
					t.Fatal(err)
				}
			}
			want := scalarVectorIndexBytes(t, dim, ids, vectors)
			dir := t.TempDir()
			path := filepath.Join(dir, "saved.bvvi")
			if err := idx.Save(path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("version-1 bytes changed: dim=%d got=%d bytes want=%d err=%v", dim, len(got), len(want), err)
			}
			// Load the independent scalar bytes, not output from Save.
			scalarPath := filepath.Join(dir, "scalar.bvvi")
			if err := os.WriteFile(scalarPath, want, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadVectorIndex(scalarPath)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Dim != dim || loaded.Size() != len(ids) {
				t.Fatalf("loaded dim=%d size=%d, want dim=%d size=%d", loaded.Dim, loaded.Size(), dim, len(ids))
			}
			for i, id := range ids {
				entry, ok := loaded.Get(id)
				if !ok || entry.ContentHash != ComputeContentHash(id) || len(entry.Vector) != dim {
					t.Fatalf("loaded entry %q is missing or changed: %+v", id, entry)
				}
				for j, value := range vectors[i] {
					if math.Float32bits(entry.Vector[j]) != math.Float32bits(value) {
						t.Fatalf("%s[%d] bits=%08x want=%08x", id, j, math.Float32bits(entry.Vector[j]), math.Float32bits(value))
					}
				}
			}
		})
	}
	// An empty index still writes exactly its header, including at the largest
	// accepted dimension; no vector body is needed.
	for _, dim := range []int{1, int(maxVectorIndexDimension)} {
		idx := NewVectorIndex(dim)
		path := filepath.Join(t.TempDir(), "empty.bvvi")
		if err := idx.Save(path); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, scalarVectorIndexBytes(t, dim, nil, nil)) {
			t.Fatalf("empty index format changed at dim=%d: %v", dim, err)
		}
		loaded, err := LoadVectorIndex(path)
		if err != nil || loaded.Dim != dim || loaded.Size() != 0 {
			t.Fatalf("empty index load changed at dim=%d: %+v %v", dim, loaded, err)
		}
	}
}

func TestVectorIndexBinaryBodyRejectsTruncationAndNonFinite(t *testing.T) {
	// A long ID leaves enough bytes to pass the minimum-body-size check even
	// when the vector is truncated. This exercises actual vector read errors.
	const dim = 17
	id := strings.Repeat("long-id-", 8)
	data := scalarVectorIndexBytes(t, dim, []string{id}, [][]float32{make([]float32, dim)})
	path := filepath.Join(t.TempDir(), "body.bvvi")
	for length := 0; length < len(data); length++ {
		if err := os.WriteFile(path, data[:length], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadVectorIndex(path); err == nil {
			t.Fatalf("accepted truncated version-1 file: bytes=%d of %d", length, len(data))
		} else if length >= len(data)-4 && !strings.Contains(err.Error(), "read vector") {
			t.Fatalf("last-component truncation missed vector read: bytes=%d err=%v", length, err)
		}
	}
	for _, bits := range []uint32{0x7f800000, 0xff800000, 0x7fc00001} {
		malformed := bytes.Clone(data)
		binary.LittleEndian.PutUint32(malformed[len(malformed)-4:], bits)
		if err := os.WriteFile(path, malformed, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadVectorIndex(path); err == nil || err.Error() != fmt.Sprintf("vector component %d must be finite", dim-1) {
			t.Fatalf("accepted non-finite last component %08x or wrong error: %v", bits, err)
		}
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

func TestVectorIndexScopedTopK(t *testing.T) {
	idx := NewVectorIndex(2)
	for id, score := range map[string]float32{"excluded": 1, "selected-a": 0.75, "selected-b": 0.5, "opaque/id.3": 0, "ABC": 0.25, "abc": 0.25} {
		if err := idx.Upsert(id, ComputeContentHash(id), []float32{score, 0}); err != nil {
			t.Fatal(err)
		}
	}
	threshold := 0.5
	cases := []struct {
		name  string
		k     int
		opts  VectorSearchOptions
		want  []string
		exact bool
	}{
		{"unscoped", 2, VectorSearchOptions{}, []string{"excluded", "selected-a"}, false},
		{"empty", 2, VectorSearchOptions{Eligible: map[string]bool{}}, nil, false},
		{"leaders cannot consume slots", 2, VectorSearchOptions{Eligible: map[string]bool{"selected-a": true, "selected-b": true}}, []string{"selected-a", "selected-b"}, false},
		{"excluded exact ID", 1, VectorSearchOptions{Eligible: map[string]bool{"selected-b": true}, ExactID: "excluded"}, []string{"selected-b"}, false},
		{"opaque exact ID", 1, VectorSearchOptions{Eligible: map[string]bool{"selected-a": true, "opaque/id.3": true}, ExactID: "opaque/id.3"}, []string{"opaque/id.3"}, true},
		{"inclusive score boundary", 2, VectorSearchOptions{Eligible: map[string]bool{"selected-a": true, "selected-b": true, "opaque/id.3": true}, MinScore: &threshold}, []string{"selected-a", "selected-b"}, false},
		{"exact ID cannot bypass threshold", 1, VectorSearchOptions{ExactID: "opaque/id.3", MinScore: &threshold}, []string{"excluded"}, false},
		{"ambiguous case fold", 1, VectorSearchOptions{ExactID: "AbC"}, []string{"excluded"}, false},
		{"scope disambiguates case fold", 1, VectorSearchOptions{Eligible: map[string]bool{"excluded": true, "abc": true}, ExactID: "AbC"}, []string{"abc"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := idx.SearchTopKWithOptions([]float32{1, 0}, tc.k, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("results=%v want IDs %v", got, tc.want)
			}
			for i := range got {
				if got[i].IssueID != tc.want[i] || got[i].ExactIDMatch != (i == 0 && tc.exact) {
					t.Errorf("result[%d]=%+v want %s exact=%v", i, got[i], tc.want[i], i == 0 && tc.exact)
				}
			}
			if idx.Size() != 6 {
				t.Fatal("scope mutated the unscoped index")
			}
		})
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := idx.SearchTopKWithOptions([]float32{1, 0}, 2, VectorSearchOptions{MinScore: &invalid}); err == nil {
			t.Errorf("accepted invalid threshold %v", invalid)
		}
	}
}

func TestVectorIndexLexicalEvidenceBeforeCutoff(t *testing.T) {
	idx := NewVectorIndex(2)
	for id, vector := range map[string][]float32{
		"a-nearest": {0.25, 0}, "b-prefix": {0, 1}, "c-prefix": {0, 1}, "opaque/id": {0, 1},
	} {
		if err := idx.Upsert(id, ComputeContentHash(id), vector); err != nil {
			t.Fatal(err)
		}
	}
	threshold := 0.1
	for _, tc := range []struct {
		name  string
		opts  VectorSearchOptions
		want  string
		score float64
		exact bool
	}{
		{"unaffected without evidence", VectorSearchOptions{}, "a-nearest", 0.25, false},
		{"prefix displaces raw leader", VectorSearchOptions{ScoreBoosts: map[string]float64{"b-prefix": 0.35}}, "b-prefix", 0.35, false},
		{"tie breaks by ID", VectorSearchOptions{ScoreBoosts: map[string]float64{"b-prefix": 0.35, "c-prefix": 0.35}}, "b-prefix", 0.35, false},
		{"excluded boost cannot consume slot", VectorSearchOptions{Eligible: map[string]bool{"a-nearest": true}, ScoreBoosts: map[string]float64{"b-prefix": 0.35}}, "a-nearest", 0.25, false},
		{"raw threshold before evidence", VectorSearchOptions{MinScore: &threshold, ScoreBoosts: map[string]float64{"b-prefix": 0.35}}, "a-nearest", 0.25, false},
		{"exact ID overrides boosted leader", VectorSearchOptions{ExactID: "opaque/id", ScoreBoosts: map[string]float64{"b-prefix": 0.35}}, "opaque/id", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for attempt := 0; attempt < 2; attempt++ {
				got, err := idx.SearchTopKWithOptions([]float32{1, 0}, 1, tc.opts)
				if err != nil || len(got) != 1 || got[0].IssueID != tc.want || got[0].Score != tc.score || got[0].ExactIDMatch != tc.exact {
					t.Fatalf("got=%+v err=%v; want %s score=%g exact=%v", got, err, tc.want, tc.score, tc.exact)
				}
			}
		})
	}
	for _, invalid := range []float64{-0.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := idx.SearchTopKWithOptions([]float32{1, 0}, 1, VectorSearchOptions{ScoreBoosts: map[string]float64{"b-prefix": invalid}}); err == nil {
			t.Errorf("accepted invalid evidence %v", invalid)
		}
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

func TestLoadVectorIndexRejectsUntrustedAllocationHeaders(t *testing.T) {
	tests := []struct {
		name  string
		dim   uint32
		count uint32
	}{
		{name: "oversized dimension", dim: maxVectorIndexDimension + 1},
		{name: "oversized entry count", dim: 1, count: maxVectorIndexEntries + 1},
		{name: "impossible entry count for file size", dim: 4, count: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "index.bvvi")
			file, err := os.Create(path)
			if err != nil {
				t.Fatalf("create vector index: %v", err)
			}
			writeErr := func() error {
				if _, err := file.WriteString(vectorIndexMagic); err != nil {
					return err
				}
				for _, value := range []any{vectorIndexVersion, uint16(0), tc.dim, tc.count} {
					if err := binary.Write(file, binary.LittleEndian, value); err != nil {
						return err
					}
				}
				return nil
			}()
			if closeErr := file.Close(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				t.Fatalf("write vector index header: %v", writeErr)
			}

			if _, err := LoadVectorIndex(path); err == nil {
				t.Fatal("LoadVectorIndex accepted a corrupt allocation header")
			}
		})
	}
}

func TestVectorIndexSaveRejectsUnreloadableDimension(t *testing.T) {
	idx := NewVectorIndex(int(maxVectorIndexDimension) + 1)
	path := filepath.Join(t.TempDir(), "index.bvvi")

	if err := idx.Save(path); err == nil {
		t.Fatal("Save accepted a dimension that LoadVectorIndex rejects")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected save left an index artifact: %v", err)
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
