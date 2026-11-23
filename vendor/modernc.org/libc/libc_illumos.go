// Copyright 2020 The Libc Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package libc // import "modernc.org/libc"

import (
	// "encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	// "runtime/debug"
	"time"
	"unsafe"

	guuid "github.com/google/uuid"
	"golang.org/x/sys/unix"
	"modernc.org/libc/errno"
	"modernc.org/libc/fcntl"
	"modernc.org/libc/fts"
	gonetdb "modernc.org/libc/honnef.co/go/netdb"
	"modernc.org/libc/langinfo"
	"modernc.org/libc/limits"
	"modernc.org/libc/netdb"
	"modernc.org/libc/netinet/in"
	"modernc.org/libc/pthread"
	"modernc.org/libc/signal"
	"modernc.org/libc/stdio"
	"modernc.org/libc/sys/socket"
	"modernc.org/libc/sys/stat"
	"modernc.org/libc/sys/types"
	"modernc.org/libc/termios"
	ctime "modernc.org/libc/time"
	"modernc.org/libc/unistd"
	"modernc.org/libc/uuid/uuid"
)

var (
	in6_addr_any in.In6_addr
	_            = X__ctype_b_loc
)

// The system calls write struct stat into the buffers of the C code, which
// are laid out per modernc.org/libc/sys/stat. Some helpers use unix.Stat_t.
var (
	_ [unsafe.Sizeof(stat.Stat{}) - unsafe.Sizeof(unix.Stat_t{})]byte
	_ [unsafe.Sizeof(unix.Stat_t{}) - unsafe.Sizeof(stat.Stat{})]byte
)

type (
	syscallErrno = unix.Errno
	long         = int64
	ulong        = uint64
)

type file uintptr

type Tsize_t = types.Size_t

// The LP64 illumos FILE is opaque, long __pad[16]: all access goes through
// functions. The hand-written stdio keeps its own state in it.
type fileState struct {
	fd    int32
	flags int32
}

const (
	fileErr = 1 << iota
	fileEOF
)

func (f file) state() *fileState { return (*fileState)(unsafe.Pointer(f)) }
func (f file) fd() int32         { return f.state().fd }
func (f file) setFd(fd int32)    { f.state().fd = fd }
func (f file) err() bool         { return f.state().flags&fileErr != 0 }
func (f file) setErr()           { f.state().flags |= fileErr }
func (f file) clearErr()         { f.state().flags &^= fileErr | fileEOF }
func (f file) eof() bool         { return f.state().flags&fileEOF != 0 }
func (f file) setEOF()           { f.state().flags |= fileEOF }

// include/stdio_iso.h: #define stdin (&__iob[0]), stdout and stderr are
// __iob[1] and __iob[2]. newFile returns them for the three standard
// descriptors.
var X__iob [stdio.X_NFILE]stdio.FILE

func (f file) close(t *TLS) int32 {
	fd := f.fd()
	r := Xclose(t, fd)
	switch fd {
	case unistd.STDIN_FILENO, unistd.STDOUT_FILENO, unistd.STDERR_FILENO:
		X__iob[fd] = stdio.FILE{}
	default:
		Xfree(t, uintptr(f))
	}
	if r < 0 {
		return stdio.EOF
	}
	return 0
}

func newFile(t *TLS, fd int32) uintptr {
	var p uintptr
	switch fd {
	case unistd.STDIN_FILENO, unistd.STDOUT_FILENO, unistd.STDERR_FILENO:
		p = uintptr(unsafe.Pointer(&X__iob[fd]))
	default:
		if p = Xcalloc(t, 1, types.Size_t(unsafe.Sizeof(stdio.FILE{}))); p == 0 {
			return 0
		}
	}
	file(p).setFd(fd)
	return p
}

func fwrite(fd int32, b []byte) (int, error) {
	if fd == unistd.STDOUT_FILENO {
		return write(b)
	}

	// if dmesgs {
	// 	dmesg("%v: fd %v: %s", origin(1), fd, b)
	// }
	return unix.Write(int(fd), b) //TODO use Xwrite
}

// int *___errno(void);
func X___errno(t *TLS) uintptr {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return t.errnop
}

// void clearerr(FILE *stream);
func Xclearerr(t *TLS, stream uintptr) {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	file(stream).clearErr()
}

// int fprintf(FILE *stream, const char *format, ...);
func Xfprintf(t *TLS, stream, format, args uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v args=%v, (%v:)", t, args, origin(2))
	}
	n, _ := fwrite(file(stream).fd(), printf(format, args))
	return int32(n)
}

// int usleep(useconds_t usec);
func Xusleep(t *TLS, usec types.Useconds_t) int32 {
	if __ccgo_strace {
		trc("t=%v usec=%v, (%v:)", t, usec, origin(2))
	}
	time.Sleep(time.Microsecond * time.Duration(usec))
	return 0
}

// int getrusage(int who, struct rusage *usage);
func Xgetrusage(t *TLS, who int32, usage uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v who=%v usage=%v, (%v:)", t, who, usage, origin(2))
	}
	return t.libcall32(&libc_ccgo_getrusage, uintptr(who), usage)
}

// int lstat(const char *pathname, struct stat *statbuf);
func Xlstat(t *TLS, pathname, statbuf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v statbuf=%v, (%v:)", t, statbuf, origin(2))
	}
	return Xlstat64(t, pathname, statbuf)
}

// int stat(const char *pathname, struct stat *statbuf);
func Xstat(t *TLS, pathname, statbuf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v statbuf=%v, (%v:)", t, statbuf, origin(2))
	}
	return Xstat64(t, pathname, statbuf)
}

// int chdir(const char *path);
func Xchdir(t *TLS, path uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v path=%v, (%v:)", t, path, origin(2))
	}
	return t.libcall32(&libc_ccgo_chdir, path)
}

var localtime ctime.Tm

// struct tm *localtime(const time_t *timep);
func Xlocaltime(t *TLS, timep uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v timep=%v, (%v:)", t, timep, origin(2))
	}
	return Xlocaltime_r(t, timep, uintptr(unsafe.Pointer(&localtime)))
}

// struct tm *localtime_r(const time_t *timep, struct tm *result);
func Xlocaltime_r(_ *TLS, timep, result uintptr) uintptr {
	loc := getLocalLocation()
	ut := *(*types.Time_t)(unsafe.Pointer(timep))
	t := time.Unix(int64(ut), 0).In(loc)
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_sec = int32(t.Second())
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_min = int32(t.Minute())
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_hour = int32(t.Hour())
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_mday = int32(t.Day())
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_mon = int32(t.Month() - 1)
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_year = int32(t.Year() - 1900)
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_wday = int32(t.Weekday())
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_yday = int32(t.YearDay() - 1)
	(*ctime.Tm)(unsafe.Pointer(result)).Ftm_isdst = Bool32(isTimeDST(t))
	return result
}

// int open(const char *pathname, int flags, ...);
func Xopen(t *TLS, pathname uintptr, flags int32, args uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v pathname=%v flags=%v args=%v, (%v:)", t, pathname, flags, args, origin(2))
	}
	return Xopen64(t, pathname, flags, args)
}

// int open(const char *pathname, int flags, ...);
func Xopen64(t *TLS, pathname uintptr, flags int32, args uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v pathname=%v flags=%v args=%v, (%v:)", t, pathname, flags, args, origin(2))
	}
	var mode types.Mode_t
	if args != 0 {
		mode = (types.Mode_t)(VaUint32(&args))
	}
	return t.libcall32(&libc_ccgo_open, pathname, uintptr(flags), uintptr(mode))
}

// int openat(int dirfd, const char *pathname, int flags, mode_t mode);
func Xopenat(t *TLS, dirfd int32, pathname uintptr, flags int32, mode types.Mode_t) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v flags=%v mode=%v, (%v:)", t, dirfd, pathname, flags, mode, origin(2))
	}
	return t.libcall32(&libc_ccgo_openat, uintptr(dirfd), pathname, uintptr(flags), uintptr(mode))
}

// off_t lseek(int fd, off_t offset, int whence);
func Xlseek(t *TLS, fd int32, offset types.Off_t, whence int32) types.Off_t {
	if __ccgo_strace {
		trc("t=%v fd=%v offset=%v whence=%v, (%v:)", t, fd, offset, whence, origin(2))
	}
	return types.Off_t(Xlseek64(t, fd, offset, whence))
}

func whenceStr(whence int32) string {
	switch whence {
	case stdio.SEEK_CUR:
		return "SEEK_CUR"
	case stdio.SEEK_END:
		return "SEEK_END"
	case stdio.SEEK_SET:
		return "SEEK_SET"
	default:
		return fmt.Sprintf("whence(%d)", whence)
	}
}

var fsyncStatbuf stat.Stat

// int fsync(int fd);
func Xfsync(t *TLS, fd int32) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v, (%v:)", t, fd, origin(2))
	}
	if noFsync {
		// Simulate -DSQLITE_NO_SYNC for sqlite3 testfixture, see function full_sync in sqlite3.c
		return Xfstat(t, fd, uintptr(unsafe.Pointer(&fsyncStatbuf)))
	}

	return t.libcall32(&libc_ccgo_fsync, uintptr(fd))
}

// int fdatasync(int fd);
func Xfdatasync(t *TLS, fd int32) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v, (%v:)", t, fd, origin(2))
	}
	if noFsync {
		// Simulate -DSQLITE_NO_SYNC for sqlite3 testfixture, see function full_sync in sqlite3.c
		return Xfstat(t, fd, uintptr(unsafe.Pointer(&fsyncStatbuf)))
	}

	return t.libcall32(&libc_ccgo_fdatasync, uintptr(fd))
}

// long sysconf(int name);
func Xsysconf(t *TLS, name int32) long {
	if __ccgo_strace {
		trc("t=%v name=%v, (%v:)", t, name, origin(2))
	}
	switch name {
	case unistd.X_SC_PAGESIZE:
		return long(unix.Getpagesize())
	case unistd.X_SC_GETPW_R_SIZE_MAX:
		return -1
	case unistd.X_SC_GETGR_R_SIZE_MAX:
		return -1
	case unistd.X_SC_NPROCESSORS_ONLN:
		return long(runtime.NumCPU())
	}

	// -1 with errno unchanged means no limit.
	r, err := libcall(&libc_ccgo_sysconf, uintptr(name))
	if err != 0 {
		t.setErrno(err)
	}
	return long(r)
}

