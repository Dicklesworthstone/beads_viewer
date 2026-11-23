// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

import (
	"context"
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// busyConns maps a connection handle to its conn, so that busyHandler finds
// the context of the call in progress.
var busyConns sync.Map // uintptr -> *conn

// The retry schedule of SQLite's own busy callback, sqliteDefaultBusyCallback:
// the delay before the n-th retry and the total time waited before it, in ms.
var (
	busyDelays = [...]int32{1, 2, 5, 10, 15, 20, 25, 25, 25, 50, 50, 100}
	busyTotals = [...]int32{0, 1, 3, 8, 18, 33, 53, 78, 103, 128, 178, 228}
)

// ensureBusyHandler replaces SQLite's default busy callback, installed by
// PRAGMA busy_timeout, with busyHandler, which keeps the same schedule and
// timeout but gives up once the context of the call in progress is done.
// PRAGMA busy_timeout installs the default callback again each time it runs,
// so this is checked at the start of every call with a context: one pointer
// comparison when nothing changed.
func (c *conn) ensureBusyHandler() {
	db := (*sqlite3.Tsqlite3)(unsafe.Pointer(c.db))
	if h := db.FbusyHandler.FxBusyHandler; h == 0 || h == cFuncPointer(busyHandler) || db.FbusyTimeout <= 0 {
		return
	}

	// sqlite3_busy_handler clears busyTimeout, which PRAGMA busy_timeout
	// reports and busyHandler reads; keep it.
	tmout := db.FbusyTimeout
	busyConns.Store(c.db, c)
	sqlite3.Xsqlite3_busy_handler(c.tls, c.db, cFuncPointer(busyHandler), c.db)
	db.FbusyTimeout = tmout
}

// busyHandler is sqliteDefaultBusyCallback with one addition: the sleep ends,
// and the handler gives up, once the context of the call in progress is done.
// It runs on the goroutine executing the call, so reading waitCtx needs no
// lock.
func busyHandler(_ *libc.TLS, pArg uintptr, count int32) int32 {
	db := (*sqlite3.Tsqlite3)(unsafe.Pointer(pArg))
	tmout := db.FbusyTimeout
	n := int32(len(busyDelays))
	var delay, prior int32
	if count < n {
		delay, prior = busyDelays[count], busyTotals[count]
	} else {
		delay = busyDelays[n-1]
		prior = busyTotals[n-1] + delay*(count-(n-1))
	}
	if prior+delay > tmout {
		if delay = tmout - prior; delay <= 0 {
			return 0
		}
	}

	var c *conn
	var ctx context.Context
	if v, ok := busyConns.Load(pArg); ok {
		c = v.(*conn)
		ctx = c.waitCtx
	}
	d := time.Duration(delay) * time.Millisecond
	if ctx == nil || ctx.Done() == nil {
		time.Sleep(d)
		return 1
	}

	t := time.NewTimer(d)
	select {
	case <-ctx.Done():
		t.Stop()
		c.canceled.Store(1)
		return 0
	case <-t.C:
		return 1
	}
}
