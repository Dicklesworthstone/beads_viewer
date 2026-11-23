// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !unix

package sqlite // import "modernc.org/sqlite"

// fdatasync mirrors the unix implementation where the transpiled library
// does not export modernc_fdatasync(): os_win.c has its own sync code.
func fdatasync(onoff int32) int32 {
	return -1
}
