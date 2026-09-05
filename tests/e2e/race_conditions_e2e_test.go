package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Race Condition Tests for bv-kozq
// Tests thread safety and concurrent access patterns.
// Run with: go test -race ./tests/e2e -run TestRace

// =============================================================================
// 1. Concurrent Robot Command Execution
// =============================================================================

// TestRace_ConcurrentRobotCommands tests that multiple robot commands can run
// concurrently without race conditions.
func TestRace_ConcurrentRobotCommands(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create a graph with dependencies
	issues := `{"id":"root","title":"Root","status":"open","priority":1}
{"id":"mid-1","title":"Mid 1","status":"open","priority":2,"dependencies":[{"depends_on_id":"root","type":"blocks"}]}
{"id":"mid-2","title":"Mid 2","status":"open","priority":2,"dependencies":[{"depends_on_id":"root","type":"blocks"}]}
{"id":"leaf-1","title":"Leaf 1","status":"open","priority":3,"dependencies":[{"depends_on_id":"mid-1","type":"blocks"}]}
{"id":"leaf-2","title":"Leaf 2","status":"open","priority":3,"dependencies":[{"depends_on_id":"mid-2","type":"blocks"}]}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Run multiple robot commands concurrently
	commands := [][]string{
		{"--robot-triage"},
		{"--robot-next"},
		{"--robot-graph", "--graph-format", "json"},
		{"--robot-plan"},
		{"--robot-priority"},
	}

	var wg sync.WaitGroup
	errors := make(chan error, len(commands)*3)

	// Run each command 3 times concurrently
	for i := 0; i < 3; i++ {
		for _, args := range commands {
			wg.Add(1)
			go func(cmdArgs []string) {
				defer wg.Done()
				cmd := exec.Command(bv, cmdArgs...)
				cmd.Dir = env
				if err := cmd.Run(); err != nil {
					errors <- err
				}
			}(args)
		}
	}

	wg.Wait()
	close(errors)

	// Check for errors
	var errCount int
	for err := range errors {
		t.Logf("concurrent command error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("had %d errors during concurrent execution", errCount)
	}
}

// TestRace_ConcurrentTriageRequests simulates multiple agents requesting triage
// simultaneously.
func TestRace_ConcurrentTriageRequests(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data with more issues for better concurrency stress
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	var issueLines []byte
	for i := 0; i < 50; i++ {
		idSuffix := string(rune('A'+i%26)) + string(rune('0'+i/26))
		line := []byte(`{"id":"issue-` + idSuffix + `","title":"Issue ` + idSuffix + `","status":"open","priority":` + string(rune('0'+i%5)) + `}` + "\n")
		issueLines = append(issueLines, line...)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, issueLines, 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Simulate 10 concurrent agent requests
	const numAgents = 10
	var wg sync.WaitGroup
	results := make(chan string, numAgents)
	errors := make(chan error, numAgents)

	for i := 0; i < numAgents; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bv, "--robot-triage")
			cmd.Dir = env
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				errors <- err
				return
			}
			results <- stdout.String()
		}()
	}

	wg.Wait()
	close(results)
	close(errors)

	// Verify all requests succeeded
	var resultCount int
	for range results {
		resultCount++
	}

	var errCount int
	for err := range errors {
		t.Logf("concurrent triage error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("had %d errors during concurrent triage requests", errCount)
	}

	if resultCount != numAgents {
		t.Errorf("expected %d results, got %d", numAgents, resultCount)
	}
}

// =============================================================================
// 2. Data Consistency Under Concurrent Access
// =============================================================================

// TestRace_DataConsistency verifies that concurrent reads return consistent data.
func TestRace_DataConsistency(t *testing.T) {
	const (
		pinnedEpochSeconds  = "1234567890"
		pinnedGeneratedAt   = "2009-02-13T23:31:30Z"
		robotCommandTimeout = 30 * time.Second
	)

	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create deterministic test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// A's deferral is active at the pinned epoch but expired in real time. D is
	// therefore the only ready diagnostic top pick at the pinned epoch. This
	// JSONL fixture has no verified live tracker, so it must not emit a claim.
	// E closed one
	// day before that epoch, so both triage and insights must count it in their
	// seven-day velocity windows. These values make failures to plumb the pinned
	// clock into analysis observable instead of merely checking stable bytes.
	issues := `{"id":"A","title":"Deferred task","status":"open","issue_type":"task","priority":0,"created_at":"2009-02-01T23:31:30Z","updated_at":"2009-02-13T22:31:30Z","defer_until":"2009-02-14T23:31:30Z"}
{"id":"B","title":"Planning parent","status":"open","issue_type":"epic","priority":1,"created_at":"2009-02-01T23:31:30Z","updated_at":"2009-02-13T21:31:30Z"}
{"id":"C","title":"Blocked child","status":"open","issue_type":"task","priority":1,"created_at":"2009-02-01T23:31:30Z","updated_at":"2009-02-13T21:31:30Z","dependencies":[{"depends_on_id":"B","type":"blocks"}]}
{"id":"D","title":"Ready task","status":"open","issue_type":"task","priority":4,"created_at":"2009-02-01T23:31:30Z","updated_at":"2009-02-13T20:31:30Z"}
{"id":"E","title":"Recently closed task","status":"closed","issue_type":"task","priority":2,"created_at":"2009-02-01T23:31:30Z","updated_at":"2009-02-12T23:31:30Z","closed_at":"2009-02-12T23:31:30Z"}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	commands := []struct {
		name string
		args []string
	}{
		{name: "next", args: []string{"--robot-next"}},
		{name: "triage", args: []string{"--robot-triage"}},
		{name: "insights", args: []string{"--robot-insights"}},
	}

	type velocityProjection struct {
		ClosedLast7Days int `json:"closed_last_7_days"`
	}
	type robotProjection struct {
		GeneratedAt  string `json:"generated_at"`
		ID           string `json:"id"`
		Actionable   bool   `json:"actionable"`
		ClaimCommand string `json:"claim_command"`
		Actions      *struct {
			LocalID           string          `json:"local_id"`
			UnavailableReason string          `json:"unavailable_reason"`
			Show              json.RawMessage `json:"show"`
			Claim             json.RawMessage `json:"claim"`
		} `json:"actions"`
		DiagnosticTopPick *struct {
			ID string `json:"id"`
		} `json:"diagnostic_top_pick"`
		Velocity *velocityProjection `json:"Velocity"`
		Triage   *struct {
			Meta struct {
				HistoryStatus string `json:"history_status"`
			} `json:"meta"`
			QuickRef struct {
				TopPicks []struct {
					ID string `json:"id"`
				} `json:"top_picks"`
			} `json:"quick_ref"`
			ProjectHealth struct {
				Velocity *velocityProjection `json:"velocity"`
			} `json:"project_health"`
		} `json:"triage"`
	}

	validatePinnedClock := func(command string, output []byte) error {
		var payload robotProjection
		if err := json.Unmarshal(output, &payload); err != nil {
			return fmt.Errorf("%s returned invalid JSON: %w; output=%q", command, err, output)
		}
		if payload.GeneratedAt != pinnedGeneratedAt {
			return fmt.Errorf("%s generated_at = %q, want pinned epoch %q", command, payload.GeneratedAt, pinnedGeneratedAt)
		}

		switch command {
		case "next":
			if payload.DiagnosticTopPick == nil || payload.DiagnosticTopPick.ID != "D" {
				return fmt.Errorf("next diagnostic pick = %+v, want D while A is deferred at %s", payload.DiagnosticTopPick, pinnedGeneratedAt)
			}
			if payload.Actionable || payload.ID != "" || payload.ClaimCommand != "" || payload.Actions == nil || payload.Actions.LocalID != "D" || payload.Actions.UnavailableReason == "" || len(payload.Actions.Show) != 0 || len(payload.Actions.Claim) != 0 {
				return fmt.Errorf("unbound next response emitted an actionable route: %s", output)
			}
		case "triage":
			if payload.Triage == nil {
				return fmt.Errorf("triage response omitted triage payload")
			}
			if payload.Triage.Meta.HistoryStatus != "skipped" {
				return fmt.Errorf("triage history_status = %q, want skipped under pinned clock", payload.Triage.Meta.HistoryStatus)
			}
			foundReady := false
			for _, pick := range payload.Triage.QuickRef.TopPicks {
				switch pick.ID {
				case "A":
					return fmt.Errorf("triage included A in top picks despite defer_until after %s", pinnedGeneratedAt)
				case "D":
					foundReady = true
				}
			}
			if !foundReady {
				return fmt.Errorf("triage top picks omitted D at pinned epoch %s", pinnedGeneratedAt)
			}
			if payload.Triage.ProjectHealth.Velocity == nil {
				return fmt.Errorf("triage response omitted project velocity")
			}
			if got := payload.Triage.ProjectHealth.Velocity.ClosedLast7Days; got != 1 {
				return fmt.Errorf("triage closed_last_7_days = %d, want 1 at pinned epoch %s", got, pinnedGeneratedAt)
			}
		case "insights":
			if payload.Velocity == nil {
				return fmt.Errorf("insights response omitted velocity")
			}
			if got := payload.Velocity.ClosedLast7Days; got != 1 {
				return fmt.Errorf("insights closed_last_7_days = %d, want 1 at pinned epoch %s", got, pinnedGeneratedAt)
			}
		default:
			return fmt.Errorf("missing pinned-clock validation for command %q", command)
		}
		return nil
	}

	for _, tt := range commands {
		t.Run(tt.name, func(t *testing.T) {
			cacheDir := t.TempDir()
			snapshotCache := func() (map[string][]byte, error) {
				paths, err := filepath.Glob(filepath.Join(cacheDir, "analysis_cache", "*.json"))
				if err != nil {
					return nil, fmt.Errorf("glob analysis cache: %w", err)
				}
				if len(paths) == 0 {
					return nil, fmt.Errorf("analysis command created no disk-cache entry under %s", cacheDir)
				}
				snapshot := make(map[string][]byte, len(paths))
				for _, path := range paths {
					raw, err := os.ReadFile(path)
					if err != nil {
						return nil, fmt.Errorf("read analysis cache entry %s: %w", path, err)
					}
					snapshot[filepath.Base(path)] = raw
				}
				return snapshot, nil
			}
			run := func() (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), robotCommandTimeout)
				defer cancel()

				cmd := exec.CommandContext(ctx, bv, tt.args...)
				cmd.Dir = env
				cmd.Env = append(os.Environ(),
					"SOURCE_DATE_EPOCH="+pinnedEpochSeconds,
					"BV_CACHE_DIR="+cacheDir,
					"BV_NO_BROWSER=1",
					"BV_TEST_MODE=1",
				)
				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				command := strings.Join(tt.args, " ")
				if err := cmd.Run(); err != nil {
					if ctx.Err() == context.DeadlineExceeded {
						return "", fmt.Errorf("%s timed out after %s in %s; stderr=%q; stdout=%q", command, robotCommandTimeout, env, stderr.String(), stdout.String())
					}
					return "", fmt.Errorf("%s failed in %s: %w; stderr=%q; stdout=%q", command, env, err, stderr.String(), stdout.String())
				}
				output := stdout.Bytes()
				if len(bytes.TrimSpace(output)) == 0 {
					return "", fmt.Errorf("%s returned empty robot output; stderr=%q", command, stderr.String())
				}
				if err := validatePinnedClock(tt.name, output); err != nil {
					return "", err
				}
				return stdout.String(), nil
			}

			// The first execution populates a cold cache; the second exercises the
			// warm-cache path. A pinned robot clock must make both byte-identical.
			want, err := run()
			if err != nil {
				t.Fatal(err)
			}
			coldCache, err := snapshotCache()
			if err != nil {
				t.Fatal(err)
			}
			warm, err := run()
			if err != nil {
				t.Fatal(err)
			}
			if warm != want {
				t.Fatalf("cold and warm output differ\ncold: %s\nwarm: %s", want, warm)
			}
			warmCache, err := snapshotCache()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(warmCache, coldCache) {
				t.Fatalf("warm invocation rewrote or added cache entries; cold entry count=%d, warm entry count=%d", len(coldCache), len(warmCache))
			}

			const concurrentReads = 3
			type readResult struct {
				output string
				err    error
			}
			results := make(chan readResult, concurrentReads)
			var wg sync.WaitGroup
			for i := 0; i < concurrentReads; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					output, err := run()
					results <- readResult{output: output, err: err}
				}()
			}
			wg.Wait()
			close(results)

			for result := range results {
				if result.err != nil {
					t.Error(result.err)
					continue
				}
				if result.output != want {
					t.Errorf("concurrent output differs from cold/warm baseline\nbaseline: %s\nconcurrent: %s", want, result.output)
				}
			}
		})
	}
}

