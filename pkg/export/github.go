// Package export provides data export functionality for bv.
//
// This file implements GitHub CLI integration for deploying static sites
// to GitHub Pages. It follows safety-first principles: no auto-install,
// confirmation prompts for destructive operations.
package export

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

const skipDeploymentIssueCountVerification = -1

var githubActionsInitialWait = 5 * time.Second

// GitHubDeployConfig configures GitHub Pages deployment.
type GitHubDeployConfig struct {
	// RepoName is the desired repository name (without owner)
	RepoName string

	// Private indicates whether the repository should be private
	Private bool

	// Description is the repository description
	Description string

	// BundlePath is the path to the static site bundle to deploy
	BundlePath string

	// SkipConfirmation skips interactive confirmation prompts (for CI)
	SkipConfirmation bool

	// ForceOverwrite allows overwriting non-empty repositories
	ForceOverwrite bool
}

// GitHubDeployResult contains the result of a deployment.
type GitHubDeployResult struct {
	// RepoFullName is the full repository name (owner/repo)
	RepoFullName string

	// PagesURL is the GitHub Pages URL
	PagesURL string

	// GitRemote is the git remote URL
	GitRemote string
}

// GitHubStatus represents the current status of gh CLI.
type GitHubStatus struct {
	Installed     bool
	Authenticated bool
	Username      string
	GitConfigured bool
	GitName       string
	GitEmail      string
}

// CheckGHStatus checks the status of gh CLI and git configuration.
func CheckGHStatus() (*GitHubStatus, error) {
	status := &GitHubStatus{}

	// Check gh CLI installation
	_, err := exec.LookPath("gh")
	status.Installed = err == nil

	if status.Installed {
		// Check authentication
		cmd := exec.Command("gh", "auth", "status")
		output, err := cmd.CombinedOutput()
		status.Authenticated = err == nil

		// Parse username from output
		if status.Authenticated {
			status.Username = parseGHUsername(string(output))
		}
	}

	// Check git configuration
	nameCmd := exec.Command("git", "--no-replace-objects", "config", "user.name")
	nameCmd.Env = loader.SanitizedGitEnv()
	if nameOut, err := nameCmd.Output(); err == nil {
		status.GitName = strings.TrimSpace(string(nameOut))
	}

	emailCmd := exec.Command("git", "--no-replace-objects", "config", "user.email")
	emailCmd.Env = loader.SanitizedGitEnv()
	if emailOut, err := emailCmd.Output(); err == nil {
		status.GitEmail = strings.TrimSpace(string(emailOut))
	}

	status.GitConfigured = status.GitName != "" && status.GitEmail != ""

	return status, nil
}

// parseGHUsername extracts the username from gh auth status output.
func parseGHUsername(output string) string {
	// Output format: "  Logged in to github.com account username (..."
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Logged in to") && strings.Contains(line, "account") {
			parts := strings.Split(line, "account ")
			if len(parts) > 1 {
				// Get word after "account "
				rest := strings.TrimSpace(parts[1])
				// Remove trailing parenthetical
				if idx := strings.Index(rest, " "); idx > 0 {
					return rest[:idx]
				}
				if idx := strings.Index(rest, "("); idx > 0 {
					return strings.TrimSpace(rest[:idx])
				}
				return rest
			}
		}
	}
	return ""
}

// ShowInstallInstructions prints gh CLI installation instructions.
func ShowInstallInstructions() {
	fmt.Println("\ngh CLI is not installed.")
	fmt.Println("\nInstallation options:")

	switch runtime.GOOS {
	case "darwin":
		fmt.Println("  macOS (Homebrew): brew install gh")
		fmt.Println("  macOS (MacPorts): sudo port install gh")
	case "linux":
		fmt.Println("  Debian/Ubuntu:    sudo apt install gh")
		fmt.Println("  Fedora:           sudo dnf install gh")
		fmt.Println("  Arch Linux:       sudo pacman -S github-cli")
	case "windows":
		fmt.Println("  Windows (winget): winget install --id GitHub.cli")
		fmt.Println("  Windows (scoop):  scoop install gh")
		fmt.Println("  Windows (choco):  choco install gh")
	}

	fmt.Println("\n  All platforms: https://cli.github.com/")
	fmt.Println("")
}

