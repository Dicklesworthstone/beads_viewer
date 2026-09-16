//go:build windows

package ui

import (
	"os"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// backgroundQueryBudget bounds the whole OSC 11 exchange. A terminal that is
// going to answer does so immediately; anything slower is treated as "no
// answer" so startup is never held up by a console that ignores the query.
const backgroundQueryBudget = 300 * time.Millisecond

// platformHasDarkBackground asks the terminal for its background colour with
// OSC 11, covering the gap described in bgquery.go.
//
// Only Windows Terminal is asked (WT_SESSION): it is the Windows console host
// known to answer, and legacy conhost neither answers nor reports that it
// cannot, so probing it would spend the budget on every start for nothing.
// Set BV_NO_BG_QUERY=1 to skip the probe entirely.
//
// Every failure path returns ok=false, which leaves the caller on termenv's
// existing answer, so this can only improve detection, never replace a working
// one with a worse guess.
func platformHasDarkBackground() (isDark bool, ok bool) {
	if env.NoBackgroundQuery.Get() != "" || os.Getenv("WT_SESSION") == "" {
		return false, false
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false, false
	}

	stdin := windows.Handle(os.Stdin.Fd())
	var savedMode uint32
	if err := windows.GetConsoleMode(stdin, &savedMode); err != nil {
		return false, false
	}
	// The reply has to arrive as bytes rather than line-buffered, echoed input,
	// so ask for VT input and drop the cooked-mode flags for the exchange.
	// Mouse and window events are dropped too: they would signal the input
	// handle without producing any VT bytes, leaving the read below waiting on
	// a terminal that has already said everything it is going to say.
	queryMode := savedMode &^ (windows.ENABLE_ECHO_INPUT |
		windows.ENABLE_LINE_INPUT |
		windows.ENABLE_PROCESSED_INPUT |
		windows.ENABLE_MOUSE_INPUT |
		windows.ENABLE_WINDOW_INPUT)
	queryMode |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(stdin, queryMode); err != nil {
		return false, false
	}
	defer windows.SetConsoleMode(stdin, savedMode) //nolint:errcheck // restoring best effort

	if _, err := os.Stdout.WriteString("\x1b]11;?\x07"); err != nil {
		return false, false
	}
	return parseOSCBackgroundIsDark(readOSCReply(stdin, backgroundQueryBudget))
}

// readOSCReply collects an OSC reply within budget.
//
// It waits on the console handle before every read and takes one byte at a
// time, so it neither blocks past the deadline nor buffers beyond the reply's
// terminator. Reading ahead would swallow whatever the user typed next, which
// matters because this runs on the real stdin that the TUI is about to take
// over.
func readOSCReply(stdin windows.Handle, budget time.Duration) string {
	deadline := time.Now().Add(budget)
	var reply strings.Builder
	buf := make([]byte, 1)

	for reply.Len() < 64 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait, err := windows.WaitForSingleObject(stdin, uint32(remaining.Milliseconds()))
		if err != nil || wait != windows.WAIT_OBJECT_0 {
			break
		}
		read, err := os.Stdin.Read(buf)
		if err != nil || read == 0 {
			break
		}
		if buf[0] == '\x07' { // BEL terminates the reply
			break
		}
		reply.WriteByte(buf[0])
		// ST (ESC \) is the other terminator; the ESC is already stored, so
		// stop once its backslash arrives.
		if buf[0] == '\\' && strings.HasSuffix(reply.String(), "\x1b\\") {
			break
		}
	}
	return reply.String()
}
