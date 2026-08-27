package datasource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Dicklesworthstone/beads_viewer/pkg/loader"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// maxLoadReportWarnings caps how many per-line warning messages a LoadReport
// retains, so a pathologically corrupt file cannot bloat robot payloads.
const maxLoadReportWarnings = 10

// LoadReport captures per-line parse accounting for the most recent successful
// JSONL load, so callers (robot payload emitters in particular) can surface
// records that were dropped during load instead of silently reporting them as
// nonexistent (#190). Errors counts issue-shaped lines that were malformed JSON
// or failed model validation (e.g. updated_at < created_at), later records that
// repeat an earlier issue ID, and over-limit lines that could not be classified;
// each such skip also contributes a human-readable reason to Warnings (capped).
type LoadReport struct {
	// Path is the JSONL file the report describes.
	Path string
	// Valid is the number of unique issue lines that parsed and validated.
	Valid int
	// Errors is the number of issue-shaped lines dropped as malformed JSON or
	// failed model validation, records rejected because the issue ID was
	// duplicated, plus over-limit lines that could not be classified.
	Errors int
	// Skipped is the number of recognized non-issue `_type` records.
	Skipped int
	// Warnings holds up to maxLoadReportWarnings skip reasons, in file order.
	Warnings []string
	// AuthorityWarnings records source-selection fallbacks that left the load
	// usable but potentially stale, such as a failed bd export followed by use of
	// an existing compatibility JSONL. These are not parse errors, but robot
	// callers must still surface them as authority gaps.
	AuthorityWarnings []string
}

var (
	lastLoadReportMu      sync.Mutex
	lastLoadReport        *LoadReport
	lastAuthorityWarnings []string
	lastSource            DataSource
	hasLastSource         bool
)

// LastLoadReport returns a copy of the parse accounting recorded by the most
// recent successful JSONL load in this process, or nil when no JSONL load has
// completed (e.g. the issues came from SQLite). Robot commands consult this to
// emit a `load_stats` block whenever records were dropped during load (#190).
func LastLoadReport() *LoadReport {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	if lastLoadReport == nil {
		return nil
	}
	cp := *lastLoadReport
	cp.Warnings = append([]string(nil), lastLoadReport.Warnings...)
	cp.AuthorityWarnings = append([]string(nil), lastLoadReport.AuthorityWarnings...)
	return &cp
}

func recordLoadReport(rep LoadReport) {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	for _, warning := range lastAuthorityWarnings {
		if len(rep.AuthorityWarnings) >= maxLoadReportWarnings {
			break
		}
		if !slices.Contains(rep.AuthorityWarnings, warning) {
			rep.AuthorityWarnings = append(rep.AuthorityWarnings, warning)
		}
	}
	lastLoadReport = &rep
}

func clearLoadReport() {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	lastLoadReport = nil
}

// LastSelectedSource returns the exact source that most recently completed a
// successful load. This is distinct from LastLoadReport: clean SQLite loads do
// not have JSONL parse accounting, but callers such as the file watcher still
// need to observe the source that was actually read rather than rediscovering
// and guessing from an unvalidated candidate list.
func LastSelectedSource() (DataSource, bool) {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	return lastSource, hasLastSource
}

func recordSelectedSource(source DataSource) {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	lastSource = source
	hasLastSource = true
}

func clearSelectedSource() {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	lastSource = DataSource{}
	hasLastSource = false
	lastAuthorityWarnings = nil
}

// LastAuthorityWarnings returns source-selection warnings for the most recent
// load, including successful SQLite loads which intentionally have no JSONL
// LoadReport. Callers use this separate channel to avoid losing fallback
// evidence when a rejected higher-ranked source was followed by SQLite.
func LastAuthorityWarnings() []string {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	return append([]string(nil), lastAuthorityWarnings...)
}

