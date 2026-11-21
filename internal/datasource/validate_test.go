package datasource

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	_ "modernc.org/sqlite"
)

func TestValidateJSONL_OversizedNewlineFreeLineBoundedByEnv(t *testing.T) {
	t.Setenv(loader.MaxLineSizeEnvVar, "1") // 1 MiB cap

	dir := t.TempDir()
	corruptPath := filepath.Join(dir, "corrupt_newline_free.jsonl")

	// Write an 8 MiB newline-free corrupt stream in 1 MiB chunks so the file is
	// 8x larger than BV_MAX_LINE_SIZE_MB=1 while validateJSONL drains in O(1) memory.
	f, err := os.Create(corruptPath)
	if err != nil {
		t.Fatalf("create corrupt file: %v", err)
	}
	chunk := bytes.Repeat([]byte("A"), 1024*1024)
	for i := 0; i < 8; i++ {
		if _, err := f.Write(chunk); err != nil {
			_ = f.Close()
			t.Fatalf("write chunk %d: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupt file: %v", err)
	}

	runtime.GC()
	var beforeMem, afterMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)

	var logs []string
	source := DataSource{
		Type: SourceTypeJSONLLocal,
		Path: corruptPath,
	}
	opts := DefaultValidationOptions()
	opts.Verbose = true
	opts.Logger = func(msg string) {
		logs = append(logs, msg)
	}

	err = ValidateSourceWithOptions(&source, opts)
	runtime.ReadMemStats(&afterMem)

	if err == nil {
		t.Fatal("expected ValidateSourceWithOptions to fail on 8 MiB newline-free corrupt line under BV_MAX_LINE_SIZE_MB=1")
	}
	if source.Valid {
		t.Fatal("expected source.Valid to be false")
	}
	if !strings.Contains(err.Error(), "exceeded max line size") {
		t.Fatalf("expected oversized-line diagnostic in error, got: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "exceeds max line size") {
		t.Fatalf("expected verbose log to report max line size breach, got: %v", logs)
	}
	// Total allocations across the entire 8 MiB drain must stay well below the
	// 8 MiB file size (only the 1 MiB bufio.Reader buffer + tiny overhead).
	allocDelta := afterMem.TotalAlloc - beforeMem.TotalAlloc
	if allocDelta > 4*1024*1024 {
		t.Fatalf("validateJSONL allocated %d bytes draining 8 MiB corrupt line (expected bounded < 4 MiB)", allocDelta)
	}

	// Verify BV_MAX_LINE_SIZE_MB also raises the ceiling for valid large JSONL lines
	// and invalidates the validation cache across different BV_MAX_LINE_SIZE_MB values.
	validLargePath := filepath.Join(dir, "valid_2mb.jsonl")
	desc2MB := strings.Repeat("z", 2*1024*1024)
	validLine := fmt.Sprintf(`{"id":"BIG-1","title":"Two MB","description":%q,"status":"open","issue_type":"task"}`+"\n", desc2MB)
	if err := os.WriteFile(validLargePath, []byte(validLine), 0o644); err != nil {
		t.Fatalf("write valid_2mb.jsonl: %v", err)
	}

	largeSource := DataSource{Type: SourceTypeJSONLLocal, Path: validLargePath}
	if err := ValidateSource(&largeSource); err == nil {
		t.Fatal("expected 2 MiB line to be rejected under BV_MAX_LINE_SIZE_MB=1")
	}

	t.Setenv(loader.MaxLineSizeEnvVar, "4")
	if err := ValidateSource(&largeSource); err != nil {
		t.Fatalf("expected 2 MiB line to pass under BV_MAX_LINE_SIZE_MB=4, got: %v", err)
	}
	if !largeSource.Valid || largeSource.IssueCount != 1 {
		t.Fatalf("largeSource = (Valid=%v, IssueCount=%d), want (true, 1)", largeSource.Valid, largeSource.IssueCount)
	}
}

func TestValidateJSONL_SkipsBDMetadataTypeLines(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		maxErrorRate   float64
		wantErr        bool
		wantIssueCount int
	}{
		{
			name: "mixed bd metadata and issue lines have zero parse errors",
			content: strings.Join([]string{
				`{"_type":"memory","key":"k","content":"remember this"}`,
				`{"_type":"sprint","id":"sp-1","name":"Sprint 1"}`,
				`{"_type":"forecast","id":"fc-1"}`,
				`{"_type":"burndown","id":"bd-1"}`,
				`{"_type":"custom_future_kind","payload":123}`,
				`{"id":"ISS-1","title":"First real issue","status":"open"}`,
				`{"_type":"issue","id":"ISS-2","title":"Explicit issue _type","status":"in_progress"}`,
			}, "\n") + "\n",
			maxErrorRate:   0.0,
			wantErr:        false,
			wantIssueCount: 2,
		},
		{
			name: "only bd metadata lines rejected as non-issue source (matches fusedLoadJSONL)",
			content: strings.Join([]string{
				`{"_type":"memory","key":"k"}`,
				`{"_type":"sprint","name":"Sprint 1"}`,
			}, "\n") + "\n",
			maxErrorRate: 0.0,
			wantErr:      true,
		},
		{
			name: "explicit null required fields rejected (matches fusedLoadJSONL)",
			content: strings.Join([]string{
				`{"id":null,"title":null,"status":null}`,
			}, "\n") + "\n",
			maxErrorRate: 0.0,
			wantErr:      true,
		},
		{
			name: "malformed json containing _type is still counted as error",
			content: strings.Join([]string{
				`{"_type":"memory","broken": }`,
				`{"id":"ISS-1","title":"Valid issue","status":"open"}`,
			}, "\n") + "\n",
			maxErrorRate: 0.0,
			wantErr:      true,
		},
		{
			name: "explicit _type issue missing title is still counted as error",
			content: strings.Join([]string{
				`{"_type":"issue","id":"ISS-BAD","status":"open"}`,
				`{"id":"ISS-OK","title":"Valid","status":"open"}`,
			}, "\n") + "\n",
			maxErrorRate: 0.0,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "issues.jsonl")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write jsonl: %v", err)
			}
			var verboseLogs []string
			opts := DefaultValidationOptions()
			opts.MaxJSONLErrorRate = tt.maxErrorRate
			opts.Verbose = true
			opts.Logger = func(msg string) {
				verboseLogs = append(verboseLogs, msg)
			}

			source := DataSource{Type: SourceTypeJSONLLocal, Path: path}
			err := ValidateSourceWithOptions(&source, opts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSourceWithOptions() err = %v, wantErr %v (logs: %v)", err, tt.wantErr, verboseLogs)
			}
			if !tt.wantErr {
				if !source.Valid {
					t.Fatal("expected source.Valid = true")
				}
				if source.IssueCount != tt.wantIssueCount {
					t.Fatalf("IssueCount = %d, want %d", source.IssueCount, tt.wantIssueCount)
				}
				for _, logLine := range verboseLogs {
					if strings.Contains(logLine, "Parse error") || strings.Contains(logLine, "Missing field") {
						t.Fatalf("unexpected parse/field error log on valid bd metadata: %q", logLine)
					}
				}
			}
		})
	}
}

