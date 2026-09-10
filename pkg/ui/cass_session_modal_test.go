package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/cass"
	"github.com/Dicklesworthstone/beads_viewer/pkg/drift"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// testTheme is defined in history_test.go and reused here

// Opt in with an existing, indexed bead ID and its workspace. This executes the
// installed cass against its real archive; ordinary suites use no user data.
func TestModel_CassInstalledSessionLookup(t *testing.T) {
	id := os.Getenv("BV_CASS_LIVE_BEAD")
	workspace := os.Getenv("BV_CASS_LIVE_WORKSPACE")
	if id == "" || workspace == "" {
		t.Skip("requires BV_CASS_LIVE_BEAD and BV_CASS_LIVE_WORKSPACE")
	}
	path, err := exec.LookPath("cass")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "search", `"`+id+`"`, "--robot", "--robot-format", "json", "--limit", "3", "--workspace", workspace, "--fields", "source_path,line_number,agent,title,content,created_at", "--max-content-length", "4000")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	t.Logf("cass=%s direct_search_exit=%v stderr=%q", path, err, stderr.String())
	if err != nil {
		t.Fatal("direct installed cass search failed")
	}
	var direct struct {
		Hits []struct {
			SourcePath string `json:"source_path"`
			LineNumber int    `json:"line_number"`
			Agent      string `json:"agent"`
			Title      string `json:"title"`
			Content    string `json:"content"`
			CreatedAt  *int64 `json:"created_at"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(output, &direct); err != nil || len(direct.Hits) == 0 {
		t.Fatalf("live prerequisite must return known sessions: parse=%v hits=%d", err, len(direct.Hits))
	}
	m := NewModel([]model.Issue{{ID: id, Title: "Live session lookup", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
	m.workDir = workspace
	m.width, m.height = 120, 40
	health := CheckCassHealthCmd()().(CassHealthMsg)
	m = asModelPtr(t, must2(m.Update(health)))
	updated, lookup := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	got := completeCassLookup(t, asModelPtr(t, updated), lookup)
	if !got.showCassModal || len(got.cassModal.sessions) == 0 {
		t.Fatalf("direct cass returned %d hits but V has no modal: health=%s status=%q", len(direct.Hits), health.Status, got.statusMsg)
	}
	matched := false
	for _, session := range got.cassModal.sessions {
		for _, hit := range direct.Hits {
			if hit.CreatedAt != nil && session.SourcePath == hit.SourcePath && session.LineNumber == hit.LineNumber && session.Agent == hit.Agent && session.Title == hit.Title && session.Snippet == hit.Content && session.Timestamp.Equal(time.UnixMilli(*hit.CreatedAt)) {
				matched = session.Title != "" && session.Snippet != "" && !session.Timestamp.IsZero()
			}
		}
	}
	if !matched {
		t.Fatal("modal did not preserve a live hit's location, title, content and timestamp")
	}
	rendered := got.cassModal.View()
	if !strings.Contains(rendered, got.cassModal.sessions[0].Agent) || strings.Contains(rendered, "(no preview available)") || !strings.Contains(rendered, id) {
		t.Fatal("live modal render omitted the bead, agent or session preview")
	}
	if got.cassStatus != health.Status {
		t.Fatal("opening sessions changed the reported archive health")
	}
	t.Logf("direct_hits=%d modal_sessions=%d archive_health=%s", len(direct.Hits), len(got.cassModal.sessions), got.cassStatus)
}

func TestNewCassSessionModal(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-abc123",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:     "claude",
					Timestamp: time.Now().Add(-2 * time.Hour),
					Snippet:   "Test snippet content",
				},
				FinalScore: 100,
				Strategy:   cass.StrategyIDMention,
			},
		},
		Strategy: cass.StrategyIDMention,
		Keywords: []string{"test"},
	}

	modal := NewCassSessionModal("bv-abc123", result, theme)

	if modal.beadID != "bv-abc123" {
		t.Errorf("Expected beadID bv-abc123, got %s", modal.beadID)
	}
	if len(modal.sessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(modal.sessions))
	}
	if modal.selected != 0 {
		t.Errorf("Expected selected=0, got %d", modal.selected)
	}
	if !modal.HasSessions() {
		t.Error("HasSessions should return true")
	}
}

func TestCassSessionModal_NoSessions(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID:      "bv-empty",
		TopSessions: []cass.ScoredResult{},
	}

	modal := NewCassSessionModal("bv-empty", result, theme)

	if modal.HasSessions() {
		t.Error("HasSessions should return false for empty sessions")
	}

	// View should still render without panic
	view := modal.View()
	if !strings.Contains(view, "Related Coding Sessions") {
		t.Error("View should contain header even with no sessions")
	}
	if !strings.Contains(view, "No correlated sessions found") {
		t.Error("View should indicate no sessions found")
	}
}

func TestCassSessionModal_Update_Navigation(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-nav",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "First"}},
			{SearchResult: cass.SearchResult{Agent: "cursor", Snippet: "Second"}},
			{SearchResult: cass.SearchResult{Agent: "windsurf", Snippet: "Third"}},
		},
	}

	modal := NewCassSessionModal("bv-nav", result, theme)

	// Initial selection should be 0
	if modal.selected != 0 {
		t.Errorf("Initial selection should be 0, got %d", modal.selected)
	}

	// Move down
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if modal.selected != 1 {
		t.Errorf("After 'j', selection should be 1, got %d", modal.selected)
	}

	// Move down again
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if modal.selected != 2 {
		t.Errorf("After second 'j', selection should be 2, got %d", modal.selected)
	}

	// Try to move past the end (should stay at 2)
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if modal.selected != 2 {
		t.Errorf("Should not move past end, selection should be 2, got %d", modal.selected)
	}

	// Move up
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if modal.selected != 1 {
		t.Errorf("After 'k', selection should be 1, got %d", modal.selected)
	}

	// Move up to beginning
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if modal.selected != 0 {
		t.Errorf("After second 'k', selection should be 0, got %d", modal.selected)
	}

	// Try to move before beginning
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if modal.selected != 0 {
		t.Errorf("Should not move before beginning, selection should be 0, got %d", modal.selected)
	}
}

func TestCassSessionModal_Update_ArrowKeys(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-arrow",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "First"}},
			{SearchResult: cass.SearchResult{Agent: "cursor", Snippet: "Second"}},
		},
	}

	modal := NewCassSessionModal("bv-arrow", result, theme)

	// Arrow down
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyDown})
	if modal.selected != 1 {
		t.Errorf("After down arrow, selection should be 1, got %d", modal.selected)
	}

	// Arrow up
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyUp})
	if modal.selected != 0 {
		t.Errorf("After up arrow, selection should be 0, got %d", modal.selected)
	}
}

func TestCassSessionModal_Update_CopyCommand(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID:   "bv-copy",
		Keywords: []string{"test", "keyword"},
	}

	modal := NewCassSessionModal("bv-copy", result, theme)

	// The search command should be built from keywords
	if !strings.Contains(modal.searchCmd, "test keyword") {
		t.Errorf("Search command should contain keywords, got: %s", modal.searchCmd)
	}

	// Pressing 'y' must schedule the potentially blocking helper rather than run
	// it on Bubble Tea's Update loop.
	modal, cmd := modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("copy should return an asynchronous Bubble Tea command")
	}
	if modal.copied {
		t.Fatal("copy feedback should wait for the asynchronous result")
	}

	modal, _ = modal.Update(cassClipboardCopyMsg{modalToken: modal.copyToken})
	if !modal.copied {
		t.Fatal("successful copy result should enable feedback")
	}

	failed := NewCassSessionModal("bv-copy", result, theme)
	failed, _ = failed.Update(cassClipboardCopyMsg{
		modalToken: failed.copyToken,
		err:        fmt.Errorf("clipboard unavailable"),
	})
	if failed.copied {
		t.Fatal("failed copy result must not enable feedback")
	}

	reopened := NewCassSessionModal("bv-copy", result, theme)
	reopened, _ = reopened.Update(cassClipboardCopyMsg{modalToken: modal.copyToken})
	if reopened.copied {
		t.Fatal("late copy result from a dismissed modal must be ignored")
	}
}

func TestCassSessionModal_View_RendersCorrectly(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-render",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:     "claude",
					Timestamp: time.Now().Add(-2 * time.Hour),
					Snippet:   "This is a test snippet",
				},
				Strategy: cass.StrategyIDMention,
			},
			{
				SearchResult: cass.SearchResult{
					Agent:     "cursor",
					Timestamp: time.Now().Add(-24 * time.Hour),
					Snippet:   "Another snippet",
				},
				Strategy: cass.StrategyKeywords,
				Keywords: []string{"test"},
			},
		},
		Strategy: cass.StrategyIDMention,
	}

	modal := NewCassSessionModal("bv-render", result, theme)
	view := modal.View()

	// Check header is present
	if !strings.Contains(view, "Related Coding Sessions") {
		t.Error("View should contain header")
	}

	// Check bead ID is present
	if !strings.Contains(view, "bv-render") {
		t.Error("View should contain bead ID")
	}

	// Check agent names are present
	if !strings.Contains(view, "claude") {
		t.Error("View should contain first agent name")
	}
	if !strings.Contains(view, "cursor") {
		t.Error("View should contain second agent name")
	}

	// Check footer keybindings
	if !strings.Contains(view, "[j/k]") {
		t.Error("View should contain navigation hint")
	}
	if !strings.Contains(view, "[V/Esc]") {
		t.Error("View should contain close hint")
	}
}

func TestCassSessionModal_SetSize(t *testing.T) {
	theme := testTheme()
	modal := NewCassSessionModal("bv-size", cass.CorrelationResult{}, theme)

	// Default width should be 70
	if modal.width != 70 {
		t.Errorf("Default width should be 70, got %d", modal.width)
	}

	// Set small terminal size
	modal.SetSize(60, 30)
	if modal.width != 50 { // min is 50
		t.Errorf("Width should be constrained to min 50, got %d", modal.width)
	}

	// Set large terminal size
	modal.SetSize(200, 50)
	if modal.width != 80 { // max is 80
		t.Errorf("Width should be constrained to max 80, got %d", modal.width)
	}

	// Set medium terminal size
	modal.SetSize(100, 40)
	if modal.width != 90 { // 100 - 10 = 90, but max is 80
		// maxWidth = 100-10 = 90, but max is 80
		if modal.width != 80 {
			t.Errorf("Width should be 80 (capped), got %d", modal.width)
		}
	}
}

func TestCassSessionModal_CenterModal(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-center",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "Test"}},
		},
	}

	modal := NewCassSessionModal("bv-center", result, theme)

	// Just verify it doesn't panic and returns non-empty string
	centered := modal.CenterModal(120, 40)
	if centered == "" {
		t.Error("CenterModal should return non-empty string")
	}
}

func TestFormatRelativeTime(t *testing.T) {
	tests := []struct {
		name     string
		t        time.Time
		contains string
	}{
		{
			name:     "zero time",
			t:        time.Time{},
			contains: "unknown",
		},
		{
			name:     "just now",
			t:        time.Now().Add(-30 * time.Second),
			contains: "just now",
		},
		{
			name:     "minutes ago",
			t:        time.Now().Add(-5 * time.Minute),
			contains: "minutes ago",
		},
		{
			name:     "1 hour ago",
			t:        time.Now().Add(-1 * time.Hour),
			contains: "1 hour ago",
		},
		{
			name:     "hours ago",
			t:        time.Now().Add(-3 * time.Hour),
			contains: "hours ago",
		},
		{
			name:     "yesterday",
			t:        time.Now().Add(-36 * time.Hour),
			contains: "yesterday",
		},
		{
			name:     "days ago",
			t:        time.Now().Add(-4 * 24 * time.Hour),
			contains: "days ago",
		},
		{
			name:     "weeks ago",
			t:        time.Now().Add(-14 * 24 * time.Hour),
			contains: "weeks ago",
		},
		{
			name:     "old date",
			t:        time.Now().Add(-60 * 24 * time.Hour),
			contains: "2", // Month day will contain a digit
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatRelativeTime(tt.t)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("formatRelativeTime(%v) = %q, want to contain %q", tt.t, result, tt.contains)
			}
		})
	}
}

func TestCassSessionModal_FormatMatchReason(t *testing.T) {
	theme := testTheme()
	modal := NewCassSessionModal("bv-match", cass.CorrelationResult{BeadID: "bv-match"}, theme)

	tests := []struct {
		name     string
		session  cass.ScoredResult
		contains string
	}{
		{
			name:     "ID mention",
			session:  cass.ScoredResult{Strategy: cass.StrategyIDMention},
			contains: "bead ID mentioned",
		},
		{
			name: "Keywords with list",
			session: cass.ScoredResult{
				Strategy: cass.StrategyKeywords,
				Keywords: []string{"auth", "login"},
			},
			contains: "auth, login",
		},
		{
			name:     "Keywords without list",
			session:  cass.ScoredResult{Strategy: cass.StrategyKeywords},
			contains: "keyword search",
		},
		{
			name:     "Timestamp",
			session:  cass.ScoredResult{Strategy: cass.StrategyTimestamp},
			contains: "timeframe",
		},
		{
			name:     "Combined",
			session:  cass.ScoredResult{Strategy: cass.StrategyCombined},
			contains: "multiple signals",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := modal.formatMatchReason(tt.session)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("formatMatchReason() = %q, want to contain %q", result, tt.contains)
			}
		})
	}
}

func TestCassSessionModal_FormatSnippet(t *testing.T) {
	theme := testTheme()
	modal := NewCassSessionModal("bv-snip", cass.CorrelationResult{}, theme)
	modal.width = 70

	tests := []struct {
		name    string
		snippet string
		want    string
	}{
		{
			name:    "empty snippet",
			snippet: "",
			want:    "(no preview available)",
		},
		{
			name:    "simple snippet",
			snippet: "Hello world",
			want:    "Hello world",
		},
		{
			name:    "multiline snippet",
			snippet: "Line 1\nLine 2\nLine 3\nLine 4",
			want:    "Line 1\nLine 2\nLine 3", // max 3 lines
		},
		{
			name:    "whitespace only",
			snippet: "   \n\n   ",
			want:    "(no preview available)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := modal.formatSnippet(tt.snippet)
			if !strings.Contains(result, tt.want) && result != tt.want {
				t.Errorf("formatSnippet(%q) = %q, want %q", tt.snippet, result, tt.want)
			}
		})
	}
}

func TestCassSessionModal_MaxDisplayLimit(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-limit",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "agent1", Snippet: "One"}},
			{SearchResult: cass.SearchResult{Agent: "agent2", Snippet: "Two"}},
			{SearchResult: cass.SearchResult{Agent: "agent3", Snippet: "Three"}},
			{SearchResult: cass.SearchResult{Agent: "agent4", Snippet: "Four"}},
			{SearchResult: cass.SearchResult{Agent: "agent5", Snippet: "Five"}},
		},
	}

	modal := NewCassSessionModal("bv-limit", result, theme)
	view := modal.View()

	// Should show "more sessions" message since we have 5 but max display is 3
	if !strings.Contains(view, "more session") {
		t.Error("View should indicate there are more sessions")
	}

	// Should show first 3 agents
	if !strings.Contains(view, "agent1") {
		t.Error("View should contain agent1")
	}
	if !strings.Contains(view, "agent2") {
		t.Error("View should contain agent2")
	}
	if !strings.Contains(view, "agent3") {
		t.Error("View should contain agent3")
	}

	// Should NOT show agent4 or agent5 in the list
	// (they may appear in the "more sessions" message indirectly but not as session entries)
}

func TestCassSessionModal_SingleSession(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-single",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "Only one"}},
		},
	}

	modal := NewCassSessionModal("bv-single", result, theme)

	// Navigation should not crash with single session
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if modal.selected != 0 {
		t.Errorf("With single session, selection should stay at 0, got %d", modal.selected)
	}

	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if modal.selected != 0 {
		t.Errorf("With single session, selection should stay at 0, got %d", modal.selected)
	}
}

func TestCassSessionModal_NavigationCappedByMaxDisplay(t *testing.T) {
	theme := testTheme()
	// Create 5 sessions but maxDisplay is 3
	result := cass.CorrelationResult{
		BeadID: "bv-navbound",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "agent1", Snippet: "One"}},
			{SearchResult: cass.SearchResult{Agent: "agent2", Snippet: "Two"}},
			{SearchResult: cass.SearchResult{Agent: "agent3", Snippet: "Three"}},
			{SearchResult: cass.SearchResult{Agent: "agent4", Snippet: "Four"}},
			{SearchResult: cass.SearchResult{Agent: "agent5", Snippet: "Five"}},
		},
	}

	modal := NewCassSessionModal("bv-navbound", result, theme)

	// Navigation should stop at index 2 (third session), not 4
	// Move down 5 times
	for i := 0; i < 5; i++ {
		modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}

	// Should be capped at maxDisplay-1 (index 2), not len(sessions)-1 (index 4)
	if modal.selected != 2 {
		t.Errorf("Navigation should be capped at maxDisplay-1 (2), got %d", modal.selected)
	}

	// Verify the selected session is the third one (still visible)
	if modal.selected >= len(modal.sessions) || modal.sessions[modal.selected].Agent != "agent3" {
		t.Error("Selected session should be the third displayed session (agent3)")
	}
}

// === Additional tests for bv-mtyf (Documentation & Testing Refresh) ===

func TestCassSessionModal_NilFieldsHandling(t *testing.T) {
	theme := testTheme()

	// Test with minimal/nil fields - should not panic
	result := cass.CorrelationResult{
		BeadID:      "bv-nil",
		TopSessions: []cass.ScoredResult{{}}, // Empty session
	}

	modal := NewCassSessionModal("bv-nil", result, theme)

	// View should render without panic even with empty session
	view := modal.View()
	if view == "" {
		t.Error("View should not be empty even with minimal data")
	}

	// Should contain header
	if !strings.Contains(view, "Related Coding Sessions") {
		t.Error("View should contain header even with nil fields")
	}
}

func TestCassSessionModal_ZeroTimestamp(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-zero-time",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:     "claude",
					Timestamp: time.Time{}, // Zero time
					Snippet:   "Test",
				},
			},
		},
	}

	modal := NewCassSessionModal("bv-zero-time", result, theme)
	view := modal.View()

	// Should handle zero timestamp gracefully (shows "unknown")
	if !strings.Contains(view, "unknown") && !strings.Contains(view, "claude") {
		t.Error("View should handle zero timestamp gracefully")
	}
}

func TestCassSessionModal_CopyFeedbackTiming(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID:   "bv-feedback",
		Keywords: []string{"test"},
	}

	modal := NewCassSessionModal("bv-feedback", result, theme)

	// Initially copied should be false
	if modal.copied {
		t.Error("copied should be false initially")
	}

	// Simulate copy (may fail in CI but should set the flag if clipboard available)
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// If clipboard is available, copied should be true and copiedAt set
	// We can't reliably test this in CI, but the View() shouldn't panic
	view := modal.View()
	if view == "" {
		t.Error("View should render after copy attempt")
	}
}

func TestCassSessionModal_SpecialCharactersInSnippet(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-special",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:   "claude",
					Snippet: "Special chars: <html>\"quotes\" & 'apostrophes' \ttab\nnewline",
				},
			},
		},
	}

	modal := NewCassSessionModal("bv-special", result, theme)
	view := modal.View()

	// View should render without issues
	if view == "" {
		t.Error("View should not be empty with special characters")
	}

	// Should contain the agent name at least
	if !strings.Contains(view, "claude") {
		t.Error("View should contain agent name")
	}
}

func TestCassSessionModal_UnicodeInSnippet(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-unicode",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:   "claude",
					Snippet: "Unicode: 你好世界 🎉 émojis ñ café αβγ",
				},
			},
		},
	}

	modal := NewCassSessionModal("bv-unicode", result, theme)
	view := modal.View()

	// View should render without issues
	if view == "" {
		t.Error("View should not be empty with unicode")
	}

	// Should contain the agent name
	if !strings.Contains(view, "claude") {
		t.Error("View should contain agent name")
	}
}

func TestCassSessionModal_LongAgentName(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-long-agent",
		TopSessions: []cass.ScoredResult{
			{
				SearchResult: cass.SearchResult{
					Agent:   "this-is-a-very-long-agent-name-that-might-cause-layout-issues",
					Snippet: "Test",
				},
			},
		},
	}

	modal := NewCassSessionModal("bv-long-agent", result, theme)
	view := modal.View()

	// View should render without panic
	if view == "" {
		t.Error("View should not be empty with long agent name")
	}
}

func TestCassSessionModal_EmptyBeadID(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "Test"}},
		},
	}

	modal := NewCassSessionModal("", result, theme)

	// Should not panic and should have some search command
	if modal.searchCmd == "" {
		t.Error("searchCmd should not be empty even with empty beadID")
	}

	view := modal.View()
	if view == "" {
		t.Error("View should not be empty")
	}
}

func TestCassSessionModal_ManyKeywords(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID:   "bv-keywords",
		Keywords: []string{"auth", "login", "session", "token", "jwt", "oauth", "security", "middleware"},
	}

	modal := NewCassSessionModal("bv-keywords", result, theme)

	// Search command should contain all keywords
	if !strings.Contains(modal.searchCmd, "auth login session") {
		t.Errorf("searchCmd should contain keywords, got: %s", modal.searchCmd)
	}

	view := modal.View()
	if view == "" {
		t.Error("View should not be empty")
	}
}

func TestCassSessionModal_UnhandledKeyIgnored(t *testing.T) {
	theme := testTheme()
	result := cass.CorrelationResult{
		BeadID: "bv-key",
		TopSessions: []cass.ScoredResult{
			{SearchResult: cass.SearchResult{Agent: "claude", Snippet: "Test"}},
			{SearchResult: cass.SearchResult{Agent: "cursor", Snippet: "Test2"}},
		},
	}

	modal := NewCassSessionModal("bv-key", result, theme)
	initialSelected := modal.selected

	// Press unhandled keys - should not change state
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	modal, _ = modal.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})

	if modal.selected != initialSelected {
		t.Errorf("Unhandled keys should not change selection, got %d", modal.selected)
	}
}

// writeStubCass puts a fake `cass` on PATH whose `health` exit code is
// controlled by CASS_STUB_EXIT (0 healthy, 1 needs index).
func writeStubCass(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"health\" ]; then exit \"${CASS_STUB_EXIT:-0}\"; fi\nif [ \"$1\" = \"search\" ]; then printf '%s' \"${CASS_STUB_SEARCH:-}\"; exit \"${CASS_STUB_SEARCH_EXIT:-0}\"; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "cass"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub cass: %v", err)
	}
	return dir
}

func TestModel_CassLookupYieldsBeforeSearch(t *testing.T) {
	t.Setenv("PATH", writeStubCass(t))
	t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/session","agent":"codex","content":"OAuth preview"}],"total_matches":1}`)
	m := NewModel([]model.Issue{{ID: "bv-preview", Title: "OAuth preview", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
	m.width, m.height = 120, 40
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	got := asModelPtr(t, updated)
	if cmd == nil || got.showCassModal || !strings.Contains(got.statusMsg, "Looking up sessions") {
		t.Fatalf("V must yield a pending lookup before executing Cass: command=%v modal=%v status=%q", cmd != nil, got.showCassModal, got.statusMsg)
	}
	t.Cleanup(got.Stop)
}

// Execute the actual command tree returned by Update, including tea.Batch.
// Other UI commands can also be queued; only the session completion is needed.
func cassLookupMessage(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			if result := cassLookupMessage(child); result != nil {
				return result
			}
		}
		return nil
	}
	if _, ok := msg.(cassSessionsLoadedMsg); ok {
		return msg
	}
	return nil
}

func completeCassLookup(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	msg := cassLookupMessage(cmd)
	if msg == nil {
		t.Fatal("V did not queue a session lookup completion")
	}
	return asModelPtr(t, must2(m.Update(msg)))
}

func TestModel_CassLookupKeepsInputResponsive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("controlled POSIX subprocess; native Windows is a separate acceptance gate")
	}
	for _, stage := range []string{"health", "search"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			script := `#!/bin/sh
if [ "$1" = "$CASS_BLOCK_STAGE" ]; then
  : > "$CASS_STARTED"
  while [ ! -e "$CASS_RELEASE" ]; do /bin/sleep 0.01; done
fi
if [ "$1" = "health" ]; then exit 0; fi
printf '%s' '{"hits":[{"source_path":"/session","agent":"codex","content":"preview"}],"total_matches":1}'
`
			if err := os.WriteFile(filepath.Join(dir, "cass"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("CASS_BLOCK_STAGE", stage)
			t.Setenv("CASS_STARTED", started)
			t.Setenv("CASS_RELEASE", release)
			m := NewModel([]model.Issue{
				{ID: "A", Title: "First", Status: model.StatusOpen, IssueType: model.TypeTask},
				{ID: "B", Title: "Second", Status: model.StatusOpen, IssueType: model.TypeTask},
			}, nil, "")
			m = asModelPtr(t, must2(m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})))
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			done := make(chan tea.Msg, 1)
			go func() { done <- cassLookupMessage(cmd) }()
			t.Cleanup(func() {
				m.Stop()
				if err := os.WriteFile(release, nil, 0o600); err != nil {
					t.Error(err)
				}
			})
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(started); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("controlled Cass did not enter the blocked subprocess")
				}
				time.Sleep(5 * time.Millisecond)
			}
			m = asModelPtr(t, must2(m.Update(tea.WindowSizeMsg{Width: 90, Height: 25})))
			if m.width != 90 || m.height != 25 || m.cassRequest == nil || !strings.Contains(m.View(), "Looking up sessions") {
				t.Fatal("resize/render did not proceed while Cass was blocked")
			}
			select {
			case <-done:
				t.Fatal("subprocess completed before release: this did not exercise a pending lookup")
			default:
			}
			before := m.focusedIssueForSessions().ID
			m = asModelPtr(t, must2(m.Update(tea.KeyMsg{Type: tea.KeyDown})))
			if m.focusedIssueForSessions().ID == before || m.cassRequest != nil {
				t.Fatal("navigation must proceed and cancel the old selection's lookup")
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				m = asModelPtr(t, must2(m.Update(result)))
				if m.showCassModal || m.focusedIssueForSessions().ID == before {
					t.Fatal("cancelled completion replaced the user's current selection")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled lookup did not finish")
			}
		})
	}
}

