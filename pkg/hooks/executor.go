package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// HookOutputLimit is the maximum number of bytes retained from each output
// stream of each hook. Excess output is drained without retaining it.
const HookOutputLimit = 1 << 20

// A descendant that escapes process cleanup must not keep an export waiting
// indefinitely for inherited stdout/stderr handles to close.
const hookWaitDelay = 250 * time.Millisecond

// HookResult contains the result of a hook execution
type HookResult struct {
	Hook            Hook
	Phase           HookPhase
	Success         bool
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
	Error           error
}

// cappedHookOutput always accepts the full write so a verbose child cannot
// deadlock on an undrained pipe or grow the exporter's memory without bound.
// Each stream has its own writer, used by one os/exec copying goroutine.
type cappedHookOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedHookOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := HookOutputLimit - b.buffer.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *cappedHookOutput) String() string { return b.buffer.String() }

// Executor runs hooks with proper environment and timeout handling
type Executor struct {
	config           *Config
	context          ExportContext
	results          []HookResult
	logger           func(string)
	allowEnv         []string
	authorizationErr error
}

// NewExecutor freezes the supplied configuration and requires explicit approval
// before either phase can run. Omitting options denies execution of all hooks.
func NewExecutor(config *Config, ctx ExportContext, options ...ExecutionOptions) *Executor {
	e := &Executor{
		config:  cloneConfig(config),
		context: ctx,
		results: make([]HookResult, 0),
		logger:  func(string) {}, // No-op default
	}
	var opts ExecutionOptions
	if len(options) > 1 {
		e.authorizationErr = fmt.Errorf("expected at most one hook execution policy")
		return e
	}
	if len(options) == 1 {
		opts = options[0]
	}
	e.allowEnv = append([]string(nil), opts.AllowEnv...)
	if err := e.config.authorize(opts.TrustDigest); err != nil {
		e.authorizationErr = err
		return e
	}
	if !e.config.hasHooks() {
		return e
	}
	if err := validateEnvGrants(e.allowEnv); err != nil {
		e.authorizationErr = err
		return e
	}
	// Validate every declaration before running any hook. An invalid post-hook
	// must not allow a pre-hook to run, even with on_error: continue.
	for _, phase := range [][]Hook{e.config.Hooks.PreExport, e.config.Hooks.PostExport} {
		for _, hook := range phase {
			if err := validateHookEnv(hook.Env, runtime.GOOS == "windows"); err != nil {
				e.authorizationErr = fmt.Errorf("hook %q: %w", hook.Name, err)
				return e
			}
		}
	}
	if e.config.projectDir == "" {
		var err error
		e.config.projectDir, err = os.Getwd()
		if err != nil {
			e.authorizationErr = fmt.Errorf("resolving hook working directory: %w", err)
		}
	}
	return e
}

// SetLogger sets the logger function for hook execution details
func (e *Executor) SetLogger(logger func(string)) {
	if logger == nil {
		e.logger = func(string) {}
		return
	}
	e.logger = logger
}

// RunPreExport executes all pre-export hooks
// Returns error if any hook fails with on_error="fail"
func (e *Executor) RunPreExport() error {
	if e.authorizationErr != nil {
		return e.authorizationErr
	}
	if e.config == nil {
		return nil
	}

	for _, hook := range e.config.Hooks.PreExport {
		e.logger(fmt.Sprintf("Running pre-export hook %q: %q", hook.Name, hook.Command))
		result := e.runHook(hook, PreExport)
		e.results = append(e.results, result)

		if !result.Success && hook.OnError == "fail" {
			return fmt.Errorf("pre-export hook %q failed: %w", hook.Name, result.Error)
		}
	}

	return nil
}

// RunPostExport executes all post-export hooks
// Errors are logged but don't fail (unless on_error="fail")
func (e *Executor) RunPostExport() error {
	if e.authorizationErr != nil {
		return e.authorizationErr
	}
	if e.config == nil {
		return nil
	}

	var firstError error
	for _, hook := range e.config.Hooks.PostExport {
		e.logger(fmt.Sprintf("Running post-export hook %q: %q", hook.Name, hook.Command))
		result := e.runHook(hook, PostExport)
		e.results = append(e.results, result)

		if !result.Success && hook.OnError == "fail" && firstError == nil {
			firstError = fmt.Errorf("post-export hook %q failed: %w", hook.Name, result.Error)
		}
	}

	return firstError
}

// shellInvocation preserves the approved command as one argument. On Windows,
// configureHookShell supplies cmd.exe's raw command line; Go's default Windows
// argument escaping is intended for a different parser.
func shellInvocation(platform, command string) (string, []string) {
	if platform == "windows" {
		return "cmd", []string{"/D", "/S", "/C", command}
	}
	return "sh", []string{"-c", command}
}

