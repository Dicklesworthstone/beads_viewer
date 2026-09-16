package ui

import (
	"runtime"
	"testing"
)

// Issue #202: a light-themed Windows console rendered with the dark palette.
// termenv's Windows backgroundColor() is `return ANSIColor(0)`, so
// lipgloss.HasDarkBackground() answers "dark" for every Windows terminal and
// bv had no other signal. These cover the reply parsing, which is kept free of
// build tags so it is checked on every platform rather than only the one where
// the query can run.
func TestParseOSCBackgroundIsDark(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reply    string
		wantDark bool
		wantOK   bool
	}{
		{"dark, BEL terminated", "\x1b]11;rgb:1e1e/1e1e/1e1e\x07", true, true},
		{"solarized light, ST terminated", "\x1b]11;rgb:fdf6/e3e3/cece\x1b\\", false, true},
		{"solarized dark", "\x1b]11;rgb:0026/002b/0036\x1b\\", true, true},
		{"pure black, 2-digit channels", "\x1b]11;rgb:00/00/00\x07", true, true},
		{"pure white, 2-digit channels", "\x1b]11;rgb:ff/ff/ff\x07", false, true},
		{"single-digit channels", "\x1b]11;rgb:f/f/f\x07", false, true},
		{"green is weighted most heavily", "\x1b]11;rgb:0000/ffff/0000\x07", false, true},
		{"blue is weighted least", "\x1b]11;rgb:0000/0000/ffff\x07", true, true},
		{"no reply at all", "", false, false},
		{"unrelated text", "not an OSC reply", false, false},
		{"different OSC code", "\x1b]10;rgb:ffff/ffff/ffff\x07", false, false},
		{"truncated reply", "\x1b]11;rgb:1e1e/1e1e", false, false},
		{"non-hex channel", "\x1b]11;rgb:zzzz/1e1e/1e1e\x07", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isDark, ok := parseOSCBackgroundIsDark(tc.reply)
			if ok != tc.wantOK {
				t.Fatalf("parseOSCBackgroundIsDark(%q): ok = %v, want %v", tc.reply, ok, tc.wantOK)
			}
			if ok && isDark != tc.wantDark {
				t.Fatalf("parseOSCBackgroundIsDark(%q): isDark = %v, want %v",
					tc.reply, isDark, tc.wantDark)
			}
		})
	}
}

func TestHexChannelNormalisesAnyWidth(t *testing.T) {
	for _, tc := range []struct {
		digits string
		want   float64
	}{
		{"0", 0}, {"f", 1}, {"00", 0}, {"ff", 1}, {"0000", 0}, {"ffff", 1},
		{"", 0},      // no digits
		{"fffff", 0}, // wider than OSC 11 reports
		{"nope", 0},  // not hex
	} {
		t.Run(tc.digits, func(t *testing.T) {
			if got := hexChannel(tc.digits); got != tc.want {
				t.Fatalf("hexChannel(%q) = %v, want %v", tc.digits, got, tc.want)
			}
		})
	}
}

// Away from Windows the query must stay out of the way entirely, so termenv's
// own detection keeps deciding.
func TestPlatformDetectionDefersWhereTermenvAlreadyQueries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows build has its own probe")
	}
	if isDark, ok := platformHasDarkBackground(); ok || isDark {
		t.Fatalf("platformHasDarkBackground() = (%v, %v), want (false, false)", isDark, ok)
	}
}
