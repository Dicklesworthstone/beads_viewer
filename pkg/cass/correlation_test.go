package cass

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "simple title",
			input:    "Token refresh timeout",
			expected: []string{"token", "refresh", "timeout"},
		},
		{
			name:     "with stop words",
			input:    "Fix the login bug in auth module",
			expected: []string{"login", "auth", "module"},
		},
		{
			name:     "all stop words",
			input:    "Fix the bug",
			expected: nil, // "fix", "the", "bug" are all stop words
		},
		{
			name:     "short words filtered",
			input:    "Add a UI to DB",
			expected: nil, // All words are too short or stop words
		},
		{
			name:     "mixed case",
			input:    "Database Schema Migration",
			expected: []string{"database", "schema", "migration"},
		},
		{
			name:     "with special characters",
			input:    "API/endpoint (v2) user-auth",
			expected: []string{"api", "endpoint", "user", "auth"},
		},
		{
			name:     "duplicate words",
			input:    "auth auth authentication auth",
			expected: []string{"auth", "authentication"},
		},
		{
			name:     "exceeds max keywords",
			input:    "database schema migration endpoint controller service repository model handler",
			expected: []string{"database", "schema", "migration", "endpoint", "controller"},
		},
		{
			name:     "numbers included",
			input:    "OAuth2 authentication v3",
			expected: []string{"oauth2", "authentication"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractKeywords(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("ExtractKeywords(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}
			for i, kw := range result {
				if kw != tt.expected[i] {
					t.Errorf("ExtractKeywords(%q)[%d] = %q, want %q", tt.input, i, kw, tt.expected[i])
				}
			}
		})
	}
}

func TestFindBeadIDMentions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "no mentions",
			input:    "This is a regular text",
			expected: nil,
		},
		{
			name:     "single mention",
			input:    "Working on bv-abc123 today",
			expected: []string{"bv-abc123"},
		},
		{
			name:     "multiple mentions",
			input:    "Fixed bv-abc123 and bv-def456",
			expected: []string{"bv-abc123", "bv-def456"},
		},
		{
			name:     "duplicate mentions",
			input:    "bv-abc123 relates to bv-abc123",
			expected: []string{"bv-abc123"},
		},
		{
			name:     "mixed with other IDs",
			input:    "JIRA-123 and bv-xyz789",
			expected: []string{"bv-xyz789"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindBeadIDMentions(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("FindBeadIDMentions(%q) = %v, want %v", tt.input, result, tt.expected)
				return
			}
			for i, id := range result {
				if id != tt.expected[i] {
					t.Errorf("FindBeadIDMentions(%q)[%d] = %q, want %q", tt.input, i, id, tt.expected[i])
				}
			}
		})
	}
}

func TestWorkspaceFromBeadsPath(t *testing.T) {
	tmpRoot := t.TempDir()
	nestedRepo := filepath.Join(tmpRoot, "nested", "repo")
	if err := os.MkdirAll(filepath.Join(nestedRepo, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmpRoot)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty path resolves to cwd",
			input:    "",
			expected: tmpRoot,
		},
		{
			name:     "whitespace path resolves to cwd",
			input:    "   ",
			expected: tmpRoot,
		},
		{
			name:     "git HEAD pseudo-path resolves to cwd instead of parent dir",
			input:    "git:HEAD",
			expected: tmpRoot,
		},
		{
			name:     "git revision pseudo-path resolves to cwd instead of parent dir",
			input:    "git:main~3",
			expected: tmpRoot,
		},
		{
			name:     "padded git revision pseudo-path resolves to cwd",
			input:    "  git:v1.2.0^{commit}  ",
			expected: tmpRoot,
		},
		{
			name:     "standard path",
			input:    "/home/user/project/.beads/beads.jsonl",
			expected: "/home/user/project",
		},
		{
			name:     "windows-like path",
			input:    "C:/Users/dev/project/.beads/issues.jsonl",
			expected: "C:/Users/dev/project",
		},
		{
			name:     "windows backslash path",
			input:    `C:\Users\dev\project\.beads\issues.jsonl`,
			expected: "C:/Users/dev/project",
		},
		{
			name:     "relative .beads path resolves to absolute workspace",
			input:    ".beads/issues.jsonl",
			expected: tmpRoot,
		},
		{
			name:     "nested relative .beads path resolves to nested workspace",
			input:    filepath.Join("nested", "repo", ".beads", "issues.jsonl"),
			expected: nestedRepo,
		},
	}

	parentOfTmpRoot := filepath.Dir(tmpRoot)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WorkspaceFromBeadsPath(tt.input)
			if result != tt.expected {
				t.Errorf("WorkspaceFromBeadsPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
			if strings.HasPrefix(strings.TrimSpace(tt.input), "git:") && result == parentOfTmpRoot {
				t.Errorf("WorkspaceFromBeadsPath(%q) erroneously resolved to parent directory %q", tt.input, result)
			}
		})
	}
}

func TestCorrelator_Correlate_IDMention(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	// Create a properly initialized detector that always reports healthy
	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check() // Prime the cache

	searcher := NewSearcher(detector)

	// Override runCommand to simulate cass responses
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// Check if this is an ID search (contains quoted bead ID)
		for _, arg := range args {
			if arg == `"bv-test123"` {
				return []byte(`{
					"hits": [
						{
							"source_path": "/home/user/project/sessions/s1.json",
							"workspace": "/home/user/project",
							"title": "Working on bv-test123",
							"score": 0.95,
							"content": "Discussing bv-test123 implementation"
						}
					],
					"total_matches": 1
				}`), nil
			}
		}
		return []byte(`{"hits": [], "total_matches": 0}`), nil
	}

	cache := NewCache()
	correlator := NewCorrelator(searcher, cache, "/home/user/project")
	correlator.now = func() time.Time { return now }

	issue := &model.Issue{
		ID:        "bv-test123",
		Title:     "Test issue",
		CreatedAt: now.Add(-24 * time.Hour),
	}

	result := correlator.Correlate(context.Background(), issue)

	if result.BeadID != "bv-test123" {
		t.Errorf("BeadID = %q, want %q", result.BeadID, "bv-test123")
	}

	if result.Strategy != StrategyIDMention {
		t.Errorf("Strategy = %q, want %q", result.Strategy, StrategyIDMention)
	}

	if len(result.TopSessions) == 0 {
		t.Error("Expected at least one session result")
	}

	// Check that result was cached
	hint := cache.Get("bv-test123")
	if hint == nil {
		t.Error("Expected result to be cached")
	}
}

func TestCorrelator_Correlate_Keywords(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	searcher := NewSearcher(detector)

	// Override to return results for keyword searches but not ID searches
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// Check for keyword query (not quoted)
		for i, arg := range args {
			if i > 0 && args[i-1] == "search" && !contains(arg, `"bv-`) {
				if contains(arg, "authentication") || contains(arg, "oauth") {
					return []byte(`{
						"hits": [
							{
								"source_path": "/home/user/project/sessions/s2.json",
								"workspace": "/home/user/project",
								"title": "OAuth implementation session",
								"score": 0.8,
								"content": "Working on authentication flow with OAuth"
							}
						],
						"total_matches": 1
					}`), nil
				}
			}
		}
		return []byte(`{"hits": [], "total_matches": 0}`), nil
	}

	cache := NewCache()
	correlator := NewCorrelator(searcher, cache, "/home/user/project")
	correlator.now = func() time.Time { return now }

	issue := &model.Issue{
		ID:        "bv-auth001",
		Title:     "OAuth authentication implementation",
		CreatedAt: now.Add(-7 * 24 * time.Hour),
	}

	result := correlator.Correlate(context.Background(), issue)

	if result.Strategy != StrategyKeywords {
		t.Errorf("Strategy = %q, want %q", result.Strategy, StrategyKeywords)
	}

	if len(result.Keywords) == 0 {
		t.Error("Expected keywords to be extracted")
	}

	// Should contain "oauth" and "authentication"
	hasOAuth := false
	hasAuth := false
	for _, kw := range result.Keywords {
		if kw == "oauth" {
			hasOAuth = true
		}
		if kw == "authentication" {
			hasAuth = true
		}
	}

	if !hasOAuth || !hasAuth {
		t.Errorf("Keywords = %v, expected to contain 'oauth' and 'authentication'", result.Keywords)
	}
}

