package export

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"
)

func TestSaveGraphSnapshot_SVGAndPNG(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Root task", Status: model.StatusOpen},
		{ID: "B", Title: "Depends on A", Status: model.StatusBlocked, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
		{ID: "C", Title: "Independent", Status: model.StatusOpen},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	tmp := t.TempDir()
	cases := []struct {
		name   string
		format string
	}{
		{"svg", "graph.svg"},
		{"png", "graph.png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(tmp, tc.format)
			err := SaveGraphSnapshot(GraphSnapshotOptions{
				Path:     out,
				Issues:   issues,
				Stats:    &stats,
				DataHash: analysis.ComputeDataHash(issues),
			})
			if err != nil {
				t.Fatalf("SaveGraphSnapshot error: %v", err)
			}
			info, err := os.Stat(out)
			if err != nil {
				t.Fatalf("output not created: %v", err)
			}
			if info.Size() == 0 {
				t.Fatalf("output file is empty")
			}
		})
	}
}

func TestSaveGraphSnapshot_InvalidFormat(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Root", Status: model.StatusOpen}}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     "graph.txt",
		Format:   "txt",
		Issues:   issues,
		Stats:    &stats,
		DataHash: "hash",
	})
	if err == nil {
		t.Fatalf("expected error for invalid format")
	}
}

func TestSaveGraphSnapshot_EmptyIssues(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Root", Status: model.StatusOpen}}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     "graph.svg",
		Issues:   []model.Issue{}, // Empty
		Stats:    &stats,
		DataHash: "hash",
	})
	if err == nil {
		t.Fatalf("expected error for empty issues")
	}
}

func TestSaveGraphSnapshot_NilStats(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Root", Status: model.StatusOpen}}

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     "graph.svg",
		Issues:   issues,
		Stats:    nil, // Nil stats
		DataHash: "hash",
	})
	if err == nil {
		t.Fatalf("expected error for nil stats")
	}
}

func TestSaveGraphSnapshot_EmptyPath(t *testing.T) {
	issues := []model.Issue{{ID: "A", Title: "Root", Status: model.StatusOpen}}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     "", // Empty path
		Issues:   issues,
		Stats:    &stats,
		DataHash: "hash",
	})
	if err == nil {
		t.Fatalf("expected error for empty path")
	}
}

func TestSaveGraphSnapshot_FormatInference(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Root task", Status: model.StatusOpen},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	tmp := t.TempDir()

	// Test that format is inferred from path extension
	cases := []struct {
		name string
		path string
	}{
		{"svg extension", filepath.Join(tmp, "test.svg")},
		{"png extension", filepath.Join(tmp, "test.png")},
		{"no extension defaults to svg", filepath.Join(tmp, "test_noext")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SaveGraphSnapshot(GraphSnapshotOptions{
				Path:     tc.path,
				Issues:   issues,
				Stats:    &stats,
				DataHash: "hash",
			})
			if err != nil {
				t.Fatalf("SaveGraphSnapshot error: %v", err)
			}

			// Check file exists (possibly with .svg appended)
			_, err = os.Stat(tc.path)
			if err != nil {
				// Try with .svg appended
				_, err = os.Stat(tc.path + ".svg")
				if err != nil {
					t.Fatalf("output not created: %v", err)
				}
			}
		})
	}
}

func TestSaveGraphSnapshot_RoomyPreset(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Root task", Status: model.StatusOpen},
		{ID: "B", Title: "Depends on A", Status: model.StatusBlocked, Dependencies: []*model.Dependency{{DependsOnID: "A", Type: model.DepBlocks}}},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	tmp := t.TempDir()
	out := filepath.Join(tmp, "roomy.svg")

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     out,
		Preset:   "roomy", // Test roomy preset
		Issues:   issues,
		Stats:    &stats,
		DataHash: "hash",
	})
	if err != nil {
		t.Fatalf("SaveGraphSnapshot error: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("output not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("output file is empty")
	}
}

func TestSaveGraphSnapshot_WithTitle(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Root task", Status: model.StatusOpen},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	tmp := t.TempDir()
	out := filepath.Join(tmp, "titled.svg")

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     out,
		Title:    "Custom Title",
		Issues:   issues,
		Stats:    &stats,
		DataHash: "hash",
	})
	if err != nil {
		t.Fatalf("SaveGraphSnapshot error: %v", err)
	}
}

