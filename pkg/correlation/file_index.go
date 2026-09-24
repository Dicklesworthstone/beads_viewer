// Package correlation provides file-to-bead reverse index functionality.
package correlation

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BeadReference links a bead to a file via commits.
type BeadReference struct {
	BeadID       string    `json:"bead_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`        // open/in_progress/closed
	CommitSHAs   []string  `json:"commit_shas"`   // full SHAs linking this bead to this file
	LastTouch    time.Time `json:"last_touch"`    // most recent commit timestamp
	TotalChanges int       `json:"total_changes"` // sum of insertions + deletions across commits
}

// FileBeadIndex provides O(1) lookup from file path to beads that touched it.
type FileBeadIndex struct {
	// FileToBeads maps normalized file paths to beads that modified them
	FileToBeads map[string][]BeadReference `json:"file_to_beads"`

	// Stats provides aggregate information about the index
	Stats FileIndexStats `json:"stats"`
}

// FileIndexStats contains aggregate statistics about the file index.
type FileIndexStats struct {
	TotalFiles             int `json:"total_files"`               // number of unique files
	TotalBeadLinks         int `json:"total_bead_links"`          // sum of all bead references
	FilesWithMultipleBeads int `json:"files_with_multiple_beads"` // files touched by >1 bead
}

// FileBeadLookupResult is the result of looking up beads for a file.
type FileBeadLookupResult struct {
	FilePath    string          `json:"file_path"`
	OpenBeads   []BeadReference `json:"open_beads"`   // currently open beads
	ClosedBeads []BeadReference `json:"closed_beads"` // recently closed beads
	TotalBeads  int             `json:"total_beads"`
}

// FileLookup provides file-to-bead lookup functionality.
type FileLookup struct {
	index    *FileBeadIndex
	beads    map[string]BeadHistory // BeadID -> history for status lookups
	coChange *CoChangeMatrix        // Co-change matrix for related files
}

// BuildFileIndex creates a file index from a history report.
// It extracts all file paths from correlated commits and maps them to beads.
func BuildFileIndex(report *HistoryReport) *FileBeadIndex {
	if report == nil {
		return &FileBeadIndex{
			FileToBeads: make(map[string][]BeadReference),
		}
	}

	// fileBeadMap: file -> beadID -> reference (for deduplication)
	fileBeadMap := make(map[string]map[string]*BeadReference)

	for beadID, history := range report.Histories {
		// Keep the index consistent with every lookup surface: beads whose
		// status classifies as skip (tombstone / soft-deleted) are excluded
		// at build time, so hotspots, per-file lookups, impact analysis, and
		// the aggregate stats all agree on the same set of bead links (#184).
		if _, skip := classifyBeadStatus(history.Status); skip {
			continue
		}
		for _, commit := range history.Commits {
			for _, file := range commit.Files {
				// Normalize path (remove leading ./ and normalize separators)
				normalizedPath := normalizePath(file.Path)
				if normalizedPath == "" {
					continue
				}

				if fileBeadMap[normalizedPath] == nil {
					fileBeadMap[normalizedPath] = make(map[string]*BeadReference)
				}

				ref := fileBeadMap[normalizedPath][beadID]
				if ref == nil {
					ref = &BeadReference{
						BeadID:     beadID,
						Title:      history.Title,
						Status:     history.Status,
						CommitSHAs: []string{},
						LastTouch:  commit.Timestamp,
					}
					fileBeadMap[normalizedPath][beadID] = ref
				}

				// Machine-readable identities must remain lossless. Short prefixes
				// can collide in sufficiently large repositories, so retain only the
				// full SHA carried by the correlated commit.
				if commit.SHA != "" {
					ref.CommitSHAs = appendUnique(ref.CommitSHAs, commit.SHA)
				}

				// Update last touch time if this commit is more recent
				if commit.Timestamp.After(ref.LastTouch) {
					ref.LastTouch = commit.Timestamp
				}

				// Accumulate changes
				ref.TotalChanges += file.Insertions + file.Deletions
			}
		}
	}

	// Convert to final structure
	result := &FileBeadIndex{
		FileToBeads: make(map[string][]BeadReference),
	}

	totalLinks := 0
	multipleBeadsCount := 0

	for filePath, beadMap := range fileBeadMap {
		refs := make([]BeadReference, 0, len(beadMap))
		for _, ref := range beadMap {
			sort.Strings(ref.CommitSHAs)
			refs = append(refs, *ref)
		}

		// Sort by last touch time (most recent first)
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].LastTouch.Equal(refs[j].LastTouch) {
				return refs[i].BeadID < refs[j].BeadID
			}
			return refs[i].LastTouch.After(refs[j].LastTouch)
		})

		result.FileToBeads[filePath] = refs
		totalLinks += len(refs)
		if len(refs) > 1 {
			multipleBeadsCount++
		}
	}

	result.Stats = FileIndexStats{
		TotalFiles:             len(result.FileToBeads),
		TotalBeadLinks:         totalLinks,
		FilesWithMultipleBeads: multipleBeadsCount,
	}

	return result
}

