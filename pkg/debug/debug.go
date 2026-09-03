// Package debug provides conditional debug logging for bv.
//
// Debug logging is enabled by setting the BV_DEBUG environment variable:
//
//	BV_DEBUG=1 bv --robot-triage
//
// When enabled, debug messages are written to stderr with timestamps.
// When disabled (default), all debug functions are no-ops with zero overhead.
//
// Usage:
//
//	import "github.com/Dicklesworthstone/beads_viewer/pkg/debug"
//
//	func myFunc() {
//	    debug.Log("processing %d items", count)
//	    // ...
//	    debug.LogTiming("myFunc", elapsed)
//	}
package debug

import (
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

var (
	// enabled is true when BV_DEBUG env var is set.
	// Uses atomic.Bool for thread-safe concurrent access.
	enabled atomic.Bool
	// logger writes to stderr with [BV_DEBUG] prefix.
	// Protected by loggerMu for safe concurrent initialization.
	logger   *log.Logger
	loggerMu sync.Mutex
)

func init() {
	if env.Debug.Get() != "" {
		enabled.Store(true)
		logger = log.New(os.Stderr, "[BV_DEBUG] ", log.Ltime|log.Lmicroseconds)
	}
}

func getLogger() *log.Logger {
	loggerMu.Lock()
	defer loggerMu.Unlock()
	return logger
}

// Enabled returns whether debug logging is enabled.
func Enabled() bool {
	return enabled.Load()
}

// SetEnabled allows programmatic control of debug logging.
// Note: This also requires initializing the logger if not already done.
func SetEnabled(e bool) {
	enabled.Store(e)
	if e {
		loggerMu.Lock()
		if logger == nil {
			logger = log.New(os.Stderr, "[BV_DEBUG] ", log.Ltime|log.Lmicroseconds)
		}
		loggerMu.Unlock()
	}
}

// Log writes a debug message if debug logging is enabled.
// Uses printf-style formatting.
func Log(format string, args ...any) {
	if !enabled.Load() {
		return
	}
	if l := getLogger(); l != nil {
		l.Printf(format, args...)
	}
}

// LogTiming writes a timing message if debug logging is enabled.
func LogTiming(name string, d time.Duration) {
	if !enabled.Load() {
		return
	}
	if l := getLogger(); l != nil {
		l.Printf("%s took %v", name, d)
	}
}

// LogIf writes a debug message only if the condition is true.
func LogIf(cond bool, format string, args ...any) {
	if !enabled.Load() || !cond {
		return
	}
	if l := getLogger(); l != nil {
		l.Printf(format, args...)
	}
}

// LogFunc returns a function that logs a debug message when called.
// Useful for deferred logging:
//
//	defer debug.LogFunc("myFunc done")()
func LogFunc(msg string) func() {
	if !enabled.Load() {
		return func() {}
	}
	return func() {
		if l := getLogger(); l != nil {
			l.Print(msg)
		}
	}
}

// LogEnterExit logs function entry and exit with timing.
// Usage:
//
//	func myFunc() {
//	    defer debug.LogEnterExit("myFunc")()
//	    // ...
//	}
func LogEnterExit(name string) func() {
	if !enabled.Load() {
		return func() {}
	}
	if l := getLogger(); l != nil {
		l.Printf("-> %s", name)
	}
	start := time.Now()
	return func() {
		if l := getLogger(); l != nil {
			l.Printf("<- %s (%v)", name, time.Since(start))
		}
	}
}

// Trace is an alias for LogEnterExit for convenience.
var Trace = LogEnterExit

// Dump logs a value with its type for debugging complex structures.
func Dump(name string, v any) {
	if !enabled.Load() {
		return
	}
	if l := getLogger(); l != nil {
		l.Printf("%s: %T = %+v", name, v, v)
	}
}

// Section logs a section header for visual organization in debug output.
func Section(name string) {
	if !enabled.Load() {
		return
	}
	if l := getLogger(); l != nil {
		l.Printf("=== %s ===", name)
	}
}

// Checkpoint logs a numbered checkpoint for tracking progress.
var checkpointCounter atomic.Int64

func Checkpoint(msg string) {
	if !enabled.Load() {
		return
	}
	n := checkpointCounter.Add(1)
	if l := getLogger(); l != nil {
		l.Printf("[%d] %s", n, msg)
	}
}

// ResetCheckpoints resets the checkpoint counter.
func ResetCheckpoints() {
	checkpointCounter.Store(0)
}

// Assert logs a message and panics if the condition is false.
// Only active when debug is enabled.
func Assert(cond bool, msg string) {
	if !enabled.Load() {
		return
	}
	if !cond {
		if l := getLogger(); l != nil {
			l.Printf("ASSERTION FAILED: %s", msg)
		}
		panic(fmt.Sprintf("debug assertion failed: %s", msg))
	}
}

// AssertNoError logs and panics if err is not nil.
// Only active when debug is enabled.
func AssertNoError(err error, context string) {
	if !enabled.Load() {
		return
	}
	if err != nil {
		if l := getLogger(); l != nil {
			l.Printf("ASSERTION FAILED: %s: %v", context, err)
		}
		panic(fmt.Sprintf("debug assertion failed: %s: %v", context, err))
	}
}
