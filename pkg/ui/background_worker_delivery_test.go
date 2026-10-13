package ui

import (
	"context"
	"errors"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func deliveryWorker(t *testing.T) *BackgroundWorker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &BackgroundWorker{
		ctx: ctx, cancel: cancel, generation: 1, state: WorkerIdle,
		msgCh: make(chan tea.Msg, backgroundWorkerMessageBuffer),
	}
}

func installDeliverySnapshot(w *BackgroundWorker, snapshot *DataSnapshot, version uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snapshot = snapshot
	w.metrics.snapshotVersion.Store(version)
}

// Processing publishes its snapshot and returns to Idle before it sends the
// UI message. A delayed old sender may therefore run after the next build has
// already queued its result. The one-slot mailbox must keep the newer result,
// including a forced same-hash refresh after recovery or a recipe change.
func TestWorkerMailboxRejectsSupersededSnapshots(t *testing.T) {
	for _, sameHash := range []bool{false, true} {
		for _, occupied := range []bool{false, true} {
			w := deliveryWorker(t)
			old := &DataSnapshot{DataHash: "old"}
			latest := &DataSnapshot{DataHash: "latest"}
			if sameHash {
				old.DataHash = latest.DataHash
			}
			installDeliverySnapshot(w, latest, 2)
			fresh := SnapshotReadyMsg{Snapshot: latest, SnapshotVer: 2, WorkerGeneration: 1}
			if occupied {
				w.send(fresh)
			}
			w.send(SnapshotReadyMsg{Snapshot: old, SnapshotVer: 1, WorkerGeneration: 1})
			select {
			case got := <-w.msgCh:
				ready, ok := got.(SnapshotReadyMsg)
				if !occupied || !ok || ready.Snapshot != latest || ready.SnapshotVer != 2 {
					t.Fatalf("delayed snapshot displaced current state (same hash=%v occupied=%v): %#v", sameHash, occupied, got)
				}
			default:
				if occupied {
					t.Fatal("superseded delivery consumed the latest queued snapshot")
				}
			}
			// Even a wrong pointer bearing the current version must not gain
			// authority from a matching data hash or lifecycle generation.
			w.send(SnapshotReadyMsg{Snapshot: old, SnapshotVer: 2, WorkerGeneration: 1})
			select {
			case got := <-w.msgCh:
				t.Fatalf("foreign snapshot borrowed the latest version: %#v", got)
			default:
			}
			w.send(fresh)
			if got := (<-w.msgCh).(SnapshotReadyMsg); got.Snapshot != latest {
				t.Fatal("current snapshot became undeliverable")
			}
		}
	}
}

func TestWorkerMailboxRejectsSupersededPhaseTwo(t *testing.T) {
	w := deliveryWorker(t)
	old := &DataSnapshot{DataHash: "same"}
	latest := &DataSnapshot{DataHash: "same"}
	installDeliverySnapshot(w, latest, 2)
	fresh := Phase2UpdateMsg{Snapshot: latest, SnapshotVer: 2, WorkerGeneration: 1, DataHash: "same"}
	w.send(fresh)
	w.send(Phase2UpdateMsg{Snapshot: old, SnapshotVer: 1, WorkerGeneration: 1, DataHash: "same"})
	got, ok := (<-w.msgCh).(Phase2UpdateMsg)
	if !ok || got.Snapshot != latest || got.SnapshotVer != 2 {
		t.Fatalf("late phase two evicted current metrics: %#v", got)
	}
	w.send(Phase2UpdateMsg{Snapshot: old, SnapshotVer: 2, WorkerGeneration: 1})
	select {
	case got := <-w.msgCh:
		t.Fatalf("phase two borrowed another snapshot's version: %#v", got)
	default:
	}
}

func TestWorkerMailboxRejectsRecoveredOrSupersededErrors(t *testing.T) {
	w := deliveryWorker(t)
	old := &WorkerError{Phase: "load", Cause: errors.New("old lock"), Retries: 1}
	latest := &WorkerError{Phase: "load", Cause: errors.New("current lock"), Retries: 2}
	w.lastError = latest
	w.send(SnapshotErrorMsg{Err: latest, Recoverable: true, WorkerGeneration: 1})
	w.send(SnapshotErrorMsg{Err: old, Recoverable: true, WorkerGeneration: 1})
	got, ok := (<-w.msgCh).(SnapshotErrorMsg)
	if !ok || got.Err != latest {
		t.Fatalf("late failure evicted the current diagnosis: %#v", got)
	}
	// The UI already received the success. No ready message remains queued
	// to mask the late error using priority alone.
	w.lastError = nil
	w.send(SnapshotErrorMsg{Err: latest, Recoverable: true, WorkerGeneration: 1})
	select {
	case got := <-w.msgCh:
		t.Fatalf("successful recovery was followed by an obsolete failure: %#v", got)
	default:
	}
}

