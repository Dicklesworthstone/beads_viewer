// Copyright 2019 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"modernc.org/libc"
	"modernc.org/libc/sys/types"
)

// mutex is what a connection waiting for a shared-cache lock blocks on, see
// (*conn).retry. It is unlocked once, by wake, either from the unlock-notify
// callback or because the context of the waiting call is done.
type mutex struct {
	sync.Mutex
	woken int32
}

// wake unlocks m unless it has already been woken, and reports whether this
// call did it.
func (m *mutex) wake() bool {
	if !atomic.CompareAndSwapInt32(&m.woken, 0, 1) {
		return false
	}

	m.Unlock()
	return true
}

func mutexAlloc(tls *libc.TLS) uintptr {
	return libc.Xcalloc(tls, 1, types.Size_t(unsafe.Sizeof(mutex{})))
}

func mutexFree(tls *libc.TLS, m uintptr) { libc.Xfree(tls, m) }
