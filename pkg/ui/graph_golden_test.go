package ui

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type testGraphFile struct {
	Description string   `json:"description"`
	Nodes       []string `json:"nodes"`
	Edges       [][]int  `json:"edges"`
}

func loadGraphFixture(t *testing.T, name string) []model.Issue {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "graphs", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read graph fixture %s: %v", path, err)
	}

	var graph testGraphFile
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatalf("parse graph fixture %s: %v", path, err)
	}

	issues := make([]model.Issue, len(graph.Nodes))
	for i, id := range graph.Nodes {
		issues[i] = model.Issue{
			ID:        id,
			Title:     id,
			Status:    model.StatusOpen,
			IssueType: model.TypeTask,
			Priority:  2,
		}
	}

	depsBySource := make(map[int][]*model.Dependency)
	for _, edge := range graph.Edges {
		if len(edge) != 2 {
			continue
		}
		from, to := edge[0], edge[1]
		if from < 0 || to < 0 || from >= len(graph.Nodes) || to >= len(graph.Nodes) {
			continue
		}
		depsBySource[from] = append(depsBySource[from], &model.Dependency{
			IssueID:     graph.Nodes[from],
			DependsOnID: graph.Nodes[to],
			Type:        model.DepBlocks,
		})
	}

	for i := range issues {
		if deps, ok := depsBySource[i]; ok {
			issues[i].Dependencies = deps
		}
	}

	return issues
}

func selectGraphID(t *testing.T, g *GraphModel, id string) {
	t.Helper()

	for i, got := range g.sortedIDs {
		if got == id {
			g.selectedIdx = i
			return
		}
	}
	t.Fatalf("graph does not contain id %q", id)
}

func TestGraphView_GoldenASCII(t *testing.T) {
	// Some graph metric styles use Lip Gloss's shared renderer. Pin that as
	// well as the theme renderer; serialize this test while changing global
	// capability, then restore it so color-sensitive tests retain coverage.
	previousProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previousProfile) })

	cases := []struct {
		fixture    string
		selectedID string
	}{
		{fixture: "chain_10", selectedID: "n5"},
		{fixture: "star_10", selectedID: "n0"},
		{fixture: "diamond_5", selectedID: "n3"},
		{fixture: "complex_20", selectedID: "task-14"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.fixture, func(t *testing.T) {
			issues := loadGraphFixture(t, tc.fixture)
			analyzer := analysis.NewAnalyzer(issues)
			stats := analyzer.AnalyzeWithConfig(analysis.FullAnalysisConfig())
			insights := (&stats).GenerateInsights(len(issues))

			// Use deterministic renderer with forced settings
			renderer := lipgloss.NewRenderer(io.Discard)
			renderer.SetColorProfile(termenv.Ascii)
			renderer.SetHasDarkBackground(true) // Force dark mode for consistency
			theme := DefaultTheme(renderer)

			g := NewGraphModel(issues, &insights, theme)
			selectGraphID(t, &g, tc.selectedID)

			out := g.View(78, 40) // narrow mode to focus on ASCII graph rendering

			golden := testutil.NewGoldenFile(t, filepath.Join("..", "..", "testdata", "golden", "graph_render"), tc.fixture+"_ascii.golden")
			golden.Assert(out)
		})
	}
}
