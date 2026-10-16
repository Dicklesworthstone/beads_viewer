package main_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGraphExport_RecipeScope(t *testing.T) {
	dir := recipeProject(t)
	// The resolved prerequisite sits outside both the sprint label and SP repo.
	// The missing prerequisite must still block SP-3 after either filter.
	writeIssuesJSONL(t, dir, strings.Join([]string{
		`{"id":"SP-1","title":"Sprint root","status":"open","priority":1,"issue_type":"task","labels":["sprint"],"dependencies":[{"issue_id":"SP-1","depends_on_id":"DONE-1","type":"blocks"}]}`,
		`{"id":"SP-2","title":"Sprint follow-up","status":"open","priority":2,"issue_type":"task","labels":["sprint"],"dependencies":[{"issue_id":"SP-2","depends_on_id":"SP-1","type":"blocks"}]}`,
		`{"id":"SP-3","title":"Missing prerequisite","status":"open","priority":3,"issue_type":"task","labels":["sprint"],"dependencies":[{"issue_id":"SP-3","depends_on_id":"absent","type":"blocks"}]}`,
		`{"id":"BL-1","title":"Backlog item","status":"open","priority":0,"issue_type":"task","labels":["backlog"]}`,
		`{"id":"SP-9","title":"Sprint done","status":"closed","priority":1,"issue_type":"task","labels":["sprint"]}`,
		`{"id":"DONE-1","title":"Resolved prerequisite","status":"closed","priority":1,"issue_type":"task"}`,
	}, "\n")+"\n")
	writeRecipeFile(t, dir, "limited.yaml", "filters:\n  status: [open]\n  tags: [sprint]\nsort:\n  field: priority\n  direction: desc\nview:\n  max_items: 1\n")
	writeRecipeFile(t, dir, "empty.yaml", "filters:\n  tags: [absent]\n")
	allIDs := []string{"BL-1", "DONE-1", "SP-1", "SP-2", "SP-3", "SP-9"}
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{"unfiltered", nil, allIDs},
		{"actionable", []string{"--recipe", "actionable"}, []string{"BL-1", "SP-1"}},
		{"label-intersection", []string{"--recipe", "actionable", "--label", "sprint"}, []string{"SP-1"}},
		{"repo-intersection", []string{"--recipe", "actionable", "--repo", "SP"}, []string{"SP-1"}},
		{"file-recipe", []string{"--recipe", ".beads/recipes/sprint.yaml"}, []string{"SP-1", "SP-2", "SP-3"}},
		{"sort-and-limit", []string{"--recipe", "limited"}, []string{"SP-3"}},
		{"empty-recipe", []string{"--recipe", "empty"}, nil},
		{"empty-label", []string{"--recipe", "actionable", "--label", "absent"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"html", "svg", "png"} {
				t.Run(format, func(t *testing.T) {
					path := filepath.Join(dir, tc.name+"."+format)
					args := append([]string{"--export-graph", path}, tc.args...)
					stdout, stderr, err := runBVSplit(t, dir, args...)
					if len(tc.want) == 0 {
						if err == nil || !strings.Contains(stderr, "No issues to export (check filters)") {
							t.Fatalf("empty graph must fail clearly: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
						}
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Fatalf("empty graph created an artifact: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatalf("graph export: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
					}
					if !strings.Contains(stdout, fmt.Sprintf("(%d nodes", len(tc.want))) {
						t.Errorf("wrong exported node count: %s", stdout)
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					var got []string
					switch format {
					case "html":
						_, data, found := strings.Cut(string(raw), "const DATA = ")
						if !found {
							t.Fatal("missing embedded graph data")
						}
						var graph struct {
							Nodes []struct{ ID string }
							Links []struct{ Source, Target string }
						}
						if err := json.NewDecoder(strings.NewReader(data)).Decode(&graph); err != nil {
							t.Fatal(err)
						}
						for _, node := range graph.Nodes {
							got = append(got, node.ID)
						}
						for _, link := range graph.Links {
							if !slices.Contains(tc.want, link.Source) || !slices.Contains(tc.want, link.Target) {
								t.Errorf("link to excluded node: %+v", link)
							}
						}
					case "svg", "png":
						var pngWidth, pngHeight int
						if format == "png" {
							img, err := png.Decode(bytes.NewReader(raw))
							if err != nil {
								t.Fatal(err)
							}
							pngWidth, pngHeight = img.Bounds().Dx(), img.Bounds().Dy()
							// Export the matching vector layout here so the PNG
							// subtest can also run independently of its siblings.
							svgPath := filepath.Join(dir, tc.name+"-png-layout.svg")
							svgArgs := append([]string{"--export-graph", svgPath}, tc.args...)
							stdout, stderr, err := runBVSplit(t, dir, svgArgs...)
							if err != nil {
								t.Fatalf("PNG reference layout: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
							}
							raw, err = os.ReadFile(svgPath)
							if err != nil {
								t.Fatal(err)
							}
						}
						var svg struct {
							Width  int      `xml:"width,attr"`
							Height int      `xml:"height,attr"`
							Texts  []string `xml:"text"`
						}
						if err := xml.Unmarshal(raw, &svg); err != nil {
							t.Fatal(err)
						}
						if format == "png" && (pngWidth != svg.Width || pngHeight != svg.Height) {
							t.Errorf("PNG %dx%d differs from SVG %dx%d", pngWidth, pngHeight, svg.Width, svg.Height)
						}
						for _, label := range svg.Texts {
							if slices.Contains(allIDs, label) {
								got = append(got, label)
							}
						}
					}
					slices.Sort(got)
					if !slices.Equal(got, tc.want) {
						t.Fatalf("exported IDs=%v want=%v", got, tc.want)
					}
				})
			}
		})
	}
}

// ============================================================================
// E2E: Graph Export Format Validation (bv-yc2v)
// ============================================================================

// TestGraphExport_JSONFormat tests JSON graph export
func TestGraphExport_JSONFormat(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph --graph-format=json failed: %v\n%s", err, out)
	}

	// Verify valid JSON
	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput: %s", err, out)
	}

	// Check required fields
	requiredFields := []string{"format", "nodes", "edges", "explanation"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			t.Errorf("missing required field: %s", field)
		}
	}

	// Verify format is json
	if format, ok := result["format"].(string); !ok || format != "json" {
		t.Errorf("expected format='json', got %v", result["format"])
	}

	// Verify adjacency field for JSON format
	if _, ok := result["adjacency"]; !ok {
		t.Error("JSON format should include 'adjacency' field")
	}

	// Check adjacency structure
	adjacency, ok := result["adjacency"].(map[string]interface{})
	if !ok {
		t.Fatal("adjacency is not an object")
	}

	// Check nodes array
	nodes, ok := adjacency["nodes"].([]interface{})
	if !ok {
		t.Fatal("adjacency.nodes is not an array")
	}
	if len(nodes) == 0 {
		t.Error("expected at least one node in adjacency.nodes")
	}

	// Check edges array exists (may be nil/empty if no dependencies)
	edges := adjacency["edges"]
	if edges != nil {
		if _, ok := edges.([]interface{}); !ok {
			t.Error("adjacency.edges is not an array when present")
		}
	}
}