func TestValidateSQLite_ExcludesTombstonesInReadOnlySnapshot(t *testing.T) {
	tests := []struct {
		name               string
		hasTombstoneColumn bool
		inserts            []string
		wantCount          int
	}{
		{
			name:               "schema with tombstone column matches SQLiteReader LoadIssues",
			hasTombstoneColumn: true,
			inserts: []string{
				`INSERT INTO issues (id, title, status, tombstone) VALUES ('ACT-1', 'Open issue', 'open', 0)`,
				`INSERT INTO issues (id, title, status, tombstone) VALUES ('ACT-2', 'Closed issue', 'closed', NULL)`,
				`INSERT INTO issues (id, title, status, tombstone) VALUES ('DEL-1', 'Status tombstone', 'tombstone', 0)`,
				`INSERT INTO issues (id, title, status, tombstone) VALUES ('DEL-2', 'Flagged tombstone', 'open', 1)`,
			},
			wantCount: 2,
		},
		{
			name:               "modern br schema without tombstone column matches SQLiteReader LoadIssues",
			hasTombstoneColumn: false,
			inserts: []string{
				`INSERT INTO issues (id, title, status) VALUES ('ACT-1', 'Open issue', 'open')`,
				`INSERT INTO issues (id, title, status) VALUES ('ACT-2', 'In progress', 'in_progress')`,
				`INSERT INTO issues (id, title, status) VALUES ('ACT-3', 'Done', 'closed')`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-1', 'Deleted lowercase', 'tombstone')`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-2', 'Deleted lowercase 2', 'tombstone')`,
			},
			wantCount: 3,
		},
		{
			name:               "case-insensitive status TOMBSTONE and TombStone excluded",
			hasTombstoneColumn: false,
			inserts: []string{
				`INSERT INTO issues (id, title, status) VALUES ('ACT-1', 'Open issue', 'open')`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-1', 'Uppercase tombstone', 'TOMBSTONE')`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-2', 'Mixed case tombstone', 'TombStone')`,
			},
			wantCount: 1,
		},
		{
			name:               "custom statuses retained and Unicode padded tombstones excluded",
			hasTombstoneColumn: true,
			inserts: []string{
				`INSERT INTO issues (id, title, status) VALUES ('ACT-1', 'Custom workflow', ' QA-Review ')`,
				`INSERT INTO issues (id, title, status) VALUES ('ACT-2', 'Custom state mentioning tombstone', 'TombStone Review')`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-1', 'Whitespace padded tombstone', char(9) || ' TombStone ' || char(10))`,
				`INSERT INTO issues (id, title, status) VALUES ('DEL-2', 'Unicode padded tombstone', char(160) || char(8239) || 'TOMBSTONE' || char(12288))`,
				`INSERT INTO issues (id, title, status, tombstone) VALUES ('DEL-3', 'Flagged custom workflow', 'QA-Review', 1)`,
			},
			wantCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "beads.db")
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
				t.Fatalf("enable WAL: %v", err)
			}
			schema := `CREATE TABLE issues (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				status TEXT NOT NULL,
				priority INTEGER DEFAULT 2,
				issue_type TEXT DEFAULT 'task'`
			if tt.hasTombstoneColumn {
				schema += `, tombstone INTEGER DEFAULT 0`
			}
			schema += `)`
			if _, err := db.Exec(schema); err != nil {
				t.Fatalf("create table: %v", err)
			}
			for _, stmt := range tt.inserts {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatalf("insert (%s): %v", stmt, err)
				}
			}

			source := DataSource{
				Type: SourceTypeSQLite,
				Path: dbPath,
			}
			if err := ValidateSource(&source); err != nil {
				t.Fatalf("ValidateSource() failed: %v", err)
			}
			if !source.Valid {
				t.Fatal("expected source.Valid = true")
			}
			if source.IssueCount != tt.wantCount {
				t.Fatalf("validateSQLite IssueCount = %d, want %d", source.IssueCount, tt.wantCount)
			}

			reader, err := NewSQLiteReader(source)
			if err != nil {
				t.Fatalf("NewSQLiteReader: %v", err)
			}
			defer reader.Close()
			loaded, err := reader.LoadIssues()
			if err != nil {
				t.Fatalf("LoadIssues: %v", err)
			}
			if len(loaded) != source.IssueCount {
				t.Fatalf("LoadIssues count = %d, validateSQLite IssueCount = %d", len(loaded), source.IssueCount)
			}
			count, err := reader.CountIssues()
			if err != nil || count != source.IssueCount {
				t.Fatalf("CountIssues = %d, %v; validateSQLite IssueCount = %d", count, err, source.IssueCount)
			}
			authority, err := reader.LoadIssueAuthority()
			if err != nil || len(authority) != len(tt.inserts) {
				t.Fatalf("tombstones lost from authority: issues=%+v err=%v", authority, err)
			}
			report := reader.LoadReport()
			if len(report.TombstoneIDs) != len(tt.inserts)-tt.wantCount || report.WarningCount != 0 {
				t.Fatalf("incorrect tombstone accounting: %+v", report)
			}
		})
	}
}

