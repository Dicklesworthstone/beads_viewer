// Package ui provides the terminal user interface for beads_viewer.
// This file implements the BackgroundWorker for off-thread data processing.
package ui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	dbg "github.com/Dicklesworthstone/beads_viewer/pkg/debug"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/recipe"
	"github.com/Dicklesworthstone/beads_viewer/pkg/watcher"
)

// WorkerState represents the current state of the background worker.
type WorkerState int

const (
	// WorkerIdle means the worker is waiting for file changes.
	WorkerIdle WorkerState = iota
	// WorkerProcessing means the worker is building a new snapshot.
	WorkerProcessing
	// WorkerStopped means the worker has been stopped.
	WorkerStopped
)

const (
	backgroundWorkerShutdownTimeout = 500 * time.Millisecond
	// Priority-aware replacement is intentionally a one-slot mailbox. With a
	// larger channel, removing and requeueing only the head can reorder the
	// untouched suffix, while draining the suffix races the UI's direct receiver.
	backgroundWorkerMessageBuffer = 1
)

// WorkerLogLevel controls background worker log verbosity.
type WorkerLogLevel int

const (
	LogLevelNone WorkerLogLevel = iota
	LogLevelError
	LogLevelWarn
	LogLevelInfo
	LogLevelDebug
	LogLevelTrace
)

func (l WorkerLogLevel) String() string {
	switch l {
	case LogLevelError:
		return "error"
	case LogLevelWarn:
		return "warn"
	case LogLevelInfo:
		return "info"
	case LogLevelDebug:
		return "debug"
	case LogLevelTrace:
		return "trace"
	default:
		return "none"
	}
}

func parseWorkerLogLevel(raw string) WorkerLogLevel {
	value := strings.TrimSpace(strings.ToLower(raw))
	switch value {
	case "none", "off", "0":
		return LogLevelNone
	case "error", "err", "1":
		return LogLevelError
	case "warn", "warning", "2":
		return LogLevelWarn
	case "info", "3":
		return LogLevelInfo
	case "debug", "4":
		return LogLevelDebug
	case "trace", "5":
		return LogLevelTrace
	default:
		return LogLevelWarn
	}
}

// WorkerError wraps errors with phase and retry context.
type WorkerError struct {
	Phase   string    // "load", "parse", "analyze_phase1", "analyze_phase2"
	Cause   error     // The underlying error
	Time    time.Time // When the error occurred
	Retries int       // Number of retry attempts
}

func (e WorkerError) Error() string {
	return fmt.Sprintf("%s failed: %v (retries: %d)", e.Phase, e.Cause, e.Retries)
}

func (e WorkerError) Unwrap() error {
	return e.Cause
}

type WorkerHealth struct {
	Started       bool
	Alive         bool
	LastHeartbeat time.Time
	RecoveryCount int
	UptimeSince   time.Time

	IdleGCEnabled      bool
	IdleGCCount        uint64
	IdleGCTotal        time.Duration
	IdleGCLastDuration time.Duration
	IdleGCLastAt       time.Time
}

// WorkerMetrics captures the most recent metrics snapshot.
type WorkerMetrics struct {
	ProcessingCount      uint64
	ProcessingDuration   time.Duration
	Phase1Duration       time.Duration
	Phase2Duration       time.Duration
	CoalesceCount        int64
	QueueDepth           int64
	SnapshotVersion      uint64
	SnapshotSizeBytes    int64
	PoolHits             uint64
	PoolMisses           uint64
	GCPauseDelta         time.Duration
	SwapLatency          time.Duration
	UIUpdateLatency      time.Duration
	LastFileChangeAt     time.Time
	LastSnapshotReadyAt  time.Time
	IncrementalListCount uint64
	FullListCount        uint64
	IncrementalListRatio float64
}

type workerMetrics struct {
	processingCount        atomic.Uint64
	lastProcessingNs       atomic.Int64
	lastPhase1Ns           atomic.Int64
	lastPhase2Ns           atomic.Int64
	lastCoalesceCount      atomic.Int64
	lastQueueDepth         atomic.Int64
	lastSnapshotSizeBytes  atomic.Int64
	lastGCPauseDeltaNs     atomic.Int64
	lastSwapLatencyNs      atomic.Int64
	lastUIUpdateLatencyNs  atomic.Int64
	lastSnapshotReadyUnix  atomic.Int64
	lastFileChangeUnixNano atomic.Int64
	poolHits               atomic.Uint64
	poolMisses             atomic.Uint64
	snapshotVersion        atomic.Uint64
	incrementalListCount   atomic.Uint64
	fullListCount          atomic.Uint64
}

// BackgroundWorker manages background processing of beads data.
// It owns the file watcher, implements coalescing, and builds snapshots
// off the UI thread.
type BackgroundWorker struct {
	// Configuration
	beadsPath         string
	debounceDelay     time.Duration
	heartbeatInterval time.Duration
	watchdogInterval  time.Duration
	heartbeatTimeout  time.Duration
	processingTimeout time.Duration
	maxRecoveries     int

	// State
	// SAFETY: never invoke logging, callbacks, channel operations, or other
	// caller-controlled work while holding mu. Go's package logger ultimately
	// calls a replaceable io.Writer, which may re-enter worker accessors.
	mu                sync.RWMutex
	sendMu            sync.Mutex // Serializes priority-aware replacement in msgCh.
	state             WorkerState
	dirty             bool // True if a change came in while processing
	processScheduled  bool // True after process() is queued but before it starts processing
	snapshot          *DataSnapshot
	started           bool // True if Start() has been called
	watchdogStarted   bool
	startTime         time.Time
	lastHeartbeat     time.Time
	processingStart   time.Time
	recoveryCount     int
	recovering        bool
	generation        uint64
	lastHash          string // Content hash of last processed snapshot (for dedup)
	forceNext         bool   // Force the next snapshot build even if content hash matches
	currentRecipe     *recipe.Recipe
	currentRecipeID   string // Recipe identifier for snapshot rebuild keys
	currentRecipeHash string // Recipe fingerprint for rebuild keys (bv-4ilb)
	logLevel          WorkerLogLevel
	logJSON           bool
	metricsEnabled    bool
	tracePath         string
	traceFile         *os.File
	traceMu           sync.Mutex

	// Idle-time GC management (bv-4yje).
	idleGCEnabled     bool
	idleGCThreshold   time.Duration
	idleGCMinInterval time.Duration
	idleGCCheckEvery  time.Duration
	idleGCGCPercent   int

	lastActivityUnixNano atomic.Int64
	lastIdleGCUnixNano   atomic.Int64

	idleGCCount             atomic.Uint64
	idleGCTotalNanos        atomic.Int64
	idleGCLastDurationNanos atomic.Int64
	idleGCLastAtUnixNano    atomic.Int64

	idleGCAppliedGCPercent bool
	idleGCPrevGCPercent    int
	idleGCFunc             func()

	pendingChanges atomic.Int64
	coalesceCount  atomic.Int64
	metrics        workerMetrics

	// Error tracking
	lastError  *WorkerError // Most recent error (nil if last operation succeeded)
	errorCount int          // Consecutive error count for backoff

	// Components
	watcher *watcher.Watcher
	msgCh   chan tea.Msg

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	loopCtx    context.Context
	loopCancel context.CancelFunc
	done       chan struct{}
}

type IdleGCConfig struct {
	Enabled     bool
	Threshold   time.Duration
	CheckEvery  time.Duration
	MinInterval time.Duration
	GCPercent   int
}

// WorkerConfig configures the BackgroundWorker.
type WorkerConfig struct {
	BeadsPath     string
	DebounceDelay time.Duration

	IdleGC *IdleGCConfig

	// Watchdog configuration (bv-03h1). Zero values use defaults.
	HeartbeatInterval time.Duration // default: 5s
	WatchdogInterval  time.Duration // default: 10s
	HeartbeatTimeout  time.Duration // default: 30s
	ProcessingTimeout time.Duration // default: 30s
	MaxRecoveries     int           // default: 3
}