func TestCorrelator_Correlate_CacheHit(t *testing.T) {
	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	searcher := NewSearcher(detector)

	// Track if search was called
	searchCalled := false
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		searchCalled = true
		return []byte(`{"hits": [], "total_matches": 0}`), nil
	}

	cache := NewCache()

	// Pre-populate cache
	cache.Set("bv-cached", &CorrelationHint{
		BeadID:      "bv-cached",
		ResultCount: 1,
		Results: []ScoredResult{
			{SearchResult: SearchResult{Title: "Cached result", Score: 0.9}},
		},
		QueryUsed: string(StrategyIDMention),
	})

	correlator := NewCorrelator(searcher, cache, "")

	issue := &model.Issue{
		ID:    "bv-cached",
		Title: "Cached issue",
	}

	result := correlator.Correlate(context.Background(), issue)

	if searchCalled {
		t.Error("Search should not be called for cached result")
	}

	if result.ComputeTimeMs != 0 {
		t.Errorf("ComputeTimeMs = %d, want 0 for cache hit", result.ComputeTimeMs)
	}

	if len(result.TopSessions) == 0 {
		t.Error("Expected cached sessions to be returned")
	}
}

func TestCorrelator_GetCached(t *testing.T) {
	// Create cache with a pre-populated entry
	cache := NewCache()
	hint := &CorrelationHint{
		BeadID:      "bv-cached",
		Results:     []ScoredResult{{SearchResult: SearchResult{Agent: "claude", Snippet: "Test"}}},
		QueryUsed:   "test query",
		ResultCount: 3,
	}
	cache.Set("bv-cached", hint)

	correlator := &Correlator{
		cache: cache,
	}

	t.Run("hit", func(t *testing.T) {
		result := correlator.GetCached("bv-cached")
		if result == nil {
			t.Fatal("Expected cache hit")
		}
		if result.ResultCount != 3 {
			t.Errorf("ResultCount = %d, want 3", result.ResultCount)
		}
	})

	t.Run("miss", func(t *testing.T) {
		result := correlator.GetCached("bv-uncached")
		if result != nil {
			t.Error("Expected cache miss")
		}
	})

	t.Run("nil_cache", func(t *testing.T) {
		c := &Correlator{cache: nil}
		result := c.GetCached("bv-any")
		if result != nil {
			t.Error("Expected nil for nil cache")
		}
	})
}

func TestCorrelator_CachePreservesScoredResults(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		query      string
		strategy   CorrelationStrategy
		baseScore  float64
		finalScore float64
	}{
		{"id", `"bv-roundtrip"`, StrategyIDMention, 100, 240},
		{"keywords", "oauth authentication", StrategyKeywords, 50, 140},
		{"timestamp", "*", StrategyTimestamp, 50, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detector := NewDetector()
			detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
			detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
			searcher := NewSearcher(detector)
			calls := 0
			searcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls++
				if args[1] != tc.query {
					return []byte(`{"hits":[],"total_matches":0}`), nil
				}
				return []byte(fmt.Sprintf(`{"hits":[{
					"source_path":"/archive/session.jsonl","line_number":42,
					"workspace":"/work/project","agent":"codex",
					"title":"OAuth authentication","content":"OAuth authentication flow",
					"score":26.75,"created_at":%d
				}],"total_matches":7}`, now.Add(-2*time.Hour).UnixMilli())), nil
			}
			cache := NewCache()
			correlator := NewCorrelator(searcher, cache, "/work/project")
			correlator.now = func() time.Time { return now }
			issue := &model.Issue{
				ID: "bv-roundtrip", Title: "OAuth authentication",
				CreatedAt: now.Add(-time.Hour),
			}

			cold := correlator.Correlate(context.Background(), issue)
			if cold.Error != "" {
				t.Fatalf("successful correlation diagnostic = %q", cold.Error)
			}
			if len(cold.TopSessions) != 1 {
				t.Fatalf("cold sessions = %v, want one scored session", cold.TopSessions)
			}
			if cold.Strategy != tc.strategy || cold.TopSessions[0].Strategy != tc.strategy {
				t.Errorf("cold strategy = %q/%q, want %q", cold.Strategy, cold.TopSessions[0].Strategy, tc.strategy)
			}
			if got := cold.TopSessions[0]; got.BaseScore != tc.baseScore || got.FinalScore != tc.finalScore {
				t.Errorf("cold score = base %v/final %v, want %v/%v", got.BaseScore, got.FinalScore, tc.baseScore, tc.finalScore)
			}
			if cold.TotalFound != 7 {
				t.Errorf("TotalFound = %d, want producer total 7", cold.TotalFound)
			}
			callsBeforeCache := calls
			warm := correlator.Correlate(context.Background(), issue)
			if calls != callsBeforeCache {
				t.Errorf("cache hit executed %d extra searches", calls-callsBeforeCache)
			}
			if !reflect.DeepEqual(warm, cold) {
				t.Errorf("cache changed correlation:\ncold = %#v\nwarm = %#v", cold, warm)
			}
		})
	}
}

func TestCorrelator_FailedSearchIsRetryable(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		failedQuery     string
		fallbackQuery   string
		partial         bool
		wantFirstResult bool
	}{
		{name: "id failure", failedQuery: `"bv-retry"`},
		{name: "keyword failure", failedQuery: "oauth authentication"},
		{name: "timestamp failure", failedQuery: "*"},
		{name: "partial id result", failedQuery: `"bv-retry"`, partial: true, wantFirstResult: true},
		{name: "partial keyword result", failedQuery: "oauth authentication", partial: true, wantFirstResult: true},
		{name: "partial timestamp result", failedQuery: "*", partial: true, wantFirstResult: true},
		{name: "failed id with keyword fallback", failedQuery: `"bv-retry"`, fallbackQuery: "oauth authentication", wantFirstResult: true},
		{name: "failed keyword with timestamp fallback", failedQuery: "oauth authentication", fallbackQuery: "*", wantFirstResult: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detector := NewDetector()
			detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
			detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
			searcher := NewSearcher(detector)
			recovered := false
			calls := 0
			searcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls++
				query := args[1]
				if !recovered && query == tc.failedQuery && !tc.partial {
					return nil, errors.New("temporary search failure")
				}
				if recovered || query == tc.fallbackQuery || (query == tc.failedQuery && tc.partial) {
					return []byte(fmt.Sprintf(`{"hits":[{
						"source_path":"/archive/session.jsonl","workspace":"/work/project",
						"title":"OAuth authentication","content":"OAuth authentication flow",
						"score":26.75,"created_at":%d
					}],"total_matches":1,"budget":{"timed_out":%t}}`, now.UnixMilli(), !recovered && tc.partial)), nil
				}
				return []byte(`{"hits":[],"total_matches":0}`), nil
			}
			cache := NewCache()
			correlator := NewCorrelator(searcher, cache, "/work/project")
			correlator.now = func() time.Time { return now }
			issue := &model.Issue{ID: "bv-retry", Title: "OAuth authentication", CreatedAt: now}

			first := correlator.Correlate(context.Background(), issue)
			if first.Error == "" {
				t.Error("failed or partial search lost its diagnostic")
			}
			if got := len(first.TopSessions) > 0; got != tc.wantFirstResult {
				t.Errorf("first result presence = %v, want %v", got, tc.wantFirstResult)
			}
			if cache.Get(issue.ID) != nil {
				t.Error("failed or partial correlation was cached")
			}
			firstCalls := calls
			recovered = true
			second := correlator.Correlate(context.Background(), issue)
			if second.Error != "" {
				t.Errorf("successful retry diagnostic = %q", second.Error)
			}
			if calls <= firstCalls {
				t.Error("retry did not execute a new search")
			}
			if len(second.TopSessions) != 1 || second.Strategy != StrategyIDMention {
				t.Errorf("retry did not recover ID result: %#v", second)
			}
			if cache.Get(issue.ID) == nil {
				t.Error("successful retry was not cached")
			}
		})
	}
}

