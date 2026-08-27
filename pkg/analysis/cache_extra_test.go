package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestCacheSetTTLAndHash(t *testing.T) {
	issues := []model.Issue{{ID: "C1", Title: "Cache"}}
	c := NewCache(10 * time.Second)
	completed := statusEntry{State: "computed"}
	stats := &GraphStats{
		NodeCount:   1,
		phase2Ready: true,
		status: MetricStatus{
			PageRank:     completed,
			Betweenness:  completed,
			Eigenvector:  completed,
			HITS:         completed,
			Critical:     completed,
			Cycles:       completed,
			KCore:        completed,
			Articulation: completed,
			Slack:        completed,
		},
	}
	c.Set(issues, stats)
	if c.Hash() == "" {
		t.Fatalf("expected hash after Set")
	}

	// Override TTL and ensure GetByHash respects expiry
	c.SetTTL(-1 * time.Second)
	if got, ok := c.Get(issues); got != nil || ok {
		t.Fatalf("expected cache miss after expired TTL")
	}
}

// TestGraphStatsCacheBlob_SoARoundTrip is a regression guard for the SoA
// (struct-of-arrays / dictionary-encoded) on-disk format: GraphStats →
// graphStatsCacheBlob → JSON (compact columnar) → graphStatsCacheBlob must
// reproduce value-identical metric maps, including the sparse and nil cases
// that distinguish absent vs. present-zero and nil vs. empty maps.
func TestGraphStatsCacheBlob_SoARoundTrip(t *testing.T) {
	cases := map[string]graphStatsCacheBlob{
		"dense": {
			OutDegree:        map[string]int{"A": 0, "B": 1, "C": 2},
			InDegree:         map[string]int{"A": 2, "B": 1, "C": 0},
			TopologicalOrder: []string{"A", "B", "C"},
			Density:          0.5,
			NodeCount:        3,
			EdgeCount:        3,
			PageRank:         map[string]float64{"A": 0.1, "B": 0.0, "C": 0.4},
			Betweenness:      map[string]float64{"A": 0, "B": 0, "C": 0},
			Eigenvector:      map[string]float64{"A": 0.7, "B": 0.2, "C": 0.99},
			Hubs:             map[string]float64{"A": 1, "B": 2, "C": 3},
			Authorities:      map[string]float64{"A": 3, "B": 2, "C": 1},
			CriticalPathScore: map[string]float64{
				"A": 5.5, "B": 0, "C": 2.25,
			},
			CoreNumber:   map[string]int{"A": 2, "B": 1, "C": 2},
			Slack:        map[string]float64{"A": 0, "B": 1.5, "C": 0},
			Articulation: []string{"B"},
			Status: MetricStatus{
				PageRank: statusEntry{State: "computed", Elapsed: 1500*time.Millisecond + 123*time.Nanosecond},
			},
			Config: AnalysisConfig{RunToCompletion: true},
		},
		// Sparse: CoreNumber/Slack cover a subset of the node union; this must
		// stay distinct from present-zero after the round trip.
		"sparse_and_nil": {
			Density:     1.0 / 3.0,
			NodeCount:   3,
			EdgeCount:   2,
			OutDegree:   map[string]int{"X": 0, "Y": 1, "Z": 1},
			InDegree:    map[string]int{"X": 2, "Y": 0, "Z": 0},
			PageRank:    map[string]float64{"X": 0.3, "Y": 0.3, "Z": 0.4},
			CoreNumber:  map[string]int{"X": 1}, // sparse: only X
			Slack:       map[string]float64{"Z": 9.0},
			Betweenness: nil, // nil must round-trip back to nil
			Hubs:        map[string]float64{},
		},
		"self_loops_can_raise_density_above_one": {
			Density:   1.5,
			NodeCount: 2,
			EdgeCount: 3,
			OutDegree: map[string]int{"A": 2, "B": 1},
			InDegree:  map[string]int{"A": 2, "B": 1},
			PageRank:  map[string]float64{"A": 0.5, "B": 0.5},
			Cycles:    [][]string{{"A", "A"}},
		},
		"empty": {
			OutDegree: map[string]int{},
			InDegree:  map[string]int{},
		},
	}

	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(blob)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got graphStatsCacheBlob
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			want := blob
			want.decoded = true
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round-trip mismatch:\n want %#v\n  got %#v", want, got)
			}
		})
	}
}

