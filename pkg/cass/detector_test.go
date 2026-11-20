package cass

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatus_String(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusUnknown, "unknown"},
		{StatusNotInstalled, "not installed"},
		{StatusNeedsIndex, "needs indexing"},
		{StatusHealthy, "healthy"},
		{Status(99), "unknown"}, // Invalid status
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("Status.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewDetector(t *testing.T) {
	d := NewDetector()

	if d.status != StatusUnknown {
		t.Errorf("initial status = %v, want StatusUnknown", d.status)
	}
	if d.cacheTTL != DefaultCacheTTL {
		t.Errorf("cacheTTL = %v, want %v", d.cacheTTL, DefaultCacheTTL)
	}
	if d.healthTimeout != DefaultHealthTimeout {
		t.Errorf("healthTimeout = %v, want %v", d.healthTimeout, DefaultHealthTimeout)
	}
}

func TestNewDetectorWithOptions(t *testing.T) {
	customTTL := 10 * time.Minute
	customTimeout := 5 * time.Second

	d := NewDetectorWithOptions(
		WithCacheTTL(customTTL),
		WithHealthTimeout(customTimeout),
	)

	if d.cacheTTL != customTTL {
		t.Errorf("cacheTTL = %v, want %v", d.cacheTTL, customTTL)
	}
	if d.healthTimeout != customTimeout {
		t.Errorf("healthTimeout = %v, want %v", d.healthTimeout, customTimeout)
	}
}

func TestDetector_Check_NotInPath(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "", errors.New("not found")
	}

	status := d.Check()
	if status != StatusNotInstalled {
		t.Errorf("Check() = %v, want StatusNotInstalled", status)
	}
}

func TestDetector_Check_HealthyExitZero(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		if name == "cass" && len(args) > 0 && args[0] == "health" {
			return 0, nil
		}
		return -1, errors.New("unexpected command")
	}

	status := d.Check()
	if status != StatusHealthy {
		t.Errorf("Check() = %v, want StatusHealthy", status)
	}
}

func TestDetector_Check_NeedsIndexExitOne(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 1, nil // Exit code 1 = needs indexing
	}

	status := d.Check()
	if status != StatusNeedsIndex {
		t.Errorf("Check() = %v, want StatusNeedsIndex", status)
	}
}

func TestDetector_Check_IndexCorruptExitThree(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 3, nil // Exit code 3 = index missing/corrupt
	}

	status := d.Check()
	if status != StatusNeedsIndex {
		t.Errorf("Check() = %v, want StatusNeedsIndex", status)
	}
}

func TestDetector_Check_UnknownExitCode(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 99, nil // Unknown exit code
	}

	status := d.Check()
	if status != StatusNeedsIndex {
		t.Errorf("Check() = %v, want StatusNeedsIndex when cass binary is on PATH", status)
	}
}

func TestDetector_Check_CommandError(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return -1, errors.New("command failed")
	}

	status := d.Check()
	if status != StatusNotInstalled {
		t.Errorf("Check() = %v, want StatusNotInstalled", status)
	}
}

func TestDetector_Caching(t *testing.T) {
	checkCount := 0
	d := NewDetectorWithOptions(WithCacheTTL(100 * time.Millisecond))
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		checkCount++
		return 0, nil
	}

	// First check
	status := d.Check()
	if status != StatusHealthy {
		t.Errorf("Check() = %v, want StatusHealthy", status)
	}
	if checkCount != 1 {
		t.Errorf("checkCount = %d, want 1", checkCount)
	}

	// Second check - should use cache
	status = d.Check()
	if status != StatusHealthy {
		t.Errorf("Check() = %v, want StatusHealthy", status)
	}
	if checkCount != 1 {
		t.Errorf("checkCount = %d, want 1 (cached)", checkCount)
	}

	// Wait for cache to expire
	time.Sleep(150 * time.Millisecond)

	// Third check - should re-check
	status = d.Check()
	if status != StatusHealthy {
		t.Errorf("Check() = %v, want StatusHealthy", status)
	}
	if checkCount != 2 {
		t.Errorf("checkCount = %d, want 2 (cache expired)", checkCount)
	}
}

func TestDetector_Status_ReturnsUnknownWhenStale(t *testing.T) {
	d := NewDetectorWithOptions(WithCacheTTL(50 * time.Millisecond))
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Initial status should be unknown
	if d.Status() != StatusUnknown {
		t.Errorf("Status() = %v, want StatusUnknown", d.Status())
	}

	// After check, should be healthy
	d.Check()
	if d.Status() != StatusHealthy {
		t.Errorf("Status() = %v, want StatusHealthy", d.Status())
	}

	// Wait for cache to expire
	time.Sleep(60 * time.Millisecond)

	// Should return unknown when stale
	if d.Status() != StatusUnknown {
		t.Errorf("Status() = %v, want StatusUnknown (stale)", d.Status())
	}
}

func TestDetector_IsHealthy(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Before check
	if d.IsHealthy() {
		t.Error("IsHealthy() = true before Check(), want false")
	}

	d.Check()

	// After check
	if !d.IsHealthy() {
		t.Error("IsHealthy() = false after Check(), want true")
	}
}

