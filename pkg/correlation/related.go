// Package correlation provides related work discovery for beads.
package correlation

import (
	"sort"
	"strings"
	"time"
)

// RelationType categorizes how beads are related
type RelationType string

const (
	// RelationFileOverlap indicates beads touch the same files
	RelationFileOverlap RelationType = "file_overlap"
	// RelationCommitOverlap indicates beads share commits
	RelationCommitOverlap RelationType = "commit_overlap"
	// RelationDependencyCluster indicates beads in same dependency cluster
	RelationDependencyCluster RelationType = "dependency_cluster"
	// RelationConcurrent indicates beads from same time window
	RelationConcurrent RelationType = "concurrent"
)

// RelatedWorkBead represents a bead that's related to a target bead
type RelatedWorkBead struct {
	BeadID        string       `json:"bead_id"`
	Title         string       `json:"title"`
	Status        string       `json:"status"`
	RelationType  RelationType `json:"relation_type"`
	Relevance     int          `json:"relevance"` // 0-100 score
	Reason        string       `json:"reason"`    // Human-readable explanation
	SharedFiles   []string     `json:"shared_files,omitempty"`
	SharedCommits []string     `json:"shared_commits,omitempty"`
}

// RelatedWorkResult contains all related beads grouped by relationship type
type RelatedWorkResult struct {
	TargetBeadID      string            `json:"target_bead_id"`
	TargetTitle       string            `json:"target_title"`
	FileOverlap       []RelatedWorkBead `json:"file_overlap"`
	CommitOverlap     []RelatedWorkBead `json:"commit_overlap"`
	DependencyCluster []RelatedWorkBead `json:"dependency_cluster"`
	Concurrent        []RelatedWorkBead `json:"concurrent"`
	TotalRelated      int               `json:"total_related"`
	GeneratedAt       time.Time         `json:"generated_at"`
}

// RelatedWorkOptions configures related work discovery
type RelatedWorkOptions struct {
	MinRelevance      int                 // Minimum relevance score (0-100) to include
	MaxResults        int                 // Maximum results per category (0 = unlimited)
	ConcurrencyWindow time.Duration       // Time window for concurrent detection
	IncludeClosed     bool                // Include closed beads in results
	FileLookup        *FileLookup         // Pre-built file lookup (optional)
	DependencyGraph   map[string][]string // BeadID -> []DependsOnIDs
}

// DefaultRelatedWorkOptions returns sensible defaults
func DefaultRelatedWorkOptions() RelatedWorkOptions {
	return RelatedWorkOptions{
		MinRelevance:      20,
		MaxResults:        10,
		ConcurrencyWindow: 7 * 24 * time.Hour, // 1 week
		IncludeClosed:     false,
	}
}

// FindRelatedWork discovers beads related to a target bead
func (hr *HistoryReport) FindRelatedWork(targetID string, opts RelatedWorkOptions) *RelatedWorkResult {
	return hr.FindRelatedWorkAt(targetID, opts, time.Now())
}

