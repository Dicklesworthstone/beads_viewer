package datasource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// maxLoadReportWarnings caps how many per-line warning messages a LoadReport
// retains, so a pathologically corrupt file cannot bloat robot payloads.
const maxLoadReportWarnings = 10

// LoadReport captures accounting for one source load,
// so callers (robot payload emitters in particular) can surface
// records that were dropped during load instead of silently reporting them as
// nonexistent (#190). Errors counts issue-shaped lines that were malformed JSON
// or failed model validation (e.g. updated_at < created_at), plus later records
// that repeat an earlier issue ID; each such skip also contributes a
// human-readable reason to Warnings (capped).
type LoadReport struct {
	// Path is the source file the report describes.
	Path string
	// Valid is the number of unique issue lines that parsed and validated.
	Valid int
	// Errors is the number of issue-shaped lines dropped as malformed JSON or
	// failed model validation, or rejected because the issue ID was duplicated.
	Errors int
	// Skipped is the number of recognized non-issue `_type` records.
	Skipped int
	// Warnings holds up to maxLoadReportWarnings skip reasons, in file order.
	Warnings     []string
	WarningCount int
	// ReadErrors counts failed related-record reads separately from dropped
	// issue rows, so Valid + Errors + Skipped continues to reconcile.
	ReadErrors        int
	AuthorityWarnings []string
	Stale             bool
	// TombstoneIDs preserves satisfied dependencies whose records are hidden
	// from the viewer. These IDs come from the same successful source read.
	TombstoneIDs []string
}

// LoadResult binds visible issues, selected source, and accounting from the
// same read. A later load cannot replace this result's source authority.
type LoadResult struct {
	Issues []model.Issue
	Source DataSource
	Report LoadReport
}

func cloneLoadReport(report LoadReport) LoadReport {
	report.Warnings = append([]string(nil), report.Warnings...)
	report.AuthorityWarnings = append([]string(nil), report.AuthorityWarnings...)
	report.TombstoneIDs = append([]string(nil), report.TombstoneIDs...)
	return report
}

func (r *LoadReport) addWarning(message string) {
	r.WarningCount++
	if len(r.Warnings) < maxLoadReportWarnings {
		r.Warnings = append(r.Warnings, message)
	}
}

// loadRecorder wires a single JSONL parse to a LoadReport: it collects
// ParseStats plus the loader's per-line skip warnings, mirroring the default
// warning behavior (stderr in interactive mode, quiet under BV_ROBOT=1 so
// robot stdout/stderr stay clean — the accounting surfaces in the JSON
// payload instead).
type loadRecorder struct {
	path         string
	stats        loader.ParseStats
	warnings     []string // capped copy kept for the LoadReport
	warningCount int
	pending      []string // every warning, replayed to stderr only if this candidate is selected
	robot        bool
}

func newLoadRecorder(path string) *loadRecorder {
	return &loadRecorder{path: path, robot: env.Robot.Bool()}
}

// options wires the parse to this recorder. Warnings are buffered rather than
// printed: the smart loader probes candidates freshest-first and discards the
// ones that fail the gate, and a discarded candidate's warnings must never
// reach the user (they used to print "skipping invalid issue on line 1" for a
// file that was then rejected). finish replays them for the selected source.
func (r *loadRecorder) options() loader.ParseOptions {
	return loader.ParseOptions{
		Stats:      &r.stats,
		BufferSize: loader.MaxLineSizeFromEnv(),
		WarningHandler: func(msg string) {
			r.warningCount++
			if len(r.warnings) < maxLoadReportWarnings {
				r.warnings = append(r.warnings, msg)
			}
			if !r.robot {
				r.pending = append(r.pending, msg)
			}
		},
	}
}

