package agents

import "testing"

func TestShouldSuppressTTYQueries_EnvRobot(t *testing.T) {
	if !shouldSuppressTTYQueries([]string{"bv"}, true, false) {
		t.Fatal("expected envRobot=true to suppress TTY queries")
	}
}

func TestShouldSuppressTTYQueries_EnvTest(t *testing.T) {
	if !shouldSuppressTTYQueries([]string{"bv"}, false, true) {
		t.Fatal("expected envTest=true to suppress TTY queries")
	}
}

func TestShouldSuppressTTYQueries_RobotFlag(t *testing.T) {
	if !shouldSuppressTTYQueries([]string{"bv", "--robot-triage"}, false, false) {
		t.Fatal("expected --robot-triage to suppress TTY queries")
	}
	if !shouldSuppressTTYQueries([]string{"bv", "--robot-file-beads=path/to/file.go"}, false, false) {
		t.Fatal("expected --robot-file-beads=... to suppress TTY queries")
	}
}

func TestShouldSuppressTTYQueries_HelpAndVersion(t *testing.T) {
	if !shouldSuppressTTYQueries([]string{"bv", "--help"}, false, false) {
		t.Fatal("expected --help to suppress TTY queries")
	}
	if !shouldSuppressTTYQueries([]string{"bv", "--version"}, false, false) {
		t.Fatal("expected --version to suppress TTY queries")
	}
}

func TestShouldSuppressTTYQueries_TUIInvocation(t *testing.T) {
	// Common TUI entry: no args, or args that still launch the TUI.
	if shouldSuppressTTYQueries([]string{"bv"}, false, false) {
		t.Fatal("did not expect plain TUI invocation to suppress TTY queries")
	}
	if shouldSuppressTTYQueries([]string{"bv", "--recipe", "triage"}, false, false) {
		t.Fatal("did not expect --recipe triage (TUI) to suppress TTY queries")
	}
}

func TestShouldSuppressTTYQueries_HeadlessHistoryAndDriftFlags(t *testing.T) {
	cases := [][]string{
		{"bv", "--bead-history", "bv-1"},
		{"bv", "--bead-history=bv-1"},
		{"bv", "--diff-since", "HEAD~1"},
		{"bv", "--diff-since=HEAD~1"},
		{"bv", "--check-drift"},
		{"bv", "--save-baseline", "sprint start"},
		{"bv", "--baseline-info"},
	}
	for _, argv := range cases {
		if !shouldSuppressTTYQueries(argv, false, false) {
			t.Fatalf("expected headless CLI invocation %v to suppress TTY queries", argv)
		}
	}
}

func TestShouldSuppressTTYQueries_AllCanonicalRobotAndHeadlessFlags(t *testing.T) {
	robotFlags := CanonicalRobotFlagNames()
	if len(robotFlags) < 40 {
		t.Fatalf("expected >= 40 canonical robot flags, got %d", len(robotFlags))
	}
	for _, name := range robotFlags {
		if !shouldSuppressTTYQueries([]string{"bv", "--" + name}, false, false) {
			t.Fatalf("expected canonical robot flag --%s to suppress TTY queries", name)
		}
		if !shouldSuppressTTYQueries([]string{"bv", "--" + name + "=arg"}, false, false) {
			t.Fatalf("expected canonical robot flag --%s=arg to suppress TTY queries", name)
		}
	}
	for _, name := range CanonicalHeadlessFlagNames() {
		if !shouldSuppressTTYQueries([]string{"bv", "--" + name}, false, false) {
			t.Fatalf("expected canonical headless flag --%s to suppress TTY queries", name)
		}
		if !shouldSuppressTTYQueries([]string{"bv", "--" + name + "=arg"}, false, false) {
			t.Fatalf("expected canonical headless flag --%s=arg to suppress TTY queries", name)
		}
	}
}

func TestShouldSuppressTTYQueries_PositionalAndStructuredFormatForms(t *testing.T) {
	suppressCases := [][]string{
		{"bv", "robot-triage", "--json"},
		{"bv", "robot-commit", "HEAD", "--json"},
		{"bv", "triage", "--json"},
		{"bv", "next"},
		{"bv", "plan"},
		{"bv", "insights"},
		{"bv", "commit", "HEAD", "--json"},
		{"bv", "drift"},
		{"bv", "check-dep", "bv-2:bv-1"},
		{"bv", "bead-history", "bv-1"},
		{"bv", "upgrade", "--check"},
		{"bv", "-w", "--robot-triage"},
		{"bv", "--beads-dir", "--robot-triage"},
		{"bv", "--robot-ignore-correlation", "abc123:bv-1"},
		{"bv", "-robot-triage"},
		{"bv", "-h"},
		{"bv", "-v"},
		{"bv", "--json"},
		{"bv", "--json=true"},
		{"bv", "--json=false"},
		{"bv", "--json=0"},
		{"bv", "--toon"},
		{"bv", "--toon=true"},
		{"bv", "--toon=false"},
		{"bv", "--toon=0"},
		{"bv", "--format", "json"},
		{"bv", "--format=toon"},
		{"bv", "-f", "json"},
		{"bv", "-f=toon"},
		{"bv", "--agents-dry-run"},
		{"bv", "--agents-force"},
		{"bv", "--output", "json"},
		{"bv", "--output=toon"},
		{"bv", "-o", "json"},
		{"bv", "-o=toon"},
	}
	for _, argv := range suppressCases {
		if !shouldSuppressTTYQueries(argv, false, false) {
			t.Fatalf("expected %v to suppress TTY queries", argv)
		}
	}

	tuiCases := [][]string{
		{"bv"},
		{"bv", "--recipe", "triage"},
		{"bv", "-r", "actionable"},
		{"bv", "--label", "-v"},
		{"bv", "--label", "--json"},
		{"bv", "--json=bogus"},
		{"bv", "--robot-triage=false"},
		{"bv", "--version=false"},
		{"bv", "--workspace", ".bv/workspace.yaml"},
		{"bv", "--as-of", "HEAD~1"},
		{"bv", "--", "--robot-triage"},
		{"bv", "--output", "report.md"},
		{"bv", "--id-pattern", "-v"},
		{"bv", "--search-mode", "-h"},
		{"bv", "--history-since", "-v"},
	}
	for _, argv := range tuiCases {
		if shouldSuppressTTYQueries(argv, false, false) {
			t.Fatalf("did not expect TUI/file argv %v to suppress TTY queries", argv)
		}
	}
}
