// Package correlation provides related work discovery for beads.
package correlation

import (
	"sort"
	"strconv"
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
	if hr == nil {
		return nil
	}
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
		if strings.TrimSpace(commit.SHA) != "" {
			targetCommits[commit.SHA] = true
		}
		for _, fc := range commit.Files {
			normalizedPath := normalizePath(fc.Path)
			if normalizedPath == "" {
				continue
			}
			targetFiles[normalizedPath] = true
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
			SharedCommits: limitStrings(sortedSHAs, 5),
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

	// Dependency clusters are undirected: A depending on B places both in the
	// same cluster. Build both halves before traversing so second-hop reverse
	// dependencies are reachable too.
	neighbors := make(map[string]map[string]struct{})
	for beadID, deps := range opts.DependencyGraph {
		if neighbors[beadID] == nil {
			neighbors[beadID] = make(map[string]struct{})
		}
		for _, depID := range deps {
			if neighbors[depID] == nil {
				neighbors[depID] = make(map[string]struct{})
			}
			neighbors[beadID][depID] = struct{}{}
			neighbors[depID][beadID] = struct{}{}
		}
	}

	type dependencyHop struct {
		id       string
		distance int
	}
	cluster := make(map[string]int) // beadID -> hop distance
	visited := map[string]bool{targetID: true}
	queue := []dependencyHop{{id: targetID}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.distance == 2 {
			continue
		}
		for neighborID := range neighbors[current.id] {
			if visited[neighborID] {
				continue
			}
			visited[neighborID] = true
			distance := current.distance + 1
			cluster[neighborID] = distance
			queue = append(queue, dependencyHop{id: neighborID, distance: distance})
		}
	}

	// Convert to RelatedWorkBead slice
	var results []RelatedWorkBead

	for beadID, hops := range cluster {
		if seen[beadID] {
			continue
		}
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
	targetStart, targetEnd, ok := relatedActivityWindow(target, now)
	if !ok {
		return []RelatedWorkBead{}
	}

	// Expand by a non-negative tolerance. A negative duration would invert or
	// shrink the interval and can manufacture negative overlap durations.
	concurrencyWindow := opts.ConcurrencyWindow
	if concurrencyWindow < 0 {
		concurrencyWindow = 0
	}
	windowStart := targetStart.Add(-concurrencyWindow)
	windowEnd := targetEnd.Add(concurrencyWindow)

	var results []RelatedWorkBead

	for beadID, history := range hr.Histories {
		if seen[beadID] {
			continue
		}

		// Skip closed/tombstone beads if not requested
		if shouldSkipRelatedStatus(history.Status, opts.IncludeClosed) {
			continue
		}

		beadStart, beadEnd, ok := relatedActivityWindow(history, now)
		if !ok {
			continue
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

// relatedActivityWindow returns the most recent continuous activity interval.
// A reopened bead was inactive between its previous close and reopen, so using
// its original creation time would manufacture concurrency across that gap.
func relatedActivityWindow(history BeadHistory, now time.Time) (time.Time, time.Time, bool) {
	if intervals := temporalActivityIntervalsAt(history.BeadID, history, now); len(intervals) > 0 {
		latest := intervals[len(intervals)-1]
		return latest.Start, latest.End, true
	}

	// Some legacy/hand-built histories have no claim/reopen events. Fall back
	// through their lossy milestone summary, then creation/commit evidence, so
	// they remain analyzable without letting a pre-claim backlog interval
	// override a real lifecycle activation.
	var start time.Time
	if history.Milestones.Claimed != nil {
		start = history.Milestones.Claimed.Timestamp
	}
	if history.Milestones.Reopened != nil && (start.IsZero() || history.Milestones.Reopened.Timestamp.After(start)) {
		start = history.Milestones.Reopened.Timestamp
	}
	if start.IsZero() && history.Milestones.Created != nil {
		start = history.Milestones.Created.Timestamp
	}
	if start.IsZero() {
		for _, commit := range history.Commits {
			if !commit.Timestamp.IsZero() && (start.IsZero() || commit.Timestamp.Before(start)) {
				start = commit.Timestamp
			}
		}
	}
	if start.IsZero() {
		return time.Time{}, time.Time{}, false
	}

	end := now
	if isClosedHistoryStatus(history.Status) {
		if history.Milestones.Closed == nil {
			return time.Time{}, time.Time{}, false
		}
		end = history.Milestones.Closed.Timestamp
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
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
	return strconv.Itoa(n)
}

func limitStrings(s []string, max int) []string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
