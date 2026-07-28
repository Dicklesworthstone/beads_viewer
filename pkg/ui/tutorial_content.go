package ui

import "github.com/charmbracelet/lipgloss"

// StructuredTutorialPage represents a tutorial page with typed elements
type StructuredTutorialPage struct {
	ID       string
	Title    string
	Section  string
	Elements []TutorialElement
	Contexts []string // Which view contexts this page applies to (empty = all)
}

// RenderStructuredPage renders a structured tutorial page
func RenderStructuredPage(page StructuredTutorialPage, theme Theme, width int) string {
	return renderElements(page.Elements, theme, width)
}

// Theme colors for status flow diagrams
var (
	colorOpen       = lipgloss.AdaptiveColor{Light: "#007700", Dark: "#50FA7B"}
	colorInProgress = lipgloss.AdaptiveColor{Light: "#006080", Dark: "#8BE9FD"}
	colorBlocked    = lipgloss.AdaptiveColor{Light: "#CC0000", Dark: "#FF5555"}
	colorClosed     = lipgloss.AdaptiveColor{Light: "#555555", Dark: "#6272A4"}
	colorPrimary    = lipgloss.AdaptiveColor{Light: "#6B47D9", Dark: "#BD93F9"}
	colorFeature    = lipgloss.AdaptiveColor{Light: "#B06800", Dark: "#FFB86C"}
)

