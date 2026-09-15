package ui

import (
	"fmt"
	"io"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestPaletteColorRenderParity(t *testing.T) {
	colors := []lipgloss.AdaptiveColor{
		ColorMuted, ColorInfo, ColorBgHighlight, ColorPrimary,
		{Light: "#555555", Dark: "#6272A4"},
		{Light: "#6B47D9", Dark: "#BD93F9"},
		{Light: "#E0E0E0", Dark: "#44475A"},
		{Light: "#000000", Dark: "#F8F8F2"},
		{Light: "#FFFFFF", Dark: "#282A36"},
		{Light: "1", Dark: "240"},
		{Light: "", Dark: "invalid"},
	}
	for i, color := range colors {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			r := lipgloss.NewRenderer(io.Discard)
			style := func(c lipgloss.TerminalColor) lipgloss.Style {
				return r.NewStyle().Foreground(c).Background(c).
					Border(lipgloss.NormalBorder()).BorderForeground(c).Bold(true)
			}
			original, cached := style(color), style(paletteColor(color))
			// Change the renderer after creating both styles: adaptation must
			// remain live rather than freezing the initial terminal settings.
			for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI, termenv.Ascii} {
				r.SetColorProfile(profile)
				for _, dark := range []bool{false, true} {
					r.SetHasDarkBackground(dark)
					if got, want := cached.Render("界 alpha\nbeta"), original.Render("界 alpha\nbeta"); got != want {
						t.Fatalf("profile=%v dark=%v: got %q want %q", profile, dark, got, want)
					}
				}
			}
		})
	}
}

func TestPanelPaletteRenderParity(t *testing.T) {
	for _, panel := range []struct {
		name  string
		style lipgloss.Style
		color lipgloss.AdaptiveColor
	}{
		{"normal", PanelStyle, ColorBgHighlight},
		{"focused", FocusedPanelStyle, ColorPrimary},
	} {
		t.Run(panel.name, func(t *testing.T) {
			r := lipgloss.NewRenderer(io.Discard)
			original := r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(panel.color)
			cached := panel.style.Renderer(r)
			for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI, termenv.Ascii} {
				r.SetColorProfile(profile)
				for _, dark := range []bool{false, true} {
					r.SetHasDarkBackground(dark)
					for _, width := range []int{3, 65} {
						content := "界 alpha\n\x1b[31mbeta\x1b[0m"
						got := cached.Width(width).Height(40).Render(content)
						want := original.Width(width).Height(40).Render(content)
						if got != want {
							t.Fatalf("profile=%v dark=%v width=%d: got %q want %q", profile, dark, width, got, want)
						}
					}
				}
			}
		})
	}
}

func BenchmarkPanelPaletteRender(b *testing.B) {
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("precomputed=%v", cached), func(b *testing.B) {
			r := lipgloss.NewRenderer(io.Discard)
			r.SetColorProfile(termenv.ANSI256)
			r.SetHasDarkBackground(true)
			normal := r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorBgHighlight)
			focused := r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorPrimary)
			if cached {
				normal, focused = PanelStyle.Renderer(r), FocusedPanelStyle.Renderer(r)
			}
			normal, focused = normal.Width(65).Height(40), focused.Width(65).Height(40)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if normal.Render("界 alpha\nbeta") == "" || focused.Render("界 alpha\nbeta") == "" {
					b.Fatal("empty rendered panel")
				}
			}
		})
	}
}

func TestPaletteColorOutOfRangeStaysLazy(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	color := lipgloss.AdaptiveColor{Light: "256", Dark: "1"}
	original := r.NewStyle().Foreground(color)
	cached := r.NewStyle().Foreground(paletteColor(color))
	for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.Ascii, termenv.ANSI} {
		r.SetColorProfile(profile)
		for _, dark := range []bool{false, true} {
			// The original library itself panics for an active out-of-range
			// ANSI color. Exercise only its supported rendering paths here.
			if profile == termenv.ANSI && !dark {
				continue
			}
			r.SetHasDarkBackground(dark)
			if got, want := cached.Render("label"), original.Render("label"); got != want {
				t.Fatalf("profile=%v dark=%v: got %q want %q", profile, dark, got, want)
			}
		}
	}
}