// NewBackgroundWorker creates a new background worker.
func NewBackgroundWorker(cfg WorkerConfig) (*BackgroundWorker, error) {
	ctx, cancel := context.WithCancel(context.Background())
	initialized := false
	defer func() {
		if !initialized {
			cancel()
		}
	}()

	if cfg.DebounceDelay == 0 {
		cfg.DebounceDelay = envDurationMilliseconds("BV_DEBOUNCE_MS", 200*time.Millisecond)
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = envDurationSeconds("BV_HEARTBEAT_INTERVAL_S", 5*time.Second)
	}
	if cfg.WatchdogInterval == 0 {
		cfg.WatchdogInterval = envDurationSeconds("BV_WATCHDOG_INTERVAL_S", 10*time.Second)
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = 30 * time.Second
	}
	if cfg.ProcessingTimeout == 0 {
		cfg.ProcessingTimeout = 30 * time.Second
	}
	if cfg.MaxRecoveries == 0 {
		cfg.MaxRecoveries = 3
	}

	logLevel := parseWorkerLogLevel(env.WorkerLogLevel.Get())
	metricsEnabled := env.WorkerMetrics.Bool()
	tracePath := strings.TrimSpace(env.WorkerTrace.Get())
	logJSON := env.Robot.Bool()

	idleGCConfig := IdleGCConfig{
		Enabled:     true,
		Threshold:   5 * time.Second,
		CheckEvery:  1 * time.Second,
		MinInterval: 30 * time.Second,
		GCPercent:   200,
	}
	if cfg.IdleGC != nil {
		idleGCConfig = *cfg.IdleGC
		if idleGCConfig.Threshold == 0 {
			idleGCConfig.Threshold = 5 * time.Second
		}
		if idleGCConfig.CheckEvery == 0 {
			idleGCConfig.CheckEvery = 1 * time.Second
		}
		if idleGCConfig.MinInterval == 0 {
			idleGCConfig.MinInterval = 30 * time.Second
		}
		if idleGCConfig.GCPercent == 0 {
			idleGCConfig.GCPercent = 200
		}
	}

	w := &BackgroundWorker{
		beadsPath:         cfg.BeadsPath,
		debounceDelay:     cfg.DebounceDelay,
		heartbeatInterval: cfg.HeartbeatInterval,
		watchdogInterval:  cfg.WatchdogInterval,
		heartbeatTimeout:  cfg.HeartbeatTimeout,
		processingTimeout: cfg.ProcessingTimeout,
		maxRecoveries:     cfg.MaxRecoveries,
		generation:        1, // Generation zero is reserved for non-worker messages.
		state:             WorkerIdle,
		msgCh:             make(chan tea.Msg, backgroundWorkerMessageBuffer),
		ctx:               ctx,
		cancel:            cancel,
		done:              make(chan struct{}),
		logLevel:          logLevel,
		logJSON:           logJSON,
		metricsEnabled:    metricsEnabled,
		tracePath:         tracePath,

		idleGCEnabled:     idleGCConfig.Enabled,
		idleGCThreshold:   idleGCConfig.Threshold,
		idleGCMinInterval: idleGCConfig.MinInterval,
		idleGCCheckEvery:  idleGCConfig.CheckEvery,
		idleGCGCPercent:   idleGCConfig.GCPercent,
		idleGCFunc:        runtime.GC,
	}
	w.lastActivityUnixNano.Store(time.Now().UnixNano())

	// Initialize file watcher
	if cfg.BeadsPath != "" {
		fw, err := watcher.NewWatcher(cfg.BeadsPath,
			watcher.WithDebounceDuration(cfg.DebounceDelay),
		)
		if err != nil {
			return nil, err
		}
		w.watcher = fw
	}

	initialized = true
	return w, nil
}

// Messages returns a channel of Bubble Tea messages emitted by the worker.
// The channel is owned by the worker and is never closed; use Done() to stop waiting.
func (w *BackgroundWorker) Messages() <-chan tea.Msg {
	if w == nil {
		return nil
	}
	return w.msgCh
}

// Done is closed when the worker is stopped.
func (w *BackgroundWorker) Done() <-chan struct{} {
	if w == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return w.ctx.Done()
}

// Metrics returns the latest metrics snapshot.
func (w *BackgroundWorker) Metrics() WorkerMetrics {
	if w == nil {
		return WorkerMetrics{}
	}
	lastSnapshotReady := time.Time{}
	if unix := w.metrics.lastSnapshotReadyUnix.Load(); unix > 0 {
		lastSnapshotReady = time.Unix(0, unix)
	}
	lastFileChange := time.Time{}
	if unix := w.metrics.lastFileChangeUnixNano.Load(); unix > 0 {
		lastFileChange = time.Unix(0, unix)
	}
	incremental := w.metrics.incrementalListCount.Load()
	full := w.metrics.fullListCount.Load()
	ratio := 0.0
	if total := incremental + full; total > 0 {
		ratio = float64(incremental) / float64(total)
	}

	return WorkerMetrics{
		ProcessingCount:      w.metrics.processingCount.Load(),
		ProcessingDuration:   time.Duration(w.metrics.lastProcessingNs.Load()),
		Phase1Duration:       time.Duration(w.metrics.lastPhase1Ns.Load()),
		Phase2Duration:       time.Duration(w.metrics.lastPhase2Ns.Load()),
		CoalesceCount:        w.metrics.lastCoalesceCount.Load(),
		QueueDepth:           w.metrics.lastQueueDepth.Load(),
		SnapshotVersion:      w.metrics.snapshotVersion.Load(),
		SnapshotSizeBytes:    w.metrics.lastSnapshotSizeBytes.Load(),
		PoolHits:             w.metrics.poolHits.Load(),
		PoolMisses:           w.metrics.poolMisses.Load(),
		GCPauseDelta:         time.Duration(w.metrics.lastGCPauseDeltaNs.Load()),
		SwapLatency:          time.Duration(w.metrics.lastSwapLatencyNs.Load()),
		UIUpdateLatency:      time.Duration(w.metrics.lastUIUpdateLatencyNs.Load()),
		LastSnapshotReadyAt:  lastSnapshotReady,
		LastFileChangeAt:     lastFileChange,
		IncrementalListCount: incremental,
		FullListCount:        full,
		IncrementalListRatio: ratio,
	}
}

func (w *BackgroundWorker) openTraceFile() {
	if w == nil || w.tracePath == "" {
		return
	}
	w.traceMu.Lock()
	if w.traceFile != nil {
		w.traceMu.Unlock()
		return
	}
	f, err := os.OpenFile(w.tracePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		w.traceMu.Unlock()
		log.Printf("background worker: trace_open_failed path=%s error=%v", w.tracePath, err)
		return
	}
	w.traceFile = f
	w.traceMu.Unlock()
}

func (w *BackgroundWorker) closeTraceFile() {
	if w == nil {
		return
	}
	w.traceMu.Lock()
	f := w.traceFile
	if f == nil {
		w.traceMu.Unlock()
		return
	}
	w.traceFile = nil
	w.traceMu.Unlock()
	if err := f.Close(); err != nil {
		w.logEvent(LogLevelWarn, "trace_close_failed", map[string]any{
			"path":  w.tracePath,
			"error": err.Error(),
		})
	}
}