// finish returns this read's accounting. Only a selected source replays its
// buffered warnings to stderr in interactive mode; rejected-source accounting
// remains available to the caller without printing it as the selected source.
func (r *loadRecorder) finish(issues []model.Issue, selected bool) LoadReport {
	if selected {
		for _, msg := range r.pending {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}
	r.pending = nil
	return LoadReport{
		Path:         r.path,
		Valid:        r.stats.Valid,
		Errors:       r.stats.Errors,
		Skipped:      r.stats.Skipped,
		Warnings:     append([]string(nil), r.warnings...),
		WarningCount: r.warningCount,
		TombstoneIDs: tombstoneIDs(issues),
	}
}

func tombstoneIDs(issues []model.Issue) []string {
	var ids []string
	for _, issue := range issues {
		if issue.Status.IsTombstone() {
			ids = append(ids, issue.ID)
		}
	}
	return ids
}

// LoadIssues performs smart multi-source detection and loading.
// It discovers all available sources (SQLite, JSONL), validates them, selects
// the freshest valid source, and loads issues from it. SQLite is preferred over
// JSONL when both exist at comparable freshness, since SQLite reflects the most
// recent state (including status changes from br operations).
//
// Falls back to legacy JSONL-only loading via loader.LoadIssues if smart
// detection finds no valid sources.
func LoadIssues(repoPath string) (result LoadResult, err error) {
	defer func() { result.bindOrigins(err) }()
	if source, ok, err := ExplicitBeadsDBSource(); err != nil {
		return LoadResult{}, err
	} else if ok {
		return LoadFromSource(source)
	}

	beadsDir, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		return LoadResult{}, err
	}

	// bd/Dolt workspaces (#189): the issue data lives in a Dolt database that
	// bv cannot read directly, so route through the bd bridge added for #141.
	// It refreshes the compatibility export at .beads/issues.jsonl via
	// `bd export` when possible and errors loudly when no export exists —
	// instead of silently reporting an empty project.
	if loader.IsBDWorkspace(beadsDir) {
		return loadBDWorkspace(beadsDir)
	}

	issues, smartErr := loadSmart(beadsDir, repoPath)
	if smartErr == nil {
		return issues, nil
	}

	// Fall back to legacy JSONL-only loading
	fallback, err := loadLegacyJSONL(beadsDir)
	if fallback.Source.Path != issues.Source.Path && issues.Source.Path != "" {
		fallback.Report.Stale = true
		fallback.Report.AuthorityWarnings = append(fallback.Report.AuthorityWarnings, fmt.Sprintf("source selection failed before fallback: %v", smartErr))
	}
	return fallback, err
}

// loadLegacyJSONL keeps the tolerant parse while returning its accounting and
// retaining tombstone authority separately from visible issue rows.
// This path is reached when the smart loader rejected every candidate — e.g. a
// small JSONL whose only records fail validation trips the error-rate gate —
// which is exactly when dropped records MUST stay visible instead of the load
// silently yielding fewer (or zero) issues (#190).
func loadLegacyJSONL(beadsDir string) (LoadResult, error) {
	jsonlPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return LoadResult{}, err
	}
	rec := newLoadRecorder(jsonlPath)
	issues, err := loader.LoadIssuesFromFileWithOptions(jsonlPath, rec.options())
	report := rec.finish(issues, err == nil)
	visible := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if !issue.Status.IsTombstone() {
			visible = append(visible, issue)
		}
	}
	return LoadResult{
		Issues: visible,
		Source: DataSource{Type: SourceTypeJSONLLocal, Path: jsonlPath, Priority: PriorityJSONLLocal},
		Report: report,
	}, err
}

// LoadIssuesFromDir performs smart source detection within a known beads directory.
// This is useful when the caller already knows the .beads path.
func LoadIssuesFromDir(beadsDir string) (result LoadResult, err error) {
	defer func() { result.bindOrigins(err) }()
	// bd/Dolt workspaces (#189): see LoadIssues.
	if loader.IsBDWorkspace(beadsDir) {
		return loadBDWorkspace(beadsDir)
	}

	issues, smartErr := loadSmart(beadsDir, "")
	if smartErr == nil {
		return issues, nil
	}

	// Fall back to JSONL (legacy tolerant parse, with load accounting; #190)
	fallback, err := loadLegacyJSONL(beadsDir)
	if fallback.Source.Path != issues.Source.Path && issues.Source.Path != "" {
		fallback.Report.Stale = true
		fallback.Report.AuthorityWarnings = append(fallback.Report.AuthorityWarnings, fmt.Sprintf("source selection failed before fallback: %v", smartErr))
	}
	return fallback, err
}