// int close(int fd);
func Xclose(t *TLS, fd int32) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v, (%v:)", t, fd, origin(2))
	}
	return t.libcall32(&libc_ccgo_close, uintptr(fd))
}

// char *getcwd(char *buf, size_t size);
func Xgetcwd(t *TLS, buf uintptr, size types.Size_t) uintptr {
	if __ccgo_strace {
		trc("t=%v buf=%v size=%v, (%v:)", t, buf, size, origin(2))
	}
	if buf != 0 {
		r, err := libcall(&libc_ccgo_getcwd, buf, uintptr(size))
		if r == 0 {
			t.setErrno(err)
		}
		return r
	}

	// The buffer the system libc would allocate cannot be passed to Xfree.
	if size == 0 {
		size = limits.PATH_MAX
	}
	if buf = Xmalloc(t, size); buf == 0 {
		t.setErrno(errno.ENOMEM)
		return 0
	}

	r, err := libcall(&libc_ccgo_getcwd, buf, uintptr(size))
	if r == 0 {
		Xfree(t, buf)
		t.setErrno(err)
	}
	return r
}

// int fstat(int fd, struct stat *statbuf);
func Xfstat(t *TLS, fd int32, statbuf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v statbuf=%v, (%v:)", t, fd, statbuf, origin(2))
	}
	return Xfstat64(t, fd, statbuf)
}

// int ftruncate(int fd, off_t length);
func Xftruncate(t *TLS, fd int32, length types.Off_t) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v length=%v, (%v:)", t, fd, length, origin(2))
	}
	return Xftruncate64(t, fd, length)
}

// int fcntl(int fd, int cmd, ... /* arg */ );
func Xfcntl(t *TLS, fd, cmd int32, args uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v cmd=%v args=%v, (%v:)", t, cmd, args, origin(2))
	}
	return Xfcntl64(t, fd, cmd, args)
}

// ssize_t read(int fd, void *buf, size_t count);
func Xread(t *TLS, fd int32, buf uintptr, count types.Size_t) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v fd=%v buf=%v count=%v, (%v:)", t, fd, buf, count, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_read, uintptr(fd), buf, uintptr(count)))
}

// ssize_t write(int fd, const void *buf, size_t count);
func Xwrite(t *TLS, fd int32, buf uintptr, count types.Size_t) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v fd=%v buf=%v count=%v, (%v:)", t, fd, buf, count, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_write, uintptr(fd), buf, uintptr(count)))
}

// int fchmod(int fd, mode_t mode);
func Xfchmod(t *TLS, fd int32, mode types.Mode_t) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v mode=%v, (%v:)", t, fd, mode, origin(2))
	}
	return t.libcall32(&libc_ccgo_fchmod, uintptr(fd), uintptr(mode))
}

// int fchmodat(int dirfd, const char *pathname, mode_t mode, int flags);
func Xfchmodat(t *TLS, dirfd int32, pathname uintptr, mode types.Mode_t, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v mode=%v flags=%v, (%v:)", t, dirfd, pathname, mode, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_fchmodat, uintptr(dirfd), pathname, uintptr(mode), uintptr(flags))
}

// int fchown(int fd, uid_t owner, gid_t group);
func Xfchown(t *TLS, fd int32, owner types.Uid_t, group types.Gid_t) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v owner=%v group=%v, (%v:)", t, fd, owner, group, origin(2))
	}
	return t.libcall32(&libc_ccgo_fchown, uintptr(fd), uintptr(owner), uintptr(group))
}

// uid_t geteuid(void);
func Xgeteuid(t *TLS) types.Uid_t {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return types.Uid_t(os.Geteuid())
}

// int munmap(void *addr, size_t length);
func Xmunmap(t *TLS, addr uintptr, length types.Size_t) int32 {
	if __ccgo_strace {
		trc("t=%v addr=%v length=%v, (%v:)", t, addr, length, origin(2))
	}
	return t.libcall32(&libc_ccgo_munmap, addr, uintptr(length))
}

// int gettimeofday(struct timeval *tv, struct timezone *tz);
func Xgettimeofday(t *TLS, tv, tz uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v tz=%v, (%v:)", t, tz, origin(2))
	}
	if tz != 0 {
		// struct timezone { int tz_minuteswest; int tz_dsttime; } is obsolete.
		*(*[2]int32)(unsafe.Pointer(tz)) = [2]int32{}
	}

	var tvs unix.Timeval
	err := unix.Gettimeofday(&tvs)
	if err != nil {
		t.setErrno(err)
		return -1
	}

	*(*unix.Timeval)(unsafe.Pointer(tv)) = tvs
	return 0
}

// int getsockopt(int sockfd, int level, int optname, void *optval, socklen_t *optlen);
func Xgetsockopt(t *TLS, sockfd, level, optname int32, optval, optlen uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v optname=%v optlen=%v, (%v:)", t, optname, optlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_getsockopt, uintptr(sockfd), uintptr(level), uintptr(optname), optval, optlen)
}

// int setsockopt(int sockfd, int level, int optname, const void *optval, socklen_t optlen);
func Xsetsockopt(t *TLS, sockfd, level, optname int32, optval uintptr, optlen socket.Socklen_t) int32 {
	if __ccgo_strace {
		trc("t=%v optname=%v optval=%v optlen=%v, (%v:)", t, optname, optval, optlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_setsockopt, uintptr(sockfd), uintptr(level), uintptr(optname), optval, uintptr(optlen))
}

// int ioctl(int fd, int request, ...);
func Xioctl(t *TLS, fd int32, request int32, va uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v request=%v va=%v, (%v:)", t, fd, request, va, origin(2))
	}
	var argp uintptr
	if va != 0 {
		argp = VaUintptr(&va)
	}
	return t.libcall32(&libc_ccgo_ioctl, uintptr(fd), uintptr(request), argp)
}

// int getsockname(int sockfd, struct sockaddr *addr, socklen_t *addrlen);
func Xgetsockname(t *TLS, sockfd int32, addr, addrlen uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v sockfd=%v addrlen=%v, (%v:)", t, sockfd, addrlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_getsockname, uintptr(sockfd), addr, addrlen)
}

// int select(int nfds, fd_set *readfds, fd_set *writefds, fd_set *exceptfds, struct timeval *timeout);
func Xselect(t *TLS, nfds int32, readfds, writefds, exceptfds, timeout uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v nfds=%v timeout=%v, (%v:)", t, nfds, timeout, origin(2))
	}
	n, err := unix.Select(
		int(nfds),
		(*unix.FdSet)(unsafe.Pointer(readfds)),
		(*unix.FdSet)(unsafe.Pointer(writefds)),
		(*unix.FdSet)(unsafe.Pointer(exceptfds)),
		(*unix.Timeval)(unsafe.Pointer(timeout)),
	)
	if err != nil {
		t.setErrno(err)
		return -1
	}

	return int32(n)
}

// int mkfifo(const char *pathname, mode_t mode);
func Xmkfifo(t *TLS, pathname uintptr, mode types.Mode_t) int32 {
	if __ccgo_strace {
		trc("t=%v pathname=%v mode=%v, (%v:)", t, pathname, mode, origin(2))
	}
	if err := unix.Mkfifo(GoString(pathname), mode); err != nil {
		t.setErrno(err)
		return -1
	}

	return 0
}

// mode_t umask(mode_t mask);
func Xumask(t *TLS, mask types.Mode_t) types.Mode_t {
	if __ccgo_strace {
		trc("t=%v mask=%v, (%v:)", t, mask, origin(2))
	}
	r, _ := libcall(&libc_ccgo_umask, uintptr(mask))
	return types.Mode_t(r)
}

// int execvp(const char *file, char *const argv[]);
func Xexecvp(t *TLS, file, argv uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v argv=%v, (%v:)", t, argv, origin(2))
	}
	path := GoString(file)
	if !strings.Contains(path, "/") {
		s, err := exec.LookPath(path)
		if err != nil {
			t.setErrno(errno.ENOENT)
			return -1
		}

		if file, err = CString(s); err != nil {
			t.setErrno(errno.ENOMEM)
			return -1
		}

		defer Xfree(t, file)
	}
	return Xexecve(t, file, argv, Environ())
}

// int execve(const char *pathname, char *const argv[], char *const envp[]);
func Xexecve(t *TLS, path, argv, envp uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v path=%v argv=%v envp=%v, (%v:)", t, path, argv, envp, origin(2))
	}
	return t.libcall32(&libc_ccgo_execve, path, argv, envp)
}

// pid_t waitpid(pid_t pid, int *wstatus, int options);
func Xwaitpid(t *TLS, pid types.Pid_t, wstatus uintptr, optname int32) types.Pid_t {
	if __ccgo_strace {
		trc("t=%v pid=%v wstatus=%v optname=%v, (%v:)", t, pid, wstatus, optname, origin(2))
	}
	return types.Pid_t(t.libcall32(&libc_ccgo_waitpid, uintptr(pid), wstatus, uintptr(optname)))
}

// int uname(struct utsname *buf);
func Xuname(t *TLS, buf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v buf=%v, (%v:)", t, buf, origin(2))
	}
	return t.libcall32(&libc_ccgo_uname, buf)
}

// ssize_t recv(int sockfd, void *buf, size_t len, int flags);
func Xrecv(t *TLS, sockfd int32, buf uintptr, len types.Size_t, flags int32) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v sockfd=%v buf=%v len=%v flags=%v, (%v:)", t, sockfd, buf, len, flags, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_recv, uintptr(sockfd), buf, uintptr(len), uintptr(flags)))
}

// ssize_t send(int sockfd, const void *buf, size_t len, int flags);
func Xsend(t *TLS, sockfd int32, buf uintptr, len types.Size_t, flags int32) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v sockfd=%v buf=%v len=%v flags=%v, (%v:)", t, sockfd, buf, len, flags, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_send, uintptr(sockfd), buf, uintptr(len), uintptr(flags)))
}

// int shutdown(int sockfd, int how);
func Xshutdown(t *TLS, sockfd, how int32) int32 {
	if __ccgo_strace {
		trc("t=%v how=%v, (%v:)", t, how, origin(2))
	}
	return t.libcall32(&libc_ccgo_shutdown, uintptr(sockfd), uintptr(how))
}

