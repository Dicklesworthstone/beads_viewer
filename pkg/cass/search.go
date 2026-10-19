package cass

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// DefaultSearchTimeout is the default timeout for search operations.
const DefaultSearchTimeout = 5 * time.Second

// DefaultSearchLimit is the default max number of results.
const DefaultSearchLimit = 10

// MaxOutputSize is the max bytes to read from cass output (1MB).
const MaxOutputSize = 1024 * 1024

// MaxConcurrentSearches limits concurrent cass processes.
const MaxConcurrentSearches = 2

// SearchOptions configures a cass search operation.
type SearchOptions struct {
	Query     string        // Required: search query
	Limit     int           // Max results (default 10)
	Days      int           // Time filter (0 = no filter)
	Workspace string        // Filter by workspace path
	Fields    string        // "minimal", "summary", or field list
	Timeout   time.Duration // Override default timeout
}

// SearchResult represents a single search hit from cass.
type SearchResult struct {
	SourcePath string    `json:"source_path"` // Path to session file
	LineNumber int       `json:"line_number"` // Line in session
	Agent      string    `json:"agent"`       // "claude", "cursor", etc.
	Title      string    `json:"title"`       // Conversation title
	Score      float64   `json:"score"`       // Producer's relevance score (not normalized)
	Snippet    string    `json:"snippet"`     // Content preview
	Timestamp  time.Time `json:"timestamp"`   // When the message occurred
	MatchType  string    `json:"match_type"`  // "exact", "prefix", "fuzzy"
	Workspace  string    `json:"workspace"`   // Project path; distinct from the session file
}

// SearchMeta contains metadata about the search operation.
type SearchMeta struct {
	ElapsedMs int    `json:"elapsed_ms"`
	Total     int    `json:"total"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"` // Non-empty if partial failure
	// TimedOut reports that bv's own deadline stopped the search: the search
	// did not finish, which is different from finding no sessions.
	TimedOut bool `json:"timed_out,omitempty"`
}

// SearchResponse contains results and metadata from a cass search.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Meta    SearchMeta     `json:"meta"`
}

// Searcher executes cass searches with safety wrappers.
// It enforces timeouts, limits concurrent processes, and handles errors gracefully.
type Searcher struct {
	detector  *Detector
	semaphore chan struct{}
	timeout   time.Duration

	// For testing: allow overriding command execution
	runCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewSearcher creates a new Searcher that uses the provided detector.
func NewSearcher(detector *Detector) *Searcher {
	return &Searcher{
		detector:   detector,
		semaphore:  make(chan struct{}, MaxConcurrentSearches),
		timeout:    DefaultSearchTimeout,
		runCommand: defaultSearchRunCommand,
	}
}

// SearcherOption configures a Searcher.
type SearcherOption func(*Searcher)

// WithSearchTimeout sets the default search timeout.
func WithSearchTimeout(timeout time.Duration) SearcherOption {
	return func(s *Searcher) {
		s.timeout = timeout
	}
}

// NewSearcherWithOptions creates a Searcher with custom options.
func NewSearcherWithOptions(detector *Detector, opts ...SearcherOption) *Searcher {
	s := NewSearcher(detector)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Search executes a cass search with the given options.
// It returns an empty response (not error) on any failure - errors are logged internally.
// This method is safe for concurrent use.
func (s *Searcher) Search(ctx context.Context, opts SearchOptions) SearchResponse {
	// Archive health is advisory: a stale or rebuilding index may still serve
	// searches. Keep the warning, but let the bounded search establish usability.
	status := s.detector.Check()
	if status != StatusHealthy && status != StatusNeedsIndex {
		return SearchResponse{
			Results: []SearchResult{},
			Meta:    SearchMeta{Error: "cass not available"},
		}
	}

	// Acquire semaphore to limit concurrent processes
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		meta := SearchMeta{Error: "context cancelled waiting for semaphore"}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			meta.Error = "search timed out waiting for a free cass slot"
			meta.TimedOut = true
		}
		return SearchResponse{Results: []SearchResult{}, Meta: meta}
	}

	// Determine timeout
	timeout := s.timeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	// Create context with timeout
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build command arguments
	args := s.buildArgs(opts)

	// Execute search
	start := time.Now()
	output, err := s.runCommand(searchCtx, "cass", args...)
	elapsed := time.Since(start)

	if err != nil {
		meta := SearchMeta{
			ElapsedMs: int(elapsed.Milliseconds()),
			Error:     "search execution failed",
		}
		// The deadline is this search's own timeout or the caller's budget,
		// whichever came first; either way the search did not complete.
		if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			meta.Error = "search timed out after " + elapsed.Round(time.Millisecond).String()
			meta.TimedOut = true
		}
		return SearchResponse{Results: []SearchResult{}, Meta: meta}
	}

	// Parse response
	return s.parseResponse(output, int(elapsed.Milliseconds()))
}

