// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package sqlite // import "modernc.org/sqlite"

// ofdLocking mirrors the unix implementation where the transpiled library
// does not export modernc_ofd_locking(): os_win.c compiles none of the unix
// locking code, and OFD locks are a Linux facility in any case.
func ofdLocking(onoff int32) int32 {
	return -1
}