// int getpeername(int sockfd, struct sockaddr *addr, socklen_t *addrlen);
func Xgetpeername(t *TLS, sockfd int32, addr uintptr, addrlen uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v sockfd=%v addr=%v addrlen=%v, (%v:)", t, sockfd, addr, addrlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_getpeername, uintptr(sockfd), addr, addrlen)
}

// int socket(int domain, int type, int protocol);
func Xsocket(t *TLS, domain, type1, protocol int32) int32 {
	if __ccgo_strace {
		trc("t=%v protocol=%v, (%v:)", t, protocol, origin(2))
	}
	return t.libcall32(&libc_ccgo_socket, uintptr(domain), uintptr(type1), uintptr(protocol))
}

// int bind(int sockfd, const struct sockaddr *addr, socklen_t addrlen);
func Xbind(t *TLS, sockfd int32, addr uintptr, addrlen uint32) int32 {
	if __ccgo_strace {
		trc("t=%v sockfd=%v addr=%v addrlen=%v, (%v:)", t, sockfd, addr, addrlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_bind, uintptr(sockfd), addr, uintptr(addrlen))
}

// int connect(int sockfd, const struct sockaddr *addr, socklen_t addrlen);
func Xconnect(t *TLS, sockfd int32, addr uintptr, addrlen uint32) int32 {
	if __ccgo_strace {
		trc("t=%v sockfd=%v addr=%v addrlen=%v, (%v:)", t, sockfd, addr, addrlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_connect, uintptr(sockfd), addr, uintptr(addrlen))
}

// int listen(int sockfd, int backlog);
func Xlisten(t *TLS, sockfd, backlog int32) int32 {
	if __ccgo_strace {
		trc("t=%v backlog=%v, (%v:)", t, backlog, origin(2))
	}
	return t.libcall32(&libc_ccgo_listen, uintptr(sockfd), uintptr(backlog))
}

// int accept(int sockfd, struct sockaddr *addr, socklen_t *addrlen);
func Xaccept(t *TLS, sockfd int32, addr uintptr, addrlen uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v sockfd=%v addr=%v addrlen=%v, (%v:)", t, sockfd, addr, addrlen, origin(2))
	}
	return t.libcall32(&libc_ccgo_accept, uintptr(sockfd), addr, addrlen)
}

// int getrlimit(int resource, struct rlimit *rlim);
func Xgetrlimit(t *TLS, resource int32, rlim uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v resource=%v rlim=%v, (%v:)", t, resource, rlim, origin(2))
	}
	return Xgetrlimit64(t, resource, rlim)
}

// int setrlimit(int resource, const struct rlimit *rlim);
func Xsetrlimit(t *TLS, resource int32, rlim uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v resource=%v rlim=%v, (%v:)", t, resource, rlim, origin(2))
	}
	return Xsetrlimit64(t, resource, rlim)
}

// uid_t getuid(void);
func Xgetuid(t *TLS) types.Uid_t {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return types.Uid_t(os.Getuid())
}

// pid_t getpid(void);
func Xgetpid(t *TLS) int32 {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return int32(os.Getpid())
}

// int system(const char *command);
func Xsystem(t *TLS, command uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v command=%v, (%v:)", t, command, origin(2))
	}
	s := GoString(command)
	if command == 0 {
		panic(todo(""))
	}

	cmd := exec.Command("sh", "-c", s)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		ps := err.(*exec.ExitError)
		return int32(ps.ExitCode())
	}

	return 0
}

// int setvbuf(FILE *stream, char *buf, int mode, size_t size);
func Xsetvbuf(t *TLS, stream, buf uintptr, mode int32, size types.Size_t) int32 {
	if __ccgo_strace {
		trc("t=%v buf=%v mode=%v size=%v, (%v:)", t, buf, mode, size, origin(2))
	}
	return 0 //TODO
}

// int raise(int sig);
func Xraise(t *TLS, sig int32) int32 {
	if __ccgo_strace {
		trc("t=%v sig=%v, (%v:)", t, sig, origin(2))
	}
	if err := unix.Kill(unix.Getpid(), unix.Signal(sig)); err != nil {
		t.setErrno(err)
		return -1
	}

	return 0
}

// int backtrace(void **buffer, int size);
func Xbacktrace(t *TLS, buf uintptr, size int32) int32 {
	if __ccgo_strace {
		trc("t=%v buf=%v size=%v, (%v:)", t, buf, size, origin(2))
	}
	panic(todo(""))
}

// void backtrace_symbols_fd(void *const *buffer, int size, int fd);
func Xbacktrace_symbols_fd(t *TLS, buffer uintptr, size, fd int32) {
	if __ccgo_strace {
		trc("t=%v buffer=%v fd=%v, (%v:)", t, buffer, fd, origin(2))
	}
	panic(todo(""))
}

// int fileno(FILE *stream);
func Xfileno(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	if stream == 0 {
		t.setErrno(errno.EBADF)
		return -1
	}

	if fd := file(stream).fd(); fd >= 0 {
		return fd
	}

	t.setErrno(errno.EBADF)
	return -1
}

func newFtsent(t *TLS, info int, path string, stat *unix.Stat_t, err syscallErrno) (r *fts.FTSENT) {
	var statp uintptr
	if stat != nil {
		statp = Xmalloc(t, types.Size_t(unsafe.Sizeof(unix.Stat_t{})))
		if statp == 0 {
			panic("OOM")
		}

		*(*unix.Stat_t)(unsafe.Pointer(statp)) = *stat
	}
	csp, errx := CString(path)
	if errx != nil {
		panic("OOM")
	}

	return &fts.FTSENT{
		Ffts_info:    uint16(info),
		Ffts_path:    csp,
		Ffts_pathlen: uint64(len(path)),
		Ffts_statp:   statp,
		Ffts_errno:   int32(err),
	}
}

func newCFtsent(t *TLS, info int, path string, stat *unix.Stat_t, err syscallErrno) uintptr {
	p := Xcalloc(t, 1, types.Size_t(unsafe.Sizeof(fts.FTSENT{})))
	if p == 0 {
		panic("OOM")
	}

	*(*fts.FTSENT)(unsafe.Pointer(p)) = *newFtsent(t, info, path, stat, err)
	return p
}

func ftsentClose(t *TLS, p uintptr) {
	Xfree(t, (*fts.FTSENT)(unsafe.Pointer(p)).Ffts_path)
	Xfree(t, (*fts.FTSENT)(unsafe.Pointer(p)).Ffts_statp)
}

type ftstream struct {
	s []uintptr
	x int
}

func (f *ftstream) close(t *TLS) {
	for _, p := range f.s {
		ftsentClose(t, p)
		Xfree(t, p)
	}
	*f = ftstream{}
}

// FTS *fts_open(char * const *path_argv, int options, int (*compar)(const FTSENT **, const FTSENT **));
func Xfts_open(t *TLS, path_argv uintptr, options int32, compar uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v path_argv=%v options=%v compar=%v, (%v:)", t, path_argv, options, compar, origin(2))
	}
	return Xfts64_open(t, path_argv, options, compar)
}

// FTS *fts_open(char * const *path_argv, int options, int (*compar)(const FTSENT **, const FTSENT **));
func Xfts64_open(t *TLS, path_argv uintptr, options int32, compar uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v path_argv=%v options=%v compar=%v, (%v:)", t, path_argv, options, compar, origin(2))
	}
	f := &ftstream{}

	var walk func(string)
	walk = func(path string) {
		var fi os.FileInfo
		var err error
		switch {
		case options&fts.FTS_LOGICAL != 0:
			fi, err = os.Stat(path)
		case options&fts.FTS_PHYSICAL != 0:
			fi, err = os.Lstat(path)
		default:
			panic(todo(""))
		}

		if err != nil {
			return
		}

		var statp *unix.Stat_t
		if options&fts.FTS_NOSTAT == 0 {
			var stat unix.Stat_t
			switch {
			case options&fts.FTS_LOGICAL != 0:
				if err := unix.Stat(path, &stat); err != nil {
					panic(todo(""))
				}
			case options&fts.FTS_PHYSICAL != 0:
				if err := unix.Lstat(path, &stat); err != nil {
					panic(todo(""))
				}
			default:
				panic(todo(""))
			}

			statp = &stat
		}

	out:
		switch {
		case fi.IsDir():
			f.s = append(f.s, newCFtsent(t, fts.FTS_D, path, statp, 0))
			g, err := os.Open(path)
			switch x := err.(type) {
			case nil:
				// ok
			case *os.PathError:
				f.s = append(f.s, newCFtsent(t, fts.FTS_DNR, path, statp, errno.EACCES))
				break out
			default:
				panic(todo("%q: %v %T", path, x, x))
			}

			names, err := g.Readdirnames(-1)
			g.Close()
			if err != nil {
				panic(todo(""))
			}

			for _, name := range names {
				walk(path + "/" + name)
				if f == nil {
					break out
				}
			}

			f.s = append(f.s, newCFtsent(t, fts.FTS_DP, path, statp, 0))
		default:
			info := fts.FTS_F
			if fi.Mode()&os.ModeSymlink != 0 {
				info = fts.FTS_SL
			}
			switch {
			case statp != nil:
				f.s = append(f.s, newCFtsent(t, info, path, statp, 0))
			case options&fts.FTS_NOSTAT != 0:
				f.s = append(f.s, newCFtsent(t, fts.FTS_NSOK, path, nil, 0))
			default:
				panic(todo(""))
			}
		}
	}

	for {
		p := *(*uintptr)(unsafe.Pointer(path_argv))
		if p == 0 {
			if f == nil {
				return 0
			}

			if compar != 0 {
				panic(todo(""))
			}

			return addObject(f)
		}

		walk(GoString(p))
		path_argv += unsafe.Sizeof(uintptr(0))
	}
}

// FTSENT *fts_read(FTS *ftsp);
func Xfts_read(t *TLS, ftsp uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v ftsp=%v, (%v:)", t, ftsp, origin(2))
	}
	return Xfts64_read(t, ftsp)
}

// FTSENT *fts_read(FTS *ftsp);
func Xfts64_read(t *TLS, ftsp uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v ftsp=%v, (%v:)", t, ftsp, origin(2))
	}
	f := getObject(ftsp).(*ftstream)
	if f.x == len(f.s) {
		t.setErrno(0)
		return 0
	}

	r := f.s[f.x]
	if e := (*fts.FTSENT)(unsafe.Pointer(r)).Ffts_errno; e != 0 {
		t.setErrno(e)
	}
	f.x++
	return r
}

