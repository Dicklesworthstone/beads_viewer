// Copyright 2017 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package sqlite is a sql/database driver using a CGo-free port of the C
// SQLite3 library.
//
// SQLite is an in-process implementation of a self-contained, serverless,
// zero-configuration, transactional SQL database engine.
//
// # Pluggable page cache
//
// The package exposes a Go-facing wrapper for SQLite's
// SQLITE_CONFIG_PCACHE2 mechanism. Applications can supply their own
// page cache implementation by registering a [PageCache] before the
// first [sql.Open] via [RegisterPageCache]. See the docstrings
// on [PageCache], [Cache], and [Page] for the contract; the binding
// owns the sqlite3_pcache_page stub on behalf of the implementation
// and re-consults Cache.Fetch on every SQLite request, so a bounded
// and evicting purgeable cache works as the C contract intends.
//
// # OFD locking (Linux)
//
// On Linux the library can take Open File Description (OFD) locks instead of
// POSIX record locks on database files, which stops an unrelated os.File
// close anywhere in the process from silently stripping SQLite's transaction
// locks. The switch is process-wide, off by default, and must happen before
// the first connection is opened: set MODERNC_SQLITE_OFD_LOCK=1 in the
// environment the process starts with, or call [OFDLocking] from Go. See the
// [OFDLocking] documentation for the full contract.
//
// # Syncing with fdatasync
//
// SQLite commits with fsync(2), which on a journaling file system also
// flushes the file's timestamps through the journal. The library can sync
// with fdatasync(2) instead, which skips that and keeps a commit just as
// durable. The switch is process-wide, off by default, and honored on Linux,
// FreeBSD, illumos and NetBSD: call [Fdatasync] from Go at any time;
// [FdatasyncEnabled] reports the setting. See the [Fdatasync] documentation
// for the full contract.
//
// # Fragile modernc.org/libc dependency
//
// When you import this package you should use in your go.mod file the exact
// same version of modernc.org/libc as seen in the go.mod file of this
// repository.
//
// See the discussion at https://gitlab.com/cznic/sqlite/-/issues/177 for more details.
//
// # Changelog
//
// Release notes are kept in CHANGELOG.md in the repository root, see
// https://gitlab.com/cznic/sqlite/-/blob/master/CHANGELOG.md.
//
// # Thanks
//
// This project is sponsored by Schleibinger Geräte Teubert u. Greim GmbH by
// allowing one of the maintainers to work on it also in office hours.
//
// # Supported platforms and architectures
//
// These combinations of GOOS and GOARCH are currently supported
//
//	OS      Arch    SQLite version
//	------------------------------
//	darwin	amd64   3.54.1
//	darwin	arm64   3.54.1
//	freebsd	386     3.54.1
//	freebsd	amd64   3.54.1
//	freebsd	arm     3.54.1
//	freebsd	arm64   3.54.1
//	illumos	amd64   3.54.1
//	linux	386     3.54.1
//	linux	amd64   3.54.1
//	linux	arm     3.54.1
//	linux	arm64   3.54.1
//	linux	loong64 3.54.1
//	linux	ppc64le 3.54.1
//	linux	riscv64 3.54.1
//	linux	s390x   3.54.1
//	netbsd	amd64   3.54.1
//	openbsd	amd64   3.54.1
//	openbsd	arm64   3.54.1
//	windows	386     3.54.1
//	windows	amd64   3.54.1
//	windows	arm64   3.54.1
//
// # Benchmarks
//
// [The SQLite Drivers Benchmarks Game]
//
// # Performance
//
// The transpiled SQLite core runs slower than the same C compiled natively.
// The gap is in CPU-bound work: the bytecode interpreter loop, b-tree page
// balancing and record building. I/O-bound work is dominated by the operating
// system either way. The ratios below are CPU time per query, measured in
// September 2026 on linux/amd64 with Go 1.27 and modernc.org/libc v1.75.7,
// against SQLite 3.53.4 compiled with the same compile-time options this
// package uses:
//
//	Workload                                                        Driver vs C
//	-------------------------------------------------------------------------
//	Unindexed ORDER BY ... LIMIT 100 over 584k rows of 23 columns      2.0x
//	GROUP BY aggregate over the same table                             1.9x
//	Correlated subquery walking an index with text comparisons         1.3x
//
// Throughput across four connections scaled at least as well as the C build
// did, so the ratios hold under concurrency.
//
// Two things follow. First, this package uses the same query planner as C
// SQLite, so a query that is slow in C is slower here by the ratio above and
// no more; but a missing index costs the same ratio more, and a query that is
// merely sluggish in C can cross a deadline here. Check EXPLAIN QUERY PLAN for
// USE TEMP B-TREE and index the columns that ORDER BY, GROUP BY and WHERE use.
// Second, database/sql opens connections without limit by default. Each
// connection carries its own page cache and its own libc thread state, and a
// periodic query that takes longer than its period piles up without bound.
// Bound the pool with [sql.DB.SetMaxOpenConns] and do not issue a periodic
// query before the previous one has returned.
//
// Part of the gap is in modernc.org/libc rather than in the transpiled SQLite.
// On Linux, libc versions before v1.75.7 implemented memcpy, memmove, memset
// and memcmp as transpiled musl loops moving at most four bytes per step;
// v1.75.7 replaced them with native Go routines backed by the runtime's
// vectorized memmove, and the three workloads above went from 3.0x, 2.2x and
// 1.6x to the figures shown. The libc version this package is validated
// against is the one pinned in its go.mod, see "Fragile modernc.org/libc
// dependency" above. On the non-Linux targets memcpy and memmove are native Go
// copies already; memcmp there is still a byte loop.
//
// Because everything is Go, the usual Go tooling reaches into the SQLite core:
// a CPU profile taken with runtime/pprof attributes time to the transpiled
// SQLite functions under their C names, for example lib._balance_nonroot or
// lib.Xsqlite3_step, and to the libc routines they call.
//
// # Builders
//
// Builder results available at:
//
// https://modern-c.appspot.com/-/builder/?importpath=modernc.org%2fsqlite
//
// # Connecting to a database
//
// To access a Sqlite database do something like
//
//	import (
//		"database/sql"
//
//		_ "modernc.org/sqlite"
//	)
//
//	...
//
//
//	db, err := sql.Open("sqlite", dsnURI)
//
//	...
//
// The dsnURI is a plain file name or a "file:" URI, either optionally followed
// by '?' and query parameters. [Driver.Open] documents the parameters the
// driver interprets and how SQLite's own URI parameters, such as mode=ro,
// reach SQLite.
//
// [NewConnector] is an alternative entry point returning a
// [driver.Connector] for use with [sql.OpenDB]. It opens the same
// connections sql.Open does, from the same driver, and exists for callers that
// need to interpose on them -- tracing, metrics, or connection-scoped setup --
// which sql.Open gives no access to. See its docstring for an example.
//
// Connection-scoped state outlives the caller that set it. A [sql.DB] is a
// pool, and a physical connection returned to it keeps whatever was done on
// it: PRAGMAs set with Exec, ATTACHed databases, temporary tables, and
// anything registered through [sql.Conn.Raw]. The next caller to borrow that
// connection inherits it; the driver does not reset it between borrowers. For
// state every connection should have, use DSN parameters or a connection hook,
// which apply to each connection as it is opened. For state only one caller
// should see, hold a [sql.Conn] for as long as it is needed and undo it, or
// close that connection, before releasing it.
//
// A driver connection reached through [sql.Conn.Raw] is not safe for
// concurrent use, and must not be used after the function passed to Raw
// returns; two goroutines using one connection can corrupt memory. Every
// connection is opened with SQLITE_OPEN_NOMUTEX, SQLite's multi-thread
// mode: SQLite keeps connections that share a cache apart, but does not
// serialize calls on one connection. Nor did SQLITE_OPEN_FULLMUTEX, which
// connections were opened with up to v1.62.0: every call on a connection
// runs on the same [modernc.org/libc.TLS], and SQLite's connection mutex
// lets in any caller on the TLS that holds it.
//
// # Debug and development versions
//
// The transpiled SQLite sources under lib/, and the sqlite-vec sources under
// vec/, are not generated in this repository. They are produced by
// modernc.org/libsqlite3 and modernc.org/libsqlite_vec respectively, which own
// the transpilation and the SQLite compile-time options it uses, and are
// copied here by
//
//	$ make vendor
//
// which reads them from checkouts of those two repositories placed next to
// this one. To build a debug or otherwise modified version, adjust the
// compile-time options in modernc.org/libsqlite3, regenerate there with 'make
// generate', and vendor the result here with
//
//	$ make vendor VENDORFLAGS=-allow-dirty
//
// since plain 'make vendor' refuses a checkout with uncommitted changes. The
// vendor.json it writes records the checkout as dirty, so the last step of
// make vendor and TestVendorStamp both fail, by design: such a tree is for
// local use and must not be committed or released.
//
// # Hacking
//
// This is an example of how to use the debug logs in modernc.org/libc when hunting a bug.
//
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ git status
//	On branch master
//	Your branch is up to date with 'origin/master'.
//
//	nothing to commit, working tree clean
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ git log -1
//	commit df33b8d15107f3cc777799c0fe105f74ef499e62 (HEAD -> master, tag: v1.21.1, origin/master, origin/HEAD, wips, ok)
//	Author: Jan Mercl <0xjnml@gmail.com>
//	Date:   Mon Mar 27 16:18:28 2023 +0200
//
//	    upgrade to SQLite 3.41.2
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ rm -f /tmp/libc.log ; go test -v -tags=libc.dmesg -run TestScalar ; ls -l /tmp/libc.log
//	test binary compiled for linux/amd64
//	=== RUN   TestScalar
//	--- PASS: TestScalar (0.09s)
//	PASS
//	ok  modernc.org/sqlite 0.128s
//	-rw-r--r-- 1 jnml jnml 76 Apr  6 11:22 /tmp/libc.log
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ cat /tmp/libc.log
//	[10723 sqlite.test] 2023-04-06 11:22:48.288066057 +0200 CEST m=+0.000707150
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$
//
// The /tmp/libc.log file is created as requested. No useful messages there because none are enabled in libc. Let's try to enable Xwrite as an example.
//
//	0:jnml@e5-1650:~/src/modernc.org/libc$ git status
//	On branch master
//	Your branch is up to date with 'origin/master'.
//
//	Changes not staged for commit:
//	  (use "git add <file>..." to update what will be committed)
//	  (use "git restore <file>..." to discard changes in working directory)
//	modified:   libc_linux.go
//
//	no changes added to commit (use "git add" and/or "git commit -a")
//	0:jnml@e5-1650:~/src/modernc.org/libc$ git log -1
//	commit 1e22c18cf2de8aa86d5b19b165f354f99c70479c (HEAD -> master, tag: v1.22.3, origin/master, origin/HEAD)
//	Author: Jan Mercl <0xjnml@gmail.com>
//	Date:   Wed Feb 22 20:27:45 2023 +0100
//
//	    support sqlite 3.41 on linux targets
//	0:jnml@e5-1650:~/src/modernc.org/libc$ git diff
//	diff --git a/libc_linux.go b/libc_linux.go
//	index 1c2f482..ac1f08d 100644
//	--- a/libc_linux.go
//	+++ b/libc_linux.go
//	@@ -332,19 +332,19 @@ func Xwrite(t *TLS, fd int32, buf uintptr, count types.Size_t) types.Ssize_t {
//	                var n uintptr
//	                switch n, _, err = unix.Syscall(unix.SYS_WRITE, uintptr(fd), buf, uintptr(count)); err {
//	                case 0:
//	-                       // if dmesgs {
//	-                       //      // dmesg("%v: %d %#x: %#x\n%s", origin(1), fd, count, n, hex.Dump(GoBytes(buf, int(n))))
//	-                       //      dmesg("%v: %d %#x: %#x", origin(1), fd, count, n)
//	-                       // }
//	+                       if dmesgs {
//	+                               // dmesg("%v: %d %#x: %#x\n%s", origin(1), fd, count, n, hex.Dump(GoBytes(buf, int(n))))
//	+                               dmesg("%v: %d %#x: %#x", origin(1), fd, count, n)
//	+                       }
//	                        return types.Ssize_t(n)
//	                case errno.EAGAIN:
//	                        // nop
//	                }
//	        }
//
//	-       // if dmesgs {
//	-       //      dmesg("%v: fd %v, count %#x: %v", origin(1), fd, count, err)
//	-       // }
//	+       if dmesgs {
//	+               dmesg("%v: fd %v, count %#x: %v", origin(1), fd, count, err)
//	+       }
//	        t.setErrno(err)
//	        return -1
//	 }
//	0:jnml@e5-1650:~/src/modernc.org/libc$
//
// We need to tell the Go build system to use our local, patched/debug libc.
// 'make work' sets up a go.work covering this and the sibling repositories;
// by hand it is:
//
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ go work use $(go env GOPATH)/src/modernc.org/libc
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ go work use .
//
// And run the test again:
//
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ rm -f /tmp/libc.log ; go test -v -tags=libc.dmesg -run TestScalar ; ls -l /tmp/libc.log
//	test binary compiled for linux/amd64
//	=== RUN   TestScalar
//	--- PASS: TestScalar (0.26s)
//	PASS
//	ok   modernc.org/sqlite 0.285s
//	-rw-r--r-- 1 jnml jnml 918 Apr  6 11:29 /tmp/libc.log
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$ cat /tmp/libc.log
//	[11910 sqlite.test] 2023-04-06 11:29:13.143589542 +0200 CEST m=+0.000689270
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x200: 0x200
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0xc: 0xc
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 7 0x1000: 0x1000
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 7 0x1000: 0x1000
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x200: 0x200
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x4: 0x4
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x1000: 0x1000
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x4: 0x4
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x4: 0x4
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x1000: 0x1000
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0x4: 0x4
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 8 0xc: 0xc
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 7 0x1000: 0x1000
//	[11910 sqlite.test] libc_linux.go:337:Xwrite: 7 0x1000: 0x1000
//	0:jnml@e5-1650:~/src/modernc.org/sqlite$
//
// # Sqlite documentation
//
// See https://sqlite.org/docs.html
//
// [The SQLite Drivers Benchmarks Game]: https://pkg.go.dev/modernc.org/sqlite-bench#readme-tl-dr-scorecard
package sqlite // import "modernc.org/sqlite"
