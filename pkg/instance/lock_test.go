package instance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestNewLock_FirstInstance(t *testing.T) {
	tmpDir := t.TempDir()

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	defer lock.Release()

	if !lock.IsFirstInstance() {
		t.Error("expected first instance, got secondary")
	}

	// Verify lock file exists
	lockPath := filepath.Join(tmpDir, LockFileName)
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file was not created")
	}

	// Verify lock file contains valid JSON with our PID
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}

	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("parsing lock file: %v", err)
	}

	if info.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
	if info.OwnerID == "" {
		t.Error("expected lock file to include owner ID")
	}
}

func TestNewLock_SecondInstance(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first lock
	lock1, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("first NewLock failed: %v", err)
	}
	defer lock1.Release()

	if !lock1.IsFirstInstance() {
		t.Error("lock1 should be first instance")
	}

	// Try to create second lock
	lock2, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("second NewLock failed: %v", err)
	}
	defer lock2.Release()

	if lock2.IsFirstInstance() {
		t.Error("lock2 should NOT be first instance")
	}

	// Second instance should know the PID of the holder
	if lock2.HolderPID() != os.Getpid() {
		t.Errorf("expected holder PID %d, got %d", os.Getpid(), lock2.HolderPID())
	}
}

func TestNewLock_StaleLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a lock file with a fake (non-existent) PID
	lockPath := filepath.Join(tmpDir, LockFileName)
	info := LockInfo{
		PID:       99999999, // Highly unlikely to be a real PID
		StartedAt: time.Now().Add(-time.Hour),
		Hostname:  "fake-host",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshaling fake lock: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("writing fake lock: %v", err)
	}

	// NewLock should detect stale lock and take over
	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	defer lock.Release()

	// Should have taken over the stale lock
	if !lock.IsFirstInstance() {
		t.Error("should have taken over stale lock and become first instance")
	}

	if lock.HolderPID() != os.Getpid() {
		t.Errorf("expected our PID %d after takeover, got %d", os.Getpid(), lock.HolderPID())
	}
}

func TestLock_Release(t *testing.T) {
	tmpDir := t.TempDir()

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}

	lockPath := filepath.Join(tmpDir, LockFileName)

	// Verify lock file exists
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file should exist before release")
	}

	// Release the lock
	lock.Release()

	// Verify lock file is removed
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after release")
	}

	// Should be safe to release again
	lock.Release()
}

func TestLock_ReleaseDoesNotRemoveReplacedLock(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, LockFileName)

	staleInfo := LockInfo{
		PID:       99999999,
		StartedAt: time.Now().Add(-time.Hour),
		Hostname:  "stale-host",
	}
	writeLockInfoForTest(t, lockPath, staleInfo)

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	if !lock.IsFirstInstance() {
		t.Fatal("expected stale lock takeover to become first instance")
	}

	replacement := LockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		Hostname:  "replacement-host",
		OwnerID:   "replacement-owner",
	}
	writeLockInfoForTest(t, lockPath, replacement)

	lock.Release()

	got, err := readLockFile(lockPath)
	if err != nil {
		t.Fatalf("replacement lock should remain after releasing stale takeover: %v", err)
	}
	if got.OwnerID != replacement.OwnerID {
		t.Fatalf("expected replacement owner ID %q, got %q", replacement.OwnerID, got.OwnerID)
	}
}

func TestLock_ReleaseAllowsNewLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Create and release first lock
	lock1, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("first NewLock failed: %v", err)
	}
	lock1.Release()

	// Should be able to create new lock
	lock2, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("second NewLock failed: %v", err)
	}
	defer lock2.Release()

	if !lock2.IsFirstInstance() {
		t.Error("lock2 should be first instance after lock1 released")
	}
}

func TestLock_Path(t *testing.T) {
	tmpDir := t.TempDir()

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	defer lock.Release()

	expected := filepath.Join(tmpDir, LockFileName)
	if lock.Path() != expected {
		t.Errorf("expected path %q, got %q", expected, lock.Path())
	}
}