// FindRelatedWorkAt discovers related work using a caller-owned reference
// instant for open activity windows and result metadata. The zero instant is
// valid and is preserved.
func (hr *HistoryReport) FindRelatedWorkAt(targetID string, opts RelatedWorkOptions, now time.Time) *RelatedWorkResult {
	target, exists := hr.Histories[targetID]
	if !exists {
		return nil
	}

	result := &RelatedWorkResult{
		TargetBeadID:      targetID,
		TargetTitle:       target.Title,
		FileOverlap:       []RelatedWorkBead{},
		CommitOverlap:     []RelatedWorkBead{},
		DependencyCluster: []RelatedWorkBead{},
		Concurrent:        []RelatedWorkBead{},
		GeneratedAt:       now,
	}

	// Build file lookup if not provided
	fileLookup := opts.FileLookup
	if fileLookup == nil {
		fileLookup = NewFileLookup(hr)
	}

	// Collect target's files and commits
	targetFiles := make(map[string]bool)
	targetCommits := make(map[string]bool)
	for _, commit := range target.Commits {
		targetCommits[commit.SHA] = true
		for _, fc := range commit.Files {
			targetFiles[normalizePath(fc.Path)] = true
		}
	}

	// Track seen beads to avoid duplicates across categories
	seen := make(map[string]bool)
	seen[targetID] = true // Don't include target in results

	// 1. File Overlap Detection
	fileOverlapCandidates := hr.findFileOverlap(targetID, targetFiles, fileLookup, opts, seen)
	result.FileOverlap = fileOverlapCandidates
	for _, rb := range fileOverlapCandidates {
		seen[rb.BeadID] = true
	}

	// 2. Commit Overlap Detection
	commitOverlapCandidates := hr.findCommitOverlap(targetID, targetCommits, opts, seen)
	result.CommitOverlap = commitOverlapCandidates
	for _, rb := range commitOverlapCandidates {
		seen[rb.BeadID] = true
	}

	// 3. Dependency Cluster Detection
	if opts.DependencyGraph != nil {
		depClusterCandidates := hr.findDependencyCluster(targetID, opts, seen)
		result.DependencyCluster = depClusterCandidates
		for _, rb := range depClusterCandidates {
			seen[rb.BeadID] = true
		}
	}

	// 4. Concurrent Detection (same time window)
	concurrentCandidates := hr.findConcurrent(targetID, target, opts, seen, now)
	result.Concurrent = concurrentCandidates
	if result.FileOverlap == nil {
		result.FileOverlap = []RelatedWorkBead{}
	}
	if result.CommitOverlap == nil {
		result.CommitOverlap = []RelatedWorkBead{}
	}
	if result.DependencyCluster == nil {
		result.DependencyCluster = []RelatedWorkBead{}
	}
	if result.Concurrent == nil {
		result.Concurrent = []RelatedWorkBead{}
	}

	// Calculate total
	result.TotalRelated = len(result.FileOverlap) + len(result.CommitOverlap) +
		len(result.DependencyCluster) + len(result.Concurrent)

	return result
}

// findFileOverlap finds beads that touch the same files as the target
func (hr *HistoryReport) findFileOverlap(targetID string, targetFiles map[string]bool, fileLookup *FileLookup, opts RelatedWorkOptions, seen map[string]bool) []RelatedWorkBead {
	if fileLookup == nil || len(targetFiles) == 0 {
		return []RelatedWorkBead{}
	}

	// Count file overlaps per bead
	overlapCount := make(map[string][]string) // beadID -> shared files

	for file := range targetFiles {
		lookup := fileLookup.LookupByFile(file)
		// Combine open and closed beads from lookup result
		for _, ref := range lookup.OpenBeads {
			if seen[ref.BeadID] {
				continue
			}
			overlapCount[ref.BeadID] = append(overlapCount[ref.BeadID], file)
		}
		if opts.IncludeClosed {
			for _, ref := range lookup.ClosedBeads {
				if seen[ref.BeadID] {
					continue
				}
				overlapCount[ref.BeadID] = append(overlapCount[ref.BeadID], file)
			}
		}
	}

	// Convert to RelatedWorkBead slice
	var results []RelatedWorkBead
	totalTargetFiles := len(targetFiles)

	for beadID, sharedFiles := range overlapCount {
		history, exists := hr.Histories[beadID]
		if !exists {
			continue
		}

		// Skip closed/tombstone beads if not requested
		if shouldSkipRelatedStatus(history.Status, opts.IncludeClosed) {
			continue
		}

		// Calculate relevance based on file overlap percentage
		relevance := (len(sharedFiles) * 100) / totalTargetFiles
		if relevance > 100 {
			relevance = 100
		}

		if relevance < opts.MinRelevance {
			continue
		}

		sortedShared := append([]string(nil), sharedFiles...)
		sort.Strings(sortedShared)
		results = append(results, RelatedWorkBead{
			BeadID:       beadID,
			Title:        history.Title,
			Status:       history.Status,
			RelationType: RelationFileOverlap,
			Relevance:    relevance,
			Reason:       formatFileOverlapReason(len(sharedFiles), totalTargetFiles),
			SharedFiles:  limitStrings(sortedShared, 5),
		})
	}

	// Sort by relevance descending
	sortRelatedResults(results)

	// Limit results
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	return results
}

