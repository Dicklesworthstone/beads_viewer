package export

import (
	"fmt"
	"image/color"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/beads_viewer/pkg/analysis"
	"github.com/Dicklesworthstone/beads_viewer/pkg/model"

	"git.sr.ht/~sbinet/gg"
	"github.com/ajstarks/svgo"
	"golang.org/x/image/font/basicfont"
)

// GraphSnapshotOptions controls graph snapshot export behaviour.
type GraphSnapshotOptions struct {
	Path     string               // Output path; format inferred from extension when Format empty
	Format   string               // "svg" or "png" (case-insensitive). If empty, inferred from Path.
	Title    string               // Optional title rendered in summary block
	Preset   string               // Layout preset: "compact" (default) or "roomy"
	Issues   []model.Issue        // Issues to render (already filtered by recipe/workspace)
	Stats    *analysis.GraphStats // Graph analysis used for layout/summary
	DataHash string               // Hash of input issues for provenance
}

// SaveGraphSnapshot renders a static graph snapshot (SVG or PNG) with a minimal
// summary block. It intentionally keeps the visual language concise so AI agents
// can parse it without reading auxiliary docs.
func SaveGraphSnapshot(opts GraphSnapshotOptions) error {
	if len(opts.Issues) == 0 {
		return fmt.Errorf("no issues to export")
	}
	if opts.Stats == nil {
		return fmt.Errorf("graph stats are required for snapshot export")
	}

	format := strings.ToLower(strings.TrimPrefix(opts.Format, "."))
	if format == "" {
		switch strings.ToLower(filepath.Ext(opts.Path)) {
		case ".svg":
			format = "svg"
		case ".png":
			format = "png"
		default:
			format = "svg" // safe default
			if opts.Path != "" && filepath.Ext(opts.Path) == "" {
				opts.Path = opts.Path + ".svg"
			}
		}
	}
	if format != "svg" && format != "png" {
		return fmt.Errorf("unsupported format %q (want svg or png)", format)
	}
	if opts.Path == "" {
		return fmt.Errorf("output path is required")
	}
	if err := validateExistingPathNotSymlinkOrDir(opts.Path); err != nil {
		return err
	}

	layout := buildLayout(opts)

	switch format {
	case "svg":
		return renderSVG(opts, layout)
	case "png":
		return renderPNG(opts, layout)
	default:
		return fmt.Errorf("unhandled format %q", format)
	}
}

// --- layout computation ----------------------------------------------------

type layoutNode struct {
	ID       string
	Title    string
	Status   model.Status
	Level    int
	Rank     float64 // pagerank for ordering
	X, Y     float64
	NodeW    float64
	NodeH    float64
	PageRank float64
}

type layoutEdge struct {
	From string
	To   string
}

type layoutResult struct {
	Nodes   []layoutNode
	Edges   []layoutEdge
	Width   int
	Height  int
	Header  float64
	Summary summaryInfo
}

type summaryInfo struct {
	Title         string
	DataHash      string
	NodeCount     int
	EdgeCount     int
	TopBottleneck string
}