func (w *BackgroundWorker) logEvent(level WorkerLogLevel, event string, fields map[string]any) {
	if w == nil || level == LogLevelNone {
		return
	}
	w.traceMu.Lock()
	traceEnabled := w.traceFile != nil
	w.traceMu.Unlock()
	if !traceEnabled && (w.logLevel == LogLevelNone || level > w.logLevel) {
		return
	}

	payload := map[string]any{
		"ts":        time.Now().UTC().Format(time.RFC3339Nano),
		"level":     level.String(),
		"component": "background_worker",
		"event":     event,
	}
	for k, v := range fields {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		log.Printf("background worker: failed to marshal log event %s: %v", event, err)
		return
	}

	if w.logLevel != LogLevelNone && level <= w.logLevel {
		log.Printf("%s", b)
	}
	if traceEnabled {
		w.traceMu.Lock()
		if w.traceFile != nil {
			_, _ = w.traceFile.Write(append(b, '\n'))
		}
		w.traceMu.Unlock()
	}
}

func (w *BackgroundWorker) noteFileChange(t time.Time) {
	if w == nil {
		return
	}
	w.metrics.lastFileChangeUnixNano.Store(t.UnixNano())
	depth := w.pendingChanges.Add(1)
	w.logEvent(LogLevelTrace, "file_change", map[string]any{
		"queue_depth": depth,
	})
}

func (w *BackgroundWorker) recordUIUpdateLatency(d time.Duration) {
	if w == nil {
		return
	}
	w.metrics.lastUIUpdateLatencyNs.Store(d.Nanoseconds())
	w.logEvent(LogLevelDebug, "ui_update_latency", map[string]any{
		"latency_ms": float64(d.Microseconds()) / 1000.0,
	})
}

// Start begins watching for file changes and processing in the background.
// Start is idempotent - calling it multiple times has no effect.
// Returns error if the worker has been stopped.
func (w *BackgroundWorker) Start() error {
	w.mu.Lock()
	if w.state == WorkerStopped {
		w.mu.Unlock()
		return fmt.Errorf("worker has been stopped")
	}
	if w.started {
		w.mu.Unlock()
		return nil // Already started
	}
	w.started = true
	// Publish the latch that belongs to this Start attempt before releasing the
	// mutex. Stop must never capture the constructor's never-closed placeholder
	// while Start is between watcher startup and process-loop launch.
	done := make(chan struct{})
	w.done = done
	now := time.Now()
	if w.startTime.IsZero() {
		w.startTime = now
	}
	w.lastHeartbeat = now
	w.recordActivityAt(now)
	idleGCEnabled := w.idleGCEnabled
	idleGCGCPercent := w.idleGCGCPercent
	idleGCCheckEvery := w.idleGCCheckEvery
	hasWatcher := w.watcher != nil
	if !hasWatcher {
		close(done)
	}
	w.mu.Unlock()

	w.openTraceFile()
	w.logEvent(LogLevelInfo, "worker_start", map[string]any{
		"beads_path": w.beadsPath,
	})

	// Avoid mutating global GC percent in tests (it can interfere with parallel test execution).
	if env.TestMode.Get() != "" {
		idleGCGCPercent = 0
	}

	if hasWatcher {
		// Serialize the external watcher transition with Stop. Otherwise Stop can
		// observe started=true in the gap before watcher.Start, stop a not-yet-live
		// watcher, and then have this goroutine start it after shutdown.
		w.mu.Lock()
		if w.state == WorkerStopped || w.ctx.Err() != nil {
			w.started = false
			// No process loop has been launched yet, so Start still owns this
			// attempt's latch and can release a concurrent Stop immediately.
			close(done)
			w.mu.Unlock()
			w.closeTraceFile()
			return fmt.Errorf("worker was stopped during start")
		}
		watcherErr := w.watcher.Start()
		if watcherErr != nil {
			// Reset started flag so caller can retry or Stop() won't block
			w.started = false
			close(done)
			w.mu.Unlock()
			w.closeTraceFile()
			return watcherErr
		}
		// Launch while holding w.mu: after unlock, Stop may win the mutex, but it
		// will always observe a loop responsible for closing this exact latch.
		w.lastHeartbeat = time.Now()
		w.startProcessLoopLocked(done)
		w.mu.Unlock()

		if idleGCEnabled && idleGCGCPercent > 0 {
			w.mu.Lock()
			if w.state != WorkerStopped && w.started && !w.idleGCAppliedGCPercent {
				w.idleGCPrevGCPercent = debug.SetGCPercent(idleGCGCPercent)
				w.idleGCAppliedGCPercent = true
			}
			w.mu.Unlock()
		}
		if idleGCEnabled && idleGCCheckEvery > 0 {
			go w.idleGCLoop(idleGCCheckEvery)
		}

		w.startWatchdog()
	} else {
		// No watcher - close done channel immediately so Stop() doesn't block
		// and re-check the lifecycle after opening the trace. Stop can run in the
		// gap between the first unlock and openTraceFile; without this check Start
		// could reopen the trace (and launch idle-GC work) after shutdown.
		w.mu.Lock()
		if w.state == WorkerStopped || w.ctx.Err() != nil {
			w.started = false
			w.mu.Unlock()
			w.closeTraceFile()
			return fmt.Errorf("worker was stopped during start")
		}
		w.mu.Unlock()
		if idleGCEnabled && idleGCGCPercent > 0 {
			w.mu.Lock()
			if w.state != WorkerStopped && w.started && !w.idleGCAppliedGCPercent {
				w.idleGCPrevGCPercent = debug.SetGCPercent(idleGCGCPercent)
				w.idleGCAppliedGCPercent = true
			}
			w.mu.Unlock()
		}
		if idleGCEnabled && idleGCCheckEvery > 0 {
			go w.idleGCLoop(idleGCCheckEvery)
		}

	}

	return nil
}

// Stop halts the background worker and cleans up resources.
// Stop is idempotent - calling it multiple times has no effect.
func (w *BackgroundWorker) Stop() {
	w.mu.Lock()
	if w.state == WorkerStopped {
		w.mu.Unlock()
		return
	}
	w.state = WorkerStopped
	w.processScheduled = false
	wasStarted := w.started
	loopCancel := w.loopCancel
	done := w.done
	w.loopCancel = nil
	restoreGCPercent := w.idleGCAppliedGCPercent
	prevGCPercent := w.idleGCPrevGCPercent
	w.idleGCAppliedGCPercent = false
	w.mu.Unlock()

	if restoreGCPercent {
		debug.SetGCPercent(prevGCPercent)
	}

	w.cancel()
	if loopCancel != nil {
		loopCancel()
	}

	if w.watcher != nil {
		w.watcher.Stop()
	}

	// Only wait for done if Start() was called
	if wasStarted {
		timer := time.NewTimer(backgroundWorkerShutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			w.logEvent(LogLevelWarn, "shutdown_timeout", nil)
		}
	}

	w.mu.Lock()
	snapshot := w.snapshot
	w.snapshot = nil
	w.mu.Unlock()
	if snapshot != nil {
		snapshot.releasePooledIssues()
	}

	w.logEvent(LogLevelInfo, "worker_stop", nil)
	w.closeTraceFile()
}

// startProcessLoopLocked publishes the loop's cancellation handle before the
// goroutine can be observed by Stop or recovery. The caller must hold w.mu.
func (w *BackgroundWorker) startProcessLoopLocked(done chan struct{}) {
	loopCtx, loopCancel := context.WithCancel(w.ctx)
	w.loopCtx = loopCtx
	w.loopCancel = loopCancel
	go func() {
		defer loopCancel()
		w.processLoop(loopCtx, done)
	}()
}

func (w *BackgroundWorker) startWatchdog() {
	if w == nil {
		return
	}

	w.mu.Lock()
	if w.watchdogStarted || w.state == WorkerStopped {
		w.mu.Unlock()
		return
	}
	w.watchdogStarted = true
	interval := w.watchdogInterval
	w.mu.Unlock()

	go w.watchdogLoop(interval)
}

func (w *BackgroundWorker) watchdogLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case now := <-ticker.C:
			w.checkHealth(now)
		}
	}
}

func (w *BackgroundWorker) recordHeartbeat(t time.Time) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.lastHeartbeat = t
	w.mu.Unlock()
}

func (w *BackgroundWorker) recordActivity() {
	w.recordActivityAt(time.Now())
}

