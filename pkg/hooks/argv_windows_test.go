//go:build windows

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookArgvWindowsRejectsResolvedBatchPrograms(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"batch.cmd", "legacy.BAT"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("@echo unexpected > batch-marker\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("PATHEXT", ".CMD;.BAT;.EXE")
	for _, program := range []string{
		"batch", "legacy", "batch.cmd", "legacy.BAT",
		filepath.Join(dir, "batch"), filepath.Join(dir, "legacy"),
		`.\batch`, `.\legacy`, `.\batch.cmd`,
	} {
		t.Run(program, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			cmd, err := newHookCommand(ctx, Hook{Argv: []string{program, "literal&argument"}}, dir)
			if err == nil {
				t.Fatalf("batch program was accepted for direct execution: cmd=%v", cmd)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "batch") {
				t.Fatalf("batch program failed for an unrelated reason: %v", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "batch-marker")); !os.IsNotExist(err) {
		t.Fatalf("batch validation executed project code: %v", err)
	}
}

func TestHookArgvWindowsResolvesExtensionlessNativeProgram(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "native program.cmd.exe")
	copyArgvProbeExecutable(t, program)
	t.Setenv("PATHEXT", ".CMD;.EXE")
	hook := argvProbeHook(t, "space and quote \"value\"", `trailing\`, "", "%LITERAL%", "&literal|")
	hook.Argv[0] = `.\native program.cmd`
	// An explicit .cmd declaration is intentionally rejected even if an .exe
	// could be appended. An ordinary extensionless path must resolve to .exe.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := newHookCommand(ctx, hook, dir); err == nil {
		t.Fatal("an explicit .cmd declaration silently selected an appended .exe")
	}
	plainProgram := filepath.Join(dir, "native program.exe")
	copyArgvProbeExecutable(t, plainProgram)
	hook.Argv[0] = `.\native program`
	config := &Config{Hooks: HooksByPhase{PreExport: []Hook{hook}}, projectDir: dir}
	executor := newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err != nil {
		t.Fatal(err)
	}
	receipt := decodeArgvReceipt(t, executor.Results()[0])
	want := hook.Argv[3:]
	if len(receipt.Args) != len(want) {
		t.Fatalf("native Windows argument count=%d, want %d: %q", len(receipt.Args), len(want), receipt.Args)
	}
	for i := range want {
		if receipt.Args[i] != want[i] {
			t.Errorf("native Windows argument %d=%q, want %q", i, receipt.Args[i], want[i])
		}
	}

	// A real native executable whose basename happens to contain .cmd remains
	// valid when the declared path has the actual .exe extension.
	hook.Argv[0] = program
	config.Hooks.PreExport[0] = hook
	executor = newApprovedExecutor(t, config, ExportContext{})
	if err := executor.RunPreExport(); err != nil {
		t.Fatalf("native .cmd.exe program was incorrectly rejected: %v", err)
	}
}
