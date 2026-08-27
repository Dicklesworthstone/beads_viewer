package correlation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFindCommitsInWindowPropagatesInFlightCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX executable script")
	}

	fakeBin := t.TempDir()
	gitPath := filepath.Join(fakeBin, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o755); err != nil {
		t.Fatalf("write blocking git fixture: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	correlator := NewTemporalCorrelator(t.TempDir()).WithContext(ctx)
	_, err := correlator.FindCommitsInWindow(TemporalWindow{
		AuthorEmail: "dev@example.com",
		Start:       time.Unix(1, 0).UTC(),
		End:         time.Unix(2, 0).UTC(),
	})
	if err == nil {
		t.Fatal("FindCommitsInWindow returned success after its git process was canceled")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FindCommitsInWindow error = %v, want context deadline exceeded", err)
	}

	claimed := &BeadEvent{Timestamp: time.Unix(1, 0).UTC(), AuthorEmail: "dev@example.com"}
	closed := &BeadEvent{Timestamp: time.Unix(2, 0).UTC()}
	_, err = correlator.ExtractAllTemporalCorrelations(map[string]BeadHistory{
		"bv-42": {Milestones: BeadMilestones{Claimed: claimed, Closed: closed}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExtractAllTemporalCorrelations error = %v, want context deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "bv-42") {
		t.Fatalf("ExtractAllTemporalCorrelations error = %v, want bead context", err)
	}
}

func TestFindCommitsInWindowPropagatesCancellationDuringFileExtraction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX executable script")
	}

	fakeBin := t.TempDir()
	gitPath := filepath.Join(fakeBin, "git")
	script := `#!/bin/sh
case " $* " in
  *" log "*)
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\0002025-12-15T10:30:00Z\000Dev\000dev@example.com\000work\n'
    ;;
  *" --no-renames "*)
    exit 0
    ;;
  *" rev-parse "*" --is-shallow-repository "*)
    printf 'false\n'
    ;;
  *" show "*)
    while :; do :; done
    ;;
  *)
    exit 64
    ;;
esac
`
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write staged git fixture: %v", err)
	}
	t.Setenv("PATH", fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewTemporalCorrelator(t.TempDir()).WithContext(ctx).FindCommitsInWindow(TemporalWindow{
		BeadID:      "bv-42",
		AuthorEmail: "dev@example.com",
		Start:       time.Unix(1, 0).UTC(),
		End:         time.Unix(2_000_000_000, 0).UTC(),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("file-extraction cancellation error = %v, want context deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), strings.Repeat("a", 40)) {
		t.Fatalf("file-extraction cancellation error = %v, want commit context", err)
	}
}

func TestFindCommitsInWindowPropagatesBeadsProbeFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture uses a POSIX executable script")
	}

	for _, tc := range []struct {
		name     string
		showBody string
	}{
		{name: "git failure", showBody: "printf 'probe failed\\n' >&2; exit 23"},
		{name: "parse failure", showBody: "printf 'bogus\\000'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			gitPath := filepath.Join(fakeBin, "git")
			script := `#!/bin/sh
case " $* " in
  *" log "*)
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\0002025-12-15T10:30:00Z\000Dev\000dev@example.com\000work\n'
    ;;
  *" show "*)
    ` + tc.showBody + `
    ;;
  *)
    exit 64
    ;;
esac
`
			if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
				t.Fatalf("write failing git fixture: %v", err)
			}
			t.Setenv("PATH", fakeBin)

			_, err := NewTemporalCorrelator(t.TempDir()).FindCommitsInWindow(TemporalWindow{
				BeadID:      "bv-42",
				AuthorEmail: "dev@example.com",
				Start:       time.Unix(1, 0).UTC(),
				End:         time.Unix(2_000_000_000, 0).UTC(),
			})
			if err == nil {
				t.Fatal("FindCommitsInWindow returned success after Beads probe failure")
			}
			if !strings.Contains(err.Error(), "checking Beads-file changes") || !strings.Contains(err.Error(), strings.Repeat("a", 40)) {
				t.Fatalf("Beads probe error = %v, want operation and commit context", err)
			}
		})
	}
}

