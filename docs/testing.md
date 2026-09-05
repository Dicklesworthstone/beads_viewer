# Testing Guide for bv Contributors

This guide explains how to write and run tests for the bv codebase. All contributions should include appropriate tests.

## Testing Philosophy

### No Mocks/Fakes

We prefer **concrete test data** over mocks or fakes. This approach:
- Makes tests easier to understand and debug
- Avoids the complexity of maintaining mock implementations
- Ensures tests exercise real code paths
- Produces more reliable tests

Instead of mocking:
```go
// DON'T do this
mockAnalyzer := &MockAnalyzer{}
mockAnalyzer.On("Analyze").Return(fakeStats)

// DO this
issues := testutil.QuickChain(5)  // Real issues with real dependencies
analyzer := analysis.NewAnalyzer(issues)
stats := analyzer.Analyze()  // Real analysis
```

### Table-Driven Tests

Use table-driven tests for comprehensive coverage:

```go
func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected int
        wantErr  bool
    }{
        {"empty input", "", 0, false},
        {"single item", "one", 1, false},
        {"invalid", "bad", 0, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := MyFunction(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("MyFunction() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.expected {
                t.Errorf("MyFunction() = %v, want %v", got, tt.expected)
            }
        })
    }
}
```

### Golden Files

For complex outputs (JSON, rendered views, SVG), use golden file testing:

```go
func TestComplexOutput(t *testing.T) {
    golden := testutil.NewGoldenFile(t, "testdata/golden", "output.json")

    result := GenerateComplexOutput()
    golden.AssertJSON(result)
}
```

Update golden files when intentionally changing output:
```bash
GENERATE_GOLDEN=1 go test ./pkg/...
```

### Deterministic Output

Tests must produce deterministic results:
- Use fixed random seeds (`testutil.DefaultConfig()` uses seed 42)
- Use fixed timestamps (`time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)`)
- Sort slices before comparison if order doesn't matter

## Test Organization

### File Naming

- Unit tests: `*_test.go` in the same directory as the code
- Package tests: `package_test` (black-box) or `package` (white-box)
- E2E tests: `tests/e2e/*_test.go`

### Test Function Naming

```go
// Unit tests: TestFunctionName_Scenario
func TestExtractKeywords_FiltersStopWords(t *testing.T) { ... }
func TestExtractKeywords_HandlesEmptyInput(t *testing.T) { ... }

// Integration tests: TestIntegration_Feature
func TestIntegration_RobotTriageCommand(t *testing.T) { ... }

// E2E tests: TestEndToEnd_Workflow
func TestEndToEnd_RobotPlanCommand(t *testing.T) { ... }
```

### Subtests

Group related tests with `t.Run()`:

```go
func TestAnalyzer(t *testing.T) {
    t.Run("Empty", func(t *testing.T) {
        // test empty input
    })
    t.Run("SingleNode", func(t *testing.T) {
        // test single node
    })
    t.Run("Chain", func(t *testing.T) {
        // test chain topology
    })
}
```

## Test Helpers (`pkg/testutil`)

### Fixture Generators

The `testutil` package provides graph topology generators:

```go
// Quick convenience functions
issues := testutil.QuickChain(10)      // Linear chain: n0 <- n1 <- ... <- n9
issues := testutil.QuickStar(5)        // Hub with 5 spokes
issues := testutil.QuickDiamond(3)     // Diamond with 3 middle nodes
issues := testutil.QuickCycle(4)       // Circular dependency (invalid DAG)
issues := testutil.QuickTree(3, 2)     // Tree: depth=3, breadth=2
issues := testutil.QuickRandom(20, 0.3) // Random DAG: 20 nodes, 30% edge density

// Edge cases
issues := testutil.Empty()             // Empty slice
issues := testutil.Single()            // Single node, no deps
```

For custom configuration:

```go
gen := testutil.New(testutil.GeneratorConfig{
    Seed:          42,
    IDPrefix:      "TEST",
    IncludeLabels: true,
    StatusMix:     []model.Status{model.StatusOpen, model.StatusInProgress},
})

fixture := gen.Chain(10)
issues := gen.ToIssues(fixture)
```

### Assertions

```go
testutil.AssertIssueCount(t, issues, 10)
testutil.AssertNoDuplicateIDs(t, issues)
testutil.AssertAllValid(t, issues)
testutil.AssertDependencyExists(t, issues, "from-id", "to-id")
testutil.AssertNoCycles(t, issues)
testutil.AssertHasCycle(t, issues)
testutil.AssertStatusCounts(t, issues, open, inProgress, blocked, closed)
testutil.AssertJSONEqual(t, expected, actual)
```

### Temporary Directories

```go
// Create temp dir with .beads subdirectory
dir := testutil.TempBeadsDir(t)  // Cleaned up automatically

// Write issues to .beads/beads.jsonl
path := testutil.WriteBeadsFile(t, dir, issues)
```

### Issue Helpers

```go
// Build lookup map
issueMap := testutil.BuildIssueMap(issues)
issue := issueMap["issue-id"]

// Find single issue
issue := testutil.FindIssue(issues, "issue-id")

// Get statistics
counts := testutil.CountByStatus(issues)
ids := testutil.GetIDs(issues)
```

## Running Tests

### Basic Commands

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run specific package
go test ./pkg/analysis/...

