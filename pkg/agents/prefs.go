package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
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

// getPrefsDir returns the directory for storing agent prompt preferences.
func getPrefsDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "bv", "agent-prompts"), nil
}

// projectHash generates a consistent hash for a project directory.
// Uses SHA256 of the absolute path, truncated to 16 hex chars.
func projectHash(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
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
// Returns nil if no preference exists (new project).
func LoadAgentPromptPreference(workDir string) (*AgentPromptPreference, error) {
	prefsPath, err := getPrefsPath(workDir)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(prefsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No preference exists
		}
		return nil, err
	}

	var pref AgentPromptPreference
	if err := json.Unmarshal(data, &pref); err != nil {
		return nil, err
	}
	return &pref, nil
}

// SaveAgentPromptPreference saves the preference for a project.
func SaveAgentPromptPreference(workDir string, pref AgentPromptPreference) error {
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
	prefsPath, err := getPrefsPath(workDir)
	if err != nil {
		return err
	}
	err = os.Remove(prefsPath)
	if os.IsNotExist(err) {
		return nil // Already doesn't exist
	}
	return err
}
