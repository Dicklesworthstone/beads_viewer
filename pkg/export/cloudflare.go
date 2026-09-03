// Package export provides data export functionality for bv.
//
// This file implements Cloudflare wrangler CLI integration for deploying
// static sites to Cloudflare Pages. It follows safety-first principles:
// no auto-install without confirmation, clear prompts for authentication.
package export

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// Package-level compiled regexes for Cloudflare operations (avoids recompilation per call)
var (
	cfPagesDevURLRegex    = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.pages\.dev[^\s]*`)
	cfCustomDomainRegex   = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.[a-zA-Z0-9-]+\.[a-zA-Z]{2,}[^\s]*`)
	cfDeploymentIDRegex   = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
	cfNonAlphanumRegex    = regexp.MustCompile(`[^a-z0-9-]`)
	cfMultipleHyphenRegex = regexp.MustCompile(`-+`)
	cfProjectNameRegex    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
)

const cloudflareProjectNameMaxLen = 63

// CloudflareDeployConfig configures Cloudflare Pages deployment.
type CloudflareDeployConfig struct {
	// ProjectName is the Cloudflare Pages project name
	ProjectName string

	// BundlePath is the path to the static site bundle to deploy
	BundlePath string

	// Branch is the branch name for deployment (default: main)
	Branch string

	// SkipConfirmation skips interactive confirmation prompts (for CI)
	SkipConfirmation bool
}

// CloudflareDeployResult contains the result of a deployment.
type CloudflareDeployResult struct {
	// ProjectName is the Cloudflare Pages project name
	ProjectName string

	// URL is the deployment URL (xxx.pages.dev)
	URL string

	// DeploymentID is the unique deployment identifier
	DeploymentID string
}

// CloudflareStatus represents the current status of wrangler CLI.
type CloudflareStatus struct {
	Installed     bool
	Authenticated bool
	AccountName   string
	AccountID     string
	NPMInstalled  bool
}

// CheckWranglerStatus checks the status of wrangler CLI.
func CheckWranglerStatus() (*CloudflareStatus, error) {
	status := &CloudflareStatus{}

	// Check npm installation (required for wrangler install)
	_, err := exec.LookPath("npm")
	status.NPMInstalled = err == nil

	// Check wrangler CLI installation
	_, err = exec.LookPath("wrangler")
	status.Installed = err == nil

	if status.Installed {
		// First check for CLOUDFLARE_API_TOKEN env var (used in CI and headless environments).
		if token := os.Getenv("CLOUDFLARE_API_TOKEN"); token != "" {
			status.Authenticated = true
			status.AccountName = "(API token)"
			status.AccountID = os.Getenv("CLOUDFLARE_ACCOUNT_ID")
		}

		// Fall back to checking the wrangler config file directly. This avoids
		// the unreliable `wrangler whoami` which can hang on headless servers.
		if !status.Authenticated {
			status.Authenticated = checkWranglerConfigFile()
		}

		// Finally try `wrangler whoami` with a timeout to extract account details,
		// but only if we haven't already confirmed auth above.
		if !status.Authenticated {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "wrangler", "whoami")
			output, err := cmd.CombinedOutput()
			outputStr := string(output)

			// wrangler whoami returns 0 even when not authenticated
			// Check output for authentication indicators
			status.Authenticated = err == nil &&
				!strings.Contains(outputStr, "not authenticated") &&
				!strings.Contains(outputStr, "You are not authenticated") &&
				(strings.Contains(outputStr, "Account ID") ||
					strings.Contains(outputStr, "account") ||
					strings.Contains(outputStr, "@"))

			if status.Authenticated {
				status.AccountName, status.AccountID = parseWranglerWhoami(outputStr)
			}
		}
	}

	return status, nil
}