func buildLayout(opts GraphSnapshotOptions) layoutResult {
	const (
		nodeWCompact  = 170.0
		nodeHCompact  = 70.0
		nodeWRoomy    = 190.0
		nodeHRoomy    = 82.0
		colGapCompact = 80.0
		rowGapCompact = 40.0
		colGapRoomy   = 110.0
		rowGapRoomy   = 55.0
		padding       = 36.0
		headerHeight  = 120.0
	)

	roomy := strings.EqualFold(opts.Preset, "roomy")
	nodeW := nodeWCompact
	nodeH := nodeHCompact
	colGap := colGapCompact
	rowGap := rowGapCompact
	if roomy {
		nodeW = nodeWRoomy
		nodeH = nodeHRoomy
		colGap = colGapRoomy
		rowGap = rowGapRoomy
	}

	// Pre-compute helper maps
	pageRank := opts.Stats.PageRank()
	critical := opts.Stats.CriticalPathScore()

	// determine levels using critical path score (fallback 1)
	levelByID := make(map[string]int, len(opts.Issues))
	maxLevel := 1
	for _, iss := range opts.Issues {
		lvl := int(math.Round(critical[iss.ID]))
		if lvl < 1 {
			lvl = 1
		}
		levelByID[iss.ID] = lvl
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	// group nodes by level for row placement
	levelBuckets := make(map[int][]layoutNode, maxLevel)
	for _, iss := range opts.Issues {
		level := levelByID[iss.ID]
		n := layoutNode{
			ID:       iss.ID,
			Title:    truncate(iss.Title, 44),
			Status:   iss.Status,
			Level:    level,
			Rank:     pageRank[iss.ID],
			NodeW:    nodeW,
			NodeH:    nodeH,
			PageRank: pageRank[iss.ID],
		}
		levelBuckets[level] = append(levelBuckets[level], n)
	}

	// sort each level by rank then ID for deterministic layout
	for lvl := 1; lvl <= maxLevel; lvl++ {
		nodes := levelBuckets[lvl]
		sort.Slice(nodes, func(i, j int) bool {
			// Use epsilon comparisons to avoid unstable ordering when PageRank is
			// effectively tied but differs by tiny floating point noise.
			const eps = 1e-6
			if diff := nodes[i].Rank - nodes[j].Rank; math.Abs(diff) > eps {
				return diff > 0
			}
			return nodes[i].ID < nodes[j].ID
		})
		levelBuckets[lvl] = nodes
	}

	// assign coordinates
	var nodes []layoutNode
	maxRows := 0
	for lvl := 1; lvl <= maxLevel; lvl++ {
		bucket := levelBuckets[lvl]
		if len(bucket) > maxRows {
			maxRows = len(bucket)
		}
		for idx := range bucket {
			bucket[idx].X = padding + float64(lvl-1)*(nodeW+colGap)
			bucket[idx].Y = padding + headerHeight + float64(idx)*(nodeH+rowGap)
			nodes = append(nodes, bucket[idx])
		}
	}

	width := int(padding*2 + float64(maxLevel)*(nodeW+colGap) + nodeW)
	if width < 640 {
		width = 640
	}
	height := int(padding*2 + headerHeight + float64(maxRows)*(nodeH+rowGap) + nodeH)
	if height < 480 {
		height = 480
	}

	// edges (blocking deps only)
	nodeIDs := make(map[string]bool, len(opts.Issues))
	for _, n := range nodes {
		nodeIDs[n.ID] = true
	}
	var edges []layoutEdge
	for _, iss := range opts.Issues {
		for _, dep := range iss.Dependencies {
			if dep == nil || !dep.Type.IsBlocking() {
				continue
			}
			if !nodeIDs[dep.DependsOnID] {
				continue // filtered out by recipe/workspace
			}
			edges = append(edges, layoutEdge{From: iss.ID, To: dep.DependsOnID})
		}
	}

	// summary
	// Collect all node IDs for fallback when betweenness is nil/empty
	allNodeIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		allNodeIDs = append(allNodeIDs, n.ID)
	}
	topBottleneck := topByMetricWithFallback(opts.Stats.Betweenness(), allNodeIDs)
	title := opts.Title
	if strings.TrimSpace(title) == "" {
		title = "Graph Snapshot"
	}

	return layoutResult{
		Nodes:  nodes,
		Edges:  edges,
		Width:  width,
		Height: height,
		Header: headerHeight,
		Summary: summaryInfo{
			Title:         title,
			DataHash:      opts.DataHash,
			NodeCount:     len(nodes),
			EdgeCount:     len(edges),
			TopBottleneck: topBottleneck,
		},
	}
}

func topByMetric(m map[string]float64) string {
	var bestID string
	var bestVal float64
	hasBest := false
	for id, v := range m {
		if !hasBest || v > bestVal || (v == bestVal && id < bestID) {
			bestID = id
			bestVal = v
			hasBest = true
		}
	}
	if !hasBest {
		return "n/a"
	}
	return fmt.Sprintf("%s (%.2f)", bestID, bestVal)
}