// structuredTutorialPages returns tutorial content using the component system
func structuredTutorialPages() []StructuredTutorialPage {
	return []StructuredTutorialPage{
		// =============================================================
		// INTRODUCTION (4 pages)
		// =============================================================
		{
			ID:      "intro-welcome",
			Title:   "Welcome",
			Section: "Introduction",
			Elements: []TutorialElement{
				Section{Title: "Welcome to beads_viewer"},
				Paragraph{Text: "Issue tracking that lives in your code."},
				Spacer{Lines: 1},
				Paragraph{Text: "The problem: You're deep in flow, coding away, when you need to check an issue. You switch to a browser, navigate to your tracker, lose context, and break concentration."},
				Spacer{Lines: 1},
				Paragraph{Text: "The solution: bv brings issue tracking into your terminal, where you already work. No browser tabs. No context switching. No cloud dependencies."},
				Spacer{Lines: 1},
				Section{Title: "The 30-Second Value Proposition"},
				Bullet{Items: []string{
					"Issues live in your repo - version controlled, diffable, greppable",
					"Works offline - no internet required, no accounts to manage",
					"AI-native - designed for both humans and coding agents",
					"Zero dependencies - just a single binary and your git repo",
				}},
				Spacer{Lines: 1},
				Tip{Text: "Press -> or Space to continue"},
			},
		},
		{
			ID:      "intro-philosophy",
			Title:   "The Beads Philosophy",
			Section: "Introduction",
			Elements: []TutorialElement{
				Section{Title: "Why \"beads\"?"},
				Paragraph{Text: "Think of git commits as beads on a string - each one a discrete, meaningful step in your project's history. Issues are beads too."},
				Spacer{Lines: 1},
				Section{Title: "Core Principles"},
				Spacer{Lines: 1},
				ValueProp{Icon: "①", Text: "Issues as First-Class Citizens - Your .beads/ directory gets the same git treatment as code: branching, merging, history."},
				Spacer{Lines: 1},
				ValueProp{Icon: "②", Text: "No External Dependencies - No servers. No accounts. No API keys. Git + terminal = everything."},
				Spacer{Lines: 1},
				ValueProp{Icon: "③", Text: "Diffable and Greppable - Issues stored as plain JSONL. Git diff your backlog. Grep for patterns."},
				Spacer{Lines: 1},
				ValueProp{Icon: "④", Text: "Human and Agent Readable - Same data works for humans (bv) and AI agents (--robot-* flags)."},
			},
		},
		{
			ID:      "intro-audience",
			Title:   "Who Is This For?",
			Section: "Introduction",
			Elements: []TutorialElement{
				Section{Title: "Solo Developers"},
				Paragraph{Text: "Managing personal projects? Keep your TODO lists organized without heavyweight tools. Everything stays in your repo, backs up with your code."},
				Spacer{Lines: 1},
				Section{Title: "Small Teams"},
				Paragraph{Text: "Want lightweight issue tracking without subscription fees? Share your .beads/ directory through git. Everyone sees the same state."},
				Spacer{Lines: 1},
				Section{Title: "AI Coding Agents"},
				Paragraph{Text: "This is where bv shines. AI agents need structured task management. The --robot-* flags output machine-readable JSON:"},
				Spacer{Lines: 1},
				Code{Text: "bv --robot-triage    # What should I work on?\nbv --robot-plan      # How can work be parallelized?"},
				Spacer{Lines: 1},
				Section{Title: "Anyone Tired of Context-Switching"},
				Paragraph{Text: "If you've ever lost your train of thought switching between your editor and a web-based tracker, bv is for you."},
			},
		},
		{
			ID:      "intro-quickstart",
			Title:   "Quick Start",
			Section: "Introduction",
			Elements: []TutorialElement{
				Section{Title: "You're already running bv!"},
				Spacer{Lines: 1},
				Section{Title: "Basic Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "j / k", Desc: "Move down / up"},
					{Key: "Enter", Desc: "Open issue details"},
					{Key: "Esc", Desc: "Close overlay / go back"},
					{Key: "q", Desc: "Quit bv"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Switching Views"},
				KeyTable{Bindings: []KeyHint{
					{Key: "b", Desc: "Board (Kanban)"},
					{Key: "g", Desc: "Graph (dependencies)"},
					{Key: "i", Desc: "Insights panel"},
					{Key: "h", Desc: "History"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Getting Help"},
				KeyTable{Bindings: []KeyHint{
					{Key: "?", Desc: "Quick help overlay"},
					{Key: "Space", Desc: "This tutorial (in help)"},
					{Key: ";", Desc: "Shortcuts sidebar"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "Press t to see Table of Contents"},
			},
		},

		// =============================================================
		// CORE CONCEPTS (5 pages)
		// =============================================================
		{
			ID:      "concepts-beads",
			Title:   "What Are Beads?",
			Section: "Core Concepts",
			Elements: []TutorialElement{
				Section{Title: "A bead is a unit of work"},
				Paragraph{Text: "Think of your project's work as beads on a string - discrete items that together form the complete picture."},
				Spacer{Lines: 1},
				Section{Title: "Issue Types"},
				KeyTable{Bindings: []KeyHint{
					{Key: "bug", Desc: "Something broken that needs fixing"},
					{Key: "feature", Desc: "New functionality to add"},
					{Key: "task", Desc: "General work item"},
					{Key: "epic", Desc: "Large initiative with sub-tasks"},
					{Key: "chore", Desc: "Maintenance, cleanup, tech debt"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Storage"},
				Paragraph{Text: "Issues live in .beads/issues.jsonl - a simple JSON Lines file:"},
				Bullet{Items: []string{
					"Version controlled - branch, merge, history",
					"Diffable - see exactly what changed",
					"Greppable - search with standard tools",
				}},
			},
		},
		{
			ID:      "concepts-dependencies",
			Title:   "Dependencies & Blocking",
			Section: "Core Concepts",
			Elements: []TutorialElement{
				Section{Title: "Not all work can happen in parallel"},
				Paragraph{Text: "Some issues must wait for others. This is where dependencies come in."},
				Spacer{Lines: 1},
				Section{Title: "The Relationship"},
				StatusFlow{Steps: []FlowStep{
					{Label: "Auth Fix", Color: colorOpen},
					{Label: "Deploy", Color: colorBlocked},
				}},
				Spacer{Lines: 1},
				Paragraph{Text: "Auth Fix BLOCKS Deploy. You can't deploy until auth is fixed."},
				Spacer{Lines: 1},
				Section{Title: "Visual Indicators"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Red", Desc: "Blocked - waiting on something"},
					{Key: "Green", Desc: "Ready - no blockers, can start"},
					{Key: "->", Desc: "Shows what this issue blocks"},
					{Key: "<-", Desc: "Shows what blocks this issue"},
				}},
				Spacer{Lines: 1},
				Section{Title: "The Ready Filter"},
				Paragraph{Text: "Press r to filter to ready issues: Open + Zero Blockers"},
				Spacer{Lines: 1},
				Tip{Text: "Start your day with 'br ready' to see actionable work"},
			},
		},
		{
			ID:      "concepts-labels",
			Title:   "Labels & Organization",
			Section: "Core Concepts",
			Elements: []TutorialElement{
				Section{Title: "Flexible categorization"},
				Paragraph{Text: "Labels provide flexible categorization that cuts across types and priorities."},
				Spacer{Lines: 1},
				Section{Title: "Common Label Patterns"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Area", Desc: "frontend, backend, api, database"},
					{Key: "Owner", Desc: "team-alpha, @alice, contractor"},
					{Key: "Scope", Desc: "mvp, v2, tech-debt, nice-to-have"},
					{Key: "State", Desc: "needs-review, blocked-external"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Working with Labels"},
				KeyTable{Bindings: []KeyHint{
					{Key: "L", Desc: "Open label picker"},
					{Key: "Shift+L", Desc: "Filter by label"},
					{Key: "[", Desc: "Labels dashboard view"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "Keep your label set small. Too many = no one uses them."},
			},
		},
		{
			ID:      "concepts-priorities",
			Title:   "Priorities & Status",
			Section: "Core Concepts",
			Elements: []TutorialElement{
				Section{Title: "How important? Where in the workflow?"},
				Spacer{Lines: 1},
				Section{Title: "Priority Levels"},
				KeyTable{Bindings: []KeyHint{
					{Key: "P0", Desc: "Critical/emergency - drop everything"},
					{Key: "P1", Desc: "High priority - this sprint/week"},
					{Key: "P2", Desc: "Medium - this cycle/month"},
					{Key: "P3", Desc: "Low - when you have time"},
					{Key: "P4", Desc: "Backlog - someday/maybe"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Status Flow"},
				StatusFlow{Steps: []FlowStep{
					{Label: "open", Color: colorOpen},
					{Label: "in_progress", Color: colorInProgress},
					{Label: "closed", Color: colorClosed},
				}},
				Spacer{Lines: 1},
				Section{Title: "Changing Priority/Status"},
				KeyTable{Bindings: []KeyHint{
					{Key: "p", Desc: "Change priority"},
					{Key: "s", Desc: "Change status"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "If everything is P0, nothing is P0"},
			},
		},
		{
			ID:      "concepts-graph",
			Title:   "The Dependency Graph",
			Section: "Core Concepts",
			Elements: []TutorialElement{
				Section{Title: "Your issues form a directed graph"},
				Paragraph{Text: "Work flows in one direction - no cycles allowed."},
				Spacer{Lines: 1},
				Section{Title: "Example Dependency Tree"},
				Tree{
					Root: "Epic: User Auth (bv-001)",
					Children: []TutorialTreeNode{
						{Label: "Login Form (bv-002)", Children: []TutorialTreeNode{
							{Label: "Login Tests (bv-005)"},
						}},
						{Label: "Signup Form (bv-003)", Children: []TutorialTreeNode{
							{Label: "Signup Tests (bv-006)"},
						}},
						{Label: "Password Reset (bv-004)"},
					},
				},
				Spacer{Lines: 1},
				Section{Title: "Key Insights"},
				Bullet{Items: []string{
					"Root nodes (no arrows in) → Can start immediately",
					"Leaf nodes (no arrows out) → Nothing depends on them",
					"High fan-out → Completing this unblocks many items",
					"Critical path → Longest chain = minimum time",
				}},
				Spacer{Lines: 1},
				Section{Title: "Visual Encoding"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Node size", Desc: "Priority (bigger = higher)"},
					{Key: "Green", Desc: "Closed"},
					{Key: "Blue", Desc: "In progress"},
					{Key: "Red", Desc: "Blocked"},
					{Key: "A → B", Desc: "A blocks B"},
				}},
				Spacer{Lines: 1},
				Section{Title: "What to Look For"},
				Bullet{Items: []string{
					"Bottlenecks: One issue blocking many others",
					"Parallel tracks: Independent work streams",
					"Priority inversions: Low-priority blocking high-priority",
				}},
			},
		},

		// =============================================================
		// VIEWS & NAVIGATION (8 pages)
		// =============================================================
		{
			ID:      "views-nav-fundamentals",
			Title:   "Navigation Fundamentals",
			Section: "Views",
			Elements: []TutorialElement{
				Section{Title: "Vim-style navigation throughout"},
				Paragraph{Text: "If you know vim, you're already at home. If not, you'll pick it up in minutes."},
				Spacer{Lines: 1},
				Section{Title: "Core Movement"},
				KeyTable{Bindings: []KeyHint{
					{Key: "j", Desc: "Move down"},
					{Key: "k", Desc: "Move up"},
					{Key: "h", Desc: "Move left (multi-column)"},
					{Key: "l", Desc: "Move right (multi-column)"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Jump Commands"},
				KeyTable{Bindings: []KeyHint{
					{Key: "g", Desc: "Jump to top"},
					{Key: "G", Desc: "Jump to bottom"},
					{Key: "Ctrl+d", Desc: "Half-page down"},
					{Key: "Ctrl+u", Desc: "Half-page up"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Universal Keys"},
				KeyTable{Bindings: []KeyHint{
					{Key: "?", Desc: "Help overlay"},
					{Key: "Esc", Desc: "Close / go back"},
					{Key: "Enter", Desc: "Select / open"},
					{Key: "q", Desc: "Quit bv"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "Press ; for a shortcuts sidebar that stays visible"},
			},
		},
		{
			ID:       "views-list",
			Title:    "List View",
			Section:  "Views",
			Contexts: []string{"list"},
			Elements: []TutorialElement{
				Section{Title: "Your issue inbox"},
				Paragraph{Text: "This is where you'll spend most of your time."},
				Spacer{Lines: 1},
				Section{Title: "Filtering"},
				KeyTable{Bindings: []KeyHint{
					{Key: "o", Desc: "Open issues only"},
					{Key: "c", Desc: "Closed issues only"},
					{Key: "r", Desc: "Ready (no blockers)"},
					{Key: "a", Desc: "All (reset filter)"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Searching"},
				KeyTable{Bindings: []KeyHint{
					{Key: "/", Desc: "Fuzzy search (fast, typo-tolerant)"},
					{Key: "Ctrl+S", Desc: "Semantic search (vector index)"},
					{Key: "H", Desc: "Hybrid ranking (semantic)"},
					{Key: "Alt+H", Desc: "Cycle hybrid preset"},
					{Key: "n / N", Desc: "Next / prev result"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Sorting"},
				Paragraph{Text: "Press s to cycle: priority -> created -> updated. Press S to reverse."},
				Spacer{Lines: 1},
				Tip{Text: "Filter to r (ready) and work top-down for daily triage"},
			},
		},
		{
			ID:       "views-detail",
			Title:    "Detail View",
			Section:  "Views",
			Contexts: []string{"detail"},
			Elements: []TutorialElement{
				Section{Title: "Full issue details"},
				Paragraph{Text: "Press Enter on any issue to see its full details."},
				Spacer{Lines: 1},
				Section{Title: "What You See"},
				Bullet{Items: []string{
					"Status, Priority, Type, Created date",
					"Full description with markdown rendering",
					"Dependencies (what it blocks, what blocks it)",
					"Labels and other metadata",
				}},
				Spacer{Lines: 1},
				Section{Title: "Detail View Actions"},
				KeyTable{Bindings: []KeyHint{
					{Key: "O", Desc: "Open in external editor"},
					{Key: "y", Desc: "Copy issue ID to clipboard"},
					{Key: "C", Desc: "Copy the full issue to clipboard"},
					{Key: "D", Desc: "Delete issue after confirmation"},
					{Key: "j / k", Desc: "Scroll content"},
					{Key: "Esc", Desc: "Return to list"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Markdown Support"},
				Paragraph{Text: "Descriptions render with headers, bold, code blocks, lists, and tables."},
			},
		},
		{
			ID:       "views-split",
			Title:    "Split View",
			Section:  "Views",
			Contexts: []string{"split"},
			Elements: []TutorialElement{
				Section{Title: "List and detail side by side"},
				Paragraph{Text: "Press Tab from Detail view to enter Split view."},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Tab", Desc: "Switch focus between panes"},
					{Key: "j / k", Desc: "Navigate in focused pane"},
					{Key: "Esc", Desc: "Return to full list"},
				}},
				Spacer{Lines: 1},
				Section{Title: "When to Use"},
				Bullet{Items: []string{
					"Code review: Quickly scan multiple issues",
					"Triage session: Read details without losing context",
					"Dependency analysis: Navigate while viewing relationships",
				}},
				Spacer{Lines: 1},
				Tip{Text: "Detail pane auto-updates as you navigate the list"},
			},
		},
		{
			ID:       "views-board",
			Title:    "Board View",
			Section:  "Views",
			Contexts: []string{"board"},
			Elements: []TutorialElement{
				Section{Title: "Kanban-style board"},
				Paragraph{Text: "Press b to switch to the board view."},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "h / l", Desc: "Move between columns"},
					{Key: "j / k", Desc: "Move within column"},
					{Key: "Tab", Desc: "Toggle detail panel"},
					{Key: "Enter", Desc: "View issue details"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Grouping Modes"},
				KeyTable{Bindings: []KeyHint{
					{Key: "s", Desc: "Cycle: Status -> Priority -> Type"},
					{Key: "e", Desc: "Toggle empty columns"},
					{Key: "d", Desc: "Inline card expansion"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Card Border Colors"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Red", Desc: "Has blockers"},
					{Key: "Yellow", Desc: "High-impact (blocks others)"},
					{Key: "Green", Desc: "Ready to work"},
				}},
			},
		},
		{
			ID:       "views-graph",
			Title:    "Graph View",
			Section:  "Views",
			Contexts: []string{"graph"},
			Elements: []TutorialElement{
				Section{Title: "Visualize dependencies"},
				Paragraph{Text: "Press g to see issues as a dependency graph."},
				Spacer{Lines: 1},
				Section{Title: "Reading the Graph"},
				Bullet{Items: []string{
					"Arrows point TO what's blocked (A->B = A blocks B)",
					"Node size reflects priority",
					"Color indicates status",
					"Highlighted node is your selection",
				}},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "j / k", Desc: "Navigate between nodes"},
					{Key: "h / l", Desc: "Navigate siblings"},
					{Key: "f", Desc: "Focus on subgraph"},
					{Key: "Enter", Desc: "View selected issue"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Use Cases"},
				Bullet{Items: []string{
					"Critical path analysis",
					"Dependency planning",
					"Impact assessment",
				}},
			},
		},
		{
			ID:       "views-insights",
			Title:    "Insights Panel",
			Section:  "Views",
			Contexts: []string{"insights"},
			Elements: []TutorialElement{
				Section{Title: "AI-powered prioritization"},
				Paragraph{Text: "Press i to open the Insights panel."},
				Spacer{Lines: 1},
				Section{Title: "Priority Score Factors"},
				Bullet{Items: []string{
					"Explicit priority (P0-P4)",
					"Blocking factor - how many issues it unblocks",
					"Freshness - recently updated scores higher",
					"Type weight - bugs often over features",
				}},
				Spacer{Lines: 1},
				Section{Title: "Attention Scores"},
				Paragraph{Text: "The panel highlights issues needing attention:"},
				Bullet{Items: []string{
					"Stale issues: Open too long without updates",
					"Blocked chains: Issues creating bottlenecks",
					"Priority inversions: Low blocking high",
				}},
				Spacer{Lines: 1},
				Section{Title: "Heatmap Mode"},
				Paragraph{Text: "Press m to color by attention: Red=high, Yellow=moderate, Green=on track"},
			},
		},
		{
			ID:       "views-history",
			Title:    "History View",
			Section:  "Views",
			Contexts: []string{"history"},
			Elements: []TutorialElement{
				Section{Title: "Git-integrated timeline"},
				Paragraph{Text: "Press h to see commits correlated with bead changes."},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "j / k", Desc: "Navigate timeline"},
					{Key: "v", Desc: "Toggle Bead/Git mode"},
					{Key: "f", Desc: "Toggle file tree panel"},
					{Key: "Tab", Desc: "Cycle focus"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Causality Markers"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Direct", Desc: "Commit mentions bead ID"},
					{Key: "Temporal", Desc: "Within time window"},
					{Key: "File", Desc: "Touches associated files"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Time Travel"},
				Paragraph{Text: "Press Enter on a commit to see project state at that point (read-only)."},
				Spacer{Lines: 1},
				Tip{Text: "Use t for time travel with git ref input"},
			},
		},

		// =============================================================
		// ADVANCED FEATURES (7 pages)
		// =============================================================
		{
			ID:      "advanced-semantic-search",
			Title:   "Semantic + Hybrid Search",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Find issues by meaning, then rank by importance"},
				Paragraph{Text: "Semantic search builds a local vector index from issue text so you can search by meaning without leaving the terminal."},
				Paragraph{Text: "Hybrid mode re-ranks those semantic matches using graph signals (impact, status, priority, recency), so results stay relevant while surfacing what matters most."},
				Spacer{Lines: 1},
				Section{Title: "Search Modes"},
				KeyTable{Bindings: []KeyHint{
					{Key: "/", Desc: "Fuzzy search (literal text)"},
					{Key: "Ctrl+S", Desc: "Semantic search (meaning)"},
					{Key: "H", Desc: "Hybrid ranking (meaning + graph)"},
					{Key: "Alt+H", Desc: "Cycle hybrid preset"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Example"},
				Paragraph{Text: "Searching \"permissions\":"},
				Bullet{Items: []string{
					"Fuzzy finds issues containing the word permissions",
					"Semantic finds access control, roles, authorization, ACLs",
					"Hybrid keeps those results but floats the ones with higher impact",
				}},
				Spacer{Lines: 1},
				Section{Title: "How It Stays Fast"},
				Paragraph{Text: "The index uses a weighted issue document (ID/title emphasized) so quick searches are precise. Short queries get a literal-match boost so you can type a single word and still land on the right issue."},
				Spacer{Lines: 1},
				Section{Title: "Tuning"},
				Code{Text: "BV_SEARCH_MODE=hybrid\nBV_SEARCH_PRESET=impact-first\nBV_SEARCH_WEIGHTS='{\"text\":0.4,\"pagerank\":0.2,\"status\":0.15,\"impact\":0.1,\"priority\":0.1,\"recency\":0.05}'"},
				Spacer{Lines: 1},
				Tip{Text: "Use natural language for semantic search and switch to hybrid when you want the most important matches surfaced."},
			},
		},
		{
			ID:      "advanced-time-travel",
			Title:   "Time Travel",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "See how your project looked at any point"},
				Spacer{Lines: 1},
				Section{Title: "Accessing Time Travel"},
				KeyTable{Bindings: []KeyHint{
					{Key: "t", Desc: "Full time travel with git ref input"},
					{Key: "T", Desc: "Quick travel to HEAD~5"},
					{Key: "h", Desc: "History view (visual timeline)"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Git Reference Syntax"},
				KeyTable{Bindings: []KeyHint{
					{Key: "HEAD~5", Desc: "5 commits ago"},
					{Key: "main", Desc: "Tip of main branch"},
					{Key: "v1.2.0", Desc: "Tagged release"},
					{Key: "@{2.weeks.ago}", Desc: "Two weeks back"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Use Cases"},
				Bullet{Items: []string{
					"Sprint review: What did we accomplish?",
					"Debugging: When did this get blocked?",
					"Onboarding: What was the project like 6mo ago?",
				}},
			},
		},
		{
			ID:      "advanced-label-analytics",
			Title:   "Label Analytics",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Labels are a lens for understanding"},
				Paragraph{Text: "Press [ to open the Labels dashboard."},
				Spacer{Lines: 1},
				Section{Title: "Health Indicators"},
				KeyTable{Bindings: []KeyHint{
					{Key: "OK", Desc: "Healthy - good progress, few blockers"},
					{Key: "WARN", Desc: "Warning - stale or slow velocity"},
					{Key: "CRIT", Desc: "Critical - high blocked ratio"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Health Score Factors"},
				Bullet{Items: []string{
					"Velocity: How fast are issues closing?",
					"Staleness: Are old issues piling up?",
					"Blocked ratio: What % is stuck?",
					"Work distribution: Is work spread evenly?",
				}},
				Spacer{Lines: 1},
				Section{Title: "Cross-Label Flow"},
				Paragraph{Text: "Press f in Labels view to see which areas block others."},
			},
		},
		{
			ID:      "advanced-export",
			Title:   "Export & Deployment",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Share with non-terminal users"},
				Spacer{Lines: 1},
				Section{Title: "Quick Markdown Export"},
				Paragraph{Text: "Press x in any view to export current state to markdown. Great for Slack, email, meeting notes."},
				Spacer{Lines: 1},
				Section{Title: "Static Site Generation"},
				Code{Text: "bv --pages              # Interactive wizard\nbv --export-pages ./out # Export to directory\nbv --preview-pages ./out # Preview locally"},
				Spacer{Lines: 1},
				Section{Title: "Output Includes"},
				Bullet{Items: []string{
					"Triage recommendations",
					"Dependency graph visualization",
					"Full-text search",
					"Works offline - no server required",
				}},
				Spacer{Lines: 1},
				Section{Title: "Exported Graph Actions"},
				Paragraph{Text: "Right-click a bead in the exported HTML graph to copy either its ID or its description."},
				Spacer{Lines: 1},
				Tip{Text: "Deploy to GitHub Pages or Cloudflare Pages"},
			},
		},
		{
			ID:      "advanced-workspace",
			Title:   "Workspace Mode",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Multiple repos, unified view"},
				Spacer{Lines: 1},
				Section{Title: "When to Use"},
				Bullet{Items: []string{
					"Monorepo alternatives: Multiple related repos",
					"Microservices: Track issues across services",
					"Frontend + Backend: Separate repos, unified view",
				}},
				Spacer{Lines: 1},
				Section{Title: "Setup"},
				Paragraph{Text: "Create .beads/workspace.json with repo paths and prefixes."},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "w", Desc: "Toggle workspace picker"},
					{Key: "W", Desc: "Workspace-wide search"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Cross-Repo Dependencies"},
				Paragraph{Text: "Issues can depend on issues in other repos. The graph shows these relationships."},
			},
		},
		{
			ID:      "advanced-recipes",
			Title:   "Recipes",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Saved filter combinations"},
				Paragraph{Text: "Press ' (single quote) to open the recipe picker."},
				Spacer{Lines: 1},
				Section{Title: "Built-in Recipes"},
				KeyTable{Bindings: []KeyHint{
					{Key: "Sprint Ready", Desc: "Actionable work for this sprint"},
					{Key: "Quick Wins", Desc: "Low-effort items"},
					{Key: "Blocked Review", Desc: "Stuck items needing attention"},
					{Key: "High Impact", Desc: "Top PageRank scores"},
					{Key: "Stale Items", Desc: "No updates in 2+ weeks"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Custom Recipes"},
				Paragraph{Text: "Stored in .beads/recipes.json - version controlled with your project."},
				Spacer{Lines: 1},
				Section{Title: "Filter Options"},
				Paragraph{Text: "status, labels, labels_exclude, priority_min/max, type, assignee"},
			},
		},
		{
			ID:      "advanced-ai",
			Title:   "AI Agent Integration",
			Section: "Advanced",
			Elements: []TutorialElement{
				Section{Title: "Designed for AI coding agents"},
				Spacer{Lines: 1},
				Section{Title: "Human vs Agent"},
				KeyTable{Bindings: []KeyHint{
					{Key: "bv", Desc: "Interactive TUI for humans"},
					{Key: "--robot-*", Desc: "JSON output for agents"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Key Robot Commands"},
				Code{Text: "bv --robot-triage  # The mega-command\nbv --robot-next    # Single top priority\nbv --robot-plan    # Parallel execution\nbv --robot-alerts  # Stale, inversions"},
				Spacer{Lines: 1},
				Section{Title: "Agent Workflow"},
				Bullet{Items: []string{
					"Call: bv --robot-next",
					"Claim: br update ID --status=in_progress",
					"Work: Do the implementation",
					"Complete: br close ID",
					"Repeat",
				}},
				Spacer{Lines: 1},
				Tip{Text: "Every project should have AGENTS.md explaining robot commands"},
			},
		},

		// =============================================================
		// WORKFLOWS (5 pages)
		// =============================================================
		{
			ID:      "workflow-new-feature",
			Title:   "Starting a New Feature",
			Section: "Workflows",
			Elements: []TutorialElement{
				Section{Title: "Feature implementation walkthrough"},
				Spacer{Lines: 1},
				Section{Title: "Step 1: Find Available Work"},
				Code{Text: "br ready  # Show actionable issues"},
				Paragraph{Text: "Or in bv: press r to filter to ready issues."},
				Spacer{Lines: 1},
				Section{Title: "Step 2: Review & Claim"},
				Bullet{Items: []string{
					"Enter: View full details",
					"g: See dependency graph",
					"br update ID --status=in_progress",
				}},
				Spacer{Lines: 1},
				Section{Title: "Step 3: Create Sub-Tasks"},
				Code{Text: "br create --title=\"Implement logic\" --type=task\nbr dep add bv-tests bv-endpoint"},
				Spacer{Lines: 1},
				Section{Title: "Step 4: Complete & Sync"},
				Code{Text: "br close ID\nbr sync  # Commit changes to git"},
				Spacer{Lines: 1},
				Tip{Text: "Check br ready after each close - new work may have unblocked"},
			},
		},
		{
			ID:      "workflow-bug-triage",
			Title:   "Triaging a Bug Report",
			Section: "Workflows",
			Elements: []TutorialElement{
				Section{Title: "Efficient bug triage process"},
				Spacer{Lines: 1},
				Section{Title: "Step 1: Create the Issue"},
				Code{Text: "br create --title=\"Login fails with special chars\" \\\n  --type=bug --priority=2"},
				Spacer{Lines: 1},
				Section{Title: "Step 2: Assess Severity"},
				KeyTable{Bindings: []KeyHint{
					{Key: "P0", Desc: "System down, data loss"},
					{Key: "P1", Desc: "Major feature broken"},
					{Key: "P2", Desc: "Feature degraded"},
					{Key: "P3-P4", Desc: "Minor, cosmetic"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Step 3: Add Labels"},
				Paragraph{Text: "Press L to open label picker. Select: bug, auth, user-reported"},
				Spacer{Lines: 1},
				Section{Title: "Step 4: Check for Blockers"},
				Code{Text: "br dep add bv-feature1 bv-bug1  # Feature blocked by bug"},
				Spacer{Lines: 1},
				Section{Title: "Checklist"},
				Bullet{Items: []string{
					"Create issue with descriptive title",
					"Set priority based on severity",
					"Add relevant labels",
					"Check if it blocks other work",
				}},
			},
		},
		{
			ID:      "workflow-sprint-planning",
			Title:   "Sprint Planning Session",
			Section: "Workflows",
			Elements: []TutorialElement{
				Section{Title: "Data-driven sprint decisions"},
				Spacer{Lines: 1},
				Section{Title: "Step 1: Review Health"},
				Paragraph{Text: "Press i for Insights panel. Check open/blocked counts and top blockers."},
				Spacer{Lines: 1},
				Section{Title: "Step 2: Identify Dependencies"},
				Paragraph{Text: "Press g for graph view:"},
				Bullet{Items: []string{
					"Tall chains = sequential (can't parallelize)",
					"Wide clusters = parallel opportunities",
					"Bottlenecks = single nodes blocking many",
				}},
				Spacer{Lines: 1},
				Section{Title: "Step 3: Filter to Ready Work"},
				Paragraph{Text: "Press r to show only unblocked issues."},
				Spacer{Lines: 1},
				Section{Title: "Step 4: Assign & Label"},
				Paragraph{Text: "For each sprint candidate: L -> 'sprint-42'"},
				Spacer{Lines: 1},
				Section{Title: "Step 5: Export Plan"},
				Paragraph{Text: "Press x to export filtered list to markdown."},
			},
		},
		{
			ID:      "workflow-onboarding",
			Title:   "Onboarding New Team Members",
			Section: "Workflows",
			Elements: []TutorialElement{
				Section{Title: "Fast onboarding - it's in the repo"},
				Spacer{Lines: 1},
				Section{Title: "Step 1: Clone & Run"},
				Code{Text: "git clone <repo>\ncd project\nbv  # Tutorial launches!"},
				Paragraph{Text: "No separate tool installation. No access requests."},
				Spacer{Lines: 1},
				Section{Title: "Step 2: Point to Help"},
				KeyTable{Bindings: []KeyHint{
					{Key: "?", Desc: "Quick reference overlay"},
					{Key: "`", Desc: "Full interactive tutorial"},
					{Key: ";", Desc: "Shortcuts sidebar"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Step 3: First Task"},
				Paragraph{Text: "Find a good-first-issue: Press L, filter to that label."},
				Spacer{Lines: 1},
				Section{Title: "Step 4: Walk Through Workflow"},
				Bullet{Items: []string{
					"Find: filters (o/r) and search (/)",
					"Review: Enter for details, g for graph",
					"Claim: br update ID --status=in_progress",
					"Complete: br close ID && br sync",
				}},
			},
		},
		{
			ID:      "workflow-stakeholder-review",
			Title:   "Stakeholder Review",
			Section: "Workflows",
			Elements: []TutorialElement{
				Section{Title: "Share with non-terminal users"},
				Spacer{Lines: 1},
				Section{Title: "Generate Dashboard"},
				Code{Text: "bv --pages  # Interactive wizard\n# Or direct:\nbv --export-pages ./dashboard --pages-title \"Sprint 42\""},
				Spacer{Lines: 1},
				Section{Title: "Output Includes"},
				Bullet{Items: []string{
					"Triage recommendations",
					"Dependency graph visualization",
					"Full-text search",
					"Works offline after load",
				}},
				Spacer{Lines: 1},
				Section{Title: "Sharing Options"},
				KeyTable{Bindings: []KeyHint{
					{Key: "GitHub Pages", Desc: "Use wizard for auto-deploy"},
					{Key: "Cloudflare", Desc: "Upload ./dashboard"},
					{Key: "Email", Desc: "Zip and send"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "Add to CI/CD to auto-update on each push"},
			},
		},

		// =============================================================
		// REFERENCE (1 page)
		// =============================================================
		{
			ID:      "ref-keyboard",
			Title:   "Keyboard Reference",
			Section: "Reference",
			Elements: []TutorialElement{
				Section{Title: "Global"},
				KeyTable{Bindings: []KeyHint{
					{Key: "?", Desc: "Help overlay"},
					{Key: "q", Desc: "Quit"},
					{Key: "Esc", Desc: "Close/back"},
					{Key: "b/g/i/h", Desc: "Switch views"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Navigation"},
				KeyTable{Bindings: []KeyHint{
					{Key: "j / k", Desc: "Move down/up"},
					{Key: "h / l", Desc: "Move left/right"},
					{Key: "g / G", Desc: "Top/bottom"},
					{Key: "Enter", Desc: "Select"},
				}},
				Spacer{Lines: 1},
				Section{Title: "Filtering"},
				KeyTable{Bindings: []KeyHint{
					{Key: "/", Desc: "Fuzzy search"},
					{Key: "Ctrl+S", Desc: "Semantic search"},
					{Key: "H", Desc: "Hybrid ranking"},
					{Key: "Alt+H", Desc: "Hybrid preset"},
					{Key: "o/c/r/a", Desc: "Status filter"},
				}},
				Spacer{Lines: 1},
				Tip{Text: "Press ? in any view for context-specific help"},
			},
		},
	}
}

// getStructuredPage returns a structured page by ID, or nil if not found
func getStructuredPage(id string) *StructuredTutorialPage {
	for _, page := range structuredTutorialPages() {
		if page.ID == id {
			return &page
		}
	}
	return nil
}