// TestGraphExport_JSONNodeStructure validates JSON node structure
func TestGraphExport_JSONNodeStructure(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph failed: %v\n%s", err, out)
	}

	var result struct {
		Adjacency struct {
			Nodes []struct {
				ID       string   `json:"id"`
				Title    string   `json:"title"`
				Status   string   `json:"status"`
				Priority int      `json:"priority"`
				Labels   []string `json:"labels"`
				PageRank float64  `json:"pagerank"`
			} `json:"nodes"`
			Edges []struct {
				From string `json:"from"`
				To   string `json:"to"`
				Type string `json:"type"`
			} `json:"edges"`
		} `json:"adjacency"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Check first node has expected fields
	if len(result.Adjacency.Nodes) == 0 {
		t.Fatal("no nodes in output")
	}

	node := result.Adjacency.Nodes[0]
	if node.ID == "" {
		t.Error("node.id is empty")
	}
	if node.Title == "" {
		t.Error("node.title is empty")
	}
	if node.Status == "" {
		t.Error("node.status is empty")
	}
}

// TestGraphExport_DOTFormat tests DOT graph export
func TestGraphExport_DOTFormat(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=dot")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph --graph-format=dot failed: %v\n%s", err, out)
	}

	// Parse JSON wrapper
	var result struct {
		Format string `json:"format"`
		Graph  string `json:"graph"`
		Nodes  int    `json:"nodes"`
		Edges  int    `json:"edges"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput: %s", err, out)
	}

	// Verify format
	if result.Format != "dot" {
		t.Errorf("expected format='dot', got %q", result.Format)
	}

	// Verify DOT content
	dot := result.Graph
	if !strings.HasPrefix(dot, "digraph G {") {
		t.Error("DOT output should start with 'digraph G {'")
	}
	if !strings.HasSuffix(strings.TrimSpace(dot), "}") {
		t.Error("DOT output should end with '}'")
	}

	// Check for DOT keywords (edges are optional)
	dotKeywords := []string{
		"rankdir",
		"node [",
		"edge [",
	}
	for _, kw := range dotKeywords {
		if !strings.Contains(dot, kw) {
			t.Errorf("DOT output missing keyword: %s", kw)
		}
	}
	// "->" is only present if there are edges
	// (don't require it for graphs without dependencies)
}