// int fts_close(FTS *ftsp);
func Xfts_close(t *TLS, ftsp uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v ftsp=%v, (%v:)", t, ftsp, origin(2))
	}
	return Xfts64_close(t, ftsp)
}

// int fts_close(FTS *ftsp);
func Xfts64_close(t *TLS, ftsp uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v ftsp=%v, (%v:)", t, ftsp, origin(2))
	}
	getObject(ftsp).(*ftstream).close(t)
	removeObject(ftsp)
	return 0
}

// void tzset (void);
func Xtzset(t *TLS) {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	//TODO
}

var strerrorBuf [100]byte

// char *strerror(int errnum);
func Xstrerror(t *TLS, errnum int32) uintptr {
	if __ccgo_strace {
		trc("t=%v errnum=%v, (%v:)", t, errnum, origin(2))
	}
	// 	if dmesgs {
	// 		dmesg("%v: %v\n%s", origin(1), errnum, debug.Stack())
	// 	}
	copy(strerrorBuf[:], fmt.Sprintf("strerror(%d)\x00", errnum))
	return uintptr(unsafe.Pointer(&strerrorBuf[0]))
}

// void *dlopen(const char *filename, int flags);
func Xdlopen(t *TLS, filename uintptr, flags int32) uintptr {
	if __ccgo_strace {
		trc("t=%v filename=%v flags=%v, (%v:)", t, filename, flags, origin(2))
	}
	panic(todo("%q", GoString(filename)))
}

// char *dlerror(void);
func Xdlerror(t *TLS) uintptr {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	panic(todo(""))
}

// int dlclose(void *handle);
func Xdlclose(t *TLS, handle uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v handle=%v, (%v:)", t, handle, origin(2))
	}
	panic(todo(""))
}

// void *dlsym(void *handle, const char *symbol);
func Xdlsym(t *TLS, handle, symbol uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v symbol=%v, (%v:)", t, symbol, origin(2))
	}
	panic(todo(""))
}

// void perror(const char *s);
func Xperror(tls *TLS, msg uintptr) {
	if __ccgo_strace {
		trc("tls=%v msg=%v, (%v:)", tls, msg, origin(2))
	}
	if msg != 0 && *(*int8)(unsafe.Pointer(msg)) != 0 {
		fmt.Fprintf(os.Stderr, "%s: ", GoString(msg))
	}
	errstr := Xstrerror(tls, *(*int32)(unsafe.Pointer(X__errno_location(tls))))
	fmt.Fprintf(os.Stderr, "%s\n", GoString(errstr))
}

// int pclose(FILE *stream);
func Xpclose(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	panic(todo(""))
}

// getaddrinfo, freeaddrinfo and gai_strerror are the system libc's. The musl
// versions in musl_illumos_amd64.go were translated for the Linux ABI: struct
// addrinfo has ai_addr and ai_canonname in the opposite order there, and the
// SOCK_*, AI_* and EAI_* values differ.

// int getaddrinfo(const char *node, const char *service, const struct addrinfo *hints, struct addrinfo **res);
func Xgetaddrinfo(t *TLS, node, service, hints, res uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v node=%v service=%v hints=%v res=%v, (%v:)", t, node, service, hints, res, origin(2))
	}
	r, err := libcall(&libc_ccgo_getaddrinfo, node, service, hints, res)
	if int32(r) == netdb.EAI_SYSTEM {
		t.setErrno(err)
	}
	return int32(r)
}

// void freeaddrinfo(struct addrinfo *res);
func Xfreeaddrinfo(t *TLS, res uintptr) {
	if __ccgo_strace {
		trc("t=%v res=%v, (%v:)", t, res, origin(2))
	}
	libcall(&libc_ccgo_freeaddrinfo, res)
}

// const char *gai_strerror(int errcode);
func Xgai_strerror(t *TLS, errcode int32) uintptr {
	if __ccgo_strace {
		trc("t=%v errcode=%v, (%v:)", t, errcode, origin(2))
	}
	r, _ := libcall(&libc_ccgo_gai_strerror, uintptr(errcode))
	return r
}

// const char *inet_ntop(int af, const void *src, char *dst, socklen_t size);
//
// The system inet_ntop and inet_pton, the musl ones in musl_illumos_amd64.go
// know AF_INET6 by its Linux value.
func Xinet_ntop(t *TLS, af int32, src, dst uintptr, size uint32) uintptr {
	if __ccgo_strace {
		trc("t=%v af=%v src=%v dst=%v size=%v, (%v:)", t, af, src, dst, size, origin(2))
	}
	r, err := libcall(&libc_ccgo_inet_ntop, uintptr(af), src, dst, uintptr(size))
	if r == 0 {
		t.setErrno(err)
	}
	return r
}

// int inet_pton(int af, const char *src, void *dst);
func Xinet_pton(t *TLS, af int32, src, dst uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v af=%v src=%v dst=%v, (%v:)", t, af, src, dst, origin(2))
	}
	r, err := libcall(&libc_ccgo_inet_pton, uintptr(af), src, dst)
	if int32(r) < 0 {
		t.setErrno(err)
	}
	return int32(r)
}

// int getnameinfo(const struct sockaddr *sa, socklen_t salen, char *host, socklen_t hostlen, char *serv, socklen_t servlen, int flags);
//
// The system getnameinfo has more arguments than syscall.sysvicall6 can pass.
// This is musl's, with the address family, the flags and the results
// translated between the illumos and the musl values. NI_NOFQDN and
// NI_NUMERICSCOPE have no effect in musl.
func Xgetnameinfo(t *TLS, sa uintptr, salen uint32, host uintptr, hostlen uint32, serv uintptr, servlen uint32, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v sa=%v salen=%v host=%v hostlen=%v serv=%v servlen=%v flags=%v, (%v:)", t, sa, salen, host, hostlen, serv, servlen, flags, origin(2))
	}
	const (
		// <sys/socket.h> and <netdb.h> of musl
		muslAF_INET6       = 10
		muslNI_NUMERICHOST = 0x01
		muslNI_NUMERICSERV = 0x02
		muslNI_NAMEREQD    = 0x08
		muslNI_DGRAM       = 0x10
	)
	if salen >= 2 && *(*uint16)(unsafe.Pointer(sa)) == socket.AF_INET6 {
		// The part of struct sockaddr_in6 musl reads has the same layout
		// on illumos, only the AF_INET6 value differs.
		p := t.Alloc(int(salen))

		defer t.Free(int(salen))

		copy((*RawMem)(unsafe.Pointer(p))[:salen:salen], (*RawMem)(unsafe.Pointer(sa))[:salen:salen])
		*(*uint16)(unsafe.Pointer(p)) = muslAF_INET6
		sa = p
	}
	var mflags int32
	for _, v := range []struct{ illumos, musl int32 }{
		{netdb.NI_NUMERICHOST, muslNI_NUMERICHOST},
		{netdb.NI_NUMERICSERV, muslNI_NUMERICSERV},
		{netdb.NI_NAMEREQD, muslNI_NAMEREQD},
		{netdb.NI_DGRAM, muslNI_DGRAM},
	} {
		if flags&v.illumos != 0 {
			mflags |= v.musl
		}
	}
	switch r := xgetnameinfo(t, sa, salen, host, hostlen, serv, servlen, mflags); r {
	case 0:
		return 0
	case -1:
		return netdb.EAI_BADFLAGS
	case -2:
		return netdb.EAI_NONAME
	case -3:
		return netdb.EAI_AGAIN
	case -4:
		return netdb.EAI_FAIL
	case -6:
		return netdb.EAI_FAMILY
	case -10:
		return netdb.EAI_MEMORY
	case -11:
		return netdb.EAI_SYSTEM
	case -12:
		return netdb.EAI_OVERFLOW
	default:
		return netdb.EAI_FAIL
	}
}

// int tcgetattr(int fd, struct termios *termios_p);
func Xtcgetattr(t *TLS, fd int32, termios_p uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v termios_p=%v, (%v:)", t, fd, termios_p, origin(2))
	}
	return t.libcall32(&libc_ccgo_tcgetattr, uintptr(fd), termios_p)
}

// int tcsetattr(int fd, int optional_actions, const struct termios *termios_p);
func Xtcsetattr(t *TLS, fd, optional_actions int32, termios_p uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v optional_actions=%v termios_p=%v, (%v:)", t, optional_actions, termios_p, origin(2))
	}
	return t.libcall32(&libc_ccgo_tcsetattr, uintptr(fd), uintptr(optional_actions), termios_p)
}

// speed_t cfgetospeed(const struct termios *termios_p);
func Xcfgetospeed(t *TLS, termios_p uintptr) termios.Speed_t {
	if __ccgo_strace {
		trc("t=%v termios_p=%v, (%v:)", t, termios_p, origin(2))
	}
	r, _ := libcall(&libc_ccgo_cfgetospeed, termios_p)
	return termios.Speed_t(r)
}

// int cfsetospeed(struct termios *termios_p, speed_t speed);
func Xcfsetospeed(t *TLS, termios_p uintptr, speed uint32) int32 {
	if __ccgo_strace {
		trc("t=%v termios_p=%v speed=%v, (%v:)", t, termios_p, speed, origin(2))
	}
	return t.libcall32(&libc_ccgo_cfsetospeed, termios_p, uintptr(speed))
}

// int cfsetispeed(struct termios *termios_p, speed_t speed);
func Xcfsetispeed(t *TLS, termios_p uintptr, speed uint32) int32 {
	if __ccgo_strace {
		trc("t=%v termios_p=%v speed=%v, (%v:)", t, termios_p, speed, origin(2))
	}
	return t.libcall32(&libc_ccgo_cfsetispeed, termios_p, uintptr(speed))
}

// pid_t fork(void);
func Xfork(t *TLS) int32 {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	t.setErrno(errno.ENOSYS)
	return -1
}

var emptyStr = [1]byte{}

// char *setlocale(int category, const char *locale);
func Xsetlocale(t *TLS, category int32, locale uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v category=%v locale=%v, (%v:)", t, category, locale, origin(2))
	}
	return uintptr(unsafe.Pointer(&emptyStr)) //TODO
}

