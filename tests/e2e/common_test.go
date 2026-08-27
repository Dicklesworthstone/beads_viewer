package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var bvBinaryPath string
var bvBinaryDir string
var e2eHomeDir string

var (
	scriptTUISupported      = true
	scriptTUIDisabledReason string
)

func TestMain(m *testing.M) {
	// Prevent any test from accidentally opening a browser
	os.Setenv("BV_NO_BROWSER", "1")
	os.Setenv("BV_TEST_MODE", "1")
	os.Setenv("BV_NO_SAVED_CONFIG", "1")

	// Isolate tests from any real user config or auth state on the machine.
	homeDir, err := os.MkdirTemp("", "bv-e2e-home-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create isolated HOME for e2e tests: %v\n", err)
		os.Exit(1)
	}
	e2eHomeDir = homeDir
	os.Setenv("HOME", homeDir)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))

	// Build the binary once for all tests
	if err := buildBvOnce(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build bv binary: %v\n", err)
		os.Exit(1)
	}

	scriptTUISupported, scriptTUIDisabledReason = detectScriptTUICapability(bvBinaryPath)

	code := m.Run()
	if bvBinaryDir != "" {
		_ = os.RemoveAll(bvBinaryDir)
	}
	if e2eHomeDir != "" {
		_ = os.RemoveAll(e2eHomeDir)
	}
	os.Exit(code)
}