func appendLastLoadAuthorityWarnings(warnings []string) {
	lastLoadReportMu.Lock()
	defer lastLoadReportMu.Unlock()
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" || slices.Contains(lastAuthorityWarnings, warning) {
			continue
		}
		if len(lastAuthorityWarnings) >= maxLoadReportWarnings {
			return
		}
		lastAuthorityWarnings = append(lastAuthorityWarnings, warning)
		if lastLoadReport != nil && !slices.Contains(lastLoadReport.AuthorityWarnings, warning) {
			lastLoadReport.AuthorityWarnings = append(lastLoadReport.AuthorityWarnings, warning)
		}
	}
}

// loadRecorder wires a single JSONL parse to a LoadReport: it collects
// ParseStats plus the loader's per-line skip warnings, mirroring the default
// warning behavior (stderr in interactive mode, quiet under BV_ROBOT=1 so
// robot stdout/stderr stay clean — the accounting surfaces in the JSON
// payload instead).
type loadRecorder struct {
	path            string
	stats           loader.ParseStats
	warnings        []string
	warningCount    int
	parseOptions    loader.ParseOptions
	robot           bool
	warningsEmitted bool
}

func newLoadRecorder(path string) *loadRecorder {
	return newLoadRecorderWithOptions(path, loader.ParseOptions{})
}

func newLoadRecorderWithOptions(path string, opts loader.ParseOptions) *loadRecorder {
	return &loadRecorder{
		path:         path,
		parseOptions: opts,
		robot:        os.Getenv("BV_ROBOT") == "1",
	}
}

func (r *loadRecorder) options() loader.ParseOptions {
	return loader.ParseOptions{
		Stats:        &r.stats,
		WarningCount: &r.warningCount,
		BufferSize:   r.parseOptions.BufferSize,
		IssueFilter:  r.parseOptions.IssueFilter,
		WarningHandler: func(msg string) {
			if len(r.warnings) < maxLoadReportWarnings {
				r.warnings = append(r.warnings, msg)
			}
		},
	}
}

// commit records the parse accounting as the process-wide last load report.
// Call only after the load succeeded — failed candidates in the smart-load
// fallthrough must not pollute the report for the source actually used.
func (r *loadRecorder) commit() {
	if r.parseOptions.Stats != nil {
		*r.parseOptions.Stats = r.stats
	}
	if r.parseOptions.WarningCount != nil {
		*r.parseOptions.WarningCount += r.warningCount
	}
	recordLoadReport(LoadReport{
		Path:     r.path,
		Valid:    r.stats.Valid,
		Errors:   r.stats.Errors,
		Skipped:  r.stats.Skipped,
		Warnings: append([]string(nil), r.warnings...),
	})
	r.emitWarnings()
}

func (r *loadRecorder) emitWarnings() {
	if r.warningsEmitted {
		return
	}
	r.warningsEmitted = true
	for _, warning := range r.warnings {
		if r.parseOptions.WarningHandler != nil {
			r.parseOptions.WarningHandler(warning)
		} else if !r.robot {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
		}
	}
	if omitted := r.warningCount - len(r.warnings); omitted > 0 {
		summary := fmt.Sprintf("%d additional parse warnings omitted", omitted)
		if r.parseOptions.WarningHandler != nil {
			r.parseOptions.WarningHandler(summary)
		} else if !r.robot {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", summary)
		}
	}
}

// LoadIssues performs smart multi-source detection and loading.
// It discovers all available sources (SQLite, JSONL), validates them, selects
// the freshest valid source, and loads issues from it. SQLite is preferred over
// JSONL when both exist at comparable freshness, since SQLite reflects the most
// recent state (including status changes from br operations).
//
// Falls back to legacy JSONL-only loading via loader.LoadIssues if smart
// detection finds no valid sources.
func LoadIssues(repoPath string) ([]model.Issue, error) {
	return LoadIssuesWithOptions(repoPath, loader.ParseOptions{})
}