func TestRC37_DiscoverAndLoadFollowRedirectAndMetadataDatabase(t *testing.T) {
	// 1. .beads/redirect followed by BOTH DiscoverSources and LoadIssuesFromDir.
	outer := t.TempDir()
	realBeads := filepath.Join(outer, "shared", ".beads")
	if err := os.MkdirAll(realBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realBeads, "issues.jsonl"), []byte(`{"id":"RED-1","title":"Via Redirect","status":"open","issue_type":"task"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	worktree := filepath.Join(outer, "wt")
	wtBeads := filepath.Join(worktree, ".beads")
	if err := os.MkdirAll(wtBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtBeads, "redirect"), []byte("../../shared/.beads\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sources, err := DiscoverSources(DiscoveryOptions{BeadsDir: wtBeads, SkipWorktreeSources: true, ValidateAfterDiscovery: true})
	if err != nil {
		t.Fatalf("DiscoverSources(redirect) failed: %v", err)
	}
	if len(sources) == 0 || filepath.Clean(sources[0].Path) != filepath.Clean(filepath.Join(realBeads, "issues.jsonl")) {
		t.Fatalf("DiscoverSources(redirect) = %+v, want %s", sources, filepath.Join(realBeads, "issues.jsonl"))
	}
	loaded, err := LoadIssuesFromDir(wtBeads)
	if err != nil || len(loaded.Issues) != 1 || loaded.Issues[0].ID != "RED-1" {
		t.Fatalf("LoadIssuesFromDir(redirect) = (%+v, %v), want [RED-1]", loaded.Issues, err)
	}

	// 2. metadata.json custom "database" field discovered by discoverSQLiteSources.
	customDir := filepath.Join(outer, "custom-db", ".beads")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customDBPath := filepath.Join(customDir, "tracker.sqlite")
	db, err := sql.Open("sqlite", customDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT, status TEXT, issue_type TEXT DEFAULT 'task');
		INSERT INTO issues (id, title, status) VALUES ('CUST-1', 'Custom DB Issue', 'open');`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(customDir, "metadata.json"), []byte(`{"database":"tracker.sqlite","backend":"sqlite"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	customSources, err := DiscoverSources(DiscoveryOptions{BeadsDir: customDir, SkipWorktreeSources: true, ValidateAfterDiscovery: true})
	if err != nil {
		t.Fatalf("DiscoverSources(custom metadata db) failed: %v", err)
	}
	if len(customSources) != 1 || customSources[0].Type != SourceTypeSQLite || filepath.Clean(customSources[0].Path) != filepath.Clean(customDBPath) {
		t.Fatalf("DiscoverSources(custom metadata db) = %+v, want SQLite %s", customSources, customDBPath)
	}
	customLoaded, err := LoadIssuesFromDir(customDir)
	if err != nil || len(customLoaded.Issues) != 1 || customLoaded.Issues[0].ID != "CUST-1" {
		t.Fatalf("LoadIssuesFromDir(custom metadata db) = (%+v, %v), want [CUST-1]", customLoaded.Issues, err)
	}

	// 3. Non-sqlite metadata.json backend must not discover custom database as SQLite.
	if err := os.WriteFile(filepath.Join(customDir, "metadata.json"), []byte(`{"database":"tracker.sqlite","backend":"unknown-tracker"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nonSQLiteSources, err := DiscoverSources(DiscoveryOptions{BeadsDir: customDir, SkipWorktreeSources: true, ValidateAfterDiscovery: true})
	if err != nil {
		t.Fatalf("DiscoverSources(non-sqlite metadata backend) failed: %v", err)
	}
	for _, src := range nonSQLiteSources {
		if filepath.Clean(src.Path) == filepath.Clean(customDBPath) {
			t.Fatalf("DiscoverSources(non-sqlite metadata backend) must not discover %s as SQLite: %+v", customDBPath, nonSQLiteSources)
		}
	}
}
