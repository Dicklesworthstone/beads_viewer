package export

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeployToGitHubPages_SkipConfirmation_UnauthenticatedReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	ghScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/gh_authed"
case "${1-}" in
  auth)
    case "${2-}" in
      status)
        if [ -f "$authed_file" ]; then
          echo "Logged in to github.com account testuser (GitHub)"
          exit 0
        fi
        echo "You are not logged in"
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "gh", ghScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)

	_, err := DeployToGitHubPages(GitHubDeployConfig{
		RepoName:         "repo",
		BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
		SkipConfirmation: true,
		ForceOverwrite:   false,
		Private:          false,
		Description:      "",
	})
	if err == nil {
		t.Fatal("Expected DeployToGitHubPages to return authentication error when unauthenticated and SkipConfirmation=true")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "authentication") {
		t.Fatalf("Unexpected DeployToGitHubPages error: %v", err)
	}
}

func TestDeployToGitHubPages_BundleMissingAfterConfirmations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	ghScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/gh_authed"
case "${1-}" in
  auth)
    case "${2-}" in
      status)
        if [ -f "$authed_file" ]; then
          echo "Logged in to github.com account testuser (GitHub)"
          exit 0
        fi
        echo "You are not logged in"
        exit 1
        ;;
    esac
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "gh", ghScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)

	// Mark as authenticated.
	if err := os.WriteFile(filepath.Join(stateDir, "gh_authed"), []byte("ok"), 0644); err != nil {
		t.Fatalf("WriteFile gh_authed: %v", err)
	}

	// Provide a minimal global git identity via isolated HOME (loader.SanitizedGitEnv strips GIT_CONFIG_*).
	tmpHome := t.TempDir()
	gitConfigPath := filepath.Join(tmpHome, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, []byte("[user]\n\tname = Test User\n\temail = test@example.com\n"), 0644); err != nil {
		t.Fatalf("WriteFile gitconfig: %v", err)
	}
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	withStdin(t, "y\n", func() {
		_, err := DeployToGitHubPages(GitHubDeployConfig{
			RepoName:         "repo",
			BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
			SkipConfirmation: false,
		})
		if err == nil {
			t.Fatal("Expected DeployToGitHubPages to return error for missing bundle path")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "bundle path does not exist") {
			t.Fatalf("Unexpected DeployToGitHubPages error: %v", err)
		}
	})
}

func TestDeployToGitHubPages_CreateRepository_ErrorSurfaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	ghScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/gh_authed"

case "${1-}" in
  auth)
    case "${2-}" in
      status)
        if [ -f "$authed_file" ]; then
          echo "Logged in to github.com account testuser (GitHub)"
          exit 0
        fi
        echo "You are not logged in"
        exit 1
        ;;
    esac
    ;;
  repo)
    case "${2-}" in
      view)
        # Pretend the repo does not exist.
        exit 1
        ;;
      create)
        echo "create failed"
        exit 1
        ;;
    esac
    ;;
esac