# Run specific test
go test -v -run TestExtractKeywords ./pkg/analysis/...

# Run with race detector
go test -race ./...
```

### Coverage

```bash
# Using the coverage script (recommended)
./scripts/coverage.sh          # Summary
./scripts/coverage.sh html     # Open HTML report
./scripts/coverage.sh check    # Check thresholds
./scripts/coverage.sh pkg      # Per-package breakdown

# By default the script runs coverage for ./pkg/... (fast). Override if needed:
COVER_PACKAGES='./cmd/... ./pkg/...' ./scripts/coverage.sh check

# Manual commands
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
go tool cover -func=coverage.out
```

### Benchmarks

```bash
# Run all benchmarks
./scripts/benchmark.sh

# Run specific benchmark
go test -bench=BenchmarkFullAnalysis -benchmem ./pkg/analysis/...

# Compare against baseline
./scripts/benchmark.sh baseline  # Save current as baseline
./scripts/benchmark.sh compare   # Run and compare
```

### Performance Tests

Performance-sensitive tests are gated behind `PERF_TEST=1`:

```bash
PERF_TEST=1 go test -v ./pkg/analysis/... -run TestE2EStartup
```

## E2E Tests

E2E tests verify the complete `bv` binary behavior:

### Running

The E2E suite includes a few large-scale/stress scenarios guarded by `testing.Short()`.

```bash
# Fast/CI-friendly run (skips stress cases)
go test -short ./tests/e2e

# Full run
go test ./tests/e2e
```

### Pattern

```go
func TestEndToEnd_Feature(t *testing.T) {
    // 1. Use the shared bv binary (built once in TestMain)
    bv := buildBvBinary(t)

    // 2. Create test environment
    envDir := t.TempDir()
    os.MkdirAll(filepath.Join(envDir, ".beads"), 0755)
    os.WriteFile(filepath.Join(envDir, ".beads", "beads.jsonl"), []byte(jsonl), 0644)

    // 3. Execute command
    runCmd := exec.Command(bv, "--robot-triage")
    runCmd.Dir = envDir
    out, err := runCmd.CombinedOutput()
    if err != nil {
        t.Fatalf("Command failed: %v\n%s", err, out)
    }

    // 4. Verify output
    var result map[string]interface{}
    if err := json.Unmarshal(out, &result); err != nil {
        t.Fatalf("Invalid JSON: %v", err)
    }

    // Assert expected fields exist
    if _, ok := result["triage"]; !ok {
        t.Error("missing 'triage' field")
    }
}
```

### Robot Command Testing

Test all `--robot-*` flags produce valid JSON:

```go
// Verify JSON output
var result map[string]interface{}
json.Unmarshal(out, &result)

// Check required fields
if _, ok := result["generated_at"]; !ok {
    t.Error("missing 'generated_at'")
}
```

## CI Integration

The checked-in CI workflow is configured for pushes and pull requests, but
the CI, Release, Fuzz, and Flake Update workflows were `disabled_manually`
when checked on 2026-09-05 with `gh workflow list --all`. A workflow file or
badge does not establish that a run occurred. Check the live workflow state
and run results before relying on CI or Codecov coverage.

Run the source tests locally or through RCH. Releases require the complete
local gate described in [RELEASING.md](RELEASING.md). The checked-in CI gate
invocation skips benchmarks and allows missing checks; it is diagnostic and
cannot authorize packaging. Its log-upload path also needs updating before
the workflow is re-enabled. No workflow was re-enabled by this change.

For local stress-testing, consider running the race detector:

```bash
go test -race ./...
```

### Coverage Thresholds

The following table describes configured per-package CI thresholds. They are
enforced only when the corresponding workflow actually runs; the configured
aggregate threshold for `pkg/*` is 71%.

| Package | Minimum |
|---------|---------|
| `pkg/analysis` | 75% |
| `pkg/export` | 69% |
| `pkg/recipe` | 90% |
| `pkg/ui` | 55% |
| `pkg/loader` | 80% |
| `pkg/updater` | 55% |
| `pkg/watcher` | 60% |
| `pkg/workspace` | 85% |

## Best Practices

1. **Test behavior, not implementation** - Focus on what functions do, not how
2. **One assertion per test case** - Makes failures easier to diagnose
3. **Use `t.Helper()`** - Mark helper functions for better error locations
4. **Clean up resources** - Use `t.TempDir()` and `t.Cleanup()`
5. **Avoid sleeping** - Use channels or polling instead of `time.Sleep()`
6. **Test edge cases** - Empty inputs, nil values, boundary conditions
7. **Document test intent** - Comment what each test case validates

## Troubleshooting

### Flaky Tests

If tests fail intermittently:
- Check for non-deterministic ordering (use `sort.Slice`)
- Look for time-dependent logic (use fixed timestamps)
- Check for race conditions (`go test -race`)
- Verify cleanup between tests

### Slow Tests

- Use `-short` flag to skip slow tests: `if testing.Short() { t.Skip() }`
- Gate performance tests behind `PERF_TEST=1`
- Profile with `go test -cpuprofile=cpu.out`

### Coverage Gaps

Run coverage locally to identify untested paths:
```bash
./scripts/coverage.sh html  # Opens browser with coverage highlighting
./scripts/coverage.sh uncovered  # Lists uncovered lines
```
