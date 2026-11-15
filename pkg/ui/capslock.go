package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// CapsLock Key Detection
//
// Technical Reality:
// CapsLock doesn't generate consistent key events across terminals.
// Most terminals/OS combinations intercept CapsLock before it reaches
// the application, using it to toggle letter case instead.
//
// Our Strategy:
// 1. Primary path: ? (help) → Space → Tutorial (always works)
// 2. Direct shortcut: ` (backtick) → Tutorial (reliable alternative)
// 3. CapsLock detection: Best-effort for terminals that do pass it through
//
// Terminal Compatibility:
// - macOS Terminal.app: CapsLock intercepted by OS
// - iTerm2: CapsLock intercepted by OS (can be remapped in settings)
// - Linux xterm/rxvt: Usually intercepted by X11
// - Windows Terminal: Usually intercepted by OS
// - Kitty: Can be configured to send CapsLock
// - Alacritty: Can be configured to send CapsLock

// TutorialTriggerKey defines the key used to trigger the tutorial directly.
// Default is backtick (`) since CapsLock is unreliable.
const TutorialTriggerKey = "`"

// ContextHelpTriggerKey defines the key for context-specific help.
// Default is tilde (~) as it's Shift+backtick, easy to remember.
const ContextHelpTriggerKey = "~"

// TutorialTrigger represents what action to take when tutorial key is pressed.
type TutorialTrigger int

const (
	TriggerNone TutorialTrigger = iota
	TriggerFullTutorial
	TriggerContextHelp
)

// String returns a human-readable description of the trigger.
func (t TutorialTrigger) String() string {
	switch t {
	case TriggerFullTutorial:
		return "full tutorial"
	case TriggerContextHelp:
		return "context help"
	default:
		return "none"
	}
}

// CapsLockTimerExpiredMsg is sent when the single-tap timer expires.
// This signals that the user intended a single tap (full tutorial)
// rather than a double-tap (context help).
type CapsLockTimerExpiredMsg struct{}

// ShowTutorialMsg signals the main model to show the tutorial.
type ShowTutorialMsg struct {
	ContextOnly bool   // If true, show context-specific help only
	Context     string // Current context identifier (used when ContextOnly is true)
}

// CapsLockTracker tracks CapsLock-style key presses for double-tap detection.
// It works with any configured trigger key, not just CapsLock.
type CapsLockTracker struct {
	lastPress time.Time
	threshold time.Duration
	pending   bool // True when waiting for potential double-tap
}

// NewCapsLockTracker creates a new tracker with the default 300ms threshold.
func NewCapsLockTracker() *CapsLockTracker {
	return &CapsLockTracker{
		threshold: 300 * time.Millisecond,
	}
}

// NewCapsLockTrackerWithThreshold creates a tracker with a custom threshold.
func NewCapsLockTrackerWithThreshold(threshold time.Duration) *CapsLockTracker {
	return &CapsLockTracker{
		threshold: threshold,
	}
}

// HandlePress processes a trigger key press and returns the appropriate command.
// Call this when the tutorial trigger key is detected.
//
// Returns:
// - TriggerContextHelp if this is a double-tap (< threshold since last)
// - TriggerNone with a timer command if this might be a single tap
func (c *CapsLockTracker) HandlePress() (TutorialTrigger, tea.Cmd) {
	now := time.Now()

	// Check for double-tap
	if c.pending && now.Sub(c.lastPress) < c.threshold {
		c.lastPress = time.Time{}
		c.pending = false
		return TriggerContextHelp, nil
	}

	// Start single-tap timer
	c.lastPress = now
	c.pending = true
	return TriggerNone, tea.Tick(c.threshold, func(time.Time) tea.Msg {
		return CapsLockTimerExpiredMsg{}
	})
}

// HandleTimerExpired processes the timer expiration message.
// Call this when CapsLockTimerExpiredMsg is received.
//
// Returns TriggerFullTutorial if we were waiting for potential double-tap.
func (c *CapsLockTracker) HandleTimerExpired() TutorialTrigger {
	if c.pending {
		c.pending = false
		return TriggerFullTutorial
	}
	return TriggerNone
}

// Reset clears the tracker state.
func (c *CapsLockTracker) Reset() {
	c.lastPress = time.Time{}
	c.pending = false
}

// IsPending returns true if waiting for potential double-tap.
func (c *CapsLockTracker) IsPending() bool {
	return c.pending
}

// IsCapsLock attempts to detect if a key message is CapsLock.
// This is best-effort and may not work on all terminals.
//
// Returns false if uncertain - users should use the alternative trigger key.
func IsCapsLock(msg tea.KeyMsg) bool {
	// Most terminals don't pass CapsLock through, so this is conservative.
	// We check for patterns that some terminals might use.

	// Some terminals might send CapsLock as a specific escape sequence
	// or with a particular key type. In practice, this rarely works.

	// Check if it looks like a potential CapsLock event
	// Note: msg.String() returns "" for unknown keys
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 0 {
		// Empty runes could indicate a modifier-only key
		return false // Still too ambiguous
	}

	key := strings.TrimSpace(msg.String())
	if strings.EqualFold(key, "caps_lock") || strings.EqualFold(key, "capslock") {
		return true
	}

	return false // Conservative: assume CapsLock not detected
}

// IsTutorialTrigger checks if the key message is the tutorial trigger key.
// This is the reliable way to trigger the tutorial.
func IsTutorialTrigger(msg tea.KeyMsg) bool {
	return msg.String() == TutorialTriggerKey
}

// IsContextHelpTrigger checks if the key message is the context help trigger.
// This provides direct access to context help without double-tap.
func IsContextHelpTrigger(msg tea.KeyMsg) bool {
	return msg.String() == ContextHelpTriggerKey
}

// GetTriggerKeyHint returns a user-friendly hint for the trigger keys.
func GetTriggerKeyHint() string {
	return TutorialTriggerKey + " tutorial | " + ContextHelpTriggerKey + " context help"
}

// TutorialKeyBindings holds configurable key bindings for tutorial access.
type TutorialKeyBindings struct {
	DirectTutorial  string // Key for direct tutorial access (default: `)
	ContextHelp     string // Key for direct context help (default: ~)
	HelpModalSpace  bool   // Whether Space in help modal opens tutorial
	DoubleTapEnable bool   // Whether double-tap detection is enabled
}

// DefaultTutorialKeyBindings returns the default key binding configuration.
func DefaultTutorialKeyBindings() TutorialKeyBindings {
	return TutorialKeyBindings{
		DirectTutorial:  TutorialTriggerKey,
		ContextHelp:     ContextHelpTriggerKey,
		HelpModalSpace:  true,
		DoubleTapEnable: true,
	}
}

// IsDirectTutorial checks if the key matches the direct tutorial binding.
func (b TutorialKeyBindings) IsDirectTutorial(msg tea.KeyMsg) bool {
	return msg.String() == b.DirectTutorial
}

// IsContextHelp checks if the key matches the context help binding.
func (b TutorialKeyBindings) IsContextHelp(msg tea.KeyMsg) bool {
	return msg.String() == b.ContextHelp
}