func TestIsProcessAlive(t *testing.T) {
	// Our own process should be alive
	if !isProcessAlive(os.Getpid()) {
		t.Error("our own process should be reported as alive")
	}

	// PID 1 (init/systemd) should be alive on most systems
	// Skip this check as it may not work in all containers
	// if !isProcessAlive(1) {
	// 	t.Error("PID 1 should be alive")
	// }

	// Non-existent PID should not be alive
	if isProcessAlive(99999999) {
		t.Error("fake PID should not be alive")
	}

	// Invalid PIDs
	if isProcessAlive(0) {
		t.Error("PID 0 should not be alive")
	}
	if isProcessAlive(-1) {
		t.Error("negative PID should not be alive")
	}
}

func TestLockInfo_JSON(t *testing.T) {
	info := LockInfo{
		PID:       12345,
		StartedAt: time.Now().Truncate(time.Second),
		Hostname:  "test-host",
		OwnerID:   "test-owner",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed LockInfo
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.PID != info.PID {
		t.Errorf("PID mismatch: expected %d, got %d", info.PID, parsed.PID)
	}
	if !parsed.StartedAt.Equal(info.StartedAt) {
		t.Errorf("StartedAt mismatch: expected %v, got %v", info.StartedAt, parsed.StartedAt)
	}
	if parsed.Hostname != info.Hostname {
		t.Errorf("Hostname mismatch: expected %q, got %q", info.Hostname, parsed.Hostname)
	}
	if parsed.OwnerID != info.OwnerID {
		t.Errorf("OwnerID mismatch: expected %q, got %q", info.OwnerID, parsed.OwnerID)
	}
}

func TestNewLock_CorruptedLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a corrupted lock file
	lockPath := filepath.Join(tmpDir, LockFileName)
	if err := os.WriteFile(lockPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("writing corrupted lock: %v", err)
	}

	// Should handle corrupted lock gracefully
	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock should not fail on corrupted lock: %v", err)
	}
	defer lock.Release()

	if lock.IsFirstInstance() {
		t.Fatal("recent corrupted lock should not be taken over")
	}
}

func TestNewLock_StaleCorruptedLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	lockPath := filepath.Join(tmpDir, LockFileName)
	if err := os.WriteFile(lockPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("writing corrupted lock: %v", err)
	}
	staleTime := time.Now().Add(-unreadableLockTakeoverAge - time.Second)
	if err := os.Chtimes(lockPath, staleTime, staleTime); err != nil {
		t.Fatalf("aging corrupted lock: %v", err)
	}

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock should not fail on stale corrupted lock: %v", err)
	}
	defer lock.Release()

	if !lock.IsFirstInstance() {
		t.Fatal("stale corrupted lock should be taken over")
	}
	if lock.HolderPID() != os.Getpid() {
		t.Fatalf("expected holder PID %d after takeover, got %d", os.Getpid(), lock.HolderPID())
	}
}

func TestNewLock_EmptyLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create an empty lock file
	lockPath := filepath.Join(tmpDir, LockFileName)
	if err := os.WriteFile(lockPath, []byte{}, 0644); err != nil {
		t.Fatalf("writing empty lock: %v", err)
	}

	// Should handle empty lock gracefully
	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock should not fail on empty lock: %v", err)
	}
	defer lock.Release()

	if lock.IsFirstInstance() {
		t.Fatal("recent empty lock should not be taken over")
	}
}

func TestNewLock_StaleEmptyLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	lockPath := filepath.Join(tmpDir, LockFileName)
	if err := os.WriteFile(lockPath, []byte{}, 0644); err != nil {
		t.Fatalf("writing empty lock: %v", err)
	}
	staleTime := time.Now().Add(-unreadableLockTakeoverAge - time.Second)
	if err := os.Chtimes(lockPath, staleTime, staleTime); err != nil {
		t.Fatalf("aging empty lock: %v", err)
	}

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock should not fail on stale empty lock: %v", err)
	}
	defer lock.Release()

	if !lock.IsFirstInstance() {
		t.Fatal("stale empty lock should be taken over")
	}
	if lock.HolderPID() != os.Getpid() {
		t.Fatalf("expected holder PID %d after takeover, got %d", os.Getpid(), lock.HolderPID())
	}
}

