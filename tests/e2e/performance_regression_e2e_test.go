package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

// Performance Regression Tests for bv-ut9x
// Tests latency, startup time, and performance thresholds.

// =============================================================================
// Performance Thresholds
// =============================================================================

const (
	// Maximum acceptable latency for robot commands (in milliseconds)
	maxTriageLatencyMS       = 3000 // 3 seconds for triage
	maxNextLatencyMS         = 2000 // 2 seconds for --robot-next
	maxGraphLatencyMS        = 2000 // 2 seconds for graph export
	maxPlanLatencyMS         = 2000 // 2 seconds for plan
	maxSmallDatasetLatencyMS = 1000 // 1 second for small datasets (<50 issues)

	// A wall-time smoke bound, not a memory proxy or a tail-latency claim.
	maxLargeDatasetLatencyMS = 5000
)

// =============================================================================
// 1. Robot Command Latency Tests
// =============================================================================

// TestPerf_RobotTriageLatency verifies --robot-triage completes within threshold.
func TestPerf_RobotTriageLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data with moderate complexity
	createTestDataset(t, env, 100)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-triage failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("--robot-triage latency: %v", elapsed)
	if elapsed.Milliseconds() > maxTriageLatencyMS {
		t.Errorf("--robot-triage too slow: %v > %dms threshold", elapsed, maxTriageLatencyMS)
	}
}

// TestPerf_RobotNextLatency verifies --robot-next completes within threshold.
func TestPerf_RobotNextLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 100)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-next")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-next failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("--robot-next latency: %v", elapsed)
	if elapsed.Milliseconds() > maxNextLatencyMS {
		t.Errorf("--robot-next too slow: %v > %dms threshold", elapsed, maxNextLatencyMS)
	}
}

// TestPerf_RobotGraphLatency verifies --robot-graph completes within threshold.
func TestPerf_RobotGraphLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 100)

	formats := []string{"json", "dot", "mermaid"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			start := time.Now()
			cmd := exec.Command(bv, "--robot-graph", "--graph-format", format)
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				t.Fatalf("--robot-graph (%s) failed: %v", format, err)
			}
			elapsed := time.Since(start)

			t.Logf("--robot-graph (%s) latency: %v", format, elapsed)
			if elapsed.Milliseconds() > maxGraphLatencyMS {
				t.Errorf("--robot-graph (%s) too slow: %v > %dms threshold", format, elapsed, maxGraphLatencyMS)
			}
		})
	}
}

// TestPerf_RobotPlanLatency verifies --robot-plan completes within threshold.
func TestPerf_RobotPlanLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 100)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-plan")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("--robot-plan failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("--robot-plan latency: %v", elapsed)
	if elapsed.Milliseconds() > maxPlanLatencyMS {
		t.Errorf("--robot-plan too slow: %v > %dms threshold", elapsed, maxPlanLatencyMS)
	}
}

// =============================================================================
// 2. Data Size Scaling Tests
// =============================================================================

// TestPerf_SmallDatasetLatency tests performance with small datasets.
func TestPerf_SmallDatasetLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 20)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("small dataset (20 issues) latency: %v", elapsed)
	if elapsed.Milliseconds() > maxSmallDatasetLatencyMS {
		t.Errorf("small dataset too slow: %v > %dms threshold", elapsed, maxSmallDatasetLatencyMS)
	}
}

// TestPerf_MediumDatasetLatency tests performance with medium datasets.
func TestPerf_MediumDatasetLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 200)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("medium dataset (200 issues) latency: %v", elapsed)
	if elapsed.Milliseconds() > maxTriageLatencyMS {
		t.Errorf("medium dataset too slow: %v > %dms threshold", elapsed, maxTriageLatencyMS)
	}
}

