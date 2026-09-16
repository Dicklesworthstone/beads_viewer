package ui

import (
	"regexp"
	"strconv"
)

// termenv's background-colour query is a stub on Windows — its
// backgroundColor() returns ANSIColor(0) unconditionally (termenv v0.16.0,
// termenv_windows.go) — so lipgloss.HasDarkBackground() reports "dark" for
// every Windows terminal regardless of its actual theme. A light-themed
// console therefore renders with the dark palette until the user passes
// --theme=light (issue #202).
//
// platformHasDarkBackground, defined per platform, is bv's own detection for
// that gap. The reply parsing lives here, without a build tag, so it is
// exercised by the test suite on every platform rather than only where the
// query itself can run.

// oscBackgroundPattern matches an OSC 11 background-colour reply, e.g.
// "\x1b]11;rgb:1e1e/1e1e/1e1e". The terminator (BEL or ST) is not matched, so
// both spellings parse.
var oscBackgroundPattern = regexp.MustCompile(`\x1b\]11;rgb:([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})/([0-9a-fA-F]{1,4})`)

// parseOSCBackgroundIsDark reports whether an OSC 11 reply describes a dark
// background, and whether it was a reply at all.
func parseOSCBackgroundIsDark(reply string) (isDark bool, ok bool) {
	match := oscBackgroundPattern.FindStringSubmatch(reply)
	if match == nil {
		return false, false
	}
	// Rec. 709 relative luminance, the same weighting termenv uses to classify
	// a background on the platforms where it does query one.
	luminance := 0.2126*hexChannel(match[1]) +
		0.7152*hexChannel(match[2]) +
		0.0722*hexChannel(match[3])
	return luminance < 0.5, true
}

// hexChannel normalises a 1-4 digit hex colour channel to 0..1. OSC 11 replies
// report whatever width the terminal keeps internally, commonly 4 digits
// ("1e1e") but legitimately fewer.
func hexChannel(digits string) float64 {
	if len(digits) == 0 || len(digits) > 4 {
		return 0
	}
	value, err := strconv.ParseUint(digits, 16, 32)
	if err != nil {
		return 0
	}
	full := uint64(1)<<uint(4*len(digits)) - 1
	return float64(value) / float64(full)
}
