package cass

import (
	"context"
	"errors"
	"fmt"
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
	tests := []struct {
		name     string
		input    string
		expected string
	}{
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WorkspaceFromBeadsPath(tt.input)
			if result != tt.expected {
				t.Errorf("WorkspaceFromBeadsPath(%q) = %q, want %q", tt.input, result, tt.expected)
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

	t.Run("closed issue", func(t *testing.T) {
		closedAt := now.Add(-10 * 24 * time.Hour)
		issue := &model.Issue{
			CreatedAt: now.Add(-20 * 24 * time.Hour),
			ClosedAt:  &closedAt,
		}

		days := correlator.calculateSearchDays(issue)
		// Should be (20-10) + 7 = 17 days
		expected := 17
		if days != expected {
			t.Errorf("calculateSearchDays for closed issue = %d, want %d", days, expected)
		}
	})

	t.Run("open issue", func(t *testing.T) {
		issue := &model.Issue{
			CreatedAt: now.Add(-14 * 24 * time.Hour),
		}

		days := correlator.calculateSearchDays(issue)
		if days != 14 {
			t.Errorf("calculateSearchDays for open issue = %d, want 14", days)
		}
	})

	t.Run("very old issue capped", func(t *testing.T) {
		issue := &model.Issue{
			CreatedAt: now.Add(-365 * 24 * time.Hour),
		}

		days := correlator.calculateSearchDays(issue)
		if days != 90 {
			t.Errorf("calculateSearchDays for old issue = %d, want 90 (capped)", days)
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