// char *nl_langinfo(nl_item item);
func Xnl_langinfo(t *TLS, item langinfo.Nl_item) uintptr {
	if __ccgo_strace {
		trc("t=%v item=%v, (%v:)", t, item, origin(2))
	}
	return uintptr(unsafe.Pointer(&emptyStr)) //TODO
}

// FILE *popen(const char *command, const char *type);
func Xpopen(t *TLS, command, type1 uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v type1=%v, (%v:)", t, type1, origin(2))
	}
	panic(todo(""))
}

// char *realpath(const char *path, char *resolved_path);
func Xrealpath(t *TLS, path, resolved_path uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v resolved_path=%v, (%v:)", t, resolved_path, origin(2))
	}
	s, err := filepath.EvalSymlinks(GoString(path))
	if err != nil {
		if os.IsNotExist(err) {
			// if dmesgs {
			// 	dmesg("%v: %q: %v", origin(1), GoString(path), err)
			// }
			t.setErrno(errno.ENOENT)
			return 0
		}

		panic(todo("", err))
	}

	if s, err = filepath.Abs(s); err != nil {
		t.setErrno(err)
		return 0
	}

	if len(s) >= limits.PATH_MAX {
		t.setErrno(errno.ENAMETOOLONG)
		return 0
	}

	if resolved_path == 0 {
		if resolved_path = Xmalloc(t, limits.PATH_MAX); resolved_path == 0 {
			t.setErrno(errno.ENOMEM)
			return 0
		}
	}

	copy((*RawMem)(unsafe.Pointer(resolved_path))[:len(s):len(s)], s)
	(*RawMem)(unsafe.Pointer(resolved_path))[len(s)] = 0
	return resolved_path
}

var inetNtoaBuf [len("255.255.255.255") + 1]byte

// tin_addr is struct in_addr of <netinet/in.h> as ccgo/v4 translates it, so
// that translated code can pass it by value. It must stay identical to the
// generated type. in.In_addr is the ccgo/v3 rendering of the same union and
// translated code cannot pass a Tin_addr as one.
type tin_addr = struct {
	FS_un struct {
		FS_un_w [0]struct {
			Fs_w1 uint16
			Fs_w2 uint16
		}
		FS_addr [0]uint32
		FS_un_b struct {
			Fs_b1 uint8
			Fs_b2 uint8
			Fs_b3 uint8
			Fs_b4 uint8
		}
	}
}

// char *inet_ntoa(struct in_addr in);
func Xinet_ntoa(t *TLS, in1 tin_addr) uintptr {
	if __ccgo_strace {
		trc("t=%v in1=%v, (%v:)", t, in1, origin(2))
	}
	b := (*[4]byte)(unsafe.Pointer(&in1))
	copy(inetNtoaBuf[:], fmt.Sprintf("%d.%d.%d.%d\x00", b[0], b[1], b[2], b[3]))
	return uintptr(unsafe.Pointer(&inetNtoaBuf))
}

func X__ccgo_in6addr_anyp(t *TLS) uintptr {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return uintptr(unsafe.Pointer(&in6_addr_any))
}

func Xabort(t *TLS) {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	if dmesgs {
		dmesg("%v:", origin(1))
	}
	// The process must die by SIGABRT, eg. SQLite's writecrash.test wants Tcl's
	// exec to report "child killed: ...". Xsignal resets SIGABRT only in
	// os/signal, the Go runtime's handler stays installed, catches the signal,
	// prints a trace and exits with status 2. So reset it in the system libc,
	// unblock it and raise it on the calling thread, where it is delivered
	// before raise returns.
	sa := t.Alloc(int(unsafe.Sizeof(signal.Sigaction{})))
	*(*signal.Sigaction)(unsafe.Pointer(sa)) = signal.Sigaction{} // SIG_DFL
	libcall(&libc_ccgo_sigaction, signal.SIGABRT, sa, 0)
	set := sa + unsafe.Offsetof(signal.Sigaction{}.Fsa_mask)
	(*signal.Sigset_t)(unsafe.Pointer(set)).F__sigbits[0] = 1 << (signal.SIGABRT - 1)
	libcall(&libc_ccgo_sigprocmask, signal.SIG_UNBLOCK, set, 0)
	libcall(&libc_ccgo_raise, signal.SIGABRT)
	unix.Kill(unix.Getpid(), unix.Signal(signal.SIGABRT))
	panic(todo("unrechable"))
}

// int fflush(FILE *stream);
func Xfflush(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	return 0 //TODO
}

// size_t fread(void *ptr, size_t size, size_t nmemb, FILE *stream);
func Xfread(t *TLS, ptr uintptr, size, nmemb types.Size_t, stream uintptr) types.Size_t {
	if __ccgo_strace {
		trc("t=%v ptr=%v nmemb=%v stream=%v, (%v:)", t, ptr, nmemb, stream, origin(2))
	}
	if size == 0 || nmemb == 0 {
		return 0
	}

	want := uintptr(size * nmemb)
	var got uintptr
	for got < want {
		m, err := libcall(&libc_ccgo_read, uintptr(file(stream).fd()), ptr+got, want-got)
		if int64(m) < 0 {
			if err == errno.EINTR {
				continue
			}

			t.setErrno(err)
			file(stream).setErr()
			break
		}

		if m == 0 {
			file(stream).setEOF()
			break
		}

		got += m
	}
	return types.Size_t(got) / size
}

// size_t fwrite(const void *ptr, size_t size, size_t nmemb, FILE *stream);
func Xfwrite(t *TLS, ptr uintptr, size, nmemb types.Size_t, stream uintptr) types.Size_t {
	if __ccgo_strace {
		trc("t=%v ptr=%v nmemb=%v stream=%v, (%v:)", t, ptr, nmemb, stream, origin(2))
	}
	if size == 0 || nmemb == 0 {
		return 0
	}

	want := uintptr(size * nmemb)
	var got uintptr
	for got < want {
		m, err := libcall(&libc_ccgo_write, uintptr(file(stream).fd()), ptr+got, want-got)
		if int64(m) < 0 {
			if err == errno.EINTR {
				continue
			}

			t.setErrno(err)
			file(stream).setErr()
			break
		}

		got += m
	}
	return types.Size_t(got) / size
}

// int fclose(FILE *stream);
func Xfclose(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	return file(stream).close(t)
}

// int fputc(int c, FILE *stream);
func Xfputc(t *TLS, c int32, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v stream=%v, (%v:)", t, c, stream, origin(2))
	}
	if _, err := fwrite(file(stream).fd(), []byte{byte(c)}); err != nil {
		file(stream).setErr()
		return stdio.EOF
	}

	return int32(byte(c))
}

// int fseek(FILE *stream, long offset, int whence);
func Xfseek(t *TLS, stream uintptr, offset long, whence int32) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v offset=%v whence=%v, (%v:)", t, stream, offset, whence, origin(2))
	}
	if n := Xlseek(t, int32(file(stream).fd()), types.Off_t(offset), whence); n < 0 {
		// if dmesgs {
		// 	dmesg("%v: fd %v, off %#x, whence %v: %v", origin(1), file(stream).fd(), offset, whenceStr(whence), n)
		// }
		file(stream).setErr()
		return -1
	}

	// if dmesgs {
	// 	dmesg("%v: fd %v, off %#x, whence %v: ok", origin(1), file(stream).fd(), offset, whenceStr(whence))
	// }
	file(stream).state().flags &^= fileEOF
	ungetcMu.Lock()
	delete(ungetc, stream)
	ungetcMu.Unlock()
	return 0
}

// long ftell(FILE *stream);
func Xftell(t *TLS, stream uintptr) long {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	n := Xlseek(t, file(stream).fd(), 0, stdio.SEEK_CUR)
	if n < 0 {
		file(stream).setErr()
		return -1
	}

	// if dmesgs {
	// 	dmesg("%v: fd %v, n %#x: ok %#x", origin(1), file(stream).fd(), n, long(n))
	// }
	return long(n)
}

// int ferror(FILE *stream);
func Xferror(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	return Bool32(file(stream).err())
}

// int ungetc(int c, FILE *stream);
func Xungetc(t *TLS, c int32, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v stream=%v, (%v:)", t, c, stream, origin(2))
	}
	if c == stdio.EOF {
		return c
	}

	ungetcMu.Lock()
	ungetc[stream] = byte(c)
	ungetcMu.Unlock()
	file(stream).state().flags &^= fileEOF
	return int32(byte(c))
}

// int fscanf(FILE *stream, const char *format, ...);
func Xfscanf(t *TLS, stream, format, va uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v va=%v, (%v:)", t, va, origin(2))
	}
	return scanf(&byteScanner{t: t, stream: stream}, format, va)
}

// int fputs(const char *s, FILE *stream);
func Xfputs(t *TLS, s, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	if _, err := libcall(&libc_ccgo_write, uintptr(file(stream).fd()), s, uintptr(Xstrlen(t, s))); err != 0 {
		file(stream).setErr()
		return stdio.EOF
	}

	return 0
}

var getservbynameStaticResult netdb.Servent

// struct servent *getservbyname(const char *name, const char *proto);
func Xgetservbyname(t *TLS, name, proto uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v proto=%v, (%v:)", t, proto, origin(2))
	}
	var protoent *gonetdb.Protoent
	if proto != 0 {
		if protoent = gonetdb.GetProtoByName(GoString(proto)); protoent == nil {
			return 0
		}
	}
	servent := gonetdb.GetServByName(GoString(name), protoent)
	if servent == nil {
		// if dmesgs {
		// 	dmesg("%q %q: nil (protoent %+v)", GoString(name), GoString(proto), protoent)
		// }
		return 0
	}

	// The result is static storage that owns its strings and the aliases
	// array until the next call frees them, even after a failed call.
	r := &getservbynameStaticResult
	Xfree(t, r.Fs_name)
	if v := r.Fs_aliases; v != 0 {
		for p := v; *(*uintptr)(unsafe.Pointer(p)) != 0; p += unsafe.Sizeof(uintptr(0)) {
			Xfree(t, *(*uintptr)(unsafe.Pointer(p)))
		}
		Xfree(t, v)
	}
	Xfree(t, r.Fs_proto)
	*r = netdb.Servent{}
	var err error
	if r.Fs_name, err = CString(servent.Name); err != nil {
		return 0
	}

	if r.Fs_aliases = Xcalloc(t, types.Size_t(len(servent.Aliases)+1), types.Size_t(unsafe.Sizeof(uintptr(0)))); r.Fs_aliases == 0 {
		return 0
	}

	for i, v := range servent.Aliases {
		p, err := CString(v)
		if err != nil {
			return 0
		}

		*(*uintptr)(unsafe.Pointer(r.Fs_aliases + uintptr(i)*unsafe.Sizeof(uintptr(0)))) = p
	}

	// servent.Protocol is nil only for a protocol missing from /etc/protocols.
	var protoname string
	if servent.Protocol != nil {
		protoname = servent.Protocol.Name
	}
	if r.Fs_proto, err = CString(protoname); err != nil {
		return 0
	}

	r.Fs_port = int32(Xhtons(t, uint16(servent.Port)))
	return uintptr(unsafe.Pointer(r))
}