// topByMetricWithFallback returns the top entry from the metric map, or if the
// map is nil/empty, falls back to the alphabetically first node from fallbackIDs
// with a zero score. This ensures we show a "zero-score leader" instead of "n/a"
// when all nodes have zero betweenness (e.g., star topology graphs).
func topByMetricWithFallback(m map[string]float64, fallbackIDs []string) string {
	result := topByMetric(m)
	if result != "n/a" {
		return result
	}

	// Metric map was nil or empty; use fallback to show zero-score leader
	if len(fallbackIDs) == 0 {
		return "n/a"
	}

	// Find alphabetically first node ID
	sort.Strings(fallbackIDs)
	return fmt.Sprintf("%s (0.00)", fallbackIDs[0])
}

// --- rendering -------------------------------------------------------------

var (
	colorOpen      = color.RGBA{0xc8, 0xe6, 0xc9, 0xff}
	colorBlocked   = color.RGBA{0xff, 0xcd, 0xd2, 0xff}
	colorInProg    = color.RGBA{0xff, 0xf3, 0xe0, 0xff}
	colorClosed    = color.RGBA{0xcf, 0xd8, 0xdc, 0xff}
	colorStroke    = color.RGBA{0x22, 0x22, 0x22, 0xff}
	colorEdge      = color.RGBA{0x6b, 0x80, 0xbf, 0xff}
	colorEdgeArrow = color.RGBA{0x6b, 0x80, 0xbf, 0xff}
	colorText      = color.RGBA{0x11, 0x11, 0x11, 0xff}
	colorSubtle    = color.RGBA{0x66, 0x66, 0x66, 0xff}
	colorBackdrop  = color.RGBA{0xf9, 0xfa, 0xfb, 0xff}
	colorHeaderBG  = color.RGBA{0xf3, 0xf4, 0xf6, 0xff}
	colorLegendBG  = color.RGBA{0xee, 0xee, 0xee, 0xff}
)

func statusColor(s model.Status) color.RGBA {
	switch {
	case isClosedLikeStatus(s):
		return colorClosed
	case s == model.StatusOpen:
		return colorOpen
	case s == model.StatusBlocked:
		return colorBlocked
	case s == model.StatusInProgress:
		return colorInProg
	default:
		return colorOpen
	}
}

func renderPNG(opts GraphSnapshotOptions, layout layoutResult) error {
	dc := gg.NewContext(layout.Width, layout.Height)
	dc.SetColor(colorBackdrop)
	dc.Clear()

	// header
	dc.SetColor(colorHeaderBG)
	dc.DrawRoundedRectangle(16, 16, float64(layout.Width)-32, layout.Header-24, 10)
	dc.Fill()

	dc.SetFontFace(basicfont.Face7x13)

	drawSummaryBlock(dc, layout)
	drawLegend(dc, layout)

	// edges
	nodePos := make(map[string]layoutNode, len(layout.Nodes))
	for _, n := range layout.Nodes {
		nodePos[n.ID] = n
	}
	dc.SetColor(colorEdge)
	dc.SetLineWidth(2)
	for _, e := range layout.Edges {
		from := nodePos[e.From]
		to := nodePos[e.To]
		x1 := from.X + from.NodeW
		y1 := from.Y + from.NodeH/2
		x2 := to.X
		y2 := to.Y + to.NodeH/2
		dc.DrawLine(x1, y1, x2, y2)
		dc.Stroke()
		drawArrow(dc, x2, y2, -8, 0)
	}

	// nodes
	for _, n := range layout.Nodes {
		drawNode(dc, n)
	}

	return SafeWriteOutputFunc(opts.Path, 0o644, func(w io.Writer) error {
		return dc.EncodePNG(w)
	})
}

func renderSVG(opts GraphSnapshotOptions, layout layoutResult) error {
	return SafeWriteOutputFunc(opts.Path, 0o644, func(w io.Writer) error {
		return renderSVGToWriter(w, layout)
	})
}

