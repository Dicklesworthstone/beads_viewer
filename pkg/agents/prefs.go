package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
)

// AgentPromptPreference stores per-project preference for AGENTS.md prompts.
type AgentPromptPreference struct {
	// ProjectPath is the absolute path to the project directory
	ProjectPath string `json:"project_path"`

	// DontAskAgain if true, never prompt for this project again
	DontAskAgain bool `json:"dont_ask_again"`

	// DeclinedAt is when the user declined the prompt (if applicable)
	DeclinedAt time.Time `json:"declined_at,omitempty"`

	// BlurbVersionOffered is the version we offered when declined
	BlurbVersionOffered int `json:"blurb_version_offered,omitempty"`

	// BlurbVersionAdded is the version that was added (if accepted)
	BlurbVersionAdded int `json:"blurb_version_added,omitempty"`

	// AddedAt is when the blurb was added (if accepted)
	AddedAt time.Time `json:"added_at,omitempty"`
}

// SavedConfigDisabled reports whether BV_NO_SAVED_CONFIG is set to any
// non-empty value, disabling reads and writes of saved agent prompt preferences.
func SavedConfigDisabled() bool {
	return strings.TrimSpace(env.NoSavedConfig.Get()) != ""
}

func savedConfigDisabled() bool {
	return SavedConfigDisabled()
}

// getPrefsDir returns the directory for storing agent prompt preferences:
// $XDG_CONFIG_HOME/bv/agent-prompts when XDG_CONFIG_HOME is set, otherwise
// ~/.config/bv/agent-prompts on Unix (matching pkg/updater and README docs)
// or os.UserConfigDir()/bv/agent-prompts on Windows.
func getPrefsDir() (string, error) {
	if xdg := strings.TrimSpace(env.Get("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "bv", "agent-prompts"), nil
	}
	if runtime.GOOS == "windows" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(configDir, "bv", "agent-prompts"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if homeDir == "" {
		return "", fmt.Errorf("resolving agent prompt config dir: empty home directory")
	}
	return filepath.Join(homeDir, ".config", "bv", "agent-prompts"), nil
}

func getLegacyPrefsPath(workDir string) string {
	if strings.TrimSpace(env.Get("XDG_CONFIG_HOME")) != "" {
		return ""
	}
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return ""
	}
	hash, err := projectHash(workDir)
	if err != nil || hash == "" {
		return ""
	}
	return filepath.Join(configDir, "bv", "agent-prompts", hash+".json")
}

// projectHash generates a consistent hash for a project directory.
// Uses SHA256 of the absolute path, truncated to 16 hex chars.
func projectHash(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != "" {
		abs = resolved
	}
	hash := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(hash[:8]), nil
}

// getPrefsPath returns the path to the preference file for a project.
func getPrefsPath(workDir string) (string, error) {
	prefsDir, err := getPrefsDir()
	if err != nil {
		return "", err
	}
	hash, err := projectHash(workDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(prefsDir, hash+".json"), nil
}

// LoadAgentPromptPreference loads the preference for a project.
// Returns nil if no preference exists (new project) or when BV_NO_SAVED_CONFIG is set.
func LoadAgentPromptPreference(workDir string) (*AgentPromptPreference, error) {
	if savedConfigDisabled() {
		return nil, nil
	}
	prefsPath, err := getPrefsPath(workDir)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(prefsPath)
	if err != nil {
		if os.IsNotExist(err) {
			legacyPath := getLegacyPrefsPath(workDir)
			if legacyPath == "" || legacyPath == prefsPath {
				return nil, nil // No preference exists
			}
			data, err = os.ReadFile(legacyPath)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, nil
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	var pref AgentPromptPreference
	if err := json.Unmarshal(data, &pref); err != nil {
		return nil, err
	}
	return &pref, nil
}

// SaveAgentPromptPreference saves the preference for a project.
func SaveAgentPromptPreference(workDir string, pref AgentPromptPreference) error {
	if savedConfigDisabled() {
		return nil
	}
	// Ensure project path is absolute
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return err
	}
	pref.ProjectPath = abs

	// Ensure prefs directory exists
	prefsDir, err := getPrefsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(prefsDir, 0755); err != nil {
		return err
	}

	// Marshal and write
	prefsPath, err := getPrefsPath(workDir)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(pref, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(prefsPath, data, 0644)
}

// ShouldPromptForAgentFile determines if we should show the AGENTS.md prompt.
// Returns true if:
// - No preference exists (new project)
// - User hasn't checked "don't ask again"
func ShouldPromptForAgentFile(workDir string) bool {
	if savedConfigDisabled() {
		return false
	}
	pref, err := LoadAgentPromptPreference(workDir)
	if err != nil {
		return true // Error loading = treat as new project
	}

	if pref == nil {
		return true // New project, never prompted
	}

	if pref.DontAskAgain {
		return false // Respect "don't ask again"
	}

	if pref.BlurbVersionAdded > 0 {
		return false // Already added blurb
	}

	// They previously declined but didn't check "don't ask again"
	// We could prompt again, but let's be respectful and not spam
	if !pref.DeclinedAt.IsZero() {
		return false
	}

	return true
}

// ShouldPromptForAgentUpgrade determines if we should offer to upgrade an
// existing outdated AGENTS.md / CLAUDE.md blurb to targetVersion.
// Unlike ShouldPromptForAgentFile, a prior acceptance of an older blurb version
// does not suppress upgrade prompts, unless the user selected "don't ask again"
// or already declined/accepted targetVersion (or newer).
func ShouldPromptForAgentUpgrade(workDir string, targetVersion int) bool {
	if savedConfigDisabled() {
		return false
	}
	pref, err := LoadAgentPromptPreference(workDir)
	if err != nil {
		return true
	}
	if pref == nil {
		return true
	}
	if pref.DontAskAgain {
		return false
	}
	if pref.BlurbVersionAdded >= targetVersion {
		return false
	}
	if !pref.DeclinedAt.IsZero() && pref.BlurbVersionOffered >= targetVersion {
		return false
	}
	return true
}

// RecordDecline records that the user declined the AGENTS.md prompt.
func RecordDecline(workDir string, dontAskAgain bool) error {
	pref := AgentPromptPreference{
		DontAskAgain:        dontAskAgain,
		DeclinedAt:          time.Now(),
		BlurbVersionOffered: BlurbVersion,
	}
	return SaveAgentPromptPreference(workDir, pref)
}

// RecordAccept records that the user accepted and blurb was added.
func RecordAccept(workDir string) error {
	pref := AgentPromptPreference{
		BlurbVersionAdded: BlurbVersion,
		AddedAt:           time.Now(),
	}
	return SaveAgentPromptPreference(workDir, pref)
}

// ClearPreference removes the preference for a project.
// Useful for testing or if user wants to be prompted again.
func ClearPreference(workDir string) error {
	if savedConfigDisabled() {
		return nil
	}
	prefsPath, err := getPrefsPath(workDir)
	if err != nil {
		return err
	}
	if err := os.Remove(prefsPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if legacyPath := getLegacyPrefsPath(workDir); legacyPath != "" && legacyPath != prefsPath {
		if err := os.Remove(legacyPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
