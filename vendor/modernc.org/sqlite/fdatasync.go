// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import "errors"

// ErrFdatasyncUnavailable is returned by [Fdatasync] where the library never
// syncs with fdatasync(2): on Windows, and on macOS, where SQLite syncs with
// the F_FULLFSYNC fcntl or fsync(2) instead.
var ErrFdatasyncUnavailable = errors.New(
	"sqlite: fdatasync is not available on this platform")

// Fdatasync switches the system call SQLite makes to commit a transaction to
// stable storage between fsync(2), the default, and fdatasync(2),
// process-wide. It returns the setting previously in effect; when err is
// non-nil the returned prev is meaningless, use [FdatasyncEnabled] for the
// current state.
//
// fdatasync writes the file's data and the metadata needed to read it back,
// its size included, but not its timestamps; on a journaling file system
// such as ext4 the timestamp update fsync forces through the journal is
// often what a commit waits for, so a commit can take half the time with no
// change in durability. SQLite itself uses fdatasync where its configure
// script finds it, which is every Linux distribution's build; the library in
// this module is transpiled from the amalgamation without that check and
// keeps SQLite's source default, fsync. Switching is opt-in because the
// different system call on every commit is observable: a seccomp allowlist
// that permits fsync but not fdatasync, say, would turn every commit into a
// disk I/O error.
//
// The switch is honored on Linux, FreeBSD, illumos and NetBSD. On OpenBSD
// the C library implements fdatasync as fsync, so the call succeeds and
// changes nothing. On macOS and Windows Fdatasync returns
// [ErrFdatasyncUnavailable] and the setting stays off. It may be changed at
// any time: every sync stands alone. Connections already open see the change
// too.
//
// Fdatasync is safe for concurrent use. See
// https://github.com/modernc-org/sqlite/issues/10 for the measurements.
func Fdatasync(on bool) (prev bool, err error) {
	arg := int32(0)
	if on {
		arg = 1
	}
	rc := fdatasync(arg)
	if rc < 0 {
		return false, ErrFdatasyncUnavailable
	}

	return rc != 0, nil
}

// FdatasyncEnabled reports whether SQLite commits with fdatasync(2) in this
// process; see [Fdatasync]. It reports false where fdatasync is unavailable.
func FdatasyncEnabled() bool {
	return fdatasync(-1) == 1
}
