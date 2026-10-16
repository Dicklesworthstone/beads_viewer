package ui

import (
	"sort"
	"strings"
	"sync"
)

// KeyBinding documents one key for one focus context. Bindings feed the
// shortcuts sidebar, the help overlay, and --robot-help; they do not dispatch.
// Runtime dispatch is Model.Update and the per-view handlers (handleListKeys,
// handleBoardKeys, handleTreeKeys, ...), and the tests in keybindings_test.go
// drive those handlers directly for every documented key.
type KeyBinding struct {
	Focus    focus  // Which view/focus context this binding applies to
	Key      string // Key string (e.g., "j", "ctrl+d", "enter")
	Desc     string // Human-readable description for help display
	Category string // Grouping category (e.g., "Navigation", "Actions")
}

// KeyRegistry is the per-focus index of documented key bindings.
type KeyRegistry struct {
	mu       sync.RWMutex
	bindings map[focus][]KeyBinding // focus -> ordered bindings (for help)
}

// NewKeyRegistry creates an empty key registry ready to accept bindings.
func NewKeyRegistry() *KeyRegistry {
	return &KeyRegistry{
		bindings: make(map[focus][]KeyBinding),
	}
}

// RegisterBinding adds a single key binding to the registry.
// If the same key is registered twice for the same focus, the later
// registration replaces the earlier one.
func (r *KeyRegistry) RegisterBinding(b KeyBinding) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Track binding for help generation (replace if exists)
	existingBindings := r.bindings[b.Focus]
	found := false
	for i, existing := range existingBindings {
		if existing.Key == b.Key {
			existingBindings[i] = b
			found = true
			break
		}
	}
	if !found {
		r.bindings[b.Focus] = append(r.bindings[b.Focus], b)
	}
}

// AllBindingsForFocus returns all registered bindings for a specific focus,
// sorted by category then by key. Returns an empty slice if no bindings exist.
func (r *KeyRegistry) AllBindingsForFocus(f focus) []KeyBinding {
	r.mu.RLock()
	bindings := r.bindings[f]
	if len(bindings) == 0 {
		r.mu.RUnlock()
		return []KeyBinding{}
	}

	// Return a sorted copy to avoid exposing internal state
	result := make([]KeyBinding, len(bindings))
	copy(result, bindings)
	r.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].Key < result[j].Key
	})

	return result
}

// AllBindings returns all registered bindings across all focus contexts,
// sorted by focus, then category, then key.
func (r *KeyRegistry) AllBindings() []KeyBinding {
	r.mu.RLock()
	if len(r.bindings) == 0 {
		r.mu.RUnlock()
		return []KeyBinding{}
	}

	var result []KeyBinding
	for _, bindings := range r.bindings {
		result = append(result, bindings...)
	}
	r.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		if result[i].Focus != result[j].Focus {
			return result[i].Focus < result[j].Focus
		}
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].Key < result[j].Key
	})

	return result
}

// HasBinding reports whether a binding is documented for the focus and key.
func (r *KeyRegistry) HasBinding(f focus, key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, b := range r.bindings[f] {
		if b.Key == key {
			return true
		}
	}
	return false
}

// registerKeyBindings populates the KeyRegistry from GetKeyBindingDocs so the
// shortcuts sidebar and help surfaces show exactly the documented keys for
// the focused view. Called from NewModel.
func (m *Model) registerKeyBindings() {
	if m == nil || m.keyRegistry == nil {
		return
	}

	for _, doc := range GetKeyBindingDocs() {
		for _, f := range focusesForBindingDoc(doc) {
			m.keyRegistry.RegisterBinding(KeyBinding{
				Focus:    f,
				Key:      doc.Key,
				Desc:     doc.Desc,
				Category: doc.Category,
			})
		}
	}
}

func focusesForBindingDoc(doc KeyBindingDoc) []focus {
	var focuses []focus
	seen := make(map[focus]struct{})

	addFocus := func(f focus) {
		if _, ok := seen[f]; ok {
			return
		}
		seen[f] = struct{}{}
		focuses = append(focuses, f)
	}

	for _, raw := range strings.Split(doc.Context, ",") {
		switch strings.TrimSpace(raw) {
		case "all":
			for _, f := range allDocumentedFocuses() {
				addFocus(f)
			}
		case "list":
			addFocus(focusList)
		case "detail":
			addFocus(focusDetail)
		case "board":
			addFocus(focusBoard)
		case "graph":
			addFocus(focusGraph)
		case "insights":
			addFocus(focusInsights)
		case "history":
			addFocus(focusHistory)
		case "actionable":
			addFocus(focusActionable)
		case "label", "label-dashboard":
			addFocus(focusLabelDashboard)
		case "tree":
			addFocus(focusTree)
		case "flow", "flow-matrix":
			addFocus(focusFlowMatrix)
		case "sprint":
			addFocus(focusSprint)
		case "attention":
			addFocus(focusAttention)
		}
	}

	return focuses
}

func allDocumentedFocuses() []focus {
	return []focus{
		focusList,
		focusDetail,
		focusBoard,
		focusGraph,
		focusInsights,
		focusHistory,
		focusActionable,
		focusLabelDashboard,
		focusTree,
		focusFlowMatrix,
		focusSprint,
		focusAttention,
	}
}

// KeyBindingDoc represents a key binding for documentation purposes (bv-xl6g).
type KeyBindingDoc struct {
	Key      string
	Desc     string
	Category string
	Context  string // Which view(s) this applies to
}

