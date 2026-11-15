package agents

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProjectHash(t *testing.T) {
	// Same path should always produce same hash
	hash1, err := projectHash("/test/path")
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := projectHash("/test/path")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Errorf("Same path produced different hashes: %s vs %s", hash1, hash2)
	}

	// Different paths should produce different hashes
	hash3, err := projectHash("/other/path")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 == hash3 {
		t.Errorf("Different paths produced same hash: %s", hash1)
	}

	// Hash should be 16 chars (8 bytes hex)
	if len(hash1) != 16 {
		t.Errorf("Expected hash length 16, got %d", len(hash1))
	}
}

func isolateAgentPrefsConfig(t *testing.T) string {
	t.Helper()
	homeDir := t.TempDir()
	xdgDir := filepath.Join(homeDir, ".config")
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("APPDATA", xdgDir)
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	return homeDir
}

func TestSaveAndLoadPreference(t *testing.T) {
	isolateAgentPrefsConfig(t)
	// Use temp dir as fake project
	tmpDir := t.TempDir()

	// Initially no preference should exist
	pref, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref != nil {
		t.Error("Expected nil for new project, got preference")
	}

	// Save a preference
	now := time.Now()
	testPref := AgentPromptPreference{
		DontAskAgain:        true,
		DeclinedAt:          now,
		BlurbVersionOffered: 1,
	}
	err = SaveAgentPromptPreference(tmpDir, testPref)
	if err != nil {
		t.Fatal(err)
	}

	// Load it back
	loaded, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("Expected loaded preference, got nil")
	}
	if !loaded.DontAskAgain {
		t.Error("Expected DontAskAgain=true")
	}
	if loaded.BlurbVersionOffered != 1 {
		t.Errorf("Expected BlurbVersionOffered=1, got %d", loaded.BlurbVersionOffered)
	}
	// ProjectPath should be populated with absolute path
	if loaded.ProjectPath == "" {
		t.Error("Expected ProjectPath to be set")
	}
}

