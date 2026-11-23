// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import (
	"sync"

	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
)

// AuthorizerActionCode identifies an operation presented to SQLite's
// connection authorizer while a statement is compiled.
//
// See https://www.sqlite.org/c3ref/c_alter_table.html for the arguments SQLite
// supplies for each action.
type AuthorizerActionCode int32

// Auth* are the actions presented to an [AuthorizerFn].
const (
	AuthCreateIndex       AuthorizerActionCode = sqlite3.SQLITE_CREATE_INDEX
	AuthCreateTable       AuthorizerActionCode = sqlite3.SQLITE_CREATE_TABLE
	AuthCreateTempIndex   AuthorizerActionCode = sqlite3.SQLITE_CREATE_TEMP_INDEX
	AuthCreateTempTable   AuthorizerActionCode = sqlite3.SQLITE_CREATE_TEMP_TABLE
	AuthCreateTempTrigger AuthorizerActionCode = sqlite3.SQLITE_CREATE_TEMP_TRIGGER
	AuthCreateTempView    AuthorizerActionCode = sqlite3.SQLITE_CREATE_TEMP_VIEW
	AuthCreateTrigger     AuthorizerActionCode = sqlite3.SQLITE_CREATE_TRIGGER
	AuthCreateView        AuthorizerActionCode = sqlite3.SQLITE_CREATE_VIEW
	AuthDelete            AuthorizerActionCode = sqlite3.SQLITE_DELETE
	AuthDropIndex         AuthorizerActionCode = sqlite3.SQLITE_DROP_INDEX
	AuthDropTable         AuthorizerActionCode = sqlite3.SQLITE_DROP_TABLE
	AuthDropTempIndex     AuthorizerActionCode = sqlite3.SQLITE_DROP_TEMP_INDEX
	AuthDropTempTable     AuthorizerActionCode = sqlite3.SQLITE_DROP_TEMP_TABLE
	AuthDropTempTrigger   AuthorizerActionCode = sqlite3.SQLITE_DROP_TEMP_TRIGGER
	AuthDropTempView      AuthorizerActionCode = sqlite3.SQLITE_DROP_TEMP_VIEW
	AuthDropTrigger       AuthorizerActionCode = sqlite3.SQLITE_DROP_TRIGGER
	AuthDropView          AuthorizerActionCode = sqlite3.SQLITE_DROP_VIEW
	AuthInsert            AuthorizerActionCode = sqlite3.SQLITE_INSERT
	AuthPragma            AuthorizerActionCode = sqlite3.SQLITE_PRAGMA
	AuthRead              AuthorizerActionCode = sqlite3.SQLITE_READ
	AuthSelect            AuthorizerActionCode = sqlite3.SQLITE_SELECT
	AuthTransaction       AuthorizerActionCode = sqlite3.SQLITE_TRANSACTION
	AuthUpdate            AuthorizerActionCode = sqlite3.SQLITE_UPDATE
	AuthAttach            AuthorizerActionCode = sqlite3.SQLITE_ATTACH
	AuthDetach            AuthorizerActionCode = sqlite3.SQLITE_DETACH
	AuthAlterTable        AuthorizerActionCode = sqlite3.SQLITE_ALTER_TABLE
	AuthReindex           AuthorizerActionCode = sqlite3.SQLITE_REINDEX
	AuthAnalyze           AuthorizerActionCode = sqlite3.SQLITE_ANALYZE
	AuthCreateVTable      AuthorizerActionCode = sqlite3.SQLITE_CREATE_VTABLE
	AuthDropVTable        AuthorizerActionCode = sqlite3.SQLITE_DROP_VTABLE
	AuthFunction          AuthorizerActionCode = sqlite3.SQLITE_FUNCTION
	AuthSavepoint         AuthorizerActionCode = sqlite3.SQLITE_SAVEPOINT
	AuthRecursive         AuthorizerActionCode = sqlite3.SQLITE_RECURSIVE
)

// AuthorizerReturnCode is a result returned by an [AuthorizerFn].
type AuthorizerReturnCode int32

// Authorizer return codes control how SQLite handles an operation.
const (
	AuthorizerOK     AuthorizerReturnCode = sqlite3.SQLITE_OK
	AuthorizerDeny   AuthorizerReturnCode = sqlite3.SQLITE_DENY
	AuthorizerIgnore AuthorizerReturnCode = sqlite3.SQLITE_IGNORE
)