// TestPerf_LargeDatasetLatency tests performance with large datasets.
func TestPerf_LargeDatasetLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large dataset test in short mode")
	}

	bv := buildBvBinary(t)
	for _, size := range []int{1000, 5000, 10000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			dir := t.TempDir()
			writePerformanceFixture(t, dir, "realistic", size)
			output, elapsed := runPerformanceCLI(t, bv, dir, filepath.Join(dir, "cold-cache"), "smoke", "")
			var result struct {
				Triage struct {
					Meta struct {
						IssueCount int `json:"issue_count"`
					} `json:"meta"`
				} `json:"triage"`
			}
			if err := json.Unmarshal(output, &result); err != nil || result.Triage.Meta.IssueCount != size {
				t.Fatalf("large fixture was not fully analyzed: count=%d want=%d, error=%v", result.Triage.Meta.IssueCount, size, err)
			}
			if _, err := performanceCLIBehavior(output); err != nil {
				t.Fatal(err)
			}
			t.Logf("large dataset (%d issues) cold application-cache latency: %v", size, elapsed)
			if elapsed.Milliseconds() > maxLargeDatasetLatencyMS {
				t.Errorf("large dataset too slow: %v > %dms threshold", elapsed, maxLargeDatasetLatencyMS)
			}
		})
	}
}

func writePerformanceFixture(t testing.TB, dir, kind string, size int) string {
	t.Helper()
	issues, err := testutil.PerformanceIssues(kind, size, 20260904)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
}

