package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readChunkConfig reads and parses the exported chunk config.
func readChunkConfig(t *testing.T, outputDir string) ChunkConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outputDir, "beads.sqlite3.config.json"))
	if err != nil {
		t.Fatalf("read chunk config: %v", err)
	}
	var c ChunkConfig
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parse chunk config: %v", err)
	}
	return c
}

// countChunkFiles counts NNNNN.bin files actually present on disk.
func countChunkFiles(t *testing.T, chunksDir string) int {
	t.Helper()
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read chunks dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bin") {
			n++
		}
	}
	return n
}

// reassembleChunks concatenates the chunks named by the config, exactly as the
// browser viewer does, and returns the reconstructed bytes.
func reassembleChunks(t *testing.T, outputDir string, c ChunkConfig) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, ch := range c.Chunks {
		data, err := os.ReadFile(filepath.Join(outputDir, ch.Path))
		if err != nil {
			t.Fatalf("read chunk %s: %v", ch.Path, err)
		}
		buf.Write(data)
	}
	return buf.Bytes()
}

// TestChunkIfNeeded_PrunesOrphanedChunksOnShrink is the regression test for
// beads_viewer#175: re-exporting a SMALLER database must not leave stale
// higher-numbered chunk files behind, and the chunks named by the new config
// must reassemble to exactly the current DB (never a malformed mix of chunk
// generations).
func TestChunkIfNeeded_PrunesOrphanedChunksOnShrink(t *testing.T) {
	tmp := t.TempDir()
	outputDir := filepath.Join(tmp, "pages")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chunksDir := filepath.Join(outputDir, "chunks")
	dbPath := filepath.Join(outputDir, "beads.sqlite3")

	// Tiny threshold/chunk-size so byte fixtures chunk deterministically.
	exp := &SQLiteExporter{
		Config: SQLiteExportConfig{ChunkThreshold: 4, ChunkSize: 4},
	}

	// Export #1: a "large" DB -> 10 chunks of 4 bytes.
	big := bytes.Repeat([]byte("ABCD"), 10) // 40 bytes
	if err := os.WriteFile(dbPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exp.chunkIfNeeded(openTestBundle(t, outputDir), filepath.Base(dbPath)); err != nil {
		t.Fatalf("export#1: %v", err)
	}
	c1 := readChunkConfig(t, outputDir)
	if !c1.Chunked || c1.ChunkCount != 10 {
		t.Fatalf("export#1 want chunked/10, got chunked=%v count=%d", c1.Chunked, c1.ChunkCount)
	}
	if got := countChunkFiles(t, chunksDir); got != 10 {
		t.Fatalf("export#1 want 10 chunk files, got %d", got)
	}

	// Export #2: DB shrinks across a chunk boundary -> 3 chunks. The prior
	// 00003..00009 must be removed; reassembly must equal the new DB.
	small := bytes.Repeat([]byte("WXYZ"), 3) // 12 bytes
	if err := os.WriteFile(dbPath, small, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exp.chunkIfNeeded(openTestBundle(t, outputDir), filepath.Base(dbPath)); err != nil {
		t.Fatalf("export#2: %v", err)
	}
	c2 := readChunkConfig(t, outputDir)
	if !c2.Chunked || c2.ChunkCount != 3 {
		t.Fatalf("export#2 want chunked/3, got chunked=%v count=%d", c2.Chunked, c2.ChunkCount)
	}
	if got := countChunkFiles(t, chunksDir); got != 3 {
		t.Fatalf("export#2 want exactly 3 chunk files (no orphans), got %d", got)
	}
	if got := reassembleChunks(t, outputDir, c2); !bytes.Equal(got, small) {
		t.Fatalf("export#2 reassembly mismatch: got %q want %q", got, small)
	}
	for i := 3; i < 10; i++ {
		p := filepath.Join(chunksDir, fmt.Sprintf("%05d.bin", i))
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("orphaned chunk %s still present after shrink", p)
		}
	}

	// Export #3: DB shrinks below the threshold -> unchunked. The whole chunks/
	// directory (all exporter-owned chunk files) must be gone, so a stale
	// chunked config can never reassemble leftover bytes.
	tiny := []byte("hi") // 2 bytes < threshold(4)
	if err := os.WriteFile(dbPath, tiny, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exp.chunkIfNeeded(openTestBundle(t, outputDir), filepath.Base(dbPath)); err != nil {
		t.Fatalf("export#3: %v", err)
	}
	c3 := readChunkConfig(t, outputDir)
	if c3.Chunked {
		t.Fatalf("export#3 want unchunked, got chunked=true")
	}
	if got := countChunkFiles(t, chunksDir); got != 0 {
		t.Fatalf("export#3 want 0 chunk files, got %d", got)
	}
	if _, err := os.Stat(chunksDir); !os.IsNotExist(err) {
		t.Fatalf("export#3 want chunks/ dir removed; it still exists")
	}
}

// TestChunkArtifactCleanup_PreservesNonChunkFiles proves the cleanup only
// removes files the exporter owns (NNNNN.bin) and never recursively deletes the
// chunks/ directory: an unrelated user file there is preserved, and so is the
// directory (AGENTS.md: never blow away user data).
func TestChunkArtifactCleanup_PreservesNonChunkFiles(t *testing.T) {
	tmp := t.TempDir()
	outputDir := filepath.Join(tmp, "pages")
	chunksDir := filepath.Join(outputDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale exporter-owned chunk plus an unrelated user file.
	if err := os.WriteFile(filepath.Join(chunksDir, "00000.bin"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(chunksDir, "NOTES.txt")
	if err := os.WriteFile(userFile, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(outputDir, "beads.sqlite3")
	if err := os.WriteFile(dbPath, []byte("hi"), 0o644); err != nil { // below threshold -> unchunked
		t.Fatal(err)
	}
	exp := &SQLiteExporter{Config: SQLiteExportConfig{ChunkThreshold: 4, ChunkSize: 4}}
	if err := exp.chunkIfNeeded(openTestBundle(t, outputDir), filepath.Base(dbPath)); err != nil {
		t.Fatalf("export: %v", err)
	}

	// The exporter's own stale chunk is removed...
	if _, err := os.Stat(filepath.Join(chunksDir, "00000.bin")); !os.IsNotExist(err) {
		t.Fatalf("stale exporter chunk should have been removed")
	}
	// ...but the unrelated user file (and hence the directory) is preserved.
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("unrelated user file must be preserved, got: %v", err)
	}
}

// TestChunkFileIndex checks the strict NNNNN.bin matcher that gates all chunk
// deletion, so cleanup can never match a non-chunk file name.
func TestChunkFileIndex(t *testing.T) {
	cases := []struct {
		name    string
		wantIdx int
		wantOK  bool
	}{
		{"00000.bin", 0, true},
		{"00014.bin", 14, true},
		{"99999.bin", 99999, true},
		{"0014.bin", 0, false},      // too few digits
		{"000014.bin", 0, false},    // too many digits
		{"0001a.bin", 0, false},     // non-digit
		{"chunk0.bin", 0, false},    // not all digits
		{"00000.txt", 0, false},     // wrong extension
		{"00000.bin.bak", 0, false}, // suffix beyond .bin
		{"README.md", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		idx, ok := chunkFileIndex(tc.name)
		if ok != tc.wantOK || (ok && idx != tc.wantIdx) {
			t.Errorf("chunkFileIndex(%q) = (%d,%v), want (%d,%v)", tc.name, idx, ok, tc.wantIdx, tc.wantOK)
		}
	}
}
