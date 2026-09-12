// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package sqlite // import "modernc.org/sqlite"

import (
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// ofdLocking calls the modernc_ofd_locking() gate the transpiled library
// exports on every unix target: onoff 1 and 0 switch OFD locking on and
// off, negative queries it; the result is the previous setting, -1 where
// OFD locks are unavailable (every unix target but Linux always returns
// this), -2 when the mode is frozen by an already-attempted lock. The C
// side lives in modernc.org/libsqlite3's internal/sqlite_issue255.patch{,2}.
func ofdLocking(onoff int32) int32 {
	tls := libc.NewTLS()
	defer tls.Close()

	return sqlite3.Xmodernc_ofd_locking(tls, onoff)
}
