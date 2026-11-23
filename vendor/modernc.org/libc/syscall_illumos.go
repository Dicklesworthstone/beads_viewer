// Copyright 2026 The Libc Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package libc // import "modernc.org/libc"

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// illumos has no stable system call ABI, the system libc is the interface to
// the kernel. The functions below are reached the way golang.org/x/sys/unix
// reaches them: dynamically imported and called through syscall.sysvicall6,
// which runs them on the system stack and reports errno.
//
// The local symbol names must start with "libc_": only for those the Go
// assembler avoids PC-relative addressing, which the linker cannot resolve for
// a dynamic import (see useAbs in cmd/internal/obj/x86). The "libc_ccgo_"
// prefix keeps them apart from the "libc_" symbols of golang.org/x/sys/unix
// and package syscall.

//go:linkname sysvicall6 syscall.sysvicall6
func sysvicall6(trap, nargs, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err unix.Errno)

type libcFunc uintptr

// libcall calls the system libc function fn with args, at most 6 of them.
// Pointer arguments must not point into Go memory that can move.
func libcall(fn *libcFunc, args ...uintptr) (r uintptr, err unix.Errno) {
	var a [6]uintptr
	copy(a[:], args)
	r, _, err = sysvicall6(uintptr(unsafe.Pointer(fn)), uintptr(len(args)), a[0], a[1], a[2], a[3], a[4], a[5])
	return r, err
}

// libcall32 is libcall for functions returning int. It sets t's errno and
// returns -1 when fn returns -1.
func (t *TLS) libcall32(fn *libcFunc, args ...uintptr) int32 {
	r, err := libcall(fn, args...)
	if int32(r) == -1 {
		t.setErrno(err)
		return -1
	}

	return int32(r)
}

// libcall64 is libcall for functions returning ssize_t or off_t. It sets t's
// errno and returns -1 when fn returns -1.
func (t *TLS) libcall64(fn *libcFunc, args ...uintptr) int64 {
	r, err := libcall(fn, args...)
	if int64(r) == -1 {
		t.setErrno(err)
		return -1
	}

	return int64(r)
}