// TestCorrelator_TimeoutStopsStrategiesAndIsReported (#212): when the shared
// lookup budget runs out during the ID search, the result says so, the later
// strategies are not attempted, and nothing is cached.
func TestCorrelator_TimeoutStopsStrategiesAndIsReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		detector := NewDetector()
		detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
		detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
		searcher := NewSearcher(detector)
		var queries []string
		searcher.runCommand = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
			queries = append(queries, args[1])
			<-ctx.Done() // a search slower than the budget
			return nil, ctx.Err()
		}
		cache := NewCache()
		correlator := NewCorrelator(searcher, cache, "/work/project")
		issue := &model.Issue{ID: "bv-slow", Title: "OAuth authentication", CreatedAt: time.Now()}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result := correlator.Correlate(ctx, issue)

		if !result.TimedOut || !strings.Contains(result.Error, "ID search: search timed out") {
			t.Fatalf("result = %+v, want a reported ID-search timeout", result)
		}
		if len(queries) != 1 {
			t.Fatalf("searches attempted = %q, want only the ID search", queries)
		}
		if cache.Get(issue.ID) != nil {
			t.Fatal("a timed-out correlation was cached")
		}
	})
}

func TestCorrelator_EmptySuccessIsCached(t *testing.T) {
	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
	searcher := NewSearcher(detector)
	calls := 0
	searcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return []byte(`{"hits":[],"total_matches":0}`), nil
	}
	cache := NewCache()
	correlator := NewCorrelator(searcher, cache, "")
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	correlator.now = func() time.Time { return now }
	issue := &model.Issue{ID: "bv-empty", Title: "OAuth authentication", CreatedAt: now}

	cold := correlator.Correlate(context.Background(), issue)
	if cold.Error != "" {
		t.Fatalf("successful empty correlation diagnostic = %q", cold.Error)
	}
	if len(cold.TopSessions) != 0 || cold.TotalFound != 0 {
		t.Fatalf("expected empty successful correlation, got %#v", cold)
	}
	if calls != 3 {
		t.Errorf("search count = %d, want all three strategies", calls)
	}
	if cache.Get(issue.ID) == nil {
		t.Error("successful empty correlation was not cached")
	}
	warm := correlator.Correlate(context.Background(), issue)
	if calls != 3 {
		t.Errorf("cached empty result executed another search: %d calls", calls)
	}
	if !reflect.DeepEqual(warm, cold) {
		t.Errorf("cache changed empty correlation:\ncold = %#v\nwarm = %#v", cold, warm)
	}
}

func TestCorrelator_Scoring(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	correlator := &Correlator{
		workspace: "/home/user/project",
		now:       func() time.Time { return now },
	}

	t.Run("applyTimeDecay_recent", func(t *testing.T) {
		recentTime := now.Add(-12 * time.Hour)
		score := correlator.applyTimeDecay(100, recentTime)
		expected := 100 + float64(BonusRecent24h)
		if score != expected {
			t.Errorf("applyTimeDecay for 12h ago = %f, want %f", score, expected)
		}
	})

	t.Run("applyTimeDecay_week", func(t *testing.T) {
		weekAgo := now.Add(-5 * 24 * time.Hour)
		score := correlator.applyTimeDecay(100, weekAgo)
		expected := 100 + float64(BonusRecent7d)
		if score != expected {
			t.Errorf("applyTimeDecay for 5d ago = %f, want %f", score, expected)
		}
	})

	t.Run("applyTimeDecay_old", func(t *testing.T) {
		monthAgo := now.Add(-45 * 24 * time.Hour)
		score := correlator.applyTimeDecay(100, monthAgo)
		expected := 100 + float64(PenaltyOld30d)
		if score != expected {
			t.Errorf("applyTimeDecay for 45d ago = %f, want %f", score, expected)
		}
	})

	t.Run("applyWorkspaceBoost_match", func(t *testing.T) {
		score := correlator.applyWorkspaceBoost(100, "/home/user/project/sessions/s1.json")
		expected := 100 * MultiplierSameWorkspace
		if score != expected {
			t.Errorf("applyWorkspaceBoost for matching path = %f, want %f", score, expected)
		}
	})

	t.Run("applyWorkspaceBoost_no_match", func(t *testing.T) {
		score := correlator.applyWorkspaceBoost(100, "/other/path/session.json")
		if score != 100 {
			t.Errorf("applyWorkspaceBoost for non-matching path = %f, want 100", score)
		}
	})

	t.Run("applyWorkspaceBoost_similar_name_no_match", func(t *testing.T) {
		// Should NOT match /other/project-v2 when workspace is /home/user/project
		score := correlator.applyWorkspaceBoost(100, "/other/project-v2/file.json")
		if score != 100 {
			t.Errorf("applyWorkspaceBoost should not match similar names = %f, want 100", score)
		}
	})

	t.Run("applyWorkspaceBoost_empty_workspace", func(t *testing.T) {
		c := &Correlator{workspace: ""}
		score := c.applyWorkspaceBoost(100, "/any/path/file.json")
		if score != 100 {
			t.Errorf("applyWorkspaceBoost with empty workspace = %f, want 100", score)
		}
	})

	t.Run("applyWorkspaceBoost_empty_source", func(t *testing.T) {
		score := correlator.applyWorkspaceBoost(100, "")
		if score != 100 {
			t.Errorf("applyWorkspaceBoost with empty source = %f, want 100", score)
		}
	})
}

func TestCorrelator_CalculateSearchDays(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	correlator := &Correlator{
		now: func() time.Time { return now },
	}

	t.Run("closed issue long ago measures from CreatedAt to now", func(t *testing.T) {
		closedAt := now.Add(-60 * 24 * time.Hour)
		issue := &model.Issue{
			CreatedAt: now.Add(-70 * 24 * time.Hour),
			ClosedAt:  &closedAt,
		}

		days := correlator.calculateSearchDays(issue)
		// Created 70d ago + 7d buffer = 77 days (>= 67 days so sessions around close are included)
		if days != 77 {
			t.Errorf("calculateSearchDays for closed issue (70d old, closed 60d ago) = %d, want 77", days)
		}
	})

	t.Run("closed issue recent", func(t *testing.T) {
		closedAt := now.Add(-10 * 24 * time.Hour)
		issue := &model.Issue{
			CreatedAt: now.Add(-20 * 24 * time.Hour),
			ClosedAt:  &closedAt,
		}

		days := correlator.calculateSearchDays(issue)
		// Created 20d ago + 7d buffer = 27 days
		if days != 27 {
			t.Errorf("calculateSearchDays for closed issue = %d, want 27", days)
		}
	})

	t.Run("open issue", func(t *testing.T) {
		issue := &model.Issue{
			CreatedAt: now.Add(-14 * 24 * time.Hour),
		}

		days := correlator.calculateSearchDays(issue)
		if days != 21 {
			t.Errorf("calculateSearchDays for open issue = %d, want 21", days)
		}
	})

	t.Run("brand new issue clamped to minimum 7 days", func(t *testing.T) {
		issue := &model.Issue{
			CreatedAt: now.Add(2 * 24 * time.Hour),
		}

		days := correlator.calculateSearchDays(issue)
		if days != 7 {
			t.Errorf("calculateSearchDays for future/new issue = %d, want 7", days)
		}
	})

	t.Run("very old issue capped at 365 days", func(t *testing.T) {
		issue := &model.Issue{
			CreatedAt: now.Add(-400 * 24 * time.Hour),
		}

		days := correlator.calculateSearchDays(issue)
		if days != 365 {
			t.Errorf("calculateSearchDays for old issue = %d, want 365 (capped)", days)
		}
	})

	t.Run("zero CreatedAt defaults to 30 days", func(t *testing.T) {
		days := correlator.calculateSearchDays(&model.Issue{})
		if days != 30 {
			t.Errorf("calculateSearchDays for zero CreatedAt = %d, want 30", days)
		}
	})
}