// DIR *opendir(const char *name);
func Xopendir(t *TLS, name uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v name=%v, (%v:)", t, name, origin(2))
	}
	r, err := libcall(&libc_ccgo_opendir, name)
	if r == 0 {
		t.setErrno(err)
	}
	return r
}

// struct dirent *readdir(DIR *dirp);
func Xreaddir(t *TLS, dir uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v dir=%v, (%v:)", t, dir, origin(2))
	}
	// NULL with errno unchanged is the end of the directory.
	r, err := libcall(&libc_ccgo_readdir, dir)
	if r == 0 && err != 0 {
		t.setErrno(err)
	}
	return r
}

func Xreaddir64(t *TLS, dir uintptr) uintptr {
	if __ccgo_strace {
		trc("t=%v dir=%v, (%v:)", t, dir, origin(2))
	}
	return Xreaddir(t, dir)
}

// int closedir(DIR *dirp);
func Xclosedir(t *TLS, dir uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v dir=%v, (%v:)", t, dir, origin(2))
	}
	return t.libcall32(&libc_ccgo_closedir, dir)
}

// void rewinddir(DIR *dirp);
func Xrewinddir(t *TLS, dir uintptr) {
	if __ccgo_strace {
		trc("t=%v dir=%v, (%v:)", t, dir, origin(2))
	}
	libcall(&libc_ccgo_rewinddir, dir)
}

// void seekdir(DIR *dirp, long loc);
//
// <dirent.h> defines rewinddir(dirp) as seekdir(dirp, 0L).
func Xseekdir(t *TLS, dir uintptr, loc long) {
	if __ccgo_strace {
		trc("t=%v dir=%v loc=%v, (%v:)", t, dir, loc, origin(2))
	}
	libcall(&libc_ccgo_seekdir, dir, uintptr(loc))
}

// long telldir(DIR *dirp);
func Xtelldir(t *TLS, dir uintptr) long {
	if __ccgo_strace {
		trc("t=%v dir=%v, (%v:)", t, dir, origin(2))
	}
	return long(t.libcall64(&libc_ccgo_telldir, dir))
}

func __syscall(r, _ uintptr, errno syscallErrno) long {
	if errno != 0 {
		return long(-errno)
	}

	return long(r)
}

func X__syscall1(t *TLS, trap, p1 long) long {
	if __ccgo_strace {
		trc("t=%v p1=%v, (%v:)", t, p1, origin(2))
	}
	return __syscall(unix.Syscall(uintptr(trap), uintptr(p1), 0, 0))
}

func X__syscall3(t *TLS, trap, p1, p2, p3 long) long {
	if __ccgo_strace {
		trc("t=%v p3=%v, (%v:)", t, p3, origin(2))
	}
	return __syscall(unix.Syscall(uintptr(trap), uintptr(p1), uintptr(p2), uintptr(p3)))
}

func X__syscall4(t *TLS, trap, p1, p2, p3, p4 long) long {
	if __ccgo_strace {
		trc("t=%v p4=%v, (%v:)", t, p4, origin(2))
	}
	return __syscall(unix.Syscall6(uintptr(trap), uintptr(p1), uintptr(p2), uintptr(p3), uintptr(p4), 0, 0))
}

func fcntlCmdStr(cmd int32) string {
	switch cmd {
	case fcntl.F_GETOWN:
		return "F_GETOWN"
	case fcntl.F_SETLK:
		return "F_SETLK"
	case fcntl.F_GETLK:
		return "F_GETLK"
	case fcntl.F_SETFD:
		return "F_SETFD"
	case fcntl.F_GETFD:
		return "F_GETFD"
	default:
		return fmt.Sprintf("cmd(%d)", cmd)
	}
}

// setenv, unsetenv and putenv below are ccgo translations of musl
// src/env/{setenv,unsetenv,putenv}.c, as in musl_darwin_amd64.go.

func X__strchrnul(tls *TLS, s uintptr, c int32) uintptr {
	if __ccgo_strace {
		trc("tls=%v s=%v c=%v, (%v:)", tls, s, c, origin(2))
	}
	c = int32(uint8(c))
	for ; *(*int8)(unsafe.Pointer(s)) != 0 && int32(*(*uint8)(unsafe.Pointer(s))) != c; s++ {
	}
	return s
}

func X__putenv(tls *TLS, s uintptr, l size_t, r uintptr) int32 { /* putenv.c:8:5: */
	if __ccgo_strace {
		trc("tls=%v s=%v l=%v r=%v, (%v:)", tls, s, l, r, origin(2))
	}
	var i size_t
	var newenv uintptr
	var tmp uintptr
	//TODO for (char **e = __environ; *e; e++, i++)
	var e uintptr
	i = uint64(0)
	if !(Environ() != 0) {
		goto __1
	}
	//TODO for (char **e = __environ; *e; e++, i++)
	e = Environ()
__2:
	if !(*(*uintptr)(unsafe.Pointer(e)) != 0) {
		goto __4
	}
	if !!(Xstrncmp(tls, s, *(*uintptr)(unsafe.Pointer(e)), l+uint64(1)) != 0) {
		goto __5
	}
	tmp = *(*uintptr)(unsafe.Pointer(e))
	*(*uintptr)(unsafe.Pointer(e)) = s
	X__env_rm_add(tls, tmp, r)
	return 0
__5:
	;
	goto __3
__3:
	e += 8
	i++
	goto __2
	goto __4
__4:
	;
__1:
	;
	if !(Environ() == _soldenv) {
		goto __6
	}
	newenv = Xrealloc(tls, _soldenv, uint64(unsafe.Sizeof(uintptr(0)))*(i+uint64(2)))
	if !!(newenv != 0) {
		goto __8
	}
	goto oom
__8:
	;
	goto __7
__6:
	newenv = Xmalloc(tls, uint64(unsafe.Sizeof(uintptr(0)))*(i+uint64(2)))
	if !!(newenv != 0) {
		goto __9
	}
	goto oom
__9:
	;
	if !(i != 0) {
		goto __10
	}
	Xmemcpy(tls, newenv, Environ(), uint64(unsafe.Sizeof(uintptr(0)))*i)
__10:
	;
	Xfree(tls, _soldenv)
__7:
	;
	*(*uintptr)(unsafe.Pointer(newenv + uintptr(i)*8)) = s
	*(*uintptr)(unsafe.Pointer(newenv + uintptr(i+uint64(1))*8)) = uintptr(0)
	*(*uintptr)(unsafe.Pointer(EnvironP())) = AssignPtrUintptr(uintptr(unsafe.Pointer(&_soldenv)), newenv)
	if !(r != 0) {
		goto __11
	}
	X__env_rm_add(tls, uintptr(0), r)
__11:
	;
	return 0
oom:
	Xfree(tls, r)
	return -1
}

var _soldenv uintptr /* putenv.c:22:14: */

func Xputenv(tls *TLS, s uintptr) int32 { /* putenv.c:43:5: */
	if __ccgo_strace {
		trc("tls=%v s=%v, (%v:)", tls, s, origin(2))
	}
	var l size_t = size_t((int64(X__strchrnul(tls, s, '=')) - int64(s)) / 1)
	if !(l != 0) || !(int32(*(*int8)(unsafe.Pointer(s + uintptr(l)))) != 0) {
		return Xunsetenv(tls, s)
	}
	return X__putenv(tls, s, l, uintptr(0))
}

func X__env_rm_add(tls *TLS, old uintptr, new uintptr) { /* setenv.c:5:6: */
	if __ccgo_strace {
		trc("tls=%v old=%v new=%v, (%v:)", tls, old, new, origin(2))
	}
	//TODO for (size_t i=0; i < env_alloced_n; i++)
	var i size_t = uint64(0)
	for ; i < _senv_alloced_n; i++ {
		if *(*uintptr)(unsafe.Pointer(_senv_alloced + uintptr(i)*8)) == old {
			*(*uintptr)(unsafe.Pointer(_senv_alloced + uintptr(i)*8)) = new
			Xfree(tls, old)
			return
		} else if !(int32(*(*uintptr)(unsafe.Pointer(_senv_alloced + uintptr(i)*8))) != 0) && new != 0 {
			*(*uintptr)(unsafe.Pointer(_senv_alloced + uintptr(i)*8)) = new
			new = uintptr(0)
		}
	}
	if !(new != 0) {
		return
	}
	var t uintptr = Xrealloc(tls, _senv_alloced, uint64(unsafe.Sizeof(uintptr(0)))*(_senv_alloced_n+uint64(1)))
	if !(t != 0) {
		return
	}
	*(*uintptr)(unsafe.Pointer(AssignPtrUintptr(uintptr(unsafe.Pointer(&_senv_alloced)), t) + uintptr(PostIncUint64(&_senv_alloced_n, 1))*8)) = new
}

var _senv_alloced uintptr  /* setenv.c:7:14: */
var _senv_alloced_n size_t /* setenv.c:8:16: */

// int setenv(const char *name, const char *value, int overwrite);
func Xsetenv(tls *TLS, var1 uintptr, value uintptr, overwrite int32) int32 { /* setenv.c:26:5: */
	if __ccgo_strace {
		trc("tls=%v var1=%v value=%v overwrite=%v, (%v:)", tls, var1, value, overwrite, origin(2))
	}
	var s uintptr
	var l1 size_t
	var l2 size_t

	if !(var1 != 0) || !(int32(AssignUint64(&l1, size_t((int64(X__strchrnul(tls, var1, '='))-int64(var1))/1))) != 0) || *(*int8)(unsafe.Pointer(var1 + uintptr(l1))) != 0 {
		*(*int32)(unsafe.Pointer(X___errno_location(tls))) = 22
		return -1
	}
	if !(overwrite != 0) && Xgetenv(tls, var1) != 0 {
		return 0
	}

	l2 = Xstrlen(tls, value)
	s = Xmalloc(tls, l1+l2+uint64(2))
	if !(s != 0) {
		return -1
	}
	Xmemcpy(tls, s, var1, l1)
	*(*int8)(unsafe.Pointer(s + uintptr(l1))) = int8('=')
	Xmemcpy(tls, s+uintptr(l1)+uintptr(1), value, l2+uint64(1))
	return X__putenv(tls, s, l1, s)
}