func BenchmarkAdaptivePaletteRender(b *testing.B) {
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("precomputed=%v", cached), func(b *testing.B) {
			r := lipgloss.NewRenderer(io.Discard)
			r.SetColorProfile(termenv.ANSI256)
			r.SetHasDarkBackground(true)
			var color lipgloss.TerminalColor = ColorMuted
			if cached {
				color = paletteColor(ColorMuted)
			}
			style := r.NewStyle().Foreground(color)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if style.Render("BV-123") == "" {
					b.Fatal("empty rendered label")
				}
			}
		})
	}
}

func TestDefaultTheme(t *testing.T) {
	renderer := lipgloss.NewRenderer(nil)
	theme := DefaultTheme(renderer)

	if theme.Renderer != renderer {
		t.Error("DefaultTheme renderer mismatch")
	}
	// Check a few known colors are set (not zero value)
	if isColorEmpty(theme.Primary) {
		t.Error("DefaultTheme Primary color is empty")
	}
	if isColorEmpty(theme.Open) {
		t.Error("DefaultTheme Open color is empty")
	}
}

func isColorEmpty(c lipgloss.AdaptiveColor) bool {
	return c.Light == "" && c.Dark == ""
}

func TestSetThemeOverride(t *testing.T) {
	// SetThemeOverride mutates package-global state (BVThemeOverride and the
	// default lipgloss renderer); restore both afterwards.
	prevOverride := BVThemeOverride
	prevDark := lipgloss.HasDarkBackground()
	defer func() {
		BVThemeOverride = prevOverride
		lipgloss.SetHasDarkBackground(prevDark)
	}()

	cases := []struct {
		in           string
		wantOverride string
		// wantDark is nil when the global renderer must be left untouched.
		wantDark *bool
	}{
		{"light", "light", boolPtrTest(false)},
		{"dark", "dark", boolPtrTest(true)},
		{"  LIGHT ", "light", boolPtrTest(false)}, // case/whitespace tolerant
		{"Dark", "dark", boolPtrTest(true)},
		{"auto", "", nil},
		{"", "", nil},
		{"solarized", "", nil}, // unrecognized clears the override
	}
	for _, tc := range cases {
		// Seed a known-dirty state so "leaves untouched" is observable.
		lipgloss.SetHasDarkBackground(true)
		SetThemeOverride(tc.in)
		if BVThemeOverride != tc.wantOverride {
			t.Errorf("SetThemeOverride(%q): BVThemeOverride = %q, want %q",
				tc.in, BVThemeOverride, tc.wantOverride)
		}
		if tc.wantDark != nil {
			if got := lipgloss.HasDarkBackground(); got != *tc.wantDark {
				t.Errorf("SetThemeOverride(%q): global HasDarkBackground() = %v, want %v",
					tc.in, got, *tc.wantDark)
			}
		} else if !lipgloss.HasDarkBackground() {
			t.Errorf("SetThemeOverride(%q): global renderer changed, want untouched", tc.in)
		}
	}
}

func boolPtrTest(b bool) *bool { return &b }