func TestShouldPromptForAgentFile(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(string) error
		expected bool
	}{
		{
			name:     "new project (no prefs)",
			setup:    func(string) error { return nil },
			expected: true,
		},
		{
			name: "declined with dont ask again",
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					DontAskAgain: true,
					DeclinedAt:   time.Now(),
				})
			},
			expected: false,
		},
		{
			name: "declined without dont ask again",
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					DontAskAgain: false,
					DeclinedAt:   time.Now(),
				})
			},
			expected: false, // We respect the decline
		},
		{
			name: "already added blurb",
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					BlurbVersionAdded: 1,
					AddedAt:           time.Now(),
				})
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAgentPrefsConfig(t)
			tmpDir := t.TempDir()
			if err := tt.setup(tmpDir); err != nil {
				t.Fatal(err)
			}

			result := ShouldPromptForAgentFile(tmpDir)
			if result != tt.expected {
				t.Errorf("ShouldPromptForAgentFile() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestRecordDecline(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Record decline with "don't ask again"
	err := RecordDecline(tmpDir, true)
	if err != nil {
		t.Fatal(err)
	}

	pref, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref == nil {
		t.Fatal("Expected preference after decline")
	}
	if !pref.DontAskAgain {
		t.Error("Expected DontAskAgain=true")
	}
	if pref.DeclinedAt.IsZero() {
		t.Error("Expected DeclinedAt to be set")
	}
	if pref.BlurbVersionOffered != BlurbVersion {
		t.Errorf("Expected BlurbVersionOffered=%d, got %d", BlurbVersion, pref.BlurbVersionOffered)
	}
}

func TestRecordAccept(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	err := RecordAccept(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	pref, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref == nil {
		t.Fatal("Expected preference after accept")
	}
	if pref.BlurbVersionAdded != BlurbVersion {
		t.Errorf("Expected BlurbVersionAdded=%d, got %d", BlurbVersion, pref.BlurbVersionAdded)
	}
	if pref.AddedAt.IsZero() {
		t.Error("Expected AddedAt to be set")
	}
}

func TestClearPreference(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// Save then clear
	err := RecordDecline(tmpDir, true)
	if err != nil {
		t.Fatal(err)
	}

	err = ClearPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Should be nil again
	pref, err := LoadAgentPromptPreference(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if pref != nil {
		t.Error("Expected nil after clear")
	}

	// Clearing non-existent should not error
	err = ClearPreference(tmpDir)
	if err != nil {
		t.Errorf("ClearPreference on missing should not error: %v", err)
	}
}

func TestGetPrefsDir(t *testing.T) {
	dir, err := getPrefsDir()
	if err != nil {
		t.Fatal(err)
	}

	// Should end with expected path
	if !filepath.IsAbs(dir) {
		t.Errorf("Expected absolute path, got %s", dir)
	}
	if !contains(dir, "bv") || !contains(dir, "agent-prompts") {
		t.Errorf("Unexpected prefs dir: %s", dir)
	}
}

func contains(s, substr string) bool {
	return filepath.Base(filepath.Dir(s)) == "bv" && filepath.Base(s) == "agent-prompts" ||
		len(s) > 0 && (s[len(s)-len(substr):] == substr || contains(s[:len(s)-1], substr))
}

func TestPrefsPathConsistency(t *testing.T) {
	// Same working dir should always produce same prefs path
	tmpDir := t.TempDir()

	path1, err := getPrefsPath(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	path2, err := getPrefsPath(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if path1 != path2 {
		t.Errorf("Same working dir produced different prefs paths: %s vs %s", path1, path2)
	}

	// Should end with .json
	if filepath.Ext(path1) != ".json" {
		t.Errorf("Expected .json extension, got %s", path1)
	}
}

func TestPrefsCreateDirectory(t *testing.T) {
	isolateAgentPrefsConfig(t)
	tmpDir := t.TempDir()

	// This should create the prefs directory
	err := RecordAccept(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Verify directory was created
	prefsDir, err := getPrefsDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prefsDir); os.IsNotExist(err) {
		t.Error("Expected prefs directory to be created")
	}
}

func TestShouldPromptForAgentUpgrade(t *testing.T) {
	isolateAgentPrefsConfig(t)

	tests := []struct {
		name          string
		targetVersion int
		setup         func(string) error
		expected      bool
	}{
		{
			name:          "new project with no saved prefs",
			targetVersion: BlurbVersion,
			setup:         func(string) error { return nil },
			expected:      true,
		},
		{
			name:          "previously accepted older blurb v1 allows upgrade prompt for current version",
			targetVersion: BlurbVersion,
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					BlurbVersionAdded: 1,
					AddedAt:           time.Now(),
				})
			},
			expected: true,
		},
		{
			name:          "already accepted target version suppresses prompt",
			targetVersion: BlurbVersion,
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					BlurbVersionAdded: BlurbVersion,
					AddedAt:           time.Now(),
				})
			},
			expected: false,
		},
		{
			name:          "dont ask again suppresses upgrade prompt",
			targetVersion: BlurbVersion,
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					DontAskAgain:        true,
					DeclinedAt:          time.Now(),
					BlurbVersionOffered: 1,
				})
			},
			expected: false,
		},
		{
			name:          "declined older version still prompts for newer version",
			targetVersion: BlurbVersion,
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					DeclinedAt:          time.Now(),
					BlurbVersionOffered: 2,
				})
			},
			expected: true,
		},
		{
			name:          "declined current target version suppresses repeat prompt",
			targetVersion: BlurbVersion,
			setup: func(dir string) error {
				return SaveAgentPromptPreference(dir, AgentPromptPreference{
					DeclinedAt:          time.Now(),
					BlurbVersionOffered: BlurbVersion,
				})
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := tt.setup(projectDir); err != nil {
				t.Fatal(err)
			}
			if got := ShouldPromptForAgentUpgrade(projectDir, tt.targetVersion); got != tt.expected {
				t.Errorf("ShouldPromptForAgentUpgrade(..., %d) = %v, want %v", tt.targetVersion, got, tt.expected)
			}
		})
	}
}

