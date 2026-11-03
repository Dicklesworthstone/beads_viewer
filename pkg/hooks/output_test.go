package hooks

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCappedHookOutputAcceptsAndDrainsWrites(t *testing.T) {
	var output cappedHookOutput
	for _, size := range []int{HookOutputLimit - 1, 1, 0, 1, HookOutputLimit} {
		data := strings.Repeat("x", size)
		n, err := io.Copy(&output, strings.NewReader(data))
		if err != nil || n != int64(size) {
			t.Fatalf("write of %d bytes returned %d, %v", size, n, err)
		}
	}
	if len(output.String()) != HookOutputLimit || !output.truncated {
		t.Fatalf("capture length=%d, truncated=%v", len(output.String()), output.truncated)
	}
	if output.String() != strings.Repeat("x", HookOutputLimit) {
		t.Fatal("capture did not retain the original output prefix")
	}
}

func TestExecutorBoundsOutputWithoutChangingExitStatus(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quotedExecutable := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
	if runtime.GOOS == "windows" {
		quotedExecutable = `"` + executable + `"`
	}
	for _, tc := range []struct {
		name     string
		size     int
		exitCode int
		direct   bool
	}{
		{"small", 31, 0, false},
		{"exact-limit", HookOutputLimit, 0, false},
		{"over-limit", 3*HookOutputLimit + 7, 0, false},
		{"over-limit-failure", 3*HookOutputLimit + 7, 7, false},
		{"argv-over-limit", 3*HookOutputLimit + 7, 0, true},
		{"argv-over-limit-failure", 3*HookOutputLimit + 7, 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "fully-written")
			config := &Config{Hooks: HooksByPhase{PreExport: []Hook{{
				Name: "bounded-output", Command: quotedExecutable + ` "-test.run=^TestHookOutputSubprocessProbe$"`,
				Timeout: 10 * time.Second, OnError: "fail",
				Env: map[string]string{
					"BV_HOOK_OUTPUT_SIZE": strconv.Itoa(tc.size),
					"BV_HOOK_OUTPUT_EXIT": strconv.Itoa(tc.exitCode),
				},
			}}}}
			if tc.direct {
				config.Hooks.PreExport[0].Command = ""
				config.Hooks.PreExport[0].Argv = []string{executable, "-test.run=^TestHookOutputSubprocessProbe$"}
			}
			executor := newApprovedExecutor(t, config, ExportContext{ExportPath: marker})
			err := executor.RunPreExport()
			if tc.exitCode == 0 && err != nil {
				t.Fatalf("output capture changed successful hook outcome: %v", err)
			}
			if tc.exitCode != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exitCode {
					t.Fatalf("output capture changed failing hook exit status: %v", err)
				}
			}
			results := executor.Results()
			if len(results) != 1 {
				t.Fatalf("got %d hook results", len(results))
			}
			result := results[0]
			if result.Success != (tc.exitCode == 0) {
				t.Fatalf("incorrect hook success status: %v", result.Success)
			}
			wantSize := min(tc.size, HookOutputLimit)
			for stream, got := range map[string]string{"stdout": result.Stdout, "stderr": result.Stderr} {
				if len(got) != wantSize {
					t.Errorf("%s length=%d, want %d", stream, len(got), wantSize)
				}
				character := "o"
				if stream == "stderr" {
					character = "e"
				}
				if got != strings.Repeat(character, wantSize) {
					t.Errorf("%s did not preserve its output prefix", stream)
				}
			}
			wantTruncated := tc.size > HookOutputLimit
			if result.StdoutTruncated != wantTruncated || result.StderrTruncated != wantTruncated {
				t.Errorf("incorrect truncation flags: stdout=%v stderr=%v", result.StdoutTruncated, result.StderrTruncated)
			}
			for _, stream := range []string{"stdout", "stderr"} {
				if strings.Contains(executor.Summary(), stream+" truncated") != wantTruncated {
					t.Errorf("summary did not report %s truncation correctly", stream)
				}
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("hook did not finish writing both streams: %v", err)
			}
		})
	}
}

// A finite real subprocess exercises os/exec's copy path, including any
// io.Copy fast paths that could accidentally bypass the writer's capture cap.
func TestHookOutputSubprocessProbe(t *testing.T) {
	sizeValue := os.Getenv("BV_HOOK_OUTPUT_SIZE")
	if sizeValue == "" {
		return
	}
	size, err := strconv.Atoi(sizeValue)
	if err != nil || size < 0 || size > 4*HookOutputLimit {
		os.Exit(31)
	}
	for stream, character := range map[*os.File]string{os.Stdout: "o", os.Stderr: "e"} {
		chunk := strings.Repeat(character, 32*1024)
		for remaining := size; remaining > 0; {
			n, err := io.WriteString(stream, chunk[:min(remaining, len(chunk))])
			if err != nil || n == 0 {
				os.Exit(32)
			}
			remaining -= n
		}
	}
	if err := os.WriteFile(os.Getenv("BV_EXPORT_PATH"), []byte("complete"), 0o600); err != nil {
		os.Exit(33)
	}
	exitCode, err := strconv.Atoi(os.Getenv("BV_HOOK_OUTPUT_EXIT"))
	if err != nil {
		os.Exit(34)
	}
	os.Exit(exitCode)
}
