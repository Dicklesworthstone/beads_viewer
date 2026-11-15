package export

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

// MermaidConfig configures the Mermaid graph generation.
type MermaidConfig struct {
	ShowNoDependenciesNode bool                  // If true, adds a "No Dependencies" node when no edges exist
	Readiness              *model.ReadinessIndex // Optional full-source readiness index (falls back to issues when nil)
}

// GenerateMermaidGraph generates a Mermaid diagram for the given issues.
func GenerateMermaidGraph(issues []model.Issue, issueIDs map[string]bool, config MermaidConfig) string {
	var sb strings.Builder

	sb.WriteString("graph TD\n")

	// Class definitions for styling
	sb.WriteString("    classDef open fill:#50FA7B,stroke:#333,color:#000\n")
	sb.WriteString("    classDef inprogress fill:#8BE9FD,stroke:#333,color:#000\n")
	sb.WriteString("    classDef blocked fill:#FF5555,stroke:#333,color:#000\n")
	sb.WriteString("    classDef deferred fill:#FFB86C,stroke:#333,color:#000\n")
	sb.WriteString("    classDef pinned fill:#BD93F9,stroke:#333,color:#000\n")
	sb.WriteString("    classDef hooked fill:#FF79C6,stroke:#333,color:#000\n")
	sb.WriteString("    classDef review fill:#F1FA8C,stroke:#333,color:#000\n")
	sb.WriteString("    classDef custom fill:#E2E8F0,stroke:#333,color:#000\n")
	sb.WriteString("    classDef closed fill:#6272A4,stroke:#333,color:#fff\n")
	sb.WriteString("\n")

	readiness := config.Readiness
	if readiness == nil {
		readiness = model.NewReadinessIndex(issues)
	}

	// Sort issues for deterministic output
	sortedIssues := make([]model.Issue, len(issues))
	copy(sortedIssues, issues)
	sort.Slice(sortedIssues, func(i, j int) bool {
		return sortedIssues[i].ID < sortedIssues[j].ID
	})

	// Build deterministic, collision-free Mermaid IDs
	safeIDMap := make(map[string]string)
	usedSafe := make(map[string]bool)

	getSafeID := func(orig string) string {
		if safe, ok := safeIDMap[orig]; ok {
			return safe
		}
		base := sanitizeMermaidID(orig)
		if base == "" {
			base = "node"
		}
		safe := base
		if usedSafe[safe] && safeIDMap[orig] == "" {
			// Collision: derive stable hash-based suffix
			h := fnv.New32a()
			_, _ = h.Write([]byte(orig))
			safe = fmt.Sprintf("%s_%x", base, h.Sum32())
		}
		usedSafe[safe] = true
		safeIDMap[orig] = safe
		return safe
	}

	// Pre-calculate all safe IDs to ensure consistency
	for _, i := range sortedIssues {
		getSafeID(i.ID)
	}

	hasLinks := false

	// Nodes
	for _, i := range sortedIssues {
		safeID := getSafeID(i.ID)
		safeTitle := sanitizeMermaidText(i.Title)
		safeLabelID := sanitizeMermaidText(i.ID)

		sb.WriteString(fmt.Sprintf("    %s[\"%s<br/>%s\"]\n", safeID, safeLabelID, safeTitle))

		// Apply class based on status and dependency readiness
		depBlocked := !isClosedLikeStatus(i.Status) &&
			(i.Status == model.StatusOpen || i.Status == model.StatusInProgress || i.Status == "") &&
			len(readiness.Blockers(i.ID)) > 0
		class := mermaidStatusClass(i.Status, depBlocked)
		if class != "" {
			sb.WriteString(fmt.Sprintf("    class %s %s\n", safeID, class))
		}
	}

	sb.WriteString("\n")

	// Edges
	for _, i := range sortedIssues {
		// Sort dependencies
		deps := make([]*model.Dependency, len(i.Dependencies))
		copy(deps, i.Dependencies)
		sort.Slice(deps, func(a, b int) bool {
			if deps[a] == nil {
				return false
			}
			if deps[b] == nil {
				return true
			}
			return deps[a].DependsOnID < deps[b].DependsOnID
		})

		for _, dep := range deps {
			if dep == nil || !issueIDs[dep.DependsOnID] {
				continue
			}

			safeFromID := getSafeID(i.ID)
			safeToID := getSafeID(dep.DependsOnID)

			linkStyle := "-.->" // Dashed for related
			if dep.Type.IsBlocking() {
				linkStyle = "==>" // Bold for blockers
			}

			sb.WriteString(fmt.Sprintf("    %s %s %s\n", safeFromID, linkStyle, safeToID))
			hasLinks = true
		}
	}

	if config.ShowNoDependenciesNode && !hasLinks && len(issues) > 0 {
		sb.WriteString("    NoLinks[\"No Dependencies\"]\n")
	}

	return sb.String()
}

func mermaidStatusClass(status model.Status, depBlocked bool) string {
	switch {
	case isClosedLikeStatus(status):
		return "closed"
	case status == model.StatusBlocked || depBlocked:
		return "blocked"
	case status == model.StatusOpen || status == "":
		return "open"
	case status == model.StatusInProgress:
		return "inprogress"
	case status == model.StatusDeferred:
		return "deferred"
	case status == model.StatusPinned:
		return "pinned"
	case status == model.StatusHooked:
		return "hooked"
	case status == model.StatusReview:
		return "review"
	default:
		return "custom"
	}
}

// Note: sanitizeMermaidID and sanitizeMermaidText are defined in markdown.go