func (w *BackgroundWorker) recordActivityAt(t time.Time) {
	if w == nil {
		return
	}
	w.lastActivityUnixNano.Store(t.UnixNano())
}

func (w *BackgroundWorker) checkHealth(now time.Time) {
	if w == nil {
		return
	}

	w.mu.RLock()
	if !w.started || w.state == WorkerStopped || w.recovering {
		w.mu.RUnlock()
		return
	}
	state := w.state
	lastHeartbeat := w.lastHeartbeat
	heartbeatTimeout := w.heartbeatTimeout
	processingStart := w.processingStart
	processingTimeout := w.processingTimeout
	w.mu.RUnlock()

	if state == WorkerProcessing && !processingStart.IsZero() && now.Sub(processingStart) > processingTimeout {
		w.attemptRecovery(fmt.Sprintf("processing exceeded %s", processingTimeout))
		return
	}

	if !lastHeartbeat.IsZero() && now.Sub(lastHeartbeat) > heartbeatTimeout {
		w.attemptRecovery(fmt.Sprintf("missed heartbeat for %s", heartbeatTimeout))
	}
}

func (w *BackgroundWorker) attemptRecovery(reason string) {
	if w == nil {
		return
	}

	w.mu.Lock()
	if w.state == WorkerStopped || !w.started || w.recovering {
		w.mu.Unlock()
		return
	}

	w.recovering = true
	w.recoveryCount++
	attempt := w.recoveryCount
	maxRecoveries := w.maxRecoveries

	// Invalidate any in-flight processing and reset to an idle baseline.
	w.generation++
	generation := w.generation
	w.state = WorkerIdle
	w.dirty = false
	w.processScheduled = false
	w.processingStart = time.Time{}
	w.lastHeartbeat = time.Now()

	loopCancel := w.loopCancel
	done := w.done
	w.loopCancel = nil
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.recovering = false
		w.mu.Unlock()
	}()

	if maxRecoveries > 0 && attempt > maxRecoveries {
		w.send(SnapshotErrorMsg{
			Err:              fmt.Errorf("background worker unresponsive (giving up): %s", reason),
			Recoverable:      false,
			WorkerGeneration: generation,
		})
		w.Stop()
		return
	}

	w.logEvent(LogLevelWarn, "recovery_attempt", map[string]any{
		"attempt": attempt,
		"max":     maxRecoveries,
		"reason":  reason,
	})

	if loopCancel != nil {
		loopCancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			w.logEvent(LogLevelWarn, "recovery_loop_shutdown_timeout", nil)
		}
	}

	var watcherErr error
	w.mu.Lock()
	if w.state == WorkerStopped || w.ctx.Err() != nil {
		w.mu.Unlock()
		return
	}
	if w.watcher != nil {
		// Keep Stop excluded across the stop/start pair. If Stop wins the mutex
		// first, the state check above aborts; if recovery wins, Stop runs next and
		// shuts down the freshly restarted watcher.
		w.watcher.Stop()
		watcherErr = w.watcher.Start()
	}
	if watcherErr == nil {
		// Install and launch the replacement loop before Stop can observe the
		// restarted watcher. The new loop owns closing this exact latch.
		nextDone := make(chan struct{})
		w.done = nextDone
		w.lastHeartbeat = time.Now()
		w.startProcessLoopLocked(nextDone)
	}
	w.mu.Unlock()
	if watcherErr != nil {
		w.send(SnapshotErrorMsg{
			Err:              fmt.Errorf("background worker recovery failed (watcher start): %w", watcherErr),
			Recoverable:      false,
			WorkerGeneration: generation,
		})
		w.Stop()
		return
	}

	w.ForceRefresh()
}

// TriggerRefresh manually triggers a refresh of the data.
// Has no effect if the worker is stopped. Concurrent refresh requests are
// coalesced while processing is active or already scheduled.
func (w *BackgroundWorker) TriggerRefresh() {
	w.mu.Lock()
	if w.state == WorkerStopped {
		w.mu.Unlock()
		return
	}
	if w.state == WorkerProcessing || w.processScheduled {
		w.dirty = true
		coalesced := w.coalesceCount.Add(1)
		w.mu.Unlock()
		w.logEvent(LogLevelDebug, "coalesce", map[string]any{
			"count": coalesced,
		})
		return
	}
	w.processScheduled = true
	w.mu.Unlock()

	// Trigger processing
	go w.process()
}

// ForceRefresh triggers immediate processing, bypassing debounce and content-hash
// dedup so the UI can deterministically refresh even when the data is "fresh".
func (w *BackgroundWorker) ForceRefresh() {
	w.mu.Lock()
	if w.state == WorkerStopped {
		w.mu.Unlock()
		return
	}

	w.forceNext = true

	if w.state == WorkerProcessing || w.processScheduled {
		w.dirty = true
		coalesced := w.coalesceCount.Add(1)
		w.mu.Unlock()
		w.logEvent(LogLevelDebug, "coalesce", map[string]any{
			"count": coalesced,
		})
		return
	}
	w.processScheduled = true
	w.mu.Unlock()

	go w.process()
}

// HandleRefreshRequest applies a UI refresh request on the worker side. Recipe
// changes and forced reloads are folded into one processing run.
func (w *BackgroundWorker) HandleRefreshRequest(msg RefreshRequestMsg) {
	recipeChanged := false
	if msg.Recipe != nil {
		recipeChanged = w.setRecipe(msg.Recipe)
	}
	if msg.Force || recipeChanged {
		w.ForceRefresh()
		return
	}
	w.TriggerRefresh()
}

func recipeFingerprint(r *recipe.Recipe) string {
	if r == nil {
		return ""
	}

	b, err := json.Marshal(r)
	if err != nil {
		// Fall back to name-only to preserve determinism.
		return r.Name
	}

	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// SetRecipe updates the worker's current recipe and triggers a refresh (bv-2h40).
// This allows Phase 3 view builders to incorporate recipe/filter state off-thread.
func (w *BackgroundWorker) SetRecipe(r *recipe.Recipe) {
	if w.setRecipe(r) {
		w.ForceRefresh()
	}
}

func (w *BackgroundWorker) setRecipe(r *recipe.Recipe) bool {
	w.mu.Lock()
	if w.state == WorkerStopped {
		w.mu.Unlock()
		return false
	}

	nextID := ""
	nextHash := ""
	if r != nil {
		nextID = r.Name
		nextHash = recipeFingerprint(r)
	}

	changed := w.currentRecipeID != nextID || w.currentRecipeHash != nextHash
	w.currentRecipe = r
	w.currentRecipeID = nextID
	w.currentRecipeHash = nextHash
	w.mu.Unlock()
	return changed
}

// GetSnapshot returns the current snapshot (may be nil).
func (w *BackgroundWorker) GetSnapshot() *DataSnapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.snapshot
}

// State returns the current worker state.
func (w *BackgroundWorker) State() WorkerState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state
}

// Generation returns the current worker lifecycle generation. Recovery bumps
// this value so callers can reject messages emitted by invalidated work.
func (w *BackgroundWorker) Generation() uint64 {
	if w == nil {
		return 0
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.generation
}

// ProcessingDuration returns how long the worker has been in the processing state.
// Returns 0 if not currently processing.
func (w *BackgroundWorker) ProcessingDuration() time.Duration {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.state != WorkerProcessing || w.processingStart.IsZero() {
		return 0
	}
	return time.Since(w.processingStart)
}

// processLoop watches for file changes and triggers processing.
func (w *BackgroundWorker) processLoop(loopCtx context.Context, done chan struct{}) {
	defer close(done)
	defer func() {
		if r := recover(); r != nil {
			w.logEvent(LogLevelError, "process_loop_panic", map[string]any{
				"panic": fmt.Sprintf("%v", r),
				"stack": string(debug.Stack()),
			})
		}
	}()

	w.mu.RLock()
	heartbeatInterval := w.heartbeatInterval
	wch := w.watcher
	w.mu.RUnlock()

	if wch == nil {
		return
	}

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-loopCtx.Done():
			return

		case <-heartbeatTicker.C:
			w.recordHeartbeat(time.Now())

		case <-wch.Changed():
			w.noteFileChange(time.Now())
			w.TriggerRefresh()
		}
	}
}