// loadBDWorkspace loads issues from a bd (Dolt-backed) workspace by resolving
// the compatibility JSONL through loader.PrepareBeadsDirForRead (#141): it
// refreshes .beads/issues.jsonl via `bd export` when the bd binary is
// available, falls back to an existing export with a warning when the refresh
// fails, and returns a hard error when no compatibility JSONL can be produced.
// This guarantees bd workspaces either load real data or fail loudly — never
// a silently-empty result from a stray non-issue JSONL (#189).
func loadBDWorkspace(beadsDir string) (LoadResult, error) {
	var authorityWarnings []string
	warn := func(msg string) {
		authorityWarnings = append(authorityWarnings, msg)
		if !env.Robot.Bool() {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}
	jsonlPath, err := loader.PrepareBeadsDirForRead(beadsDir, true, warn)
	if err != nil {
		return LoadResult{Report: LoadReport{Path: beadsDir, AuthorityWarnings: authorityWarnings}}, fmt.Errorf("bd/Dolt workspace detected at %s: %w", beadsDir, err)
	}
	src := DataSource{
		Type:     SourceTypeJSONLLocal,
		Path:     jsonlPath,
		Priority: PriorityJSONLLocal,
	}
	result, err := loadAndValidateJSONL(src)
	result.Report.AuthorityWarnings = authorityWarnings
	result.Report.Stale = len(authorityWarnings) > 0
	return result, err
}

// ExplicitBeadsDBSource returns the direct source named by BEADS_DB when it
// points at a concrete source file. Directory values return ok=false so callers
// can use normal source discovery within that directory.
func ExplicitBeadsDBSource() (DataSource, bool, error) {
	return SourceFromFile(env.BeadsDB.Get())
}

// SourceFromFile returns a DataSource for a concrete source file path. Directory
// paths and empty values return ok=false so callers can fall back to discovery.
func SourceFromFile(dbPath string) (DataSource, bool, error) {
	if strings.TrimSpace(dbPath) == "" {
		return DataSource{}, false, nil
	}

	info, err := os.Stat(dbPath)
	if err == nil && info.IsDir() {
		return DataSource{}, false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return DataSource{}, true, fmt.Errorf("stat %s: %w", dbPath, err)
	}

	sourceType, priority, ok := explicitBeadsDBFileType(dbPath)
	if !ok {
		if err == nil {
			return DataSource{}, true, fmt.Errorf("unsupported BEADS_DB file type: %s", dbPath)
		}
		return DataSource{}, false, nil
	}

	source := DataSource{
		Type:     sourceType,
		Path:     dbPath,
		Priority: priority,
	}
	if err == nil {
		source.ModTime = info.ModTime()
		source.Size = info.Size()
	}
	return source, true, nil
}

func explicitBeadsDBFileType(dbPath string) (SourceType, int, bool) {
	switch strings.ToLower(filepath.Ext(dbPath)) {
	case ".db", ".sqlite", ".sqlite3":
		return SourceTypeSQLite, PrioritySQLite, true
	case ".jsonl":
		return SourceTypeJSONLLocal, PriorityJSONLLocal, true
	default:
		return "", 0, false
	}
}

// loadSmart discovers sources, selects the best, and loads from it in a single
// fused pass.
//
// Historically discovery ran a full content-scan validation (validateJSONL) on
// every source to set Valid/IssueCount and apply the malformed-error-rate gate,
// and the selected source was then read AGAIN by the loader — so the 1.9MB
// issues.jsonl was parsed twice per robot invocation. Here we skip the standalone
// validation pass and let the loader's own tolerant parse (it already strips BOM,
// caps long lines, and skips/counts malformed lines) serve as the validation
// pass: the same 10% malformed-error-rate gate is applied to the loader's parse
// stats post-load. A genuinely-corrupt JSONL is still rejected (and we fall
// through to the next candidate), but the happy path reads the file exactly once.
func loadSmart(beadsDir, repoPath string) (LoadResult, error) {
	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		RepoPath:               repoPath,
		ValidateAfterDiscovery: false,
	})
	if err != nil {
		return LoadResult{}, err
	}
	if len(sources) == 0 {
		return LoadResult{}, fmt.Errorf("no valid sources discovered")
	}

	// Order candidates exactly as SelectBestSource would (freshest, then
	// priority) so we try the authoritative source first and fall back through
	// the rest only if it fails to validate-and-load.
	ordered := make([]DataSource, len(sources))
	copy(ordered, sources)
	sortByFreshnessThenPriority(ordered)

	var lastErr error
	var lastResult LoadResult
	var rejected []string
	for i := range ordered {
		result, err := loadAndValidate(ordered[i])
		if err != nil {
			lastErr = err
			lastResult = result
			if len(rejected) < maxLoadReportWarnings {
				rejected = append(rejected, fmt.Sprintf("rejected source %s: %v", ordered[i].Path, err))
			}
			continue
		}
		result.Report.AuthorityWarnings = rejected
		result.Report.Stale = len(rejected) > 0
		return result, nil
	}

	if lastErr != nil {
		lastResult.Report.AuthorityWarnings = rejected
		return lastResult, fmt.Errorf("no valid sources discovered: %w", lastErr)
	}
	return LoadResult{}, fmt.Errorf("no valid sources discovered")
}