// int unsetenv(const char *name);
func Xunsetenv(tls *TLS, name uintptr) int32 { /* unsetenv.c:9:5: */
	if __ccgo_strace {
		trc("tls=%v name=%v, (%v:)", tls, name, origin(2))
	}
	var l size_t = size_t((int64(X__strchrnul(tls, name, '=')) - int64(name)) / 1)
	if !(l != 0) || *(*int8)(unsafe.Pointer(name + uintptr(l))) != 0 {
		*(*int32)(unsafe.Pointer(X___errno_location(tls))) = 22
		return -1
	}
	if Environ() != 0 {
		var e uintptr = Environ()
		var eo uintptr = e
		for ; *(*uintptr)(unsafe.Pointer(e)) != 0; e += 8 {
			//TODO if (!strncmp(name, *e, l) && l[*e] == '=')
			if !(Xstrncmp(tls, name, *(*uintptr)(unsafe.Pointer(e)), l) != 0) && int32(*(*int8)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(e)) + uintptr(l)))) == '=' {
				X__env_rm_add(tls, *(*uintptr)(unsafe.Pointer(e)), uintptr(0))
			} else if eo != e {
				*(*uintptr)(unsafe.Pointer(PostIncUintptr(&eo, 8))) = *(*uintptr)(unsafe.Pointer(e))
			} else {
				eo += 8
			}
		}
		if eo != e {
			*(*uintptr)(unsafe.Pointer(eo)) = uintptr(0)
		}
	}
	return 0
}

// int pause(void);
func Xpause(t *TLS) int32 {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	err := unix.Pause()
	if err != nil {
		t.setErrno(err)
	}

	return -1
}

// ssize_t writev(int fd, const struct iovec *iov, int iovcnt);
func Xwritev(t *TLS, fd int32, iov uintptr, iovcnt int32) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v fd=%v iov=%v iovcnt=%v, (%v:)", t, fd, iov, iovcnt, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_writev, uintptr(fd), iov, uintptr(iovcnt)))
}

// int __isoc99_sscanf(const char *str, const char *format, ...);
func X__isoc99_sscanf(t *TLS, str, format, va uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v va=%v, (%v:)", t, va, origin(2))
	}
	r := Xsscanf(t, str, format, va)
	// if dmesgs {
	// 	dmesg("%v: %q %q: %d", origin(1), GoString(str), GoString(format), r)
	// }
	return r
}

// var ctimeStaticBuf [32]byte
//
// // char *ctime(const time_t *timep);
// func Xctime(t *TLS, timep uintptr) uintptr {
// 	return Xctime_r(t, timep, uintptr(unsafe.Pointer(&ctimeStaticBuf[0])))
// }
//
// // char *ctime_r(const time_t *timep, char *buf);
// func Xctime_r(t *TLS, timep, buf uintptr) uintptr {
// 	ut := *(*unix.Time_t)(unsafe.Pointer(timep))
// 	tm := time.Unix(int64(ut), 0).Local()
// 	s := tm.Format(time.ANSIC) + "\n\x00"
// 	copy((*RawMem)(unsafe.Pointer(buf))[:26:26], s)
// 	return buf
// }

// ssize_t pwrite(int fd, const void *buf, size_t count, off_t offset);
func Xpwrite(t *TLS, fd int32, buf uintptr, count types.Size_t, offset types.Off_t) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v fd=%v buf=%v count=%v offset=%v, (%v:)", t, fd, buf, count, offset, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_pwrite, uintptr(fd), buf, uintptr(count), uintptr(offset)))
}

// int fstatfs(int fd, struct statfs *buf);
func Xfstatfs(t *TLS, fd int32, buf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v buf=%v, (%v:)", t, fd, buf, origin(2))
	}
	// illumos has only the obsolete SVR4 fstatfs(int, struct statfs *, int, int).
	t.setErrno(errno.ENOSYS)
	return -1
}

// int fstatvfs(int fd, struct statvfs *buf);
func Xfstatvfs(t *TLS, fd int32, buf uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v buf=%v, (%v:)", t, fd, buf, origin(2))
	}
	return t.libcall32(&libc_ccgo_fstatvfs, uintptr(fd), buf)
}

// ssize_t getrandom(void *buf, size_t buflen, unsigned int flags);
func Xgetrandom(t *TLS, buf uintptr, buflen size_t, flags uint32) ssize_t {
	if __ccgo_strace {
		trc("t=%v buf=%v buflen=%v flags=%v, (%v:)", t, buf, buflen, flags, origin(2))
	}
	return ssize_t(t.libcall64(&libc_ccgo_getrandom, buf, uintptr(buflen), uintptr(flags)))
}

// int posix_fadvise(int fd, off_t offset, off_t len, int advice);
func Xposix_fadvise(t *TLS, fd int32, offset, len types.Off_t, advice int32) int32 {
	if __ccgo_strace {
		trc("t=%v fd=%v len=%v advice=%v, (%v:)", t, fd, len, advice, origin(2))
	}
	// Returns the error number, errno is not set.
	r, _ := libcall(&libc_ccgo_posix_fadvise, uintptr(fd), uintptr(offset), uintptr(len), uintptr(advice))
	return int32(r)
}

// int fgetc(FILE *stream);
func Xfgetc(t *TLS, stream uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v stream=%v, (%v:)", t, stream, origin(2))
	}
	ungetcMu.Lock()
	c, ok := ungetc[stream]
	delete(ungetc, stream)
	ungetcMu.Unlock()
	if ok {
		return int32(c)
	}

	p := t.Alloc(1)
	defer t.Free(1)
	switch n, err := libcall(&libc_ccgo_read, uintptr(file(stream).fd()), p, 1); int64(n) {
	case 1:
		return int32(*(*byte)(unsafe.Pointer(p)))
	case 0:
		file(stream).setEOF()
	default:
		t.setErrno(err)
		file(stream).setErr()
	}
	return stdio.EOF
}

// int getchar(void);
//
// <stdio.h> defines getchar as a macro only when _REENTRANT is not defined.
func Xgetchar(t *TLS) int32 {
	if __ccgo_strace {
		trc("t=%v, (%v:)", t, origin(2))
	}
	return Xfgetc(t, Xstdin)
}

// void __assert_c99(const char *expr, const char *file, int line, const char *func);
func X__assert_c99(t *TLS, expr, file uintptr, line int32, fn uintptr) {
	if __ccgo_strace {
		trc("t=%v expr=%v file=%v line=%v fn=%v, (%v:)", t, expr, file, line, fn, origin(2))
	}
	X__assert_fail(t, expr, file, uint32(line), fn)
}

// void __assert(const char *expr, const char *file, int line);
func X__assert(t *TLS, expr, file uintptr, line int32) {
	if __ccgo_strace {
		trc("t=%v expr=%v file=%v line=%v, (%v:)", t, expr, file, line, origin(2))
	}
	X__assert_fail(t, expr, file, uint32(line), 0)
}

// void *_memset(void *s, int c, size_t n);
//
// <sys/select.h> defines FD_ZERO with _memset, the system libc's alias of
// memset.
func X_memset(t *TLS, s uintptr, c int32, n types.Size_t) uintptr {
	if __ccgo_strace {
		trc("t=%v s=%v c=%v n=%v, (%v:)", t, s, c, n, origin(2))
	}
	return Xmemset(t, s, c, n)
}

// The <ctype.h> classification functions not in musl_illumos_amd64.go. illumos
// has no ctype macros, see <iso/ctype_iso.h>. These are musl's, for the C
// locale.

// int isblank(int c);
func Xisblank(t *TLS, c int32) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v, (%v:)", t, c, origin(2))
	}
	return Bool32(c == ' ' || c == '\t')
}

// int iscntrl(int c);
func Xiscntrl(t *TLS, c int32) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v, (%v:)", t, c, origin(2))
	}
	return Bool32(uint32(c) < 0x20 || c == 0x7f)
}

// int isgraph(int c);
func Xisgraph(t *TLS, c int32) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v, (%v:)", t, c, origin(2))
	}
	return Bool32(uint32(c)-0x21 < 0x5e)
}

// int ispunct(int c);
func Xispunct(t *TLS, c int32) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v, (%v:)", t, c, origin(2))
	}
	return Bool32(Xisgraph(t, c) != 0 && Xisalnum(t, c) == 0)
}

// int isspace(int c);
func Xisspace(t *TLS, c int32) int32 {
	if __ccgo_strace {
		trc("t=%v c=%v, (%v:)", t, c, origin(2))
	}
	return Bool32(c == ' ' || uint32(c)-'\t' < 5)
}

// void uuid_copy(uuid_t dst, uuid_t src);
func Xuuid_copy(t *TLS, dst, src uintptr) {
	if __ccgo_strace {
		trc("t=%v src=%v, (%v:)", t, src, origin(2))
	}
	*(*uuid.Uuid_t)(unsafe.Pointer(dst)) = *(*uuid.Uuid_t)(unsafe.Pointer(src))
}

// int uuid_parse( char *in, uuid_t uu);
func Xuuid_parse(t *TLS, in uintptr, uu uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v in=%v uu=%v, (%v:)", t, in, uu, origin(2))
	}
	r, err := guuid.Parse(GoString(in))
	if err != nil {
		return -1
	}

	copy((*RawMem)(unsafe.Pointer(uu))[:unsafe.Sizeof(uuid.Uuid_t{})], r[:])
	return 0
}

// int mkdirat(int dirfd, const char *pathname, mode_t mode);
func Xmkdirat(t *TLS, dirfd int32, pathname uintptr, mode types.Mode_t) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v mode=%v, (%v:)", t, dirfd, pathname, mode, origin(2))
	}
	return t.libcall32(&libc_ccgo_mkdirat, uintptr(dirfd), pathname, uintptr(mode))
}