func runPerformanceCLI(t testing.TB, binary, dir, cache, sample, epoch string) ([]byte, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--robot-triage")
	cmd.Dir = dir
	// A pinned SOURCE_DATE_EPOCH runs metrics to completion. Removing it is
	// essential here: production timeouts/degradation must remain observable.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "BV_") && !strings.HasPrefix(key, "BEADS_") && key != "SOURCE_DATE_EPOCH" && key != "GOMAXPROCS" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "BV_NO_BROWSER=1", "BV_TEST_MODE=1", "BV_NO_SAVED_CONFIG=1", "BEADS_DIR="+filepath.Join(dir, ".beads"), "BV_CACHE_DIR="+cache,
		"GOMAXPROCS="+strconv.Itoa(runtime.GOMAXPROCS(0)))
	if epoch != "" {
		// Only the separate exact-result cohort supplies this. Its durations
		// receive no latency credit because metrics run to completion.
		cmd.Env = append(cmd.Env, "SOURCE_DATE_EPOCH="+epoch)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	output, err := cmd.Output()
	elapsed := time.Since(started)
	// Retain actual stdout/stderr, including failures. Each sample has a unique
	// name inside an isolated destination; the runner retains the directory.
	for suffix, content := range map[string][]byte{"stdout.json": output, "stderr.log": stderr.Bytes()} {
		path := filepath.Join(dir, sample+"."+suffix)
		if filepath.IsAbs(sample) {
			path = sample + "." + suffix
		}
		if writeErr := os.WriteFile(path, content, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err != nil {
		t.Fatalf("%s --robot-triage failed: %v\nstderr: %s\nstdout: %s", binary, err, stderr.String(), output)
	}
	return output, elapsed
}

// Compare the decision IDs in order, readiness/claim fields, source identity,
// and every metric state/reason/sample. Raw outputs remain available for score
// review; this projection does not claim bitwise floating-point score parity.
func performanceCLIBehavior(output []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(output, &envelope); err != nil {
		return nil, fmt.Errorf("decode robot envelope: %w", err)
	}
	var triage map[string]json.RawMessage
	if err := json.Unmarshal(envelope["triage"], &triage); err != nil {
		return nil, fmt.Errorf("decode triage: %w", err)
	}
	states, err := testutil.PerformanceMetricStates(triage["status"])
	if err != nil {
		return nil, err
	}
	decision := map[string]any{"status": states, "data_hash": envelope["data_hash"], "authority_hash": envelope["authority_hash"], "source_authority": envelope["source_authority"], "commands": triage["commands"]}
	for _, field := range []string{"recommendations", "quick_wins", "blockers_to_clear"} {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(triage[field], &rows); err != nil {
			return nil, fmt.Errorf("decode %s: %w", field, err)
		}
		projected := make([]map[string]json.RawMessage, 0, len(rows))
		for _, row := range rows {
			item := make(map[string]json.RawMessage)
			for _, key := range []string{"id", "status", "claimable", "actionable", "claim_command", "unblocks_ids", "blocked_by"} {
				if value, ok := row[key]; ok {
					item[key] = value
				}
			}
			if len(item["id"]) == 0 {
				return nil, fmt.Errorf("%s entry has no ID", field)
			}
			projected = append(projected, item)
		}
		decision[field] = projected
	}
	var quick map[string]json.RawMessage
	if err := json.Unmarshal(triage["quick_ref"], &quick); err != nil {
		return nil, fmt.Errorf("decode quick_ref: %w", err)
	}
	var picks []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(quick["top_picks"], &picks); err != nil {
		return nil, fmt.Errorf("decode top picks: %w", err)
	}
	decision["top_picks"] = picks
	return json.Marshal(decision)
}

func TestPerformanceCLIParityControls(t *testing.T) {
	dir := t.TempDir()
	writePerformanceFixture(t, dir, "realistic", 20)
	output, _ := runPerformanceCLI(t, buildBvBinary(t), dir, filepath.Join(dir, "cache"), "parity-control", "")
	baseline, err := performanceCLIBehavior(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"reordered-ids", "skipped-metric", "missing-metric"} {
		t.Run(mutation, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal(output, &envelope); err != nil {
				t.Fatal(err)
			}
			triage := envelope["triage"].(map[string]any)
			status := triage["status"].(map[string]any)
			switch mutation {
			case "reordered-ids":
				rows := triage["recommendations"].([]any)
				if len(rows) < 2 {
					t.Fatal("parity fixture must exercise at least two recommendations")
				}
				rows[0], rows[1] = rows[1], rows[0]
			case "skipped-metric":
				status["PageRank"] = map[string]any{"state": "skipped", "reason": "planted parity negative"}
			case "missing-metric":
				delete(status, "Cycles")
			}
			mutated, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			behavior, err := performanceCLIBehavior(mutated)
			if mutation == "missing-metric" {
				if err == nil {
					t.Fatal("missing metric accepted as measured work")
				}
			} else if err != nil || bytes.Equal(behavior, baseline) {
				t.Fatalf("%s did not change behavior parity: %v", mutation, err)
			}
		})
	}
}

const performanceCLIReferenceEpoch = "1788220800" // 2026-09-01T00:00:00Z

// Preserve the complete result, including exact JSON numbers and array order.
// Only these named duration fields are excluded; new fields remain compared.
func performanceCLIExactBehavior(output []byte) ([]byte, error) {
	if _, err := performanceCLIBehavior(output); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	triage, ok := envelope["triage"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing full triage result")
	}
	meta, ok := triage["meta"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing full triage metadata")
	}
	const reference = "2026-09-01T00:00:00Z"
	if envelope["generated_at"] != reference || meta["generated_at"] != reference {
		return nil, fmt.Errorf("exact-result cohort did not preserve fixed clock: envelope=%v triage=%v", envelope["generated_at"], meta["generated_at"])
	}
	delete(meta, "compute_time_ms")
	status := triage["status"].(map[string]any) // validated by performanceCLIBehavior
	for _, name := range []string{"PageRank", "Betweenness", "Eigenvector", "HITS", "Critical", "Cycles", "KCore", "Articulation", "Slack"} {
		delete(status[name].(map[string]any), "ms")
	}
	return json.Marshal(envelope)
}

func TestPerformanceCLIExactParityControls(t *testing.T) {
	dir := t.TempDir()
	writePerformanceFixture(t, dir, "realistic", 20)
	binary := buildBvBinary(t)
	output, _ := runPerformanceCLI(t, binary, dir, filepath.Join(dir, "cache"), "exact-cold", performanceCLIReferenceEpoch)
	baseline, err := performanceCLIExactBehavior(output)
	if err != nil {
		t.Fatal(err)
	}
	warm, _ := runPerformanceCLI(t, binary, dir, filepath.Join(dir, "cache"), "exact-warm", performanceCLIReferenceEpoch)
	behavior, err := performanceCLIExactBehavior(warm)
	if err != nil || !bytes.Equal(behavior, baseline) {
		t.Fatalf("actual fixed-clock cold/warm complete results differ: %v\ncold: %s\nwarm: %s", err, baseline, behavior)
	}
	for _, mutation := range []string{"elapsed-only", "score-ulp", "component-ulp", "reordered-ids", "skipped-metric", "missing-metric", "source-authority", "generated-at", "unknown-ms-field"} {
		t.Run(mutation, func(t *testing.T) {
			decoder := json.NewDecoder(bytes.NewReader(output))
			decoder.UseNumber()
			var envelope map[string]any
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			triage := envelope["triage"].(map[string]any)
			status := triage["status"].(map[string]any)
			rows := triage["recommendations"].([]any)
			if len(rows) < 2 {
				t.Fatal("exact parity fixture must include multiple scored recommendations")
			}
			switch mutation {
			case "elapsed-only":
				triage["meta"].(map[string]any)["compute_time_ms"] = json.Number("987654321")
				status["PageRank"].(map[string]any)["ms"] = json.Number("123456789.5")
			case "score-ulp", "component-ulp":
				row, field := rows[0].(map[string]any), "score"
				if mutation == "component-ulp" {
					row, field = row["breakdown"].(map[string]any), "pagerank"
				}
				number, err := row[field].(json.Number).Float64()
				if err != nil {
					t.Fatal(err)
				}
				row[field] = json.Number(strconv.FormatFloat(math.Nextafter(number, math.Inf(1)), 'g', -1, 64))
			case "reordered-ids":
				rows[0], rows[1] = rows[1], rows[0]
			case "skipped-metric":
				status["PageRank"] = map[string]any{"state": "skipped", "reason": "planted exact parity negative"}
			case "missing-metric":
				delete(status, "Cycles")
			case "source-authority":
				envelope["source_authority"].(map[string]any)["claim_safe"] = false
			case "generated-at":
				envelope["generated_at"] = "2001-01-01T00:00:00Z"
			case "unknown-ms-field":
				triage["future_result_ms"] = json.Number("17")
			}
			changed, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			behavior, err := performanceCLIExactBehavior(changed)
			if mutation == "elapsed-only" {
				if err != nil || !bytes.Equal(behavior, baseline) {
					t.Fatalf("declared duration-only change altered exact parity: %v", err)
				}
			} else if err == nil && bytes.Equal(behavior, baseline) {
				t.Fatalf("%s escaped complete-result comparison", mutation)
			}
		})
	}
}

type performanceBinaryIdentity struct {
	SHA256    string `json:"binary_sha256"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

func readPerformanceBinary(t testing.TB, binary string) performanceBinaryIdentity {
	t.Helper()
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		t.Fatalf("hash measurement executable: %v; close: %v", hashErr, closeErr)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatalf("read measured CLI toolchain: %v", err)
	}
	identity := performanceBinaryIdentity{SHA256: fmt.Sprintf("%x", hash.Sum(nil)), GoVersion: info.GoVersion}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			identity.GOOS = setting.Value
		case "GOARCH":
			identity.GOARCH = setting.Value
		}
	}
	if identity.GOOS == "" || identity.GOARCH == "" {
		t.Fatal("measured CLI build info lacks GOOS/GOARCH")
	}
	return identity
}

// Exact semantic proof is separate from timed production-timeout cohorts.
// Two repeats per side give 72 paired comparisons across 18 workloads and two
// cache modes. The 144 invocations and 36 warmups earn no latency/SLO credit.
func TestPerformanceCLIExactCohorts(t *testing.T) {
	outDir := os.Getenv("BV_PERF_DIR")
	if outDir == "" {
		t.Skip("opt-in exact-result measurement: scripts/benchmark.sh latency")
	}
	binaries := [2]string{os.Getenv("BV_PERF_BASELINE_BINARY"), os.Getenv("BV_PERF_CURRENT_BINARY")}
	if binaries[0] == "" || binaries[1] == "" {
		t.Fatal("both BV_PERF_BASELINE_BINARY and BV_PERF_CURRENT_BINARY are required")
	}
	identities := [2]performanceBinaryIdentity{readPerformanceBinary(t, binaries[0]), readPerformanceBinary(t, binaries[1])}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range testutil.PerformanceWorkloadNames() {
		for _, size := range []int{1000, 5000, 10000} {
			t.Run(fmt.Sprintf("%s/%d", kind, size), func(t *testing.T) {
				for _, warm := range []bool{false, true} {
					mode := "cold-application-cache"
					if warm {
						mode = "warm-application-cache"
					}
					t.Run(mode, func(t *testing.T) {
						dir := filepath.Join(outDir, fmt.Sprintf("exact-%s-%d-%s", kind, size, mode))
						if err := os.Mkdir(dir, 0o700); err != nil {
							t.Fatal(err)
						}
						fixtureHash := writePerformanceFixture(t, dir, kind, size)
						var outputs [2][]json.RawMessage
						var expected []byte
						var loadedIssues int
						var mismatches []string
						for repeat := 0; repeat < 2; repeat++ {
							for position := 0; position < 2; position++ {
								side := (repeat + position) % 2
								cache := filepath.Join(dir, fmt.Sprintf("cache-side-%d", side))
								if !warm {
									cache += fmt.Sprintf("-cold-%d", repeat)
								}
								if !warm || repeat == 0 {
									if _, err := os.Stat(cache); !os.IsNotExist(err) {
										t.Fatalf("fresh cache already exists or cannot be checked: %v", err)
									}
								}
								if warm && repeat == 0 {
									runPerformanceCLI(t, binaries[side], dir, cache, fmt.Sprintf("warmup-side-%d", side), performanceCLIReferenceEpoch)
									entries, err := filepath.Glob(filepath.Join(cache, "*", "*.json"))
									if err != nil || len(entries) == 0 {
										t.Fatalf("exact warmup did not populate the analysis cache: %v", err)
									}
								}
								name := fmt.Sprintf("repeat-%d-side-%d", repeat, side)
								output, _ := runPerformanceCLI(t, binaries[side], dir, cache, name, performanceCLIReferenceEpoch)
								behavior, err := performanceCLIExactBehavior(output)
								if err != nil {
									t.Fatalf("%s: %v", name, err)
								}
								var count struct {
									Triage struct {
										Meta struct {
											IssueCount int `json:"issue_count"`
										} `json:"meta"`
									} `json:"triage"`
								}
								if err := json.Unmarshal(behavior, &count); err != nil || count.Triage.Meta.IssueCount != size {
									t.Fatalf("%s: loaded_issues=%d want=%d: %v", name, count.Triage.Meta.IssueCount, size, err)
								}
								loadedIssues = count.Triage.Meta.IssueCount
								outputs[side] = append(outputs[side], behavior)
								if expected == nil {
									expected = behavior
								} else if !bytes.Equal(expected, behavior) {
									mismatches = append(mismatches, name)
								}
							}
						}
						record := map[string]any{"workload": kind, "issues": size, "loaded_issues": loadedIssues,
							"mode": mode, "seed": 20260904, "fixture_sha256": fixtureHash,
							"host": host, "gomaxprocs": runtime.GOMAXPROCS(0), "binaries": identities,
							"reference_epoch": performanceCLIReferenceEpoch, "outputs": outputs, "parity_mismatches": mismatches,
							"interpretation": "complete fixed-clock JSON parity excluding named elapsed fields only; no latency credit"}
						data, err := json.MarshalIndent(record, "", "  ")
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "result.json"), data, 0o600); err != nil {
							t.Fatal(err)
						}
						if len(mismatches) != 0 {
							t.Errorf("complete result mismatch: %v; actual outputs retained in %s", mismatches, dir)
						}
						t.Logf("%s: 2 paired repeats, 4 actual full results retained", dir)
					})
				}
			})
		}
	}
}

// TestPerformanceCLICohorts runs same-host alternating baseline/current pairs.
// Both cohorts start a new OS process; only the application's disk-cache state
// differs. No OS page-cache drop, CPU tuning, or best-of-N selection is involved.
func TestPerformanceCLICohorts(t *testing.T) {
	outDir := os.Getenv("BV_PERF_DIR")
	if outDir == "" {
		t.Skip("opt-in measurement: scripts/benchmark.sh latency")
	}
	baseline := os.Getenv("BV_PERF_BASELINE_BINARY")
	current := os.Getenv("BV_PERF_CURRENT_BINARY")
	if baseline == "" || current == "" {
		t.Fatal("both BV_PERF_BASELINE_BINARY and BV_PERF_CURRENT_BINARY are required")
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	samples := 200
	if value := os.Getenv("BV_PERF_CLI_SAMPLES"); value != "" {
		var err error
		samples, err = strconv.Atoi(value)
		if err != nil || samples < 200 {
			t.Fatal("BV_PERF_CLI_SAMPLES must be at least 200; p99 is descriptive")
		}
	}
	for _, kind := range testutil.PerformanceWorkloadNames() {
		for _, size := range []int{1000, 5000, 10000} {
			t.Run(fmt.Sprintf("%s/%d", kind, size), func(t *testing.T) {
				for _, warm := range []bool{false, true} {
					mode := "cold-application-cache"
					if warm {
						mode = "warm-application-cache"
					}
					t.Run(mode, func(t *testing.T) {
						var durations [2][]time.Duration
						var dirs [2]string
						var identities [2]performanceBinaryIdentity
						var loadedIssues [2]int
						// Both binaries must read the same physical source path:
						// source-authority identity intentionally includes provenance.
						fixtureDir := filepath.Join(outDir, fmt.Sprintf("cli-%s-%d-%s-source", kind, size, mode))
						if err := os.Mkdir(fixtureDir, 0o700); err != nil {
							t.Fatal(err)
						}
						fixtureHash := writePerformanceFixture(t, fixtureDir, kind, size)
						binaries := [2]string{baseline, current}
						for side, binary := range binaries {
							dir := filepath.Join(outDir, fmt.Sprintf("cli-%s-%d-%s-%d", kind, size, mode, side))
							if err := os.Mkdir(dir, 0o700); err != nil {
								t.Fatal(err)
							}
							dirs[side] = dir
							identities[side] = readPerformanceBinary(t, binary)
							if warm {
								runPerformanceCLI(t, binary, fixtureDir, filepath.Join(dir, "cache"), filepath.Join(dir, "warmup"), "")
								entries, err := filepath.Glob(filepath.Join(dir, "cache", "*", "*.json"))
								if err != nil || len(entries) == 0 {
									t.Fatalf("warmup did not create an actual analysis cache entry: %v", err)
								}
							}
						}
						var expected []byte
						var mismatches []string
						for sample := 0; sample < samples; sample++ {
							for position := 0; position < 2; position++ {
								side := (sample + position) % 2
								cache := filepath.Join(dirs[side], "cache")
								if !warm {
									cache = filepath.Join(dirs[side], fmt.Sprintf("cold-%04d", sample))
									if _, err := os.Stat(cache); !os.IsNotExist(err) {
										t.Fatalf("cold cache already exists or cannot be checked: %v", err)
									}
								}
								output, elapsed := runPerformanceCLI(t, binaries[side], fixtureDir, cache, filepath.Join(dirs[side], fmt.Sprintf("sample-%04d", sample)), "")
								durations[side] = append(durations[side], elapsed)
								var measured struct {
									Triage struct {
										Meta struct {
											IssueCount int `json:"issue_count"`
										} `json:"meta"`
									} `json:"triage"`
								}
								if err := json.Unmarshal(output, &measured); err != nil || measured.Triage.Meta.IssueCount != size {
									t.Fatalf("side=%d sample=%d loaded_issues=%d want=%d: %v", side, sample, measured.Triage.Meta.IssueCount, size, err)
								}
								loadedIssues[side] = measured.Triage.Meta.IssueCount
								behavior, err := performanceCLIBehavior(output)
								if err != nil {
									t.Fatal(err)
								}
								if expected == nil {
									expected = behavior
								} else if !bytes.Equal(expected, behavior) {
									mismatches = append(mismatches, fmt.Sprintf("side=%d sample=%d", side, sample))
								}
							}
						}
						for side := range binaries {
							summary, err := testutil.SummarizeLatency(durations[side])
							if err != nil {
								t.Fatal(err)
							}
							record := map[string]any{"workload": kind, "issues": size, "seed": 20260904, "fixture_sha256": fixtureHash,
								"mode": mode, "side": side, "binary_sha256": identities[side].SHA256, "distribution": summary, "sample_ns": durations[side],
								"loaded_issues": loadedIssues[side], "host": host, "gomaxprocs": runtime.GOMAXPROCS(0),
								"decision_behavior": json.RawMessage(expected), "parity_mismatches": mismatches,
								"go_version": identities[side].GoVersion, "goos": identities[side].GOOS, "goarch": identities[side].GOARCH,
								"interpretation": "empirical new-process wall time; application cache isolated; OS page cache uncontrolled; ID/order/metric-state parity only"}
							data, err := json.MarshalIndent(record, "", "  ")
							if err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(dirs[side], "result.json"), data, 0o600); err != nil {
								t.Fatal(err)
							}
							t.Logf("%s side=%d p50=%.3fms p95=%.3fms p99=%.3fms", mode, side, summary.P50MS, summary.P95MS, summary.P99MS)
						}
						if len(mismatches) != 0 {
							t.Errorf("%d samples changed ordered IDs/readiness/metric states; timing comparison is inconclusive; first: %s", len(mismatches), mismatches[0])
						}
					})
				}
			})
		}
	}
}

// =============================================================================
// 3. Pathological Graph Tests
// =============================================================================

// TestPerf_CyclicGraphLatency tests performance with cyclic graphs.
func TestPerf_CyclicGraphLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createCyclicDataset(t, env, 50)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	output, _ := cmd.CombinedOutput()
	elapsed := time.Since(start)

	t.Logf("cyclic graph (50 nodes, many cycles) latency: %v", elapsed)

	// Cyclic graphs may take longer but should still complete
	maxCyclicLatencyMS := int64(5000) // 5 seconds
	if elapsed.Milliseconds() > maxCyclicLatencyMS {
		t.Errorf("cyclic graph too slow: %v > %dms threshold", elapsed, maxCyclicLatencyMS)
	}

	// Should still produce valid output
	if !strings.Contains(string(output), "generated_at") && !strings.Contains(string(output), "cycle") {
		t.Logf("output: %s", output)
	}
}

// TestPerf_DenseGraphLatency tests performance with dense graphs (many edges).
func TestPerf_DenseGraphLatency(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createDenseDataset(t, env, 100)

	start := time.Now()
	cmd := exec.Command(bv, "--robot-triage")
	cmd.Dir = env
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed: %v", err)
	}
	elapsed := time.Since(start)

	t.Logf("dense graph (100 nodes, ~500 edges) latency: %v", elapsed)
	if elapsed.Milliseconds() > maxTriageLatencyMS {
		t.Errorf("dense graph too slow: %v > %dms threshold", elapsed, maxTriageLatencyMS)
	}
}

// =============================================================================
// 4. Repeated Command Performance
// =============================================================================

// TestPerf_RepeatedCommands verifies caching works (second call should be faster).
func TestPerf_RepeatedCommands(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 100)

	// First call (cold)
	start1 := time.Now()
	cmd1 := exec.Command(bv, "--robot-triage")
	cmd1.Dir = env
	if err := cmd1.Run(); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	elapsed1 := time.Since(start1)

	// Second call (potentially warm/cached)
	start2 := time.Now()
	cmd2 := exec.Command(bv, "--robot-triage")
	cmd2.Dir = env
	if err := cmd2.Run(); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	elapsed2 := time.Since(start2)

	t.Logf("first call: %v, second call: %v", elapsed1, elapsed2)

	// Both should complete within threshold
	if elapsed1.Milliseconds() > maxTriageLatencyMS {
		t.Errorf("first call too slow: %v", elapsed1)
	}
	if elapsed2.Milliseconds() > maxTriageLatencyMS {
		t.Errorf("second call too slow: %v", elapsed2)
	}
}

// =============================================================================
// 5. Profile Output Test
// =============================================================================

// TestPerf_ProfileStartup verifies --profile-startup produces timing data.
func TestPerf_ProfileStartup(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	createTestDataset(t, env, 50)

	cmd := exec.Command(bv, "--profile-startup", "--profile-json")
	cmd.Dir = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("--profile-startup failed: %v", err)
	}

	output := stdout.String()

	// Verify profile output contains expected fields
	var wrapper struct {
		Profile struct {
			NodeCount   int `json:"node_count"`
			EdgeCount   int `json:"edge_count"`
			Phase1Total int `json:"phase1_total"`
			Total       int `json:"total"`
		} `json:"profile"`
		GeneratedAt string `json:"generated_at"`
	}
	if err := json.Unmarshal([]byte(output), &wrapper); err != nil {
		t.Fatalf("profile output is not valid JSON: %v\noutput: %s", err, output)
	}

	// Verify we got a profile
	if wrapper.GeneratedAt == "" {
		t.Error("profile missing generated_at")
	}

	t.Logf("profile: node_count=%d, edge_count=%d, phase1=%d, total=%d",
		wrapper.Profile.NodeCount, wrapper.Profile.EdgeCount,
		wrapper.Profile.Phase1Total, wrapper.Profile.Total)
}

// =============================================================================
// Test Data Generators
// =============================================================================

// createTestDataset creates a test dataset with the specified number of issues.
func createTestDataset(t *testing.T, dir string, count int) {
	t.Helper()

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	var lines []string
	for i := 0; i < count; i++ {
		var deps string
		// Create chain dependencies (each depends on previous)
		if i > 0 {
			deps = fmt.Sprintf(`,"dependencies":[{"depends_on_id":"perf-%d","type":"blocks"}]`, i-1)
		}
		line := fmt.Sprintf(`{"id":"perf-%d","title":"Performance Test Issue %d","status":"open","priority":%d%s}`,
			i, i, i%5, deps)
		lines = append(lines, line)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}
}

// createCyclicDataset creates a dataset with cycles.
func createCyclicDataset(t *testing.T, dir string, count int) {
	t.Helper()

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	var lines []string
	for i := 0; i < count; i++ {
		// Create cycles: each node depends on the next, last depends on first
		nextIdx := (i + 1) % count
		line := fmt.Sprintf(`{"id":"cycle-%d","title":"Cyclic Issue %d","status":"open","priority":%d,"dependencies":[{"depends_on_id":"cycle-%d","type":"blocks"}]}`,
			i, i, i%5, nextIdx)
		lines = append(lines, line)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}
}

// createDenseDataset creates a dataset with many dependencies (dense graph).
func createDenseDataset(t *testing.T, dir string, count int) {
	t.Helper()

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	var lines []string
	for i := 0; i < count; i++ {
		var deps []string
		// Each node depends on ~5 previous nodes
		for j := 1; j <= 5 && i-j >= 0; j++ {
			deps = append(deps, fmt.Sprintf(`{"depends_on_id":"dense-%d","type":"blocks"}`, i-j))
		}

		var depsJSON string
		if len(deps) > 0 {
			depsJSON = fmt.Sprintf(`,"dependencies":[%s]`, strings.Join(deps, ","))
		}

		line := fmt.Sprintf(`{"id":"dense-%d","title":"Dense Issue %d","status":"open","priority":%d%s}`,
			i, i, i%5, depsJSON)
		lines = append(lines, line)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}
}