// loadAndValidate loads a single source while applying the validation gate in the
// same pass. For SQLite the validation (integrity + schema check) is cheap and
// independent of the row read, so it runs first. For JSONL the loader's tolerant
// parse IS the validation pass: a single read materializes issues and yields the
// parse stats used to apply the malformed-error-rate gate.
func loadAndValidate(source DataSource) (LoadResult, error) {
	var (
		result LoadResult
		err    error
	)
	switch source.Type {
	case SourceTypeSQLite:
		if err := ValidateSource(&source); err != nil {
			return LoadResult{Source: source, Report: LoadReport{Path: source.Path}}, err
		}
		result, err = LoadFromSource(source)
	case SourceTypeJSONLLocal, SourceTypeJSONLWorktree:
		result, err = loadAndValidateJSONL(source)
	default:
		return LoadResult{Source: source}, fmt.Errorf("unsupported source type: %s", source.Type)
	}
	return result, err
}

// loadAndValidateJSONL performs the fused validate-and-materialize pass for a
// JSONL source: it parses the file once, applies the same default 10%
// malformed-error-rate gate that validateJSONL uses, and filters tombstones to
// honor the IssueReader contract. Reading the file a single time replaces the
// previous validate-then-load double parse.
func loadAndValidateJSONL(source DataSource) (LoadResult, error) {
	rec := newLoadRecorder(source.Path)
	all, err := loader.LoadIssuesFromFileWithOptions(source.Path, rec.options())
	if err != nil {
		return LoadResult{Source: source, Report: rec.finish(all, false)}, err
	}
	stats := rec.stats

	// Apply the error-rate gate against the same default threshold
	// validateJSONL enforces. Malformed JSON or failed model validation count as
	// Errors, so a genuinely-corrupt file is rejected here and we fall through to
	// the next candidate.
	maxRate := DefaultValidationOptions().MaxJSONLErrorRate
	if rate := stats.ErrorRate(); rate > maxRate {
		return LoadResult{Source: source, Report: rec.finish(all, false)}, fmt.Errorf("%s: too many errors: %.1f%% (max %.1f%%)", source.Path, rate*100, maxRate*100)
	}

	// Reject a source that contained records but yielded ZERO valid issues — e.g.
	// a fresher stray sprints.jsonl/memories.jsonl, or a file made entirely of
	// non-issue `_type` records. The error-rate gate above misses these because
	// non-issue records are Skipped (not Errors), so the rate is 0. The old
	// validateJSONL counted every such non-issue line as an error and rejected the
	// file; this reproduces that so loadSmart falls through to the next candidate
	// instead of returning an empty list as "success". An empty file (no records
	// at all: Valid==Errors==Skipped==0) stays valid — a legitimately empty
	// project — matching validateJSONL's empty-file behavior. A file with Valid>0
	// whose issues are all tombstones is a real issues file and is accepted.
	if stats.Valid == 0 && stats.Errors+stats.Skipped > 0 {
		return LoadResult{Source: source, Report: rec.finish(all, false)}, fmt.Errorf("%s: no issue records (%d non-issue/error lines, 0 valid issues)", source.Path, stats.Errors+stats.Skipped)
	}

	// This source is the one actually being used: publish its parse accounting
	// so robot payloads can surface any dropped records (#190).
	report := rec.finish(all, true)

	// Filter out tombstone issues to match the IssueReader contract (the same
	// filtering JSONLReader.LoadIssues applies).
	out := make([]model.Issue, 0, len(all))
	for i := range all {
		if !all[i].Status.IsTombstone() {
			out = append(out, all[i])
		}
	}
	return LoadResult{Issues: out, Source: source, Report: report}, nil
}

// LoadFromSource loads issues from a specific DataSource via the IssueReader
// interface, dispatching to the appropriate backend based on source type.
func LoadFromSource(source DataSource) (result LoadResult, err error) {
	defer func() { result.bindOrigins(err) }()
	reader, err := NewReader(source)
	if err != nil {
		return LoadResult{Source: source, Report: LoadReport{Path: source.Path}}, fmt.Errorf("failed to open source %s: %w", source.Path, err)
	}
	defer reader.Close()
	issues, err := reader.LoadIssues()
	return LoadResult{Issues: issues, Source: source, Report: reader.LoadReport()}, err
}

func (r *LoadResult) bindOrigins(err error) {
	if err == nil {
		complete := r.Report.Errors == 0 && r.Report.ReadErrors == 0 && !r.Report.Stale && len(r.Report.AuthorityWarnings) == 0
		loader.AttachIssueOrigins(r.Issues, r.Source.Path, complete)
	}
}