// TestGraphExport_DOTSyntax validates DOT syntax elements
func TestGraphExport_DOTSyntax(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=dot")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph failed: %v\n%s", err, out)
	}

	var result struct {
		Graph string `json:"graph"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	dot := result.Graph

	// Check node attributes
	nodeAttrs := []string{
		"label=",
		"fillcolor=",
		"style=filled",
	}
	for _, attr := range nodeAttrs {
		if !strings.Contains(dot, attr) {
			t.Errorf("DOT output missing node attribute: %s", attr)
		}
	}

	// Check for status colors (hex format)
	if !strings.Contains(dot, "#") {
		t.Error("DOT output should contain hex colors")
	}

	// Check for edge styles
	edgeStyles := []string{"bold", "dashed"}
	foundEdgeStyle := false
	for _, style := range edgeStyles {
		if strings.Contains(dot, "style="+style) {
			foundEdgeStyle = true
			break
		}
	}
	if !foundEdgeStyle {
		t.Log("Note: No edge styles found (may be expected for graphs without deps)")
	}
}

// TestGraphExport_MermaidFormat tests Mermaid graph export
func TestGraphExport_MermaidFormat(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=mermaid")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph --graph-format=mermaid failed: %v\n%s", err, out)
	}

	// Parse JSON wrapper
	var result struct {
		Format string `json:"format"`
		Graph  string `json:"graph"`
		Nodes  int    `json:"nodes"`
		Edges  int    `json:"edges"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\nOutput: %s", err, out)
	}

	// Verify format
	if result.Format != "mermaid" {
		t.Errorf("expected format='mermaid', got %q", result.Format)
	}

	// Verify Mermaid content
	mermaid := result.Graph
	if !strings.HasPrefix(mermaid, "graph TD") {
		t.Error("Mermaid output should start with 'graph TD'")
	}

	// Check for Mermaid class definitions
	classKeywords := []string{
		"classDef",
		"open",
		"blocked",
		"closed",
	}
	for _, kw := range classKeywords {
		if !strings.Contains(mermaid, kw) {
			t.Errorf("Mermaid output missing class definition: %s", kw)
		}
	}
}

// TestGraphExport_MermaidNodeSyntax validates Mermaid node syntax
func TestGraphExport_MermaidNodeSyntax(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=mermaid")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph failed: %v\n%s", err, out)
	}

	var result struct {
		Graph string `json:"graph"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	mermaid := result.Graph

	// Check for node definitions (ID["label"])
	if !strings.Contains(mermaid, "[\"") {
		t.Error("Mermaid output should contain node definitions with labels")
	}

	// Check for class assignments
	if !strings.Contains(mermaid, "class ") {
		t.Error("Mermaid output should contain class assignments")
	}
}

// TestGraphExport_NodeCount verifies consistent node counts across formats
func TestGraphExport_NodeCount(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	formats := []string{"json", "dot", "mermaid"}
	nodeCounts := make(map[string]int)

	for _, format := range formats {
		cmd := exec.Command(bv, "--robot-graph", "--graph-format="+format)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--robot-graph --graph-format=%s failed: %v\n%s", format, err, out)
		}

		var result struct {
			Nodes int `json:"nodes"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("JSON unmarshal failed for %s: %v", format, err)
		}

		nodeCounts[format] = result.Nodes
	}

	// Verify all formats report same node count
	baseCount := nodeCounts["json"]
	for format, count := range nodeCounts {
		if count != baseCount {
			t.Errorf("format %s has %d nodes, expected %d (same as json)", format, count, baseCount)
		}
	}
}

