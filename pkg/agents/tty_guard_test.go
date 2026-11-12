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