// runHook executes a single hook with timeout and environment
func (e *Executor) runHook(hook Hook, phase HookPhase) HookResult {
	result := HookResult{
		Hook:  hook,
		Phase: phase,
	}
	result.Hook.Env = maps.Clone(hook.Env)
	if e.authorizationErr != nil {
		result.Error = e.authorizationErr
		return result
	}

	start := time.Now()

	// Create context with timeout
	timeout := hook.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Create command - use shell to interpret the command
	shell, args := shellInvocation(runtime.GOOS, hook.Command)
	cmd := exec.CommandContext(ctx, shell, args...)
	configureHookShell(cmd, hook.Command)
	cmd.Dir = e.config.projectDir
	cmd.WaitDelay = hookWaitDelay

	// Expansion and execution see the same filtered environment. Only a grant
	// supplied by the invoking user can restore a protected parent variable.
	var err error
	cmd.Env, err = buildHookEnvironment(os.Environ(), e.context.ToEnv(), hook.Env, e.allowEnv, runtime.GOOS == "windows")
	if err != nil {
		result.Error = err
		return result
	}

	// Retain a bounded prefix of each stream and continue draining the rest.
	var stdout, stderr cappedHookOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Own the subprocess group/job for the lifetime of this synchronous hook.
	err = runHookCommand(ctx, cmd)
	result.Duration = time.Since(start)
	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated

	// The immediate shell can exit successfully while descendants hold its
	// pipes open. A nil Wait error must not turn an expired deadline into success.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = errors.Join(fmt.Errorf("timeout after %v: %w", timeout, ctx.Err()), err)
	}
	result.Error = err
	result.Success = err == nil

	return result
}

// sensitiveEnvMarkers are substrings that, when present in an (upper-cased)
// environment variable name, mark it as credential-bearing. Matching variables
// are withheld unless the invoking user explicitly grants the source variable.
var sensitiveEnvMarkers = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
	"API_KEY",
	"APIKEY",
	"ACCESS_KEY",
	"PRIVATE_KEY",
}

// sensitiveEnvExact are exact environment variable names that are stripped from
// hook subprocess environments in addition to marker matches.
var sensitiveEnvExact = map[string]bool{
	"SSH_AUTH_SOCK": true, // forwarding the SSH agent grants signing/auth capability
}

// isSensitiveEnvKey reports whether an environment variable name looks like it
// carries a credential and should be withheld from project-controlled hooks.
func isSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if sensitiveEnvExact[upper] {
		return true
	}
	for _, marker := range sensitiveEnvMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// Results returns all hook execution results
func (e *Executor) Results() []HookResult {
	results := append([]HookResult(nil), e.results...)
	for i := range results {
		results[i].Hook.Env = maps.Clone(results[i].Hook.Env)
	}
	return results
}

// Summary returns a human-readable summary of hook execution
func (e *Executor) Summary() string {
	if len(e.results) == 0 {
		return "No hooks executed"
	}

	var sb strings.Builder
	var succeeded, failed int

	for _, r := range e.results {
		if r.Success {
			succeeded++
			sb.WriteString(fmt.Sprintf("  [OK] %s (%v)\n", r.Hook.Name, r.Duration.Round(time.Millisecond)))
		} else {
			failed++
			sb.WriteString(fmt.Sprintf("  [FAIL] %s: %v\n", r.Hook.Name, r.Error))
			if r.Stderr != "" {
				sb.WriteString(fmt.Sprintf("         stderr: %s\n", truncate(r.Stderr, 200)))
			}
		}
		if r.StdoutTruncated {
			sb.WriteString(fmt.Sprintf("         stdout truncated after %d bytes\n", HookOutputLimit))
		}
		if r.StderrTruncated {
			sb.WriteString(fmt.Sprintf("         stderr truncated after %d bytes\n", HookOutputLimit))
		}
	}

	header := fmt.Sprintf("Hook execution: %d succeeded, %d failed\n", succeeded, failed)
	return header + sb.String()
}

// truncate shortens a string to max length with ellipsis
func truncate(s string, max int) string {
	if max < 0 {
		max = 0
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// RunHooks is a convenience function that runs all hooks for an export operation
func RunHooks(projectDir string, ctx ExportContext, noHooks bool, options ...ExecutionOptions) (*Executor, error) {
	if noHooks {
		return nil, nil
	}

	loader := NewLoader(WithProjectDir(projectDir))
	if err := loader.Load(); err != nil {
		return nil, fmt.Errorf("loading hooks: %w", err)
	}

	if !loader.HasHooks() {
		return nil, nil
	}

	executor := NewExecutor(loader.Config(), ctx, options...)
	if executor.authorizationErr != nil {
		return nil, executor.authorizationErr
	}
	return executor, nil
}