// NewFileLookup creates a file lookup from a history report.
func NewFileLookup(report *HistoryReport) *FileLookup {
	if report == nil {
		return &FileLookup{
			index:    BuildFileIndex(nil),
			beads:    make(map[string]BeadHistory),
			coChange: BuildCoChangeMatrix(nil),
		}
	}
	return &FileLookup{
		index:    BuildFileIndex(report),
		beads:    report.Histories,
		coChange: BuildCoChangeMatrix(report),
	}
}

// LookupByFile finds all beads that have touched a given file.
// The path can be exact or a prefix (for directory lookups).
func (fl *FileLookup) LookupByFile(path string) *FileBeadLookupResult {
	normalizedPath := normalizePath(path)

	result := &FileBeadLookupResult{
		FilePath:    path,
		OpenBeads:   []BeadReference{},
		ClosedBeads: []BeadReference{},
	}

	// Try exact match first
	if refs, ok := fl.index.FileToBeads[normalizedPath]; ok {
		for _, ref := range refs {
			// Lookup results are caller-owned snapshots. A struct copy still aliases
			// the index's CommitSHAs backing array, so clone it just as the
			// prefix/glob accumulation path does.
			ref.CommitSHAs = append([]string(nil), ref.CommitSHAs...)
			// Get current status from beads map (may have changed)
			status := ref.Status
			if history, ok := fl.beads[ref.BeadID]; ok {
				ref.Status = history.Status
				ref.Title = history.Title
				status = history.Status
			}

			bucket, skip := classifyBeadStatus(status)
			if skip {
				continue
			}
			if bucket == "closed" {
				result.ClosedBeads = append(result.ClosedBeads, ref)
			} else {
				result.OpenBeads = append(result.OpenBeads, ref)
			}
		}
		sortBeadRefs(result.OpenBeads)
		sortBeadRefs(result.ClosedBeads)
		result.TotalBeads = len(result.OpenBeads) + len(result.ClosedBeads)
		return result
	}

	// Try prefix match for directory lookups
	// Note: normalizePath converts all backslashes to forward slashes, so we only need to check "/"
	openRefs := make(map[string]BeadReference)
	closedRefs := make(map[string]BeadReference)
	for filePath, refs := range fl.index.FileToBeads {
		if strings.HasPrefix(filePath, normalizedPath+"/") {
			for _, ref := range refs {
				// Get current status
				status := ref.Status
				if history, ok := fl.beads[ref.BeadID]; ok {
					ref.Status = history.Status
					ref.Title = history.Title
					status = history.Status
				}

				bucket, skip := classifyBeadStatus(status)
				if skip {
					continue
				}

				if bucket == "closed" {
					accumulateBeadReference(closedRefs, ref)
				} else {
					accumulateBeadReference(openRefs, ref)
				}
			}
		}
	}

	result.OpenBeads = beadReferencesFromMap(openRefs)
	result.ClosedBeads = beadReferencesFromMap(closedRefs)
	sortBeadRefs(result.OpenBeads)
	sortBeadRefs(result.ClosedBeads)
	result.TotalBeads = len(result.OpenBeads) + len(result.ClosedBeads)
	return result
}