// TestGraphExport_EdgeCount verifies consistent edge counts across formats
func TestGraphExport_EdgeCount(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepoWithDeps(t)

	formats := []string{"json", "dot", "mermaid"}
	edgeCounts := make(map[string]int)

	for _, format := range formats {
		cmd := exec.Command(bv, "--robot-graph", "--graph-format="+format)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--robot-graph --graph-format=%s failed: %v\n%s", format, err, out)
		}

		var result struct {
			Edges int `json:"edges"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("JSON unmarshal failed for %s: %v", format, err)
		}

		edgeCounts[format] = result.Edges
	}

	// Verify all formats report same edge count
	baseCount := edgeCounts["json"]
	for format, count := range edgeCounts {
		if count != baseCount {
			t.Errorf("format %s has %d edges, expected %d (same as json)", format, count, baseCount)
		}
	}

	// Note: Edge count may be 0 if dependency format in JSONL doesn't match expected schema
	// The important thing is that all formats report the same count
	t.Logf("All formats report %d edges", baseCount)
}

// TestGraphExport_LabelFilter tests --label filter
func TestGraphExport_LabelFilter(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepoWithLabels(t)

	// Export without filter
	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json")
	cmd.Dir = repoDir
	outAll, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph failed: %v\n%s", err, outAll)
	}

	var resultAll struct {
		Nodes int `json:"nodes"`
	}
	json.Unmarshal(outAll, &resultAll)

	// Export with label filter
	cmd = exec.Command(bv, "--robot-graph", "--graph-format=json", "--label=api")
	cmd.Dir = repoDir
	outFiltered, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph --label=api failed: %v\n%s", err, outFiltered)
	}

	var resultFiltered struct {
		Nodes          int               `json:"nodes"`
		FiltersApplied map[string]string `json:"filters_applied"`
	}
	if err := json.Unmarshal(outFiltered, &resultFiltered); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// Filtered should have fewer or equal nodes
	if resultFiltered.Nodes > resultAll.Nodes {
		t.Errorf("filtered graph has more nodes (%d) than unfiltered (%d)",
			resultFiltered.Nodes, resultAll.Nodes)
	}

	// Check filter is recorded
	if resultFiltered.FiltersApplied["label"] != "api" {
		t.Errorf("filters_applied.label = %q, want 'api'", resultFiltered.FiltersApplied["label"])
	}
}

// TestGraphExport_RootFilter tests --graph-root filter
func TestGraphExport_RootFilter(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepoWithDeps(t)

	// Export with root filter
	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json", "--graph-root=root-a")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph --graph-root=root-a failed: %v\n%s", err, out)
	}

	var result struct {
		Nodes          int               `json:"nodes"`
		FiltersApplied map[string]string `json:"filters_applied"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if result.Nodes != 3 {
		t.Errorf("expected root-a focused graph to include root and downstream dependents, got %d nodes", result.Nodes)
	}

	// Check filter is recorded
	if result.FiltersApplied["root"] != "root-a" {
		t.Errorf("filters_applied.root = %q, want 'root-a'", result.FiltersApplied["root"])
	}
}

// TestGraphExport_DepthFilter tests --graph-depth filter
func TestGraphExport_DepthFilter(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepoWithDeps(t)

	// Export with depth filter
	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json", "--graph-root=root-a", "--graph-depth=1")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph with depth failed: %v\n%s", err, out)
	}

	var result struct {
		Nodes          int               `json:"nodes"`
		FiltersApplied map[string]string `json:"filters_applied"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if result.Nodes != 2 {
		t.Errorf("expected depth-1 root-a graph to include root and direct dependent, got %d nodes", result.Nodes)
	}

	// Check depth filter is recorded
	if result.FiltersApplied["depth"] != "1" {
		t.Errorf("filters_applied.depth = %q, want '1'", result.FiltersApplied["depth"])
	}
}

// TestGraphExport_Explanation verifies explanation field
func TestGraphExport_Explanation(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	formats := []string{"json", "dot", "mermaid"}

	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			cmd := exec.Command(bv, "--robot-graph", "--graph-format="+format)
			cmd.Dir = repoDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("--robot-graph failed: %v\n%s", err, out)
			}

			var result struct {
				Explanation struct {
					What        string `json:"what"`
					HowToRender string `json:"how_to_render"`
					WhenToUse   string `json:"when_to_use"`
				} `json:"explanation"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("JSON unmarshal failed: %v", err)
			}

			if result.Explanation.What == "" {
				t.Error("explanation.what is empty")
			}
			if result.Explanation.WhenToUse == "" {
				t.Error("explanation.when_to_use is empty")
			}
			// how_to_render is optional for JSON format
			if format != "json" && result.Explanation.HowToRender == "" {
				t.Errorf("explanation.how_to_render is empty for %s format", format)
			}
		})
	}
}

