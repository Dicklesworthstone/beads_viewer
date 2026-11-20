// Package cass integrates the Coding Agent Session Search CLI.
package cass

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Status represents the availability state of cass.
type Status int

const (
	// StatusUnknown indicates detection hasn't been performed yet.
	StatusUnknown Status = iota
	// StatusNotInstalled indicates cass binary is not found in PATH or is not executable.
	StatusNotInstalled
	// StatusNeedsIndex indicates archive health needs attention. Existing search
	// data may still be usable while stale or rebuilding.
	StatusNeedsIndex
	// StatusHealthy indicates cass is installed, indexed, and ready for searches.
	StatusHealthy
)

// String returns a human-readable status description.
func (s Status) String() string {
	switch s {
	case StatusUnknown:
		return "unknown"
	case StatusNotInstalled:
		return "not installed"
	case StatusNeedsIndex:
		return "needs indexing"
	case StatusHealthy:
		return "healthy"
	default:
		return "unknown"
	}
}

// DefaultCacheTTL is the default duration to cache detection results.
const DefaultCacheTTL = 5 * time.Minute

// DefaultHealthTimeout is the default timeout for health check commands.
const DefaultHealthTimeout = 2 * time.Second

// Detector checks if cass is installed and operational.
// It caches results and is safe for concurrent use.
type Detector struct {
	status        Status
	checkedAt     time.Time
	cacheTTL      time.Duration
	healthTimeout time.Duration
	mu            sync.RWMutex
	checkMu       sync.Mutex

	// For testing: allow overriding command execution
	lookPath   func(string) (string, error)
	runCommand func(ctx context.Context, name string, args ...string) (int, error)
}

// NewDetector creates a new Detector with default settings.
func NewDetector() *Detector {
	return &Detector{
		status:        StatusUnknown,
		cacheTTL:      DefaultCacheTTL,
		healthTimeout: DefaultHealthTimeout,
		lookPath:      exec.LookPath,
		runCommand:    defaultRunCommand,
	}
}

// Option configures a Detector.
type Option func(*Detector)

// WithCacheTTL sets the cache time-to-live.
func WithCacheTTL(ttl time.Duration) Option {
	return func(d *Detector) {
		d.cacheTTL = ttl
	}
}

// WithHealthTimeout sets the health check timeout.
func WithHealthTimeout(timeout time.Duration) Option {
	return func(d *Detector) {
		d.healthTimeout = timeout
	}
}

// NewDetectorWithOptions creates a new Detector with custom options.
func NewDetectorWithOptions(opts ...Option) *Detector {
	d := NewDetector()
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Status returns the current cached status.
// If the cache is stale or unknown, returns StatusUnknown.
// Use Check() to perform an actual detection.
func (d *Detector) Status() Status {
	status, ok := d.cachedStatus(time.Now())
	if !ok {
		return StatusUnknown
	}
	return status
}

// IsHealthy returns true if cass is ready to use.
// This is a convenience method that checks the cached status.
func (d *Detector) IsHealthy() bool {
	return d.Status() == StatusHealthy
}

// Check performs detection and returns the current status.
// Results are cached for cacheTTL duration.
// This method is safe for concurrent use.
func (d *Detector) Check() Status {
	if status, ok := d.cachedStatus(time.Now()); ok {
		return status
	}

	// Only one goroutine should run cass health at a time, but status readers
	// must stay independent so UI and search paths do not block behind it.
	d.checkMu.Lock()
	defer d.checkMu.Unlock()

	if status, ok := d.cachedStatus(time.Now()); ok {
		return status
	}

	status := d.detect()

	d.mu.Lock()
	d.status = status
	d.checkedAt = time.Now()
	d.mu.Unlock()

	return status
}

// Invalidate clears the cached status, forcing a fresh check on next Check() call.
func (d *Detector) Invalidate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.status = StatusUnknown
	d.checkedAt = time.Time{}
}

// detect performs the actual detection logic.
func (d *Detector) detect() Status {
	d.mu.RLock()
	lookPath := d.lookPath
	runCommand := d.runCommand
	healthTimeout := d.healthTimeout
	d.mu.RUnlock()

	// Step 1: Check if cass binary exists in PATH
	_, err := lookPath("cass")
	if err != nil {
		return StatusNotInstalled
	}

	// Step 2: Run health check with timeout
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()

	exitCode, err := runCommand(ctx, "cass", "health")
	if err != nil {
		// Command failed to run (timeout, not found, etc.)
		return StatusNotInstalled
	}

	// Interpret exit code: binary is confirmed present on PATH via lookPath,
	// so any non-zero health exit indicates unhealthy/unindexed cass, never "not installed".
	switch exitCode {
	case 0:
		return StatusHealthy
	default:
		return StatusNeedsIndex
	}
}

// defaultRunCommand executes a command and returns its exit code.
func defaultRunCommand(ctx context.Context, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// CheckedAt returns when the last check was performed.
// Returns zero time if never checked.
func (d *Detector) CheckedAt() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.checkedAt
}

// CacheValid returns true if the cached result is still valid.
func (d *Detector) CacheValid() bool {
	_, ok := d.cachedStatus(time.Now())
	return ok
}

func (d *Detector) cachedStatus(now time.Time) (Status, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.status == StatusUnknown {
		return StatusUnknown, false
	}
	if now.Sub(d.checkedAt) > d.cacheTTL {
		return StatusUnknown, false
	}
	return d.status, true
}
