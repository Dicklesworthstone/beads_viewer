// Package instance provides multi-instance coordination for bv.
// It detects when multiple bv instances are running on the same repository
// and provides mechanisms for coordination and user feedback.
package instance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// staleLockMu serializes stale lock takeover attempts within the same process.
// This prevents multiple goroutines from simultaneously claiming the same stale lock.
// Lock ordering: always acquire staleLockMu before Lock.mu.
var staleLockMu sync.Mutex

// DefaultForeignHostLeaseTTL bounds how long a lock created on a different
// hostname is treated as active before being considered stale and recoverable.
const DefaultForeignHostLeaseTTL = 30 * time.Minute

var (
	lockHooksMu         sync.RWMutex
	hostnameFunc        = os.Hostname
	foreignHostLeaseTTL = DefaultForeignHostLeaseTTL
	processAliveFunc    = isProcessAlive
	signalZeroOverride  func(pid int) error
)

// classifySignalZeroErr interprets the error from sending signal 0 to a PID.
// EPERM indicates the target process exists under a different UID/credentials.
func classifySignalZeroErr(err error) bool {
	return err == nil || errors.Is(err, syscall.EPERM)
}

func currentHostname() (string, error) {
	lockHooksMu.RLock()
	fn := hostnameFunc
	lockHooksMu.RUnlock()
	return fn()
}

func currentForeignHostLeaseTTL() time.Duration {
	lockHooksMu.RLock()
	ttl := foreignHostLeaseTTL
	lockHooksMu.RUnlock()
	if ttl <= 0 {
		return DefaultForeignHostLeaseTTL
	}
	return ttl
}

func checkProcessAlive(pid int) bool {
	lockHooksMu.RLock()
	fn := processAliveFunc
	lockHooksMu.RUnlock()
	return fn(pid)
}

// LockInfo contains metadata about the process holding the lock.
type LockInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Hostname  string    `json:"hostname,omitempty"`
	OwnerID   string    `json:"owner_id,omitempty"`
}

// Lock represents an instance lock for a beads directory.
// It uses a lock file to detect multiple concurrent instances.
type Lock struct {
	mu       sync.RWMutex
	path     string
	lockFile *os.File
	pid      int
	isFirst  bool
	ownerID  string
}

// LockFileName is the name of the lock file created in .beads directory.
const LockFileName = ".bv.lock"

const unreadableLockTakeoverAge = 2 * time.Second

// NewLock creates a new instance lock for the given beads directory.
// If this is the first instance, it creates and holds the lock.
// If another instance already holds the lock, it returns a Lock with isFirst=false.
func NewLock(beadsDir string) (*Lock, error) {
	lockPath := filepath.Join(beadsDir, LockFileName)

	// Try to create lock file with exclusive access
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			// Lock file exists - read it to see who owns it
			existing, readErr := readLockFile(lockPath)
			lock := &Lock{
				path:    lockPath,
				isFirst: false,
			}
			if readErr == nil {
				lock.pid = existing.PID
			}

			// Check if the existing lock is stale
			lock.checkStale()

			return lock, nil
		}
		return nil, fmt.Errorf("creating lock file: %w", err)
	}

	ownerID, err := newLockOwnerID()
	if err != nil {
		_ = file.Close()
		removeLockFileBestEffort(lockPath)
		return nil, fmt.Errorf("generating lock owner id: %w", err)
	}

	// We got the lock - write our info
	lock := &Lock{
		path:     lockPath,
		lockFile: file,
		isFirst:  true,
		pid:      os.Getpid(),
		ownerID:  ownerID,
	}
	if err := lock.writeLockInfo(); err != nil {
		_ = file.Close()
		removeLockFileBestEffort(lockPath)
		return nil, fmt.Errorf("writing lock info: %w", err)
	}

	return lock, nil
}

// writeLockInfo writes the current process info to the lock file.
// Caller must hold l.mu or invoke during single-owner initialization.
func (l *Lock) writeLockInfo() error {
	if l.lockFile == nil {
		return nil
	}
	if l.ownerID == "" {
		ownerID, err := newLockOwnerID()
		if err != nil {
			return fmt.Errorf("generating lock owner id: %w", err)
		}
		l.ownerID = ownerID
	}

	hostname, _ := currentHostname()
	info := LockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now(),
		Hostname:  hostname,
		OwnerID:   l.ownerID,
	}

	// Truncate and seek to beginning before writing
	if err := l.lockFile.Truncate(0); err != nil {
		return err
	}
	if _, err := l.lockFile.Seek(0, 0); err != nil {
		return err
	}

	return json.NewEncoder(l.lockFile).Encode(info)
}

// readLockFile reads lock info from an existing lock file.
func readLockFile(path string) (*LockInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	return &info, nil
}

// IsFirstInstance returns true if this is the first/primary instance.
func (l *Lock) IsFirstInstance() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isFirst
}

// HolderPID returns the PID of the process holding the lock.
// Returns 0 if unknown.
func (l *Lock) HolderPID() int {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.pid
}