func TestGetStatusColor(t *testing.T) {
	renderer := lipgloss.NewRenderer(nil)
	theme := DefaultTheme(renderer)

	tests := []struct {
		status string
		want   lipgloss.AdaptiveColor
	}{
		{"open", theme.Open},
		{"in_progress", theme.InProgress},
		{"blocked", theme.Blocked},
		{"closed", theme.Closed},
		{"unknown", theme.Subtext},
		{"", theme.Subtext},
	}

	for _, tt := range tests {
		got := theme.GetStatusColor(tt.status)
		if got != tt.want {
			t.Errorf("GetStatusColor(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestGetTypeIcon(t *testing.T) {
	renderer := lipgloss.NewRenderer(nil)
	theme := DefaultTheme(renderer)

	tests := []struct {
		typ      string
		wantIcon string
		wantCol  lipgloss.AdaptiveColor
	}{
		{"bug", "🐛", theme.Bug},
		{"feature", "✨", theme.Feature},
		{"task", "📋", theme.Task},
		{"epic", "🚀", theme.Epic}, // Changed from 🏔️ - variation selector caused width issues
		{"chore", "🧹", theme.Chore},
		{"unknown", "•", theme.Subtext},
	}

	for _, tt := range tests {
		icon, col := theme.GetTypeIcon(tt.typ)
		if icon != tt.wantIcon {
			t.Errorf("GetTypeIcon(%q) icon = %q, want %q", tt.typ, icon, tt.wantIcon)
		}
		if col != tt.wantCol {
			t.Errorf("GetTypeIcon(%q) color = %v, want %v", tt.typ, col, tt.wantCol)
		}
	}
}

// ── Color profile detection tests (bd-2rih) ─────────────────────────────

func TestColorProfile_Detection(t *testing.T) {
	// TermProfile is set at init(); just verify it's a valid value
	valid := map[colorprofile.Profile]bool{
		colorprofile.Unknown:   true,
		colorprofile.NoTTY:     true,
		colorprofile.ASCII:     true,
		colorprofile.ANSI:      true,
		colorprofile.ANSI256:   true,
		colorprofile.TrueColor: true,
	}
	if !valid[TermProfile] {
		t.Errorf("TermProfile has unexpected value: %d", TermProfile)
	}
}

func TestThemeBg_TrueColor(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.TrueColor

	got := ThemeBg("#282A36")
	if _, ok := got.(lipgloss.NoColor); ok {
		t.Error("ThemeBg should return hex color in TrueColor mode, got NoColor")
	}
}

func TestThemeBg_ANSI(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.ANSI

	got := ThemeBg("#282A36")
	if _, ok := got.(lipgloss.NoColor); !ok {
		t.Errorf("ThemeBg should return NoColor in ANSI mode, got %T", got)
	}
}

func TestThemeBg_ANSI256(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.ANSI256

	got := ThemeBg("#282A36")
	if _, ok := got.(lipgloss.NoColor); !ok {
		t.Errorf("ThemeBg should return NoColor in ANSI256 mode (only TrueColor gets hex bg), got %T", got)
	}
}

func TestThemeFg_TrueColor(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.TrueColor

	got := ThemeFg("#FF6B6B")
	if _, ok := got.(lipgloss.ANSIColor); ok {
		t.Error("ThemeFg should return hex color in TrueColor mode, got ANSIColor")
	}
}

func TestThemeFg_ANSI256(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.ANSI256

	got := ThemeFg("#FF6B6B")
	if _, ok := got.(lipgloss.ANSIColor); ok {
		t.Error("ThemeFg should return hex color in ANSI256 mode, got ANSIColor")
	}
}

func TestThemeFg_ANSI(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.ANSI

	got := ThemeFg("#FF6B6B")
	ansiColor, ok := got.(lipgloss.ANSIColor)
	if !ok {
		t.Errorf("ThemeFg should return ANSIColor in ANSI mode, got %T", got)
	} else if ansiColor != 7 {
		t.Errorf("ThemeFg should return ANSI white (7) in ANSI mode, got %d", ansiColor)
	}
}

func TestThemeFg_NoTTY(t *testing.T) {
	saved := TermProfile
	defer func() { TermProfile = saved }()

	TermProfile = colorprofile.NoTTY

	got := ThemeFg("#FF6B6B")
	if _, ok := got.(lipgloss.ANSIColor); !ok {
		t.Errorf("ThemeFg should return ANSIColor in NoTTY mode, got %T", got)
	}
}
