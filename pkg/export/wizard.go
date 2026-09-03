// Package export provides data export functionality for bv.
//
// This file implements the interactive deployment wizard for --pages flag.
// It guides users through exporting and deploying static sites to GitHub Pages.
package export

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// WizardConfig holds configuration for the deployment wizard.
type WizardConfig struct {
	// Export options
	IncludeClosed  bool   `json:"include_closed"`
	IncludeHistory bool   `json:"include_history"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle,omitempty"`

	// Source metadata for reliable updates
	SourceBeadsDir string `json:"source_beads_dir,omitempty"`
	SourceRepoRoot string `json:"source_repo_root,omitempty"`
	SourcePath     string `json:"source_path,omitempty"`
	LastIssueCount int    `json:"last_issue_count,omitempty"`
	LastDataHash   string `json:"last_data_hash,omitempty"`

	// Deployment target
	DeployTarget string `json:"deploy_target"` // "github", "cloudflare", "local"

	// GitHub options
	RepoName        string `json:"repo_name,omitempty"`
	RepoPrivate     bool   `json:"repo_private,omitempty"`
	RepoDescription string `json:"repo_description,omitempty"`

	// Cloudflare options
	CloudflareProject string `json:"cloudflare_project,omitempty"`
	CloudflareBranch  string `json:"cloudflare_branch,omitempty"`

	// Output path for bundle
	OutputPath string `json:"output_path,omitempty"`
}

// WizardResult contains the result of running the wizard.
type WizardResult struct {
	BundlePath   string
	RepoFullName string
	PagesURL     string
	DeployTarget string
	// Cloudflare-specific
	CloudflareProject string
	CloudflareURL     string
}

// Wizard handles the interactive deployment flow.
type Wizard struct {
	config     *WizardConfig
	beadsPath  string
	bundlePath string
	isUpdate   bool // true when updating an existing deployment
	input      io.Reader
}

// NewWizard creates a new deployment wizard.
func NewWizard(beadsPath string) *Wizard {
	return &Wizard{
		config: &WizardConfig{
			IncludeClosed:  true, // Include all issues by default
			IncludeHistory: true, // Include git history by default
		},
		beadsPath: beadsPath,
	}
}

// isTerminal checks if stdin is connected to a terminal
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// newForm creates a form with appropriate settings based on TTY detection
func newForm(groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithTheme(huh.ThemeDracula())
	if !isTerminal() {
		form = form.WithAccessible(true)
	}
	return form
}

func (w *Wizard) newForm(groups ...*huh.Group) *huh.Form {
	form := newForm(groups...)
	if w != nil && w.input != nil {
		form = form.WithInput(w.input)
	}
	return form
}

// wizardInputReader prevents huh's per-field scanners from reading ahead into
// later answers and discarding them when a field completes.
type wizardInputReader struct {
	data   []byte
	offset int
}

func (r *wizardInputReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r == nil || r.offset >= len(r.data) {
		return 0, io.EOF
	}

	remaining := r.data[r.offset:]
	n := len(remaining)
	if newline := bytes.IndexByte(remaining, '\n'); newline >= 0 {
		n = newline + 1
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, remaining[:n])
	r.offset += n
	return n, nil
}

// readWizardInput reads a non-terminal stdin without leaving an uncancellable
// goroutine behind when the writer keeps the pipe open without sending data.
func readWizardInput(input *os.File, timeout time.Duration) ([]byte, error) {
	if input == nil {
		return nil, fmt.Errorf("stdin is nil")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("stdin read timeout must be positive")
	}
	if err := input.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		if errors.Is(err, os.ErrNoDeadline) {
			// Some platforms cannot set deadlines on exec-inherited pipes
			// (macOS returns ErrNoDeadline for os.Stdin in that case). Fall
			// back to a goroutine read bounded by the same timeout.
			return readWizardInputBlocking(input, timeout)
		}
		return nil, fmt.Errorf("set stdin read deadline: %w", err)
	}

	data, readErr := io.ReadAll(input)
	clearErr := input.SetReadDeadline(time.Time{})
	if readErr != nil {
		return nil, readErr
	}
	if clearErr != nil {
		return nil, fmt.Errorf("clear stdin read deadline: %w", clearErr)
	}
	return data, nil
}

