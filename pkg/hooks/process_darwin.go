package hooks

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

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