// process builds a new snapshot from the current file.
func (w *BackgroundWorker) process() {
	w.processWithSnapshotBuilder(w.buildSnapshotResult)
}

// processWithSnapshotBuilder keeps the generation-fenced completion path
// independently testable without relying on filesystem timing.
func (w *BackgroundWorker) processWithSnapshotBuilder(build func(bool) snapshotBuildResult) {
	w.mu.Lock()
	w.processScheduled = false
	if w.state != WorkerIdle {
		// Already stopped or processing
		if w.state == WorkerProcessing {
			// Mark dirty so current processor will re-run when done
			w.dirty = true
		}
		w.mu.Unlock()
		return
	}
	w.state = WorkerProcessing
	w.dirty = false
	forceNext := w.forceNext
	w.forceNext = false
	now := time.Now()
	w.processingStart = now
	w.lastHeartbeat = now
	gen := w.generation
	w.mu.Unlock()
	w.logEvent(LogLevelDebug, "state_change", map[string]any{
		"state": "processing",
	})

	processStart := time.Now()
	queueDepth := w.pendingChanges.Swap(0)
	w.metrics.lastQueueDepth.Store(queueDepth)
	w.coalesceCount.Store(0)
	w.logEvent(LogLevelInfo, "process_start", map[string]any{
		"queue_depth": queueDepth,
	})

	// Load and build snapshot. Error publication is deliberately deferred until
	// after the generation fence below: a timed-out build may continue after
	// recovery has already started a replacement generation.
	result := build(forceNext)
	snapshot := result.snapshot

	w.mu.Lock()
	// If we recovered while processing, ignore this stale result.
	if w.generation != gen {
		w.mu.Unlock()
		if snapshot != nil {
			snapshot.releasePooledIssues()
		}
		return
	}
	// Check if stopped while we were processing - don't overwrite stopped state
	if w.state == WorkerStopped {
		w.mu.Unlock()
		if snapshot != nil {
			snapshot.releasePooledIssues()
		}
		return
	}
	if result.err != nil || result.clearError {
		w.recordErrorLocked(result.err)
	}
	w.processingStart = time.Time{}
	// Only update snapshot if we got a new one (nil means deduped or error)
	var swapLatency time.Duration
	var version uint64
	var previousSnapshot *DataSnapshot
	if snapshot != nil {
		swapStart := time.Now()
		w.lastHash = snapshot.DataHash
		previousSnapshot = w.snapshot
		w.snapshot = snapshot
		swapLatency = time.Since(swapStart)
		version = w.metrics.snapshotVersion.Add(1)
		if snapshot.IncrementalListUsed {
			w.metrics.incrementalListCount.Add(1)
		} else {
			w.metrics.fullListCount.Add(1)
		}
	}
	wasDirty := w.dirty
	coalesced := w.coalesceCount.Load()
	w.state = WorkerIdle
	w.lastHeartbeat = time.Now()
	w.mu.Unlock()
	w.logEvent(LogLevelDebug, "state_change", map[string]any{
		"state": "idle",
	})
	if previousSnapshot != nil && previousSnapshot != snapshot {
		previousSnapshot.releasePooledIssues()
	}

	processingDuration := time.Since(processStart)
	w.metrics.processingCount.Add(1)
	w.metrics.lastProcessingNs.Store(processingDuration.Nanoseconds())
	if swapLatency > 0 {
		w.metrics.lastSwapLatencyNs.Store(swapLatency.Nanoseconds())
	}
	w.metrics.lastCoalesceCount.Store(coalesced)

	w.recordActivity()

	if result.err != nil {
		fields := map[string]any{
			"phase": result.err.Phase,
			"error": result.err.Error(),
		}
		if result.err.Phase == "load" {
			fields["path"] = w.beadsPath
		}
		w.logEvent(LogLevelError, "snapshot_build_failed", fields)
		w.send(SnapshotErrorMsg{
			Err:              result.err,
			Recoverable:      true,
			WorkerGeneration: gen,
		})
	}

	// Notify UI only if we have a new snapshot
	if snapshot != nil {
		readyAt := time.Now()
		w.metrics.lastSnapshotReadyUnix.Store(readyAt.UnixNano())
		var fileChangeAt time.Time
		if unix := w.metrics.lastFileChangeUnixNano.Load(); unix > 0 {
			fileChangeAt = time.Unix(0, unix)
		}
		w.logEvent(LogLevelInfo, "snapshot_ready", map[string]any{
			"issues":      len(snapshot.Issues),
			"hash":        hashPrefix(snapshot.DataHash),
			"version":     version,
			"swap_us":     float64(swapLatency.Microseconds()),
			"process_ms":  float64(processingDuration.Microseconds()) / 1000.0,
			"coalesced":   coalesced,
			"queue_depth": queueDepth,
		})
		w.send(SnapshotReadyMsg{
			Snapshot:         snapshot,
			FileChangeAt:     fileChangeAt,
			SentAt:           readyAt,
			SnapshotVer:      version,
			WorkerGeneration: gen,
			QueueDepth:       queueDepth,
			CoalesceCount:    coalesced,
		})
		// Start the Phase 2 waiter only after SnapshotReadyMsg has been queued.
		// This guarantees message order and gives the completion message the
		// exact snapshot/version identity needed to reject same-hash refreshes.
		if !snapshot.IsPhase2Ready() {
			go w.runPhase2Analysis(snapshot, version, gen)
		}
	}

	// If dirty, process again immediately
	if wasDirty {
		w.TriggerRefresh()
	}
}

// safeCompute executes fn and recovers from any panics.
// Returns a WorkerError if fn panics, nil otherwise.
func (w *BackgroundWorker) safeCompute(phase string, fn func() error) *WorkerError {
	var result *WorkerError
	func() {
		defer func() {
			if r := recover(); r != nil {
				result = &WorkerError{
					Phase: phase,
					Cause: fmt.Errorf("panic: %v\n%s", r, debug.Stack()),
					Time:  time.Now(),
				}
			}
		}()
		if err := fn(); err != nil {
			result = &WorkerError{
				Phase: phase,
				Cause: err,
				Time:  time.Now(),
			}
		}
	}()
	return result
}

// recordError tracks an error and updates error state.
func (w *BackgroundWorker) recordError(err *WorkerError) {
	w.mu.Lock()
	w.recordErrorLocked(err)
	w.mu.Unlock()
}

// recordErrorLocked updates error state while the caller holds w.mu. process
// uses this form so generation validation and error-state publication are one
// atomic decision.
func (w *BackgroundWorker) recordErrorLocked(err *WorkerError) {
	w.lastError = err
	if err != nil {
		w.errorCount++
		err.Retries = w.errorCount
	} else {
		w.errorCount = 0
	}
}

// LastError returns the most recent error (nil if last operation succeeded).
func (w *BackgroundWorker) LastError() *WorkerError {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastError
}

func (w *BackgroundWorker) WatcherInfo() (polling bool, fsType watcher.FilesystemType, pollInterval time.Duration) {
	if w == nil || w.watcher == nil {
		return false, watcher.FSTypeUnknown, 0
	}
	return w.watcher.IsPolling(), w.watcher.FilesystemType(), w.watcher.PollInterval()
}