//go:cgo_import_dynamic libc_ccgo_accept accept "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_access access "libc.so"
//go:cgo_import_dynamic libc_ccgo_alarm alarm "libc.so"
//go:cgo_import_dynamic libc_ccgo_bind bind "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_cfgetospeed cfgetospeed "libc.so"
//go:cgo_import_dynamic libc_ccgo_cfsetispeed cfsetispeed "libc.so"
//go:cgo_import_dynamic libc_ccgo_cfsetospeed cfsetospeed "libc.so"
//go:cgo_import_dynamic libc_ccgo_chdir chdir "libc.so"
//go:cgo_import_dynamic libc_ccgo_chmod chmod "libc.so"
//go:cgo_import_dynamic libc_ccgo_chown chown "libc.so"
//go:cgo_import_dynamic libc_ccgo_clock_gettime clock_gettime "libc.so"
//go:cgo_import_dynamic libc_ccgo_close close "libc.so"
//go:cgo_import_dynamic libc_ccgo_closedir closedir "libc.so"
//go:cgo_import_dynamic libc_ccgo_connect connect "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_dup dup "libc.so"
//go:cgo_import_dynamic libc_ccgo_dup2 dup2 "libc.so"
//go:cgo_import_dynamic libc_ccgo_dup3 dup3 "libc.so"
//go:cgo_import_dynamic libc_ccgo_execve execve "libc.so"
//go:cgo_import_dynamic libc_ccgo_faccessat faccessat "libc.so"
//go:cgo_import_dynamic libc_ccgo_fchmod fchmod "libc.so"
//go:cgo_import_dynamic libc_ccgo_fchmodat fchmodat "libc.so"
//go:cgo_import_dynamic libc_ccgo_fchown fchown "libc.so"
//go:cgo_import_dynamic libc_ccgo_fchownat fchownat "libc.so"
//go:cgo_import_dynamic libc_ccgo_fcntl fcntl "libc.so"
//go:cgo_import_dynamic libc_ccgo_fdatasync fdatasync "libc.so"
//go:cgo_import_dynamic libc_ccgo_freeaddrinfo freeaddrinfo "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_fstat fstat "libc.so"
//go:cgo_import_dynamic libc_ccgo_fstatvfs fstatvfs "libc.so"
//go:cgo_import_dynamic libc_ccgo_fsync fsync "libc.so"
//go:cgo_import_dynamic libc_ccgo_ftruncate ftruncate "libc.so"
//go:cgo_import_dynamic libc_ccgo_gai_strerror gai_strerror "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_getaddrinfo getaddrinfo "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_getcwd getcwd "libc.so"
//go:cgo_import_dynamic libc_ccgo_getpeername getpeername "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_getrandom getrandom "libc.so"
//go:cgo_import_dynamic libc_ccgo_getrlimit getrlimit "libc.so"
//go:cgo_import_dynamic libc_ccgo_getrusage getrusage "libc.so"
//go:cgo_import_dynamic libc_ccgo_getsockname getsockname "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_getsockopt getsockopt "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_inet_ntop inet_ntop "libc.so"
//go:cgo_import_dynamic libc_ccgo_inet_pton inet_pton "libc.so"
//go:cgo_import_dynamic libc_ccgo_ioctl ioctl "libc.so"
//go:cgo_import_dynamic libc_ccgo_link link "libc.so"
//go:cgo_import_dynamic libc_ccgo_linkat linkat "libc.so"
//go:cgo_import_dynamic libc_ccgo_listen listen "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_lseek lseek "libc.so"
//go:cgo_import_dynamic libc_ccgo_lstat lstat "libc.so"
//go:cgo_import_dynamic libc_ccgo_mkdir mkdir "libc.so"
//go:cgo_import_dynamic libc_ccgo_mkdirat mkdirat "libc.so"
//go:cgo_import_dynamic libc_ccgo_mknod mknod "libc.so"
//go:cgo_import_dynamic libc_ccgo_mknodat mknodat "libc.so"
//go:cgo_import_dynamic libc_ccgo_mmap mmap "libc.so"
//go:cgo_import_dynamic libc_ccgo_munmap munmap "libc.so"
//go:cgo_import_dynamic libc_ccgo_open open "libc.so"
//go:cgo_import_dynamic libc_ccgo_openat openat "libc.so"
//go:cgo_import_dynamic libc_ccgo_opendir opendir "libc.so"
//go:cgo_import_dynamic libc_ccgo_pipe pipe "libc.so"
//go:cgo_import_dynamic libc_ccgo_pipe2 pipe2 "libc.so"
//go:cgo_import_dynamic libc_ccgo_posix_fadvise posix_fadvise "libc.so"
//go:cgo_import_dynamic libc_ccgo_pread pread "libc.so"
//go:cgo_import_dynamic libc_ccgo_pwrite pwrite "libc.so"
//go:cgo_import_dynamic libc_ccgo_raise raise "libc.so"
//go:cgo_import_dynamic libc_ccgo_read read "libc.so"
//go:cgo_import_dynamic libc_ccgo_readdir readdir "libc.so"
//go:cgo_import_dynamic libc_ccgo_readlink readlink "libc.so"
//go:cgo_import_dynamic libc_ccgo_readlinkat readlinkat "libc.so"
//go:cgo_import_dynamic libc_ccgo_readv readv "libc.so"
//go:cgo_import_dynamic libc_ccgo_recv recv "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_recvfrom recvfrom "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_recvmsg recvmsg "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_rename rename "libc.so"
//go:cgo_import_dynamic libc_ccgo_renameat renameat "libc.so"
//go:cgo_import_dynamic libc_ccgo_rewinddir rewinddir "libc.so"
//go:cgo_import_dynamic libc_ccgo_rmdir rmdir "libc.so"
//go:cgo_import_dynamic libc_ccgo_seekdir seekdir "libc.so"
//go:cgo_import_dynamic libc_ccgo_send send "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_sendmsg sendmsg "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_sendto sendto "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_setrlimit setrlimit "libc.so"
//go:cgo_import_dynamic libc_ccgo_setsockopt setsockopt "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_shutdown shutdown "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_sigaction sigaction "libc.so"
//go:cgo_import_dynamic libc_ccgo_sigprocmask sigprocmask "libc.so"
//go:cgo_import_dynamic libc_ccgo_socket socket "libsocket.so"
//go:cgo_import_dynamic libc_ccgo_stat stat "libc.so"
//go:cgo_import_dynamic libc_ccgo_symlink symlink "libc.so"
//go:cgo_import_dynamic libc_ccgo_symlinkat symlinkat "libc.so"
//go:cgo_import_dynamic libc_ccgo_sysconf sysconf "libc.so"
//go:cgo_import_dynamic libc_ccgo_tcgetattr tcgetattr "libc.so"
//go:cgo_import_dynamic libc_ccgo_tcsetattr tcsetattr "libc.so"
//go:cgo_import_dynamic libc_ccgo_telldir telldir "libc.so"
//go:cgo_import_dynamic libc_ccgo_umask umask "libc.so"
//go:cgo_import_dynamic libc_ccgo_uname uname "libc.so"
//go:cgo_import_dynamic libc_ccgo_unlink unlink "libc.so"
//go:cgo_import_dynamic libc_ccgo_unlinkat unlinkat "libc.so"
//go:cgo_import_dynamic libc_ccgo_utime utime "libc.so"
//go:cgo_import_dynamic libc_ccgo_utimensat utimensat "libc.so"
//go:cgo_import_dynamic libc_ccgo_utimes utimes "libc.so"
//go:cgo_import_dynamic libc_ccgo_waitpid waitpid "libc.so"
//go:cgo_import_dynamic libc_ccgo_write write "libc.so"
//go:cgo_import_dynamic libc_ccgo_writev writev "libc.so"

