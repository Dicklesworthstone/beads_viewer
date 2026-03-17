package datasource

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// doltTestServer holds state for a temporary dolt sql-server.
type doltTestServer struct {
	cmd  *exec.Cmd
	dir  string
	port int
	dsn  string
}

// startDoltServer initializes a dolt repo with test data and starts sql-server.
// Returns nil if dolt is not in PATH (test will be skipped).
func startDoltServer(t *testing.T) *doltTestServer {
	t.Helper()

	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt not in PATH, skipping Dolt integration tests")
	}

	dir := t.TempDir()
	repoDir := filepath.Join(dir, "testrepo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Fully isolate dolt from any parent .dolt directories
	doltEnv := []string{
		"DOLT_ROOT_PATH=" + dir,
		"HOME=" + dir,
		"PATH=" + os.Getenv("PATH"),
	}

	// Initialize dolt repo
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(doltBin, args...)
		cmd.Dir = repoDir
		cmd.Env = doltEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dolt %v failed: %v\n%s", args, err, out)
		}
	}

	// Configure git identity for dolt
	run("config", "--global", "--add", "user.email", "test@test.com")
	run("config", "--global", "--add", "user.name", "Test")
	run("init")

	// Create schema and seed data via dolt sql
	now := time.Now().UTC().Format(time.RFC3339Nano)
	schema := fmt.Sprintf(`
		CREATE TABLE issues (
			id VARCHAR(64) PRIMARY KEY,
			title VARCHAR(255) NOT NULL,
			description TEXT,
			status VARCHAR(32) NOT NULL,
			priority INT DEFAULT 3,
			issue_type VARCHAR(32) DEFAULT 'task',
			assignee VARCHAR(255),
			estimated_minutes INT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			due_date DATETIME,
			closed_at DATETIME,
			external_ref VARCHAR(255),
			compaction_level INT DEFAULT 0,
			compacted_at DATETIME,
			compacted_at_commit VARCHAR(255),
			original_size INT DEFAULT 0,
			labels TEXT,
			design TEXT,
			acceptance_criteria TEXT,
			notes TEXT,
			source_repo VARCHAR(255),
			tombstone INT DEFAULT 0
		);
		CREATE TABLE dependencies (
			issue_id VARCHAR(64),
			depends_on_id VARCHAR(64),
			dependency_type VARCHAR(32)
		);
		CREATE TABLE comments (
			id VARCHAR(64),
			issue_id VARCHAR(64),
			author VARCHAR(255),
			text TEXT,
			created_at DATETIME
		);
		INSERT INTO issues (id, title, status, issue_type, updated_at) VALUES
			('CTR-1', 'First issue',  'open',   'task', '%s'),
			('CTR-2', 'Second issue', 'open',   'task', '%s'),
			('CTR-3', 'Third issue',  'closed', 'task', '%s');
	`, now, now, now)

	cmd := exec.Command(doltBin, "sql", "-q", schema)
	cmd.Dir = repoDir
	cmd.Env = doltEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt sql schema setup failed: %v\n%s", err, out)
	}

	// Commit the data
	run("add", ".")
	run("commit", "-m", "seed test data")

	// Find a free port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start sql-server (root user is auto-created on first launch)
	serverCmd := exec.Command(doltBin, "sql-server",
		"--host", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
	)
	serverCmd.Dir = repoDir
	serverCmd.Env = doltEnv
	logFile, _ := os.CreateTemp(dir, "dolt-server-*.log")
	serverCmd.Stdout = logFile
	serverCmd.Stderr = logFile
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("cannot start dolt sql-server: %v", err)
	}

	srv := &doltTestServer{
		cmd:  serverCmd,
		dir:  dir,
		port: port,
		dsn:  fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, filepath.Base(repoDir)),
	}

	// Wait for server to be ready
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", srv.dsn)
		if err == nil {
			if err := db.Ping(); err == nil {
				db.Close()
				t.Cleanup(func() { srv.stop() })
				return srv
			}
			db.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}

	srv.stop()
	logFile.Seek(0, 0)
	logBytes, _ := os.ReadFile(logFile.Name())
	t.Fatalf("dolt sql-server did not become ready within 10s. Log:\n%s", string(logBytes))
	return nil
}

func (s *doltTestServer) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
}

// TestDoltReaderContract runs the IssueReader contract tests against a real Dolt server.
func TestDoltReaderContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Dolt integration test in short mode")
	}

	srv := startDoltServer(t)

	makeReader := func(t *testing.T) IssueReader {
		t.Helper()
		src := DataSource{
			Type: SourceTypeDolt,
			Path: srv.dsn,
		}
		r, err := NewReader(src)
		if err != nil {
			t.Fatalf("NewReader(Dolt): %v", err)
		}
		t.Cleanup(func() { r.Close() })
		return r
	}

	t.Run("LoadIssues", func(t *testing.T) {
		r := makeReader(t)
		issues, err := r.LoadIssues()
		if err != nil {
			t.Fatalf("LoadIssues: %v", err)
		}
		if len(issues) != 3 {
			t.Errorf("want 3 issues, got %d", len(issues))
		}
	})

	t.Run("LoadIssuesFiltered", func(t *testing.T) {
		r := makeReader(t)
		issues, err := r.LoadIssuesFiltered(func(iss *model.Issue) bool {
			return iss.Status == "open"
		})
		if err != nil {
			t.Fatalf("LoadIssuesFiltered: %v", err)
		}
		if len(issues) != 2 {
			t.Errorf("want 2 open issues, got %d", len(issues))
		}
	})

	t.Run("LoadIssuesFiltered_Nil", func(t *testing.T) {
		r := makeReader(t)
		issues, err := r.LoadIssuesFiltered(nil)
		if err != nil {
			t.Fatalf("LoadIssuesFiltered(nil): %v", err)
		}
		if len(issues) != 3 {
			t.Errorf("want 3, got %d", len(issues))
		}
	})

	t.Run("CountIssues", func(t *testing.T) {
		r := makeReader(t)
		count, err := r.CountIssues()
		if err != nil {
			t.Fatalf("CountIssues: %v", err)
		}
		if count != 3 {
			t.Errorf("want 3, got %d", count)
		}
	})

	t.Run("GetIssueByID", func(t *testing.T) {
		r := makeReader(t)
		iss, err := r.GetIssueByID("CTR-2")
		if err != nil {
			t.Fatalf("GetIssueByID: %v", err)
		}
		if iss.Title != "Second issue" {
			t.Errorf("title = %q; want %q", iss.Title, "Second issue")
		}
	})

	t.Run("GetIssueByID_NotFound", func(t *testing.T) {
		r := makeReader(t)
		_, err := r.GetIssueByID("NOPE-999")
		if err == nil {
			t.Error("want error for missing issue")
		}
	})

	t.Run("GetLastModified", func(t *testing.T) {
		r := makeReader(t)
		mod, err := r.GetLastModified()
		if err != nil {
			t.Fatalf("GetLastModified: %v", err)
		}
		if mod.IsZero() {
			t.Error("GetLastModified returned zero time")
		}
	})
}
