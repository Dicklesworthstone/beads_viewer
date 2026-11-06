package hooks

import (
	"errors"

	"golang.org/x/sys/unix"
)

// Linux delivers a group signal successfully while exited members are still
// unreaped, so EPERM there always comes from a live member that refused it.
func hookGroupHasOnlyExitedMembers(int) bool { return false }

// WNOWAIT leaves the child's status and PID reserved until Cmd.Wait reaps it.
// See waitid(2): https://man7.org/linux/man-pages/man2/waitid.2.html.
func waitHookCommandExit(pid int) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return err
	}
}