// =============================================================================
// 3. Concurrent Analysis and Graph Commands
// =============================================================================

// TestRace_ConcurrentAnalysisAndGraph tests running analysis while getting graph data.
func TestRace_ConcurrentAnalysisAndGraph(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := `{"id":"test-1","title":"Test 1","status":"open","priority":1}
{"id":"test-2","title":"Test 2","status":"open","priority":2,"dependencies":[{"depends_on_id":"test-1","type":"blocks"}]}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	// Run analysis commands
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bv, "--robot-triage")
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				errors <- err
			}
		}()
	}

	// Run graph commands concurrently with different formats
	formats := []string{"json", "dot", "mermaid"}
	for _, fmt := range formats {
		wg.Add(1)
		go func(format string) {
			defer wg.Done()
			cmd := exec.Command(bv, "--robot-graph", "--graph-format", format)
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				errors <- err
			}
		}(fmt)
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Logf("concurrent analysis/graph error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("had %d errors during concurrent analysis and graph", errCount)
	}
}

// =============================================================================
// 4. Rapid Sequential Commands
// =============================================================================

// TestRace_RapidSequentialCommands tests rapid sequential command execution.
func TestRace_RapidSequentialCommands(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := `{"id":"rapid-1","title":"Rapid Test","status":"open","priority":1}`
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Run commands in rapid succession (no delay between them)
	commands := [][]string{
		{"--robot-triage"},
		{"--robot-next"},
		{"--robot-plan"},
		{"--robot-priority"},
		{"--robot-triage"},
		{"--robot-graph", "--graph-format", "json"},
		{"--robot-next"},
	}

	for i, args := range commands {
		cmd := exec.Command(bv, args...)
		cmd.Dir = env
		if err := cmd.Run(); err != nil {
			t.Errorf("command %d (%v) failed: %v", i, args, err)
		}
	}
}

// =============================================================================
// 5. Concurrent Different Output Formats
// =============================================================================

// TestRace_ConcurrentGraphFormats tests concurrent exports in different formats.
func TestRace_ConcurrentGraphFormats(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data with dependencies for interesting graph
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := `{"id":"fmt-1","title":"Format Test 1","status":"open","priority":1}
{"id":"fmt-2","title":"Format Test 2","status":"open","priority":2,"dependencies":[{"depends_on_id":"fmt-1","type":"blocks"}]}
{"id":"fmt-3","title":"Format Test 3","status":"open","priority":2,"dependencies":[{"depends_on_id":"fmt-1","type":"blocks"}]}`

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	var wg sync.WaitGroup
	errors := make(chan error, 3)

	formats := []string{"json", "dot", "mermaid"}
	for _, format := range formats {
		wg.Add(1)
		go func(fmt string) {
			defer wg.Done()
			cmd := exec.Command(bv, "--robot-graph", "--graph-format", fmt)
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				errors <- err
			}
		}(format)
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Logf("concurrent format error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("had %d errors during concurrent format exports", errCount)
	}
}

// =============================================================================
// 6. High Concurrency Stress Test
// =============================================================================

// TestRace_HighConcurrencyStress runs many concurrent operations.
func TestRace_HighConcurrencyStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Create larger dataset
	var issueLines []byte
	for i := 0; i < 100; i++ {
		var deps string
		if i > 0 {
			deps = `,"dependencies":[{"depends_on_id":"stress-` + string(rune('A'+(i-1)%26)) + string(rune('0'+(i-1)/26)) + `","type":"blocks"}]`
		}
		line := []byte(`{"id":"stress-` + string(rune('A'+i%26)) + string(rune('0'+i/26)) + `","title":"Stress Issue","status":"open","priority":` + string(rune('0'+i%5)) + deps + `}` + "\n")
		issueLines = append(issueLines, line...)
	}

	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, issueLines, 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Run 20 concurrent operations
	const numOps = 20
	var wg sync.WaitGroup
	errors := make(chan error, numOps)

	commands := [][]string{
		{"--robot-triage"},
		{"--robot-next"},
		{"--robot-plan"},
		{"--robot-graph", "--graph-format", "json"},
	}

	for i := 0; i < numOps; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			args := commands[idx%len(commands)]
			cmd := exec.Command(bv, args...)
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Logf("high concurrency error: %v", err)
		errCount++
	}

	// Allow some tolerance for high concurrency (resource contention is possible)
	if errCount > 2 {
		t.Errorf("too many errors during high concurrency: %d", errCount)
	}
}

// =============================================================================
// 7. Concurrent File Reading
// =============================================================================

// TestRace_ConcurrentFileReading tests that multiple processes can read
// the same beads files concurrently.
func TestRace_ConcurrentFileReading(t *testing.T) {
	bv := buildBvBinary(t)
	env := t.TempDir()

	// Create test data
	beadsDir := filepath.Join(env, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	issues := `{"id":"read-1","title":"Read Test","status":"open","priority":1}`
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(issuesPath, []byte(issues), 0644); err != nil {
		t.Fatalf("failed to write issues.jsonl: %v", err)
	}

	// Run multiple readers concurrently
	const numReaders = 10
	var wg sync.WaitGroup
	errors := make(chan error, numReaders)

	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(bv, "--robot-triage")
			cmd.Dir = env
			if err := cmd.Run(); err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	var errCount int
	for err := range errors {
		t.Logf("concurrent read error: %v", err)
		errCount++
	}

	if errCount > 0 {
		t.Errorf("had %d errors during concurrent file reading", errCount)
	}
}