//go:linkname libc_ccgo_accept libc_ccgo_accept
//go:linkname libc_ccgo_access libc_ccgo_access
//go:linkname libc_ccgo_alarm libc_ccgo_alarm
//go:linkname libc_ccgo_bind libc_ccgo_bind
//go:linkname libc_ccgo_cfgetospeed libc_ccgo_cfgetospeed
//go:linkname libc_ccgo_cfsetispeed libc_ccgo_cfsetispeed
//go:linkname libc_ccgo_cfsetospeed libc_ccgo_cfsetospeed
//go:linkname libc_ccgo_chdir libc_ccgo_chdir
//go:linkname libc_ccgo_chmod libc_ccgo_chmod
//go:linkname libc_ccgo_chown libc_ccgo_chown
//go:linkname libc_ccgo_clock_gettime libc_ccgo_clock_gettime
//go:linkname libc_ccgo_close libc_ccgo_close
//go:linkname libc_ccgo_closedir libc_ccgo_closedir
//go:linkname libc_ccgo_connect libc_ccgo_connect
//go:linkname libc_ccgo_dup libc_ccgo_dup
//go:linkname libc_ccgo_dup2 libc_ccgo_dup2
//go:linkname libc_ccgo_dup3 libc_ccgo_dup3
//go:linkname libc_ccgo_execve libc_ccgo_execve
//go:linkname libc_ccgo_faccessat libc_ccgo_faccessat
//go:linkname libc_ccgo_fchmod libc_ccgo_fchmod
//go:linkname libc_ccgo_fchmodat libc_ccgo_fchmodat
//go:linkname libc_ccgo_fchown libc_ccgo_fchown
//go:linkname libc_ccgo_fchownat libc_ccgo_fchownat
//go:linkname libc_ccgo_fcntl libc_ccgo_fcntl
//go:linkname libc_ccgo_fdatasync libc_ccgo_fdatasync
//go:linkname libc_ccgo_freeaddrinfo libc_ccgo_freeaddrinfo
//go:linkname libc_ccgo_fstat libc_ccgo_fstat
//go:linkname libc_ccgo_fstatvfs libc_ccgo_fstatvfs
//go:linkname libc_ccgo_fsync libc_ccgo_fsync
//go:linkname libc_ccgo_ftruncate libc_ccgo_ftruncate
//go:linkname libc_ccgo_gai_strerror libc_ccgo_gai_strerror
//go:linkname libc_ccgo_getaddrinfo libc_ccgo_getaddrinfo
//go:linkname libc_ccgo_getcwd libc_ccgo_getcwd
//go:linkname libc_ccgo_getpeername libc_ccgo_getpeername
//go:linkname libc_ccgo_getrandom libc_ccgo_getrandom
//go:linkname libc_ccgo_getrlimit libc_ccgo_getrlimit
//go:linkname libc_ccgo_getrusage libc_ccgo_getrusage
//go:linkname libc_ccgo_getsockname libc_ccgo_getsockname
//go:linkname libc_ccgo_getsockopt libc_ccgo_getsockopt
//go:linkname libc_ccgo_inet_ntop libc_ccgo_inet_ntop
//go:linkname libc_ccgo_inet_pton libc_ccgo_inet_pton
//go:linkname libc_ccgo_ioctl libc_ccgo_ioctl
//go:linkname libc_ccgo_link libc_ccgo_link
//go:linkname libc_ccgo_linkat libc_ccgo_linkat
//go:linkname libc_ccgo_listen libc_ccgo_listen
//go:linkname libc_ccgo_lseek libc_ccgo_lseek
//go:linkname libc_ccgo_lstat libc_ccgo_lstat
//go:linkname libc_ccgo_mkdir libc_ccgo_mkdir
//go:linkname libc_ccgo_mkdirat libc_ccgo_mkdirat
//go:linkname libc_ccgo_mknod libc_ccgo_mknod
//go:linkname libc_ccgo_mknodat libc_ccgo_mknodat
//go:linkname libc_ccgo_mmap libc_ccgo_mmap
//go:linkname libc_ccgo_munmap libc_ccgo_munmap
//go:linkname libc_ccgo_open libc_ccgo_open
//go:linkname libc_ccgo_openat libc_ccgo_openat
//go:linkname libc_ccgo_opendir libc_ccgo_opendir
//go:linkname libc_ccgo_pipe libc_ccgo_pipe
//go:linkname libc_ccgo_pipe2 libc_ccgo_pipe2
//go:linkname libc_ccgo_posix_fadvise libc_ccgo_posix_fadvise
//go:linkname libc_ccgo_pread libc_ccgo_pread
//go:linkname libc_ccgo_pwrite libc_ccgo_pwrite
//go:linkname libc_ccgo_raise libc_ccgo_raise
//go:linkname libc_ccgo_read libc_ccgo_read
//go:linkname libc_ccgo_readdir libc_ccgo_readdir
//go:linkname libc_ccgo_readlink libc_ccgo_readlink
//go:linkname libc_ccgo_readlinkat libc_ccgo_readlinkat
//go:linkname libc_ccgo_readv libc_ccgo_readv
//go:linkname libc_ccgo_recv libc_ccgo_recv
//go:linkname libc_ccgo_recvfrom libc_ccgo_recvfrom
//go:linkname libc_ccgo_recvmsg libc_ccgo_recvmsg
//go:linkname libc_ccgo_rename libc_ccgo_rename
//go:linkname libc_ccgo_renameat libc_ccgo_renameat
//go:linkname libc_ccgo_rewinddir libc_ccgo_rewinddir
//go:linkname libc_ccgo_rmdir libc_ccgo_rmdir
//go:linkname libc_ccgo_seekdir libc_ccgo_seekdir
//go:linkname libc_ccgo_send libc_ccgo_send
//go:linkname libc_ccgo_sendmsg libc_ccgo_sendmsg
//go:linkname libc_ccgo_sendto libc_ccgo_sendto
//go:linkname libc_ccgo_setrlimit libc_ccgo_setrlimit
//go:linkname libc_ccgo_setsockopt libc_ccgo_setsockopt
//go:linkname libc_ccgo_shutdown libc_ccgo_shutdown
//go:linkname libc_ccgo_sigaction libc_ccgo_sigaction
//go:linkname libc_ccgo_sigprocmask libc_ccgo_sigprocmask
//go:linkname libc_ccgo_socket libc_ccgo_socket
//go:linkname libc_ccgo_stat libc_ccgo_stat
//go:linkname libc_ccgo_symlink libc_ccgo_symlink
//go:linkname libc_ccgo_symlinkat libc_ccgo_symlinkat
//go:linkname libc_ccgo_sysconf libc_ccgo_sysconf
//go:linkname libc_ccgo_tcgetattr libc_ccgo_tcgetattr
//go:linkname libc_ccgo_tcsetattr libc_ccgo_tcsetattr
//go:linkname libc_ccgo_telldir libc_ccgo_telldir
//go:linkname libc_ccgo_umask libc_ccgo_umask
//go:linkname libc_ccgo_uname libc_ccgo_uname
//go:linkname libc_ccgo_unlink libc_ccgo_unlink
//go:linkname libc_ccgo_unlinkat libc_ccgo_unlinkat
//go:linkname libc_ccgo_utime libc_ccgo_utime
//go:linkname libc_ccgo_utimensat libc_ccgo_utimensat
//go:linkname libc_ccgo_utimes libc_ccgo_utimes
//go:linkname libc_ccgo_waitpid libc_ccgo_waitpid
//go:linkname libc_ccgo_write libc_ccgo_write
//go:linkname libc_ccgo_writev libc_ccgo_writev