func TestNewLock_ConcurrentStaleTakeover(t *testing.T) {
	// Test that concurrent attempts to take over a stale lock don't both succeed
	// (verifies the atomic rename + verify fix for the TOCTOU race condition)
	tmpDir := t.TempDir()

	// Create a stale lock file with a fake PID
	staleLockPath := filepath.Join(tmpDir, LockFileName)
	info := LockInfo{
		PID:       99999999,
		StartedAt: time.Now().Add(-time.Hour),
		Hostname:  "stale-host",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshaling stale lock: %v", err)
	}
	if err := os.WriteFile(staleLockPath, data, 0644); err != nil {
		t.Fatalf("writing stale lock: %v", err)
	}

	// Simulate concurrent takeover attempts using goroutines
	// Each goroutine creates a Lock and tries to take over
	const numGoroutines = 10
	type result struct {
		lock    *Lock
		isFirst bool
	}
	results := make(chan result, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(_ int) {
			lock, err := NewLock(tmpDir)
			if err != nil {
				results <- result{nil, false}
				return
			}
			// Don't release here - we need to keep locks held until all goroutines finish
			results <- result{lock, lock.IsFirstInstance()}
		}(i)
	}

	// Collect all results and count first instances
	locks := make([]*Lock, 0, numGoroutines)
	firstCount := 0
	for i := 0; i < numGoroutines; i++ {
		r := <-results
		if r.lock != nil {
			locks = append(locks, r.lock)
		}
		if r.isFirst {
			firstCount++
		}
	}

	// Release all locks after counting
	for _, lock := range locks {
		lock.Release()
	}

	// Exactly one goroutine should claim to be first instance
	if firstCount != 1 {
		t.Errorf("Expected exactly 1 goroutine to be first instance, got %d", firstCount)
	}
}

func writeLockInfoForTest(t *testing.T, path string, info LockInfo) {
	t.Helper()

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshaling lock info: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writing lock info: %v", err)
	}
}

func TestClassifySignalZeroErr_EPERMIsAlive(t *testing.T) {
	if !classifySignalZeroErr(nil) {
		t.Fatal("nil error must classify as alive")
	}
	if !classifySignalZeroErr(syscall.EPERM) {
		t.Fatal("EPERM must classify as alive (process owned by different UID)")
	}
	if !classifySignalZeroErr(fmt.Errorf("wrapped: %w", syscall.EPERM)) {
		t.Fatal("wrapped EPERM must classify as alive")
	}
	if classifySignalZeroErr(syscall.ESRCH) {
		t.Fatal("ESRCH must classify as dead")
	}
	if classifySignalZeroErr(os.ErrProcessDone) {
		t.Fatal("ErrProcessDone must classify as dead")
	}
}

func TestNewLock_EPERMSameHostLockNotStolen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal(0) EPERM semantics apply to non-Windows hosts")
	}

	const localHost = "local-workstation"
	const otherUIDPID = 424242

	lockHooksMu.Lock()
	origHost := hostnameFunc
	origSignal := signalZeroOverride
	hostnameFunc = func() (string, error) { return localHost, nil }
	signalZeroOverride = func(pid int) error {
		if pid == otherUIDPID {
			return syscall.EPERM
		}
		return syscall.ESRCH
	}
	lockHooksMu.Unlock()
	t.Cleanup(func() {
		lockHooksMu.Lock()
		hostnameFunc = origHost
		signalZeroOverride = origSignal
		lockHooksMu.Unlock()
	})

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, LockFileName)
	initial := LockInfo{
		PID:       otherUIDPID,
		StartedAt: time.Now().Add(-2 * time.Hour),
		Hostname:  localHost,
		OwnerID:   "other-uid-owner",
	}
	writeLockInfoForTest(t, lockPath, initial)

	lock, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock failed: %v", err)
	}
	defer lock.Release()

	if lock.IsFirstInstance() {
		t.Fatal("EPERM PID on same host must be treated as running; lock must not be stolen")
	}
	if lock.HolderPID() != otherUIDPID {
		t.Fatalf("HolderPID() = %d, want %d", lock.HolderPID(), otherUIDPID)
	}

	got, err := readLockFile(lockPath)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	if got.OwnerID != initial.OwnerID {
		t.Fatalf("lock file owner mutated: got %q, want %q", got.OwnerID, initial.OwnerID)
	}
}

