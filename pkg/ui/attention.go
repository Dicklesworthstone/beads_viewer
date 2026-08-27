package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// ComputeAttentionView builds a pre-rendered table for label attention
// This keeps the TUI layer simple and deterministic for tests.
func ComputeAttentionView(issues []model.Issue, width int) (string, error) {
	return ComputeAttentionViewAt(issues, width, time.Now())
}

// ComputeAttentionViewAt builds the attention table at a caller-owned clock.
// Historical TUI views use this so old snapshots do not age as wall time moves.
func ComputeAttentionViewAt(issues []model.Issue, width int, now time.Time) (string, error) {
	cfg := analysis.DefaultLabelHealthConfig()
	result := analysis.ComputeLabelAttentionScores(issues, cfg, now.UTC())

	headers := []string{"Rank", "Label", "Attention", "Reason"}
	sepWidth := len(" | ") * (len(headers) - 1)
	colWidths := []int{4, 18, 10, width - 4 - 18 - 10 - sepWidth}
	if colWidths[3] < 20 {
		colWidths[3] = 20
	}

	var b strings.Builder
	row := func(cells []string, _ bool) {
		var parts []string
		for i, c := range cells {
			c = truncate(c, colWidths[i])
			parts = append(parts, padRight(c, colWidths[i]))
		}
		line := strings.Join(parts, " | ")
		b.WriteString(line)
		b.WriteString("\n")
	}

	row(headers, true)
	limit := len(result.Labels)
	if limit > 10 {
		limit = 10
	}
	for i := 0; i < limit; i++ {
		s := result.Labels[i]
		// Use BlockedCount (int) instead of BlockImpact (float)
		reason := fmt.Sprintf("blocked=%d stale=%d vel=%.1f", s.BlockedCount, s.StaleCount, s.VelocityFactor)
		row([]string{
			fmt.Sprintf("%d", i+1),
			s.Label,
			fmt.Sprintf("%.2f", s.AttentionScore),
			reason,
		}, false)
	}

	return b.String(), nil
}