// LoadIssuesWithOptions performs repository-aware smart source selection while
// honoring the caller's JSONL parse controls. Repository identity is kept
// separate from the routed tracker directory so redirected/shared .beads
// stores can still discover sources belonging to the invoking Git worktree.
func LoadIssuesWithOptions(repoPath string, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	clearSelectedSource()
	if parseOptions.Stats != nil {
		*parseOptions.Stats = loader.ParseStats{}
	}
	if source, ok, err := ExplicitBeadsDBSource(); err != nil {
		return nil, err
	} else if ok {
		return loadAndValidateWithOptions(source, parseOptions)
	}

	beadsDir, err := loader.GetBeadsDir(repoPath)
	if err != nil {
		return nil, err
	}

	// bd/Dolt workspaces (#189): the issue data lives in a Dolt database that
	// bv cannot read directly, so route through the bd bridge added for #141.
	// It refreshes the compatibility export at .beads/issues.jsonl via
	// `bd export` when possible and errors loudly when no export exists —
	// instead of silently reporting an empty project.
	if loader.IsBDWorkspace(beadsDir) {
		return loadBDWorkspaceWithOptions(beadsDir, parseOptions)
	}

	issues, rejectedAuthorities, smartErr := loadSmart(
		beadsDir,
		repoPath,
		parseOptions,
		explicitBeadsDirectorySelectorActive(),
	)
	if smartErr == nil {
		return issues, nil
	}
	if len(rejectedAuthorities) == 0 {
		return nil, smartErr
	}

	// Fall back to the legacy tolerant JSONL parser only when smart discovery
	// found canonical tracker sources but rejected them during validation. A
	// discovery/stat failure is not equivalent: bypassing it here could conceal
	// an authority we were unable to inspect.
	issues, legacyErr := loadLegacyJSONLWithOptions(beadsDir, parseOptions)
	if legacyErr != nil {
		return nil, fmt.Errorf("smart source loading failed (%v); legacy JSONL fallback failed: %w", smartErr, legacyErr)
	}
	selected, _ := LastSelectedSource()
	warnings := authorityFallbackWarnings(rejectedAuthorities, selected.Path)
	appendLastLoadAuthorityWarnings(warnings)
	emitAuthorityWarnings(parseOptions, warnings)
	return issues, nil
}

// loadLegacyJSONL preserves the legacy parser's tolerance while publishing parse
// accounting via LastLoadReport and retaining the IssueReader contract: deleted
// tombstones are excluded, and a file containing only recognized non-issue
// records is rejected as the wrong source. Malformed issue-shaped records remain
// tolerated here so their dropped-record evidence can reach robot output.
// This path is reached when the smart loader rejected every candidate — e.g. a
// small JSONL whose only records fail validation trips the error-rate gate —
// which is exactly when dropped records MUST stay visible instead of the load
// silently yielding fewer (or zero) issues (#190).
func loadLegacyJSONL(beadsDir string) ([]model.Issue, error) {
	return loadLegacyJSONLWithOptions(beadsDir, loader.ParseOptions{})
}

func loadLegacyJSONLWithOptions(beadsDir string, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	jsonlPath, err := loader.FindJSONLPath(beadsDir)
	if err != nil {
		return nil, err
	}
	rec := newLoadRecorderWithOptions(jsonlPath, parseOptions)
	issues, err := loader.LoadIssuesFromFileWithOptions(jsonlPath, rec.options())
	if err != nil {
		return nil, err
	}
	if rec.stats.Valid == 0 && rec.stats.Errors == 0 && rec.stats.Skipped > 0 {
		return nil, fmt.Errorf("%s: no issue records (%d non-issue lines, 0 valid issues)", jsonlPath, rec.stats.Skipped)
	}
	rec.commit()
	recordSelectedSource(DataSource{
		Type: SourceTypeJSONLLocal,
		Path: jsonlPath,
	})
	out := make([]model.Issue, 0, len(issues))
	for i := range issues {
		if !issues[i].Status.IsTombstone() {
			out = append(out, issues[i])
		}
	}
	return out, nil
}

// LoadIssuesFromDir performs smart source detection within a known beads directory.
// This is useful when the caller already knows the .beads path.
func LoadIssuesFromDir(beadsDir string) ([]model.Issue, error) {
	return LoadIssuesFromDirWithOptions(beadsDir, loader.ParseOptions{})
}