// buildArgs constructs command line arguments for cass search.
func (s *Searcher) buildArgs(opts SearchOptions) []string {
	args := []string{"search", opts.Query, "--robot", "--robot-format", "json"}

	// Limit
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	args = append(args, "--limit", strconv.Itoa(limit))

	// Fields
	fields := opts.Fields
	if fields == "" {
		fields = "source_path,line_number,agent,title,score,content,created_at,match_type,workspace"
	}
	args = append(args, "--fields", fields, "--max-content-length", "4000")

	// Days filter
	if opts.Days > 0 {
		args = append(args, "--days", strconv.Itoa(opts.Days))
	}

	// Workspace filter
	if opts.Workspace != "" {
		args = append(args, "--workspace", opts.Workspace)
	}

	return args
}

// parseResponse parses cass JSON output into a SearchResponse.
// It handles malformed JSON gracefully by returning empty results.
func (s *Searcher) parseResponse(output []byte, elapsedMs int) SearchResponse {
	resp := SearchResponse{Results: []SearchResult{}, Meta: SearchMeta{ElapsedMs: elapsedMs}}
	// Keep the external protocol separate from the normalized UI/cache types.
	var wire struct {
		Hits []struct {
			SourcePath string  `json:"source_path"`
			LineNumber int     `json:"line_number"`
			Agent      string  `json:"agent"`
			Title      string  `json:"title"`
			Score      float64 `json:"score"`
			Content    string  `json:"content"`
			CreatedAt  *int64  `json:"created_at"`
			MatchType  string  `json:"match_type"`
			Workspace  string  `json:"workspace"`
		} `json:"hits"`
		TotalMatches int  `json:"total_matches"`
		HitsClamped  bool `json:"hits_clamped"`
		Budget       struct {
			TimedOut bool `json:"timed_out"`
		} `json:"budget"`
	}
	if err := json.Unmarshal(output, &wire); err != nil || wire.Hits == nil {
		resp.Meta.Error = "failed to parse cass hits"
		return resp
	}
	resp.Meta.Total = wire.TotalMatches
	resp.Meta.Truncated = wire.HitsClamped || wire.TotalMatches > len(wire.Hits) || wire.Budget.TimedOut
	if wire.Budget.TimedOut {
		resp.Meta.Error = "cass search budget exhausted"
	}
	for _, hit := range wire.Hits {
		var timestamp time.Time
		if hit.CreatedAt != nil {
			timestamp = time.UnixMilli(*hit.CreatedAt).UTC()
		}
		resp.Results = append(resp.Results, SearchResult{
			SourcePath: hit.SourcePath, LineNumber: hit.LineNumber, Agent: hit.Agent,
			Title: hit.Title, Score: hit.Score, Snippet: hit.Content,
			Timestamp: timestamp, MatchType: hit.MatchType, Workspace: hit.Workspace,
		})
	}

	return resp
}

// SearchWithQuery is a convenience method for simple searches.
func (s *Searcher) SearchWithQuery(ctx context.Context, query string) SearchResponse {
	return s.Search(ctx, SearchOptions{Query: query})
}

// SearchInWorkspace searches within a specific workspace directory.
func (s *Searcher) SearchInWorkspace(ctx context.Context, query, workspace string) SearchResponse {
	return s.Search(ctx, SearchOptions{
		Query:     query,
		Workspace: workspace,
	})
}

// defaultSearchRunCommand executes a command and returns its stdout.
func defaultSearchRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	// Use a limited reader for stdout
	var stdout bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: MaxOutputSize}

	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	return stdout.Bytes(), nil
}

// limitedWriter wraps a writer and limits total bytes written.
type limitedWriter struct {
	w       io.Writer
	limit   int
	written int
}

func (lw *limitedWriter) Write(p []byte) (n int, err error) {
	remaining := lw.limit - lw.written
	if remaining <= 0 {
		return len(p), nil // Silently discard, return original length
	}
	toWrite := p
	if len(p) > remaining {
		toWrite = p[:remaining]
	}
	written, err := lw.w.Write(toWrite)
	lw.written += written
	if err != nil {
		return written, err
	}
	return len(p), nil // Return original length - we "handled" all data
}
