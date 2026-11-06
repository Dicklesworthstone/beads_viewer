package hooks

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// Process state and flag from Darwin's <sys/proc.h>.
const (
	darwinProcessZombie  = 5      // SZOMB: awaiting collection by its parent
	darwinProcessExiting = 0x2000 // P_WEXIT: working on exiting
)

// hookGroupHasOnlyExitedMembers reports whether every process still in the
// group has exited or is irrevocably exiting. Darwin's kill(2) fails with
// EPERM, not ESRCH, when a process group's only members are exited but
// unreaped, which is exactly the state of a hook whose leader finished without
// leaving descendants: the leader is kept unreaped on purpose until group
// cleanup. NOTE_EXIT can arrive while the leader is still marked P_WEXIT and
// not yet SZOMB, so both count as exited. A live member that refused the
// signal, or a failed lookup, keeps the original error.
func hookGroupHasOnlyExitedMembers(pgid int) bool {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	if err != nil {
		return false
	}
	for i := range procs {
		proc := &procs[i].Proc
		if proc.P_stat != darwinProcessZombie && proc.P_flag&darwinProcessExiting == 0 {
			return false
		}
	}
	return true
}

// kqueue observes exit without consuming the child's wait status. Darwin's
// filt_procattach looks up live processes and reports ESRCH for a child already
// on the zombie list. We exclusively own this unreaped child, so ESRCH during
// registration also means its exit has been observed and its PID is reserved.
// Sources: Apple's kqueue(2) manual and XNU bsd/kern/kern_event.c filt_procattach.
func waitHookCommandExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	unix.CloseOnExec(kq)
	changes := []unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(kq, changes, events[:], nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("expected a hook process exit event, received %d events", n)
		}
		event := events[0]
		if event.Flags&unix.EV_ERROR != 0 {
			if unix.Errno(event.Data) == unix.ESRCH {
				return nil
			}
			return fmt.Errorf("registering hook process exit event: %w", unix.Errno(event.Data))
		}
		if event.Ident != uint64(pid) || event.Filter != unix.EVFILT_PROC || event.Fflags&unix.NOTE_EXIT == 0 {
			return errors.New("unexpected hook process exit event")
		}
		return nil
	}
}
