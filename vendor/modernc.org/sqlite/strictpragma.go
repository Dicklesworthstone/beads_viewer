// Copyright 2026 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite // import "modernc.org/sqlite"

import (
	"errors"
	"strings"
	"sync/atomic"
)

// ErrMultiStatementPragma is returned, wrapped, when [StrictPragmas] is in
// effect and a _pragma DSN value is more than one SQL statement.
var ErrMultiStatementPragma = errors.New(
	"sqlite: _pragma value is more than one SQL statement")

var strictPragmas atomic.Bool

// StrictPragmas makes every connection opened afterwards in this process
// reject a _pragma DSN value that is more than one SQL statement, and
// returns the setting previously in effect. It is off by default.
//
// A _pragma value is run as SQL text with PRAGMA prepended, so without this
// setting anything after a ';' runs too: "_pragma=foreign_keys(1);ATTACH
// 'x.db' AS x" also attaches, and creates, x.db. With it, such a DSN fails
// to open with an error wrapping [ErrMultiStatementPragma], and it fails in
// the validation phase, before any DSN parameter has been applied. Trailing
// semicolons, whitespace and comments are still accepted.
//
// Enabling it is recommended for any application whose DSN is not a
// compile-time constant: one read from a configuration file, an environment
// variable or a command line. It narrows what such a DSN can do to the
// PRAGMAs it names; it does not make an untrusted DSN safe, because a
// single PRAGMA can still change the database file, and the file name and
// the vfs parameter are the DSN's to choose. See [Driver.Open].
//
// The switch is process-wide, and deliberately not a DSN parameter: the DSN
// is what it guards, and whoever can write one could leave the parameter
// out.
//
// StrictPragmas is safe for concurrent use.
func StrictPragmas(on bool) (prev bool) {
	return strictPragmas.Swap(on)
}

// StrictPragmasEnabled reports whether [StrictPragmas] is in effect.
func StrictPragmasEnabled() bool {
	return strictPragmas.Load()
}

// singleStatement reports whether sql holds at most one SQL statement: after
// the ';' that ends the first statement there may only be whitespace, further
// semicolons and comments.
//
// It is lexical on purpose. Preparing the text to let SQLite find the tail
// would compile the statements it is meant to reject, some PRAGMAs take
// effect while they compile, and it would run before busy_timeout is set. The
// rules are those of SQLite's tokenizer: '...', "..." and `...` quote with
// the quote character doubled as the only escape, [...] quotes with no
// escape, -- comments run to the end of the line and /* comments to */, where
// a /* with nothing after it is not a comment but a slash and a star. An
// unterminated quote or comment swallows the rest of the text, which SQLite
// rejects as a single malformed statement. TestSingleStatementAgainstSQLite
// and FuzzSingleStatement hold this to SQLite's own parser.
func singleStatement(sql string) bool {
	// SQLite receives the text as a C string and sees nothing past a NUL.
	if i := strings.IndexByte(sql, 0); i >= 0 {
		sql = sql[:i]
	}

	end := statementEnd(sql)
	if end < 0 {
		return true
	}

	for i := end + 1; i < len(sql); {
		switch c := sql[i]; {
		// Not \v: SQLite accepts it only inside a run of other whitespace,
		// and rejecting it everywhere errs on the safe side.
		case c == ';' || c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r':
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = lineCommentEnd(sql, i)
		case c == '/' && i+2 < len(sql) && sql[i+1] == '*':
			i = blockCommentEnd(sql, i)
		default:
			return false
		}
	}
	return true
}

// statementEnd returns the index of the ';' that ends the first statement in
// sql, or -1 if there is none.
func statementEnd(sql string) int {
	for i := 0; i < len(sql); {
		switch c := sql[i]; {
		case c == ';':
			return i
		case c == '\'' || c == '"' || c == '`':
			i = quotedEnd(sql, i, c)
		case c == '[':
			i = quotedEnd(sql, i, ']')
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = lineCommentEnd(sql, i)
		case c == '/' && i+2 < len(sql) && sql[i+1] == '*':
			i = blockCommentEnd(sql, i)
		default:
			i++
		}
	}
	return -1
}

// quotedEnd returns the index just past the quoted token opening at sql[i]
// and closed by close, or len(sql) if it is unterminated. A doubled close
// character escapes itself, except for [...], which has no escape.
func quotedEnd(sql string, i int, close byte) int {
	for j := i + 1; j < len(sql); j++ {
		if sql[j] != close {
			continue
		}
		if close != ']' && j+1 < len(sql) && sql[j+1] == close {
			j++
			continue
		}
		return j + 1
	}
	return len(sql)
}

// lineCommentEnd returns the index just past the -- comment opening at
// sql[i], including its newline.
func lineCommentEnd(sql string, i int) int {
	for j := i + 2; j < len(sql); j++ {
		if sql[j] == '\n' {
			return j + 1
		}
	}
	return len(sql)
}

// blockCommentEnd returns the index just past the /* comment opening at
// sql[i].
func blockCommentEnd(sql string, i int) int {
	for j := i + 2; j+1 < len(sql); j++ {
		if sql[j] == '*' && sql[j+1] == '/' {
			return j + 2
		}
	}
	return len(sql)
}