func TestExtractAllTemporalCorrelationsHasStableBeadOrder(t *testing.T) {
	repo := initTempGitRepo(t)
	now := time.Now()
	histories := make(map[string]BeadHistory)
	for _, beadID := range []string{"bv-03", "bv-01", "bv-02"} {
		histories[beadID] = BeadHistory{
			Title: beadID,
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{Timestamp: now.Add(-time.Hour), Author: "Test User", AuthorEmail: "test@example.com"},
				Closed:  &BeadEvent{Timestamp: now.Add(time.Hour)},
			},
		}
	}

	commits, err := NewTemporalCorrelator(repo).ExtractAllTemporalCorrelations(histories)
	if err != nil {
		t.Fatalf("ExtractAllTemporalCorrelations: %v", err)
	}
	want := []string{"bv-01", "bv-02", "bv-03"}
	if len(commits) != len(want) {
		t.Fatalf("len(commits) = %d, want %d: %+v", len(commits), len(want), commits)
	}
	for i, commit := range commits {
		if commit.BeadID != want[i] {
			t.Fatalf("commit %d BeadID = %q, want %q", i, commit.BeadID, want[i])
		}
	}
}

func TestTemporalTouchesBeadsFileUsesInvariantNULSafeDiff(t *testing.T) {
	repo := initTempGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o755); err != nil {
		t.Fatalf("create beads directory: %v", err)
	}
	oldRelative := ".beads/odd\nname.jsonl"
	newRelative := "archive/odd\nname.jsonl"
	if err := os.WriteFile(filepath.Join(repo, oldRelative), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write odd-path beads fixture: %v", err)
	}
	runGit(t, repo, "add", oldRelative)
	runGit(t, repo, "commit", "-m", "add odd-path beads fixture")
	if err := os.MkdirAll(filepath.Join(repo, "archive"), 0o755); err != nil {
		t.Fatalf("create archive directory: %v", err)
	}
	runGit(t, repo, "mv", oldRelative, newRelative)
	runGit(t, repo, "commit", "-m", "move odd-path beads fixture")
	sha, err := getGitHead(repo)
	if err != nil {
		t.Fatalf("resolve rename commit: %v", err)
	}

	// These settings alter the legacy newline-delimited/name-only output, but
	// the production probe pins its own policy and forces rename pre/post images.
	runGit(t, repo, "config", "core.quotePath", "false")
	runGit(t, repo, "config", "diff.renames", "true")
	touches, err := NewTemporalCorrelator(repo).touchesBeadsFile(sha)
	if err != nil {
		t.Fatalf("touchesBeadsFile: %v", err)
	}
	if !touches {
		t.Fatal("rename out of .beads with a newline path was not recognized as a Beads-file commit")
	}
}

func TestExtractPathHints(t *testing.T) {
	tests := []struct {
		title string
		want  []string
	}{
		{
			title: "Fix authentication bug in pkg/auth",
			want:  []string{"pkg/auth"}, // "auth" in "authentication" is not a word boundary match
		},
		{
			title: "Update user login flow",
			want:  []string{"user", "login"},
		},
		{
			title: "Refactor database connection handler",
			want:  []string{"database", "handler"},
		},
		{
			title: "Add tests for api service",
			want:  []string{"tests", "api", "service"}, // "tests" is now a keyword
		},
		{
			title: "internal/config improvements",
			want:  []string{"internal/config"}, // Only the path, not "config" separately
		},
		{
			title: "Simple title with no hints",
			want:  nil,
		},
		{
			title: "",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := extractPathHints(tt.title)

			if tt.want == nil {
				if got != nil {
					t.Errorf("extractPathHints(%q) = %v, want nil", tt.title, got)
				}
				return
			}

			// Check that all expected hints are present
			for _, w := range tt.want {
				found := false
				for _, g := range got {
					if g == w {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("extractPathHints(%q) missing expected hint %q, got %v", tt.title, w, got)
				}
			}
		})
	}
}

