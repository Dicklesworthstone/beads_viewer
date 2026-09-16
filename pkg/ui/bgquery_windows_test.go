//go:build windows

package ui

import "testing"

func TestParseOSCBackgroundIsDark(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		wantDark bool
		wantOK   bool
	}{
		{"dark, BEL terminated", "\x1b]11;rgb:1e1e/1e1e/1e1e\a", true, true},
		{"light, ST terminated", "\x1b]11;rgb:fdf6/e3e3/cece\x1b\\", false, true},
		{"short 2-digit channels", "\x1b]11;rgb:00/00/00\a", true, true},
		{"not an OSC reply", "not an OSC reply", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isDark, ok := parseOSCBackgroundIsDark(tc.reply)
			if ok != tc.wantOK {
				t.Fatalf("parseOSCBackgroundIsDark(%q): ok = %v, want %v", tc.reply, ok, tc.wantOK)
			}
			if ok && isDark != tc.wantDark {
				t.Fatalf("parseOSCBackgroundIsDark(%q): isDark = %v, want %v", tc.reply, isDark, tc.wantDark)
			}
		})
	}
}

func TestPlatformHasDarkBackgroundNoWindowsTerminal(t *testing.T) {
	t.Setenv("WT_SESSION", "")

	isDark, ok := platformHasDarkBackground()

	if ok {
		t.Fatalf("platformHasDarkBackground() without WT_SESSION: ok = true, want false")
	}
	if isDark {
		t.Fatalf("platformHasDarkBackground() without WT_SESSION: isDark = true, want false")
	}
}