func TestModel_CassLookupDiscardsStaleCompletions(t *testing.T) {
	for _, invalidate := range []string{"V", "esc", "selection", "dataset", "workspace", "help", "alerts", "filter", "quit", "stop"} {
		t.Run(invalidate, func(t *testing.T) {
			t.Setenv("PATH", writeStubCass(t))
			t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/session","agent":"codex","content":"preview"}],"total_matches":1}`)
			m := NewModel([]model.Issue{
				{ID: "A", Title: "First", Status: model.StatusOpen, IssueType: model.TypeTask},
				{ID: "B", Title: "Second", Status: model.StatusOpen, IssueType: model.TypeTask},
			}, nil, "")
			t.Cleanup(m.Stop)
			m.width, m.height = 120, 40
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			old := cassLookupMessage(cmd)
			if old == nil {
				t.Fatal("missing original completion")
			}
			switch invalidate {
			case "V", "esc":
				key := tea.KeyMsg{Type: tea.KeyEsc}
				if invalidate == "V" {
					key = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")}
				}
				_, duplicate := m.Update(key)
				if duplicate != nil || m.cassRequest != nil {
					t.Fatal("cancel must not start a duplicate lookup")
				}
			case "selection":
				m.Update(tea.KeyMsg{Type: tea.KeyDown})
			case "dataset":
				m.beginSemanticDatasetUpdate()
			case "workspace":
				m.workDir = t.TempDir()
			case "help":
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
			case "alerts":
				m.alerts = []drift.Alert{{Type: drift.AlertStaleIssue, Severity: drift.SeverityWarning, IssueID: "A", Message: "Test alert"}}
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("!")})
				if !m.showAlertsPanel {
					t.Fatal("alerts key did not open the intended overlay")
				}
			case "filter":
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
				if m.list.FilterState() != list.Filtering {
					t.Fatal("filter key did not start editing the filter")
				}
			case "quit":
				m.quitCommand()
			case "stop":
				m.Stop()
			}
			m.Update(old)
			if m.showCassModal || m.cassRequest != nil {
				t.Fatal("stale completion opened the modal or retained the pending request")
			}
			// A fresh request for the same ID must not accept the older reply.
			m.focused, m.showHelp = focusList, false
			m.showAlertsPanel = false
			m.list.ResetFilter()
			m.list.Select(0)
			_, next := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			request := m.cassRequest
			m.Update(old)
			if m.showCassModal || request == nil || m.cassRequest != request {
				t.Fatal("out-of-order completion stole the newer lookup")
			}
			got := completeCassLookup(t, m, next)
			if !got.showCassModal || got.cassModal.beadID != got.list.SelectedItem().(IssueItem).Issue.ID {
				t.Fatal("fresh retry failed to display the selected bead's sessions")
			}
		})
	}
}