func TestCorrelator_ScoreKeywordMatch(t *testing.T) {
	correlator := &Correlator{}

	t.Run("all keywords match", func(t *testing.T) {
		result := SearchResult{
			Title:   "OAuth authentication flow",
			Snippet: "Implementing OAuth2 authentication",
		}
		keywords := []string{"oauth", "authentication"}

		score := correlator.scoreKeywordMatch(result, keywords)
		if score != float64(ScoreExactKeyword) {
			t.Errorf("scoreKeywordMatch for full match = %f, want %f", score, float64(ScoreExactKeyword))
		}
	})

	t.Run("partial match", func(t *testing.T) {
		result := SearchResult{
			Title:   "OAuth setup",
			Snippet: "Setting up OAuth",
		}
		keywords := []string{"oauth", "authentication", "login", "session"}

		score := correlator.scoreKeywordMatch(result, keywords)
		// 1/4 = 0.25 match ratio, so ScorePartialKeyword * 0.25
		expected := float64(ScorePartialKeyword) * 0.25
		if score != expected {
			t.Errorf("scoreKeywordMatch for partial = %f, want %f", score, expected)
		}
	})

	t.Run("no match", func(t *testing.T) {
		result := SearchResult{
			Title:   "Database setup",
			Snippet: "SQL migrations",
		}
		keywords := []string{"oauth", "authentication"}

		score := correlator.scoreKeywordMatch(result, keywords)
		if score != 0 {
			t.Errorf("scoreKeywordMatch for no match = %f, want 0", score)
		}
	})
}

func TestCorrelator_RankAndLimit(t *testing.T) {
	correlator := &Correlator{}

	results := []ScoredResult{
		{FinalScore: 50},
		{FinalScore: 100},
		{FinalScore: 75},
		{FinalScore: 25},
		{FinalScore: 90},
	}

	ranked := correlator.rankAndLimit(results)

	if len(ranked) != MaxSessionsReturned {
		t.Errorf("rankAndLimit returned %d results, want %d", len(ranked), MaxSessionsReturned)
	}

	// Should be sorted descending
	if ranked[0].FinalScore != 100 {
		t.Errorf("First result score = %f, want 100", ranked[0].FinalScore)
	}
	if ranked[1].FinalScore != 90 {
		t.Errorf("Second result score = %f, want 90", ranked[1].FinalScore)
	}
	if ranked[2].FinalScore != 75 {
		t.Errorf("Third result score = %f, want 75", ranked[2].FinalScore)
	}
}

func TestCorrelator_NilIssue(t *testing.T) {
	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	searcher := NewSearcher(detector)
	correlator := NewCorrelator(searcher, nil, "")

	// Should not panic with nil issue
	result := correlator.Correlate(context.Background(), nil)

	if result.BeadID != "" {
		t.Errorf("BeadID = %q, want empty for nil issue", result.BeadID)
	}

	if len(result.TopSessions) != 0 {
		t.Errorf("Expected empty TopSessions for nil issue, got %d", len(result.TopSessions))
	}
}