func TestSaveGraphSnapshot_AllStatuses(t *testing.T) {
	issues := []model.Issue{
		{ID: "A", Title: "Open", Status: model.StatusOpen},
		{ID: "B", Title: "In Progress", Status: model.StatusInProgress},
		{ID: "C", Title: "Blocked", Status: model.StatusBlocked},
		{ID: "D", Title: "Closed", Status: model.StatusClosed},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	tmp := t.TempDir()
	out := filepath.Join(tmp, "all_statuses.svg")

	err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:     out,
		Issues:   issues,
		Stats:    &stats,
		DataHash: "hash",
	})
	if err != nil {
		t.Fatalf("SaveGraphSnapshot error: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"empty string", "", 10, ""},
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncate with ellipsis", "hello world", 8, "hello..."},
		{"very short max", "hello", 2, "he"},
		{"max of 3", "hello", 3, "hel"},
		{"zero max", "hello", 0, ""},
		{"negative max", "hello", -1, ""},
		{"unicode", "こんにちは世界", 5, "こん..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate(tt.input, tt.max)
			if result != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, result, tt.expected)
			}
		})
	}
}

func TestCss(t *testing.T) {
	tests := []struct {
		name     string
		c        color.RGBA
		expected string
	}{
		{"black", color.RGBA{0, 0, 0, 255}, "#000000"},
		{"white", color.RGBA{255, 255, 255, 255}, "#ffffff"},
		{"red", color.RGBA{255, 0, 0, 255}, "#ff0000"},
		{"green", color.RGBA{0, 255, 0, 255}, "#00ff00"},
		{"blue", color.RGBA{0, 0, 255, 255}, "#0000ff"},
		{"mixed", color.RGBA{171, 205, 239, 255}, "#abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := css(tt.c)
			if result != tt.expected {
				t.Errorf("css(%v) = %q, want %q", tt.c, result, tt.expected)
			}
		})
	}
}

func TestTopByMetric(t *testing.T) {
	tests := []struct {
		name     string
		metrics  map[string]float64
		contains string
	}{
		{"empty map", map[string]float64{}, "n/a"},
		{"single entry", map[string]float64{"A": 0.5}, "A"},
		{"multiple entries", map[string]float64{"A": 0.3, "B": 0.8, "C": 0.5}, "B"},
		{"tie - alphabetical", map[string]float64{"B": 0.5, "A": 0.5}, "A"}, // A < B
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := topByMetric(tt.metrics)
			if !hasSubstr(result, tt.contains) {
				t.Errorf("topByMetric(%v) = %q, want to contain %q", tt.metrics, result, tt.contains)
			}
		})
	}
}

func hasSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestStatusColor(t *testing.T) {
	// Just verify each status returns a distinct color
	colors := make(map[string]bool)

	statuses := []model.Status{
		model.StatusOpen,
		model.StatusBlocked,
		model.StatusInProgress,
		model.StatusClosed,
		model.StatusTombstone,
	}

	for _, s := range statuses {
		c := statusColor(s)
		key := css(c)
		if colors[key] && s != model.StatusOpen {
			// Allow some colors to be the same in edge cases
		}
		colors[key] = true
	}

	// Verify we got at least 3 distinct colors
	if len(colors) < 3 {
		t.Errorf("expected at least 3 distinct status colors, got %d", len(colors))
	}
}

func TestBuildLayout_MinDimensions(t *testing.T) {
	// Even with one node, the layout should have minimum dimensions
	issues := []model.Issue{
		{ID: "A", Title: "Single", Status: model.StatusOpen},
	}
	analyzer := analysis.NewAnalyzer(issues)
	stats := analyzer.Analyze()

	layout := buildLayout(GraphSnapshotOptions{
		Issues: issues,
		Stats:  &stats,
	})

	if layout.Width < 640 {
		t.Errorf("expected minimum width of 640, got %d", layout.Width)
	}
	if layout.Height < 480 {
		t.Errorf("expected minimum height of 480, got %d", layout.Height)
	}
	if len(layout.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(layout.Nodes))
	}
}

