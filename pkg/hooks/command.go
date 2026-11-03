package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

// validateHookExecution is a preflight check for every hook in both phases.
// Resolving executables is deliberately deferred: an approved pre-hook may
// create a program used by a later hook.
func validateHookExecution(hook Hook, platform string) error {
	if hook.Argv == nil {
		if strings.TrimSpace(hook.Command) == "" {
			return fmt.Errorf("hook requires a command or a nonempty argv list")
		}
		if strings.ContainsRune(hook.Command, '\x00') || !utf8.ValidString(hook.Command) {
			return fmt.Errorf("command must be valid UTF-8 without NUL bytes")
		}
		return nil
	}
	if hook.Command != "" {
		return fmt.Errorf("command and argv are mutually exclusive")
	}
	if len(hook.Argv) == 0 || strings.TrimSpace(hook.Argv[0]) == "" {
		return fmt.Errorf("argv must contain a nonempty executable as its first element")
	}
	for i, argument := range hook.Argv {
		if strings.ContainsRune(argument, '\x00') || !utf8.ValidString(argument) {
			// Never echo an argument value: callers may have supplied a literal
			// credential by mistake. JSON inspection must represent each byte
			// faithfully, so invalid UTF-8 cannot share an approval fingerprint.
			return fmt.Errorf("argv element %d must be valid UTF-8 without NUL bytes", i+1)
		}
	}
	if platform == "windows" {
		return validateWindowsArgvProgram(hook.Argv[0])
	}
	return nil
}

func validateWindowsArgvProgram(program string) error {
	path := strings.ReplaceAll(program, "/", `\`)
	if strings.HasPrefix(path, `\`) && !strings.HasPrefix(path, `\\`) ||
		len(path) >= 2 && path[1] == ':' && (len(path) < 3 || path[2] != '\\') {
		return fmt.Errorf("argv executable must use an absolute or project-relative path, not a Windows drive-relative or root-relative path")
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimRight(path, " .")))
	if ext == ".bat" || ext == ".cmd" {
		return fmt.Errorf("Windows batch files require an explicit shell command or interpreter; argv never adds a shell")
	}
	return nil
}

// newHookCommand keeps direct arguments literal. In particular, none of the
// shell command-line construction or environment expansion applies to argv.
func newHookCommand(ctx context.Context, hook Hook, projectDir string) (*exec.Cmd, error) {
	if err := validateHookExecution(hook, runtime.GOOS); err != nil {
		return nil, err
	}
	if hook.Argv == nil {
		shell, args := shellInvocation(runtime.GOOS, hook.Command)
		cmd := exec.CommandContext(ctx, shell, args...)
		configureHookShell(cmd, hook.Command)
		return cmd, nil
	}
	program := hook.Argv[0]
	if !filepath.IsAbs(program) && filepath.Base(program) != program {
		program = filepath.Join(projectDir, program)
	}
	// Use the invoking user's PATH and Go's ErrDot protections. Hook-specific
	// PATH values affect the child's environment, not this initial resolution.
	// Resolve explicitly because Windows can select a PATHEXT suffix privately
	// inside Cmd.Start; its resolved target must also pass the batch-file check.
	resolved, err := exec.LookPath(program)
	if err != nil {
		return nil, fmt.Errorf("resolving argv executable: %w", err)
	}
	if runtime.GOOS == "windows" {
		if err := validateWindowsArgvProgram(resolved); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, resolved, hook.Argv[1:]...)
	cmd.Args[0] = hook.Argv[0]
	return cmd, nil
}

func hookInvocationDescription(hook Hook) string {
	if hook.Argv == nil {
		return fmt.Sprintf("%q", hook.Command)
	}
	// A validated string slice always marshals successfully. JSON keeps argument
	// boundaries, empty arguments, and control characters visible in logs.
	data, _ := json.Marshal(hook.Argv)
	return "argv " + string(data)
}