func TestNewLock_ForeignHostnameLeaseGuard(t *testing.T) {
	const (
		localHost   = "node-alpha"
		foreignHost = "node-beta"
		foreignPID  = 99999999
		testTTL     = 5 * time.Minute
	)

	var localPIDProbes atomic.Int64

	lockHooksMu.Lock()
	origHost := hostnameFunc
	origTTL := foreignHostLeaseTTL
	origAlive := processAliveFunc
	hostnameFunc = func() (string, error) { return localHost, nil }
	foreignHostLeaseTTL = testTTL
	processAliveFunc = func(pid int) bool {
		localPIDProbes.Add(1)
		return isProcessAlive(pid)
	}
	lockHooksMu.Unlock()
	t.Cleanup(func() {
		lockHooksMu.Lock()
		hostnameFunc = origHost
		foreignHostLeaseTTL = origTTL
		processAliveFunc = origAlive
		lockHooksMu.Unlock()
	})

	t.Run("active foreign lease is preserved without probing local PID table", func(t *testing.T) {
		localPIDProbes.Store(0)
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, LockFileName)
		activeForeign := LockInfo{
			PID:       foreignPID,
			StartedAt: time.Now().Add(-1 * time.Minute),
			Hostname:  foreignHost,
			OwnerID:   "foreign-active-owner",
		}
		writeLockInfoForTest(t, lockPath, activeForeign)

		lock, err := NewLock(tmpDir)
		if err != nil {
			t.Fatalf("NewLock failed: %v", err)
		}
		defer lock.Release()

		if lock.IsFirstInstance() {
			t.Fatal("active foreign-host lock must not be taken over")
		}
		if lock.HolderPID() != foreignPID {
			t.Fatalf("HolderPID() = %d, want %d", lock.HolderPID(), foreignPID)
		}
		if probes := localPIDProbes.Load(); probes != 0 {
			t.Fatalf("foreign-host lock probed local PID table %d times, want 0", probes)
		}

		got, err := readLockFile(lockPath)
		if err != nil {
			t.Fatalf("reading lock file: %v", err)
		}
		if got.OwnerID != activeForeign.OwnerID {
			t.Fatalf("active foreign lock was overwritten: got owner %q, want %q", got.OwnerID, activeForeign.OwnerID)
		}
	})

	t.Run("expired foreign lease is recovered after TTL without probing local PID table", func(t *testing.T) {
		selfPID := os.Getpid()
		localPIDProbes.Store(0)
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, LockFileName)
		expiredForeign := LockInfo{
			PID:       foreignPID,
			StartedAt: time.Now().Add(-2 * testTTL),
			Hostname:  foreignHost,
			OwnerID:   "foreign-expired-lease",
		}
		writeLockInfoForTest(t, lockPath, expiredForeign)

		lock, err := NewLock(tmpDir)
		if err != nil {
			t.Fatalf("NewLock failed: %v", err)
		}
		defer lock.Release()

		if !lock.IsFirstInstance() {
			t.Fatal("expired foreign-host lock should be recovered")
		}
		if lock.HolderPID() != selfPID {
			t.Fatalf("HolderPID() = %d, want %d", lock.HolderPID(), selfPID)
		}
		if probes := localPIDProbes.Load(); probes != 0 {
			t.Fatalf("foreign-host lock probed local PID table %d times, want 0", probes)
		}

		got, err := readLockFile(lockPath)
		if err != nil {
			t.Fatalf("reading lock file after takeover: %v", err)
		}
		if got.Hostname != localHost || got.PID != selfPID {
			t.Fatalf("unexpected takeover lock info: %+v", got)
		}
	})
}

func TestLock_ConcurrentMethodsRace(t *testing.T) {
	tmpDir := t.TempDir()

	primary, err := NewLock(tmpDir)
	if err != nil {
		t.Fatalf("NewLock(primary) failed: %v", err)
	}

	secondary, err := NewLock(tmpDir)
	if err != nil {
		primary.Release()
		t.Fatalf("NewLock(secondary) failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = primary.IsFirstInstance()
				_ = primary.HolderPID()
				_ = primary.Path()
				_ = secondary.IsFirstInstance()
				_ = secondary.HolderPID()
				_ = secondary.Path()
				secondary.checkStale()
				if id%4 == 0 && j == 12 {
					primary.Release()
				}
			}
		}(i)
	}
	wg.Wait()

	primary.Release()
	secondary.Release()
}