// AuthorizerFn decides whether SQLite may compile an operation. The arguments
// have the same meaning as the third through sixth arguments of SQLite's
// authorizer callback. A callback must not panic or modify the SQLite
// connection that invoked it, including by preparing or stepping SQL on that
// connection.
//
// SQLite NULL callback arguments are passed as empty strings, so AuthorizerFn
// cannot distinguish NULL from an actual empty string.
//
// SQLite invokes the callback during statement preparation and may invoke it
// again when a statement is automatically reprepared. The strings are owned by
// Go and remain valid after the callback returns. The authorizer also applies
// to statements the driver prepares on the connection. In particular, BEGIN,
// COMMIT, and ROLLBACK are presented as [AuthTransaction], so denying that
// action can make BeginTx or transaction completion fail.
type AuthorizerFn func(action AuthorizerActionCode, arg1, arg2, databaseName, triggerOrView string) AuthorizerReturnCode

// AuthorizerRegisterer exposes the authorizer of a physical SQLite connection.
// To install one policy consistently on every physical connection in a
// database/sql pool, use [RegisterConnectionHook] before opening any
// connections:
//
//	sqlite.RegisterConnectionHook(func(driverConn sqlite.ExecQuerierContext, _ string) error {
//		registerer, ok := driverConn.(sqlite.AuthorizerRegisterer)
//		if !ok {
//			return fmt.Errorf("driver does not support an authorizer")
//		}
//		return registerer.RegisterAuthorizer(fn)
//	})
//	db, err := sql.Open("sqlite", dsn)
//
// Use [Driver.RegisterConnectionHook] instead for a caller-constructed Driver.
// The hook runs once for each newly opened connection. Install the policy in
// the hook and do not retain driverConn after the hook returns.
//
// For a policy intentionally limited to one physical connection, use
// database/sql's [database/sql.Conn.Raw] escape hatch:
//
//	err := sqlConn.Raw(func(driverConn any) error {
//		registerer, ok := driverConn.(sqlite.AuthorizerRegisterer)
//		if !ok {
//			return fmt.Errorf("driver does not support an authorizer")
//		}
//		return registerer.RegisterAuthorizer(fn)
//	})
//
// RegisterAuthorizer must be called within the Raw callback; the underlying
// driver connection must not be used after the callback returns. The installed
// policy remains on that physical connection after sqlConn is returned to the
// pool, so Raw is not a substitute for the connection-hook pattern when every
// pooled connection must share one policy.
//
// RegisterAuthorizer replaces any authorizer already installed on the
// connection. Passing nil removes it. The callback remains installed until it
// is replaced, removed, or the connection is closed. Installing, replacing, or
// removing an authorizer expires prepared statements on that connection;
// SQLite transparently prepares them again on next use and authorizes them
// under the then-current policy.
//
// The callback bridge is fail-closed: if SQLite invokes it when no Go callback
// is registered for the SQLite handle, it returns [AuthorizerDeny].
type AuthorizerRegisterer interface {
	RegisterAuthorizer(AuthorizerFn) error
}

var xAuthorizers = struct {
	mu sync.RWMutex
	m  map[uintptr]AuthorizerFn
}{m: make(map[uintptr]AuthorizerFn)}

var _ AuthorizerRegisterer = (*conn)(nil)

func (c *conn) RegisterAuthorizer(fn AuthorizerFn) error {
	if fn == nil {
		xAuthorizers.mu.Lock()
		delete(xAuthorizers.m, c.db)
		xAuthorizers.mu.Unlock()
		if rc := sqlite3.Xsqlite3_set_authorizer(c.tls, c.db, 0, 0); rc != sqlite3.SQLITE_OK {
			return c.errstr(rc)
		}
		return nil
	}

	xAuthorizers.mu.Lock()
	xAuthorizers.m[c.db] = fn
	xAuthorizers.mu.Unlock()
	if rc := sqlite3.Xsqlite3_set_authorizer(c.tls, c.db, cFuncPointer(authorizerTrampoline), c.db); rc != sqlite3.SQLITE_OK {
		xAuthorizers.mu.Lock()
		delete(xAuthorizers.m, c.db)
		xAuthorizers.mu.Unlock()
		return c.errstr(rc)
	}
	return nil
}

func unregisterAuthorizer(db uintptr) {
	xAuthorizers.mu.Lock()
	delete(xAuthorizers.m, db)
	xAuthorizers.mu.Unlock()
}

func authorizerTrampoline(_ *libc.TLS, db uintptr, action int32, z1, z2, zDatabaseName, zTriggerOrView uintptr) int32 {
	xAuthorizers.mu.RLock()
	fn := xAuthorizers.m[db]
	xAuthorizers.mu.RUnlock()
	if fn == nil {
		return sqlite3.SQLITE_DENY
	}

	toString := func(p uintptr) string {
		if p == 0 {
			return ""
		}
		return libc.GoString(p)
	}
	return int32(fn(
		AuthorizerActionCode(action),
		toString(z1),
		toString(z2),
		toString(zDatabaseName),
		toString(zTriggerOrView),
	))
}
