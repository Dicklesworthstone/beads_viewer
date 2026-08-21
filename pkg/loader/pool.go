package loader

import (
	"sync"
	"sync/atomic"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

const (
	defaultDepsCap     = 8
	defaultCommentsCap = 4
	defaultLabelsCap   = 8
)

// IssuePool manages reusable Issue structs.
// Only return issues when they are no longer referenced by any snapshot.
var IssuePool = sync.Pool{
	New: func() any {
		issuePoolNews.Add(1)
		return &model.Issue{
			Dependencies: make([]*model.Dependency, 0, defaultDepsCap),
			Comments:     make([]*model.Comment, 0, defaultCommentsCap),
			Labels:       make([]string, 0, defaultLabelsCap),
		}
	},
}

var issuePoolGets atomic.Uint64
var issuePoolNews atomic.Uint64

// PooledIssues bundles parsed issues with their pooled backing objects.
// The pooled references must be returned via ReturnIssuePtrsToPool when
// the snapshot is no longer used.
type PooledIssues struct {
	Issues   []model.Issue
	PoolRefs []*model.Issue
}

// GetIssue retrieves an Issue from the pool and resets it.
func GetIssue() *model.Issue {
	issuePoolGets.Add(1)
	issue := IssuePool.Get().(*model.Issue)
	resetIssue(issue)
	return issue
}

// PutIssue returns an Issue to the pool for reuse.
// After this call, the issue must not be used.
func PutIssue(issue *model.Issue) {
	if issue == nil {
		return
	}
	clearIssueReferences(issue)
	resetIssue(issue)
	IssuePool.Put(issue)
}

// ReturnIssuePtrsToPool returns a slice of pooled issue pointers to the pool.
// Use this when a snapshot is replaced and no longer referenced.
func ReturnIssuePtrsToPool(issues []*model.Issue) {
	for _, issue := range issues {
		PutIssue(issue)
	}
}

// IssuePoolStats returns the total pool hits and misses since process start.
func IssuePoolStats() (hits uint64, misses uint64) {
	gets := issuePoolGets.Load()
	news := issuePoolNews.Load()
	if gets >= news {
		return gets - news, news
	}
	return 0, news
}

func resetIssue(issue *model.Issue) {
	deps := issue.Dependencies
	comments := issue.Comments
	labels := issue.Labels

	*issue = model.Issue{}

	if deps == nil {
		deps = make([]*model.Dependency, 0, defaultDepsCap)
	} else {
		deps = deps[:0]
	}
	if comments == nil {
		comments = make([]*model.Comment, 0, defaultCommentsCap)
	} else {
		comments = comments[:0]
	}
	if labels == nil {
		labels = make([]string, 0, defaultLabelsCap)
	} else {
		labels = labels[:0]
	}

	issue.Dependencies = deps
	issue.Comments = comments
	issue.Labels = labels
}

func clearIssueReferences(issue *model.Issue) {
	if issue == nil {
		return
	}

	for i := range issue.Dependencies {
		issue.Dependencies[i] = nil
	}
	for i := range issue.Comments {
		issue.Comments[i] = nil
	}
	for i := range issue.Labels {
		issue.Labels[i] = ""
	}

	issue.Dependencies = issue.Dependencies[:0]
	issue.Comments = issue.Comments[:0]
	issue.Labels = issue.Labels[:0]

	issue.DueDate = nil
	issue.DeferUntil = nil
	issue.ClosedAt = nil
	issue.EstimatedMinutes = nil
	issue.ExternalRef = nil
	issue.CompactedAt = nil
	issue.CompactedAtCommit = nil
}

// DeepCopyIssueSlices creates independent copies of the Dependencies, Comments,
// and Labels slices in the destination issue to break sharing with pooled backing arrays.
// This must be called before returning a pooled issue to the pool to prevent
// data races and corruption when the pool reuses the backing arrays.
func DeepCopyIssueSlices(dst *model.Issue) {
	if dst == nil {
		return
	}

	// Deep copy Dependencies - each Dependency is a pointer, so we copy the slice
	// but the Dependency structs themselves are not modified by the pool reset.
	// Copy even zero-length non-nil slices to drop pooled backing-array capacity.
	if dst.Dependencies != nil {
		oldDeps := dst.Dependencies
		dst.Dependencies = make([]*model.Dependency, len(oldDeps))
		copy(dst.Dependencies, oldDeps)
	}

	// Deep copy Comments - same logic as Dependencies.
	if dst.Comments != nil {
		oldComments := dst.Comments
		dst.Comments = make([]*model.Comment, len(oldComments))
		copy(dst.Comments, oldComments)
	}

	// Deep copy Labels - strings are immutable so copying the slice is sufficient.
	if dst.Labels != nil {
		oldLabels := dst.Labels
		dst.Labels = make([]string, len(oldLabels))
		copy(dst.Labels, oldLabels)
	}
}