func detectScriptTUICapability(bvPath string) (bool, string) {
	if _, err := exec.LookPath("script"); err != nil {
		return false, "script command not available"
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return false, "script TUI harness unsupported on this OS"
	}
	if bvPath == "" {
		return false, "bv binary path is empty"
	}

	// Smoke check: can we start bv under `script` and auto-close quickly?
	// If this fails, script-based TUI tests should skip immediately rather than
	// waiting out their per-test timeouts (common in CI/PTY mismatch scenarios).
	tempDir, err := os.MkdirTemp("", "bv-e2e-tui-cap-*")
	if err != nil {
		return false, fmt.Sprintf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	beadsDir := filepath.Join(tempDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		return false, fmt.Sprintf("failed to create beads dir: %v", err)
	}
	beads := `{"id":"cap-1","title":"Capability check","status":"open","priority":1,"issue_type":"task"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.jsonl"), []byte(beads), 0o644); err != nil {
		return false, fmt.Sprintf("failed to write beads.jsonl: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := scriptTUICommand(ctx, bvPath)
	if cmd == nil {
		return false, "script command unavailable"
	}
	cmd.Dir = tempDir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"BV_TUI_AUTOCLOSE_MS=250",
	)

	outFile := filepath.Join(tempDir, "script.out")
	f, err := os.Create(outFile)
	if err != nil {
		return false, fmt.Sprintf("failed to create output file: %v", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f

	runErr := cmd.Run()
	_ = f.Close()

	if ctx.Err() == context.DeadlineExceeded {
		return false, "bv did not auto-exit under script (PTY/CI mismatch)"
	}
	if runErr != nil {
		return false, fmt.Sprintf("script TUI run failed: %v", runErr)
	}

	return true, ""
}

func buildBvOnce() error {
	tempDir, err := os.MkdirTemp("", "bv-e2e-build-*")
	if err != nil {
		return err
	}
	bvBinaryDir = tempDir

	binName := "bv"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tempDir, binName)

	// Determine project root (../../) relative to this file
	// We assume tests are run from project root or package dir.
	// `go test ./tests/e2e/...` -> CWD is project root?
	// Actually `go test` sets CWD to the package directory.
	// So `../../` is correct for `tests/e2e`.

	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/bv")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build failed: %v\n%s", err, out)
	}

	bvBinaryPath = binPath
	return nil
}

// buildBvBinary returns the path to the pre-built binary.
// It acts as a helper to ensure tests use the shared binary.
func buildBvBinary(t *testing.T) string {
	t.Helper()
	if bvBinaryPath == "" {
		t.Fatal("bv binary not built")
	}
	return bvBinaryPath
}

// skipIfNoScript skips the test if the script command is unavailable
func skipIfNoScript(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("skipping: script command not available")
	}
	if !scriptTUISupported {
		if scriptTUIDisabledReason != "" {
			t.Skipf("skipping: %s", scriptTUIDisabledReason)
		}
		t.Skip("skipping: script-based TUI harness unavailable")
	}
}

// scriptTUICommand creates an exec.Cmd that runs the bv binary under `script`
// to provide a pseudo-TTY for TUI tests. This handles OS-specific differences:
// - macOS: script -q /dev/null <cmd> [args...]
// - Linux: script -q -c "<cmd> [args...]" /dev/null
// Returns nil if script is unavailable (test should skip).
func scriptTUICommand(ctx context.Context, bvPath string, args ...string) *exec.Cmd {
	// Check if script command is available
	if _, err := exec.LookPath("script"); err != nil {
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		// macOS: script -q /dev/null <cmd> [args...]
		scriptArgs := []string{"-q", "/dev/null", bvPath}
		scriptArgs = append(scriptArgs, args...)
		return exec.CommandContext(ctx, "script", scriptArgs...)

	case "linux":
		// Linux: script -q -c "<cmd> [args...]" /dev/null
		// Build the command string - need to quote/escape properly
		cmdStr := bvPath
		for _, arg := range args {
			// Simple quoting for args with spaces
			if strings.ContainsAny(arg, " \t") {
				cmdStr += " \"" + arg + "\""
			} else {
				cmdStr += " " + arg
			}
		}
		// -e: return child exit code; -f: flush output (both help avoid hangs in CI)
		return exec.CommandContext(ctx, "script", "-q", "-e", "-f", "-c", cmdStr, "/dev/null")

	default:
		// Windows and others don't have script
		return nil
	}
}

// ensureCmdStdinCloses wires a controllable stdin for command execution.
//
// Some `script` implementations can keep the pseudo-TTY session open until stdin
// is closed, even if the child process has exited. In CI, the parent process
// stdin may never close; using a pipe gives us a reliable way to force EOF.
//
// If cmd.Stdin is already set, this is a no-op (the caller owns stdin).
func ensureCmdStdinCloses(t *testing.T, ctx context.Context, cmd *exec.Cmd, closeAfter time.Duration) {
	t.Helper()
	if cmd == nil || cmd.Stdin != nil {
		return
	}
	stdinR, stdinW := io.Pipe()
	cmd.Stdin = stdinR
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = stdinR.Close()
	})

	go func() {
		select {
		case <-ctx.Done():
			_ = stdinW.Close()
		case <-time.After(closeAfter):
			_ = stdinW.Close()
		}
	}()
}

// runCmdToFile runs a command and captures stdout+stderr to a temp file.
//
// This avoids exec.Cmd's CombinedOutput piping, which can interact poorly with
// `script` on some platforms and cause hangs/timeouts.
func runCmdToFile(t *testing.T, cmd *exec.Cmd) ([]byte, error) {
	t.Helper()
	if cmd == nil {
		return nil, fmt.Errorf("nil cmd")
	}

	outPath := filepath.Join(t.TempDir(), "cmd.out")
	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("create output file: %w", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f

	runErr := cmd.Run()
	_ = f.Close()

	out, readErr := os.ReadFile(outPath)
	if readErr != nil {
		return nil, fmt.Errorf("read output file: %w (run err: %v)", readErr, runErr)
	}
	return out, runErr
}

// ============================================================================
// DetailedLogger - Enhanced test logging for E2E debugging
// ============================================================================

// DetailedLogger provides structured logging for E2E tests with automatic
// failure reports. It tracks steps, metrics, and produces a detailed report
// when a test fails.
type DetailedLogger struct {
	t       *testing.T
	started time.Time
	mu      sync.Mutex
	steps   []logStep
	metrics map[string]int64
}

type logStep struct {
	time    time.Time
	message string
}

// newDetailedLogger creates a new DetailedLogger attached to the test.
// It automatically generates a failure report via t.Cleanup when the test fails.
func newDetailedLogger(t *testing.T) *DetailedLogger {
	t.Helper()
	l := &DetailedLogger{
		t:       t,
		started: time.Now(),
		metrics: make(map[string]int64),
	}
	t.Cleanup(l.report)
	return l
}

// Step logs a named step in the test execution with timing.
func (l *DetailedLogger) Step(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	l.steps = append(l.steps, logStep{
		time:    time.Now(),
		message: msg,
	})
	l.t.Logf("[STEP %d] %s", len(l.steps), msg)
}

// Metric records a named numeric metric for later reporting.
func (l *DetailedLogger) Metric(name string, value int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.metrics[name] = value
	l.t.Logf("[METRIC] %s = %d", name, value)
}

// MetricDuration records a duration metric in milliseconds.
func (l *DetailedLogger) MetricDuration(name string, d time.Duration) {
	l.Metric(name+"_ms", d.Milliseconds())
}

// Success logs a success message with total elapsed time.
func (l *DetailedLogger) Success(msg string) {
	l.t.Logf("[SUCCESS] %s (total time: %v)", msg, time.Since(l.started))
}

// Error logs an error message with context.
func (l *DetailedLogger) Error(format string, args ...any) {
	l.t.Logf("[ERROR] "+format, args...)
}

// report generates a detailed failure report when the test fails.
func (l *DetailedLogger) report() {
	if !l.t.Failed() {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.t.Log("")
	l.t.Log("╔══════════════════════════════════════════════════════════════════╗")
	l.t.Log("║                        FAILURE REPORT                            ║")
	l.t.Log("╚══════════════════════════════════════════════════════════════════╝")
	l.t.Logf("Test: %s", l.t.Name())
	l.t.Logf("Total duration: %v", time.Since(l.started))
	l.t.Logf("Steps completed: %d", len(l.steps))
	l.t.Log("")

	if len(l.steps) > 0 {
		l.t.Log("── Steps Timeline ──")
		for i, step := range l.steps {
			elapsed := step.time.Sub(l.started)
			l.t.Logf("  [%v] Step %d: %s", elapsed.Truncate(time.Millisecond), i+1, step.message)
		}
		l.t.Log("")
	}

	if len(l.metrics) > 0 {
		l.t.Log("── Metrics Collected ──")
		for k, v := range l.metrics {
			l.t.Logf("  %s: %d", k, v)
		}
		l.t.Log("")
	}
}

// ============================================================================
// BV Command Runner - Run bv with robot flags
// ============================================================================

// runBVCommand runs the bv binary with the given arguments and returns stdout.
// It automatically sets up the working directory and environment.
func runBVCommand(t *testing.T, workDir string, args ...string) ([]byte, error) {
	t.Helper()

	binPath := buildBvBinary(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"BV_NO_BROWSER=1",
		"BV_TEST_MODE=1",
		"TERM=dumb",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("command timed out after 30s: %w", err)
		}
		return nil, fmt.Errorf("command failed: %w\nstderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

// runBVCommandJSON runs the bv binary and parses the JSON output into result.
func runBVCommandJSON(t *testing.T, workDir string, result any, args ...string) error {
	t.Helper()

	out, err := runBVCommand(t, workDir, args...)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("failed to parse JSON: %w\nraw output: %s", err, string(out))
	}

	return nil
}

// ============================================================================
// Test Fixture Utilities
// ============================================================================

// TestFixture represents a temporary test environment with .beads data.
type TestFixture struct {
	t      *testing.T
	Dir    string
	beads  []fixtureIssue
	nextID int
}

type fixtureIssue struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status"`
	Priority     int      `json:"priority"`
	IssueType    string   `json:"issue_type"`
	Labels       []string `json:"labels,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
	CreatedAt    string   `json:"created_at"`
}

// NewTestFixture creates a new test fixture with a temporary directory.
func NewTestFixture(t *testing.T) *TestFixture {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	return &TestFixture{
		t:      t,
		Dir:    dir,
		nextID: 1,
	}
}

// AddIssue adds an issue to the fixture.
func (f *TestFixture) AddIssue(title, status string, priority int, issueType string) string {
	f.t.Helper()
	id := fmt.Sprintf("test-%04d", f.nextID)
	f.nextID++

	f.beads = append(f.beads, fixtureIssue{
		ID:        id,
		Title:     title,
		Status:    status,
		Priority:  priority,
		IssueType: issueType,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	return id
}

// AddIssueWithDeps adds an issue with dependencies.
func (f *TestFixture) AddIssueWithDeps(title, status string, priority int, issueType string, deps ...string) string {
	f.t.Helper()
	id := fmt.Sprintf("test-%04d", f.nextID)
	f.nextID++

	f.beads = append(f.beads, fixtureIssue{
		ID:           id,
		Title:        title,
		Status:       status,
		Priority:     priority,
		IssueType:    issueType,
		Dependencies: deps,
		CreatedAt:    time.Now().Format(time.RFC3339),
	})
	return id
}

// AddIssueWithLabels adds an issue with labels.
func (f *TestFixture) AddIssueWithLabels(title, status string, priority int, issueType string, labels ...string) string {
	f.t.Helper()
	id := fmt.Sprintf("test-%04d", f.nextID)
	f.nextID++

	f.beads = append(f.beads, fixtureIssue{
		ID:        id,
		Title:     title,
		Status:    status,
		Priority:  priority,
		IssueType: issueType,
		Labels:    labels,
		CreatedAt: time.Now().Format(time.RFC3339),
	})
	return id
}

// Write creates a current-br issue export plus its source-routing metadata.
func (f *TestFixture) Write() error {
	f.t.Helper()

	beadsDir := filepath.Join(f.Dir, ".beads")
	beadsPath := filepath.Join(beadsDir, "issues.jsonl")
	file, err := os.Create(beadsPath)
	if err != nil {
		return fmt.Errorf("create issues.jsonl: %w", err)
	}

	for _, issue := range f.beads {
		data, err := json.Marshal(issue)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("marshal issue %s: %w", issue.ID, err)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return fmt.Errorf("write issue %s: %w", issue.ID, err)
		}
		if _, err := file.WriteString("\n"); err != nil {
			_ = file.Close()
			return fmt.Errorf("write newline: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close issues.jsonl: %w", err)
	}
	metadata := []byte("{\"database\":\"beads.db\",\"jsonl_export\":\"issues.jsonl\"}\n")
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0o644); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}

	return nil
}

// Count returns the number of issues in the fixture.
func (f *TestFixture) Count() int {
	return len(f.beads)
}

// ============================================================================
// Fixture Builders - Create common test scenarios
// ============================================================================

// FixtureConfig holds configuration for fixture generation.
type FixtureConfig struct {
	NumBeads        int
	NumDependencies int
	NumCycles       int
	MaxDepth        int
	BlockedCount    int
	ActionableCount int
}

// createGraphFixture creates a fixture with a specified graph structure.
func createGraphFixture(t *testing.T, cfg FixtureConfig) *TestFixture {
	t.Helper()
	f := NewTestFixture(t)

	// Create base issues
	ids := make([]string, 0, cfg.NumBeads)
	for i := 0; i < cfg.NumBeads; i++ {
		id := f.AddIssue(
			fmt.Sprintf("Issue %d", i+1),
			"open",
			2,
			"task",
		)
		ids = append(ids, id)
	}

	// Add dependencies (respecting depth constraint)
	depsAdded := 0
	for i := 1; i < len(ids) && depsAdded < cfg.NumDependencies; i++ {
		// Depend on a random earlier issue
		depIdx := i - 1
		if cfg.MaxDepth > 0 && i > cfg.MaxDepth {
			depIdx = i - cfg.MaxDepth + (i % cfg.MaxDepth)
		}
		f.beads[i].Dependencies = append(f.beads[i].Dependencies, ids[depIdx])
		depsAdded++
	}

	// Add cycles if requested
	for c := 0; c < cfg.NumCycles && c+2 < len(ids); c++ {
		// Create a cycle: c -> c+1 -> c+2 -> c
		base := c * 3
		if base+2 < len(ids) {
			f.beads[base+2].Dependencies = append(f.beads[base+2].Dependencies, ids[base])
		}
	}

	if err := f.Write(); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	return f
}

// createTriageFixture creates a fixture with blocked and actionable issues.
func createTriageFixture(t *testing.T, cfg FixtureConfig) *TestFixture {
	t.Helper()
	f := NewTestFixture(t)

	total := cfg.BlockedCount + cfg.ActionableCount
	if total == 0 {
		total = 50
		cfg.ActionableCount = 30
		cfg.BlockedCount = 20
	}

	// Create blocker issues first (these will block others)
	blockerIDs := make([]string, 0)
	numBlockers := cfg.BlockedCount / 2
	if numBlockers == 0 {
		numBlockers = 1
	}
	for i := 0; i < numBlockers; i++ {
		id := f.AddIssue(
			fmt.Sprintf("Blocker %d", i+1),
			"open",
			1,
			"task",
		)
		blockerIDs = append(blockerIDs, id)
	}

	// Create blocked issues (depend on blockers)
	for i := 0; i < cfg.BlockedCount; i++ {
		blockerId := blockerIDs[i%len(blockerIDs)]
		f.AddIssueWithDeps(
			fmt.Sprintf("Blocked issue %d", i+1),
			"open",
			2,
			"task",
			blockerId,
		)
	}

	// Create actionable issues (no dependencies on open issues)
	for i := 0; i < cfg.ActionableCount; i++ {
		f.AddIssue(
			fmt.Sprintf("Actionable issue %d", i+1),
			"open",
			2,
			"task",
		)
	}

	if err := f.Write(); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	return f
}
