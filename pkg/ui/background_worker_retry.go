package ui

import (
	"context"
	"time"
)

// Load failures need a retry even when the source bytes do not change: a
// SQLite lock or WAL-index recovery can finish without another watched event.
// The existing heartbeat services these delays; no timer/goroutine is retained
// per error. Eligibility is 5s, 10s, 20s, then 30s after the last failure, and
// the next heartbeat starts the attempt. File events and manual refreshes do
// not wait for this backoff. Analysis failures are not automatically retried.
func loadRetryDelay(failures int) time.Duration {
	delay := 5 * time.Second
	for failures > 1 && delay < 30*time.Second {
		delay = min(2*delay, 30*time.Second)
		failures--
	}
	return delay
}

// A queued retry belongs to one exact failure and process-loop lifetime.
// Rechecking it under the processing lock prevents a delayed goroutine from
// stealing a newer run's scheduled flag or force-refresh request.
type loadRetryAttempt struct {
	loop       context.Context
	generation uint64
	failure    *WorkerError
}

func (w *BackgroundWorker) reserveLoadRetry(loopCtx context.Context, now time.Time) *loadRetryAttempt {
	w.mu.Lock()
	defer w.mu.Unlock()
	if loopCtx == nil || w.loopCtx != loopCtx || loopCtx.Err() != nil ||
		!w.started || w.recovering || w.state != WorkerIdle || w.processScheduled ||
		w.lastError == nil || w.lastError.Phase != "load" || w.lastError.Time.IsZero() ||
		now.Sub(w.lastError.Time) < loadRetryDelay(w.errorCount) {
		return nil
	}
	attempt := &loadRetryAttempt{loop: loopCtx, generation: w.generation, failure: w.lastError}
	w.processScheduled = true
	return attempt
}

// Called only while w.mu is held, immediately before entering WorkerProcessing.
func (a *loadRetryAttempt) validLocked(w *BackgroundWorker) bool {
	return a.loop != nil && a.loop == w.loopCtx && a.loop.Err() == nil &&
		w.started && !w.recovering && w.state == WorkerIdle && w.processScheduled &&
		a.generation == w.generation && a.failure != nil && a.failure == w.lastError
}

func (w *BackgroundWorker) retryFailedLoad(loopCtx context.Context, now time.Time) {
	if attempt := w.reserveLoadRetry(loopCtx, now); attempt != nil {
		go w.processSnapshotContext(w.buildSnapshotResultContext, attempt)
	}
}