// readWizardInputBlocking is the fallback for file types that do not support
// read deadlines. It reads on a goroutine bounded by the caller's timeout; on
// timeout the goroutine may linger blocked on stdin, which only happens when a
// writer keeps the pipe open without producing input (the case the timeout
// exists to report).
func readWizardInputBlocking(input *os.File, timeout time.Duration) ([]byte, error) {
	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(input)
		results <- readResult{data: data, err: err}
	}()
	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		return res.data, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("wizard stdin probe timed out: %w", os.ErrDeadlineExceeded)
	}
}

// offerSavedConfig asks if the user wants to use previously saved settings
func (w *Wizard) offerSavedConfig(saved *WizardConfig) (bool, error) {
	fmt.Println("Found previous deployment configuration:")
	fmt.Println("────────────────────────────────────────")

	switch saved.DeployTarget {
	case "github":
		fmt.Printf("  Target:     GitHub Pages\n")
		fmt.Printf("  Repository: %s\n", saved.RepoName)
		if saved.Title != "" {
			fmt.Printf("  Title:      %s\n", saved.Title)
		}
	case "cloudflare":
		fmt.Printf("  Target:  Cloudflare Pages\n")
		fmt.Printf("  Project: %s\n", saved.CloudflareProject)
		if saved.Title != "" {
			fmt.Printf("  Title:   %s\n", saved.Title)
		}
	case "local":
		fmt.Printf("  Target: Local export\n")
		fmt.Printf("  Path:   %s\n", saved.OutputPath)
	}
	fmt.Println("")

	var useSaved bool = true
	form := w.newForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Update existing deployment with these settings?").
				Description("Select No to configure a new deployment").
				Value(&useSaved).
				Affirmative("Yes, update").
				Negative("No, reconfigure"),
		),
	)

	if err := form.Run(); err != nil {
		return false, err
	}

	fmt.Println("")
	return useSaved, nil
}

// Run executes the interactive wizard flow.
func (w *Wizard) Run() (*WizardResult, error) {
	// Always print banner first (tests and users expect to see it)
	w.printBanner()

	// Early exit if stdin is not a terminal and is empty/closed.
	if !isTerminal() {
		stat, err := os.Stdin.Stat()
		if err == nil {
			mode := stat.Mode()
			// For regular files, check if empty
			if mode.IsRegular() && stat.Size() == 0 {
				return nil, fmt.Errorf("wizard requires interactive input; stdin is empty")
			}
			// For pipes, check for EOF without waiting forever for a writer that
			// keeps the pipe open but has not produced any input.
			isPipe := (mode & os.ModeCharDevice) == 0
			if isPipe && !mode.IsRegular() {
				// A read deadline makes the EOF probe synchronous and cancellable. The
				// previous goroutine-based probe survived its timeout and kept consuming
				// stdin after Run returned.
				data, err := readWizardInput(os.Stdin, 100*time.Millisecond)
				if err != nil {
					if errors.Is(err, os.ErrDeadlineExceeded) {
						return nil, fmt.Errorf("wizard requires interactive input; stdin did not provide data within timeout")
					}
					return nil, fmt.Errorf("wizard requires interactive input; stdin error: %w", err)
				}
				if len(data) == 0 {
					return nil, fmt.Errorf("wizard requires interactive input; stdin is closed")
				}
				// Every wizard form shares this finite reader. Keeping it on the Wizard
				// avoids a pipe-copy goroutine and avoids mutating process-global stdin.
				w.input = &wizardInputReader{data: data}
			}
		}
	}

	// Check for saved configuration first (unless BV_NO_SAVED_CONFIG is set)
	savedConfig, err := LoadWizardConfig()
	skipSaved := env.NoSavedConfig.Get() != ""
	if !skipSaved && err == nil && savedConfig != nil && savedConfig.DeployTarget != "" {
		// Found saved config - ask if user wants to use it
		useSaved, err := w.offerSavedConfig(savedConfig)
		if err != nil {
			return nil, err
		}
		if useSaved {
			// Use saved config and mark as update
			w.config = savedConfig
			w.isUpdate = true

			// Skip to prerequisites check
			fmt.Println("Using saved configuration...")
			fmt.Println("")

			// Step 4: Prerequisites check
			if err := w.checkPrerequisites(); err != nil {
				return nil, err
			}

			return &WizardResult{
				DeployTarget: w.config.DeployTarget,
			}, nil
		}
	}

	// Step 1: Export configuration
	if err := w.collectExportOptions(); err != nil {
		return nil, err
	}

	// Step 2: Deployment target
	if err := w.collectDeployTarget(); err != nil {
		return nil, err
	}

	// Step 3: Target-specific configuration
	if err := w.collectTargetConfig(); err != nil {
		return nil, err
	}

	// Step 4: Prerequisites check
	if err := w.checkPrerequisites(); err != nil {
		return nil, err
	}

	// Step 5: Export bundle (handled externally by caller)
	// Return config for caller to perform export
	return &WizardResult{
		DeployTarget: w.config.DeployTarget,
	}, nil
}