// findCommitOverlap finds beads that share commits with the target
func (hr *HistoryReport) findCommitOverlap(targetID string, targetCommits map[string]bool, opts RelatedWorkOptions, seen map[string]bool) []RelatedWorkBead {
	if len(targetCommits) == 0 {
		return []RelatedWorkBead{}
	}

	// Count shared commits per bead
	sharedCount := make(map[string][]string) // beadID -> shared commit SHAs

	for sha := range targetCommits {
		beadIDs, exists := hr.CommitIndex[sha]
		if !exists {
			continue
		}
		for _, beadID := range beadIDs {
			if seen[beadID] || beadID == targetID {
				continue
			}
			sharedCount[beadID] = appendUnique(sharedCount[beadID], sha)
		}
	}

	// Convert to RelatedWorkBead slice
	var results []RelatedWorkBead
	totalTargetCommits := len(targetCommits)

	for beadID, sharedSHAs := range sharedCount {
		history, exists := hr.Histories[beadID]
		if !exists {
			continue
		}

		// Skip closed/tombstone beads if not requested
		if shouldSkipRelatedStatus(history.Status, opts.IncludeClosed) {
			continue
		}

		// Calculate relevance based on commit overlap percentage
		relevance := (len(sharedSHAs) * 100) / totalTargetCommits
		if relevance > 100 {
			relevance = 100
		}

		if relevance < opts.MinRelevance {
			continue
		}

		sortedSHAs := append([]string(nil), sharedSHAs...)
		sort.Strings(sortedSHAs)
		results = append(results, RelatedWorkBead{
			BeadID:        beadID,
			Title:         history.Title,
			Status:        history.Status,
			RelationType:  RelationCommitOverlap,
			Relevance:     relevance,
			Reason:        formatCommitOverlapReason(len(sharedSHAs), totalTargetCommits),
			SharedCommits: limitStrings(shortenSHAs(sortedSHAs), 5),
		})
	}

	// Sort by relevance descending
	sortRelatedResults(results)

	// Limit results
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	return results
}

// findDependencyCluster finds beads in the same dependency cluster
func (hr *HistoryReport) findDependencyCluster(targetID string, opts RelatedWorkOptions, seen map[string]bool) []RelatedWorkBead {
	if opts.DependencyGraph == nil {
		return []RelatedWorkBead{}
	}

	// BFS to find beads within 2 hops of target (direct deps and their deps)
	cluster := make(map[string]int) // beadID -> hop distance

	// Direct dependencies (things target depends on)
	if deps, ok := opts.DependencyGraph[targetID]; ok {
		for _, depID := range deps {
			if !seen[depID] {
				cluster[depID] = 1
			}
		}
	}

	// Reverse dependencies (things that depend on target)
	for beadID, deps := range opts.DependencyGraph {
		if seen[beadID] {
			continue
		}
		for _, depID := range deps {
			if depID == targetID {
				if _, exists := cluster[beadID]; !exists {
					cluster[beadID] = 1
				}
			}
		}
	}

	// Second hop: deps of deps
	firstHop := make([]string, 0, len(cluster))
	for id := range cluster {
		firstHop = append(firstHop, id)
	}

	for _, hopID := range firstHop {
		if deps, ok := opts.DependencyGraph[hopID]; ok {
			for _, depID := range deps {
				if !seen[depID] && depID != targetID {
					if _, exists := cluster[depID]; !exists {
						cluster[depID] = 2
					}
				}
			}
		}
	}

	// Convert to RelatedWorkBead slice
	var results []RelatedWorkBead

	for beadID, hops := range cluster {
		history, exists := hr.Histories[beadID]
		if !exists {
			continue
		}

		// Skip closed/tombstone beads if not requested
		if shouldSkipRelatedStatus(history.Status, opts.IncludeClosed) {
			continue
		}

		// Relevance: direct deps (1 hop) = 80, indirect (2 hops) = 40
		relevance := 80
		reason := "Direct dependency"
		if hops == 2 {
			relevance = 40
			reason = "Indirect dependency (2 hops)"
		}

		if relevance < opts.MinRelevance {
			continue
		}

		results = append(results, RelatedWorkBead{
			BeadID:       beadID,
			Title:        history.Title,
			Status:       history.Status,
			RelationType: RelationDependencyCluster,
			Relevance:    relevance,
			Reason:       reason,
		})
	}

	sortRelatedResults(results)

	// Limit results
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	return results
}