func TestPathsMatchHints(t *testing.T) {
	tests := []struct {
		name  string
		files []FileChange
		hints []string
		want  bool
	}{
		{
			name: "match in path",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
			},
			hints: []string{"auth"},
			want:  true,
		},
		{
			name: "match in nested path",
			files: []FileChange{
				{Path: "internal/service/user/handler.go"},
			},
			hints: []string{"user"},
			want:  true,
		},
		{
			name: "no match",
			files: []FileChange{
				{Path: "pkg/billing/invoice.go"},
			},
			hints: []string{"auth", "login"},
			want:  false,
		},
		{
			name: "case insensitive",
			files: []FileChange{
				{Path: "pkg/AUTH/Login.go"},
			},
			hints: []string{"auth"},
			want:  true,
		},
		{
			name:  "empty files",
			files: []FileChange{},
			hints: []string{"auth"},
			want:  false,
		},
		{
			name: "empty hints",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
			},
			hints: []string{},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathsMatchHints(tt.files, tt.hints)
			if (got && !tt.want) || (!got && tt.want) {
				t.Errorf("pathsMatchHints() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitAuthorRegexEscapesEmailMetacharacters(t *testing.T) {
	email := "dev+triage@example.com"
	got := gitAuthorRegex("Dev Triage", email)

	if strings.Compare(got, email) == 0 {
		t.Fatalf("gitAuthorRegex(%q) was not escaped", email)
	}

	re, err := regexp.Compile("^" + got + "$")
	if err != nil {
		t.Fatalf("escaped author regex did not compile: %v", err)
	}
	if !re.MatchString(email) {
		t.Fatalf("escaped author regex %q did not match original email", got)
	}
	if re.MatchString("devvtriage@examplexcom") {
		t.Fatalf("escaped author regex %q matched a regex-expanded email", got)
	}
}

func TestGitAuthorRegexFallsBackToAuthorName(t *testing.T) {
	got := gitAuthorRegex(" Dev+Triage ", "")
	if len(got) < 1 {
		t.Fatal("expected author-name fallback regex")
	}

	re, err := regexp.Compile("^" + got + "$")
	if err != nil {
		t.Fatalf("author-name fallback regex did not compile: %v", err)
	}
	if !re.MatchString("Dev+Triage") {
		t.Fatalf("author-name fallback regex %q did not match trimmed author name", got)
	}
	if re.MatchString("DevvTriage") {
		t.Fatalf("author-name fallback regex %q matched a regex-expanded author name", got)
	}
}

func TestGitAuthorRegexEmptyIdentity(t *testing.T) {
	if got := gitAuthorRegex(" ", " "); len(got) > 0 {
		t.Fatalf("gitAuthorRegex returned %q for empty author identity", got)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct {
		value, min, max, want float64
	}{
		{0.5, 0.0, 1.0, 0.5},   // Within range
		{-0.5, 0.0, 1.0, 0.0},  // Below min
		{1.5, 0.0, 1.0, 1.0},   // Above max
		{0.0, 0.0, 1.0, 0.0},   // At min
		{1.0, 0.0, 1.0, 1.0},   // At max
		{0.5, 0.2, 0.85, 0.5},  // Within temporal range
		{0.1, 0.2, 0.85, 0.2},  // Below temporal min
		{0.9, 0.2, 0.85, 0.85}, // Above temporal max
	}

	for _, tt := range tests {
		got := clamp(tt.value, tt.min, tt.max)
		if got < tt.want || got > tt.want {
			t.Errorf("clamp(%v, %v, %v) = %v, want %v", tt.value, tt.min, tt.max, got, tt.want)
		}
	}
}

func TestCalculateTemporalConfidence(t *testing.T) {
	tc := NewTemporalCorrelator("/test/repo")
	now := time.Now()

	tests := []struct {
		name       string
		window     TemporalWindow
		files      []FileChange
		pathHints  []string
		authActive map[string]int
		wantRange  [2]float64
	}{
		{
			name: "base case - single bead, moderate window",
			window: TemporalWindow{
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-12 * time.Hour),
				End:         now,
			},
			files:      []FileChange{{Path: "file.go"}},
			pathHints:  nil,
			authActive: map[string]int{"dev@test.com": 1},
			wantRange:  [2]float64{0.65, 0.85}, // base 0.50 + 0.20 (single bead) + 0.05 (moderate window)
		},
		{
			name: "short window boost",
			window: TemporalWindow{
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-2 * time.Hour),
				End:         now,
			},
			files:      []FileChange{{Path: "file.go"}},
			pathHints:  nil,
			authActive: map[string]int{"dev@test.com": 1},
			wantRange:  [2]float64{0.75, 0.85}, // base 0.50 + 0.20 (single bead) + 0.10 (short window)
		},
		{
			name: "long window penalty",
			window: TemporalWindow{
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-10 * 24 * time.Hour),
				End:         now,
			},
			files:      []FileChange{{Path: "file.go"}},
			pathHints:  nil,
			authActive: map[string]int{"dev@test.com": 1},
			wantRange:  [2]float64{0.50, 0.60}, // base 0.50 + 0.20 (single bead) - 0.15 (long window)
		},
		{
			name: "many beads penalty",
			window: TemporalWindow{
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-12 * time.Hour),
				End:         now,
			},
			files:      []FileChange{{Path: "file.go"}},
			pathHints:  nil,
			authActive: map[string]int{"dev@test.com": 5},
			wantRange:  [2]float64{0.40, 0.50}, // base 0.50 - 0.10 (many beads) + 0.05 (moderate window)
		},
		{
			name: "path hint match boost",
			window: TemporalWindow{
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-12 * time.Hour),
				End:         now,
			},
			files:      []FileChange{{Path: "pkg/auth/login.go"}},
			pathHints:  []string{"auth"},
			authActive: map[string]int{"dev@test.com": 2},
			wantRange:  [2]float64{0.70, 0.85}, // base 0.50 + 0.10 (2 beads) + 0.05 (moderate) + 0.15 (path match)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc.activeByAuth = tt.authActive
			got := tc.calculateTemporalConfidence(tt.window, tt.files, tt.pathHints)
			if got < tt.wantRange[0] || got > tt.wantRange[1] {
				t.Errorf("calculateTemporalConfidence() = %v, want in range [%v, %v]", got, tt.wantRange[0], tt.wantRange[1])
			}
		})
	}
}