func (w *BackgroundWorker) Health() WorkerHealth {
	if w == nil {
		return WorkerHealth{}
	}

	w.mu.RLock()
	started := w.started
	state := w.state
	lastHeartbeat := w.lastHeartbeat
	recoveryCount := w.recoveryCount
	startTime := w.startTime
	timeout := w.heartbeatTimeout
	w.mu.RUnlock()

	alive := started && state != WorkerStopped && !lastHeartbeat.IsZero() && time.Since(lastHeartbeat) <= timeout

	idleGCCount := w.idleGCCount.Load()
	idleGCTotalNanos := w.idleGCTotalNanos.Load()
	idleGCLastDurationNanos := w.idleGCLastDurationNanos.Load()
	idleGCLastAtUnixNano := w.idleGCLastAtUnixNano.Load()

	idleGCLastAt := time.Time{}
	if idleGCLastAtUnixNano != 0 {
		idleGCLastAt = time.Unix(0, idleGCLastAtUnixNano)
	}

	return WorkerHealth{
		Started:       started,
		Alive:         alive,
		LastHeartbeat: lastHeartbeat,
		RecoveryCount: recoveryCount,
		UptimeSince:   startTime,

		IdleGCEnabled:      w.idleGCEnabled,
		IdleGCCount:        idleGCCount,
		IdleGCTotal:        time.Duration(idleGCTotalNanos),
		IdleGCLastDuration: time.Duration(idleGCLastDurationNanos),
		IdleGCLastAt:       idleGCLastAt,
	}
}

func (w *BackgroundWorker) idleGCLoop(checkEvery time.Duration) {
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case now := <-ticker.C:
			w.maybeIdleGC(now)
		}
	}
}

func (w *BackgroundWorker) maybeIdleGC(now time.Time) {
	if w == nil {
		return
	}

	// Hold the worker lock while triggering GC to ensure it never overlaps with processing.
	w.mu.Lock()
	enabled := w.idleGCEnabled
	state := w.state
	processScheduled := w.processScheduled
	threshold := w.idleGCThreshold
	minInterval := w.idleGCMinInterval
	w.mu.Unlock()

	if !enabled || state != WorkerIdle || processScheduled {
		return
	}

	w.mu.Lock()
	// Re-check under lock for correctness and to prevent racing with process() state transitions.
	if !w.idleGCEnabled || w.state != WorkerIdle || w.processScheduled {
		w.mu.Unlock()
		return
	}

	lastActivityUnixNano := w.lastActivityUnixNano.Load()
	if lastActivityUnixNano == 0 {
		w.recordActivityAt(now)
		w.mu.Unlock()
		return
	}
	lastActivity := time.Unix(0, lastActivityUnixNano)
	if now.Sub(lastActivity) < threshold {
		w.mu.Unlock()
		return
	}

	lastIdleGCUnixNano := w.lastIdleGCUnixNano.Load()
	if lastIdleGCUnixNano != 0 && now.Sub(time.Unix(0, lastIdleGCUnixNano)) < minInterval {
		w.mu.Unlock()
		return
	}

	gcFunc := w.idleGCFunc
	if gcFunc == nil {
		gcFunc = runtime.GC
	}
	start := time.Now()
	gcFunc()
	duration := time.Since(start)

	ranAt := time.Now()
	w.lastIdleGCUnixNano.Store(ranAt.UnixNano())
	w.idleGCCount.Add(1)
	w.idleGCTotalNanos.Add(duration.Nanoseconds())
	w.idleGCLastDurationNanos.Store(duration.Nanoseconds())
	w.idleGCLastAtUnixNano.Store(ranAt.UnixNano())
	w.mu.Unlock()
}

type snapshotBuildResult struct {
	snapshot   *DataSnapshot
	err        *WorkerError
	clearError bool
}

// buildSnapshot loads data and constructs a new DataSnapshot. Direct benchmark
// and test callers only need the snapshot; process uses buildSnapshotResult so
// it can publish errors after validating the worker generation.
func (w *BackgroundWorker) buildSnapshot(forceNext bool) *DataSnapshot {
	return w.buildSnapshotResult(forceNext).snapshot
}