// LookupByFileGlob finds beads for files matching a glob pattern.
func (fl *FileLookup) LookupByFileGlob(pattern string) *FileBeadLookupResult {
	result := &FileBeadLookupResult{
		FilePath:    pattern,
		OpenBeads:   []BeadReference{},
		ClosedBeads: []BeadReference{},
	}

	openRefs := make(map[string]BeadReference)
	closedRefs := make(map[string]BeadReference)

	for filePath, refs := range fl.index.FileToBeads {
		matched, err := filepath.Match(pattern, filePath)
		if err != nil || !matched {
			continue
		}

		for _, ref := range refs {
			// Get current status
			status := ref.Status
			if history, ok := fl.beads[ref.BeadID]; ok {
				ref.Status = history.Status
				ref.Title = history.Title
				status = history.Status
			}

			bucket, skip := classifyBeadStatus(status)
			if skip {
				continue
			}
			if bucket == "closed" {
				accumulateBeadReference(closedRefs, ref)
			} else {
				accumulateBeadReference(openRefs, ref)
			}
		}
	}

	result.OpenBeads = beadReferencesFromMap(openRefs)
	result.ClosedBeads = beadReferencesFromMap(closedRefs)
	sortBeadRefs(result.OpenBeads)
	sortBeadRefs(result.ClosedBeads)
	result.TotalBeads = len(result.OpenBeads) + len(result.ClosedBeads)
	return result
}

// GetAllFiles returns all files in the index, sorted by path.
func (fl *FileLookup) GetAllFiles() []string {
	files := make([]string, 0, len(fl.index.FileToBeads))
	for path := range fl.index.FileToBeads {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

// GetStats returns statistics about the file index.
func (fl *FileLookup) GetStats() FileIndexStats {
	return fl.index.Stats
}

// GetRelatedFiles returns files that frequently co-change with the given file.
// threshold is the minimum correlation (0.0-1.0) to include (default 0.5 if <= 0).
// limit is the maximum number of related files to return (default 10 if <= 0).
func (fl *FileLookup) GetRelatedFiles(filePath string, threshold float64, limit int) *CoChangeResult {
	return fl.coChange.GetRelatedFiles(filePath, threshold, limit)
}

// GetCoChangeMatrix returns the underlying co-change matrix for advanced queries.
func (fl *FileLookup) GetCoChangeMatrix() *CoChangeMatrix {
	return fl.coChange
}

// GetHotspots returns files touched by the most beads (potential conflict zones).
//
// Bead counting uses the exact same status classification as LookupByFile and
// ImpactAnalysis (classifyBeadStatus on the current status from fl.beads), so
// the three robot surfaces built on this index can never disagree about how
// many beads touch a given file (#184). Beads that classify as skip
// (tombstone) are not counted, and files whose every bead is skipped are
// omitted entirely.
func (fl *FileLookup) GetHotspots(limit int) []FileHotspot {
	counts := []FileHotspot{}
	for path, refs := range fl.index.FileToBeads {
		openCount := 0
		closedCount := 0
		for _, ref := range refs {
			// Get current status from beads map (may have changed since index was built)
			status := ref.Status
			if history, ok := fl.beads[ref.BeadID]; ok {
				status = history.Status
			}
			bucket, skip := classifyBeadStatus(status)
			if skip {
				continue
			}
			if bucket == "closed" {
				closedCount++
			} else {
				openCount++
			}
		}
		total := openCount + closedCount
		if total == 0 {
			continue
		}
		counts = append(counts, FileHotspot{
			FilePath:    path,
			TotalBeads:  total,
			OpenBeads:   openCount,
			ClosedBeads: closedCount,
		})
	}

	// Sort by count descending; tie-break on path for deterministic output.
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].TotalBeads != counts[j].TotalBeads {
			return counts[i].TotalBeads > counts[j].TotalBeads
		}
		return counts[i].FilePath < counts[j].FilePath
	})

	// Take top N
	if limit <= 0 || limit > len(counts) {
		limit = len(counts)
	}

	return counts[:limit]
}

