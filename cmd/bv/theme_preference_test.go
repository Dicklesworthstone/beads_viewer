package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalTheme(t *testing.T) {
	cases := map[string]string{
		"light":     "light",
		"LIGHT":     "light",
		" light\t":  "light",
		"dark":      "dark",
		"Dark":      "dark",
		"auto":      "auto",
		"AUTO ":     "auto",
		"":          "",
		"   ":       "",
		"solarized": "",
		"darkish":   "",
	}
	for in, want := range cases {
		if got := canonicalTheme(in); got != want {
			t.Errorf("canonicalTheme(%q) = %q, want %q", in, got, want)
		}
	}
}

// withThemeConfig points HOME at a fresh temp dir containing
// ~/.config/bv/config.yaml with the given body, isolates XDG_CONFIG_HOME and
// BV_NO_SAVED_CONFIG from ambient harness variables, and returns that HOME.
func withThemeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "bv")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	return home
}

func TestLoadThemeFromUserConfig(t *testing.T) {
	// theme: key present.
	withThemeConfig(t, "theme: light\n")
	if v, ok := loadThemeFromUserConfig(); !ok || v != "light" {
		t.Fatalf("loadThemeFromUserConfig() = (%q, %v), want (light, true)", v, ok)
	}

	// Coexists with unrelated keys.
	withThemeConfig(t, "experimental:\n  background_mode: true\ntheme: dark\n")
	if v, ok := loadThemeFromUserConfig(); !ok || v != "dark" {
		t.Fatalf("with other keys: loadThemeFromUserConfig() = (%q, %v), want (dark, true)", v, ok)
	}

	// Key absent.
	withThemeConfig(t, "experimental:\n  background_mode: true\n")
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("key absent: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}

	// Key present but blank.
	withThemeConfig(t, "theme: \"\"\n")
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("blank value: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}

	// Malformed YAML.
	withThemeConfig(t, "theme: [unterminated\n")
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("malformed yaml: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}

	// No config file at all.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("no file: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}
}

func TestLoadBackgroundModeFromUserConfig(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		want  bool
		found bool
	}{
		{name: "enabled", body: "experimental:\n  background_mode: true\n", want: true, found: true},
		{name: "disabled", body: "experimental:\n  background_mode: false\n", want: false, found: true},
		{name: "missing key", body: "theme: dark\n", found: false},
		{name: "malformed yaml", body: "experimental: [unterminated\n", found: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withThemeConfig(t, tt.body)
			got, found := loadBackgroundModeFromUserConfig()
			if got != tt.want || found != tt.found {
				t.Fatalf("loadBackgroundModeFromUserConfig() = (%v, %v), want (%v, %v)",
					got, found, tt.want, tt.found)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("BV_NO_SAVED_CONFIG", "")
		if got, found := loadBackgroundModeFromUserConfig(); got || found {
			t.Fatalf("loadBackgroundModeFromUserConfig() = (%v, %v), want (false, false)", got, found)
		}
	})
}

func TestEffectiveThemePreference(t *testing.T) {
	// Explicit flag beats env and config.
	withThemeConfig(t, "theme: dark\n")
	t.Setenv("BV_THEME", "dark")
	if got := effectiveThemePreference("light", true, nil); got != "light" {
		t.Errorf("flag over env+config: got %q, want light", got)
	}

	// Explicit-but-invalid flag warns and resolves to auto (no fall-through).
	var warn bytes.Buffer
	if got := effectiveThemePreference("chartreuse", true, &warn); got != "auto" {
		t.Errorf("invalid explicit flag: got %q, want auto", got)
	}
	if !strings.Contains(warn.String(), "chartreuse") {
		t.Errorf("invalid explicit flag: warning missing value, got %q", warn.String())
	}

	// Flag unset: env beats config.
	withThemeConfig(t, "theme: light\n")
	t.Setenv("BV_THEME", "dark")
	if got := effectiveThemePreference("", false, nil); got != "dark" {
		t.Errorf("env over config: got %q, want dark", got)
	}

	// Flag and env unset: config wins.
	withThemeConfig(t, "theme: light\n")
	t.Setenv("BV_THEME", "")
	if got := effectiveThemePreference("", false, nil); got != "light" {
		t.Errorf("config: got %q, want light", got)
	}

	// Invalid env falls through to config.
	withThemeConfig(t, "theme: dark\n")
	t.Setenv("BV_THEME", "banana")
	if got := effectiveThemePreference("", false, nil); got != "dark" {
		t.Errorf("invalid env falls through to config: got %q, want dark", got)
	}

	// Invalid config value falls through to auto-detect.
	withThemeConfig(t, "theme: mauve\n")
	t.Setenv("BV_THEME", "")
	if got := effectiveThemePreference("", false, nil); got != "" {
		t.Errorf("invalid config: got %q, want empty (auto-detect)", got)
	}

	// Nothing anywhere: empty (auto-detect).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BV_NO_SAVED_CONFIG", "")
	t.Setenv("BV_THEME", "")
	if got := effectiveThemePreference("", false, nil); got != "" {
		t.Errorf("no source: got %q, want empty (auto-detect)", got)
	}
}