// AttemptGHInstall attempts to install gh CLI via Homebrew (macOS only).
// Returns an error if not on macOS or if installation fails.
func AttemptGHInstall() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("automatic installation only supported on macOS via Homebrew")
	}

	// Check if Homebrew is installed
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("homebrew not found - install gh CLI manually from https://cli.github.com/")
	}

	fmt.Println("Installing gh CLI via Homebrew...")
	cmd := exec.Command("brew", "install", "gh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew install gh failed: %w", err)
	}

	fmt.Println("gh CLI installed successfully!")
	return nil
}

// AuthenticateGH starts the interactive gh authentication flow.
func AuthenticateGH() error {
	fmt.Println("\nStarting GitHub authentication...")
	fmt.Println("This will open a browser for authentication.")
	fmt.Println("")

	cmd := exec.Command("gh", "auth", "login", "--web")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh auth login failed: %w", err)
	}

	return nil
}

// CreateRepository creates a new GitHub repository.
func CreateRepository(name string, private bool, description string) (string, error) {
	visibility := "--public"
	if private {
		visibility = "--private"
	}

	args := []string{"repo", "create", name, visibility}
	if description != "" {
		args = append(args, "--description", description)
	}
	args = append(args, "--clone=false")

	cmd := exec.Command("gh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create repository: %s", strings.TrimSpace(string(output)))
	}

	// Get full repo name (owner/repo)
	return getRepoFullName(name)
}

// getRepoFullName retrieves the full name (owner/repo) of a repository.
func getRepoFullName(name string) (string, error) {
	// If already has owner, use as-is
	if strings.Contains(name, "/") {
		return name, nil
	}

	cmd := exec.Command("gh", "repo", "view", name,
		"--json", "nameWithOwner",
		"-q", ".nameWithOwner")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get repository info: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// RepoExists checks if a repository exists.
func RepoExists(name string) bool {
	cmd := exec.Command("gh", "repo", "view", name, "--json", "name")
	return cmd.Run() == nil
}

// RepoHasContent checks if a repository has any content.
func RepoHasContent(repoFullName string) (bool, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/contents", repoFullName),
		"-q", "length")
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputText := strings.TrimSpace(string(output))
		if repoContentsErrorIndicatesEmpty(outputText) {
			return false, nil
		}
		if outputText == "" {
			return false, fmt.Errorf("repo contents query failed: %w", err)
		}
		return false, fmt.Errorf("repo contents query failed: %w: %s", err, outputText)
	}

	length := strings.TrimSpace(string(output))
	return repoContentsLengthIndicatesContent(length), nil
}

func repoContentsLengthIndicatesContent(length string) bool {
	switch length {
	case "", "0":
		return false
	default:
		return true
	}
}

func repoContentsErrorIndicatesEmpty(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "404") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "repository is empty") ||
		strings.Contains(lower, "this repository is empty")
}

