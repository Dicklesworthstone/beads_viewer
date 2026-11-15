package correlation

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// gitCommand returns an exec.Cmd for the git binary bound to ctx (issue #166).
// When ctx is cancelled — for example when the --robot-triage history
// prologue's timeout fires — any in-flight git subprocess is killed instead of
// leaking unbounded work. A nil ctx degrades to context.Background() (no
// cancellation), preserving the behavior of legacy constructors that never
// attach a context.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	return exec.CommandContext(ctx, "git", args...)
}

// validateGitRefArg rejects empty, NUL-containing, or leading-dash revision/SHA
// strings before they are passed to git subprocesses (defense against git flag
// injection via exported library APIs).
func validateGitRefArg(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return fmt.Errorf("commit SHA is required")
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("invalid commit SHA or git revision %q: must not start with '-'", ref)
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return fmt.Errorf("invalid commit SHA or git revision: contains NUL byte")
	}
	return nil
}