// LoadIssuesFromDirWithOptions performs smart source selection while honoring
// the caller's JSONL parse controls. This lets long-lived TUI reloads route
// warnings into UI state, apply bounded line sizes, and pre-filter records
// without leaking parser diagnostics onto the terminal renderer.
func LoadIssuesFromDirWithOptions(beadsDir string, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	clearSelectedSource()
	if parseOptions.Stats != nil {
		*parseOptions.Stats = loader.ParseStats{}
	}
	// bd/Dolt workspaces (#189): see LoadIssues.
	if loader.IsBDWorkspace(beadsDir) {
		return loadBDWorkspaceWithOptions(beadsDir, parseOptions)
	}

	issues, rejectedAuthorities, smartErr := loadSmart(
		beadsDir,
		inferredRepoPathForBeadsDir(beadsDir),
		parseOptions,
		false,
	)
	if smartErr == nil {
		return issues, nil
	}
	if len(rejectedAuthorities) == 0 {
		return nil, smartErr
	}

	// Fall back to JSONL (legacy tolerant parse, with load accounting; #190).
	issues, legacyErr := loadLegacyJSONLWithOptions(beadsDir, parseOptions)
	if legacyErr != nil {
		return nil, fmt.Errorf("smart source loading failed (%v); legacy JSONL fallback failed: %w", smartErr, legacyErr)
	}
	selected, _ := LastSelectedSource()
	warnings := authorityFallbackWarnings(rejectedAuthorities, selected.Path)
	appendLastLoadAuthorityWarnings(warnings)
	emitAuthorityWarnings(parseOptions, warnings)
	return issues, nil
}

func inferredRepoPathForBeadsDir(beadsDir string) string {
	cleaned := filepath.Clean(beadsDir)
	switch filepath.Base(cleaned) {
	case ".beads", "_beads":
		return filepath.Dir(cleaned)
	default:
		return ""
	}
}

// loadBDWorkspace loads issues from a bd (Dolt-backed) workspace by resolving
// the compatibility JSONL through loader.PrepareBeadsDirForRead (#141): it
// refreshes .beads/issues.jsonl via `bd export` when the bd binary is
// available, falls back to an existing export with a warning when the refresh
// fails, and returns a hard error when no compatibility JSONL can be produced.
// This guarantees bd workspaces either load real data or fail loudly — never
// a silently-empty result from a stray non-issue JSONL (#189).
func loadBDWorkspace(beadsDir string) ([]model.Issue, error) {
	return loadBDWorkspaceWithOptions(beadsDir, loader.ParseOptions{})
}

func loadBDWorkspaceWithOptions(beadsDir string, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	var authorityWarnings []string
	warn := func(msg string) {
		if warning := strings.TrimSpace(msg); warning != "" {
			authorityWarnings = append(authorityWarnings, warning)
		}
		if parseOptions.WarningCount != nil {
			*parseOptions.WarningCount = *parseOptions.WarningCount + 1
		}
		if parseOptions.WarningHandler != nil {
			parseOptions.WarningHandler(msg)
		} else if os.Getenv("BV_ROBOT") != "1" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}
	jsonlPath, err := loader.PrepareBeadsDirForRead(beadsDir, true, warn)
	if err != nil {
		return nil, fmt.Errorf("bd/Dolt workspace detected at %s: %w", beadsDir, err)
	}
	issues, err := loadAndValidateJSONLWithOptions(DataSource{
		Type:     SourceTypeJSONLLocal,
		Path:     jsonlPath,
		Priority: PriorityJSONLLocal,
	}, parseOptions)
	if err != nil {
		return nil, err
	}
	appendLastLoadAuthorityWarnings(authorityWarnings)
	return issues, nil
}

