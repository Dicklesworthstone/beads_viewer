package watcher

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A busy repository can commit more often than the quiet window for minutes.
// Require refreshes while the event producer is STILL active, not merely one
// trailing callback once it stops. Exercise the real timer and Trigger path.
func TestDebouncerContinuousEventsDoNotStarveRefresh(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(200 * time.Millisecond)
	defer d.Cancel()
	calls := make(chan int, 512)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	stop := time.NewTimer(2500 * time.Millisecond)
	defer stop.Stop()
	started := time.Now()
	last, delivered, periodic := 0, 0, 0
	for producing := true; producing; {
		select {
		case <-ticker.C:
			last++
			version := last
			d.Trigger(func() { calls <- version })
		case version := <-calls:
			if version <= delivered {
				t.Fatalf("duplicate or backwards refresh: %d after %d", version, delivered)
			}
			delivered = version
			periodic++
			t.Logf("refresh during writes: version=%d elapsed=%v", version, time.Since(started))
		case <-stop.C:
			producing = false
		}
	}
	if periodic < 2 {
		t.Errorf("continuous writes starved refresh: got %d periodic callbacks, want at least 2", periodic)
	}
	// The update after the last periodic notification must not disappear when
	// writes stop. The final quiet window delivers the latest callback, not the
	// callback captured when that burst's maximum-wait timer was first created.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for delivered != last {
		select {
		case version := <-calls:
			if version <= delivered {
				t.Fatalf("duplicate or backwards final refresh: %d after %d", version, delivered)
			}
			delivered = version
		case <-deadline.C:
			t.Fatalf("last committed version lost: delivered=%d latest=%d", delivered, last)
		}
	}
	t.Logf("final refresh: version=%d elapsed=%v", delivered, time.Since(started))
}

func TestDebouncerQuietBurstDeliversLatestOnly(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(100 * time.Millisecond)
	defer d.Cancel()
	calls := make(chan int, 32)
	for version := 1; version <= 10; version++ {
		d.Trigger(func() { calls <- version })
	}
	select {
	case version := <-calls:
		if version != 10 {
			t.Fatalf("quiet burst delivered %d, want latest callback 10", version)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quiet burst was not delivered")
	}
	// A deadline that lost to the quiet timer must not produce a duplicate.
	select {
	case version := <-calls:
		t.Fatalf("quiet burst delivered another callback: %d", version)
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestDebouncerCancelDisarmsMaximumWaitAndCanRestart(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(200 * time.Millisecond)
	defer d.Cancel()
	calls := make(chan string, 32)
	for i := 0; i < 10; i++ {
		d.Trigger(func() { calls <- "cancelled" })
		time.Sleep(5 * time.Millisecond)
	}
	d.Cancel()
	// Wait beyond BOTH timers, not just the short quiet window.
	select {
	case got := <-calls:
		t.Fatalf("cancelled burst fired: %q", got)
	case <-time.After(1100 * time.Millisecond):
	}
	d.Trigger(func() { calls <- "new burst" })
	select {
	case got := <-calls:
		if got != "new burst" {
			t.Fatalf("old burst crossed Cancel/Trigger: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled debouncer could not be reused")
	}
}

func TestDebouncerCallbackCanCancelAndRetrigger(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(time.Millisecond)
	defer d.Cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	var callback func()
	callback = func() {
		d.Cancel()
		if calls.Add(1) == 20 {
			close(done)
			return
		}
		d.Trigger(callback)
	}
	d.Trigger(callback)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("reentrant callback deadlocked after %d calls", calls.Load())
	}
}

func TestDebouncerConcurrentCancelTriggerKeepsFinalEvent(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(5 * time.Millisecond)
	defer d.Cancel()
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 1000; i++ {
				d.Trigger(func() {})
				if i%3 == 0 {
					d.Cancel()
				}
			}
		}()
	}
	workers.Wait()
	d.Cancel()
	final := make(chan struct{}, 16)
	d.Trigger(func() { final <- struct{}{} })
	select {
	case <-final:
	case <-time.After(3 * time.Second):
		t.Fatal("racing Trigger/Cancel lost the final refresh")
	}
	select {
	case <-final:
		t.Fatal("an old timer delivered the final callback again")
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestDebouncerRespectsLongerRequestedQuietWindow(t *testing.T) {
	t.Parallel()
	d := NewDebouncer(1250 * time.Millisecond)
	defer d.Cancel()
	called := make(chan struct{}, 4)
	d.Trigger(func() { called <- struct{}{} })
	select {
	case <-called:
		t.Fatal("the one-second maximum shortened an explicitly longer quiet window")
	case <-time.After(1050 * time.Millisecond):
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("long quiet window was not delivered")
	}
	// Here the quiet/deadline timers expire together. Only one may win.
	select {
	case <-called:
		t.Fatal("simultaneous quiet/deadline timers both delivered")
	case <-time.After(100 * time.Millisecond):
	}
}