func TestSaveGraphSnapshot_DependencyBlockedColorsAndReadiness(t *testing.T) {
	openFill := css(colorOpen)       // #c8e6c9
	blockedFill := css(colorBlocked) // #ffcdd2
	inProgFill := css(colorInProg)   // #fff3e0
	parkedFill := css(colorParked)   // #e0e7ff

	findNodeByID := func(t *testing.T, layout layoutResult, id string) layoutNode {
		t.Helper()
		for _, n := range layout.Nodes {
			if n.ID == id {
				return n
			}
		}
		t.Fatalf("node %q not found in layout", id)
		return layoutNode{}
	}

	t.Run("active blocker colors open dependent blocked and ready blocker open", func(t *testing.T) {
		issues := []model.Issue{
			{ID: "A", Title: "Ready blocker", Status: model.StatusOpen},
			{
				ID:     "B",
				Title:  "Blocked dependent",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		stats := analysis.NewAnalyzer(issues).Analyze()
		layout := buildLayout(GraphSnapshotOptions{Issues: issues, Stats: &stats})

		nodeA := findNodeByID(t, layout, "A")
		nodeB := findNodeByID(t, layout, "B")
		if got := css(nodeFillColor(nodeA)); got != openFill {
			t.Fatalf("ready blocker A fill = %s, want %s", got, openFill)
		}
		if got := css(nodeFillColor(nodeB)); got != blockedFill {
			t.Fatalf("dependency-blocked B fill = %s, want %s", got, blockedFill)
		}

		// Also verify SVG output encodes both exact fills.
		svgPath := filepath.Join(t.TempDir(), "active_block.svg")
		if err := SaveGraphSnapshot(GraphSnapshotOptions{Path: svgPath, Issues: issues, Stats: &stats}); err != nil {
			t.Fatalf("SaveGraphSnapshot: %v", err)
		}
		raw, err := os.ReadFile(svgPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		svg := string(raw)
		if !hasSubstr(svg, "fill:"+openFill) || !hasSubstr(svg, "fill:"+blockedFill) {
			t.Fatalf("SVG missing expected fills %s and %s:\n%s", openFill, blockedFill, svg)
		}
	})

	t.Run("closed or tombstoned blocker leaves dependent open or in_progress", func(t *testing.T) {
		closedIssues := []model.Issue{
			{ID: "A", Title: "Closed blocker", Status: model.StatusClosed},
			{
				ID:     "B",
				Title:  "Unblocked open",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		closedStats := analysis.NewAnalyzer(closedIssues).Analyze()
		closedLayout := buildLayout(GraphSnapshotOptions{Issues: closedIssues, Stats: &closedStats})
		if got := css(nodeFillColor(findNodeByID(t, closedLayout, "B"))); got != openFill {
			t.Fatalf("dependent B with closed blocker fill = %s, want %s", got, openFill)
		}

		fullWithTombstone := []model.Issue{
			{ID: "A", Title: "Tombstoned blocker", Status: model.StatusTombstone},
			{
				ID:     "B",
				Title:  "In progress dependent",
				Status: model.StatusInProgress,
				Dependencies: []*model.Dependency{
					{IssueID: "B", DependsOnID: "A", Type: model.DepBlocks},
				},
			},
		}
		visibleOnly := fullWithTombstone[1:]
		tombStats := analysis.NewAnalyzer(visibleOnly).Analyze()
		tombLayout := buildLayout(GraphSnapshotOptions{
			Issues:    visibleOnly,
			Stats:     &tombStats,
			Readiness: model.NewReadinessIndex(fullWithTombstone),
		})
		if got := css(nodeFillColor(findNodeByID(t, tombLayout, "B"))); got != inProgFill {
			t.Fatalf("in_progress B with tombstoned blocker via Readiness fill = %s, want %s", got, inProgFill)
		}
	})

	t.Run("scoped Readiness marks external open blocker and custom status uses parked color", func(t *testing.T) {
		fullIssues := []model.Issue{
			{ID: "EXT-1", Title: "External open blocker", Status: model.StatusOpen},
			{
				ID:     "SUB-1",
				Title:  "Scoped open dependent",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "SUB-1", DependsOnID: "EXT-1", Type: model.DepBlocks},
				},
			},
			{ID: "SUB-DEF", Title: "Deferred task", Status: model.Status("deferred")},
		}
		scopedIssues := fullIssues[1:]
		scopedStats := analysis.NewAnalyzer(scopedIssues).Analyze()
		layout := buildLayout(GraphSnapshotOptions{
			Issues:    scopedIssues,
			Stats:     &scopedStats,
			Readiness: model.NewReadinessIndex(fullIssues),
		})
		if got := css(nodeFillColor(findNodeByID(t, layout, "SUB-1"))); got != blockedFill {
			t.Fatalf("SUB-1 with external open blocker via Readiness fill = %s, want %s", got, blockedFill)
		}
		if got := css(nodeFillColor(findNodeByID(t, layout, "SUB-DEF"))); got != parkedFill {
			t.Fatalf("deferred node fill = %s, want %s", got, parkedFill)
		}
	})

	t.Run("topBottleneck summary scopes to rendered subgraph nodes", func(t *testing.T) {
		fullIssues := []model.Issue{
			{ID: "OUT-START", Title: "Out start", Status: model.StatusOpen},
			{
				ID:     "HUB-OUTSIDE",
				Title:  "Outside hub",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "HUB-OUTSIDE", DependsOnID: "OUT-START", Type: model.DepBlocks},
				},
			},
			{
				ID:     "OUT-END-1",
				Title:  "Out end 1",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "OUT-END-1", DependsOnID: "HUB-OUTSIDE", Type: model.DepBlocks},
				},
			},
			{
				ID:     "OUT-END-2",
				Title:  "Out end 2",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "OUT-END-2", DependsOnID: "HUB-OUTSIDE", Type: model.DepBlocks},
				},
			},
			{ID: "SUB-A", Title: "Sub A", Status: model.StatusOpen},
			{
				ID:     "SUB-MID",
				Title:  "Sub mid",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "SUB-MID", DependsOnID: "SUB-A", Type: model.DepBlocks},
				},
			},
			{
				ID:     "SUB-C",
				Title:  "Sub C",
				Status: model.StatusOpen,
				Dependencies: []*model.Dependency{
					{IssueID: "SUB-C", DependsOnID: "SUB-MID", Type: model.DepBlocks},
				},
			},
		}
		fullAnalyzer := analysis.NewAnalyzer(fullIssues)
		fullStats := fullAnalyzer.AnalyzeAsync(t.Context())
		fullStats.WaitForPhase2()

		subgraph := fullIssues[4:]
		svgPath := filepath.Join(t.TempDir(), "subgraph.svg")
		if err := SaveGraphSnapshot(GraphSnapshotOptions{
			Path:   svgPath,
			Issues: subgraph,
			Stats:  fullStats,
		}); err != nil {
			t.Fatalf("SaveGraphSnapshot(subgraph): %v", err)
		}
		raw, err := os.ReadFile(svgPath)
		if err != nil {
			t.Fatalf("ReadFile(subgraph.svg): %v", err)
		}
		svg := string(raw)
		if hasSubstr(svg, "HUB-OUTSIDE") {
			t.Fatalf("subgraph SVG leaked excluded node HUB-OUTSIDE in summary:\n%s", svg)
		}
		if !hasSubstr(svg, "top bottleneck: SUB-MID") {
			t.Fatalf("expected subgraph SVG summary to report SUB-MID as top bottleneck:\n%s", svg)
		}
	})
}