// ExplicitBeadsDBSource returns the direct source named by BEADS_DB when it
// points at a concrete source file. Directory values return ok=false so callers
// can use normal source discovery within that directory.
func ExplicitBeadsDBSource() (DataSource, bool, error) {
	return SourceFromFile(os.Getenv(loader.BeadsDBEnvVar))
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
func loadSmart(
	beadsDir, repoPath string,
	parseOptions loader.ParseOptions,
	skipWorktreeSources bool,
) ([]model.Issue, []string, error) {
	sources, err := DiscoverSources(DiscoveryOptions{
		BeadsDir:               beadsDir,
		RepoPath:               repoPath,
		ValidateAfterDiscovery: false,
		SkipWorktreeSources:    skipWorktreeSources,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(sources) == 0 {
		return nil, nil, fmt.Errorf("no valid sources discovered")
	}

	// Order candidates exactly as SelectBestSource would (freshness, source
	// priority, then canonical JSONL filename) so we try the authoritative
	// source first and fall back through the rest only if it fails to
	// validate-and-load.
	ordered := make([]DataSource, len(sources))
	copy(ordered, sources)
	sortByFreshnessThenPriority(ordered)

	var lastErr error
	var rejectedAuthorities []string
	for i := range ordered {
		issues, err := loadAndValidateWithOptions(ordered[i], parseOptions)
		if err != nil {
			lastErr = err
			if isTrackerAuthorityCandidate(ordered[i]) && len(rejectedAuthorities) < maxLoadReportWarnings {
				rejectedAuthorities = append(rejectedAuthorities,
					fmt.Sprintf("higher-ranked tracker source %q was rejected: %v", ordered[i].Path, err))
			}
			continue
		}
		if len(rejectedAuthorities) > 0 {
			warnings := authorityFallbackWarnings(rejectedAuthorities, ordered[i].Path)
			appendLastLoadAuthorityWarnings(warnings)
			emitAuthorityWarnings(parseOptions, warnings)
		}
		return issues, nil, nil
	}

	if lastErr != nil {
		return nil, rejectedAuthorities, fmt.Errorf("no valid sources discovered: %w", lastErr)
	}
	return nil, rejectedAuthorities, fmt.Errorf("no valid sources discovered")
}

func explicitBeadsDirectorySelectorActive() bool {
	return strings.TrimSpace(os.Getenv(loader.BeadsDBEnvVar)) != "" ||
		strings.TrimSpace(os.Getenv(loader.BeadsDirEnvVar)) != ""
}

func authorityFallbackWarnings(rejectedAuthorities []string, fallbackPath string) []string {
	warnings := make([]string, 0, len(rejectedAuthorities))
	for _, warning := range rejectedAuthorities {
		warnings = append(warnings, fmt.Sprintf("%s; using fallback %q", warning, fallbackPath))
	}
	return warnings
}

func emitAuthorityWarnings(options loader.ParseOptions, warnings []string) {
	for _, warning := range warnings {
		if options.WarningCount != nil {
			*options.WarningCount = *options.WarningCount + 1
		}
		if options.WarningHandler != nil {
			options.WarningHandler(warning)
		} else if os.Getenv("BV_ROBOT") != "1" {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
		}
	}
}

func isTrackerAuthorityCandidate(source DataSource) bool {
	if source.Type == SourceTypeSQLite {
		return true
	}
	if source.Type == SourceTypeJSONLWorktree {
		return filepath.Base(source.Path) == "issues.jsonl"
	}
	if source.Type != SourceTypeJSONLLocal {
		return false
	}
	return slices.Contains(loader.PreferredJSONLNames, filepath.Base(source.Path))
}

// loadAndValidate loads a single source while applying the validation gate in the
// same pass. For SQLite the validation (integrity + schema check) is cheap and
// independent of the row read, so it runs first. For JSONL the loader's tolerant
// parse IS the validation pass: a single read materializes issues and yields the
// parse stats used to apply the malformed-error-rate gate.
func loadAndValidate(source DataSource) ([]model.Issue, error) {
	return loadAndValidateWithOptions(source, loader.ParseOptions{})
}

func loadAndValidateWithOptions(source DataSource, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	switch source.Type {
	case SourceTypeSQLite:
		if err := ValidateSource(&source); err != nil {
			return nil, err
		}
		issues, err := loadFromSourceWithReaderFactory(source, NewReader)
		if err != nil || parseOptions.IssueFilter == nil {
			return issues, err
		}
		filtered := issues[:0]
		for i := range issues {
			if parseOptions.IssueFilter(&issues[i]) {
				filtered = append(filtered, issues[i])
			}
		}
		clear(issues[len(filtered):])
		return filtered, nil
	case SourceTypeJSONLLocal, SourceTypeJSONLWorktree:
		return loadAndValidateJSONLWithOptions(source, parseOptions)
	default:
		return nil, fmt.Errorf("unsupported source type: %s", source.Type)
	}
}

// loadAndValidateJSONL performs the fused validate-and-materialize pass for a
// JSONL source: it parses the file once, applies the same default 10%
// malformed-error-rate gate that validateJSONL uses, and filters tombstones to
// honor the IssueReader contract. Reading the file a single time replaces the
// previous validate-then-load double parse.
func loadAndValidateJSONL(source DataSource) ([]model.Issue, error) {
	return loadAndValidateJSONLWithOptions(source, loader.ParseOptions{})
}

func loadAndValidateJSONLWithOptions(source DataSource, parseOptions loader.ParseOptions) ([]model.Issue, error) {
	rec := newLoadRecorderWithOptions(source.Path, parseOptions)
	all, err := loader.LoadIssuesFromFileWithOptions(source.Path, rec.options())
	if err != nil {
		return nil, err
	}
	stats := rec.stats

	// Apply the error-rate gate against the same default threshold
	// validateJSONL enforces. Malformed JSON or failed model validation count as
	// Errors, so a genuinely-corrupt file is rejected here and we fall through to
	// the next candidate.
	maxRate := DefaultValidationOptions().MaxJSONLErrorRate
	if rate := stats.ErrorRate(); rate > maxRate {
		return nil, fmt.Errorf("%s: too many errors: %.1f%% (max %.1f%%)", source.Path, rate*100, maxRate*100)
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
		return nil, fmt.Errorf("%s: no issue records (%d non-issue/error lines, 0 valid issues)", source.Path, stats.Errors+stats.Skipped)
	}

	// This source is the one actually being used: publish its parse accounting
	// so robot payloads can surface any dropped records (#190).
	rec.commit()
	recordSelectedSource(source)

	// Filter out tombstone issues to match the IssueReader contract (the same
	// filtering JSONLReader.LoadIssues applies).
	out := make([]model.Issue, 0, len(all))
	for i := range all {
		if !all[i].Status.IsTombstone() {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// LoadFromSource loads issues from a specific DataSource via the IssueReader
// interface, dispatching to the appropriate backend based on source type.
func LoadFromSource(source DataSource) ([]model.Issue, error) {
	clearSelectedSource()
	if source.Type == SourceTypeJSONLLocal || source.Type == SourceTypeJSONLWorktree {
		return loadAndValidateJSONLWithOptions(source, loader.ParseOptions{})
	}
	return loadFromSourceWithReaderFactory(source, NewReader)
}

// loadFromSourceWithReaderFactory keeps resource-finalization behavior directly
// testable without mutating a process-wide reader factory. Production callers
// always pass NewReader via LoadFromSource.
func loadFromSourceWithReaderFactory(
	source DataSource,
	newReader func(DataSource) (IssueReader, error),
) ([]model.Issue, error) {
	reader, err := newReader(source)
	if err != nil {
		return nil, fmt.Errorf("failed to open source %s: %w", source.Path, err)
	}
	issues, loadErr := reader.LoadIssues()
	closeErr := reader.Close()
	if err := errors.Join(loadErr, closeErr); err != nil {
		return nil, fmt.Errorf("loading source %s: %w", source.Path, err)
	}
	if source.Type == SourceTypeSQLite {
		// LastLoadReport describes JSONL parser omissions only. A successful
		// SQLite authority must clear an older JSONL report so a later robot
		// envelope cannot inherit stale dropped-record evidence.
		clearLoadReport()
	}
	recordSelectedSource(source)
	return issues, nil
}