// FileHotspot represents a file that has been touched by many beads.
type FileHotspot struct {
	FilePath    string `json:"file_path"`
	TotalBeads  int    `json:"total_beads"`
	OpenBeads   int    `json:"open_beads"`
	ClosedBeads int    `json:"closed_beads"`
}

// CoChangeEntry represents a file that frequently co-changes with another file.
type CoChangeEntry struct {
	FilePath      string   `json:"file_path"`       // The related file
	CoChangeCount int      `json:"co_change_count"` // Number of commits where both files changed
	TotalCommits  int      `json:"total_commits"`   // Total commits touching the source file
	Correlation   float64  `json:"correlation"`     // co_change_count / total_commits (0.0 - 1.0)
	SampleCommits []string `json:"sample_commits"`  // Up to 3 sample full commit SHAs
}

// CoChangeResult is the result of looking up files that co-change with a given file.
type CoChangeResult struct {
	FilePath     string          `json:"file_path"`     // The queried file
	TotalCommits int             `json:"total_commits"` // Total commits touching this file
	RelatedFiles []CoChangeEntry `json:"related_files"` // Files that co-change, sorted by correlation
	Threshold    float64         `json:"threshold"`     // Minimum correlation threshold used
}

// CoChangeMatrix stores co-change relationships between files.
// Key is normalized file path, value is map of related file -> commit count.
type CoChangeMatrix struct {
	// Matrix maps file -> related file -> count of commits where both changed
	Matrix map[string]map[string]int `json:"matrix"`
	// FileCommitCounts maps file -> total commits touching that file
	FileCommitCounts map[string]int `json:"file_commit_counts"`
	// CommitFiles maps full commit SHA -> files changed in that commit (for sampling)
	CommitFiles map[string][]string `json:"-"` // Not serialized, internal use
}

// BuildCoChangeMatrix creates a co-change matrix from a history report.
// It analyzes which files frequently change together in the same commits.
func BuildCoChangeMatrix(report *HistoryReport) *CoChangeMatrix {
	matrix := &CoChangeMatrix{
		Matrix:           make(map[string]map[string]int),
		FileCommitCounts: make(map[string]int),
		CommitFiles:      make(map[string][]string),
	}

	if report == nil {
		return matrix
	}

	// A commit can appear in multiple bead histories. Assemble the union of its
	// file observations before counting it so complementary copies cannot make
	// co-change results depend on map iteration order.
	commitFileSets := make(map[string]map[string]struct{})
	for _, history := range report.Histories {
		for _, commit := range history.Commits {
			if commit.SHA == "" {
				continue
			}
			files := commitFileSets[commit.SHA]
			if files == nil {
				files = make(map[string]struct{}, len(commit.Files))
				commitFileSets[commit.SHA] = files
			}
			for _, fc := range commit.Files {
				normalized := normalizePath(fc.Path)
				if normalized != "" {
					files[normalized] = struct{}{}
				}
			}
		}
	}

	commitSHAs := make([]string, 0, len(commitFileSets))
	for sha := range commitFileSets {
		commitSHAs = append(commitSHAs, sha)
	}
	sort.Strings(commitSHAs)
	for _, sha := range commitSHAs {
		fileSet := commitFileSets[sha]
		files := make([]string, 0, len(fileSet))
		for file := range fileSet {
			files = append(files, file)
		}
		sort.Strings(files)

		// Store files by immutable full SHA. Seven-character prefixes are not
		// unique and can otherwise overwrite one another in long histories.
		matrix.CommitFiles[sha] = files

		// Update file commit counts
		for _, file := range files {
			matrix.FileCommitCounts[file]++
		}

		// Build co-change relationships (all ordered file pairs in this commit).
		for i := 0; i < len(files); i++ {
			for j := 0; j < len(files); j++ {
				if i == j {
					continue // Skip self-relationships
				}
				fileA, fileB := files[i], files[j]
				if matrix.Matrix[fileA] == nil {
					matrix.Matrix[fileA] = make(map[string]int)
				}
				matrix.Matrix[fileA][fileB]++
			}
		}
	}

	return matrix
}