// findConcurrent finds beads active in the same time window
func (hr *HistoryReport) findConcurrent(targetID string, target BeadHistory, opts RelatedWorkOptions, seen map[string]bool, now time.Time) []RelatedWorkBead {
	// Determine target's activity window
	var targetStart, targetEnd time.Time

	if target.Milestones.Created != nil {
		targetStart = target.Milestones.Created.Timestamp
	}
	if isClosedHistoryStatus(target.Status) && target.Milestones.Closed != nil {
		targetEnd = target.Milestones.Closed.Timestamp
	} else {
		targetEnd = now
	}

	// If no start time, use first commit time
	if targetStart.IsZero() && len(target.Commits) > 0 {
		targetStart = target.Commits[0].Timestamp
	}

	// If still no times, can't determine concurrency
	if targetStart.IsZero() {
		return []RelatedWorkBead{}
	}
	if targetEnd.Before(targetStart) {
		targetEnd = targetStart
	}

	// Expand window by concurrency window option
	windowStart := targetStart.Add(-opts.ConcurrencyWindow)
	windowEnd := targetEnd.Add(opts.ConcurrencyWindow)

	var results []RelatedWorkBead

	for beadID, history := range hr.Histories {
		if seen[beadID] {
			continue
		}

		// Skip closed/tombstone beads if not requested
		if shouldSkipRelatedStatus(history.Status, opts.IncludeClosed) {
			continue
		}

		// Determine this bead's activity window
		var beadStart, beadEnd time.Time

		if history.Milestones.Created != nil {
			beadStart = history.Milestones.Created.Timestamp
		}
		if isClosedHistoryStatus(history.Status) && history.Milestones.Closed != nil {
			beadEnd = history.Milestones.Closed.Timestamp
		} else {
			beadEnd = now
		}

		// Use first commit if no created timestamp
		if beadStart.IsZero() && len(history.Commits) > 0 {
			beadStart = history.Commits[0].Timestamp
		}

		if beadStart.IsZero() {
			continue
		}
		if beadEnd.Before(beadStart) {
			beadEnd = beadStart
		}

		// Check for overlap
		if !beadStart.After(windowEnd) && !beadEnd.Before(windowStart) {
			// Calculate overlap duration for relevance
			overlapStart := beadStart
			if overlapStart.Before(windowStart) {
				overlapStart = windowStart
			}
			overlapEnd := beadEnd
			if overlapEnd.After(windowEnd) {
				overlapEnd = windowEnd
			}

			overlapDuration := overlapEnd.Sub(overlapStart)
			targetDuration := targetEnd.Sub(targetStart)

			// Relevance based on overlap percentage
			relevance := 30 // Base relevance for any overlap
			if targetDuration > 0 {
				overlapPct := int((float64(overlapDuration) / float64(targetDuration)) * 50)
				relevance += overlapPct
				if relevance > 100 {
					relevance = 100
				}
			}

			if relevance < opts.MinRelevance {
				continue
			}

			results = append(results, RelatedWorkBead{
				BeadID:       beadID,
				Title:        history.Title,
				Status:       history.Status,
				RelationType: RelationConcurrent,
				Relevance:    relevance,
				Reason:       formatConcurrentReason(overlapDuration),
			})
		}
	}

	sortRelatedResults(results)

	// Limit results
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	return results
}

// Helper functions

func shouldSkipRelatedStatus(status string, includeClosed bool) bool {
	normalized := normalizeStatus(status)
	if normalized == "tombstone" {
		return true
	}
	if !includeClosed && normalized == "closed" {
		return true
	}
	return false
}

func sortRelatedResults(results []RelatedWorkBead) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Relevance == results[j].Relevance {
			return results[i].BeadID < results[j].BeadID
		}
		return results[i].Relevance > results[j].Relevance
	})
}

func normalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func formatFileOverlapReason(shared, total int) string {
	pct := (shared * 100) / total
	if shared == 1 {
		return "1 shared file"
	}
	return formatPluralRelated(shared, "shared file", "shared files") + formatPctRelated(pct)
}

func formatCommitOverlapReason(shared, total int) string {
	pct := (shared * 100) / total
	if shared == 1 {
		return "1 shared commit"
	}
	return formatPluralRelated(shared, "shared commit", "shared commits") + formatPctRelated(pct)
}

func formatConcurrentReason(overlap time.Duration) string {
	days := int(overlap.Hours() / 24)
	if days < 1 {
		return "Active in same time window"
	}
	return formatPluralRelated(days, "day", "days") + " of overlapping activity"
}

func formatPluralRelated(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return formatIntRelated(n) + " " + plural
}

func formatPctRelated(pct int) string {
	if pct <= 0 {
		return ""
	}
	return " (" + formatIntRelated(pct) + "%)"
}

func formatIntRelated(n int) string {
	// Simple int to string without importing strconv
	if n == 0 {
		return "0"
	}
	result := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	if neg {
		result = "-" + result
	}
	return result
}

func limitStrings(s []string, max int) []string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func shortenSHAs(shas []string) []string {
	result := make([]string, len(shas))
	for i, sha := range shas {
		if len(sha) > 7 {
			result[i] = sha[:7]
		} else {
			result[i] = sha
		}
	}
	return result
}