var (
	libc_ccgo_accept,
	libc_ccgo_access,
	libc_ccgo_alarm,
	libc_ccgo_bind,
	libc_ccgo_cfgetospeed,
	libc_ccgo_cfsetispeed,
	libc_ccgo_cfsetospeed,
	libc_ccgo_chdir,
	libc_ccgo_chmod,
	libc_ccgo_chown,
	libc_ccgo_clock_gettime,
	libc_ccgo_close,
	libc_ccgo_closedir,
	libc_ccgo_connect,
	libc_ccgo_dup,
	libc_ccgo_dup2,
	libc_ccgo_dup3,
	libc_ccgo_execve,
	libc_ccgo_faccessat,
	libc_ccgo_fchmod,
	libc_ccgo_fchmodat,
	libc_ccgo_fchown,
	libc_ccgo_fchownat,
	libc_ccgo_fcntl,
	libc_ccgo_fdatasync,
	libc_ccgo_freeaddrinfo,
	libc_ccgo_fstat,
	libc_ccgo_fstatvfs,
	libc_ccgo_fsync,
	libc_ccgo_ftruncate,
	libc_ccgo_gai_strerror,
	libc_ccgo_getaddrinfo,
	libc_ccgo_getcwd,
	libc_ccgo_getpeername,
	libc_ccgo_getrandom,
	libc_ccgo_getrlimit,
	libc_ccgo_getrusage,
	libc_ccgo_getsockname,
	libc_ccgo_getsockopt,
	libc_ccgo_inet_ntop,
	libc_ccgo_inet_pton,
	libc_ccgo_ioctl,
	libc_ccgo_link,
	libc_ccgo_linkat,
	libc_ccgo_listen,
	libc_ccgo_lseek,
	libc_ccgo_lstat,
	libc_ccgo_mkdir,
	libc_ccgo_mkdirat,
	libc_ccgo_mknod,
	libc_ccgo_mknodat,
	libc_ccgo_mmap,
	libc_ccgo_munmap,
	libc_ccgo_open,
	libc_ccgo_openat,
	libc_ccgo_opendir,
	libc_ccgo_pipe,
	libc_ccgo_pipe2,
	libc_ccgo_posix_fadvise,
	libc_ccgo_pread,
	libc_ccgo_pwrite,
	libc_ccgo_raise,
	libc_ccgo_read,
	libc_ccgo_readdir,
	libc_ccgo_readlink,
	libc_ccgo_readlinkat,
	libc_ccgo_readv,
	libc_ccgo_recv,
	libc_ccgo_recvfrom,
	libc_ccgo_recvmsg,
	libc_ccgo_rename,
	libc_ccgo_renameat,
	libc_ccgo_rewinddir,
	libc_ccgo_rmdir,
	libc_ccgo_seekdir,
	libc_ccgo_send,
	libc_ccgo_sendmsg,
	libc_ccgo_sendto,
	libc_ccgo_setrlimit,
	libc_ccgo_setsockopt,
	libc_ccgo_shutdown,
	libc_ccgo_sigaction,
	libc_ccgo_sigprocmask,
	libc_ccgo_socket,
	libc_ccgo_stat,
	libc_ccgo_symlink,
	libc_ccgo_symlinkat,
	libc_ccgo_sysconf,
	libc_ccgo_tcgetattr,
	libc_ccgo_tcsetattr,
	libc_ccgo_telldir,
	libc_ccgo_umask,
	libc_ccgo_uname,
	libc_ccgo_unlink,
	libc_ccgo_unlinkat,
	libc_ccgo_utime,
	libc_ccgo_utimensat,
	libc_ccgo_utimes,
	libc_ccgo_waitpid,
	libc_ccgo_write,
	libc_ccgo_writev libcFunc
)
