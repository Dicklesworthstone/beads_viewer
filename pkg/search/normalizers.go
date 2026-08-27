package search

import (
	"math"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// normalizeStatus maps status to [0,1] range favoring actionable states.
func normalizeStatus(status string) float64 {
	switch model.Status(strings.ToLower(strings.TrimSpace(status))) {
	case model.StatusOpen:
		return 1.0
	case model.StatusInProgress, model.StatusHooked, model.StatusReview:
		return 0.8
	case model.StatusPinned:
		return 0.7
	case model.StatusBlocked:
		return 0.5
	case model.StatusDraft, model.StatusDeferred:
		return 0.2
	case model.StatusClosed:
		return 0.1
	case model.StatusTombstone:
		return 0.0
	default:
		return 0.5
	}
}

// normalizePriority maps P0-P4 to [0.2, 1.0] range.
func normalizePriority(priority int) float64 {
	switch priority {
	case 0:
		return 1.0
	case 1:
		return 0.8
	case 2:
		return 0.6
	case 3:
		return 0.4
	case 4:
		return 0.2
	default:
		return 0.5
	}
}

// normalizeImpact normalizes blocker count to [0,1].
func normalizeImpact(blockerCount, maxBlockerCount int) float64 {
	if maxBlockerCount == 0 {
		return 0.5
	}
	if blockerCount <= 0 {
		return 0
	}
	if blockerCount >= maxBlockerCount {
		return 1.0
	}
	return float64(blockerCount) / float64(maxBlockerCount)
}

// normalizeRecency applies exponential decay with a 30-day decay constant
// (half-life about 21 days) relative to the current clock. Call
// normalizeRecencyAt when the caller owns a historical or otherwise pinned
// analysis clock.
func normalizeRecency(updatedAt time.Time) float64 {
	return normalizeRecencyAt(updatedAt, time.Now())
}

func normalizeRecencyAt(updatedAt, now time.Time) float64 {
	if updatedAt.IsZero() {
		return 0.5
	}
	if now.IsZero() {
		now = time.Now()
	}
	daysSinceUpdate := now.Sub(updatedAt).Hours() / 24
	if daysSinceUpdate < 0 {
		return 1.0
	}
	score := math.Exp(-daysSinceUpdate / 30)
	if score > 1.0 {
		return 1.0
	}
	return score
}