// GetRelatedFiles returns files that frequently co-change with the given file.
// threshold is the minimum correlation (0.0-1.0) to include (default 0.5 if <= 0).
// limit is the maximum number of related files to return (default 10 if <= 0).
func (m *CoChangeMatrix) GetRelatedFiles(filePath string, threshold float64, limit int) *CoChangeResult {
	if threshold <= 0 {
		threshold = 0.5 // Default: 50% co-occurrence
	}
	if limit <= 0 {
		limit = 10
	}

	normalizedPath := normalizePath(filePath)
	result := &CoChangeResult{
		FilePath:     filePath,
		TotalCommits: m.FileCommitCounts[normalizedPath],
		RelatedFiles: []CoChangeEntry{},
		Threshold:    threshold,
	}

	if result.TotalCommits == 0 {
		return result // File not found in history
	}

	related := m.Matrix[normalizedPath]
	if related == nil {
		return result // No co-changes found
	}

	// Build list of related files with correlation
	var entries []CoChangeEntry
	for relatedFile, count := range related {
		correlation := float64(count) / float64(result.TotalCommits)
		if correlation >= threshold {
			entry := CoChangeEntry{
				FilePath:      relatedFile,
				CoChangeCount: count,
				TotalCommits:  result.TotalCommits,
				Correlation:   correlation,
				SampleCommits: []string{},
			}

			// Find sample commits where both files changed together. Collect then
			// sort: taking the first three directly from a map made robot output
			// vary from process to process.
			var sampleCommits []string
			for sha, files := range m.CommitFiles {
				hasSource, hasRelated := false, false
				for _, f := range files {
					if f == normalizedPath {
						hasSource = true
					}
					if f == relatedFile {
						hasRelated = true
					}
				}
				if hasSource && hasRelated {
					sampleCommits = append(sampleCommits, sha)
				}
			}
			sort.Strings(sampleCommits)
			if len(sampleCommits) > 3 {
				sampleCommits = sampleCommits[:3]
			}
			entry.SampleCommits = sampleCommits

			entries = append(entries, entry)
		}
	}

	// Sort by correlation descending
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Correlation != entries[j].Correlation {
			return entries[i].Correlation > entries[j].Correlation
		}
		return entries[i].FilePath < entries[j].FilePath
	})

	// Apply limit
	if len(entries) > limit {
		entries = entries[:limit]
	}

	result.RelatedFiles = entries
	return result
}

// ImpactResult is the result of analyzing what beads might be affected by file changes.
type ImpactResult struct {
	Files         []string       `json:"files"`
	AffectedBeads []AffectedBead `json:"affected_beads"`
	RiskLevel     string         `json:"risk_level"`
	RiskScore     float64        `json:"risk_score"`
	Warnings      []string       `json:"warnings"`
	Summary       string         `json:"summary"`
}