func TestCorrelator_EmptyResults(t *testing.T) {
	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	searcher := NewSearcher(detector)

	// Return empty results for everything
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"hits": [], "total_matches": 0}`), nil
	}

	correlator := NewCorrelator(searcher, nil, "")

	issue := &model.Issue{
		ID:    "bv-empty",
		Title: "Nothing matches this",
	}

	result := correlator.Correlate(context.Background(), issue)

	if result.BeadID != "bv-empty" {
		t.Errorf("BeadID = %q, want %q", result.BeadID, "bv-empty")
	}

	if len(result.TopSessions) != 0 {
		t.Errorf("Expected empty TopSessions, got %d", len(result.TopSessions))
	}
}

// Helper function - simple contains check for test assertions
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestCassWorkspaceOriginLocalID(t *testing.T) {
	detector := NewDetector()
	detector.lookPath = func(name string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(ctx context.Context, name string, args ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	type callRecord struct {
		query     string
		workspace string
	}
	var calls []callRecord

	searcher := NewSearcher(detector)
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var q, ws string
		if len(args) >= 2 && args[0] == "search" {
			q = args[1]
		}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--workspace" {
				ws = args[i+1]
			}
		}
		calls = append(calls, callRecord{query: q, workspace: ws})

		switch q {
		case `"bv-42"`:
			return []byte(`{
				"hits": [
					{"source_path": "/sessions/api-1.jsonl", "line_number": 10, "agent": "claude", "title": "API fix", "score": 5.0, "content": "Fixed bv-42", "workspace": "/workspace/services/api"},
					{"source_path": "/sessions/web-1.jsonl", "line_number": 4, "agent": "claude", "title": "Sibling mention", "score": 5.0, "content": "Mentioned bv-42", "workspace": "/workspace/services/web"}
				],
				"total_matches": 2
			}`), nil
		case `"api-bv-42"`:
			return []byte(`{
				"hits": [
					{"source_path": "/sessions/api-1.jsonl", "line_number": 10, "agent": "claude", "title": "API fix", "score": 5.0, "content": "Fixed bv-42", "workspace": "/workspace/services/api"},
					{"source_path": "/sessions/api-2.jsonl", "line_number": 22, "agent": "codex", "title": "Follow-up", "score": 4.0, "content": "Verified api-bv-42", "workspace": "/workspace/services/api"}
				],
				"total_matches": 2
			}`), nil
		case `"bv-single"`:
			return []byte(`{
				"hits": [
					{"source_path": "/sessions/root.jsonl", "line_number": 1, "agent": "claude", "title": "Root", "score": 3.0, "content": "bv-single", "workspace": "/workspace"}
				],
				"total_matches": 1
			}`), nil
		default:
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
	}

	correlator := NewCorrelator(searcher, nil, "/workspace")

	wsIssue := &model.Issue{
		ID:     "api-bv-42",
		Title:  "Fix token refresh race",
		Status: model.StatusOpen,
		Origin: &model.IssueOrigin{
			LocalID:          "bv-42",
			WorkingDirectory: "/workspace/services/api",
		},
	}

	res := correlator.Correlate(context.Background(), wsIssue)
	if res.Strategy != StrategyIDMention {
		t.Fatalf("Strategy = %q, want %q", res.Strategy, StrategyIDMention)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 cass ID queries (LocalID + namespaced ID), got %d: %+v", len(calls), calls)
	}
	if calls[0].query != `"bv-42"` || calls[1].query != `"api-bv-42"` {
		t.Fatalf("queries = %+v, want [\"bv-42\", \"api-bv-42\"]", calls)
	}
	for i, c := range calls {
		if c.workspace != "/workspace/services/api" {
			t.Fatalf("call[%d].workspace = %q, want sub-repo WorkingDirectory /workspace/services/api", i, c.workspace)
		}
	}

	// Duplicate (/sessions/api-1.jsonl, line 10) across both queries must deduplicate,
	// leaving 3 unique sessions (api-1, api-2, web-1).
	if len(res.TopSessions) != 3 {
		t.Fatalf("TopSessions len = %d, want 3 deduplicated sessions: %+v", len(res.TopSessions), res.TopSessions)
	}
	// Positive + negative workspace boost check: api-1 (in /workspace/services/api) gets 1.5x boost
	// while web-1 (sibling /workspace/services/web with identical raw score 5.0) does NOT.
	if res.TopSessions[0].SourcePath != "/sessions/api-1.jsonl" {
		t.Fatalf("TopSessions[0] = %q, want boosted /sessions/api-1.jsonl ahead of sibling web-1", res.TopSessions[0].SourcePath)
	}

	// Keyword fallback elevation check: if direct ID queries return 0 hits, keyword fallback
	// mentioning Origin.LocalID ("bv-local-7") elevates to ScoreIDMention & scopes to WorkingDirectory.
	calls = nil
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var q, ws string
		if len(args) >= 2 && args[0] == "search" {
			q = args[1]
		}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--workspace" {
				ws = args[i+1]
			}
		}
		calls = append(calls, callRecord{query: q, workspace: ws})
		if strings.HasPrefix(q, `"`) {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		return []byte(`{
			"hits": [
				{"source_path": "/sessions/kw.jsonl", "line_number": 9, "agent": "claude", "title": "OAuth callback", "score": 2.0, "content": "investigating bv-local-7 timeout", "workspace": "/workspace/services/auth"}
			],
			"total_matches": 1
		}`), nil
	}
	kwIssue := &model.Issue{
		ID:    "auth-bv-local-7",
		Title: "OAuth callback timeout",
		Origin: &model.IssueOrigin{
			LocalID:          "bv-local-7",
			WorkingDirectory: "/workspace/services/auth",
		},
	}
	kwRes := correlator.Correlate(context.Background(), kwIssue)
	if len(kwRes.TopSessions) != 1 {
		t.Fatalf("expected 1 keyword-fallback session, got %+v", kwRes.TopSessions)
	}
	for i, c := range calls {
		if c.workspace != "/workspace/services/auth" {
			t.Fatalf("fallback call[%d].workspace = %q, want /workspace/services/auth", i, c.workspace)
		}
	}
	wantBoostedIDScore := float64(ScoreIDMention) * MultiplierSameWorkspace
	if kwRes.TopSessions[0].FinalScore < wantBoostedIDScore-1e-6 || kwRes.TopSessions[0].FinalScore > wantBoostedIDScore+1e-6 {
		t.Fatalf("keyword hit mentioning Origin.LocalID FinalScore = %.2f, want %.2f", kwRes.TopSessions[0].FinalScore, wantBoostedIDScore)
	}

	// Planted negative control: single-repo issue (Origin == nil) executes exactly 1 ID query on root "/workspace".
	calls = nil
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		var q, ws string
		if len(args) >= 2 && args[0] == "search" {
			q = args[1]
		}
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--workspace" {
				ws = args[i+1]
			}
		}
		calls = append(calls, callRecord{query: q, workspace: ws})
		return []byte(`{
			"hits": [{"source_path": "/sessions/root.jsonl", "line_number": 1, "agent": "claude", "score": 3.0, "content": "bv-single", "workspace": "/workspace"}],
			"total_matches": 1
		}`), nil
	}
	singleRes := correlator.Correlate(context.Background(), &model.Issue{ID: "bv-single", Title: "Single repo"})
	if len(calls) != 1 || calls[0].query != `"bv-single"` || calls[0].workspace != "/workspace" {
		t.Fatalf("Origin==nil calls = %+v, want single (\"bv-single\", \"/workspace\")", calls)
	}
	if len(singleRes.TopSessions) != 1 {
		t.Fatalf("Origin==nil TopSessions = %+v, want 1", singleRes.TopSessions)
	}
}

func TestMentionsAnyIssueID_StrictTokenBoundariesAndJSONLOnlySubRepo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		snippet string
		title   string
		ids     []string
		want    bool
	}{
		{name: "exact_token", snippet: "bv-1", ids: []string{"bv-1"}, want: true},
		{name: "sentence_final_period", snippet: "Fixed bv-1.", title: "Summary", ids: []string{"bv-1"}, want: true},
		{name: "parens", snippet: "see (bv-1) for details", ids: []string{"bv-1"}, want: true},
		{name: "brackets_colon", snippet: "[BV-1]: handle auth", ids: []string{"bv-1"}, want: true},
		{name: "dash_spaced", snippet: "bv-1 - completed", ids: []string{"bv-1"}, want: true},
		{name: "later_real_match_after_false_prefix", snippet: "checked bv-10 then fixed bv-1.", ids: []string{"bv-1"}, want: true},
		// Planted negative controls that previously false-matched via strings.Contains:
		{name: "numeric_suffix_10", snippet: "working on bv-10", ids: []string{"bv-1"}, want: false},
		{name: "numeric_suffix_100", snippet: "closed bv-100 today", ids: []string{"bv-1"}, want: false},
		{name: "alpha_suffix", snippet: "bv-1a follow-up", ids: []string{"bv-1"}, want: false},
		{name: "underscore_suffix", snippet: "bv-1_patch", ids: []string{"bv-1"}, want: false},
		{name: "hierarchical_child_dot", snippet: "implemented bv-1.2", ids: []string{"bv-1"}, want: false},
		{name: "hyphen_sub_id", snippet: "branch bv-1-hotfix", ids: []string{"bv-1"}, want: false},
		{name: "namespaced_other_repo_prefix", snippet: "working on api-bv-1", ids: []string{"bv-1"}, want: false},
		{name: "namespaced_other_repo_longer", snippet: "working on api-bv-19", ids: []string{"bv-1"}, want: false},
		{name: "word_prefix", snippet: "xbv-1", ids: []string{"bv-1"}, want: false},
		{name: "dot_prefix", snippet: "ns.bv-1", ids: []string{"bv-1"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mentionsAnyIssueID(SearchResult{Snippet: tc.snippet, Title: tc.title}, tc.ids)
			if got != tc.want {
				t.Fatalf("mentionsAnyIssueID(snippet=%q, title=%q, ids=%v) = %v, want %v", tc.snippet, tc.title, tc.ids, got, tc.want)
			}
		})
	}

	// End-to-end check: keyword fallback with LocalID="bv-1" must NOT promote "bv-10" / "api-bv-19" to ScoreIDMention.
	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	searcher := NewSearcher(detector)
	searcher.runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) >= 2 && strings.HasPrefix(args[1], `"`) {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		return []byte(`{
			"hits": [
				{"source_path": "/sessions/false-prefix-nokw.jsonl", "line_number": 1, "agent": "claude", "title": "Misc", "score": 5.0, "content": "worked on bv-10 and bv-1.2 and web-bv-19", "workspace": "/other"},
				{"source_path": "/sessions/false-prefix-kw.jsonl", "line_number": 2, "agent": "claude", "title": "Token", "score": 5.0, "content": "worked on bv-100 authentication token validation", "workspace": "/other"},
				{"source_path": "/sessions/true-mention.jsonl", "line_number": 3, "agent": "claude", "title": "Auth", "score": 5.0, "content": "Fixed bv-1.", "workspace": "/other"}
			],
			"total_matches": 3
		}`), nil
	}

	correlator := NewCorrelator(searcher, nil, "/workspace")
	res := correlator.Correlate(context.Background(), &model.Issue{
		ID:    "api-bv-1",
		Title: "Authentication token validation",
		Origin: &model.IssueOrigin{
			LocalID:          "bv-1",
			WorkingDirectory: "/workspace/services/api",
		},
	})
	if len(res.TopSessions) != 2 {
		t.Fatalf("expected 2 sessions (false-prefix-nokw dropped below threshold), got %+v", res.TopSessions)
	}
	if res.TopSessions[0].SourcePath != "/sessions/true-mention.jsonl" || res.TopSessions[0].BaseScore != ScoreIDMention {
		t.Fatalf("true mention session = %+v, want BaseScore=%d", res.TopSessions[0], ScoreIDMention)
	}
	if res.TopSessions[1].SourcePath != "/sessions/false-prefix-kw.jsonl" || res.TopSessions[1].BaseScore >= ScoreIDMention {
		t.Fatalf("false-prefix session (bv-100) falsely promoted to ID mention: %+v", res.TopSessions[1])
	}
}

func TestSearchByIDInWorkspace_RejectsSeparatorSplitsAndDecrementsTotalFound(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	// 1. Verify searchByIDInWorkspace rejects all false-positive token/separator forms of "bv-1"
	// and decrements Meta.Total to 0, while accepting exact "bv-1" and unsplit truncated snippets.
	falseSnippets := []string{
		"working on bv-10",
		"child task bv-1.2",
		"other repo api-bv-1",
		"snake_case bv_1",
		"dotted bv.1",
		"spaced bv 1",
		"slashed bv/1",
		"punctuated bv, 1",
		"colon bv: 1",
	}
	for _, snippet := range falseSnippets {
		searcher := NewSearcher(detector)
		searcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{
				"hits": [{"source_path": "/s/neg.jsonl", "line_number": 1, "agent": "codex", "title": "Misc", "score": 10.0, "content": %q, "created_at": %d, "workspace": "/work/proj"}],
				"total_matches": 1
			}`, snippet, now.UnixMilli())), nil
		}
		correlator := NewCorrelator(searcher, nil, "/work/proj")
		correlator.now = func() time.Time { return now }
		got, meta := correlator.searchByIDInWorkspace(context.Background(), "bv-1", "/work/proj")
		if len(got) != 0 || meta.Total != 0 {
			t.Fatalf("false positive snippet %q was not rejected/decremented: len=%d meta.Total=%d", snippet, len(got), meta.Total)
		}
	}

	// Positive observable: exact "bv-1" is accepted and preserves producer total_matches.
	posSearcher := NewSearcher(detector)
	posSearcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/pos.jsonl", "line_number": 1, "agent": "codex", "title": "Fix", "score": 10.0, "content": "resolved bv-1 cleanly", "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/sep.jsonl", "line_number": 2, "agent": "codex", "title": "Noise", "score": 8.0, "content": "bv_1 and bv.1 and bv 1", "created_at": %d, "workspace": "/work/proj"}
			],
			"total_matches": 5
		}`, now.UnixMilli(), now.UnixMilli())), nil
	}
	posCorr := NewCorrelator(posSearcher, nil, "/work/proj")
	posCorr.now = func() time.Time { return now }
	posHits, posMeta := posCorr.searchByIDInWorkspace(context.Background(), "bv-1", "/work/proj")
	if len(posHits) != 1 || posHits[0].SourcePath != "/s/pos.jsonl" || posMeta.Total != 4 {
		t.Fatalf("expected 1 accepted hit and Meta.Total=4 (5 - 1 rejected), got hits=%+v meta=%+v", posHits, posMeta)
	}

	// 2. Verify TotalFound decrements on < MinScoreThreshold hits in keyword search (Strategy 2)
	// and Strategy 3 ('*') timestamp search (rejecting >7d same-workspace without ID, accepting <=6d or LocalID mention,
	// and triggering Strategy 3 even when only UpdatedAt is set!).
	kwSearcher := NewSearcher(detector)
	kwSearcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] == `"bv-kw"` {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		// Keyword search: 1 strong hit + 1 old cross-workspace zero-keyword hit (< MinScoreThreshold=30).
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/kw-good.jsonl", "line_number": 1, "title": "Database migration", "content": "database migration schema", "score": 10.0, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/kw-weak.jsonl", "line_number": 2, "title": "Unrelated", "content": "nothing matching", "score": 1.0, "created_at": %d, "workspace": "/elsewhere"}
			],
			"total_matches": 6
		}`, now.Add(-time.Hour).UnixMilli(), now.Add(-60*24*time.Hour).UnixMilli())), nil
	}
	kwCorr := NewCorrelator(kwSearcher, nil, "/work/proj")
	kwCorr.now = func() time.Time { return now }
	kwRes := kwCorr.Correlate(context.Background(), &model.Issue{
		ID:        "bv-kw",
		Title:     "Database migration schema",
		CreatedAt: now.Add(-2 * time.Hour),
	})
	if kwRes.Strategy != StrategyKeywords || len(kwRes.TopSessions) != 1 || kwRes.TotalFound != 5 {
		t.Fatalf("keyword TotalFound decrement failed: Strategy=%q len=%d TotalFound=%d (want 5)", kwRes.Strategy, len(kwRes.TopSessions), kwRes.TotalFound)
	}

	// Strategy 3 ('*'): issue has short/stopword-only title ("To do") so ExtractKeywords is empty,
	// CreatedAt is zero, ClosedAt is nil, and ONLY UpdatedAt is non-zero!
	tsSearcher := NewSearcher(detector)
	tsSearcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] != "*" {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/stale-9d.jsonl", "line_number": 1, "title": "Old chat", "content": "general notes", "score": 5.0, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/recent-6d.jsonl", "line_number": 2, "title": "Recent chat", "content": "general notes", "score": 5.0, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/old-localid.jsonl", "line_number": 3, "title": "Old ID chat", "content": "investigated loc-7 here", "score": 5.0, "created_at": %d, "workspace": "/work/proj"}
			],
			"total_matches": 4
		}`,
			now.Add(-9*24*time.Hour).UnixMilli(),
			now.Add(-6*24*time.Hour).UnixMilli(),
			now.Add(-12*24*time.Hour).UnixMilli(),
		)), nil
	}
	tsCorr := NewCorrelator(tsSearcher, nil, "/work/proj")
	tsCorr.now = func() time.Time { return now }
	tsRes := tsCorr.Correlate(context.Background(), &model.Issue{
		ID:        "proj-loc-7",
		Title:     "To do", // Zero extracted keywords -> falls through to Strategy 3 '*'
		UpdatedAt: now,     // Zero CreatedAt & nil ClosedAt -> exercises !issue.UpdatedAt.IsZero() guard!
		Origin: &model.IssueOrigin{
			LocalID:          "loc-7",
			WorkingDirectory: "/work/proj",
		},
	})
	if tsRes.Strategy != StrategyTimestamp {
		t.Fatalf("expected StrategyTimestamp via UpdatedAt guard, got %+v", tsRes)
	}
	if len(tsRes.TopSessions) != 2 || tsRes.TotalFound != 2 {
		t.Fatalf("Strategy 3 sessions=%+v TotalFound=%d, want 2 sessions (<=6d + LocalID mention) and TotalFound=2 (== len(TopSessions))",
			tsRes.TopSessions, tsRes.TotalFound)
	}
	for _, sr := range tsRes.TopSessions {
		if sr.SourcePath == "/s/stale-9d.jsonl" {
			t.Fatalf("negative control failed: >7d session without ID mention leaked into Strategy 3: %+v", sr)
		}
	}
}

func TestCorrelator_EmbeddedSubtokensRetainedWildcardTotalAndTrackerDirectoryWorkspace(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	// [A-2] Positive observable: issue.ID = "ui-1" with Title = "Fix build in 12ms", Snippet = "...truncated..."
	// ("ui" embedded inside "build" and "1" embedded inside "12ms") MUST be retained as a valid Strategy 1 hit!
	embSearcher := NewSearcher(detector)
	embSearcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{
			"hits": [{"source_path": "/s/ui1-truncated.jsonl", "line_number": 9, "agent": "claude", "title": "Fix build in 12ms", "score": 15.0, "content": "...truncated...", "created_at": %d, "workspace": "/work/proj"}],
			"total_matches": 1
		}`, now.UnixMilli())), nil
	}
	embCorr := NewCorrelator(embSearcher, nil, "/work/proj")
	embCorr.now = func() time.Time { return now }
	embHits, embMeta := embCorr.searchByIDInWorkspace(context.Background(), "ui-1", "/work/proj")
	if len(embHits) != 1 || embMeta.Total != 1 {
		t.Fatalf("[A-2] positive failed: embedded 'ui' in 'build' + '1' in '12ms' falsely rejected: hits=%+v meta=%+v", embHits, embMeta)
	}
	// [R2-A2] Positive observable: standalone tokens separated by an intervening alphanumeric word ("Fix UI step 1") must NOT be rejected!
	stepSearcher := NewSearcher(detector)
	stepSearcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf(`{
			"hits": [{"source_path": "/s/ui1-step.jsonl", "line_number": 9, "agent": "claude", "title": "Fix UI step 1", "score": 15.0, "content": "...truncated...", "created_at": %d, "workspace": "/work/proj"}],
			"total_matches": 1
		}`, now.UnixMilli())), nil
	}
	stepCorr := NewCorrelator(stepSearcher, nil, "/work/proj")
	stepCorr.now = func() time.Time { return now }
	stepHits, stepMeta := stepCorr.searchByIDInWorkspace(context.Background(), "ui-1", "/work/proj")
	if len(stepHits) != 1 || stepMeta.Total != 1 {
		t.Fatalf("[R2-A2] positive failed: intervening-word title 'Fix UI step 1' falsely rejected: hits=%+v meta=%+v", stepHits, stepMeta)
	}

	// [A-2] Planted negative controls: separator-split "ui_1", "ui.1", "ui 1" without "ui-1" MUST be rejected!
	for _, sepSnippet := range []string{"touched ui_1", "touched ui.1", "touched ui 1"} {
		negSearcher := NewSearcher(detector)
		negSearcher.runCommand = func(context.Context, string, ...string) ([]byte, error) {
			return []byte(fmt.Sprintf(`{
				"hits": [{"source_path": "/s/ui1-sep.jsonl", "line_number": 4, "agent": "claude", "title": "Fix build in 12ms", "score": 15.0, "content": %q, "created_at": %d, "workspace": "/work/proj"}],
				"total_matches": 1
			}`, sepSnippet, now.UnixMilli())), nil
		}
		negCorr := NewCorrelator(negSearcher, nil, "/work/proj")
		negCorr.now = func() time.Time { return now }
		negHits, negMeta := negCorr.searchByIDInWorkspace(context.Background(), "ui-1", "/work/proj")
		if len(negHits) != 0 || negMeta.Total != 0 {
			t.Fatalf("[A-2] negative failed: separator-split %q was not rejected: hits=%+v meta=%+v", sepSnippet, negHits, negMeta)
		}
	}

	// [A-5] Strategy 3 ('*') with producer total_matches=500 and 1 session <=7d + 2 sessions >7d reports TotalFound == 1 (== len(TopSessions)).
	wildSearcher := NewSearcher(detector)
	wildSearcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] != "*" {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/near.jsonl", "line_number": 1, "title": "Misc", "content": "notes", "score": 5.0, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/far-1.jsonl", "line_number": 2, "title": "Misc", "content": "notes", "score": 5.0, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/far-2.jsonl", "line_number": 3, "title": "Misc", "content": "notes", "score": 5.0, "created_at": %d, "workspace": "/work/proj"}
			],
			"total_matches": 500
		}`,
			now.Add(-2*24*time.Hour).UnixMilli(),
			now.Add(-20*24*time.Hour).UnixMilli(),
			now.Add(-45*24*time.Hour).UnixMilli(),
		)), nil
	}
	wildCorr := NewCorrelator(wildSearcher, nil, "/work/proj")
	wildCorr.now = func() time.Time { return now }
	closedAt := now.Add(-24 * time.Hour)
	wildRes := wildCorr.Correlate(context.Background(), &model.Issue{
		ID:        "bv-nokw",
		Title:     "To do",
		CreatedAt: now.Add(-60 * 24 * time.Hour),
		ClosedAt:  &closedAt,
	})
	if wildRes.Strategy != StrategyTimestamp || len(wildRes.TopSessions) != 1 || wildRes.TotalFound != 1 {
		t.Fatalf("[A-5] failed: Strategy=%q len=%d TotalFound=%d, want StrategyTimestamp len=1 TotalFound=1 (not inflated 498)",
			wildRes.Strategy, len(wildRes.TopSessions), wildRes.TotalFound)
	}

	// [B-6] workspaceForIssue derives workspace from Origin.TrackerDirectory when Origin.WorkingDirectory is empty.
	memberDir := filepath.Join(t.TempDir(), "services", "billing")
	trackerDir := filepath.Join(memberDir, ".beads")
	if err := os.MkdirAll(trackerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var capturedWorkspace string
	tdSearcher := NewSearcher(detector)
	tdSearcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "--workspace" {
				capturedWorkspace = args[i+1]
			}
		}
		return []byte(fmt.Sprintf(`{
			"hits": [{"source_path": "/s/td.jsonl", "line_number": 1, "title": "Billing", "content": "fixed bd-9", "score": 10.0, "created_at": %d, "workspace": %q}],
			"total_matches": 1
		}`, now.UnixMilli(), memberDir)), nil
	}
	tdCorr := NewCorrelator(tdSearcher, nil, "/umbrella/root")
	tdCorr.now = func() time.Time { return now }
	tdRes := tdCorr.Correlate(context.Background(), &model.Issue{
		ID:    "billing-bd-9",
		Title: "Billing fix",
		Origin: &model.IssueOrigin{
			LocalID:          "bd-9",
			WorkingDirectory: "   ",
			TrackerDirectory: trackerDir,
		},
	})
	if capturedWorkspace != memberDir || len(tdRes.TopSessions) != 1 {
		t.Fatalf("[B-6] TrackerDirectory workspace = %q (want %q), sessions=%+v", capturedWorkspace, memberDir, tdRes.TopSessions)
	}
	// Negative counterpart: when both WorkingDirectory and TrackerDirectory are empty, fall back to c.workspace.
	if gotFallback := tdCorr.workspaceForIssue(&model.Issue{ID: "x", Origin: &model.IssueOrigin{}}); gotFallback != "/umbrella/root" {
		t.Fatalf("[B-6] empty Origin fallback = %q, want /umbrella/root", gotFallback)
	}
	// [R2-A1] Standalone JSONL outside .beads/_beads without tracker metadata (e.g., TrackerDirectory="/tmp") must fall back to c.workspace, NOT "/"!
	if gotStandalone := tdCorr.workspaceForIssue(&model.Issue{
		ID: "x-standalone",
		Origin: &model.IssueOrigin{
			TrackerDirectory: "/tmp",
			ReadOnlyReason:   "source has no readable tracker metadata",
		},
	}); gotStandalone != "/umbrella/root" {
		t.Fatalf("[R2-A1] unvalidated standalone TrackerDirectory=/tmp derived %q, want fallback /umbrella/root", gotStandalone)
	}
}

func TestCorrelator_WithCutoffAndMaxSessions(t *testing.T) {
	cutoff := time.Date(2026, 5, 10, 18, 0, 0, 0, time.UTC)
	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }
	_ = detector.Check()

	// 1. Strategy 1 returns ONLY post-cutoff and undated hits: WithCutoff must reject them BEFORE
	// short-circuiting so Strategy 2 (keywords) runs without --days and finds historical sessions!
	var seenQueries []string
	var seenDaysFlag bool
	fallbackSearcher := NewSearcher(detector)
	fallbackSearcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "search" {
			seenQueries = append(seenQueries, args[1])
		}
		for _, a := range args {
			if a == "--days" {
				seenDaysFlag = true
			}
		}
		if len(args) >= 2 && strings.HasPrefix(args[1], `"`) {
			return []byte(fmt.Sprintf(`{
				"hits": [
					{"source_path": "/s/future-id.jsonl", "line_number": 1, "agent": "claude", "title": "Future", "score": 9.0, "content": "fixed bv-cut", "created_at": %d, "workspace": "/work/proj"},
					{"source_path": "/s/undated-id.jsonl", "line_number": 2, "agent": "claude", "title": "Undated", "score": 9.0, "content": "fixed bv-cut", "workspace": "/work/proj"}
				],
				"total_matches": 2
			}`, cutoff.Add(2*time.Hour).UnixMilli())), nil
		}
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/future-kw.jsonl", "line_number": 1, "agent": "claude", "title": "OAuth", "score": 9.0, "content": "oauth token refresh", "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/undated-kw.jsonl", "line_number": 2, "agent": "claude", "title": "OAuth", "score": 9.0, "content": "oauth token refresh", "workspace": "/work/proj"},
				{"source_path": "/s/hist-kw-1.jsonl", "line_number": 3, "agent": "claude", "title": "OAuth", "score": 8.0, "content": "oauth token refresh", "created_at": %d, "workspace": "/work/proj"}
			],
			"total_matches": 99
		}`, cutoff.Add(time.Minute).UnixMilli(), cutoff.Add(-48*time.Hour).UnixMilli())), nil
	}

	cutoffCorr := NewCorrelatorWithOptions(
		fallbackSearcher,
		nil,
		WithWorkspace("/work/proj"),
		WithCutoff(cutoff),
		WithNow(func() time.Time { return cutoff }),
	)
	res := cutoffCorr.Correlate(context.Background(), &model.Issue{
		ID:        "bv-cut",
		Title:     "OAuth token refresh",
		CreatedAt: cutoff.Add(-72 * time.Hour),
	})
	if seenDaysFlag {
		t.Fatalf("WithCutoff passed --days to cass search (would anchor to wall-clock today instead of historical cutoff)")
	}
	if len(seenQueries) < 2 {
		t.Fatalf("expected Strategy 1 post-cutoff/undated hits to fall through to Strategy 2, queries=%v", seenQueries)
	}
	if res.Strategy != StrategyKeywords || len(res.TopSessions) != 1 || res.TopSessions[0].SourcePath != "/s/hist-kw-1.jsonl" {
		t.Fatalf("expected 1 historical keyword hit, got strategy=%q sessions=%+v", res.Strategy, res.TopSessions)
	}
	if res.TotalFound != 1 {
		t.Fatalf("TotalFound = %d, want 1 (excluding post-cutoff and undated hits)", res.TotalFound)
	}

	// 2. WithMaxSessions(5) returns 5 ranked sessions instead of truncating at MaxSessionsReturned (3).
	maxSearcher := NewSearcher(detector)
	maxSearcher.runCommand = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		var rows []string
		for i := 1; i <= 6; i++ {
			rows = append(rows, fmt.Sprintf(
				`{"source_path": "/s/s%d.jsonl", "line_number": %d, "agent": "claude", "title": "Hit %d", "score": %d.0, "content": "fixed bv-max and bv-fresh", "created_at": %d, "workspace": "/work/proj"}`,
				i, i, i, 10-i, cutoff.Add(-time.Duration(i)*time.Hour).UnixMilli(),
			))
		}
		return []byte(fmt.Sprintf(`{"hits": [%s], "total_matches": 6}`, strings.Join(rows, ","))), nil
	}
	maxCorr := NewCorrelatorWithOptions(
		maxSearcher,
		nil,
		WithWorkspace("/work/proj"),
		WithMaxSessions(5),
		WithCutoff(cutoff),
		WithNow(func() time.Time { return cutoff }),
	)
	maxRes := maxCorr.Correlate(context.Background(), &model.Issue{ID: "bv-max", Title: "Max sessions check"})
	if len(maxRes.TopSessions) != 5 || maxRes.TotalFound != 6 {
		t.Fatalf("WithMaxSessions(5): len(TopSessions)=%d TotalFound=%d, want 5 and 6", len(maxRes.TopSessions), maxRes.TotalFound)
	}

	// 3. [R2-D5] Shared Cache isolation: WithCutoff and non-default WithMaxSessions must neither
	// read stale default 3-item cached entries nor write cutoff/custom-limit results back into Cache.
	sharedCache := NewCache()
	defaultCorr := NewCorrelatorWithOptions(maxSearcher, sharedCache, WithWorkspace("/work/proj"), WithNow(func() time.Time { return cutoff }))
	defRes := defaultCorr.Correlate(context.Background(), &model.Issue{ID: "bv-max", Title: "Max sessions check"})
	if len(defRes.TopSessions) != MaxSessionsReturned {
		t.Fatalf("default Correlator cached len=%d, want %d", len(defRes.TopSessions), MaxSessionsReturned)
	}
	cachedMaxCorr := NewCorrelatorWithOptions(maxSearcher, sharedCache, WithWorkspace("/work/proj"), WithMaxSessions(5), WithNow(func() time.Time { return cutoff }))
	cachedMaxRes := cachedMaxCorr.Correlate(context.Background(), &model.Issue{ID: "bv-max", Title: "Max sessions check"})
	if len(cachedMaxRes.TopSessions) != 5 {
		t.Fatalf("WithMaxSessions(5) read stale 3-item cache: len=%d, want 5", len(cachedMaxRes.TopSessions))
	}
	strictCutoff := cutoff.Add(-10 * time.Hour)
	strictCorr := NewCorrelatorWithOptions(maxSearcher, sharedCache, WithWorkspace("/work/proj"), WithCutoff(strictCutoff), WithNow(func() time.Time { return strictCutoff }))
	strictRes := strictCorr.Correlate(context.Background(), &model.Issue{ID: "bv-fresh", Title: "Fresh cache check"})
	if len(strictRes.TopSessions) != 0 {
		t.Fatalf("expected 0 sessions before strictCutoff, got %d", len(strictRes.TopSessions))
	}
	if hint := sharedCache.Get("bv-fresh"); hint != nil {
		t.Fatalf("WithCutoff poisoned sharedCache with %+v", hint)
	}
	freshAfterStrict := defaultCorr.Correlate(context.Background(), &model.Issue{ID: "bv-fresh", Title: "Fresh cache check"})
	if len(freshAfterStrict.TopSessions) != MaxSessionsReturned {
		t.Fatalf("expected unfiltered Correlator after WithCutoff to return %d sessions, got %d", MaxSessionsReturned, len(freshAfterStrict.TopSessions))
	}
}

func TestSearchByTimestamp_PreservesPreFilterTotalHits(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	detector := NewDetector()
	detector.lookPath = func(string) (string, error) { return "/usr/bin/cass", nil }
	detector.runCommand = func(context.Context, string, ...string) (int, error) { return 0, nil }

	searcher := NewSearcher(detector)
	searcher.runCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[1] != "*" {
			return []byte(`{"hits": [], "total_matches": 0}`), nil
		}
		// 5 valid recent sessions (all within 7d) + 1 far rejected session (>7d)
		return []byte(fmt.Sprintf(`{
			"hits": [
				{"source_path": "/s/t1.jsonl", "line_number": 1, "title": "s1", "content": "notes", "score": 10, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/t2.jsonl", "line_number": 1, "title": "s2", "content": "notes", "score": 9, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/t3.jsonl", "line_number": 1, "title": "s3", "content": "notes", "score": 8, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/t4.jsonl", "line_number": 1, "title": "s4", "content": "notes", "score": 7, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/t5.jsonl", "line_number": 1, "title": "s5", "content": "notes", "score": 6, "created_at": %d, "workspace": "/work/proj"},
				{"source_path": "/s/far.jsonl", "line_number": 1, "title": "far", "content": "notes", "score": 5, "created_at": %d, "workspace": "/work/proj"}
			],
			"total_matches": 100
		}`,
			now.Add(-1*time.Hour).UnixMilli(),
			now.Add(-2*time.Hour).UnixMilli(),
			now.Add(-3*time.Hour).UnixMilli(),
			now.Add(-4*time.Hour).UnixMilli(),
			now.Add(-5*time.Hour).UnixMilli(),
			now.Add(-30*24*time.Hour).UnixMilli(),
		)), nil
	}

	corr := NewCorrelator(searcher, nil, "/work/proj")
	corr.now = func() time.Time { return now }
	closedAt := now.Add(-30 * time.Minute)
	res := corr.Correlate(context.Background(), &model.Issue{
		ID:        "bv-ts5",
		Title:     "To do",
		CreatedAt: now.Add(-15 * 24 * time.Hour),
		ClosedAt:  &closedAt,
	})
	if res.Strategy != StrategyTimestamp {
		t.Fatalf("Strategy = %q, want StrategyTimestamp", res.Strategy)
	}
	if len(res.TopSessions) != MaxSessionsReturned {
		t.Fatalf("len(TopSessions) = %d, want %d", len(res.TopSessions), MaxSessionsReturned)
	}
	if res.TotalFound != 5 {
		t.Fatalf("TotalFound = %d, want 5 (pre-limit scored count, not capped at %d)", res.TotalFound, MaxSessionsReturned)
	}
}
