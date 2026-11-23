// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package sqlite // import "modernc.org/sqlite"

import (
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// fdatasync calls the modernc_fdatasync() switch the transpiled library
// exports on every unix target: onoff 1 and 0 select fdatasync(2) and
// fsync(2) for full_fsync(), negative queries; the result is the previous
// setting, -1 where the library never calls fdatasync (darwin). The C side
// lives in modernc.org/libsqlite3's internal/sqlite_fdatasync.patch{,2}.
func fdatasync(onoff int32) int32 {
	tls := libc.NewTLS()
	defer tls.Close()

	return sqlite3.Xmodernc_fdatasync(tls, onoff)
}