// InitAndPush initializes a git repository and pushes to GitHub.
func InitAndPush(bundlePath string, repoFullName string, forceOverwrite bool) error {
	// Check if repo has existing content
	hasContent, err := RepoHasContent(repoFullName)
	if err != nil {
		return fmt.Errorf("failed to check repository content: %w", err)
	}

	if hasContent && !forceOverwrite {
		return fmt.Errorf("repository %s has existing content - use ForceOverwrite option to overwrite", repoFullName)
	}

	remoteURL := fmt.Sprintf("https://github.com/%s.git", repoFullName)

	// Sequence of git commands
	commands := []struct {
		args []string
		desc string
	}{
		{[]string{"init"}, "Initializing git repository"},
		{[]string{"add", "."}, "Staging files"},
		{[]string{"commit", "-m", "Deploy static site via bv --pages"}, "Creating commit"},
		{[]string{"branch", "-M", "main"}, "Setting main branch"},
		{[]string{"remote", "add", "origin", remoteURL}, "Adding remote"},
	}

	// Check if remote already exists
	checkRemote := exec.Command("git", "remote", "get-url", "origin")
	checkRemote.Dir = bundlePath
	checkRemote.Env = loader.SanitizedGitEnv()
	if checkRemote.Run() == nil {
		// Remote exists, remove it first
		rmRemote := exec.Command("git", "remote", "remove", "origin")
		rmRemote.Dir = bundlePath
		rmRemote.Env = loader.SanitizedGitEnv()
		rmRemote.Run()
	}

	for _, c := range commands {
		fmt.Printf("  -> %s...\n", c.desc)
		commandName := firstArg(c.args)
		args := c.args
		if commandName == "commit" {
			args = gitCommitArgsForDir(bundlePath, c.args)
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = bundlePath
		cmd.Env = loader.SanitizedGitEnv()
		if output, err := cmd.CombinedOutput(); err != nil {
			switch commandName {
			case "branch":
				// Skip errors for branch -M if already on main
				if strings.Contains(string(output), "already") {
					continue
				}
			case "commit":
				// Skip commit error if nothing to commit
				if strings.Contains(string(output), "nothing to commit") {
					continue
				}
			}
			return fmt.Errorf("%s failed: %s", commandName, strings.TrimSpace(string(output)))
		}
	}

	// Configure git for large pushes (prevents HTTP 408 timeouts)
	configCmd := exec.Command("git", "config", "http.postBuffer", "524288000") // 500MB
	configCmd.Dir = bundlePath
	configCmd.Env = loader.SanitizedGitEnv()
	_ = configCmd.Run() // Ignore errors - this is best-effort

	// Push with force-with-lease for safety.
	// Fetch first so the lease is tied to the remote main ref we observed during this deploy.
	if hasContent {
		if err := fetchRemoteBranch(bundlePath, "main"); err != nil {
			return fmt.Errorf("preparing force-with-lease: %w", err)
		}
	}

	fmt.Println("  -> Pushing to GitHub...")
	pushArgs := []string{"push", "-u", "origin", "main"}
	if hasContent {
		// Use force-with-lease for safety when overwriting
		pushArgs = append(pushArgs, "--force-with-lease")
	}

	pushCmd := exec.Command("git", pushArgs...)
	pushCmd.Dir = bundlePath
	pushCmd.Env = loader.SanitizedGitEnv()
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return gitCommandOutputError("push", output, err)
	}

	return nil
}

func fetchRemoteBranch(bundlePath, branch string) error {
	cmd := exec.Command("git", "fetch", "--depth=1", "origin", branch)
	cmd.Dir = bundlePath
	cmd.Env = loader.SanitizedGitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		return gitCommandOutputError(fmt.Sprintf("git fetch origin %s", branch), output, err)
	}
	return nil
}

func gitCommandOutputError(context string, output []byte, err error) error {
	msg := strings.TrimSpace(string(output))
	if msg == "" {
		return fmt.Errorf("%s failed: %w", context, err)
	}
	return fmt.Errorf("%s failed: %w: %s", context, err, msg)
}

