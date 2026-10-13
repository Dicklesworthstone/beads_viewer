// Package watcher provides file watching with debouncing and fallback polling.
package watcher

import (
	"sync"
	"time"
)

// DefaultDebounceDuration is the default debounce window.
const DefaultDebounceDuration = 250 * time.Millisecond

// DefaultMaxDebounceWait bounds coalescing under continuous writes. A longer
// explicitly requested debounce duration is respected instead. This is a timer
// deadline, not a guarantee about callback execution or consumer load latency.
const DefaultMaxDebounceWait = time.Second

// Debouncer coalesces rapid events into the latest callback. It fires after a
// quiet debounce window or the maximum wait from the first pending event,
// whichever comes first. Busy WAL writers therefore cannot starve live refresh.
type Debouncer struct {
	duration time.Duration
	timer    *time.Timer
	deadline *time.Timer
	callback func()
	mu       sync.Mutex
	seq      uint64
	burst    uint64
}

// NewDebouncer creates a new Debouncer with the specified duration.
// If duration is 0, DefaultDebounceDuration is used.
func NewDebouncer(duration time.Duration) *Debouncer {
	if duration == 0 {
		duration = DefaultDebounceDuration
	}
	return &Debouncer{
		duration: duration,
	}
}

// Trigger replaces the pending callback and restarts the quiet-window timer,
// but never postpones this burst's maximum-wait timer. After either timer wins,
// another Trigger starts a new burst, including while a callback is running.
func (d *Debouncer) Trigger(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	seq := d.seq
	d.callback = callback

	if d.deadline == nil {
		d.burst++
		burst := d.burst
		d.deadline = time.AfterFunc(max(d.duration, DefaultMaxDebounceWait), func() {
			d.fire(burst, 0, true)
		})
	}
	burst := d.burst
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.duration, func() {
		d.fire(burst, seq, false)
	})
}

// fire arbitrates the quiet and deadline timers under one lock. A deadline is
// bound to the burst, not seq: retriggering cannot invalidate an expired deadline
// whose goroutine has not run yet. Quiet timers additionally check seq so an old
// timer that raced Stop cannot deliver a callback before the new quiet window.
func (d *Debouncer) fire(burst, seq uint64, deadline bool) {
	d.mu.Lock()
	if d.deadline == nil || burst != d.burst || (!deadline && seq != d.seq) {
		d.mu.Unlock()
		return
	}
	callback := d.callback
	d.cancelLocked()
	d.mu.Unlock()

	// No user code under mu: watcher callbacks may Stop, Cancel or Trigger.
	callback()
}

// cancelLocked releases both timers and invalidates callbacks already queued
// by either timer. It cannot cancel user code that fire has already admitted.
func (d *Debouncer) cancelLocked() {
	d.burst++
	d.seq++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.deadline != nil {
		d.deadline.Stop()
		d.deadline = nil
	}
	d.callback = nil
}

// Cancel cancels both pending timers. A callback already admitted by fire may
// finish; it executes outside the lock and may itself re-enter the debouncer.
func (d *Debouncer) Cancel() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cancelLocked()
}

// Duration returns the debounce duration.
func (d *Debouncer) Duration() time.Duration {
	return d.duration
}
