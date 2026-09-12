// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import "errors"

// ErrOFDLockingUnavailable is returned by [OFDLocking] where OFD locks do
// not exist: on every platform but Linux, and on Linux after the kernel or
// the filesystem has rejected them and the library has fallen back to POSIX
// locks for the remainder of the process.
var ErrOFDLockingUnavailable = errors.New(
	"sqlite: OFD locking is not available on this platform or kernel")

// ErrOFDLockingTooLate is returned by [OFDLocking] when the locking mode can
// no longer be changed: a database file lock has already been attempted in
// this process, and from the first lock attempt on the mode is fixed,
// because locks of the two kinds do not release one another and switching
// with locks in the wild would leave them behind. Enable OFD locking before
// the first connection is opened.
var ErrOFDLockingTooLate = errors.New(
	"sqlite: OFDLocking called after the first database file lock in this process")

// OFDLocking switches SQLite between POSIX record locks (the default) and
// Linux Open File Description (OFD) locks for database files, process-wide.
// It returns the setting previously in effect; when err is non-nil the
// returned prev is meaningless — use [OFDLockingEnabled] for the current
// state.
//
// A POSIX record lock belongs to the (process, inode) pair: the kernel drops
// every POSIX lock the process holds on a file at any close(2) of any
// descriptor of that file. Code as innocent as
//
//	f, _ := os.Open(dbPath) // hash, back up, inspect, ...
//	f.Close()
//
// anywhere in the process — a third-party library included — therefore
// silently strips SQLite's own transaction locks and leaves the database
// unprotected against other processes. An OFD lock belongs to the open file
// description through which it was placed and survives such a close. OFD
// locking is opt-in and off by default: unless it is enabled, nothing about
// locking changes. See https://gitlab.com/cznic/sqlite/-/issues/255 for
// background and measurements.
//
// The switch is process-wide by necessity, which is why it is not a DSN
// parameter: POSIX and OFD locks taken by one process are different owners
// to the kernel and conflict with each other, so every connection to a given
// database file inside one process must use the same kind.
//
// OFDLocking must be called before the first database file is opened. From
// the process's first lock attempt on, the mode is fixed and calls
// attempting to change it return [ErrOFDLockingTooLate]; querying with
// [OFDLockingEnabled], and calling OFDLocking with the value already in
// effect, always work. The call overrides the MODERNC_SQLITE_OFD_LOCK
// environment variable, which enables OFD locking when set to anything but
// the empty string or a value starting with "0" and which the library reads
// once, at initialization time, from the environment the process started
// with — to switch from Go, prefer this call over os.Setenv.
//
// OFD locks exist on Linux only; everywhere else OFDLocking returns
// [ErrOFDLockingUnavailable]. On Linux kernels older than 3.15, and on
// filesystems that reject OFD locks, the first lock attempt fails with
// EINVAL and the library permanently falls back to POSIX locks: OFDLocking
// returns [ErrOFDLockingUnavailable] from then on and [OFDLockingEnabled]
// reports false, which is how a caller can detect the fallback.
//
// Enabling OFD locking covers the locks on the database file itself; the
// locks coordinating WAL mode through the -shm file remain POSIX locks. And
// one visible behavior change comes with the immunity: code in the same
// process that takes fcntl record locks of its own on a database file used
// to share ownership with SQLite's locks — never conflicting, while quietly
// destroying them — whereas with OFD locking enabled such locks conflict
// with SQLite's and fail loudly instead.
//
// OFDLocking is safe for concurrent use.
func OFDLocking(on bool) (prev bool, err error) {
	arg := int32(0)
	if on {
		arg = 1
	}
	switch rc := ofdLocking(arg); rc {
	case -1:
		return false, ErrOFDLockingUnavailable
	case -2:
		return false, ErrOFDLockingTooLate
	default:
		return rc != 0, nil
	}
}

// OFDLockingEnabled reports whether Linux Open File Description locks are in
// effect for database files in this process; see [OFDLocking]. It reports
// false where OFD locks are unavailable: on every platform but Linux, and on
// Linux once the kernel has rejected them and the library has fallen back to
// POSIX locks.
func OFDLockingEnabled() bool {
	return ofdLocking(-1) == 1
}