// AffectedBead represents a bead that touches one or more of the analyzed files.
type AffectedBead struct {
	BeadID       string    `json:"bead_id"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	OverlapFiles []string  `json:"overlap_files"`
	OverlapCount int       `json:"overlap_count"`
	LastActivity time.Time `json:"last_activity"`
	Relevance    float64   `json:"relevance"`
	TotalChanges int       `json:"total_changes"`
}

// ImpactAnalysis analyzes what beads might be affected if the given files are modified.
func (fl *FileLookup) ImpactAnalysis(files []string) *ImpactResult {
	return fl.ImpactAnalysisAt(files, time.Now())
}

// ImpactAnalysisAt is ImpactAnalysis evaluated at a caller-owned instant. It
// keeps recency inclusion and relevance deterministic for robot callers.
func (fl *FileLookup) ImpactAnalysisAt(files []string, now time.Time) *ImpactResult {
	result := &ImpactResult{
		Files:         []string{},
		AffectedBeads: []AffectedBead{},
		RiskLevel:     "low",
		RiskScore:     0.0,
		Warnings:      []string{},
	}

	if len(files) == 0 {
		result.Summary = "No files to analyze"
		return result
	}

	// Normalize, filter empty/whitespace strings, and deduplicate file paths
	seen := make(map[string]bool)
	normalizedFiles := make([]string, 0, len(files))
	for _, f := range files {
		norm := strings.TrimSpace(normalizePath(f))
		if norm == "" {
			continue // Skip empty or whitespace-only paths
		}
		if !seen[norm] {
			seen[norm] = true
			normalizedFiles = append(normalizedFiles, norm)
		}
	}

	if len(normalizedFiles) == 0 {
		result.Summary = "No valid files to analyze"
		return result
	}

	result.Files = normalizedFiles
	beadMap := make(map[string]*AffectedBead)
	now = now.UTC()

	for _, filePath := range normalizedFiles {
		lookup := fl.LookupByFile(filePath)

		for _, ref := range lookup.OpenBeads {
			ab := beadMap[ref.BeadID]
			if ab == nil {
				ab = &AffectedBead{
					BeadID:       ref.BeadID,
					Title:        ref.Title,
					Status:       ref.Status,
					OverlapFiles: []string{},
					LastActivity: ref.LastTouch,
				}
				beadMap[ref.BeadID] = ab
			}
			ab.OverlapFiles = append(ab.OverlapFiles, filePath)
			ab.OverlapCount = len(ab.OverlapFiles)
			ab.TotalChanges += ref.TotalChanges
			if ref.LastTouch.After(ab.LastActivity) {
				ab.LastActivity = ref.LastTouch
			}
		}

		for _, ref := range lookup.ClosedBeads {
			if now.Sub(ref.LastTouch) > 7*24*time.Hour {
				continue
			}
			ab := beadMap[ref.BeadID]
			if ab == nil {
				ab = &AffectedBead{
					BeadID:       ref.BeadID,
					Title:        ref.Title,
					Status:       ref.Status,
					OverlapFiles: []string{},
					LastActivity: ref.LastTouch,
				}
				beadMap[ref.BeadID] = ab
			}
			ab.OverlapFiles = append(ab.OverlapFiles, filePath)
			ab.OverlapCount = len(ab.OverlapFiles)
			ab.TotalChanges += ref.TotalChanges
			if ref.LastTouch.After(ab.LastActivity) {
				ab.LastActivity = ref.LastTouch
			}
		}
	}

	openCount := 0
	inProgressCount := 0
	recentClosedCount := 0

	for _, ab := range beadMap {
		daysSince := now.Sub(ab.LastActivity).Hours() / 24
		recencyScore := 1.0 - (daysSince / 7.0)
		if recencyScore < 0 {
			recencyScore = 0
		} else if recencyScore > 1 {
			recencyScore = 1
		}
		overlapScore := float64(ab.OverlapCount) / float64(len(normalizedFiles))
		statusMultiplier := 0.5
		switch normalizedBeadStatus(ab.Status) {
		case "in_progress":
			statusMultiplier = 1.0
			inProgressCount++
		case "closed":
			recentClosedCount++
		default:
			// Every non-closed status admitted by LookupByFile is live work
			// (for example open, blocked, or deferred) and must not be
			// misreported as a recently closed bead.
			statusMultiplier = 0.8
			openCount++
		}
		ab.Relevance = (recencyScore*0.4 + overlapScore*0.4 + statusMultiplier*0.2)
		result.AffectedBeads = append(result.AffectedBeads, *ab)
	}

	sort.Slice(result.AffectedBeads, func(i, j int) bool {
		pi, pj := affectedBeadStatusPriority(result.AffectedBeads[i].Status), affectedBeadStatusPriority(result.AffectedBeads[j].Status)
		if pi != pj {
			return pi < pj
		}
		if result.AffectedBeads[i].Relevance != result.AffectedBeads[j].Relevance {
			return result.AffectedBeads[i].Relevance > result.AffectedBeads[j].Relevance
		}
		return result.AffectedBeads[i].BeadID < result.AffectedBeads[j].BeadID
	})

	result.RiskScore = float64(inProgressCount)*0.4 + float64(openCount)*0.2 + float64(recentClosedCount)*0.05
	if len(normalizedFiles) > 3 {
		result.RiskScore += 0.1
	}
	if result.RiskScore > 1.0 {
		result.RiskScore = 1.0
	}

	switch {
	case result.RiskScore >= 0.7:
		result.RiskLevel = "critical"
	case result.RiskScore >= 0.4:
		result.RiskLevel = "high"
	case result.RiskScore >= 0.2:
		result.RiskLevel = "medium"
	default:
		result.RiskLevel = "low"
	}

	if inProgressCount > 0 {
		result.Warnings = append(result.Warnings, "Active work in progress on these files - coordinate before making changes")
	}
	if openCount > 0 {
		result.Warnings = append(result.Warnings, "Open beads touch these files - review before modifying")
	}

	total := inProgressCount + openCount + recentClosedCount
	if total == 0 {
		result.Summary = "No beads found touching these files - safe to proceed"
	} else {
		parts := []string{}
		if inProgressCount > 0 {
			parts = append(parts, fmt.Sprintf("%d %s in progress", inProgressCount, pluralize(inProgressCount, "bead")))
		}
		if openCount > 0 {
			parts = append(parts, fmt.Sprintf("%d open %s", openCount, pluralize(openCount, "bead")))
		}
		if recentClosedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d recently closed %s", recentClosedCount, pluralize(recentClosedCount, "bead")))
		}
		prefix := "Found "
		if inProgressCount > 0 {
			prefix = "⚠️ Conflict risk: "
		}
		result.Summary = prefix + strings.Join(parts, ", ") + " touching these files"
	}

	return result
}

// Helper functions

// normalizePath normalizes a file path for consistent lookup.
func normalizePath(path string) string {
	// Normalize backslashes to forward slashes first (before prefix removal)
	path = strings.ReplaceAll(path, "\\", "/")

	// Remove leading ./ or ./
	path = strings.TrimPrefix(path, "./")

	// Remove trailing slash
	path = strings.TrimSuffix(path, "/")

	return path
}

func classifyBeadStatus(status string) (bucket string, skip bool) {
	switch normalizedBeadStatus(status) {
	case "tombstone":
		return "", true
	case "closed":
		return "closed", false
	default:
		return "open", false
	}
}

func affectedBeadStatusPriority(status string) int {
	switch normalizedBeadStatus(status) {
	case "in_progress":
		return 0
	case "closed":
		return 2
	default:
		return 1
	}
}

func normalizedBeadStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func sortBeadRefs(refs []BeadReference) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].LastTouch.Equal(refs[j].LastTouch) {
			return refs[i].BeadID < refs[j].BeadID
		}
		return refs[i].LastTouch.After(refs[j].LastTouch)
	})
}

func accumulateBeadReference(refs map[string]BeadReference, ref BeadReference) {
	existing, ok := refs[ref.BeadID]
	if !ok {
		ref.CommitSHAs = append([]string(nil), ref.CommitSHAs...)
		refs[ref.BeadID] = ref
		return
	}

	if ref.Title != "" {
		existing.Title = ref.Title
	}
	if ref.Status != "" {
		existing.Status = ref.Status
	}
	for _, sha := range ref.CommitSHAs {
		existing.CommitSHAs = appendUnique(existing.CommitSHAs, sha)
	}
	if ref.LastTouch.After(existing.LastTouch) {
		existing.LastTouch = ref.LastTouch
	}
	existing.TotalChanges += ref.TotalChanges
	refs[ref.BeadID] = existing
}

func beadReferencesFromMap(refs map[string]BeadReference) []BeadReference {
	out := make([]BeadReference, 0, len(refs))
	for _, ref := range refs {
		sort.Strings(ref.CommitSHAs)
		out = append(out, ref)
	}
	return out
}

// pluralize returns the singular or plural form of a word based on count.
func pluralize(count int, singular string) string {
	if count == 1 {
		return singular
	}
	return singular + "s"
}