func TestWorkerMailboxKeepsCurrentPriorityAndLegacyMessages(t *testing.T) {
	w := deliveryWorker(t)
	latest := &DataSnapshot{DataHash: "current"}
	installDeliverySnapshot(w, latest, 3)
	ready := SnapshotReadyMsg{Snapshot: latest, SnapshotVer: 3, WorkerGeneration: 1}
	failure := &WorkerError{Phase: "load", Cause: errors.New("current load failure")}
	w.lastError = failure
	w.send(ready)
	w.send(SnapshotErrorMsg{Err: failure, Recoverable: true, WorkerGeneration: 1})
	w.send(Phase2UpdateMsg{Snapshot: latest, SnapshotVer: 3, WorkerGeneration: 1})
	if got, ok := (<-w.msgCh).(SnapshotReadyMsg); !ok || got.Snapshot != latest {
		t.Fatalf("current ready lost its priority: %#v", got)
	}
	w.send(ready)
	terminal := SnapshotErrorMsg{Err: errors.New("worker stopped"), WorkerGeneration: 1}
	w.send(terminal)
	if got, ok := (<-w.msgCh).(SnapshotErrorMsg); !ok || got.Err != terminal.Err || got.Recoverable {
		t.Fatalf("terminal detach signal lost its priority: %#v", got)
	}
	for _, legacy := range []tea.Msg{
		SnapshotReadyMsg{}, Phase2UpdateMsg{}, SnapshotErrorMsg{Recoverable: true}, "unversioned",
	} {
		if !w.workerMessageIsCurrent(legacy) {
			t.Fatalf("manual untagged message was rejected: %#v", legacy)
		}
	}
	if w.workerMessageIsCurrent(SnapshotReadyMsg{Snapshot: latest, SnapshotVer: 3, WorkerGeneration: 2}) {
		t.Fatal("snapshot bypassed lifecycle generation fencing")
	}
}

func TestWorkerMailboxConcurrentOldSendersCannotLoseLatest(t *testing.T) {
	w := deliveryWorker(t)
	latest := &DataSnapshot{DataHash: "latest"}
	installDeliverySnapshot(w, latest, 100)
	fresh := SnapshotReadyMsg{Snapshot: latest, SnapshotVer: 100, WorkerGeneration: 1}
	w.send(fresh)
	var senders sync.WaitGroup
	for i := 1; i < 100; i++ {
		senders.Add(1)
		go func(version uint64) {
			defer senders.Done()
			old := &DataSnapshot{DataHash: "old"}
			w.send(SnapshotReadyMsg{Snapshot: old, SnapshotVer: version, WorkerGeneration: 1})
		}(uint64(i))
	}
	senders.Wait()
	got, ok := (<-w.msgCh).(SnapshotReadyMsg)
	if !ok || got.Snapshot != latest || got.SnapshotVer != 100 {
		t.Fatalf("late producer lost the only latest notification: %#v", got)
	}
}

// Existing direct mailbox callers can supply versions before the worker has
// published a build. Preserve their priority contract without letting an older
// version through once the real publication watermark exists.
func TestWorkerMailboxBeforeFirstBuildPreservesPriority(t *testing.T) {
	w := deliveryWorker(t)
	snapshot := &DataSnapshot{DataHash: "manual"}
	ready := SnapshotReadyMsg{Snapshot: snapshot, SnapshotVer: 7, WorkerGeneration: 1}
	w.send(ready)
	w.send(SnapshotErrorMsg{Err: errors.New("generic reload error"), Recoverable: true, WorkerGeneration: 1})
	got, ok := (<-w.msgCh).(SnapshotReadyMsg)
	if !ok || got.Snapshot != snapshot {
		t.Fatalf("uninitialized mailbox lost ready priority: %#v", got)
	}
	w.send(SnapshotErrorMsg{Err: errors.New("generic reload error"), Recoverable: true, WorkerGeneration: 1})
	w.send(ready)
	if got, ok := (<-w.msgCh).(SnapshotReadyMsg); !ok || got.Snapshot != snapshot {
		t.Fatalf("uninitialized mailbox rejected recovery: %#v", got)
	}
}