func firstArg(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func hasGitConfigValue(dir, key string) bool {
	cmd := exec.Command("git", "--no-replace-objects", "config", "--get", key)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = loader.SanitizedGitEnv()
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func gitCommitArgsForDir(dir string, commitArgs []string) []string {
	var prefix []string
	if !hasGitConfigValue(dir, "user.name") {
		prefix = append(prefix, "-c", "user.name=bv-pages")
	}
	if !hasGitConfigValue(dir, "user.email") {
		prefix = append(prefix, "-c", "user.email=bv-pages@users.noreply.github.com")
	}
	if len(prefix) == 0 {
		return commitArgs
	}
	return append(prefix, commitArgs...)
}

func switchToLegacyDeploymentArgs(repoFullName string) []string {
	return []string{
		"api", "-X", "PUT",
		fmt.Sprintf("repos/%s/pages", repoFullName),
		"-f", "build_type=legacy",
		"-f", "source[branch]=gh-pages",
		"-f", "source[path]=/",
	}
}

func enableGitHubPagesArgs(repoFullName string) []string {
	return []string{
		"api",
		fmt.Sprintf("repos/%s/pages", repoFullName),
		"-X", "POST",
		"-f", "build_type=workflow",
		"-f", "source[branch]=main",
		"-f", "source[path]=/",
	}
}

// EnableGitHubPages enables GitHub Pages for a repository.
func EnableGitHubPages(repoFullName string) (string, error) {
	fmt.Println("  -> Enabling GitHub Pages...")

	// Try to enable Pages via API
	cmd := exec.Command("gh", enableGitHubPagesArgs(repoFullName)...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already enabled
		if strings.Contains(string(output), "already exists") ||
			strings.Contains(string(output), "409") {
			fmt.Println("  -> GitHub Pages already enabled")
			return getGitHubPagesURL(repoFullName)
		}
		return "", fmt.Errorf("failed to enable GitHub Pages: %s", strings.TrimSpace(string(output)))
	}

	// Wait a moment for Pages to be configured
	fmt.Println("  -> Waiting for Pages configuration...")
	time.Sleep(3 * time.Second)

	return getGitHubPagesURL(repoFullName)
}

// getGitHubPagesURL retrieves the GitHub Pages URL for a repository.
func getGitHubPagesURL(repoFullName string) (string, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/pages", repoFullName),
		"-q", ".html_url")
	output, err := cmd.Output()
	if err != nil {
		// Construct URL manually as fallback
		parts := strings.Split(repoFullName, "/")
		if len(parts) == 2 {
			return fmt.Sprintf("https://%s.github.io/%s/", parts[0], parts[1]), nil
		}
		return "", fmt.Errorf("failed to get Pages URL")
	}

	url := strings.TrimSpace(string(output))
	if url == "" {
		// Construct URL manually as fallback
		parts := strings.Split(repoFullName, "/")
		if len(parts) == 2 {
			return fmt.Sprintf("https://%s.github.io/%s/", parts[0], parts[1]), nil
		}
	}

	return url, nil
}

// DeployToGitHubPages performs a complete deployment to GitHub Pages.
func DeployToGitHubPages(config GitHubDeployConfig) (*GitHubDeployResult, error) {
	// 1. Check gh CLI status
	status, err := CheckGHStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to check GitHub status: %w", err)
	}

	// 2. Handle missing gh CLI
	if !status.Installed {
		ShowInstallInstructions()
		return nil, fmt.Errorf("gh CLI is required for GitHub Pages deployment")
	}

	// 3. Handle missing authentication
	if !status.Authenticated {
		fmt.Println("\nYou are not authenticated with GitHub.")
		if config.SkipConfirmation {
			return nil, fmt.Errorf("GitHub authentication required - run 'gh auth login' first")
		}
		if !confirmPrompt("Would you like to authenticate now?") {
			return nil, fmt.Errorf("GitHub authentication required")
		}
		if err := AuthenticateGH(); err != nil {
			return nil, err
		}
		// Re-check status
		status, _ = CheckGHStatus()
		if !status.Authenticated {
			return nil, fmt.Errorf("authentication failed")
		}
	}

	// 4. Verify git identity
	if !status.GitConfigured && !config.SkipConfirmation {
		fmt.Println("\nGit identity is not configured.")
		fmt.Println("Configure with:")
		fmt.Println("  git config --global user.name \"Your Name\"")
		fmt.Println("  git config --global user.email \"your@email.com\"")
		return nil, fmt.Errorf("git identity not configured")
	}

	if !config.SkipConfirmation {
		fmt.Printf("\nGit identity: %s <%s>\n", status.GitName, status.GitEmail)
		fmt.Printf("GitHub user:  %s\n", status.Username)
		if !confirmPrompt("Is this correct?") {
			return nil, fmt.Errorf("deployment cancelled")
		}
	}

	// 5. Verify bundle path exists
	if _, err := os.Stat(config.BundlePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("bundle path does not exist: %s", config.BundlePath)
	}

	// 6. Create or use existing repository
	var repoFullName string
	repoExists := RepoExists(config.RepoName)

	if repoExists {
		repoFullName, err = getRepoFullName(config.RepoName)
		if err != nil {
			return nil, err
		}
		fmt.Printf("\nUsing existing repository: %s\n", repoFullName)

		// Check for existing content
		hasContent, err := RepoHasContent(repoFullName)
		if err != nil {
			return nil, fmt.Errorf("failed to check repository content: %w", err)
		}
		if hasContent && !config.ForceOverwrite && !config.SkipConfirmation {
			fmt.Println("\nRepository has existing content!")
			fmt.Println("Pushing will overwrite all existing files.")
			if !confirmPrompt("Continue anyway?") {
				return nil, fmt.Errorf("deployment cancelled - repository not empty")
			}
			config.ForceOverwrite = true
		}
	} else {
		fmt.Printf("\nCreating repository: %s\n", config.RepoName)
		repoFullName, err = CreateRepository(config.RepoName, config.Private, config.Description)
		if err != nil {
			return nil, err
		}
		fmt.Printf("Created: %s\n", repoFullName)
	}

	// 7. Initialize and push
	fmt.Println("\nDeploying to GitHub...")
	if err := InitAndPush(config.BundlePath, repoFullName, config.ForceOverwrite); err != nil {
		return nil, err
	}

	// 8. Enable GitHub Pages
	pagesURL, err := EnableGitHubPages(repoFullName)
	if err != nil {
		return nil, err
	}

	return &GitHubDeployResult{
		RepoFullName: repoFullName,
		PagesURL:     pagesURL,
		GitRemote:    fmt.Sprintf("https://github.com/%s.git", repoFullName),
	}, nil
}