// checkStale checks if the existing lock is stale (held by a dead process or
// an expired foreign-host lease) and takes it over if so. Uses atomic rename
// to avoid TOCTOU race conditions.
func (l *Lock) checkStale() {
	if l == nil {
		return
	}
	l.mu.RLock()
	if l.isFirst {
		l.mu.RUnlock()
		return
	}
	lockPath := l.path
	l.mu.RUnlock()

	// Serialize stale lock takeover attempts within the same process
	staleLockMu.Lock()
	defer staleLockMu.Unlock()

	l.mu.RLock()
	if l.isFirst {
		l.mu.RUnlock()
		return
	}
	l.mu.RUnlock()

	// Re-read the lock file to get current state (another goroutine may have taken over)
	existing, err := readLockFile(lockPath)
	if err != nil {
		// A recent unreadable file may be a process between O_EXCL create and
		// JSON write. Only recover older unreadable files that are likely left
		// behind by a crash or interrupted write.
		if !shouldTakeOverUnreadableLock(lockPath) {
			return
		}
		l.takeOverStaleLock(lockPath)
		return
	}

	if !isExistingLockStale(lockPath, existing) {
		// Lock is held by a live local process or active foreign-host lease -
		// update our record and don't take over.
		l.mu.Lock()
		if !l.isFirst {
			l.pid = existing.PID
		}
		l.mu.Unlock()
		return
	}

	l.takeOverStaleLock(lockPath)
}

func isExistingLockStale(lockPath string, existing *LockInfo) bool {
	if existing == nil {
		return true
	}

	// When the lock records a non-empty Hostname that differs from our current
	// host, the recorded PID belongs to a foreign PID namespace/machine. Do NOT
	// probe our local PID table; instead enforce a bounded lease TTL on StartedAt.
	if existing.Hostname != "" {
		localHost, hostErr := currentHostname()
		if hostErr != nil || localHost == "" || existing.Hostname != localHost {
			ttl := currentForeignHostLeaseTTL()
			if !existing.StartedAt.IsZero() {
				return time.Since(existing.StartedAt) > ttl
			}
			st, statErr := os.Stat(lockPath)
			if statErr != nil {
				return false
			}
			return time.Since(st.ModTime()) > ttl
		}
	}

	return !checkProcessAlive(existing.PID)
}

func shouldTakeOverUnreadableLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) >= unreadableLockTakeoverAge
}

func (l *Lock) takeOverStaleLock(lockPath string) {
	// Attempt atomic takeover using rename.
	// This avoids the race condition of delete + create with O_EXCL.
	ownerID, err := newLockOwnerID()
	if err != nil {
		return
	}
	ownerPID := os.Getpid()
	tmpPath := fmt.Sprintf("%s.%d.%s.tmp", lockPath, ownerPID, ownerID[:8])

	// Create temp file exclusively with our lock info
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0644)
	if err != nil {
		return
	}

	hostname, _ := currentHostname()
	info := LockInfo{
		PID:       ownerPID,
		StartedAt: time.Now(),
		Hostname:  hostname,
		OwnerID:   ownerID,
	}

	if err := json.NewEncoder(file).Encode(info); err != nil {
		_ = file.Close()
		removeLockFileBestEffort(tmpPath)
		return
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		removeLockFileBestEffort(tmpPath)
		return
	}
	if err := file.Close(); err != nil {
		removeLockFileBestEffort(tmpPath)
		return
	}

	// Atomic rename to take over the lock
	// On most filesystems, rename is atomic and will overwrite the existing file.
	// Windows does not allow rename over an existing file, so fall back to remove + rename.
	if err := os.Rename(tmpPath, lockPath); err != nil {
		if runtime.GOOS == "windows" {
			if rmErr := os.Remove(lockPath); rmErr == nil {
				if err2 := os.Rename(tmpPath, lockPath); err2 == nil {
					// Success after Windows-safe fallback.
					goto verify
				}
			}
		}
		removeLockFileBestEffort(tmpPath)
		return
	}

verify:
	// Verify we won the race by re-reading and checking our owner ID.
	// This handles the case where two processes both rename simultaneously
	verifyInfo, err := readLockFile(lockPath)
	if err != nil || verifyInfo.PID != ownerPID || verifyInfo.OwnerID != ownerID {
		// Lost the race to another process - don't claim ownership
		return
	}

	// Successfully took over the stale lock.
	// The lock is enforced by file existence + content, so we don't need to keep
	// the file open after takeover (it will be closed by the OS on exit anyway).
	l.mu.Lock()
	l.isFirst = true
	l.pid = ownerPID
	l.ownerID = ownerID
	l.mu.Unlock()
}

// isProcessAlive is implemented in platform-specific files:
// - lock_unix.go for Unix/Linux/macOS (uses signal 0)
// - lock_windows.go for Windows (uses OpenProcess API)

// Release releases the lock file and cleans up.
// This should be called when the application exits.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lockFile != nil {
		_ = l.lockFile.Close()
		l.lockFile = nil
	}

	if l.isFirst && l.ownsCurrentLockFileLocked() {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			l.isFirst = false
			return
		}
	}

	l.isFirst = false
}

func (l *Lock) ownsCurrentLockFileLocked() bool {
	info, err := readLockFile(l.path)
	if err != nil {
		return false
	}
	if info.PID != l.pid {
		return false
	}
	if l.ownerID != "" {
		return info.OwnerID == l.ownerID
	}
	return true
}

func newLockOwnerID() (string, error) {
	var ownerID [16]byte
	if _, err := rand.Read(ownerID[:]); err != nil {
		return "", fmt.Errorf("reading random lock owner id: %w", err)
	}
	return hex.EncodeToString(ownerID[:]), nil
}

func removeLockFileBestEffort(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}

// Path returns the path to the lock file.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.path
}