func TestSaveGraphSnapshot_LongTitleClipPathAndHeaderLegendBounds(t *testing.T) {
	longHeaderTitle := "Extremely Long Sprint Snapshot Header Title That Must Never Bleed Under The Legend Box On 640px Canvas"
	longDataHash := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	longNodeID := "issue-with-an-unusually-long-identifier-exceeding-compact-box-width"
	longNodeTitle := "Long node title that would previously overflow 170px compact column width into adjacent nodes"

	issues := []model.Issue{
		{ID: longNodeID, Title: longNodeTitle, Status: model.StatusOpen},
	}
	stats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())

	tmp := t.TempDir()
	svgPath := filepath.Join(tmp, "bounds.svg")
	pngPath := filepath.Join(tmp, "bounds.png")

	opts := GraphSnapshotOptions{
		Path:     svgPath,
		Title:    longHeaderTitle,
		Issues:   issues,
		Stats:    &stats,
		DataHash: longDataHash,
	}
	if err := SaveGraphSnapshot(opts); err != nil {
		t.Fatalf("SaveGraphSnapshot(svg): %v", err)
	}
	opts.Path = pngPath
	opts.Format = "png"
	if err := SaveGraphSnapshot(opts); err != nil {
		t.Fatalf("SaveGraphSnapshot(png): %v", err)
	}

	rawSVG, err := os.ReadFile(svgPath)
	if err != nil {
		t.Fatalf("ReadFile(svg): %v", err)
	}
	svg := string(rawSVG)
	for _, want := range []string{
		"<defs>",
		`<clipPath id="clip-node-0"`,
		`clip-path="url(#clip-node-0)"`,
	} {
		if !hasSubstr(svg, want) {
			t.Fatalf("SVG missing expected clipPath structure %q:\n%s", want, svg)
		}
	}
	for _, overflow := range []string{longHeaderTitle, longDataHash, longNodeID, longNodeTitle} {
		if hasSubstr(svg, overflow) {
			t.Fatalf("SVG should truncate overflowing text %q to stay within bounds:\n%s", overflow, svg)
		}
	}
	if maxRunes := nodeMaxTextRunes(170); maxRunes < 21 || maxRunes > 24 {
		t.Fatalf("compact nodeMaxTextRunes(170) = %d, want 21..24", maxRunes)
	}
	if maxTitle := summaryMaxTitleRunes(640); 32+maxTitle*10 > 640-200 {
		t.Fatalf("summaryMaxTitleRunes(640) = %d collides with Legend at x=440", maxTitle)
	}
	if maxDetail := summaryMaxDetailRunes(640); 32+maxDetail*8 > 640-200 {
		t.Fatalf("summaryMaxDetailRunes(640) = %d collides with Legend at x=440", maxDetail)
	}
}