// confirmPrompt asks for user confirmation.
func confirmPrompt(question string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N] ", question)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// GetGitHubPagesStatus checks if Pages is enabled and returns the URL.
type GitHubPagesStatus struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Path      string `json:"path,omitempty"`
	BuildType string `json:"build_type,omitempty"`
}

// CheckGitHubPagesStatus checks the GitHub Pages status for a repository.
func CheckGitHubPagesStatus(repoFullName string) (*GitHubPagesStatus, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/pages", repoFullName))
	output, err := cmd.Output()
	if err != nil {
		// 404 means not enabled
		return &GitHubPagesStatus{Enabled: false}, nil
	}

	var response struct {
		HTMLURL string `json:"html_url"`
		Source  struct {
			Branch string `json:"branch"`
			Path   string `json:"path"`
		} `json:"source"`
		BuildType string `json:"build_type"`
	}

	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Pages status: %w", err)
	}

	return &GitHubPagesStatus{
		Enabled:   true,
		URL:       response.HTMLURL,
		Branch:    response.Source.Branch,
		Path:      response.Source.Path,
		BuildType: response.BuildType,
	}, nil
}

// ListUserRepos lists repositories for the authenticated user.
func ListUserRepos(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 30
	}

	cmd := exec.Command("gh", "repo", "list",
		"--limit", fmt.Sprintf("%d", limit),
		"--json", "nameWithOwner",
		"-q", ".[].nameWithOwner")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w", err)
	}

	repos := strings.Split(strings.TrimSpace(string(output)), "\n")
	var result []string
	for _, repo := range repos {
		if repo != "" {
			result = append(result, repo)
		}
	}

	return result, nil
}

// DeleteRepository deletes a repository (requires confirmation).
func DeleteRepository(repoFullName string, confirm bool) error {
	if !confirm {
		return fmt.Errorf("repository deletion requires confirmation")
	}

	cmd := exec.Command("gh", "repo", "delete", repoFullName, "--yes")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete repository: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// OpenInBrowser opens a URL in the default browser.
// Set BV_NO_BROWSER=1 to suppress browser opening (useful for tests).
func OpenInBrowser(url string) error {
	cmd, err := prepareBrowserOpen(url)
	if err != nil || cmd == nil {
		return err
	}
	return cmd.Start()
}

// prepareBrowserOpen resolves everything a browser launch depends on — the
// BV_NO_BROWSER/BV_TEST_MODE gate, the platform command, its PATH lookup, and
// the environment — at the moment it is called, and returns a ready command
// (nil when opening is disabled). Callers that launch later from a goroutine
// must prepare here first: deferring the lookup let a delayed opener run under
// a different caller's environment (in the test suite, a previous test's
// preview server opened a browser inside the next test's stubbed PATH).
func prepareBrowserOpen(url string) (*exec.Cmd, error) {
	if env.NoBrowser.Get() != "" || env.TestMode.Get() != "" {
		return nil, nil
	}
	name, args, err := browserOpenCommandForGOOS(runtime.GOOS, url)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...) // resolves PATH now
	cmd.Env = os.Environ()             // snapshot the environment now
	return cmd, nil
}

func startBrowserURL(url string) error {
	cmd, err := prepareBrowserOpen(url)
	if err != nil || cmd == nil {
		return err
	}
	return cmd.Start()
}