// GetConfig returns the collected wizard configuration.
func (w *Wizard) GetConfig() *WizardConfig {
	return w.config
}

func (w *Wizard) printBanner() {
	fmt.Println("")
	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║           bv → Static Site Deployment Wizard                     ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════╣")
	fmt.Println("║  This wizard will:                                               ║")
	fmt.Println("║    1. Export your issues to a static HTML bundle                 ║")
	fmt.Println("║    2. Preview it locally                                         ║")
	fmt.Println("║    3. Deploy to GitHub Pages, Cloudflare Pages, or export only   ║")
	fmt.Println("║                                                                  ║")
	fmt.Println("║  Press Ctrl+C anytime to cancel                                  ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Println("")
}

func (w *Wizard) collectExportOptions() error {
	fmt.Println("Step 1: Export Configuration")
	fmt.Println("────────────────────────────")

	// Default title
	defaultTitle := "Project Issues"
	title := defaultTitle

	form := w.newForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Include closed issues?").
				Description("Export both open and closed issues").
				Value(&w.config.IncludeClosed),
			huh.NewConfirm().
				Title("Include git history?").
				Description("Export git commit history for each issue").
				Value(&w.config.IncludeHistory),
			huh.NewInput().
				Title("Site title").
				Value(&title).
				Placeholder(defaultTitle),
			huh.NewInput().
				Title("Site subtitle (optional)").
				Value(&w.config.Subtitle).
				Placeholder(""),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	if title != "" {
		w.config.Title = title
	} else {
		w.config.Title = defaultTitle
	}

	fmt.Println("")
	return nil
}

func (w *Wizard) collectDeployTarget() error {
	fmt.Println("Step 2: Deployment Target")
	fmt.Println("────────────────────────────")

	form := w.newForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Where do you want to deploy?").
				Options(
					huh.NewOption("GitHub Pages (create/update repository)", "github"),
					huh.NewOption("Cloudflare Pages (requires wrangler CLI)", "cloudflare"),
					huh.NewOption("Export locally only", "local"),
				).
				Value(&w.config.DeployTarget),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	fmt.Println("")
	return nil
}

func (w *Wizard) collectTargetConfig() error {
	switch w.config.DeployTarget {
	case "github":
		return w.collectGitHubConfig()
	case "cloudflare":
		return w.collectCloudflareConfig()
	case "local":
		return w.collectLocalConfig()
	}
	return nil
}

func (w *Wizard) collectGitHubConfig() error {
	fmt.Println("Step 3: GitHub Configuration")
	fmt.Println("────────────────────────────")

	// Suggest repo name based on current directory
	cwd, _ := os.Getwd()
	base := filepath.Base(cwd)
	if base == "." || base == "/" {
		base = "beads-viewer-pages"
	}
	suggestedName := base + "-pages"
	repoName := suggestedName
	description := "Issue tracker dashboard"

	form := w.newForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Repository name").
				Value(&repoName).
				Placeholder(suggestedName),
			huh.NewConfirm().
				Title("Make repository private?").
				Value(&w.config.RepoPrivate),
			huh.NewInput().
				Title("Repository description (optional)").
				Value(&description).
				Placeholder("Issue tracker dashboard"),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	if repoName != "" {
		w.config.RepoName = repoName
	} else {
		w.config.RepoName = suggestedName
	}
	w.config.RepoDescription = description

	fmt.Println("")
	return nil
}

func (w *Wizard) collectCloudflareConfig() error {
	fmt.Println("Step 3: Cloudflare Pages Configuration")
	fmt.Println("────────────────────────────")

	// Suggest project name based on bundle path or current directory
	suggestedName := SuggestProjectName(w.beadsPath)
	if suggestedName == "" {
		cwd, _ := os.Getwd()
		base := filepath.Base(cwd)
		if base == "." || base == "/" {
			base = "beads-viewer-pages"
		}
		suggestedName = base + "-pages"
	}

	projectName := suggestedName
	branch := "main"

	form := w.newForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Cloudflare Pages project name").
				Value(&projectName).
				Placeholder(suggestedName),
			huh.NewInput().
				Title("Branch name").
				Value(&branch).
				Placeholder("main"),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	if projectName != "" {
		w.config.CloudflareProject = projectName
	} else {
		w.config.CloudflareProject = suggestedName
	}
	if branch != "" {
		w.config.CloudflareBranch = branch
	} else {
		w.config.CloudflareBranch = "main"
	}

	fmt.Println("")
	return nil
}

func (w *Wizard) collectLocalConfig() error {
	fmt.Println("Step 3: Local Export Configuration")
	fmt.Println("────────────────────────────")

	// Default output path
	defaultPath := "./bv-pages"
	outputPath := defaultPath

	form := w.newForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Output directory").
				Value(&outputPath).
				Placeholder(defaultPath),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	if outputPath != "" {
		w.config.OutputPath = outputPath
	} else {
		w.config.OutputPath = defaultPath
	}

	fmt.Println("")
	return nil
}

func (w *Wizard) checkPrerequisites() error {
	fmt.Println("Step 4: Prerequisites Check")
	fmt.Println("────────────────────────────")

	switch w.config.DeployTarget {
	case "github":
		status, err := CheckGHStatus()
		if err != nil {
			return fmt.Errorf("failed to check GitHub status: %w", err)
		}

		// Check gh CLI
		if !status.Installed {
			fmt.Println("✗ gh CLI not installed")
			ShowInstallInstructions()
			return fmt.Errorf("gh CLI is required for GitHub Pages deployment")
		}
		fmt.Println("✓ gh CLI installed")

		// Check authentication
		if !status.Authenticated {
			fmt.Println("✗ gh CLI not authenticated")
			fmt.Println("")

			var doAuth bool
			form := w.newForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Would you like to authenticate now?").
						Value(&doAuth).
						Affirmative("Yes").
						Negative("No"),
				),
			)

			if err := form.Run(); err != nil {
				return err
			}

			if doAuth {
				if err := AuthenticateGH(); err != nil {
					return fmt.Errorf("authentication failed: %w", err)
				}
				// Re-check
				status, _ = CheckGHStatus()
				if !status.Authenticated {
					return fmt.Errorf("authentication failed")
				}
			} else {
				return fmt.Errorf("GitHub authentication required")
			}
		}
		fmt.Printf("✓ Authenticated as %s\n", status.Username)

		// Check git config
		if !status.GitConfigured {
			fmt.Println("✗ Git identity not configured")
			fmt.Println("  Please run:")
			fmt.Println("    git config --global user.name \"Your Name\"")
			fmt.Println("    git config --global user.email \"your@email.com\"")
			return fmt.Errorf("git identity not configured")
		}
		fmt.Printf("✓ Git configured (%s <%s>)\n", status.GitName, status.GitEmail)

	case "cloudflare":
		status, err := CheckWranglerStatus()
		if err != nil {
			return fmt.Errorf("failed to check wrangler status: %w", err)
		}

		// Check wrangler CLI
		if !status.Installed {
			fmt.Println("✗ wrangler CLI not installed")
			if !status.NPMInstalled {
				fmt.Println("  npm is required to install wrangler")
				fmt.Println("  Download Node.js from: https://nodejs.org/")
				return fmt.Errorf("npm is required to install wrangler CLI")
			}
			ShowWranglerInstallInstructions()

			var doInstall bool
			form := w.newForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Would you like to install wrangler now?").
						Value(&doInstall).
						Affirmative("Yes").
						Negative("No"),
				),
			)

			if err := form.Run(); err != nil {
				return err
			}

			if doInstall {
				if err := AttemptWranglerInstall(); err != nil {
					return fmt.Errorf("wrangler installation failed: %w", err)
				}
				// Re-check
				status, _ = CheckWranglerStatus()
				if !status.Installed {
					return fmt.Errorf("wrangler installation failed")
				}
			} else {
				return fmt.Errorf("wrangler CLI is required for Cloudflare Pages deployment")
			}
		}
		fmt.Println("✓ wrangler CLI installed")

		// Check authentication
		if !status.Authenticated {
			fmt.Println("✗ wrangler not authenticated")
			fmt.Println("")

			var doAuth bool
			form := w.newForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Would you like to authenticate now?").
						Value(&doAuth).
						Affirmative("Yes").
						Negative("No"),
				),
			)

			if err := form.Run(); err != nil {
				return err
			}

			if doAuth {
				if err := AuthenticateWrangler(); err != nil {
					return fmt.Errorf("authentication failed: %w", err)
				}
				// Re-check
				status, _ = CheckWranglerStatus()
				if !status.Authenticated {
					return fmt.Errorf("authentication failed")
				}
			} else {
				return fmt.Errorf("cloudflare authentication required")
			}
		}
		if status.AccountName != "" {
			fmt.Printf("✓ Authenticated (%s)\n", status.AccountName)
		} else {
			fmt.Println("✓ Authenticated with Cloudflare")
		}
	}

	fmt.Println("")
	return nil
}