// checkWranglerConfigFile checks if a valid wrangler OAuth config file exists.
// Wrangler stores credentials in different locations depending on version:
//   - ~/.wrangler/config/default.toml (wrangler v3+)
//   - ~/.config/.wrangler/config/default.toml (some installations)
//   - $XDG_CONFIG_HOME/.wrangler/config/default.toml (XDG-aware)
func checkWranglerConfigFile() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// Build list of candidate paths (checked in order).
	candidates := []string{
		filepath.Join(home, ".wrangler", "config", "default.toml"),
		filepath.Join(home, ".config", ".wrangler", "config", "default.toml"),
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, ".wrangler", "config", "default.toml"))
	}

	for _, configPath := range candidates {
		if validWranglerConfig(configPath) {
			return true
		}
	}
	return false
}

// validWranglerConfig reads a wrangler config TOML and returns true if it
// contains an oauth_token that hasn't expired (or has a refresh_token).
func validWranglerConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	if !strings.Contains(content, "oauth_token") {
		return false
	}
	hasRefreshToken := strings.Contains(content, "refresh_token")
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "expiration_time") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				timeStr := strings.Trim(strings.TrimSpace(parts[1]), "\"")
				expiry, err := time.Parse(time.RFC3339, timeStr)
				if err == nil && time.Now().After(expiry) && !hasRefreshToken {
					return false // Token expired and no refresh token
				}
			}
		}
	}
	return true
}

// parseWranglerWhoami extracts account info from wrangler whoami output.
func parseWranglerWhoami(output string) (name, id string) {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for account name patterns
		if strings.Contains(line, "Account Name:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) > 1 {
				name = strings.TrimSpace(parts[1])
			}
		}

		// Look for account ID patterns
		if strings.Contains(line, "Account ID:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) > 1 {
				id = strings.TrimSpace(parts[1])
			}
		}

		// Also check for email pattern as name fallback
		if name == "" && strings.Contains(line, "@") && !strings.Contains(line, "http") {
			// This might be an email
			name = strings.TrimSpace(line)
		}
	}

	return name, id
}

// ShowWranglerInstallInstructions prints wrangler CLI installation instructions.
func ShowWranglerInstallInstructions() {
	fmt.Println("\nwrangler CLI is not installed.")
	fmt.Println("\nInstallation options:")
	fmt.Println("  npm install -g wrangler")
	fmt.Println("  # or")
	fmt.Println("  yarn global add wrangler")
	fmt.Println("")
	fmt.Println("Requires Node.js to be installed.")
	fmt.Println("  Download from: https://nodejs.org/")
	fmt.Println("")
}

// AttemptWranglerInstall attempts to install wrangler via npm.
func AttemptWranglerInstall() error {
	// Check if npm is available
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm not found - install Node.js from https://nodejs.org/")
	}

	fmt.Println("Installing wrangler via npm...")
	cmd := exec.Command("npm", "install", "-g", "wrangler")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("npm install wrangler failed: %w", err)
	}

	fmt.Println("wrangler CLI installed successfully!")
	return nil
}

// AuthenticateWrangler starts the interactive wrangler authentication flow.
func AuthenticateWrangler() error {
	fmt.Println("\nStarting Cloudflare authentication...")
	fmt.Println("This will open a browser for authentication.")
	fmt.Println("")

	cmd := exec.Command("wrangler", "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wrangler login failed: %w", err)
	}

	return nil
}

// GenerateHeadersFile creates a _headers file for Cloudflare Pages.
// This provides security headers without needing a service worker.
func GenerateHeadersFile(bundlePath string) error {
	headersContent := "/*\n" +
		"  X-Frame-Options: DENY\n" +
		"  X-Content-Type-Options: nosniff\n" +
		"  Referrer-Policy: strict-origin-when-cross-origin\n" +
		"  Cross-Origin-Opener-Policy: same-origin\n" +
		"  Cross-Origin-Embedder-Policy: require-corp\n" +
		"  Cross-Origin-Resource-Policy: same-origin\n" +
		"  Permissions-Policy: accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()\n" +
		"\n" +
		"/*.js\n" +
		"  Content-Type: application/javascript; charset=utf-8\n" +
		"\n" +
		"/*.wasm\n" +
		"  Content-Type: application/wasm\n" +
		"\n" +
		"/*.css\n" +
		"  Content-Type: text/css; charset=utf-8\n"

	headersPath := filepath.Join(bundlePath, "_headers")
	if err := os.WriteFile(headersPath, []byte(headersContent), 0644); err != nil {
		return fmt.Errorf("failed to write _headers file: %w", err)
	}

	return nil
}