// buildSnapshotResult is called from the worker goroutine (NOT the UI thread).
// It is intentionally side-effect free with respect to worker error state and
// UI messages: process owns those effects after its generation fence.
func (w *BackgroundWorker) buildSnapshotResult(forceNext bool) snapshotBuildResult {
	if w.beadsPath == "" {
		return snapshotBuildResult{}
	}

	start := time.Now()
	profileSnapshot := dbg.Enabled()
	var snapshotTimings map[string]time.Duration
	recordTiming := func(name string, d time.Duration) {
		if !profileSnapshot {
			return
		}
		if snapshotTimings == nil {
			snapshotTimings = make(map[string]time.Duration, 8)
		}
		snapshotTimings[name] = d
		dbg.LogTiming("worker."+name, d)
	}
	if profileSnapshot {
		dbg.Log("worker.snapshot_start path=%s", w.beadsPath)
	}
	metricsEnabled := w.metricsEnabled
	var memBefore runtime.MemStats
	if metricsEnabled {
		runtime.ReadMemStats(&memBefore)
	}

	// Capture recipe state for this snapshot before loading (bv-2h40).
	w.mu.RLock()
	currentRecipe := w.currentRecipe
	recipeID := w.currentRecipeID
	recipeHash := w.currentRecipeHash
	w.mu.RUnlock()

	// Determine dataset tier using a fast line count (bv-9thm).
	sourceLineCount := 0
	tier := datasetTierUnknown
	var countStart time.Time
	if profileSnapshot {
		countStart = time.Now()
	}
	countErr := w.safeCompute("count_lines", func() error {
		n, err := countIssuesForReload(w.beadsPath)
		if err != nil {
			return err
		}
		sourceLineCount = n
		tier = datasetTierForIssueCount(n)
		return nil
	})
	if profileSnapshot {
		recordTiming("line_count", time.Since(countStart))
	}
	if countErr != nil {
		w.logEvent(LogLevelDebug, "snapshot_line_count_failed", map[string]any{
			"path":  w.beadsPath,
			"error": countErr.Error(),
		})
	}

	// Huge tier: default to open-only unless the recipe explicitly includes closed/tombstone.
	loadOpenOnly := tier == datasetTierHuge && !recipeIncludesClosedStatuses(currentRecipe)

	// Load issues from file with panic recovery
	var issues []model.Issue
	var pooledRefs []*model.Issue
	var authority *model.ReadinessIndex
	var authorityHash string
	loadWarningCount := 0
	var loadStart time.Time
	if profileSnapshot {
		loadStart = time.Now()
	}
	loadErr := w.safeCompute("load", func() error {
		var err error
		var loaded reloadIssueData
		opts := loader.ParseOptions{
			WarningHandler: func(string) {
				loadWarningCount++
			},
			BufferSize: envMaxLineSizeBytes(),
		}
		if loadOpenOnly {
			opts.IssueFilter = func(i *model.Issue) bool {
				return i.Status != model.StatusClosed && i.Status != model.StatusTombstone
			}
		}
		loaded, err = loadIssuesForReload(w.beadsPath, opts)
		if err == nil {
			issues = loaded.Issues
			pooledRefs = loaded.PoolRefs
			authority = loaded.Authority
			authorityHash = loaded.AuthorityHash
		}
		return err
	})
	if profileSnapshot {
		recordTiming("load_issues", time.Since(loadStart))
	}

	if loadErr != nil {
		return snapshotBuildResult{err: loadErr}
	}

	loadDuration := time.Since(start)

	// Compute content hash for dedup
	hash := analysis.ComputeDataHash(issues)

	// Check if content is unchanged (dedup optimization)
	w.mu.RLock()
	lastHash := w.lastHash
	prevSnapshot := w.snapshot
	w.mu.RUnlock()

	loadMetadataUnchanged := prevSnapshot != nil &&
		prevSnapshot.AuthorityHash == authorityHash &&
		prevSnapshot.SourceIssueCountHint == sourceLineCount &&
		prevSnapshot.LoadWarningCount == loadWarningCount &&
		prevSnapshot.DatasetTier == tier &&
		prevSnapshot.LoadedOpenOnly == loadOpenOnly &&
		prevSnapshot.RecipeName == recipeID &&
		prevSnapshot.RecipeHash == recipeHash
	if !forceNext && hash == lastHash && lastHash != "" && loadMetadataUnchanged {
		w.logEvent(LogLevelDebug, "snapshot_deduped", map[string]any{
			"hash": hashPrefix(hash),
		})
		loader.ReturnIssuePtrsToPool(pooledRefs)
		return snapshotBuildResult{clearError: true}
	}

	var diff *analysis.IssueDiff
	if prevSnapshot != nil {
		diffValue := analysis.ComputeIssueDiff(prevSnapshot.Issues, issues)
		diff = &diffValue
		w.logEvent(LogLevelDebug, "snapshot_diff", map[string]any{
			"added":              len(diffValue.Added),
			"removed":            len(diffValue.Removed),
			"modified":           len(diffValue.Modified),
			"content_changed":    len(diffValue.ContentChanged),
			"dependency_changed": len(diffValue.DependencyChanged),
			"unchanged":          len(diffValue.Unchanged),
			"total_prev":         len(prevSnapshot.Issues),
			"total_new":          len(issues),
		})
	}

	// Build snapshot (includes Phase 1 analysis) with panic recovery
	var snapshot *DataSnapshot
	analyzeStart := time.Now()
	analyzeErr := w.safeCompute("analyze_phase1", func() error {
		builder := NewSnapshotBuilder(issues, authority).
			WithRecipe(currentRecipe).
			WithWeights(feedbackWeightsForBeadsPath(w.beadsPath)).
			WithBuildConfig(snapshotBuildConfigForTier(tier))
		if prevSnapshot != nil {
			builder.WithPreviousSnapshot(prevSnapshot, diff)
		}
		snapshot = builder.Build()
		return nil
	})

	analyzeDuration := time.Since(analyzeStart)
	if profileSnapshot {
		recordTiming("phase1", analyzeDuration)
	}
	if metricsEnabled {
		w.metrics.lastPhase1Ns.Store(analyzeDuration.Nanoseconds())
	}

	if analyzeErr != nil {
		loader.ReturnIssuePtrsToPool(pooledRefs)
		return snapshotBuildResult{err: analyzeErr}
	}

	// Store hash in snapshot for external access
	if snapshot != nil {
		snapshot.DataHash = hash
		snapshot.AuthorityHash = authorityHash
		snapshot.LoadWarningCount = loadWarningCount
		snapshot.RecipeName = recipeID
		snapshot.RecipeHash = recipeHash
		snapshot.attachPooledIssues(pooledRefs)
		snapshot.DatasetTier = tier
		snapshot.SourceIssueCountHint = sourceLineCount
		snapshot.LoadedOpenOnly = loadOpenOnly
		if loadOpenOnly && sourceLineCount > len(snapshot.Issues) {
			snapshot.TruncatedCount = sourceLineCount - len(snapshot.Issues)
		}
		snapshot.LargeDatasetWarning = largeDatasetWarning(tier, sourceLineCount, len(snapshot.Issues), loadOpenOnly)
	} else {
		loader.ReturnIssuePtrsToPool(pooledRefs)
	}

	if metricsEnabled {
		if snapshot != nil {
			w.metrics.lastSnapshotSizeBytes.Store(estimateSnapshotBytes(snapshot.Issues))
		}
		poolHits, poolMisses := loader.IssuePoolStats()
		w.metrics.poolHits.Store(poolHits)
		w.metrics.poolMisses.Store(poolMisses)

		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		gcPauseDelta := int64(memAfter.PauseTotalNs - memBefore.PauseTotalNs)
		w.metrics.lastGCPauseDeltaNs.Store(gcPauseDelta)
	}

	totalDuration := time.Since(start)
	if profileSnapshot {
		recordTiming("snapshot_total", totalDuration)
	}
	fields := map[string]any{
		"issues":    len(issues),
		"load_ms":   float64(loadDuration.Microseconds()) / 1000.0,
		"phase1_ms": float64(analyzeDuration.Microseconds()) / 1000.0,
		"total_ms":  float64(totalDuration.Microseconds()) / 1000.0,
		"hash":      hashPrefix(hash),
	}
	if metricsEnabled {
		fields["snapshot_bytes"] = w.metrics.lastSnapshotSizeBytes.Load()
		fields["pool_hits"] = w.metrics.poolHits.Load()
		fields["pool_misses"] = w.metrics.poolMisses.Load()
		fields["gc_pause_ms"] = float64(w.metrics.lastGCPauseDeltaNs.Load()) / 1e6
	}
	w.logEvent(LogLevelInfo, "snapshot_built", fields)
	if profileSnapshot && snapshotTimings != nil {
		dbg.Log("worker.snapshot_summary issues=%d total=%s load=%s phase1=%s",
			len(issues),
			formatReloadDuration(snapshotTimings["snapshot_total"]),
			formatReloadDuration(snapshotTimings["load_issues"]),
			formatReloadDuration(snapshotTimings["phase1"]),
		)
	}

	return snapshotBuildResult{snapshot: snapshot, clearError: true}
}

func recipeIncludesClosedStatuses(r *recipe.Recipe) bool {
	if r == nil {
		return false
	}
	for _, s := range r.Filters.Status {
		switch strings.TrimSpace(strings.ToLower(s)) {
		case string(model.StatusClosed), string(model.StatusTombstone):
			return true
		}
	}
	return false
}

func largeDatasetWarning(tier datasetTier, sourceHint, loaded int, openOnly bool) string {
	switch tier {
	case datasetTierLarge:
		n := loaded
		if sourceHint > 0 {
			n = sourceHint
		}
		return fmt.Sprintf("⚠ large %s issues", compactCount(n))
	case datasetTierHuge:
		if openOnly && sourceHint > 0 {
			return fmt.Sprintf("⚠ huge open-only %s/%s", compactCount(loaded), compactCount(sourceHint))
		}
		n := loaded
		if sourceHint > 0 {
			n = sourceHint
		}
		return fmt.Sprintf("⚠ huge %s issues", compactCount(n))
	default:
		return ""
	}
}

func countJSONLLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	const bufSize = 32 * 1024
	buf := make([]byte, bufSize)
	lines := 0
	sawAny := false
	lastByte := byte(0)

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			sawAny = true
			lastByte = buf[n-1]
			for _, b := range buf[:n] {
				if b == '\n' {
					lines++
				}
			}
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			break
		}
		return 0, readErr
	}

	if sawAny && lastByte != '\n' {
		lines++
	}
	return lines, nil
}

func envMaxLineSizeBytes() int {
	// One definition for every loading path (TUI, background worker, robot).
	return loader.MaxLineSizeFromEnv()
}