// PerformExport creates the static site bundle.
// This is called by the main CLI after collecting issues.
func (w *Wizard) PerformExport(bundlePath string) error {
	w.bundlePath = bundlePath

	fmt.Println("Step 5: Export")
	fmt.Println("────────────────────────────")
	// Export logic is handled by caller
	return nil
}

// OfferPreview asks if user wants to preview and handles the preview flow.
func (w *Wizard) OfferPreview() (string, error) {
	fmt.Println("Step 6: Preview")
	fmt.Println("────────────────────────────")

	var doPreview bool = true
	form := w.newForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Preview the site before deploying?").
				Value(&doPreview).
				Affirmative("Yes").
				Negative("No"),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}

	if !doPreview {
		return "deploy", nil
	}

	fmt.Println("")
	fmt.Printf("Starting preview server for %s...\n", w.bundlePath)
	fmt.Println("Use the prompt below when done previewing.")
	fmt.Println("")

	if _, err := validatePreviewBundle(w.bundlePath); err != nil {
		return "", fmt.Errorf("preview bundle is not valid: %w", err)
	}

	// Start preview server
	port, err := FindAvailablePort(PreviewPortRangeStart, PreviewPortRangeEnd)
	if err != nil {
		return "", fmt.Errorf("could not find available port: %w", err)
	}

	server := NewPreviewServer(w.bundlePath, port)

	// Start server in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
		}
	}()

	select {
	case err := <-errChan:
		return "", fmt.Errorf("start preview server: %w", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Wait for user to press enter with a simple huh form
	var cont bool = true // Default to continue after preview
	waitForm := w.newForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Done previewing?").
				Description("Press Enter to continue with deployment").
				Value(&cont).
				Affirmative("Continue").
				Negative("Cancel"),
		),
	)

	if err := waitForm.Run(); err != nil {
		server.Stop()
		return "", err
	}

	// Stop server
	server.Stop()

	if !cont {
		return "cancel", nil
	}

	fmt.Println("")
	return "deploy", nil
}

