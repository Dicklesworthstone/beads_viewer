package datasource

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// GH beads_rust#555: mode=ro/query_only still permits SQLite to maintain
// shared WAL-index state. Do not "fix" that by using immutable, restoring
// -shm, or copying the main file without a transactionally consistent WAL.
func TestSQLiteWALReaderInteroperability(t *testing.T) {
	for _, headerOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("header_only_%t", headerOnly), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "beads.db")
			writer, err := sql.Open("sqlite", sqliteFileDSN(path, ""))
			if err != nil {
				t.Fatal(err)
			}
			writer.SetMaxOpenConns(1)
			if _, err := writer.Exec(`PRAGMA page_size=4096;
PRAGMA journal_mode=WAL;
CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, priority INTEGER);
INSERT INTO issues VALUES ('seed', 'Checkpointed issue', 'open', 2);`); err != nil {
				writer.Close()
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			// Construct only the frame-free WAL, while no connections exist.
			// The WAL index itself must be produced by the real viewer reader.
			wal := []byte{}
			if headerOnly {
				wal = make([]byte, 32)
				for i, word := range []uint32{0x377f0682, 3007000, 4096, 0, 0x12345678, 0x90abcdef} {
					binary.BigEndian.PutUint32(wal[4*i:], word)
				}
				a, b := walInteropChecksum(wal[:24], binary.LittleEndian)
				binary.BigEndian.PutUint32(wal[24:], a)
				binary.BigEndian.PutUint32(wal[28:], b)
			}
			if err := os.WriteFile(path+"-wal", wal, 0o600); err != nil {
				t.Fatal(err)
			}
			mainBefore := walInteropReadFile(t, path)
			source := DataSource{Type: SourceTypeSQLite, Path: path}
			for i := 0; i < 12; i++ {
				reader, err := NewSQLiteReader(source)
				if err != nil {
					t.Fatal(err)
				}
				issues, loadErr := reader.LoadIssues()
				closeErr := reader.Close()
				if loadErr != nil || closeErr != nil || len(issues) != 1 || issues[0].ID != "seed" {
					t.Fatalf("read %d: issues=%+v load=%v close=%v", i, issues, loadErr, closeErr)
				}
				if !bytes.Equal(walInteropReadFile(t, path), mainBefore) || !bytes.Equal(walInteropReadFile(t, path+"-wal"), wal) {
					t.Fatalf("read %d changed database or WAL bytes", i)
				}
				shm := walInteropReadFile(t, path+"-shm")
				if len(shm) < 136 || !bytes.Equal(shm[:48], shm[48:96]) || shm[12] != 1 || binary.NativeEndian.Uint32(shm[:4]) != 3007000 {
					t.Fatalf("read %d did not leave a settled initialized WAL index", i)
				}
				a, b := walInteropChecksum(shm[:40], binary.NativeEndian)
				if a != binary.NativeEndian.Uint32(shm[40:44]) || b != binary.NativeEndian.Uint32(shm[44:48]) {
					t.Fatalf("read %d left an invalid WAL-index checksum", i)
				}
				if !bytes.Equal(shm[14:40], make([]byte, 26)) {
					t.Fatalf("read %d did not exercise the unindexed empty header: %x", i, shm[:48])
				}
			}

			// Model a persistent interactive reader and a pinned snapshot while
			// a separate connection commits. Fresh reads must see WAL-only rows;
			// the held transaction must not see them until its snapshot ends.
			reader, err := NewSQLiteReader(source)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tx, err := reader.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&count); err != nil || count != 1 {
				t.Fatalf("initial snapshot: count=%d err=%v", count, err)
			}
			indexBefore, err := os.Stat(path + "-shm")
			if err != nil {
				t.Fatal(err)
			}
			writer, err = sql.Open("sqlite", sqliteFileDSN(path, "_pragma=busy_timeout(5000)&_pragma=wal_autocheckpoint(0)"))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.SetMaxOpenConns(1)
			for i := 0; i < 12; i++ {
				id := fmt.Sprintf("wal-%d", i)
				if _, err := writer.ExecContext(ctx, "INSERT INTO issues VALUES (?, 'WAL-only issue', 'open', 2)", id); err != nil {
					t.Fatalf("write %d beside persistent reader: %v", i, err)
				}
				issues, err := reader.LoadIssues()
				if err != nil || len(issues) != i+2 {
					t.Fatalf("refresh %d missed committed WAL rows: count=%d err=%v", i, len(issues), err)
				}
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&count); err != nil || count != 1 {
					t.Fatalf("held snapshot changed at write %d: count=%d err=%v", i, count, err)
				}
			}
			if !bytes.Equal(walInteropReadFile(t, path), mainBefore) {
				t.Fatal("writer changed pages protected by the held main-file snapshot")
			}
			indexAfter, err := os.Stat(path + "-shm")
			if err != nil || !os.SameFile(indexBefore, indexAfter) {
				t.Fatalf("live reader's WAL-index inode was replaced: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := reader.db.QueryRowContext(ctx, "SELECT count(*) FROM issues").Scan(&count); err != nil || count != 13 {
				t.Fatalf("new snapshot is stale: count=%d err=%v", count, err)
			}
			if _, err := reader.db.ExecContext(ctx, "INSERT INTO issues VALUES ('forbidden', 'Must not write', 'open', 2)"); err == nil {
				t.Fatal("viewer connection admitted a write")
			}
			var check string
			if err := reader.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
				t.Fatalf("integrity check: %q, %v", check, err)
			}
		})
	}
}

func walInteropChecksum(data []byte, order binary.ByteOrder) (uint32, uint32) {
	var a, b uint32
	for i := 0; i < len(data); i += 8 {
		a += order.Uint32(data[i:]) + b
		b += order.Uint32(data[i+4:]) + a
	}
	return a, b
}

func walInteropReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