func TestAgentPrefs_NoSavedConfigAndXDGConfigHome(t *testing.T) {
	homeDir := t.TempDir()
	xdgDir := filepath.Join(t.TempDir(), "custom-xdg")
	projectDir := t.TempDir()

	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("BV_NO_SAVED_CONFIG", "1")

	if ShouldPromptForAgentFile(projectDir) {
		t.Fatal("ShouldPromptForAgentFile should return false when BV_NO_SAVED_CONFIG=1")
	}
	if ShouldPromptForAgentUpgrade(projectDir, BlurbVersion) {
		t.Fatal("ShouldPromptForAgentUpgrade should return false when BV_NO_SAVED_CONFIG=1")
	}
	pref, err := LoadAgentPromptPreference(projectDir)
	if err != nil {
		t.Fatalf("LoadAgentPromptPreference error: %v", err)
	}
	if pref != nil {
		t.Fatalf("expected nil preference when BV_NO_SAVED_CONFIG=1, got %+v", pref)
	}

	if err := SaveAgentPromptPreference(projectDir, AgentPromptPreference{DontAskAgain: true}); err != nil {
		t.Fatalf("SaveAgentPromptPreference should no-op cleanly under BV_NO_SAVED_CONFIG=1: %v", err)
	}
	if err := RecordDecline(projectDir, true); err != nil {
		t.Fatalf("RecordDecline should no-op cleanly under BV_NO_SAVED_CONFIG=1: %v", err)
	}
	if err := RecordAccept(projectDir); err != nil {
		t.Fatalf("RecordAccept should no-op cleanly under BV_NO_SAVED_CONFIG=1: %v", err)
	}
	if err := ClearPreference(projectDir); err != nil {
		t.Fatalf("ClearPreference should no-op cleanly under BV_NO_SAVED_CONFIG=1: %v", err)
	}
	if _, err := os.Stat(xdgDir); !os.IsNotExist(err) {
		t.Fatalf("expected zero disk writes to XDG_CONFIG_HOME under BV_NO_SAVED_CONFIG=1, stat err=%v", err)
	}

	// Any non-empty value (including "0" or "false") still disables saved config.
	for _, nonEmpty := range []string{"0", "false", "no", "off"} {
		t.Setenv("BV_NO_SAVED_CONFIG", nonEmpty)
		if !SavedConfigDisabled() {
			t.Fatalf("SavedConfigDisabled() = false for BV_NO_SAVED_CONFIG=%q, want true", nonEmpty)
		}
		if err := RecordAccept(projectDir); err != nil {
			t.Fatalf("RecordAccept under BV_NO_SAVED_CONFIG=%q: %v", nonEmpty, err)
		}
		if _, err := os.Stat(xdgDir); !os.IsNotExist(err) {
			t.Fatalf("expected zero disk writes under BV_NO_SAVED_CONFIG=%q, stat err=%v", nonEmpty, err)
		}
	}

	// Unset / empty BV_NO_SAVED_CONFIG re-enables saved config and routes to XDG_CONFIG_HOME.
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	if SavedConfigDisabled() {
		t.Fatal("SavedConfigDisabled() = true when BV_NO_SAVED_CONFIG is empty, want false")
	}
	if err := RecordAccept(projectDir); err != nil {
		t.Fatalf("RecordAccept with XDG_CONFIG_HOME: %v", err)
	}
	prefsPath, err := getPrefsPath(projectDir)
	if err != nil {
		t.Fatalf("getPrefsPath: %v", err)
	}
	wantXDGPrefix := filepath.Join(xdgDir, "bv", "agent-prompts")
	if filepath.Dir(prefsPath) != wantXDGPrefix {
		t.Fatalf("getPrefsPath dir = %q, want %q", filepath.Dir(prefsPath), wantXDGPrefix)
	}
	if _, err := os.Stat(prefsPath); err != nil {
		t.Fatalf("expected preference file at %s: %v", prefsPath, err)
	}

	// Even when a preference file exists on disk, BV_NO_SAVED_CONFIG=1 must ignore it and suppress prompts.
	t.Setenv("BV_NO_SAVED_CONFIG", "true")
	if got, err := LoadAgentPromptPreference(projectDir); err != nil || got != nil {
		t.Fatalf("expected nil preference when BV_NO_SAVED_CONFIG=true, got %+v (err=%v)", got, err)
	}
	if err := ClearPreference(projectDir); err != nil {
		t.Fatalf("ClearPreference under BV_NO_SAVED_CONFIG=true: %v", err)
	}
	if _, err := os.Stat(prefsPath); err != nil {
		t.Fatalf("ClearPreference must not delete existing file when BV_NO_SAVED_CONFIG=true: %v", err)
	}

	// Unset XDG_CONFIG_HOME on Unix -> ~/.config/bv/agent-prompts.
	if runtime.GOOS != "windows" {
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		dir, err := getPrefsDir()
		if err != nil {
			t.Fatalf("getPrefsDir with unset XDG_CONFIG_HOME: %v", err)
		}
		wantHomeConfig := filepath.Join(homeDir, ".config", "bv", "agent-prompts")
		if dir != wantHomeConfig {
			t.Fatalf("getPrefsDir() = %q, want %q", dir, wantHomeConfig)
		}
	}
}