exit 0
`
	writeExecutable(t, binDir, "gh", ghScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)

	// Mark as authenticated so we reach repository creation.
	if err := os.WriteFile(filepath.Join(stateDir, "gh_authed"), []byte("ok"), 0644); err != nil {
		t.Fatalf("WriteFile gh_authed: %v", err)
	}

	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}

	_, err := DeployToGitHubPages(GitHubDeployConfig{
		RepoName:         "repo",
		BundlePath:       bundleDir,
		SkipConfirmation: true,
	})
	if err == nil {
		t.Fatal("Expected DeployToGitHubPages to return error when repo create fails")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "failed to create repository") {
		t.Fatalf("Unexpected DeployToGitHubPages error: %v", err)
	}
}

func TestDeployToGitHubPages_AuthenticateFlow_StopsAtGitIdentityCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	ghScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/gh_authed"
case "${1-}" in
  auth)
    case "${2-}" in
      status)
        if [ -f "$authed_file" ]; then
          echo "Logged in to github.com account testuser (GitHub)"
          exit 0
        fi
        echo "You are not logged in"
        exit 1
        ;;
      login)
        mkdir -p "$state_dir"
        : > "$authed_file"
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
	t.Setenv("BV_TEST_STATE_DIR", stateDir)

	// Force "git identity not configured" via isolated HOME (loader.SanitizedGitEnv strips GIT_CONFIG_*).
	tmpHome := t.TempDir()
	gitConfigPath := filepath.Join(tmpHome, ".gitconfig")
	if err := os.WriteFile(gitConfigPath, []byte(""), 0644); err != nil {
		t.Fatalf("WriteFile gitconfig: %v", err)
	}
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))

	// Ensure git isn't reading repo-local config from the test workspace.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmpCwd := t.TempDir()
	if err := os.Chdir(tmpCwd); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})

	// Single prompt: authenticate now? (stops at git identity check)
	withStdin(t, "y\n", func() {
		_, err := DeployToGitHubPages(GitHubDeployConfig{
			RepoName:         "repo",
			BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
			SkipConfirmation: false,
		})
		if err == nil {
			t.Fatal("Expected DeployToGitHubPages to return error for missing git identity")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "git identity not configured") {
			t.Fatalf("Unexpected DeployToGitHubPages error: %v", err)
		}
	})
}

func TestDeployToCloudflarePages_SkipConfirmation_UnauthenticatedReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	wranglerScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/wrangler_authed"
case "${1-}" in
  whoami)
    if [ -f "$authed_file" ]; then
      echo "Account Name: test@example.com"
      echo "Account ID: 123"
      exit 0
    fi
    echo "You are not authenticated. Please run wrangler login"
    exit 0
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "wrangler", wranglerScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)
	// Override HOME so the real machine's wrangler config file isn't found
	// by checkWranglerConfigFile(), ensuring the stub wrangler binary controls auth.
	t.Setenv("HOME", t.TempDir())

	_, err := DeployToCloudflarePages(CloudflareDeployConfig{
		ProjectName:      "proj",
		BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
		SkipConfirmation: true,
	})
	if err == nil {
		t.Fatal("Expected DeployToCloudflarePages to return authentication error when unauthenticated and SkipConfirmation=true")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "authentication") {
		t.Fatalf("Unexpected DeployToCloudflarePages error: %v", err)
	}
}

func TestDeployToCloudflarePages_AuthenticateFlow_ReachesBundleCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	wranglerScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/wrangler_authed"
case "${1-}" in
  whoami)
    if [ -f "$authed_file" ]; then
      echo "Account ID: 123"
      exit 0
    fi
    echo "You are not authenticated. Please run wrangler login"
    exit 0
    ;;
  login)
    mkdir -p "$state_dir"
    : > "$authed_file"
    exit 0
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "wrangler", wranglerScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)
	// Override HOME so the real machine's wrangler config file isn't found.
	t.Setenv("HOME", t.TempDir())

	// Single prompt: authenticate now? (account confirm is skipped when AccountName is empty)
	withStdin(t, "y\n", func() {
		_, err := DeployToCloudflarePages(CloudflareDeployConfig{
			ProjectName:      "proj",
			BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
			SkipConfirmation: false,
		})
		if err == nil {
			t.Fatal("Expected DeployToCloudflarePages to return error for missing bundle path")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "bundle path does not exist") {
			t.Fatalf("Unexpected DeployToCloudflarePages error: %v", err)
		}
	})
}

func TestDeployToCloudflarePages_BundleMissingAfterAccountConfirm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()
	stateDir := t.TempDir()

	wranglerScript := `#!/bin/sh
set -eu
state_dir="${BV_TEST_STATE_DIR:-}"
authed_file="$state_dir/wrangler_authed"
case "${1-}" in
  whoami)
    if [ -f "$authed_file" ]; then
      echo "Account Name: test@example.com"
      echo "Account ID: 123"
      exit 0
    fi
    echo "You are not authenticated. Please run wrangler login"
    exit 0
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "wrangler", wranglerScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	t.Setenv("BV_TEST_STATE_DIR", stateDir)
	// Override HOME so the real machine's wrangler config file isn't found.
	t.Setenv("HOME", t.TempDir())

	// Mark as authenticated.
	if err := os.WriteFile(filepath.Join(stateDir, "wrangler_authed"), []byte("ok"), 0644); err != nil {
		t.Fatalf("WriteFile wrangler_authed: %v", err)
	}

	withStdin(t, "y\n", func() {
		_, err := DeployToCloudflarePages(CloudflareDeployConfig{
			ProjectName:      "proj",
			BundlePath:       filepath.Join(t.TempDir(), "missing-bundle"),
			SkipConfirmation: false,
		})
		if err == nil {
			t.Fatal("Expected DeployToCloudflarePages to return error for missing bundle path")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "bundle path does not exist") {
			t.Fatalf("Unexpected DeployToCloudflarePages error: %v", err)
		}
	})
}

func TestDeployToCloudflarePages_DeployCommandFailureSurfaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stubs not supported on windows in this test")
	}

	binDir := t.TempDir()

	wranglerScript := `#!/bin/sh
set -eu
case "${1-}" in
  whoami)
    echo "Account Name: test@example.com"
    echo "Account ID: 123"
    exit 0
    ;;
  pages)
    if [ "${2-}" = "deploy" ]; then
      echo "deploy failed"
      exit 1
    fi
    ;;
esac
exit 0
`
	writeExecutable(t, binDir, "wrangler", wranglerScript)

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", fmt.Sprintf("%s%c%s", binDir, os.PathListSeparator, origPath))
	// Override HOME so the real machine's wrangler config file isn't found.
	t.Setenv("HOME", t.TempDir())

	bundleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundleDir, "index.html"), []byte("<!doctype html>"), 0644); err != nil {
		t.Fatalf("WriteFile index.html: %v", err)
	}

	_, err := DeployToCloudflarePages(CloudflareDeployConfig{
		ProjectName:      "proj",
		BundlePath:       bundleDir,
		SkipConfirmation: true,
	})
	if err == nil {
		t.Fatal("Expected DeployToCloudflarePages to return error when wrangler deploy fails")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "deployment failed") {
		t.Fatalf("Unexpected DeployToCloudflarePages error: %v", err)
	}
}
