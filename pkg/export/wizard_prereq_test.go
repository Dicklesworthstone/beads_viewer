package export

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeExecutable(t *testing.T, dir string, name string, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("WriteFile %s: %v", name, err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("Chmod %s: %v", name, err)
	}
	return path
}

// Note: Full interactive prerequisite tests have been removed since huh forms
// cannot be easily tested with stdin injection. The wizard's interactive
// behavior is tested via E2E tests instead.

func TestWizard_checkPrerequisites_Local_NoCheck(t *testing.T) {
	wizard := NewWizard("/tmp/test")
	wizard.config.DeployTarget = "local"

	// Local deployment should not require any prerequisites
	if err := wizard.checkPrerequisites(); err != nil {
		t.Fatalf("checkPrerequisites for local target returned error: %v", err)
	}
}

func TestWizard_checkPrerequisites_GitHub_GHInstalled_AlreadyAuthed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()

	// gh stub that reports already authenticated
	ghScript := `#!/bin/sh
case "${1-}" in
  auth)
    case "${2-}" in
      status)
        echo "Logged in to github.com account testuser (GitHub)"
        exit 0
        ;;
    esac
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "gh", ghScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))

	// Provide a minimal global git identity. The identity check runs git with
	// SanitizedGitEnv, which drops GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM,
	// so git reads the global config from HOME. Put it in a temp HOME;
	// otherwise this test reads the developer's own ~/.gitconfig and fails
	// on a machine or CI runner that has no global identity.
	homeDir := t.TempDir()
	gitConfigPath := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, []byte("[user]\n\tname = Test User\n\temail = test@example.com\n"), 0644); err != nil {
		t.Fatalf("WriteFile gitconfig: %v", err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(homeDir, ".config"))

	wizard := NewWizard("/tmp/test")
	wizard.config.DeployTarget = "github"

	// Already authenticated, no prompt needed
	if err := wizard.checkPrerequisites(); err != nil {
		t.Fatalf("checkPrerequisites returned error: %v", err)
	}
}

func TestWizard_checkPrerequisites_Cloudflare_WranglerInstalled_AlreadyAuthed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()

	// wrangler stub that reports already authenticated
	wranglerScript := `#!/bin/sh
case "${1-}" in
  whoami)
    echo "Account Name: test@example.com"
    echo "Account ID: 123"
    exit 0
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "wrangler", wranglerScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))

	wizard := NewWizard("/tmp/test")
	wizard.config.DeployTarget = "cloudflare"

	// Already authenticated, no prompt needed
	if err := wizard.checkPrerequisites(); err != nil {
		t.Fatalf("checkPrerequisites returned error: %v", err)
	}
}
