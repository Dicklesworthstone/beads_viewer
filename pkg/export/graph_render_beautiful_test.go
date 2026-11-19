package export

import (
	"fmt"
	"html"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateUltimateHTML_EscapesTitleAndProject(t *testing.T) {
	title := `bad <title> "x"`
	project := `proj & more <name>`
	hash := `hash<bad&`

	out := generateUltimateHTML(title, hash, `{}`, 1, 1, project, "", "")

	safeTitle := html.EscapeString(title)
	safeProject := html.EscapeString(project)
	safeHash := html.EscapeString(hash)

	if !strings.Contains(out, fmt.Sprintf("<title>%s | bv Graph</title>", safeTitle)) {
		t.Fatalf("expected escaped title in <title> tag")
	}
	if !strings.Contains(out, fmt.Sprintf("<h1><span>%s</span> Graph</h1>", safeTitle)) {
		t.Fatalf("expected escaped title in header")
	}
	if !strings.Contains(out, fmt.Sprintf("Hash: %s", safeHash)) {
		t.Fatalf("expected escaped hash in footer")
	}
	if !strings.Contains(out, fmt.Sprintf("Project: %s", safeProject)) {
		t.Fatalf("expected escaped project name in footer")
	}
}

// TestGraphHTML_HasNoExternalRequests (I4): the graph export is documented
// as self-contained, so no stylesheet, script, image, or font may be fetched
// from the network. Plain hyperlinks (the footer link to the project) are
// navigation, not resources, and are allowed.
func TestGraphHTML_HasNoExternalRequests(t *testing.T) {
	out := generateUltimateHTML("t", "hash", `{}`, 1, 1, "proj", "", "")
	resource := regexp.MustCompile(`(?i)<(link|script|img|source|iframe)[^>]*\b(href|src)\s*=\s*["']?(https?:)?//`)
	if m := resource.FindString(out); m != "" {
		t.Fatalf("graph HTML fetches an external resource: %s", m)
	}
	if strings.Contains(out, "fonts.googleapis.com") || strings.Contains(out, "@import url(http") {
		t.Fatalf("graph HTML still references a web font service")
	}
}

func TestGenerateUltimateHTML_CustomStatusTypeFiltersAndKeyboardGuards(t *testing.T) {
	graphJSON := `{
		"nodes": [
			{"id": "1", "status": "deferred", "type": "chore"},
			{"id": "2", "status": "qa_review", "type": "incident<1>"},
			{"id": "3", "status": "open", "type": "task"}
		],
		"links": []
	}`
	out := generateUltimateHTML("Custom Graph", "h1", graphJSON, 3, 0, "proj", "", "")

	for _, want := range []string{
		`<option value="deferred">Deferred</option>`,
		`<option value="qa_review">Qa Review</option>`,
		`<option value="chore">Chore</option>`,
		`<option value="incident&lt;1&gt;">Incident&lt;1&gt;</option>`,
		`deferred: '#94a3b8'`,
		`['INPUT', 'SELECT', 'TEXTAREA'].includes(e.target.tagName)`,
		`e.target.isContentEditable`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generateUltimateHTML output missing %q", want)
		}
	}
}

func TestGenerateUltimateHTML_ClosedArticulationAndWithheldStatusGuards(t *testing.T) {
	graphJSON := `{
		"nodes": [
			{"id": "open-cut", "title": "Active Cut", "status": "open", "priority": 1, "type": "task", "is_articulation": true, "is_actionable": true},
			{"id": "closed-cut", "title": "Closed Cut", "status": "closed", "priority": 1, "type": "task", "is_articulation": true, "is_actionable": false},
			{"id": "eph-open", "title": "Ephemeral Open", "status": "open", "priority": 1, "type": "task", "ephemeral": true},
			{"id": "pin-open", "title": "Pinned Open", "status": "open", "priority": 1, "type": "task", "pinned": true},
			{"id": "tpl-open", "title": "Template Open", "status": "open", "priority": 1, "type": "task", "is_template": true}
		],
		"links": []
	}`
	out := generateUltimateHTML("Articulation & Withheld Guards", "h2", graphJSON, 5, 0, "proj", "", "")

	// Positive observables: active-status guard protects stat-articulation, ring glow, Cut Vertex badge, and withheld node colors.
	for _, want := range []string{
		`Boolean(n.is_template) || Boolean(n.pinned) || Boolean(n.ephemeral)`,
		`if (isNodeActionable(n)) actionable++;`,
		`document.getElementById('stat-actionable').textContent = actionable;`,
		`if (isActiveNodeStatus(n.status) && n.is_articulation) articulationCount++;`,
		`if (isActiveNodeStatus(node.status) && node.is_articulation && isHighlighted)`,
		`if (isActiveNodeStatus(node.status) && node.is_articulation) addBadge('badge-articulation', 'Cut Vertex');`,
		`if (isNodeWithheldFromReady(n)) {`,
		`function buildGraphLinksForMode(blockingOnly)`,
		`filter(l => !blockingOnly || isBlockingLinkType(l.type))`,
		`setGraphLinksMode(true);`,
		`setGraphLinksMode(false);`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("generateUltimateHTML missing guard %q", want)
		}
	}

	// Planted negative controls: unguarded articulation checks must never appear.
	for _, forbidden := range []string{
		`if (node.is_articulation && isHighlighted)`,
		`if (node.is_articulation) addBadge('badge-articulation', 'Cut Vertex');`,
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("generateUltimateHTML contains unguarded articulation check %q", forbidden)
		}
	}
}