// parseCloudflareURL extracts the deployment URL from wrangler output.
func parseCloudflareURL(output string) string {
	// Look for pattern: https://xxx.pages.dev or https://xxx-xxx.pages.dev
	match := cfPagesDevURLRegex.FindString(output)
	if match != "" {
		// Clean up any trailing punctuation
		match = strings.TrimRight(match, ".,;:\"'")
		return match
	}

	// Also look for custom domain patterns in case configured
	match = cfCustomDomainRegex.FindString(output)
	if match != "" && strings.Contains(output, "pages") {
		match = strings.TrimRight(match, ".,;:\"'")
		return match
	}

	return ""
}

// parseDeploymentID extracts the deployment ID from wrangler output.
func parseDeploymentID(output string) string {
	// Look for deployment ID patterns (typically UUID-like)
	return cfDeploymentIDRegex.FindString(output)
}

// SuggestProjectName generates a suggested Cloudflare Pages project name.
func SuggestProjectName(bundlePath string) string {
	// Use the directory name
	name := filepath.Base(bundlePath)
	if isEmptyPathBase(name) {
		// Get parent dir name
		abs, err := filepath.Abs(bundlePath)
		if err == nil {
			name = filepath.Base(filepath.Dir(abs))
		}
	}

	// If it's bv-pages or similar, use parent project name
	if isGenericCloudflareOutputDir(name) {
		abs, err := filepath.Abs(bundlePath)
		if err == nil {
			parent := filepath.Base(filepath.Dir(abs))
			if !isEmptyPathBase(parent) {
				name = parent + "-pages"
			} else {
				name = ""
			}
		}
	}

	// Sanitize for Cloudflare project name (alphanumeric and hyphens only)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, ".", "-") // Convert dots to hyphens to separate words

	// Remove any characters that aren't alphanumeric or hyphens
	name = cfNonAlphanumRegex.ReplaceAllString(name, "")

	// Remove leading/trailing hyphens and collapse multiple hyphens
	name = strings.Trim(name, "-")
	name = cfMultipleHyphenRegex.ReplaceAllString(name, "-")
	if name == "" {
		return "beads-viewer-pages"
	}

	return name
}

func isGenericCloudflareOutputDir(name string) bool {
	switch name {
	case "bv-pages", "pages", "docs", "dist":
		return true
	default:
		return false
	}
}

func validateCloudflareProjectName(projectName string) error {
	trimmed := strings.TrimSpace(projectName)
	if trimmed == "" {
		return fmt.Errorf("cloudflare project name is required")
	}
	if trimmed != projectName {
		return fmt.Errorf("cloudflare project name %q must not contain leading or trailing whitespace", projectName)
	}
	if len(projectName) > cloudflareProjectNameMaxLen {
		return fmt.Errorf("cloudflare project name %q is too long: %d characters, max %d",
			projectName, len(projectName), cloudflareProjectNameMaxLen)
	}
	if !cfProjectNameRegex.MatchString(projectName) {
		return fmt.Errorf("cloudflare project name %q must contain only lowercase letters, numbers, and hyphens, and start and end with a letter or number", projectName)
	}
	return nil
}