func TestGenerateTemporalReason(t *testing.T) {
	tc := NewTemporalCorrelator("/test/repo")
	now := time.Now()

	tests := []struct {
		name           string
		window         TemporalWindow
		files          []FileChange
		pathHints      []string
		authActive     map[string]int
		expectContains []string
	}{
		{
			name: "basic reason",
			window: TemporalWindow{
				Author:      "Test Dev",
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-12 * time.Hour),
				End:         now,
			},
			files:          []FileChange{{Path: "file.go"}},
			authActive:     map[string]int{"dev@test.com": 2},
			expectContains: []string{"Commit by Test Dev", "active window"},
		},
		{
			name: "short window",
			window: TemporalWindow{
				Author:      "Test Dev",
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-2 * time.Hour),
				End:         now,
			},
			files:          []FileChange{{Path: "file.go"}},
			authActive:     map[string]int{"dev@test.com": 1},
			expectContains: []string{"short window", "only this bead active"},
		},
		{
			name: "long window with many beads",
			window: TemporalWindow{
				Author:      "Test Dev",
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-10 * 24 * time.Hour),
				End:         now,
			},
			files:          []FileChange{{Path: "file.go"}},
			authActive:     map[string]int{"dev@test.com": 5},
			expectContains: []string{"long window", "5 beads active"},
		},
		{
			name: "path hint match",
			window: TemporalWindow{
				Author:      "Test Dev",
				AuthorEmail: "dev@test.com",
				Start:       now.Add(-12 * time.Hour),
				End:         now,
			},
			files:          []FileChange{{Path: "pkg/auth/login.go"}},
			pathHints:      []string{"auth"},
			authActive:     map[string]int{"dev@test.com": 1},
			expectContains: []string{"file paths match bead title keywords"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc.activeByAuth = tt.authActive
			_ = tc.calculateTemporalConfidence(tt.window, tt.files, tt.pathHints) // Ensure confidence calc works
			got := tc.generateTemporalReason(tt.window, tt.files, tt.pathHints)

			for _, exp := range tt.expectContains {
				if !strings.Contains(got, exp) {
					t.Errorf("generateTemporalReason() = %q, expected to contain %q", got, exp)
				}
			}
		})
	}
}