func TestModel_CassLookupRefreshDiscardsOldCache(t *testing.T) {
	for _, refresh := range []string{"dataset", "workspace"} {
		t.Run(refresh, func(t *testing.T) {
			t.Setenv("PATH", writeStubCass(t))
			t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/old","agent":"codex","content":"old preview"}],"total_matches":1}`)
			m := NewModel([]model.Issue{{ID: "A", Title: "First", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
			t.Cleanup(m.Stop)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			old := cassLookupMessage(cmd)
			oldCorrelator := m.cassCorrelator
			if old == nil || oldCorrelator.GetCached("A") == nil {
				t.Fatal("original real command did not populate its correlation cache")
			}
			if refresh == "dataset" {
				m.beginSemanticDatasetUpdate()
			} else {
				m.workDir = t.TempDir()
			}
			m.Update(old)
			t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/new","agent":"codex","content":"fresh preview"}],"total_matches":1}`)
			_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			got := completeCassLookup(t, m, cmd)
			if got.cassCorrelator == oldCorrelator || !got.showCassModal || got.cassModal.sessions[0].Snippet != "fresh preview" {
				t.Fatal("new lookup reused an older dataset/workspace correlation cache")
			}
		})
	}
}

func TestModel_CassStartupHealthAfterCancelledLookup(t *testing.T) {
	t.Setenv("PATH", writeStubCass(t))
	t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/session","agent":"codex","content":"preview"}],"total_matches":1}`)
	m := NewModel([]model.Issue{{ID: "A", Title: "First", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
	t.Cleanup(m.Stop)
	_, pending := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	detector, correlator := m.cassDetector, m.cassCorrelator
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	startup := CheckCassHealthCmd()().(CassHealthMsg)
	m.Update(startup)
	if startup.Status != cass.StatusHealthy || m.cassStatus != startup.Status || m.cassDetector != detector || m.cassCorrelator != correlator {
		t.Fatal("cancelled early lookup hid startup health or replaced its detector/correlator")
	}
	m.Update(cassLookupMessage(pending))
	if m.showCassModal || m.cassStatus != cass.StatusHealthy {
		t.Fatal("cancelled command disturbed the startup health result")
	}
	_, lookup := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	m = completeCassLookup(t, m, lookup)
	m.Update(CassHealthMsg{Status: cass.StatusNeedsIndex, Detector: startup.Detector})
	if !m.showCassModal || m.cassStatus != cass.StatusHealthy || m.cassDetector != detector {
		t.Fatal("late startup probe overwrote a newer completed lookup's health")
	}
}

func TestModel_CassLookupReturnsToOriginatingView(t *testing.T) {
	for _, origin := range []focus{focusList, focusDetail, focusBoard, focusTree, focusHistory} {
		t.Run(fmt.Sprint(origin), func(t *testing.T) {
			t.Setenv("PATH", writeStubCass(t))
			t.Setenv("CASS_STUB_SEARCH", `{"hits":[{"source_path":"/session","agent":"codex","content":"preview"}],"total_matches":1}`)
			m := NewModel([]model.Issue{{ID: "A", Title: "First", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
			m.width, m.height = 120, 40
			if origin == focusTree {
				m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("E")})
			}
			if origin == focusHistory {
				m.historyView = NewHistoryModel(createTestHistoryReport(), m.theme)
				m.issues = append(m.issues, model.Issue{ID: m.historyView.SelectedBeadID(), Title: "History", Status: model.StatusOpen, IssueType: model.TypeTask})
				m.historyReportDataGeneration = m.semanticDataGeneration
			}
			m.focused = origin
			m.isBoardView, m.isHistoryView = origin == focusBoard, origin == focusHistory
			want := m.focusedIssueForSessions()
			if want == nil {
				t.Fatal("origin has no selected issue")
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			got := completeCassLookup(t, m, cmd)
			if !got.showCassModal || got.cassModal.beadID != want.ID {
				t.Fatal("origin did not receive its selected issue's sessions")
			}
			got.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
			if got.cassModal.width != 60 || got.cassModal.height != 30 {
				t.Fatal("open session modal did not adapt to the terminal resize")
			}
			got.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if got.showCassModal || got.focused != origin {
				t.Fatalf("dismiss returned to %v instead of %v", got.focused, origin)
			}
			if origin == focusList || origin == focusBoard || origin == focusHistory {
				_, pending := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
				result := cassLookupMessage(pending)
				got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
				searching := (origin == focusList && got.list.FilterState() == list.Filtering) ||
					(origin == focusBoard && got.board.IsSearchMode()) ||
					(origin == focusHistory && got.historyView.IsSearchActive())
				if result == nil || !searching || got.cassRequest != nil {
					t.Fatal("starting an embedded search did not cancel the pending session lookup")
				}
				got.Update(result)
				if got.showCassModal {
					t.Fatal("late session completion interrupted the active search input")
				}
			}
		})
	}
}

func TestModel_CassLookupUsesSearchOutcome(t *testing.T) {
	// Controlled subprocess cases, separate from the installed-archive proof.
	for _, tc := range []struct {
		name, output, exit string
		wantModal          bool
		wantStatus         string
	}{
		{"stale searchable", `{"hits":[{"source_path":"/session","agent":"codex","content":"OAuth preview","created_at":1788322597432}],"total_matches":1}`, "0", true, ""},
		{"empty success", `{"hits":[],"total_matches":0}`, "0", false, "No correlated sessions found"},
		{"failed search", "", "1", false, "Session lookup incomplete"},
		{"malformed response", `{"hits":`, "0", false, "Session lookup incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", writeStubCass(t))
			t.Setenv("CASS_STUB_EXIT", "1")
			t.Setenv("CASS_STUB_SEARCH", tc.output)
			t.Setenv("CASS_STUB_SEARCH_EXIT", tc.exit)
			m := NewModel([]model.Issue{{ID: "bv-preview", Title: "OAuth preview", Status: model.StatusOpen, IssueType: model.TypeTask}}, nil, "")
			m.width, m.height = 120, 40
			updated, lookup := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
			got := completeCassLookup(t, asModelPtr(t, updated), lookup)
			if got.showCassModal != tc.wantModal || !strings.Contains(got.statusMsg, tc.wantStatus) || got.cassStatus != cass.StatusNeedsIndex {
				t.Fatalf("modal=%v status=%q health=%s", got.showCassModal, got.statusMsg, got.cassStatus)
			}
			if tc.wantModal && (!strings.Contains(got.cassModal.View(), "OAuth preview") || !strings.Contains(got.cassModal.View(), "codex")) {
				t.Fatal("modal lost preview text or agent")
			}
		})
	}
}

// TestModel_CassDetectionOnStartup (E4): the startup health check feeds the
// footer badge: healthy shows "🤖 cass", an unindexed install shows
// "⚠ cass index", and no cass on PATH shows nothing.
func TestModel_CassDetectionOnStartup(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	footerAfter := func(t *testing.T, path, stubExit string) string {
		t.Helper()
		t.Setenv("PATH", path)
		t.Setenv("CASS_STUB_EXIT", stubExit)
		t.Setenv("BV_TEST_MODE", "")
		m := NewModel(issues, nil, "")
		m.width, m.height = 120, 40
		msg := CheckCassHealthCmd()()
		health, ok := msg.(CassHealthMsg)
		if !ok {
			t.Fatalf("CheckCassHealthCmd returned %T", msg)
		}
		got := asModelPtr(t, must2(m.Update(health)))
		if got.cassStatus != health.Status || got.cassDetector == nil {
			t.Fatalf("model did not record the detection result: status=%v detector=%v", got.cassStatus, got.cassDetector)
		}
		return got.renderFooter()
	}

	stub := writeStubCass(t)
	if footer := footerAfter(t, stub, "0"); !strings.Contains(footer, "🤖 cass") {
		t.Fatalf("healthy cass should show the badge, footer=%q", footer)
	}
	if footer := footerAfter(t, stub, "1"); !strings.Contains(footer, "⚠ cass index") {
		t.Fatalf("unindexed cass should ask for an index, footer=%q", footer)
	}
	if footer := footerAfter(t, t.TempDir(), "0"); strings.Contains(footer, "cass") {
		t.Fatalf("no cass on PATH must show nothing, footer=%q", footer)
	}
}

// TestModel_InitSkipsCassProbeInTestMode: BV_TEST_MODE (set by TestMain for
// every other test) must keep Init from executing cass.
func TestModel_InitSkipsCassProbeInTestMode(t *testing.T) {
	t.Setenv("BV_TEST_MODE", "1")
	t.Setenv("BV_NO_UPDATE_CHECK", "1")
	issues := []model.Issue{{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask}}
	m := NewModel(issues, nil, "")
	m.analysis = nil
	n := 0
	if cmd := m.Init(); cmd != nil {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			n = len(batch)
		} else {
			n = 1
		}
	}
	t.Setenv("BV_TEST_MODE", "")
	m2 := NewModel(issues, nil, "")
	m2.analysis = nil
	n2 := 0
	if cmd := m2.Init(); cmd != nil {
		if batch, ok := cmd().(tea.BatchMsg); ok {
			n2 = len(batch)
		} else {
			n2 = 1
		}
	}
	if n2 != n+1 {
		t.Fatalf("Init should queue exactly one extra command (the cass probe) outside test mode: test=%d normal=%d", n, n2)
	}
}

// TestModel_VUsesFocusedSelection (E4): the session lookup follows the
// focused view's selection, and V is reachable from the board, tree, and
// history views (a missing cass turns into a status message, not silence).
func TestModel_VUsesFocusedSelection(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cass anywhere
	issues := []model.Issue{
		{ID: "A", Title: "Issue A", Status: model.StatusOpen, Priority: 1, IssueType: model.TypeTask},
		{ID: "B", Title: "Issue B", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask},
		{ID: "C", Title: "Issue C", Status: model.StatusInProgress, Priority: 2, IssueType: model.TypeTask},
	}
	m := NewModel(issues, nil, "")
	m.width, m.height = 120, 40

	// List: the list item.
	m.focused = focusList
	m.list.Select(1)
	if got := m.focusedIssueForSessions(); got == nil || got.ID != issues[1].ID && got.ID != m.list.Items()[1].(IssueItem).Issue.ID {
		t.Fatalf("list selection: got %+v", got)
	}

	// Board: the selected card.
	m.board = NewBoardModel(issues, m.theme)
	m.board.MoveDown()
	m.focused = focusBoard
	want := m.board.SelectedIssue()
	if want == nil {
		t.Fatalf("board fixture has no selection")
	}
	if got := m.focusedIssueForSessions(); got == nil || got.ID != want.ID {
		t.Fatalf("board selection: got %+v want %s", got, want.ID)
	}
	m.isBoardView = true
	updated, lookup := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("V")})
	got := completeCassLookup(t, asModelPtr(t, updated), lookup)
	if !strings.Contains(got.statusMsg, "cass not available") {
		t.Fatalf("V from the board should reach the session lookup and report the missing cass, status=%q", got.statusMsg)
	}

	// History: the selected bead row (fixture from history_test.go).
	m.historyView = NewHistoryModel(createTestHistoryReport(), m.theme)
	m.focused = focusHistory
	id := m.historyView.SelectedBeadID()
	if id == "" {
		t.Fatalf("history fixture has no selection")
	}
	m.issues = append(m.issues, model.Issue{ID: id, Title: "from history", Status: model.StatusOpen, Priority: 2, IssueType: model.TypeTask})
	if got := m.focusedIssueForSessions(); got == nil || got.ID != id {
		t.Fatalf("history selection: got %+v want %s", got, id)
	}

	// Tree: the selected node.
	m.focused = focusTree
	if want := m.tree.SelectedIssue(); want != nil {
		if got := m.focusedIssueForSessions(); got == nil || got.ID != want.ID {
			t.Fatalf("tree selection: got %+v want %s", got, want.ID)
		}
	}
}