func envBool(name string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if v == "" {
		return false
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func envPositiveInt(name string) (int, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func envDurationSeconds(name string, fallback time.Duration) time.Duration {
	n, ok := envPositiveInt(name)
	if !ok {
		return fallback
	}
	return time.Duration(n) * time.Second
}

func envDurationMilliseconds(name string, fallback time.Duration) time.Duration {
	n, ok := envPositiveInt(name)
	if !ok {
		return fallback
	}
	return time.Duration(n) * time.Millisecond
}

// runPhase2Analysis waits for Phase 2 analysis to complete and notifies the UI.
// This runs in a goroutine so it doesn't block snapshot delivery.
func (w *BackgroundWorker) runPhase2Analysis(snapshot *DataSnapshot, snapshotVer, workerGeneration uint64) {
	if snapshot == nil || snapshot.Analysis == nil {
		return
	}
	stats := snapshot.Analysis
	dataHash := snapshot.DataHash

	// Wait for Phase 2 to complete (blocking)
	phase2Start := time.Now()
	stats.WaitForPhase2()
	phase2Duration := time.Since(phase2Start)
	if w.metricsEnabled {
		w.metrics.lastPhase2Ns.Store(phase2Duration.Nanoseconds())
	}
	if dbg.Enabled() {
		dbg.LogTiming("worker.phase2", phase2Duration)
	}

	// Check if this Phase 2 completion still corresponds to the active snapshot.
	w.mu.RLock()
	stopped := w.state == WorkerStopped
	current := w.snapshot
	currentGeneration := w.generation
	w.mu.RUnlock()

	if stopped || current != snapshot || currentGeneration != workerGeneration {
		w.logEvent(LogLevelDebug, "phase2_skip", map[string]any{
			"hash": hashPrefix(dataHash),
		})
		return
	}
	w.logEvent(LogLevelInfo, "phase2_complete", map[string]any{
		"hash":      hashPrefix(dataHash),
		"phase2_ms": float64(phase2Duration.Microseconds()) / 1000.0,
	})

	// Notify UI that Phase 2 metrics are ready
	w.send(Phase2UpdateMsg{
		DataHash:         dataHash,
		Stats:            stats,
		Snapshot:         snapshot,
		SnapshotVer:      snapshotVer,
		WorkerGeneration: workerGeneration,
	})
}

// SnapshotReadyMsg is sent to the UI when a new snapshot is ready.
type SnapshotReadyMsg struct {
	Snapshot         *DataSnapshot
	FileChangeAt     time.Time
	SentAt           time.Time
	SnapshotVer      uint64
	WorkerGeneration uint64
	QueueDepth       int64
	CoalesceCount    int64
}

// SnapshotErrorMsg is sent to the UI when snapshot building fails.
type SnapshotErrorMsg struct {
	Err              error
	Recoverable      bool // True if we expect to recover on next file change
	WorkerGeneration uint64
}

// Phase2UpdateMsg is sent when Phase 2 analysis completes.
// This allows the UI to update without waiting for full rebuild.
// The UI should check DataHash matches current snapshot before using.
type Phase2UpdateMsg struct {
	DataHash         string // Content hash to verify this matches current snapshot
	Stats            *analysis.GraphStats
	Snapshot         *DataSnapshot
	SnapshotVer      uint64
	WorkerGeneration uint64
}

// RefreshRequestMsg asks the BackgroundWorker to reload data. Force bypasses
// content-hash deduplication; Recipe applies new background view configuration.
// A nil Recipe keeps the worker's current recipe.
type RefreshRequestMsg struct {
	Force  bool
	Recipe *recipe.Recipe
}

func (w *BackgroundWorker) send(msg tea.Msg) {
	if w == nil || msg == nil {
		return
	}

	w.sendMu.Lock()
	defer w.sendMu.Unlock()

	// Reject invalidated work before it consumes queue capacity. The UI repeats
	// this fence because recovery can advance the generation immediately after
	// this check.
	if !w.workerMessageIsCurrent(msg) {
		w.releaseDroppedMessage(msg)
		return
	}

	select {
	case w.msgCh <- msg:
		return
	case <-w.ctx.Done():
		w.releaseDroppedMessage(msg)
		return
	default:
	}

	// The one-slot mailbox is full. Serialize the remove/compare/replace
	// operation so a lower-priority error or Phase 2 update cannot evict the
	// authoritative SnapshotReady message.
	var queued tea.Msg
	select {
	case queued = <-w.msgCh:
	case <-w.ctx.Done():
		w.releaseDroppedMessage(msg)
		return
	default:
		// The UI waiter made room after the failed send; retry once through the
		// normal path while still holding sendMu.
		if !w.workerMessageIsCurrent(msg) {
			w.releaseDroppedMessage(msg)
			return
		}
		select {
		case w.msgCh <- msg:
		case <-w.ctx.Done():
			w.releaseDroppedMessage(msg)
		}
		return
	}

	if w.workerMessageIsCurrent(queued) && workerMessagePriority(queued) > workerMessagePriority(msg) {
		select {
		case w.msgCh <- queued:
		case <-w.ctx.Done():
			w.releaseDroppedMessage(queued)
		}
		w.releaseDroppedMessage(msg)
		return
	}

	w.releaseDroppedMessage(queued)
	select {
	case w.msgCh <- msg:
	case <-w.ctx.Done():
		w.releaseDroppedMessage(msg)
	}
}

func workerMessagePriority(msg tea.Msg) int {
	switch typed := msg.(type) {
	case SnapshotReadyMsg:
		return 2
	case SnapshotErrorMsg:
		// A recoverable error may be superseded by the last usable snapshot. A
		// terminal error must win, because it is the UI's signal to detach the
		// stopped worker and fall back to synchronous refreshes.
		if !typed.Recoverable {
			return 3
		}
		return 1
	case Phase2UpdateMsg:
		return 0
	default:
		return 1
	}
}

func workerMessageGeneration(msg tea.Msg) (uint64, bool) {
	switch typed := msg.(type) {
	case SnapshotReadyMsg:
		return typed.WorkerGeneration, true
	case SnapshotErrorMsg:
		return typed.WorkerGeneration, true
	case Phase2UpdateMsg:
		return typed.WorkerGeneration, true
	default:
		return 0, false
	}
}

func (w *BackgroundWorker) workerMessageIsCurrent(msg tea.Msg) bool {
	generation, tagged := workerMessageGeneration(msg)
	// Generation zero denotes a manually constructed or non-worker message and
	// preserves direct Model.Update/test compatibility.
	return !tagged || generation == 0 || generation == w.Generation()
}

func (w *BackgroundWorker) releaseDroppedMessage(msg tea.Msg) {
	ready, ok := msg.(SnapshotReadyMsg)
	if !ok || ready.Snapshot == nil {
		return
	}
	w.mu.RLock()
	current := w.snapshot
	w.mu.RUnlock()
	// The worker retains its current snapshot until replacement/Stop. Older
	// dropped messages have no remaining owner unless the UI already installed
	// them, whose release path shares the same one-shot lease.
	if ready.Snapshot != current {
		ready.Snapshot.releasePooledIssues()
	}
}

// WatcherChanged returns the watcher's change notification channel.
// This is useful for integration with existing code.
func (w *BackgroundWorker) WatcherChanged() <-chan struct{} {
	if w.watcher == nil {
		return nil
	}
	return w.watcher.Changed()
}

// LastHash returns the content hash from the last successful snapshot build.
// Useful for testing and debugging.
func (w *BackgroundWorker) LastHash() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastHash
}

func estimateSnapshotBytes(issues []model.Issue) int64 {
	const (
		baseIssueBytes      = 256
		baseDependencyBytes = 64
		baseCommentBytes    = 128
		baseLabelBytes      = 16
	)

	var total int64
	for i := range issues {
		issue := &issues[i]
		total += baseIssueBytes
		total += int64(len(issue.ID) + len(issue.Title) + len(issue.Description) + len(issue.Design) +
			len(issue.AcceptanceCriteria) + len(issue.Notes) + len(issue.Assignee) + len(issue.SourceRepo))
		if issue.ExternalRef != nil {
			total += int64(len(*issue.ExternalRef))
		}

		for _, label := range issue.Labels {
			total += baseLabelBytes + int64(len(label))
		}
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			total += baseDependencyBytes + int64(len(dep.IssueID)+len(dep.DependsOnID)+len(dep.CreatedBy))
		}
		for _, c := range issue.Comments {
			if c == nil {
				continue
			}
			total += baseCommentBytes + int64(len(c.Text)+len(c.Author))
		}
	}

	return total
}

// hashPrefix returns a safe prefix of the hash for logging.
// Returns up to 16 characters, or the full hash if shorter.
func hashPrefix(hash string) string {
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// ResetHash clears the stored content hash, forcing the next buildSnapshot
// to process even if content is unchanged. Useful for testing.
func (w *BackgroundWorker) ResetHash() {
	w.mu.Lock()
	w.lastHash = ""
	w.mu.Unlock()
}