// TestGraphStatsCacheBlob_SoAStoresNodesOnce verifies the columnar layout: the
// serialized payload stores each node ID exactly once (in "nodes") rather than
// repeating it as a key in every per-node metric map.
func TestGraphStatsCacheBlob_SoAStoresNodesOnce(t *testing.T) {
	blob := graphStatsCacheBlob{
		OutDegree:   map[string]int{"NODE-001": 1, "NODE-002": 2},
		InDegree:    map[string]int{"NODE-001": 0, "NODE-002": 1},
		PageRank:    map[string]float64{"NODE-001": 0.5, "NODE-002": 0.5},
		Betweenness: map[string]float64{"NODE-001": 0.1, "NODE-002": 0.2},
	}
	data, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	for _, id := range []string{"NODE-001", "NODE-002"} {
		n := 0
		for i := 0; i+len(id) <= len(s); i++ {
			if s[i:i+len(id)] == id {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("node %q appears %d times in SoA payload, want exactly 1 (columnar)", id, n)
		}
	}
}

// TestRobotDiskCache_VersionGate confirms an entry written by an older layout
// version, or one whose embedded key does not match the lookup (filename
// collision / foreign file), is treated as a miss and reaped — never served.
func TestRobotDiskCache_VersionGate(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	dir, err := robotAnalysisDiskCacheDir(true)
	if err != nil {
		t.Fatal(err)
	}
	issues := []model.Issue{{ID: "version-gate", Status: model.StatusOpen}}
	config := ConfigForSize(1, 0)
	config.DisableCache = false
	config.ComputePageRank = true
	config.PageRankTimeout = time.Second
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()

	key := analyzer.DataHash() + "|" + ComputeConfigHash(&config)
	path := filepath.Join(dir, robotAnalysisEntryFileName(key))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read writer-produced cache entry: %v", err)
	}
	var valid robotAnalysisDiskCacheEntry
	if err := json.Unmarshal(raw, &valid); err != nil {
		t.Fatalf("decode writer-produced cache entry: %v", err)
	}
	if got, _, hit := getRobotDiskCachedStats(key); !hit || got == nil {
		t.Fatalf("writer-produced control entry did not hit: hit=%v stats=%p", hit, got)
	}

	writeEntry := func(e robotAnalysisDiskCacheEntry) {
		t.Helper()
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Mutate only the outer version, keeping the embedded result valid. A miss
	// therefore proves the outer version gate rather than an earlier decode
	// failure on an incomplete fixture.
	oldVersion := valid
	oldVersion.Version--
	writeEntry(oldVersion)
	if _, _, hit := getRobotDiskCachedStats(key); hit {
		t.Fatal("old-version entry must be a miss")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("old-version entry file should be reaped, stat err = %v", err)
	}

	// Likewise, mutate only the embedded outer key.
	wrongKey := valid
	wrongKey.Key = "other|key"
	writeEntry(wrongKey)
	if _, _, hit := getRobotDiskCachedStats(key); hit {
		t.Fatal("key-mismatched entry must be a miss")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("key-mismatched entry file should be reaped, stat err = %v", err)
	}
}

// TestExpandFloatIntNegativeIndexNoPanic guards a corrupt/hand-edited cache file
// with a NEGATIVE sparse index: it must degrade (drop the bad entry) rather than
// panic on nodes[-1] and crash the whole bv command.
func TestExpandFloatIntNegativeIndexNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on negative index (must degrade to a miss): %v", r)
		}
	}()
	nodes := []string{"a", "b"}
	fm := expandFloat(true, []int32{-1, 1}, []float64{9.0, 2.0}, nodes)
	if len(fm) != 1 || fm["b"] != 2.0 {
		t.Errorf("expandFloat: expected only the valid index kept, got %v", fm)
	}
	im := expandInt(true, []int32{-3}, []int{7}, nodes)
	if len(im) != 0 {
		t.Errorf("expandInt: expected empty (only negative index), got %v", im)
	}
}