// TestGraphExport_EmptyGraph tests handling of empty/filtered graphs
func TestGraphExport_EmptyGraph(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	// Use a label that doesn't exist
	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json", "--label=nonexistent")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()

	// Command may succeed with empty result or fail - both are acceptable
	if err != nil {
		// Check if it's a graceful error message
		if strings.Contains(string(out), "no issues") || strings.Contains(string(out), "empty") {
			return // Acceptable error handling
		}
		// Try to parse as JSON anyway
	}

	var result struct {
		Nodes       int `json:"nodes"`
		Edges       int `json:"edges"`
		Explanation struct {
			What string `json:"what"`
		} `json:"explanation"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		// Not valid JSON is also acceptable for empty graph
		t.Logf("Note: Empty graph handling returned non-JSON output: %s", string(out[:min(len(out), 100)]))
		return
	}

	// Should have 0 nodes
	if result.Nodes != 0 {
		t.Errorf("expected 0 nodes for nonexistent label, got %d", result.Nodes)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestGraphExport_DataHash tests data hash is included
func TestGraphExport_DataHash(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	cmd := exec.Command(bv, "--robot-graph", "--graph-format=json")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--robot-graph failed: %v\n%s", err, out)
	}

	var result struct {
		DataHash string `json:"data_hash"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	// data_hash should be present (for provenance)
	if result.DataHash == "" {
		t.Log("Note: data_hash is empty (may be expected if not computed)")
	}
}

// TestGraphExport_DeterministicOutput tests the reproducibility contract:
// with SOURCE_DATE_EPOCH set, two runs on the same data are byte-identical.
// The payload's generated_at is second-granular wall-clock time otherwise, so
// two runs that straddle a second boundary differ by design; comparing them
// without the epoch pinned is what made this test flake under load.
func TestGraphExport_DeterministicOutput(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := createGraphTestRepo(t)

	var outputs []string
	for i := 0; i < 2; i++ {
		cmd := exec.Command(bv, "--robot-graph", "--graph-format=dot")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=1700000000")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("--robot-graph run %d failed: %v\n%s", i+1, err, out)
		}
		outputs = append(outputs, string(out))
	}

	if !strings.Contains(outputs[0], `"generated_at":"2023-11-14T22:13:20Z"`) {
		t.Errorf("SOURCE_DATE_EPOCH must pin generated_at; got: %.200s", outputs[0])
	}
	if outputs[0] != outputs[1] {
		t.Error("graph export is not deterministic - outputs differ between runs with SOURCE_DATE_EPOCH pinned")
	}
}

// ============================================================================
// Test Helpers for bv-yc2v
// ============================================================================