func renderSVGToWriter(w io.Writer, layout layoutResult) error {
	canvas := svg.New(w)
	canvas.Start(layout.Width, layout.Height)
	canvas.Rect(0, 0, layout.Width, layout.Height, fmt.Sprintf("fill:%s", css(colorBackdrop)))
	canvas.Roundrect(16, 16, layout.Width-32, int(layout.Header-24), 10, 10, fmt.Sprintf("fill:%s", css(colorHeaderBG)))

	drawSummaryBlockSVG(canvas, layout)
	drawLegendSVG(canvas, layout)

	nodePos := make(map[string]layoutNode, len(layout.Nodes))
	for _, n := range layout.Nodes {
		nodePos[n.ID] = n
	}

	for _, e := range layout.Edges {
		from := nodePos[e.From]
		to := nodePos[e.To]
		x1 := int(from.X + from.NodeW)
		y1 := int(from.Y + from.NodeH/2)
		x2 := int(to.X)
		y2 := int(to.Y + to.NodeH/2)
		canvas.Line(x1, y1, x2, y2, fmt.Sprintf("stroke:%s;stroke-width:2", css(colorEdge)))
		// simple arrow head
		canvas.Polygon(
			[]int{x2, x2 + 8, x2 + 8},
			[]int{y2, y2 + 4, y2 - 4},
			fmt.Sprintf("fill:%s", css(colorEdgeArrow)),
		)
	}

	for _, n := range layout.Nodes {
		x := int(n.X)
		y := int(n.Y)
		canvas.Roundrect(x, y, int(n.NodeW), int(n.NodeH), 8, 8,
			fmt.Sprintf("fill:%s;stroke:%s;stroke-width:1.2", css(statusColor(n.Status)), css(colorStroke)))
		canvas.Text(x+10, y+22, n.ID, fmt.Sprintf("fill:%s;font-size:13px;font-family:monospace;font-weight:bold", css(colorText)))
		canvas.Text(x+10, y+42, truncate(n.Title, 40), fmt.Sprintf("fill:%s;font-size:12px;font-family:monospace", css(colorSubtle)))
		canvas.Text(x+10, y+60, fmt.Sprintf("PR %.3f", n.PageRank),
			fmt.Sprintf("fill:%s;font-size:11px;font-family:monospace", css(colorSubtle)))
	}

	canvas.End()
	return nil
}

func drawNode(dc *gg.Context, n layoutNode) {
	dc.SetColor(statusColor(n.Status))
	dc.DrawRoundedRectangle(n.X, n.Y, n.NodeW, n.NodeH, 8)
	dc.Fill()
	dc.SetColor(colorStroke)
	dc.SetLineWidth(1.2)
	dc.DrawRoundedRectangle(n.X, n.Y, n.NodeW, n.NodeH, 8)
	dc.Stroke()

	dc.SetColor(colorText)
	dc.DrawStringAnchored(n.ID, n.X+10, n.Y+18, 0, 0.5)
	dc.SetColor(colorSubtle)
	dc.DrawStringAnchored(truncate(n.Title, 40), n.X+10, n.Y+36, 0, 0.5)
	dc.DrawStringAnchored(fmt.Sprintf("PR %.3f", n.PageRank), n.X+10, n.Y+54, 0, 0.5)
}

func drawArrow(dc *gg.Context, x, y, dx, dy float64) {
	dc.SetColor(colorEdgeArrow)
	dc.NewSubPath()
	dc.MoveTo(x, y)
	dc.LineTo(x+dx, y+dy+4)
	dc.LineTo(x+dx, y+dy-4)
	dc.ClosePath()
	dc.Fill()
}

func drawSummaryBlock(dc *gg.Context, layout layoutResult) {
	dc.SetColor(colorText)
	dc.DrawStringAnchored(layout.Summary.Title, 32, 44, 0, 0.5)
	dc.SetColor(colorSubtle)
	dc.DrawStringAnchored(fmt.Sprintf("data_hash: %s", layout.Summary.DataHash), 32, 64, 0, 0.5)
	dc.DrawStringAnchored(fmt.Sprintf("nodes: %d  edges: %d", layout.Summary.NodeCount, layout.Summary.EdgeCount), 32, 84, 0, 0.5)
	dc.DrawStringAnchored(fmt.Sprintf("top bottleneck: %s", layout.Summary.TopBottleneck), 32, 104, 0, 0.5)
}