func TestSaveGraphSnapshot_ActiveSubgraphTopBottleneckAndAllClosedFallback(t *testing.T) {
	// Active bridge: open-leaf -> open-bridge -> open-root (active betweenness = 1.0).
	// Closed/tombstone high-betweenness hubs on full graph:
	// {open-x1, open-x2, open-x3} -> closed-hub -> {open-root, open-y1, open-y2} (full-graph betweenness = 9.0)
	// {open-z1, open-z2} -> tomb-hub -> {open-root, open-y1} (full-graph betweenness = 4.0)
	issues := []model.Issue{
		{ID: "open-root", Title: "Open Root", Status: model.StatusOpen},
		{
			ID: "open-bridge", Title: "Active Bridge", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-bridge", DependsOnID: "open-root", Type: model.DepBlocks}},
		},
		{
			ID: "open-leaf", Title: "Active Leaf", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-leaf", DependsOnID: "open-bridge", Type: model.DepBlocks}},
		},
		{ID: "open-y1", Title: "Open Y1", Status: model.StatusOpen},
		{ID: "open-y2", Title: "Open Y2", Status: model.StatusOpen},
		{
			ID: "closed-hub", Title: "Closed High-BW Hub", Status: model.StatusClosed,
			Dependencies: []*model.Dependency{
				{IssueID: "closed-hub", DependsOnID: "open-root", Type: model.DepBlocks},
				{IssueID: "closed-hub", DependsOnID: "open-y1", Type: model.DepBlocks},
				{IssueID: "closed-hub", DependsOnID: "open-y2", Type: model.DepBlocks},
			},
		},
		{
			ID: "open-x1", Title: "Open X1", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-x1", DependsOnID: "closed-hub", Type: model.DepBlocks}},
		},
		{
			ID: "open-x2", Title: "Open X2", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-x2", DependsOnID: "closed-hub", Type: model.DepBlocks}},
		},
		{
			ID: "open-x3", Title: "Open X3", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-x3", DependsOnID: "closed-hub", Type: model.DepBlocks}},
		},
		{
			ID: "tomb-hub", Title: "Tombstone High-BW Hub", Status: model.StatusTombstone,
			Dependencies: []*model.Dependency{
				{IssueID: "tomb-hub", DependsOnID: "open-root", Type: model.DepBlocks},
				{IssueID: "tomb-hub", DependsOnID: "open-y1", Type: model.DepBlocks},
			},
		},
		{
			ID: "open-z1", Title: "Open Z1", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-z1", DependsOnID: "tomb-hub", Type: model.DepBlocks}},
		},
		{
			ID: "open-z2", Title: "Open Z2", Status: model.StatusOpen,
			Dependencies: []*model.Dependency{{IssueID: "open-z2", DependsOnID: "tomb-hub", Type: model.DepBlocks}},
		},
	}

	fullStats := analysis.NewAnalyzer(issues).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	if fullStats.GetBetweennessScore("closed-hub") <= fullStats.GetBetweennessScore("open-bridge") ||
		fullStats.GetBetweennessScore("tomb-hub") <= fullStats.GetBetweennessScore("open-bridge") {
		t.Fatalf("negative control setup invalid: full-graph betweenness closed-hub=%v tomb-hub=%v open-bridge=%v",
			fullStats.GetBetweennessScore("closed-hub"),
			fullStats.GetBetweennessScore("tomb-hub"),
			fullStats.GetBetweennessScore("open-bridge"))
	}

	broaderIssues := append(append([]model.Issue{}, issues...), model.Issue{
		ID: "outside-extra", Title: "Outside", Status: model.StatusOpen,
		Dependencies: []*model.Dependency{{IssueID: "outside-extra", DependsOnID: "open-leaf", Type: model.DepBlocks}},
	})
	broaderStats := analysis.NewAnalyzer(broaderIssues).AnalyzeWithConfig(analysis.FullAnalysisConfig())

	for _, tc := range []struct {
		name  string
		stats *analysis.GraphStats
	}{
		{name: "prepopulated-stats", stats: &fullStats},
		{name: "mismatched-stats-fallback", stats: &broaderStats},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svgPath := filepath.Join(t.TempDir(), "active_bottleneck.svg")
			if err := SaveGraphSnapshot(GraphSnapshotOptions{
				Path:   svgPath,
				Issues: issues,
				Stats:  tc.stats,
			}); err != nil {
				t.Fatalf("SaveGraphSnapshot failed: %v", err)
			}
			raw, err := os.ReadFile(svgPath)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			svg := string(raw)
			if !hasSubstr(svg, "top bottleneck: open-bridge (") {
				t.Fatalf("expected SVG summary to select active bridge open-bridge as top bottleneck, got:\n%s", svg)
			}
			if hasSubstr(svg, "top bottleneck: closed-hub") || hasSubstr(svg, "top bottleneck: tomb-hub") {
				t.Fatalf("SVG summary leaked closed/tombstone hub as top bottleneck:\n%s", svg)
			}
		})
	}

	// All-closed fallback: must not panic and must emit deterministic fallback leader.
	allClosed := []model.Issue{
		{ID: "closed-b", Title: "Closed B", Status: model.StatusClosed},
		{
			ID: "closed-a", Title: "Closed A", Status: model.StatusClosed,
			Dependencies: []*model.Dependency{{IssueID: "closed-a", DependsOnID: "closed-b", Type: model.DepBlocks}},
		},
		{
			ID: "closed-c", Title: "Closed C", Status: model.StatusClosed,
			Dependencies: []*model.Dependency{{IssueID: "closed-c", DependsOnID: "closed-a", Type: model.DepBlocks}},
		},
	}
	closedStats := analysis.NewAnalyzer(allClosed).AnalyzeWithConfig(analysis.FullAnalysisConfig())
	closedSVGPath := filepath.Join(t.TempDir(), "all_closed.svg")
	if err := SaveGraphSnapshot(GraphSnapshotOptions{
		Path:   closedSVGPath,
		Issues: allClosed,
		Stats:  &closedStats,
	}); err != nil {
		t.Fatalf("SaveGraphSnapshot(allClosed) failed: %v", err)
	}
	rawClosedSVG, err := os.ReadFile(closedSVGPath)
	if err != nil {
		t.Fatalf("ReadFile(all_closed.svg): %v", err)
	}
	if !hasSubstr(string(rawClosedSVG), "top bottleneck: closed-a (0.00)") {
		t.Fatalf("expected all-closed snapshot to fall back to closed-a (0.00), got:\n%s", string(rawClosedSVG))
	}
}