// DeployToCloudflarePages performs a complete deployment to Cloudflare Pages.
func DeployToCloudflarePages(config CloudflareDeployConfig) (*CloudflareDeployResult, error) {
	// Set default branch
	if config.Branch == "" {
		config.Branch = "main"
	}
	if err := validateCloudflareProjectName(config.ProjectName); err != nil {
		return nil, err
	}

	// 1. Check wrangler CLI status
	status, err := CheckWranglerStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to check wrangler status: %w", err)
	}

	// 2. Handle missing wrangler CLI
	if !status.Installed {
		if !status.NPMInstalled {
			fmt.Println("\nNode.js/npm is required to install wrangler.")
			fmt.Println("Download from: https://nodejs.org/")
			return nil, fmt.Errorf("npm is required to install wrangler CLI")
		}

		ShowWranglerInstallInstructions()

		if config.SkipConfirmation {
			return nil, fmt.Errorf("wrangler CLI is required - run 'npm install -g wrangler' first")
		}

		if !cloudflareConfirmPrompt("Would you like to install wrangler now?") {
			return nil, fmt.Errorf("wrangler CLI is required for Cloudflare Pages deployment")
		}

		if err := AttemptWranglerInstall(); err != nil {
			return nil, err
		}

		// Re-check status
		status, _ = CheckWranglerStatus()
		if !status.Installed {
			return nil, fmt.Errorf("wrangler installation failed")
		}
	}

	// 3. Handle missing authentication
	if !status.Authenticated {
		fmt.Println("\nYou are not authenticated with Cloudflare.")
		if config.SkipConfirmation {
			return nil, fmt.Errorf("cloudflare authentication required - run 'wrangler login' first")
		}
		if !cloudflareConfirmPrompt("Would you like to authenticate now?") {
			return nil, fmt.Errorf("cloudflare authentication required")
		}
		if err := AuthenticateWrangler(); err != nil {
			return nil, err
		}
		// Re-check status
		status, _ = CheckWranglerStatus()
		if !status.Authenticated {
			return nil, fmt.Errorf("authentication failed")
		}
	}

	// 4. Show account info
	if !config.SkipConfirmation && status.AccountName != "" {
		fmt.Printf("\nCloudflare account: %s\n", status.AccountName)
		if status.AccountID != "" {
			fmt.Printf("Account ID: %s\n", status.AccountID)
		}
		if !cloudflareConfirmPrompt("Deploy to this account?") {
			return nil, fmt.Errorf("deployment cancelled")
		}
	}

	// 5. Verify bundle path exists
	if _, err := os.Stat(config.BundlePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("bundle path does not exist: %s", config.BundlePath)
	}

	// 6. Generate _headers file for Cloudflare
	fmt.Println("\n  -> Generating _headers file...")
	if err := GenerateHeadersFile(config.BundlePath); err != nil {
		// Non-fatal, just warn
		fmt.Printf("  Warning: %v\n", err)
	}

	// 7. Deploy to Cloudflare Pages
	fmt.Printf("\n  -> Deploying to Cloudflare Pages (project: %s)...\n", config.ProjectName)

	cmd := exec.Command("wrangler", "pages", "deploy",
		config.BundlePath,
		"--project-name", config.ProjectName,
		"--branch", config.Branch,
	)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		return nil, fmt.Errorf("deployment failed: %s\n%s", err, outputStr)
	}

	// 8. Parse deployment result
	deployURL := parseCloudflareURL(outputStr)
	deployID := parseDeploymentID(outputStr)

	if deployURL == "" {
		// Try to construct URL from project name
		deployURL = fmt.Sprintf("https://%s.pages.dev", config.ProjectName)
	}

	fmt.Println("  -> Deployment complete!")

	return &CloudflareDeployResult{
		ProjectName:  config.ProjectName,
		URL:          deployURL,
		DeploymentID: deployID,
	}, nil
}

// cloudflareConfirmPrompt asks for user confirmation.
func cloudflareConfirmPrompt(question string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N] ", question)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}

// ListCloudflareProjects lists existing Cloudflare Pages projects.
func ListCloudflareProjects() ([]string, error) {
	cmd := exec.Command("wrangler", "pages", "project", "list")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	// Parse output - each line is a project
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var projects []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip header lines and empty lines
		if line == "" || strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "---") {
			continue
		}
		// First column is the project name
		fields := strings.Fields(line)
		if len(fields) > 0 {
			projects = append(projects, fields[0])
		}
	}

	return projects, nil
}