func drawLegend(dc *gg.Context, layout layoutResult) {
	boxW := 180.0
	boxH := 96.0
	x := float64(layout.Width) - boxW - 20
	y := 24.0
	dc.SetColor(colorLegendBG)
	dc.DrawRoundedRectangle(x, y, boxW, boxH, 10)
	dc.Fill()
	dc.SetColor(colorStroke)
	dc.DrawRoundedRectangle(x, y, boxW, boxH, 10)
	dc.Stroke()

	dc.SetColor(colorText)
	dc.DrawStringAnchored("Legend", x+12, y+18, 0, 0.5)
	drawLegendRow(dc, x+12, y+36, colorOpen, "Open / Ready")
	drawLegendRow(dc, x+12, y+52, colorInProg, "In Progress")
	drawLegendRow(dc, x+12, y+68, colorBlocked, "Blocked (has blockers)")
	drawLegendRow(dc, x+12, y+84, colorClosed, "Closed")
}

func drawLegendRow(dc *gg.Context, x, y float64, c color.RGBA, label string) {
	dc.SetColor(c)
	dc.DrawRoundedRectangle(x, y-8, 14, 14, 3)
	dc.Fill()
	dc.SetColor(colorStroke)
	dc.DrawRoundedRectangle(x, y-8, 14, 14, 3)
	dc.Stroke()
	dc.SetColor(colorSubtle)
	dc.DrawStringAnchored(label, x+20, y, 0, 0.5)
}

func drawSummaryBlockSVG(canvas *svg.SVG, layout layoutResult) {
	canvas.Text(32, 44, layout.Summary.Title, fmt.Sprintf("fill:%s;font-size:16px;font-family:monospace;font-weight:bold", css(colorText)))
	canvas.Text(32, 64, fmt.Sprintf("data_hash: %s", layout.Summary.DataHash), fmt.Sprintf("fill:%s;font-size:13px;font-family:monospace", css(colorSubtle)))
	canvas.Text(32, 84, fmt.Sprintf("nodes: %d  edges: %d", layout.Summary.NodeCount, layout.Summary.EdgeCount), fmt.Sprintf("fill:%s;font-size:13px;font-family:monospace", css(colorSubtle)))
	canvas.Text(32, 104, fmt.Sprintf("top bottleneck: %s", layout.Summary.TopBottleneck), fmt.Sprintf("fill:%s;font-size:13px;font-family:monospace", css(colorSubtle)))
}

func drawLegendSVG(canvas *svg.SVG, layout layoutResult) {
	boxW := 180
	boxH := 96
	x := layout.Width - boxW - 20
	y := 24
	canvas.Roundrect(x, y, boxW, boxH, 10, 10, fmt.Sprintf("fill:%s;stroke:%s;stroke-width:1", css(colorLegendBG), css(colorStroke)))
	canvas.Text(x+12, y+18, "Legend", fmt.Sprintf("fill:%s;font-size:13px;font-family:monospace;font-weight:bold", css(colorText)))
	drawLegendRowSVG(canvas, x+12, y+36, colorOpen, "Open / Ready")
	drawLegendRowSVG(canvas, x+12, y+52, colorInProg, "In Progress")
	drawLegendRowSVG(canvas, x+12, y+68, colorBlocked, "Blocked")
	drawLegendRowSVG(canvas, x+12, y+84, colorClosed, "Closed")
}

func drawLegendRowSVG(canvas *svg.SVG, x, y int, c color.RGBA, label string) {
	canvas.Roundrect(x, y-8, 14, 14, 3, 3, fmt.Sprintf("fill:%s;stroke:%s;stroke-width:1", css(c), css(colorStroke)))
	canvas.Text(x+20, y, label, fmt.Sprintf("fill:%s;font-size:12px;font-family:monospace", css(colorSubtle)))
}

// --- helpers ---------------------------------------------------------------

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func css(c color.RGBA) string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}