// PerformDeploy deploys the bundle to the configured target.
func (w *Wizard) PerformDeploy() (*WizardResult, error) {
	return w.PerformDeployWithIssueCount(skipDeploymentIssueCountVerification)
}

// PerformDeployWithIssueCount deploys the bundle and verifies the expected issue count.
// Pass a negative value to skip live count verification.
func (w *Wizard) PerformDeployWithIssueCount(expectedIssueCount int) (*WizardResult, error) {
	fmt.Println("Step 7: Deploy")
	fmt.Println("────────────────────────────")

	result := &WizardResult{
		BundlePath:   w.bundlePath,
		DeployTarget: w.config.DeployTarget,
	}

	switch w.config.DeployTarget {
	case "github":
		// Note: GitHub Actions workflow is already added by CopyEmbeddedAssets
		// during the export phase, so we don't need to add it again here.

		deployConfig := GitHubDeployConfig{
			RepoName:         w.config.RepoName,
			Private:          w.config.RepoPrivate,
			Description:      w.config.RepoDescription,
			BundlePath:       w.bundlePath,
			SkipConfirmation: true,       // Already confirmed in wizard prerequisites
			ForceOverwrite:   w.isUpdate, // Auto-overwrite when updating existing deployment
		}

		// Use enhanced deployment with fallback
		deployResult, err := DeployToGitHubPagesWithFallback(deployConfig, expectedIssueCount)
		if err != nil {
			// If GitHub deployment fails, offer Cloudflare as fallback
			fmt.Printf("\n⚠ GitHub Pages deployment failed: %v\n", err)
			fmt.Println("Would you like to try Cloudflare Pages instead?")

			var tryCloudflare bool
			form := w.newForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title("Try Cloudflare Pages as fallback?").
						Value(&tryCloudflare).
						Affirmative("Yes").
						Negative("No"),
				),
			)
			if formErr := form.Run(); formErr == nil && tryCloudflare {
				// Switch to Cloudflare
				cfConfig := CloudflareDeployConfig{
					ProjectName:      SuggestProjectName(w.bundlePath),
					BundlePath:       w.bundlePath,
					Branch:           "main",
					SkipConfirmation: true,
				}
				cfResult, cfErr := DeployToCloudflareWithAutoCreate(cfConfig, expectedIssueCount)
				if cfErr != nil {
					return nil, fmt.Errorf("cloudflare fallback also failed: %w", cfErr)
				}
				result.CloudflareProject = cfResult.ProjectName
				result.CloudflareURL = cfResult.URL
				result.PagesURL = cfResult.URL
				result.DeployTarget = "cloudflare"
				return result, nil
			}
			return nil, fmt.Errorf("deployment failed: %w", err)
		}

		result.RepoFullName = deployResult.RepoFullName
		result.PagesURL = deployResult.PagesURL

	case "cloudflare":
		// Use enhanced deployment with auto-create
		deployConfig := CloudflareDeployConfig{
			ProjectName:      w.config.CloudflareProject,
			BundlePath:       w.bundlePath,
			Branch:           w.config.CloudflareBranch,
			SkipConfirmation: true, // Already confirmed in prerequisites
		}

		deployResult, err := DeployToCloudflareWithAutoCreate(deployConfig, expectedIssueCount)
		if err != nil {
			return nil, fmt.Errorf("deployment failed: %w", err)
		}

		result.CloudflareProject = deployResult.ProjectName
		result.CloudflareURL = deployResult.URL
		result.PagesURL = deployResult.URL

	case "local":
		fmt.Printf("Bundle exported to: %s\n", w.bundlePath)
		result.BundlePath = w.bundlePath
	}

	return result, nil
}

