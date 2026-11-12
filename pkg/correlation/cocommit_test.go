package correlation

import (
	"strings"
	"testing"
	"time"
)

func TestIsCodeFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Code files
		{"pkg/auth/login.go", true},
		{"src/app.py", true},
		{"index.js", true},
		{"app.tsx", true},
		{"main.rs", true},
		{"App.java", true},
		{"config.yaml", true},
		{"data.json", true},
		{"README.md", true},
		{"schema.sql", true},
		{"script.sh", true},

		// Modern web, schema, infra, systems, and script extensions
		{"web/index.html", true},
		{"web/legacy.htm", true},
		{"styles/main.css", true},
		{"styles/theme.scss", true},
		{"styles/vars.sass", true},
		{"styles/compact.less", true},
		{"src/App.svelte", true},
		{"src/Widget.vue", true},
		{"src/pages/index.astro", true},
		{"api/v1/service.proto", true},
		{"api/schema.graphql", true},
		{"api/query.gql", true},
		{"infra/main.tf", true},
		{"infra/terragrunt.hcl", true},
		{"src/kernel.zig", true},
		{"wasm/module.wat", true},
		{"wasm/world.wit", true},
		{"nix/shell.nix", true},
		{"scripts/deploy.ps1", true},
		{"scripts/common.psm1", true},
		{"scripts/build.bat", true},
		{"scripts/run.cmd", true},
		{"cmake/FindDep.cmake", true},

		// Exact build/container/manifest basenames (root & nested)
		{"Makefile", true},
		{"sub/Makefile", true},
		{"GNUmakefile", true},
		{"Dockerfile", true},
		{"deploy/Dockerfile", true},
		{"Containerfile", true},
		{"Justfile", true},
		{"Vagrantfile", true},
		{"Tiltfile", true},
		{"BUILD", true},
		{"pkg/foo/BUILD.bazel", true},
		{"WORKSPACE", true},
		{"flake.nix", true},
		{"go.mod", true},
		{"Cargo.toml", true},
		{"package.json", true},
		{"pyproject.toml", true},

		// Non-code files
		{"image.png", false},
		{"photo.jpg", false},
		{"document.pdf", false},
		{"archive.zip", false},
		{"binary.exe", false},
		{"data.csv", false},

		// Edge cases
		{".gitignore", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isCodeFile(tt.path)
			if got != tt.want {
				t.Errorf("isCodeFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsExcludedPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Excluded paths
		{".beads/beads.jsonl", true},
		{".beads/issues.jsonl", true},
		{".bv/hooks.yaml", true},
		{".git/objects/abc", true},
		{"node_modules/lodash/index.js", true},
		{"vendor/github.com/pkg/errors/errors.go", true},
		{"__pycache__/module.pyc", true},
		{".venv/lib/python3.9/site.py", true},

		// Not excluded
		{"pkg/auth/login.go", false},
		{"src/components/Button.tsx", false},
		{"cmd/main.go", false},
		{"internal/service/user.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isExcludedPath(tt.path)
			if got != tt.want {
				t.Errorf("isExcludedPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestExcludePathspecArgs(t *testing.T) {
	args := excludePathspecArgs()

	// Must begin with the pathspec separator and the "." include so the
	// excludes are interpreted as pathspecs, not revisions.
	if len(args) < 2 || args[0] != "--" || args[1] != "." {
		t.Fatalf("expected args to start with [\"--\", \".\"], got %v", args[:min(len(args), 2)])
	}

	// Every excluded directory must produce a matching exclude pathspec so the
	// git show diff skips that content (the cost #160 was paying then discarding).
	joined := strings.Join(args, " ")
	for _, prefix := range excludedPaths {
		dir := strings.TrimSuffix(prefix, "/")
		want := ":(exclude,glob)" + dir + "/**"
		if !strings.Contains(joined, want) {
			t.Errorf("missing exclude pathspec for %q (want %q) in %v", prefix, want, args)
		}
	}

	// .beads/ is the dominant blob; assert it explicitly.
	if !strings.Contains(joined, ":(exclude,glob).beads/**") {
		t.Errorf("expected .beads exclusion in pathspec args, got %v", args)
	}
}

func TestContainsBeadID(t *testing.T) {
	tests := []struct {
		text   string
		beadID string
		want   bool
	}{
		{"fix: resolve issue bv-123", "bv-123", true},
		{"feat(auth): implement login for BV-123", "bv-123", true}, // Case insensitive
		{"chore: update deps", "bv-123", false},
		{"", "bv-123", false},
		{"some text", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := containsBeadID(tt.text, tt.beadID)
			if got != tt.want {
				t.Errorf("containsBeadID(%q, %q) = %v, want %v", tt.text, tt.beadID, got, tt.want)
			}
		})
	}
}

func TestAllTestFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []FileChange
		want  bool
	}{
		{
			name:  "empty list",
			files: []FileChange{},
			want:  false,
		},
		{
			name: "all go tests",
			files: []FileChange{
				{Path: "pkg/auth/login_test.go"},
				{Path: "pkg/auth/session_test.go"},
			},
			want: true,
		},
		{
			name: "all js tests",
			files: []FileChange{
				{Path: "src/app.test.js"},
				{Path: "src/utils.spec.ts"},
			},
			want: true,
		},
		{
			name: "mixed files",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
				{Path: "pkg/auth/login_test.go"},
			},
			want: false,
		},
		{
			name: "no test files",
			files: []FileChange{
				{Path: "pkg/auth/login.go"},
				{Path: "pkg/auth/session.go"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allTestFiles(tt.files)
			if got != tt.want {
				t.Errorf("allTestFiles() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShortSHA(t *testing.T) {
	tests := []struct {
		sha  string
		want string
	}{
		{"abc123def456789012345678901234567890abcd", "abc123d"},
		{"abc123", "abc123"},
		{"abc", "abc"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.sha, func(t *testing.T) {
			got := shortSHA(tt.sha)
			if got != tt.want {
				t.Errorf("shortSHA(%q) = %q, want %q", tt.sha, got, tt.want)
			}
		})
	}
}

func TestExtractNewPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Simple rename
		{"old.go => new.go", "new.go"},
		// With braces
		{"pkg/{old => new}/file.go", "pkg/new/file.go"},
		// Complex braces
		{"{old => new}.go", "new.go"},
		// No rename
		{"regular/path.go", "regular/path.go"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractNewPath(tt.input)
			if got != tt.want {
				t.Errorf("extractNewPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCalculateConfidence(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")
	now := time.Now()

	tests := []struct {
		name      string
		event     BeadEvent
		files     []FileChange
		wantRange [2]float64 // [min, max] expected range
	}{
		{
			name: "base case",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "fix: some bug",
			},
			files: []FileChange{
				{Path: "file.go"},
			},
			wantRange: [2]float64{0.94, 0.96}, // ~0.95
		},
		{
			name: "commit mentions bead ID",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "fix: resolve bv-123",
			},
			files: []FileChange{
				{Path: "file.go"},
			},
			wantRange: [2]float64{0.98, 1.0}, // 0.95 + 0.04 = 0.99
		},
		{
			name: "shotgun commit",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "refactor: big change",
			},
			files:     make([]FileChange, 25), // >20 files
			wantRange: [2]float64{0.84, 0.86}, // 0.95 - 0.10 = 0.85
		},
		{
			name: "only test files",
			event: BeadEvent{
				BeadID:    "bv-123",
				CommitMsg: "test: add tests",
			},
			files: []FileChange{
				{Path: "auth_test.go"},
				{Path: "user_test.go"},
			},
			wantRange: [2]float64{0.89, 0.91}, // 0.95 - 0.05 = 0.90
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.event.Timestamp = now
			got := c.calculateConfidence(tt.event, tt.files)
			if got < tt.wantRange[0] || got > tt.wantRange[1] {
				t.Errorf("calculateConfidence() = %v, want in range [%v, %v]", got, tt.wantRange[0], tt.wantRange[1])
			}
		})
	}
}

func TestGenerateReason(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClosed,
		CommitMsg: "fix: resolve bv-123",
	}

	files := []FileChange{{Path: "file.go"}}

	reason := c.generateReason(event, files, 0.99)

	if reason == "" {
		t.Error("reason should not be empty")
	}

	// Should mention the event type
	if !strings.Contains(reason, "closed") {
		t.Errorf("reason should mention event type, got: %s", reason)
	}

	// Should mention bead ID reference
	if !strings.Contains(reason, "bead ID") {
		t.Errorf("reason should mention bead ID reference, got: %s", reason)
	}
}

func TestCreateCorrelatedCommit(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")
	now := time.Now()

	event := BeadEvent{
		BeadID:      "bv-123",
		EventType:   EventClosed,
		Timestamp:   now,
		CommitSHA:   "abc123def456",
		CommitMsg:   "fix: close bv-123",
		Author:      "Test User",
		AuthorEmail: "test@example.com",
	}

	files := []FileChange{
		{Path: "pkg/auth/login.go", Action: "M", Insertions: 10, Deletions: 5},
	}

	commit := c.CreateCorrelatedCommit(event, files)

	if commit.SHA != event.CommitSHA {
		t.Errorf("SHA mismatch: got %s, want %s", commit.SHA, event.CommitSHA)
	}
	if commit.ShortSHA != "abc123d" {
		t.Errorf("ShortSHA mismatch: got %s", commit.ShortSHA)
	}
	if commit.Method != MethodCoCommitted {
		t.Errorf("Method should be MethodCoCommitted, got %s", commit.Method)
	}
	if commit.Confidence < 0.9 {
		t.Errorf("Confidence should be high for bead ID mention, got %v", commit.Confidence)
	}
	if len(commit.Files) != 1 {
		t.Errorf("Files count mismatch: got %d, want 1", len(commit.Files))
	}
	if commit.Author != event.Author {
		t.Errorf("Author mismatch: got %s, want %s", commit.Author, event.Author)
	}
}

func TestNewCoCommitExtractor(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")
	if c.repoPath != "/tmp/test" {
		t.Errorf("repoPath = %s, want /tmp/test", c.repoPath)
	}
}

func TestExtractAllCoCommits_Empty(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")

	// Empty events
	commits, err := c.ExtractAllCoCommits(nil)
	if err != nil {
		t.Fatalf("ExtractAllCoCommits(nil) failed: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("len(commits) = %d, want 0", len(commits))
	}
}

func TestExtractAllCoCommits_NonStatusEvents(t *testing.T) {
	c := NewCoCommitExtractor("/tmp/test")

	// Only non-status events (created, modified)
	events := []BeadEvent{
		{BeadID: "bv-1", EventType: EventCreated, CommitSHA: "abc"},
		{BeadID: "bv-2", EventType: EventModified, CommitSHA: "def"},
	}

	commits, err := c.ExtractAllCoCommits(events)
	if err != nil {
		t.Fatalf("ExtractAllCoCommits failed: %v", err)
	}
	// Should skip non-status events
	if len(commits) != 0 {
		t.Errorf("len(commits) = %d, want 0 (non-status events)", len(commits))
	}
}

func TestExtractAllCoCommits_PropagatesBatchFailureWithoutMemoizing(t *testing.T) {
	t.Setenv("BV_ROBOT", "1")
	t.Setenv("BV_NO_CACHE", "")
	t.Setenv("BV_CACHE_DIR", t.TempDir())

	sha := strings.Repeat("a", 40)
	c := NewCoCommitExtractor(t.TempDir()) // Existing directory, intentionally not a Git repository.
	commits, err := c.ExtractAllCoCommits([]BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, CommitSHA: sha},
	})
	if err == nil {
		t.Fatal("expected the failed git batch to propagate")
	}
	if !strings.Contains(err.Error(), "priming co-commit batch") {
		t.Fatalf("error = %q, want co-commit batch context", err)
	}
	if commits != nil {
		t.Fatalf("commits = %#v, want nil on batch failure", commits)
	}
	if _, ok := c.batchedSHAs[sha]; ok {
		t.Fatal("failed SHA was marked as successfully batched")
	}
	if _, ok := c.fileCache[sha]; ok {
		t.Fatal("failed SHA was memoized in the file cache")
	}
	if _, ok := c.statCache[sha]; ok {
		t.Fatal("failed SHA was memoized in the line-stat cache")
	}
	if disk := loadPerCommitCoCommit(perCommitCoCommitCacheNamespace()); disk != nil {
		if _, ok := disk[sha]; ok {
			t.Fatal("failed SHA was persisted in the per-commit cache")
		}
	}

	if _, err := c.ExtractAllCoCommits([]BeadEvent{
		{BeadID: "bv-1", EventType: EventClaimed, CommitSHA: sha},
	}); err == nil {
		t.Fatal("second attempt unexpectedly used a poisoned cache entry")
	}
}

func TestGenerateReason_LargeCommit(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClaimed,
		CommitMsg: "big change",
	}

	// Create > 20 files to trigger large commit message
	files := make([]FileChange, 25)
	for i := range files {
		files[i] = FileChange{Path: "file" + string(rune('a'+i)) + ".go"}
	}

	reason := c.generateReason(event, files, 0.85)

	if !strings.Contains(reason, "large commit") {
		t.Errorf("reason should mention large commit, got: %s", reason)
	}
}

func TestGenerateReason_OnlyTestFiles(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	event := BeadEvent{
		BeadID:    "bv-123",
		EventType: EventClaimed,
		CommitMsg: "add tests",
	}

	files := []FileChange{
		{Path: "auth_test.go"},
		{Path: "login_test.go"},
	}

	reason := c.generateReason(event, files, 0.90)

	if !strings.Contains(reason, "test files") {
		t.Errorf("reason should mention test files, got: %s", reason)
	}
}

func TestCalculateConfidence_Combined(t *testing.T) {
	c := NewCoCommitExtractor("/test/repo")

	// Test combination: shotgun commit with bead ID mention
	event := BeadEvent{
		BeadID:    "bv-123",
		CommitMsg: "big refactor bv-123",
	}

	files := make([]FileChange, 30)
	for i := range files {
		files[i] = FileChange{Path: "file" + string(rune('a'+i)) + ".go"}
	}

	confidence := c.calculateConfidence(event, files)

	// Base 0.95 + 0.04 (bead ID) - 0.10 (shotgun) = 0.89
	if confidence < 0.88 || confidence > 0.90 {
		t.Errorf("Combined confidence = %v, expected ~0.89", confidence)
	}
}

func TestExtractNewPath_DoubleSlashBug(t *testing.T) {
	// Git output for renaming "pkg/old/file.go" to "pkg/file.go"
	// is "pkg/{old => }/file.go"
	input := "pkg/{old => }/file.go"

	// We expect "pkg/file.go"
	expected := "pkg/file.go"

	got := extractNewPath(input)

	if got != expected {
		t.Errorf("extractNewPath(%q) = %q; want %q", input, got, expected)
	}
}

func TestExtractNewPath_ComplexCases(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"{old => new}", "new"},
		{"src/{old => new}/main.go", "src/new/main.go"},
		{"src/{ => new}/main.go", "src/new/main.go"}, // Addition
		{"src/{old => }/main.go", "src/main.go"},     // Deletion - vulnerable case
		{"old => new", "new"},
	}

	for _, tc := range cases {
		got := extractNewPath(tc.input)
		if got != tc.expected {
			t.Errorf("extractNewPath(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}