func browserOpenCommandForGOOS(goos, url string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{url}, nil
	case "linux":
		return "xdg-open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	default:
		return "", nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// SuggestRepoName generates a suggested repository name from the bundle path.
func SuggestRepoName(bundlePath string) string {
	trimmed := strings.TrimSpace(bundlePath)
	var base string
	appendPages := false
	switch {
	case trimmed == "":
		if cwd, err := os.Getwd(); err == nil {
			base = filepath.Base(cwd)
			appendPages = true
		}
	case trimmed == ".":
		if cwd, err := os.Getwd(); err == nil {
			base = filepath.Base(cwd)
		}
	case isBeadsSourcePath(trimmed):
		if root := projectRootFromBeadsPath(trimmed); root != "" {
			base = filepath.Base(root)
			appendPages = true
		}
	default:
		candidate := filepath.Base(trimmed)
		if !isEmptyPathBase(candidate) {
			if isGenericPagesDir(candidate) {
				if abs, err := filepath.Abs(trimmed); err == nil {
					parent := filepath.Base(filepath.Dir(abs))
					if !isEmptyPathBase(parent) {
						base = parent
						appendPages = true
					}
				}
			} else {
				base = candidate
			}
		}
	}

	name := sanitizeGitHubRepoSegment(stripDotGitSuffix(base))
	if name == "" {
		return "beads-viewer-pages"
	}
	if appendPages && !strings.HasSuffix(name, "-pages") {
		name = strings.Trim(name, ".-_") + "-pages"
	}
	return name
}

func stripDotGitSuffix(name string) string {
	if len(name) >= 4 && strings.EqualFold(name[len(name)-4:], ".git") {
		return name[:len(name)-4]
	}
	return name
}

func sanitizeGitHubRepoSegment(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if isEmptyPathBase(lower) {
		return ""
	}
	var sb strings.Builder
	lastHyphen := false
	for _, r := range lower {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if isAllowed {
			sb.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			sb.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(sb.String(), ".-_")
}

func isEmptyPathBase(name string) bool {
	switch name {
	case ".", "/", "":
		return true
	default:
		return false
	}
}

func isGenericPagesDir(name string) bool {
	switch name {
	case "bv-pages", "pages", "docs", "dist":
		return true
	default:
		return false
	}
}

// GitHubActionsWorkflowContent returns the content for a static site deployment workflow.
// This workflow triggers on push to main and uses GitHub's official Pages actions.
const GitHubActionsWorkflowContent = `name: Deploy static content to Pages

on:
  push:
    branches: ["main"]
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

concurrency:
  group: "pages"
  cancel-in-progress: false

jobs:
  deploy:
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
      - name: Setup Pages
        uses: actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b # v5
      - name: Upload artifact
        uses: actions/upload-pages-artifact@56afc609e74202658d3ffba0e8f6dda462b719fa # v3
        with:
          path: '.'
      - name: Deploy to GitHub Pages
        id: deployment
        uses: actions/deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e # v4
`

// WriteGitHubActionsWorkflow creates the .github/workflows/static.yml file in the bundle.
// This ensures GitHub Pages deployment always triggers via an explicit workflow,
// not relying on the built-in Pages workflow which may not auto-trigger.
func WriteGitHubActionsWorkflow(bundlePath string) error {
	bundle, err := OpenBundle(bundlePath)
	if err != nil {
		return err
	}
	defer bundle.Close()

	workflowPath := filepath.Join(".github", "workflows", "static.yml")
	if err := bundle.WriteFile(workflowPath, []byte(GitHubActionsWorkflowContent), 0644); err != nil {
		return fmt.Errorf("write workflow file: %w", err)
	}

	return nil
}

// GitHubActionsStatus represents the status of GitHub Actions for a repository.
type GitHubActionsStatus struct {
	TotalCount          int
	HasActiveRun        bool
	WorkflowRunning     bool
	WorkflowQueued      bool
	LastRunStatus       string
	LastConclusion      string
	LastRunCreatedAt    string
	PossiblyRateLimited bool
}

func isGitHubActionsRunActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "in_progress", "waiting", "requested", "pending":
		return true
	default:
		return false
	}
}

func shouldFallbackToLegacyPages(status *GitHubActionsStatus) bool {
	if status == nil {
		return false
	}
	if status.PossiblyRateLimited {
		return true
	}
	if !status.HasActiveRun {
		if status.TotalCount == 0 {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(status.LastConclusion)) {
		case "failure", "cancelled", "startup_failure", "timed_out", "action_required":
			return true
		}
	}
	return false
}

// CheckGitHubActionsStatus checks if GitHub Actions is working properly for a repository.
// It detects missing, failed, or stuck/queued workflows that require legacy Pages fallback.
func CheckGitHubActionsStatus(repoFullName string) (*GitHubActionsStatus, error) {
	status := &GitHubActionsStatus{}

	// Query workflow runs from GitHub API without jq null-projection pitfalls.
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/actions/runs", repoFullName))
	output, err := cmd.Output()
	if err != nil {
		// No workflow runs / Actions unavailable - TotalCount remains 0.
		return status, nil
	}

	type actionsRunPayload struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		CreatedAt  string `json:"created_at"`
	}
	var payload struct {
		TotalCount   *int                `json:"total_count"`
		WorkflowRuns []actionsRunPayload `json:"workflow_runs"`
		Status       string              `json:"status"`
		Conclusion   string              `json:"conclusion"`
		CreatedAt    string              `json:"created_at"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return status, nil
	}

	runs := payload.WorkflowRuns
	if len(runs) == 0 && strings.TrimSpace(payload.Status) != "" {
		runs = []actionsRunPayload{{
			Status:     payload.Status,
			Conclusion: payload.Conclusion,
			CreatedAt:  payload.CreatedAt,
		}}
	}

	if payload.TotalCount != nil {
		status.TotalCount = *payload.TotalCount
	} else {
		status.TotalCount = len(runs)
	}

	for _, r := range runs {
		if isGitHubActionsRunActive(r.Status) {
			status.HasActiveRun = true
			break
		}
	}

	if len(runs) == 0 {
		return status, nil
	}

	latest := runs[0]
	normStatus := strings.ToLower(strings.TrimSpace(latest.Status))
	status.LastRunStatus = normStatus
	status.LastConclusion = strings.ToLower(strings.TrimSpace(latest.Conclusion))
	status.LastRunCreatedAt = latest.CreatedAt

	if normStatus == "queued" {
		status.WorkflowQueued = true
		// Check if it's been queued for more than 2 minutes - might be rate limited
		createdAt, err := time.Parse(time.RFC3339, latest.CreatedAt)
		if err == nil && time.Since(createdAt) > 2*time.Minute {
			status.PossiblyRateLimited = true
		}
	} else if normStatus == "in_progress" {
		status.WorkflowRunning = true
	}

	return status, nil
}

// SwitchToLegacyDeployment switches GitHub Pages from workflow-based to legacy branch-based deployment.
// This is useful when GitHub Actions is rate-limited or the workflow isn't triggering.
// Returns the gh-pages branch name that should be pushed to.
func SwitchToLegacyDeployment(repoFullName string) error {
	fmt.Println("  -> Switching to legacy branch-based deployment...")

	// Update Pages source to use gh-pages branch with legacy build type
	cmd := exec.Command("gh", switchToLegacyDeploymentArgs(repoFullName)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's just telling us the config is already set
		if strings.Contains(string(output), "already") || strings.Contains(string(output), "422") {
			return nil
		}
		return fmt.Errorf("failed to switch to legacy deployment: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// PushToGHPagesBranch pushes the current bundle commit to the gh-pages branch for legacy deployment.
func PushToGHPagesBranch(bundlePath string, repoFullName string) error {
	fmt.Println("  -> Preparing gh-pages branch...")

	commands := []struct {
		args []string
		desc string
	}{
		{[]string{"add", "."}, "Staging files"},
		{[]string{"commit", "-m", "Deploy via legacy gh-pages branch"}, "Creating commit"},
	}

	for _, c := range commands {
		commandName := firstArg(c.args)
		args := c.args
		if commandName == "commit" {
			args = gitCommitArgsForDir(bundlePath, c.args)
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = bundlePath
		cmd.Env = loader.SanitizedGitEnv()
		if output, err := cmd.CombinedOutput(); err != nil {
			switch commandName {
			case "commit":
				// Skip commit error if nothing to commit
				if strings.Contains(string(output), "nothing to commit") {
					continue
				}
			}
			return fmt.Errorf("%s failed: %s", commandName, strings.TrimSpace(string(output)))
		}
	}

	// Push the current export commit to gh-pages. Do not check out an existing
	// gh-pages branch here: that would replace the freshly exported bundle with
	// the old branch contents before staging and can redeploy stale pages.
	fmt.Println("  -> Pushing gh-pages branch...")
	pushArgs := []string{"push", "-u", "origin", "HEAD:gh-pages"}
	if err := fetchRemoteBranch(bundlePath, "gh-pages"); err == nil {
		pushArgs = append(pushArgs, "--force-with-lease")
	}

	pushCmd := exec.Command("git", pushArgs...)
	pushCmd.Dir = bundlePath
	pushCmd.Env = loader.SanitizedGitEnv()
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return gitCommandOutputError("push gh-pages", output, err)
	}

	return nil
}

func sleepDeploymentPoll(interval time.Duration, deadline time.Time) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	if remaining < interval {
		interval = remaining
	}
	time.Sleep(interval)
}

func fetchDeploymentMetaJSON(client *http.Client, metaURL string, deadline time.Time) ([]byte, error) {
	reqTimeout := 10 * time.Second
	if remaining := time.Until(deadline); remaining > 0 && remaining < reqTimeout {
		reqTimeout = remaining
	}
	ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if readErr != nil {
		return nil, readErr
	}
	return body, nil
}

// VerifyGitHubPagesDeployment polls the live site to verify deployment succeeded.
// It checks that meta.json is accessible and contains the expected issue count.
func VerifyGitHubPagesDeployment(pagesURL string, expectedIssueCount int, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 90 * time.Second
	}

	metaURL := strings.TrimSuffix(pagesURL, "/") + "/data/meta.json"
	deadline := time.Now().Add(timeout)
	httpClient := &http.Client{Timeout: 10 * time.Second}
	var lastErr error

	fmt.Printf("  -> Verifying deployment at %s...\n", pagesURL)

	for time.Now().Before(deadline) {
		output, err := fetchDeploymentMetaJSON(httpClient, metaURL, deadline)
		if err != nil {
			lastErr = fmt.Errorf("fetch failed: %w", err)
			sleepDeploymentPoll(5*time.Second, deadline)
			continue
		}

		// Parse meta.json
		var meta struct {
			IssueCount int `json:"issue_count"`
		}
		if err := json.Unmarshal(output, &meta); err != nil {
			lastErr = fmt.Errorf("parse failed: %w", err)
			sleepDeploymentPoll(5*time.Second, deadline)
			continue
		}

		// Check issue count matches expected. Zero is a valid expected
		// count for empty exports; negative disables count comparison.
		if expectedIssueCount >= 0 && meta.IssueCount != expectedIssueCount {
			return fmt.Errorf("deployment verification issue count mismatch: live site shows %d issues, expected %d",
				meta.IssueCount, expectedIssueCount)
		}

		fmt.Printf("  ✓ Deployment verified: %d issues live\n", meta.IssueCount)
		return nil
	}

	if lastErr != nil {
		return fmt.Errorf("deployment verification failed for %s: %w", metaURL, lastErr)
	}
	return fmt.Errorf("deployment verification timed out for %s", metaURL)
}

// DeployToGitHubPagesWithFallback performs deployment with automatic fallback to legacy mode
// if workflow-based deployment fails or is rate-limited.
func DeployToGitHubPagesWithFallback(config GitHubDeployConfig, expectedIssueCount int) (*GitHubDeployResult, error) {
	// First, try normal deployment
	// Note: The GitHub Actions workflow should already be in the bundle
	// (added by CopyEmbeddedAssets or the wizard before deployment)
	result, err := DeployToGitHubPages(config)
	if err != nil {
		return nil, err
	}

	// Wait a moment for Pages to process
	if githubActionsInitialWait > 0 {
		time.Sleep(githubActionsInitialWait)
	}

	// Check if workflow deployment is working
	actionsStatus, _ := CheckGitHubActionsStatus(result.RepoFullName)
	if shouldFallbackToLegacyPages(actionsStatus) {
		if actionsStatus != nil && actionsStatus.PossiblyRateLimited {
			fmt.Println("\n  ⚠ GitHub Actions appears to be rate-limited (workflow stuck in queue)")
		} else {
			fmt.Println("\n  ⚠ GitHub Actions workflow did not trigger or failed")
		}
		fmt.Println("  -> Attempting fallback to legacy branch-based deployment...")

		// Try legacy deployment
		if err := SwitchToLegacyDeployment(result.RepoFullName); err != nil {
			fmt.Printf("  Warning: Could not switch to legacy mode: %v\n", err)
		} else {
			if err := PushToGHPagesBranch(config.BundlePath, result.RepoFullName); err != nil {
				fmt.Printf("  Warning: Legacy push failed: %v\n", err)
			} else {
				fmt.Println("  ✓ Fallback to legacy deployment succeeded")
			}
		}
	}

	// Verify deployment if we have an expected issue count. Zero is valid for
	// empty exports; a negative sentinel means the caller opted out.
	if expectedIssueCount >= 0 {
		if err := VerifyGitHubPagesDeployment(result.PagesURL, expectedIssueCount, 90*time.Second); err != nil {
			return nil, fmt.Errorf("verify github pages deployment: %w", err)
		}
	}

	return result, nil
}