// DeleteCloudflareProject deletes a Cloudflare Pages project.
func DeleteCloudflareProject(projectName string, confirm bool) error {
	if !confirm {
		return fmt.Errorf("project deletion requires confirmation")
	}
	if err := validateCloudflareProjectName(projectName); err != nil {
		return err
	}

	cmd := exec.Command("wrangler", "pages", "project", "delete", projectName, "--yes")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete project: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// OpenCloudflareInBrowser opens the Cloudflare Pages dashboard in browser.
// Set BV_NO_BROWSER=1 to suppress browser opening (useful for tests).
func OpenCloudflareInBrowser(projectName string) error {
	// Skip browser opening in test mode or when explicitly disabled
	if env.NoBrowser.Get() != "" || env.TestMode.Get() != "" {
		return nil
	}
	if err := validateCloudflareProjectName(projectName); err != nil {
		return err
	}

	url := fmt.Sprintf("https://dash.cloudflare.com/?to=/:account/pages/view/%s", projectName)

	return startBrowserURL(url)
}

// CloudflareProjectExists checks if a Cloudflare Pages project exists.
func CloudflareProjectExists(projectName string) (bool, error) {
	if err := validateCloudflareProjectName(projectName); err != nil {
		return false, err
	}

	// Try to get project info - if it fails with "not found", project doesn't exist
	cmd := exec.Command("wrangler", "pages", "project", "list")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to list projects: %w", err)
	}

	// Check if project name appears in the output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == projectName {
			return true, nil
		}
	}

	return false, nil
}

// CreateCloudflareProject creates a new Cloudflare Pages project.
func CreateCloudflareProject(projectName string, productionBranch string) error {
	if err := validateCloudflareProjectName(projectName); err != nil {
		return err
	}
	if productionBranch == "" {
		productionBranch = "main"
	}

	fmt.Printf("  -> Creating Cloudflare Pages project: %s...\n", projectName)

	cmd := exec.Command("wrangler", "pages", "project", "create", projectName,
		"--production-branch", productionBranch)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Check if project already exists (not an error)
		if strings.Contains(outputStr, "already exists") ||
			strings.Contains(outputStr, "A project with this name already exists") {
			fmt.Println("  -> Project already exists")
			return nil
		}
		return fmt.Errorf("failed to create project: %s", strings.TrimSpace(outputStr))
	}

	fmt.Printf("  ✓ Project created: https://%s.pages.dev/\n", projectName)
	return nil
}

// EnsureCloudflareProject ensures a Cloudflare Pages project exists, creating it if necessary.
func EnsureCloudflareProject(projectName string, productionBranch string) error {
	if err := validateCloudflareProjectName(projectName); err != nil {
		return err
	}

	exists, err := CloudflareProjectExists(projectName)
	if err != nil {
		// Can't check, try to create anyway
		return CreateCloudflareProject(projectName, productionBranch)
	}

	if !exists {
		return CreateCloudflareProject(projectName, productionBranch)
	}

	return nil
}