// PrintSuccess prints the success message after deployment.
func (w *Wizard) PrintSuccess(result *WizardResult) {
	// Build content lines first to calculate required width
	var lines []string
	lines = append(lines, "Deployment Complete!")

	switch result.DeployTarget {
	case "github":
		lines = append(lines, "Repository: https://github.com/"+result.RepoFullName)
		lines = append(lines, "Live site:  "+result.PagesURL)
		lines = append(lines, "")
		lines = append(lines, "Note: GitHub Pages may take 1-2 minutes to become available")
	case "cloudflare":
		lines = append(lines, "Project:    "+result.CloudflareProject)
		lines = append(lines, "Live site:  "+result.CloudflareURL)
		lines = append(lines, "")
		lines = append(lines, "Cloudflare Pages deploys are typically available immediately")
	case "local":
		lines = append(lines, "Bundle: "+result.BundlePath)
		lines = append(lines, "")
		lines = append(lines, "To preview:")
		lines = append(lines, "  bv --preview-pages "+result.BundlePath)
	}

	// Calculate width: max line length + 4 (for "║  " prefix and " ║" suffix)
	width := 0
	for _, line := range lines {
		if len(line) > width {
			width = len(line)
		}
	}
	width += 4 // Add padding for borders

	// Minimum width for aesthetics
	if width < 50 {
		width = 50
	}

	// Print box
	fmt.Println("")
	fmt.Print("╔")
	for i := 0; i < width; i++ {
		fmt.Print("═")
	}
	fmt.Println("╗")

	// Title line (centered)
	title := lines[0]
	padding := (width - len(title)) / 2
	fmt.Printf("║%s%s%s║\n", strings.Repeat(" ", padding), title, strings.Repeat(" ", width-padding-len(title)))

	fmt.Print("╠")
	for i := 0; i < width; i++ {
		fmt.Print("═")
	}
	fmt.Println("╣")

	// Content lines (left-aligned with 2-space indent)
	for _, line := range lines[1:] {
		fmt.Printf("║  %-*s ║\n", width-3, line)
	}

	fmt.Print("╚")
	for i := 0; i < width; i++ {
		fmt.Print("═")
	}
	fmt.Println("╝")
	fmt.Println("")
}

// WizardConfigPath returns the path to the wizard config file.
func WizardConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "bv", "pages-wizard.json")
}

// LoadWizardConfig loads previously saved wizard configuration.
func LoadWizardConfig() (*WizardConfig, error) {
	path := WizardConfigPath()
	if path == "" {
		return nil, fmt.Errorf("could not determine config path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No saved config
		}
		return nil, err
	}

	var config WizardConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveWizardConfig saves wizard configuration for future runs.
func SaveWizardConfig(config *WizardConfig) error {
	if config == nil {
		return errors.New("wizard config is nil")
	}

	path := WizardConfigPath()
	if path == "" {
		return fmt.Errorf("could not determine config path")
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create wizard config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wizard config: %w", err)
	}

	return writeWizardConfigFile(path, data)
}

func writeWizardConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary wizard config: %w", err)
	}

	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temporary wizard config: %w", err)
	}
	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temporary wizard config: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temporary wizard config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary wizard config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace wizard config: %w", err)
	}

	cleanup = false
	return nil
}