func TestExtractWindowFromMilestones(t *testing.T) {
	now := time.Now()
	oneHourAgo := now.Add(-time.Hour)

	claimedEvent := &BeadEvent{
		BeadID:      "bv-123",
		EventType:   EventClaimed,
		Timestamp:   oneHourAgo,
		Author:      "Test Dev",
		AuthorEmail: "dev@test.com",
	}

	closedEvent := &BeadEvent{
		BeadID:      "bv-123",
		EventType:   EventClosed,
		Timestamp:   now,
		Author:      "Test Dev",
		AuthorEmail: "dev@test.com",
	}

	tests := []struct {
		name       string
		beadID     string
		title      string
		milestones BeadMilestones
		wantNil    bool
	}{
		{
			name:   "valid window",
			beadID: "bv-123",
			title:  "Fix auth bug",
			milestones: BeadMilestones{
				Claimed: claimedEvent,
				Closed:  closedEvent,
			},
			wantNil: false,
		},
		{
			name:   "missing claimed",
			beadID: "bv-123",
			title:  "Fix auth bug",
			milestones: BeadMilestones{
				Closed: closedEvent,
			},
			wantNil: true,
		},
		{
			name:   "missing closed",
			beadID: "bv-123",
			title:  "Fix auth bug",
			milestones: BeadMilestones{
				Claimed: claimedEvent,
			},
			wantNil: true,
		},
		{
			name:       "empty milestones",
			beadID:     "bv-123",
			title:      "Fix auth bug",
			milestones: BeadMilestones{},
			wantNil:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractWindowFromMilestones(tt.beadID, tt.title, tt.milestones)

			if tt.wantNil {
				if got != nil {
					t.Errorf("ExtractWindowFromMilestones() = %v, want nil", got)
				}
				return
			}

			if got == nil {
				t.Fatal("ExtractWindowFromMilestones() = nil, want non-nil")
			}

			if strings.Compare(got.BeadID, tt.beadID) != 0 {
				t.Errorf("BeadID = %q, want %q", got.BeadID, tt.beadID)
			}
			if strings.Compare(got.Title, tt.title) != 0 {
				t.Errorf("Title = %q, want %q", got.Title, tt.title)
			}
			if strings.Compare(got.Author, claimedEvent.Author) != 0 {
				t.Errorf("Author = %q, want %q", got.Author, claimedEvent.Author)
			}
			if !got.Start.Equal(claimedEvent.Timestamp) {
				t.Errorf("Start = %v, want %v", got.Start, claimedEvent.Timestamp)
			}
			if !got.End.Equal(closedEvent.Timestamp) {
				t.Errorf("End = %v, want %v", got.End, closedEvent.Timestamp)
			}
		})
	}
}

func TestExtractWindowFromMilestonesUsesLatestContinuousInterval(t *testing.T) {
	claimed := &BeadEvent{Timestamp: time.Unix(10, 0).UTC(), Author: "Dev", AuthorEmail: "dev@example.com"}
	reopened := &BeadEvent{Timestamp: time.Unix(30, 0).UTC()}
	closed := &BeadEvent{Timestamp: time.Unix(40, 0).UTC()}
	window := ExtractWindowFromMilestones("bv-reopened", "Reopened", BeadMilestones{
		Claimed: claimed, Reopened: reopened, Closed: closed,
	})
	if window == nil || !window.Start.Equal(reopened.Timestamp) || !window.End.Equal(closed.Timestamp) {
		t.Fatalf("reopened window = %+v, want %v..%v", window, reopened.Timestamp, closed.Timestamp)
	}

	inverted := ExtractWindowFromMilestones("bv-still-open", "Still open", BeadMilestones{
		Claimed:  claimed,
		Closed:   &BeadEvent{Timestamp: time.Unix(20, 0).UTC()},
		Reopened: reopened,
	})
	if inverted != nil {
		t.Fatalf("close before latest reopen produced inverted completed window: %+v", inverted)
	}
}

func TestExtractTemporalWindowFromHistoryUsesLatestActivationIdentity(t *testing.T) {
	claimA := BeadEvent{EventType: EventClaimed, Timestamp: time.Unix(10, 0).UTC(), Author: "Dev A", AuthorEmail: "a@example.com"}
	firstClose := BeadEvent{EventType: EventClosed, Timestamp: time.Unix(20, 0).UTC()}
	reopenB := BeadEvent{EventType: EventReopened, Timestamp: time.Unix(30, 0).UTC(), Author: "Dev B", AuthorEmail: "b@example.com"}
	claimC := BeadEvent{EventType: EventClaimed, Timestamp: time.Unix(35, 0).UTC(), Author: "Dev C", AuthorEmail: "c@example.com"}
	finalClose := BeadEvent{EventType: EventClosed, Timestamp: time.Unix(40, 0).UTC()}

	for _, tc := range []struct {
		name       string
		events     []BeadEvent
		wantStart  time.Time
		wantAuthor string
		wantEmail  string
	}{
		{
			name:       "reopen author replaces original claimant",
			events:     []BeadEvent{claimA, firstClose, reopenB, finalClose},
			wantStart:  reopenB.Timestamp,
			wantAuthor: reopenB.Author,
			wantEmail:  reopenB.AuthorEmail,
		},
		{
			name:       "claim after reopen starts actual work interval",
			events:     []BeadEvent{claimA, firstClose, reopenB, claimC, finalClose},
			wantStart:  claimC.Timestamp,
			wantAuthor: claimC.Author,
			wantEmail:  claimC.AuthorEmail,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := BeadHistory{
				Title:  "Reassigned work",
				Events: tc.events,
				// Keep the intentionally lossy summary present to prove the
				// production helper uses Events rather than the first claim.
				Milestones: BeadMilestones{Claimed: &claimA, Reopened: &reopenB, Closed: &finalClose},
			}
			window := extractTemporalWindowFromHistory("bv-reassigned", history)
			if window == nil {
				t.Fatal("history produced no completed temporal window")
			}
			if !window.Start.Equal(tc.wantStart) || !window.End.Equal(finalClose.Timestamp) || window.Author != tc.wantAuthor || window.AuthorEmail != tc.wantEmail {
				t.Fatalf("window = %+v, want %v..%v owned by %s <%s>", window, tc.wantStart, finalClose.Timestamp, tc.wantAuthor, tc.wantEmail)
			}
		})
	}
}

