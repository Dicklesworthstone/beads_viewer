package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeThemePreference(t *testing.T) {
	cases := map[string]string{
		"light":    "light",
		"LIGHT":    "light",
		"  dark ":  "dark",
		"Dark":     "dark",
		"Auto":     "auto",
		"":         "",
		"blue":     "",
		"lightish": "",
	}
	for in, want := range cases {
		if got := normalizeThemePreference(in); got != want {
			t.Errorf("normalizeThemePreference(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeThemeConfig writes a ~/.config/bv/config.yaml with the given body under a
// fresh temp HOME and returns that HOME directory.
func writeThemeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "bv")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return home
}

func TestLoadThemeFromUserConfig(t *testing.T) {
	// Present and non-empty.
	t.Setenv("HOME", writeThemeConfig(t, "theme: light\n"))
	if v, ok := loadThemeFromUserConfig(); !ok || v != "light" {
		t.Fatalf("loadThemeFromUserConfig() = (%q, %v), want (light, true)", v, ok)
	}

	// Key absent (other config present).
	t.Setenv("HOME", writeThemeConfig(t, "experimental:\n  background_mode: true\n"))
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("key absent: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}

	// No config file at all.
	t.Setenv("HOME", t.TempDir())
	if v, ok := loadThemeFromUserConfig(); ok {
		t.Errorf("file absent: loadThemeFromUserConfig() = (%q, %v), want (_, false)", v, ok)
	}
}

func TestResolveThemePreference(t *testing.T) {
	// Flag (explicitly set) wins over env and config.
	t.Setenv("HOME", writeThemeConfig(t, "theme: dark\n"))
	t.Setenv("BV_THEME", "dark")
	if got := resolveThemePreference("light", true); got != "light" {
		t.Errorf("flag should win over env/config: got %q, want light", got)
	}

	// Explicitly-set but invalid flag → auto (does not fall through to env/config).
	if got := resolveThemePreference("banana", true); got != "auto" {
		t.Errorf("invalid explicit flag: got %q, want auto", got)
	}

	// Flag unset → env wins over config.
	t.Setenv("HOME", writeThemeConfig(t, "theme: light\n"))
	t.Setenv("BV_THEME", "dark")
	if got := resolveThemePreference("", false); got != "dark" {
		t.Errorf("env should win over config: got %q, want dark", got)
	}

	// Flag unset, env unset → config is used.
	t.Setenv("HOME", writeThemeConfig(t, "theme: light\n"))
	t.Setenv("BV_THEME", "")
	if got := resolveThemePreference("", false); got != "light" {
		t.Errorf("config should be used: got %q, want light", got)
	}

	// Nothing specified anywhere → empty (auto-detect).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BV_THEME", "")
	if got := resolveThemePreference("", false); got != "" {
		t.Errorf("no source: got %q, want empty", got)
	}
}
