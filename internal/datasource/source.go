// Package datasource provides intelligent multi-source data detection and selection
// for beads_viewer. It discovers, validates, and selects the freshest valid source
// from SQLite databases, worktree JSONL files, and local JSONL files.
package datasource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
)

// SourceType identifies the type of data source
type SourceType string

const (
	// SourceTypeSQLite is a SQLite database (beads.db)
	SourceTypeSQLite SourceType = "sqlite"
	// SourceTypeJSONLWorktree is a JSONL file from a git worktree
	SourceTypeJSONLWorktree SourceType = "jsonl_worktree"
	// SourceTypeJSONLLocal is a local JSONL file
	SourceTypeJSONLLocal SourceType = "jsonl_local"
)

// Priority values for source types (higher = more authoritative)
const (
	PrioritySQLite        = 100
	PriorityJSONLWorktree = 80
	PriorityJSONLLocal    = 50
)

// DataSource represents a potential source of beads data
type DataSource struct {
	// Type identifies the source type
	Type SourceType `json:"type"`
	// Path is the absolute path to the source file
	Path string `json:"path"`
	// Priority determines preference when timestamps are equal (higher = preferred)
	Priority int `json:"priority"`
	// ModTime is the last modification time of the source
	ModTime time.Time `json:"mod_time"`
	// Valid indicates whether the source passed validation
	Valid bool `json:"valid"`
	// ValidationError describes why validation failed (if Valid is false)
	ValidationError string `json:"validation_error,omitempty"`
	// IssueCount is the number of issues in the source (set during validation)
	IssueCount int `json:"issue_count"`
	// Size is the file size in bytes
	Size int64 `json:"size"`
}

// String returns a human-readable description of the source
func (s DataSource) String() string {
	status := "valid"
	if !s.Valid {
		status = fmt.Sprintf("invalid: %s", s.ValidationError)
	}
	return fmt.Sprintf("%s (%s, priority=%d, mod=%s, issues=%d, %s)",
		s.Path, s.Type, s.Priority, s.ModTime.Format(time.RFC3339), s.IssueCount, status)
}

// DiscoveryOptions configures source discovery behavior
type DiscoveryOptions struct {
	// BeadsDir is the .beads directory path (optional, auto-detected if empty)
	BeadsDir string
	// RepoPath is the repository root path (optional, uses cwd if empty)
	RepoPath string
	// ValidateAfterDiscovery runs validation on each discovered source
	ValidateAfterDiscovery bool
	// IncludeInvalid includes sources that failed validation in results
	IncludeInvalid bool
	// SkipWorktreeSources confines discovery to BeadsDir. Explicit BEADS_DB,
	// BEADS_DIR, and --db directory selectors use this so a newer export under
	// the caller's unrelated Git administrative directory cannot override the
	// selected tracker authority.
	SkipWorktreeSources bool
	// Verbose enables detailed logging during discovery
	Verbose bool
	// Logger receives log messages when Verbose is true
	Logger func(msg string)
}

// DiscoverSources finds all potential data sources in the beads directory
func DiscoverSources(opts DiscoveryOptions) ([]DataSource, error) {
	if opts.Logger == nil {
		opts.Logger = func(string) {}
	}

	// Determine beads directory
	// Priority: opts.BeadsDir (set by caller, e.g. from --db) > BEADS_DB env > BEADS_DIR env > auto-discovery
	beadsDir := opts.BeadsDir
	if beadsDir == "" {
		if source, ok, err := ExplicitBeadsDBSource(); err != nil {
			return nil, err
		} else if ok {
			sources := []DataSource{source}
			if opts.ValidateAfterDiscovery {
				if err := ValidateSource(&sources[0]); err != nil && opts.Verbose {
					opts.Logger(fmt.Sprintf("Validation failed for %s: %v", sources[0].Path, err))
				}
				if !opts.IncludeInvalid && !sources[0].Valid {
					return nil, nil
				}
			}
			if opts.Verbose {
				opts.Logger(fmt.Sprintf("Discovered explicit BEADS_DB source: %s", sources[0].Path))
			}
			return sources, nil
		}

		var err error
		beadsDir, err = loader.GetBeadsDir(opts.RepoPath)
		if err != nil {
			return nil, fmt.Errorf("resolve beads directory: %w", err)
		}
	}

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("Discovering sources in: %s", beadsDir))
	}

	var sources []DataSource

	// Discover SQLite database
	sqliteSources, err := discoverSQLiteSources(beadsDir, opts)
	if err != nil {
		return nil, err
	}
	sources = append(sources, sqliteSources...)

	// Discover local JSONL files
	localSources, err := discoverLocalJSONLSources(beadsDir, opts)
	if err != nil {
		return nil, err
	}
	sources = append(sources, localSources...)

	// Discover worktree JSONL files unless the caller selected a concrete
	// tracker directory whose authority must remain self-contained.
	if !opts.SkipWorktreeSources {
		worktreeSources, err := discoverWorktreeSources(opts.RepoPath, opts)
		if err != nil {
			return nil, err
		}
		sources = append(sources, worktreeSources...)
	}

	// Validate sources if requested
	if opts.ValidateAfterDiscovery {
		for i := range sources {
			if err := ValidateSource(&sources[i]); err != nil && opts.Verbose {
				opts.Logger(fmt.Sprintf("Validation failed for %s: %v", sources[i].Path, err))
			}
		}
	}

	// Filter out invalid sources if not including them
	if opts.ValidateAfterDiscovery && !opts.IncludeInvalid {
		var validSources []DataSource
		for _, s := range sources {
			if s.Valid {
				validSources = append(validSources, s)
			}
		}
		sources = validSources
	}

	// Use the same deterministic authority order as selection and loading.
	sortByFreshnessThenPriority(sources)

	if opts.Verbose {
		opts.Logger(fmt.Sprintf("Discovered %d sources", len(sources)))
	}

	return sources, nil
}