func TestConcurrentTemporalBeadsUseCurrentIntervalIdentity(t *testing.T) {
	target := TemporalWindow{
		BeadID:      "target",
		Author:      "Dev C",
		AuthorEmail: "c@example.com",
		Start:       time.Unix(35, 0).UTC(),
		End:         time.Unix(40, 0).UTC(),
	}
	event := func(kind EventType, second int64, author, email string) BeadEvent {
		return BeadEvent{EventType: kind, Timestamp: time.Unix(second, 0).UTC(), Author: author, AuthorEmail: email}
	}
	histories := map[string]BeadHistory{
		"target": {Events: []BeadEvent{
			event(EventClaimed, 35, "Dev C", "c@example.com"),
			event(EventClosed, 40, "", ""),
		}},
		"current-same-author": {Events: []BeadEvent{
			event(EventClaimed, 5, "Dev A", "a@example.com"),
			event(EventClosed, 10, "", ""),
			event(EventReopened, 36, "Dev C", "c@example.com"),
		}},
		"stale-same-author": {Events: []BeadEvent{
			event(EventClaimed, 5, "Dev C", "c@example.com"),
			event(EventClosed, 10, "", ""),
			event(EventReopened, 36, "Dev D", "d@example.com"),
		}},
	}
	if got := countConcurrentTemporalBeads(histories, target); got != 2 {
		t.Fatalf("concurrent beads = %d, want target plus current-same-author only", got)
	}
}

func TestSetSeenCommits(t *testing.T) {
	tc := NewTemporalCorrelator("/test/repo")

	commits := []CorrelatedCommit{
		{SHA: "abc123"},
		{SHA: "def456"},
		{SHA: "ghi789"},
	}

	tc.SetSeenCommits(commits)

	for _, c := range commits {
		if !tc.seenCommits[c.SHA] {
			t.Errorf("SetSeenCommits() did not mark %q as seen", c.SHA)
		}
	}

	if tc.seenCommits["unknown"] {
		t.Error("SetSeenCommits() incorrectly marked unknown SHA as seen")
	}
}

func TestSetActiveBeadsPerAuthor(t *testing.T) {
	tc := NewTemporalCorrelator("/test/repo")

	counts := map[string]int{
		"dev1@test.com": 3,
		"dev2@test.com": 1,
	}

	tc.SetActiveBeadsPerAuthor(counts)

	if tc.activeByAuth["dev1@test.com"] != 3 {
		t.Errorf("activeByAuth[dev1] = %d, want 3", tc.activeByAuth["dev1@test.com"])
	}
	if tc.activeByAuth["dev2@test.com"] != 1 {
		t.Errorf("activeByAuth[dev2] = %d, want 1", tc.activeByAuth["dev2@test.com"])
	}
}

func TestCalculateActiveBeadsPerAuthor(t *testing.T) {
	tc := NewTemporalCorrelator("/test/repo")

	now := time.Now()
	histories := map[string]BeadHistory{
		"bv-1": {
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{AuthorEmail: "dev1@test.com", Timestamp: now},
			},
		},
		"bv-2": {
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{AuthorEmail: "dev1@test.com", Timestamp: now},
			},
		},
		"bv-3": {
			Milestones: BeadMilestones{
				Claimed: &BeadEvent{AuthorEmail: "dev2@test.com", Timestamp: now},
			},
		},
		"bv-4": {
			Milestones: BeadMilestones{}, // No claimed event
		},
	}

	tc.calculateActiveBeadsPerAuthor(histories)

	if tc.activeByAuth["dev1@test.com"] != 2 {
		t.Errorf("activeByAuth[dev1] = %d, want 2", tc.activeByAuth["dev1@test.com"])
	}
	if tc.activeByAuth["dev2@test.com"] != 1 {
		t.Errorf("activeByAuth[dev2] = %d, want 1", tc.activeByAuth["dev2@test.com"])
	}
}