func TestRobotDiskCacheRejectsStructurallyCorruptSoA(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	dir, err := robotAnalysisDiskCacheDir(true)
	if err != nil {
		t.Fatal(err)
	}
	const dataHash = "data-hash"
	configHash := ComputeConfigHash(&AnalysisConfig{})
	key := dataHash + "|" + configHash
	path := filepath.Join(dir, robotAnalysisEntryFileName(key))
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name   string
		result string
	}{
		{name: "missing result", result: ""},
		{name: "wrong inner version", result: `{"v":2,"nodes":[],"node_count":0}`},
		{name: "short dense column", result: `{"v":3,"nodes":["a","b"],"node_count":2,"od_set":true,"od_idx":null,"od":[1]}`},
		{name: "mismatched sparse columns", result: `{"v":3,"nodes":["a","b"],"node_count":2,"od_set":true,"od_idx":[0,1],"od":[1]}`},
		{name: "out of range sparse index", result: `{"v":3,"nodes":["a","b"],"node_count":2,"od_set":true,"od_idx":[2],"od":[1]}`},
		{name: "duplicate sparse index", result: `{"v":3,"nodes":["a","b"],"node_count":2,"od_set":true,"od_idx":[0,0],"od":[1,2]}`},
		{name: "unsorted node dictionary", result: `{"v":3,"nodes":["b","a"],"node_count":2}`},
		{name: "missing required phase1 degrees", result: `{"v":3,"nodes":["a"],"node_count":1}`},
		{name: "unknown topological node", result: `{"v":3,"nodes":["a"],"node_count":1,"topological_order":["missing"],"od_set":true,"od_idx":null,"od":[0],"id_set":true,"id_idx":null,"id":[0]}`},
		{name: "negative degree", result: `{"v":3,"nodes":["a"],"node_count":1,"od_set":true,"od_idx":null,"od":[-1],"id_set":true,"id_idx":null,"id":[-1]}`},
		{name: "degree sums do not match edges", result: `{"v":3,"nodes":["a"],"node_count":1,"edge_count":1,"od_set":true,"od_idx":null,"od":[1],"id_set":true,"id_idx":null,"id":[0]}`},
		{name: "density does not match graph size", result: `{"v":3,"nodes":["a","b"],"node_count":2,"edge_count":1,"density":0.25,"od_set":true,"od_idx":null,"od":[1,0],"id_set":true,"id_idx":null,"id":[0,1]}`},
		{name: "nonclosing cycle", result: `{"v":3,"nodes":["a","b"],"node_count":2,"edge_count":2,"density":1,"cycles":[["a","b"]],"od_set":true,"od_idx":null,"od":[1,1],"id_set":true,"id_idx":null,"id":[1,1]}`},
		{name: "cycle repeats interior node", result: `{"v":3,"nodes":["a","b"],"node_count":2,"edge_count":2,"density":1,"cycles":[["a","b","a","b","a"]],"od_set":true,"od_idx":null,"od":[1,1],"id_set":true,"id_idx":null,"id":[1,1]}`},
		{name: "minimal graph lacks completed metrics", result: `{"v":3,"nodes":["a"],"node_count":1,"od_set":true,"od_idx":null,"od":[0],"id_set":true,"id_idx":null,"id":[0]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultField := ""
			if tt.result != "" {
				resultField = `,"result":` + tt.result
			}
			raw := fmt.Sprintf(`{"version":%d,"key":%q,"created_at":%q,"data_hash":%q,"config_hash":%q%s}`,
				robotAnalysisDiskCacheVersion, key, createdAt, dataHash, configHash, resultField)
			if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}

			if stats, _, hit := getRobotDiskCachedStats(key); hit || stats != nil {
				t.Fatalf("corrupt cache returned hit=%v stats=%p", hit, stats)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("corrupt cache entry was not reaped: %v", err)
			}
		})
	}
}

func TestRobotDiskCacheRejectsIsolatedCompletedResultCorruption(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	issues := []model.Issue{{ID: "valid-base", Status: model.StatusOpen}}
	config := ConfigForSize(1, 0)
	config.DisableCache = false
	config.ComputeBetweenness = false
	config.ComputePageRank = true
	config.PageRankTimeout = time.Second
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()

	key := analyzer.DataHash() + "|" + ComputeConfigHash(&config)
	dir, err := robotAnalysisDiskCacheDir(false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, robotAnalysisEntryFileName(key))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read writer-produced cache entry: %v", err)
	}
	var valid robotAnalysisDiskCacheEntry
	if err := json.Unmarshal(raw, &valid); err != nil {
		t.Fatalf("decode writer-produced cache entry: %v", err)
	}
	if got, _, hit := getRobotDiskCachedStats(key); !hit || got == nil {
		t.Fatalf("writer-produced control entry did not hit: hit=%v stats=%p", hit, got)
	}

	tests := []struct {
		name   string
		mutate func(*robotAnalysisDiskCacheEntry)
	}{
		{
			name: "missing producer-required metric map",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.PageRank = nil
			},
		},
		{
			name: "enabled page rank map is nonnil but empty",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.PageRank = map[string]float64{}
			},
		},
		{
			name: "enabled page rank contains a negative score",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.PageRank["valid-base"] = -1
			},
		},
		{
			name: "enabled page rank is not normalized",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.PageRank["valid-base"] = 1e300
			},
		},
		{
			name: "enabled page rank status is skipped",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.PageRank.State = "skipped"
			},
		},
		{
			name: "enabled page rank status is unproduced approximation",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.PageRank.State = "approx"
			},
		},
		{
			name: "transient page rank timeout is not reusable",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.PageRank.State = "timeout"
			},
		},
		{
			name: "disabled betweenness status is computed",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.Betweenness.State = "computed"
			},
		},
		{
			name: "disabled betweenness has values",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Betweenness = map[string]float64{"valid-base": 1}
			},
		},
		{
			name: "tiny negative density within numeric tolerance",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Density = -5e-13
			},
		},
		{
			name: "missing completed metric statuses",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status = MetricStatus{}
			},
		},
		{
			name: "pending status in completed result",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.PageRank.State = "pending"
			},
		},
		{
			name: "negative elapsed metric time",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Status.PageRank.Elapsed = -time.Millisecond
			},
		},
		{
			name: "result config not bound to outer config hash",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.Result.Config.ComputePageRank = !entry.Result.Config.ComputePageRank
			},
		},
		{
			name: "future creation time",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.CreatedAt = time.Now().Add(time.Hour).UTC()
			},
		},
		{
			name: "negative compute duration",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.ComputeDuration = -time.Nanosecond
			},
		},
		{
			name: "implausibly large compute duration",
			mutate: func(entry *robotAnalysisDiskCacheEntry) {
				entry.ComputeDuration = robotAnalysisDiskCacheMaxAge + time.Nanosecond
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Decode the writer-produced bytes afresh so mutations to nested maps
			// in one case cannot contaminate the supposedly isolated cases after it.
			var entry robotAnalysisDiskCacheEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				t.Fatalf("decode clean writer entry: %v", err)
			}
			tt.mutate(&entry)
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}

			if got, _, hit := getRobotDiskCachedStats(key); hit || got != nil {
				t.Fatalf("corrupt cache returned hit=%v stats=%p", hit, got)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("corrupt cache entry was not reaped: %v", err)
			}
		})
	}
}

func TestCompletedCacheValidationRejectsNegativeBetweenness(t *testing.T) {
	blob := graphStatsCacheBlob{
		decoded:           true,
		NodeCount:         1,
		PageRank:          map[string]float64{"node": 1},
		Betweenness:       map[string]float64{"node": -1},
		Eigenvector:       map[string]float64{},
		Hubs:              map[string]float64{},
		Authorities:       map[string]float64{},
		CriticalPathScore: map[string]float64{},
		Config: AnalysisConfig{
			ComputePageRank:    true,
			ComputeBetweenness: true,
		},
		Status: MetricStatus{
			PageRank:     statusEntry{State: "computed"},
			Betweenness:  statusEntry{State: "computed"},
			Eigenvector:  statusEntry{State: "skipped"},
			HITS:         statusEntry{State: "skipped"},
			Critical:     statusEntry{State: "skipped"},
			Cycles:       statusEntry{State: "skipped"},
			KCore:        statusEntry{State: "skipped"},
			Articulation: statusEntry{State: "skipped"},
			Slack:        statusEntry{State: "skipped"},
		},
	}

	if err := blob.validateForCacheHit(); err == nil || !strings.Contains(err.Error(), "betweenness") {
		t.Fatalf("validateForCacheHit() error=%v, want negative betweenness rejection", err)
	}
}

func TestRobotDiskCacheRejectsOversizedEntryBeforeDecode(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	cacheDir := t.TempDir()
	t.Setenv("BV_CACHE_DIR", cacheDir)
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DB", beadsDir)

	dir, err := robotAnalysisDiskCacheDir(true)
	if err != nil {
		t.Fatal(err)
	}
	const key = "oversized|entry"
	path := filepath.Join(dir, robotAnalysisEntryFileName(key))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(robotAnalysisDiskCacheMaxEntrySize + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if stats, _, hit := getRobotDiskCachedStats(key); hit || stats != nil {
		t.Fatalf("oversized cache returned hit=%v stats=%p", hit, stats)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized cache entry was not reaped: %v", err)
	}
}

func TestRobotDiskCacheRunToCompletionSuppressesXFetch(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())
	t.Setenv("BEADS_DB", "")
	t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "missing-beads-dir"))
	t.Setenv(EnvSourceDateEpoch, "1234567890")
	t.Setenv(EnvSkipPhase2, "")
	t.Setenv(EnvPhase2TimeoutSeconds, "")

	issues := []model.Issue{{ID: "pinned-cache", Status: model.StatusOpen}}
	config := ConfigForSize(1, 0)
	if !config.RunToCompletion {
		t.Fatal("valid SOURCE_DATE_EPOCH did not enable RunToCompletion")
	}
	analyzer := NewAnalyzer(issues)
	stats := analyzer.AnalyzeAsyncWithConfig(context.Background(), config)
	stats.WaitForPhase2()

	key := analyzer.DataHash() + "|" + ComputeConfigHash(&config)
	dir, err := robotAnalysisDiskCacheDir(false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, robotAnalysisEntryFileName(key))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read writer-produced cache entry: %v", err)
	}
	var entry robotAnalysisDiskCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode writer-produced cache entry: %v", err)
	}
	if !entry.Result.Config.RunToCompletion {
		t.Fatal("disk-cache codec lost RunToCompletion")
	}

	entry.CreatedAt = time.Now().Add(-time.Hour).UTC()
	entry.ComputeDuration = time.Nanosecond
	raw, err = json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	got, refresh, hit := getRobotDiskCachedStats(key)
	if !hit || got == nil {
		t.Fatalf("run-to-completion cache entry did not hit: hit=%v stats=%p", hit, got)
	}
	if refresh {
		t.Fatal("run-to-completion cache hit requested probabilistic XFetch refresh")
	}
}

func TestRemoveRobotDiskCacheEntryIfSamePreservesConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry.json")
	if err := os.WriteFile(path, []byte("old corrupt entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_ = unlockFile(lock)
		}
	}()

	cleanupDone := make(chan struct{})
	go func() {
		removeRobotDiskCacheEntryIfSame(path, oldInfo)
		close(cleanupDone)
	}()
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("best-effort cache cleanup blocked behind an active writer")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old corrupt entry" {
		t.Fatalf("cleanup modified writer-owned entry: content=%q err=%v", got, err)
	}

	replacement := filepath.Join(dir, "replacement.json")
	if err := os.WriteFile(replacement, []byte("new valid entry"), 0o644); err != nil {
		t.Fatal(err)
	}
	displaced := filepath.Join(dir, "displaced-old-entry.json")
	if err := os.Rename(path, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if err := unlockFile(lock); err != nil {
		t.Fatal(err)
	}
	locked = false

	removeRobotDiskCacheEntryIfSame(path, oldInfo)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("concurrent replacement was removed: %v", err)
	}
	if string(got) != "new valid entry" {
		t.Fatalf("replacement content=%q, want preserved valid entry", got)
	}
}