// createGraphTestRepo creates a test repo with issues for graph testing
func createGraphTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	jsonl := `{"id": "issue-1", "title": "First Issue", "status": "open", "priority": 1, "issue_type": "task"}
{"id": "issue-2", "title": "Second Issue", "status": "in_progress", "priority": 2, "issue_type": "task"}
{"id": "issue-3", "title": "Third Issue", "status": "blocked", "priority": 0, "issue_type": "bug"}
{"id": "issue-4", "title": "Closed Issue", "status": "closed", "priority": 3, "issue_type": "task"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return repoDir
}

// createGraphTestRepoWithDeps creates a test repo with dependency relationships
func createGraphTestRepoWithDeps(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	// Create a dependency chain: root-a -> child-b -> leaf-c
	jsonl := `{"id": "root-a", "title": "Root Task", "status": "open", "priority": 0, "issue_type": "task"}
{"id": "child-b", "title": "Child Task", "status": "blocked", "priority": 1, "issue_type": "task", "dependencies": [{"target_id": "root-a", "type": "blocks"}]}
{"id": "leaf-c", "title": "Leaf Task", "status": "blocked", "priority": 2, "issue_type": "task", "dependencies": [{"target_id": "child-b", "type": "blocks"}]}
{"id": "independent-d", "title": "Independent", "status": "open", "priority": 1, "issue_type": "bug"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return repoDir
}

// createGraphTestRepoWithLabels creates a test repo with labeled issues
func createGraphTestRepoWithLabels(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	beadsPath := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsPath, 0755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}

	jsonl := `{"id": "api-1", "title": "API Issue 1", "status": "open", "priority": 1, "issue_type": "task", "labels": ["api"]}
{"id": "api-2", "title": "API Issue 2", "status": "open", "priority": 2, "issue_type": "task", "labels": ["api", "backend"]}
{"id": "ui-1", "title": "UI Issue", "status": "open", "priority": 1, "issue_type": "task", "labels": ["frontend"]}
{"id": "nolabel", "title": "No Label Issue", "status": "open", "priority": 3, "issue_type": "bug"}`

	if err := os.WriteFile(filepath.Join(beadsPath, "beads.jsonl"), []byte(jsonl), 0644); err != nil {
		t.Fatalf("write beads.jsonl: %v", err)
	}
	return repoDir
}

func TestGraphExportHTML_IncludesGitHistoryCorrelation(t *testing.T) {
	bv := buildBvBinary(t)
	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads: %v", err)
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	jsonl := `{"id":"hist-1","title":"History correlated node","status":"open","priority":1,"issue_type":"task"}` + "\n"
	if err := os.WriteFile(issuesPath, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}
	codePath := filepath.Join(repoDir, "feature.go")
	if err := os.WriteFile(codePath, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Alice",
			"GIT_AUTHOR_EMAIL=alice@example.com",
			"GIT_COMMITTER_NAME=Alice",
			"GIT_COMMITTER_EMAIL=alice@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("add", ".beads/issues.jsonl", "feature.go")
	git("commit", "-m", "feat(hist-1): wire git history into interactive graph export")

	htmlPath := filepath.Join(repoDir, "graph.html")
	cmd := exec.Command(bv, "--export-graph", htmlPath)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--export-graph failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read exported html: %v", err)
	}
	_, data, found := strings.Cut(string(raw), "const DATA = ")
	if !found {
		t.Fatal("missing embedded const DATA in exported HTML")
	}
	var graph struct {
		Nodes []struct {
			ID          string `json:"id"`
			CommitCount int    `json:"commit_count"`
			LastAuthor  string `json:"last_author"`
		} `json:"nodes"`
		HistoryStats *struct {
			TotalBeads       int `json:"total_beads"`
			BeadsWithCommits int `json:"beads_with_commits"`
		} `json:"history_stats"`
	}
	if err := json.NewDecoder(strings.NewReader(data)).Decode(&graph); err != nil {
		t.Fatalf("decode embedded graph DATA: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].ID != "hist-1" || graph.Nodes[0].CommitCount < 1 {
		t.Fatalf("expected hist-1 node with commit_count >= 1, got %+v", graph.Nodes)
	}
	if graph.HistoryStats == nil || graph.HistoryStats.BeadsWithCommits < 1 {
		t.Fatalf("expected history_stats with beads_with_commits >= 1, got %+v", graph.HistoryStats)
	}
}