// GetKeyBindingDocs returns all key bindings for documentation/robot-help (bv-xl6g).
// This is separate from the registry to allow documentation even before handlers
// are registered (bv-3bsx migration).
func GetKeyBindingDocs() []KeyBindingDoc {
	// Authoritative keybinding documentation - update when bindings change
	return []KeyBindingDoc{
		// Global Navigation
		{"j", "Move down", "Navigation", "all"},
		{"k", "Move up", "Navigation", "all"},
		{"G", "Go to end", "Navigation", "all"},
		{"home", "Go to start", "Navigation", "list"},
		// gg is a 200 ms combo; only the board and tree implement it. In the
		// list a single g opens the graph view.
		{"gg", "Go to start", "Navigation", "board,tree"},
		{"ctrl+d", "Page down", "Navigation", "all"},
		{"ctrl+u", "Page up", "Navigation", "all"},
		{"enter", "Open details", "Navigation", "all"},
		{"esc", "Back/close (list: clear filters)", "Navigation", "all"},
		{"q", "Quit", "Navigation", "all"},

		// View Switching
		{"a", "Actionable view", "Views", "list,detail"},
		{"b", "Board view", "Views", "list,detail"},
		{"g", "Graph view", "Views", "list,detail"},
		{"h", "History view", "Views", "list,detail"},
		{"i", "Insights panel", "Views", "list,detail"},
		{"E", "Tree view (parent-child hierarchy)", "Views", "list,detail"},
		{"f", "Flow matrix (cross-label dependencies)", "Views", "list,detail"},
		{"P", "Sprint dashboard", "Views", "list,detail"},
		{"[", "Label dashboard", "Views", "list,detail"},
		{"]", "Attention view", "Views", "list,detail"},
		{"!", "Alerts panel", "Views", "list"},
		{"w", "Repo picker (workspace mode)", "Views", "list"},
		{"?", "Help overlay", "Views", "all"},
		{";", "Shortcuts sidebar", "Views", "all"},
		{"p", "Priority hints", "Views", "list,detail"},

		// Filters
		{"o", "Open issues only", "Filters", "list"},
		{"c", "Closed issues only", "Filters", "list"},
		{"r", "Ready (unblocked)", "Filters", "list"},
		{"l", "Label picker", "Filters", "list"},
		{"/", "Search/filter", "Filters", "list"},
		{"s", "Cycle sort mode", "Filters", "list"},
		{"S", "Sort by triage score (triage recipe)", "Filters", "list"},

		// Actions
		{"t", "Time travel (custom revision)", "Actions", "list,detail"},
		{"T", "Time travel (HEAD~5)", "Actions", "list,detail"},
		{"n", "Next changed issue (time travel)", "Actions", "list"},
		{"N", "Previous changed issue (time travel)", "Actions", "list"},
		{"x", "Export to markdown", "Actions", "list,detail"},
		{"y", "Copy issue ID", "Actions", "all"},
		{"C", "Copy full issue", "Actions", "detail"},
		{"O", "Open in $EDITOR", "Actions", "detail"},
		{"'", "Recipe picker", "Actions", "list"},
		{"U", "Self-update check", "Actions", "all"},
		{"V", "Cass sessions", "Actions", "list"},

		// Graph View
		{"hjkl", "Navigate graph", "Graph", "graph"},
		{"H", "Scroll left", "Graph", "graph"},
		{"L", "Scroll right", "Graph", "graph"},
		{"J/K", "Scroll graph vertically", "Graph", "graph"},
		{"space", "Expand/collapse dependency paths", "Graph", "graph"},
		{"PgUp", "Previous 10 nodes", "Graph", "graph"},
		{"PgDn", "Next 10 nodes", "Graph", "graph"},

		// Board View
		{"h", "Previous column", "Board", "board"},
		{"l", "Next column", "Board", "board"},
		{"H", "First column", "Board", "board"},
		{"L", "Last column", "Board", "board"},
		{"s", "Cycle swimlane mode", "Board", "board"},
		{"tab", "Toggle detail", "Board", "board"},

		// Tree View
		{"E", "Exit tree view", "Tree", "tree"},
		{"ctrl+j", "Scroll detail down", "Board", "board"},
		{"ctrl+k", "Scroll detail up", "Board", "board"},

		// Insights View
		{"h", "Previous panel", "Insights", "insights"},
		{"l", "Next panel", "Insights", "insights"},
		{"tab", "Next panel", "Insights", "insights"},
		{"shift+tab", "Previous panel", "Insights", "insights"},
		{"e", "Toggle explanations", "Insights", "insights"},
		{"x", "Calculation proof", "Insights", "insights"},
		{"m", "Heatmap toggle", "Insights", "insights"},

		// History View
		{"v", "Toggle git/bead mode", "History", "history"},
		{"tab", "Toggle focus", "History", "history"},
		{"t", "Toggle timeline pane", "History", "history"},
		{"f", "Toggle file tree", "History", "history"},
		{"J", "Detail scroll down", "History", "history"},
		{"K", "Detail scroll up", "History", "history"},
		{"o", "Open in browser", "History", "history"},

		// Attention View (])
		{"g", "Go to top", "Attention", "attention"},
		{"enter", "Label drilldown", "Attention", "attention"},
		{"1-9", "Filter list by rank", "Attention", "attention"},
		{"]", "Close attention view", "Attention", "attention"},

		// Label Dashboard ([)
		{"h", "Label health detail", "Label", "label"},
		{"d", "Label drilldown", "Label", "label"},
		{"v", "Velocity comparison", "Label", "label"},

		// Sprint Dashboard (P)
		{"P", "Close sprint dashboard", "Sprint", "sprint"},
		{"j", "Next sprint", "Sprint", "sprint"},
		{"k", "Previous sprint", "Sprint", "sprint"},
	}
}