// resolveBeadsDBPath interprets a BEADS_DB value which can be either a file path or directory path.
// If it points to a file (or looks like one), returns the parent directory.
// If it points to a directory, returns the directory itself.
func resolveBeadsDBPath(dbPath string) string {
	info, err := os.Stat(dbPath)
	if err != nil {
		// Path doesn't exist -- guess based on extension
		if _, _, ok := explicitBeadsDBFileType(dbPath); ok {
			return filepath.Dir(dbPath)
		}
		return dbPath
	}
	if info.IsDir() {
		return dbPath
	}
	return filepath.Dir(dbPath)
}

// discoverSQLiteSources finds SQLite databases in the beads directory
func discoverSQLiteSources(beadsDir string, opts DiscoveryOptions) ([]DataSource, error) {
	var sources []DataSource

	// Look for beads.db
	dbPath := filepath.Join(beadsDir, "beads.db")
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat SQLite source %s: %w", dbPath, err)
	}
	// In WAL mode, a committed transaction can advance beads.db-wal while the
	// main database file keeps its older checkpoint timestamp. Treat the newest
	// of the pair as SQLite's freshness or smart selection can receive a WAL
	// watcher event and still choose an older JSONL export.
	modTime := info.ModTime()
	walPath := dbPath + "-wal"
	if walInfo, walErr := os.Stat(walPath); walErr == nil {
		if walInfo.ModTime().After(modTime) {
			modTime = walInfo.ModTime()
		}
	} else if !os.IsNotExist(walErr) {
		return nil, fmt.Errorf("stat SQLite WAL source %s: %w", walPath, walErr)
	}
	sources = append(sources, DataSource{
		Type:     SourceTypeSQLite,
		Path:     dbPath,
		Priority: PrioritySQLite,
		ModTime:  modTime,
		Size:     info.Size(),
	})
	if opts.Verbose {
		opts.Logger(fmt.Sprintf("Found SQLite: %s (mod=%s)", dbPath, modTime.Format(time.RFC3339)))
	}

	return sources, nil
}

// discoverLocalJSONLSources finds JSONL files in the beads directory
func discoverLocalJSONLSources(beadsDir string, opts DiscoveryOptions) ([]DataSource, error) {
	var sources []DataSource

	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read beads directory: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()

		// Must be a .jsonl file
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		// Skip backups, merge artifacts, and deletion manifests
		if strings.Contains(name, ".backup") ||
			strings.Contains(name, ".orig") ||
			strings.Contains(name, ".merge") ||
			name == "deletions.jsonl" ||
			strings.HasPrefix(name, "beads.left") ||
			strings.HasPrefix(name, "beads.right") {
			continue
		}

		path := filepath.Join(beadsDir, name)
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("stat JSONL source %s: %w", path, err)
		}

		sources = append(sources, DataSource{
			Type:     SourceTypeJSONLLocal,
			Path:     path,
			Priority: PriorityJSONLLocal,
			ModTime:  info.ModTime(),
			Size:     info.Size(),
		})

		if opts.Verbose {
			opts.Logger(fmt.Sprintf("Found local JSONL: %s (mod=%s)", path, info.ModTime().Format(time.RFC3339)))
		}
	}

	return sources, nil
}

// discoverWorktreeSources finds JSONL files in git worktree beads directories
func discoverWorktreeSources(repoPath string, opts DiscoveryOptions) ([]DataSource, error) {
	if repoPath == "" {
		var err error
		repoPath, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to get current directory: %w", err)
		}
	}

	// Resolve the Git directory through the loader's repository-pinned command
	// path. Ambient GIT_DIR/GIT_WORK_TREE values must not redirect discovery to
	// another repository's issue exports.
	gitDir, err := loader.GetGitDir(repoPath)
	if err != nil {
		// Not a git repository
		return nil, nil
	}

	// Look for beads-worktrees directory
	worktreesDir := filepath.Join(gitDir, "beads-worktrees")
	if _, err := os.Stat(worktreesDir); err != nil {
		if os.IsNotExist(err) {
			// No worktrees directory
			return nil, nil
		}
		return nil, fmt.Errorf("stat beads worktrees directory %s: %w", worktreesDir, err)
	}

	var sources []DataSource

	// Enumerate worktree directories
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read worktrees directory: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		wtDir := filepath.Join(worktreesDir, e.Name())

		// Look for issues.jsonl in this worktree
		jsonlPath := filepath.Join(wtDir, "issues.jsonl")
		info, err := os.Stat(jsonlPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat worktree JSONL source %s: %w", jsonlPath, err)
		}

		sources = append(sources, DataSource{
			Type:     SourceTypeJSONLWorktree,
			Path:     jsonlPath,
			Priority: PriorityJSONLWorktree,
			ModTime:  info.ModTime(),
			Size:     info.Size(),
		})

		if opts.Verbose {
			opts.Logger(fmt.Sprintf("Found worktree JSONL: %s (mod=%s)", jsonlPath, info.ModTime().Format(time.RFC3339)))
		}
	}

	return sources, nil
}