// int symlinkat(const char *target, int newdirfd, const char *linkpath);
func Xsymlinkat(t *TLS, target uintptr, newdirfd int32, linkpath uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v target=%v newdirfd=%v linkpath=%v, (%v:)", t, target, newdirfd, linkpath, origin(2))
	}
	return t.libcall32(&libc_ccgo_symlinkat, target, uintptr(newdirfd), linkpath)
}

// int utimensat(int dirfd, const char *pathname, const struct timespec times[2], int flags);
func Xutimensat(t *TLS, dirfd int32, pathname, times uintptr, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v times=%v flags=%v, (%v:)", t, dirfd, times, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_utimensat, uintptr(dirfd), pathname, times, uintptr(flags))
}

// int unlinkat(int dirfd, const char *pathname, int flags);
func Xunlinkat(t *TLS, dirfd int32, pathname uintptr, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v flags=%v, (%v:)", t, dirfd, pathname, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_unlinkat, uintptr(dirfd), pathname, uintptr(flags))
}

// int faccessat(int dirfd, const char *pathname, int mode, int flags);
func Xfaccessat(t *TLS, dirfd int32, pathname uintptr, mode, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v flags=%v, (%v:)", t, dirfd, pathname, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_faccessat, uintptr(dirfd), pathname, uintptr(mode), uintptr(flags))
}

// int renameat2(int olddirfd, const char *oldpath,	int newdirfd, const char *newpath, unsigned int flags);
func Xrenameat2(t *TLS, olddirfd int32, oldpath uintptr, newdirfd int32, newpath uintptr, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v olddirfd=%v oldpath=%v newdirfd=%v newpath=%v flags=%v, (%v:)", t, olddirfd, oldpath, newdirfd, newpath, flags, origin(2))
	}
	// renameat2 is Linux only, illumos has none of its flags.
	if flags != 0 {
		t.setErrno(errno.EINVAL)
		return -1
	}

	return t.libcall32(&libc_ccgo_renameat, uintptr(olddirfd), oldpath, uintptr(newdirfd), newpath)
}

// int mknodat(int dirfd, const char *pathname, mode_t mode, dev_t dev);
func Xmknodat(t *TLS, dirfd int32, pathname uintptr, mode types.Mode_t, dev types.Dev_t) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v mode=%v dev=%v, (%v:)", t, dirfd, pathname, mode, dev, origin(2))
	}
	return t.libcall32(&libc_ccgo_mknodat, uintptr(dirfd), pathname, uintptr(mode), uintptr(dev))
}

// int fchownat(int dirfd, const char *pathname, uid_t owner, gid_t group, int flags);
func Xfchownat(t *TLS, dirfd int32, pathname uintptr, uid types.Uid_t, gid types.Gid_t, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v dirfd=%v pathname=%v uid=%v gid=%v flags=%v, (%v:)", t, dirfd, pathname, uid, gid, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_fchownat, uintptr(dirfd), pathname, uintptr(uid), uintptr(gid), uintptr(flags))
}

// int linkat(int olddirfd, const char *oldpath, int newdirfd, const char *newpath, int flags);
func Xlinkat(t *TLS, olddirfd int32, oldpath uintptr, newdirfd int32, newpath uintptr, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v olddirfd=%v oldpath=%v newdirfd=%v newpath=%v flags=%v, (%v:)", t, olddirfd, oldpath, newdirfd, newpath, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_linkat, uintptr(olddirfd), oldpath, uintptr(newdirfd), newpath, uintptr(flags))
}

// int pipe2(int pipefd[2], int flags);
func Xpipe2(t *TLS, pipefd uintptr, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v pipefd=%v flags=%v, (%v:)", t, pipefd, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_pipe2, pipefd, uintptr(flags))
}

// int dup(int oldfd);
func Xdup(t *TLS, oldfd int32) int32 {
	if __ccgo_strace {
		trc("t=%v oldfd=%v, (%v:)", t, oldfd, origin(2))
	}
	return t.libcall32(&libc_ccgo_dup, uintptr(oldfd))
}

// int dup3(int oldfd, int newfd, int flags);
func Xdup3(t *TLS, oldfd int32, newfd int32, flags int32) int32 {
	if __ccgo_strace {
		trc("t=%v oldfd=%v newfd=%v flags=%v, (%v:)", t, oldfd, newfd, flags, origin(2))
	}
	return t.libcall32(&libc_ccgo_dup3, uintptr(oldfd), uintptr(newfd), uintptr(flags))
}

// ssize_t readlinkat(int dirfd, const char *pathname, char *buf, size_t bufsiz);
func Xreadlinkat(t *TLS, dirfd int32, pathname, buf uintptr, bufsiz types.Size_t) types.Ssize_t {
	if __ccgo_strace {
		trc("t=%v dirfd=%v buf=%v bufsiz=%v, (%v:)", t, dirfd, buf, bufsiz, origin(2))
	}
	return types.Ssize_t(t.libcall64(&libc_ccgo_readlinkat, uintptr(dirfd), pathname, buf, uintptr(bufsiz)))
}

// int nanosleep(const struct timespec *req, struct timespec *rem);
func Xnanosleep(t *TLS, req, rem uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v rem=%v, (%v:)", t, rem, origin(2))
	}
	v := *(*ctime.Timespec)(unsafe.Pointer(req))
	time.Sleep(time.Second*time.Duration(v.Ftv_sec) + time.Duration(v.Ftv_nsec))
	return 0
}

func Xfeof(t *TLS, f uintptr) (r int32) {
	if __ccgo_strace {
		trc("t=%v f=%v, (%v:)", t, f, origin(2))
		defer func() { trc("-> %v", r) }()
	}
	return Bool32(file(f).eof())
}

type byteScanner struct {
	t      *TLS
	stream uintptr

	last byte
}

func (s *byteScanner) ReadByte() (byte, error) {
	c := Xfgetc(s.t, s.stream)
	if c < 0 {
		return 0, io.EOF
	}

	s.last = byte(c)
	return byte(c), nil
}

func (s *byteScanner) UnreadByte() error {
	Xungetc(s.t, int32(s.last), s.stream)
	return nil
}

// int clock_gettime(clockid_t clk_id, struct timespec *tp);
func Xclock_gettime(t *TLS, clk_id int32, tp uintptr) int32 {
	if __ccgo_strace {
		trc("t=%v clk_id=%v tp=%v, (%v:)", t, clk_id, tp, origin(2))
	}
	return t.libcall32(&libc_ccgo_clock_gettime, uintptr(clk_id), tp)
}

// struct tm *gmtime_r(const time_t *timep, struct tm *result);
func Xgmtime_r(tls *TLS, t uintptr, tm uintptr) (r uintptr) {
	if __ccgo_strace {
		trc("tls=%v t=%v tm=%v, (%v:)", tls, t, tm, origin(2))
		defer func() { trc("-> %v", r) }()
	}
	if x___secs_to_tm(tls, int64(*(*time_t)(unsafe.Pointer(t))), tm) < 0 {
		*(*int32)(unsafe.Pointer(X__errno_location(tls))) = int32(errno.EOVERFLOW)
		return uintptr(0)
	}
	(*ctime.Tm)(unsafe.Pointer(tm)).Ftm_isdst = 0
	return tm
}

// The illumos pthread_attr_t and pthread_mutexattr_t hold a pointer to an
// object the system libc allocates. The hand-written pthreads keep their state
// in that pointer-sized slot instead: pthreadAttr and pthreadMutexattr.

type pthreadMutexattr struct {
	typ int32
}

func X__ccgo_pthreadMutexattrGettype(tls *TLS, a uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v, (%v:)", tls, a, origin(2))
	}
	return (*pthreadMutexattr)(unsafe.Pointer(a)).typ
}

func X__ccgo_getMutexType(tls *TLS, m uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v m=%v, (%v:)", tls, m, origin(2))
	}
	// Only statically initialized mutexes get here and PTHREAD_MUTEX_INITIALIZER
	// is the only static initializer illumos has.
	return pthread.PTHREAD_MUTEX_NORMAL
}

func X__ccgo_pthreadAttrGetDetachState(tls *TLS, a uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v, (%v:)", tls, a, origin(2))
	}
	return (*pthreadAttr)(unsafe.Pointer(a)).detachState
}

// int pthread_attr_getdetachstate(const pthread_attr_t *attr, int *detachstate);
func Xpthread_attr_getdetachstate(tls *TLS, a uintptr, state uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v state=%v, (%v:)", tls, a, state, origin(2))
	}
	*(*int32)(unsafe.Pointer(state)) = (*pthreadAttr)(unsafe.Pointer(a)).detachState
	return 0
}

// int pthread_attr_setdetachstate(pthread_attr_t *attr, int detachstate);
func Xpthread_attr_setdetachstate(tls *TLS, a uintptr, state int32) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v state=%v, (%v:)", tls, a, state, origin(2))
	}
	switch state {
	case pthread.PTHREAD_CREATE_JOINABLE, pthread.PTHREAD_CREATE_DETACHED:
		(*pthreadAttr)(unsafe.Pointer(a)).detachState = state
		return 0
	default:
		return errno.EINVAL
	}
}

// int pthread_mutexattr_destroy(pthread_mutexattr_t *attr);
func Xpthread_mutexattr_destroy(tls *TLS, a uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v, (%v:)", tls, a, origin(2))
	}
	return 0
}

// int pthread_mutexattr_init(pthread_mutexattr_t *attr);
func Xpthread_mutexattr_init(tls *TLS, a uintptr) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v, (%v:)", tls, a, origin(2))
	}
	*(*pthreadMutexattr)(unsafe.Pointer(a)) = pthreadMutexattr{typ: pthread.PTHREAD_MUTEX_DEFAULT}
	return 0
}

// int pthread_mutexattr_settype(pthread_mutexattr_t *attr, int type);
func Xpthread_mutexattr_settype(tls *TLS, a uintptr, type1 int32) int32 {
	if __ccgo_strace {
		trc("tls=%v a=%v type1=%v, (%v:)", tls, a, type1, origin(2))
	}
	switch type1 {
	case pthread.PTHREAD_MUTEX_NORMAL, pthread.PTHREAD_MUTEX_ERRORCHECK, pthread.PTHREAD_MUTEX_RECURSIVE:
		(*pthreadMutexattr)(unsafe.Pointer(a)).typ = type1
		return 0
	default:
		return errno.EINVAL
	}
}
