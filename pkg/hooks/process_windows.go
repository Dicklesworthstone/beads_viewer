//go:build windows

package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cmd.exe does not use the CommandLineToArgvW quoting rules used by os/exec.
// /D suppresses AutoRun; /S removes only our outer command wrapper. The command
// itself is the exact text covered by the invoking user's approval digest.
func configureHookShell(cmd *exec.Cmd, command string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: syscall.EscapeArg(cmd.Path) + ` /D /S /C "` + command + `"`,
	}
}

// runHookCommand assigns the initially suspended process to a private Job
// Object before any hook code can run. Assigning an already running process
// would allow it to spawn an unowned child in the startup window.
func runHookCommand(ctx context.Context, cmd *exec.Cmd) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.WaitDelay <= 0 {
		return errors.New("hook process requires a bounded pipe wait")
	}
	if cmd.Process != nil {
		return errors.New("hook process has already been started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating hook process job: %w", err)
	}
	defer func() {
		if err := windows.CloseHandle(job); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("closing hook process job: %w", err))
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// This x/sys wrapper accepts uintptr rather than a typed pointer. Pin the
	// structure across lazy DLL resolution and the native call, so stack growth
	// or garbage collection cannot invalidate the converted address.
	var pin runtime.Pinner
	pin.Pin(&limits)
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	pin.Unpin()
	if err != nil {
		return fmt.Errorf("configuring hook process job: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED

	// CommandContext's cancellation goroutine starts inside Start. Serialize it
	// with job assignment and resume, so cancellation during setup can never be
	// followed by starting an uncontained hook.
	var mu sync.Mutex
	var assigned, canceled, finished bool
	terminate := func() error {
		if assigned {
			return windows.TerminateJobObject(job, 1)
		}
		return cmd.Process.Kill()
	}
	cmd.Cancel = func() error {
		mu.Lock()
		defer mu.Unlock()
		if finished {
			return os.ErrProcessDone
		}
		canceled = true
		return terminate()
	}
	if err := cmd.Start(); err != nil {
		mu.Lock()
		finished = true
		mu.Unlock()
		return err
	}

	mu.Lock()
	var setupErr error
	var process windows.Handle
	if canceled || ctx.Err() != nil {
		setupErr = ctx.Err()
		if setupErr == nil {
			setupErr = context.Canceled
		}
	} else {
		process, err = windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
			false, uint32(cmd.Process.Pid))
		if err != nil {
			setupErr = fmt.Errorf("opening suspended hook process: %w", err)
		} else {
			defer windows.CloseHandle(process)
			if err := windows.AssignProcessToJobObject(job, process); err != nil {
				setupErr = fmt.Errorf("containing hook process before execution: %w", err)
			} else {
				assigned = true
			}
		}
		if setupErr == nil {
			if err := ctx.Err(); err != nil {
				setupErr = err
			} else if err := resumeSuspendedHook(uint32(cmd.Process.Pid)); err != nil {
				setupErr = fmt.Errorf("resuming contained hook process: %w", err)
			}
		}
	}
	if setupErr != nil {
		if err := terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			setupErr = errors.Join(setupErr, fmt.Errorf("terminating suspended hook process: %w", err))
		}
	}
	mu.Unlock()

	// Observe the shell's exit before os/exec waits for pipe EOF. Background
	// descendants may still hold those pipes, so kill the job before draining.
	var observeErr error
	if setupErr == nil {
		event, err := windows.WaitForSingleObject(process, windows.INFINITE)
		if err != nil {
			observeErr = fmt.Errorf("waiting for hook process: %w", err)
		} else if event != windows.WAIT_OBJECT_0 {
			observeErr = fmt.Errorf("unexpected hook process wait status: %d", event)
		}
	}
	mu.Lock()
	finished = true
	// This also terminates background descendants whose shell exited normally
	// and whose redirected output would otherwise hide their continued life.
	var cleanupErr error
	if assigned {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			cleanupErr = fmt.Errorf("terminating hook descendants: %w", err)
		}
	}
	mu.Unlock()
	waitErr := cmd.Wait()
	return errors.Join(ctx.Err(), setupErr, observeErr, waitErr, cleanupErr)
}

var getHookThreadProcessID = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

// Go closes the primary thread handle returned by CreateProcess. Recover it
// while the process is still suspended. Fail closed if its primary thread
// cannot be identified unambiguously; never resume an arbitrary thread.
func resumeSuspendedHook(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	var threadID uint32
	for err == nil {
		if entry.OwnerProcessID == processID {
			if threadID != 0 {
				return errors.New("suspended hook process has more than one thread")
			}
			threadID = entry.ThreadID
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = windows.Thread32Next(snapshot, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if threadID == 0 {
		return errors.New("suspended hook process has no primary thread")
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION,
		false, threadID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(thread)
	// Validate after opening: a snapshot alone must never let a recycled thread
	// ID cause ResumeThread to act on a thread belonging to another process.
	owner, _, callErr := getHookThreadProcessID.Call(uintptr(thread))
	if owner == 0 {
		return fmt.Errorf("checking hook thread owner: %w", callErr)
	}
	if uint32(owner) != processID {
		return errors.New("hook thread owner changed before resume")
	}
	previous, err := windows.ResumeThread(thread)
	if err != nil {
		return err
	}
	if previous != 1 {
		return fmt.Errorf("unexpected hook thread suspend count: %d", previous)
	}
	return nil
}