func TestDetector_Invalidate(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	d.Check()
	if d.Status() != StatusHealthy {
		t.Errorf("Status() = %v, want StatusHealthy", d.Status())
	}

	d.Invalidate()

	if d.Status() != StatusUnknown {
		t.Errorf("Status() after Invalidate() = %v, want StatusUnknown", d.Status())
	}
	if !d.CheckedAt().IsZero() {
		t.Error("CheckedAt() after Invalidate() should be zero time")
	}
}

func TestDetector_ConcurrentAccess(t *testing.T) {
	var checkCount int32
	var failureCount int32 // Track failures atomically instead of using t.Errorf in goroutines
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		atomic.AddInt32(&checkCount, 1)
		time.Sleep(10 * time.Millisecond) // Simulate some work
		return 0, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := d.Check()
			if status != StatusHealthy {
				atomic.AddInt32(&failureCount, 1)
			}
		}()
	}
	wg.Wait()

	// Check for failures after all goroutines complete (safe to use t.Errorf now)
	if failures := atomic.LoadInt32(&failureCount); failures > 0 {
		t.Errorf("%d goroutines got non-healthy status, want all StatusHealthy", failures)
	}

	// checkMu should collapse concurrent cache misses into a single health probe.
	if checkCount != 1 {
		t.Errorf("checkCount = %d, want 1", checkCount)
	}
}

func TestDetector_Status_DoesNotBlockDuringSlowCheck(t *testing.T) {
	d := NewDetectorWithOptions(WithHealthTimeout(5 * time.Second))
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}

	healthStarted := make(chan struct{})
	releaseHealth := make(chan struct{})
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		close(healthStarted)
		select {
		case <-releaseHealth:
			return 0, nil
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}

	done := make(chan Status, 1)
	go func() {
		done <- d.Check()
	}()

	select {
	case <-healthStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cass health check did not start")
	}

	start := time.Now()
	status := d.Status()
	elapsed := time.Since(start)

	if status != StatusUnknown {
		t.Errorf("Status() during slow Check() = %v, want StatusUnknown", status)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("Status() blocked for %v during slow Check(), want <= 10ms", elapsed)
	}

	close(releaseHealth)

	select {
	case status := <-done:
		if status != StatusHealthy {
			t.Errorf("Check() = %v, want StatusHealthy", status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Check() did not finish after health command was released")
	}
}

func TestDetector_CheckedAt(t *testing.T) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Before check
	if !d.CheckedAt().IsZero() {
		t.Error("CheckedAt() before Check() should be zero")
	}

	before := time.Now()
	d.Check()
	after := time.Now()

	checkedAt := d.CheckedAt()
	if checkedAt.Before(before) || checkedAt.After(after) {
		t.Errorf("CheckedAt() = %v, want between %v and %v", checkedAt, before, after)
	}
}

func TestDetector_CacheValid(t *testing.T) {
	d := NewDetectorWithOptions(WithCacheTTL(50 * time.Millisecond))
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Before check
	if d.CacheValid() {
		t.Error("CacheValid() before Check() = true, want false")
	}

	d.Check()

	// After check
	if !d.CacheValid() {
		t.Error("CacheValid() after Check() = false, want true")
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	if d.CacheValid() {
		t.Error("CacheValid() after expiry = true, want false")
	}
}

func TestDetector_Check_Timeout(t *testing.T) {
	d := NewDetectorWithOptions(WithHealthTimeout(50 * time.Millisecond))
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		// Simulate a hanging command
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return 0, nil
		}
	}

	start := time.Now()
	status := d.Check()
	elapsed := time.Since(start)

	// Should timeout and return NotInstalled
	if status != StatusNotInstalled {
		t.Errorf("Check() = %v, want StatusNotInstalled (timeout)", status)
	}

	// Should have timed out quickly
	if elapsed > 100*time.Millisecond {
		t.Errorf("Check() took %v, want < 100ms (should timeout)", elapsed)
	}
}

// BenchmarkDetector_Check_Cached benchmarks cached Check() calls.
func BenchmarkDetector_Check_Cached(b *testing.B) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Prime the cache
	d.Check()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Check()
	}
}

// BenchmarkDetector_Status benchmarks Status() calls.
func BenchmarkDetector_Status(b *testing.B) {
	d := NewDetector()
	d.lookPath = func(name string) (string, error) {
		return "/usr/local/bin/cass", nil
	}
	d.runCommand = func(ctx context.Context, name string, args ...string) (int, error) {
		return 0, nil
	}

	// Prime the cache
	d.Check()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Status()
	}
}

func TestDefaultRunCommand_WaitDelayBoundsBackgroundPipeHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX /bin/sh")
	}

	// Positive control: fast command returns exit code 0 cleanly.
	ctxFast, cancelFast := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelFast()
	code, err := defaultRunCommand(ctxFast, "/bin/sh", "-c", "exit 0")
	if err != nil || code != 0 {
		t.Fatalf("fast defaultRunCommand = (%d, %v), want (0, nil)", code, err)
	}

	// Planted negative / timeout control: backgrounded pipe child with 50ms context timeout
	// returns within WaitDelay (~1s, < 3s) instead of hanging 5s.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = defaultRunCommand(ctx, "/bin/sh", "-c", "sleep 5 & exec sleep 5")
	elapsed := time.Since(start)
	if err == nil && ctx.Err() == nil {
		t.Fatal("expected context timeout on backgrounded child")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("defaultRunCommand hung for %v despite WaitDelay=1s (expected < 3s)", elapsed)
	}
}
