package correlation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	// lifecycleGitPolicyVersion participates in the persistent history-cache
	// namespace. Bump it whenever lifecycleGitConfigArgs,
	// lifecycleGitLogOutputArgs, lifecycleHistoryOrderArgs, or
	// lifecycleGitDiffArgs changes semantics.
	lifecycleGitPolicyVersion = "lifecycle-git-policy-v3"
	lifecycleRenameLimit      = 1000
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

// repoGitPolicyArgs prevents repository-local replacement refs from silently
// changing the object graph observed by cache keys and history extraction.
func repoGitPolicyArgs() []string {
	return []string{"--no-replace-objects"}
}

// repoGitCommand pins a Git subprocess to repoPath even when the host process
// carries ambient Git routing variables from a hook, alias, or parent command.
// All correlation reads that contribute to one report must observe the same
// repository; otherwise HEAD, lifecycle events, and co-commit diffs can be
// assembled from different object databases.
func repoGitCommand(ctx context.Context, repoPath string, args ...string) *exec.Cmd {
	gitArgs := make([]string, 0, len(args)+1)
	gitArgs = append(gitArgs, repoGitPolicyArgs()...)
	gitArgs = append(gitArgs, args...)
	cmd := gitCommand(ctx, gitArgs...)
	cmd.Dir = repoPath
	cmd.Env = repoGitEnvironment()
	return cmd
}

// lifecycleGitConfigArgs fixes repository/global configuration that can change
// either --follow traversal or the bytes parsed from `git log`. Command-line
// options in lifecycleGitLogOutputArgs/lifecycleGitDiffArgs repeat the
// correctness-critical settings where Git exposes an explicit override.
func lifecycleGitConfigArgs() []string {
	return []string{
		"-c", "color.ui=false",
		"-c", "core.quotePath=true",
		"-c", "core.bigFileThreshold=512m",
		"-c", "diff.renames=true",
		"-c", fmt.Sprintf("diff.renameLimit=%d", lifecycleRenameLimit),
		"-c", "diff.algorithm=default",
		"-c", "diff.indentHeuristic=false",
		"-c", "i18n.logOutputEncoding=UTF-8",
		"-c", "log.follow=false",
		"-c", "log.showSignature=false",
		"-c", "log.decorate=false",
		"-c", "log.showRoot=true",
	}
}

// lifecycleGitLogOutputArgs stabilizes commit metadata and suppresses optional
// decorations that are not part of the parser grammar.
func lifecycleGitLogOutputArgs() []string {
	return []string{
		"--encoding=UTF-8",
		"--no-use-mailmap",
		"--no-abbrev-commit",
		"--no-expand-tabs",
		"--no-show-signature",
		"--no-decorate",
		"--no-notes",
		"--root",
	}
}

// lifecycleHistoryOrderArgs pins the traversal order shared by cold history
// extraction and incremental range discovery. Git's default date-oriented walk
// can interleave commits from branches differently from --topo-order, making an
// incremental replay disagree with a cold rebuild when commit dates are skewed.
func lifecycleHistoryOrderArgs() []string {
	return []string{"--topo-order"}
}

// lifecycleGitDiffArgs fixes every diff policy that can change --follow rename
// selection or the added/removed JSONL records consumed by lifecycle parsers.
// --text also prevents clone-local attributes from suppressing the patch as a
// binary diff; external drivers and textconv are explicitly disabled.
func lifecycleGitDiffArgs() []string {
	return []string{
		"--find-renames=50%",
		fmt.Sprintf("-l%d", lifecycleRenameLimit),
		"--no-rename-empty",
		"--diff-algorithm=default",
		"--no-indent-heuristic",
		"--no-ext-diff",
		"--no-textconv",
		"--text",
		"--ignore-submodules=none",
		"--submodule=short",
		"--no-relative",
		"--no-diff-merges",
	}
}

// lifecycleGitLogCommand runs a lifecycle-producing `git log` through the
// repository-pinned environment and the complete deterministic parser policy.
// logArgs must contain only arguments after the `log` subcommand.
func lifecycleGitLogCommand(ctx context.Context, repoPath string, logArgs ...string) *exec.Cmd {
	args := make([]string, 0, len(logArgs)+len(lifecycleGitConfigArgs())+len(lifecycleGitLogOutputArgs())+len(lifecycleGitDiffArgs())+1)
	args = append(args, lifecycleGitConfigArgs()...)
	args = append(args, "log")
	args = append(args, lifecycleGitLogOutputArgs()...)
	args = append(args, lifecycleGitDiffArgs()...)
	args = append(args, logArgs...)
	return repoGitCommand(ctx, repoPath, args...)
}

func lifecycleGitPolicyNamespaceInputs() []string {
	inputs := []string{lifecycleGitPolicyVersion}
	inputs = append(inputs, repoGitPolicyArgs()...)
	inputs = append(inputs, lifecycleGitConfigArgs()...)
	inputs = append(inputs, lifecycleGitLogOutputArgs()...)
	inputs = append(inputs, lifecycleHistoryOrderArgs()...)
	inputs = append(inputs, lifecycleGitDiffArgs()...)
	return inputs
}

func repoGitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		normalizedName := strings.ToUpper(name)
		if gitEnvironmentOverridesRepository(normalizedName) || normalizedName == "LC_ALL" || normalizedName == "LANG" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "LC_ALL=C")
}

func gitEnvironmentOverridesRepository(name string) bool {
	name = strings.ToUpper(name)
	switch name {
	case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH",
		"GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS", "GIT_NAMESPACE",
		"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_PREFIX", "GIT_INTERNAL_SUPER_PREFIX", "GIT_IMPLICIT_WORK_TREE",
		"GIT_ATTR_SOURCE", "GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS",
		"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_LITERAL_PATHSPECS",
		"GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS":
		return true
	default:
		return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
	}
}