// VerifyCloudflareDeployment polls the live site to verify deployment succeeded.
func VerifyCloudflareDeployment(deployURL string, expectedIssueCount int, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second // Cloudflare is usually faster
	}

	metaURL := strings.TrimSuffix(deployURL, "/") + "/data/meta.json"
	deadline := time.Now().Add(timeout)
	var lastErr error

	fmt.Printf("  -> Verifying deployment at %s...\n", deployURL)

	for time.Now().Before(deadline) {
		cmd := exec.Command("curl", "-sf", "--max-time", "10", metaURL)
		output, err := cmd.Output()
		if err != nil {
			lastErr = fmt.Errorf("fetch failed: %w", err)
			time.Sleep(3 * time.Second)
			continue
		}

		// Parse meta.json
		var meta struct {
			IssueCount int `json:"issue_count"`
		}
		if err := json.Unmarshal(output, &meta); err != nil {
			lastErr = fmt.Errorf("parse failed: %w", err)
			time.Sleep(3 * time.Second)
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

// DeployToCloudflareWithAutoCreate performs deployment with automatic project creation.
func DeployToCloudflareWithAutoCreate(config CloudflareDeployConfig, expectedIssueCount int) (*CloudflareDeployResult, error) {
	// Set default branch
	if config.Branch == "" {
		config.Branch = "main"
	}
	if err := validateCloudflareProjectName(config.ProjectName); err != nil {
		return nil, err
	}

	// 1. Check wrangler CLI status
	status, err := CheckWranglerStatus()
	if err != nil {
		return nil, fmt.Errorf("failed to check wrangler status: %w", err)
	}

	// 2. Handle missing wrangler CLI
	if !status.Installed {
		if !status.NPMInstalled {
			fmt.Println("\nNode.js/npm is required to install wrangler.")
			fmt.Println("Download from: https://nodejs.org/")
			return nil, fmt.Errorf("npm is required to install wrangler CLI")
		}

		ShowWranglerInstallInstructions()

		if config.SkipConfirmation {
			return nil, fmt.Errorf("wrangler CLI is required - run 'npm install -g wrangler' first")
		}

		if !cloudflareConfirmPrompt("Would you like to install wrangler now?") {
			return nil, fmt.Errorf("wrangler CLI is required for Cloudflare Pages deployment")
		}

		if err := AttemptWranglerInstall(); err != nil {
			return nil, err
		}

		// Re-check status
		status, _ = CheckWranglerStatus()
		if !status.Installed {
			return nil, fmt.Errorf("wrangler installation failed")
		}
	}

	// 3. Handle missing authentication
	if !status.Authenticated {
		fmt.Println("\nYou are not authenticated with Cloudflare.")
		if config.SkipConfirmation {
			return nil, fmt.Errorf("cloudflare authentication required - run 'wrangler login' first")
		}
		if !cloudflareConfirmPrompt("Would you like to authenticate now?") {
			return nil, fmt.Errorf("cloudflare authentication required")
		}
		if err := AuthenticateWrangler(); err != nil {
			return nil, err
		}
		// Re-check status
		status, _ = CheckWranglerStatus()
		if !status.Authenticated {
			return nil, fmt.Errorf("authentication failed")
		}
	}

	// 4. Show account info
	if !config.SkipConfirmation && status.AccountName != "" {
		fmt.Printf("\nCloudflare account: %s\n", status.AccountName)
		if status.AccountID != "" {
			fmt.Printf("Account ID: %s\n", status.AccountID)
		}
		if !cloudflareConfirmPrompt("Deploy to this account?") {
			return nil, fmt.Errorf("deployment cancelled")
		}
	}

	// 5. Verify bundle path exists
	if _, err := os.Stat(config.BundlePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("bundle path does not exist: %s", config.BundlePath)
	}

	// 6. Ensure project exists (auto-create if not)
	if err := EnsureCloudflareProject(config.ProjectName, config.Branch); err != nil {
		return nil, fmt.Errorf("failed to ensure project exists: %w", err)
	}

	// 7. Generate _headers file for Cloudflare
	fmt.Println("\n  -> Generating _headers file...")
	if err := GenerateHeadersFile(config.BundlePath); err != nil {
		// Non-fatal, just warn
		fmt.Printf("  Warning: %v\n", err)
	}

	// 8. Deploy to Cloudflare Pages
	fmt.Printf("\n  -> Deploying to Cloudflare Pages (project: %s)...\n", config.ProjectName)

	cmd := exec.Command("wrangler", "pages", "deploy",
		config.BundlePath,
		"--project-name", config.ProjectName,
		"--branch", config.Branch,
		"--commit-dirty=true", // Don't warn about uncommitted changes in bundle
	)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		return nil, fmt.Errorf("deployment failed: %s\n%s", err, outputStr)
	}

	// 9. Parse deployment result
	deployURL := parseCloudflareURL(outputStr)
	deployID := parseDeploymentID(outputStr)

	if deployURL == "" {
		// Try to construct URL from project name
		deployURL = fmt.Sprintf("https://%s.pages.dev", config.ProjectName)
	}

	fmt.Println("  -> Deployment complete!")

	result := &CloudflareDeployResult{
		ProjectName:  config.ProjectName,
		URL:          deployURL,
		DeploymentID: deployID,
	}

	// 10. Verify deployment. Zero is valid for empty exports; a negative
	// sentinel means the caller opted out.
	if expectedIssueCount >= 0 {
		if err := VerifyCloudflareDeployment(deployURL, expectedIssueCount, 30*time.Second); err != nil {
			return nil, fmt.Errorf("verify cloudflare deployment: %w", err)
		}
	}

	return result, nil
}
