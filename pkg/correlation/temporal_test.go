package correlation

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

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

func TestTemporalWindows_OpenReopenedInstantaneousAndIdentityMatching(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)
	t3 := t0.Add(3 * time.Hour)
	latestCommit := t0.Add(5 * time.Hour)

	// 1. Open claimed window + Reopened claimed window (Claimed -> Closed -> Reopened) bounded by latestCommitTime.
	openMilestones := BeadMilestones{
		Claimed: &BeadEvent{BeadID: "bv-open", EventType: EventClaimed, Timestamp: t1, Author: "Alice", AuthorEmail: "ALICE@Example.com"},
	}
	if w := ExtractWindowFromMilestones("bv-open", "Open task", openMilestones); w != nil {
		t.Fatalf("expected nil window for open claimed bead without fallbackEnd, got %+v", w)
	}
	openWin := ExtractWindowFromMilestones("bv-open", "Open task", openMilestones, latestCommit)
	if openWin == nil || !openWin.Start.Equal(t1) || !openWin.End.Equal(latestCommit) {
		t.Fatalf("open claimed window = %+v, want [%s, %s]", openWin, t1, latestCommit)
	}

	reopenedMilestones := BeadMilestones{
		Claimed:  &BeadEvent{BeadID: "bv-reopen", EventType: EventClaimed, Timestamp: t0, Author: "Alice", AuthorEmail: "alice@example.com"},
		Closed:   &BeadEvent{BeadID: "bv-reopen", EventType: EventClosed, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
		Reopened: &BeadEvent{BeadID: "bv-reopen", EventType: EventReopened, Timestamp: t2, Author: "Alice", AuthorEmail: "alice@example.com"},
	}
	// Planted negative control: reopened bead without fallbackEnd must NOT truncate at old Closed (t1); returns nil without bound.
	if w := ExtractWindowFromMilestones("bv-reopen", "Reopened task", reopenedMilestones); w != nil {
		t.Fatalf("reopened claimed bead without fallbackEnd returned stale closed window %+v, want nil", w)
	}
	reopenWin := ExtractWindowFromMilestones("bv-reopen", "Reopened task", reopenedMilestones, latestCommit)
	if reopenWin == nil || !reopenWin.Start.Equal(t0) || !reopenWin.End.Equal(latestCommit) {
		t.Fatalf("reopened claimed window = %+v, want [%s, %s] (not old Closed %s)", reopenWin, t0, latestCommit, t1)
	}

	// 2. temporalWindowsFromEvents: open + reopened dilute ActiveBeads on closed windows without auto-linking open claims,
	//    instantaneous w.Start.Equal(w.End) (ActiveBeads >= 1), and case-insensitive email overlap.
	events := []BeadEvent{
		{BeadID: "bv-open", EventType: EventClaimed, Timestamp: t1, Author: "Alice", AuthorEmail: "ALICE@Example.com"},
		{BeadID: "bv-reopen", EventType: EventClaimed, Timestamp: t0, Author: "alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-reopen", EventType: EventClosed, Timestamp: t1, Author: "alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-reopen", EventType: EventReopened, Timestamp: t2, Author: "alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-alice-closed", EventType: EventClaimed, Timestamp: t2, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-alice-closed", EventType: EventClosed, Timestamp: latestCommit, Author: "Alice", AuthorEmail: "alice@example.com"},
		// Instantaneous window at t3 (Start == End == t3) for a distinct author with empty email:
		{BeadID: "bv-instant", EventType: EventClaimed, Timestamp: t3, Author: "SoloDev", AuthorEmail: ""},
		{BeadID: "bv-instant", EventType: EventClosed, Timestamp: t3, Author: "SoloDev", AuthorEmail: ""},
		// Another empty-email distinct author overlapping at t3 (planted negative: must NOT increment SoloDev's ActiveBeads):
		{BeadID: "bv-other-empty", EventType: EventClaimed, Timestamp: t0, Author: "OtherDev", AuthorEmail: ""},
		{BeadID: "bv-other-empty", EventType: EventClosed, Timestamp: latestCommit, Author: "OtherDev", AuthorEmail: ""},
	}
	windows := temporalWindowsFromEvents(events, "")
	byID := make(map[string]TemporalWindow, len(windows))
	for _, w := range windows {
		byID[w.BeadID] = w
	}

	if _, leaked := byID["bv-open"]; leaked {
		t.Fatalf("still-open bv-open must not emit automatic temporal window: %+v", byID["bv-open"])
	}
	if _, leaked := byID["bv-reopen"]; leaked {
		t.Fatalf("reopened bv-reopen must not emit stale closed temporal window: %+v", byID["bv-reopen"])
	}
	if got := byID["bv-alice-closed"]; !got.End.Equal(latestCommit) || got.ActiveBeads != 3 {
		t.Fatalf("bv-alice-closed window = %+v, want End=%s ActiveBeads=3 (counting bv-alice-closed + case-insensitive bv-open + reopened bv-reopen)", got, latestCommit)
	}
	inst := byID["bv-instant"]
	if !inst.Start.Equal(inst.End) || inst.ActiveBeads != 1 {
		t.Fatalf("instantaneous window = %+v, want Start.Equal(End) and ActiveBeads == 1 (empty-email OtherDev excluded)", inst)
	}

	// 3. Direct commitInWindow positive (case-insensitive email) and planted negative (empty-email distinct authors / both empty).
	if !commitInWindow(*openWin, walkedCommit{Timestamp: t3, Author: "Anyone", AuthorEmail: "alice@example.com"}) {
		t.Fatal("expected case-insensitive email match in commitInWindow")
	}
	if commitInWindow(inst, walkedCommit{Timestamp: t3, Author: "OtherDev", AuthorEmail: ""}) {
		t.Fatal("negative control failed: empty-email distinct authors matched in commitInWindow")
	}
	if sameClaimantIdentity("", "", "", "") {
		t.Fatal("negative control failed: two empty author+email identities must not match")
	}
}

func TestSameSecondCloseReopenChronology_AndMilestonesFallback(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Minute) // Same-second close & reopen timestamp
	t3 := t0.Add(5 * time.Minute) // Later fallbackEnd

	// Positive observable: EventClosed at t1 followed by EventReopened at exact same second t1 -> still open!
	reopenedEvents := []BeadEvent{
		{BeadID: "bv-same-reopen", EventType: EventClaimed, Timestamp: t0, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-same-reopen", EventType: EventClosed, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-same-reopen", EventType: EventReopened, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
	}
	if eventsStillClosed(reopenedEvents) {
		t.Fatal("eventsStillClosed returned true when EventReopened followed EventClosed at same timestamp")
	}
	reopenMilestones := GetBeadMilestones(reopenedEvents)
	if milestonesStillClosed(reopenMilestones) {
		t.Fatal("milestonesStillClosed returned true when Reopened.Timestamp.Equal(Closed.Timestamp)")
	}
	if w := ExtractWindowFromMilestones("bv-same-reopen", "Same-sec reopen", reopenMilestones); w != nil {
		t.Fatalf("same-second reopened milestones without fallbackEnd returned closed window %+v, want nil", w)
	}
	if w := ExtractWindowFromMilestones("bv-same-reopen", "Same-sec reopen", reopenMilestones, t3); w == nil || !w.End.Equal(t3) {
		t.Fatalf("same-second reopened milestones with fallbackEnd = %+v, want End=%s", w, t3)
	}
	openHist := BeadHistory{BeadID: "bv-same-reopen", Status: "open", Events: reopenedEvents, Milestones: reopenMilestones}
	if historyStillClosed(openHist) {
		t.Fatal("historyStillClosed returned true for Status=open same-second reopened bead")
	}

	// Planted negative control: EventReopened at t1 followed by EventClosed at exact same second t1 -> closed!
	reclosedEvents := []BeadEvent{
		{BeadID: "bv-same-reclosed", EventType: EventClaimed, Timestamp: t0, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-same-reclosed", EventType: EventClosed, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-same-reclosed", EventType: EventReopened, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-same-reclosed", EventType: EventClosed, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
	}
	if !eventsStillClosed(reclosedEvents) {
		t.Fatal("negative control failed: eventsStillClosed returned false when EventClosed followed EventReopened at same timestamp")
	}
	reclosedMilestones := GetBeadMilestones(reclosedEvents)
	closedHist := BeadHistory{BeadID: "bv-same-reclosed", Status: "closed", Events: reclosedEvents, Milestones: reclosedMilestones}
	if !historyStillClosed(closedHist) {
		t.Fatal("negative control failed: historyStillClosed returned false for Status=closed re-closed bead")
	}
	if w := extractWindowForBead("bv-same-reclosed", "Reclosed", reclosedMilestones, historyStillClosed(closedHist), t3); w == nil || !w.End.Equal(t1) {
		t.Fatalf("re-closed bead window = %+v, want End=%s (bounded at Closed, not fallbackEnd %s)", w, t1, t3)
	}

	// Verify temporalWindowsFromEvents emits ONLY bv-same-reclosed (not bv-same-reopen) while counting both as active at t1.
	combined := append(append([]BeadEvent(nil), reopenedEvents...), reclosedEvents...)
	wins := temporalWindowsFromEvents(combined, "")
	if len(wins) != 1 || wins[0].BeadID != "bv-same-reclosed" || !wins[0].End.Equal(t1) || wins[0].ActiveBeads != 2 {
		t.Fatalf("temporalWindowsFromEvents = %+v, want single window for bv-same-reclosed with End=%s ActiveBeads=2", wins, t1)
	}

	// [R2-A3] Closed-then-deleted synthesized BeadHistory (Status="deleted") stays closed at Closed.Timestamp (t1),
	// whereas reopened-then-deleted BeadHistory (Status="deleted") stays open (stillClosed == false)!
	t2 := t0.Add(2 * time.Minute)
	closedThenDeletedEvents := []BeadEvent{
		{BeadID: "bv-closed-del", EventType: EventClaimed, Timestamp: t0, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-closed-del", EventType: EventClosed, Timestamp: t1, Author: "Alice", AuthorEmail: "alice@example.com"},
		{BeadID: "bv-closed-del", EventType: EventDeleted, Timestamp: t2, Author: "Alice", AuthorEmail: "alice@example.com"},
	}
	closedDelHist := BeadHistory{
		BeadID:     "bv-closed-del",
		Status:     "deleted",
		Events:     closedThenDeletedEvents,
		Milestones: GetBeadMilestones(closedThenDeletedEvents),
	}
	if !historyStillClosed(closedDelHist) {
		t.Fatal("[R2-A3] historyStillClosed returned false for closed-then-deleted history (Status=deleted)")
	}
	if w := extractWindowForBead("bv-closed-del", "Closed then deleted", closedDelHist.Milestones, historyStillClosed(closedDelHist), t3); w == nil || !w.End.Equal(t1) {
		t.Fatalf("[R2-A3] closed-then-deleted window = %+v, want End=%s (not fallbackEnd %s)", w, t1, t3)
	}
	reopenedThenDeletedEvents := append(append([]BeadEvent(nil), reopenedEvents...), BeadEvent{
		BeadID: "bv-same-reopen", EventType: EventDeleted, Timestamp: t2, Author: "Alice", AuthorEmail: "alice@example.com",
	})
	reopenDelHist := BeadHistory{
		BeadID:     "bv-same-reopen",
		Status:     "deleted",
		Events:     reopenedThenDeletedEvents,
		Milestones: GetBeadMilestones(reopenedThenDeletedEvents),
	}
	if historyStillClosed(reopenDelHist) {
		t.Fatal("[R2-A3] negative control failed: historyStillClosed returned true for reopened-then-deleted history")
	}
}
